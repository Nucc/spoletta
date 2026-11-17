import { useCallback, useEffect, useState, type FormEvent } from 'react'
import {
  listProjectGroups,
  listProjectMembers,
  listUsers,
  setProjectGroups,
  setProjectMembers,
  updateProject,
  type OthersAccess,
  type ProjectGroup,
  type ProjectLevel,
  type ProjectMember,
  type User,
} from './api'
import { reloadMe, useMe } from './Account'
import { currentProject } from './address'
import { PlusIcon } from './icons'
import { SettingsEmpty, SettingsHead } from './SettingsHead'
import { formatTime } from './Runs'
import { useUndoNotice } from './UndoNotice'

// Who enters the project the settings are in, and what they may do there,
// for its owners (projects.go): the people given it by hand, each at a
// level; the identity provider's groups it is given to, each at a level,
// so whoever is in one enters as they sign in; and what anyone else signed
// in may do. Someone who came in through a group, as anyone signed in, or
// as an admin of Spoletta is listed with how; picking their level gives it
// to them by hand from then. Taking someone or a group out is undone by
// adding them back, so it asks nothing first but offers to undo it; those
// taken out are listed apart.

const LEVELS: ProjectLevel[] = ['owner', 'editor', 'viewer']

// What each level lets its holders do, under the levels.
const LEVEL_TEXT: Record<ProjectLevel, string> = {
  owner: 'Owners do everything: the project’s settings, keys, credentials and limits, and who has access.',
  editor:
    'Editors change and run its flows, task boards, procedures and structs, and their runs use the project’s model keys, MCP servers and credentials.',
  viewer: 'Viewers see its boards and the data its runs and records hold, and change and start nothing.',
}

// How someone came to be a member.
const SOURCE_WORDS: Record<ProjectMember['source'], string> = {
  manual: 'Given access by hand',
  group: 'Through a group of theirs',
  open: 'As anyone signed in may enter',
  super: 'As an admin of Spoletta',
}

// What anyone signed in, given access or not, may do, each said plainly.
const OTHERS: { value: OthersAccess; label: string; text: string }[] = [
  { value: 'none', label: 'Nothing', text: 'Only the people and groups given access above enter the project.' },
  {
    value: 'view',
    label: 'See it',
    text: 'Anyone signed in enters it as a viewer: they see its boards, its runs and the data they held, and its records, and change and start nothing.',
  },
  {
    value: 'edit',
    label: 'Change and run it',
    text: 'Anyone signed in enters it as an editor: they change and run its flows, which then run on the project’s model keys, MCP servers and credentials.',
  },
]

