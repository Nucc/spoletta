import { useEffect, useState } from 'react'
import { Hint } from './Hints'
import {
  clearMCPToken,
  connectMCPServer,
  createMCPServer,
  deleteMCPServer,
  disconnectMCPServer,
  listMCPTools,
  setMCPToken,
  updateMCPServer,
  type MCPAuth,
  type MCPServer,
  type MCPTool,
} from './api'
import { useConfirm } from './Confirm'
import TabLink from './TabLink'

// What pi takes as a server name; it names the tools mcp__<name>__<tool>.
const NAME_PATTERN = /^[A-Za-z0-9_-]{1,64}$/

// A name for a server from its URL: the last part of its path, such as slack
// for https://example.com/mcps/slack, if pi takes it as a name.
function nameFromURL(url: string): string {
  try {
    const last = new URL(url.trim()).pathname.split('/').filter(Boolean).pop() ?? ''
    return NAME_PATTERN.test(last) ? last : ''
  } catch {
    return ''
  }
}

// The MCP servers agents can use, in the board's side panel. Each is
// connected by signing in through the browser; an agent process then picks
// the ones it uses, in its panel.
export function MCPSettingsPanel({
  servers,
  canConfigure,
  onChanged,
  onClose,
  onError,
}: {
  servers: MCPServer[]
  // Whether whoever is signed in adds, changes and removes servers: an
  // admin, or anyone without sign-in.
  canConfigure: boolean
  // Load the servers again.
  onChanged: () => void
  onClose: () => void
  onError: (err: unknown) => void
}) {
  return (
    <aside className="panel mcp-panel" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <header className="panel-header">
        <h2>MCP servers</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close MCP servers">
          ×
        </button>
      </header>
      <Hint id="panel-mcp" className="panel-hint">
        The servers agents reach tools through; an agent picks those it uses in its panel.
      </Hint>
      <MCPServers servers={servers} canConfigure={canConfigure} onChanged={onChanged} onError={onError} />
    </aside>
  )
}

// The servers, each to connect, and for those who configure them, to change,
// remove or add.
export function MCPServers({
  servers,
  canConfigure,
  onChanged,
  onError,
}: {
  servers: MCPServer[]
  canConfigure: boolean
  onChanged: () => void
  onError: (err: unknown) => void
}) {
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [description, setDescription] = useState('')
  const [auth, setAuth] = useState<MCPAuth>('oauth')
  const [adding, setAdding] = useState(false)
  const nameOk = NAME_PATTERN.test(name.trim())
  const urlOk = /^https?:\/\/\S+$/.test(url.trim())

  const add = async () => {
    setAdding(true)
    try {
      await createMCPServer({ name: name.trim(), url: url.trim(), description: description.trim(), auth })
      setName('')
      setUrl('')
      setDescription('')
      setAuth('oauth')
      onChanged()
    } catch (err) {
      onError(err)
    } finally {
      setAdding(false)
    }
  }

  const act = (f: () => Promise<void>) => f().then(onChanged, onError)
  const [confirm, confirmDialog] = useConfirm()
  const remove = async (s: MCPServer) => {
    const yes = await confirm({
      title: `Remove ${s.name}?`,
      message: <p>Agents picking it lose it, and everyone's sign-ins to it go. This can't be undone.</p>,
      confirm: 'Remove',
    })
    if (yes) act(() => deleteMCPServer(s.id))
  }

  return (
    <>
      {confirmDialog}
      <p className="panel-note">
        Servers agents can use. Connect each by signing in, or for one taking a token, have an admin set it for
        everyone; then pick, in an agent process's panel, the ones it may use and, if you like, which of their tools.
        Agents use none unless picked, and reach them as whoever their run is for, with that person's connection.
        Built-in connectors, such as GitHub's, are added from Connectors.
      </p>
      {servers.length === 0 ? (
        <p className="panel-note">No servers yet.</p>
      ) : (
        <ul className="mcp-list">
          {servers.map((s) => (
            <MCPServerItem
              key={s.id}
              server={s}
              canConfigure={canConfigure}
              onConnect={() => connectMCPServer(s.id)}
              onDisconnect={() => act(() => disconnectMCPServer(s.id))}
              onSetToken={(token) => act(() => setMCPToken(s.id, token))}
              onClearToken={() => act(() => clearMCPToken(s.id))}
              onDescribe={(d) => act(() => updateMCPServer(s.id, { description: d }).then(() => undefined))}
              onRemove={() => remove(s)}
            />
          ))}
        </ul>
      )}
      {canConfigure && (
        <form
          className="mcp-add"
          onSubmit={(e) => {
            e.preventDefault()
            if (nameOk && urlOk && !adding) add()
          }}
        >
          <h3>Add a server</h3>
          <label className="panel-field">
            <span>URL</span>
            <input
              value={url}
              placeholder="https://example.com/mcp"
              spellCheck={false}
              onChange={(e) => {
                const v = e.target.value
                // Named after its URL, unless named by hand.
                if (name === '' || name === nameFromURL(url)) setName(nameFromURL(v))
                setUrl(v)
              }}
            />
          </label>
          <label className="panel-field">
            <span>Name</span>
            <input value={name} placeholder="slack" spellCheck={false} onChange={(e) => setName(e.target.value)} />
            {name.trim() !== '' && !nameOk && <small className="panel-warning">Letters, digits, - and _ only.</small>}
          </label>
          <label className="panel-field">
            <span>Description</span>
            <input
              value={description}
              placeholder="Optional. What it offers, which tells agents when to use it."
              onChange={(e) => setDescription(e.target.value)}
            />
          </label>
          <label className="panel-field">
            <span>Connected by</span>
            <select value={auth} onChange={(e) => setAuth(e.target.value as MCPAuth)}>
              <option value="oauth">Each person signing in</option>
              <option value="token">A token an admin sets, such as an API key</option>
            </select>
          </label>
          <button className="panel-button" type="submit" disabled={!nameOk || !urlOk || adding}>
            Add
          </button>
        </form>
      )}
    </>
  )
}

