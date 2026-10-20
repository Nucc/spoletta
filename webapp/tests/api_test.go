package tests

// The webapp's API as the tests see it: copies of the JSON shapes it sends
// and takes, and of the constants that name kinds, statuses and messages,
// taken from the webapp's source. They live here rather than imported, so the
// tests use the webapp only through its HTTP API, as the frontend does; a
// change to what the API sends shows up as a test failing, not as one
// adapting to it.

import (
	"encoding/json"
	"time"
)

const agentLinkMessage = "a person carries out only a human process; link them to one"
const dataAttribute = "data"
const decisionLinkMessage = "only a human's or an agent's output link lists states"
const directionInput = "input"
const directionOutput = "output"
const kindAgent = "agent"
const kindConnector = "connector"
const kindFlow = "flow"
const kindHuman = "human"
const kindInput = "input"
const kindScript = "script"
const kindSentry = "sentry"
const kindSubflow = "subflow"
const maxItems = 50
const maxRouteLanes = 64
const maxSentryIssues = maxItems
const relationAggregation = "aggregation"
const relationExhibition = "exhibition"
const runDone = "done"
const runFailed = "failed"
const runInterrupted = "interrupted"
const runRunning = "running"
const runTerminated = "terminated"
const stepActive = "active"
const stepDone = "done"
const stepDuplicate = "duplicate"
const stepError = "error"
const stepSkipped = "skipped"
const stepWaiting = "waiting"

var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// The models offered for agent processes: pi's enabledModels and its default.
type agentModels struct {
	Default string   `json:"default"`
	Models  []string `json:"models"`
	// The names of the model providers admins added, by ID.
	Providers map[string]string `json:"providers,omitempty"`
}

