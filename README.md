# Furo

Self-hosted reverse tunneling (ngrok-style), open source. Host your own server on your own domain and let your team create tunnels.

```bash
furo login <token>
furo http 3003 --name api-orbium
# → https://api-orbium.cosmoabdon.proxy.duto.sh → localhost:3003
```

## Status

Early development. Current milestone: **M2** — multiuser (SQLite, token auth, server-generated names, heartbeat, automatic reconnection with re-register). No public TLS yet (M3).

## Layout

```
server/          Go — furo-server binary (control listener, public HTTP routing, API, TLS)
client/          Go — furo binary (CLI, persistent tunnel connection, local inspector)
proto/           Shared control-protocol message types
web-server/      Admin SPA (React + Vite + Tailwind), embedded into furo-server
web-inspector/   Inspector SPA (React + Vite + Tailwind), embedded into furo
```

## Dev quickstart (M2)

```bash
# create a user (prints the token once)
go run ./server/cmd/furo-server user add alice

# terminal 1 — server (control :7835, public HTTP :8080)
go run ./server/cmd/furo-server serve

# terminal 2 — something to expose
python3 -m http.server 3000

# terminal 3 — client (token from user add; --name optional)
go run ./client/cmd/furo http 3000 --name test --token furo_...

# then
curl -H 'Host: test.alice.localhost' http://127.0.0.1:8080/
```

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
