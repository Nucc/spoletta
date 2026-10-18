import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import type { SearchFacet } from './api'
import { bound, bounds, NONE, parse, quote, resolve, select, selection, type Facets, type Mark, type Node, type Sort } from './query'

// The search over a table page's rows, as a log explorer has it: a
// query bar that colours what is typed and suggests keys and values, the
// facets beside the table with how many rows have each value, a time range,
// and column headers sorting the rows. The server searches every row
// stored (search.go); the page asks again as the query changes. What is
// searched and how rows sort stays in the page's address, so a link opens
// the same rows, and is kept for the next visit.

// How a facet's value reads, such as a run status's label; none for as it is.
export type Display = (facet: SearchFacet, value: string) => string | undefined

// The query and sort a table page keeps in its address and for next time.
export function useSearch(page: 'data' | 'records', id: number) {
  const storeKey = `search-${page}-${id}`
  const [state, setState] = useState<{ query: string; sort: Sort | null }>(() => {
    const params = new URLSearchParams(window.location.hash.split('?')[1] ?? '')
    if (params.has('q') || params.has('sort')) return { query: params.get('q') ?? '', sort: sortOf(params.get('sort')) }
    try {
      const saved = JSON.parse(localStorage.getItem(storeKey) ?? 'null') as { query?: string; sort?: string } | null
      return { query: saved?.query ?? '', sort: sortOf(saved?.sort ?? null) }
    } catch {
      return { query: '', sort: null }
    }
  })

  useEffect(() => {
    const params = new URLSearchParams()
    if (state.query.trim() !== '') params.set('q', state.query)
    if (state.sort) params.set('sort', (state.sort.desc ? '-' : '') + state.sort.key)
    const hash = `#/flows/${id}/${page}${params.size > 0 ? '?' + params.toString().replace(/\+/g, '%20') : ''}`
    if (window.location.hash !== hash) window.history.replaceState(null, '', window.location.pathname + window.location.search + hash)
    try {
      localStorage.setItem(storeKey, JSON.stringify({ query: state.query, sort: params.get('sort') ?? undefined }))
    } catch {
      // The search starts empty next time.
    }
  }, [state, id, page, storeKey])

  const setQuery = useCallback((query: string) => setState((s) => ({ ...s, query })), [])
  const setSort = useCallback((sort: Sort | null) => setState((s) => ({ ...s, sort })), [])
  // What the server is asked: the query once typing stops for a moment.
  const asked = useDebounced(state.query, 250)
  const sortText = state.sort ? (state.sort.desc ? '-' : '') + state.sort.key : ''
  const search = useMemo(() => ({ q: asked.trim(), sort: sortText }), [asked, sortText])
  return { query: state.query, sort: state.sort, setQuery, setSort, search }
}

function useDebounced<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value)
  useEffect(() => {
    const t = window.setTimeout(() => setSettled(value), ms)
    return () => window.clearTimeout(t)
  }, [value, ms])
  return settled
}

function sortOf(text: string | null): Sort | null {
  if (!text) return null
  return text.startsWith('-') ? { key: text.slice(1), desc: true } : { key: text, desc: false }
}

// Whether the facets show beside the table, the same on every table page.
export function useFacetsShown(): [boolean, () => void] {
  const [shown, setShown] = useState(() => {
    try {
      return localStorage.getItem('search-facets') !== 'hidden'
    } catch {
      return true
    }
  })
  const toggle = () =>
    setShown((s) => {
      try {
        localStorage.setItem('search-facets', s ? 'hidden' : 'shown')
      } catch {
        // They show again next time.
      }
      return !s
    })
  return [shown, toggle]
}

// A suggestion for the term being typed, and what replaces it.
type Suggestion = { insert: string; label: string; hint?: string; count?: number; next: boolean }

const PAST: [string, string][] = [
  ['now-15m', 'Past 15 minutes'],
  ['now-1h', 'Past hour'],
  ['now-4h', 'Past 4 hours'],
  ['now-1d', 'Past day'],
  ['now-7d', 'Past 7 days'],
  ['now-30d', 'Past 30 days'],
]

