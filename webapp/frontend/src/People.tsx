import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { addUser, getMe, listRoles, listUsers, updateUser, type User } from './api'
import { FlowBuilderIcon, PlusIcon } from './icons'
import { formatTime } from './Runs'

// The people who sign in, for admins: adding someone, before they first sign
// in if need be, making them an admin, and giving them roles. A role is the
// name of the human objects whose processes its holders decide, such as
// Finance where "Finance handles Approve payment".
export default function People() {
  const [people, setPeople] = useState<User[] | null>(null)
  const [roles, setRoles] = useState<string[]>([])
  const [me, setMe] = useState<User | null>(null)
  const [email, setEmail] = useState('')
  const [error, setError] = useState<string | null>(null)

  const report = (err: unknown) => setError(err instanceof Error ? err.message : String(err))

  const refresh = useCallback(async () => {
    const [ps, rs] = await Promise.all([listUsers(), listRoles()])
    setPeople(ps)
    setRoles(rs)
  }, [])

  useEffect(() => {
    document.title = 'People · Flows'
    getMe()
      .then((m) => setMe(m.user))
      .catch(report)
    refresh().catch(report)
  }, [refresh])

  const onAdd = async (e: FormEvent) => {
    e.preventDefault()
    try {
      await addUser({ email: email.trim() })
      setEmail('')
      await refresh()
    } catch (err) {
      report(err)
    }
  }

  const onUpdate = async (id: number, patch: { admin?: boolean; roles?: string[] }) => {
    try {
      const updated = await updateUser(id, patch)
      setPeople((ps) => ps && ps.map((p) => (p.id === id ? updated : p)))
      setRoles((rs) => [...new Set([...rs, ...updated.roles])].sort((a, b) => a.localeCompare(b)))
    } catch (err) {
      report(err)
    }
  }

  return (
    <div className="flows-page">
      <header className="toolbar">
        <h1>
          <a className="home" href="#/" title="All flows">
            <FlowBuilderIcon /> Flows
          </a>
          <span className="crumb-separator" aria-hidden>
            /
          </span>
          People
        </h1>
      </header>
      {error && (
        <div className="error-banner" role="alert" onClick={() => setError(null)}>
          {error} (click to dismiss)
        </div>
      )}
      <main className="flows">
        <section className="flow-section" aria-label="People">
          <h2>People</h2>
          <p className="flows-note">
            Someone holding a role decides the reviews of the processes that a person of that name handles on any board,
            such as Finance in “Finance handles Approve payment”. A review no person handles is anyone's to decide; admins
            decide any. Add someone to let them sign in from any domain.
          </p>
          <form className="flow-new" onSubmit={onAdd}>
            <input
              type="email"
              placeholder="Email of someone to add"
              aria-label="Email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
            />
            <button type="submit" disabled={!email.trim()}>
              <PlusIcon /> Add
            </button>
          </form>
          <datalist id="role-names">
            {roles.map((r) => (
              <option key={r} value={r} />
            ))}
          </datalist>
          {people === null ? (
            <p className="flows-note">Loading…</p>
          ) : (
            <ul className="flow-list people-list">
              {people.map((p) => (
                <li key={p.id}>
                  <div className="flow-card">
                    <span className="flow-card-name">{p.name || p.email}</span>
                    <span className="flows-note">
                      {p.name && `${p.email} · `}
                      {p.signedInAt ? `Signed in ${formatTime(p.signedInAt)}` : 'Hasn’t signed in yet'}
                    </span>
                    <RoleEditor roles={p.roles} onChange={(roles) => onUpdate(p.id, { roles })} />
                  </div>
                  <label className="person-admin" title={p.id === me?.id ? 'Another admin can take this from you' : undefined}>
                    <input
                      type="checkbox"
                      checked={p.admin}
                      disabled={p.id === me?.id}
                      onChange={(e) => onUpdate(p.id, { admin: e.target.checked })}
                    />
                    Admin
                  </label>
                </li>
              ))}
            </ul>
          )}
        </section>
      </main>
    </div>
  )
}

// A person's roles, each taken away with its ×, and one added by name.
function RoleEditor({ roles, onChange }: { roles: string[]; onChange: (roles: string[]) => void }) {
  const [name, setName] = useState('')
  const onAdd = (e: FormEvent) => {
    e.preventDefault()
    const n = name.trim()
    if (!n) return
    if (!roles.some((r) => r.toLowerCase() === n.toLowerCase())) onChange([...roles, n])
    setName('')
  }
  return (
    <form className="person-roles" onSubmit={onAdd}>
      {roles.map((r) => (
        <span key={r} className="person-role">
          {r}
          <button type="button" aria-label={`Take away ${r}`} onClick={() => onChange(roles.filter((x) => x !== r))}>
            ×
          </button>
        </span>
      ))}
      <input
        list="role-names"
        placeholder="Add a role"
        aria-label="Add a role"
        value={name}
        onChange={(e) => setName(e.target.value)}
      />
    </form>
  )
}
