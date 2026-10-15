import { useEffect, useRef, useState } from 'react'
import { getRerun, rerun, type FlowRun, type RerunPlan } from './api'

// Re-runs a run from the process of one of its steps, in the middle of the
// screen: shows what the process took in the run, each to change before it
// runs again, and what the re-run copies from the run and generates anew.
export default function RerunDialog({
  run,
  step,
  within,
  onStarted,
  onClose,
}: {
  run: number
  step: number
  // For the run of a flow process: the flow process, and the run it ran in.
  within?: { name: string; run: number }
  onStarted: (run: FlowRun) => void
  onClose: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const [plan, setPlan] = useState<RerunPlan | null>(null)
  // What each object the process takes holds, by object id, as edited.
  const [contents, setContents] = useState<Record<number, string>>({})
  const [starting, setStarting] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])

  useEffect(() => {
    let stale = false
    getRerun(run, step)
      .then((p) => {
        if (stale) return
        setPlan(p)
        setContents(Object.fromEntries(p.inputs.map((i) => [i.object, i.content])))
      })
      .catch((err) => !stale && setError(err instanceof Error ? err.message : String(err)))
    return () => {
      stale = true
    }
  }, [run, step])

  const start = async () => {
    if (!plan || starting) return
    setStarting(true)
    setError('')
    try {
      // Only what changed goes, so the rest is taken as the run had it.
      const changed = Object.fromEntries(plan.inputs.filter((i) => contents[i.object] !== i.content).map((i) => [i.object, contents[i.object]]))
      onStarted(await rerun(run, step, changed))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setStarting(false)
    }
  }

  return (
    <dialog
      ref={ref}
      className="review-dialog rerun-dialog"
      aria-label="Re-run"
      onClose={onClose}
      // Escape closes the dialog, not the run's panel it opened from.
      onKeyDown={(e) => e.stopPropagation()}
    >
      <header className="panel-header">
        <h2>Re-run from {plan?.processName ?? '…'}</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <div className="rerun-body">
        <p className="panel-note">
          A new run on the board as it is now, starting at {plan?.processName ?? 'the process'}. What came before it is copied from run #{run};
          it and what follows it run again.
        </p>
        {within && (
          <p className="panel-note">
            It runs {within.name}&rsquo;s board again in run #{within.run}, but what comes out doesn&rsquo;t go on to what follows {within.name}
            there; for that, re-run run #{within.run} from {within.name}.
          </p>
        )}
        {plan && plan.inputs.length === 0 && <p className="panel-note">It takes nothing.</p>}
        {plan?.inputs.map((i) => (
          <label key={i.object} className="panel-field">
            <span>
              {i.name}
              {i.state && ` (${i.state})`}
              {!i.present && ' · the run had nothing of it'}
            </span>
            <textarea
              className="rerun-input"
              rows={Math.min(12, Math.max(3, (contents[i.object] ?? '').split('\n').length))}
              value={contents[i.object] ?? ''}
              onChange={(e) => setContents((c) => ({ ...c, [i.object]: e.target.value }))}
            />
          </label>
        ))}
        {plan && plan.copied.length > 0 && <p className="panel-note">Copied as the run left them: {plan.copied.join(', ')}.</p>}
        {plan && plan.anew.length > 0 && <p className="panel-note">Generated anew: {plan.anew.join(', ')}.</p>}
        {plan && (
          <p className="panel-note">
            The re-run takes over the values run #{run} stored. It works on a branch of its own, starting where run #{run}'s branch was
            as {plan.processName} started.
          </p>
        )}
        {error && <p className="panel-warning">{error}</p>}
      </div>
      <div className="review-actions">
        <button onClick={onClose}>Cancel</button>
        <button onClick={start} disabled={!plan || starting}>
          {starting ? 'Starting…' : 'Re-run'}
        </button>
      </div>
    </dialog>
  )
}
