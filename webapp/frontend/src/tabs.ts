import { createContext, useEffect, useRef, useState } from 'react'

// Tabs: several pages open at once in one browser tab, such as a flow's
// board beside one of its runs, or a procedure it calls. Each tab keeps the
// address of its page; only the open tab's page is shown, made again from
// its address when the tab is opened. Every entry of the browser's history
// carries the tab it was made in, so back and forward go between tabs as
// they were opened, and an entry of a tab since closed opens it again. The
// tabs are kept for the browser tab's session, so a reload keeps them, as is
// where each tab's board was looked at, so it opens there again unmoved.

export type Tab = { id: number; hash: string; title: string }

type Tabs = { tabs: Tab[]; active: number }

const STORE = 'tabs'
const OPEN = 'open-tab'

// Opens the page at the address in a new tab, next to the open one.
export const openTab = (hash: string) => window.dispatchEvent(new CustomEvent<string>(OPEN, { detail: hash }))

// The tab a page is in.
export const TabContext = createContext(0)

// Where a tab's flow was looked at on its top board: the viewport, and
// whether its run's panel was open beside it, narrowing the canvas.
export type TabView = { flow: number; x: number; y: number; zoom: number; panel: boolean }

const VIEWS = 'tab-views'

const views: Record<number, TabView> = (() => {
  try {
    return (JSON.parse(sessionStorage.getItem(VIEWS) ?? 'null') as Record<number, TabView> | null) ?? {}
  } catch {
    return {}
  }
})()

export const tabView = (tab: number, flow: number): TabView | null => (views[tab]?.flow === flow ? views[tab] : null)

// Keeps where the tab's board is looked at; none forgets it.
export function keepTabView(tab: number, view: TabView | null) {
  if (view) views[tab] = view
  else delete views[tab]
  try {
    sessionStorage.setItem(VIEWS, JSON.stringify(views))
  } catch {
    // The board is shown whole on a reload.
  }
}

const url = (hash: string) => window.location.pathname + window.location.search + hash

const tabOfEntry = (): number | undefined => {
  const tab = (window.history.state as { tab?: unknown } | null)?.tab
  return typeof tab === 'number' ? tab : undefined
}

// Marks the history entry as the tab's, keeping the address.
const stamp = (tab: number) => window.history.replaceState({ ...(window.history.state ?? {}), tab }, '', window.location.href)

// Pages name themselves in the document's title, which the tab shows
// without the app's name.
const titleOf = (title: string) => title.replace(/ · Flows$/, '')

// A new tab's id, unlike any a history entry may still carry.
const newId = (tabs: Tab[]) => Math.max(Date.now(), ...tabs.map((t) => t.id + 1))

function load(): Tabs {
  const hash = window.location.hash
  let saved: Tabs | null = null
  try {
    saved = JSON.parse(sessionStorage.getItem(STORE) ?? 'null') as Tabs | null
  } catch {
    // Only the page in the address opens.
  }
  const want = tabOfEntry()
  let state: Tabs
  if (saved && Array.isArray(saved.tabs) && saved.tabs.length > 0) {
    const active = saved.tabs.some((t) => t.id === want) ? want! : want ?? saved.active
    const tabs = saved.tabs.some((t) => t.id === active) ? saved.tabs : [...saved.tabs, { id: active, hash, title: '' }]
    state = { tabs: tabs.map((t) => (t.id === active ? { ...t, hash } : t)), active }
  } else {
    const id = want ?? 1
    state = { tabs: [{ id, hash, title: titleOf(document.title) }], active: id }
  }
  stamp(state.active)
  return state
}

export function useTabs() {
  const [state, setState] = useState(load)
  const current = useRef(state)
  const set = (next: Tabs) => {
    current.current = next
    setState(next)
  }

  // The open tab's address as it is now: pages change it in place, as a
  // board does opening a run, without saying so.
  const kept = (): Tab[] => {
    const s = current.current
    return s.tabs.map((t) => (t.id === s.active ? { ...t, hash: window.location.hash } : t))
  }

  const select = (id: number) => {
    const s = current.current
    if (id === s.active) return
    const tabs = kept()
    const tab = tabs.find((t) => t.id === id)
    if (!tab) return
    window.history.pushState({ tab: id }, '', url(tab.hash))
    set({ tabs, active: id })
  }

  // A new tab opens next to the open one, or last with the plus button.
  const add = (hash: string, last = false) => {
    const tabs = kept()
    const tab = { id: newId(tabs), hash, title: '' }
    const at = last ? tabs.length : tabs.findIndex((t) => t.id === current.current.active) + 1
    window.history.pushState({ tab: tab.id }, '', url(hash))
    set({ tabs: [...tabs.slice(0, at), tab, ...tabs.slice(at)], active: tab.id })
  }

  // Closing the open tab opens the one next to it; the last tab stays.
  const close = (id: number) => {
    const s = current.current
    if (s.tabs.length === 1) return
    const tabs = kept()
    const at = tabs.findIndex((t) => t.id === id)
    const rest = tabs.filter((t) => t.id !== id)
    keepTabView(id, null)
    if (id !== s.active) {
      set({ tabs: rest, active: s.active })
      return
    }
    const next = rest[Math.min(at, rest.length - 1)]
    window.history.pushState({ tab: next.id }, '', url(next.hash))
    set({ tabs: rest, active: next.id })
  }

  // Going back or forward opens the tab the entry was made in; following a
  // link stays in the open tab.
  useEffect(() => {
    const sync = () => {
      const s = current.current
      const hash = window.location.hash
      const want = tabOfEntry()
      if (want === undefined) {
        stamp(s.active)
        set({ tabs: s.tabs.map((t) => (t.id === s.active ? { ...t, hash } : t)), active: s.active })
        return
      }
      const tabs = s.tabs.some((t) => t.id === want) ? s.tabs : [...s.tabs, { id: want, hash, title: '' }]
      set({ tabs: tabs.map((t) => (t.id === want ? { ...t, hash } : t)), active: want })
    }
    const onOpen = (e: Event) => add((e as CustomEvent<string>).detail)
    window.addEventListener('popstate', sync)
    window.addEventListener('hashchange', sync)
    window.addEventListener(OPEN, onOpen)
    return () => {
      window.removeEventListener('popstate', sync)
      window.removeEventListener('hashchange', sync)
      window.removeEventListener(OPEN, onOpen)
    }
  })

  // The open tab takes the title its page gives the document.
  useEffect(() => {
    const observer = new MutationObserver(() => {
      const s = current.current
      const title = titleOf(document.title)
      if (s.tabs.find((t) => t.id === s.active)?.title === title) return
      set({ tabs: s.tabs.map((t) => (t.id === s.active ? { ...t, title } : t)), active: s.active })
    })
    observer.observe(document.head, { subtree: true, childList: true, characterData: true })
    return () => observer.disconnect()
  }, [])

  useEffect(() => {
    try {
      sessionStorage.setItem(STORE, JSON.stringify({ tabs: kept(), active: state.active }))
    } catch {
      // The tabs last until the page is reloaded.
    }
  })

  return { ...state, select, add, close }
}
