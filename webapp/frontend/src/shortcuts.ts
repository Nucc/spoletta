// Keyboard shortcuts: every key the app and its boards answer, in one list.
// The pages handle them through it (useShortcuts in Keyboard.tsx), the
// command palette shows each action's keys from it, and `?` lists it whole,
// so what the sheet says is always what the keys do. A shortcut is never
// taken from someone typing in a field or working in a dialog, but for the
// palette, which opens from anywhere. The palette's, the find box's and a
// note's own keys are listed too, though their fields handle them.

export type ShortcutId =
  | 'palette'
  | 'sheet'
  | 'find'
  | 'save'
  | 'run'
  | 'undo'
  | 'redo'
  | 'copy'
  | 'paste'
  | 'duplicate'
  | 'selectAll'
  | 'delete'
  | 'fit'
  | 'close'
  | 'addNote'
  | 'noteDone'
  | 'findNext'
  | 'findPrevious'
  | 'findClose'
  | 'paletteMove'
  | 'palettePick'
  | 'paletteBack'
  | 'paletteClose'

// A key and the modifiers held with it. Mod is Cmd on a Mac and Ctrl
// elsewhere. A key is the character typed, or a named key such as Enter; a
// code is the key's place on the keyboard, for a digit typed with Shift,
// which types something else on each layout.
export type Combo = { key?: string; code?: string; mod?: boolean; shift?: boolean; alt?: boolean }

// Where a shortcut works, as the sheet groups them.
export type Scope = 'anywhere' | 'board' | 'find' | 'note' | 'palette'

export const SCOPES: { id: Scope; title: string }[] = [
  { id: 'anywhere', title: 'Anywhere' },
  { id: 'board', title: 'On a board' },
  { id: 'find', title: 'Finding on a board' },
  { id: 'note', title: 'Writing a note' },
  { id: 'palette', title: 'In the command palette' },
]

export type Shortcut = {
  id: ShortcutId
  scope: Scope
  label: string
  combos: Combo[]
  // Taken even from a field or a dialog: only the palette is.
  everywhere?: boolean
}

export const SHORTCUTS: Shortcut[] = [
  {
    id: 'palette',
    scope: 'anywhere',
    label: 'Open the command palette: go to a flow, a run, a page or a setting, or do something on the board',
    combos: [{ key: 'k', mod: true }],
    everywhere: true,
  },
  { id: 'sheet', scope: 'anywhere', label: 'Show these shortcuts', combos: [{ key: '?' }] },
  {
    id: 'find',
    scope: 'board',
    label: 'Find a process, object or state by name, on this board and those inside it',
    combos: [{ key: 'f', mod: true }],
  },
  { id: 'save', scope: 'board', label: 'Save the board as a version', combos: [{ key: 's', mod: true }] },
  { id: 'run', scope: 'board', label: 'Run the flow', combos: [{ key: 'Enter', mod: true }] },
  { id: 'undo', scope: 'board', label: 'Undo the latest change to the board', combos: [{ key: 'z', mod: true }] },
  { id: 'redo', scope: 'board', label: 'Redo the change undone', combos: [{ key: 'z', mod: true, shift: true }] },
  {
    id: 'copy',
    scope: 'board',
    label: 'Copy the selected processes and objects, with what is inside them',
    combos: [{ key: 'c', mod: true }],
  },
  {
    id: 'paste',
    scope: 'board',
    label: 'Paste what was copied where the pointer is, on this board or one of another flow',
    combos: [{ key: 'v', mod: true }],
  },
  {
    id: 'duplicate',
    scope: 'board',
    label: 'Duplicate the selection, with the links between what is selected',
    combos: [{ key: 'd', mod: true }],
  },
  { id: 'selectAll', scope: 'board', label: 'Select every process and object on the board', combos: [{ key: 'a', mod: true }] },
  { id: 'delete', scope: 'board', label: 'Delete the selection', combos: [{ key: 'Delete' }, { key: 'Backspace' }] },
  { id: 'fit', scope: 'board', label: 'Zoom to fit the board', combos: [{ code: 'Digit1', shift: true }] },
  { id: 'close', scope: 'board', label: 'Close the open panel, menu or box', combos: [{ key: 'Escape' }] },
  { id: 'addNote', scope: 'board', label: 'Add a note where the pointer is, to write on the board', combos: [{ key: 'n' }] },
  { id: 'findNext', scope: 'find', label: 'Go to the next match, entering the board it is on', combos: [{ key: 'Enter' }] },
  { id: 'findPrevious', scope: 'find', label: 'Go to the match before', combos: [{ key: 'Enter', shift: true }] },
  { id: 'findClose', scope: 'find', label: 'Stop finding', combos: [{ key: 'Escape' }] },
  {
    id: 'noteDone',
    scope: 'note',
    label: 'Stop writing, keeping what is written',
    combos: [{ key: 'Escape' }, { key: 'Enter', mod: true }],
  },
  { id: 'paletteMove', scope: 'palette', label: 'Move through what it lists', combos: [{ key: 'ArrowDown' }, { key: 'ArrowUp' }] },
  { id: 'palettePick', scope: 'palette', label: 'Open or do what is picked', combos: [{ key: 'Enter' }] },
  {
    id: 'paletteBack',
    scope: 'palette',
    label: 'Back from a list of choices, such as the run to compare with',
    combos: [{ key: 'Backspace' }],
  },
  { id: 'paletteClose', scope: 'palette', label: 'Close it', combos: [{ key: 'Escape' }] },
]

