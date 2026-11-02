import type { DragEvent, ReactNode } from 'react'
import type { Flow, MCPServer, ProcessKind, ProcessSettings } from './api'
import { AgentIcon, HumanIcon, ObjectIcon, PlusIcon, SubflowIcon } from './icons'
import { ServiceLogo } from './logos'

// Something the sidebar adds to the board: a node of an OPM element, or a
// process of a kind that talks to a service.
export type Block = {
  id: string
  label: string
  title: string
  type: 'process' | 'object' | 'note'
  // The kind a new process gets.
  kind?: ProcessKind
  // A new object is a person; see FlowObject.
  human?: boolean
  // The name and settings a new process starts with, such as the flow a
  // subflow process runs.
  name?: string
  settings?: ProcessSettings
  // The struct a new object is a copy of, named after it.
  struct?: number
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
  { id: 'note', label: 'Note', title: 'Text on the board saying why it is built so, or what to look at; no run reads it', type: 'note' },
]

// A tile of the connectors, with its service's logo.
export type ConnectorBlock = Block & { logo: () => ReactNode }

// What the tile of each built-in connector says, by its name in the
// directory. Each new connector is one more entry.
const CONNECTOR_TILES: Record<string, { label: string; title: string }> = {
  github: { label: 'GitHub', title: 'Carries out an operation on GitHub, such as listing issues, commenting or opening a pull request' },
  slack: { label: 'Slack', title: 'Carries out an operation on Slack, such as posting a message or reading a channel' },
  telegram: { label: 'Telegram', title: "Carries out an operation on Telegram as a bot, such as sending a message or reading the bot's" },
  google: { label: 'Google', title: 'Carries out an operation on Google Sheets or Docs, such as reading rows or appending them' },
  homeassistant: {
    label: 'Home Assistant',
    title: 'Carries out an operation on Home Assistant, such as reading a sensor or turning a light on',
  },
  http: { label: 'HTTP API', title: 'Calls an HTTP API installed as a connector, such as getting what a path holds' },
}

// The installed connectors, each a process already set to carry out an
// operation of it. One installed several times, such as the HTTP API for
// each API, goes by the name each was installed as.
export function connectorBlocks(servers: MCPServer[]): ConnectorBlock[] {
  const installed = servers.filter((s) => s.connector)
  return installed.map((s) => {
    const tile = CONNECTOR_TILES[s.connector!]
    const several = s.connector === 'http' || installed.filter((x) => x.connector === s.connector).length > 1
    const label = tile && !several ? tile.label : s.name
    return {
      id: `connector-${s.id}`,
      label,
      title: tile?.title ?? `Carries out an operation of ${s.name}`,
      type: 'process',
      kind: 'connector',
      name: label,
      settings: { connector: { server: s.id, operation: '' } },
      logo: () => <ServiceLogo name={s.connector!} title={label} size={22} />,
    }
  })
}

// A module, added as a process that runs it.
export const flowBlock = (f: Flow): Block => ({
  id: `flow-${f.id}`,
  label: f.name,
  title: `Runs the procedure ${f.name}, a flow reused here, with the objects of the same names`,
  type: 'process',
  kind: 'subflow',
  name: f.name,
  settings: { flow: f.id },
})

// A struct, added as a copy of its object.
export const structBlock = (f: Flow): Block => ({
  id: `struct-${f.id}`,
  label: f.name,
  title: `A copy of the struct ${f.name}, an object reused here, which changes only where it is made`,
  type: 'object',
  struct: f.id,
})

// What a block dragged onto the board carries: its id.
export const BLOCK_MIME = 'application/x-flow-block'

export const findBlock = (id: string): Block | undefined => BUILDING_BLOCKS.find((b) => b.id === id)

