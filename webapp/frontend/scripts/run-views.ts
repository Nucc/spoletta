// What a run's timeline (src/timeline.ts) and the comparison of two runs
// (src/compare.ts) make of runs' steps: the lanes, the rows overlapping
// steps take, the axis with long waits for people shortened, and which
// steps and lines of text go beside which. Run with `npm test`, which needs
// Node 22.6 or later to load TypeScript.

import { runMatches, runOrigin, type FlowRunDetail, type RunObject, type RunStep } from '../src/api.ts'
import { diffLines, pairObjects, pairProcesses, pairSteps } from '../src/compare.ts'
import { formatSpan, lanesOf, scaleOf, shown, stretchesOf, ticksOf, type Bar } from '../src/timeline.ts'

declare const process: { exitCode?: number }

let failed = 0
let checked = 0
function check(what: string, got: unknown, want: unknown) {
  checked++
  if (JSON.stringify(got) !== JSON.stringify(want)) {
    failed++
    console.error(`${what}\n  got  ${JSON.stringify(got)}\n  want ${JSON.stringify(want)}`)
  }
}

const T0 = Date.parse('2026-10-01T10:00:00Z')
const at = (s: number) => new Date(T0 + s * 1000).toISOString()

let ids = 0
function step(p: Partial<RunStep> & { process: number | null; start: number; end?: number | null }): RunStep {
  const { start, end, ...rest } = p
  return {
    id: ++ids,
    processName: `P${p.process}`,
    kind: 'agent',
    model: '',
    thinking: '',
    prompt: '',
    inputs: [],
    changeRequest: null,
    status: 'done',
    output: '',
    error: '',
    outcome: '',
    instructions: '',
    decidedBy: '',
    canDecide: false,
    childRun: null,
    branch: '',
    commits: [],
    session: '',
    canFollowUp: false,
    followUps: [],
    slotWaitMs: null,
    try: 1,
    startedAt: at(start),
    finishedAt: end === null ? null : at(end ?? start + 1),
    ...rest,
  } as RunStep
}

function run(id: number, steps: RunStep[], objects: Partial<RunObject>[] = []): FlowRunDetail {
  return {
    id,
    flow: 1,
    status: 'done',
    waiting: false,
    version: 1,
    parent: null,
    process: null,
    processName: '',
    rerunOf: null,
    rerunFrom: null,
    rerunFromName: '',
    itemKey: '',
    schedule: null,
    startedBy: '',
    apiKey: '',
    record: null,
    trialOf: null,
    trialOfName: '',
    startedAt: at(0),
    finishedAt: at(100),
    steps,
    objects: objects.map((o, i) => ({ id: i + 1, step: 0, object: null, name: '', state: '', content: '', files: [], ...o })),
    spent: 0,
    spendLimit: 0,
  }
}

// Lanes: a process's steps share its lane, overlapping ones on rows of their
// own; a flow process's inner processes go under it; copied steps are left out.
{
  const r = run(1, [
    step({ process: 1, start: 0, end: 10 }),
    step({ process: 2, kind: 'flow', start: 10, end: 40 }),
    step({ process: 3, start: 12, end: 20, slotWaitMs: 2000 }),
    step({ process: 3, start: 15, end: 30 }),
    step({ process: 3, start: 25, end: 35 }),
    step({ process: 4, kind: 'copied', start: 0, end: 0 }),
    step({ process: 5, kind: 'human', start: 40, end: null, status: 'waiting' }),
  ])
  const parents = new Map<number, number | null>([
    [1, null],
    [2, null],
    [3, 2],
    [5, null],
  ])
  const lanes = lanesOf([r], T0 + 500_000, parents)
  check('top lanes', lanes.map((l) => [l.name, l.depth, l.rows]), [
    ['P1', 0, 1],
    ['P2', 0, 1],
    ['P5', 0, 1],
  ])
  const inner = lanes[1].inner
  check('a flow process holds its inner lane', inner.map((l) => [l.name, l.depth, l.rows]), [['P3', 1, 2]])
  check('overlapping steps take rows, one after another ending first reuses it', inner[0].bars.map((b) => b.row), [0, 1, 0])
  check('a slot wait starts the bar earlier', inner[0].bars[0].queued, T0 + 10_000)
  check('a step going runs to now', lanes[2].bars[0].end, T0 + 500_000)
  check('shown unfolds as told', shown(lanes, (l) => (l.name === 'P2' ? l.inner : [])).map((l) => l.name), ['P1', 'P2', 'P3', 'P5'])

  // The runs a flow process ran as, merged by process under its lane.
  const items = [run(2, [step({ process: 9, start: 11, end: 15 })]), run(3, [step({ process: 9, start: 12, end: 18 })])]
  const merged = lanesOf(items, T0, new Map(), 'x', 1)
  check('item runs merge by process', merged.map((l) => [l.key, l.depth, l.rows, l.bars.map((b) => b.run)]), [['x/9', 1, 2, [2, 3]]])
}

