# Setup files

A setup file describes everything built in the app as YAML. That covers the MCP servers, the skills and commands, the structs, the procedures and the flows, with every board, process, object, link and schedule on them. You can keep it under version control, edit it by hand, or have an LLM write it, and then import it into any instance of the app.

It leaves out what runs produce: runs, versions and which of them a flow published, undo history, and the values that persistent objects stored. MCP servers' sign-ins and tokens stay out too, so after importing a server you sign in to it again, or an admin sets its token. Who a flow, skill or command belongs to stays out as well.

## Exporting and importing

On the main page, **Export** downloads `spoletta-setup.yaml`, and **Import** reads a file back in. The API does the same:

```sh
curl -o spoletta-setup.yaml http://localhost:8080/api/setup
curl --data-binary @spoletta-setup.yaml -H 'Content-Type: application/yaml' http://localhost:8080/api/setup
```

Import replies with the flows it created and the ones it updated. Since YAML takes JSON too, a JSON file works as well.

What importing does:

- A struct, procedure or flow the app doesn't have yet is **created**.
- One the app already has, by name, is **updated** to match the file. Anything on its boards that the file doesn't have is removed. Processes, objects, states and attributes that match by name, on the same board, keep their ids, so the flow's past runs and the values its persistent objects stored still point to them. The board is saved as a version first, and the import is one step of the board's undo history, so you can go back with Cmd-Z or from Versions. Going back restores the board only. The flow's repositories, branch prefix, schedules, tags and whether it's a procedure stay as imported, and so do MCP servers. Importing over a flow that published a version changes its draft only: its schedules, the API and runs on records go on running the version it published until someone publishes again.
- Flows that the file doesn't mention stay as they are. Import never deletes a flow.
- An MCP server is matched by name. Its URL, description, auth and connector are set from the file. A server whose URL or auth changes is signed out, and loses its token.
- A skill or command is matched by name. Its description, arguments and content are set from the file, and it keeps whoever it belongs to. One the app doesn't have is added as everyone's.
- A flow's schedules become the file's. Schedules the flow already has with the same cron and time zone are kept; others are removed.
- A flow's, procedure's or struct's tags become the file's, so one the file gives none loses those it had.
- The file is imported **whole or not at all**. If anything is wrong, the reply says what and where, and nothing changes. A flow that is running can't be updated.

Exporting refuses while two flows share a name, since the file refers to flows by name.

## The format

```yaml
spoletta: 1         # the format's version; required
mcpServers: [...]
skills: [...]       # what agents are given to know, picked per process
commands: [...]     # what /name in an agent's prompt stands for
structs: [...]      # objects defined once and used by flows
procedures: [...]   # flows other flows run
flows: [...]        # the jobs that run
```

Every field except names is optional. Leave out what you don't need: export writes only what differs from the default.

### MCP servers

```yaml
mcpServers:
  - name: jira          # letters, digits, - and _
    url: https://jira.example.com/mcp
    description: Our Jira
  - name: home
    url: https://home.example.com/api/mcp
    auth: token         # oauth (default): each person signs in; token: an admin sets one for everyone
  - name: github
    connector: github   # a connector built into the app; it takes a token an admin sets
    url: https://ghe.example.com/api/v3   # its service's API; the connector's own if absent
```

