import { Position, type XYPosition } from '@xyflow/react'

// A link's route runs in straight segments that turn at right angles. It
// leaves its source along the handle's axis, then alternates between
// horizontal and vertical. Lanes are where the segments in between run: the
// first move along the source's axis ends at lanes[0], the next, across it,
// at lanes[1], and so on. After the lanes, the route lines up with the target
// and enters it along its handle's axis. Lanes are absolute flow
// coordinates, so a route keeps its shape when its ends move, and the
// segments at either end stretch to follow them.

export type Lanes = number[]

export type End = XYPosition & { position: Position }

// How far a route leaves an end before turning, and how far to the side a
// link running back up the flow goes round.
const STUB = 20
const SIDE = 160

// Snaps dragged lanes to half the background's dot spacing.
const GRID = 9

const vertical = (p: Position) => p === Position.Top || p === Position.Bottom

// The direction a route moves along its source's axis when leaving it.
const outward = (p: Position) => (p === Position.Bottom || p === Position.Right ? 1 : -1)

// The direction a route moves along its target's axis when entering it.
const inward = (p: Position) => (p === Position.Top || p === Position.Left ? 1 : -1)

const along = (p: XYPosition, isVertical: boolean) => (isVertical ? p.y : p.x)
const across = (p: XYPosition, isVertical: boolean) => (isVertical ? p.x : p.y)

// The number of lanes must leave the route moving across the target's axis,
// so the last two moves can line it up and enter it.
function fits(source: End, target: End, lanes: Lanes): boolean {
  const same = vertical(source.position) === vertical(target.position)
  return lanes.length % 2 === (same ? 1 : 0)
}

// A route going straight from source to target where it can, round the side
// where the target lies behind the source, as a review deciding on the
// object it took does. It looks at nothing but the link's ends, so it is
// only what a link falls back on until the board has routed it round the
// other nodes (routeLinks), or when no route keeps clear of them.
export function defaultLanes(source: End, target: End): Lanes {
  const sv = vertical(source.position)
  const tv = vertical(target.position)
  const leave = along(source, sv) + STUB * outward(source.position)
  const arrive = along(target, tv) - STUB * inward(target.position)
  if (sv !== tv) {
    // Reaching the target means going back against its way in, so first
    // move well clear of the source rather than straight back past it.
    const back = (along(target, tv) - along(source, tv)) * inward(target.position) < 0
    return [back ? along(source, sv) + SIDE * outward(source.position) : leave, arrive]
  }
  const gap = (along(target, sv) - along(source, sv)) * outward(source.position)
  if (outward(source.position) === inward(target.position) && gap >= 2 * STUB) {
    return [(along(source, sv) + along(target, sv)) / 2]
  }
  return [leave, Math.max(across(source, sv), across(target, sv)) + SIDE, arrive]
}

// Keeps the first and last lanes at least a stub clear of their ends, so a
// route leaves and enters each end in a short straight line, never along
// the node's edge or behind it, as a lane left where it was when its node
// moved would.
function clearOfEnds(source: End, target: End, lanes: Lanes): Lanes {
  if (lanes.length === 0) return lanes
  const clear = [...lanes]
  const leave = along(source, vertical(source.position)) + STUB * outward(source.position)
  clear[0] = outward(source.position) > 0 ? Math.max(clear[0], leave) : Math.min(clear[0], leave)
  const last = clear.length - 1
  const arrive = along(target, vertical(target.position)) - STUB * inward(target.position)
  clear[last] = inward(target.position) > 0 ? Math.min(clear[last], arrive) : Math.max(clear[last], arrive)
  return clear
}

// The corners of the route, both ends included. Lanes that don't fit the
// ends, as when a link was redrawn, give way to the board's route for it
// (routeLinks below), else the plain default.
export function routePoints(
  source: End,
  target: End,
  stored: Lanes | null | undefined,
  auto?: Lanes | null,
): XYPosition[] {
  return pointsOf(source, target, clearOfEnds(source, target, lanesOf(source, target, stored, auto)))
}

// The corners a route through the lanes turns at, both ends included: each
// lane's move, then lining up with the target, then entering it. Moves can
// have no length.
function pointsOf(source: End, target: End, lanes: Lanes): XYPosition[] {
  let isVertical = vertical(source.position)
  let at: XYPosition = { x: source.x, y: source.y }
  const points = [at]
  for (const lane of lanes) {
    at = isVertical ? { x: at.x, y: lane } : { x: lane, y: at.y }
    points.push(at)
    isVertical = !isVertical
  }
  // Line up with the target, then enter it.
  points.push(isVertical ? { x: at.x, y: target.y } : { x: target.x, y: at.y })
  points.push({ x: target.x, y: target.y })
  return points
}

