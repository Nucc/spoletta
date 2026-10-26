import { useCallback, useEffect, useLayoutEffect, useRef, useState, useSyncExternalStore, type KeyboardEvent, type ReactNode } from 'react'
import { Keys } from './Keyboard'
import type { ShortcutId } from './shortcuts'
import { ChevronDownIcon, PlayIcon } from './icons'

// The board's toolbar keeps in reach what is done most, saving, publishing
// and running, and puts the rest in menus: the board's views in one
// switcher, and the ways to run a flow beside Run. As the window narrows it
// keeps to one row by moving buttons into the views menu, first arranging
// the board, then saving and publishing. index.css's toolbar media queries
// repeat these widths.
export const COMPACT_TOOLBAR = '(max-width: 1180px)'
export const NARROW_TOOLBAR = '(max-width: 860px)'

// Whether the window matches the media query, followed as it changes; the
// listener stays the same across renders, as a board re-renders often.
export function useMedia(query: string): boolean {
  const subscribe = useCallback(
    (changed: () => void) => {
      const m = window.matchMedia(query)
      m.addEventListener('change', changed)
      return () => m.removeEventListener('change', changed)
    },
    [query],
  )
  return useSyncExternalStore(subscribe, () => window.matchMedia(query).matches)
}

// Something a toolbar menu lists, and why it can't be done now if it can't:
// it is listed all the same, dimmed, saying so.
export type ToolbarItem = {
  id: string
  label: string
  icon?: ReactNode
  hint?: string
  shortcut?: ShortcutId
  disabled?: string
  // The view showing now, which picking again closes.
  current?: boolean
  onSelect: () => void
}

export type ToolbarSection = { title?: string; items: ToolbarItem[] }

// A button opening a menu under it. The arrow keys move through what it
// lists, Enter picks, and Escape or a click elsewhere closes it, Escape
// giving the focus back to the button; the board's shortcuts wait while it
// has the focus (typing in Keyboard.tsx). What else goes under the button,
// such as the note to save the board with when the menu holds Save, comes
// as children.
// How far from the window's edges a menu keeps.
const EDGE = 16

export function ToolbarMenu({
  label,
  title,
  ariaLabel,
  className,
  active = false,
  align = 'right',
  sections,
  children,
}: {
  label: ReactNode
  title: string
  ariaLabel?: string
  className?: string
  // Whether what the menu picks shows now, as a view it opened.
  active?: boolean
  align?: 'left' | 'right'
  sections: ToolbarSection[]
  children?: ReactNode
}) {
  const [open, setOpen] = useState(false)
  const anchor = useRef<HTMLSpanElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  const list = useRef<HTMLDivElement>(null)

  // The menu opens under its button, moved along to stay in the window
  // when the button is near an edge, as on a phone.
  useLayoutEffect(() => {
    const el = list.current
    if (!open || !el || !anchor.current) return
    el.style.left = ''
    el.style.right = ''
    const at = el.getBoundingClientRect()
    const by = Math.max(EDGE - at.left, 0) - Math.max(at.right - (window.innerWidth - EDGE), 0)
    if (by !== 0) {
      el.style.left = `${at.left + by - anchor.current.getBoundingClientRect().left}px`
      el.style.right = 'auto'
    }
  }, [open])

  useEffect(() => {
    if (!open) return
    list.current?.querySelector<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')?.focus()
    const onDown = (e: PointerEvent) => {
      if (!anchor.current?.contains(e.target as Node)) setOpen(false)
    }
    window.addEventListener('pointerdown', onDown, true)
    return () => window.removeEventListener('pointerdown', onDown, true)
  }, [open])

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const items = [...(list.current?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)') ?? [])]
    const at = items.indexOf(document.activeElement as HTMLButtonElement)
    const go = (i: number) => items[(i + items.length) % items.length]?.focus()
    switch (e.key) {
      case 'ArrowDown':
        go(at + 1)
        break
      case 'ArrowUp':
        go(at < 0 ? -1 : at - 1)
        break
      case 'Home':
        go(0)
        break
      case 'End':
        go(-1)
        break
      case 'Escape':
        setOpen(false)
        button.current?.focus()
        break
      case 'Tab':
        setOpen(false)
        return
      default:
        return
    }
    e.preventDefault()
    e.stopPropagation()
  }

  const shown = sections.filter((s) => s.items.length > 0)
  return (
    <span ref={anchor} className={`note-anchor toolbar-menu${className ? ` ${className}` : ''}`}>
      <button
        ref={button}
        className={`toolbar-menu-button${open || active ? ' open' : ''}`}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={ariaLabel}
        title={title}
        onClick={() => setOpen(!open)}
      >
        {label}
      </button>
      {open && (
        <div ref={list} className={`toolbar-menu-list ${align}`} role="menu" aria-label={ariaLabel ?? title} onKeyDown={onKeyDown}>
          {shown.map((s, i) => (
            <div key={s.title ?? i} className="toolbar-menu-section" role="group" aria-label={s.title}>
              {s.title && <h3 aria-hidden>{s.title}</h3>}
              {s.items.map((item) => (
                <button
                  key={item.id}
                  role="menuitem"
                  className={item.current ? 'current' : undefined}
                  aria-current={item.current || undefined}
                  disabled={item.disabled !== undefined}
                  title={item.disabled ?? item.hint}
                  onClick={() => {
                    setOpen(false)
                    item.onSelect()
                  }}
                >
                  <span className="toolbar-menu-icon">{item.icon}</span>
                  <span className="toolbar-menu-text">
                    <span className="toolbar-menu-title">{item.label}</span>
                    {(item.disabled ?? item.hint) && <span className="toolbar-menu-hint">{item.disabled ?? item.hint}</span>}
                  </span>
                  {item.shortcut && <Keys id={item.shortcut} />}
                </button>
              ))}
            </div>
          ))}
        </div>
      )}
      {children}
    </span>
  )
}

// Run, labelled, with a menu of the other ways to start the flow beside it.
export function RunButton({
  disabled,
  title,
  onRun,
  sections,
}: {
  disabled: boolean
  title: string
  onRun: () => void
  sections: ToolbarSection[]
}) {
  return (
    <span className="run-button">
      <button className="play" onClick={onRun} disabled={disabled} title={title}>
        <PlayIcon />
        Run
      </button>
      <ToolbarMenu className="play-more" label={<ChevronDownIcon />} title="More ways to run the flow" ariaLabel="More ways to run" sections={sections} />
    </span>
  )
}
