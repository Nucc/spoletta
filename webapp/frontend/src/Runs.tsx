import { useEffect, useRef, useState } from 'react'
import { Data } from './Data'
import {
  fileUrl,
  getStepMessages,
  type AgentMessage,
  type FlowRun,
  type FlowRunDetail,
  type FlowRunStatus,
  type ProcessKind,
  type RunStep,
  type StepMessage,
  type StepStatus,
} from './api'

// How often an open conversation of a step still going is refreshed.
const CONVERSATION_REFRESH_MS = 1500

export const RUN_LABELS: Record<FlowRunStatus, string> = {
  running: 'Running',
  done: 'Done',
  failed: 'Failed',
  interrupted: 'Interrupted',
  terminated: 'Terminated',
}

const STEP_LABELS: Record<StepStatus, string> = {
  active: 'Running',
  waiting: 'Waiting for review',
  done: 'Done',
  error: 'Failed',
  duplicate: 'Already processed',
}

export const formatTime = (s: string) =>
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

// Every run of the flow, newest first, each with the runs of its recording
// flow processes under it, such as one per item of a list, in the order they
// started.
export function RunList({
  runs,
  selected,
  onSelect,
}: {
  runs: FlowRun[]
  selected: number | null
  onSelect: (id: number) => void
}) {
  const ids = new Set(runs.map((r) => r.id))
  const children = new Map<number, FlowRun[]>()
  for (const r of [...runs].reverse()) {
    if (r.parent !== null && ids.has(r.parent)) children.set(r.parent, [...(children.get(r.parent) ?? []), r])
  }
  // A run whose parent fell off the list shows on its own.
  const top = runs.filter((r) => r.parent === null || !ids.has(r.parent))

  const item = (r: FlowRun, nested: boolean) => (
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
        {r.itemKey && (
          <span className="run-item-key" title={r.itemKey}>
            {r.itemKey}
          </span>
        )}
        {r.parent !== null && (
          <span className="run-item-process">
            {r.processName}
            {!nested && ` · in run #${r.parent}`}
          </span>
        )}
        <time dateTime={r.startedAt}>
          {r.version !== null && `v${r.version} · `}
          {formatTime(r.startedAt)}
          {r.finishedAt && ` · ${formatDuration(r.startedAt, r.finishedAt)}`}
        </time>
      </button>
      {(children.get(r.id)?.length ?? 0) > 0 && <ol className="run-list nested">{children.get(r.id)!.map((c) => item(c, true))}</ol>}
    </li>
  )

  return runs.length === 0 ? (
    <p className="panel-note">No runs yet. Press play to run the flow.</p>
  ) : (
    <ol className="run-list">{top.map((r) => item(r, false))}</ol>
  )
}

// What happened in a run: each process execution in the order it started.
export function RunPanel({
  run,
  onStop,
  onStopStep,
  onOpenRun,
  onClose,
}: {
  run: FlowRunDetail
  onStop: () => void
  onStopStep: (id: number) => void
  // Opens another run: the one a recording flow process ran as, or the one it ran in.
  onOpenRun: (id: number) => void
  onClose: () => void
}) {
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
      {run.parent !== null && (
        <p className="panel-note">
          The run of {run.processName} in{' '}
          <button className="link-button" onClick={() => onOpenRun(run.parent!)}>
            run #{run.parent}
          </button>
          .
        </p>
      )}
      <p className="panel-note">
        Started {formatTime(run.startedAt)}
        {run.finishedAt && `, took ${formatDuration(run.startedAt, run.finishedAt)}`}
      </p>
      {run.status === 'running' && (
        <button className="run-stop" onClick={onStop} title="End the run where it is; steps still going are cancelled">
          Stop run
        </button>
      )}
      <p className="panel-note">
        {run.version === null
          ? 'Ran before versions were kept, so the board shows it as the board is now.'
          : run.status === 'running'
            ? `Running on version ${run.version}.`
            : `Ran on version ${run.version}, which the board shows until the run closes.`}
      </p>
      {run.steps.length === 0 ? (
        <p className="panel-note">No process has run yet.</p>
      ) : (
        <ol className="steps">
          {run.steps.map((s) => (
            <li key={s.id}>
              <Step
                run={run.id}
                step={s}
                objects={run.objects.filter((o) => o.step === s.id)}
                onStop={stoppable(s) ? () => onStopStep(s.id) : undefined}
                onOpenRun={onOpenRun}
              />
            </li>
          ))}
        </ol>
      )}
    </aside>
  )
}

