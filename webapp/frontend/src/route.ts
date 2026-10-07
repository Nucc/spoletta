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
// object it took does.
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

// The corners of the route, both ends included. Lanes that don't fit the
// ends, as when a link was redrawn, give way to the default route.
export function routePoints(source: End, target: End, stored: Lanes | null | undefined): XYPosition[] {
  const lanes = stored && fits(source, target, stored) ? stored : defaultLanes(source, target)
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

// The lanes the route has, as drawn: its stored ones if they fit, else the default.
export function lanesOf(source: End, target: End, stored: Lanes | null | undefined): Lanes {
  return stored && fits(source, target, stored) ? stored : defaultLanes(source, target)
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

// An SVG path through the points with rounded corners, and the middle of its
// longest segment, where the link's buttons go.
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
  for (let i = 0; i < pts.length - 1; i++) {
    const len = Math.hypot(pts[i + 1].x - pts[i].x, pts[i + 1].y - pts[i].y)
    if (len > longest) {
      longest = len
      label = { x: (pts[i].x + pts[i + 1].x) / 2, y: (pts[i].y + pts[i + 1].y) / 2 }
    }
  }
  return { path, label }
}
