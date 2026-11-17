import type { Copied } from './api'
import { projectKey } from './address'

// What was copied stays in the browser, so it can be pasted into another
// flow, or another tab; without storage, until the page is left. Each
// project keeps its own, as pasting copies from a flow of the project
// pasted into alone, so what was copied in one is never offered in another.
const STORAGE_KEY = 'board-clipboard'

const kept = new Map<number | null, Copied>()

export function readCopied(project: number | null): Copied | null {
  try {
    const stored = localStorage.getItem(projectKey(STORAGE_KEY, project))
    if (stored !== null) return JSON.parse(stored) as Copied
  } catch {
    // Kept in the page instead.
  }
  return kept.get(project) ?? null
}

export function writeCopied(c: Copied, project: number | null) {
  kept.set(project, c)
  try {
    localStorage.setItem(projectKey(STORAGE_KEY, project), JSON.stringify(c))
  } catch {
    // Kept in the page instead.
  }
}

// Calls back when another tab copies something in the project.
export function onCopiedElsewhere(project: number | null, callback: () => void): () => void {
  const onStorage = (e: StorageEvent) => e.key === projectKey(STORAGE_KEY, project) && callback()
  window.addEventListener('storage', onStorage)
  return () => window.removeEventListener('storage', onStorage)
}
