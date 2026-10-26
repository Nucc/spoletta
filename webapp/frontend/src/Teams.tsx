import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { createTeam, deleteTeam, listTeams, listUsers, updateTeam, type Team, type User } from './api'
import { PlusIcon, TrashIcon } from './icons'
import { useConfirm } from './Confirm'

// The teams, which own flows together. Everyone sees them, and who is in
// them; admins and team managers make them and say who is in them.
export function TeamsSection({ canManage, onError }: { canManage: boolean; onError: (err: unknown) => void }) {
  const [teams, setTeams] = useState<Team[] | null>(null)
  const [people, setPeople] = useState<User[]>([])
  const [confirm, confirmDialog] = useConfirm()
  const [name, setName] = useState('')

  const refresh = useCallback(async () => setTeams(await listTeams()), [])

  useEffect(() => {
    refresh().catch(onError)
    if (canManage) listUsers().then(setPeople, onError)
  }, [refresh, canManage, onError])

  const act = async (f: () => Promise<unknown>) => {
    try {
      await f()
      await refresh()
    } catch (err) {
      onError(err)
    }
  }

  const onCreate = (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    act(async () => {
      await createTeam({ name: name.trim() })
      setName('')
    })
  }

  return (
    <section className="flow-section" aria-label="Teams">
      <h2>Teams</h2>
      <p className="flows-note">
        A flow, procedure or struct given to a team is its members'. They see it, change it and run it; nobody else
        does, but admins.{canManage ? '' : ' Admins and team managers make teams and say who is in them.'}
      </p>
      {canManage && (
        <form className="flow-new" onSubmit={onCreate}>
          <input
            placeholder="Name a new team, such as Payments"
            aria-label="Team name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <button type="submit" disabled={!name.trim()}>
            <PlusIcon /> New team
          </button>
        </form>
      )}
      {teams === null ? (
        <p className="flows-note">Loading…</p>
      ) : teams.length === 0 ? (
        <p className="flows-note">No teams yet.</p>
      ) : (
        <ul className="flow-list people-list">
          {teams.map((t) => (
            <TeamItem
              key={t.id}
              team={t}
              people={people}
              canManage={canManage}
              onRename={(name) => act(() => updateTeam(t.id, { name }))}
              onMembers={(members) => act(() => updateTeam(t.id, { members }))}
              onDelete={async () => {
                const yes = await confirm({
                  title: `Delete the team ${t.name}?`,
                  message: <p>What it owns must be given to someone else first. This can't be undone.</p>,
                  confirm: 'Delete',
                })
                if (yes) act(() => deleteTeam(t.id))
              }}
            />
          ))}
        </ul>
      )}
      {confirmDialog}
    </section>
  )
}

function TeamItem({
  team: t,
  people,
  canManage,
  onRename,
  onMembers,
  onDelete,
}: {
  team: Team
  people: User[]
  canManage: boolean
  onRename: (name: string) => void
  onMembers: (members: number[]) => void
  onDelete: () => void
}) {
  const [name, setName] = useState(t.name)
  const [adding, setAdding] = useState('')
  const members = t.members.map((m) => m.id)
  const others = people.filter((p) => !members.includes(p.id))
  const rename = () => name.trim() && name.trim() !== t.name && onRename(name.trim())

  return (
    <li>
      <div className="flow-card">
        {canManage ? (
          <input
            className="team-name"
            value={name}
            aria-label="Team name"
            onChange={(e) => setName(e.target.value)}
            onBlur={rename}
            onKeyDown={(e) => e.key === 'Enter' && rename()}
          />
        ) : (
          <span className="flow-card-name">{t.name}</span>
        )}
        <div className="person-roles">
          {t.members.length === 0 && <span className="flows-note">Nobody yet.</span>}
          {t.members.map((m) => (
            <span key={m.id} className="person-role" title={m.email}>
              {m.name || m.email}
              {canManage && (
                <button
                  type="button"
                  aria-label={`Take ${m.name || m.email} out`}
                  onClick={() => onMembers(members.filter((id) => id !== m.id))}
                >
                  ×
                </button>
              )}
            </span>
          ))}
          {canManage && others.length > 0 && (
            <select
              aria-label="Add someone"
              value={adding}
              onChange={(e) => {
                setAdding('')
                if (e.target.value) onMembers([...members, Number(e.target.value)])
              }}
            >
              <option value="">Add someone…</option>
              {others.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name || p.email}
                </option>
              ))}
            </select>
          )}
        </div>
      </div>
      {canManage && (
        <button className="icon-button" onClick={onDelete} aria-label={`Delete ${t.name}`} title="Delete team">
          <TrashIcon />
        </button>
      )}
    </li>
  )
}