// The axis: a long while with only a person to wait for is shortened,
// what else went on keeps the room.
{
  const bar = (kind: RunStep['kind'], start: number, end: number): Bar => ({
    run: 1,
    step: step({ process: 1, kind, start, end }),
    queued: null,
    start: T0 + start * 1000,
    end: T0 + end * 1000,
    going: false,
    row: 0,
  })
  const bars = [bar('agent', 0, 10), bar('human', 10, 3610), bar('agent', 3610, 3620)]
  const stretches = stretchesOf(T0, T0 + 3620_000, bars, true)
  check('a long wait for a person is cut', stretches.map((s) => [(s.from - T0) / 1000, (s.to - T0) / 1000, s.cut]), [
    [0, 10, false],
    [10, 3610, true],
    [3610, 3620, false],
  ])
  check('not cut when told not to', stretchesOf(T0, T0 + 3620_000, bars, false).length, 1)
  check('a short wait stays', stretchesOf(T0, T0 + 30_000, [bar('agent', 0, 10), bar('human', 10, 25), bar('agent', 25, 30)], true).length, 1)
  check('nothing but people waiting stays whole', stretchesOf(T0, T0 + 3600_000, [bar('human', 0, 3600)], true).length, 1)
  const x = scaleOf(stretches, 1056)
  check('the axis maps across the cut', [x(T0), x(T0 + 10_000), x(T0 + 3610_000), x(T0 + 3620_000)].map(Math.round), [0, 500, 556, 1056])
  check("ticks only where time isn't cut", ticksOf(stretches, 4).map((t) => (t - T0) / 1000), [0, 5, 10, 3610, 3615, 3620])
}

check('spans read as people say them', [850, 12_000, 200_000, 7_500_000, 270_000_000].map(formatSpan), ['850ms', '12s', '3m 20s', '2h 5m', '3d 3h'])

// Comparing: steps go beside the other run's that took the same, then by
// order; lines are marked where the other text lacks them.
{
  const took = (content: string) => [{ name: 'Item', state: '', content }]
  const a = [step({ process: 7, start: 0, inputs: took('A') }), step({ process: 7, start: 1, inputs: took('B') }), step({ process: 7, start: 2, inputs: took('C') })]
  const b = [step({ process: 7, start: 0, inputs: took('B') }), step({ process: 7, start: 1, inputs: took('A') }), step({ process: 7, start: 2, inputs: took('D') }), step({ process: 7, start: 3, inputs: took('E') })]
  const name = (s?: RunStep) => s?.inputs[0].content ?? '-'
  check('steps pair by what they took, then in order', pairSteps(a, b).map((t) => name(t.a) + name(t.b)), ['AA', 'BB', 'CD', '-E'])

  const ra = run(1, [step({ process: 1, start: 0 }), step({ process: 2, start: 1 })], [
    { object: 5, name: 'Bug', content: 'one' },
    { object: 5, name: 'Bug', content: 'two' },
  ])
  const rb = run(2, [step({ process: 2, start: 0 }), step({ process: 3, start: 1 })], [{ object: 6, name: 'Fix', content: 'x' }])
  check("processes in the first run's order, then the second's", pairProcesses(ra, rb).map((p) => [p.key, p.a.length, p.b.length]), [
    ['#1', 1, 0],
    ['#2', 1, 1],
    ['#3', 0, 1],
  ])
  check("an object's last value", pairObjects(ra, rb).map((o) => [o.name, o.a?.content ?? null, o.b?.content ?? null]), [
    ['Bug', 'two', null],
    ['Fix', null, 'x'],
  ])
  const d = diffLines('a\nb\nc\nd', 'a\nc\nd\ne')
  check('lines the other lacks', [d.a.map((l) => l.differs), d.b.map((l) => l.differs)], [
    [false, true, false, false],
    [false, false, false, true],
  ])
  check('the same text differs nowhere', diffLines('x\ny', 'x\ny').a.some((l) => l.differs), false)
}

// A trial of one process alone: its one step is the timeline's one lane,
// and beside a run of the whole flow it pairs with that run's step of the
// process; the list leaves it out unless asked for.
{
  const real = run(20, [step({ process: 1, start: 0, end: 5 }), step({ process: 2, start: 5, end: 9 })])
  const trial = { ...run(21, [step({ process: 2, start: 0, end: 3 })]), trialOf: 2, trialOfName: 'P2' }
  const parents = new Map<number, number | null>([
    [1, null],
    [2, null],
  ])
  check("a trial's timeline", lanesOf([trial], T0, parents).map((l) => [l.name, l.bars.length]), [['P2', 1]])
  check('a trial beside a run', pairProcesses(trial, real).map((p) => [p.key, p.a.length, p.b.length]), [
    ['#2', 1, 1],
    ['#1', 0, 1],
  ])
  check('a trial started as one', [runOrigin(trial), runOrigin(real)], ['trial', 'person'])
  const all = { status: [], started: [] }
  check('trials are listed only when asked for', [runMatches(trial, all), runMatches(real, all), runMatches(trial, { status: [], started: ['trial'] })], [
    false,
    true,
    true,
  ])
}

console.log(`${checked} checks of the run views, ${failed} failed`)
if (failed > 0) process.exitCode = 1
