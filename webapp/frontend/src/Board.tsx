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
import AgentSettingsDialog from './AgentSettingsDialog'
import StartNode, { type StartNodeType } from './StartNode'
import {
  createConnection,
  createProcess,
  deleteConnection,
  getAgentSettings,
  getStart,
  listConnections,
  listProcesses,
  runProcess,
  updateProcess,
  updateStart,
  type AgentSettings,
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

// Script processes don't execute yet; they just occupy their slot in the run.
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
  const [settings, setSettings] = useState<AgentSettings | null>(null)
  const [settingsOpen, setSettingsOpen] = useState(false)
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
    getAgentSettings().then(setSettings).catch(report)
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
  const agentConfigured = settings !== null && settings.model !== '' && settings.hasApiKey

  // Agent processes run their prompt in the worker's pi harness; script
  // processes are still simulated. Once a process succeeds, the processes it
  // connects to run in parallel; a failure stops its branch. Every process
  // runs at most once, so cycles terminate.
  const execute = async (id: string): Promise<boolean> => {
    const node = getNode(id)
    if (node?.type !== 'process' || node.data.kind !== 'agent') {
      await sleep(SIMULATED_RUN_MS)
      updateData(id, { status: 'done' })
      return true
    }
    try {
      const res = await runProcess(Number(id))
      updateData(id, { status: res.ok ? 'done' : 'error', output: res.output, runError: res.error })
      return res.ok
    } catch (err) {
      updateData(id, { status: 'error', runError: err instanceof Error ? err.message : String(err) })
      return false
    }
  }

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
      updateData(id, { status: 'active', output: undefined, runError: undefined })
      if (!(await execute(id))) return
      await Promise.all((successors.get(id) ?? []).map(runProcess))
    }

    setRunning(true)
    setNodes((ns) =>
      ns.map((n) =>
        n.type === 'process' ? { ...n, data: { ...n.data, status: undefined, output: undefined, runError: undefined } } : n,
      ),
    )
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
          className={`settings${settings && !agentConfigured ? ' unconfigured' : ''}`}
          onClick={() => setSettingsOpen(true)}
          disabled={settings === null}
          aria-label="Agent settings"
          title={settings && !agentConfigured ? 'Agent settings (not configured yet)' : 'Agent settings'}
        >
          <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
            <path
              fill="currentColor"
              d="M8 0a8.2 8.2 0 0 1 .701.031C9.444.095 9.99.645 10.16 1.29l.288 1.107c.018.066.079.158.212.224.231.114.454.243.668.386.123.082.233.09.299.071l1.103-.303c.644-.176 1.392.021 1.82.63.27.385.506.792.704 1.218.315.675.111 1.422-.364 1.891l-.814.806c-.049.048-.098.147-.088.294.016.257.016.515 0 .772-.01.147.038.246.088.294l.814.806c.475.469.679 1.216.364 1.891a7.977 7.977 0 0 1-.704 1.217c-.428.61-1.176.807-1.82.63l-1.102-.302c-.067-.019-.177-.011-.3.071a5.909 5.909 0 0 1-.668.386c-.133.066-.194.158-.211.224l-.29 1.106c-.168.646-.715 1.196-1.458 1.26a8.006 8.006 0 0 1-1.402 0c-.743-.064-1.289-.614-1.458-1.26l-.289-1.106c-.018-.066-.079-.158-.212-.224a5.738 5.738 0 0 1-.668-.386c-.123-.082-.233-.09-.299-.071l-1.103.303c-.644.176-1.392-.021-1.82-.63a8.12 8.12 0 0 1-.704-1.218c-.315-.675-.111-1.422.363-1.891l.815-.806c.05-.048.098-.147.088-.294a6.214 6.214 0 0 1 0-.772c.01-.147-.038-.246-.088-.294l-.815-.806C.635 6.045.431 5.298.746 4.623a7.92 7.92 0 0 1 .704-1.217c.428-.61 1.176-.807 1.82-.63l1.102.302c.067.019.177.011.3-.071.214-.143.437-.272.668-.386.133-.066.194-.158.211-.224l.29-1.106C6.009.645 6.556.095 7.299.03 7.53.01 7.764 0 8 0Zm0 5.5a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5Z"
            />
          </svg>
        </button>
        <button
          className="play"
          onClick={onRun}
          disabled={!canRun}
          aria-label="Run flow"
          title={
            running
              ? 'Running…'
              : !canRun
                ? 'Connect the start node to a process first'
                : agentConfigured
                  ? 'Run flow'
                  : 'Run flow (agent processes will fail until the agent is configured)'
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
            run={{ status: panelNode.data.status, output: panelNode.data.output, error: panelNode.data.runError }}
            onChange={(fields) => updateData(panelNode.id, fields)}
            onClose={() => setPanelId(null)}
            onError={report}
          />
        )}
      </div>
      {settingsOpen && settings && (
        <AgentSettingsDialog initial={settings} onSaved={setSettings} onClose={() => setSettingsOpen(false)} />
      )}
    </div>
  )
}
