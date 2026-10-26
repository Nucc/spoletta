import { InboxIcon } from './icons'
import type { Tab } from './tabs'

// The tabs along the top of the page, the open one joined to the page below,
// with a plus to open another on the list of flows, and at the end the
// inbox, with how many steps wait on the person. A tab on a run says which.
export default function TabBar({
  tabs,
  active,
  inbox,
  onSelect,
  onAdd,
  onClose,
  onInbox,
}: {
  tabs: Tab[]
  active: number
  // How many steps wait on the person; null until known.
  inbox: number | null
  onSelect: (id: number) => void
  onAdd: () => void
  onClose: (id: number) => void
  onInbox: () => void
}) {
  return (
    <nav className="tab-bar" aria-label="Tabs">
      <div className="tab-list" role="tablist">
        {tabs.map((t) => {
          const run = /\/runs\/(\d+)/.exec(t.hash)?.[1]
          const label = (t.title || 'New tab') + (run ? ` · run ${run}` : '')
          return (
            <div
              key={t.id}
              className={`tab${t.id === active ? ' active' : ''}`}
              role="tab"
              aria-selected={t.id === active}
              title={label}
              onClick={() => onSelect(t.id)}
              // A middle click closes it, as a browser's tab.
              onAuxClick={(e) => e.button === 1 && onClose(t.id)}
            >
              <span className="tab-label">{label}</span>
              {tabs.length > 1 && (
                <button
                  className="tab-close"
                  aria-label={`Close ${label}`}
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
      <button className="tab-add" onClick={onAdd} aria-label="New tab" title="Open another tab on the list of flows">
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
