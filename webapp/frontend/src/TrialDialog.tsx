import { useEffect, useRef, useState } from 'react'
import {
  changesSettled,
  deleteSample,
  getTrial,
  saveSample,
  tryProcess,
  type FlowRun,
  type TrialPlan,
  type TrialValue,
} from './api'

// What the dialog gives an object the process takes: whether it is given
// at all, in which state, holding what.
type Given = { given: boolean; state: string; content: string }

// Where the inputs shown came from: a sample, the last run that reached the
// process, or nothing.
type Source = `sample-${number}` | 'last' | 'none'

// Tries one process on its own, in the middle of the screen: what it takes,
// each to change before it runs, starting from one of its samples or what it
// took the last time a run reached it; its samples, to pick, save and
// delete; and plainly, that it really runs, with what it does outside the
// flow. Trying it starts a trial, a run in which only it runs; see trials.go.
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
  const [error, setError] = useState('')

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])

  // Shows what source gives each object the process takes; one it gives
  // nothing of is left out, but with nothing to start from, each is given
  // empty in the state its link takes it in.
  const load = (p: TrialPlan, from: Source) => {
    const sample = p.samples.find((s) => `sample-${s.id}` === from)
    const inputs = sample?.inputs ?? (from === 'last' ? p.last?.inputs : undefined)
    setSource(from)
    setName(sample?.name ?? '')
    setValues(
      Object.fromEntries(
        p.takes.map((o) => {
          const v = inputs?.find((i) => i.object === o.object)
          return [o.object, v ? { given: true, state: v.state, content: v.content } : { given: !inputs, state: o.state, content: '' }]
        }),
      ),
    )
  }

  useEffect(() => {
    let stale = false
    getTrial(process)
      .then((p) => {
        if (stale) return
        setPlan(p)
        load(p, p.samples.length > 0 ? `sample-${p.samples[0].id}` : p.last ? 'last' : 'none')
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
  const set = (object: number, patch: Partial<Given>) => setValues((vs) => ({ ...vs, [object]: { ...vs[object], ...patch } }))

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
      load(next, next.last ? 'last' : 'none')
    })

  // Edits to the process still being saved go first, so it runs as shown.
  const start = () =>
    act(async () => {
      await changesSettled()
      onStarted(await tryProcess(process, inputs()))
    })

  const processName = plan?.processName ?? 'the process'
  const sample = plan?.samples.find((s) => `sample-${s.id}` === source)
  return (
    <dialog
      ref={ref}
      className="review-dialog rerun-dialog trial-dialog"
      aria-label="Try the process"
      onClose={onClose}
      // Escape closes the dialog, not the panel it opened from.
      onKeyDown={(e) => e.stopPropagation()}
    >
      <header className="panel-header">
        <h2>Try {plan?.processName ?? '…'}</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <div className="rerun-body">
        <p className="panel-note">
          A trial of the flow on the board as it is, in which only {processName} runs, on what you give it below: nothing
          before or after it runs, and nothing handles it failing.
        </p>
        <div className="trial-warning" role="note">
          <strong>It really runs.</strong> Whatever it does outside the flow, it does: a message it posts is posted, a
          comment is made, a branch it pushes is pushed.
          {plan && plan.effects.length > 0 && (
            <ul>
              {plan.effects.map((e) => (
                <li key={e}>{e}</li>
              ))}
            </ul>
          )}
        </div>
        <p className="panel-note">
          What it generates stays in the trial: it isn't remembered as handled, carried to the next run, or written to
          shared records, and the Data page leaves the trial out. Its spending counts as any run's.
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
              {plan.last && (
                <option value="last">
                  What it took in {plan.last.trial ? 'trial' : 'run'} #{plan.last.run}
                </option>
              )}
              <option value="none">Nothing</option>
            </select>
          </label>
        )}
        {plan && plan.takes.length === 0 && <p className="panel-note">It takes nothing.</p>}
        {plan?.takes.map((o) => {
          const v = values[o.object]
          if (!v) return null
          return (
            <div key={o.object} className="panel-field trial-input">
              <div className="trial-input-head">
                <label className="panel-check">
                  <input type="checkbox" checked={v.given} onChange={(e) => set(o.object, { given: e.target.checked })} />
                  <span>{o.name}</span>
                </label>
                {v.given && o.states.length > 0 && (
                  <select aria-label={`${o.name}'s state`} value={v.state} onChange={(e) => set(o.object, { state: e.target.value })}>
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
            <button onClick={save} disabled={busy || name.trim() === ''} title="Keep these inputs with the process, to try it on again">
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
        <button onClick={start} disabled={!plan || busy}>
          {busy ? 'Working…' : 'Try'}
        </button>
      </div>
    </dialog>
  )
}
