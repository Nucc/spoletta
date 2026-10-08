import { useState, type PointerEvent as ReactPointerEvent } from 'react'

type Column = {
  // Where the width is remembered between visits.
  storageKey: string
  min: number
  max: number
  initial: number
  // The column's edge the handle sits on, which dragging moves.
  edge: 'left' | 'right'
}

const KEY_STEP = 20

// A column's width, which dragging the handle on one of its edges changes.
export function useColumnWidth(column: Column) {
  const [width, setWidth] = useState(() => loadWidth(column))
  return { column, width, setWidth }
}

// The line along a column's edge that drags its width, also moved with the arrow keys.
export function ColumnResizer({
  label,
  column,
  width,
  setWidth,
}: ReturnType<typeof useColumnWidth> & { label: string }) {
  const clamp = (w: number) => clampWidth(column, w)
  // Moving the edge right widens a column whose right edge it is, and narrows one whose left edge it is.
  const grow = (dx: number) => (column.edge === 'right' ? dx : -dx)

  const startResize = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return
    e.preventDefault()
    const handle = e.currentTarget
    handle.setPointerCapture(e.pointerId)
    const startX = e.clientX
    // The rendered width, which CSS may hold below the remembered one.
    const resized = column.edge === 'right' ? handle.previousElementSibling : handle.nextElementSibling
    const startWidth = resized?.getBoundingClientRect().width ?? width
    document.body.classList.add('resizing-column')
    const move = (ev: PointerEvent) => setWidth(clamp(startWidth + grow(ev.clientX - startX)))
    const end = (ev: PointerEvent) => {
      handle.removeEventListener('pointermove', move)
      handle.removeEventListener('pointerup', end)
      handle.removeEventListener('pointercancel', end)
      document.body.classList.remove('resizing-column')
      setWidth(saveWidth(column, startWidth + grow(ev.clientX - startX)))
    }
    handle.addEventListener('pointermove', move)
    handle.addEventListener('pointerup', end)
    handle.addEventListener('pointercancel', end)
  }

  return (
    <div
      className="column-resizer"
      role="separator"
      aria-orientation="vertical"
      aria-label={label}
      aria-valuenow={width}
      aria-valuemin={column.min}
      aria-valuemax={column.max}
      tabIndex={0}
      onPointerDown={startResize}
      onKeyDown={(e) => {
        const step = e.key === 'ArrowLeft' ? -KEY_STEP : e.key === 'ArrowRight' ? KEY_STEP : 0
        if (!step) return
        e.preventDefault()
        setWidth((w) => saveWidth(column, w + grow(step)))
      }}
    />
  )
}

const clampWidth = (column: Column, w: number) => Math.round(Math.min(column.max, Math.max(column.min, w)))

function loadWidth(column: Column): number {
  try {
    const w = Number(localStorage.getItem(column.storageKey))
    return w ? clampWidth(column, w) : column.initial
  } catch {
    return column.initial
  }
}

// Clamps a width and remembers it for the next visit.
function saveWidth(column: Column, w: number): number {
  const clamped = clampWidth(column, w)
  try {
    localStorage.setItem(column.storageKey, String(clamped))
  } catch {
    // The width just won't be remembered.
  }
  return clamped
}