// The lanes the route has, as drawn: its stored ones if they fit, else the
// board's route for it, else the plain default.
export function lanesOf(source: End, target: End, stored: Lanes | null | undefined, auto?: Lanes | null): Lanes {
  if (stored && fits(source, target, stored)) return stored
  return auto && fits(source, target, auto) ? auto : defaultLanes(source, target)
}

// Moves segment i, the one from points[i] to points[i + 1], across its
// length to `to`, and returns the new lanes. The segments touching an end
// stay attached to it, so moving one of those adds a jog next to the end.
export function moveSegment(source: End, target: End, lanes: Lanes, i: number, to: number): Lanes {
  const value = Math.round(to / GRID) * GRID
  if (i === 0) {
    const sv = vertical(source.position)
    return [along(source, sv) + STUB * outward(source.position), value, ...lanes]
  }
  // Segment i runs where move i - 1 ended; the one lining up with the target
  // runs at the last lane.
  if (i <= lanes.length) return lanes.map((l, k) => (k === i - 1 ? value : l))
  const tv = vertical(target.position)
  return [...lanes, value, along(target, tv) - STUB * inward(target.position)]
}

// Each segment that has a length: its index, whether it runs vertically, and
// where its grip goes: its middle, or a quarter of the way along when the
// link's buttons sit in the middle.
export function segments(
  points: XYPosition[],
  label: XYPosition,
): { index: number; vertical: boolean; grip: XYPosition }[] {
  return points.slice(0, -1).flatMap((p, i) => {
    const q = points[i + 1]
    if (p.x === q.x && p.y === q.y) return []
    const mid = { x: (p.x + q.x) / 2, y: (p.y + q.y) / 2 }
    const busy = Math.hypot(mid.x - label.x, mid.y - label.y) < 24
    const grip = busy ? { x: p.x + (q.x - p.x) / 4, y: p.y + (q.y - p.y) / 4 } : mid
    return [{ index: i, vertical: p.x === q.x, grip }]
  })
}

// A level stretch at least this long takes a link's buttons and labels.
const LEVEL_LABEL = 60

// An SVG path through the points with rounded corners, and where the link's
// buttons and labels go: the middle of its longest level segment, if long
// enough, where a label as wide as the decision pill lies along the link
// rather than over a node beside it, as it would on an upright segment
// running down a node's side; else the middle of its longest segment.
export function roundedPath(points: XYPosition[], radius = 10): { path: string; label: XYPosition } {
  // Drop repeated points and points in the middle of a straight run.
  const pts = points.filter((p, i) => {
    const prev = points[i - 1]
    const next = points[i + 1]
    if (prev && prev.x === p.x && prev.y === p.y) return false
    if (!prev || !next) return true
    return !((prev.x === p.x && p.x === next.x) || (prev.y === p.y && p.y === next.y))
  })
  let path = `M ${pts[0].x} ${pts[0].y}`
  for (let i = 1; i < pts.length - 1; i++) {
    const [a, b, c] = [pts[i - 1], pts[i], pts[i + 1]]
    const r = Math.min(radius, Math.hypot(b.x - a.x, b.y - a.y) / 2, Math.hypot(c.x - b.x, c.y - b.y) / 2)
    const toward = (from: XYPosition, to: XYPosition, d: number) => {
      const len = Math.hypot(to.x - from.x, to.y - from.y) || 1
      return { x: from.x + ((to.x - from.x) / len) * d, y: from.y + ((to.y - from.y) / len) * d }
    }
    const before = toward(b, a, r)
    const after = toward(b, c, r)
    path += ` L ${before.x} ${before.y} Q ${b.x} ${b.y} ${after.x} ${after.y}`
  }
  const last = pts[pts.length - 1]
  path += ` L ${last.x} ${last.y}`

  let label = pts[0]
  let longest = -1
  let level: XYPosition | null = null
  let longestLevel = LEVEL_LABEL - 1
  for (let i = 0; i < pts.length - 1; i++) {
    const len = Math.hypot(pts[i + 1].x - pts[i].x, pts[i + 1].y - pts[i].y)
    const middle = { x: (pts[i].x + pts[i + 1].x) / 2, y: (pts[i].y + pts[i + 1].y) / 2 }
    if (len > longest) {
      longest = len
      label = middle
    }
    if (pts[i].y === pts[i + 1].y && len > longestLevel) {
      longestLevel = len
      level = middle
    }
  }
  return { path, label: level ?? label }
}

