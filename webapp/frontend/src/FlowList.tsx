import { Fragment, useCallback, useEffect, useRef, useState, type ChangeEvent, type FormEvent, type KeyboardEvent, type ReactNode } from 'react'
import {
  createFlow,
  createFromTemplate,
  createTaskBoard,
  deleteTaskBoard,
  exportSetup,
  importSetup,
  listFlows,
  listTaskBoards,
  listTeams,
  restoreFlow,
  setFavourite,
  setFlowOwner,
  setFlowTags,
  setTaskBoardOwner,
  trashFlow,
  type Flow,
  type FlowTemplate,
  type Me,
  type TaskBoard,
  type Team,
} from './api'
import {
  ActivityIcon,
  ChevronDownIcon,
  ClockIcon,
  CopyIcon,
  DownloadIcon,
  FlowBuilderIcon,
  FlowIcon,
  GridIcon,
  MoreIcon,
  ObjectIcon,
  PlusIcon,
  SearchIcon,
  StarIcon,
  SubflowIcon,
  TagIcon,
  TaskBoardIcon,
  TrashIcon,
  UploadIcon,
} from './icons'
import { formatTime, RUN_LABELS } from './Runs'
import { Account, configures, useMe } from './Account'
import { OwnerPicker, ownerLabel } from './Owner'
import { TemplateDialog, TemplateGallery } from './Templates'
import { useConfirm } from './Confirm'
import { useUndoNotice } from './UndoNotice'
import { ToolbarMenu, type ToolbarItem, type ToolbarSection } from './Toolbar'
import { Keys, useShortcuts } from './Keyboard'
import { openTab } from './tabs'
import { PROCEDURE_WORDS, STRUCT_WORDS } from './words'
import {
  EVERYTHING,
  failedPercent,
  KINDS,
  kindOf,
  listHash,
  marked,
  matches,
  needs,
  NEEDS,
  readList,
  sameTag,
  sortBoards,
  sortFlows,
  strip,
  timeAgo,
  type Kind,
  type ListState,
  type Need,
  type View,
} from './home'

