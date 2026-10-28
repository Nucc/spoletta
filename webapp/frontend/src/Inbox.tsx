import { useCallback, useEffect, useState } from 'react'
import { listInbox, onInboxChanged, reviewHash, type InboxItem } from './api'
import { useMe } from './Account'
import { FlowBuilderIcon, InboxIcon } from './icons'
import { formatTime } from './Runs'

// How often the inbox, and the count of it in the tab bar, ask again.
export const INBOX_REFRESH_MS = 30_000

// What each step waits for, as its row says.
const WAITS: Record<InboxItem['waits'], string> = {
  review: 'Review',
  input: 'Input',
  loop: 'Loop question',
}

// Every step waiting on the person signed in, across every flow they see,
// newest first: the reviews and inputs they may decide, and the loop
// questions of flows they may change; without sign-in, every step waiting.
// Each opens its flow's board on its run, with its review showing.
export default function Inbox() {
  const me = useMe()
  const [items, setItems] = useState<InboxItem[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(
    () =>
      listInbox()
        .then((is) => {
          setItems(is)
          setError(null)
        })
        .catch((err) => setError(err instanceof Error ? err.message : String(err))),
    [],
  )

  useEffect(() => {
    document.title = 'Inbox · Flows'
    refresh()
    const t = window.setInterval(refresh, INBOX_REFRESH_MS)
    const onVisible = () => document.visibilityState === 'visible' && refresh()
    document.addEventListener('visibilitychange', onVisible)
    const off = onInboxChanged(refresh)
    return () => {
      window.clearInterval(t)
      document.removeEventListener('visibilitychange', onVisible)
      off()
    }
  }, [refresh])

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
          Inbox
        </h1>
      </header>
      {error && (
        <div className="error-banner" role="alert" onClick={() => setError(null)}>
          {error} (click to dismiss)
        </div>
      )}
      <main className="flows inbox">
        <section className="flow-section" aria-label="Inbox">
          <h2>
            <InboxIcon /> What waits on you
          </h2>
          <p className="flows-note">
            {me?.signIn === false
              ? 'Every review, input and loop question waiting, in every flow, as nobody signs in and so everyone decides everything.'
              : 'The reviews and inputs your roles let you decide, and the loop questions of flows you may change, in every flow you see.'}{' '}
            {me?.user && (
              <>
                How you are told of them is in <a href="#/settings/account">your settings</a>.
              </>
            )}
          </p>
          {items === null ? (
            <p className="flows-note">Loading…</p>
          ) : items.length === 0 ? (
            <p className="flows-note inbox-empty">Nothing waits on you.</p>
          ) : (
            <ul className="inbox-list">
              {items.map((it) => (
                <li key={it.step}>
                  <InboxRow item={it} />
                </li>
              ))}
            </ul>
          )}
        </section>
      </main>
    </div>
  )
}

function InboxRow({ item }: { item: InboxItem }) {
  // The first lines of the instructions or the question, the rest on the board.
  const detail = item.detail.trim().split('\n').slice(0, 3).join('\n')
  return (
    <a className={`inbox-item waits-${item.waits}`} href={reviewHash(item)}>
      <span className="inbox-head">
        <span className="inbox-waits">{WAITS[item.waits]}</span>
        <span className="inbox-process">{item.processName}</span>
      </span>
      <span className="inbox-where">
        {item.flowName} · run {item.run}
        {item.parent !== null && ` (within run ${item.parent})`}
        {item.startedBy && `, started by ${item.startedBy}`}
      </span>
      {detail && <span className="inbox-detail">{detail}</span>}
      <span className="inbox-when">
        Waiting since {formatTime(item.since)}
        {item.handledBy.length > 0 && ` · for ${item.handledBy.join(' or ')}`}
        {item.escalated && <strong> · handed on, as nobody decided it in time</strong>}
        {item.expires && <strong> · fails {formatTime(item.expires)} if nobody decides</strong>}
      </span>
    </a>
  )
}
