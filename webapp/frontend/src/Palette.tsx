import type { DragEvent, ReactNode } from 'react'
import type { Flow, MCPServer, ProcessKind, ProcessSettings } from './api'
import { ObjectIcon, SubflowIcon } from './icons'
import { ServiceLogo } from './logos'

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
      logo: () => <ServiceLogo name={s.connector!} title={label} size={32} />,
    }
  })
}

// A module, added as a process that runs it.
export const flowBlock = (f: Flow): Block => ({
  id: `flow-${f.id}`,
  label: f.name,
  title: `Runs the procedure ${f.name}, with the objects of the same names`,
  type: 'process',
  kind: 'subflow',
  name: f.name,
  settings: { flow: f.id },
})

// A struct, added as a copy of its object.
export const structBlock = (f: Flow): Block => ({
  id: `struct-${f.id}`,
  label: f.name,
  title: `A copy of the struct ${f.name}, which changes only there`,
  type: 'object',
  struct: f.id,
})

// What a block dragged onto the board carries: its id.
export const BLOCK_MIME = 'application/x-flow-block'

export const findBlock = (id: string): Block | undefined => BUILDING_BLOCKS.find((b) => b.id === id)

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

// The connectors installed; installing more
// is for admins, in the settings.
export function ConnectorPalette({
  blocks,
  disabled,
  onAdd,
}: {
  // Null while the installed connectors load.
  blocks: ConnectorBlock[] | null
  disabled: boolean
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
    <ul className="palette connectors">
      {blocks.map((c) => (
        <li key={c.id}>
          <PaletteTile block={c} disabled={disabled} onAdd={onAdd}>
            {c.logo()}
          </PaletteTile>
        </li>
      ))}
    </ul>
  )
}

// The modules, to reuse in this flow.
export function FlowPalette({
  blocks,
  disabled,
  onAdd,
}: {
  blocks: Block[]
  disabled: boolean
  onAdd: (block: Block) => void
}) {
  if (blocks.length === 0) return <p className="panel-note">No procedures yet. Make one on the main page, or mark a flow as one in its settings.</p>
  return (
    <ul className="palette procedures">
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

// The structs, whose objects to use in this flow.
export function StructPalette({
  blocks,
  disabled,
  onAdd,
}: {
  blocks: Block[]
  disabled: boolean
  onAdd: (block: Block) => void
}) {
  if (blocks.length === 0) return <p className="panel-note">No structs yet. Make one on the main page.</p>
  return (
    <ul className="palette structs">
      {blocks.map((b) => (
        <li key={b.id}>
          <PaletteTile block={b} disabled={disabled} onAdd={onAdd}>
            <span className="palette-logo struct">
              <ObjectIcon />
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
