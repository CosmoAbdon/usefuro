import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

type Summary = {
  id: number
  tunnel: string
  method: string
  path: string
  status: number
  start: string
  duration: number // ns
  req_size: number
  resp_size: number
  is_replay: boolean
}

type Detail = Summary & {
  req_headers: Record<string, string[]> | null
  req_body: string | null // base64
  req_truncated: boolean
  resp_headers: Record<string, string[]> | null
  resp_body: string | null
  resp_truncated: boolean
}

function fmtDuration(ns: number): string {
  const ms = ns / 1e6
  if (ms < 1) return '<1ms'
  if (ms < 1000) return `${Math.round(ms)}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

function fmtSize(n: number): string {
  if (n < 1024) return `${n}B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}KB`
  return `${(n / (1024 * 1024)).toFixed(2)}MB`
}

function statusColor(s: number): string {
  if (s === 0) return 'text-zinc-500'
  if (s < 300) return 'text-emerald-400'
  if (s < 400) return 'text-sky-400'
  if (s < 500) return 'text-amber-400'
  return 'text-rose-400'
}

function methodColor(m: string): string {
  switch (m) {
    case 'GET': return 'text-sky-300'
    case 'POST': return 'text-emerald-300'
    case 'PUT': return 'text-amber-300'
    case 'PATCH': return 'text-orange-300'
    case 'DELETE': return 'text-rose-300'
    default: return 'text-zinc-300'
  }
}

function decodeBody(b64: string | null): string {
  if (!b64) return ''
  try {
    const bin = atob(b64)
    const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0))
    return new TextDecoder('utf-8', { fatal: false }).decode(bytes)
  } catch {
    return '(binary)'
  }
}

function isJSON(headers: Record<string, string[]> | null): boolean {
  const ct = headers?.['Content-Type']?.[0] ?? ''
  return ct.includes('json')
}

function prettyJSON(s: string): string | null {
  try {
    return JSON.stringify(JSON.parse(s), null, 2)
  } catch {
    return null
  }
}

// Minimal JSON syntax highlight: keys, strings, numbers, literals.
function highlightJSON(src: string): string {
  const esc = src.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  return esc.replace(
    /("(?:\\.|[^"\\])*")(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/g,
    (m, str, colon, lit) => {
      if (str && colon) return `<span class="text-sky-300">${str}</span>${colon}`
      if (str) return `<span class="text-emerald-300">${str}</span>`
      if (lit) return `<span class="text-purple-300">${lit}</span>`
      return `<span class="text-amber-300">${m}</span>`
    },
  )
}

function BodyView({ headers, b64, truncated }: { headers: Record<string, string[]> | null; b64: string | null; truncated: boolean }) {
  const [raw, setRaw] = useState(false)
  const text = useMemo(() => decodeBody(b64), [b64])
  const pretty = useMemo(() => (isJSON(headers) ? prettyJSON(text) : null), [headers, text])
  if (!text) return <div className="text-zinc-600 text-xs italic px-1 py-2">empty body</div>
  const showPretty = pretty !== null && !raw
  return (
    <div>
      <div className="flex items-center gap-2 mb-1">
        {pretty !== null && (
          <button onClick={() => setRaw(!raw)} className="text-[11px] px-2 py-0.5 rounded bg-zinc-800 hover:bg-zinc-700 text-zinc-400">
            {raw ? 'pretty' : 'raw'}
          </button>
        )}
        {truncated && <span className="text-[11px] px-2 py-0.5 rounded bg-amber-900/40 text-amber-400 border border-amber-800">truncated at 1MB</span>}
      </div>
      {showPretty ? (
        <pre className="text-xs bg-zinc-900 rounded p-3 overflow-auto max-h-96 font-mono leading-relaxed" dangerouslySetInnerHTML={{ __html: highlightJSON(pretty!) }} />
      ) : (
        <pre className="text-xs bg-zinc-900 rounded p-3 overflow-auto max-h-96 font-mono leading-relaxed whitespace-pre-wrap break-all">{text}</pre>
      )}
    </div>
  )
}

function Headers({ h }: { h: Record<string, string[]> | null }) {
  if (!h || Object.keys(h).length === 0) return <div className="text-zinc-600 text-xs italic px-1 py-2">no headers</div>
  return (
    <table className="text-xs w-full">
      <tbody>
        {Object.entries(h).sort(([a], [b]) => a.localeCompare(b)).map(([k, vs]) =>
          vs.map((v, i) => (
            <tr key={k + i} className="border-b border-zinc-800/60 last:border-0">
              <td className="py-1 pr-3 text-zinc-500 font-mono whitespace-nowrap align-top">{k}</td>
              <td className="py-1 font-mono text-zinc-300 break-all">{v}</td>
            </tr>
          )),
        )}
      </tbody>
    </table>
  )
}

export default function App() {
  const [list, setList] = useState<Summary[]>([])
  const [selected, setSelected] = useState<number | null>(null)
  const [det, setDet] = useState<Detail | null>(null)
  const [fTunnel, setFTunnel] = useState('')
  const [fMethod, setFMethod] = useState('')
  const [fStatus, setFStatus] = useState('')
  const [fPath, setFPath] = useState('')
  const [replaying, setReplaying] = useState(false)
  const [replayErr, setReplayErr] = useState('')
  const listRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    fetch('/api/requests').then((r) => r.json()).then(setList)
    const es = new EventSource('/api/events')
    es.onmessage = (ev) => {
      const e = JSON.parse(ev.data)
      if (e.type === 'clear') {
        setList([])
        setSelected(null)
        setDet(null)
      } else if (e.type === 'entry' && e.entry) {
        setList((prev) => [...prev.slice(-499), e.entry])
      }
    }
    return () => es.close()
  }, [])

  useEffect(() => {
    if (selected === null) { setDet(null); return }
    setReplayErr('')
    fetch(`/api/requests/${selected}`).then((r) => (r.ok ? r.json() : null)).then(setDet)
  }, [selected])

  const tunnels = useMemo(() => [...new Set(list.map((e) => e.tunnel))], [list])
  const filtered = useMemo(
    () =>
      list.filter(
        (e) =>
          (!fTunnel || e.tunnel === fTunnel) &&
          (!fMethod || e.method === fMethod) &&
          (!fStatus || String(e.status).startsWith(fStatus)) &&
          (!fPath || e.path.toLowerCase().includes(fPath.toLowerCase())),
      ),
    [list, fTunnel, fMethod, fStatus, fPath],
  )

  const replay = useCallback(async () => {
    if (selected === null) return
    setReplaying(true)
    setReplayErr('')
    const r = await fetch(`/api/replay/${selected}`, { method: 'POST' })
    if (!r.ok) setReplayErr(await r.text())
    setReplaying(false)
  }, [selected])

  const clear = useCallback(() => fetch('/api/clear', { method: 'POST' }), [])

  return (
    <div className="h-full flex flex-col">
      <header className="flex items-center gap-3 px-4 py-2 border-b border-zinc-800 bg-zinc-950/95 sticky top-0">
        <h1 className="font-semibold text-sm tracking-wide">
          <span className="text-emerald-400">furo</span> <span className="text-zinc-400">inspector</span>
        </h1>
        <div className="flex-1" />
        <select value={fTunnel} onChange={(e) => setFTunnel(e.target.value)} className="bg-zinc-900 border border-zinc-800 rounded px-2 py-1 text-xs">
          <option value="">all tunnels</option>
          {tunnels.map((t) => <option key={t} value={t}>{t}</option>)}
        </select>
        <select value={fMethod} onChange={(e) => setFMethod(e.target.value)} className="bg-zinc-900 border border-zinc-800 rounded px-2 py-1 text-xs">
          <option value="">any method</option>
          {['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'].map((m) => <option key={m}>{m}</option>)}
        </select>
        <input value={fStatus} onChange={(e) => setFStatus(e.target.value)} placeholder="status" className="bg-zinc-900 border border-zinc-800 rounded px-2 py-1 text-xs w-16" />
        <input value={fPath} onChange={(e) => setFPath(e.target.value)} placeholder="filter path…" className="bg-zinc-900 border border-zinc-800 rounded px-2 py-1 text-xs w-48" />
        <button onClick={clear} className="text-xs px-3 py-1 rounded bg-zinc-800 hover:bg-rose-900/60 hover:text-rose-200 text-zinc-300">clear</button>
      </header>

      <div className="flex-1 flex min-h-0">
        <div ref={listRef} className="w-1/2 border-r border-zinc-800 overflow-auto">
          {filtered.length === 0 ? (
            <div className="p-8 text-center text-zinc-600 text-sm">
              no requests yet — traffic through your tunnel shows up here live
            </div>
          ) : (
            <table className="w-full text-xs">
              <thead className="sticky top-0 bg-zinc-950 text-zinc-500">
                <tr className="text-left">
                  <th className="px-3 py-1.5 font-medium">method</th>
                  <th className="px-2 py-1.5 font-medium">path</th>
                  <th className="px-2 py-1.5 font-medium">status</th>
                  <th className="px-2 py-1.5 font-medium">dur</th>
                  <th className="px-2 py-1.5 font-medium">size</th>
                  <th className="px-2 py-1.5 font-medium">tunnel</th>
                </tr>
              </thead>
              <tbody>
                {[...filtered].reverse().map((e) => (
                  <tr
                    key={e.id}
                    onClick={() => setSelected(e.id)}
                    className={`cursor-pointer border-b border-zinc-900 hover:bg-zinc-900/70 ${selected === e.id ? 'bg-zinc-900' : ''}`}
                  >
                    <td className={`px-3 py-1.5 font-mono font-semibold ${methodColor(e.method)}`}>
                      {e.method || '—'}
                      {e.is_replay && <span className="ml-1 text-[10px] px-1 rounded bg-purple-900/60 text-purple-300">replay</span>}
                    </td>
                    <td className="px-2 py-1.5 font-mono text-zinc-300 max-w-[220px] truncate">{e.path || '—'}</td>
                    <td className={`px-2 py-1.5 font-mono ${statusColor(e.status)}`}>{e.status || '—'}</td>
                    <td className="px-2 py-1.5 text-zinc-500">{fmtDuration(e.duration)}</td>
                    <td className="px-2 py-1.5 text-zinc-500">{fmtSize(e.resp_size)}</td>
                    <td className="px-2 py-1.5 text-zinc-500">{e.tunnel}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>

        <div className="w-1/2 overflow-auto p-4">
          {!det ? (
            <div className="h-full flex items-center justify-center text-zinc-600 text-sm">select a request</div>
          ) : (
            <div className="space-y-5">
              <div className="flex items-center gap-3">
                <span className={`font-mono font-bold ${methodColor(det.method)}`}>{det.method}</span>
                <span className="font-mono text-sm break-all">{det.path}</span>
                <span className={`font-mono ${statusColor(det.status)}`}>{det.status || '—'}</span>
                <span className="text-zinc-500 text-xs">{fmtDuration(det.duration)}</span>
                <div className="flex-1" />
                <button
                  onClick={replay}
                  disabled={replaying}
                  className="text-xs px-3 py-1 rounded bg-emerald-800/70 hover:bg-emerald-700 text-emerald-100 disabled:opacity-50"
                >
                  {replaying ? 'replaying…' : '⟳ replay'}
                </button>
              </div>
              {replayErr && <div className="text-xs text-rose-400 bg-rose-950/40 border border-rose-900 rounded px-3 py-2">{replayErr}</div>}

              <section>
                <h2 className="text-[11px] uppercase tracking-widest text-zinc-500 mb-2">request headers</h2>
                <Headers h={det.req_headers} />
              </section>
              <section>
                <h2 className="text-[11px] uppercase tracking-widest text-zinc-500 mb-2">request body</h2>
                <BodyView headers={det.req_headers} b64={det.req_body} truncated={det.req_truncated} />
              </section>
              <section>
                <h2 className="text-[11px] uppercase tracking-widest text-zinc-500 mb-2">response headers</h2>
                <Headers h={det.resp_headers} />
              </section>
              <section>
                <h2 className="text-[11px] uppercase tracking-widest text-zinc-500 mb-2">response body</h2>
                <BodyView headers={det.resp_headers} b64={det.resp_body} truncated={det.resp_truncated} />
              </section>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
