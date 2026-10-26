import type { FlowRunDetail, RunStep } from './api'

// A run's steps on a time axis, a lane per process, so what ran at once
// shows side by side; see RunTimeline.tsx. A lane's steps that overlap, such
// as the items of a list going at once, take a row each. A flow process
// that doesn't record runs its board's processes as steps of the same run,
// so their lanes go under its lane; one that records, a subflow process, or
// one going over the items of a list, ran its board as runs of their own,
// whose lanes, merged by process across those runs, go under its lane once
// they are loaded. Times are milliseconds since the epoch.

// A step on the axis: from when it began waiting for its slots, if it
// recorded that, to when it started, then to when it ended, or now while it goes.
export type Bar = { run: number; step: RunStep; queued: number | null; start: number; end: number; going: boolean; row: number }

export type Lane = {
  // Its place among all lanes: the lane it is under, and its process.
  key: string
  process: number | null
  name: string
  kind: RunStep['kind']
  depth: number
  bars: Bar[]
  rows: number
  // The runs its steps ran their boards as, in the order they started.
  childRuns: number[]
  // The lanes of the processes on its board that ran as steps of its run.
  inner: Lane[]
}

// Steps a re-run copied, and the one holding what the API gave a run, took
// no time; the timeline leaves them out.
export const timed = (s: RunStep) => s.kind !== 'copied' && s.kind !== 'input'

// The lanes of the steps of the runs, one run's or those its process's
// board ran as, merged by process, in the order each first started; a
// process inside a flow process of the same run goes in that one's lane.
// parents gives each process's flow process, if it has one.
export function lanesOf(runs: FlowRunDetail[], now: number, parents: Map<number, number | null>, under = '', depth = 0): Lane[] {
  const byKey = new Map<string, Lane>()
  for (const run of runs) {
    for (const s of run.steps.filter(timed)) {
      const key = `${under}/${s.process ?? s.processName}`
      let lane = byKey.get(key)
      if (!lane) {
        lane = { key, process: s.process, name: s.processName, kind: s.kind, depth, bars: [], rows: 1, childRuns: [], inner: [] }
        byKey.set(key, lane)
      }
      lane.bars.push(barOf(run.id, s, now))
      if (s.childRun !== null) lane.childRuns.push(s.childRun)
    }
  }
  const lanes = [...byKey.values()]
  for (const l of lanes) l.rows = pack(l.bars)
  lanes.sort((a, b) => first(a) - first(b))
  // A lane goes under the nearest flow process above it that has one here.
  const byProcess = new Map(lanes.flatMap((l) => (l.process === null ? [] : [[l.process, l] as const])))
  const above = (l: Lane): Lane | undefined => {
    let p = l.process === null ? null : (parents.get(l.process) ?? null)
    // Boards nest a few deep; the bound only guards against a cycle.
    for (let i = 0; p !== null && i < 64; i++) {
      const lane = byProcess.get(p)
      if (lane) return lane
      p = parents.get(p) ?? null
    }
    return undefined
  }
  const top: Lane[] = []
  for (const l of lanes) {
    const a = above(l)
    if (a) a.inner.push(l)
    else top.push(l)
  }
  const deepen = (ls: Lane[], d: number) => {
    for (const l of ls) {
      l.depth = d
      deepen(l.inner, d + 1)
    }
  }
  deepen(top, depth)
  return top
}

export function barOf(run: number, s: RunStep, now: number): Bar {
  const start = Date.parse(s.startedAt)
  const going = s.finishedAt === null
  const end = going ? Math.max(start, now) : Math.max(start, Date.parse(s.finishedAt!))
  return { run, step: s, queued: s.slotWaitMs ? start - s.slotWaitMs : null, start, end, going, row: 0 }
}

// When a bar begins on the axis: as it began waiting, if it did.
export const barFrom = (b: Bar) => b.queued ?? b.start

const first = (l: Lane) => Math.min(...l.bars.map(barFrom))

// Puts each bar on the first row it overlaps nothing on, in the order they
// began; returns how many rows they take.
export function pack(bars: Bar[]): number {
  const ends: number[] = []
  for (const b of [...bars].sort((x, y) => barFrom(x) - barFrom(y))) {
    let row = ends.findIndex((e) => e <= barFrom(b))
    if (row < 0) row = ends.length
    ends[row] = b.end
    b.row = row
  }
  return Math.max(1, ends.length)
}

// Every lane and those under it, depth first, as far as open says to go.
export function shown(lanes: Lane[], open: (l: Lane) => Lane[]): Lane[] {
  return lanes.flatMap((l) => [l, ...shown(open(l), open)])
}

