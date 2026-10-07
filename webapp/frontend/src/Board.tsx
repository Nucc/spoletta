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
import { CopyIcon, FlowBuilderIcon, HistoryIcon, LayoutIcon, PencilIcon, PlayIcon, PlusIcon, SaveIcon, TrashIcon } from './icons'
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
  deleteState,
  duplicateText,
  endRun,
  finishStep,
  getRun,
  getStart,
  getVersionBoard,
  listConnections,
  listLinks,
  listModels,
  listObjects,
  listProcesses,
  listRuns,
  listVersions,
  renameState,
  restoreVersion,
  runProcess,
  saveVersion,
  setLinkRoute,
  setLinkStates,
  startRun,
  startStep,
  updateAttribute,
  updateObject,
  updateProcess,
  updateStart,
  type BoardContents,
  type ChangeRequest,
  type Connection,
  type Duplicate,
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
  type RunInput,
  type Start,
} from './api'

type FlowNode = ProcessNodeType | ObjectNodeType | StartNodeType
type FlowEdge = Edge<LinkEdgeData>

const nodeTypes = { process: ProcessNode, object: ObjectNode, start: StartNode }
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
// Direct process-to-process connections predate objects. New ones can't be
// drawn, but existing ones still show, run and can be deleted.
const CONNECTION_PREFIX = 'conn-'
const LINK_PREFIX = 'link-'
// Objects and processes have separate id sequences, so object node ids are prefixed.
const OBJECT_PREFIX = 'obj-'

const markerEnd = { type: MarkerType.ArrowClosed }

// Script processes don't execute yet; they just occupy their slot in the run.
const SIMULATED_RUN_MS = 3000

// How often an open run that is still going is refreshed.
const RUN_REFRESH_MS = 1500

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

// The decision a human process made during a run: the state it set, if it
// had any to choose from.
type Decision = { state: number | null; instructions: string }

// The state a handle of an object node belongs to, if any.
const stateOfHandle = (handle: string | null | undefined): number | null => {
  const m = handle?.match(/^state-(?:in|out)-(\d+)$/)
  return m ? Number(m[1]) : null
}

const isDraft = (id: string) => id.startsWith(DRAFT_PREFIX)
const isObject = (id: string) => id.startsWith(OBJECT_PREFIX)
const isProcess = (id: string) => id !== START_ID && !isObject(id) && !isDraft(id)

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
  Promise.all([listProcesses(), listObjects(), getStart(), listConnections(), listLinks()]).then(
    ([processes, objects, start, connections, links]) => ({ processes, objects, start, connections, links }),
  )

const toStartNode = (s: Start): StartNodeType => ({
  id: START_ID,
  type: 'start',
  position: { x: s.x, y: s.y },
  deletable: false,
  data: {},
})

