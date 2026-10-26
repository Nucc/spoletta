import { useCallback, useEffect, useLayoutEffect, useRef, useState, type FormEvent, type KeyboardEvent, type Ref } from 'react'
import {
  createSkill,
  deleteSkill,
  listSkills,
  listTeams,
  setSkillOwner,
  updateSkill,
  type FlowOwner,
  type Me,
  type Skill,
  type SkillFields,
  type SkillKind,
  type Team,
} from './api'
import { useMe } from './Account'
import { PlusIcon } from './icons'
import { OwnerPicker } from './Owner'
import { useConfirm } from './Confirm'

// Skills and commands, written for agents in the settings: a skill an agent
// process picks, a command its prompt names as /name. Each is its maker's
// until they share it with a team or everyone, as a flow is.
export function SkillsSection({ onError }: { onError: (err: unknown) => void }) {
  const me = useMe()
  const [teams, setTeams] = useState<Team[]>([])
  useEffect(() => {
    if (me?.user) listTeams().then(setTeams, onError)
  }, [me, onError])
  return (
    <>
      <SkillList kind="skill" me={me} teams={teams} onError={onError} />
      <SkillList kind="command" me={me} teams={teams} onError={onError} />
    </>
  )
}

const WORDS = {
  skill: {
    title: 'Skills',
    one: 'skill',
    note: 'Instructions for a kind of work, which an agent reads once its task calls for them. An agent process picks the skills it is given; the agent sees each one’s description and loads the rest when it applies.',
    content: 'Instructions',
    contentPlaceholder: '# Release notes\n\nGroup the changes under Added, Changed and Fixed…',
    description: 'What it does and when it applies, which the agent picks it by',
  },
  command: {
    title: 'Commands',
    one: 'command',
    note: 'Prompts kept under a name. Type / in an agent’s prompt to name one; it is spelled out as the step runs. One with $1, $@ or ${1:-default} takes the rest of its line as arguments, quoted ones keeping their spaces.',
    content: 'Prompt',
    contentPlaceholder: 'Review the change. Focus on ${1:-correctness}.',
    description: 'What it is for, shown as you pick it',
  },
}

function SkillList({ kind, me, teams, onError }: { kind: SkillKind; me: Me | null; teams: Team[]; onError: (err: unknown) => void }) {
  const [items, setItems] = useState<Skill[] | null>(null)
  // What is being written: a new one, or the one of this id.
  const [editing, setEditing] = useState<number | 'new' | null>(null)
  const [confirm, confirmDialog] = useConfirm()
  const words = WORDS[kind]

  const refresh = useCallback(() => listSkills(kind).then(setItems, onError), [kind, onError])
  useEffect(() => {
    refresh()
  }, [refresh])

  // Saves what was written, giving it to the owner picked if that changed.
  const save = async (fields: SkillFields, owner: FlowOwner) => {
    try {
      const was = items?.find((s) => s.id === editing)?.owner
      const ownerOf = (o: FlowOwner) => (o.kind === 'global' ? { kind: o.kind } : { kind: o.kind, id: o.id })
      if (editing === 'new') await createSkill(kind, me?.user ? { ...fields, owner: ownerOf(owner) } : fields)
      else if (editing !== null) {
        await updateSkill(kind, editing, fields)
        if (me?.user && was && JSON.stringify(ownerOf(was)) !== JSON.stringify(ownerOf(owner))) {
          await setSkillOwner(kind, editing, ownerOf(owner))
        }
      }
      setEditing(null)
      await refresh()
    } catch (err) {
      onError(err)
    }
  }

  const remove = async (s: Skill) => {
    const uses = kind === 'skill' ? 'Agents picking it fail to run until it is unpicked.' : 'Prompts naming it pass /' + s.name + ' on as written.'
    const yes = await confirm({
      title: `Delete the ${words.one} ${s.name}?`,
      message: <p>{uses} This can't be undone.</p>,
      confirm: 'Delete',
    })
    if (!yes) return
    try {
      await deleteSkill(kind, s.id)
      await refresh()
    } catch (err) {
      onError(err)
    }
  }

  const give = async (s: Skill, owner: Parameters<typeof setSkillOwner>[2]) => {
    try {
      await setSkillOwner(kind, s.id, owner)
      await refresh()
    } catch (err) {
      onError(err)
    }
  }

  return (
    <section className="flow-section skills" aria-label={words.title}>
      <div className="skills-header">
        <h2>{words.title}</h2>
        {editing !== 'new' && (
          <button type="button" className="skills-new" onClick={() => setEditing('new')}>
            <PlusIcon /> New {words.one}
          </button>
        )}
      </div>
      <p className="flows-note">{words.note}</p>
      {editing === 'new' && <SkillEditor kind={kind} me={me} teams={teams} onSave={save} onCancel={() => setEditing(null)} />}
      {items === null ? (
        <p className="flows-note">Loading…</p>
      ) : items.length === 0 ? (
        editing !== 'new' && <p className="flows-note">None yet.</p>
      ) : (
        <ul className="flow-list skill-list">
          {items.map((s) =>
            editing === s.id ? (
              <li key={s.id}>
                <SkillEditor kind={kind} initial={s} me={me} teams={teams} onSave={save} onCancel={() => setEditing(null)} />
              </li>
            ) : (
              <li key={s.id}>
                <div className="skill-item">
                  <span className="skill-what">
                    <code className="skill-name">
                      {kind === 'command' ? '/' : ''}
                      {s.name}
                      {s.argumentHint && <span className="skill-hint"> {s.argumentHint}</span>}
                    </code>
                    <span className="flows-note">{s.description || firstLine(s.content)}</span>
                  </span>
                  <OwnerPicker flow={{ name: s.name, owner: s.owner, access: 'edit' }} me={me} teams={teams} compact onChange={(o) => give(s, o)} />
                  <button type="button" onClick={() => setEditing(s.id)}>
                    Edit
                  </button>
                  <button type="button" className="danger" onClick={() => remove(s)}>
                    Delete
                  </button>
                </div>
              </li>
            ),
          )}
        </ul>
      )}
      {confirmDialog}
    </section>
  )
}

