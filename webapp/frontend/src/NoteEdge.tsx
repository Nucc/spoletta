import { BaseEdge, useInternalNode, type Edge, type EdgeProps, type InternalNode } from '@xyflow/react'

export type NoteEdgeType = Edge<Record<string, never>, 'note'>

// Where the line from one node's middle to another's leaves its box.
function borderPoint(from: InternalNode, to: InternalNode): { x: number; y: number } {
  const w = (from.measured.width ?? 0) / 2
  const h = (from.measured.height ?? 0) / 2
  const cx = from.internals.positionAbsolute.x + w
  const cy = from.internals.positionAbsolute.y + h
  const dx = to.internals.positionAbsolute.x + (to.measured.width ?? 0) / 2 - cx
  const dy = to.internals.positionAbsolute.y + (to.measured.height ?? 0) / 2 - cy
  if (dx === 0 && dy === 0) return { x: cx, y: cy }
  const scale = Math.min(w / Math.abs(dx || Infinity), h / Math.abs(dy || Infinity))
  return { x: cx + dx * scale, y: cy + dy * scale }
}

// A thin line from a note to what it points at, straight between the edges
// of the two, wherever either sits.
export default function NoteEdge({ id, source, target }: EdgeProps<NoteEdgeType>) {
  const from = useInternalNode(source)
  const to = useInternalNode(target)
  if (!from || !to) return null
  const a = borderPoint(from, to)
  const b = borderPoint(to, from)
  return <BaseEdge id={id} className="note-edge" path={`M ${a.x},${a.y} L ${b.x},${b.y}`} />
}
