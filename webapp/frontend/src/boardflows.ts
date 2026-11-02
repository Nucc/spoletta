import type { BoardFlow, TaskFlowProblem } from './api'

// What a task board's Flows strip (TaskFlows.tsx) draws over its columns:
// the state each column holds, the flow the board playing gives its records
// to, and arrows from that flow to the columns holding the states it leaves
// them in, so the board reads as its records' way from state to state. A
// column's records go to the flow it names, else to the one taking its
// state, by a query naming it or, as none does, by one of any state (see
// taskboard_play.go); several taking it start none, which the strip flags
// for a person to pick one. Pure, so scripts/boardflows.ts checks it.

// The flows taking records in the state, by a query naming it, else, as
// none does, by one of any state, whether they take it so, and the others.
export function byState<F extends { states: string[] }>(flows: F[], state: string): { taking: F[]; any: boolean; others: F[] } {
  const naming = flows.filter((f) => state !== '' && f.states.includes(state))
  const taking = naming.length > 0 ? naming : flows.filter((f) => f.states.includes(''))
  return {
    taking,
    any: naming.length === 0 && taking.length > 0,
    others: flows.filter((f) => !taking.includes(f)),
  }
}

// A column as the strip reads it: its struct, the one state its query names
// ('' for none or several), the flow it names, whether its records rest,
// why its flow wouldn't start, and whether it shows its records at all.
export type StripColumn = {
  struct: number | null
  state: string
  flow: number | null
  rests: boolean
  flowProblem?: TaskFlowProblem
  shown: boolean
}

// What moves a column's records on: nothing, as it rests or no flow takes
// its state; the flow it names or the one taking its state; a flow it names
// that takes other states, so starts nothing; several flows taking its
// state, so none starts; or a flow it names that the board can't start.
export type Out =
  | { kind: 'rest' }
  | { kind: 'none' }
  | { kind: 'flow'; flow: BoardFlow; named: boolean; any: boolean }
  | { kind: 'idle'; flow: BoardFlow }
  | { kind: 'conflict'; flows: BoardFlow[]; any: boolean }
  | { kind: 'problem'; problem: TaskFlowProblem }

// A state the flow moving a column's records on leaves them in, and the
// column holding it, null for none.
export type Target = { state: string; column: number | null }

// What the strip draws of a column holding a state.
export type StripItem = {
  column: number
  state: string
  out: Out
  targets: Target[]
}

const same = (a: string, b: string) => a.toLowerCase() === b.toLowerCase()

// What moves the column's records on, of the flows the board may start on
// its struct's records.
export function outOf(c: StripColumn, flows: BoardFlow[]): Out {
  if (c.rests) return { kind: 'rest' }
  if (c.flow !== null) {
    const f = flows.find((f) => f.id === c.flow)
    if (!f) return { kind: 'problem', problem: c.flowProblem ?? 'unqueried' }
    if (!f.states.includes(c.state) && !f.states.includes('')) return { kind: 'idle', flow: f }
    return {
      kind: 'flow',
      flow: f,
      named: true,
      any: !f.states.includes(c.state),
    }
  }
  const { taking, any } = byState(flows, c.state)
  if (taking.length === 1) return { kind: 'flow', flow: taking[0], named: false, any }
  if (taking.length > 1) return { kind: 'conflict', flows: taking, any }
  return { kind: 'none' }
}

// The states a column's records leave it for, once each, in the order its
// flow, or its candidates, list them.
function leavesOf(out: Out): string[] {
  const all = out.kind === 'flow' ? out.flow.leaves : out.kind === 'conflict' ? out.flows.flatMap((f) => f.leaves) : []
  return all.filter((s, i) => all.findIndex((t) => same(s, t)) === i)
}

// What the strip draws of each column, by its place; null for one holding
// no single state, or showing no records. A state a flow leaves records in
// is the first column's of the same struct holding it, matched whatever
// its case, as a flow's version may spell it otherwise.
export function strip(columns: StripColumn[], flows: Record<string, BoardFlow[] | undefined>): (StripItem | null)[] {
  return columns.map((c, i) => {
    if (!c.shown || c.struct === null || c.state === '') return null
    const out = outOf(c, flows[c.struct] ?? [])
    const targets = leavesOf(out).map((state) => {
      const j = columns.findIndex((d) => d.shown && d.struct === c.struct && d.state !== '' && same(d.state, state))
      return { state, column: j < 0 ? null : j }
    })
    return { column: i, state: c.state, out, targets }
  })
}

// How many columns several flows take the state of, starting none.
export const conflicts = (items: (StripItem | null)[]) => items.filter((it) => it?.out.kind === 'conflict').length

// The sizes the strip draws in, in px: a state's box and a flow's are as
// wide as these, and as tall as BOX; arrows routed over the states run a
// LANE apart.
export const PILL = 112
export const CHIP = 140
export const BOX = 28
export const LANE = 12
// Below the states, room for a line or two saying what goes wrong; beside
// a state, room for saying no flow moves its records on.
export const NOTES = 44
export const ASIDE = 190