A connector is a service the app itself works with, such as GitHub. Agents use it as an MCP server, its operations being its tools, and a `connector` process carries out one of its operations as a step. The app has the connectors `github`, `slack`, `telegram`, `google` (Sheets and Docs), `homeassistant` (whose `url` is the installation's, which it needs) and `http` (any JSON API, at its `url`, installed once for each under its own name). docs/FLOWS.md lists their operations.

### Skills and commands

```yaml
skills:
  - name: house-style   # lowercase letters, digits and single hyphens
    description: How we write; use for anything a customer reads.   # required: the agent picks it by this
    content: |
      Short sentences. No jargon.
commands:
  - name: review
    arguments: "[file]" # what it takes, as the / menu shows it
    content: Review ${1:-the change} for bugs.
```

They are the settings' Skills & commands, written out whole. Processes name the skills they use, and a file may name ones it doesn't hold if the app it goes into has them.

### Flows and procedures

Flows and procedures have the same shape:

```yaml
- name: Fix exceptions
  tags: [nightly, billing]      # what the flow list finds it by and narrows to
  repositories:                 # what agents and scripts check out
    - name: app
      url: https://github.com/example/app
      base: main                # the branch runs start from; the default branch if absent
  branchPrefix: fix             # runs' branches are <prefix>/run-<id>
  schedules:
    - cron: "0 9 * * 1-5"       # or @hourly, @every 90m, ...
      timezone: Europe/London
      enabled: false            # enabled if absent
  start: {next: Fetch}          # the process the top board starts with
  end: {x: 40, y: 600}          # where the board's end is drawn
  processes: [...]
  objects: [...]
```

### Processes

```yaml
- name: Fetch
  kind: agent           # agent (default), script, human, flow, subflow or connector
  prompt: |
    Fetch the newest ticket.
  script: echo hi       # for script processes
  model: anthropic/claude-sonnet   # agent: provider/id; the default if absent
  thinking: low         # off, minimal, low, medium, high, xhigh or max
  mcpServers: [jira]    # agent: the MCP servers it may use, by name
  mcpTools:             # agent: the tools it keeps to of each, by server; all of an MCP server's,
    jira: [search]      #   and a connector's that only read, for a server not named here
  skills: [release-notes]   # agent: the skills it is given, by name: the file's, or the app's
  repositories: [app]   # the flow's repositories it works on; all if absent, none if []
  rounds: 3             # agent deciding states: how many times it can send work back
  loops: 5              # how many times it runs round a loop before someone is asked
  timeout: 600          # seconds
  retries: 2
  quorum: 1             # how many of what it takes it waits for
  describeFlow: true    # agent: tell it the flow it works in, in OPL
  recording: true       # flow process: each time it runs is a run of its own
  runs: Summarize       # subflow: the procedure it runs
  connector:            # connector process: an operation of an installed connector
    server: github
    operation: add_comment
    args: {owner: acme, repo: app, number: "{{Bug.number}}", body: "Looking at {{Bug.title}}"}
  takes: [...]          # links from objects it takes
  yields: [...]         # links to objects it generates
  next: [Review]        # the processes that run after it
  onFailure: [Alert]    # the processes that run instead, once it fails
  board: {...}          # flow process: its own board, shaped like a flow's
                        # (start, end, processes, objects)
  x: 40                 # where it is drawn; see Positions
  y: 140
  width: 240
```

### Links: `takes` and `yields`

An entry that is just a name is a plain link to that object. The long form:

```yaml
takes:
  - Ticket                          # takes the object and leaves it for others
  - {object: Ticket, role: instrument}    # uses it without changing it
  - {object: Ticket, role: consumption}   # uses it up; only one process can
  - {object: Ticket, state: open}         # runs once the ticket enters "open"
  - {object: Ticket, state: closed, condition: true}  # runs only if it is "closed", skipped otherwise
yields:
  - Summary                         # generates it
  - {object: Ticket, state: open}   # generates it in that state
  - {object: Ticket, states: [approved, rejected]}  # a human or agent decides between these
```

The app's own rules apply. A human process lists the states it decides between and never names a single state. Only human and agent processes list states. Any other process generates an object at most once. A person (an object with `human: true`) is taken only by human processes. `condition` needs a `state`.

A process taking an object and yielding it too affects it: it hands the object in changed. A review is written like this:

```yaml
- name: Review
  kind: human
  takes: [Patch, Reviewer]
  yields: [{object: Patch, states: [approved, changes requested]}]
```

### Objects

```yaml
- name: Ticket
  persistent: true      # stored across runs
  many: true            # a list of items
  min: 1                # how many items a list holds
  max: 5
  human: true           # a person, who carries out human processes
  end: true             # an outcome: on the end of the board, which callers get
  specializes: Issue    # another object of the flow it is a kind of
  states:
    - open
    - name: closed
      rule: When the fix is merged    # when an object generated in no state is in this one
  attributes:
    - name: title
      definition: What the ticket is about
      unique: true      # no two stored values share it
      file: true        # holds the path of a file the process wrote
  parts:                # objects making it up, shaped like objects
    - name: comments
      relation: aggregation   # exhibition (an attribute; default) or aggregation (a part)
      many: true
```

### Structs and copies

A struct is a single object, listed under `structs:`. Its name is the struct's name, and its `tags` are the struct's; no other object has tags. Flows and procedures use it through a **copy**:

```yaml
objects:
  - copyOf: {flow: Ticket}                       # a struct's object
  - copyOf: {flow: Summarize, object: Summary}   # one of a procedure's objects
```

A copy has the name, states, attributes and parts of what it copies, and follows it as that changes. So a copy lists none of them, and its name may be left out. If given, the name must be the source's. Links name the copy's states as the source names them.

### Subflows

A subflow process runs a procedure. What it takes is handed, by name, to the object of the same name on the procedure's top board. What it yields comes back from the procedure's outcomes, the objects marked `end: true`. If you leave out its `yields`, import brings in a copy of each outcome by itself and links it as yielded. Export writes them out explicitly.

A procedure can't run itself, however many procedures apart. A flow that other flows run must be listed under `procedures`.

### Names, keys and references

Everything refers to everything else by name. Process names and object names are each unique across a whole flow, inner boards included. State names are unique within their object. When two share a name, give each one after the first a `key` and refer to it by that:

```yaml
objects:
  - name: Issue
  - name: Issue
    key: Issue (2)
processes:
  - name: Triage
    takes: [Issue, Issue (2)]
```

Export does this by itself.

`next`, `onFailure` and `start.next` name processes on the same board. Processes on a flow process's inner board refer to the objects linked to that flow process, which sit on the board around it, by plain name, without listing them again. A link's `at: {x, y}` places such an object on the inner board.

### Positions

`x`, `y`, `width`, `route` and `at` say where things are drawn, and all of them are optional:

- Something the board already has keeps its place, width and `at` when the file doesn't give them. A link's `route` left out goes back to the default route.
- Something new is laid out in rows down from the start, following `next`. Each object goes beside the first process that links to it.

Use the board's Tidy button afterwards for a better layout.

## Writing a setup with an LLM

Give the LLM this file and, ideally, an export of your current setup as an example, then describe the flow you want. Things worth telling it:

- Start the file with `spoletta: 1`. Leave out positions. Unknown fields are refused, so stick to the ones above.
- Name a copy by `copyOf` alone, and refer to it by its source's name.
- To change one flow, the file only needs that flow, plus the procedures, structs and MCP servers it refers to that the app doesn't already have.

If the import refuses the file, its message names the flow, the process or object, and the link at fault. Hand it back to the LLM to fix.
