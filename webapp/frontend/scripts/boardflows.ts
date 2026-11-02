// What a task board's Flows strip (src/boardflows.ts) makes of its columns:
// the flow moving each column's records on, or why none does, the columns
// holding the states it leaves them in, and where the strip draws its boxes
// and arrows. Run with `npm test`, which needs Node 22.6 or later to load
// TypeScript.

import type { BoardFlow } from '../src/api.ts'
import { byState, conflicts, layout, outOf, strip, type StripColumn, type StripItem } from '../src/boardflows.ts'

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

const flow = (id: number, name: string, states: string[], leaves: string[]): BoardFlow => ({ id, name, states, leaves })
const prepare = flow(1, 'Prepare PR', ['draft'], ['in review'])
const ai = flow(2, 'AI code review', ['in review'], ['approved', 'draft'])
const human = flow(3, 'Human review', ['in review'], ['Approved', 'draft'])
const merge = flow(4, 'Merge PR', ['approved'], ['merged', 'closed'])
const sweep = flow(5, 'Sweep', [''], ['merged'])
const flows = { '7': [prepare, ai, human, merge], '8': [sweep] }

const column = (state: string, c: Partial<StripColumn> = {}): StripColumn => ({
  struct: 7,
  state,
  flow: null,
  rests: false,
  shown: true,
  ...c,
})
const board = [column('draft'), column('in review'), column('approved'), column('merged', { rests: true })]
const kinds = (items: (StripItem | null)[]) => items.map((it) => it && it.out.kind)
const names = (it: StripItem | null) =>
  it &&
  (it.out.kind === 'flow' || it.out.kind === 'idle'
    ? [it.out.flow.name]
    : it.out.kind === 'conflict'
      ? it.out.flows.map((f) => f.name)
      : [])

// Which flows take a state: those naming it, else those taking any.
check(
  'flows naming the state',
  byState([prepare, sweep], 'draft').taking.map((f) => f.name),
  ['Prepare PR'],
)
check('none naming it, those taking any', byState([prepare, sweep], 'merged'), {
  taking: [sweep],
  any: true,
  others: [prepare],
})
check('none taking it', byState([prepare], 'merged').taking, [])

// What moves each column's records on.
const items = strip(board, flows)
check('each column', kinds(items), ['flow', 'conflict', 'flow', 'rest'])
check('the flows', items.map(names), [['Prepare PR'], ['AI code review', 'Human review'], ['Merge PR'], []])
check('a column several flows take', conflicts(items), 1)
check(
  'where they go',
  items.map((it) => it?.targets),
  [
    [{ state: 'in review', column: 1 }],
    [
      { state: 'approved', column: 2 },
      { state: 'draft', column: 0 },
    ],
    [
      { state: 'merged', column: 3 },
      { state: 'closed', column: null },
    ],
    [],
  ],
)
check('a column it names', outOf(column('in review', { flow: 3 }), flows['7']), { kind: 'flow', flow: human, named: true, any: false })
check('a flow it names taking another state', outOf(column('in review', { flow: 4 }), flows['7']), { kind: 'idle', flow: merge })
check('a flow it names gone', outOf(column('in review', { flow: 9, flowProblem: 'trash' }), flows['7']), {
  kind: 'problem',
  problem: 'trash',
})
check('a flow it names no longer offered', outOf(column('in review', { flow: 9 }), flows['7']), { kind: 'problem', problem: 'unqueried' })
check('a state no flow takes', outOf(column('merged'), flows['7']), {
  kind: 'none',
})
check('a flow taking any state', outOf(column('merged', { struct: 8 }), flows['8']), { kind: 'flow', flow: sweep, named: false, any: true })
check(
  'columns holding no single state, showing no records, or of another struct',
  kinds(strip([column(''), column('draft', { shown: false }), column('approved', { struct: 8 }), column('in review')], flows)),
  [null, null, 'flow', 'conflict'],
)
check(
  'a state another struct holds is no target',
  strip([column('in review', { flow: 2 }), column('approved', { struct: 8 })], flows)[0]?.targets,
  [
    { state: 'approved', column: null },
    { state: 'draft', column: null },
  ],
)

// Where the strip draws them, over columns 288px wide 16px apart.
const centers = [144, 448, 752, 1056]
const l = layout(items, centers, 1200)
check('the states', l.states, [
  { column: 0, x: 144 },
  { column: 1, x: 448 },
  { column: 2, x: 752 },
  { column: 3, x: 1056 },
])
check('the flows, halfway to the next column', l.chips, [
  { column: 0, x: 296 },
  { column: 1, x: 600 },
  { column: 2, x: 904 },
])
check('one lane over the boxes, room beside the state resting', [l.top, l.height, l.width], [22, 94, 1056 + 56 + 190])
check('the arrows', l.arrows, [
  {
    points: [
      [200, 36],
      [226, 36],
    ],
    head: false,
    warn: false,
  },
  {
    points: [
      [504, 36],
      [530, 36],
    ],
    head: false,
    warn: true,
  },
  {
    points: [
      [808, 36],
      [834, 36],
    ],
    head: false,
    warn: false,
  },
  {
    points: [
      [366, 36],
      [391, 36],
    ],
    head: true,
    warn: false,
  },
  {
    points: [
      [670, 36],
      [695, 36],
    ],
    head: true,
    warn: true,
  },
  {
    points: [
      [974, 36],
      [999, 36],
    ],
    head: true,
    warn: false,
  },
  {
    points: [
      [600, 22],
      [600, 6],
      [144, 6],
      [144, 21],
    ],
    head: true,
    warn: true,
  },
])
check(
  'the last column’s flow past it widens the strip',
  layout([null, null, null, items[2] && { ...items[2], column: 3 }], centers, 1100).width,
  1056 + 152 + 70 + 8,
)

// Arrows over the boxes: two into one state spread apart, the longer on a
// lane further up; one beside them on the lowest lane.
const going = (column: number, ...to: number[]): StripItem => ({
  column,
  state: `s${column}`,
  out: {
    kind: 'flow',
    flow: flow(column, `F${column}`, [`s${column}`], []),
    named: false,
    any: false,
  },
  targets: to.map((t) => ({ state: `s${t}`, column: t })),
})
const many = layout([going(0), going(1, 0), going(2, 0), going(3, 3)], centers, 1200)
check(
  'lanes',
  [many.top, many.arrows.slice(4).map((a) => a.points)],
  [
    34,
    [
      [
        [600, 34],
        [600, 18],
        [139, 18],
        [139, 33],
      ],
      [
        [904, 34],
        [904, 6],
        [149, 6],
        [149, 33],
      ],
      [
        [1208, 34],
        [1208, 18],
        [1056, 18],
        [1056, 33],
      ],
    ],
  ],
)
check(
  'no arrows over the boxes',
  layout(
    strip([column('draft'), column('in review', { flow: 3 })], {
      '7': [prepare, flow(3, 'Human review', ['in review'], [])],
    }),
    centers,
    600,
  ).top,
  10,
)

console.log(`${checked} checks of a task board's flows, ${failed} failed`)
if (failed > 0) process.exitCode = 1
