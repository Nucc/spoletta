import { useCallback, useEffect, useState, type FormEvent } from 'react'
import {
  deleteModelKey,
  listMCPServers,
  listModelKeys,
  setModelKey,
  type MCPServer,
  type Me,
  type ModelKey,
  type ModelKeys,
} from './api'
import { configures, useMe } from './Account'
import { FlowBuilderIcon } from './icons'
import { MCPServers } from './MCPSettings'
import { PeopleSection } from './People'
import { TeamsSection } from './Teams'
import { formatTime } from './Runs'

// The parts of the settings, each a tab: #/settings/<tab>.
export type SettingsTab = 'account' | 'keys' | 'mcp' | 'people' | 'teams'

// The tabs whoever is signed in sees: their own settings, and an admin's
// over the instance. Without sign-in, there is only the instance's.
function tabsFor(me: Me): { id: SettingsTab; label: string }[] {
  const tabs: { id: SettingsTab; label: string }[] = []
  if (me.user) tabs.push({ id: 'account', label: 'Account' })
  if (configures(me)) tabs.push({ id: 'keys', label: 'Model keys' })
  tabs.push({ id: 'mcp', label: 'MCP servers' })
  if (me.user) tabs.push({ id: 'teams', label: 'Teams' })
  if (me.user?.admin) tabs.push({ id: 'people', label: 'People' })
  return tabs
}

// Where everyone keeps their MCP connections, and admins and team managers
// configure the instance: its model keys and MCP servers, who signs in, and
// the teams.
export default function Settings({ tab }: { tab: SettingsTab | null }) {
  const me = useMe()
  const [error, setError] = useState<string | null>(null)
  const report = useCallback((err: unknown) => setError(err instanceof Error ? err.message : String(err)), [])

  useEffect(() => {
    document.title = 'Settings · Flows'
  }, [])

  const tabs = me ? tabsFor(me) : []
  const shown = tabs.find((t) => t.id === tab)?.id ?? tabs[0]?.id

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
          Settings
        </h1>
      </header>
      {error && (
        <div className="error-banner" role="alert" onClick={() => setError(null)}>
          {error} (click to dismiss)
        </div>
      )}
      {me && (
        <div className="settings">
          <nav className="settings-tabs" aria-label="Settings">
            {tabs.map((t) => (
              <a
                key={t.id}
                href={`#/settings/${t.id}`}
                className={t.id === shown ? 'active' : undefined}
                aria-current={t.id === shown ? 'page' : undefined}
              >
                {t.label}
              </a>
            ))}
          </nav>
          <main className="settings-main">
            {shown === 'account' && <AccountSection me={me} />}
            {shown === 'keys' && <ModelKeysSection onError={report} />}
            {shown === 'mcp' && <MCPSection me={me} onError={report} />}
            {shown === 'teams' && (
              <TeamsSection canManage={!!(me.user?.admin || me.user?.teamManager)} onError={report} />
            )}
            {shown === 'people' && <PeopleSection onError={report} />}
          </main>
        </div>
      )}
    </div>
  )
}

function AccountSection({ me }: { me: Me }) {
  const u = me.user!
  return (
    <section className="flow-section" aria-label="Account">
      <h2>{u.name || u.email}</h2>
      <dl className="settings-facts">
        <dt>Email</dt>
        <dd>{u.email}</dd>
        <dt>Can</dt>
        <dd>
          {u.admin
            ? 'Configure everything: people, teams, model keys, MCP servers'
            : u.teamManager
              ? 'Make teams and say who is in them'
              : 'Work on the flows that are yours, your teams’ and everyone’s'}
        </dd>
        <dt>Teams</dt>
        <dd>{u.teams.length > 0 ? u.teams.map((t) => t.name).join(', ') : 'None'}</dd>
        <dt>Roles</dt>
        <dd>
          {u.roles.length > 0 ? `${u.roles.join(', ')}: you decide the reviews of the processes these handle` : 'None'}
        </dd>
      </dl>
    </section>
  )
}

