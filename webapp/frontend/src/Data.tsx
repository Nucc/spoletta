import { useState, type ReactNode } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'

export type Json = null | boolean | number | string | Json[] | { [key: string]: Json }

// How deep strings holding JSON are unwrapped, so odd content can't recurse forever.
const maxDepth = 8

// Content as the fields and items it holds when it is a JSON object or array,
// the way processes hand objects in, without the envelope an object without
// attributes holds its value in; Markdown rendered; other text as it is. Raw
// shows the text as it is. A label heads it, with Raw beside it rather than
// over the content.
export function Data({ text, label }: { text: string; label?: ReactNode }) {
  const [raw, setRaw] = useState(false)
  const parsed = parse(text)
  const formatted = parsed !== undefined || isMarkdown(text)
  const toggle = formatted && (
    <button className="link-button data-toggle" onClick={() => setRaw(!raw)}>
      {raw ? 'Formatted' : 'Raw'}
    </button>
  )
  const body =
    !formatted || raw ? <pre>{text}</pre> : parsed === undefined ? <Text text={text} /> : <Value value={held(parsed)} />
  if (label === undefined) {
    if (!formatted) return body
    return (
      <div className="data">
        {toggle}
        {body}
      </div>
    )
  }
  return (
    <>
      <div className="data-head">
        <span>{label}</span>
        {toggle}
      </div>
      {formatted ? <div className="data">{body}</div> : body}
    </>
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

// Lines that only Markdown starts with: headings, list items, quotes, fences
// and table rules.
const markdownLine = /^(#{1,6} \S|\s*[-*+] \S|\s*\d+[.)] \S|> |```|\|?\s*:?-{3,}:?\s*\|)/m
// Inline marks: bold, links and code.
const markdownInline = /\*\*\S[^*\n]*\*\*|\[[^\]\n]+\]\([^)\s]+\)|`[^`\n]+`/g

// Whether the text reads as Markdown: a heading, list, quote, fence or table,
// or a few inline marks, so a stray asterisk or backtick doesn't count.
function isMarkdown(text: string): boolean {
  return markdownLine.test(text) || (text.match(markdownInline)?.length ?? 0) >= 2
}

function Text({ text }: { text: string }) {
  if (!isMarkdown(text)) return <span className="data-text">{text}</span>
  return (
    <div className="data-markdown">
      <Markdown
        remarkPlugins={[remarkGfm]}
        components={{ a: ({ node: _, ...props }) => <a {...props} target="_blank" rel="noreferrer" /> }}
      >
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

function Value({ value }: { value: Json }) {
  if (value === null) return <span className="data-scalar">null</span>
  if (typeof value === 'string') return value === '' ? <span className="data-scalar">(empty)</span> : <Text text={value} />
  if (typeof value !== 'object') return <span className="data-scalar">{String(value)}</span>
  if (Array.isArray(value)) {
    if (value.length === 0) return <span className="data-scalar">(none)</span>
    if (value.every((e) => e === null || typeof e !== 'object')) {
      return (
        <ul className="data-list">
          {value.map((e, i) => (
            <li key={i}>
              <Value value={e} />
            </li>
          ))}
        </ul>
      )
    }
    return (
      <ol className="data-items">
        {value.map((e, i) => (
          <li key={i}>
            <Value value={e} />
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
        <div key={k} className={isNested(e) || isProse(e) ? 'data-wide' : undefined}>
          <dt>{k}</dt>
          <dd>
            <Value value={e} />
          </dd>
        </div>
      ))}
    </dl>
  )
}

// How long a line of text may be and still show beside its name.
const shortText = 160

// Whether the value is a text to read rather than a value: lines, Markdown or
// a long line.
function isProse(v: Json): boolean {
  return typeof v === 'string' && (v.includes('\n') || v.length > shortText || isMarkdown(v))
}

// Whether the value holds fields of its own, as an object or a list of them.
function isNested(v: Json): boolean {
  if (v === null || typeof v !== 'object') return false
  return !Array.isArray(v) || v.some((e) => e !== null && typeof e === 'object')
}