// Routing the board's links. A link nobody moved by hand runs clear of
// every node on its board: the shortest route, turning least, along the
// lines PAD out from the nodes' sides and the lines midway between them (an
// orthogonal visibility graph, searched with A*). The links at one side of a
// process or object, at its own handle rather than at a state, are spread
// along that side in the order their routes come and go, so they don't all
// meet in its middle; and links whose routes would run along the same
// stretch are moved apart within the room around it, ordered so they cross
// each other least.

// How far routes keep from nodes, and what a turn costs, in length.
const PAD = 12
const TURN = 40
// Running along a node's side costs a little more than down the middle of
// the room between nodes, so a route keeps to the middle where it can.
const HUG = 0.2
// Running within a lane of a route already there costs more again for each
// such route, and crossing one costs as much as this much length, so a route
// goes another way where one is as short, or nearly.
const CLOSE = 0.3
const CROSS = 25
// How far apart links sharing a stretch run, and the ends sharing a side.
const LANE_GAP = 10
const PORT_GAP = 24
// How far the ends on a side keep from its corners, at most: a process's
// corners are as round as it is tall.
const CORNER = 32
// A search that hasn't found a route by then gives way to the plain default.
const SEARCH_LIMIT = 200_000
// How many times routes are moved apart at most.
const NUDGE_ROUNDS = 4

// A node's box on the board, which routes keep clear of.
export type Box = { id: string; x: number; y: number; width: number; height: number }

// A link to route: the nodes it joins and its ends as drawn; whether each end
// is its node's own handle, spread along the side with the others there,
// rather than a state's; and the lanes it was moved to by hand, if any.
export type LinkEnds = {
  id: string
  source: string
  target: string
  from: End
  to: End
  spreadFrom: boolean
  spreadTo: boolean
  stored?: Lanes | null
}

// How a link is drawn: how far each end moves along its side, and the lanes
// it runs in, null for a link moved by hand, which keeps its own.
export type LinkRoute = { fromShift: number; toShift: number; lanes: Lanes | null }

type Shift = { from: number; to: number }
type Routed = { id: string; from: End; to: End; lanes: Lanes }

// An end moved along its side.
export const shifted = (end: End, by: number): End =>
  vertical(end.position) ? { ...end, x: end.x + by } : { ...end, y: end.y + by }

// Routes the links of a board whose nodes have these boxes. The shortest
// links go first, and each later one keeps away from the routes before it
// where it can, so links take different ways round rather than all one.
// While nodes are dragged, only the links to and from them are routed
// again, the others keeping the lanes they had, so a drag stays smooth on a
// large board; they are all routed again once it ends.
export function routeLinks(
  boxes: Box[],
  links: LinkEnds[],
  dragging?: { moving: Set<string>; before: Map<string, LinkRoute> },
): Map<string, LinkRoute> {
  const byId = new Map(boxes.map((b) => [b.id, b]))
  const grid = new Grid(boxes)
  const byHand = (l: LinkEnds) => Boolean(l.stored && fits(l.from, l.to, l.stored))
  const lanes = new Map<string, Lanes>()
  let shifts = spreadEnds(byId, links, (l, end) => (end === 'from' ? l.to.x : l.from.x))
  const ends = (l: LinkEnds): [End, End] => {
    const s = shifts.get(l.id)!
    return [shifted(l.from, s.from), shifted(l.to, s.to)]
  }
  // Where each link routed so far runs, those moved by hand from the start.
  const paths = new Map<string, XYPosition[]>()
  const draw = (l: LinkEnds) => {
    const [from, to] = ends(l)
    const own = byHand(l) ? l.stored! : (lanes.get(l.id) ?? defaultLanes(from, to))
    paths.set(l.id, collapse(pointsOf(from, to, clearOfEnds(from, to, own))))
  }
  const route = (l: LinkEnds) => {
    const [from, to] = ends(l)
    const others = [...paths].filter(([id]) => id !== l.id).map(([, path]) => path)
    lanes.set(l.id, grid.route(from, to, others) ?? defaultLanes(from, to))
    draw(l)
  }
  const kept = (l: LinkEnds) => {
    if (!dragging || dragging.moving.has(l.source) || dragging.moving.has(l.target)) return false
    const lanes = dragging.before.get(l.id)?.lanes
    return Boolean(lanes && fits(l.from, l.to, lanes))
  }
  for (const l of links) {
    if (kept(l)) lanes.set(l.id, [...dragging!.before.get(l.id)!.lanes!])
    if (byHand(l) || kept(l)) draw(l)
  }
  const reach = (l: LinkEnds) => Math.abs(l.to.x - l.from.x) + Math.abs(l.to.y - l.from.y)
  const shortest = links.filter((l) => !byHand(l) && !kept(l)).sort((a, b) => reach(a) - reach(b) || (a.id < b.id ? -1 : 1))
  for (const l of shortest) route(l)

  // Spread the ends again in the order the routes leave and reach their
  // sides, and route again the links whose ends moved.
  const was = shifts
  shifts = spreadEnds(byId, links, (l, end) => approach(paths.get(l.id)!, end))
  for (const l of links) {
    const a = was.get(l.id)!
    const b = shifts.get(l.id)!
    if (a.from !== b.from || a.to !== b.to) {
      if (byHand(l) || kept(l)) draw(l)
      else route(l)
    }
  }

  const routed: Routed[] = links
    .filter((l) => !byHand(l))
    .map((l) => {
      const [from, to] = ends(l)
      return { id: l.id, from, to, lanes: [...lanes.get(l.id)!] }
    })
  // Moving some apart can bring them near others, so again until none move.
  for (let round = 0; round < NUDGE_ROUNDS; round++) {
    const moved = nudge(routed, boxes, true)
    if (!nudge(routed, boxes, false) && !moved) break
  }
  const done = new Map(routed.map((r) => [r.id, r.lanes]))
  return new Map(
    links.map((l) => {
      const s = shifts.get(l.id)!
      return [l.id, { fromShift: s.from, toShift: s.to, lanes: done.get(l.id) ?? null }]
    }),
  )
}

