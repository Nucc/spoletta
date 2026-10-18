import { useCallback, useEffect, useState } from 'react'
import { getFlowSetup } from './api'

// The flow as code: the flow alone as a setup file, in the YAML Export
// writes and Import reads, as its assistant is told it. The structs and
// procedures it uses are named rather than written out. It is read when the
// panel opens, and again on asking, after the board changed.
export default function CodePanel({ flowId, onClose, onError }: { flowId: number; onClose: () => void; onError: (err: unknown) => void }) {
  const [setup, setSetup] = useState<{ name: string; text: string } | null>(null)
  const [copied, setCopied] = useState(false)

  const load = useCallback(() => {
    getFlowSetup(flowId).then(setSetup).catch(onError)
  }, [flowId, onError])
  useEffect(load, [load])

  const copy = () => {
    if (setup === null) return
    navigator.clipboard
      .writeText(setup.text)
      .then(() => {
        setCopied(true)
        window.setTimeout(() => setCopied(false), 1500)
      })
      .catch(onError)
  }

  const download = () => {
    if (setup === null) return
    const url = URL.createObjectURL(new Blob([setup.text], { type: 'application/yaml' }))
    const a = document.createElement('a')
    a.href = url
    a.download = `${setup.name.replace(/[^\w.-]+/g, '-') || 'flow'}.yaml`
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <aside className="panel code-panel" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <header className="panel-header">
        <h2>Code</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close code">
          ×
        </button>
      </header>
      <p className="panel-note">
        The flow as a setup file, the YAML Export writes and Import reads. The structs, procedures and MCP servers it uses are
        named here, not written out.
      </p>
      <div className="opl-actions">
        <button className="panel-button" onClick={load}>
          Refresh
        </button>
        <button className="panel-button" onClick={copy} disabled={setup === null}>
          {copied ? 'Copied' : 'Copy'}
        </button>
        <button className="panel-button" onClick={download} disabled={setup === null}>
          Download
        </button>
      </div>
      {setup === null ? <p className="panel-note">Loading…</p> : <pre className="code-text">{setup.text}</pre>}
    </aside>
  )
}
