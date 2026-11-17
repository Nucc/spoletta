import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { addUser, listUsers, removeUser, updateUser, type User } from './api'
import { useMe } from './Account'
import { PlusIcon, TrashIcon } from './icons'
import { SettingsHead } from './SettingsHead'
import { formatTime } from './Runs'
import { useConfirm } from './Confirm'

// The people who sign in, for the instance's admins, in the settings'
// Instance group: adding someone, before they first sign in if need be,
// making them an admin of the instance (who makes projects and enters each
// as its owner) or a team manager, and removing them. What a person is in a
// project, the projects they enter, the roles they hold and the teams they
// are in, is each project's: its owners give access and roles in its
// Access tab (Access.tsx) and keep its teams in its Teams tab, so none of
// it shows here.
export function PeopleSection({ onError }: { onError: (err: unknown) => void }) {
  const [people, setPeople] = useState<User[] | null>(null)
  const [confirm, confirmDialog] = useConfirm()
  const me = useMe()
  const [email, setEmail] = useState('')

  const refresh = useCallback(async () => setPeople(await listUsers()), [])

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

  const onUpdate = async (id: number, patch: { admin?: boolean; teamManager?: boolean }) => {
    try {
      const updated = await updateUser(id, patch)
      setPeople((ps) => ps && ps.map((p) => (p.id === id ? updated : p)))
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
      <SettingsHead
        title="People"
        lead="Who signs in to Spoletta, and whether they are its admins or team managers. Add someone to let them sign in from any domain; each project's owners say who enters it and give its roles."
        more={
          <>
            Admins make projects, enter each as its owner and keep the people here. Team managers make teams and say who
            is in them, in each project they enter. Which projects someone enters, the roles they hold there and the
            teams they are in are each project's, kept in its settings' Access and Teams tabs.
          </>
        }
      />
      <form className="flow-new" onSubmit={onAdd}>
        <input
          type="email"
          placeholder="Email of someone to add"
          aria-label="Email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
        <button type="submit" disabled={!email.trim()}>
          <PlusIcon /> Add person
        </button>
      </form>
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
                  </span>
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
