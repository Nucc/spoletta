import { Handle, Position, type Node, type NodeProps } from '@xyflow/react'
import { NameInput } from './ProcessNode'

export type ObjectNodeData = {
  name: string
  // True while a new object is being named or an existing one renamed.
  editing: boolean
  // Set once a process generated this object in the latest flow run.
  status?: 'done'
  // What the generating process printed in the latest run; passed to the processes that take this object.
  content?: string
  onCommit: (id: string, name: string) => void
  onCancel: (id: string) => void
}

export type ObjectNodeType = Node<ObjectNodeData, 'object'>

// OPM object: a rectangle that processes generate (arrow in) and take as input (arrow out).
export default function ObjectNode({ id, data, selected }: NodeProps<ObjectNodeType>) {
  return (
    <div
      className={`object-node${selected ? ' selected' : ''}${data.status ? ` ${data.status}` : ''}`}
      title={data.content}
    >
      <Handle type="target" position={Position.Left} />
      <Handle type="source" position={Position.Right} />
      {data.editing ? (
        <NameInput
          initial={data.name}
          placeholder="Object name"
          onCommit={(name) => data.onCommit(id, name)}
          onCancel={() => data.onCancel(id)}
        />
      ) : (
        <span className="object-name">{data.name}</span>
      )}
    </div>
  )
}