// Where a route comes from or goes to beside its end: the across coordinate
// of the corner after its first turn, or before its last; for a straight
// route, the other end's.
function approach(path: XYPosition[], end: 'from' | 'to'): number {
  if (path.length < 3) return end === 'from' ? path[path.length - 1].x : path[0].x
  return end === 'from' ? path[2].x : path[path.length - 3].x
}

// How far each link's ends move along the top or bottom of their nodes: the
// ends sharing a side are spaced out along it, in the order of their keys,
// keeping off its rounded corners. An end alone on its side lines up with
// the end it faces, if that is within its side, so a link straight down
// stays straight.
function spreadEnds(
  byId: Map<string, Box>,
  links: LinkEnds[],
  key: (l: LinkEnds, end: 'from' | 'to') => number,
): Map<string, Shift> {
  const shifts = new Map(links.map((l): [string, Shift] => [l.id, { from: 0, to: 0 }]))
  type Side = { box: Box | undefined; ends: { link: LinkEnds; end: 'from' | 'to'; key: number }[] }
  const sides = new Map<string, Side>()
  const add = (node: string, position: Position, link: LinkEnds, end: 'from' | 'to') => {
    const k = `${node} ${position}`
    const side = sides.get(k) ?? { box: byId.get(node), ends: [] }
    side.ends.push({ link, end, key: key(link, end) })
    sides.set(k, side)
  }
  for (const l of links) {
    if (l.spreadFrom && vertical(l.from.position)) add(l.source, l.from.position, l, 'from')
    if (l.spreadTo && vertical(l.to.position)) add(l.target, l.to.position, l, 'to')
  }
  const span = (box: Box | undefined) => (box ? Math.max(0, box.width - 2 * Math.min(CORNER, box.height / 2)) : 0)
  const alone: { link: LinkEnds; end: 'from' | 'to'; box: Box | undefined }[] = []
  for (const { box, ends } of sides.values()) {
    if (ends.length === 1) {
      alone.push({ ...ends[0], box })
      continue
    }
    ends.sort((a, b) => a.key - b.key || (a.link.id < b.link.id ? -1 : a.link.id > b.link.id ? 1 : 0))
    const gap = Math.min(PORT_GAP, span(box) / (ends.length - 1))
    ends.forEach((e, i) => (shifts.get(e.link.id)![e.end] = (i - (ends.length - 1) / 2) * gap))
  }
  // Targets first, so a source alone lines up with a target that is too.
  alone.sort((a, b) => (a.end === b.end ? 0 : a.end === 'to' ? -1 : 1))
  for (const { link: l, end, box } of alone) {
    const facing = l.from.position === Position.Bottom && l.to.position === Position.Top && l.to.y > l.from.y
    if (!facing) continue
    const s = shifts.get(l.id)!
    const by = end === 'to' ? l.from.x + s.from - l.to.x : l.to.x + s.to - l.from.x
    if (Math.abs(by) <= span(box) / 2) s[end] = by
  }
  return shifts
}

