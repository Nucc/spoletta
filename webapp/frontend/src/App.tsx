import { useCallback, useEffect, useState } from 'react'
import { ReactFlowProvider } from '@xyflow/react'
import { getFlow, updateFlow, type Flow } from './api'
import Board from './Board'
import FlowData from './FlowData'
import FlowList from './FlowList'
import People from './People'
import Schedules from './Schedules'

// The page the URL's hash names: #/flows/<id> for a flow's board,
// #/flows/<id>/runs/<run> for it with one of its runs open, #/flows/<id>/data
// for what its runs held, #/schedules for the schedules, #/people for the
// people who sign in, anything else for the list of flows.
type Page = { flow: number; run: number | null } | { data: number } | 'schedules' | 'people' | null

const pageInHash = (): Page => {
  if (window.location.hash === '#/schedules') return 'schedules'
  if (window.location.hash === '#/people') return 'people'
  const d = /^#\/flows\/(\d+)\/data$/.exec(window.location.hash)
  if (d) return { data: Number(d[1]) }
  const m = /^#\/flows\/(\d+)(?:\/runs\/(\d+))?$/.exec(window.location.hash)
  return m ? { flow: Number(m[1]), run: m[2] ? Number(m[2]) : null } : null
}

export default function App() {
  const [page, setPage] = useState(pageInHash)

  useEffect(() => {
    const onHash = () => setPage(pageInHash())
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [])

  if (page === null) return <FlowList onOpen={(id) => (window.location.hash = `#/flows/${id}`)} />
  if (page === 'schedules') return <Schedules />
  if (page === 'people') return <People />
  if ('data' in page) return <FlowData key={page.data} id={page.data} />
  // A new board for each flow, so nothing of one shows on another.
  return <FlowPage key={page.flow} id={page.flow} run={page.run} />
}

function FlowPage({ id, run }: { id: number; run: number | null }) {
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

  const onUpdate = useCallback(
    async (patch: Partial<Pick<Flow, 'name' | 'module' | 'settings'>>) => setFlow(await updateFlow(id, patch)),
    [id],
  )

  // The assistant may change the flow's settings along with its board.
  const onReload = useCallback(async () => setFlow(await getFlow(id)), [id])

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
      <Board flow={flow} initialRun={run} onUpdate={onUpdate} onReload={onReload} />
    </ReactFlowProvider>
  )
}