export const shortcut = (id: ShortcutId): Shortcut => SHORTCUTS.find((s) => s.id === id)!

// What a key press says, as a KeyboardEvent does.
export type KeyPress = {
  key: string
  code: string
  metaKey: boolean
  ctrlKey: boolean
  shiftKey: boolean
  altKey: boolean
}

// A character typed with Shift on some layouts, such as ?, matches whether
// Shift was held or not; a letter or a named key matches only as given.
const shifted = (key: string) => key.length === 1 && !/[a-z0-9]/i.test(key)

// Whether the press is the combo. Either Cmd or Ctrl is taken as mod, as
// the board always took them, but in a field on a Mac only Cmd is, since
// Ctrl and a letter edit the text there.
export function pressed(e: KeyPress, c: Combo, mac: boolean, typing = false): boolean {
  const mod = typing && mac ? e.metaKey : e.metaKey || e.ctrlKey
  if (Boolean(c.mod) !== mod || Boolean(c.alt) !== e.altKey) return false
  if (c.code !== undefined) return e.code === c.code && Boolean(c.shift) === e.shiftKey
  if (c.key === undefined || e.key.toLowerCase() !== c.key.toLowerCase()) return false
  return shifted(c.key) || Boolean(c.shift) === e.shiftKey
}

// The shortcut among those asked for that the press is, if any. Typing,
// in a field or a dialog, only those working everywhere are.
export function shortcutOf(e: KeyPress, ids: Iterable<ShortcutId>, mac: boolean, typing: boolean): ShortcutId | null {
  for (const id of ids) {
    const s = shortcut(id)
    if (typing && !s.everywhere) continue
    if (s.combos.some((c) => pressed(e, c, mac, typing))) return id
  }
  return null
}

const KEY_NAMES: Record<string, [mac: string, other: string]> = {
  Enter: ['↵', 'Enter'],
  Escape: ['Esc', 'Esc'],
  Backspace: ['⌫', 'Backspace'],
  Delete: ['⌦', 'Delete'],
  ArrowUp: ['↑', '↑'],
  ArrowDown: ['↓', '↓'],
}

// The keys of a combo as they are written on the keyboard, one per key:
// ⌘ ⇧ K on a Mac, Ctrl Shift K elsewhere.
export function comboKeys(c: Combo, mac: boolean): string[] {
  const keys: string[] = []
  if (c.mod) keys.push(mac ? '⌘' : 'Ctrl')
  if (c.alt) keys.push(mac ? '⌥' : 'Alt')
  if (c.shift) keys.push(mac ? '⇧' : 'Shift')
  const key = c.code !== undefined ? c.code.replace(/^(Digit|Key)/, '') : c.key!
  const named = KEY_NAMES[key]
  keys.push(named ? named[mac ? 0 : 1] : key.length === 1 ? key.toUpperCase() : key)
  return keys
}

// A shortcut's first combo written out, as a title or a palette's row says it.
export const shortcutText = (id: ShortcutId, mac: boolean) => comboKeys(shortcut(id).combos[0], mac).join(mac ? '' : '+')

// How well a query matches a text, as the palette ranks what it lists, and
// where in the text it matched, to mark it: null if it doesn't. Each word
// of the query must turn up in the text, whole inside one of its words, or
// in pieces each starting one of its words, in order: "fix exc" and "fiex"
// find Fix exceptions, "ept" does, "fxe" doesn't. Letters together, at the
// start of the text or of one of its words, score higher, as does a word
// found whole.
export function fuzzy(query: string, text: string): { score: number; hits: number[] } | null {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean)
  const lower = text.toLowerCase()
  let score = 0
  const hits = new Set<number>()
  for (const w of words) {
    const m = matchWord(w, lower, text)
    if (!m) return null
    score += m.score
    m.hits.forEach((h) => hits.add(h))
  }
  return { score, hits: [...hits].sort((a, b) => a - b) }
}

