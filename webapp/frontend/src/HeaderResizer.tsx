import { useCallback, useState, type CSSProperties, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react'

// A table's column widths, each set by dragging the right edge of the
// column's header, and remembered for the next visit. A column not dragged
// takes the width its cells need, as before.

const MIN_WIDTH = 48
const MAX_WIDTH = 1200
const KEY_STEP = 20

const clamp = (w: number) => Math.round(Math.min(MAX_WIDTH, Math.max(MIN_WIDTH, w)))

export function useColumnWidths(storageKey: string) {
  const [widths, setWidths] = useState<Record<string, number>>(() => {
    try {
      const saved = JSON.parse(localStorage.getItem(storageKey) ?? '{}') as Record<string, number>
      return typeof saved === 'object' && saved !== null ? saved : {}
    } catch {
      return {}
    }
  })
  const setWidth = useCallback(
    (column: string, width: number | null, save: boolean) =>
      setWidths((ws) => {
        const next = { ...ws }
        if (width === null) delete next[column]
        else next[column] = clamp(width)
        if (save) {
          try {
            localStorage.setItem(storageKey, JSON.stringify(next))
          } catch {
            // The widths just won't be remembered.
          }
        }
        return next
      }),
    [storageKey],
  )
  // A cell's style for its column: fixed to the width it was dragged to.
  const style = (column: string): CSSProperties | undefined => {
    const w = widths[column]
    return w === undefined ? undefined : { width: w, minWidth: w, maxWidth: w }
  }
  // What a cell of a column dragged to a width holds, cut to it: a table
  // cell is never narrower than what it holds, whatever its own width.
  // padding is the cell's own on either side.
  const fit = (column: string, children: ReactNode, padding = 10) => {
    const w = widths[column]
    if (w === undefined) return children
    return (
      <span className="sized-content" style={{ maxWidth: Math.max(0, w - 2 * padding) }}>
        {children}
      </span>
    )
  }
  return { widths, setWidth, style, fit }
}

// The handle on a header's right edge that drags its column's width, also
// moved with the arrow keys; a double click gives the column back the width
// its cells need.
export function HeaderResizer({ column, label, columns }: { column: string; label: string; columns: ReturnType<typeof useColumnWidths> }) {
  const { widths, setWidth } = columns
  const start = (e: ReactPointerEvent<HTMLSpanElement>) => {
    if (e.button !== 0) return
    e.preventDefault()
    e.stopPropagation()
    const handle = e.currentTarget
    handle.setPointerCapture(e.pointerId)
    const startX = e.clientX
    const startWidth = handle.parentElement?.getBoundingClientRect().width ?? widths[column] ?? 120
    document.body.classList.add('resizing-column')
    const move = (ev: PointerEvent) => setWidth(column, startWidth + ev.clientX - startX, false)
    const end = (ev: PointerEvent) => {
      handle.removeEventListener('pointermove', move)
      handle.removeEventListener('pointerup', end)
      handle.removeEventListener('pointercancel', end)
      document.body.classList.remove('resizing-column')
      setWidth(column, startWidth + ev.clientX - startX, true)
    }
    handle.addEventListener('pointermove', move)
    handle.addEventListener('pointerup', end)
    handle.addEventListener('pointercancel', end)
  }
  return (
    <span
      className="header-resizer"
      role="separator"
      aria-orientation="vertical"
      aria-label={`Resize the ${label} column`}
      aria-valuenow={widths[column]}
      tabIndex={0}
      title="Drag to resize; double-click to fit"
      onPointerDown={start}
      onClick={(e) => e.stopPropagation()}
      onDoubleClick={(e) => {
        e.stopPropagation()
        setWidth(column, null, true)
      }}
      onKeyDown={(e) => {
        const step = e.key === 'ArrowLeft' ? -KEY_STEP : e.key === 'ArrowRight' ? KEY_STEP : 0
        if (!step) return
        e.preventDefault()
        const now = widths[column] ?? e.currentTarget.parentElement?.getBoundingClientRect().width ?? 120
        setWidth(column, now + step, true)
      }}
    />
  )
}
