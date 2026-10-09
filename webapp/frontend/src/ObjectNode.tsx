import { useEffect, useState } from 'react'
import { Handle, Position, useNodeConnections, useUpdateNodeInternals, type Node, type NodeProps } from '@xyflow/react'
import { useBoardActions } from './actions'
import type { ObjectAttribute, ObjectState, Relation } from './api'
import { AggregationIcon, ExhibitionIcon, GearIcon, HumanIcon, ObjectIcon, TrashIcon, ZoomInIcon } from './icons'
import { AppendButton, NameInput } from './ProcessNode'
import { quantityText, type Quantity } from './quantity'
import WidthResizer from './WidthResizer'

export type ObjectNodeData = {
  name: string
  states: ObjectState[]
  // Edited in the side panel; only the names show on the node.
  attributes: ObjectAttribute[]
  // The objects it owns, which make it up; they show on its own board.
  parts?: (Quantity & { id: number; name: string; relation: Relation })[]
  // The name of the object it specializes.
  general?: string
  // It makes up another object, on whose board it is.
  owned?: boolean
  // On an unfolded object's board, an object that isn't on it: the one
  // unfolded, the one it specializes or one specializing it. It stays where
  // it is shown, and is deleted where it is.
  fixed?: boolean
  // Runs store what they generate it with; see FlowObject.
  persistent: boolean
  // A list of items on its own board; inside a flow process taking it, the
  // item. Its links write how many items it holds at its end.
  many: boolean
  min: number
  max: number | null
  // A person, who carries out the human processes they link to.
  human: boolean
  // The object unfolded on the board shown.
  root?: boolean
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
  // Set on a flow process's board for the objects the flow process takes
  // ('in', shown at the start) or only generates ('out', at the end). They
  // belong to the board outside, so they can't be moved or deleted here.
  boundary?: 'in' | 'out'
}

export type ObjectNodeType = Node<ObjectNodeData, 'object'>

// Handle ids of a state: links into it generate the object in that state,
// links out of it take the object once it enters that state.
export const stateTargetHandle = (state: number) => `state-in-${state}`
export const stateSourceHandle = (state: number) => `state-out-${state}`

// OPM object: a square-cornered card that processes generate (link in on
// top) and take as input (link out below). Its states sit inside it, as in
// OPM, each with its own links in and out. The attributes it exhibits are
// listed below them, then the objects it is made of, which its own board
// shows, unfolded, linked to it. An object made of others has a thick
// border, as OPM marks a refined thing.
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
      className={`object-node${selected ? ' selected' : ''}${data.status ? ` ${data.status}` : ''}${data.boundary ? ' boundary' : ''}${data.many && !data.boundary ? ' many' : ''}${data.parts?.length ? ' refined' : ''}`}
      title={data.content}
    >
      <WidthResizer id={id} minWidth={140} />
      <Handle type="target" position={Position.Top} />
      <Handle type="source" position={Position.Bottom} />
      <div className="object-main">
        <span className="node-icon">{data.human ? <HumanIcon /> : <ObjectIcon />}</span>
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
                {data.activeState
                  ? `In state ${data.activeState}`
                  : data.status === 'done'
                    ? 'Generated'
                    : data.human
                      ? 'Person'
                      : data.general
                        ? `${data.many && !data.boundary ? 'List of ' : ''}${data.general}`
                        : !data.many
                        ? 'Object'
                        : data.boundary
                          ? 'One item of the list'
                          : 'List of items'}
              </span>
            </>
          )}
        </div>
        {!draft && (
          <span className="node-buttons nodrag">
            {!data.human && !data.root && (
              <button title="Unfold: what it is made of and what it specializes" aria-label="Unfold" onClick={() => actions.zoomIn(id)}>
                <ZoomInIcon />
              </button>
            )}
            <button title="Settings" aria-label="Settings" onClick={() => actions.openPanel(id)}>
              <GearIcon />
            </button>
            <button
              title="Delete"
              aria-label="Delete"
              disabled={!actions.editable || !!data.boundary || !!data.fixed}
              onClick={() => actions.remove(id)}
            >
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
      {data.parts && data.parts.length > 0 && (
        <div className="object-attributes object-parts" title="What it is made of; unfold it to see them">
          <ul>
            {data.parts.map((p) => (
              <li key={p.id}>
                {p.relation === 'aggregation' ? <AggregationIcon /> : <ExhibitionIcon />}
                {p.name}
                {p.many && <span className="part-many">[{quantityText(p)}]</span>}
              </li>
            ))}
          </ul>
        </div>
      )}
      {leaf && !draft && actions.editable && !data.owned && !data.fixed && (
        <AppendButton id={id} title="Add a process that takes this object" />
      )}
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
