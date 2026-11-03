import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { getRecordChanges, type FieldChange, type RecordChange, type RecordChanges } from './api'
import { AgentIcon, ConnectorIcon, FlowIcon, HumanIcon, LockIcon, PencilIcon, ScriptIcon } from './icons'
import { formatTime } from './Runs'

// A shared record's history in its dialog, newest first: each change the
// state it moved to and from, what made it (a process of a run, linked to
// the run, inside a procedure's run if one made it, decided by whom, or a
// person by hand), when, and each field it changed, before and after, with
// the whole record as it stood then a click away. Over it, the record's
// state now and how many changes it had since it began; only the list
// scrolls. See record_changes.go.

const isFields = (v: unknown): v is Record<string, unknown> => v !== null && typeof v === 'object' && !Array.isArray(v)

// The icon of what made a change: the kind of its process, a person by
// hand, or a flow the person doesn't see.
function originIcon(c: RecordChange) {
  if (c.run === null) return <PencilIcon />
  if (c.hidden) return <LockIcon />
  switch (c.processKind) {
    case 'agent':
      return <AgentIcon />
    case 'script':
      return <ScriptIcon />
    case 'connector':
      return <ConnectorIcon />
    case 'human':
      return <HumanIcon />
  }
  return <FlowIcon />
}

// What made a change, as one line: its flow and process, the run, the
// procedure's run inside it and who decided it; or by hand, by whom.
function Origin({ c, onNavigate }: { c: RecordChange; onNavigate: () => void }) {
  const run = (id: number, flow: number | null) =>
    flow !== null ? (
      <a href={`#/flows/${flow}/runs/${id}`} title="Open the run on its flow's board" onClick={onNavigate}>
        run #{id}
      </a>
    ) : (
      <>run #{id}</>
    )
  const by = c.by && <strong>{c.by}</strong>
  let what: ReactNode
  if (c.run === null) {
    what = c.kind === 'found' ? <>As it stood then{by && <>, corrected by hand by {by}</>}</> : <>By hand{by && <> · {by}</>}</>
  } else if (c.hidden) {
    what = <>Run #{c.run}, of a flow you don’t see</>
  } else {
    const lead = [c.flowName, c.process].filter((s) => s !== '').join(': ')
    what = (
      <>
        {c.kind === 'found' && 'As it stood then, last moved by '}
        {lead && <>{lead} · </>}
        {run(c.run, c.flow)}
        {c.inner !== null && (
          <>
            {' '}
            · in {c.innerFlowName || 'a flow you don’t see'}, {run(c.inner, c.innerFlow)}
          </>
        )}
        {c.processKind === 'human' && by && <> · decided by {by}</>}
      </>
    )
  }
  return (
    <div className="record-history-origin">
      {originIcon(c)}
      <span>{what}</span>
    </div>
  )
}

// A value cut to three lines, with a button showing all of it when it is
// longer.
function Clamped({ className, children }: { className: string; children: ReactNode }) {
  const ref = useRef<HTMLSpanElement>(null)
  const [long, setLong] = useState(false)
  const [open, setOpen] = useState(false)
  useLayoutEffect(() => {
    const el = ref.current
    if (el && !open) setLong(el.scrollHeight > el.clientHeight + 1)
  }, [children, open])
  return (
    <span className="record-history-cell">
      <span ref={ref} className={`${className}${open ? '' : ' clamped'}`}>
        {children}
      </span>
      {long && (
        <button type="button" className="link-button record-history-more" onClick={() => setOpen((o) => !o)}>
          {open ? 'Show less' : 'Show all'}
        </button>
      )}
    </span>
  )
}

// A value as it reads: text as it is, anything else as JSON, on one line
// when short.
function textOf(v: unknown): string {
  if (typeof v === 'string') return v
  const line = JSON.stringify(v)
  return line.length <= 60 ? line : JSON.stringify(v, null, 2)
}

// A value in a change: none when the record didn't have the field, empty
// when its text is.
function Value({ v, side }: { v: unknown; side: 'before' | 'after' }) {
  const said = <span className="visually-hidden">{side === 'before' ? 'changed from ' : 'to '}</span>
  if (v === undefined || v === '')
    return (
      <span className="record-history-none">
        {said}
        {v === undefined ? 'none' : 'empty'}
      </span>
    )
  return (
    <Clamped className={`record-history-value ${side}`}>
      {said}
      {textOf(v)}
    </Clamped>
  )
}

// The fields a change changed, each before and after; a record's first
// change, its fields as they came.
function Changed({ c }: { c: RecordChange }) {
  if (c.changed.length === 0) return null
  if (c.from === null) return <Values fields={c.changed.map((f) => [f.name || 'value', f.after] as const)} />
  return (
    <div className="record-history-fields">
      {c.changed.map((f: FieldChange) => (
        <div key={f.name} className="record-history-field">
          <span className="record-history-name">{f.name || 'value'}</span>
          <Value v={f.before} side="before" />
          <span className="record-history-arrow" aria-hidden="true">
            →
          </span>
          <Value v={f.after} side="after" />
        </div>
      ))}
    </div>
  )
}