// The term the caret is in, with a quoted value with spaces as one, and
// the key of the group of values it is in, as in status:(done OR f.
function termAt(s: string, caret: number): { from: number; to: number; text: string; group: string | null } {
  const stop = /[\s()]/
  let from = caret
  while (from > 0 && !stop.test(s[from - 1])) from--
  if ((s.slice(0, caret).match(/(?<!\\)"/g)?.length ?? 0) % 2 === 1) {
    from = s.lastIndexOf('"', caret - 1)
    while (from > 0 && !stop.test(s[from - 1])) from--
  }
  let to = caret
  while (to < s.length && !stop.test(s[to])) to++
  let depth = 0
  let group: string | null = null
  for (let i = from - 1; i >= 0; i--) {
    if (s[i] === ')') depth++
    else if (s[i] === '(' && depth-- === 0) {
      group = /(?:^|[\s(])-?([^\s()]+):$/.exec(s.slice(0, i))?.[1] ?? null
      break
    }
  }
  return { from, to, text: s.slice(from, caret), group }
}

function suggest(facets: Facets, query: string, caret: number, display?: Display): { from: number; to: number; items: Suggestion[] } {
  const t = termAt(query, caret)
  const neg = t.text.startsWith('-') ? '-' : ''
  const text = t.text.slice(neg.length)
  const colon = t.group === null ? text.indexOf(':') : -1
  if (t.group === null && colon < 0) {
    const want = text.toLowerCase()
    const seen = new Set<string>()
    const items = facets.list
      .filter((f) => {
        const k = f.key.toLowerCase()
        if (seen.has(k) || !(k.includes(want) || f.label.toLowerCase().includes(want))) return false
        seen.add(k)
        return true
      })
      .sort((a, b) => Number(!a.key.toLowerCase().replace(/^@/, '').startsWith(want)) - Number(!b.key.toLowerCase().replace(/^@/, '').startsWith(want)))
      .slice(0, 12)
      .map((f) => ({ insert: `${neg}${f.key}:`, label: f.key, hint: f.group === f.label ? undefined : `${f.group} · ${f.label}`, next: true }))
    return { ...t, items }
  }
  const key = t.group ?? text.slice(0, colon)
  const rest = t.group === null ? text.slice(colon + 1) : text
  const op = /^(>=|<=|>|<)?/.exec(rest)![0]
  const want = rest.slice(op.length).replace(/^"/, '').toLowerCase()
  const head = t.group === null ? `${neg}${key}:` : neg
  const named = resolve(facets, key).map((i) => facets.list[i])
  if (named.some((f) => f.date)) {
    const items = PAST.filter(([v, label]) => (op + v).includes(rest) || label.toLowerCase().includes(want)).map(([v, label]) => ({
      insert: `${head}>${v} `,
      label: `${key}:>${v}`,
      hint: label,
      next: false,
    }))
    return { ...t, items }
  }
  const all = new Map<string, number>()
  for (const f of named) for (const { value, count } of f.values) all.set(value, (all.get(value) ?? 0) + count)
  const shown = (v: string) => (named.length > 0 ? display?.(named[0], v) : undefined)
  const items = [...all]
    .filter(([v]) => v.toLowerCase().includes(want) || (shown(v)?.toLowerCase().includes(want) ?? false))
    .sort((a, b) => b[1] - a[1])
    .slice(0, 10)
    .map(([v, n]) => ({
      insert: `${head}${op}${quote(v)}${t.group === null ? ' ' : ''}`,
      label: shown(v) ?? v,
      hint: shown(v) !== undefined && shown(v) !== v ? v : undefined,
      count: n,
      next: false,
    }))
  return { ...t, items }
}

// The query as typed, coloured by what each part is; a key no facet has is
// underlined.
function Highlight({ query, marks, facets }: { query: string; marks: Mark[]; facets: Facets }) {
  const out: ReactNode[] = []
  let at = 0
  for (const m of [...marks].sort((a, b) => a.from - b.from)) {
    if (m.from < at) continue
    if (m.from > at) out.push(query.slice(at, m.from))
    const text = query.slice(m.from, m.to)
    const unknown = m.kind === 'key' && facets.list.length > 0 && resolve(facets, text.slice(0, -1).replace(/\\(.)/g, '$1')).length === 0
    out.push(
      <span key={m.from} className={`q-${m.kind}${unknown ? ' q-unknown' : ''}`}>
        {text}
      </span>,
    )
    at = m.to
  }
  out.push(query.slice(at) + ' ')
  return <>{out}</>
}

export function QueryBar({
  facets,
  query,
  onChange,
  placeholder,
  display,
}: {
  facets: Facets
  query: string
  onChange: (query: string) => void
  placeholder: string
  display?: Display
}) {
  const input = useRef<HTMLInputElement>(null)
  const mirror = useRef<HTMLDivElement>(null)
  const [caret, setCaret] = useState(query.length)
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(-1)
  const pending = useRef<number | null>(null)
  const { marks } = useMemo(() => parse(query), [query])
  const found = useMemo(() => (open ? suggest(facets, query, caret, display) : null), [open, facets, query, caret, display])
  const items = found?.items ?? []

  const sync = () => {
    if (mirror.current && input.current) mirror.current.scrollLeft = input.current.scrollLeft
  }
  useLayoutEffect(() => {
    if (pending.current !== null && input.current) {
      input.current.setSelectionRange(pending.current, pending.current)
      pending.current = null
    }
    sync()
  })

  const track = () => {
    setCaret(input.current?.selectionStart ?? query.length)
    sync()
  }

  const accept = (s: Suggestion) => {
    if (!found) return
    const next = query.slice(0, found.from) + s.insert + query.slice(found.to)
    const at = found.from + s.insert.length
    pending.current = at
    setCaret(at)
    setActive(-1)
    setOpen(s.next)
    onChange(next)
  }

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Escape') {
      if (open) setOpen(false)
      else input.current?.blur()
      return
    }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      if (!open) {
        setOpen(true)
        return
      }
      if (items.length === 0) return
      e.preventDefault()
      // Past the last suggestion, or before the first, is back in the query.
      const step = e.key === 'ArrowDown' ? 1 : -1
      setActive((a) => ((a + 1 + step + items.length + 1) % (items.length + 1)) - 1)
      return
    }
    if ((e.key === 'Enter' || e.key === 'Tab') && open && items.length > 0) {
      const pick = active >= 0 && active < items.length ? items[active] : e.key === 'Tab' ? items[0] : null
      if (pick) {
        e.preventDefault()
        accept(pick)
        return
      }
    }
    if (e.key === 'Enter') setOpen(false)
  }

  return (
    <div className="query-bar">
      <SearchGlyph />
      <div className="query-field">
        <div ref={mirror} className="query-mirror" aria-hidden>
          <Highlight query={query} marks={marks} facets={facets} />
        </div>
        <input
          ref={input}
          className="query-input"
          value={query}
          placeholder={placeholder}
          spellCheck={false}
          autoComplete="off"
          aria-label="Search"
          aria-expanded={open && items.length > 0}
          aria-autocomplete="list"
          onChange={(e) => {
            setActive(-1)
            setOpen(true)
            onChange(e.target.value)
            setCaret(e.target.selectionStart ?? e.target.value.length)
          }}
          onSelect={track}
          onScroll={sync}
          onFocus={() => setOpen(true)}
          onBlur={() => setOpen(false)}
          onKeyDown={onKeyDown}
        />
      </div>
      {query !== '' && (
        <button
          className="query-clear"
          title="Clear the search"
          aria-label="Clear the search"
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => {
            onChange('')
            setCaret(0)
            input.current?.focus()
          }}
        >
          ×
        </button>
      )}
      {open && (
        <div className="query-suggestions" role="listbox" onMouseDown={(e) => e.preventDefault()}>
          {items.map((s, i) => (
            <button
              key={s.insert}
              role="option"
              aria-selected={i === active}
              className={i === active ? 'active' : undefined}
              onMouseEnter={() => setActive(i)}
              onClick={() => accept(s)}
            >
              <span className="query-suggestion-label">{s.label}</span>
              {s.hint && <span className="query-suggestion-hint">{s.hint}</span>}
              {s.count !== undefined && <span className="facet-count">{s.count}</span>}
            </button>
          ))}
          <div className="query-help">
            <code>key:value</code> <code>-key:value</code> <code>key:(a OR b)</code> <code>key:*text*</code>{' '}
            <code>key:&gt;10</code> <code>key:[1 TO 5]</code> <code>"some text"</code> · Tab or ↓ to pick a suggestion
          </div>
        </div>
      )}
    </div>
  )
}

