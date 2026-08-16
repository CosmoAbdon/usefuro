# Furo — Briefing de Implementação

> Documento para agente implementador. Contém contexto, decisões já tomadas (não reabrir sem necessidade), arquitetura, protocolo, modelo de dados e milestones.

---

## 1. O que é

**Furo** é uma ferramenta de tunneling reverso estilo ngrok, **open source e self-hosted**. Não é um produto comercial: qualquer pessoa hospeda o próprio servidor no próprio domínio e libera acesso para seu time/amigos criarem túneis.

Fluxo do usuário final:

```bash
furo login <token>          # autentica na instância
furo http 3003 --name api-orbium
# → https://api-orbium.cosmoabdon.proxy.duto.sh  →  localhost:3003
```

Diferenciais em relação a frp/chisel/bore:

1. **Multiusuário simples**: users, tokens e namespacing de subdomínios por usuário, prontos out-of-the-box.
2. **Inspector local com replay**: dashboard em `localhost:4040` mostrando cada request/response (headers + body) com botão de replay — como o ngrok, que nenhuma alternativa self-hosted oferece de forma decente.
3. **DX de setup**: servidor sobe com 1 binário + config de ~5 linhas. Se o setup levar mais de 10 minutos, falhamos.

## 2. Decisões já tomadas

| Tema | Decisão |
| --- | --- |
| Linguagem (server + client) | **Go** (goroutines + yamux; binário único; cross-compile via GoReleaser) |
| Esquema de URL | `(tunnel-name).(username).(dominio-base)` — ex.: `api-orbium.cosmoabdon.proxy.duto.sh` |
| TLS | **Wildcard por usuário, emitido sob demanda** via Let's Encrypt **DNS-01**, usando **certmagic** + providers **libdns** (`*.cosmoabdon.proxy.duto.sh` emitido quando o usuário é criado) |
| Nome do túnel | Flag `--name` / `-n`. Sem flag → **nanoID com alfabeto customizado** `a-z0-9`, tamanho 8, primeiro caractere sempre letra. Unique constraint no banco; regenerar em colisão |
| Persistência | **SQLite** (users, tokens, subdomínios reservados). Estado de túneis *ativos* fica em memória (`map` + mutex), reconstruído na reconexão. Backup via Litestream (documentar, não implementar) |
| Topologia | **2 binários**: `furo-server` (frentes 1+2+3 num processo só) e `furo` (CLI, frentes 4+4.1). SPAs embedadas via `//go:embed` |
| Frontend do server | **Entra desde o início** (não é v2) |
| Conexões | **1 conexão persistente por cliente, N túneis lógicos multiplexados** (yamux). Nunca N conexões |
| Subdomínios | Reservados por usuário: `cosmoabdon` sempre publica sob `*.cosmoabdon.<base>` |
| Licença | MIT ou Apache-2.0 |
| Domínio base | **Configurável por instância** (self-host). `proxy.duto.sh` é só a instância de referência |

## 3. Arquitetura

```
repo/
├── server/                     Go — binário furo-server
│   ├── cmd/furo-server/        main, subcomandos (serve, init, user)
│   └── internal/
│       ├── tunnel/             listener de controle, yamux, roteamento por Host
│       ├── api/                REST: users, tokens, subdomínios, sessões ativas
│       ├── store/              SQLite (database/sql + modernc.org/sqlite, sem CGO)
│       └── tlsmgr/             certmagic + libdns (emissão on-demand por usuário)
├── client/                     Go — binário furo
│   ├── cmd/furo/               main, subcomandos (login, http, start, config)
│   └── internal/
│       ├── tunnel/             conexão persistente, reconexão com backoff
│       ├── proxy/              captura req/res (fita para o inspector)
│       └── inspector/          servidor local :4040 + API de replay
├── web-server/                 SPA admin (React + Vite + Tailwind) → embed no furo-server
└── web-inspector/              SPA inspector (React + Vite + Tailwind) → embed no furo
```

**Portas do servidor** (todas configuráveis):

- `:443` — HTTPS público (tráfego dos túneis + SPA admin + API REST, roteado por Host/path)
- `:7835` — porta de controle (conexões TLS persistentes dos clientes)

## 4. Protocolo de controle (cliente ↔ servidor)

Transporte: TCP + TLS na porta de controle. Sobre a conexão, uma sessão **yamux** (`hashicorp/yamux`).

- **Stream 0 (controle)**: aberto pelo cliente logo após o handshake. Mensagens **NDJSON** (um JSON por linha) nos dois sentidos.
- **Streams de dados**: abertos **pelo servidor**, um por requisição HTTP recebida. Primeiro frame do stream é um header JSON de 1 linha; depois, bytes crus da requisição HTTP/1.1.

