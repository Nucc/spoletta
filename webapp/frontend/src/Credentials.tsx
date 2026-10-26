import { useCallback, useEffect, useState, type FormEvent } from 'react'
import {
  createCredential,
  credentialEnv,
  deleteCredential,
  listCredentials,
  listTeams,
  updateCredential,
  type Credential,
  type CredentialAuth,
  type CredentialChoice,
  type CredentialFields,
  type Team,
} from './api'
import { useMe } from './Account'
import { PlusIcon } from './icons'

// Credentials, kept by admins: a service's base URL and secret, which
// scripts and agents call it with through the webapp's proxy without ever
// seeing the secret. Each is everyone's, or only the teams' given it.
export function CredentialsSection({ onError }: { onError: (err: unknown) => void }) {
  const me = useMe()
  const [items, setItems] = useState<Credential[] | null>(null)
  const [teams, setTeams] = useState<Team[]>([])
  // What is being written: a new one, or the one of this id.
  const [editing, setEditing] = useState<number | 'new' | null>(null)

  const refresh = useCallback(() => listCredentials().then(setItems, onError), [onError])
  useEffect(() => {
    refresh()
  }, [refresh])
  useEffect(() => {
    if (me?.user) listTeams().then(setTeams, onError)
  }, [me, onError])

  const save = async (fields: CredentialFields) => {
    try {
      setItems(editing === 'new' ? await createCredential(fields) : await updateCredential(editing as number, fields))
      setEditing(null)
    } catch (err) {
      onError(err)
    }
  }

  const remove = async (c: Credential) => {
    if (!window.confirm(`Remove the credential ${c.name}? Processes picking it fail to run until it is unpicked, and steps going lose it.`)) return
    try {
      await deleteCredential(c.id)
      await refresh()
    } catch (err) {
      onError(err)
    }
  }

  return (
    <section className="flow-section skills" aria-label="Credentials">
      <div className="skills-header">
        <h2>Credentials</h2>
        {editing !== 'new' && (
          <button type="button" className="skills-new" onClick={() => setEditing('new')}>
            <PlusIcon /> New credential
          </button>
        )}
      </div>
      <p className="flows-note">
        A service’s secret, such as a Jira token or an internal API’s key, which scripts and agents call it with
        without seeing it. A process picks the credentials it uses; its step reaches each service at{' '}
        <code>$CREDENTIAL_NAME_URL</code>, the webapp’s proxy, sending <code>Authorization: Bearer $CREDENTIALS_TOKEN</code>,
        a token that ends with the step, and the proxy puts the secret in its place, blanking it out of what comes back.
        Every call goes in its run’s log and the audit log.
      </p>
      {editing === 'new' && <CredentialEditor teams={teams} signIn={!!me?.user} onSave={save} onCancel={() => setEditing(null)} />}
      {items === null ? (
        <p className="flows-note">Loading…</p>
      ) : items.length === 0 ? (
        editing !== 'new' && <p className="flows-note">None yet.</p>
      ) : (
        <ul className="flow-list skill-list">
          {items.map((c) =>
            editing === c.id ? (
              <li key={c.id}>
                <CredentialEditor initial={c} teams={teams} signIn={!!me?.user} onSave={save} onCancel={() => setEditing(null)} />
              </li>
            ) : (
              <li key={c.id}>
                <div className="skill-item">
                  <span className="skill-what">
                    <span className="skill-name">
                      {c.name} <code className="skill-hint">${credentialEnv(c.variable)}</code>
                    </span>
                    <span className="flows-note">
                      {c.baseUrl} · {howItGoes(c)}
                      {c.headers.length > 0 && ` · sends ${c.headers.join(', ')}`} · {c.everyone ? 'everyone' : c.teams.map((t) => t.name).join(', ') || 'no team'} ·{' '}
                      {c.secretSet ? (c.hint ? `secret …${c.hint}` : 'secret set') : 'no secret'}
                    </span>
                  </span>
                  <button type="button" onClick={() => setEditing(c.id)}>
                    Edit
                  </button>
                  <button type="button" className="danger" onClick={() => remove(c)}>
                    Remove
                  </button>
                </div>
              </li>
            ),
          )}
        </ul>
      )}
    </section>
  )
}

// How a credential's secret goes on each request, in a few words.
function howItGoes(c: Credential): string {
  switch (c.auth) {
    case 'header':
      return `${c.header}: ${c.template}`
    case 'basic':
      return `basic auth as ${c.username || 'no username'}`
    case 'query':
      return `?${c.param}={secret}`
  }
}

// A URL's scheme, host and port, as the server compares them.
function originOf(raw: string): string {
  try {
    const u = new URL(raw.trim())
    return `${u.protocol}//${u.hostname}:${u.port || (u.protocol === 'https:' ? '443' : '80')}`
  } catch {
    return raw.trim()
  }
}

