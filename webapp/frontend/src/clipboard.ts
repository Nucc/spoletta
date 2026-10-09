import type { Copied } from './api'

// What was copied stays in the browser, so it can be pasted into another
// flow, or another tab; without storage, until the page is left.
const STORAGE_KEY = 'board-clipboard'

let kept: Copied | null = null

export function readCopied(): Copied | null {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    if (stored !== null) return JSON.parse(stored) as Copied
  } catch {
    // Kept in the page instead.
  }
  return kept
}

export function writeCopied(c: Copied) {
  kept = c
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(c))
  } catch {
    // Kept in the page instead.
  }
}

// Calls back when another tab copies something.
export function onCopiedElsewhere(callback: () => void): () => void {
  const onStorage = (e: StorageEvent) => e.key === STORAGE_KEY && callback()
  window.addEventListener('storage', onStorage)
  return () => window.removeEventListener('storage', onStorage)
}
