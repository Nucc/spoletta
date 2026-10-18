import type { SearchFacet } from './api'

// A search over a table page's rows written the way a log explorer
// takes one. The server searches (search.go), over every row stored; this
// parses a query the same way, to colour it in the search bar and to tick
// facets' values, and rewrites it as facets are ticked, so the query stays
// the only thing saying what shows. See search.go for what a query says.

// The facets a search found, by their key in lower case; two objects can
// share a name.
export type Facets = { list: SearchFacet[]; byKey: Map<string, number[]> }

export function indexFacets(list: SearchFacet[]): Facets {
  const byKey = new Map<string, number[]>()
  list.forEach((f, i) => {
    const k = f.key.toLowerCase()
    byKey.set(k, [...(byKey.get(k) ?? []), i])
  })
  return { list, byKey }
}

// The facets a key names, ignoring case, with or without its @, or else
// the attributes of that name of any object: title for @Bug.title.
export function resolve(facets: Facets, key: string): number[] {
  const k = key.toLowerCase()
  const exact = facets.byKey.get(k) ?? (k.startsWith('@') ? facets.byKey.get(k.slice(1)) : facets.byKey.get('@' + k))
  if (exact) return exact
  const tail = '.' + k.replace(/^@/, '')
  return [...facets.byKey].filter(([f]) => f.startsWith('@') && f.endsWith(tail)).flatMap(([, i]) => i)
}

