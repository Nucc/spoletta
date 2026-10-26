import { useCallback, useEffect, useState, type ReactNode } from 'react'
import { getFlow, getRun, runOrigin, type Flow, type FlowRunDetail, type RunInput, type RunObject, type RunStep } from './api'
import { diffLines, fieldsOf, fieldText, outcomeOf, pairObjects, pairProcesses, pairSteps, type ObjectPair, type ProcessPair } from './compare'
import { FlowBuilderIcon } from './icons'
import { decisionText, formatTime, ORIGIN_LABELS, RUN_LABELS, STEP_LABELS } from './Runs'
import { formatSpan } from './timeline'

// How often the page catches up with a run still going.
const REFRESH_MS = 3000

// Two runs of a flow side by side, to see why one went another way: how
// each started and ended, and on which version of the board; each process's
// steps in both, how they ended, how long they took and what their models
// cost, what each took and handed back; and the objects' last values. What
// differs is marked, lines of text and fields of objects alike; a process
// that went differently opens on its own. See compare.ts.
export default function RunCompare({ flow, a: aId, b: bId }: { flow: number; a: number; b: number }) {
  const [flowInfo, setFlowInfo] = useState<Flow | null>(null)
  const [runs, setRuns] = useState<[FlowRunDetail, FlowRunDetail] | null>(null)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(
    () =>
      Promise.all([getRun(aId), getRun(bId)])
        .then(([a, b]) => setRuns([a, b]))
        .catch((err) => setError(err instanceof Error ? err.message : String(err))),
    [aId, bId],
  )

  useEffect(() => {
    getFlow(flow)
      .then(setFlowInfo)
      .catch(() => setFlowInfo(null))
    load()
  }, [flow, load])

  const going = runs?.some((r) => r.status === 'running') ?? false
  useEffect(() => {
    if (!going) return
    const t = window.setInterval(load, REFRESH_MS)
    return () => window.clearInterval(t)
  }, [going, load])

  useEffect(() => {
    document.title = `Run #${aId} and #${bId}${flowInfo ? ` · ${flowInfo.name}` : ''} · Flows`
  }, [aId, bId, flowInfo])

  const [a, b] = runs ?? [null, null]
  const otherFlows = a && b && a.flow !== b.flow
  return (
    <div className="flows-page compare-page">
      <header className="toolbar">
        <h1>
          <a className="home" href="#/" title="All flows">
            <FlowBuilderIcon /> Flows
          </a>
          <span className="crumb-separator" aria-hidden>
            /
          </span>
          <a href={`#/flows/${a?.flow ?? flow}`}>{flowInfo?.name ?? '…'}</a>
          <span className="crumb-separator" aria-hidden>
            /
          </span>
          Run #{aId} and #{bId}
        </h1>
        <a className="compare-swap" href={`#/flows/${flow}/compare/${bId}/${aId}`} title="Put each run on the other side">
          Swap sides
        </a>
        <button onClick={load} title="Fetch both runs again">
          Refresh
        </button>
      </header>
      {error && (
        <div className="error-banner" role="alert">
          {error}
        </div>
      )}
      <main className="compare-main">
        {!a || !b ? (
          !error && <p className="flows-note">Loading…</p>
        ) : otherFlows ? (
          <p className="flows-note">
            Runs #{a.id} and #{b.id} are runs of different flows, which can't be compared.
          </p>
        ) : (
          <Comparison a={a} b={b} />
        )}
      </main>
    </div>
  )
}