const firstLine = (s: string) => s.split('\n').find((l) => l.trim()) ?? ''

// Writes a new skill or command, or changes one.
function SkillEditor({
  kind,
  initial,
  me,
  teams,
  onSave,
  onCancel,
}: {
  kind: SkillKind
  initial?: Skill
  me: Me | null
  teams: Team[]
  onSave: (fields: SkillFields, owner: FlowOwner) => Promise<void>
  onCancel: () => void
}) {
  // A new one is its maker's until they pick a team or everyone.
  const [owner, setOwner] = useState<FlowOwner>(
    initial?.owner ?? (me?.user ? { kind: 'user', id: me.user.id, name: me.user.name || me.user.email } : { kind: 'global' }),
  )
  const [name, setName] = useState(initial?.name ?? '')
  const [description, setDescription] = useState(initial?.description ?? '')
  const [argumentHint, setArgumentHint] = useState(initial?.argumentHint ?? '')
  const [content, setContent] = useState(initial?.content ?? '')
  const [saving, setSaving] = useState(false)
  const words = WORDS[kind]
  const ready = name.trim() !== '' && content.trim() !== '' && (kind === 'command' || description.trim() !== '')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!ready) return
    setSaving(true)
    await onSave({ name, description, argumentHint, content }, owner)
    setSaving(false)
  }

  return (
    <form className="skill-editor" onSubmit={submit} onKeyDown={(e) => e.key === 'Escape' && onCancel()}>
      <div className="panel-row">
        <label className="panel-field">
          <span>Name</span>
          <input
            autoFocus
            value={name}
            placeholder={kind === 'skill' ? 'release-notes' : 'review'}
            spellCheck={false}
            onChange={(e) => setName(e.target.value.toLowerCase().replace(/[\s_]+/g, '-'))}
          />
          <small className="panel-note">Lowercase letters, digits and hyphens.</small>
        </label>
        {kind === 'command' && (
          <label className="panel-field">
            <span>Arguments</span>
            <input value={argumentHint} placeholder="[focus]" onChange={(e) => setArgumentHint(e.target.value)} />
            <small className="panel-note">Shown as you pick it: &lt;required&gt; [optional].</small>
          </label>
        )}
      </div>
      <label className="panel-field">
        <span>Description{kind === 'command' && ' (optional)'}</span>
        <input value={description} placeholder={words.description} maxLength={1024} onChange={(e) => setDescription(e.target.value)} />
      </label>
      <label className="panel-field">
        <span>{words.content}</span>
        <textarea rows={10} value={content} placeholder={words.contentPlaceholder} spellCheck={false} onChange={(e) => setContent(e.target.value)} />
      </label>
      <label className="panel-field skill-owner">
        <span>Who sees it</span>
        {me?.user ? (
          <>
            <OwnerPicker
              flow={{ name: name || `this ${words.one}`, owner, access: 'edit' }}
              me={me}
              teams={teams}
              compact
              onChange={(o) => setOwner(o.kind === 'global' ? o : { ...o, name: '' })}
            />
            <small className="panel-note">
              {owner.kind === 'user'
                ? 'Only you see it and use it in your flows.'
                : owner.kind === 'team'
                  ? 'The team’s members see it, change it and use it in the team’s flows and their own.'
                  : 'Everyone sees it, changes it and uses it in any flow; only an admin takes it back.'}
            </small>
          </>
        ) : (
          <small className="panel-note">Everyone: nobody signs in to this app, so everything is everyone’s.</small>
        )}
      </label>
      <div className="skill-actions">
        <button type="submit" className="skills-new" disabled={!ready || saving}>
          {initial ? 'Save' : `Add ${words.one}`}
        </button>
        <button type="button" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  )
}

