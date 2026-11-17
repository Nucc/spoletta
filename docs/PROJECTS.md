# Projects

A project is a tenant: a standalone space with its own flows, task boards,
procedures and structs, its own model providers and keys, MCP servers,
connectors, skills, credentials, allowed internet hosts, limits, teams, roles,
API keys and audit log. A new project is empty, as the app is when first
started. Nothing in one project reaches anything in another.

This file is the design the work on the `projects` branch follows. Where it
says "transition", the rule holds only until the last phase lands.

## People

Users stay the instance's: one sign-in, one `users` row, no `project_id`.
`users.admin` is the super admin, who makes, renames, archives and deletes
projects, assigns people and groups to them, and manages people.

A project gives access at one of three levels:

| Level | May |
|---|---|
| `owner` | everything, including the project's settings and who has access |
| `editor` | change and run flows, task boards, procedures and structs, as an ordinary user does today |
| `viewer` | see boards, runs and records; change and start nothing |

Access comes from, the highest winning:

- `project_members (project_id, user_id, level, source, removed_at)`: a person
  assigned by hand (`source = manual`), or a row made for someone who came in
  another way (`group`, `open`, `super`), so what they own has a member to
  point at.
- `project_groups (project_id, group_name, level)`: a group from the identity
  provider. `OIDC_GROUPS_CLAIM` names the claim (a string or a list of
  strings, in the ID token or userinfo); each person's groups are kept in
  `user_groups (user_id, group_name)`, replaced at every sign-in, so
  assigning a group takes effect at once. Google sign-in sends none.
- `projects.others_access` (`none` by default, `view`, `edit`): what anyone
  signed in to the instance, assigned or not, may do.
- A super admin enters any project as owner.

Someone entering through a group, `others_access` or as super admin gets a
`project_members` row the first time they enter, with that source. When the
group is unassigned or `others_access` goes back, those rows are soft-removed
(`removed_at`); what they made stays. Removing a member is always soft.

Within a project today's ownership (owners.go) goes on unchanged: a flow is
its maker's, a team's or everyone's (everyone in the project). An owner of the
project is what an admin was: sees and changes everything in it. A viewer
sees what an editor would, read-only.

Teams and roles (`user_roles`) are the project's. Groups from the token are
not teams.

Without sign-in there is nobody, as today: every request may do anything,
including making and switching projects.

## Every table names its project

Every table has `project_id BIGINT NOT NULL` but the ones above tenants:
`projects`, `users`, `user_identities`, `user_groups`, `sessions`,
`project_groups` and `project_members` (which have it as part of what they
are), `instance_events`, `replicas`, `replica_commands`,
`goose_db_version`. `audit_events` and `llm_usage` have it too, not null;
what happens above projects (a project made, a super admin changed) is
audited in `instance_events`.

The column alone proves nothing, so the database checks it:

- Each parent table has `UNIQUE (id, project_id)`, and each child's foreign
  key to it is composite, `(flow_id, project_id) REFERENCES flows (id,
  project_id)`, so a row can't be in another project than its parent. The
  same for links across flows: a subflow process and its procedure, a copy
  and the struct it follows, a reference attribute and its struct, a task
  board column and its struct and flow.
- A row naming a user (an owner, a team member, a role, an API key, an MCP
  connection) refers to `project_members (user_id, project_id)`, so it can't
  name someone outside the project. (User 0, whom nobody is, is the
  exception where it is used today; such columns keep no foreign key.)
- Names unique in the instance today are unique in the project: teams, MCP
  servers, skills, credentials (name and variable), model providers, model
  keys, the default model, limits, internet hosts.
- `webapp/schema_test.go` (or an e2e test on the migrated database) asserts
  every table has `project_id NOT NULL` or is on the list above, so a new
  table can't forget it.

A flow never moves between projects; export and import carry it over.
Versions and history keep boards as JSON, which no key checks: restoring a
snapshot writes rows with the flow's project whatever the JSON says.

### Transition

The first migrations add the column as `NOT NULL DEFAULT 1`, 1 being the
Legacy project, so inserts that don't yet name the project keep working while
Legacy is the only one, and fail on the composite keys, loudly, in any other.
Each later phase makes its inserts name the project. The last migration drops
the defaults.

## The Legacy project

The migration makes project 1, `Legacy`, with `inherits_instance` set, and
everything there is belongs to it. Every user not removed becomes a member:
admins as owners (and they stay super admins), others as editors.

