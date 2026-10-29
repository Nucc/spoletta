// A human process is a step a person carries out: they check its inputs and
// decide by setting an object to one of the states the process's output links
// name, and fill in the objects its output links list no states for.
// A flow process is made of processes, on a board of its own. A subflow
// process runs another flow, so flows can reuse each other: the objects it
// takes are given to the objects of the same names on that flow's top board,
// and it generates what that flow's run leaves of the objects named like the
// ones it generates. A connector process carries out an operation of a
// connector built into the webapp, such as commenting on a GitHub issue.
export type ProcessKind = 'agent' | 'script' | 'human' | 'flow' | 'subflow' | 'connector'

// The operation a connector process carries out, on a connector's installed
// server. Its arguments are text in which {{Object}} and
// {{Object.attribute}} stand for what the step takes.
export type ConnectorSettings = {
  server: number
  operation: string
  args?: Record<string, string>
}

// Settings of the kinds that take more than a prompt or a script, by kind.
export type ProcessSettings = {
  // The MCP servers an agent process may use, by id; none when absent.
  mcpServers?: number[]
  // The tools it keeps to of each, by server id. Without picking, it has
  // all of an MCP server's tools and a connector's that only read.
  mcpTools?: Record<string, string[]>
  // The skills an agent process is given, by id; none when absent.
  skills?: number[]
  // The credentials an agent or script process calls services with, by
  // id; none when absent.
  credentials?: number[]
  connector?: ConnectorSettings
  // The flow's repositories an agent or script process works on, by name;
  // all of them when absent, none when empty.
  repositories?: string[]
  // The flow a subflow process runs, by id.
  flow?: number
  // How many times an agent deciding states runs before its decisions can
  // no longer send the object back to a process that ran; DEFAULT_ROUNDS
  // when absent.
  rounds?: number
  // How many times a process runs round a loop with no person deciding in
  // between before someone is asked whether it goes on; DEFAULT_LOOPS when
  // absent.
  loops?: number
  // How long an agent, script or connector step may take, in seconds; the
  // worker's own limit when absent.
  timeout?: number
  // How many more times a failed step is tried; none when absent.
  retries?: number
  // How many of the objects it takes without waiting for a state a process
  // needs before it runs; all of them when absent.
  quorum?: number
  // Whether an agent's prompt describes the flow it works in, in OPL.
  describeFlow?: boolean
  // How often those who may decide a human step are reminded of it while
  // it waits, and the longest it waits before it fails, so what
  // handles its failure takes over; in seconds, neither when absent.
  remindAfter?: number
  waitAtMost?: number
  // How long a human step waits, in seconds, before the roles escalateTo
  // names may decide it too, and are told so; never when either is absent.
  escalateAfter?: number
  escalateTo?: string[]
}

export const DEFAULT_ROUNDS = 3
export const MAX_ROUNDS = 20
export const DEFAULT_LOOPS = 5
export const MAX_LOOPS = 100
// The longest timeout, a day in seconds, and the most retries.
export const MAX_TIMEOUT = 24 * 60 * 60
export const MAX_RETRIES = 5
// The longest a human step may wait before it is reminded or fails, a year
// in seconds.
export const MAX_WAIT = 365 * 24 * 60 * 60

export type Process = {
  id: number
  // The flow it is in.
  flow: number
  name: string
  kind: ProcessKind
  // The agent's prompt when kind is 'agent', the reviewer's instructions when 'human'.
  prompt: string
  // Used when kind is 'script'.
  script: string
  // Model (provider/id) and thinking level for agents; empty means pi's default.
  model: string
  thinking: ThinkingLevel | ''
  x: number
  y: number
  // Set when resized; null draws the default width.
  width: number | null
  // The flow process whose board it is on; null on the top board.
  parent: number | null
  // For flow processes: each time it runs is a run of its own.
  recording: boolean
  settings: ProcessSettings
  // For a subflow process whose procedure is in the trash, that procedure's
  // name: it fails to run it until it is restored. Only the board as it is
  // says so, not a version.
  trashedFlow?: string
}

export type Start = {
  x: number
  y: number
  // Process the flow starts with, if connected.
  target: number | null
  // Where its link runs, if moved by hand; cleared when the target changes.
  route: number[] | null
}

// A process running right after another, or for a failure connection, OPM's
// exception link, instead of it once it fails, handling the failure.
export type Connection = {
  id: number
  source: number
  target: number
  kind: '' | 'failure'
}

// An OPM object: something a process generates and other processes take as input.
export type FlowObject = {
  id: number
  // The flow it is in.
  flow: number
  name: string
  x: number
  y: number
  // Set when resized; null draws the default width.
  width: number | null
  // Runs store what they generate it with, and its unique attributes tell
  // whether another run already stored a value.
  persistent: boolean
  // A struct's object whose copies, in whatever flow, read and write one
  // store of records, and those copies; see shared.go.
  shared: boolean
  // A list of items: an agent generating it replies with a list, and a flow
  // process taking it runs once per item. Inside that flow process, it is
  // the item.
  many: boolean
  // How many items the list holds: at least min, and at most max unless it
  // is null; see quantity.ts.
  min: number
  max: number | null
  // A person, such as a requester: in OPM a human object. Its link into a
  // process is an agent link, drawn ending in a filled circle: the person
  // carries the process out, so it must be a human process.
  human: boolean
  // Leads into the end of the flow's top board, as one of what the flow
  // leaves, connected there by hand. Flows running it get these objects.
  end: boolean
  // The flow process whose board it is on; null on the top board. It also
  // shows inside the flow processes linked to it.
  parent: number | null
  // The object it makes up, which it is on the board of instead of a
  // process's: as an attribute that object exhibits, or a part it
  // aggregates. See structure.ts.
  owner: number | null
  // '' for an object owned by none.
  relation: Relation | ''
  // The object it specializes: it has that object's fields too.
  general: number | null
  // The module's or struct's object it is a copy of, and that flow: the copy follows
  // it, so what it has changes only there. Null for an object of its own.
  source: number | null
  sourceFlow: number | null
  states: ObjectState[]
  attributes: ObjectAttribute[]
}

// How an owned object makes up its owner, as OPM's structural links say.
export type Relation = 'exhibition' | 'aggregation'

// Something an object exhibits, such as a ticket's subject. An agent
// generating an object with attributes replies with a JSON object holding
// exactly those, each as its definition says.
export type ObjectAttribute = {
  id: number
  object: number
  name: string
  definition: string
  // No two values the object stores share this attribute's value; a step
  // generating one another run stored terminates its run. Only matters
  // while the object is persistent.
  unique: boolean
  // Holds the path of a file the process wrote in its step's files
  // directory, which must exist.
  file: boolean
}

// A situation an object can be in, such as approved or changes requested.
export type ObjectState = {
  id: number
  object: number
  name: string
  // When an object generated in no state is in this one, such as
  // confidence >= 0.8; absent for none.
  rule?: string
}

// 'output': the process generates the object. 'input': the process takes it as input.
export type LinkDirection = 'output' | 'input'

// A link naming a state generates the object in that state (output), or
// runs the process when the object enters that state (input). A human
// process's output link names no state but lists the states it decides
// between; other links list none.
export type Link = {
  id: number
  process: number
  object: number
  direction: LinkDirection
  state: number | null
  states: number[]
  // How an input link's process takes the object, as in OPM: as an
  // instrument it doesn't change, or consuming it; empty when it just takes it.
  role: LinkRole
  // For an input link naming a state: its process runs only if the object
  // is in it, and is skipped otherwise, rather than running once it enters it.
  condition: boolean
  // For an input link: which stored records of a persistent object it takes,
  // rather than the object in the run; see queries.go.
  query: LinkQuery | null
  // Where the link's segments run, if moved by hand; see route.ts.
  route: number[] | null
  // Where the object shows on the process's own board, if moved there by
  // hand; only a flow process has one.
  at: { x: number; y: number } | null
}

// Requests changing what isn't a board, which leave boards' histories be.
const NOT_BOARDS = ['/api/history', '/api/me', '/api/runs', '/api/schedules', '/api/webhooks', '/api/api-keys', '/api/mcp-servers', '/api/users', '/api/teams', '/api/model-keys', '/api/model-providers', '/api/model-default', '/api/credentials', '/api/taskboards']

// What is told of each change the server took that may change a board.
const changeListeners = new Set<(url: string) => void>()
// The changes sent and not answered yet.
const changing = new Set<Promise<unknown>>()

// Calls back after each change the server took that may change a board,
// with the change's path.
export function onChanged(callback: (url: string) => void): () => void {
  changeListeners.add(callback)
  return () => changeListeners.delete(callback)
}

// Resolves once every change sent has been answered.
export async function changesSettled(): Promise<void> {
  while (changing.size > 0) await Promise.allSettled([...changing])
}

// Sends the browser to sign in, coming back to the page it is on.
export function signIn() {
  window.location.href = `/api/auth/login?${new URLSearchParams({ next: window.location.pathname + window.location.hash })}`
}

async function send(url: string, init?: RequestInit): Promise<Response> {
  const res = await fetch(url, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...init?.headers },
  })
  // The session ended, or there was none.
  if (res.status === 401) signIn()
  if (!res.ok) {
    throw new Error(`${init?.method ?? 'GET'} ${url}: ${res.status} ${await res.text()}`)
  }
  return res
}

async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const method = init?.method ?? 'GET'
  let res: Response
  if (method === 'GET' || NOT_BOARDS.some((p) => url.startsWith(p))) {
    res = await send(url, init)
  } else {
    const sent = send(url, init)
    changing.add(sent)
    try {
      res = await sent
    } finally {
      changing.delete(sent)
    }
    changeListeners.forEach((l) => l(url))
  }
  if (res.status === 204) return undefined as T
  return res.json() as Promise<T>
}

// A board of its own with everything on it, its versions and its runs; each
// flow does one job, such as fixing exceptions or bumping dependencies.
export type Flow = {
  id: number
  name: string
  // Made to be reused: other flows run it, and only modules are offered to
  // them. Every flow another runs is one.
  module: boolean
  // Holds a single object and nothing else, which flows use: each copy of
  // it follows it, as one of a module's does.
  struct: boolean
  // How many other flows run it, or for a struct, use its object.
  usedBy: number
  settings: FlowSettings
  createdAt: string
  // Who sees it and may change it: someone, a team, or everyone.
  owner: FlowOwner
  // How far whoever asked may go with it: change it, or only see it to
  // decide the reviews their roles let them.
  access: 'edit' | 'review'
  // Its latest run of the whole flow, if any.
  lastRun: FlowRun | null
  // The words it is found by, which those who may change it give it.
  tags: string[]
  // Whether whoever asked starred it, which lists it first.
  favourite: boolean
  // How its runs of its own go of late; none for a struct, which never runs.
  health: FlowHealth | null
}

// The runs of a flow of its own that started in the last 7 days, how many
// of those failed, and how many are going on now; and how many of its steps
// wait on whoever asked, as their inbox lists them.
export type FlowHealth = { runs: number; failed: number; running: number; waiting: number }

// Gives the flow these tags, in place of those it had; they come back
// trimmed, each once whatever its case, in order.
export async function setFlowTags(id: number, tags: string[]): Promise<string[]> {
  const res = await send(`/api/flows/${id}/tags`, { method: 'PUT', body: JSON.stringify({ tags }) })
  return ((await res.json()) as { tags: string[] }).tags
}

// Stars the flow as one of whoever asks' favourites, or takes the star away.
export async function setFavourite(id: number, favourite: boolean): Promise<void> {
  await send(`/api/flows/${id}/favourite`, { method: favourite ? 'PUT' : 'DELETE' })
}

// Who owns a flow; id and name are the team's or the user's.
export type FlowOwner = { kind: 'global' } | { kind: 'team' | 'user'; id: number; name: string }