function MCPServerItem({
  server: s,
  canConfigure,
  onConnect,
  onDisconnect,
  onSetToken,
  onClearToken,
  onDescribe,
  onRemove,
}: {
  server: MCPServer
  canConfigure: boolean
  onConnect: () => void
  onDisconnect: () => void
  onSetToken: (token: string) => void
  onClearToken: () => void
  onDescribe: (description: string) => void
  onRemove: () => void
}) {
  const [description, setDescription] = useState(s.description)
  const [token, setToken] = useState('')
  const save = () => description.trim() !== s.description && onDescribe(description.trim())
  const takesToken = s.auth === 'token'
  return (
    <li className="mcp-item">
      <div className="mcp-item-head">
        <span className="mcp-item-name">{s.name}</span>
        {s.connector && <span className="mcp-badge">Built-in connector</span>}
        <span className={`mcp-status${s.connected ? ' connected' : ''}`}>
          {takesToken ? (s.connected ? 'Token set' : 'No token') : s.connected ? 'Connected' : 'Not connected'}
        </span>
      </div>
      <span className="mcp-item-url" title={s.url}>
        {s.url}
      </span>
      {canConfigure ? (
        <input
          className="mcp-item-description"
          value={description}
          placeholder="Add a description…"
          aria-label={`Description of ${s.name}`}
          onChange={(e) => setDescription(e.target.value)}
          onBlur={save}
          onKeyDown={(e) => e.key === 'Enter' && save()}
        />
      ) : (
        s.description && <span className="mcp-item-description">{s.description}</span>
      )}
      {takesToken && canConfigure && (
        <form
          className="mcp-token"
          onSubmit={(e) => {
            e.preventDefault()
            if (token.trim() === '') return
            onSetToken(token.trim())
            setToken('')
          }}
        >
          <input
            type="password"
            value={token}
            autoComplete="off"
            placeholder={s.connected ? 'Set a new token…' : 'Paste its token…'}
            aria-label={`Token of ${s.name}`}
            onChange={(e) => setToken(e.target.value)}
          />
          <button type="submit" disabled={token.trim() === ''}>
            Set token
          </button>
        </form>
      )}
      {takesToken && !canConfigure && (
        <small className="panel-note">
          {s.connected ? 'Everyone uses the token an admin set.' : 'An admin sets its token for everyone.'}
        </small>
      )}
      <div className="mcp-item-actions">
        {!takesToken && <button onClick={onConnect}>{s.connected ? 'Sign in again' : 'Connect'}</button>}
        {!takesToken && s.connected && <button onClick={onDisconnect}>Disconnect</button>}
        {takesToken && canConfigure && s.connected && <button onClick={onClearToken}>Clear token</button>}
        {canConfigure && (
          <button className="danger" onClick={onRemove}>
            Remove
          </button>
        )}
      </div>
    </li>
  )
}

