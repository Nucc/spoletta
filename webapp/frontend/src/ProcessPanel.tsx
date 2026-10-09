import { useEffect, useRef, useState } from 'react'
import {
  DEFAULT_SENTRY,
  THINKING_LEVELS,
  listObjects,
  updateProcess,
  type Entries,
  type EntryValue,
  type Flow,
  type MCPServer,
  type Repository,
  type Models,
  type ProcessKind,
  type ProcessSettings,
  type RunInput,
  type SentrySettings,
  type ThinkingLevel,
} from './api'
import { Data } from './Data'
import { quantityWords, type Quantity } from './quantity'
import { fieldsOf, holdsFile, shapeOf, type Field } from './structure'
import { MCPServerPicker } from './MCPSettings'
import { RepositoryPicker } from './FlowSettings'
import type { RunStatus } from './ProcessNode'

type Fields = {
  name: string
  kind: ProcessKind
  prompt: string
  script: string
  model: string
  thinking: ThinkingLevel | ''
  recording: boolean
  settings: ProcessSettings
}

const SAVE_DELAY_MS = 500

const SCRIPT_PLACEHOLDER = `# e.g. the latest unresolved issues in Sentry:
curl -sf -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" \\
  "https://sentry.io/api/0/organizations/my-org/issues/?query=is:unresolved&limit=10" |
  jq '[.[] | {id: .shortId, title, permalink}]'`

const KINDS: { value: ProcessKind; label: string }[] = [
  { value: 'agent', label: 'Agent' },
  { value: 'script', label: 'Script' },
  { value: 'human', label: 'Human' },
  { value: 'flow', label: 'Flow' },
  { value: 'subflow', label: 'Another flow' },
  { value: 'sentry', label: 'Sentry' },
]

// Sentry's orders for issues, before the limit applies.
const SENTRY_SORTS = [
  { value: 'date', label: 'Last seen' },
  { value: 'new', label: 'First seen' },
  { value: 'freq', label: 'Events' },
  { value: 'user', label: 'Users' },
  { value: 'trends', label: 'Trends' },
]

// As many as a flow process runs at once.
const MAX_SENTRY_ISSUES = 50

// An object a human process generates without deciding on its states, which
// the person carrying it out fills in.
export type EntryObject = Quantity & {
  id: number
  name: string
  // The fields the person fills in, nested as the objects it is made of are.
  fields: Field[]
}

// A human process waiting for its decision during a run.
export type Review = {
  inputs: RunInput[]
  // The states the process's output links name, which it decides between.
  options: { state: number; object: string; name: string }[]
  // The objects the person fills in.
  entries: EntryObject[]
  // Null when there are no states to set, which ends the branch, unless
  // there are objects filled in. Resolves to whether the server took it.
  onDecide: (state: number | null, instructions: string, entries?: Entries) => Promise<boolean>
}

