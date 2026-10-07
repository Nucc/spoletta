import { useCallback, useEffect, useRef, useState } from 'react'
import type { MouseEvent } from 'react'
import {
  Background,
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
  type OnEdgesDelete,
  type OnNodeDrag,
} from '@xyflow/react'
import ObjectNode, { type ObjectNodeData, type ObjectNodeType } from './ObjectNode'
import ProcessNode, { type ProcessNodeData, type ProcessNodeType } from './ProcessNode'
import ProcessPanel from './ProcessPanel'
import StartNode, { type StartNodeType } from './StartNode'
import {
  createLink,
  createObject,
  createProcess,
  deleteConnection,
  deleteLink,
  deleteRoute,
  getStart,
  listConnections,
  listLinks,
  listModels,
  listObjects,
  listProcesses,
  listRoutes,
  runProcess,
  setRoute,
  updateObject,
  updateProcess,
  updateStart,
  type ChangeRequest,
  type Connection,
  type FlowObject,
  type Link,
  type Models,
  type Outcome,
  type Process,
  type ProcessKind,
  type Route,
  type RunInput,
  type Start,
} from './api'

type FlowNode = ProcessNodeType | ObjectNodeType | StartNodeType

const nodeTypes = { process: ProcessNode, object: ObjectNode, start: StartNode }

// Must match .process-node and .object-node in index.css; used to center new nodes.
const NODE_SIZE = {
  process: { width: 160, height: 90 },
  object: { width: 140, height: 60 },
}
const DRAFT_PREFIX = 'draft-'
const START_ID = 'start'
const START_EDGE_ID = 'start-edge'
// Direct process-to-process connections predate objects. New ones can't be
// drawn, but existing ones still show, run and can be deleted.
const CONNECTION_PREFIX = 'conn-'
const LINK_PREFIX = 'link-'
const ROUTE_PREFIX = 'route-'
// Objects and processes have separate id sequences, so object node ids are prefixed.
const OBJECT_PREFIX = 'obj-'

const markerEnd = { type: MarkerType.ArrowClosed }

// Script processes don't execute yet; they just occupy their slot in the run.
const SIMULATED_RUN_MS = 3000

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

// The decision a human process made during a run.
type Decision = { outcome: Outcome; instructions: string }

const isOutcome = (handle: string | null | undefined): handle is Outcome =>
  handle === 'approved' || handle === 'changes'

const isDraft = (id: string) => id.startsWith(DRAFT_PREFIX)
const isObject = (id: string) => id.startsWith(OBJECT_PREFIX)
const isProcess = (id: string) => id !== START_ID && !isObject(id) && !isDraft(id)

const objectNodeId = (id: number) => `${OBJECT_PREFIX}${id}`
const objectIdOf = (nodeId: string) => Number(nodeId.slice(OBJECT_PREFIX.length))

const startEdge = (target: number | string): Edge => ({
  id: START_EDGE_ID,
  source: START_ID,
  target: String(target),
  markerEnd,
})

const connectionEdge = (c: Connection): Edge => ({
  id: `${CONNECTION_PREFIX}${c.id}`,
  source: String(c.source),
  target: String(c.target),
  markerEnd,
})

// An output link points from the process to the object it generates, an
// input link from the object to the process that takes it.
const linkEdge = (l: Link): Edge => {
  const process = String(l.process)
  const object = objectNodeId(l.object)
  return {
    id: `${LINK_PREFIX}${l.id}`,
    source: l.direction === 'output' ? process : object,
    target: l.direction === 'output' ? object : process,
    markerEnd,
  }
}

const ROUTE_COLORS: Record<Outcome, string> = { approved: '#1a7f37', changes: '#bc4c00' }

// A route leaves a human process from the handle of its outcome.
const routeEdge = (r: Route): Edge => ({
  id: `${ROUTE_PREFIX}${r.id}`,
  source: String(r.process),
  sourceHandle: r.outcome,
  target: String(r.target),
  label: r.outcome === 'approved' ? 'approved' : 'changes requested',
  className: `route ${r.outcome}`,
  markerEnd: { ...markerEnd, color: ROUTE_COLORS[r.outcome] },
})

