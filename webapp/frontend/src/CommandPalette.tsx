import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { listFlows, listRuns, runOrigin, type Flow, type FlowRun } from './api'
import { configures, useMe } from './Account'
import { formatTime, ORIGIN_LABELS, RUN_LABELS } from './Runs'
import { tabsFor } from './Settings'
import { Keys, MAC } from './Keyboard'
import { picked, rank, shortcutOf, type ShortcutId } from './shortcuts'
import { PROCEDURE_WORDS, STRUCT_WORDS } from './words'

// The command palette, Cmd-K or Ctrl-K anywhere: type to go to a flow,
// procedure or struct, one of the open flow's latest runs, a page or a tab
// of the settings, or, on a board, to do what its toolbar, sidebar and
// panels do. What it lists is ranked by how well its title matches what is
// typed (fuzzy in shortcuts.ts), and what was picked lately comes first.
// A page opens in a tab of its own, or in the tab already showing it. It
// reads the flows and runs as it opens, from the routes their pages use.

export type PaletteItem = {
  // Kept to put what was picked lately first, so the same for the same thing.
  id: string
  title: string
  group: string
  // Said beside the title, dimmed.
  hint?: string
  // Other words it is found by.
  words?: string
  shortcut?: ShortcutId
  // Why it can't be done now; listed, but not picked.
  disabled?: string
  // What picking it does: open a page, do something, or list choices to
  // pick from, such as the run to compare with.
  hash?: string
  run?: () => void
  choose?: { title: string; items: () => Promise<PaletteItem[]> | PaletteItem[] }
}

// What the page open adds: a board's actions, and opening a run of its
// flow on it rather than in a tab of its own.
export type PaletteSource = {
  items: PaletteItem[]
  openRun?: (flow: number, run: number) => boolean
}

const sources = new Set<() => PaletteSource>()

// Offers the palette what the page can do while it is shown; the items are
// asked for as the palette lists them, so they are the page's as it is then.
export function usePaletteSource(source: () => PaletteSource) {
  const latest = useRef(source)
  latest.current = source
  useEffect(() => {
    const get = () => latest.current()
    sources.add(get)
    return () => {
      sources.delete(get)
    }
  }, [])
}

const RECENT = 'palette-recent'

function loadRecent(): string[] {
  try {
    const saved = JSON.parse(localStorage.getItem(RECENT) ?? '[]') as unknown
    return Array.isArray(saved) ? saved.filter((s): s is string => typeof s === 'string') : []
  } catch {
    return []
  }
}

function saveRecent(recent: string[]) {
  try {
    localStorage.setItem(RECENT, JSON.stringify(recent))
  } catch {
    // Nothing comes first next time.
  }
}

// How many of the open flow's runs it lists.
const RUNS_LISTED = 20

// The groups in the order they are listed in with nothing typed.
const GROUPS = ['Board', 'Views', 'Run', 'Add to the board', 'Runs', 'Pages', 'Flows', PROCEDURE_WORDS.many, STRUCT_WORDS.many, 'Settings']

// The flow the page in the address is about, if any.
const flowInHash = () => {
  const m = /^#\/flows\/(\d+)/.exec(window.location.hash)
  return m ? Number(m[1]) : null
}

// A run as the palette lists it.
export const runItem = (r: FlowRun, flowName: string, extra: Partial<PaletteItem> = {}): PaletteItem => ({
  id: `run:${r.id}`,
  title: `Run #${r.id}`,
  group: 'Runs',
  hint: `${RUN_LABELS[r.status]}${r.waiting ? ', waiting for a person' : ''} · ${formatTime(r.startedAt)}${r.startedBy ? ` · ${r.startedBy}` : ''}`,
  words: `${flowName} ${RUN_LABELS[r.status]} ${ORIGIN_LABELS[runOrigin(r)]} ${r.startedBy}`,
  hash: `#/flows/${r.flow}/runs/${r.id}`,
  ...extra,
})

