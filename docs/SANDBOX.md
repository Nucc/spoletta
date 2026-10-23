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
7. [Limits and the audit log](#limits-and-the-audit-log)
8. [Adding a sandbox](#adding-a-sandbox)

## The threat model

A step's program (pi and the tools it runs, or a script's bash) is assumed
hostile. It may read anything it can reach, run anything, and leave traps in
whatever it can write. It must not be able to:

- read a model provider's key, an MCP server's token, the instance's git
  token or any other secret;
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

`SANDBOX` picks where a step's program runs (`worker/sandbox.go`):

| `SANDBOX` | Each step runs | Sees | Network |
|---|---|---|---|
| `process` (default) | as a process of the worker's | what the worker sees | the worker's |
| `podman` | in a container, through podman's libpod API | its own directories | only the webapp |
| `docker` | in a container, through the Docker Engine API (26+) | its own directories | only the webapp |
| `kubernetes` | in a pod, through the cluster's API | its own directories | as network policies allow |

`process` isolates nothing between steps. It is for development, tests, and a
laptop without containers in containers. Everything in the previous section
still holds, so even then no step sees a secret.

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
database or neighbours. Every connection goes in the run's log.

So under a sandbox, a script that calls an API or installs packages needs its
host allowed. pi's own requests go to the webapp directly. Bedrock without a
key, which signs in with the worker's AWS credentials, isn't supported in a
container; give it a Bedrock API key in the settings instead.

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
a step is handed for its models, MCP servers, the internet and git, when
the model and internet ones end, each internet connection, allowed or
refused, calls the proxy refused, runs stopped for spending what they may,
and the limits, internet hosts and model keys admins changed: who, which
run and step, and what, never a token. What each model call took and cost
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
