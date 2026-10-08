import { useState } from 'react'
import {
  connectMCPServer,
  createMCPServer,
  deleteMCPServer,
  disconnectMCPServer,
  updateMCPServer,
  type MCPServer,
} from './api'

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

// The MCP servers agents can use. Each is connected by signing in through
// the browser; an agent process then picks the ones it uses, in its panel.
export function MCPSettingsPanel({
  servers,
  onChanged,
  onClose,
  onError,
}: {
  servers: MCPServer[]
  // Load the servers again.
  onChanged: () => void
  onClose: () => void
  onError: (err: unknown) => void
}) {
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [description, setDescription] = useState('')
  const [adding, setAdding] = useState(false)
  const nameOk = NAME_PATTERN.test(name.trim())
  const urlOk = /^https?:\/\/\S+$/.test(url.trim())

  const add = async () => {
    setAdding(true)
    try {
      await createMCPServer({ name: name.trim(), url: url.trim(), description: description.trim() })
      setName('')
      setUrl('')
      setDescription('')
      onChanged()
    } catch (err) {
      onError(err)
    } finally {
      setAdding(false)
    }
  }

  const act = (f: () => Promise<void>) => f().then(onChanged, onError)

  return (
    <aside className="panel mcp-panel" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <header className="panel-header">
        <h2>MCP servers</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close MCP servers">
          ×
        </button>
      </header>
      <p className="panel-note">
        Servers agents can use. Connect each by signing in; then pick, in an agent process's panel, the ones it may use.
        Agents use none unless picked.
      </p>
      {servers.length === 0 ? (
        <p className="panel-note">No servers yet.</p>
      ) : (
        <ul className="mcp-list">
          {servers.map((s) => (
            <MCPServerItem
              key={s.id}
              server={s}
              onConnect={() => connectMCPServer(s.id)}
              onDisconnect={() => act(() => disconnectMCPServer(s.id))}
              onDescribe={(d) => act(() => updateMCPServer(s.id, { description: d }).then(() => undefined))}
              onRemove={() => act(() => deleteMCPServer(s.id))}
            />
          ))}
        </ul>
      )}
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
          {name.trim() !== '' && !nameOk && (
            <small className="panel-warning">Letters, digits, - and _ only.</small>
          )}
        </label>
        <label className="panel-field">
          <span>Description</span>
          <input
            value={description}
            placeholder="Optional. What it offers, which tells agents when to use it."
            onChange={(e) => setDescription(e.target.value)}
          />
        </label>
        <button className="panel-button" type="submit" disabled={!nameOk || !urlOk || adding}>
          Add
        </button>
      </form>
    </aside>
  )
}

function MCPServerItem({
  server: s,
  onConnect,
  onDisconnect,
  onDescribe,
  onRemove,
}: {
  server: MCPServer
  onConnect: () => void
  onDisconnect: () => void
  onDescribe: (description: string) => void
  onRemove: () => void
}) {
  const [description, setDescription] = useState(s.description)
  const save = () => description.trim() !== s.description && onDescribe(description.trim())
  return (
    <li className="mcp-item">
      <div className="mcp-item-head">
        <span className="mcp-item-name">{s.name}</span>
        <span className={`mcp-status${s.connected ? ' connected' : ''}`}>{s.connected ? 'Connected' : 'Not connected'}</span>
      </div>
      <span className="mcp-item-url" title={s.url}>
        {s.url}
      </span>
      <input
        className="mcp-item-description"
        value={description}
        placeholder="Add a description…"
        aria-label={`Description of ${s.name}`}
        onChange={(e) => setDescription(e.target.value)}
        onBlur={save}
        onKeyDown={(e) => e.key === 'Enter' && save()}
      />
      <div className="mcp-item-actions">
        <button onClick={onConnect}>{s.connected ? 'Sign in again' : 'Connect'}</button>
        {s.connected && <button onClick={onDisconnect}>Disconnect</button>}
        <button className="danger" onClick={onRemove}>
          Remove
        </button>
      </div>
    </li>
  )
}

// The servers an agent process may use, picked from those in the settings.
export function MCPServerPicker({
  servers,
  selected,
  readOnly,
  onChange,
  onOpenSettings,
}: {
  servers: MCPServer[] | null
  selected: number[]
  readOnly: boolean
  onChange: (ids: number[]) => void
  onOpenSettings: () => void
}) {
  // Picked servers since removed from the settings stay listed, so they can be unpicked.
  const missing = servers ? selected.filter((id) => !servers.some((s) => s.id === id)) : []
  return (
    <fieldset className="panel-field mcp-picker">
      <legend>MCP servers</legend>
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
                onChange={(e) => onChange(e.target.checked ? [...selected, s.id] : selected.filter((id) => id !== s.id))}
              />
              <span>{s.name}</span>
            </label>
            {on && !s.connected && (
              <>
                <span className="mcp-status">Not connected</span>
                <button className="link-button" onClick={() => connectMCPServer(s.id)}>
                  Connect
                </button>
              </>
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
