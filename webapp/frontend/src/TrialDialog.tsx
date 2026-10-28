import { useEffect, useRef, useState } from 'react'
import {
  changesSettled,
  deleteSample,
  getTrial,
  getRun,
  getTrialRun,
  saveSample,
  tryProcess,
  trySamples,
  type FlowRun,
  type FlowRunDetail,
  type SampleTrial,
  type TrialPast,
  type TrialPlan,
  type TrialValue,
} from './api'
import { formatTime, RUN_LABELS, STEP_LABELS } from './Runs'

// How often the trials on every sample are asked how they go.
const SAMPLE_TRIALS_POLL_MS = 2000

// What a trial's step came to, in a line: what went wrong, or the start of
// what it handed back.
function trialOutcome(d: FlowRunDetail | undefined): string {
  const s = d?.steps[d.steps.length - 1]
  if (!s) return ''
  const text = (s.error || s.output).trim().split('\n')[0]
  return text.length > 160 ? `${text.slice(0, 160)}…` : text
}

// What the dialog gives an object the process takes: whether it is given
// at all, in which state, holding what.
type Given = { given: boolean; state: string; content: string }

// Where the inputs shown came from: a sample, a run of the flow, or nothing.
type Source = `sample-${number}` | `run-${number}` | 'none'

// Where a plan starts from: its first sample, else the latest run it may
// start from, else nothing.
const firstSource = (p: TrialPlan): Source =>
  p.samples.length > 0 ? `sample-${p.samples[0].id}` : p.past.length > 0 ? `run-${p.past[0].run}` : 'none'

// What a run gives the process, as the dialog offers it: what the process
// took there, or what the run held, such as a trial of the process before
// it generated; and when it started, and how it ended if not well.
function pastLabel(r: TrialPast): string {
  const which = r.trial ? `trial #${r.run} of ${r.trialOf}` : `run #${r.run}`
  const ended = r.status === 'done' || r.status === 'running' ? '' : ` · ${RUN_LABELS[r.status].toLowerCase()}`
  return `${r.reached ? `What it took in ${which}` : `What ${which} held`} · ${formatTime(r.startedAt)}${ended}`
}

