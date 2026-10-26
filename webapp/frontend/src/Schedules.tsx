import { useCallback, useEffect, useState, type FormEvent, type ReactNode } from 'react'
import {
  createSchedule,
  deleteSchedule,
  listFlows,
  listSchedules,
  previewSchedule,
  updateSchedule,
  type Flow,
  type Schedule,
  type ScheduleFields,
} from './api'
import { ClockIcon, FlowBuilderIcon, PencilIcon, PlusIcon, TrashIcon } from './icons'
import { formatTime, RUN_LABELS } from './Runs'
import { useConfirm } from './Confirm'

// The common times, offered before writing a cron expression by hand.
const PRESETS: { cron: string; label: string }[] = [
  { cron: '* * * * *', label: 'Every minute' },
  { cron: '*/5 * * * *', label: 'Every 5 minutes' },
  { cron: '*/15 * * * *', label: 'Every 15 minutes' },
  { cron: '0 * * * *', label: 'Every hour' },
  { cron: '0 9 * * *', label: 'Every day at 9:00' },
  { cron: '0 9 * * 1-5', label: 'Weekdays at 9:00' },
  { cron: '0 9 * * 1', label: 'Mondays at 9:00' },
]

const CUSTOM = ''

const presetLabel = (cron: string) => PRESETS.find((p) => p.cron === cron.trim())?.label

const localZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'

const ZONES: string[] = (() => {
  try {
    return Intl.supportedValuesOf('timeZone')
  } catch {
    return []
  }
})()

// How often the list catches up with the runs the schedules start.
const REFRESH_MS = 5000

