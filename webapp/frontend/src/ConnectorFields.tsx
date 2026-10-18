import { useEffect, useState } from 'react'
import { listDirectory, type ConnectorOperation, type ConnectorSettings, type DirectoryEntry, type MCPServer } from './api'

// What a connector process carries out: a connector, among those installed,
// one of its operations, and the operation's arguments, in which
// {{Object}} and {{Object.attribute}} stand for what the step takes.
export function ConnectorFields({
  servers,
  settings,
  takes,
  readOnly,
  onChange,
  onError,
}: {
  // The MCP servers, the installed connectors among them, or null while they load.
  servers: MCPServer[] | null
  settings: ConnectorSettings | undefined
  // The names of the objects the process takes.
  takes: string[]
  readOnly: boolean
  onChange: (settings: ConnectorSettings | undefined) => void
  onError: (err: unknown) => void
}) {
  const [directory, setDirectory] = useState<DirectoryEntry[] | null>(null)
  useEffect(() => {
    listDirectory().then(setDirectory, onError)
  }, [])

  const connectors = servers?.filter((s) => s.connector) ?? []
  const server = connectors.find((s) => s.id === settings?.server)
  const entry = directory?.find((e) => e.connector && e.name === server?.connector)
  const op = entry?.operations?.find((o) => o.name === settings?.operation)
  const args = settings?.args ?? {}

  const setArg = (name: string, value: string) => {
    if (!settings) return
    const { [name]: _, ...rest } = args
    const next = value === '' ? rest : { ...rest, [name]: value }
    onChange({ ...settings, args: Object.keys(next).length > 0 ? next : undefined })
  }
  // Arguments the operation doesn't take go when it changes; the rest stay.
  const pickOperation = (name: string) => {
    if (!settings) return
    const picked = entry?.operations?.find((o) => o.name === name)
    const kept = Object.fromEntries(Object.entries(args).filter(([k]) => picked?.params.some((p) => p.name === k)))
    onChange({ ...settings, operation: name, args: Object.keys(kept).length > 0 ? kept : undefined })
  }

  if (servers && connectors.length === 0) {
    return (
      <p className="panel-note">
        No connector is installed. An admin installs one, such as GitHub, in{' '}
        <a href="#/settings/connectors">Settings, under Connectors</a>.
      </p>
    )
  }
  const reads = entry?.operations?.filter((o) => !o.write) ?? []
  const writes = entry?.operations?.filter((o) => o.write) ?? []
  return (
    <div className="connector-fields">
      <div className="panel-row">
        <label className="panel-field">
          <span>Connector</span>
          <select
            value={server?.id ?? ''}
            disabled={readOnly}
            onChange={(e) =>
              onChange(e.target.value === '' ? undefined : { server: Number(e.target.value), operation: '', args: undefined })
            }
          >
            <option value="">Pick one…</option>
            {settings && !server && <option value={settings.server}>A removed connector</option>}
            {connectors.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </select>
        </label>
        {server && (
          <label className="panel-field">
            <span>Does</span>
            <select value={settings?.operation ?? ''} disabled={readOnly || !entry} onChange={(e) => pickOperation(e.target.value)}>
              <option value="">Pick what it does…</option>
              <optgroup label="Reads">
                {reads.map((o) => (
                  <option key={o.name} value={o.name}>
                    {o.title}
                  </option>
                ))}
              </optgroup>
              <optgroup label={`Changes ${entry?.title ?? ''}`}>
                {writes.map((o) => (
                  <option key={o.name} value={o.name}>
                    {o.title}
                  </option>
                ))}
              </optgroup>
            </select>
          </label>
        )}
      </div>
      {server && !server.connected && (
        <small className="panel-warning">{server.name} has no token yet; an admin sets it in Settings, under MCP servers.</small>
      )}
      {op && <OperationArgs op={op} title={entry?.title ?? ''} args={args} takes={takes} readOnly={readOnly} onArg={setArg} />}
    </div>
  )
}

function OperationArgs({
  op,
  title,
  args,
  takes,
  readOnly,
  onArg,
}: {
  op: ConnectorOperation
  title: string
  args: Record<string, string>
  takes: string[]
  readOnly: boolean
  onArg: (name: string, value: string) => void
}) {
  return (
    <>
      <p className="panel-note">
        {op.description}
        {op.write && <span className="mcp-badge write">changes {title} when it runs</span>}
      </p>
      {op.params.map((p) => {
        const value = args[p.name] ?? ''
        const id = `connector-arg-${p.name}`
        return (
          <label key={p.name} className="panel-field" htmlFor={id}>
            <span>
              {p.name}
              {p.required && <span className="panel-required"> *</span>}
            </span>
            {p.name === 'body' ? (
              <textarea id={id} rows={4} value={value} readOnly={readOnly} placeholder={p.description} onChange={(e) => onArg(p.name, e.target.value)} />
            ) : (
              <input
                id={id}
                value={value}
                readOnly={readOnly}
                list={p.enum || p.type === 'boolean' ? `${id}-values` : undefined}
                placeholder={p.type === 'strings' ? `${p.description}, separated by commas` : p.description}
                spellCheck={false}
                onChange={(e) => onArg(p.name, e.target.value)}
              />
            )}
            {(p.enum || p.type === 'boolean') && (
              <datalist id={`${id}-values`}>
                {(p.enum ?? ['true', 'false']).map((v) => (
                  <option key={v} value={v} />
                ))}
              </datalist>
            )}
          </label>
        )
      })}
      <small className="panel-note">
        In any argument, <code>{'{{Object}}'}</code> and <code>{'{{Object.attribute}}'}</code> stand for what the step
        takes
        {takes.length > 0 ? (
          <>
            , such as <code>{`{{${takes[0]}}}`}</code>
          </>
        ) : (
          '; link an object into it first'
        )}
        . What {title} answers fills the object it generates: its attributes take the answer's fields of their names,
        a dotted name reaching into one, and a list gets an item per result.
      </small>
    </>
  )
}