const toStartNode =(s: Start): StartNodeType => ({
  id: START_ID,
  type: 'start',
  position: { x: s.x, y: s.y },
  deletable: false,
  data: {},
})

export default function Board() {
  const [nodes, setNodes, onNodesChange] = useNodesState<FlowNode>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([])
  const [error, setError] = useState<string | null>(null)
  const [running, setRunning] = useState(false)
  const [panelId, setPanelId] = useState<string | null>(null)
  const [models, setModels] = useState<Models | null>(null)
  const { getNode, screenToFlowPosition } = useReactFlow<FlowNode>()
  const wrapper = useRef<HTMLDivElement>(null)
  const draftSeq = useRef(0)
  // Resolves the decision of each human process waiting during a run.
  const decisions = useRef(new Map<string, (d: Decision) => void>())

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

  // Inline name editing only happens for freshly created (draft) processes;
  // existing ones are edited in the side panel.
  const onCancel = useCallback((id: string) => setNodes((ns) => ns.filter((n) => n.id !== id)), [setNodes])

  const onCommit = useCallback(
    async (id: string, name: string) => {
      updateData(id, { name, editing: false })
      const node = getNode(id)
      if (!node) return
      try {
        const created = await createProcess({ name, kind: 'agent', prompt: '', script: '', model: '', thinking: '', x: node.position.x, y: node.position.y })
        setNodes((ns) => ns.map((n) => (n.id === id ? { ...n, id: String(created.id) } : n)))
      } catch (err) {
        report(err)
      }
    },
    [getNode, setNodes, updateData, report],
  )

  // New objects are named inline like processes; existing ones are renamed
  // inline too, since a name is all an object has.
  const onObjectCommit = useCallback(
    async (id: string, name: string) => {
      updateObjectData(id, { name, editing: false })
      try {
        if (!isDraft(id)) {
          await updateObject(objectIdOf(id), { name })
          return
        }
        const node = getNode(id)
        if (!node) return
        const created = await createObject({ name, x: node.position.x, y: node.position.y })
        setNodes((ns) => ns.map((n) => (n.id === id ? { ...n, id: objectNodeId(created.id) } : n)))
      } catch (err) {
        report(err)
      }
    },
    [getNode, setNodes, updateObjectData, report],
  )

  const onObjectCancel = useCallback(
    (id: string) => {
      if (isDraft(id)) setNodes((ns) => ns.filter((n) => n.id !== id))
      else updateObjectData(id, { editing: false })
    },
    [setNodes, updateObjectData],
  )

  const toObjectNode = useCallback(
    (o: FlowObject): ObjectNodeType => ({
      id: objectNodeId(o.id),
      type: 'object',
      position: { x: o.x, y: o.y },
      deletable: false,
      data: { name: o.name, editing: false, onCommit: onObjectCommit, onCancel: onObjectCancel },
    }),
    [onObjectCommit, onObjectCancel],
  )

  const toNode = useCallback(
    (p: Process): ProcessNodeType => ({
      id: String(p.id),
      type: 'process',
      position: { x: p.x, y: p.y },
      deletable: false,
      data: {
        name: p.name,
        kind: p.kind,
        prompt: p.prompt,
        script: p.script,
        model: p.model,
        thinking: p.thinking,
        editing: false,
        onCommit,
        onCancel,
      },
    }),
    [onCommit, onCancel],
  )

  useEffect(() => {
    listModels().then(setModels).catch(report)
    Promise.all([listProcesses(), listObjects(), getStart(), listConnections(), listLinks(), listRoutes()])
      .then(([ps, os, start, cs, ls, rs]) => {
        setNodes([toStartNode(start), ...ps.map(toNode), ...os.map(toObjectNode)])
        setEdges([
          ...(start.target !== null ? [startEdge(start.target)] : []),
          ...cs.map(connectionEdge),
          ...ls.map(linkEdge),
          ...rs.map(routeEdge),
        ])
      })
      .catch(report)
  }, [setNodes, setEdges, toNode, toObjectNode, report])

  const addDraft = useCallback(
    (type: 'process' | 'object', screenX: number, screenY: number) => {
      const center = screenToFlowPosition({ x: screenX, y: screenY })
      const { width, height } = NODE_SIZE[type]
      const base = {
        id: `${DRAFT_PREFIX}${++draftSeq.current}`,
        position: { x: center.x - width / 2, y: center.y - height / 2 },
        selected: true,
        deletable: false,
      }
      const draft: FlowNode =
        type === 'process'
          ? {
              ...base,
              type,
              data: { name: '', kind: 'agent', prompt: '', script: '', model: '', thinking: '', editing: true, onCommit, onCancel },
            }
          : { ...base, type, data: { name: '', editing: true, onCommit: onObjectCommit, onCancel: onObjectCancel } }
      setNodes((ns) => [...ns.map((n) => ({ ...n, selected: false })), draft])
    },
    [screenToFlowPosition, setNodes, onCommit, onCancel, onObjectCommit, onObjectCancel],
  )

  const addAtViewportCenter = (type: 'process' | 'object') => {
    const rect = wrapper.current?.getBoundingClientRect()
    if (rect) addDraft(type, rect.left + rect.width / 2, rect.top + rect.height / 2)
  }

  const onPaneDoubleClick = (e: MouseEvent<HTMLDivElement>) => {
    if ((e.target as HTMLElement).classList.contains('react-flow__pane')) {
      addDraft(e.shiftKey ? 'object' : 'process', e.clientX, e.clientY)
    }
  }

  const onNodeDoubleClick: NodeMouseHandler<FlowNode> = (_, node) => {
    if (isDraft(node.id)) return
    if (node.type === 'process') setPanelId(node.id)
    if (node.type === 'object') updateObjectData(node.id, { editing: true })
  }

  const panelNode = nodes.find((n): n is ProcessNodeType => n.type === 'process' && n.id === panelId)

  const onNodeDragStop: OnNodeDrag<FlowNode> = (_, __, dragged) => {
    for (const n of dragged) {
      const pos = { x: n.position.x, y: n.position.y }
      if (n.type === 'start') updateStart(pos).catch(report)
      else if (isDraft(n.id)) continue
      else if (n.type === 'object') updateObject(objectIdOf(n.id), pos).catch(report)
      else updateProcess(Number(n.id), pos).catch(report)
    }
  }

  // As in OPM, processes connect through objects: a process generates an
  // object, which another process takes as input. The start node points at a
  // process, and a human process routes each outcome of its decision to the
  // process that runs next. Drafts get a new id once saved, which would
  // orphan their edges, so they can't be connected until they have a name.
  const isValidConnection: IsValidConnection = ({ source, target, sourceHandle }) => {
    if (isDraft(source) || isDraft(target)) return false
    if (edges.some((e) => e.source === source && e.target === target && (e.sourceHandle ?? null) === (sourceHandle ?? null))) {
      return false
    }
    if (isOutcome(sourceHandle)) return isProcess(target) && target !== source
    if (source === START_ID || isObject(source)) return isProcess(target)
    return isProcess(source) && isObject(target)
  }

  const onConnect: OnConnect = async ({ source, target, sourceHandle }) => {
    try {
      if (source === START_ID) {
        // The initial node has a single outgoing transition; a new one replaces it.
        await updateStart({ target: Number(target) })
        setEdges((es) => [...es.filter((e) => e.id !== START_EDGE_ID), startEdge(target)])
      } else if (isOutcome(sourceHandle)) {
        // One route per outcome; a new one replaces it.
        const r = await setRoute({ process: Number(source), outcome: sourceHandle, target: Number(target) })
        setEdges((es) => [
          ...es.filter((e) => !(e.id.startsWith(ROUTE_PREFIX) && e.source === source && e.sourceHandle === sourceHandle)),
          routeEdge(r),
        ])
      } else {
        const l = isObject(source)
          ? await createLink({ process: Number(target), object: objectIdOf(source), direction: 'input' })
          : await createLink({ process: Number(source), object: objectIdOf(target), direction: 'output' })
        setEdges((es) => [...es, linkEdge(l)])
      }
    } catch (err) {
      report(err)
    }
  }

  const onEdgesDelete: OnEdgesDelete = (deleted) => {
    for (const e of deleted) {
      const req =
        e.id === START_EDGE_ID
          ? updateStart({ target: null })
          : e.id.startsWith(LINK_PREFIX)
            ? deleteLink(Number(e.id.slice(LINK_PREFIX.length)))
            : e.id.startsWith(ROUTE_PREFIX)
              ? deleteRoute(Number(e.id.slice(ROUTE_PREFIX.length)))
              : deleteConnection(Number(e.id.slice(CONNECTION_PREFIX.length)))
      req.catch(report)
    }
  }

  const startTarget = edges.find((e) => e.id === START_EDGE_ID)?.target
  const canRun = startTarget !== undefined && !running

  // Agent processes run their prompt in the worker's pi harness; script
  // processes are still simulated. Once a process succeeds, the processes it
  // connects to run in parallel; a failure stops its branch. Every process
  // runs at most once, so cycles terminate.
  // Returns what the process printed, or null if it failed.
  const execute = async (id: string, inputs: RunInput[], changeRequest?: ChangeRequest): Promise<string | null> => {
    const node = getNode(id)
    if (node?.type !== 'process' || node.data.kind !== 'agent') {
      await sleep(SIMULATED_RUN_MS)
      updateData(id, { status: 'done' })
      return ''
    }
    try {
      const res = await runProcess(Number(id), inputs, changeRequest)
      updateData(id, { status: res.ok ? 'done' : 'error', output: res.output, runError: res.error })
      return res.ok ? res.output : null
    } catch (err) {
      updateData(id, { status: 'error', runError: err instanceof Error ? err.message : String(err) })
      return null
    }
  }

  // A process that succeeds generates its output objects, which hold what it
  // printed. A process taking objects as input runs once all of them exist,
  // and gets the generated ones' contents with its prompt; objects no process
  // generates are given, so they exist from the start. Processes still
  // connected directly run right after their predecessor.
  //
  // A human process waits for a person to review its inputs, then runs the
  // process its decision routes to. Requesting changes passes the reviewed
  // work and instructions along, typically back to the process that made it.
  // Each decision starts a fresh pass, so such review loops can run any
  // number of times while cycles without a human still run once per pass.
  const onRun = async () => {
    if (!startTarget) return
    const add = (m: Map<string, string[]>, k: string, v: string) => m.set(k, [...(m.get(k) ?? []), v])
    const outputs = new Map<string, string[]>()
    const inputs = new Map<string, string[]>()
    const consumers = new Map<string, string[]>()
    const successors = new Map<string, string[]>()
    // `${process}:${outcome}` to the process the human's decision leads to.
    const routes = new Map<string, string>()
    const routeKey = (id: string, outcome: Outcome) => `${id}:${outcome}`
    for (const e of edges) {
      if (e.source === START_ID) continue
      if (e.id.startsWith(ROUTE_PREFIX)) {
        if (isOutcome(e.sourceHandle)) routes.set(routeKey(e.source, e.sourceHandle), e.target)
      } else if (isObject(e.target)) add(outputs, e.source, e.target)
      else if (isObject(e.source)) {
        add(inputs, e.target, e.source)
        add(consumers, e.source, e.target)
      } else add(successors, e.source, e.target)
    }
    const generated = new Set([...outputs.values()].flat())
    const names = new Map(nodes.flatMap((n) => (n.type === 'object' ? [[n.id, n.data.name] as const] : [])))
    // Object node id to its content; an object exists once it has an entry.
    const contents = new Map([...names.keys()].filter((o) => !generated.has(o)).map((o) => [o, '']))

    const decide = (id: string, review: RunInput[]) =>
      new Promise<Decision>((resolve) => {
        decisions.current.set(id, resolve)
        updateData(id, { status: 'waiting', review })
        setPanelId((open) => open ?? id)
      })

    const runProcess = async (id: string, visited: Set<string>, changeRequest?: ChangeRequest): Promise<void> => {
      if (visited.has(id)) return
      visited.add(id)
      updateData(id, { status: 'active', output: undefined, runError: undefined })
      const given = (inputs.get(id) ?? [])
        .filter((o) => generated.has(o) && contents.has(o))
        .map((o) => ({ name: names.get(o) ?? '', content: contents.get(o) ?? '' }))

      const node = getNode(id)
      if (node?.type === 'process' && node.data.kind === 'human') {
        const { outcome, instructions } = await decide(id, given)
        const next = routes.get(routeKey(id, outcome))
        if (!next) return
        const request =
          outcome === 'changes' ? { reviewer: node.data.name, instructions, reviewed: given } : undefined
        await runProcess(next, new Set(), request)
        return
      }

      const output = await execute(id, given, changeRequest)
      if (output === null) return
      const produced = outputs.get(id) ?? []
      for (const o of produced) {
        contents.set(o, output)
        updateObjectData(o, { status: 'done', content: output })
      }
      const ready = produced
        .flatMap((o) => consumers.get(o) ?? [])
        .filter((p) => (inputs.get(p) ?? []).every((o) => contents.has(o)))
      await Promise.all([...new Set([...ready, ...(successors.get(id) ?? [])])].map((p) => runProcess(p, visited)))
    }

    setRunning(true)
    decisions.current.clear()
    setNodes((ns) =>
      ns.map((n): FlowNode => {
        if (n.type === 'process') {
          return { ...n, data: { ...n.data, status: undefined, output: undefined, runError: undefined, review: undefined } }
        }
        if (n.type === 'object') return { ...n, data: { ...n.data, status: undefined, content: undefined } }
        return n
      }),
    )
    try {
      await runProcess(startTarget, new Set())
    } finally {
      setRunning(false)
    }
  }

  const onDecide = (id: string, outcome: Outcome, instructions: string) => {
    const resolve = decisions.current.get(id)
    if (!resolve) return
    decisions.current.delete(id)
    updateData(id, {
      status: 'done',
      review: undefined,
      output: outcome === 'approved' ? 'Approved' : `Changes requested:\n${instructions}`,
    })
    resolve({ outcome, instructions })
  }

  // A human process leaves only through its routes, other processes never do;
  // the server deletes the same edges when it saves the new kind.
  const dropMismatchedEdges = (id: string, kind: ProcessKind) =>
    setEdges((es) => es.filter((e) => e.source !== id || e.id.startsWith(ROUTE_PREFIX) === (kind === 'human')))

  return (
    <div className="board">
      <header className="toolbar">
        <h1>Flow Builder</h1>
        <button onClick={() => addAtViewportCenter('process')}>+ Process</button>
        <button onClick={() => addAtViewportCenter('object')}>+ Object</button>
        <span className="hint">
          Double-click the canvas to add a process, Shift+double-click to add an object. Connect a
          process to the objects it generates, and objects to the processes that take them as input.
          A human process routes its approved and changes handles to the process that runs next.
          Double-click to edit; select a connection and press Delete to remove it.
        </span>
        <button
          className="play"
          onClick={onRun}
          disabled={!canRun}
          aria-label="Run flow"
          title={
            running ? 'Running…' : canRun ? 'Run flow' : 'Connect the start node to a process first'
          }
        >
          <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
            <path d="M4 2.5v11l9-5.5z" fill="currentColor" />
          </svg>
        </button>
      </header>
      {error && (
        <div className="error" onClick={() => setError(null)}>
          {error} (click to dismiss)
        </div>
      )}
      <div className="workspace">
        <div className="canvas" ref={wrapper}>
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={onConnect}
            onEdgesDelete={onEdgesDelete}
            isValidConnection={isValidConnection}
            onNodeDoubleClick={onNodeDoubleClick}
            onPaneClick={() => setPanelId(null)}
            onNodeDragStop={onNodeDragStop}
            onDoubleClick={onPaneDoubleClick}
            zoomOnDoubleClick={false}
          >
            <Background />
            <Controls />
          </ReactFlow>
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
            run={{ status: panelNode.data.status, output: panelNode.data.output, error: panelNode.data.runError }}
            review={
              panelNode.data.review && {
                inputs: panelNode.data.review,
                routed: edges.flatMap((e) =>
                  e.id.startsWith(ROUTE_PREFIX) && e.source === panelNode.id && isOutcome(e.sourceHandle)
                    ? [e.sourceHandle]
                    : [],
                ),
                onDecide: (outcome, instructions) => onDecide(panelNode.id, outcome, instructions),
              }
            }
            onChange={(fields) => {
              if (fields.kind !== panelNode.data.kind) dropMismatchedEdges(panelNode.id, fields.kind)
              updateData(panelNode.id, fields)
            }}
            onClose={() => setPanelId(null)}
            onError={report}
          />
        )}
      </div>
    </div>
  )
}