function SearchGlyph() {
  return (
    <svg className="query-glyph" width="14" height="14" viewBox="0 0 16 16" aria-hidden>
      <circle cx="7" cy="7" r="5" fill="none" stroke="currentColor" strokeWidth="1.6" />
      <path d="M11 11l3.5 3.5" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
    </svg>
  )
}

// How far back the rows go by a time facet, written into the query as
// key:>now-1d, so the query bar shows it and it can be typed as well.
export function TimeRange({
  facets,
  query,
  field,
  onChange,
}: {
  facets: Facets
  query: string
  field: string
  onChange: (query: string) => void
}) {
  const terms = bounds(facets, query, field)
  const one = terms.length === 1 ? new RegExp(`^${field}:>=?(now-\\S+)$`, 'i').exec(terms[0])?.[1] : undefined
  const value = terms.length === 0 ? '' : PAST.some(([v]) => v === one) ? one! : 'custom'
  return (
    <select
      className="time-range"
      value={value}
      title={`Show only rows ${field} within a time`}
      onChange={(e) => onChange(bound(facets, query, field, e.target.value === '' ? null : `${field}:>${e.target.value}`))}
    >
      <option value="">Any time</option>
      {PAST.map(([v, label]) => (
        <option key={v} value={v}>
          {label}
        </option>
      ))}
      {value === 'custom' && (
        <option value="custom" disabled>
          {terms.join(' ')}
        </option>
      )}
    </select>
  )
}

