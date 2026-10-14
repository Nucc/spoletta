import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Data, parse, type Json } from './Data'
import { getFlow, getFlowData, type DataCell, type DataRow, type Flow, type FlowData } from './api'
import { FlowBuilderIcon, TableIcon } from './icons'
import { formatTime, RUN_LABELS } from './Runs'

// How often the table catches up with runs still going.
const REFRESH_MS = 5000

// A column of an object: one of its fields, its state, or all of it when
// its content has no fields, such as plain text or a list.
type Column = { id: string; object: string; label: string; field: string | null; kind: 'field' | 'state' | 'whole' }

// The columns of an object, by what tells it apart in the rows.
type Group = { key: string; name: string; columns: Column[] }

// What a cell opens in full.
type Opened = { title: string; text: string; whole?: { title: string; text: string } }

const cellKey = (c: Pick<DataCell, 'object' | 'name'>) => (c.object !== null ? `#${c.object}` : c.name)

const isFields = (v: Json | undefined): v is { [key: string]: Json } =>
  v !== undefined && v !== null && typeof v === 'object' && !Array.isArray(v)

// The value as a cell shows it: text as it is, anything else as JSON.
const asText = (v: Json): string => (typeof v === 'string' ? v : JSON.stringify(v, null, 2))

// A flow's runs as a table, a run per row and a column per attribute of each
// object they held, so what the flow processed reads at a glance. Columns
// come from what the runs held, in the board's order where it has them, so
// older runs' attributes and deleted objects still show.
export default function FlowDataPage({ id }: { id: number }) {
  const [flow, setFlow] = useState<Flow | null>(null)
  const [data, setData] = useState<FlowData | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [hidden, setHidden] = useState(() => loadHidden(id))
  const [opened, setOpened] = useState<Opened | null>(null)

  const report = useCallback((err: unknown) => setError(err instanceof Error ? err.message : String(err)), [])
  const refresh = useCallback(() => getFlowData(id).then(setData).catch(report), [id, report])

  useEffect(() => {
    getFlow(id).then(setFlow).catch(report)
    refresh()
  }, [id, refresh, report])

  const running = data?.rows.some((r) => r.status === 'running') ?? false
  useEffect(() => {
    if (!running) return
    const t = window.setInterval(refresh, REFRESH_MS)
    return () => window.clearInterval(t)
  }, [running, refresh])

  useEffect(() => {
    if (flow) document.title = `Data · ${flow.name} · Flows`
  }, [flow])

  const groups = useMemo(() => (data ? columnsOf(data) : []), [data])
  const items = data?.rows.some((r) => r.itemKey !== '' || r.parent !== null) ?? false

  const toggle = (column: string) => {
    const next = new Set(hidden)
    if (next.has(column)) next.delete(column)
    else next.add(column)
    setHidden(next)
    saveHidden(id, next)
  }

  const shown = groups
    .map((g) => ({ ...g, columns: g.columns.filter((c) => !hidden.has(c.id)) }))
    .filter((g) => g.columns.length > 0)

  return (
    <div className="flows-page data-page">
      <header className="toolbar">
        <h1>
          <a className="home" href="#/" title="All flows">
            <FlowBuilderIcon /> Flows
          </a>
          <span className="crumb-separator" aria-hidden>
            /
          </span>
          <a href={`#/flows/${id}`}>{flow?.name ?? '…'}</a>
          <span className="crumb-separator" aria-hidden>
            /
          </span>
          Data
        </h1>
        {groups.length > 0 && <ColumnPicker groups={groups} hidden={hidden} onToggle={toggle} />}
        <button onClick={refresh} title="Fetch the latest runs">
          Refresh
        </button>
      </header>
      {error && (
        <div className="error-banner" role="alert" onClick={() => setError(null)}>
          {error} (click to dismiss)
        </div>
      )}
      <main className="data-main">
        {data === null ? (
          <p className="flows-note">Loading…</p>
        ) : data.rows.length === 0 ? (
          <p className="flows-note">
            <TableIcon /> No runs yet. Once the flow runs, each run shows here as a row of what it held of each object.
          </p>
        ) : (
          <div className="data-table-scroll">
            <table className="data-table">
              <thead>
                <tr>
                  <th rowSpan={2} className="data-run">
                    Run
                  </th>
                  <th rowSpan={2}>Status</th>
                  <th rowSpan={2}>Started</th>
                  {items && <th rowSpan={2}>Item</th>}
                  {shown.map((g) => (
                    <th key={g.key} colSpan={g.columns.length} className="data-group">
                      {g.name}
                    </th>
                  ))}
                </tr>
                <tr>
                  {shown.flatMap((g) =>
                    g.columns.map((c) => (
                      <th key={c.id} className="data-column" title={`${g.name}: ${c.label}`}>
                        {c.label}
                      </th>
                    )),
                  )}
                </tr>
              </thead>
              <tbody>
                {data.rows.map((r) => (
                  <Row key={r.id} row={r} flow={id} groups={shown} items={items} onOpen={setOpened} />
                ))}
              </tbody>
            </table>
          </div>
        )}
        {data && data.rows.length > 0 && (
          <p className="flows-note">
            The latest {data.rows.length} runs, newest first; a run of one item of a list is a row of its own. Click a
            cell to see all of it.
          </p>
        )}
      </main>
      {opened && <ValueDialog opened={opened} onOpen={setOpened} onClose={() => setOpened(null)} />}
    </div>
  )
}

