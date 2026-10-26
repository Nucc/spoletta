import type { LinkRole } from './api'

// OPM's words, said plainly. Where the UI names a link's role or a kind of
// flow it says what it does first and OPM's term after, such as "uses
// (instrument)", so a newcomer reads what it does and someone who knows OPM
// knows it; a kind of flow keeps its name, with what it is after, such as
// "procedure (reused flow)". docs/FLOWS.md's glossary lists them all. OPL,
// the API and the setup file keep OPM's words as they are.

// How an input link's process takes its object: the picker's label, the
// short word a link shows, and what it means.
export const ROLE_WORDS: Record<LinkRole, { label: string; short: string; title: string }> = {
  '': { label: 'takes', short: 'takes', title: 'Takes the object and leaves it for other processes' },
  instrument: {
    label: 'uses (instrument)',
    short: 'uses',
    title: 'Uses the object as a reference, without changing it: an instrument link, in OPM',
  },
  consumption: {
    label: 'uses up (consumption)',
    short: 'uses up',
    title: 'Uses the object up: once taken, it is gone. A consumption link, in OPM',
  },
}

// When a link from a state runs its process.
export const EVENT_WORDS = {
  label: 'when it enters (event)',
  title: 'The process runs when the object enters the state: an event, in OPM',
}

export const CONDITION_WORDS = {
  label: 'only if in it (condition)',
  title: 'The process runs only if the object is in the state once it is ready, and is skipped otherwise: a condition, in OPM',
}

// The kinds of flow other flows reuse, one and many, and what they are
// after a list's name.
export const PROCEDURE_WORDS = { one: 'Procedure (reused flow)', many: 'Procedures (reused flows)', gloss: 'reused flows' }
export const STRUCT_WORDS = { one: 'Struct (reused object)', many: 'Structs (reused objects)', gloss: 'reused objects' }
