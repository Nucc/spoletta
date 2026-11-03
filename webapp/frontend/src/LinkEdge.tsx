import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type PointerEvent as ReactPointerEvent,
} from 'react'
import { createPortal } from 'react-dom'
import {
  BaseEdge,
  Position,
  useEdges,
  useNodesData,
  useReactFlow,
  useStore,
  type Edge,
  type EdgeProps,
  type ReactFlowState,
} from '@xyflow/react'
import { useBoardActions } from './actions'
import type { Link, LinkRole } from './api'
import { PlusIcon } from './icons'
import QuantityLabel from './QuantityLabel'
import EdgeLabel, { EdgeDimmed } from './EdgeLabel'
import type { ObjectNodeType } from './ObjectNode'
import type { ProcessNodeType } from './ProcessNode'
import {
  lanesOf,
  moveSegment,
  roundedPath,
  routeLinks,
  routePoints,
  segments,
  shifted,
  type Box,
  type End,
  type Lanes,
  type LinkEnds,
  type LinkRoute,
} from './route'
import { CONDITION_WORDS, EVENT_WORDS, ROLE_WORDS } from './words'

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
  // Faded, while another node's links are in focus (Board.tsx).
  dim?: boolean
  // A failure connection, which runs its target once its source fails.
  failure?: boolean
}

export type LinkEdgeType = Edge<LinkEdgeData, 'link'>

// The radius of the filled circle an agent link ends in.
const AGENT_DOT_RADIUS = 5

// Where the board's links run when nobody moved them by hand, worked out for
// the whole board at once from where its nodes are (routeLinks in route.ts),
// so each keeps clear of the nodes and of the others, and the ends sharing a
// side spread along it; each link reads its own. The board provides it from
// useLinkRoutes.
export const LinkRoutes = createContext<Map<string, LinkRoute>>(new Map())

// A handle and where a link's end on it goes.
type HandleEnd = { id: string | null; end: End }

// What routing the links depends on of the nodes: the boxes of all but
// notes, their handles' ends, and those being dragged, with a key that
// changes when any does.
type Geometry = {
  key: string
  boxes: Box[]
  handles: Map<string, { source: HandleEnd[]; target: HandleEnd[] }>
  moving: string[]
}

// Where React Flow puts a link's end on a handle: the middle of its outer side.
function handleEnd(x: number, y: number, h: { x: number; y: number; width: number; height: number; position: Position }): End {
  const [left, top] = [x + h.x, y + h.y]
  switch (h.position) {
    case Position.Top:
      return { x: left + h.width / 2, y: top, position: h.position }
    case Position.Right:
      return { x: left + h.width, y: top + h.height / 2, position: h.position }
    case Position.Bottom:
      return { x: left + h.width / 2, y: top + h.height, position: h.position }
    default:
      return { x: left, y: top + h.height / 2, position: h.position }
  }
}

function geometryOf(s: ReactFlowState): Geometry {
  const boxes: Box[] = []
  const handles: Geometry['handles'] = new Map()
  const moving: string[] = []
  for (const node of s.nodeLookup.values()) {
    const { width = 0, height = 0 } = node.measured
    if (node.type === 'note' || node.hidden || width === 0 || height === 0) continue
    const { x, y } = node.internals.positionAbsolute
    boxes.push({ id: node.id, x, y, width, height })
    type Bounds = { id?: string | null; x: number; y: number; width: number; height: number; position: Position }[]
    const ends = (bounds: Bounds | null | undefined) => (bounds ?? []).map((h) => ({ id: h.id ?? null, end: handleEnd(x, y, h) }))
    handles.set(node.id, { source: ends(node.internals.handleBounds?.source), target: ends(node.internals.handleBounds?.target) })
    if (node.dragging) moving.push(node.id)
  }
  return { key: JSON.stringify([boxes, [...handles], moving]), boxes, handles, moving }
}