function Comparison({ a, b }: { a: FlowRunDetail; b: FlowRunDetail }) {
  const processes = pairProcesses(a, b)
  const objects = pairObjects(a, b)
  const took = (r: FlowRunDetail) => (r.finishedAt ? formatSpan(Date.parse(r.finishedAt) - Date.parse(r.startedAt)) : 'still going')
  const board = (r: FlowRunDetail) => (r.process === null ? 'the whole flow' : `the board of ${r.processName}`)
  const differing = processes.filter((p) => outcomeOf(p.a) !== outcomeOf(p.b)).length
  return (
    <>
      <section className="compare-section">
        <div className="compare-grid compare-heads">
          <span />
          {[a, b].map((r) => (
            <h2 key={r.id}>
              <a href={`#/flows/${r.flow}/runs/${r.id}`} title="Open the run on the board">
                Run #{r.id}
              </a>{' '}
              <span className={`run-badge ${r.waiting ? 'waiting' : r.status}`}>{r.waiting ? 'Waiting' : RUN_LABELS[r.status]}</span>
            </h2>
          ))}
        </div>
        <Row label="Started" a={formatTime(a.startedAt)} b={formatTime(b.startedAt)} plain />
        <Row label="Started by" a={startedBy(a)} b={startedBy(b)} />
        <Row label="Took" a={took(a)} b={took(b)} plain />
        <Row label="Spent on models" a={formatCost(a.spent)} b={formatCost(b.spent)} plain />
        <Row
          label="Version"
          a={a.version === null ? 'none kept' : `v${a.version}`}
          b={b.version === null ? 'none kept' : `v${b.version}`}
        />
        {a.process !== b.process && <Row label="Ran" a={board(a)} b={board(b)} />}
        <p className="panel-note compare-note">
          {a.version === null || b.version === null
            ? 'A run from before versions were kept may have run on another board than the one it shows.'
            : a.version !== b.version
              ? `They ran on different versions of the board, v${a.version} and v${b.version}: what changed in between may explain what differs.`
              : `Both ran on version ${a.version} of the board.`}{' '}
          {differing === 0
            ? 'Every process ended the same way in both.'
            : `${differing} of ${processes.length} processes went differently, and open below.`}
        </p>
      </section>

      <section className="compare-section">
        <h2>Processes</h2>
        {processes.length === 0 ? (
          <p className="panel-note">No process ran in either.</p>
        ) : (
          processes.map((p) => <ProcessCompare key={p.key} flow={a.flow} pair={p} objects={{ a: a.objects, b: b.objects }} />)
        )}
      </section>

      <section className="compare-section">
        <h2>Objects as the runs left them</h2>
        {objects.length === 0 ? (
          <p className="panel-note">Neither made any object.</p>
        ) : (
          objects.map((o) => <ObjectCompare key={o.key} pair={o} />)
        )}
      </section>
    </>
  )
}

const startedBy = (r: FlowRunDetail) => {
  const origin = ORIGIN_LABELS[runOrigin(r)]
  return [
    r.trialOf !== null
      ? `${origin} of ${r.trialOfName} alone`
      : r.rerunOf !== null
        ? `${origin} of #${r.rerunOf} from ${r.rerunFromName}`
        : r.parent !== null
        ? `Run #${r.parent}, as ${r.processName}`
        : origin,
    r.apiKey && `key ${r.apiKey}`,
    r.webhook && `webhook ${r.webhook}`,
    r.startedBy,
  ]
    .filter(Boolean)
    .join(' · ')
}

// A label and what each run has of it, marked where they differ, unless
// plain says a difference tells nothing, as two start times.
function Row({ label, a, b, plain }: { label: string; a: ReactNode; b: ReactNode; plain?: boolean }) {
  const differs = !plain && a !== b
  return (
    <div className={`compare-grid compare-row${differs ? ' differs' : ''}`}>
      <span className="compare-label">{label}</span>
      <span>{a}</span>
      <span>{b}</span>
    </div>
  )
}

