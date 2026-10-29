import { useCallback, useEffect, useState } from 'react'
import { ReactFlowProvider } from '@xyflow/react'
import { countInbox, getFlow, onInboxChanged, updateFlow, type Flow } from './api'
import Board from './Board'
import CommandPalette from './CommandPalette'
import FlowData from './FlowData'
import FlowList from './FlowList'
import Activity from './Activity'
import AuditLog from './Audit'
import Inbox, { INBOX_REFRESH_MS } from './Inbox'
import Records from './Records'
import RunCompare from './RunCompare'
import Schedules from './Schedules'
import Settings, { type SettingsTab } from './Settings'
import { ShortcutSheet, useShortcuts } from './Keyboard'
import TabBar from './TabBar'
import TaskBoard from './TaskBoard'
import Trash from './Trash'
import { TabContext, useTabs } from './tabs'

// The page the URL's hash names: #/flows/<id> for a flow's board,
// #/flows/<id>/runs/<run> for it with one of its runs open, and with
// /steps/<step> after it that step's review showing,
// #/flows/<id>/compare/<run>/<run> for two of its runs side by side,
// #/flows/<id>/data for what its runs held, #/flows/<id>/records for a
// shared struct's records, both with ?q=<search>&sort=<key> for what they
// show, #/taskboards/<id> for a task board, #/audit for the audit log,
// with them too, #/schedules for the schedules, #/inbox for what waits on
// the person, #/settings/<tab> for the settings, #/activity for what goes
// on across the flows, with ?days=<n> for its period, #/trash for the
// flows deleted, anything else for the list of flows. #/people, where the
// people were before the settings, is their tab now. The address is the
// open tab's; each tab keeps its own (tabs.ts).
type Page =
  | { flow: number; run: number | null; step: number | null }
  | { compare: number; a: number; b: number }
  | { data: number }
  | { records: number }
  | { taskBoard: number }
  | { settings: SettingsTab | null }
  | 'schedules'
  | 'audit'
  | 'activity'
  | 'inbox'
  | 'trash'
  | null

const INBOX = '#/inbox'

const pageInHash = (): Page => {
  if (window.location.hash === '#/schedules') return 'schedules'
  if (window.location.hash === INBOX) return 'inbox'
  if (window.location.hash === '#/trash') return 'trash'
  if (/^#\/audit(?:\?.*)?$/.test(window.location.hash)) return 'audit'
  if (/^#\/activity(?:\?.*)?$/.test(window.location.hash)) return 'activity'
  if (window.location.hash === '#/people') return { settings: 'people' }
  const s = /^#\/settings(?:\/(\w+))?$/.exec(window.location.hash)
  if (s) return { settings: (s[1] as SettingsTab | undefined) ?? null }
  const c = /^#\/flows\/(\d+)\/compare\/(\d+)\/(\d+)$/.exec(window.location.hash)
  if (c) return { compare: Number(c[1]), a: Number(c[2]), b: Number(c[3]) }
  const d = /^#\/flows\/(\d+)\/data(?:\?.*)?$/.exec(window.location.hash)
  if (d) return { data: Number(d[1]) }
  const r = /^#\/flows\/(\d+)\/records(?:\?.*)?$/.exec(window.location.hash)
  if (r) return { records: Number(r[1]) }
  const t = /^#\/taskboards\/(\d+)$/.exec(window.location.hash)
  if (t) return { taskBoard: Number(t[1]) }
  const m = /^#\/flows\/(\d+)(?:\/runs\/(\d+)(?:\/steps\/(\d+))?)?$/.exec(window.location.hash)
  return m ? { flow: Number(m[1]), run: m[2] ? Number(m[2]) : null, step: m[3] ? Number(m[3]) : null } : null
}

// How many steps wait on the person, asked every so often, again once they
// decide one, and as they come back to the page; null until known.
function useInboxCount(): number | null {
  const [count, setCount] = useState<number | null>(null)
  useEffect(() => {
    const ask = () => void countInbox().then((n) => n !== null && setCount(n))
    ask()
    const t = window.setInterval(ask, INBOX_REFRESH_MS)
    const onVisible = () => document.visibilityState === 'visible' && ask()
    document.addEventListener('visibilitychange', onVisible)
    const off = onInboxChanged(ask)
    return () => {
      window.clearInterval(t)
      document.removeEventListener('visibilitychange', onVisible)
      off()
    }
  }, [])
  return count
}

// The list of flows has an address of #/ or none.
const sameHash = (a: string, b: string) => (a || '#/') === (b || '#/')

export default function App() {
  const { tabs, active, select, add, close } = useTabs()
  const inbox = useInboxCount()
  // A page opens in the tab showing it, if one does, else in a new one.
  const openPage = (hash: string) => {
    if (sameHash(window.location.hash, hash)) return
    const open = tabs.find((t) => t.id !== active && sameHash(t.hash, hash))
    if (open) select(open.id)
    else add(hash)
  }
  const onInbox = () => openPage(INBOX)
  // The command palette and the sheet of shortcuts, over any page.
  const [palette, setPalette] = useState(false)
  const [sheet, setSheet] = useState(false)
  useShortcuts({
    palette: () => setPalette((open) => !open),
    sheet: () => setSheet(true),
  })
  return (
    <>
      <TabBar
        tabs={tabs}
        active={active}
        inbox={inbox}
        onSelect={select}
        onAdd={() => add('#/', true)}
        onClose={close}
        onInbox={onInbox}
      />
      <div className="tab-page">
        {/* A new page for each tab, so nothing of one shows on another. */}
        <TabContext.Provider value={active}>
          <Page key={active} />
        </TabContext.Provider>
      </div>
      {palette && <CommandPalette onOpen={openPage} onShortcuts={() => setSheet(true)} onClose={() => setPalette(false)} />}
      {sheet && <ShortcutSheet onClose={() => setSheet(false)} />}
    </>
  )
}

function Page() {
  const page = pageInHash()
  if (page === null) return <FlowList onOpen={(id) => (window.location.hash = `#/flows/${id}`)} />
  if (page === 'schedules') return <Schedules />
  if (page === 'audit') return <AuditLog />
  if (page === 'activity') return <Activity />
  if (page === 'inbox') return <Inbox />
  if (page === 'trash') return <Trash />
  if ('settings' in page) return <Settings tab={page.settings} />
  if ('compare' in page) return <RunCompare key={`${page.a}-${page.b}`} flow={page.compare} a={page.a} b={page.b} />
  if ('data' in page) return <FlowData key={page.data} id={page.data} />
  if ('records' in page) return <Records key={page.records} id={page.records} />
  if ('taskBoard' in page) return <TaskBoard key={page.taskBoard} id={page.taskBoard} />
  // A new board for each flow, so nothing of one shows on another.
  return <FlowPage key={page.flow} id={page.flow} run={page.run} step={page.step} />
}

function FlowPage({ id, run, step }: { id: number; run: number | null; step: number | null }) {
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
      <Board flow={flow} initialRun={run} initialStep={step} onUpdate={onUpdate} onReload={onReload} />
    </ReactFlowProvider>
  )
}
