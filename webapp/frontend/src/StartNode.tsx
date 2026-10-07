import { Handle, Position, type Node, type NodeProps } from '@xyflow/react'
import { PlayIcon } from './icons'

export type StartNodeType = Node<Record<string, never>, 'start'>

// UML initial node, drawn as a card with a single outgoing connection.
export default function StartNode({ selected }: NodeProps<StartNodeType>) {
  return (
    <div className={`start-node${selected ? ' selected' : ''}`}>
      <span className="node-icon">
        <PlayIcon />
      </span>
      <span className="node-name">Start</span>
      <Handle type="source" position={Position.Bottom} />
    </div>
  )
}