export default function CommandPalette({
  onOpen,
  onShortcuts,
  onClose,
}: {
  // Opens a page, in a tab.
  onOpen: (hash: string) => void
  onShortcuts: () => void
  onClose: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const input = useRef<HTMLInputElement>(null)
  const list = useRef<HTMLUListElement>(null)
  const me = useMe()
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const [flows, setFlows] = useState<Flow[] | null>(null)
  const [runs, setRuns] = useState<FlowRun[]>([])
  const [recent, setRecent] = useState(loadRecent)
  // The choices an item listed, such as the run to compare with, in place
  // of everything else until Backspace goes back.
  const [choosing, setChoosing] = useState<{ title: string; items: PaletteItem[] } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [flow] = useState(flowInHash)

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    input.current?.focus()
    return () => dialog.close()
  }, [])

  useEffect(() => {
    listFlows().then(setFlows, () => setFlows([]))
    if (flow !== null) listRuns(flow, undefined, undefined, RUNS_LISTED).then(setRuns, () => setRuns([]))
  }, [flow])

  const page = [...sources].map((get) => get())
  const flowName = flows?.find((f) => f.id === flow)?.name ?? ''
  // Everything it lists, read again with each render, as the page's items
  // may have changed.
  const listed = (): PaletteItem[] => {
    if (choosing) return choosing.items
    const all: PaletteItem[] = page.flatMap((s) => s.items)
    all.push(...runs.map((r) => runItem(r, flowName)))
    all.push(
      {
        id: 'page:flows',
        title: 'Flows',
        group: 'Pages',
        hint: 'Every flow, procedure and struct',
        words: 'home list procedures structs',
        hash: '#/',
      },
      {
        id: 'page:inbox',
        title: 'Inbox',
        group: 'Pages',
        hint: 'The reviews, inputs and loop questions waiting on you',
        words: 'reviews waiting',
        hash: '#/inbox',
      },
      {
        id: 'page:activity',
        title: 'Activity',
        group: 'Pages',
        hint: 'What runs and fails across the flows, and what they spend',
        words: 'failures spending reviews',
        hash: '#/activity',
      },
      { id: 'page:schedules', title: 'Schedules', group: 'Pages', hint: 'When flows run on their own', words: 'cron', hash: '#/schedules' },
      {
        id: 'page:trash',
        title: 'Trash',
        group: 'Pages',
        hint: 'Flows, procedures and structs deleted, to restore or delete for good',
        words: 'deleted restore bin',
        hash: '#/trash',
      },
    )
    if (configures(me)) {
      all.push({
        id: 'page:audit',
        title: 'Audit log',
        group: 'Pages',
        hint: 'Tokens handed to steps, calls refused, changes to limits',
        words: 'security',
        hash: '#/audit',
      })
    }
    if (flow !== null && flowName && !flows?.find((f) => f.id === flow)?.struct) {
      all.push({
        id: `data:${flow}`,
        title: `Data of ${flowName}`,
        group: 'Pages',
        hint: 'What each run held of each object, a run per row',
        words: 'search table',
        hash: `#/flows/${flow}/data`,
      })
    }
    for (const t of me ? tabsFor(me) : []) {
      all.push({ id: `settings:${t.id}`, title: t.label, group: 'Settings', words: 'settings', hash: `#/settings/${t.id}` })
    }
    // Favourites first, as the flow list puts them.
    const ordered = [...(flows ?? [])].sort((a, b) => Number(b.favourite) - Number(a.favourite))
    for (const f of ordered) {
      const group = f.struct ? STRUCT_WORDS.many : f.module ? PROCEDURE_WORDS.many : 'Flows'
      all.push({
        id: `flow:${f.id}`,
        title: f.name,
        group,
        hint: [f.favourite ? '★' : '', f.tags.join(', ')].filter(Boolean).join(' ') || undefined,
        words: `${f.tags.join(' ')} ${group}`,
        hash: `#/flows/${f.id}`,
      })
    }
    return GROUPS.flatMap((g) => all.filter((i) => i.group === g)).concat(all.filter((i) => !GROUPS.includes(i.group)))
  }

  // What can't be done now follows what can, still saying why.
  const ranked = rank(listed(), query, choosing ? [] : recent)
  const shown = [...ranked.filter((r) => !r.item.disabled), ...ranked.filter((r) => r.item.disabled)]
  // With nothing typed, the groups are headed, what was picked lately first.
  const headed = !query.trim()
  const lately = new Set(recent)

  useEffect(() => setActive(0), [query, choosing])
  useEffect(() => {
    list.current?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [active, query, choosing])

  const pick = async (item: PaletteItem | undefined) => {
    if (!item || item.disabled) return
    if (item.choose) {
      try {
        const choices = await item.choose.items()
        setChoosing({ title: item.choose.title, items: choices })
        setQuery('')
        setError(null)
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err))
      }
      return
    }
    if (!choosing) {
      const next = picked(recent, item.id)
      setRecent(next)
      saveRecent(next)
    }
    // A run of the board's flow opens on it; anything else as a page.
    const run = /^#\/flows\/(\d+)\/runs\/(\d+)$/.exec(item.hash ?? '')
    const inPlace = run && page.some((s) => s.openRun?.(Number(run[1]), Number(run[2])))
    onClose()
    // The palette closes first, giving the focus back, so what opens next,
    // such as the find box or a name to type, keeps it.
    if (item.hash && !inPlace) onOpen(item.hash)
    if (item.run) window.setTimeout(item.run)
  }

  // Its own keys, from the registry; Escape closes the dialog by itself.
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return
    const id = shortcutOf(e, ['paletteMove', 'palettePick', 'paletteBack'], MAC, false)
    if (id === 'paletteMove') {
      e.preventDefault()
      const n = shown.length
      if (n > 0) setActive((a) => (a + (e.key === 'ArrowDown' ? 1 : n - 1)) % n)
    } else if (id === 'palettePick') {
      e.preventDefault()
      void pick(shown[active]?.item)
    } else if (id === 'paletteBack' && query === '' && choosing) {
      e.preventDefault()
      setChoosing(null)
    }
  }

  let group = ''
  const leading = recentCount(shown, lately)
  return (
    <dialog
      ref={ref}
      className="command-palette"
      aria-label="Command palette"
      onClose={onClose}
      onClick={(e) => e.target === ref.current && onClose()}
    >
      <div className="palette-search">
        {choosing && <span className="palette-choosing">{choosing.title}</span>}
        <input
          ref={input}
          role="combobox"
          aria-expanded="true"
          aria-controls="palette-list"
          aria-activedescendant={shown[active] ? `palette-${active}` : undefined}
          aria-label={choosing ? choosing.title : 'Go to or do'}
          placeholder={choosing ? 'Type to narrow the choices' : 'Go to a flow, a run, a page or a setting, or do something on the board'}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={onKeyDown}
          spellCheck={false}
          autoComplete="off"
        />
      </div>
      {error && (
        <p className="palette-error" role="alert">
          {error}
        </p>
      )}
      <ul ref={list} id="palette-list" className="palette-list" role="listbox" aria-label="What it finds">
        {shown.length === 0 && <li className="palette-empty">{flows === null && !choosing ? 'Loading…' : 'Nothing matches.'}</li>}
        {shown.map(({ item, hits }, i) => {
          const heading = !headed || choosing ? '' : item.disabled ? 'Not now' : i < leading ? 'Lately' : item.group
          const head = heading !== group ? heading : ''
          group = heading
          return (
            <li key={`${item.id}-${i}`} role="presentation">
              {head && <div className="palette-group">{head}</div>}
              <div
                id={`palette-${i}`}
                role="option"
                aria-selected={i === active}
                aria-disabled={item.disabled ? true : undefined}
                className={`palette-item${i === active ? ' active' : ''}${item.disabled ? ' disabled' : ''}`}
                title={item.disabled}
                onMouseMove={() => i !== active && setActive(i)}
                onClick={() => void pick(item)}
              >
                <span className="palette-title">{marked(item.title, hits)}</span>
                {(item.disabled || item.hint) && <span className="palette-hint">{item.disabled ?? item.hint}</span>}
                {!headed && !choosing && <span className="palette-tag">{item.group}</span>}
                {item.shortcut && <Keys id={item.shortcut} />}
              </div>
            </li>
          )
        })}
      </ul>
      <footer className="palette-footer">
        <span>
          <kbd>↑</kbd>
          <kbd>↓</kbd> to move, <kbd>↵</kbd> to open,{' '}
          {choosing && (
            <>
              <kbd>⌫</kbd> to go back,{' '}
            </>
          )}
          <kbd>Esc</kbd> to close
        </span>
        <button
          type="button"
          className="link-button"
          onClick={() => {
            onClose()
            onShortcuts()
          }}
        >
          Every shortcut
        </button>
      </footer>
    </dialog>
  )
}

// How many of the items listed with nothing typed were picked lately: they
// lead the list.
const recentCount = (shown: { item: PaletteItem }[], lately: Set<string>) => {
  let n = 0
  while (n < shown.length && lately.has(shown[n].item.id)) n++
  return n
}

// The title with the letters the query matched marked.
function marked(title: string, hits: number[]): ReactNode {
  if (hits.length === 0) return title
  const at = new Set(hits)
  const parts: ReactNode[] = []
  let run = ''
  let on = false
  const flush = (i: number) => {
    if (run) parts.push(on ? <mark key={i}>{run}</mark> : run)
    run = ''
  }
  for (let i = 0; i < title.length; i++) {
    if (at.has(i) !== on) {
      flush(i)
      on = at.has(i)
    }
    run += title[i]
  }
  flush(title.length)
  return parts
}
