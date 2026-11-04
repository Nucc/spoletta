import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type FormEvent } from 'react'
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
  type ReferringCount,
  type TaskColumn,
  type TaskColumnEdit,
  type TaskStruct,
} from './api'
import { type Json } from './Data'
import {
  AlertIcon,
  ExternalIcon,
  GearIcon,
  HistoryIcon,
  PauseIcon,
  PencilIcon,
  PlayIcon,
  PlusIcon,
  SidebarIcon,
  TaskBoardIcon,
  TransitionIcon,
} from './icons'
import { timeAgo } from './home'
import { indexFacets, parse, quote, selection, type Facets } from './query'
import { RecordDialog, type RecordTab } from './RecordDialog'
import { RecordRunDialog } from './RecordRunDialog'
import { formatDuration, formatTime } from './Runs'
import { QueryBar } from './Search'
import { useConfirm } from './Confirm'
import { usePaletteSource } from './CommandPalette'
import { HomeFrame } from './FlowList'
import ErrorBanner from './ErrorBanner'
import { FlowStrip, type FlowChoice } from './TaskFlows'
import { byState, conflicts, strip } from './boardflows'

// How often the board's records are asked for again, as flows and people
// move them on; more often while it plays or runs go on its records, so a
// card shows its run while it goes.
const REFRESH_MS = 10_000
const BUSY_REFRESH_MS = 2_000

// Where whether task boards hide the side menu is remembered, for every
// board in this browser.
const RAIL_KEY = 'spoletta.taskboard.rail'

// Where whether task boards show their Flows strip is remembered, for every
// board in this browser.
const FLOWS_KEY = 'spoletta.taskboard.flows'

// What went wrong, in the server's words.
const message = (err: unknown) => (err instanceof Error ? err.message : String(err))

const editOf = (c: TaskColumn): TaskColumnEdit => ({
  name: c.name,
  struct: c.struct,
  query: c.query,
  sort: c.sort,
  flow: c.flow,
  maxRuns: c.maxRuns,
})

// What a board's cards of a struct show unless told: the record's key as
// its ID, the struct's first other field as the title, its state and when
// it was last written.
const defaultCard = (fields: string[], key: string): TaskCard => ({
  fields: fields.filter((f) => f !== key).slice(0, 1),
  state: true,
  updated: true,
  run: false,
})

// What the board's cards of a column's struct show.
const cardOf = (board: TaskBoard, c: TaskColumn, cards: TaskCards): TaskCard =>
  (c.struct !== null && board.cards[c.struct]) || defaultCard(cards.fields, cards.key)

