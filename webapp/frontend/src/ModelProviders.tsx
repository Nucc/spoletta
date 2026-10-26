import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import {
  addModelProvider,
  CUSTOM_APIS,
  deleteModelKey,
  deleteModelProvider,
  listModelKeys,
  listModelProviders,
  listModels,
  listProviderModels,
  providerCatalog,
  setDefaultModel,
  setModelKey,
  updateModelProvider,
  type CatalogModel,
  type CatalogProvider,
  type KeySource,
  type ModelKey,
  type ModelKeys,
  type ModelProvider,
  type ModelProviders,
  type Models,
} from './api'
import { formatTime } from './Runs'
import { useConfirm } from './Confirm'

// Where agents' models come from, which admins set up: the providers added
// here, each with the models picked of it and its key, saved here or kept
// in a vault that puts it in the webapp's environment; the default model;
// and the keys of the worker's own pi config. Agents never get the keys:
// they reach their models through the webapp, which holds them.
export function ModelProvidersSection({ onError }: { onError: (err: unknown) => void }) {
  const [providers, setProviders] = useState<ModelProviders | null>(null)
  const [models, setModels] = useState<Models | null>(null)
  const [editing, setEditing] = useState<ModelProvider | 'new' | null>(null)
  const refreshModels = useCallback(() => listModels().then(setModels, () => setModels(null)), [])
  const refresh = useCallback(() => {
    listModelProviders().then(setProviders, onError)
    refreshModels()
  }, [onError, refreshModels])
  useEffect(() => {
    refresh()
  }, [refresh])

  const changed = (p: ModelProviders) => {
    setProviders(p)
    refreshModels()
  }
  const [confirm, confirmDialog] = useConfirm()
  const remove = async (p: ModelProvider) => {
    const yes = await confirm({
      title: `Remove ${p.name || p.id}?`,
      message: <p>Agents using its models fail to run until they pick others, and a key saved for it goes. This can't be undone.</p>,
      confirm: 'Remove',
    })
    if (!yes) return
    try {
      changed(await deleteModelProvider(p.id))
    } catch (err) {
      onError(err)
    }
  }
  const pickDefault = async (model: string) => {
    try {
      changed(await setDefaultModel(model))
    } catch (err) {
      onError(err)
    }
  }

  return (
    <section className="flow-section" aria-label="Model providers">
      {confirmDialog}
      <h2>Model providers</h2>
      <p className="flows-note">
        Where agents' models come from. Add a provider with its key, or your own AI gateway as a custom provider; its
        key is saved here, or kept in a vault that puts it in the webapp's environment, and never shows again. Agents
        reach their models through the webapp and never see a key.
      </p>
      {providers?.workerError && <p className="panel-warning">{providers.workerError}</p>}
      {providers === null ? (
        <p className="flows-note">Loading…</p>
      ) : (
        providers.providers.length > 0 && (
          <ul className="flow-list key-list">
            {providers.providers.map((p) => (
              <ProviderItem key={p.id} provider={p} onEdit={() => setEditing(p)} onRemove={() => remove(p)} />
            ))}
          </ul>
        )
      )}
      <div className="provider-actions">
        <button type="button" className="provider-add" onClick={() => setEditing('new')}>
          Add provider
        </button>
      </div>
      {/* With a single model, it is the default, and needn't show. */}
      {models && models.models.length > 1 && (
        <label className="panel-field default-model">
          <span>Default model</span>
          <span className="flows-note">What agents of processes without a model of their own use.</span>
          <select value={models.default} onChange={(e) => pickDefault(e.target.value)}>
            {!models.models.includes(models.default) && <option value={models.default}>{models.default || 'pi’s own'}</option>}
            <ModelOptions models={models} />
          </select>
        </label>
      )}
      <WorkerKeys onError={onError} />
      {editing && providers && (
        <ProviderDialog
          provider={editing === 'new' ? null : editing}
          added={providers.providers}
          offered={models?.models ?? []}
          onClose={() => setEditing(null)}
          onSaved={(p) => {
            changed(p)
            setEditing(null)
          }}
        />
      )}
    </section>
  )
}

