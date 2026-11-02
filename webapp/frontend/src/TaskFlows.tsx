import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import type { BoardFlow, TaskColumn, TaskFlowProblem } from './api'
import { AlertIcon, FlowIcon } from './icons'
import { ASIDE, BOX, byState, CHIP, layout, PILL, type Layout, type StripItem } from './boardflows'

// A task board's Flows strip, shown over its columns from the Flows button
// in its toolbar (TaskBoard.tsx): each column's state in a box over it, the
// flow the board playing gives its records to halfway to the next column,
// and arrows from that flow to the columns holding the states it leaves
// them in, the next column's straight on, others over the boxes, so a
// person sees their records' whole way from state to state. A column whose
// state several flows take, starting none, shows Pick a flow, its arrows
// dashed to where either would leave them; one resting, or whose state no
// flow takes, says so. Each flow, and what says why none moves the records
// on, opens a picker of the column's flow: the one taking its state, each
// flow taking it, or none, its records resting, saved as the column's flow
// as its dialog saves it. What it draws is worked out in boardflows.ts.

// What a picker saves: the flow the column names, none for the one taking
// its state, or that its records rest.
export type FlowChoice = { flow: number | null; rest: boolean }

// A flow's name, or what stands for one whoever looks doesn't see.
const nameOf = (f: BoardFlow) => f.name || 'A flow you don’t see'

// Names, as a sentence lists them, joined by and or or.
const listOf = (names: string[], last = 'and') =>
  names.length < 2 ? names.join('') : `${names.slice(0, -1).join(', ')} ${last} ${names[names.length - 1]}`

// Why the board starts no flow a column names, said short.
const PROBLEMS: Record<TaskFlowProblem, string> = {
  gone: 'was deleted for good, so nothing starts',
  trash: 'is in the trash, so nothing starts',
  hidden: 'isn’t one the board’s runs see, so nothing starts',
  denied: 'isn’t one the board’s runs may run, so nothing starts',
  unqueried: 'no longer queries these records, so nothing starts',
}

// Where a flow leaves records, said short: in the states, or where they were.
const leavesText = (f: BoardFlow, state: string) =>
  f.leaves.length > 0 ? `leaves them in ${listOf(f.leaves, 'or')}` : `leaves them in ${state}`

// The states a flow takes records in, said short.
const takesText = (f: BoardFlow) =>
  `takes ${listOf(
    f.states.map((s) => s || 'any state'),
    'or',
  )}`

// What the strip says under a column's flow, and whether it warns: why
// none starts, that it leaves its records where they were, where it leaves
// them that no column holds, or that the column names it.
function noteOf(it: StripItem, c: TaskColumn): { text: string; warn: boolean } | null {
  const off = it.targets.filter((t) => t.column === null).map((t) => t.state)
  switch (it.out.kind) {
    case 'conflict': {
      const names = it.out.flows.map(nameOf)
      return {
        text: `${listOf(names)} ${names.length === 2 ? 'both' : 'all'} ${it.out.any ? 'query any state' : `take ${it.state}`}, so none starts`,
        warn: true,
      }
    }
    case 'idle':
      return {
        text: `${nameOf(it.out.flow)} ${takesText(it.out.flow)}, so it starts nothing here`,
        warn: true,
      }
    case 'problem':
      return {
        text: `${c.flowName || 'Its flow'} ${PROBLEMS[it.out.problem]}`,
        warn: true,
      }
    case 'flow':
      if (it.out.flow.leaves.length === 0)
        return {
          text: `It leaves them in ${it.state}, where they stick`,
          warn: true,
        }
      if (off.length > 0) {
        const all = off.length === it.targets.length
        return {
          text: `${all ? 'It leaves them in' : 'And in'} ${listOf(off, 'or')}, which no column holds`,
          warn: false,
        }
      }
      return it.out.named ? { text: `Named by ${c.name}`, warn: false } : null
  }
  return null
}

