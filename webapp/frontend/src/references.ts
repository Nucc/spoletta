// References on a board (references.go): a copy of a shared struct on a job
// flow's top board standing, in each run, for the record another object's
// attribute names, such as the ticket a pull request is for. The board draws
// it as a line from the object holding the reference to the copy following
// it, tagged with the attribute (ReferenceEdge.tsx), and makes one when an
// object is dragged onto the other, either way round. Another copy of the
// struct, with no line, is a record of its own: a ticket the pull request
// doesn't name. Checked by scripts/references.ts.

import { Position } from '@xyflow/react'
import type { FlowObject } from './api'
import type { Box, End } from './route'

// What a reference's ends are judged by: an object as the board has it now.
export type ReferenceEnd = Pick<FlowObject, 'id' | 'shared' | 'pick' | 'many' | 'human' | 'source' | 'parent' | 'owner' | 'attributes'>

// The line from the object holding a reference to the one following it is
// named after the one following it, which follows one reference at most.
export const REFERENCE_PREFIX = 'refers-'
export const referenceEdgeId = (followed: number) => `${REFERENCE_PREFIX}${followed}`

// Whether another object may follow a reference the object holds: one on
// the top board picking the record each run works on, or one runs are given
// as they start, which is a plain object, neither a person nor a list.
export function holdsReferences(o: ReferenceEnd): boolean {
  return o.parent === null && o.owner === null && (o.pick !== null || (!o.shared && !o.human && !o.many))
}

// Whether the object may follow a reference: a shared struct's copy on the
// top board, one record, not picking one of its own.
export function followsReferences(o: ReferenceEnd): boolean {
  return o.shared && o.source !== null && o.parent === null && o.owner === null && !o.many && !o.human && o.pick === null
}

// The attributes of holder through which followed can stand for the record
// they name: those referring to the records of followed's struct.
export function referencesTo(holder: ReferenceEnd, followed: ReferenceEnd): string[] {
  if (holder.id === followed.id || !holdsReferences(holder) || !followsReferences(followed)) return []
  return holder.attributes.filter((a) => a.ref !== undefined && a.ref === followed.source).map((a) => a.name)
}

// The handle on the right of the row of an attribute referring to another
// struct's records (ObjectNode.tsx), which a reference through it leaves
// from. It takes no links of its own.
export const referenceHandle = (attribute: string) => `reference-out-${attribute}`

// How far below an object's top a reference leaves its side while the row of
// its attribute isn't there: level with its name.
const NAME_ROW = 28

// Where a reference's line leaves the object holding it and meets the one
// following it, which the board routes it between as it does processes'
// links (routeLinks in route.ts): from the right of the row of the attribute
// it goes through, as a state's links leave from the right of its row, into
// the top of the one following it, where the links of processes generating
// it come in too. Given those handles' ends as the board has them; before
// the attribute's row is measured, or once the holder has no attribute of
// that name, the line leaves level with its name, and it meets the middle of
// the top.
export function referenceEnds(holder: Box, followed: Box, attribute?: End, top?: End): [End, End] {
  return [
    attribute ?? { x: holder.x + holder.width, y: holder.y + Math.min(NAME_ROW, holder.height / 2), position: Position.Right },
    top ?? { x: followed.x + followed.width / 2, y: followed.y, position: Position.Top },
  ]
}

// A line drawn between two objects read as a reference: which holds it,
// which follows it, and the attributes it could go through, the first taken.
export type DrawnReference = { holder: number; followed: number; attributes: string[] }

// The reference a line dragged from one object onto another makes, either
// way round, the way it was dragged first; null if neither way can.
export function drawnReference(from: ReferenceEnd, onto: ReferenceEnd): DrawnReference | null {
  for (const [holder, followed] of [
    [from, onto],
    [onto, from],
  ]) {
    const attributes = referencesTo(holder, followed)
    if (attributes.length > 0) return { holder: holder.id, followed: followed.id, attributes }
  }
  return null
}