// The keys agents reach their models' providers with, which admins save and
// everyone's agents use: one for each variable the worker's models read a
// key from, which several providers may share, as a gateway's do.
function ModelKeysSection({ onError }: { onError: (err: unknown) => void }) {
  const [keys, setKeys] = useState<ModelKeys | null>(null)
  const refresh = useCallback(() => listModelKeys().then(setKeys, onError), [onError])
  useEffect(() => {
    refresh()
  }, [refresh])

  const save = async (env: string, key: string) => {
    try {
      setKeys(await setModelKey(env, key))
      return true
    } catch (err) {
      onError(err)
      return false
    }
  }
  const remove = async (env: string) => {
    try {
      await deleteModelKey(env)
      await refresh()
    } catch (err) {
      onError(err)
    }
  }

  const settable = keys?.keys.filter((k) => k.env !== '') ?? []
  const elsewhere = keys?.keys.filter((k) => k.env === '').flatMap((k) => k.providers) ?? []
  return (
    <section className="flow-section" aria-label="Model keys">
      <h2>Model keys</h2>
      <p className="flows-note">
        The keys the models agents may use reach their providers with, whoever started the run. A key saved here goes
        over the worker's own; keys don't show again once saved.
      </p>
      {keys?.workerError && <p className="panel-warning">{keys.workerError}</p>}
      {keys === null ? (
        <p className="flows-note">Loading…</p>
      ) : settable.length === 0 ? (
        <p className="flows-note">The worker's models read no key it doesn't set up itself.</p>
      ) : (
        <ul className="flow-list key-list">
          {settable.map((k) => (
            <KeyItem key={k.env} modelKey={k} onSave={(key) => save(k.env, key)} onRemove={() => remove(k.env)} />
          ))}
        </ul>
      )}
      {elsewhere.length > 0 && (
        <p className="flows-note">
          {elsewhere.join(', ')} {elsewhere.length === 1 ? 'gets its key' : 'get their keys'} in the worker itself: from
          its models.json, or its cloud credentials.
        </p>
      )}
    </section>
  )
}

// Where a key comes from, said on its row.
function keyStatus(k: ModelKey): string {
  if (k.saved) return `Saved here, …${k.hint ?? ''}${k.updatedAt ? ` · ${formatTime(k.updatedAt)}` : ''}`
  if (k.set) return 'From the worker’s environment'
  return 'Not set: models of these providers fail'
}

function KeyItem({
  modelKey: k,
  onSave,
  onRemove,
}: {
  modelKey: ModelKey
  onSave: (key: string) => Promise<boolean>
  onRemove: () => void
}) {
  const [key, setKey] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (key.trim() && (await onSave(key.trim()))) setKey('')
  }
  const unused = k.providers.length === 0
  return (
    <li>
      <form className="key-item" onSubmit={submit}>
        <span className="key-provider">
          <span className="flow-card-name key-env">{k.env}</span>
          <span className="flows-note">
            {unused ? 'No model the worker offers reads it' : `For ${k.providers.join(', ')}`}
          </span>
          <span className={`key-status${!k.saved && !k.set ? ' missing' : ''}`}>{keyStatus(k)}</span>
        </span>
        {!unused && (
          <>
            <input
              type="password"
              autoComplete="off"
              data-1p-ignore
              data-lpignore="true"
              spellCheck={false}
              placeholder={k.saved || k.set ? 'Replace the key' : 'Paste the key'}
              aria-label={`Key for ${k.env}`}
              value={key}
              onChange={(e) => setKey(e.target.value)}
            />
            <button type="submit" disabled={!key.trim()}>
              Save
            </button>
          </>
        )}
        {k.saved && (
          <button type="button" className="danger" onClick={onRemove}>
            Remove
          </button>
        )}
      </form>
    </li>
  )
}

// The MCP servers, each connected by everyone for themselves; admins add,
// change and remove them.
function MCPSection({ me, onError }: { me: Me; onError: (err: unknown) => void }) {
  const [servers, setServers] = useState<MCPServer[]>([])
  const refresh = useCallback(() => listMCPServers().then(setServers, onError), [onError])
  useEffect(() => {
    refresh()
    // The sign-in window says when it is done.
    const onMessage = (e: MessageEvent) =>
      e.origin === window.location.origin && e.data?.type === 'mcp-sign-in' && refresh()
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [refresh])
  return (
    <section className="flow-section settings-mcp" aria-label="MCP servers">
      <h2>MCP servers</h2>
      <MCPServers servers={servers} canConfigure={configures(me)} onChanged={refresh} onError={onError} />
    </section>
  )
}
