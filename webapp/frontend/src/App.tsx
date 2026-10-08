import { useCallback, useEffect, useState } from 'react'
import { ReactFlowProvider } from '@xyflow/react'
import { getFlow, renameFlow, type Flow } from './api'
import Board from './Board'
import FlowList from './FlowList'

// The page the URL's hash names: #/flows/<id> for a flow's board, anything
// else for the list of flows.
const flowInHash = () => {
  const m = /^#\/flows\/(\d+)$/.exec(window.location.hash)
  return m ? Number(m[1]) : null
}

export default function App() {
  const [flowId, setFlowId] = useState(flowInHash)

  useEffect(() => {
    const onHash = () => setFlowId(flowInHash())
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [])

  if (flowId === null) return <FlowList onOpen={(id) => (window.location.hash = `#/flows/${id}`)} />
  // A new board for each flow, so nothing of one shows on another.
  return <FlowPage key={flowId} id={flowId} />
}

function FlowPage({ id }: { id: number }) {
  const [flow, setFlow] = useState<Flow | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    getFlow(id)
      .then(setFlow)
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
  }, [id])

  useEffect(() => {
    if (flow) document.title = `${flow.name} · Flows`
  }, [flow])

  const onRename = useCallback(async (name: string) => setFlow(await renameFlow(id, name)), [id])

  if (error) {
    return (
      <div className="flows-page">
        <div className="error-banner" role="alert">
          {error}
        </div>
        <p className="flows">
          <a href="#/">Back to every flow</a>
        </p>
      </div>
    )
  }
  if (!flow) return null
  return (
    <ReactFlowProvider>
      <Board flow={flow} onRename={onRename} />
    </ReactFlowProvider>
  )
}
