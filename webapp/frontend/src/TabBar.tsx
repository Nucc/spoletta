import type { ReactNode } from 'react'
import { ActivityIcon, ClockIcon, DatabaseIcon, GearIcon, InboxIcon, TrashIcon } from './icons'
import type { Tab } from './tabs'

// The tabs along the top of the page, the open one joined to the page below,
// with Spoletta's mark first opening the home page, a plus to open another
// tab on it, and at the end the inbox, with how many steps wait on the
// person. Each tab shows by an icon what kind of page it holds, and tabs
// whose titles start alike show what tells them apart. A tab on a run says
// which.
export default function TabBar({
  tabs,
  active,
  inbox,
  onSelect,
  onAdd,
  onClose,
  onHome,
  onInbox,
}: {
  tabs: Tab[]
  active: number
  // How many steps wait on the person; null until known.
  inbox: number | null
  onSelect: (id: number) => void
  onAdd: () => void
  onClose: (id: number) => void
  onHome: () => void
  onInbox: () => void
}) {
  const labels = tabLabels(tabs)
  return (
    <nav className="tab-bar" aria-label="Tabs">
      <button className="tab-home" onClick={onHome} aria-label="Home" title="Home">
        <SpolettaMark />
      </button>
      <div className="tab-list" role="tablist">
        {tabs.map((t, i) => {
          const { short, full } = labels[i]
          return (
            <div
              key={t.id}
              className={`tab${t.id === active ? ' active' : ''}`}
              role="tab"
              aria-selected={t.id === active}
              aria-label={full}
              title={full}
              onClick={() => onSelect(t.id)}
              // A middle click closes it, as a browser's tab.
              onAuxClick={(e) => e.button === 1 && onClose(t.id)}
            >
              <span className="tab-kind">{kindIcon(t.hash)}</span>
              <span className="tab-label">{short}</span>
              {tabs.length > 1 && (
                <button
                  className="tab-close"
                  aria-label={`Close ${full}`}
                  onClick={(e) => {
                    e.stopPropagation()
                    onClose(t.id)
                  }}
                >
                  ×
                </button>
              )}
            </div>
          )
        })}
      </div>
      <button className="tab-add" onClick={onAdd} aria-label="New tab" title="Open another tab on the home page">
        +
      </button>
      <button
        className={`tab-inbox${inbox ? ' waiting' : ''}`}
        onClick={onInbox}
        aria-label={inbox ? `Inbox, ${inbox} waiting on you` : 'Inbox'}
        title="What waits on you: the reviews, inputs and loop questions you may decide"
      >
        <InboxIcon />
        <span className="tab-inbox-label">Inbox</span>
        {inbox !== null && inbox > 0 && <span className="tab-inbox-count">{inbox > 99 ? '99+' : inbox}</span>}
      </button>
    </nav>
  )
}

// Spoletta's mark: OPM's object, a green rectangle, beside its process, an
// ellipse in the text's colour.
const SpolettaMark = () => (
  <svg viewBox="0 0 32 32" width="22" height="22" aria-hidden="true">
    <rect x="3" y="10" width="11" height="12" rx="1" fill="none" stroke="#1a7f37" strokeWidth="2.5" />
    <ellipse cx="23" cy="16" rx="7" ry="6" fill="none" stroke="currentColor" strokeWidth="2.5" />
  </svg>
)

// A board: the flow builder's mark, drawn in lines in the text's colour.
const BoardIcon = () => (
  <svg viewBox="4 3.5 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <circle cx="6.5" cy="7.5" r="1.75" />
    <path d="M8.5 7.5h4M15.5 10v6.5h-4" />
    <rect x="12.5" y="5" width="6" height="5" rx="1.5" />
    <rect x="5.5" y="14" width="6" height="5" rx="1.5" />
  </svg>
)

// A task board: columns of cards.
const ColumnsIcon = () => (
  <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
    <rect x="1.75" y="2.5" width="5" height="11" rx="0.5" />
    <rect x="9.25" y="2.5" width="5" height="7" rx="0.5" />
  </svg>
)

// What kind of page the tab holds, by its address.
function kindIcon(hash: string): ReactNode {
  if (!hash || hash === '#/' || hash.startsWith('#/?')) return <SpolettaMark />
  if (/^#\/flows\/\d+\/(records|data)/.test(hash)) return <DatabaseIcon size={14} />
  if (hash.startsWith('#/flows/')) return <BoardIcon />
  if (hash.startsWith('#/taskboards/')) return <ColumnsIcon />
  if (hash === '#/inbox') return <InboxIcon />
  if (hash.startsWith('#/activity')) return <ActivityIcon />
  if (hash === '#/schedules') return <ClockIcon />
  if (hash === '#/trash') return <TrashIcon />
  if (/^#\/(settings|people|audit)/.test(hash)) return <GearIcon />
  return <BoardIcon />
}

// Where a title splits into what it starts with and the rest, as in
// "Jira Ticket - Implement login".
const SEPARATOR = / [-–—·:] /

// Each tab's label in full, with its run, and shortened: a title starting
// as another open tab's, before a separator, but going on differently
// shows only what follows, so "Jira Ticket - Implement login" beside
// "Jira Ticket - Implement search" shows "…Implement login".
export function tabLabels(tabs: Tab[]): { short: string; full: string }[] {
  const titles = tabs.map((t) => t.title || 'New tab')
  return tabs.map((t, i) => {
    const run = /\/runs\/(\d+)/.exec(t.hash)?.[1]
    const suffix = run ? ` · #${run}` : ''
    const title = titles[i]
    const full = title + suffix
    const m = SEPARATOR.exec(title)
    if (!m) return { short: full, full }
    const head = title.slice(0, m.index)
    const rest = title.slice(m.index + m[0].length)
    const shared = titles.some((o, j) => j !== i && o !== title && (o === head || o.startsWith(head + m[0])))
    return { short: shared ? `…${rest}${suffix}` : full, full }
  })
}
