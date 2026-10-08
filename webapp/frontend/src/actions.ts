import { createContext, useContext } from 'react'

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
  // Replaces the states a human's link to an object decides between.
  setDecisionStates: (edgeId: string, states: number[]) => void
  // False while a run is in progress, when the flow can't change shape.
  editable: boolean
}

export const BoardActionsContext = createContext<BoardActions | null>(null)

export function useBoardActions(): BoardActions {
  const actions = useContext(BoardActionsContext)
  if (!actions) throw new Error('useBoardActions outside BoardActionsContext')
  return actions
}
