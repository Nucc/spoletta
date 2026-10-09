import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { createFlow, deleteFlow, listFlows, type Flow } from './api'
import { FlowBuilderIcon, PlusIcon, SubflowIcon, TrashIcon } from './icons'
import { formatTime, RUN_LABELS } from './Runs'

// The main page: the flows that run, each a job of its own, such as fixing
// exceptions or bumping dependencies, and apart from them the modules, the
// flows made to be reused inside others. Opening one shows its board.
export default function FlowList({ onOpen }: { onOpen: (id: number) => void }) {
  const [flows, setFlows] = useState<Flow[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  const report = useCallback((err: unknown) => setError(err instanceof Error ? err.message : String(err)), [])
  const refresh = useCallback(() => listFlows().then(setFlows).catch(report), [report])

  useEffect(() => {
    document.title = 'Flows'
    refresh()
  }, [refresh])

  const onCreate = async (name: string, module: boolean) => {
    const f = await createFlow(name, module)
    onOpen(f.id)
  }

  const onDelete = async (f: Flow) => {
    const used = f.usedBy > 0 ? ` ${usedByLabel(f.usedBy)}; they will fail to run it.` : ''
    if (!window.confirm(`Delete ${f.name}, with everything on its board, its versions and its runs?${used}`)) return
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
        <FlowSection
          title="Flows"
          note="The jobs that run, each doing one thing end to end."
          empty="No flows yet. Each flow does one job; name one above to start its board."
          placeholder="Name a new flow, such as Fix exceptions"
          button="New flow"
          flows={flows?.filter((f) => !f.module) ?? null}
          onCreate={(name) => onCreate(name, false)}
          onDelete={onDelete}
          onError={report}
        />
        <FlowSection
          module
          title="Procedures"
          note="Parts reused inside flows, dragged in from a board's sidebar. Changing one changes it everywhere it runs, and the objects it leaves follow it."
          empty="No procedures yet. A procedure is a group of processes other flows run, such as creating a Jira ticket."
          placeholder="Name a new procedure, such as Create a Jira ticket"
          button="New procedure"
          flows={flows?.filter((f) => f.module) ?? null}
          onCreate={(name) => onCreate(name, true)}
          onDelete={onDelete}
          onError={report}
        />
      </main>
    </div>
  )
}

const usedByLabel = (n: number) => `Used in ${n} ${n === 1 ? 'flow' : 'flows'}`

function FlowSection({
  module = false,
  title,
  note,
  empty,
  placeholder,
  button,
  flows,
  onCreate,
  onDelete,
  onError,
}: {
  module?: boolean
  title: string
  note: string
  empty: string
  placeholder: string
  button: string
  flows: Flow[] | null
  onCreate: (name: string) => Promise<void>
  onDelete: (f: Flow) => void
  onError: (err: unknown) => void
}) {
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    setBusy(true)
    try {
      await onCreate(name.trim())
    } catch (err) {
      onError(err)
      setBusy(false)
    }
  }

  return (
    <section className={`flow-section${module ? ' modules' : ''}`} aria-label={title}>
      <h2>
        {module && <SubflowIcon />}
        {title}
      </h2>
      <p className="flows-note">{note}</p>
      <form className="flow-new" onSubmit={submit}>
        <input aria-label={placeholder} placeholder={placeholder} value={name} onChange={(e) => setName(e.target.value)} />
        <button type="submit" disabled={busy || !name.trim()}>
          <PlusIcon /> {button}
        </button>
      </form>
      {flows === null ? (
        <p className="flows-note">Loading…</p>
      ) : flows.length === 0 ? (
        <p className="flows-note">{empty}</p>
      ) : (
        <ul className="flow-list">
          {flows.map((f) => (
            <li key={f.id}>
              <a className="flow-card" href={`#/flows/${f.id}`}>
                <span className="flow-card-name">{f.name}</span>
                <span className="flow-card-run">
                  {module && <span>{f.usedBy > 0 ? usedByLabel(f.usedBy) : 'Not used yet'} · </span>}
                  {f.lastRun ? (
                    <>
                      <span className={`status-dot ${f.lastRun.status}`} aria-hidden />
                      {RUN_LABELS[f.lastRun.status]} · {formatTime(f.lastRun.startedAt)}
                    </>
                  ) : module ? (
                    'Never run on its own'
                  ) : (
                    'Never run'
                  )}
                </span>
              </a>
              <button
                className="icon-button flow-delete"
                onClick={() => onDelete(f)}
                aria-label={`Delete ${f.name}`}
                title={module ? 'Delete procedure' : 'Delete flow'}
              >
                <TrashIcon />
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