// The main page: everything one sees, a block of each kind: the flows that
// run, each a job of its own, such as fixing exceptions or bumping
// dependencies; the task boards, laying out shared structs' records in
// columns (TaskBoard.tsx); and what flows reuse, the procedures and the
// structs. The rail at its side shows everything, the starred or one kind,
// and narrows to a tag or to whose they are; the search above finds by name
// and tag across them all, Enter opening the first found; and chips say how
// many flows need a look, with steps waiting on the person, failing or with
// runs going on, each narrowing the list to them. What it shows is kept in
// the address (home.ts). Each flow shows its latest runs as a strip, a tick
// each, failures standing tallest, and when its last run started; the list
// asks again every so often, so they move on. With sign-in, only those one
// sees are listed: one's own, one's teams', everyone's, and those whose
// reviews one decides. Favourites come first in each block. One New menu
// makes each kind, or a flow from a template, which are offered in place
// while there is no flow at all. Deleting a flow moves it to the trash,
// which can be undone, so it asks nothing first unless runs going on in it
// would stop; deleting a task board can't be undone, so it asks.
export default function FlowList({ onOpen }: { onOpen: (id: number) => void }) {
  const me = useMe()
  const [all, setFlows] = useState<Flow[] | null>(null)
  const [allBoards, setBoards] = useState<TaskBoard[] | null>(null)
  const [list, setList] = useState<ListState>(() => readList(window.location.hash))
  const [teams, setTeams] = useState<Team[]>([])
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [templating, setTemplating] = useState(false)
  const [making, setMaking] = useState<{ kind: Kind; name: string } | null>(null)
  const [now, setNow] = useState(() => Date.now())
  const picker = useRef<HTMLInputElement>(null)
  const search = useRef<HTMLInputElement>(null)
  const newButton = useRef<HTMLButtonElement>(null)
  const [confirm, confirmDialog] = useConfirm()
  const [showUndo, , undoNotice] = useUndoNotice()
  const show = (patch: Partial<ListState>) => setList((s) => ({ ...s, ...patch }))

  // What the list shows goes in the address, in place, keeping the tab's entry.
  useEffect(() => {
    const hash = listHash(list)
    const kept = hash === '#/' && window.location.hash === '' ? '' : hash
    if (window.location.hash !== kept) window.history.replaceState(window.history.state, '', window.location.pathname + window.location.search + kept)
  }, [list])

  const report = useCallback((err: unknown) => setError(err instanceof Error ? err.message : String(err)), [])
  const refresh = useCallback(() => listFlows().then(setFlows).catch(report), [report])
  const refreshBoards = useCallback(() => listTaskBoards().then(setBoards).catch(report), [report])

  useEffect(() => {
    document.title = 'Home · Spoletta'
    refresh()
    refreshBoards()
  }, [refresh, refreshBoards])

  // Runs go on by themselves, so the list asks again every so often while
  // it shows, and as one comes back to it, saying nothing if that fails.
  useEffect(() => {
    const again = () => {
      if (document.visibilityState !== 'visible') return
      setNow(Date.now())
      listFlows().then(setFlows, () => {})
    }
    const t = window.setInterval(again, LIST_REFRESH_MS)
    document.addEventListener('visibilitychange', again)
    return () => {
      window.clearInterval(t)
      document.removeEventListener('visibilitychange', again)
    }
  }, [])

  useEffect(() => {
    if (me?.user) listTeams().then(setTeams, report)
  }, [me, report])

  useShortcuts({
    search: () => {
      search.current?.focus()
      search.current?.select()
    },
    newItem: () => newButton.current?.click(),
  })

  const onOwner = (f: Flow, owner: Parameters<typeof setFlowOwner>[1]) => setFlowOwner(f.id, owner).then(refresh, report)

  // What changes of a flow on the list, at once, and back if the server
  // turns it down.
  const patch = (f: Flow, p: Partial<Flow>) => setFlows((fs) => fs?.map((x) => (x.id === f.id ? { ...x, ...p } : x)) ?? null)

  const onStar = (f: Flow) => {
    patch(f, { favourite: !f.favourite })
    setFavourite(f.id, !f.favourite).catch((err) => {
      patch(f, { favourite: f.favourite })
      report(err)
    })
  }

  const onTags = async (f: Flow, tags: string[]) => {
    try {
      patch(f, { tags: await setFlowTags(f.id, tags) })
    } catch (err) {
      report(err)
    }
  }

  // Picking a tag shows only what has it; picking it again, everything.
  const onTag = (t: string) => show({ tag: sameTag(list.tag, t) ? '' : t })

  // Who new ones belong to: the team picked last, else the first of one's
  // teams, else oneself.
  const myTeams = teams.filter((t) => t.members.some((m) => m.id === me?.user?.id))
  const [picked, setPicked] = useState(() => {
    try {
      return localStorage.getItem(NEW_OWNER_KEY) ?? ''
    } catch {
      return ''
    }
  })
  const newOwnerKey =
    picked === 'me' || picked === 'global' || myTeams.some((t) => `team:${t.id}` === picked)
      ? picked
      : myTeams.length > 0
        ? `team:${myTeams[0].id}`
        : 'me'
  const pickNewOwner = (key: string) => {
    setPicked(key)
    try {
      localStorage.setItem(NEW_OWNER_KEY, key)
    } catch {
      // Only remembered while it can be.
    }
  }
  const newOwner = (): Parameters<typeof createFlow>[2] => {
    if (!me?.user) return undefined
    if (newOwnerKey === 'global') return { kind: 'global' }
    if (newOwnerKey === 'me') return { kind: 'user', id: me.user.id }
    return { kind: 'team', id: Number(newOwnerKey.slice('team:'.length)) }
  }
  const newOwners: [string, string][] = [...myTeams.map((t): [string, string] => [`team:${t.id}`, `Team ${t.name}`]), ['me', 'Only me'], ['global', 'Everyone']]

  // A new flow, procedure or struct opens on its board, a new task board
  // to add its columns.
  const onMake = async (kind: Kind, name: string) => {
    if (kind === 'board') {
      const b = await createTaskBoard(name, newOwner())
      window.location.hash = `#/taskboards/${b.id}`
      return
    }
    const f = await createFlow(name, kind, newOwner())
    onOpen(f.id)
  }

  // A new flow made of a template, belonging to whoever new ones do.
  const onTemplate = async (t: FlowTemplate) => {
    const f = await createFromTemplate(t.id, undefined, newOwner())
    onOpen(f.id)
  }

  const onDelete = async (f: Flow) => {
    setError(null)
    try {
      const { running } = await trashFlow(f.id)
      if (running > 0) {
        const stop = await confirm({
          title: `Move ${f.name} to the trash?`,
          message: (
            <p>
              {running === 1 ? 'A run is' : `${running} runs are`} going on in it, which moving it to the trash stops; that
              can't be undone. The {KIND_INFO[kindOf(f)].one} itself can be restored from the trash.
            </p>
          ),
          confirm: running === 1 ? 'Stop it and move' : 'Stop them and move',
        })
        if (!stop) return
        await trashFlow(f.id, true)
      }
      await refresh()
      showUndo({ message: trashedMessage(f), undo: () => restoreFlow(f.id).then(refresh, report) })
    } catch (err) {
      report(err)
    }
  }

  const onDeleteBoard = async (b: TaskBoard) => {
    const yes = await confirm({
      title: `Delete ${b.name}?`,
      message: <p>Its columns go with it; the records they hold stay as they are. This can't be undone.</p>,
      confirm: 'Delete',
    })
    if (!yes) return
    setError(null)
    deleteTaskBoard(b.id).then(refreshBoards, report)
  }

  const onBoardOwner = (b: TaskBoard, owner: Parameters<typeof setTaskBoardOwner>[1]) =>
    setTaskBoardOwner(b.id, owner).then(refreshBoards, report)

  // Saves the setup as a file.
  const onExport = async () => {
    setError(null)
    try {
      const url = URL.createObjectURL(await exportSetup())
      const a = document.createElement('a')
      a.href = url
      a.download = 'spoletta-setup.yaml'
      a.click()
      URL.revokeObjectURL(url)
    } catch (err) {
      report(err)
    }
  }

  // Reads the setup file picked and imports it.
  const onImport = async (e: ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file) return
    setError(null)
    setNotice(null)
    try {
      const flows = await importSetup(await file.text())
      const created = flows.filter((f) => f.created).length
      setNotice(`Imported ${file.name}: ${created} created, ${flows.length - created} updated.`)
      await refresh()
    } catch (err) {
      report(err)
    }
  }

  // What the view, the search, the tag and whose narrow the list to; what
  // needs a look narrows it further, and is counted within it. Task boards
  // have no tags or stars, and need no look.
  const signedIn = !!me?.user
  const owned = (o: Owned) => !list.whose || whoseKey(o) === list.whose
  const inView = (kind: Kind, starred: boolean) => list.view === 'all' || (list.view === 'starred' ? starred : list.view === kind)
  const narrowedFlows =
    all?.filter(
      (f) =>
        inView(kindOf(f), f.favourite) &&
        owned(f) &&
        (!list.tag || f.tags.some((t) => sameTag(t, list.tag))) &&
        matches(f.name, f.tags, list.q),
    ) ?? []
  const flows = sortFlows(list.need ? narrowedFlows.filter(needs[list.need]) : narrowedFlows, list.sort)
  const boards = sortBoards(
    allBoards?.filter((b) => inView('board', false) && owned(b) && !list.tag && !list.need && matches(b.name, [], list.q)) ?? [],
    list.sort,
  )
  const ofKind = (k: Kind) => flows.filter((f) => kindOf(f) === k)
  const shown = KINDS.filter((k) => (k === 'board' ? boards.length : ofKind(k).length) > 0)
  const count = flows.length + boards.length
  const narrowed = !!(list.q.trim() || list.tag || list.whose || list.need)
  const loaded = all !== null && allBoards !== null
  // Without a flow at all, the templates are offered in place of the flows.
  const templates = all !== null && all.length === 0 && !narrowed && (list.view === 'all' || list.view === 'flow')

  // Enter in the search opens the first found.
  const first = shown[0] === 'board' ? `#/taskboards/${boards[0].id}` : shown[0] ? `#/flows/${ofKind(shown[0])[0].id}` : null
  const onSearchKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter' && first) {
      e.preventDefault()
      window.location.hash = first
    } else if (e.key === 'ArrowDown') {
      const link = document.querySelector<HTMLElement>('.home-main .home-link')
      if (link) {
        e.preventDefault()
        link.focus()
      }
    }
  }

  // The arrow keys go from one name on the list to the next, and up from
  // the first back to the search.
  const onListKey = (e: KeyboardEvent<HTMLElement>) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
    const links = [...e.currentTarget.querySelectorAll<HTMLElement>('.home-link')]
    const at = links.indexOf(e.target as HTMLElement)
    if (at < 0) return
    e.preventDefault()
    const next = at + (e.key === 'ArrowDown' ? 1 : -1)
    if (next < 0) search.current?.focus()
    else links[Math.min(next, links.length - 1)].focus()
  }

  const tags = all ? tagCounts(all) : []
  const owners = me?.user && all && allBoards ? ownersOf([...all, ...allBoards], me) : []
  const chips = NEEDS.map((n) => ({ need: n, count: narrowedFlows.filter(needs[n]).length })).filter(
    (c) => c.count > 0 || list.need === c.need,
  )

  const newMenu: ToolbarSection[] = [
    {
      items: [
        makeItem('flow', () => setMaking({ kind: 'flow', name: '' })),
        {
          id: 'template',
          label: 'Flow from a template',
          hint: 'A ready flow, made a flow of your own to change as you like',
          icon: <span className="kind-icon flow"><CopyIcon /></span>,
          onSelect: () => setTemplating(true),
        },
        makeItem('board', () => setMaking({ kind: 'board', name: '' })),
      ],
    },
    {
      title: 'Reused in flows',
      items: [makeItem('module', () => setMaking({ kind: 'module', name: '' })), makeItem('struct', () => setMaking({ kind: 'struct', name: '' }))],
    },
  ]

  return (
    <div className="flows-page home">
      <header className="toolbar home-bar">
        <h1>
          <FlowBuilderIcon /> Home
        </h1>
        <label className="home-search">
          <SearchIcon />
          <input
            ref={search}
            type="search"
            aria-label="Find flows, task boards, procedures and structs by name or tag"
            placeholder="Find a flow, task board, procedure or struct by name or tag"
            value={list.q}
            onChange={(e) => show({ q: e.target.value })}
            onKeyDown={onSearchKey}
          />
          {!list.q && <Keys id="search" />}
        </label>
        <div className="home-actions">
          <Account />
          {configures(me) && (
            <>
              <ToolbarMenu
                className="home-more"
                label={<MoreIcon />}
                title="Import or export the setup"
                sections={[
                  {
                    items: [
                      {
                        id: 'import',
                        label: 'Import a setup file',
                        hint: 'Create or update what a YAML file holds',
                        icon: <UploadIcon />,
                        onSelect: () => picker.current?.click(),
                      },
                      {
                        id: 'export',
                        label: 'Export the setup',
                        hint: 'Every flow, procedure, struct, MCP server, skill and command, as one YAML file',
                        icon: <DownloadIcon />,
                        onSelect: () => void onExport(),
                      },
                    ],
                  },
                ]}
              />
              <input ref={picker} type="file" accept=".yaml,.yml,.json" hidden onChange={onImport} />
            </>
          )}
          <ToolbarMenu
            className="home-new"
            buttonRef={newButton}
            label={
              <>
                <PlusIcon size={14} /> New <ChevronDownIcon />
              </>
            }
            title="Make a flow, task board, procedure or struct"
            sections={newMenu}
          />
        </div>
      </header>
      {error && (
        <div className="error-banner" role="alert" onClick={() => setError(null)}>
          {error} (click to dismiss)
        </div>
      )}
      {notice && (
        <div className="notice-banner" role="status" onClick={() => setNotice(null)}>
          {notice} (click to dismiss)
        </div>
      )}
      <div className="home-layout">
        <HomeRail flows={all} boards={allBoards} me={me} here={list} onShow={show} />
        <main className="home-main" onKeyDown={onListKey}>
          <div className="home-head">
            <h2>
              {VIEW_TITLES[list.view]}
              {loaded && <span className="home-count">{count}</span>}
            </h2>
            <div className="home-chips">
              {chips.map(({ need, count }) => (
                <button
                  key={need}
                  type="button"
                  className={`home-chip ${need}${list.need === need ? ' on' : ''}`}
                  aria-pressed={list.need === need}
                  title={NEED_WORDS[need].title}
                  onClick={() => show({ need: list.need === need ? '' : need })}
                >
                  <span className="home-chip-dot" aria-hidden />
                  {NEED_WORDS[need].label(count)}
                </button>
              ))}
              {list.tag && (
                <button type="button" className="home-token" title="Show what has any tag" onClick={() => show({ tag: '' })}>
                  Tagged {list.tag} <span aria-hidden>×</span>
                </button>
              )}
              {list.whose && (
                <button type="button" className="home-token" title="Show whoever it belongs to" onClick={() => show({ whose: '' })}>
                  Belongs to {owners.find((o) => o.key === list.whose)?.label ?? 'someone'} <span aria-hidden>×</span>
                </button>
              )}
            </div>
            <label className="home-sort">
              Sort
              <select value={list.sort} onChange={(e) => show({ sort: e.target.value as ListState['sort'] })}>
                <option value="name">By name</option>
                <option value="recent">Latest run first</option>
                <option value="failing">Most failing first</option>
              </select>
            </label>
          </div>
          {templates && (
            <section className="home-templates" aria-label="Start from a template">
              <h3>Start from a template</h3>
              <p className="flows-note">
                Each is a ready flow, made a flow of your own to change as you like; or make an empty one with New.
              </p>
              <TemplateGallery admin={configures(me)} signIn={!!me?.signIn} onUse={onTemplate} onError={report} />
            </section>
          )}
          {!loaded ? (
            <p className="flows-note">Loading…</p>
          ) : (
            shown.map((k) => (
              <KindBlock key={k} kind={k} count={k === 'board' ? boards.length : ofKind(k).length} owned={signedIn} onNew={() => setMaking({ kind: k, name: '' })}>
                {k === 'board'
                  ? boards.map((b) => (
                      <BoardRow key={b.id} b={b} query={list.q} me={me} teams={teams} now={now} onOwner={onBoardOwner} onDelete={onDeleteBoard} />
                    ))
                  : ofKind(k).map((f) => (
                      <FlowRow
                        key={f.id}
                        f={f}
                        kind={k}
                        query={list.q}
                        me={me}
                        teams={teams}
                        now={now}
                        known={tags.map((t) => t.tag)}
                        onStar={onStar}
                        onTags={onTags}
                        onTag={onTag}
                        onOwner={onOwner}
                        onDelete={onDelete}
                      />
                    ))}
              </KindBlock>
            ))
          )}
          {loaded && shown.length === 0 && !templates && (
            <Nothing
              list={list}
              narrowed={narrowed}
              onMake={(kind, name) => setMaking({ kind, name })}
              onClear={() => show({ q: '', tag: '', whose: '', need: '' })}
            />
          )}
        </main>
      </div>
      {making && (
        <NewDialog
          kind={making.kind}
          name={making.name}
          owners={me?.user ? newOwners : null}
          owner={newOwnerKey}
          onOwner={pickNewOwner}
          onMake={(name) => onMake(making.kind, name)}
          onClose={() => setMaking(null)}
        />
      )}
      {templating && (
        <TemplateDialog admin={configures(me)} signIn={!!me?.signIn} onUse={onTemplate} onClose={() => setTemplating(false)} onError={report} />
      )}
      {confirmDialog}
      {undoNotice}
    </div>
  )
}

