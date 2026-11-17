# Projects

A project is a tenant: a space of its own with its own flows, task boards,
procedures and structs, its own model providers and keys, default models,
MCP servers and their sign-ins, connectors, skills and commands,
credentials, allowed internet hosts, git token, limits, teams, roles, API
keys, webhooks and audit log. A new project is empty, as the app is when
first started. Nothing in one project reaches anything in another: every row
below the projects names its project, the database checks that a row is in
the project of what it belongs to, and the code keeps every request, query,
run and proxy to the one it is worked in.

The concept is described at the top of
[webapp/projects.go](../webapp/projects.go) (people, levels, requests),
[webapp/run_projects.go](../webapp/run_projects.go) (runs and what starts
them) and [webapp/board_projects.go](../webapp/board_projects.go) (boards
and what they refer to).

1. [People and levels](#people-and-levels)
2. [Groups from the identity provider](#groups-from-the-identity-provider)
3. [Every table names its project](#every-table-names-its-project)
4. [Legacy and what the instance is configured with](#legacy-and-what-the-instance-is-configured-with)
5. [Requests](#requests)
6. [Boards](#boards)
7. [Runs and what starts them](#runs-and-what-starts-them)
8. [The worker](#the-worker)
9. [Settings and proxies](#settings-and-proxies)
10. [Limits and ceilings](#limits-and-ceilings)
11. [The setup file, templates and the assistant](#the-setup-file-templates-and-the-assistant)
12. [The frontend](#the-frontend)
13. [How the walls are tested](#how-the-walls-are-tested)
14. [Adding a table, a route or a query](#adding-a-table-a-route-or-a-query)
15. [Operating it](#operating-it)
16. [Limits](#limits)

## People and levels

People are the instance's: one sign-in, one `users` row, in no project of
its own. `users.admin` makes someone a **super admin**, who makes, renames,
archives, brings back and deletes projects, enters any project as its
owner, adds, removes and promotes people, makes team managers, and sets the
ceilings over every project's limits.

Within a project someone is at one of three levels:

| Level | May |
|---|---|
| `owner` | everything in it, its settings, who has access and their roles included |
| `editor` | change and run flows, task boards, procedures and structs, and the settings anyone may change, such as their own skills and API keys |
| `viewer` | see boards, runs and records; change and start nothing, but for starring a flow, saying they have its board open, deciding and commenting on the reviews their roles decide, and deleting their own API key |

Their level comes from, the highest winning, earlier ones winning ties
(`projectView.work`):

- **Being given it by hand**: a row in `project_members` with `source`
  `manual`, at a level an owner picks.
- **A group of the identity provider's** the project is given to:
  `project_groups (project_id, group_name, level)`, matched exactly against
  the groups the person is in, `user_groups`; see
  [Groups](#groups-from-the-identity-provider).
- **What the project lets anyone signed in do**: `projects.others_access`,
  `none` (the default), `view` or `edit`.
- **Being a super admin**, who enters every project as an owner.

Only a manual member's level is kept: what a group, `others_access` or being
a super admin give is worked out at each request, so giving a project to a
group, or taking it away, holds at once. Someone entering another way than
by hand gets a `project_members` row the first time they enter, with
`source` `group`, `open` or `super` (`enterAsMember`), so that what they own
there, a team they are in and the roles they hold have a member to point at.
That row is soft-removed (`removed_at`) when the group stops being given the
project or `others_access` goes down; what they made stays. Removing a
member is always soft. Making someone a super admin, or one no more, leaves
their level in each project as it was given.

An archived project is entered by super admins alone.

Someone signing in the first time, or added by a super admin, is in no
project until given one, by hand, by a group or by a project's
`others_access`. A super admin adding someone (`POST /api/users`) may give
them a project and a level at once, and roles there only with them, as roles
are a project's. Someone who may enter no project, or not the one an
address names, is told so, with the super admins to ask (`GET /api/me`'s
`admins`).

Within a project, ownership goes on as [webapp/owners.go](../webapp/owners.go)
has it: a flow, procedure, struct, task board or skill is its maker's, a
team's or everyone's, everyone being everyone in the project. An owner of
the project sees and changes everything in it. A viewer sees what an editor
would and changes none of it. **Roles** (`user_roles.project_id`) and
**teams** (`teams.project_id`, named uniquely there) are the project's: its
owners give its members roles, and its owners and team managers make its
teams of people who may enter it. Being a team manager is the instance's
(`users.team_manager`): they make teams in each project they enter as an
editor or owner.

Without sign-in there is nobody: every request is every project's owner, and
may make and switch projects.

## Groups from the identity provider

`OIDC_GROUPS_CLAIM` names the claim of the company's provider saying which
groups someone is in: a string, one group, or a list of strings, in the ID
token or, failing that, in what the provider's userinfo endpoint says.
Google's sign-ins carry none. Names longer than 255 characters are left
out, as no project could be given them. Each sign-in replaces the person's
groups (`user_groups`) and records when they were read
(`users.groups_read_at`). See [webapp/users.go](../webapp/users.go).

A session of someone with groups keeps what it needs to read them again: its
issuer, the provider's refresh token and access token, sealed with
`SECRET_KEY`, when the access token expires, where the groups were found
(`groups_from`) and when to read them next (`groups_check_at`). Every
replica looks every five seconds (`watchGroups`, `readGroupsDue`) for
sessions due whose person's groups were read longer ago than an hour, or
half `GROUPS_MAX_AGE` if that is shorter, claims each by moving its
`groups_check_at` on, and reads the groups again with the refresh token, or
the access token while it lasts. A provider refusing to refresh ends the
session. A session whose groups can't be read again keeps its tokens no
more. A session of someone in any group whose groups were read longer ago
than `GROUPS_MAX_AGE` (24 hours unless set) is over (`staleGroups` in
`sessionUser`), so they sign in again. The sign-in asks for
`offline_access` only when `OIDC_GROUPS_CLAIM` is set and the provider's
discovery document lists it.

So how stale groups can get:

- Someone taken out of a group loses what it gave them at the next reading,
  within the hour (or half `GROUPS_MAX_AGE`); with no refresh token, once
  `GROUPS_MAX_AGE` has passed, as their session ends.
- Someone put in a group who was in some group already gains it at the next
  reading. Someone in no group isn't read again: a group added to them shows
  only at their next sign-in.
- What runs on someone's behalf without a session, an API key's runs, a
  webhook's, a schedule's and a playing task board's, weighs their groups as
  last read, until they sign in again or a session of theirs reads them.

## Every table names its project

Every table below the projects has `project_id BIGINT NOT NULL`, with no
default, so a row inserted without naming its project is refused. (The
column came in with `DEFAULT 1` in migration `00050`, while the inserts came
to name their project, and `00090` dropped it.) Above the projects, naming
none or having it as part of what they are, are `projects`, `users`,
`user_identities`, `user_groups`, `sessions`, `project_groups`,
`project_members`, `instance_events`, `instance_limits`, `replicas`,
`replica_commands` and `goose_db_version`, which the e2e test
`TestProjectsEveryTableNamesItsProject` lists.

The column alone proves nothing, so the database checks it:

- **What other rows refer to is unique by its id and its project**
  (`UNIQUE (id, project_id)`, migration `00052`): flows, versions,
  processes, objects, links, states, object instances, runs, steps,
  schedules, shared records, teams, credentials, MCP servers, task boards
  and webhooks.
- **Each foreign key to a row of a project names the project too**
  (`00053`, `00065`, `00091`): `(flow_id, project_id) REFERENCES flows (id,
  project_id)`, so a row can't be in another project than its parent. So
  are a flow's versions and owner team, a credential's teams, an MCP token's
  server, a copy's source, the object an object specializes, what a note
  points at, a task board column's struct and flow, and the record a run
  takes, starts on or a task board runs on.
- **A row naming a person as one of a project's** refers to
  `project_members (user_id, project_id)`: a flow's, skill's or task board's
  owner, a team's member, a role's holder, an API key's maker. Members are
  only ever soft-removed, so the key proves they were a member once; the code
  checks that they may enter now (`memberOfProject`).
- **Names unique in the instance before are unique in their project**
  (`00051`): teams, MCP servers, skills and commands, credentials (name and
  variable), model providers, model keys, the default models, limits and
  internet hosts. A person holds roles in each project apart.
- What no other row holds to, a flow, a task board, the settings and what is
  kept apart from them (the audit log, what models cost, grants, people's
  slots, MCP sign-ins under way), refers to `projects`.

What the database doesn't check, and why:

- **Keys that would null their column as what they refer to goes.** MySQL
  nulls every column of a key `ON DELETE SET NULL`, which `project_id`,
  never null, can't be, so such keys take no action and the code nulls them
  first: `deleteFlow` a flow's version and the one it published and a task
  board column's struct and flow; `releaseGoing` (whatever deletes
  processes or objects calls it) a copy's source, an object's general and a
  note's process and object.
- **A struct's store and the struct a reference refers to**
  (`objects.store_id`, `object_attributes.ref_id`) have no key: they outlive
  the struct deleted for good, so that its copies still read its records
  and references are still checked against them. The routes writing them
  keep them to the project (`checkRefHere`, `checkStructure`, `checkFollow`),
  and bringing a board back leaves out one of another project.
- **Runs among themselves** keep no key on `runs.parent_run_id` and
  `run_steps.child_run_id` (their flows are deleted for good apart, and a run
  inside another may go before the step naming it), `runs.rerun_of` (it
  goes in the same statement as the run it re-ran), `runs.trial_of` (a
  process, kept after the process goes), nor `shared_records.object_id` (a
  record outlives its struct's object). The engine keeps them to the
  project: `startChildRun` refuses another project's flow (`errOtherProject`).
- **Run and step ids in `audit_events`, `llm_usage`, `proxy_grants`,
  `person_slots` and `record_changes`** are values, never keys: logs outlive
  the runs they name, and grants and slots are short-lived rows each replica
  clears. `mcp_tokens.user_id` has no key either, as a server taking one
  token keeps it as user 0's, whom nobody is.
- **JSON**: versions' and history's snapshots, what a step keeps as JSON,
  the board clipboard and setup files are read by no key. Whatever writes rows from
  them takes the project from the flow, process or object they go in, never
  from what the JSON says ([Boards](#boards)).

## Legacy and what the instance is configured with

Migration `00049` makes project 1, **Legacy**, on every database, with
`projects.inherits_instance` set. On a database from before projects
everything there is belongs to it, and everyone there was becomes its
member by hand: admins as owners, others as editors, those removed
soft-removed, so what they still own has a member to point at. On a new
database it is the one project there is, empty. No route sets
`inherits_instance`, so Legacy is the only project with it. Legacy can be
renamed and archived like any project, but deleting it for good is refused.

What the instance is configured with outside the database belongs only to a
project inheriting it (`inheritsInstance` in the webapp, the worker's
`inheritsInstance` in [worker/env.go](../worker/env.go)):

- the providers of the worker's own pi config (`models.json`,
  `settings.json`), its default model, the model keys saved for their
  variables (`model_keys`) and those in the webapp's environment, and
  `models.json`'s prices;
- model providers whose key is in the webapp's environment rather than saved
  (`keyEnv`): another project's owners are refused one (`envKeyRefused`), as
  the proxy would send the webapp's environment wherever their provider's
  URL says;
- `LLM_UPSTREAMS`;
- the GitHub App's tokens (`GITHUB_APP_*`) and `GIT_TOKEN`;
- the worker's `AWS_*` variables, the variables `CHILD_ENV` names,
  `CLAUDE_SMALL_MODEL`, its own providers' say in Claude Code's choice of
  Bedrock, and its git config, global and system;
- dialling a host its owners allowed by name at a private address.

Another project has only what its owners set in its settings, a git token
of its own included.

## Requests

Every `/api/` request about what is in a project names it in the
`X-Spoletta-Project` header; a page the browser opens itself, such as an MCP
server's sign-in window or a run's file, can't send it, so a GET names its
project in its address's `project` parameter instead. `enterProject`, before
`guardFlows`, refuses a request naming none (400) or naming something other
than a project's id (400), answers 404 for no such project, or an archived
one to anyone but a super admin, 403 to someone with no level there, and 403
to a viewer whose request would change or start anything; makes the member
row of someone entering another way than by hand; and passes the request on
with the project in its context (`requestProject`) and its user as they are
there.

So in a handler:

- `u.Admin`, `refuseNonAdmin`, `refuseKeys` and `refuseMCPConfig` mean an
  owner of the request's project;
- `u.Super` and `refuseNonSuper` mean a super admin;
- `refuseNonTeamManager` means an owner of the project or a team manager;
- `u.Project` is the project, which `ownedBy` and `visibleFlows` keep what
  someone owns, sees and may change to.

A viewer's requests that change or start anything are refused in one place,
by `neededAccess` in [webapp/owners.go](../webapp/owners.go): GET and HEAD
read, and everything else changes, but starring a flow (`favouritePattern`),
saying a board is open (`presencePattern`), deciding a review
(`decidePattern`, which its route lets only those holding its role do),
commenting on one (`commentPattern`) and deleting an API key of one's own
(`ownKeyPattern`). Connecting an MCP server, though a GET, changes what
steps may do on someone's behalf (`connectPattern`).

`guardFlows` answers 404 for anything a request names that is in another
project, a flow or anything in one, or a task board, whoever asks, super
admins and nobody signing in too. It finds what a request names from its
path (`flowPaths`), its query and JSON body (`flowFields`, `bodyRefs`), a
version diff's `to` and a run's file's path, so each route needn't.

Requests about the person, the projects or the instance name none, or go on
whatever their level in the one they name (`outsideProjects`): `/api/me`
and `/api/me/…`, `/api/projects` and `/api/projects/…`, `/api/instance/…`,
`/api/inbox`, and the MCP sign-in's callback (`/api/mcp-oauth/callback`),
which comes back with only the sign-in's state, whose project it is.
`/api/auth/…`, signing in and out, is no project's. A handler of a request
inside the projects reads its project with `requestProject`, which panics
for a request naming none; one outside them reads it with
`requestProjectIf`.

| Route | Who | What |
|---|---|---|
| `GET /api/me` | anyone | who is signed in, with their roles and teams in the project named, whether they are a super admin (`super`), the projects they may enter with their level there (`projects`), and, for someone who may enter none or not the one named, the super admins to ask (`admins`) |
| `GET /api/projects` | anyone | the projects they may enter, by name; a super admin's are all of them, archived too |
| `POST /api/projects` | super admins | makes an empty project, `{name}` |
| `PATCH /api/projects/{id}` | its owners | `{name, othersAccess}`; a super admin brings an archived one back with `{"archived": false}` |
| `DELETE /api/projects/{id}` | super admins | archives it; once archived, `?forever=true` deletes it for good |
| `GET`, `PUT /api/projects/{id}/members` | its owners | who is given it by hand and at which level, `[{user \| email, level}]`, in place of those who were; those left out are removed |
| `GET`, `PUT /api/projects/{id}/groups` | its owners | the groups it is given to, `[{group, level}]` |
| `GET`, `PUT /api/instance/limits` | super admins | the ceilings over every project's limits |
| `GET /api/instance/events` | super admins | what happened above the projects, searched as the audit log is |
| `POST /api/users`, `DELETE /api/users/{id}` | super admins | adding someone, with a project, level and roles if given, and removing them |
| `PATCH /api/users/{id}` | super admins, and the project's owners for `roles` | making someone a super admin or team manager, and the roles they hold in the request's project |

Renaming a project, what anyone signed in may do there and who it is given
to are audited in its own log (`project.renamed`, `project.access`,
`project.members`, `project.groups`); making, archiving, bringing back and
deleting one, and making someone a super admin or one no more, in
`instance_events` (`project.made`, `project.archived`, `project.restored`,
`project.deleted`, `user.super`).

Outside `/api/`, `/v1` takes the project from the API key, `/hooks/{token}`
from the webhook's flow, and the proxies (`/llm`, `/mcp`, `/credentials`
and the internet proxy) from the step's grant in `proxy_grants`.

## Boards

Every row of a board is its flow's project's: processes, objects, states,
attributes, links and the states they decide between, connections, starts,
flow processes' boards, notes, samples, schedules, versions and the roles
they name, history, and who has the board open and changed it. Each is
written naming the project taken from what it belongs to, never from what a
version's or a step's JSON, a clipboard or a setup file says, so a board
brought back from one is its flow's project's whatever it holds.

Nothing on a board refers to another project: a subflow process's
procedure, a copy's source and store, the object an object specializes, a
reference attribute's struct, what a note points at, and the MCP servers,
skills, credentials and connector a process picks. A route writing one
refuses it as if it weren't there (`checkCall`, `settingsElsewhere`,
`checkRefHere`, `checkStructure`, `checkFollow`). Bringing a board back,
from a version, history, a setup file or the assistant, and pasting leave
it out instead, as they do what is gone (`settingsElsewhere` with `drop`).
Restoring a version naming processes or objects another flow has is refused
(409, `errOthersRows`). Pasting from another project's flow is refused
(404), as the guard finds it.

A flow never moves between projects: exporting it and importing the file
into another project carries it over, as a new flow there.

## Runs and what starts them

A run is its flow's project's, and so is everything it writes: its steps,
objects, logs, records and what it stores, its spending and its audit
events. The project goes with it from where it starts (`newRun`,
`startChildRun`) into what walks it (`pendingRun.project`,
`flowRun.project`, `runBoard`) and what the worker is asked
(`workspaceSpec.Project`), and a replica taking a run over reads it again
from the run (`adopt.go`). A board somehow naming another project's
procedure, struct, store or record, which editing it never lets it, doesn't
make a run reach it: the step, or the run, fails saying so.

What starts a run outside a request is for whoever made it, as they are in
the project now:

- an **API key**'s runs are its maker's (`apiKeyUser`, `apiCaller`): its
  project's flows they may run, and nothing once they may no longer enter
  the project; a viewer's key reads what a viewer reads and starts nothing;
- a **webhook**'s, a **schedule**'s and a **playing task board**'s are for
  whoever added it, made it or pressed Play (`memberAs`): an owner there or
  an editor. Someone removed, who may no longer enter the project, or who
  only sees it now, has nothing started: a webhook refuses its deliveries, a
  schedule skips its times, saying why in the server's log, and a board stops
  playing, saying why (`boardRunner`).

An **archived project** starts no runs (`errProjectArchived`, which is
`sql.ErrNoRows` too, so whatever answers a flow that isn't there answers it
the same): `newRun` and `startChildRun` refuse, starting one by hand answers
409, `/v1` and `/hooks` answer 404, schedules skip, and a playing board
stops. Runs going on as it is archived go on.

The links the webapp writes into messages and API answers name the project,
`/#/p/<id>/flows/…` (`runAddress` in `api.go`, `projectPath` in
`notify.go`).

## The worker

Every agent and script request, and every diff request (`diffRequest` in
`changes.go`), names the step's project and says whether it inherits the
instance's configuration (`project`, `inheritsInstance`); a follow-up takes
its run's project. See the top of [worker/workspace.go](../worker/workspace.go)
and [worker/env.go](../worker/env.go).

Shared repositories, where a repository is fetched once and the runs'
branches live, are per project (`sharedRepo`): Legacy's stay in `.repos`,
where they were before projects, so runs from then and re-runs of them find
their branches; every other project's are in `.projects/<id>/repos`. Two
projects may reach the same URL with different tokens, and a step of one
gets nothing another's steps fetched, nor its runs' branches. A workspace is
bound to its project by its first step, kept in its `.meta`; a request for
it naming another project is refused. Workspaces, sessions and runs' homes
are named after runs, unique across projects, so their paths name none.

For a step of a project not inheriting the instance's configuration the
worker withholds its pi config (`models.json`, `settings.json`), its `AWS_*`
variables, those `CHILD_ENV` names, `CLAUDE_SMALL_MODEL`, its own
providers' say in Claude Code's choice of Bedrock, and its git config:
neither the step's git nor the worker's git for it reads the worker's global
or system config (`stepEnv`, `workerEnv`), in diffs too. A request saying
nothing, as an old webapp's does, is Legacy's and inherits; one naming a
project but saying nothing gets none of it.

## Settings and proxies

Model providers, model keys, the default models, MCP servers and people's
connections to them, connectors, skills and commands, credentials, allowed
internet hosts, the git token, limits, API keys, webhooks and the audit log
are each a project's, empty in a new one, set by its owners (skills, API
keys and webhooks by those who may change what they are for), names unique
within it.

Each proxy grant names its project (`proxy_grants.project_id`): its step's
run's, or for an assistant's reply its flow's
([webapp/grants.go](../webapp/grants.go)). Every proxy reaches only that
project's keys, providers, MCP servers, tokens, credentials and hosts,
refusing anything else, and what it does goes in that project's
`llm_usage` and `audit_events`.

- **Models.** A project's providers are those its owners added, and for a
  project inheriting the instance's configuration the worker's own too
  (`llm.go`, `model_providers.go`, `model_keys.go`). The webapp asks the
  worker which coding agents it offers for any project, but takes its models
  only for one inheriting its configuration.
- **MCP servers.** The sign-in window names its project in its address; the
  sign-in under way (`mcp_sign_ins`) keeps the project and who started it,
  and the callback connects the server for them alone, if they may still
  enter the project and the server is still in it. Connecting needs edit
  access, so a viewer connects none.
- **Git.** A project's owners set its own git token
  (`project_git_tokens`, sealed with `SECRET_KEY`, never sent back; `GET`,
  `PUT`, `DELETE /api/git-token`). A step gets, in order, the GitHub App's
  token for repositories on its GitHub, the project's own, else `GIT_TOKEN`,
  the first and last only in a project inheriting the instance's
  configuration (`gitTokens.forWorkspace`); `GET /api/git-access` says which
  for the settings and a flow's repositories.
- **The internet.** A project's owners allow its hosts (`egress_hosts`),
  dialled only at public addresses but in a project inheriting the
  instance's configuration, where a host allowed by name may be at a private
  one.
- **The audit log.** Each event is a project's: its step's grant's, its
  run's, or the project a request changing a setting is worked on in. Its
  owners search its events alone (`GET /api/audit`). What is no project's, a
  project made or deleted, a ceiling set, a call with a token no grant has,
  goes in `instance_events`, which super admins read.

## Limits and ceilings

A project's owners set its limits in `limits`, a row by project and name
(`run_spend`, `user_month_spend`, `user_parallel_steps`, `run_disk_mb`).
Super admins set ceilings by the same names in `instance_limits`. An owner
may set no limit above a ceiling (`overCeilings`), and what the proxy, the
engine and the worker hold a project's runs and people to is the lesser of
its own and the ceiling, 0 being none (`limitsIn`), so a ceiling lowered
holds at once over a limit set before. A person's monthly spending and the
steps their runs have going at once count in each project apart
(`llm_usage`, `person_slots` by project); a run's spending counts with its
subflows', items' and follow-ups' toward the run at its top, all in its
project.
Every replica reads every project's limits and the ceilings again every
five seconds ([webapp/limits.go](../webapp/limits.go)).

`GET /api/limits` shows anyone in the project its own, the ceilings and what
holds of the two; `PUT /api/limits` is its owners'.

## The setup file, templates and the assistant

`GET` and `POST /api/setup` read and write the request's project alone
(`exportSetup`, `importSetup` take the project): its MCP servers, skills,
commands, structs, procedures and flows, matched by name within it. With
sign-in they are its owners'. The file names no project, so one exported
from a project makes everything over in another, and the same file imported
into two makes two sets apart.

A template is imported into the project it is used in: its flow is named
apart from that project's flows alone, takes that project's connectors, and
what it needs is what that project has (`templates.go`).

A flow's assistant works in the flow's project: what it is told of, the
procedures, structs, MCP servers, skills and credentials, is that project's,
its reply is imported there, and it reaches the models of the project's
providers.

## The frontend

- **Addresses** ([address.ts](../webapp/frontend/src/address.ts)): every
  page inside a project is at `#/p/<id>/…`, and the rest is the address the
  page had before projects, which `App.tsx` reads (`currentPage`).
  `pageHash` builds every link and address the app writes, in the project of
  the page it is on unless told another; `replacePage` puts one in place.
  Each tab keeps its own address, so its own project, and two tabs may show
  two projects. An address naming none, as old bookmarks and links have,
  opens in the project its tab last showed, else the one last used, else the
  person's first (`projectFor`, `rewritten`). The Projects page
  (`#/projects`) is outside any project.
- **Requests** ([api.ts](../webapp/frontend/src/api.ts)): every request to
  `/api/` names the page's project in the header (`nameProjectsBy`); what a
  page saves as it goes names the project it opened in (`inProject`); an
  address the browser opens itself names it as `?project=` (`apiLink`,
  `inProjectLink`). Its `outsideProjects` repeats the server's. A request
  refused as forbidden or not found has the app ask `/api/me` again
  (`onRefusedHere`), as the person may have lost the project.
- **[Projects.tsx](../webapp/frontend/src/Projects.tsx)**: the switcher
  leading every page's breadcrumb (`ProjectSwitcher`), the Projects page for
  super admins (`ProjectsPage`: making, renaming, archiving, bringing back,
  deleting for good), and the page telling someone who may enter no project,
  or not this one, whom to ask (`NoProject`).
- **[Access.tsx](../webapp/frontend/src/Access.tsx)**, the settings' Access
  tab for owners: people given the project by hand, added by email, the
  groups it is given to, what anyone else signed in may do, and the roles its
  people hold. **[GitAccess.tsx](../webapp/frontend/src/GitAccess.tsx)**:
  the project's git token (`#/settings/git`) and `GitAccessNote`, which says
  beside a flow's repositories where its steps' tokens come from.
  `Spending.tsx` shows the project's limits against the ceilings. The
  settings' Instance group holds, for super admins, People, Ceilings and
  Instance events (`Audit.tsx` with `instance`).
- **What a person may do** ([Account.tsx](../webapp/frontend/src/Account.tsx)):
  `levelIn`, `projectHere`, `inheritsInstance`, `configures` (an owner),
  `onlySees` (a viewer), `mayChange`, `managesProjects` and `managesPeople`
  decide what is offered: a viewer sees boards, runs and records read-only,
  API keys, connectors, MCP servers and skills read-only, and no control
  that changes or starts anything.
- **Browser storage** that is a project's is kept under `projectKey`: the
  palette's recent picks, a task board's rail and Flows strip, who new flows
  belong to, the audit log's search and the board clipboard.
- **The Inbox** lists what waits on the person in every project they may
  enter, each item naming its project, and opens it there.

## How the walls are tested

- `TestProjectsEveryTableNamesItsProject` fails for a table without
  `project_id BIGINT NOT NULL`, or with a default, unless it is listed above
  the projects with why. `TestProjectsMigrateEverythingIntoLegacy` migrates
  a database from before projects up and checks Legacy and its members.
- `projects_*_e2e_test.go` test access and levels, boards, people, runs and
  what starts them, and settings and proxies, each in a project of its own.
- **The sweep** (`tests/projects_sweep_e2e_test.go`): two projects get one of
  everything, and every route the webapp serves, read from its sources by
  `tests/routes_test.go`, is called as an owner of the first, as a super
  admin and with nobody signing in, under the first project's header, naming
  the second's things in the path, the query and the body, mixed with the
  first's and in the fields the guard doesn't read (`sweepBody`). A path
  naming the second's thing must be answered 404, 403 or 400; nothing may
  answer with a server error, change a row of the second project, answer
  with anything of the second's, or leave the first naming the second's
  where no key checks it. Then the proxies are called with the first
  project's grants on the second's servers, credentials and providers. A
  route added is swept as it is written; one the sweep can't call is listed
  in `sweepSkipped` with why, and the project and people routes a super
  admin may use across projects in `sweepInstance`.
- The e2e harness names a project on every request, Legacy unless the test's
  `app.project` says another (`projectID`); `usersAdd` gives the person added
  the test's project; and a test fails if its webapp panicked serving a
  request, as `requestProject` does for one naming none, or inserted a row
  without naming its project.

## Adding a table, a route or a query

- **A table**: give it `project_id BIGINT NOT NULL` with no default; `UNIQUE
  (id, project_id)` if other rows refer to it; foreign keys to rows of a
  project naming `project_id` beside the id, and to a person as one of a
  project's through `project_members (user_id, project_id)`; names unique
  within the project. A key that would null its column takes no action, and
  the code nulls it first. A table above the projects goes in the schema
  test's list, with why. Its Down carries the check refusing it while a
  second project exists ([MIGRATIONS.md](MIGRATIONS.md#projects)). Deleting a
  project empties every table with a `project_id` column (`projectTables`),
  so it needs nothing more.
- **An insert** names the project, taken from what the row belongs to (its
  flow, run, process or object, or the request's project for a setting),
  never from JSON a person or a version sent.
- **A query** of a project's rows keeps to the project: `project_id = ?`, or
  `ownedBy`, `editableFlows` and `visibleFlows` for a user with their
  `Project`. Without sign-in those give `TRUE`, so a listing names the
  project itself.
- **A route** inside the projects reads `requestProject`. A new kind of id in
  its path, query or body needs an entry in `flowPaths` or `flowFields` for
  `guardFlows`. A write a viewer may make needs a pattern in `neededAccess`;
  a GET that changes something needs one too. A route about the person, the
  projects or the instance goes in `outsideProjects`, and in `api.ts`'s,
  which must match it. The sweep calls it as soon as it is written; one it
  can't call goes in `sweepSkipped` with why.
- **Work outside a request** (a sweep, a notice, what a schedule, key,
  webhook or board starts) takes the project from what it is for and the
  person as they are there: `userInProject`, `memberAs`, `projectPeople`.
- **Links and browser storage** in the frontend go through `pageHash`,
  `apiLink` and `projectKey`; links the webapp writes through `runAddress`
  and `projectPath`.

## Operating it

- **A new install** has Legacy, empty, inheriting the instance's
  configuration. People in `ADMIN_EMAILS` are super admins and enter it, and
  every project, as owners. Anyone else signing in is in no project, and is
  told to ask the super admins, until one gives them access.
- **Upgrading** from before projects puts everything in Legacy and makes
  everyone there its member, admins as owners and others as editors. People
  signing in for the first time afterwards are in no project; setting
  Legacy's "what anyone signed in may do" to edit, in its Access tab, lets
  them in as before.
- **Making a project** is the Projects page's (`#/projects`), a super admin's.
  It starts empty: its owners add model providers with their keys, MCP
  servers, connectors, credentials, internet hosts and a git token in its
  settings, or import a setup file exported from another project.
- **Giving access** is the project's owners', in its settings' Access tab: a
  person by the email they sign in with, at a level; a group of the identity
  provider's (`OIDC_GROUPS_CLAIM`, [INSTALL.md](INSTALL.md#groups)); or what
  anyone signed in may do. A super admin adding someone may give them a
  project at once.
- **Archiving** (deleting from the Projects page) keeps everything, enterable
  by super admins alone, and starts no runs; runs going on go on. Bringing it
  back undoes it. **Deleting for good**, only once archived and with no run
  going on, empties every table of the project, its audit log included, has
  the worker delete its runs' workspaces and branches, and records it in
  `instance_events`. Legacy can't be deleted for good.
- **The worker goes before or with the webapp.** An old worker ignores
  `project` and `inheritsInstance`: it keeps every project's repositories in
  the same `.repos` and hands every step the instance's pi config, `AWS_*`,
  `CHILD_ENV` variables and git config. A new worker with an old webapp takes
  its requests as Legacy's, which is safe.
- **Migrating up** runs `00049` to `00053`, `00065`, `00070` to `00072`,
  `00080`, `00090` and `00091` as the webapp starts, and with them
  everything in Legacy. **Migrating down** past any of them is refused while
  a project other than Legacy is left, as it would lose which project
  everything was in: delete every other project for good first. Going below
  `00049` drops the projects and everything they kept of people's access.
  See [MIGRATIONS.md](MIGRATIONS.md#projects).

## Limits

- `SANDBOX=process` isolates nothing between projects, as it isolates
  nothing between steps: a step can read every project's workspaces, shared
  repositories and the worker's environment. Run steps in containers or pods
  ([SANDBOX.md](SANDBOX.md)) once projects are anyone's but one team's.
- The worker trusts the webapp's pairing of a run and its project: it keeps
  a workspace to the project of its first step, but takes a request's
  project and `inheritsInstance` as the webapp says them.
- Shared repositories count toward no run's disk limit. Deleting a project
  for good leaves its `.projects/<id>/repos` on the worker's volume, to be
  removed by hand.
- The URLs a project's owners set are dialled from the webapp without
  keeping to public addresses: a model provider's, an MCP server's and a
  connector's at any address, and a credential's at any but link-local,
  multicast and the machine's own, unless its base URL names the machine
  (an http sidecar). So an owner of any project can have the webapp call a
  service on its own network. Only the internet proxy keeps a project not
  inheriting the instance's configuration to public addresses.
- Groups are as stale as [their reading](#groups-from-the-identity-provider)
  lets them be: a group added to someone in none shows only at their next
  sign-in, and what runs on someone's behalf without a session weighs their
  groups as last read.
- Some references have no composite key, as [above](#every-table-names-its-project):
  `runs.parent_run_id`, `run_steps.child_run_id`, `runs.rerun_of`,
  `runs.trial_of`, `shared_records.object_id`, `objects.store_id`,
  `object_attributes.ref_id`, and the run and step ids in `audit_events`,
  `llm_usage`, `proxy_grants`, `person_slots` and `record_changes`. The code
  alone keeps them to the project.
- Access is to a whole project: there is no level per flow beyond who owns
  it within the project.
- A flow never moves between projects; exporting and importing copies it,
  without its runs, versions or webhooks.
- Migrations run in order, each once: goose refuses one numbered below the
  highest applied, so a branch's new migration is numbered above the
  highest on any database it will meet.