// Points in a row without their repeats, and without the corners that don't
// turn.
function collapse(points: XYPosition[]): XYPosition[] {
  const out: XYPosition[] = []
  for (const p of points) {
    const last = out[out.length - 1]
    if (last && last.x === p.x && last.y === p.y) continue
    const before = out[out.length - 2]
    if (before && ((before.x === last.x && last.x === p.x) || (before.y === last.y && last.y === p.y))) out[out.length - 1] = p
    else out.push(p)
  }
  return out
}

// Directions on the grid: right, down, left, up.
const DX = [1, 0, -1, 0]
const DY = [0, 1, 0, -1]
const direction = (p: Position) => (p === Position.Right ? 0 : p === Position.Bottom ? 1 : p === Position.Left ? 2 : 3)

// The grid routes are searched on: the lines PAD out from each node's sides
// and the lines midway between neighbouring ones, and which of the cells
// between the lines lie inside a node, padded. A route may run along a
// node's padded side but never through it.
class Grid {
  private xs: number[]
  private ys: number[]
  private xHug: Set<number>
  private yHug: Set<number>
  private inside: Uint8Array

  constructor(boxes: Box[]) {
    const padded = boxes.map((b) => ({ l: b.x - PAD, r: b.x + b.width + PAD, t: b.y - PAD, b: b.y + b.height + PAD }))
    this.xHug = new Set(padded.flatMap((p) => [p.l, p.r]))
    this.yHug = new Set(padded.flatMap((p) => [p.t, p.b]))
    this.xs = withMiddles([...this.xHug])
    this.ys = withMiddles([...this.yHug])
    const w = Math.max(0, this.xs.length - 1)
    this.inside = new Uint8Array(w * Math.max(0, this.ys.length - 1))
    for (const p of padded) {
      const [i0, i1] = [this.xs.indexOf(p.l), this.xs.indexOf(p.r)]
      const [j0, j1] = [this.ys.indexOf(p.t), this.ys.indexOf(p.b)]
      for (let j = j0; j < j1; j++) for (let i = i0; i < i1; i++) this.inside[j * w + i] = 1
    }
  }

