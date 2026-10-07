import { useEffect, useState } from 'react'
import { Handle, Position, useNodeConnections, useUpdateNodeInternals, type Node, type NodeProps } from '@xyflow/react'
import { useBoardActions } from './actions'
import type { ObjectAttribute, ObjectState } from './api'
import { ExhibitionIcon, GearIcon, ObjectIcon, TrashIcon } from './icons'
import { AppendButton, NameInput } from './ProcessNode'
import WidthResizer from './WidthResizer'

export type ObjectNodeData = {
  name: string
  states: ObjectState[]
  // Edited in the side panel; only the names show on the node.
  attributes: ObjectAttribute[]
  // Runs store what they generate it with; see FlowObject.
  persistent: boolean
  // True while a new object is being named or an existing one renamed.
  editing: boolean
  // True while a new state is being named.
  addingState?: boolean
  // Set once a process generated this object in the latest flow run.
  status?: 'done'
  // What the generating process printed in the latest run; passed to the processes that take this object.
  content?: string
  // The name of the state the object was in at the end of the latest run.
  activeState?: string
}

export type ObjectNodeType = Node<ObjectNodeData, 'object'>

// Handle ids of a state: links into it generate the object in that state,
// links out of it take the object once it enters that state.
export const stateTargetHandle = (state: number) => `state-in-${state}`
export const stateSourceHandle = (state: number) => `state-out-${state}`

// OPM object: a square-cornered card that processes generate (link in on
// top) and take as input (link out below). Its states sit inside it, as in
// OPM, each with its own links in and out. The attributes it exhibits are
// listed below them; OPM would draw each as an object of its own.
export default function ObjectNode({ id, data, selected }: NodeProps<ObjectNodeType>) {
  const actions = useBoardActions()
  const draft = id.startsWith('draft-')
  const leaf = useNodeConnections({ handleType: 'source' }).length === 0
  const updateNodeInternals = useUpdateNodeInternals()
  const stateKey = data.states.map((s) => `${s.id}:${s.name}`).join() + (data.addingState ? '+' : '')

  // Each state has its own handles, which move as states come and go.
  useEffect(() => updateNodeInternals(id), [id, stateKey, updateNodeInternals])

  return (
    <div
      className={`object-node${selected ? ' selected' : ''}${data.status ? ` ${data.status}` : ''}`}
      title={data.content}
    >
      <WidthResizer id={id} minWidth={140} />
      <Handle type="target" position={Position.Top} />
      <Handle type="source" position={Position.Bottom} />
      <div className="object-main">
        <span className="node-icon">
          <ObjectIcon />
        </span>
        <div className="node-text">
          {data.editing ? (
            <NameInput
              initial={data.name}
              placeholder="Object name"
              onCommit={(name) => actions.commitName(id, name)}
              onCancel={() => actions.cancelName(id)}
            />
          ) : (
            <>
              <span className="node-name">{data.name}</span>
              <span className="node-subtitle">
                {data.activeState ? `In state ${data.activeState}` : data.status === 'done' ? 'Generated' : 'Object'}
              </span>
            </>
          )}
        </div>
        {!draft && (
          <span className="node-buttons nodrag">
            <button title="Settings" aria-label="Settings" onClick={() => actions.openPanel(id)}>
              <GearIcon />
            </button>
            <button title="Delete" aria-label="Delete" disabled={!actions.editable} onClick={() => actions.remove(id)}>
              <TrashIcon />
            </button>
          </span>
        )}
      </div>
      {(data.states.length > 0 || data.addingState) && (
        <div className="object-states">
          {data.states.map((s) => (
            <StatePill key={s.id} objectId={id} state={s} active={data.activeState === s.name} />
          ))}
          {data.addingState && (
            <span className="state-pill editing">
              <NameInput
                initial=""
                placeholder="State"
                onCommit={(name) => actions.addState(id, name)}
                onCancel={() => actions.cancelAddState(id)}
              />
            </span>
          )}
        </div>
      )}
      {data.attributes.length > 0 && (
        <div className="object-attributes" title="Attributes: what an agent generating this object replies with">
          <ExhibitionIcon />
          <ul>
            {data.attributes.map((a) => (
              <li key={a.id} title={a.definition || undefined}>
                {a.name}
              </li>
            ))}
          </ul>
        </div>
      )}
      {leaf && !draft && actions.editable && <AppendButton id={id} title="Add a process that takes this object" />}
    </div>
  )
}

// OPM state: a rounded box inside its object, entered from the left and left
// from the right, so links to and from states stand apart from the object's
// own. Its handles sit on the object's edges, level with it, so its links
// run clear of the object. Double-click to rename it.
function StatePill({ objectId, state, active }: { objectId: string; state: ObjectState; active: boolean }) {
  const actions = useBoardActions()
  const [editing, setEditing] = useState(false)

  return (
    <div className="state-row" data-state={state.id}>
      <Handle type="target" position={Position.Left} id={stateTargetHandle(state.id)} />
      <Handle type="source" position={Position.Right} id={stateSourceHandle(state.id)} />
      <span
        className={`state-pill${active ? ' active' : ''}${editing ? ' editing' : ''}`}
        onDoubleClick={(e) => {
          e.stopPropagation()
          if (actions.editable) setEditing(true)
        }}
      >
        {editing ? (
          <NameInput
            initial={state.name}
            placeholder="State"
            onCommit={(name) => {
              setEditing(false)
              if (name !== state.name) actions.renameState(objectId, state.id, name)
            }}
            onCancel={() => setEditing(false)}
          />
        ) : (
          <>
            <span className="state-name">{state.name}</span>
            {actions.editable && (
              <button
                className="state-remove nodrag"
                title="Delete state"
                aria-label={`Delete state ${state.name}`}
                onClick={() => actions.removeState(objectId, state.id)}
              >
                ×
              </button>
            )}
          </>
        )}
      </span>
    </div>
  )
}