// The skills an agent process is given, picked from those its flow may use.
export function SkillPicker({
  skills,
  selected,
  readOnly,
  onChange,
}: {
  // The skills the flow may use, or null while they load.
  skills: Skill[] | null
  selected: number[]
  readOnly: boolean
  onChange: (ids: number[]) => void
}) {
  // Picked ones since deleted, or no longer the flow's to use, stay listed so they can be dropped.
  const missing = skills ? selected.filter((id) => !skills.some((s) => s.id === id)) : []
  return (
    <fieldset className="panel-field mcp-picker">
      <legend>Skills</legend>
      {skills?.length === 0 && (
        <p className="panel-note">
          None this flow may use. <a href="#/settings/skills">Write skills</a>
        </p>
      )}
      {skills?.map((s) => (
        <div key={s.id} className="mcp-pick" title={s.description}>
          <label className="panel-check">
            <input
              type="checkbox"
              checked={selected.includes(s.id)}
              disabled={readOnly}
              onChange={(e) => onChange(e.target.checked ? [...selected, s.id] : selected.filter((id) => id !== s.id))}
            />
            <span>{s.name}</span>
          </label>
        </div>
      ))}
      {missing.length > 0 && (
        <div className="mcp-pick">
          <small className="panel-warning">
            {missing.length === 1
              ? 'A skill it picked was deleted or isn’t this flow’s to use.'
              : `${missing.length} skills it picked were deleted or aren’t this flow’s to use.`}
          </small>
          {!readOnly && (
            <button className="link-button" onClick={() => onChange(selected.filter((id) => !missing.includes(id)))}>
              Drop
            </button>
          )}
        </div>
      )}
    </fieldset>
  )
}

// The /name being typed where the caret is, at the start of a line or after
// a space, and where its slash is.
function typedCommand(text: string, caret: number): { start: number; prefix: string } | null {
  const m = /(^|[ \t\n])\/([a-z0-9-]*)$/.exec(text.slice(0, caret))
  if (!m) return null
  // A name running on past the caret is being edited, not typed.
  if (/^[a-z0-9-]/.test(text.slice(caret))) return null
  return { start: m.index + m[1].length, prefix: m[2] }
}

// Where the caret is in a textarea, in pixels from its top left, measured
// on a copy of it laid out the same.
function caretAt(el: HTMLTextAreaElement, pos: number): { top: number; left: number; height: number } {
  const style = window.getComputedStyle(el)
  const copy = document.createElement('div')
  for (const p of [
    'boxSizing',
    'width',
    'paddingTop',
    'paddingRight',
    'paddingBottom',
    'paddingLeft',
    'borderTopWidth',
    'borderRightWidth',
    'borderBottomWidth',
    'borderLeftWidth',
    'fontFamily',
    'fontSize',
    'fontWeight',
    'lineHeight',
    'letterSpacing',
    'tabSize',
  ] as const) {
    copy.style[p] = style[p]
  }
  copy.style.position = 'absolute'
  copy.style.visibility = 'hidden'
  copy.style.whiteSpace = 'pre-wrap'
  copy.style.overflowWrap = 'break-word'
  copy.style.borderStyle = 'solid'
  copy.textContent = el.value.slice(0, pos)
  const mark = document.createElement('span')
  mark.textContent = '​'
  copy.appendChild(mark)
  document.body.appendChild(copy)
  const at = { top: mark.offsetTop - el.scrollTop, left: mark.offsetLeft - el.scrollLeft, height: mark.offsetHeight }
  document.body.removeChild(copy)
  return at
}

