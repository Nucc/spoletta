import { useState, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react'
import { ChevronIcon } from './icons'

// The column on the left of the board: groups that fold away, such as the
// building blocks and the runs, its right edge dragged to change its width.
export function Sidebar({ children }: { children: ReactNode }) {
  const { width, setWidth, startResize } = useSidebarWidth()
  return (
    <>
      <aside className="sidebar" aria-label="Sidebar" style={{ width }}>
        {children}
      </aside>
      <div
        className="sidebar-resizer"
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize sidebar"
        aria-valuenow={width}
        aria-valuemin={MIN_WIDTH}
        aria-valuemax={MAX_WIDTH}
        tabIndex={0}
        onPointerDown={startResize}
        onKeyDown={(e) => {
          const step = e.key === 'ArrowLeft' ? -KEY_STEP : e.key === 'ArrowRight' ? KEY_STEP : 0
          if (!step) return
          e.preventDefault()
          setWidth((w) => saveWidth(w + step))
        }}
      />
    </>
  )
}

// A titled part of the sidebar that folds away, and stays as it was left.
// A filling group takes the height the others leave, and scrolls.
export function SidebarGroup({
  id,
  title,
  fill = false,
  children,
}: {
  id: string
  title: string
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

const MIN_WIDTH = 160
const MAX_WIDTH = 640
const DEFAULT_WIDTH = 220
const KEY_STEP = 20
// From when the sidebar held only the runs, so widths set then carry over.
const WIDTH_KEY = 'runs-width'

const clampWidth = (w: number) => Math.round(Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, w)))

function loadWidth(): number {
  try {
    const w = Number(localStorage.getItem(WIDTH_KEY))
    return w ? clampWidth(w) : DEFAULT_WIDTH
  } catch {
    return DEFAULT_WIDTH
  }
}

// Clamps a width and remembers it for the next visit.
function saveWidth(w: number): number {
  const clamped = clampWidth(w)
  try {
    localStorage.setItem(WIDTH_KEY, String(clamped))
  } catch {
    // The width just won't be remembered.
  }
  return clamped
}

// The sidebar's width, which dragging its right edge changes.
function useSidebarWidth() {
  const [width, setWidth] = useState(loadWidth)

  const startResize = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return
    e.preventDefault()
    const handle = e.currentTarget
    handle.setPointerCapture(e.pointerId)
    const startX = e.clientX
    const startWidth = width
    document.body.classList.add('resizing-sidebar')
    const move = (ev: PointerEvent) => setWidth(clampWidth(startWidth + ev.clientX - startX))
    const end = (ev: PointerEvent) => {
      handle.removeEventListener('pointermove', move)
      handle.removeEventListener('pointerup', end)
      handle.removeEventListener('pointercancel', end)
      document.body.classList.remove('resizing-sidebar')
      setWidth(saveWidth(startWidth + ev.clientX - startX))
    }
    handle.addEventListener('pointermove', move)
    handle.addEventListener('pointerup', end)
    handle.addEventListener('pointercancel', end)
  }

  return { width, setWidth, startResize }
}