function Row({
  row,
  flow,
  groups,
  items,
  onOpen,
}: {
  row: DataRow
  flow: number
  groups: Group[]
  items: boolean
  onOpen: (o: Opened) => void
}) {
  const cells = new Map(row.cells.map((c) => [cellKey(c), c]))
  return (
    <tr>
      <td className="data-run">
        <a href={`#/flows/${flow}/runs/${row.id}`} title="Open the run on the board">
          #{row.id}
        </a>
      </td>
      <td>
        <span className="data-status">
          <span className={`status-dot ${row.status}`} aria-hidden />
          {RUN_LABELS[row.status]}
        </span>
      </td>
      <td className="data-started">{formatTime(row.startedAt)}</td>
      {items && (
        <td className="data-item">
          {row.itemKey && <span title={row.itemKey}>{row.itemKey}</span>}
          {row.parent !== null && (
            <span className="data-parent">
              {' '}
              in <a href={`#/flows/${flow}/runs/${row.parent}`}>#{row.parent}</a>
            </span>
          )}
        </td>
      )}
      {groups.flatMap((g) => {
        const cell = cells.get(g.key)
        return g.columns.map((c) => <Cell key={c.id} run={row.id} group={g} column={c} cell={cell} onOpen={onOpen} />)
      })}
    </tr>
  )
}

function Cell({
  run,
  group,
  column,
  cell,
  onOpen,
}: {
  run: number
  group: Group
  column: Column
  cell: DataCell | undefined
  onOpen: (o: Opened) => void
}) {
  if (!cell) return <td className="data-cell empty" />
  const value = parse(cell.content)
  const whole = { title: `Run #${run} · ${group.name}`, text: cell.content }
  const given = cell.given ? ' given' : ''
  const givenTitle = cell.given ? ' It was given to the run.' : ''

  if (column.kind === 'state') {
    return <td className={`data-cell${given}`}>{cell.state}</td>
  }
  if (column.kind === 'whole') {
    // A list is its number of items; fields show in their own columns.
    if (isFields(value)) return <td className="data-cell empty" />
    const label = Array.isArray(value) ? `${value.length} ${value.length === 1 ? 'item' : 'items'}` : cell.content
    return (
      <td className={`data-cell${given}`}>
        <button className="data-cell-open" title={`Show all of it.${givenTitle}`} onClick={() => onOpen(whole)}>
          {label}
        </button>
      </td>
    )
  }
  if (!isFields(value) || !(column.field! in value)) return <td className="data-cell empty" />
  const v = value[column.field!]
  const text = asText(v)
  const label = Array.isArray(v) ? `${v.length} ${v.length === 1 ? 'item' : 'items'}` : text
  return (
    <td className={`data-cell${given}`}>
      <button
        className="data-cell-open"
        title={`Show all of it.${givenTitle}`}
        onClick={() => onOpen({ title: `Run #${run} · ${group.name}: ${column.label}`, text, whole })}
      >
        {label}
      </button>
    </td>
  )
}

