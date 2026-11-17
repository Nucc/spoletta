import { useCallback, useEffect, useState, type FormEvent } from 'react'
import {
  deleteProjectGitToken,
  getGitAccess,
  getProjectGitToken,
  setProjectGitToken,
  type GitAccess,
  type ProjectGitToken,
} from './api'
import { configures, inheritsInstance, useMe } from './Account'
import { useConfirm } from './Confirm'
import { SettingsHead } from './SettingsHead'
import { formatTime } from './Runs'
import TabLink from './TabLink'
import { pageHash } from './address'

// How the steps of a project's runs reach its flows' repositories with git
// (git_tokens.go): each step working on repositories is handed a token for
// them. Where the project gets what the instance is configured with outside
// the app, as Legacy does, that is the instance's GitHub App's token for
// repositories on its GitHub, made for the run's repositories and ending
// within the hour, else the token the project's owners set, else the
// instance's GIT_TOKEN. Another project has only the token its owners set
// here, and without one clones public repositories alone. A flow's settings
// say where its steps' tokens come from (GitAccessNote); the settings' Git
// access tab is where owners set, replace and remove the project's own.

// Where the steps of the page's project get their git tokens, said beside a
// flow's repositories and in the settings: only a GitHub App's are for the
// run's repositories alone and end within the hour. Owners are pointed at
// the settings, where the project's own token is set.
export function GitAccessNote({ access, inSettings = false }: { access: GitAccess; inSettings?: boolean }) {
  const me = useMe()
  const owner = configures(me)
  // Where the project's own token is set, unless this is that place.
  const setIt = !inSettings && owner && (
    <>
      {' '}
      <TabLink hash={pageHash('#/settings/git')} title="Set the project's own git token in Settings, in a new tab">
        Set the project’s git token
      </TabLink>
      .
    </>
  )
  switch (access.source) {
    case 'app':
      return (
        <p className="panel-note">
          Each step gets a GitHub App’s token for the run’s repositories alone, which ends within the hour.
          {access.otherHosts &&
            ' Repositories elsewhere than its GitHub get the project’s own git token, else the instance’s GIT_TOKEN, neither of which is short-lived.'}
        </p>
      )
    case 'project':
      return (
        <p className="panel-note">
          Each step gets the git token this project’s owners set, which isn’t short-lived and reaches whatever it
          reaches, and agents can read it while they work.
          {setIt}
        </p>
      )
    case 'token':
      return (
        <p className="panel-warning">
          Each step gets the instance’s GIT_TOKEN, which isn’t short-lived and reaches whatever it reaches, and agents
          can read it while they work. A GitHub App’s tokens are made for the run’s repositories alone and end within
          the hour{owner ? ', and a token of the project’s own reaches only what you give it' : ''}.
          {setIt}
        </p>
      )
    default:
      return (
        <p className="panel-note">
          {inheritsInstance(me)
            ? 'Neither the instance nor this project has a git token, so only public repositories clone.'
            : 'This project has no git token of its own, and doesn’t get the instance’s, so only public repositories clone.'}
          {owner ? setIt : ' Its owners set one in its settings.'}
        </p>
      )
  }
}

// The project's own git token, for its owners: whether it has one, setting
// it, replacing it and removing it, and where its steps' tokens come from
// as things stand. The token is never shown again once set.
export function GitAccessSection({ inherits, onError }: { inherits: boolean; onError: (err: unknown) => void }) {
  const [access, setAccess] = useState<GitAccess | null>(null)
  const [token, setToken] = useState<ProjectGitToken | null>(null)
  const [draft, setDraft] = useState('')
  const [username, setUsername] = useState('')
  const [saving, setSaving] = useState(false)
  const [confirm, confirmDialog] = useConfirm()

  // The username stays as it is set, so replacing the token keeps it.
  const took = (t: ProjectGitToken) => {
    setToken(t)
    setUsername(t.username ?? '')
  }
  const refresh = useCallback(async () => {
    const [a, t] = await Promise.all([getGitAccess(), getProjectGitToken()])
    setAccess(a)
    took(t)
  }, [])
  useEffect(() => {
    refresh().catch(onError)
  }, [refresh, onError])

  const save = async (e: FormEvent) => {
    e.preventDefault()
    if (!draft.trim()) return
    setSaving(true)
    try {
      took(await setProjectGitToken(draft.trim(), username.trim()))
      setDraft('')
      setAccess(await getGitAccess())
    } catch (err) {
      onError(err)
    } finally {
      setSaving(false)
    }
  }

  const remove = async () => {
    const yes = await confirm({
      title: 'Remove the project’s git token?',
      message: (
        <p>
          {inherits
            ? 'Its steps get the instance’s GitHub App’s token or GIT_TOKEN then, if it has one, else clone only public repositories.'
            : 'Its steps clone only public repositories then, and pushes to private ones fail.'}{' '}
          The token can't be shown again, so setting it back means pasting it again.
        </p>
      ),
      confirm: 'Remove',
    })
    if (!yes) return
    try {
      await deleteProjectGitToken()
      await refresh()
    } catch (err) {
      onError(err)
    }
  }

  return (
    <section className="flow-section git-access" aria-label="Git access">
      {confirmDialog}
      <SettingsHead
        title="Git access"
        lead="The token this project's steps clone and push its flows' repositories with."
        more={
          <>
            A step working on repositories is handed a token for them, and nothing else is. Where the project gets what
            the instance is configured with and the instance has a GitHub App, a step working only on repositories on its
            GitHub gets the app’s token, made for them and ending within the hour; otherwise it gets the token set here,
            else the instance’s GIT_TOKEN. The token is kept sealed and never shown again, and agents and scripts can read
            it while they work, so give it only what the flows need, such as a fine-grained token for their repositories.
          </>
        }
      />
      <p className="flows-note">
        {inherits
          ? 'This project gets what the instance is configured with too, so the instance’s GitHub App and GIT_TOKEN, if it has them, serve where this token doesn’t.'
          : 'This project doesn’t get what the instance is configured with, so it clones private repositories only with the token set here.'}
      </p>
      {access && <GitAccessNote access={access} inSettings />}
      {token !== null && (
        <form className="key-item git-token" onSubmit={(e) => void save(e)}>
          <span className="key-provider">
            <span className="flow-card-name">Project’s token</span>
            <span className={`key-status${token.set ? '' : ' missing'}`}>
              {token.set
                ? `Set${token.hint ? `, …${token.hint}` : ''}, as ${token.username}${token.updatedAt ? ` · ${formatTime(token.updatedAt)}` : ''}`
                : 'None set'}
            </span>
          </span>
          <input
            type="password"
            autoComplete="off"
            data-1p-ignore
            data-lpignore="true"
            spellCheck={false}
            aria-label="Git token"
            placeholder={token.set ? 'Paste a token to replace it' : 'Paste the token'}
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
          />
          <input
            className="git-username"
            autoComplete="off"
            spellCheck={false}
            aria-label="Username it goes with"
            title="The username git sends with the token: x-access-token for GitHub, oauth2 for GitLab, or the account's name"
            placeholder="x-access-token"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
          <button type="submit" disabled={saving || !draft.trim()}>
            {saving ? 'Saving…' : token.set ? 'Replace' : 'Save'}
          </button>
          {token.set && (
            <button type="button" className="danger" disabled={saving} onClick={() => void remove()}>
              Remove
            </button>
          )}
        </form>
      )}
    </section>
  )
}