// What the worker runs.
type agentRun struct {
	// Identify the process in the worker's logs.
	ProcessID   int64  `json:"processId"`
	ProcessName string `json:"processName"`
	Prompt      string `json:"prompt"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
	// What the process generates, which the agent hands in through the
	// worker's submit_output tool, whose parameters it shapes.
	Output *outputSpec `json:"output,omitempty"`
	// Where the agent writes the files it generates.
	FilesDir string `json:"filesDir,omitempty"`
	// The MCP servers the process uses, reached through the webapp's proxy
	// with the step's token; pi gets no MCP servers without them.
	MCPServers []mcpRunServer `json:"mcpServers,omitempty"`
	MCPToken   string         `json:"mcpToken,omitempty"`
	// The model keys of whoever the run is for, by environment variable.
	Keys map[string]string `json:"keys,omitempty"`
	// The model providers admins added, without their keys.
	Providers []runProvider `json:"providers,omitempty"`
	// The workspace it works in, if it works on repositories.
	Workspace *workspaceSpec `json:"workspace,omitempty"`
	// In a workspace, the pi session the worker keeps its conversation in, so
	// a person can carry it on once the step is over; see followups.go.
	// Resume carries on one that exists.
	SessionID string `json:"sessionId,omitempty"`
	Resume    bool   `json:"resume,omitempty"`
	// How long the run may take, if the process has a timeout; the
	// worker's own limit otherwise.
	TimeoutMS int64 `json:"timeoutMs,omitempty"`
	NoTools   bool  `json:"noTools,omitempty"`
	Guide     bool  `json:"guide,omitempty"`
}

// A model provider as the worker gets it.
type runProvider struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	API     string   `json:"api,omitempty"`
	BaseURL string   `json:"baseUrl,omitempty"`
	Env     string   `json:"env"`
	Models  []string `json:"models"`
}

// An attribute is something an OPM object exhibits, such as a ticket's
// subject. Its definition says what it holds. An agent process generating an
// object with attributes must reply with a JSON object holding exactly those
// attributes, so what it generates has a known shape.
type attribute struct {
	ID         int64  `json:"id"`
	Object     int64  `json:"object"`
	Name       string `json:"name"`
	Definition string `json:"definition"`
	// No two values a persistent object stores share this attribute's value.
	Unique bool `json:"unique"`
	// Holds the path of a file the process wrote in its step's files
	// directory, which must exist.
	File bool `json:"file"`
	// The attribute of a module's object it is a copy of; see modules.go.
	Source *int64 `json:"source,omitempty"`
}

// Where a board ends.
type boardEnd struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Sent when a human process set an object to a state this process takes it
// in, and left instructions: the work the human reviewed, the state they set
// it to and what they want done. Requests from before states have no state.
type changeRequest struct {
	Reviewer     string     `json:"reviewer"`
	State        string     `json:"state,omitempty"`
	Instructions string     `json:"instructions"`
	Reviewed     []runInput `json:"reviewed"`
}

// A connection is a directed edge between two processes.
type connection struct {
	ID     int64  `json:"id"`
	Source int64  `json:"source"`
	Target int64  `json:"target"`
	Kind   string `json:"kind"`
}

const (
	connectionNext    = ""
	connectionFailure = "failure"
)

const failureSourceMessage = "only an agent, script, Sentry or connector process fails, so only one leads to what handles its failure"

// A flow is a board of its own with everything on it: its processes and
// objects, the boards of its flow processes, its versions and its runs. Each
// flow does one job, such as fixing exceptions or bumping dependencies, or
// is a module, a part of such jobs that other flows reuse; see modules.go.
// Who owns a flow: kind is global, team or user.
type flowOwner struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type flow struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Offered to other flows to run; every flow another runs is one.
	Module bool `json:"module"`
	// How many other flows run it.
	UsedBy int `json:"usedBy"`
	// The repositories its agents and scripts work on, and how its runs'
	// branches are named; see workspace.go.
	Settings  flowSettings `json:"settings"`
	CreatedAt time.Time    `json:"createdAt"`
	// Who sees it and may change it; see owners.go.
	Owner flowOwner `json:"owner"`
	// How far whoever asked may go with it: edit or review.
	Access string `json:"access"`
	// Its latest run of the whole flow, if any.
	LastRun *run `json:"lastRun"`
}

type flowSettings struct {
	Repositories []repository `json:"repositories"`
	// Runs' branches are named <prefix>/run-<id>; empty uses flow/<the flow's name>.
	BranchPrefix string `json:"branchPrefix"`
}

// An agent step that worked in a workspace keeps its pi session there, so
// once it is over a person can carry its conversation on: send it a message,
// such as to push what it committed or to change something, and the agent
// acts on it with everything it did in mind, in the same checkouts on the
// same branch. Each such follow-up is another try of the step's conversation;
// what it does doesn't change what the step generated.
type followUp struct {
	ID int64 `json:"id"`
	// The try of the step's conversation its messages belong to.
	Attempt int    `json:"attempt"`
	Message string `json:"message"`
	// active while it goes, then done or error.
	Status     string        `json:"status"`
	Output     string        `json:"output"`
	Error      string        `json:"error"`
	Commits    []repoCommits `json:"commits"`
	StartedAt  time.Time     `json:"startedAt"`
	FinishedAt *time.Time    `json:"finishedAt"`
}

// A flow process's board as versions keep it.
type innerBoard struct {
	Process int64     `json:"process"`
	Start   startNode `json:"start"`
	End     boardEnd  `json:"end"`
}

// A link ties an object to a process: the process generates it (output) or
// takes it as input. A link naming one of the object's states generates the
// object in that state, or takes it once it enters that state. A human
// process's output link names no state; it lists the states the human
// decides between instead.
type link struct {
	ID        int64   `json:"id"`
	Process   int64   `json:"process"`
	Object    int64   `json:"object"`
	Direction string  `json:"direction"`
	State     *int64  `json:"state"`
	States    []int64 `json:"states"`
	Role      string  `json:"role"`
	Condition bool    `json:"condition"`
	// Which stored records of the object it takes, for a query link.
	Query *linkQuery `json:"query"`
	// Where the link's drawn segments run, if moved by hand; see route.ts.
	Route json.RawMessage `json:"route"`
	// Where the object shows on the process's own board, if moved there by
	// hand; only a flow process has one.
	At *point `json:"at"`
}

type linkQuery struct {
	Where  string `json:"where,omitempty"`
	State  string `json:"state,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Oldest bool   `json:"oldest,omitempty"`
}

