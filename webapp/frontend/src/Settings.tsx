import { Fragment, useCallback, useEffect, useState } from 'react'
import {
  getNotificationSettings,
  listMCPServers,
  updateNotificationSettings,
  type MCPServer,
  type Me,
  type NotificationSettings,
} from './api'
import { configures, levelIn, managesPeople, managesProjects, onlySees, useMe } from './Account'
import { AccessSection } from './Access'
import { HomeCrumb, LEVEL_WORDS } from './Projects'
import { pageHash } from './address'
import { MCPServers } from './MCPSettings'
import { DirectorySection } from './Directory'
import { PeopleSection } from './People'
import { TeamsSection } from './Teams'
import { ModelProvidersSection } from './ModelProviders'
import { APIKeysSection } from './APIKeys'
import { SkillsSection } from './Skills'
import { EgressSection } from './Egress'
import { CredentialsSection } from './Credentials'
import { SpendingSection } from './Spending'
import { AuditLogSection } from './Audit'
import { SettingsHead } from './SettingsHead'
import ErrorBanner from './ErrorBanner'

// The parts of the settings, each a tab: #/settings/<tab>, in the project
// the page is in.
export type SettingsTab =
  | 'account'
  | 'access'
  | 'keys'
  | 'connectors'
  | 'mcp'
  | 'egress'
  | 'credentials'
  | 'spending'
  | 'skills'
  | 'api'
  | 'people'
  | 'teams'
  | 'audit'

// The groups the tabs show in down the side, each under its heading: the
// person's own, what runs use, keeping watch over them, all the project's;
// and the instance's, above the projects.
type SettingsGroup = 'you' | 'runs' | 'oversight' | 'instance'

const GROUPS: { id: SettingsGroup; label: string }[] = [
  { id: 'you', label: 'You' },
  { id: 'runs', label: 'What runs use' },
  { id: 'oversight', label: 'Oversight' },
  { id: 'instance', label: 'Instance' },
]

// The tabs whoever is signed in sees: their own settings, the project's
// owners' over the project, and the instance's admin's over its people.
// Without sign-in, there are only the project's. Someone who only sees the
// project has nothing of theirs to make there: no API keys, MCP
// connections or skills. The command palette offers the same, leaving out
// the groups.
export function tabsFor(me: Me): { id: SettingsTab; label: string; group: SettingsGroup }[] {
  const tabs: { id: SettingsTab; label: string; group: SettingsGroup }[] = []
  const sees = onlySees(me)
  if (me.user) tabs.push({ id: 'account', label: 'Account', group: 'you' })
  if (!sees) tabs.push({ id: 'api', label: 'API keys', group: 'you' })
  if (configures(me)) tabs.push({ id: 'keys', label: 'Model providers', group: 'runs' })
  if (!sees) tabs.push({ id: 'connectors', label: 'Connectors', group: 'runs' })
  if (!sees) tabs.push({ id: 'mcp', label: 'MCP servers', group: 'runs' })
  if (!sees) tabs.push({ id: 'skills', label: 'Skills & commands', group: 'runs' })
  if (configures(me)) tabs.push({ id: 'credentials', label: 'Credentials', group: 'runs' })
  if (configures(me)) tabs.push({ id: 'egress', label: 'Internet access', group: 'runs' })
  if (me.user && configures(me)) tabs.push({ id: 'access', label: 'Access', group: 'oversight' })
  tabs.push({ id: 'spending', label: 'Spending & limits', group: 'oversight' })
  if (me.user) tabs.push({ id: 'teams', label: 'Teams', group: 'oversight' })
  if (configures(me)) tabs.push({ id: 'audit', label: 'Audit log', group: 'oversight' })
  if (managesPeople(me)) tabs.push({ id: 'people', label: 'People', group: 'instance' })
  return tabs
}

// The tab the settings open on when the address names none: the person's
// account, or without sign-in what runs use first, never the API keys.
function landingTab(tabs: { id: SettingsTab; group: SettingsGroup }[]): SettingsTab | undefined {
  return (tabs.find((t) => t.id === 'account') ?? tabs.find((t) => t.group === 'runs') ?? tabs[0])?.id
}

