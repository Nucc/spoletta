// The flow settings' branch named after what a run works on, such as
// feature/{@Ticket.key}: typing @ suggests the values runs start with, from
// the flow's top board as it is now (branchrule.ts), and picking one writes
// it out in braces. See branch_names.go.

import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { listLinks, listObjects } from './api'
import { branchValues, matchValues, typedValue, type BranchValue } from './branchrule'

export function BranchRuleField({
  flowId,
  rule,
  readOnly,
  onSave,
  onError,
}: {
  flowId: number
  rule: string
  readOnly: boolean
  onSave: (rule: string) => void
  onError: (err: unknown) => void
}) {
  const id = useId()
  const input = useRef<HTMLInputElement>(null)
  const [value, setValue] = useState(rule)
  const [caret, setCaret] = useState(rule.length)
  const [open, setOpen] = useState(false)
  const [active, setActive] = useState(-1)
  const [values, setValues] = useState<BranchValue[] | null>(null)
  const pending = useRef<number | null>(null)

  // What the board offers now, read again each time the field is entered,
  // as the board may have changed since.
  const load = () => {
    Promise.all([listObjects(flowId), listLinks(flowId)])
      .then(([objects, links]) => setValues(branchValues(objects, links)))
      .catch(onError)
  }
  useEffect(load, [flowId])

  useLayoutEffect(() => {
    if (pending.current !== null && input.current) {
      input.current.setSelectionRange(pending.current, pending.current)
      pending.current = null
    }
  })

  const typing = useMemo(() => (open ? typedValue(value, caret) : null), [open, value, caret])
  const items = useMemo(() => (typing && values ? matchValues(values, typing.typed) : []), [typing, values])

  const save = () => {
    if (value.trim() !== rule) onSave(value.trim())
  }

  const accept = (v: BranchValue) => {
    if (!typing) return
    const next = value.slice(0, typing.from) + v.insert + value.slice(typing.to)
    const at = typing.from + v.insert.length
    pending.current = at
    setValue(next)
    setCaret(at)
    setActive(-1)
  }

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    const showing = typing !== null && items.length > 0
    if (e.key === 'Escape' && typing) {
      // Closes the list, not the panel.
      e.stopPropagation()
      setOpen(false)
      return
    }
    if (showing && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
      e.preventDefault()
      const step = e.key === 'ArrowDown' ? 1 : -1
      setActive((a) => ((a + 1 + step + items.length + 1) % (items.length + 1)) - 1)
      return
    }
    if (showing && (e.key === 'Enter' || e.key === 'Tab')) {
      const pick = active >= 0 && active < items.length ? items[active] : items[0]
      e.preventDefault()
      accept(pick)
      return
    }
    if (e.key === 'Enter') save()
  }

  return (
    <div className="panel-field">
      <label htmlFor={id}>Branch named after what a run works on</label>
      <div className="branch-rule">
        <input
          id={id}
          ref={input}
          value={value}
          readOnly={readOnly}
          placeholder="Optional. Type @ for a value, such as feature/{@Ticket.key}"
          spellCheck={false}
          autoComplete="off"
          role="combobox"
          aria-expanded={typing !== null}
          aria-autocomplete="list"
          onChange={(e) => {
            setValue(e.target.value)
            setCaret(e.target.selectionStart ?? e.target.value.length)
            setActive(-1)
            setOpen(true)
          }}
          onSelect={() => setCaret(input.current?.selectionStart ?? value.length)}
          onFocus={() => {
            setOpen(true)
            load()
          }}
          onBlur={() => {
            setOpen(false)
            save()
          }}
          onKeyDown={onKeyDown}
        />
        {!readOnly && typing && values && (
          <div className="query-suggestions" role="listbox" onMouseDown={(e) => e.preventDefault()}>
            {items.map((v, i) => (
              <button
                key={v.insert}
                type="button"
                role="option"
                aria-selected={i === active}
                className={i === active ? 'active' : undefined}
                onMouseEnter={() => setActive(i)}
                onClick={() => accept(v)}
              >
                <span className="query-suggestion-label">{v.insert}</span>
                <span className="query-suggestion-hint">{v.hint}</span>
              </button>
            ))}
            <div className="query-help">
              {values.length === 0
                ? "No object on the top board is given to runs or picks a record, so there's nothing to name the branch after yet."
                : items.length === 0
                  ? 'No value runs start with is named so.'
                  : 'Values each run starts with · Tab or Enter to pick one'}
            </div>
          </div>
        )}
      </div>
      <small className="panel-note">
        Each <code>{'{@Object.attribute}'}</code> is a value the run starts with: an object on the top board it is given,
        or a record it picks, follows or was started on. Every run on ticket PROJ-123 then works on{' '}
        <code>feature/PROJ-123</code>, starting from origin's latest commit of it if origin has it, else from the base
        branch, and a push updates it, so each flow working on that ticket works on the same branch. Spaces in a value
        become <code>_</code> and what a branch name can't hold is left out. A run lacking the value works on a branch of
        its own, as its log says.
      </small>
    </div>
  )
}