// A field's value as the address a card's ID links to: only a web page's,
// so a value such as javascript: stays text.
const linkOf = (v: string | null): string | null => (v !== null && /^https?:\/\//i.test(v.trim()) ? v.trim() : null)

// A task board's page, #/taskboards/<id>: a column for each of its queries
// of a shared struct's records, as the Records page searches them, each
// record a card showing what the board picks of its struct's records: a
// field as its ID, linked to the address another holds if told, such as
// the ticket's page in Jira, a field as its title and others under it, its
// state, when it was last written and the run that last moved it on. The
// records are asked for again every so often, and as the page is shown
// again, so cards go from column to column as flows and people move them
// on. A card opens its record, to correct it as on the Records page, or
// starts a flow on it. A column adds a record, in the state its query names
// if it names one, opens its records on the Records page, and is changed or
// taken off the board; the board's settings rename it and pick what its
// cards show, and columns are laid out from a struct's states, one for
// each.
//
// Play keeps giving each record no run is going on to the flow taking its
// state, a run per record, as many at once as the board allows, or to the
// flow its column names; records in a state no flow takes rest there (see
// taskboard_play.go). Each column says which flow takes its state, or why
// none starts. A card shows the run going on its record, its flow, the
// steps going and for how long, asked for every couple of seconds while
// the board plays or runs go, or that the board skips it as stuck and why,
// to try again. Over the columns, how many records of the whole board run,
// wait or are stuck.
export default function TaskBoardPage({ id }: { id: number }) {
  const [board, setBoard] = useState<TaskBoard | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  // The column being changed, by its place, or a new one.
  const [editing, setEditing] = useState<number | 'new' | null>(null)
  // The record opened, of a column by its place, or a new one added to it,
  // on its fields or its history.
  const [opened, setOpened] = useState<{ column: number; record: SharedRecord | null; tab?: RecordTab } | null>(null)
  // The record a flow is being started on.
  const [running, setRunning] = useState<{ struct: number; name: string; record: number } | null>(null)
  // Whether the board's settings are open.
  const [setting, setSetting] = useState(false)
  // Whether columns are being laid out from a struct's states.
  const [laying, setLaying] = useState(false)
  // Whether the side menu is hidden, giving the columns the window's width.
  const [railHidden, setRailHidden] = useState(() => {
    try {
      return localStorage.getItem(RAIL_KEY) === 'hidden'
    } catch {
      return false
    }
  })
  const toggleRail = () => {
    const hidden = !railHidden
    setRailHidden(hidden)
    try {
      if (hidden) localStorage.setItem(RAIL_KEY, 'hidden')
      else localStorage.removeItem(RAIL_KEY)
    } catch {
      // Only remembered while it can be.
    }
  }
  // Whether the Flows strip shows over the columns.
  const [flowsShown, setFlowsShown] = useState(() => {
    try {
      return localStorage.getItem(FLOWS_KEY) === 'shown'
    } catch {
      return false
    }
  })
  const toggleFlows = () => {
    const shown = !flowsShown
    setFlowsShown(shown)
    try {
      if (shown) localStorage.setItem(FLOWS_KEY, 'shown')
      else localStorage.removeItem(FLOWS_KEY)
    } catch {
      // Only remembered while it can be.
    }
  }
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

  // What the Flows strip draws of each column: its state, the flow moving
  // its records on and where to; and how many columns several flows take
  // the state of, starting none, which the Flows button counts.
  const items = useMemo(
    () =>
      board
        ? strip(
            board.columns.map((c) => ({
              struct: c.struct,
              state: c.cards ? namedState(c.query, c.cards.states) : '',
              flow: c.flow,
              rests: c.maxRuns === 0,
              flowProblem: c.flowProblem,
              shown: c.cards !== undefined,
            })),
            board.flows ?? {},
          )
        : [],
    [board],
  )
  const conflicting = conflicts(items)
  // Where the columns are laid out, which the strip draws over: each one's
  // centre and their width, measured as they show and whenever they move.
  const row = useRef<HTMLDivElement>(null)
  const [laidOut, setLaidOut] = useState<{ centers: number[]; width: number }>({ centers: [], width: 0 })
  const columnCount = board?.columns.length ?? 0
  useLayoutEffect(() => {
    const el = row.current
    if (!el || !flowsShown) return
    const measure = () => {
      const centers = (Array.from(el.children) as HTMLElement[]).map((c) => Math.round(c.offsetLeft + c.offsetWidth / 2))
      const width = el.scrollWidth
      setLaidOut((l) => (l.width === width && l.centers.join() === centers.join() ? l : { centers, width }))
    }
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    return () => observer.disconnect()
  }, [flowsShown, columnCount, railHidden])
  // Saves the flow the strip's picker picked for the column, by its place,
  // as its dialog saves it: a column resting names no flow, and one
  // resting no more works on as many records as the board lets it.
  const pickFlow = (at: number, choice: FlowChoice) =>
    change(() =>
      saveTaskBoard(id, {
        name: board!.name,
        columns: board!.columns.map((c, i) =>
          i !== at
            ? editOf(c)
            : { ...editOf(c), flow: choice.rest ? null : choice.flow, maxRuns: choice.rest ? 0 : c.maxRuns === 0 ? null : c.maxRuns },
        ),
      }),
    )

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
          {
            id: 'taskboard:settings',
            title: 'Task board settings',
            group: 'Task board',
            hint: `Rename ${board.name}, and pick what its cards show`,
            words: 'task board settings rename name cards fields id url link',
            run: () => setSetting(true),
          },
          {
            id: 'taskboard:flows',
            title: flowsShown ? 'Hide the flows moving records on' : 'Show the flows moving records on',
            group: 'Task board',
            hint: flowsShown
              ? `Over ${board.name}’s columns`
              : `Which flow moves each column’s records on, and to which state, over ${board.name}’s columns`,
            words: 'task board flows states transitions arrows moves lifecycle pick',
            run: toggleFlows,
          },
          {
            id: 'taskboard:rail',
            title: railHidden ? 'Show the side menu' : 'Hide the side menu',
            group: 'Task board',
            hint: railHidden ? 'Beside the task board' : 'Giving the task board’s columns the window’s width',
            words: 'task board side menu sidebar rail hide show width',
            run: toggleRail,
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

  // Saves the board's name and columns, which come back with their records;
  // what its cards show stays.
  const save = (name: string, columns: TaskColumnEdit[]) => change(() => saveTaskBoard(id, { name, columns }))

  // The columns with the one changed, or a new one, in the place picked.
  const placed = (edit: TaskColumnEdit, at: number): TaskColumnEdit[] => {
    const columns = board!.columns.map(editOf)
    if (editing !== 'new' && editing !== null) columns.splice(editing, 1)
    columns.splice(at, 0, edit)
    return columns
  }

  const column = opened !== null ? board?.columns[opened.column] : undefined
  const struct = column?.struct ?? null

  return (
    <div className="flows-page data-page task-board-page home">
      <header className="toolbar">
        <h1>
          <a className="home" href="#/" title="Every flow, task board, procedure and struct">
            Home
          </a>
          <span className="crumb-separator" aria-hidden>
            /
          </span>
          <span className="task-board-name">
            <TaskBoardIcon /> {board?.name ?? '…'}
          </span>
        </h1>
        <button
          onClick={toggleRail}
          aria-label={railHidden ? 'Show the side menu' : 'Hide the side menu'}
          aria-pressed={!railHidden}
          title={railHidden ? 'Show the side menu' : 'Hide the side menu, giving the columns the window’s width'}
        >
          <SidebarIcon />
        </button>
        <button onClick={() => setSetting(true)} disabled={!board} title="Rename the task board, and pick what its cards show">
          <GearIcon /> Settings
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
        <button
          className="task-board-flows"
          onClick={toggleFlows}
          disabled={!board}
          aria-pressed={flowsShown}
          title={
            (flowsShown ? 'Hide the flows moving each state’s records on' : 'Show which flow moves each column’s records on, and to which state') +
            (conflicting > 0 ? `; several flows take the state of ${conflicting === 1 ? 'a column' : `${conflicting} columns`}, starting none` : '')
          }
        >
          <TransitionIcon /> Flows
          {conflicting > 0 && <span className="task-board-flows-count">{conflicting}</span>}
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
      <ErrorBanner error={error ?? actionError} />
      {board?.stopped && !board.playing && (
        <div className="task-board-stopped" role="status">
          It stopped playing: {board.stopped}
        </div>
      )}
      <HomeFrame at="taskboard" rail={!railHidden}>
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
          <>
            <BoardWork columns={board.columns} />
            <main className="task-board">
              {flowsShown && laidOut.centers.length === board.columns.length && (
                <FlowStrip
                  items={items}
                  columns={board.columns}
                  flows={board.flows ?? {}}
                  centers={laidOut.centers}
                  width={laidOut.width}
                  runner={board.playing && board.playedBy ? board.playedBy : ''}
                  onPick={pickFlow}
                />
              )}
              <div className="task-columns" ref={row}>
                {board.columns.map((c, i) => (
                  <Column
                    key={i}
                    column={c}
                    card={c.cards ? cardOf(board, c, c.cards) : null}
                    onEdit={() => setEditing(i)}
                    onOpen={(record, tab) => setOpened({ column: i, record, tab })}
                    onRun={(record) => c.struct !== null && setRunning({ struct: c.struct, name: c.structName, record })}
                    onRetry={retry}
                  />
                ))}
              </div>
            </main>
          </>
        )}
      </HomeFrame>
      {editing !== null && board && (
        <ColumnDialog
          column={editing === 'new' ? null : board.columns[editing]}
          places={board.columns.length + (editing === 'new' ? 1 : 0)}
          place={editing === 'new' ? board.columns.length : editing}
          runner={board.playing && board.playedBy ? board.playedBy : ''}
          onSave={async (edit, at) => {
            await save(board.name, placed(edit, at))
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
          onAdd={async (added) => {
            await save(board.name, [...board.columns.map(editOf), ...added])
            setLaying(false)
          }}
          onClose={() => setLaying(false)}
        />
      )}
      {setting && board && (
        <BoardSettings
          board={board}
          onSave={async (name, cards) => {
            await change(() => saveTaskBoard(id, { name, columns: board.columns.map(editOf), cards }))
            setSetting(false)
          }}
          onClose={() => setSetting(false)}
        />
      )}
      {opened !== null && column?.cards && struct !== null && (
        <RecordDialog
          title={opened.record ? `${column.structName} · ${keyOf(opened.record, column.cards)}` : `Add a record to ${column.structName}`}
          struct={struct}
          tab={opened.tab}
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

// How the board's work goes, over its columns, as some may be scrolled out
// of sight: its records running, waiting for a person and stuck, each
// counted once though two columns hold it.
function BoardWork({ columns }: { columns: TaskColumn[] }) {
  const status = new Map<string, CardRun['status']>()
  for (const c of columns) for (const [id, r] of Object.entries(c.cards?.runs ?? {})) if (r) status.set(`${c.struct}:${id}`, r.status)
  const all = [...status.values()]
  const running = all.filter((s) => s === 'running').length
  const waiting = all.filter((s) => s === 'waiting').length
  const stuck = all.filter((s) => s === 'stuck').length
  return (
    <p className="task-board-work">
      {running > 0 && <span className="running">{running} running</span>}
      {waiting > 0 && <span className="waiting">{waiting} waiting for a person</span>}
      {stuck > 0 && <span className="stuck">{stuck} stuck</span>}
      {running + waiting + stuck === 0 && <span>Nothing running</span>}
    </p>
  )
}

// A column: its name, how many records it holds and their cards; which
// struct, query and flow it has is in its dialog.
function Column({
  column: c,
  card,
  onEdit,
  onOpen,
  onRun,
  onRetry,
}: {
  column: TaskColumn
  // What the board's cards of its struct show, while it shows its records.
  card: TaskCard | null
  onEdit: () => void
  // Opens a record of it, or a new one for none, on its fields or history.
  onOpen: (record: SharedRecord | null, tab?: RecordTab) => void
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
            {shown < cards.matched ? (
              <>
                {shown} <span className="task-column-of">of {cards.matched}</span>
              </>
            ) : (
              cards.matched
            )}
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
      <div className="task-column-lane">
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
                    card={card ?? defaultCard(cards.fields, cards.key)}
                    run={cards.runs[r.id]}
                    onOpen={() => onOpen(r)}
                    onHistory={() => onOpen(r, 'history')}
                    referring={cards.referring?.[r.id]}
                    onReferring={() => onOpen(r, 'references')}
                    onRun={() => onRun(r.id)}
                    onRetry={() => onRetry([r.id])}
                  />
                ))}
              </ol>
            )}
            {cards.truncated && (
              <p className="task-column-note">Of the latest records written only, as there are more than a search reads.</p>
            )}
            <button className="task-column-add" onClick={() => onOpen(null)} title="Add a record by hand">
              <PlusIcon size={12} /> Add a record
            </button>
          </>
        ) : (
          <p className="task-column-problem">{problemOf(c)}</p>
        )}
      </div>
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
        <AlertIcon />
        Stuck: {short}
      </span>
      {link}
      <button className="task-card-retry" onClick={onRetry} title="Start its column's flow on it again, from the board's next turn">
        Try again
      </button>
    </div>
  )
}

