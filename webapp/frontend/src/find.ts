import type { FlowObject, InnerBoard, Process } from './api'

// Finding on a board (Cmd-F): the processes, objects and states whose names
// hold what is typed, on the board shown and the boards inside it first,
// then on the rest of the flow, each board's from the top down, so going to
// the next match walks down the board and into the flow processes on it.
// An object making up another is on that one's board unfolded; a state is
// found on its object.

// The board a node is on: a flow process's, or the top one with null, and
// the object unfolded, if it is on one's board.
export type Place = { board: number | null; unfolded: number | null }

export type FindMatch = Place & {
  kind: 'process' | 'object' | 'state'
  // The React Flow id of the node to go to: a state's is its object's.
  node: string
  name: string
  // A state's object.
  of?: string
}

type Contents = { processes: Process[]; objects: FlowObject[]; boards: InnerBoard[] }

const top = (a: { x: number; y: number }, b: { x: number; y: number }) => a.y - b.y || a.x - b.x

// The matches in the order Enter goes to them. On a struct's board, its
// object is unfolded, so what is on no board of its own is on its object's.
export function findOnBoards(c: Contents, query: string, from: Place, structRoot: number | null = null): FindMatch[] {
  const q = query.trim().toLowerCase()
  if (!q) return []
  const holds = (name: string) => name.toLowerCase().includes(q)
  const inner = new Set(c.boards.map((b) => b.process))
  const matches: FindMatch[] = []

  // An object and what makes it up, unfolded on its board, depth first.
  const object = (o: FlowObject, place: Place, seen: Set<number>) => {
    if (seen.has(o.id)) return
    seen.add(o.id)
    const node = `obj-${o.id}`
    if (holds(o.name)) matches.push({ ...place, kind: 'object', node, name: o.name })
    for (const s of o.states) if (holds(s.name)) matches.push({ ...place, kind: 'state', node, name: s.name, of: o.name })
    const parts = c.objects.filter((p) => p.owner === o.id).sort(top)
    for (const p of parts) object(p, { board: place.board, unfolded: o.id }, seen)
  }

  const seenBoards = new Set<number | null>()
  const seenObjects = new Set<number>()
  const board = (b: number | null) => {
    if (seenBoards.has(b)) return
    seenBoards.add(b)
    const processes = c.processes.filter((p) => p.parent === b)
    const objects = c.objects.filter((o) => o.parent === b && o.owner === null)
    const here = [...processes.map((p) => ({ p, at: p })), ...objects.map((o) => ({ o, at: o }))].sort((x, y) => top(x.at, y.at))
    for (const h of here) {
      if ('p' in h) {
        if (holds(h.p.name)) matches.push({ board: b, unfolded: null, kind: 'process', node: String(h.p.id), name: h.p.name })
      } else {
        object(h.o, { board: b, unfolded: structRoot === h.o.id ? h.o.id : null }, seenObjects)
      }
    }
    for (const p of [...processes].sort(top)) if (inner.has(p.id)) board(p.id)
  }

  if (from.board === null || inner.has(from.board)) board(from.board)
  board(null)
  // Boards a broken parent leaves out of the walk still count.
  for (const b of c.boards) board(b.process)
  return matches
}

// Where a match is, as the find box says it: the board, or the object it
// makes up, named.
export function placeOf(m: FindMatch, c: Contents): string {
  if (m.unfolded !== null && m.node !== `obj-${m.unfolded}`) {
    return `in ${c.objects.find((o) => o.id === m.unfolded)?.name ?? 'an object'}`
  }
  if (m.board === null) return 'on the top board'
  return `on ${c.processes.find((p) => p.id === m.board)?.name ?? 'a flow process'}'s board`
}
