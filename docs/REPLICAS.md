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
replica), webhooks' deliveries (each taken once by the run its id or
payload starts, as the API's requests are by their `Idempotency-Key`, and
the GitHub payloads they ignored), the tokens steps are handed for the proxies (only their SHA-256,
[webapp/grants.go](../webapp/grants.go)), what runs and people spent, the
steps each person has going, MCP sign-ins under way, and the limits, which
each replica reads again every five seconds. So is who has a board open,
each browser tab saying so every few seconds, and what changed boards
lately, which tells the other tabs to load them again
([webapp/presence.go](../webapp/presence.go)); both go by the database's
clock.

What is going on isn't: a run is walked by the replica that started it, or
took it over, which holds its steps' connections to the worker, its reviews
waiting for a person, and the assistant's replies and follow-ups it
started. Each is recorded with that replica's id. A request reaching
another replica, such as stopping a step or deciding a review, is recorded
there and passed to the owner through `replica_commands`, which each
replica reads four times a second. A person's decision of a review is kept
with its step until the run's walk has taken it in, so it isn't lost if the
owner stops before it hears of it.

Telling people what waits on them is claimed too
([webapp/notify.go](../webapp/notify.go)): each message is a row in
`notifications` for its step or run, person and kind, which only one
replica adds, with the time it was sent. A message claimed by a replica
that stopped before sending it is sent by another. A review's reminders and
time limit are kept with its step, by the database's clock; whichever
replica finds one due first claims it: a round of reminders by counting it
and moving the next one on, a time limit by failing the step as a decision
would, which the replica walking the run then handles. Every replica
empties the trash of the flows it kept longer than `TRASH_KEEP`, each
deleted by whichever replica takes its row first; and moving a flow to the
trash takes its row as starting a run does, so no run starts on it unseen
and left going.

## When a replica stops

Each replica says it is alive in `replicas` every sixth of
`REPLICA_TIMEOUT` (30s by default). The runs one walked when it stopped are
taken over by a replica alive, and carry on
([webapp/adopt.go](../webapp/adopt.go)):

- Each run is claimed once, by whichever replica gets to it first, with the
  runs of its flow processes and subflows going inside it. A single webapp
  restarting takes over its own runs as it starts, since it starts as a
  replica of its own, with an id of its own.
- The replica walks the run's version again, taking up the steps it stored
  as it comes to them, so every branch, item and board inside it goes on
  from where it was.
- Reviews and loops' questions still waiting wait again, the same steps:
  the inbox, links to them, reminders and time limits go on as they were,
  and nobody is told again. One decided while no replica walked the run is
  taken in as decided.
- An agent, script or connector step that was going is lost, as the worker
  ends it once the replica that asked for it is gone. It fails, saying so,
  and goes again on what it took, as a try of its own, if its retries let
  it; otherwise its failure is handled as any step's. A connector step lost
  may have done what it does already, such as posting a message, which
  going again does twice.
- The run's log says which replica took it over and what it lost, tried
  again or failed, and its panel shows it moved. It is never interrupted
  for it.

What can't carry over still ends: the assistant's replies and step
follow-ups under way fail, the people's slots the replica held are given
back, its steps' tokens end and what other replicas asked of it is dropped.
Trials end as interrupted, as whoever tried the process can try it again,
and so do runs from before versions were recorded. With `RESUME_RUNS=false`
every run a replica walked ends as interrupted when it stops, its open
steps failing, as a single webapp's restart used to; give every replica the
same setting.

- **Told to stop** (SIGTERM, as Kubernetes does a pod it replaces), it
  leaves at once, and another replica, or the next to start, takes over
  what it ran within a few seconds.
- **Killed or lost**, what it ran is taken over once it hasn't said it is
  alive for `REPLICA_TIMEOUT`, by the database's clock.

A replica that finds another took it for stopped, as after a pause longer
than the timeout, exits at once rather than go on with what was taken over,
and Kubernetes starts it again. Until it notices, what it records of those
runs is refused, so what the replica that took them over does wins. Give
every replica the same `REPLICA_TIMEOUT`.

Starting a replica never takes over or ends what the others are running,
so a rolling update leaves runs going, and the runs of the pods it replaces
move to the pods alive.

## Settings that are each replica's own

- `PARALLEL_STEPS` is how many agent, script and connector steps
  one replica runs at once; with three replicas, three times as many run in
  all. A person's limit on steps going at once counts across replicas.
- `PROXY_URL` is where the worker reaches the proxies. Point it at the
  Service, not a pod: any replica answers for any step.
- The rate each address may send to webhooks' URLs (`/hooks/`), and how
  often requests not signed with a webhook's secret are audited, are held
  by each replica for itself; with three replicas, an address gets three
  times as many through. Each webhook's count of them is in the database.

## Upgrading to a version with replicas

Runs started before this version have no owner, and are taken as left by
a replica that stopped. Deploy it with `strategy: Recreate`, or scaled to
one first, so the first replica of the new version doesn't take over, or
end, runs an old one is still walking. A version with migrations is deployed that way
anyway; see [MIGRATIONS.md](MIGRATIONS.md).
