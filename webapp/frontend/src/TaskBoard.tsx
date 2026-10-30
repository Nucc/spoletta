import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import {
  addSharedRecord,
  deleteSharedRecords,
  getTaskBoard,
  listSharedRecords,
  listTaskStructs,
  saveTaskBoard,
  updateSharedRecord,
  type SearchFacet,
  type SharedRecord,
  type TaskBoard,
  type TaskCard,
  type TaskCards,
  type TaskColumn,
  type TaskColumnEdit,
  type TaskStruct,
} from './api'
import { type Json } from './Data'
import { FlowBuilderIcon, PencilIcon, PlayIcon, PlusIcon, TableIcon, TaskBoardIcon } from './icons'
import { indexFacets, parse, selection, type Facets } from './query'
import { RecordDialog } from './RecordDialog'
import { RecordRunDialog } from './RecordRunDialog'
import { formatTime } from './Runs'
import { QueryBar } from './Search'
import { useConfirm } from './Confirm'
import { usePaletteSource } from './CommandPalette'
import { HomeFrame } from './FlowList'

// How often the board's records are asked for again, as flows and people
// move them on.
const REFRESH_MS = 10_000

// What went wrong, without the request it answered.
const message = (err: unknown) => (err instanceof Error ? err.message : String(err)).replace(/^\w+ \S+: \d+ /, '').trim()

const editOf = (c: TaskColumn): TaskColumnEdit => ({ name: c.name, struct: c.struct, query: c.query, sort: c.sort, card: c.card })

// What a column's cards show unless told: the record's key, the struct's
// first other field as the title, its state and when it was last written.
const defaultCard = (fields: string[], key: string): TaskCard => ({
  fields: [key, ...fields.filter((f) => f !== key).slice(0, 1)].filter((f) => f !== ''),
  state: true,
  updated: true,
  run: false,
})

