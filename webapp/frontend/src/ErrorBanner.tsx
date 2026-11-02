// The banner under a page's header saying what went wrong: the page's own
// error, in the server's words (ApiError in api.ts), and above it how
// things stand with the server while it doesn't answer, which every page
// says the same way, so a page needn't report it itself. Each is dismissed with its ×,
// never by clicking the banner, so its text can be selected and copied.

import { useEffect, useRef, useSyncExternalStore, type ReactNode } from 'react'
import { connectionText, currentConnection, dismissConnection, errorText, onConnection, unreachable } from './api'

function Dismiss({ onClick }: { onClick: () => void }) {
  return (
    <button type="button" className="banner-dismiss" aria-label="Dismiss" title="Dismiss" onClick={onClick}>
      ×
    </button>
  )
}

type Props = {
  // What went wrong on the page, an error or the text kept of one; none
  // shows nothing of its own.
  error: unknown
  // Without it the page's error stays, as one saying the page couldn't load.
  onDismiss?: () => void
  // Offered once the server is back after a change made while it was away
  // was lost, to show again what it kept, such as the board.
  reload?: { label: string; onClick: () => void }
}

export default function ErrorBanner({ error, onDismiss, reload }: Props) {
  const connection = useSyncExternalStore(onConnection, currentConnection)
  const notice = connectionText(connection)

  // A page's own error that the server couldn't be reached goes once it is
  // back, a change lost meanwhile said by the notice.
  const wasOffline = useRef(connection.offline)
  useEffect(() => {
    if (wasOffline.current && !connection.offline && unreachable(error)) onDismiss?.()
    wasOffline.current = connection.offline
  }, [connection.offline, error, onDismiss])

  // While the notice has it to say, even dismissed, the page doesn't say
  // again that the server couldn't be reached.
  const said = connection.offline || connection.lostChange
  const own = error != null && error !== '' && !(said && unreachable(error)) ? errorText(error) : null

  if (!notice && !own) return null
  return (
    <>
      {notice && (
        <div className="error-banner" role="alert">
          <span className="banner-text">{notice}</span>
          {reload && !connection.offline && (
            <button type="button" className="banner-action" onClick={reload.onClick}>
              {reload.label}
            </button>
          )}
          <Dismiss onClick={dismissConnection} />
        </div>
      )}
      {own && (
        <div className="error-banner" role="alert">
          <span className="banner-text">{own}</span>
          {onDismiss && <Dismiss onClick={onDismiss} />}
        </div>
      )}
    </>
  )
}

// The banner saying what something did, such as an import, dismissed as
// the error banner is.
export function NoticeBanner({ children, onDismiss }: { children: ReactNode; onDismiss?: () => void }) {
  return (
    <div className="notice-banner" role="status">
      <span className="banner-text">{children}</span>
      {onDismiss && <Dismiss onClick={onDismiss} />}
    </div>
  )
}
