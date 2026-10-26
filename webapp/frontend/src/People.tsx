import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { addUser, listRoles, listUsers, removeUser, updateUser, type User } from './api'
import { useMe } from './Account'
import { PlusIcon, TrashIcon } from './icons'
import { formatTime } from './Runs'
import { useConfirm } from './Confirm'

// The people who sign in, for admins: adding someone, before they first sign
// in if need be, making them an admin or a team manager, giving them roles,
// and removing them. A role is the name of the human objects whose processes
// its holders decide, such as Finance where "Finance handles Approve payment".
export function PeopleSection({ onError }: { onError: (err: unknown) => void }) {
  const [people, setPeople] = useState<User[] | null>(null)
  const [roles, setRoles] = useState<string[]>([])
  const [confirm, confirmDialog] = useConfirm()
  const me = useMe()
  const [email, setEmail] = useState('')

  const refresh = useCallback(async () => {
    const [ps, rs] = await Promise.all([listUsers(), listRoles()])
    setPeople(ps)
    setRoles(rs)
  }, [])

  useEffect(() => {
    refresh().catch(onError)
  }, [refresh, onError])

  const onAdd = async (e: FormEvent) => {
    e.preventDefault()
    try {
      await addUser({ email: email.trim() })
      setEmail('')
      await refresh()
    } catch (err) {
      onError(err)
    }
  }

  const onUpdate = async (id: number, patch: { admin?: boolean; teamManager?: boolean; roles?: string[] }) => {
    try {
      const updated = await updateUser(id, patch)
      setPeople((ps) => ps && ps.map((p) => (p.id === id ? updated : p)))
      setRoles((rs) => [...new Set([...rs, ...updated.roles])].sort((a, b) => a.localeCompare(b)))
    } catch (err) {
      onError(err)
    }
  }

  const onRemove = async (p: User) => {
    const yes = await confirm({
      title: `Remove ${p.name || p.email}?`,
      message: <p>They are signed out and can't sign in until added back; their flows become yours.</p>,
      confirm: 'Remove',
    })
    if (!yes) return
    try {
      await removeUser(p.id)
      await refresh()
    } catch (err) {
      onError(err)
    }
  }

  return (
    <section className="flow-section" aria-label="People">
      <h2>People</h2>
      <p className="flows-note">
        Someone holding a role decides the reviews of the processes that a person of that name handles on any board,
        such as Finance in “Finance handles Approve payment”, and sees those flows to do so. A review no person handles
        is anyone's to decide; admins decide any. Team managers make teams and say who is in them. Add someone to let
        them sign in from any domain.
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
          {people.map((p) => {
            const self = p.id === me?.user?.id
            return (
              <li key={p.id}>
                <div className="flow-card">
                  <span className="flow-card-name">{p.name || p.email}</span>
                  <span className="flows-note">
                    {p.name && `${p.email} · `}
                    {p.signedInAt ? `Signed in ${formatTime(p.signedInAt)}` : 'Hasn’t signed in yet'}
                    {p.teams.length > 0 && ` · ${p.teams.map((t) => t.name).join(', ')}`}
                  </span>
                  <RoleEditor roles={p.roles} onChange={(roles) => onUpdate(p.id, { roles })} />
                </div>
                <div className="person-flags">
                  <label className="person-admin" title={self ? 'Another admin can take this from you' : undefined}>
                    <input
                      type="checkbox"
                      checked={p.admin}
                      disabled={self}
                      onChange={(e) => onUpdate(p.id, { admin: e.target.checked })}
                    />
                    Admin
                  </label>
                  <label className="person-admin" title="Makes teams and says who is in them">
                    <input
                      type="checkbox"
                      checked={p.teamManager || p.admin}
                      disabled={p.admin}
                      onChange={(e) => onUpdate(p.id, { teamManager: e.target.checked })}
                    />
                    Team manager
                  </label>
                  {!self && (
                    <button
                      className="icon-button"
                      onClick={() => onRemove(p)}
                      aria-label={`Remove ${p.name || p.email}`}
                      title="Remove"
                    >
                      <TrashIcon />
                    </button>
                  )}
                </div>
              </li>
            )
          })}
        </ul>
      )}
      {confirmDialog}
    </section>
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
