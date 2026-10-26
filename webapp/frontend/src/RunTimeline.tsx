import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { getRun, type FlowRunDetail, type Process } from './api'
import { formatTime, RUN_LABELS, STEP_LABELS } from './Runs'
import { ChevronIcon } from './icons'
import { barFrom, CUT_WIDTH, extent, forPerson, formatSpan, lanesOf, scaleOf, shown, stretchesOf, ticksOf, type Bar, type Lane } from './timeline'

// How often the runs a lane unfolded are fetched again while they go.
const CHILD_REFRESH_MS = 1500

// The most runs one lane unfolds, such as the items of a long list.
const CHILD_RUNS_SHOWN = 50

// How wide the names of the lanes are, and how tall a row of bars.
const LABEL_WIDTH = 220
const ROW_HEIGHT = 24

// How far the axis can be stretched, scrolling sideways.
const ZOOMS = [1, 2, 4, 8, 16]

// A run's timeline in a large window: its steps on a time axis, a lane per
// process, so what ran at once shows side by side; see timeline.ts. A bar is
// coloured by how its step ended; what waited for a person, a review or a
// loop's question, is hatched, and the time a step waited for a slot comes
// before it, lighter. Each bar says how long its step took, and what its
// agent's models cost. A flow process's lane unfolds the lanes of what ran
// on its board, the runs it ran as fetched as it does. Clicking a bar opens
// its step in the run's panel.
export default function RunTimeline({
  run,
  processes,
  onOpenStep,
  onClose,
}: {
  run: FlowRunDetail
  // The processes of the version the board shows, for which flow process each is in.
  processes: Process[] | undefined
  onOpenStep: (run: number, step: number) => void
  onClose: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const scroller = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState<Set<string>>(new Set())
  const [children, setChildren] = useState<Map<number, FlowRunDetail>>(new Map())
  const [error, setError] = useState('')
  const [zoom, setZoom] = useState(1)
  // Whether long whiles with only people to wait for are shortened.
  const [cutWaits, setCutWaits] = useState(true)
  const [width, setWidth] = useState(0)
  // Bars still going grow as time passes.
  const [now, setNow] = useState(Date.now())

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])

  useLayoutEffect(() => {
    const el = scroller.current
    if (!el) return
    const observer = new ResizeObserver(() => setWidth(el.clientWidth))
    observer.observe(el)
    setWidth(el.clientWidth)
    return () => observer.disconnect()
  }, [])

  const parents = useMemo(() => new Map((processes ?? []).map((p) => [p.id, p.parent])), [processes])

  // The lanes as far as they are unfolded: under a lane, those of what ran
  // on its board in its run, then those of the runs it ran as.
  const top = useMemo(() => lanesOf([run], now, parents), [run, now, parents])
  const below = (l: Lane): Lane[] => {
    if (!open.has(l.key)) return []
    const runs = l.childRuns.slice(0, CHILD_RUNS_SHOWN).flatMap((id) => children.get(id) ?? [])
    return [...l.inner, ...lanesOf(runs, now, parents, l.key, l.depth + 1)]
  }
  const lanes = shown(top, below)
  const wanted = lanes.filter((l) => open.has(l.key)).flatMap((l) => l.childRuns.slice(0, CHILD_RUNS_SHOWN))
  const wantedKey = [...new Set(wanted)].sort((a, b) => a - b).join(',')
  const going = run.status === 'running' || wanted.some((id) => children.get(id)?.status === 'running')

  useEffect(() => {
    if (!going) return
    const t = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(t)
  }, [going])

  // The runs unfolded lanes ran as, fetched once, and again while they go.
  useEffect(() => {
    if (wantedKey === '') return
    let stale = false
    let timer: number | undefined
    const ids = wantedKey.split(',').map(Number)
    const load = async () => {
      const need = ids.filter((id) => !children.has(id) || children.get(id)!.status === 'running')
      try {
        const got = await Promise.all(need.map((id) => getRun(id)))
        if (stale) return
        if (got.length > 0) setChildren((cs) => new Map([...cs, ...got.map((d) => [d.id, d] as const)]))
        setError('')
        if (got.some((d) => d.status === 'running')) timer = window.setTimeout(load, CHILD_REFRESH_MS)
      } catch (err) {
        if (!stale) setError(err instanceof Error ? err.message : String(err))
      }
    }
    load()
    return () => {
      stale = true
      window.clearTimeout(timer)
    }
    // The runs fetched are read as they are when a load starts; depending on
    // them would load again on every answer.
  }, [wantedKey])

  const bars = lanes.flatMap((l) => l.bars)
  const [from, to] = extent(run, bars, now)
  const stretches = stretchesOf(from, to, bars, cutWaits)
  const cuttable = cutWaits ? stretches.some((s) => s.cut) : stretchesOf(from, to, bars, true).some((s) => s.cut)
  const track = Math.max(200, (width - LABEL_WIDTH) * zoom)
  const x = scaleOf(stretches, track)
  // Ticks too close to the one before, past a shortened stretch, are left out.
  const marks = ticksOf(stretches, Math.max(2, Math.floor(track / 110))).filter(
    (t, i, ts) => i === 0 || x(t) - x(ts[i - 1]) >= 60,
  )
  const unrecorded = run.steps.some((s) => (s.kind === 'agent' || s.kind === 'script' || s.kind === 'connector') && s.slotWaitMs === null)
  const copied = run.steps.filter((s) => s.kind === 'copied').length

  const toggle = (l: Lane) => {
    const next = new Set(open)
    if (next.has(l.key)) next.delete(l.key)
    else next.add(l.key)
    setOpen(next)
  }

  return (
    <dialog
      ref={ref}
      className="review-dialog run-timeline"
      aria-label={`Timeline of run #${run.id}`}
      onClose={onClose}
      // Escape closes the dialog, not the run's panel it opened from.
      onKeyDown={(e) => e.stopPropagation()}
    >
      <header className="panel-header">
        <h2>
          Timeline of run #{run.id}
          {going && <span className="run-logs-live"> · following the run</span>}
        </h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <p className="panel-note">
        {RUN_LABELS[run.status]} · started {formatTime(run.startedAt)}
        {run.finishedAt && `, took ${formatSpan(Date.parse(run.finishedAt) - Date.parse(run.startedAt))}`}
        {copied > 0 && ` · the ${copied} steps copied from run #${run.rerunOf} took no time here`}
      </p>
      <div className="timeline-tools">
        <ul className="timeline-legend" aria-label="What the bars' colours say">
          <li>
            <span className="timeline-swatch active" /> Running
          </li>
          <li>
            <span className="timeline-swatch done" /> Done
          </li>
          <li>
            <span className="timeline-swatch error" /> Failed
          </li>
          <li>
            <span className="timeline-swatch slot" /> Waiting for a slot
          </li>
          <li>
            <span className="timeline-swatch person" /> Waiting for a person
          </li>
        </ul>
        {cuttable && (
          <label className="timeline-cut-toggle" title="Show long whiles with only people to wait for shortened, so what else the run did has room">
            <input type="checkbox" checked={cutWaits} onChange={(e) => setCutWaits(e.target.checked)} /> Shorten long waits for people
          </label>
        )}
        <span className="timeline-zoom">
          <button onClick={() => setZoom(ZOOMS[Math.max(0, ZOOMS.indexOf(zoom) - 1)])} disabled={zoom === ZOOMS[0]} aria-label="Zoom out">
            −
          </button>
          <span>{zoom}×</span>
          <button
            onClick={() => setZoom(ZOOMS[Math.min(ZOOMS.length - 1, ZOOMS.indexOf(zoom) + 1)])}
            disabled={zoom === ZOOMS.at(-1)}
            aria-label="Zoom in"
          >
            +
          </button>
        </span>
      </div>
      {error && <p className="panel-warning">A run this one ran as couldn't load: {error}</p>}
      <div ref={scroller} className="timeline-body">
        {lanes.length === 0 ? (
          <p className="panel-note">No process has run yet.</p>
        ) : (
          <div className="timeline" style={{ width: LABEL_WIDTH + track }}>
            <div className="timeline-axis" style={{ paddingLeft: LABEL_WIDTH }}>
              {marks.map((t) => (
                <span key={t} style={{ left: LABEL_WIDTH + x(t) }}>
                  {t === from ? '0' : `+${formatSpan(t - from)}`}
                </span>
              ))}
              {stretches
                .filter((st) => st.cut)
                .map((st) => (
                  <span
                    key={st.from}
                    className="timeline-cut-label"
                    style={{ left: LABEL_WIDTH + x(st.from), width: CUT_WIDTH }}
                    title={`${formatSpan(st.to - st.from)} with only people to wait for, shortened`}
                  >
                    {formatSpan(st.to - st.from)}
                  </span>
                ))}
            </div>
            <div className="timeline-lines" style={{ left: LABEL_WIDTH }} aria-hidden="true">
              {marks.map((t) => (
                <span key={t} style={{ left: x(t) }} />
              ))}
              {stretches
                .filter((st) => st.cut)
                .map((st) => (
                  <span key={st.from} className="timeline-cut" style={{ left: x(st.from), width: CUT_WIDTH }} />
                ))}
            </div>
            {lanes.map((l) => (
              <LaneRow
                key={l.key}
                lane={l}
                open={open.has(l.key)}
                onToggle={l.inner.length > 0 || l.childRuns.length > 0 ? () => toggle(l) : undefined}
                runs={children}
                x={x}
                track={track}
                onOpen={(b) => {
                  onOpenStep(b.run, b.step.id)
                  onClose()
                }}
              />
            ))}
          </div>
        )}
      </div>
      <p className="panel-note">
        {unrecorded
          ? 'This run is from before how long steps waited for a slot was recorded, so its waits for a slot don’t show. '
          : 'A step waiting for a slot shows once it has one. '}
        Costs are what each agent’s models cost as pi reports them, its follow-ups included.
      </p>
    </dialog>
  )
}