// The servers an agent process may use, picked from those in the settings,
// and of each, if it picks, the tools it keeps to.
export function MCPServerPicker({
  servers,
  selected,
  tools,
  readOnly,
  onChange,
  onToolsChange,
  onOpenSettings,
}: {
  servers: MCPServer[] | null
  selected: number[]
  // The tools picked of each server, by id; a server not in it gives all
  // its tools, a connector only those that read.
  tools: Record<string, string[]>
  readOnly: boolean
  onChange: (ids: number[]) => void
  onToolsChange: (tools: Record<string, string[]>) => void
  onOpenSettings: () => void
}) {
  const pickTools = (id: number, picked: string[] | undefined) => {
    const { [String(id)]: _, ...rest } = tools
    onToolsChange(picked === undefined ? rest : { ...rest, [String(id)]: picked })
  }
  // Picked servers since removed from the settings stay listed, so they can be unpicked.
  const missing = servers ? selected.filter((id) => !servers.some((s) => s.id === id)) : []
  return (
    <fieldset className="panel-field mcp-picker">
      <legend>
        <TabLink hash="#/settings/mcp" title="Set up and connect MCP servers in Settings, in a new tab">
          MCP servers
        </TabLink>
      </legend>
      {servers?.length === 0 && (
        <p className="panel-note">
          None set up.{' '}
          <button className="link-button" onClick={onOpenSettings}>
            Add servers
          </button>
        </p>
      )}
      {servers?.map((s) => {
        const on = selected.includes(s.id)
        return (
          <div key={s.id} className="mcp-pick">
            <label className="panel-check">
              <input
                type="checkbox"
                checked={on}
                disabled={readOnly}
                onChange={(e) => {
                  if (!e.target.checked) pickTools(s.id, undefined)
                  onChange(e.target.checked ? [...selected, s.id] : selected.filter((id) => id !== s.id))
                }}
              />
              <span>{s.name}</span>
            </label>
            {on && !s.connected && s.auth === 'token' && <span className="mcp-status">No token yet</span>}
            {on && !s.connected && s.auth !== 'token' && (
              <>
                <span className="mcp-status">Not connected</span>
                <button className="link-button" onClick={() => connectMCPServer(s.id)}>
                  Connect
                </button>
              </>
            )}
            {on && (
              <MCPToolPicker
                server={s}
                picked={tools[String(s.id)]}
                readOnly={readOnly}
                onChange={(picked) => pickTools(s.id, picked)}
              />
            )}
          </div>
        )
      })}
      {missing.length > 0 && (
        <div className="mcp-pick">
          <small className="panel-warning">
            {missing.length === 1 ? 'A server it uses was removed.' : `${missing.length} servers it uses were removed.`}
          </small>
          {!readOnly && (
            <button className="link-button" onClick={() => onChange(selected.filter((id) => !missing.includes(id)))}>
              Drop
            </button>
          )}
        </div>
      )}
      {servers && servers.length > 0 && selected.length === 0 && (
        <small className="panel-note">The agent uses no MCP servers.</small>
      )}
    </fieldset>
  )
}

// Which of a server's tools an agent process keeps to: all of an MCP
// server's and a connector's that only read until it picks, and those it
// picks once it does. The tools are asked of the server when opened.
function MCPToolPicker({
  server,
  picked,
  readOnly,
  onChange,
}: {
  server: MCPServer
  // undefined until it picks.
  picked: string[] | undefined
  readOnly: boolean
  onChange: (picked: string[] | undefined) => void
}) {
  const [open, setOpen] = useState(false)
  const [tools, setTools] = useState<MCPTool[] | null>(null)
  const [problem, setProblem] = useState<string | null>(null)
  useEffect(() => {
    if (!open || tools) return
    listMCPTools(server.id).then(setTools, (err) => setProblem(err instanceof Error ? err.message : String(err)))
  }, [open, tools, server.id])

  const summary =
    picked !== undefined
      ? `${picked.length} ${picked.length === 1 ? 'tool' : 'tools'} picked`
      : server.connector
        ? 'Tools that only read'
        : 'All its tools'
  // Picking starts from what it has without picking.
  const current = picked ?? tools?.filter((t) => !server.connector || t.readOnly).map((t) => t.name) ?? []
  const toggle = (name: string, on: boolean) =>
    onChange(on ? [...current.filter((n) => n !== name), name] : current.filter((n) => n !== name))

  return (
    <div className="mcp-tools">
      <button className="link-button" onClick={() => setOpen(!open)} aria-expanded={open}>
        {summary}
        {open ? ' ▾' : ' ▸'}
      </button>
      {open && problem && <small className="panel-warning">{problem}</small>}
      {open && !problem && !tools && <small className="panel-note">Asking the server for its tools…</small>}
      {open && tools && (
        <ul className="mcp-tool-list">
          {tools.map((t) => (
            <li key={t.name}>
              <label className="panel-check" title={t.description}>
                <input
                  type="checkbox"
                  checked={current.includes(t.name)}
                  disabled={readOnly}
                  onChange={(e) => toggle(t.name, e.target.checked)}
                />
                <span>{t.title || t.name}</span>
                {/* Only a connector says for each tool; an MCP server may say a tool only reads. */}
                {server.connector
                  ? !t.readOnly && <span className="mcp-badge write">changes things</span>
                  : t.readOnly && <span className="mcp-badge">only reads</span>}
              </label>
            </li>
          ))}
          {picked !== undefined && !readOnly && (
            <li>
              <button className="link-button" onClick={() => onChange(undefined)}>
                {server.connector ? 'Back to the tools that only read' : 'Back to all its tools'}
              </button>
            </li>
          )}
        </ul>
      )}
    </div>
  )
}
