import { createContext, useContext } from 'react'
import type { LinkRole, NoteColour } from './api'
import type { Quantity } from './quantity'

// The flows that are structs, which say where a copy of an object comes from.
export const StructFlowsContext = createContext<ReadonlySet<number>>(new Set())

// What nodes and edges can ask the board to do. Node ids are React Flow ids:
// a process id, an obj- prefixed object id or a draft- prefixed draft.
export type BoardActions = {
  // Saves a name typed inline; a draft is created with it.
  commitName: (id: string, name: string) => void
  // Drops a draft, or stops renaming an existing node.
  cancelName: (id: string) => void
  openPanel: (id: string) => void
  // Shows a flow process's board.
  zoomIn: (id: string) => void
  // Stops the process's step going in the run shown.
  stopProcess: (id: string) => void
  // Opens the dialog re-running the run shown from the process.
  rerunProcess: (id: string) => void
  remove: (id: string) => void
  // Adds what comes next below a node nothing leads on from.
  append: (id: string) => void
  // Splits a link with a new process and the object between it and the rest.
  insert: (edgeId: string) => void
  // Saves a state typed into an object's new state box, or drops the box.
  addState: (objectId: string, name: string) => void
  cancelAddState: (objectId: string) => void
  renameState: (objectId: string, stateId: number, name: string) => void
  removeState: (objectId: string, stateId: number) => void
  // Sets where a link's segments run, null for the default route; saves it
  // only when asked, so a drag can update it on every move.
  setRoute: (edgeId: string, route: number[] | null, save: boolean) => void
  // Saves a node's width after it was resized, with its x, which a resize
  // from the left moves.
  resize: (id: string, x: number, width: number) => void
  // Saves a note's place and size after it was resized.
  resizeNote: (id: string, box: { x: number; y: number; width: number; height: number }) => void
  // Saves what a note says, and its colour.
  setNoteText: (id: string, text: string) => void
  setNoteColour: (id: string, colour: NoteColour) => void
  // Points a note at a process or an object, by node id, or at nothing.
  pointNote: (id: string, target: string | null) => void
  // Replaces the states a human's link to an object decides between.
  setDecisionStates: (edgeId: string, states: number[]) => void
  // Sets how an input link's process takes its object.
  setRole: (edgeId: string, role: LinkRole) => void
  // Makes an input link naming a state a condition, or an event again.
  setCondition: (edgeId: string, condition: boolean) => void
  // Sets how many of an object there are; resolves to whether the server took it.
  setQuantity: (objectId: string, quantity: Quantity) => Promise<boolean>
  // False while a run is in progress, when the flow can't change shape.
  editable: boolean
  // True while a run shows on the board, whose processes can't be deleted
  // but can be re-run from once it ended.
  runShown: boolean
}

export const BoardActionsContext = createContext<BoardActions | null>(null)

export function useBoardActions(): BoardActions {
  const actions = useContext(BoardActionsContext)
  if (!actions) throw new Error('useBoardActions outside BoardActionsContext')
  return actions
}
