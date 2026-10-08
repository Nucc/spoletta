import type { DragEvent, ReactNode } from 'react'
import type { ProcessKind } from './api'
import { SentryLogo } from './icons'

// Something the sidebar adds to the board: a node of an OPM element, or a
// process of a kind that talks to a service.
export type Block = {
  id: string
  label: string
  title: string
  type: 'process' | 'object'
  // The kind a new process gets.
  kind?: ProcessKind
}

// Each new OPM element is one more entry.
export const BUILDING_BLOCKS: Block[] = [
  { id: 'process', label: 'Process', title: 'Something that happens, such as an agent at work', type: 'process', kind: 'agent' },
  { id: 'object', label: 'Object', title: 'Something processes generate and take', type: 'object' },
]

// Each new service is one more entry, once the server runs processes of its kind.
export const CONNECTORS: (Block & { logo: () => ReactNode })[] = [
  { id: 'sentry', label: 'Sentry', title: 'Fetches issues from Sentry as a list', type: 'process', kind: 'sentry', logo: () => <SentryLogo /> },
]

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
            <span className={`palette-shape ${b.type}`} aria-hidden="true" />
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