// Fields by name with their values, each cut to three lines.
function Values({ fields, boxed }: { fields: (readonly [string, unknown])[]; boxed?: boolean }) {
  return (
    <dl className={`record-history-values${boxed ? ' boxed' : ''}`}>
      {fields.map(([name, v]) => (
        <div key={name} className="record-history-field">
          <dt className="record-history-name">{name}</dt>
          <dd>
            {v === undefined || v === '' ? (
              <span className="record-history-none">{v === undefined ? 'none' : 'empty'}</span>
            ) : (
              <Clamped className="record-history-plain">{textOf(v)}</Clamped>
            )}
          </dd>
        </div>
      ))}
    </dl>
  )
}

// The record as a change left it, its state first, then its struct's
// fields in order and any others by name.
function Then({ c, fields }: { c: RecordChange; fields: string[] }) {
  const content = c.content
  const rows: (readonly [string, unknown])[] = [['state', c.state || 'no state']]
  if (isFields(content)) {
    for (const f of fields) rows.push([f, content[f]])
    for (const f of Object.keys(content).sort()) if (!fields.includes(f)) rows.push([f, content[f]])
  } else {
    rows.push(['value', content])
  }
  return <Values fields={rows} boxed />
}

// What a change did to the record's state, beside the state it came out in.
function moved(c: RecordChange): string {
  if (c.from === null) return c.kind === 'found' ? 'history starts here' : 'added'
  if (c.from === c.state) return 'state unchanged'
  return `from ${c.from || 'no state'}`
}

function Entry({ c, latest, fields, onNavigate }: { c: RecordChange; latest: boolean; fields: string[]; onNavigate: () => void }) {
  const [then, setThen] = useState(false)
  const first = c.from === null
  const dot = latest ? ' latest' : first ? ` ${c.kind}` : ''
  return (
    <li className="record-history-entry">
      <span className={`record-history-dot${dot}`} aria-hidden="true" />
      <div className="record-history-body">
        <div className="record-history-head">
          <span className="record-state large">{c.state || 'no state'}</span>
          <span className="record-history-moved">{moved(c)}</span>
          <time dateTime={c.at}>{formatTime(c.at)}</time>
        </div>
        <Origin c={c} onNavigate={onNavigate} />
        <Changed c={c} />
        {!first && (
          <>
            <button type="button" className="link-button record-history-then" aria-expanded={then} onClick={() => setThen((t) => !t)}>
              {then ? '▾' : '▸'} The record then
            </button>
            {then && <Then c={c} fields={fields} />}
          </>
        )}
      </div>
    </li>
  )
}

const changes = (n: number) => `${n} change${n === 1 ? '' : 's'}`

// How many changes the record had since it began, and how it began.
function since(h: RecordChanges): string {
  if (h.more) return `more than ${h.changes.length} changes`
  const oldest = h.changes[h.changes.length - 1]
  if (!oldest) return ''
  const n = h.changes.length - 1
  const at = formatTime(oldest.at)
  if (oldest.kind === 'found') return n > 0 ? `${changes(n)} since its history began on ${at}` : `its history began on ${at}`
  const how = oldest.run !== null ? 'a run added it' : 'it was added by hand'
  return n > 0 ? `${changes(n)} since ${how} on ${at}` : `${how.replace(/^it was /, '')} on ${at}`
}

// A record's history, as the History tab of its dialog shows it: the line
// saying where it stands, and the list under it, which alone scrolls.
// Asked for once it shows; state is the record's as the dialog has it,
// until the history says.
export function RecordHistory({
  struct,
  record,
  state,
  hidden,
  onNavigate,
}: {
  struct: number
  record: number
  state: string
  hidden?: boolean
  onNavigate: () => void
}) {
  const [history, setHistory] = useState<RecordChanges | null>(null)
  const [error, setError] = useState<string | null>(null)
  const load = useCallback(() => {
    setError(null)
    getRecordChanges(struct, record)
      .then(setHistory)
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
  }, [struct, record])
  useEffect(load, [load])

  const now = history?.record.state ?? state
  return (
    <>
      <p className="record-history-now" hidden={hidden}>
        Now <span className="record-state large">{now || 'no state'}</span>
        {history && history.changes.length > 0 && <> · {since(history)}</>}
      </p>
      <div id="record-history" role="tabpanel" aria-label="History" className="record-history-scroll" hidden={hidden}>
        {error ? (
          <p className="record-history-status panel-warning">
            Couldn’t load its history: {error}{' '}
            <button type="button" className="link-button" onClick={load}>
              Try again
            </button>
          </p>
        ) : history === null ? (
          <p className="record-history-status">Loading the history…</p>
        ) : (
          <>
            <ol className="record-history">
              {history.changes.map((c, i) => (
                <Entry key={c.id} c={c} latest={i === 0} fields={history.fields} onNavigate={onNavigate} />
              ))}
            </ol>
            {history.more && <p className="record-history-status">Only the latest {history.changes.length} changes are shown.</p>}
          </>
        )}
      </div>
    </>
  )
}