// The pages the rail links to besides the list.
type RailPage = 'activity' | 'schedules' | 'trash'

// What else the rail stays beside: those pages, and a task board, shown as
// one of the task boards.
type Beside = RailPage | 'taskboard'

// The home page's rail: everything, the starred or one kind, each tag and
// whose they are, and the pages about all the flows. It stays beside those
// pages and task boards too (HomeFrame), so going between them keeps one's
// place. On the list it narrows what the list shows; from elsewhere, it
// goes back to the list showing that.
function HomeRail({
  flows,
  boards,
  me,
  here,
  onShow,
}: {
  flows: Flow[] | null
  boards: TaskBoard[] | null
  me: Me | null
  here: ListState | Beside
  onShow: (patch: Partial<ListState>) => void
}) {
  const list = typeof here === 'string' ? null : here
  const shown: View | null = list ? list.view : here === 'taskboard' ? 'board' : null
  const kindCount = (k: Kind) => (k === 'board' ? (boards?.length ?? 0) : (flows?.filter((f) => kindOf(f) === k).length ?? 0))
  const tags = flows ? tagCounts(flows) : []
  const owners = me?.user && flows && boards ? ownersOf([...flows, ...boards], me) : []

  const item = (view: View, icon: ReactNode, label: string, n: number, title?: string) => (
    <button
      type="button"
      className={shown === view ? 'home-rail-item on' : 'home-rail-item'}
      aria-current={shown === view || undefined}
      title={title}
      onClick={() => onShow({ view })}
    >
      {icon}
      <span className="home-rail-label">{label}</span>
      <span className="home-rail-count">{n}</span>
    </button>
  )
  const page = (to: RailPage, icon: ReactNode, label: string, title?: string) => (
    <a
      className={here === to ? 'home-rail-item on' : 'home-rail-item'}
      aria-current={here === to ? 'page' : undefined}
      href={`#/${to}`}
      title={title}
    >
      {icon}
      <span className="home-rail-label">{label}</span>
    </a>
  )

  return (
    <nav className="home-rail" aria-label="What to show">
      {item('all', <GridIcon />, 'Everything', kindCount('flow') + kindCount('board') + kindCount('module') + kindCount('struct'))}
      {item(
        'starred',
        <span className="kind-icon starred">
          <StarIcon filled />
        </span>,
        'Starred',
        flows?.filter((f) => f.favourite).length ?? 0,
        'Your favourites, which come first in every list',
      )}
      <hr />
      {item('flow', KIND_INFO.flow.icon, 'Flows', kindCount('flow'), KIND_INFO.flow.note)}
      {item('board', KIND_INFO.board.icon, 'Task boards', kindCount('board'), KIND_INFO.board.note)}
      <h2>Reused in flows</h2>
      {item('module', KIND_INFO.module.icon, 'Procedures', kindCount('module'), `${PROCEDURE_WORDS.many}: ${KIND_INFO.module.note}`)}
      {item('struct', KIND_INFO.struct.icon, 'Structs', kindCount('struct'), `${STRUCT_WORDS.many}: ${KIND_INFO.struct.note}`)}
      {tags.length > 0 && (
        <>
          <h2>Tags</h2>
          {tags.map(({ tag, n }) => {
            const on = !!list && sameTag(list.tag, tag)
            return (
              <button
                key={tag}
                type="button"
                className={on ? 'home-rail-item on' : 'home-rail-item'}
                aria-pressed={on}
                onClick={() => onShow({ tag: on ? '' : tag })}
              >
                <TagIcon />
                <span className="home-rail-label">{tag}</span>
                <span className="home-rail-count">{n}</span>
              </button>
            )
          })}
        </>
      )}
      {owners.length > 1 && (
        <>
          <h2>Belongs to</h2>
          {owners.map((o) => {
            const on = list?.whose === o.key
            return (
              <button
                key={o.key}
                type="button"
                className={on ? 'home-rail-item on' : 'home-rail-item'}
                aria-pressed={on}
                onClick={() => onShow({ whose: on ? '' : o.key })}
              >
                <span className="home-rail-label">{o.label}</span>
                <span className="home-rail-count">{o.n}</span>
              </button>
            )
          })}
        </>
      )}
      <hr />
      {page('activity', <ActivityIcon />, 'Activity', 'What runs and fails across the flows, what they spend, and how reviews go')}
      {page('schedules', <ClockIcon />, 'Schedules')}
      {page('trash', <TrashIcon />, 'Trash', 'What was deleted, to restore or delete for good')}
    </nav>
  )
}

