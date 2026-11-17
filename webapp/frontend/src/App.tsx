import { useCallback, useEffect, useLayoutEffect, useReducer, useState } from 'react'
import { ReactFlowProvider } from '@xyflow/react'
import { countInbox, getFlow, onInboxChanged, onRefusedHere, updateFlow, type Flow } from './api'
import Board from './Board'
import CommandPalette from './CommandPalette'
import FlowData from './FlowData'
import FlowList from './FlowList'
import Activity from './Activity'
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
import ErrorBanner from './ErrorBanner'
import { reloadMe, useMe } from './Account'
import { NoProject, ProjectsPage } from './Projects'
import {
  currentPage,
  hashIn,
  lastProject,
  outsideProjects,
  pageHash,
  projectFor,
  rememberProject,
  rewritten,
  splitHash,
} from './address'

// The page the URL's hash names, within the project it starts with
// (#/p/<id>, address.ts): #/flows/<id> for a flow's board,
// #/flows/<id>/runs/<run> for it with one of its runs open, and with
// /steps/<step> after it that step's review showing,
// #/flows/<id>/compare/<run>/<run> for two of its runs side by side,
// #/flows/<id>/data for what its runs held, #/flows/<id>/records for a
// shared struct's records, both with ?q=<search>&sort=<key> for what they
// show, #/taskboards/<id> for a task board, #/schedules for the
// schedules, #/inbox for what waits on the person, #/settings/<tab> for
// the settings, #/settings/audit with ?q= and &sort= too, #/activity for
// what goes on across the flows, with ?days=<n> for its period, #/trash
// for the flows deleted, anything else for the list of flows. #/people
// and #/audit, where the people and the audit log were before the
// settings, are their tabs now. #/projects, outside any project, is where
// projects are made and kept. The address is the open tab's; each tab
// keeps its own (tabs.ts), so its own project.
type Page =
  | { flow: number; run: number | null; step: number | null }
  | { compare: number; a: number; b: number }
  | { data: number }
  | { records: number }
  | { taskBoard: number }
  | { settings: SettingsTab | null }
  | 'schedules'
  | 'activity'
  | 'inbox'
  | 'trash'
  | 'projects'
  | null