function LaneRow({
  lane: l,
  open,
  onToggle,
  runs,
  x,
  track,
  onOpen,
}: {
  lane: Lane
  open: boolean
  // Unfolds what ran on its board; absent if nothing did.
  onToggle?: () => void
  // The runs unfolded lanes ran as, by id, as far as they are fetched.
  runs: Map<number, FlowRunDetail>
  x: (t: number) => number
  track: number
  onOpen: (b: Bar) => void
}) {
  const took = l.bars.reduce((n, b) => n + b.end - b.start, 0)
  const cost = l.bars.reduce((n, b) => n + (b.step.usage?.cost ?? 0), 0)
  const more = l.childRuns.length - CHILD_RUNS_SHOWN
  return (
    <div className="timeline-lane" style={{ height: l.rows * ROW_HEIGHT + 8 }}>
      <div className="timeline-label" style={{ width: LABEL_WIDTH, paddingLeft: 8 + l.depth * 14 }}>
        {onToggle ? (
          <button
            className={`timeline-fold${open ? ' open' : ''}`}
            onClick={onToggle}
            aria-expanded={open}
            title={open ? 'Fold what ran on its board' : 'Unfold what ran on its board'}
          >
            <ChevronIcon />
          </button>
        ) : (
          <span className="timeline-fold-space" />
        )}
        <span
          className="timeline-name"
          title={`${l.name}: ${l.bars.length} ${l.bars.length === 1 ? 'step' : 'steps'}, ${formatSpan(took)} in all${cost > 0 ? `, ${formatCost(cost)}` : ''}`}
        >
          {l.name}
        </span>
        <span className={`process-kind ${l.kind}`}>{l.kind}</span>
        {open && more > 0 && (
          <span className="timeline-more">
            first {CHILD_RUNS_SHOWN} of {l.childRuns.length} runs
          </span>
        )}
      </div>
      {l.bars.map((b) => (
        <BarButton
          key={`${b.run}-${b.step.id}`}
          bar={b}
          x={x}
          track={track}
          item={(b.step.childRun !== null ? runs.get(b.step.childRun) : runs.get(b.run))?.itemKey || undefined}
          onOpen={() => onOpen(b)}
        />
      ))}
    </div>
  )
}

