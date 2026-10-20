import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { createAPIKey, deleteAPIKey, getFlowAPI, listAPIKeys, type APIKey, type FlowAPI, type JSONSchema } from './api'
import { PlusIcon } from './icons'

// The API keys of whoever is signed in, which other systems start flows
// with as them. A new key shows once, as it is made.
export function APIKeysSection({ onError }: { onError: (err: unknown) => void }) {
  const [keys, setKeys] = useState<APIKey[] | null>(null)
  const [name, setName] = useState('')
  const [made, setMade] = useState<APIKey | null>(null)

  const refresh = useCallback(async () => setKeys(await listAPIKeys()), [])

  useEffect(() => {
    refresh().catch(onError)
  }, [refresh, onError])

  const onCreate = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    try {
      setMade(await createAPIKey(name.trim()))
      setName('')
      await refresh()
    } catch (err) {
      onError(err)
    }
  }

  const onDelete = async (k: APIKey) => {
    if (!window.confirm(`Delete the key ${k.name}? Whatever uses it is turned away from then on.`)) return
    try {
      await deleteAPIKey(k.id)
      if (made?.id === k.id) setMade(null)
      await refresh()
    } catch (err) {
      onError(err)
    }
  }

  return (
    <section className="flow-section" aria-label="API keys">
      <h2>API keys</h2>
      <p className="flows-note">
        Other systems start flows with a key, as you: they start the flows you may run, and their runs use your MCP
        connections. Each flow's settings say where to send the request and what it takes.
      </p>
      <form className="flow-new" onSubmit={onCreate}>
        <input
          placeholder="Name a new key after what uses it, such as Helpdesk"
          aria-label="Key name"
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <button type="submit" disabled={!name.trim()}>
          <PlusIcon /> New key
        </button>
      </form>
      {made?.key && (
        <div className="api-key-made" role="status">
          <p>
            The key <strong>{made.name}</strong>. Copy it now: it isn't shown again.
          </p>
          <code>{made.key}</code>
          <button type="button" onClick={() => navigator.clipboard.writeText(made.key!).catch(onError)}>
            Copy
          </button>
        </div>
      )}
      {keys === null ? (
        <p className="flows-note">Loading…</p>
      ) : keys.length === 0 ? (
        <p className="flows-note">No keys yet.</p>
      ) : (
        <ul className="flow-list key-list">
          {keys.map((k) => (
            <li key={k.id}>
              <div className="key-item">
                <span className="key-provider">
                  <span className="flow-card-name">{k.name}</span>
                  <code className="flows-note">{k.prefix}…</code>
                </span>
                <span className="flows-note api-key-used">
                  Made {new Date(k.createdAt).toLocaleDateString()};{' '}
                  {k.lastUsedAt ? `last used ${new Date(k.lastUsedAt).toLocaleString()}` : 'never used'}
                </span>
                <button type="button" className="danger" onClick={() => onDelete(k)}>
                  Delete
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

// What a request could give of what a schema describes, to show as an example.
function example(s: JSONSchema): unknown {
  if (s.type === 'object')
    return Object.fromEntries(Object.entries(s.properties ?? {}).map(([k, v]) => [k, example(v)]))
  if (s.type === 'array') return [example(s.items ?? {})]
  return '…'
}

// How the flow is started over the API, in its settings: where to send the
// request, what it takes, and a curl command doing it.
export function FlowAPISection({ flowId, onError }: { flowId: number; onError: (err: unknown) => void }) {
  const [api, setAPI] = useState<FlowAPI | null>(null)

  useEffect(() => {
    getFlowAPI(flowId).then(setAPI, onError)
  }, [flowId, onError])

  if (!api) return null
  const body =
    api.inputs.length > 0 ? { inputs: Object.fromEntries(api.inputs.map((i) => [i.name, example(i.schema)])) } : null
  const curl = [
    `curl -X POST ${api.url}`,
    `  -H 'Authorization: Bearer <key>'`,
    ...(body ? [`  -H 'Content-Type: application/json'`, `  -d '${JSON.stringify(body)}'`] : []),
  ].join(' \\\n')

  return (
    <>
      <h3 className="panel-subheading">API</h3>
      {api.problem ? (
        <p className="panel-warning">It can't be started over the API: {api.problem}.</p>
      ) : (
        <>
          <p className="panel-note">
            Other systems start it with a POST to <code>{api.url}</code> and an API key from{' '}
            <a href="#/settings/api">the settings</a>, and read the run back from the <code>Location</code> it answers
            with.{' '}
            {api.inputs.length > 0
              ? 'It takes the objects its processes start from, each by name:'
              : 'Its processes start from nothing, so it takes no inputs. Objects some process takes and none generates would be its inputs.'}
          </p>
          {api.inputs.length > 0 && (
            <ul className="api-inputs">
              {api.inputs.map((i) => (
                <li key={i.name}>
                  <strong>{i.name}</strong>{' '}
                  <span className="panel-note">
                    {i.schema.type === 'string'
                      ? 'its text'
                      : i.many
                        ? `a list of objects with ${(i.schema.items?.required ?? []).join(', ')}`
                        : `an object with ${(i.schema.required ?? []).join(', ')}`}
                    {i.states.length > 0 && `; may be put in a state under states: ${i.states.join(', ')}`}
                  </span>
                </li>
              ))}
            </ul>
          )}
          <pre className="api-curl">{curl}</pre>
          <p className="panel-note">
            What the run leaves of the objects connected to the end comes back as its outcomes.
          </p>
        </>
      )}
    </>
  )
}
