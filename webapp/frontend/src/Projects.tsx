import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { createProject, deleteProject, listProjects, updateProject, type Me, type Project, type ProjectLevel } from './api'
import { managesProjects, reloadMe, useMe } from './Account'
import { currentProject, pageHash } from './address'
import { ChevronDownIcon, GearIcon, LockIcon, PlusIcon } from './icons'
import { ToolbarMenu, type ToolbarSection } from './Toolbar'
import { useConfirm } from './Confirm'
import { useUndoNotice } from './UndoNotice'
import TabLink from './TabLink'
import ErrorBanner from './ErrorBanner'

// Projects, each a space of its own with its own flows, task boards,
// settings and people (docs/PROJECTS.md). Every page's breadcrumb starts
// with the project it is in, which switches the tab to another of the
// person's projects, at its home page (ProjectSwitcher). The Projects page
// (#/projects), outside any project, is where the instance's admin makes,
// renames, archives, brings back and deletes them, and opens each one's
// access, which its owners keep in its settings (Access.tsx). Someone who
// may enter no project, or not the one an address names, is told so and
// whom to ask (NoProject).

// What a level lets the person do, as the switcher and the Projects page
// say it.
export const LEVEL_WORDS: Record<ProjectLevel, { label: string; you: string }> = {
  owner: { label: 'Owner', you: 'You own it: everything, its settings and who has access too' },
  editor: { label: 'Editor', you: 'You change and run its flows, task boards, procedures and structs' },
  viewer: { label: 'Viewer', you: 'You see its boards, runs and records, and change nothing' },
}

// The project the page is in, which opens a menu of the person's projects
// to switch the tab to another's home page; for the instance's admin, the
// Projects page too. With one project only it still says which.
export function ProjectSwitcher() {
  const me = useMe()
  const project = currentProject()
  if (!me) return null
  const here = me.projects.find((p) => p.id === project)
  const sections: ToolbarSection[] = [
    {
      title: 'Projects',
      items: me.projects.map((p) => ({
        id: `project:${p.id}`,
        label: p.name,
        icon: p.archived ? <LockIcon /> : undefined,
        hint: `${LEVEL_WORDS[p.level].label}${p.archived ? ' · archived' : ''}`,
        current: p.id === project,
        onSelect: () => {
          if (p.id !== project) window.location.hash = pageHash('#/', p.id)
        },
      })),
    },
  ]
  if (managesProjects(me)) {
    sections.push({
      items: [
        {
          id: 'projects',
          label: 'Every project…',
          hint: 'Make, rename, archive and delete projects, and say who enters each',
          icon: <GearIcon />,
          onSelect: () => {
            window.location.hash = pageHash('#/projects')
          },
        },
      ],
    })
  }
  return (
    <ToolbarMenu
      className="project-switcher"
      align="left"
      label={
        <>
          <span className="project-switcher-name">{here?.name ?? 'Projects'}</span>
          <ChevronDownIcon />
        </>
      }
      title={here ? `${here.name}: ${LEVEL_WORDS[here.level].you}. Switch to another project` : 'Switch to a project'}
      ariaLabel="Switch project"
      sections={sections}
    />
  )
}

// The start of every page's breadcrumb inside a project: the project, then
// Home, a link unless the page is the home page itself.
export function HomeCrumb({ here = false }: { here?: boolean }) {
  // The project, and the separator after it, show once who is signed in
  // is known, which is asked once for the whole page.
  const known = useMe() !== null
  return (
    <>
      {known && (
        <>
          <ProjectSwitcher />
          <span className="crumb-separator" aria-hidden>
            /
          </span>
        </>
      )}
      {here ? (
        'Home'
      ) : (
        <a className="home" href={pageHash('#/')} title="Every flow, task board, procedure and struct">
          Home
        </a>
      )}
    </>
  )
}