// The models offered as options, grouped by provider when there are several.
export function ModelOptions({ models }: { models: Models }) {
  const groups = new Map<string, string[]>()
  for (const m of models.models) {
    const provider = m.split('/')[0]
    groups.set(provider, [...(groups.get(provider) ?? []), m])
  }
  if (groups.size < 2) {
    return models.models.map((m) => (
      <option key={m} value={m}>
        {m}
      </option>
    ))
  }
  return [...groups].map(([provider, ms]) => (
    <optgroup key={provider} label={models.providers?.[provider] ?? provider}>
      {ms.map((m) => (
        <option key={m} value={m}>
          {m.slice(provider.length + 1)}
        </option>
      ))}
    </optgroup>
  ))
}

function apiLabel(api?: string): string {
  return CUSTOM_APIS.find((a) => a.value === api)?.label ?? api ?? ''
}

// Where a provider's key comes from, said on its row.
function providerKeyStatus(p: ModelProvider): { text: string; missing: boolean } {
  if (p.keySource === 'saved') {
    return { text: `Key saved here, …${p.hint ?? ''} · ${formatTime(p.updatedAt)}`, missing: false }
  }
  if (p.envSet === false) {
    return { text: `Set ${p.env} in the webapp’s environment: its models fail until then`, missing: true }
  }
  return { text: `Key from ${p.env} in the webapp’s environment`, missing: false }
}

function ProviderItem({ provider: p, onEdit, onRemove }: { provider: ModelProvider; onEdit: () => void; onRemove: () => void }) {
  const status = providerKeyStatus(p)
  return (
    <li>
      <div className="key-item">
        <span className="key-provider">
          <span className="flow-card-name">{p.name}</span>
          <span className="flows-note">{p.baseUrl ? `${apiLabel(p.api)} at ${p.baseUrl}` : p.id}</span>
          <span className={`key-status${status.missing ? ' missing' : ''}`}>{status.text}</span>
        </span>
        <span className="provider-models flows-note" title={p.models.join('\n')}>
          {p.models.length === 1 ? '1 model: ' : `${p.models.length} models: `}
          {p.models.join(', ')}
        </span>
        <button type="button" onClick={onEdit}>
          Edit
        </button>
        <button type="button" className="danger" onClick={onRemove}>
          Remove
        </button>
      </div>
    </li>
  )
}

// What a custom provider reads its key from the environment by default, as
// the server names it, by its ID or its name: "Company gateway", whose ID is
// company-gateway, reads COMPANY_GATEWAY_API_KEY.
function customEnv(idOrName: string): string {
  const slug = idOrName
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 48)
    .replace(/-+$/, '')
  return slug ? `${slug.replace(/-/g, '_').toUpperCase()}_API_KEY` : ''
}

