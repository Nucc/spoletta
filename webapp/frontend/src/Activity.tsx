import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { getActivity, webhookLabel, type Activity as ActivityData, type ActivityRun, type FlowActivity, type ReviewTimes } from './api'
import { ActivityIcon, FlowBuilderIcon } from './icons'
import { formatTime } from './Runs'
import { HomeFrame } from './FlowList'

// What goes on across every flow one sees: the runs going on now and those
// that failed lately, and over the last 7, 30 or 90 days, the runs a day and
// how many failed, what they spent on models, by flow and in all, the
// processes that fail most, and how long people take to decide reviews and
// how often they ask for changes. The server works it all out; the page
// draws it, a bar a day, in the viewer's time zone. The period is in the
// address, #/activity?days=<n>.

const PERIODS = [7, 30, 90]
const DEFAULT_DAYS = 30

// How often the page catches up with the runs going on.
const REFRESH_MS = 60_000

// The runs that didn't fail, those that did, and what was spent.
const RAN = '#0969da'
const FAILED = '#cf222e'
const SPENT = '#0969da'

const zone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'

const daysInHash = () => {
  const n = Number(new URLSearchParams(window.location.hash.split('?')[1] ?? '').get('days'))
  return PERIODS.includes(n) ? n : DEFAULT_DAYS
}

export default function Activity() {
  const [days, setDays] = useState(daysInHash)
  const [data, setData] = useState<ActivityData | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(() => {
    setLoading(true)
    getActivity(days, zone())
      .then((a) => {
        setData(a)
        setError(null)
      })
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
      .finally(() => setLoading(false))
  }, [days])

  useEffect(() => {
    document.title = 'Activity · Spoletta'
  }, [])

  useEffect(() => {
    load()
    const t = window.setInterval(load, REFRESH_MS)
    return () => window.clearInterval(t)
  }, [load])

  // The period goes in the address, in place, keeping the tab's entry.
  const pick = (n: number) => {
    setDays(n)
    const hash = n === DEFAULT_DAYS ? '#/activity' : `#/activity?days=${n}`
    if (window.location.hash !== hash) window.history.replaceState(window.history.state, '', window.location.pathname + window.location.search + hash)
  }

  return (
    <div className="flows-page home">
      <header className="toolbar">
        <h1>
          <a className="home" href="#/" title="Every flow, task board, procedure and struct">
            <FlowBuilderIcon /> Home
          </a>
          <span className="crumb-separator" aria-hidden>
            /
          </span>
          Activity
        </h1>
      </header>
      {error && (
        <div className="error-banner" role="alert" onClick={() => setError(null)}>
          {error} (click to dismiss)
        </div>
      )}
      <HomeFrame at="activity">
        <main className="activity">
          <div className="activity-filters">
            <div className="activity-period" role="group" aria-label="Period">
              {PERIODS.map((n) => (
                <button key={n} className={n === days ? 'active' : ''} aria-pressed={n === days} onClick={() => pick(n)}>
                  {n} days
                </button>
              ))}
            </div>
            {data && (
              <span className="flows-note">
                Days in {data.zone === 'UTC' ? 'UTC' : `your time zone, ${data.zone}`}, from {formatDay(data.days[0])} to today.
              </span>
            )}
          </div>
          {data === null ? (
            <p className="flows-note">{loading ? 'Loading…' : 'Nothing to show.'}</p>
          ) : (
            <div className={loading ? 'activity-body loading' : 'activity-body'}>
              <Now data={data} />
              <Totals data={data} />
              <section className="activity-card" aria-label="Runs a day">
                <h2>Runs a day</h2>
                <DayBars
                  days={data.days}
                  series={[
                    { label: 'Failed', color: FAILED, values: data.total.failed },
                    { label: 'Didn’t fail', color: RAN, values: data.total.runs.map((n, i) => n - data.total.failed[i]) },
                  ]}
                  format={(n) => String(n)}
                  readout={(i) => runsText(data.total.runs[i], data.total.failed[i])}
                />
              </section>
              <section className="activity-card" aria-label="Spent on models a day">
                <h2>Spent on models a day</h2>
                <DayBars
                  days={data.days}
                  series={[{ label: 'Spent', color: SPENT, values: data.total.cost }]}
                  format={formatDollars}
                  readout={(i) => `${formatDollars(data.total.cost[i])} spent by the runs started that day`}
                />
                {data.total.unpriced > 0 && <p className="flows-note">{unpricedNote(data.total.unpriced)}</p>}
              </section>
              <Flows data={data} />
              <Processes data={data} />
              <Reviews data={data} />
              <p className="flows-note activity-footnote">
                A run here is a run of a whole flow, or of a procedure on its own, whoever or whatever started it; a re-run is a
                run of its own. The runs of a flow process’s board, a subflow or an item run inside one and count toward it:
                what they spend, and when they fail it, its failing. A run failed if it ended failed; one interrupted, by the
                app stopping, or terminated, by a duplicate, didn’t. What a run spent includes its follow-ups, and counts on the
                day it started. Steps count in the flow they ran in, a retry each, so a procedure’s process failing in another
                flow counts toward the procedure. A trial of one process alone isn’t counted as a run, nor its step, but what it
                spent counts toward its flow’s spending: it cost that money. Only the flows you see count.
              </p>
            </div>
          )}
        </main>
      </HomeFrame>
    </div>
  )
}

