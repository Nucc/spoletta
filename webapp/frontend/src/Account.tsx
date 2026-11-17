import { useEffect, useState } from 'react'
import { getMe, signOut, type Me, type Project, type ProjectLevel } from './api'
import { currentProject, pageHash } from './address'
import { GearIcon } from './icons'

// Who is signed in, asked once for the whole page, and again after reload().
let mePromise: Promise<Me> | null = null
const meListeners = new Set<(me: Me) => void>()

function loadMe(): Promise<Me> {
  mePromise ??= getMe()
  return mePromise
}

// Asks again who is signed in, as after an admin changed themselves, or
// the projects they may enter changed.
export function reloadMe(): Promise<Me> {
  mePromise = getMe()
  mePromise.then(
    (me) => meListeners.forEach((l) => l(me)),
    () => {},
  )
  return mePromise
}

// Who is signed in, and whether anyone signs in at all; null until known.
export function useMe(): Me | null {
  const [me, setMe] = useState<Me | null>(null)
  useEffect(() => {
    loadMe().then(setMe, () => setMe(null))
    meListeners.add(setMe)
    return () => {
      meListeners.delete(setMe)
    }
  }, [])
  return me
}

// The person's level in the project, the page's unless told another, as
// /api/me lists the projects they may enter; none in one they may not
// enter, or outside the projects. Everyone owns every project without
// sign-in.
export function levelIn(me: Me | null, project: number | null = currentProject()): ProjectLevel | null {
  return projectHere(me, project)?.level ?? null
}

// The project the page is in, the page's unless told another, as /api/me
// lists it; none outside the projects, or in one the person may not enter.
export function projectHere(me: Me | null, project: number | null = currentProject()): Project | null {
  if (me === null || project === null) return null
  return me.projects.find((p) => p.id === project) ?? null
}

// Whether the project the page is in gets what the instance is configured
// with outside the app (the worker's own models and their keys, keys in
// the webapp's environment, its git token and GitHub App), as Legacy does;
// another has only what its owners set in its settings.
export const inheritsInstance = (me: Me | null) => projectHere(me)?.inheritsInstance ?? false

// Whether whoever is signed in configures the project the page is in: one
// of its owners, as everyone is when nobody signs in.
export const configures = (me: Me | null) => me !== null && (!me.signIn || levelIn(me) === 'owner')

// Whether whoever is signed in only sees the project the page is in: they
// see its boards, runs and records, and change and start nothing there,
// but for starring a flow and deciding the reviews their roles decide.
// Every control that changes or starts something asks this, beside what
// the server says of each flow (Flow.access), so nothing is offered that
// the server would refuse.
export const onlySees = (me: Me | null) => me !== null && me.signIn && levelIn(me) === 'viewer'

// Whether whoever is signed in may change the flow, procedure, struct or
// task board: the server says so of each (access), unless they only see
// the project it is in.
export const mayChange = (owned: { access: string }, me: Me | null) => owned.access === 'edit' && !onlySees(me)

// Whether whoever is signed in makes, renames, archives and deletes
// projects: the instance's admin, as everyone is without sign-in.
export const managesProjects = (me: Me | null) => !!me?.super

// Whether whoever is signed in manages the instance's people: its admin;
// nobody without sign-in, when there are no people.
export const managesPeople = (me: Me | null) => !!me?.user && me.super

// The settings, who is signed in, and signing out.
export function Account() {
  const me = useMe()
  if (!me) return null
  return (
    <span className="account">
      <a
        className="toolbar-link"
        href={pageHash('#/settings')}
        title="Your model keys and MCP connections, and for the project's owners, its access, teams and what runs use"
      >
        <GearIcon /> Settings
      </a>
      {me.user && (
        <>
          <span className="account-name" title={me.user.email}>
            {me.user.name || me.user.email}
          </span>
          <button className="toolbar-link" onClick={() => signOut()}>
            Sign out
          </button>
        </>
      )}
    </span>
  )
}
