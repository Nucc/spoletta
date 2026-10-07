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
import ProcessNode, { type ProcessNodeData, type ProcessNodeType } from './ProcessNode'
import ProcessPanel from './ProcessPanel'
import StartNode, { type StartNodeType } from './StartNode'
import {
  createConnection,
  createProcess,
  deleteConnection,
  getStart,
  listConnections,
  listProcesses,
  updateProcess,
  updateStart,
  type Connection,
  type Process,
  type Start,
} from './api'

type FlowNode = ProcessNodeType | StartNodeType

const nodeTypes = { process: ProcessNode, start: StartNode }

const NODE_SIZE = 120
const DRAFT_PREFIX = 'draft-'
const START_ID = 'start'
const START_EDGE_ID = 'start-edge'
const CONNECTION_PREFIX = 'conn-'

const markerEnd = { type: MarkerType.ArrowClosed }

const SIMULATED_RUN_MS = 3000

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))

const isDraft =(id: string) => id.startsWith(DRAFT_PREFIX)

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

const toStartNode = (s: Start): StartNodeType => ({
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
  const { getNode, screenToFlowPosition } = useReactFlow<FlowNode>()
  const wrapper = useRef<HTMLDivElement>(null)
  const draftSeq = useRef(0)

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

  // Inline name editing only happens for freshly created (draft) processes;
  // existing ones are edited in the side panel.
  const onCancel = useCallback((id: string) => setNodes((ns) => ns.filter((n) => n.id !== id)), [setNodes])

  const onCommit = useCallback(
    async (id: string, name: string) => {
      updateData(id, { name, editing: false })
      const node = getNode(id)
      if (!node) return
      try {
        const created = await createProcess({ name, kind: 'agent', prompt: '', script: '', x: node.position.x, y: node.position.y })
        setNodes((ns) => ns.map((n) => (n.id === id ? { ...n, id: String(created.id) } : n)))
      } catch (err) {
        report(err)
      }
    },
    [getNode, setNodes, updateData, report],
  )

  const toNode = useCallback(
    (p: Process): ProcessNodeType => ({
      id: String(p.id),
      type: 'process',
      position: { x: p.x, y: p.y },
      deletable: false,
      data: { name: p.name, kind: p.kind, prompt: p.prompt, script: p.script, editing: false, onCommit, onCancel },
    }),
    [onCommit, onCancel],
  )

  useEffect(() => {
    Promise.all([listProcesses(), getStart(), listConnections()])
      .then(([ps, start, cs]) => {
        setNodes([toStartNode(start), ...ps.map(toNode)])
        setEdges([...(start.target !== null ? [startEdge(start.target)] : []), ...cs.map(connectionEdge)])
      })
      .catch(report)
  }, [setNodes, setEdges, toNode, report])

  const addDraft = useCallback(
    (screenX: number, screenY: number) => {
      const center = screenToFlowPosition({ x: screenX, y: screenY })
      const id = `${DRAFT_PREFIX}${++draftSeq.current}`
      setNodes((ns) => [
        ...ns.map((n) => ({ ...n, selected: false })),
        {
          id,
          type: 'process',
          position: { x: center.x - NODE_SIZE / 2, y: center.y - NODE_SIZE / 2 },
          selected: true,
          deletable: false,
          data: { name: '', kind: 'agent', prompt: '', script: '', editing: true, onCommit, onCancel },
        },
      ])
    },
    [screenToFlowPosition, setNodes, onCommit, onCancel],
  )

  const addAtViewportCenter = () => {
    const rect = wrapper.current?.getBoundingClientRect()
    if (rect) addDraft(rect.left + rect.width / 2, rect.top + rect.height / 2)
  }

  const onPaneDoubleClick = (e: MouseEvent<HTMLDivElement>) => {
    if ((e.target as HTMLElement).classList.contains('react-flow__pane')) {
      addDraft(e.clientX, e.clientY)
    }
  }

  const onNodeDoubleClick: NodeMouseHandler<FlowNode> = (_, node) => {
    if (node.type === 'process' && !isDraft(node.id)) setPanelId(node.id)
  }

  const panelNode = nodes.find((n): n is ProcessNodeType => n.type === 'process' && n.id === panelId)

  const onNodeDragStop: OnNodeDrag<FlowNode> = (_, __, dragged) => {
    for (const n of dragged) {
      const pos = { x: n.position.x, y: n.position.y }
      if (n.type === 'start') updateStart(pos).catch(report)
      else if (!isDraft(n.id)) updateProcess(Number(n.id), pos).catch(report)
    }
  }

  // Drafts get a new id once saved, which would orphan their edges, so they
  // can't be connected until they have a name.
  const isValidConnection: IsValidConnection = (c) =>
    c.source !== c.target &&
    c.target !== START_ID &&
    !isDraft(c.source) &&
    !isDraft(c.target) &&
    !edges.some((e) => e.source === c.source && e.target === c.target)

  const onConnect: OnConnect = async ({ source, target }) => {
    try {
      if (source === START_ID) {
        // The initial node has a single outgoing transition; a new one replaces it.
        await updateStart({ target: Number(target) })
        setEdges((es) => [...es.filter((e) => e.id !== START_EDGE_ID), startEdge(target)])
      } else {
        const c = await createConnection({ source: Number(source), target: Number(target) })
        setEdges((es) => [...es, connectionEdge(c)])
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
          : deleteConnection(Number(e.id.slice(CONNECTION_PREFIX.length)))
      req.catch(report)
    }
  }

  const startTarget = edges.find((e) => e.id === START_EDGE_ID)?.target
  const canRun = startTarget !== undefined && !running

  // Simulated run: each process is active for a while and finishes without
  // output, then the processes it connects to run in parallel. Every process
  // runs at most once, so cycles terminate.
  const onRun = async () => {
    if (!startTarget) return
    const successors = new Map<string, string[]>()
    for (const e of edges) {
      if (e.source !== START_ID) successors.set(e.source, [...(successors.get(e.source) ?? []), e.target])
    }
    const visited = new Set<string>()
    const runProcess = async (id: string): Promise<void> => {
      if (visited.has(id)) return
      visited.add(id)
      updateData(id, { status: 'active' })
      await sleep(SIMULATED_RUN_MS)
      updateData(id, { status: 'done' })
      await Promise.all((successors.get(id) ?? []).map(runProcess))
    }

    setRunning(true)
    setNodes((ns) => ns.map((n) => (n.type === 'process' ? { ...n, data: { ...n.data, status: undefined } } : n)))
    try {
      await runProcess(startTarget)
    } finally {
      setRunning(false)
    }
  }

  return (
    <div className="board">
      <header className="toolbar">
        <h1>Flow Builder</h1>
        <button onClick={addAtViewportCenter}>+ Process</button>
        <span className="hint">
          Double-click the canvas to add a process, double-click a process to edit it. Drag between
          handles to connect; select a connection and press Delete to remove it.
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
            }}
            onChange={(fields) => updateData(panelNode.id, fields)}
            onClose={() => setPanelId(null)}
            onError={report}
          />
        )}
      </div>
    </div>
  )
}