export function AccessSection({ onError }: { onError: (err: unknown) => void }) {
  const me = useMe()
  const project = currentProject()
  const here = me?.projects.find((p) => p.id === project)
  const [members, setMembers] = useState<ProjectMember[] | null>(null)
  const [groups, setGroups] = useState<ProjectGroup[] | null>(null)
  // The instance's people to give access to; null when whoever asks may not
  // list them.
  const [people, setPeople] = useState<User[] | null>(null)
  const [showUndo, , undoNotice] = useUndoNotice()

  const refresh = useCallback(async () => {
    if (project === null) return
    const [ms, gs] = await Promise.all([listProjectMembers(project), listProjectGroups(project)])
    setMembers(ms)
    setGroups(gs)
  }, [project])

  useEffect(() => {
    refresh().catch(onError)
    listUsers().then(setPeople, () => setPeople(null))
  }, [refresh, onError])

  if (project === null || !here) return null

  // Those given it by hand now, which a change of who has access replaces.
  const manual = (members ?? []).filter((m) => m.source === 'manual' && !m.removedAt).map((m) => ({ user: m.user, level: m.level }))
  const give = async (to: { user: number; level: ProjectLevel }[]) => {
    try {
      setMembers(await setProjectMembers(project, to))
      // The person's own level may be what changed.
      reloadMe().catch(() => {})
    } catch (err) {
      onError(err)
    }
  }
  const setLevel = (m: ProjectMember, level: ProjectLevel) => give([...manual.filter((x) => x.user !== m.user), { user: m.user, level }])
  const takeOut = async (m: ProjectMember) => {
    await give(manual.filter((x) => x.user !== m.user))
    showUndo({ message: `Took ${m.name || m.email} out`, undo: () => give([...manual.filter((x) => x.user !== m.user), { user: m.user, level: m.level }]) })
  }

  const putGroups = async (to: ProjectGroup[]) => {
    try {
      setGroups(await setProjectGroups(project, to))
      reloadMe().catch(() => {})
    } catch (err) {
      onError(err)
    }
  }

  const takeFromGroup = async (g: ProjectGroup) => {
    const before = groups ?? []
    await putGroups(before.filter((x) => x.group !== g.group))
    showUndo({ message: `Took access from ${g.group}`, undo: () => putGroups([...before.filter((x) => x.group !== g.group), g]) })
  }

  const change = async (patch: { name?: string; othersAccess?: OthersAccess }) => {
    try {
      await updateProject(project, patch)
      await reloadMe()
    } catch (err) {
      onError(err)
    }
  }

  const current = (members ?? []).filter((m) => !m.removedAt)
  const removed = (members ?? []).filter((m) => m.removedAt)
  const others = (people ?? []).filter((p) => !current.some((m) => m.user === p.id && m.source === 'manual'))

  return (
    <section className="flow-section project-access" aria-label="Access">
      <SettingsHead
        title="Access"
        lead="Who enters this project, and what they may do in it."
        more={
          <>
            Someone enters at the highest level any way gives them: given access by hand, through a group of theirs, or as
            anyone signed in may. An admin of Spoletta enters every project as an owner. {LEVEL_TEXT.owner} {LEVEL_TEXT.editor}{' '}
            {LEVEL_TEXT.viewer} Within the project, a flow is still its maker’s, a team’s or everyone’s, and owners see and change
            them all.
          </>
        }
      />
      <ProjectName name={here.name} onRename={(name) => change({ name })} />

      <SettingsHead title="People" level={3} lead={`${LEVEL_TEXT.owner} ${LEVEL_TEXT.editor} ${LEVEL_TEXT.viewer}`} />
      {people !== null && others.length > 0 && <AddMember people={others} onAdd={(user, level) => give([...manual, { user, level }])} />}
      {members === null ? (
        <p className="flows-note">Loading…</p>
      ) : current.length === 0 ? (
        <SettingsEmpty>Nobody is given access yet. Add the people who work here, or give access to a group below.</SettingsEmpty>
      ) : (
        <ul className="flow-list people-list access-list">
          {current.map((m) => (
            <li key={m.user}>
              <div className="flow-card">
                <span className="flow-card-name">{m.name || m.email}</span>
                <span className="flows-note">
                  {m.name && `${m.email} · `}
                  {SOURCE_WORDS[m.source]}
                </span>
              </div>
              <LevelPick
                level={m.level}
                label={`${m.name || m.email}'s level`}
                title={m.source === 'manual' ? undefined : 'Picking a level gives them access by hand at it, whatever else gives them'}
                onChange={(level) => setLevel(m, level)}
              />
              {m.source === 'manual' ? (
                <button className="icon-button" onClick={() => void takeOut(m)} aria-label={`Take ${m.name || m.email} out`} title="Take out of the project">
                  ×
                </button>
              ) : (
                <span className="access-spacer" aria-hidden />
              )}
            </li>
          ))}
        </ul>
      )}
      {people === null && (
        <p className="flows-note">You can’t list the instance’s people to add someone here; an admin of Spoletta can.</p>
      )}
      {removed.length > 0 && (
        <>
          <h4 className="access-apart">Taken out</h4>
          <ul className="flow-list people-list access-list removed">
            {removed.map((m) => (
              <li key={m.user}>
                <div className="flow-card">
                  <span className="flow-card-name">{m.name || m.email}</span>
                  <span className="flows-note">
                    {m.name && `${m.email} · `}
                    {SOURCE_WORDS[m.source]}, taken out {formatTime(m.removedAt!)}
                  </span>
                </div>
                <button className="panel-button" onClick={() => give([...manual, { user: m.user, level: m.level }])} title={`Give them access by hand again, as ${m.level}`}>
                  Add back
                </button>
              </li>
            ))}
          </ul>
        </>
      )}

      <SettingsHead
        title="Groups"
        level={3}
        lead="Groups people are in at the identity provider they sign in with, by name. Whoever is in one enters at its level as they next sign in."
        more="The groups come from the claim OIDC_GROUPS_CLAIM names, in the sign-in's ID token or user info; signing in with Google sends none. Taking a group's access away takes it from those it gave it to."
      />
      <AddGroup taken={(groups ?? []).map((g) => g.group)} onAdd={(g) => putGroups([...(groups ?? []), g])} />
      {groups !== null && groups.length > 0 && (
        <ul className="flow-list people-list access-list">
          {groups.map((g) => (
            <li key={g.group}>
              <div className="flow-card">
                <span className="flow-card-name">{g.group}</span>
              </div>
              <LevelPick
                level={g.level}
                label={`${g.group}'s level`}
                onChange={(level) => putGroups(groups.map((x) => (x.group === g.group ? { ...x, level } : x)))}
              />
              <button className="icon-button" onClick={() => void takeFromGroup(g)} aria-label={`Take access from ${g.group}`} title="Take access away from the group">
                ×
              </button>
            </li>
          ))}
        </ul>
      )}

      <SettingsHead title="Everyone else signed in" level={3} lead="What anyone signed in to Spoletta may do here, given access or not." />
      <div className="access-others" role="radiogroup" aria-label="What everyone else signed in may do">
        {OTHERS.map((o) => (
          <label key={o.value} className="access-other">
            <input type="radio" name="others-access" checked={here.othersAccess === o.value} onChange={() => change({ othersAccess: o.value })} />
            <span>
              <strong>{o.label}</strong>
              <small className="panel-note">{o.text}</small>
            </span>
          </label>
        ))}
      </div>
      {undoNotice}
    </section>
  )
}

