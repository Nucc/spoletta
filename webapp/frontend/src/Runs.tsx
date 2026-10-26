import { useEffect, useRef, useState, type FormEvent } from 'react'
import { createPortal } from 'react-dom'
import { Data } from './Data'
import {
  fileUrl,
  getStepMessages,
  sendFollowUp,
  stopFollowUp,
  webhookLabel,
  type AgentMessage,
  type FollowUp,
  type FlowRun,
  type FlowRunDetail,
  type FlowRunStatus,
  type ProcessKind,
  type RepoCommits,
  type RunFilter,
  type RunOrigin,
  type RunStanding,
  type RunStep,
  type StepMessage,
  type StepStatus,
  type StepUsage,
} from './api'
import { ClockIcon, TerminalIcon } from './icons'
import StepTerminal, { formatLogTime, useStepLogs } from './StepTerminal'
import { formatSpan } from './timeline'

// How often an open conversation of a step still going is refreshed.
const CONVERSATION_REFRESH_MS = 1500

// The shortest wait for a slot a step's panel tells of.
const SLOT_WAIT_SHOWN_MS = 1000

export const RUN_LABELS: Record<FlowRunStatus, string> = {
  running: 'Running',
  done: 'Done',
  failed: 'Failed',
  interrupted: 'Interrupted',
  terminated: 'Terminated',
}

export const STEP_LABELS: Record<StepStatus, string> = {
  active: 'Running',
  waiting: 'Waiting for a person',
  done: 'Done',
  error: 'Failed',
  duplicate: 'Already processed',
  skipped: 'Skipped',
}

// Tokens, in thousands once there are many.
const formatTokens = (n: number) => (n >= 1000 ? `${(n / 1000).toFixed(n >= 10000 ? 0 : 1)}k` : String(n))

// What model calls used, as tokens in and out and their cost.
export const formatUsage = (u: StepUsage) =>
  `${formatTokens(u.input)} in · ${formatTokens(u.output)} out${u.cost > 0 ? ` · $${u.cost.toFixed(u.cost < 0.1 ? 4 : 2)}` : ''}`

// The usage of every step that has one, added up; undefined if none has.
const totalUsage = (steps: RunStep[]): StepUsage | undefined =>
  steps.reduce<StepUsage | undefined>(
    (t, s) => (s.usage ? { input: (t?.input ?? 0) + s.usage.input, output: (t?.output ?? 0) + s.usage.output, cost: (t?.cost ?? 0) + s.usage.cost } : t),
    undefined,
  )

export const formatTime = (s: string) =>
  new Date(s).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' })

