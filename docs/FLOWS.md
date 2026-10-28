# Authoring Spoletta flows

This guide is for an agent that builds Spoletta flows for a user: it turns
"I want X to happen" into a flow that Spoletta can run. It explains what Spoletta
is, the parts of OPM (Object-Process Methodology) Spoletta uses, how a run
actually executes, what each step sees and must hand back, and how to write a
flow as a setup file. Worked examples come after that, and the guide ends
with the rules the app enforces and a step-by-step procedure.

Read it in order the first time. When you write a flow, keep sections 4
(running), 5 (step contracts) and 9 (rules) open beside you.

The field-by-field reference for the setup file is
[webapp/SETUP.md](../webapp/SETUP.md). This guide shows how to use the fields
and why; SETUP.md says exactly what each one is.

---

## 1. Spoletta in one page

Spoletta is a visual builder and engine for agent workflows. A user draws a
**flow** on a **board**: **processes** (things that happen) and **objects**
(things that exist), joined by **links**. Pressing Run, or a schedule firing,
starts a **run**. The server walks the board, and each process that runs is
recorded as a **step**.

A process is carried out by one of these:

| Kind | Carried out by | Typical use |
|---|---|---|
| `agent` (the default) | an AI coding agent (pi) in the worker, optionally in git checkouts of the flow's repositories and with MCP servers | write, analyse, fix code, decide, review |
| `script` | bash in the worker, with `curl` and `jq` | fetch from an API, run a build, transform JSON |
| `human` | a person in the Spoletta UI | approve or send back work, fill in what a run starts from |
| `connector` | Spoletta itself, carrying out an operation of a connector built into it (GitHub, Slack, Telegram, Google Sheets and Docs, Home Assistant, or any HTTP API), with the token an admin set | list issues, post to Slack, append a spreadsheet row, turn a light on |
| `flow` | its own board of processes, zoomed into | group steps, run steps once per item of a list |
| `subflow` | another flow (a procedure), run as a run of its own | reuse a flow across jobs |

Objects carry what the steps produce. Each object holds JSON. Its
**attributes** give that JSON a known shape, and its **states** (such as
`approved` / `changes requested`) drive which process runs next.

There are three kinds of flow:

- a **job flow**, which actually runs (fix exceptions, write release notes);
- a **procedure** (or module), a flow other flows reuse through subflow
  processes;
- a **struct**, a single reusable object definition (states, attributes,
  parts) for other flows to use.

Every board can also be read as **OPL**, plain sentences generated from the
diagram (section 6). Agents can be given their flow's OPL in their prompt.

---

## 2. OPM in brief, and how Spoletta maps it

OPM (Object-Process Methodology, ISO 19450) models any system with two kinds
of thing:

- **Objects** exist, or can exist. An object can be in one of several
  **states**.
- **Processes** transform objects. A process creates, consumes or changes
  objects, or changes their state.

The things are joined by **links**. OPM has two families of link, and Spoletta
uses both.

### 2.1 Procedural links: what a process does to an object

| OPM link | Meaning in OPM | In Spoletta | Setup file |
|---|---|---|---|
| Result | the process creates the object | output link: the process **yields** (generates) the object | `yields: [X]` |
| Result to a state | it creates the object in a given state | output link naming a state | `yields: [{object: X, state: s}]` |
| Consumption | the process uses the object up | input link with role `consumption`: once taken, it is gone | `takes: [{object: X, role: consumption}]` |
| Effect | the process changes the object | the process both takes and yields the same object | `takes: [X]` and `yields: [X]` |
| Instrument | the process needs the object but doesn't change it | input link with role `instrument` | `takes: [{object: X, role: instrument}]` |
| Agent | a human carries out the process | a person object (`human: true`) taken by a human process | `takes: [Editor]` on a `kind: human` process |
| Event | the object entering a state triggers the process | input link naming a state | `takes: [{object: X, state: s}]` |
| Condition | the process runs only if the object is in the state; otherwise it is skipped | input link naming a state, marked as a condition | `takes: [{object: X, state: s, condition: true}]` |
| Exception | a process handles another's failure | failure connection between processes | `onFailure: [Handler]` |

Spoletta adds two connections between processes that OPM doesn't draw as
links. One is the **start**, the board's single initial node, which points at
the process a run begins with. The other is a plain **next** connection (the
target runs right after the source, whatever objects say).

### 2.2 Structural links: what an object is made of

| OPM link | Meaning | In Spoletta |
|---|---|---|
| Exhibition (characterization) | the object exhibits an attribute, such as a ticket's `title` | `attributes:` with plain values. An attribute that is itself an object (an address with street and city) is a part with `relation: exhibition` (the default) |
| Aggregation (consists of) | the object is made of parts, such as a car's wheels | `parts:` with `relation: aggregation` |
| Generalization (is a) | a car is a vehicle and has everything a vehicle has | `specializes: Vehicle` |

### 2.3 In-zooming

OPM manages complexity by **zooming into** a process: inside it is a diagram
of sub-processes. In Spoletta that is a `flow` process with a `board` of its own.
The objects linked to the flow process from outside are its interface:
zoomed in, the ones it takes show at the inner board's start and the ones it
yields at its end. They are the **same objects**, so the processes inside
link to them directly. Nothing is declared twice.

### 2.4 OPL

OPL, OPM's Object-Process Language, says the diagram in sentences, one fact
per sentence. Spoletta writes the OPL of every board. People read it beside the
board. An agent process with `describeFlow: true` gets it in its prompt so it
knows the flow it works in. Section 6 lists every sentence Spoletta writes; use
it to check that a flow says what you meant.

### 2.5 The app's words and OPM's

The app says what a link or a kind of flow does first and OPM's term after,
as "uses (instrument)". OPL, the API and the setup file keep OPM's words:
the setup file's roles are still `instrument` and `consumption`.

