import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
import { type Json } from './Data'
import { searchable, ValueDialog, type Opened } from './FlowData'
import {
  addSharedRecord,
  deleteSharedRecords,
  getFlow,
  listSharedRecords,
  updateSharedRecord,
  type Flow,
  type Search,
  type SharedRecord,
  type SharedRecords as Records,
} from './api'
import { HeaderResizer, useColumnWidths } from './HeaderResizer'
import { FlowBuilderIcon, TableIcon } from './icons'
import { exclude, indexFacets, parse as parseQuery, select } from './query'
import { RecordDialog } from './RecordDialog'
import { formatTime } from './Runs'
import { ariaSort, FacetList, QueryBar, SortButton, TimeRange, useSearch } from './Search'

// How often the records are asked for again, as flows write them.
const REFRESH_MS = 10_000

// A shared struct's records the search matches, asked for again every so
// often. Only the answer to the latest ask shows, so a slow one for an
// older search doesn't replace it.
function useRecords(struct: number, search?: Search) {
  const [records, setRecords] = useState<Records | null>(null)
  const [error, setError] = useState<string | null>(null)
  const asked = useRef(0)
  const load = useCallback(() => {
    const n = ++asked.current
    return listSharedRecords(struct, search)
      .then((r) => n === asked.current && (setRecords(r), setError(null)))
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
  }, [struct, search])
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
// using the struct made of them reads at a glance. The records can be
// searched and sorted by any of them, on the server over every record
// stored (Search.tsx). A person corrects them here too: adds a record,
// edits one's fields and state, and deletes those ticked, one or many; and
// drags a column's header edge to widen or narrow it.
export default function RecordsPage({ id }: { id: number }) {
  const [flow, setFlow] = useState<Flow | null>(null)
  const [flowError, setFlowError] = useState<string | null>(null)
  const { query, setQuery, sort, setSort, search } = useSearch('records', id)
  const { records, error, load } = useRecords(id, search)
  const [opened, setOpened] = useState<Opened | null>(null)
  // The record being written by hand: one corrected, or a new one.
  const [editing, setEditing] = useState<SharedRecord | 'new' | null>(null)
  const [ticked, setTicked] = useState<Set<number>>(new Set())
  const [actionError, setActionError] = useState<string | null>(null)
  const columns = useColumnWidths(`records-columns-${id}`)

  useEffect(() => {
    getFlow(id)
      .then(setFlow)
      .catch((err) => setFlowError(err instanceof Error ? err.message : String(err)))
  }, [id])

  useEffect(() => {
    if (flow) document.title = `Records · ${flow.name} · Flows`
  }, [flow])

  const fields = useMemo(() => (records ? fieldsOf(records) : []), [records])
  const facets = useMemo(() => indexFacets(records?.facets ?? []), [records])
  // The facet each field is searched by (shared.go).
  const keyOf = (field: string) => records?.facets.find((f) => f.kind === 'field' && f.field === field)?.key
  const ast = useMemo(() => parseQuery(query).ast, [query])
  const name = flow?.name ?? '…'

  // What is ticked stays ticked among the records shown, as they are asked for again.
  const shown = useMemo(() => records?.records.map((r) => r.id) ?? [], [records])
  useEffect(() => {
    setTicked((t) => {
      const kept = new Set(shown.filter((r) => t.has(r)))
      return kept.size === t.size ? t : kept
    })
  }, [shown])
  const all = shown.length > 0 && shown.every((r) => ticked.has(r))
  const tickAll = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (tickAll.current) tickAll.current.indeterminate = ticked.size > 0 && !all
  })
  const tick = (record: number) =>
    setTicked((t) => {
      const next = new Set(t)
      if (!next.delete(record)) next.add(record)
      return next
    })

  const deleteTicked = async () => {
    const n = ticked.size
    if (
      !window.confirm(
        `Delete ${n === 1 ? 'this record' : `these ${n} records`}? Flows using ${name} no longer find ${n === 1 ? 'it' : 'them'}.`,
      )
    )
      return
    setActionError(null)
    try {
      await deleteSharedRecords(id, [...ticked])
      setTicked(new Set())
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err))
    }
    load()
  }
  const save = async (record: SharedRecord | null, content: Record<string, unknown>, state: string) => {
    if (record) await updateSharedRecord(id, record.id, { content, state })
    else await addSharedRecord(id, { content, state })
    setEditing(null)
    load()
  }
  // A header's text, sorting by its facet if it has one, and the handle widening it.
  const header = (column: string, key: string | undefined, label: string, className?: string, newestFirst = false) => (
    <th key={column} className={className} style={columns.style(column)} aria-sort={key ? ariaSort(sort, key) : undefined}>
      {columns.fit(column, sortable(key, label, newestFirst))}
      <HeaderResizer column={column} label={label} columns={columns} />
    </th>
  )

  const filterBy = (key: string, value: string, out: boolean) => {
    setQuery(out ? exclude(facets, query, key, value) : select(facets, query, key, [value]))
    setOpened(null)
  }
  const sortable = (key: string | undefined, label: string, newestFirst = false) =>
    key ? (
      <SortButton facet={key} sort={sort} onSort={setSort} newestFirst={newestFirst}>
        {label}
      </SortButton>
    ) : (
      label
    )

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
        <button onClick={() => setEditing('new')} disabled={records === null} title="Add a record by hand">
          Add record
        </button>
        <button onClick={load} title="Fetch the latest records">
          Refresh
        </button>
      </header>
      {(error || flowError || actionError) && (
        <div className="error-banner" role="alert">
          {error ?? flowError ?? actionError}
        </div>
      )}
      <main className="data-main">
        {records === null ? (
          <p className="flows-note">Loading…</p>
        ) : records.searched === 0 ? (
          <p className="flows-note">
            <TableIcon /> No records yet. Once a flow using {name} generates it, each record shows here as a row; or add one by hand.
          </p>
        ) : (
          <>
            <div className="search-row">
              <QueryBar
                facets={facets}
                query={query}
                onChange={setQuery}
                placeholder="Search records, e.g. state:open -@severity:low crash"
              />
              <TimeRange facets={facets} query={query} field="updated" onChange={setQuery} />
            </div>
            <div className="search-body">
              <FacetList facets={facets} ast={ast} query={query} onChange={setQuery} rows={records.searched} />
              <div className="search-results">
                {ticked.size > 0 && (
                  <div className="records-selection">
                    <span>
                      {ticked.size} {ticked.size === 1 ? 'record' : 'records'} ticked
                    </span>
                    <button className="link-button danger" onClick={deleteTicked}>
                      Delete
                    </button>
                    <button className="link-button" onClick={() => setTicked(new Set())}>
                      Untick all
                    </button>
                  </div>
                )}
                <div className="data-table-scroll">
                  <table className="data-table">
                    <thead>
                      <tr>
                        <th className="data-run" style={columns.style('id')} aria-sort={ariaSort(sort, 'id')}>
                          {columns.fit(
                            'id',
                            <span className="records-tick">
                              <input
                                ref={tickAll}
                                type="checkbox"
                                checked={all}
                                aria-label="Tick every record shown"
                                onChange={() => setTicked(all ? new Set() : new Set(shown))}
                              />
                              {sortable('id', 'Record')}
                            </span>,
                          )}
                          <HeaderResizer column="id" label="Record" columns={columns} />
                        </th>
                        {header('state', 'state', 'State')}
                        {fields.map((f) => header('.' + f, keyOf(f), f, 'data-column'))}
                        {header('updated', 'updated', 'Updated', undefined, true)}
                        {header('run', 'run', 'Run')}
                      </tr>
                    </thead>
                    <tbody>
                      {records.records.map((r) => {
                        const content = r.content as Json
                        const title = `${name} · record ${r.id}`
                        const whole = {
                          title,
                          text: JSON.stringify(content, null, 2),
                        }
                        return (
                          <tr key={r.id} className={ticked.has(r.id) ? 'ticked' : undefined}>
                            <td className="data-run" style={columns.style('id')}>
                              {columns.fit(
                                'id',
                                <span className="records-tick">
                                  <input
                                    type="checkbox"
                                    checked={ticked.has(r.id)}
                                    aria-label={`Tick record ${r.id}`}
                                    onChange={() => tick(r.id)}
                                  />
                                  <button className="link-button" title="Show all of it" onClick={() => setOpened(whole)}>
                                    {r.id}
                                  </button>
                                  {isFields(content) && (
                                    <button className="link-button records-edit" title="Correct this record" onClick={() => setEditing(r)}>
                                      Edit
                                    </button>
                                  )}
                                </span>,
                              )}
                            </td>
                            <td style={columns.style('state')}>
                              {columns.fit('state', r.state || <span className="data-started">no state</span>)}
                            </td>
                            {fields.map((f) => (
                              <Cell
                                key={f}
                                value={isFields(content) ? content[f] : undefined}
                                title={`${title}: ${f}`}
                                facet={keyOf(f)}
                                width={columns.style('.' + f)}
                                whole={whole}
                                onOpen={setOpened}
                              />
                            ))}
                            <td className="data-started" style={columns.style('updated')}>
                              {columns.fit('updated', formatTime(r.updated))}
                            </td>
                            <td style={columns.style('run')}>
                              {columns.fit(
                                'run',
                                <>
                                  {r.run !== null &&
                                    (r.runFlow !== null ? (
                                      <a href={`#/flows/${r.runFlow}/runs/${r.run}`} title="Open the run on its flow's board">
                                        #{r.run}
                                      </a>
                                    ) : (
                                      `#${r.run}`
                                    ))}
                                  {r.edited && (
                                    <span className="data-started" title="Corrected by hand since a run last moved it on">
                                      {r.run !== null && ' · '}edited
                                      {r.editedBy && ` by ${r.editedBy}`}
                                    </span>
                                  )}
                                </>,
                              )}
                            </td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                  {records.records.length === 0 && <p className="search-none">No records match the search.</p>}
                </div>
                <p className="flows-note">
                  {search.q !== '' ? `${records.matched} of ${records.searched} records match` : `${records.searched} records`}
                  {records.truncated && ', searching the latest written of them'}
                  {records.more && `; the first ${records.records.length} show`}
                  {sort ? '' : ', the latest written first'}. Click a cell to see all of it, a column's name to sort by it, its edge to
                  resize it.
                </p>
              </div>
            </div>
          </>
        )}
      </main>
      {editing !== null && records && (
        <RecordDialog
          title={editing === 'new' ? `Add a record to ${name}` : `${name} · record ${editing.id}`}
          fields={records.fields}
          states={records.states}
          record={editing === 'new' ? null : editing}
          onSave={(content, state) => save(editing === 'new' ? null : editing, content, state)}
          onDelete={
            editing === 'new'
              ? undefined
              : async () => {
                  await deleteSharedRecords(id, [editing.id])
                  setEditing(null)
                  load()
                }
          }
          onClose={() => setEditing(null)}
        />
      )}
      {opened && <ValueDialog opened={opened} onOpen={setOpened} onClose={() => setOpened(null)} onFilter={filterBy} />}
    </div>
  )
}

// A field of a record: text as it is, a list as its number of items,
// anything else as JSON, cut to a line; clicking it shows all of it.
function Cell({
  value,
  title,
  facet,
  width,
  whole,
  onOpen,
}: {
  value: Json | undefined
  title: string
  facet: string | undefined
  // The width the column was dragged to, if it was.
  width: CSSProperties | undefined
  whole: Opened
  onOpen: (o: Opened) => void
}) {
  if (value === undefined || value === null || value === '') return <td className="data-cell empty" style={width} />
  const text = typeof value === 'string' ? value : JSON.stringify(value, null, 2)
  const label = Array.isArray(value) ? `${value.length} ${value.length === 1 ? 'item' : 'items'}` : text
  return (
    <td className="data-cell" style={width}>
      <button
        className={width ? 'data-cell-open sized' : 'data-cell-open'}
        style={width && { maxWidth: width.width }}
        title="Show all of it"
        onClick={() => {
          const s = searchable(value)
          onOpen({
            title,
            text,
            whole,
            ...(s !== null && facet !== undefined && { filter: { key: facet, value: s } }),
          })
        }}
      >
        {label}
      </button>
    </td>
  )
}
