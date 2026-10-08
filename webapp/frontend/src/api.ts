// A human process is a review step: a person checks its inputs and decides
// by setting an object to one of the states the process's output links name.
// A flow process is made of processes, on a board of its own. A Sentry
// process fetches issues from Sentry.
export type ProcessKind = 'agent' | 'script' | 'human' | 'flow' | 'sentry'

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
export type ProcessSettings = { sentry?: SentrySettings }

export type Process = {
  id: number
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

export type Connection = {
  id: number
  source: number
  target: number
}

// An OPM object: something a process generates and other processes take as input.
export type FlowObject = {
  id: number
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
  // The flow process whose board it is on; null on the top board. It also
  // shows inside the flow processes linked to it.
  parent: number | null
  states: ObjectState[]
  attributes: ObjectAttribute[]
}

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
  // Where the link's segments run, if moved by hand; see route.ts.
  route: number[] | null
  // Where the object shows on the process's own board, if moved there by
  // hand; only a flow process has one.
  at: { x: number; y: number } | null
}

async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...init?.headers },
  })
  if (!res.ok) {
    throw new Error(`${init?.method ?? 'GET'} ${url}: ${res.status} ${await res.text()}`)
  }
  if (res.status === 204) return undefined as T
  return res.json() as Promise<T>
}

export function listProcesses(): Promise<Process[]> {
  return request('/api/processes')
}

export function createProcess(
  p: Omit<Process, 'id' | 'width' | 'recording' | 'settings'> & {
    width?: number | null
    recording?: boolean
    settings?: ProcessSettings
  },
): Promise<Process> {
  return request('/api/processes', { method: 'POST', body: JSON.stringify(p) })
}

