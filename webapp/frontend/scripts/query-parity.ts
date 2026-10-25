// The search bar's parser (src/query.ts) checked against the queries in
// webapp/testdata/search_grammar.json, which the server's parser
// (search.go) is checked against too (search_grammar_test.go), so the two
// can't read a query differently. Each query's tree is written in a form
// neither parser's own shape shows through, the same as the Go test
// writes it, and each value quote() writes has to read back as itself.
// Run with `npm test`, which needs Node 22.6 or later to load TypeScript;
// it is outside src, so neither tsc -b nor the bundle sees it.

import fixture from '../../testdata/search_grammar.json' with { type: 'json' }
import { parse, quote, type Node, type Test } from '../src/query.ts'

declare const process: { exitCode?: number }

// Text in double quotes, with only backslashes and double quotes escaped.
const q = (s: string) => `"${s.replace(/["\\]/g, (c) => '\\' + c)}"`

function test(t: Test): string {
  switch (t.op) {
    case 'is':
      return `(${t.literal ? 'is-literal' : 'is'} ${q(t.value)})`
    case 'exists':
      return 'exists'
    case 'range':
      return `(range ${q(t.from)} ${q(t.to)})`
    default:
      return `(${t.op} ${q(t.value)})`
  }
}

function canonical(n: Node): string {
  switch (n.kind) {
    case 'all':
      return 'all'
    case 'and':
    case 'or':
      return `(${n.kind} ${n.nodes.map(canonical).join(' ')})`
    case 'not':
      return `(not ${canonical(n.node)})`
    case 'term':
      return `(term ${n.key === null ? '_' : q(n.key)} ${test(n.test)})`
  }
}

const cases: { query: string; ast: string }[] = fixture.cases
const quotes: { value: string; quoted: string }[] = fixture.quotes
let failed = 0
const fail = (what: string) => {
  failed++
  console.error(what)
}
for (const c of cases) {
  const got = canonical(parse(c.query).ast)
  if (got !== c.ast) fail(`${JSON.stringify(c.query)}\n  got  ${got}\n  want ${c.ast}`)
}
for (const c of quotes) {
  const got = quote(c.value)
  if (got !== c.quoted) fail(`quote(${JSON.stringify(c.value)}) = ${JSON.stringify(got)}, want ${JSON.stringify(c.quoted)}`)
  const want = `(term "k" (${got.startsWith('"') ? 'is-literal' : 'is'} ${q(c.value)}))`
  const read = canonical(parse(`k:${got}`).ast)
  if (read !== want) fail(`k:${got}\n  got  ${read}\n  want ${want}`)
}
console.log(`${cases.length} queries and ${quotes.length} quoted values, ${failed} failed`)
if (failed > 0) process.exitCode = 1