export default function Board() {
  const [nodes, setNodes, onNodesChange] = useNodesState<FlowNode>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<FlowEdge>([])
  const [error, setError] = useState<string | null>(null)
  const [running, setRunning] = useState(false)
  const [panelId, setPanelId] = useState<string | null>(null)
  const [models, setModels] = useState<Models | null>(null)
  const [runs, setRuns] = useState<FlowRun[]>([])
  // The run open in the side panel, and its steps once loaded.
  const [runId, setRunId] = useState<number | null>(null)
  const [runDetail, setRunDetail] = useState<FlowRunDetail | null>(null)
  const [versions, setVersions] = useState<Versions>({ current: null, versions: [] })
  const [versionsOpen, setVersionsOpen] = useState(false)
  // Set while a save or a jump to another version is under way.
  const [versioning, setVersioning] = useState(false)
  // The node whose context menu is open, and where.
  const [menu, setMenu] = useState<{ id: string; x: number; y: number } | null>(null)
  const { getNode, getNodes, screenToFlowPosition, fitView } = useReactFlow<FlowNode>()
  const wrapper = useRef<HTMLDivElement>(null)
  const draftSeq = useRef(0)
  // Resolves the decision of each human process waiting during a run.
  const decisions = useRef(new Map<string, (d: Decision) => void>())
  // The run in progress, so it can be marked interrupted if the page closes.
  const liveRun = useRef<number | null>(null)

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
        ns.map((n) => (n.id === id && n.type !== 'start' ? ({ ...n, data: { ...n.data, editing } } as FlowNode) : n)),
      ),
    [setNodes],
  )

  // Processes and objects are named inline when they are created, and renamed
  // inline from their menu. A draft only gets saved once it has a name.
  const commitName = useCallback(
    async (id: string, name: string) => {
      const node = getNode(id)
      if (!node || node.type === 'start') return
      setNodes((ns) => ns.map((n) => (n.id === id ? ({ ...n, data: { ...n.data, name, editing: false } } as FlowNode) : n)))
      try {
        if (!isDraft(id)) {
          if (node.type === 'object') await updateObject(objectIdOf(id), { name })
          else await updateProcess(Number(id), { name })
          return
        }
        const { x, y } = node.position
        const newId =
          node.type === 'object'
            ? objectNodeId((await createObject({ name, x, y })).id)
            : String((await createProcess({ name, kind: 'agent', prompt: '', script: '', model: '', thinking: '', x, y })).id)
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
    (o: FlowObject, editing = false): ObjectNodeType => ({
      id: objectNodeId(o.id),
      type: 'object',
      position: { x: o.x, y: o.y },
      width: o.width ?? NODE_SIZE.object.width,
      deletable: false,
      data: { name: o.name, states: o.states, attributes: o.attributes, persistent: o.persistent, editing },
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
        editing,
      },
    }),
    [],
  )

  const refreshRuns = useCallback(() => listRuns().then(setRuns).catch(report), [report])

  const refreshVersions = useCallback(() => listVersions().then(setVersions).catch(report), [report])

  const showBoard = useCallback(
    ({ processes: ps, objects: os, start, connections: cs, links: ls }: BoardContents) => {
      const humans = new Set(ps.filter((p) => p.kind === 'human').map((p) => p.id))
      setNodes([toStartNode(start), ...ps.map((p) => toNode(p)), ...os.map((o) => toObjectNode(o))])
      setEdges([
        ...(start.target !== null ? [startEdge(start.target, start.route)] : []),
        ...cs.map(connectionEdge),
        ...ls.map((l) => linkEdge(l, humans.has(l.process))),
      ])
    },
    [setNodes, setEdges, toNode, toObjectNode],
  )

  const loadBoard = useCallback(() => fetchBoard().then(showBoard), [showBoard])

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

  // A run can't continue without the page that drives it.
  useEffect(() => {
    const onHide = () => {
      if (liveRun.current !== null) endRun(liveRun.current, 'interrupted', true).catch(() => {})
    }
    window.addEventListener('pagehide', onHide)
    return () => window.removeEventListener('pagehide', onHide)
  }, [])

  // Loads the open run, and keeps refreshing it while it goes on.
  useEffect(() => {
    setRunDetail(null)
    if (runId === null) return
    let stopped = false
    let timer: number | undefined
    const load = async () => {
      try {
        const detail = await getRun(runId)
        if (stopped) return
        setRunDetail(detail)
        if (detail.status === 'running') timer = window.setTimeout(load, RUN_REFRESH_MS)
      } catch (err) {
        report(err)
      }
    }
    load()
    return () => {
      stopped = true
      window.clearTimeout(timer)
    }
  }, [runId, report])

  // A finished run shows on the board it ran on, its version, which can't be
  // edited while the run is open. Runs from before versions were kept show
  // on the board as it is. The run list can still have the run running after
  // it ends; the run's own details can't, but they reload when another run opens.
  const finishedRun = runDetail && runDetail.status !== 'running' ? runDetail : null
  const openRun = runs.find((r) => r.id === runId)
  const runFinished = (openRun && openRun.status !== 'running') || finishedRun?.id === runId
  const runVersion = !running && runFinished ? (openRun?.version ?? null) : null
  // The version the board shows, null for the board itself.
  const [shownVersion, setShownVersion] = useState<number | null>(null)
  const reviewing = shownVersion !== null
  useEffect(() => {
    if (runVersion === shownVersion) return
    let stale = false
    ;(runVersion === null ? fetchBoard() : getVersionBoard(runVersion))
      .then((board) => {
        if (stale) return
        setPanelId(null)
        setMenu(null)
        showBoard(board)
        setShownVersion(runVersion)
      })
      .catch(report)
    return () => {
      stale = true
    }
  }, [runVersion, shownVersion, showBoard, report])

  // Shows a finished run on the board: each process as its last step in the
  // run left it, each object with the content and state it last got.
  useEffect(() => {
    if (!finishedRun || finishedRun.id !== runId || running || shownVersion !== runVersion) return
    const steps = new Map(finishedRun.steps.flatMap((s) => (s.process === null ? [] : [[String(s.process), s] as const])))
    const objects = new Map(finishedRun.objects.flatMap((o) => (o.object === null ? [] : [[objectNodeId(o.object), o] as const])))
    setNodes((ns) =>
      ns.map((n): FlowNode => {
        if (n.type === 'process') {
          const s = steps.get(n.id)
          const output =
            s?.kind === 'human' && s.outcome
              ? decisionText(s.outcome, finishedRun.objects.find((o) => o.step === s.id)?.name, s.instructions)
              : s?.output
          return {
            ...n,
            data: { ...n.data, status: s?.status, output: output || undefined, runError: s?.error || undefined, review: undefined },
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
  }, [finishedRun, runId, running, shownVersion, runVersion, setNodes])

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
          ? { ...base, type, data: { name: '', kind: 'agent', prompt: '', script: '', model: '', thinking: '', editing: true } }
          : { ...base, type, data: { name: '', states: [], attributes: [], persistent: false, editing: true } }
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

  const onNodeDoubleClick: NodeMouseHandler<FlowNode> = (_, node) => {
    if (!isDraft(node.id) && node.type !== 'start') setPanelId(node.id)
  }

  const panelNode = nodes.find((n): n is ProcessNodeType => n.type === 'process' && n.id === panelId)
  const objectPanelNode = nodes.find((n): n is ObjectNodeType => n.type === 'object' && n.id === panelId)

  const savePositions = (positions: Map<string, XYPosition>) => {
    for (const [id, { x, y }] of positions) {
      if (id === START_ID) updateStart({ x, y }).catch(report)
      else if (isDraft(id)) continue
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
    if (edgeId === START_EDGE_ID) updateStart({ route }).catch(report)
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
        await updateStart({ target: Number(target) })
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
          ? updateStart({ target: null })
          : e.id.startsWith(LINK_PREFIX)
            ? deleteLink(Number(e.id.slice(LINK_PREFIX.length)))
            : deleteConnection(Number(e.id.slice(CONNECTION_PREFIX.length)))
      req.catch(report)
    }
  }

  const newProcess = (position: XYPosition) =>
    createProcess({ name: NEW_PROCESS, kind: 'agent', prompt: '', script: '', model: '', thinking: '', ...position })

  // Where a node of the given type goes right below another, centered on it.
  const below = (node: FlowNode, type: 'process' | 'object'): XYPosition => ({
    x: node.position.x + (node.measured?.width ?? 0) / 2 - NODE_SIZE[type].width / 2,
    y: node.position.y + (node.measured?.height ?? 0) + RANK_GAP,
  })

  // Below a process goes the object it generates, below an object a process
  // that takes it. The new node starts out being named.
  const append = async (id: string) => {
    const node = getNode(id)
    if (!node || node.type === 'start' || running) return
    try {
      if (node.type === 'process') {
        const o = await createObject({ name: NEW_OBJECT, ...below(node, 'object') })
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
    if (!edge || !source || !target || running) return
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
      const o = await createObject({ name: NEW_OBJECT, ...at('object', processFirst ? secondY : firstY) })
      const links: Link[] = []
      if (processFirst) {
        if (edge.id === START_EDGE_ID) await updateStart({ target: p.id })
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
            .filter((n) => n.position.y >= firstY)
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

  // Its links go with it on the server; runs keep their record of it.
  const remove = async (id: string) => {
    const node = getNode(id)
    if (!node || node.type === 'start' || running) return
    if (!window.confirm(`Delete “${node.data.name}” and its links?`)) return
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
    if (!node || node.type === 'start') return
    const position = { x: node.position.x + DUPLICATE_OFFSET, y: node.position.y + DUPLICATE_OFFSET }
    try {
      const { name } = node.data
      const width = node.width ?? null
      const copy: FlowNode =
        node.type === 'object'
          ? toObjectNode(await createObject({ name, width, ...position }))
          : toNode(
              await createProcess({
                name,
                kind: node.data.kind,
                prompt: node.data.prompt,
                script: node.data.script,
                model: node.data.model,
                thinking: node.data.thinking,
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
    if (!state || running || !window.confirm(`Delete state “${state.name}” and its links?`)) return
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
    if (!l || running) return
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
    if (running) return false
    try {
      const attribute = await createAttribute(objectIdOf(objectId), { name, definition: '' })
      setAttributes(objectId, (as) => [...as, attribute])
      return true
    } catch (err) {
      report(err)
      return false
    }
  }

  const setPersistent = async (objectId: string, persistent: boolean) => {
    if (running) return false
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
    if (!attribute || running || !window.confirm(`Delete attribute “${attribute.name}”?`)) return false
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
    if (!reviewing && node.type !== 'start' && !isDraft(node.id)) setMenu({ id: node.id, x: e.clientX, y: e.clientY })
  }

  const menuItems = (id: string): MenuItem[] => [
    {
      label: 'Rename',
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
            disabled: running,
            onSelect: () => updateObjectData(id, { addingState: true }),
          },
        ]
      : []),
    { label: 'Duplicate', icon: <CopyIcon />, onSelect: () => duplicate(id) },
    { label: 'Delete', icon: <TrashIcon />, danger: true, disabled: running, onSelect: () => remove(id) },
  ]

  // Nodes and edges reach the board through a context whose functions stay
  // the same, so they don't all re-render whenever the board does.
  const latest = useRef({ commitName, cancelName, remove, append, insert, addState, renameObjectState, removeState, setDecisionStates, setRoute, resize })
  latest.current = { commitName, cancelName, remove, append, insert, addState, renameObjectState, removeState, setDecisionStates, setRoute, resize }
  const actions = useMemo<BoardActions>(
    () => ({
      commitName: (id, name) => latest.current.commitName(id, name),
      cancelName: (id) => latest.current.cancelName(id),
      openPanel: (id) => setPanelId(id),
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
      editable: !running && !reviewing,
    }),
    [running, reviewing, updateObjectData],
  )

  const startTarget = edges.find((e) => e.id === START_EDGE_ID)?.target
  // Set while going back from a run's version to the board, to run it then.
  const [queuedRun, setQueuedRun] = useState(false)
  const canRun = (reviewing || startTarget !== undefined) && !running && !queuedRun

  // Agent processes run their prompt in the worker's pi harness; script
  // processes are still simulated. Each execution is recorded as a step of
  // the flow run; the server records agent steps itself. Returns what the
  // process printed and, for output objects with attributes, what each holds;
  // or null if it failed. A duplicate means the server terminated the run.
  const execute = async (
    run: number,
    id: string,
    inputs: RunInput[],
    changeRequest?: ChangeRequest,
  ): Promise<{ output: string; objects?: Record<string, string>; duplicate?: Duplicate } | null> => {
    const node = getNode(id)
    if (node?.type !== 'process' || node.data.kind !== 'agent') {
      const step = await startStep(run, { process: Number(id), status: 'active', inputs }).catch(report)
      await sleep(SIMULATED_RUN_MS)
      const finished = step ? await finishStep(run, step.id, { status: 'done', output: '' }).catch(report) : undefined
      const duplicate = finished?.duplicate
      updateData(id, duplicate ? { status: 'duplicate', runError: duplicateText(duplicate) } : { status: 'done' })
      return { output: '', duplicate }
    }
    try {
      const res = await runProcess(Number(id), inputs, changeRequest, run)
      if (res.duplicate) {
        updateData(id, { status: 'duplicate', output: res.output, runError: duplicateText(res.duplicate) })
        return { output: res.output, objects: res.objects, duplicate: res.duplicate }
      }
      updateData(id, { status: res.ok ? 'done' : 'error', output: res.output, runError: res.error })
      return res.ok ? { output: res.output, objects: res.objects } : null
    } catch (err) {
      updateData(id, { status: 'error', runError: err instanceof Error ? err.message : String(err) })
      return null
    }
  }

  // A process that succeeds generates its output objects, which hold what it
  // printed, or for an object with attributes its part of the checked reply,
  // each in the state its link names or in none. Then the processes
  // taking those objects run, in parallel:
  // - A link without a state takes the object whatever state it is in. Its
  //   process runs once every object it takes that way exists; objects no
  //   process generates are given, so they exist from the start.
  // - A link naming a state is a condition: its process runs whenever the
  //   object enters that state, with whichever objects it takes without a
  //   state exist. Several such links are alternatives, not all required.
  // A failure stops its branch, and every process runs at most once per pass,
  // so cycles end. Processes still connected directly run right after their
  // predecessor.
  //
  // A human process waits for a person to review its inputs and set an object
  // to one of the states its output links name; the object keeps its content.
  // Instructions they leave go as a change request to the processes taking
  // the object in that state. Each decision starts a fresh pass, so review
  // loops can repeat while cycles without a human still run once per pass.
  //
  // A step generating a unique value another run stored terminates the run:
  // no further process starts, and reviews still waiting end undecided.
  const onRun = async () => {
    if (!startTarget) return
    type End = { object: string; state: number | null }
    const add = <T,>(m: Map<string, T[]>, k: string, v: T) => m.set(k, [...(m.get(k) ?? []), v])
    // Process to the objects it generates, or the states a human decides on.
    const outputs = new Map<string, End[]>()
    // Process to the objects it takes, and object to the processes taking it.
    const inputs = new Map<string, End[]>()
    const takers = new Map<string, { process: string; state: number | null }[]>()
    const successors = new Map<string, string[]>()
    for (const e of edges) {
      const l = e.data?.link
      if (l) {
        const process = String(l.process)
        const object = objectNodeId(l.object)
        if (l.direction === 'output') {
          // A human's link stands for each state it can set.
          if (l.states.length > 0) for (const state of l.states) add(outputs, process, { object, state })
          else add(outputs, process, { object, state: l.state })
        }
        else {
          add(inputs, process, { object, state: l.state })
          add(takers, object, { process, state: l.state })
        }
      } else if (e.id.startsWith(CONNECTION_PREFIX)) add(successors, e.source, e.target)
    }
    const objects = new Map(nodes.flatMap((n) => (n.type === 'object' ? [[n.id, n.data] as const] : [])))
    const stateNames = new Map([...objects.values()].flatMap((o) => o.states.map((s) => [s.id, s.name] as const)))
    const stateObjects = new Map([...objects].flatMap(([id, o]) => o.states.map((s) => [s.id, id] as const)))
    const humans = new Set(nodes.flatMap((n) => (n.type === 'process' && n.data.kind === 'human' ? [n.id] : [])))
    const generated = new Set([...outputs].flatMap(([p, ends]) => (humans.has(p) ? [] : ends.map((e) => e.object))))

    // Each object that exists: its content, state and the instructions of the
    // human who set that state.
    type Value = { content: string; state: number | null; review?: { reviewer: string; instructions: string } }
    const values = new Map<string, Value>([...objects.keys()].filter((o) => !generated.has(o)).map((o) => [o, { content: '', state: null }]))
    const asInput = (o: string): RunInput => {
      const v = values.get(o)
      const state = v?.state != null ? stateNames.get(v.state) : undefined
      return { name: objects.get(o)?.name ?? '', ...(state && { state }), content: v?.content ?? '' }
    }
    const plainInputs = (p: string) => (inputs.get(p) ?? []).filter((i) => i.state === null)
    const ready = (p: string) => plainInputs(p).length > 0 && plainInputs(p).every((i) => values.has(i.object))
    const takersIn = (object: string, state: number) =>
      (takers.get(object) ?? []).filter((t) => t.state === state).map((t) => t.process)
    const showObject = (o: string, v: Value) =>
      updateObjectData(o, {
        status: 'done',
        content: v.content,
        activeState: v.state === null ? undefined : stateNames.get(v.state),
      })

    const decide = (id: string, review: RunInput[]) =>
      new Promise<Decision>((resolve) => {
        decisions.current.set(id, resolve)
        updateData(id, { status: 'waiting', review })
        setPanelId((open) => open ?? id)
      })

    let failed = false
    let terminated = false
    const terminate = () => {
      terminated = true
      for (const [id, resolve] of decisions.current) {
        updateData(id, { status: undefined, review: undefined })
        resolve({ state: null, instructions: '' })
      }
      decisions.current.clear()
    }
    // trigger: the object whose entering a state the process takes it in made it run.
    const runProcess = async (run: number, id: string, visited: Set<string>, trigger?: string): Promise<void> => {
      if (terminated || visited.has(id)) return
      visited.add(id)
      updateData(id, { status: 'active', output: undefined, runError: undefined })
      const given = plainInputs(id)
        .filter((i) => generated.has(i.object) && values.has(i.object))
        .map((i) => asInput(i.object))
      const node = getNode(id)
      const human = node?.type === 'process' && node.data.kind === 'human'
      const review = trigger ? values.get(trigger)?.review : undefined
      let changeRequest: ChangeRequest | undefined
      if (trigger && review && !human) {
        const state = values.get(trigger)?.state
        changeRequest = {
          reviewer: review.reviewer,
          state: state != null ? stateNames.get(state) : undefined,
          instructions: review.instructions,
          reviewed: [asInput(trigger)],
        }
      } else if (trigger) given.push(asInput(trigger))

      if (human) {
        const step = await startStep(run, { process: Number(id), status: 'waiting', inputs: given }).catch(report)
        const { state, instructions } = await decide(id, given)
        // Ending the run closes the step.
        if (terminated) return
        if (step) {
          await finishStep(run, step.id, { status: 'done', state: state ?? undefined, instructions }).catch(report)
        }
        const object = state === null ? undefined : stateObjects.get(state)
        if (state === null || !object) return
        const value = {
          content: values.get(object)?.content ?? '',
          state,
          review: instructions ? { reviewer: node.data.name, instructions } : undefined,
        }
        values.set(object, value)
        showObject(object, value)
        await Promise.all(takersIn(object, state).map((p) => runProcess(run, p, new Set(), object)))
        return
      }

      const result = await execute(run, id, given, changeRequest)
      if (result === null) {
        failed = true
        return
      }
      const produced = outputs.get(id) ?? []
      for (const { object, state } of produced) {
        const content = result.objects?.[objectIdOf(object)] ?? result.output
        values.set(object, { content, state })
        showObject(object, { content, state })
      }
      if (result.duplicate) terminate()
      if (terminated) return
      const next = produced.flatMap(({ object }) =>
        (takers.get(object) ?? []).filter((t) => t.state === null && ready(t.process)).map((t) => t.process),
      )
      const triggered = produced.flatMap(({ object, state }) =>
        state === null ? [] : takersIn(object, state).map((p) => [p, object] as const),
      )
      await Promise.all([
        ...[...next, ...(successors.get(id) ?? [])].map((p) => runProcess(run, p, visited)),
        ...triggered.map(([p, object]) => runProcess(run, p, visited, object)),
      ])
    }

    let run: FlowRun
    try {
      run = await startRun()
    } catch (err) {
      report(err)
      return
    }
    liveRun.current = run.id
    // Starting the run may have saved the board as a new version.
    refreshVersions()
    setRuns((rs) => [run, ...rs])
    setRunId(run.id)
    setVersionsOpen(false)
    setRunning(true)
    decisions.current.clear()
    setNodes((ns) =>
      ns.map((n): FlowNode => {
        if (n.type === 'process') {
          return { ...n, data: { ...n.data, status: undefined, output: undefined, runError: undefined, review: undefined } }
        }
        if (n.type === 'object') {
          return { ...n, data: { ...n.data, status: undefined, content: undefined, activeState: undefined } }
        }
        return n
      }),
    )
    try {
      await runProcess(run.id, startTarget, new Set())
    } catch (err) {
      failed = true
      report(err)
    } finally {
      liveRun.current = null
      await endRun(run.id, failed ? 'failed' : 'done').catch(report)
      setRunning(false)
      refreshRuns()
    }
  }

  // The flow runs as the board has it, so a run's version shown on the board
  // makes way for the board first.
  const onPlay = () => {
    if (!reviewing) {
      onRun()
      return
    }
    setRunId(null)
    setQueuedRun(true)
  }
  useEffect(() => {
    if (!queuedRun || reviewing) return
    setQueuedRun(false)
    onRun()
  })

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

  const onDecide = (id: string, state: number | null, instructions: string) => {
    const resolve = decisions.current.get(id)
    if (!resolve) return
    decisions.current.delete(id)
    const option = decisionOptions(id).find((o) => o.state === state)
    updateData(id, {
      status: 'done',
      review: undefined,
      output: option ? decisionText(option.name, option.object, instructions) : instructions || 'Reviewed',
    })
    resolve({ state, instructions })
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
          <button onClick={onSave} disabled={running || versioning || reviewing} title="Save the board as a new version">
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
            running ? 'Running…' : canRun ? 'Run flow' : 'Connect the start node to a process first'
          }
        >
          <PlayIcon />
        </button>
      </header>
      {error && (
        <div className="error" onClick={() => setError(null)}>
          {error} (click to dismiss)
        </div>
      )}
      <div className="workspace">
        <RunList
          runs={runs}
          selected={runId}
          onSelect={(id) => {
            setPanelId(null)
            setVersionsOpen(false)
            setRunId(id)
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
              onEdgeDoubleClick={(_, edge) => !running && !reviewing && edge.data?.route && setRoute(edge.id, null, true)}
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
          {reviewing && openRun && (
            <div className="review-banner" role="status">
              Run #{openRun.id} ran on version {shownVersion}; this is the board as it was then.
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
            editable={!running && !reviewing}
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
            disabled={running || versioning}
            onRestore={onRestore}
            onClose={() => setVersionsOpen(false)}
          />
        )}
        {!panelNode && !objectPanelNode && !versionsOpen && runDetail && (
          <RunPanel run={runDetail} onClose={() => setRunId(null)} />
        )}
      </div>
    </div>
  )
}