  // The lanes of the best route between the ends, or null if none keeps
  // clear of the nodes. The route leaves and enters each end along its
  // axis, a stub long at least, and keeps away from the other routes.
  route(from: End, to: End, others: XYPosition[][] = []): Lanes | null {
    const s = shifted(from, 0)
    const t = shifted(to, 0)
    const sv = vertical(from.position)
    const tv = vertical(to.position)
    if (sv) s.y += STUB * outward(from.position)
    else s.x += STUB * outward(from.position)
    if (tv) t.y -= STUB * inward(to.position)
    else t.x -= STUB * inward(to.position)

    // The grid's lines and the stubs' ends, and which cell of the grid's
    // each cell between them lies in.
    const xs = merged(this.xs, [s.x, t.x])
    const ys = merged(this.ys, [s.y, t.y])
    const nx = xs.length
    const ny = ys.length
    const cellOf = (coords: number[], base: number[]) => {
      const cells = new Int32Array(coords.length)
      let k = -1
      coords.forEach((c, i) => {
        while (k + 1 < base.length && base[k + 1] <= c) k++
        cells[i] = k < base.length - 1 ? k : -1
      })
      return cells
    }
    const cx = cellOf(xs, this.xs)
    const cy = cellOf(ys, this.ys)
    const w = this.xs.length - 1
    const inside = (i: number, j: number) => {
      if (i < 0 || j < 0 || i >= nx - 1 || j >= ny - 1) return false
      const bi = cx[i]
      const bj = cy[j]
      return bi >= 0 && bj >= 0 && this.inside[bj * w + bi] === 1
    }
    // A step runs through a node when the cells on both sides of it are inside one.
    const blocked = (i: number, j: number, d: number) =>
      d === 0
        ? inside(i, j - 1) && inside(i, j)
        : d === 2
          ? inside(i - 1, j - 1) && inside(i - 1, j)
          : d === 1
            ? inside(i - 1, j) && inside(i, j)
            : inside(i - 1, j - 1) && inside(i, j - 1)

    // What each step costs beyond its length for the other routes: running
    // within a lane of one, or crossing it. A stretch of another route at
    // `at` from lo to hi runs along the lines beside it, steps over `along`
    // the line's coordinates, and crosses the lines between lo and hi across
    // it, where the step over `at` crosses it. Steps along rows are
    // numbered by row, those along columns by column.
    const alongRow = new Float64Array(Math.max(0, nx - 1) * ny)
    const alongColumn = new Float64Array(nx * Math.max(0, ny - 1))
    const paint = (
      along: number[],
      beside: number[],
      at: number,
      lo: number,
      hi: number,
      runs: (line: number, step: number, length: number) => void,
      crosses: (line: number, step: number) => void,
    ) => {
      for (let line = lowerBound(beside, at - LANE_GAP + 1e-6); line < beside.length && beside[line] < at + LANE_GAP; line++) {
        for (let step = Math.max(0, lowerBound(along, lo) - 1); step < along.length - 1 && along[step] < hi; step++) {
          const length = Math.min(along[step + 1], hi) - Math.max(along[step], lo)
          if (length > 0) runs(line, step, length)
        }
      }
      const over = lowerBound(beside, at + 1e-6) - 1
      if (over < 0 || over >= beside.length - 1) return
      for (let line = lowerBound(along, lo + 1e-6); line < along.length && along[line] < hi; line++) crosses(line, over)
    }
    const row = (j: number, i: number) => j * (nx - 1) + i
    const column = (i: number, j: number) => i * (ny - 1) + j
    for (const path of others) {
      for (let k = 0; k + 1 < path.length; k++) {
        const [a, b] = [path[k], path[k + 1]]
        if (a.y === b.y && a.x !== b.x) {
          paint(
            xs,
            ys,
            a.y,
            Math.min(a.x, b.x),
            Math.max(a.x, b.x),
            (j, i, length) => (alongRow[row(j, i)] += CLOSE * length),
            (i, j) => (alongColumn[column(i, j)] += CROSS),
          )
        } else if (a.x === b.x && a.y !== b.y) {
          paint(
            ys,
            xs,
            a.x,
            Math.min(a.y, b.y),
            Math.max(a.y, b.y),
            (i, j, length) => (alongColumn[column(i, j)] += CLOSE * length),
            (j, i) => (alongRow[row(j, i)] += CROSS),
          )
        }
      }
    }

    const si = xs.indexOf(s.x)
    const sj = ys.indexOf(s.y)
    const ti = xs.indexOf(t.x)
    const tj = ys.indexOf(t.y)
    const leave = direction(from.position)
    const enter = (direction(to.position) + 2) % 4
    const state = (i: number, j: number, d: number) => (j * nx + i) * 4 + d
    const cost = new Float64Array(nx * ny * 4).fill(Infinity)
    const prev = new Int32Array(nx * ny * 4).fill(-1)
    const estimate = (i: number, j: number) => Math.abs(xs[i] - xs[ti]) + Math.abs(ys[j] - ys[tj])
    const open = new Heap()
    const start = state(si, sj, leave)
    cost[start] = 0
    open.push(estimate(si, sj), start)
    let best = Infinity
    let reached = -1
    for (let steps = 0; open.size > 0; steps++) {
      if (steps > SEARCH_LIMIT) return null
      const [f, at] = open.pop()
      if (f >= best) break
      const d = at & 3
      const cell = at >> 2
      const i = cell % nx
      const j = (cell - i) / nx
      if (f > cost[at] + estimate(i, j) + 1e-6) continue
      if (i === ti && j === tj) {
        if (d === (enter + 2) % 4) continue
        const total = cost[at] + (d === enter ? 0 : TURN)
        if (total < best) {
          best = total
          reached = at
        }
        continue
      }
      for (let nd = 0; nd < 4; nd++) {
        if (nd === (d + 2) % 4) continue
        const ni = i + DX[nd]
        const nj = j + DY[nd]
        if (ni < 0 || nj < 0 || ni >= nx || nj >= ny || blocked(i, j, nd)) continue
        const horizontal = nd % 2 === 0
        const length = horizontal ? Math.abs(xs[ni] - xs[i]) : Math.abs(ys[nj] - ys[j])
        const hug = horizontal ? this.yHug.has(ys[j]) : this.xHug.has(xs[i])
        const extra = horizontal ? alongRow[row(j, Math.min(i, ni))] : alongColumn[column(i, Math.min(j, nj))]
        const next = state(ni, nj, nd)
        const c = cost[at] + length * (hug ? 1 + HUG : 1) + extra + (nd === d ? 0 : TURN)
        if (c < cost[next]) {
          cost[next] = c
          prev[next] = at
          open.push(c + estimate(ni, nj), next)
        }
      }
    }
    if (reached < 0) return null

    const path: XYPosition[] = []
    for (let at = reached; at >= 0; at = prev[at]) {
      const cell = at >> 2
      path.push({ x: xs[cell % nx], y: ys[Math.floor(cell / nx)] })
    }
    path.reverse()
    const points = collapse([{ x: from.x, y: from.y }, ...path, { x: to.x, y: to.y }])
    // A straight route still has a lane to line up in.
    const lanes =
      points.length <= 2
        ? [(along(from, sv) + along(to, sv)) / 2]
        : points.slice(1, -2).map((p, m) => (p.x === points[m + 2].x ? p.x : p.y))
    return fits(from, to, lanes) ? lanes : null
  }
}

