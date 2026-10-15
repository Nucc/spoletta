import { useEffect, useState } from 'react'
import { getMe, signOut, type Me } from './api'
import { GearIcon } from './icons'

// Who is signed in, asked once for the whole page, and again after reload().
let mePromise: Promise<Me> | null = null
const meListeners = new Set<(me: Me) => void>()

function loadMe(): Promise<Me> {
  mePromise ??= getMe()
  return mePromise
}

// Asks again who is signed in, as after an admin changed themselves.
export function reloadMe() {
  mePromise = getMe()
  mePromise.then(
    (me) => meListeners.forEach((l) => l(me)),
    () => {},
  )
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

// Whether whoever is signed in configures the instance: an admin, or anyone
// when nobody signs in.
export const configures = (me: Me | null) => me !== null && (!me.signIn || !!me.user?.admin)

// The settings, who is signed in, and signing out.
export function Account() {
  const me = useMe()
  if (!me) return null
  return (
    <span className="account">
      <a
        className="toolbar-link"
        href="#/settings"
        title="Your model keys and MCP connections, and for admins, people and teams"
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
