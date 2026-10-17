import { useCallback, useEffect, useMemo, useState } from 'react'
import { type Json } from './Data'
import { ValueDialog, type Opened } from './FlowData'
import { getFlow, listSharedRecords, type Flow, type SharedRecords as Records } from './api'
import { FlowBuilderIcon, TableIcon } from './icons'
import { formatTime } from './Runs'

// How often the records are asked for again, as flows write them.
const REFRESH_MS = 10_000

// A shared struct's records, asked for again every so often.
function useRecords(struct: number) {
  const [records, setRecords] = useState<Records | null>(null)
  const [error, setError] = useState<string | null>(null)
  const load = useCallback(
    () =>
      listSharedRecords(struct)
        .then((r) => (setRecords(r), setError(null)))
        .catch((err) => setError(err instanceof Error ? err.message : String(err))),
    [struct],
  )
  useEffect(() => {
    load()
    const timer = setInterval(load, REFRESH_MS)
    return () => clearInterval(timer)
  }, [load])
  return { records, error, load }
}

// The records of a shared struct's object, as every flow using it reads and
// writes them, the latest written first, each named by its key: the value of
// its first unique attribute.
export function SharedRecords({ struct, keyName }: { struct: number; keyName: string | null }) {
  const { records, error } = useRecords(struct)

  if (error) return <p className="panel-warning">{error}</p>
  if (records === null) return null
  const table = (
    <a className="link-button records-table-link" href={`#/flows/${struct}/records`}>
      <TableIcon /> Show them in a table
    </a>
  )
  if (records.records.length === 0)
    return (
      <>
        <p className="panel-note">No records yet. Flows using this struct write them as they run.</p>
        {table}
      </>
    )
  return (
    <>
      {table}
      <ol className="run-list">
        {records.records.map((r) => (
          <li key={r.id} className="record-item" title={JSON.stringify(r.content, null, 2)}>
            <span className="run-item-title">
              <span className="record-key">{keyOf(r.content, keyName) ?? `Record ${r.id}`}</span>
              <span className="run-item-status">{r.state || 'no state'}</span>
            </span>
            <time dateTime={r.updated}>
              {formatTime(r.updated)}
              {r.run !== null && ` · run #${r.run}`}
            </time>
          </li>
        ))}
      </ol>
    </>
  )
}

function keyOf(content: unknown, keyName: string | null): string | null {
  if (keyName === null || typeof content !== 'object' || content === null) return null
  const v = (content as Record<string, unknown>)[keyName]
  if (v === undefined || v === null || v === '') return null
  return typeof v === 'string' ? v : JSON.stringify(v)
}

const isFields = (v: Json | undefined): v is { [key: string]: Json } =>
  v !== undefined && v !== null && typeof v === 'object' && !Array.isArray(v)

// Every field the records hold: the object's, in the board's order, then
// any others they hold, such as one since deleted from the board.
function fieldsOf(records: Records): string[] {
  const fields = [...records.fields]
  for (const r of records.records) {
    const v = r.content as Json
    if (isFields(v)) for (const f of Object.keys(v)) if (!fields.includes(f)) fields.push(f)
  }
  return fields
}

