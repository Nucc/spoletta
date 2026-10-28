import { useCallback, useEffect, useRef, useState, type ChangeEvent, type FormEvent, type KeyboardEvent } from 'react'
import {
  createFlow,
  createFromTemplate,
  exportSetup,
  importSetup,
  listFlows,
  listTeams,
  restoreFlow,
  setFavourite,
  setFlowOwner,
  setFlowTags,
  trashFlow,
  type Flow,
  type FlowTemplate,
  type Me,
  type Team,
} from './api'
import {
  ActivityIcon,
  ClockIcon,
  DownloadIcon,
  FlowBuilderIcon,
  ObjectIcon,
  PlusIcon,
  SearchIcon,
  StarIcon,
  SubflowIcon,
  TagIcon,
  TrashIcon,
  UploadIcon,
} from './icons'
import { formatTime, RUN_LABELS } from './Runs'
import { Account, configures, useMe } from './Account'
import { OwnerPicker, ownerLabel } from './Owner'
import { TemplateDialog, TemplateGallery } from './Templates'
import { useConfirm } from './Confirm'
import { useUndoNotice } from './UndoNotice'
import { PROCEDURE_WORDS, STRUCT_WORDS } from './words'

// The main page: the flows that run, each a job of its own, such as fixing
// exceptions or bumping dependencies, and apart from them the procedures,
// the flows made to be reused inside others, and the structs, the objects
// they use. Opening one shows its board. With sign-in, only those one sees
// are listed: one's own, one's teams', everyone's, and those whose reviews
// one decides; they can be narrowed to whose they are, to those whose name
// or tags hold what is typed in the search, and to a tag. One's favourites,
// starred, come first in each section, and each flow says how its runs went
// this week. What is searched and the tag picked are in the address,
// #/?q=<words>&tag=<tag>. A flow can start from a template, offered in
// place while there is no flow at all. Deleting one moves it to the trash,
// which can be undone, so it asks nothing first unless runs going on in it
// would stop.
export default function FlowList({ onOpen }: { onOpen: (id: number) => void }) {
  const me = useMe()
  const [all, setFlows] = useState<Flow[] | null>(null)
  const [whose, setWhose] = useState('')
  const [query, setQuery] = useState(() => hashParam('q'))
  const [tag, setTag] = useState(() => hashParam('tag'))
  const [teams, setTeams] = useState<Team[]>([])
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [templating, setTemplating] = useState(false)
  const picker = useRef<HTMLInputElement>(null)
  const [confirm, confirmDialog] = useConfirm()
  const [showUndo, , undoNotice] = useUndoNotice()

  const narrowed = !!(whose || query.trim() || tag)
  const flows =
    all &&
    all
      .filter((f) => (!whose || whoseKey(f) === whose) && matches(f, query) && (!tag || f.tags.some((t) => sameTag(t, tag))))
      // Favourites first, each part in the order the server gives, by name.
      .sort((a, b) => Number(b.favourite) - Number(a.favourite))
  const tags = all ? allTags(all) : []

  // The search and the tag go in the address, in place, keeping the tab's entry.
  useEffect(() => {
    const params = new URLSearchParams()
    if (query) params.set('q', query)
    if (tag) params.set('tag', tag)
    const hash = params.toString() ? `#/?${params}` : window.location.hash === '' ? '' : '#/'
    if (window.location.hash !== hash) window.history.replaceState(window.history.state, '', window.location.pathname + window.location.search + hash)
  }, [query, tag])

  const report = useCallback((err: unknown) => setError(err instanceof Error ? err.message : String(err)), [])
  const refresh = useCallback(() => listFlows().then(setFlows).catch(report), [report])

  useEffect(() => {
    document.title = 'Flows'
    refresh()
  }, [refresh])

  useEffect(() => {
    if (me?.user) listTeams().then(setTeams, report)
  }, [me, report])

  const onOwner = (f: Flow, owner: Parameters<typeof setFlowOwner>[1]) =>
    setFlowOwner(f.id, owner).then(refresh, report)

  // What changes of a flow on the list, at once, and back if the server
  // turns it down.
  const change = (f: Flow, patch: Partial<Flow>) => setFlows((fs) => fs?.map((x) => (x.id === f.id ? { ...x, ...patch } : x)) ?? null)

  const onStar = (f: Flow) => {
    change(f, { favourite: !f.favourite })
    setFavourite(f.id, !f.favourite).catch((err) => {
      change(f, { favourite: f.favourite })
      report(err)
    })
  }

  const onTags = async (f: Flow, tags: string[]) => {
    try {
      change(f, { tags: await setFlowTags(f.id, tags) })
    } catch (err) {
      report(err)
    }
  }

  // Who new flows belong to: the team picked last, else the first of one's
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

  const onCreate = async (name: string, kind: FlowKind) => {
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
              can't be undone. The {KIND_WORDS[kindOf(f)]} itself can be restored from the trash.
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

  return (
    <div className="flows-page">
      <header className="toolbar">
        <h1>
          <FlowBuilderIcon /> Flows
        </h1>
        <a className="toolbar-link" href="#/activity" title="What runs and fails across the flows, what they spend, and how reviews go">
          <ActivityIcon /> Activity
        </a>
        <a className="toolbar-link" href="#/schedules">
          <ClockIcon /> Schedules
        </a>
        <a className="toolbar-link" href="#/trash" title="What was deleted, to restore or delete for good">
          <TrashIcon /> Trash
        </a>
        {configures(me) && (
          <>
            <button
              className="toolbar-link"
              onClick={onExport}
              title="Download every flow, procedure, struct, MCP server, skill and command as a YAML file"
            >
              <DownloadIcon /> Export
            </button>
            <button
              className="toolbar-link"
              onClick={() => picker.current?.click()}
              title="Create or update flows, procedures, structs, MCP servers, skills and commands from a YAML file"
            >
              <UploadIcon /> Import
            </button>
            <input ref={picker} type="file" accept=".yaml,.yml,.json" hidden onChange={onImport} />
          </>
        )}
        <Account />
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
      <main className="flows">
        {all && all.length === 0 && (
          <section className="flow-section templates-start" aria-label="Start from a template">
            <h2>Start from a template</h2>
            <p className="flows-note">
              Each is a ready flow, made a flow of your own to change as you like; or name a new one below to start from an empty board.
            </p>
            <TemplateGallery admin={configures(me)} signIn={!!me?.signIn} onUse={onTemplate} onError={report} />
          </section>
        )}
        {templating && (
          <TemplateDialog admin={configures(me)} signIn={!!me?.signIn} onUse={onTemplate} onClose={() => setTemplating(false)} onError={report} />
        )}
        {all && all.length > 0 && (
          <div className="flows-filters">
            <label className="flows-search">
              <SearchIcon />
              <input
                type="search"
                aria-label="Search the flows by name and tag"
                placeholder="Search by name or tag"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
            </label>
            {tags.length > 0 && (
              <label>
                <TagIcon />
                <select aria-label="Tag" value={tags.find((t) => sameTag(t, tag)) ?? ''} onChange={(e) => setTag(e.target.value)}>
                  <option value="">Every tag</option>
                  {tags.map((t) => (
                    <option key={t} value={t}>
                      {t}
                    </option>
                  ))}
                </select>
              </label>
            )}
          </div>
        )}
        {me?.user && all && (
          <div className="flows-whose">
            <label>
              Show
              <select value={whose} onChange={(e) => setWhose(e.target.value)}>
                {whoseChoices(all, me).map(([key, label]) => (
                  <option key={key} value={key}>
                    {label}
                  </option>
                ))}
              </select>
            </label>
            <label title="Who sees the flows, procedures and structs you make, and may change them">
              New ones belong to
              <select value={newOwnerKey} onChange={(e) => pickNewOwner(e.target.value)}>
                {myTeams.map((t) => (
                  <option key={t.id} value={`team:${t.id}`}>
                    Team {t.name}
                  </option>
                ))}
                <option value="me">Only me</option>
                <option value="global">Everyone</option>
              </select>
            </label>
          </div>
        )}
        <FlowSection
          title="Flows"
          note="The jobs that run, each doing one thing end to end."
          empty="No flows yet. Each flow does one job; name one above to start its board."
          placeholder="Name a new flow, such as Fix exceptions"
          button="New flow"
          flows={flows?.filter((f) => kindOf(f) === 'flow') ?? null}
          onCreate={(name) => onCreate(name, 'flow')}
          onTemplate={() => setTemplating(true)}
          me={me}
          teams={teams}
          onOwner={onOwner}
          onDelete={onDelete}
          onError={report}
          narrowed={narrowed}
          tags={tags}
          onStar={onStar}
          onTags={onTags}
          onTag={setTag}
        />
        <FlowSection
          kind="module"
          title="Procedures"
          gloss={PROCEDURE_WORDS.gloss}
          note="Parts reused inside flows, dragged in from a board's sidebar. Changing one changes it everywhere it runs, and the objects it leaves follow it."
          empty="No procedures yet. A procedure is a group of processes other flows run, such as creating a Jira ticket."
          placeholder="Name a new procedure, such as Create a Jira ticket"
          button="New procedure"
          flows={flows?.filter((f) => kindOf(f) === 'module') ?? null}
          onCreate={(name) => onCreate(name, 'module')}
          me={me}
          teams={teams}
          onOwner={onOwner}
          onDelete={onDelete}
          onError={report}
          narrowed={narrowed}
          tags={tags}
          onStar={onStar}
          onTags={onTags}
          onTag={setTag}
        />
        <FlowSection
          kind="struct"
          title="Structs"
          gloss={STRUCT_WORDS.gloss}
          note="Objects defined once, with their states, attributes and parts, and dragged in from a board's sidebar. Changing one changes every copy of it."
          empty="No structs yet. A struct is a single object flows use, such as a Jira ticket."
          placeholder="Name a new struct, such as Jira ticket"
          button="New struct"
          flows={flows?.filter((f) => kindOf(f) === 'struct') ?? null}
          onCreate={(name) => onCreate(name, 'struct')}
          me={me}
          teams={teams}
          onOwner={onOwner}
          onDelete={onDelete}
          onError={report}
          narrowed={narrowed}
          tags={tags}
          onStar={onStar}
          onTags={onTags}
          onTag={setTag}
        />
      </main>
      {confirmDialog}
      {undoNotice}
    </div>
  )
}

// What flows are narrowed to by whose they are: an owner's key, or
// "review" for those one only decides the reviews of.
const whoseKey = (f: Flow) =>
  f.access === 'review' ? 'review' : f.owner.kind === 'global' ? 'global' : `${f.owner.kind}:${f.owner.id}`

// The choices of whose flows to show, the owners of those listed.
function whoseChoices(flows: Flow[], me: Me): [string, string][] {
  const choices = new Map<string, string>([['', 'Every flow I see']])
  const mine = `user:${me.user?.id}`
  if (flows.some((f) => whoseKey(f) === mine)) choices.set(mine, 'Mine')
  for (const f of flows) {
    const k = whoseKey(f)
    if (k === 'review') choices.set(k, 'Those I review')
    else if (k === 'global') choices.set(k, 'Everyone’s')
    else if (!choices.has(k))
      choices.set(k, f.owner.kind === 'team' ? `Team ${ownerLabel(f.owner, me)}` : `${ownerLabel(f.owner, me)}’s`)
  }
  return [...choices]
}

// Where who new flows belong to is remembered.
const NEW_OWNER_KEY = 'spoletta.newOwner'

// What the address says of the list: the search, or the tag picked.
const hashParam = (key: string) => new URLSearchParams(window.location.hash.split('?')[1] ?? '').get(key) ?? ''

// Tags are the same whatever their case.
const sameTag = (a: string, b: string) => a.toLowerCase() === b.toLowerCase()

// Whether every word searched for is in the flow's name or one of its tags.
const matches = (f: Flow, query: string) =>
  query
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean)
    .every((w) => f.name.toLowerCase().includes(w) || f.tags.some((t) => t.toLowerCase().includes(w)))

