import { useCallback, useEffect, useState } from 'react'
import { createMCPServer, listDirectory, listMCPServers, setMCPToken, type DirectoryEntry, type MCPServer } from './api'

// The order categories show in; any other comes after them.
const CATEGORIES = ['Code', 'Ticketing', 'Support', 'Monitoring', 'Docs', 'Communication', 'Home']

// The connectors built into the webapp and the MCP servers picked to go
// with them, by category. An admin installs one as an MCP server: a
// connector with its token, which everyone then uses, an MCP server for
// everyone to connect by signing in.
export function DirectorySection({ canConfigure, onError }: { canConfigure: boolean; onError: (err: unknown) => void }) {
  const [entries, setEntries] = useState<DirectoryEntry[] | null>(null)
  const [servers, setServers] = useState<MCPServer[]>([])
  const refresh = useCallback(
    () => Promise.all([listDirectory(), listMCPServers()]).then(([d, s]) => (setEntries(d), setServers(s)), onError),
    [onError],
  )
  useEffect(() => {
    refresh()
  }, [refresh])

  const byCategory = new Map<string, DirectoryEntry[]>()
  for (const e of entries ?? []) byCategory.set(e.category, [...(byCategory.get(e.category) ?? []), e])
  const categories = [...byCategory.keys()].sort((a, b) => rank(a) - rank(b) || a.localeCompare(b))

  return (
    <section className="flow-section settings-directory" aria-label="Connectors">
      <h2>Connectors</h2>
      <p className="panel-note">
        Services flows work with. A built-in connector, such as GitHub's, is part of Spoletta: agents use its tools, only
        those that read unless their process picks others, and a connector process carries out one of its operations as
        a step. It takes a token an admin sets for everyone, so give it one limited to what flows need. The MCP servers
        here are each service's own, which everyone connects by signing in.
      </p>
      {entries === null && <p className="panel-note">Loading…</p>}
      {categories.map((c) => (
        <div key={c} className="directory-category">
          <h3>{c}</h3>
          <ul className="directory-list">
            {byCategory.get(c)!.map((e) => (
              <DirectoryItem
                key={e.name}
                entry={e}
                server={servers.find((s) => s.id === e.installed)}
                taken={servers.some((s) => s.name === e.name)}
                canConfigure={canConfigure}
                onChanged={refresh}
                onError={onError}
              />
            ))}
          </ul>
        </div>
      ))}
    </section>
  )
}

function rank(category: string): number {
  const i = CATEGORIES.indexOf(category)
  return i < 0 ? CATEGORIES.length : i
}

function DirectoryItem({
  entry: e,
  server,
  taken,
  canConfigure,
  onChanged,
  onError,
}: {
  entry: DirectoryEntry
  // The MCP server it is installed as, if it is.
  server: MCPServer | undefined
  // Whether a server already has its name, so installing names it otherwise.
  taken: boolean
  canConfigure: boolean
  onChanged: () => void
  onError: (err: unknown) => void
}) {
  const [url, setUrl] = useState(e.url)
  const [token, setToken] = useState('')
  const [busy, setBusy] = useState(false)
  const [open, setOpen] = useState(false)

  const install = async () => {
    setBusy(true)
    try {
      const s = await createMCPServer({
        name: taken ? `${e.name}-2` : e.name,
        url: url.trim(),
        description: e.description,
        ...(e.connector ? { connector: e.name } : { auth: e.auth }),
      })
      if (e.connector && token.trim() !== '') await setMCPToken(s.id, token.trim())
      setToken('')
      onChanged()
    } catch (err) {
      onError(err)
    } finally {
      setBusy(false)
    }
  }

  const status = server
    ? e.connector
      ? server.connected
        ? `Installed as ${server.name}, token set`
        : `Installed as ${server.name}, no token yet`
      : `Installed as ${server.name}`
    : 'Not installed'

  return (
    <li className="directory-item">
      <div className="mcp-item-head">
        <span className="mcp-item-name">{e.title}</span>
        <span className="mcp-badge">{e.connector ? 'Built in' : 'MCP server'}</span>
        <span className={`mcp-status${server && (!e.connector || server.connected) ? ' connected' : ''}`}>{status}</span>
      </div>
      <span className="directory-description">{e.description}</span>
      {e.operations && (
        <>
          <button className="link-button" onClick={() => setOpen(!open)} aria-expanded={open}>
            {e.operations.length} operations{open ? ' ▾' : ' ▸'}
          </button>
          {open && (
            <ul className="directory-operations">
              {e.operations.map((o) => (
                <li key={o.name} title={o.description}>
                  <code>{o.name}</code> {o.title}
                  {o.write && <span className="mcp-badge write">changes things</span>}
                </li>
              ))}
            </ul>
          )}
        </>
      )}
      {e.docs && (
        <a className="directory-docs" href={e.docs} target="_blank" rel="noreferrer">
          The service's documentation
        </a>
      )}
      {!server && canConfigure && (
        <form
          className="directory-install"
          onSubmit={(ev) => {
            ev.preventDefault()
            if (!busy && url.trim() !== '') install()
          }}
        >
          <label className="panel-field">
            <span>{e.connector ? 'API' : 'URL'}</span>
            <input value={url} spellCheck={false} onChange={(ev) => setUrl(ev.target.value)} />
          </label>
          {e.connector && (
            <label className="panel-field">
              <span>Token</span>
              <input
                type="password"
                autoComplete="off"
                value={token}
                placeholder="Optional now; set it later under MCP servers"
                onChange={(ev) => setToken(ev.target.value)}
              />
              {e.tokenHelp && <small className="panel-note">{e.tokenHelp}</small>}
            </label>
          )}
          <button className="panel-button" type="submit" disabled={busy || url.trim() === ''}>
            Install
          </button>
        </form>
      )}
      {server && (
        <a className="directory-docs" href="#/settings/mcp">
          {e.connector ? 'Set its token under MCP servers' : 'Connect it under MCP servers'}
        </a>
      )}
    </li>
  )
}
