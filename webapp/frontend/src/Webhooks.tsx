import { useCallback, useEffect, useRef, useState } from 'react'
import {
  createWebhook,
  deleteWebhook,
  getWebhookDeliveries,
  listWebhooks,
  updateWebhook,
  type Webhook,
  type WebhookDeliveries,
  type WebhookDelivery,
  type WebhookFields,
  type WebhookFlowInput,
  type WebhookInput,
  type WebhookKind,
  type WebhookList,
  type WebhookVerify,
} from './api'
import { formatTime } from './Runs'
import { useConfirm } from './Confirm'

const KIND_LABELS: Record<WebhookKind, string> = { github: 'GitHub', slack: 'Slack', generic: 'Generic' }

const OUTCOME_LABELS: Record<WebhookDelivery['outcome'], string> = {
  started: 'Started',
  repeat: 'Sent again',
  ignored: 'Ignored',
  refused: 'Refused',
}

// What a webhook's fields are, to send back changed.
const fieldsOf = (h: Webhook): WebhookFields => ({
  flow: h.flow,
  name: h.name,
  kind: h.kind,
  verify: h.verify ?? '',
  header: h.header ?? '',
  idHeader: h.idHeader ?? '',
  timestampHeader: h.timestampHeader ?? '',
  events: h.events,
  mapping: h.mapping,
  enabled: h.enabled,
  secret: '',
})

// What came of a delivery, in a few words.
const deliveryText = (d: WebhookDelivery) =>
  [OUTCOME_LABELS[d.outcome], d.run !== null && `run #${d.run}`, d.reason].filter(Boolean).join(' · ')

// A payload as it reads best: JSON indented, anything else as it came.
function readable(payload: string): string {
  try {
    return JSON.stringify(JSON.parse(payload), null, 2)
  } catch {
    return payload
  }
}

