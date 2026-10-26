import { useState, type ReactNode } from 'react'
import { ColumnResizer, useColumnWidth } from './ColumnResizer'
import { ChevronIcon } from './icons'

// The column on the left of the board: groups that fold away, such as the
// building blocks and the runs, its right edge dragged to change its width.
export function Sidebar({ children }: { children: ReactNode }) {
  const resizable = useColumnWidth(SIDEBAR_COLUMN)
  return (
    <>
      <aside className="sidebar" aria-label="Sidebar" style={{ width: resizable.width }}>
        {children}
      </aside>
      <ColumnResizer label="Resize sidebar" {...resizable} />
    </>
  )
}

// A titled part of the sidebar that folds away, and stays as it was left.
// A filling group takes the height the others leave, and scrolls. A gloss
// after the title says in plain words what it holds; see words.ts.
export function SidebarGroup({
  id,
  title,
  gloss,
  fill = false,
  children,
}: {
  id: string
  title: string
  gloss?: string
  fill?: boolean
  children: ReactNode
}) {
  const [open, setOpen] = useState(() => loadOpen(id))
  const toggle = () => {
    setOpen(!open)
    saveOpen(id, !open)
  }
  return (
    <section className={`sidebar-group${open ? ' open' : ''}${fill ? ' fill' : ''}`}>
      <h2>
        <button aria-expanded={open} aria-controls={`sidebar-${id}`} onClick={toggle}>
          <ChevronIcon />
          {title}
          {gloss && <span className="sidebar-group-gloss">({gloss})</span>}
        </button>
      </h2>
      {open && (
        <div className="sidebar-group-body" id={`sidebar-${id}`}>
          {children}
        </div>
      )}
    </section>
  )
}

const GROUP_KEY_PREFIX = 'sidebar-open-'

function loadOpen(id: string): boolean {
  try {
    return localStorage.getItem(GROUP_KEY_PREFIX + id) !== 'false'
  } catch {
    return true
  }
}

function saveOpen(id: string, open: boolean) {
  try {
    localStorage.setItem(GROUP_KEY_PREFIX + id, String(open))
  } catch {
    // It just opens again next time.
  }
}

const SIDEBAR_COLUMN = {
  // From when the sidebar held only the runs, so widths set then carry over.
  storageKey: 'runs-width',
  min: 160,
  max: 640,
  initial: 220,
  edge: 'right',
} as const