// Agent, script and Sentry steps can be stopped while they go.
export const stoppable = (s: { kind: ProcessKind; status: StepStatus }) =>
  s.status === 'active' && (s.kind === 'agent' || s.kind === 'script' || s.kind === 'sentry')

function Step({
  run,
  step: s,
  objects,
  onStop,
  onOpenRun,
}: {
  run: number
  step: RunStep
  objects: FlowRunDetail['objects']
  onStop?: () => void
  onOpenRun: (id: number) => void
}) {
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
        {onStop && (
          <button className="run-stop" onClick={onStop} title="Stop this step; its branch ends, the rest of the run goes on">
            Stop step
          </button>
        )}
        {s.kind === 'agent' && (
          <p className="panel-note">
            Model: {s.model || 'default'} · thinking: {s.thinking || 'default'}
          </p>
        )}
        {s.branch && (
          <p className="panel-note">
            Worked on the branch <code>{s.branch}</code>
          </p>
        )}
        {s.childRun !== null && (
          <p className="panel-note">
            Ran its board as{' '}
            <button className="link-button" onClick={() => onOpenRun(s.childRun!)}>
              run #{s.childRun}
            </button>
            .
          </p>
        )}
        {s.inputs.map((input, i) => (
          <Block key={i} label={`Input: ${input.name}${input.state ? ` (${input.state})` : ''}`} text={input.content} data />
        ))}
        {s.changeRequest && (
          <>
            {s.changeRequest.reviewed.map((input, i) => (
              <Block key={i} label={`Reviewed: ${input.name}`} text={input.content} data />
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
            <summary>
              {s.kind === 'script' ? 'Script it ran' : s.kind === 'sentry' ? 'Request sent to Sentry' : 'Prompt sent to the agent'}
            </summary>
            <pre>{s.prompt}</pre>
          </details>
        )}
        {s.kind === 'agent' && s.prompt && <Conversation run={run} step={s} />}
        {s.kind === 'human' && s.outcome && (
          <Block label="Decision" text={decisionText(s.outcome, objects[0]?.name, s.instructions)} />
        )}
        {s.error && <Block label={s.status === 'duplicate' ? 'Duplicate' : 'Error'} text={s.error} failed={s.status !== 'duplicate'} />}
        {s.kind !== 'human' && s.kind !== 'flow' && (s.status === 'done' || s.status === 'duplicate') && (
          <Block label="Output" text={s.output || '(empty)'} data />
        )}
        {s.kind !== 'human' && objects.length > 0 && (
          <p className="panel-note">
            Generated {objects.map((o) => (o.state ? `${o.name} (${o.state})` : o.name)).join(', ')}
          </p>
        )}
        {s.kind !== 'human' && objects.some((o) => o.files.length > 0) && (
          <div className="panel-run">
            <span>Files</span>
            <ul className="step-files">
              {[...new Set(objects.flatMap((o) => o.files))].map((f) => (
                <li key={f}>
                  <a href={fileUrl(f)} target="_blank" rel="noreferrer">
                    {f.split('/').pop()}
                  </a>
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </details>
  )
}

// What the agent did: its conversation with the model, loaded once opened,
// and followed while the step goes. Once the step ended, it loads once more
// for the messages that came in last.
function Conversation({ run, step }: { run: number; step: RunStep }) {
  const [open, setOpen] = useState(false)
  const [messages, setMessages] = useState<StepMessage[]>([])
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState('')
  const last = useRef(0)
  const going = step.status === 'active'

  useEffect(() => {
    if (!open) return
    let stopped = false
    let timer: number | undefined
    const load = async () => {
      try {
        const more = await getStepMessages(run, step.id, last.current)
        if (stopped) return
        if (more.length > 0) {
          last.current = more[more.length - 1].id
          setMessages((ms) => [...ms, ...more])
        }
        setLoaded(true)
        setError('')
      } catch (err) {
        if (stopped) return
        setError(err instanceof Error ? err.message : String(err))
      }
      if (going) timer = window.setTimeout(load, CONVERSATION_REFRESH_MS)
    }
    load()
    return () => {
      stopped = true
      window.clearTimeout(timer)
    }
  }, [open, going, run, step.id])

  return (
    <details className="step-prompt conversation" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary>What the agent did</summary>
      {error && <Block label="The conversation couldn't load" text={error} failed />}
      {loaded && messages.length === 0 && (
        <p className="panel-note">{going ? 'Nothing yet.' : 'No conversation was recorded for this step.'}</p>
      )}
      <ol className="messages">
        {messages.map((m, i) => (
          <li key={m.id}>
            {m.attempt > 1 && messages[i - 1]?.attempt !== m.attempt && (
              <p className="panel-note">Try {m.attempt}: the agent didn't hand in its output, so it was asked again.</p>
            )}
            <Message message={m.message} />
          </li>
        ))}
      </ol>
      {going && loaded && <p className="panel-note">Still going…</p>}
    </details>
  )
}

const blocks = (m: AgentMessage) =>
  typeof m.content === 'string' ? [{ type: 'text' as const, text: m.content }] : (m.content ?? [])

const textOf = (m: AgentMessage) =>
  blocks(m)
    .map((b) => (b.type === 'text' ? b.text : b.type === 'image' ? `(${b.mimeType} image)` : ''))
    .filter(Boolean)
    .join('\n')

function Message({ message: m }: { message: AgentMessage }) {
  if (m.omitted !== undefined) {
    return <p className="panel-note">A {m.role} message of {Math.round(m.omitted / 1024)} KB, too large to keep.</p>
  }
  switch (m.role) {
    case 'system':
      return (
        <details className="message-fold">
          <summary>System prompt</summary>
          <pre>{m.sections ? Object.values(m.sections).filter(Boolean).join('\n\n') : textOf(m)}</pre>
        </details>
      )
    case 'user':
      return (
        <details className="message-fold">
          <summary>Prompt</summary>
          <pre>{textOf(m)}</pre>
        </details>
      )
    case 'toolResult':
      return (
        <Block label={`${m.toolName ?? 'Tool'} ${m.isError ? 'failed' : 'returned'}`} text={textOf(m) || '(nothing)'} failed={m.isError} data={!m.isError} />
      )
    case 'assistant':
      return (
        <>
          {blocks(m).map((b, i) =>
            b.type === 'thinking' ? (
              <details key={i} className="message-fold">
                <summary>Thinking</summary>
                <pre>{b.redacted && !b.thinking ? '(redacted)' : b.thinking}</pre>
              </details>
            ) : b.type === 'toolCall' ? (
              <Block key={i} label={`Calls ${b.name}`} text={JSON.stringify(b.arguments, null, 2)} data />
            ) : b.type === 'text' ? (
              <Block key={i} label="Says" text={b.text} />
            ) : null,
          )}
          {(m.stopReason === 'error' || m.stopReason === 'aborted') && (
            <Block label={m.stopReason === 'error' ? 'The request failed' : 'The request was aborted'} text={m.errorMessage ?? ''} failed />
          )}
        </>
      )
    default:
      return <Block label={m.role} text={textOf(m)} />
  }
}

// A labelled text; data shows the fields of JSON content instead of the JSON.
function Block({ label, text, failed, data }: { label: string; text: string; failed?: boolean; data?: boolean }) {
  return (
    <div className="panel-run">
      <span>{label}</span>
      {data ? <Data text={text} /> : <pre className={failed ? 'failed' : undefined}>{text}</pre>}
    </div>
  )
}
