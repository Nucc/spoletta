// How the board lays links out (src/route.ts, src/layout.ts): Tidy up puts
// the end below everything, and links nobody moved by hand keep clear of
// every node, don't run along each other, and spread out along the sides
// they share. The board is a review flow whose every process leads back
// into the ticket it works on. Run with `npm test`, which needs Node 22.6 or
// later to load TypeScript.

import { Position, type Edge, type Node, type XYPosition } from '@xyflow/react'
import { tidy } from '../src/layout.ts'
import { routeLinks, routePoints, type Box, type LinkEnds, type LinkRoute } from '../src/route.ts'

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

// The board: its nodes' sizes, the rows of the ticket's states from its top,
// and its links, each from a node's own handle or a state's.
const sizes: Record<string, { width: number; height: number }> = {
  start: { width: 96, height: 42 },
  end: { width: 88, height: 42 },
  fetch: { width: 270, height: 62 },
  ticket: { width: 266, height: 242 },
  review: { width: 280, height: 62 },
  sendBack: { width: 196, height: 62 },
  merge: { width: 170, height: 62 },
}
const rows: Record<string, number> = { initialized: 58, created: 86, inProgress: 113, inReview: 140, done: 168 }
type Link = { id: string; source: string; target: string; fromState?: string; toState?: string }
const links: Link[] = [
  { id: 'start', source: 'start', target: 'fetch' },
  { id: 'query', source: 'ticket', target: 'fetch' },
  { id: 'fetched', source: 'fetch', target: 'ticket', toState: 'inReview' },
  { id: 'toSendBack', source: 'ticket', target: 'sendBack', fromState: 'inProgress' },
  { id: 'toReview', source: 'ticket', target: 'review', fromState: 'inReview' },
  { id: 'toMerge', source: 'ticket', target: 'merge', fromState: 'done' },
  { id: 'decision', source: 'review', target: 'ticket' },
  { id: 'sentBack', source: 'sendBack', target: 'ticket', toState: 'inProgress' },
  { id: 'merged', source: 'merge', target: 'ticket', toState: 'done' },
]

// Tidy up, as the board does it: nothing leads into the end and every node
// leads somewhere, so no edge reaches the end.
const nodes = Object.entries(sizes).map(([id, size]): Node => ({ id, position: { x: 0, y: 0 }, data: {}, measured: size }))
const edges = links.map((l): Edge => ({ id: l.id, source: l.source, target: l.target }))
const laid = tidy(nodes, edges, 'end')
const boxes: Box[] = Object.entries(sizes).map(([id, size]) => ({ id, ...laid.get(id)!, ...size }))
const box = (id: string) => boxes.find((b) => b.id === id)!

const others = boxes.filter((b) => b.id !== 'end')
check('the end goes below everything', Math.min(...others.map((b) => box('end').y - b.y - b.height)) > 0, true)
check('the start stays at the top', others.every((b) => b.id === 'start' || b.y > box('start').y), true)

// Handles sit just outside their node, as React Flow places them.
const HANDLE = 4
const ends = links.map((l): LinkEnds => {
  const s = box(l.source)
  const t = box(l.target)
  return {
    id: l.id,
    source: l.source,
    target: l.target,
    from: l.fromState
      ? { x: s.x + s.width + HANDLE, y: s.y + rows[l.fromState], position: Position.Right }
      : { x: s.x + s.width / 2, y: s.y + s.height + HANDLE, position: Position.Bottom },
    to: l.toState
      ? { x: t.x - HANDLE, y: t.y + rows[l.toState], position: Position.Left }
      : { x: t.x + t.width / 2, y: t.y - HANDLE, position: Position.Top },
    spreadFrom: !l.fromState,
    spreadTo: !l.toState,
  }
})
const started = performance.now()
const routes = routeLinks(boxes, ends)
const took = performance.now() - started

const drawn = (e: LinkEnds, r: LinkRoute) => {
  const from = { ...e.from, x: e.from.x + r.fromShift }
  const to = { ...e.to, x: e.to.x + (e.to.position === Position.Top ? r.toShift : 0) }
  return routePoints(from, to, e.stored, r.lanes)
}
const paths = new Map(ends.map((e) => [e.id, drawn(e, routes.get(e.id)!)]))