// An agent's prompt, offering the commands its flow may use as / is typed.
export function PromptEditor({
  value,
  commands,
  readOnly,
  placeholder,
  textareaRef,
  onChange,
}: {
  value: string
  // The commands the flow may use, or null while they load.
  commands: Skill[] | null
  readOnly: boolean
  placeholder: string
  textareaRef: Ref<HTMLTextAreaElement>
  onChange: (value: string) => void
}) {
  const own = useRef<HTMLTextAreaElement | null>(null)
  const [typed, setTyped] = useState<{ start: number; prefix: string } | null>(null)
  const [active, setActive] = useState(0)
  const [at, setAt] = useState<{ top: number; left: number } | null>(null)
  // Escape closes the menu until the next /.
  const [dismissed, setDismissed] = useState<number | null>(null)

  const matches =
    typed && commands && typed.start !== dismissed ? commands.filter((c) => c.name.startsWith(typed.prefix)).slice(0, 8) : []
  const open = !readOnly && matches.length > 0

  const look = () => {
    const el = own.current
    if (!el || el.selectionStart !== el.selectionEnd) return setTyped(null)
    const t = typedCommand(el.value, el.selectionStart)
    setTyped((prev) => (prev?.start === t?.start && prev?.prefix === t?.prefix ? prev : t))
  }

  useEffect(() => setActive(0), [typed?.start, typed?.prefix])

  useLayoutEffect(() => {
    const el = own.current
    if (!open || !el || !typed) return setAt(null)
    const c = caretAt(el, typed.start)
    setAt({ top: el.offsetTop + c.top + c.height + 2, left: Math.min(el.offsetLeft + c.left, el.offsetLeft + el.clientWidth - 240) })
  }, [open, typed, value])

  const pick = (c: Skill) => {
    const el = own.current
    if (!el || !typed) return
    const end = typed.start + 1 + typed.prefix.length
    const inserted = `/${c.name} `
    onChange(value.slice(0, typed.start) + inserted + value.slice(end).replace(/^ /, ''))
    setTyped(null)
    const caret = typed.start + inserted.length
    requestAnimationFrame(() => {
      el.focus()
      el.setSelectionRange(caret, caret)
    })
  }

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (!open) return
    switch (e.key) {
      case 'ArrowDown':
        setActive((i) => (i + 1) % matches.length)
        break
      case 'ArrowUp':
        setActive((i) => (i - 1 + matches.length) % matches.length)
        break
      case 'Enter':
      case 'Tab':
        pick(matches[Math.min(active, matches.length - 1)])
        break
      case 'Escape':
        // Closes the menu, not the panel.
        e.stopPropagation()
        setDismissed(typed!.start)
        break
      default:
        return
    }
    e.preventDefault()
  }

  return (
    <div className="prompt-editor">
      <textarea
        ref={(el) => {
          own.current = el
          if (typeof textareaRef === 'function') textareaRef(el)
          else if (textareaRef) textareaRef.current = el
        }}
        readOnly={readOnly}
        value={value}
        placeholder={placeholder}
        role="combobox"
        aria-expanded={open}
        aria-autocomplete="list"
        aria-controls="prompt-commands"
        onChange={(e) => {
          onChange(e.target.value)
          requestAnimationFrame(look)
        }}
        onKeyDown={onKeyDown}
        onKeyUp={(e) => (e.key === 'ArrowLeft' || e.key === 'ArrowRight' || e.key === 'Home' || e.key === 'End') && look()}
        onClick={look}
        onBlur={() => setTyped(null)}
      />
      {open && at && (
        <ul id="prompt-commands" className="command-menu" role="listbox" style={{ top: at.top, left: Math.max(at.left, 0) }}>
          {matches.map((c, i) => (
            <li
              key={c.id}
              role="option"
              aria-selected={i === active}
              className={i === active ? 'active' : undefined}
              // Keeps the prompt focused.
              onMouseDown={(e) => {
                e.preventDefault()
                pick(c)
              }}
              onMouseEnter={() => setActive(i)}
            >
              <code>
                /{c.name}
                {c.argumentHint && <span className="skill-hint"> {c.argumentHint}</span>}
              </code>
              <span>{c.description || firstLine(c.content)}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
