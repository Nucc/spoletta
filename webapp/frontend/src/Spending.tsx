import {
  useCallback,
  useEffect,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import {
  ApiError,
  errorText,
  getCeilings,
  getLimits,
  getSpending,
  saveCeilings,
  saveLimits,
  type Limits,
  type ProjectLimits,
  type Spending,
} from "./api";
import { SettingsHead } from "./SettingsHead";
import { pageHash } from './address'

// What agents spent on models this month, by whom and on which runs, and the
// limits the project's owners set on it (limits.go), each at most the
// ceiling the instance's admin sets over every project: what holds is the
// lesser of the two, which everyone else sees as what they are held to.
// The ceilings are set in the settings' Instance group (CeilingsSection).
// A limit above its ceiling is marked where it is typed, and the server's
// refusal of it shows there too, in its own words.

// A limit by its name in what the server sends.
type LimitKey = keyof Limits;

// Each limit as the settings show it: what it is called where it is set,
// and where only what holds is shown; its unit; whether it is a whole
// number; what it holds runs or people to; and the server's words for a
// limit above its ceiling (overCeilings in limits.go).
const LIMIT_FIELDS: {
  key: LimitKey;
  label: string;
  held: string;
  prefix?: string;
  suffix?: string;
  whole: boolean;
  help: ReactNode;
  over: (ceiling: number) => string;
}[] = [
  {
    key: "runSpend",
    label: "Most a run may spend",
    held: "A run may spend",
    prefix: "$",
    whole: false,
    help: "With its subflows, items and follow-ups. A run that spends it is stopped and fails; a flow may set a lower one in its settings.",
    over: (c) => `a run may spend at most ${dollars(c)}, the instance's ceiling`,
  },
  {
    key: "userMonthSpend",
    label: "Most a person may spend a month",
    held: "You may spend a month",
    prefix: "$",
    whole: false,
    help: "On their runs, follow-ups and flows' assistants. Past it their agents' calls are refused until next month.",
    over: (c) => `a person may spend at most ${dollars(c)} a month, the instance's ceiling`,
  },
  {
    key: "userParallelSteps",
    label: "Steps a person's runs may have going at once",
    held: "Your runs' steps at once",
    suffix: "steps",
    whole: true,
    help: (
      <>
        Agent, script and connector steps, of the instance's{" "}
        <code>PARALLEL_STEPS</code>, so one person's runs can't take every
        slot; the rest wait their turn.
      </>
    ),
    over: (c) =>
      `a person's runs may have at most ${c} steps going at once, the instance's ceiling`,
  },
  {
    key: "runDiskMb",
    label: "Disk a run may take",
    held: "Disk a run may take",
    suffix: "MB",
    whole: true,
    help: "Its checkouts, agents' sessions, home and files, checked after each step: a step that leaves the run over it fails, its work kept.",
    over: (c) => `a run may take at most ${c} MB of disk, the instance's ceiling`,
  },
];

// The limits as typed, a text each.
type Draft = Record<LimitKey, string>;

const draftOf = (l: Limits): Draft => ({
  runSpend: l.runSpend ? String(l.runSpend) : "",
  userMonthSpend: l.userMonthSpend ? String(l.userMonthSpend) : "",
  userParallelSteps: l.userParallelSteps ? String(l.userParallelSteps) : "",
  runDiskMb: l.runDiskMb ? String(l.runDiskMb) : "",
});

// The limits typed, an empty field none, and those that aren't a limit:
// an amount of dollars, or a whole number of steps or MB, none below 0.
function readDraft(d: Draft): { limits: Limits; bad: Set<LimitKey> } {
  const limits = {} as Limits;
  const bad = new Set<LimitKey>();
  for (const f of LIMIT_FIELDS) {
    const n = d[f.key].trim() === "" ? 0 : Number(d[f.key]);
    if (!Number.isFinite(n) || n < 0 || (f.whole && !Number.isInteger(n)))
      bad.add(f.key);
    limits[f.key] = n;
  }
  return { limits, bad };
}

const sameLimits = (a: Limits, b: Limits) =>
  LIMIT_FIELDS.every((f) => a[f.key] === b[f.key]);

// What holds of a project's own limit and the ceiling over it, 0 for
// neither, as limits.go's leastLimit has it.
const leastLimit = (own: number, ceiling: number) =>
  own === 0 ? ceiling : ceiling === 0 ? own : Math.min(own, ceiling);

// A limit as read: in dollars, steps or MB, or none.
function amount(key: LimitKey, v: number): string {
  if (v === 0) return "No limit";
  const f = LIMIT_FIELDS.find((x) => x.key === key)!;
  if (f.prefix) return dollars(v);
  return `${v.toLocaleString()} ${f.suffix}`;
}

// What holds of a limit as typed and the instance's ceiling over it, said
// under its field: the server's words for one above the ceiling, else what
// holds and whether the ceiling is what does.
function CeilingNote({
  field,
  own,
  ceiling,
  changed,
}: {
  field: LimitKey;
  own: number;
  ceiling: number;
  // Whether it is typed and not saved yet, so would hold once saved.
  changed: boolean;
}) {
  const f = LIMIT_FIELDS.find((x) => x.key === field)!;
  // One saved before the ceiling came down below it, or typed above it.
  if (ceiling > 0 && own > ceiling)
    return (
      <span className="limit-over">
        {sentence(f.over(ceiling))}
        {changed ? "." : ", which holds over this."}
      </span>
    );
  const holds = leastLimit(own, ceiling);
  const word = changed ? "Would hold" : "Holds";
  if (holds === 0)
    return (
      <span className="limit-holds">
        No limit {changed ? "would hold" : "holds"}: the instance sets no
        ceiling over it.
      </span>
    );
  if (own === 0)
    return (
      <span className="limit-holds">
        {word}: {amount(field, holds)}, the instance's ceiling.
      </span>
    );
  return (
    <span className="limit-holds">
      {word}: {amount(field, holds)}
      {ceiling > 0
        ? `, under the instance's ceiling of ${amount(field, ceiling)}.`
        : "."}
    </span>
  );
}

// The server writes its words to be read after something else too, in
// lower case; on their own they start a sentence.
const sentence = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

// The four limits' fields, each with what it holds runs or people to, and
// a note of its own under it if given.
function LimitFields({
  draft,
  bad,
  over,
  onChange,
  note,
}: {
  draft: Draft;
  bad: Set<LimitKey>;
  // The fields above their ceilings.
  over?: Set<LimitKey>;
  onChange: (d: Draft) => void;
  note?: (key: LimitKey) => ReactNode;
}) {
  return (
    <div className="limits-grid">
      {LIMIT_FIELDS.map((f) => (
        <LimitField
          key={f.key}
          label={f.label}
          prefix={f.prefix}
          suffix={f.suffix}
          value={draft[f.key]}
          onChange={(v) => onChange({ ...draft, [f.key]: v })}
          invalid={bad.has(f.key) || !!over?.has(f.key)}
          inputMode={f.whole ? "numeric" : "decimal"}
          note={note?.(f.key)}
        >
          {f.help}
        </LimitField>
      ))}
    </div>
  );
}

// Saves limits from their form, saying where they are set what the server
// refused, in its words, and anything else as the page's error.
function useLimitsSave<T extends Limits>(
  save: (l: Limits) => Promise<T>,
  saved: (l: T) => void,
  onError: (err: unknown) => void,
) {
  const [saving, setSaving] = useState(false);
  const [refused, setRefused] = useState<string | null>(null);
  const submit = async (l: Limits) => {
    setSaving(true);
    setRefused(null);
    try {
      saved(await save(l));
    } catch (err) {
      if (err instanceof ApiError && err.status === 400)
        setRefused(errorText(err));
      else onError(err);
    } finally {
      setSaving(false);
    }
  };
  return { saving, refused, submit };
}

// The foot of a form of limits: what the server refused, whether there is
// anything to save, and saving it.
function LimitsFoot({
  refused,
  dirty,
  saving,
  ready,
  label,
}: {
  refused: string | null;
  dirty: boolean;
  saving: boolean;
  ready: boolean;
  label: string;
}) {
  return (
    <div className="spending-card-foot">
      {refused && (
        <span className="limits-refused" role="alert">
          {refused}
        </span>
      )}
      <span className="spending-card-sub">
        {dirty ? "Unsaved changes" : "Saved"}
      </span>
      <button
        type="submit"
        className="spending-save"
        disabled={!dirty || saving || !ready}
      >
        {saving ? "Saving…" : label}
      </button>
    </div>
  );
}

export function SpendingSection({
  admin,
  onError,
}: {
  admin: boolean;
  onError: (err: unknown) => void;
}) {
  const [spending, setSpending] = useState<Spending | null>(null);
  const [limits, setLimits] = useState<ProjectLimits | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);

  const took = (l: ProjectLimits) => {
    setLimits(l);
    setDraft(draftOf(l));
  };
  const refresh = useCallback(async () => {
    const [s, l] = await Promise.all([getSpending(), getLimits()]);
    setSpending(s);
    setLimits(l);
    setDraft(draftOf(l));
  }, []);
  useEffect(() => {
    refresh().catch(onError);
  }, [refresh, onError]);
  const { saving, refused, submit } = useLimitsSave(saveLimits, took, onError);

  const typed = draft ? readDraft(draft) : null;
  // The fields above their ceilings, which the server refuses; marked, but
  // left to it to say, as the ceilings may have changed since.
  const over = new Set(
    typed && limits
      ? LIMIT_FIELDS.filter(
          (f) =>
            limits.ceilings[f.key] > 0 &&
            typed.limits[f.key] > limits.ceilings[f.key],
        ).map((f) => f.key)
      : [],
  );
  const onSave = (e: FormEvent) => {
    e.preventDefault();
    if (typed) submit(typed.limits);
  };

  const dirty =
    limits !== null && typed !== null && !sameLimits(typed.limits, limits);
  const holding = limits?.holding ?? null;
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
  const unpriced = spending
    ? spending.people.reduce((sum, p) => sum + p.unpriced, 0)
    : 0;
  // Bars say how amounts compare; where every amount reads the same, they
  // would only show differences too small to read, so they are left out.
  const runBars =
    spending !== null &&
    new Set(spending.runs.map((r) => dollars(r.spent))).size > 1;
  const personBars =
    spending !== null &&
    ((holding?.userMonthSpend ?? 0) > 0 ||
      new Set(spending.people.map((p) => dollars(p.spent))).size > 1);

  return (
    <section className="flow-section spending" aria-label="Spending and limits">
      <SettingsHead
        title="Spending & limits"
        lead="What agents spend on their models this month, and what runs and people may use in this project."
        more="Spending is at the prices of pi's catalog or of the worker's models.json. A model without a known price is counted at the largest models' price, so a limit isn't passed unnoticed. Months are UTC's. The project's owners set its limits, each at most the ceiling an admin of Spoletta sets over every project; what holds is the lesser of the two."
      />

      <div className="spending-stats">
        <div className="spending-stat">
          <span className="spending-stat-label">
            Spent{spending && ` in ${monthName(spending.month)}`}
          </span>
          <span className="spending-stat-value">
            {spending ? dollars(totalSpent) : "…"}
          </span>
          {holding && holding.userMonthSpend > 0 && (
            <span className="spending-stat-note">
              each person up to {dollars(holding.userMonthSpend)}
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
          <span className="spending-stat-label">Without a known price</span>
          <span className="spending-stat-value">
            {spending ? unpriced.toLocaleString() : "…"}
          </span>
          {spending && (
            <span className="spending-stat-note">
              {unpriced > 0
                ? `${unpriced === 1 ? "call" : "calls"} counted at the largest models' price`
                : "every call had its model's price"}
            </span>
          )}
        </div>
      </div>

      <div className="spending-card">
        <div className="spending-card-head">
          <h3>Limits</h3>
          <span className="spending-card-sub">
            {admin
              ? "Leave a field empty for none of the project's own."
              : "What holds in this project: its owners' limits, under the instance's ceilings."}
          </span>
        </div>
        {admin ? (
          draft &&
          typed &&
          limits && (
            <form onSubmit={onSave}>
              <LimitFields
                draft={draft}
                bad={typed.bad}
                over={over}
                onChange={setDraft}
                note={(key) =>
                  !typed.bad.has(key) && (
                    <CeilingNote
                      field={key}
                      own={typed.limits[key]}
                      ceiling={limits.ceilings[key]}
                      changed={typed.limits[key] !== limits[key]}
                    />
                  )
                }
              />
              <LimitsFoot
                refused={refused}
                dirty={dirty}
                saving={saving}
                ready={typed.bad.size === 0}
                label="Save limits"
              />
            </form>
          )
        ) : (
          limits && (
            <dl className="limits-read">
              {LIMIT_FIELDS.map((f) => (
                <div key={f.key}>
                  <dt>{f.held}</dt>
                  <dd>{amount(f.key, limits.holding[f.key])}</dd>
                  {limits.ceilings[f.key] > 0 &&
                    limits.holding[f.key] === limits.ceilings[f.key] && (
                      <dd className="limits-ceiling">the instance's ceiling</dd>
                    )}
                </div>
              ))}
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
                const cap = holding?.userMonthSpend || 0;
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
                        {p.user === null ? "Runs nobody started" : p.name}
                      </span>
                      <span className="spending-amount">
                        {dollars(p.spent)}
                      </span>
                    </div>
                    {personBars && (
                      <Bar share={share} over={cap > 0 && p.spent >= cap} />
                    )}
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
                {holding &&
                  holding.runSpend > 0 &&
                  `; each may spend ${dollars(holding.runSpend)}`}
              </span>
            </div>
            <ol className="spending-bars">
              {spending.runs.map((r, i) => (
                <li key={r.run}>
                  <div className="spending-bar-row">
                    <a
                      className="spending-run"
                      href={pageHash(`#/flows/${r.flow}/runs/${r.run}`)}
                    >
                      <span className="spending-rank">{i + 1}</span>
                      <span className="spending-run-name">{r.name}</span>
                      <span className="spending-run-id">run {r.run}</span>
                    </a>
                    <span className="spending-amount">{dollars(r.spent)}</span>
                  </div>
                  {runBars && (
                    <Bar share={topRun > 0 ? r.spent / topRun : 0} />
                  )}
                </li>
              ))}
            </ol>
          </div>
        </div>
      )}
    </section>
  );
}

// The ceilings over every project's limits, for the instance's admins, in
// the settings' Instance group: a project's owners may set no limit above
// one, and a ceiling lowered holds at once over a limit set before.
export function CeilingsSection({
  onError,
}: {
  onError: (err: unknown) => void;
}) {
  const [ceilings, setCeilings] = useState<Limits | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const took = (c: Limits) => {
    setCeilings(c);
    setDraft(draftOf(c));
  };
  useEffect(() => {
    getCeilings().then((c) => {
      setCeilings(c);
      setDraft(draftOf(c));
    }, onError);
  }, [onError]);
  const { saving, refused, submit } = useLimitsSave(saveCeilings, took, onError);

  const typed = draft ? readDraft(draft) : null;
  const dirty =
    ceilings !== null && typed !== null && !sameLimits(typed.limits, ceilings);
  const onSave = (e: FormEvent) => {
    e.preventDefault();
    if (typed) submit(typed.limits);
  };

  return (
    <section className="flow-section spending" aria-label="Ceilings">
      <SettingsHead
        title="Ceilings"
        lead="The most any project's runs and people may use, whatever its owners set."
        more="Each project's owners set its own limits in its Spending & limits tab, none above these. What holds in a project is the lesser of its own limit and the ceiling, so a ceiling lowered holds at once, even over a limit set higher before; such a project's owners can save no limits until they bring that one down. Leave a field empty for no ceiling."
      />
      <div className="spending-card">
        <div className="spending-card-head">
          <h3>Over every project</h3>
          <span className="spending-card-sub">
            Leave a field empty for no ceiling.
          </span>
        </div>
        {draft && typed && (
          <form onSubmit={onSave}>
            <LimitFields draft={draft} bad={typed.bad} onChange={setDraft} />
            <LimitsFoot
              refused={refused}
              dirty={dirty}
              saving={saving}
              ready={typed.bad.size === 0}
              label="Save ceilings"
            />
          </form>
        )}
      </div>
    </section>
  );
}

// A limit's field: its label, the input with its unit beside it, what the
// limit holds runs or people to, and a note of its own, such as what holds
// under the instance's ceiling.
function LimitField({
  label,
  prefix,
  suffix,
  value,
  onChange,
  invalid,
  inputMode,
  note,
  children,
}: {
  label: string;
  prefix?: string;
  suffix?: string;
  value: string;
  onChange: (v: string) => void;
  invalid: boolean;
  inputMode: "decimal" | "numeric";
  note?: ReactNode;
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
      {note && <small className="limit-note">{note}</small>}
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
