import { useEffect, useRef, useState } from 'react'
import type { Node, NodeProps } from '@xyflow/react'

export type ProcessNodeData = {
  name: string
  // Edited in the side panel, never rendered on the node.
  prompt: string
  editing: boolean
  onCommit: (id: string, name: string) => void
  onCancel: (id: string) => void
}

export type ProcessNodeType = Node<ProcessNodeData, 'process'>

export default function ProcessNode({ id, data, selected }: NodeProps<ProcessNodeType>) {
  return (
    <div className={`process-node${selected ? ' selected' : ''}`}>
      {data.editing ? (
        <NameInput
          initial={data.name}
          onCommit={(name) => data.onCommit(id, name)}
          onCancel={() => data.onCancel(id)}
        />
      ) : (
        <span className="process-name">{data.name}</span>
      )}
    </div>
  )
}

function NameInput({
  initial,
  onCommit,
  onCancel,
}: {
  initial: string
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
      className="process-name-input nodrag"
      value={value}
      placeholder="Process name"
      onChange={(e) => setValue(e.target.value)}
      onKeyDown={(e) => {
        if (e.key === 'Enter') finish(true)
        if (e.key === 'Escape') finish(false)
      }}
      onBlur={() => finish(true)}
    />
  )
}
