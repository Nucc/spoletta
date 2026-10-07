import { useEffect, useRef, useState } from 'react'
import {
  THINKING_LEVELS,
  updateProcess,
  type Models,
  type ProcessKind,
  type RunInput,
  type ThinkingLevel,
} from './api'
import type { RunStatus } from './ProcessNode'

type Fields = {
  name: string
  kind: ProcessKind
  prompt: string
  script: string
  model: string
  thinking: ThinkingLevel | ''
}

const SAVE_DELAY_MS = 500

const KINDS: { value: ProcessKind; label: string }[] = [
  { value: 'agent', label: 'Agent' },
  { value: 'script', label: 'Script' },
  { value: 'human', label: 'Human' },
]

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
  run,
  review,
  onChange,
  onClose,
  onError,
}: {
  id: number
  initial: Fields
  // Models pi offers, or null while they load.
  models: Models | null
  // How this process did in the latest flow run, if it has run.
  run?: { status?: RunStatus; output?: string; error?: string }
  review?: Review
  onChange: (fields: Fields) => void
  onClose: () => void
  onError: (err: unknown) => void
}) {
  const [name, setName] = useState(initial.name)
  const [kind, setKind] = useState(initial.kind)
  const [prompt, setPrompt] = useState(initial.prompt)
  const [script, setScript] = useState(initial.script)
  const [model, setModel] = useState(initial.model)
  const [thinking, setThinking] = useState(initial.thinking)
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
    if (Object.keys(patch).length === 0) return

    saved.current = { ...saved.current, ...patch }
    pending.current = { ...pending.current, ...patch }
    onChange(saved.current)
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(flush, SAVE_DELAY_MS)
  }, [name, kind, prompt, script, model, thinking])

  useEffect(() => flush, [])

  // Both bodies are kept, so switching type back and forth loses nothing.
  useEffect(() => {
    bodyRef.current?.focus()
  }, [kind])

  return (
    <aside className="panel" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <header className="panel-header">
        <h2>Process</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close panel">
          ×
        </button>
      </header>
      <label className="panel-field">
        <span>Name</span>
        <input value={name} onChange={(e) => setName(e.target.value)} />
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
            <select value={model} onChange={(e) => setModel(e.target.value)}>
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
            <select value={thinking} onChange={(e) => setThinking(e.target.value as ThinkingLevel | '')}>
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
      {kind === 'agent' ? (
        <label key="agent" className="panel-field panel-field-grow">
          <span>Prompt</span>
          <textarea
            ref={bodyRef}
            value={prompt}
            placeholder="Describe what this process should do…"
            onChange={(e) => setPrompt(e.target.value)}
          />
        </label>
      ) : kind === 'human' ? (
        // The prompt doubles as the reviewer's instructions; switching kinds keeps it.
        <label key="human" className={`panel-field${review ? '' : ' panel-field-grow'}`}>
          <span>Reviewer instructions</span>
          <textarea
            ref={bodyRef}
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
            value={script}
            placeholder="Script to run for this process…"
            spellCheck={false}
            onChange={(e) => setScript(e.target.value)}
          />
        </label>
      )}
      {review && <ReviewForm review={review} />}
      {run?.status === 'active' && <p className="panel-note">Running…</p>}
      {run?.error && (
        <div className="panel-run">
          <span>Last run failed</span>
          <pre className="failed">{run.error}</pre>
        </div>
      )}
      {run?.output && (
        <div className="panel-run">
          <span>{kind === 'human' ? 'Last decision' : 'Last run output'}</span>
          <pre>{run.output}</pre>
        </div>
      )}
    </aside>
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
            <pre>{input.content}</pre>
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