// Routes the board's links, again whenever its nodes move or change size or
// its links change.
export function useLinkRoutes(edges: Edge[]): Map<string, LinkRoute> {
  const geometry = useStore(geometryOf, (a, b) => a.key === b.key)
  const links = edges
    .filter((e) => e.type === 'link')
    .map((e) => ({
      id: e.id,
      source: e.source,
      target: e.target,
      sourceHandle: e.sourceHandle ?? null,
      targetHandle: e.targetHandle ?? null,
      route: (e.data as LinkEdgeData | undefined)?.route ?? null,
    }))
  const linksKey = JSON.stringify(links)
  const before = useRef(new Map<string, LinkRoute>())
  return useMemo(() => {
    const handle = (node: string, type: 'source' | 'target', id: string | null) => {
      const all = geometry.handles.get(node)?.[type] ?? []
      return (id === null ? all[0] : all.find((h) => h.id === id))?.end
    }
    const ends: LinkEnds[] = links.flatMap((l) => {
      const from = handle(l.source, 'source', l.sourceHandle)
      const to = handle(l.target, 'target', l.targetHandle)
      if (!from || !to) return []
      const { id, source, target, route: stored } = l
      return [{ id, source, target, from, to, spreadFrom: l.sourceHandle === null, spreadTo: l.targetHandle === null, stored }]
    })
    const dragging = geometry.moving.length > 0 ? { moving: new Set(geometry.moving), before: before.current } : undefined
    before.current = routeLinks(geometry.boxes, ends, dragging)
    return before.current
    // The links are as their key says.
  }, [geometry, linksKey])
}

// How an input link's process takes the object, as the picker names them:
// in plain words, OPM's after; see words.ts.
const ROLES: { value: LinkRole; label: string; title: string }[] = (['', 'instrument', 'consumption'] as const).map((value) => ({
  value,
  ...ROLE_WORDS[value],
}))

