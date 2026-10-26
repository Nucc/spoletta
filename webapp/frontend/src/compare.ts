import type { FlowRunDetail, RunObject, RunStep } from './api'

// Two runs of a flow side by side; see RunCompare.tsx. Their steps are
// matched by process, the process's id or, for one deleted before ids were
// kept, its name. A process's steps in one run go beside those in the other
// that took just the same, such as the same item of a list, which items
// going at once may start in another order; the rest in the order they
// started, its first try against the other's first, and so on. What objects
// they ended with is each object's last value in each run.

// A process and its steps in either run, in the order the processes first
// started in the first run, then those only the second ran.
export type ProcessPair = { key: string; name: string; kind: RunStep['kind']; a: RunStep[]; b: RunStep[] }

// An object's last value in either run, absent from one that had none.
export type ObjectPair = { key: string; name: string; a?: RunObject; b?: RunObject }

// Steps a re-run copied, and the one holding what the API gave a run, ran
// nothing; what they hold shows among the objects.
const ran = (s: RunStep) => s.kind !== 'copied' && s.kind !== 'input'

const processKey = (s: RunStep) => (s.process !== null ? `#${s.process}` : s.processName)
const objectKey = (o: RunObject) => (o.object !== null ? `#${o.object}` : o.name)

export function pairProcesses(a: FlowRunDetail, b: FlowRunDetail): ProcessPair[] {
  const pairs = new Map<string, ProcessPair>()
  const add = (s: RunStep, side: 'a' | 'b') => {
    const key = processKey(s)
    let p = pairs.get(key)
    if (!p) {
      p = { key, name: s.processName, kind: s.kind, a: [], b: [] }
      pairs.set(key, p)
    }
    p[side].push(s)
  }
  a.steps.filter(ran).forEach((s) => add(s, 'a'))
  b.steps.filter(ran).forEach((s) => add(s, 'b'))
  return [...pairs.values()]
}

export function pairObjects(a: FlowRunDetail, b: FlowRunDetail): ObjectPair[] {
  const pairs = new Map<string, ObjectPair>()
  const add = (o: RunObject, side: 'a' | 'b') => {
    const key = objectKey(o)
    const p = pairs.get(key) ?? { key, name: o.name }
    // In the order they were made, so the last one stays.
    p[side] = o
    pairs.set(key, p)
  }
  a.objects.forEach((o) => add(o, 'a'))
  b.objects.forEach((o) => add(o, 'b'))
  return [...pairs.values()]
}

// A process's steps of either run side by side, in the order those of the
// first started, then the second's left over; a turn lacks a side where one
// ran it fewer times.
export function pairSteps(a: RunStep[], b: RunStep[]): { a?: RunStep; b?: RunStep }[] {
  const took = (s: RunStep) => JSON.stringify(s.inputs.map((i) => [i.name, i.state, i.content]))
  const left = [...b]
  const turns: { a?: RunStep; b?: RunStep }[] = a.map((s) => {
    const same = left.findIndex((o) => took(o) === took(s))
    return same < 0 ? { a: s } : { a: s, b: left.splice(same, 1)[0] }
  })
  for (const t of turns) if (!t.b && left.length > 0) t.b = left.shift()
  return [...turns, ...left.map((s) => ({ b: s }))]
}

// What a process's steps came to, to tell at a glance whether it went the
// same way in both runs: each step's status in turn.
export const outcomeOf = (steps: RunStep[]) => steps.map((s) => s.status).join(',')

// A line of a text compared with another: in both, or only in this one.
export type DiffLine = { text: string; differs: boolean }

// Beyond this many pairs of lines to weigh, texts are told apart as a whole.
const DIFF_LIMIT = 4_000_000

// The lines of two texts, each marked where the other lacks it, as the
// longest run of lines they share leaves them.
export function diffLines(a: string, b: string): { a: DiffLine[]; b: DiffLine[] } {
  const x = a.split('\n')
  const y = b.split('\n')
  // What they start and end with alike needs no weighing.
  let head = 0
  while (head < x.length && head < y.length && x[head] === y[head]) head++
  let tail = 0
  while (tail < x.length - head && tail < y.length - head && x[x.length - 1 - tail] === y[y.length - 1 - tail]) tail++
  const xs = x.slice(head, x.length - tail)
  const ys = y.slice(head, y.length - tail)
  const same = (l: string) => ({ text: l, differs: false })
  const differ = (l: string) => ({ text: l, differs: true })
  const wrap = (mid: { a: DiffLine[]; b: DiffLine[] }) => ({
    a: [...x.slice(0, head).map(same), ...mid.a, ...x.slice(x.length - tail).map(same)],
    b: [...y.slice(0, head).map(same), ...mid.b, ...y.slice(y.length - tail).map(same)],
  })
  if (xs.length * ys.length > DIFF_LIMIT) return wrap({ a: xs.map(differ), b: ys.map(differ) })

  // lengths[i][j]: the longest run of lines xs[i:] and ys[j:] share.
  const lengths = Array.from({ length: xs.length + 1 }, () => new Uint32Array(ys.length + 1))
  for (let i = xs.length - 1; i >= 0; i--)
    for (let j = ys.length - 1; j >= 0; j--)
      lengths[i][j] = xs[i] === ys[j] ? lengths[i + 1][j + 1] + 1 : Math.max(lengths[i + 1][j], lengths[i][j + 1])
  const mid = { a: [] as DiffLine[], b: [] as DiffLine[] }
  let i = 0
  let j = 0
  while (i < xs.length && j < ys.length) {
    if (xs[i] === ys[j]) {
      mid.a.push(same(xs[i++]))
      mid.b.push(same(ys[j++]))
    } else if (lengths[i + 1][j] >= lengths[i][j + 1]) mid.a.push(differ(xs[i++]))
    else mid.b.push(differ(ys[j++]))
  }
  while (i < xs.length) mid.a.push(differ(xs[i++]))
  while (j < ys.length) mid.b.push(differ(ys[j++]))
  return wrap(mid)
}

// A JSON value's fields, if it is an object with any; else null.
export function fieldsOf(text: string): Record<string, unknown> | null {
  try {
    const v: unknown = JSON.parse(text)
    return v !== null && typeof v === 'object' && !Array.isArray(v) && Object.keys(v).length > 0 ? (v as Record<string, unknown>) : null
  } catch {
    return null
  }
}

// A field's value as it reads: text as it is, anything else as JSON.
export const fieldText = (v: unknown): string => (typeof v === 'string' ? v : JSON.stringify(v, null, 2))