### Mensagens do stream de controle

```jsonc
// cliente → servidor
{"type":"auth","token":"<token>","client_version":"0.1.0"}
{"type":"register","proto":"http","name":"api-orbium","local":"127.0.0.1:3003"}
{"type":"unregister","tunnel_id":"t_..."}
{"type":"pong","ts":1723800000}

// servidor → cliente
{"type":"auth_ok","username":"cosmoabdon"}
{"type":"auth_err","reason":"invalid_token"}
{"type":"registered","tunnel_id":"t_...","url":"https://api-orbium.cosmoabdon.proxy.duto.sh"}
{"type":"register_err","name":"api-orbium","reason":"name_taken|invalid_name"}
{"type":"ping","ts":1723800000}
```

### Stream de dados (por requisição)

```
servidor abre stream →
  linha 1: {"tunnel_id":"t_...","remote_addr":"203.0.113.9:51234"}
  depois:  request HTTP/1.1 crua (io.Copy)
cliente responde no mesmo stream:
  response HTTP/1.1 crua (io.Copy)
```

### Regras

- **Heartbeat**: servidor envia `ping` a cada 30 s; sem `pong` em 90 s → encerra sessão e desregistra túneis dela.
- **Reconexão (cliente)**: backoff exponencial 1s → 2s → 4s → ... máx 30 s, com jitter. Ao reconectar, re-autentica e re-registra todos os túneis ativos automaticamente. Usuário não faz nada.
- **Streaming**: `io.Copy` nos dois sentidos, **nunca** bufferizar a resposta inteira — SSE/WebSocket/chunked precisam fluir. (Upgrade de WebSocket funciona naturalmente se o proxy for byte-level e não parser-level; validar com teste.)
- **Backpressure**: yamux já aplica flow control por stream; não adicionar buffers ilimitados em cima.
- **Validação de nomes**: `^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$` (label DNS; sem hífen nas pontas; máx 63).
- **Sessões múltiplas por usuário são permitidas** (dois terminais, ou notebook + desktop): o registry é por `(username, name)`, então cada sessão pode registrar túneis livremente; nome já em uso por *qualquer* sessão do usuário → `register_err name_taken`.

## 5. Roteamento HTTP público (servidor)

1. Requisição chega em `:443` com SNI/Host `api-orbium.cosmoabdon.proxy.duto.sh`.
2. Extrair `name` e `username` dos dois primeiros labels (relativos ao domínio base configurado).
3. Buscar no registry em memória: `(username, name) → sessão yamux do cliente`.
4. Achou → abrir stream de dados e proxear. Não achou → resposta 404 padrão do Furo ("tunnel offline") com página estática simpática.
5. Host == domínio base (sem labels extras) → serve API REST + SPA admin.

Adicionar headers de proxy no request encaminhado: `X-Forwarded-For`, `X-Forwarded-Proto: https`, `X-Forwarded-Host`.

## 6. Modelo de dados (SQLite)

```sql
CREATE TABLE users (
  id INTEGER PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,      -- label DNS válido, lowercase
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE tokens (
  id INTEGER PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,    -- SHA-256 do token; token em claro só é exibido na criação
  label TEXT,                         -- "notebook", "ci", etc.
  revoked_at TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE reserved_names (         -- nomes de túnel fixos que o usuário quer garantir (opcional na v1)
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  PRIMARY KEY (user_id, name)
);
```

Túneis ativos **não** vão no banco. Formato do token: `furo_` + 32 bytes random base62.

## 7. CLI — superfície de comandos

### `furo` (cliente)

```
furo login <token> [--server control.proxy.duto.sh:7835]   # grava em ~/.config/furo/config.yml
furo http <porta> [--name X | -n X]                        # 1 túnel; sem --name → nanoID
furo start                                                 # sobe todos os túneis do furo.yml do diretório
furo status                                                # túneis ativos + URLs
```

`furo.yml` (multi-túnel, estilo compose):

```yaml
tunnels:
  api:
    proto: http
    port: 3003
    name: api-orbium
  web:
    proto: http
    port: 3000        # sem name → aleatório
```

Com túnel ativo, o CLI imprime as URLs públicas + `Inspector: http://localhost:4040` e fica em foreground (Ctrl-C encerra e desregistra).

### `furo-server`

