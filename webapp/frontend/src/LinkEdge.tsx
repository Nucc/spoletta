import { useEffect, useRef, useState, type CSSProperties, type PointerEvent as ReactPointerEvent } from 'react'
import { createPortal } from 'react-dom'
import { BaseEdge, EdgeLabelRenderer, Position, useEdges, useNodesData, useReactFlow, type Edge, type EdgeProps } from '@xyflow/react'
import { useBoardActions } from './actions'
import type { Link } from './api'
import { PlusIcon } from './icons'
import QuantityLabel from './QuantityLabel'
import type { ObjectNodeType } from './ObjectNode'
import { lanesOf, moveSegment, roundedPath, routePoints, segments, type End, type Lanes } from './route'

export type LinkEdgeData = {
  // The object link the edge draws; the start edge and direct connections have none.
  link?: Link
  // Whether a process can be inserted here with the + button.
  insertable?: boolean
  // A human's link to the object it decides on, showing the states it
  // decides between.
  decision?: boolean
  // Where the segments run if moved by hand, and whether they can be; the
  // board keeps routes of links and the start edge, not of direct connections.
  route?: Lanes | null
  routable?: boolean
}

export type LinkEdgeType = Edge<LinkEdgeData, 'link'>

// The radius of the filled circle an agent link ends in.
const AGENT_DOT_RADIUS = 5

// A link drawn in straight segments with rounded corners, with a button that
// inserts a step into it. Once selected, each segment has a grip that drags
// it sideways, so the link can be routed round other nodes; double-click the
// link to go back to the default route. A person's link into a process is an
// OPM agent link, which ends in a filled circle instead of an arrow.
export default function LinkEdge({
  id,
  source: sourceId,
  target,
  selected,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  markerEnd,
  style,
  data,
}: EdgeProps<LinkEdgeType>) {
  const actions = useBoardActions()
  const { screenToFlowPosition } = useReactFlow()
  const from = useNodesData<ObjectNodeType>(sourceId)
  const agent = data?.link?.direction === 'input' && from?.type === 'object' && Boolean(from.data.human)
  // The object's end says how many of it there are: always for a list, and
  // for one object once the link is selected, to make it a list. Inside a
  // flow process a list from outside is one item, and a person is one.
  const objectAtSource = data?.link?.direction === 'input'
  const objectId = data?.link ? (objectAtSource ? sourceId : target) : undefined
  const object = useNodesData<ObjectNodeType>(objectId ?? '')
  const counted =
    object?.type === 'object' &&
    !object.data.boundary &&
    !object.data.human &&
    (object.data.many || (selected && actions.editable))
  const source: End = { x: sourceX, y: sourceY, position: sourcePosition }
  const end: End = { x: targetX, y: targetY, position: targetPosition }
  const points = routePoints(source, end, data?.route)
  const {
    path,
    label: { x: labelX, y: labelY },
  } = roundedPath(points)
  const insertable = data?.insertable && actions.editable
  // Grips come and go as segments are added during a drag, so the drag
  // follows the pointer on the window, from the lanes it started with.
  const onGripDown = (e: ReactPointerEvent<HTMLDivElement>, index: number, vertical: boolean) => {
    e.stopPropagation()
    e.preventDefault()
    const lanes = lanesOf(source, end, data?.route)
    const start = { x: e.clientX, y: e.clientY }
    // A click without a drag, as in a double-click, leaves the route alone.
    let moved = false
    const lanesAt = (ev: PointerEvent) => {
      const p = screenToFlowPosition({ x: ev.clientX, y: ev.clientY })
      return moveSegment(source, end, lanes, index, vertical ? p.x : p.y)
    }
    const onMove = (ev: PointerEvent) => {
      moved ||= Math.hypot(ev.clientX - start.x, ev.clientY - start.y) > 3
      if (moved) actions.setRoute(id, lanesAt(ev), false)
    }
    const onUp = (ev: PointerEvent) => {
      window.removeEventListener('pointermove', onMove)
      window.removeEventListener('pointerup', onUp)
      if (moved) actions.setRoute(id, lanesAt(ev), true)
    }
    window.addEventListener('pointermove', onMove)
    window.addEventListener('pointerup', onUp)
  }

  return (
    <>
      <BaseEdge id={id} path={path} markerEnd={agent ? undefined : markerEnd} style={style} />
      {agent && <circle className="agent-dot" r={AGENT_DOT_RADIUS} {...dotCenter(targetX, targetY, targetPosition)} />}
      {selected && data?.routable && actions.editable && (
        <EdgeLabelRenderer>
          {segments(points, { x: labelX, y: labelY }).map((seg) => (
            <div
              key={seg.index}
              className={`segment-grip nodrag nopan ${seg.vertical ? 'vertical' : 'horizontal'}`}
              style={{ transform: `translate(-50%, -50%) translate(${seg.grip.x}px, ${seg.grip.y}px)` }}
              title={`${seg.vertical ? 'Drag left or right' : 'Drag up or down'}; double-click to reset the route`}
              onPointerDown={(e) => onGripDown(e, seg.index, seg.vertical)}
              onDoubleClick={() => actions.setRoute(id, null, true)}
            />
          ))}
        </EdgeLabelRenderer>
      )}
      {counted && objectId && (
        <QuantityLabel
          objectId={objectId}
          x={objectAtSource ? sourceX : targetX}
          y={objectAtSource ? sourceY : targetY}
          side={objectAtSource ? sourcePosition : targetPosition}
        />
      )}
      {data?.decision && data.link && (
        <EdgeLabelRenderer>
          <DecisionStates
            edgeId={id}
            processId={sourceId}
            objectId={target}
            chosen={data.link.states}
            style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)` }}
          />
        </EdgeLabelRenderer>
      )}
      {insertable && (
        <EdgeLabelRenderer>
          <button
            className="edge-insert nodrag nopan"
            style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)` }}
            title="Insert a step"
            aria-label="Insert a step"
            onClick={() => actions.insert(id)}
          >
            <PlusIcon />
          </button>
        </EdgeLabelRenderer>
      )}
    </>
  )
}

