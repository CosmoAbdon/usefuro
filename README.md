# Furo

Self-hosted reverse tunneling (ngrok-style), open source. Host your own server on your own domain and let your team create tunnels — with multiuser auth, per-user wildcard TLS and a local request inspector with replay.

```bash
furo http 3003 --name api
# → https://api.alice.tunnel.example.com → localhost:3003
# Inspector: http://localhost:4040
```

Why not frp/chisel/bore?

1. **Multiuser out of the box** — users, tokens, per-user subdomain namespacing: every tunnel lives at `<name>.<user>.<your-domain>`.
2. **Local inspector with replay** — dashboard at `localhost:4040` with every request/response (payloads, headers, copy as cURL) and one-click replay, like ngrok's.
3. **Setup DX** — one binary + ~8-line config. If the server takes more than 10 minutes you did something exotic.

---

## Install the client (any machine that creates tunnels)

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/CosmoAbdon/usefuro/main/install.sh | sh

# Windows (PowerShell)
irm https://raw.githubusercontent.com/CosmoAbdon/usefuro/main/install.ps1 | iex

# or build from source
go install github.com/cosmoabdon/usefuro/client/cmd/furo@latest
```

Update any time with `furo update`.

## Use the client

Your server admin gives you a token (`furo_...`) once. Then:

```bash
furo login furo_...  --server tunnel.example.com:7835    # saved; run once
furo http 3000 --name web                                # expose localhost:3000
furo status                                              # all tunnels on this machine
```

- `--name` is optional — without it the server assigns a random name like `web7k2a`.
- The URL is stable across reconnects; the client reconnects with backoff and re-registers by itself.
- Every run gets a local **inspector** at `http://localhost:4040` (auto-increments if busy): live request list with filters, payload/header detail, copy as cURL, replay straight to your local port. `--no-inspector` disables it.

Several tunnels at once — write a `furo.yml` and run `furo start`:

```yaml
tunnels:
  api:
    proto: http
    port: 3003
    name: custom-api   # public name; omit for a random one
  web:
    port: 3000
```

All tunnels of a process share one multiplexed connection and one inspector.

---

## Run your own server (goal: under 10 minutes)

You need: a Linux box with ports 443 and 7835 reachable, a domain with a wildcard record (`*.tunnel.example.com` AND `tunnel.example.com` → server IP, **not** proxied/CDN), and a DNS provider API token for Let's Encrypt DNS-01 (cloudflare, digitalocean, hetzner, gandi or desec).

```bash
# 1. install
curl -fsSL https://raw.githubusercontent.com/CosmoAbdon/usefuro/main/install.sh | sh -s -- --server

# 2. wizard: domain, ACME e-mail, DNS provider + token, ports → config.yml
#    (also checks that *.your-domain resolves to this machine)
furo-server init

# 3. secrets referenced by config.yml
export FURO_DNS_TOKEN=...       # DNS provider API token (e.g. Cloudflare: Zone:Read + DNS:Edit)
export FURO_ADMIN_TOKEN=...     # web admin password (any strong secret)

# 4. first user — prints their client token ONCE and issues *.alice.<your-domain>
furo-server user add alice --config config.yml

# 5. run
furo-server serve --config config.yml
```

Admin web UI at `https://tunnel.example.com` (login with `FURO_ADMIN_TOKEN`): create/remove users, mint/revoke tokens, watch and kill active tunnels.

<details>
<summary><b>config.yml reference</b></summary>

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

Any other [libdns](https://github.com/libdns) provider works — add the import and one case in `server/internal/tlsmgr/acme.go`.
</details>

<details>
<summary><b>Docker</b></summary>

```bash
furo-server init          # generate config.yml first (or write it by hand)
FURO_DNS_TOKEN=... FURO_ADMIN_TOKEN=... docker compose up -d
```
</details>

<details>
<summary><b>systemd</b></summary>

```ini
# /etc/systemd/system/furo-server.service
[Unit]
Description=furo tunnel server
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/furo-server serve --config /etc/furo/config.yml
EnvironmentFile=/etc/furo/env
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

`/etc/furo/env` (chmod 600) holds `FURO_DNS_TOKEN=...` and `FURO_ADMIN_TOKEN=...`.
</details>

<details>
<summary><b>Backups (Litestream)</b></summary>

State lives in one SQLite file: `<data_dir>/furo.db` (users, token hashes — active tunnels are memory-only). Stream it to S3-compatible storage with [Litestream](https://litestream.io):

```yaml
# /etc/litestream.yml
dbs:
  - path: /var/lib/furo/furo.db
    replicas:
      - url: s3://my-bucket/furo
```

Certificates in `<data_dir>/certs` re-issue themselves; backing them up just avoids a re-issuance burst after disaster recovery.
</details>

---

## CLI reference

```
furo login <token> [--server addr] [--ca file] [--insecure] [--plaintext]
furo http <port>   [--name X] [--inspector-port N] [--no-inspector]
furo start         [--file furo.yml]               all tunnels from furo.yml
furo status        [--inspector-port N]            tunnels of every local furo process
furo update                                        self-update to the latest release

furo-server init                                   setup wizard → config.yml
furo-server serve  [--config config.yml]
furo-server user   add <username> | ls | rm <username>
furo-server token  add <username> [--label X] | ls <username> | revoke <hash-prefix>
furo-server update                                 self-update to the latest release
```

## Dev mode (no domain, no TLS)

```bash
go run ./server/cmd/furo-server user add alice
go run ./server/cmd/furo-server serve                 # tls off without config.yml
go run ./client/cmd/furo http 3000 --name test --token furo_... --plaintext
curl -H 'Host: test.alice.localhost' http://127.0.0.1:8080/
```

`--tls self-signed` gives real TLS locally: CA lands in `data/certs/ca.pem`, point the client at it with `--ca`.

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
