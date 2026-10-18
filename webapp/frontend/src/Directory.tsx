import { useCallback, useEffect, useState } from 'react'
import {
  clearMCPToken,
  connectMCPServer,
  createMCPServer,
  deleteMCPServer,
  disconnectMCPServer,
  listDirectory,
  listMCPServers,
  setMCPToken,
  updateMCPServer,
  type DirectoryEntry,
  type MCPServer,
} from './api'
import { ServiceLogo } from './logos'

// The order categories show in; any other comes after them.
const CATEGORIES = ['Code', 'Communication', 'Ticketing', 'Support', 'Docs', 'Monitoring', 'Home', 'Other']

const NAME_PATTERN = /^[A-Za-z0-9_-]{1,64}$/

// The connectors built into the webapp and the MCP servers picked to go
// with them, by category, each a card with its logo. An admin installs one
// with its Install button, then sets it up on the card: a connector's token,
// which everyone then uses, or an MCP server everyone connects by signing in.
export function DirectorySection({ canConfigure, onError }: { canConfigure: boolean; onError: (err: unknown) => void }) {
  const [entries, setEntries] = useState<DirectoryEntry[] | null>(null)
  const [servers, setServers] = useState<MCPServer[]>([])
  const refresh = useCallback(
    () => Promise.all([listDirectory(), listMCPServers()]).then(([d, s]) => (setEntries(d), setServers(s)), onError),
    [onError],
  )
  useEffect(() => {
    refresh()
    // Signing in to an MCP server, in a window of its own, connects it.
    const signedIn = (e: MessageEvent) => {
      if (e.origin === window.location.origin && e.data?.type === 'mcp-sign-in') refresh()
    }
    window.addEventListener('message', signedIn)
    return () => window.removeEventListener('message', signedIn)
  }, [refresh])

  const byCategory = new Map<string, DirectoryEntry[]>()
  for (const e of entries ?? []) byCategory.set(e.category, [...(byCategory.get(e.category) ?? []), e])
  const categories = [...byCategory.keys()].sort((a, b) => rank(a) - rank(b) || a.localeCompare(b))

  return (
    <section className="flow-section settings-directory" aria-label="Connectors">
      <h2>Connectors</h2>
      <p className="panel-note">
        Services flows work with. A built-in connector is part of Spoletta and calls the service's API with a token an
        admin sets for everyone, so give it one limited to what flows need; agents use its tools, only those that read
        unless their process picks others, and a connector process carries out one of them as a step. An MCP server is
        the service's own, which everyone connects by signing in. Install one, then set it up on its card.
      </p>
      {entries === null && <p className="panel-note">Loading…</p>}
      {categories.map((c) => (
        <div key={c} className="directory-category">
          <h3>{c}</h3>
          <ul className="directory-list">
            {byCategory.get(c)!.map((e) => (
              <DirectoryCard
                key={e.name}
                entry={e}
                installed={servers.filter((s) => (e.connector ? s.connector === e.name : !s.connector && sameURL(s.url, e.url)))}
                taken={(name) => servers.some((s) => s.name.toLowerCase() === name.toLowerCase())}
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

const sameURL = (a: string, b: string) => a.replace(/\/+$/, '') === b.replace(/\/+$/, '')

// A name for a new server: the entry's, or with a number after it if a
// server has it already.
function freeName(base: string, taken: (name: string) => boolean): string {
  if (!taken(base)) return base
  for (let i = 2; ; i++) if (!taken(`${base}-${i}`)) return `${base}-${i}`
}

function DirectoryCard({
  entry: e,
  installed,
  taken,
  canConfigure,
  onChanged,
  onError,
}: {
  entry: DirectoryEntry
  // The servers it is installed as: one at most, but for a connector
  // installed for several services, such as the HTTP API's.
  installed: MCPServer[]
  taken: (name: string) => boolean
  canConfigure: boolean
  onChanged: () => void
  onError: (err: unknown) => void
}) {
  // What the card shows beneath its description: nothing, the operations,
  // the questions an install needs answered first, or a server's setup.
  const [open, setOpen] = useState<'operations' | 'install' | number | null>(null)
  const [busy, setBusy] = useState(false)
  // Whether installing first asks where the service is, and for several,
  // what to name each.
  const asks = e.multiple || (e.connector && e.url === '')

  const install = async (name: string, url: string) => {
    setBusy(true)
    try {
      const s = await createMCPServer({
        name,
        url,
        description: e.description,
        ...(e.connector ? { connector: e.name } : { auth: e.auth }),
      })
      setOpen(s.id)
      onChanged()
    } catch (err) {
      onError(err)
    } finally {
      setBusy(false)
    }
  }

  const status =
    installed.length === 0
      ? 'Not installed'
      : installed.length > 1
        ? `Installed ${installed.length} times`
        : !installed[0].connected
          ? e.auth === 'token'
            ? 'Installed, needs its token'
            : 'Installed, not connected'
          : e.auth === 'token'
            ? 'Installed, ready'
            : 'Installed, connected'
  const ready = installed.length > 0 && installed.every((s) => s.connected)

  return (
    <li className={`directory-card${installed.length > 0 ? ' installed' : ''}`}>
      <div className="directory-card-head">
        <ServiceLogo name={e.name} title={e.title} />
        <div className="directory-card-title">
          <span className="directory-card-name">{e.title}</span>
          <span className="directory-card-kind">{e.connector ? 'Built-in connector' : 'MCP server'}</span>
        </div>
      </div>
      <span className="directory-description">{e.description}</span>
      <span className={`directory-status${installed.length === 0 ? '' : ready ? ' ready' : ' pending'}`}>{status}</span>

      {installed.length > (e.multiple ? 0 : 1) && (
        <ul className="directory-installs">
          {installed.map((s) => (
            <li key={s.id}>
              <button className="link-button" onClick={() => setOpen(open === s.id ? null : s.id)} aria-expanded={open === s.id}>
                {s.name}
              </button>
              <span className={`mcp-status${s.connected ? ' connected' : ''}`}>{s.connected ? 'ready' : 'needs its token'}</span>
            </li>
          ))}
        </ul>
      )}

      <div className="directory-actions">
        {canConfigure && (installed.length === 0 || e.multiple) && open !== 'install' && (
          <button
            className="directory-install-button"
            disabled={busy}
            onClick={() => (asks ? setOpen('install') : install(freeName(e.name, taken), e.url))}
          >
            {installed.length === 0 ? 'Install' : 'Install another'}
          </button>
        )}
        {installed.length === 1 && !e.multiple && (
          <button
            className="directory-configure-button"
            onClick={() => setOpen(open === installed[0].id ? null : installed[0].id)}
            aria-expanded={open === installed[0].id}
          >
            {open === installed[0].id ? 'Done' : canConfigure ? 'Configure' : 'Details'}
          </button>
        )}
        {!canConfigure && installed.length === 0 && <span className="panel-note">An admin installs it.</span>}
        {e.operations && (
          <button className="link-button" onClick={() => setOpen(open === 'operations' ? null : 'operations')} aria-expanded={open === 'operations'}>
            {e.operations.length} operations
          </button>
        )}
        {e.docs && (
          <a className="directory-docs" href={e.docs} target="_blank" rel="noreferrer">
            Docs
          </a>
        )}
      </div>

      {open === 'operations' && e.operations && (
        <ul className="directory-operations">
          {e.operations.map((o) => (
            <li key={o.name} title={o.description}>
              {o.title}
              {o.write && <span className="mcp-badge write">changes things</span>}
            </li>
          ))}
        </ul>
      )}
      {open === 'install' && (
        <InstallQuestions entry={e} busy={busy} name={freeName(e.name, taken)} onInstall={install} onCancel={() => setOpen(null)} />
      )}
      {typeof open === 'number' &&
        installed
          .filter((s) => s.id === open)
          .map((s) => (
            <ServerSetup
              key={s.id}
              entry={e}
              server={s}
              canConfigure={canConfigure}
              onChanged={onChanged}
              onRemoved={() => (setOpen(null), onChanged())}
              onError={onError}
            />
          ))}
    </li>
  )
}

// What installing asks first: where the service is, for one that is
// wherever it was installed, and a name, for a connector installed for
// several services.
function InstallQuestions({
  entry: e,
  busy,
  name: suggested,
  onInstall,
  onCancel,
}: {
  entry: DirectoryEntry
  busy: boolean
  name: string
  onInstall: (name: string, url: string) => void
  onCancel: () => void
}) {
  const [name, setName] = useState(e.multiple ? '' : suggested)
  const [url, setUrl] = useState(e.url)
  const ok = NAME_PATTERN.test(name.trim()) && /^https?:\/\/\S+$/.test(url.trim())
  return (
    <form
      className="directory-setup"
      onSubmit={(ev) => {
        ev.preventDefault()
        if (ok && !busy) onInstall(name.trim(), url.trim())
      }}
    >
      {e.multiple && (
        <label className="panel-field">
          <span>Name</span>
          <input value={name} spellCheck={false} placeholder="Such as billing_api" autoFocus onChange={(ev) => setName(ev.target.value)} />
          <small className="panel-note">How agents and connector processes know it: letters, digits, - and _.</small>
        </label>
      )}
      <label className="panel-field">
        <span>{e.connector ? 'API URL' : 'URL'}</span>
        <input
          value={url}
          spellCheck={false}
          placeholder={e.urlExample}
          autoFocus={!e.multiple}
          onChange={(ev) => setUrl(ev.target.value)}
        />
      </label>
      <div className="directory-setup-buttons">
        <button className="directory-install-button" type="submit" disabled={!ok || busy}>
          Install
        </button>
        <button className="link-button" type="button" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  )
}

// An installed server's setup: its token or its sign-in, its URL, and
// removing it.
function ServerSetup({
  entry: e,
  server: s,
  canConfigure,
  onChanged,
  onRemoved,
  onError,
}: {
  entry: DirectoryEntry
  server: MCPServer
  canConfigure: boolean
  onChanged: () => void
  onRemoved: () => void
  onError: (err: unknown) => void
}) {
  const [token, setToken] = useState('')
  const [url, setUrl] = useState(s.url)
  const [removing, setRemoving] = useState(false)
  const [busy, setBusy] = useState(false)
  const act = async (f: () => Promise<unknown>, after = onChanged) => {
    setBusy(true)
    try {
      await f()
      after()
    } catch (err) {
      onError(err)
    } finally {
      setBusy(false)
    }
  }
  const urlChanged = url.trim() !== s.url && /^https?:\/\/\S+$/.test(url.trim())

  return (
    <div className="directory-setup">
      {s.auth === 'token' ? (
        <>
          <div className="directory-step">
            <span className={`directory-step-mark${s.connected ? ' done' : ''}`} aria-hidden="true">
              {s.connected ? '✓' : '1'}
            </span>
            <span>{s.connected ? 'Its token is set. Setting another replaces it.' : 'Set its token, which every flow then uses.'}</span>
          </div>
          {e.tokenHelp && <small className="panel-note">{e.tokenHelp}</small>}
          {canConfigure && (
            <form
              className="mcp-token"
              onSubmit={(ev) => {
                ev.preventDefault()
                if (token.trim() !== '' && !busy) act(() => setMCPToken(s.id, token.trim()).then(() => setToken('')))
              }}
            >
              <input
                type="password"
                autoComplete="off"
                value={token}
                placeholder={e.name === 'google' ? "Paste the whole JSON key file" : s.connected ? 'A new token' : 'Token'}
                aria-label={`${e.title}'s token`}
                onChange={(ev) => setToken(ev.target.value)}
              />
              <button type="submit" disabled={busy || token.trim() === ''}>
                {s.connected ? 'Replace' : 'Save'}
              </button>
              {s.connected && (
                <button type="button" disabled={busy} onClick={() => act(() => clearMCPToken(s.id))}>
                  Clear
                </button>
              )}
            </form>
          )}
        </>
      ) : (
        <div className="directory-step">
          <span className={`directory-step-mark${s.connected ? ' done' : ''}`} aria-hidden="true">
            {s.connected ? '✓' : '1'}
          </span>
          <span>
            {s.connected ? 'You are connected. ' : 'Everyone connects it for themselves, by signing in to it. '}
            {s.connected ? (
              <button className="link-button" disabled={busy} onClick={() => act(() => disconnectMCPServer(s.id))}>
                Disconnect
              </button>
            ) : (
              <button className="link-button" onClick={() => connectMCPServer(s.id)}>
                Connect now
              </button>
            )}
          </span>
        </div>
      )}
      <div className="directory-step">
        <span className={`directory-step-mark${s.connected ? '' : ' later'}`} aria-hidden="true">
          2
        </span>
        <span>
          {e.connector
            ? `Use it: add a ${e.title} process from the board's sidebar, or pick ${s.name} among an agent process's MCP servers.`
            : `Use it: pick ${s.name} among an agent process's MCP servers.`}
        </span>
      </div>
      {canConfigure && (
        <form
          className="directory-url"
          onSubmit={(ev) => {
            ev.preventDefault()
            if (urlChanged && !busy) act(() => updateMCPServer(s.id, { url: url.trim() }))
          }}
        >
          <label className="panel-field">
            <span>{e.connector ? 'API URL' : 'URL'}</span>
            <input value={url} spellCheck={false} placeholder={e.urlExample} onChange={(ev) => setUrl(ev.target.value)} />
          </label>
          {urlChanged && (
            <button className="link-button" type="submit" disabled={busy}>
              Save the URL, which {s.auth === 'oauth' ? 'signs everyone out of it' : 'clears its token'}
            </button>
          )}
        </form>
      )}
      {canConfigure &&
        (removing ? (
          <div className="directory-remove">
            <span>Remove {s.name}? Processes using it stop working.</span>
            <button className="link-button danger" disabled={busy} onClick={() => act(() => deleteMCPServer(s.id), onRemoved)}>
              Remove
            </button>
            <button className="link-button" onClick={() => setRemoving(false)}>
              Keep it
            </button>
          </div>
        ) : (
          <button className="link-button directory-remove-button" onClick={() => setRemoving(true)}>
            Uninstall
          </button>
        ))}
    </div>
  )
}
