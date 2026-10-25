import { useEffect, useRef, useState, type ReactNode } from 'react'
import { listRecordFlows, startRecordRuns, type FlowRun, type RecordFlow, type RecordRun } from './api'

// Starts a flow on records of a shared struct, a run on each: the flows
// listed are those whose queries read the struct's records, and each run's
// queries read its record alone (record_runs.go). Before a person starts
// them, it says which records a flow's query wouldn't select, and which
// have a run going on them already, since no run claims a record; neither
// stops them.
export function RecordRunDialog({
  struct,
  name,
  records,
  onClose,
}: {
  struct: number
  // The struct's name.
  name: string
  // The records to start a run on each of, by id.
  records: number[]
  onClose: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const [flows, setFlows] = useState<RecordFlow[] | null>(null)
  const [going, setGoing] = useState<Record<string, RecordRun[] | undefined>>({})
  const [flow, setFlow] = useState<number | null>(null)
  const [started, setStarted] = useState<FlowRun[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [starting, setStarting] = useState(false)

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])

  useEffect(() => {
    listRecordFlows(struct, records)
      .then((r) => {
        setFlows(r.flows)
        setGoing(r.going)
        if (r.flows.length === 1) setFlow(r.flows[0].id)
      })
      .catch((err) => setError(err instanceof Error ? err.message : String(err)))
    // The records are taken as the dialog opens.
  }, [struct])

  const start = async () => {
    if (flow === null) return
    setStarting(true)
    setError(null)
    try {
      setStarted(await startRecordRuns(struct, flow, records))
    } catch (err) {
      // The server's reason, without the request it answered.
      setError((err instanceof Error ? err.message : String(err)).replace(/^\w+ \S+: \d+ /, '').trim())
    }
    setStarting(false)
  }

  const one = records.length === 1
  const what = one ? `record ${records[0]}` : `${records.length} records`
  const title = `Run a flow on ${what} of ${name}`
  const picked = flows?.find((f) => f.id === flow)
  const off = picked ? records.filter((r) => picked.off[r] !== undefined) : []
  const busy = records.filter((r) => (going[r]?.length ?? 0) > 0)
  const runLink = (r: RecordRun | FlowRun) => {
    const id = 'run' in r ? r.run : r.id
    return (
      <a key={id} href={`#/flows/${r.flow}/runs/${id}`} onClick={onClose}>
        #{id}
      </a>
    )
  }
  const joined = (links: ReactNode[]) => links.flatMap((l, i) => (i === 0 ? [l] : [', ', l]))

  return (
    <dialog ref={ref} className="review-dialog record-run-dialog" aria-label={title} onClose={onClose} onKeyDown={(e) => e.stopPropagation()}>
      <header className="panel-header">
        <h2>{title}</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      {started ? (
        <div className="record-form">
          <p>
            Started {started.length === 1 ? 'run' : 'runs'} {joined(started.map(runLink))} of {picked?.name}.
          </p>
          <div className="review-actions">
            <button type="button" onClick={onClose}>
              Close
            </button>
          </div>
        </div>
      ) : (
        <div className="record-form">
          {flows === null ? (
            !error && <p className="panel-note">Loading…</p>
          ) : flows.length === 0 ? (
            <p className="panel-note">
              No flow you may run queries {name}'s records. A flow does once one of its agents or scripts takes {name} by a query link.
            </p>
          ) : (
            <>
              <p className="panel-note">
                {one ? 'The run' : 'Each run'} reads {one ? 'this record' : 'its record'} alone where the flow queries {name}, whatever its query
                selects.
              </p>
              <fieldset className="record-run-flows">
                <legend>Flow</legend>
                {flows.map((f) => (
                  <label key={f.id}>
                    <input type="radio" name="record-run-flow" checked={flow === f.id} onChange={() => setFlow(f.id)} />
                    {f.name}
                  </label>
                ))}
              </fieldset>
              {picked && off.length > 0 && (
                <p className="panel-warning">
                  {one
                    ? `${picked.name}'s query wouldn't select this record: ${picked.off[records[0]]}. It runs on it anyway.`
                    : `${picked.name}'s query wouldn't select ${off.length === 1 ? 'record' : 'records'} ${off.join(', ')}: ${[
                        ...new Set(off.map((r) => picked.off[r])),
                      ].join('; ')}. It runs on them anyway.`}
                </p>
              )}
              {busy.length > 0 && (
                <p className="panel-warning">
                  {busy.map((r) => (
                    <span key={r} className="record-run-busy">
                      {one ? 'A run is going on this record already' : `A run is going on record ${r} already`}:{' '}
                      {joined((going[r] ?? []).map(runLink))}.
                    </span>
                  ))}{' '}
                  Another works on it at the same time.
                </p>
              )}
            </>
          )}
          {error && <p className="panel-warning">{error}</p>}
          <div className="review-actions">
            <button type="button" onClick={onClose}>
              Cancel
            </button>
            <button type="button" disabled={flow === null || starting} onClick={start}>
              {one ? 'Start the run' : `Start ${records.length} runs`}
            </button>
          </div>
        </div>
      )}
    </dialog>
  )
}
