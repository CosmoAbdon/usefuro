import { useCallback, useEffect, useMemo, useState } from 'react'

type Summary = {
  id: number
  tunnel: string
  method: string
  path: string
  host: string
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

function fmtTime(iso: string): string {
  const d = new Date(iso)
  return d.toLocaleTimeString('en-GB', { hour12: false })
}

function statusPill(s: number): string {
  if (s === 0) return 'bg-zinc-800 text-zinc-500'
  if (s < 300) return 'bg-emerald-950 text-emerald-400 border border-emerald-900'
  if (s < 400) return 'bg-sky-950 text-sky-400 border border-sky-900'
  if (s < 500) return 'bg-amber-950 text-amber-400 border border-amber-900'
  return 'bg-rose-950 text-rose-400 border border-rose-900'
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

function buildCurl(d: Detail): string {
  const proto = d.req_headers?.['X-Forwarded-Proto']?.[0] ?? 'https'
  const parts = [`curl -X ${d.method} '${proto}://${d.host}${d.path}'`]
  for (const [k, vs] of Object.entries(d.req_headers ?? {})) {
    if (['Content-Length', 'X-Forwarded-For', 'X-Forwarded-Host', 'X-Forwarded-Proto'].includes(k)) continue
    for (const v of vs) parts.push(`  -H '${k}: ${v.replace(/'/g, "'\\''")}'`)
  }
  const body = decodeBody(d.req_body)
  if (body && body !== '(binary)') parts.push(`  --data-raw '${body.replace(/'/g, "'\\''")}'`)
  return parts.join(' \\\n')
}

function CopyBtn({ text, label }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      onClick={() => {
        navigator.clipboard.writeText(text)
        setCopied(true)
        setTimeout(() => setCopied(false), 1200)
      }}
      className="text-[11px] px-2 py-0.5 rounded bg-zinc-800 hover:bg-zinc-700 text-zinc-400"
    >
      {copied ? '✓ copied' : (label ?? 'copy')}
    </button>
  )
}

function BodyBlock({ title, headers, b64, truncated, size }: {
  title: string
  headers: Record<string, string[]> | null
  b64: string | null
  truncated: boolean
  size: number
}) {
  const [raw, setRaw] = useState(false)
  const text = useMemo(() => decodeBody(b64), [b64])
  const pretty = useMemo(() => (isJSON(headers) ? prettyJSON(text) : null), [headers, text])
  const showPretty = pretty !== null && !raw
  return (
    <section>
      <div className="flex items-center gap-2 mb-1.5">
        <h2 className="text-[11px] uppercase tracking-widest text-zinc-500">{title}</h2>
        <span className="text-[11px] text-zinc-600">{fmtSize(size)}</span>
        <div className="flex-1" />
        {truncated && <span className="text-[11px] px-2 py-0.5 rounded bg-amber-900/40 text-amber-400 border border-amber-800">truncated at 1MB</span>}
        {pretty !== null && (
          <button onClick={() => setRaw(!raw)} className="text-[11px] px-2 py-0.5 rounded bg-zinc-800 hover:bg-zinc-700 text-zinc-400">
            {raw ? 'pretty' : 'raw'}
          </button>
        )}
        {text && <CopyBtn text={text} />}
      </div>
      {!text ? (
        <div className="text-zinc-600 text-xs italic bg-zinc-900/50 rounded px-3 py-2">empty body</div>
      ) : showPretty ? (
        <pre
          className="text-xs bg-zinc-900 rounded-lg p-3 overflow-auto max-h-[45vh] font-mono leading-relaxed"
          dangerouslySetInnerHTML={{ __html: highlightJSON(pretty!) }}
        />
      ) : (
        <pre className="text-xs bg-zinc-900 rounded-lg p-3 overflow-auto max-h-[45vh] font-mono leading-relaxed whitespace-pre-wrap break-all">{text}</pre>
      )}
    </section>
  )
}

function HeaderRow({ k, v }: { k: string; v: string }) {
  const [open, setOpen] = useState(false)
  const long = v.length > 140
  return (
    <tr className="border-b border-zinc-800/60 last:border-0 align-top">
      <td className="py-1 pr-3 text-zinc-500 font-mono whitespace-nowrap">{k}</td>
      <td
        className={`py-1 font-mono text-zinc-300 break-all ${long && !open ? 'cursor-pointer' : ''}`}
        onClick={() => long && setOpen(!open)}
        title={long && !open ? 'click to expand' : undefined}
      >
        {long && !open ? v.slice(0, 140) + '…' : v}
        {long && open && <span className="text-zinc-600 cursor-pointer select-none" onClick={() => setOpen(false)}> [collapse]</span>}
      </td>
    </tr>
  )
}

function HeadersTable({ title, h }: { title: string; h: Record<string, string[]> | null }) {
  const entries = Object.entries(h ?? {}).sort(([a], [b]) => a.localeCompare(b))
  return (
    <section>
      <div className="flex items-center gap-2 mb-1.5">
        <h2 className="text-[11px] uppercase tracking-widest text-zinc-500">{title}</h2>
        <span className="text-[11px] text-zinc-600">{entries.length}</span>
        {entries.length > 0 && (
          <CopyBtn text={entries.flatMap(([k, vs]) => vs.map((v) => `${k}: ${v}`)).join('\n')} />
        )}
      </div>
      {entries.length === 0 ? (
        <div className="text-zinc-600 text-xs italic bg-zinc-900/50 rounded px-3 py-2">no headers</div>
      ) : (
        <div className="bg-zinc-900/50 rounded-lg px-3 py-1">
          <table className="text-xs w-full">
            <tbody>
              {entries.flatMap(([k, vs]) => vs.map((v, i) => <HeaderRow key={k + i} k={k} v={v} />))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}

function DetailPanel({ det, onReplay, replaying, replayErr }: {
  det: Detail
  onReplay: () => void
  replaying: boolean
  replayErr: string
}) {
  const [tab, setTab] = useState<'payloads' | 'headers'>('payloads')
  return (
    <div className="h-full flex flex-col">
      <div className="sticky top-0 bg-zinc-950/95 backdrop-blur border-b border-zinc-800 px-4 py-3 space-y-2 z-10">
        <div className="flex items-center gap-2.5 min-w-0">
          <span className={`font-mono font-bold text-sm shrink-0 ${methodColor(det.method)}`}>{det.method}</span>
          <span className="font-mono text-sm truncate" title={det.path}>{det.path}</span>
          {det.is_replay && <span className="text-[10px] px-1.5 rounded bg-purple-900/60 text-purple-300 shrink-0">replay</span>}
          <div className="flex-1" />
          <span className={`font-mono text-xs px-2 py-0.5 rounded-full shrink-0 ${statusPill(det.status)}`}>{det.status || '—'}</span>
        </div>
        <div className="flex items-center gap-3 text-[11px] text-zinc-500">
          <span>{fmtTime(det.start)}</span>
          <span>{fmtDuration(det.duration)}</span>
          <span>↑ {fmtSize(det.req_size)} ↓ {fmtSize(det.resp_size)}</span>
          <span className="truncate">{det.host}</span>
          <div className="flex-1" />
          <CopyBtn text={buildCurl(det)} label="copy as cURL" />
          <button
            onClick={onReplay}
            disabled={replaying}
            className="text-[11px] px-2.5 py-0.5 rounded bg-emerald-800/70 hover:bg-emerald-700 text-emerald-100 disabled:opacity-50"
          >
            {replaying ? 'replaying…' : '⟳ replay'}
          </button>
        </div>
        <div className="flex gap-1">
          {(['payloads', 'headers'] as const).map((t) => (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={`px-3 py-1 rounded text-xs ${tab === t ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300'}`}
            >
              {t}
            </button>
          ))}
        </div>
        {replayErr && <div className="text-xs text-rose-400 bg-rose-950/40 border border-rose-900 rounded px-3 py-1.5">{replayErr}</div>}
      </div>

      <div className="flex-1 overflow-auto p-4 space-y-5">
        {tab === 'payloads' ? (
          <>
            <BodyBlock title="request body" headers={det.req_headers} b64={det.req_body} truncated={det.req_truncated} size={det.req_size} />
            <BodyBlock title="response body" headers={det.resp_headers} b64={det.resp_body} truncated={det.resp_truncated} size={det.resp_size} />
          </>
        ) : (
          <>
            <HeadersTable title="request headers" h={det.req_headers} />
            <HeadersTable title="response headers" h={det.resp_headers} />
          </>
        )}
      </div>
    </div>
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
      <header className="flex items-center gap-3 px-4 py-2 border-b border-zinc-800 bg-zinc-950/95 sticky top-0 z-20">
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
        <div className="w-[44%] border-r border-zinc-800 overflow-auto">
          {filtered.length === 0 ? (
            <div className="p-8 text-center text-zinc-600 text-sm">
              no requests yet — traffic through your tunnel shows up here live
            </div>
          ) : (
            <table className="w-full text-xs">
              <thead className="sticky top-0 bg-zinc-950 text-zinc-500 z-10">
                <tr className="text-left">
                  <th className="px-3 py-1.5 font-medium">time</th>
                  <th className="px-2 py-1.5 font-medium">method</th>
                  <th className="px-2 py-1.5 font-medium">path</th>
                  <th className="px-2 py-1.5 font-medium">status</th>
                  <th className="px-2 py-1.5 font-medium">dur</th>
                  <th className="px-2 py-1.5 font-medium">size</th>
                </tr>
              </thead>
              <tbody>
                {[...filtered].reverse().map((e) => (
                  <tr
                    key={e.id}
                    onClick={() => setSelected(e.id)}
                    className={`cursor-pointer border-b border-zinc-900 hover:bg-zinc-900/70 ${selected === e.id ? 'bg-zinc-900' : ''}`}
                  >
                    <td className="px-3 py-1.5 text-zinc-600 font-mono">{fmtTime(e.start)}</td>
                    <td className={`px-2 py-1.5 font-mono font-semibold ${methodColor(e.method)}`}>
                      {e.method || '—'}
                      {e.is_replay && <span className="ml-1 text-[10px] px-1 rounded bg-purple-900/60 text-purple-300">R</span>}
                    </td>
                    <td className="px-2 py-1.5 font-mono text-zinc-300 max-w-[200px] truncate" title={e.path}>{e.path || '—'}</td>
                    <td className="px-2 py-1.5">
                      <span className={`font-mono px-1.5 py-0.5 rounded-full text-[11px] ${statusPill(e.status)}`}>{e.status || '—'}</span>
                    </td>
                    <td className="px-2 py-1.5 text-zinc-500">{fmtDuration(e.duration)}</td>
                    <td className="px-2 py-1.5 text-zinc-500">{fmtSize(e.resp_size)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>

        <div className="w-[56%] min-w-0">
          {!det ? (
            <div className="h-full flex items-center justify-center text-zinc-600 text-sm">select a request</div>
          ) : (
            <DetailPanel det={det} onReplay={replay} replaying={replaying} replayErr={replayErr} />
          )}
        </div>
      </div>
    </div>
  )
}
