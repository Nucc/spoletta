export type Process = {
  id: number
  name: string
  prompt: string
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
