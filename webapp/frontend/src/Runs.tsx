import type { FlowRun, FlowRunDetail, FlowRunStatus, RunStep, StepStatus } from './api'

const RUN_LABELS: Record<FlowRunStatus, string> = {
  running: 'Running',
  done: 'Done',
  failed: 'Failed',
  interrupted: 'Interrupted',
}

const STEP_LABELS: Record<StepStatus, string> = {
  active: 'Running',
  waiting: 'Waiting for review',
  done: 'Done',
  error: 'Failed',
}

const formatTime = (s: string) =>
  new Date(s).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' })

function formatDuration(start: string, end: string | null): string {
  if (!end) return ''
  const s = Math.max(0, Math.round((Date.parse(end) - Date.parse(start)) / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  return m < 60 ? `${m}m ${s % 60}s` : `${Math.floor(m / 60)}h ${m % 60}m`
}

// What a human decided: the state they set the object to, with their
// instructions. Steps from before states hold approved or changes, and no object.
export function decisionText(outcome: string, object: string | undefined, instructions: string): string {
  const legacy: Record<string, string> = { approved: 'Approved', changes: 'Changes requested' }
  const head = object ? `Set ${object} to ${outcome}` : (legacy[outcome] ?? outcome)
  return instructions ? `${head}:\n${instructions}` : head
}

// Every run of the flow, newest first.
export function RunList({
  runs,
  selected,
  onSelect,
}: {
  runs: FlowRun[]
  selected: number | null
  onSelect: (id: number) => void
}) {
  return (
    <aside className="runs" aria-label="Runs">
      <h2>Runs</h2>
      {runs.length === 0 ? (
        <p className="panel-note">No runs yet. Press play to run the flow.</p>
      ) : (
        <ol className="run-list">
          {runs.map((r) => (
            <li key={r.id}>
              <button
                className={`run-item${selected === r.id ? ' selected' : ''}`}
                aria-current={selected === r.id}
                onClick={() => onSelect(r.id)}
              >
                <span className="run-item-title">
                  <span className={`status-dot ${r.status}`} aria-hidden="true" />
                  Run #{r.id}
                  <span className="run-item-status">{RUN_LABELS[r.status]}</span>
                </span>
                <time dateTime={r.startedAt}>
                  {formatTime(r.startedAt)}
                  {r.finishedAt && ` · ${formatDuration(r.startedAt, r.finishedAt)}`}
                </time>
              </button>
            </li>
          ))}
        </ol>
      )}
    </aside>
  )
}

// What happened in a run: each process execution in the order it started.
export function RunPanel({ run, onClose }: { run: FlowRunDetail; onClose: () => void }) {
  return (
    <aside className="panel run-panel" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <header className="panel-header">
        <h2>
          Run #{run.id} <span className={`run-badge ${run.status}`}>{RUN_LABELS[run.status]}</span>
        </h2>
        <button className="panel-close" onClick={onClose} aria-label="Close run">
          ×
        </button>
      </header>
      <p className="panel-note">
        Started {formatTime(run.startedAt)}
        {run.finishedAt && `, took ${formatDuration(run.startedAt, run.finishedAt)}`}
      </p>
      {run.steps.length === 0 ? (
        <p className="panel-note">No process has run yet.</p>
      ) : (
        <ol className="steps">
          {run.steps.map((s) => (
            <li key={s.id}>
              <Step step={s} objects={run.objects.filter((o) => o.step === s.id)} />
            </li>
          ))}
        </ol>
      )}
    </aside>
  )
}

function Step({ step: s, objects }: { step: RunStep; objects: FlowRunDetail['objects'] }) {
  return (
    <details className="step" open={s.status !== 'done'}>
      <summary>
        <span className={`status-dot ${s.status}`} aria-hidden="true" />
        <span className="step-name">{s.processName}</span>
        <span className={`process-kind ${s.kind}`}>{s.kind}</span>
        <span className="step-meta">{s.finishedAt ? formatDuration(s.startedAt, s.finishedAt) : STEP_LABELS[s.status]}</span>
      </summary>
      <div className="step-body">
        <p className="panel-note">
          {STEP_LABELS[s.status]} · started {formatTime(s.startedAt)}
          {s.process === null && ' · process since deleted'}
        </p>
        {s.kind === 'agent' && (
          <p className="panel-note">
            Model: {s.model || 'default'} · thinking: {s.thinking || 'default'}
          </p>
        )}
        {s.inputs.map((input, i) => (
          <Block key={i} label={`Input: ${input.name}${input.state ? ` (${input.state})` : ''}`} text={input.content} />
        ))}
        {s.changeRequest && (
          <>
            {s.changeRequest.reviewed.map((input, i) => (
              <Block key={i} label={`Reviewed: ${input.name}`} text={input.content} />
            ))}
            <Block
              label={
                s.changeRequest.state
                  ? `Marked ${s.changeRequest.state} by ${s.changeRequest.reviewer}`
                  : `Changes requested by ${s.changeRequest.reviewer}`
              }
              text={s.changeRequest.instructions}
            />
          </>
        )}
        {s.prompt && (
          <details className="step-prompt">
            <summary>Prompt sent to the agent</summary>
            <pre>{s.prompt}</pre>
          </details>
        )}
        {s.kind === 'human' && s.outcome && (
          <Block label="Decision" text={decisionText(s.outcome, objects[0]?.name, s.instructions)} />
        )}
        {s.error && <Block label="Error" text={s.error} failed />}
        {s.kind !== 'human' && s.status === 'done' && <Block label="Output" text={s.output || '(empty)'} />}
        {s.kind !== 'human' && objects.length > 0 && (
          <p className="panel-note">
            Generated {objects.map((o) => (o.state ? `${o.name} (${o.state})` : o.name)).join(', ')}
          </p>
        )}
      </div>
    </details>
  )
}

function Block({ label, text, failed }: { label: string; text: string; failed?: boolean }) {
  return (
    <div className="panel-run">
      <span>{label}</span>
      <pre className={failed ? 'failed' : undefined}>{text}</pre>
    </div>
  )
}