// What a name is as a variable's, as the server makes it.
const variableOf = (name: string) =>
  name
    .toUpperCase()
    .replace(/[^A-Z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '')

// Adds a credential, or changes one; its secret shows only as set.
function CredentialEditor({
  initial,
  teams,
  signIn,
  onSave,
  onCancel,
}: {
  initial?: Credential
  teams: Team[]
  signIn: boolean
  onSave: (fields: CredentialFields) => Promise<void>
  onCancel: () => void
}) {
  const [name, setName] = useState(initial?.name ?? '')
  const [description, setDescription] = useState(initial?.description ?? '')
  const [baseUrl, setBaseUrl] = useState(initial?.baseUrl ?? '')
  const [auth, setAuth] = useState<CredentialAuth>(initial?.auth ?? 'header')
  const [header, setHeader] = useState(initial?.header || 'Authorization')
  const [template, setTemplate] = useState(initial?.template || 'Bearer {secret}')
  const [username, setUsername] = useState(initial?.username ?? '')
  const [param, setParam] = useState(initial?.param ?? '')
  const [headers, setHeaders] = useState((initial?.headers ?? []).join(', '))
  const [secret, setSecret] = useState('')
  const [everyone, setEveryone] = useState(initial?.everyone ?? true)
  const [given, setGiven] = useState<number[]>(initial?.teams.map((t) => t.id) ?? [])
  const [saving, setSaving] = useState(false)
  // A kept secret never goes elsewhere than it was given for.
  const elsewhere =
    !!initial &&
    (originOf(baseUrl) !== originOf(initial.baseUrl) ||
      auth !== initial.auth ||
      (auth === 'header' && header.trim().toLowerCase() !== (initial.header ?? '').toLowerCase()) ||
      (auth === 'query' && param.trim() !== (initial.param ?? '')))
  const needsSecret = !initial || elsewhere
  const ready =
    name.trim() !== '' &&
    baseUrl.trim() !== '' &&
    (!needsSecret || secret.trim() !== '') &&
    (auth !== 'header' || template.includes('{secret}')) &&
    (auth !== 'query' || param.trim() !== '') &&
    (everyone || given.length > 0)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!ready) return
    setSaving(true)
    const passed = headers
      .split(',')
      .map((h) => h.trim())
      .filter(Boolean)
    await onSave({ name, description, baseUrl, auth, header, template, username, param, headers: passed, secret, everyone, teams: everyone ? [] : given })
    setSaving(false)
  }

  return (
    <form className="skill-editor" onSubmit={submit} onKeyDown={(e) => e.key === 'Escape' && onCancel()}>
      <div className="panel-row">
        <label className="panel-field">
          <span>Name</span>
          <input autoFocus value={name} placeholder="Jira" onChange={(e) => setName(e.target.value)} />
          <small className="panel-note">
            {variableOf(name) ? (
              <>
                Steps reach it at <code>${credentialEnv(variableOf(name))}</code>.
              </>
            ) : (
              'Letters, digits, spaces, hyphens and underscores, starting with a letter.'
            )}
          </small>
        </label>
        <label className="panel-field">
          <span>Base URL</span>
          <input value={baseUrl} placeholder="https://example.atlassian.net" spellCheck={false} onChange={(e) => setBaseUrl(e.target.value)} />
          <small className="panel-note">https; steps reach only paths under it. Its host needn’t be allowed for internet access.</small>
        </label>
      </div>
      <label className="panel-field">
        <span>Description (optional)</span>
        <input value={description} placeholder="What the service is, which agents are told" maxLength={1024} onChange={(e) => setDescription(e.target.value)} />
      </label>
      <div className="panel-row">
        <label className="panel-field">
          <span>The secret goes</span>
          <select value={auth} onChange={(e) => setAuth(e.target.value as CredentialAuth)}>
            <option value="header">In a header</option>
            <option value="basic">As basic auth’s password</option>
            <option value="query">As a query parameter</option>
          </select>
        </label>
        {auth === 'header' && (
          <>
            <label className="panel-field">
              <span>Header</span>
              <input value={header} placeholder="Authorization" spellCheck={false} onChange={(e) => setHeader(e.target.value)} />
            </label>
            <label className="panel-field">
              <span>Its value</span>
              <input value={template} placeholder="Bearer {secret}" spellCheck={false} onChange={(e) => setTemplate(e.target.value)} />
              <small className="panel-note">
                <code>{'{secret}'}</code> stands for the secret, such as <code>token {'{secret}'}</code>.
              </small>
            </label>
          </>
        )}
        {auth === 'basic' && (
          <label className="panel-field">
            <span>Username</span>
            <input value={username} placeholder="me@example.com" spellCheck={false} onChange={(e) => setUsername(e.target.value)} />
          </label>
        )}
        {auth === 'query' && (
          <label className="panel-field">
            <span>Parameter</span>
            <input value={param} placeholder="api_key" spellCheck={false} onChange={(e) => setParam(e.target.value)} />
          </label>
        )}
      </div>
      <label className="panel-field">
        <span>Headers steps may send (optional)</span>
        <input value={headers} placeholder="Notion-Version, X-Atlassian-Token" spellCheck={false} onChange={(e) => setHeaders(e.target.value)} />
        <small className="panel-note">
          Besides the usual ones, such as Accept, Content-Type, If-Match and User-Agent; the rest of a step’s headers stay
          with the proxy.
        </small>
      </label>
      <label className="panel-field">
        <span>Secret</span>
        <input
          type="password"
          autoComplete="off"
          value={secret}
          placeholder={
            initial?.secretSet && !elsewhere ? `Kept${initial.hint ? ` (…${initial.hint})` : ''}; type to replace it` : 'The token or key'
          }
          onChange={(e) => setSecret(e.target.value)}
        />
        <small className="panel-note">
          {elsewhere
            ? 'Give it again: the kept one never goes to another service, or another place on its requests, than it was given for.'
            : 'Sealed in the database and never shown again.'}
        </small>
      </label>
      <fieldset className="panel-field mcp-picker">
        <legend>Whose flows may use it</legend>
        <label className="panel-check">
          <input type="checkbox" checked={everyone} disabled={!signIn} onChange={(e) => setEveryone(e.target.checked)} />
          <span>Everyone’s</span>
        </label>
        {!signIn ? (
          <small className="panel-note">Nobody signs in to this app, so every flow is everyone’s.</small>
        ) : (
          !everyone && (
            <>
              {teams.length === 0 && <small className="panel-note">There are no teams yet.</small>}
              {teams.map((t) => (
                <div key={t.id} className="mcp-pick">
                  <label className="panel-check">
                    <input
                      type="checkbox"
                      checked={given.includes(t.id)}
                      onChange={(e) => setGiven(e.target.checked ? [...given, t.id] : given.filter((id) => id !== t.id))}
                    />
                    <span>{t.name}</span>
                  </label>
                </div>
              ))}
              <small className="panel-note">The flows of these teams and their members’ own.</small>
            </>
          )
        )}
      </fieldset>
      <div className="skill-actions">
        <button type="submit" className="skills-new" disabled={!ready || saving}>
          {initial ? 'Save' : 'Add credential'}
        </button>
        <button type="button" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  )
}

