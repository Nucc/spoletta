import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties, DragEvent, MouseEvent } from 'react'
import { flushSync } from 'react-dom'
import {
  Background,
  BackgroundVariant,
  Controls,
  MarkerType,
  ReactFlow,
  useEdgesState,
  useNodesState,
  useReactFlow,
  type Edge,
  type IsValidConnection,
  type NodeMouseHandler,
  type OnConnect,
  type OnConnectEnd,
  type OnConnectStart,
  type OnEdgesDelete,
  type OnNodeDrag,
  type XYPosition,
} from '@xyflow/react'
import { BoardActionsContext, StructFlowsContext, type BoardActions } from './actions'
import { ColumnResizer, useColumnWidth } from './ColumnResizer'
import ContextMenu, { type MenuItem } from './ContextMenu'
import {
  CodeIcon,
  CopyIcon,
  FlowBuilderIcon,
  FlowIcon,
  GearIcon,
  HistoryIcon,
  LayoutIcon,
  PasteIcon,
  PencilIcon,
  PlayIcon,
  PlugIcon,
  TableIcon,
  PlusIcon,
  SaveIcon,
  TrashIcon,
  ZoomInIcon,
} from './icons'
import { RANK_GAP, tidy } from './layout'
import LinkEdge, { type LinkEdgeData, type LinkEdgeType } from './LinkEdge'
import ObjectNode, {
  stateSourceHandle,
  stateTargetHandle,
  type ObjectNodeData,
  type ObjectNodeType,
} from './ObjectNode'
import ProcessNode, { type ProcessNodeData, type ProcessNodeType } from './ProcessNode'
import ObjectPanel from './ObjectPanel'
import ProcessPanel, { type EntryObject, type QueryLink, type Review } from './ProcessPanel'
import ReviewDialog from './ReviewDialog'
import StartNode, { type StartNodeType } from './StartNode'
import EndNode, { type EndNodeType } from './EndNode'
import StructureEdge, { type StructureKind } from './StructureEdge'
import { fieldsOf, onBoard, ownedBy, ownersOf, possibleGenerals, shapeOf, specializationsOf } from './structure'
import {
  BLOCK_MIME,
  BlockPalette,
  BUILDING_BLOCKS,
  connectorBlocks,
  ConnectorPalette,
  findBlock,
  flowBlock,
  FlowPalette,
  structBlock,
  SENTRY_BLOCK,
  StructPalette,
  type Block,
} from './Palette'
import { decisionText, rerunnableFrom, RunList, RunPanel, type RunFocus } from './Runs'
import RerunDialog from './RerunDialog'
import OPLPanel from './OPLPanel'
import CodePanel from './CodePanel'
import Assistant from './Assistant'
import { Sidebar, SidebarGroup } from './Sidebar'
import { SharedRecords } from './Records'
import { VersionPanel } from './Versions'
import type { Quantity } from './quantity'
import { MCPSettingsPanel } from './MCPSettings'
import { configures, useMe } from './Account'
import { OwnerPicker } from './Owner'
import { onCopiedElsewhere, readCopied, writeCopied } from './clipboard'
import { FlowSettingsPanel } from './FlowSettings'
import {
  listTeams,
  setFlowOwner,
  type Team,
  createAttribute,
  createConnection,
  createLink,
  copyStruct,
  createObject,
  createProcess,
  createState,
  deleteAttribute,
  deleteConnection,
  deleteLink,
  deleteObject,
  deleteProcess,
  decideStep,
  deleteState,
  getRun,
  getEnd,
  listFlows,
  getStart,
  getVersionBoard,
  listConnections,
  listLinks,
  listModels,
  listMCPServers,
  sentryConfigured,
  listBoards,
  listObjects,
  listProcesses,
  listRuns,
  listVersions,
  changesSettled,
  onChanged,
  pasteCopied,
  recordHistory,
  redoHistory,
  renameState,
  restoreVersion,
  undoHistory,
  saveVersion,
  setLinkAt,
  setLinkRoute,
  setLinkCondition,
  setLinkQuery,
  setStateRule,
  setLinkRole,
  setLinkStates,
  startRun,
  stopRun,
  stopStep,
  updateAttribute,
  updateEnd,
  updateObject,
  updateProcess,
  updateStart,
  wrapProcesses,
  type BoardContents,
  type BoardId,
  type Connection,
  type Copied,
  type FlowObject,
  defaultBranchPrefix,
  type Flow,
  type FlowRun,
  type Versions,
  type MCPServer,
  type FlowRunDetail,
  type Link,
  type LinkQuery,
  type LinkRole,
  type Models,
  type ObjectAttribute,
  type ObjectState,
  type Process,
  type Relation,
  type Entries,
  type ProcessKind,
  type Start,
} from './api'

type FlowNode = ProcessNodeType | ObjectNodeType | StartNodeType | EndNodeType
type FlowEdge = Edge<LinkEdgeData & { kind?: StructureKind }>

const nodeTypes = { process: ProcessNode, object: ObjectNode, start: StartNode, end: EndNode }
const edgeTypes = { link: LinkEdge, structure: StructureEdge }

// The column on the right of the board that holds the open panel.
const PANEL_COLUMN = { storageKey: 'panel-width', min: 320, max: 1600, initial: 420, edge: 'left' } as const

// Default sizes of new nodes; resizing changes only the width. Used to place
// new nodes before they are measured. A never resized node widens to fit its
// name, up to the max-width of .auto-width in index.css.
const NODE_SIZE = {
  process: { width: 240, height: 64 },
  object: { width: 200, height: 56 },
}

// Names of nodes added with a + button, typed over straight away.
const NEW_PROCESS = 'New process'
const NEW_OBJECT = 'New object'

// How far a duplicate lands from its original.
const DUPLICATE_OFFSET = 40
const DRAFT_PREFIX = 'draft-'
const START_ID = 'start'
const START_EDGE_ID = 'start-edge'
// A board ends here.
const END_ID = 'end'
// How far below the lowest node a top board's end sits until it is moved.
const END_GAP = 200
// The dashed lines from the start to the objects the flow process takes, and
// from the ones it generates, or the top board leaves, to the end.
const BOUNDARY_PREFIX = 'boundary-'
// Where the objects outside sit, relative to the start and end.
const BOUNDARY_GAP = { x: 24, y: 90 }
// Direct process-to-process connections predate objects. New ones can't be
// drawn, but existing ones still show, run and can be deleted.
const CONNECTION_PREFIX = 'conn-'
const LINK_PREFIX = 'link-'
// The lines from the objects a flow leaves into its top board's end.
const END_EDGE_PREFIX = 'ends-'
// Objects and processes have separate id sequences, so object node ids are prefixed.
const OBJECT_PREFIX = 'obj-'
// The structural links on an unfolded object's board.
const STRUCTURE_PREFIX = 'struct-'
// Where an unfolded object sits on its own board, and how far from it the
// object it specializes, above, and those specializing it, beside it.
const UNFOLDED_AT = { x: 0, y: 0 }
const UNFOLD_GAP = { x: 60, y: 160 }

const markerEnd = { type: MarkerType.ArrowClosed }

// How often an open run that is still going is refreshed, and the run list
// while any run in it is.
const RUN_REFRESH_MS = 1500
const RUN_LIST_REFRESH_MS = 5000

// How long a board just shown gets to measure its nodes before it is fitted into view.
const BOARD_FIT_DELAY_MS = 100
// How long after a change the board waits for more before keeping it as a
// step of its history, so an action sending several changes is one step.
const HISTORY_DELAY_MS = 300

// Fitting the board into view; a board of a few nodes is not blown up past
// its natural size.
const FIT_VIEW = { duration: 300, maxZoom: 1 }

// The state a handle of an object node belongs to, if any.
const stateOfHandle = (handle: string | null | undefined): number | null => {
  const m = handle?.match(/^state-(?:in|out)-(\d+)$/)
  return m ? Number(m[1]) : null
}

const isDraft = (id: string) => id.startsWith(DRAFT_PREFIX)
const isObject = (id: string) => id.startsWith(OBJECT_PREFIX)
const isProcess = (id: string) => id !== START_ID && id !== END_ID && !isObject(id) && !isDraft(id)

const objectNodeId = (id: number) => `${OBJECT_PREFIX}${id}`
const objectIdOf = (nodeId: string) => Number(nodeId.slice(OBJECT_PREFIX.length))

// A step can be inserted into the start edge and into links, except a human
// process's links to the states it decides on. Direct connections have no
// object to split.
const startEdge = (target: number | string, route: number[] | null = null): FlowEdge => ({
  id: START_EDGE_ID,
  type: 'link',
  source: START_ID,
  target: String(target),
  markerEnd,
  data: { insertable: true, routable: true, route },
})

// A failure connection is drawn dashed, saying it runs on failure.
const connectionEdge = (c: Connection): FlowEdge => ({
  id: `${CONNECTION_PREFIX}${c.id}`,
  type: 'link',
  source: String(c.source),
  target: String(c.target),
  className: c.kind === 'failure' ? 'failure' : undefined,
  markerEnd,
  data: { failure: c.kind === 'failure' },
})

// An output link points from the process to the object it generates, an
// input link from the object to the process that takes it. A link naming a
// state ends or starts at that state inside the object. A human's output link
// goes to the object and lists the states it decides between.
const linkEdge = (l: Link, human: boolean): LinkEdgeType => {
  const process = String(l.process)
  const object = objectNodeId(l.object)
  const output = l.direction === 'output'
  return {
    id: `${LINK_PREFIX}${l.id}`,
    type: 'link',
    source: output ? process : object,
    sourceHandle: !output && l.state !== null ? stateSourceHandle(l.state) : null,
    target: output ? object : process,
    targetHandle: output && l.state !== null ? stateTargetHandle(l.state) : null,
    className: output && human ? 'decision' : undefined,
    markerEnd,
    data: { link: l, insertable: !(output && human), decision: output && human, routable: true, route: l.route },
  }
}

const fetchBoard = (flow: number): Promise<BoardContents> =>
  Promise.all([
    listProcesses(flow),
    listObjects(flow),
    getStart(flow),
    getEnd(flow),
    listConnections(flow),
    listLinks(flow),
    listBoards(flow),
  ]).then(([processes, objects, start, end, connections, links, boards]) => ({
    processes,
    objects,
    start,
    end,
    connections,
    links,
    boards,
  }))

// From the whole to what makes it up, or from the general to what specializes it.
const structureEdge = (source: number, target: number, kind: StructureKind): FlowEdge => ({
  id: `${STRUCTURE_PREFIX}${source}-${target}`,
  type: 'structure',
  source: objectNodeId(source),
  target: objectNodeId(target),
  data: { kind },
  selectable: false,
  deletable: false,
  focusable: false,
})

const boundaryEdge = (source: string, target: string): FlowEdge => ({
  id: `${BOUNDARY_PREFIX}${source}-${target}`,
  source,
  target,
  className: 'boundary-edge',
  selectable: false,
  deletable: false,
  focusable: false,
})

const toStartNode = (s: Start): StartNodeType => ({
  id: START_ID,
  type: 'start',
  position: { x: s.x, y: s.y },
  deletable: false,
  data: {},
})

// Only a top board's end takes links drawn to it.
const toEndNode = (e: { x: number; y: number }, connectable: boolean): EndNodeType => ({
  id: END_ID,
  type: 'end',
  position: { x: e.x, y: e.y },
  deletable: false,
  data: { connectable },
})

// Drawn like the lines into a flow process's end, but drawn and deleted by hand.
const endEdge = (object: number): FlowEdge => ({
  id: `${END_EDGE_PREFIX}${object}`,
  source: objectNodeId(object),
  target: END_ID,
  className: 'boundary-edge',
})