// Blocks are dragged onto the board, or clicked to add one in the middle of
// it. Each shows the badge its node has on the board, two to a row, or four
// once the sidebar is wide enough, so none is left on a row of its own.
export function BlockPalette({ blocks, onAdd }: { blocks: Block[]; onAdd: (block: Block) => void }) {
  return (
    <ul className="palette blocks">
      {blocks.map((b) => (
        <li key={b.id}>
          <PaletteItem block={b} onAdd={onAdd}>
            <BlockBadge block={b} />
          </PaletteItem>
        </li>
      ))}
    </ul>
  )
}

// The badge a block's node has on the board: a new process is an agent's, a
// person an object's with a person in it, and a note a note's folded corner.
function BlockBadge({ block }: { block: Block }) {
  if (block.type === 'note') return <span className="palette-badge note" aria-hidden="true" />
  if (block.type === 'process') {
    return (
      <span className="palette-badge process" aria-hidden="true">
        <AgentIcon />
      </span>
    )
  }
  return (
    <span className="palette-badge object" aria-hidden="true">
      {block.human ? <HumanIcon /> : <ObjectIcon />}
    </span>
  )
}

// The connectors installed, a row each; installing more
// is for admins, in the settings.
export function ConnectorPalette({
  blocks,
  onAdd,
}: {
  // Null while the installed connectors load.
  blocks: ConnectorBlock[] | null
  onAdd: (block: Block) => void
}) {
  if (blocks === null) return null
  if (blocks.length === 0) {
    return (
      <p className="panel-note">
        No connectors installed. An admin installs them, such as GitHub, in{' '}
        <a href="#/settings/connectors">Settings, under Connectors</a>.
      </p>
    )
  }
  return (
    <ul className="palette rows">
      {blocks.map((c) => (
        <li key={c.id}>
          <PaletteItem block={c} onAdd={onAdd} row>
            {c.logo()}
          </PaletteItem>
        </li>
      ))}
    </ul>
  )
}

// The modules, to reuse in this flow, a row each: people name them, and
// as many as there are, so each name gets the sidebar's width.
export function FlowPalette({ blocks, onAdd }: { blocks: Block[]; onAdd: (block: Block) => void }) {
  if (blocks.length === 0) return <p className="panel-note">No procedures yet. Make one on the main page, or mark a flow as one in its settings.</p>
  return (
    <ul className="palette rows">
      {blocks.map((b) => (
        <li key={b.id}>
          <PaletteItem block={b} onAdd={onAdd} row>
            <span className="palette-badge subflow" aria-hidden="true">
              <SubflowIcon />
            </span>
          </PaletteItem>
        </li>
      ))}
    </ul>
  )
}

// The structs, whose objects to use in this flow, a row each.
export function StructPalette({ blocks, onAdd }: { blocks: Block[]; onAdd: (block: Block) => void }) {
  if (blocks.length === 0) return <p className="panel-note">No structs yet. Make one on the main page.</p>
  return (
    <ul className="palette rows">
      {blocks.map((b) => (
        <li key={b.id}>
          <PaletteItem block={b} onAdd={onAdd} row>
            <span className="palette-badge object" aria-hidden="true">
              <ObjectIcon />
            </span>
          </PaletteItem>
        </li>
      ))}
    </ul>
  )
}

// A block to drag onto the board or click: a tile, or a row with a + that
// shows on hover saying a click adds it.
function PaletteItem({
  block,
  onAdd,
  row = false,
  children,
}: {
  block: Block
  onAdd: (block: Block) => void
  row?: boolean
  children: ReactNode
}) {
  const onDragStart = (e: DragEvent<HTMLButtonElement>) => {
    e.dataTransfer.setData(BLOCK_MIME, block.id)
    e.dataTransfer.effectAllowed = 'copy'
  }
  return (
    <button
      className={row ? 'palette-row' : 'palette-tile'}
      draggable
      onDragStart={onDragStart}
      onClick={() => onAdd(block)}
      title={`${block.title}. Drag it onto the board, or click to add one in the middle.`}
    >
      {children}
      <span className="palette-label">{block.label}</span>
      {row && (
        <span className="palette-row-add" aria-hidden="true">
          <PlusIcon />
        </span>
      )}
    </button>
  )
}