const pageInHash = (): Page => {
  const hash = currentPage()
  if (outsideProjects(hash)) return 'projects'
  if (hash === '#/schedules') return 'schedules'
  if (hash === '#/inbox') return 'inbox'
  if (hash === '#/trash') return 'trash'
  if (/^#\/activity(?:\?.*)?$/.test(hash)) return 'activity'
  if (hash === '#/people') return { settings: 'people' }
  if (/^#\/audit(?:\?.*)?$/.test(hash)) return { settings: 'audit' }
  const s = /^#\/settings(?:\/(\w+))?(?:\?.*)?$/.exec(hash)
  if (s) return { settings: (s[1] as SettingsTab | undefined) ?? null }
  const c = /^#\/flows\/(\d+)\/compare\/(\d+)\/(\d+)$/.exec(hash)
  if (c) return { compare: Number(c[1]), a: Number(c[2]), b: Number(c[3]) }
  const d = /^#\/flows\/(\d+)\/data(?:\?.*)?$/.exec(hash)
  if (d) return { data: Number(d[1]) }
  const r = /^#\/flows\/(\d+)\/records(?:\?.*)?$/.exec(hash)
  if (r) return { records: Number(r[1]) }
  const t = /^#\/taskboards\/(\d+)$/.exec(hash)
  if (t) return { taskBoard: Number(t[1]) }
  const m = /^#\/flows\/(\d+)(?:\/runs\/(\d+)(?:\/steps\/(\d+))?)?$/.exec(hash)
  return m ? { flow: Number(m[1]), run: m[2] ? Number(m[2]) : null, step: m[3] ? Number(m[3]) : null } : null
}

// How many steps wait on the person, asked every so often, again once they
// decide one, and as they come back to the page; null until known. The
// server counts those of the open tab's project, so it asks again as that
// changes.
function useInboxCount(project: number | null): number | null {
  const [count, setCount] = useState<number | null>(null)
  useEffect(() => {
    if (project === null) return
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
  }, [project])
  return count
}

// Two addresses of the same page: the list of flows has an address of #/
// or none, in its project or with none named.
const sameHash = (a: string, b: string) => {
  const same = (h: string) => {
    const { project, page } = splitHash(h)
    return hashIn(project, page === '' || page === '#' ? '#/' : page)
  }
  return same(a) === same(b)
}

// How long after asking /api/me again, as a request about the project was
// refused, it isn't asked again for another.
const REFUSED_ASK_MS = 10_000

export default function App() {
  const { tabs, active, select, add, close } = useTabs()
  const me = useMe()
  const [, redraw] = useReducer((n: number) => n + 1, 0)
  const { project, page } = splitHash(window.location.hash)
  const inbox = useInboxCount(project)

  // An address naming no project, as an old bookmark's or a notification's,
  // opens in the project last used, else the person's first, in place, once
  // the projects they may enter are known, so one they lost since is passed
  // over. (A link followed within a tab stays in the tab's project, tabs.ts.)
  useLayoutEffect(() => {
    if (project !== null || outsideProjects(page) || !me) return
    const to = rewritten(window.location.hash, projectFor(null, lastProject(), me.projects.map((p) => p.id)))
    if (to === null) return
    window.history.replaceState(window.history.state, '', window.location.pathname + window.location.search + to)
    redraw()
  })

  // The project shown is the one an address naming none opens in next.
  const enterable = project !== null && !!me?.projects.some((p) => p.id === project)
  useEffect(() => {
    if (enterable && project !== null) rememberProject(project)
  }, [enterable, project])

  // A request about what is in the project refused as forbidden or not
  // found may mean the person was taken out of it, or it was archived:
  // who they are is asked again, and the page gives way to saying so once
  // the project is no longer among theirs.
  useEffect(() => {
    let asked = 0
    return onRefusedHere(() => {
      if (Date.now() - asked < REFUSED_ASK_MS) return
      asked = Date.now()
      reloadMe().catch(() => {})
    })
  }, [])

  // A page opens in the tab showing it, if one does, else in a new one.
  const openPage = (hash: string) => {
    if (sameHash(window.location.hash, hash)) return
    const open = tabs.find((t) => t.id !== active && sameHash(t.hash, hash))
    if (open) select(open.id)
    else add(hash)
  }
  const onInbox = () => openPage(pageHash('#/inbox'))
  // The command palette and the sheet of shortcuts, over any page.
  const [palette, setPalette] = useState(false)
  const [sheet, setSheet] = useState(false)
  useShortcuts({
    palette: () => setPalette((open) => !open),
    sheet: () => setSheet(true),
  })

  // What shows in place of the page: nothing while the project an address
  // naming none opens in isn't known, and that they may enter no project,
  // or not the one the address names.
  const outside = outsideProjects(page)
  let shown: 'page' | 'wait' | 'none' = 'page'
  if (!outside && project === null) shown = me && me.projects.length === 0 ? 'none' : 'wait'
  else if (!outside && me && !enterable) shown = 'none'

  return (
    <>
      <TabBar
        tabs={tabs}
        active={active}
        inbox={inbox}
        onSelect={select}
        onAdd={() => add(pageHash('#/'), true)}
        onClose={close}
        onHome={() => openPage(pageHash('#/'))}
        onInbox={onInbox}
      />
      <div className="tab-page">
        {/* A new page for each tab, and for each project a tab goes to, so
            nothing of one shows on another. */}
        <TabContext.Provider value={active}>
          {shown === 'page' && <Page key={`${active}:${project ?? ''}`} />}
          {shown === 'none' && me && <NoProject me={me} project={project} />}
        </TabContext.Provider>
      </div>
      {palette && <CommandPalette onOpen={openPage} onShortcuts={() => setSheet(true)} onClose={() => setPalette(false)} />}
      {sheet && <ShortcutSheet onClose={() => setSheet(false)} />}
    </>
  )
}

function Page() {
  const page = pageInHash()
  if (page === null) return <FlowList onOpen={(id) => (window.location.hash = pageHash(`#/flows/${id}`))} />
  if (page === 'projects') return <ProjectsPage />
  if (page === 'schedules') return <Schedules />
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
    if (flow) document.title = `${flow.name} · Spoletta`
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
        <ErrorBanner error={error} />
        <p className="flows">
          <a href={pageHash('#/')}>Back to every flow</a>
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
