import { useCallback, useEffect, useState } from 'react'

type UserView = {
  username: string
  created_at: string
  token_count: number
  tunnels_online: number
}

type TokenView = {
  HashPrefix: string
  Label: string
  CreatedAt: string
  Revoked: boolean
}

type TunnelView = {
  username: string
  name: string
  url: string
  uptime_seconds: number
}

function fmtUptime(s: number): string {
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`
  return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`
}

function Copyable({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      onClick={() => {
        navigator.clipboard.writeText(text)
        setCopied(true)
        setTimeout(() => setCopied(false), 1500)
      }}
      className="font-mono text-xs bg-zinc-900 border border-zinc-700 rounded px-2 py-1 hover:border-emerald-600 text-left break-all"
      title="click to copy"
    >
      {text} {copied ? <span className="text-emerald-400">✓ copied</span> : <span className="text-zinc-600">⧉</span>}
    </button>
  )
}

type Api = <T>(path: string, init?: RequestInit) => Promise<T>

function TokenGate({ onAuth }: { onAuth: (token: string) => void }) {
  const [value, setValue] = useState('')
  const [err, setErr] = useState('')
  const submit = async () => {
    const r = await fetch('/api/users', { headers: { Authorization: `Bearer ${value}` } })
    if (r.ok) onAuth(value)
    else setErr(r.status === 503 ? 'admin_token not configured on the server' : 'wrong token')
  }
  return (
    <div className="h-full flex items-center justify-center">
      <div className="w-80 space-y-3">
        <h1 className="text-center font-semibold">
          <span className="text-emerald-400">furo</span> <span className="text-zinc-400">admin</span>
        </h1>
        <input
          type="password"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && submit()}
          placeholder="admin token"
          className="w-full bg-zinc-900 border border-zinc-700 rounded px-3 py-2 text-sm focus:border-emerald-600 outline-none"
          autoFocus
        />
        <button onClick={submit} className="w-full bg-emerald-800 hover:bg-emerald-700 rounded py-2 text-sm">
          enter
        </button>
        {err && <div className="text-rose-400 text-xs text-center">{err}</div>}
      </div>
    </div>
  )
}