// A page the rail links to, or a task board, shown beside it. It reads the
// flows and task boards for the rail's counts, again whenever the page says
// it changed them (`changed`, such as the trash restoring one); without them
// the rail counts nothing, the page saying what went wrong for its own part.
export function HomeFrame({ at, changed = 0, children }: { at: Beside; changed?: number; children: ReactNode }) {
  const me = useMe()
  const [flows, setFlows] = useState<Flow[] | null>(null)
  const [boards, setBoards] = useState<TaskBoard[] | null>(null)

  useEffect(() => {
    listFlows().then(setFlows, () => {})
    listTaskBoards().then(setBoards, () => {})
  }, [changed])

  return (
    <div className="home-layout">
      <HomeRail
        flows={flows}
        boards={boards}
        me={me}
        here={at}
        onShow={(patch) => (window.location.hash = listHash({ ...EVERYTHING, ...patch }))}
      />
      <div className="home-page">{children}</div>
    </div>
  )
}

// How often the list asks again how the flows' runs go.
const LIST_REFRESH_MS = 30_000

// Where who new ones belong to is remembered.
const NEW_OWNER_KEY = 'spoletta.newOwner'

// Each kind: what it is called, what it is, as the New menu and a block's
// heading say it, an example name, and its icon; and what its block's
// columns hold.
const KIND_INFO: Record<Kind, { title: string; one: string; gloss?: string; note: string; hint: string; example: string; icon: ReactNode; columns: [string, string] }> = {
  flow: {
    title: 'Flows',
    one: 'flow',
    note: 'The jobs that run, each doing one thing end to end.',
    hint: 'A job that runs end to end, such as fixing exceptions',
    example: 'Fix exceptions',
    icon: (
      <span className="kind-icon flow">
        <FlowIcon />
      </span>
    ),
    columns: ['Latest runs', 'Last run'],
  },
  board: {
    title: 'Task boards',
    one: 'task board',
    note: "Shared structs' records in columns, each a query of them, such as tickets by state, moving on as flows and people move them; played, the flow taking each record's state moves it on.",
    hint: 'Shared records in columns, such as tickets by state',
    example: 'Sprint',
    icon: (
      <span className="kind-icon taskboard">
        <TaskBoardIcon />
      </span>
    ),
    columns: ['Columns', 'Changed'],
  },
  module: {
    title: 'Procedures',
    one: 'procedure',
    gloss: PROCEDURE_WORDS.gloss,
    note: "Parts reused inside flows, dragged in from a board's sidebar. Changing one changes it everywhere it runs, and the objects it leaves follow it.",
    hint: 'A part other flows reuse, such as creating a Jira ticket',
    example: 'Create a Jira ticket',
    icon: (
      <span className="kind-icon module">
        <SubflowIcon />
      </span>
    ),
    columns: ['Latest runs on its own', 'Last run on its own'],
  },
  struct: {
    title: 'Structs',
    one: 'struct',
    gloss: STRUCT_WORDS.gloss,
    note: "Objects defined once, with their states, attributes and parts, and dragged in from a board's sidebar. Changing one changes every copy of it.",
    hint: 'An object flows reuse, such as a Jira ticket',
    example: 'Jira ticket',
    icon: (
      <span className="kind-icon struct">
        <ObjectIcon />
      </span>
    ),
    columns: ['Used in', ''],
  },
}