// Every tag of the flows, each once, spelled as it is first, by name.
function allTags(flows: Flow[]): string[] {
  const tags = new Map<string, string>()
  for (const f of flows) for (const t of f.tags) if (!tags.has(t.toLowerCase())) tags.set(t.toLowerCase(), t)
  return [...tags.values()].sort((a, b) => a.localeCompare(b, undefined, { sensitivity: 'base' }))
}

const usedByLabel = (n: number) => `Used in ${n} ${n === 1 ? 'flow' : 'flows'}`

type FlowKind = 'flow' | 'module' | 'struct'

const kindOf = (f: Flow): FlowKind => (f.struct ? 'struct' : f.module ? 'module' : 'flow')

const DELETE_TITLES: Record<FlowKind, string> = {
  flow: 'Move the flow to the trash',
  module: 'Move the procedure to the trash',
  struct: 'Move the struct to the trash',
}

const KIND_WORDS: Record<FlowKind, string> = { flow: 'flow', module: 'procedure', struct: 'struct' }

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

// What a card says under its name: how many flows use it, its latest run,
// and how its runs went this week; a struct never runs.
function cardStatus(f: Flow, kind: FlowKind) {
  const used = f.usedBy > 0 ? usedByLabel(f.usedBy) : 'Not used yet'
  if (kind === 'struct') return used
  return (
    <>
      {kind === 'module' && <span>{used} · </span>}
      {f.lastRun ? (
        <>
          <span className={`status-dot ${f.lastRun.status}`} aria-hidden />
          {RUN_LABELS[f.lastRun.status]} · {formatTime(f.lastRun.startedAt)}
        </>
      ) : kind === 'module' ? (
        'Never run on its own'
      ) : (
        'Never run'
      )}
      <Health f={f} />
    </>
  )
}

