import dagre from '@dagrejs/dagre'
import type { Edge, Node, XYPosition } from '@xyflow/react'

// Vertical gap between ranks and horizontal gap between nodes in a rank.
export const RANK_GAP = 60
const NODE_GAP = 50

// Lays the flow out top to bottom, ranked by its edges. dagre breaks cycles,
// such as a review sending work back, by laying one of their edges upwards.
// Returns each node's new top-left position.
export function tidy(nodes: Node[], edges: Edge[]): Map<string, XYPosition> {
  const g = new dagre.graphlib.Graph()
  g.setGraph({ rankdir: 'TB', ranksep: RANK_GAP, nodesep: NODE_GAP })
  g.setDefaultEdgeLabel(() => ({}))
  for (const n of nodes) {
    g.setNode(n.id, { width: n.measured?.width ?? 0, height: n.measured?.height ?? 0 })
  }
  for (const e of edges) g.setEdge(e.source, e.target)
  dagre.layout(g)

  // dagre positions node centers.
  return new Map(
    nodes.map((n) => {
      const { x, y, width, height } = g.node(n.id)
      return [n.id, { x: x - width / 2, y: y - height / 2 }]
    }),
  )
}