// The facets beside the table, under what they belong to, each with its
// values and how many rows the rest of the query leaves have them.
export function FacetList({
  facets,
  ast,
  query,
  onChange,
  display,
  rows,
}: {
  facets: Facets
  ast: Node
  query: string
  onChange: (q: string) => void
  display?: Display
  // How many rows were searched, to fold facets whose values are mostly each a row's own.
  rows: number
}) {
  const groups: { name: string; keys: string[] }[] = []
  const seen = new Set<string>()
  for (const f of facets.list) {
    const k = f.key.toLowerCase()
    if (f.hidden || seen.has(k)) continue
    seen.add(k)
    // Two objects of one name are one facet, their counts added up.
    if (resolve(facets, f.key).every((i) => facets.list[i].distinct === 0)) continue
    const g = groups.find((g) => g.name === f.group)
    if (g) g.keys.push(f.key)
    else groups.push({ name: f.group, keys: [f.key] })
  }
  return (
    <aside className="facets" aria-label="Facets">
      {groups.map((g) => (
        <section key={g.name} className="facet-group">
          <h3>{g.name}</h3>
          {g.keys.map((k) => (
            <FacetItem key={k} facets={facets} facetKey={k} ast={ast} query={query} onChange={onChange} display={display} rows={rows} />
          ))}
        </section>
      ))}
    </aside>
  )
}

// How many values a facet lists before showing the rest takes a click.
const FACET_VALUES = 8

