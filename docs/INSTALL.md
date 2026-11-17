# Installing Spoletta

Spoletta runs as three containers: MySQL, the webapp, which serves the app
and its API and walks the runs, and the worker, which runs agents and
scripts for it. [compose.yaml](../compose.yaml) runs all three, and its
comments describe every setting this page names.

## Trying it

With [podman](https://podman.io):

```sh
podman compose up --build
```

then open http://localhost:8080 and start a flow from a template.

That is enough to try Spoletta on your own machine, but not to share it.
Nobody signs in, steps run as the worker's own processes, and the webapp is
published only on this machine's loopback. Read on before others reach it.

## Models

The worker uses the models in your pi config (`~/.pi/agent/models.json` and
`settings.json`, or the directory `PI_CONFIG_DIR` names), and only those two
files: the rest of the directory holds sessions and OAuth tokens, which an
agent could read. Restart the worker after changing them.

Agents never see a model key. The webapp holds the keys and reaches the
providers for them, so the keys go to the webapp: `OPENAI_API_KEY`,
`ANTHROPIC_API_KEY`, `GEMINI_API_KEY` and `OPENROUTER_API_KEY` are passed
through from `.env` (a models.json naming other variables needs them added
to compose.yaml). A project's owners can also add providers and their keys
in its settings, under Model providers. Bedrock without a key signs in with
the worker's AWS credentials, passed to the worker as `AWS_*`.

The pi config, the keys in the environment, `LLM_UPSTREAMS` and the worker's
AWS credentials are what the instance is configured with, which only the
Legacy project gets (see [Projects](#projects)); every other project has
only the providers its owners add, with their keys saved in the app.

## Claude Code

Agent processes run with pi. They may run with Claude Code instead, as a
second coding agent, when the worker's images are built with it: it isn't
installed unless you set `CLAUDE_CODE_VERSION` in `.env` to an exact version,
such as `2.1.325`, and build again (`podman compose up --build`). The worker
then offers it, and an agent process's panel says what it runs with. Take
the version CI tests, which `.github/workflows/ci.yml` names in its
`claude-code` job: it runs that Claude Code as a step runs it, against a fake
of the model's API, so a release that takes the worker's flags or prints its
output otherwise fails there first.

Set it only if you accept Anthropic's terms for running Claude Code in a
product. Anthropic's legal page says doing so "requires agreeing to our
Commercial Terms", that "The Claude Code binary must not be modified", and
that "Customers may not pay for, resell, or intermediate Claude usage on
their end users' behalf"; it may not use Claude.ai (Free, Pro, Max) sign-ins
on behalf of users. What it allows is an organisation using keys it
provisions for its own authorised users, billed to it. So here Claude Code
signs in only with the step's token, standing in for a key configured for
its project (Anthropic's API key, an Anthropic-API gateway's added under
Model providers, or a Bedrock API key), through the webapp's model proxy
like pi; never with anyone's Claude.ai account. The image installs Anthropic's package as it is, its own
install script putting the platform's binary in place. Whether a hosted
offering serving several customers may include it is for you to settle
with Anthropic.

Claude Code takes only models of providers speaking Anthropic's Messages
API (`anthropic`, or a provider of the API `anthropic-messages`) and
Claude's models on Amazon Bedrock (`amazon-bedrock` with a Bedrock API key,
or a provider of the API `bedrock-converse-stream`, such as a gateway, which
must then also serve Bedrock's InvokeModel, as Claude Code calls it, not
only the Converse API pi calls). On Bedrock it also lists the inference
profiles and calls two models of its own once with a token each as it
starts, which the model proxy records and counts like any call. A process
running with it without a model of its own takes the default model for
Claude Code, which a project's owner picks in Settings, under Model providers, else
the default model, failing when that isn't one Claude Code can use. It has no
web search here, and runs only in steps' containers safely: it runs without
asking permission, the sandbox holding it (see `docs/SANDBOX.md`).

## The .env file

Secrets and settings go in a `.env` file next to `compose.yaml`, which is
never committed. The ones to know:

| Variable | What it does |
|---|---|
| `WORKER_TOKEN` | The token only the webapp asks the worker with. Without one the worker answers anyone reaching it, and runs their bash. Make one with `openssl rand -hex 32`. |
| `SECRET_KEY` | Seals the model keys, MCP tokens, credentials' and webhooks' secrets, projects' git tokens and sign-ins' tokens the webapp keeps in the database. Make one with `openssl rand -base64 32`. |
| `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_NAME` | Signing in with your identity provider over OpenID Connect. |
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | Signing in with Google. |
| `ADMIN_EMAILS`, `OIDC_ALLOWED_DOMAINS` | Who may sign in, and who is a super admin, who makes projects and enters every one as its owner. |
| `OIDC_GROUPS_CLAIM`, `GROUPS_MAX_AGE` | The claim naming the groups people are in at your identity provider, which projects are given to, and how long groups read hold, `24h` by default, where they can't be read again. See [Groups](#groups). |
| `PUBLIC_URL` | Where people reach the app, for sign-in's redirect and the links in messages. |
| `PUBLISH_HOST` | `0.0.0.0` to reach the webapp from other machines, once people sign in. |
| `GITHUB_APP_ID`, `GITHUB_APP_INSTALLATION_ID`, `GITHUB_APP_PRIVATE_KEY` | A GitHub App whose tokens the Legacy project's steps clone and push the flows' repositories with. |
| `GIT_TOKEN`, `GIT_USERNAME` | Otherwise, one token for every Legacy step's git. |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_FROM`, `SMTP_USERNAME`, `SMTP_PASSWORD` | Emailing people when a review waits on them, and when their runs fail. |
| `RESUME_RUNS` | `false` ends the runs of a webapp that stopped, rather than having another carry them on. |
| `TRASH_KEEP` | How long the trash keeps a deleted flow, such as `30d`, the default. |
| `LOCAL_MIGRATIONS` | A directory of migrations of your own, run after Spoletta's in place of those built into `webapp/local_migrations/`; see [MIGRATIONS.md](MIGRATIONS.md#local-migrations). |
| `CLAUDE_CODE_VERSION` | Builds the worker's and steps' images with Claude Code, at that exact version, so agent processes may run with it; empty, the default, leaves it out. See [Claude Code](#claude-code). |
| `CHILD_ENV` | Variables of the worker's own to hand every step of the Legacy project, comma-separated, each passed through in compose.yaml too. |
| `MYSQL_ROOT_PASSWORD`, `MYSQL_DATABASE`, `MYSQL_USER`, `MYSQL_PASSWORD` | Compose's MySQL, which the webapp connects to as that user. |
| `MYSQL_PUBLISH` | Where MySQL is published on the host, `127.0.0.1:3306` by default. |

## Signing in

Without `OIDC_ISSUER` or `GOOGLE_CLIENT_ID` nobody signs in, and anyone
reaching the webapp can do anything, running bash on the worker included.
So compose publishes it, and MySQL, only on this machine's loopback.

To have people sign in, register `http://localhost:8080/api/auth/callback`,
or `PUBLIC_URL`'s, as the redirect URI of each client: your identity
provider's, and for Google a web application's OAuth client in the Google
Cloud console. `OIDC_NAME` names your provider on the page asking which to
sign in with. Only people in `ADMIN_EMAILS`, at a domain in
`OIDC_ALLOWED_DOMAINS`, or added by a super admin may sign in. Google is taken at
its word only for Gmail addresses and for accounts of the Google Workspace
at the email's domain, since anyone can make a Google account with any
email. Those in `ADMIN_EMAILS` are super admins; super admins add, remove
and promote people in the app's settings, under People.

Once people sign in, set `PUBLISH_HOST=0.0.0.0` to reach the webapp from
elsewhere.

### Groups

A project's owners may give it to a group of your identity provider's, so
whoever is in it enters the project at the group's level. `OIDC_GROUPS_CLAIM`
names the claim saying which groups someone is in, such as `groups`: a
string, one group, or a list of strings, in the ID token or, where the ID
token leaves it out, in what the provider's userinfo endpoint says. Map the
groups into that claim at your provider. Each sign-in replaces the person's
groups with those it carries; without the variable, or signing in with
Google, they are in none.

Someone taken out of a group at the provider keeps what it gave them until
their groups are read again. While their session lives, the webapp reads
them again every hour (or every half of `GROUPS_MAX_AGE`, if that is
shorter), with the refresh token the provider gave the session, or its
access token while that lasts, both kept sealed with `SECRET_KEY`; a
session the provider refuses to refresh ends. To get a refresh token the
sign-in asks for the `offline_access` scope, but only when
`OIDC_GROUPS_CLAIM` is set and the provider's discovery document lists
`offline_access` among its `scopes_supported`; let the client use refresh
tokens at your provider, or one listing the scope may refuse the sign-in.
Where groups can't be read again, as the provider gave no refresh token, a
session of someone in any group ends once their groups are older than
`GROUPS_MAX_AGE` (`24h` unless set, a duration such as `8h` or `90m`), so
they sign in again; someone in no group stays signed in as long as before,
and isn't read again, so a group they are put in counts from their next
sign-in. What runs on someone's behalf without a session, an API key's runs, a
webhook's, a schedule's or a task board's, keeps their groups as last read
until they are read again.

## Projects

Everything in the app is in a project: flows, task boards, settings, people's
access, the audit log. Each project is a space of its own, which nothing in
another reaches; [PROJECTS.md](PROJECTS.md) says how.

- **A new install** has one project, Legacy, empty. Super admins enter every
  project as its owner, and make more on the Projects page (`#/projects`).
  Anyone else who signs in is in no project, and is told to ask the super
  admins, until a project's owner gives them access, in its settings under
  Access, by their email, by a [group](#groups) they are in, or by letting
  anyone signed in see or change it.
- **Upgrading** from a version before projects puts everything there is in
  Legacy, and makes everyone there was its member, admins as owners and
  others as editors. People signing in for the first time afterwards are in
  no project: set Legacy's access for anyone signed in to edit, under
  Access, to let them in as before.
- **Legacy alone** gets what the instance is configured with outside the
  app: the worker's pi config and the model keys in the environment,
  `LLM_UPSTREAMS`, the GitHub App and `GIT_TOKEN`, the worker's `AWS_*` and
  `CHILD_ENV` variables and its git config. Another project has only what
  its owners set in its settings, a git token of its own included.
- **Deploy the worker before the webapp, or with it.** A worker from before
  projects ignores which project a step is of: it would keep every
  project's repositories together and hand every step what only Legacy's
  may have. A new worker with an old webapp takes every step as Legacy's.
- **Migrating down** past the projects' migrations is refused while a
  project other than Legacy is left; see [MIGRATIONS.md](MIGRATIONS.md#projects).

## Sealing secrets

Set `SECRET_KEY` before keeping any secret in the app, and keep it where
you keep the database's backups: the webapp doesn't start without the key
its secrets were sealed with. The key can come from a mounted file instead,
named by `SECRET_KEY_FILE` in compose.yaml's webapp. [SECRETS.md](SECRETS.md)
says how secrets are sealed and how to rotate the key.

## Repositories

Each step working on a flow's repositories gets a token for them, handed to
its git and nothing else. The better way is a GitHub App installed on the
repositories, with contents read and write: each step then gets one of its
installation tokens, for the run's repositories only, lasting an hour. Set
its ID, its installation's ID and its private key, or name a mounted file
holding the key in `GITHUB_APP_PRIVATE_KEY_FILE`; GitHub Enterprise takes
`GITHUB_API_URL` too, each added to compose.yaml's webapp. Otherwise
`GIT_TOKEN`, such as a GitHub token with contents read, is handed to every
step as it is, which isn't short-lived; `GIT_USERNAME` defaults to
`x-access-token`, which GitHub takes.

Both are the instance's, which only the Legacy project's steps get. Each
project's owners may set a git token of its own in its settings, under Git
access, sealed with `SECRET_KEY`: Legacy's steps take it for repositories
the GitHub App doesn't reach, before `GIT_TOKEN`, and another project's
steps take it or nothing, so without one they clone only public
repositories.

## Telling people

A review waiting on someone, a reminder, or a run that failed for whoever
asked to know is sent by email over SMTP to `SMTP_HOST`, on `SMTP_PORT`
(587 by default), from `SMTP_FROM`, such as
`Spoletta <spoletta@example.com>`, signing in as `SMTP_USERNAME` with
`SMTP_PASSWORD` (or a mounted file named by `SMTP_PASSWORD_FILE`, added to
compose.yaml's webapp), with
STARTTLS whenever the server offers it. With the Slack connector installed
in the run's project, people are told by a direct message from its app too.
Each person picks how in their settings. Only people who may enter the
run's project are told. The messages link to `PUBLIC_URL`, which should be
set. Without sign-in nobody is told anything.

## Running steps in a sandbox

compose.yaml runs each step as one of the worker's own processes
(`SANDBOX=process`), which sees everything the worker sees. That is for
development only. To give each step a container of its own, which sees only
its own directories and reaches only the webapp, run compose.sandbox.yaml
with it:

```sh
podman compose -f compose.yaml -f compose.sandbox.yaml up --build
```

It needs the container engine's API socket; Docker, Kubernetes, the
internet hosts steps may reach and the threat model are in
[SANDBOX.md](SANDBOX.md).

## The database

Compose's webapp connects to compose's MySQL as the `DB_*` variables say. To
connect elsewhere, copy [config/database.yaml.example](../config/database.yaml.example)
to `config/database.yaml`, which isn't committed, and change it there, or
name a file anywhere with `DB_CONFIG`; `APP_ENV` picks its environment
(`development`, `staging` or `production`). Its values can read the
environment and files, read again on every connection, so a password
rotated in its file is used without a restart.

The webapp migrates its database as it starts. [MIGRATIONS.md](MIGRATIONS.md)
says how to migrate down before running an older version.

## Running it for a team

- [PROJECTS.md](PROJECTS.md): projects, each a space of its own, who enters
  them, and what keeps them apart.
- [SANDBOX.md](SANDBOX.md): a container or pod per step, and the internet
  hosts steps may reach.
- [REPLICAS.md](REPLICAS.md): any number of webapps on one database, such as
  a Kubernetes Deployment's pods behind one Service, and what happens to the
  runs of one that stops.
- [SECRETS.md](SECRETS.md): sealing and rotating secrets.
- [MIGRATIONS.md](MIGRATIONS.md): the schema, and going back a version.
