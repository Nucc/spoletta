// Where a page is: every page inside a project has an address starting with
// it, #/p/<id>/…, such as #/p/3/flows/12/runs/40 for a run of flow 12 in
// project 3, and the rest is the address the page had before projects
// (#/flows/12/runs/40), which App.tsx reads. The Projects page (#/projects)
// is outside any project. Each tab of the app keeps its own address, so its
// own project, and every request a page makes names that project
// (api.ts), so two tabs may show two projects at once.
//
// Every link and every address a page writes is built here (pageHash), in
// the project of the page it is on unless told another, so none is left
// pointing into no project. An address without a project, as old bookmarks
// and the links in notifications have, opens in the project its tab last
// showed, else the one the person last used, else their first
// (projectFor).
//
// The helpers on strings are pure, so scripts/address.ts checks them; the
// few reading the window or the browser's storage are at the end.

// The pages outside any project.
const OUTSIDE = ['#/projects']

// An address split into its project, none when it names none, and the page
// within it, as addressed before projects: '#/flows/12', '#/' for the home
// page, '' for none at all.
export function splitHash(hash: string): { project: number | null; page: string } {
  const m = /^#\/p\/(\d+)(?=$|[/?])\/?(.*)$/.exec(hash)
  if (!m) return { project: null, page: hash }
  const project = Number(m[1])
  if (!Number.isSafeInteger(project) || project <= 0) return { project: null, page: hash }
  return { project, page: `#/${m[2]}` }
}

// Whether the page is outside any project, such as the Projects page.
export const outsideProjects = (page: string) => OUTSIDE.some((p) => page === p || page.startsWith(`${p}?`) || page.startsWith(`${p}/`))

// The address of the page in the project: '#/flows/12' in project 3 is
// '#/p/3/flows/12', the home page ('#/', '' or '#') '#/p/3/'. A page
// outside the projects, or no project, leaves it as it is; one already in a
// project moves to this one.
export function hashIn(project: number | null, page: string): string {
  const own = splitHash(page).page
  if (project === null || outsideProjects(own)) return own
  const rest = own.replace(/^#\/?/, '')
  return `#/p/${project}/${rest}`
}

// The project an address without one opens in: the one the tab last showed,
// else the one the person last used, else the first they may enter. Those
// the person may not enter, once known, are passed over; while the
// projects they may enter aren't known, the tab's or the last used is
// taken as it is. Null when there is none to take, or none known yet.
export function projectFor(tab: number | null, last: number | null, enterable: number[] | null): number | null {
  if (enterable === null) return tab ?? last
  for (const p of [tab, last]) if (p !== null && enterable.includes(p)) return p
  return enterable[0] ?? null
}

// The address to put in place of one naming no project, in the project
// given; null when it needs none: it names one already, is outside the
// projects, or there is no project to put it in.
export function rewritten(hash: string, project: number | null): string | null {
  const { project: named, page } = splitHash(hash)
  if (named !== null || outsideProjects(page) || project === null) return null
  return hashIn(project, page)
}

// An address reached in a tab, as by a link followed there or an entry of
// its history, in the project of the address the tab had before when it
// names none and that one did; else as it is, an address naming none in a
// tab outside the projects left for App.tsx to put in the last used.
export const inTabsProject = (hash: string, before: string | undefined): string =>
  rewritten(hash, before === undefined ? null : splitHash(before).project) ?? hash

// The key a browser keeps something under that is the project's own, such
// as the commands picked lately in the palette: each project has its own.
export const projectKey = (key: string, project: number | null) => (project === null ? key : `${key}.p${project}`)

// The window's side: the open tab's address, which is the page's own, as
// only the open tab's page is shown.

// The project of the page shown; none outside the projects, or at an
// address naming none.
export const currentProject = (): number | null => splitHash(window.location.hash).project

// The address of the page shown, without its project, as App.tsx and the
// pages read it.
export const currentPage = (): string => splitHash(window.location.hash).page

// The address of a page, '#/flows/12' as written before projects, in the
// project of the page shown, or the one given: every link and address the
// app writes goes through here.
export const pageHash = (page: string, project: number | null = currentProject()): string => hashIn(project, page)

// Puts the address of a page, in the project of the page shown, in place of
// the open tab's, keeping its history entry, as a page does to keep what it
// shows in its address.
export function replacePage(page: string) {
  const hash = pageHash(page)
  if (window.location.hash !== hash) window.history.replaceState(window.history.state, '', window.location.pathname + window.location.search + hash)
}

// Where the project last used is remembered, in this browser.
const LAST = 'spoletta.project'

export function lastProject(): number | null {
  try {
    const n = Number(localStorage.getItem(LAST))
    return Number.isSafeInteger(n) && n > 0 ? n : null
  } catch {
    return null
  }
}

export function rememberProject(project: number) {
  try {
    localStorage.setItem(LAST, String(project))
  } catch {
    // An address naming no project opens in the person's first.
  }
}
