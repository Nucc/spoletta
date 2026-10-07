import { useCallback, useEffect, useRef, useState } from 'react'
import type { MouseEvent } from 'react'
import {
  Background,
  Controls,
  ReactFlow,
  useNodesState,
  useReactFlow,
  type NodeMouseHandler,
  type OnNodeDrag,
} from '@xyflow/react'
import ProcessNode, { type ProcessNodeData, type ProcessNodeType } from './ProcessNode'
import { createProcess, listProcesses, updateProcess, type Process } from './api'

const nodeTypes = { process: ProcessNode }

const NODE_SIZE = 120
const DRAFT_PREFIX = 'draft-'

export default function Board() {
  const [nodes, setNodes, onNodesChange] = useNodesState<ProcessNodeType>([])
  const [error, setError] = useState<string | null>(null)
  const { getNode, screenToFlowPosition } = useReactFlow<ProcessNodeType>()
  const wrapper = useRef<HTMLDivElement>(null)
  const draftSeq = useRef(0)

  const report = useCallback((err: unknown) => {
    console.error(err)
    setError(err instanceof Error ? err.message : String(err))
  }, [])

  const updateData = useCallback(
    (id: string, data: Partial<ProcessNodeData>) =>
      setNodes((ns) => ns.map((n) => (n.id === id ? { ...n, data: { ...n.data, ...data } } : n))),
    [setNodes],
  )

  const onCancel = useCallback(
    (id: string) => {
      if (id.startsWith(DRAFT_PREFIX)) {
        setNodes((ns) => ns.filter((n) => n.id !== id))
      } else {
        updateData(id, { editing: false })
      }
    },
    [setNodes, updateData],
  )

  const onCommit = useCallback(
    async (id: string, name: string) => {
      updateData(id, { name, editing: false })
      const node = getNode(id)
      if (!node) return
      try {
        if (id.startsWith(DRAFT_PREFIX)) {
          const created = await createProcess({ name, x: node.position.x, y: node.position.y })
          setNodes((ns) => ns.map((n) => (n.id === id ? { ...n, id: String(created.id) } : n)))
        } else {
          await updateProcess(Number(id), { name })
        }
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
      data: { name: p.name, editing: false, onCommit, onCancel },
    }),
    [onCommit, onCancel],
  )

  useEffect(() => {
    listProcesses()
      .then((ps) => setNodes(ps.map(toNode)))
      .catch(report)
  }, [setNodes, toNode, report])

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
          data: { name: '', editing: true, onCommit, onCancel },
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

  const onNodeDoubleClick: NodeMouseHandler<ProcessNodeType> = (_, node) => {
    updateData(node.id, { editing: true })
  }

  const onNodeDragStop: OnNodeDrag<ProcessNodeType> = (_, __, dragged) => {
    for (const n of dragged) {
      if (n.id.startsWith(DRAFT_PREFIX)) continue
      updateProcess(Number(n.id), { x: n.position.x, y: n.position.y }).catch(report)
    }
  }

  return (
    <div className="board">
      <header className="toolbar">
        <h1>Flow Builder</h1>
        <button onClick={addAtViewportCenter}>+ Process</button>
        <span className="hint">Double-click the canvas to add a process, double-click a process to rename it.</span>
      </header>
      {error && (
        <div className="error" onClick={() => setError(null)}>
          {error} (click to dismiss)
        </div>
      )}
      <div className="canvas" ref={wrapper}>
        <ReactFlow
          nodes={nodes}
          nodeTypes={nodeTypes}
          onNodesChange={onNodesChange}
          onNodeDoubleClick={onNodeDoubleClick}
          onNodeDragStop={onNodeDragStop}
          onDoubleClick={onPaneDoubleClick}
          zoomOnDoubleClick={false}
          deleteKeyCode={null}
        >
          <Background />
          <Controls />
        </ReactFlow>
      </div>
    </div>
  )
}
