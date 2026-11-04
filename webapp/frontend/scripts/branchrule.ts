// What a flow's branch rule suggests as `@` is typed (src/branchrule.ts):
// the values runs start with, the one being typed, and those matching it.
// Run with `npm test`, which needs Node 22.6 or later to load TypeScript.

import type { FlowObject, Link, ObjectAttribute } from '../src/api.ts'
import { branchValues, matchValues, slug, typedValue } from '../src/branchrule.ts'

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

function output(object: number): Link {
  return { id: object, process: 1, object, direction: 'output', state: null, states: [], role: '', condition: false, query: null, route: null, at: null }
}

check('a name as a facet writes it', slug(' Pull request '), 'Pull_request')

const objects = [
  object(1, 'Name', [attr(1, 'first'), attr(1, 'surname'), attr(1, 'born', { type: 'datetime' }), attr(1, 'photo', { file: true })]),
  object(2, 'Greeting', [attr(2, 'text')]),
  object(3, 'Pull request', [attr(3, 'branch')], { shared: true, pick: { state: 'open', where: '', oldest: true } as FlowObject['pick'] }),
  object(4, 'Key', []),
  object(5, 'Requester', [], { human: true }),
  object(6, 'Tickets', [attr(6, 'key')], { many: true }),
  object(7, 'Inner', [], { parent: 9 }),
  object(8, 'Ticket', [attr(8, 'key')], { shared: true }),
]
const values = branchValues(objects, [output(2), output(8)])
check(
  'what runs start with',
  values.map((v) => [v.insert, v.hint]),
  [
    ['{@Name.first}', 'given to each run'],
    ['{@Name.surname}', 'given to each run'],
    ['{@Pull_request.branch}', 'the record each run picks'],
    ['{@Key}', 'given to each run'],
    ['{@Ticket.key}', 'the record a run is started on'],
  ],
)

check('nothing typed', typedValue('feature/', 8), null)
check('an @ typed', typedValue('@N', 2), { from: 0, to: 2, typed: 'N' })
check('after a prefix', typedValue('feature/@na', 11), { from: 8, to: 11, typed: 'na' })
check('within braces', typedValue('feature/{@Na', 12), { from: 8, to: 12, typed: 'Na' })
check('one written out, its close replaced too', typedValue('feature/{@Na}-x', 12), { from: 8, to: 13, typed: 'Na' })
check('past a value', typedValue('{@Name.first}/x', 15), null)

check('matched by name', matchValues(values, 'n').map((v) => v.insert), ['{@Name.first}', '{@Name.surname}'])
check('by the name with a space', matchValues(values, 'pull r').map((v) => v.insert), ['{@Pull_request.branch}'])
check('as a facet writes it', matchValues(values, 'pull_request.b').map((v) => v.insert), ['{@Pull_request.branch}'])
check('by an attribute', matchValues(values, 'key').map((v) => v.insert), ['{@Key}', '{@Ticket.key}'])
check('everything when nothing is typed', matchValues(values, '').length, values.length)

console.log(`${checked} checks of a branch's values, ${failed} failed`)
if (failed > 0) process.exitCode = 1
