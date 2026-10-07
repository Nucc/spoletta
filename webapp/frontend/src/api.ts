export type ProcessKind = 'agent' | 'script'

export type Process = {
  id: number
  name: string
  kind: ProcessKind
  // Used when kind is 'agent'.
  prompt: string
  // Used when kind is 'script'.
  script: string
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

export type ThinkingLevel = 'off' | 'minimal' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'

export type AgentSettings = {
  provider: string
  model: string
  thinking: ThinkingLevel
  // The key itself is never sent back, only whether one is stored and its last characters.
  hasApiKey: boolean
  keyHint: string
}

export type AgentSettingsPatch = Partial<Pick<AgentSettings, 'provider' | 'model' | 'thinking'>> & {
  // Omitted or empty keeps the stored key.
  apiKey?: string
  clearApiKey?: boolean
}

export type RunResult = {
  ok: boolean
  output: string
  error: string
}

export function getAgentSettings(): Promise<AgentSettings> {
  return request('/api/agent-settings')
}

export function updateAgentSettings(patch: AgentSettingsPatch): Promise<AgentSettings> {
  return request('/api/agent-settings', { method: 'PATCH', body: JSON.stringify(patch) })
}

export function runProcess(id: number, signal?: AbortSignal): Promise<RunResult> {
  return request(`/api/processes/${id}/run`, { method: 'POST', signal })
}
