import { useEffect, useRef } from 'react'
import { ReviewForm, type Review } from './ProcessPanel'

// A review or input a human process waits for, in the middle of the screen
// over everything else. Closing it, with × or Escape, leaves the form in the
// process's panel to come back to.
export default function ReviewDialog({ name, review, onClose }: { name: string; review: Review; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null)

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])

  return (
    <dialog
      ref={ref}
      className={`review-dialog${review.inputs.length > 0 ? ' reading' : ''}`}
      aria-label={name}
      onClose={onClose}
    >
      <header className="panel-header">
        <h2>{name}</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close" title="Close; the form stays in the process's panel">
          ×
        </button>
      </header>
      <ReviewForm review={review} />
    </dialog>
  )
}
