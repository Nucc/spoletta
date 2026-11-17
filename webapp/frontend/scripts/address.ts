// How a page's address names its project (src/address.ts): reading the
// project and the page from an address, putting a page in a project, the
// project an address naming none opens in, rewriting such an address in
// place, naming the project in an address the browser opens itself
// (inProjectLink in src/api.ts), and keeping what a browser remembers apart
// for each project. Run with `npm test`, which needs Node 22.6 or later to
// load TypeScript.

import { hashIn, inTabsProject, outsideProjects, projectFor, projectKey, rewritten, splitHash } from '../src/address.ts'
import { inProjectLink } from '../src/api.ts'

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

// Reading an address: its project, and the page as addressed before projects.
check('a run of a flow', splitHash('#/p/3/flows/12/runs/40'), { project: 3, page: '#/flows/12/runs/40' })
check('a review', splitHash('#/p/3/flows/12/runs/40/steps/7'), { project: 3, page: '#/flows/12/runs/40/steps/7' })
check('the home page', splitHash('#/p/3/'), { project: 3, page: '#/' })
check('the home page without its slash', splitHash('#/p/3'), { project: 3, page: '#/' })
check('the home page narrowed', splitHash('#/p/3/?q=jira&show=flows'), { project: 3, page: '#/?q=jira&show=flows' })
check('the home page narrowed without its slash', splitHash('#/p/3?q=jira'), { project: 3, page: '#/?q=jira' })
check('a search kept in the address', splitHash('#/p/12/flows/4/data?q=status%3Afailed'), { project: 12, page: '#/flows/4/data?q=status%3Afailed' })
check('the settings', splitHash('#/p/1/settings/access'), { project: 1, page: '#/settings/access' })
check('an old address names none', splitHash('#/flows/12'), { project: null, page: '#/flows/12' })
check('no address at all', splitHash(''), { project: null, page: '' })
check('the projects page is in none', splitHash('#/projects'), { project: null, page: '#/projects' })
check('a project is a number', splitHash('#/p/x/flows/1'), { project: null, page: '#/p/x/flows/1' })
check('a project is never 0', splitHash('#/p/0/flows/1'), { project: null, page: '#/p/0/flows/1' })
check('a page starting like a project', splitHash('#/p/3flows'), { project: null, page: '#/p/3flows' })

// Putting a page in a project.
check('a board', hashIn(3, '#/flows/12'), '#/p/3/flows/12')
check('the home page', hashIn(3, '#/'), '#/p/3/')
check('the home page from no address', hashIn(3, ''), '#/p/3/')
check('the home page from a bare #', hashIn(3, '#'), '#/p/3/')
check('the home page narrowed', hashIn(3, '#/?q=jira'), '#/p/3/?q=jira')
check('another project moves it', hashIn(5, '#/p/3/flows/12'), '#/p/5/flows/12')
check('no project leaves it', hashIn(null, '#/flows/12'), '#/flows/12')
check('the projects page stays outside', hashIn(3, '#/projects'), '#/projects')
for (const page of ['#/flows/1/runs/2/steps/3', '#/', '#/?show=starred', '#/settings/audit?q=x', '#/taskboards/9', '#/inbox']) {
  check(`${page} reads back`, splitHash(hashIn(7, page)), { project: 7, page })
}
check('outside the projects', ['#/projects', '#/projects?x=1', '#/flows/1', '#/'].map(outsideProjects), [true, true, false, false])

// The project an address naming none opens in: the tab's, else the one
// last used, else the first the person may enter, passing over those they
// may not; while those aren't known, the tab's or last used as it is.
check('the tab’s first', projectFor(4, 2, [1, 2, 4]), 4)
check('then the one last used', projectFor(null, 2, [1, 2, 4]), 2)
check('then their first', projectFor(null, null, [1, 2, 4]), 1)
check('a tab’s project they lost', projectFor(9, 2, [1, 2]), 2)
check('a last used they lost', projectFor(null, 9, [3, 5]), 3)
check('none to enter', projectFor(null, 2, []), null)
check('not known yet, the tab’s', projectFor(4, 2, null), 4)
check('not known yet, the last used', projectFor(null, 2, null), 2)
check('not known yet, nothing to go on', projectFor(null, null, null), null)

// Rewriting an address naming no project in place.
check('an old link of a notification', rewritten('#/flows/7/runs/42/steps/9', 2), '#/p/2/flows/7/runs/42/steps/9')
check('an old bookmark of the home page', rewritten('', 2), '#/p/2/')
check('an old address of the settings', rewritten('#/settings/account', 2), '#/p/2/settings/account')
check('one naming a project already', rewritten('#/p/3/flows/7', 2), null)
check('the projects page', rewritten('#/projects', 2), null)
check('no project to put it in', rewritten('#/flows/7', null), null)

// Each tab keeps its own project: a link naming none followed in a tab
// stays in the project the tab showed, whatever another tab shows.
const tabs = { left: '#/p/3/flows/12', right: '#/p/5/taskboards/2' }
check('a link in the left tab', inTabsProject('#/flows/40', tabs.left), '#/p/3/flows/40')
check('the same link in the right tab', inTabsProject('#/flows/40', tabs.right), '#/p/5/flows/40')
check('the home page in the right tab', inTabsProject('#/', tabs.right), '#/p/5/')
check('a link naming its project stays there', inTabsProject('#/p/5/flows/40', tabs.left), '#/p/5/flows/40')
check('the projects page stays outside', inTabsProject('#/projects', tabs.left), '#/projects')
check('a tab outside the projects leaves it for the last used', inTabsProject('#/inbox', '#/projects'), '#/inbox')
check('a tab from before projects too', inTabsProject('#/inbox', '#/flows/12'), '#/inbox')
check('a tab with no address before', inTabsProject('#/flows/40', undefined), '#/flows/40')

// An address under /api/ the browser opens itself, which can't send the
// project's header, names it in its query: a run's file, an MCP server's
// sign-in window. Addresses about the person or the projects, signing in,
// and what isn't the webapp's stay as they are.
check('a run’s file', inProjectLink('/api/files?path=runs%2F4%2Freport.pdf', 3), '/api/files?path=runs%2F4%2Freport.pdf&project=3')
check('a file with a space keeps it', new URLSearchParams(inProjectLink('/api/files?path=a%20b.txt', 3).split('?')[1]).get('path'), 'a b.txt')
check('an MCP server’s sign-in', inProjectLink('/api/mcp-servers/9/connect', 12), '/api/mcp-servers/9/connect?project=12')
check('one naming a project already moves it', inProjectLink('/api/files?project=2&path=x', 5), '/api/files?project=5&path=x')
check('outside any project', inProjectLink('/api/files?path=x', null), '/api/files?path=x')
check('signing in', inProjectLink('/api/auth/login?next=%2F', 3), '/api/auth/login?next=%2F')
check('the person', inProjectLink('/api/me', 3), '/api/me')
check('the projects', inProjectLink('/api/projects/3/members', 3), '/api/projects/3/members')
check('not the webapp’s', inProjectLink('https://example.com/api/files?path=x', 3), 'https://example.com/api/files?path=x')

// What a browser remembers that is a project's own.
check('a project’s own key', projectKey('palette-recent', 3), 'palette-recent.p3')
check('another project’s apart', projectKey('palette-recent', 4) !== projectKey('palette-recent', 3), true)
check('outside any project, as it was', projectKey('palette-recent', null), 'palette-recent')

console.log(`${checked} checks of addresses and projects, ${failed} failed`)
if (failed > 0) process.exitCode = 1
