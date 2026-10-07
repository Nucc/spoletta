// A human process is a review step: a person checks its inputs and approves
// them or requests changes, and the matching route says what runs next.
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
}

export type Start = {
  x: number
  y: number
  // Process the flow starts with, if connected.
  target: number | null
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
}

// 'output': the process generates the object. 'input': the process takes it as input.
export type LinkDirection = 'output' | 'input'

export type Link = {
  id: number
  process: number
  object: number
  direction: LinkDirection
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

export function createProcess(p: Omit<Process, 'id'>): Promise<Process> {
  return request('/api/processes', { method: 'POST', body: JSON.stringify(p) })
}

export function updateProcess(id: number, patch: Partial<Omit<Process, 'id'>>): Promise<Process> {
  return request(`/api/processes/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
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

export function createObject(o: Omit<FlowObject, 'id'>): Promise<FlowObject> {
  return request('/api/objects', { method: 'POST', body: JSON.stringify(o) })
}

export function updateObject(id: number, patch: Partial<Omit<FlowObject, 'id'>>): Promise<FlowObject> {
  return request(`/api/objects/${id}`, { method: 'PATCH', body: JSON.stringify(patch) })
}

export function listLinks(): Promise<Link[]> {
  return request('/api/links')
}

export function createLink(l: Omit<Link, 'id'>): Promise<Link> {
  return request('/api/links', { method: 'POST', body: JSON.stringify(l) })
}

export function deleteLink(id: number): Promise<void> {
  return request(`/api/links/${id}`, { method: 'DELETE' })
}

export type Outcome = 'approved' | 'changes'

// Which process runs after a human process decides on the given outcome.
export type Route = {
  id: number
  process: number
  outcome: Outcome
  target: number
}

export function listRoutes(): Promise<Route[]> {
  return request('/api/routes')
}

// Replaces the process's previous route for the same outcome.
export function setRoute(r: Omit<Route, 'id'>): Promise<Route> {
  return request('/api/routes', { method: 'PUT', body: JSON.stringify(r) })
}

export function deleteRoute(id: number): Promise<void> {
  return request(`/api/routes/${id}`, { method: 'DELETE' })
}

export type ThinkingLevel = 'off' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'

export const THINKING_LEVELS: ThinkingLevel[] = ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']

export type RunResult = {
  ok: boolean
  output: string
  error: string
}

export type Models = {
  // pi's default model as provider/id, empty if pi picks one itself.
  default: string
  // pi's enabledModels.
  models: string[]
}

export function listModels(): Promise<Models> {
  return request('/api/models')
}

// An object the process takes as input, with what the process that generated it printed.
export type RunInput = {
  name: string
  content: string
}

// Sent to the process a human routed back to: what they reviewed and what to change.
export type ChangeRequest = {
  reviewer: string
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

export type FlowRunStatus = 'running' | 'done' | 'failed' | 'interrupted'

// One execution of the flow from the start node.
export type FlowRun = {
  id: number
  status: FlowRunStatus
  startedAt: string
  finishedAt: string | null
}

export type StepStatus = 'active' | 'waiting' | 'done' | 'error'

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
  // For human steps.
  outcome: Outcome | ''
  instructions: string
  startedAt: string
  finishedAt: string | null
}

// The content an object got from the step that generated it.
export type RunObject = {
  id: number
  step: number
  // Null once the object is deleted.
  object: number | null
  name: string
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
export function endRun(id: number, status: Exclude<FlowRunStatus, 'running'>, keepalive = false): Promise<FlowRun> {
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
  result: { status: 'done' | 'error'; output?: string; error?: string; outcome?: Outcome; instructions?: string },
): Promise<void> {
  return request(`/api/runs/${run}/steps/${id}`, { method: 'PATCH', body: JSON.stringify(result) })
}
