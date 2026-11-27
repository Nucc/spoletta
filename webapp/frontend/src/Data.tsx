import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import Markdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { isMarkdown } from './markdown'

export type Json = null | boolean | number | string | Json[] | { [key: string]: Json }

// How deep strings holding JSON are unwrapped, so odd content can't recurse forever.
const maxDepth = 8

// The names of the fields typed Markdown (attribute_types.go), whose text is
// rendered as Markdown wherever content holds them, at any depth, where other
// text is only when it reads as Markdown. A page showing a run's objects or a
// struct's records gives the names its run's version or struct has.
export const MarkdownFields = createContext<ReadonlySet<string>>(new Set())

// Content as the fields and items it holds when it is a JSON object or array,
// the way processes hand objects in, without the envelope an object without
// attributes holds its value in; Markdown rendered; other text as it is. Raw
// shows the text as it is. A label heads it, with Raw beside it rather than
// over the content, and Open, for content too long to read where it is, to
// read it in a dialog of its own. markdown says the text is the value of a
// field typed Markdown, rendered as such whatever it reads as.
export function Data({ text, label, markdown = false }: { text: string; label?: ReactNode; markdown?: boolean }) {
  const [raw, setRaw] = useState(false)
  const [open, setOpen] = useState(false)
  const parsed = markdown ? undefined : parse(text)
  const formatted = markdown || parsed !== undefined || isMarkdown(text)
  const toggle = formatted && (
    <button type="button" className="link-button data-toggle" onClick={() => setRaw(!raw)}>
      {raw ? 'Formatted' : 'Raw'}
    </button>
  )
  const body =
    !formatted || raw ? <pre>{text}</pre> : parsed === undefined ? <Text text={text} markdown={markdown} /> : <Value value={held(parsed)} />
  if (label === undefined) {
    if (!formatted) return body
    return (
      <div className="data">
        {toggle}
        {body}
      </div>
    )
  }
  const long = text.length > longText || text.split('\n').length > longLines
  return (
    <>
      <div className="data-head">
        <span>{label}</span>
        <span className="data-head-actions">
          {long && (
            <button type="button" className="link-button data-toggle" onClick={() => setOpen(true)} title="Read all of it in a window of its own">
              Open
            </button>
          )}
          {toggle}
        </span>
      </div>
      {formatted ? <div className="data">{body}</div> : body}
      {open && <DataDialog label={label} text={text} markdown={markdown} onClose={() => setOpen(false)} />}
    </>
  )
}

// How long a content is, in characters or lines, before Open offers to read
// it in a dialog, as a panel shows only so much of it.
const longText = 400
const longLines = 8

// A labelled content in full, as wide as a dialog, formatted as it is where
// it was opened from. It goes in the page's body, so the panel it was opened
// from doesn't cut it to its own height, and its keys and clicks stay its
// own, so Escape closes it and not the panel under it.
function DataDialog({ label, text, markdown, onClose }: { label: ReactNode; text: string; markdown: boolean; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])
  return createPortal(
    <dialog
      ref={ref}
      className="data-dialog"
      // React hands a dialog's close up to the dialogs it was opened in,
      // such as a review's, which would close with it.
      onClose={(e) => {
        e.stopPropagation()
        onClose()
      }}
      onKeyDown={(e) => e.stopPropagation()}
      onClick={(e) => {
        e.stopPropagation()
        if (e.target === ref.current) onClose()
      }}
    >
      <header>
        <h2>{label}</h2>
        <button type="button" className="panel-close" onClick={onClose} aria-label="Close" title="Close">
          ×
        </button>
      </header>
      <Data text={text} markdown={markdown} />
    </dialog>,
    document.body,
  )
}

const onlyData = (v: Json): v is { data: Json } =>
  v !== null && typeof v === 'object' && !Array.isArray(v) && Object.keys(v).length === 1 && 'data' in v

// An object, or a list of them, out of the envelope of one without attributes.
const opened = (v: Json): Json => (onlyData(v) ? v.data : Array.isArray(v) ? v.map((e) => (onlyData(e) ? e.data : e)) : v)

// What content holds as a process hands it in: an object without attributes
// holds its value under data (see attributes.go), alone or in a list of
// them, and an output of several objects holds each under its name. Deeper
// down, a field named data is the content's own.
function held(v: Json): Json {
  if (onlyData(v) || Array.isArray(v)) return opened(v)
  if (v !== null && typeof v === 'object') return Object.fromEntries(Object.entries(v).map(([k, e]) => [k, opened(e)]))
  return v
}

// Text, rendered as Markdown when it is a field typed so or reads as it.
function Text({ text, markdown = false }: { text: string; markdown?: boolean }) {
  if (!markdown && !isMarkdown(text)) return <span className="data-text">{text}</span>
  return <MarkdownText text={text} />
}

