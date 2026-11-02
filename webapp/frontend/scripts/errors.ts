// What the pages say of a request that went wrong (ApiError and the
// server's state in src/api.ts): the webapp's own words, a proxy answering
// for it when it is down taken as the server not being reached, and the
// notice of how things stand with it.
// Run with `npm test`, which needs Node 22.6 or later to load TypeScript.

import { ApiError, answerError, connectionText, errorText, proxyAnswer, unreachable, UNREACHABLE } from '../src/api.ts'

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

const said = (e: ApiError) => [e.message, e.status, e.unreachable]

// The webapp's words, without the request they answer.
const held = answerError('PUT', '/api/processes/12', 409, 'text/plain; charset=utf-8', 'the board is held by run 412\n')
check('its words alone', said(held), ['the board is held by run 412', 409, false])
check('the request kept', [held.method, held.url], ['PUT', '/api/processes/12'])
check('a sentence on the page', errorText(held), 'The board is held by run 412')
check('an error kept as text', errorText('name is taken'), 'Name is taken')
check('anything else thrown', errorText(new Error('Boom')), 'Boom')

// The webapp's own 502s say what it couldn't reach, which is no outage.
check(
  'its own 502',
  said(answerError('POST', '/api/agent', 502, 'text/plain; charset=utf-8', 'worker unavailable: connection refused\n')),
  ['worker unavailable: connection refused', 502, false],
)
check(
  'its own 503',
  said(answerError('GET', '/api/flows', 503, 'text/plain; charset=utf-8', 'db error: timeout')),
  ['db error: timeout', 503, false],
)

// A proxy answering for the webapp when it is down.
check("Vite's empty 502", said(answerError('GET', '/api/flows', 502, 'text/plain', '')), [UNREACHABLE, 502, true])
check(
  "a load balancer's page",
  said(answerError('GET', '/api/flows', 503, 'text/html', '<html><body><h1>503 Service Unavailable</h1></body></html>')),
  [UNREACHABLE, 503, true],
)
check('a gateway timing out', proxyAnswer(504, '', '<!DOCTYPE html><title>Gateway Timeout</title>'), true)
check('not a 500', proxyAnswer(500, 'text/plain', ''), false)

// What the webapp didn't foresee, or answered with nothing to say.
check(
  'an internal error',
  said(answerError('GET', '/api/flows/3', 500, 'text/plain; charset=utf-8', 'internal server error\n')),
  ['Something went wrong on the server (500)', 500, false],
)
check('an empty refusal', said(answerError('DELETE', '/api/notes/2', 403, '', '')), ['The server refused the request (403)', 403, false])
check(
  'a page of HTML refusing',
  said(answerError('GET', '/api/me', 404, 'text/html', '<html>404</html>')),
  ['The server refused the request (404)', 404, false],
)

// Whether the server couldn't be reached, from an error or the text a page kept.
check('unreached error', unreachable(new ApiError('GET', '/api/flows', 0, UNREACHABLE, true)), true)
check('kept as text', unreachable(UNREACHABLE), true)
check('a refusal', unreachable(held), false)
check('the browser’s own words', unreachable(new TypeError('Failed to fetch')), false)
check('nothing', unreachable(null), false)

// The notice of how things stand with the server.
const state = (s: Partial<{ offline: boolean; lostChange: boolean; round: number; dismissed: number }>) => ({
  offline: false,
  lostChange: false,
  round: 1,
  dismissed: 0,
  ...s,
})
check('answering', connectionText(state({})), null)
check('not answering', connectionText(state({ offline: true })), 'Can’t reach the server, so this page may be out of date. Waiting for it to come back…')
check(
  'not answering, a change lost',
  connectionText(state({ offline: true, lostChange: true })),
  'Can’t reach the server, so changes aren’t being saved. Waiting for it to come back…',
)
check('back, a change lost', connectionText(state({ lostChange: true })), 'The server is back, but changes made while it was away weren’t saved.')
check('dismissed', connectionText(state({ offline: true, dismissed: 1 })), null)
check('something new since', connectionText(state({ offline: true, round: 2, dismissed: 1 })) !== null, true)

console.log(`${checked} checks of what errors say, ${failed} failed`)
if (failed > 0) process.exitCode = 1