// A process in both runs: its steps in each, then, opened, each step beside
// the other run's of the same turn, with what it took and handed back.
function ProcessCompare({ flow, pair: p, objects }: { flow: number; pair: ProcessPair; objects: { a: RunObject[]; b: RunObject[] } }) {
  const differs = outcomeOf(p.a) !== outcomeOf(p.b)
  const turns = pairSteps(p.a, p.b)
  return (
    <details className={`compare-process${differs ? ' differs' : ''}`} open={differs}>
      <summary className="compare-grid">
        <span className="compare-label">
          {p.name} <span className={`process-kind ${p.kind}`}>{p.kind}</span>
        </span>
        <StepsSummary steps={p.a} />
        <StepsSummary steps={p.b} />
      </summary>
      {turns.map((t, i) => (
        <StepCompare
          key={i}
          flow={flow}
          turn={i + 1}
          turns={turns.length}
          a={t.a}
          b={t.b}
          made={{ a: objects.a.filter((o) => o.step === t.a?.id), b: objects.b.filter((o) => o.step === t.b?.id) }}
        />
      ))}
    </details>
  )
}

// A process's steps in one run: how each ended, how long it took, its
// retries, and what its models cost in all.
function StepsSummary({ steps }: { steps: RunStep[] }) {
  if (steps.length === 0) return <span className="panel-note">Didn't run</span>
  const cost = steps.reduce((n, s) => n + (s.usage?.cost ?? 0), 0)
  const retries = steps.filter((s) => s.try !== null && s.try > 1).length
  return (
    <span className="compare-steps">
      {steps.map((s) => (
        <span key={s.id} className="compare-step" title={`${STEP_LABELS[s.status]}, started ${formatTime(s.startedAt)}`}>
          <span className={`status-dot ${s.status}`} aria-hidden="true" />
          {STEP_LABELS[s.status]}
          {s.finishedAt && ` ${formatSpan(Date.parse(s.finishedAt) - Date.parse(s.startedAt))}`}
        </span>
      ))}
      {steps.length > 1 && (
        <span className="panel-note">{retries > 0 ? `${steps.length} steps, ${retries} of them retries` : `${steps.length} steps`}</span>
      )}
      {cost > 0 && <span className="panel-note">{formatCost(cost)}</span>}
    </span>
  )
}

