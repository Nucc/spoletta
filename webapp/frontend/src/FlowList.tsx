import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { createFlow, deleteFlow, listFlows, type Flow } from './api'
import { FlowBuilderIcon, PlusIcon, TrashIcon } from './icons'
import { formatTime, RUN_LABELS } from './Runs'

// The main page: every flow, each a job of its own, such as fixing
// exceptions or bumping dependencies. Opening one shows its board.
export default function FlowList({ onOpen }: { onOpen: (id: number) => void }) {
  const [flows, setFlows] = useState<Flow[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)

  const report = useCallback((err: unknown) => setError(err instanceof Error ? err.message : String(err)), [])
  const refresh = useCallback(() => listFlows().then(setFlows).catch(report), [report])

  useEffect(() => {
    document.title = 'Flows'
    refresh()
  }, [refresh])

  const onCreate = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    setBusy(true)
    try {
      const f = await createFlow(name.trim())
      onOpen(f.id)
    } catch (err) {
      report(err)
      setBusy(false)
    }
  }

  const onDelete = async (f: Flow) => {
    if (!window.confirm(`Delete ${f.name}, with everything on its board, its versions and its runs?`)) return
    try {
      await deleteFlow(f.id)
      await refresh()
    } catch (err) {
      report(err)
    }
  }

  return (
    <div className="flows-page">
      <header className="toolbar">
        <h1>
          <FlowBuilderIcon /> Flows
        </h1>
      </header>
      {error && (
        <div className="error-banner" role="alert" onClick={() => setError(null)}>
          {error} (click to dismiss)
        </div>
      )}
      <main className="flows">
        <form className="flow-new" onSubmit={onCreate}>
          <input
            aria-label="New flow name"
            placeholder="Name a new flow, such as Fix exceptions"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <button type="submit" disabled={busy || !name.trim()}>
            <PlusIcon /> New flow
          </button>
        </form>
        {flows === null ? (
          <p className="flows-note">Loading…</p>
        ) : flows.length === 0 ? (
          <p className="flows-note">No flows yet. Each flow does one job; name one above to start its board.</p>
        ) : (
          <ul className="flow-list">
            {flows.map((f) => (
              <li key={f.id}>
                <a className="flow-card" href={`#/flows/${f.id}`}>
                  <span className="flow-card-name">{f.name}</span>
                  <span className="flow-card-run">
                    {f.lastRun ? (
                      <>
                        <span className={`status-dot ${f.lastRun.status}`} aria-hidden />
                        {RUN_LABELS[f.lastRun.status]} · {formatTime(f.lastRun.startedAt)}
                      </>
                    ) : (
                      'Never run'
                    )}
                  </span>
                </a>
                <button
                  className="icon-button flow-delete"
                  onClick={() => onDelete(f)}
                  aria-label={`Delete ${f.name}`}
                  title="Delete flow"
                >
                  <TrashIcon />
                </button>
              </li>
            ))}
          </ul>
        )}
      </main>
    </div>
  )
}