// An MCP server as the worker registers it with pi for one agent step.
type mcpRunServer struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

// An object is an OPM object: a named thing that processes generate and take
// as input. Its attributes say what an agent generating it must reply with.
type object struct {
	ID int64 `json:"id"`
	// The flow it is in, on its top board or a flow process's.
	Flow int64   `json:"flow"`
	Name string  `json:"name"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	// Set when resized; null draws the default width.
	Width *float64 `json:"width"`
	// Runs store what they generate it with; see instances.go.
	Persistent bool `json:"persistent"`
	// A list of items: an agent generating it replies with a list, and a
	// flow process taking it runs once per item; see items.go.
	Many bool `json:"many"`
	// How many items the list holds: at least min, and at most max unless
	// it is null; see items.go.
	Min int  `json:"min"`
	Max *int `json:"max"`
	// A person, such as a requester: in OPM a human object, whose input link
	// is an agent link, saying the person carries out the process. Only a
	// human process takes it.
	Human bool `json:"human"`
	// Leads into the end of the flow's top board, as one of what the flow
	// leaves: an object of its top board a person connected to the end.
	// Flows running it take these as its outcomes; see calls.go.
	End bool `json:"end"`
	// The flow process whose board it is on; null on the top board. It also
	// shows inside the flow processes linked to it; see boards.go.
	Parent *int64 `json:"parent"`
	// The object it makes up, as an attribute it exhibits or a part it
	// aggregates, as relation says; it is on that object's board rather than
	// a process's. See structure.go.
	Owner    *int64 `json:"owner"`
	Relation string `json:"relation"`
	// The object it specializes, whose fields it has too.
	General *int64 `json:"general"`
	// The module's object it is a copy of, and the module, which the copy
	// follows; see modules.go.
	Source     *int64        `json:"source"`
	SourceFlow *int64        `json:"sourceFlow"`
	States     []objectState `json:"states"`
	Attributes []attribute   `json:"attributes"`
}

// A state is one of the situations an OPM object can be in, such as a draft
// being approved or having changes requested. Links can name a state: an
// output link generates its object in that state, and an input link is a
// condition link that runs its process when the object enters the state. A
// human process decides by setting its object to one of the states its
// output link lists.
// The tokens a step's model calls took in and gave out, and what they cost.
type stepUsage struct {
	Input  int64   `json:"input"`
	Output int64   `json:"output"`
	Cost   float64 `json:"cost"`
}

type objectState struct {
	ID     int64  `json:"id"`
	Object int64  `json:"object"`
	Name   string `json:"name"`
	// When an object generated in no state is in this one; see rules.go.
	Rule string `json:"rule,omitempty"`
	// The state of a module's object it is a copy of; see modules.go.
	Source *int64 `json:"source,omitempty"`
}

// The contract as the worker's submit_output tool takes it.
type outputSpec struct {
	Shape   string       `json:"shape"`
	Objects []specObject `json:"objects"`
	// The objects the process decides the state of; with any, the output
	// goes under output, next to the decisions.
	Decisions []specDecision `json:"decisions,omitempty"`
}

// An object the agent decides the state of, as one of states.
type specDecision struct {
	Name   string   `json:"name"`
	States []string `json:"states"`
	Review bool     `json:"review"`
}

type point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type process struct {
	ID int64 `json:"id"`
	// The flow it is in, on its top board or a flow process's. Versions
	// leave it out: they belong to the flow.
	Flow   int64  `json:"flow,omitempty"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Prompt string `json:"prompt"`
	Script string `json:"script"`
	// Model ("provider/id") and thinking level for agent processes; empty uses the pi default.
	Model    string  `json:"model"`
	Thinking string  `json:"thinking"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	// Set when resized; null draws the default width.
	Width *float64 `json:"width"`
	// The flow process whose board it is on; null on the top board.
	Parent *int64 `json:"parent"`
	// For flow processes: each time it runs is a run of its own.
	Recording bool            `json:"recording"`
	Settings  processSettings `json:"settings"`
}

// The settings of kinds that take more than a prompt or a script, by kind,
// so switching kinds back and forth loses none.
type processSettings struct {
	Sentry *sentrySettings `json:"sentry,omitempty"`
	// The MCP servers an agent process may use, by id; none by default. See mcp.go.
	MCPServers []int64 `json:"mcpServers,omitempty"`
	// The flow's repositories an agent or script process works on, by name;
	// all of them when absent, none when empty. See workspace.go.
	Repositories *[]string `json:"repositories,omitempty"`
	// The flow a subflow process runs, by id; see calls.go.
	Flow *int64 `json:"flow,omitempty"`
	// How many times an agent deciding states runs in a run before its
	// decisions can no longer send the object back to a process that ran;
	// defaultRounds when absent. See engine.go.
	Rounds *int `json:"rounds,omitempty"`
	// How long an agent, script or Sentry step may take, in seconds, before
	// it fails; the worker's own limit when absent.
	Timeout *int `json:"timeout,omitempty"`
	// How many more times a failed agent, script or Sentry step is tried, as
	// a step of its own each time; none when absent.
	Retries *int `json:"retries,omitempty"`
	// How many of the objects a process takes without waiting for a state
	// it needs before it runs; all of them when absent. See engine.go.
	Quorum *int `json:"quorum,omitempty"`
	// Whether an agent's prompt describes the flow it works in, in OPL; see opl.go.
	DescribeFlow bool `json:"describeFlow,omitempty"`
}

const (
	defaultRounds = 3
	maxRounds     = 20
	maxTimeout    = 24 * 60 * 60
	maxRetries    = 5
)

// What a step did to a repository it worked on.
type repoCommits struct {
	Name string `json:"name"`
	// The commit it started from and the one it left; the same if it committed nothing.
	Before string `json:"before"`
	After  string `json:"after"`
	// The commits it made, newest first, as "<short hash> <subject>"; only
	// the latest ones of many.
	Commits []string `json:"commits"`
	// Whether it left changes it didn't commit.
	Uncommitted bool `json:"uncommitted"`
}

type repository struct {
	// Its checkout's directory in the workspace, and what processes pick it by.
	Name string `json:"name"`
	// Only https, which the worker's credentials cover.
	URL string `json:"url"`
	// The branch runs' branches start from; empty for the default branch.
	Base string `json:"base"`
	// What the worker gets for a re-run: the commit its branch starts from.
	Start string `json:"start,omitempty"`
}

// A run is one execution of the flow from the start node. The engine walks
// the flow and records each process it executes as a step; see engine.go.
type run struct {
	ID int64 `json:"id"`
	// The flow it is a run of.
	Flow   int64  `json:"flow"`
	Status string `json:"status"`
	// The version of the board the run ran on; null for runs from before
	// versions were recorded.
	Version *int64 `json:"version"`
	// For the run of a recording flow process: the run it ran in, and the
	// process, kept after it is deleted. Null for runs of the whole flow. The
	// run of a flow a subflow process ran has a parent in that process's
	// flow, and no process: it ran on its own flow's top board.
	Parent      *int64 `json:"parent"`
	Process     *int64 `json:"process"`
	ProcessName string `json:"processName"`
	// For a run re-running another from one of its processes: that run, and
	// the process.
	RerunOf       *int64 `json:"rerunOf"`
	RerunFrom     *int64 `json:"rerunFrom"`
	RerunFromName string `json:"rerunFromName"`
	// For a run of a flow process for one item of a list: what tells the
	// item apart, its first unique attribute's value or its place in the list.
	ItemKey string `json:"itemKey"`
	// The schedule that started it, if one did; see schedules.go.
	Schedule  *int64 `json:"schedule"`
	StartedBy string `json:"startedBy"`
	// The name of the API key that started it, if one did.
	APIKey     string     `json:"apiKey"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

type schedule struct {
	ID       int64  `json:"id"`
	Flow     int64  `json:"flow"`
	FlowName string `json:"flowName"`
	// Five fields, minute to day of the week, or a descriptor such as
	// @hourly or @every 90m; a minute apart at the least.
	Cron string `json:"cron"`
	// The IANA zone the cron expression's times are in, such as Europe/London.
	Timezone string `json:"timezone"`
	Enabled  bool   `json:"enabled"`
	// When it runs next; null while disabled.
	NextRunAt *time.Time `json:"nextRunAt"`
	// The latest run it started, if any; while it is running, the times due
	// are skipped.
	LastRun   *run      `json:"lastRun"`
	CreatedAt time.Time `json:"createdAt"`
}

// An object a process takes as input, with the output of the process that
// generated it and the state it is in, if any.
type runInput struct {
	Name    string `json:"name"`
	State   string `json:"state,omitempty"`
	Content string `json:"content"`
	// How the process takes it: as an instrument, consumed, or affected,
	// to hand in changed; empty when it just takes it.
	Role string `json:"role,omitempty"`
}

const (
	roleNone        = ""
	roleInstrument  = "instrument"
	roleConsumption = "consumption"
	inputInstrument = "instrument"
	inputConsumed   = "consumed"
	inputAffected   = "affected"
)

// The content and state an object got in a run from the step that generated
// it, or the state a human step set it to.
type runObject struct {
	ID   int64 `json:"id"`
	Step int64 `json:"step"`
	// Kept after the object is deleted, as a step's process is.
	Object  *int64 `json:"object"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Content string `json:"content"`
	// The generated files its content names, which /api/files serves.
	Files []string `json:"files"`
}

type runResult struct {
	OK     bool   `json:"ok"`
	Output string `json:"output"`
	Error  string `json:"error"`
	// What each output object with attributes holds, by object id, when the
	// process generates objects with attributes; the others hold the output.
	// The worker leaves it out.
	Objects map[int64]string `json:"objects,omitempty"`
	// Set by the worker when the agent handed in its output through
	// submit_output, which checked it against the output's interface.
	Submitted bool `json:"submitted,omitempty"`
	// For a step in a workspace: what it did to each repository it worked
	// on, as the worker saw it.
	Commits []repoCommits `json:"commits,omitempty"`
}

type scriptInput struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// What the worker runs.
type scriptRun struct {
	// Identify the process in the worker's logs.
	ProcessID   int64         `json:"processId"`
	ProcessName string        `json:"processName"`
	Script      string        `json:"script"`
	Inputs      []scriptInput `json:"inputs"`
	// Where the script writes the files it generates, which $OUTPUTS points to.
	FilesDir string `json:"filesDir,omitempty"`
	// The workspace it works in, its working directory, if it works on
	// repositories; $WORKSPACE and $BRANCH say where and on which branch.
	Workspace *workspaceSpec `json:"workspace,omitempty"`
	// How long the script may take, if the process has a timeout; the
	// worker's own limit otherwise.
	TimeoutMS int64 `json:"timeoutMs,omitempty"`
	// When its schedule's last run that got through started, as a Unix
	// timestamp, which $SINCE and $LAST_RUN hold.
	Since *int64 `json:"since,omitempty"`
}

type sentrySettings struct {
	Organization string `json:"organization"`
	// Slug or id; empty fetches from all projects the token can see.
	Project     string `json:"project"`
	Environment string `json:"environment"`
	// Sentry's search syntax, e.g. is:unresolved level:error; empty is
	// is:unresolved.
	Query string `json:"query"`
	// How Sentry orders the issues before the limit applies.
	Sort  string `json:"sort"`
	Limit int    `json:"limit"`
}

type snapshot struct {
	Processes   []process           `json:"processes"`
	Objects     []snapshotObject    `json:"objects"`
	States      []objectState       `json:"states"`
	Attributes  []attribute         `json:"attributes"`
	Links       []snapshotLink      `json:"links"`
	LinkStates  []snapshotLinkState `json:"linkStates"`
	Connections []connection        `json:"connections"`
	Start       startNode           `json:"start"`
	// The top board's end; none while it has no place, and in versions from
	// before it had one.
	End *boardEnd `json:"end,omitempty"`
	// Flow processes' boards; versions from before them have none.
	Boards []innerBoard `json:"boards"`
}

type snapshotLink struct {
	ID        int64  `json:"id"`
	Process   int64  `json:"process"`
	Object    int64  `json:"object"`
	Direction string `json:"direction"`
	// 0 when the link names no state, as stored.
	State int64           `json:"state"`
	Route json.RawMessage `json:"route"`
	// Versions from before boundary places have none.
	At *point `json:"at,omitempty"`
}

type snapshotLinkState struct {
	Link  int64 `json:"link"`
	State int64 `json:"state"`
}

type snapshotObject struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	X          float64  `json:"x"`
	Y          float64  `json:"y"`
	Width      *float64 `json:"width"`
	Persistent bool     `json:"persistent"`
	Many       bool     `json:"many"`
	// Left out when unset, as in versions from before lists said how many
	// items they hold.
	Min int  `json:"min,omitempty"`
	Max *int `json:"max,omitempty"`
	// Left out when false, so versions from before people match boards as they are.
	Human bool `json:"human,omitempty"`
	// Left out when false too, as is it in versions from before ends were drawn.
	End    bool   `json:"end,omitempty"`
	Parent *int64 `json:"parent"`
	// Left out when unset, as in versions from before objects were made up
	// of others.
	Owner    *int64 `json:"owner,omitempty"`
	Relation string `json:"relation,omitempty"`
	General  *int64 `json:"general,omitempty"`
	// The module's object it is a copy of; left out for others.
	Source *int64 `json:"source,omitempty"`
}

// An attribute, or an object the object is made of: a JSON object with
// its own attributes, or for a list an array of them.
type specAttribute struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	File        bool            `json:"file"`
	Many        bool            `json:"many,omitempty"`
	MinItems    int             `json:"minItems,omitempty"`
	MaxItems    *int            `json:"maxItems,omitempty"`
	Attributes  []specAttribute `json:"attributes,omitempty"`
}

type specObject struct {
	Name       string          `json:"name"`
	Many       bool            `json:"many"`
	MinItems   int             `json:"minItems,omitempty"`
	MaxItems   *int            `json:"maxItems,omitempty"`
	Attributes []specAttribute `json:"attributes"`
}

// The start node is a board's single UML initial node. It points at no more
// than one process; a flow's top board keeps it in flow_starts.
type startNode struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Target *int64  `json:"target"`
	// Where its link's segments run, if moved by hand; reset when the target changes.
	Route json.RawMessage `json:"route"`
}

// A step is one execution of a process in a run. A process can run several
// times in one run when a human's decision leads back to it. The process's
// name and settings are copied, so the history survives later edits.
type step struct {
	ID int64 `json:"id"`
	// The process's id, kept after it is deleted, since the run's version
	// still has it. Null only for steps whose process was deleted before ids
	// were kept.
	Process     *int64 `json:"process"`
	ProcessName string `json:"processName"`
	Kind        string `json:"kind"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
	// The prompt as sent to the agent, inputs and change request included.
	Prompt        string         `json:"prompt"`
	Inputs        []runInput     `json:"inputs"`
	ChangeRequest *changeRequest `json:"changeRequest"`
	Status        string         `json:"status"`
	Output        string         `json:"output"`
	Error         string         `json:"error"`
	// For human steps: the name of the state the human set, and their
	// instructions. Steps from before states hold approved or changes.
	Outcome      string   `json:"outcome"`
	Instructions string   `json:"instructions"`
	DecidedBy    string   `json:"decidedBy"`
	HandledBy    []string `json:"handledBy"`
	CanDecide    bool     `json:"canDecide"`
	// For a recording flow process: the run its board ran as.
	ChildRun *int64 `json:"childRun"`
	// The branch it worked on in its run's workspace, if it worked in one,
	// and what it did to each repository there.
	Branch  string        `json:"branch"`
	Commits []repoCommits `json:"commits"`
	// For an agent step that kept its session: the pi session's id, which a
	// person can carry the conversation on in, and the follow-ups they sent;
	// see followups.go.
	Session     string     `json:"session"`
	CanFollowUp bool       `json:"canFollowUp"`
	FollowUps   []followUp `json:"followUps"`
	// What an agent step's model calls used, as pi reports it.
	Usage      *stepUsage `json:"usage,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
}

// An agent step records its conversation as pi has it, a message at a time
// while it goes: the prompt, each reply of the model with its thinking and
// tool calls, and each tool's result. The messages are pi's own (see
// docs/message-types.md in pi), trimmed to what is worth keeping.
type stepMessage struct {
	ID int64 `json:"id"`
	// Which try of the step it belongs to, from 1; an agent that didn't hand
	// in its output gets a second.
	Attempt   int             `json:"attempt"`
	Message   json.RawMessage `json:"message"`
	CreatedAt time.Time       `json:"createdAt"`
}

// A version is a saved snapshot of a flow's whole board: its processes, objects
// with their states and attributes, links, connections and start node. Saving
// always adds a version on top, and jumping to one rewrites the board as it
// was, so no version is ever lost. The board keeps its rows' ids across
// jumps: runs refer to processes and objects by id, and persistent objects
// remember what runs stored by object and attribute id.
type version struct {
	ID int64 `json:"id"`
	// The version the board was on when this one was saved, if any; a save
	// after jumping back is based on an older version.
	BasedOn   *int64    `json:"basedOn"`
	CreatedAt time.Time `json:"createdAt"`
}

// What the worker takes for a step: the workspace with the repositories the
// process works on.
type workspaceSpec struct {
	ID     string `json:"id"`
	Branch string `json:"branch"`
	// The run and step working in it, which the worker's commit of what the
	// step left uncommitted names.
	Run          int64        `json:"run,omitempty"`
	Step         int64        `json:"step,omitempty"`
	Repositories []repository `json:"repositories"`
}

// An API key as its owner sees it; the key itself only as it is made.
type apiKey struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	Key        string     `json:"key,omitempty"`
}

// How a flow is started over the API.
type flowAPI struct {
	ID      int64          `json:"id"`
	Name    string         `json:"name"`
	URL     string         `json:"url"`
	Inputs  []apiInput     `json:"inputs"`
	Schema  map[string]any `json:"schema"`
	Problem string         `json:"problem,omitempty"`
}

type apiInput struct {
	Name   string         `json:"name"`
	Many   bool           `json:"many"`
	States []string       `json:"states"`
	Schema map[string]any `json:"schema"`
}

// What a run came to, as the API shows it.
type apiRun struct {
	ID         int64                 `json:"id"`
	Flow       int64                 `json:"flow"`
	Status     string                `json:"status"`
	URL        string                `json:"url"`
	StartedAt  time.Time             `json:"startedAt"`
	FinishedAt *time.Time            `json:"finishedAt"`
	Outcomes   map[string]apiOutcome `json:"outcomes"`
	Errors     []apiStepError        `json:"errors"`
}

type apiOutcome struct {
	State string          `json:"state,omitempty"`
	Value json.RawMessage `json:"value"`
}

type apiStepError struct {
	Process string `json:"process"`
	Error   string `json:"error"`
}
