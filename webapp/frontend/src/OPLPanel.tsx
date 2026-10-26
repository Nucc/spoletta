import { useCallback, useEffect, useState } from 'react'
import { getFlowOPL } from './api'

// The flow in OPL, OPM's Object-Process Language: what the board shows, a
// sentence per fact, as an agent describing the flow is told it. It is read
// when the panel opens, and again on asking, after the board changed. A
// key says what its verbs are in plain words, and OPM's names for them.
export default function OPLPanel({ flowId, onClose, onError }: { flowId: number; onClose: () => void; onError: (err: unknown) => void }) {
  const [text, setText] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)

  const load = useCallback(() => {
    getFlowOPL(flowId)
      .then((t) => setText(t))
      .catch(onError)
  }, [flowId, onError])
  useEffect(load, [load])

  const copy = () => {
    if (text === null) return
    navigator.clipboard
      .writeText(text)
      .then(() => {
        setCopied(true)
        window.setTimeout(() => setCopied(false), 1500)
      })
      .catch(onError)
  }

  return (
    <aside className="panel opl-panel" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <header className="panel-header">
        <h2>Sentences (OPL)</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close the sentences">
          ×
        </button>
      </header>
      <p className="panel-note">
        The flow said in sentences, a sentence per fact the board shows, in OPM's Object-Process Language. An agent process
        can be given it in its prompt, to know the flow it works in: tick that in its panel.
      </p>
      <details className="opl-key">
        <summary>How its words read</summary>
        <dl>
          {OPL_KEY.map(([words, plain]) => (
            <div key={words}>
              <dt>{words}</dt>
              <dd>{plain}</dd>
            </div>
          ))}
        </dl>
      </details>
      <div className="opl-actions">
        <button className="panel-button" onClick={load}>
          Refresh
        </button>
        <button className="panel-button" onClick={copy} disabled={text === null}>
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      {text === null ? <p className="panel-note">Loading…</p> : <pre className="opl-text">{text || 'The board is empty.'}</pre>}
    </aside>
  )
}

// OPL's verbs, each with what it says in plain words and OPM's name for it.
const OPL_KEY: [string, string][] = [
  ['yields', 'makes the object (result)'],
  ['affects', 'takes the object and changes it (effect)'],
  ['requires …, which it doesn’t change', 'uses the object without changing it (instrument)'],
  ['consumes', 'uses the object up (consumption)'],
  ['handles', 'a person does the process (agent)'],
  ['entering … triggers', 'runs when the object enters the state (event)'],
  ['occurs if …, and is skipped otherwise', 'runs only if the object is in the state (condition)'],
  ['exhibits', 'has the attributes (exhibition)'],
  ['consists of', 'is made of the parts (aggregation)'],
  ['is a', 'is a kind of the other object, with all it has (generalization)'],
]