// An arrow's corners, from its tail to its tip, and whether it ends in a
// head and stands for a flow that doesn't start, as several take its state.
export type Arrow = {
  points: [number, number][]
  head: boolean
  warn: boolean
}

// Where the strip draws: its size, where its states' boxes start from the
// top, each state's box and flow's by its centre, and its arrows.
export type Layout = {
  width: number
  height: number
  top: number
  states: { column: number; x: number }[]
  chips: { column: number; x: number }[]
  arrows: Arrow[]
}

// An arrow from a column's flow over the boxes to another column's state.
type Route = {
  from: number
  to: number
  warn: boolean
  sx: number
  ex: number
  lane: number
}

// Where the strip draws its items over columns centred at centers, at least
// width wide. A column's state sits over its centre and its flow halfway to
// the next column (the last's as far past it); an arrow to the next
// column's state runs straight, others go up from the flow, along a lane
// over the boxes and down into the state, nested ones on lanes further up,
// those side by side on one lane.
export function layout(items: (StripItem | null)[], centers: number[], width: number): Layout {
  const n = centers.length
  const gapAfter = (i: number) => (i + 1 < n ? centers[i + 1] - centers[i] : i > 0 ? centers[i] - centers[i - 1] : PILL + CHIP + 52)
  const chipX = (i: number) => Math.round(centers[i] + gapAfter(i) / 2)
  const states: Layout['states'] = []
  const chips: Layout['chips'] = []
  const straight: { from: number; to: number; warn: boolean }[] = []
  const routes: Route[] = []
  for (const it of items) {
    if (!it || it.column >= n) continue
    states.push({ column: it.column, x: centers[it.column] })
    if (it.out.kind === 'rest' || it.out.kind === 'none') continue
    chips.push({ column: it.column, x: chipX(it.column) })
    const warn = it.out.kind !== 'flow'
    for (const t of it.targets) {
      if (t.column === null || t.column >= n) continue
      if (t.column === it.column + 1) straight.push({ from: it.column, to: t.column, warn })
      else
        routes.push({
          from: it.column,
          to: t.column,
          warn,
          sx: 0,
          ex: 0,
          lane: 0,
        })
    }
  }
  // Spread the arrows leaving one flow, and those entering one state from
  // above, 10px apart, those coming from further left entering further left.
  const spread = (k: number, of: number) => (k - (of - 1) / 2) * 10
  for (const from of new Set(routes.map((r) => r.from))) {
    const out = routes.filter((r) => r.from === from).sort((x, y) => centers[x.to] - centers[y.to])
    out.forEach((r, k) => (r.sx = Math.round(chipX(from) + spread(k, out.length))))
  }
  for (const to of new Set(routes.map((r) => r.to))) {
    const into = routes.filter((r) => r.to === to).sort((x, y) => x.sx - y.sx)
    into.forEach((r, k) => (r.ex = Math.round(centers[to] + spread(k, into.length))))
  }
  // The shortest first, each on the lowest lane none it overlaps is on.
  const span = (r: Route): [number, number] => [Math.min(r.sx, r.ex), Math.max(r.sx, r.ex)]
  const placed: Route[] = []
  for (const r of [...routes].sort((x, y) => span(x)[1] - span(x)[0] - (span(y)[1] - span(y)[0]) || x.sx - y.sx)) {
    const [lo, hi] = span(r)
    let lane = 0
    while (placed.some((p) => p.lane === lane && span(p)[0] <= hi + 6 && lo - 6 <= span(p)[1])) lane++
    r.lane = lane
    placed.push(r)
  }
  const lanes = routes.length === 0 ? 0 : Math.max(...routes.map((r) => r.lane)) + 1
  const top = lanes === 0 ? 10 : 22 + (lanes - 1) * LANE
  const mid = top + BOX / 2
  const laneY = (lane: number) => top - 16 - lane * LANE
  const arrows: Arrow[] = []
  for (const c of chips) {
    const it = items[c.column]!
    arrows.push({
      points: [
        [centers[c.column] + PILL / 2, mid],
        [c.x - CHIP / 2, mid],
      ],
      head: false,
      warn: it.out.kind !== 'flow',
    })
  }
  for (const s of straight) {
    arrows.push({
      points: [
        [chipX(s.from) + CHIP / 2, mid],
        [centers[s.to] - PILL / 2 - 1, mid],
      ],
      head: true,
      warn: s.warn,
    })
  }
  for (const r of routes) {
    const y = laneY(r.lane)
    arrows.push({
      points: [
        [r.sx, top],
        [r.sx, y],
        [r.ex, y],
        [r.ex, top - 1],
      ],
      head: true,
      warn: r.warn,
    })
  }
  const resting = items.filter((it) => it && it.column < n && (it.out.kind === 'rest' || it.out.kind === 'none'))
  const right = Math.max(width, ...chips.map((c) => c.x + CHIP / 2 + 8), ...resting.map((it) => centers[it!.column] + PILL / 2 + ASIDE))
  return {
    width: right,
    height: top + BOX + NOTES,
    top,
    states,
    chips,
    arrows,
  }
}
