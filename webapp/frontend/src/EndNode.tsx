import { Handle, Position, type Node, type NodeProps } from '@xyflow/react'
import { EndIcon } from './icons'

export type EndNodeType = Node<Record<string, never>, 'end'>

// UML activity final node of a board. On a flow process's board, the objects
// the flow process generates lead into it: what the processes inside leave of
// them comes out of the flow process. On a flow's top board, the objects its
// processes generate and none of them takes do: what the flow leaves.
export default function EndNode({ selected }: NodeProps<EndNodeType>) {
  return (
    <div className={`start-node end-node${selected ? ' selected' : ''}`}>
      <Handle type="target" position={Position.Top} isConnectable={false} />
      <span className="node-icon">
        <EndIcon />
      </span>
      <span className="node-name">End</span>
    </div>
  )
}
