# Furo

Self-hosted reverse tunneling (ngrok-style), open source. Host your own server on your own domain and let your team create tunnels.

```bash
furo login <token>
furo http 3003 --name api-orbium
# → https://api-orbium.cosmoabdon.proxy.duto.sh → localhost:3003
```

## Status

Early development. Current milestone: **M4** — local inspector: every request through the tunnel is captured (last 500, bodies capped at 1 MB) and browsable at `http://localhost:4040` — live list (SSE), header/body detail with JSON pretty-print, one-click replay straight to your local port, clear. Port auto-increments when 4040 is taken; `--no-inspector` disables it.

## Layout

```
server/          Go — furo-server binary (control listener, public HTTP routing, API, TLS)
client/          Go — furo binary (CLI, persistent tunnel connection, local inspector)
proto/           Shared control-protocol message types
web-server/      Admin SPA (React + Vite + Tailwind), embedded into furo-server
web-inspector/   Inspector SPA (React + Vite + Tailwind), embedded into furo
```

## Server quickstart (production)

Requirements: a domain with a wildcard record (`*.tunnel.example.com`) pointing
at the server, and a DNS provider API token (Cloudflare supported) for
Let's Encrypt DNS-01.

```bash
furo-server init                    # wizard → config.yml (validates wildcard DNS)
export FURO_DNS_TOKEN=...
furo-server user add alice --config config.yml   # prints token once + issues *.alice.<base>
furo-server serve --config config.yml
```

## Client quickstart

```bash
furo login furo_...  --server control.tunnel.example.com:7835
furo http 3000 --name web
# → https://web.alice.tunnel.example.com → localhost:3000
# Inspector: http://localhost:4040  (live requests, detail, replay)
```

## Dev mode (no domain, no TLS)

```bash
go run ./server/cmd/furo-server user add alice
go run ./server/cmd/furo-server serve            # tls off by default without config.yml
go run ./client/cmd/furo http 3000 --name test --token furo_... --plaintext
curl -H 'Host: test.alice.localhost' http://127.0.0.1:8080/
```

`--tls self-signed` gives real TLS locally: the CA lands in `data/certs/ca.pem`,
point the client at it with `--ca`.

Admin CLI:

```bash
furo-server user  add <username> | ls | rm <username>
furo-server token add <username> [--label X] | ls <username> | revoke <hash-prefix>
```

Tests:

```bash
go test ./...
```

## License

MIT
