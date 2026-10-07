import { useEffect, useRef, useState } from 'react'
import { Handle, Position, useUpdateNodeInternals, type Node, type NodeProps } from '@xyflow/react'
import type { Outcome, ProcessKind, RunInput, ThinkingLevel } from './api'

export type ProcessNodeData = {
  name: string
  kind: ProcessKind
  // Edited in the side panel, never rendered on the node.
  prompt: string
  script: string
  model: string
  thinking: ThinkingLevel | ''
  editing: boolean
  // Set while a flow run is in progress or after it has passed this process.
  status?: RunStatus
  // What the last run of this process printed, or why it failed.
  output?: string
  runError?: string
  // While a human process waits for a decision: the inputs under review.
  review?: RunInput[]
  onCommit: (id: string, name: string) => void
  onCancel: (id: string) => void
}

export type RunStatus = 'active' | 'waiting' | 'done' | 'error'

export type ProcessNodeType = Node<ProcessNodeData, 'process'>

// A human process leaves through one handle per outcome; ids match Outcome.
export const OUTCOME_HANDLES: { id: Outcome; position: Position; label: string }[] = [
  { id: 'approved', position: Position.Right, label: 'approved' },
  { id: 'changes', position: Position.Bottom, label: 'changes' },
]

export default function ProcessNode({ id, data, selected }: NodeProps<ProcessNodeType>) {
  const human = data.kind === 'human'
  const updateNodeInternals = useUpdateNodeInternals()

  // The handles differ between kinds; React Flow must re-measure them.
  useEffect(() => updateNodeInternals(id), [id, human, updateNodeInternals])

  return (
    <div className={`process-node${selected ? ' selected' : ''}${data.status ? ` ${data.status}` : ''}`}>
      <Handle type="target" position={Position.Left} />
      {human ? (
        OUTCOME_HANDLES.map((h) => (
          <Handle key={h.id} id={h.id} type="source" position={h.position} className={`outcome ${h.id}`}>
            <span className="outcome-label">{h.label}</span>
          </Handle>
        ))
      ) : (
        <Handle type="source" position={Position.Right} />
      )}
      <span className={`process-kind ${data.kind}`}>{data.kind}</span>
      {data.editing ? (
        <NameInput
          initial={data.name}
          placeholder="Process name"
          onCommit={(name) => data.onCommit(id, name)}
          onCancel={() => data.onCancel(id)}
        />
      ) : (
        <span className="process-name">{data.name}</span>
      )}
    </div>
  )
}

export function NameInput({
  initial,
  placeholder,
  onCommit,
  onCancel,
}: {
  initial: string
  placeholder: string
  onCommit: (name: string) => void
  onCancel: () => void
}) {
  const [value, setValue] = useState(initial)
  const ref = useRef<HTMLInputElement>(null)
  // Enter/Escape unmount the input, which fires blur; don't handle it twice.
  const done = useRef(false)

  useEffect(() => {
    ref.current?.focus()
    ref.current?.select()
  }, [])

  const finish = (commit: boolean) => {
    if (done.current) return
    done.current = true
    const name = value.trim()
    if (commit && name !== '') onCommit(name)
    else onCancel()
  }

  return (
    <input
      ref={ref}
      className="name-input nodrag"
      value={value}
      placeholder={placeholder}
      onChange={(e) => setValue(e.target.value)}
      onKeyDown={(e) => {
        if (e.key === 'Enter') finish(true)
        if (e.key === 'Escape') finish(false)
      }}
      onBlur={() => finish(true)}
    />
  )
}
