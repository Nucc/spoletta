import { useCallback, useEffect, useState, type FormEvent } from 'react'
import { allowEgressHost, listEgressHosts, removeEgressHost, type EgressHost } from './api'
import { PlusIcon } from './icons'
import { SettingsEmpty, SettingsHead } from './SettingsHead'

// The internet hosts steps may reach when they run in containers of their
// own, which admins allow; each step reaches its repositories' hosts too.
export function EgressSection({ onError }: { onError: (err: unknown) => void }) {
  const [hosts, setHosts] = useState<EgressHost[] | null>(null)
  const [host, setHost] = useState('')
  const [note, setNote] = useState('')

  const refresh = useCallback(async () => setHosts(await listEgressHosts()), [])
  useEffect(() => {
    refresh().catch(onError)
  }, [refresh, onError])

  const onAllow = async (e: FormEvent) => {
    e.preventDefault()
    if (!host.trim()) return
    try {
      setHosts(await allowEgressHost(host.trim(), note.trim()))
      setHost('')
      setNote('')
    } catch (err) {
      onError(err)
    }
  }

  const onRemove = async (h: EgressHost) => {
    try {
      await removeEgressHost(h.host)
      await refresh()
    } catch (err) {
      onError(err)
    }
  }

  return (
    <section className="flow-section" aria-label="Internet access">
      <SettingsHead
        title="Internet access"
        lead={
          <>
            The hosts steps in containers may reach on the internet, besides their repositories': a name such as{' '}
            <code>registry.npmjs.org</code>, or <code>*.example.com</code> for its subdomains.
          </>
        }
        more={
          <>
            When steps run in containers of their own, they reach their models and MCP servers through the webapp, and
            the internet only at the hosts allowed here, besides their own repositories' hosts, which their git pushes
            to. Add <code>:port</code> to a host for a port other than 80 and 443. Every connection goes in its run's
            log.
          </>
        }
      />
      <form className="flow-new" onSubmit={onAllow}>
        <input placeholder="Host, such as registry.npmjs.org" aria-label="Host" value={host} onChange={(e) => setHost(e.target.value)} />
        <input placeholder="What for (optional)" aria-label="Note" value={note} onChange={(e) => setNote(e.target.value)} />
        <button type="submit" disabled={!host.trim()}>
          <PlusIcon /> Allow host
        </button>
      </form>
      {hosts === null ? (
        <p className="flows-note">Loading…</p>
      ) : hosts.length === 0 ? (
        <SettingsEmpty>
          None yet, so steps in containers reach only the webapp and their repositories. Allow a host above for a
          package registry or an API a step calls.
        </SettingsEmpty>
      ) : (
        <ul className="flow-list key-list">
          {hosts.map((h) => (
            <li key={h.host}>
              <div className="key-item egress-item">
                <span className="key-provider">
                  <code className="flow-card-name">{h.host}</code>
                  {h.note && <span className="flows-note">{h.note}</span>}
                </span>
                <button type="button" className="danger" onClick={() => onRemove(h)}>
                  Remove
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