// Tries one process on its own, in the middle of the screen: what it takes,
// each to change before it runs, starting from one of its samples or from
// one of the flow's latest runs, what it took there or what the run held;
// its samples, to pick, save and delete; and plainly, that it really runs,
// with what it does outside the flow. Trying it starts a trial, a run in
// which only it runs; see trials.go.
export default function TrialDialog({
  process,
  onStarted,
  onClose,
}: {
  process: number
  onStarted: (run: FlowRun) => void
  onClose: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const [plan, setPlan] = useState<TrialPlan | null>(null)
  const [source, setSource] = useState<Source>('none')
  const [values, setValues] = useState<Record<number, Given>>({})
  // The name the inputs are saved under, the sample's they came from.
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  // Whether what a run gives is being fetched, which the dialog then shows.
  const [fetching, setFetching] = useState(false)
  const [error, setError] = useState('')
  // Counts what was picked to start from, so a run's values fetched after
  // something else was picked are dropped.
  const picked = useRef(0)
  // The trials on every sample once started, and how each goes, by run.
  const [sampleTrials, setSampleTrials] = useState<SampleTrial[] | null>(null)
  const [details, setDetails] = useState<Record<number, FlowRunDetail>>({})

  useEffect(() => {
    if (!sampleTrials) return
    let stale = false
    let timer = 0
    const known: Record<number, FlowRunDetail> = {}
    // Asks again of each trial still going, until none is; one that can't
    // be read is asked again next time.
    const poll = async () => {
      const going = sampleTrials.filter((s) => (known[s.run.id]?.status ?? s.run.status) === 'running')
      const read = await Promise.all(going.map((s) => getRun(s.run.id).catch(() => null)))
      if (stale) return
      for (const d of read) if (d) known[d.id] = d
      setDetails({ ...known })
      if (read.some((d) => !d || d.status === 'running')) timer = window.setTimeout(poll, SAMPLE_TRIALS_POLL_MS)
    }
    timer = window.setTimeout(poll, 0)
    return () => {
      stale = true
      window.clearTimeout(timer)
    }
  }, [sampleTrials])

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])

  // Shows what inputs give each object the process takes; one they give
  // nothing of is left out, but with nothing to start from, each is given
  // empty in the state its link takes it in.
  const show = (p: TrialPlan, inputs: TrialValue[] | undefined) =>
    setValues(
      Object.fromEntries(
        p.takes.map((o) => {
          const v = inputs?.find((i) => i.object === o.object)
          return [
            o.object,
            v ? { given: true, state: v.state, content: v.content } : { given: !inputs, state: o.state, content: '' },
          ]
        }),
      ),
    )

  // Starts from a sample, a run, whose values are fetched, or nothing.
  const load = async (p: TrialPlan, from: Source) => {
    const pick = ++picked.current
    const sample = p.samples.find((s) => `sample-${s.id}` === from)
    const past = p.past.find((r) => `run-${r.run}` === from)
    setSource(from)
    setName(sample?.name ?? '')
    setError('')
    if (!past) {
      setFetching(false)
      show(p, sample?.inputs)
      return
    }
    setFetching(true)
    try {
      const inputs = await getTrialRun(p.process, past.run)
      if (pick === picked.current) show(p, inputs)
    } catch (err) {
      if (pick === picked.current) setError(err instanceof Error ? err.message : String(err))
    } finally {
      if (pick === picked.current) setFetching(false)
    }
  }

  useEffect(() => {
    let stale = false
    getTrial(process)
      .then((p) => {
        if (stale) return
        setPlan(p)
        load(p, firstSource(p))
      })
      .catch((err) => !stale && setError(err instanceof Error ? err.message : String(err)))
    return () => {
      stale = true
    }
  }, [process])

  const inputs = (): TrialValue[] =>
    (plan?.takes ?? []).flatMap((o) => {
      const v = values[o.object]
      return v?.given ? [{ object: o.object, state: v.state, content: v.content }] : []
    })
  const set = (object: number, patch: Partial<Given>) =>
    setValues((vs) => ({ ...vs, [object]: { ...vs[object], ...patch } }))

  const act = async (what: () => Promise<void>) => {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await what()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const save = () =>
    act(async () => {
      if (!plan) return
      const samples = await saveSample(process, name.trim(), inputs())
      const saved = samples.find((s) => s.name === name.trim())
      setPlan({ ...plan, samples })
      if (saved) setSource(`sample-${saved.id}`)
    })

  const remove = () =>
    act(async () => {
      const sample = plan?.samples.find((s) => `sample-${s.id}` === source)
      if (!plan || !sample) return
      await deleteSample(process, sample.id)
      const next = { ...plan, samples: plan.samples.filter((s) => s.id !== sample.id) }
      setPlan(next)
      load(next, next.past.length > 0 ? `run-${next.past[0].run}` : 'none')
    })

  // Edits to the process still being saved go first, so it runs as shown.
  const start = () =>
    act(async () => {
      await changesSettled()
      onStarted(await tryProcess(process, inputs()))
    })

  const startAll = () =>
    act(async () => {
      await changesSettled()
      setDetails({})
      setSampleTrials(await trySamples(process))
    })

  const processName = plan?.processName ?? 'the process'
  const sample = plan?.samples.find((s) => `sample-${s.id}` === source)
  // The trials on every sample, shown in the dialog in place of the inputs.
  const trialsView = sampleTrials && (
    <>
      <header className="panel-header">
        <h2>{processName} on every sample</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <div className="rerun-body">
        <p className="panel-note">
          A trial of {processName} on each of its samples, on the board as it is. Open one to see its step in full.
        </p>
        <ul className="sample-trials">
          {sampleTrials.map((s) => {
            const d = details[s.run.id]
            const step = d?.steps[d.steps.length - 1]
            const status = step ? STEP_LABELS[step.status] : RUN_LABELS[d?.status ?? s.run.status]
            return (
              <li key={s.run.id} className={`sample-trial ${step?.status ?? 'active'}`}>
                <div className="sample-trial-head">
                  <strong>{s.name}</strong>
                  <span className="sample-trial-status">{status}</span>
                  <button onClick={() => onStarted(d ?? s.run)}>Open trial #{s.run.id}</button>
                </div>
                {trialOutcome(d) && <div className="sample-trial-outcome">{trialOutcome(d)}</div>}
              </li>
            )
          })}
        </ul>
      </div>
      <div className="review-actions">
        <button onClick={() => setSampleTrials(null)}>Back to the inputs</button>
        <button onClick={onClose}>Close</button>
      </div>
    </>
  )
  return (
    <dialog
      ref={ref}
      className="review-dialog rerun-dialog trial-dialog"
      aria-label={sampleTrials ? 'Trials on every sample' : 'Try the process'}
      onClose={onClose}
      // Escape closes the dialog, not the panel it opened from.
      onKeyDown={(e) => e.stopPropagation()}
    >
      {trialsView || (
        <>
          <header className="panel-header">
            <h2>Try {plan?.processName ?? '…'}</h2>
            <button className="panel-close" onClick={onClose} aria-label="Close">
              ×
            </button>
          </header>
          <div className="rerun-body">
            <p className="panel-note">
              A trial of the flow on the board as it is, in which only {processName} runs, on what you give it below:
              nothing before or after it runs, and nothing handles it failing.
            </p>
            <div className="trial-warning" role="note">
              <strong>It really runs.</strong> Whatever it does outside the flow, it does: a message it posts is posted,
              a comment is made, a branch it pushes is pushed.
              {plan && plan.effects.length > 0 && (
                <ul>
                  {plan.effects.map((e) => (
                    <li key={e}>{e}</li>
                  ))}
                </ul>
              )}
            </div>
            <p className="panel-note">
              What it generates stays in the trial: it isn't remembered as handled, carried to the next run, or written
              to shared records, and the Data page leaves the trial out. Its spending counts as any run's.
            </p>
            {plan && (
              <label className="panel-field">
                <span>Start from</span>
                <select value={source} disabled={busy} onChange={(e) => load(plan, e.target.value as Source)}>
                  {plan.samples.map((s) => (
                    <option key={s.id} value={`sample-${s.id}`}>
                      Sample: {s.name}
                    </option>
                  ))}
                  {plan.past.length > 0 && (
                    <optgroup label="The flow's latest runs">
                      {plan.past.map((r) => (
                        <option key={r.run} value={`run-${r.run}`}>
                          {pastLabel(r)}
                        </option>
                      ))}
                    </optgroup>
                  )}
                  <option value="none">Nothing</option>
                </select>
              </label>
            )}
            {fetching && <p className="panel-note">Reading what the run gives it…</p>}
            {plan && plan.takes.length === 0 && <p className="panel-note">It takes nothing.</p>}
            {plan?.takes.map((o) => {
              const v = values[o.object]
              if (!v) return null
              return (
                <div key={o.object} className="panel-field trial-input">
                  <div className="trial-input-head">
                    <label className="panel-check">
                      <input
                        type="checkbox"
                        checked={v.given}
                        onChange={(e) => set(o.object, { given: e.target.checked })}
                      />
                      <span>{o.name}</span>
                    </label>
                    {v.given && o.states.length > 0 && (
                      <select
                        aria-label={`${o.name}'s state`}
                        value={v.state}
                        onChange={(e) => set(o.object, { state: e.target.value })}
                      >
                        <option value="">In no state</option>
                        {o.states.map((st) => (
                          <option key={st} value={st}>
                            {st}
                          </option>
                        ))}
                      </select>
                    )}
                    {!v.given && <span className="panel-note">left out</span>}
                  </div>
                  {v.given && (
                    <textarea
                      className="rerun-input"
                      aria-label={o.name}
                      rows={Math.min(12, Math.max(3, v.content.split('\n').length))}
                      value={v.content}
                      onChange={(e) => set(o.object, { content: e.target.value })}
                    />
                  )}
                </div>
              )
            })}
            {plan && (
              <div className="trial-sample">
                <input
                  type="text"
                  aria-label="Sample name"
                  placeholder="Name to save these inputs as a sample"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
                <button
                  onClick={save}
                  disabled={busy || fetching || name.trim() === ''}
                  title="Keep these inputs with the process, to try it on again"
                >
                  {sample && sample.name === name.trim() ? 'Save sample' : 'Save as sample'}
                </button>
                {sample && (
                  <button onClick={remove} disabled={busy}>
                    Delete sample
                  </button>
                )}
              </div>
            )}
            {error && <p className="panel-warning">{error}</p>}
          </div>
          <div className="review-actions">
            <button onClick={onClose}>Cancel</button>
            {plan && plan.samples.length > 1 && (
              <button
                onClick={startAll}
                disabled={busy}
                title="Try it on each of its samples at once, a trial each, to check a change against all of them"
              >
                Try on all {plan.samples.length} samples
              </button>
            )}
            <button onClick={start} disabled={!plan || busy || fetching}>
              {busy ? 'Working…' : 'Try'}
            </button>
          </div>
        </>
      )}
    </dialog>
  )
}
