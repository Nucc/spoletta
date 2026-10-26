import { useEffect, useRef } from 'react'
import { SearchIcon } from './icons'
import { MAC } from './Keyboard'
import { shortcutOf, shortcutText } from './shortcuts'

// The box finding on a board (Cmd-F, see find.ts), at the top right of the
// canvas: what is typed, how many match and which one was gone to, where it
// is, and buttons going to the match before and the next, as Shift-Enter
// and Enter do. Asking to find again while it is open selects what is typed.
export default function BoardFind({
  query,
  count,
  at,
  place,
  focus,
  onQuery,
  onNext,
  onPrevious,
  onClose,
}: {
  query: string
  count: number
  // The match gone to, if any.
  at: number | null
  // Where the match gone to is, as the box says it.
  place: string | null
  // Bumped to take the focus again.
  focus: number
  onQuery: (query: string) => void
  onNext: () => void
  onPrevious: () => void
  onClose: () => void
}) {
  const input = useRef<HTMLInputElement>(null)
  useEffect(() => {
    input.current?.focus()
    input.current?.select()
  }, [focus])
  return (
    <div className="board-find" role="search">
      <div className="board-find-row">
        <SearchIcon />
        <input
          ref={input}
          aria-label="Find on the board"
          placeholder="Find a process, object or state"
          value={query}
          spellCheck={false}
          onChange={(e) => onQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.nativeEvent.isComposing) return
            const id = shortcutOf(e, ['findPrevious', 'findNext', 'findClose'], MAC, false)
            if (id === null) return
            e.preventDefault()
            e.stopPropagation()
            if (id === 'findNext') onNext()
            else if (id === 'findPrevious') onPrevious()
            else onClose()
          }}
        />
        <span className="board-find-count" aria-live="polite">
          {!query.trim() ? '' : count === 0 ? 'None' : at === null ? `${count} found` : `${at + 1} of ${count}`}
        </span>
        <button
          className="icon-button"
          onClick={onPrevious}
          disabled={count === 0}
          aria-label="The match before"
          title={`The match before (${shortcutText('findPrevious', MAC)})`}
        >
          ↑
        </button>
        <button
          className="icon-button"
          onClick={onNext}
          disabled={count === 0}
          aria-label="The next match"
          title={`The next match, entering the board it is on (${shortcutText('findNext', MAC)})`}
        >
          ↓
        </button>
        <button
          className="icon-button"
          onClick={onClose}
          aria-label="Stop finding"
          title={`Stop finding (${shortcutText('findClose', MAC)})`}
        >
          ×
        </button>
      </div>
      {place && <p className="board-find-place">{place}</p>}
    </div>
  )
}