// The runs going on and those that failed lately.
function Now({ data }: { data: ActivityData }) {
  return (
    <div className="activity-now">
      <section className="activity-card" aria-label="Running now">
        <h2>
          Running now <span className="activity-count">{data.running.total}</span>
        </h2>
        {data.running.runs.length === 0 ? (
          <p className="flows-note">Nothing is running.</p>
        ) : (
          <RunLines runs={data.running.runs} total={data.running.total} />
        )}
      </section>
      <section className="activity-card" aria-label="Failed in the last 7 days">
        <h2>
          Failed in the last 7 days <span className="activity-count">{data.failed.total}</span>
        </h2>
        {data.failed.runs.length === 0 ? (
          <p className="flows-note">No run failed.</p>
        ) : (
          <RunLines runs={data.failed.runs} total={data.failed.total} />
        )}
      </section>
    </div>
  )
}

function RunLines({ runs, total }: { runs: ActivityRun[]; total: number }) {
  return (
    <>
      <ul className="activity-runs">
        {runs.map((r) => (
          <li key={r.id}>
            <a href={`#/flows/${r.flow}/runs/${r.id}`}>
              <span className={`status-dot ${r.status}`} aria-hidden />
              <span className="activity-run-flow">{r.flowName}</span>
              <span className="activity-run-id">#{r.id}</span>
            </a>
            <span className="activity-run-when">
              {r.status === 'running' ? `for ${formatSeconds((Date.now() - Date.parse(r.startedAt)) / 1000)}` : formatTime(r.startedAt)}
              {startedBy(r) && ` · ${startedBy(r)}`}
            </span>
            {r.failedProcess && (
              <span className="activity-run-error" title={r.error}>
                {r.failedProcess}: {r.error || 'failed'}
              </span>
            )}
          </li>
        ))}
      </ul>
      {total > runs.length && <p className="flows-note">And {total - runs.length} more.</p>}
    </>
  )
}

const startedBy = (r: ActivityRun) =>
  r.rerunOf !== null
    ? `re-run of #${r.rerunOf}`
    : r.schedule !== null
      ? 'by a schedule'
      : r.apiKey
        ? `by API key ${r.apiKey}`
        : r.webhook !== null
          ? `by ${webhookLabel(r.webhook)}`
          : r.startedBy
          ? `by ${r.startedBy}`
          : ''

