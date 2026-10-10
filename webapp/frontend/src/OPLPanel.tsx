import { useCallback, useEffect, useState } from 'react'
import { getFlowOPL } from './api'

// The flow in OPL, OPM's Object-Process Language: what the board shows, a
// sentence per fact, as an agent describing the flow is told it. It is read
// when the panel opens, and again on asking, after the board changed.
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
        <h2>OPL</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close OPL">
          ×
        </button>
      </header>
      <p className="panel-note">
        The flow in OPM's Object-Process Language, a sentence per fact the board shows. An agent process can be given it in
        its prompt, to know the flow it works in: tick that in its panel.
      </p>
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
