import { useEffect, useState } from 'react'
import { getMe, signOut, type Me } from './api'

// Who is signed in, the people page for admins, and signing out; nothing
// when nobody signs in.
export function Account() {
  const [me, setMe] = useState<Me | null>(null)
  useEffect(() => {
    getMe().then(setMe, () => setMe(null))
  }, [])
  if (!me?.user) return null
  return (
    <span className="account">
      {me.user.admin && (
        <a className="toolbar-link" href="#/people">
          People
        </a>
      )}
      <span className="account-name" title={me.user.email}>
        {me.user.name || me.user.email}
      </span>
      <button className="toolbar-link" onClick={() => signOut()}>
        Sign out
      </button>
    </span>
  )
}
