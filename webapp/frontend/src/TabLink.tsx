import type { ReactNode } from 'react'
import { openTab } from './tabs'

// A link to another page of the app opened in a tab of its own, next to the
// open one, so the page it is on stays as it was: a process's panel linking
// to where its skills, credentials or MCP servers are set up. Cmd-, Ctrl- or
// Shift-clicking it, or the middle button, leaves it to the browser.
export default function TabLink({ hash, title, children }: { hash: string; title?: string; children: ReactNode }) {
  return (
    <a
      className="tab-link"
      href={hash}
      title={title}
      onClick={(e) => {
        if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
        e.preventDefault()
        openTab(hash)
      }}
    >
      {children}
    </a>
  )
}
