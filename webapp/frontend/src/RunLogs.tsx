import { useEffect, useRef, useState } from 'react'
import { getRunLogs, webhookLabel, type FlowRunDetail, type RunLogLine } from './api'
import { formatTime, RUN_LABELS } from './Runs'

// How often the log of a run still going is fetched again.
const LOGS_REFRESH_MS = 1500

// The most lines the webapp gives at once; a full page means there are more.
const LOGS_PAGE = 5000

const formatLogTime = (s: string) =>
  new Date(s).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3 })

// A run's log in a large window in the middle of the screen: a line for
// each thing that happened, the webapp's and the worker's, with what went
// wrong in red. While the run goes on it fetches the new lines and, if it is
// scrolled to the end, keeps the newest in view. Errors only shows just
// those, and a step's name shows which step a line is about.
export default function RunLogs({ run, onClose }: { run: FlowRunDetail; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null)
  const body = useRef<HTMLDivElement>(null)
  const [lines, setLines] = useState<RunLogLine[] | null>(null)
  const [errorsOnly, setErrorsOnly] = useState(false)
  const [error, setError] = useState('')
  // Whether the newest line is in view, so new ones keep it there.
  const atEnd = useRef(true)
  const running = run.status === 'running'
  const stepNames = new Map(run.steps.map((s) => [s.id, s.processName]))

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])

  // The id of the last line fetched, so each fetch gets only newer ones.
  const last = useRef(0)

  // The lines so far, then while the run goes on those past the last.
  useEffect(() => {
    let stale = false
    let timer: ReturnType<typeof setTimeout> | undefined
    let full = false
    const fetchMore = () => {
      getRunLogs(run.id, last.current)
        .then((more) => {
          if (stale) return
          setError('')
          full = more.length >= LOGS_PAGE
          const fresh = more.filter((l) => l.id > last.current)
          if (fresh.length > 0) last.current = fresh[fresh.length - 1].id
          setLines((ls) => (fresh.length === 0 && ls ? ls : [...(ls ?? []), ...fresh]))
        })
        .catch((err) => !stale && setError(err instanceof Error ? err.message : String(err)))
        .finally(() => {
          if (!stale && (full || running)) timer = setTimeout(fetchMore, full ? 0 : LOGS_REFRESH_MS)
        })
    }
    fetchMore()
    return () => {
      stale = true
      clearTimeout(timer)
    }
    // A run that ended gets its last lines once more as it does.
  }, [run.id, running])

  useEffect(() => {
    if (atEnd.current && body.current) body.current.scrollTop = body.current.scrollHeight
  }, [lines, errorsOnly])

  const shown = (lines ?? []).filter((l) => !errorsOnly || l.level === 'error')
  const errors = (lines ?? []).filter((l) => l.level === 'error').length

  return (
    <dialog
      ref={ref}
      className="review-dialog run-logs"
      aria-label={`Logs of run #${run.id}`}
      onClose={onClose}
      // Escape closes the dialog, not the run's panel it opened from.
      onKeyDown={(e) => e.stopPropagation()}
    >
      <header className="panel-header">
        <h2>
          Logs of run #{run.id}
          {running && <span className="run-logs-live"> · following the run</span>}
        </h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <p className="panel-note">
        {RUN_LABELS[run.status]} · started {formatTime(run.startedAt)}
        {run.startedBy && ` by ${run.startedBy}`}
        {run.apiKey && ` through the API key ${run.apiKey}`}
        {run.webhook !== null && ` for a delivery to ${webhookLabel(run.webhook)}`}
        {run.schedule !== null && ' by a schedule'}
        {run.finishedAt && ` · finished ${formatTime(run.finishedAt)}`}
      </p>
      <div className="run-logs-tools">
        <label>
          <input type="checkbox" checked={errorsOnly} onChange={(e) => setErrorsOnly(e.target.checked)} /> Errors only
          {errors > 0 && ` (${errors})`}
        </label>
        <span className="panel-note">
          {lines === null ? 'Loading…' : `${lines.length} lines`}
        </span>
      </div>
      {error && <p className="panel-warning">{error}</p>}
      <div
        ref={body}
        className="run-logs-body"
        onScroll={(e) => {
          const el = e.currentTarget
          atEnd.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
        }}
      >
        {lines !== null && shown.length === 0 && (
          <p className="panel-note">{errorsOnly ? 'Nothing went wrong.' : 'Nothing logged; runs from before logs were kept have none.'}</p>
        )}
        {shown.map((l) => (
          <div key={l.id} className={`run-log-line ${l.level} ${l.source}`}>
            <time dateTime={l.time} title={new Date(l.time).toLocaleString()}>
              {formatLogTime(l.time)}
            </time>
            <span className="run-log-source">{l.source}</span>
            <span className="run-log-step" title={l.step !== null ? `Step ${l.step}` : undefined}>
              {l.step !== null ? (stepNames.get(l.step) ?? `step ${l.step}`) : 'run'}
            </span>
            <span className="run-log-message">{l.message}</span>
          </div>
        ))}
      </div>
    </dialog>
  )
}