// A task board's page, #/taskboards/<id>: a column for each of its queries
// of a shared struct's records, as the Records page searches them, each
// record a card showing what the column picks of it: its key heading it,
// a field as its title and others under it, its state, when it was last
// written and the run that last moved it on. The records are asked for again
// every so often, and as the page is shown again, so cards go from column
// to column as flows and people move them on. A card opens its record, to
// correct it as on the Records page, or starts a flow on it. A column adds
// a record, in the state its query names if it names one, opens its records
// on the Records page, and is changed or taken off the board; the board is
// renamed in place.
export default function TaskBoardPage({ id }: { id: number }) {
  const [board, setBoard] = useState<TaskBoard | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  // The column being changed, by its place, or a new one.
  const [editing, setEditing] = useState<number | 'new' | null>(null)
  // The record opened, of a column by its place, or a new one added to it.
  const [opened, setOpened] = useState<{ column: number; record: SharedRecord | null } | null>(null)
  // The record a flow is being started on.
  const [running, setRunning] = useState<{ struct: number; name: string; record: number } | null>(null)
  const [renaming, setRenaming] = useState<string | null>(null)
  // Only the answer to the latest ask shows, so a slow one doesn't undo a save.
  const asked = useRef(0)

  const load = useCallback(() => {
    const n = ++asked.current
    return getTaskBoard(id)
      .then((b) => n === asked.current && (setBoard(b), setError(null)))
      .catch((err) => n === asked.current && setError(message(err)))
  }, [id])

  useEffect(() => {
    load()
    const timer = window.setInterval(load, REFRESH_MS)
    const onShown = () => document.visibilityState === 'visible' && load()
    document.addEventListener('visibilitychange', onShown)
    return () => {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', onShown)
    }
  }, [load])

  useEffect(() => {
    if (board) document.title = `${board.name} · Task board · Spoletta`
  }, [board])

  usePaletteSource(() => ({
    items: board
      ? [
          {
            id: 'taskboard:add-column',
            title: 'Add a column',
            group: 'Task board',
            hint: `To ${board.name}`,
            words: 'task board new column',
            run: () => setEditing('new'),
          },
        ]
      : [],
  }))

  // Saves the board's name and columns, which come back with their records.
  const save = async (name: string, columns: TaskColumnEdit[]) => {
    const n = ++asked.current
    const b = await saveTaskBoard(id, { name, columns })
    if (n === asked.current) setBoard(b)
  }

  // Renames it once, on Enter or leaving the name, but not after Escape:
  // what is typed is kept in a ref too, as leaving the name follows either.
  const typed = useRef<string | null>(null)
  const typeName = (name: string | null) => {
    typed.current = name
    setRenaming(name)
  }
  const rename = () => {
    const name = typed.current?.trim()
    typeName(null)
    if (!board || !name || name === board.name) return
    setActionError(null)
    save(name, board.columns.map(editOf)).catch((err) => setActionError(message(err)))
  }

  // The columns with the one changed, or a new one, in the place picked,
  // and with everywhere, the board's other columns of its struct showing
  // on their cards what it shows.
  const placed = (edit: TaskColumnEdit, at: number, everywhere: boolean): TaskColumnEdit[] => {
    const columns = board!.columns.map(editOf)
    if (editing !== 'new' && editing !== null) columns.splice(editing, 1)
    columns.splice(at, 0, edit)
    return everywhere ? columns.map((c) => (c.struct === edit.struct ? { ...c, card: edit.card } : c)) : columns
  }

  const column = opened !== null ? board?.columns[opened.column] : undefined
  const struct = column?.struct ?? null

  return (
    <div className="flows-page data-page task-board-page home">
      <header className="toolbar">
        <h1>
          <a className="home" href="#/" title="Every flow, task board, procedure and struct">
            <FlowBuilderIcon /> Home
          </a>
          <span className="crumb-separator" aria-hidden>
            /
          </span>
          {renaming !== null ? (
            <form
              className="task-board-rename"
              onSubmit={(e) => {
                e.preventDefault()
                rename()
              }}
            >
              <input
                autoFocus
                aria-label="The task board's name"
                maxLength={255}
                value={renaming}
                onChange={(e) => typeName(e.target.value)}
                onKeyDown={(e) => e.key === 'Escape' && typeName(null)}
                onBlur={rename}
              />
            </form>
          ) : (
            <span className="task-board-name">
              <TaskBoardIcon /> {board?.name ?? '…'}
            </span>
          )}
        </h1>
        <button onClick={() => board && typeName(board.name)} disabled={!board || renaming !== null} title="Rename the task board">
          Rename
        </button>
        <button onClick={() => setEditing('new')} disabled={!board} title="Add a column of a shared struct's records">
          <PlusIcon /> Add column
        </button>
        <button onClick={load} title="Fetch the latest records">
          Refresh
        </button>
      </header>
      {(error || actionError) && (
        <div className="error-banner" role="alert">
          {error ?? actionError}
        </div>
      )}
      <HomeFrame at="taskboard">
        {board === null ? (
          !error && <p className="flows-note task-board-note">Loading…</p>
        ) : board.columns.length === 0 ? (
          <div className="task-board-empty">
            <TaskBoardIcon />
            <p>
              No columns yet. Each column holds the records of a shared struct that a query matches, such as the Jira tickets in state new,
              as the struct's Records page searches them; as flows and people move a record on, it moves from column to column.
            </p>
            <button onClick={() => setEditing('new')}>
              <PlusIcon /> Add a column
            </button>
          </div>
        ) : (
          <main className="task-board">
            {board.columns.map((c, i) => (
              <Column
                key={i}
                column={c}
                onEdit={() => setEditing(i)}
                onOpen={(record) => setOpened({ column: i, record })}
                onRun={(record) => c.struct !== null && setRunning({ struct: c.struct, name: c.structName, record })}
              />
            ))}
          </main>
        )}
      </HomeFrame>
      {editing !== null && board && (
        <ColumnDialog
          column={editing === 'new' ? null : board.columns[editing]}
          cardFor={(s) => board.columns.find((c) => c.struct === s && c.card !== null)?.card ?? null}
          others={(s) => board.columns.filter((c, i) => c.struct === s && i !== editing).length}
          places={board.columns.length + (editing === 'new' ? 1 : 0)}
          place={editing === 'new' ? board.columns.length : editing}
          onSave={async (edit, at, everywhere) => {
            await save(board.name, placed(edit, at, everywhere))
            setEditing(null)
          }}
          onRemove={
            editing === 'new'
              ? undefined
              : async () => {
                  await save(board.name, board.columns.filter((_, i) => i !== editing).map(editOf))
                  setEditing(null)
                }
          }
          onClose={() => setEditing(null)}
        />
      )}
      {opened !== null && column?.cards && struct !== null && (
        <RecordDialog
          title={opened.record ? `${column.structName} · ${keyOf(opened.record, column.cards)}` : `Add a record to ${column.structName}`}
          fields={column.cards.fields}
          states={column.cards.states}
          record={opened.record}
          state={stateOf(column)}
          onSave={async (content, state) => {
            if (opened.record) await updateSharedRecord(struct, opened.record.id, { content, state })
            else await addSharedRecord(struct, { content, state })
            setOpened(null)
            load()
          }}
          onDelete={
            opened.record
              ? async () => {
                  await deleteSharedRecords(struct, [opened.record!.id])
                  setOpened(null)
                  load()
                }
              : undefined
          }
          onClose={() => setOpened(null)}
        />
      )}
      {running !== null && (
        <RecordRunDialog
          struct={running.struct}
          name={running.name}
          records={[running.record]}
          onClose={() => (setRunning(null), load())}
        />
      )}
    </div>
  )
}