// A value as a query takes it: quoted where it holds what would end it,
// or a * that would stand for any text.
export function quote(value: string): string {
  return /^[^\s():"\\[\]<>*]+$/.test(value) && !/^(OR|AND|NOT)$/.test(value) && !value.startsWith('-')
    ? value
    : `"${value.replace(/["\\]/g, (c) => '\\' + c)}"`
}

export type Sort = { key: string; desc: boolean }

export type Test =
  | { op: 'is'; value: string; literal: boolean }
  | { op: 'exists' }
  | { op: '<' | '<=' | '>' | '>='; value: string }
  | { op: 'range'; from: string; to: string }

// A part of a query, and where in its text it is.
export type Node = { from: number; to: number } & (
  | { kind: 'all' }
  | { kind: 'and' | 'or'; nodes: Node[] }
  | { kind: 'not'; node: Node }
  | { kind: 'term'; key: string | null; test: Test }
)

// A stretch of the query's text by what it is, for colouring it.
export type Mark = { from: number; to: number; kind: 'key' | 'value' | 'op' | 'text' }

const space = /\s/

class Parser {
  i = 0
  marks: Mark[] = []
  // The key a group of values after a colon is for, as in status:(a OR b).
  key: string | null = null
  readonly s: string

  constructor(s: string) {
    this.s = s
  }

  ws() {
    while (this.i < this.s.length && space.test(this.s[this.i])) this.i++
  }

  // Whether an operator word starts here, on its own.
  word(w: string): boolean {
    if (!this.s.startsWith(w, this.i)) return false
    const next = this.s[this.i + w.length]
    return next === undefined || space.test(next) || next === '(' || (w !== 'NOT' && next === ')')
  }

  mark(from: number, to: number, kind: Mark['kind']) {
    if (to > from) this.marks.push({ from, to, kind })
  }

  or(): Node {
    const nodes = [this.and()]
    for (;;) {
      this.ws()
      if (!this.word('OR')) break
      this.mark(this.i, this.i + 2, 'op')
      this.i += 2
      nodes.push(this.and())
    }
    const real = nodes.filter((n) => n.kind !== 'all')
    if (real.length <= 1) return real[0] ?? nodes[0]
    return { kind: 'or', nodes: real, from: real[0].from, to: real[real.length - 1].to }
  }

  and(): Node {
    const nodes: Node[] = []
    const start = this.i
    for (;;) {
      this.ws()
      if (this.i >= this.s.length || this.s[this.i] === ')' || this.word('OR')) break
      if (this.word('AND')) {
        this.mark(this.i, this.i + 3, 'op')
        this.i += 3
        continue
      }
      const n = this.unary()
      if (n.kind !== 'all') nodes.push(n)
    }
    if (nodes.length === 0) return { kind: 'all', from: start, to: this.i }
    if (nodes.length === 1) return nodes[0]
    return { kind: 'and', nodes, from: nodes[0].from, to: nodes[nodes.length - 1].to }
  }

  unary(): Node {
    const start = this.i
    const next = this.s[this.i + 1]
    if (this.s[this.i] === '-' && next !== undefined && !space.test(next) && next !== ')') {
      this.mark(start, start + 1, 'op')
      this.i++
      const node = this.primary()
      return node.kind === 'all' ? node : { kind: 'not', node, from: start, to: node.to }
    }
    if (this.word('NOT')) {
      this.mark(start, start + 3, 'op')
      this.i += 3
      this.ws()
      if (this.i >= this.s.length || this.s[this.i] === ')') return { kind: 'all', from: start, to: this.i }
      const node = this.unary()
      return node.kind === 'all' ? node : { kind: 'not', node, from: start, to: node.to }
    }
    return this.primary()
  }

  primary(): Node {
    const start = this.i
    if (this.s[this.i] === '(') {
      this.mark(start, start + 1, 'op')
      this.i++
      const node = this.or()
      this.ws()
      if (this.s[this.i] === ')') {
        this.mark(this.i, this.i + 1, 'op')
        this.i++
      }
      return { ...node, from: start, to: this.i }
    }
    if (this.key !== null) return this.value(this.key, start)
    if (this.s[this.i] === '"') {
      const value = this.quoted()
      this.mark(start, this.i, 'text')
      return { kind: 'term', key: null, test: { op: 'is', value, literal: true }, from: start, to: this.i }
    }
    // A key runs up to its colon; a term without one is text.
    let j = this.i
    while (j < this.s.length && !space.test(this.s[j]) && !'():'.includes(this.s[j])) j += this.s[j] === '\\' ? 2 : 1
    if (this.s[j] === ':' && j > this.i) {
      const key = unescape(this.s.slice(this.i, j))
      this.mark(this.i, j + 1, 'key')
      this.i = j + 1
      return this.value(key, start)
    }
    const value = this.bare()
    this.mark(start, this.i, 'text')
    // A dash on its own is a term being typed.
    if (value === '-') return { kind: 'all', from: start, to: this.i }
    return { kind: 'term', key: null, test: { op: 'is', value, literal: false }, from: start, to: this.i }
  }

  // What follows a key's colon.
  value(key: string, start: number): Node {
    const term = (test: Test): Node => ({ kind: 'term', key, test, from: start, to: this.i })
    const c = this.s[this.i]
    if (c === undefined || space.test(c) || c === ')') return { kind: 'all', from: start, to: this.i }
    if (c === '(') {
      const outer = this.key
      this.key = key
      const node = this.primary()
      this.key = outer
      return { ...node, from: start, to: this.i }
    }
    const at = this.i
    if (c === '"') {
      const value = this.quoted()
      this.mark(at, this.i, 'value')
      return term({ op: 'is', value, literal: true })
    }
    if (c === '[') {
      const end = this.s.indexOf(']', this.i)
      const inner = this.s.slice(this.i + 1, end < 0 ? this.s.length : end)
      this.i = end < 0 ? this.s.length : end + 1
      this.mark(at, this.i, 'value')
      const [from, to] = inner.split(/\s+TO\s+/)
      return term({ op: 'range', from: (from ?? '').trim() || '*', to: (to ?? '').trim() || '*' })
    }
    const op = /^(>=|<=|>|<)/.exec(this.s.slice(this.i))?.[1] as '<' | '<=' | '>' | '>=' | undefined
    if (op) {
      this.mark(at, at + op.length, 'op')
      this.i += op.length
      const v = this.s[this.i] === '"' ? this.quoted() : this.bare()
      this.mark(at + op.length, this.i, 'value')
      return v === '' ? { kind: 'all', from: start, to: this.i } : term({ op, value: v })
    }
    const value = this.bare()
    this.mark(at, this.i, 'value')
    return term(value === '*' ? { op: 'exists' } : { op: 'is', value, literal: false })
  }

  // Text up to a space or a bracket, with a backslash keeping what follows it.
  bare(): string {
    const start = this.i
    while (this.i < this.s.length && !space.test(this.s[this.i]) && !'()'.includes(this.s[this.i]))
      this.i += this.s[this.i] === '\\' ? 2 : 1
    this.i = Math.min(this.i, this.s.length)
    if (this.i === start) this.i++ // A stray character, taken as text of its own.
    return unescape(this.s.slice(start, this.i))
  }

  quoted(): string {
    let out = ''
    this.i++
    while (this.i < this.s.length && this.s[this.i] !== '"') {
      if (this.s[this.i] === '\\' && this.i + 1 < this.s.length) this.i++
      out += this.s[this.i++]
    }
    if (this.i < this.s.length) this.i++
    return out
  }
}

const unescape = (s: string) => s.replace(/\\(.)/g, '$1')

// The query as a tree, and its text marked by what each part is. Brackets
// closing nothing are skipped, so the rest still counts.
export function parse(text: string): { ast: Node; marks: Mark[] } {
  const p = new Parser(text)
  const nodes: Node[] = []
  for (;;) {
    const n = p.or()
    if (n.kind !== 'all') nodes.push(n)
    p.ws()
    if (p.i >= text.length) break
    p.i++ // A ) closing nothing.
  }
  const ast: Node =
    nodes.length === 0
      ? { kind: 'all', from: 0, to: text.length }
      : nodes.length === 1
        ? nodes[0]
        : { kind: 'and', nodes, from: nodes[0].from, to: nodes[nodes.length - 1].to }
  return { ast, marks: p.marks }
}

// The terms all of which a row has to match.
function clauses(ast: Node): Node[] {
  return ast.kind === 'all' ? [] : ast.kind === 'and' ? ast.nodes : [ast]
}

// The facets a key names, as one string, so keys naming the same compare equal.
function idOf(facets: Facets, key: string): string {
  return resolve(facets, key).join(',') || key.toLowerCase()
}

// What the facet list ticks for rows with no value for a facet, as -key:*
// says in a query.
export const NONE = '\u0000none'

// The values a term ticks of the facet: status:done, status:(done OR
// failed), or -status:* for none.
function ticked(node: Node, id: string, facets: Facets): string[] | null {
  if (node.kind === 'not')
    return node.node.kind === 'term' && node.node.key !== null && node.node.test.op === 'exists' && idOf(facets, node.node.key) === id
      ? [NONE]
      : null
  if (node.kind === 'term')
    return node.key !== null && node.test.op === 'is' && (node.test.literal || !node.test.value.includes('*')) && idOf(facets, node.key) === id
      ? [node.test.value]
      : null
  if (node.kind !== 'or') return null
  const values: string[] = []
  for (const n of node.nodes) {
    const v = ticked(n, id, facets)
    if (v === null) return null
    values.push(...v)
  }
  return values
}

// The values of the facet the query has ticked.
export function selection(facets: Facets, ast: Node, key: string): string[] {
  const id = idOf(facets, key)
  return clauses(ast).flatMap((c) => ticked(c, id, facets) ?? [])
}

// The query with its terms the keep test turns down taken out, and the
// term given added, keeping how the rest of it was written.
function rewrite(text: string, keep: (n: Node) => boolean, add: string | null): string {
  const { ast } = parse(text)
  const parts = clauses(ast)
    .filter(keep)
    .map((c) => text.slice(c.from, c.to))
  // A query of either of two things stays one when another term joins it.
  if (ast.kind === 'or' && parts.length === 1 && add !== null) parts[0] = `(${parts[0]})`
  if (add !== null) parts.push(add)
  return parts.join(' ')
}

// The query with the facet's ticked values those given.
export function select(facets: Facets, text: string, key: string, values: string[]): string {
  const id = idOf(facets, key)
  const some = values.filter((v) => v !== NONE)
  let add = some.length === 0 ? null : some.length === 1 ? `${key}:${quote(some[0])}` : `${key}:(${some.map(quote).join(' OR ')})`
  if (values.includes(NONE)) add = add === null ? `-${key}:*` : `(${add} OR -${key}:*)`
  return rewrite(text, (c) => ticked(c, id, facets) === null, add)
}

// The query leaving out rows with the value.
export function exclude(facets: Facets, text: string, key: string, value: string): string {
  const id = idOf(facets, key)
  const term = `-${key}:${quote(value)}`
  // A term ticking the value goes, as it can't both be and not be.
  return rewrite(
    text,
    (c) => {
      const v = ticked(c, id, facets)
      return v === null || !v.some((x) => x.toLowerCase() === value.toLowerCase())
    },
    term,
  )
}

// The comparison terms on the facet, such as started:>now-1d, as their text.
export function bounds(facets: Facets, text: string, key: string): string[] {
  const id = idOf(facets, key)
  return clauses(parse(text).ast)
    .filter((c) => c.kind === 'term' && c.key !== null && c.test.op !== 'is' && c.test.op !== 'exists' && idOf(facets, c.key) === id)
    .map((c) => text.slice(c.from, c.to))
}

// The query with the facet's comparison terms replaced by the one given, or none.
export function bound(facets: Facets, text: string, key: string, term: string | null): string {
  const id = idOf(facets, key)
  return rewrite(
    text,
    (c) => !(c.kind === 'term' && c.key !== null && c.test.op !== 'is' && c.test.op !== 'exists' && idOf(facets, c.key) === id),
    term,
  )
}