// How Markdown a run or a person wrote is rendered: links open in a tab of
// their own, and an image is a link to it rather than loaded, since loading
// it would have whoever reads an agent's output fetch whatever address the
// agent wrote, past the hosts its project allows (egress.go). Raw HTML is
// never rendered, as react-markdown leaves it out.
const markdownComponents: Components = {
  a: ({ node: _, ...props }) => <a {...props} target="_blank" rel="noreferrer" />,
  img: ({ src, alt }) => (
    <a href={typeof src === 'string' ? src : undefined} target="_blank" rel="noreferrer">
      {alt ? `Image: ${alt}` : 'Image'}
    </a>
  ),
}

// Markdown text rendered, as content shows it.
export function MarkdownText({ text }: { text: string }) {
  return (
    <div className="data-markdown">
      <Markdown remarkPlugins={[remarkGfm]} components={markdownComponents}>
        {text}
      </Markdown>
    </div>
  )
}

// The JSON object or array the text holds, inside a code fence or not.
export function parse(text: string): Json | undefined {
  let s = text.trim()
  if (s.startsWith('```') && s.endsWith('```') && s.length >= 6) {
    s = s.slice(3, -3)
    const nl = s.indexOf('\n')
    if (nl >= 0) s = s.slice(nl + 1)
    s = s.trim()
  }
  if (!s.startsWith('{') && !s.startsWith('[')) return undefined
  try {
    const v = unwrap(JSON.parse(s) as Json, 0)
    return v !== null && typeof v === 'object' ? v : undefined
  } catch {
    return undefined
  }
}

// The value with strings that hold JSON, an object, an array or a quoted
// string, replaced by what they hold. Agents often hand in values encoded twice.
function unwrap(v: Json, depth: number): Json {
  if (depth > maxDepth) return v
  if (typeof v === 'string') {
    const s = v.trim()
    if (!s.startsWith('{') && !s.startsWith('[') && !s.startsWith('"')) return v
    try {
      const inner = JSON.parse(s) as Json
      if (typeof inner === 'string' || (inner !== null && typeof inner === 'object')) return unwrap(inner, depth + 1)
    } catch {
      // Not JSON after all; the text is shown as it is.
    }
    return v
  }
  if (Array.isArray(v)) return v.map((e) => unwrap(e, depth + 1))
  if (v !== null && typeof v === 'object') {
    return Object.fromEntries(Object.entries(v).map(([k, e]) => [k, unwrap(e, depth + 1)]))
  }
  return v
}

// A value, which markdown says is held by a field typed Markdown.
function Value({ value, markdown = false }: { value: Json; markdown?: boolean }) {
  const typed = useContext(MarkdownFields)
  if (value === null) return <span className="data-scalar">null</span>
  if (typeof value === 'string') return value === '' ? <span className="data-scalar">(empty)</span> : <Text text={value} markdown={markdown} />
  if (typeof value !== 'object') return <span className="data-scalar">{String(value)}</span>
  if (Array.isArray(value)) {
    if (value.length === 0) return <span className="data-scalar">(none)</span>
    if (value.every((e) => e === null || typeof e !== 'object')) {
      return (
        <ul className="data-list">
          {value.map((e, i) => (
            <li key={i}>
              <Value value={e} markdown={markdown} />
            </li>
          ))}
        </ul>
      )
    }
    return (
      <ol className="data-items">
        {value.map((e, i) => (
          <li key={i}>
            <Value value={e} markdown={markdown} />
          </li>
        ))}
      </ol>
    )
  }
  const entries = Object.entries(value)
  if (entries.length === 0) return <span className="data-scalar">(no fields)</span>
  return (
    <dl className="data-fields">
      {entries.map(([k, e]) => (
        // A value beside its name; one with fields of its own, or a text to
        // read, under it.
        <div key={k} className={isNested(e) || isProse(e, typed.has(k)) ? 'data-wide' : undefined}>
          <dt>{k}</dt>
          <dd>
            <Value value={e} markdown={typed.has(k)} />
          </dd>
        </div>
      ))}
    </dl>
  )
}

// How long a line of text may be and still show beside its name.
const shortText = 160

// Whether the value is a text to read rather than a value: lines, Markdown, a
// field typed Markdown or a long line.
function isProse(v: Json, markdown: boolean): boolean {
  return typeof v === 'string' && v !== '' && (markdown || v.includes('\n') || v.length > shortText || isMarkdown(v))
}

// Whether the value holds fields of its own, as an object or a list of them.
function isNested(v: Json): boolean {
  if (v === null || typeof v !== 'object') return false
  return !Array.isArray(v) || v.some((e) => e !== null && typeof e === 'object')
}
