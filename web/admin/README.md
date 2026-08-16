# web/admin

Admin SPA (React + Vite + Tailwind), embedded into `furo-server` via `go:embed`.
Screens: users & tokens, active tunnels (with kill).

```bash
npm install && npm run build   # writes dist/ (committed)
npm run dev                    # dev server proxying /api to :8080
```