const isFields = (v: Json | undefined): v is { [key: string]: Json } =>
  v !== undefined && v !== null && typeof v === 'object' && !Array.isArray(v)

// A field's value as a card says it: text as it is, a number or a yes or
// no as written; nothing for anything else.
function textOf(v: Json | undefined): string | null {
  if (typeof v === 'string') return v.trim() === '' ? null : v
  if (typeof v === 'number' || typeof v === 'boolean') return String(v)
  return null
}

// Any field's value as a card shows it: as textOf says, a list as its
// number of items, fields as JSON; nothing for none.
function shownOf(v: Json | undefined): string | null {
  if (v === undefined || v === null) return null
  if (Array.isArray(v)) return v.length === 0 ? null : `${v.length} ${v.length === 1 ? 'item' : 'items'}`
  if (typeof v === 'object') return JSON.stringify(v)
  return textOf(v)
}

// What names a record: its key, the value of the struct's first unique
// attribute, else its number.
function keyOf(record: SharedRecord, cards: TaskCards): string {
  const content = record.content as Json
  return (isFields(content) && textOf(content[cards.key])) || `Record ${record.id}`
}

// The facets a query is read against to find the state it names.
const STATE: Facets = { list: [], byKey: new Map([['state', [0]]]) }

// The one state a column's query holds its records in, as state:new does,
// which a record added to it starts in; none for a query naming no state,
// or several.
function stateOf(c: TaskColumn): string {
  const named = selection(STATE, parse(c.query).ast, 'state')
  if (named.length !== 1) return ''
  return c.cards?.states.find((s) => s.toLowerCase() === named[0].toLowerCase()) ?? ''
}

// Where the column's records show on the Records page, searched and sorted
// as the column does.
function recordsHash(c: TaskColumn): string {
  const params = new URLSearchParams()
  if (c.query) params.set('q', c.query)
  if (c.sort) params.set('sort', c.sort)
  const q = params.toString().replace(/\+/g, '%20')
  return `#/flows/${c.struct}/records${q ? '?' + q : ''}`
}

// Why a column shows no records, and what would show them again.
function problemOf(c: TaskColumn) {
  switch (c.problem) {
    case 'gone':
      return <>Its struct was deleted for good. Change the column to pick another, or take it off the board.</>
    case 'trash':
      return (
        <>
          {c.structName} is in the trash; <a href="#/trash">restore it</a> to see its records again.
        </>
      )
    case 'hidden':
      return <>Its struct isn't one you see; its owner can give it to a team of yours, or everyone.</>
    case 'unshared':
      return <>{c.structName || 'Its struct'} keeps no records now; share its object again to see them.</>
  }
  return null
}