type Segment = { link: string; a: XYPosition; b: XYPosition; end: boolean }
const segments: Segment[] = [...paths].flatMap(([link, points]) =>
  points
    .slice(0, -1)
    .map((a, i) => ({ link, a, b: points[i + 1], end: i === 0 || i === points.length - 2 }))
    .filter((s) => s.a.x !== s.b.x || s.a.y !== s.b.y),
)

// No route runs through a node, its own ones included.
const through = segments.flatMap((s) =>
  boxes
    .filter((b) => {
      const [x0, x1] = [Math.min(s.a.x, s.b.x), Math.max(s.a.x, s.b.x)]
      const [y0, y1] = [Math.min(s.a.y, s.b.y), Math.max(s.a.y, s.b.y)]
      return x1 > b.x + 1 && x0 < b.x + b.width - 1 && y1 > b.y + 1 && y0 < b.y + b.height - 1
    })
    .map((b) => `${s.link} through ${b.id}`),
)
check('no link runs through a node', through, [])

// No two links run along each other, or so close they look as if they did,
// but where they leave or reach one state.
const along = segments.flatMap((s, i) =>
  segments.slice(i + 1).flatMap((t) => {
    if (s.link === t.link || s.end || t.end) return []
    const upright = s.a.x === s.b.x
    if (upright !== (t.a.x === t.b.x)) return []
    if (Math.abs(upright ? s.a.x - t.a.x : s.a.y - t.a.y) >= 6) return []
    const [s0, s1] = upright ? [Math.min(s.a.y, s.b.y), Math.max(s.a.y, s.b.y)] : [Math.min(s.a.x, s.b.x), Math.max(s.a.x, s.b.x)]
    const [t0, t1] = upright ? [Math.min(t.a.y, t.b.y), Math.max(t.a.y, t.b.y)] : [Math.min(t.a.x, t.b.x), Math.max(t.a.x, t.b.x)]
    return Math.min(s1, t1) - Math.max(s0, t0) > 1 ? [`${s.link} along ${t.link}`] : []
  }),
)
check('no two links run along each other', along, [])

// The fetch's top takes the start and the query, at two places.
const fetchTop = ['start', 'query'].map((id) => routes.get(id)!.toShift)
check('ends sharing a side are spread', fetchTop[0] !== fetchTop[1], true)

// The start leads straight down into the fetch.
const start = paths.get('start')!
check('the start leads straight down', start.every((p) => p.x === start[0].x), true)

// The fetch leads into its ticket's In Review state, leaving down and
// reaching the state from the left, clear of the ticket.
const fetched = paths.get('fetched')!
check('the fetch reaches the state from the left', fetched[fetched.length - 2].x < box('ticket').x, true)

// A link moved by hand keeps its lanes.
const moved = { ...ends[0], stored: [box('fetch').y - 30] }
check('a link moved by hand keeps its lanes', routeLinks(boxes, [moved]).get(moved.id)!.lanes, null)

// A link with a node in its way goes round it.
const wall: Box[] = [
  { id: 'a', x: 0, y: 0, width: 200, height: 60 },
  { id: 'wall', x: -100, y: 120, width: 400, height: 60 },
  { id: 'b', x: 0, y: 240, width: 200, height: 60 },
]
const round: LinkEnds = {
  id: 'round',
  source: 'a',
  target: 'b',
  from: { x: 100, y: 64, position: Position.Bottom },
  to: { x: 100, y: 236, position: Position.Top },
  spreadFrom: true,
  spreadTo: true,
}
const roundPath = routePoints(round.from, round.to, null, routeLinks(wall, [round]).get('round')!.lanes)
check(
  'a link goes round a node in its way',
  roundPath.every((p) => p.y < 120 - 11 || p.y > 180 + 11 || p.x < -100 - 11 || p.x > 300 + 11),
  true,
)

if (took > 250) console.warn(`routing the board took ${took.toFixed(0)}ms`)
console.log(`${checked} checks of the board's links, ${failed} failed`)
if (failed > 0) process.exitCode = 1
