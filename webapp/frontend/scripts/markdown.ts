// What src/markdown.ts makes of text: whether it reads as Markdown, which
// text not typed Markdown is rendered as only then, and the plain text a
// cell or a card shows of it. Run with `npm test`, which needs Node 22.6 or
// later to load TypeScript.

import { isMarkdown, plainText } from '../src/markdown.ts'

declare const process: { exitCode?: number }

let failed = 0
let checked = 0
function check(what: string, got: unknown, want: unknown) {
  checked++
  if (JSON.stringify(got) !== JSON.stringify(want)) {
    failed++
    console.error(`${what}\n  got  ${JSON.stringify(got)}\n  want ${JSON.stringify(want)}`)
  }
}

check('a heading reads as Markdown', isMarkdown('# Findings\nAll good'), true)
check('a list reads as Markdown', isMarkdown('Found:\n- one\n- two'), true)
check('a table reads as Markdown', isMarkdown('| a | b |\n|---|---|\n| 1 | 2 |'), true)
check('a few inline marks read as Markdown', isMarkdown('See **this** and `that`'), true)
check('one stray mark is text', isMarkdown('Costs 3 * 4, so **twelve'), false)
check('plain text is text', isMarkdown('The build passed on the second try.'), false)

check(
  'a report on one line',
  plainText('# Findings\n\nThe **build** failed in `make test`:\n\n- a *flaky* test\n- see [the log](https://example.com/log)\n'),
  'Findings The build failed in make test: a flaky test see the log',
)
check('a numbered list and a quote', plainText('1. first\n2) second\n> quoted'), 'first second quoted')
check('ticked items', plainText('- [x] done\n- [ ] to do'), 'done to do')
check('an image as its text', plainText('![a chart](chart.png) above'), 'a chart above')
check('a fence keeps its code', plainText('Run:\n```sh\nmake test\n```'), 'Run: make test')
check('a table as its cells', plainText('| name | result |\n|---|:---:|\n| build | failed |'), 'name · result build · failed')
check('snake_case keeps its underscores', plainText('set max_runs and min_items'), 'set max_runs and min_items')
check('underscores and struck text', plainText('_slow_ and ~~gone~~ and __bold__'), 'slow and gone and bold')
check('a rule goes', plainText('above\n\n---\n\nbelow'), 'above below')
check('arithmetic keeps its asterisks', plainText('3 * 4 * 5'), '3 * 4 * 5')

if (failed > 0) {
  console.error(`${failed} of ${checked} checks failed`)
  process.exitCode = 1
} else {
  console.log(`markdown: ${checked} checks passed`)
}