function Column({
  column: c,
  onEdit,
  onOpen,
  onRun,
}: {
  column: TaskColumn
  onEdit: () => void
  // Opens a record of it, or a new one for none.
  onOpen: (record: SharedRecord | null) => void
  onRun: (record: number) => void
}) {
  const cards = c.cards
  const shown = cards?.records.length ?? 0
  return (
    <section className="task-column" aria-label={c.name}>
      <header className="task-column-head">
        <h2 title={c.name}>{c.name}</h2>
        {cards && (
          <span
            className="task-column-count"
            title={
              shown < cards.matched
                ? `The first ${shown} of the ${cards.matched} records its query matches`
                : 'The records its query matches'
            }
          >
            {shown < cards.matched ? `${shown} of ${cards.matched}` : cards.matched}
          </span>
        )}
        <button
          className="icon-button"
          onClick={onEdit}
          aria-label={`Change ${c.name}`}
          title="Change the column: its name, struct, query, order and place"
        >
          <PencilIcon />
        </button>
      </header>
      <p className="task-column-query">
        {c.struct !== null && !c.problem ? (
          <a href={recordsHash(c)} title="Open these records in a table, on the struct's Records page">
            <TableIcon /> {c.structName}
          </a>
        ) : (
          c.structName && <span>{c.structName}</span>
        )}
        {c.query && <code title={c.query}>{c.query}</code>}
      </p>
      {cards ? (
        <>
          {cards.records.length === 0 ? (
            <p className="task-column-empty">No records match.</p>
          ) : (
            <ol className="task-cards">
              {cards.records.map((r) => (
                <Card
                  key={r.id}
                  record={r}
                  cards={cards}
                  card={c.card ?? defaultCard(cards.fields, cards.key)}
                  onOpen={() => onOpen(r)}
                  onRun={() => onRun(r.id)}
                />
              ))}
            </ol>
          )}
          {cards.truncated && (
            <p className="task-column-empty">Of the latest records written only, as there are more than a search reads.</p>
          )}
          <button className="link-button task-column-add" onClick={() => onOpen(null)} title="Add a record by hand">
            <PlusIcon size={10} /> Add a record
          </button>
        </>
      ) : (
        <p className="task-column-problem">{problemOf(c)}</p>
      )}
    </section>
  )
}

// A record as a card, showing what its column picks: its key heading it,
// the first other field picked as its title and the rest under it, each
// named, those it has no value for left out; then its state, when it was
// last written, and the run that last moved it on, linked to it. A card
// with nothing else to show is named by its key.
function Card({
  record,
  cards,
  card,
  onOpen,
  onRun,
}: {
  record: SharedRecord
  cards: TaskCards
  card: TaskCard
  onOpen: () => void
  onRun: () => void
}) {
  const content = isFields(record.content as Json) ? (record.content as { [key: string]: Json }) : {}
  const key = keyOf(record, cards)
  const keyShown = cards.key !== '' && card.fields.includes(cards.key)
  const [title, ...rest] = card.fields.filter((f) => f !== cards.key).map((f) => [f, shownOf(content[f])] as const)
  const heading = title?.[1] ?? (keyShown ? null : key)
  const run = card.run && record.run !== null
  return (
    <li className="task-card">
      <button className="task-card-open" onClick={onOpen} title="Show the record, to correct it">
        {keyShown && <span className="task-card-key">{key}</span>}
        {heading && <span className="task-card-summary">{heading}</span>}
        {rest.map(
          ([f, v]) =>
            v !== null && (
              <span key={f} className="task-card-field">
                <span className="task-card-field-name">{f}</span> {v}
              </span>
            ),
        )}
      </button>
      {(card.state || card.updated || run) && (
        <div className="task-card-meta">
          {card.state && <span className="task-card-state">{record.state || 'no state'}</span>}
          {card.updated && <time dateTime={record.updated}>{formatTime(record.updated)}</time>}
          {card.updated && record.edited && (
            <span title={`Corrected by hand${record.editedBy ? ` by ${record.editedBy}` : ''} since a run last moved it on`}>edited</span>
          )}
          {run &&
            (record.runFlow !== null ? (
              <a href={`#/flows/${record.runFlow}/runs/${record.run}`} title="Open the run that last moved it on, on its flow's board">
                run #{record.run}
              </a>
            ) : (
              <span>run #{record.run}</span>
            ))}
        </div>
      )}
      <button className="icon-button task-card-run" aria-label={`Run a flow on ${key}`} title="Run a flow on this record" onClick={onRun}>
        <PlayIcon />
      </button>
    </li>
  )
}

// What a column's cards can show of a record besides its fields.
const RECORD_INFO: [keyof Omit<TaskCard, 'fields'>, string][] = [
  ['state', 'Its state'],
  ['updated', 'When it was last written'],
  ['run', 'The run that last moved it on'],
]