```
furo-server init          # wizard: domínio base, e-mail ACME, provider DNS + token, porta de controle;
                          #         valida que *.base resolve para o IP da máquina; gera config.yml
furo-server serve         # sobe tudo
furo-server user add <username>      # cria user + emite cert wildcard + imprime token (uma vez)
furo-server user ls | rm
furo-server token add <username> [--label X] | revoke <hash-prefix>
```

Config do servidor (`config.yml`):

```yaml
base_domain: proxy.duto.sh
acme_email: cosmo@example.com
dns_provider: cloudflare        # qualquer provider libdns
dns_token: ${FURO_DNS_TOKEN}
control_port: 7835
http_port: 443
admin_token: ${FURO_ADMIN_TOKEN}   # auth da SPA/API admin
data_dir: /var/lib/furo            # sqlite + certs
```

## 8. SPA admin (web-server) — escopo v1

Auth: `admin_token` (input único, guardado em memória da aba). Telas:

1. **Users** — lista (username, nº de tokens, túneis online agora), criar usuário (mostra token gerado **uma única vez**, com copy), remover.
2. **Tokens** — por usuário: label, prefixo do hash, criado em, revogar.
3. **Túneis ativos** — tempo real (polling 5 s é suficiente na v1): usuário, nome, URL, uptime da sessão.

Sem gráficos, sem métricas na v1. Visual limpo, dark mode, denso em informação.

## 9. SPA inspector (web-inspector) — escopo v1

Servida pelo CLI em `localhost:4040`. **Se a :4040 estiver ocupada** (outro processo `furo` rodando em paralelo), incrementar automaticamente (:4041, :4042...) e imprimir a URL correta no terminal — cada processo tem seu próprio inspector. Dados vêm de um ring buffer em memória do processo `furo` (ex.: últimas 500 requisições; bodies truncados em 1 MB com aviso "truncated").

1. **Lista de requisições** (tempo real via SSE): método, path, status, duração, tamanho, túnel de origem. Filtro por túnel/método/status/path.
2. **Detalhe**: request headers + body, response headers + body. Body com pretty-print JSON, toggle raw, syntax highlight básico.
3. **Replay**: reenvia a request armazenada direto para o `localhost:<porta>` do túnel (não passa pelo servidor). A nova execução entra na lista marcada como replay.
4. **Clear** do buffer.

## 10. Requisitos não-funcionais

- **Zero CGO** (sqlite via `modernc.org/sqlite`) para cross-compile limpo.
- Binários para linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64 via **GoReleaser**; Homebrew tap; `Dockerfile` + `docker-compose.yml` para o servidor.
- Logs estruturados (`log/slog`), nível configurável.
- Graceful shutdown nos dois binários (drenar streams em andamento, ~10 s de grace).
- Testes prioritários: roteamento por Host, re-registro pós-reconexão, passthrough de WebSocket/SSE, truncamento de body no inspector, validação de nomes.

## 11. Milestones (ordem de implementação)

**M1 — Túnel que funciona (sem produto):** servidor + cliente hardcoded (1 usuário fixo, 1 túnel, sem TLS público — HTTP puro numa porta alta). Provar: request externa → túnel → localhost → resposta. Incluir teste de WebSocket.

**M2 — Multiusuário:** SQLite, auth por token, `register/unregister`, roteamento por `(username, name)`, nanoID, heartbeat + reconexão com re-registro, `furo-server user/token` via CLI.

**M3 — TLS de verdade:** certmagic + libdns, emissão on-demand do wildcard por usuário no `user add`, `:443` público, `furo-server init` com validação de DNS.

**M4 — Inspector:** proxy de captura no cliente, ring buffer, SPA inspector com lista/detalhe/replay via SSE.

**M5 — SPA admin + release:** web-server embedada, GoReleaser, Dockerfile, README com quickstart de servidor (meta: <10 min) e de cliente (<2 min).

Cada milestone deve terminar em estado demonstrável de ponta a ponta.

## 12. Referências de código (estudar, não copiar cegamente)

- **chisel** (jpillora) — arquitetura cliente/servidor sobre WebSocket, código pequeno; melhor referência global.
- **frp** — referência de "como resolveram X" para subdomínios, dashboard e edge cases.
- **ngrok v1** (open source, abandonado) — blueprint do inspector + replay.
- **bore** (Rust) — protocolo mínimo, bom para sanity check do design do controle.
- **certmagic + libdns** — TLS on-demand.
- **hashicorp/yamux** — multiplexação.

## 13. Fora de escopo da v1 (não implementar)

Túnel TCP genérico (não-HTTP), domínio customizado do usuário (CNAME próprio), métricas/gráficos, rate limiting sofisticado, PSL (relevante só se virar instância pública), persistência do histórico do inspector em disco.