// Edits one process; mount with key={id} so switching processes resets the form.
export default function ProcessPanel({
  id,
  initial,
  models,
  mcpServers,
  readOnly = false,
  run,
  review,
  onChange,
  onZoomIn,
  flows,
  onFlowSaved,
  takes,
  generates,
  shapes,
  onOpenMCPSettings,
  repositories,
  onOpenFlowSettings,
  onClose,
  onError,
}: {
  id: number
  initial: Fields
  // Models pi offers, or null while they load.
  models: Models | null
  // The MCP servers agents can use, or null while they load.
  mcpServers: MCPServer[] | null
  // Shows the process without letting it change, such as as a past run's version has it.
  readOnly?: boolean
  // How this process did in the latest flow run, if it has run.
  run?: { status?: RunStatus; output?: string; error?: string }
  review?: Review
  onChange: (fields: Fields) => void
  // Shows a flow process's board, or the flow a subflow process runs.
  onZoomIn: () => void
  // The flows a subflow process can run, null while they load, and the
  // names of the objects it takes and generates.
  flows: Flow[] | null
  // A subflow process's flow was saved, which brings its outcomes onto the board.
  onFlowSaved: () => void
  takes: string[]
  generates: string[]
  // The shape of what each of those objects holds, by name; see shapeOf.
  shapes: Record<string, string>
  onOpenMCPSettings: () => void
  // The flow's repositories, which agents and scripts pick from.
  repositories: Repository[]
  onOpenFlowSettings: () => void
  onClose: () => void
  onError: (err: unknown) => void
}) {
  const [name, setName] = useState(initial.name)
  const [kind, setKind] = useState(initial.kind)
  const [prompt, setPrompt] = useState(initial.prompt)
  const [script, setScript] = useState(initial.script)
  const [model, setModel] = useState(initial.model)
  const [thinking, setThinking] = useState(initial.thinking)
  const [recording, setRecording] = useState(initial.recording)
  const [settings, setSettings] = useState(initial.settings)
  const saved = useRef(initial)
  const pending = useRef<Partial<Fields> | null>(null)
  const timer = useRef<number>(undefined)
  const bodyRef = useRef<HTMLTextAreaElement>(null)

  const flush = () => {
    window.clearTimeout(timer.current)
    const patch = pending.current
    pending.current = null
    if (!patch) return
    const kindNow = saved.current.kind
    updateProcess(id, patch)
      .then(() => {
        if (kindNow === 'subflow' && (patch.kind !== undefined || patch.settings !== undefined)) onFlowSaved()
      })
      .catch(onError)
  }

  // Autosave shortly after typing stops, and flush whatever is left when the panel closes.
  useEffect(() => {
    const trimmed = name.trim()
    const patch: Partial<Fields> = {}
    if (trimmed !== '' && trimmed !== saved.current.name) patch.name = trimmed
    if (kind !== saved.current.kind) patch.kind = kind
    if (prompt !== saved.current.prompt) patch.prompt = prompt
    if (script !== saved.current.script) patch.script = script
    if (model !== saved.current.model) patch.model = model
    if (thinking !== saved.current.thinking) patch.thinking = thinking
    if (recording !== saved.current.recording) patch.recording = recording
    if (JSON.stringify(settings) !== JSON.stringify(saved.current.settings)) patch.settings = settings
    if (Object.keys(patch).length === 0) return

    saved.current = { ...saved.current, ...patch }
    pending.current = { ...pending.current, ...patch }
    onChange(saved.current)
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(flush, SAVE_DELAY_MS)
  }, [name, kind, prompt, script, model, thinking, recording, settings])

  useEffect(() => flush, [])

  // Absent means every repository, so unpicking the last one keeps [].
  const repositoryPicker = (
    <RepositoryPicker
      repositories={repositories}
      selected={settings.repositories}
      readOnly={readOnly}
      onChange={(names) =>
        setSettings(({ repositories: _, ...rest }) => (names === undefined ? rest : { ...rest, repositories: names }))
      }
      onOpenSettings={onOpenFlowSettings}
    />
  )

  // Both bodies are kept, so switching type back and forth loses nothing.
  useEffect(() => {
    bodyRef.current?.focus()
  }, [kind])

  return (
    <aside className="panel" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <header className="panel-header">
        <h2>Process{readOnly && <span className="run-badge">read-only</span>}</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close panel">
          ×
        </button>
      </header>
      <label className="panel-field">
        <span>Name</span>
        <input value={name} readOnly={readOnly} onChange={(e) => setName(e.target.value)} />
        {name.trim() === '' && <small className="panel-warning">Name can't be empty.</small>}
      </label>
      <div className="panel-field" role="radiogroup" aria-label="Type">
        <span>Type</span>
        <div className="segmented">
          {KINDS.map((k) => (
            <button
              key={k.value}
              role="radio"
              aria-checked={kind === k.value}
              className={kind === k.value ? 'selected' : ''}
              disabled={readOnly}
              onClick={() => setKind(k.value)}
            >
              {k.label}
            </button>
          ))}
        </div>
      </div>
      {kind === 'agent' && (
        <div className="panel-row">
          <label className="panel-field">
            <span>Model</span>
            <select value={model} disabled={readOnly} onChange={(e) => setModel(e.target.value)}>
              <option value="">Default{models?.default ? ` (${models.default})` : ''}</option>
              {/* A model dropped from pi's list stays selectable so it isn't silently replaced. */}
              {model !== '' && !models?.models.includes(model) && <option value={model}>{model}</option>}
              {models?.models.map((m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ))}
            </select>
          </label>
          <label className="panel-field">
            <span>Thinking</span>
            <select value={thinking} disabled={readOnly} onChange={(e) => setThinking(e.target.value as ThinkingLevel | '')}>
              <option value="">Default</option>
              {THINKING_LEVELS.map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </select>
          </label>
        </div>
      )}
      {kind === 'sentry' ? (
        <SentryFields
          key="sentry"
          settings={settings.sentry ?? DEFAULT_SENTRY}
          readOnly={readOnly}
          onChange={(sentry) => setSettings((s) => ({ ...s, sentry }))}
        />
      ) : kind === 'flow' ? (
        <div key="flow" className="panel-field">
          <p className="panel-note">
            Made of processes on a board of its own. The objects it takes show at the board's start, the ones it
            generates at its end.
          </p>
          <label className="panel-check">
            <input type="checkbox" checked={recording} disabled={readOnly} onChange={(e) => setRecording(e.target.checked)} />
            <span>Record each time it runs as a run of its own</span>
          </label>
          <button className="panel-button" onClick={onZoomIn}>
            Open its board
          </button>
        </div>
      ) : kind === 'subflow' ? (
        <SubflowFields
          key="subflow"
          flows={flows}
          flow={settings.flow}
          takes={takes}
          generates={generates}
          shapes={shapes}
          readOnly={readOnly}
          onChange={(flow) => setSettings(({ flow: _, ...rest }) => (flow === undefined ? rest : { ...rest, flow }))}
          onOpen={onZoomIn}
          onError={onError}
        />
      ) : kind === 'agent' ? (
        <>
          <label key="agent" className="panel-field panel-field-grow">
            <span>Prompt</span>
            <textarea
              ref={bodyRef}
              readOnly={readOnly}
              value={prompt}
              placeholder="Describe what this process should do…"
              onChange={(e) => setPrompt(e.target.value)}
            />
          </label>
          <MCPServerPicker
            servers={mcpServers}
            selected={settings.mcpServers ?? []}
            readOnly={readOnly}
            onChange={(ids) =>
              setSettings(({ mcpServers: _, ...rest }) => (ids.length > 0 ? { ...rest, mcpServers: ids } : rest))
            }
            onOpenSettings={onOpenMCPSettings}
          />
          {repositoryPicker}
        </>
      ) : kind === 'human' ? (
        // The prompt doubles as the person's instructions; switching kinds keeps it.
        <label key="human" className={`panel-field${review ? '' : ' panel-field-grow'}`}>
          <span>Instructions for the person</span>
          <textarea
            ref={bodyRef}
            readOnly={readOnly}
            value={prompt}
            rows={review ? 3 : undefined}
            placeholder="What should the person check before deciding, or fill in?"
            onChange={(e) => setPrompt(e.target.value)}
          />
        </label>
      ) : (
        <label key="script" className="panel-field panel-field-grow">
          <span>Script</span>
          <textarea
            ref={bodyRef}
            readOnly={readOnly}
            value={script}
            placeholder={SCRIPT_PLACEHOLDER}
            spellCheck={false}
            onChange={(e) => setScript(e.target.value)}
          />
          <small className="panel-note">
            Runs with bash, curl and jq. Each object it takes is a file in <code>$INPUTS</code>, named after the object
            with spaces as underscores. What it prints is its output; a list of items is a JSON array. A non-zero exit
            fails it. With repositories, it runs in their workspace, <code>$WORKSPACE</code>, on the branch{' '}
            <code>$BRANCH</code>.
          </small>
        </label>
      )}
      {kind === 'script' && repositoryPicker}
      {review && <ReviewForm review={review} />}
      {run?.status === 'active' && <p className="panel-note">Running…</p>}
      {run?.error &&
        (run.status === 'duplicate' ? (
          <div className="panel-run">
            <span>Already processed; the run was terminated</span>
            <pre>{run.error}</pre>
          </div>
        ) : (
          <div className="panel-run">
            <span>Last run failed</span>
            <pre className="failed">{run.error}</pre>
          </div>
        ))}
      {run?.output && (
        <div className="panel-run">
          <span>{kind === 'human' ? 'Last decision' : 'Last run output'}</span>
          <Data text={run.output} />
        </div>
      )}
    </aside>
  )
}

function SentryFields({
  settings: s,
  readOnly,
  onChange,
}: {
  settings: SentrySettings
  readOnly: boolean
  onChange: (s: SentrySettings) => void
}) {
  const set = (patch: Partial<SentrySettings>) => onChange({ ...s, ...patch })
  // Typing a limit may pass through an empty field; only whole numbers in range are kept.
  const [limit, setLimit] = useState(String(s.limit))
  const slug = (v: string) => v.trim().replace(/[^A-Za-z0-9_-]/g, '')

  return (
    <div className="panel-field">
      <div className="panel-row">
        <label className="panel-field">
          <span>Organization</span>
          <input value={s.organization} readOnly={readOnly} placeholder="my-org" onChange={(e) => set({ organization: slug(e.target.value) })} />
          {s.organization === '' && <small className="panel-warning">The organization's slug is needed.</small>}
        </label>
        <label className="panel-field">
          <span>Project</span>
          <input value={s.project} readOnly={readOnly} placeholder="All projects" onChange={(e) => set({ project: slug(e.target.value) })} />
        </label>
      </div>
      <label className="panel-field">
        <span>Query</span>
        <input
          value={s.query}
          readOnly={readOnly}
          placeholder="is:unresolved"
          spellCheck={false}
          onChange={(e) => set({ query: e.target.value })}
        />
      </label>
      <div className="panel-row">
        <label className="panel-field">
          <span>Environment</span>
          <input value={s.environment} readOnly={readOnly} placeholder="Any" onChange={(e) => set({ environment: e.target.value.trim() })} />
        </label>
        <label className="panel-field">
          <span>Sort by</span>
          <select value={s.sort} disabled={readOnly} onChange={(e) => set({ sort: e.target.value })}>
            {SENTRY_SORTS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
        </label>
        <label className="panel-field">
          <span>Limit</span>
          <input
            type="number"
            min={1}
            max={MAX_SENTRY_ISSUES}
            value={limit}
            readOnly={readOnly}
            onChange={(e) => {
              setLimit(e.target.value)
              const n = Number(e.target.value)
              if (Number.isInteger(n) && n >= 1 && n <= MAX_SENTRY_ISSUES) set({ limit: n })
            }}
          />
        </label>
      </div>
      <small className="panel-note">
        Fetches the issues with the token in <code>SENTRY_AUTH_TOKEN</code>. Each attribute of the list it generates takes
        the issue's field of the same name, such as <code>shortId</code>, <code>title</code>, <code>permalink</code>{' '}
        or <code>metadata.value</code>; without attributes, the list holds the main fields.
      </small>
    </div>
  )
}

// What the person typed for one object: the text of an object without
// fields, or its fields, each the text typed for it, the fields of an object
// it is made of, or for a list one such value per item. It is sent as it is.
type Draft = EntryValue

// One item of a field: its fields, or the text typed for it.
const emptyOne = (fields: Field[]): Draft => (fields.length === 0 ? '' : Object.fromEntries(fields.map((f) => [f.name, emptyValue(f)])))
// A list starts with as many items as it holds at least, and one if it may
// have none.
const emptyValue = (f: Pick<Field, 'fields' | 'many' | 'min'>): Draft =>
  f.many ? Array.from({ length: Math.max(f.min, 1) }, () => emptyOne(f.fields)) : emptyOne(f.fields)
const emptyDraft = (o: EntryObject): Draft => (o.fields.length === 0 ? '' : emptyValue(o))

// Every field is needed: the objects are what the run goes on from. A list
// may have no items, unless it holds at least some; the form keeps it within
// how many it holds.
const filled = (d: Draft): boolean =>
  typeof d === 'string' ? d.trim() !== '' : Array.isArray(d) ? d.every(filled) : Object.values(d).every(filled)

function ReviewForm({ review }: { review: Review }) {
  const [instructions, setInstructions] = useState('')
  const [drafts, setDrafts] = useState<Record<number, Draft>>(() =>
    Object.fromEntries(review.entries.map((o) => [o.id, emptyDraft(o)])),
  )
  const [sending, setSending] = useState(false)
  // Name the object only when the decision is about more than one.
  const objects = new Set(review.options.map((o) => o.object))
  const entering = review.entries.length > 0
  const files = review.entries.flatMap((o) => o.fields.filter(holdsFile).map((f) => `${o.name}: ${f.name}`))
  const ready = !sending && files.length === 0 && review.entries.every((o) => drafts[o.id] !== undefined && filled(drafts[o.id]))

  const decide = async (state: number | null) => {
    if (!entering) {
      void review.onDecide(state, instructions.trim())
      return
    }
    const entries: Entries = Object.fromEntries(review.entries.map((o) => [String(o.id), drafts[o.id]]))
    setSending(true)
    // Kept as typed when the server turns it down, to fix and send again.
    await review.onDecide(state, instructions.trim(), entries)
    setSending(false)
  }

  return (
    <section className="review" aria-label={entering ? 'Input' : 'Review'}>
      <span className="review-title">{entering ? 'Waiting for your input' : 'Waiting for your review'}</span>
      {review.inputs.length === 0
        ? !entering && <p className="panel-note">This process has no generated inputs to review.</p>
        : review.inputs.map((input, i) => (
            <div key={i} className="panel-run">
              <span>
                {input.name}
                {input.state && ` (${input.state})`}
              </span>
              <Data text={input.content} />
            </div>
          ))}
      {review.entries.map((o) => (
        <EntryFields
          key={o.id}
          object={o}
          draft={drafts[o.id] ?? emptyDraft(o)}
          onChange={(d) => setDrafts((ds) => ({ ...ds, [o.id]: d }))}
        />
      ))}
      {files.length > 0 && (
        <small className="panel-note">
          Files can't be filled in by hand, so this can't be sent: {files.join(', ')}. Turn those attributes into text.
        </small>
      )}
      {(!entering || review.options.length > 0) && (
        <label className="panel-field">
          <span>Instructions</span>
          <textarea
            rows={3}
            value={instructions}
            placeholder="Optional. Passed with the object to the processes that take it in the state you set."
            onChange={(e) => setInstructions(e.target.value)}
          />
        </label>
      )}
      {review.options.length === 0 ? (
        !entering && (
          <small className="panel-note">
            Link this process to an object and pick, on the link, the states it decides between. Without any, the branch
            ends here.
          </small>
        )
      ) : (
        <span className="panel-note">Set to:</span>
      )}
      <div className="review-actions">
        {review.options.length === 0 && (
          <button disabled={entering && !ready} onClick={() => decide(null)}>
            {entering ? 'Submit' : 'Done'}
          </button>
        )}
        {review.options.map((o) => (
          <button key={o.state} disabled={entering && !ready} onClick={() => decide(o.state)}>
            {objects.size > 1 && `${o.object}: `}
            {o.name}
          </button>
        ))}
      </div>
    </section>
  )
}

// The fields of one object the person fills in: one per field, nested for
// the objects it is made of, for each item of a list, or a single text
// without fields.
function EntryFields({ object, draft, onChange }: { object: EntryObject; draft: Draft; onChange: (d: Draft) => void }) {
  if (typeof draft === 'string')
    return (
      <label className="panel-field">
        <span>{object.name}</span>
        <textarea rows={3} value={draft} onChange={(e) => onChange(e.target.value)} />
      </label>
    )
  return (
    <fieldset className="entry">
      <legend>{object.name}</legend>
      <EntryValueFields fields={object.fields} quantity={object} value={draft} onChange={onChange} />
    </fieldset>
  )
}

// A value with fields, or for a list its items, each with a header to remove
// it, as many as the list holds.
function EntryValueFields({
  fields,
  quantity,
  value,
  onChange,
}: {
  fields: Field[]
  quantity: Quantity
  value: Draft
  onChange: (d: Draft) => void
}) {
  if (!quantity.many) return <EntryItem fields={fields} value={value} onChange={onChange} />
  const items = Array.isArray(value) ? value : []
  const words = quantityWords(quantity)
  const fewest = items.length <= quantity.min
  const most = quantity.max !== null && items.length >= quantity.max
  return (
    <>
      {words && <small className="panel-note">Holds {words} items.</small>}
      {items.map((item, i) => (
        <div key={i} className="entry-item">
          <div className="entry-item-header">
            <span>Item {i + 1}</span>
            <button
              aria-label={`Remove item ${i + 1}`}
              title={fewest ? `It holds ${words} items` : 'Remove item'}
              disabled={fewest}
              onClick={() => onChange(items.filter((_, j) => j !== i))}
            >
              ×
            </button>
          </div>
          <EntryItem fields={fields} value={item} onChange={(d) => onChange(items.map((it, j) => (j === i ? d : it)))} />
        </div>
      ))}
      <button
        className="entry-add"
        disabled={most}
        title={most ? `It holds ${words} items` : undefined}
        onClick={() => onChange([...items, emptyOne(fields)])}
      >
        Add item
      </button>
    </>
  )
}

// One item: a field per field, an object it is made of in a box of its own.
function EntryItem({ fields, value, onChange }: { fields: Field[]; value: Draft; onChange: (d: Draft) => void }) {
  if (fields.length === 0) {
    return <input value={typeof value === 'string' ? value : ''} onChange={(e) => onChange(e.target.value)} />
  }
  const item = typeof value === 'object' && !Array.isArray(value) ? value : {}
  const set = (name: string, v: Draft) => onChange({ ...item, [name]: v })
  return (
    <>
      {fields.map((f) =>
        f.fields.length > 0 || f.many ? (
          <fieldset key={f.name} className="entry entry-nested">
            <legend>{f.name}</legend>
            <EntryValueFields fields={f.fields} quantity={f} value={item[f.name] ?? emptyValue(f)} onChange={(v) => set(f.name, v)} />
          </fieldset>
        ) : (
          <label key={f.name} className="panel-field">
            <span>{f.name}</span>
            <input
              value={typeof item[f.name] === 'string' ? (item[f.name] as string) : ''}
              disabled={f.file}
              placeholder={f.file ? "A file; can't be filled in by hand" : f.definition || undefined}
              title={f.definition || undefined}
              onChange={(e) => set(f.name, e.target.value)}
            />
          </label>
        ),
      )}
    </>
  )
}

// The flow a subflow process runs, and how its objects meet that flow's: by
// name, on that flow's top board. Names with no namesake there are marked.
function SubflowFields({
  flows,
  flow,
  takes,
  generates,
  shapes,
  readOnly,
  onChange,
  onOpen,
  onError,
}: {
  flows: Flow[] | null
  flow: number | undefined
  takes: string[]
  generates: string[]
  shapes: Record<string, string>
  readOnly: boolean
  onChange: (flow: number | undefined) => void
  onOpen: () => void
  onError: (err: unknown) => void
}) {
  // The shapes of what the objects on that flow's top board hold, by name,
  // once loaded.
  const [names, setNames] = useState<Map<string, string> | null>(null)
  useEffect(() => {
    setNames(null)
    if (flow === undefined) return
    let stale = false
    listObjects(flow)
      .then(
        (os) =>
          !stale &&
          setNames(new Map(os.filter((o) => o.parent === null && o.owner === null).map((o) => [o.name, shapeOf(fieldsOf(os, o.id))]))),
      )
      .catch((err) => !stale && onError(err))
    return () => {
      stale = true
    }
  }, [flow])

  const gone = flow !== undefined && flows !== null && !flows.some((f) => f.id === flow)
  const matches = (label: string, list: string[]) =>
    list.length > 0 && (
      <div className="panel-field">
        <span>{label}</span>
        <ul className="subflow-names">
          {list.map((n) => {
            const unmatched = names !== null && !names.has(n)
            // Handed over as it is, so what doesn't fit fails there.
            const unlike = names !== null && !unmatched && shapes[n] !== undefined && names.get(n) !== shapes[n]
            return (
              <li key={n} className={unmatched || unlike ? 'unmatched' : undefined}>
                {n}
                {unmatched && <small> · no object of that name on its top board</small>}
                {unlike && <small> · its namesake there holds different fields</small>}
              </li>
            )
          })}
        </ul>
      </div>
    )

  return (
    <>
      <label className="panel-field">
        <span>Flow it runs</span>
        <select
          value={flow ?? ''}
          disabled={readOnly}
          onChange={(e) => onChange(e.target.value === '' ? undefined : Number(e.target.value))}
        >
          <option value="">Pick a flow…</option>
          {flow !== undefined && (flows === null || gone) && (
            <option value={flow}>{gone ? `Flow #${flow} (deleted)` : `Flow #${flow}`}</option>
          )}
          {flows?.map((f) => (
            <option key={f.id} value={f.id}>
              {f.name}
            </option>
          ))}
        </select>
      </label>
      <p className="panel-note">
        Each time it runs is a run of that flow. The objects it takes are given to the objects of the same names on
        that flow's top board, and it generates what the run leaves of the objects named like the ones it generates.
      </p>
      {matches('Takes', takes)}
      {matches('Generates', generates)}
      {flow !== undefined && !gone && (
        <button className="panel-button" onClick={onOpen}>
          Open the flow
        </button>
      )}
    </>
  )
}