// Where a word of a text starts: at its start, after a space or a mark, or
// at a capital after a small letter.
const wordStart = (text: string, i: number) =>
  i === 0 || /[\s\-_./:#(›]/.test(text[i - 1]) || (/[a-z]/.test(text[i - 1]) && /[A-Z]/.test(text[i]))

const MATCH = 1
const TOGETHER = 3
const AT_WORD = 4
const AT_START = 2
const GAP = 0.05

// The best placing of a word's letters in the text, by dynamic programming
// over where each letter lands: each starting a word of the text or right
// after the letter before. Failing that, the word inside one of the text's.
function matchWord(word: string, lower: string, text: string): { score: number; hits: number[] } | null {
  const n = lower.length
  const m = word.length
  if (m === 0) return { score: 0, hits: [] }
  if (m > n) return null
  // best[i][j]: the best score with letter i of the word at j of the text.
  const best: number[][] = []
  const from: number[][] = []
  for (let i = 0; i < m; i++) {
    best.push(new Array(n).fill(-Infinity))
    from.push(new Array(n).fill(-1))
    for (let j = i; j < n; j++) {
      if (lower[j] !== word[i]) continue
      const starts = wordStart(text, j)
      const bonus = MATCH + (starts ? AT_WORD : 0) + (j === 0 ? AT_START : 0)
      if (i === 0) {
        if (starts) best[i][j] = bonus - GAP * j
        continue
      }
      for (let k = i - 1; k < j; k++) {
        if (best[i - 1][k] === -Infinity || (k !== j - 1 && !starts)) continue
        const s = best[i - 1][k] + bonus + (k === j - 1 ? TOGETHER : -GAP * (j - k - 1))
        if (s > best[i][j]) {
          best[i][j] = s
          from[i][j] = k
        }
      }
    }
  }
  let end = -1
  for (let j = 0; j < n; j++) if (best[m - 1][j] > (end < 0 ? -Infinity : best[m - 1][end])) end = j
  if (end < 0 || best[m - 1][end] === -Infinity) {
    const at = lower.indexOf(word)
    if (at < 0) return null
    return { score: MATCH * m + TOGETHER * (m - 1) - GAP * at, hits: Array.from({ length: m }, (_, i) => at + i) }
  }
  const hits: number[] = []
  for (let i = m - 1, j = end; i >= 0; j = from[i][j], i--) hits.unshift(j)
  // A word found whole, as a word of the text, counts most.
  const whole = hits.every((h, i) => i === 0 || h === hits[i - 1] + 1) && wordStart(text, hits[0])
  return { score: best[m - 1][end] + (whole ? m : 0), hits }
}

// What the palette lists: anything with a title, found by it and by its
// other words, which aren't marked.
export type Rankable = { id: string; title: string; words?: string }

// How much higher a match picked lately ranks, and up to one more for the latest.
const RECENT_BONUS = 3

// The items matching the query, best first; those picked lately go first
// with no query and rank higher with one. Among equals, the order given
// stays.
export function rank<T extends Rankable>(items: T[], query: string, recent: string[]): { item: T; hits: number[] }[] {
  const lately = (id: string) => {
    const i = recent.indexOf(id)
    return i < 0 ? 0 : recent.length - i
  }
  if (!query.trim()) {
    return items
      .map((item, i) => ({ item, hits: [] as number[], i, lately: lately(item.id) }))
      .sort((a, b) => b.lately - a.lately || a.i - b.i)
      .map(({ item, hits }) => ({ item, hits }))
  }
  return items
    .flatMap((item, i) => {
      const inTitle = fuzzy(query, item.title)
      const anywhere = inTitle ?? (item.words ? fuzzy(query, `${item.title} ${item.words}`) : null)
      if (!anywhere) return []
      // A match in the title alone beats one needing the other words, and
      // one picked lately beats about as good a match.
      const l = lately(item.id)
      const score = anywhere.score + (inTitle ? 2 : 0) + (l > 0 ? RECENT_BONUS + l / recent.length : 0)
      return [{ item, hits: anywhere.hits.filter((h) => h < item.title.length), score, i }]
    })
    .sort((a, b) => b.score - a.score || a.i - b.i)
    .map(({ item, hits }) => ({ item, hits }))
}

// The ids picked lately, the latest first, with the one just picked
// added, at most max of them.
export const picked = (recent: string[], id: string, max = 20) => [id, ...recent.filter((r) => r !== id)].slice(0, max)
