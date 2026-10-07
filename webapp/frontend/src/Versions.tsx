import type { Version } from './api'

const formatTime = (s: string) =>
  new Date(s).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium' })

// Every saved version of the board, newest first. Picking one rewrites the
// board as it was; the newer versions stay, and saving adds one on top.
export function VersionPanel({
  versions,
  current,
  disabled,
  onRestore,
  onClose,
}: {
  versions: Version[]
  current: number | null
  // While the flow runs, the board can't change underneath it.
  disabled: boolean
  onRestore: (id: number) => void
  onClose: () => void
}) {
  const latest = versions[0]?.id
  return (
    <aside className="panel version-panel" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <header className="panel-header">
        <h2>Versions</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close versions">
          ×
        </button>
      </header>
      {versions.length === 0 ? (
        <p className="panel-note">No versions yet. Save to keep the board as it is now.</p>
      ) : (
        <>
          <p className="panel-note">
            {disabled
              ? 'Wait for the run to finish to switch versions.'
              : 'Pick a version to bring the board back to it. Changes made since the last save are lost; saving afterwards adds a new version on top.'}
          </p>
          <ol className="version-list">
            {versions.map((v, i) => (
              <li key={v.id}>
                <button
                  className={`version-item${v.id === current ? ' current' : ''}`}
                  aria-current={v.id === current}
                  disabled={disabled || v.id === current}
                  onClick={() => onRestore(v.id)}
                  title={v.id === current ? 'The board is on this version' : `Go back to version ${v.id}`}
                >
                  <span className="version-item-title">
                    Version {v.id}
                    {v.id === latest && <span className="version-tag latest">Latest</span>}
                    {v.id === current && <span className="version-tag current">Current</span>}
                  </span>
                  <time dateTime={v.createdAt}>
                    {formatTime(v.createdAt)}
                    {v.basedOn !== null && v.basedOn !== versions[i + 1]?.id && ` · from version ${v.basedOn}`}
                  </time>
                </button>
              </li>
            ))}
          </ol>
        </>
      )}
    </aside>
  )
}
