// What the keyboard's helpers make of key presses and names: which shortcut
// of the registry (src/shortcuts.ts) a press is, typing or not, on a Mac or
// not, that no two shortcuts the window answers share a key, how the
// command palette ranks what it lists, and the order finding on a board
// (src/find.ts) goes through the matches in. Run with `npm test`, which
// needs Node 22.6 or later to load TypeScript.

import type { FlowObject, InnerBoard, Process } from '../src/api.ts'
import { findOnBoards, placeOf } from '../src/find.ts'
import { comboKeys, fuzzy, picked, rank, SHORTCUTS, shortcutOf, type KeyPress, type ShortcutId } from '../src/shortcuts.ts'

declare const process: { exitCode?: number }

let failed = 0
let checked = 0
function check(what: string, got: unknown, want: unknown) {
  checked++
  if (JSON.stringify(got) !== JSON.stringify(want)) {
    failed++
    console.error(`${what}\n  got  ${JSON.stringify(got)}\n  want ${JSON.stringify(want)}`)
  }
}

const press = (key: string, mods: Partial<KeyPress> = {}): KeyPress => ({
  key,
  code: mods.code ?? '',
  metaKey: false,
  ctrlKey: false,
  shiftKey: false,
  altKey: false,
  ...mods,
})

const all = SHORTCUTS.map((s) => s.id)
const app: ShortcutId[] = ['palette', 'sheet']
const board = SHORTCUTS.filter((s) => s.scope === 'board').map((s) => s.id)

// The palette, from anywhere.
check('Cmd-K on a Mac', shortcutOf(press('k', { metaKey: true }), app, true, false), 'palette')
check('Cmd-K on a Mac in a field', shortcutOf(press('k', { metaKey: true }), app, true, true), 'palette')
check('Ctrl-K on a Mac in a field edits the text', shortcutOf(press('k', { ctrlKey: true }), app, true, true), null)
check('Ctrl-K on a Mac outside a field', shortcutOf(press('k', { ctrlKey: true }), app, true, false), 'palette')
check('Ctrl-K elsewhere in a field', shortcutOf(press('k', { ctrlKey: true }), app, false, true), 'palette')
check('K alone', shortcutOf(press('k'), app, false, false), null)
check('Cmd-Alt-K', shortcutOf(press('k', { metaKey: true, altKey: true }), app, true, false), null)

// The sheet, typed with Shift or not.
check('? with Shift', shortcutOf(press('?', { shiftKey: true }), app, false, false), 'sheet')
check('? on a layout without Shift', shortcutOf(press('?'), app, false, false), 'sheet')
check('? while typing', shortcutOf(press('?', { shiftKey: true }), app, false, true), null)

// The board's.
check('Cmd-Z', shortcutOf(press('z', { metaKey: true }), board, true, false), 'undo')
check('Cmd-Shift-Z', shortcutOf(press('z', { metaKey: true, shiftKey: true }), board, true, false), 'redo')
check('Ctrl-Shift-Z gives a capital', shortcutOf(press('Z', { ctrlKey: true, shiftKey: true }), board, false, false), 'redo')
check('Cmd-Z while typing undoes the text', shortcutOf(press('z', { metaKey: true }), board, true, true), null)
check('Cmd-S', shortcutOf(press('s', { metaKey: true }), board, true, false), 'save')
check('Ctrl-Enter', shortcutOf(press('Enter', { ctrlKey: true }), board, false, false), 'run')
check('Enter alone runs nothing', shortcutOf(press('Enter'), board, false, false), null)
check('Cmd-D', shortcutOf(press('d', { metaKey: true }), board, true, false), 'duplicate')
check('Cmd-A', shortcutOf(press('a', { metaKey: true }), board, true, false), 'selectAll')
check('Delete', shortcutOf(press('Delete'), board, true, false), 'delete')
check('Backspace', shortcutOf(press('Backspace'), board, true, false), 'delete')
check('Shift-Backspace deletes nothing', shortcutOf(press('Backspace', { shiftKey: true }), board, true, false), null)
check('Shift-1, typing !', shortcutOf(press('!', { shiftKey: true, code: 'Digit1' }), board, false, false), 'fit')
check('Shift-1 on a layout typing 1', shortcutOf(press('1', { shiftKey: true, code: 'Digit1' }), board, false, false), 'fit')
check('1 alone', shortcutOf(press('1', { code: 'Digit1' }), board, false, false), null)
check('Escape', shortcutOf(press('Escape'), board, false, false), 'close')
check('Escape in a field is the field’s', shortcutOf(press('Escape'), board, false, true), null)
check('Cmd-F', shortcutOf(press('f', { metaKey: true }), board, true, false), 'find')
check(
  'the find box’s Shift-Enter',
  shortcutOf(press('Enter', { shiftKey: true }), ['findPrevious', 'findNext', 'findClose'], true, false),
  'findPrevious',
)
check('the find box’s Enter', shortcutOf(press('Enter'), ['findPrevious', 'findNext', 'findClose'], true, false), 'findNext')