// The period in a few numbers.
function Totals({ data }: { data: ActivityData }) {
  const runs = sum(data.total.runs)
  const failed = sum(data.total.failed)
  const all = data.allReviews
  return (
    <div className="activity-totals">
      <Tile label="Runs" value={runs.toLocaleString()} />
      <Tile label="Failed" value={runs ? share(failed, runs) : '–'} note={`${failed.toLocaleString()} of them`} />
      <Tile label="Spent on models" value={formatDollars(sum(data.total.cost))} />
      <Tile
        label="Reviews decided"
        value={all.decided.toLocaleString()}
        note={
          [
            all.decided ? `half within ${formatSeconds(all.median)}, nine in ten within ${formatSeconds(all.p90)}` : '',
            all.timedOut ? `${all.timedOut.toLocaleString()} more timed out` : '',
          ]
            .filter(Boolean)
            .join('; ') || undefined
        }
      />
    </div>
  )
}

function Tile({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div className="activity-tile">
      <span className="activity-tile-label">{label}</span>
      <span className="activity-tile-value">{value}</span>
      {note && <span className="activity-tile-note">{note}</span>}
    </div>
  )
}

type Series = { label: string; color: string; values: number[] }

// Geometry of a day chart, in pixels: it is drawn at the width it has, so
// its labels keep their size however wide the page is, starting at W until
// it is measured.
const W = 720
const H = 200
const PAD = { top: 8, right: 8, bottom: 22, left: 44 }
const GAP = 2

// A bar a day, its series stacked from the baseline in the order given,
// with a readout above it of the day pointed at, or of the whole period.
function DayBars({
  days,
  series,
  format,
  readout,
}: {
  days: string[]
  series: Series[]
  format: (n: number) => string
  readout: (day: number) => string
}) {
  const [at, setAt] = useState<number | null>(null)
  const box = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(W)
  useEffect(() => {
    const el = box.current
    if (!el) return
    const observer = new ResizeObserver(([e]) => setWidth(Math.max(PAD.left + PAD.right + 100, Math.round(e.contentRect.width))))
    observer.observe(el)
    return () => observer.disconnect()
  }, [])
  const totals = days.map((_, i) => series.reduce((s, x) => s + x.values[i], 0))
  const top = niceMax(Math.max(0, ...totals))
  const innerW = width - PAD.left - PAD.right
  const innerH = H - PAD.top - PAD.bottom
  const band = innerW / days.length
  const barW = Math.max(1, Math.min(24, band * 0.7))
  const y = (v: number) => PAD.top + innerH - (v / top) * innerH
  const ticks = [0, top / 2, top]
  const labelled = labelledDays(days.length)

  return (
    <div className="day-bars" ref={box}>
      <p className="day-bars-readout" aria-live="polite">
        {at === null ? (
          <span className="flows-note">Point at a day to see it.</span>
        ) : (
          <>
            <strong>{formatDay(days[at])}</strong> · {readout(at)}
          </>
        )}
      </p>
      <svg viewBox={`0 0 ${width} ${H}`} role="img" aria-label={series.map((s) => s.label).join(' and ') + ' a day'} onMouseLeave={() => setAt(null)}>
        {ticks.map((t) => (
          <g key={t}>
            <line className="day-bars-grid" x1={PAD.left} x2={width - PAD.right} y1={y(t)} y2={y(t)} />
            <text className="day-bars-tick" x={PAD.left - 6} y={y(t)} dy="0.32em" textAnchor="end">
              {format(t)}
            </text>
          </g>
        ))}
        {days.map((d, i) => {
          const x = PAD.left + band * i + (band - barW) / 2
          let base = 0
          const segments: ReactNode[] = []
          series.forEach((s, k) => {
            const v = s.values[i]
            if (v <= 0) return
            const y0 = y(base) - (base > 0 ? GAP : 0)
            base += v
            const y1 = y(base)
            if (y0 - y1 < 0.5) return
            // Only the top of the stack gets rounded.
            const last = series.slice(k + 1).every((n) => n.values[i] <= 0)
            segments.push(<path key={s.label} d={barPath(x, y1, barW, y0 - y1, last ? 4 : 0)} fill={s.color} />)
          })
          return (
            <g key={d} className={at === i ? 'day-bars-day at' : 'day-bars-day'}>
              {segments}
              {labelled.has(i) && (
                <text className="day-bars-tick" x={PAD.left + band * i + band / 2} y={H - 6} textAnchor="middle">
                  {formatDay(d, true)}
                </text>
              )}
              <rect
                className="day-bars-hit"
                x={PAD.left + band * i}
                y={PAD.top}
                width={band}
                height={innerH}
                tabIndex={0}
                aria-label={`${formatDay(d)}: ${readout(i)}`}
                onMouseEnter={() => setAt(i)}
                onFocus={() => setAt(i)}
                onBlur={() => setAt(null)}
              />
            </g>
          )
        })}
        <line className="day-bars-axis" x1={PAD.left} x2={width - PAD.right} y1={y(0)} y2={y(0)} />
      </svg>
      {series.length > 1 && (
        <div className="day-bars-legend">
          {series.map((s) => (
            <span key={s.label}>
              <span className="day-bars-swatch" style={{ background: s.color }} aria-hidden />
              {s.label}
            </span>
          ))}
        </div>
      )}
    </div>
  )
}

