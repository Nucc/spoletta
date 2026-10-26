import { useEffect, useRef, useState } from 'react'
import type { Version, Versions } from './api'

const formatTime = (s: string) =>
  new Date(s).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' })

// What saved a version on its own, as the list says it, or made the flow
// it is the first of.
const REASONS: Record<Version['reason'], string> = {
  '': '',
  run: 'Saved before a run',
  assistant: 'Saved before the assistant changed the flow',
  import: 'Saved before a setup file was imported',
  template: 'Made from a template',
}

// Every saved version of the board, newest first, with who saved it, why,
// and the note they left. Going back to one rewrites the board as it was;
// the newer versions stay, and saving adds one on top. The version
// published is the one the runs nobody starts from the board run, and any
// version can be published, which rolls back to it without changing the
// board. Any two can be compared, or one with the board as it is.
export function VersionPanel({
  versions,
  disabled,
  onRestore,
  onPublish,
  onUnpublish,
  onRunPublished,
  onCompare,
  onClose,
}: {
  versions: Versions
  // While the board can't change underneath a run, or a change is going.
  disabled: boolean
  onRestore: (id: number) => void
  onPublish: (id: number) => void
  onUnpublish: () => void
  onRunPublished: () => void
  // Compares from a version to another, or to the board as it is with null.
  onCompare: (from: number, to: number | null) => void
  onClose: () => void
}) {
  const { versions: list, current, published } = versions
  // The version picked to compare from, waiting for the one to compare to.
  const [comparing, setComparing] = useState<number | null>(null)
  const latest = list[0]?.id
  const position = (id: number) => list.findIndex((v) => v.id === id)
  // Older first, whichever was picked first.
  const compare = (a: number, b: number) => (position(a) > position(b) ? onCompare(a, b) : onCompare(b, a))
  return (
    <aside className="panel version-panel" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <header className="panel-header">
        <h2>Versions</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close versions">
          ×
        </button>
      </header>
      <section className={`published-note${published === null ? '' : ' published'}`}>
        {published === null ? (
          <p>
            Nothing is published, so schedules, the API and runs on records run the board as it is. Publish to have them run
            a version of your choosing while you edit the board.
          </p>
        ) : (
          <>
            <p>
              Version {published} is published
              {versions.publishedBy && ` by ${versions.publishedBy}`}
              {versions.publishedAt && ` on ${formatTime(versions.publishedAt)}`}: schedules, the API and runs on records run
              it. {versions.draft ? 'The board has changed since.' : 'The board is the same.'}
            </p>
            <div className="published-actions">
              <button onClick={onRunPublished} title="Run the version published rather than the board">
                Run it
              </button>
              {versions.draft && <button onClick={() => onCompare(published, null)}>What changed since</button>}
              <button onClick={onUnpublish} title="Schedules, the API and runs on records run the board as it is again">
                Stop publishing
              </button>
            </div>
          </>
        )}
      </section>
      {list.length === 0 ? (
        <p className="panel-note">No versions yet. Save to keep the board as it is now.</p>
      ) : (
        <>
          <p className="panel-note">
            {comparing !== null
              ? `Pick the version to compare version ${comparing} with, or the board.`
              : disabled
                ? 'Wait for the run of the board to finish to switch versions.'
                : 'Going back to a version brings the board back to it; changes since the last save are lost, and saving afterwards adds a new version on top.'}
          </p>
          {comparing !== null && (
            <div className="published-actions">
              <button onClick={() => (setComparing(null), onCompare(comparing, null))}>Compare with the board</button>
              <button onClick={() => setComparing(null)}>Cancel</button>
            </div>
          )}
          <ol className="version-list">
            {list.map((v, i) => (
              <li
                key={v.id}
                className={`version-item${v.id === current ? ' current' : ''}${v.id === comparing ? ' comparing' : ''}`}
                aria-current={v.id === current}
              >
                <span className="version-item-title">
                  Version {v.id}
                  {v.id === latest && <span className="version-tag latest">Latest</span>}
                  {v.id === current && <span className="version-tag current">Current</span>}
                  {v.id === published && <span className="version-tag published">Published</span>}
                </span>
                <span className="version-meta">
                  <time dateTime={v.createdAt}>{formatTime(v.createdAt)}</time>
                  {v.savedBy && ` · ${v.savedBy}`}
                  {v.basedOn !== null && v.basedOn !== list[i + 1]?.id && ` · from version ${v.basedOn}`}
                </span>
                {v.reason && <span className="version-reason">{REASONS[v.reason]}</span>}
                {v.note && <span className="version-note">{v.note}</span>}
                <span className="version-actions">
                  {comparing === null ? (
                    <>
                      <button
                        disabled={disabled || v.id === current}
                        onClick={() => onRestore(v.id)}
                        title={v.id === current ? 'The board is on this version' : `Bring the board back to version ${v.id}`}
                      >
                        Go back to it
                      </button>
                      <button
                        disabled={v.id === published}
                        onClick={() => onPublish(v.id)}
                        title={`Have schedules, the API and runs on records run version ${v.id}; the board stays as it is`}
                      >
                        Publish
                      </button>
                      <button onClick={() => setComparing(v.id)} title="Compare with the board or another version">
                        Compare
                      </button>
                    </>
                  ) : (
                    v.id !== comparing && <button onClick={() => (setComparing(null), compare(comparing, v.id))}>Compare with this</button>
                  )}
                </span>
              </li>
            ))}
          </ol>
        </>
      )}
    </aside>
  )
}

// Asks for the note to save or publish the board with, if any, under the
// toolbar's button.
export function NoteDialog({
  title,
  hint,
  action,
  onSubmit,
  onClose,
}: {
  title: string
  hint: string
  action: string
  onSubmit: (note: string) => void
  onClose: () => void
}) {
  const [note, setNote] = useState('')
  const input = useRef<HTMLInputElement>(null)
  useEffect(() => input.current?.focus(), [])
  return (
    <form
      className="note-dialog"
      role="dialog"
      aria-label={title}
      onSubmit={(e) => {
        e.preventDefault()
        onSubmit(note)
      }}
      onKeyDown={(e) => e.key === 'Escape' && onClose()}
    >
      <h3>{title}</h3>
      <p>{hint}</p>
      <input ref={input} value={note} maxLength={2000} placeholder="Why, if you like: what changed, what it fixes" onChange={(e) => setNote(e.target.value)} />
      <div className="note-dialog-actions">
        <button type="button" onClick={onClose}>
          Cancel
        </button>
        <button type="submit" className="primary">
          {action}
        </button>
      </div>
    </form>
  )
}
