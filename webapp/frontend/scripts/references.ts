// Which objects on a board a line can join as a reference (src/references.ts):
// a copy of a shared struct standing for the record another object's
// attribute names, either way the line was dragged, and nothing else.
// Run with `npm test`, which needs Node 22.6 or later to load TypeScript.

import { Position } from '@xyflow/react'
import type { FlowObject, ObjectAttribute } from '../src/api.ts'
import { drawnReference, followsReferences, holdsReferences, referenceEnds, referenceHandle, referencesTo } from '../src/references.ts'

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

function attr(object: number, name: string, more: Partial<ObjectAttribute> = {}): ObjectAttribute {
  return { id: object * 10 + name.length, object, name, definition: '', unique: false, file: false, ...more }
}

function object(id: number, name: string, attributes: ObjectAttribute[], more: Partial<FlowObject> = {}): FlowObject {
  return {
    id, flow: 1, name, x: 0, y: 0, width: null, persistent: false, shared: false, pick: null, follows: null, many: false,
    min: 0, max: null, human: false, end: false, parent: null, owner: null, relation: '', general: null, source: null,
    sourceFlow: null, states: [], attributes, ...more,
  }
}

// The Ticket struct's object is 100, the Pull Request struct's 200; a pull
// request's ticket refers to tickets, and so does its reviewer's team key,
// for a second reference to the same struct.
const TICKET = 100
const pr = object(1, 'Pull Request', [attr(1, 'url', { unique: true }), attr(1, 'ticket', { ref: TICKET })], {
  shared: true, source: 200, pick: { states: ['open'], oldest: true },
})
const ticket = object(2, 'Ticket', [attr(2, 'key', { unique: true })], { shared: true, source: TICKET })
const newTicket = object(3, 'New Ticket', [attr(3, 'key', { unique: true })], { shared: true, source: TICKET })

check('a picked pull request holds references', holdsReferences(pr), true)
check('a ticket copy follows them', followsReferences(ticket), true)
check('a pull request names its ticket by ticket', referencesTo(pr, ticket), ['ticket'])
check('a ticket names no pull request', referencesTo(ticket, pr), [])
check('dragged from the pull request onto the ticket', drawnReference(pr, ticket), { holder: 1, followed: 2, attributes: ['ticket'] })
check('dragged the other way round, the same', drawnReference(ticket, pr), { holder: 1, followed: 2, attributes: ['ticket'] })
check('two tickets make no reference', drawnReference(ticket, newTicket), null)
check('an object refers to nothing of its own', referencesTo(pr, pr), [])

// A pull request neither picked nor given holds none another can follow.
const madePR = { ...pr, pick: null }
check('a pull request a process makes holds none', drawnReference(madePR, ticket), null)

// What a run is given, a plain object, holds one; a list or a person doesn't.
const entered = object(4, 'Entered ticket', [attr(4, 'key', { ref: TICKET })])
check('a given key names a ticket', drawnReference(entered, ticket), { holder: 4, followed: 2, attributes: ['key'] })
check('a list holds none', drawnReference({ ...entered, many: true }, ticket), null)
check('a person holds none', drawnReference({ ...entered, human: true }, ticket), null)

// Only a copy of the struct referred to, one record, on the top board, not
// picking its own, follows.
const other = object(5, 'Epic', [attr(5, 'key', { unique: true })], { shared: true, source: 300 })
check('another struct follows no ticket reference', drawnReference(pr, other), null)
check('a ticket picking its own follows none', drawnReference(pr, { ...ticket, pick: { oldest: true } }), null)
check('a list of tickets follows none', drawnReference(pr, { ...ticket, many: true }), null)
check('a ticket inside a flow process follows none', drawnReference(pr, { ...ticket, parent: 9 }), null)
check('a ticket not shared follows none', drawnReference(pr, { ...ticket, shared: false }), null)
check('a pull request inside a flow process holds none', drawnReference({ ...pr, parent: 9 }, ticket), null)

// Two attributes referring to tickets: both offered, the first taken.
const twice = { ...pr, attributes: [...pr.attributes, attr(1, 'parent_ticket', { ref: TICKET })] }
check('both references are offered', drawnReference(twice, ticket)?.attributes, ['ticket', 'parent_ticket'])

// Where the line leaves and meets the objects: from the handle on the right
// of the attribute's row into the ticket's top, where it is anywhere; while
// those aren't measured, from the right level with the pull request's name
// into the middle of the ticket's top.
const box = (id: string, x: number, y: number) => ({ id, x, y, width: 200, height: 120 })
const ends = ([from, to]: ReturnType<typeof referenceEnds>) => [from.position, from.x, from.y, to.position, to.x, to.y]
const row = { x: 203, y: 96, position: Position.Right }
const top = { x: 380, y: 296, position: Position.Top }
check('from the attribute into the top', ends(referenceEnds(box('pr', 0, 0), box('t', 300, 300), row, top)), ['right', 203, 96, 'top', 380, 296])
check('so with the ticket to the left', ends(referenceEnds(box('pr', 300, 0), box('t', 0, 300), row, top)), ['right', 203, 96, 'top', 380, 296])
check('unmeasured, level with the name and into the middle', ends(referenceEnds(box('pr', 0, 0), box('t', 50, 300))), ['right', 200, 28, 'top', 150, 300])
check('a short object leaves halfway down', referenceEnds({ ...box('pr', 0, 0), height: 40 }, box('t', 300, 0))[0].y, 20)
check('a row named after its attribute', referenceHandle('ticket'), 'reference-out-ticket')

console.log(`${checked} checks of a board's references, ${failed} failed`)
if (failed > 0) process.exitCode = 1
