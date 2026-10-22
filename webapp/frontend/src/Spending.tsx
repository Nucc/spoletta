import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { getLimits, getSpending, saveLimits, type Limits, type Spending } from './api'

// What agents spent on models this month, by whom and on which runs, and the
// limits admins set on it; everyone else sees their own spending and the
// limits they are held to.
export function SpendingSection({ admin, onError }: { admin: boolean; onError: (err: unknown) => void }) {
  const [spending, setSpending] = useState<Spending | null>(null)
  const [limits, setLimits] = useState<Limits | null>(null)
  const [runSpend, setRunSpend] = useState('')
  const [userSpend, setUserSpend] = useState('')
  const [userSteps, setUserSteps] = useState('')
  const [saving, setSaving] = useState(false)

  const refresh = useCallback(async () => {
    const [s, l] = await Promise.all([getSpending(), getLimits()])
    setSpending(s)
    setLimits(l)
    setRunSpend(l.runSpend ? String(l.runSpend) : '')
    setUserSpend(l.userMonthSpend ? String(l.userMonthSpend) : '')
    setUserSteps(l.userParallelSteps ? String(l.userParallelSteps) : '')
  }, [])
  useEffect(() => {
    refresh().catch(onError)
  }, [refresh, onError])

  const amount = (s: string) => (s.trim() === '' ? 0 : Number(s))
  const valid = (n: number) => Number.isFinite(n) && n >= 0
  const run = amount(runSpend)
  const user = amount(userSpend)
  const steps = amount(userSteps)
  const stepsOk = Number.isInteger(steps) && steps >= 0

  const onSave = async (e: FormEvent) => {
    e.preventDefault()
    setSaving(true)
    try {
      setLimits(await saveLimits({ runSpend: run, userMonthSpend: user, userParallelSteps: steps }))
    } catch (err) {
      onError(err)
    } finally {
      setSaving(false)
    }
  }

  return (
    <section className="flow-section" aria-label="Spending">
      <h2>Spending</h2>
      <p className="flows-note">
        What agents spend on their models, at the prices of pi's catalog or of the worker's models.json. A model without
        a known price is counted at the largest models' price, so a limit isn't passed unnoticed. Months are UTC's.
      </p>
      <h3>Limits</h3>
      {admin ? (
        <form className="limits-form" onSubmit={onSave}>
          <label className="panel-field">
            <span>Most a run may spend ($)</span>
            <input
              value={runSpend}
              inputMode="decimal"
              placeholder="No limit"
              onChange={(e) => setRunSpend(e.target.value)}
            />
            <small className="panel-note">
              With its subflows, items and follow-ups. A run that spends it is stopped and fails; a flow may set a lower
              one in its settings.
            </small>
          </label>
          <label className="panel-field">
            <span>Most a person may spend a month ($)</span>
            <input
              value={userSpend}
              inputMode="decimal"
              placeholder="No limit"
              onChange={(e) => setUserSpend(e.target.value)}
            />
            <small className="panel-note">
              On their runs, follow-ups and flows' assistants. Past it their agents' calls are refused until next month.
            </small>
          </label>
          <label className="panel-field">
            <span>Steps a person's runs may have going at once</span>
            <input
              value={userSteps}
              inputMode="numeric"
              placeholder="No limit"
              onChange={(e) => setUserSteps(e.target.value)}
            />
            <small className="panel-note">
              Agent, script, Sentry and connector steps, of the instance's PARALLEL_STEPS, so one person's runs can't
              take every slot; the rest wait their turn.
            </small>
          </label>
          <button type="submit" disabled={saving || !valid(run) || !valid(user) || !stepsOk}>
            Save limits
          </button>
        </form>
      ) : (
        limits && (
          <p className="flows-note">
            A run may spend {limits.runSpend ? `$${limits.runSpend}` : 'without a limit'}, and you{' '}
            {limits.userMonthSpend ? `$${limits.userMonthSpend} a month` : 'without a limit'}
            {limits.userParallelSteps > 0 &&
              `, with ${limits.userParallelSteps} steps of your runs going at once at most`}
            .
          </p>
        )
      )}
      <h3>This month{spending && `, ${spending.month}`}</h3>
      {spending === null ? (
        <p className="flows-note">Loading…</p>
      ) : spending.people.length === 0 ? (
        <p className="flows-note">Nothing spent yet.</p>
      ) : (
        <>
          <table className="spending-table">
            <thead>
              <tr>
                <th>Who</th>
                <th>Spent</th>
                <th>Calls</th>
              </tr>
            </thead>
            <tbody>
              {spending.people.map((p) => (
                <tr key={p.user ?? 'nobody'}>
                  <td>{p.user === null ? 'Nobody signed in' : p.name}</td>
                  <td>{dollars(p.spent)}</td>
                  <td>
                    {p.calls}
                    {p.unpriced > 0 && <span className="flows-note"> ({p.unpriced} at the largest models' price)</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <h3>Costliest runs</h3>
          <table className="spending-table">
            <thead>
              <tr>
                <th>Run</th>
                <th>Spent</th>
              </tr>
            </thead>
            <tbody>
              {spending.runs.map((r) => (
                <tr key={r.run}>
                  <td>
                    <a href={`#/flows/${r.flow}/runs/${r.run}`}>
                      {r.name} · run {r.run}
                    </a>
                  </td>
                  <td>{dollars(r.spent)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}
    </section>
  )
}

// Dollars to the cent, or closer under a dime.
function dollars(d: number): string {
  return '$' + (d < 0.1 ? d.toFixed(4) : d.toFixed(2))
}