// Where the agent link's circle goes: touching the process, outside it.
function dotCenter(x: number, y: number, side: Position): { cx: number; cy: number } {
  switch (side) {
    case Position.Top:
      return { cx: x, cy: y - AGENT_DOT_RADIUS }
    case Position.Bottom:
      return { cx: x, cy: y + AGENT_DOT_RADIUS }
    case Position.Left:
      return { cx: x - AGENT_DOT_RADIUS, cy: y }
    default:
      return { cx: x + AGENT_DOT_RADIUS, cy: y }
  }
}

// The states a human decides between, as a pill on its link; with none, the
// person fills the object in instead, unless the process takes it too; clicking it
// opens a checklist of the object's states. Edge labels sit below nodes, so
// the checklist goes on the page itself, under the pill, and closes when the
// board moves.
function DecisionStates({
  edgeId,
  processId,
  objectId,
  chosen,
  style,
}: {
  edgeId: string
  processId: string
  objectId: string
  chosen: number[]
  style: CSSProperties
}) {
  const actions = useBoardActions()
  const object = useNodesData<ObjectNodeType>(objectId)
  // Where the checklist opens, in page coordinates, while it is open.
  const [at, setAt] = useState<{ x: number; y: number } | null>(null)
  const ref = useRef<HTMLDivElement>(null)
  const menu = useRef<HTMLDivElement>(null)
  const states = object?.data.states ?? []
  const names = states.filter((s) => chosen.includes(s.id)).map((s) => s.name)
  const reviewed = useEdges<LinkEdgeType>().some((e) => e.source === objectId && e.target === processId)
  const filledIn = names.length === 0 && !reviewed

  useEffect(() => {
    if (!at) return
    const close = () => setAt(null)
    const onDown = (e: PointerEvent) => {
      const t = e.target as Node
      if (!ref.current?.contains(t) && !menu.current?.contains(t)) close()
    }
    window.addEventListener('pointerdown', onDown, true)
    window.addEventListener('wheel', close, true)
    return () => {
      window.removeEventListener('pointerdown', onDown, true)
      window.removeEventListener('wheel', close, true)
    }
  }, [at])

  const onPill = () => {
    if (at) return setAt(null)
    const r = ref.current?.getBoundingClientRect()
    if (r) setAt({ x: r.left + r.width / 2, y: r.bottom + 6 })
  }

  const toggle = (state: number) =>
    actions.setDecisionStates(
      edgeId,
      chosen.includes(state) ? chosen.filter((s) => s !== state) : [...chosen, state],
    )

  return (
    <div ref={ref} className="decision nodrag nopan" style={style}>
      <button
        className={`decision-pill${names.length === 0 ? ' empty' : ''}`}
        title={
          filledIn
            ? 'The person fills this object in. Choose states to decide between them instead.'
            : 'States this review decides between'
        }
        onClick={onPill}
      >
        {names.length > 0 ? names.join(' · ') : filledIn ? 'Filled in' : 'Choose states'}
      </button>
      {at &&
        createPortal(
          <div
            ref={menu}
            className="decision-menu"
            role="group"
            aria-label="States to decide between"
            style={{ left: at.x, top: at.y }}
          >
            {states.length === 0 ? (
              <p className="panel-note">Right-click the object to add states.</p>
            ) : (
              states.map((s) => (
                <label key={s.id}>
                  <input
                    type="checkbox"
                    checked={chosen.includes(s.id)}
                    disabled={!actions.editable}
                    onChange={() => toggle(s.id)}
                  />
                  {s.name}
                </label>
              ))
            )}
          </div>,
          document.body,
        )}
    </div>
  )
}