// A link drawn in straight segments with rounded corners, with a button that
// inserts a step into it. Once selected, each segment has a grip that drags
// it sideways, so the link can be routed round other nodes; double-click the
// link to go back to the default route. A person's link into a process is an
// OPM agent link, which ends in a filled circle instead of an arrow, and an
// instrument link in a hollow one; a link using its object up says so. A
// selected input link picks how its process takes the object.
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
  const role = data?.link?.direction === 'input' && !agent ? data.link.role : undefined
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
  // The board's route for the link, and its ends moved along their sides
  // apart from the others there.
  const auto = useContext(LinkRoutes).get(id)
  const source = shifted({ x: sourceX, y: sourceY, position: sourcePosition }, auto?.fromShift ?? 0)
  const end = shifted({ x: targetX, y: targetY, position: targetPosition }, auto?.toShift ?? 0)
  const points = routePoints(source, end, data?.route, auto?.lanes)
  const {
    path,
    label: { x: labelX, y: labelY },
  } = roundedPath(points)
  // An agent's link to an object, not one of its states, can list states
  // for the agent to decide between: shown once it lists any, or the link is
  // selected to choose them, if the object has states.
  const process = useNodesData<ProcessNodeType>(data?.link ? String(data.link.process) : '')
  const agentDecision =
    data?.link?.direction === 'output' &&
    data.link.state === null &&
    process?.type === 'process' &&
    process.data.kind === 'agent' &&
    object?.type === 'object' &&
    object.data.states.length > 0 &&
    (data.link.states.length > 0 || (selected && actions.editable))
  const insertable = data?.insertable && actions.editable && !agentDecision
  // Grips come and go as segments are added during a drag, so the drag
  // follows the pointer on the window, from the lanes it started with.
  const onGripDown = (e: ReactPointerEvent<HTMLDivElement>, index: number, vertical: boolean) => {
    e.stopPropagation()
    e.preventDefault()
    const lanes = lanesOf(source, end, data?.route, auto?.lanes)
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
    <EdgeDimmed.Provider value={Boolean(data?.dim)}>
      <BaseEdge id={id} path={path} markerEnd={agent || role === 'instrument' ? undefined : markerEnd} style={style} />
      {agent && <circle className="agent-dot" r={AGENT_DOT_RADIUS} {...dotCenter(end.x, end.y, targetPosition)} />}
      {role === 'instrument' && <circle className="instrument-dot" r={AGENT_DOT_RADIUS} {...dotCenter(end.x, end.y, targetPosition)} />}
      {role !== undefined && ((selected && actions.editable) || role === 'consumption' || data?.link?.condition || data?.link?.query) && (
        <EdgeLabel>
          <div className="link-role nodrag nopan" style={{ transform: `translate(-50%, -50%) translate(${roleAt(end.x, end.y, targetPosition)})` }}>
            {selected && actions.editable ? (
              <>
                <select
                  aria-label="How the process takes the object"
                  value={role}
                  title={ROLES.find((r) => r.value === role)?.title}
                  onChange={(e) => actions.setRole(id, e.target.value as LinkRole)}
                >
                  {ROLES.map((r) => (
                    <option key={r.value} value={r.value} title={r.title}>
                      {r.label}
                    </option>
                  ))}
                </select>
                {data?.link?.state !== null && (
                  <select
                    aria-label="When the state makes the process run"
                    value={data?.link?.condition ? 'condition' : 'event'}
                    onChange={(e) => actions.setCondition(id, e.target.value === 'condition')}
                  >
                    <option value="event" title={EVENT_WORDS.title}>
                      {EVENT_WORDS.label}
                    </option>
                    <option value="condition" title={CONDITION_WORDS.title}>
                      {CONDITION_WORDS.label}
                    </option>
                  </select>
                )}
              </>
            ) : (
              <>
                {data?.link?.condition && (
                  <span className="condition" title={`Only if in it: ${CONDITION_WORDS.title.charAt(0).toLowerCase()}${CONDITION_WORDS.title.slice(1)}`}>
                    c
                  </span>
                )}
                {role === 'consumption' && <span title={ROLE_WORDS.consumption.title}>{ROLE_WORDS.consumption.short}</span>}
                {data?.link?.query && (
                  <span title="A query: the process takes what earlier runs stored of the object, as its panel says, rather than waiting for it">
                    records
                  </span>
                )}
              </>
            )}
          </div>
        </EdgeLabel>
      )}
      {selected && data?.routable && actions.editable && (
        <EdgeLabel>
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
        </EdgeLabel>
      )}
      {counted && objectId && (
        <QuantityLabel
          objectId={objectId}
          x={objectAtSource ? source.x : end.x}
          y={objectAtSource ? source.y : end.y}
          side={objectAtSource ? sourcePosition : targetPosition}
        />
      )}
      {(data?.decision || agentDecision) && data?.link && (
        <EdgeLabel>
          <DecisionStates
            edgeId={id}
            processId={sourceId}
            objectId={target}
            agent={!data.decision}
            chosen={data.link.states}
            style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)` }}
          />
        </EdgeLabel>
      )}
      {data?.failure && (
        <EdgeLabel>
          <div
            className="failure-label nodrag nopan"
            style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)` }}
            title="Runs when the process it leaves fails, taking what it took and what went wrong"
          >
            on failure
          </div>
        </EdgeLabel>
      )}
      {insertable && (
        <EdgeLabel>
          <button
            className="edge-insert nodrag nopan"
            style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)` }}
            title="Insert a step"
            aria-label="Insert a step"
            onClick={() => actions.insert(id)}
          >
            <PlusIcon />
          </button>
        </EdgeLabel>
      )}
    </EdgeDimmed.Provider>
  )
}

// Where an input link's role shows: on the link, just before the process.
function roleAt(x: number, y: number, side: Position): string {
  const away = 26
  switch (side) {
    case Position.Top:
      return `${x}px, ${y - away}px`
    case Position.Bottom:
      return `${x}px, ${y + away}px`
    case Position.Left:
      return `${x - away - 20}px, ${y}px`
    default:
      return `${x + away + 20}px, ${y}px`
  }
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
// opens a checklist of the object's states. An agent's link lists the states
// it decides between the same way; with none, it generates the object as it
// is. Edge labels sit below nodes, so
// the checklist goes on the page itself, under the pill, and closes when the
// board moves.
function DecisionStates({
  edgeId,
  processId,
  objectId,
  agent,
  chosen,
  style,
}: {
  edgeId: string
  processId: string
  objectId: string
  agent: boolean
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
  const filledIn = names.length === 0 && !reviewed && !agent

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
          agent
            ? reviewed
              ? 'States the agent decides between as it reviews this object, which keeps what it holds'
              : 'States the agent decides this object comes out in. With none, it generates it as it is.'
            : filledIn
              ? 'The person fills this object in. Choose states to decide between them instead.'
              : 'States this review decides between'
        }
        onClick={onPill}
      >
        {names.length > 0 ? names.join(' · ') : filledIn ? 'Filled in' : agent ? 'Decide a state' : 'Choose states'}
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