// A record as a card, showing what the board picks of its struct's: the
// field picked as its ID heading it, else its key, linked to the address
// the field picked for it holds, which opens in a tab of its own, or else
// opening the record with the rest; the first other field picked as its
// title and the rest under it, each named, those it has no value for left
// out; then its state, when it was last written, and the run that last
// moved it on, linked to it; and the run going on it, or why the board
// skips it as stuck. Its history and running a flow on it are buttons in
// its corner, beside its ID.
function Card({
  record,
  cards,
  card,
  run: work,
  referring,
  onOpen,
  onHistory,
  onReferring,
  onRun,
  onRetry,
}: {
  record: SharedRecord
  cards: TaskCards
  card: TaskCard
  run?: CardRun
  // What refers to it, such as a ticket's pull requests; see references.go.
  referring?: ReferringCount[]
  onOpen: () => void
  onHistory: () => void
  onReferring: () => void
  onRun: () => void
  onRetry: () => void
}) {
  const content = isFields(record.content as Json) ? (record.content as { [key: string]: Json }) : {}
  const key = keyOf(record, cards)
  const idField = card.key || cards.key
  const id = textOf(content[idField]) ?? key
  const href = card.url ? linkOf(textOf(content[card.url])) : null
  const [title, ...rest] = card.fields.filter((f) => f !== idField).map((f) => [f, shownOf(content[f])] as const)
  const heading = title?.[1] ?? null
  const run = card.run && record.run !== null
  const look = work ? ` ${work.status}` : ''
  return (
    <li className={`task-card${look}`}>
      {href && (
        <a className="task-card-key task-card-link" href={href} target="_blank" rel="noopener noreferrer" title={`Open ${href}`}>
          {id} <ExternalIcon size={11} />
        </a>
      )}
      <button className="task-card-open" onClick={onOpen} title="Show the record, to correct it">
        {!href && <span className="task-card-key">{id}</span>}
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
          {card.state && <span className="record-state">{record.state || 'no state'}</span>}
          {card.updated && (
            <time dateTime={record.updated} title={`Last written ${formatTime(record.updated)}`}>
              {timeAgo(record.updated, Date.now())}
            </time>
          )}
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
      {referring && referring.length > 0 && (
        <div className="task-card-referring">
          {referring.map((r) => (
            <button
              key={`${r.name}:${r.attribute}`}
              className="record-ref"
              title={`The ${r.name} records whose ${r.attribute} names it`}
              onClick={onReferring}
            >
              {r.total} {r.name}
              {r.states.length > 0 && ` · ${r.states.map((s) => `${s.count} ${s.state || 'no state'}`).join(' · ')}`}
            </button>
          ))}
        </div>
      )}
      {work && <CardWork run={work} onRetry={onRetry} />}
      <div className="task-card-actions">
        <button className="icon-button" aria-label={`Show how ${id} got here`} title="Show how this record got here" onClick={onHistory}>
          <HistoryIcon />
        </button>
        <button className="icon-button" aria-label={`Run a flow on ${id}`} title="Run a flow on this record" onClick={onRun}>
          <PlayIcon />
        </button>
      </div>
    </li>
  )
}