// The checklist of what a column's cards show: the struct's fields, in the
// board's order, then any picked it no longer has, and the record's state,
// when it was last written and its run.
function CardPicker({ card, struct, onChange }: { card: TaskCard; struct: TaskStruct; onChange: (card: TaskCard) => void }) {
  const fields = [...struct.fields, ...card.fields.filter((f) => !struct.fields.includes(f))]
  const toggle = (field: string) =>
    onChange({ ...card, fields: fields.filter((f) => (f === field ? !card.fields.includes(f) : card.fields.includes(f))) })
  return (
    <fieldset className="task-card-picker">
      <legend>On its cards</legend>
      <div className="task-card-picks">
        {fields.map((f) => (
          <label key={f} className="panel-check">
            <input type="checkbox" checked={card.fields.includes(f)} onChange={() => toggle(f)} />
            {f}
            {f === struct.key && <span className="task-card-pick-note">its key</span>}
            {!struct.fields.includes(f) && <span className="task-card-pick-note">no longer the struct’s</span>}
          </label>
        ))}
      </div>
      <div className="task-card-picks">
        {RECORD_INFO.map(([k, label]) => (
          <label key={k} className="panel-check">
            <input type="checkbox" checked={card[k]} onChange={() => onChange({ ...card, [k]: !card[k] })} />
            {label}
          </label>
        ))}
      </div>
      <span className="panel-note">
        The key heads each card, the first field ticked after it is its title, and the others show under it.
      </span>
    </fieldset>
  )
}

// How the records of a struct can sort: the latest written first, or by
// any facet the Records page sorts by, one way or the other.
function sortsOf(facets: SearchFacet[], sort: string): [string, string][] {
  const sorts: [string, string][] = [['', 'Latest written first']]
  for (const f of facets) {
    const [up, down] = f.date ? ['oldest first', 'newest first'] : f.numeric ? ['lowest first', 'highest first'] : ['A to Z', 'Z to A']
    sorts.push([f.key, `${f.label}, ${up}`], [`-${f.key}`, `${f.label}, ${down}`])
  }
  if (sort && !sorts.some(([k]) => k === sort)) sorts.push([sort, sort])
  return sorts
}