// The arrows' path, from tail to tip.
const pathOf = (points: [number, number][]) => points.map(([x, y], i) => `${i === 0 ? 'M' : 'L'} ${x} ${y}`).join(' ')

export function FlowStrip({
  items,
  columns,
  flows,
  centers,
  width,
  runner,
  onPick,
}: {
  // What it draws of each column, by its place (boardflows.ts strip).
  items: (StripItem | null)[]
  columns: TaskColumn[]
  // The flows the board may start on each struct's records, by its id.
  flows: Record<string, BoardFlow[] | undefined>
  // Each column's centre, and the columns' width, as they are laid out.
  centers: number[]
  width: number
  // Whose the board's runs are while it plays, by name; empty when it
  // doesn't, or nobody signs in.
  runner: string
  // Saves the column's flow, by its place; refused, it throws.
  onPick: (column: number, choice: FlowChoice) => Promise<void>
}) {
  // The column whose flow is being picked, by its place.
  const [picking, setPicking] = useState<number | null>(null)
  const l = layout(items, centers, width)
  if (!items.some(Boolean)) {
    return (
      <p className="task-flows-empty">
        No column holds a single state, so there are no flows to draw. Columns from states give each state its column, and the strip the
        flow moving each one’s records on.
      </p>
    )
  }
  const chipAt = new Map(l.chips.map((c) => [c.column, c.x]))
  const picked = picking !== null ? items[picking] : null
  return (
    <div className="task-flows" style={{ width: l.width, height: l.height }}>
      <svg className="task-flows-arrows" width={l.width} height={l.height} aria-hidden="true">
        <defs>
          <marker
            id="task-flow-head"
            viewBox="0 0 8 8"
            refX="8"
            refY="4"
            markerWidth="8"
            markerHeight="8"
            markerUnits="userSpaceOnUse"
            orient="auto"
          >
            <path d="M0 0 L8 4 L0 8 Z" className="task-flow-head" />
          </marker>
          <marker
            id="task-flow-head-warn"
            viewBox="0 0 8 8"
            refX="8"
            refY="4"
            markerWidth="8"
            markerHeight="8"
            markerUnits="userSpaceOnUse"
            orient="auto"
          >
            <path d="M0 0 L8 4 L0 8 Z" className="task-flow-head warn" />
          </marker>
        </defs>
        {l.arrows.map((a, i) => (
          <path
            key={i}
            d={pathOf(a.points)}
            className={a.warn ? 'task-flow-arrow warn' : 'task-flow-arrow'}
            markerEnd={a.head ? `url(#${a.warn ? 'task-flow-head-warn' : 'task-flow-head'})` : undefined}
          />
        ))}
      </svg>
      {l.states.map((s) => {
        const it = items[s.column]!
        const c = columns[s.column]
        return (
          <div
            key={s.column}
            className="task-flow-state"
            style={{
              left: s.x - PILL / 2,
              top: l.top,
              width: PILL,
              height: BOX,
            }}
            title={`${c.structName ? `${c.structName}: ` : ''}records in ${it.state}, which ${c.name} holds`}
          >
            {it.state}
          </div>
        )
      })}
      {items.map((it) => {
        if (!it || it.column >= centers.length) return null
        const c = columns[it.column]
        const open = () => setPicking(it.column)
        if (it.out.kind === 'rest' || it.out.kind === 'none') {
          return (
            <button
              key={it.column}
              type="button"
              className="task-flow-rest"
              style={{
                left: centers[it.column] + PILL / 2 + 10,
                maxWidth: ASIDE - 16,
                top: l.top,
                height: BOX,
              }}
              onClick={open}
              title={`Pick the flow moving ${c.name}’s records on`}
            >
              {it.out.kind === 'rest' ? 'rests here: no flow starts' : `no flow takes ${it.state}`}
            </button>
          )
        }
        const x = chipAt.get(it.column)!
        const note = noteOf(it, c)
        const warn = it.out.kind !== 'flow'
        return (
          <div key={it.column}>
            <button
              type="button"
              className={warn ? 'task-flow-chip warn' : 'task-flow-chip'}
              style={{
                left: x - CHIP / 2,
                top: l.top,
                width: CHIP,
                height: BOX,
              }}
              onClick={open}
              aria-haspopup="dialog"
              aria-expanded={picking === it.column}
              title={
                it.out.kind === 'flow'
                  ? `${it.out.named ? `${c.name} names ${nameOf(it.out.flow)}` : `${nameOf(it.out.flow)} takes ${it.state}`}; ${leavesText(it.out.flow, it.state)}. Pick another for ${c.name}`
                  : `Pick the flow moving ${c.name}’s records on`
              }
            >
              {warn ? <AlertIcon /> : <FlowIcon />}
              <span>
                {it.out.kind === 'conflict' ? 'Pick a flow' : it.out.kind === 'problem' ? c.flowName || 'Its flow' : nameOf(it.out.flow)}
              </span>
            </button>
            {note && picking !== it.column && (
              <p
                className={note.warn ? 'task-flow-note warn' : 'task-flow-note'}
                style={{
                  left: x - noteWidth(centers, it.column) / 2,
                  top: l.top + BOX + 6,
                  width: noteWidth(centers, it.column),
                }}
              >
                {note.text}
              </p>
            )}
          </div>
        )
      })}
      {picked && picking !== null && (
        <FlowPicker
          key={picking}
          item={picked}
          column={columns[picking]}
          flows={(columns[picking].struct !== null && flows[columns[picking].struct!]) || []}
          x={chipAt.get(picking) ?? centers[picking] + PILL / 2 + 60}
          layout={l}
          runner={runner}
          onSave={async (choice) => {
            await onPick(picking, choice)
            setPicking(null)
          }}
          onClose={() => setPicking(null)}
        />
      )}
    </div>
  )
}

