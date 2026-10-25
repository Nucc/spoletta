import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Handle, Position, useNodeConnections, type Node, type NodeProps } from '@xyflow/react'
import { useBoardActions } from './actions'
import type { ProcessKind, ProcessSettings, RunInput, ThinkingLevel } from './api'
import { AgentIcon, ConnectorIcon, FlowIcon, GearIcon, HumanIcon, PlusIcon, RerunIcon, ScriptIcon, StopIcon, SubflowIcon, TrashIcon, ZoomInIcon } from './icons'
import { stoppable } from './Runs'
import WidthResizer from './WidthResizer'

export type ProcessNodeData = {
  name: string
  kind: ProcessKind
  // Edited in the side panel; only the model shows on the node.
  prompt: string
  script: string
  model: string
  thinking: ThinkingLevel | ''
  // For flow processes: each time it runs is a run of its own.
  recording: boolean
  settings: ProcessSettings
  editing: boolean
  // Set while a flow run is in progress or after it has passed this process.
  status?: RunStatus
  // What the last run of this process printed, or why it failed.
  output?: string
  runError?: string
  // While a human process waits for a decision, or a process asks whether it
  // goes on round a loop: the inputs under review.
  review?: RunInput[]
  // Whether the run shown can be re-run from this process: it ended, and
  // the process ran in it rather than being copied from an earlier run.
  rerunnable?: boolean
}

export type RunStatus = 'active' | 'waiting' | 'done' | 'error' | 'duplicate' | 'skipped'

export type ProcessNodeType = Node<ProcessNodeData, 'process'>

export const KIND_ICONS: Record<ProcessKind, () => ReactNode> = {
  agent: AgentIcon,
  script: ScriptIcon,
  human: HumanIcon,
  flow: FlowIcon,
  subflow: SubflowIcon,
  connector: ConnectorIcon,
}

const KIND_LABELS: Record<ProcessKind, string> = {
  agent: 'Agent',
  script: 'Script',
  human: 'Human',
  flow: 'Flow',
  subflow: 'Procedure',
  connector: 'Connector',
}

const STATUS_LABELS: Record<RunStatus, string> = {
  active: 'Running',
  waiting: 'Waiting for a person',
  done: 'Done',
  error: 'Failed',
  duplicate: 'Already processed',
  skipped: 'Skipped',
}

// What the card says under the name: the kind, and an agent's model.
function subtitle(data: ProcessNodeData): string {
  if (data.kind === 'flow') return data.recording ? 'Flow · recorded' : 'Flow'
  if (data.kind === 'connector') {
    const op = data.settings.connector?.operation
    return op ? `Connector · ${op.replaceAll('_', ' ')}` : 'Connector'
  }
  if (data.kind !== 'agent') return KIND_LABELS[data.kind]
  return `Agent · ${data.model ? data.model.slice(data.model.indexOf('/') + 1) : 'default model'}`
}

// OPM process: a rounded card, its corners as round as the card is tall, with
// an icon for who carries it out. Inputs arrive on top, outputs leave below.
export default function ProcessNode({ id, data, selected, width }: NodeProps<ProcessNodeType>) {
  const actions = useBoardActions()
  const human = data.kind === 'human'
  const draft = id.startsWith('draft-')
  const leaf = useNodeConnections({ handleType: 'source' }).length === 0
  const KindIcon = KIND_ICONS[data.kind]

  return (
    <div className={`process-node ${data.kind}${width === undefined ? ' auto-width' : ''}${selected ? ' selected' : ''}${data.status ? ` ${data.status}` : ''}`}>
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
            <Subtitle text={subtitle(data)} status={data.status && STATUS_LABELS[data.status]} />
          </>
        )}
      </div>
      {!draft && (
        <span className="node-buttons nodrag">
          {(data.kind === 'flow' || (data.kind === 'subflow' && data.settings.flow !== undefined)) && (
            <button
              title={data.kind === 'flow' ? 'Zoom in' : 'Open the flow it runs'}
              aria-label="Zoom in"
              onClick={() => actions.zoomIn(id)}
            >
              <ZoomInIcon />
            </button>
          )}
          <button title="Settings" aria-label="Settings" onClick={() => actions.openPanel(id)}>
            <GearIcon />
          </button>
          {/* Stop and Re-run take Delete's place, which a run's board doesn't allow, so the card keeps its width. */}
          {data.status && stoppable({ kind: data.kind, status: data.status }) ? (
            <button title="Stop" aria-label="Stop" onClick={() => actions.stopProcess(id)}>
              <StopIcon />
            </button>
          ) : actions.runShown ? (
            <button
              title="Re-run from here"
              aria-label="Re-run from here"
              disabled={!data.rerunnable}
              onClick={() => actions.rerunProcess(id)}
            >
              <RerunIcon />
            </button>
          ) : (
            <button
              title="Delete"
              aria-label="Delete"
              disabled={!actions.editable}
              onClick={() => actions.remove(id)}
            >
              <TrashIcon />
            </button>
          )}
        </span>
      )}
      {leaf && !draft && actions.editable && (
        <AppendButton id={id} title={human ? 'Add an object the person fills in' : 'Add the object this process generates'} />
      )}
    </div>
  )
}

// The line under a card's name. A run's status shows in place of the usual
// text, but the card keeps the width the usual text gives it: a card that
// widened or narrowed with each status would move its links, which sit at its
// middle, out of line with the cards above and below.
export function Subtitle({ text, status }: { text: string; status?: string }) {
  if (!status) return <span className="node-subtitle">{text}</span>
  return (
    <span className="node-subtitle replaced">
      <span aria-hidden="true">{text}</span>
      <span>{status}</span>
    </span>
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
