import { useEffect, useMemo, useRef, useState } from 'react'
import { getRunLogs, getStepChanges, getStepOutput, type AgentMessage, type OutputChunk, type RunLogLine, type FileChange, type MessageBlock, type RepoChanges, type RunStep, type StepMessage } from './api'
import { formatTime, formatUsage, STEP_LABELS, useStepMessages } from './Runs'

// An agent step's conversation as a terminal shows it, in a large window in
// the middle of the screen: the prompt, what the model thinks and says, each
// tool it calls with what came back, and an edit's diff, all of it in full,
// one long page scrolled as a terminal is. While the step goes it follows
// along, keeping the newest in view when scrolled to the end. Beside it, for
// a step working on repositories, the files it changed with their diffs:
// once it ended, what it committed (see changes.go); while it goes, what its
// edit and write calls did, as a command's changes show only in the commits.
// A script's step shows the script, what it was given and what it prints,
// stdout and stderr as they came, followed as it runs; one from before that
// was kept shows the lines the worker logged of it and its output instead.
export default function StepTerminal({ run, step, onClose }: { run: number; step: RunStep; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null)
  const screen = useRef<HTMLDivElement>(null)
  const atEnd = useRef(true)
  const script = step.kind === 'script'
  const { messages, loaded, error, going } = useStepMessages(run, step, !script)
  const logs = useStepLogs(run, step.id, script, going)
  const printed = useStepOutput(run, step.id, script, going)
  const results = useMemo(() => resultsByCall(messages), [messages])
  const followUpOf = new Map(step.followUps.map((f) => [f.attempt, f]))

  useEffect(() => {
    const dialog = ref.current
    if (!dialog) return
    dialog.showModal()
    return () => dialog.close()
  }, [])

  useEffect(() => {
    if (atEnd.current && screen.current) screen.current.scrollTop = screen.current.scrollHeight
  }, [messages.length, loaded, logs.lines?.length, printed.chunks?.length])

  return (
    <dialog
      ref={ref}
      className="review-dialog step-terminal"
      aria-label={`What ${step.processName} did`}
      onClose={onClose}
      // Escape closes the dialog, not the run's panel it opened from.
      onKeyDown={(e) => e.stopPropagation()}
    >
      <header className="panel-header">
        <h2>
          {step.processName}
          <span className="run-logs-live">
            {' '}
            · {STEP_LABELS[step.status]}
            {going && ', following along'} · started {formatTime(step.startedAt)}
            {step.model && ` · ${step.model}`}
            {step.usage && ` · ${formatUsage(step.usage)}`}
          </span>
        </h2>
        <button className="panel-close" onClick={onClose} aria-label="Close">
          ×
        </button>
      </header>
      <div className={`terminal-body${step.branch ? ' with-files' : ''}`}>
        <div
          ref={screen}
          className="terminal-screen"
          onScroll={(e) => {
            const el = e.currentTarget
            atEnd.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
          }}
        >
          {script ? (
            <ScriptScreen step={step} printed={printed.chunks} logs={logs.lines} error={printed.error || logs.error} going={going} />
          ) : (
            <>
              {error && <p className="term-error">The conversation couldn't load: {error}</p>}
              {!loaded && !error && <p className="term-dim">Loading…</p>}
              {loaded && messages.length === 0 && (
                <p className="term-dim">{going ? 'Nothing yet.' : 'No conversation was recorded for this step.'}</p>
              )}
              {messages.map((m, i) => (
                <div key={m.id}>
                  {m.attempt > 1 && messages[i - 1]?.attempt !== m.attempt && (
                    <p className="term-rule">
                      {followUpOf.has(m.attempt)
                        ? `Follow-up · ${formatTime(followUpOf.get(m.attempt)!.startedAt)}`
                        : `Try ${m.attempt}: the agent didn't hand in its output, so it was asked again`}
                    </p>
                  )}
                  <TerminalMessage message={m.message} results={results} going={going} />
                </div>
              ))}
              {going && loaded && (
                <p className="term-dim">
                  <span className="term-cursor" aria-hidden="true" /> working…
                </p>
              )}
            </>
          )}
        </div>
        {step.branch && <ChangedFiles run={run} step={step} messages={messages} going={going} />}
      </div>
    </dialog>
  )
}

// The most log lines the webapp gives at once; a full page means there are more.
const LOGS_PAGE = 5000
const LOGS_REFRESH_MS = 1500

// The run's log lines about the step, followed while it goes, and once
// more as it ends for the last ones.
function useStepLogs(run: number, step: number, enabled: boolean, going: boolean) {
  const [lines, setLines] = useState<RunLogLine[] | null>(null)
  const [error, setError] = useState('')
  const last = useRef(0)
  useEffect(() => {
    if (!enabled) return
    let stale = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const fetchMore = () => {
      getRunLogs(run, last.current)
        .then((more) => {
          if (stale) return
          setError('')
          const full = more.length >= LOGS_PAGE
          if (more.length > 0) last.current = more[more.length - 1].id
          setLines((ls) => [...(ls ?? []), ...more.filter((l) => l.step === step)])
          if (full || going) timer = setTimeout(fetchMore, full ? 0 : LOGS_REFRESH_MS)
        })
        .catch((err) => !stale && setError(err instanceof Error ? err.message : String(err)))
    }
    fetchMore()
    return () => {
      stale = true
      clearTimeout(timer)
    }
  }, [run, step, enabled, going])
  return { lines, error }
}

// What the script step printed, followed while it goes, and once more as
// it ends for the last of it.
function useStepOutput(run: number, step: number, enabled: boolean, going: boolean) {
  const [chunks, setChunks] = useState<OutputChunk[] | null>(null)
  const [error, setError] = useState('')
  const last = useRef(0)
  useEffect(() => {
    if (!enabled) return
    let stale = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const fetchMore = () => {
      getStepOutput(run, step, last.current)
        .then((more) => {
          if (stale) return
          setError('')
          if (more.length > 0) last.current = more[more.length - 1].id
          setChunks((cs) => (more.length === 0 && cs ? cs : [...(cs ?? []), ...more]))
          // A full page means there is more.
          if (more.length >= OUTPUT_PAGE || going) timer = setTimeout(fetchMore, more.length >= OUTPUT_PAGE ? 0 : OUTPUT_REFRESH_MS)
        })
        .catch((err) => !stale && setError(err instanceof Error ? err.message : String(err)))
    }
    fetchMore()
    return () => {
      stale = true
      clearTimeout(timer)
    }
  }, [run, step, enabled, going])
  return { chunks, error }
}

const OUTPUT_PAGE = 1000
const OUTPUT_REFRESH_MS = 1000

const formatLogTime = (s: string) =>
  new Date(s).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' })

function ScriptScreen({
  step,
  printed,
  logs,
  error,
  going,
}: {
  step: RunStep
  printed: OutputChunk[] | null
  logs: RunLogLine[] | null
  error: string
  going: boolean
}) {
  // A step from before what scripts print was kept as they ran.
  const streamed = going || (printed !== null && printed.length > 0)
  // A script's error starts with what it printed to stderr, shown already.
  const stderr = (printed ?? [])
    .filter((c) => c.stream === 'stderr')
    .map((c) => c.text)
    .join('')
    .trim()
  const failure = stderr && step.error.startsWith(stderr) ? step.error.slice(stderr.length).trim() || 'It failed.' : step.error
  return (
    <>
      <div className="term-prompt">
        <span className="term-mark">$</span>
        <pre>bash script</pre>
      </div>
      <pre className="term-script">{step.prompt}</pre>
      {step.inputs.map((input, i) => (
        <div key={i} className="term-call">
          <div className="term-call-line">
            <span className="term-mark">⏺</span>
            <pre>
              $INPUTS/{input.name}
              {input.state && ` · ${input.state}`}
            </pre>
          </div>
          <div className="term-result">
            <span className="term-mark">⎿</span>
            <pre>{input.content || '(empty)'}</pre>
          </div>
        </div>
      ))}
      {error && <p className="term-error">What it printed couldn't load: {error}</p>}
      {streamed && printed !== null && printed.length > 0 && (
        <pre className="term-output">
          {printed.map((c) => (
            <span key={c.id} className={c.stream}>
              {c.text}
            </span>
          ))}
        </pre>
      )}
      {streamed && !going && (failure ? <pre className="term-error">{failure}</pre> : <p className="term-dim">⏺ It ended without an error.</p>)}
      {!streamed && logs === null && !error && <p className="term-dim">Loading…</p>}
      {!streamed && logs?.map((l) => (
        <div key={l.id} className={`term-log ${l.level}`}>
          <time dateTime={l.time} title={new Date(l.time).toLocaleString()}>
            {formatLogTime(l.time)}
          </time>
          <pre>{l.message}</pre>
        </div>
      ))}
      {going ? (
        <p className="term-dim">
          <span className="term-cursor" aria-hidden="true" /> running…
        </p>
      ) : streamed ? null : (
        <>
          {step.output && (
            <div className="term-says">
              <span className="term-mark">⏺</span>
              <pre>{step.output}</pre>
            </div>
          )}
          {step.error && <pre className="term-error">{step.error}</pre>}
          {!step.output && !step.error && <p className="term-dim">It printed nothing.</p>}
        </>
      )}
    </>
  )
}

const blocksOf = (m: AgentMessage): MessageBlock[] =>
  typeof m.content === 'string' ? [{ type: 'text', text: m.content }] : (m.content ?? [])

const textOf = (m: AgentMessage) =>
  blocksOf(m)
    .map((b) => (b.type === 'text' ? b.text : b.type === 'image' ? `(${b.mimeType} image)` : ''))
    .filter(Boolean)
    .join('\n')

// Each tool's result by the id of the call it answers.
function resultsByCall(messages: StepMessage[]): Map<string, AgentMessage> {
  const results = new Map<string, AgentMessage>()
  for (const { message: m } of messages) if (m.role === 'toolResult' && m.toolCallId) results.set(m.toolCallId, m)
  return results
}

type Args = Record<string, unknown>
const str = (v: unknown) => (typeof v === 'string' ? v : v === undefined ? '' : JSON.stringify(v))

// A call as a line: the tool's name and what it works on.
function callLine(name: string, a: Args): string {
  const title = name.charAt(0).toUpperCase() + name.slice(1)
  switch (name) {
    case 'bash':
      return `${title}(${str(a.command)})`
    case 'read': {
      const range = a.offset !== undefined || a.limit !== undefined ? ` · from line ${str(a.offset ?? 1)}${a.limit !== undefined ? `, ${str(a.limit)} lines` : ''}` : ''
      return `${title}(${str(a.path)}${range})`
    }
    case 'edit':
    case 'write':
    case 'ls':
      return `${title}(${str(a.path)})`
    case 'grep':
    case 'find':
      return `${title}(${str(a.pattern)}${a.path ? ` in ${str(a.path)}` : ''})`
    default:
      return `${title}(${JSON.stringify(a)})`
  }
}

// An edit's replacements, which models send as a list, a list in a string,
// or a single one beside the path.
function editsOf(a: Args): { oldText: string; newText: string }[] {
  let edits: unknown = a.edits
  if (typeof edits === 'string') {
    try {
      edits = JSON.parse(edits)
    } catch {
      edits = undefined
    }
  }
  const list = Array.isArray(edits) ? edits : edits && typeof edits === 'object' ? [edits] : []
  if (typeof a.oldText === 'string' && typeof a.newText === 'string') list.push({ oldText: a.oldText, newText: a.newText })
  return list.filter((e): e is { oldText: string; newText: string } => typeof e?.oldText === 'string' && typeof e?.newText === 'string')
}

const linesOf = (text: string, mark: string) => (text === '' ? [] : text.replace(/\n$/, '').split('\n').map((l) => mark + l))

// What an edit or write did, as a patch: the edit's own once it returned,
// else made from its replacements, or a written file's lines as added.
function callPatch(name: string, a: Args, result: AgentMessage | undefined): string {
  if (name === 'edit') {
    if (result?.details?.patch) return result.details.patch
    return editsOf(a)
      .map((e) => ['@@ replaced @@', ...linesOf(e.oldText, '-'), ...linesOf(e.newText, '+')].join('\n'))
      .join('\n')
  }
  return ['@@ written @@', ...linesOf(str(a.content), '+')].join('\n')
}

function TerminalMessage({ message: m, results, going }: { message: AgentMessage; results: Map<string, AgentMessage>; going: boolean }) {
  if (m.omitted !== undefined) {
    return <p className="term-dim">A {m.role} message of {Math.round(m.omitted / 1024)} KB, too large to keep.</p>
  }
  switch (m.role) {
    case 'system':
      return (
        <details className="term-fold">
          <summary>System prompt</summary>
          <pre className="term-dim">{m.sections ? Object.values(m.sections).filter(Boolean).join('\n\n') : textOf(m)}</pre>
        </details>
      )
    case 'user':
      return (
        <div className="term-prompt">
          <span className="term-mark">&gt;</span>
          <pre>{textOf(m)}</pre>
        </div>
      )
    // Shown under its call; one without a call is shown on its own.
    case 'toolResult':
      return m.toolCallId && results.get(m.toolCallId) === m ? null : <ToolResult result={m} />
    case 'assistant':
      return (
        <>
          {blocksOf(m).map((b, i) =>
            b.type === 'thinking' ? (
              <div key={i} className="term-thinking">
                <span className="term-mark">✻</span>
                <pre>{b.redacted && !b.thinking ? '(thinking redacted)' : b.thinking}</pre>
              </div>
            ) : b.type === 'toolCall' ? (
              <ToolCall key={i} call={b} result={results.get(b.id)} going={going} />
            ) : b.type === 'text' ? (
              <div key={i} className="term-says">
                <span className="term-mark">⏺</span>
                <pre>{b.text}</pre>
              </div>
            ) : null,
          )}
          {(m.stopReason === 'error' || m.stopReason === 'aborted') && (
            <p className="term-error">
              {m.stopReason === 'error' ? 'The request failed' : 'The request was aborted'}
              {m.errorMessage && `: ${m.errorMessage}`}
            </p>
          )}
        </>
      )
    default:
      return <pre>{textOf(m)}</pre>
  }
}

function ToolCall({ call, result, going }: { call: Extract<MessageBlock, { type: 'toolCall' }>; result?: AgentMessage; going: boolean }) {
  const a = (call.arguments && typeof call.arguments === 'object' ? call.arguments : {}) as Args
  const changes = call.name === 'edit' || call.name === 'write'
  return (
    <div className="term-call">
      <div className={`term-call-line${result?.isError ? ' failed' : ''}`}>
        <span className="term-mark">⏺</span>
        <pre>{callLine(call.name, a)}</pre>
      </div>
      {changes && !result?.isError && <Patch text={callPatch(call.name, a, result)} />}
      {result ? <ToolResult result={result} /> : <p className="term-result term-dim">{going ? '⎿ running…' : '⎿ no result'}</p>}
    </div>
  )
}

function ToolResult({ result }: { result: AgentMessage }) {
  return (
    <div className={`term-result${result.isError ? ' failed' : ''}`}>
      <span className="term-mark">⎿</span>
      <pre>{textOf(result) || '(nothing)'}</pre>
    </div>
  )
}

// A unified diff, a line at a time coloured by what it is.
function Patch({ text }: { text: string }) {
  return (
    <pre className="term-patch">
      {text.split('\n').map((l, i) => {
        const kind =
          l.startsWith('diff --git') || l.startsWith('index ') || l.startsWith('--- ') || l.startsWith('+++ ')
            ? 'head'
            : l.startsWith('@@')
              ? 'hunk'
              : l.startsWith('+')
                ? 'add'
                : l.startsWith('-')
                  ? 'del'
                  : ''
        return (
          <span key={i} className={kind}>
            {l}
            {'\n'}
          </span>
        )
      })}
    </pre>
  )
}

// A file the agent's edit and write calls changed while it goes, with what
// each did, in order.
type LiveFile = { path: string; written: boolean; patches: string[] }

// Paths are relative to the workspace, which holds a checkout of each
// repository; an absolute one is cut to that too.
const workspacePath = (p: string) => p.replace(/^\.\//, '').replace(/^\/.*?\/run-\d+[^/]*\//, '')

function liveChanges(messages: StepMessage[], results: Map<string, AgentMessage>): LiveFile[] {
  const files = new Map<string, LiveFile>()
  for (const { message: m } of messages) {
    if (m.role !== 'assistant') continue
    for (const b of blocksOf(m)) {
      if (b.type !== 'toolCall' || (b.name !== 'edit' && b.name !== 'write')) continue
      const result = results.get(b.id)
      const a = (b.arguments && typeof b.arguments === 'object' ? b.arguments : {}) as Args
      if (!result || result.isError || typeof a.path !== 'string') continue
      const path = workspacePath(a.path)
      const f = files.get(path) ?? { path, written: false, patches: [] }
      f.written ||= b.name === 'write'
      f.patches.push(callPatch(b.name, a, result))
      files.set(path, f)
    }
  }
  return [...files.values()]
}

const STATUS_MARKS: Record<FileChange['status'], string> = {
  added: 'A',
  modified: 'M',
  deleted: 'D',
  renamed: 'R',
  copied: 'C',
  'type changed': 'T',
}

function ChangedFiles({ run, step, messages, going }: { run: number; step: RunStep; messages: StepMessage[]; going: boolean }) {
  const [changes, setChanges] = useState<RepoChanges[] | null>(null)
  const [error, setError] = useState('')
  const [open, setOpen] = useState<Set<string>>(new Set())
  const ended = step.followUps.filter((f) => f.status !== 'active').length
  const results = useMemo(() => resultsByCall(messages), [messages])
  const live = useMemo(() => liveChanges(messages, results), [messages, results])

  // What it committed, once it and each follow-up ended.
  useEffect(() => {
    if (going) return
    let stale = false
    getStepChanges(run, step.id)
      .then((cs) => !stale && (setChanges(cs), setError('')))
      .catch((err) => !stale && setError(err instanceof Error ? err.message : String(err)))
    return () => {
      stale = true
    }
  }, [run, step.id, going, ended])

  const toggle = (key: string) =>
    setOpen((o) => {
      const next = new Set(o)
      if (!next.delete(key)) next.add(key)
      return next
    })
  const committed = !going && changes !== null && changes.length > 0
  const keys = committed
    ? changes.flatMap((c, i) => c.files.map((f) => `${i}:${f.path}`))
    : live.map((f) => f.path)
  const allOpen = keys.length > 0 && keys.every((k) => open.has(k))

  return (
    <aside className="terminal-files" aria-label="Changed files">
      <header>
        <h3>Changed files</h3>
        {keys.length > 0 && (
          <button className="link-button" onClick={() => setOpen(allOpen ? new Set() : new Set(keys))}>
            {allOpen ? 'Hide all diffs' : 'Show all diffs'}
          </button>
        )}
      </header>
      {error && <p className="panel-warning">{error}</p>}
      {committed ? (
        changes.map((c, i) => (
          <section key={i} className="terminal-repo">
            <p className="terminal-repo-name">
              <strong>{c.name}</strong>{' '}
              <span className="panel-note">
                <code>{c.before.slice(0, 7)}</code>
                {c.after !== c.before && (
                  <>
                    {' '}
                    → <code>{c.after.slice(0, 7)}</code>
                  </>
                )}
              </span>
            </p>
            {c.uncommitted && <p className="panel-warning">It left changes that couldn't be committed.</p>}
            {c.error && <p className="panel-warning">{c.error}</p>}
            {!c.error && c.files.length === 0 && <p className="panel-note">No changes.</p>}
            <ul>
              {c.files.map((f) => {
                const key = `${i}:${f.path}`
                return (
                  <li key={key}>
                    <button className="terminal-file" onClick={() => toggle(key)} aria-expanded={open.has(key)}>
                      <span className={`terminal-file-status ${f.status.replace(' ', '-')}`} title={f.status}>
                        {STATUS_MARKS[f.status] ?? 'M'}
                      </span>
                      <span className="terminal-file-path">{f.oldPath ? `${f.oldPath} → ${f.path}` : f.path}</span>
                      {f.binary ? (
                        <span className="panel-note">binary</span>
                      ) : (
                        <span className="terminal-file-counts">
                          <span className="add">+{f.additions}</span> <span className="del">−{f.deletions}</span>
                        </span>
                      )}
                    </button>
                    {open.has(key) &&
                      (f.omitted ? (
                        <p className="panel-note">Too large to show here.</p>
                      ) : f.binary ? (
                        <p className="panel-note">A binary file.</p>
                      ) : (
                        <Patch text={f.patch.replace(/\n$/, '')} />
                      ))}
                  </li>
                )
              })}
            </ul>
            {c.more > 0 && <p className="panel-note">and {c.more} more files.</p>}
          </section>
        ))
      ) : (
        <>
          <p className="panel-note">
            {going && step.kind === 'script'
              ? 'What the script changes shows once it ends.'
              : going
                ? live.length === 0
                  ? 'No files yet. They show as the agent edits and writes them; what its commands change shows once it ends.'
                  : 'As the agent edits and writes them; what its commands change shows once it ends.'
                  : changes === null && !error
                  ? 'Loading…'
                  : live.length === 0
                    ? 'It changed nothing.'
                    : 'Nothing was committed; these are the files its edits and writes touched.'}
          </p>
          <ul>
            {live.map((f) => (
              <li key={f.path}>
                <button className="terminal-file" onClick={() => toggle(f.path)} aria-expanded={open.has(f.path)}>
                  <span className={`terminal-file-status ${f.written ? 'added' : 'modified'}`} title={f.written ? 'written' : 'edited'}>
                    {f.written ? 'W' : 'M'}
                  </span>
                  <span className="terminal-file-path">{f.path}</span>
                  <span className="panel-note">
                    {f.patches.length} {f.patches.length === 1 ? 'change' : 'changes'}
                  </span>
                </button>
                {open.has(f.path) && <Patch text={f.patches.join('\n')} />}
              </li>
            ))}
          </ul>
        </>
      )}
    </aside>
  )
}