// Every schedule, each running a flow on its own when its cron expression
// comes round: the version the flow published, or its board as it is.
export default function Schedules() {
  const [schedules, setSchedules] = useState<Schedule[] | null>(null)
  const [flows, setFlows] = useState<Flow[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<number | null>(null)
  const [confirm, confirmDialog] = useConfirm()

  const report = useCallback((err: unknown) => setError(err instanceof Error ? err.message : String(err)), [])
  const refresh = useCallback(() => listSchedules().then(setSchedules).catch(report), [report])

  useEffect(() => {
    document.title = 'Schedules · Flows'
    listFlows().then(setFlows).catch(report)
    refresh()
    const t = window.setInterval(refresh, REFRESH_MS)
    return () => window.clearInterval(t)
  }, [refresh, report])

  // Structs hold an object, not processes to run.
  const runnable = flows?.filter((f) => !f.struct) ?? null

  const onCreate = async (s: ScheduleFields) => {
    await createSchedule(s)
    await refresh()
  }

  const onUpdate = async (id: number, patch: Parameters<typeof updateSchedule>[1]) => {
    const updated = await updateSchedule(id, patch)
    setSchedules((ss) => ss?.map((s) => (s.id === id ? updated : s)) ?? null)
  }

  const onDelete = async (s: Schedule) => {
    const yes = await confirm({
      title: `Delete the schedule running ${s.flowName}?`,
      message: <p>Its runs stay. This can't be undone.</p>,
      confirm: 'Delete',
    })
    if (!yes) return
    try {
      await deleteSchedule(s.id)
      await refresh()
    } catch (err) {
      report(err)
    }
  }

  return (
    <div className="flows-page">
      <header className="toolbar">
        <h1>
          <a className="home" href="#/" title="All flows">
            <FlowBuilderIcon /> Flows
          </a>
          <span className="crumb-separator" aria-hidden>
            /
          </span>
          Schedules
        </h1>
      </header>
      {error && (
        <div className="error-banner" role="alert" onClick={() => setError(null)}>
          {error} (click to dismiss)
        </div>
      )}
      <main className="flows">
        <section className="flow-section" aria-label="Schedules">
          <h2>
            <ClockIcon /> Schedules
          </h2>
          <p className="flows-note">
            Each runs a flow on its own whenever its time comes round: the version the flow published, or its board as it
            is while it publishes none, so publish a flow to go on editing it without a schedule running a half-made board.
            While its last run is still going, such as one waiting on a person, the times that come round are skipped.
          </p>
          {runnable && (
            <ScheduleForm
              flows={runnable}
              initial={{ flow: runnable[0]?.id ?? 0, cron: PRESETS[0].cron, timezone: localZone() }}
              submitLabel={
                <>
                  <PlusIcon /> New schedule
                </>
              }
              onSubmit={onCreate}
              onError={report}
            />
          )}
          {schedules === null ? (
            <p className="flows-note">Loading…</p>
          ) : schedules.length === 0 ? (
            <p className="flows-note">No schedules yet. Pick a flow and when it should run above.</p>
          ) : (
            <ul className="flow-list schedule-list">
              {schedules.map((s) =>
                editing === s.id && runnable ? (
                  <li key={s.id} className="schedule-editing">
                    <ScheduleForm
                      flows={runnable}
                      initial={s}
                      submitLabel="Save"
                      onSubmit={async (fields) => {
                        await onUpdate(s.id, fields)
                        setEditing(null)
                      }}
                      onCancel={() => setEditing(null)}
                      onError={report}
                    />
                  </li>
                ) : (
                  <ScheduleRow
                    key={s.id}
                    schedule={s}
                    onToggle={() => onUpdate(s.id, { enabled: !s.enabled }).catch(report)}
                    onEdit={() => setEditing(s.id)}
                    onDelete={() => onDelete(s)}
                  />
                ),
              )}
            </ul>
          )}
        </section>
      </main>
      {confirmDialog}
    </div>
  )
}

function ScheduleRow({
  schedule: s,
  onToggle,
  onEdit,
  onDelete,
}: {
  schedule: Schedule
  onToggle: () => void
  onEdit: () => void
  onDelete: () => void
}) {
  const running = s.lastRun?.status === 'running'
  return (
    <li className={s.enabled ? undefined : 'disabled'}>
      <div className="flow-card">
        <span className="flow-card-name">
          <a href={`#/flows/${s.flow}`}>{s.flowName}</a>
        </span>
        <span className="schedule-when">
          {presetLabel(s.cron) ?? 'Custom'} · <code>{s.cron}</code> · {s.timezone}
        </span>
        <span className="flow-card-run">
          {s.enabled && s.nextRunAt ? `Next ${formatTime(s.nextRunAt)}` : 'Disabled'}
          {s.lastRun && (
            <>
              {' · '}
              <a className="schedule-last-run" href={`#/flows/${s.lastRun.flow}/runs/${s.lastRun.id}`}>
                <span className={`status-dot ${s.lastRun.status}`} aria-hidden />
                Last run {RUN_LABELS[s.lastRun.status].toLowerCase()} · {formatTime(s.lastRun.startedAt)}
              </a>
            </>
          )}
        </span>
        {running && s.enabled && <span className="schedule-skipping">Skipping its times until this run ends.</span>}
      </div>
      <label className="schedule-toggle" title={s.enabled ? 'Disable' : 'Enable'}>
        <input type="checkbox" checked={s.enabled} onChange={onToggle} aria-label={`Run ${s.flowName} on schedule`} />
        <span aria-hidden />
      </label>
      <button className="icon-button" onClick={onEdit} aria-label={`Edit the schedule of ${s.flowName}`} title="Edit">
        <PencilIcon />
      </button>
      <button
        className="icon-button flow-delete"
        onClick={onDelete}
        aria-label={`Delete the schedule of ${s.flowName}`}
        title="Delete schedule"
      >
        <TrashIcon />
      </button>
    </li>
  )
}

// Which flow runs, and when, with the next times it gives shown as they are
// typed.
function ScheduleForm({
  flows,
  initial,
  submitLabel,
  onSubmit,
  onCancel,
  onError,
}: {
  flows: Flow[]
  initial: ScheduleFields
  submitLabel: ReactNode
  onSubmit: (s: ScheduleFields) => Promise<void>
  onCancel?: () => void
  onError: (err: unknown) => void
}) {
  const [flow, setFlow] = useState(initial.flow)
  const [cron, setCron] = useState(initial.cron)
  const [timezone, setTimezone] = useState(initial.timezone)
  const [preview, setPreview] = useState<{ upcoming: string[] } | { problem: string } | null>(null)
  const [busy, setBusy] = useState(false)

  // The flows may load after the form, or the picked one go away.
  useEffect(() => {
    if (!flows.some((f) => f.id === flow) && flows.length > 0) setFlow(flows[0].id)
  }, [flows, flow])

  useEffect(() => {
    if (!cron.trim() || !timezone.trim()) {
      setPreview(null)
      return
    }
    let current = true
    const t = window.setTimeout(() => {
      previewSchedule(cron.trim(), timezone.trim())
        .then((p) => current && setPreview(p))
        .catch(onError)
    }, 250)
    return () => {
      current = false
      window.clearTimeout(t)
    }
  }, [cron, timezone, onError])

  const preset = presetLabel(cron) ? cron.trim() : CUSTOM
  const valid = preview !== null && 'upcoming' in preview && flows.some((f) => f.id === flow)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!valid) return
    setBusy(true)
    try {
      await onSubmit({ flow, cron: cron.trim(), timezone: timezone.trim() })
    } catch (err) {
      onError(err)
    } finally {
      setBusy(false)
    }
  }

  if (flows.length === 0) return <p className="flows-note">No flows to schedule yet; make one on the main page first.</p>

  return (
    <form className="schedule-form" onSubmit={submit}>
      <div className="schedule-fields">
        <label>
          Flow
          <select value={flow} onChange={(e) => setFlow(Number(e.target.value))}>
            {flows.map((f) => (
              <option key={f.id} value={f.id}>
                {f.name}
                {f.module ? ' (procedure)' : ''}
              </option>
            ))}
          </select>
        </label>
        <label>
          When
          <select value={preset} onChange={(e) => e.target.value !== CUSTOM && setCron(e.target.value)}>
            {PRESETS.map((p) => (
              <option key={p.cron} value={p.cron}>
                {p.label}
              </option>
            ))}
            <option value={CUSTOM}>Custom</option>
          </select>
        </label>
        <label>
          Cron
          <input
            className="schedule-cron"
            value={cron}
            onChange={(e) => setCron(e.target.value)}
            placeholder="minute hour day month weekday"
            spellCheck={false}
          />
        </label>
        <label>
          Time zone
          <input value={timezone} onChange={(e) => setTimezone(e.target.value)} list="schedule-zones" spellCheck={false} />
        </label>
      </div>
      <datalist id="schedule-zones">
        {ZONES.map((z) => (
          <option key={z} value={z} />
        ))}
      </datalist>
      <div className="schedule-form-footer">
        <p className={`schedule-preview${preview && 'problem' in preview ? ' problem' : ''}`} aria-live="polite">
          {preview === null
            ? ' '
            : 'problem' in preview
              ? preview.problem
              : `Next: ${preview.upcoming.slice(0, 3).map(formatTime).join(' · ')}`}
        </p>
        {onCancel && (
          <button type="button" className="secondary" onClick={onCancel}>
            Cancel
          </button>
        )}
        <button type="submit" disabled={busy || !valid}>
          {submitLabel}
        </button>
      </div>
    </form>
  )
}