// A value in full, formatted as a run's objects are.
function ValueDialog({ opened, onOpen, onClose }: { opened: Opened; onOpen: (o: Opened) => void; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])
  return (
    <dialog ref={ref} className="data-dialog" onClose={onClose} onClick={(e) => e.target === ref.current && onClose()}>
      <header>
        <h2>{opened.title}</h2>
        {opened.whole && (
          <button className="link-button" onClick={() => onOpen(opened.whole!)}>
            Show the whole object
          </button>
        )}
        <button className="panel-close" onClick={onClose} aria-label="Close" title="Close">
          ×
        </button>
      </header>
      {/* A new view for each value, so one shown raw doesn't carry over. */}
      <Data key={opened.title} text={opened.text} />
    </dialog>
  )
}

// Which columns show, by object, saved for the flow.
function ColumnPicker({
  groups,
  hidden,
  onToggle,
}: {
  groups: Group[]
  hidden: Set<string>
  onToggle: (column: string) => void
}) {
  const count = groups.reduce((n, g) => n + g.columns.filter((c) => !hidden.has(c.id)).length, 0)
  const all = groups.reduce((n, g) => n + g.columns.length, 0)
  return (
    <details className="data-picker">
      <summary>
        <TableIcon /> Columns {count < all && `(${count} of ${all})`}
      </summary>
      <div className="data-picker-body">
        {groups.map((g) => (
          <fieldset key={g.key}>
            <legend>{g.name}</legend>
            {g.columns.map((c) => (
              <label key={c.id}>
                <input type="checkbox" checked={!hidden.has(c.id)} onChange={() => onToggle(c.id)} />
                {c.label}
              </label>
            ))}
          </fieldset>
        ))}
      </div>
    </details>
  )
}

// The columns of each object the runs held: its board attributes, then
// other fields the runs held of it, then all of it where a run held no
// fields, such as a list, and its state where a run gave it one. Objects
// go in the board's order, then those no longer on it.
function columnsOf(data: FlowData): Group[] {
  type Seen = { name: string; fields: string[]; whole: boolean; text: boolean; state: boolean }
  const seen = new Map<string, Seen>()
  for (const o of data.objects) seen.set(`#${o.id}`, { name: o.name, fields: [...o.attributes], whole: false, text: false, state: false })
  const held = new Set<string>()
  // Oldest first, so the latest name of an object deleted since wins.
  for (const r of [...data.rows].reverse()) {
    for (const c of r.cells) {
      const key = cellKey(c)
      held.add(key)
      let s = seen.get(key)
      if (!s) {
        s = { name: c.name, fields: [], whole: false, text: false, state: false }
        seen.set(key, s)
      } else if (c.object !== null && !data.objects.some((o) => o.id === c.object)) {
        s.name = c.name
      }
      if (c.state) s.state = true
      const v = parse(c.content)
      if (isFields(v)) {
        for (const f of Object.keys(v)) if (!s.fields.includes(f)) s.fields.push(f)
      } else {
        s.whole = true
        if (!Array.isArray(v)) s.text = true
      }
    }
  }
  const groups: Group[] = []
  for (const [key, s] of seen) {
    if (!held.has(key)) continue
    const columns: Column[] = []
    if (s.state) columns.push({ id: `${key}\u0000state`, object: key, label: 'state', field: null, kind: 'state' })
    for (const f of s.fields) columns.push({ id: `${key}\u0000.${f}`, object: key, label: f, field: f, kind: 'field' })
    if (s.whole || columns.length === 0) {
      columns.push({
        id: `${key}\u0000whole`,
        object: key,
        label: s.fields.length === 0 ? 'value' : s.text ? 'all of it' : 'items',
        field: null,
        kind: 'whole',
      })
    }
    groups.push({ key, name: s.name, columns })
  }
  return groups
}

const HIDDEN_KEY_PREFIX = 'data-hidden-'

function loadHidden(flow: number): Set<string> {
  try {
    const saved = localStorage.getItem(HIDDEN_KEY_PREFIX + flow)
    return new Set(saved ? (JSON.parse(saved) as string[]) : [])
  } catch {
    return new Set()
  }
}

function saveHidden(flow: number, hidden: Set<string>) {
  try {
    localStorage.setItem(HIDDEN_KEY_PREFIX + flow, JSON.stringify([...hidden]))
  } catch {
    // Every column shows again next time.
  }
}