// Gives the flow to someone, a team, or everyone.
export function setFlowOwner(id: number, owner: { kind: 'global' } | { kind: 'team' | 'user'; id: number }): Promise<Flow> {
  return request(`/api/flows/${id}/owner`, { method: 'PUT', body: JSON.stringify(owner) })
}

// A repository the flow's agents and scripts work on, checked out in their
// run's workspace in a directory named after it.
export type Repository = {
  name: string
  // https only.
  url: string
  // The branch runs' branches start from; empty for the default branch.
  base: string
  // Paths in it its checkouts leave out, such as a cache of gems: not
  // fetched nor there, yet kept in the branch as they are.
  exclude?: string[]
}

export type FlowSettings = {
  repositories: Repository[]
  // Runs' branches are named <prefix>/run-<id>; empty uses flow/<the flow's name>.
  branchPrefix: string
  // The most, in dollars, a run may spend on its models; none or 0 for the
  // instance's limit, which is kept to if lower.
  runSpendLimit?: number
}

// Where the git tokens steps reach repositories with come from: a GitHub
// App's, for the run's repositories and lasting an hour; the instance's
// GIT_TOKEN, which isn't short-lived; or none. otherHosts says GIT_TOKEN
// covers repositories off the app's GitHub.
export type GitAccess = { source: 'app' | 'token' | 'none'; otherHosts: boolean }

export function getGitAccess(): Promise<GitAccess> {
  return request('/api/git-access')
}

// The prefix runs' branches get when the flow sets none.
export const defaultBranchPrefix = (flowName: string) =>
  'flow/' + (flowName.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'flow')

// The setup, every MCP server, struct, procedure and flow, as a YAML file
// to keep under version control; see SETUP.md.
export const SETUP_URL = '/api/setup'

// The setup as a YAML file, refused with why, such as flows sharing a name.
export async function exportSetup(): Promise<Blob> {
  return (await send(SETUP_URL)).blob()
}

// What importing a setup file did to each of its flows.
export type ImportedFlow = { id: number; name: string; created: boolean }

// Imports a setup file: creates the flows it has that the app doesn't, and
// makes over the ones it has by name. A file with anything wrong changes nothing.
export async function importSetup(file: string): Promise<ImportedFlow[]> {
  const res = await request<{ flows: ImportedFlow[] }>(SETUP_URL, {
    method: 'POST',
    body: file,
    headers: { 'Content-Type': 'application/yaml' },
  })
  return res.flows
}

export function listFlows(): Promise<Flow[]> {
  return request('/api/flows')
}

export function getFlow(id: number): Promise<Flow> {
  return request(`/api/flows/${id}`)
}

// A struct is made with its object, named after it.
// Without an owner, it is its maker's team's when they are in one only, else
// theirs.
export function createFlow(
  name: string,
  kind: 'flow' | 'module' | 'struct' = 'flow',
  owner?: { kind: 'global' } | { kind: 'team' | 'user'; id: number },
): Promise<Flow> {
  return request('/api/flows', {
    method: 'POST',
    body: JSON.stringify({ name, module: kind === 'module', struct: kind === 'struct', owner }),
  })
}

// Something a template needs of the app: a repository its processes work
// on, which the flow made of it lacks until someone adds it in its
// settings; a connector, met once installed; or a role, met once someone
// holds it, or always without sign-in.
export type TemplateNeed = { kind: 'repository' | 'connector' | 'role'; name: string; title?: string; met: boolean }

// A ready flow to start from, kept in the webapp; see templates.go.
export type FlowTemplate = {
  id: string
  title: string
  description: string
  // The name its flow takes, or with a number after it if a flow has it.
  flow: string
  // The processes of its top board.
  steps: { name: string; kind: ProcessKind }[]
  needs: TemplateNeed[]
}

export function listTemplates(): Promise<FlowTemplate[]> {
  return request('/api/templates')
}

// Makes a new flow of the template, named as it is unless name says, and
// belonging to owner as a flow made by hand does.
export function createFromTemplate(
  id: string,
  name?: string,
  owner?: { kind: 'global' } | { kind: 'team' | 'user'; id: number },
): Promise<Flow> {
  return request(`/api/templates/${encodeURIComponent(id)}`, { method: 'POST', body: JSON.stringify({ name, owner }) })
}

// What is wrong with a board, worked out on the server from the rules the
// engine keeps to; see problems.go. An error is what a run can't get past,
// so Run refuses the board; a warning what a run may well get past, or what
// is set up outside the board. Each is about a process or an object, on the
// board of the flow process given, or on the top board, or about the board
// itself.
export type Problem = {
  level: 'error' | 'warning'
  rule: string
  message: string
  process?: number
  object?: number
  board?: number
}

export type FlowProblems = {
  problems: Problem[]
  // The version the flow published and its errors, as schedules, the API
  // and runs on records run it; null when none is published.
  published: { version: number; errors: Problem[] } | null
}

export function getProblems(flow: number): Promise<FlowProblems> {
  return request(`/api/flows/${flow}/problems`)
}

// Puts a copy of a struct's object on a board of a flow, which follows it.
export function copyStruct(
  struct: number,
  at: { flow: number; parent: number | null; x: number; y: number },
): Promise<FlowObject> {
  return request(`/api/structs/${struct}/copies`, { method: 'POST', body: JSON.stringify(at) })
}

// A record of a shared struct's object, as every flow using it reads and
// writes it.
export type SharedRecord = {
  id: number
  content: unknown
  state: string
  // The run that last moved it on, or added it, and that run's flow.
  run: number | null
  runFlow: number | null
  updated: string
  // Whether a person corrected it since a run last moved it on, and who.
  edited: boolean
  editedBy: string
}

// A column a table page's rows are searched by, as the server found it
// (search.go): its values the most common first among the rows the rest
// of the query matches, how many values it has in all, and how many rows
// have none. Object, kind and field say which of the page's columns it is.
export type SearchFacet = {
  key: string
  label: string
  group: string
  object?: string
  kind?: 'state' | 'field' | 'whole'
  field?: string
  date?: boolean
  hidden?: boolean
  numeric: boolean
  values: { value: string; count: number }[]
  distinct: number
  none: number
}

// What a search found: the rows it matched among those it read, and
// whether more are stored than a search reads.
export type SearchResult = { facets: SearchFacet[]; matched: number; searched: number; truncated: boolean }

// A query for a table page, and the facet key to sort by, with a - before
// it for the other way.
export type Search = { q?: string; sort?: string }

const searchParams = (s: Search | undefined) => {
  const p = new URLSearchParams()
  if (s?.q) p.set('q', s.q)
  if (s?.sort) p.set('sort', s.sort)
  return p.size > 0 ? p.toString() : ''
}

// An event of the audit log: what a step was handed or did with it, or what
// an admin changed, for whom and which run and step.
export type AuditEvent = {
  id: number
  at: string
  who: string
  run: number | null
  flow: number | null
  step: number | null
  kind: string
  detail: string
}

export type AuditLog = { events: AuditEvent[] } & SearchResult

// The latest events the search matches; for admins.
export function getAuditLog(search?: Search): Promise<AuditLog> {
  const p = searchParams(search)
  return request(`/api/audit${p ? '?' + p : ''}`)
}

// A shared struct's records, the latest written first or as sorted, with
// the fields its object has in the board's order. more: more than these match.
export type SharedRecords = { fields: string[]; states: string[]; records: SharedRecord[]; more: boolean } & SearchResult

// What a person writes of a record: all its fields, replacing what it held,
// and its state, one of the object's or none.
export type RecordEdit = { content: Record<string, unknown>; state: string }

// The struct's records the search matches, the latest written first.
export function listSharedRecords(struct: number, search?: Search): Promise<SharedRecords> {
  const p = searchParams(search)
  return request(`/api/structs/${struct}/records${p ? '?' + p : ''}`)
}

// A record added by hand; a unique value another record holds is refused.
export function addSharedRecord(struct: number, edit: RecordEdit): Promise<SharedRecord> {
  return request(`/api/structs/${struct}/records`, { method: 'POST', body: JSON.stringify(edit) })
}

// A record corrected by hand, its fields replaced by these.
export function updateSharedRecord(struct: number, record: number, edit: RecordEdit): Promise<SharedRecord> {
  return request(`/api/structs/${struct}/records/${record}`, { method: 'PUT', body: JSON.stringify(edit) })
}

// Deletes the records, one or many at once.
export function deleteSharedRecords(struct: number, ids: number[]): Promise<{ deleted: number }> {
  return request(`/api/structs/${struct}/records/delete`, { method: 'POST', body: JSON.stringify({ ids }) })
}

// A task board: shared structs' records in columns, each column a struct
// and a query of its records as the Records page searches them, owned as a
// flow is; see taskboards.go.
export type TaskBoard = {
  id: number
  name: string
  owner: FlowOwner
  // Those who see it change it.
  access: 'edit'
  columns: TaskColumn[]
  updatedAt: string
}

// Why a column shows no records: its struct was deleted for good, is in the
// trash, isn't one whoever asks sees, or keeps no records.
export type TaskColumnProblem = 'gone' | 'trash' | 'hidden' | 'unshared'

export type TaskColumn = {
  name: string
  // Null once deleted for good.
  struct: number | null
  // Empty for one whoever asks doesn't see.
  structName: string
  query: string
  // A facet's key to sort by, with a - before it for the other way; empty
  // for the latest written first.
  sort: string
  // What its cards show; null for its key, the first other field, its
  // state and when it was last written.
  card: TaskCard | null
  problem?: TaskColumnProblem
  // Its records, on the board's own page.
  cards?: TaskCards
}

// The records a column's query matches, the first 200 of them, with what
// its cards and correcting one need of its struct.
export type TaskCards = {
  fields: string[]
  states: string[]
  // The struct's first unique attribute, which names a record.
  key: string
  records: SharedRecord[]
  // How many records the query matched, and whether the struct has more
  // than a search reads.
  matched: number
  truncated: boolean
}

// What a column's cards show of each record: the fields picked, in order,
// the key heading the card and the first other field read as its title;
// and whether its state, when it was last written, and the run that last
// moved it on show.
export type TaskCard = { fields: string[]; state: boolean; updated: boolean; run: boolean }

// What is saved of a column.
export type TaskColumnEdit = { name: string; struct: number | null; query: string; sort: string; card: TaskCard | null }

// A shared struct a column can hold the records of, with the fields they
// have, in the board's order, and the one naming them.
export type TaskStruct = { id: number; name: string; fields: string[]; key: string }

// The task boards whoever asks sees, by name, without their records.
export function listTaskBoards(): Promise<TaskBoard[]> {
  return request('/api/taskboards')
}

// The board with each column's records.
export function getTaskBoard(id: number): Promise<TaskBoard> {
  return request(`/api/taskboards/${id}`)
}

// Without an owner, it is its maker's team's when they are in one only, else theirs.
export function createTaskBoard(name: string, owner?: { kind: 'global' } | { kind: 'team' | 'user'; id: number }): Promise<TaskBoard> {
  return request('/api/taskboards', { method: 'POST', body: JSON.stringify({ name, owner }) })
}

// Saves the board's name and all its columns, in order, and answers with
// their records.
export function saveTaskBoard(id: number, board: { name: string; columns: TaskColumnEdit[] }): Promise<TaskBoard> {
  return request(`/api/taskboards/${id}`, { method: 'PUT', body: JSON.stringify(board) })
}

export function deleteTaskBoard(id: number): Promise<void> {
  return request(`/api/taskboards/${id}`, { method: 'DELETE' })
}

// Gives the board to someone, a team, or everyone.
export function setTaskBoardOwner(id: number, owner: { kind: 'global' } | { kind: 'team' | 'user'; id: number }): Promise<TaskBoard> {
  return request(`/api/taskboards/${id}/owner`, { method: 'PUT', body: JSON.stringify(owner) })
}