// The coordinates sorted, without repeats, with the middles between
// neighbours that are apart.
function withMiddles(values: number[]): number[] {
  const sorted = [...new Set(values)].sort((a, b) => a - b)
  return sorted.flatMap((v, i) => (i > 0 && v - sorted[i - 1] > 2 * PAD ? [(v + sorted[i - 1]) / 2, v] : [v]))
}

// The index of the first of the sorted coordinates at or past the value.
function lowerBound(sorted: number[], value: number): number {
  let lo = 0
  let hi = sorted.length
  while (lo < hi) {
    const mid = (lo + hi) >> 1
    if (sorted[mid] < value) lo = mid + 1
    else hi = mid
  }
  return lo
}

// Sorted coordinates with a few more put in their places.
function merged(sorted: number[], more: number[]): number[] {
  return [...new Set([...sorted, ...more])].sort((a, b) => a - b)
}

// A binary heap of states by their estimated cost, the least first.
class Heap {
  private keys: number[] = []
  private values: number[] = []

  get size() {
    return this.keys.length
  }

  push(key: number, value: number) {
    const { keys, values } = this
    let i = keys.length
    keys.push(key)
    values.push(value)
    while (i > 0) {
      const parent = (i - 1) >> 1
      if (keys[parent] <= key) break
      keys[i] = keys[parent]
      values[i] = values[parent]
      i = parent
    }
    keys[i] = key
    values[i] = value
  }

  pop(): [number, number] {
    const { keys, values } = this
    const top: [number, number] = [keys[0], values[0]]
    const key = keys.pop()!
    const value = values.pop()!
    if (keys.length > 0) {
      let i = 0
      for (;;) {
        let child = 2 * i + 1
        if (child >= keys.length) break
        if (child + 1 < keys.length && keys[child + 1] < keys[child]) child++
        if (keys[child] >= key) break
        keys[i] = keys[child]
        values[i] = values[child]
        i = child
      }
      keys[i] = key
      values[i] = value
    }
    return top
  }
}

// A stretch of a route that could be moved apart from others: one of its
// lanes, where it runs (c) and from where to where along it, and which way
// the route turns at each end, -1 towards lower coordinates, 1 higher.
type Stretch = { route: Routed; lane: number; c: number; lo: number; hi: number; turnLo: number; turnHi: number }

// Moves apart the vertical (or horizontal) stretches of routes that run
// along the same line, or nearly, and overlap, keeping each within the room
// the nodes beside it and its own ends leave. Says whether any moved.
function nudge(routes: Routed[], boxes: Box[], upright: boolean): boolean {
  const stretches: Stretch[] = []
  const at = (p: XYPosition) => (upright ? p.x : p.y)
  const on = (p: XYPosition) => (upright ? p.y : p.x)
  for (const route of routes) {
    const points = pointsOf(route.from, route.to, route.lanes)
    for (let m = 1; m <= route.lanes.length; m++) {
      const a = points[m]
      const b = points[m + 1]
      if (a.x === b.x && a.y === b.y) continue
      if ((a.x === b.x) !== upright) continue
      const c = at(a)
      const turn = (k: number, step: number) => {
        for (; k >= 0 && k < points.length; k += step) {
          const t = Math.sign(at(points[k]) - c)
          if (t !== 0) return t
        }
        return 0
      }
      const [ta, tb] = [turn(m - 1, -1), turn(m + 2, 1)]
      const forward = on(a) < on(b)
      stretches.push({
        route,
        lane: m - 1,
        c,
        lo: Math.min(on(a), on(b)),
        hi: Math.max(on(a), on(b)),
        turnLo: forward ? ta : tb,
        turnHi: forward ? tb : ta,
      })
    }
  }
  // Stretches less than a lane apart whose spans overlap are moved apart
  // together, with those they are close to in turn.
  stretches.sort((a, b) => a.c - b.c || a.lo - b.lo)
  const group = stretches.map((_, i) => i)
  const find = (i: number): number => (group[i] === i ? i : (group[i] = find(group[i])))
  for (let i = 0; i < stretches.length; i++) {
    const a = stretches[i]
    for (let j = i + 1; j < stretches.length && stretches[j].c - a.c < LANE_GAP; j++) {
      const b = stretches[j]
      if (b.lo <= a.hi && a.lo <= b.hi) group[find(j)] = find(i)
    }
  }
  const groups = new Map<number, Stretch[]>()
  stretches.forEach((s, i) => groups.set(find(i), [...(groups.get(find(i)) ?? []), s]))
  let moved = false
  for (const g of groups.values()) if (g.length > 1 && moveApart(g, boxes, upright)) moved = true
  return moved
}