export function formatDuration(start: string, end: string | null): string {
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

export const STANDING_LABELS: Record<RunStanding, string> = {
  running: 'Running',
  waiting: 'Waiting',
  done: 'Done',
  failed: 'Failed',
  interrupted: 'Interrupted',
  terminated: 'Terminated',
}

export const ORIGIN_LABELS: Record<RunOrigin, string> = {
  person: 'A person',
  schedule: 'A schedule',
  api: 'The API',
  webhook: 'A webhook',
  rerun: 'A re-run',
  record: 'Records',
  run: 'Another run',
  trial: 'A trial',
}

const STANDINGS = Object.keys(STANDING_LABELS) as RunStanding[]
const ORIGINS = Object.keys(ORIGIN_LABELS) as RunOrigin[]

// The flow's runs the filter picks, newest first, each with the runs of its
// recording flow processes under it, such as one per item of a list, and
// its re-runs, in the order they started, as far as they are loaded; older
// ones load a page at a time. Above them, a search of what the runs held,
// on the flow's Data page, and the filter. While a run to compare is
// picked, the next run clicked is compared with it.
export function RunList({
  flow,
  runs,
  filter,
  onFilter,
  more,
  loading,
  onMore,
  selected,
  onSelect,
  picking,
  onCancelPick,
}: {
  flow: number
  runs: FlowRun[]
  filter: RunFilter
  onFilter: (f: RunFilter) => void
  // Whether older runs the filter picks may not be loaded yet, and what loads them.
  more: boolean
  loading: boolean
  onMore: () => void
  selected: number | null
  onSelect: (id: number) => void
  // The run the next one clicked is compared with, if one is.
  picking: number | null
  onCancelPick: () => void
}) {
  const [filtering, setFiltering] = useState(false)
  const [query, setQuery] = useState('')
  const filtered = filter.status.length > 0 || filter.started.length > 0
  const ids = new Set(runs.map((r) => r.id))
  const children = new Map<number, FlowRun[]>()
  // A re-run goes under the run it re-runs.
  const parentOf = (r: FlowRun) => r.rerunOf ?? r.parent
  for (const r of [...runs].reverse()) {
    const p = parentOf(r)
    if (p !== null && ids.has(p)) children.set(p, [...(children.get(p) ?? []), r])
  }
  // A run whose parent fell off the list, or the filter left out, shows on its own.
  const top = runs.filter((r) => parentOf(r) === null || !ids.has(parentOf(r)!))

  // The Data page opens on the query, written as its address keeps one.
  const search = (e: FormEvent) => {
    e.preventDefault()
    const q = query.trim()
    window.location.hash = `#/flows/${flow}/data${q ? '?' + new URLSearchParams({ q }).toString().replace(/\+/g, '%20') : ''}`
  }
  const toggle = <K extends keyof RunFilter>(key: K, value: RunFilter[K][number]) => {
    const values = filter[key] as RunFilter[K][number][]
    onFilter({ ...filter, [key]: values.includes(value) ? values.filter((v) => v !== value) : [...values, value] })
  }

  const item = (r: FlowRun, nested: boolean) => (
    <li key={r.id}>
      <button
        className={`run-item${selected === r.id ? ' selected' : ''}${picking === r.id ? ' picking' : ''}`}
        aria-current={selected === r.id}
        disabled={picking === r.id}
        onClick={() => onSelect(r.id)}
      >
        <span className="run-item-title">
          <span className={`status-dot ${r.waiting ? 'waiting' : r.status}`} aria-hidden="true" />
          Run #{r.id}
          {r.schedule !== null && (
            <span className="run-item-scheduled" title="Started by a schedule">
              <ClockIcon />
            </span>
          )}
          <span className="run-item-status" title={r.waiting ? 'A step waits for a person' : undefined}>
            {r.waiting ? STANDING_LABELS.waiting : RUN_LABELS[r.status]}
          </span>
        </span>
        {r.itemKey && (
          <span className="run-item-key" title={r.itemKey}>
            {r.itemKey}
          </span>
        )}
        {r.rerunOf !== null && (
          <span className="run-item-process">
            {nested ? 'Re-run' : `Re-run of #${r.rerunOf}`} from {r.rerunFromName}
          </span>
        )}
        {r.trialOf !== null && (
          <span className="run-item-process run-item-trial" title="Only this process ran, on inputs given it; what it generated stays in the trial">
            Trial of {r.trialOfName} alone
          </span>
        )}
        {r.parent !== null && (
          <span className="run-item-process">
            {r.processName}
            {(!nested || r.rerunOf !== null) && ` · in run #${r.parent}`}
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

  return (
    <div className="run-finder" onKeyDown={(e) => e.key === 'Escape' && picking !== null && onCancelPick()}>
      <form className="run-search" role="search" onSubmit={search}>
        <input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search what runs held…"
          aria-label="Search the flow's runs on its Data page"
          title="Opens the flow's Data page with this search, e.g. status:failed -@Bug.state:fixed timeout"
        />
        <button
          type="button"
          className={`run-filter-button${filtering ? ' open' : ''}${filtered ? ' active' : ''}`}
          aria-expanded={filtering}
          onClick={() => setFiltering(!filtering)}
          title="Show only runs in some states, or started some ways"
        >
          Filter{filtered && ` · ${filter.status.length + filter.started.length}`}
        </button>
      </form>
      {filtering && (
        <div className="run-filters">
          <fieldset>
            <legend>Status</legend>
            {STANDINGS.map((s) => (
              <label key={s} className={`run-chip${filter.status.includes(s) ? ' on' : ''}`}>
                <input type="checkbox" checked={filter.status.includes(s)} onChange={() => toggle('status', s)} />
                <span className={`status-dot ${s}`} aria-hidden="true" />
                {STANDING_LABELS[s]}
              </label>
            ))}
          </fieldset>
          <fieldset>
            <legend>Started by</legend>
            {ORIGINS.map((o) => (
              <label key={o} className={`run-chip${filter.started.includes(o) ? ' on' : ''}`}>
                <input type="checkbox" checked={filter.started.includes(o)} onChange={() => toggle('started', o)} />
                {ORIGIN_LABELS[o]}
              </label>
            ))}
          </fieldset>
        </div>
      )}
      {filtered && !filtering && (
        <p className="panel-note run-filtered">
          {[
            filter.status.map((s) => STANDING_LABELS[s]).join(' or '),
            filter.started.length > 0 && `started by ${filter.started.map((o) => ORIGIN_LABELS[o].toLowerCase()).join(' or ')}`,
          ]
            .filter(Boolean)
            .join(', ')}{' '}
          <button className="link-button" onClick={() => onFilter({ status: [], started: [] })}>
            Show all
          </button>
        </p>
      )}
      {picking !== null && (
        <p className="run-picking" role="status">
          Pick the run to compare run #{picking} with.{' '}
          <button className="link-button" onClick={onCancelPick}>
            Cancel
          </button>
        </p>
      )}
      {runs.length === 0 ? (
        <p className="panel-note">
          {loading ? 'Loading…' : filtered ? 'No runs match the filter.' : 'No runs yet. Press play to run the flow.'}
        </p>
      ) : (
        <ol className="run-list">{top.map((r) => item(r, false))}</ol>
      )}
      {more && (
        <button className="run-more" onClick={onMore} disabled={loading}>
          {loading ? 'Loading…' : 'Load older runs'}
        </button>
      )}
    </div>
  )
}

// A process or object of the board the run panel is on, double-clicked
// while the run shows, with its name on the board, or a step clicked on the
// run's timeline; seq tells a second double-click on it from the first.
export type RunFocus = { kind: 'process' | 'object' | 'step'; id: number; name: string; seq: number }

// What happened in a run: each process execution in the order it started.
// On a process, its steps open and the panel scrolls to them; on an object,
// what it held in the run comes first, and the steps that made it open; on
// a step, it opens. Its timeline and its log open in windows the board shows.
export function RunPanel({
  run,
  focus,
  onClearFocus,
  onLogs,
  onTimeline,
  comparing,
  onCompare,
  onStop,
  onStopStep,
  onOpenRun,
  onRerun,
  onTryAgain,
  onAsk,
  onChanged,
  onClose,
}: {
  run: FlowRunDetail
  focus: RunFocus | null
  onClearFocus: () => void
  // Open the run's log and its timeline, which the board shows.
  onLogs: () => void
  onTimeline: () => void
  // Whether the run waits for the run to compare it with to be picked in the
  // list, and what starts or ends that.
  comparing: boolean
  onCompare: () => void
  onStop: () => void
  onStopStep: (id: number) => void
  // Opens another run: the one a recording flow process ran as, or the one it ran in.
  onOpenRun: (id: number) => void
  // Opens the dialog re-running this run from the step's process; see RerunDialog.
  onRerun: (step: number) => void
  // For a trial: opens the dialog trying its process again; see TrialDialog.
  onTryAgain?: (process: number) => void
  // Opens the flow's assistant with a question about the run to send,
  // naming it, which has the assistant told it; absent for those who may
  // not ask, and for a run of another flow.
  onAsk?: (question: string) => void
  // A follow-up started or was stopped, so the run is loaded again.
  onChanged: () => void
  onClose: () => void
}) {
  // Follow-ups work on the run's branch, so only one goes at a time, once its steps are over.
  const busy = run.steps.some((s) => s.status === 'active' || s.followUps.some((f) => f.status === 'active'))
  const focusedObjects = focus?.kind === 'object' ? run.objects.filter((o) => o.object === focus.id) : []
  const focused = (s: RunStep) =>
    focus?.kind === 'process'
      ? s.process === focus.id
      : focus?.kind === 'step'
        ? s.id === focus.id
        : focus?.kind === 'object' && focusedObjects.some((o) => o.step === s.id)
  const panel = useRef<HTMLElement>(null)
  useEffect(() => {
    if (!focus || !panel.current) return
    const steps = [...panel.current.querySelectorAll<HTMLDetailsElement>('.step.focused')]
    steps.forEach((d) => (d.open = true))
    const first = panel.current.querySelector('.run-focus') ?? steps[0]
    first?.scrollIntoView({ block: 'start', behavior: 'smooth' })
    // Only a new focus moves the panel, not the run refreshing.
  }, [focus?.seq])
  return (
    <aside ref={panel} className="panel run-panel" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
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
      {run.rerunOf !== null && (
        <p className="panel-note">
          A re-run of{' '}
          <button className="link-button" onClick={() => onOpenRun(run.rerunOf!)}>
            run #{run.rerunOf}
          </button>{' '}
          from {run.rerunFromName}.
        </p>
      )}
      {run.trialOf !== null && <TrialSummary run={run} onTryAgain={onTryAgain} />}
      <p className="panel-note">
        Started {formatTime(run.startedAt)}
        {run.startedBy && ` by ${run.startedBy}`}
        {run.apiKey && ` through the API key ${run.apiKey}`}
        {run.webhook !== null && ` for a delivery to ${webhookLabel(run.webhook)}`}
        {run.finishedAt && `, took ${formatDuration(run.startedAt, run.finishedAt)}`}
        {totalUsage(run.steps) && `; its agents used ${formatUsage(totalUsage(run.steps)!)}`}
      </p>
      {(run.spent > 0 || run.spendLimit > 0) && (
        <p className="panel-note">
          It spent {formatDollars(run.spent)} on its models
          {run.spendLimit > 0 && ` of the ${formatDollars(run.spendLimit)} it may; past that it is stopped`}.
        </p>
      )}
      <div className="run-actions">
        <button className="run-logs-button" onClick={onLogs} title="Read what happened in the run, step by step">
          Logs
        </button>
        <button
          className="run-logs-button"
          onClick={onTimeline}
          title="See the run's steps on a time axis: what ran at once, and how long each took and waited"
        >
          Timeline
        </button>
        <button
          className={`run-logs-button${comparing ? ' on' : ''}`}
          onClick={onCompare}
          aria-pressed={comparing}
          title="Put this run beside another of the flow's, to see where they differ"
        >
          Compare…
        </button>
        {onAsk && (
          <button
            className="run-logs-button"
            onClick={() => onAsk(run.status === 'failed' ? `Why did run #${run.id} fail?` : `About run #${run.id}: `)}
            title="Ask the flow's assistant about this run, which it is told: its steps, what they took and handed back, decisions, errors and log"
          >
            Ask the assistant
          </button>
        )}
        {run.status === 'running' && (
          <button className="run-stop" onClick={onStop} title="End the run where it is; steps still going are cancelled">
            Stop run
          </button>
        )}
      </div>
      {comparing && (
        <p className="run-picking" role="status">
          Pick the run to compare it with in the list of runs.{' '}
          <button className="link-button" onClick={onCompare}>
            Cancel
          </button>
        </p>
      )}
      <p className="panel-note">
        {run.version === null
          ? 'Ran before versions were kept, so the board shows it as the board is now.'
          : run.status === 'running'
            ? `Running on version ${run.version}.`
            : `Ran on version ${run.version}, which the board shows until the run closes.`}
      </p>
      {focus?.kind === 'object' && <ObjectInRun run={run} name={focus.name} objects={focusedObjects} onClose={onClearFocus} />}
      {focus?.kind === 'process' && !run.steps.some(focused) && (
        <p className="panel-note run-focus">
          {focus.name} didn't run in this run.{' '}
          <button className="link-button" onClick={onClearFocus}>
            Show all
          </button>
        </p>
      )}
      {run.steps.length === 0 ? (
        <p className="panel-note">No process has run yet.</p>
      ) : (
        <ol className="steps">
          {run.steps.map((s) => (
            <li key={s.id}>
              <Step
                run={run.id}
                step={s}
                focused={focused(s)}
                objects={run.objects.filter((o) => o.step === s.id)}
                onStop={stoppable(s) ? () => onStopStep(s.id) : undefined}
                onOpenRun={onOpenRun}
                onRerun={rerunnableFrom(run, s) ? () => onRerun(s.id) : undefined}
                onAsk={
                  onAsk && s.status === 'error' ? () => onAsk(`Why did ${s.processName} fail in run #${run.id}?`) : undefined
                }
                busy={busy}
                onChanged={onChanged}
              />
            </li>
          ))}
        </ol>
      )}
    </aside>
  )
}

// A run can be re-run from a step's process once it ended, unless the step
// was copied from an earlier run or its process is gone; a trial is tried
// again instead.
export const rerunnableFrom = (run: FlowRunDetail, s: RunStep) =>
  run.status !== 'running' && run.trialOf === null && s.process !== null && s.kind !== 'copied'

// What a trial is, and once it is over, what its process handed back and
// what that cost.
function TrialSummary({ run, onTryAgain }: { run: FlowRunDetail; onTryAgain?: (process: number) => void }) {
  const usage = totalUsage(run.steps)
  const cost = run.spent > 0 ? run.spent : (usage?.cost ?? 0)
  const handed = run.objects.map((o) => (o.state ? `${o.name} (${o.state})` : o.name))
  return (
    <div className="trial-summary">
      <p>
        A trial of {run.trialOfName} alone, on what it was given: nothing before or after it ran, and what it generated
        stays here. What it did outside the flow, it did.
      </p>
      {run.status !== 'running' && (
        <p>
          {handed.length > 0 ? `It handed back ${handed.join(', ')}` : 'It handed back nothing'}
          {cost > 0 ? `, and cost ${formatDollars(cost)} on models` : usage ? '' : ', and used no models'}.
        </p>
      )}
      {onTryAgain && (
        <button className="panel-button" onClick={() => onTryAgain(run.trialOf!)} title="Try the process again, on the inputs this trial took or a sample">
          Try again…
        </button>
      )}
    </div>
  )
}

// Agent, script and connector steps can be stopped while they go.
export const stoppable = (s: { kind: ProcessKind | 'copied' | 'input' | 'loop'; status: StepStatus }) =>
  s.status === 'active' && (s.kind === 'agent' || s.kind === 'script' || s.kind === 'connector')

function Step({
  run,
  step: s,
  focused,
  objects,
  onStop,
  onOpenRun,
  onRerun,
  onAsk,
  busy,
  onChanged,
}: {
  run: number
  step: RunStep
  // Its process, or an object it made, was double-clicked on the board.
  focused: boolean
  objects: FlowRunDetail['objects']
  onStop?: () => void
  onOpenRun: (id: number) => void
  // Re-runs the run from the step's process; absent when it can't be.
  onRerun?: () => void
  // For a failed step: asks the flow's assistant why it failed.
  onAsk?: () => void
  busy: boolean
  onChanged: () => void
}) {
  const [terminal, setTerminal] = useState(false)
  if (s.kind === 'copied' || s.kind === 'input') return <CopiedStep step={s} focused={focused} objects={objects} />
  return (
    <details className={`step${focused ? ' focused' : ''}`} open={s.status !== 'done' || s.followUps.length > 0}>
      <summary>
        <span className={`status-dot ${s.status}`} aria-hidden="true" />
        <span className="step-name">{s.processName}</span>
        <span className={`process-kind ${s.kind}`}>{s.kind}</span>
        <span className="step-meta">{s.finishedAt ? formatDuration(s.startedAt, s.finishedAt) : STEP_LABELS[s.status]}</span>
      </summary>
      <div className="step-body">
        <p className="panel-note">
          {STEP_LABELS[s.status]} · started {formatTime(s.startedAt)}
          {s.slotWaitMs !== null && s.slotWaitMs >= SLOT_WAIT_SHOWN_MS && ` after waiting ${formatSpan(s.slotWaitMs)} for a slot`}
          {s.try !== null && s.try > 1 && ` · try ${s.try}`}
          {s.process === null && ' · process since deleted'}
          {s.usage && ` · ${formatUsage(s.usage)}`}
        </p>
        {onStop && (
          <button className="run-stop" onClick={onStop} title="Stop this step; its branch ends, the rest of the run goes on">
            Stop step
          </button>
        )}
        {onRerun && (
          <button
            className="step-rerun"
            onClick={onRerun}
            title="Run the flow again from this process, with what came before it copied from this run"
          >
            Re-run from here
          </button>
        )}
        {(s.kind === 'agent' || s.kind === 'script') &&
          (s.status === 'active' || s.followUps.some((f) => f.status === 'active')) && <StepActivity run={run} step={s.id} />}
        {s.kind === 'agent' && (
          <p className="panel-note">
            Model: {s.model || 'default'} · thinking: {s.thinking || 'default'}
          </p>
        )}
        {s.session && (
          <p className="panel-note">
            pi session <code className="step-session">{s.session}</code>
          </p>
        )}
        {s.branch && (
          <p className="panel-note">
            Worked on the branch <code>{s.branch}</code>
          </p>
        )}
        {s.commits.length > 0 && <StepCommits commits={s.commits} />}
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
        {s.kind === 'loop' && (
          <Block
            label={s.decidedBy && s.status !== 'waiting' ? `Asked · answered by ${s.decidedBy}` : 'Asked'}
            text={s.prompt}
          />
        )}
        {s.prompt && s.kind !== 'loop' && (
          <details className="step-prompt">
            <summary>
              {s.kind === 'script' ? 'Script it ran' : s.kind === 'connector' ? 'Operation it carried out' : 'Prompt sent to the agent'}
            </summary>
            <pre>{s.prompt}</pre>
          </details>
        )}
        {(s.kind === 'agent' || s.kind === 'script') && s.prompt && (
          <button
            className="terminal-button"
            onClick={() => setTerminal(true)}
            title={`Follow what the ${s.kind === 'agent' ? 'agent does' : 'script does'} as in a terminal, with the files it changed beside it`}
          >
            <TerminalIcon /> Terminal
          </button>
        )}
        {s.kind === 'agent' && s.prompt && <Conversation run={run} step={s} />}
        {/* Outside the step, whose details close as it ends and would hide
            the dialog while it stays modal. */}
        {terminal && createPortal(<StepTerminal run={run} step={s} onClose={() => setTerminal(false)} />, document.body)}
        {s.kind === 'human' && s.outcome && (
          <Block
            label={s.decidedBy ? `Decision · ${s.decidedBy}` : 'Decision'}
            text={decisionText(s.outcome, objects[0]?.name, s.instructions)}
          />
        )}
        {s.error && (
          <Block
            label={s.status === 'duplicate' ? 'Duplicate' : s.status === 'skipped' ? 'Skipped' : 'Error'}
            text={s.error}
            failed={s.status !== 'duplicate' && s.status !== 'skipped'}
          />
        )}
        {onAsk && (
          <button className="step-rerun" onClick={onAsk} title="Ask the flow's assistant why this step failed, with the run told it">
            Ask the assistant why
          </button>
        )}
        {s.kind !== 'human' && s.kind !== 'flow' && s.kind !== 'subflow' && s.kind !== 'loop' && (s.status === 'done' || s.status === 'duplicate') && (
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
        {s.followUps.map((f) => (
          <FollowUpItem key={f.id} run={run} step={s.id} followUp={f} onChanged={onChanged} />
        ))}
        {s.canFollowUp && s.status !== 'active' && <FollowUpForm run={run} step={s.id} busy={busy} onSent={onChanged} />}
      </div>
    </details>
  )
}

// How many of the latest lines of the worker's log a going step shows.
const ACTIVITY_LINES = 6

// What the worker does of a step while it goes, the latest lines of its log
// followed as they come: getting its workspace ready, fetching and cloning
// with git's progress and what it waits for, then starting pi or bash.
function StepActivity({ run, step }: { run: number; step: number }) {
  const { lines, error } = useStepLogs(run, step, true, true)
  const shown = (lines ?? []).filter((l) => l.source === 'worker').slice(-ACTIVITY_LINES)
  return (
    <div className="step-activity" aria-live="polite">
      <span>Worker</span>
      {error ? (
        <p className="term-error">Its log couldn't load: {error}</p>
      ) : shown.length === 0 ? (
        <p className="term-dim">Waiting for the worker…</p>
      ) : (
        shown.map((l) => (
          <div key={l.id} className={`term-log ${l.level}`}>
            <time dateTime={l.time} title={new Date(l.time).toLocaleString()}>
              {formatLogTime(l.time)}
            </time>
            <pre>{l.message}</pre>
          </div>
        ))
      )}
    </div>
  )
}

// What a re-run copied from the run it re-runs, as the run left it before
// the process it re-ran from.
function CopiedStep({ step: s, focused, objects }: { step: RunStep; focused: boolean; objects: FlowRunDetail['objects'] }) {
  return (
    <details className={`step${focused ? ' focused' : ''}`}>
      <summary>
        <span className={`status-dot ${s.status}`} aria-hidden="true" />
        <span className="step-name">{s.processName}</span>
        <span className={`process-kind ${s.kind}`}>{s.kind}</span>
      </summary>
      <div className="step-body">
        {objects.map((o) => (
          <Block key={o.id} label={o.state ? `${o.name} (${o.state})` : o.name} text={o.content} data />
        ))}
      </div>
    </details>
  )
}

// What an object held in the run: each instance of it a step made, in the
// state it was made in, and the steps that took it.
function ObjectInRun({
  run,
  name,
  objects,
  onClose,
}: {
  run: FlowRunDetail
  name: string
  objects: FlowRunDetail['objects']
  onClose: () => void
}) {
  const stepOf = (step: number) => run.steps.find((s) => s.id === step)
  // Inputs name the object they were, not its id.
  const takers = run.steps.filter((s) => s.inputs.some((i) => i.name === name))
  return (
    <section className="run-focus">
      <header>
        <h3>{name}</h3>
        <button className="link-button" onClick={onClose}>
          Show all
        </button>
      </header>
      {objects.length === 0 ? (
        <p className="panel-note">Nothing made it in this run.</p>
      ) : (
        objects.map((o) => (
          <div key={o.id} className="run-focus-instance">
            <Block
              label={`${o.state ? `${o.name} (${o.state})` : o.name} · ${stepOf(o.step)?.kind === 'copied' ? 'copied from the run re-run' : stepOf(o.step)?.kind === 'input' ? 'given through the API' : `made by ${stepOf(o.step)?.processName ?? 'a step'}`}`}
              text={o.content}
              data
            />
            {o.files.length > 0 && (
              <ul className="step-files">
                {o.files.map((f) => (
                  <li key={f}>
                    <a href={fileUrl(f)} target="_blank" rel="noreferrer">
                      {f.split('/').pop()}
                    </a>
                  </li>
                ))}
              </ul>
            )}
          </div>
        ))
      )}
      {takers.length > 0 && <p className="panel-note">Taken by {[...new Set(takers.map((s) => s.processName))].join(', ')}</p>}
    </section>
  )
}

// A message sent to the agent once its step was over, and what came of it.
function FollowUpItem({ run, step, followUp: f, onChanged }: { run: number; step: number; followUp: FollowUp; onChanged: () => void }) {
  const stop = async () => {
    try {
      await stopFollowUp(run, step, f.id)
    } catch {
      // It ended meanwhile; the reload shows how.
    }
    onChanged()
  }
  return (
    <div className="follow-up">
      <Block label={`You asked · ${formatTime(f.startedAt)}`} text={f.message} />
      {f.status === 'active' ? (
        <>
          <p className="panel-note">The agent is working on it…</p>
          <button className="run-stop" onClick={stop}>
            Stop
          </button>
        </>
      ) : (
        <>
          {f.commits.length > 0 && <StepCommits commits={f.commits} />}
          {f.error ? <Block label="Error" text={f.error} failed /> : <Block label="The agent replied" text={f.output || '(nothing)'} />}
        </>
      )}
    </div>
  )
}

// Sends the agent a message in its session: it acts on it with what it did in
// mind, in the run's workspace, such as to push its branch or change its work.
function FollowUpForm({ run, step, busy, onSent }: { run: number; step: number; busy: boolean; onSent: () => void }) {
  const [message, setMessage] = useState('')
  const [sending, setSending] = useState(false)
  const [error, setError] = useState('')
  const disabled = busy || sending || message.trim() === ''

  const send = async () => {
    if (disabled) return
    setSending(true)
    setError('')
    try {
      await sendFollowUp(run, step, message.trim())
      setMessage('')
      onSent()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="follow-up-form">
      <label className="panel-field">
        <span>Message the agent</span>
        <textarea
          rows={3}
          value={message}
          placeholder="Carries on its conversation, in the same workspace and branch. E.g. push the branch, or change something."
          onChange={(e) => setMessage(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
              e.preventDefault()
              send()
            }
          }}
        />
      </label>
      {busy && <p className="panel-note">Wait until what is going in this run is over.</p>}
      {error && <p className="panel-warning">{error}</p>}
      <div className="review-actions">
        <button onClick={send} disabled={disabled} title="Send (⌘/Ctrl+Enter)">
          {sending ? 'Sending…' : 'Send'}
        </button>
      </div>
    </div>
  )
}

// The commits a step made in each repository, from the one it started on.
function StepCommits({ commits }: { commits: RepoCommits[] }) {
  return (
    <ul className="step-commits">
      {commits.map((c) => (
        <li key={c.name}>
          <span className="step-commits-repo">{c.name}</span>{' '}
          <span className="panel-note">
            from <code>{c.before.slice(0, 7)}</code>
            {c.after === c.before ? ', no commits' : <> to <code>{c.after.slice(0, 7)}</code></>}
          </span>
          {c.uncommitted && <span className="panel-warning"> · left changes uncommitted</span>}
          {c.commits.length > 0 && (
            <ul>
              {c.commits.map((line) => (
                <li key={line}>
                  <code>{line.slice(0, line.indexOf(' '))}</code> {line.slice(line.indexOf(' ') + 1)}
                </li>
              ))}
            </ul>
          )}
        </li>
      ))}
    </ul>
  )
}

// A step's conversation with the model, loaded once enabled, and followed
// while the step or a follow-up of it goes. Once it ended, it loads once
// more for the messages that came in last.
export function useStepMessages(run: number, step: RunStep, enabled: boolean) {
  const [messages, setMessages] = useState<StepMessage[]>([])
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState('')
  const last = useRef(0)
  const going = step.status === 'active' || step.followUps.some((f) => f.status === 'active')

  useEffect(() => {
    if (!enabled) return
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
  }, [enabled, going, run, step.id])

  return { messages, loaded, error, going }
}

// What the agent did: its conversation with the model, loaded once opened.
function Conversation({ run, step }: { run: number; step: RunStep }) {
  const [open, setOpen] = useState(false)
  const { messages, loaded, error, going } = useStepMessages(run, step, open)
  const followUpOf = new Map(step.followUps.map((f) => [f.attempt, f]))

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
              <p className="panel-note">
                {followUpOf.has(m.attempt)
                  ? `Follow-up, ${formatTime(followUpOf.get(m.attempt)!.startedAt)}: you messaged the agent.`
                  : `Try ${m.attempt}: the agent didn't hand in its output, so it was asked again.`}
              </p>
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

// Dollars to the cent, or closer under a dime.
function formatDollars(d: number): string {
  return '$' + (d < 0.1 ? d.toFixed(4) : d.toFixed(2))
}