// Where everyone keeps their MCP connections, skills and commands, and admins
// and team managers configure the instance: its model providers and MCP
// servers, who signs in, and the teams.
export default function Settings({ tab }: { tab: SettingsTab | null }) {
  const me = useMe()
  const [error, setError] = useState<string | null>(null)
  const report = useCallback((err: unknown) => setError(err instanceof Error ? err.message : String(err)), [])

  const tabs = me ? tabsFor(me) : []
  const shown = tabs.find((t) => t.id === tab)?.id ?? landingTab(tabs)

  useEffect(() => {
    document.title = shown === 'audit' ? 'Audit log · Spoletta' : 'Settings · Spoletta'
  }, [shown])

  return (
    <div className="flows-page">
      <header className="toolbar">
        <h1>
          <HomeCrumb />
          <span className="crumb-separator" aria-hidden>
            /
          </span>
          Settings
        </h1>
      </header>
      <ErrorBanner error={error} onDismiss={() => setError(null)} />
      {me && (
        <div className={shown === 'audit' ? 'settings wide' : 'settings'}>
          <nav className="settings-tabs" aria-label="Settings">
            {GROUPS.filter((g) => tabs.some((t) => t.group === g.id) || (g.id === 'instance' && managesProjects(me))).map((g) => (
              <Fragment key={g.id}>
                <div className="settings-tabs-group">{g.label}</div>
                {tabs
                  .filter((t) => t.group === g.id)
                  .map((t) => (
                    <a
                      key={t.id}
                      href={pageHash(`#/settings/${t.id}`)}
                      className={t.id === shown ? 'active' : undefined}
                      aria-current={t.id === shown ? 'page' : undefined}
                    >
                      {t.label}
                    </a>
                  ))}
                {/* The projects are kept on a page of their own, outside any. */}
                {g.id === 'instance' && managesProjects(me) && (
                  <a href={pageHash('#/projects')} title="Make, rename, archive and delete projects, and open each one's access">
                    Projects
                  </a>
                )}
              </Fragment>
            ))}
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
            {shown === 'credentials' && <CredentialsSection onError={report} />}
            {shown === 'spending' && <SpendingSection admin={configures(me)} onError={report} />}
            {shown === 'skills' && <SkillsSection onError={report} />}
            {shown === 'api' && <APIKeysSection onError={report} />}
            {shown === 'access' && <AccessSection onError={report} />}
            {shown === 'teams' && (
              <TeamsSection canManage={!onlySees(me) && (configures(me) || !!me.user?.teamManager)} onError={report} />
            )}
            {shown === 'people' && <PeopleSection onError={report} />}
            {shown === 'audit' && <AuditLogSection />}
          </main>
        </div>
      )}
    </div>
  )
}

function AccountSection({ me }: { me: Me }) {
  const u = me.user!
  const level = levelIn(me)
  return (
    <section className="flow-section" aria-label="Account">
      <h2>{u.name || u.email}</h2>
      <dl className="settings-facts">
        <dt>Email</dt>
        <dd>{u.email}</dd>
        <dt>Here</dt>
        <dd>
          {level === 'owner'
            ? 'Own this project: its settings, who has access, and everything in it'
            : level === 'viewer'
              ? 'See this project’s boards, runs and records'
              : 'Work on the flows that are yours, your teams’ and everyone’s in this project'}
          {level !== 'owner' && level !== 'viewer' && u.teamManager && '; make teams and say who is in them'}
        </dd>
        <dt>Projects</dt>
        <dd>
          {me.projects.length > 0 ? me.projects.map((p) => `${p.name} (${LEVEL_WORDS[p.level].label.toLowerCase()})`).join(', ') : 'None'}
          {me.super && '; as an admin of Spoletta you make projects, enter each as an owner and manage people'}
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
        what waits on you is in your <a href={pageHash('#/inbox')}>inbox</a> too. Each message links straight to it.
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
      <SettingsHead
        title="MCP servers"
        lead="The servers agents reach tools through, each connected by signing in, or for one taking a token, by an admin."
        more={
          <>
            Pick, in an agent process's panel, the servers it may use and, if you like, which of their tools. Agents use
            none unless picked, and reach them as whoever their run is for, with that person's connection. Services of
            the directory, and built-in connectors such as GitHub's, are installed from{' '}
            <a href={pageHash('#/settings/connectors')}>Connectors</a>
            {configures(me) ? '; add any other server by its URL below.' : '.'}
          </>
        }
      />
      <MCPServers servers={servers} canConfigure={configures(me)} onChanged={refresh} onError={onError} note={false} />
    </section>
  )
}
