// A human process is a step a person carries out: they check its inputs and
// decide by setting an object to one of the states the process's output links
// name, and fill in the objects its output links list no states for.
// A flow process is made of processes, on a board of its own. A subflow
// process runs another flow, so flows can reuse each other: the objects it
// takes are given to the objects of the same names on that flow's top board,
// and it generates what that flow's run leaves of the objects named like the
// ones it generates. A Sentry process fetches issues from Sentry.
export type ProcessKind = 'agent' | 'script' | 'human' | 'flow' | 'subflow' | 'sentry'

// Which of Sentry's issues a Sentry process fetches.
export type SentrySettings = {
  organization: string
  // Slug or id; empty fetches from every project the token can see.
  project: string
  environment: string
  // Sentry's search syntax.
  query: string
  sort: string
  limit: number
}

export const DEFAULT_SENTRY: SentrySettings = {
  organization: '',
  project: '',
  environment: '',
  query: 'is:unresolved',
  sort: 'date',
  limit: 10,
}

// Settings of the kinds that take more than a prompt or a script, by kind.
export type ProcessSettings = {
  sentry?: SentrySettings
  // The MCP servers an agent process may use, by id; none when absent.
  mcpServers?: number[]
  // The flow's repositories an agent or script process works on, by name;
  // all of them when absent, none when empty.
  repositories?: string[]
  // The flow a subflow process runs, by id.
  flow?: number
  // How many times an agent deciding states runs before its decisions can
  // no longer send the object back to a process that ran; DEFAULT_ROUNDS
  // when absent.
  rounds?: number
  // How long an agent, script or Sentry step may take, in seconds; the
  // worker's own limit when absent.
  timeout?: number
  // How many more times a failed step is tried; none when absent.
  retries?: number
  // How many of the objects it takes without waiting for a state a process
  // needs before it runs; all of them when absent.
  quorum?: number
  // Whether an agent's prompt describes the flow it works in, in OPL.
  describeFlow?: boolean
}

export const DEFAULT_ROUNDS = 3
export const MAX_ROUNDS = 20
// The longest timeout, a day in seconds, and the most retries.
export const MAX_TIMEOUT = 24 * 60 * 60
export const MAX_RETRIES = 5

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
  // Where the link's segments run, if moved by hand; see route.ts.
  route: number[] | null
  // Where the object shows on the process's own board, if moved there by
  // hand; only a flow process has one.
  at: { x: number; y: number } | null
}

// Requests changing what isn't a board, which leave boards' histories be.
const NOT_BOARDS = ['/api/history', '/api/runs', '/api/schedules', '/api/mcp-servers', '/api/users']

// What is told of each change the server took that may change a board.
const changeListeners = new Set<() => void>()
// The changes sent and not answered yet.
const changing = new Set<Promise<unknown>>()

// Calls back after each change the server took that may change a board.
export function onChanged(callback: () => void): () => void {
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
    changeListeners.forEach((l) => l())
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
  // Its latest run of the whole flow, if any.
  lastRun: FlowRun | null
}

// A repository the flow's agents and scripts work on, checked out in their
// run's workspace in a directory named after it.
export type Repository = {
  name: string
  // https only.
  url: string
  // The branch runs' branches start from; empty for the default branch.
  base: string
}