// Which of two stretches on one line goes nearer lower coordinates, so that
// where both turn the same way at an end, the one reaching further turns
// outside the other, and where they turn apart, each keeps to its side.
function order(a: Stretch, b: Stretch): number {
  let vote = 0
  if (a.turnLo !== 0 && b.turnLo !== 0) {
    if (a.turnLo !== b.turnLo) vote += a.turnLo
    else if (a.lo !== b.lo) vote += (a.lo < b.lo) === a.turnLo > 0 ? -1 : 1
  }
  if (a.turnHi !== 0 && b.turnHi !== 0) {
    if (a.turnHi !== b.turnHi) vote += a.turnHi
    else if (a.hi !== b.hi) vote += (a.hi > b.hi) === a.turnHi > 0 ? -1 : 1
  }
  return vote
}

// Spaces out a group of stretches a lane apart, or as near that as the room
// lets them, around where they ran, in the order that crosses least. Each
// keeps within its own room: the nodes beside its own span, and its route's
// ends. Says whether any moved.
function moveApart(group: Stretch[], boxes: Box[], upright: boolean): boolean {
  const low = Math.min(...group.map((s) => s.c))
  const high = Math.max(...group.map((s) => s.c))
  const room = group.map((s) => {
    let min = -Infinity
    let max = Infinity
    for (const b of boxes) {
      const [from, to, near, far] = upright ? [b.y, b.y + b.height, b.x, b.x + b.width] : [b.x, b.x + b.width, b.y, b.y + b.height]
      if (to <= s.lo || from >= s.hi) continue
      if (far <= low) min = Math.max(min, far + PAD)
      else if (near >= high) max = Math.min(max, near - PAD)
    }
    // A route's first and last lanes keep a stub from its ends.
    const { route, lane } = s
    if (lane === 0) {
      const leave = along(route.from, vertical(route.from.position)) + STUB * outward(route.from.position)
      if (outward(route.from.position) > 0) min = Math.max(min, leave)
      else max = Math.min(max, leave)
    }
    if (lane === route.lanes.length - 1) {
      const arrive = along(route.to, vertical(route.to.position)) - STUB * inward(route.to.position)
      if (inward(route.to.position) > 0) max = Math.min(max, arrive)
      else min = Math.max(min, arrive)
    }
    return { min, max }
  })
  const ranked = group
    .map((s, k) => ({ s, k }))
    .sort((a, b) => order(a.s, b.s) || a.s.c - b.s.c || (a.s.route.id < b.s.route.id ? -1 : a.s.route.id > b.s.route.id ? 1 : 0))
    .map(({ k }) => k)

  // The widest gap, up to a lane, at which they fit in order in their rooms.
  const packed = (gap: number): number[] | null => {
    const at: number[] = []
    let last = -Infinity
    for (const k of ranked) {
      last = Math.max(room[k].min, last + gap)
      if (last > room[k].max) return null
      at.push(last)
    }
    return at
  }
  let gap = LANE_GAP
  if (!packed(gap)) {
    if (!packed(0)) return false
    let [fits, fails] = [0, LANE_GAP]
    for (let i = 0; i < 10; i++) {
      const mid = (fits + fails) / 2
      if (packed(mid)) fits = mid
      else fails = mid
    }
    gap = fits
  }
  // Around where they ran, pushed apart forwards, back into their rooms,
  // and forwards again; else as tightly packed from the low side.
  const centre = group.reduce((sum, s) => sum + s.c, 0) / group.length
  const at = ranked.map((_, i) => centre + (i - (ranked.length - 1) / 2) * gap)
  const pass = (forwards: boolean) => {
    for (let n = 0; n < at.length; n++) {
      const i = forwards ? n : at.length - 1 - n
      const { min, max } = room[ranked[i]]
      at[i] = forwards ? Math.max(at[i], min, i > 0 ? at[i - 1] + gap : -Infinity) : Math.min(at[i], max, i < at.length - 1 ? at[i + 1] - gap : Infinity)
    }
  }
  pass(true)
  pass(false)
  pass(true)
  const fine = at.every((p, i) => p >= room[ranked[i]].min - 1e-6 && p <= room[ranked[i]].max + 1e-6)
  const placed = fine ? at : packed(gap)!
  let moved = false
  ranked.forEach((k, i) => {
    const s = group[k]
    if (Math.abs(s.route.lanes[s.lane] - placed[i]) > 0.5) moved = true
    s.route.lanes[s.lane] = placed[i]
  })
  return moved
}