// The credentials an agent or script process calls services with, picked
// from those its flow may use, each with the variable its step reaches it at.
export function CredentialPicker({
  credentials,
  selected,
  readOnly,
  onChange,
}: {
  // The credentials the flow may use, or null while they load.
  credentials: CredentialChoice[] | null
  selected: number[]
  readOnly: boolean
  onChange: (ids: number[]) => void
}) {
  // Picked ones since removed, or no longer the flow's to use, stay listed so they can be dropped.
  const missing = credentials ? selected.filter((id) => !credentials.some((c) => c.id === id)) : []
  return (
    <fieldset className="panel-field mcp-picker">
      <legend>Credentials</legend>
      {credentials?.length === 0 && <p className="panel-note">None this flow may use; admins add them in Settings, under Credentials.</p>}
      {credentials?.map((c) => (
        <div key={c.id} className="mcp-pick" title={c.description || c.baseUrl}>
          <label className="panel-check">
            <input
              type="checkbox"
              checked={selected.includes(c.id)}
              disabled={readOnly}
              onChange={(e) => onChange(e.target.checked ? [...selected, c.id] : selected.filter((id) => id !== c.id))}
            />
            <span>{c.name}</span>
          </label>
          {selected.includes(c.id) && <code className="panel-note">${credentialEnv(c.variable)}</code>}
        </div>
      ))}
      {selected.length > 0 && (
        <small className="panel-note">
          Call each at its variable’s URL with <code>Authorization: Bearer $CREDENTIALS_TOKEN</code>.
        </small>
      )}
      {missing.length > 0 && (
        <div className="mcp-pick">
          <small className="panel-warning">
            {missing.length === 1
              ? 'A credential it picked was removed or isn’t this flow’s to use.'
              : `${missing.length} credentials it picked were removed or aren’t this flow’s to use.`}
          </small>
          {!readOnly && (
            <button className="link-button" onClick={() => onChange(selected.filter((id) => !missing.includes(id)))}>
              Drop
            </button>
          )}
        </div>
      )}
    </fieldset>
  )
}
