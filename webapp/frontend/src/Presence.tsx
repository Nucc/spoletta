import type { BoardViewer } from './api'

// How many people show by their initials before the rest are counted.
const SHOWN = 4

// The initials a person shows by: of their first and last names, or of an
// email address's name.
export function initials(name: string): string {
  const words = name.split('@')[0].split(/[\s._-]+/).filter(Boolean)
  if (words.length === 0) return '?'
  const first = [...words[0]][0] ?? ''
  const last = words.length > 1 ? ([...words[words.length - 1]][0] ?? '') : ''
  return (first + last).toUpperCase()
}

// A hue of a person's own, the same wherever they show.
function hue(id: number): number {
  return (id * 137) % 360
}

// The others with the board open, by their initials in the toolbar; those
// editing it have a ring and say so. Nobody shows without sign-in, nor when
// nobody else is there. Where the toolbar is short of room, as on a phone,
// fewer show, down to none but their count.
export default function Presence({ people, max = SHOWN }: { people: BoardViewer[]; max?: number }) {
  if (people.length === 0) return null
  const shown = people.slice(0, max)
  const rest = people.slice(max)
  const describe = (p: BoardViewer) => `${p.name}${p.editing ? ', editing' : ', looking'}`
  return (
    <ul className="presence" aria-label="Also on this board">
      {shown.map((p) => (
        <li
          key={p.id}
          className={`presence-person${p.editing ? ' editing' : ''}`}
          style={{ backgroundColor: `hsl(${hue(p.id)} 55% 42%)` }}
          title={describe(p)}
          aria-label={describe(p)}
        >
          {initials(p.name)}
        </li>
      ))}
      {rest.length > 0 && (
        <li className="presence-person presence-more" title={rest.map(describe).join('\n')} aria-label={rest.map(describe).join('; ')}>
          {shown.length > 0 && '+'}
          {rest.length}
        </li>
      )}
    </ul>
  )
}
