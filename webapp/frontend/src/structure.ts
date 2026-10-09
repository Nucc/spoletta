import type { FlowObject, ObjectAttribute } from './api'
import { quantityText } from './quantity'

// What an object is made of, as OPM unfolds an object into a diagram of its
// own, the same way the server reads it (see structure.go): the attributes
// it exhibits, the objects it owns as attributes or parts, each made of more
// in turn, and everything the object it specializes, its general, has.
//
// What an object holds is a JSON object with a key per field: the general's
// fields first, then its own attributes, then the objects it owns, each in
// the order they were added.

// An attribute, or an object the object owns, with that object's fields.
export type Field = {
  name: string
  definition: string
  file: boolean
  // An owned object that is a list: an array of items, each with its fields,
  // at least min and at most max of them unless it is null.
  many: boolean
  min: number
  max: number | null
  // An owned object's fields; none for an attribute, or for an owned object
  // holding any JSON value.
  fields: Field[]
  // The attribute, or the owned object, it is.
  attribute?: ObjectAttribute
  object?: FlowObject
  // Set when the object has it through what it specializes.
  inherited: boolean
}

// Whether an object is on a board of processes, rather than making up another.
export const onBoard = (o: FlowObject) => o.owner === null

// The objects an object owns, in the order they were added.
export const ownedBy = (objects: FlowObject[], id: number) => objects.filter((o) => o.owner === id).sort((a, b) => a.id - b.id)

// The objects specializing one directly.
export const specializationsOf = (objects: FlowObject[], id: number) => objects.filter((o) => o.general === id)

// The fields of what an object holds. Objects the server would turn down as
// holding themselves end where they come back.
export function fieldsOf(objects: FlowObject[], id: number, seen: Set<number> = new Set()): Field[] {
  const o = objects.find((x) => x.id === id)
  if (!o || seen.has(id)) return []
  const inner = new Set(seen).add(id)
  const inherited = o.general === null ? [] : fieldsOf(objects, o.general, inner).map((f) => ({ ...f, inherited: true }))
  return [
    ...inherited,
    ...o.attributes.map((a) => ({ name: a.name, definition: a.definition, file: a.file, many: false, min: 0, max: null, fields: [], attribute: a, inherited: false })),
    ...ownedBy(objects, id).map((c) => ({
      name: c.name,
      definition: '',
      file: false,
      many: c.many,
      min: c.min,
      max: c.max,
      fields: fieldsOf(objects, c.id, inner),
      object: c,
      inherited: false,
    })),
  ]
}

// Whether a field holds a file, at any depth.
export const holdsFile = (f: Field): boolean => f.file || f.fields.some(holdsFile)

// The general and its generals, nearest first.
export function generalsOf(objects: FlowObject[], id: number): FlowObject[] {
  const chain: FlowObject[] = []
  for (let o = objects.find((x) => x.id === id); o?.general != null; ) {
    const g = objects.find((x) => x.id === o!.general)
    if (!g || chain.includes(g) || g.id === id) break
    chain.push(g)
    o = g
  }
  return chain
}

// The objects from the one on a board down to an owned object, which unfold
// in turn to reach it.
export function ownersOf(objects: FlowObject[], id: number): FlowObject[] {
  const chain: FlowObject[] = []
  for (let o = objects.find((x) => x.id === id); o; o = o.owner === null ? undefined : objects.find((x) => x.id === o!.owner)) {
    if (chain.includes(o)) break
    chain.unshift(o)
  }
  return chain
}

// The objects an object could specialize: those of its flow, other than
// people, that don't hold it through what they specialize or are made of,
// since it would then hold itself. Names that clash are the server's to say.
export function possibleGenerals(objects: FlowObject[], id: number): FlowObject[] {
  const holds = (x: number, seen: Set<number>): boolean => {
    if (x === id) return true
    if (seen.has(x)) return false
    seen.add(x)
    const o = objects.find((y) => y.id === x)
    return (o?.general != null && holds(o.general, seen)) || ownedBy(objects, x).some((c) => holds(c.id, seen))
  }
  return objects.filter((o) => !o.human && !holds(o.id, new Set()))
}

// The shape of what fields hold, as text that is the same for the same
// shape: names, lists with how many items they hold, and nested fields, in
// order.
export const shapeOf = (fields: Field[]): string =>
  fields.map((f) => `${JSON.stringify(f.name)}${f.many ? `[${quantityText(f)}]` : ''}${f.fields.length > 0 ? `{${shapeOf(f.fields)}}` : ''}`).join(',')
