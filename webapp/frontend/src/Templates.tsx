import { useEffect, useRef, useState, type ReactNode } from 'react'
import { listTemplates, type FlowTemplate, type ProcessKind, type TemplateNeed } from './api'
import { AgentIcon, ConnectorIcon, FlowIcon, HumanIcon, ScriptIcon, SubflowIcon } from './icons'

// Ready flows to start from, kept in the webapp (templates.go): each says
// what it does, its steps, and what it needs of the app, a repository, a
// connector or a role, and whether the app has it. Using one makes it a
// new flow, which opens; what it still lacks shows on its board as its
// problems. On an empty list of flows it shows in place; otherwise New
// from template opens it as a dialog.
export function TemplateGallery({
  admin,
  onUse,
  onError,
}: {
  // Whether whoever looks may install connectors and give roles.
  admin: boolean
  onUse: (t: FlowTemplate) => Promise<void>
  onError: (err: unknown) => void
}) {
  const [templates, setTemplates] = useState<FlowTemplate[] | null>(null)
  const [using, setUsing] = useState<string | null>(null)
  useEffect(() => {
    listTemplates().then(setTemplates, onError)
  }, [onError])

  const use = async (t: FlowTemplate) => {
    setUsing(t.id)
    try {
      await onUse(t)
    } catch (err) {
      onError(err)
      setUsing(null)
    }
  }

  if (!templates) return <p className="flows-note">Loading the templates…</p>
  return (
    <ul className="template-list">
      {templates.map((t) => (
        <li key={t.id} className="template-card">
          <h3>{t.title}</h3>
          <p className="template-description">{t.description}</p>
          <ol className="template-steps" aria-label="Its processes">
            {t.steps.map((s) => (
              <li key={s.name} title={KIND_LABELS[s.kind]}>
                {KIND_ICONS[s.kind]}
                {s.name}
              </li>
            ))}
          </ol>
          {t.needs.length > 0 && (
            <ul className="template-needs" aria-label="What it needs">
              {t.needs.map((n) => (
                <li key={`${n.kind}:${n.name}`} className={n.met ? 'met' : 'unmet'}>
                  <Need need={n} admin={admin} />
                </li>
              ))}
            </ul>
          )}
          <button className="template-use" disabled={using !== null} onClick={() => void use(t)}>
            {using === t.id ? 'Making it…' : 'Use this template'}
          </button>
        </li>
      ))}
    </ul>
  )
}

// What a template needs, and what to do while the app hasn't it.
function Need({ need, admin }: { need: TemplateNeed; admin: boolean }) {
  const settings = (path: string, what: string) =>
    admin ? (
      <a href={`#/settings/${path}`}>in Settings, under {what}</a>
    ) : (
      `an admin does in Settings, under ${what}`
    )
  switch (need.kind) {
    case 'repository':
      return (
        <>
          A repository to work on: add it in the flow’s settings once it is made, named <strong>{need.name}</strong>.
        </>
      )
    case 'connector':
      return need.met ? (
        <>The {need.title} connector, which is installed.</>
      ) : (
        <>
          The {need.title} connector, which isn’t installed yet; until it is, the flow can’t run. Installing it {settings('connectors', 'Connectors')}.
        </>
      )
    case 'role':
      return need.met ? (
        <>
          Someone holding the role <strong>{need.name}</strong> decides its reviews.
        </>
      ) : (
        <>
          The role <strong>{need.name}</strong>, which nobody holds yet, so only admins decide its reviews. Giving it to someone {settings('people', 'People')}.
        </>
      )
  }
}

// The gallery as a dialog, from New from template.
export function TemplateDialog({
  admin,
  onUse,
  onClose,
  onError,
}: {
  admin: boolean
  onUse: (t: FlowTemplate) => Promise<void>
  onClose: () => void
  onError: (err: unknown) => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])
  return (
    <dialog ref={ref} className="review-dialog template-dialog" aria-label="New from a template" onClose={onClose}>
      <header className="panel-header">
        <h2>New from a template</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <p className="panel-note">A ready flow to start from, made a flow of your own to change as you like.</p>
      <TemplateGallery admin={admin} onUse={onUse} onError={onError} />
    </dialog>
  )
}

const KIND_LABELS: Record<ProcessKind, string> = {
  agent: 'An agent',
  script: 'A script',
  human: 'A person',
  flow: 'A flow of its own',
  subflow: 'Another flow',
  connector: 'A connector',
}

const KIND_ICONS: Record<ProcessKind, ReactNode> = {
  agent: <AgentIcon />,
  script: <ScriptIcon />,
  human: <HumanIcon />,
  flow: <FlowIcon />,
  subflow: <SubflowIcon />,
  connector: <ConnectorIcon />,
}
