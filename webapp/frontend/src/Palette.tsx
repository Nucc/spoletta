import type { DragEvent, ReactNode } from 'react'
import type { Flow, ProcessKind, ProcessSettings } from './api'
import { SentryLogo, SubflowIcon } from './icons'

// Something the sidebar adds to the board: a node of an OPM element, or a
// process of a kind that talks to a service.
export type Block = {
  id: string
  label: string
  title: string
  type: 'process' | 'object'
  // The kind a new process gets.
  kind?: ProcessKind
  // A new object is a person; see FlowObject.
  human?: boolean
  // The name and settings a new process starts with, such as the flow a
  // subflow process runs.
  name?: string
  settings?: ProcessSettings
}

// Each new OPM element is one more entry.
export const BUILDING_BLOCKS: Block[] = [
  { id: 'process', label: 'Process', title: 'Something that happens, such as an agent at work', type: 'process', kind: 'agent' },
  { id: 'object', label: 'Object', title: 'Something processes generate and take', type: 'object' },
  {
    id: 'person',
    label: 'Person',
    title: 'Someone who carries out a human process, such as filling in what a run starts from',
    type: 'object',
    human: true,
  },
]

// Each new service is one more entry, once the server runs processes of its kind.
export const CONNECTORS: (Block & { logo: () => ReactNode })[] = [
  { id: 'sentry', label: 'Sentry', title: 'Fetches issues from Sentry as a list', type: 'process', kind: 'sentry', logo: () => <SentryLogo /> },
]

// Another flow, added as a process that runs it.
export const flowBlock = (f: Flow): Block => ({
  id: `flow-${f.id}`,
  label: f.name,
  title: `Runs the flow ${f.name}, with the objects of the same names`,
  type: 'process',
  kind: 'subflow',
  name: f.name,
  settings: { flow: f.id },
})

// What a block dragged onto the board carries: its id.
export const BLOCK_MIME = 'application/x-flow-block'

export const findBlock = (id: string): Block | undefined => [...BUILDING_BLOCKS, ...CONNECTORS].find((b) => b.id === id)

// Blocks are dragged onto the board, or clicked to add one in the middle of it.
export function BlockPalette({
  blocks,
  disabled,
  onAdd,
}: {
  blocks: Block[]
  disabled: boolean
  onAdd: (block: Block) => void
}) {
  return (
    <ul className="palette">
      {blocks.map((b) => (
        <li key={b.id}>
          <PaletteTile block={b} disabled={disabled} onAdd={onAdd}>
            <span className={`palette-shape ${b.type}${b.human ? ' human' : ''}`} aria-hidden="true" />
          </PaletteTile>
        </li>
      ))}
    </ul>
  )
}

export function ConnectorPalette({
  disabled,
  onAdd,
}: {
  disabled: boolean
  onAdd: (block: Block) => void
}) {
  return (
    <ul className="palette connectors">
      {CONNECTORS.map((c) => (
        <li key={c.id}>
          <PaletteTile block={c} disabled={disabled} onAdd={onAdd}>
            <span className={`palette-logo ${c.id}`}>{c.logo()}</span>
          </PaletteTile>
        </li>
      ))}
    </ul>
  )
}

// The other flows, to reuse in this one.
export function FlowPalette({
  blocks,
  disabled,
  onAdd,
}: {
  blocks: Block[]
  disabled: boolean
  onAdd: (block: Block) => void
}) {
  if (blocks.length === 0) return <p className="panel-note">No other flows yet.</p>
  return (
    <ul className="palette flows">
      {blocks.map((b) => (
        <li key={b.id}>
          <PaletteTile block={b} disabled={disabled} onAdd={onAdd}>
            <span className="palette-logo subflow">
              <SubflowIcon />
            </span>
          </PaletteTile>
        </li>
      ))}
    </ul>
  )
}

function PaletteTile({
  block,
  disabled,
  onAdd,
  children,
}: {
  block: Block
  disabled: boolean
  onAdd: (block: Block) => void
  children: ReactNode
}) {
  const onDragStart = (e: DragEvent<HTMLButtonElement>) => {
    e.dataTransfer.setData(BLOCK_MIME, block.id)
    e.dataTransfer.effectAllowed = 'copy'
  }
  return (
    <button
      className={`palette-tile ${block.type}`}
      draggable={!disabled}
      disabled={disabled}
      onDragStart={onDragStart}
      onClick={() => onAdd(block)}
      title={`${block.title}. Drag it onto the board, or click to add one in the middle.`}
    >
      {children}
      <span className="palette-label">{block.label}</span>
    </button>
  )
}
