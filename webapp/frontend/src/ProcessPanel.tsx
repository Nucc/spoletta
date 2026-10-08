import { useEffect, useRef, useState } from 'react'
import {
  DEFAULT_SENTRY,
  THINKING_LEVELS,
  updateProcess,
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

// A human process waiting for its decision during a run.
export type Review = {
  inputs: RunInput[]
  // The states the process's output links name, which it decides between.
  options: { state: number; object: string; name: string }[]
  // Null when there are no states to set, which ends the branch.
  onDecide: (state: number | null, instructions: string) => void
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
  // Shows a flow process's board.
  onZoomIn: () => void
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
    updateProcess(id, patch).catch(onError)
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
        // The prompt doubles as the reviewer's instructions; switching kinds keeps it.
        <label key="human" className={`panel-field${review ? '' : ' panel-field-grow'}`}>
          <span>Reviewer instructions</span>
          <textarea
            ref={bodyRef}
            readOnly={readOnly}
            value={prompt}
            rows={review ? 3 : undefined}
            placeholder="What should the reviewer check before approving?"
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

function ReviewForm({ review }: { review: Review }) {
  const [instructions, setInstructions] = useState('')
  // Name the object only when the decision is about more than one.
  const objects = new Set(review.options.map((o) => o.object))

  return (
    <section className="review" aria-label="Review">
      <span className="review-title">Waiting for your review</span>
      {review.inputs.length === 0 ? (
        <p className="panel-note">This process has no generated inputs to review.</p>
      ) : (
        review.inputs.map((input, i) => (
          <div key={i} className="panel-run">
            <span>
              {input.name}
              {input.state && ` (${input.state})`}
            </span>
            <Data text={input.content} />
          </div>
        ))
      )}
      <label className="panel-field">
        <span>Instructions</span>
        <textarea
          rows={3}
          value={instructions}
          placeholder="Optional. Passed with the object to the processes that take it in the state you set."
          onChange={(e) => setInstructions(e.target.value)}
        />
      </label>
      {review.options.length === 0 ? (
        <small className="panel-note">
          Link this process to an object and pick, on the link, the states it decides between. Without any, the branch ends here.
        </small>
      ) : (
        <span className="panel-note">Set to:</span>
      )}
      <div className="review-actions">
        {review.options.length === 0 && <button onClick={() => review.onDecide(null, instructions.trim())}>Done</button>}
        {review.options.map((o) => (
          <button key={o.state} onClick={() => review.onDecide(o.state, instructions.trim())}>
            {objects.size > 1 && `${o.object}: `}
            {o.name}
          </button>
        ))}
      </div>
    </section>
  )
}
