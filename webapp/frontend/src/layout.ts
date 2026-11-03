import dagre from '@dagrejs/dagre'
import type { Edge, Node, XYPosition } from '@xyflow/react'

// Vertical gap between ranks and horizontal gap between nodes in a rank,
// leaving room between them for a few links' lanes besides the stubs they
// leave and enter nodes by (route.ts).
export const RANK_GAP = 80
const NODE_GAP = 60

// Lays the flow out top to bottom, ranked by its edges. dagre breaks cycles,
// such as a review sending work back, by laying one of their edges upwards.
// The node `last` names, the board's end, goes below everything in the
// middle when no edge reaches it, rather than in the top rank with whatever
// else no edge reaches, as when every process of a flow leads back into an
// object. Returns each node's new top-left position.
export function tidy(nodes: Node[], edges: Edge[], last?: string): Map<string, XYPosition> {
  const size = (n: Node) => ({ width: n.measured?.width ?? 0, height: n.measured?.height ?? 0 })
  const apart = last !== undefined && !edges.some((e) => e.source === last || e.target === last)
  const ranked = apart ? nodes.filter((n) => n.id !== last) : nodes
  const g = new dagre.graphlib.Graph()
  g.setGraph({ rankdir: 'TB', ranksep: RANK_GAP, nodesep: NODE_GAP })
  g.setDefaultEdgeLabel(() => ({}))
  for (const n of ranked) g.setNode(n.id, size(n))
  for (const e of edges) if (g.hasNode(e.source) && g.hasNode(e.target)) g.setEdge(e.source, e.target)
  dagre.layout(g)

  // dagre positions node centers.
  const laid = new Map(
    ranked.map((n): [string, XYPosition] => {
      const { x, y, width, height } = g.node(n.id)
      return [n.id, { x: x - width / 2, y: y - height / 2 }]
    }),
  )
  const end = apart ? nodes.find((n) => n.id === last) : undefined
  if (end) {
    const boxes = ranked.map((n) => ({ ...laid.get(n.id)!, ...size(n) }))
    const left = Math.min(...boxes.map((b) => b.x))
    const right = Math.max(...boxes.map((b) => b.x + b.width))
    const bottom = Math.max(...boxes.map((b) => b.y + b.height))
    laid.set(end.id, boxes.length === 0 ? { x: 0, y: 0 } : { x: (left + right - size(end).width) / 2, y: bottom + RANK_GAP })
  }
  return laid
}
