# web/inspector

Inspector SPA (React + Vite + Tailwind), embedded into `furo` (client) via `go:embed`. Served at `localhost:4040`.
Request list (SSE), payload/header detail, copy as cURL, replay, clear.

```bash
npm install && npm run build   # writes dist/ (committed)
npm run dev                    # dev server proxying /api to :4040
```
