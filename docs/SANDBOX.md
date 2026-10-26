# Sandboxes: how steps are kept apart

Agents and scripts run code nobody reviewed: what a model decides to run,
what a flow's author wrote, what a repository's build scripts do. This is how
Spoletta keeps that code from the instance's secrets and from other runs.

1. [The threat model](#the-threat-model)
2. [What every step gets, whatever the sandbox](#what-every-step-gets-whatever-the-sandbox)
3. [The sandboxes](#the-sandboxes)
4. [Podman and Docker](#podman-and-docker)
5. [Kubernetes](#kubernetes)
6. [Internet access](#internet-access)
7. [Credentials](#credentials)
8. [Limits and the audit log](#limits-and-the-audit-log)
9. [Adding a sandbox](#adding-a-sandbox)

## The threat model

A step's program (pi and the tools it runs, or a script's bash) is assumed
hostile. It may read anything it can reach, run anything, and leave traps in
whatever it can write. It must not be able to:

- read a model provider's key, an MCP server's token, a credential's
  secret, the instance's git token or any other secret;
- read or change another run's workspace, files, session or home, or another
  step's private directory;
- reach the internet other than the hosts an admin allowed;
- make the trusted side (the worker, the webapp) run code of its choosing.

The worker is a trusted launcher. It gets a step's workspace and directories
ready, starts the step's program in a sandbox, and takes back what it made.
The webapp holds every secret and hands out short-lived tokens instead.

## What every step gets, whatever the sandbox

- **An environment from a list**, never the worker's own (`worker/env.go`):
  `PATH`, `HOME`, the locale, pi's settings, proxies and certificates, who
  commits. `CHILD_ENV` names more, for an instance that wants scripts to have
  a token of its own, knowing agents can read it too.
- **No model keys.** pi reaches every provider through the webapp's model
  proxy (`webapp/llm.go`) with a token of the step's, which the proxy swaps
  for the real key. The token ends with the step.
- **No MCP tokens.** MCP servers are reached through the webapp's MCP proxy
  (`webapp/mcp.go`) with a token of the step's, the same way.
- **No credentials' secrets.** A service of the step's own, such as Jira, is
  reached through the webapp's credential proxy (`webapp/credentials.go`)
  at `$CREDENTIAL_<NAME>_URL` with `$CREDENTIALS_TOKEN`, which the proxy
  swaps for the credential's secret; see [Credentials](#credentials).
- **A git token of its own**, for the run's repositories only and lasting an
  hour when it comes from a GitHub App (`webapp/git_tokens.go`), in a file of
  the step's private directory that git finds through `GIT_CONFIG_*`.
- **A pi agent directory of its own**, so pi's credentials and caches of one
  step are never another's.
- **Private files only it reads**: its prompt, spec, skills and tokens are
  0600 in a 0700 directory, deleted when it ends.
- **Its run's home**: `HOME` is the run's own directory, beside the
  workspaces and outside every checkout, so tools and caches a step installs
  carry over to the run's next steps. Releasing the run's workspaces keeps
  it; deleting them deletes it.
- **A worker that only takes the webapp's requests**: with `WORKER_TOKEN`
  set in both, nothing else reaching the worker's port can run anything.

### Checkouts the worker never runs git in

A run's checkout is a clone of its branch, which the step writes: its git
config, hooks and attributes are the step's. Git runs what they say, such as
`core.fsmonitor`, a filter or a hook, for whoever runs git there, so the
worker never does. What a step left uncommitted is committed inside a
sandbox, offline, seeing only the checkout. The worker then brings the branch
back by *fetching* from the checkout into the shared repository, which runs
none of the checkout's config or hooks, after checking the checkout and its
`.git` are real directories and dropping any borrowed objects
(`worker/workspace.go`). What the worker knows of a workspace, its branch and
repositories, is kept in `.meta`, which no step reaches.

## The sandboxes

`SANDBOX` picks where a step's program runs (`worker/sandbox.go`). It has
no default: a worker without it set doesn't start, naming the choices, so
running without isolation is something someone chose, never something a
deploy forgot. The choices:

| `SANDBOX` | Each step runs | Sees | Network |
|---|---|---|---|
| `process` | as a process of the worker's | what the worker sees | the worker's |
| `podman` | in a container, through podman's libpod API | its own directories | only the webapp |
| `docker` | in a container, through the Docker Engine API (26+) | its own directories | only the webapp |
| `kubernetes` | in a pod, through the cluster's API | its own directories | as network policies allow, checked as the worker starts |

`process` isolates nothing between steps. A step runs as the worker's user,
so it can read the worker's environment through `/proc`, with whatever
secrets it holds, such as `WORKER_TOKEN` or `GIT_TOKEN`, and the private
directories, and so the tokens, of every other step running at the same
time, and it reaches whatever the worker reaches. What the previous section
lists keeps secrets out of a step's own environment and files, but not out
of its reach. `process` is for development and tests only, and for a laptop
without containers in containers; the worker warns as it starts with it.

In the others, a step's container sees only the directories the worker gives
it, each a part of one of the worker's volumes, mounted at the same path:

| Directory | Mounted | What for |
|---|---|---|
| its workspace, `/data/workspaces/run-<id>` | read-write | its checkouts |
| its private directory, `/data/steps/private-*` | read-only | prompt, spec, skills, tokens |
| its agent directory and result, inside that | read-write | pi's state, submit_output's result |
| its files, `/data/files/run-<id>/step-<id>` | read-write | what it generates |
| its session, `/data/workspaces/.sessions/<workspace>` | read-write | pi's conversation |
| its run's home, `/data/workspaces/.homes/run-<id>` | read-write | `HOME` |
| `/tmp` | tmpfs | scratch |

The worker finds which volume holds which directory by inspecting its own
container or pod; it never mounts a path of its host. Engines differ in how
they mount a part of a volume, and one that mounts the whole volume instead
would show a step every run's directories. So as it starts, the worker runs a
probe: a container given a directory must find what is in it and nothing
around it. If not, the worker doesn't start. It never falls back to running a
step itself, and it removes containers and step directories a stopped worker
left behind.

Every step's container runs as `SANDBOX_USER` (1000:1000) with a read-only
root, every capability dropped, no new privileges, limits on memory, CPUs and
processes (`SANDBOX_MEMORY_MB`, `SANDBOX_CPUS`, `SANDBOX_PIDS`) and the
default seccomp profile. `SANDBOX_RUNTIME` picks another OCI runtime or
runtime class, such as gVisor's `runsc`, for a kernel of its own. The image,
`worker/sandbox.Containerfile`, holds pi, its extensions, bash, git, curl and
jq, and nothing of the worker's.

## Podman and Docker

Run the stack with the override:

```sh
# podman, rootless
podman compose -f compose.yaml -f compose.sandbox.yaml up --build
# Docker 26 or later
SANDBOX=docker CONTAINER_SOCKET_HOST=/var/run/docker.sock \
  docker compose -f compose.yaml -f compose.sandbox.yaml up --build
```

It builds the sandbox image, mounts the engine's socket into the worker, and
puts the webapp on `spoletta-sandbox`, an internal network that reaches
nothing outside. Steps' containers join it and reach only the webapp, by the
name `webapp`. Committing a step's leftovers gets no network at all.

The worker is root in its container, to use the socket and hand what it
makes to the steps' user. Whoever has the socket controls every container of
its user, so give the worker the socket of a user that runs nothing else: a
rootless podman's or rootless Docker's, never a rootful daemon's. On a Mac,
the podman machine's socket is inside the machine; `podman info --format
'{{.Host.RemoteSocket.Path}}'` says where, and `CONTAINER_SOCKET_HOST` takes
it.

podman is spoken to in its own API because its Docker-compatible one mounts
the whole volume where a part is asked for. The probe catches that, so
`SANDBOX=docker` against podman refuses to start.

## Kubernetes

With `SANDBOX=kubernetes` each step runs in a pod beside the worker's, made
with the worker's service account (`worker/sandbox_kubernetes.go`). The pod
runs non-root with a read-only root, no capabilities, no privilege escalation,
the runtime's default seccomp profile, memory and CPU limits, a
memory-backed `/tmp`, no service account token and no service links. Each
directory is a `subPath` of one of the worker's persistent volume claims.
Claims that only one node mounts (ReadWriteOnce) keep steps' pods on the
worker's node; with ReadWriteMany claims, set `SANDBOX_SAME_NODE=false`. Kubernetes
has no per-pod process limit; set the kubelet's `podPidsLimit`.

The worker runs as the steps' user, so what it makes is theirs. It needs:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata: {name: spoletta-worker, namespace: spoletta}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: spoletta-worker, namespace: spoletta}
rules:
  - apiGroups: [""]
    resources: [pods]
    verbs: [create, get, list, delete]
  - apiGroups: [""]
    resources: [pods/log]
    verbs: [get]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: spoletta-worker, namespace: spoletta}
subjects: [{kind: ServiceAccount, name: spoletta-worker, namespace: spoletta}]
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: spoletta-worker}
```

and, in its pod, its claims at the worker's directories and its own name:

```yaml
spec:
  serviceAccountName: spoletta-worker
  securityContext: {runAsUser: 1000, runAsGroup: 1000, runAsNonRoot: true}
  containers:
    - name: worker
      image: registry.example.com/spoletta-worker
      env:
        - {name: SANDBOX, value: kubernetes}
        - {name: SANDBOX_IMAGE, value: registry.example.com/spoletta-sandbox}
        - {name: STEPS_DIR, value: /data/steps}
        - {name: POD_NAME, valueFrom: {fieldRef: {fieldPath: metadata.name}}}
        - {name: WORKER_TOKEN, valueFrom: {secretKeyRef: {name: spoletta, key: worker-token}}}
      volumeMounts:
        - {name: data, mountPath: /data/files, subPath: files}
        - {name: data, mountPath: /data/workspaces, subPath: workspaces}
        - {name: data, mountPath: /data/steps, subPath: steps}
  volumes:
    - {name: data, persistentVolumeClaim: {claimName: spoletta-data}}
```

The webapp mounts the same files, read-only. The network is the cluster's to
fence. Steps' pods are labelled `spoletta.sandbox/network`: `webapp` for those
that may reach the webapp, `offline` for those that may reach nothing. With
a CNI that enforces network policies:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: spoletta-steps, namespace: spoletta}
spec:
  podSelector: {matchLabels: {spoletta.sandbox/network: webapp}}
  policyTypes: [Ingress, Egress]
  egress:
    - to: [{podSelector: {matchLabels: {app: spoletta-webapp}}}]
      ports: [{port: 8080}]
    - to: [{namespaceSelector: {}, podSelector: {matchLabels: {k8s-app: kube-dns}}}]
      ports: [{port: 53, protocol: UDP}, {port: 53, protocol: TCP}]
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: spoletta-steps-offline, namespace: spoletta}
spec:
  podSelector: {matchLabels: {spoletta.sandbox/network: offline}}
  policyTypes: [Ingress, Egress]
```

The webapp's `PROXY_URL` must be its service's address, such as
`http://spoletta-webapp.spoletta:8080`, which steps reach through those
policies.

Nothing in a pod's spec holds it to these policies. Without them, or with a
CNI that doesn't enforce them, a step's pod reaches whatever any pod
reaches: the cloud's metadata service at `169.254.169.254`, the cluster's
API, other pods, the internet. So as it starts, the worker runs a second
probe: a pod of each label, `offline` and `webapp`, tries to connect to the
cluster's API and to `1.1.1.1:443`, and if either gets through to either,
the worker doesn't start. Both labels must be fenced from them: an offline
pod from everything, and a webapp pod held to the webapp and the cluster's
DNS, so a cluster with only one of the two policies is refused too. A
connection that fails could mean a policy refused it or that
nothing answers there, so the probe tries addresses that answer every pod
of a cluster without policies: every pod reaches the API through the
`kubernetes` service, and the public address answers whoever reaches the
internet. The metadata service isn't tried, as it doesn't answer on every
cluster. The worker doesn't know the webapp's address, which the webapp
gives steps, so it doesn't try that either. A cluster that fences off the
API some other way and has no internet passes without proving its
policies. Where the operator knows the policies hold and the probe fails
for some other reason, `SANDBOX_NETWORK_PROBE=off` skips it, which the
worker warns of as it starts.

## Internet access

A step's container reaches the internet only through the webapp's egress
proxy (`webapp/egress.go`), which its `HTTPS_PROXY` and `HTTP_PROXY` name with
a token of the step's as the proxy's credentials, revoked with its open
tunnels as the step ends. The proxy reaches:

- the hosts an admin allowed in the settings, under Internet access: a name,
  `*.example.com` for its subdomains, with `:port` for a port other than 80
  and 443;
- the step's own repositories' hosts, which its git pushes to.

It dials only public addresses, unless an admin allowed the host by name, so
neither an allowed name nor a repository URL can lead to the webapp's
database or neighbours. An IPv6 address carrying an IPv4 one (NAT64's
`64:ff9b::/96` and `64:ff9b:1::/48`, the IPv4-compatible `::/96`, 6to4's
`2002::/16`) is judged as the IPv4 address it carries, so the metadata
service at `169.254.169.254` is out of reach written either way, while a
public address behind NAT64 is dialled; Teredo's `2001::/32` is never
dialled. Every connection goes in the run's log.

So under a sandbox, a script that calls an API or installs packages needs its
host allowed. pi's own requests go to the webapp directly. Bedrock without a
key, which signs in with the worker's AWS credentials, isn't supported in a
container; give it a Bedrock API key in the settings instead.

## Credentials

A script or agent calling a service of its own, such as Jira or an internal
API, uses a credential an admin keeps in the settings, under Credentials:
the service's base URL, how its secret goes on each request (a header whose
value a template makes, basic auth's password, or a query parameter), and
the secret, sealed with `SECRET_KEY` and never sent back to the browser,
the worker or the step. Each is everyone's, or only given teams'; a flow
uses those its owner may, checked as each step starts and as each call is
made (`webapp/credentials.go`). Which ones a step gets is what its run's
version picked, never the board as it is, so a draft can't hand a run, or
its follow-ups, a credential the version it runs on didn't pick.

A step whose process picks credentials gets, for each, `$CREDENTIAL_<NAME>_URL`,
the webapp's proxy at `/credentials/<id>`, and one token for them all,
`$CREDENTIALS_TOKEN`, kept hashed in `proxy_grants` so it works at whichever
replica a call reaches. It ends with the step, or a while after its timeout
should the webapp lose the step. The proxy takes the token as a bearer token
or as basic auth's password, drops it, and puts the secret where the
credential says before passing the call on, streaming the answer back.

What a step can do with a credential, then, is call its service, as the
service lets the secret do, at paths under the base URL, while it runs. It
can't:

- **read the secret.** Neither the worker nor the step ever has it, and it
  is blanked out of what the service answers, headers and body, in each
  form it went on the request in and those a service could echo it in, in
  any case of their letters: as it is, URL-encoded either way (`%2f` as
  `%2F`), as the header's whole value, as basic auth's `user:secret` in
  base64, in a JSON string with HTML's characters and slashes escaped or
  not (`\u003c`, `\/`), HTML-escaped (`&amp;`, `&quot;`, `&#39;`), and in
  base64, either alphabet, alone or within something longer; a secret with
  characters outside ASCII also as ISO-8859-1 writes it and with them
  escaped in JSON (`\u00e4`). Any eight of
  its characters in a row, in any of those forms but the header's and
  basic auth's, whose own text could be anybody's, are blanked out too, so
  a service echoing what it got cut short, wherever a step pads the path to
  have it cut, hands back at most seven at either end of the cut. A query
  parameter credential's parameter goes first in the query, so the step's
  own parameters can't move the cut. The proxy asks the service for its
  answer uncompressed, reads one gzipped once uncompressed all the same,
  refuses (502) one encoded any other way or more than once, however many
  `Content-Encoding` values say it, and doesn't pass on the step's `Range`,
  so a secret can't come back cut in pieces or compressed out of sight. Nor
  can it come back in a charset whose bytes its forms aren't: the step's
  `Accept-Charset`, and the `charset` an `Accept` asks for, don't go on
  (ASP.NET answers in UTF-16 when asked, Spring in what `Accept` names),
  and an answer whose `Content-Type` says a charset but UTF-8, US-ASCII or
  ISO-8859-1, or that starts with UTF-16's or UTF-32's byte order mark,
  gzipped or not, is refused (502). A service handing the secret back in
  another form, such as hashed, re-encoded or split across fields, hands
  it over; give credentials only for services that don't.
- **hand its token to someone else.** What is kept of the step, what a
  script prints (stdout and stderr, as shown in its terminal window, a
  token printed a piece at a time included, as `set -x` does), an agent's
  conversation, the worker's log lines, in the run's log and the worker's
  own, and the step's result, has the step's tokens blanked out, as they
  are, in base64 within something longer, and as basic auth's `user:token`
  (as `curl -v` prints `Proxy-Authorization: Basic …`), so nobody who sees
  the flow can call its services with them while it runs. Its model, MCP,
  internet and credential tokens reach nothing once the step ends, wherever
  it printed or left them. Its git token doesn't end with it: a GitHub
  App's installation token lasts its hour, and `GIT_TOKEN` until it is
  changed, so a step leaving it in a file or a commit others read hands
  over the run's repositories for that long.
- **reach other paths or hosts with it.** The path is joined to the base
  URL's; one stepping out of it (`..`, `.`, `//`), carrying an encoded slash
  or backslash (`%2F`, `%5C`), a `%` at all once decoded, which a service
  decoding again would read as one, or a query or fragment of its making in
  the path, is refused. So is what some servers read as a step up: a `;`
  anywhere (Tomcat, Jetty and Spring take `..;` for `..`), a segment of
  only dots and spaces (IIS trims a segment's trailing ones, so `.. ` is
  `..`), or of only those before a `:` (Windows reads `..:x` and
  `..::$INDEX_ALLOCATION` as `..`), and any character outside ASCII
  (normalised, full-width `．．` is `..`); `:` is kept otherwise, as APIs
  such as Google's use it. The step's query goes on as it sent it, but for
  a query parameter credential's own, which the secret replaces, first.
- **send the call elsewhere, or as another method.** Only the headers a
  client sends to say what it sends and wants go on (`Accept`, without a
  `charset`, `Accept-Language`, `Cache-Control`,
  `Content-Disposition`, `Content-Encoding`, `Content-Language`,
  `Content-Type`, `Idempotency-Key`, `If-Match`, `If-Modified-Since`,
  `If-None-Match`, `If-Unmodified-Since`, `Pragma`, `Prefer`,
  `User-Agent`), and those the credential names, such as an API's version
  header, but for any the request's `Connection` names. A credential can't
  name its own header, its token's, the connection's, `Range`,
  `Accept-Charset`, or the headers some servers and gateways take as
  another URL, host, method or client (`X-Original-URL`, `X-Original-URI`,
  `X-Original-*`, `X-Rewrite-URL`, `X-Host`, `X-Forwarded-*`, `Forwarded`,
  `X-HTTP-Method-Override` and the like), with `_` read as `-`, as nginx
  and CGI read it (`X_Forwarded_Host`).
- **be redirected with it.** The proxy never follows a redirect. One within
  the base URL comes back as the proxy's URL, so following it goes through
  the proxy again; one elsewhere comes back without the secret's query
  parameter, and with no `Location` at all if the secret is still in it.
- **keep a session got with it, or have a browser act on the webapp.**
  Only the answer's headers saying what it is come back: `Content-Type`,
  `Content-Disposition`, `Content-Language`, `Cache-Control`, `Expires`,
  `Pragma`, `Age`, `Date`, `Vary`, `ETag`, `Last-Modified`, `Allow`,
  `Accept-Patch`, `Retry-After`, `RateLimit*` and `X-RateLimit-*`,
  `Deprecation`, `Sunset`, `X-Request-Id`, `Request-Id`,
  `X-Correlation-Id`, and pages' counts (`X-Total`, `X-Total-Count`,
  `X-Total-Pages`, `X-Page`, `X-Per-Page`, `X-Next-Page`, `X-Prev-Page`),
  never the credential's own header, nor one holding the secret or a piece
  of it. `Location` and each URL of `Link` come back as a redirect's do,
  the proxy's within the base URL, so the next page is fetched through it
  too. `Set-Cookie`, `WWW-Authenticate`, `Clear-Site-Data`, `Refresh`,
  `Service-Worker-Allowed`, `Access-Control-*`, `Alt-Svc`, digests and the
  rest stay with the proxy, as do the step's `Cookie` and forwarding
  headers. What comes back is sandboxed (`Content-Security-Policy:
  sandbox`, `X-Content-Type-Options: nosniff`), so an answer opened in a
  browser can't act as the app.
- **reach the webapp's neighbours through it.** The base URL is an admin's,
  https, or http only at the webapp's own machine (a sidecar), where the
  secret crosses no network. The proxy never dials a link-local or
  multicast address, such as the cloud's metadata service, whatever the
  service's name resolves to, nor the machine's own unless the base URL
  names it, and the host needn't be allowed for internet access, as the
  webapp, not the step, makes the call.
- **use a credential its process didn't pick**, or one its flow's owner may
  no longer use: taking a credential from a team, or removing it, refuses
  the next call of every step using it.

Nor can an admin's edit send a kept secret elsewhere: changing a
credential's base URL, even only its path, as a path at an origin many
share may be another's service (`script.google.com/macros/s/<id>`), or
where its secret goes (the way, the header, the parameter), needs the
secret again.

A request may send at most 64 MB, and a gzipped answer come to at most
256 MB uncompressed; a call may take ten minutes, two of them waiting for
the service to start answering. An answer past its most, or one that
can't be read to its end, is cut off with the connection broken, so the
step never takes what came of it for the whole answer. Each call goes in the run's log
and the audit log by its method, credential, path without its query, and
status, never with the secret or the token.

What this leaves:

- A call already going when its token ends, or its credential is taken
  away, goes on until it ends, ten minutes at most.
- When the webapp itself reaches the internet through a proxy of its own
  (`HTTPS_PROXY`), the address checked before dialling is that proxy's, not
  the service's.
- Every call, and every refused one, is a line in the audit log, so a step
  calling in a loop, or anyone sending tokens at the proxy, makes many.
- A service echoing what it got cut short hands back up to seven of the
  secret's characters at the cut; one echoing a window of it cut at both
  ends, seven a call wherever the step moves the window, so a step calling
  over and over could piece together a secret such a service echoes.
- How soon the proxy passes on the end of a piece of the answer says
  whether it held it back as what could be the start of the secret, which
  a step timing the answers could tell.
- `ETag` comes back as the service made it, before the body is read, so
  for a body that held the secret it is a hash a step could test guesses
  of a short secret against.
- Paths with a `;`, a character outside ASCII or an encoded slash are
  refused, so an API needing one, such as GitLab's `projects/group%2Fname`,
  is called by its numeric ids instead.
- The step's `Range` doesn't go on, so a download comes whole.
- A service answering only in brotli or zstd, never uncompressed, gets a
  502; so does one answering in a charset but UTF-8, US-ASCII or
  ISO-8859-1, even one writing ASCII as UTF-8 does, such as
  windows-1252, and a download starting with a byte order mark, such as a
  UTF-16 text file. One answering in UTF-16 without saying so, neither in
  its `Content-Type` nor with a byte order mark, isn't caught, as telling
  that from any other bytes would refuse downloads.
- An answer's headers wait for the first byte of its body, which is read
  as far as it could still be a byte order mark: for an event stream, its
  first event.

## Limits and the audit log

Isolation keeps a step from what isn't its own; limits keep a run that
goes wrong, or a person, from using up what is everyone's. Admins set them
in the settings, under Spending & limits (`webapp/limits.go`), each off
until set:

- **What a run may spend on its models.** Every call goes through the
  model proxy, which prices it as it is recorded, cache reads and writes
  included, at the price of the worker's models.json or pi's catalog, and
  a model without a known price at the largest models' (`webapp/spend.go`).
  A run's subflows, list items and follow-ups count toward it. Once it has
  spent its limit, the proxy refuses its calls, saying it is out of budget,
  which pi doesn't try again, and the engine stops the run, which fails
  saying what it spent. A flow may hold its runs to less in its settings.
  Calls already going when a run passes its limit finish, so a run with
  steps running at once spends a little more than its limit.
- **What a person may spend a month**, UTC's, on their runs, follow-ups and
  flows' assistants; past it their calls are refused until the next month.
- **How many steps a person's runs may have going at once**, at every
  replica of the webapp, of their `PARALLEL_STEPS`, so one person's runs
  can't take every slot.
  A step waiting for its person's turn holds none of the slots.
- **How much disk a run may take**: its checkouts, sessions, home and
  files, measured after each step by the blocks they take, each file once
  however many links it has (`worker/disk.go`). A step that leaves its run
  over it fails, its work committed as any step's. Engines can't hold part
  of a volume to a size, so a step can still fill the volume while it runs;
  give the workspaces' volume room, or a quota of its own.

Bedrock without a key, which signs in with the worker's AWS credentials,
doesn't go through the proxy, so no spending limit covers it.

The audit log (`webapp/audit.go`, `#/audit` for admins) records each token
a step is handed for its models, MCP servers, the internet, credentials and
git, when the model, internet and credential ones end, each internet
connection, allowed or refused, each call through the credential proxy,
calls the proxies refused, runs stopped for spending what they may, and the
limits, internet hosts, model keys and credentials admins changed: who,
which run and step, and what, never a token or a secret. What each model call took and cost
is in `llm_usage`, shown in the Spending & limits tab and each run's panel.

## Adding a sandbox

A sandbox implements one method (`worker/sandbox.go`):

```go
run(ctx context.Context, spec sandboxSpec, stdout, stderr io.Writer) error
```

`spec` names the program, the file its stdin comes from, its environment,
directory and home, the worker's directories it may see (each read-only or
not), whether it may tidy up when stopped, whether it is offline, and the
egress proxy. Run it so it sees nothing else. Stop it when `ctx` ends, and
return an `exitError` for a status other than 0. Have `newSandbox` make it
from `SANDBOX`, and call `probeSubpaths` as it starts.
