// What a flow's branch rule (its settings' branch, branch_names.go) can name
// after: the values a run holds as it starts, suggested as `@` is typed in
// the flow settings (BranchRuleField.tsx). Checked by scripts/branchrule.ts.

import type { FlowObject, Link } from './api'

// A value to name a branch after, as the rule writes it, with what it is
// and why a run holds it.
export type BranchValue = { insert: string; label: string; hint: string }

// A name as the search's facets write it, spaces and such as underscores,
// which the server matches the rule's names against (slug in search.go).
export function slug(name: string): string {
  const s = name.trim().replace(/[\s:()"\\[\]]+/g, '_')
  return s === '' ? '_' : s
}

// The values runs start with, by the top board's objects: what a run is
// given (an object no process generates), the record a shared struct's copy
// picks or follows, or the one a run was started on. Each attribute holding
// text or a number is one; an object without attributes is one whole.
// People and lists hold no one value.
export function branchValues(objects: FlowObject[], links: Link[]): BranchValue[] {
  const generated = new Set(links.filter((l) => l.direction === 'output').map((l) => l.object))
  const values: BranchValue[] = []
  for (const o of objects) {
    if (o.parent !== null || o.owner !== null || o.human || o.many) continue
    if (!o.shared && generated.has(o.id)) continue
    const hint = o.pick
      ? 'the record each run picks'
      : o.follows
        ? 'the record each run follows'
        : o.shared
          ? 'the record a run is started on'
          : 'given to each run'
    if (o.attributes.length === 0) {
      values.push({ insert: `{@${slug(o.name)}}`, label: o.name, hint })
      continue
    }
    for (const a of o.attributes) {
      if (a.file || a.type === 'boolean' || a.type === 'datetime') continue
      values.push({ insert: `{@${slug(o.name)}.${slug(a.name)}}`, label: `${o.name}.${a.name}`, hint })
    }
  }
  return values
}

// The value being typed at caret: from its `@`, or the `{` before it, to
// the caret, or past the `}` closing one written already; null when the
// caret isn't in one.
export function typedValue(text: string, caret: number): { from: number; to: number; typed: string } | null {
  const before = text.slice(0, caret)
  const at = before.lastIndexOf('@')
  if (at < 0) return null
  const typed = before.slice(at + 1)
  if (/[{}@/]/.test(typed)) return null
  const braced = at > 0 && text[at - 1] === '{'
  const close = braced ? /^[^{}@/]*\}/.exec(text.slice(caret)) : null
  return { from: braced ? at - 1 : at, to: caret + (close ? close[0].length : 0), typed }
}

// The values whose object's or attribute's name starts as typed, either way
// a name is written.
export function matchValues(values: BranchValue[], typed: string): BranchValue[] {
  const t = typed.trim().toLowerCase()
  if (t === '') return values
  const s = slug(t).toLowerCase()
  return values.filter((v) => {
    const label = v.label.toLowerCase()
    const key = v.insert.slice(2, -1).toLowerCase()
    const attribute = key.includes('.') ? key.slice(key.indexOf('.') + 1) : ''
    return label.startsWith(t) || key.startsWith(s) || attribute.startsWith(s)
  })
}
