import { useEffect, useRef, useState, type FormEvent } from 'react'
import type { SharedRecord } from './api'

// A record of a shared struct written by hand: a new one, or one a flow
// wrote corrected, its state and each of its fields, those the struct has
// and any other, which can be added and taken out. Text stays text; a
// number, a yes or no, a list or anything with fields is written as JSON.

type Field = {
  key: number
  name: string
  text: string
  json: boolean
  own: boolean
}

let nextKey = 0

function draftOf(fields: string[], record: SharedRecord | null): Field[] {
  const content =
    record && typeof record.content === 'object' && record.content !== null && !Array.isArray(record.content)
      ? (record.content as Record<string, unknown>)
      : {}
  const names = [...fields, ...Object.keys(content).filter((f) => !fields.includes(f))]
  return names.map((name) => {
    const v = content[name]
    const json = v !== undefined && v !== null && typeof v !== 'string'
    return {
      key: nextKey++,
      name,
      text: v === undefined || v === null ? '' : json ? JSON.stringify(v, null, 2) : (v as string),
      json,
      own: fields.includes(name),
    }
  })
}

export function RecordDialog({
  title,
  fields,
  states,
  record,
  onSave,
  onDelete,
  onClose,
}: {
  title: string
  // The struct's own fields, in the board's order.
  fields: string[]
  states: string[]
  // The record corrected, or none for a new one.
  record: SharedRecord | null
  onSave: (content: Record<string, unknown>, state: string) => Promise<void>
  onDelete?: () => Promise<void>
  onClose: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  // Taken once, so the records asked for again meanwhile don't undo what is typed.
  const [draft, setDraft] = useState(() => draftOf(fields, record))
  const [state, setState] = useState(record?.state ?? '')
  const [error, setError] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])

  const change = (key: number, patch: Partial<Field>) => setDraft((d) => d.map((f) => (f.key === key ? { ...f, ...patch } : f)))

  const run = async (what: () => Promise<void>) => {
    setSaving(true)
    setError(null)
    try {
      await what()
    } catch (err) {
      // The server's reason, without the request it answered.
      setError((err instanceof Error ? err.message : String(err)).replace(/^\w+ \S+: \d+ /, '').trim())
      setSaving(false)
    }
  }

  const save = (e: FormEvent) => {
    e.preventDefault()
    const content: Record<string, unknown> = {}
    for (const f of draft) {
      const name = f.name.trim()
      if (name === '') continue
      if (name in content) return setError(`There are two fields named ${name}.`)
      if (f.json) {
        if (f.text.trim() === '') continue
        try {
          content[name] = JSON.parse(f.text)
        } catch {
          return setError(`${name} isn't JSON; untick JSON to keep it as text.`)
        }
      } else if (f.text !== '') {
        content[name] = f.text
      }
    }
    run(() => onSave(content, state))
  }

  return (
    <dialog ref={ref} className="review-dialog record-dialog" aria-label={title} onClose={onClose} onKeyDown={(e) => e.stopPropagation()}>
      <header className="panel-header">
        <h2>{title}</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <form className="record-form" onSubmit={save}>
        <label className="panel-field">
          State
          <select value={state} onChange={(e) => setState(e.target.value)}>
            <option value="">No state</option>
            {states.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
            {state !== '' && !states.includes(state) && <option value={state}>{state}</option>}
          </select>
        </label>
        <div className="record-fields">
          {draft.map((f) => (
            <div key={f.key} className="record-field">
              <div className="record-field-head">
                {f.own ? (
                  <span className="record-field-name">{f.name}</span>
                ) : (
                  <input
                    className="record-field-name"
                    value={f.name}
                    placeholder="Field name"
                    aria-label="Field name"
                    autoFocus={f.name === ''}
                    onChange={(e) => change(f.key, { name: e.target.value })}
                  />
                )}
                <label className="record-field-json" title="Write the value as JSON: a number, true or false, a list or fields">
                  <input type="checkbox" checked={f.json} onChange={(e) => change(f.key, { json: e.target.checked })} /> JSON
                </label>
                <button
                  type="button"
                  className="link-button danger"
                  title={f.own ? 'Leave this field empty' : 'Take this field out of the record'}
                  onClick={() => (f.own ? change(f.key, { text: '' }) : setDraft((d) => d.filter((x) => x.key !== f.key)))}
                >
                  {f.own ? 'Clear' : 'Remove'}
                </button>
              </div>
              <textarea
                value={f.text}
                rows={Math.min(8, Math.max(1, f.text.split('\n').length))}
                spellCheck={false}
                aria-label={f.name || 'Field value'}
                onChange={(e) => change(f.key, { text: e.target.value })}
              />
            </div>
          ))}
        </div>
        <button
          type="button"
          className="link-button record-add-field"
          onClick={() => setDraft((d) => [...d, { key: nextKey++, name: '', text: '', json: false, own: false }])}
        >
          + Add a field
        </button>
        {error && <p className="panel-warning">{error}</p>}
        <div className="review-actions">
          {onDelete && (
            <button
              type="button"
              className="record-delete"
              disabled={saving}
              onClick={() => window.confirm('Delete this record? Flows using the struct no longer find it.') && run(onDelete)}
            >
              Delete
            </button>
          )}
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" disabled={saving}>
            {record ? 'Save' : 'Add'}
          </button>
        </div>
      </form>
    </dialog>
  )
}
