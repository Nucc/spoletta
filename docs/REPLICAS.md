# Running several replicas of the webapp

Any number of webapps can run on one database, such as the pods of a
Kubernetes Deployment behind one Service. A request can reach any of them:
people's, other systems' through the API, and the worker's steps' calls to
the model, MCP and egress proxies. How it works is described at the top of
[webapp/replicas.go](../webapp/replicas.go) and
[webapp/commands.go](../webapp/commands.go).

## What is shared, and what isn't

Everything the webapp keeps is in the database, so every replica sees it:
runs and their steps, sessions, schedules (each time claimed by one
replica), the tokens steps are handed for the proxies (only their SHA-256,
[webapp/grants.go](../webapp/grants.go)), what runs and people spent, the
steps each person has going, MCP sign-ins under way, and the limits, which
each replica reads again every five seconds.

What is going on isn't: a run is walked by the replica that started it,
which holds its steps' connections to the worker, its reviews waiting for a
person, and the assistant's replies and follow-ups it started. Each is
recorded with that replica's id. A request reaching another replica, such
as stopping a step or deciding a review, is recorded there and passed to
the owner through `replica_commands`, which each replica reads four times
a second.

Telling people what waits on them is claimed too
([webapp/notify.go](../webapp/notify.go)): each message is a row in
`notifications` for its step or run, person and kind, which only one
replica adds, with the time it was sent. A message claimed by a replica
that stopped before sending it is sent by another. A review's reminders and
time limit are kept with its step, by the database's clock; whichever
replica finds one due first claims it: a round of reminders by counting it
and moving the next one on, a time limit by failing the step as a decision
would, which the replica walking the run then handles.

## When a replica stops

Each replica says it is alive in `replicas` every sixth of
`REPLICA_TIMEOUT` (30s by default). One that stops ends what it ran, as a
single webapp's restart always did: its runs end as interrupted, their open
steps and its assistant's replies and follow-ups fail, and its steps'
tokens end.

- **Told to stop** (SIGTERM, as Kubernetes does a pod it replaces), it
  leaves at once, and another replica, or the next to start, ends what it
  ran within a few seconds.
- **Killed or lost**, what it ran is ended once it hasn't said it is alive
  for `REPLICA_TIMEOUT`, by the database's clock.

A replica that finds another took it for stopped, as after a pause longer
than the timeout, exits at once rather than go on with what was ended, and
Kubernetes starts it again. Give every replica the same `REPLICA_TIMEOUT`.

Starting a replica never ends what the others are running, so a rolling
update leaves runs going, except those of the pods it replaces.

## Settings that are each replica's own

- `PARALLEL_STEPS` is how many agent, script and connector steps
  one replica runs at once; with three replicas, three times as many run in
  all. A person's limit on steps going at once counts across replicas.
- `PROXY_URL` is where the worker reaches the proxies. Point it at the
  Service, not a pod: any replica answers for any step.

## Upgrading to a version with replicas

Runs started before this version have no owner, and are taken as left by
a replica that stopped. Deploy it with `strategy: Recreate`, or scaled to
one first, so the first replica of the new version doesn't end runs an old
one is still walking. A version with migrations is deployed that way
anyway; see [MIGRATIONS.md](MIGRATIONS.md).
