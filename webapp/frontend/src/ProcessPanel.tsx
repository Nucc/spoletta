import { useEffect, useRef, useState } from 'react'
import { updateProcess } from './api'

type Fields = { name: string; prompt: string }

const SAVE_DELAY_MS = 500

// Edits one process; mount with key={id} so switching processes resets the form.
export default function ProcessPanel({
  id,
  initial,
  onChange,
  onClose,
  onError,
}: {
  id: number
  initial: Fields
  onChange: (fields: Fields) => void
  onClose: () => void
  onError: (err: unknown) => void
}) {
  const [name, setName] = useState(initial.name)
  const [prompt, setPrompt] = useState(initial.prompt)
  const saved = useRef(initial)
  const pending = useRef<Partial<Fields> | null>(null)
  const timer = useRef<number>(undefined)
  const promptRef = useRef<HTMLTextAreaElement>(null)

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
    if (prompt !== saved.current.prompt) patch.prompt = prompt
    if (Object.keys(patch).length === 0) return

    saved.current = { ...saved.current, ...patch }
    pending.current = { ...pending.current, ...patch }
    onChange(saved.current)
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(flush, SAVE_DELAY_MS)
  }, [name, prompt])

  useEffect(() => flush, [])

  useEffect(() => {
    promptRef.current?.focus()
  }, [])

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
      <label className="panel-field panel-field-grow">
        <span>Prompt</span>
        <textarea
          ref={promptRef}
          value={prompt}
          placeholder="Describe what this process should do…"
          onChange={(e) => setPrompt(e.target.value)}
        />
      </label>
    </aside>
  )
}