export function updateProcess(id: number, patch: Partial<Omit<Process, 'id'>>): Promise<Process> {
  return request(`/api/processes/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

export function deleteProcess(id: number): Promise<void> {
  return request(`/api/processes/${id}`, { method: 'DELETE' })
}

// A board is the top board, null, or a flow process's, by its id.
export type BoardId = number | null

const boardQuery = (board: BoardId) => (board === null ? '' : `?board=${board}`)

export function getStart(): Promise<Start> {
  return request('/api/start')
}

export function updateStart(board: BoardId, patch: Partial<Start>): Promise<Start> {
  return request(`/api/start${boardQuery(board)}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

// Where a flow process's board ends; the objects it generates show there.
export type End = { x: number; y: number }

// A flow process's board: where it starts and ends.
export type InnerBoard = { process: number; start: Start; end: End }

export function listBoards(): Promise<InnerBoard[]> {
  return request('/api/boards')
}

export function updateEnd(board: number, patch: Partial<End>): Promise<End> {
  return request(`/api/end${boardQuery(board)}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

// Moves processes, and the selected objects only they link to, into a new
// flow process in their place.
export function wrapProcesses(w: { name: string; processes: number[]; objects: number[] }): Promise<Process> {
  return request('/api/processes/wrap', { method: 'POST', body: JSON.stringify(w) })
}

export function listConnections(): Promise<Connection[]> {
  return request('/api/connections')
}

export function createConnection(c: Omit<Connection, 'id'>): Promise<Connection> {
  return request('/api/connections', { method: 'POST', body: JSON.stringify(c) })
}

export function deleteConnection(id: number): Promise<void> {
  return request(`/api/connections/${id}`, { method: 'DELETE' })
}

export function listObjects(): Promise<FlowObject[]> {
  return request('/api/objects')
}

export function createObject(
  o: Omit<FlowObject, 'id' | 'states' | 'attributes' | 'width' | 'persistent' | 'many' | 'parent'> & {
    width?: number | null
    parent?: number | null
  },
): Promise<FlowObject> {
  return request('/api/objects', { method: 'POST', body: JSON.stringify(o) })
}

export function updateObject(id: number, patch: Partial<Omit<FlowObject, 'id' | 'states' | 'attributes'>>): Promise<FlowObject> {
  return request(`/api/objects/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

export function deleteObject(id: number): Promise<void> {
  return request(`/api/objects/${id}`, { method: 'DELETE' })
}

export function listLinks(): Promise<Link[]> {
  return request('/api/links')
}

// Without a state, the link names none; without states, it lists none.
export function createLink(
  l: Omit<Link, 'id' | 'state' | 'states' | 'route' | 'at'> & { state?: number | null; states?: number[] },
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
  status: FlowRunStatus
  // The version of the board it ran on; starting a run saves a board changed
  // since its version as a new one. Null for runs from before versions were kept.
  version: number | null
  // For the run of a recording flow process: the run it ran in, and the
  // process. Null for runs of the whole flow.
  parent: number | null
  process: number | null
  processName: string
  // For the run of one item of a list: what tells the item apart.
  itemKey: string
  startedAt: string
  finishedAt: string | null
}

export type StepStatus = 'active' | 'waiting' | 'done' | 'error' | 'duplicate'

// One execution of a process in a run, with the process as it was then.
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
  // For a recording flow process: the run its board ran as.
  childRun: number | null
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

export function listRuns(): Promise<FlowRun[]> {
  return request('/api/runs')
}

export function startRun(): Promise<FlowRun> {
  return request('/api/runs', { method: 'POST' })
}

export function getRun(id: number): Promise<FlowRunDetail> {
  return request(`/api/runs/${id}`)
}

// Ends a run where it is; steps still going are cancelled.
export function stopRun(id: number): Promise<FlowRun> {
  return request(`/api/runs/${id}`, { method: 'PATCH', body: JSON.stringify({ status: 'interrupted' }) })
}

// Decides a human step waiting for its review, and lets the run go on: the
// state the human set, if they had any to choose from, and their instructions.
export function decideStep(run: number, id: number, decision: { state: number | null; instructions: string }): Promise<void> {
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

export function listVersions(): Promise<Versions> {
  return request('/api/versions')
}

export function saveVersion(): Promise<Version> {
  return request('/api/versions', { method: 'POST' })
}

// Rewrites the board as the version has it.
export function restoreVersion(id: number): Promise<void> {
  return request(`/api/versions/${id}/restore`, { method: 'POST' })
}

// The whole flow, every board of it, as the board's endpoints give it or a
// version saved it.
export type BoardContents = {
  processes: Process[]
  objects: FlowObject[]
  // The top board's start.
  start: Start
  connections: Connection[]
  links: Link[]
  boards: InnerBoard[]
}

// Versions from before boards have no parents, recording or boards, and
// processes from before settings none.
type Snapshot = {
  processes: (Omit<Process, 'parent' | 'recording' | 'settings'> & Partial<Pick<Process, 'parent' | 'recording' | 'settings'>>)[]
  objects: (Omit<FlowObject, 'states' | 'attributes' | 'parent' | 'many'> & Partial<Pick<FlowObject, 'parent' | 'many'>>)[]
  states: ObjectState[]
  attributes: ObjectAttribute[]
  // state is 0 when the link names none; versions from before boundary
  // places have no at.
  links: (Omit<Link, 'state' | 'states' | 'at'> & { state: number; at?: Link['at'] })[]
  linkStates: { link: number; state: number }[]
  connections: Connection[]
  start: Start
  boards?: InnerBoard[]
}

// The board as the version has it, without changing the board.
export async function getVersionBoard(id: number): Promise<BoardContents> {
  const s: Snapshot = await request(`/api/versions/${id}`)
  return {
    processes: s.processes.map((p) => ({
      ...p,
      parent: p.parent ?? null,
      recording: p.recording ?? false,
      settings: p.settings ?? {},
    })),
    objects: s.objects.map((o) => ({
      ...o,
      parent: o.parent ?? null,
      many: o.many ?? false,
      states: s.states.filter((st) => st.object === o.id),
      attributes: s.attributes.filter((a) => a.object === o.id),
    })),
    start: s.start,
    boards: s.boards ?? [],
    connections: s.connections,
    links: s.links.map((l) => ({
      ...l,
      state: l.state || null,
      at: l.at ?? null,
      states: s.linkStates.filter((ls) => ls.link === l.id).map((ls) => ls.state),
    })),
  }
}
