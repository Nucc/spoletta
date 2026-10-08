import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { MouseEvent } from 'react'
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
import { BoardActionsContext, type BoardActions } from './actions'
import ContextMenu, { type MenuItem } from './ContextMenu'
import {
  CopyIcon,
  FlowBuilderIcon,
  FlowIcon,
  HistoryIcon,
  LayoutIcon,
  PencilIcon,
  PlayIcon,
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
import ProcessPanel from './ProcessPanel'
import StartNode, { type StartNodeType } from './StartNode'
import EndNode, { type EndNodeType } from './EndNode'
import { decisionText, RunList, RunPanel } from './Runs'
import { VersionPanel } from './Versions'
import {
  createAttribute,
  createLink,
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
  getStart,
  getVersionBoard,
  listConnections,
  listLinks,
  listModels,
  listBoards,
  listObjects,
  listProcesses,
  listRuns,
  listVersions,
  renameState,
  restoreVersion,
  saveVersion,
  setLinkRoute,
  setLinkStates,
  startRun,
  stopRun,
  updateAttribute,
  updateEnd,
  updateObject,
  updateProcess,
  updateStart,
  wrapProcesses,
  type BoardContents,
  type BoardId,
  type Connection,
  type FlowObject,
  type FlowRun,
  type Versions,
  type FlowRunDetail,
  type Link,
  type Models,
  type ObjectAttribute,
  type ObjectState,
  type Process,
  type ProcessKind,
  type Start,
} from './api'

type FlowNode = ProcessNodeType | ObjectNodeType | StartNodeType | EndNodeType
type FlowEdge = Edge<LinkEdgeData>

const nodeTypes = { process: ProcessNode, object: ObjectNode, start: StartNode, end: EndNode }
const edgeTypes = { link: LinkEdge }

// Default sizes of new or never resized nodes; resizing changes only the
// width. Used to place new nodes before they are measured.
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
// A flow process's board ends here.
const END_ID = 'end'
// The dashed lines from the start to the objects the flow process takes, and
// from the ones it generates to the end.
const BOUNDARY_PREFIX = 'boundary-'
// Where the objects outside sit, relative to the start and end.
const BOUNDARY_GAP = { x: 24, y: 90 }
// Direct process-to-process connections predate objects. New ones can't be
// drawn, but existing ones still show, run and can be deleted.
const CONNECTION_PREFIX = 'conn-'
const LINK_PREFIX = 'link-'
// Objects and processes have separate id sequences, so object node ids are prefixed.
const OBJECT_PREFIX = 'obj-'

const markerEnd = { type: MarkerType.ArrowClosed }

// How often an open run that is still going is refreshed, and the run list
// while any run in it is.
const RUN_REFRESH_MS = 1500
const RUN_LIST_REFRESH_MS = 5000

// How long a board just shown gets to measure its nodes before it is fitted into view.
const BOARD_FIT_DELAY_MS = 100

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

const connectionEdge = (c: Connection): FlowEdge => ({
  id: `${CONNECTION_PREFIX}${c.id}`,
  type: 'link',
  source: String(c.source),
  target: String(c.target),
  markerEnd,
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

const fetchBoard = (): Promise<BoardContents> =>
  Promise.all([listProcesses(), listObjects(), getStart(), listConnections(), listLinks(), listBoards()]).then(
    ([processes, objects, start, connections, links, boards]) => ({ processes, objects, start, connections, links, boards }),
  )

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

const toEndNode = (e: { x: number; y: number }): EndNodeType => ({
  id: END_ID,
  type: 'end',
  position: { x: e.x, y: e.y },
  deletable: false,
  data: {},
})

export default function Board() {
  const [nodes, setNodes, onNodesChange] = useNodesState<FlowNode>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<FlowEdge>([])
  const [error, setError] = useState<string | null>(null)
  // Set while a run is being started.
  const [starting, setStarting] = useState(false)
  const [panelId, setPanelId] = useState<string | null>(null)
  const [models, setModels] = useState<Models | null>(null)
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
  // The whole flow the board shows part of, for the names of the boards above it.
  const [contents, setContents] = useState<BoardContents | null>(null)
  // Bumped each time a board is shown, so the open run shows on it again.
  const [shownSeq, setShownSeq] = useState(0)
  // The node whose context menu is open, and where.
  const [menu, setMenu] = useState<{ id: string; x: number; y: number } | null>(null)
  const { getNode, getNodes, screenToFlowPosition, fitView } = useReactFlow<FlowNode>()
  const wrapper = useRef<HTMLDivElement>(null)
  const draftSeq = useRef(0)
  // The reviews whose process's panel was opened for them, so closing it keeps it closed.
  const shownReviews = useRef(new Set<number>())

  const report = useCallback((err: unknown) => {
    console.error(err)
    setError(err instanceof Error ? err.message : String(err))
  }, [])

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

  // Processes and objects are named inline when they are created, and renamed
  // inline from their menu. A draft only gets saved once it has a name.
  const commitName = useCallback(
    async (id: string, name: string) => {
      const node = getNode(id)
      if (!node || node.type === 'start' || node.type === 'end') return
      setNodes((ns) => ns.map((n) => (n.id === id ? ({ ...n, data: { ...n.data, name, editing: false } } as FlowNode) : n)))
      try {
        if (!isDraft(id)) {
          if (node.type === 'object') await updateObject(objectIdOf(id), { name })
          else await updateProcess(Number(id), { name })
          return
        }
        const { x, y } = node.position
        const parent = boardRef.current
        const newId =
          node.type === 'object'
            ? objectNodeId((await createObject({ name, x, y, parent })).id)
            : String((await createProcess({ name, kind: 'agent', prompt: '', script: '', model: '', thinking: '', x, y, parent })).id)
        setNodes((ns) => ns.map((n) => (n.id === id ? { ...n, id: newId } : n)))
      } catch (err) {
        report(err)
      }
    },
    [getNode, setNodes, report],
  )

  const cancelName = useCallback(
    (id: string) => {
      if (isDraft(id)) setNodes((ns) => ns.filter((n) => n.id !== id))
      else setEditing(id, false)
    },
    [setNodes, setEditing],
  )

  const toObjectNode = useCallback(
    (o: FlowObject, editing = false, boundary?: 'in' | 'out', position?: XYPosition): ObjectNodeType => ({
      id: objectNodeId(o.id),
      type: 'object',
      position: position ?? { x: o.x, y: o.y },
      width: o.width ?? NODE_SIZE.object.width,
      deletable: false,
      draggable: !boundary,
      data: { name: o.name, states: o.states, attributes: o.attributes, persistent: o.persistent, many: o.many, editing, boundary },
    }),
    [],
  )

  const toNode = useCallback(
    (p: Process, editing = false): ProcessNodeType => ({
      id: String(p.id),
      type: 'process',
      position: { x: p.x, y: p.y },
      width: p.width ?? NODE_SIZE.process.width,
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

  const refreshRuns = useCallback(() => listRuns().then(setRuns).catch(report), [report])

  const refreshVersions = useCallback(() => listVersions().then(setVersions).catch(report), [report])

  // Shows one board of the flow: its processes, its objects and their links.
  // A flow process's board also shows the objects the flow process is linked
  // to, the ones it takes below its start and the ones it only generates
  // above its end. A board that is gone, such as after jumping to a version
  // without its flow process, gives way to the top board.
  const showBoard = useCallback(
    (c: BoardContents, wanted: BoardId) => {
      const inner = wanted === null ? undefined : c.boards.find((b) => b.process === wanted)
      const board = inner ? wanted : null
      if (board !== wanted) setBoardId(board)
      const ps = c.processes.filter((p) => p.parent === board)
      const ids = new Set(ps.map((p) => p.id))
      const humans = new Set(ps.filter((p) => p.kind === 'human').map((p) => p.id))
      const start = inner?.start ?? c.start
      const own = c.objects.filter((o) => o.parent === board)

      // The objects outside, each once: taken ones at the start, even if generated too.
      const outside = (direction: 'input' | 'output', except: Set<number>) => [
        ...new Set(c.links.filter((l) => l.process === board && l.direction === direction && !except.has(l.object)).map((l) => l.object)),
      ]
      const ins = board === null ? [] : outside('input', new Set(own.map((o) => o.id)))
      const outs = board === null ? [] : outside('output', new Set([...own.map((o) => o.id), ...ins]))
      const byId = new Map(c.objects.map((o) => [o.id, o]))
      const row = (objects: number[], at: XYPosition, side: 'in' | 'out') =>
        objects.flatMap((id, i) => {
          const o = byId.get(id)
          const x = at.x + i * (NODE_SIZE.object.width + BOUNDARY_GAP.x)
          return o ? [toObjectNode(o, false, side, { x, y: at.y })] : []
        })

      setNodes([
        toStartNode(start),
        ...(inner ? [toEndNode(inner.end)] : []),
        ...ps.map((p) => toNode(p)),
        ...own.map((o) => toObjectNode(o)),
        ...row(ins, { x: start.x, y: start.y + BOUNDARY_GAP.y }, 'in'),
        ...(inner ? row(outs, { x: inner.end.x, y: inner.end.y - BOUNDARY_GAP.y }, 'out') : []),
      ])
      setEdges([
        ...(start.target !== null && ids.has(start.target) ? [startEdge(start.target, start.route)] : []),
        ...c.connections.filter((cn) => ids.has(cn.source)).map(connectionEdge),
        ...c.links.filter((l) => ids.has(l.process)).map((l) => linkEdge(l, humans.has(l.process))),
        ...ins.map((o) => boundaryEdge(START_ID, objectNodeId(o))),
        ...(inner
          ? [...new Set(c.links.filter((l) => l.process === board && l.direction === 'output').map((l) => l.object))].map((o) =>
              boundaryEdge(objectNodeId(o), END_ID),
            )
          : []),
      ])
      setContents(c)
      setShownSeq((n) => n + 1)
    },
    [setNodes, setEdges, toNode, toObjectNode],
  )

  const loadBoard = useCallback(() => fetchBoard().then((c) => showBoard(c, boardRef.current)), [showBoard])

  useEffect(() => {
    listModels().then(setModels).catch(report)
    refreshRuns()
    refreshVersions()
    loadBoard().catch(report)
  }, [loadBoard, report, refreshRuns, refreshVersions])

  const onSave = async () => {
    setVersioning(true)
    try {
      await saveVersion()
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
        if (detail.status === 'running') timer = window.setTimeout(load, RUN_REFRESH_MS)
        else setRuns((rs) => rs.map((r) => (r.id === detail.id ? { ...r, status: detail.status, finishedAt: detail.finishedAt } : r)))
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
  const reviewing = shownVersion !== null
  useEffect(() => {
    if (runVersion === shownVersion && boardId === shownBoard) return
    let stale = false
    ;(runVersion === null ? fetchBoard() : getVersionBoard(runVersion))
      .then((c) => {
        if (stale) return
        setPanelId(null)
        setMenu(null)
        showBoard(c, boardId)
        setShownVersion(runVersion)
        setShownBoard(boardId)
        // Another board is a different place; show all of it once its nodes are measured.
        if (boardId !== shownBoard) window.setTimeout(() => fitView({ duration: 300 }), BOARD_FIT_DELAY_MS)
      })
      .catch(report)
    return () => {
      stale = true
    }
  }, [runVersion, shownVersion, boardId, shownBoard, showBoard, fitView, report])

  // Shows the open run on the board: each process as its last step in the
  // run left it, each object with the content and state it last got. A
  // review waiting for a decision opens its process's panel, unless another
  // panel is open.
  useEffect(() => {
    if (!shownRun || shownVersion !== runVersion) return
    const steps = new Map(shownRun.steps.flatMap((s) => (s.process === null ? [] : [[String(s.process), s] as const])))
    const objects = new Map(shownRun.objects.flatMap((o) => (o.object === null ? [] : [[objectNodeId(o.object), o] as const])))
    // A review inside a flow process opens once its board shows.
    for (const [process, s] of steps) {
      if (s.status !== 'waiting' || shownReviews.current.has(s.id) || !getNode(process)) continue
      shownReviews.current.add(s.id)
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
            data: { ...n.data, status: s?.status, output: output || undefined, runError: s?.error || undefined, review },
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

  const addDraft = useCallback(
    (type: 'process' | 'object', screenX: number, screenY: number) => {
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
          ? { ...base, type, data: { name: '', kind: 'agent', prompt: '', script: '', model: '', thinking: '', recording: false, settings: {}, editing: true } }
          : { ...base, type, data: { name: '', states: [], attributes: [], persistent: false, many: false, editing: true } }
      setNodes((ns) => [...ns.map((n) => ({ ...n, selected: false })), draft])
    },
    [screenToFlowPosition, setNodes],
  )

  const addAtViewportCenter = (type: 'process' | 'object') => {
    const rect = wrapper.current?.getBoundingClientRect()
    if (rect) addDraft(type, rect.left + rect.width / 2, rect.top + rect.height / 2)
  }

  const onPaneDoubleClick = (e: MouseEvent<HTMLDivElement>) => {
    if (!reviewing && (e.target as HTMLElement).classList.contains('react-flow__pane')) {
      addDraft(e.shiftKey ? 'object' : 'process', e.clientX, e.clientY)
    }
  }

  // A flow process opens its board; other nodes their settings.
  const onNodeDoubleClick: NodeMouseHandler<FlowNode> = (_, node) => {
    if (node.type === 'process' && node.data.kind === 'flow' && !isDraft(node.id)) zoomIn(node.id)
    else if (!isDraft(node.id) && node.type !== 'start' && node.type !== 'end') setPanelId(node.id)
  }

  const zoomIn = (id: string) => {
    setPanelId(null)
    setMenu(null)
    setBoardId(Number(id))
  }

  const isBoundary = (id: string) => {
    const node = getNode(id)
    return node?.type === 'object' && node.data.boundary !== undefined
  }

  const panelNode = nodes.find((n): n is ProcessNodeType => n.type === 'process' && n.id === panelId)
  const objectPanelNode = nodes.find((n): n is ObjectNodeType => n.type === 'object' && n.id === panelId)

  const savePositions = (positions: Map<string, XYPosition>) => {
    for (const [id, { x, y }] of positions) {
      if (id === START_ID) updateStart(boardId, { x, y }).catch(report)
      else if (id === END_ID) {
        if (boardId !== null) updateEnd(boardId, { x, y }).catch(report)
      }
      // The objects outside keep their place there.
      else if (isDraft(id) || isBoundary(id)) continue
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
    const req = isObject(id) ? updateObject(objectIdOf(id), { x, width }) : updateProcess(Number(id), { x, width })
    req.catch(report)
  }

  // Sets where an edge's segments run, null for the default route, and saves
  // it if asked.
  const setRoute = (edgeId: string, route: number[] | null, save: boolean) => {
    setEdges((es) => es.map((e) => (e.id === edgeId ? { ...e, data: { ...e.data, route } } : e)))
    if (!save) return
    const link = edges.find((e) => e.id === edgeId)?.data?.link
    if (edgeId === START_EDGE_ID) updateStart(boardId, { route }).catch(report)
    else if (link) setLinkRoute(link.id, route).catch(report)
  }

  // Lays the whole board out from the top, and keeps that layout. Routes
  // drawn by hand were for the old layout, so links go back to the default.
  const onTidy = () => {
    moveNodes(tidy(getNodes(), edges))
    for (const e of edges) if (e.data?.route) setRoute(e.id, null, true)
    window.requestAnimationFrame(() => fitView({ duration: 300 }))
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
    if (isDraft(source) || isDraft(target)) return false
    const same = (a: string | null | undefined, b: string | null | undefined) => (a ?? null) === (b ?? null)
    if (edges.some((e) => e.source === source && e.target === target && same(e.sourceHandle, sourceHandle) && same(e.targetHandle, targetHandle))) {
      return false
    }
    if (source === START_ID || isObject(source)) return isProcess(target)
    if (!isProcess(source) || !isObject(target)) return false
    if (edges.some((e) => e.source === source && e.target === target)) return false
    return !(humanProcess(source) && stateOfHandle(targetHandle) !== null)
  }

  const onConnect: OnConnect = async ({ source, target, sourceHandle, targetHandle }) => {
    try {
      if (source === START_ID) {
        // The initial node has a single outgoing transition; a new one replaces it.
        await updateStart(boardId, { target: Number(target) })
        setEdges((es) => [...es.filter((e) => e.id !== START_EDGE_ID), startEdge(target)])
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
          ? updateStart(boardId, { target: null })
          : e.id.startsWith(LINK_PREFIX)
            ? deleteLink(Number(e.id.slice(LINK_PREFIX.length)))
            : deleteConnection(Number(e.id.slice(CONNECTION_PREFIX.length)))
      req.catch(report)
    }
  }

  const newProcess = (position: XYPosition) =>
    createProcess({ name: NEW_PROCESS, kind: 'agent', prompt: '', script: '', model: '', thinking: '', parent: boardId, ...position })

  // Where a node of the given type goes right below another, centered on it.
  const below = (node: FlowNode, type: 'process' | 'object'): XYPosition => ({
    x: node.position.x + (node.measured?.width ?? 0) / 2 - NODE_SIZE[type].width / 2,
    y: node.position.y + (node.measured?.height ?? 0) + RANK_GAP,
  })

  // Below a process goes the object it generates, below an object a process
  // that takes it. The new node starts out being named.
  const append = async (id: string) => {
    const node = getNode(id)
    if (!node || node.type === 'start' || node.type === 'end' || reviewing) return
    try {
      if (node.type === 'process') {
        const o = await createObject({ name: NEW_OBJECT, parent: boardId, ...below(node, 'object') })
        const l = await createLink({ process: Number(id), object: o.id, direction: 'output' })
        setNodes((ns) => [...ns, toObjectNode(o, true)])
        setEdges((es) => [...es, linkEdge(l, false)])
      } else {
        const p = await newProcess(below(node, 'process'))
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
    if (!edge || !source || !target || reviewing) return
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
      const o = await createObject({ name: NEW_OBJECT, parent: boardId, ...at('object', processFirst ? secondY : firstY) })
      const links: Link[] = []
      if (processFirst) {
        if (edge.id === START_EDGE_ID) await updateStart(boardId, { target: p.id })
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
    if (!node || node.type === 'start' || node.type === 'end' || reviewing || isBoundary(id)) return
    const inside = node.type === 'process' && node.data.kind === 'flow' ? ', its links and everything on its board' : ' and its links'
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
          ? toObjectNode(await createObject({ name, width, parent: boardId, ...position }))
          : toNode(
              await createProcess({
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

  // The links naming the state go with it.
  const removeState = async (objectId: string, stateId: number) => {
    const node = getNode(objectId)
    const state = node?.type === 'object' ? node.data.states.find((s) => s.id === stateId) : undefined
    if (!state || reviewing || !window.confirm(`Delete state “${state.name}” and its links?`)) return
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

  const setDecisionStates = async (edgeId: string, states: number[]) => {
    const edge = edges.find((e) => e.id === edgeId)
    const l = edge?.data?.link
    if (!l || reviewing) return
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

  const addAttribute = async (objectId: string, name: string) => {
    if (reviewing) return false
    try {
      const attribute = await createAttribute(objectIdOf(objectId), { name, definition: '' })
      setAttributes(objectId, (as) => [...as, attribute])
      return true
    } catch (err) {
      report(err)
      return false
    }
  }

  const setMany = async (objectId: string, many: boolean) => {
    if (reviewing) return false
    try {
      await updateObject(objectIdOf(objectId), { many })
      updateObjectData(objectId, { many })
      return true
    } catch (err) {
      report(err)
      return false
    }
  }

  const setPersistent = async (objectId: string, persistent: boolean) => {
    if (reviewing) return false
    try {
      await updateObject(objectIdOf(objectId), { persistent })
      updateObjectData(objectId, { persistent })
      return true
    } catch (err) {
      report(err)
      return false
    }
  }

  const changeAttribute = async (
    objectId: string,
    id: number,
    patch: { name?: string; definition?: string; unique?: boolean },
  ) => {
    try {
      const attribute = await updateAttribute(id, patch)
      setAttributes(objectId, (as) => as.map((a) => (a.id === id ? attribute : a)))
      return true
    } catch (err) {
      report(err)
      return false
    }
  }

  const removeAttribute = async (objectId: string, id: number) => {
    const node = getNode(objectId)
    const attribute = node?.type === 'object' ? node.data.attributes.find((a) => a.id === id) : undefined
    if (!attribute || reviewing || !window.confirm(`Delete attribute “${attribute.name}”?`)) return false
    try {
      await deleteAttribute(id)
    } catch (err) {
      report(err)
      return false
    }
    setAttributes(objectId, (as) => as.filter((a) => a.id !== id))
    return true
  }

  const onNodeContextMenu: NodeMouseHandler<FlowNode> = (e, node) => {
    e.preventDefault()
    if (node.type === 'start' || node.type === 'end' || isDraft(node.id)) return
    // Viewing a run, a flow process can still be opened.
    if (!reviewing || (node.type === 'process' && node.data.kind === 'flow')) setMenu({ id: node.id, x: e.clientX, y: e.clientY })
  }

  const flowProcess = (id: string) => {
    const node = getNode(id)
    return node?.type === 'process' && node.data.kind === 'flow'
  }

  const menuItems = (id: string): MenuItem[] => [
    ...(flowProcess(id) ? [{ label: 'Zoom in', icon: <ZoomInIcon />, onSelect: () => zoomIn(id) }] : []),
    {
      label: 'Rename',
      disabled: reviewing,
      icon: <PencilIcon />,
      onSelect: () => {
        // The panel holds its own copy of the name.
        setPanelId((open) => (open === id ? null : open))
        setEditing(id, true)
      },
    },
    ...(isObject(id)
      ? [
          {
            label: 'Add state',
            icon: <PlusIcon size={16} />,
            disabled: reviewing,
            onSelect: () => updateObjectData(id, { addingState: true }),
          },
        ]
      : []),
    { label: 'Duplicate', icon: <CopyIcon />, disabled: reviewing, onSelect: () => duplicate(id) },
    { label: 'Delete', icon: <TrashIcon />, danger: true, disabled: reviewing || isBoundary(id), onSelect: () => remove(id) },
  ]

  // Nodes and edges reach the board through a context whose functions stay
  // the same, so they don't all re-render whenever the board does.
  const latest = useRef({ commitName, cancelName, remove, append, insert, addState, renameObjectState, removeState, setDecisionStates, setRoute, resize, zoomIn })
  latest.current = { commitName, cancelName, remove, append, insert, addState, renameObjectState, removeState, setDecisionStates, setRoute, resize, zoomIn }
  const actions = useMemo<BoardActions>(
    () => ({
      commitName: (id, name) => latest.current.commitName(id, name),
      cancelName: (id) => latest.current.cancelName(id),
      openPanel: (id) => setPanelId(id),
      zoomIn: (id) => latest.current.zoomIn(id),
      remove: (id) => latest.current.remove(id),
      append: (id) => latest.current.append(id),
      insert: (edgeId) => latest.current.insert(edgeId),
      addState: (objectId, name) => latest.current.addState(objectId, name),
      cancelAddState: (objectId) => updateObjectData(objectId, { addingState: false }),
      renameState: (objectId, stateId, name) => latest.current.renameObjectState(objectId, stateId, name),
      removeState: (objectId, stateId) => latest.current.removeState(objectId, stateId),
      setDecisionStates: (edgeId, states) => latest.current.setDecisionStates(edgeId, states),
      setRoute: (edgeId, route, save) => latest.current.setRoute(edgeId, route, save),
      resize: (id, x, width) => latest.current.resize(id, x, width),
      editable: !reviewing,
    }),
    [reviewing, updateObjectData],
  )

  const startTarget = edges.find((e) => e.id === START_EDGE_ID)?.target
  // A run always runs the whole flow, from the top board.
  const canRun = (reviewing || boardId !== null || startTarget !== undefined) && !starting

  // Starts a run of the board, which the server executes, and opens it. The
  // board shows the run on its version, which is the board as it is.
  const onPlay = async () => {
    setStarting(true)
    try {
      const run = await startRun()
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

  // Opens a run on the board it ran on: a recording flow process's run on
  // the flow process's board, a run of the whole flow on the top board.
  const showRun = (run: { id: number; process: number | null }) => {
    setPanelId(null)
    setVersionsOpen(false)
    setRunId(run.id)
    setBoardId(run.process)
  }

  // A run the list may not have yet, such as a flow process's that just started.
  const openRunById = async (id: number) => {
    let run = runs.find((r) => r.id === id)
    if (!run) {
      try {
        const rs = await listRuns()
        setRuns(rs)
        run = rs.find((r) => r.id === id)
      } catch (err) {
        report(err)
      }
    }
    if (run) showRun(run)
  }

  // The selected processes, and the objects among the selection, move into a
  // new flow process, which is then named.
  const selectedProcesses = nodes.filter((n) => n.selected && n.type === 'process' && !isDraft(n.id))
  const onWrap = async () => {
    if (reviewing || selectedProcesses.length === 0) return
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

  // The boards from the top one down to the one shown.
  const trail: { id: BoardId; name: string }[] = [{ id: null, name: 'Flow' }]
  for (let id = boardId; id !== null; ) {
    const p = contents?.processes.find((q) => q.id === id)
    trail.splice(1, 0, { id, name: p?.name ?? `Process ${id}` })
    id = p?.parent ?? null
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

  // Sends the decision on the review the process waits for in the open run,
  // which goes on in the server.
  const onDecide = async (id: string, state: number | null, instructions: string) => {
    const step = shownRun?.steps.filter((s) => String(s.process) === id && s.status === 'waiting').at(-1)
    if (!shownRun || !step) return
    const option = decisionOptions(id).find((o) => o.state === state)
    updateData(id, {
      status: 'done',
      review: undefined,
      output: option ? decisionText(option.name, option.object, instructions) : instructions || 'Reviewed',
    })
    try {
      await decideStep(shownRun.id, step.id, { state, instructions })
    } catch (err) {
      report(err)
    }
    setRunReload((n) => n + 1)
  }

  // Becoming or ceasing to be human drops the process's outputs, and becoming
  // human its direct connections; the server deletes the same links when it
  // saves the new kind.
  const dropMismatchedEdges = (id: string, from: ProcessKind, to: ProcessKind) => {
    if ((from === 'human') === (to === 'human')) return
    setEdges((es) =>
      es.filter((e) => {
        const l = e.data?.link
        if (l) return !(l.direction === 'output' && String(l.process) === id)
        return !(to === 'human' && e.id.startsWith(CONNECTION_PREFIX) && e.source === id)
      }),
    )
  }

  return (
    <div className="board">
      <header className="toolbar">
        <h1>
          <FlowBuilderIcon /> Flow Builder
        </h1>
        <button onClick={() => addAtViewportCenter('process')} disabled={reviewing}>
          <PlusIcon /> Process
        </button>
        <button onClick={() => addAtViewportCenter('object')} disabled={reviewing}>
          <PlusIcon /> Object
        </button>
        <button
          onClick={onWrap}
          disabled={reviewing || selectedProcesses.length === 0}
          title="Move the selected processes into a new flow process; shift-click or drag to select several"
        >
          <FlowIcon /> Group into flow
        </button>
        <button onClick={onTidy} disabled={reviewing} title="Lay the board out from the top">
          <LayoutIcon /> Tidy up
        </button>
        <span
          className="hint"
          title="Double-click the canvas to add a process, Shift+double-click to add an object. Use + on a link to insert a step, or below a node to add what comes next. Double-click an object to give it attributes, which agents generating it reply with as JSON; right-click it to add states; link a human process to an object and pick on the link the states it decides between, and link a state to the processes that take the object in it. Select a link to drag its segments; double-click it to reset its route. Double-click a process to edit it, right-click a node to rename it and more; select a connection and press Delete to remove it."
        >
          Processes connect through the objects they generate and take. Right-click a node for more.
        </span>
        <div className="versioning">
          <button onClick={onSave} disabled={versioning || reviewing} title="Save the board as a new version">
            <SaveIcon /> Save
          </button>
          <button
            className={`version-toggle${versionsOpen ? ' open' : ''}`}
            aria-expanded={versionsOpen}
            onClick={() => {
              if (!versionsOpen) {
                setPanelId(null)
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
        </div>
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
      </header>
      {error && (
        <div className="error-banner" onClick={() => setError(null)}>
          {error} (click to dismiss)
        </div>
      )}
      <div className="workspace">
        <RunList
          runs={runs}
          selected={runId}
          onSelect={(id) => {
            const run = runs.find((r) => r.id === id)
            if (run) showRun(run)
          }}
        />
        <div className={`canvas${linkingTo ? ` linking-to-${linkingTo}` : ''}`} ref={wrapper}>
          <BoardActionsContext.Provider value={actions}>
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
              onEdgeDoubleClick={(_, edge) => !reviewing && edge.data?.route && setRoute(edge.id, null, true)}
              onPaneClick={() => setPanelId(null)}
              onNodeDragStop={onNodeDragStop}
              onDoubleClick={onPaneDoubleClick}
              zoomOnDoubleClick={false}
              nodesDraggable={!reviewing}
              nodesConnectable={!reviewing}
              deleteKeyCode={reviewing ? null : undefined}
            >
              <Background variant={BackgroundVariant.Dots} gap={18} size={1.5} />
              <Controls />
            </ReactFlow>
          </BoardActionsContext.Provider>
          {trail.length > 1 && (
            <nav className="breadcrumb" aria-label="Boards">
              {trail.map((b, i) =>
                i === trail.length - 1 ? (
                  <span key={b.id ?? 'top'} aria-current="page">
                    {b.name}
                  </span>
                ) : (
                  <span key={b.id ?? 'top'}>
                    <button onClick={() => setBoardId(b.id)}>{b.name}</button>
                    <span className="breadcrumb-sep" aria-hidden="true">
                      ›
                    </span>
                  </span>
                ),
              )}
            </nav>
          )}
          {reviewing && openRun && (
            <div className="review-banner" role="status">
              {openRun.status === 'running'
                ? `Run #${openRun.id} is running on version ${shownVersion}.`
                : `Run #${openRun.id} ran on version ${shownVersion}; this is the board as it was then.`}
              <button onClick={() => setRunId(null)}>Back to the board</button>
            </div>
          )}
          {menu && (
            <ContextMenu x={menu.x} y={menu.y} items={menuItems(menu.id)} onClose={() => setMenu(null)} />
          )}
        </div>
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
            readOnly={reviewing}
            run={{ status: panelNode.data.status, output: panelNode.data.output, error: panelNode.data.runError }}
            review={
              panelNode.data.review && {
                inputs: panelNode.data.review,
                options: decisionOptions(panelNode.id),
                onDecide: (state, instructions) => onDecide(panelNode.id, state, instructions),
              }
            }
            onChange={(fields) => {
              dropMismatchedEdges(panelNode.id, panelNode.data.kind, fields.kind)
              updateData(panelNode.id, fields)
            }}
            onZoomIn={() => zoomIn(panelNode.id)}
            onClose={() => setPanelId(null)}
            onError={report}
          />
        )}
        {objectPanelNode && (
          <ObjectPanel
            key={objectPanelNode.id}
            name={objectPanelNode.data.name}
            attributes={objectPanelNode.data.attributes}
            persistent={objectPanelNode.data.persistent}
            onPersistentChange={(persistent) => setPersistent(objectPanelNode.id, persistent)}
            many={objectPanelNode.data.many}
            onManyChange={(many) => setMany(objectPanelNode.id, many)}
            editable={!reviewing}
            content={objectPanelNode.data.content}
            onRename={(name) => commitName(objectPanelNode.id, name)}
            onAdd={(name) => addAttribute(objectPanelNode.id, name)}
            onUpdate={(id, patch) => changeAttribute(objectPanelNode.id, id, patch)}
            onRemove={(id) => removeAttribute(objectPanelNode.id, id)}
            onClose={() => setPanelId(null)}
          />
        )}
        {/* A node being edited or reviewed takes the panel; the versions or run show again once it closes. */}
        {!panelNode && !objectPanelNode && versionsOpen && (
          <VersionPanel
            versions={versions.versions}
            current={versions.current}
            disabled={versioning}
            onRestore={onRestore}
            onClose={() => setVersionsOpen(false)}
          />
        )}
        {!panelNode && !objectPanelNode && !versionsOpen && runDetail && (
          <RunPanel
            run={runDetail}
            onStop={() => onStop(runDetail.id)}
            onOpenRun={openRunById}
            onClose={() => setRunId(null)}
          />
        )}
      </div>
    </div>
  )
}