// The registry: each shortcut once, and none the window answers sharing a
// key with another, on a Mac or not.
check('each shortcut listed once', new Set(all).size, all.length)
const windowWide = SHORTCUTS.filter((s) => s.scope === 'anywhere' || s.scope === 'board')
for (const mac of [true, false]) {
  const seen = new Map<string, ShortcutId>()
  for (const s of windowWide) {
    for (const c of s.combos) {
      const keys = comboKeys(c, mac).join(' ')
      check(`${keys} is ${s.id} alone (mac ${mac})`, seen.get(keys) ?? s.id, s.id)
      seen.set(keys, s.id)
    }
  }
}
check(
  'every shortcut has a combo and a label',
  SHORTCUTS.every((s) => s.combos.length > 0 && s.label.length > 0),
  true,
)

// Keys written out.
check('Cmd-K on a Mac', comboKeys({ key: 'k', mod: true }, true), ['⌘', 'K'])
check('Ctrl+K elsewhere', comboKeys({ key: 'k', mod: true }, false), ['Ctrl', 'K'])
check('Shift-1', comboKeys({ code: 'Digit1', shift: true }, true), ['⇧', '1'])
check('Cmd-Shift-Z', comboKeys({ key: 'z', mod: true, shift: true }, false), ['Ctrl', 'Shift', 'Z'])
check('Enter on a Mac', comboKeys({ key: 'Enter', mod: true }, true), ['⌘', '↵'])

// Fuzzy matching.
check('letters at word starts', fuzzy('fe', 'Fix exceptions')?.hits, [0, 4])
check('pieces starting words', fuzzy('fiex', 'Fix exceptions')?.hits, [0, 1, 4, 5])
check('letters inside words apart', fuzzy('fxe', 'Fix exceptions'), null)
check('a word inside one', fuzzy('ept', 'Fix exceptions')?.hits, [7, 8, 9])
check('letters picked across words', fuzzy('cred', 'Select every process and object'), null)
check('a word starting one beats one inside one', fuzzy('patch', 'Patch')!.score > fuzzy('patch', 'Dispatcher')!.score, true)
check('letters out of order', fuzzy('xf', 'Fix'), null)
check('each word of the query', fuzzy('exc fix', 'Fix exceptions')?.hits, [0, 1, 2, 4, 5, 6])
check('a word missing', fuzzy('fix bug', 'Fix exceptions'), null)
check('case is ignored', fuzzy('FIX', 'fix')?.hits, [0, 1, 2])
check('a capital inside a word starts one', fuzzy('rc', 'RunCompare')?.hits, [0, 3])
check('letters together beat letters apart', fuzzy('run', 'Run the flow')!.score > fuzzy('run', 'Review unpublished notes')!.score, true)
check('a word start beats the middle of one', fuzzy('pub', 'Publish')!.score > fuzzy('pub', 'Unpublish')!.score, true)

