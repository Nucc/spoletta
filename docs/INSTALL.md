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
to compose.yaml). Admins can also add providers and their keys in the app's
settings, under Model providers. Bedrock without a key signs in with the
worker's AWS credentials, passed to the worker as `AWS_*`.

## The .env file

Secrets and settings go in a `.env` file next to `compose.yaml`, which is
never committed. The ones to know:

| Variable | What it does |
|---|---|
| `WORKER_TOKEN` | The token only the webapp asks the worker with. Without one the worker answers anyone reaching it, and runs their bash. Make one with `openssl rand -hex 32`. |
| `SECRET_KEY` | Seals the model keys, MCP tokens, credentials' secrets and webhooks' secrets the webapp keeps in the database. Make one with `openssl rand -base64 32`. |
| `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_NAME` | Signing in with your identity provider over OpenID Connect. |
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | Signing in with Google. |
| `ADMIN_EMAILS`, `OIDC_ALLOWED_DOMAINS` | Who may sign in, and who is an admin. |
| `PUBLIC_URL` | Where people reach the app, for sign-in's redirect and the links in messages. |
| `PUBLISH_HOST` | `0.0.0.0` to reach the webapp from other machines, once people sign in. |
| `GITHUB_APP_ID`, `GITHUB_APP_INSTALLATION_ID`, `GITHUB_APP_PRIVATE_KEY` | A GitHub App whose tokens steps clone and push the flows' repositories with. |
| `GIT_TOKEN`, `GIT_USERNAME` | Otherwise, one token for every step's git. |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_FROM`, `SMTP_USERNAME`, `SMTP_PASSWORD` | Emailing people when a review waits on them, and when their runs fail. |
| `RESUME_RUNS` | `false` ends the runs of a webapp that stopped, rather than having another carry them on. |
| `TRASH_KEEP` | How long the trash keeps a deleted flow, such as `30d`, the default. |
| `CHILD_ENV` | Variables of the worker's own to hand every step, comma-separated, each passed through in compose.yaml too. |
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
`OIDC_ALLOWED_DOMAINS`, or added by an admin may sign in. Google is taken at
its word only for Gmail addresses and for accounts of the Google Workspace
at the email's domain, since anyone can make a Google account with any
email. Admins add, remove and promote people in the app's settings.

Once people sign in, set `PUBLISH_HOST=0.0.0.0` to reach the webapp from
elsewhere.

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

## Telling people

A review waiting on someone, a reminder, or a run that failed for whoever
asked to know is sent by email over SMTP to `SMTP_HOST`, on `SMTP_PORT`
(587 by default), from `SMTP_FROM`, such as
`Spoletta <spoletta@example.com>`, signing in as `SMTP_USERNAME` with
`SMTP_PASSWORD` (or a mounted file named by `SMTP_PASSWORD_FILE`, added to
compose.yaml's webapp), with
STARTTLS whenever the server offers it. With the Slack connector installed,
people are told by a direct message from its app too. Each person picks how
in their settings. The messages link to `PUBLIC_URL`, which should be set.
Without sign-in nobody is told anything.

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

- [SANDBOX.md](SANDBOX.md): a container or pod per step, and the internet
  hosts steps may reach.
- [REPLICAS.md](REPLICAS.md): any number of webapps on one database, such as
  a Kubernetes Deployment's pods behind one Service, and what happens to the
  runs of one that stops.
- [SECRETS.md](SECRETS.md): sealing and rotating secrets.
- [MIGRATIONS.md](MIGRATIONS.md): the schema, and going back a version.