// What a board's cards can show of a record besides its fields.
const RECORD_INFO: [keyof Omit<TaskCard, 'fields' | 'key' | 'url'>, string][] = [
  ['state', 'Its state'],
  ['updated', 'When it was last written'],
  ['run', 'The run that last moved it on'],
]

// What a board's cards of a struct show: the field heading each as its ID,
// the struct's key unless another is picked, and the field holding the
// address the ID links to, if any; then a checklist of the other fields,
// in the board's order, then any picked it no longer has, and the record's
// state, when it was last written and its run.
function CardSettings({
  card,
  struct,
  legend,
  onChange,
}: {
  card: TaskCard
  struct: TaskStruct
  legend: string
  onChange: (card: TaskCard) => void
}) {
  const idField = card.key || struct.key
  const fields = [...struct.fields, ...card.fields.filter((f) => !struct.fields.includes(f))]
  const toggle = (field: string) =>
    onChange({ ...card, fields: fields.filter((f) => (f === field ? !card.fields.includes(f) : card.fields.includes(f))) })
  // A field the struct no longer has, still picked, stays offered as it is.
  const offered = (picked: string | undefined) => (picked && !struct.fields.includes(picked) ? [...struct.fields, picked] : struct.fields)
  return (
    <fieldset className="task-card-picker">
      <legend>{legend}</legend>
      <div className="task-card-ids">
        <label className="panel-field">
          ID, at the top left
          <select value={idField} onChange={(e) => onChange({ ...card, key: e.target.value === struct.key ? undefined : e.target.value })}>
            {offered(card.key).map((f) => (
              <option key={f} value={f}>
                {f}
                {f === struct.key ? ' (its key)' : ''}
              </option>
            ))}
          </select>
        </label>
        <label className="panel-field">
          The ID links to
          <select value={card.url ?? ''} onChange={(e) => onChange({ ...card, url: e.target.value || undefined })}>
            <option value="">Nothing: it opens the record</option>
            {offered(card.url).map((f) => (
              <option key={f} value={f}>
                The address in {f}
              </option>
            ))}
          </select>
        </label>
      </div>
      <span className="panel-note">
        Pick the field holding the record’s own page, such as the ticket’s URL in Jira, and its ID opens it in a new tab. Only web addresses
        (http or https) become links.
      </span>
      <div className="task-card-picks">
        {fields
          .filter((f) => f !== idField)
          .map((f) => (
            <label key={f} className="panel-check">
              <input type="checkbox" checked={card.fields.includes(f)} onChange={() => toggle(f)} />
              {f}
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
      <span className="panel-note">The first field ticked is each card’s title, and the others show under it.</span>
    </fieldset>
  )
}

// The board's settings: its name, and what its cards of each struct its
// columns hold show, a struct picked anew for the first time showing what
// cards show unless told. Cards of a struct whoever changes it doesn't see
// keep what they show, as do those not changed here.
function BoardSettings({
  board,
  onSave,
  onClose,
}: {
  board: TaskBoard
  onSave: (name: string, cards: Record<string, TaskCard | undefined>) => Promise<void>
  onClose: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const [name, setName] = useState(board.name)
  const [cards, setCards] = useState(board.cards)
  const [structs, setStructs] = useState<TaskStruct[] | null>(null)
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

  // The structs the board's columns hold that whoever changes it sees, in
  // the order of their first columns.
  const held = [...new Set(board.columns.flatMap((c) => (c.struct !== null ? [c.struct] : [])))].flatMap(
    (s) => structs?.find((t) => t.id === s) ?? [],
  )

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    setSaving(true)
    setError(null)
    try {
      await onSave(name.trim(), cards)
    } catch (err) {
      setError(message(err))
      setSaving(false)
    }
  }

  return (
    <dialog
      ref={ref}
      className="review-dialog task-column-dialog"
      aria-label="Task board settings"
      onClose={onClose}
      onKeyDown={(e) => e.stopPropagation()}
    >
      <header className="panel-header">
        <h2>Task board settings</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <form className="task-column-form" onSubmit={submit}>
        <label className="panel-field">
          Name
          <input autoFocus value={name} maxLength={255} onChange={(e) => setName(e.target.value)} />
        </label>
        {structs === null && !error && <p className="panel-note">Looking up the fields of its records…</p>}
        {structs !== null && held.length === 0 && (
          <p className="panel-note">Once a column holds a struct’s records, pick here what their cards show.</p>
        )}
        {held.map((s) => (
          <CardSettings
            key={s.id}
            legend={held.length > 1 ? `Cards of ${s.name}` : 'Cards'}
            struct={s}
            card={cards[s.id] ?? defaultCard(s.fields, s.key)}
            onChange={(card) => setCards({ ...cards, [s.id]: card })}
          />
        ))}
        {error && <p className="panel-warning">{error}</p>}
        <div className="review-actions">
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" disabled={saving || !name.trim()}>
            Save
          </button>
        </div>
      </form>
    </dialog>
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
// it works on at once, and its place on the board; what its cards show is
// the board's, in its settings. One whose struct they don't see keeps it. A
// column whose query names a state is named after it unless named
// otherwise. Its records go to
// the flow taking each one's state, which the dialog says, unless it names
// a flow to give them all to, or lets them rest; one it names that won't
// start, it says why.
function ColumnDialog({
  column,
  places,
  place,
  runner,
  onSave,
  onRemove,
  onClose,
}: {
  // None for a new one.
  column: TaskColumn | null
  // How many places there are, the new one's counted, and the column's.
  places: number
  place: number
  // Whose the board's runs are while it plays, by name; empty when it
  // doesn't, or nobody signs in.
  runner: string
  onSave: (edit: TaskColumnEdit, place: number) => Promise<void>
  onRemove?: () => Promise<void>
  onClose: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const [name, setName] = useState(column?.name ?? '')
  const [struct, setStruct] = useState<number | null>(column?.struct ?? null)
  const [query, setQuery] = useState(column?.query ?? '')
  const [sort, setSort] = useState(column?.sort ?? '')
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
    const edit = { name: named, struct, query: query.trim(), sort, flow, maxRuns }
    run(() => onSave(edit, at))
  }

  const pick = (s: number | null) => {
    setStruct(s)
    setFlow(column !== null && s === column.struct ? column.flow : null)
  }

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
            {column?.flowProblem && flow === column.flow && struct === column.struct ? (
              <p className="panel-warning">{flowProblemOf(column, runner)}</p>
            ) : (
              <span className="panel-note">{flowNote}</span>
            )}
          </div>
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
  onAdd,
  onClose,
}: {
  columns: TaskColumn[]
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