// The flow's webhooks, in its settings: other systems' events starting it,
// each at a URL of its own and signed with its secret; for each, where its
// deliveries go, which events it takes, what keeps them from starting a
// run, and what the latest came to. A secret the server makes is shown
// once, as it is made.
export function FlowWebhooksSection({ flowId, onError }: { flowId: number; onError: (err: unknown) => void }) {
  const [list, setList] = useState<WebhookList | null>(null)
  const [editing, setEditing] = useState<Webhook | 'new' | null>(null)
  const [showing, setShowing] = useState<Webhook | null>(null)
  const [made, setMade] = useState<Webhook | null>(null)
  const [confirm, confirmDialog] = useConfirm()

  const refresh = useCallback(async () => setList(await listWebhooks(flowId)), [flowId])

  useEffect(() => {
    refresh().catch(onError)
  }, [refresh, onError])

  const saved = (h: Webhook) => {
    setEditing(null)
    if (h.secret) setMade(h)
    refresh().catch(onError)
  }

  const toggle = async (h: Webhook) => {
    try {
      await updateWebhook(h.id, { ...fieldsOf(h), enabled: !h.enabled })
      await refresh()
    } catch (err) {
      onError(err)
    }
  }

  const remove = async (h: Webhook) => {
    const yes = await confirm({
      title: `Remove the webhook ${h.name}?`,
      message: <p>Its deliveries are turned away from then on; its runs stay. This can't be undone.</p>,
      confirm: 'Remove',
    })
    if (!yes) return
    try {
      await deleteWebhook(h.id)
      if (made?.id === h.id) setMade(null)
      await refresh()
    } catch (err) {
      onError(err)
    }
  }

  const copy = (text: string) => navigator.clipboard.writeText(text).catch(onError)

  if (!list) return null
  return (
    <>
      {confirmDialog}
      <h3 className="panel-subheading">Webhooks</h3>
      <p className="panel-note">
        GitHub, Slack and other systems start it with their events, each at a URL of its own, signed with its secret.
        Each event it takes starts a run once, however often it comes, on the version the flow published, or its board as
        it is while it publishes none, given what the webhook takes from the payload. Its runs are for whoever added it, or
        last changed what starts them.
      </p>
      {list.problem && <p className="panel-warning">Deliveries can't start it: {list.problem}.</p>}
      {made?.secret && (
        <div className="api-key-made" role="status">
          <p>
            The secret of <strong>{made.name}</strong>, to give {made.kind === 'github' ? 'GitHub' : 'the sender'} with its
            URL. Copy it now: it isn't shown again.
          </p>
          <code>{made.secret}</code>
          <button type="button" onClick={() => copy(made.secret!)}>
            Copy
          </button>
          <button type="button" onClick={() => setMade(null)}>
            Done
          </button>
        </div>
      )}
      {list.webhooks.length > 0 && (
        <ul className="repo-list">
          {list.webhooks.map((h) => (
            <li key={h.id} className="repo-item">
              <div className="repo-item-head">
                <span className="repo-item-name">{h.name}</span>
                <span className="repo-item-base">
                  {KIND_LABELS[h.kind]}
                  {!h.enabled && ' · turned off'}
                </span>
              </div>
              <div className="webhook-url">
                <span className="repo-item-url" title={h.url}>
                  {h.url}
                </span>
                <button type="button" className="link-button" onClick={() => copy(h.url)}>
                  Copy
                </button>
              </div>
              <span className="panel-note">
                {h.kind === 'generic'
                  ? 'Every delivery'
                  : h.events.length > 0
                    ? h.events.join(', ')
                    : 'Every event'}
                {h.createdBy && ` · for ${h.createdBy}`}
                {h.hint && ` · secret …${h.hint}`}
              </span>
              {h.problems.map((p) => (
                <small key={p} className="panel-warning">
                  {p}
                </small>
              ))}
              {h.lastDelivery && (
                <span className="panel-note">
                  Last delivery {formatTime(h.lastDelivery.receivedAt)}: {deliveryText(h.lastDelivery)}
                </span>
              )}
              {h.refused > 0 && (
                <small className="panel-warning">
                  {h.refused === 1 ? 'A request was' : `${h.refused} requests were`} refused as not signed with its secret
                  {h.lastRefusedAt && `, the last ${formatTime(h.lastRefusedAt)}`}.
                </small>
              )}
              <div className="mcp-item-actions">
                <button type="button" onClick={() => setShowing(h)}>
                  Deliveries
                </button>
                <button type="button" onClick={() => setEditing(h)}>
                  Edit
                </button>
                <button
                  type="button"
                  onClick={() => toggle(h)}
                  title={!h.enabled && !h.yours ? `Its runs become yours rather than ${h.createdBy || 'nobody'}'s` : undefined}
                >
                  {h.enabled ? 'Turn off' : 'Turn on'}
                </button>
                <button type="button" className="danger" onClick={() => remove(h)}>
                  Remove
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
      <button type="button" className="panel-button" onClick={() => setEditing('new')}>
        Add a webhook
      </button>
      {editing && (
        <WebhookDialog
          flowId={flowId}
          webhook={editing === 'new' ? null : editing}
          inputs={list.inputs}
          onSaved={saved}
          onClose={() => setEditing(null)}
        />
      )}
      {showing && <DeliveriesDialog webhook={showing} onClose={() => setShowing(null)} />}
    </>
  )
}

// How the mapping takes an object the flow starts from.
type Source = 'whole' | 'path' | 'attributes'

const sourceOf = (i: WebhookInput | undefined, input: WebhookFlowInput): Source =>
  i === undefined
    ? !input.many && input.attributes.length > 0
      ? 'attributes'
      : 'path'
    : i.attributes && Object.keys(i.attributes).length > 0
      ? 'attributes'
      : i.path === ''
        ? 'whole'
        : 'path'

// Adds a webhook or changes one, in the middle of the screen: its name and
// kind, how a generic one checks deliveries, the events it takes, its
// secret, and where each object the flow starts from is taken from in the
// payload, beside the latest delivery's payload to write that from.
function WebhookDialog({
  flowId,
  webhook,
  inputs,
  onSaved,
  onClose,
}: {
  flowId: number
  webhook: Webhook | null
  inputs: WebhookFlowInput[]
  onSaved: (h: Webhook) => void
  onClose: () => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const [name, setName] = useState(webhook?.name ?? '')
  const [kind, setKind] = useState<WebhookKind>(webhook?.kind ?? 'github')
  const [verify, setVerify] = useState<WebhookVerify>(webhook?.verify ?? 'hmac')
  const [header, setHeader] = useState(webhook?.header ?? '')
  const [idHeader, setIDHeader] = useState(webhook ? (webhook.idHeader ?? '') : 'Idempotency-Key')
  const [timestampHeader, setTimestampHeader] = useState(webhook?.timestampHeader ?? '')
  const [events, setEvents] = useState((webhook?.events ?? []).join(', '))
  // Keep the secret kept, make one, or take the one pasted.
  const [secretMode, setSecretMode] = useState<'keep' | 'make' | 'paste'>(webhook ? 'keep' : 'make')
  const [secret, setSecret] = useState('')
  const [sources, setSources] = useState<Record<string, Source>>(() =>
    Object.fromEntries(inputs.map((i) => [i.name, sourceOf(webhook?.mapping[i.name], i)])),
  )
  const [paths, setPaths] = useState<Record<string, WebhookInput>>(() => ({ ...(webhook?.mapping ?? {}) }))
  const [payload, setPayload] = useState<WebhookDeliveries['payload']>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])

  useEffect(() => {
    if (!webhook) return
    let stale = false
    getWebhookDeliveries(webhook.id).then(
      (d) => !stale && setPayload(d.payload),
      () => {},
    )
    return () => {
      stale = true
    }
  }, [webhook])

  // Slack's signing secret comes from Slack, so it is always pasted.
  const mode = kind === 'slack' && secretMode === 'make' ? 'paste' : secretMode
  const unused = Object.keys(paths).filter((n) => !inputs.some((i) => i.name === n))

  const setPath = (input: string, path: string) => setPaths((p) => ({ ...p, [input]: { path } }))
  const setAttribute = (input: string, attr: string, path: string) =>
    setPaths((p) => ({ ...p, [input]: { path: '', attributes: { ...(p[input]?.attributes ?? {}), [attr]: path } } }))

  const save = async () => {
    if (saving) return
    setSaving(true)
    setError('')
    // What each object is taken from, as its source says; what the flow
    // doesn't start from is kept as it was.
    const mapping: Record<string, WebhookInput> = Object.fromEntries(unused.map((n) => [n, paths[n]]))
    for (const i of inputs) {
      const p = paths[i.name]
      switch (sources[i.name]) {
        case 'whole':
          mapping[i.name] = { path: '' }
          break
        case 'path':
          if (p?.path.trim()) mapping[i.name] = { path: p.path.trim() }
          break
        case 'attributes': {
          const attrs = Object.entries(p?.attributes ?? {})
            .map(([a, v]) => [a, v.trim()] as const)
            .filter(([a, v]) => v && i.attributes.includes(a))
          if (attrs.length > 0) mapping[i.name] = { path: '', attributes: Object.fromEntries(attrs) }
        }
      }
    }
    const fields: WebhookFields = {
      flow: flowId,
      name: name.trim(),
      kind,
      verify: kind === 'generic' ? verify : '',
      header: kind === 'generic' ? header.trim() : '',
      idHeader: kind === 'generic' ? idHeader.trim() : '',
      timestampHeader: kind === 'generic' && verify === 'hmac' ? timestampHeader.trim() : '',
      events:
        kind === 'generic'
          ? []
          : events
              .split(/[\s,]+/)
              .map((e) => e.trim())
              .filter(Boolean),
      mapping,
      enabled: webhook?.enabled ?? true,
      secret: mode === 'paste' ? secret.trim() : '',
      newSecret: webhook !== null && mode === 'make',
    }
    try {
      onSaved(webhook ? await updateWebhook(webhook.id, fields) : await createWebhook(fields))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setSaving(false)
    }
  }

  return (
    <dialog
      ref={ref}
      className="review-dialog webhook-dialog"
      aria-label={webhook ? `Change the webhook ${webhook.name}` : 'Add a webhook'}
      onClose={onClose}
      // Escape closes the dialog, not the settings it opened from.
      onKeyDown={(e) => e.stopPropagation()}
    >
      <header className="panel-header">
        <h2>{webhook ? `Change ${webhook.name}` : 'Add a webhook'}</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <div className="rerun-body">
        {webhook && !webhook.yours && (
          <p className="panel-note">
            Its runs are {webhook.createdBy ? `${webhook.createdBy}'s` : "nobody's"}. Changing anything but its name makes
            them yours.
          </p>
        )}
        <label className="panel-field">
          <span>Name</span>
          <input value={name} placeholder="What sends it, such as Issues labeled" onChange={(e) => setName(e.target.value)} />
        </label>
        <div className="panel-field" role="radiogroup" aria-label="Kind">
          <span>Sent by</span>
          <div className="segmented">
            {(Object.keys(KIND_LABELS) as WebhookKind[]).map((k) => (
              <button
                key={k}
                type="button"
                role="radio"
                aria-checked={kind === k}
                className={kind === k ? 'selected' : ''}
                onClick={() => setKind(k)}
              >
                {KIND_LABELS[k]}
              </button>
            ))}
          </div>
        </div>
        {kind === 'github' && (
          <p className="panel-note">
            In the repository's or organization's settings, under Webhooks, add one with this webhook's URL, the content type
            application/json and its secret, and pick the events GitHub sends it.
          </p>
        )}
        {kind === 'slack' && (
          <p className="panel-note">
            In the Slack app's settings, under Event Subscriptions, give this webhook's URL as the Request URL, which Slack
            checks at once, and subscribe to the events it takes. The signing secret is on the app's Basic Information page.
          </p>
        )}
        {kind === 'generic' && (
          <>
            <div className="panel-field" role="radiogroup" aria-label="Checked by">
              <span>Each delivery carries</span>
              <div className="segmented">
                {(
                  [
                    ['hmac', 'An HMAC of its body'],
                    ['token', 'The secret'],
                  ] as const
                ).map(([v, label]) => (
                  <button
                    key={v}
                    type="button"
                    role="radio"
                    aria-checked={verify === v}
                    className={verify === v ? 'selected' : ''}
                    onClick={() => setVerify(v)}
                  >
                    {label}
                  </button>
                ))}
              </div>
              <small className="panel-note">
                {verify === 'hmac'
                  ? `An HMAC-SHA256 keyed with the secret, in hex or base64, with or without sha256= before it, of ${[
                      timestampHeader.trim() && 'the timestamp and a dot',
                      idHeader.trim() && 'the id and a dot',
                      'the body',
                    ]
                      .filter(Boolean)
                      .join(', then ')}.`
                  : 'The secret itself, or Bearer and the secret.'}
              </small>
            </div>
            <label className="panel-field">
              <span>In the header</span>
              <input
                value={header}
                spellCheck={false}
                placeholder={verify === 'hmac' ? 'X-Signature' : 'Authorization'}
                onChange={(e) => setHeader(e.target.value)}
              />
            </label>
            <label className="panel-field">
              <span>Its id in the header</span>
              <input value={idHeader} spellCheck={false} placeholder="None" onChange={(e) => setIDHeader(e.target.value)} />
              <small className="panel-note">
                A delivery sent again with the same id gets the run the first started; without one, each starts a run.
                {verify === 'hmac' && ' Its HMAC covers the id, so nobody can send a delivery again under another.'}
              </small>
            </label>
            {verify === 'hmac' && (
              <label className="panel-field">
                <span>Its timestamp in the header</span>
                <input
                  value={timestampHeader}
                  spellCheck={false}
                  placeholder="None"
                  onChange={(e) => setTimestampHeader(e.target.value)}
                />
                <small className="panel-note">
                  Seconds since 1970, which its HMAC covers; one more than five minutes off is refused, so a delivery
                  can't be sent again later.
                </small>
              </label>
            )}
            {(verify === 'token' || (!idHeader.trim() && !timestampHeader.trim())) && (
              <small className="panel-warning">
                {verify === 'token'
                  ? 'The secret travels with each delivery, and nothing tells one sent again from a new one.'
                  : 'With neither an id nor a timestamp signed, whoever sees a delivery could send it again as a new one.'}
              </small>
            )}
          </>
        )}
        {kind !== 'generic' && (
          <label className="panel-field">
            <span>Events it takes</span>
            <input
              value={events}
              spellCheck={false}
              placeholder={kind === 'github' ? 'issues.labeled, pull_request' : 'app_mention; none for every event'}
              onChange={(e) => setEvents(e.target.value)}
            />
            <small className="panel-note">
              {kind === 'github'
                ? 'Each event alone, such as pull_request, or with its action, such as issues.labeled; it answers the others and starts nothing.'
                : 'Each event by its type; none takes every event Slack sends.'}
            </small>
          </label>
        )}
        <div className="panel-field" role="radiogroup" aria-label="Secret">
          <span>{kind === 'slack' ? 'Signing secret' : 'Secret'}</span>
          <div className="segmented">
            {(
              [
                ...(webhook ? ([['keep', `Keep it${webhook.hint ? ` (…${webhook.hint})` : ''}`]] as const) : []),
                ...(kind !== 'slack' ? ([['make', webhook ? 'Make a new one' : 'Make one']] as const) : []),
                ['paste', kind === 'slack' ? 'Paste it' : 'Paste one'],
              ] as const
            ).map(([v, label]) => (
              <button
                key={v}
                type="button"
                role="radio"
                aria-checked={mode === v}
                className={mode === v ? 'selected' : ''}
                onClick={() => setSecretMode(v)}
              >
                {label}
              </button>
            ))}
          </div>
          {mode === 'paste' && (
            <input type="password" value={secret} autoComplete="off" spellCheck={false} onChange={(e) => setSecret(e.target.value)} />
          )}
          {mode === 'make' && <small className="panel-note">It is shown once, as it is made, to give the sender.</small>}
        </div>
        <h3 className="panel-subheading">What the flow is given</h3>
        {inputs.length === 0 ? (
          <p className="panel-note">The flow starts from nothing, so each delivery only starts it.</p>
        ) : (
          <p className="panel-note">
            Each object the flow starts from is taken from the payload: from a path into it, such as issue.title or
            labels.0.name, the whole of it, or for an object with attributes, a path for each.
          </p>
        )}
        {inputs.map((i) => (
          <fieldset key={i.name} className="webhook-input">
            <legend>{i.name}</legend>
            <div className="segmented">
              {(
                [
                  ['path', 'A path'],
                  ['whole', 'The whole payload'],
                  ...(!i.many && i.attributes.length > 0 ? ([['attributes', 'A path for each attribute']] as const) : []),
                ] as const
              ).map(([v, label]) => (
                <button
                  key={v}
                  type="button"
                  className={sources[i.name] === v ? 'selected' : ''}
                  aria-pressed={sources[i.name] === v}
                  onClick={() => setSources((s) => ({ ...s, [i.name]: v }))}
                >
                  {label}
                </button>
              ))}
            </div>
            {sources[i.name] === 'path' && (
              <input
                aria-label={`${i.name}'s path`}
                value={paths[i.name]?.attributes ? '' : (paths[i.name]?.path ?? '')}
                spellCheck={false}
                placeholder="issue.title"
                onChange={(e) => setPath(i.name, e.target.value)}
              />
            )}
            {sources[i.name] === 'attributes' &&
              i.attributes.map((a) => (
                <label key={a} className="webhook-attribute">
                  <span>{a}</span>
                  <input
                    value={paths[i.name]?.attributes?.[a] ?? ''}
                    spellCheck={false}
                    placeholder="issue.number"
                    onChange={(e) => setAttribute(i.name, a, e.target.value)}
                  />
                </label>
              ))}
            {i.many && sources[i.name] !== 'whole' && (
              <small className="panel-note">A list: the path leads to an array of its items.</small>
            )}
          </fieldset>
        ))}
        {unused.length > 0 && (
          <p className="panel-note">
            The mapping also gives {unused.join(', ')}, which the flow doesn't start from, so that goes unused.{' '}
            <button
              type="button"
              className="link-button"
              onClick={() => setPaths((p) => Object.fromEntries(Object.entries(p).filter(([n]) => !unused.includes(n))))}
            >
              Drop it
            </button>
          </p>
        )}
        {payload ? (
          <details className="webhook-payload">
            <summary>
              The latest delivery's payload{payload.event && `, ${payload.event}`}, {formatTime(payload.receivedAt)}
            </summary>
            <pre className="api-curl">{readable(payload.payload)}</pre>
          </details>
        ) : (
          webhook && <p className="panel-note">Once a delivery comes, its payload shows here, to write the paths from.</p>
        )}
        {error && <p className="panel-warning">{error}</p>}
      </div>
      <div className="review-actions">
        <button onClick={onClose}>Cancel</button>
        <button onClick={save} disabled={saving || !name.trim() || (mode === 'paste' && !secret.trim())}>
          {saving ? 'Saving…' : webhook ? 'Save' : 'Add'}
        </button>
      </div>
    </dialog>
  )
}

// A webhook's latest deliveries, newest first: when, which event, what came
// of it and why, and the run it started; and the latest signed payload.
function DeliveriesDialog({ webhook, onClose }: { webhook: Webhook; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null)
  const [data, setData] = useState<WebhookDeliveries | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])

  const load = useCallback(() => {
    getWebhookDeliveries(webhook.id).then(setData, (err) => setError(err instanceof Error ? err.message : String(err)))
  }, [webhook.id])

  useEffect(load, [load])

  return (
    <dialog
      ref={ref}
      className="review-dialog webhook-dialog"
      aria-label={`Deliveries to ${webhook.name}`}
      onClose={onClose}
      onKeyDown={(e) => e.stopPropagation()}
    >
      <header className="panel-header">
        <h2>Deliveries to {webhook.name}</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <div className="rerun-body">
        {error && <p className="panel-warning">{error}</p>}
        {data && data.deliveries.length === 0 && <p className="panel-note">None yet.</p>}
        {data && data.deliveries.length > 0 && (
          <table className="webhook-deliveries">
            <thead>
              <tr>
                <th>When</th>
                <th>Event</th>
                <th>What came of it</th>
              </tr>
            </thead>
            <tbody>
              {data.deliveries.map((d) => (
                <tr key={d.id} className={`webhook-${d.outcome}`}>
                  <td>{formatTime(d.receivedAt)}</td>
                  <td>
                    {d.event || '—'}
                    {d.delivery && <div className="webhook-delivery-id">{d.delivery}</div>}
                  </td>
                  <td>
                    {OUTCOME_LABELS[d.outcome]}
                    {d.run !== null && (
                      <>
                        {' '}
                        <a href={`#/flows/${webhook.flow}/runs/${d.run}`} onClick={onClose}>
                          run #{d.run}
                        </a>
                      </>
                    )}
                    {d.reason && <div className="panel-note">{d.reason}</div>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {data?.payload && (
          <details className="webhook-payload">
            <summary>
              The latest signed delivery's payload{data.payload.event && `, ${data.payload.event}`},{' '}
              {formatTime(data.payload.receivedAt)}
            </summary>
            <pre className="api-curl">{readable(data.payload.payload)}</pre>
          </details>
        )}
      </div>
      <div className="review-actions">
        <button onClick={load}>Refresh</button>
        <button onClick={onClose}>Close</button>
      </div>
    </dialog>
  )
}
