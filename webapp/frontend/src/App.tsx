import { useCallback, useEffect, useState } from 'react'
import { ReactFlowProvider } from '@xyflow/react'
import { getFlow, updateFlow, type Flow } from './api'
import Board from './Board'
import FlowData from './FlowData'
import FlowList from './FlowList'
import AuditLog from './Audit'
import Records from './Records'
import RunCompare from './RunCompare'
import Schedules from './Schedules'
import Settings, { type SettingsTab } from './Settings'
import TabBar from './TabBar'
import { TabContext, useTabs } from './tabs'

// The page the URL's hash names: #/flows/<id> for a flow's board,
// #/flows/<id>/runs/<run> for it with one of its runs open,
// #/flows/<id>/compare/<run>/<run> for two of its runs side by side,
// #/flows/<id>/data for what its runs held, #/flows/<id>/records for a
// shared struct's records, both with ?q=<search>&sort=<key> for what they
// show, #/audit for the audit log, with them too, #/schedules for the
// schedules, #/settings/<tab> for the settings, anything else for the list
// of flows. #/people, where the people were before the settings, is their
// tab now. The address is the open tab's; each tab keeps its own (tabs.ts).
type Page =
  | { flow: number; run: number | null }
  | { compare: number; a: number; b: number }
  | { data: number }
  | { records: number }
  | { settings: SettingsTab | null }
  | 'schedules'
  | 'audit'
  | null

const pageInHash = (): Page => {
  if (window.location.hash === '#/schedules') return 'schedules'
  if (/^#\/audit(?:\?.*)?$/.test(window.location.hash)) return 'audit'
  if (window.location.hash === '#/people') return { settings: 'people' }
  const s = /^#\/settings(?:\/(\w+))?$/.exec(window.location.hash)
  if (s) return { settings: (s[1] as SettingsTab | undefined) ?? null }
  const c = /^#\/flows\/(\d+)\/compare\/(\d+)\/(\d+)$/.exec(window.location.hash)
  if (c) return { compare: Number(c[1]), a: Number(c[2]), b: Number(c[3]) }
  const d = /^#\/flows\/(\d+)\/data(?:\?.*)?$/.exec(window.location.hash)
  if (d) return { data: Number(d[1]) }
  const r = /^#\/flows\/(\d+)\/records(?:\?.*)?$/.exec(window.location.hash)
  if (r) return { records: Number(r[1]) }
  const m = /^#\/flows\/(\d+)(?:\/runs\/(\d+))?$/.exec(window.location.hash)
  return m ? { flow: Number(m[1]), run: m[2] ? Number(m[2]) : null } : null
}

export default function App() {
  const { tabs, active, select, add, close } = useTabs()
  return (
    <>
      <TabBar tabs={tabs} active={active} onSelect={select} onAdd={() => add('#/', true)} onClose={close} />
      <div className="tab-page">
        {/* A new page for each tab, so nothing of one shows on another. */}
        <TabContext.Provider value={active}>
          <Page key={active} />
        </TabContext.Provider>
      </div>
    </>
  )
}

function Page() {
  const page = pageInHash()
  if (page === null) return <FlowList onOpen={(id) => (window.location.hash = `#/flows/${id}`)} />
  if (page === 'schedules') return <Schedules />
  if (page === 'audit') return <AuditLog />
  if ('settings' in page) return <Settings tab={page.settings} />
  if ('compare' in page) return <RunCompare key={`${page.a}-${page.b}`} flow={page.compare} a={page.a} b={page.b} />
  if ('data' in page) return <FlowData key={page.data} id={page.data} />
  if ('records' in page) return <Records key={page.records} id={page.records} />
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