// A column changed, or a new one: its name, the shared struct whose records
// it holds, picked among those whoever changes it sees, the query picking
// them, written in the Records page's search bar, how many it matches as
// it is typed, how they sort, what its cards show, and its place on the
// board. A struct picked takes what another column of it on the board
// shows, if one was told; what is picked anew goes for the board's other
// columns of it too, unless untold. One whose struct they don't see keeps
// it, and what its cards show, which only those who see it change.
function ColumnDialog({
  column,
  cardFor,
  others,
  places,
  place,
  onSave,
  onRemove,
  onClose,
}: {
  // None for a new one.
  column: TaskColumn | null
  // What the cards of the board's columns of a struct show, if told.
  cardFor: (struct: number) => TaskCard | null
  // How many of the board's other columns hold the struct's records.
  others: (struct: number) => number
  // How many places there are, the new one's counted, and the column's.
  places: number
  place: number
  // Saves it, with everywhere its cards' picks for the other columns of its struct too.
  onSave: (edit: TaskColumnEdit, place: number, everywhere: boolean) => Promise<void>
  onRemove?: () => Promise<void>
  onClose: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const [name, setName] = useState(column?.name ?? '')
  const [struct, setStruct] = useState<number | null>(column?.struct ?? null)
  const [query, setQuery] = useState(column?.query ?? '')
  const [sort, setSort] = useState(column?.sort ?? '')
  // What its cards show; none while they show what they do unless told.
  const [card, setCard] = useState<TaskCard | null>(column?.card ?? null)
  // Whether what they show was picked here, and goes for the board's other
  // columns of the struct too.
  const [changed, setChanged] = useState(false)
  const [everywhere, setEverywhere] = useState(true)
  const [at, setAt] = useState(place)
  const [structs, setStructs] = useState<TaskStruct[] | null>(null)
  // What the query matches among the struct's records, once typing stops for a moment.
  const [found, setFound] = useState<{ facets: SearchFacet[]; matched: number; searched: number } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [confirm, confirmDialog] = useConfirm()

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])

  useEffect(() => {
    listTaskStructs().then(setStructs, (err) => setError(message(err)))
  }, [])

  const picked = structs?.find((s) => s.id === struct)
  const seen = picked !== undefined
  useEffect(() => {
    if (!seen || struct === null) {
      setFound(null)
      return
    }
    let current = true
    const t = window.setTimeout(() => {
      listSharedRecords(struct, { q: query.trim() }).then(
        (r) => current && setFound({ facets: r.facets, matched: r.matched, searched: r.searched }),
        () => current && setFound(null),
      )
    }, 250)
    return () => {
      current = false
      window.clearTimeout(t)
    }
  }, [seen, struct, query])
  const facets = useMemo(() => indexFacets(found?.facets ?? []), [found])

  const run = async (what: () => Promise<void>) => {
    setSaving(true)
    setError(null)
    try {
      await what()
    } catch (err) {
      setError(message(err))
      setSaving(false)
    }
  }

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim() || struct === null) return
    run(() => onSave({ name: name.trim(), struct, query: query.trim(), sort, card }, at, changed && everywhere))
  }

  const pick = (s: number | null) => {
    setStruct(s)
    setCard(s === null ? null : s === column?.struct ? column.card : cardFor(s))
    setChanged(false)
  }
  const siblings = picked ? others(picked.id) : 0

  const title = column ? `Change ${column.name}` : 'Add a column'
  return (
    <dialog
      ref={ref}
      className="review-dialog task-column-dialog"
      aria-label={title}
      onClose={onClose}
      onKeyDown={(e) => e.stopPropagation()}
    >
      <header className="panel-header">
        <h2>{title}</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <form className="task-column-form" onSubmit={submit}>
        <label className="panel-field">
          Name
          <input autoFocus value={name} maxLength={255} placeholder="Such as To do" onChange={(e) => setName(e.target.value)} />
        </label>
        <label className="panel-field">
          Records of
          <select value={struct ?? ''} onChange={(e) => pick(e.target.value ? Number(e.target.value) : null)}>
            <option value="">Pick a shared struct</option>
            {structs?.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
            {struct !== null && structs && !seen && <option value={struct}>{column?.structName || 'A struct you don’t see'}</option>}
          </select>
        </label>
        {structs?.length === 0 && (
          <p className="panel-note">
            No shared structs yet. A struct's object keeps records once it is shared, told apart by a unique attribute; flows using it write
            them as they run.
          </p>
        )}
        <div className="task-column-matching">
          Matching
          <QueryBar facets={facets} query={query} onChange={setQuery} placeholder="Every record, or such as state:new -@priority:low" />
          <span className="panel-note">
            {found
              ? `${found.matched} of the ${found.searched} records match${query.trim() ? '' : ', as nothing narrows them'}.`
              : seen
                ? 'Searching…'
                : 'Written as on the struct’s Records page: state:open, @field:value, words anywhere.'}
          </span>
        </div>
        <label className="panel-field">
          Order
          <select value={sort} onChange={(e) => setSort(e.target.value)}>
            {sortsOf(found?.facets ?? [], sort).map(([key, label]) => (
              <option key={key} value={key}>
                {label}
              </option>
            ))}
          </select>
        </label>
        {picked && (
          <CardPicker
            card={card ?? defaultCard(picked.fields, picked.key)}
            struct={picked}
            onChange={(c) => (setCard(c), setChanged(true))}
          />
        )}
        {changed && siblings > 0 && (
          <label className="panel-check">
            <input type="checkbox" checked={everywhere} onChange={(e) => setEverywhere(e.target.checked)} />
            The same on the board's other {siblings === 1 ? `${picked?.name} column` : `${siblings} ${picked?.name} columns`}
          </label>
        )}
        {places > 1 && (
          <label className="panel-field">
            Place, from the left
            <select value={at} onChange={(e) => setAt(Number(e.target.value))}>
              {Array.from({ length: places }, (_, i) => (
                <option key={i} value={i}>
                  {i + 1} of {places}
                </option>
              ))}
            </select>
          </label>
        )}
        {error && <p className="panel-warning">{error}</p>}
        <div className="review-actions">
          {onRemove && (
            <button
              type="button"
              className="record-delete"
              disabled={saving}
              onClick={async () => {
                const yes = await confirm({
                  title: `Take ${column?.name} off the board?`,
                  message: <p>Its records stay as they are, on their struct's Records page. This can't be undone.</p>,
                  confirm: 'Take it off',
                })
                if (yes) run(onRemove)
              }}
            >
              Take off the board
            </button>
          )}
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" disabled={saving || !name.trim() || struct === null}>
            {column ? 'Save' : 'Add'}
          </button>
        </div>
      </form>
      {confirmDialog}
    </dialog>
  )
}