// A shared struct's records as a table, a record per row and a column per
// field, with its state and the run that last wrote it, so what every flow
// using the struct made of them reads at a glance. A state picked shows only
// the records in it.
export default function RecordsPage({ id }: { id: number }) {
  const [flow, setFlow] = useState<Flow | null>(null)
  const [flowError, setFlowError] = useState<string | null>(null)
  const { records, error, load } = useRecords(id)
  const [state, setState] = useState<string | null>(null)
  const [opened, setOpened] = useState<Opened | null>(null)

  useEffect(() => {
    getFlow(id)
      .then(setFlow)
      .catch((err) => setFlowError(err instanceof Error ? err.message : String(err)))
  }, [id])

  useEffect(() => {
    if (flow) document.title = `Records · ${flow.name} · Flows`
  }, [flow])

  const fields = useMemo(() => (records ? fieldsOf(records) : []), [records])
  const states = useMemo(() => {
    const counts = new Map<string, number>()
    for (const r of records?.records ?? []) counts.set(r.state, (counts.get(r.state) ?? 0) + 1)
    return [...counts].sort(([a], [b]) => a.localeCompare(b))
  }, [records])
  const shown = (records?.records ?? []).filter((r) => state === null || r.state === state)
  const name = flow?.name ?? '…'

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
          <a href={`#/flows/${id}`}>{name}</a>
          <span className="crumb-separator" aria-hidden>
            /
          </span>
          Records
        </h1>
        {states.length > 1 && (
          <select
            className="records-state"
            value={state ?? ''}
            onChange={(e) => setState(e.target.value === '' ? null : e.target.value)}
            title="Show only the records in a state"
          >
            <option value="">Every state ({records?.records.length})</option>
            {states.map(([s, n]) => (
              <option key={s} value={s}>
                {s || 'no state'} ({n})
              </option>
            ))}
          </select>
        )}
        <button onClick={load} title="Fetch the latest records">
          Refresh
        </button>
      </header>
      {(error || flowError) && (
        <div className="error-banner" role="alert">
          {error ?? flowError}
        </div>
      )}
      <main className="data-main">
        {records === null ? (
          <p className="flows-note">Loading…</p>
        ) : records.records.length === 0 ? (
          <p className="flows-note">
            <TableIcon /> No records yet. Once a flow using {name} generates it, each record shows here as a row.
          </p>
        ) : (
          <div className="data-table-scroll">
            <table className="data-table">
              <thead>
                <tr>
                  <th className="data-run">Record</th>
                  <th>State</th>
                  {fields.map((f) => (
                    <th key={f} className="data-column">
                      {f}
                    </th>
                  ))}
                  <th>Updated</th>
                  <th>Run</th>
                </tr>
              </thead>
              <tbody>
                {shown.map((r) => {
                  const content = r.content as Json
                  const title = `${name} · record ${r.id}`
                  const whole = { title, text: JSON.stringify(content, null, 2) }
                  return (
                    <tr key={r.id}>
                      <td className="data-run">
                        <button className="link-button" title="Show all of it" onClick={() => setOpened(whole)}>
                          {r.id}
                        </button>
                      </td>
                      <td>{r.state || <span className="data-started">no state</span>}</td>
                      {fields.map((f) => (
                        <Cell key={f} value={isFields(content) ? content[f] : undefined} title={`${title}: ${f}`} whole={whole} onOpen={setOpened} />
                      ))}
                      <td className="data-started">{formatTime(r.updated)}</td>
                      <td>
                        {r.run !== null &&
                          (r.runFlow !== null ? (
                            <a href={`#/flows/${r.runFlow}/runs/${r.run}`} title="Open the run on its flow's board">
                              #{r.run}
                            </a>
                          ) : (
                            `#${r.run}`
                          ))}
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
        {records && records.records.length > 0 && (
          <p className="flows-note">
            {records.more ? `The latest ${records.records.length} records` : `All ${records.records.length} records`}, the latest
            written first{state !== null && `, ${shown.length} of them ${state || 'with no state'}`}. Click a cell to see all of it.
          </p>
        )}
      </main>
      {opened && <ValueDialog opened={opened} onOpen={setOpened} onClose={() => setOpened(null)} />}
    </div>
  )
}

// A field of a record: text as it is, a list as its number of items,
// anything else as JSON, cut to a line; clicking it shows all of it.
function Cell({
  value,
  title,
  whole,
  onOpen,
}: {
  value: Json | undefined
  title: string
  whole: Opened
  onOpen: (o: Opened) => void
}) {
  if (value === undefined || value === null || value === '') return <td className="data-cell empty" />
  const text = typeof value === 'string' ? value : JSON.stringify(value, null, 2)
  const label = Array.isArray(value) ? `${value.length} ${value.length === 1 ? 'item' : 'items'}` : text
  return (
    <td className="data-cell">
      <button className="data-cell-open" title="Show all of it" onClick={() => onOpen({ title, text, whole })}>
        {label}
      </button>
    </td>
  )
}