// A step's bar, saying how long it took and what it cost, inside it if it
// fits, else after it, or before it at the axis's end.
function BarButton({
  bar: b,
  x,
  track,
  item,
  onOpen,
}: {
  bar: Bar
  x: (t: number) => number
  track: number
  // What tells apart the item of a list its step ran its board for.
  item?: string
  onOpen: () => void
}) {
  const s = b.step
  const person = forPerson(b)
  const left = x(barFrom(b))
  const width = Math.max(3, x(b.end) - left)
  const wait = b.queued === null ? 0 : x(b.start) - left
  const took = formatSpan(b.end - b.start)
  const cost = s.usage && s.usage.cost > 0 ? formatCost(s.usage.cost) : ''
  const label = [item, took, cost].filter(Boolean).join(' · ')
  const labelWidth = label.length * 6.5
  const inside = width - wait > labelWidth + 10
  const aside = left + width + 4 + labelWidth > track && left > labelWidth + 4 ? left - 4 - labelWidth : left + width + 4
  const title = [
    `${s.processName}${item ? ` (${item})` : ''}: ${STEP_LABELS[s.status]}`,
    `started ${formatTime(s.startedAt)}`,
    `${person ? 'waited for a person' : b.going ? 'running for' : 'took'} ${took}`,
    b.queued !== null && `waited ${formatSpan(b.start - b.queued)} for a slot first`,
    s.try !== null && s.try > 1 && `try ${s.try}`,
    cost && `its models cost ${cost}`,
  ]
    .filter(Boolean)
    .join(' · ')
  return (
    <>
      <button
        className={`timeline-bar ${person ? 'person' : s.status}${b.going ? ' going' : ''}`}
        style={{ left: LABEL_WIDTH + left, width, top: 4 + b.row * ROW_HEIGHT }}
        title={title}
        aria-label={title}
        onClick={onOpen}
      >
        {wait > 0 && <span className="timeline-slot" style={{ width: wait }} />}
        {inside && <span className="timeline-bar-label">{label}</span>}
      </button>
      {!inside && (
        <span className="timeline-bar-aside" style={{ left: LABEL_WIDTH + aside, top: 4 + b.row * ROW_HEIGHT }} aria-hidden="true">
          {label}
        </span>
      )}
    </>
  )
}

// Dollars to the cent, or closer under a dime.
const formatCost = (d: number) => '$' + (d < 0.1 ? d.toFixed(4) : d.toFixed(2))
