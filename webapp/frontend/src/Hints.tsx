import { useCallback, useSyncExternalStore, type ReactNode } from 'react'
import { useMe } from './Account'

// Help shown where and when it applies, rather than all at once: a guide
// on an empty board saying what to drag first, a line in each panel saying
// what it edits, and tips on the board at the moment they apply, as a link
// is selected or drawn from a state. Each can be hidden, which is
// remembered per person in the browser, and the board's menu shows them
// all again.

// The hints a person hid, by id, kept under their own key: user 0's
// without sign-in.
const storeKey = (user: number) => `spoletta.hints.${user}`

const listeners = new Set<() => void>()
const cache = new Map<number, Set<string>>()

function hidden(user: number): Set<string> {
  let ids = cache.get(user)
  if (!ids) {
    ids = new Set()
    try {
      const raw = localStorage.getItem(storeKey(user))
      if (raw) for (const id of JSON.parse(raw) as string[]) ids.add(id)
    } catch {
      // Without storage, nothing stays hidden past the page.
    }
    cache.set(user, ids)
  }
  return ids
}

function keep(user: number, ids: Set<string>) {
  cache.set(user, ids)
  try {
    localStorage.setItem(storeKey(user), JSON.stringify([...ids]))
  } catch {
    // Hidden while the page lasts.
  }
  listeners.forEach((l) => l())
}

const subscribe = (l: () => void) => {
  listeners.add(l)
  return () => {
    listeners.delete(l)
  }
}

// The hints the person signed in hid, and hiding one, or showing them all
// again.
export function useHints() {
  const me = useMe()
  const user = me?.user?.id ?? 0
  const ids = useSyncExternalStore(subscribe, () => hidden(user))
  const hide = useCallback((id: string) => keep(user, new Set([...hidden(user), id])), [user])
  const showAll = useCallback(() => keep(user, new Set()), [user])
  // Until it is known who looks, none shows, rather than one they hid.
  return { isHidden: (id: string) => me === null || ids.has(id), anyHidden: ids.size > 0, hide, showAll }
}

// A hint the person can hide for good.
export function Hint({ id, className, children }: { id: string; className?: string; children: ReactNode }) {
  const { isHidden, hide } = useHints()
  if (isHidden(id)) return null
  return (
    <div className={`hint-line${className ? ` ${className}` : ''}`} role="note">
      <div className="hint-text">{children}</div>
      <button type="button" className="hint-hide" onClick={() => hide(id)} aria-label="Hide this hint" title="Hide this hint; the board's menu shows hints again">
        ×
      </button>
    </div>
  )
}

// What to do first on a board with nothing on it.
export function EmptyBoardGuide({ inner }: { inner: boolean }) {
  return (
    <Hint id={inner ? 'empty-inner-board' : 'empty-board'} className="board-guide">
      <h3>{inner ? 'An empty flow process' : 'Start here'}</h3>
      <ol>
        <li>
          Drag a building block, such as an <strong>Agent</strong>, or a connector from the sidebar onto the canvas, or click it to add
          one in the middle. Double-click the canvas to add a process, and Shift+double-click to add an object.
        </li>
        <li>
          {inner
            ? 'Connect this board’s start to the process it begins with; the objects the flow process takes come in at the start, and those it generates go out at the end.'
            : 'Connect the start to the process a run begins with, such as a person filling in what the run works on.'}
        </li>
        <li>
          Processes connect through the objects they generate and take: drag from a process to the object it generates, and from an object
          to the processes taking it. Right-click a node for more.
        </li>
      </ol>
      {!inner && <p>Or start from a template on the flow list, or ask the assistant at the bottom right to draw it for you.</p>}
    </Hint>
  )
}

// How blocks go on the board, above them in the sidebar.
export function BlocksHint() {
  return (
    <Hint id="blocks" className="panel-hint sidebar-hint">
      Drag a block onto the canvas, or click it to add one in the middle. Double-click the canvas to add a process, and Shift+double-click
      to add an object. Processes connect through the objects they generate and take; right-click a node for more.
    </Hint>
  )
}

// What the board shows a tip about: what is selected, or what a link is
// being drawn from.
export type BoardTip = 'process' | 'object' | 'note' | 'input' | 'output' | 'connection' | 'state' | 'several' | 'struct'

// The tip for what is selected or being drawn, if any.
export function BoardTips({ tip }: { tip: BoardTip | null }) {
  if (tip === null) return null
  return (
    <div className="board-tips">
      <Hint id={`tip-${tip}`} className="board-tip">
        {TIPS[tip]}
      </Hint>
    </div>
  )
}

const TIPS: Record<BoardTip, ReactNode> = {
  process: (
    <>
      Double-click a process to edit it, and right-click it to rename it and more. Use + below it to add what comes next. Drag from an agent,
      script or connector process, or a human one with a time limit, onto another process to run that one when it fails.
    </>
  ),
  object: (
    <>
      Double-click an object to give it attributes, which agents generating it reply with as JSON, and right-click it to rename it, add
      states and more. Unfold it
      with its magnifier to build what it is made of: attributes and parts that are objects of their own, nested in what it holds. In its
      settings, pick an object it specializes, whose fields it has too.
    </>
  ),
  note: (
    <>
      Double-click a note to write in it, in Markdown. Pick its colour above it, size it from its corners, and drag the dot on its edge onto a
      process or an object to point at it. No run reads notes; press N to add one where the pointer is.
    </>
  ),
  input: (
    <>
      Pick how the process takes the object: it just takes it, uses it without changing it (an instrument, in OPM’s words), or uses it up
      (a consumption). For a link from a state, pick whether the process runs when the object enters it (an event), or only if the object
      is in it once the process is ready (a condition), being skipped otherwise. Drag the link’s segments to route it, double-click it to
      reset its route, and use + on it to insert a step.
    </>
  ),
  output: (
    <>
      The process makes the object (a result, in OPM’s words), or changes it if it takes it too (an effect). For a human process, pick on
      the link the states the person decides between; for an agent, the states it decides between, reviewing the object if it takes it
      too. Drag the link’s segments to route it, double-click it to reset its route, and use + on it to insert a step.
    </>
  ),
  connection: (
    <>
      The process it leads to runs right after, or once it fails, instead. Press Delete to remove it; drag its segments to route it, and
      double-click it to reset its route.
    </>
  ),
  state: <>Drop it on a process that takes the object in this state: the process runs when the object enters it.</>,
  several: (
    <>
      Cmd/Ctrl+C copies the selected processes, objects and notes, with what is inside them, and Cmd/Ctrl+V pastes them where the pointer is, on this
      board or one of another flow; right-click the board to paste too. Group into flow moves the selected processes into a flow process of
      their own.
    </>
  ),
  struct: (
    <>
      A struct is a reused object: one object that flows use, each copy following it. Add objects to give it nested fields, and make one a
      list in its settings. Double-click an object for its attributes; right-click the struct’s to add states.
    </>
  ),
}