function FacetItem({
  facets,
  facetKey,
  ast,
  query,
  onChange,
  display,
  rows,
}: {
  facets: Facets
  facetKey: string
  ast: Node
  query: string
  onChange: (q: string) => void
  display?: Display
  rows: number
}) {
  const named = useMemo(() => facets.byKey.get(facetKey.toLowerCase())!.map((i) => facets.list[i]), [facets, facetKey])
  const facet = named[0]
  const ticked = useMemo(() => selection(facets, ast, facetKey), [facets, ast, facetKey])
  const distinct = named.reduce((n, f) => n + f.distinct, 0)
  // A facet whose values are mostly each a row's own starts folded.
  const [open, setOpen] = useState(() => distinct <= 20 || distinct < rows / 2)
  const [all, setAll] = useState(false)
  const [filter, setFilter] = useState('')
  const isOpen = open || ticked.length > 0
  const label = (v: string) => (v === NONE ? 'no value' : (display?.(facet, v) ?? v))

  const list = useMemo(() => {
    const out = new Map<string, number>()
    for (const f of named) for (const { value, count } of f.values) out.set(value, (out.get(value) ?? 0) + count)
    const none = named.reduce((n, f) => n + f.none, 0)
    if (none > 0) out.set(NONE, none)
    for (const v of ticked) if (![...out.keys()].some((k) => k.toLowerCase() === v.toLowerCase())) out.set(v, 0)
    const want = filter.toLowerCase()
    return [...out]
      .filter(([v]) => want === '' || label(v).toLowerCase().includes(want))
      .sort((a, b) => Number(a[0] === NONE) - Number(b[0] === NONE) || b[1] - a[1] || a[0].localeCompare(b[0], undefined, { numeric: true }))
    // label reads only facet and display, both among the dependencies.
  }, [named, ticked, filter, facet, display])

  const has = (v: string) => ticked.some((t) => t.toLowerCase() === v.toLowerCase())
  const toggle = (v: string) =>
    onChange(select(facets, query, facetKey, has(v) ? ticked.filter((t) => t.toLowerCase() !== v.toLowerCase()) : [...ticked, v]))
  const only = (v: string) => onChange(select(facets, query, facetKey, ticked.length === 1 && has(v) ? [] : [v]))
  const shown = all ? list : list.slice(0, FACET_VALUES)

  return (
    <div className="facet">
      <div className="facet-head">
        <button className="facet-name" aria-expanded={isOpen} onClick={() => setOpen(!isOpen)} title={facetKey}>
          <span className="facet-caret" aria-hidden>
            {isOpen ? '▾' : '▸'}
          </span>
          {facet.label}
        </button>
        {ticked.length > 0 ? (
          <button className="link-button facet-clear" onClick={() => onChange(select(facets, query, facetKey, []))}>
            Clear
          </button>
        ) : (
          !isOpen && <span className="facet-count">{distinct}</span>
        )}
      </div>
      {isOpen && (
        <div className="facet-values">
          {list.length > FACET_VALUES && (
            <input className="facet-filter" placeholder="Filter values" value={filter} onChange={(e) => setFilter(e.target.value)} />
          )}
          {shown.map(([v, n]) => (
            <div key={v} className={`facet-value${n === 0 ? ' none' : ''}`}>
              <input type="checkbox" checked={has(v)} onChange={() => toggle(v)} aria-label={`Include ${label(v)}`} />
              <button
                className={v === NONE ? 'facet-value-name facet-missing' : 'facet-value-name'}
                title={`Show only ${v === NONE ? 'rows with no value' : label(v)}; click again to show all`}
                onClick={() => only(v)}
              >
                {label(v)}
              </button>
              <span className="facet-count">{n}</span>
            </div>
          ))}
          {list.length > FACET_VALUES && (
            <button className="link-button facet-more" onClick={() => setAll(!all)}>
              {all ? 'Show fewer' : `Show all ${list.length}`}
            </button>
          )}
          {list.length === 0 && <span className="facet-empty">No values</span>}
        </div>
      )}
    </div>
  )
}

// A column header sorting the rows by its facet: first one way, then the
// other, then as they came. Times sort newest first first.
export function SortButton({
  facet,
  sort,
  onSort,
  newestFirst = false,
  children,
}: {
  facet: string
  sort: Sort | null
  onSort: (sort: Sort | null) => void
  newestFirst?: boolean
  children: ReactNode
}) {
  const on = sort?.key.toLowerCase() === facet.toLowerCase()
  const next = (): Sort | null => {
    if (!on) return { key: facet, desc: newestFirst }
    if (sort!.desc === newestFirst) return { key: facet, desc: !newestFirst }
    return null
  }
  return (
    <button className={`sort-button${on ? ' sorted' : ''}`} onClick={() => onSort(next())} title={`Sort by ${facet}`}>
      {children}
      <span className="sort-arrow" aria-hidden>
        {on ? (sort!.desc ? '↓' : '↑') : '↕'}
      </span>
    </button>
  )
}

export function ariaSort(sort: Sort | null, facet: string): 'ascending' | 'descending' | undefined {
  if (sort?.key.toLowerCase() !== facet.toLowerCase()) return undefined
  return sort.desc ? 'descending' : 'ascending'
}
