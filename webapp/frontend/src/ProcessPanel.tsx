import { useEffect, useRef, useState } from 'react'
import {
  DEFAULT_LOOPS,
  DEFAULT_ROUNDS,
  MAX_LOOPS,
  MAX_RETRIES,
  MAX_ROUNDS,
  MAX_TIMEOUT,
  MAX_WAIT,
  THINKING_LEVELS,
  listObjects,
  updateProcess,
  type CredentialChoice,
  type Entries,
  type EntryValue,
  type Flow,
  type MCPServer,
  type Skill,
  type Repository,
  type Models,
  type ProcessKind,
  type ProcessSettings,
  type RunInput,
  type ThinkingLevel,
  type LinkQuery,
} from './api'
import { Data } from './Data'
import { quantityWords, type Quantity } from './quantity'
import { fieldsOf, holdsFile, shapeOf, type Field } from './structure'
import { MCPServerPicker } from './MCPSettings'
import { PromptEditor, SkillPicker } from './Skills'
import { CredentialPicker } from './Credentials'
import { ConnectorFields } from './ConnectorFields'
import { Hint } from './Hints'
import { RepositoryPicker } from './FlowSettings'
import { ModelOptions } from './ModelProviders'
import type { RunStatus } from './ProcessNode'

type Fields = {
  name: string
  kind: ProcessKind
  prompt: string
  script: string
  model: string
  thinking: ThinkingLevel | ''
  recording: boolean
  settings: ProcessSettings
}

const SAVE_DELAY_MS = 500

const SCRIPT_PLACEHOLDER = `# e.g. the open bugs in Jira, through a credential the process picks:
curl -sf -H "Authorization: Bearer $CREDENTIALS_TOKEN" \\
  "$CREDENTIAL_JIRA_URL/rest/api/2/search?jql=type=Bug+AND+status=Open" |
  jq '[.issues[] | {key, summary: .fields.summary}]'`

const KINDS: { value: ProcessKind; label: string }[] = [
  { value: 'agent', label: 'Agent' },
  { value: 'script', label: 'Script' },
  { value: 'human', label: 'Human' },
  { value: 'flow', label: 'Flow' },
  { value: 'subflow', label: 'Procedure' },
  { value: 'connector', label: 'Connector' },
]

// What each kind of process is, as its panel says above what it edits.
const KIND_HELP: Record<ProcessKind, string> = {
  agent: 'An AI agent does what the prompt says with what the process takes; its inputs and what to hand in are added for it.',
  script: 'A bash script: what the process takes is in $INPUTS, and what it prints is its output.',
  human: 'A person reviews what it takes, deciding the states its links list, or fills in what it generates.',
  flow: 'A board of its own, opened with its magnifier; taking a list, it runs once per item.',
  subflow: 'Runs a procedure, a flow reused in others, giving it the objects named as those this process takes.',
  connector: 'Carries out one operation of an installed connector, such as posting to Slack.',
}

// An object a human process generates without deciding on its states, which
// the person carrying it out fills in.
export type EntryObject = Quantity & {
  id: number
  name: string
  // The fields the person fills in, nested as the objects it is made of are.
  fields: Field[]
}

// A human process waiting for its decision during a run.
export type Review = {
  inputs: RunInput[]
  // The states the process's output links name, which it decides between.
  options: { state: number; object: string; name: string }[]
  // The objects the person fills in.
  entries: EntryObject[]
  // The roles that decide it, none when anyone does, and whether the person
  // looking may.
  handledBy: string[]
  canDecide: boolean
  // Null when there are no states to set, which ends the branch, unless
  // there are objects filled in. Resolves to whether the server took it.
  onDecide: (state: number | null, instructions: string, entries?: Entries) => Promise<boolean>
  // For a process going round a loop: what it asks, whether the process
  // goes on, instead of states to set; see loops.go.
  loop?: { question: string; onAnswer: (goOn: boolean) => Promise<boolean> }
}

