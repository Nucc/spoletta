import type { Flow, FlowRunStatus, RunMark, TaskBoard } from './api'

// What the main page's list (FlowList.tsx) shows and how: everything, the
// starred or one kind, narrowed by a search, a tag, whose it is and what
// needs a look, and sorted, all kept in the address; and how a flow's
// latest runs, its failures and its times read there. Pure, so
// scripts/home.ts checks it.

// The kinds the list holds, each a block of it: flows, task boards, and
// what flows reuse, procedures (modules) and structs.
export type Kind = 'flow' | 'board' | 'module' | 'struct'

export const KINDS: Kind[] = ['flow', 'board', 'module', 'struct']

export const kindOf = (f: Pick<Flow, 'module' | 'struct'>): Kind => (f.struct ? 'struct' : f.module ? 'module' : 'flow')

// What the rail picks: everything, the starred, or one kind.
export type View = 'all' | 'starred' | Kind

// What needs a look: steps waiting on whoever looks, flows failing of late,
// and runs going on.
export type Need = 'waiting' | 'failing' | 'running'

export const NEEDS: Need[] = ['waiting', 'failing', 'running']

export type Sort = 'name' | 'recent' | 'failing'

// What the list shows. whose is an owner's key, as FlowList.tsx makes it,
// or empty for everyone's.
export type ListState = { q: string; tag: string; view: View; whose: string; need: Need | ''; sort: Sort }

export const EVERYTHING: ListState = { q: '', tag: '', view: 'all', whose: '', need: '', sort: 'name' }

// How the address names each view.
const VIEW_WORDS: Record<View, string> = {
  all: '',
  starred: 'starred',
  flow: 'flows',
  board: 'taskboards',
  module: 'procedures',
  struct: 'structs',
}

// What the list shows by the address, #/?q=<words>&tag=<tag>&show=<view>&
// whose=<owner>&needs=<need>&sort=<sort>; what it doesn't say, or says
// wrongly, is as with nothing narrowed.
export function readList(hash: string): ListState {
  const p = new URLSearchParams(hash.split('?')[1] ?? '')
  const view = (Object.keys(VIEW_WORDS) as View[]).find((v) => v !== 'all' && VIEW_WORDS[v] === p.get('show')) ?? 'all'
  const need = p.get('needs') as Need | null
  const sort = p.get('sort') as Sort | null
  return {
    q: p.get('q') ?? '',
    tag: p.get('tag') ?? '',
    view,
    whose: p.get('whose') ?? '',
    need: need && NEEDS.includes(need) ? need : '',
    sort: sort === 'recent' || sort === 'failing' ? sort : 'name',
  }
}

// The address of the list showing that, #/ when nothing is narrowed.
export function listHash(s: ListState): string {
  const p = new URLSearchParams()
  if (s.q) p.set('q', s.q)
  if (s.tag) p.set('tag', s.tag)
  if (s.view !== 'all') p.set('show', VIEW_WORDS[s.view])
  if (s.whose) p.set('whose', s.whose)
  if (s.need) p.set('needs', s.need)
  if (s.sort !== 'name') p.set('sort', s.sort)
  const query = p.toString()
  return query ? `#/?${query}` : '#/'
}

// Tags are the same whatever their case.
export const sameTag = (a: string, b: string) => a.toLowerCase() === b.toLowerCase()

const words = (query: string) => query.toLowerCase().split(/\s+/).filter(Boolean)

// Whether every word searched for is in the name or one of the tags.
export const matches = (name: string, tags: string[], query: string) =>
  words(query).every((w) => name.toLowerCase().includes(w) || tags.some((t) => t.toLowerCase().includes(w)))

