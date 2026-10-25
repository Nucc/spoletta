import type { Tab } from './tabs'

// The tabs along the top of the page, the open one joined to the page below,
// with a plus to open another on the list of flows. A tab on a run says
// which.
export default function TabBar({
  tabs,
  active,
  onSelect,
  onAdd,
  onClose,
}: {
  tabs: Tab[]
  active: number
  onSelect: (id: number) => void
  onAdd: () => void
  onClose: (id: number) => void
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
    </nav>
  )
}
