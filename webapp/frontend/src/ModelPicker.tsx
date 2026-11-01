import { Fragment, useEffect, useRef, useState, type KeyboardEvent } from 'react'
import type { Models } from './api'
import { otherModel, shortModel } from './models'
import { fuzzy } from './shortcuts'

// An agent process's model, picked from a list opened under the field and
// searched as one types: the default first, named by its short name, then
// the models the flow's processes use, then each provider's chat models.
// Those for something else, such as speech or images (see models.ts), are a
// click away, and a search finds them too. The model picked stays listed
// even once it is no longer offered, so it isn't silently replaced.

type Row = { value: string; name: string; detail: string; others?: boolean }
type Group = { label: string; rows: Row[] }

// A model by its short name and where it comes from, as the field and a
// panel that can't be changed say it.
export function modelText(models: Models | null, model: string): string {
  if (model === '') return models?.default ? `the default, ${shortModel(models.default)}` : 'the default'
  return `${shortModel(model)} (${providerName(models, model)})`
}

const providerName = (models: Models | null, model: string) => {
  const p = model.slice(0, model.indexOf('/'))
  return models?.providers?.[p] ?? p
}

export function ModelPicker({
  models,
  value,
  used,
  labelledBy,
  onChange,
}: {
  // The models offered, or null while they load.
  models: Models | null
  value: string
  // The models the flow's other processes use.
  used: string[]
  labelledBy: string
  onChange: (model: string) => void
}) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [showOthers, setShowOthers] = useState(false)
  const [active, setActive] = useState(0)
  const root = useRef<HTMLDivElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  const list = useRef<HTMLUListElement>(null)

  const offered = models?.models ?? []
  const row = (m: string): Row => ({
    value: m,
    name: shortModel(m),
    detail: offered.includes(m) ? providerName(models, m) : `${providerName(models, m)} · no longer offered`,
  })
  const defaultRow: Row = {
    value: '',
    name: 'Default',
    detail: models?.default ? `${shortModel(models.default)} · ${providerName(models, models.default)}` : 'pi picks one',
  }
  const inFlow = [...new Set([value, ...used].filter((m) => m !== ''))].sort()
  const firsts = new Set(inFlow)
  const others = offered.filter((m) => !firsts.has(m) && otherModel(m))

  let groups: Group[]
  if (query.trim()) {
    const found = [defaultRow, ...[...new Set([...inFlow, ...offered])].map(row)]
      .map((r) => ({ r, m: fuzzy(query, r.value === '' ? `default ${r.detail}` : `${r.value} ${r.detail}`) }))
      .filter((x) => x.m !== null)
      .sort((a, b) => b.m!.score - a.m!.score)
      .map((x) => x.r)
    groups = [{ label: '', rows: found }]
  } else {
    const byProvider = new Map<string, Row[]>()
    // Fine-tunes, of one task each, after the models they were made of.
    const fineTune = (m: string) => shortModel(m).startsWith('ft:')
    for (const m of [...offered.filter((m) => !fineTune(m)), ...offered.filter(fineTune)]) {
      if (firsts.has(m) || otherModel(m)) continue
      const p = providerName(models, m)
      byProvider.set(p, [...(byProvider.get(p) ?? []), row(m)])
    }
    groups = [
      { label: '', rows: [defaultRow] },
      { label: 'In this flow', rows: inFlow.map(row) },
      ...[...byProvider].map(([label, rows]) => ({ label, rows })),
      showOthers
        ? { label: 'For speech, images, embeddings and more', rows: others.map(row) }
        : { label: '', rows: others.length > 0 ? [{ value: '', name: `Show ${others.length} more`, detail: 'speech, images, embeddings and more', others: true }] : [] },
    ].filter((g) => g.rows.length > 0)
  }
  const rows = groups.flatMap((g) => g.rows)

  const close = (refocus: boolean) => {
    setOpen(false)
    setQuery('')
    if (refocus) button.current?.focus()
  }
  const openList = () => {
    setOpen(true)
    setActive(0)
  }
  const pick = (r: Row) => {
    if (r.others) {
      setShowOthers(true)
      return
    }
    onChange(r.value)
    close(true)
  }

  // The one picked is where the list starts; a search starts at its best.
  useEffect(() => {
    if (open) setActive(query.trim() ? 0 : Math.max(0, rows.findIndex((r) => !r.others && r.value === value)))
  }, [open, query])

  useEffect(() => {
    list.current?.querySelector('[aria-selected="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [active, open])

  // A click anywhere else closes it.
  useEffect(() => {
    if (!open) return
    const away = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) close(false)
    }
    document.addEventListener('mousedown', away)
    return () => document.removeEventListener('mousedown', away)
  }, [open])

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    switch (e.key) {
      case 'ArrowDown':
        setActive((i) => Math.min(i + 1, rows.length - 1))
        break
      case 'ArrowUp':
        setActive((i) => Math.max(i - 1, 0))
        break
      case 'Enter':
        if (rows[active]) pick(rows[active])
        break
      case 'Escape':
        // Closes the list, not the panel.
        e.stopPropagation()
        close(true)
        break
      case 'Tab':
        close(false)
        return
      default:
        return
    }
    e.preventDefault()
  }

  const shown = value === '' ? defaultRow : row(value)
  let index = 0
  return (
    <div className="model-picker" ref={root}>
      <button
        ref={button}
        type="button"
        className="model-picker-button"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-labelledby={labelledBy}
        aria-describedby={`${labelledBy}-value`}
        onClick={() => (open ? close(false) : openList())}
      >
        <span id={`${labelledBy}-value`} className="model-picker-name">
          {shown.name}
        </span>
        <span className="model-picker-detail">{shown.detail}</span>
      </button>
      {open && (
        <div className="model-picker-list">
          <input
            type="search"
            autoFocus
            value={query}
            placeholder={`Search ${offered.length} models`}
            aria-label="Search models"
            aria-controls={`${labelledBy}-options`}
            aria-activedescendant={rows[active] ? `${labelledBy}-option-${active}` : undefined}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onKeyDown}
          />
          <ul id={`${labelledBy}-options`} role="listbox" aria-labelledby={labelledBy} ref={list}>
            {groups.map((g, gi) => (
              <Fragment key={gi}>
                {g.label && (
                  <li role="presentation" className="model-picker-group">
                    {g.label}
                  </li>
                )}
                {g.rows.map((r) => {
                  const i = index++
                  return (
                    <li
                      key={`${r.others ? 'others' : r.value}-${i}`}
                      id={`${labelledBy}-option-${i}`}
                      role="option"
                      aria-selected={i === active}
                      className={`${r.others ? 'model-picker-more' : ''}${!r.others && r.value === value ? ' picked' : ''}`}
                      // Keeps the search focused.
                      onMouseDown={(e) => {
                        e.preventDefault()
                        pick(r)
                      }}
                      onMouseEnter={() => setActive(i)}
                    >
                      <span>{r.name}</span>
                      <small>{r.detail}</small>
                    </li>
                  )
                })}
              </Fragment>
            ))}
            {rows.length === 0 && <li className="model-picker-none">No model matches.</li>}
          </ul>
        </div>
      )}
    </div>
  )
}
