import { useEffect, useRef, useState } from 'react'
import { changesSettled, getFlowInputs, startRun, type FlowInputs, type FlowRun, type JSONSchema } from './api'

// What the dialog gives an object the flow starts from: the state it is in,
// if any, and what it holds as typed.
type Given = { state: string; content: string }

// Starts a run of the flow from Run's menu, given what it starts from: the
// objects some process on its top board takes and none generates, which a
// run from Run alone starts without, as a run over the API is given them
// (see api.go). An object without attributes is given its text, one with
// them the JSON they make, laid out to fill in. It runs the board as it is,
// or the version published if one is and the person picks it.
export default function StartDialog({
  flowId,
  published,
  onStarted,
  onClose,
}: {
  flowId: number
  // The version the flow published, if any.
  published: number | null
  onStarted: (run: FlowRun) => void
  onClose: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const [ofPublished, setOfPublished] = useState(false)
  const [found, setFound] = useState<FlowInputs | null>(null)
  const [values, setValues] = useState<Record<string, Given>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])

  // What the board picked starts from, each laid out empty; what was typed
  // of an object both boards start from is kept.
  useEffect(() => {
    let stale = false
    setFound(null)
    getFlowInputs(flowId, ofPublished)
      .then((f) => {
        if (stale) return
        setFound(f)
        setValues((vs) =>
          Object.fromEntries(f.inputs.map((i) => [i.name, vs[i.name] ?? { state: '', content: textOf(i.schema) ? '' : JSON.stringify(skeleton(i.schema), null, 2) }])),
        )
      })
      .catch((err) => !stale && setError(said(err)))
    return () => {
      stale = true
    }
  }, [flowId, ofPublished])

  const set = (name: string, patch: Partial<Given>) => setValues((vs) => ({ ...vs, [name]: { ...vs[name], ...patch } }))

  // Edits still being saved go first, so the board runs as shown.
  const start = async () => {
    if (!found || busy) return
    const inputs: Record<string, unknown> = {}
    const states: Record<string, string> = {}
    for (const i of found.inputs) {
      const v = values[i.name]
      if (textOf(i.schema)) inputs[i.name] = v.content
      else {
        try {
          inputs[i.name] = JSON.parse(v.content)
        } catch (err) {
          setError(`${i.name} isn't JSON: ${err instanceof Error ? err.message : String(err)}`)
          return
        }
      }
      if (v.state) states[i.name] = v.state
    }
    setBusy(true)
    setError('')
    try {
      await changesSettled()
      onStarted(await startRun(flowId, ofPublished, { inputs, states }))
    } catch (err) {
      setError(said(err))
    } finally {
      setBusy(false)
    }
  }

  const takesNothing = found !== null && !found.problem && found.inputs.length === 0
  return (
    <dialog
      ref={ref}
      className="review-dialog rerun-dialog start-dialog"
      aria-label="Start with inputs"
      onClose={onClose}
      // Escape closes the dialog, not the panel under it.
      onKeyDown={(e) => e.stopPropagation()}
    >
      <header className="panel-header">
        <h2>Start with inputs</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <div className="rerun-body">
        <p className="panel-note">
          Give the run the objects the flow starts from: those a process on its top board takes and none generates. Run alone
          starts without them, as schedules do; a request over the API gives them as this does.
        </p>
        {published !== null && (
          <div className="panel-field" role="radiogroup" aria-label="Which board runs">
            <label className="panel-check">
              <input type="radio" checked={!ofPublished} onChange={() => setOfPublished(false)} />
              <span>The board as it is</span>
            </label>
            <label className="panel-check">
              <input type="radio" checked={ofPublished} onChange={() => setOfPublished(true)} />
              <span>The published version, {published}</span>
            </label>
          </div>
        )}
        {found === null && !error && <p className="panel-note">Loading…</p>}
        {found?.problem && <p className="panel-warning">{found.problem}</p>}
        {takesNothing && (
          <p className="panel-note">
            It starts from nothing to give: its first processes take nothing, or what a person fills in. Run it with Run.
          </p>
        )}
        {found?.inputs.map((i) => {
          const v = values[i.name]
          if (!v) return null
          const text = textOf(i.schema)
          return (
            <div key={i.name} className="panel-field trial-input">
              <div className="trial-input-head">
                <span>
                  {i.name}
                  <span className="panel-note">{text ? ' · its text' : i.many ? ' · a list, as JSON' : ' · its attributes, as JSON'}</span>
                </span>
                {i.states.length > 0 && (
                  <select aria-label={`${i.name}'s state`} value={v.state} onChange={(e) => set(i.name, { state: e.target.value })}>
                    <option value="">In no state</option>
                    {i.states.map((st) => (
                      <option key={st} value={st}>
                        {st}
                      </option>
                    ))}
                  </select>
                )}
              </div>
              <textarea
                className="rerun-input"
                aria-label={i.name}
                rows={Math.min(12, Math.max(3, v.content.split('\n').length))}
                value={v.content}
                onChange={(e) => set(i.name, { content: e.target.value })}
              />
            </div>
          )
        })}
        {error && <p className="panel-warning">{error}</p>}
      </div>
      <div className="review-actions">
        <button onClick={onClose}>Cancel</button>
        <button onClick={() => void start()} disabled={!found || !!found.problem || takesNothing || busy}>
          {busy ? 'Starting…' : 'Start'}
        </button>
      </div>
    </dialog>
  )
}

// Whether the object is given as its text: it has no attributes.
const textOf = (schema: JSONSchema) => schema.type === 'string'

// What to fill in of a schema: each attribute empty, a list of one.
function skeleton(schema: JSONSchema): unknown {
  switch (schema.type) {
    case 'object':
      return Object.fromEntries(Object.entries(schema.properties ?? {}).map(([k, v]) => [k, skeleton(v)]))
    case 'array':
      return schema.items ? [skeleton(schema.items)] : []
    default:
      return ''
  }
}

// What went wrong, without the request it went wrong in.
const said = (err: unknown) => (err instanceof Error ? err.message : String(err)).replace(/^[A-Z]+ \S+: \d{3} /, '').trim()
