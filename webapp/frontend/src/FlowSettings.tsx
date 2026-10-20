import { useState, type ReactNode } from 'react'
import type { FlowSettings, Repository } from './api'
import { FlowAPISection } from './APIKeys'

// What a repository is called in the workspace, from its URL: the last part
// of its path, without .git.
export function nameFromURL(url: string): string {
  const last = url.trim().replace(/\/+$/, '').split('/').pop() ?? ''
  return last.replace(/\.git$/, '')
}

// A GitHub repository's URL from org/repository, also taking a pasted
// github.com URL; empty if it isn't one.
export function githubURL(v: string): string {
  const path = v
    .trim()
    .replace(/^(https?:\/\/)?(www\.)?github\.com\//, '')
    .replace(/\.git$/, '')
    .replace(/\/+$/, '')
  return /^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(path) ? `https://github.com/${path}.git` : ''
}

// org/repository of a GitHub URL, or undefined for another host.
const githubPath = (url: string) => /^https:\/\/github\.com\/([^/]+\/[^/]+?)(\.git)?$/.exec(url)?.[1]

// The flow's settings: the repositories its agents and scripts work on, how
// its runs' branches are named, and how other systems start it. Each run checks the repositories out on
// a branch of its own, which every process of the run works on.
export function FlowSettingsPanel({
  flowId,
  owner,
  settings,
  module,
  usedBy,
  defaultPrefix,
  readOnly,
  onSave,
  onModuleChange,
  onClose,
  onError,
}: {
  flowId: number
  // Who sees it, to pick another; see Owner.tsx.
  owner?: ReactNode
  settings: FlowSettings
  // Whether other flows reuse it, and how many do.
  module: boolean
  usedBy: number
  onModuleChange: (module: boolean) => Promise<void>
  // The prefix used when none is set.
  defaultPrefix: string
  readOnly: boolean
  onSave: (s: FlowSettings) => Promise<void>
  onClose: () => void
  onError: (err: unknown) => void
}) {
  const [host, setHost] = useState<'github' | 'other'>('github')
  // org/repository on GitHub, the whole URL otherwise.
  const [where, setWhere] = useState('')
  const [base, setBase] = useState('')
  const [prefix, setPrefix] = useState(settings.branchPrefix)
  const [saving, setSaving] = useState(false)
  const url = host === 'github' ? githubURL(where) : where.trim()
  const urlOk = /^https:\/\/\S+$/.test(url)
  const name = nameFromURL(url)
  const taken = settings.repositories.some((r) => r.name === name)

  const setModule = async (m: boolean) => {
    setSaving(true)
    try {
      await onModuleChange(m)
    } catch (err) {
      onError(err)
    } finally {
      setSaving(false)
    }
  }

  const save = async (s: FlowSettings) => {
    setSaving(true)
    try {
      await onSave(s)
      return true
    } catch (err) {
      onError(err)
      return false
    } finally {
      setSaving(false)
    }
  }

  const add = async () => {
    const repo: Repository = { name, url, base: base.trim() }
    if (await save({ ...settings, repositories: [...settings.repositories, repo] })) {
      setWhere('')
      setBase('')
    }
  }

  const savePrefix = () => {
    if (prefix.trim() !== settings.branchPrefix) save({ ...settings, branchPrefix: prefix.trim() })
  }

  const branchPrefix = settings.branchPrefix || defaultPrefix

  return (
    <aside className="panel flow-settings-panel" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <header className="panel-header">
        <h2>Flow settings</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close flow settings">
          ×
        </button>
      </header>
      {owner}
      <label className="panel-check">
        <input
          type="checkbox"
          checked={module}
          // A module others run stays one until they no longer do.
          disabled={readOnly || saving || (module && usedBy > 0)}
          onChange={(e) => setModule(e.target.checked)}
        />
        <span>Procedure: a group of processes reused inside other flows</span>
      </label>
      <p className="panel-note">
        {module
          ? usedBy > 0
            ? `It is offered in other flows' sidebars, and ${usedBy} ${usedBy === 1 ? 'flow runs' : 'flows run'} it; take it out of them to make it a flow of its own again.`
            : "It is offered in other flows' sidebars, and listed with the procedures on the main page."
          : 'A flow of its own, listed with the flows that run. Make it a procedure to reuse it inside others.'}
      </p>
      <h3 className="panel-subheading">Repositories</h3>
      <p className="panel-note">
        The repositories this flow's agents and scripts work on. Each run checks them out on a branch of its own,{' '}
        <code>{branchPrefix}/run-&lt;id&gt;</code>, and every process of the run works on that branch. Each item of a list
        gets a branch of its own. Pick in a process's panel which of them it works on.
      </p>
      {settings.repositories.length === 0 ? (
        <p className="panel-note">None yet; agents and scripts run in an empty directory.</p>
      ) : (
        <ul className="repo-list">
          {settings.repositories.map((r) => (
            <li key={r.name} className="repo-item">
              <div className="repo-item-head">
                <span className="repo-item-name">{r.name}</span>
                <span className="repo-item-base">{r.base || 'default branch'}</span>
              </div>
              <span className="repo-item-url" title={r.url}>
                {githubPath(r.url) ?? r.url}
              </span>
              {!readOnly && (
                <div className="mcp-item-actions">
                  <button
                    className="danger"
                    disabled={saving}
                    onClick={() =>
                      save({ ...settings, repositories: settings.repositories.filter((x) => x.name !== r.name) })
                    }
                  >
                    Remove
                  </button>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
      {!readOnly && (
        <form
          className="mcp-add"
          onSubmit={(e) => {
            e.preventDefault()
            if (urlOk && name && !taken && !saving) add()
          }}
        >
          <h3>Add a repository</h3>
          <div className="panel-field" role="radiogroup" aria-label="Host">
            <span>Host</span>
            <div className="segmented">
              {(
                [
                  ['github', 'GitHub'],
                  ['other', 'Other'],
                ] as const
              ).map(([value, label]) => (
                <button
                  key={value}
                  type="button"
                  role="radio"
                  aria-checked={host === value}
                  className={host === value ? 'selected' : ''}
                  onClick={() => {
                    setHost(value)
                    setWhere('')
                  }}
                >
                  {label}
                </button>
              ))}
            </div>
          </div>
          {host === 'github' ? (
            <label className="panel-field">
              <span>Repository</span>
              <input
                value={where}
                placeholder="org/repository"
                spellCheck={false}
                onChange={(e) => setWhere(e.target.value)}
              />
              {where.trim() !== '' && !urlOk && <small className="panel-warning">Write it as org/repository.</small>}
              {urlOk && taken && <small className="panel-warning">There's a repository named {name} already.</small>}
            </label>
          ) : (
            <label className="panel-field">
              <span>URL</span>
              <input
                value={where}
                placeholder="https://git.example.com/org/repository.git"
                spellCheck={false}
                onChange={(e) => setWhere(e.target.value)}
              />
              {where.trim() !== '' && !urlOk && <small className="panel-warning">Only https URLs.</small>}
              {urlOk && taken && <small className="panel-warning">There's a repository named {name} already.</small>}
            </label>
          )}
          <label className="panel-field">
            <span>Base branch</span>
            <input
              value={base}
              placeholder="Optional. The default branch if empty."
              spellCheck={false}
              onChange={(e) => setBase(e.target.value)}
            />
          </label>
          <button className="panel-button" type="submit" disabled={!urlOk || !name || taken || saving}>
            Add
          </button>
        </form>
      )}
      <h3 className="panel-subheading">Branches</h3>
      <label className="panel-field">
        <span>Branch prefix</span>
        <input
          value={prefix}
          readOnly={readOnly}
          placeholder={defaultPrefix}
          spellCheck={false}
          onChange={(e) => setPrefix(e.target.value)}
          onBlur={savePrefix}
          onKeyDown={(e) => e.key === 'Enter' && savePrefix()}
        />
        <small className="panel-note">Runs started from now on use it; runs going on keep their branch.</small>
      </label>
      <FlowAPISection flowId={flowId} onError={onError} />
    </aside>
  )
}

// The flow's repositories a process works on: all of them unless picked.
export function RepositoryPicker({
  repositories,
  selected,
  readOnly,
  onChange,
  onOpenSettings,
}: {
  repositories: Repository[]
  // Undefined for all of them, which takes ones added later too.
  selected: string[] | undefined
  readOnly: boolean
  onChange: (names: string[] | undefined) => void
  onOpenSettings: () => void
}) {
  const all = selected === undefined
  const missing = selected?.filter((n) => !repositories.some((r) => r.name === n)) ?? []
  return (
    <fieldset className="panel-field mcp-picker">
      <legend>Repositories</legend>
      {repositories.length === 0 ? (
        <p className="panel-note">
          The flow has none.{' '}
          <button className="link-button" onClick={onOpenSettings}>
            Add repositories
          </button>
        </p>
      ) : (
        <>
          <label className="panel-check">
            <input
              type="checkbox"
              checked={all}
              disabled={readOnly}
              onChange={(e) => onChange(e.target.checked ? undefined : repositories.map((r) => r.name))}
            />
            <span>Every repository of the flow</span>
          </label>
          {!all &&
            repositories.map((r) => {
              const on = selected.includes(r.name)
              return (
                <div key={r.name} className="mcp-pick">
                  <label className="panel-check">
                    <input
                      type="checkbox"
                      checked={on}
                      disabled={readOnly}
                      onChange={(e) =>
                        onChange(e.target.checked ? [...selected, r.name] : selected.filter((n) => n !== r.name))
                      }
                    />
                    <span>{r.name}</span>
                  </label>
                </div>
              )
            })}
          {!all && selected.length === 0 && (
            <small className="panel-note">It works on no repository, in an empty directory.</small>
          )}
        </>
      )}
      {missing.length > 0 && (
        <div className="mcp-pick">
          <small className="panel-warning">
            {missing.length === 1 ? 'A repository it works on was removed.' : `${missing.length} repositories it works on were removed.`}
          </small>
          {!readOnly && (
            <button className="link-button" onClick={() => onChange(selected!.filter((n) => !missing.includes(n)))}>
              Drop
            </button>
          )}
        </div>
      )}
    </fieldset>
  )
}