// A flow's board, its runs and its versions. Opening another flow mounts a
// new board, showing the run given, if any.
export default function Board({
  flow,
  initialRun,
  onUpdate,
  onReload,
}: {
  flow: Flow
  initialRun: number | null
  onUpdate: (patch: Partial<Pick<Flow, 'name' | 'module' | 'settings'>>) => Promise<void>
  // Reads the flow again, after the server changed it.
  onReload: () => Promise<void>
}) {
  const flowId = flow.id
  // A struct's board holds its one object alone: no start, no end, no
  // processes and no runs; see structs.go.
  const structBoard = flow.struct
  const [nodes, setNodes, onNodesChange] = useNodesState<FlowNode>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<FlowEdge>([])
  const [error, setError] = useState<string | null>(null)
  // Set while a run is being started.
  const [starting, setStarting] = useState(false)
  const [panelId, setPanelId] = useState<string | null>(null)
  // The process whose waiting review shows in the middle of the screen.
  const [reviewId, setReviewId] = useState<string | null>(null)
  const [models, setModels] = useState<Models | null>(null)
  const me = useMe()
  // The teams, to give the flow to one; see Owner.tsx.
  const [teams, setTeams] = useState<Team[]>([])
  const [mcpServers, setMCPServers] = useState<MCPServer[] | null>(null)
  // Whether Sentry is set up, offering Sentry processes.
  const [sentry, setSentry] = useState(false)
  const [mcpOpen, setMCPOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [oplOpen, setOPLOpen] = useState(false)
  const [codeOpen, setCodeOpen] = useState(false)
  // Every other flow, which this one can run; null while they load.
  const [otherFlows, setOtherFlows] = useState<Flow[] | null>(null)
  const [runs, setRuns] = useState<FlowRun[]>([])
  // The run open in the side panel, and its steps once loaded.
  const [runId, setRunId] = useState<number | null>(null)
  const [runDetail, setRunDetail] = useState<FlowRunDetail | null>(null)
  // Bumped to load the open run again straight away, such as after a decision.
  const [runReload, setRunReload] = useState(0)
  const [versions, setVersions] = useState<Versions>({ current: null, versions: [] })
  const [versionsOpen, setVersionsOpen] = useState(false)
  // Set while a save or a jump to another version is under way.
  const [versioning, setVersioning] = useState(false)
  // The board shown: the top board, or a flow process's. The ref has it for
  // loads started before it changed.
  const [boardId, setBoardId] = useState<BoardId>(null)
  const boardRef = useRef<BoardId>(null)
  boardRef.current = boardId
  // The object unfolded into a board of its own, if any, shown instead of
  // the board above; see structure.ts. The ref has it for loads started
  // before it changed.
  const [unfolded, setUnfolded] = useState<number | null>(null)
  const unfoldedRef = useRef<number | null>(null)
  unfoldedRef.current = unfolded
  const objectBoard = unfolded !== null
  // The whole flow the board shows part of, for the names of the boards above it.
  const [contents, setContents] = useState<BoardContents | null>(null)
  // Bumped each time a board is shown, so the open run shows on it again.
  const [shownSeq, setShownSeq] = useState(0)
  // The node whose context menu is open, and where.
  const [menu, setMenu] = useState<{ id: string; x: number; y: number } | null>(null)
  // Where the board's own context menu is open, if it is.
  const [paneMenu, setPaneMenu] = useState<{ x: number; y: number } | null>(null)
  // What was copied, from any board of any flow; see clipboard.ts.
  const [copied, setCopied] = useState<Copied | null>(readCopied)
  useEffect(() => onCopiedElsewhere(() => setCopied(readCopied())), [])
  // Where the pointer is over the canvas, on the screen, for pasting there.
  const pointer = useRef<XYPosition | null>(null)
  const { getNode, getNodes, screenToFlowPosition, fitView } = useReactFlow<FlowNode>()
  const wrapper = useRef<HTMLDivElement>(null)
  const draftSeq = useRef(0)
  // Loads the board again, for callbacks made before loadBoard is.
  const reload = useRef<() => Promise<void>>(async () => {})
  // Whether a node is a struct's object, for callbacks made before it is known.
  const isStructRootRef = useRef<(id: string) => boolean>(() => false)
  // The reviews whose process's panel was opened for them, so closing it keeps it closed.
  const shownReviews = useRef(new Set<number>())

  const report = useCallback((err: unknown) => {
    console.error(err)
    setError(err instanceof Error ? err.message : String(err))
  }, [])

  useEffect(() => {
    if (me?.user) listTeams().then(setTeams, report)
  }, [me, report])

  const updateData = useCallback(
    (id: string, data: Partial<ProcessNodeData>) =>
      setNodes((ns) =>
        ns.map((n) => (n.id === id && n.type === 'process' ? { ...n, data: { ...n.data, ...data } } : n)),
      ),
    [setNodes],
  )

  const updateObjectData = useCallback(
    (id: string, data: Partial<ObjectNodeData>) =>
      setNodes((ns) =>
        ns.map((n) => (n.id === id && n.type === 'object' ? { ...n, data: { ...n.data, ...data } } : n)),
      ),
    [setNodes],
  )

  const setEditing = useCallback(
    (id: string, editing: boolean) =>
      setNodes((ns) =>
        ns.map((n) => (n.id === id && n.type !== 'start' && n.type !== 'end' ? ({ ...n, data: { ...n.data, editing } } as FlowNode) : n)),
      ),
    [setNodes],
  )

  // Where a new object goes: on the unfolded object's board, making it up,
  // or on the board shown.
  const placement = useCallback(
    () => (unfoldedRef.current !== null ? { owner: unfoldedRef.current } : { parent: boardRef.current }),
    [],
  )

  // Processes and objects are named inline when they are created, and renamed
  // inline from their menu. A draft only gets saved once it has a name.
  const commitName = useCallback(
    async (id: string, name: string) => {
      const node = getNode(id)
      if (!node || node.type === 'start' || node.type === 'end') return
      setNodes((ns) => ns.map((n) => (n.id === id ? ({ ...n, data: { ...n.data, name, editing: false } } as FlowNode) : n)))
      try {
        if (!isDraft(id)) {
          // A struct is named after its object.
          if (isStructRootRef.current(id)) {
            await onUpdate({ name })
            await reload.current()
          } else if (node.type === 'object') await updateObject(objectIdOf(id), { name })
          else await updateProcess(Number(id), { name })
          return
        }
        const { x, y } = node.position
        const parent = boardRef.current
        const newId =
          node.type === 'object'
            ? objectNodeId((await createObject({ flow: flowId, name, x, y, human: node.data.human, ...placement() })).id)
            : String(
                (
                  await createProcess({
                    flow: flowId,
                    name,
                    kind: node.data.kind,
                    prompt: '',
                    script: '',
                    model: '',
                    thinking: '',
                    x,
                    y,
                    parent,
                    settings: node.data.settings,
                  })
                ).id,
              )
        // Once named, it widens to fit its name.
        setNodes((ns) => ns.map((n) => (n.id === id ? { ...n, id: newId, width: undefined } : n)))
        // A new subflow process brings the outcomes of the flow it runs, and
        // a new object on an unfolded one's board makes it up.
        if ((node.type === 'process' && node.data.kind === 'subflow') || unfoldedRef.current !== null) await reload.current()
      } catch (err) {
        report(err)
      }
    },
    [getNode, setNodes, report, flowId, placement, onUpdate],
  )

  const cancelName = useCallback(
    (id: string) => {
      if (isDraft(id)) setNodes((ns) => ns.filter((n) => n.id !== id))
      else setEditing(id, false)
    },
    [setNodes, setEditing],
  )

  // The objects of the flow tell what it is made of and what it specializes.
  const toObjectNode = useCallback(
    (o: FlowObject, editing = false, boundary?: 'in' | 'out', position?: XYPosition, objects: FlowObject[] = []): ObjectNodeType => ({
      id: objectNodeId(o.id),
      type: 'object',
      position: position ?? { x: o.x, y: o.y },
      width: o.width ?? undefined,
      deletable: false,
      data: {
        name: o.name,
        states: o.states,
        attributes: o.attributes,
        parts: ownedBy(objects, o.id).map((c) => ({
          id: c.id,
          name: c.name,
          many: c.many,
          min: c.min,
          max: c.max,
          relation: c.relation || 'exhibition',
        })),
        general: objects.find((g) => g.id === o.general)?.name,
        persistent: o.persistent,
        shared: o.shared,
        many: o.many,
        min: o.min,
        max: o.max,
        human: o.human,
        owned: o.owner !== null,
        copied: o.source !== null,
        sourceFlow: o.sourceFlow,
        editing,
        boundary,
      },
    }),
    [],
  )

  const toNode = useCallback(
    (p: Process, editing = false): ProcessNodeType => ({
      id: String(p.id),
      type: 'process',
      position: { x: p.x, y: p.y },
      width: p.width ?? undefined,
      deletable: false,
      data: {
        name: p.name,
        kind: p.kind,
        prompt: p.prompt,
        script: p.script,
        model: p.model,
        thinking: p.thinking,
        recording: p.recording,
        settings: p.settings,
        editing,
      },
    }),
    [],
  )

  const refreshRuns = useCallback(() => listRuns(flowId).then(setRuns).catch(report), [report, flowId])

  const refreshVersions = useCallback(() => listVersions(flowId).then(setVersions).catch(report), [report, flowId])

  // Shows an object unfolded, as OPM does: the object, the objects it owns,
  // linked to it by the structural link of how they make it up, the object it
  // specializes above it and those specializing it beside it. Only what it
  // owns is on its board; the others are where they are, and unfold in turn.
  const showObject = useCallback(
    (c: BoardContents, object: FlowObject) => {
      const fixed = (n: ObjectNodeType): ObjectNodeType => ({ ...n, draggable: false, data: { ...n.data, fixed: true } })
      const general = c.objects.find((o) => o.id === object.general)
      const specializations = specializationsOf(c.objects, object.id)
      const parts = ownedBy(c.objects, object.id)
      const width = object.width ?? NODE_SIZE.object.width
      // Each object once, though the general may be a part too.
      const nodes = new Map<string, ObjectNodeType>()
      const add = (n: ObjectNodeType) => nodes.has(n.id) || nodes.set(n.id, n)
      const root = fixed(toObjectNode(object, false, undefined, UNFOLDED_AT, c.objects))
      add({ ...root, data: { ...root.data, root: true } })
      for (const p of parts) add(toObjectNode(p, false, undefined, undefined, c.objects))
      if (general) add(fixed(toObjectNode(general, false, undefined, { x: UNFOLDED_AT.x, y: UNFOLDED_AT.y - UNFOLD_GAP.y }, c.objects)))
      specializations.forEach((sp, i) =>
        add(fixed(toObjectNode(sp, false, undefined, { x: UNFOLDED_AT.x + (i + 1) * (width + UNFOLD_GAP.x), y: UNFOLDED_AT.y }, c.objects))),
      )
      setNodes([...nodes.values()])
      setEdges([
        ...parts.map((p) => structureEdge(object.id, p.id, p.relation || 'exhibition')),
        ...(general ? [structureEdge(general.id, object.id, 'generalization')] : []),
        ...specializations.map((sp) => structureEdge(object.id, sp.id, 'generalization')),
      ])
    },
    [setNodes, setEdges, toObjectNode],
  )

  // Shows one board of the flow: its processes, its objects and their links.
  // A flow process's board also shows the objects the flow process is linked
  // to, the ones it takes above its start, leading into it, and the ones it
  // only generates above its end. On the top board, the objects connected to
  // its end by hand lead into it. A board
  // that is gone, such as after jumping to a version without its flow
  // process, gives way to the top board.
  const showBoard = useCallback(
    (c: BoardContents, wanted: BoardId, wantedObject: number | null) => {
      const inner = wanted === null ? undefined : c.boards.find((b) => b.process === wanted)
      const board = inner ? wanted : null
      if (board !== wanted) setBoardId(board)
      const object = c.objects.find((o) => o.id === wantedObject)
      if (wantedObject !== null && !object) setUnfolded(null)
      setContents(c)
      setShownSeq((n) => n + 1)
      if (object) {
        showObject(c, object)
        return
      }
      const ps = c.processes.filter((p) => p.parent === board)
      const ids = new Set(ps.map((p) => p.id))
      const humans = new Set(ps.filter((p) => p.kind === 'human').map((p) => p.id))
      const start = inner?.start ?? c.start
      const own = c.objects.filter((o) => o.parent === board && onBoard(o))
      // A top board's end without a place of its own sits below what is on it.
      const lowest = Math.max(
        start.y,
        ...ps.map((p) => p.y + NODE_SIZE.process.height),
        ...own.map((o) => o.y + NODE_SIZE.object.height),
      )
      const end = inner?.end ?? c.end ?? { x: start.x, y: lowest + END_GAP }

      // The objects outside, each once: taken ones at the start, even if generated too.
      const outside = (direction: 'input' | 'output', except: Set<number>) => [
        ...new Set(c.links.filter((l) => l.process === board && l.direction === direction && !except.has(l.object)).map((l) => l.object)),
      ]
      const ins = board === null ? [] : outside('input', new Set(own.map((o) => o.id)))
      const outs = board === null ? [] : outside('output', new Set([...own.map((o) => o.id), ...ins]))
      // What leads into a flow process's end.
      const ends = inner
        ? [...new Set(c.links.filter((l) => l.process === board && l.direction === 'output').map((l) => l.object))]
        : []
      const byId = new Map(c.objects.map((o) => [o.id, o]))
      // In a row, unless moved by hand.
      const placed = (id: number) => c.links.find((l) => l.process === board && l.object === id && l.at)?.at ?? undefined
      const row = (objects: number[], at: XYPosition, side: 'in' | 'out') =>
        objects.flatMap((id, i) => {
          const o = byId.get(id)
          const x = at.x + i * (NODE_SIZE.object.width + BOUNDARY_GAP.x)
          return o ? [toObjectNode(o, false, side, placed(id) ?? { x, y: at.y }, c.objects)] : []
        })

      setNodes([
        ...(structBoard ? [] : [toStartNode(start), toEndNode(end, !inner)]),
        ...ps.map((p) => toNode(p)),
        ...own.map((o) => toObjectNode(o, false, undefined, undefined, c.objects)),
        ...row(ins, { x: start.x, y: start.y - BOUNDARY_GAP.y }, 'in'),
        ...(inner ? row(outs, { x: inner.end.x, y: inner.end.y - BOUNDARY_GAP.y }, 'out') : []),
      ])
      setEdges([
        ...(start.target !== null && ids.has(start.target) ? [startEdge(start.target, start.route)] : []),
        ...c.connections.filter((cn) => ids.has(cn.source)).map(connectionEdge),
        ...c.links.filter((l) => ids.has(l.process)).map((l) => linkEdge(l, humans.has(l.process))),
        ...ins.map((o) => boundaryEdge(objectNodeId(o), START_ID)),
        ...ends.map((o) => boundaryEdge(objectNodeId(o), END_ID)),
        ...(inner ? [] : own.filter((o) => o.end).map((o) => endEdge(o.id))),
      ])
    },
    [setNodes, setEdges, toNode, toObjectNode, showObject, structBoard],
  )

  // A struct's board is its object unfolded, where what makes it up is added.
  const loadBoard = useCallback(
    () =>
      fetchBoard(flowId).then((c) => {
        let wanted = unfoldedRef.current
        if (structBoard && !c.objects.some((o) => o.id === wanted)) {
          wanted = c.objects.find((o) => o.parent === null && o.owner === null)?.id ?? null
          setUnfolded(wanted)
        }
        showBoard(c, boardRef.current, wanted)
      }),
    [showBoard, flowId, structBoard],
  )
  reload.current = loadBoard

  const refreshMCPServers = useCallback(() => listMCPServers().then(setMCPServers).catch(report), [report])

  // A sign-in window says when it is done; coming back to this window after
  // one that couldn't say so shows the outcome too.
  useEffect(() => {
    const onMessage = (e: MessageEvent) => {
      if (e.origin === window.location.origin && e.data?.type === 'mcp-sign-in') refreshMCPServers()
    }
    const onFocus = () => refreshMCPServers()
    window.addEventListener('message', onMessage)
    window.addEventListener('focus', onFocus)
    return () => {
      window.removeEventListener('message', onMessage)
      window.removeEventListener('focus', onFocus)
    }
  }, [refreshMCPServers])

  useEffect(() => {
    listModels().then(setModels).catch(report)
    sentryConfigured().then(setSentry).catch(report)
    listFlows()
      .then((fs) => setOtherFlows(fs.filter((f) => f.id !== flowId)))
      .catch(report)
    refreshMCPServers()
    refreshRuns()
    refreshVersions()
    // The flow just loaded; show all of it once its nodes are measured.
    loadBoard()
      .then(() => window.setTimeout(() => fitView(FIT_VIEW), BOARD_FIT_DELAY_MS))
      .catch(report)
  }, [loadBoard, report, refreshRuns, refreshVersions, refreshMCPServers, fitView, flowId])

  // The settings take the side panel; a node opened afterwards takes it back.
  const openMCPSettings = (open: boolean) => {
    if (open) {
      setPanelId(null)
      setVersionsOpen(false)
      setSettingsOpen(false)
      setOPLOpen(false)
      setCodeOpen(false)
      refreshMCPServers()
    }
    setMCPOpen(open)
  }

  const openFlowSettings = (open: boolean) => {
    if (open) {
      setPanelId(null)
      setVersionsOpen(false)
      setMCPOpen(false)
      setOPLOpen(false)
      setCodeOpen(false)
    }
    setSettingsOpen(open)
  }

  const openOPL = (open: boolean) => {
    if (open) {
      setPanelId(null)
      setVersionsOpen(false)
      setMCPOpen(false)
      setSettingsOpen(false)
      setCodeOpen(false)
    }
    setOPLOpen(open)
  }

  const openCode = (open: boolean) => {
    if (open) {
      setPanelId(null)
      setVersionsOpen(false)
      setMCPOpen(false)
      setSettingsOpen(false)
      setOPLOpen(false)
    }
    setCodeOpen(open)
  }

  const onSave = async () => {
    setVersioning(true)
    try {
      await saveVersion(flowId)
      await refreshVersions()
    } catch (err) {
      report(err)
    } finally {
      setVersioning(false)
    }
  }

  // Brings the board back to a saved version. The run shown on the board
  // belongs to the board it left, so it closes.
  const onRestore = async (id: number) => {
    setVersioning(true)
    try {
      await restoreVersion(id)
      setPanelId(null)
      setMenu(null)
      setRunId(null)
      await Promise.all([loadBoard(), refreshVersions()])
    } catch (err) {
      report(err)
    } finally {
      setVersioning(false)
    }
  }

  // Loads the open run, and keeps refreshing it while it goes on. Once it
  // ended, the run list catches up.
  useEffect(() => {
    if (runId === null) {
      setRunDetail(null)
      return
    }
    let stopped = false
    let timer: number | undefined
    const load = async () => {
      try {
        const detail = await getRun(runId)
        if (stopped) return
        setRunDetail(detail)
        // Follow-ups go on once the run is over.
        const following = detail.steps.some((s) => s.followUps.some((f) => f.status === 'active'))
        if (detail.status === 'running' || following) timer = window.setTimeout(load, RUN_REFRESH_MS)
        if (detail.status !== 'running') setRuns((rs) => rs.map((r) => (r.id === detail.id ? { ...r, status: detail.status, finishedAt: detail.finishedAt } : r)))
      } catch (err) {
        report(err)
      }
    }
    load()
    return () => {
      stopped = true
      window.clearTimeout(timer)
    }
  }, [runId, runReload, report])

  // Runs go on in the server whether open or not, so the list keeps up with them.
  const anyRunning = runs.some((r) => r.status === 'running')
  useEffect(() => {
    if (!anyRunning) return
    const timer = window.setInterval(refreshRuns, RUN_LIST_REFRESH_MS)
    return () => window.clearInterval(timer)
  }, [anyRunning, refreshRuns])

  // A run shows on the board it runs or ran on, its version, which can't be
  // edited while the run is open. Runs from before versions were kept show
  // on the board as it is.
  const shownRun = runDetail?.id === runId ? runDetail : null
  const openRun = runs.find((r) => r.id === runId)
  const runVersion = openRun?.version ?? null
  // The version the board shows, null for the board itself, and which of
  // its boards it shows.
  const [shownVersion, setShownVersion] = useState<number | null>(null)
  const [shownBoard, setShownBoard] = useState<BoardId>(null)
  const [shownUnfolded, setShownUnfolded] = useState<number | null>(null)
  const reviewing = shownVersion !== null
  // Someone who only decides the flow's reviews sees it, and changes nothing.
  const viewOnly = flow.access === 'review'
  const readOnly = reviewing || viewOnly
  const runShown = shownRun !== null
  useEffect(() => {
    if (runVersion === shownVersion && boardId === shownBoard && unfolded === shownUnfolded) return
    let stale = false
    ;(runVersion === null ? fetchBoard(flowId) : getVersionBoard(flowId, runVersion))
      .then((c) => {
        if (stale) return
        setPanelId(null)
        setMenu(null)
        showBoard(c, boardId, unfolded)
        setShownVersion(runVersion)
        setShownBoard(boardId)
        setShownUnfolded(unfolded)
        // Another board is a different place; show all of it once its nodes are measured.
        if (boardId !== shownBoard || unfolded !== shownUnfolded) window.setTimeout(() => fitView(FIT_VIEW), BOARD_FIT_DELAY_MS)
      })
      .catch(report)
    return () => {
      stale = true
    }
  }, [runVersion, shownVersion, boardId, shownBoard, unfolded, shownUnfolded, showBoard, fitView, report, flowId])

  // Shows the open run on the board: each process as its last step in the
  // run left it, each object with the content and state it last got. A
  // review waiting for a decision shows in the middle of the screen, unless
  // another does, and opens its process's panel, unless another panel is open.
  useEffect(() => {
    if (!shownRun || shownVersion !== runVersion) return
    const steps = new Map(shownRun.steps.flatMap((s) => (s.process === null ? [] : [[String(s.process), s] as const])))
    const rerunnable = new Set(shownRun.steps.flatMap((s) => (rerunnableFrom(shownRun, s) ? [String(s.process)] : [])))
    const objects = new Map(shownRun.objects.flatMap((o) => (o.object === null ? [] : [[objectNodeId(o.object), o] as const])))
    // A review inside a flow process opens once its board shows.
    for (const [process, s] of steps) {
      if (s.status !== 'waiting' || shownReviews.current.has(s.id) || !getNode(process)) continue
      shownReviews.current.add(s.id)
      setReviewId((open) => open ?? process)
      setPanelId((open) => open ?? process)
    }
    setNodes((ns) =>
      ns.map((n): FlowNode => {
        if (n.type === 'process') {
          const s = steps.get(n.id)
          const output =
            s?.kind === 'human' && s.outcome
              ? decisionText(s.outcome, shownRun.objects.find((o) => o.step === s.id)?.name, s.instructions)
              : s?.output
          const review = s?.kind === 'human' && s.status === 'waiting' ? s.inputs : undefined
          return {
            ...n,
            data: {
              ...n.data,
              status: s?.status,
              output: output || undefined,
              runError: s?.error || undefined,
              review,
              rerunnable: rerunnable.has(n.id),
            },
          }
        }
        if (n.type === 'object') {
          const o = objects.get(n.id)
          return {
            ...n,
            data: { ...n.data, status: o && 'done', content: o?.content, activeState: o?.state || undefined },
          }
        }
        return n
      }),
    )
  }, [shownRun, shownVersion, runVersion, shownSeq, getNode, setNodes])

  // A process takes the block's kind, such as a connector's.
  const addDraft = useCallback(
    ({ type, kind = 'agent', human = false, name = '', settings = {} }: Block, screenX: number, screenY: number) => {
      // What makes up an object is objects.
      if (unfoldedRef.current !== null && (type !== 'object' || human)) return
      const center = screenToFlowPosition({ x: screenX, y: screenY })
      const { width, height } = NODE_SIZE[type]
      const base = {
        id: `${DRAFT_PREFIX}${++draftSeq.current}`,
        position: { x: center.x - width / 2, y: center.y - height / 2 },
        width,
        selected: true,
        deletable: false,
      }
      const draft: FlowNode =
        type === 'process'
          ? { ...base, type, data: { name, kind, prompt: '', script: '', model: '', thinking: '', recording: false, settings, editing: true } }
          : { ...base, type, data: { name: '', states: [], attributes: [], persistent: false, shared: false, many: false, min: 0, max: null, human, editing: true } }
      setNodes((ns) => [...ns.map((n) => ({ ...n, selected: false })), draft])
    },
    [screenToFlowPosition, setNodes],
  )

  // A struct's object lands as a copy, already named after it.
  const addStruct = async (struct: number, screenX: number, screenY: number) => {
    if (unfoldedRef.current !== null) return
    const center = screenToFlowPosition({ x: screenX, y: screenY })
    const { width, height } = NODE_SIZE.object
    try {
      await copyStruct(struct, { flow: flowId, parent: boardRef.current, x: center.x - width / 2, y: center.y - height / 2 })
      await reload.current()
    } catch (err) {
      report(err)
    }
  }

  const addBlock = (block: Block, screenX: number, screenY: number) =>
    block.struct === undefined ? addDraft(block, screenX, screenY) : addStruct(block.struct, screenX, screenY)

  const addAtViewportCenter = (block: Block) => {
    const rect = wrapper.current?.getBoundingClientRect()
    if (rect && !readOnly) addBlock(block, rect.left + rect.width / 2, rect.top + rect.height / 2)
  }

  const onPaneDoubleClick = (e: MouseEvent<HTMLDivElement>) => {
    if (structBoard && !objectBoard) return
    if (!readOnly && (e.target as HTMLElement).classList.contains('react-flow__pane')) {
      addDraft(BUILDING_BLOCKS.find((b) => b.id === (e.shiftKey || objectBoard ? 'object' : 'process'))!, e.clientX, e.clientY)
    }
  }

  // A block dragged from the sidebar lands where it is dropped. Its id can
  // only be read on the drop, so a drag is told apart by its type.
  const onCanvasDragOver = (e: DragEvent<HTMLDivElement>) => {
    if (readOnly || !e.dataTransfer.types.includes(BLOCK_MIME)) return
    e.preventDefault()
    e.dataTransfer.dropEffect = 'copy'
  }

  // The module or struct an object is a copy of an object of, if it is one.
  const moduleOf = (object: number) => {
    const o = contents?.objects.find((x) => x.id === object)
    if (!o || o.source === null || o.sourceFlow === null) return undefined
    const from = otherFlows?.find((f) => f.id === o.sourceFlow)
    return { id: o.sourceFlow, name: from?.name ?? `Procedure #${o.sourceFlow}`, struct: Boolean(from?.struct) }
  }

  // The object a struct is, which goes only with the struct.
  const isStructRoot = (id: string) => {
    if (!structBoard || !isObject(id)) return false
    const o = contents?.objects.find((x) => x.id === objectIdOf(id))
    return o !== undefined && o.owner === null && o.parent === null
  }
  isStructRootRef.current = isStructRoot
  // On a shared struct's board, its object, whose records the sidebar lists.
  const sharedRoot = structBoard
    ? nodes.find((n): n is ObjectNodeType => n.type === 'object' && isStructRoot(n.id) && Boolean(n.data.shared))
    : undefined

  // Only modules and structs are reused; the flows that run stay on the main page.
  const flowBlocks = useMemo(() => (otherFlows ?? []).filter((f) => f.module).map(flowBlock), [otherFlows])
  const structBlocks = useMemo(() => (otherFlows ?? []).filter((f) => f.struct).map(structBlock), [otherFlows])
  const connectorTiles = useMemo(
    () => (mcpServers === null ? null : [...(sentry ? [SENTRY_BLOCK] : []), ...connectorBlocks(mcpServers)]),
    [mcpServers, sentry],
  )
  const structFlows = useMemo(() => new Set((otherFlows ?? []).filter((f) => f.struct).map((f) => f.id)), [otherFlows])

  const onCanvasDrop = (e: DragEvent<HTMLDivElement>) => {
    const id = e.dataTransfer.getData(BLOCK_MIME)
    const block = findBlock(id) ?? [...(connectorTiles ?? []), ...flowBlocks, ...structBlocks].find((b) => b.id === id)
    if (readOnly || !block || (structBoard && !objectBoard)) return
    e.preventDefault()
    addBlock(block, e.clientX, e.clientY)
  }

  // A flow process opens its board, a subflow process the flow it runs;
  // other nodes their settings, or, while a run is shown, what they did and
  // held in it, their settings staying behind their Settings button. An
  // object unfolds from its menu or button; a person has nothing in a run.
  const onNodeDoubleClick: NodeMouseHandler<FlowNode> = (_, node) => {
    if (node.type === 'process' && zoomable(node.id)) zoomIn(node.id)
    else if (isDraft(node.id) || node.type === 'start' || node.type === 'end') return
    else if (shownRun && (node.type === 'process' || (node.type === 'object' && !node.data.human))) showInRun(node.id, node.data.name)
    else setPanelId(node.id)
  }

  // Brings the run panel back, closing what took it, on what the node did
  // or held in the run.
  const showInRun = (id: string, name: string) => {
    setPanelId(null)
    setMenu(null)
    setMCPOpen(false)
    setSettingsOpen(false)
    setOPLOpen(false)
    setCodeOpen(false)
    setVersionsOpen(false)
    setRunFocus((f) => ({
      kind: isObject(id) ? 'object' : 'process',
      id: isObject(id) ? objectIdOf(id) : Number(id),
      name,
      seq: (f?.seq ?? 0) + 1,
    }))
  }

  // An object, other than a person, unfolds, unless it is the one unfolded.
  const zoomable = (id: string) => {
    const node = getNode(id)
    if (isDraft(id)) return false
    if (node?.type === 'object') return !node.data.human && objectIdOf(id) !== unfolded
    if (node?.type !== 'process') return false
    return node.data.kind === 'flow' || (node.data.kind === 'subflow' && node.data.settings.flow !== undefined)
  }

  // Shows the object unfolded, under the board of the object it makes up.
  const unfold = (id: number) => {
    setPanelId(null)
    setMenu(null)
    const top = ownersOf(contents?.objects ?? [], id)[0]
    if (top && top.parent !== boardId && (top.parent === null || contents?.boards.some((b) => b.process === top.parent))) setBoardId(top.parent)
    setUnfolded(id)
  }

  // Zooming into a subflow process goes to the flow it runs, on the run it
  // ran there if a run is shown, and the browser's back button comes back.
  const zoomIn = (id: string) => {
    if (isObject(id)) {
      unfold(objectIdOf(id))
      return
    }
    setPanelId(null)
    setMenu(null)
    const node = getNode(id)
    if (node?.type === 'process' && node.data.kind === 'subflow') {
      const target = node.data.settings.flow
      if (target === undefined) return
      const child = shownRun?.steps.filter((s) => String(s.process) === id && s.childRun !== null).at(-1)?.childRun
      window.location.hash = child ? `#/flows/${target}/runs/${child}` : `#/flows/${target}`
      return
    }
    setUnfolded(null)
    setBoardId(Number(id))
  }

  const isBoundary = (id: string) => {
    const node = getNode(id)
    return node?.type === 'object' && node.data.boundary !== undefined
  }

  // On an unfolded object's board, the objects that aren't on it: itself,
  // the one it specializes and those specializing it.
  const isFixed = (id: string) => {
    const node = getNode(id)
    return node?.type === 'object' && Boolean(node.data.fixed)
  }

  // A copy of a module's object changes only in the module.
  const isCopied = (id: string) => {
    const node = getNode(id)
    return node?.type === 'object' && Boolean(node.data.copied)
  }

  const panelNode = nodes.find((n): n is ProcessNodeType => n.type === 'process' && n.id === panelId)
  const reviewNode = nodes.find((n): n is ProcessNodeType => n.type === 'process' && n.id === reviewId && Boolean(n.data.review))
  const objectPanelNode = nodes.find((n): n is ObjectNodeType => n.type === 'object' && n.id === panelId)
  const panelOpen = Boolean(panelNode || objectPanelNode || mcpOpen || settingsOpen || oplOpen || codeOpen || versionsOpen || runDetail)

  // The panel opening or closing changes the canvas's width; the board moves
  // to the middle of it once the canvas has taken its new width.
  const panelWasOpen = useRef(panelOpen)
  useEffect(() => {
    if (panelOpen === panelWasOpen.current) return
    panelWasOpen.current = panelOpen
    window.setTimeout(() => fitView(FIT_VIEW), BOARD_FIT_DELAY_MS)
  }, [panelOpen, fitView])

  const savePositions = (positions: Map<string, XYPosition>) => {
    for (const [id, { x, y }] of positions) {
      if (id === START_ID) updateStart(flowId, boardId, { x, y }).catch(report)
      else if (id === END_ID) updateEnd(flowId, boardId, { x, y }).catch(report)
      else if (isDraft(id) || isFixed(id)) continue
      // An object outside keeps its place here on its links to the board's process.
      else if (isBoundary(id)) {
        for (const l of contents?.links ?? [])
          if (l.process === boardId && l.object === objectIdOf(id)) setLinkAt(l.id, { x, y }).catch(report)
      }
      else if (isObject(id)) updateObject(objectIdOf(id), { x, y }).catch(report)
      else updateProcess(Number(id), { x, y }).catch(report)
    }
  }

  const moveNodes = (positions: Map<string, XYPosition>) => {
    setNodes((ns) =>
      ns.map((n) => {
        const position = positions.get(n.id)
        return position ? { ...n, position } : n
      }),
    )
    savePositions(positions)
  }

  const onNodeDragStop: OnNodeDrag<FlowNode> = (_, __, dragged) =>
    savePositions(new Map(dragged.map((n) => [n.id, n.position])))

  // Saves a node's new width, and its position, which moves when resized from the left.
  const resize = (id: string, x: number, width: number) => {
    if (isDraft(id)) return
    // An unfolded object keeps its place on the board it is on.
    const req = isFixed(id)
      ? updateObject(objectIdOf(id), { width })
      : isObject(id)
        ? updateObject(objectIdOf(id), { x, width })
        : updateProcess(Number(id), { x, width })
    req.catch(report)
  }

  // Sets where an edge's segments run, null for the default route, and saves
  // it if asked.
  const setRoute = (edgeId: string, route: number[] | null, save: boolean) => {
    setEdges((es) => es.map((e) => (e.id === edgeId ? { ...e, data: { ...e.data, route } } : e)))
    if (!save) return
    const link = edges.find((e) => e.id === edgeId)?.data?.link
    if (edgeId === START_EDGE_ID) updateStart(flowId, boardId, { route }).catch(report)
    else if (link) setLinkRoute(link.id, route).catch(report)
  }

  // Lays the whole board out from the top, and keeps that layout. Routes
  // drawn by hand were for the old layout, so links go back to the default.
  // The end goes last even when nothing leads into it, as if what leads
  // nowhere led into it.
  const onTidy = () => {
    if (objectBoard) {
      tidyObject()
      return
    }
    const nodes = getNodes()
    const leads = edges.some((e) => e.target === END_ID)
      ? []
      : nodes
          .filter((n) => n.id !== START_ID && n.id !== END_ID && !edges.some((e) => e.source === n.id))
          .map((n) => ({ id: `${BOUNDARY_PREFIX}${n.id}-${END_ID}`, source: n.id, target: END_ID }))
    moveNodes(tidy(nodes, [...edges, ...leads]))
    for (const e of edges) if (e.data?.route) setRoute(e.id, null, true)
    window.requestAnimationFrame(() => fitView(FIT_VIEW))
  }

  // An unfolded object stays where it is, and what makes it up is laid out
  // below it; the objects shown beside it, such as its general, stay too.
  const tidyObject = () => {
    const nodes = getNodes().filter((n) => n.type === 'object' && (n.data.root || !isFixed(n.id)))
    const root = nodes.find((n) => n.type === 'object' && n.data.root)
    if (!root) return
    const ids = new Set(nodes.map((n) => n.id))
    const laid = tidy(nodes, edges.filter((e) => ids.has(e.source) && ids.has(e.target)))
    const at = laid.get(root.id)!
    const dx = root.position.x - at.x
    const dy = root.position.y - at.y
    const moved = [...laid].filter(([id]) => id !== root.id)
    moveNodes(new Map(moved.map(([id, p]) => [id, { x: p.x + dx, y: p.y + dy }])))
    window.requestAnimationFrame(() => fitView(FIT_VIEW))
  }

  const humanProcess = (id: string) => {
    const node = getNode(id)
    return node?.type === 'process' && node.data.kind === 'human'
  }

  // As in OPM, processes connect through objects: a process generates an
  // object, which another process takes as input. Either end can be one of
  // the object's states, except a human's link out: it goes to the object,
  // and lists the states the human decides between. A process links out to
  // an object once. The start node points at a process. Drafts get a new id once saved,
  // which would orphan their edges, so they can't be connected until they
  // have a name.
  const isValidConnection: IsValidConnection = ({ source, target, sourceHandle, targetHandle }) => {
    // An unfolded object's links come from what makes it up.
    if (objectBoard || isDraft(source) || isDraft(target)) return false
    const same = (a: string | null | undefined, b: string | null | undefined) => (a ?? null) === (b ?? null)
    if (edges.some((e) => e.source === source && e.target === target && same(e.sourceHandle, sourceHandle) && same(e.targetHandle, targetHandle))) {
      return false
    }
    // An object of the top board, not a person, leads into its end.
    if (target === END_ID) {
      const node = getNode(source)
      return boardId === null && node?.type === 'object' && !node.data.human && stateOfHandle(sourceHandle) === null
    }
    if (source === START_ID || isObject(source)) return isProcess(target)
    // A process dropped on another handles its failure, if it can fail.
    if (isProcess(source) && isProcess(target)) {
      const node = getNode(source)
      const fails = node?.type === 'process' && ['agent', 'script', 'sentry', 'connector'].includes(node.data.kind)
      return fails && source !== target && !edges.some((e) => e.source === source && e.target === target)
    }
    if (!isProcess(source) || !isObject(target)) return false
    if (edges.some((e) => e.source === source && e.target === target)) return false
    return !(humanProcess(source) && stateOfHandle(targetHandle) !== null)
  }

  const onConnect: OnConnect = async ({ source, target, sourceHandle, targetHandle }) => {
    try {
      if (source === START_ID) {
        // The initial node has a single outgoing transition; a new one replaces it.
        await updateStart(flowId, boardId, { target: Number(target) })
        setEdges((es) => [...es.filter((e) => e.id !== START_EDGE_ID), startEdge(target)])
        return
      }
      if (target === END_ID) {
        await updateObject(objectIdOf(source), { end: true })
        setEdges((es) => [...es, endEdge(objectIdOf(source))])
        return
      }
      if (isProcess(source) && isProcess(target)) {
        const c = await createConnection({ source: Number(source), target: Number(target), kind: 'failure' })
        setEdges((es) => [...es, connectionEdge(c)])
        return
      }
      // A human starts out deciding between all of the object's states.
      const object = getNode(isObject(source) ? source : target)
      const human = !isObject(source) && humanProcess(source)
      const l = isObject(source)
        ? await createLink({ process: Number(target), object: objectIdOf(source), direction: 'input', state: stateOfHandle(sourceHandle) })
        : await createLink({
            process: Number(source),
            object: objectIdOf(target),
            direction: 'output',
            state: stateOfHandle(targetHandle),
            states: human && object?.type === 'object' ? object.data.states.map((st) => st.id) : [],
          })
      setEdges((es) => [...es, linkEdge(l, human)])
    } catch (err) {
      report(err)
    }
  }

  // While a link is being drawn, the kind of node it can end on, which lights
  // up under the pointer.
  const [linkingTo, setLinkingTo] = useState<'process' | 'object' | null>(null)
  const onConnectStart: OnConnectStart = (_, { nodeId, handleId, handleType }) => {
    if (!nodeId) return
    if (handleType === 'source') setLinkingTo(isProcess(nodeId) ? 'object' : 'process')
    else if (stateOfHandle(handleId) !== null) setLinkingTo('process')
  }

  // A link dropped on a node rather than on one of its handles goes to the
  // node, or to the state whose row it was dropped on. A human's link can't
  // end on a state, so it falls back to the object. A state's two handles
  // look alike, so a link drawn from its way in to a process is taken as one
  // from its way out: the process takes the object in that state.
  const onConnectEnd: OnConnectEnd = (event, { isValid, fromHandle }) => {
    setLinkingTo(null)
    if (isValid || !fromHandle) return
    let sourceHandle = fromHandle.id ?? null
    if (fromHandle.type === 'target') {
      const state = stateOfHandle(fromHandle.id)
      if (state === null) return
      sourceHandle = stateSourceHandle(state)
    }
    const { clientX, clientY } = 'changedTouches' in event ? event.changedTouches[0] : event
    const below = document.elementFromPoint(clientX, clientY)
    const target = below?.closest<HTMLElement>('.react-flow__node')?.dataset.id
    if (!target) return
    const row = below?.closest<HTMLElement>('[data-state]')?.dataset.state
    const candidates = [...(row ? [stateTargetHandle(Number(row))] : []), null]
    for (const targetHandle of candidates) {
      const connection = { source: fromHandle.nodeId, sourceHandle, target, targetHandle }
      if (isValidConnection(connection)) {
        onConnect(connection)
        return
      }
    }
  }

  const onEdgesDelete: OnEdgesDelete<FlowEdge> = (deleted) => {
    for (const e of deleted) {
      const req =
        e.id === START_EDGE_ID
          ? updateStart(flowId, boardId, { target: null })
          : e.id.startsWith(END_EDGE_PREFIX)
            ? updateObject(Number(e.id.slice(END_EDGE_PREFIX.length)), { end: false })
            : e.id.startsWith(LINK_PREFIX)
              ? deleteLink(Number(e.id.slice(LINK_PREFIX.length)))
              : deleteConnection(Number(e.id.slice(CONNECTION_PREFIX.length)))
      req.catch(report)
    }
  }

  const newProcess = (position: XYPosition, kind: ProcessKind = 'agent') =>
    createProcess({ flow: flowId, name: NEW_PROCESS, kind, prompt: '', script: '', model: '', thinking: '', parent: boardId, ...position })

  // Where a node of the given type goes right below another, centered on it.
  const below = (node: FlowNode, type: 'process' | 'object'): XYPosition => ({
    x: node.position.x + (node.measured?.width ?? 0) / 2 - NODE_SIZE[type].width / 2,
    y: node.position.y + (node.measured?.height ?? 0) + RANK_GAP,
  })

  // Below a process goes the object it generates, below an object a process
  // that takes it; below a person, the human process they carry out. The new
  // node starts out being named.
  const append = async (id: string) => {
    const node = getNode(id)
    if (!node || node.type === 'start' || node.type === 'end' || readOnly || objectBoard) return
    try {
      if (node.type === 'process') {
        const o = await createObject({ flow: flowId, name: NEW_OBJECT, parent: boardId, ...below(node, 'object') })
        const l = await createLink({ process: Number(id), object: o.id, direction: 'output' })
        setNodes((ns) => [...ns, toObjectNode(o, true)])
        setEdges((es) => [...es, linkEdge(l, node.data.kind === 'human')])
      } else {
        const p = await newProcess(below(node, 'process'), node.data.human ? 'human' : 'agent')
        const l = await createLink({ process: p.id, object: objectIdOf(id), direction: 'input' })
        setNodes((ns) => [...ns, toNode(p, true)])
        setEdges((es) => [...es, linkEdge(l, false)])
      }
    } catch (err) {
      report(err)
    }
  }

  // Splits an edge with a new process and the object between it and the
  // other side, so processes stay connected through objects: after an object
  // or the start node the process comes first, after a process the object
  // does. A state the link named stays at its end. Everything from the edge's
  // target down moves down to make room.
  const insert = async (edgeId: string) => {
    const edge = edges.find((e) => e.id === edgeId)
    const source = edge && getNode(edge.source)
    const target = edge && getNode(edge.target)
    if (!edge || !source || !target || readOnly) return
    const state = edge.data?.link?.state ?? null
    const processFirst = source.type !== 'process'
    const [firstType, secondType] = processFirst ? (['process', 'object'] as const) : (['object', 'process'] as const)
    const centerX = target.position.x + (target.measured?.width ?? 0) / 2
    const at = (type: 'process' | 'object', y: number) => ({ x: centerX - NODE_SIZE[type].width / 2, y })
    const firstY = target.position.y
    const secondY = firstY + NODE_SIZE[firstType].height + RANK_GAP
    const shift = secondY + NODE_SIZE[secondType].height + RANK_GAP - firstY

    try {
      const p = await newProcess(at('process', processFirst ? firstY : secondY))
      const o = await createObject({ flow: flowId, name: NEW_OBJECT, parent: boardId, ...at('object', processFirst ? secondY : firstY) })
      const links: Link[] = []
      if (processFirst) {
        if (edge.id === START_EDGE_ID) await updateStart(flowId, boardId, { target: p.id })
        else links.push(await createLink({ process: p.id, object: objectIdOf(edge.source), direction: 'input', state }))
        links.push(await createLink({ process: p.id, object: o.id, direction: 'output' }))
        links.push(await createLink({ process: Number(edge.target), object: o.id, direction: 'input' }))
      } else {
        links.push(await createLink({ process: Number(edge.source), object: o.id, direction: 'output' }))
        links.push(await createLink({ process: p.id, object: o.id, direction: 'input' }))
        links.push(await createLink({ process: p.id, object: objectIdOf(edge.target), direction: 'output', state }))
      }
      if (edge.id.startsWith(LINK_PREFIX)) await deleteLink(Number(edge.id.slice(LINK_PREFIX.length)))

      moveNodes(
        new Map(
          getNodes()
            .filter((n) => n.position.y >= firstY && !isBoundary(n.id))
            .map((n) => [n.id, { x: n.position.x, y: n.position.y + shift }]),
        ),
      )
      setNodes((ns) => [...ns, toNode(p, true), toObjectNode(o)])
      setEdges((es) => [
        ...es.filter((e) => e.id !== edge.id),
        ...(edge.id === START_EDGE_ID ? [startEdge(p.id)] : []),
        ...links.map((l) => linkEdge(l, false)),
      ])
    } catch (err) {
      report(err)
    }
  }

  // Its links go with it on the server, and a flow process's board with all
  // on it; runs keep their record of it. The objects outside a flow process
  // belong to the board outside.
  const remove = async (id: string) => {
    const node = getNode(id)
    if (!node || node.type === 'start' || node.type === 'end' || readOnly || isBoundary(id) || isFixed(id) || isStructRoot(id))
      return
    const inside =
      node.type === 'process' && node.data.kind === 'flow'
        ? ', its links and everything on its board'
        : node.type === 'object' && objectBoard
          ? ' and everything it is made of'
          : ' and its links'
    if (!window.confirm(`Delete “${node.data.name}”${inside}?`)) return
    try {
      if (node.type === 'object') await deleteObject(objectIdOf(id))
      else await deleteProcess(Number(id))
    } catch (err) {
      report(err)
      return
    }
    setNodes((ns) => ns.filter((n) => n.id !== id))
    setEdges((es) => es.filter((e) => e.source !== id && e.target !== id))
    setPanelId((open) => (open === id ? null : open))
    // The object it made up has one field less.
    if (objectBoard) reload.current().catch(report)
  }

  // A copy without links, next to the original.
  const duplicate = async (id: string) => {
    const node = getNode(id)
    if (!node || node.type === 'start' || node.type === 'end') return
    const position = { x: node.position.x + DUPLICATE_OFFSET, y: node.position.y + DUPLICATE_OFFSET }
    try {
      const { name } = node.data
      const width = node.width ?? null
      const copy: FlowNode =
        node.type === 'object'
          ? toObjectNode(await createObject({ flow: flowId, name, width, human: node.data.human, parent: boardId, ...position }))
          : toNode(
              await createProcess({
                flow: flowId,
                name,
                kind: node.data.kind,
                prompt: node.data.prompt,
                script: node.data.script,
                model: node.data.model,
                thinking: node.data.thinking,
                recording: node.data.recording,
                settings: node.data.settings,
                parent: boardId,
                width,
                ...position,
              }),
            )
      setNodes((ns) => [...ns.map((n) => ({ ...n, selected: false })), { ...copy, selected: true }])
    } catch (err) {
      report(err)
    }
  }

  const setStates = (objectId: string, update: (states: ObjectState[]) => ObjectState[]) =>
    setNodes((ns) =>
      ns.map((n) => (n.id === objectId && n.type === 'object' ? { ...n, data: { ...n.data, states: update(n.data.states) } } : n)),
    )

  const addState = async (objectId: string, name: string) => {
    updateObjectData(objectId, { addingState: false })
    try {
      const state = await createState(objectIdOf(objectId), name)
      setStates(objectId, (ss) => [...ss, state])
    } catch (err) {
      report(err)
    }
  }

  const renameObjectState = async (objectId: string, stateId: number, name: string) => {
    try {
      const state = await renameState(stateId, name)
      setStates(objectId, (ss) => ss.map((s) => (s.id === stateId ? state : s)))
    } catch (err) {
      report(err)
    }
  }

  const changeStateRule = async (objectId: string, stateId: number, rule: string) => {
    try {
      const state = await setStateRule(stateId, rule)
      setStates(objectId, (ss) => ss.map((s) => (s.id === stateId ? state : s)))
      return true
    } catch (err) {
      report(err)
      return false
    }
  }

  // The links naming the state go with it.
  const removeState = async (objectId: string, stateId: number) => {
    const node = getNode(objectId)
    const state = node?.type === 'object' ? node.data.states.find((s) => s.id === stateId) : undefined
    if (!state || readOnly || !window.confirm(`Delete state “${state.name}” and its links?`)) return
    try {
      await deleteState(stateId)
    } catch (err) {
      report(err)
      return
    }
    setStates(objectId, (ss) => ss.filter((s) => s.id !== stateId))
    setEdges((es) =>
      es.flatMap((e) => {
        const l = e.data?.link
        if (!l) return [e]
        if (l.state === stateId) return []
        if (!l.states.includes(stateId)) return [e]
        return [{ ...e, data: { ...e.data, link: { ...l, states: l.states.filter((st) => st !== stateId) } } }]
      }),
    )
  }

  const setCondition = async (edgeId: string, condition: boolean) => {
    const edge = edges.find((e) => e.id === edgeId)
    const l = edge?.data?.link
    if (!l || readOnly) return
    try {
      await setLinkCondition(l.id, condition)
    } catch (err) {
      report(err)
      return
    }
    setEdges((es) => es.map((e) => (e.id === edgeId ? { ...e, data: { ...e.data, link: { ...l, condition } } } : e)))
  }

  // Makes an input link a query of the object's stored records, or a plain
  // link again; see queries.go.
  const setQuery = async (linkId: number, query: LinkQuery | null) => {
    if (readOnly) return
    try {
      await setLinkQuery(linkId, query)
    } catch (err) {
      report(err)
      return
    }
    setEdges((es) => es.map((e) => (e.data?.link?.id === linkId ? { ...e, data: { ...e.data, link: { ...e.data.link, query } } } : e)))
  }

  const setRole = async (edgeId: string, role: LinkRole) => {
    const edge = edges.find((e) => e.id === edgeId)
    const l = edge?.data?.link
    if (!l || readOnly) return
    try {
      await setLinkRole(l.id, role)
    } catch (err) {
      report(err)
      return
    }
    setEdges((es) => es.map((e) => (e.id === edgeId ? { ...e, data: { ...e.data, link: { ...l, role } } } : e)))
  }

  const setDecisionStates = async (edgeId: string, states: number[]) => {
    const edge = edges.find((e) => e.id === edgeId)
    const l = edge?.data?.link
    if (!l || readOnly) return
    try {
      await setLinkStates(l.id, states)
    } catch (err) {
      report(err)
      return
    }
    setEdges((es) => es.map((e) => (e.id === edgeId ? { ...e, data: { ...e.data, link: { ...l, states } } } : e)))
  }

  const setAttributes = (objectId: string, update: (attributes: ObjectAttribute[]) => ObjectAttribute[]) =>
    setNodes((ns) =>
      ns.map((n) =>
        n.id === objectId && n.type === 'object' ? { ...n, data: { ...n.data, attributes: update(n.data.attributes) } } : n,
      ),
    )

  // What an object is made of shows on the objects specializing it or made
  // up of it too, so the board loads again once it changed.
  const restructured = () => reload.current().catch(report)

  const addAttribute = async (objectId: string, name: string) => {
    if (readOnly) return false
    try {
      const attribute = await createAttribute(objectIdOf(objectId), { name, definition: '' })
      setAttributes(objectId, (as) => [...as, attribute])
      restructured()
      return true
    } catch (err) {
      report(err)
      return false
    }
  }

  const setQuantity = async (objectId: string, quantity: Quantity) => {
    if (readOnly) return false
    try {
      await updateObject(objectIdOf(objectId), quantity)
      updateObjectData(objectId, quantity)
      restructured()
      return true
    } catch (err) {
      report(err)
      return false
    }
  }

  // A person's links into processes become agent links, which the server
  // allows only into human processes.
  const setHuman = async (objectId: string, human: boolean) => {
    if (readOnly) return false
    try {
      await updateObject(objectIdOf(objectId), { human })
      updateObjectData(objectId, { human })
      return true
    } catch (err) {
      report(err)
      return false
    }
  }

  const setRelation = async (objectId: string, relation: Relation) => {
    if (readOnly) return false
    try {
      await updateObject(objectIdOf(objectId), { relation })
      restructured()
      return true
    } catch (err) {
      report(err)
      return false
    }
  }

  const setGeneral = async (objectId: string, general: number | null) => {
    if (readOnly) return false
    try {
      await updateObject(objectIdOf(objectId), { general })
      restructured()
      return true
    } catch (err) {
      report(err)
      return false
    }
  }

  const setPersistent = async (objectId: string, persistent: boolean) => {
    if (readOnly) return false
    try {
      await updateObject(objectIdOf(objectId), { persistent })
      updateObjectData(objectId, { persistent })
      return true
    } catch (err) {
      report(err)
      return false
    }
  }

  const setShared = async (objectId: string, shared: boolean) => {
    if (readOnly) return false
    try {
      await updateObject(objectIdOf(objectId), { shared })
      updateObjectData(objectId, { shared })
      return true
    } catch (err) {
      report(err)
      return false
    }
  }

  const changeAttribute = async (
    objectId: string,
    id: number,
    patch: { name?: string; definition?: string; unique?: boolean; file?: boolean },
  ) => {
    try {
      const attribute = await updateAttribute(id, patch)
      setAttributes(objectId, (as) => as.map((a) => (a.id === id ? attribute : a)))
      if (patch.name !== undefined || patch.file !== undefined) restructured()
      return true
    } catch (err) {
      report(err)
      return false
    }
  }

  const removeAttribute = async (objectId: string, id: number) => {
    const node = getNode(objectId)
    const attribute = node?.type === 'object' ? node.data.attributes.find((a) => a.id === id) : undefined
    if (!attribute || readOnly || !window.confirm(`Delete attribute “${attribute.name}”?`)) return false
    try {
      await deleteAttribute(id)
    } catch (err) {
      report(err)
      return false
    }
    setAttributes(objectId, (as) => as.filter((a) => a.id !== id))
    restructured()
    return true
  }

  const onNodeContextMenu: NodeMouseHandler<FlowNode> = (e, node) => {
    e.preventDefault()
    if (node.type === 'start' || node.type === 'end' || isDraft(node.id)) return
    // Viewing a run, a flow process, or the flow a subflow process runs, can still be opened.
    if (!readOnly || zoomable(node.id)) setMenu({ id: node.id, x: e.clientX, y: e.clientY })
  }

  const menuItems = (id: string): MenuItem[] => [
    ...(zoomable(id) ? [{ label: isObject(id) ? 'Unfold' : 'Zoom in', icon: <ZoomInIcon />, onSelect: () => zoomIn(id) }] : []),
    {
      label: 'Rename',
      disabled: readOnly || isCopied(id),
      icon: <PencilIcon />,
      onSelect: () => {
        // The panel holds its own copy of the name.
        setPanelId((open) => (open === id ? null : open))
        setEditing(id, true)
      },
    },
    ...(isObject(id) && (!objectBoard || isStructRoot(id))
      ? [
          {
            label: 'Add state',
            icon: <PlusIcon size={16} />,
            disabled: readOnly || isCopied(id),
            onSelect: () => updateObjectData(id, { addingState: true }),
          },
        ]
      : []),
    // Two objects making up one can't share a name.
    { label: 'Duplicate', icon: <CopyIcon />, disabled: readOnly || objectBoard || structBoard, onSelect: () => duplicate(id) },
    { label: 'Copy', icon: <CopyIcon />, disabled: readOnly || objectBoard || isBoundary(id), onSelect: () => copySelection(id) },
    {
      label: 'Delete',
      icon: <TrashIcon />,
      danger: true,
      disabled: readOnly || isBoundary(id) || isFixed(id) || isStructRoot(id),
      onSelect: () => remove(id),
    },
  ]

  const onStopStep = async (run: number, step: number) => {
    try {
      await stopStep(run, step)
    } catch (err) {
      report(err)
    }
    setRunReload((n) => n + 1)
  }

  // Stops the step the process has going in the open run.
  const stopProcess = (id: string) => {
    const step = shownRun?.steps.filter((s) => String(s.process) === id && s.status === 'active').at(-1)
    if (shownRun && step) onStopStep(shownRun.id, step.id)
  }

  // The step of the open run it is being re-run from; see RerunDialog.
  const [rerunning, setRerunning] = useState<number | null>(null)
  useEffect(() => setRerunning(null), [runId])

  // The process or object of the open run its panel is on; see showInRun.
  const [runFocus, setRunFocus] = useState<RunFocus | null>(null)
  useEffect(() => setRunFocus(null), [runId])

  // Re-runs the open run from the last step the process had in it.
  const rerunProcess = (id: string) => {
    const step = shownRun?.steps.filter((s) => String(s.process) === id && rerunnableFrom(shownRun, s)).at(-1)
    if (step) setRerunning(step.id)
  }

  // Nodes and edges reach the board through a context whose functions stay
  // the same, so they don't all re-render whenever the board does.
  const latest = useRef({ commitName, cancelName, remove, append, insert, addState, renameObjectState, removeState, setDecisionStates, setRole, setCondition, setQuantity, setRoute, resize, zoomIn, stopProcess, rerunProcess })
  latest.current = { commitName, cancelName, remove, append, insert, addState, renameObjectState, removeState, setDecisionStates, setRole, setCondition, setQuantity, setRoute, resize, zoomIn, stopProcess, rerunProcess }
  const actions = useMemo<BoardActions>(
    () => ({
      commitName: (id, name) => latest.current.commitName(id, name),
      cancelName: (id) => latest.current.cancelName(id),
      openPanel: (id) => setPanelId(id),
      zoomIn: (id) => latest.current.zoomIn(id),
      stopProcess: (id) => latest.current.stopProcess(id),
      rerunProcess: (id) => latest.current.rerunProcess(id),
      remove: (id) => latest.current.remove(id),
      append: (id) => latest.current.append(id),
      insert: (edgeId) => latest.current.insert(edgeId),
      addState: (objectId, name) => latest.current.addState(objectId, name),
      cancelAddState: (objectId) => updateObjectData(objectId, { addingState: false }),
      renameState: (objectId, stateId, name) => latest.current.renameObjectState(objectId, stateId, name),
      removeState: (objectId, stateId) => latest.current.removeState(objectId, stateId),
      setDecisionStates: (edgeId, states) => latest.current.setDecisionStates(edgeId, states),
      setRole: (edgeId, role) => latest.current.setRole(edgeId, role),
      setCondition: (edgeId, condition) => latest.current.setCondition(edgeId, condition),
      setQuantity: (objectId, quantity) => latest.current.setQuantity(objectId, quantity),
      setRoute: (edgeId, route, save) => latest.current.setRoute(edgeId, route, save),
      resize: (id, x, width) => latest.current.resize(id, x, width),
      editable: !readOnly,
      runShown,
    }),
    [readOnly, runShown, updateObjectData],
  )

  const startTarget = edges.find((e) => e.id === START_EDGE_ID)?.target
  // A run always runs the whole flow, from the top board.
  const canRun = (reviewing || boardId !== null || startTarget !== undefined) && !starting && !viewOnly

  // Starts a run of the board, which the server executes, and opens it. The
  // board shows the run on its version, which is the board as it is.
  const onPlay = async () => {
    setStarting(true)
    try {
      const run = await startRun(flowId)
      // Starting the run may have saved the board as a new version.
      refreshVersions()
      setRuns((rs) => [run, ...rs])
      showRun(run)
    } catch (err) {
      report(err)
    } finally {
      setStarting(false)
    }
  }

  // A re-run started from the open run; it opens as a run started with play
  // does, having saved the board as a new version if it changed.
  const onRerun = (run: FlowRun) => {
    refreshVersions()
    setRuns((rs) => [run, ...rs])
    showRun(run)
  }

  // Opens a run on the board it ran on: a recording flow process's run on
  // the flow process's board, a run of the whole flow, or of a flow a subflow
  // process ran, on the top board.
  const showRun = (run: { id: number; process: number | null }) => {
    setPanelId(null)
    setVersionsOpen(false)
    setRunId(run.id)
    setUnfolded(null)
    setBoardId(run.process)
  }

  // A run the list may not have yet, such as a flow process's that just
  // started. A run of another flow, such as one a subflow process ran, or
  // the run that ran this one, opens on that flow's page.
  const openRunById = async (id: number) => {
    let run = runs.find((r) => r.id === id)
    try {
      if (!run) {
        const rs = await listRuns(flowId)
        setRuns(rs)
        run = rs.find((r) => r.id === id)
      }
      if (!run) {
        const other = await getRun(id)
        if (other.flow !== flowId) window.location.hash = `#/flows/${other.flow}/runs/${id}`
        return
      }
    } catch (err) {
      report(err)
      return
    }
    showRun(run)
  }

  // The run asked for in the address opens once.
  const openedInitial = useRef(false)
  useEffect(() => {
    if (initialRun === null || openedInitial.current) return
    openedInitial.current = true
    openRunById(initialRun)
  })

  // The address names the open run, so going back to this flow opens it again.
  useEffect(() => {
    const want = runId === null ? `#/flows/${flowId}` : `#/flows/${flowId}/runs/${runId}`
    if (window.location.hash !== want) window.history.replaceState(null, '', want)
  }, [runId, flowId])

  // The selected processes, and the objects among the selection, move into a
  // new flow process, which is then named.
  const selectedProcesses = nodes.filter((n) => n.selected && n.type === 'process' && !isDraft(n.id))
  const onWrap = async () => {
    if (readOnly || selectedProcesses.length === 0) return
    try {
      const p = await wrapProcesses({
        name: 'New flow',
        processes: selectedProcesses.map((n) => Number(n.id)),
        objects: nodes.filter((n) => n.selected && n.type === 'object' && !n.data.boundary).map((n) => objectIdOf(n.id)),
      })
      setPanelId(null)
      await loadBoard()
      setEditing(String(p.id), true)
    } catch (err) {
      report(err)
    }
  }

  // Remembers the processes and objects selected, for pasting on any board
  // of any flow; from a node's menu, the selection it is in, or else the
  // node. The objects outside a flow process belong to the board outside.
  const copySelection = (id?: string) => {
    if (readOnly || objectBoard) return
    const copyable = (n: FlowNode) => !isDraft(n.id) && (n.type === 'process' || (n.type === 'object' && n.data.boundary === undefined))
    const inSelection = id === undefined || getNode(id)?.selected
    const chosen = getNodes().filter((n) => copyable(n) && (inSelection ? n.selected : n.id === id))
    if (chosen.length === 0) return
    const c: Copied = {
      flow: flowId,
      processes: chosen.filter((n) => n.type === 'process').map((n) => Number(n.id)),
      objects: chosen.filter((n) => n.type === 'object').map((n) => objectIdOf(n.id)),
      at: { x: Math.min(...chosen.map((n) => n.position.x)), y: Math.min(...chosen.map((n) => n.position.y)) },
    }
    writeCopied(c)
    setCopied(c)
  }

  // Pastes what was copied with its top left at a point on the screen: the
  // one given, where the pointer is over the board, or the board's middle.
  // The copies come selected.
  const pasteAt = async (screen?: XYPosition) => {
    const c = readCopied()
    const rect = wrapper.current?.getBoundingClientRect()
    const point = screen ?? pointer.current ?? (rect && { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 })
    if (!c || !point || readOnly || objectBoard || structBoard) return
    const at = screenToFlowPosition(point)
    try {
      const res = await pasteCopied(c, flowId, boardId, at.x - c.at.x, at.y - c.at.y)
      await loadBoard()
      const ids = new Set([...res.processes.map(String), ...res.objects.map(objectNodeId)])
      setNodes((ns) => ns.map((n) => ({ ...n, selected: ids.has(n.id) })))
    } catch (err) {
      report(err)
    }
  }

  // The board's history calls go one after another.
  const historyQueue = useRef<Promise<unknown>>(Promise.resolve())
  const queueHistory = useCallback(<T,>(call: () => Promise<T>): Promise<T> => {
    const next = historyQueue.current.then(call)
    historyQueue.current = next.catch(() => {})
    return next
  }, [])
  const recordTimer = useRef<number | undefined>(undefined)
  // The board as loaded is the step its history comes back to, and each
  // change after is a step of its own.
  useEffect(() => {
    let pending = false
    const record = () => {
      pending = false
      queueHistory(() => recordHistory(flowId)).catch(report)
    }
    record()
    const off = onChanged(() => {
      pending = true
      window.clearTimeout(recordTimer.current)
      recordTimer.current = window.setTimeout(record, HISTORY_DELAY_MS)
    })
    return () => {
      off()
      window.clearTimeout(recordTimer.current)
      if (pending) record()
    }
  }, [flowId, queueHistory, report])

  // Brings the board back a step of its history, or forward again, once the
  // changes sent are done; the server keeps a change not kept yet as the
  // step undone. Panels would save what they show over it, so they close
  // first, saving what they hold yet.
  const stepHistory = async (undo: boolean) => {
    if (readOnly) return
    flushSync(() => {
      setPanelId(null)
      setMenu(null)
    })
    window.clearTimeout(recordTimer.current)
    try {
      const h = await queueHistory(async () => {
        await new Promise((resolve) => window.setTimeout(resolve))
        await changesSettled()
        return undo ? undoHistory(flowId) : redoHistory(flowId)
      })
      if (h.changed) await loadBoard()
    } catch (err) {
      report(err)
    }
  }

  // Before the assistant is asked, panels close, saving what they hold yet,
  // and the changes sent are done, so it sees the board as it is. Once it
  // changed the flow, the board is loaded as the import left it, with the
  // version saved before; panels opened meanwhile close first.
  const closePanels = useCallback(
    () =>
      flushSync(() => {
        setPanelId(null)
        setMenu(null)
      }),
    [],
  )
  const beforeAssistant = useCallback(async () => {
    closePanels()
    await new Promise((resolve) => window.setTimeout(resolve))
    await changesSettled()
  }, [closePanels])
  const assistantApplied = useCallback(async () => {
    closePanels()
    window.clearTimeout(recordTimer.current)
    await Promise.all([loadBoard(), refreshVersions(), onReload()])
  }, [closePanels, loadBoard, refreshVersions, onReload])

  // Cmd or Ctrl and C copies the selection, and with V pastes it, but not
  // while typing or with text selected, which they copy and paste as ever.
  // With Z it undoes the latest change to the board, and with Shift and Z
  // redoes it, but not while typing, which they undo and redo as ever.
  const clipboard = useRef({ copySelection, pasteAt, stepHistory })
  clipboard.current = { copySelection, pasteAt, stepHistory }
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.altKey) return
      const target = e.target as HTMLElement | null
      if (target?.isContentEditable || target?.closest('input, textarea, select, dialog')) return
      const key = e.key.toLowerCase()
      if (key === 'z') {
        e.preventDefault()
        clipboard.current.stepHistory(!e.shiftKey)
      } else if (e.shiftKey) return
      else if (key === 'c' && !window.getSelection()?.toString()) clipboard.current.copySelection()
      else if (key === 'v') clipboard.current.pasteAt()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  // The boards from the top one down to the one shown, then the objects
  // unfolded down to the one shown.
  const showProcessBoard = (id: BoardId) => {
    setUnfolded(null)
    setBoardId(id)
  }
  // A struct has no board above its object.
  const trail: { key: string; name: string; show: () => void }[] = structBoard
    ? []
    : [{ key: 'top', name: 'Flow', show: () => showProcessBoard(null) }]
  for (let id = boardId; id !== null; ) {
    const p = contents?.processes.find((q) => q.id === id)
    const board = id
    trail.splice(1, 0, { key: `process-${id}`, name: p?.name ?? `Process ${id}`, show: () => showProcessBoard(board) })
    id = p?.parent ?? null
  }
  if (unfolded !== null) {
    for (const o of ownersOf(contents?.objects ?? [], unfolded)) {
      trail.push({ key: `object-${o.id}`, name: o.name, show: () => setUnfolded(o.id) })
    }
  }

  const onStop = async (id: number) => {
    try {
      const run = await stopRun(id)
      setRuns((rs) => rs.map((r) => (r.id === id ? run : r)))
      setRunReload((n) => n + 1)
    } catch (err) {
      report(err)
    }
  }

  // The states a human process can set: those its output links list.
  const decisionOptions = (id: string) =>
    edges.flatMap((e) => {
      const l = e.data?.link
      if (!l || l.direction !== 'output' || String(l.process) !== id) return []
      const object = getNode(objectNodeId(l.object))
      if (object?.type !== 'object') return []
      return object.data.states
        .filter((s) => l.states.includes(s.id))
        .map((s) => ({ state: s.id, object: object.data.name, name: s.name }))
    })

  // The objects a human process fills in: those its output links list no
  // states for, unless it takes them too, as a review does. Inside a flow
  // process, a list from outside is one item.
  const entryObjects = (id: string): EntryObject[] =>
    edges.flatMap((e) => {
      const l = e.data?.link
      if (!l || l.direction !== 'output' || String(l.process) !== id || l.states.length > 0) return []
      if (edges.some((i) => i.data?.link?.direction === 'input' && String(i.data.link.process) === id && i.data.link.object === l.object)) return []
      const object = getNode(objectNodeId(l.object))
      if (object?.type !== 'object') return []
      const many = object.data.many && !object.data.boundary
      return [{ id: l.object, name: object.data.name, many, min: object.data.min, max: object.data.max, fields: fieldsOf(contents?.objects ?? [], l.object) }]
    })

  // The names of the objects a process takes or generates, which a subflow
  // process hands to and from their namesakes in the flow it runs.
  // The process's input links naming no state to persistent or shared
  // objects, which it can take as queries of their stored records.
  const queryLinks = (id: string): QueryLink[] =>
    edges.flatMap((e) => {
      const l = e.data?.link
      if (!l || l.direction !== 'input' || String(l.process) !== id || l.state !== null || l.role === 'consumption') return []
      const object = getNode(objectNodeId(l.object))
      if (object?.type !== 'object' || !(object.data.persistent || object.data.shared) || object.data.human) return []
      return [{ link: l.id, object: object.data.name, states: object.data.states.map((s) => s.name), query: l.query }]
    })

  const linkedNames = (id: string, direction: 'input' | 'output'): string[] => [
    ...new Set(
      edges.flatMap((e) => {
        const l = e.data?.link
        if (!l || l.direction !== direction || String(l.process) !== id) return []
        const object = getNode(objectNodeId(l.object))
        return object?.type === 'object' ? [object.data.name] : []
      }),
    ),
  ]

  // Sends the decision on the review the process waits for in the open run,
  // which goes on in the server. What the person entered is checked there
  // first, so the form stays until the server takes it.
  const onDecide = async (id: string, state: number | null, instructions: string, entries?: Entries) => {
    const step = shownRun?.steps.filter((s) => String(s.process) === id && s.status === 'waiting').at(-1)
    if (!shownRun || !step) return false
    const option = decisionOptions(id).find((o) => o.state === state)
    const done = () =>
      updateData(id, {
        status: 'done',
        review: undefined,
        output: option ? decisionText(option.name, option.object, instructions) : instructions || (entries ? 'Filled in' : 'Reviewed'),
      })
    if (!entries) done()
    try {
      await decideStep(shownRun.id, step.id, { state, instructions, entries })
    } catch (err) {
      report(err)
      return false
    } finally {
      setRunReload((n) => n + 1)
    }
    if (entries) done()
    return true
  }

  // What a human process waiting in the open run shows to decide on.
  const reviewOf = (n: ProcessNodeType): Review | undefined => {
    if (!n.data.review) return undefined
    const step = shownRun?.steps.filter((s) => String(s.process) === n.id && s.status === 'waiting').at(-1)
    return {
      inputs: n.data.review,
      options: decisionOptions(n.id),
      entries: entryObjects(n.id),
      handledBy: step?.handledBy ?? [],
      canDecide: step?.canDecide ?? true,
      onDecide: (state, instructions, entries) => onDecide(n.id, state, instructions, entries),
    }
  }

  // Becoming or ceasing to be human drops the process's outputs, becoming
  // human its direct connections, and ceasing to be human its people's agent
  // links; the server deletes the same links when it saves the new kind.
  const dropMismatchedEdges = (id: string, from: ProcessKind, to: ProcessKind) => {
    // Only an agent decides between states; the others generate the object as it is.
    if (from === 'agent' && to !== 'agent' && to !== 'human') {
      setEdges((es) =>
        es.map((e) => {
          const l = e.data?.link
          if (!l || l.direction !== 'output' || String(l.process) !== id || l.states.length === 0) return e
          return { ...e, data: { ...e.data, link: { ...l, states: [] } } }
        }),
      )
      return
    }
    if ((from === 'human') === (to === 'human')) return
    const person = (object: number) => {
      const node = getNode(objectNodeId(object))
      return node?.type === 'object' && node.data.human
    }
    setEdges((es) =>
      es.filter((e) => {
        const l = e.data?.link
        if (l && String(l.process) === id && l.direction === 'input' && to !== 'human' && person(l.object)) return false
        if (l) return !(l.direction === 'output' && String(l.process) === id)
        return !(to === 'human' && e.id.startsWith(CONNECTION_PREFIX) && e.source === id)
      }),
    )
  }

  const panelColumn = useColumnWidth(PANEL_COLUMN)

  return (
    <div className="board">
      <header className="toolbar">
        <h1>
          <a className="home" href="#/" title="All flows">
            <FlowBuilderIcon /> Flows
          </a>
          <span className="crumb-separator" aria-hidden>
            /
          </span>
          <FlowName
            name={flow.name}
            // A struct's object is renamed with it.
            onRename={(name) => onUpdate({ name }).then(() => (structBoard ? reload.current() : undefined))}
            onError={report}
          />
        </h1>
        {!structBoard && (
          <button
            onClick={onWrap}
            disabled={readOnly || objectBoard || selectedProcesses.length === 0}
            title="Move the selected processes into a new flow process; shift-click or drag to select several"
          >
            <FlowIcon /> Group into flow
          </button>
        )}
        <button onClick={onTidy} disabled={readOnly} title="Lay the board out from the top">
          <LayoutIcon /> Tidy up
        </button>
        {structBoard ? (
          <span className="hint">
            A struct is one object that flows use. Add objects to give it nested fields, and make one a list in its
            settings. Double-click an object for its attributes; right-click the struct's to add states.
          </span>
        ) : (
          <span
            className="hint"
            title="Drag a building block or a connector from the sidebar onto the canvas, or click it to add one in the middle. Double-click the canvas to add a process, Shift+double-click to add an object. Use + on a link to insert a step, or below a node to add what comes next. Double-click an object to give it attributes, which agents generating it reply with as JSON; unfold it with its magnifier to build what it is made of, attributes and parts that are objects of their own, nested in what it holds, and pick in its settings an object it specializes, whose fields it has too; right-click it to add states; link a human process to an object and pick on the link the states it decides between, or select an agent's link to an object to pick states the agent decides between, readOnly the object if it takes it too, and link a state to the processes that take the object in it. Drag from an agent, script or Sentry process onto another process to run that one when it fails. Select an object's link into a process to pick how the process takes it: just takes it, uses it as an instrument it doesn't change, or consumes it; for a link from a state, whether the process runs when the object enters it, or only if the object is in it once the process is ready, being skipped otherwise. Select a link to drag its segments; double-click it to reset its route. Double-click a process to edit it, right-click a node to rename it and more; select a connection and press Delete to remove it. Cmd/Ctrl+C copies the selected processes and objects, with what is inside them, and Cmd/Ctrl+V pastes them where the pointer is, on this board or one of another flow; right-click the board to paste too."
          >
            Processes connect through the objects they generate and take. Right-click a node for more.
          </span>
        )}
        <div className="versioning">
          <button onClick={onSave} disabled={versioning || readOnly} title="Save the board as a new version">
            <SaveIcon /> Save
          </button>
          <button
            className={`version-toggle${versionsOpen ? ' open' : ''}`}
            aria-expanded={versionsOpen}
            onClick={() => {
              if (!versionsOpen) {
                setPanelId(null)
                setMCPOpen(false)
                setSettingsOpen(false)
                setOPLOpen(false)
                setCodeOpen(false)
                refreshVersions()
              }
              setVersionsOpen(!versionsOpen)
            }}
            title="Show every saved version"
          >
            <HistoryIcon />
            {versions.current === null ? 'Not saved' : `Version ${versions.current}`}
            {versions.versions.length > 0 && versions.current !== versions.versions[0].id && (
              <span className="version-latest">latest {versions.versions[0].id}</span>
            )}
          </button>
          {sharedRoot && (
            <button onClick={() => (window.location.hash = `#/flows/${flowId}/records`)} title="The records every flow using the struct wrote, a record per row">
              <TableIcon /> Records
            </button>
          )}
          <button
            className={`code-toggle${codeOpen ? ' open' : ''}`}
            aria-expanded={codeOpen}
            onClick={() => openCode(!codeOpen)}
            title="The flow as code, the setup file Export writes and Import reads"
          >
            <CodeIcon /> Code
          </button>
          {!structBoard && (
            <>
              <button
                className={`mcp-toggle${mcpOpen ? ' open' : ''}`}
                aria-expanded={mcpOpen}
                onClick={() => openMCPSettings(!mcpOpen)}
                title="The MCP servers agents can use"
              >
                <PlugIcon /> MCP servers
              </button>
              <button onClick={() => (window.location.hash = `#/flows/${flowId}/data`)} title="What each run held of each object, a run per row">
                <TableIcon /> Data
              </button>
              <button
                className={`opl-toggle${oplOpen ? ' open' : ''}`}
                aria-expanded={oplOpen}
                onClick={() => openOPL(!oplOpen)}
                title="The flow in sentences, in OPM's Object-Process Language"
              >
                OPL
              </button>
              <button
                className={`settings-toggle${settingsOpen ? ' open' : ''}`}
                aria-expanded={settingsOpen}
                onClick={() => openFlowSettings(!settingsOpen)}
                title="The repositories the flow works on, and its branches"
              >
                <GearIcon /> Settings
              </button>
            </>
          )}
        </div>
        {!structBoard && (
          <button
            className="play"
            onClick={onPlay}
            disabled={!canRun}
            aria-label="Run flow"
            title={
              starting ? 'Starting…' : canRun ? 'Run flow' : 'Connect the start node to a process first'
            }
          >
            <PlayIcon />
          </button>
        )}
      </header>
      {error && (
        <div className="error-banner" onClick={() => setError(null)}>
          {error} (click to dismiss)
        </div>
      )}
      <div className="workspace" style={{ '--panel-width': `${panelColumn.width}px` } as CSSProperties}>
        <Sidebar>
          <SidebarGroup id="blocks" title="Building blocks">
            {structBoard && !objectBoard ? null : (
              <BlockPalette
                blocks={objectBoard ? BUILDING_BLOCKS.filter((b) => b.type === 'object' && !b.human) : BUILDING_BLOCKS}
                disabled={readOnly}
                onAdd={addAtViewportCenter}
              />
            )}
          </SidebarGroup>
          {!objectBoard && !structBoard && (
            <>
              <SidebarGroup id="connectors" title="Connectors">
                <ConnectorPalette blocks={connectorTiles} disabled={readOnly} onAdd={addAtViewportCenter} />
              </SidebarGroup>
              <SidebarGroup id="flows" title="Procedures">
                <FlowPalette blocks={flowBlocks} disabled={readOnly} onAdd={addAtViewportCenter} />
              </SidebarGroup>
              <SidebarGroup id="structs" title="Structs">
                <StructPalette blocks={structBlocks} disabled={readOnly} onAdd={addAtViewportCenter} />
              </SidebarGroup>
            </>
          )}
          {structBoard && !objectBoard && sharedRoot && (
            <SidebarGroup id="records" title="Records" fill>
              <SharedRecords
                struct={flow.id}
                keyName={sharedRoot.data.attributes.find((a) => a.unique)?.name ?? null}
              />
            </SidebarGroup>
          )}
          {!structBoard && (
            <SidebarGroup id="runs" title="Runs" fill>
              <RunList
                runs={runs}
                selected={runId}
                onSelect={(id) => {
                  const run = runs.find((r) => r.id === id)
                  if (run) showRun(run)
                }}
              />
            </SidebarGroup>
          )}
        </Sidebar>
        <div
          className={`canvas${linkingTo ? ` linking-to-${linkingTo}` : ''}`}
          ref={wrapper}
          onMouseMove={(e) => (pointer.current = { x: e.clientX, y: e.clientY })}
          onMouseLeave={() => (pointer.current = null)}
          onDragOver={onCanvasDragOver}
          onDrop={onCanvasDrop}
        >
          <BoardActionsContext.Provider value={actions}>
            <StructFlowsContext.Provider value={structFlows}>
              <ReactFlow
                nodes={nodes}
                edges={edges}
                nodeTypes={nodeTypes}
                edgeTypes={edgeTypes}
                onNodesChange={onNodesChange}
                onEdgesChange={onEdgesChange}
                onConnect={onConnect}
                onConnectStart={onConnectStart}
                onConnectEnd={onConnectEnd}
                onEdgesDelete={onEdgesDelete}
                isValidConnection={isValidConnection}
                onNodeDoubleClick={onNodeDoubleClick}
                onNodeContextMenu={onNodeContextMenu}
                onPaneContextMenu={(e) => {
                  e.preventDefault()
                  if (!readOnly && !objectBoard && !structBoard) setPaneMenu({ x: e.clientX, y: e.clientY })
                }}
                onEdgeDoubleClick={(_, edge) => !readOnly && edge.data?.route && setRoute(edge.id, null, true)}
                onPaneClick={() => setPanelId(null)}
                onNodeDragStop={onNodeDragStop}
                onDoubleClick={onPaneDoubleClick}
                zoomOnDoubleClick={false}
                nodesDraggable={!readOnly}
                nodesConnectable={!readOnly && !objectBoard}
                deleteKeyCode={readOnly ? null : undefined}
              >
                <Background variant={BackgroundVariant.Dots} gap={18} size={1.5} />
                <Controls fitViewOptions={FIT_VIEW} />
              </ReactFlow>
            </StructFlowsContext.Provider>
          </BoardActionsContext.Provider>
          {trail.length > 1 && (
            <nav className="breadcrumb" aria-label="Boards">
              {trail.map((b, i) =>
                i === trail.length - 1 ? (
                  <span key={b.key} aria-current="page">
                    {b.name}
                  </span>
                ) : (
                  <span key={b.key}>
                    <button onClick={b.show}>{b.name}</button>
                    <span className="breadcrumb-sep" aria-hidden="true">
                      ›
                    </span>
                  </span>
                ),
              )}
            </nav>
          )}
          {viewOnly && !openRun && (
            <div className="review-banner" role="status">
              You decide this flow's reviews, so you see it, but only its owner{flow.owner.kind !== 'global' && `, ${flow.owner.name},`} changes it.
            </div>
          )}
          {reviewing && openRun && (
            <div className="review-banner" role="status">
              {openRun.status === 'running'
                ? `Run #${openRun.id} is running on version ${shownVersion}.`
                : `Run #${openRun.id} ran on version ${shownVersion}; this is the board as it was then.`}
              <button onClick={() => setRunId(null)}>Back to the board</button>
            </div>
          )}
          <Assistant
            flowId={flowId}
            disabled={readOnly}
            beforeSend={beforeAssistant}
            onApplied={assistantApplied}
            onError={report}
          />
          {menu && (
            <ContextMenu x={menu.x} y={menu.y} items={menuItems(menu.id)} onClose={() => setMenu(null)} />
          )}
          {paneMenu && (
            <ContextMenu
              x={paneMenu.x}
              y={paneMenu.y}
              items={[{ label: 'Paste', icon: <PasteIcon />, disabled: copied === null, onSelect: () => pasteAt(paneMenu) }]}
              onClose={() => setPaneMenu(null)}
            />
          )}
        </div>
        {panelOpen && (
          <ColumnResizer label="Resize panel" {...panelColumn} />
        )}
        {panelNode && (
          <ProcessPanel
            key={panelNode.id}
            id={Number(panelNode.id)}
            initial={{
              name: panelNode.data.name,
              kind: panelNode.data.kind,
              prompt: panelNode.data.prompt,
              script: panelNode.data.script,
              model: panelNode.data.model,
              thinking: panelNode.data.thinking,
              recording: panelNode.data.recording,
              settings: panelNode.data.settings,
            }}
            models={models}
            mcpServers={mcpServers}
            readOnly={readOnly}
            run={{ status: panelNode.data.status, output: panelNode.data.output, error: panelNode.data.runError }}
            // The form shows in one place at a time.
            review={reviewNode?.id === panelNode.id ? undefined : reviewOf(panelNode)}
            onChange={(fields) => {
              dropMismatchedEdges(panelNode.id, panelNode.data.kind, fields.kind)
              updateData(panelNode.id, fields)
            }}
            onZoomIn={() => zoomIn(panelNode.id)}
            flows={otherFlows}
            onFlowSaved={() => loadBoard().catch(report)}
            takes={linkedNames(panelNode.id, 'input')}
            queries={queryLinks(panelNode.id)}
            onQuery={(link, query) => void setQuery(link, query)}
            generates={linkedNames(panelNode.id, 'output')}
            shapes={Object.fromEntries(
              (contents?.objects ?? [])
                .filter((o) => edges.some((e) => e.data?.link && String(e.data.link.process) === panelNode.id && e.data.link.object === o.id))
                .map((o) => [o.name, shapeOf(fieldsOf(contents?.objects ?? [], o.id))]),
            )}
            onOpenMCPSettings={() => openMCPSettings(true)}
            repositories={flow.settings.repositories}
            onOpenFlowSettings={() => openFlowSettings(true)}
            onClose={() => setPanelId(null)}
            onError={report}
          />
        )}
        {objectPanelNode && (
          <ObjectPanel
            key={objectPanelNode.id}
            id={objectIdOf(objectPanelNode.id)}
            objects={contents?.objects ?? []}
            name={objectPanelNode.data.name}
            attributes={objectPanelNode.data.attributes}
            states={objectPanelNode.data.states}
            onStateRule={(id, rule) => changeStateRule(objectPanelNode.id, id, rule)}
            onRelationChange={(relation) => setRelation(objectPanelNode.id, relation)}
            generals={possibleGenerals(contents?.objects ?? [], objectIdOf(objectPanelNode.id))}
            onGeneralChange={(general) => setGeneral(objectPanelNode.id, general)}
            onUnfold={zoomable(objectPanelNode.id) ? () => unfold(objectIdOf(objectPanelNode.id)) : undefined}
            onUnfoldPart={unfold}
            persistent={objectPanelNode.data.persistent}
            onPersistentChange={(persistent) => setPersistent(objectPanelNode.id, persistent)}
            shared={objectPanelNode.data.shared}
            onSharedChange={(shared) => setShared(objectPanelNode.id, shared)}
            quantity={objectPanelNode.data}
            onQuantityChange={(quantity) => setQuantity(objectPanelNode.id, quantity)}
            human={objectPanelNode.data.human}
            onHumanChange={(human) => setHuman(objectPanelNode.id, human)}
            editable={!readOnly}
            module={moduleOf(objectIdOf(objectPanelNode.id))}
            structRoot={isStructRoot(objectPanelNode.id)}
            content={objectPanelNode.data.content}
            onRename={(name) => commitName(objectPanelNode.id, name)}
            onAdd={(name) => addAttribute(objectPanelNode.id, name)}
            onUpdate={(id, patch) => changeAttribute(objectPanelNode.id, id, patch)}
            onRemove={(id) => removeAttribute(objectPanelNode.id, id)}
            onClose={() => setPanelId(null)}
          />
        )}
        {/* A node being edited or reviewed takes the panel; the versions or run show again once it closes. */}
        {!panelNode && !objectPanelNode && mcpOpen && (
          <MCPSettingsPanel
            servers={mcpServers ?? []}
            canConfigure={configures(me)}
            onChanged={refreshMCPServers}
            onClose={() => setMCPOpen(false)}
            onError={report}
          />
        )}
        {!panelNode && !objectPanelNode && !mcpOpen && settingsOpen && (
          <FlowSettingsPanel
            flowId={flowId}
            owner={
              <OwnerPicker
                flow={flow}
                me={me}
                teams={teams}
                onChange={(owner) => setFlowOwner(flowId, owner).then(onReload).catch(report)}
              />
            }
            settings={flow.settings}
            module={flow.module}
            usedBy={flow.usedBy}
            defaultPrefix={defaultBranchPrefix(flow.name)}
            readOnly={readOnly}
            onSave={(settings) => onUpdate({ settings })}
            onModuleChange={(module) => onUpdate({ module })}
            onClose={() => setSettingsOpen(false)}
            onError={report}
          />
        )}
        {!panelNode && !objectPanelNode && !mcpOpen && !settingsOpen && oplOpen && (
          <OPLPanel flowId={flowId} onClose={() => setOPLOpen(false)} onError={report} />
        )}
        {!panelNode && !objectPanelNode && !mcpOpen && !settingsOpen && !oplOpen && codeOpen && (
          <CodePanel flowId={flowId} onClose={() => setCodeOpen(false)} onError={report} />
        )}
        {!panelNode && !objectPanelNode && !mcpOpen && !settingsOpen && !oplOpen && !codeOpen && versionsOpen && (
          <VersionPanel
            versions={versions.versions}
            current={versions.current}
            disabled={versioning}
            onRestore={onRestore}
            onClose={() => setVersionsOpen(false)}
          />
        )}
        {!panelNode && !objectPanelNode && !mcpOpen && !settingsOpen && !oplOpen && !codeOpen && !versionsOpen && runDetail && (
          <RunPanel
            run={runDetail}
            onStop={() => onStop(runDetail.id)}
            onStopStep={(step) => onStopStep(runDetail.id, step)}
            focus={runFocus}
            onClearFocus={() => setRunFocus(null)}
            onOpenRun={openRunById}
            onRerun={setRerunning}
            onChanged={() => setRunReload((n) => n + 1)}
            onClose={() => setRunId(null)}
          />
        )}
      </div>
      {rerunning !== null && runDetail && (
        <RerunDialog
          run={runDetail.id}
          step={rerunning}
          within={runDetail.parent !== null ? { name: runDetail.processName, run: runDetail.parent } : undefined}
          onStarted={(r) => {
            setRerunning(null)
            onRerun(r)
          }}
          onClose={() => setRerunning(null)}
        />
      )}
      {reviewNode && (
        <ReviewDialog
          key={reviewNode.id}
          name={reviewNode.data.name}
          review={reviewOf(reviewNode)!}
          onClose={() => setReviewId(null)}
        />
      )}
    </div>
  )
}

// The flow's name, which a click on the pencil edits; Enter or leaving the
// field saves it, Escape keeps the old one.
function FlowName({
  name,
  onRename,
  onError,
}: {
  name: string
  onRename: (name: string) => Promise<void>
  onError: (err: unknown) => void
}) {
  const [draft, setDraft] = useState<string | null>(null)
  if (draft === null) {
    return (
      <span className="flow-name">
        {name}
        <button className="icon-button" onClick={() => setDraft(name)} aria-label="Rename flow" title="Rename flow">
          <PencilIcon />
        </button>
      </span>
    )
  }
  const save = () => {
    const next = draft.trim()
    setDraft(null)
    if (next && next !== name) onRename(next).catch(onError)
  }
  return (
    <input
      className="flow-name-input"
      aria-label="Flow name"
      autoFocus
      value={draft}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={save}
      onKeyDown={(e) => {
        if (e.key === 'Enter') save()
        if (e.key === 'Escape') setDraft(null)
      }}
    />
  )
}
