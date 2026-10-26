import { useCallback, useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'

// Asking before what can't be undone, such as deleting something for good,
// in a dialog of the page's own rather than the browser's, which looks
// foreign and blocks the page. It holds the keyboard while open: Tab goes
// round its buttons, Escape keeps things as they are, and Enter goes on, its
// button being the one focused. Deleting what can be undone asks nothing,
// offering to undo it instead; see UndoNotice.

export type ConfirmQuestion = {
  title: string
  // What happens, and what can't be undone of it.
  message: ReactNode
  // The button going on, such as Delete.
  confirm: string
}

// A confirm function, which resolves true when the person goes on, and the
// dialog to render for it while it asks.
export function useConfirm(): [(q: ConfirmQuestion) => Promise<boolean>, ReactNode] {
  const [asking, setAsking] = useState<{ q: ConfirmQuestion; answer: (yes: boolean) => void; n: number } | null>(null)
  const open = useRef<{ answer: (yes: boolean) => void } | null>(null)
  const asked = useRef(0)
  const confirm = useCallback(
    (q: ConfirmQuestion) =>
      new Promise<boolean>((resolve) => {
        // A question still open is taken as kept as it is.
        open.current?.answer(false)
        open.current = { answer: resolve }
        setAsking({ q, answer: resolve, n: ++asked.current })
      }),
    [],
  )
  const dialog = asking && (
    <ConfirmDialog
      key={asking.n}
      question={asking.q}
      onAnswer={(yes) => {
        asking.answer(yes)
        open.current = null
        setAsking(null)
      }}
    />
  )
  return [confirm, dialog]
}

export function ConfirmDialog({ question, onAnswer }: { question: ConfirmQuestion; onAnswer: (yes: boolean) => void }) {
  const ref = useRef<HTMLDialogElement>(null)
  const go = useRef<HTMLButtonElement>(null)
  const answered = useRef(false)
  const titleId = useId()
  const messageId = useId()
  const answer = (yes: boolean) => {
    if (answered.current) return
    answered.current = true
    onAnswer(yes)
  }

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    go.current?.focus()
    return () => dialog.close()
  }, [])

  // Tab and Shift-Tab go round the dialog's buttons, never out of it.
  const onKeyDown = (e: KeyboardEvent<HTMLDialogElement>) => {
    e.stopPropagation()
    if (e.key === 'Tab') {
      const focusable = [...(ref.current?.querySelectorAll<HTMLElement>('button:not(:disabled)') ?? [])]
      if (focusable.length === 0) return
      const at = focusable.indexOf(document.activeElement as HTMLElement)
      const next = (at + (e.shiftKey ? -1 : 1) + focusable.length) % focusable.length
      e.preventDefault()
      focusable[at < 0 ? 0 : next].focus()
    } else if (e.key === 'Enter' && !(e.target instanceof HTMLButtonElement)) {
      e.preventDefault()
      answer(true)
    }
  }

  return (
    <dialog
      ref={ref}
      className="confirm-dialog"
      role="alertdialog"
      aria-labelledby={titleId}
      aria-describedby={messageId}
      onKeyDown={onKeyDown}
      onCancel={(e) => {
        e.preventDefault()
        answer(false)
      }}
      onClose={() => answer(false)}
    >
      <h2 id={titleId}>{question.title}</h2>
      <div id={messageId} className="confirm-message">
        {question.message}
      </div>
      <div className="confirm-actions">
        <button type="button" onClick={() => answer(false)}>
          Cancel
        </button>
        <button ref={go} type="button" className="danger" onClick={() => answer(true)}>
          {question.confirm}
        </button>
      </div>
    </dialog>
  )
}
