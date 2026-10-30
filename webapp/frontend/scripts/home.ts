// What the main page's list (src/home.ts) makes of the flows: the address
// it keeps what it shows in, the search and what it marks of a name, what
// needs a look, the orders it sorts in, a flow's run strip and how long ago
// things happened. Run with `npm test`, which needs Node 22.6 or later to
// load TypeScript.

import type { Flow, FlowHealth, FlowRun, RunMark, TaskBoard } from '../src/api.ts'
import {
  EVERYTHING,
  failedPercent,
  listHash,
  marked,
  matches,
  needs,
  readList,
  sortBoards,
  sortFlows,
  strip,
  timeAgo,
} from '../src/home.ts'

declare const process: { exitCode?: number }

let failed = 0
let checked = 0
function check(what: string, got: unknown, want: unknown) {
  checked++
  if (JSON.stringify(got) !== JSON.stringify(want)) {
    failed++
    console.error(`${what}\n  got  ${JSON.stringify(got)}\n  want ${JSON.stringify(want)}`)
  }
}

const NOW = Date.parse('2026-10-29T21:00:00Z')
const ago = (s: number) => new Date(NOW - s * 1000).toISOString()

let ids = 0
function flow(name: string, p: Partial<Flow> & { last?: [FlowRun['status'], number]; week?: Partial<FlowHealth> } = {}): Flow {
  const { last, week, ...rest } = p
  return {
    id: ++ids,
    name,
    module: false,
    struct: false,
    usedBy: 0,
    settings: { repositories: [], branchPrefix: '' },
    createdAt: ago(86400 * 30),
    owner: { kind: 'global' },
    access: 'edit',
    lastRun: last ? ({ id: ++ids, status: last[0], waiting: false, startedAt: ago(last[1]) } as FlowRun) : null,
    tags: [],
    favourite: false,
    health: { runs: 0, failed: 0, running: 0, waiting: 0, latest: [], ...week },
    ...rest,
  } as Flow
}

const names = (xs: { name: string }[]) => xs.map((x) => x.name)

// The address: what isn't narrowed isn't written, and what is reads back.
check('nothing narrowed', listHash(EVERYTHING), '#/')
check('nothing in the address', readList('#/'), EVERYTHING)
check('no address at all', readList(''), EVERYTHING)
const narrowed = { q: 'jira fix', tag: 'Billing', view: 'module', whose: 'team:3', need: 'failing', sort: 'recent' } as const
check('everything narrowed', listHash(narrowed), '#/?q=jira+fix&tag=Billing&show=procedures&whose=team%3A3&needs=failing&sort=recent')
check('reads back', readList(listHash(narrowed)), narrowed)
check('the old address, search and tag', readList('#/?q=bump&tag=deps'), { ...EVERYTHING, q: 'bump', tag: 'deps' })
check('task boards', readList('#/?show=taskboards').view, 'board')
check('the starred', readList('#/?show=starred').view, 'starred')
check('a view it doesn’t know', readList('#/?show=nonsense&needs=x&sort=y'), EVERYTHING)

// The search: every word in the name or a tag, and what it marks.
check('words anywhere', matches('Fix exceptions', [], 'exc fix'), true)
check('a word in a tag', matches('Bump', ['dependencies'], 'depend'), true)
check('a word nowhere', matches('Fix exceptions', ['ops'], 'fix jira'), false)
check('nothing searched', matches('Anything', [], '  '), true)
check('marks whatever its case', marked('Fix exceptions', 'EX'), [
  { text: 'Fix ', hit: false },
  { text: 'ex', hit: true },
  { text: 'ceptions', hit: false },
])
check('marks each place a word is', marked('Tests of tests', 'test'), [
  { text: 'Test', hit: true },
  { text: 's of ', hit: false },
  { text: 'test', hit: true },
  { text: 's', hit: false },
])
check('marks two words', marked('Fix exceptions', 'fix ions'), [
  { text: 'Fix', hit: true },
  { text: ' except', hit: false },
  { text: 'ions', hit: true },
])
check('overlapping words', marked('Example', 'exa amp'), [
  { text: 'Examp', hit: true },
  { text: 'le', hit: false },
])
check('nothing searched marks nothing', marked('Example', ''), [{ text: 'Example', hit: false }])

