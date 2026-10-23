import {
  useCallback,
  useEffect,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import {
  getLimits,
  getSpending,
  saveLimits,
  type Limits,
  type Spending,
} from "./api";

// What agents spent on models this month, by whom and on which runs, and the
// limits admins set on it; everyone else sees their own spending and the
// limits they are held to.
export function SpendingSection({
  admin,
  onError,
}: {
  admin: boolean;
  onError: (err: unknown) => void;
}) {
  const [spending, setSpending] = useState<Spending | null>(null);
  const [limits, setLimits] = useState<Limits | null>(null);
  const [runSpend, setRunSpend] = useState("");
  const [userSpend, setUserSpend] = useState("");
  const [userSteps, setUserSteps] = useState("");
  const [disk, setDisk] = useState("");
  const [saving, setSaving] = useState(false);

  const refresh = useCallback(async () => {
    const [s, l] = await Promise.all([getSpending(), getLimits()]);
    setSpending(s);
    setLimits(l);
    setRunSpend(l.runSpend ? String(l.runSpend) : "");
    setUserSpend(l.userMonthSpend ? String(l.userMonthSpend) : "");
    setUserSteps(l.userParallelSteps ? String(l.userParallelSteps) : "");
    setDisk(l.runDiskMb ? String(l.runDiskMb) : "");
  }, []);
  useEffect(() => {
    refresh().catch(onError);
  }, [refresh, onError]);

  const amount = (s: string) => (s.trim() === "" ? 0 : Number(s));
  const valid = (n: number) => Number.isFinite(n) && n >= 0;
  const run = amount(runSpend);
  const user = amount(userSpend);
  const steps = amount(userSteps);
  const stepsOk = Number.isInteger(steps) && steps >= 0;
  const diskMb = amount(disk);
  const diskOk = Number.isInteger(diskMb) && diskMb >= 0;

  const onSave = async (e: FormEvent) => {
    e.preventDefault();
    setSaving(true);
    try {
      setLimits(
        await saveLimits({
          runSpend: run,
          userMonthSpend: user,
          userParallelSteps: steps,
          runDiskMb: diskMb,
        }),
      );
    } catch (err) {
      onError(err);
    } finally {
      setSaving(false);
    }
  };

  const dirty =
    limits !== null &&
    (run !== limits.runSpend ||
      user !== limits.userMonthSpend ||
      steps !== limits.userParallelSteps ||
      diskMb !== limits.runDiskMb);
  const totalSpent = spending
    ? spending.people.reduce((sum, p) => sum + p.spent, 0)
    : 0;
  const totalCalls = spending
    ? spending.people.reduce((sum, p) => sum + p.calls, 0)
    : 0;
  const topPerson = spending
    ? Math.max(0, ...spending.people.map((p) => p.spent))
    : 0;
  const topRun = spending
    ? Math.max(0, ...spending.runs.map((r) => r.spent))
    : 0;

  return (
    <section className="flow-section spending" aria-label="Spending and limits">
      <header className="spending-header">
        <h2>Spending &amp; limits</h2>
        <p className="flows-note">
          What agents spend on their models, at the prices of pi's catalog or of
          the worker's models.json. A model without a known price is counted at
          the largest models' price, so a limit isn't passed unnoticed. Months
          are UTC's.
        </p>
      </header>

      <div className="spending-stats">
        <div className="spending-stat">
          <span className="spending-stat-label">
            Spent{spending && ` in ${monthName(spending.month)}`}
          </span>
          <span className="spending-stat-value">
            {spending ? dollars(totalSpent) : "…"}
          </span>
          {limits && limits.userMonthSpend > 0 && (
            <span className="spending-stat-note">
              each person up to {dollars(limits.userMonthSpend)}
            </span>
          )}
        </div>
        <div className="spending-stat">
          <span className="spending-stat-label">Model calls</span>
          <span className="spending-stat-value">
            {spending ? totalCalls.toLocaleString() : "…"}
          </span>
          {spending && totalCalls > 0 && (
            <span className="spending-stat-note">
              {dollars(totalSpent / totalCalls)} a call on average
            </span>
          )}
        </div>
        <div className="spending-stat">
          <span className="spending-stat-label">Costliest run</span>
          <span className="spending-stat-value">
            {spending ? dollars(topRun) : "…"}
          </span>
          {limits && limits.runSpend > 0 && (
            <span className="spending-stat-note">
              of {dollars(limits.runSpend)} a run may spend
            </span>
          )}
        </div>
      </div>

      <div className="spending-card">
        <div className="spending-card-head">
          <h3>Limits</h3>
          <span className="spending-card-sub">
            Leave a field empty for no limit.
          </span>
        </div>
        {admin ? (
          <form onSubmit={onSave}>
            <div className="limits-grid">
              <LimitField
                label="Most a run may spend"
                prefix="$"
                value={runSpend}
                onChange={setRunSpend}
                invalid={!valid(run)}
                inputMode="decimal"
              >
                With its subflows, items and follow-ups. A run that spends it is
                stopped and fails; a flow may set a lower one in its settings.
              </LimitField>
              <LimitField
                label="Most a person may spend a month"
                prefix="$"
                value={userSpend}
                onChange={setUserSpend}
                invalid={!valid(user)}
                inputMode="decimal"
              >
                On their runs, follow-ups and flows' assistants. Past it their
                agents' calls are refused until next month.
              </LimitField>
              <LimitField
                label="Steps a person's runs may have going at once"
                suffix="steps"
                value={userSteps}
                onChange={setUserSteps}
                invalid={!stepsOk}
                inputMode="numeric"
              >
                Agent, script, Sentry and connector steps, of the instance's{" "}
                <code>PARALLEL_STEPS</code>, so one person's runs can't take
                every slot; the rest wait their turn.
              </LimitField>
              <LimitField
                label="Disk a run may take"
                suffix="MB"
                value={disk}
                onChange={setDisk}
                invalid={!diskOk}
                inputMode="numeric"
              >
                Its checkouts, agents' sessions, home and files, checked after
                each step: a step that leaves the run over it fails, its work
                kept.
              </LimitField>
            </div>
            <div className="spending-card-foot">
              <span className="spending-card-sub">
                {dirty ? "Unsaved changes" : "Saved"}
              </span>
              <button
                type="submit"
                className="spending-save"
                disabled={
                  !dirty ||
                  saving ||
                  !valid(run) ||
                  !valid(user) ||
                  !stepsOk ||
                  !diskOk
                }
              >
                {saving ? "Saving…" : "Save limits"}
              </button>
            </div>
          </form>
        ) : (
          limits && (
            <dl className="limits-read">
              <div>
                <dt>A run may spend</dt>
                <dd>
                  {limits.runSpend ? dollars(limits.runSpend) : "No limit"}
                </dd>
              </div>
              <div>
                <dt>You may spend a month</dt>
                <dd>
                  {limits.userMonthSpend
                    ? dollars(limits.userMonthSpend)
                    : "No limit"}
                </dd>
              </div>
              <div>
                <dt>Your runs' steps at once</dt>
                <dd>{limits.userParallelSteps || "No limit"}</dd>
              </div>
              <div>
                <dt>Disk a run may take</dt>
                <dd>
                  {limits.runDiskMb ? `${limits.runDiskMb} MB` : "No limit"}
                </dd>
              </div>
            </dl>
          )
        )}
      </div>

      {spending === null ? (
        <p className="flows-note">Loading…</p>
      ) : spending.people.length === 0 ? (
        <div className="spending-card spending-empty">
          Nothing spent in {monthName(spending.month)} yet.
        </div>
      ) : (
        <div className="spending-columns">
          <div className="spending-card">
            <div className="spending-card-head">
              <h3>By person</h3>
              <span className="spending-card-sub">
                {monthName(spending.month)}
              </span>
            </div>
            <ol className="spending-bars">
              {spending.people.map((p) => {
                const cap = limits?.userMonthSpend || 0;
                const share =
                  cap > 0
                    ? p.spent / cap
                    : topPerson > 0
                      ? p.spent / topPerson
                      : 0;
                return (
                  <li key={p.user ?? "nobody"}>
                    <div className="spending-bar-row">
                      <span
                        className={
                          p.user === null
                            ? "spending-who muted"
                            : "spending-who"
                        }
                      >
                        {p.user === null ? "Nobody signed in" : p.name}
                      </span>
                      <span className="spending-amount">
                        {dollars(p.spent)}
                      </span>
                    </div>
                    <Bar share={share} over={cap > 0 && p.spent >= cap} />
                    <span className="spending-meta">
                      {p.calls.toLocaleString()}{" "}
                      {p.calls === 1 ? "call" : "calls"}
                      {p.unpriced > 0 &&
                        ` · ${p.unpriced} at the largest models' price`}
                      {cap > 0 &&
                        ` · ${Math.round((p.spent / cap) * 100)}% of the month's limit`}
                    </span>
                  </li>
                );
              })}
            </ol>
          </div>
          <div className="spending-card">
            <div className="spending-card-head">
              <h3>Costliest runs</h3>
              <span className="spending-card-sub">
                with their subflows and follow-ups
              </span>
            </div>
            <ol className="spending-bars">
              {spending.runs.map((r, i) => (
                <li key={r.run}>
                  <div className="spending-bar-row">
                    <a
                      className="spending-run"
                      href={`#/flows/${r.flow}/runs/${r.run}`}
                    >
                      <span className="spending-rank">{i + 1}</span>
                      <span className="spending-run-name">{r.name}</span>
                      <span className="spending-run-id">run {r.run}</span>
                    </a>
                    <span className="spending-amount">{dollars(r.spent)}</span>
                  </div>
                  <Bar share={topRun > 0 ? r.spent / topRun : 0} />
                </li>
              ))}
            </ol>
          </div>
        </div>
      )}
    </section>
  );
}

// A limit's field: its label, the input with its unit beside it, and what
// the limit holds runs or people to.
function LimitField({
  label,
  prefix,
  suffix,
  value,
  onChange,
  invalid,
  inputMode,
  children,
}: {
  label: string;
  prefix?: string;
  suffix?: string;
  value: string;
  onChange: (v: string) => void;
  invalid: boolean;
  inputMode: "decimal" | "numeric";
  children: ReactNode;
}) {
  return (
    <label className="limit-field">
      <span className="limit-label">{label}</span>
      <span className={invalid ? "limit-input invalid" : "limit-input"}>
        {prefix && <span className="limit-unit">{prefix}</span>}
        <input
          value={value}
          inputMode={inputMode}
          placeholder="No limit"
          onChange={(e) => onChange(e.target.value)}
        />
        {suffix && <span className="limit-unit">{suffix}</span>}
      </span>
      <small className="limit-help">{children}</small>
    </label>
  );
}

// How much of the largest amount, or of a limit, one took.
function Bar({ share, over }: { share: number; over?: boolean }) {
  return (
    <span className="spending-bar" aria-hidden="true">
      <span
        className={over ? "over" : undefined}
        style={{ width: `${Math.max(2, Math.min(1, share) * 100)}%` }}
      />
    </span>
  );
}

// A month as 2026-10 names it: October 2026.
function monthName(month: string): string {
  const [y, m] = month.split("-").map(Number);
  if (!y || !m) return month;
  return new Date(Date.UTC(y, m - 1, 1)).toLocaleString(undefined, {
    month: "long",
    year: "numeric",
    timeZone: "UTC",
  });
}

// Dollars to the cent, or closer under a dime.
function dollars(d: number): string {
  return "$" + (d < 0.1 ? d.toFixed(4) : d.toFixed(2));
}
