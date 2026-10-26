import { Fragment, useEffect, useRef } from 'react'
import { comboKeys, SCOPES, SHORTCUTS, shortcutOf, type ShortcutId } from './shortcuts'

// Whether this is a Mac, where mod is Cmd and the keys are written as signs.
export const MAC = typeof navigator !== 'undefined' && /Mac|iPhone|iPad|iPod/.test(navigator.platform || navigator.userAgent)

// Whether a key pressed now goes to something typed or to a dialog rather
// than to the page's shortcuts: a field has the focus, or a dialog is open
// over the page, such as a review, or the focus is in a box that is one,
// such as the note to save the board with, or in a menu, which has keys of
// its own.
export function typing(target: EventTarget | null): boolean {
  const el = target instanceof Element ? target : null
  if (el?.closest('input, textarea, select, [contenteditable=""], [contenteditable="true"], dialog, [role="dialog"], [role="menu"]')) return true
  return document.querySelector('dialog:modal') !== null
}

// What a shortcut does, given the key press; false leaves the key to the
// browser, as copying text selected, and anything else keeps it from it.
export type ShortcutHandlers = Partial<Record<ShortcutId, (e: KeyboardEvent) => boolean | void>>

// Answers the shortcuts given handlers for, from the registry in
// shortcuts.ts. The keys are caught on their way down to the focus, so a
// dialog keeping them from the page, as the run's windows do with Escape,
// still lets the palette open over it; the handlers are read as they are
// at each press.
export function useShortcuts(handlers: ShortcutHandlers) {
  const latest = useRef(handlers)
  latest.current = handlers
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.isComposing) return
      const asked = Object.keys(latest.current) as ShortcutId[]
      const id = shortcutOf(e, asked, MAC, typing(e.target))
      if (id === null) return
      if (latest.current[id]?.(e) !== false) e.preventDefault()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [])
}

// A combo's keys, each in a box.
export function Keys({ id, all = false }: { id: ShortcutId; all?: boolean }) {
  const s = SHORTCUTS.find((x) => x.id === id)!
  const combos = all ? s.combos : s.combos.slice(0, 1)
  return (
    <span className="keys">
      {combos.map((c, i) => (
        <Fragment key={i}>
          {i > 0 && <span className="keys-or">or</span>}
          {comboKeys(c, MAC).map((k, j) => (
            <kbd key={j}>{k}</kbd>
          ))}
        </Fragment>
      ))}
    </span>
  )
}

// Every shortcut of the registry, grouped by where it works, over the page;
// Escape, a click outside or × closes it.
export function ShortcutSheet({ onClose }: { onClose: () => void }) {
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
      className="shortcut-sheet"
      aria-label="Keyboard shortcuts"
      onClose={onClose}
      onClick={(e) => e.target === ref.current && onClose()}
    >
      <header className="panel-header">
        <h2>Keyboard shortcuts</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <p className="shortcut-sheet-note">
        None is taken from you while you type in a field, but {MAC ? '⌘K' : 'Ctrl+K'}, which opens the command palette from anywhere, over a
        dialog too.
      </p>
      <div className="shortcut-groups">
        {SCOPES.map((scope) => (
          <section key={scope.id}>
            <h3>{scope.title}</h3>
            <dl>
              {SHORTCUTS.filter((s) => s.scope === scope.id).map((s) => (
                <div key={s.id} className="shortcut-row">
                  <dt>
                    <Keys id={s.id} all />
                  </dt>
                  <dd>{s.label}</dd>
                </div>
              ))}
            </dl>
          </section>
        ))}
      </div>
    </dialog>
  )
}