// The New menu's item making a kind.
const makeItem = (kind: Kind, onSelect: () => void): ToolbarItem => ({
  id: kind,
  label: KIND_INFO[kind].one[0].toUpperCase() + KIND_INFO[kind].one.slice(1),
  hint: KIND_INFO[kind].hint,
  icon: KIND_INFO[kind].icon,
  onSelect,
})

const VIEW_TITLES: Record<View, string> = {
  all: 'Everything',
  starred: 'Starred',
  flow: 'Flows',
  board: 'Task boards',
  module: 'Procedures',
  struct: 'Structs',
}

// What each chip says of the flows needing a look, and what they are.
const NEED_WORDS: Record<Need, { label: (n: number) => string; title: string }> = {
  waiting: {
    label: (n) => `${n} waiting on you`,
    title: 'Flows with reviews, inputs or loop questions in their runs that you may decide, as your Inbox lists them',
  },
  failing: { label: (n) => `${n} failing`, title: 'Flows whose last run failed, or some of whose runs this week did' },
  running: { label: (n) => `${n} running`, title: 'Flows with runs going on now, those waiting for a person included' },
}

// What a run's tick says of it.
const LOOK_WORDS = { done: 'Done', failed: 'Failed', running: 'Running', waiting: 'Waiting for a person', stopped: 'Stopped' }

// What is owned as a flow is, and listed here: a flow, or a task board.
type Owned = Pick<Flow, 'access' | 'owner'>

// Whose something listed is, to narrow the list to: an owner's key, or
// "review" for the flows one only decides the reviews of.
const whoseKey = (f: Owned) =>
  f.access === 'review' ? 'review' : f.owner.kind === 'global' ? 'global' : `${f.owner.kind}:${f.owner.id}`

