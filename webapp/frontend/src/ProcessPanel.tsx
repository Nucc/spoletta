import { useEffect, useRef, useState } from 'react'
import {
  THINKING_LEVELS,
  updateProcess,
  type Models,
  type Outcome,
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
  // Outcomes that have a route; requesting changes needs somewhere to send them.
  routed: Outcome[]
  onDecide: (outcome: Outcome, instructions: string) => void
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
  // Outcome of this process in the latest flow run, if it has run.
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
  const canRequestChanges = review.routed.includes('changes') && instructions.trim() !== ''

  return (
    <section className="review" aria-label="Review">
      <span className="review-title">Waiting for your review</span>
      {review.inputs.length === 0 ? (
        <p className="panel-note">This process has no generated inputs to review.</p>
      ) : (
        review.inputs.map((input, i) => (
          <div key={i} className="panel-run">
            <span>{input.name}</span>
            <pre>{input.content}</pre>
          </div>
        ))
      )}
      <label className="panel-field">
        <span>Change request</span>
        <textarea
          rows={3}
          value={instructions}
          placeholder="What should be changed? Sent to the process the changes route leads to."
          onChange={(e) => setInstructions(e.target.value)}
        />
      </label>
      {!review.routed.includes('changes') && (
        <small className="panel-note">Connect the changes handle to a process to request changes.</small>
      )}
      <div className="review-actions">
        <button
          className="request-changes"
          disabled={!canRequestChanges}
          onClick={() => review.onDecide('changes', instructions.trim())}
        >
          Request changes
        </button>
        <button
          className="approve"
          title={review.routed.includes('approved') ? undefined : 'No approved route: the branch ends here'}
          onClick={() => review.onDecide('approved', '')}
        >
          Approve
        </button>
      </div>
    </section>
  )
}
