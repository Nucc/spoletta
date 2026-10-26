import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'

// Deleting what can be undone, something on a board (its history undoes
// it) or a flow moved to the trash, asks nothing first: a notice at the
// bottom of the screen says what went, with a button undoing it, for a few
// seconds, longer while the pointer or the focus is on it. What can't be
// undone is asked about first instead; see Confirm.

export type Undoable = {
  // What went, such as Deleted “Fetch”.
  message: string
  undo: () => Promise<void> | void
}

// How long a notice shows.
const NOTICE_MS = 8000

// A function showing a notice, one dismissing it, and the notice to render.
export function useUndoNotice(): [(u: Undoable) => void, () => void, ReactNode] {
  const [shown, setShown] = useState<(Undoable & { n: number }) | null>(null)
  const count = useRef(0)
  const show = useCallback((u: Undoable) => setShown({ ...u, n: ++count.current }), [])
  const dismiss = useCallback(() => setShown(null), [])
  const notice = shown && <UndoNotice key={shown.n} notice={shown} onDone={dismiss} />
  return [show, dismiss, notice]
}

function UndoNotice({ notice, onDone }: { notice: Undoable; onDone: () => void }) {
  const [held, setHeld] = useState(false)
  const [undoing, setUndoing] = useState(false)

  useEffect(() => {
    if (held || undoing) return
    const t = window.setTimeout(onDone, NOTICE_MS)
    return () => window.clearTimeout(t)
  }, [held, undoing, onDone])

  const undo = async () => {
    setUndoing(true)
    try {
      await notice.undo()
    } finally {
      onDone()
    }
  }

  return (
    <div
      className="undo-notice"
      role="status"
      onMouseEnter={() => setHeld(true)}
      onMouseLeave={() => setHeld(false)}
      onFocus={() => setHeld(true)}
      onBlur={() => setHeld(false)}
    >
      <span>{notice.message}</span>
      <button type="button" className="undo-notice-undo" disabled={undoing} onClick={undo}>
        Undo
      </button>
      <button type="button" className="undo-notice-close" aria-label="Dismiss" title="Dismiss" onClick={onDone}>
        ×
      </button>
    </div>
  )
}
