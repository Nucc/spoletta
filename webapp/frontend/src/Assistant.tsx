import { useCallback, useEffect, useRef, useState } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { askAssistant, clearAssistant, getAssistant, stopAssistant, type AssistantMessage } from './api'
import { AssistantIcon, StopIcon, TrashIcon } from './icons'
import { useConfirm } from './Confirm'

// How often a reply going is looked in on.
const POLL_MS = 1500

// The flow's assistant, behind a button at the bottom right of the board: an
// agent the person talks to to have it set the flow up, such as build a flow
// that processes Jira tickets, or to ask about its runs: a message naming a
// run (run 412, #412 or its link) has the assistant told it, and the run's
// panel asks from a run or a failed step, with a question naming it for the
// person to edit and send. A reply that changes the flow has the board
// reloaded, highlighting what it changed, and the board before is saved as
// a version, so it can be undone.
// The conversation is kept with the flow; a reply goes on in the server, so
// closing the widget or the page doesn't stop it.
export default function Assistant({
  flowId,
  disabled,
  runOpen,
  asking,
  beforeSend,
  onApplied,
  onShowChanges,
  onError,
}: {
  flowId: number
  // While a version is shown: the conversation reads, but nothing is sent.
  disabled: boolean
  // The run open on the board, if any, which the person may ask about.
  runOpen: number | null
  // A question about a run asked from its panel, which opens the assistant
  // with it to send; seq tells one asked again apart.
  asking: { question: string; seq: number } | null
  // Settles what the board is sending, so the assistant sees the board as it is.
  beforeSend: () => Promise<void>
  // With the version saved before the reply changed the flow, to show what
  // it changed against.
  onApplied: (version: number | null) => Promise<void>
  onShowChanges: (version: number) => void
  onError: (err: unknown) => void
}) {
  const [open, setOpen] = useState(false)
  const [messages, setMessages] = useState<AssistantMessage[] | null>(null)
  const [confirm, confirmDialog] = useConfirm()
  const [draft, setDraft] = useState('')
  const [sending, setSending] = useState(false)
  const list = useRef<HTMLDivElement>(null)
  const input = useRef<HTMLTextAreaElement>(null)

  const last = messages?.[messages.length - 1]
  const replying = last?.role === 'assistant' && last.status === 'active'

  // The reply waited on: once it is over, a change it made reloads the board.
  const waiting = useRef<number | null>(null)
  const take = useCallback(
    (msgs: AssistantMessage[]) => {
      setMessages(msgs)
      const end = msgs[msgs.length - 1]
      if (end?.role === 'assistant' && end.status === 'active') {
        waiting.current = end.id
        return
      }
      const reply = msgs.find((m) => m.id === waiting.current)
      waiting.current = null
      if (reply?.applied) onApplied(reply.version).catch(onError)
    },
    [onApplied, onError],
  )

  const load = useCallback(() => getAssistant(flowId).then(take).catch(onError), [flowId, take, onError])
  useEffect(() => {
    load()
  }, [load])

  // Looked in on while it replies, open or not.
  useEffect(() => {
    if (!replying) return
    const timer = window.setInterval(load, POLL_MS)
    return () => window.clearInterval(timer)
  }, [replying, load])

  useEffect(() => {
    if (open) list.current?.scrollTo({ top: list.current.scrollHeight })
  }, [open, messages])

  useEffect(() => {
    if (open) input.current?.focus()
  }, [open])

  // Asked from a run: opens with the question after what is written yet, to
  // edit and send.
  useEffect(() => {
    if (!asking) return
    setOpen(true)
    setDraft((d) => (d.includes(asking.question) ? d : d.trim() ? `${d.trimEnd()}\n${asking.question}` : asking.question))
    const timer = window.setTimeout(() => {
      const el = input.current
      el?.focus()
      el?.setSelectionRange(el.value.length, el.value.length)
    })
    return () => window.clearTimeout(timer)
  }, [asking])

  const send = async () => {
    const message = draft.trim()
    if (!message || sending || replying || disabled) return
    setSending(true)
    try {
      await beforeSend()
      take(await askAssistant(flowId, message))
      setDraft('')
    } catch (err) {
      onError(err)
    } finally {
      setSending(false)
    }
  }

  const stop = () => stopAssistant(flowId).then(load).catch(onError)
  // The conversation goes for good; what it changed on the board stays.
  const clear = async () => {
    const yes = await confirm({
      title: 'Start the conversation over?',
      message: <p>The conversation is deleted for good; what the assistant changed on the board stays.</p>,
      confirm: 'Start over',
    })
    if (yes) clearAssistant(flowId).then(load).catch(onError)
  }

  if (!open) {
    return (
      <button
        className={`assistant-button${replying ? ' replying' : ''}`}
        onClick={() => setOpen(true)}
        aria-label="Open the assistant"
        title={replying ? 'The assistant is working on your flow' : 'Ask the assistant to set up this flow, or about its runs'}
      >
        <AssistantIcon size={22} />
      </button>
    )
  }
  return (
    <section className="assistant" aria-label="Assistant" onKeyDown={(e) => e.key === 'Escape' && setOpen(false)}>
      <header className="assistant-header">
        <AssistantIcon />
        <h2>Assistant</h2>
        <button
          className="icon-button"
          onClick={clear}
          disabled={replying || !messages?.length}
          aria-label="Start over"
          title="Start the conversation over"
        >
          <TrashIcon />
        </button>
        <button className="panel-close" onClick={() => setOpen(false)} aria-label="Close the assistant">
          ×
        </button>
      </header>
      {confirmDialog}
      <div className="assistant-messages" ref={list}>
        {messages?.length === 0 && (
          <p className="assistant-hint">
            Describe what this flow should do, and the assistant builds it on the board. For example: “Build a flow that
            takes new Jira tickets, summarizes each one and posts the summary as a comment.” It saves the flow as a version
            before changing it, so you can always go back. It also explains the flow's runs: ask about one by its number,
            such as “Why did run 412 fail?”, or from the run's panel.
          </p>
        )}
        {messages?.map((m) => <Message key={m.id} message={m} onShowChanges={onShowChanges} />)}
      </div>
      <form
        className="assistant-form"
        onSubmit={(e) => {
          e.preventDefault()
          send()
        }}
      >
        <textarea
          ref={input}
          aria-label="Message to the assistant"
          placeholder={
            disabled
              ? 'Go back to the board as it is to ask'
              : runOpen !== null
                ? `Ask about run #${runOpen}, or for a change to the flow`
                : 'Ask for a flow, a change to it, or about a run'
          }
          rows={3}
          value={draft}
          disabled={disabled}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
              e.preventDefault()
              send()
            }
          }}
        />
        {replying ? (
          <button type="button" className="panel-button" onClick={stop}>
            <StopIcon /> Stop
          </button>
        ) : (
          <button type="submit" className="panel-button" disabled={disabled || sending || !draft.trim()}>
            Send
          </button>
        )}
      </form>
    </section>
  )
}

function Message({ message: m, onShowChanges }: { message: AssistantMessage; onShowChanges: (version: number) => void }) {
  if (m.role === 'person') return <div className="assistant-message from-person">{m.content}</div>
  if (m.status === 'active') return <div className="assistant-message from-assistant working">Working on it…</div>
  const version = m.version
  return (
    <div className="assistant-message from-assistant">
      {m.content && (
        <div className="data-markdown">
          <Markdown
            remarkPlugins={[remarkGfm]}
            components={{ a: ({ node: _, ...props }) => <a {...props} target="_blank" rel="noreferrer" /> }}
          >
            {m.content}
          </Markdown>
        </div>
      )}
      {m.applied && (
        <p className="assistant-applied">
          Changed the flow. The board before is saved as a version; Cmd-Z undoes it.
          {version !== null && (
            <>
              {' '}
              <button className="link-button" onClick={() => onShowChanges(version)}>
                Show what changed
              </button>
            </>
          )}
        </p>
      )}
      {m.error && <p className="assistant-error">{m.error}</p>}
    </div>
  )
}
