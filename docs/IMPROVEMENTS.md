# Ideas for improving Spoletta

Ideas for making Spoletta easier to use, and features it could gain. They
come from reading the code as of commit `411e2e0` (25 October 2026), not
from using the UI, so a few may need checking against the app. Each gap
names the code it was found in. The ideas are ranked by how much they would
help, the first five in most detail.

Left out on purpose, since they exist already: versions, undo and redo,
re-runs, retries and timeouts, the HTTP API, spending limits, the audit log,
the runs' and records' search, and carrying on an agent's conversation.

---

## The five that matter most

### 1. An inbox of what waits on me, and telling me about it

For flows with people in them, this is likely the biggest gap.

**What happens now**

- A review shows only while someone has its flow's board open with the run
  selected: the board brings a waiting review to the middle of the screen
  (`webapp/frontend/src/Board.tsx:922`).
- The flow list shows each flow's last run and nothing else
  (`webapp/frontend/src/FlowList.tsx:295`).
- Runs are listed only by flow (`webapp/runs.go:446`), so nothing can show
  everything waiting on a person across flows.
- Nothing ever tells a person that something waits on them. A flow can post
  to Slack with a connector step, but only if its author adds one to every
  flow.
- A human step never times out: `execute()` (`webapp/engine.go:1673`) runs
  only agent, script, Sentry and connector steps, so a review nobody decides
  waits forever.

**What it could be**

- A `#/inbox` page with a count beside it in the header, listing every
  waiting review and loop question the user's roles decide, in every flow
  they see.
- A message sent to the person when something starts waiting on them, by
  Slack, email or Telegram as they choose, linking straight to the review
  dialog. Nobody should have to wire it into a flow.
- Reminders for a review left waiting, and optionally, after a while,
  asking another role or failing the step so a failure connection can take
  over.
- A review dialog that works on a phone, since that is where a message
  sends people. The stylesheet has only six `@media` rules.

### 2. Draft and published versions

**What happens now**

A run takes the board as it is: `newRun` saves the current board as a
version and runs that (`webapp/runs.go:415`). That holds for runs started
by a schedule, over the API or on records too, so any of them can fire
while someone is halfway through an edit and run a half-built flow.

The Versions panel can only bring an older version back, and warns that
what changed since the last save is lost
(`webapp/frontend/src/Versions.tsx:7`). There is no note saying why a
version was saved and no way to see what changed between two.

**What it could be**

- A Publish action. Schedules, API keys and record runs run the last
  published version; the Run button can run the draft or the published one.
- A note on each version saying why it was saved.
- A diff between two versions shown on the board: processes, objects and
  links added, removed or changed, highlighted.

### 3. A home page of what happens across every flow

**What happens now**

The main page lists flows with their last run. Nothing shows what is
running, failing or waiting across them, or how cost and success change
over time.

**What it could be**

The data is kept already (`runs`, `run_steps`, `llm_usage`, `run_logs`), so
this is mostly UI:

- runs a day and the share that failed, per flow;
- the processes that fail most;
- what each flow costs, over time;
- how long people take to decide reviews;
- how often a reviewer sends an agent's work back, a measure of how good
  the agent's prompt is.

### 4. Trying one step cheaply while building a flow

**What happens now**

The only way to see whether the fifth step works is a whole run, paying for
every agent before it and doing whatever it does along the way: comments
posted, branches pushed, messages sent. Re-running from a process
(`webapp/reruns.go`) helps only once a run has ended.

**What it could be**

- **Try this process** in the process panel: run that one process on sample
  inputs, which are kept with it.
- **Pin** a step's output, so later steps can be tried without running the
  agents before them again.
- Later, a set of saved inputs per process, so a change of prompt or model
  can be checked against them before it is published.

### 5. Getting started

**What happens now**

- A new user starts from an empty list of flows, with nothing to copy.
- All of the board's help is one long hover text on a hint in the toolbar
  (`webapp/frontend/src/Board.tsx:2312`).
- When Run can't start, the only reason it gives is "Connect the start node
  to a process first" (`webapp/frontend/src/Board.tsx:2395`).

**What it could be**

- A gallery of templates made from setup files. The three flows in the
  README's screenshots are natural first ones: an agent triaging a ticket,
  a review loop, a fix approved by risk.
- A list of problems before Run: an object nothing generates, a process
  nothing reaches, a connector not installed, an agent without a model, a
  human step whose role nobody holds.
- Short hints shown where they apply (dragging from a state, say), and a
  `?` sheet of the shortcuts, instead of one long text.

---

## Smaller usability fixes

- **Flow list.** It filters only by whose a flow is
  (`webapp/frontend/src/FlowList.tsx:34`). Add a search, tags or folders,
  favourites, and a badge per flow saying it is failing or how many reviews
  wait in it.
- **Deleting.** Eleven deletes ask with `window.confirm`. The board already
  has undo, so an "Undo" notice after the delete would do better. Deleting a
  flow (`webapp/frontend/src/FlowList.tsx:95`) should put it in a trash it
  can be restored from.
- **OPM's words.** Say "uses (instrument)", "uses up (consumption)",
  "changes (effect)": plain words first, OPM's second, so newcomers aren't
  put off and OPM's readers still find their terms.
- **Toolbar.** About a dozen buttons and a Run button that is only an icon.
  Group the views (Code, OPL, Data, MCP servers, Repositories) under one
  switcher, and give Run a label and a menu: run with inputs, run the
  published version.
- **Keyboard.** A Cmd-K palette to jump to flows, runs and settings or add a
  building block; a way to find a process or object on a large board;
  shortcuts to save and to run.
- **Dark mode.** No `prefers-color-scheme` anywhere in the stylesheet,
  which people watching agents in terminal windows would notice.
- **Run list.** The sidebar shows the newest 200 runs (`webapp/runs.go:450`)
  with no filters. Add filters by status that open the flow's Data page with
  the search already filled in.
- **The assistant's changes.** They can be undone
  (`webapp/frontend/src/Assistant.tsx:188`), but showing on the board what
  it added, removed and changed would make them easier to trust.

---

## Further features

- **Triggers from other systems.** Webhooks that start a flow: a GitHub
  issue labelled or pull request opened, a Sentry alert, a Slack message.
  Each would check the sender's signature and map the payload onto the
  flow's inputs, built on what `POST /v1/flows/{id}/runs` already does with
  `flowInputs` (`webapp/api.go`).
- **Credentials for scripts.** There is no way to give a script a
  credential; a call needing one has to go through the HTTP API connector.
  The internet proxy can't add a header to a TLS tunnel it only passes on
  (`webapp/egress.go`), so this would work as the model proxy does
  (`webapp/llm.go`): the step gets a token, and a proxy reached by path
  swaps it for the real key, which the step never sees.
- **Comparing runs.** Two runs side by side: what each took and handed
  back, what it cost, how long it took. And a timeline of a run's steps,
  showing where the time went and what ran at once.
- **An assistant that knows the runs.** "Why did run 412 fail?" The
  assistant sees the flow but not its runs' logs.
- **Working together.** Notes on the board, comments on a review's
  decision, and seeing who else is editing the board.

---

## Where to start

The inbox with notifications (1) and draft and published versions (2):
the first is what people deciding reviews miss most, the second keeps
unattended runs from running half-made edits. Both build on what is there,
roles and versions, rather than needing anything new underneath.