What the instance was configured with outside the database applies only to
projects with `inherits_instance`, which is Legacy alone: the worker's own
`models.json` providers and the keys in its environment, `LLM_UPSTREAMS`,
`GIT_TOKEN`, `GITHUB_APP_*`. Another project has only what its owners set in
its settings, including a git token of its own.

Down migrations put the old keys back and drop the columns, and say in a
comment that they lose which project things were in; going down is refused
(the Down fails) while a second project exists.

## Requests

The page's address carries the project, `#/p/<id>/…`, and every `/api/`
request names it in the `X-Spoletta-Project` header, so each tab keeps its
own project. A middleware before `guardFlows` works out the person's level
there (403 without one, 404 for no such project), and puts the project and
the request's user in the context: `u.Admin` then means owner of this
project, so the checks there are keep their meaning, and `u.Super` is the
instance's admin. A viewer's requests that change or start anything are
refused in one place, by method and path, with the few exceptions listed
there (starring a flow, deciding a review their role decides).

`guardFlows` refuses, with 404, whatever the request names (a flow, version,
run, process, object, struct, task board) that is in another project.

Transition: a request without the header is Legacy's.

Outside `/api/`: `/v1` takes the project from the API key, `/hooks/{token}`
from the webhook's flow, the proxies (`/llm`, `/mcp`, `/credentials`, the
internet proxy) from the step's grant in `proxy_grants`.

Routes:

- `GET /api/me` gains `super` and `projects`: those the person may enter,
  each `{id, name, level}`.
- `GET /api/projects` (the same list; a super admin sees all, archived too),
  `POST /api/projects` `{name}` (super admin), `PATCH /api/projects/{id}`
  `{name, othersAccess}` (owner), `DELETE /api/projects/{id}` (super admin;
  archives it, and for good only once archived and asked again with
  `?forever=true`; Legacy can be archived like any).
- `GET` and `PUT /api/projects/{id}/members` and `/groups` (owner).
- Changes to projects and who has access are audited in the project's log,
  and making, archiving and deleting one in `instance_events`.

## Runs

A run is its flow's project's. Everything it reaches is that project's: the
models and keys the proxy uses, MCP servers, connectors, credentials, skills,
allowed hosts, spending limits (a project's owners set theirs; the super
admin sets ceilings no project may exceed), and what it writes (`llm_usage`,
`audit_events`, `run_logs`). A subflow, a struct's copy, a reference, a pick
and a task board column name only things of the same project.

The worker keeps projects apart on disk: the shared repository where runs'
branches are kept is per project (`sharedRepo(project, url)`), since two
projects may reach the same URL with different tokens, and a workspace is
told its project (`workspaceSpec.Project`). Nothing a step of one project
fetched is readable by a step of another.

Nothing about projects lives only in a replica's memory; caches keyed by
what is now per project (the price book's providers, allowed hosts) are keyed
by project.

## The setup file, templates, the assistant

`GET` and `POST /api/setup` read and write the request's project alone: its
MCP servers, skills, commands, structs, procedures and flows, matched by name
within it. The format doesn't change and names no project, so a file from one
imports into another. Templates and the assistant import into the project
they are used in.

## The frontend

- The project in the address and the header (`api.ts`), tabs keeping theirs.
- A switcher in the breadcrumb before Home, listing `me.projects`.
- A Projects page for super admins (`#/projects`): make, rename, archive,
  delete, and each project's people and groups.
- The settings show the project's tabs, with an Access tab for owners
  (members, groups, what others may do), and an Instance group for super
  admins (People, Projects, ceilings).
- A viewer sees no control that changes or starts anything.
- The Inbox lists what waits on the person in every project they are in, each
  with its project; opening one goes to its project.

## Phases

1. Schema, Legacy, `projects.go`, the request's project and level,
   `guardFlows`, flows and task boards listed and made in the request's
   project. The worker's per-project repositories (worker/ alone).
2. In parallel, each making its inserts name the project and its reads keep
   to it: the engine, runs and records; boards, versions, history, paste, the
   setup file, templates and the assistant; settings and proxies (model
   providers and keys, MCP, connectors, skills, credentials, internet hosts,
   limits, spending, audit, API keys, webhooks, git tokens,
   `inherits_instance`); sign-in groups, teams, roles, inbox and notices; the
   frontend.
3. Drop the defaults, require the header, e2e tests proving one project can't
   reach another's by any route, AGENTS.md and the docs.
