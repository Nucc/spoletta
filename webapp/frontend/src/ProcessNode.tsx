import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Handle, Position, useNodeConnections, type Node, type NodeProps } from '@xyflow/react'
import { useBoardActions } from './actions'
import type { ProcessKind, RunInput, ThinkingLevel } from './api'
import { AgentIcon, GearIcon, HumanIcon, PlusIcon, ScriptIcon, TrashIcon } from './icons'
import WidthResizer from './WidthResizer'

export type ProcessNodeData = {
  name: string
  kind: ProcessKind
  // Edited in the side panel; only the model shows on the node.
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
}

export type RunStatus = 'active' | 'waiting' | 'done' | 'error' | 'duplicate'

export type ProcessNodeType = Node<ProcessNodeData, 'process'>

export const KIND_ICONS: Record<ProcessKind, () => ReactNode> = {
  agent: AgentIcon,
  script: ScriptIcon,
  human: HumanIcon,
}

const KIND_LABELS: Record<ProcessKind, string> = { agent: 'Agent', script: 'Script', human: 'Human review' }

const STATUS_LABELS: Record<RunStatus, string> = {
  active: 'Running',
  waiting: 'Waiting for review',
  done: 'Done',
  error: 'Failed',
  duplicate: 'Already processed',
}

// What the card says under the name: the kind, and an agent's model.
function subtitle(data: ProcessNodeData): string {
  if (data.status) return STATUS_LABELS[data.status]
  if (data.kind !== 'agent') return KIND_LABELS[data.kind]
  return `Agent · ${data.model ? data.model.slice(data.model.indexOf('/') + 1) : 'default model'}`
}

// OPM process: a rounded card, its corners as round as the card is tall, with
// an icon for who carries it out. Inputs arrive on top, outputs leave below.
export default function ProcessNode({ id, data, selected }: NodeProps<ProcessNodeType>) {
  const actions = useBoardActions()
  const human = data.kind === 'human'
  const draft = id.startsWith('draft-')
  const leaf = useNodeConnections({ handleType: 'source' }).length === 0
  const KindIcon = KIND_ICONS[data.kind]

  return (
    <div className={`process-node ${data.kind}${selected ? ' selected' : ''}${data.status ? ` ${data.status}` : ''}`}>
      <WidthResizer id={id} minWidth={160} />
      <Handle type="target" position={Position.Top} />
      <Handle type="source" position={Position.Bottom} />
      <span className="node-icon">
        <KindIcon />
      </span>
      <div className="node-text">
        {data.editing ? (
          <NameInput
            initial={data.name}
            placeholder="Process name"
            onCommit={(name) => actions.commitName(id, name)}
            onCancel={() => actions.cancelName(id)}
          />
        ) : (
          <>
            <span className="node-name">{data.name}</span>
            <span className="node-subtitle">{subtitle(data)}</span>
          </>
        )}
      </div>
      {!draft && (
        <span className="node-buttons nodrag">
          <button title="Settings" aria-label="Settings" onClick={() => actions.openPanel(id)}>
            <GearIcon />
          </button>
          <button
            title="Delete"
            aria-label="Delete"
            disabled={!actions.editable}
            onClick={() => actions.remove(id)}
          >
            <TrashIcon />
          </button>
        </span>
      )}
      {/* A human process only links to states, so nothing is appended below it. */}
      {leaf && !human && !draft && actions.editable && <AppendButton id={id} title="Add the object this process generates" />}
    </div>
  )
}

export function AppendButton({ id, title }: { id: string; title: string }) {
  const actions = useBoardActions()
  return (
    <button className="node-append nodrag" title={title} aria-label={title} onClick={() => actions.append(id)}>
      <PlusIcon />
    </button>
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

  // React Flow hides a new node until it has measured it, and hidden inputs
  // can't take focus, so keep trying for a few frames.
  useEffect(() => {
    let tries = 0
    let frame = 0
    const focus = () => {
      const input = ref.current
      if (!input) return
      input.focus()
      input.select()
      if (document.activeElement !== input && ++tries < 10) frame = requestAnimationFrame(focus)
    }
    focus()
    return () => cancelAnimationFrame(frame)
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
