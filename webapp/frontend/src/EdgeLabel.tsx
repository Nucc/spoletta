// A link's labels and buttons, drawn by React Flow in a layer of their own
// over the board rather than with the link, so they fade with it while
// another node's links are in focus (`dim` in the link's data, Board.tsx).
import { createContext, useContext, type ReactNode } from 'react'
import { EdgeLabelRenderer } from '@xyflow/react'

export const EdgeDimmed = createContext(false)

export default function EdgeLabel({ children }: { children: ReactNode }) {
  const dimmed = useContext(EdgeDimmed)
  return (
    <EdgeLabelRenderer>
      {dimmed ? <div className="edge-label-dim">{children}</div> : children}
    </EdgeLabelRenderer>
  )
}
