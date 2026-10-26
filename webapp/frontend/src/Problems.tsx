import { useEffect, useRef } from 'react'
import type { FlowProblems, Problem } from './api'
import { AlertIcon, CheckIcon } from './icons'

// What is wrong with the board, as the server works it out (problems.go):
// a count beside Run, its errors apart from its warnings, which opens their
// list. Picking one shows its process or object on the board. Errors are
// what Run refuses the board for; warnings what a run may well get past.
// The version published, which schedules and the API run, says its errors
// too. It stands alone, so the toolbar can put it where it likes.

// Why the list opened, if Run was refused: for the board's errors, or for
// the version published's when it was asked to run that.
export type Refusal = false | 'board' | 'published'

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
  refusing: Refusal
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
  const said = [errors.length > 0 && countOf(errors.length, 'error'), warnings.length > 0 && countOf(warnings.length, 'warning')]
    .filter(Boolean)
    .join(' and ')
  const title = none
    ? published
      ? `Nothing is wrong with the board, but version ${published.version}, the one published, has errors`
      : 'Nothing is wrong with the board that can be seen before it runs'
    : `The board has ${said}`
  return (
    <span className="note-anchor problems-anchor">
      <button
        className={`problems-toggle${errors.length > 0 ? ' errors' : warnings.length > 0 || published ? ' warnings' : ' none'}${open ? ' open' : ''}`}
        aria-expanded={open}
        aria-label={none ? (published ? 'Problems: the version published has errors' : 'No problems') : `Problems: ${said}`}
        title={title}
        onClick={() => onOpen(!open)}
      >
        {none && !published ? (
          <CheckIcon />
        ) : (
          <>
            <AlertIcon />
            {errors.length > 0 && <span className="problems-errors">{errors.length}</span>}
            {warnings.length > 0 && <span className="problems-warnings">{warnings.length}</span>}
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
          <h3>{refusing === 'published' ? 'The published version can’t run' : refusing ? 'Run can’t start yet' : 'Problems'}</h3>
          {none && <p>Nothing is wrong with the board that can be seen before it runs.</p>}
          {errors.length > 0 && (
            <>
              <p>{refusing === 'board' ? 'Fix these first; Run refuses the board until then.' : 'Run refuses the board until these are fixed.'}</p>
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
                {refusing === 'published'
                  ? `Version ${published.version}, the one published, has ${countOf(published.errors.length, 'error')}, which it can’t run past. Schedules, the API and runs on records run it too; publish the board once it is right.`
                  : `Version ${published.version}, the one published, has ${countOf(published.errors.length, 'error')} too. Schedules, the API and runs on records run it; publish the board once it is right.`}
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
