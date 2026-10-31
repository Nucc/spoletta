import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import {
  addSharedRecord,
  deleteSharedRecords,
  getTaskBoard,
  listRecordFlows,
  listSharedRecords,
  listTaskStructs,
  pauseTaskBoard,
  playTaskBoard,
  retryTaskBoardRecords,
  saveTaskBoard,
  updateSharedRecord,
  type CardRun,
  type RecordFlow,
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
import { FlowBuilderIcon, PauseIcon, PencilIcon, PlayIcon, PlusIcon, TableIcon, TaskBoardIcon } from './icons'
import { indexFacets, parse, quote, selection, type Facets } from './query'
import { RecordDialog } from './RecordDialog'
import { RecordRunDialog } from './RecordRunDialog'
import { formatDuration, formatTime } from './Runs'
import { QueryBar } from './Search'
import { useConfirm } from './Confirm'
import { usePaletteSource } from './CommandPalette'
import { HomeFrame } from './FlowList'

// How often the board's records are asked for again, as flows and people
// move them on; more often while it plays or runs go on its records, so a
// card shows its run while it goes.
const REFRESH_MS = 10_000
const BUSY_REFRESH_MS = 2_000

// What went wrong, without the request it answered.
const message = (err: unknown) => (err instanceof Error ? err.message : String(err)).replace(/^\w+ \S+: \d+ /, '').trim()

const editOf = (c: TaskColumn): TaskColumnEdit => ({
  name: c.name,
  struct: c.struct,
  query: c.query,
  sort: c.sort,
  card: c.card,
  flow: c.flow,
  maxRuns: c.maxRuns,
})

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
// renamed in place, and columns are laid out from a struct's states, one
// for each.
//
// Play keeps giving each record no run is going on to the flow taking its
// state, a run per record, as many at once as the board allows, or to the
// flow its column names; records in a state no flow takes rest there (see
// taskboard_play.go). Each column says which flow takes its state, or why
// none starts. A card shows the run going on its record, its flow, the
// steps going and for how long, asked for every couple of seconds while
// the board plays or runs go, or that the board skips it as stuck and why,
// to try again.
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
  // Whether columns are being laid out from a struct's states.
  const [laying, setLaying] = useState(false)
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
    const onShown = () => document.visibilityState === 'visible' && load()
    document.addEventListener('visibilitychange', onShown)
    return () => document.removeEventListener('visibilitychange', onShown)
  }, [load])

  const busy =
    board !== null &&
    (board.playing || board.columns.some((c) => Object.values(c.cards?.runs ?? {}).some((r) => r && r.status !== 'stuck')))
  useEffect(() => {
    const timer = window.setInterval(load, busy ? BUSY_REFRESH_MS : REFRESH_MS)
    return () => window.clearInterval(timer)
  }, [load, busy])

  useEffect(() => {
    if (board) document.title = `${board.name} · Task board · Spoletta`
  }, [board])

  // Answers what changes the board, which comes back with its records.
  const change = async (what: () => Promise<TaskBoard>) => {
    const n = ++asked.current
    const b = await what()
    if (n === asked.current) setBoard(b)
  }
  const act = (what: () => Promise<TaskBoard>) => {
    setActionError(null)
    change(what).catch((err) => setActionError(message(err)))
  }
  const play = () => act(() => playTaskBoard(id))
  const pause = () => act(() => pauseTaskBoard(id))
  const retry = (records: number[]) => act(() => retryTaskBoardRecords(id, records))

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
          {
            id: 'taskboard:states',
            title: 'Add columns from states',
            group: 'Task board',
            hint: `A column for each state of a struct, to ${board.name}`,
            words: 'task board columns states lifecycle',
            run: () => setLaying(true),
          },
          board.playing
            ? {
                id: 'taskboard:pause',
                title: 'Pause the task board',
                group: 'Task board',
                hint: 'Stop starting runs; those going go on',
                words: 'task board pause stop',
                run: pause,
              }
            : {
                id: 'taskboard:play',
                title: 'Play the task board',
                group: 'Task board',
                hint: 'Start each column’s flow on the records it holds',
                words: 'task board play start run flows',
                run: play,
              },
        ]
      : [],
  }))

  // Saves the board's name and columns, which come back with their records.
  const save = (name: string, columns: TaskColumnEdit[]) => change(() => saveTaskBoard(id, { name, columns }))

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
        <button
          onClick={() => setLaying(true)}
          disabled={!board}
          title="Add a column for each state of a shared struct, its records going to the flow taking that state"
        >
          Columns from states
        </button>
        <button onClick={load} title="Fetch the latest records">
          Refresh
        </button>
        {board && (
          <PlayControls
            board={board}
            onPlay={play}
            onPause={pause}
            onLimit={(n) => act(() => saveTaskBoard(id, { name: board.name, maxRuns: n, columns: board.columns.map(editOf) }))}
          />
        )}
      </header>
      {(error || actionError) && (
        <div className="error-banner" role="alert">
          {error ?? actionError}
        </div>
      )}
      {board?.stopped && !board.playing && (
        <div className="task-board-stopped" role="status">
          It stopped playing: {board.stopped}
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
                runner={board.playing && board.playedBy ? board.playedBy : ''}
                onEdit={() => setEditing(i)}
                onOpen={(record) => setOpened({ column: i, record })}
                onRun={(record) => c.struct !== null && setRunning({ struct: c.struct, name: c.structName, record })}
                onRetry={retry}
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
      {laying && board && (
        <StatesDialog
          columns={board.columns}
          cardFor={(s) => board.columns.find((c) => c.struct === s && c.card !== null)?.card ?? null}
          onAdd={async (added) => {
            await save(board.name, [...board.columns.map(editOf), ...added])
            setLaying(false)
          }}
          onClose={() => setLaying(false)}
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
  return namedState(c.query, c.cards?.states ?? [])
}

// The one state of states a query names, as the struct writes it; none for
// a query naming no state, or several.
function namedState(query: string, states: string[]): string {
  const named = selection(STATE, parse(query).ast, 'state')
  if (named.length !== 1) return ''
  return states.find((s) => s.toLowerCase() === named[0].toLowerCase()) ?? ''
}

// The flows taking records in the state, by a query naming it, else, as
// none does, by one of any state, whether they take it so, and the others.
function byState(flows: RecordFlow[], state: string): { taking: RecordFlow[]; any: boolean; others: RecordFlow[] } {
  const naming = flows.filter((f) => state !== '' && f.states.includes(state))
  const taking = naming.length > 0 ? naming : flows.filter((f) => f.states.includes(''))
  return { taking, any: naming.length === 0 && taking.length > 0, others: flows.filter((f) => !taking.includes(f)) }
}

// The value of a flow's select for a column whose records rest.
const REST = 'rest'

// The states a flow's queries take records in, as its option says them.
const takesOf = (f: RecordFlow) => `takes ${f.states.map((s) => s || 'any state').join(', ')}`

// Names, as a sentence lists them.
const listOf = (names: string[]) =>
  names.length < 2 ? names.join('') : `${names.slice(0, -1).join(', ')} and ${names[names.length - 1]}`

// What a column naming no flow gives a record in the state to, said short.
function byItsState(flows: RecordFlow[], state: string): string {
  if (state === '') return 'The flow taking each record’s state'
  const { taking, any } = byState(flows, state)
  if (taking.length === 1) return `The flow taking ${state}: ${taking[0].name}${any ? ', of any state' : ''}`
  if (taking.length === 0) return `The flow taking ${state}: none does`
  return `The flow taking ${state}: ${taking.length} do, so none`
}

// The flows a column of a state can give its records to: by default the
// one taking each record's state, else none, its records resting, or one
// it names, those taking its state first, and the one it has, if neither,
// by the name given.
function FlowOptions({ flows, state, kept }: { flows: RecordFlow[]; state: string; kept?: { id: number; name: string } }) {
  const { taking, others } = byState(flows, state)
  return (
    <>
      <option value="">{byItsState(flows, state)}</option>
      <option value={REST}>None: its records rest here</option>
      {[...taking, ...others].map((f) => (
        <option key={f.id} value={f.id}>
          {f.name} · {takesOf(f)}
        </option>
      ))}
      {kept && !flows.some((f) => f.id === kept.id) && <option value={kept.id}>{kept.name || 'A flow you can’t start on these records'}</option>}
    </>
  )
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

// Why the board playing starts nothing in a column, its runs runner's, or
// those of whoever reads it while it doesn't play.
function flowProblemOf(c: TaskColumn, runner: string) {
  const flow = c.flowName || 'Its flow'
  switch (c.flowProblem) {
    case 'gone':
      return <>Its flow was deleted for good, so the board starts nothing here; change the column to pick another.</>
    case 'trash':
      return (
        <>
          {flow} is in the trash, so the board starts nothing here; <a href="#/trash">restore it</a> to go on.
        </>
      )
    case 'hidden':
      return <>Its flow isn’t one {runner || 'you'} see{runner ? 's' : ''}, so the board starts nothing here.</>
    case 'denied':
      return (
        <>
          {runner || 'You'} may not run {flow}, so the board starts nothing here.
        </>
      )
    case 'unqueried':
      return <>{flow} no longer queries these records, so the board starts nothing here; change the column to pick another.</>
  }
  return null
}

// What playing the board does in a column: nothing, if its records rest;
// else the flow it gives its records to, the one it names or the one taking
// each state it holds, by naming it or by querying any state, how many of
// them it works on at once and how many have runs going; the states it
// starts nothing on, as several flows take them, or none; why the flow it
// names wouldn't start, if it wouldn't; and the records it skips as stuck,
// to try again at once.
function ColumnFlow({ column: c, runner, onRetry }: { column: TaskColumn; runner: string; onRetry: (records: number[]) => void }) {
  if (c.maxRuns === 0) return <p className="task-column-rest">Records rest here; the board starts no flow on them.</p>
  const runs = Object.entries(c.cards?.runs ?? {})
  const going = runs.filter(([, r]) => r && r.status !== 'stuck').length
  const stuck = runs.filter(([, r]) => r?.status === 'stuck').map(([id]) => Number(id))
  const named = c.flow !== null
  const states = c.byState ?? []
  const one = states.filter((s) => s.flows.length === 1)
  const several = states.filter((s) => s.flows.length > 1)
  const none = states.filter((s) => s.flows.length === 0)
  return (
    <>
      {(named || one.length > 0) && (
        <p className="task-column-flow">
          <PlayIcon />
          {named ? (
            c.flowName ? (
              <a href={`#/flows/${c.flow}`} title="Open the flow this column gives its records to, whatever their state">
                {c.flowName}
              </a>
            ) : (
              <span>A flow you don’t see</span>
            )
          ) : (
            one.map((s, i) => (
              <span key={s.state}>
                {i > 0 && ', '}
                {states.length > 1 && `${s.state}: `}
                <a
                  href={`#/flows/${s.flows[0].id}`}
                  title={
                    s.any
                      ? `Open the flow taking records in ${s.state}: it queries them in any state, and no flow takes ${s.state} by name`
                      : `Open the flow taking records in ${s.state}, which the board gives them to`
                  }
                >
                  {s.flows[0].name || 'A flow you don’t see'}
                </a>
                {s.any && ' (any state)'}
              </span>
            ))
          )}
          {c.maxRuns !== null && <span title="How many of its records the board works on at once at most">· {c.maxRuns} at once</span>}
          {going > 0 && (
            <span className="task-column-going" title="Records here with a run going">
              {going} going
            </span>
          )}
        </p>
      )}
      {several.map((s) => (
        <p key={s.state} className="task-column-problem">
          {listOf(s.flows.map((f) => f.name || 'a flow you don’t see'))} {s.flows.length === 2 ? 'both' : 'all'}{' '}
          {s.any ? `query any state, and no flow takes ${s.state} by name` : `take ${s.state}`}, so the board starts none of them here;
          change the column to pick the one, or to let its records rest.
        </p>
      ))}
      {!named && none.length > 0 && <p className="task-column-rest">No flow takes {listOf(none.map((s) => s.state))}, so records rest here.</p>}
      {c.flowProblem && <p className="task-column-problem">{flowProblemOf(c, runner)}</p>}
      {stuck.length > 0 && (
        <p className="task-column-stuck">
          {stuck.length} stuck
          <button className="link-button" onClick={() => onRetry(stuck)} title="Start the flow on them again, from the board's next turn">
            Try {stuck.length === 1 ? 'it' : 'them'} again
          </button>
        </p>
      )}
    </>
  )
}

function Column({
  column: c,
  runner,
  onEdit,
  onOpen,
  onRun,
  onRetry,
}: {
  column: TaskColumn
  // Whose the board's runs are while it plays, by name; empty when it
  // doesn't, or nobody signs in.
  runner: string
  onEdit: () => void
  // Opens a record of it, or a new one for none.
  onOpen: (record: SharedRecord | null) => void
  onRun: (record: number) => void
  onRetry: (records: number[]) => void
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
          title="Change the column: its name, struct, query, order, flow and place"
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
      {!c.problem && <ColumnFlow column={c} runner={runner} onRetry={onRetry} />}
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
                  run={cards.runs[r.id]}
                  onOpen={() => onOpen(r)}
                  onRun={() => onRun(r.id)}
                  onRetry={() => onRetry([r.id])}
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

// Why the board skips a record as stuck, said short, and in full.
const STUCK: Record<NonNullable<CardRun['why']>, [string, string]> = {
  failed: ['its run failed', 'Its run failed. Once what failed is fixed, try it again.'],
  interrupted: ['its run was stopped', 'Its run was stopped before it ended. Try it again to start it anew.'],
  terminated: ['its run ended early', 'Its run ended on a value another run had stored already. Correct the record, or try it again.'],
  unmoved: [
    'its run left it here',
    'Its run ended done but left the record in the state it took it in: the flow needs an output link leaving it in another state. Trying again runs the flow the same way.',
  ],
  rounds: [
    'it went round too often',
    'The board started its flows on it from this state several times with no person in between. Correct it, or try it again to let it go round more.',
  ],
}

// The run going on a card's record, linked: its flow and the steps going in
// it, if the board shows its flow, and for how long; or why the board skips
// it as stuck, with the run that left it so and trying it again.
function CardWork({ run, onRetry }: { run: CardRun; onRetry: () => void }) {
  const link = (
    <a href={`#/flows/${run.flow}/runs/${run.run}`} title="Open the run on its flow's board">
      run #{run.run}
    </a>
  )
  if (run.status !== 'stuck') {
    const steps = run.steps ?? []
    return (
      <div className={`task-card-work ${run.status}`}>
        <span className="task-card-work-status">
          <span className="task-card-work-dot" aria-hidden />
          {run.status === 'waiting' ? 'Waiting for a person' : 'Running'}
        </span>
        {run.flowName && (
          <span className="task-card-work-what" title={steps.length > 0 ? `${run.flowName}, at ${listOf(steps)}` : run.flowName}>
            {run.flowName}
            {steps.length > 0 && `: ${listOf(steps)}`}
          </span>
        )}
        {run.started && (
          <span title={`Started ${formatTime(run.started)}`}>for {formatDuration(run.started, new Date().toISOString()) || '0s'}</span>
        )}
        {link}
      </div>
    )
  }
  const [short, full] = STUCK[run.why ?? 'failed']
  return (
    <div className="task-card-work stuck">
      <span className="task-card-work-status" title={full}>
        Stuck: {short}
      </span>
      {link}
      <button className="link-button" onClick={onRetry} title="Start its column's flow on it again, from the board's next turn">
        Try again
      </button>
    </div>
  )
}

// A record as a card, showing what its column picks: its key heading it,
// the first other field picked as its title and the rest under it, each
// named, those it has no value for left out; then its state, when it was
// last written, and the run that last moved it on, linked to it; and the
// run going on it, or why the board skips it as stuck. A card with nothing
// else to show is named by its key.
function Card({
  record,
  cards,
  card,
  run: work,
  onOpen,
  onRun,
  onRetry,
}: {
  record: SharedRecord
  cards: TaskCards
  card: TaskCard
  run?: CardRun
  onOpen: () => void
  onRun: () => void
  onRetry: () => void
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
      {work && <CardWork run={work} onRetry={onRetry} />}
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
// it is typed, how they sort, the flow moving them on and how many of them
// it works on at once, what its cards show, and its place on the board. A
// struct picked takes what another column of it on the board shows, if one
// was told; what is picked anew goes for the board's other columns of it
// too, unless untold. One whose struct they don't see keeps it, and what
// its cards show, which only those who see it change. A column whose query
// names a state is named after it unless named otherwise. Its records go to
// the flow taking each one's state, which the dialog says, unless it names
// a flow to give them all to, or lets them rest.
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
  // The flow it gives its records to; none for the one taking each state.
  const [flow, setFlow] = useState<number | null>(column?.flow ?? null)
  const [maxRuns, setMaxRuns] = useState<number | null>(column?.maxRuns ?? null)
  // The flows whoever changes it may start on the struct's records.
  const [flows, setFlows] = useState<RecordFlow[] | null>(null)
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

  useEffect(() => {
    setFlows(null)
    if (!seen || struct === null) return
    let current = true
    listRecordFlows(struct, []).then(
      (r) => current && setFlows(r.flows),
      () => current && setFlows([]),
    )
    return () => {
      current = false
    }
  }, [seen, struct])
  const state = namedState(query, picked?.states ?? [])
  const { taking, any } = byState(flows ?? [], state)
  const flowName = flows?.find((f) => f.id === flow)?.name ?? (flow === column?.flow ? column.flowName : '')
  const named = name.trim() || state
  const rests = maxRuns === 0
  const flowNote =
    flows === null
      ? 'Looking for the flows querying its records…'
      : rests
        ? 'While the board plays, it starts no flow on the records here: they rest, for a person to move on, or as done.'
        : flow !== null
          ? `While the board plays, it gives every record here to ${flowName || 'that flow'}, whatever its state, a run per record.`
          : state === ''
            ? 'While the board plays, each record here goes to the flow taking records in its state, if one does, a run per record.'
            : taking.length === 1
              ? any
                ? `While the board plays, each record here goes to ${taking[0].name}, which queries records in any state, as no flow takes ${state} by name, a run per record.`
                : `While the board plays, each record here goes to ${taking[0].name}, the flow taking ${state}, a run per record.`
              : taking.length > 1
                ? `${listOf(taking.map((f) => f.name))} all ${any ? `query any state, and no flow takes ${state} by name` : `take ${state}`}, so the board starts none of them here; pick the one, or let its records rest.`
                : `No flow you may run takes ${state}, so its records rest here, for a person to move on, or as done.`
  const pickFlow = (value: string) => {
    setFlow(value && value !== REST ? Number(value) : null)
    setMaxRuns(value === REST ? 0 : rests ? null : maxRuns)
  }

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
    if (!named || struct === null) return
    const edit = { name: named, struct, query: query.trim(), sort, card, flow, maxRuns }
    run(() => onSave(edit, at, changed && everywhere))
  }

  const pick = (s: number | null) => {
    setStruct(s)
    setCard(s === null ? null : s === column?.struct ? column.card : cardFor(s))
    setChanged(false)
    setFlow(column !== null && s === column.struct ? column.flow : null)
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
          <input autoFocus value={name} maxLength={255} placeholder={state || 'Such as To do'} onChange={(e) => setName(e.target.value)} />
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
          <div className="task-column-moving">
            <label className="panel-field">
              Moved on by
              <select value={rests ? REST : (flow ?? '')} onChange={(e) => pickFlow(e.target.value)}>
                <FlowOptions
                  flows={flows ?? []}
                  state={state}
                  kept={column?.flow != null && struct === column.struct ? { id: column.flow, name: column.flowName } : undefined}
                />
              </select>
            </label>
            {!rests && (
              <label className="panel-field">
                At once, at most
                <select value={maxRuns ?? ''} onChange={(e) => setMaxRuns(e.target.value ? Number(e.target.value) : null)}>
                  <option value="">As many as the board runs</option>
                  {LIMITS.map((n) => (
                    <option key={n} value={n}>
                      {n} {n === 1 ? 'record' : 'records'}
                    </option>
                  ))}
                </select>
              </label>
            )}
            <span className="panel-note">{flowNote}</span>
          </div>
        )}
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
          <button type="submit" disabled={saving || !named || struct === null}>
            {column ? 'Save' : 'Add'}
          </button>
        </div>
      </form>
      {confirmDialog}
    </dialog>
  )
}

// How many runs a board, or a column, may have going at once, to pick from.
const LIMITS = [1, 2, 3, 4, 5, 6, 8, 10, 15, 20, 30, 40, 50]

// Play, or Pause while it plays, how many of the board's runs go at once,
// and while it plays, whose runs they are and how many are going.
function PlayControls({
  board,
  onPlay,
  onPause,
  onLimit,
}: {
  board: TaskBoard
  onPlay: () => void
  onPause: () => void
  onLimit: (n: number) => void
}) {
  const limits = LIMITS.includes(board.maxRuns) ? LIMITS : [...LIMITS, board.maxRuns].sort((x, y) => x - y)
  return (
    <div className="task-board-play">
      {board.playing && (
        <span className="task-board-playing" title="Its runs going, of as many as it runs at once">
          <span className="task-board-playing-dot" aria-hidden />
          Playing{board.playedBy && ` for ${board.playedBy}`} · {board.running} of {board.maxRuns} going
        </span>
      )}
      <label className="task-board-limit" title="How many of the board's runs go at once at most">
        At once
        <select value={board.maxRuns} onChange={(e) => onLimit(Number(e.target.value))}>
          {limits.map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </select>
      </label>
      {board.playing ? (
        <button className="task-board-pause" onClick={onPause} title="Stop starting runs; those going go on">
          <PauseIcon /> Pause
        </button>
      ) : (
        <button
          className="task-board-start"
          onClick={onPlay}
          title="Keep giving each record no run is going on to the flow taking its state, a run per record, as records come and move on"
        >
          <PlayIcon /> Play
        </button>
      )}
    </div>
  )
}

// Lays out a column for each state of a shared struct, in the struct's
// order, each named after its state and holding the records in it, which
// go to the flow taking that state unless a flow is picked for it or they
// rest, as the last state's do at first, as done; a state the board has a
// column of already is left unticked.
function StatesDialog({
  columns,
  cardFor,
  onAdd,
  onClose,
}: {
  columns: TaskColumn[]
  // What the cards of the board's columns of a struct show, if told.
  cardFor: (struct: number) => TaskCard | null
  onAdd: (columns: TaskColumnEdit[]) => Promise<void>
  onClose: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  // The board's columns as it opened, so its refreshing undoes no ticks.
  const [existing] = useState(columns)
  const [structs, setStructs] = useState<TaskStruct[] | null>(null)
  const [struct, setStruct] = useState<number | null>(null)
  const [flows, setFlows] = useState<RecordFlow[] | null>(null)
  // Each state, whether it gets a column, and the flow moving it on, or
  // none, its records resting.
  const [rows, setRows] = useState<{ state: string; on: boolean; flow: number | null; rest: boolean; has: boolean }[]>([])
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

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
  useEffect(() => {
    setFlows(null)
    setRows([])
    if (!picked) return
    let current = true
    listRecordFlows(picked.id, []).then(
      (r) => {
        if (!current) return
        setFlows(r.flows)
        setRows(
          picked.states.map((state, i) => {
            const has = existing.some((c) => c.struct === picked.id && namedState(c.query, picked.states) === state)
            return { state, on: !has, flow: null, rest: i === picked.states.length - 1, has }
          }),
        )
      },
      (err) => current && setError(message(err)),
    )
    return () => {
      current = false
    }
  }, [picked, existing])

  const change = (i: number, row: Partial<(typeof rows)[number]>) => setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...row } : r)))
  const ticked = rows.filter((r) => r.on)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!picked || ticked.length === 0) return
    setSaving(true)
    setError(null)
    try {
      await onAdd(
        ticked.map((r) => ({
          name: r.state,
          struct: picked.id,
          query: `state:${quote(r.state)}`,
          sort: '',
          card: cardFor(picked.id),
          flow: r.rest ? null : r.flow,
          maxRuns: r.rest ? 0 : null,
        })),
      )
    } catch (err) {
      setError(message(err))
      setSaving(false)
    }
  }

  return (
    <dialog
      ref={ref}
      className="review-dialog task-column-dialog"
      aria-label="Add columns from states"
      onClose={onClose}
      onKeyDown={(e) => e.stopPropagation()}
    >
      <header className="panel-header">
        <h2>Add columns from states</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <form className="task-column-form" onSubmit={submit}>
        <label className="panel-field">
          Records of
          <select value={struct ?? ''} onChange={(e) => setStruct(e.target.value ? Number(e.target.value) : null)}>
            <option value="">Pick a shared struct</option>
            {structs?.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </select>
        </label>
        {picked && picked.states.length === 0 && (
          <p className="panel-note">{picked.name} has no states yet; add them on its board, one for each step its records go through.</p>
        )}
        {picked && flows === null && picked.states.length > 0 && <p className="panel-note">Looking for the flows querying its records…</p>}
        {flows !== null && rows.length > 0 && (
          <>
            <ol className="task-states">
              {rows.map((r, i) => (
                <li key={r.state} className="task-state">
                  <label className="panel-check">
                    <input type="checkbox" checked={r.on} onChange={(e) => change(i, { on: e.target.checked })} />
                    {r.state}
                    {r.has && <span className="task-card-pick-note">on the board already</span>}
                  </label>
                  {r.on && (
                    <select
                      aria-label={`What moves records in ${r.state} on`}
                      value={r.rest ? REST : (r.flow ?? '')}
                      onChange={(e) => {
                        const v = e.target.value
                        change(i, { rest: v === REST, flow: v && v !== REST ? Number(v) : null })
                      }}
                    >
                      <FlowOptions flows={flows} state={r.state} />
                    </select>
                  )}
                </li>
              ))}
            </ol>
            <span className="panel-note">
              Each column holds the records in its state, and while the board plays, each goes to the flow taking that state, a run per
              record; where no flow takes it, or the column lets them rest, as the last state’s does at first, records rest, for a person,
              or as done.
            </span>
          </>
        )}
        {error && <p className="panel-warning">{error}</p>}
        <div className="review-actions">
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" disabled={saving || ticked.length === 0}>
            Add {ticked.length === 1 ? 'a column' : `${ticked.length} columns`}
          </button>
        </div>
      </form>
    </dialog>
  )
}