// The span the axis covers: the run's, widened to any bar outside it.
export function extent(run: FlowRunDetail, bars: Bar[], now: number): [number, number] {
  const from = Math.min(Date.parse(run.startedAt), ...bars.map(barFrom))
  const to = Math.max(run.finishedAt ? Date.parse(run.finishedAt) : now, ...bars.map((b) => b.end), from + 1000)
  return [from, to]
}

const SECOND = 1000
const MINUTE = 60 * SECOND
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR
const STEPS = [1, 2, 5, 10, 15, 30]
  .map((n) => n * SECOND)
  .concat(
    [1, 2, 5, 10, 15, 30].map((n) => n * MINUTE),
    [1, 2, 3, 6, 12].map((n) => n * HOUR),
    [1, 2, 7, 14, 30].map((n) => n * DAY),
  )

// Whether a bar is a wait for a person: a review, or a loop's question.
export const forPerson = (b: Bar) => b.step.kind === 'human' || b.step.kind === 'loop'

// A part of the axis: a span of time, and whether it is shortened, being a
// long while with only people to wait for, such as a review left for hours,
// which would squeeze what else the run did into a sliver.
export type Stretch = { from: number; to: number; cut: boolean }

// How wide a shortened stretch is on the axis, and how long a while with
// only people to wait for must last to be one: longer than this, and than
// all the time something else went on.
export const CUT_WIDTH = 56
const CUT_MIN = 20 * SECOND

// The axis from from to to in stretches, those with only people to wait for
// long enough shortened if cut says to.
export function stretchesOf(from: number, to: number, bars: Bar[], cut: boolean): Stretch[] {
  const merged: [number, number][] = []
  for (const [s, e] of bars
    .filter((b) => !forPerson(b))
    .map((b): [number, number] => [barFrom(b), b.end])
    .sort((x, y) => x[0] - y[0])) {
    const last = merged.at(-1)
    if (last && s <= last[1]) last[1] = Math.max(last[1], e)
    else merged.push([s, e])
  }
  const busy = merged.reduce((n, [s, e]) => n + e - s, 0)
  if (!cut || busy === 0) return [{ from, to, cut: false }]
  const longest = Math.max(CUT_MIN, busy)
  const out: Stretch[] = []
  const add = (s: number, e: number, shortened: boolean) => {
    if (e <= s) return
    const last = out.at(-1)
    if (last && !last.cut && !shortened) last.to = e
    else out.push({ from: s, to: e, cut: shortened })
  }
  let at = from
  for (const [s, e] of merged) {
    add(at, s, s - at > longest)
    add(Math.max(at, s), e, false)
    at = Math.max(at, e)
  }
  add(at, to, to - at > longest)
  return out
}

// Where each time goes on a track px wide: the shortened stretches
// CUT_WIDTH each, the others sharing the rest by how long they last.
export function scaleOf(stretches: Stretch[], track: number): (t: number) => number {
  const cuts = stretches.filter((s) => s.cut).length
  const per = Math.max(0, track - cuts * CUT_WIDTH) / Math.max(1, unshortened(stretches))
  return (t) => {
    let x = 0
    for (const s of stretches) {
      if (t <= s.to) return x + (s.cut ? ((t - s.from) / (s.to - s.from)) * CUT_WIDTH : (t - s.from) * per)
      x += s.cut ? CUT_WIDTH : (s.to - s.from) * per
    }
    return x
  }
}

const unshortened = (stretches: Stretch[]) => stretches.reduce((n, s) => n + (s.cut ? 0 : s.to - s.from), 0)

// The times the axis's ticks go at: every round step from its start, the
// smallest giving at most max of them over the stretches not shortened.
export function ticksOf(stretches: Stretch[], max = 8): number[] {
  const from = stretches[0].from
  const time = unshortened(stretches)
  const step = STEPS.find((s) => time / s <= max) ?? Math.ceil(time / max / DAY) * DAY
  const out: number[] = []
  for (const s of stretches) {
    if (s.cut) continue
    for (let t = from + Math.ceil((s.from - from) / step) * step; t <= s.to; t += step) out.push(t)
  }
  return out
}

// A length of time as a person reads it: 850ms, 12s, 3m 20s, 2h 5m, 3d 4h.
export function formatSpan(ms: number): string {
  if (ms < SECOND) return `${Math.round(ms)}ms`
  const s = Math.round(ms / SECOND)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return s % 60 ? `${m}m ${s % 60}s` : `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return m % 60 ? `${h}h ${m % 60}m` : `${h}h`
  const d = Math.floor(h / 24)
  return h % 24 ? `${d}d ${h % 24}h` : `${d}d`
}
