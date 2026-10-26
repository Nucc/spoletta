import { useState } from 'react'
import type { BoardDiff, BoardId, DiffField, DiffItem } from './api'

// What changed between two boards of the flow, as the server works it out
// (diff.go): listed in a panel, and highlighted on the board shown, the
// newer one. What was added or changed on the board shown is outlined;
// what was removed is only listed, as is what is on another board, whose
// flow process is outlined instead.

export type Change = DiffItem['change']

// The nodes and edges of the board shown to highlight, by their ids there.
export type Highlights = { nodes: Map<string, Change>; edges: Map<string, Change> }

export const noHighlights: Highlights = { nodes: new Map(), edges: new Map() }

// How the board names its nodes and edges, and where each flow process is.
export type BoardIds = {
  process: (id: number) => string
  object: (id: number) => string
  link: (id: number) => string
  connection: (id: number) => string
  start: string
  // The board a flow process is on, null for the top board; undefined for
  // one the board doesn't have.
  parentOf: (process: number) => BoardId | undefined
}

// What to highlight of the diff on board.
export function highlightsOf(d: BoardDiff, board: BoardId, ids: BoardIds): Highlights {
  const h: Highlights = { nodes: new Map(), edges: new Map() }
  const mark = (into: Map<string, Change>, id: string, change: Change) => {
    if (into.get(id) !== 'added') into.set(id, change)
  }
  // Something changed on another board: the flow process holding that board
  // on this one changed, if any does.
  const inside = (on: BoardId) => {
    for (let b = on, seen = 0; b !== null && seen < 64; seen++) {
      const parent = ids.parentOf(b)
      if (parent === undefined) return
      if (parent === board) {
        mark(h.nodes, ids.process(b), 'changed')
        return
      }
      b = parent
    }
  }
  const each = (items: DiffItem[], node: (it: DiffItem) => void) =>
    items.forEach((it) => {
      if (it.board !== board) inside(it.board)
      else if (it.change !== 'removed') node(it)
    })
  each(d.processes, (it) => mark(h.nodes, ids.process(it.id), it.change))
  each(d.objects, (it) => mark(h.nodes, ids.object(it.id), it.change))
  // A state or attribute added or removed changes its object; removed
  // ones are left out above, so they are marked here.
  for (const it of [...d.states, ...d.attributes]) {
    if (it.object === undefined) continue
    if (it.board !== board) inside(it.board)
    else if (!d.objects.some((o) => o.id === it.object && o.change === 'removed')) mark(h.nodes, ids.object(it.object), 'changed')
  }
  each(d.links, (it) => mark(h.edges, ids.link(it.id), it.change))
  each(d.connections, (it) => mark(h.edges, it.id === 0 ? ids.start : ids.connection(it.id), it.change))
  return h
}

const KINDS: [keyof Pick<BoardDiff, 'processes' | 'objects' | 'states' | 'attributes' | 'links' | 'connections'>, string][] = [
  ['processes', 'Processes'],
  ['objects', 'Objects'],
  ['states', 'States'],
  ['attributes', 'Attributes'],
  ['links', 'Links'],
  ['connections', 'Connections'],
]

const CHANGES: Record<Change, string> = { added: 'Added', removed: 'Removed', changed: 'Changed' }

// How many things the diff lists.
export const diffCount = (d: BoardDiff) => KINDS.reduce((n, [k]) => n + d[k].length, 0) + d.settings.length

// The diff, listed by kind of thing. title says what is compared with what;
// boardName names a flow process's board for what is on one.
export function DiffPanel({
  diff,
  title,
  boardName,
  onBack,
  onClose,
}: {
  diff: BoardDiff | null
  title: string
  boardName: (board: number) => string
  onBack?: () => void
  onClose: () => void
}) {
  return (
    <aside className="panel diff-panel" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <header className="panel-header">
        <h2>Changes</h2>
        <button className="panel-close" onClick={onClose} aria-label="Stop comparing">
          ×
        </button>
      </header>
      <p className="panel-note">{title}</p>
      {onBack && (
        <div className="published-actions">
          <button onClick={onBack}>Back to versions</button>
        </div>
      )}
      {diff === null ? (
        <p className="panel-note">Comparing…</p>
      ) : diffCount(diff) === 0 ? (
        <p className="panel-note">{diff.layout ? 'Nothing changed but where things sit on the board.' : 'Nothing changed.'}</p>
      ) : (
        <>
          {diff.layout && <p className="panel-note">Things were also moved on the board, which isn't listed.</p>}
          {KINDS.filter(([k]) => diff[k].length > 0).map(([k, label]) => (
            <section key={k} className="diff-group">
              <h3>{label}</h3>
              <ul>
                {diff[k].map((it, i) => (
                  <li key={`${it.change}-${it.id}-${i}`} className={`diff-entry ${it.change}`}>
                    <span className="diff-entry-title">
                      <span className={`diff-tag ${it.change}`}>{CHANGES[it.change]}</span>
                      {it.name}
                      {it.board !== null && <span className="diff-where"> in {boardName(it.board)}</span>}
                    </span>
                    {it.fields && <DiffFields fields={it.fields} />}
                  </li>
                ))}
              </ul>
            </section>
          ))}
          {diff.settings.length > 0 && (
            <section className="diff-group">
              <h3>Flow settings</h3>
              <ul>
                <li className="diff-entry changed">
                  <DiffFields fields={diff.settings} />
                </li>
              </ul>
            </section>
          )}
        </>
      )}
    </aside>
  )
}

// How long a value shows before it folds away behind a toggle.
const SHORT = 80

function DiffFields({ fields }: { fields: DiffField[] }) {
  return (
    <dl className="diff-fields">
      {fields.map((f) => (
        <DiffValue key={f.field} field={f} />
      ))}
    </dl>
  )
}

// A field as it was and as it is: short ones on a line, long ones, such as
// prompts and scripts, folded until opened.
function DiffValue({ field }: { field: DiffField }) {
  const long = field.before.length + field.after.length > SHORT || field.before.includes('\n') || field.after.includes('\n')
  const [open, setOpen] = useState(false)
  const shown = (v: string) => (v === '' ? <em>none</em> : v)
  return (
    <>
      <dt>{field.field}</dt>
      <dd>
        {!long ? (
          <>
            <del>{shown(field.before)}</del> → <ins>{shown(field.after)}</ins>
          </>
        ) : (
          <>
            <button className="link-button" onClick={() => setOpen(!open)} aria-expanded={open}>
              {open ? 'Hide' : 'Show'} before and after
            </button>
            {open && (
              <div className="diff-texts">
                <pre className="diff-before">{field.before || '(none)'}</pre>
                <pre className="diff-after">{field.after || '(none)'}</pre>
              </div>
            )}
          </>
        )}
      </dd>
    </>
  )
}