// Whether the flow is failing, by its last run or its runs this week, how
// many of its steps wait on the person, and how many of its runs are going on.
function Health({ f }: { f: Flow }) {
  const h = f.health
  if (!h) return null
  const lastFailed = f.lastRun?.status === 'failed'
  const share = h.runs > 0 ? Math.round((100 * h.failed) / h.runs) : 0
  const failing = lastFailed || h.failed > 0
  return (
    <>
      {failing && (
        <span
          className={lastFailed || share >= 50 ? 'flow-health failing' : 'flow-health flaky'}
          title={`Of its ${h.runs} runs in the last 7 days, ${h.failed} failed${lastFailed ? ', and its last run did' : ''}.`}
        >
          {[lastFailed && 'Last run failed', h.failed > 0 && `${share}% failed this week`].filter(Boolean).join(' · ')}
        </span>
      )}
      {h.waiting > 0 && (
        <span className="flow-health waiting" title="Reviews, inputs and loop questions in its runs that you may decide, which the Inbox lists">
          {h.waiting} waiting on you
        </span>
      )}
      {h.running > 0 && (
        <span className="flow-health running" title="Its runs going on now, those waiting for a person included">
          {h.running} running
        </span>
      )}
    </>
  )
}

// A flow's tags, each narrowing the list to it; those who may change the
// flow take one away, or add another.
function FlowTags({
  f,
  known,
  onTags,
  onTag,
}: {
  f: Flow
  known: string[]
  onTags: (f: Flow, tags: string[]) => Promise<void>
  onTag: (tag: string) => void
}) {
  const [adding, setAdding] = useState(false)
  const [text, setText] = useState('')
  const editable = f.access === 'edit'
  if (f.tags.length === 0 && !editable) return null

  const add = async () => {
    const t = text.trim()
    setAdding(false)
    setText('')
    if (t && !f.tags.some((x) => sameTag(x, t))) await onTags(f, [...f.tags, t])
  }
  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      void add()
    } else if (e.key === 'Escape') {
      setAdding(false)
      setText('')
    }
  }

  return (
    <div className={f.tags.length === 0 ? 'flow-tags empty' : 'flow-tags'}>
      {f.tags.map((t) => (
        <span key={t} className="flow-tag">
          <button className="flow-tag-name" onClick={() => onTag(t)} title={`Show only the flows tagged ${t}`}>
            {t}
          </button>
          {editable && (
            <button className="flow-tag-remove" onClick={() => void onTags(f, f.tags.filter((x) => x !== t))} aria-label={`Take the tag ${t} away`}>
              ×
            </button>
          )}
        </span>
      ))}
      {editable &&
        (adding ? (
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
        ) : (
          <button className="flow-tag-add" onClick={() => setAdding(true)} title="Tag it, to find it by">
            <PlusIcon size={10} /> Tag
          </button>
        ))}
    </div>
  )
}

