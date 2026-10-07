import { useEffect, useRef, useState, type FormEvent } from 'react'
import { updateAgentSettings, type AgentSettings, type AgentSettingsPatch, type ThinkingLevel } from './api'

// Must match agentProviders in webapp/agent.go.
const PROVIDERS: { value: string; label: string; model: string }[] = [
  { value: 'anthropic', label: 'Anthropic', model: 'claude-sonnet-4-5' },
  { value: 'openai', label: 'OpenAI', model: 'gpt-4o' },
  { value: 'google', label: 'Google Gemini', model: 'gemini-2.5-pro' },
  { value: 'openrouter', label: 'OpenRouter', model: 'anthropic/claude-sonnet-4.5' },
  { value: 'mistral', label: 'Mistral', model: 'mistral-large-latest' },
  { value: 'groq', label: 'Groq', model: 'llama-3.3-70b-versatile' },
  { value: 'xai', label: 'xAI', model: 'grok-4' },
  { value: 'deepseek', label: 'DeepSeek', model: 'deepseek-chat' },
]

const THINKING: ThinkingLevel[] = ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']

// Configures the pi harness that runs agent processes.
export default function AgentSettingsDialog({
  initial,
  onSaved,
  onClose,
}: {
  initial: AgentSettings
  onSaved: (settings: AgentSettings) => void
  onClose: () => void
}) {
  const [provider, setProvider] = useState(initial.provider)
  const [model, setModel] = useState(initial.model)
  const [thinking, setThinking] = useState(initial.thinking)
  const [apiKey, setApiKey] = useState('')
  const [clearKey, setClearKey] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const dialog = useRef<HTMLDialogElement>(null)

  useEffect(() => {
    dialog.current?.showModal()
  }, [])

  const placeholder = PROVIDERS.find((p) => p.value === provider)?.model

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault()
    const patch: AgentSettingsPatch = { provider, model: model.trim(), thinking }
    if (clearKey) patch.clearApiKey = true
    else if (apiKey.trim() !== '') patch.apiKey = apiKey.trim()
    setSaving(true)
    setError(null)
    try {
      onSaved(await updateAgentSettings(patch))
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setSaving(false)
    }
  }

  return (
    // onCancel covers Escape; the native dialog would close itself without telling React.
    <dialog
      ref={dialog}
      className="settings-dialog"
      onCancel={(e) => {
        e.preventDefault()
        onClose()
      }}
    >
      <form onSubmit={onSubmit}>
        <header className="panel-header">
          <h2>Agent settings</h2>
          <button type="button" className="panel-close" onClick={onClose} aria-label="Close settings">
            ×
          </button>
        </header>
        <p className="settings-note">
          Agent processes run their prompt in a pi harness in the worker container, using these settings.
        </p>
        <label className="panel-field">
          <span>Provider</span>
          <select value={provider} onChange={(e) => setProvider(e.target.value)}>
            {PROVIDERS.map((p) => (
              <option key={p.value} value={p.value}>
                {p.label}
              </option>
            ))}
          </select>
        </label>
        <label className="panel-field">
          <span>Model</span>
          <input
            value={model}
            placeholder={placeholder && `e.g. ${placeholder}`}
            spellCheck={false}
            onChange={(e) => setModel(e.target.value)}
          />
        </label>
        <label className="panel-field">
          <span>Thinking</span>
          <select value={thinking} onChange={(e) => setThinking(e.target.value as ThinkingLevel)}>
            {THINKING.map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </select>
        </label>
        <label className="panel-field">
          <span>API key</span>
          <input
            type="password"
            value={apiKey}
            disabled={clearKey}
            autoComplete="off"
            placeholder={initial.hasApiKey ? 'Leave empty to keep the saved key' : 'Paste your API key'}
            onChange={(e) => setApiKey(e.target.value)}
          />
          {initial.hasApiKey && (
            <small className="settings-key">
              Key saved{initial.keyHint && ` (ends in ${initial.keyHint})`}.
              <label>
                <input type="checkbox" checked={clearKey} onChange={(e) => setClearKey(e.target.checked)} /> Remove it
              </label>
            </small>
          )}
        </label>
        {error && <p className="panel-warning">{error}</p>}
        <footer className="settings-actions">
          <button type="button" className="secondary" onClick={onClose}>
            Cancel
          </button>
          <button type="submit" disabled={saving}>
            {saving ? 'Saving…' : 'Save'}
          </button>
        </footer>
      </form>
    </dialog>
  )
}