// A bar from its top left, rounded at the top by r.
function barPath(x: number, y: number, w: number, h: number, r: number): string {
  const rr = Math.min(r, w / 2, h)
  if (rr <= 0) return `M${x},${y + h}V${y}H${x + w}V${y + h}Z`
  return `M${x},${y + h}V${y + rr}Q${x},${y} ${x + rr},${y}H${x + w - rr}Q${x + w},${y} ${x + w},${y + rr}V${y + h}Z`
}

// A round number at or above v for the top of a chart's scale.
function niceMax(v: number): number {
  if (v <= 0) return 1
  const step = 10 ** Math.floor(Math.log10(v))
  for (const m of [1, 2, 2.5, 5, 10]) if (m * step >= v) return m * step
  return 10 * step
}

// The days named under a chart: about six, the last among them.
function labelledDays(n: number): Set<number> {
  const every = Math.max(1, Math.ceil(n / 6))
  const out = new Set<number>()
  for (let i = n - 1; i >= 0; i -= every) out.add(i)
  return out
}

// A day of the period, YYYY-MM-DD, as the viewer reads dates.
function formatDay(day: string, short = false): string {
  return new Date(`${day}T00:00:00Z`).toLocaleDateString(undefined, short ? { month: 'short', day: 'numeric', timeZone: 'UTC' } : { weekday: 'short', month: 'short', day: 'numeric', timeZone: 'UTC' })
}

// A small bar a day for a row of a table, failures stacked under the rest.
function Spark({ values, failed, color }: { values: number[]; failed?: number[]; color: string }) {
  const w = 120
  const h = 24
  const top = Math.max(...values, 0) || 1
  const band = w / values.length
  const bw = Math.max(1, band - (band > 3 ? 1 : 0))
  return (
    <svg className="spark" viewBox={`0 0 ${w} ${h}`} width={w} height={h} aria-hidden>
      <line className="day-bars-axis" x1={0} x2={w} y1={h - 0.5} y2={h - 0.5} />
      {values.map((v, i) => {
        if (v <= 0) return null
        const total = (v / top) * (h - 1)
        const bad = failed ? (failed[i] / top) * (h - 1) : 0
        return (
          <g key={i}>
            {bad > 0 && <rect x={band * i} y={h - 1 - bad} width={bw} height={bad} fill={FAILED} />}
            {total - bad > 0 && <rect x={band * i} y={h - 1 - total} width={bw} height={total - bad} fill={color} />}
          </g>
        )
      })}
    </svg>
  )
}

