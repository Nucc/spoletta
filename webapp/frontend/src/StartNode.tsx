import { Handle, Position, useNodeConnections, type Node, type NodeProps } from '@xyflow/react'
import { PlayIcon } from './icons'

export type StartNodeType = Node<Record<string, never>, 'start'>

// UML initial node, drawn as a card with a single outgoing connection. On a
// flow process's board, the objects the flow process takes lead into it.
export default function StartNode({ selected }: NodeProps<StartNodeType>) {
  const taking = useNodeConnections({ handleType: 'target' }).length > 0
  return (
    <div className={`start-node${selected ? ' selected' : ''}`}>
      {/* Hidden rather than left out, so that it is measured before the objects lead into it. */}
      <Handle type="target" position={Position.Top} isConnectable={false} style={taking ? undefined : { visibility: 'hidden' }} />
      <span className="node-icon">
        <PlayIcon />
      </span>
      <span className="node-name">Start</span>
      <Handle type="source" position={Position.Bottom} />
    </div>
  )
}