// The owners of what is listed, each with how many of it are theirs:
// oneself first, then teams and people by name, everyone, and last the
// flows one only reviews.
function ownersOf(items: Owned[], me: Me): { key: string; label: string; n: number }[] {
  const mine = `user:${me.user?.id}`
  const found = new Map<string, { key: string; label: string; n: number }>()
  for (const f of items) {
    const key = whoseKey(f)
    const label =
      key === mine
        ? 'Me'
        : key === 'review'
          ? 'Those I review'
          : key === 'global'
            ? 'Everyone'
            : f.owner.kind === 'team'
              ? `Team ${ownerLabel(f.owner, me)}`
              : ownerLabel(f.owner, me)
    const o = found.get(key) ?? { key, label, n: 0 }
    o.n++
    found.set(key, o)
  }
  const rank = (k: string) => (k === mine ? 0 : k === 'global' ? 2 : k === 'review' ? 3 : 1)
  return [...found.values()].sort((a, b) => rank(a.key) - rank(b.key) || a.label.localeCompare(b.label))
}

// Every tag of the flows, each once whatever its case, spelled as it is
// first, by name, with how many flows have it.
function tagCounts(flows: Flow[]): { tag: string; n: number }[] {
  const tags = new Map<string, { tag: string; n: number }>()
  for (const f of flows) {
    for (const t of f.tags) {
      const c = tags.get(t.toLowerCase()) ?? { tag: t, n: 0 }
      c.n++
      tags.set(t.toLowerCase(), c)
    }
  }
  return [...tags.values()].sort((a, b) => a.tag.localeCompare(b.tag, undefined, { sensitivity: 'base' }))
}

const flowsUsing = (n: number) => `${n} ${n === 1 ? 'flow' : 'flows'}`

// What moving it to the trash did, as its notice says, with what that does
// to the flows using it.
function trashedMessage(f: Flow): string {
  const moved = `Moved ${f.name} to the trash`
  const n = f.usedBy
  if (n === 0) return moved
  return f.struct
    ? `${moved}; ${n === 1 ? 'the flow using it keeps its copy' : `the ${n} flows using it keep their copies`} as their own until it comes back`
    : `${moved}; ${n === 1 ? 'the flow running it fails' : `the ${n} flows running it fail`} to run it until it comes back`
}

// A name with what was searched for marked in it.
function Name({ name, query }: { name: string; query: string }) {
  return (
    <>
      {marked(name, query).map((p, i) => (p.hit ? <mark key={i}>{p.text}</mark> : <Fragment key={i}>{p.text}</Fragment>))}
    </>
  )
}

// A kind's block: its heading, saying what the kind is as it is pointed
// at, with a button making another, the names of its columns, and its rows.
function KindBlock({ kind, count, owned, onNew, children }: { kind: Kind; count: number; owned: boolean; onNew: () => void; children: ReactNode }) {
  const info = KIND_INFO[kind]
  return (
    <section className="home-block" aria-label={info.title}>
      <header className="home-block-head">
        <h3 title={info.note}>
          {info.icon}
          {info.title}
          {info.gloss && <span className="flow-section-gloss">({info.gloss})</span>}
        </h3>
        <span className="home-count">{count}</span>
        <button type="button" className="home-add" onClick={onNew}>
          <PlusIcon /> New {info.one}
        </button>
      </header>
      <div className={owned ? 'home-columns owned' : 'home-columns'} aria-hidden>
        <span />
        <span>Name</span>
        <span>{info.columns[0]}</span>
        <span>{info.columns[1]}</span>
        {owned && <span>Belongs to</span>}
        <span />
      </div>
      <ul className={owned ? 'home-rows owned' : 'home-rows'}>{children}</ul>
    </section>
  )
}

// A flow, procedure or struct on the list: its star, its name with its
// tags, how its latest runs went or for a struct how many flows use it,
// when its last run started, whose it is, and a menu of the rest.
function FlowRow({
  f,
  kind,
  query,
  me,
  teams,
  now,
  known,
  onStar,
  onTags,
  onTag,
  onOwner,
  onDelete,
}: {
  f: Flow
  kind: Kind
  query: string
  me: Me | null
  teams: Team[]
  now: number
  // Every tag there is, to offer when adding one.
  known: string[]
  onStar: (f: Flow) => void
  onTags: (f: Flow, tags: string[]) => Promise<void>
  onTag: (tag: string) => void
  onOwner: (f: Flow, owner: Parameters<typeof setFlowOwner>[1]) => void
  onDelete: (f: Flow) => void
}) {
  const [tagging, setTagging] = useState(false)
  const hash = `#/flows/${f.id}`
  const editable = f.access === 'edit'
  const items: ToolbarItem[] = [
    { id: 'tab', label: 'Open in a new tab', onSelect: () => openTab(hash) },
    { id: 'star', label: f.favourite ? 'Take the star away' : 'Star it', hint: f.favourite ? undefined : 'Listed first, and under Starred', onSelect: () => onStar(f) },
  ]
  if (editable) items.push({ id: 'tag', label: 'Add a tag', hint: 'To find it by, and narrow the list to', onSelect: () => setTagging(true) })
  return (
    <li className="home-row">
      <button
        type="button"
        className={f.favourite ? 'home-star starred' : 'home-star'}
        onClick={() => onStar(f)}
        aria-pressed={f.favourite}
        aria-label={f.favourite ? `Take ${f.name} out of your favourites` : `Star ${f.name} as a favourite`}
        title={f.favourite ? 'A favourite of yours, listed first' : 'Star it as a favourite, to list it first'}
      >
        <StarIcon filled={f.favourite} />
      </button>
      <div className="home-name">
        <a className="home-link" href={hash}>
          <Name name={f.name} query={query} />
        </a>
        {(f.health?.waiting ?? 0) > 0 && (
          <a className="home-waiting" href="#/inbox" title="Reviews, inputs and loop questions in its runs that you may decide, which the Inbox lists">
            {f.health!.waiting} waiting on you
          </a>
        )}
        {kind === 'module' && <span className="home-quiet">{f.usedBy > 0 ? `used in ${flowsUsing(f.usedBy)}` : 'not used in a flow yet'}</span>}
        <FlowTags f={f} known={known} adding={tagging} onAdding={setTagging} onTags={onTags} onTag={onTag} />
      </div>
      <div className="home-detail">
        {kind === 'struct' ? (
          <span className={f.usedBy > 0 ? undefined : 'home-quiet'}>{f.usedBy > 0 ? flowsUsing(f.usedBy) : 'Not used yet'}</span>
        ) : (
          <RunStrip f={f} />
        )}
      </div>
      <div className="home-when">{kind !== 'struct' && <LastRun f={f} kind={kind} now={now} />}</div>
      {me?.user && (
        <div className="home-owner">
          {f.access === 'review' ? (
            <span className="flow-owner review" title="You decide its reviews, so you see it; only its owner changes it">
              Reviewing · {ownerLabel(f.owner, me)}
            </span>
          ) : (
            <OwnerPicker flow={f} me={me} teams={teams} compact onChange={(o) => onOwner(f, o)} />
          )}
        </div>
      )}
      <ToolbarMenu
        className="home-more"
        label={<MoreIcon />}
        ariaLabel={`More for ${f.name}`}
        title="More"
        sections={[
          { items },
          {
            items: editable
              ? [
                  {
                    id: 'trash',
                    label: 'Move to the trash',
                    hint: f.usedBy > 0 ? (f.struct ? 'The flows using it keep their copies' : 'The flows running it fail to until it comes back') : undefined,
                    icon: <TrashIcon />,
                    danger: true,
                    onSelect: () => onDelete(f),
                  },
                ]
              : [],
          },
        ]}
      />
    </li>
  )
}