const items = [
  { id: 'a', title: 'Prune old runs' },
  { id: 'b', title: 'Run the flow' },
  { id: 'c', title: 'Settings', words: 'credentials' },
  { id: 'd', title: 'Credentials' },
]
check(
  'ranked by how well the title matches',
  rank(items, 'run', []).map((r) => r.item.id),
  ['b', 'a'],
)
check(
  'the other words find it too, after the titles',
  rank(items, 'cred', []).map((r) => r.item.id),
  ['d', 'c'],
)
check('hits only in the title', rank(items, 'cred', [])[1].hits, [])
check(
  'no query keeps the order',
  rank(items, '', []).map((r) => r.item.id),
  ['a', 'b', 'c', 'd'],
)
check(
  'no query puts the latest picks first',
  rank(items, '', ['c', 'a']).map((r) => r.item.id),
  ['c', 'a', 'b', 'd'],
)
check(
  'a pick lately ranks higher',
  rank(items, 'run', ['a']).map((r) => r.item.id),
  ['a', 'b'],
)
check('picking moves to the front', picked(['a', 'b', 'c'], 'c'), ['c', 'a', 'b'])
check('picks are kept to a few', picked(['a', 'b'], 'c', 2), ['c', 'a'])

// Finding on a board.
let id = 0
const proc = (name: string, parent: number | null, y: number, kind = 'agent'): Process =>
  ({ id: ++id, name, parent, x: 0, y, kind }) as unknown as Process
const obj = (name: string, parent: number | null, y: number, states: string[] = [], owner: number | null = null): FlowObject =>
  ({
    id: ++id,
    name,
    parent,
    owner,
    x: 0,
    y,
    states: states.map((s, i) => ({ id: 1000 + id * 10 + i, object: id, name: s })),
  }) as unknown as FlowObject

const triage = proc('Triage bug', null, 0)
const fix = proc('Fix', null, 200, 'flow')
const report = obj('Bug report', null, 100, ['open', 'closed'])
const field = obj('Bug id', null, 0, [], report.id)
const inner = proc('Fix the bug', fix.id, 0)
const bug = obj('Bug', fix.id, 100, ['fixed', 'bugged'])
const other = proc('Write tests', null, 300)
const c = {
  processes: [other, fix, triage, inner],
  objects: [bug, report, field],
  boards: [{ process: fix.id } as InnerBoard],
}
const names = (q: string, from = { board: null as number | null, unfolded: null as number | null }) =>
  findOnBoards(c, q, from).map((m) => `${m.kind}:${m.name}${m.of ? `@${m.of}` : ''}`)

check('from the top board down, then inside', names('bug'), [
  'process:Triage bug',
  'object:Bug report',
  'object:Bug id',
  'process:Fix the bug',
  'object:Bug',
  'state:bugged@Bug',
])
check('from inside a flow process, its board first', names('bug', { board: fix.id, unfolded: null }), [
  'process:Fix the bug',
  'object:Bug',
  'state:bugged@Bug',
  'process:Triage bug',
  'object:Bug report',
  'object:Bug id',
])
check('states by name', names('CLOSED'), ['state:closed@Bug report'])
check('nothing typed finds nothing', names('  '), [])
const found = findOnBoards(c, 'bug', { board: null, unfolded: null })
check('a part is on its object unfolded', [found[2].node, found[2].board, found[2].unfolded], [`obj-${field.id}`, null, report.id])
check('a state goes to its object', found[5].node, `obj-${bug.id}`)
check('an inner board’s match is on it', [found[3].board, found[3].unfolded], [fix.id, null])
check('where a part is', placeOf(found[2], c), 'in Bug report')
check('where an inner board’s match is', placeOf(found[3], c), 'on the board of Fix')
check('where a top board’s match is', placeOf(found[0], c), 'on the top board')
const struct = { processes: [], objects: [report, field], boards: [] }
check(
  'a struct’s object is on its own board unfolded',
  findOnBoards(struct, 'bug', { board: null, unfolded: report.id }, report.id).map((m) => [m.name, m.unfolded]),
  [
    ['Bug report', report.id],
    ['Bug id', report.id],
  ],
)
check(
  'where a struct’s object is',
  findOnBoards(struct, 'bug', { board: null, unfolded: report.id }, report.id).map((m) => placeOf(m, struct)),
  ["the struct's own object", 'in Bug report'],
)

console.log(`${checked} checks of the keyboard's helpers, ${failed} failed`)
if (failed > 0) process.exitCode = 1
