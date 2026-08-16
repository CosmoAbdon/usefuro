# Furo

Self-hosted reverse tunneling (ngrok-style), open source. Host your own server on your own domain and let your team create tunnels — with multiuser auth, per-user wildcard TLS and a local request inspector with replay.

```bash
furo login furo_...
furo http 3003 --name api-orbium
# → https://api-orbium.cosmoabdon.tunnel.example.com → localhost:3003
# Inspector: http://localhost:4040
```

Why not frp/chisel/bore?

1. **Multiuser out of the box** — users, tokens, per-user subdomain namespacing (`*.<user>.<base>`).
2. **Local inspector with replay** — dashboard at `localhost:4040` with every request/response (headers + body) and one-click replay, like ngrok's.
3. **Setup DX** — one binary + ~8-line config. If the server takes more than 10 minutes you did something exotic.

## Server setup (goal: under 10 minutes)

Requirements: a Linux box with ports 443 and 7835 reachable, a domain with a wildcard record (`*.tunnel.example.com` → server IP), and a DNS provider API token for Let's Encrypt DNS-01 (Cloudflare supported).

```bash
# 1. install (or grab a release binary / use Docker below)
go install github.com/cosmoabdon/furo/server/cmd/furo-server@latest

# 2. wizard: domain, ACME e-mail, DNS token, ports → config.yml
#    (also validates that *.your-domain resolves to this machine)
furo-server init

# 3. secrets referenced by config.yml
export FURO_DNS_TOKEN=...       # DNS provider API token
export FURO_ADMIN_TOKEN=...     # web admin token (any strong secret)

# 4. first user — prints the client token ONCE and issues *.alice.<base>
furo-server user add alice --config config.yml

# 5. run
furo-server serve --config config.yml
```

Admin web UI lives at `https://<base-domain>` (login with `FURO_ADMIN_TOKEN`): create/remove users, mint/revoke tokens, watch active tunnels live.

### Docker

```bash
furo-server init          # generate config.yml first (or write it by hand)
FURO_DNS_TOKEN=... FURO_ADMIN_TOKEN=... docker compose up -d
```

### config.yml

```yaml
base_domain: tunnel.example.com
acme_email: you@example.com
dns_provider: cloudflare        # cloudflare | digitalocean | hetzner | gandi | desec
dns_token: ${FURO_DNS_TOKEN}
tls: acme                       # off | self-signed | acme
control_port: 7835
http_port: 443
admin_token: ${FURO_ADMIN_TOKEN}
data_dir: /var/lib/furo         # sqlite + certs
```

## Client setup (goal: under 2 minutes)

```bash
# install: brew tap cosmoabdon/tap && brew install furo
# or:      go install github.com/cosmoabdon/furo/client/cmd/furo@latest

furo login furo_...  --server control.tunnel.example.com:7835   # once
furo http 3000 --name web                                       # tunnel up
furo status                                                     # all tunnels on this machine
```

Multiple tunnels from one file (`furo start`, compose-style `furo.yml`):

```yaml
tunnels:
  api:
    proto: http
    port: 3003
    name: api-orbium
  web:
    port: 3000        # no name → server-generated
```

All tunnels of a process share one multiplexed connection and one inspector.

- `--name` optional — without it the server assigns a random `web7k2a`-style name.
- The tunnel URL is stable across reconnects; the client reconnects automatically with backoff and re-registers.
- Every tunnel gets a local **inspector** (`http://localhost:4040`, auto-increments if busy): live request list with filters, header/body detail with JSON pretty-print, replay straight to your local port, clear. `--no-inspector` disables it.
- Multiple terminals/machines per user work; names collide per user (`register_err name_taken`).

## Dev mode (no domain, no TLS)

```bash
go run ./server/cmd/furo-server user add alice
go run ./server/cmd/furo-server serve                 # tls off without config.yml
go run ./client/cmd/furo http 3000 --name test --token furo_... --plaintext
curl -H 'Host: test.alice.localhost' http://127.0.0.1:8080/
```

`--tls self-signed` gives real TLS locally: CA lands in `data/certs/ca.pem`, point the client at it with `--ca`.

## CLI reference

```
furo-server init                                   setup wizard → config.yml
furo-server serve  [--config config.yml]
furo-server user   add <username> | ls | rm <username>
furo-server token  add <username> [--label X] | ls <username> | revoke <hash-prefix>

furo login <token> [--server addr] [--ca file] [--insecure] [--plaintext]
furo http <port>   [--name X] [--inspector-port N] [--no-inspector] [...]
furo start         [--file furo.yml] [...]         all tunnels from furo.yml
furo status        [--inspector-port N]            tunnels of every local furo process
```

More DNS providers: anything implementing [libdns](https://github.com/libdns) works — add the import and one case in `server/internal/tlsmgr/acme.go`.

## Backups (Litestream)

State lives in one SQLite file: `<data_dir>/furo.db` (users, token hashes, reserved names — active tunnels are memory-only). Stream it to S3-compatible storage with [Litestream](https://litestream.io):

```yaml
# /etc/litestream.yml
dbs:
  - path: /var/lib/furo/furo.db
    replicas:
      - url: s3://my-bucket/furo
```

```bash
litestream replicate -config /etc/litestream.yml
```

Certificates in `<data_dir>/certs` re-issue themselves; backing them up just avoids a re-issuance burst after disaster recovery.

## Architecture

```
repo/
├── server/           Go — furo-server (control listener, Host routing, REST API, TLS via certmagic)
├── client/           Go — furo (persistent yamux session, capture proxy, inspector)
├── proto/            Control-protocol message types (NDJSON over yamux stream 0)
├── web-server/       Admin SPA (React + Vite + Tailwind) → go:embed in furo-server
└── web-inspector/    Inspector SPA (React + Vite + Tailwind) → go:embed in furo
```

One TCP+TLS connection per client, N logical tunnels multiplexed over yamux. The server opens one stream per incoming HTTP request; proxying is byte-level (`io.Copy`), so WebSocket, SSE and chunked responses stream through untouched.

Rebuilding the SPAs (dist/ is committed; only needed when changing them):

```bash
cd web-server    && npm install && npm run build
cd web-inspector && npm install && npm run build
```

## Out of scope (v1)

Generic TCP tunnels, custom user domains (CNAME), metrics/graphs, sophisticated rate limiting, inspector history on disk.

## License

MIT