// A task board on the list: its name, whether it plays, its columns, when
// it last changed, whose it is, and a menu of the rest; it has no star.
function BoardRow({
  b,
  query,
  me,
  teams,
  now,
  onOwner,
  onDelete,
}: {
  b: TaskBoard
  query: string
  me: Me | null
  teams: Team[]
  now: number
  onOwner: (b: TaskBoard, owner: Parameters<typeof setTaskBoardOwner>[1]) => void
  onDelete: (b: TaskBoard) => void
}) {
  const hash = `#/taskboards/${b.id}`
  return (
    <li className="home-row">
      <span />
      <div className="home-name">
        <a className="home-link" href={hash}>
          <Name name={b.name} query={query} />
        </a>
        {b.playing && (
          <span
            className="home-board-playing"
            title={`It gives each record to the flow taking its state${b.playedBy ? `, its runs ${b.playedBy}'s` : ''}`}
          >
            <span className="task-board-playing-dot" aria-hidden /> Playing
          </span>
        )}
      </div>
      <div className="home-detail home-board-columns">
        {b.columns.length === 0 ? (
          <span className="home-quiet">No columns yet</span>
        ) : (
          b.columns.map((c, i) => (
            <span key={i} className="home-board-column">
              {c.name}
            </span>
          ))
        )}
      </div>
      <div className="home-when">
        <span className="home-quiet" title={formatTime(b.updatedAt)}>
          {timeAgo(b.updatedAt, now)}
        </span>
      </div>
      {me?.user && (
        <div className="home-owner">
          <OwnerPicker flow={b} me={me} teams={teams} compact onChange={(o) => onOwner(b, o)} />
        </div>
      )}
      <ToolbarMenu
        className="home-more"
        label={<MoreIcon />}
        ariaLabel={`More for ${b.name}`}
        title="More"
        sections={[
          { items: [{ id: 'tab', label: 'Open in a new tab', onSelect: () => openTab(hash) }] },
          {
            items: [
              {
                id: 'delete',
                label: 'Delete…',
                hint: 'Its columns go; the records stay',
                icon: <TrashIcon />,
                danger: true,
                onSelect: () => onDelete(b),
              },
            ],
          },
        ]}
      />
    </li>
  )
}

// A flow's latest runs, oldest first, a tick each opening its run: a
// failure stands tallest and red, so it shows by its height as well as its
// colour; the room for runs it hasn't had yet is a line. Then the share of
// its runs this week that failed, red once it is half or its last run
// failed.
function RunStrip({ f }: { f: Flow }) {
  const { empty, runs } = strip(f.health?.latest ?? [])
  const failures = runs.filter((r) => r.look === 'failed').length
  const percent = failedPercent(f)
  const failing = percent >= 50 || f.lastRun?.status === 'failed'
  return (
    <>
      <span
        className="run-strip"
        role="img"
        aria-label={runs.length === 0 ? 'No runs yet' : `Its latest ${runs.length} runs, newest last: ${failures} failed`}
      >
        {Array.from({ length: empty }, (_, i) => (
          <span key={i} className="run-tick none" />
        ))}
        {runs.map(({ run, look }) => (
          <a
            key={run.id}
            className={`run-tick ${look}`}
            href={`#/flows/${f.id}/runs/${run.id}`}
            tabIndex={-1}
            title={`Run ${run.id}: ${LOOK_WORDS[look]}, ${formatTime(run.startedAt)}`}
          />
        ))}
      </span>
      {percent > 0 && (
        <span
          className={failing ? 'home-failed failing' : 'home-failed'}
          title={`Of its ${f.health!.runs} runs in the last 7 days, ${f.health!.failed} failed`}
        >
          {percent}% failed
        </span>
      )}
    </>
  )
}

// How a flow's last run went and how long ago it started, the time in full
// as it is pointed at.
function LastRun({ f, kind, now }: { f: Flow; kind: Kind; now: number }) {
  const r = f.lastRun
  if (!r) {
    return (
      <span className="home-quiet">
        <span className="status-dot never" aria-hidden />
        {kind === 'module' ? 'Never run on its own' : 'Never run'}
      </span>
    )
  }
  return (
    <span title={formatTime(r.startedAt)}>
      <span className={`status-dot ${r.waiting ? 'waiting' : r.status}`} aria-hidden />
      {r.waiting ? 'Waiting' : RUN_LABELS[r.status]} <span className="home-quiet">{timeAgo(r.startedAt, now)}</span>
    </span>
  )
}

// A flow's tags, each narrowing the list to it; those who may change the
// flow take one away, or add another, from its menu.
function FlowTags({
  f,
  known,
  adding,
  onAdding,
  onTags,
  onTag,
}: {
  f: Flow
  known: string[]
  adding: boolean
  onAdding: (adding: boolean) => void
  onTags: (f: Flow, tags: string[]) => Promise<void>
  onTag: (tag: string) => void
}) {
  const [text, setText] = useState('')
  const editable = f.access === 'edit'
  if (f.tags.length === 0 && !adding) return null

  const add = async () => {
    const t = text.trim()
    onAdding(false)
    setText('')
    if (t && !f.tags.some((x) => sameTag(x, t))) await onTags(f, [...f.tags, t])
  }
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      void add()
    } else if (e.key === 'Escape') {
      onAdding(false)
      setText('')
    }
  }

  return (
    <span className="flow-tags">
      {f.tags.map((t) => (
        <span key={t} className="flow-tag">
          <button className="flow-tag-name" onClick={() => onTag(t)} title={`Show only what is tagged ${t}`}>
            {t}
          </button>
          {editable && (
            <button className="flow-tag-remove" onClick={() => void onTags(f, f.tags.filter((x) => x !== t))} aria-label={`Take the tag ${t} away`}>
              ×
            </button>
          )}
        </span>
      ))}
      {adding && (
        <>
          <input
            className="flow-tag-input"
            autoFocus
            list={`flow-tags-${f.id}`}
            aria-label={`A new tag for ${f.name}`}
            placeholder="Tag"
            maxLength={64}
            value={text}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={onKey}
            onBlur={() => void add()}
          />
          <datalist id={`flow-tags-${f.id}`}>
            {known
              .filter((k) => !f.tags.some((t) => sameTag(t, k)))
              .map((k) => (
                <option key={k} value={k} />
              ))}
          </datalist>
        </>
      )}
    </span>
  )
}