function UsersTab({ api }: { api: Api }) {
  const [users, setUsers] = useState<UserView[]>([])
  const [newName, setNewName] = useState('')
  const [created, setCreated] = useState<{ username: string; token: string } | null>(null)
  const [err, setErr] = useState('')
  const [expanded, setExpanded] = useState<string | null>(null)
  const [tokens, setTokens] = useState<TokenView[]>([])
  const [newToken, setNewToken] = useState<string | null>(null)

  const reload = useCallback(() => api<UserView[]>('/api/users').then(setUsers).catch(() => {}), [api])
  useEffect(() => { reload() }, [reload])

  const loadTokens = useCallback(
    (u: string) => api<TokenView[]>(`/api/users/${u}/tokens`).then(setTokens).catch(() => {}),
    [api],
  )

  const create = async () => {
    setErr('')
    try {
      const r = await api<{ username: string; token: string }>('/api/users', {
        method: 'POST',
        body: JSON.stringify({ username: newName }),
      })
      setCreated(r)
      setNewName('')
      reload()
    } catch (e) {
      setErr(String(e))
    }
  }

  const remove = async (u: string) => {
    if (!confirm(`remove user ${u}? their tokens stop working immediately.`)) return
    await api(`/api/users/${u}`, { method: 'DELETE' })
    if (expanded === u) setExpanded(null)
    reload()
  }

  const addToken = async (u: string) => {
    const label = prompt('token label (e.g. "notebook", "ci")') ?? ''
    const r = await api<{ token: string }>(`/api/users/${u}/tokens`, {
      method: 'POST',
      body: JSON.stringify({ label }),
    })
    setNewToken(r.token)
    loadTokens(u)
    reload()
  }

  const revoke = async (prefix: string, u: string) => {
    await api('/api/tokens/revoke', { method: 'POST', body: JSON.stringify({ prefix }) })
    loadTokens(u)
    reload()
  }

  return (
    <div className="space-y-4">
      <div className="flex gap-2">
        <input
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && create()}
          placeholder="new username (dns label: a-z, 0-9, hyphens)"
          className="flex-1 bg-zinc-900 border border-zinc-700 rounded px-3 py-1.5 text-sm outline-none focus:border-emerald-600"
        />
        <button onClick={create} className="px-4 py-1.5 rounded bg-emerald-800 hover:bg-emerald-700 text-sm">
          create user
        </button>
      </div>
      {err && <div className="text-rose-400 text-xs">{err}</div>}
      {created && (
        <div className="border border-emerald-800 bg-emerald-950/40 rounded p-3 space-y-2">
          <div className="text-sm">
            user <b>{created.username}</b> created — token below is shown <b>only once</b>:
          </div>
          <Copyable text={created.token} />
          <button onClick={() => setCreated(null)} className="text-xs text-zinc-500 hover:text-zinc-300">dismiss</button>
        </div>
      )}

      <table className="w-full text-sm">
        <thead className="text-zinc-500 text-xs text-left">
          <tr>
            <th className="py-2 px-2 font-medium">username</th>
            <th className="py-2 px-2 font-medium">tokens</th>
            <th className="py-2 px-2 font-medium">online now</th>
            <th className="py-2 px-2 font-medium">created</th>
            <th className="py-2 px-2" />
          </tr>
        </thead>
        <tbody>
          {users.map((u) => (
            <>
              <tr key={u.username} className="border-t border-zinc-800/70 hover:bg-zinc-900/50">
                <td className="py-2 px-2 font-mono">{u.username}</td>
                <td className="py-2 px-2">{u.token_count}</td>
                <td className="py-2 px-2">
                  {u.tunnels_online > 0 ? (
                    <span className="text-emerald-400">● {u.tunnels_online}</span>
                  ) : (
                    <span className="text-zinc-600">—</span>
                  )}
                </td>
                <td className="py-2 px-2 text-zinc-500 text-xs">{u.created_at}</td>
                <td className="py-2 px-2 text-right space-x-2 whitespace-nowrap">
                  <button
                    onClick={() => {
                      const next = expanded === u.username ? null : u.username
                      setExpanded(next)
                      setNewToken(null)
                      if (next) loadTokens(next)
                    }}
                    className="text-xs px-2 py-1 rounded bg-zinc-800 hover:bg-zinc-700"
                  >
                    tokens
                  </button>
                  <button onClick={() => remove(u.username)} className="text-xs px-2 py-1 rounded bg-zinc-800 hover:bg-rose-900/70 hover:text-rose-200">
                    remove
                  </button>
                </td>
              </tr>
              {expanded === u.username && (
                <tr key={u.username + ':tokens'} className="bg-zinc-900/40">
                  <td colSpan={5} className="px-4 py-3">
                    {newToken && (
                      <div className="mb-2 space-y-1">
                        <div className="text-xs text-emerald-400">new token (shown only once):</div>
                        <Copyable text={newToken} />
                      </div>
                    )}
                    <table className="w-full text-xs">
                      <tbody>
                        {tokens.map((t) => (
                          <tr key={t.HashPrefix} className="border-t border-zinc-800/50">
                            <td className="py-1.5 pr-3 font-mono text-zinc-400">{t.HashPrefix}…</td>
                            <td className="py-1.5 pr-3">{t.Label || <span className="text-zinc-600">no label</span>}</td>
                            <td className="py-1.5 pr-3 text-zinc-500">{t.CreatedAt}</td>
                            <td className="py-1.5 pr-3">
                              {t.Revoked ? (
                                <span className="text-zinc-600">revoked</span>
                              ) : (
                                <button onClick={() => revoke(t.HashPrefix, u.username)} className="px-2 py-0.5 rounded bg-zinc-800 hover:bg-rose-900/70 hover:text-rose-200">
                                  revoke
                                </button>
                              )}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                    <button onClick={() => addToken(u.username)} className="mt-2 text-xs px-2 py-1 rounded bg-emerald-900/60 hover:bg-emerald-800 text-emerald-200">
                      + new token
                    </button>
                  </td>
                </tr>
              )}
            </>
          ))}
        </tbody>
      </table>
      {users.length === 0 && <div className="text-zinc-600 text-sm text-center py-6">no users yet — create the first one above</div>}
    </div>
  )
}

function TunnelsTab({ api }: { api: Api }) {
  const [tunnels, setTunnels] = useState<TunnelView[]>([])
  useEffect(() => {
    const load = () => api<TunnelView[]>('/api/tunnels').then(setTunnels).catch(() => {})
    load()
    const iv = setInterval(load, 5000)
    return () => clearInterval(iv)
  }, [api])

  if (tunnels.length === 0)
    return <div className="text-zinc-600 text-sm text-center py-10">no active tunnels</div>
  return (
    <table className="w-full text-sm">
      <thead className="text-zinc-500 text-xs text-left">
        <tr>
          <th className="py-2 px-2 font-medium">user</th>
          <th className="py-2 px-2 font-medium">name</th>
          <th className="py-2 px-2 font-medium">url</th>
          <th className="py-2 px-2 font-medium">uptime</th>
        </tr>
      </thead>
      <tbody>
        {tunnels.map((t) => (
          <tr key={t.username + '/' + t.name} className="border-t border-zinc-800/70 hover:bg-zinc-900/50">
            <td className="py-2 px-2 font-mono">{t.username}</td>
            <td className="py-2 px-2 font-mono">{t.name}</td>
            <td className="py-2 px-2">
              <a href={t.url} target="_blank" rel="noreferrer" className="text-sky-400 hover:underline font-mono text-xs">
                {t.url}
              </a>
            </td>
            <td className="py-2 px-2 text-zinc-400">{fmtUptime(t.uptime_seconds)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

export default function App() {
  const [token, setToken] = useState<string | null>(null) // memory only, by design
  const [tab, setTab] = useState<'users' | 'tunnels'>('users')

  const api = useCallback<Api>(
    async (path, init) => {
      const r = await fetch(path, {
        ...init,
        headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}`, ...init?.headers },
      })
      if (r.status === 401) {
        setToken(null)
        throw new Error('unauthorized')
      }
      if (!r.ok) {
        const body = await r.json().catch(() => null)
        throw new Error(body?.error ?? `HTTP ${r.status}`)
      }
      return r.json()
    },
    [token],
  )

  if (!token) return <TokenGate onAuth={setToken} />

  return (
    <div className="max-w-4xl mx-auto p-6 space-y-5">
      <header className="flex items-center gap-6">
        <h1 className="font-semibold">
          <span className="text-emerald-400">furo</span> <span className="text-zinc-400">admin</span>
        </h1>
        <nav className="flex gap-1">
          {(['users', 'tunnels'] as const).map((t) => (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={`px-3 py-1 rounded text-sm ${tab === t ? 'bg-zinc-800 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300'}`}
            >
              {t === 'users' ? 'users & tokens' : 'active tunnels'}
            </button>
          ))}
        </nav>
        <div className="flex-1" />
        <button onClick={() => setToken(null)} className="text-xs text-zinc-500 hover:text-zinc-300">
          lock
        </button>
      </header>
      {tab === 'users' ? <UsersTab api={api} /> : <TunnelsTab api={api} />}
    </div>
  )
}