// The name in pieces, those holding a word searched for marked, in order.
export function marked(name: string, query: string): { text: string; hit: boolean }[] {
  const lower = name.toLowerCase()
  const hit = new Array<boolean>(name.length).fill(false)
  for (const w of words(query)) {
    for (let i = lower.indexOf(w); i >= 0; i = lower.indexOf(w, i + 1)) hit.fill(true, i, i + w.length)
  }
  const pieces: { text: string; hit: boolean }[] = []
  for (let i = 0; i < name.length; i++) {
    const last = pieces[pieces.length - 1]
    if (last && last.hit === hit[i]) last.text += name[i]
    else pieces.push({ text: name[i], hit: hit[i] })
  }
  return pieces
}

// Whether a flow needs a look of each kind: one of its steps waits on
// whoever looks, its last run failed or some of its runs this week did, or
// a run of it is going on. A struct never runs, so needs none.
export const needs: Record<Need, (f: Flow) => boolean> = {
  waiting: (f) => (f.health?.waiting ?? 0) > 0,
  failing: (f) => f.lastRun?.status === 'failed' || (f.health?.failed ?? 0) > 0,
  running: (f) => (f.health?.running ?? 0) > 0,
}

// The share of its runs this week that failed, in whole percent; 0 with none.
export const failedPercent = (f: Flow) => {
  const h = f.health
  return h && h.runs > 0 ? Math.round((100 * h.failed) / h.runs) : 0
}

// How a run looks in a strip: a run that ended otherwise than done or failed
// was stopped.
export type RunLook = 'done' | 'failed' | 'running' | 'waiting' | 'stopped'

export function runLook(status: FlowRunStatus, waiting: boolean): RunLook {
  if (status === 'running') return waiting ? 'waiting' : 'running'
  return status === 'done' || status === 'failed' ? status : 'stopped'
}

// How many runs a strip has room for, as many as the list is given.
export const STRIP = 20

// A flow's strip: the room left before its runs, then its latest runs,
// oldest first, each as it looks.
export function strip(latest: RunMark[]): { empty: number; runs: { run: RunMark; look: RunLook }[] } {
  const runs = latest.slice(-STRIP).map((run) => ({ run, look: runLook(run.status, run.waiting) }))
  return { empty: STRIP - runs.length, runs }
}

// When something happened, as a list reads it at a glance: just now, so
// many minutes, hours or days ago, and further back the date.
export function timeAgo(iso: string, now: number): string {
  const then = Date.parse(iso)
  const s = Math.max(0, (now - then) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)} min ago`
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`
  const days = Math.floor(s / 86400)
  if (days === 1) return 'yesterday'
  if (days < 14) return `${days} days ago`
  const d = new Date(then)
  return d.toLocaleDateString('en-GB', {
    day: 'numeric',
    month: 'short',
    year: d.getFullYear() === new Date(now).getFullYear() ? undefined : 'numeric',
  })
}

const byName = (a: { name: string }, b: { name: string }) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' })

const started = (f: Flow) => (f.lastRun ? Date.parse(f.lastRun.startedAt) : -Infinity)

// The flows in the order asked, favourites first: by name, the latest run
// first (those never run last), or the most failing first, by the share of
// this week's runs that failed, then by whether the last run did.
export function sortFlows(fs: Flow[], sort: Sort): Flow[] {
  const then: Record<Sort, (a: Flow, b: Flow) => number> = {
    name: byName,
    recent: (a, b) => started(b) - started(a) || byName(a, b),
    failing: (a, b) =>
      failedPercent(b) - failedPercent(a) ||
      Number(b.lastRun?.status === 'failed') - Number(a.lastRun?.status === 'failed') ||
      byName(a, b),
  }
  return [...fs].sort((a, b) => Number(b.favourite) - Number(a.favourite) || then[sort](a, b))
}

// The task boards in the order asked: by name, or the latest changed first;
// they never fail.
export function sortBoards(bs: TaskBoard[], sort: Sort): TaskBoard[] {
  return [...bs].sort((a, b) => (sort === 'recent' && Date.parse(b.updatedAt) - Date.parse(a.updatedAt)) || byName(a, b))
}