// What the list says when it shows nothing: that nothing has the name
// searched for, offering to make something of it; that nothing matches
// what it is narrowed to; that nothing is starred; or that there is none
// of a kind yet, saying what it is.
function Nothing({
  list,
  narrowed,
  onMake,
  onClear,
}: {
  list: ListState
  narrowed: boolean
  onMake: (kind: Kind, name: string) => void
  onClear: () => void
}) {
  const kind: Kind = list.view === 'all' || list.view === 'starred' ? 'flow' : list.view
  const q = list.q.trim()
  if (narrowed) {
    return (
      <div className="home-empty">
        <h3>{q ? `Nothing is called “${q}”` : 'Nothing matches'}</h3>
        <p>{q ? 'The search looks at names and tags.' : 'Nothing listed has the tag, owner or look picked.'}</p>
        <div className="home-empty-actions">
          {q && (
            <button type="button" className="primary" onClick={() => onMake(kind, q)}>
              <PlusIcon /> New {KIND_INFO[kind].one} called “{q}”
            </button>
          )}
          <button type="button" onClick={onClear}>
            Clear the search and filters
          </button>
        </div>
      </div>
    )
  }
  if (list.view === 'starred') {
    return (
      <div className="home-empty">
        <h3>Nothing starred yet</h3>
        <p>Star what you open often, by its name: it shows here, and first in every list.</p>
      </div>
    )
  }
  return (
    <div className="home-empty">
      <h3>No {KIND_INFO[kind].title.toLowerCase()} yet</h3>
      <p>{KIND_INFO[kind].note}</p>
      <div className="home-empty-actions">
        <button type="button" className="primary" onClick={() => onMake(kind, '')}>
          <PlusIcon /> New {KIND_INFO[kind].one}
        </button>
      </div>
    </div>
  )
}

// Naming something new, and saying who it belongs to with sign-in, before
// it is made. What goes wrong is said in it, keeping what was typed.
function NewDialog({
  kind,
  name: given,
  owners,
  owner,
  onOwner,
  onMake,
  onClose,
}: {
  kind: Kind
  name: string
  // Who new ones may belong to, by key, or null without sign-in.
  owners: [string, string][] | null
  owner: string
  onOwner: (key: string) => void
  onMake: (name: string) => Promise<void>
  onClose: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const [name, setName] = useState(given)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const info = KIND_INFO[kind]

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    setBusy(true)
    setError(null)
    try {
      await onMake(name.trim())
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setBusy(false)
    }
  }

  return (
    <dialog ref={ref} className="new-dialog" aria-label={`New ${info.one}`} onClose={onClose} onClick={(e) => e.target === ref.current && onClose()}>
      <form onSubmit={submit}>
        <h2>
          {info.icon}
          New {info.one}
        </h2>
        <p className="flows-note">{info.note}</p>
        <label>
          Name
          <input autoFocus value={name} placeholder={`Such as ${info.example}`} onChange={(e) => setName(e.target.value)} />
        </label>
        {owners && (
          <label title="Who sees it and may change it; a team’s are its members’">
            Belongs to
            <select value={owner} onChange={(e) => onOwner(e.target.value)}>
              {owners.map(([key, label]) => (
                <option key={key} value={key}>
                  {label}
                </option>
              ))}
            </select>
          </label>
        )}
        {error && (
          <p className="new-dialog-error" role="alert">
            {error}
          </p>
        )}
        <div className="confirm-actions">
          <button type="button" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="primary" disabled={busy || !name.trim()}>
            Create {info.one}
          </button>
        </div>
      </form>
    </dialog>
  )
}
