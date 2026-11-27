import { useContext } from 'react'
import { BaseEdge, useInternalNode, type Edge, type EdgeProps, type InternalNode, type XYPosition } from '@xyflow/react'
import EdgeLabel, { EdgeDimmed } from './EdgeLabel'
import { handleEnd, LinkRoutes } from './LinkEdge'
import type { ObjectNodeType } from './ObjectNode'
import { referenceEnds, referenceHandle } from './references'
import { roundedPath, routePoints, shifted, type Box, type End } from './route'

// A reference on the board (references.ts): the copy of a struct an edge
// points at is, in each run, the record its source's attribute names.
export type ReferenceEdgeData = { attribute: string; dim?: boolean }

export type ReferenceEdgeType = Edge<ReferenceEdgeData, 'reference'>

// How long the arrowhead's sides are, and how far each opens from the line.
const ARROW = { length: 11, spread: Math.PI / 7 }

// A node's box on the board, as the router has it.
const boxOf = (n: InternalNode<ObjectNodeType>): Box => ({
  id: n.id,
  x: n.internals.positionAbsolute.x,
  y: n.internals.positionAbsolute.y,
  width: n.measured.width ?? 0,
  height: n.measured.height ?? 0,
})

// Where a link's end on one of a node's handles goes, by its id, or on its
// first of that type, the node's own; none before it is measured.
function endOf(n: InternalNode<ObjectNodeType>, type: 'source' | 'target', id: string | null): End | undefined {
  const all = n.internals.handleBounds?.[type] ?? []
  const h = id === null ? all[0] : all.find((b) => b.id === id)
  return h ? handleEnd(n.internals.positionAbsolute.x, n.internals.positionAbsolute.y, h) : undefined
}

// The open arrowhead at the route's end, along its last stretch.
function arrowhead(points: XYPosition[]): string {
  const end = points[points.length - 1]
  const before = [...points].reverse().find((p) => p.x !== end.x || p.y !== end.y) ?? end
  const angle = Math.atan2(end.y - before.y, end.x - before.x)
  const side = (turn: number) =>
    `${end.x - ARROW.length * Math.cos(angle + turn)},${end.y - ARROW.length * Math.sin(angle + turn)}`
  return `M ${side(ARROW.spread)} L ${end.x},${end.y} L ${side(-ARROW.spread)}`
}

// OPM's tagged structural link: a dashed line from the object holding the
// reference to the one standing for the record it names, leaving from the
// row of the attribute it goes through and coming into the top of the other,
// routed round the other nodes as processes' links are, ending in an open
// arrowhead and tagged with the attribute, such as a pull request's ticket.
// Dashed and grey, so it never reads as a process's link.
export default function ReferenceEdge({ id, source, target, data }: EdgeProps<ReferenceEdgeType>) {
  const auto = useContext(LinkRoutes).get(id)
  const holder = useInternalNode<ObjectNodeType>(source)
  const followed = useInternalNode<ObjectNodeType>(target)
  if (!holder || !followed) return null
  const attribute = data?.attribute ?? ''
  const along = endOf(holder, 'source', referenceHandle(attribute))
  const [from, to] = referenceEnds(boxOf(holder), boxOf(followed), along, endOf(followed, 'target', null))
  const points = routePoints(shifted(from, auto?.fromShift ?? 0), shifted(to, auto?.toShift ?? 0), null, auto?.lanes)
  const { path, label } = roundedPath(points)
  return (
    <EdgeDimmed.Provider value={Boolean(data?.dim)}>
      <BaseEdge id={id} path={path} className="reference-path" />
      <path d={arrowhead(points)} className="reference-arrow" />
      <EdgeLabel>
        <div
          className="reference-label nodrag nopan"
          style={{ transform: `translate(-50%, -50%) translate(${label.x}px, ${label.y}px)` }}
          title={`In each run, ${followed.data.name} is the record ${holder.data.name}’s ${attribute} names`}
        >
          {attribute}
        </div>
      </EdgeLabel>
    </EdgeDimmed.Provider>
  )
}