// The projects there are, for the instance's admin: making one, renaming,
// archiving (undone by bringing it back), deleting for good once archived,
// and opening each one's access in its settings. Anyone else reaching it
// sees the projects they may enter.
export function ProjectsPage() {
  const me = useMe()
  const [projects, setProjects] = useState<Project[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [name, setName] = useState('')
  const [confirm, confirmDialog] = useConfirm()
  const [showUndo, , undoNotice] = useUndoNotice()
  const manages = managesProjects(me)

  const report = useCallback((err: unknown) => setError(err instanceof Error ? err.message : String(err)), [])
  // The switcher lists them too, so who may enter what is asked again.
  const refresh = useCallback(async () => {
    setProjects(await listProjects())
    reloadMe().catch(() => {})
  }, [])

  useEffect(() => {
    document.title = 'Projects · Spoletta'
    listProjects().then(setProjects, report)
  }, [report])

  const act = async (f: () => Promise<unknown>) => {
    setError(null)
    try {
      await f()
      await refresh()
    } catch (err) {
      report(err)
    }
  }

  const onCreate = (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    act(async () => {
      await createProject(name.trim())
      setName('')
    })
  }

  const onArchive = (p: Project) =>
    act(async () => {
      await deleteProject(p.id)
      showUndo({ message: `Archived ${p.name}`, undo: () => act(() => updateProject(p.id, { archived: false })) })
    })

  const onDelete = async (p: Project) => {
    const yes = await confirm({
      title: `Delete ${p.name} for good?`,
      message: (
        <p>
          Everything in it goes with it: its flows, task boards, procedures and structs, their versions, runs and records, its
          settings, keys and credentials, and who has access. This can't be undone.
        </p>
      ),
      confirm: 'Delete for good',
    })
    if (yes) act(() => deleteProject(p.id, true))
  }

  return (
    <div className="flows-page">
      <header className="toolbar">
        <h1>
          <ProjectSwitcher />
        </h1>
      </header>
      <ErrorBanner error={error} onDismiss={() => setError(null)} />
      <main className="flows">
        <section className="flow-section" aria-label="Projects">
          <h2>
            <GearIcon /> Projects
          </h2>
          <p className="flows-note">
            {manages
              ? 'Each project is a space of its own: its own flows, task boards, procedures and structs, its own model providers, keys, MCP servers, credentials and limits, and its own people. Nothing in one reaches anything in another. An archived project is entered only by an admin, and its runs start no more until it is brought back.'
              : 'The projects you may enter. Only an admin of Spoletta makes and keeps projects.'}
          </p>
          {manages && (
            <form className="flow-new" onSubmit={onCreate}>
              <input
                placeholder="Name a new project, such as Payments"
                aria-label="Project name"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
              <button type="submit" disabled={!name.trim()}>
                <PlusIcon /> New project
              </button>
            </form>
          )}
          {projects === null ? (
            <p className="flows-note">Loading…</p>
          ) : (
            <ul className="flow-list trash-list project-list">
              {projects.map((p) => (
                <ProjectRow
                  key={p.id}
                  project={p}
                  manages={manages}
                  onRename={(name) => act(() => updateProject(p.id, { name }))}
                  onArchive={() => onArchive(p)}
                  onRestore={() => act(() => updateProject(p.id, { archived: false }))}
                  onDelete={() => onDelete(p)}
                />
              ))}
            </ul>
          )}
        </section>
      </main>
      {confirmDialog}
      {undoNotice}
    </div>
  )
}

// What anyone signed in may do in a project, as its row says it.
const OTHERS_WORDS = { none: 'Only those given access enter it', view: 'Anyone signed in sees it', edit: 'Anyone signed in changes and runs it' }

// The project Legacy, which holds what there was before projects and which
// everyone joins: archived like any, never deleted.
const LEGACY = 1

function ProjectRow({
  project: p,
  manages,
  onRename,
  onArchive,
  onRestore,
  onDelete,
}: {
  project: Project
  manages: boolean
  onRename: (name: string) => void
  onArchive: () => void
  onRestore: () => void
  onDelete: () => void
}) {
  const [name, setName] = useState(p.name)
  useEffect(() => setName(p.name), [p.name])
  const rename = () => {
    const n = name.trim()
    if (n && n !== p.name) onRename(n)
    else setName(p.name)
  }
  return (
    <li className={p.archived ? 'archived' : undefined}>
      <div className="flow-card">
        <span className="flow-card-name">
          {manages ? (
            <input
              className="team-name"
              value={name}
              aria-label={`Rename ${p.name}`}
              onChange={(e) => setName(e.target.value)}
              onBlur={rename}
              onKeyDown={(e) => {
                if (e.key === 'Enter') rename()
                if (e.key === 'Escape') setName(p.name)
              }}
            />
          ) : (
            p.name
          )}
          {p.archived && <span className="project-tag">Archived</span>}
        </span>
        <span className="flow-card-run">
          {LEVEL_WORDS[p.level].label} · {OTHERS_WORDS[p.othersAccess]}
          {p.inheritsInstance && ' · gets what the instance is configured with outside the app'}
        </span>
      </div>
      <span className="trash-actions">
        <a className="project-open" href={pageHash('#/', p.id)}>
          Open
        </a>
        {p.level === 'owner' && (
          <TabLink hash={pageHash('#/settings/access', p.id)} title="Who enters the project and what they may do, in its settings, in a new tab">
            Access
          </TabLink>
        )}
        {manages &&
          (p.archived ? (
            <>
              <button type="button" onClick={onRestore}>
                Bring back
              </button>
              <button
                type="button"
                className="danger"
                disabled={p.id === LEGACY}
                title={p.id === LEGACY ? 'Legacy holds what there was before projects, and everyone joins it, so it is never deleted' : undefined}
                onClick={onDelete}
              >
                Delete for good
              </button>
            </>
          ) : (
            <button type="button" title="Only an admin enters it then; its runs start no more until it comes back" onClick={onArchive}>
              Archive
            </button>
          ))}
      </span>
    </li>
  )
}

// What shows in place of a page someone may not enter: that they are in no
// project yet, or may not enter the one the address names, as it was
// archived or they were taken out of it; whom to ask, the instance's admins
// by name for someone in no project; and the projects they may enter
// instead.
export function NoProject({ me, project }: { me: Me; project: number | null }) {
  useEffect(() => {
    document.title = 'No project · Spoletta'
  }, [])
  const none = me.projects.length === 0
  // The instance's admins by name, which the server gives only someone who
  // may enter no project.
  const admins = me.admins ?? []
  const askAdmin =
    admins.length > 0
      ? `Ask ${admins.length === 1 ? admins[0] : `${admins.slice(0, -1).join(', ')} or ${admins[admins.length - 1]}`}, ${admins.length === 1 ? 'the admin' : 'the admins'} of Spoletta,`
      : 'Ask an admin of Spoletta'
  return (
    <div className="flows-page">
      <header className="toolbar">
        <h1>{none ? 'No project yet' : <ProjectSwitcher />}</h1>
      </header>
      <main className="flows">
        <section className="flow-section no-project" aria-label="No project">
          <h2>{none ? 'You aren’t in any project yet' : 'You can’t enter this project'}</h2>
          <p className="flows-note">
            {none
              ? `Everything here is kept in projects, and you may enter none. ${askAdmin} to give you one, or an owner of the project you work in to let you in; then reload this page.`
              : `Project ${project} may have been archived, or you were taken out of it, or it was never yours. Ask one of its owners, or an admin of Spoletta, to let you in.`}
          </p>
          {!none && (
            <>
              <h3>Your projects</h3>
              <ul className="flow-list">
                {me.projects.map((p) => (
                  <li key={p.id}>
                    <a className="flow-card" href={pageHash('#/', p.id)}>
                      <span className="flow-card-name">{p.name}</span>
                      <span className="flow-card-run">{LEVEL_WORDS[p.level].you}</span>
                    </a>
                  </li>
                ))}
              </ul>
            </>
          )}
          {managesProjects(me) && (
            <p className="flows-note">
              <a href={pageHash('#/projects')}>Every project</a>, to make one or bring one back.
            </p>
          )}
        </section>
      </main>
    </div>
  )
}
