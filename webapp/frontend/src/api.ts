// A human process is a review step: a person checks its inputs and decides
// by setting an object to one of the states the process's output links name.
export type ProcessKind = 'agent' | 'script' | 'human'

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

export function createProcess(p: Omit<Process, 'id' | 'width'> & { width?: number | null }): Promise<Process> {
  return request('/api/processes', { method: 'POST', body: JSON.stringify(p) })
}

export function updateProcess(id: number, patch: Partial<Omit<Process, 'id'>>): Promise<Process> {
  return request(`/api/processes/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

export function deleteProcess(id: number): Promise<void> {
  return request(`/api/processes/${id}`, { method: 'DELETE' })
}

export function getStart(): Promise<Start> {
  return request('/api/start')
}

export function updateStart(patch: Partial<Start>): Promise<Start> {
  return request('/api/start', { method: 'PATCH', body: JSON.stringify(patch) })
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
  o: Omit<FlowObject, 'id' | 'states' | 'attributes' | 'width' | 'persistent'> & { width?: number | null },
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
  l: Omit<Link, 'id' | 'state' | 'states' | 'route'> & { state?: number | null; states?: number[] },
): Promise<Link> {
  return request('/api/links', { method: 'POST', body: JSON.stringify(l) })
}

// Sets where a link's segments run; null goes back to the default route.
export function setLinkRoute(id: number, route: number[] | null): Promise<void> {
  return request(`/api/links/${id}`, { method: 'PATCH', body: JSON.stringify({ route }) })
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

export function updateAttribute(id: number, patch: Partial<Pick<ObjectAttribute, 'name' | 'definition' | 'unique'>>): Promise<ObjectAttribute> {
  return request(`/api/attributes/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

export function deleteAttribute(id: number): Promise<void> {
  return request(`/api/attributes/${id}`, { method: 'DELETE' })
}

export type ThinkingLevel = 'off' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'

export const THINKING_LEVELS: ThinkingLevel[] = ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']

export type RunResult = {
  ok: boolean
  // When the process generates objects with attributes, the reply as checked
  // JSON; each such object holds its part, given in objects by object id.
  output: string
  error: string
  objects?: Record<string, string>
  // Set when what the step generated holds a unique value another run
  // stored; the server terminated the run.
  duplicate?: Duplicate
}

// A unique value a step generated that another run already stored.
export type Duplicate = {
  object: string
  attribute: string
  value: string
  // The run that stored it.
  run: number
}

export const duplicateText = (d: Duplicate) => `${d.object} with ${d.attribute} ${d.value} was already stored by run #${d.run}.`

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

// Runs an agent process; with a flow run id, the server records it as a step of that run.
export function runProcess(
  id: number,
  inputs: RunInput[],
  changeRequest?: ChangeRequest,
  run?: number,
  signal?: AbortSignal,
): Promise<RunResult> {
  return request(`/api/processes/${id}/run`, {
    method: 'POST',
    body: JSON.stringify({ run, inputs, changeRequest }),
    signal,
  })
}

// The server terminates a run whose step generated a duplicate.
export type FlowRunStatus = 'running' | 'done' | 'failed' | 'interrupted' | 'terminated'

// One execution of the flow from the start node.
export type FlowRun = {
  id: number
  status: FlowRunStatus
  startedAt: string
  finishedAt: string | null
}

export type StepStatus = 'active' | 'waiting' | 'done' | 'error' | 'duplicate'

// One execution of a process in a run, with the process as it was then.
export type RunStep = {
  id: number
  // Null once the process is deleted.
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
  startedAt: string
  finishedAt: string | null
}

// The content and state an object got from the step that generated it, or
// the state a human step set it to.
export type RunObject = {
  id: number
  step: number
  // Null once the object is deleted.
  object: number | null
  name: string
  state: string
  content: string
}

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

// keepalive lets the request finish while the page unloads.
export function endRun(id: number, status: Exclude<FlowRunStatus, 'running' | 'terminated'>, keepalive = false): Promise<FlowRun> {
  return request(`/api/runs/${id}`, { method: 'PATCH', body: JSON.stringify({ status }), keepalive })
}

// Records a script or human step; agent steps are recorded by runProcess.
export function startStep(
  run: number,
  step: { process: number; status: 'active' | 'waiting'; inputs: RunInput[] },
): Promise<{ id: number }> {
  return request(`/api/runs/${run}/steps`, { method: 'POST', body: JSON.stringify(step) })
}

export function finishStep(
  run: number,
  id: number,
  result: { status: 'done' | 'error'; output?: string; error?: string; state?: number; instructions?: string },
): Promise<{ duplicate: Duplicate } | undefined> {
  return request(`/api/runs/${run}/steps/${id}`, { method: 'PATCH', body: JSON.stringify(result) })
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