// The shared structs whoever asks sees, to pick a column's from.
export function listTaskStructs(): Promise<TaskStruct[]> {
  return request('/api/taskboards/structs')
}

// A flow a person may start on a struct's records: one whose queries read
// them, and why its queries wouldn't select each record asked about, by id,
// for those they wouldn't.
export type RecordFlow = { id: number; name: string; off: Record<string, string> }

// A run going on a record: one started on it, or the one that last moved it
// on, still running.
export type RecordRun = { run: number; flow: number }

// The flows that can be started on the struct's records, with which of the
// records named each wouldn't select, and the runs going on each, by id.
export function listRecordFlows(
  struct: number,
  records: number[],
): Promise<{ flows: RecordFlow[]; going: Record<string, RecordRun[] | undefined> }> {
  return request(`/api/structs/${struct}/records/flows?records=${records.join(',')}`)
}

// Starts a run of the flow on each record, its queries of the struct's
// records reading that one alone.
export function startRecordRuns(struct: number, flow: number, records: number[]): Promise<FlowRun[]> {
  return request(`/api/structs/${struct}/records/runs`, { method: 'POST', body: JSON.stringify({ flow, records }) })
}

// A module other flows run stays one.
export function updateFlow(id: number, patch: Partial<Pick<Flow, 'name' | 'module' | 'settings'>>): Promise<Flow> {
  return request(`/api/flows/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

// Moves the flow to the trash, where it can be restored from, unless runs
// are going on in it and stopRuns doesn't say to stop them, which can't be
// undone: then it moves nothing and says how many are going on.
export async function trashFlow(id: number, stopRuns = false): Promise<{ running: number }> {
  const url = `/api/flows/${id}`
  const res = await fetch(url, { method: 'DELETE', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ stopRuns }) })
  if (res.status === 401) signIn()
  if (res.status === 409) {
    const refused = (await res.json().catch(() => null)) as { running?: number } | null
    if (typeof refused?.running === 'number') return { running: refused.running }
  }
  if (!res.ok) throw new Error(`DELETE ${url}: ${res.status} ${await res.text()}`)
  return { running: 0 }
}

// A flow, procedure or struct in the trash, which those who may change it
// restore or delete for good; the trash empties itself of it at goesAt.
export type TrashedFlow = {
  id: number
  name: string
  module: boolean
  struct: boolean
  owner: FlowOwner
  deletedAt: string
  // Who moved it there; empty without sign-in.
  deletedBy: string
  goesAt: string
  // Whether a flow out of the trash has its name, so it comes back under another.
  nameTaken: boolean
  // How many flows run it, or have copies of its object, which fail to run
  // it, or have the copies as their own, until it comes back.
  usedBy: number
}

// What whoever asks may restore, the latest moved there first.
export function listTrash(): Promise<TrashedFlow[]> {
  return request('/api/trash')
}

// Brings it back, under name if given, as its own was taken.
export function restoreFlow(id: number, name?: string): Promise<Flow> {
  return request(`/api/trash/${id}/restore`, { method: 'POST', body: JSON.stringify(name ? { name } : {}) })
}

// Everything in it goes with it, its runs and versions too.
export function deleteForGood(id: number): Promise<void> {
  return request(`/api/trash/${id}`, { method: 'DELETE' })
}

export function listProcesses(flow: number): Promise<Process[]> {
  return request(`/api/processes?flow=${flow}`)
}

// On a flow process's board, it goes in that process's flow.
export function createProcess(
  p: Omit<Process, 'id' | 'width' | 'recording' | 'settings'> & {
    width?: number | null
    recording?: boolean
    settings?: ProcessSettings
  },
): Promise<Process> {
  return request('/api/processes', { method: 'POST', body: JSON.stringify(p) })
}

export function updateProcess(id: number, patch: Partial<Omit<Process, 'id' | 'flow'>>): Promise<Process> {
  return request(`/api/processes/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

export function deleteProcess(id: number): Promise<void> {
  return request(`/api/processes/${id}`, { method: 'DELETE' })
}

// A board is the top board, null, or a flow process's, by its id.
export type BoardId = number | null

// A flow process's board, or the flow's top board.
const boardQuery = (flow: number, board: BoardId) => (board === null ? `?flow=${flow}` : `?board=${board}`)

export function getStart(flow: number): Promise<Start> {
  return request(`/api/start${boardQuery(flow, null)}`)
}

export function updateStart(flow: number, board: BoardId, patch: Partial<Start>): Promise<Start> {
  return request(`/api/start${boardQuery(flow, board)}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

// Where a board ends. On a flow process's board, the objects it generates
// show there; on a flow's top board, the objects it leaves lead into it.
export type End = { x: number; y: number }

// A flow process's board: where it starts and ends.
export type InnerBoard = { process: number; start: Start; end: End }

export function listBoards(flow: number): Promise<InnerBoard[]> {
  return request(`/api/boards?flow=${flow}`)
}

// The top board's end; null until it is moved.
export function getEnd(flow: number): Promise<End | null> {
  return request(`/api/end${boardQuery(flow, null)}`)
}

export function updateEnd(flow: number, board: BoardId, patch: Partial<End>): Promise<End> {
  return request(`/api/end${boardQuery(flow, board)}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

// Moves processes, and the selected objects only they link to, into a new
// flow process in their place.
export function wrapProcesses(w: { name: string; processes: number[]; objects: number[] }): Promise<Process> {
  return request('/api/processes/wrap', { method: 'POST', body: JSON.stringify(w) })
}

// What was copied from a board: the processes and objects selected on it,
// and the top left of where they were.
// Notes are left out of what was copied before notes were.
export type Copied = { flow: number; processes: number[]; objects: number[]; notes?: number[]; at: { x: number; y: number } }

// Copies what was copied onto a board of a flow, moved by dx and dy, with
// what it takes along: a flow process's board, what makes an object up, and
// the links between them. Resolves to the copies of what was selected.
export function pasteCopied(
  c: Copied,
  flow: number,
  board: BoardId,
  dx: number,
  dy: number,
): Promise<{ processes: number[]; objects: number[]; notes: number[] }> {
  return request('/api/paste', {
    method: 'POST',
    body: JSON.stringify({ from: c.flow, processes: c.processes, objects: c.objects, notes: c.notes ?? [], flow, board, dx, dy }),
  })
}

// The colours a note comes in, the first a new one's; see notes.go.
export const NOTE_COLOURS = ['yellow', 'blue', 'green', 'pink', 'purple', 'grey'] as const
export type NoteColour = (typeof NOTE_COLOURS)[number]

// A box of Markdown text on a board, saying what the board doesn't, such as
// why it is built so; no run reads it. It may point at a process or an
// object of the flow, drawn as a line to it while both are on the board shown.
export type Note = {
  id: number
  // The flow process whose board it is on; null on the top board.
  parent: number | null
  x: number
  y: number
  width: number
  height: number
  text: string
  colour: NoteColour
  process: number | null
  object: number | null
}

export function listNotes(flow: number): Promise<Note[]> {
  return request(`/api/notes?flow=${flow}`)
}

// A new note, on the flow's top board or a flow process's.
export function createNote(n: { flow: number; parent: number | null; x: number; y: number; text: string; colour?: NoteColour }): Promise<Note> {
  return request('/api/notes', { method: 'POST', body: JSON.stringify(n) })
}

// Changes what the patch gives; giving process or object points the note at
// it, or with null at nothing.
export function updateNote(
  id: number,
  patch: Partial<Pick<Note, 'x' | 'y' | 'width' | 'height' | 'text' | 'colour' | 'process' | 'object'>>,
): Promise<Note> {
  return request(`/api/notes/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

export function deleteNote(id: number): Promise<void> {
  return request(`/api/notes/${id}`, { method: 'DELETE' })
}

// The flow's board in OPL, OPM's Object-Process Language, as it is now.
export async function getFlowOPL(flow: number): Promise<string> {
  const { text } = await request<{ text: string }>(`/api/flows/${flow}/opl`)
  return text
}

// The flow alone as a setup file, the YAML Export writes it in, as it is
// now; the structs and procedures it uses are named, not written out.
export function getFlowSetup(flow: number): Promise<{ name: string; text: string }> {
  return request(`/api/flows/${flow}/setup`)
}

export function listConnections(flow: number): Promise<Connection[]> {
  return request(`/api/connections?flow=${flow}`)
}

export function createConnection(c: Omit<Connection, 'id'>): Promise<Connection> {
  return request('/api/connections', { method: 'POST', body: JSON.stringify(c) })
}

export function deleteConnection(id: number): Promise<void> {
  return request(`/api/connections/${id}`, { method: 'DELETE' })
}

export function listObjects(flow: number): Promise<FlowObject[]> {
  return request(`/api/objects?flow=${flow}`)
}

// On a flow process's board, it goes in that process's flow; owned, in its
// owner's.
export function createObject(
  o: Omit<
    FlowObject,
    | 'id'
    | 'states'
    | 'attributes'
    | 'width'
    | 'persistent'
    | 'shared'
    | 'source'
    | 'sourceFlow'
    | 'many'
    | 'min'
    | 'max'
    | 'human'
    | 'end'
    | 'parent'
    | 'owner'
    | 'relation'
    | 'general'
  > & {
    width?: number | null
    human?: boolean
    parent?: number | null
    owner?: number | null
    relation?: Relation
  },
): Promise<FlowObject> {
  return request('/api/objects', { method: 'POST', body: JSON.stringify(o) })
}

export function updateObject(id: number, patch: Partial<Omit<FlowObject, 'id' | 'flow' | 'states' | 'attributes'>>): Promise<FlowObject> {
  return request(`/api/objects/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

export function deleteObject(id: number): Promise<void> {
  return request(`/api/objects/${id}`, { method: 'DELETE' })
}

export function listLinks(flow: number): Promise<Link[]> {
  return request(`/api/links?flow=${flow}`)
}

// Which stored records a query link takes: those whose fields hold the rule
// (all when empty) in the state (any when empty), newest first unless
// oldest, at most limit (20 when absent, 100 at most).
export type LinkQuery = {
  where?: string
  state?: string
  limit?: number
  oldest?: boolean
}

// Without a state, the link names none; without states, it lists none.
export function createLink(
  l: Omit<Link, 'id' | 'state' | 'states' | 'route' | 'at' | 'role' | 'condition' | 'query'> & {
    state?: number | null
    states?: number[]
    role?: LinkRole
    condition?: boolean
    query?: LinkQuery | null
  },
): Promise<Link> {
  return request('/api/links', { method: 'POST', body: JSON.stringify(l) })
}

// Sets where a link's segments run; null goes back to the default route.
export function setLinkRoute(id: number, route: number[] | null): Promise<void> {
  return request(`/api/links/${id}`, { method: 'PATCH', body: JSON.stringify({ route }) })
}

// Places the object on the link's process's own board; null goes back to the default place.
export function setLinkAt(id: number, at: { x: number; y: number } | null): Promise<void> {
  return request(`/api/links/${id}/at`, { method: 'PUT', body: JSON.stringify(at) })
}

// Replaces the states a human's output link decides between.
export type LinkRole = '' | 'instrument' | 'consumption'

export function setLinkCondition(id: number, condition: boolean): Promise<void> {
  return request(`/api/links/${id}/condition`, { method: 'PUT', body: JSON.stringify({ condition }) })
}

// Makes an input link a query, or with null a plain link again.
export function setLinkQuery(id: number, query: LinkQuery | null): Promise<void> {
  return request(`/api/links/${id}/query`, { method: 'PUT', body: JSON.stringify({ query }) })
}

export function setLinkRole(id: number, role: LinkRole): Promise<void> {
  return request(`/api/links/${id}/role`, { method: 'PUT', body: JSON.stringify({ role }) })
}

export function setLinkStates(id: number, states: number[]): Promise<void> {
  return request(`/api/links/${id}/states`, { method: 'PUT', body: JSON.stringify({ states }) })
}

export function deleteLink(id: number): Promise<void> {
  return request(`/api/links/${id}`, { method: 'DELETE' })
}

export function createState(object: number, name: string): Promise<ObjectState> {
  return request(`/api/objects/${object}/states`, { method: 'POST', body: JSON.stringify({ name }) })
}

export function renameState(id: number, name: string): Promise<ObjectState> {
  return request(`/api/states/${id}`, { method: 'PATCH', body: JSON.stringify({ name }) })
}

// An empty rule is none.
export function setStateRule(id: number, rule: string): Promise<ObjectState> {
  return request(`/api/states/${id}`, { method: 'PATCH', body: JSON.stringify({ rule }) })
}

// The links naming the state go with it.
export function deleteState(id: number): Promise<void> {
  return request(`/api/states/${id}`, { method: 'DELETE' })
}

export function createAttribute(object: number, a: { name: string; definition: string }): Promise<ObjectAttribute> {
  return request(`/api/objects/${object}/attributes`, { method: 'POST', body: JSON.stringify(a) })
}

export function updateAttribute(id: number, patch: Partial<Pick<ObjectAttribute, 'name' | 'definition' | 'unique' | 'file'>>): Promise<ObjectAttribute> {
  return request(`/api/attributes/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

export function deleteAttribute(id: number): Promise<void> {
  return request(`/api/attributes/${id}`, { method: 'DELETE' })
}

export type ThinkingLevel = 'off' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'

export const THINKING_LEVELS: ThinkingLevel[] = ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']

export type Models = {
  // The default model as provider/id: the one kept in the settings, or pi's;
  // empty if pi picks one itself.
  default: string
  // The model providers' picked models, and pi's enabledModels of others.
  models: string[]
  // The names of the providers added in the settings, by ID.
  providers?: Record<string, string>
}

export function listModels(): Promise<Models> {
  return request('/api/models')
}

// An MCP server agents can use once it is connected, which signs in through
// the browser; each agent process picks the ones it uses. With sign-in,
// connected says whether whoever asked connected it for themselves.
// A server taking a token, a connector among them, has one an admin sets
// for everyone instead, and connected says whether it is set.
export type MCPServer = {
  id: number
  name: string
  url: string
  description: string
  auth: MCPAuth
  // The built-in connector it is, if it is one; its URL is then its service's API.
  connector?: string
  connected: boolean
}

export type MCPAuth = 'oauth' | 'token'

export function listMCPServers(): Promise<MCPServer[]> {
  return request('/api/mcp-servers')
}

export function createMCPServer(
  s: Pick<MCPServer, 'name' | 'url' | 'description'> & { auth?: MCPAuth; connector?: string },
): Promise<MCPServer> {
  return request('/api/mcp-servers', { method: 'POST', body: JSON.stringify(s) })
}

// Sets the token of a server taking one, which nobody sees again.
export function setMCPToken(id: number, token: string): Promise<void> {
  return request(`/api/mcp-servers/${id}/token`, { method: 'PUT', body: JSON.stringify({ token }) })
}

export function clearMCPToken(id: number): Promise<void> {
  return request(`/api/mcp-servers/${id}/token`, { method: 'DELETE' })
}

// A tool of a server, as an agent process picks those it uses.
export type MCPTool = {
  name: string
  title?: string
  description?: string
  // Whether it changes nothing, as it says.
  readOnly: boolean
}

export function listMCPTools(id: number): Promise<MCPTool[]> {
  return request(`/api/mcp-servers/${id}/tools`)
}

// An entry of the settings' directory: a connector built into the webapp,
// or an MCP server picked to go with them, either installed as an MCP server.
export type DirectoryEntry = {
  name: string
  title: string
  category: string
  description: string
  connector: boolean
  // Its service's API, or '' for one that is wherever it was installed,
  // such as Home Assistant, whose URL looks like urlExample.
  url: string
  urlExample?: string
  auth: MCPAuth
  tokenHelp?: string
  // Whether it may be installed several times, each for its own service.
  multiple?: boolean
  docs?: string
  operations?: ConnectorOperation[]
  // The installed server, if any.
  installed: number | null
}

export type ConnectorOperation = {
  name: string
  title: string
  description: string
  // Whether it changes something on the service.
  write: boolean
  params: ConnectorParam[]
}

export type ConnectorParam = {
  name: string
  description: string
  type: 'string' | 'integer' | 'boolean' | 'strings'
  required?: boolean
  enum?: string[]
}

export function listDirectory(): Promise<DirectoryEntry[]> {
  return request('/api/connectors')
}

export function updateMCPServer(id: number, patch: Partial<Pick<MCPServer, 'name' | 'url' | 'description'>>): Promise<MCPServer> {
  return request(`/api/mcp-servers/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

export function deleteMCPServer(id: number): Promise<void> {
  return request(`/api/mcp-servers/${id}`, { method: 'DELETE' })
}

export function disconnectMCPServer(id: number): Promise<void> {
  return request(`/api/mcp-servers/${id}/disconnect`, { method: 'POST' })
}

// Signs in to the server in a window of its own, which tells this one when
// it is done with a "mcp-sign-in" message. Call it straight from a click, so
// the window isn't blocked.
export function connectMCPServer(id: number) {
  window.open(`/api/mcp-servers/${id}/connect`, `mcp-sign-in-${id}`, 'popup,width=560,height=720')
}

// An object the process takes as input, with what the process that generated
// it printed and the state it is in, if any.
export type RunInput = {
  name: string
  state?: string
  content: string
}

// Sent to a process taking an object in the state a human set it to, when
// the human left instructions: what they reviewed and what to do with it.
// Requests from before states have no state.
export type ChangeRequest = {
  reviewer: string
  state?: string
  instructions: string
  reviewed: RunInput[]
}

// The server terminates a run whose step generated a duplicate.
export type FlowRunStatus = 'running' | 'done' | 'failed' | 'interrupted' | 'terminated'

// One execution of the flow from the start node.
export type FlowRun = {
  id: number
  flow: number
  status: FlowRunStatus
  // Running with a step waiting for a person: a review, or a loop's question.
  waiting: boolean
  // The version of the board it ran on; starting a run saves a board changed
  // since its version as a new one. Null for runs from before versions were kept.
  version: number | null
  // For the run of a recording flow process: the run it ran in, and the
  // process. Null for runs of the whole flow. The run of a flow a subflow
  // process ran has a parent in that process's flow, and no process. A
  // re-run of such a run is another run of it in the same run.
  parent: number | null
  process: number | null
  processName: string
  // For a run re-running another from one of its processes: that run, and
  // the process.
  rerunOf: number | null
  rerunFrom: number | null
  rerunFromName: string
  // For the run of one item of a list: what tells the item apart.
  itemKey: string
  // The schedule that started it, if one did.
  schedule: number | null
  // Who started it, when people sign in; empty otherwise.
  startedBy: string
  // The name of the API key that started it, if one did.
  apiKey: string
  // The name of the webhook that started it, empty once it is removed;
  // null for a run no webhook started.
  webhook: string | null
  // The shared record a person started it on, if one did; its queries of
  // the record's store read that record alone.
  record: number | null
  // For a trial: the process a person tried in it, which alone ran; see
  // TrialDialog.
  trialOf: number | null
  trialOfName: string
  startedAt: string
  finishedAt: string | null
}

// What a step did to a repository: the commit it started from, the one it
// left, the commits it made in between, newest first, as "<short hash>
// <subject>", and whether it left changes uncommitted.
export type RepoCommits = {
  name: string
  before: string
  after: string
  commits: string[]
  uncommitted: boolean
}

// A skipped step's process didn't run, its condition not holding.
export type StepStatus = 'active' | 'waiting' | 'done' | 'error' | 'duplicate' | 'skipped'

// One execution of a process in a run, with the process as it was then.
// The tokens a step's model calls took in and gave out, and what they cost
// at the models' catalog prices, in dollars.
export type StepUsage = { input: number; output: number; cost: number }

export type RunStep = {
  id: number
  // Kept once the process is deleted; null only for steps whose process was
  // deleted before that.
  process: number | null
  processName: string
  // A re-run's step holding what it copied from the run it re-runs has no
  // process, and is of kind copied; so has the step holding what a run
  // started over the API was given, of kind input.
  kind: ProcessKind | 'copied' | 'input' | 'loop'
  model: string
  thinking: ThinkingLevel | ''
  // For agents, the prompt as sent, inputs and change request included; for
  // a loop step, what it asks: whether its process goes on round a loop.
  prompt: string
  inputs: RunInput[]
  changeRequest: ChangeRequest | null
  status: StepStatus
  output: string
  error: string
  // For human steps: the name of the state the human set; steps from before
  // states hold approved or changes.
  outcome: string
  instructions: string
  // Who decided it, when people sign in; empty otherwise. For human steps,
  // the roles that decide them, absent when anyone does, and whether the
  // person signed in may; loop steps are answered by those who may edit
  // the flow.
  decidedBy: string
  handledBy?: string[]
  // Whether a review was handed on to more roles, which handledBy includes.
  escalated?: boolean
  // What people said of a review, oldest first.
  comments?: ReviewComment[]
  canDecide: boolean
  // For a recording flow process: the run its board ran as.
  childRun: number | null
  // The branch it worked on in its run's workspace; empty if it worked in none.
  branch: string
  // What it did to each repository it worked on.
  commits: RepoCommits[]
  // For an agent step that kept its session: the pi session's id, empty if
  // none; a person can carry its conversation on, and the follow-ups they sent.
  session: string
  canFollowUp: boolean
  followUps: FollowUp[]
  // What an agent step's model calls used, as pi reports it; absent for
  // other steps.
  usage?: StepUsage
  // For an agent, script or connector step: how long it waited for its
  // slots before it started, its person's and the engine's, in
  // milliseconds, and which try of its process it is, from 1, one more for
  // each retry after a failure. Null for other steps and those from before
  // they were recorded.
  slotWaitMs: number | null
  try: number | null
  startedAt: string
  finishedAt: string | null
}

// A message a person sent an agent step once it was over, which the agent
// acted on in its session; another try of the step's conversation.
export type FollowUp = {
  id: number
  attempt: number
  message: string
  status: 'active' | 'done' | 'error'
  output: string
  error: string
  commits: RepoCommits[]
  startedAt: string
  finishedAt: string | null
}

// The content and state an object got from the step that generated it, or
// the state a human step set it to.
export type RunObject = {
  id: number
  step: number
  // Kept once the object is deleted, as a step's process is.
  object: number | null
  name: string
  state: string
  content: string
  // The generated files its content names; fileUrl opens one.
  files: string[]
}

export const fileUrl = (path: string) => `/api/files?path=${encodeURIComponent(path)}`

// A time the run moved to another replica of the webapp, as the one walking
// it stopped: from which replica to which, the host it went to, and the
// steps that were going then, lost, each tried again or failed; see
// adopt.go.
export type RunMove = {
  from: string
  to: string
  host: string
  lost: { step: number; process: string; retried: boolean }[]
  movedAt: string
}

// spent is what the run, with its subflows' and items' runs, spent on its
// models, in dollars, and spendLimit the most it may, 0 for no limit.
export type FlowRunDetail = FlowRun & { steps: RunStep[]; objects: RunObject[]; spent: number; spendLimit: number; moves: RunMove[] }

// Runs a flow on its own whenever its cron expression comes round, on the
// board as it is, as the Run button does. A time that comes round while its
// last run is still going is skipped.
export type Schedule = {
  id: number
  flow: number
  flowName: string
  // Five fields, minute to day of the week, or a descriptor such as @hourly
  // or @every 90m; a minute apart at the least.
  cron: string
  // The IANA zone its times are in, such as Europe/London.
  timezone: string
  enabled: boolean
  // Null while disabled.
  nextRunAt: string | null
  lastRun: FlowRun | null
  createdAt: string
}

export type ScheduleFields = Pick<Schedule, 'flow' | 'cron' | 'timezone'>

export function listSchedules(): Promise<Schedule[]> {
  return request('/api/schedules')
}

export function createSchedule(s: ScheduleFields): Promise<Schedule> {
  return request('/api/schedules', { method: 'POST', body: JSON.stringify(s) })
}

// Changing when it runs, or enabling it, counts its next time from now.
export function updateSchedule(id: number, patch: Partial<ScheduleFields & Pick<Schedule, 'enabled'>>): Promise<Schedule> {
  return request(`/api/schedules/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

// Its runs stay.
export function deleteSchedule(id: number): Promise<void> {
  return request(`/api/schedules/${id}`, { method: 'DELETE' })
}

// The next times a cron expression gives in a time zone, or why it gives none.
export async function previewSchedule(cron: string, timezone: string): Promise<{ upcoming: string[] } | { problem: string }> {
  const res = await fetch(`/api/schedules/preview?${new URLSearchParams({ cron, timezone })}`)
  if (res.status === 400) return { problem: (await res.text()).trim() }
  if (!res.ok) throw new Error(`preview schedule: ${res.status} ${await res.text()}`)
  return res.json()
}

// How a run stands, as the run list picks runs by: waiting is running with
// a step waiting for a person, and running the others going.
export type RunStanding = 'running' | 'waiting' | 'done' | 'failed' | 'interrupted' | 'terminated'

// How a run started: by a person with the Run button, a schedule, the API,
// a webhook, as a re-run of another, on records a person picked, or in
// another run (a flow process's board, an item of a list, a subflow).
export type RunOrigin = 'person' | 'schedule' | 'api' | 'webhook' | 'rerun' | 'record' | 'run' | 'trial'

// What narrows the run list; each picks any of its values, none for all,
// but for how runs started, where none is every way but a trial of one
// process alone: trials are listed only when asked for.
export type RunFilter = { status: RunStanding[]; started: RunOrigin[] }

// The most runs a page of the list holds; a full page means there may be more.
export const RUN_PAGE = 200

// How a run started, the first its markers say, as run_list.go tells.
export function runOrigin(r: FlowRun): RunOrigin {
  if (r.trialOf !== null) return 'trial'
  if (r.rerunOf !== null) return 'rerun'
  if (r.record !== null) return 'record'
  if (r.apiKey) return 'api'
  if (r.webhook !== null) return 'webhook'
  if (r.schedule !== null) return 'schedule'
  return r.parent !== null ? 'run' : 'person'
}

export const runStanding = (r: FlowRun): RunStanding => (r.status === 'running' ? (r.waiting ? 'waiting' : 'running') : r.status)

// Whether the run is one the filter picks.
export const runMatches = (r: FlowRun, f: RunFilter) =>
  (f.status.length === 0 || f.status.includes(runStanding(r))) &&
  (f.started.length === 0 ? runOrigin(r) !== 'trial' : f.started.includes(runOrigin(r)))

// The flow's runs the filter picks, newest first, a page at a time: those
// older than the run before, if given, and as many as the limit, if given.
export function listRuns(flow: number, filter?: RunFilter, before?: number, limit?: number): Promise<FlowRun[]> {
  const p = new URLSearchParams({ flow: String(flow) })
  if (filter?.status.length) p.set('status', filter.status.join(','))
  if (filter?.started.length) p.set('started', filter.started.join(','))
  if (before !== undefined) p.set('before', String(before))
  if (limit !== undefined) p.set('limit', String(limit))
  return request(`/api/runs?${p}`)
}

// Starts a run on the board as it is, or with published on the version the
// flow published.
// Starts a run of the flow, given what it starts from if Run's menu asks
// for it: each object by name, holding its text, or the JSON its attributes
// make, and the states some of them are in.
export function startRun(flow: number, published = false, given?: StartGiven): Promise<FlowRun> {
  return request(`/api/runs?flow=${flow}`, { method: 'POST', body: JSON.stringify({ published, ...given }) })
}

export type StartGiven = { inputs: Record<string, unknown>; states: Record<string, string> }

// What a run started by hand is given: the objects the board as it is
// starts from, or with published the version published's, each with a JSON
// Schema of what to give of it, or why it can't be given anything.
export type FlowInputs = { inputs: FlowAPI['inputs']; problem?: string }

export function getFlowInputs(flow: number, published = false): Promise<FlowInputs> {
  return request(`/api/flows/${flow}/inputs?published=${published}`)
}

// An object the process a run is re-run from takes, as it took it in the
// run; present is false if the run had nothing of it then.
export type RerunInput = { object: number; name: string; state: string; content: string; present: boolean }

// What re-running a run from a process takes: the objects the process took,
// which can be changed, and the names of the objects the re-run copies from
// the run and those it generates anew.
export type RerunPlan = { process: number; processName: string; inputs: RerunInput[]; copied: string[]; anew: string[] }

export function getRerun(run: number, step: number): Promise<RerunPlan> {
  return request(`/api/runs/${run}/steps/${step}/rerun`)
}

// Re-runs the run from the step's process as a new run on the board as it
// is, with content in place of what the process took of each object given,
// by object id.
export function rerun(run: number, step: number, inputs: Record<number, string>): Promise<FlowRun> {
  return request(`/api/runs/${run}/steps/${step}/rerun`, { method: 'POST', body: JSON.stringify({ inputs }) })
}

// An object a process tried takes: the states it can be given in, and the
// one it is given in unless someone says otherwise, the state its link
// waits for or one its conditions name; empty for none.
export type TrialObject = { object: number; name: string; states: string[]; state: string }

// What an object is given in a trial, or a sample gives it: its state, empty
// for none, and what it holds.
export type TrialValue = { object: number; state: string; content: string }

// Inputs saved with a process to try it on, under a name of their own.
export type TrialSample = { id: number; name: string; inputs: TrialValue[] }

// A run of the flow, a trial or not, trying a process may start from: one
// that reached it, or else holds an object it takes, such as a trial of
// the process before it.
export type TrialPast = {
  run: number
  trial: boolean
  // The process a trial tried.
  trialOf: string
  status: FlowRunStatus
  startedAt: string
  reached: boolean
}

// What trying a process takes, the samples it may start from, the latest
// runs it may start from, and what it does outside the flow, which it does
// in a trial too.
export type TrialPlan = {
  process: number
  processName: string
  kind: ProcessKind
  takes: TrialObject[]
  samples: TrialSample[]
  past: TrialPast[]
  effects: string[]
}

export function getTrial(process: number): Promise<TrialPlan> {
  return request(`/api/processes/${process}/trial`)
}

// What a run of the flow gives the objects the process takes: what it took
// there, or else what the run held of each last.
export function getTrialRun(process: number, run: number): Promise<TrialValue[]> {
  return request(`/api/processes/${process}/trial/runs/${run}`)
}

// Tries the process alone, as a trial on the board as it is, giving it the
// objects listed and none of the others it takes.
export function tryProcess(process: number, inputs: TrialValue[]): Promise<FlowRun> {
  return request(`/api/processes/${process}/trial`, { method: 'POST', body: JSON.stringify({ inputs }) })
}

// A trial of a process on one of its samples.
export type SampleTrial = { sample: number; name: string; run: FlowRun }

// Tries the process on each of its samples at once, a trial each, in the
// order the samples were saved.
export function trySamples(process: number): Promise<SampleTrial[]> {
  return request(`/api/processes/${process}/trial/samples`, { method: 'POST' })
}

// Saves inputs as the process's sample of that name, replacing one it has;
// resolves to its samples.
export function saveSample(process: number, name: string, inputs: TrialValue[]): Promise<TrialSample[]> {
  return request(`/api/processes/${process}/samples`, { method: 'PUT', body: JSON.stringify({ name, inputs }) })
}

export function deleteSample(process: number, sample: number): Promise<void> {
  return request(`/api/processes/${process}/samples/${sample}`, { method: 'DELETE' })
}

// What a run held of an object: the latest content and state its steps gave
// it, or for the run of a recording flow process what it was given, such as
// the item of a list it ran for.
export type DataCell = {
  // Null for an object deleted before ids were kept, or a given object the
  // flow has none named as.
  object: number | null
  name: string
  state: string
  content: string
  given: boolean
}

// A run and what it held of each object.
export type DataRow = FlowRun & { cells: DataCell[] }

// An object on the flow's boards with its attributes, in the order its
// content holds them.
export type DataObject = { id: number; name: string; many: boolean; attributes: string[] }

// The flow's objects and what the latest of its runs the search matches
// held of them, newest first or as sorted; and how many of the runs searched
// were trials it left out, its query not naming the trial facet.
export type FlowData = { objects: DataObject[]; rows: DataRow[]; trials: number } & SearchResult

export function getFlowData(flow: number, search?: Search): Promise<FlowData> {
  const p = searchParams(search)
  return request(`/api/data?flow=${flow}${p ? '&' + p : ''}`)
}

export function getRun(id: number): Promise<FlowRunDetail> {
  return request(`/api/runs/${id}`)
}

// A line of a run's log: the webapp's about the run and its steps, or one the
// worker logged of a step it ran; see logs.go.
export type RunLogLine = {
  id: number
  step: number | null
  source: 'webapp' | 'worker'
  level: 'info' | 'error'
  message: string
  time: string
}

// The run's log, oldest first; after gives only the lines past that id.
export function getRunLogs(id: number, after = 0): Promise<RunLogLine[]> {
  return request(`/api/runs/${id}/logs?after=${after}`)
}

// A content block of a message of an agent's conversation, as pi has it.
// Images come without their data.
export type MessageBlock =
  | { type: 'text'; text: string }
  | { type: 'thinking'; thinking: string; redacted?: boolean }
  | { type: 'toolCall'; id: string; name: string; arguments: unknown }
  | { type: 'image'; mimeType: string }

// A message of an agent's conversation, as pi has it. A message too large
// to keep has only its role, and omitted holds its size.
export type AgentMessage = {
  role: 'system' | 'user' | 'assistant' | 'toolResult' | string
  content?: string | MessageBlock[]
  omitted?: number
  // System messages: the prompt in named sections.
  sections?: Record<string, string | null>
  // Assistant messages.
  model?: string
  stopReason?: string
  errorMessage?: string
  // Tool results, with the call they answer; an edit's details hold its
  // patch unless too large to keep.
  toolCallId?: string
  toolName?: string
  isError?: boolean
  details?: { patch?: string; diff?: string }
}

export type StepMessage = {
  id: number
  // Which try of the step it belongs to, from 1.
  attempt: number
  message: AgentMessage
  createdAt: string
}

// An agent step's conversation; after, a message id, gives only the messages since.
export function getStepMessages(run: number, id: number, after = 0): Promise<StepMessage[]> {
  return request(`/api/runs/${run}/steps/${id}/messages?after=${after}`)
}

// A file a step changed, with its part of the diff; omitted when too large
// to show.
export type FileChange = {
  path: string
  oldPath?: string
  status: 'added' | 'modified' | 'deleted' | 'renamed' | 'copied' | 'type changed'
  additions: number
  deletions: number
  binary?: boolean
  patch: string
  omitted?: boolean
}

// What a step, with its follow-ups, changed in a repository from the commit
// it started from to the one it left; more counts the files past those
// listed, and error says why they couldn't be read.
export type RepoChanges = {
  name: string
  before: string
  after: string
  uncommitted: boolean
  files: FileChange[]
  more: number
  error?: string
}

// A chunk of what a script step printed, as it printed it.
export type OutputChunk = {
  id: number
  stream: 'stdout' | 'stderr'
  text: string
  createdAt: string
}

// What the script step printed, in order; after gives only what came since.
export function getStepOutput(run: number, id: number, after = 0): Promise<OutputChunk[]> {
  return request(`/api/runs/${run}/steps/${id}/output?after=${after}`)
}

export function getStepChanges(run: number, id: number): Promise<RepoChanges[]> {
  return request(`/api/runs/${run}/steps/${id}/changes`)
}

// Ends a run where it is; steps still going are cancelled.
export function stopRun(id: number): Promise<FlowRun> {
  return request(`/api/runs/${id}`, { method: 'PATCH', body: JSON.stringify({ status: 'interrupted' }) })
}

// Stops an agent, script or connector step still going. It fails, which ends its
// branch; the rest of the run goes on.
export function stopStep(run: number, id: number): Promise<void> {
  return request(`/api/runs/${run}/steps/${id}/stop`, { method: 'POST' })
}

// Carries on the conversation of an agent step that is over with a message,
// which the agent acts on in its session, in the run's workspace.
export function sendFollowUp(run: number, step: number, message: string): Promise<FollowUp> {
  return request(`/api/runs/${run}/steps/${step}/followups`, { method: 'POST', body: JSON.stringify({ message }) })
}

export function stopFollowUp(run: number, step: number, id: number): Promise<void> {
  return request(`/api/runs/${run}/steps/${step}/followups/${id}/stop`, { method: 'POST' })
}

// A message of a flow's conversation with its assistant, the agent beside the
// board that sets the flow up as the person asks it to.
export type AssistantMessage = {
  id: number
  role: 'person' | 'assistant'
  content: string
  // A reply is active while the agent works on it, then done or error.
  status: 'active' | 'done' | 'error'
  error: string
  // Whether the reply changed the flow, and the version the board was saved
  // as first, which the board shows what it changed against.
  applied: boolean
  version: number | null
  createdAt: string
  finishedAt: string | null
}

export function getAssistant(flow: number): Promise<AssistantMessage[]> {
  return request(`/api/flows/${flow}/assistant`)
}

// Sends the assistant a message, and returns the conversation with its reply
// going. Sent around request: the board changes only once the reply is in.
export async function askAssistant(flow: number, message: string): Promise<AssistantMessage[]> {
  return (await send(`/api/flows/${flow}/assistant`, { method: 'POST', body: JSON.stringify({ message }) })).json()
}

export async function stopAssistant(flow: number): Promise<void> {
  await send(`/api/flows/${flow}/assistant/stop`, { method: 'POST' })
}

// Starts the conversation over.
export async function clearAssistant(flow: number): Promise<void> {
  await send(`/api/flows/${flow}/assistant`, { method: 'DELETE' })
}

// Decides a human step waiting for its review, and lets the run go on: the
// state the human set, if they had any to choose from, their instructions,
// and what they entered of the objects the step fills in, by object id: for
// an object with attributes a JSON object of them, or for a list an array of
// such objects; otherwise the text entered.
// What a person entered for each object, by object id: the text of one
// without fields, or a JSON value with its fields, nested as they are.
export type EntryValue = string | EntryValue[] | { [name: string]: EntryValue }
export type Entries = Record<string, EntryValue>

export async function decideStep(
  run: number,
  id: number,
  decision: { state: number | null; instructions: string; entries?: Entries },
): Promise<void> {
  await request(`/api/runs/${run}/steps/${id}`, { method: 'PATCH', body: JSON.stringify(decision) })
  inboxChanged()
}

// What someone said of a review while it waited, empty author without
// sign-in; see comments.go.
export type ReviewComment = { id: number; author: string; text: string; at: string }

// Comments on a review waiting, which those who may decide it, or edit its
// flow, may; resolves to its comments.
export function commentOnReview(run: number, step: number, text: string): Promise<ReviewComment[]> {
  return request(`/api/runs/${run}/steps/${step}/comments`, { method: 'POST', body: JSON.stringify({ text }) })
}

// Answers a loop step: whether its process goes on round the loop, or stops,
// which fails the run.
export async function answerLoop(run: number, id: number, goOn: boolean): Promise<void> {
  await request(`/api/runs/${run}/steps/${id}/loop`, { method: 'POST', body: JSON.stringify({ goOn }) })
  inboxChanged()
}

// A step waiting on the person signed in: a review or input they may
// decide, or a loop question of a flow they may edit; without sign-in,
// every step waiting. See inbox.go.
export type InboxItem = {
  step: number
  run: number
  // The run's flow, and the run it is within, for a flow process's or a
  // subflow's run.
  flow: number
  flowName: string
  parent: number | null
  // Kept once the process is deleted, with its name then.
  process: number | null
  processName: string
  // A decision on a review, what they fill in, or whether a process goes on
  // round a loop.
  waits: 'review' | 'input' | 'loop'
  // A loop step's question, or the human process's instructions.
  detail: string
  // The roles that decide it, none when anyone does.
  handledBy: string[]
  // Whether it was handed on to more roles, which handledBy includes.
  escalated: boolean
  // Who started its run, when someone did.
  startedBy: string
  since: string
  // When it fails if nobody decides it, if its process says.
  expires: string | null
}

// What waits on the person, newest first.
export function listInbox(): Promise<InboxItem[]> {
  return request('/api/inbox')
}

// How many steps wait on the person, asked in the background: null when it
// can't be told, without sending them to sign in as a page's own request
// does, so a session ending doesn't pull them away from what they type.
export async function countInbox(): Promise<number | null> {
  try {
    const res = await fetch('/api/inbox')
    return res.ok ? ((await res.json()) as InboxItem[]).length : null
  } catch {
    return null
  }
}

// Where the board opens on the step's run with its review showing.
export const reviewHash = (item: Pick<InboxItem, 'flow' | 'run' | 'step'>) =>
  `#/flows/${item.flow}/runs/${item.run}/steps/${item.step}`

const INBOX_CHANGED = 'inbox-changed'

// Calls back once the person decided something, which may take it out of
// their inbox.
export function onInboxChanged(callback: () => void): () => void {
  window.addEventListener(INBOX_CHANGED, callback)
  return () => window.removeEventListener(INBOX_CHANGED, callback)
}

const inboxChanged = () => window.dispatchEvent(new Event(INBOX_CHANGED))

// A saved snapshot of the whole board and the flow's settings. Saving always
// adds a version on top; jumping to an older one keeps every other version.
export type Version = {
  id: number
  // The version the board was on when this one was saved; older than the
  // previous version when saved after jumping back.
  basedOn: number | null
  createdAt: string
  // Who saved it, empty when nobody signs in, and why it was saved: empty
  // when a person saved it, or what saved it on its own first. The note is
  // the person's, saying why.
  savedBy: string
  reason: '' | 'run' | 'assistant' | 'import' | 'template'
  note: string
}

export type Versions = {
  // The version the board was last saved as or jumped to; null before the first save.
  current: number | null
  // The version published, which the runs nobody starts from the board run,
  // when and by whom; null while none is. Draft says whether the board
  // differs from it in more than where things sit.
  published: number | null
  publishedAt: string | null
  publishedBy: string
  draft: boolean
  // Newest first.
  versions: Version[]
}

export function listVersions(flow: number): Promise<Versions> {
  return request(`/api/versions?flow=${flow}`)
}

// Saves the board as a new version, with a note saying why, if any.
export function saveVersion(flow: number, note = ''): Promise<Version> {
  return request(`/api/versions?flow=${flow}`, { method: 'POST', body: JSON.stringify({ note }) })
}

// Publishes the board as it is: saved as a version if it changed, which
// the note goes with, and that version published.
export function publishBoard(flow: number, note = ''): Promise<Version> {
  return request(`/api/versions?flow=${flow}`, { method: 'POST', body: JSON.stringify({ note, publish: true }) })
}

// Publishes a version saved before, such as an older one to roll back to;
// the board stays as it is.
export function publishVersion(id: number): Promise<void> {
  return request(`/api/versions/${id}/publish`, { method: 'POST' })
}

// Stops publishing any version: the flow's runs take its board as it is again.
export function unpublish(flow: number): Promise<void> {
  return request(`/api/versions/published?flow=${flow}`, { method: 'DELETE' })
}

// One thing changed of something, as it was and as it is.
export type DiffField = { field: string; before: string; after: string }

// A process, object, state, attribute, link, connection or sample added,
// removed or changed. Its id is on the newer board, or on the older for one
// removed; board is the flow process whose board it is on. A state's or
// attribute's object is the one it is of, an object's the one it is part
// of, a link's its object; a link's or sample's process and a connection's
// ends are its process and target. A board's start is a connection without
// an id.
export type DiffItem = {
  change: 'added' | 'removed' | 'changed'
  id: number
  name: string
  board: number | null
  object?: number
  process?: number
  target?: number
  fields?: DiffField[]
}

// What changed between two boards; layout says whether anything moved,
// which the lists leave out.
export type BoardDiff = {
  from: number
  // The newer version, null for the board as it is.
  to: number | null
  processes: DiffItem[]
  objects: DiffItem[]
  states: DiffItem[]
  attributes: DiffItem[]
  links: DiffItem[]
  connections: DiffItem[]
  // What processes are tried on, and notes; they don't make a draft differ
  // from what it published, as no run of a version reads them.
  samples: DiffItem[]
  notes: DiffItem[]
  settings: DiffField[]
  layout: boolean
}

// What changed from a version to another of the same flow, or to the board
// as it is without one.
export function diffVersions(from: number, to: number | null = null): Promise<BoardDiff> {
  return request(`/api/versions/${from}/diff${to === null ? '' : `?to=${to}`}`)
}

// Rewrites the board as the version has it.
export function restoreVersion(id: number): Promise<void> {
  return request(`/api/versions/${id}/restore`, { method: 'POST' })
}

// Whether a flow's board has a change to undo, and one to redo, whether the
// board changed just now, and whether a run of it holds it, refusing undo
// and redo until it ends.
export type History = { undo: boolean; redo: boolean; changed: boolean; held: boolean }

// Keeps the board as it is as a step of its history, unless it is the step
// the board is on.
// The tab names itself as client, so that it isn't told of its own changes;
// see presence.go.
export function recordHistory(flow: number, client: string): Promise<History> {
  return request(`/api/history?flow=${flow}&client=${encodeURIComponent(client)}`, { method: 'POST' })
}

// Brings the board back a step, or forward again to the one undone last.
export function undoHistory(flow: number, client: string): Promise<History> {
  return request(`/api/history/undo?flow=${flow}&client=${encodeURIComponent(client)}`, { method: 'POST' })
}

export function redoHistory(flow: number, client: string): Promise<History> {
  return request(`/api/history/redo?flow=${flow}&client=${encodeURIComponent(client)}`, { method: 'POST' })
}

// Someone else with the board open, editing if they changed it lately.
export type BoardViewer = { id: number; name: string; editing: boolean }

// What a tab with a board open is told each time it says so: the latest
// change to the board, whether another tab changed it since the change the
// tab gave, and who did if it was someone else, and the others there.
export type Presence = { rev: number; changed: boolean; changedBy: string; people: BoardViewer[] }

// Says the board is open in this tab, which has loaded the change since,
// if it has heard of one.
export async function sayPresent(flow: number, client: string, since: number | null): Promise<Presence> {
  const res = await send(`/api/flows/${flow}/presence`, {
    method: 'PUT',
    body: JSON.stringify(since === null ? { client } : { client, since }),
  })
  return res.json()
}

// The tab leaves the board; sent as it closes too, so the others see it go
// at once rather than once it is no longer heard from.
export function leaveBoard(flow: number, client: string) {
  fetch(`/api/flows/${flow}/presence?client=${encodeURIComponent(client)}`, { method: 'DELETE', keepalive: true }).catch(() => {})
}

// The whole flow, every board of it, as the board's endpoints give it or a
// version saved it.
export type BoardContents = {
  processes: Process[]
  objects: FlowObject[]
  // The top board's start, and its end, null until it is moved.
  start: Start
  end: End | null
  connections: Connection[]
  links: Link[]
  boards: InnerBoard[]
  notes: Note[]
}

// Versions from before boards have no parents, recording or boards,
// processes from before settings none, objects that aren't people no human,
// and lists holding any number of items no min or max.
type Snapshot = {
  processes: (Omit<Process, 'flow' | 'parent' | 'recording' | 'settings'> & Partial<Pick<Process, 'parent' | 'recording' | 'settings'>>)[]
  // source is left out for an object that isn't a copy, and sourceFlow is
  // never kept.
  objects: (Omit<FlowObject, 'flow' | 'states' | 'attributes' | 'parent' | 'many' | 'min' | 'max' | 'human' | 'end' | 'owner' | 'relation' | 'general' | 'source' | 'sourceFlow'> &
    Partial<Pick<FlowObject, 'parent' | 'many' | 'min' | 'max' | 'human' | 'end' | 'owner' | 'relation' | 'general' | 'source'>>)[]
  states: ObjectState[]
  attributes: ObjectAttribute[]
  // state is 0 when the link names none; versions from before boundary
  // places have no at, from before roles no role, from before failure
  // connections no kind, and from before queries no query.
  links: (Omit<Link, 'state' | 'states' | 'at' | 'role' | 'condition' | 'query'> & {
    state: number
    at?: Link['at']
    role?: LinkRole
    condition?: boolean
    query?: LinkQuery
  })[]
  linkStates: { link: number; state: number }[]
  connections: (Omit<Connection, 'kind'> & { kind?: Connection['kind'] })[]
  start: Start
  end?: End
  boards?: InnerBoard[]
  // Left out while a board has none, as in versions from before notes.
  notes?: Note[]
}

// The flow's board as its version has it, without changing the board.
export async function getVersionBoard(flow: number, id: number): Promise<BoardContents> {
  const s: Snapshot = await request(`/api/versions/${id}`)
  return {
    processes: s.processes.map((p) => ({
      ...p,
      flow,
      parent: p.parent ?? null,
      recording: p.recording ?? false,
      settings: p.settings ?? {},
    })),
    objects: s.objects.map((o) => ({
      ...o,
      flow,
      parent: o.parent ?? null,
      many: o.many ?? false,
      min: o.min ?? 0,
      max: o.max ?? null,
      human: o.human ?? false,
      end: o.end ?? false,
      owner: o.owner ?? null,
      relation: o.relation ?? '',
      general: o.general ?? null,
      source: o.source ?? null,
      sourceFlow: null,
      states: s.states.filter((st) => st.object === o.id),
      attributes: s.attributes.filter((a) => a.object === o.id),
    })),
    start: s.start,
    end: s.end ?? null,
    boards: s.boards ?? [],
    notes: s.notes ?? [],
    connections: s.connections.map((c) => ({ ...c, kind: c.kind ?? '' })),
    links: s.links.map((l) => ({
      ...l,
      role: l.role ?? '',
      condition: l.condition ?? false,
      query: l.query ?? null,
      state: l.state || null,
      at: l.at ?? null,
      states: s.linkStates.filter((ls) => ls.link === l.id).map((ls) => ls.state),
    })),
  }
}

// Someone who signs in, with the roles that say which human processes they
// carry out: those the human objects of the same names handle.
export type User = {
  id: number
  email: string
  name: string
  admin: boolean
  // Makes teams and says who is in them.
  teamManager: boolean
  roles: string[]
  teams: { id: number; name: string }[]
  // Null until they first sign in, when an admin added them.
  signedInAt: string | null
}

// Whether people sign in, and who is.
export type Me = { signIn: boolean; user: User | null }

export function getMe(): Promise<Me> {
  return request('/api/me')
}

// What the person signed in is told of, and how: by email and on Slack,
// when reviews wait on them, and whether of their runs failing; and whether
// the instance can email them, and tell them on Slack, at all.
export type NotificationSettings = {
  email: boolean
  slack: boolean
  failures: boolean
  canEmail: boolean
  canSlack: boolean
}

export function getNotificationSettings(): Promise<NotificationSettings> {
  return request('/api/me/notifications')
}

export function updateNotificationSettings(
  patch: Partial<Pick<NotificationSettings, 'email' | 'slack' | 'failures'>>,
): Promise<NotificationSettings> {
  return request('/api/me/notifications', { method: 'PATCH', body: JSON.stringify(patch) })
}

// Signing in again right away could find the provider still signed in, and
// come straight back; the page signing out ends on asks first.
export async function signOut(): Promise<void> {
  await send('/api/auth/logout', { method: 'POST' })
  window.location.href = '/api/auth/signed-out'
}

// Everyone who signs in or an admin added; for admins.
export function listUsers(): Promise<User[]> {
  return request('/api/users')
}

export function addUser(user: { email: string; name?: string; admin?: boolean; roles?: string[] }): Promise<User> {
  return request('/api/users', { method: 'POST', body: JSON.stringify(user) })
}

export function updateUser(id: number, patch: { admin?: boolean; teamManager?: boolean; roles?: string[] }): Promise<User> {
  return request(`/api/users/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

// Signs them out for good, until an admin adds them back; their flows go to
// the admin removing them.
export function removeUser(id: number): Promise<void> {
  return request(`/api/users/${id}`, { method: 'DELETE' })
}

// A group of people that owns flows together.
export type Team = {
  id: number
  name: string
  members: { id: number; email: string; name: string }[]
}

export function listTeams(): Promise<Team[]> {
  return request('/api/teams')
}

export function createTeam(team: { name: string; members?: number[] }): Promise<Team> {
  return request('/api/teams', { method: 'POST', body: JSON.stringify(team) })
}

export function updateTeam(id: number, patch: { name?: string; members?: number[] }): Promise<Team> {
  return request(`/api/teams/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

export function deleteTeam(id: number): Promise<void> {
  return request(`/api/teams/${id}`, { method: 'DELETE' })
}

// A variable the worker's models read their provider's key from, as the
// worker says: the providers reading it, whether the webapp's environment
// has it, and whether one is saved here, of which only the last four
// characters show. With no variable, the providers' keys can't be had,
// unless Bedrock's, which signs in with the worker's cloud credentials.
export type ModelKey = {
  env: string
  providers: string[]
  set: boolean
  saved: boolean
  hint?: string
  updatedAt?: string
}

// The keys, and why the worker didn't say which its models read, if it didn't.
export type ModelKeys = { keys: ModelKey[]; workerError: string }

// The keys admins save and everyone's agents use.
export function listModelKeys(): Promise<ModelKeys> {
  return request('/api/model-keys')
}

export function setModelKey(env: string, key: string): Promise<ModelKeys> {
  return request(`/api/model-keys/${env}`, { method: 'PUT', body: JSON.stringify({ key }) })
}

export function deleteModelKey(env: string): Promise<void> {
  return request(`/api/model-keys/${env}`, { method: 'DELETE' })
}

// Where a provider's key comes from: saved here, or the webapp's environment,
// where a vault puts it.
export type KeySource = 'saved' | 'env'

// A model provider admins added: one pi knows, or a custom one, such as an
// AI gateway, at its URL and speaking its API. It reads its key from env;
// a saved key shows only by its last four characters, and one in the
// webapp's environment by whether the webapp has it.
export type ModelProvider = {
  id: string
  name: string
  api?: string
  baseUrl?: string
  keySource: KeySource
  env: string
  models: string[]
  hint?: string
  envSet?: boolean
  updatedAt: string
}

// The providers, the default model kept, and what went wrong asking the
// worker, if anything.
export type ModelProviders = { providers: ModelProvider[]; default: string; workerError: string }

// A model of pi's catalog.
export type CatalogModel = {
  id: string
  name: string
  reasoning: boolean
  input?: string[]
  contextWindow: number
  maxTokens: number
}

// A provider pi knows, with its models and the variable it reads its key from.
export type CatalogProvider = { id: string; name: string; env: string; models: CatalogModel[] }

// A provider as the settings send it: a known one by its ID, a custom one by
// its name, API and URL. With a key saved already, no key keeps it.
export type ProviderInput = {
  id?: string
  name?: string
  api?: string
  baseUrl?: string
  keySource: KeySource
  env?: string
  key?: string
  models: string[]
}

// The APIs a custom provider may speak, as pi names them.
export const CUSTOM_APIS: { value: string; label: string }[] = [
  { value: 'openai-completions', label: 'OpenAI-compatible (Chat Completions)' },
  { value: 'openai-responses', label: 'OpenAI Responses' },
  { value: 'anthropic-messages', label: 'Anthropic-compatible (Messages)' },
  { value: 'bedrock-converse-stream', label: 'Amazon Bedrock (Converse)' },
]

export function listModelProviders(): Promise<ModelProviders> {
  return request('/api/model-providers')
}

export function providerCatalog(): Promise<{ providers: CatalogProvider[] }> {
  return request('/api/model-providers/catalog')
}

export function addModelProvider(input: ProviderInput): Promise<ModelProviders> {
  return request('/api/model-providers', { method: 'POST', body: JSON.stringify(input) })
}

export function updateModelProvider(id: string, input: ProviderInput): Promise<ModelProviders> {
  return request(`/api/model-providers/${id}`, { method: 'PUT', body: JSON.stringify(input) })
}

export function deleteModelProvider(id: string): Promise<ModelProviders> {
  return request(`/api/model-providers/${id}`, { method: 'DELETE' })
}

export function setDefaultModel(model: string): Promise<ModelProviders> {
  return request('/api/model-default', { method: 'PUT', body: JSON.stringify({ model }) })
}

// The models a custom provider says it serves.
export function listProviderModels(input: ProviderInput): Promise<{ models: string[] }> {
  return request('/api/model-providers/models', { method: 'POST', body: JSON.stringify(input) })
}

// The roles boards name and people hold, to pick from.
export function listRoles(): Promise<string[]> {
  return request('/api/roles')
}

// A key another system starts flows with, as its maker; the key itself only
// as it is made, never again.
export type APIKey = {
  id: number
  name: string
  // Its first characters, to tell it apart.
  prefix: string
  createdAt: string
  lastUsedAt: string | null
  key?: string
}

export function listAPIKeys(): Promise<APIKey[]> {
  return request('/api/api-keys')
}

export function createAPIKey(name: string): Promise<APIKey> {
  return request('/api/api-keys', { method: 'POST', body: JSON.stringify({ name }) })
}

export function deleteAPIKey(id: number): Promise<void> {
  return request(`/api/api-keys/${id}`, { method: 'DELETE' })
}

// How a flow is started over the API: where to POST, and the objects it
// starts from, each with a JSON Schema of what to give of it.
export type FlowAPI = {
  id: number
  name: string
  url: string
  inputs: { name: string; many: boolean; states: string[]; schema: JSONSchema }[]
  schema: JSONSchema
  // Why it can't be started this way, if it can't.
  problem?: string
}

export type JSONSchema = {
  type?: string
  description?: string
  properties?: Record<string, JSONSchema>
  items?: JSONSchema
  required?: string[]
}

export function getFlowAPI(flow: number): Promise<FlowAPI> {
  return request(`/api/flows/${flow}/api`)
}

// A webhook: another system's events starting the flow at a URL of its
// own, each delivery signed with its secret as its kind says, and taken
// once. GitHub's takes the events it names, alone (issues) or with an
// action (issues.labeled); Slack's the Events API's, those it names if
// any; a generic one every delivery, checking an HMAC or the secret itself
// in the header it names: the HMAC covers the timestamp timestampHeader
// holds and the id idHeader holds, when it names them, each followed by a
// dot, then the body, and deliveries are told apart by that id. Its runs
// are for whoever added it, or last changed what starts them.
export type WebhookKind = 'github' | 'slack' | 'generic'
export type WebhookVerify = 'hmac' | 'token'

// Where an object the flow starts from is taken from in a delivery's
// payload: a dotted path into it (issue.title, labels.0.name), the whole
// of it for an empty path, or for an object with attributes a path for each.
export type WebhookInput = { path: string; attributes?: Record<string, string> }

export type WebhookDelivery = {
  id: number
  receivedAt: string
  // Its id, as its sender gave it.
  delivery: string
  event: string
  outcome: 'started' | 'repeat' | 'ignored' | 'refused'
  reason: string
  run: number | null
}

export type Webhook = {
  id: number
  flow: number
  name: string
  kind: WebhookKind
  // Where its deliveries go.
  url: string
  verify?: WebhookVerify
  header?: string
  idHeader?: string
  timestampHeader?: string
  events: string[]
  mapping: Record<string, WebhookInput>
  enabled: boolean
  // Whom its runs are for, and whether that is whoever is signed in.
  createdBy: string
  yours: boolean
  createdAt: string
  // The secret's last four characters, when it is long enough.
  hint?: string
  // How many requests were refused as not signed with its secret, which
  // leave no delivery, and when the last was.
  refused: number
  lastRefusedAt: string | null
  // What keeps its deliveries from starting a run, as the flow stands.
  problems: string[]
  lastDelivery: WebhookDelivery | null
  // Only as it is made, or given a new one.
  secret?: string
}

// The webhook a run names, as its panel says it.
export const webhookLabel = (name: string) => (name ? `the webhook ${name}` : 'a webhook since removed')

// An object the flow starts from, which a mapping is written for.
export type WebhookFlowInput = { name: string; many: boolean; attributes: string[] }

export type WebhookList = {
  webhooks: Webhook[]
  inputs: WebhookFlowInput[]
  // Why the flow can't be given what it starts from, if it can't.
  problem: string
}

// What adding or changing a webhook sends. Without a secret, adding makes
// one and a change keeps the one kept, unless newSecret asks for another.
// Changing what starts its runs (all but its name), or turning it on, makes
// them the runs of whoever changes it.
export type WebhookFields = {
  flow: number
  name: string
  kind: WebhookKind
  verify: WebhookVerify | ''
  header: string
  idHeader: string
  timestampHeader: string
  events: string[]
  mapping: Record<string, WebhookInput>
  enabled: boolean
  secret: string
  newSecret?: boolean
}

export type WebhookDeliveries = {
  deliveries: WebhookDelivery[]
  // The latest signed delivery's payload, to write the mapping from.
  payload: { event: string; receivedAt: string; payload: string } | null
}

export function listWebhooks(flow: number): Promise<WebhookList> {
  return request(`/api/webhooks?flow=${flow}`)
}

export function createWebhook(fields: WebhookFields): Promise<Webhook> {
  return request('/api/webhooks', { method: 'POST', body: JSON.stringify(fields) })
}

export function updateWebhook(id: number, fields: WebhookFields): Promise<Webhook> {
  return request(`/api/webhooks/${id}`, { method: 'PUT', body: JSON.stringify(fields) })
}

export function deleteWebhook(id: number): Promise<void> {
  return request(`/api/webhooks/${id}`, { method: 'DELETE' })
}

export function getWebhookDeliveries(id: number): Promise<WebhookDeliveries> {
  return request(`/api/webhooks/${id}/deliveries`)
}

// A skill or a command, written in the settings for agents. A skill is a
// name, a description saying when it applies, and instructions an agent
// reads once its task calls for them; an agent process picks the ones it is
// given. A command is a prompt kept under a name, which /name in an agent's
// prompt stands for as the step runs, taking the rest of its line as
// arguments where it has placeholders ($1, $@, ${1:-default}). Each is owned
// as a flow is: its maker's until shared with a team or everyone.
export type SkillKind = 'skill' | 'command'

export type Skill = {
  id: number
  kind: SkillKind
  name: string
  description: string
  // What a command takes, such as [focus] or <ticket>.
  argumentHint: string
  content: string
  owner: FlowOwner
  updatedAt: string
}

export type SkillFields = {
  name: string
  description: string
  argumentHint: string
  content: string
  owner?: { kind: 'global' } | { kind: 'team' | 'user'; id: number }
}

const skillRoute = (kind: SkillKind) => (kind === 'skill' ? '/api/skills' : '/api/commands')

// The skills or commands whoever is signed in sees; given a flow, only the
// ones it may use, as its owner may.
export function listSkills(kind: SkillKind, flow?: number): Promise<Skill[]> {
  return request(flow === undefined ? skillRoute(kind) : `${skillRoute(kind)}?flow=${flow}`)
}

export function createSkill(kind: SkillKind, fields: SkillFields): Promise<Skill> {
  return request(skillRoute(kind), { method: 'POST', body: JSON.stringify(fields) })
}

export function updateSkill(kind: SkillKind, id: number, fields: SkillFields): Promise<Skill> {
  return request(`${skillRoute(kind)}/${id}`, { method: 'PUT', body: JSON.stringify(fields) })
}

export function deleteSkill(kind: SkillKind, id: number): Promise<void> {
  return request(`${skillRoute(kind)}/${id}`, { method: 'DELETE' })
}

// Gives a skill or command to someone, a team, or everyone.
export function setSkillOwner(
  kind: SkillKind,
  id: number,
  owner: { kind: 'global' } | { kind: 'team' | 'user'; id: number },
): Promise<Skill> {
  return request(`${skillRoute(kind)}/${id}/owner`, { method: 'PUT', body: JSON.stringify(owner) })
}

// A credential scripts and agents call a service with without seeing it:
// admins keep its secret, which is never sent back, and the step reaches
// the service at $CREDENTIAL_<variable>_URL, the webapp's proxy, which adds
// the secret as auth says: in a header its template makes, as basic auth's
// password, or as a query parameter. Everyone's flows may use it, or only
// those of the teams given it.
export type CredentialAuth = 'header' | 'basic' | 'query'

export type Credential = {
  id: number
  name: string
  variable: string
  description: string
  baseUrl: string
  auth: CredentialAuth
  header?: string
  template?: string
  username?: string
  param?: string
  // The headers steps may send the service besides the usual ones, such
  // as an API's version header.
  headers: string[]
  everyone: boolean
  teams: { id: number; name: string }[]
  // Whether its secret is kept, with its last four characters when long enough.
  secretSet: boolean
  hint?: string
  updatedAt: string
}

// A credential as a board offers it to a process.
export type CredentialChoice = Pick<Credential, 'id' | 'name' | 'variable' | 'description' | 'baseUrl'>

// What adding or changing a credential sends; a change without a secret
// keeps the one kept, unless it would send it elsewhere: to another
// origin, or in another header, parameter or way.
export type CredentialFields = {
  name: string
  description: string
  baseUrl: string
  auth: CredentialAuth
  header: string
  template: string
  username: string
  param: string
  headers: string[]
  secret: string
  everyone: boolean
  teams: number[]
}

// The variable a step reaches a credential at.
export const credentialEnv = (variable: string) => `CREDENTIAL_${variable}_URL`

// Every credential, for admins.
export function listCredentials(): Promise<Credential[]> {
  return request('/api/credentials')
}

// The credentials the flow may use, as its owner may.
export function listFlowCredentials(flow: number): Promise<CredentialChoice[]> {
  return request(`/api/credentials?flow=${flow}`)
}

export function createCredential(fields: CredentialFields): Promise<Credential[]> {
  return request('/api/credentials', { method: 'POST', body: JSON.stringify(fields) })
}

export function updateCredential(id: number, fields: CredentialFields): Promise<Credential[]> {
  return request(`/api/credentials/${id}`, { method: 'PUT', body: JSON.stringify(fields) })
}

export function deleteCredential(id: number): Promise<void> {
  return request(`/api/credentials/${id}`, { method: 'DELETE' })
}

// An internet host an admin allows steps in containers of their own to
// reach through the webapp's egress proxy: a name, *.name for its
// subdomains, with :port for a port other than 80 and 443.
export type EgressHost = { host: string; note: string; createdAt: string }

export function listEgressHosts(): Promise<EgressHost[]> {
  return request('/api/egress-hosts')
}

export function allowEgressHost(host: string, note: string): Promise<EgressHost[]> {
  return request('/api/egress-hosts', { method: 'POST', body: JSON.stringify({ host, note }) })
}

export function removeEgressHost(host: string): Promise<void> {
  return request(`/api/egress-hosts/${encodeURIComponent(host)}`, { method: 'DELETE' })
}

// What the instance lets runs and people use, 0 for no limit: what a run
// and a person in a month (UTC) may spend on models, in dollars (a run that
// spends its limit is stopped and fails; someone who spends theirs has their
// agents' calls refused until the next month), how many steps a person's
// runs may have going at once, and how much disk a run may take, in MB.
export type Limits = { runSpend: number; userMonthSpend: number; userParallelSteps: number; runDiskMb: number }

export function getLimits(): Promise<Limits> {
  return request('/api/limits')
}

export function saveLimits(l: Limits): Promise<Limits> {
  return request('/api/limits', { method: 'PUT', body: JSON.stringify(l) })
}

// What was spent on models in a month: by each person (unpriced counts the
// calls to models without a known price, counted at the largest models'),
// and on the costliest runs. Others' spending is for admins.
export type Spending = {
  month: string
  people: { user: number | null; name: string; spent: number; calls: number; unpriced: number }[]
  runs: { run: number; flow: number; name: string; spent: number }[]
}

export function getSpending(month?: string): Promise<Spending> {
  return request('/api/spending' + (month ? `?month=${encodeURIComponent(month)}` : ''))
}

// A run the Activity page lists, with its flow's name, and for a failed
// one, the process whose step failed first and what went wrong.
export type ActivityRun = FlowRun & { flowName: string; failedProcess?: string; error?: string }

// A flow's runs, each a day of the period: how many started that day, how
// many of those failed, and the dollars they spent on models, with how many
// of their calls were to models without a known price, counted at the
// largest models'.
export type FlowActivity = {
  flow: number
  name: string
  module: boolean
  runs: number[]
  failed: number[]
  cost: number[]
  unpriced: number
}

// A process's steps in the period, how many failed, a retry each counting,
// and the latest that did: its run, maybe one inside another flow's.
export type ProcessFailures = {
  flow: number
  flowName: string
  process: number
  name: string
  kind: ProcessKind
  steps: number
  failed: number
  lastRun: number
  lastError: string
}

// The reviews a human process asked for in the period that someone decided:
// how long they waited, in seconds, half of them and nine in ten within;
// how many came with instructions, asking for changes; and the reviews
// decided in one run, the rounds, over how many runs and at most.
export type ReviewTimes = {
  flow?: number
  flowName?: string
  process?: number
  name?: string
  decided: number
  // Those that waited past their time limit with nobody deciding, which
  // failed rather than being decided.
  timedOut: number
  median: number
  p90: number
  longest: number
  changes: number
  runs: number
  mostRounds: number
}

// What goes on across every flow one sees, over the last days, each a day
// in zone.
export type Activity = {
  zone: string
  days: string[]
  running: { total: number; runs: ActivityRun[] }
  failed: { total: number; runs: ActivityRun[] }
  total: FlowActivity
  flows: FlowActivity[]
  processes: ProcessFailures[]
  reviews: ReviewTimes[]
  allReviews: ReviewTimes
}

export function getActivity(days: number, zone: string): Promise<Activity> {
  return request(`/api/activity?${new URLSearchParams({ days: String(days), tz: zone })}`)
}