type FlowSort = 'runs' | 'failed' | 'spent' | 'name'

// Each flow's runs a day, how many failed, and what they spent.
function Flows({ data }: { data: ActivityData }) {
  const [by, setBy] = useState<FlowSort>('runs')
  const [all, setAll] = useState(false)
  const rows = useMemo(() => {
    const key = (f: FlowActivity) =>
      by === 'runs' ? sum(f.runs) : by === 'failed' ? (sum(f.runs) ? sum(f.failed) / sum(f.runs) : 0) : by === 'spent' ? sum(f.cost) : 0
    return [...data.flows].sort((a, b) => (by === 'name' ? a.name.localeCompare(b.name) : key(b) - key(a) || a.name.localeCompare(b.name)))
  }, [data.flows, by])
  const shown = all ? rows : rows.slice(0, 15)
  const sortable = (k: FlowSort, label: string, className?: string) => (
    <th className={className} aria-sort={by === k ? (k === 'name' ? 'ascending' : 'descending') : undefined}>
      <button className={by === k ? 'activity-sort active' : 'activity-sort'} onClick={() => setBy(k)}>
        {label}
      </button>
    </th>
  )
  return (
    <section className="activity-card" aria-label="By flow">
      <h2>By flow</h2>
      {rows.length === 0 ? (
        <p className="flows-note">No flow ran in the period.</p>
      ) : (
        <div className="activity-table-wrap">
          <table className="activity-table">
            <thead>
              <tr>
                {sortable('name', 'Flow')}
                <th>Runs a day</th>
                {sortable('runs', 'Runs', 'num')}
                {sortable('failed', 'Failed', 'num')}
                <th>Spent a day</th>
                {sortable('spent', 'Spent', 'num')}
              </tr>
            </thead>
            <tbody>
              {shown.map((f) => {
                const runs = sum(f.runs)
                const failed = sum(f.failed)
                return (
                  <tr key={f.flow}>
                    <td>
                      <a href={`#/flows/${f.flow}`}>{f.name}</a>
                      {f.module && <span className="activity-kind">procedure</span>}
                    </td>
                    <td>
                      <Spark values={f.runs} failed={f.failed} color={RAN} />
                    </td>
                    <td className="num">{runs.toLocaleString()}</td>
                    <td className={failed > 0 ? 'num activity-bad' : 'num'}>{runs ? `${failed.toLocaleString()} · ${share(failed, runs)}` : '–'}</td>
                    <td>
                      <Spark values={f.cost} color={SPENT} />
                    </td>
                    <td className="num" title={f.unpriced > 0 ? unpricedNote(f.unpriced) : undefined}>
                      {formatDollars(sum(f.cost))}
                      {f.unpriced > 0 && '*'}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {rows.length > 15 && (
        <button className="activity-more" onClick={() => setAll(!all)}>
          {all ? 'Show the first 15' : `Show all ${rows.length} flows`}
        </button>
      )}
    </section>
  )
}

// The processes whose steps failed most.
function Processes({ data }: { data: ActivityData }) {
  return (
    <section className="activity-card" aria-label="Processes that fail most">
      <h2>Processes that fail most</h2>
      {data.processes.length === 0 ? (
        <p className="flows-note">No agent, script or connector step failed in the period.</p>
      ) : (
        <div className="activity-table-wrap">
          <table className="activity-table">
            <thead>
              <tr>
                <th>Process</th>
                <th>Flow</th>
                <th className="num">Failed</th>
                <th>Last went wrong</th>
              </tr>
            </thead>
            <tbody>
              {data.processes.map((p) => (
                <tr key={p.process}>
                  <td>{p.name}</td>
                  <td>
                    <a href={`#/flows/${p.flow}`}>{p.flowName}</a>
                  </td>
                  <td className="num activity-bad">
                    {p.failed.toLocaleString()} of {p.steps.toLocaleString()} · {share(p.failed, p.steps)}
                  </td>
                  <td className="activity-error-cell">
                    <a href={`#/flows/${p.flow}/runs/${p.lastRun}`} title={p.lastError}>
                      #{p.lastRun}
                    </a>{' '}
                    <span title={p.lastError}>{p.lastError || 'failed'}</span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}

// How long reviews waited for a person, and how often they asked for changes.
function Reviews({ data }: { data: ActivityData }) {
  return (
    <section className="activity-card" aria-label="Reviews">
      <h2>Reviews</h2>
      <p className="flows-note">
        The reviews asked for in the period that someone decided, from when each began waiting to when it was decided.
        Those that waited past their time limit with nobody deciding timed out, failing instead, and count apart. Changes
        asked are decisions that came with instructions, which go back to the work as a change request; the rounds are the
        times a review was decided in one run.
      </p>
      {data.reviews.length === 0 ? (
        <p className="flows-note">No review was decided, or timed out, in the period.</p>
      ) : (
        <div className="activity-table-wrap">
          <table className="activity-table">
            <thead>
              <tr>
                <th>Review</th>
                <th>Flow</th>
                <th className="num">Decided</th>
                <th className="num">Timed out</th>
                <th className="num">Half within</th>
                <th className="num">Nine in ten within</th>
                <th className="num">Longest</th>
                <th className="num">Changes asked</th>
                <th className="num">Rounds a run</th>
              </tr>
            </thead>
            <tbody>
              <ReviewRow r={data.allReviews} name="All reviews" />
              {data.reviews.map((r) => (
                <ReviewRow
                  key={r.process}
                  r={r}
                  name={r.name ?? ''}
                  flow={
                    <a href={`#/flows/${r.flow}`} className="activity-flow-link">
                      {r.flowName}
                    </a>
                  }
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}

function ReviewRow({ r, name, flow }: { r: ReviewTimes; name: string; flow?: ReactNode }) {
  return (
    <tr className={flow ? undefined : 'activity-all'}>
      <td>{name}</td>
      <td>{flow}</td>
      <td className="num">{r.decided.toLocaleString()}</td>
      <td className="num">{r.timedOut ? r.timedOut.toLocaleString() : '–'}</td>
      {/* Only decisions took a time to decide. */}
      <td className="num">{r.decided ? formatSeconds(r.median) : '–'}</td>
      <td className="num">{r.decided ? formatSeconds(r.p90) : '–'}</td>
      <td className="num">{r.decided ? formatSeconds(r.longest) : '–'}</td>
      <td className="num">
        {r.changes.toLocaleString()} · {share(r.changes, r.decided)}
      </td>
      <td className="num">
        {r.runs ? (r.decided / r.runs).toFixed(1) : '–'} · at most {r.mostRounds}
      </td>
    </tr>
  )
}

const sum = (xs: number[]) => xs.reduce((s, x) => s + x, 0)

const share = (n: number, of: number) => (of ? `${Math.round((100 * n) / of)}%` : '–')

const runsText = (runs: number, failed: number) =>
  runs === 0 ? 'no runs' : `${runs.toLocaleString()} ${runs === 1 ? 'run' : 'runs'}, ${failed.toLocaleString()} failed (${share(failed, runs)})`

const unpricedNote = (n: number) =>
  `${n.toLocaleString()} ${n === 1 ? 'call was' : 'calls were'} to models without a known price, counted at the largest models’ price.`

// Dollars to the cent, or closer under a dime; none as $0.
function formatDollars(d: number): string {
  if (d === 0) return '$0'
  if (d < 0.1) return '$' + d.toFixed(4)
  return '$' + d.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}

// A while, as long as it takes to read.
function formatSeconds(s: number): string {
  s = Math.max(0, Math.round(s))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ${m % 60}m`
  return `${Math.floor(h / 24)}d ${h % 24}h`
}