// How wide a note under a column's flow is: as far as the gap to the next
// column's flow leaves room for.
const noteWidth = (centers: number[], i: number) => {
  const gap = i + 1 < centers.length ? centers[i + 1] - centers[i] : i > 0 ? centers[i] - centers[i - 1] : 304
  return Math.max(CHIP, Math.min(280, gap - 16))
}

// The value of a picker's choice: the flow taking the state, one by id, or
// resting.
const AUTO = 'auto'
const REST = 'rest'

const PICKER_WIDTH = 400

// Picks the flow moving a column's records on, under its flow on the strip:
// the one taking its state, which says which that is or that several do and
// none starts; each flow taking it, with where it leaves them; one the
// column names that doesn't take it; or none, its records resting. What it
// starts with is the column's as it opened, so the board refreshing
// meanwhile undoes no pick.
function FlowPicker({
  item,
  column,
  flows,
  x,
  layout: l,
  runner,
  onSave,
  onClose,
}: {
  item: StripItem
  column: TaskColumn
  flows: BoardFlow[]
  // The centre of what it opened from.
  x: number
  layout: Layout
  runner: string
  onSave: (choice: FlowChoice) => Promise<void>
  onClose: () => void
}) {
  const ref = useRef<HTMLDivElement>(null)
  const [was] = useState(() => (column.maxRuns === 0 ? REST : column.flow !== null ? String(column.flow) : AUTO))
  const [choice, setChoice] = useState(was)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [kept] = useState(() => (column.flow !== null ? { id: column.flow, name: column.flowName } : null))
  const state = item.state
  const { taking, any } = byState(flows, state)

  // Closes on a press outside it but on what opened it, and puts the focus
  // on the choice it has, once: the board refreshing leaves both be.
  const close = useRef(onClose)
  useEffect(() => {
    close.current = onClose
  })
  useEffect(() => {
    ref.current?.querySelector<HTMLInputElement>('input:checked')?.focus()
    const away = (e: MouseEvent) => {
      const at = e.target as Element
      if (ref.current && !ref.current.contains(at) && !at.closest?.('[aria-expanded="true"]')) close.current()
    }
    document.addEventListener('mousedown', away)
    return () => document.removeEventListener('mousedown', away)
  }, [])

  const keys = (e: KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.stopPropagation()
      onClose()
    }
  }

  const save = async () => {
    setSaving(true)
    setError(null)
    try {
      await onSave({
        flow: choice !== AUTO && choice !== REST ? Number(choice) : null,
        rest: choice === REST,
      })
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setSaving(false)
    }
  }

  const auto =
    taking.length === 1
      ? {
          text: `${nameOf(taking[0])}${any ? ', which queries any state' : ''}`,
          warn: false,
        }
      : taking.length > 1
        ? { text: `${taking.length} do, so none starts`, warn: true }
        : { text: 'None does, so its records rest', warn: false }
  const keptFlow = kept && !taking.some((f) => f.id === kept.id) ? kept : null
  const keptOffered = keptFlow ? flows.find((f) => f.id === keptFlow.id) : undefined
  const intro =
    item.out.kind === 'conflict'
      ? `${listOf(item.out.flows.map(nameOf))} ${item.out.flows.length === 2 ? 'both' : 'all'} ${item.out.any ? 'query any state' : `take ${state}`}, so while the board plays it starts none of them on ${column.name}’s records. Pick the one it starts.`
      : `The flow the board gives ${column.name}’s records to while it plays, a run per record.`
  const left = Math.max(0, Math.min(x - PICKER_WIDTH / 2, l.width - PICKER_WIDTH))
  const title = `What moves ${column.name} on`

  const option = (value: string, name: string, sub: string, warn = false) => (
    <label key={value} className={choice === value ? 'task-flow-option chosen' : 'task-flow-option'}>
      <input type="radio" name="task-flow-choice" value={value} checked={choice === value} onChange={() => setChoice(value)} />
      <span>
        <span className="task-flow-option-name">{name}</span>
        <span className={warn ? 'task-flow-option-sub warn' : 'task-flow-option-sub'}>{sub}</span>
      </span>
    </label>
  )

  return (
    <div
      ref={ref}
      className="task-flow-picker"
      role="dialog"
      aria-label={title}
      style={{
        left,
        top: l.top + BOX + 10,
        width: PICKER_WIDTH,
        ['--caret' as string]: `${x - left}px`,
      }}
      onKeyDown={keys}
    >
      <header className="panel-header">
        <h2>{title}</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <p className="task-flow-picker-intro">{intro}</p>
      <div className="task-flow-options" role="radiogroup" aria-label={`${column.name}’s flow`}>
        {option(AUTO, `The flow taking ${state}`, auto.text, auto.warn)}
        {taking.map((f) =>
          option(String(f.id), nameOf(f), `${f.states.includes(state) ? `Takes ${state}` : 'Takes any state'} · ${leavesText(f, state)}`),
        )}
        {keptFlow &&
          option(
            String(keptFlow.id),
            keptOffered ? nameOf(keptOffered) : keptFlow.name || 'A flow you don’t see',
            keptOffered ? `${takesText(keptOffered)}, so it starts nothing here` : 'The board can’t start it on these records',
            true,
          )}
        {option(REST, 'None: its records rest here', 'The board starts nothing on them')}
      </div>
      <p className="task-flow-picker-note">
        Kept as {column.name}’s flow, which its pencil changes too.
        {runner && ` The board’s runs are ${runner}’s while it plays.`}
      </p>
      {error && <p className="panel-warning">{error}</p>}
      <div className="review-actions">
        <button type="button" onClick={onClose}>
          Cancel
        </button>
        <button type="button" className="primary" disabled={saving || choice === was} onClick={save}>
          Save
        </button>
      </div>
    </div>
  )
}
