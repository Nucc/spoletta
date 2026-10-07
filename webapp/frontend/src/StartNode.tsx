import { Handle, Position, type Node, type NodeProps } from '@xyflow/react'

export type StartNodeType = Node<Record<string, never>, 'start'>

// UML initial node: a filled circle with a single outgoing connection.
export default function StartNode({ selected }: NodeProps<StartNodeType>) {
  return (
    <div className={`start-node${selected ? ' selected' : ''}`} title="Start">
      <Handle type="source" position={Position.Right} />
    </div>
  )
}