// Edits one process; mount with key={id} so switching processes resets the form.
export default function ProcessPanel({
  id,
  trashedFlow,
  initial,
  models,
  mcpServers,
  skills,
  commands,
  credentials,
  readOnly = false,
  run,
  review,
  onChange,
  onZoomIn,
  flows,
  onFlowSaved,
  takes,
  generates,
  queries,
  onQuery,
  shapes,
  onOpenMCPSettings,
  repositories,
  onOpenFlowSettings,
  onTry,
  onClose,
  onError,
}: {
  id: number
  // For a subflow process, the procedure it runs if that is in the trash.
  trashedFlow?: string
  initial: Fields
  // Models pi offers, or null while they load.
  models: Models | null
  // The MCP servers agents can use, or null while they load.
  mcpServers: MCPServer[] | null
  // The skills its agent may be given and the commands its prompt may name,
  // those its flow may use, or null while they load.
  skills: Skill[] | null
  commands: Skill[] | null
  // The credentials its agent or script may call services with, those its
  // flow may use, or null while they load.
  credentials: CredentialChoice[] | null
  // Shows the process without letting it change, such as as a past run's version has it.
  readOnly?: boolean
  // How this process did in the latest flow run, if it has run.
  run?: { status?: RunStatus; output?: string; error?: string }
  review?: Review
  onChange: (fields: Fields) => void
  // Shows a flow process's board, or the flow a subflow process runs.
  onZoomIn: () => void
  // The flows a subflow process can run, null while they load, and the
  // names of the objects it takes and generates.
  flows: Flow[] | null
  // A subflow process's flow was saved, which brings its outcomes onto the board.
  onFlowSaved: () => void
  takes: string[]
  generates: string[]
  // Its input links an agent or script can take as queries of stored records.
  queries: QueryLink[]
  onQuery: (link: number, query: LinkQuery | null) => void
  // The shape of what each of those objects holds, by name; see shapeOf.
  shapes: Record<string, string>
  onOpenMCPSettings: () => void
  // The flow's repositories, which agents and scripts pick from.
  repositories: Repository[]
  onOpenFlowSettings: () => void
  // Opens the dialog trying an agent, script or connector process on its
  // own; see TrialDialog.
  onTry?: () => void
  onClose: () => void
  onError: (err: unknown) => void
}) {
  const [name, setName] = useState(initial.name)
  const [kind, setKind] = useState(initial.kind)
  const [prompt, setPrompt] = useState(initial.prompt)
  const [script, setScript] = useState(initial.script)
  const [model, setModel] = useState(initial.model)
  const [thinking, setThinking] = useState(initial.thinking)
  const [recording, setRecording] = useState(initial.recording)
  const [settings, setSettings] = useState(initial.settings)
  const saved = useRef(initial)
  const pending = useRef<Partial<Fields> | null>(null)
  const timer = useRef<number>(undefined)
  const bodyRef = useRef<HTMLTextAreaElement>(null)

  const flush = () => {
    window.clearTimeout(timer.current)
    const patch = pending.current
    pending.current = null
    if (!patch) return
    const kindNow = saved.current.kind
    updateProcess(id, patch)
      .then(() => {
        if (kindNow === 'subflow' && (patch.kind !== undefined || patch.settings !== undefined)) onFlowSaved()
      })
      .catch(onError)
  }

  // Autosave shortly after typing stops, and flush whatever is left when the panel closes.
  useEffect(() => {
    const trimmed = name.trim()
    const patch: Partial<Fields> = {}
    if (trimmed !== '' && trimmed !== saved.current.name) patch.name = trimmed
    if (kind !== saved.current.kind) patch.kind = kind
    if (prompt !== saved.current.prompt) patch.prompt = prompt
    if (script !== saved.current.script) patch.script = script
    if (model !== saved.current.model) patch.model = model
    if (thinking !== saved.current.thinking) patch.thinking = thinking
    if (recording !== saved.current.recording) patch.recording = recording
    if (JSON.stringify(settings) !== JSON.stringify(saved.current.settings)) patch.settings = settings
    if (Object.keys(patch).length === 0) return

    saved.current = { ...saved.current, ...patch }
    pending.current = { ...pending.current, ...patch }
    onChange(saved.current)
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(flush, SAVE_DELAY_MS)
  }, [name, kind, prompt, script, model, thinking, recording, settings])

  useEffect(() => flush, [])

  // Absent means every repository, so unpicking the last one keeps [].
  const repositoryPicker = (
    <RepositoryPicker
      repositories={repositories}
      selected={settings.repositories}
      readOnly={readOnly}
      onChange={(names) =>
        setSettings(({ repositories: _, ...rest }) => (names === undefined ? rest : { ...rest, repositories: names }))
      }
      onOpenSettings={onOpenFlowSettings}
    />
  )

  const credentialPicker = (
    <CredentialPicker
      credentials={credentials}
      selected={settings.credentials ?? []}
      readOnly={readOnly}
      onChange={(ids) => setSettings(({ credentials: _, ...rest }) => (ids.length > 0 ? { ...rest, credentials: ids } : rest))}
    />
  )

  // Both bodies are kept, so switching type back and forth loses nothing.
  useEffect(() => {
    bodyRef.current?.focus()
  }, [kind])

  return (
    <aside className="panel" onKeyDown={(e) => e.key === 'Escape' && onClose()}>
      <header className="panel-header">
        <h2>Process{readOnly && <span className="run-badge">read-only</span>}</h2>
        <button className="panel-close" onClick={onClose} aria-label="Close panel">
          ×
        </button>
      </header>
      {!readOnly && (
        <Hint id={`panel-process-${kind}`} className="panel-hint">
          {KIND_HELP[kind]}
        </Hint>
      )}
      <label className="panel-field">
        <span>Name</span>
        <input value={name} readOnly={readOnly} onChange={(e) => setName(e.target.value)} />
        {name.trim() === '' && <small className="panel-warning">Name can't be empty.</small>}
      </label>
      <div className="panel-field" role="radiogroup" aria-label="Type">
        <span>Type</span>
        <div className="segmented">
          {KINDS.map((k) => (
            <button
              key={k.value}
              role="radio"
              aria-checked={kind === k.value}
              className={kind === k.value ? 'selected' : ''}
              disabled={readOnly}
              onClick={() => setKind(k.value)}
            >
              {k.label}
            </button>
          ))}
        </div>
      </div>
      {kind === 'agent' && (
        <div className="panel-row">
          <label className="panel-field">
            <span>Model</span>
            <select value={model} disabled={readOnly} onChange={(e) => setModel(e.target.value)}>
              <option value="">Default{models?.default ? ` (${models.default})` : ''}</option>
              {/* A model dropped from pi's list stays selectable so it isn't silently replaced. */}
              {model !== '' && !models?.models.includes(model) && <option value={model}>{model}</option>}
              {models && <ModelOptions models={models} />}
            </select>
          </label>
          <label className="panel-field">
            <span>Thinking</span>
            <select value={thinking} disabled={readOnly} onChange={(e) => setThinking(e.target.value as ThinkingLevel | '')}>
              <option value="">Default</option>
              {THINKING_LEVELS.map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </select>
          </label>
        </div>
      )}
      {kind === 'connector' ? (
        <ConnectorFields
          key="connector"
          servers={mcpServers}
          settings={settings.connector}
          takes={takes}
          readOnly={readOnly}
          onChange={(connector) =>
            setSettings(({ connector: _, ...rest }) => (connector === undefined ? rest : { ...rest, connector }))
          }
          onError={onError}
        />
      ) : kind === 'flow' ? (
        <div key="flow" className="panel-field">
          <p className="panel-note">
            Made of processes on a board of its own. The objects it takes show at the board's start, the ones it
            generates at its end.
          </p>
          <label className="panel-check">
            <input type="checkbox" checked={recording} disabled={readOnly} onChange={(e) => setRecording(e.target.checked)} />
            <span>Record each time it runs as a run of its own</span>
          </label>
          <button className="panel-button" onClick={onZoomIn}>
            Open its board
          </button>
        </div>
      ) : kind === 'subflow' ? (
        <SubflowFields
          key="subflow"
          flows={flows}
          flow={settings.flow}
          trashed={trashedFlow !== undefined && settings.flow === initial.settings.flow ? trashedFlow : undefined}
          takes={takes}
          generates={generates}
          shapes={shapes}
          readOnly={readOnly}
          onChange={(flow) => setSettings(({ flow: _, ...rest }) => (flow === undefined ? rest : { ...rest, flow }))}
          onOpen={onZoomIn}
          onError={onError}
        />
      ) : kind === 'agent' ? (
        <>
          <label key="agent" className="panel-field panel-field-grow">
            <span>Prompt</span>
            <PromptEditor
              textareaRef={bodyRef}
              readOnly={readOnly}
              value={prompt}
              commands={commands}
              placeholder={commands?.length ? 'Describe what this process should do… Type / for a command.' : 'Describe what this process should do…'}
              onChange={setPrompt}
            />
          </label>
          <SkillPicker
            skills={skills}
            selected={settings.skills ?? []}
            readOnly={readOnly}
            onChange={(ids) => setSettings(({ skills: _, ...rest }) => (ids.length > 0 ? { ...rest, skills: ids } : rest))}
          />
          {credentialPicker}
          <MCPServerPicker
            servers={mcpServers}
            selected={settings.mcpServers ?? []}
            tools={settings.mcpTools ?? {}}
            readOnly={readOnly}
            onChange={(ids) =>
              setSettings(({ mcpServers: _, ...rest }) => (ids.length > 0 ? { ...rest, mcpServers: ids } : rest))
            }
            onToolsChange={(tools) =>
              setSettings(({ mcpTools: _, ...rest }) => (Object.keys(tools).length > 0 ? { ...rest, mcpTools: tools } : rest))
            }
            onOpenSettings={onOpenMCPSettings}
          />
          {repositoryPicker}
          <label className="panel-check" title="The flow in OPL goes into the prompt, so the agent knows where its work comes from and goes">
            <input
              type="checkbox"
              checked={Boolean(settings.describeFlow)}
              disabled={readOnly}
              onChange={(e) =>
                setSettings(({ describeFlow: _, ...rest }) => (e.target.checked ? { ...rest, describeFlow: true } : rest))
              }
            />
            <span>Tell it about the flow it works in, in OPL</span>
          </label>
        </>
      ) : kind === 'human' ? (
        // The prompt doubles as the person's instructions; switching kinds keeps it.
        <label key="human" className={`panel-field${review ? '' : ' panel-field-grow'}`}>
          <span>Instructions for the person</span>
          <textarea
            ref={bodyRef}
            readOnly={readOnly}
            value={prompt}
            rows={review ? 3 : undefined}
            placeholder="What should the person check before deciding, or fill in?"
            onChange={(e) => setPrompt(e.target.value)}
          />
        </label>
      ) : (
        <label key="script" className="panel-field panel-field-grow">
          <span>Script</span>
          <textarea
            ref={bodyRef}
            readOnly={readOnly}
            value={script}
            placeholder={SCRIPT_PLACEHOLDER}
            spellCheck={false}
            onChange={(e) => setScript(e.target.value)}
          />
          <small className="panel-note">
            Runs with bash, curl and jq. Each object it takes is a file in <code>$INPUTS</code>, named after the object
            with spaces as underscores. What it prints is its output; a list of items is a JSON array. A non-zero exit
            fails it. With repositories, it runs in their workspace, <code>$WORKSPACE</code>, on the branch{' '}
            <code>$BRANCH</code>. In a scheduled run, <code>$SINCE</code> (or <code>$LAST_RUN</code>) is when the
            schedule's last run that got through started, as a Unix timestamp, empty if none did. Each credential it
            picks is at <code>$CREDENTIAL_NAME_URL</code>, called with{' '}
            <code>Authorization: Bearer $CREDENTIALS_TOKEN</code>.
          </small>
        </label>
      )}
      {kind === 'script' && credentialPicker}
      {kind === 'script' && repositoryPicker}
      {kind === 'human' && (
        <div className="panel-row panel-durations">
          <DurationSetting
            label="Remind every"
            title="How often whoever may decide it is reminded while it waits, such as 4h or 1d, but never once it fails; never when empty"
            value={settings.remindAfter}
            readOnly={readOnly}
            onChange={(remindAfter) =>
              setSettings(({ remindAfter: _, ...rest }) => (remindAfter === undefined ? rest : { ...rest, remindAfter }))
            }
          />
          <DurationSetting
            label="Fail after"
            title="The longest it waits with nobody deciding before it fails, such as 2d; never when empty. Drag from this process onto another to run that one when it fails."
            value={settings.waitAtMost}
            readOnly={readOnly}
            onChange={(waitAtMost) =>
              setSettings(({ waitAtMost: _, ...rest }) => (waitAtMost === undefined ? rest : { ...rest, waitAtMost }))
            }
          />
          <DurationSetting
            label="Hand on after"
            title="How long it waits with nobody deciding before the roles below may decide it too, and are told so, such as 1d; never when empty"
            value={settings.escalateAfter}
            readOnly={readOnly}
            onChange={(escalateAfter) =>
              setSettings(({ escalateAfter: _, ...rest }) => (escalateAfter === undefined ? rest : { ...rest, escalateAfter }))
            }
          />
          <RolesSetting
            label="Hand on to"
            title="The roles that may decide it too once it has waited that long, such as Lead, Finance; those who decide it now still may"
            value={settings.escalateTo}
            readOnly={readOnly}
            onChange={(escalateTo) =>
              setSettings(({ escalateTo: _, ...rest }) => (escalateTo === undefined ? rest : { ...rest, escalateTo }))
            }
          />
        </div>
      )}
      {(kind === 'agent' || kind === 'script' || kind === 'connector') && (
        <div className="panel-row">
          {kind === 'agent' && (
            <NumberSetting
              label="Rounds"
              title="When this agent decides states of what it reviews: how many times it runs in a run before sending the work back fails it"
              value={settings.rounds}
              fallback={DEFAULT_ROUNDS}
              min={1}
              max={MAX_ROUNDS}
              readOnly={readOnly}
              onChange={(rounds) => setSettings(({ rounds: _, ...rest }) => (rounds === undefined ? rest : { ...rest, rounds }))}
            />
          )}
          <NumberSetting
            label="Loops"
            title="How many times it may run round a loop with no person deciding in between, such as a failure connection leading back to it, before someone is asked whether it goes on"
            value={settings.loops}
            fallback={DEFAULT_LOOPS}
            min={1}
            max={MAX_LOOPS}
            readOnly={readOnly}
            onChange={(loops) => setSettings(({ loops: _, ...rest }) => (loops === undefined ? rest : { ...rest, loops }))}
          />
          <NumberSetting
            label="Timeout (s)"
            title="How long a step may take before it fails; the worker's own limit when empty"
            value={settings.timeout}
            fallback="Worker's"
            min={1}
            max={MAX_TIMEOUT}
            readOnly={readOnly}
            onChange={(timeout) => setSettings(({ timeout: _, ...rest }) => (timeout === undefined ? rest : { ...rest, timeout }))}
          />
          <NumberSetting
            label="Retries"
            title="How many more times a failed step is tried before it fails for good; a stopped step isn't. Drag from this process onto another to run that one when it fails."
            value={settings.retries}
            fallback={0}
            min={0}
            max={MAX_RETRIES}
            readOnly={readOnly}
            onChange={(retries) => setSettings(({ retries: _, ...rest }) => (retries === undefined ? rest : { ...rest, retries }))}
          />
        </div>
      )}
      {kind !== 'human' && (
        <div className="panel-row">
          <NumberSetting
            label="Needs"
            title="How many of the objects it takes without waiting for a state must come, holding their conditions, before it runs; all of them when empty. The rest that come later are left."
            value={settings.quorum}
            fallback="All inputs"
            min={1}
            max={99}
            readOnly={readOnly}
            onChange={(quorum) => setSettings(({ quorum: _, ...rest }) => (quorum === undefined ? rest : { ...rest, quorum }))}
          />
        </div>
      )}
      {(kind === 'agent' || kind === 'script') && queries.length > 0 && (
        <div className="panel-queries">
          <span className="panel-label">Stored records</span>
          {queries.map((q) => (
            <QueryLinkFields key={q.link} q={q} readOnly={readOnly} onQuery={onQuery} />
          ))}
        </div>
      )}
      {onTry && !readOnly && (kind === 'agent' || kind === 'script' || kind === 'connector') && (
        <button
          className="panel-button"
          // What is typed goes first, so the trial runs it.
          onClick={() => {
            flush()
            onTry()
          }}
          title="Run only this process, on inputs you give it, without running the flow before or after it"
        >
          Try this process…
        </button>
      )}
      {review && <ReviewForm review={review} />}
      {run?.status === 'active' && <p className="panel-note">Running…</p>}
      {run?.error &&
        (run.status === 'duplicate' ? (
          <div className="panel-run">
            <span>Already processed; the run was terminated</span>
            <pre>{run.error}</pre>
          </div>
        ) : run.status === 'skipped' ? (
          <div className="panel-run">
            <span>Skipped</span>
            <pre>{run.error}</pre>
          </div>
        ) : (
          <div className="panel-run">
            <span>Last run failed</span>
            <pre className="failed">{run.error}</pre>
          </div>
        ))}
      {run?.output && (
        <div className="panel-run">
          <span>{kind === 'human' ? 'Last decision' : 'Last run output'}</span>
          <Data text={run.output} />
        </div>
      )}
    </aside>
  )
}

// A whole number a process's settings may hold, from min to max; empty
// leaves it out, which means fallback. What is typed shows as it is, and only
// a number in range is saved.
// An input link to a persistent or shared object, which can take the object's stored
// records instead of the object in the run; see queries.go.
export type QueryLink = {
  link: number
  object: string
  // The object's states, by name, to pick which records by.
  states: string[]
  query: LinkQuery | null
}

function QueryLinkFields({ q, readOnly, onQuery }: { q: QueryLink; readOnly: boolean; onQuery: (link: number, query: LinkQuery | null) => void }) {
  const query = q.query
  const [where, setWhere] = useState(query?.where ?? '')
  useEffect(() => setWhere(query?.where ?? ''), [query?.where])
  const set = (patch: LinkQuery) => onQuery(q.link, { ...query, ...patch })
  return (
    <div className="query-link">
      <label
        className="panel-check"
        title="Rather than wait for the object in this run, take what runs that ended done stored of it, as a list"
      >
        <input type="checkbox" checked={query !== null} disabled={readOnly} onChange={(e) => onQuery(q.link, e.target.checked ? {} : null)} />
        <span>Look up stored {q.object}</span>
      </label>
      {query && (
        <>
          <label className="panel-field" title="A rule its fields must hold, as a state's: comparisons joined by and. Empty takes them all.">
            <span>Where</span>
            <input
              type="text"
              value={where}
              placeholder='score >= 4 and kind == "bug"'
              readOnly={readOnly}
              onChange={(e) => setWhere(e.target.value)}
              onBlur={() => where.trim() !== (query.where ?? '') && set({ where: where.trim() || undefined })}
            />
          </label>
          <div className="panel-row">
            <select aria-label="In which state" value={query.state ?? ''} disabled={readOnly} onChange={(e) => set({ state: e.target.value || undefined })}>
              <option value="">In any state</option>
              {q.states.map((s) => (
                <option key={s} value={s}>
                  In {s}
                </option>
              ))}
            </select>
            <select
              aria-label="Which first"
              value={query.oldest ? 'oldest' : 'newest'}
              disabled={readOnly}
              onChange={(e) => set({ oldest: e.target.value === 'oldest' || undefined })}
            >
              <option value="newest">Newest first</option>
              <option value="oldest">Oldest first</option>
            </select>
            <NumberSetting
              label="At most"
              title="How many records it takes"
              value={query.limit}
              fallback={20}
              min={1}
              max={100}
              readOnly={readOnly}
              onChange={(limit) => set({ limit })}
            />
          </div>
        </>
      )}
    </div>
  )
}

function NumberSetting({
  label,
  title,
  value,
  fallback,
  min,
  max,
  readOnly,
  onChange,
}: {
  label: string
  title: string
  value: number | undefined
  fallback: number | string
  min: number
  max: number
  readOnly: boolean
  onChange: (n: number | undefined) => void
}) {
  const [text, setText] = useState(value === undefined ? '' : String(value))
  useEffect(() => {
    setText((t) => (Number(t) === value || (t === '' && value === undefined) ? t : value === undefined ? '' : String(value)))
  }, [value])
  return (
    <label className="panel-field" title={title}>
      <span>{label}</span>
      <input
        type="number"
        min={min}
        max={max}
        value={text}
        placeholder={String(fallback)}
        readOnly={readOnly}
        onChange={(e) => {
          setText(e.target.value)
          if (e.target.value === '') return onChange(undefined)
          const n = Number(e.target.value)
          if (Number.isInteger(n) && n >= min && n <= max) onChange(n)
        }}
      />
    </label>
  )
}

// A duration as written: days, hours, minutes and seconds, such as 4h or
// 1d 12h, or seconds alone; null if it isn't one.
function parseDuration(text: string): number | null {
  const t = text.trim().toLowerCase()
  if (/^\d+$/.test(t)) return Number(t)
  if (!/^(\d+\s*[dhms]\s*)+$/.test(t)) return null
  const size: Record<string, number> = { d: 86400, h: 3600, m: 60, s: 1 }
  let total = 0
  for (const [, n, unit] of t.matchAll(/(\d+)\s*([dhms])/g)) total += Number(n) * size[unit]
  return total
}

// A number of seconds as parseDuration takes it, such as 1d 12h.
function durationText(seconds: number): string {
  const parts: string[] = []
  for (const [unit, size] of [['d', 86400], ['h', 3600], ['m', 60], ['s', 1]] as const) {
    const n = Math.floor(seconds / size)
    if (n > 0) parts.push(`${n}${unit}`)
    seconds -= n * size
  }
  return parts.join(' ') || '0s'
}

// A setting holding a duration in seconds, written as parseDuration takes
// it, none when empty.
function DurationSetting({
  label,
  title,
  value,
  readOnly,
  onChange,
}: {
  label: string
  title: string
  value: number | undefined
  readOnly: boolean
  onChange: (seconds: number | undefined) => void
}) {
  const [text, setText] = useState(value === undefined ? '' : durationText(value))
  useEffect(() => {
    setText((t) =>
      parseDuration(t) === (value ?? null) || (t.trim() === '' && value === undefined)
        ? t
        : value === undefined
          ? ''
          : durationText(value),
    )
  }, [value])
  const seconds = parseDuration(text)
  const invalid = text.trim() !== '' && (seconds === null || seconds < 1 || seconds > MAX_WAIT)
  return (
    <label className="panel-field" title={title}>
      <span>{label}</span>
      <input
        type="text"
        value={text}
        placeholder="Never"
        readOnly={readOnly}
        aria-invalid={invalid || undefined}
        onChange={(e) => {
          setText(e.target.value)
          if (e.target.value.trim() === '') return onChange(undefined)
          const n = parseDuration(e.target.value)
          if (n !== null && n >= 1 && n <= MAX_WAIT) onChange(n)
        }}
      />
    </label>
  )
}

// Roles typed as a list separated by commas; none when empty.
function RolesSetting({
  label,
  title,
  value,
  readOnly,
  onChange,
}: {
  label: string
  title: string
  value: string[] | undefined
  readOnly: boolean
  onChange: (roles: string[] | undefined) => void
}) {
  const parse = (t: string) => {
    const roles = t
      .split(',')
      .map((r) => r.trim())
      .filter(Boolean)
    return roles.length > 0 ? roles : undefined
  }
  const [text, setText] = useState((value ?? []).join(', '))
  useEffect(() => {
    setText((t) => ((parse(t) ?? []).join(',') === (value ?? []).join(',') ? t : (value ?? []).join(', ')))
  }, [value])
  return (
    <label className="panel-field" title={title}>
      <span>{label}</span>
      <input
        type="text"
        value={text}
        placeholder="Nobody"
        readOnly={readOnly}
        onChange={(e) => {
          setText(e.target.value)
          onChange(parse(e.target.value))
        }}
      />
    </label>
  )
}

// What the person typed for one object: the text of an object without
// fields, or its fields, each the text typed for it, the fields of an object
// it is made of, or for a list one such value per item. It is sent as it is.
type Draft = EntryValue

// One item of a field: its fields, or the text typed for it.
const emptyOne = (fields: Field[]): Draft => (fields.length === 0 ? '' : Object.fromEntries(fields.map((f) => [f.name, emptyValue(f)])))
// A list starts with as many items as it holds at least, and one if it may
// have none.
const emptyValue = (f: Pick<Field, 'fields' | 'many' | 'min'>): Draft =>
  f.many ? Array.from({ length: Math.max(f.min, 1) }, () => emptyOne(f.fields)) : emptyOne(f.fields)
const emptyDraft = (o: EntryObject): Draft => (o.fields.length === 0 ? '' : emptyValue(o))

// Every field is needed: the objects are what the run goes on from. A list
// may have no items, unless it holds at least some; the form keeps it within
// how many it holds.
const filled = (d: Draft): boolean =>
  typeof d === 'string' ? d.trim() !== '' : Array.isArray(d) ? d.every(filled) : Object.values(d).every(filled)

export function ReviewForm({ review }: { review: Review }) {
  if (review.loop) return <LoopForm review={review} loop={review.loop} />
  return <DecisionForm review={review} />
}

// Asks whether a process going round a loop goes on, beside what it would take.
function LoopForm({ review, loop }: { review: Review; loop: NonNullable<Review['loop']> }) {
  const [sending, setSending] = useState(false)
  const answer = async (goOn: boolean) => {
    setSending(true)
    await loop.onAnswer(goOn)
    setSending(false)
  }
  return (
    <section className="review" aria-label="Loop">
      <span className="review-title">Going round a loop</span>
      <div className="review-body">
        {review.inputs.length > 0 && (
          <div className="review-inputs">
            {review.inputs.map((input, i) => (
              <div key={i} className="panel-run">
                <span>
                  {input.name}
                  {input.state && ` (${input.state})`}
                </span>
                <Data text={input.content} />
              </div>
            ))}
          </div>
        )}
        <div className="review-form">
          <p>{loop.question}</p>
          <small className="panel-note">
            Going on lets it run as many more times round the loop, and on what it took before. Stopping ends its
            branch and fails the run.
          </small>
          {!review.canDecide && <small className="panel-note">Only those who may edit this flow answer this.</small>}
          <div className="review-actions">
            <button disabled={!review.canDecide || sending} onClick={() => answer(true)}>
              Go on
            </button>
            <button disabled={!review.canDecide || sending} onClick={() => answer(false)}>
              Stop
            </button>
          </div>
        </div>
      </div>
    </section>
  )
}

function DecisionForm({ review }: { review: Review }) {
  const [instructions, setInstructions] = useState('')
  const [drafts, setDrafts] = useState<Record<number, Draft>>(() =>
    Object.fromEntries(review.entries.map((o) => [o.id, emptyDraft(o)])),
  )
  const [sending, setSending] = useState(false)
  // Name the object only when the decision is about more than one.
  const objects = new Set(review.options.map((o) => o.object))
  const entering = review.entries.length > 0
  const files = review.entries.flatMap((o) => o.fields.filter(holdsFile).map((f) => `${o.name}: ${f.name}`))
  const ready = !sending && files.length === 0 && review.entries.every((o) => drafts[o.id] !== undefined && filled(drafts[o.id]))
  const handlers = review.handledBy.join(' or ')

  const decide = async (state: number | null) => {
    if (!entering) {
      void review.onDecide(state, instructions.trim())
      return
    }
    const entries: Entries = Object.fromEntries(review.entries.map((o) => [String(o.id), drafts[o.id]]))
    setSending(true)
    // Kept as typed when the server turns it down, to fix and send again.
    await review.onDecide(state, instructions.trim(), entries)
    setSending(false)
  }

  return (
    <section className="review" aria-label={entering ? 'Input' : 'Review'}>
      <span className="review-title">
        {!review.canDecide ? `Waiting for ${handlers}` : entering ? 'Waiting for your input' : 'Waiting for your review'}
      </span>
      {/* What the process takes, to read before deciding, beside the form when there's room. */}
      <div className="review-body">
        {review.inputs.length > 0 && (
          <div className="review-inputs">
            {review.inputs.map((input, i) => (
              <div key={i} className="panel-run">
                <span>
                  {input.name}
                  {input.state && ` (${input.state})`}
                </span>
                <Data text={input.content} />
              </div>
            ))}
          </div>
        )}
        <div className="review-form">
          {review.inputs.length === 0 && !entering && (
            <p className="panel-note">This process has no generated inputs to review.</p>
          )}
          {review.entries.map((o) => (
            <EntryFields
              key={o.id}
              object={o}
              draft={drafts[o.id] ?? emptyDraft(o)}
              onChange={(d) => setDrafts((ds) => ({ ...ds, [o.id]: d }))}
            />
          ))}
          {files.length > 0 && (
            <small className="panel-note">
              Files can't be filled in by hand, so this can't be sent: {files.join(', ')}. Turn those attributes into text.
            </small>
          )}
          {(!entering || review.options.length > 0) && (
            <label className="panel-field">
              <span>Instructions</span>
              <textarea
                rows={3}
                value={instructions}
                placeholder="Optional. Passed with the object to the processes that take it in the state you set."
                onChange={(e) => setInstructions(e.target.value)}
              />
            </label>
          )}
          {review.options.length === 0 ? (
            !entering && (
              <small className="panel-note">
                Link this process to an object and pick, on the link, the states it decides between. Without any, the branch
                ends here.
              </small>
            )
          ) : (
            <span className="panel-note">Set to:</span>
          )}
          {!review.canDecide && (
            <small className="panel-note">Only {handlers} decides this. Ask an admin for the role to decide it yourself.</small>
          )}
          <div className="review-actions">
            {review.options.length === 0 && (
              <button disabled={!review.canDecide || (entering && !ready)} onClick={() => decide(null)}>
                {entering ? 'Submit' : 'Done'}
              </button>
            )}
            {review.options.map((o) => (
              <button key={o.state} disabled={!review.canDecide || (entering && !ready)} onClick={() => decide(o.state)}>
                {objects.size > 1 && `${o.object}: `}
                {o.name}
              </button>
            ))}
          </div>
        </div>
      </div>
    </section>
  )
}

// The fields of one object the person fills in: one per field, nested for
// the objects it is made of, for each item of a list, or a single text
// without fields.
function EntryFields({ object, draft, onChange }: { object: EntryObject; draft: Draft; onChange: (d: Draft) => void }) {
  if (typeof draft === 'string')
    return (
      <label className="panel-field">
        <span>{object.name}</span>
        <textarea rows={3} value={draft} onChange={(e) => onChange(e.target.value)} />
      </label>
    )
  return (
    <fieldset className="entry">
      <legend>{object.name}</legend>
      <EntryValueFields fields={object.fields} quantity={object} value={draft} onChange={onChange} />
    </fieldset>
  )
}

// A value with fields, or for a list its items, each with a header to remove
// it, as many as the list holds.
function EntryValueFields({
  fields,
  quantity,
  value,
  onChange,
}: {
  fields: Field[]
  quantity: Quantity
  value: Draft
  onChange: (d: Draft) => void
}) {
  if (!quantity.many) return <EntryItem fields={fields} value={value} onChange={onChange} />
  const items = Array.isArray(value) ? value : []
  const words = quantityWords(quantity)
  const fewest = items.length <= quantity.min
  const most = quantity.max !== null && items.length >= quantity.max
  return (
    <>
      {words && <small className="panel-note">Holds {words} items.</small>}
      {items.map((item, i) => (
        <div key={i} className="entry-item">
          <div className="entry-item-header">
            <span>Item {i + 1}</span>
            <button
              aria-label={`Remove item ${i + 1}`}
              title={fewest ? `It holds ${words} items` : 'Remove item'}
              disabled={fewest}
              onClick={() => onChange(items.filter((_, j) => j !== i))}
            >
              ×
            </button>
          </div>
          <EntryItem fields={fields} value={item} onChange={(d) => onChange(items.map((it, j) => (j === i ? d : it)))} />
        </div>
      ))}
      <button
        className="entry-add"
        disabled={most}
        title={most ? `It holds ${words} items` : undefined}
        onClick={() => onChange([...items, emptyOne(fields)])}
      >
        Add item
      </button>
    </>
  )
}

// One item: a field per field, an object it is made of in a box of its own.
function EntryItem({ fields, value, onChange }: { fields: Field[]; value: Draft; onChange: (d: Draft) => void }) {
  if (fields.length === 0) {
    return <input value={typeof value === 'string' ? value : ''} onChange={(e) => onChange(e.target.value)} />
  }
  const item = typeof value === 'object' && !Array.isArray(value) ? value : {}
  const set = (name: string, v: Draft) => onChange({ ...item, [name]: v })
  return (
    <>
      {fields.map((f) =>
        f.fields.length > 0 || f.many ? (
          <fieldset key={f.name} className="entry entry-nested">
            <legend>{f.name}</legend>
            <EntryValueFields fields={f.fields} quantity={f} value={item[f.name] ?? emptyValue(f)} onChange={(v) => set(f.name, v)} />
          </fieldset>
        ) : (
          <label key={f.name} className="panel-field">
            <span>{f.name}</span>
            <input
              value={typeof item[f.name] === 'string' ? (item[f.name] as string) : ''}
              disabled={f.file}
              placeholder={f.file ? "A file; can't be filled in by hand" : f.definition || undefined}
              title={f.definition || undefined}
              onChange={(e) => set(f.name, e.target.value)}
            />
          </label>
        ),
      )}
    </>
  )
}

// The flow a subflow process runs, and how its objects meet that flow's: by
// name, on that flow's top board. Names with no namesake there are marked.
function SubflowFields({
  flows,
  flow,
  trashed,
  takes,
  generates,
  shapes,
  readOnly,
  onChange,
  onOpen,
  onError,
}: {
  flows: Flow[] | null
  flow: number | undefined
  // The name of the procedure it runs, if that is in the trash, whose board
  // can't be read.
  trashed: string | undefined
  takes: string[]
  generates: string[]
  shapes: Record<string, string>
  readOnly: boolean
  onChange: (flow: number | undefined) => void
  onOpen: () => void
  onError: (err: unknown) => void
}) {
  // The shapes of what the objects on that flow's top board hold, by name,
  // once loaded.
  const [names, setNames] = useState<Map<string, string> | null>(null)
  useEffect(() => {
    setNames(null)
    if (flow === undefined || trashed !== undefined) return
    let stale = false
    listObjects(flow)
      .then(
        (os) =>
          !stale &&
          setNames(new Map(os.filter((o) => o.parent === null && o.owner === null).map((o) => [o.name, shapeOf(fieldsOf(os, o.id))]))),
      )
      .catch((err) => !stale && onError(err))
    return () => {
      stale = true
    }
  }, [flow, trashed])

  const gone = flow !== undefined && flows !== null && !flows.some((f) => f.id === flow)
  const matches = (label: string, list: string[]) =>
    list.length > 0 && (
      <div className="panel-field">
        <span>{label}</span>
        <ul className="subflow-names">
          {list.map((n) => {
            const unmatched = names !== null && !names.has(n)
            // Handed over as it is, so what doesn't fit fails there.
            const unlike = names !== null && !unmatched && shapes[n] !== undefined && names.get(n) !== shapes[n]
            return (
              <li key={n} className={unmatched || unlike ? 'unmatched' : undefined}>
                {n}
                {unmatched && <small> · no object of that name on its top board</small>}
                {unlike && <small> · its namesake there holds different fields</small>}
              </li>
            )
          })}
        </ul>
      </div>
    )

  return (
    <>
      <label className="panel-field">
        <span>Procedure it runs</span>
        <select
          value={flow ?? ''}
          disabled={readOnly}
          onChange={(e) => onChange(e.target.value === '' ? undefined : Number(e.target.value))}
        >
          <option value="">Pick a procedure…</option>
          {flow !== undefined && (flows === null || gone) && (
            <option value={flow}>
              {trashed !== undefined ? `${trashed} (in the trash)` : gone ? `Flow #${flow} (deleted)` : `Flow #${flow}`}
            </option>
          )}
          {flows
            ?.filter((f) => f.module || f.id === flow)
            .map((f) => (
            <option key={f.id} value={f.id}>
              {f.name}
            </option>
          ))}
        </select>
      </label>
      {trashed !== undefined && (
        <p className="panel-warning">
          {trashed} is in the trash, so this fails to run it until someone restores it from the trash; or pick another
          procedure.
        </p>
      )}
      <p className="panel-note">
        Each time it runs is a run of that flow. The objects it takes are given to the objects of the same names on
        that flow's top board, and it generates what the run leaves of the objects named like the ones it generates.
      </p>
      {matches('Takes', takes)}
      {matches('Generates', generates)}
      {flow !== undefined && !gone && (
        <button className="panel-button" onClick={onOpen}>
          Open the flow
        </button>
      )}
    </>
  )
}