function FlowSection({
  kind = 'flow',
  me,
  teams,
  onOwner,
  title,
  gloss,
  note,
  empty,
  placeholder,
  button,
  flows,
  onCreate,
  onDelete,
  onError,
  narrowed,
  tags,
  onStar,
  onTags,
  onTag,
  onTemplate,
}: {
  kind?: FlowKind
  title: string
  // What the kind is in plain words, after its name; see words.ts.
  gloss?: string
  note: string
  empty: string
  placeholder: string
  button: string
  flows: Flow[] | null
  me: Me | null
  teams: Team[]
  onOwner: (f: Flow, owner: Parameters<typeof setFlowOwner>[1]) => void
  onCreate: (name: string) => Promise<void>
  onDelete: (f: Flow) => void
  onError: (err: unknown) => void
  // Whether the list shows only some flows, and every tag there is.
  narrowed: boolean
  tags: string[]
  onStar: (f: Flow) => void
  onTags: (f: Flow, tags: string[]) => Promise<void>
  onTag: (tag: string) => void
  // Opens the templates to start a new one from, if it can.
  onTemplate?: () => void
}) {
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    setBusy(true)
    try {
      await onCreate(name.trim())
    } catch (err) {
      onError(err)
      setBusy(false)
    }
  }

  return (
    <section className={`flow-section${kind === 'flow' ? '' : ` ${kind}s`}`} aria-label={title}>
      <h2>
        {kind === 'module' && <SubflowIcon />}
        {kind === 'struct' && <ObjectIcon />}
        {title}
        {gloss && <span className="flow-section-gloss">({gloss})</span>}
      </h2>
      <p className="flows-note">{note}</p>
      <form className="flow-new" onSubmit={submit}>
        <input
          aria-label={placeholder}
          placeholder={placeholder}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <button type="submit" disabled={busy || !name.trim()}>
          <PlusIcon /> {button}
        </button>
        {onTemplate && (
          <button type="button" className="flow-new-template" onClick={onTemplate} title="Start from a ready flow, made a flow of your own">
            New from template
          </button>
        )}
      </form>
      {flows === null ? (
        <p className="flows-note">Loading…</p>
      ) : flows.length === 0 ? (
        <p className="flows-note">{narrowed ? `None of the ${title.toLowerCase()} match.` : empty}</p>
      ) : (
        <ul className="flow-list">
          {flows.map((f) => (
            <li key={f.id}>
              <button
                className={f.favourite ? 'icon-button flow-star starred' : 'icon-button flow-star'}
                onClick={() => onStar(f)}
                aria-pressed={f.favourite}
                aria-label={f.favourite ? `Take ${f.name} out of your favourites` : `Star ${f.name} as a favourite`}
                title={f.favourite ? 'A favourite of yours, listed first' : 'Star it as a favourite, to list it first'}
              >
                <StarIcon filled={f.favourite} />
              </button>
              <div className="flow-card-body">
                <a className="flow-card" href={`#/flows/${f.id}`}>
                  <span className="flow-card-name">{f.name}</span>
                  <span className="flow-card-run">{cardStatus(f, kind)}</span>
                </a>
                <FlowTags f={f} known={tags} onTags={onTags} onTag={onTag} />
              </div>
              {me?.user &&
                (f.access === 'review' ? (
                  <span
                    className="flow-owner review"
                    title="You decide its reviews, so you see it; only its owner changes it"
                  >
                    Reviewing · {ownerLabel(f.owner, me)}
                  </span>
                ) : (
                  <OwnerPicker flow={f} me={me} teams={teams} compact onChange={(o) => onOwner(f, o)} />
                ))}
              {f.access === 'edit' && (
                <button
                  className="icon-button flow-delete"
                  onClick={() => onDelete(f)}
                  aria-label={`Delete ${f.name}`}
                  title={DELETE_TITLES[kind]}
                >
                  <TrashIcon />
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
