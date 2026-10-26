import { useEffect, useRef } from 'react'
import type { FlowProblems, Problem } from './api'

// What is wrong with the board, as the server works it out (problems.go):
// a count beside Run, its errors apart from its warnings, which opens their
// list. Picking one shows its process or object on the board. Errors are
// what Run refuses the board for; warnings what a run may well get past.
// The version published, which schedules and the API run, says its errors
// too. It stands alone, so the toolbar can put it where it likes.
export function ProblemCount({
  found,
  open,
  onOpen,
  onPick,
  refusing,
}: {
  // Null while they are worked out for the first time.
  found: FlowProblems | null
  open: boolean
  onOpen: (open: boolean) => void
  onPick: (p: Problem) => void
  // Whether the list opened as Run was refused for the errors.
  refusing: boolean
}) {
  const list = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (open) list.current?.focus()
  }, [open])
  if (!found) return null
  const errors = found.problems.filter((p) => p.level === 'error')
  const warnings = found.problems.filter((p) => p.level === 'warning')
  const published = found.published && found.published.errors.length > 0 ? found.published : null
  const none = errors.length === 0 && warnings.length === 0
  const title = none
    ? published
      ? `Nothing is wrong with the board, but version ${published.version}, the one published, has errors`
      : 'Nothing is wrong with the board that can be seen before it runs'
    : 'What is wrong with the board, before it runs'
  return (
    <span className="note-anchor problems-anchor">
      <button
        className={`problems-toggle${errors.length > 0 ? ' errors' : warnings.length > 0 ? ' warnings' : ''}${open ? ' open' : ''}`}
        aria-expanded={open}
        title={title}
        onClick={() => onOpen(!open)}
      >
        {none ? (
          <span className="problems-none">{published ? 'Published version has errors' : 'No problems'}</span>
        ) : (
          <>
            {errors.length > 0 && <span className="problems-errors">{countOf(errors.length, 'error')}</span>}
            {warnings.length > 0 && <span className="problems-warnings">{countOf(warnings.length, 'warning')}</span>}
          </>
        )}
      </button>
      {open && (
        <div
          ref={list}
          className="note-dialog problems-list"
          role="dialog"
          aria-label="Problems"
          tabIndex={-1}
          onKeyDown={(e) => e.key === 'Escape' && onOpen(false)}
        >
          <h3>{refusing ? 'Run can’t start yet' : 'Problems'}</h3>
          {none && <p>Nothing is wrong with the board that can be seen before it runs.</p>}
          {errors.length > 0 && (
            <>
              <p>{refusing ? 'Fix these first; Run refuses the board until then.' : 'Run refuses the board until these are fixed.'}</p>
              <ProblemItems items={errors} onPick={onPick} />
            </>
          )}
          {warnings.length > 0 && (
            <>
              <p>A run may get past these, or fail once it reaches them.</p>
              <ProblemItems items={warnings} onPick={onPick} />
            </>
          )}
          {published && (
            <>
              <p>
                Version {published.version}, the one published, has {countOf(published.errors.length, 'error')} too. Schedules, the API
                and runs on records run it; publish the board once it is right.
              </p>
              <ul className="problems-items published">
                {published.errors.map((p, i) => (
                  <li key={i} className="problem error">
                    {p.message}
                  </li>
                ))}
              </ul>
            </>
          )}
        </div>
      )}
    </span>
  )
}

function ProblemItems({ items, onPick }: { items: Problem[]; onPick: (p: Problem) => void }) {
  return (
    <ul className="problems-items">
      {items.map((p, i) => (
        <li key={i} className={`problem ${p.level}`}>
          <button type="button" onClick={() => onPick(p)} title={p.process !== undefined || p.object !== undefined ? 'Show it on the board' : 'Show the start'}>
            {p.message}
          </button>
        </li>
      ))}
    </ul>
  )
}

const countOf = (n: number, what: string) => `${n} ${what}${n === 1 ? '' : 's'}`