export type FlowSettings = {
  repositories: Repository[]
  // Runs' branches are named <prefix>/run-<id>; empty uses flow/<the flow's name>.
  branchPrefix: string
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
export function createFlow(name: string, kind: 'flow' | 'module' | 'struct' = 'flow'): Promise<Flow> {
  return request('/api/flows', {
    method: 'POST',
    body: JSON.stringify({ name, module: kind === 'module', struct: kind === 'struct' }),
  })
}

// Puts a copy of a struct's object on a board of a flow, which follows it.
export function copyStruct(
  struct: number,
  at: { flow: number; parent: number | null; x: number; y: number },
): Promise<FlowObject> {
  return request(`/api/structs/${struct}/copies`, { method: 'POST', body: JSON.stringify(at) })
}

// A module other flows run stays one.
export function updateFlow(id: number, patch: Partial<Pick<Flow, 'name' | 'module' | 'settings'>>): Promise<Flow> {
  return request(`/api/flows/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

// Everything in it goes with it, its runs too.
export function deleteFlow(id: number): Promise<void> {
  return request(`/api/flows/${id}`, { method: 'DELETE' })
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
export type Copied = { flow: number; processes: number[]; objects: number[]; at: { x: number; y: number } }

// Copies what was copied onto a board of a flow, moved by dx and dy, with
// what it takes along: a flow process's board, what makes an object up, and
// the links between them. Resolves to the copies of what was selected.
export function pasteCopied(
  c: Copied,
  flow: number,
  board: BoardId,
  dx: number,
  dy: number,
): Promise<{ processes: number[]; objects: number[] }> {
  return request('/api/paste', {
    method: 'POST',
    body: JSON.stringify({ from: c.flow, processes: c.processes, objects: c.objects, flow, board, dx, dy }),
  })
}

// The flow's board in OPL, OPM's Object-Process Language, as it is now.
export async function getFlowOPL(flow: number): Promise<string> {
  const { text } = await request<{ text: string }>(`/api/flows/${flow}/opl`)
  return text
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

// Without a state, the link names none; without states, it lists none.
export function createLink(
  l: Omit<Link, 'id' | 'state' | 'states' | 'route' | 'at' | 'role' | 'condition'> & {
    state?: number | null
    states?: number[]
    role?: LinkRole
    condition?: boolean
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
  // pi's default model as provider/id, empty if pi picks one itself.
  default: string
  // pi's enabledModels.
  models: string[]
}

export function listModels(): Promise<Models> {
  return request('/api/models')
}

// An MCP server agents can use once it is connected, which signs in through
// the browser; each agent process picks the ones it uses.
export type MCPServer = {
  id: number
  name: string
  url: string
  description: string
  connected: boolean
}

export function listMCPServers(): Promise<MCPServer[]> {
  return request('/api/mcp-servers')
}

export function createMCPServer(s: Pick<MCPServer, 'name' | 'url' | 'description'>): Promise<MCPServer> {
  return request('/api/mcp-servers', { method: 'POST', body: JSON.stringify(s) })
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
  // The version of the board it ran on; starting a run saves a board changed
  // since its version as a new one. Null for runs from before versions were kept.
  version: number | null
  // For the run of a recording flow process: the run it ran in, and the
  // process. Null for runs of the whole flow. The run of a flow a subflow
  // process ran has a parent in that process's flow, and no process.
  parent: number | null
  process: number | null
  processName: string
  // For the run of one item of a list: what tells the item apart.
  itemKey: string
  // The schedule that started it, if one did.
  schedule: number | null
  // Who started it, when people sign in; empty otherwise.
  startedBy: string
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
  kind: ProcessKind
  model: string
  thinking: ThinkingLevel | ''
  // For agents, the prompt as sent, inputs and change request included.
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
  // person signed in may.
  decidedBy: string
  handledBy?: string[]
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

export type FlowRunDetail = FlowRun & { steps: RunStep[]; objects: RunObject[] }

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

export function listRuns(flow: number): Promise<FlowRun[]> {
  return request(`/api/runs?flow=${flow}`)
}

export function startRun(flow: number): Promise<FlowRun> {
  return request(`/api/runs?flow=${flow}`, { method: 'POST' })
}

export function getRun(id: number): Promise<FlowRunDetail> {
  return request(`/api/runs/${id}`)
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
  // Tool results.
  toolName?: string
  isError?: boolean
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

// Ends a run where it is; steps still going are cancelled.
export function stopRun(id: number): Promise<FlowRun> {
  return request(`/api/runs/${id}`, { method: 'PATCH', body: JSON.stringify({ status: 'interrupted' }) })
}

// Stops an agent, script or Sentry step still going. It fails, which ends its
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
  // Whether the reply changed the flow; the board before is saved as a version.
  applied: boolean
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

export function decideStep(
  run: number,
  id: number,
  decision: { state: number | null; instructions: string; entries?: Entries },
): Promise<void> {
  return request(`/api/runs/${run}/steps/${id}`, { method: 'PATCH', body: JSON.stringify(decision) })
}

// A saved snapshot of the whole board. Saving always adds a version on top;
// jumping to an older one keeps every other version.
export type Version = {
  id: number
  // The version the board was on when this one was saved; older than the
  // previous version when saved after jumping back.
  basedOn: number | null
  createdAt: string
}

export type Versions = {
  // The version the board was last saved as or jumped to; null before the first save.
  current: number | null
  // Newest first.
  versions: Version[]
}

export function listVersions(flow: number): Promise<Versions> {
  return request(`/api/versions?flow=${flow}`)
}

export function saveVersion(flow: number): Promise<Version> {
  return request(`/api/versions?flow=${flow}`, { method: 'POST' })
}

// Rewrites the board as the version has it.
export function restoreVersion(id: number): Promise<void> {
  return request(`/api/versions/${id}/restore`, { method: 'POST' })
}

// Whether a flow's board has a change to undo, and one to redo, and whether
// the board changed just now.
export type History = { undo: boolean; redo: boolean; changed: boolean }

// Keeps the board as it is as a step of its history, unless it is the step
// the board is on.
export function recordHistory(flow: number): Promise<History> {
  return request(`/api/history?flow=${flow}`, { method: 'POST' })
}

// Brings the board back a step, or forward again to the one undone last.
export function undoHistory(flow: number): Promise<History> {
  return request(`/api/history/undo?flow=${flow}`, { method: 'POST' })
}

export function redoHistory(flow: number): Promise<History> {
  return request(`/api/history/redo?flow=${flow}`, { method: 'POST' })
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
}

// Versions from before boards have no parents, recording or boards,
// processes from before settings none, objects that aren't people no human,
// and lists holding any number of items no min or max.
type Snapshot = {
  processes: (Omit<Process, 'flow' | 'parent' | 'recording' | 'settings'> & Partial<Pick<Process, 'parent' | 'recording' | 'settings'>>)[]
  objects: (Omit<FlowObject, 'flow' | 'states' | 'attributes' | 'parent' | 'many' | 'min' | 'max' | 'human' | 'end' | 'owner' | 'relation' | 'general'> &
    Partial<Pick<FlowObject, 'parent' | 'many' | 'min' | 'max' | 'human' | 'end' | 'owner' | 'relation' | 'general'>>)[]
  states: ObjectState[]
  attributes: ObjectAttribute[]
  // state is 0 when the link names none; versions from before boundary
  // places have no at, from before roles no role, and from before failure
  // connections no kind.
  links: (Omit<Link, 'state' | 'states' | 'at' | 'role' | 'condition'> & { state: number; at?: Link['at']; role?: LinkRole; condition?: boolean })[]
  linkStates: { link: number; state: number }[]
  connections: (Omit<Connection, 'kind'> & { kind?: Connection['kind'] })[]
  start: Start
  end?: End
  boards?: InnerBoard[]
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
      states: s.states.filter((st) => st.object === o.id),
      attributes: s.attributes.filter((a) => a.object === o.id),
    })),
    start: s.start,
    end: s.end ?? null,
    boards: s.boards ?? [],
    connections: s.connections.map((c) => ({ ...c, kind: c.kind ?? '' })),
    links: s.links.map((l) => ({
      ...l,
      role: l.role ?? '',
      condition: l.condition ?? false,
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
  roles: string[]
  // Null until they first sign in, when an admin added them.
  signedInAt: string | null
}

// Whether people sign in, and who is.
export type Me = { signIn: boolean; user: User | null }

export function getMe(): Promise<Me> {
  return request('/api/me')
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

export function updateUser(id: number, patch: { admin?: boolean; roles?: string[] }): Promise<User> {
  return request(`/api/users/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

// The roles boards name and people hold, to pick from.
export function listRoles(): Promise<string[]> {
  return request('/api/roles')
}
