import { useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react'
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
import { useConfirm } from './Confirm'
import { useUndoNotice } from './UndoNotice'
import {
  CodeIcon,
  CopyIcon,
  FlowBuilderIcon,
  FlowIcon,
  GearIcon,
  HistoryIcon,
  PublishIcon,
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
  StructPalette,
  type Block,
} from './Palette'
import { decisionText, formatTime, ORIGIN_LABELS, rerunnableFrom, RunList, RunPanel, STANDING_LABELS, type RunFocus } from './Runs'
import RerunDialog from './RerunDialog'
import TrialDialog from './TrialDialog'
import RunLogs from './RunLogs'
import RunTimeline from './RunTimeline'
import BoardFind from './BoardFind'
import { findOnBoards, placeOf, type FindMatch, type Place } from './find'
import { runItem, usePaletteSource, type PaletteItem } from './CommandPalette'
import { MAC, useShortcuts } from './Keyboard'
import { shortcutText } from './shortcuts'
import OPLPanel from './OPLPanel'
import CodePanel from './CodePanel'
import Assistant from './Assistant'
import { Sidebar, SidebarGroup } from './Sidebar'
import { SharedRecords } from './Records'
import { NoteDialog, VersionPanel } from './Versions'
import { ProblemCount } from './Problems'
import { BlocksHint, BoardTips, EmptyBoardGuide, useHints, type BoardTip } from './Hints'
import { DiffPanel, diffCount, highlightsOf, noHighlights, type Highlights } from './Diff'
import type { Quantity } from './quantity'
import { MCPSettingsPanel } from './MCPSettings'
import { configures, useMe } from './Account'
import { OwnerPicker } from './Owner'
import { onCopiedElsewhere, readCopied, writeCopied } from './clipboard'
import { FlowSettingsPanel } from './FlowSettings'
import { keepTabView, openTab, TabContext, tabView, type TabView } from './tabs'
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
  answerLoop,
  decideStep,
  deleteState,
  getRun,
  getEnd,
  listFlows,
  getStart,
  getProblems,
  diffVersions,
  getVersionBoard,
  listConnections,
  listLinks,
  listModels,
  listMCPServers,
  listSkills,
  listFlowCredentials,
  listBoards,
  listObjects,
  listProcesses,
  listRuns,
  RUN_PAGE,
  runMatches,
  listVersions,
  changesSettled,
  onChanged,
  pasteCopied,
  recordHistory,
  redoHistory,
  renameState,
  publishBoard,
  publishVersion,
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
  unpublish,
  stopRun,
  stopStep,
  updateAttribute,
  updateEnd,
  updateObject,
  updateProcess,
  updateStart,
  wrapProcesses,
  type BoardContents,
  type BoardDiff,
  type BoardId,
  type Connection,
  type Copied,
  type FlowObject,
  defaultBranchPrefix,
  type Flow,
  type FlowProblems,
  type Problem,
  type FlowRun,
  type RunFilter,
  type Versions,
  type MCPServer,
  type Skill,
  type CredentialChoice,
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

// Going to what was found zooms in to at least this, to read it.
const FIND_ZOOM = 0.75

// How many of the flow's latest runs the palette offers to compare a run with.
const COMPARE_RUNS = 50

// The state a handle of an object node belongs to, if any.
const stateOfHandle = (handle: string | null | undefined): number | null => {
  const m = handle?.match(/^state-(?:in|out)-(\d+)$/)
  return m ? Number(m[1]) : null
}

const isDraft = (id: string) => id.startsWith(DRAFT_PREFIX)
const isObject = (id: string) => id.startsWith(OBJECT_PREFIX)
const isProcess = (id: string) => id !== START_ID && id !== END_ID && !isObject(id) && !isDraft(id)

const objectNodeId = (id: number) => `${OBJECT_PREFIX}${id}`

// How many things a comparison found changed, as its banner says it.
const changesText = (n: number) => (n === 0 ? 'Nothing changed but the layout, if anything' : n === 1 ? 'One change' : `${n} changes`)
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
// new board, showing the run given, if any, with the review of the step
// given showing, as the inbox opens one.
export default function Board({
  flow,
  initialRun,
  initialStep = null,
  onUpdate,
  onReload,
}: {
  flow: Flow
  initialRun: number | null
  initialStep?: number | null
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
  // The run and the step whose review the address asked for as the board
  // opened, kept as the board names its open run in the address instead.
  const [askedRun] = useState(initialRun)
  const [askedStep] = useState(initialStep)
  const [models, setModels] = useState<Models | null>(null)
  const me = useMe()
  // The teams, to give the flow to one; see Owner.tsx.
  const [teams, setTeams] = useState<Team[]>([])
  const [mcpServers, setMCPServers] = useState<MCPServer[] | null>(null)
  // The skills and commands the flow's agents may use; see Skills.tsx.
  const [skills, setSkills] = useState<Skill[] | null>(null)
  const [commands, setCommands] = useState<Skill[] | null>(null)
  // The credentials the flow's agents and scripts may use; see Credentials.tsx.
  const [credentials, setCredentials] = useState<CredentialChoice[] | null>(null)
  const [mcpOpen, setMCPOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  const [oplOpen, setOPLOpen] = useState(false)
  const [codeOpen, setCodeOpen] = useState(false)
  // Every other flow, which this one can run; null while they load.
  const [otherFlows, setOtherFlows] = useState<Flow[] | null>(null)
  // The runs the list shows: those its filter picks, newest first, as many
  // pages as are loaded, and whether older ones may be left to load.
  const [runs, setRuns] = useState<FlowRun[]>([])
  const [runFilter, setRunFilter] = useState<RunFilter>(() => loadRunFilter(flowId))
  const [moreRuns, setMoreRuns] = useState(false)
  const [loadingRuns, setLoadingRuns] = useState(true)
  // The run open in the side panel, and its steps once loaded. The list
  // may not have it, filtered out or older than it loaded, so it is kept
  // as it opened too, for the version it ran on.
  const [runId, setRunId] = useState<number | null>(null)
  const [openedRun, setOpenedRun] = useState<FlowRun | null>(null)
  // The run whose comparison waits for the other run to be picked in the list.
  const [comparing, setComparing] = useState<number | null>(null)
  const [runDetail, setRunDetail] = useState<FlowRunDetail | null>(null)
  // Bumped to load the open run again straight away, such as after a decision.
  const [runReload, setRunReload] = useState(0)
  const [versions, setVersions] = useState<Versions>({ current: null, published: null, publishedAt: null, publishedBy: '', draft: false, versions: [] })
  const [versionsOpen, setVersionsOpen] = useState(false)
  // Which note the toolbar asks for: to save the board, or to publish it.
  const [noting, setNoting] = useState<'save' | 'publish' | null>(null)
  // What the board compares, highlighting what changed: from a version to
  // the board as it is, or to another version, which the board shows
  // instead; after the assistant changed the flow, from the version saved
  // before. Listed says whether its panel lists the changes. See Diff.tsx.
  const [compare, setCompare] = useState<{ from: number; to: number | null; assistant?: boolean } | null>(null)
  const [diff, setDiff] = useState<BoardDiff | null>(null)
  const [diffListed, setDiffListed] = useState(false)
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
  const { getNode, getNodes, getInternalNode, screenToFlowPosition, fitView, getViewport, setCenter, getZoom, deleteElements } = useReactFlow<FlowNode>()

  // Coming back to its tab, the board opens where it was looked at, rather
  // than moving to show all of it.
  const tab = useContext(TabContext)
  const [savedView] = useState(() => tabView(tab, flowId))
  // Until the board is shown, there is nothing of it to keep.
  const viewShown = useRef(false)
  // Set while finding goes to a match on another board, which is centred
  // once it shows, so the board isn't fitted into view over it.
  const centring = useRef(false)
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
        trashedFlow: p.trashedFlow,
      },
    }),
    [],
  )

  // The newest page of runs the filter picks, in place of the runs the list
  // had from then on; older pages loaded stay, with whether more are left,
  // unless the page holds them all. An answer for another filter is dropped.
  const runPages = useRef({ filter: runFilter, loaded: 1 })
  const refreshRuns = useCallback(() => {
    const asked = runPages.current
    return listRuns(flowId, runFilter)
      .then((page) => {
        if (runPages.current !== asked) return
        const full = page.length >= RUN_PAGE
        const oldest = page.at(-1)?.id ?? Infinity
        setRuns((rs) => (full ? [...page, ...rs.filter((r) => r.id < oldest)] : page))
        if (!full || asked.loaded === 1) {
          asked.loaded = 1
          setMoreRuns(full)
        }
      })
      .catch(report)
      .finally(() => setLoadingRuns(false))
  }, [report, flowId, runFilter])

  // Another filter shows its own runs, from the newest.
  const filterRuns = (f: RunFilter) => {
    runPages.current = { filter: f, loaded: 1 }
    setRuns([])
    setMoreRuns(false)
    setLoadingRuns(true)
    setRunFilter(f)
    saveRunFilter(flowId, f)
  }

  const loadOlderRuns = () => {
    const asked = runPages.current
    const oldest = runs.at(-1)?.id
    if (oldest === undefined) return
    setLoadingRuns(true)
    listRuns(flowId, runFilter, oldest)
      .then((page) => {
        if (runPages.current !== asked) return
        asked.loaded++
        setRuns((rs) => [...rs, ...page.filter((r) => r.id < (rs.at(-1)?.id ?? Infinity))])
        setMoreRuns(page.length >= RUN_PAGE)
      })
      .catch(report)
      .finally(() => setLoadingRuns(false))
  }

  useEffect(() => {
    refreshRuns()
  }, [refreshRuns])

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
  // What the flow may use as its owner may: skills, commands and credentials.
  const refreshSkills = useCallback(() => {
    listSkills('skill', flowId).then(setSkills).catch(report)
    listSkills('command', flowId).then(setCommands).catch(report)
    listFlowCredentials(flowId).then(setCredentials).catch(report)
  }, [report, flowId])

  // A sign-in window says when it is done; coming back to this window after
  // one that couldn't say so shows the outcome too.
  useEffect(() => {
    const onMessage = (e: MessageEvent) => {
      if (e.origin === window.location.origin && e.data?.type === 'mcp-sign-in') refreshMCPServers()
    }
    // Skills, commands and credentials written in another tab show once back.
    const onFocus = () => {
      refreshMCPServers()
      refreshSkills()
    }
    window.addEventListener('message', onMessage)
    window.addEventListener('focus', onFocus)
    return () => {
      window.removeEventListener('message', onMessage)
      window.removeEventListener('focus', onFocus)
    }
  }, [refreshMCPServers, refreshSkills])

  useEffect(() => {
    listModels().then(setModels).catch(report)
    listFlows()
      .then((fs) => setOtherFlows(fs.filter((f) => f.id !== flowId)))
      .catch(report)
    refreshMCPServers()
    refreshSkills()
    refreshVersions()
    // The flow just loaded; show all of it once its nodes are measured.
    loadBoard()
      .then(() => {
        viewShown.current = true
        if (!savedView) window.setTimeout(() => fitView(FIT_VIEW), BOARD_FIT_DELAY_MS)
      })
      .catch(report)
  }, [loadBoard, report, refreshVersions, refreshMCPServers, refreshSkills, fitView, flowId, savedView])

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

  // Does something to the flow's versions, which are read again after.
  const versionChange = async (change: () => Promise<unknown>) => {
    setVersioning(true)
    try {
      await change()
      await refreshVersions()
    } catch (err) {
      report(err)
    } finally {
      setVersioning(false)
    }
  }
  const onSave = (note: string) => versionChange(() => saveVersion(flowId, note))
  // Publishing saves the board as a version if it changed; see publish.go.
  const onPublish = (note: string) => versionChange(() => publishBoard(flowId, note))
  const onPublishVersion = (id: number) => versionChange(() => publishVersion(id))
  const onUnpublish = () => versionChange(() => unpublish(flowId))

  // Brings the board back to a saved version. The run shown on the board
  // belongs to the board it left, so it closes, as does a comparison with
  // the board.
  const onRestore = async (id: number) => {
    setVersioning(true)
    try {
      await restoreVersion(id)
      setPanelId(null)
      setMenu(null)
      setRunId(null)
      setCompare(null)
      await Promise.all([loadBoard(), refreshVersions()])
    } catch (err) {
      report(err)
    } finally {
      setVersioning(false)
    }
  }

  // Compares a version with another, or with the board as it is, listing
  // what changed; the board shows the newer one.
  const onCompare = (from: number, to: number | null) => {
    setPanelId(null)
    setMenu(null)
    setVersionsOpen(false)
    setCompare({ from, to })
    setDiffListed(true)
  }

  // The diff of what the board compares, worked out again when the board
  // it compares with changes.
  const [diffSeq, setDiffSeq] = useState(0)
  useEffect(() => setDiff(null), [compare])
  useEffect(() => {
    if (!compare) return
    let stale = false
    diffVersions(compare.from, compare.to)
      .then((d) => !stale && setDiff(d))
      .catch(report)
    return () => {
      stale = true
    }
  }, [compare, diffSeq, report])
  // The assistant's changes are highlighted until the person changes the
  // board; saving or publishing it changes nothing on it.
  useEffect(() => {
    if (!compare?.assistant) return
    return onChanged((url) => {
      if (!/^\/api\/versions(\?|\/\d+\/publish|\/published)/.test(url)) setCompare(null)
    })
  }, [compare])
  // What is wrong with the board as it is, as the server works it out,
  // read again as each change is kept; see Problems.tsx. Refusing says the
  // list opened as Run was refused for its errors.
  const [problems, setProblems] = useState<FlowProblems | null>(null)
  const [problemsOpen, setProblemsOpen] = useState(false)
  const [refusing, setRefusing] = useState(false)
  const refreshProblems = useCallback(
    () =>
      getProblems(flowId).then(
        (found) => {
          setProblems(found)
          return found
        },
        () => null,
      ),
    [flowId],
  )
  useEffect(() => {
    if (!structBoard) refreshProblems()
  }, [structBoard, refreshProblems])
  const openProblems = (open: boolean) => {
    setRefusing(false)
    setProblemsOpen(open)
    if (open) refreshProblems()
  }

  // Once a change is kept: whether the board differs from what is
  // published is read again, and a comparison with the board worked out
  // again. Undoing or redoing ends showing what the assistant changed.
  const boardChanged = useRef((_undone: boolean) => {})
  boardChanged.current = (undone) => {
    if (!structBoard) refreshProblems()
    if (versions.published !== null) refreshVersions()
    if (compare?.assistant && undone) setCompare(null)
    else if (compare?.to === null) setDiffSeq((n) => n + 1)
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
        const current = (r: FlowRun) => (r.id === detail.id ? { ...r, status: detail.status, waiting: detail.waiting, finishedAt: detail.finishedAt } : r)
        setRuns((rs) => rs.map(current))
        setOpenedRun((r) => r && current(r))
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

  // Runs go on in the server whether open or not, so the list keeps up with
  // them; a filtered list, which may leave out those going, keeps asking.
  const anyRunning = runs.some((r) => r.status === 'running') || runFilter.status.length > 0 || runFilter.started.length > 0
  useEffect(() => {
    if (!anyRunning) return
    const timer = window.setInterval(refreshRuns, RUN_LIST_REFRESH_MS)
    return () => window.clearInterval(timer)
  }, [anyRunning, refreshRuns])

  // A run shows on the board it runs or ran on, its version, which can't be
  // edited while the run is open. Runs from before versions were kept show
  // on the board as it is.
  const shownRun = runDetail?.id === runId ? runDetail : null
  const openRun = runs.find((r) => r.id === runId) ?? (openedRun?.id === runId ? openedRun : undefined)
  // The trial open stays listed, though the filter leaves trials out.
  const listedRuns =
    openRun && openRun.trialOf !== null && !runs.some((r) => r.id === openRun.id) ? [...runs, openRun].sort((x, y) => y.id - x.id) : runs
  const runVersion = openRun?.version ?? null
  // Comparing shows the newer board instead: the version compared to, or
  // the board as it is.
  const viewVersion = compare ? compare.to : runVersion
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
    if (viewVersion === shownVersion && boardId === shownBoard && unfolded === shownUnfolded) return
    let stale = false
    ;(viewVersion === null ? fetchBoard(flowId) : getVersionBoard(flowId, viewVersion))
      .then((c) => {
        if (stale) return
        setPanelId(null)
        setMenu(null)
        showBoard(c, boardId, unfolded)
        setShownVersion(viewVersion)
        setShownBoard(boardId)
        setShownUnfolded(unfolded)
        // Another board is a different place; show all of it once its nodes are measured.
        if ((boardId !== shownBoard || unfolded !== shownUnfolded) && !centring.current) {
          window.setTimeout(() => fitView(FIT_VIEW), BOARD_FIT_DELAY_MS)
        }
      })
      .catch(report)
    return () => {
      stale = true
    }
  }, [viewVersion, shownVersion, boardId, shownBoard, unfolded, shownUnfolded, showBoard, fitView, report, flowId])

  // Shows the open run on the board: each process as its last step in the
  // run left it, each object with the content and state it last got. A
  // review waiting for a decision shows in the middle of the screen, unless
  // another does, and opens its process's panel, unless another panel is open.
  useEffect(() => {
    if (!shownRun || compare || shownVersion !== runVersion) return
    const steps = new Map(shownRun.steps.flatMap((s) => (s.process === null ? [] : [[String(s.process), s] as const])))
    const rerunnable = new Set(shownRun.steps.flatMap((s) => (rerunnableFrom(shownRun, s) ? [String(s.process)] : [])))
    const objects = new Map(shownRun.objects.flatMap((o) => (o.object === null ? [] : [[objectNodeId(o.object), o] as const])))
    // A review inside a flow process opens once its board shows; the one
    // the address asked for over any other.
    for (const [process, s] of steps) {
      if (s.status !== 'waiting' || shownReviews.current.has(s.id) || !getNode(process)) continue
      shownReviews.current.add(s.id)
      const asked = s.id === askedStep && shownRun.id === askedRun
      setReviewId((open) => (asked ? process : (open ?? process)))
      setPanelId((open) => (asked ? process : (open ?? process)))
    }
    setNodes((ns) =>
      ns.map((n): FlowNode => {
        if (n.type === 'process') {
          const s = steps.get(n.id)
          const output =
            s?.kind === 'human' && s.outcome
              ? decisionText(s.outcome, shownRun.objects.find((o) => o.step === s.id)?.name, s.instructions)
              : s?.output
          const review = (s?.kind === 'human' || s?.kind === 'loop') && s.status === 'waiting' ? s.inputs : undefined
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
  }, [shownRun, compare, shownVersion, runVersion, shownSeq, getNode, setNodes])

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
    () => (mcpServers === null ? null : connectorBlocks(mcpServers)),
    [mcpServers],
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
    return node.data.kind === 'flow' || (node.data.kind === 'subflow' && node.data.settings.flow !== undefined && node.data.trashedFlow === undefined)
  }

  // Shows the object unfolded, under the board of the object it makes up.
  const unfold = (id: number) => {
    setPanelId(null)
    setMenu(null)
    const top = ownersOf(contents?.objects ?? [], id)[0]
    if (top && top.parent !== boardId && (top.parent === null || contents?.boards.some((b) => b.process === top.parent))) setBoardId(top.parent)
    setUnfolded(id)
  }

  // Zooming into a subflow process opens the flow it runs in a new tab, on
  // the run it ran there if a run is shown.
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
      openTab(child ? `#/flows/${target}/runs/${child}` : `#/flows/${target}`)
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
  // The run's panel opening again as the board comes back to its tab leaves
  // it where it was kept, with the panel beside it.
  const panelWasOpen = useRef(panelOpen)
  const panelComingBack = useRef(savedView?.panel === true)
  useEffect(() => {
    if (panelOpen === panelWasOpen.current) return
    panelWasOpen.current = panelOpen
    const comingBack = panelComingBack.current && panelOpen
    panelComingBack.current = false
    if (!comingBack && !centring.current) window.setTimeout(() => fitView(FIT_VIEW), BOARD_FIT_DELAY_MS)
  }, [panelOpen, fitView])

  // Where the top board is looked at is kept for its tab as it moves and as
  // the board leaves, as when another tab opens; on another board, the top
  // one is shown whole when the tab comes back.
  const onTop = boardId === null && unfolded === null
  const viewOf = useRef(() => null as TabView | null)
  useEffect(() => {
    viewOf.current = () => (onTop ? { flow: flowId, ...getViewport(), panel: panelOpen && runId !== null } : null)
  })
  useEffect(() => () => void (viewShown.current && keepTabView(tab, viewOf.current())), [tab])

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
    // A process dropped on another handles its failure, if it can fail: a
    // person's past its time limit.
    if (isProcess(source) && isProcess(target)) {
      const node = getNode(source)
      const fails = node?.type === 'process' && ['agent', 'script', 'connector', 'human'].includes(node.data.kind)
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
  // Whether it is drawn from a state, which a tip explains.
  const [fromState, setFromState] = useState(false)
  const onConnectStart: OnConnectStart = (_, { nodeId, handleId, handleType }) => {
    if (!nodeId) return
    setFromState(stateOfHandle(handleId) !== null)
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
    setFromState(false)
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

  const onEdgesDelete: OnEdgesDelete<FlowEdge> = (gone) => {
    const told = removing.current
    const reqs = gone.map((e) =>
      e.id === START_EDGE_ID
        ? updateStart(flowId, boardId, { target: null })
        : e.id.startsWith(END_EDGE_PREFIX)
          ? updateObject(Number(e.id.slice(END_EDGE_PREFIX.length)), { end: false })
          : e.id.startsWith(LINK_PREFIX)
            ? deleteLink(Number(e.id.slice(LINK_PREFIX.length)))
            : deleteConnection(Number(e.id.slice(CONNECTION_PREFIX.length))),
    )
    Promise.all(reqs)
      .then(() => queueHistory(() => recordHistory(flowId)))
      .then((h) => {
        held.current = h.held
        if (!told) deleted(gone.length === 1 ? 'the link' : `${gone.length} links`)
      }, report)
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

  // Deleting from the board can be undone, so it asks nothing: a notice
  // says what went, offering to undo it, until the next change, which undoing
  // would undo instead. A run of the board as it is, a trial's too, holds it,
  // refusing undo (lockBoard), so while one goes deleting asks first; the
  // board's history says whether one does, as the run list may not show it.
  const [confirm, confirmDialog] = useConfirm()
  const [showUndo, dismissUndo, undoNotice] = useUndoNotice()
  useEffect(() => onChanged(dismissUndo), [dismissUndo])
  const held = useRef(false)
  const mayDelete = async (what: string, goes: string) => {
    try {
      held.current = (await queueHistory(() => recordHistory(flowId))).held
    } catch {
      held.current = runs.some((r) => r.status === 'running' && r.version !== versions.published)
    }
    return (
      !held.current ||
      confirm({
        title: `Delete ${what}?`,
        message: (
          <p>
            {goes && `${goes} `}A run of the board is going on, so this can't be undone until it ends.
          </p>
        ),
        confirm: 'Delete',
      })
    )
  }
  const deleted = (what: string) => {
    if (!held.current) showUndo({ message: `Deleted ${what}`, undo: () => stepHistory(true) })
  }
  // While processes and objects are being deleted, the links deleted with
  // them are told of in their notice rather than one of their own.
  const removing = useRef(false)

  // Whether a node can be deleted: not the start or end, an object outside a
  // flow process, which belongs to the board outside, one shown beside an
  // unfolded object, or a struct's own object, which goes only with it.
  const removable = (id: string) => {
    const node = getNode(id)
    return (
      node !== undefined &&
      node.type !== 'start' &&
      node.type !== 'end' &&
      !isDraft(id) &&
      !readOnly &&
      !isBoundary(id) &&
      !isFixed(id) &&
      !isStructRoot(id)
    )
  }

  // Their links go with them on the server, and a flow process's board with
  // all on it; runs keep their record of them. They go at once, with a
  // notice offering to undo it, or while a run holds the board once asked;
  // then whatever else was to go with them goes too (confirmed), such as the
  // links selected beside them, links many, told of in the same notice.
  const removeNodes = async (ids: string[], confirmed?: () => void, links = 0) => {
    let chosen = ids.filter(removable).map((id) => getNode(id)!)
    // What an object deleted is made of goes with it.
    if (objectBoard) {
      const all = new Set(chosen.map((n) => n.id))
      chosen = chosen.filter((n) => !ownersOf(contents?.objects ?? [], objectIdOf(n.id)).some((o) => all.has(objectNodeId(o.id))))
    }
    if (chosen.length === 0) return
    const node = chosen[0]
    const one = chosen.length === 1
    const flowsInside = chosen.some((n) => n.type === 'process' && n.data.kind === 'flow')
    const goes =
      one && flowsInside
        ? 'Its links go with it, and everything on its board.'
        : one && node.type === 'object' && objectBoard
          ? 'Everything it is made of goes with it.'
          : one
            ? 'Its links go with it.'
            : flowsInside
              ? 'Their links go with them, and everything on their boards.'
              : objectBoard
                ? 'Everything they are made of goes with them.'
                : 'Their links go with them.'
    const what =
      (one && node.type !== 'start' && node.type !== 'end' ? `“${node.data.name}”` : `${chosen.length} processes and objects`) +
      (links === 0 ? '' : links === 1 ? ' and a link' : ` and ${links} links`)
    if (!(await mayDelete(what, goes))) return
    removing.current = true
    const gone = new Set<string>()
    try {
      confirmed?.()
      for (const n of chosen) {
        try {
          if (n.type === 'object') await deleteObject(objectIdOf(n.id))
          else await deleteProcess(Number(n.id))
          gone.add(n.id)
        } catch (err) {
          report(err)
          break
        }
      }
    } finally {
      removing.current = false
    }
    setNodes((ns) => ns.filter((n) => !gone.has(n.id)))
    setEdges((es) => es.filter((e) => !gone.has(e.source) && !gone.has(e.target)))
    setPanelId((open) => (open !== null && gone.has(open) ? null : open))
    // The object they made up has fields less.
    if (objectBoard) reload.current().catch(report)
    if (gone.size > 0) deleted(what)
  }
  const remove = (id: string) => removeNodes([id])

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
    if (!state || readOnly || !(await mayDelete(`the state “${state.name}”`, 'The links naming it go with it.'))) return
    try {
      await deleteState(stateId)
    } catch (err) {
      report(err)
      return
    }
    deleted(`the state “${state.name}”`)
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
    if (!attribute || readOnly || !(await mayDelete(`the attribute “${attribute.name}”`, 'What runs stored of it stays with them.'))) return false
    try {
      await deleteAttribute(id)
    } catch (err) {
      report(err)
      return false
    }
    deleted(`the attribute “${attribute.name}”`)
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

  // The process being tried on its own; see TrialDialog.
  const [trying, setTrying] = useState<number | null>(null)
  // The step of the open run it is being re-run from; see RerunDialog.
  const [rerunning, setRerunning] = useState<number | null>(null)
  useEffect(() => setRerunning(null), [runId])

  // The process or object of the open run its panel is on; see showInRun.
  const [runFocus, setRunFocus] = useState<RunFocus | null>(null)
  useEffect(() => setRunFocus(null), [runId])

  // Opens a step from the run's timeline in the run's panel: of the open
  // run, or of a run it ran as, which opens first.
  const [pendingStep, setPendingStep] = useState<{ run: number; step: number } | null>(null)
  const openStep = (run: number, step: number) => {
    setPendingStep({ run, step })
    if (run !== runId) openRunById(run)
  }
  useEffect(() => {
    if (!shownRun || pendingStep?.run !== shownRun.id) return
    setPendingStep(null)
    const s = shownRun.steps.find((s) => s.id === pendingStep.step)
    if (s) setRunFocus((f) => ({ kind: 'step', id: s.id, name: s.processName, seq: (f?.seq ?? 0) + 1 }))
  }, [shownRun, pendingStep])

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

  // A run always runs the whole flow, from the top board; one with errors
  // isn't run, and says why.
  const canRun = !starting && !viewOnly
  const runErrors = problems?.problems.filter((p) => p.level === 'error').length ?? 0

  // Starts a run of the board, which the server executes, and opens it. The
  // board shows the run on its version, which is the board as it is, or
  // with published the version the flow published.
  const onPlay = async (published = false) => {
    setStarting(true)
    try {
      const run = await startRun(flowId, published)
      // Starting the run may have saved the board as a new version.
      refreshVersions()
      if (runMatches(run, runFilter)) setRuns((rs) => [run, ...rs])
      showRun(run)
    } catch (err) {
      report(err)
    } finally {
      setStarting(false)
    }
  }

  // Run refuses a board with errors, as the server does: their list opens
  // instead, worked out again once what is being saved is.
  const onRunClicked = async () => {
    await changesSettled()
    const found = (await refreshProblems()) ?? problems
    if (found?.problems.some((p) => p.level === 'error')) {
      setProblemsOpen(true)
      setRefusing(true)
      return
    }
    setProblemsOpen(false)
    onPlay()
  }

  // Picking a problem shows the board as it is, on the board its process or
  // object is on, with it selected in the middle; one about the board itself
  // shows its start.
  const [focusing, setFocusing] = useState<string | null>(null)
  const pickProblem = (p: Problem) => {
    setProblemsOpen(false)
    setRefusing(false)
    setMenu(null)
    setPanelId(null)
    if (runId !== null) setRunId(null)
    if (compare && compare.to !== null) setCompare(null)
    setUnfolded(null)
    setBoardId(p.board ?? null)
    setFocusing(p.process !== undefined ? String(p.process) : p.object !== undefined ? objectNodeId(p.object) : START_ID)
  }
  useEffect(() => {
    const id = focusing
    if (id === null || shownVersion !== null || boardId !== shownBoard || unfolded !== null) return
    const node = getInternalNode(id)
    if (!node) return
    setFocusing(null)
    setNodes((ns) => ns.map((n) => (n.selected === (n.id === id) ? n : { ...n, selected: n.id === id })))
    const at = node.internals.positionAbsolute
    setCenter(at.x + (node.measured.width ?? 0) / 2, at.y + (node.measured.height ?? 0) / 2, {
      zoom: Math.max(getViewport().zoom, 1),
      duration: 300,
    })
  }, [focusing, nodes, boardId, shownBoard, shownVersion, unfolded, getInternalNode, setCenter, getViewport, setNodes])

  // A re-run started from the open run, or a trial of a process; it opens
  // as a run started with play does, having saved the board as a new version
  // if it changed.
  const onRerun = (run: FlowRun) => {
    refreshVersions()
    if (runMatches(run, runFilter)) setRuns((rs) => [run, ...rs])
    showRun(run)
  }

  // Opens a run on the board it ran on: a recording flow process's run on
  // the flow process's board, a trial on the board of the process it tried,
  // a run of the whole flow, or of a flow a subflow process ran, on the top
  // board.
  const showRun = (run: FlowRun) => {
    setPanelId(null)
    setVersionsOpen(false)
    setOpenedRun(run)
    setRunId(run.id)
    setUnfolded(null)
    const tried = run.trialOf == null ? undefined : contents?.processes.find((p) => p.id === run.trialOf)
    setBoardId(tried ? tried.parent : run.process)
  }

  // A run the list may not have, such as a flow process's that just
  // started, one the filter leaves out or one older than the list loaded. A
  // run of another flow, such as one a subflow process ran, or the run that
  // ran this one, opens on that flow's page.
  const openRunById = async (id: number) => {
    let run = runs.find((r) => r.id === id)
    try {
      if (!run) {
        run = await getRun(id)
        if (run.flow !== flowId) {
          window.location.hash = `#/flows/${run.flow}/runs/${id}`
          return
        }
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

  // The review asked for may be on the board of a flow process the run went
  // into, which shows, once, so its review can.
  const steppedIn = useRef(false)
  useEffect(() => {
    if (askedStep === null || steppedIn.current || shownRun?.id !== askedRun || !contents) return
    const s = shownRun.steps.find((st) => st.id === askedStep)
    if (!s) return
    steppedIn.current = true
    const p = contents.processes.find((pr) => pr.id === s.process)
    if (p && p.parent !== boardId) setBoardId(p.parent)
  })

  // The address names the open run, so going back to this flow opens it again.
  useEffect(() => {
    const want = runId === null ? `#/flows/${flowId}` : `#/flows/${flowId}/runs/${runId}`
    if (window.location.hash !== want) window.history.replaceState(window.history.state, '', want)
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
    const inSelection = id === undefined || getNode(id)?.selected
    const c = copiedOf(getNodes().filter((n) => (inSelection ? n.selected : n.id === id)))
    if (!c) return
    writeCopied(c)
    setCopied(c)
  }

  // What copying the nodes remembers, if any of them can be copied.
  const copiedOf = (nodes: FlowNode[]): Copied | null => {
    const copyable = (n: FlowNode) => !isDraft(n.id) && (n.type === 'process' || (n.type === 'object' && n.data.boundary === undefined))
    const chosen = nodes.filter(copyable)
    if (chosen.length === 0) return null
    return {
      flow: flowId,
      processes: chosen.filter((n) => n.type === 'process').map((n) => Number(n.id)),
      objects: chosen.filter((n) => n.type === 'object').map((n) => objectIdOf(n.id)),
      at: { x: Math.min(...chosen.map((n) => n.position.x)), y: Math.min(...chosen.map((n) => n.position.y)) },
    }
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
    await pasteMoved(c, at.x - c.at.x, at.y - c.at.y)
  }

  // Pastes copies moved by dx and dy onto the board shown, and selects them.
  const pasteMoved = async (c: Copied, dx: number, dy: number) => {
    try {
      const res = await pasteCopied(c, flowId, boardId, dx, dy)
      await loadBoard()
      const ids = new Set([...res.processes.map(String), ...res.objects.map(objectNodeId)])
      setNodes((ns) => ns.map((n) => ({ ...n, selected: ids.has(n.id) })))
    } catch (err) {
      report(err)
    }
  }

  // Copies of the selected processes and objects next to them, with what is
  // inside them and the links between them, as pasting brings, leaving
  // what was copied before as it was.
  const duplicateSelection = async () => {
    if (readOnly || objectBoard || structBoard) return
    const c = copiedOf(getNodes().filter((n) => n.selected))
    if (c) await pasteMoved(c, DUPLICATE_OFFSET, DUPLICATE_OFFSET)
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
      queueHistory(() => recordHistory(flowId))
        .then(() => boardChanged.current(false))
        .catch(report)
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
      if (h.changed) {
        await loadBoard()
        boardChanged.current(true)
      }
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
  // What it changed is highlighted against the version saved before, on
  // the board as it is: a run open, asked about, closes.
  const assistantApplied = useCallback(
    async (version: number | null) => {
      closePanels()
      setRunId(null)
      window.clearTimeout(recordTimer.current)
      await Promise.all([loadBoard(), refreshVersions(), onReload()])
      if (version !== null) {
        setCompare({ from: version, to: null, assistant: true })
        setDiffListed(false)
      }
    },
    [closePanels, loadBoard, refreshVersions, onReload],
  )
  // A question about a run, asked from its panel: the assistant opens with
  // it, to send while the run stays open on its version.
  const [asking, setAsking] = useState<{ question: string; seq: number } | null>(null)
  const askAbout = useCallback((question: string) => setAsking((a) => ({ question, seq: (a?.seq ?? 0) + 1 })), [])
  const showAssistantChanges = useCallback((version: number) => {
    setCompare({ from: version, to: null, assistant: true })
    setDiffListed(true)
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

  // Answers the loop step the process waits on in the open run.
  const onAnswerLoop = async (id: string, goOn: boolean) => {
    const step = shownRun?.steps.filter((s) => String(s.process) === id && s.status === 'waiting').at(-1)
    if (!shownRun || !step) return false
    try {
      await answerLoop(shownRun.id, step.id, goOn)
    } catch (err) {
      report(err)
      return false
    } finally {
      setRunReload((n) => n + 1)
    }
    updateData(id, { status: goOn ? 'active' : 'error', review: undefined })
    return true
  }

  // What a human process waiting in the open run shows to decide on, or a
  // process asking whether it goes on round a loop.
  const reviewOf = (n: ProcessNodeType): Review | undefined => {
    if (!n.data.review) return undefined
    const step = shownRun?.steps.filter((s) => String(s.process) === n.id && s.status === 'waiting').at(-1)
    if (step?.kind === 'loop')
      return {
        inputs: n.data.review,
        options: [],
        entries: [],
        handledBy: [],
        canDecide: step.canDecide,
        onDecide: async () => false,
        loop: { question: step.prompt, onAnswer: (goOn) => onAnswerLoop(n.id, goOn) },
      }
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
  // human its direct connections but those handling its failure, and ceasing
  // to be human its people's agent links; the server deletes the same links
  // when it saves the new kind.
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
        // A person can fail past their time limit, so what handles it stays.
        return !(to === 'human' && e.id.startsWith(CONNECTION_PREFIX) && e.source === id && !e.data?.failure)
      }),
    )
  }

  // The run's log or its timeline, in a window over the board; see RunLogs
  // and RunTimeline. They are the board's, not the run panel's, so they open
  // from the palette whatever panel shows.
  const [runWindow, setRunWindow] = useState<'logs' | 'timeline' | null>(null)
  useEffect(() => setRunWindow(null), [runId])

  // Finding on the board (Cmd-F; see find.ts): what is typed, the board it
  // was typed on, which the order of the matches starts from, the match gone
  // to, and a count bumped to give the box the focus again.
  const [finding, setFinding] = useState<{ query: string; from: Place; at: number | null; focus: number } | null>(null)
  const structRoot = structBoard ? (contents?.objects.find((o) => o.parent === null && o.owner === null)?.id ?? null) : null
  const matches = useMemo(
    () => (finding && contents ? findOnBoards(contents, finding.query, finding.from, structRoot) : []),
    [finding, contents, structRoot],
  )
  const here = (): Place => ({ board: boardId, unfolded })
  const openFind = () => setFinding((f) => (f ? { ...f, focus: f.focus + 1 } : { query: '', from: here(), at: null, focus: 0 }))
  const findQuery = (query: string) => setFinding((f) => f && { ...f, query, from: here(), at: null })
  // The match to centre once its board shows and its node is measured.
  const [findTarget, setFindTarget] = useState<FindMatch | null>(null)
  const goToMatch = (step: 1 | -1) => {
    if (!finding || matches.length === 0) return
    const at = finding.at === null ? (step === 1 ? 0 : matches.length - 1) : (finding.at + step + matches.length) % matches.length
    const m = matches[at]
    setFinding({ ...finding, at })
    setMenu(null)
    if (m.board !== boardId || m.unfolded !== unfolded) {
      centring.current = true
      setBoardId(m.board)
      setUnfolded(m.unfolded)
    }
    setFindTarget(m)
  }
  const closeFind = () => {
    setFinding(null)
    setFindTarget(null)
    centring.current = false
  }
  useEffect(() => {
    const t = findTarget
    if (!t || boardId !== t.board || unfolded !== t.unfolded || shownBoard !== t.board || shownUnfolded !== t.unfolded) return
    const node = nodes.find((n) => n.id === t.node)
    if (node && !node.measured?.width) return
    setFindTarget(null)
    centring.current = false
    if (!node) return
    setNodes((ns) => ns.map((n) => (Boolean(n.selected) === (n.id === node.id) ? n : { ...n, selected: n.id === node.id })))
    setEdges((es) => es.map((e) => (e.selected ? { ...e, selected: false } : e)))
    const { width = 0, height = 0 } = node.measured ?? {}
    setCenter(node.position.x + width / 2, node.position.y + height / 2, { zoom: Math.max(getZoom(), FIND_ZOOM), duration: 300 })
  }, [findTarget, nodes, boardId, unfolded, shownBoard, shownUnfolded, setNodes, setEdges, setCenter, getZoom])

  // Every process and object on the board, selected.
  const selectAll = () => setNodes((ns) => ns.map((n) => (n.type === 'process' || n.type === 'object' ? { ...n, selected: true } : n)))

  // Deletes the links and connections selected, and the processes and
  // objects selected, offering to undo it, or while a run holds the board
  // asking first; false if nothing selected can go.
  const deleteSelection = () => {
    if (readOnly) return false
    const doomed = new Set(
      getNodes()
        .filter((n) => n.selected && removable(n.id))
        .map((n) => n.id),
    )
    const links = edges.filter((e) => e.selected && e.deletable !== false && !doomed.has(e.source) && !doomed.has(e.target))
    if (doomed.size === 0 && links.length === 0) return false
    const deleteLinks = () => void (links.length > 0 && deleteElements({ edges: links }))
    if (doomed.size > 0) void removeNodes([...doomed], deleteLinks, links.length)
    else void mayDelete(links.length === 1 ? 'the link' : `${links.length} links`, '').then((yes) => yes && deleteLinks())
  }

  // Closes what Escape closes first: a note being written, the find box, a
  // run being picked to compare with, then the panel showing, as the panels
  // take the column below; false if nothing is open. A menu closes itself.
  const closeOpen = () => {
    if (menu || paneMenu) return
    if (noting) setNoting(null)
    else if (finding) closeFind()
    else if (comparing !== null) setComparing(null)
    else if (panelNode || objectPanelNode) setPanelId(null)
    else if (mcpOpen) setMCPOpen(false)
    else if (settingsOpen) setSettingsOpen(false)
    else if (oplOpen) setOPLOpen(false)
    else if (codeOpen) setCodeOpen(false)
    else if (versionsOpen) setVersionsOpen(false)
    else if (compare && diffListed) setCompare(null)
    else if (runDetail) setRunId(null)
    else return false
  }

  // What the toolbar's Save and the play button may do, which their
  // shortcuts go by too.
  const canSave = !versioning && !readOnly
  const runnable = canRun && !structBoard
  // The process its panel shows, or else the one process selected, if it
  // can be tried alone, as its panel's Try does.
  const triable = (() => {
    if (readOnly || viewOnly) return undefined
    const selected = nodes.filter((n) => n.selected && n.type === 'process' && !isDraft(n.id))
    const n = panelNode ?? (selected.length === 1 ? selected[0] : undefined)
    return n?.type === 'process' && ['agent', 'script', 'connector'].includes(n.data.kind) ? n : undefined
  })()

  // The board's shortcuts, from the registry in shortcuts.ts. Keys held down
  // don't start a run, save or duplicate again with each repeat.
  useShortcuts({
    find: () => openFind(),
    save: (e) => void (canSave && !e.repeat && setNoting('save')),
    run: (e) => void (runnable && !e.repeat && onPlay()),
    undo: () => void stepHistory(true),
    redo: () => void stepHistory(false),
    // Text selected is copied as ever.
    copy: () => (window.getSelection()?.toString() ? false : copySelection()),
    paste: () => void pasteAt(),
    duplicate: (e) => void (!e.repeat && duplicateSelection()),
    selectAll,
    delete: deleteSelection,
    fit: () => void fitView(FIT_VIEW),
    close: closeOpen,
  })

  // What the board offers the command palette: its actions, its views, the
  // building blocks to add, and opening a run of the flow on it.
  const cannotChange = viewOnly ? 'Only its owner changes the flow' : 'Not while a run or another version shows'
  usePaletteSource(() => {
    const items: PaletteItem[] = []
    const add = (item: Omit<PaletteItem, 'group'>, group = 'Board') => items.push({ ...item, group })
    add({
      id: 'board:save',
      title: 'Save a version…',
      shortcut: 'save',
      words: 'note',
      disabled: readOnly ? cannotChange : versioning ? 'Saving…' : undefined,
      run: () => setNoting('save'),
    })
    if (!structBoard) {
      add({
        id: 'board:publish',
        title: versions.published !== null && versions.draft ? 'Publish the changes…' : 'Publish…',
        hint:
          versions.published === null
            ? 'Have schedules, the API and records run the board as it is now'
            : `Version ${versions.published} is published`,
        disabled: readOnly ? cannotChange : versioning ? 'Saving…' : undefined,
        run: () => setNoting('publish'),
      })
      add({
        id: 'board:run',
        title: 'Run the flow',
        shortcut: 'run',
        disabled: runnable ? undefined : viewOnly ? cannotChange : starting ? 'Starting…' : 'Connect the start node to a process first',
        run: () => void onPlay(),
      })
      if (versions.published !== null && !viewOnly) {
        add({
          id: 'board:run-published',
          title: `Run the published version, ${versions.published}`,
          disabled: starting ? 'Starting…' : undefined,
          run: () => void onPlay(true),
        })
      }
    }
    if (triable)
      add({
        id: 'board:try',
        title: `Try ${triable.data.name}…`,
        hint: 'Run only this process, on inputs you give it',
        words: 'trial',
        run: () => setTrying(Number(triable.id)),
      })
    add({ id: 'board:find', title: 'Find on the board…', shortcut: 'find', words: 'search process object state', run: openFind })
    add({ id: 'board:fit', title: 'Zoom to fit', shortcut: 'fit', run: () => void fitView(FIT_VIEW) })
    add({
      id: 'board:tidy',
      title: 'Tidy up',
      hint: 'Lay the board out from the top',
      words: 'layout arrange',
      disabled: readOnly ? cannotChange : undefined,
      run: onTidy,
    })
    if (!structBoard && !objectBoard && selectedProcesses.length > 0 && !readOnly) {
      add({
        id: 'board:group',
        title: 'Group into flow',
        hint: 'Move the selected processes into a new flow process',
        run: () => void onWrap(),
      })
    }
    add({ id: 'board:select-all', title: 'Select every process and object', shortcut: 'selectAll', run: selectAll })
    if (!readOnly && !objectBoard && !structBoard && nodes.some((n) => n.selected)) {
      add({ id: 'board:duplicate', title: 'Duplicate the selection', shortcut: 'duplicate', run: () => void duplicateSelection() })
    }
    add({
      id: 'board:undo',
      title: 'Undo',
      shortcut: 'undo',
      disabled: readOnly ? cannotChange : undefined,
      run: () => void stepHistory(true),
    })
    add({
      id: 'board:redo',
      title: 'Redo',
      shortcut: 'redo',
      disabled: readOnly ? cannotChange : undefined,
      run: () => void stepHistory(false),
    })

    const view = (item: Omit<PaletteItem, 'group'>) => add(item, 'Views')
    view({
      id: 'view:versions',
      title: 'Versions',
      hint: 'Every saved version, to go back to, publish or compare',
      words: 'history restore rollback',
      run: () => {
        setPanelId(null)
        setMCPOpen(false)
        setSettingsOpen(false)
        setOPLOpen(false)
        setCodeOpen(false)
        refreshVersions()
        setVersionsOpen(true)
      },
    })
    if (versions.published !== null) {
      view({
        id: 'view:changes-published',
        title: `Changes since the published version, ${versions.published}`,
        words: 'diff compare draft',
        run: () => onCompare(versions.published!, null),
      })
    }
    if (versions.versions.length > 0) {
      view({
        id: 'view:changes',
        title: 'Changes since a version…',
        words: 'diff compare',
        choose: {
          title: 'Changes since',
          items: () =>
            versions.versions.map((v) => ({
              id: `version:${v.id}`,
              title: `Version ${v.id}`,
              group: 'Versions',
              hint: [v.id === versions.published ? 'published' : '', v.note, formatTime(v.createdAt)].filter(Boolean).join(' · '),
              words: v.savedBy,
              run: () => onCompare(v.id, null),
            })),
        },
      })
    }
    view({
      id: 'view:code',
      title: 'Code',
      hint: 'The flow as its setup file',
      words: 'yaml export import setup',
      run: () => openCode(true),
    })
    if (!structBoard) {
      view({
        id: 'view:mcp',
        title: 'MCP servers',
        hint: 'The MCP servers agents can use',
        words: 'tools',
        run: () => openMCPSettings(true),
      })
      view({
        id: 'view:opl',
        title: 'OPL',
        hint: "The flow in sentences, in OPM's Object-Process Language",
        words: 'sentences language',
        run: () => openOPL(true),
      })
      view({
        id: 'view:settings',
        title: 'Flow settings',
        hint: 'The repositories the flow works on, its branches and owner',
        words: 'repositories branch owner spend',
        run: () => openFlowSettings(true),
      })
    }
    if (sharedRoot)
      view({
        id: 'view:records',
        title: 'Records',
        hint: 'The records every flow using the struct wrote',
        hash: `#/flows/${flowId}/records`,
      })
    for (const b of contents?.boards ?? []) {
      const p = contents?.processes.find((q) => q.id === b.process)
      if (p && b.process !== boardId)
        view({
          id: `board-of:${b.process}`,
          title: `The board of ${p.name}`,
          words: 'flow process inside zoom',
          run: () => showProcessBoard(b.process),
        })
    }
    if (boardId !== null || unfolded !== null)
      view({ id: 'view:top', title: 'The top board', words: 'flow back up', run: () => showProcessBoard(null) })

    if (runDetail) {
      const run = (item: Omit<PaletteItem, 'group'>) => add(item, 'Run')
      run({ id: 'run:timeline', title: `Timeline of run #${runDetail.id}`, words: 'steps time axis', run: () => setRunWindow('timeline') })
      run({ id: 'run:logs', title: `Log of run #${runDetail.id}`, words: 'logs', run: () => setRunWindow('logs') })
      run({
        id: 'run:compare',
        title: `Compare run #${runDetail.id} with…`,
        words: 'diff side by side',
        choose: {
          title: `Compare run #${runDetail.id} with`,
          items: async () =>
            (await listRuns(flowId, undefined, undefined, COMPARE_RUNS))
              .filter((r) => r.id !== runDetail.id)
              .map((r) => runItem(r, flow.name, { hash: `#/flows/${flowId}/compare/${runDetail.id}/${r.id}` })),
        },
      })
      run({ id: 'run:close', title: `Close run #${runDetail.id}`, hint: 'Back to the board', run: () => setRunId(null) })
    }

    if (!readOnly && !(structBoard && !objectBoard)) {
      const blocks: [Block, string][] = [
        ...(objectBoard ? BUILDING_BLOCKS.filter((b) => b.type === 'object' && !b.human) : BUILDING_BLOCKS).map((b): [Block, string] => [
          b,
          'Building block',
        ]),
        ...(objectBoard || structBoard
          ? []
          : [
              ...(connectorTiles ?? []).map((b): [Block, string] => [b, 'Connector']),
              ...flowBlocks.map((b): [Block, string] => [b, 'Procedure']),
              ...structBlocks.map((b): [Block, string] => [b, 'Struct']),
            ]),
      ]
      for (const [b, kind] of blocks) {
        add(
          {
            id: `add:${b.id}`,
            title: `Add ${kind === 'Building block' ? `a ${b.label.toLowerCase()}` : b.label}`,
            hint: kind === 'Building block' ? b.title : kind,
            words: `new ${kind}`,
            run: () => addAtViewportCenter(b),
          },
          'Add to the board',
        )
      }
    }
    return {
      items,
      openRun: (f, run) => {
        if (f !== flowId || structBoard) return false
        void openRunById(run)
        return true
      },
    }
  })

  const panelColumn = useColumnWidth(PANEL_COLUMN)

  // The help the board shows: a guide while the board shows nothing, and a
  // tip on what is selected or being drawn; see Hints.tsx. They are about
  // changing the board, so a board only read shows none.
  const hints = useHints()
  const loaded = contents !== null && shownVersion === null && shownBoard === boardId
  const emptyBoard =
    loaded && !readOnly && !structBoard && !objectBoard && !nodes.some((n) => n.type === 'process' || (boardId === null && n.type === 'object'))
  const boardTip = ((): BoardTip | null => {
    if (readOnly || !loaded) return null
    if (fromState) return 'state'
    const picked = nodes.filter((n) => n.selected && !isDraft(n.id))
    const pickedEdges = edges.filter((e) => e.selected)
    if (picked.length > 1) return 'several'
    if (picked.length === 1 && pickedEdges.length === 0) {
      if (picked[0].type === 'process') return 'process'
      if (picked[0].type === 'object') return structBoard ? 'struct' : 'object'
    }
    if (picked.length === 0 && pickedEdges.length === 1) {
      const e = pickedEdges[0]
      if (e.data?.link) return e.data.link.direction === 'input' ? 'input' : 'output'
      if (e.id.startsWith(CONNECTION_PREFIX)) return 'connection'
    }
    return structBoard && picked.length === 0 ? 'struct' : null
  })()

  // What the comparison highlights on the board shown, if it shows the
  // newer board; the nodes and edges are drawn with it, so loading the
  // board again keeps it.
  const highlights = useMemo<Highlights>(() => {
    if (!diff || !compare || shownVersion !== compare.to) return noHighlights
    const parents = new Map(contents?.processes.map((p) => [p.id, p.parent]) ?? [])
    return highlightsOf(diff, boardId, {
      process: (id) => String(id),
      object: objectNodeId,
      link: (id) => `${LINK_PREFIX}${id}`,
      connection: (id) => `${CONNECTION_PREFIX}${id}`,
      start: START_EDGE_ID,
      parentOf: (p) => parents.get(p),
    })
  }, [diff, compare, shownVersion, boardId, contents])
  // What finding found is outlined too, the match gone to most.
  const found = useMemo(() => new Set(matches.map((m) => m.node)), [matches])
  const foundAt = finding?.at != null ? matches[finding.at]?.node : undefined
  const shownNodes = useMemo(
    () =>
      highlights.nodes.size === 0 && found.size === 0
        ? nodes
        : nodes.map((n) => {
            const change = highlights.nodes.get(n.id)
            const match = found.has(n.id) ? (n.id === foundAt ? 'find-current' : 'find-match') : undefined
            if (!change && !match) return n
            return { ...n, className: [n.className, change && `diff-${change}`, match].filter(Boolean).join(' ') }
          }),
    [nodes, highlights, found, foundAt],
  )
  const shownEdges = useMemo(
    () =>
      highlights.edges.size === 0
        ? edges
        : edges.map((e) => {
            const change = highlights.edges.get(e.id)
            return change ? { ...e, className: `${e.className ?? ''} diff-${change}`.trim() } : e
          }),
    [edges, highlights],
  )

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
        <div className="versioning">
          <span className="note-anchor">
            <button
              onClick={() => setNoting(noting === 'save' ? null : 'save')}
              disabled={versioning || readOnly}
              aria-expanded={noting === 'save'}
              title={`Save the board as a new version, with a note saying why if you like (${shortcutText('save', MAC)})`}
            >
              <SaveIcon /> Save
            </button>
            {noting === 'save' && (
              <NoteDialog
                title="Save a version"
                hint="Keeps the board as it is now as a version to come back to."
                action="Save"
                onSubmit={(note) => {
                  setNoting(null)
                  onSave(note)
                }}
                onClose={() => setNoting(null)}
              />
            )}
          </span>
          {!structBoard && (
            <span className="note-anchor">
              <button
                className={`publish-toggle${versions.published === null ? '' : versions.draft ? ' draft' : ' published'}`}
                onClick={() => setNoting(noting === 'publish' ? null : 'publish')}
                disabled={versioning || readOnly}
                aria-expanded={noting === 'publish'}
                title={
                  versions.published === null
                    ? 'Not published: schedules, the API and runs on records run the board as it is. Publish to have them run it as it is now while you go on editing.'
                    : versions.draft
                      ? `Version ${versions.published} is published, and the board has changed since. Publish to have schedules, the API and runs on records run the board as it is now.`
                      : `Version ${versions.published} is published, the board as it is: schedules, the API and runs on records run it.`
                }
              >
                <PublishIcon />
                {versions.published === null ? 'Publish' : versions.draft ? 'Publish changes' : 'Published'}
              </button>
              {noting === 'publish' && (
                <NoteDialog
                  title="Publish the board"
                  hint="Schedules, the API and runs on records run the board as it is now, until you publish again; the board stays yours to edit, and Run runs it as it is."
                  action="Publish"
                  onSubmit={(note) => {
                    setNoting(null)
                    onPublish(note)
                  }}
                  onClose={() => setNoting(null)}
                />
              )}
            </span>
          )}
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
                title="The repositories the flow works on, its branches, and how other systems start it"
              >
                <GearIcon /> Settings
              </button>
            </>
          )}
        </div>
        {!structBoard && (
          <ProblemCount found={problems} open={problemsOpen} onOpen={openProblems} onPick={pickProblem} refusing={refusing} />
        )}
        {!structBoard && (
          <button
            className="play"
            onClick={() => void onRunClicked()}
            disabled={!canRun}
            aria-label="Run flow"
            title={
              starting
                ? 'Starting…'
                : viewOnly
                  ? 'Only those who may change the flow run it'
                  : runErrors > 0
                    ? `Run flow: it has ${runErrors === 1 ? 'an error' : `${runErrors} errors`} to fix first, which clicking lists`
                    : `Run flow (${shortcutText('run', MAC)})`
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
            {!readOnly && !(structBoard && !objectBoard) && <BlocksHint />}
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
                flow={flowId}
                runs={listedRuns}
                filter={runFilter}
                onFilter={filterRuns}
                more={moreRuns}
                loading={loadingRuns}
                onMore={loadOlderRuns}
                selected={runId}
                onSelect={(id) => {
                  // The run picked to compare with opens beside the other, in a tab of its own.
                  if (comparing !== null) {
                    if (id !== comparing) openTab(`#/flows/${flowId}/compare/${comparing}/${id}`)
                    setComparing(null)
                    return
                  }
                  const run = listedRuns.find((r) => r.id === id)
                  if (run) showRun(run)
                }}
                picking={comparing}
                onCancelPick={() => setComparing(null)}
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
          {emptyBoard && <EmptyBoardGuide inner={boardId !== null} />}
          {!emptyBoard && <BoardTips tip={boardTip} />}
          <BoardActionsContext.Provider value={actions}>
            <StructFlowsContext.Provider value={structFlows}>
              <ReactFlow
                nodes={shownNodes}
                edges={shownEdges}
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
                onMoveEnd={() => keepTabView(tab, viewOf.current())}
                defaultViewport={savedView ?? undefined}
                onDoubleClick={onPaneDoubleClick}
                zoomOnDoubleClick={false}
                nodesDraggable={!readOnly}
                nodesConnectable={!readOnly && !objectBoard}
                // Deleting goes through the board's shortcuts.
                deleteKeyCode={null}
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
          {finding && (
            <BoardFind
              query={finding.query}
              count={matches.length}
              at={finding.at}
              place={(() => {
                const m = finding.at === null ? undefined : matches[finding.at]
                if (!m || !contents) return null
                const what = { process: 'Process', object: 'Object', state: 'State' }[m.kind]
                return `${what} ${m.name}${m.of ? ` of ${m.of}` : ''}, ${placeOf(m, contents)}`
              })()}
              focus={finding.focus}
              onQuery={findQuery}
              onNext={() => goToMatch(1)}
              onPrevious={() => goToMatch(-1)}
              onClose={closeFind}
            />
          )}
          {viewOnly && !openRun && (
            <div className="review-banner" role="status">
              You decide this flow's reviews, so you see it, but only its owner{flow.owner.kind !== 'global' && `, ${flow.owner.name},`} changes it.
            </div>
          )}
          {compare && shownVersion === compare.to && (
            <div className="review-banner diff-banner" role="status">
              {compare.assistant
                ? 'Highlighted is what the assistant changed.'
                : compare.to === null
                  ? `Highlighted is what changed since version ${compare.from}.`
                  : `This is version ${compare.to}, highlighting what changed since version ${compare.from}.`}
              {diff && ` ${changesText(diffCount(diff))}.`}
              {!diffListed && <button onClick={() => setDiffListed(true)}>List the changes</button>}
              <button onClick={() => setCompare(null)}>{compare.to === null ? 'Dismiss' : 'Back to the board'}</button>
            </div>
          )}
          {reviewing && openRun && !compare && (
            <div className="review-banner" role="status">
              {openRun.status === 'running'
                ? `Run #${openRun.id} is running on version ${shownVersion}.`
                : `Run #${openRun.id} ran on version ${shownVersion}; this is the board as it was then.`}
              <button onClick={() => setRunId(null)}>Back to the board</button>
            </div>
          )}
          <Assistant
            flowId={flowId}
            disabled={viewOnly || (reviewing && runId === null)}
            runOpen={runId}
            asking={asking}
            beforeSend={beforeAssistant}
            onApplied={assistantApplied}
            onShowChanges={showAssistantChanges}
            onError={report}
          />
          {menu && (
            <ContextMenu x={menu.x} y={menu.y} items={menuItems(menu.id)} onClose={() => setMenu(null)} />
          )}
          {paneMenu && (
            <ContextMenu
              x={paneMenu.x}
              y={paneMenu.y}
              items={[
                { label: 'Paste', icon: <PasteIcon />, disabled: copied === null, onSelect: () => pasteAt(paneMenu) },
                ...(hints.anyHidden ? [{ label: 'Show the hints again', icon: <span aria-hidden>?</span>, onSelect: hints.showAll }] : []),
              ]}
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
            trashedFlow={panelNode.data.trashedFlow}
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
            skills={skills}
            commands={commands}
            credentials={credentials}
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
            onTry={viewOnly ? undefined : () => setTrying(Number(panelNode.id))}
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
            flow={flowId}
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
                onChange={(owner) =>
                  setFlowOwner(flowId, owner)
                    .then(() => {
                      onReload()
                      // What the flow may use goes by its owner.
                      refreshSkills()
                    })
                    .catch(report)
                }
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
            versions={versions}
            disabled={versioning}
            onRestore={onRestore}
            onPublish={onPublishVersion}
            onUnpublish={onUnpublish}
            onRunPublished={() => onPlay(true)}
            onCompare={onCompare}
            onClose={() => setVersionsOpen(false)}
          />
        )}
        {!panelNode && !objectPanelNode && !mcpOpen && !settingsOpen && !oplOpen && !codeOpen && !versionsOpen && compare && diffListed && (
          <DiffPanel
            diff={diff}
            title={
              compare.assistant
                ? `What the assistant changed, against version ${compare.from}, saved before.`
                : compare.to === null
                  ? `What changed from version ${compare.from} to the board as it is.`
                  : `What changed from version ${compare.from} to version ${compare.to}, which the board shows.`
            }
            boardName={(board) => contents?.processes.find((p) => p.id === board)?.name ?? 'a flow process'}
            onBack={
              compare.assistant
                ? undefined
                : () => {
                    setCompare(null)
                    refreshVersions()
                    setVersionsOpen(true)
                  }
            }
            onClose={() => setCompare(null)}
          />
        )}
        {!panelNode && !objectPanelNode && !mcpOpen && !settingsOpen && !oplOpen && !codeOpen && !versionsOpen && !(compare && diffListed) && runDetail && (
          <RunPanel
            run={runDetail}
            onStop={() => onStop(runDetail.id)}
            onStopStep={(step) => onStopStep(runDetail.id, step)}
            onOpenStep={openStep}
            focus={runFocus}
            onClearFocus={() => setRunFocus(null)}
            onLogs={() => setRunWindow('logs')}
            onTimeline={() => setRunWindow('timeline')}
            comparing={comparing === runDetail.id}
            onCompare={() => setComparing((c) => (c === runDetail.id ? null : runDetail.id))}
            onOpenRun={openRunById}
            onRerun={setRerunning}
            onTryAgain={viewOnly ? undefined : setTrying}
            onAsk={viewOnly || runDetail.flow !== flowId ? undefined : askAbout}
            onChanged={() => setRunReload((n) => n + 1)}
            onClose={() => setRunId(null)}
          />
        )}
      </div>
      {runWindow === 'logs' && runDetail && <RunLogs run={runDetail} onClose={() => setRunWindow(null)} />}
      {runWindow === 'timeline' && runDetail && (
        <RunTimeline run={runDetail} processes={contents?.processes} onOpenStep={openStep} onClose={() => setRunWindow(null)} />
      )}
      {trying !== null && (
        <TrialDialog
          process={trying}
          onStarted={(r) => {
            setTrying(null)
            onRerun(r)
          }}
          onClose={() => setTrying(null)}
        />
      )}
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
      {confirmDialog}
      {undoNotice}
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
        <span className="flow-name-text" title={name}>
          {name}
        </span>
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

const RUN_FILTER_KEY_PREFIX = 'run-filter-'

// The run list's filter last picked for the flow, so it filters the same
// way next time; only values it still knows.
function loadRunFilter(flow: number): RunFilter {
  try {
    const saved = JSON.parse(localStorage.getItem(RUN_FILTER_KEY_PREFIX + flow) ?? 'null') as Partial<RunFilter> | null
    return {
      status: (saved?.status ?? []).filter((s) => s in STANDING_LABELS),
      started: (saved?.started ?? []).filter((o) => o in ORIGIN_LABELS),
    }
  } catch {
    return { status: [], started: [] }
  }
}

function saveRunFilter(flow: number, f: RunFilter) {
  try {
    if (f.status.length === 0 && f.started.length === 0) localStorage.removeItem(RUN_FILTER_KEY_PREFIX + flow)
    else localStorage.setItem(RUN_FILTER_KEY_PREFIX + flow, JSON.stringify(f))
  } catch {
    // The list shows every run next time.
  }
}
