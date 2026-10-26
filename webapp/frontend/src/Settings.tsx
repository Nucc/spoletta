import { useCallback, useEffect, useState } from 'react'
import {
  getNotificationSettings,
  listMCPServers,
  updateNotificationSettings,
  type MCPServer,
  type Me,
  type NotificationSettings,
} from './api'
import { configures, useMe } from './Account'
import { FlowBuilderIcon } from './icons'
import { MCPServers } from './MCPSettings'
import { DirectorySection } from './Directory'
import { PeopleSection } from './People'
import { TeamsSection } from './Teams'
import { ModelProvidersSection } from './ModelProviders'
import { APIKeysSection } from './APIKeys'
import { SkillsSection } from './Skills'
import { EgressSection } from './Egress'
import { SpendingSection } from './Spending'

// The parts of the settings, each a tab: #/settings/<tab>.
export type SettingsTab = 'account' | 'keys' | 'connectors' | 'mcp' | 'egress' | 'spending' | 'skills' | 'api' | 'people' | 'teams'

// The tabs whoever is signed in sees: their own settings, and an admin's
// over the instance. Without sign-in, there is only the instance's.
function tabsFor(me: Me): { id: SettingsTab; label: string }[] {
  const tabs: { id: SettingsTab; label: string }[] = []
  if (me.user) tabs.push({ id: 'account', label: 'Account' })
  if (configures(me)) tabs.push({ id: 'keys', label: 'Model providers' })
  tabs.push({ id: 'connectors', label: 'Connectors' })
  tabs.push({ id: 'mcp', label: 'MCP servers' })
  if (configures(me)) tabs.push({ id: 'egress', label: 'Internet access' })
  tabs.push({ id: 'spending', label: 'Spending & limits' })
  tabs.push({ id: 'skills', label: 'Skills & commands' })
  tabs.push({ id: 'api', label: 'API keys' })
  if (me.user) tabs.push({ id: 'teams', label: 'Teams' })
  if (me.user?.admin) tabs.push({ id: 'people', label: 'People' })
  return tabs
}

// Where everyone keeps their MCP connections, skills and commands, and admins
// and team managers configure the instance: its model providers and MCP
// servers, who signs in, and the teams.
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
            {configures(me) && <a href="#/audit">Audit log</a>}
          </nav>
          <main className="settings-main">
            {shown === 'account' && (
              <>
                <AccountSection me={me} />
                <NotificationsSection onError={report} />
              </>
            )}
            {shown === 'keys' && <ModelProvidersSection onError={report} />}
            {shown === 'connectors' && <DirectorySection canConfigure={configures(me)} onError={report} />}
            {shown === 'mcp' && <MCPSection me={me} onError={report} />}
            {shown === 'egress' && <EgressSection onError={report} />}
            {shown === 'spending' && <SpendingSection admin={configures(me)} onError={report} />}
            {shown === 'skills' && <SkillsSection onError={report} />}
            {shown === 'api' && <APIKeysSection onError={report} />}
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
            ? 'Configure everything: people, teams, model providers, MCP servers'
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

// What the person is told of, and how: that reviews and questions wait on
// them, by email and on Slack, and whether of their runs failing. A channel
// the instance isn't set up for says so.
function NotificationsSection({ onError }: { onError: (err: unknown) => void }) {
  const [settings, setSettings] = useState<NotificationSettings | null>(null)
  useEffect(() => {
    getNotificationSettings().then(setSettings, onError)
  }, [onError])
  if (!settings) return null
  const change = (patch: Parameters<typeof updateNotificationSettings>[0]) =>
    updateNotificationSettings(patch).then(setSettings, onError)
  return (
    <section className="flow-section notification-settings" aria-label="Notifications">
      <h2>Notifications</h2>
      <p className="flows-note">
        You are told when a review, input or loop question starts waiting on you, and reminded when its process says;
        what waits on you is in your <a href="#/inbox">inbox</a> too. Each message links straight to it.
      </p>
      <label className="panel-check">
        <input
          type="checkbox"
          checked={settings.email}
          disabled={!settings.canEmail}
          onChange={(e) => change({ email: e.target.checked })}
        />
        <span>
          By email
          {!settings.canEmail && <small className="panel-note"> (not set up here: an admin sets SMTP_HOST)</small>}
        </span>
      </label>
      <label className="panel-check">
        <input
          type="checkbox"
          checked={settings.slack}
          disabled={!settings.canSlack}
          onChange={(e) => change({ slack: e.target.checked })}
        />
        <span>
          On Slack, by a direct message
          {!settings.canSlack && <small className="panel-note"> (not set up here: an admin installs the Slack connector)</small>}
        </span>
      </label>
      <label className="panel-check">
        <input type="checkbox" checked={settings.failures} onChange={(e) => change({ failures: e.target.checked })} />
        <span>Also tell me when a run I started fails</span>
      </label>
    </section>
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
