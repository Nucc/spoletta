import { useCallback, useEffect, useState, type FormEvent } from 'react'
import {
  deleteModelKey,
  listMCPServers,
  listModelKeys,
  listModelProviders,
  setModelKey,
  type KeyScope,
  type MCPServer,
  type Me,
  type ModelKey,
  type ModelProvider,
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
  tabs.push({ id: 'keys', label: 'Model keys' }, { id: 'mcp', label: 'MCP servers' })
  if (me.user) tabs.push({ id: 'teams', label: 'Teams' })
  if (me.user?.admin) tabs.push({ id: 'people', label: 'People' })
  return tabs
}

// Where everyone keeps their model keys and MCP connections, and admins and
// team managers configure the instance: its keys and MCP servers, who signs
// in, and the teams.
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
            {shown === 'keys' && <ModelKeysSection me={me} onError={report} />}
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

// The keys agents reach their models' providers with: one's own, which the
// agents of runs for them use, and for admins the instance's, which those
// without one of their own use.
function ModelKeysSection({ me, onError }: { me: Me; onError: (err: unknown) => void }) {
  const [providers, setProviders] = useState<ModelProvider[]>([])
  useEffect(() => {
    listModelProviders().then(setProviders, onError)
  }, [onError])
  return (
    <>
      {me.user && (
        <KeyList
          scope="me"
          title="Your keys"
          note="Agents of the runs you start, and of your schedules, reach their model with your key for its provider, else the instance's. Keys don't show again once saved."
          providers={providers}
          onError={onError}
        />
      )}
      {configures(me) && (
        <KeyList
          scope="instance"
          title="The instance's keys"
          note="Used by everyone without a key of their own for the provider; the worker's environment has the last say for a provider without either."
          providers={providers}
          onError={onError}
        />
      )}
    </>
  )
}

function KeyList({
  scope,
  title,
  note,
  providers,
  onError,
}: {
  scope: KeyScope
  title: string
  note: string
  providers: ModelProvider[]
  onError: (err: unknown) => void
}) {
  const [keys, setKeys] = useState<ModelKey[] | null>(null)
  useEffect(() => {
    listModelKeys(scope).then(setKeys, onError)
  }, [scope, onError])

  const save = async (provider: string, key: string) => {
    try {
      setKeys(await setModelKey(scope, provider, key))
      return true
    } catch (err) {
      onError(err)
      return false
    }
  }
  const remove = async (provider: string) => {
    try {
      await deleteModelKey(scope, provider)
      setKeys((ks) => ks && ks.filter((k) => k.provider !== provider))
    } catch (err) {
      onError(err)
    }
  }

  return (
    <section className="flow-section" aria-label={title}>
      <h2>{title}</h2>
      <p className="flows-note">{note}</p>
      {keys === null ? (
        <p className="flows-note">Loading…</p>
      ) : (
        <ul className="flow-list key-list">
          {providers.map((p) => (
            <KeyItem
              key={p.id}
              provider={p}
              saved={keys.find((k) => k.provider === p.id)}
              onSave={(k) => save(p.id, k)}
              onRemove={() => remove(p.id)}
            />
          ))}
        </ul>
      )}
    </section>
  )
}

function KeyItem({
  provider,
  saved,
  onSave,
  onRemove,
}: {
  provider: ModelProvider
  saved: ModelKey | undefined
  onSave: (key: string) => Promise<boolean>
  onRemove: () => void
}) {
  const [key, setKey] = useState('')
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (key.trim() && (await onSave(key.trim()))) setKey('')
  }
  return (
    <li>
      <form className="key-item" onSubmit={submit}>
        <span className="key-provider">
          <span className="flow-card-name">{provider.name}</span>
          <span className="flows-note">
            {saved ? `…${saved.hint} · saved ${formatTime(saved.updatedAt)}` : 'Not set'}
          </span>
        </span>
        <input
          type="password"
          autoComplete="off"
          spellCheck={false}
          placeholder={saved ? 'Replace the key' : provider.env}
          aria-label={`${provider.name} key`}
          value={key}
          onChange={(e) => setKey(e.target.value)}
        />
        <button type="submit" disabled={!key.trim()}>
          Save
        </button>
        {saved && (
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