// A process's step of one turn in either run: what it took, of each object
// by name, and what it handed back, each object it made or else what it
// replied, or a person's decision; or what went wrong. A flow process's runs
// of its board can be compared in turn.
function StepCompare({
  flow,
  turn,
  turns,
  a,
  b,
  made,
}: {
  flow: number
  turn: number
  turns: number
  a?: RunStep
  b?: RunStep
  // The objects each step made.
  made: { a: RunObject[]; b: RunObject[] }
}) {
  const inputs = new Map<string, { a?: RunInput; b?: RunInput }>()
  for (const [side, s] of [
    ['a', a],
    ['b', b],
  ] as const)
    for (const i of s?.inputs ?? []) inputs.set(i.name, { ...inputs.get(i.name), [side]: i })
  const handed = new Map<string, { a?: string; b?: string; state?: boolean }>()
  const hand = (label: string, side: 'a' | 'b', text: string, state = false) => handed.set(label, { ...handed.get(label), [side]: text, state })
  for (const [side, s] of [
    ['a', a],
    ['b', b],
  ] as const) {
    const objects = made[side]
    if (!s) continue
    if (s.kind === 'human') {
      if (s.outcome) hand('Decided', side, decisionText(s.outcome, objects[0]?.name, s.instructions))
    } else if (objects.length > 0) {
      for (const o of objects) {
        if (o.state) hand(`Handed back ${o.name}: state`, side, o.state, true)
        hand(`Handed back ${o.name}`, side, o.content)
      }
    } else if (s.kind !== 'flow' && s.kind !== 'subflow' && s.kind !== 'loop' && (s.status === 'done' || s.status === 'duplicate')) {
      hand('Handed back', side, s.output)
    }
  }
  // A run of another flow, a subflow's, opens on that flow's board.
  const child = (s?: RunStep) => (s?.childRun ? <a href={`#/flows/${flow}/runs/${s.childRun}`}>run #{s.childRun}</a> : '')
  return (
    <div className="compare-turn">
      {turns > 1 && (
        <h3>
          Step {turn} of {turns}
        </h3>
      )}
      {(a?.try ?? 1) > 1 || (b?.try ?? 1) > 1 ? <Row label="Try" a={a?.try ?? '—'} b={b?.try ?? '—'} /> : null}
      {[...inputs].map(([name, i]) => (
        <div key={name}>
          {(i.a?.state || i.b?.state) && <Row label={`Took ${name}: state`} a={i.a?.state || '—'} b={i.b?.state || '—'} />}
          <ValueRow label={`Took ${name}`} a={i.a?.content} b={i.b?.content} />
        </div>
      ))}
      {[...handed].map(([label, h]) =>
        h.state ? <Row key={label} label={label} a={h.a ?? '—'} b={h.b ?? '—'} /> : <ValueRow key={label} label={label} a={h.a} b={h.b} />,
      )}
      {(a?.error || b?.error) && <ValueRow label="What went wrong" a={a?.error || undefined} b={b?.error || undefined} />}
      {(a?.childRun || b?.childRun) && (
        <div className="compare-grid compare-row">
          <span className="compare-label">
            Ran its board as
            {a?.childRun && b?.childRun && (
              <>
                {' '}
                <a href={`#/flows/${flow}/compare/${a.childRun}/${b.childRun}`} title="Compare the runs of its board in turn">
                  compare them
                </a>
              </>
            )}
          </span>
          <span>{child(a)}</span>
          <span>{child(b)}</span>
        </div>
      )}
    </div>
  )
}

// An object's last value in each run, and its state.
function ObjectCompare({ pair: o }: { pair: ObjectPair }) {
  return (
    <div className="compare-object">
      {(o.a?.state || o.b?.state) && <Row label={`${o.name}: state`} a={o.a?.state || '—'} b={o.b?.state || '—'} />}
      <ValueRow label={o.name} a={o.a?.content} b={o.b?.content} />
    </div>
  )
}

// A value in each run, an object's fields a row each, else its lines, what
// differs marked; absent where a run had none.
function ValueRow({ label, a, b }: { label: string; a?: string; b?: string }) {
  if (a === undefined && b === undefined) return null
  const fa = a === undefined ? null : fieldsOf(a)
  const fb = b === undefined ? null : fieldsOf(b)
  if ((fa || a === undefined) && (fb || b === undefined) && (fa || fb)) {
    const keys = [...new Set([...Object.keys(fa ?? {}), ...Object.keys(fb ?? {})])]
    return (
      <>
        {keys.map((k) => (
          <TextRow
            key={k}
            label={`${label}.${k}`}
            a={fa && k in fa ? fieldText(fa[k]) : undefined}
            b={fb && k in fb ? fieldText(fb[k]) : undefined}
          />
        ))}
      </>
    )
  }
  return <TextRow label={label} a={a} b={b} />
}

function TextRow({ label, a, b }: { label: string; a?: string; b?: string }) {
  const differs = a !== b
  const lines = differs && a !== undefined && b !== undefined ? diffLines(a, b) : null
  const side = (text: string | undefined, marked: { text: string; differs: boolean }[] | undefined) =>
    text === undefined ? (
      <span className="panel-note">nothing</span>
    ) : (
      <pre className="compare-text">
        {(marked ?? text.split('\n').map((t) => ({ text: t, differs: false }))).map((l, i) => (
          <span key={i} className={l.differs ? 'differs' : undefined}>
            {l.text}
            {'\n'}
          </span>
        ))}
      </pre>
    )
  return (
    <div className={`compare-grid compare-row${differs ? ' differs' : ''}`}>
      <span className="compare-label">{label}</span>
      {side(a, lines?.a)}
      {side(b, lines?.b)}
    </div>
  )
}

// Dollars to the cent, or closer under a dime.
const formatCost = (d: number) => '$' + (d < 0.1 ? d.toFixed(4) : d.toFixed(2))