// What needs a look.
const waiting = flow('Triage', { last: ['failed', 3600], week: { runs: 3, failed: 2, running: 1, waiting: 1 } })
const flaky = flow('Hacker News', { last: ['done', 3 * 86400], week: { runs: 13, failed: 8 } })
const lastFailed = flow('Consensus', { last: ['failed', 19 * 86400] })
const fine = flow('Exceptions', { last: ['done', 4 * 86400], week: { runs: 1 } })
const never = flow('Maintenance')
const struct = flow('Jira ticket', { struct: true, health: null })
check('waiting on whoever looks', [waiting, flaky, fine, struct].map(needs.waiting), [true, false, false, false])
check('failing', [waiting, flaky, lastFailed, fine, never, struct].map(needs.failing), [true, true, true, false, false, false])
check('running', [waiting, flaky, struct].map(needs.running), [true, false, false])
check('the share failed', [waiting, flaky, fine, never].map(failedPercent), [67, 62, 0, 0])

// The orders, favourites always first.
const starred = flow('Zebra', { favourite: true, last: ['done', 30 * 86400] })
const all = [waiting, flaky, lastFailed, fine, never, starred]
check('by name', names(sortFlows(all, 'name')), ['Zebra', 'Consensus', 'Exceptions', 'Hacker News', 'Maintenance', 'Triage'])
check('latest run first, never run last', names(sortFlows(all, 'recent')), [
  'Zebra',
  'Triage',
  'Hacker News',
  'Exceptions',
  'Consensus',
  'Maintenance',
])
check('most failing first', names(sortFlows(all, 'failing')), [
  'Zebra',
  'Triage',
  'Hacker News',
  'Consensus',
  'Exceptions',
  'Maintenance',
])
const board = (name: string, changed: number) => ({ id: ++ids, name, updatedAt: ago(changed) }) as TaskBoard
const boards = [board('Sprint', 86400), board('Releases', 60), board('Bugs', 7200)]
check('boards by name', names(sortBoards(boards, 'name')), ['Bugs', 'Releases', 'Sprint'])
check('boards latest changed first', names(sortBoards(boards, 'recent')), ['Releases', 'Bugs', 'Sprint'])
check('boards never fail', names(sortBoards(boards, 'failing')), ['Bugs', 'Releases', 'Sprint'])

// A run strip: room before the runs, each run as it looks.
const mark = (status: RunMark['status'], waiting = false): RunMark => ({ id: ++ids, status, waiting, startedAt: ago(60) })
const s = strip([mark('done'), mark('failed'), mark('interrupted'), mark('running', true), mark('running')])
check('room before a few runs', s.empty, 15)
check('how each run looks', s.runs.map((r) => r.look), ['done', 'failed', 'stopped', 'waiting', 'running'])
const many = Array.from({ length: 25 }, () => mark('done'))
check('no more than a strip holds', [strip(many).empty, strip(many).runs.length, strip(many).runs[0].run.id], [0, 20, many[5].id])
check('no runs', strip([]), { empty: 20, runs: [] })

// How long ago.
check('seconds', timeAgo(ago(20), NOW), 'just now')
check('minutes', timeAgo(ago(5 * 60 + 10), NOW), '5 min ago')
check('hours', timeAgo(ago(21 * 3600), NOW), '21 h ago')
check('a day', timeAgo(ago(30 * 3600), NOW), 'yesterday')
check('days', timeAgo(ago(6 * 86400), NOW), '6 days ago')
check('weeks back, the date', timeAgo('2026-10-10T17:06:04Z', NOW), '10 Oct')
check('another year, with it', timeAgo('2025-12-24T10:00:00Z', NOW), '24 Dec 2025')
check('a clock a little ahead', timeAgo(ago(-30), NOW), 'just now')

console.log(`${checked} checks of the home page, ${failed} failed`)
if (failed > 0) process.exitCode = 1