| The app says | OPM's term | Meaning | Setup file |
|---|---|---|---|
| takes | (a plain input) | the process takes the object and leaves it for others | `takes: [X]` |
| uses (instrument) | instrument | needs the object and doesn't change it | `role: instrument` |
| uses up (consumption) | consumption | once taken, the object is gone | `role: consumption` |
| makes | result | the process generates the object | `yields: [X]` |
| changes (effect) | effect | the process takes the object and generates it again | `takes: [X]` and `yields: [X]` |
| handles | agent | a person does a human process | a person object taken by a `kind: human` process |
| when it enters (event) | event | the object entering the state starts the process | `takes: [{object: X, state: s}]` |
| only if in it (condition) | condition | the process runs only if the object is in the state, else it is skipped | `condition: true` |
| procedure (reused flow) | (Spoletta's) | a flow other flows run, through a subflow process | `procedures:` |
| struct (reused object) | (Spoletta's) | one object flows use copies of | `structs:` |
| sentences (OPL) | OPL | the board in sentences | (none) |

---

## 3. The building blocks

### 3.1 Objects

An object is a named thing processes generate and take. What it holds is
always JSON:

- With **no attributes and no parts**, it holds one value under `data`: the
  agent hands in `{"data": "…any JSON…"}`. Most text artefacts (a draft, a
  summary, a patch description) are objects like this.
- With **attributes**, it holds a JSON object with exactly those keys, in
  order: first the fields of the object it specializes, then its own
  attributes, then its parts. Give each attribute a `definition` saying what
  it holds; the agent sees it as the contract.
- A **list** (`many: true`) holds a JSON array of such items. `min` and `max`
  bound how many it holds (OPM's multiplicity): `min: 1` is "at least 1",
  `min: 2, max: 5` is "between 2 and 5". A generated or entered list must
  have that many items.

Other object flags:

- `persistent: true` stores what each run generated of it, one instance per
  run. Mark an attribute `unique: true` and no two stored instances may share
  its value. That is how a flow tells it already handled something, such as
  a Sentry issue id. A step that generates a value another run already
  stored is a **duplicate**: its run ends as *terminated*, not failed, and no
  further process starts. Only runs that end *done* keep their values, so
  something whose run failed is picked up again next time.
- For a persistent **list**, each item is claimed by its own run when it
  starts. An item another run already holds is skipped, so fetching the same
  issues every morning processes only the new ones.
- A persistent object (not a list) on the top board that only the processes
  taking it yield, an **effect**, is **carried over**: each run of the flow
  starts with what the last run to end *done* stored of it. That is how a
  flow keeps a cursor, such as when it last fetched or the offset it got to:
  `takes: [Cursor]` + `yields: [Cursor]`. A script finds it in
  `$INPUTS/Cursor` and prints the new value. On the first run there is
  nothing yet and no file; a failed run doesn't move it on. A process waiting
  only for it runs after the one moving it on, on the new value; one that
  also waits for something ready sooner may get the old one. Leave its
  attributes not unique, or a run leaving it as it was is a duplicate. Only
  a whole run of the flow carries it: not a subflow process running the
  flow, nor items of a list.
- `file: true` on an attribute means it holds the path of a file the step
  wrote. A script writes the file in `$OUTPUTS`; an agent writes it in its
  step's files directory. A person can't enter a file by hand.
- `human: true` makes it a **person**, such as `Editor` or `Requester`. A
  person is only ever taken by a human process (OPM's agent link: "Editor
  handles Review"). A person is never an instrument, never consumed, and
  never part of another object.
- `end: true`, on an object of the flow's top board, says it is one of what
  the flow leaves. When the flow is a procedure, those are its outcomes.

### 3.2 States and rules

A state is one situation an object can be in: `draft`, `approved`,
`changes requested`, `bug`, `feature`. States do the routing in Spoletta. A
link naming a state either generates the object in it, or waits for the
object to enter it.

A state can have a **rule** saying when an object is in it, such as
`confidence >= 0.8 and kind == "bug"`. The rule syntax:

- comparisons joined by `and`, all of which must hold;
- the operators are `==`, `!=`, `<`, `<=`, `>`, `>=` and `contains` (text in
  text, or an item in a list);
- the left side is a field path, with dots into parts (`author.email`). An
  object without attributes holds its value under `data`, so its rule reads
  `data contains "urgent"`;
- the right side is a JSON value, and anything that isn't JSON is a string.

When a script, an agent, a connector step or a person generates an
object **in no state**, it comes out in the **first** of its states, in the
order they are listed, whose rule holds. If none holds, it comes out in no
state. Rules are how a script or a connector step (which can't decide
states) still routes work, and how an agent's numeric judgement turns into a branch.

Order states from most to least specific. A last state without a rule is not
a catch-all, because a state without a rule never matches. Give a catch-all
state a rule that always holds for that object instead.

### 3.3 Processes

Every process has a `name` and a `kind` (`agent` when absent). The settings
by kind:

**agent**
- `prompt`: what to do, in plain words. The engine appends the inputs, the
  output contract, any change request, and optionally the flow's OPL
  (section 5), so the prompt should say *what* to do and *how well*, not
  repeat the plumbing.
- `model`: `"provider/id"`, empty for the default model the settings
  keep, or pi's without one. `thinking`: one of
  `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, empty for the
  default.
- `mcpServers`: names of MCP servers configured in the app that this agent
  may use; none by default. An installed connector, such as GitHub's, is one
  of them too, its operations being its tools.
- `mcpTools`: of each of its `mcpServers`, by name, the tools it keeps to,
  such as `{github: [list_issues, add_comment]}`. A server it names none for
  gives it all its tools, but a connector only those that change nothing.
  Pick write tools only for an agent meant to change things.
- `skills`: names of skills kept in the app's settings that this agent is
  given: pi lists each by its description, and the agent reads its
  instructions once its task calls for them. The flow may use only the
  skills of its owner (theirs, their teams' and everyone's) or, for a team's
  flow, the team's and everyone's.
- `credentials`: names of credentials an admin keeps in the settings that
  this agent calls services with, through the webapp's proxy, never seeing
  their secrets (section 5.3). The flow may use those given everyone, or a
  team of its owner's, or for a team's flow, the team.
- In a prompt, `/name` at the start of a line or after a space stands for
  the command of that name kept in the settings, spelled out as the step
  runs. A command taking arguments (`$1`, `$@`, `${1:-default}`) takes the
  rest of its line; others leave it be. Any other `/word`, such as a path
  or a command the flow may not use, stays as it is.
- `repositories`: which of the flow's repositories it works in. All of them
  when absent; none when `[]`.
- `describeFlow: true`: give it the flow's OPL.
- `rounds`: for an agent that **reviews** (decides the state of something it
  takes), how many times it may run in a run before sending work back fails
  it. Default 3, 1 to 20.

**script**
- `script`: a bash script. Inputs are files in `$INPUTS`. What it prints is
  its output, and a non-zero exit fails the step (section 5.3).
- `repositories` and `credentials`, as for an agent.

**human**
- No prompt or script: a person reviews what it takes and decides a state,
  fills in objects, or both (section 5.4). It can take a person object, the
  one who carries it out.

**connector**
- `connector: {server, operation, args}`. `server` names an installed
  connector among the `mcpServers`, `operation` one of its operations, and
  `args` its arguments as text, in which `{{Object}}` and
  `{{Object.attribute}}` stand for what the step takes (section 5.5).
  Without `server`, none is picked yet, as when the connector isn't
  installed: the process keeps its operation and arguments, and the flow
  can't run until someone picks one.
- GitHub's operations that read: `list_issues`, `search_issues`,
  `get_issue`, `list_comments`, `list_pull_requests`, `get_pull_request`,
  `list_pull_request_files`, `get_file`. Those that change GitHub:
  `create_issue`, `update_issue`, `add_comment`, `add_labels`,
  `create_pull_request`, `review_pull_request`, `merge_pull_request`. Each
  takes `owner` and `repo`, and one about an issue or pull request its
  `number`.
- `slack` (a bot token): reads `list_channels`, `list_messages`,
  `list_replies`, `find_user`; changes `post_message`, `update_message`,
  `add_reaction`. A channel is named by its id, such as `C0123456789`, and a
  message by its `ts`.
- `telegram` (a bot's token): reads `get_updates`, the messages people sent
  the bot that it hasn't confirmed, and `get_chat`; changes `send_message`,
  `edit_message`, to a `chat_id`, and `confirm_updates`, after which
  Telegram forgets the messages up to an update.
- `google` (a service account's JSON key; it sees only the files shared with
  its email): reads `find_files`, `get_spreadsheet`, `read_rows` (each row an
  object named by the first row's headings), `read_document`; changes
  `append_rows`, `update_cells` (`rows` as JSON: lists of values, or objects
  by heading), `append_to_document`.
- `homeassistant` (a long-lived access token, at the installation's URL):
  reads `list_states`, `get_state`, `get_history`; changes `call_service`,
  such as `domain: light`, `service: turn_on`, `entity_id: light.kitchen`.
- `http` (any JSON API, installed once for each, at its URL): reads `get`;
  changes `post`, `put`, `patch`, `delete`. Each takes a `path` within the
  API's URL, an optional `query`, and the writes a JSON `body`.

**flow**
- `board:` an inner board with its own `start`, `processes` and `objects`.
- `recording: true`: each time it runs (each item, for a list) is a run of
  its own, which shows separately and whose failure or duplicate only ends
  that branch of the outer run.

**subflow**
- `runs:` the name of a procedure. Its links name the procedure's top-board
  objects by name (section 3.8).

Settings for agent, script and connector processes:
- `timeout` in seconds (1 to 86400): the step fails after it.
- `retries` (0 to 5): how many more times a failed step is tried, each a
  step of its own.
- `loops` (1 to 100, default 5): how many times it may run round a loop with
  no person deciding in between before someone is asked whether it goes on
  (section 4.4).

Settings for human processes, both in seconds (1 to 31536000, a year):
- `remindAfter`: those who may decide it are reminded every this long while
  it waits, until it is decided, stopped, or fails on its `waitAtMost`; never
  at or after that.
- `waitAtMost`: once it has waited this long with nobody deciding, it fails
  as a step past its `timeout` does, so its `onFailure` handler takes over
  (section 4.6).

Settings for any process:
- `quorum`: run once this many of the objects it takes without waiting for
  a state exist and hold their conditions, rather than all of them (at
  least 1).
- `next:` processes that run right after it succeeds.
- `onFailure:` processes that handle its failure. The source must be an
  agent, script or connector process, or a human one with `waitAtMost`.

### 3.4 Links

A link joins a process and an object on the same board. In the setup file a
process lists them under `takes` (input links) and `yields` (output links).
A link that says nothing but the object is written as just the object's name.

| Written as | Means |
|---|---|
| `takes: [Ticket]` | waits until Ticket exists, in any state |
| `takes: [{object: Guide, role: instrument}]` | needs Guide as a reference it must not change |
| `takes: [{object: Token, role: consumption}]` | uses Token up; no later process gets it until it is generated again |
| `takes: [{object: Post, state: weak}]` | runs **whenever** Post enters `weak` (an event) |
| `takes: [{object: Ticket, state: bug, condition: true}]` | runs only if Ticket **is** `bug` once it is otherwise ready; skipped if not |
| `yields: [Patch]` | generates Patch, in no state (or the first state whose rule holds) |
| `yields: [{object: Patch, state: draft}]` | generates Patch in `draft` |
| `yields: [{object: Ticket, states: [bug, feature]}]` | an agent generates Ticket and **decides** which state it is in |
| `takes: [Post]` + `yields: [{object: Post, states: [good, weak]}]` | a **review**: the agent or person keeps Post's content and only decides its state |
| `takes: [Notes]` + `yields: [Notes]` (agent or script) | **effect**: hands in a changed Notes |
| `yields: [Ticket]` on a human process that doesn't take Ticket | a person **fills in** Ticket |
| `takes: [{object: Review, query: {where: 'score >= 4', state: approved, limit: 5}}]` | **looks up** Review's stored records, not waiting for it |

A **query** link (agent or script only) takes what earlier runs stored of a
persistent object rather than the object in this run (for a shared struct's
copy, its records; see 3.8): the records of runs that ended *done*, whose fields hold `where` (a rule, as a state's; all when
left out) and that are in `state` (any when left out), newest first unless
`oldest: true`, at most `limit` (20 by default, 100 at most). They come as
a JSON array, the object's shape as a list, in `$INPUTS/<Name>` or the
agent's prompt; it may be empty. The process doesn't wait for it, nor does
it make the process run: what else it takes, the start or a `next` does.
What this run stored isn't among them. It is the process's only link taking that
object; it names no state and doesn't consume. `limit: 1` is the latest
record, and with `oldest: true` the first.

A process tells its objects apart by name: no two it takes may share one,
nor two it yields, compared as file names in `$INPUTS` (`Jira Ticket` and
`Jira_Ticket` are the same). One it takes and another it yields may, such as
a ticket in and the updated ticket out. Give objects on different boards
that meet at one process different names.

### 3.5 Connections and the start

- `start: {next: P}` on a board names the process a run of it begins with.
  Without one, nothing on the top board runs. **Always give a board a
  start.**
- `next: [Q]` on P runs Q right after P succeeds (or is skipped), in the same
  pass. Use it for ordering without data (P pushes a branch, Q opens the PR).
  Usually prefer data links: if Q needs what P made, link Q to P's output
  instead. A `next` out of a **human** process is not followed when the person
  finishes. Have what follows a person take what they entered or decided.
- `onFailure: [H]` on P runs H instead of failing the run when P fails (after
  its retries). P's branch ends, but the run goes on. H runs in a fresh pass
  and takes everything P took, plus an extra input named `Failure of P`
  holding what went wrong.

### 3.6 Boards and zooming

A `flow` process's `board` is a board of its own. The objects the flow
process takes and yields on the outer board are its interface. The processes
inside link to those same objects. The inner board's start process takes
everything the flow process was given, linked to it or not.

When a flow process **takes a list**, it runs **once per item**, and inside
it that object *is the item*. Whatever the inner processes generate of a
list the flow process yields is an item of it, and the flow process collects
the items into the list outside. With `recording: true` each item runs as a
run of its own, labelled by what tells the item apart. Each item also gets
its own git branch, so items running at once don't meet.

### 3.7 Workspaces

A flow can have `repositories` (`name`, `url`, optional `base` branch) and a
`branchPrefix`. A run's agents and scripts then work in a **workspace**: a
checkout of each repository, all on a branch of the run's own. Every process
of the run works on the same branch and picks up where the one before left
off. After an agent step ends, a person can send it follow-up messages
("push it", "rename that function") in the same session and checkout.

### 3.8 Procedures, subflows and structs

- A **procedure** is a flow under `procedures:`. Other flows run it with a
  `kind: subflow` process whose `runs:` names it. The two flows share no
  objects: an object the subflow process takes is given to the procedure's
  top-board object **of the same name**, in the state of the same name, and
  an object it yields is what the procedure's run left of the object of the
  same name (mark those `end: true` in the procedure). On the caller's board
  the outcomes are **copies** of the procedure's objects. A copy follows its
  source (`copyOf: {flow: <procedure>, object: <name>}`): its states,
  attributes and parts change only in the procedure. A flow can't run
  itself, however many subflows apart.
- A **struct** is a single object under `structs:`. A flow uses it with an
  object `copyOf: {flow: <struct name>}`, which follows the struct as a
  procedure's copies do. Use a struct when several flows exchange the same
  shape, such as a `Ticket` with `key`, `summary` and `priority`.
- A struct's object can be **shared** (`shared: true` on it, which needs a
  `unique` attribute): every copy of it, in whatever flow, reads and writes
  one store of **records**, told apart by the unique attribute, as a table
  the flows have in common. Use it when one flow adds things and another
  works on them, such as one fetching Sentry issues and another fixing them.
  - A process that only **generates** it (one fetching the issues) adds
    the records that are new, in the state its link names, and refreshes
    the fields of those it knows, **keeping their state**. Fetching an issue
    again doesn't undo how far it got. Name the state new records start in
    on the link, `yields: [{object: Exception, state: new}]`, rather than by
    a rule that holds of every record.
  - A process that also **takes** it, by any input link or a query, moves
    the records it generates into the state it comes out in: `yields:
    [{object: Exception, state: completed}]`, a rule, or an agent's decision.
    It writes fields it leaves out as they were.
  - A list writes a record per item, all in the state the list comes out in.
  - What gets worked on, or worked on again, is up to **queries**: `takes:
    [{object: Exception, query: {state: new, limit: 1, oldest: true}}]`
    reads the records in that state, whichever flow wrote them, and a rule
    on their fields narrows them, such as `attempts < 3`. To retry what
    failed, have the failure handler take it and yield it `failed`, and
    query that state too.
  - Records stay once the step writing them is done, whatever its run does
    later. No run claims a record, so two runs querying the same state at
    once may take the same one; don't let the working flow's runs overlap
    (a schedule skips a time while its last run goes on).
  - A shared object's copy isn't `persistent` too. The struct's board lists
    its records, and its Records page (`#/flows/<struct>/records`) shows them
    as a table, a column per field, searched and sorted as a flow's Data page
    is: faceted queries such as `state:new -@severity:low crash`, over
    every record stored.
  - A person can pick the record a flow works on: **Run…** on a record (or
    on those ticked) starts a flow querying the struct on it, a run per
    record, and each of that run's queries of the struct reads the record
    alone, whatever its state and rule select. So a flow implementing "the
    open Jira ticket" its query gives it implements the one picked; write
    its query for the scheduled case, and it serves both.
- Deleting a flow, procedure or struct moves it to the **trash**, from
  where those who may change it restore it, for 30 days by default. While
  it is there it runs nothing and exports nowhere: a subflow process running
  a procedure in the trash fails, saying so, until it is restored or picks
  another, and the copies of a struct's or procedure's objects there are
  their flows' own, as they are once it is deleted for good. A shared
  struct's copies keep its records.

### 3.9 Schedules

`schedules: [{cron: "0 7 * * 1-5", timezone: Europe/London}]` runs the flow
on its own: the version the flow published, or its board as it is while it
publishes none (4.1a). The cron has five fields (or a descriptor such as
`@hourly`), and the timezone is an IANA zone given separately. A time is skipped if the
flow's last run is still going (for example, waiting on a person), and times
missed while the server was down are skipped too. A scheduled flow can't
have a human fill in what it starts from and still run unattended, so start
it with an agent, script or connector process that fetches what it works
on instead.

### 3.10 Tags

`tags: [nightly, billing]` gives a flow, procedure or struct (on its
object) the words the flow list finds it by and narrows to. They change
nothing in how it runs. Each is at most 64 characters, a flow has at most
20, and two differing only in case are one.

### 3.11 Notes

A board's `notes` are boxes of Markdown text placed on it, each optionally
pointing at a process or an object (`process: Fetch` or `object: Ticket`),
for saying what the board doesn't: why the flow is built so, what is
unfinished, what someone should look at. They change nothing in how it
runs: no step sees them, and OPL leaves them out. Write one when a choice
in the flow needs explaining to whoever changes it next, such as why a
review sends work back at most three rounds. A board whose `notes` the
file leaves out keeps the notes it has; `notes: []` leaves it none.

---

## 4. How a run executes

This is the part that decides whether a flow works. The engine's own
description is the header of `webapp/engine.go`; this section says the same
in terms of what to draw.

### 4.1 What a run starts with

**A run from the Run button or a schedule takes no input.** It starts at the
board's start. An object that no process generates exists from the
beginning, but **empty**. So whatever such a flow works on has to come from
inside the flow:

- a **human** process that has a person fill it in ("Which Jira ticket?"),
  the usual first step of a hand-started flow;
- a **connector**, **script** or **agent** process that fetches it, the
  usual first step of a scheduled flow: Sentry's issues, say, through
  Sentry's MCP server or the `http` connector installed at its API;
- an agent that finds it itself (with MCP servers or in the repositories).

A first agent that "takes the request" when nothing produces the request
gets an empty input. This is the most common authoring mistake.

**A run started over the API is given its inputs.** Another system with an
API key POSTs to `/v1/flows/<id>/runs` with `{"inputs": {"<object>": ...}}`,
optionally with `"states": {"<object>": "<state>"}`. The inputs are the
objects on the top board that some process takes and none generates, each
required, holding what a person would enter of it: a JSON object with
exactly its attributes, an array of them for a list, or text for an object
without attributes. They are given as a subflow process gives its objects:
the process the start points at takes them all, and the others taking them
run once they exist. So a flow meant to be called this way has its first
process take the request object, with nothing generating it. Connect to the
end what the caller should get back: `GET /v1/runs/<id>` returns what the run
left of those objects. A flow starting from a human or a fetching process
takes no inputs.

**A person can give them by hand too.** Run's menu on the board has *Start
with inputs…*, which asks for the same objects, each as its text or its
attributes' JSON, with a state if it has states, checked as an API request's
inputs are, and starts the board as it is, or the version published if the
person picks it (`POST /api/runs` with `inputs` and `states`). So a flow
meant for the API can be run from the board while it is built.

**A run started by a webhook is given its inputs from the event.** Services
sending events, GitHub, Slack, a monitoring alert, can't call the API, so a
flow's settings can add **webhooks**: each a URL of its own that the service
sends its events to, signed with the webhook's secret. A GitHub webhook takes
the events it names, alone (`issues`) or with an action (`issues.labeled`);
a Slack one takes the Events API's events, those it names if any; a generic
one takes every delivery. Its **mapping** says where each input is taken
from in the event's payload: a dotted path (`issue.title`,
`pull_request.number`, `labels.0.name`), the whole payload, or, for an
object with attributes, a path for each attribute (`number` from
`issue.number`, `title` from `issue.title`). What it takes is checked as an
API request's inputs are, and the run starts as one over the API does. Each
event starts one run however often the service sends it; one the flow can't
take (a path its payload lacks, an input the mapping doesn't give) is
refused and listed with why among the webhook's deliveries, beside the last
payload, to write the mapping from. So a flow meant to run on an issue being
labeled has its first process take an `Issue` object with the attributes it
needs, nothing generating it, and its webhook maps them from the payload.
Webhooks run as whoever added them, or last changed what starts them, and
aren't part of the setup file.

### 4.1a Which version a run runs

A run runs on a **version** of the flow: a snapshot of every board and of
the flow's settings. It takes its processes, prompts, scripts, settings,
objects, states and attributes from that version, not from the board as it
is while it goes, so editing the board reaches only the runs after.

A flow can **publish** one of its versions (Publish, beside Save; or any
older version from the Versions panel, which rolls back to it). The board
is then its draft, and:

- **Run** on the board, and **re-running** a run from one of its processes,
  run the board as it is (saved as a version first if it changed); a person
  can run the version published by hand from the Versions panel.
- **Trying** one process alone from its panel (a trial, see 10.4) always
  runs the board as it is, the draft, never the version published.
- A **schedule**'s run, a run **over the API**, one a **webhook** starts
  and a run started **on a record** run the **version published**. What the
  API says the flow takes, what a webhook's mapping is checked against, and
  which records its queries select, are that version's too.
- A **subflow** process in a run of a version published runs the version
  its procedure published, if it has one, and its board as it is
  otherwise; in a run of the board, it runs the procedure's board as it is.

A flow that never published a version runs its board as it is everywhere.
So publish a flow that schedules or other systems run before editing it, and
publish its procedures too. Publishing changes nothing on the board, and
bringing the board back to a version leaves what is published as it is.

### 4.2 Readiness

Once a process succeeds, its outputs exist, and the processes taking them
run, **in parallel** where several are ready at once:

- A process whose links name **no state** runs once **every** object it takes
  that way exists.
- A process with an **event link** (a link naming a state, not a condition)
  runs **whenever** the object enters that state, and only then, with
  whichever of its stateless inputs exist. Several event links on one process
  are **alternatives**: any one triggers it. Its stateless inputs never
  trigger it on their own: with even one event link, a process runs only on
  its events, from the start, or from a `next`. So a process that both
  writes something first and revises it later must be the board's start
  (8.2), or be split into a writer and a reviser (8.1).
- A **condition link** counts as an ordinary input for readiness, and once
  the process is ready it is checked. If the object isn't in the named state
  (or one of the states its other condition links to the same object name),
  the process is **skipped**.
- With a **quorum** of *n*, the process runs once *n* of its stateless and
  condition inputs exist and hold their conditions, and is skipped once too
  few still can.

### 4.3 Skipping propagates

A skipped process generates nothing. What it would have generated is
**settled as absent**, so processes waiting for it go on without it, and are
skipped in turn if they would get nothing at all. That makes condition links
the way to build if/else branches that rejoin: the join process runs with
whichever branch's output exists.

### 4.4 Passes, loops and rounds

Every process runs **at most once per pass**, so a cycle of agents ends
instead of spinning. A **decision** (a person or an agent setting a state)
starts a **fresh pass** from that object, so review loops can repeat:

1. Write yields Post.
2. Critic reviews Post: weak.
3. Write takes Post in `weak` and runs again, with Critic's instructions.
4. Critic reviews the new Post, and so on.

For an agent reviewer, `rounds` bounds this. Once it has run that many
times, a decision sending the work back to a process that already ran fails
it instead, which ends that branch (or goes to its `onFailure` handler). A
person can loop as long as they like.

Any other loop, such as an `onFailure` handler fixing what the failed
process takes so it runs again, has a **circuit breaker**. A process about
to run round a loop, with no person deciding since it last did, waits for
someone who may edit the flow to say whether it goes on:

- once it has run `loops` times round it (default 5), or
- at once, when it would take exactly what it took on an earlier round,
  since the loop then gets nowhere.

The question shows on the board as a review does. Going on lets it run
`loops` more times, on the same input too; stopping ends its branch and
fails the run, without its `onFailure` handler. Loops a reviewer's `rounds`
bound, or a person's decisions drive, never ask. A loop meant to go round
many times, such as polling until something is ready, needs a higher
`loops`.

When a reviewer sends work back, the process taking it in that state gets a
**change request**: the reviewed work and the reviewer's instructions, in
place of the object as a plain input (section 5.1). A person's decision
always carries their instructions to the processes taking the object in the
state they chose.

### 4.5 Failure

- An agent, script or connector step fails on an error, a non-zero exit, a reply
  that doesn't match its contract after it was told so, its `timeout`, or a
  stop by a person. A human step fails only past its `waitAtMost`.
- `retries` tries it again, each try a step of its own. A stopped step isn't
  retried.
- A failure **ends its branch**. The rest of the run goes on, but the run
  ends failed, unless an `onFailure` handler takes it, in which case the run
  can still end done.
- A step stopped by a person, or with its run, is not handled.

### 4.6 People

A human step **waits** (as long as it takes) for a person in the UI. A
review shows them its inputs, and they set the object to one of the listed
states, optionally with instructions. Anything it fills in, they enter as
JSON matching the object's attributes, or as text for an object without
attributes. Each decision starts a fresh pass.

Whoever may decide it finds it in their inbox, and is told it waits on them,
by email and on Slack as they choose: the holders of the roles its human
objects name, or for a review no human object handles, everyone who sees the
flow; admins only when nobody else may. Name a role to say who is told.
`remindAfter` reminds them every that long while it waits. `waitAtMost` fails
it once it has waited that long, so a review nobody decides doesn't hold the
run for ever: the failure ends its branch, or goes to its `onFailure`
handler, which takes what the review took and the failure, as any handler
does (an escalation, say, or a default decision made by an agent).

### 4.7 How a run ends

- **done**: nothing is left to run and no unhandled failure happened.
- **failed**: some branch failed without a handler.
- **terminated**: a duplicate of a unique value; reviews still waiting end
  undecided.
- **interrupted**: a person stopped it. A run whose server stops, as for a
  deploy, isn't: another server takes it over and it goes on from what it
  stored. Its reviews wait again as they were; an agent, script or
  connector step going then is lost and goes again if its `retries` let it,
  or else fails, which `onFailure` can handle. So give a step whose work
  is long or costly `retries` if it should survive a deploy, and keep in
  mind that a connector step going again may post or write twice.

---

## 5. What each step sees and must hand in

You don't write the plumbing, but the prompts you write sit inside it.

### 5.1 An agent's prompt

The engine builds it as follows:

1. **Your `prompt`.** With `credentials`, a paragraph after it names each
   service, its base URL and the variable holding its URL at the proxy, and
   says to send `Authorization: Bearer $CREDENTIALS_TOKEN` (section 5.3).
2. With `describeFlow`, the line *You carry out the process "Name" of the
   flow described below in OPL…*, then `<flow>…OPL…</flow>`.
3. The inputs, one element per object:
   ```
   <input name="Ticket" state="bug">{"key":"SHOP-12","summary":"…"}</input>
   <input name="Style guide" role="instrument">{"data":"…"}</input>
   <input name="Notes" role="affected">{"data":"…"}</input>
   ```
   `instrument` is a reference not to change. `affected` is the agent's to
   change: what it hands in is the new version.
4. For a revision, a change request:
   ```
   The reviewer "Critic" marked the work below as "weak". Act on it following their instructions.
   <reviewed name="Post" state="weak">…</reviewed>
   <change_request>Cut the intro to two sentences.</change_request>
   ```
5. The worker gives the agent a `submit_output` tool with the **output
   contract**, and the agent hands in through it. A reply that doesn't fit
   is sent back once with what was wrong.

So write prompts that name the inputs as the board names them ("Fix the
exception in **Exception** following the **Style guide**") and say what a
good output is. Don't describe JSON shapes; the attributes and their
definitions do that.

### 5.2 The output contract

- One generated object without attributes: `{"data": …}`.
- One with attributes: `{"attr1": …, "attr2": …}`, exactly those keys.
- A list: a JSON array of those.
- Several generated objects: one JSON object holding each of them under the
  object's name.
- When the agent **decides states**, it hands in its output next to its
  decisions: for each decided object, the chosen state and optional
  instructions for whoever takes it in that state. An object it **reviews**
  (takes too) keeps its content, so only the decision goes in.

### 5.3 A script

- Each object it takes is a file in `$INPUTS`, named after the object with
  unsafe characters made underscores (`Jira Ticket` → `$INPUTS/Jira_Ticket`),
  holding the object's JSON (`{"data": …}`, an attribute object, or an array).
- It **prints** its output to stdout in the same contract as an agent's.
- Files go in `$OUTPUTS`, with their paths in file attributes.
- In a workspace, `$WORKSPACE` is the checkouts' directory and `$BRANCH` the
  run's branch.
- In a run a schedule started, `$SINCE` (alias `$LAST_RUN`) is when the
  schedule's last run that ended done started, as a Unix timestamp, and empty
  on its first run or in a run started by hand. Fetch only what changed
  since, and make what it lists persistent with a unique attribute, so the
  overlap is skipped. A failed run doesn't move it on; an item whose own run
  fails comes round only if it changes again.
  For a cursor of your own, such as the newest `updated_at` seen, carry a
  persistent object over instead (see 3.1).
- A non-zero exit fails it, with stderr as the error.
- What it prints, stdout and stderr, shows in its step's terminal window as
  it runs. Its stdout is a pipe, so most programs hold what they print back
  until a few KB pile up; Python is told not to (`PYTHONUNBUFFERED=1`), and
  others can be made to line by line with `stdbuf -oL`, `jq --unbuffered` or
  `grep --line-buffered`. Progress on stderr shows at once.
- It gets none of the instance's secrets: no model keys, no Sentry or Jira
  token. To call a service of its own, such as Jira or an internal API, its
  process picks a **credential** an admin keeps in the settings, under
  Credentials: the service's base URL and its secret. For each, the step
  gets `$CREDENTIAL_<NAME>_URL`, the credential's name in capitals with
  underscores for what isn't a letter or digit (`Issue Tracker` →
  `$CREDENTIAL_ISSUE_TRACKER_URL`), which is the webapp's proxy standing for
  the base URL, and one `$CREDENTIALS_TOKEN` for them all. Call a path under
  it with `Authorization: Bearer $CREDENTIALS_TOKEN` (or the token as basic
  auth's password); the proxy puts the secret where the service wants it, a
  header, basic auth or a query parameter, and passes the call on, blanking
  the secret out of the answer. The token ends with the step, so write the
  variables, never their values; it is blanked out of what the script
  prints, as kept. A path leaving the base URL (`..`, an encoded slash, a
  `;`, anything not ASCII) is refused; a redirect within it comes back as
  the proxy's URL, so `curl -L` follows it through the proxy. Only the usual
  request headers go on (`Accept`, `Content-Type`, `If-Match`,
  `User-Agent` and the like) and those the credential names, such as an
  API's version header, which an admin adds to it, and only the answer's
  headers saying what it is come back (`Content-Type`, `ETag`,
  `Retry-After`, rate limits, pages' counts), a `Link` header's pages as
  the proxy's URLs. Every call goes in the run's log. The service's host needn't be allowed for internet access, as
  the webapp makes the call.
- A variable such as `$JIRA_TOKEN` is there only if the instance passes it
  to every script with `CHILD_ENV`, which agents read too; prefer a
  credential. For a service built in, a connector step or an agent with the
  service's MCP server does without either. In a workspace, its git (as an
  agent's) has a token for the run's repositories, which can push.
- Where steps run in containers of their own, a script (as an agent's
  tools) reaches the internet only at the hosts an admin allowed in the
  settings, under Internet access, and its repositories' hosts: a script
  calling an API, or installing packages, needs its host allowed, which
  belongs in what you tell the user.
- A script can't decide states. Route its output with state rules.

```bash
# Prints {"data": ...} for an object without attributes. The process picks
# the credential Jira, whose base URL is https://jira.example.com.
key=$(jq -r .data "$INPUTS/Ticket_key")
curl -fsS "$CREDENTIAL_JIRA_URL/rest/api/2/issue/$key" \
  -H "Authorization: Bearer $CREDENTIALS_TOKEN" |
  jq '{key: .key, summary: .fields.summary, priority: .fields.priority.id | tonumber}'
```

In the setup file:

```yaml
- name: Fetch ticket
  kind: script
  credentials: [Jira]
  script: |
    key=$(jq -r .data "$INPUTS/Ticket_key")
    curl -fsS "$CREDENTIAL_JIRA_URL/rest/api/2/issue/$key" \
      -H "Authorization: Bearer $CREDENTIALS_TOKEN" | jq '{data: .fields.summary}'
  takes: [Ticket key]
  yields: [Summary]
```

A credential is named in the file, never written in it: if the app has none
of that name, an admin adds it first, and the import says so.

### 5.4 A person

- A **review**: the human process takes X and yields X with `states`. The
  person sees X and picks a state, with instructions.
- **Entry**: the human process yields Y without taking it. The person fills
  Y in, as JSON with exactly Y's attributes (an array for a list), or as text
  if Y has none. That is how a hand-started run gets its input.
- Both at once: a person can review one object and fill in another in the
  same step.
- A person object it takes (`Editor`) says who carries it out.

### 5.5 A connector step

It carries out its one operation with the token an admin set for the
connector, and generates one object, what the service answered:
- An object with attributes takes the answer's fields its attributes are
  named after; a dotted name reaches into one. An attribute no result has a
  field for fails the step, naming the fields there are.
- A list gets an item per result of an operation that lists, such as
  `list_issues`.
- An object without attributes holds the whole answer under `data`.

GitHub's issues and pull requests come as `key` (`owner/repo#number`, unique,
so a shared struct can tell records apart by it), `repo`, `number`, `title`,
`state` (`open`, `closed`, or for a pull request `merged`), `body`,
`author`, `labels`, `url`, `createdAt` and `updatedAt`; an issue has
`stateReason`, `assignees`, `comments`, `closedAt` and `pullRequest`, a pull
request `draft`, `head`, `headSha`, `base`, `merged`, `mergeable` and
`mergedAt`.

Other connectors' records have a `key` too: a Slack message
`channel/ts`, a Telegram message `chatId/messageId`, a Home Assistant entity
its `entityId`, a Google file its id. Slack's messages have `channel`, `ts`,
`user`, `text`, `threadTs` and `replies`; Telegram's `messageId`, `chatId`,
`chat`, `from`, `text`, `replyTo` and `date`; Home Assistant's entities
`entityId`, `name`, `state`, `unit`, `lastChanged` and `attributes`. An HTTP
API answers what it answers, or `text` for what isn't JSON.

`{{Object}}` in an argument is what the object holds, its value for one
without attributes, and `{{Object.attribute}}` one of its attributes: a
string as it is, anything else as JSON. A list of strings, such as
`labels`, is JSON or separated by commas. A step that takes a list doesn't
pick an item of it; put the connector process in a flow process that runs
per item. An argument naming an object the step doesn't take fails it before
anything reaches the service. A pull request is opened from a branch that is
already on GitHub: runs' branches stay in the worker unless a step pushes
them.

---

## 6. OPL, sentence by sentence

Spoletta writes exactly these sentences (names in double quotes). Read the OPL
of a flow you built (`GET /api/flows/{id}/opl`, or beside the board) and
check that each one is what you meant.

**Objects**

| Construct | Sentence |
|---|---|
| person | `"Editor" is a person.` |
| list | `"Exceptions" is a list of items, at least 1.` / `… between 2 and 5.` / `… is a list of items.` |
| specialization | `"Bug" is a "Ticket".` |
| persistent | `"Exceptions" is stored, so each run keeps what it generated of it.` |
| shared | `"Exception" is shared, so every flow using it reads and writes the same records.` |
| states | `"Post" can be "good" or "weak".` |
| rule | `"Ticket" is "urgent" when priority <= 2.` |
| attributes and exhibited parts | `"Ticket" exhibits "key" and "summary".` |
| aggregated parts | `"Release" consists of a list of "Change".` |

**Processes**

| Construct | Sentence |
|---|---|
| kind | `"Fix" is an agent process, which an AI agent carries out.` (also: a script process / a human process, which a person carries out / a flow process, made of processes of its own / a connector process, which carries out an operation on a service such as GitHub) |
| subflow | `"Triage" is a procedure, which runs the flow "Triage ticket".` |
| agent link | `"Editor" handles "Review".` |
| event | `"Post" entering "weak" triggers "Write".` |
| condition | `"Fix" occurs if "Ticket" is "bug", and is skipped otherwise.` |
| condition under a quorum | `"Merge" takes "Review A" if it is "approved".` |
| instrument | `"Fix" requires "Guide", which it doesn't change.` |
| consumption | `"Ship" consumes "Token".` |
| plain input | `"Fix" takes "Exception".` |
| review | `"Critic" decides whether "Post" is "good" or "weak".` |
| decided output | `"Triage" yields "Ticket" as "bug" or "feature", deciding which.` |
| entry | `"Request" has a person fill in "Ticket".` |
| effect | `"Edit" affects "Notes".` / `"Edit" affects "Notes", changing it to "edited".` |
| output in a state | `"Draft" yields "Notes" as "draft".` |
| output | `"Fix" yields "Patch".` |
| quorum | `"Summarize" needs 2 of what it takes, and is skipped once it can't get them.` |
| rounds (agent reviews only) | `"Critic" runs at most 3 rounds.` |
| loops (when set) | `"Fix" asks before going round a loop more than 10 times.` |
| timeout | `"Fix" fails after 900 seconds.` |
| retries | `"Fix" is tried again up to 2 times when it fails.` |
| remindAfter (human) | `"Approve" reminds whoever decides it every 4 hours.` |
| waitAtMost (human) | `"Approve" fails after waiting 2 days.` |
| failure connection | `"Escalate" handles a failure of "Fix".` |
| next connection | `"Open PR" follows "Fix".` |

**Board**

| Construct | Sentence |
|---|---|
| start | `The flow starts with "Request".` / `"Each exception" starts with "Fix".` |
| end objects | `The flow ends with "Report".` |
| zoom | `"Each exception" zooms into:`, then the inner board indented |

Objects come first, then processes top to bottom as the board places them,
then the start, the end, and zoomed boards.

---

## 7. The setup file

A **setup** is everything built in the app, as one YAML file: MCP servers,
structs, procedures and flows, with every board, process, object, link and
schedule on them. You can write one and import it, and you can export the
app's current setup to read or edit. [webapp/SETUP.md](../webapp/SETUP.md) is
the full reference; the essentials are below.

```yaml
spoletta: 1              # the format's version, always first
mcpServers: [...]        # name, url, description, auth, connector (sign-in and tokens happen in the app)
structs: [...]           # objects, each its own struct named as the object
procedures: [...]        # flows other flows run through subflow processes
flows: [...]             # job flows
```

Conventions that matter when writing one:

- **Everything refers to everything else by name.** Processes name objects in
  `takes`/`yields`, and processes in `next`/`onFailure`/`start.next`.
  Process and object names are identifiers across the **whole flow**, inner
  boards included, so an inner process refers to its flow process's
  interface objects by plain name, without listing them again. `next`,
  `onFailure` and `start.next` only reach processes on the same board. State
  names are identifiers per object. If two processes or objects of a flow,
  or two states of an object, share a name, give each but the first a `key`
  and refer to it by that. Simplest: give every process and object of a flow
  a distinct name.
- **Unknown fields are refused**, so a typo fails the import rather than
  being dropped silently.
- **A file is imported whole or not at all.** A problem anywhere changes
  nothing and says what was wrong.
- **Import creates the flows the app doesn't have and makes over the ones it
  has, by name, to match the file.** Flows the file doesn't mention are left
  alone. A flow made over keeps the processes, objects, states and attributes
  that match by name, so its runs and stored instances still refer to them.
  It is saved as a version first, and the import is a step of its history,
  so it can be undone. A file is one board: it carries no versions and
  nothing of which version a flow published, and importing over a flow
  changes its draft, leaving the version it published as it was.
- **Positions are optional.** `x`, `y`, `width`, link `at` and `route` are
  for round-tripping a board someone drew. When you write a flow, leave them
  out: import lays the board out in layers from the start and `next`, with
  objects beside the first process linking them. Anything matched to an
  existing element keeps its place, width and link placement when the file
  leaves them out. The user can press Tidy afterwards.
- Shorthands: a link with nothing but its object is just the name; a state
  without a key or rule is just the name; `kind` defaults to `agent`.
- **Notes** (§3.11) are listed per board under `notes:`. Leaving them out
  keeps a board's notes as they are, so a file changing processes never
  loses them.

Import: `POST /api/setup` with the YAML (or JSON) as the body, or the Import
button on the flow list page. It replies with the flows it created and those
it made over. Export: `GET /api/setup`, or the Export button.

---

## 8. Worked examples

Each example starts from what a user might ask, then gives the design, the
setup file, and the OPL to expect. They build on each other; read the first
two even if you need only a later one.

### 8.1 Release notes with a human sign-off

> "Every release, draft the release notes from the merged PRs and let me
> approve them before they're posted. If I ask for changes, redo them."

Design:
- The run needs to know which release, so a person fills in **Release**
  first.
- **Draft** (agent, in the repo) takes Release and writes **Notes**.
- **Approve** (human) reviews Notes: `approved`, `changes requested` or
  `rejected`.
- **Revise** (agent) takes Notes in `changes requested` and writes Notes
  again, following the instructions. Approve reviews the new version.
- **Post** (agent with the Slack MCP server) runs on `approved`.
- `rejected` ends the run, since nothing takes it.

```yaml
spoletta: 1
mcpServers:
  - name: Slack
    url: https://mcp.example.com/slack
flows:
  - name: Release notes
    repositories:
      - name: shop
        url: https://github.com/acme/shop.git
        base: main
    start:
      next: Pick release
    objects:
      - name: Release manager
        human: true
      - name: Release
        attributes:
          - name: version
            definition: The version being released, such as 4.12.0.
          - name: previous
            definition: The version released before it, to compare against.
      - name: Notes
        states: [approved, changes requested, rejected]
    processes:
      - name: Pick release
        kind: human
        takes: [Release manager]
        yields: [Release]
      - name: Draft
        prompt: |
          Write release notes for the Release, covering every pull request
          merged between the previous version's tag and the new one. Group
          them under Features, Fixes and Internal. One line each, in plain
          language a customer understands; link the pull request.
        takes: [Release]
        yields: [Notes]
      - name: Approve
        kind: human
        takes: [Release manager, Notes]
        yields:
          - object: Notes
            states: [approved, changes requested, rejected]
      - name: Revise
        prompt: Revise the release notes for the Release as the reviewer asks.
        takes:
          - Release
          - object: Notes
            state: changes requested
        yields: [Notes]
      - name: Post
        prompt: Post the Notes to the #releases channel, as they are.
        mcpServers: [Slack]
        repositories: []
        takes:
          - Release
          - object: Notes
            state: approved
```

OPL to expect (the order of process sentences follows board position):

```
"Release manager" is a person.
"Release" exhibits "version" and "previous".
"Notes" can be "approved", "changes requested" or "rejected".
"Pick release" is a human process, which a person carries out.
"Release manager" handles "Pick release".
"Pick release" has a person fill in "Release".
"Draft" is an agent process, which an AI agent carries out.
"Draft" takes "Release".
"Draft" yields "Notes".
"Approve" is a human process, which a person carries out.
"Release manager" handles "Approve".
"Approve" decides whether "Notes" is "approved", "changes requested" or "rejected".
"Revise" is an agent process, which an AI agent carries out.
"Revise" takes "Release".
"Notes" entering "changes requested" triggers "Revise".
"Revise" yields "Notes".
"Post" is an agent process, which an AI agent carries out.
"Post" takes "Release".
"Notes" entering "approved" triggers "Post".
The flow starts with "Pick release".
```

Points to notice:
- Why two processes write Notes: Draft runs once Release exists. A process
  with an event link runs only on its events (section 4.2), so a single
  Draft with both links would never write the first version.
- Revise gets Approve's instructions and the Notes it reviewed as a change
  request. Release still goes along as an ordinary input.
- Neither Draft nor Revise takes Notes plainly. Draft would wait for Notes
  before it could ever write it.
- Approve takes Notes plainly, so it runs again each time Revise hands in a
  new version. The person can send it back as often as they like.
- Post's `repositories: []` keeps it out of the checkout it doesn't need.

### 8.2 A writer and a critic

> "Write a blog post about our new search, and have a second agent critique
> it until it's good, but don't loop forever."

```yaml
spoletta: 1
flows:
  - name: Blog post
    start:
      next: Write
    objects:
      - name: Post
        states: [good, weak]
    processes:
      - name: Write
        prompt: |
          Write an 800-word blog post introducing our new search: what it
          finds that the old one didn't, and how to use filters.
        thinking: medium
        takes:
          - object: Post
            state: weak
        yields: [Post]
      - name: Critic
        prompt: |
          Critique the Post as an editor would. It is good when it is
          accurate, concrete and under 900 words; otherwise it is weak.
          When weak, say exactly what to change.
        model: anthropic/claude-opus-5-5
        rounds: 3
        takes: [Post]
        yields:
          - object: Post
            states: [good, weak]
      - name: Publish
        kind: human
        takes:
          - object: Post
            state: good
```

Write is the start, so it writes the first version without being
triggered, and its event link brings it back for each rewrite. This is the
other way (besides 8.1's split) to have one process both write and revise.
The topic is in the prompt because the user gave it. If each run needs a
different topic, a person must enter it first. Then use 8.1's
writer-and-reviser split, because Write would no longer be the start.

Lines to expect in the OPL (an excerpt; each sentence about what triggers a
process is printed with that process):

```
"Critic" decides whether "Post" is "good" or "weak".
"Critic" runs at most 3 rounds.
"Post" entering "weak" triggers "Write".
```

What happens: Write, Critic (weak), Write with a change request, Critic
(weak), Write, Critic. On a fourth verdict of weak, the Critic fails, because
it has run 3 rounds and sending Post back would rerun Write. Add
`onFailure: [Publish anyway]` to the Critic to handle that rather than fail
the run.

### 8.3 Triage: let an agent pick the branch

> "When a support ticket comes in, decide if it's a bug or a feature
> request. Bugs get a fix attempted; features get a spec. Either way, post a
> summary."

```yaml
spoletta: 1
structs:
  - name: Ticket
    attributes:
      - name: key
        definition: The Jira key, such as SHOP-123.
      - name: summary
      - name: description
    states: [bug, feature]
flows:
  - name: Triage ticket
    start:
      next: Enter ticket
    objects:
      - copyOf:
          flow: Ticket
      - name: Fix
      - name: Spec
      - name: Summary
    processes:
      - name: Enter ticket
        kind: human
        yields: [Ticket]
      - name: Classify
        prompt: |
          Decide whether the Ticket reports a bug (something that used to
          work, or should work, doesn't) or asks for a feature.
        takes: [Ticket]
        yields:
          - object: Ticket
            states: [bug, feature]
      - name: Attempt fix
        prompt: Find the cause of the bug in the Ticket and fix it, with a test.
        takes:
          - object: Ticket
            state: bug
        yields: [Fix]
      - name: Write spec
        prompt: Write a one-page spec for the feature the Ticket asks for.
        repositories: []
        takes:
          - object: Ticket
            state: feature
        yields: [Spec]
      - name: Summarize
        prompt: Summarize what was done about the Ticket, in three lines.
        quorum: 1
        takes: [Fix, Spec]
        yields: [Summary]
```

This shows two ideas:
- **An agent deciding a state routes.** Classify reviews Ticket (takes it and
  lists states), so Ticket keeps the content the person entered and only
  gains a state. Each branch's process has an event link on its state.
- **Rejoining with a quorum.** Summarize takes Fix and Spec, but only one of
  them ever exists. Without `quorum: 1` it would never run, and the run would
end done without a Summary. (Fix
  and Spec are never skipped here; their processes simply never run.)

The alternative rejoin uses **condition links**, so the branch not taken is
*skipped* and its output settled as absent. Summarize can then do without a
quorum. A condition is checked as soon as its process is ready, so it must
be on an object that only exists once the decision is made. Ticket won't do:
it exists, in no state, from the moment the person enters it. So Classify
generates a separate **Triage** object in the state it decides, and the
branches condition on that:

```yaml
    objects:
      # ...Ticket, Fix, Spec and Summary as above, plus:
      - name: Triage
        states: [bug, feature]
    processes:
      - name: Classify
        prompt: |
          Decide whether the Ticket reports a bug or asks for a feature.
        takes: [Ticket]
        yields:
          - object: Triage
            states: [bug, feature]
      - name: Attempt fix
        prompt: Find the cause of the bug in the Ticket and fix it, with a test.
        takes:
          - Ticket
          - object: Triage
            state: bug
            condition: true
        yields: [Fix]
      - name: Write spec
        prompt: Write a one-page spec for the feature the Ticket asks for.
        takes:
          - Ticket
          - object: Triage
            state: feature
            condition: true
        yields: [Spec]
      - name: Summarize
        prompt: Summarize what was done about the Ticket, in three lines.
        takes: [Fix, Spec]
        yields: [Summary]
```

Both branches become ready once Triage exists. The one whose condition fails
is skipped, its output is settled as absent, and Summarize runs with
whichever of Fix and Spec exists. Prefer conditions when a branch is decided
once, and events when an object can enter a state many times, as in review
loops.

### 8.4 Fix Sentry exceptions every morning, one at a time

> "Every weekday at 7, take our top unresolved Sentry errors, try to fix
> each one in its own branch, and let me review each fix before a PR is
> opened. Never work on the same issue twice."

Design:
- A schedule, so no human can come first: an agent with Sentry's MCP
  server fetches the issues. The `http` connector, installed at Sentry's
  API with a token, would do as a connector process.
- **Exceptions** is a persistent list with a unique `id`, so each issue is
  claimed by one run only. Issues whose runs failed are released and tried
  again.
- **Each exception** is a recording flow process. It runs per item, each a
  run of its own on its own branch.
- Inside: Fix (agent) yields Patch → Review (human) → Open PR (agent). Fix failing goes
  to Report (agent), which comments on the Sentry issue.

```yaml
spoletta: 1
mcpServers:
  - name: Sentry
    url: https://mcp.sentry.dev/mcp
flows:
  - name: Fix Sentry exceptions
    repositories:
      - name: shop
        url: https://github.com/acme/shop.git
        base: main
    branchPrefix: spoletta/sentry
    schedules:
      - cron: "0 7 * * 1-5"
        timezone: Europe/London
    start:
      next: Fetch
    objects:
      - name: Exceptions
        many: true
        persistent: true
        attributes:
          - name: id
            unique: true
          - name: shortId
          - name: title
          - name: culprit
          - name: permalink
      - name: Outcomes
        many: true
    processes:
      - name: Fetch
        prompt: |
          List the 5 unresolved errors of the acme organization's shop
          project in Sentry with the most events (is:unresolved
          level:error), and reply with them.
        mcpServers: [Sentry]
        repositories: []
        yields: [Exceptions]
      - name: Each exception
        kind: flow
        recording: true
        takes: [Exceptions]
        yields: [Outcomes]
        board:
          start:
            next: Fix
          objects:
            - name: Developer
              human: true
            - name: Patch
              states: [approved, changes requested]
              attributes:
                - name: cause
                  definition: Why the exception happened, in two sentences.
                - name: change
                  definition: What was changed, and why that fixes it.
          processes:
            - name: Fix
              prompt: |
                Find the cause of the exception in Exceptions, fix it with
                the smallest change that is clearly right, add a test that
                fails without it, and commit. Don't push.
              mcpServers: [Sentry]
              timeout: 1800
              retries: 1
              takes:
                - Exceptions
                - object: Patch
                  state: changes requested
              yields: [Patch]
              onFailure: [Report]
            - name: Review
              kind: human
              takes: [Developer, Patch]
              yields:
                - object: Patch
                  states: [approved, changes requested]
            - name: Open PR
              prompt: Push the branch and open a pull request describing the Patch.
              takes:
                - Exceptions
                - object: Patch
                  state: approved
              yields: [Outcomes]
            - name: Report
              prompt: |
                Fixing this exception failed. Comment on the Sentry issue
                with what was tried and why it failed.
              mcpServers: [Sentry]
              repositories: []
              yields: [Outcomes]
```

Things to notice:
- Inside **Each exception**, `Exceptions` is **one item**, and both Fix and
  Open PR take it. Outcomes is a list outside, so what Open PR or Report
  generates inside is one item of it.
- The inner objects (Developer, Patch) are listed under the inner board's
  `objects`. Exceptions and Outcomes are not: they are the flow process's
  interface, so the inner processes name them directly.
- Report needs no explicit `takes`: as a failure handler it is given what Fix
  took, plus `Failure of Fix`.
- On a repeat morning, Fetch returns some of the same issues. Those already
  claimed by a run that is still going or ended done are skipped. Only new
  ones, or ones whose earlier run failed, are worked on.

### 8.5 A script with rules instead of an agent's judgement

> "Check our staging health endpoint every 10 minutes. If it's down, page
> on-call; if it's just slow, post to #ops."

```yaml
spoletta: 1
flows:
  - name: Staging health
    schedules:
      - cron: "*/10 * * * *"
        timezone: UTC
    start:
      next: Probe
    objects:
      - name: Health
        attributes:
          - name: status
            definition: The HTTP status code, 0 if unreachable.
          - name: ms
            definition: How long the request took, in milliseconds.
        states:
          - name: down
            rule: status != 200
          - name: slow
            rule: ms > 2000
          - name: fine
            rule: ms <= 2000
    processes:
      - name: Probe
        kind: script
        timeout: 30
        script: |
          out=$(curl -s -o /dev/null -w '%{http_code} %{time_total}' --max-time 20 https://staging.acme.com/health) || out="0 20"
          set -- $out
          jq -n --argjson s "$1" --argjson t "$2" '{status: $s, ms: ($t * 1000 | floor)}'
        yields: [Health]
      - name: Page on-call
        prompt: Page the on-call engineer that staging is down, with the Health.
        takes:
          - object: Health
            state: down
      - name: Tell ops
        prompt: Post to #ops that staging is slow, with the Health.
        takes:
          - object: Health
            state: slow
```

The script prints the object's contract (two attributes). Health comes out
in the **first** state whose rule holds. Order matters: an unreachable host
has `status: 0`, which is `down` even though `ms` may also exceed 2000.

### 8.6 Reusing a procedure

> "Several of our flows end with 'tell the team what changed'. Make that one
> reusable piece."

```yaml
spoletta: 1
mcpServers:
  - name: Slack
    url: https://mcp.example.com/slack
procedures:
  - name: Announce change
    start:
      next: Announce
    objects:
      - name: Change
        attributes:
          - name: title
          - name: description
          - name: link
            definition: Where to see the change, such as its pull request.
      - name: Announcement
        end: true
        attributes:
          - name: url
            definition: The link to the message posted.
    processes:
      - name: Announce
        prompt: |
          Post the Change to #changes on Slack: its title, two sentences
          of what it means for the team, and the link.
        mcpServers: [Slack]
        takes: [Change]
        yields: [Announcement]
flows:
  - name: Bump dependencies
    repositories:
      - name: shop
        url: https://github.com/acme/shop.git
    start:
      next: Bump
    objects:
      - copyOf:
          flow: Announce change
          object: Change
      - copyOf:
          flow: Announce change
          object: Announcement
    processes:
      - name: Bump
        prompt: |
          Bump every dependency with a patch release available, run the
          tests, push the branch and open a pull request. Describe it as
          the Change.
        yields: [Change]
      - name: Tell the team
        kind: subflow
        runs: Announce change
        takes: [Change]
        yields: [Announcement]
```

Tell the team hands its Change to the procedure's `Change` (same name) and
gets back what the procedure's run left of `Announcement`, an `end` object.
A copy is named as what it copies, so it needs no `name` (one that differs
is refused), and links refer to it by the source's name. The copies are
written out here. A subflow process with no `yields` gets them anyway:
import creates copies of the procedure's `end` objects and links them as
its outcomes. Because Change is a copy, Bump's output has exactly the
procedure's attributes.

A procedure runs as a run of its own, with **its own** repositories, in its
own workspace on its own branch. It can't carry on the caller's checkout. So
Bump pushes and opens the pull request itself, and only what needs no
checkout goes in the procedure. A procedure that works on code must list the
repositories itself, and works on a fresh branch of its own.

### 8.7 Fan-in with a quorum

> "Ask three different models whether this migration is safe; go ahead if
> at least two say yes."

```yaml
spoletta: 1
flows:
  - name: Migration check
    repositories:
      - name: db
        url: https://github.com/acme/db.git
    start:
      next: Submit migration
    objects:
      - name: Migration
      - name: Verdict A
        states: [safe, unsafe]
      - name: Verdict B
        states: [safe, unsafe]
      - name: Verdict C
        states: [safe, unsafe]
    processes:
      - name: Submit migration
        kind: human
        yields: [Migration]
      - name: Judge A
        model: anthropic/claude-opus-5-5
        prompt: Is the Migration safe to run on a live database with no downtime?
        takes: [Migration]
        yields:
          - object: Verdict A
            states: [safe, unsafe]
      - name: Judge B
        model: openai/gpt-5
        prompt: Is the Migration safe to run on a live database with no downtime?
        takes: [Migration]
        yields:
          - object: Verdict B
            states: [safe, unsafe]
      - name: Judge C
        model: google/gemini-3-pro
        prompt: Is the Migration safe to run on a live database with no downtime?
        takes: [Migration]
        yields:
          - object: Verdict C
            states: [safe, unsafe]
      - name: Run migration
        kind: script
        quorum: 3
        takes:
          - Migration
          - object: Verdict A
            state: safe
            condition: true
          - object: Verdict B
            state: safe
            condition: true
          - object: Verdict C
            state: safe
            condition: true
        script: |
          cd "$WORKSPACE/db"
          ./bin/migrate "$(jq -r .data "$INPUTS/Migration")"
```

With a quorum, Migration and each verdict count towards it only once they
exist **and** hold their condition. Migration always counts, so `quorum: 3`
means "Migration plus two safe verdicts". A quorum of 2 would run on a
single safe verdict. Count everything the process takes when you set a
quorum. The script runs in the flow's workspace, which has a checkout of
each repository under `$WORKSPACE`, named as the repository is. The model ids are examples: use those the app offers (the
agent settings list them).

---

## 9. Rules the app enforces

An import that breaks one of these is refused as a whole, with the reason.
Check them before you import.

**Links**
- A process and the objects it links to are on the same board. A flow
  process's inner processes may also link to its interface objects.
- A non-human process yields a given object **at most once** (one output
  link per object).
- Only an **agent's** or a **human's** output link lists `states`. An
  agent's output link names either `state` or `states`, never both.
- A human's output link never names a single `state`. It lists the states to
  decide between, or none (an entry).
- `condition: true` only goes on an input link that names a `state`.
- `role` is only `instrument` or `consumption`, only on input links, never
  for a person. Only one process may consume a given object.
- A person (`human: true`) is taken only by a human process, and never makes
  up another object.
- A named state must be one of that object's states.

**Processes**
- `onFailure` only from an agent, script, connector or human process.
- A process can't `next` or `onFailure` to itself. Connections stay within
  one flow.
- A subflow can't, directly or through other subflows, run its own flow.
- `rounds` from 1 to 20; `loops` from 1 to 100; `timeout` from 1 to 86400;
  `retries` from 0 to 5; `quorum` at least 1; `remindAfter` and
  `waitAtMost` from 1 to 31536000.
- `thinking` is one of the listed levels.
- `credentials` only on an agent or script process, each naming a credential
  the app has.

**Objects**
- Part names don't clash with their owner's attribute names, and parts don't
  loop.
- A copy's name, states, attributes and parts come from its source. Leave
  them out (a `name` other than the source's is refused); links refer to it
  by the source's name.
- A file attribute can't be filled in by a person.

**Common mistakes that import fine but don't work**
1. A first agent taking an object nothing produces gets it **empty** when the
   flow is run with the Run button or a schedule; only a run started over the
   API, or with Start with inputs in Run's menu, is given it (section 4.1).
2. A process taking the object it revises **plainly** (no state) waits for it
   before it ever writes it. Revisers take it **in the send-back state**.
   And a process with an event link runs only on its events (or from the
   start or a `next`), so one that must also write the first version
   doesn't, unless it is the start. Split it into a writer and a reviser
   (8.1).
3. Rejoining branches **without** a quorum or condition links: the join never
   runs, because the branch not taken never generates its object.
4. Expecting a **script or connector** step to decide a state. Use rules.
5. A **rule-less catch-all state**: a state without a rule never matches.
6. An agent reviewer **without an `onFailure`** when hitting its rounds
   should not fail the run.
7. Two objects on the same board with the **same name**: allowed with a
   `key`, but every reference then needs that key. Name things apart.
8. A **scheduled** flow starting with a human entry: it waits for a person
   every time, and later times are skipped while it waits.
9. Using `next` where data should flow. `next` passes nothing. If Q needs
   P's output, link Q to that object. And a `next` out of a human process is
   not followed.
10. A **procedure** expected to work on the caller's branch. It has its own
   workspace (8.6).

---

## 10. Building a flow for a user, step by step

### 10.1 Ask

Before drawing anything, find out:

1. **The trigger.** By hand, or on a schedule (when, what timezone)?
2. **The input.** What does a run work on, and where does it come from (a
   person types it, Sentry, an API, a repository)? This decides the first
   process.
3. **The steps and who does each.** An agent, a script (deterministic: API
   calls, builds), or a person (approval, judgement, entering data)?
4. **The decisions.** Where does the flow branch, and on what? Who decides:
   an agent, a person, or a rule on a value?
5. **The loops.** What can be sent back, and to whom? How many rounds before
   giving up?
6. **Failure.** What should happen when a step fails: retry, hand to someone,
   or fail the run?
7. **Repeats.** Must the same thing never be processed twice (persistent +
   unique)? Is there a list to handle item by item?
8. **Tools.** Which repositories, MCP servers, models? Which services do its
   scripts or agents call with a secret, and is there a credential for each
   (or must an admin add one)?
9. **Reuse.** Does this share a piece (a procedure) or a shape (a struct)
   with flows that exist? Export the current setup (`GET /api/setup`) to see
   what is there, and reuse it by name.

### 10.2 Draft it in OPL first

Write the flow as the OPL sentences of section 6, before any YAML. Read them
back to the user: *"Release manager handles Approve. Approve decides whether
Notes is approved, changes requested or rejected. Notes entering approved
triggers Post."* Users can check sentences far more easily than YAML, and
mistakes such as a missing start or an input nothing produces show up as
missing sentences.

### 10.3 Write the setup file

The flow list's templates (*New from template*) are ready flows of the
kinds in section 8: triage, a write-and-critique loop, a fix approved by
its risk, a digest posted to Slack, a flow per item of a list and a form
a person fills in. When one is close to what the user wants, suggest
starting from it and changing it, rather than writing it anew.

- Start with `spoletta: 1`. Include only the flows you create or change (the
  import leaves others alone), plus any structs, procedures and MCP servers
  they name that don't exist yet.
- Give every board a `start`.
- Leave positions out.
- Give every attribute a `definition`; it is the agent's contract.
- Write prompts that name the inputs and outputs as the board does and say
  what good looks like. Leave out JSON shapes and the flow's plumbing.
- Prefer distinct names everywhere.

### 10.4 Import and verify

1. `POST /api/setup` with the file. If it is refused, the message says what
   is wrong; fix it and import again. Nothing changed meanwhile.
2. Read what is wrong with the board (`GET /api/flows/{id}/problems`), as
   the board shows beside Run. Errors are what a run can't get past as it
   is drawn (no start, a subflow running no flow, a connector process
   without its connector, operation or an argument it needs), and Run
   refuses the flow until they are fixed. Warnings are what a run may get
   past or what is set up outside the flow: an object nothing generates
   (common mistake 1), a process nothing leads to, an agent with no model,
   a review whose role nobody holds, a repository the flow lacks, or a
   skill, MCP server, credential or token it can't use. Fix what is yours
   to fix, and tell the user the rest.
3. Read back the OPL (`GET /api/flows/{id}/opl`, flow ids from
   `GET /api/flows`) and compare it with your draft sentence by sentence.
4. Tell the user what needs their hands: signing in to any new MCP server in
   the settings, a connector's token, and the first run.
5. Suggest a first run by hand, even for a scheduled flow, and watching its
   steps. A process can be tried first on its own, from its panel's *Try this
   process*: a trial runs only it, on inputs the user gives it, and stores
   nothing real runs see (persistent objects, shared records and `$SINCE`
   stay as they were), but what it does outside the flow, a message posted
   or a branch pushed, it really does. Its inputs can start from one of the
   flow's latest runs, trials too: what the process took there, or what the
   run held, so a flow can be tried a process at a time, each on what the
   trial of the one before it generated. Its inputs can be saved with it as
   named samples, which a setup file holds as a process's `samples`, and
   *Try on all samples* tries it on each of them at once, a trial each, to
   check a change of its prompt or model against all of them.

### 10.5 Changing a flow later

Export, edit, import. A flow is made over by name, keeping what matches by
name, so runs and stored instances survive. A flow in the trash is neither
exported nor made over: importing its name makes a new flow. **Renaming** a process, object,
state or attribute in the file makes a new one and drops the old. The stored
instances outlive the old object, but the new one doesn't see them, so
duplicate detection on a unique attribute starts over. Rename in the app
instead when stored values matter. Every import is a step of the flow's history, so
the user can undo it with Cmd-Z on the board.
