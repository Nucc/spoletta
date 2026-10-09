import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { createFlow, deleteFlow, listFlows, type Flow } from './api'
import { FlowBuilderIcon, ObjectIcon, PlusIcon, SubflowIcon, TrashIcon } from './icons'
import { formatTime, RUN_LABELS } from './Runs'

// The main page: the flows that run, each a job of its own, such as fixing
// exceptions or bumping dependencies, and apart from them the procedures,
// the flows made to be reused inside others, and the structs, the objects
// they use. Opening one shows its board.
export default function FlowList({ onOpen }: { onOpen: (id: number) => void }) {
  const [flows, setFlows] = useState<Flow[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  const report = useCallback((err: unknown) => setError(err instanceof Error ? err.message : String(err)), [])
  const refresh = useCallback(() => listFlows().then(setFlows).catch(report), [report])

  useEffect(() => {
    document.title = 'Flows'
    refresh()
  }, [refresh])

  const onCreate = async (name: string, kind: FlowKind) => {
    const f = await createFlow(name, kind)
    onOpen(f.id)
  }

  const onDelete = async (f: Flow) => {
    const used =
      f.usedBy === 0
        ? ''
        : f.struct
          ? ` ${usedByLabel(f.usedBy)}; its copies there become objects of their own.`
          : ` ${usedByLabel(f.usedBy)}; they will fail to run it.`
    const what = f.struct ? 'what makes up its object and its versions' : 'everything on its board, its versions and its runs'
    if (!window.confirm(`Delete ${f.name}, with ${what}?${used}`)) return
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
          flows={flows?.filter((f) => kindOf(f) === 'flow') ?? null}
          onCreate={(name) => onCreate(name, 'flow')}
          onDelete={onDelete}
          onError={report}
        />
        <FlowSection
          kind="module"
          title="Procedures"
          note="Parts reused inside flows, dragged in from a board's sidebar. Changing one changes it everywhere it runs, and the objects it leaves follow it."
          empty="No procedures yet. A procedure is a group of processes other flows run, such as creating a Jira ticket."
          placeholder="Name a new procedure, such as Create a Jira ticket"
          button="New procedure"
          flows={flows?.filter((f) => kindOf(f) === 'module') ?? null}
          onCreate={(name) => onCreate(name, 'module')}
          onDelete={onDelete}
          onError={report}
        />
        <FlowSection
          kind="struct"
          title="Structs"
          note="Objects defined once, with their states, attributes and parts, and dragged in from a board's sidebar. Changing one changes every copy of it."
          empty="No structs yet. A struct is a single object flows use, such as a Jira ticket."
          placeholder="Name a new struct, such as Jira ticket"
          button="New struct"
          flows={flows?.filter((f) => kindOf(f) === 'struct') ?? null}
          onCreate={(name) => onCreate(name, 'struct')}
          onDelete={onDelete}
          onError={report}
        />
      </main>
    </div>
  )
}

const usedByLabel = (n: number) => `Used in ${n} ${n === 1 ? 'flow' : 'flows'}`

type FlowKind = 'flow' | 'module' | 'struct'

const kindOf = (f: Flow): FlowKind => (f.struct ? 'struct' : f.module ? 'module' : 'flow')

const DELETE_TITLES: Record<FlowKind, string> = { flow: 'Delete flow', module: 'Delete procedure', struct: 'Delete struct' }

// What a card says under its name: how many flows use it, and its latest
// run; a struct never runs.
function cardStatus(f: Flow, kind: FlowKind) {
  const used = f.usedBy > 0 ? usedByLabel(f.usedBy) : 'Not used yet'
  if (kind === 'struct') return used
  return (
    <>
      {kind === 'module' && <span>{used} · </span>}
      {f.lastRun ? (
        <>
          <span className={`status-dot ${f.lastRun.status}`} aria-hidden />
          {RUN_LABELS[f.lastRun.status]} · {formatTime(f.lastRun.startedAt)}
        </>
      ) : kind === 'module' ? (
        'Never run on its own'
      ) : (
        'Never run'
      )}
    </>
  )
}

function FlowSection({
  kind = 'flow',
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
  kind?: FlowKind
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
    <section className={`flow-section${kind === 'flow' ? '' : ` ${kind}s`}`} aria-label={title}>
      <h2>
        {kind === 'module' && <SubflowIcon />}
        {kind === 'struct' && <ObjectIcon />}
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
                <span className="flow-card-run">{cardStatus(f, kind)}</span>
              </a>
              <button
                className="icon-button flow-delete"
                onClick={() => onDelete(f)}
                aria-label={`Delete ${f.name}`}
                title={DELETE_TITLES[kind]}
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