// The project's name, renamed in place.
function ProjectName({ name, onRename }: { name: string; onRename: (name: string) => void }) {
  const [draft, setDraft] = useState(name)
  useEffect(() => setDraft(name), [name])
  const rename = () => {
    const n = draft.trim()
    if (n && n !== name) onRename(n)
    else setDraft(name)
  }
  return (
    <label className="panel-field access-name">
      <span>Name</span>
      <input
        value={draft}
        aria-label="Project name"
        onChange={(e) => setDraft(e.target.value)}
        onBlur={rename}
        onKeyDown={(e) => {
          if (e.key === 'Enter') rename()
          if (e.key === 'Escape') setDraft(name)
        }}
      />
    </label>
  )
}

function LevelPick({ level, label, title, onChange }: { level: ProjectLevel; label: string; title?: string; onChange: (level: ProjectLevel) => void }) {
  return (
    <span className="person-roles access-level">
      <select value={level} aria-label={label} title={title} onChange={(e) => onChange(e.target.value as ProjectLevel)}>
        {LEVELS.map((l) => (
          <option key={l} value={l}>
            {l.charAt(0).toUpperCase() + l.slice(1)}
          </option>
        ))}
      </select>
    </span>
  )
}

// Gives someone of the instance's people access, at a level.
function AddMember({ people, onAdd }: { people: User[]; onAdd: (user: number, level: ProjectLevel) => void }) {
  const [user, setUser] = useState('')
  const [level, setLevel] = useState<ProjectLevel>('editor')
  const add = (e: FormEvent) => {
    e.preventDefault()
    if (!user) return
    onAdd(Number(user), level)
    setUser('')
  }
  return (
    <form className="flow-new access-add" onSubmit={add}>
      <select aria-label="Someone to give access" value={user} onChange={(e) => setUser(e.target.value)}>
        <option value="">Someone to give access…</option>
        {people.map((p) => (
          <option key={p.id} value={p.id}>
            {p.name ? `${p.name} (${p.email})` : p.email}
          </option>
        ))}
      </select>
      <select aria-label="Their level" value={level} onChange={(e) => setLevel(e.target.value as ProjectLevel)}>
        {LEVELS.map((l) => (
          <option key={l} value={l}>
            {l.charAt(0).toUpperCase() + l.slice(1)}
          </option>
        ))}
      </select>
      <button type="submit" disabled={!user}>
        <PlusIcon /> Give access
      </button>
    </form>
  )
}

// Gives a group access by its name, at a level.
function AddGroup({ taken, onAdd }: { taken: string[]; onAdd: (g: ProjectGroup) => void }) {
  const [name, setName] = useState('')
  const [level, setLevel] = useState<ProjectLevel>('editor')
  const n = name.trim()
  const add = (e: FormEvent) => {
    e.preventDefault()
    if (!n || taken.includes(n)) return
    onAdd({ group: n, level })
    setName('')
  }
  return (
    <form className="flow-new access-add" onSubmit={add}>
      <input placeholder="A group’s name, as people’s sign-in names it" aria-label="Group name" value={name} onChange={(e) => setName(e.target.value)} />
      <select aria-label="The group’s level" value={level} onChange={(e) => setLevel(e.target.value as ProjectLevel)}>
        {LEVELS.map((l) => (
          <option key={l} value={l}>
            {l.charAt(0).toUpperCase() + l.slice(1)}
          </option>
        ))}
      </select>
      <button type="submit" disabled={!n || taken.includes(n)}>
        <PlusIcon /> Give access
      </button>
    </form>
  )
}