// How much a model takes in: 1M, 200K.
function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${+(n / 1_000_000).toFixed(1)}M`
  if (n >= 1000) return `${Math.round(n / 1000)}K`
  return String(n)
}

// Adding a provider, picking which first, or changing one: its key, saved
// or from the webapp's environment, and its models, picked from pi's
// catalog or, for a custom provider, listed by their IDs. Adding one pi
// knows replaces the worker's own models of it, which it says.
function ProviderDialog({
  provider,
  added,
  offered,
  onClose,
  onSaved,
}: {
  provider: ModelProvider | null
  added: ModelProvider[]
  offered: string[]
  onClose: () => void
  onSaved: (p: ModelProviders) => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    ref.current?.showModal()
  }, [])
  const [catalog, setCatalog] = useState<CatalogProvider[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const report = (err: unknown) => setError(err instanceof Error ? err.message : String(err))
  useEffect(() => {
    providerCatalog().then((c) => setCatalog(c.providers), report)
  }, [])

  // Which provider: one pi knows by its ID, 'custom', or none picked yet.
  const [picked, setPicked] = useState<string | null>(provider ? (provider.baseUrl ? 'custom' : provider.id) : null)
  const known = catalog?.find((c) => c.id === picked)
  const custom = picked === 'custom'
  const [name, setName] = useState(provider?.name ?? '')
  const [api, setApi] = useState(provider?.api ?? CUSTOM_APIS[0].value)
  const [baseUrl, setBaseUrl] = useState(provider?.baseUrl ?? '')
  const [keySource, setKeySource] = useState<KeySource>(provider?.keySource ?? 'saved')
  const [key, setKey] = useState('')
  const [env, setEnv] = useState(provider?.keySource === 'env' ? provider.env : '')
  const [models, setModels] = useState<string[]>(provider?.models ?? [])
  const [listed, setListed] = useState(provider?.baseUrl ? provider.models.join('\n') : '')
  const [saving, setSaving] = useState(false)
  const [fetching, setFetching] = useState(false)

  const defaultEnv = known?.env ?? (custom ? customEnv(provider ? provider.id : name) : '')
  const replaced = !provider && known ? offered.filter((m) => m.startsWith(`${known.id}/`)).length : 0
  const keptKey = provider?.keySource === 'saved' && keySource === 'saved'
  const ids = custom
    ? listed
        .split(/[\n,]/)
        .map((m) => m.trim())
        .filter(Boolean)
    : models

  const input = () => ({
    id: provider ? undefined : custom ? undefined : picked!,
    name: custom ? name : undefined,
    api: custom ? api : undefined,
    baseUrl: custom ? baseUrl : undefined,
    keySource,
    env: keySource === 'env' ? env.trim() || defaultEnv : undefined,
    key: keySource === 'saved' ? key : undefined,
    models: ids,
  })
  const save = async (e: FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setError(null)
    try {
      onSaved(provider ? await updateModelProvider(provider.id, input()) : await addModelProvider(input()))
    } catch (err) {
      report(err)
      setSaving(false)
    }
  }
  const fetchModels = async () => {
    setFetching(true)
    setError(null)
    try {
      const got = await listProviderModels({ ...input(), id: provider?.id })
      const have = new Set(ids)
      setListed([...ids, ...got.models.filter((m) => !have.has(m))].join('\n'))
    } catch (err) {
      report(err)
    } finally {
      setFetching(false)
    }
  }

  const title = provider ? provider.name : known ? `Add ${known.name}` : custom ? 'Add a custom provider' : 'Add a provider'
  return (
    <dialog
      ref={ref}
      className="review-dialog provider-dialog"
      aria-label={title}
      onClose={onClose}
      onKeyDown={(e) => e.stopPropagation()}
    >
      <header className="panel-header">
        <h2>{title}</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      {picked === null ? (
        <div className="provider-body">
          {catalog === null && !error && <p className="flows-note">Loading…</p>}
          <div className="provider-choices">
            {catalog
              ?.filter((c) => !added.some((a) => a.id === c.id))
              .map((c) => (
                <button key={c.id} type="button" className="provider-choice" onClick={() => setPicked(c.id)}>
                  <span className="flow-card-name">{c.name}</span>
                  <span className="flows-note">{c.models.length} models</span>
                </button>
              ))}
            <button type="button" className="provider-choice" onClick={() => setPicked('custom')}>
              <span className="flow-card-name">Custom provider</span>
              <span className="flows-note">Your own AI gateway, by its URL</span>
            </button>
          </div>
          {error && <p className="panel-warning">{error}</p>}
        </div>
      ) : (
        <form className="provider-body" onSubmit={save}>
          {added.some((a) => a.id === picked) && !provider && <p className="panel-warning">It is added already.</p>}
          {replaced > 0 && (
            <p className="panel-warning">
              The worker's own pi config has {replaced === 1 ? 'a model' : `${replaced} models`} of {known!.id}, which this
              replaces: processes using {replaced === 1 ? 'it' : 'them'} need one picked here. To reach your own gateway
              instead, add a custom provider.
            </p>
          )}
          {custom && (
            <>
              <label className="panel-field">
                <span>Name</span>
                <input value={name} onChange={(e) => setName(e.target.value)} placeholder="Company gateway" autoFocus={!provider} />
              </label>
              <label className="panel-field">
                <span>API it speaks</span>
                <select value={api} onChange={(e) => setApi(e.target.value)}>
                  {CUSTOM_APIS.map((a) => (
                    <option key={a.value} value={a.value}>
                      {a.label}
                    </option>
                  ))}
                </select>
              </label>
              <label className="panel-field">
                <span>URL</span>
                <input
                  type="url"
                  spellCheck={false}
                  value={baseUrl}
                  onChange={(e) => setBaseUrl(e.target.value)}
                  placeholder={api === 'bedrock-converse-stream' ? 'https://gateway.example.com/bedrock' : 'https://gateway.example.com/v1'}
                />
              </label>
            </>
          )}
          <div className="panel-field">
            <span>Key</span>
            <div className="segmented" role="radiogroup" aria-label="Where the key comes from">
              {(
                [
                  ['saved', 'Save it here'],
                  ['env', 'From the environment'],
                ] as const
              ).map(([value, label]) => (
                <button
                  key={value}
                  type="button"
                  role="radio"
                  aria-checked={keySource === value}
                  className={keySource === value ? 'selected' : ''}
                  onClick={() => setKeySource(value)}
                >
                  {label}
                </button>
              ))}
            </div>
            {keySource === 'saved' ? (
              <input
                type="password"
                autoComplete="off"
                data-1p-ignore
                data-lpignore="true"
                spellCheck={false}
                aria-label="Key"
                placeholder={keptKey ? `Saved, …${provider?.hint ?? ''}; paste to replace it` : 'Paste the key'}
                value={key}
                onChange={(e) => setKey(e.target.value)}
              />
            ) : (
              <>
                <input
                  className="key-env"
                  spellCheck={false}
                  aria-label="Variable"
                  placeholder={defaultEnv || 'COMPANY_GATEWAY_API_KEY'}
                  value={env}
                  onChange={(e) => setEnv(e.target.value.toUpperCase())}
                />
                <span className="flows-note">
                  Set <code>{env.trim() || defaultEnv || 'the variable'}</code> in the webapp's environment, from your vault
                  or secret store; the key never comes here.
                  {provider?.keySource === 'env' &&
                    (env.trim() || defaultEnv) === provider.env &&
                    provider.envSet !== undefined &&
                    (provider.envSet ? ' The webapp has it.' : ' The webapp doesn’t have it yet.')}
                </span>
              </>
            )}
          </div>
          {custom ? (
            <label className="panel-field">
              <span className="provider-models-label">
                Models, by their IDs, one per line
                {api !== 'bedrock-converse-stream' && (
                  <button type="button" onClick={fetchModels} disabled={fetching || !baseUrl.trim()}>
                    {fetching ? 'Asking…' : 'Ask the provider'}
                  </button>
                )}
              </span>
              <textarea
                className="provider-ids"
                rows={6}
                spellCheck={false}
                value={listed}
                onChange={(e) => setListed(e.target.value)}
                placeholder={api === 'bedrock-converse-stream' ? 'global.anthropic.claude-sonnet-4-6' : 'gpt-5'}
              />
            </label>
          ) : (
            known && <ModelPicker models={known.models} picked={models} onChange={setModels} />
          )}
          {error && <p className="panel-warning">{error}</p>}
          <div className="review-actions">
            {!provider && (
              <button type="button" onClick={() => setPicked(null)}>
                Back
              </button>
            )}
            <button type="button" onClick={onClose}>
              Cancel
            </button>
            <button type="submit" disabled={saving || ids.length === 0}>
              {saving ? 'Saving…' : provider ? 'Save' : 'Add provider'}
            </button>
          </div>
        </form>
      )}
    </dialog>
  )
}

// Picking which of a provider's models agents may use, found by name or ID.
function ModelPicker({ models, picked, onChange }: { models: CatalogModel[]; picked: string[]; onChange: (ids: string[]) => void }) {
  const [search, setSearch] = useState('')
  const shown = useMemo(() => {
    const words = search.toLowerCase().split(/\s+/).filter(Boolean)
    return models.filter((m) => words.every((w) => `${m.id} ${m.name}`.toLowerCase().includes(w)))
  }, [models, search])
  const toggle = (id: string, on: boolean) => onChange(on ? [...picked, id] : picked.filter((p) => p !== id))
  const allShown = shown.length > 0 && shown.every((m) => picked.includes(m.id))
  return (
    <div className="panel-field">
      <span className="provider-models-label">
        Models agents may use · {picked.length} picked
        <button
          type="button"
          disabled={shown.length === 0}
          onClick={() =>
            onChange(allShown ? picked.filter((p) => !shown.some((m) => m.id === p)) : [...new Set([...picked, ...shown.map((m) => m.id)])])
          }
        >
          {allShown ? 'Untick these' : 'Tick these'}
        </button>
      </span>
      <input type="search" placeholder="Find a model" value={search} onChange={(e) => setSearch(e.target.value)} />
      <ul className="provider-model-list">
        {shown.map((m) => (
          <li key={m.id}>
            <label className="panel-check">
              <input type="checkbox" checked={picked.includes(m.id)} onChange={(e) => toggle(m.id, e.target.checked)} />
              <span className="provider-model-name">{m.name}</span>
              <span className="flows-note">
                {m.id} · {formatTokens(m.contextWindow)}
                {m.reasoning ? ' · thinks' : ''}
                {m.input?.includes('image') ? ' · sees images' : ''}
              </span>
            </label>
          </li>
        ))}
        {shown.length === 0 && <li className="flows-note">No model matches.</li>}
      </ul>
    </div>
  )
}

// The keys of the worker's own pi config, which admins save here or set in
// the webapp's environment: one for each variable its models read a key
// from, which several providers may share, as a gateway's do.
function WorkerKeys({ onError }: { onError: (err: unknown) => void }) {
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
  const [confirm, confirmDialog] = useConfirm()
  const remove = async (env: string) => {
    const yes = await confirm({
      title: `Remove the key in ${env}?`,
      message: <p>Agents reach its provider with the webapp's environment's key then, if it has one. This can't be undone.</p>,
      confirm: 'Remove',
    })
    if (!yes) return
    try {
      await deleteModelKey(env)
      await refresh()
    } catch (err) {
      onError(err)
    }
  }

  const settable = keys?.keys.filter((k) => k.env !== '') ?? []
  const elsewhere = keys?.keys.filter((k) => k.env === '').flatMap((k) => k.providers) ?? []
  if (keys === null || (settable.length === 0 && elsewhere.length === 0 && !keys.workerError)) return null
  return (
    <div className="worker-keys">
      {confirmDialog}
      <h3>From the worker's pi config</h3>
      <p className="flows-note">
        The models of the worker's own models.json and settings.json, of providers not added above, and the keys they
        read, which agents reach them with through the webapp. A key saved here goes over the webapp's environment's.
      </p>
      {keys.workerError && <p className="panel-warning">{keys.workerError}</p>}
      {settable.length > 0 && (
        <ul className="flow-list key-list">
          {settable.map((k) => (
            <KeyItem key={k.env} modelKey={k} onSave={(key) => save(k.env, key)} onRemove={() => remove(k.env)} />
          ))}
        </ul>
      )}
      {elsewhere.length > 0 && (
        <p className="flows-note">
          {elsewhere.join(', ')} {elsewhere.length === 1 ? 'reads its key' : 'read their keys'} from no variable, so
          agents can't reach {elsewhere.length === 1 ? 'it' : 'them'}: add {elsewhere.length === 1 ? 'it' : 'them'} above
          with a key. Amazon Bedrock without a key signs in with the worker's cloud credentials.
        </p>
      )}
    </div>
  )
}

// Where a key comes from, said on its row.
function keyStatus(k: ModelKey): string {
  if (k.saved) return `Saved here, …${k.hint ?? ''}${k.updatedAt ? ` · ${formatTime(k.updatedAt)}` : ''}`
  if (k.set) return 'From the webapp’s environment'
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
