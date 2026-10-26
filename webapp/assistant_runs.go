package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// What the flow's assistant (assistant.go) is told of the flow's runs, so
// it can explain them: why one failed, what a step took and handed back,
// why a review keeps sending something back. A run the person's message
// names, as "run 412", "#412" or a link to it, is told in full: how it
// started and ended, what changed on the board since its version (diff.go),
// each step with how it ended, how long it took and what it cost, what it
// took and handed back, the decisions people made with their instructions,
// and its log. The "Ask the assistant" actions of a run's panel and of a
// failed step write such a message for the person to send. A message naming
// no run is about those the latest of the person's few messages before it
// named, so a question following up on one keeps it. Besides, the assistant
// gets a summary of the flow's latest runs, trials left out: how they
// ended, the latest failures and decisions.
//
// Only this flow's runs are told: whoever may talk to the assistant may
// change the flow, and so see every run of it, as GET /api/runs/{id} lets
// them. A run of another flow, a subflow's run of its procedure included,
// is told to be none of this flow's, whether or not it exists.
//
// What goes in has a budget, shared between the runs named. A run longer
// than its share is told again with less of it, the least useful cut
// first: what steps that ended well took and handed back, then the worker's
// lines of the log, then what failed steps took, then what changed since
// its version, errors and decisions last; the context says what was cut.
//
// What runs produced comes from outside the app, from people, services and
// agents, so it may hold text written to mislead the assistant: the prompt
// gives it as data, with the tags the prompt wraps things in taken out of
// it, and the rules say to take none of it as instructions. Nor does it
// carry secrets: a step's prompt is left out, being its process's prompt,
// which the setup file holds, with the flow and its inputs, told apart;
// outputs, errors, conversations and what scripts print are kept with the
// step's tokens blanked out (agent.go, script.go), a connector's token goes
// on its call only as it leaves (connectors.go), and what still looks like
// a key or token, such as one a step read and printed, is blanked here.

const (
	// How many runs the person's messages may name for one reply, and how
	// many of the person's messages, latest first, are looked back on for
	// one naming any.
	maxAssistantRuns     = 3
	assistantRunsCarried = 3
	// How many failed runs within a run named, of its flow processes, are
	// told of as runs of their own besides.
	maxAssistantRunsWithin = 2
	// How many bytes the runs told of may take together, shared between
	// them, and the summary of the latest runs. With the setup format, the
	// flow and the conversation, the prompt stays within what the worker
	// takes.
	maxAssistantRunBytes    = 48 << 10
	maxAssistantRecentBytes = 6 << 10
	// How many of the flow's latest runs the summary counts.
	assistantRecentRuns = 20
	// How many characters of a long text are read from the database: of
	// its start, and of its end.
	clipChars = 4000
	// How many of a run's steps, log lines and runs within are read at most.
	maxToldSteps    = 400
	maxToldLogLines = 1000
	maxToldWithin   = 50
)

var (
	// A run's link, as the board's address has it, with one of its steps
	// after it or not: #/flows/3/runs/412/steps/9.
	runLinkPattern = regexp.MustCompile(`#/flows/\d+/runs/(\d+)(?:/steps/(\d+))?`)
	// "run 412", "Run #412", "runs 412, 413 and #414".
	runWordPattern = regexp.MustCompile(`(?i)\bruns?\s+#?(\d+)\b((?:\s*(?:,|&|\band\b|\bor\b)\s*#?\d+\b)*)`)
	// "#412" alone: not within a word, a character reference (&#39;) or a link.
	runHashPattern = regexp.MustCompile(`(?:^|[^\w&/#])#(\d+)\b`)
	digitsPattern  = regexp.MustCompile(`\d+`)
)

// The runs a message names, in the order it names them, and the steps its
// links name.
func namedRuns(text string) (runs, steps []int64) {
	type named struct {
		at int
		id int64
	}
	var found []named
	add := func(at int, s string) {
		if id, err := strconv.ParseInt(s, 10, 64); err == nil && id > 0 {
			found = append(found, named{at, id})
		}
	}
	for _, m := range runLinkPattern.FindAllStringSubmatchIndex(text, -1) {
		add(m[2], text[m[2]:m[3]])
		if m[4] >= 0 {
			if id, err := strconv.ParseInt(text[m[4]:m[5]], 10, 64); err == nil {
				steps = append(steps, id)
			}
		}
	}
	for _, m := range runWordPattern.FindAllStringSubmatchIndex(text, -1) {
		add(m[2], text[m[2]:m[3]])
		for _, d := range digitsPattern.FindAllStringIndex(text[m[4]:m[5]], -1) {
			add(m[4]+d[0], text[m[4]+d[0]:m[4]+d[1]])
		}
	}
	for _, m := range runHashPattern.FindAllStringSubmatchIndex(text, -1) {
		add(m[2], text[m[2]:m[3]])
	}
	slices.SortStableFunc(found, func(a, b named) int { return a.at - b.at })
	for _, n := range found {
		if !slices.Contains(runs, n.id) {
			runs = append(runs, n.id)
		}
	}
	return runs, steps
}

// The runs a reply is about, and the steps links name in them: those the
// person's latest message names, else those the latest of the few before
// it to name any named; at most maxAssistantRuns. about is what the person
// said of them, which names the processes they ask about.
func askedRuns(history []assistantMessage) (runs, steps []int64, about string) {
	seen := 0
	for i := len(history) - 1; i >= 0 && seen < assistantRunsCarried; i-- {
		m := history[i]
		if m.Role != rolePerson {
			continue
		}
		seen++
		if seen == 1 {
			about = m.Content
		}
		r, s := namedRuns(m.Content)
		if len(r) == 0 {
			continue
		}
		if seen > 1 {
			about += "\n" + m.Content
		}
		if len(r) > maxAssistantRuns {
			r = r[:maxAssistantRuns]
		}
		return r, s, about
	}
	return nil, nil, about
}

// A text as read from the database: whole, or for a long one its start and
// its end, or its end alone when only that was read.
type clipped struct {
	head, tail string
	// Its length in characters.
	n int
}

// A text read as LEFT(x, clipChars), RIGHT(x, clipChars), CHAR_LENGTH(x).
func readClipped(head, tail string, n int) clipped {
	if n <= utf8.RuneCountInString(head) {
		return clipped{head: head, n: n}
	}
	return clipped{head: head, tail: tail, n: n}
}

func wholeText(s string) clipped {
	return clipped{head: s, n: utf8.RuneCountInString(s)}
}

// The text in at most about max characters: whole if it fits, else its
// start and its end, saying how much is cut between them.
func (c clipped) cut(max int) string {
	if c.n <= max && c.tail == "" {
		return c.head
	}
	if max <= 0 {
		return fmt.Sprintf("(%d characters, left out)", c.n)
	}
	keepHead := max * 2 / 3
	if c.head == "" {
		keepHead = 0
	}
	keepTail := max - keepHead
	head := []rune(c.head)
	tail := head
	if c.tail != "" {
		tail = []rune(c.tail)
	}
	keepHead, keepTail = min(keepHead, len(head)), min(keepTail, len(tail))
	return fmt.Sprintf("%s[…%d characters cut…]%s", spaced(string(head[:keepHead]), " "), c.n-keepHead-keepTail, spaced(" ", string(tail[len(tail)-keepTail:])))
}

// a and b joined, or nothing when either is empty.
func spaced(a, b string) string {
	if a == "" || b == "" {
		return ""
	}
	return a + b
}

// A value a step took or handed back.
type toldValue struct {
	name, state string
	content     clipped
}

func (v toldValue) label() string {
	if v.state != "" {
		return v.name + " (" + v.state + ")"
	}
	return v.name
}

// A step of a run, as the assistant is told it.
type toldStep struct {
	id                               int64
	name, kind, status               string
	output, err                      clipped
	outcome, instructions, decidedBy string
	// For a loop step: what it asked.
	asked    string
	childRun *int64
	// For a step a review sent back: who, the state they set and what they
	// asked for.
	review        *changeRequest
	slotWait, try *int64
	started       time.Time
	finished      *time.Time
	usage         *stepUsage
	took, handed  []toldValue
	// For a failed script or agent step: what it printed or said last.
	last     clipped
	lastWhat string
	// Whether the person asks about it, by its process's name or a link.
	focused bool
}

// A line of a run's log.
type toldLog struct {
	step                   string
	source, level, message string
	at                     time.Time
}

// A run, as the assistant is told it.
type toldRun struct {
	id int64
	// Not a run of this flow, or none: all that is told of it.
	none bool
	run  run
	// Whether the flow publishes the version it ran on, and what changed from
	// that version to the board as it is, nil when nothing did, with the two
	// boards to name what it names.
	published   bool
	changes     *boardDiff
	before, now snapshot
	spent       float64
	limit       float64
	steps       []toldStep
	moreSteps   int
	logs        []toldLog
	moreLogs    int
	// The runs within it, of its flow processes.
	within []run
}

// Reads what the assistant is told of run id, if a run of flow. steps are
// those links name, and about what the person said, naming the processes
// they ask about.
func loadToldRun(ctx context.Context, db *sql.DB, flow, id int64, steps []int64, about string) (toldRun, error) {
	t := toldRun{id: id}
	rn, err := getRun(ctx, db, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && rn.Flow != flow {
		t.none = true
		return t, nil
	}
	if err != nil {
		return t, err
	}
	t.run = rn
	if t.spent, t.limit, err = runSpent(ctx, db, id); err != nil {
		return t, err
	}
	if err := t.loadVersion(ctx, db); err != nil {
		return t, err
	}
	if err := t.loadSteps(ctx, db, steps, about); err != nil {
		return t, err
	}
	if err := t.loadLog(ctx, db); err != nil {
		return t, err
	}
	rows, err := db.QueryContext(ctx, "SELECT "+runColumns+" FROM runs WHERE parent_run_id = ? AND flow_id = ? ORDER BY id LIMIT ?", id, flow, maxToldWithin)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	for rows.Next() {
		var c run
		if err := scanRun(rows, &c); err != nil {
			return t, err
		}
		t.within = append(t.within, c)
	}
	return t, rows.Err()
}

// What changed from the run's version to the board as it is. Samples are
// left out, as the assistant never sees them, and so is layout.
func (t *toldRun) loadVersion(ctx context.Context, db *sql.DB) error {
	if t.run.Version == nil {
		return nil
	}
	return inTx(ctx, db, func(tx *sql.Tx) error {
		var body []byte
		var published *int64
		if err := tx.QueryRowContext(ctx, `SELECT v.snapshot, f.published_version_id FROM flow_versions v JOIN flows f ON f.id = v.flow_id
			WHERE v.id = ?`, *t.run.Version).Scan(&body, &published); err != nil {
			return err
		}
		t.published = published != nil && *published == *t.run.Version
		if err := json.Unmarshal(body, &t.before); err != nil {
			return err
		}
		var err error
		if t.now, err = takeSnapshot(ctx, tx, t.run.Flow); err != nil {
			return err
		}
		d := diffSnapshots(t.before, t.now)
		d.Samples, d.Layout = nil, false
		if d.changed() {
			t.changes = &d
		}
		return nil
	})
}

// The columns reading a long text column as readClipped takes it.
func clippedColumns(col string) string {
	n := strconv.Itoa(clipChars)
	return "LEFT(" + col + ", " + n + "), RIGHT(" + col + ", " + n + "), CHAR_LENGTH(" + col + ")"
}

func (t *toldRun) loadSteps(ctx context.Context, db *sql.DB, linked []int64, about string) error {
	if err := db.QueryRowContext(ctx, "SELECT GREATEST(COUNT(*) - ?, 0) FROM run_steps WHERE run_id = ?", maxToldSteps, t.id).Scan(&t.moreSteps); err != nil {
		return err
	}
	byID := map[int64]int{}
	err := scanAll(ctx, db, `SELECT id, process_name, kind, status, `+clippedColumns("output")+`, `+clippedColumns("error")+`,
			outcome, instructions, `+userLabelColumn("decided_by")+`, child_run_id, IF(kind = ?, prompt, ''),
			JSON_UNQUOTE(JSON_EXTRACT(change_request, '$.reviewer')), JSON_UNQUOTE(JSON_EXTRACT(change_request, '$.state')),
			JSON_UNQUOTE(JSON_EXTRACT(change_request, '$.instructions')), slot_wait_ms, try, started_at, finished_at
		FROM run_steps WHERE run_id = ? ORDER BY id LIMIT ?`, func(rows *sql.Rows) error {
		var s toldStep
		var outHead, outTail, errHead, errTail string
		var outN, errN int
		var reviewer, state, instructions sql.NullString
		if err := rows.Scan(&s.id, &s.name, &s.kind, &s.status, &outHead, &outTail, &outN, &errHead, &errTail, &errN,
			&s.outcome, &s.instructions, &s.decidedBy, &s.childRun, &s.asked, &reviewer, &state, &instructions,
			&s.slotWait, &s.try, &s.started, &s.finished); err != nil {
			return err
		}
		s.output, s.err = readClipped(outHead, outTail, outN), readClipped(errHead, errTail, errN)
		if reviewer.Valid {
			s.review = &changeRequest{Reviewer: reviewer.String, State: state.String, Instructions: instructions.String}
		}
		s.focused = slices.Contains(linked, s.id) || namesProcess(about, s.name)
		byID[s.id] = len(t.steps)
		t.steps = append(t.steps, s)
		return nil
	}, kindLoop, t.id, maxToldSteps)
	if err != nil {
		return err
	}
	err = scanAll(ctx, db, `SELECT s.id, j.name, COALESCE(j.state, ''), `+clippedColumns("j.content")+`
		FROM run_steps s, JSON_TABLE(s.inputs, '$[*]' COLUMNS (i FOR ORDINALITY, name varchar(1024) PATH '$.name',
			state varchar(1024) PATH '$.state', content longtext PATH '$.content')) j
		WHERE s.run_id = ? ORDER BY s.id, j.i`, func(rows *sql.Rows) error {
		var step int64
		var v toldValue
		var head, tail string
		var n int
		if err := rows.Scan(&step, &v.name, &v.state, &head, &tail, &n); err != nil {
			return err
		}
		if i, ok := byID[step]; ok {
			v.content = readClipped(head, tail, n)
			t.steps[i].took = append(t.steps[i].took, v)
		}
		return nil
	}, t.id)
	if err != nil {
		return err
	}
	err = scanAll(ctx, db, `SELECT step_id, name, state, `+clippedColumns("content")+` FROM run_objects WHERE run_id = ? ORDER BY id`,
		func(rows *sql.Rows) error {
			var step int64
			var v toldValue
			var head, tail string
			var n int
			if err := rows.Scan(&step, &v.name, &v.state, &head, &tail, &n); err != nil {
				return err
			}
			if i, ok := byID[step]; ok {
				v.content = readClipped(head, tail, n)
				t.steps[i].handed = append(t.steps[i].handed, v)
			}
			return nil
		}, t.id)
	if err != nil {
		return err
	}
	usage, err := runUsage(ctx, db, t.id)
	if err != nil {
		return err
	}
	for i := range t.steps {
		s := &t.steps[i]
		s.usage = usage[s.id]
		if s.status != stepError {
			continue
		}
		switch s.kind {
		case kindScript:
			s.last, s.lastWhat, err = printedLast(ctx, db, s.id)
		case kindAgent:
			s.last, s.lastWhat, err = saidLast(ctx, db, s.id)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Whether what the person said names the process, as a word of its own.
func namesProcess(about, process string) bool {
	process = strings.TrimSpace(process)
	if process == "" {
		return false
	}
	re, err := regexp.Compile(`(?i)(^|\W)` + regexp.QuoteMeta(process) + `($|\W)`)
	return err == nil && re.MatchString(about)
}

// The end of what a script step printed, stdout and stderr as they came.
func printedLast(ctx context.Context, db *sql.DB, step int64) (clipped, string, error) {
	var parts []string
	size, n := 0, 0
	err := scanAll(ctx, db, `SELECT RIGHT(text, ?), CHAR_LENGTH(text) FROM run_step_output WHERE step_id = ? ORDER BY id DESC LIMIT 200`,
		func(rows *sql.Rows) error {
			var text string
			var length int
			if err := rows.Scan(&text, &length); err != nil {
				return err
			}
			n += length
			if size < clipChars {
				parts = append(parts, text)
				size += utf8.RuneCountInString(text)
			}
			return nil
		}, clipChars, step)
	if err != nil || n == 0 {
		return clipped{}, "", err
	}
	slices.Reverse(parts)
	tail := []rune(strings.Join(parts, ""))
	if len(tail) > clipChars {
		tail = tail[len(tail)-clipChars:]
	}
	if n <= len(tail) {
		return wholeText(string(tail)), "What it printed last", nil
	}
	return clipped{tail: string(tail), n: n}, "What it printed last", nil
}

// How many of an agent step's latest messages are read, and the largest
// told whole.
const (
	saidMessages  = 8
	saidLargest   = 30000
	saidToolChars = 300
)

// The end of an agent step's conversation: what it said, the tools it
// called and what they answered, not its prompt.
func saidLast(ctx context.Context, db *sql.DB, step int64) (clipped, string, error) {
	var lines []string
	err := scanAll(ctx, db, `SELECT JSON_UNQUOTE(JSON_EXTRACT(message, '$.role')), LEFT(message, ?), CHAR_LENGTH(message)
		FROM run_step_messages WHERE step_id = ? ORDER BY id DESC LIMIT ?`, func(rows *sql.Rows) error {
		var role sql.NullString
		var body string
		var n int
		if err := rows.Scan(&role, &body, &n); err != nil {
			return err
		}
		if role.String != "assistant" && role.String != "toolResult" {
			return nil
		}
		if n > saidLargest {
			lines = append(lines, fmt.Sprintf("(a message of %d characters, left out)", n))
			return nil
		}
		if line := conversationLine(body); line != "" {
			lines = append(lines, line)
		}
		return nil
	}, saidLargest, step, saidMessages)
	if err != nil || len(lines) == 0 {
		return clipped{}, "", err
	}
	slices.Reverse(lines)
	return wholeText(strings.Join(lines, "\n")), "The end of its conversation", nil
}

// A message of an agent's conversation in a line or a few: what it said
// and the tools it called, or what a tool answered.
func conversationLine(body string) string {
	var m struct {
		Role         string          `json:"role"`
		Content      json.RawMessage `json:"content"`
		ErrorMessage string          `json:"errorMessage"`
		ToolName     string          `json:"toolName"`
		IsError      bool            `json:"isError"`
	}
	if json.Unmarshal([]byte(body), &m) != nil {
		return ""
	}
	var blocks []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	var text string
	if json.Unmarshal(m.Content, &text) != nil {
		json.Unmarshal(m.Content, &blocks)
	}
	var parts []string
	if text != "" {
		parts = append(parts, text)
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "toolCall":
			parts = append(parts, fmt.Sprintf("called %s %s", b.Name, wholeText(string(b.Arguments)).cut(saidToolChars)))
		}
	}
	said := strings.TrimSpace(strings.Join(parts, "\n"))
	switch m.Role {
	case "toolResult":
		how := "answered"
		if m.IsError {
			how = "failed"
		}
		return fmt.Sprintf("%s %s: %s", m.ToolName, how, wholeText(said).cut(saidToolChars*2))
	default:
		if m.ErrorMessage != "" {
			said = strings.TrimSpace(said + "\n(the model failed: " + m.ErrorMessage + ")")
		}
		if said == "" {
			return ""
		}
		return "the agent: " + said
	}
}

func (t *toldRun) loadLog(ctx context.Context, db *sql.DB) error {
	names := map[int64]string{}
	for _, s := range t.steps {
		names[s.id] = s.name
	}
	if err := db.QueryRowContext(ctx, "SELECT GREATEST(COUNT(*) - ?, 0) FROM run_logs WHERE run_id = ?", maxToldLogLines, t.id).Scan(&t.moreLogs); err != nil {
		return err
	}
	return scanAll(ctx, db, `SELECT step_id, source, level, LEFT(message, ?), created_at FROM run_logs WHERE run_id = ? ORDER BY id LIMIT ?`,
		func(rows *sql.Rows) error {
			var l toldLog
			var step *int64
			if err := rows.Scan(&step, &l.source, &l.level, &l.message, &l.at); err != nil {
				return err
			}
			if step != nil {
				l.step = names[*step]
			}
			t.logs = append(t.logs, l)
			return nil
		}, clipChars, t.id, maxToldLogLines)
}

// Which lines of a run's log are told.
const (
	logsAll = iota
	logsWebapp
	logsErrors
	logsNone
)

// How much of a run is told, from the most to the least; a run longer than
// its share of the budget is told at the next level, until it fits.
type runLevel struct {
	// The characters of each value a step that ended well took or handed
	// back, 0 for its name alone; and of each a step that failed, or the
	// person asks about, took or handed back, and of its output.
	value, focused int
	// Of what a failed step printed or said last; 0 for none.
	last int
	// Of each error, and of a person's instructions.
	err int
	// Which lines of the log, and the characters of each.
	logs, logLine int
	// Of what each field of the board was and is since the version; 0 for
	// the fields' names alone.
	diff int
	// Whether steps that ended well are told in a line each.
	brief bool
	// What the level leaves out, said in the context.
	cut string
}

var runLevels = []runLevel{
	{value: 1500, focused: 4000, last: 4000, err: 4000, logs: logsAll, logLine: 400, diff: 1500},
	{value: 300, focused: 3000, last: 3000, err: 4000, logs: logsWebapp, logLine: 300, diff: 1000,
		cut: "what steps that ended well took and handed back is cut to 300 characters each, and the worker's lines are left out of the log"},
	{value: 0, focused: 1500, last: 1500, err: 3000, logs: logsErrors, logLine: 300, diff: 400,
		cut: "what steps that ended well took and handed back is named alone, what failed steps took, handed back and said last is cut to 1500 characters, and the log keeps its errors alone"},
	{value: 0, focused: 400, last: 0, err: 2000, logs: logsErrors, logLine: 200, diff: 100, brief: true,
		cut: "steps that ended well are told in a line each, what failed steps took and handed back is cut to 400 characters, what they said last is left out, the log keeps its errors alone, and what changed since the version is cut to 100 characters a field"},
	{value: 0, focused: 0, last: 0, err: 1000, logs: logsNone, diff: 0, brief: true,
		cut: "steps that ended well are told in a line each, what steps took and handed back is named alone, errors and instructions are cut to 1000 characters, the log is left out, and what changed since the version is named alone"},
}

// The run told in at most budget bytes, as much of it as fits.
func fitRun(t toldRun, budget int) string {
	for i, lv := range runLevels {
		s := renderRun(t, lv)
		if len(s) <= budget {
			return s
		}
		if i == len(runLevels)-1 {
			return cutBytes(s, budget) + "\n(The rest of the run is cut, to fit.)\n"
		}
	}
	return ""
}

// The start of s in at most n bytes, cut between characters.
func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

const toldTime = "2006-01-02 15:04:05 UTC"

func toldSpan(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(time.Second).String()
}

func toldDollars(d float64) string {
	if d > 0 && d < 0.01 {
		return "less than $0.01"
	}
	return fmt.Sprintf("$%.2f", d)
}

// How the run came to be.
func (t toldRun) origin() string {
	r := t.run
	switch {
	case r.TrialOf != nil:
		return fmt.Sprintf("a trial of %s alone, on inputs a person gave it: nothing before or after it ran, and what it made isn't kept for the flow's real runs", quote(r.TrialOfName))
	case r.Parent != nil && r.Process != nil:
		s := fmt.Sprintf("the run of the flow process %s within run #%d", quote(r.ProcessName), *r.Parent)
		if r.ItemKey != "" {
			s += fmt.Sprintf(", for the item %s", quote(r.ItemKey))
		}
		return s
	case r.Parent != nil:
		return fmt.Sprintf("a run of this flow by a subflow process in run #%d, of another flow", *r.Parent)
	}
	var s string
	switch {
	case r.RerunOf != nil:
		s = fmt.Sprintf("a re-run of run #%d from %s", *r.RerunOf, quote(r.RerunFromName))
	case r.Record != nil:
		s = fmt.Sprintf("a run on the shared record #%d", *r.Record)
	case r.APIKey != "":
		s = fmt.Sprintf("a run started through the API key %s", quote(r.APIKey))
	case r.Schedule != nil:
		s = fmt.Sprintf("a run started by schedule #%d", *r.Schedule)
	default:
		s = "a run started from the board"
	}
	if r.StartedBy != "" {
		s += " by " + r.StartedBy
	}
	return s
}

func runStatusText(r run) string {
	switch {
	case r.Status == runRunning && r.Waiting:
		return "running, waiting for a person"
	case r.Status == runDone:
		return "done"
	}
	return r.Status
}

func renderRun(t toldRun, lv runLevel) string {
	var b strings.Builder
	if t.none {
		fmt.Fprintf(&b, "Run #%d is no run of this flow.\n", t.id)
		return b.String()
	}
	r := t.run
	fmt.Fprintf(&b, "Run #%d, %s: %s.\n", r.ID, runStatusText(r), t.origin())
	fmt.Fprintf(&b, "Started %s", r.StartedAt.UTC().Format(toldTime))
	if r.FinishedAt != nil {
		fmt.Fprintf(&b, ", ended %s, after %s", r.FinishedAt.UTC().Format(toldTime), toldSpan(r.FinishedAt.Sub(r.StartedAt)))
	}
	b.WriteString(".")
	if t.spent > 0 || t.limit > 0 {
		fmt.Fprintf(&b, " It spent %s on its models", toldDollars(t.spent))
		if t.limit > 0 {
			fmt.Fprintf(&b, ", of the %s it may", toldDollars(t.limit))
		}
		b.WriteString(".")
	}
	b.WriteString("\n")
	t.renderVersion(&b, lv)

	if len(t.steps) == 0 {
		b.WriteString("No process ran in it.\n")
	} else {
		b.WriteString("Its steps, in the order they started:\n")
		within := map[int64]run{}
		for _, c := range t.within {
			within[c.ID] = c
		}
		told := map[string]string{}
		for i, s := range t.steps {
			s.render(&b, i+1, lv, within, told)
		}
		if t.moreSteps > 0 {
			fmt.Fprintf(&b, "(%d later steps are left out.)\n", t.moreSteps)
		}
	}
	if len(t.within) > 0 {
		b.WriteString("Runs within it, of its flow processes:")
		for _, c := range t.within {
			fmt.Fprintf(&b, " #%d (%s", c.ID, c.ProcessName)
			if c.ItemKey != "" {
				fmt.Fprintf(&b, ", item %s", quote(c.ItemKey))
			}
			fmt.Fprintf(&b, "): %s;", runStatusText(c))
		}
		b.WriteString("\n")
	}
	t.renderLog(&b, lv)
	if lv.cut != "" {
		fmt.Fprintf(&b, "To fit, %s.\n", lv.cut)
	}
	return b.String()
}

func (t toldRun) renderVersion(b *strings.Builder, lv runLevel) {
	v := t.run.Version
	switch {
	case v == nil:
		b.WriteString("It ran before versions were kept, so what changed on the board since isn't known.\n")
		return
	case t.published:
		fmt.Fprintf(b, "It ran on version %d, the one the flow publishes", *v)
	default:
		fmt.Fprintf(b, "It ran on version %d", *v)
	}
	if t.changes == nil {
		b.WriteString(", which is the board as it is now.\n")
		return
	}
	fmt.Fprintf(b, ". The board has changed since; what changed from version %d to the board as it is now:\n", *v)
	renderChanges(b, *t.changes, t.before, t.now, lv.diff)
}

// How many of the changes since a run's version are told.
const maxToldChanges = 60

// What changed on a board, a line each, with what each field was and is,
// cut to max characters, or named alone for 0.
func renderChanges(b *strings.Builder, d boardDiff, before, now snapshot, max int) {
	told := 0
	objectName := func(s snapshot, id *int64) string {
		if id == nil {
			return ""
		}
		for _, o := range s.Objects {
			if o.ID == *id {
				return o.Name
			}
		}
		return ""
	}
	item := func(kind string, it diffItem) {
		told++
		if told > maxToldChanges {
			return
		}
		board := now
		if it.Change == diffRemoved {
			board = before
		}
		fmt.Fprintf(b, "- %s %s", kind, quote(it.Name))
		if o := objectName(board, it.Object); o != "" && (kind == "state" || kind == "attribute") {
			fmt.Fprintf(b, " of %s", quote(o))
		}
		if kind == "process" || kind == "object" {
			fmt.Fprintf(b, ", on %s", boardName(board, it.Board))
		}
		fmt.Fprintf(b, ": %s", it.Change)
		renderFields(b, it.Fields, max)
		b.WriteString("\n")
	}
	for _, list := range []struct {
		kind  string
		items []diffItem
	}{{"process", d.Processes}, {"object", d.Objects}, {"state", d.States}, {"attribute", d.Attributes}, {"link", d.Links}, {"connection", d.Connections}} {
		for _, it := range list.items {
			item(list.kind, it)
		}
	}
	if len(d.Settings) > 0 {
		b.WriteString("- the flow's settings: changed")
		renderFields(b, d.Settings, max)
		b.WriteString("\n")
	}
	if told > maxToldChanges {
		fmt.Fprintf(b, "(%d more changes are left out.)\n", told-maxToldChanges)
	}
}

func renderFields(b *strings.Builder, fs []diffField, max int) {
	if len(fs) == 0 {
		return
	}
	if max == 0 {
		var names []string
		for _, f := range fs {
			names = append(names, f.Field)
		}
		fmt.Fprintf(b, " (%s)", strings.Join(names, ", "))
		return
	}
	for _, f := range fs {
		fmt.Fprintf(b, "; %s was %s, is %s", f.Field, quote(wholeText(f.Before).cut(max)), quote(wholeText(f.After).cut(max)))
	}
}

// How long a value is, at the least, to be told once and referred to after.
const toldOnce = 80

// Tells the step, the nth of its run. told holds the values steps before it
// took or handed back, by what they hold, and where they were told, so a
// value passed on is referred to rather than told again.
func (s toldStep) render(b *strings.Builder, n int, lv runLevel, within map[int64]run, told map[string]string) {
	failed := s.status == stepError
	focus := failed || s.focused
	fmt.Fprintf(b, "%d. %s (%s", n, s.name, s.kind)
	if s.try != nil && *s.try > 1 {
		fmt.Fprintf(b, ", try %d", *s.try)
	}
	b.WriteString("): ")
	switch s.status {
	case stepDone:
		b.WriteString("done")
	case stepError:
		b.WriteString("failed")
	case stepActive:
		b.WriteString("going")
	case stepWaiting:
		b.WriteString("waiting for a person")
	case stepSkipped:
		b.WriteString("skipped")
	case stepDuplicate:
		b.WriteString("found what it generated already processed")
	default:
		b.WriteString(s.status)
	}
	if s.kind == kindCopied {
		b.WriteString(", copied from the run it re-runs")
	}
	if s.kind == kindInput {
		b.WriteString(", what the run was given")
	}
	if s.finished != nil && s.kind != kindCopied && s.kind != kindInput {
		fmt.Fprintf(b, " after %s", toldSpan(s.finished.Sub(s.started)))
	} else if s.finished == nil {
		fmt.Fprintf(b, " since %s", s.started.UTC().Format(toldTime))
	}
	if s.slotWait != nil && *s.slotWait >= 1000 {
		fmt.Fprintf(b, ", having waited %s for a slot", toldSpan(time.Duration(*s.slotWait)*time.Millisecond))
	}
	if s.usage != nil {
		fmt.Fprintf(b, "; its model calls cost %s (%d tokens in, %d out)", toldDollars(s.usage.Cost), s.usage.Input, s.usage.Output)
	}
	if s.focused {
		b.WriteString("; the person asks about it")
	}
	b.WriteString(".\n")

	if s.review != nil {
		fmt.Fprintf(b, "   It ran again, sent back by a review: %s", s.review.Reviewer)
		if s.review.State != "" {
			fmt.Fprintf(b, " set %s and", s.review.State)
		}
		fmt.Fprintf(b, " asked for %s\n", quote(wholeText(s.review.Instructions).cut(lv.err)))
	}
	if s.kind == kindLoop {
		fmt.Fprintf(b, "   Asked: %s\n", quote(wholeText(s.asked).cut(lv.err)))
		if s.decidedBy != "" {
			fmt.Fprintf(b, "   Answered by %s.\n", s.decidedBy)
		}
	}
	if s.kind == kindHuman && (s.outcome != "" || s.decidedBy != "") {
		fmt.Fprintf(b, "   Decision")
		if s.decidedBy != "" {
			fmt.Fprintf(b, " by %s", s.decidedBy)
		}
		if s.outcome != "" {
			fmt.Fprintf(b, ": %s", s.outcome)
		}
		if s.instructions != "" {
			fmt.Fprintf(b, ", with the instructions %s", quote(wholeText(s.instructions).cut(lv.err)))
		}
		b.WriteString("\n")
	}
	if s.childRun != nil {
		fmt.Fprintf(b, "   Ran as run #%d", *s.childRun)
		if c, ok := within[*s.childRun]; ok {
			fmt.Fprintf(b, " (%s)", runStatusText(c))
		}
		b.WriteString(".\n")
	}
	if s.err.n > 0 {
		label := "Error"
		switch s.status {
		case stepSkipped:
			label = "Why"
		case stepDuplicate:
			label = "Duplicate"
		}
		fmt.Fprintf(b, "   %s: %s\n", label, s.err.cut(lv.err))
	}
	size := lv.value
	if focus {
		size = lv.focused
	}
	values := func(what string, vs []toldValue) {
		if len(vs) == 0 {
			return
		}
		if size == 0 {
			var names []string
			for _, v := range vs {
				names = append(names, v.label())
			}
			fmt.Fprintf(b, "   %s %s.\n", what, strings.Join(names, ", "))
			return
		}
		for _, v := range vs {
			key := fmt.Sprintf("%d\x00%s\x00%s", v.content.n, v.content.head, v.content.tail)
			if where, ok := told[key]; ok && v.content.n >= toldOnce {
				fmt.Fprintf(b, "   %s %s: the same as %s\n", what, v.label(), where)
				continue
			}
			fmt.Fprintf(b, "   %s %s: %s\n", what, v.label(), v.content.cut(size))
			if size >= toldOnce {
				told[key] = fmt.Sprintf("what %d. %s %s", n, s.name, strings.ToLower(what))
			}
		}
	}
	// Told in a line, a step that ended well names what it handed back.
	if lv.brief && !focus {
		values("Handed back", s.handed)
		return
	}
	values("Took", s.took)
	values("Handed back", s.handed)
	if len(s.handed) == 0 && s.output.n > 0 && s.kind != kindLoop {
		if size == 0 {
			fmt.Fprintf(b, "   Its output: %d characters.\n", s.output.n)
		} else {
			fmt.Fprintf(b, "   Its output: %s\n", s.output.cut(size))
		}
	}
	if s.last.n > 0 && lv.last > 0 {
		fmt.Fprintf(b, "   %s:\n%s\n", s.lastWhat, s.last.cut(lv.last))
	}
}

func (t toldRun) renderLog(b *strings.Builder, lv runLevel) {
	if lv.logs == logsNone || len(t.logs) == 0 {
		return
	}
	var lines []toldLog
	for _, l := range t.logs {
		switch {
		case lv.logs == logsWebapp && l.source != logWebapp:
		case lv.logs == logsErrors && l.level != logError:
		default:
			lines = append(lines, l)
		}
	}
	switch lv.logs {
	case logsAll:
		fmt.Fprintf(b, "Its log, %d lines:\n", len(t.logs)+t.moreLogs)
	case logsWebapp:
		fmt.Fprintf(b, "Its log, %d lines, the webapp's %d alone:\n", len(t.logs)+t.moreLogs, len(lines))
	case logsErrors:
		fmt.Fprintf(b, "Its log, %d lines, the %d errors alone:\n", len(t.logs)+t.moreLogs, len(lines))
	}
	for _, l := range lines {
		fmt.Fprintf(b, "%s", l.at.UTC().Format("15:04:05"))
		if l.step != "" {
			fmt.Fprintf(b, " [%s]", l.step)
		}
		if l.source == logWorker {
			b.WriteString(" worker:")
		}
		if l.level == logError {
			b.WriteString(" ERROR:")
		}
		fmt.Fprintf(b, " %s\n", wholeText(l.message).cut(lv.logLine))
	}
	if t.moreLogs > 0 {
		fmt.Fprintf(b, "(%d later lines are left out.)\n", t.moreLogs)
	}
}

// The flow's latest runs, as the assistant is told them.
type recentRuns struct {
	runs []run
	// The latest steps that failed, and people's latest decisions, in the
	// latest runs and those within them.
	failed, decided []recentStep
}

type recentStep struct {
	run                        int64
	parent                     *int64
	name, kind, status         string
	err, outcome, instructions string
	decidedBy                  string
	at                         time.Time
}

// How many of the latest failures and decisions the summary tells.
const recentSteps = 8

func loadRecentRuns(ctx context.Context, db *sql.DB, flow int64) (recentRuns, error) {
	var rr recentRuns
	// Runs at their top within the flow: a procedure's are started by
	// subflow processes in runs of other flows.
	rows, err := db.QueryContext(ctx, "SELECT "+runColumns+` FROM runs WHERE flow_id = ? AND trial_of IS NULL
		AND NOT EXISTS (SELECT 1 FROM runs p WHERE p.id = runs.parent_run_id AND p.flow_id = runs.flow_id) ORDER BY id DESC LIMIT ?`,
		flow, assistantRecentRuns)
	if err != nil {
		return rr, err
	}
	defer rows.Close()
	for rows.Next() {
		var r run
		if err := scanRun(rows, &r); err != nil {
			return rr, err
		}
		rr.runs = append(rr.runs, r)
	}
	if err := rows.Err(); err != nil || len(rr.runs) == 0 {
		return rr, err
	}
	rows.Close()
	// Runs within a run start after it, so those of the latest runs are
	// among the flow's runs since the earliest of them; those within
	// trials are left out with the trials.
	since := rr.runs[len(rr.runs)-1].ID
	read := func(cond string, into *[]recentStep) error {
		return scanAll(ctx, db, `SELECT s.run_id, r.parent_run_id, s.process_name, s.kind, s.status, LEFT(s.error, 600), s.outcome,
				LEFT(s.instructions, 600), `+userLabelColumn("s.decided_by")+`, COALESCE(s.finished_at, s.started_at)
			FROM run_steps s JOIN runs r ON r.id = s.run_id
			WHERE r.flow_id = ? AND r.id >= ? AND r.trial_of IS NULL
				AND (r.parent_run_id IS NULL OR r.parent_run_id NOT IN (SELECT id FROM runs WHERE trial_of IS NOT NULL))
				AND `+cond+` ORDER BY s.id DESC LIMIT ?`, func(rows *sql.Rows) error {
			var s recentStep
			if err := rows.Scan(&s.run, &s.parent, &s.name, &s.kind, &s.status, &s.err, &s.outcome, &s.instructions, &s.decidedBy, &s.at); err != nil {
				return err
			}
			*into = append(*into, s)
			return nil
		}, flow, since, recentSteps)
	}
	if err := read("s.status = '"+stepError+"' AND s.kind <> '"+kindLoop+"'", &rr.failed); err != nil {
		return rr, err
	}
	err = read("((s.kind = '"+kindHuman+"' AND s.outcome <> '') OR (s.kind = '"+kindLoop+"' AND s.decided_by IS NOT NULL))", &rr.decided)
	return rr, err
}

func renderRecent(rr recentRuns) string {
	var b strings.Builder
	if len(rr.runs) == 0 {
		b.WriteString("The flow hasn't run yet, but for any trials of its processes.\n")
		return b.String()
	}
	counts := map[string]int{}
	waiting := 0
	for _, r := range rr.runs {
		counts[r.Status]++
		if r.Status == runRunning && r.Waiting {
			waiting++
		}
	}
	first, last := rr.runs[len(rr.runs)-1], rr.runs[0]
	fmt.Fprintf(&b, "The flow's latest %d runs, trials left out, from #%d, started %s, to #%d, started %s:", len(rr.runs),
		first.ID, first.StartedAt.UTC().Format(toldTime), last.ID, last.StartedAt.UTC().Format(toldTime))
	var parts []string
	for _, s := range []string{runDone, runFailed, runInterrupted, runRunning} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], runStatusText(run{Status: s})))
		}
	}
	b.WriteString(" " + strings.Join(parts, ", "))
	if waiting > 0 {
		fmt.Fprintf(&b, ", of which %d wait for a person", waiting)
	}
	b.WriteString(".\n")
	var failed []string
	for _, r := range rr.runs {
		if r.Status == runFailed {
			failed = append(failed, "#"+strconv.FormatInt(r.ID, 10))
		}
	}
	if len(failed) > 0 {
		fmt.Fprintf(&b, "Those that failed: %s.\n", strings.Join(failed, ", "))
	}
	where := func(s recentStep) string {
		if s.parent != nil {
			return fmt.Sprintf("#%d, within #%d", s.run, *s.parent)
		}
		return fmt.Sprintf("#%d", s.run)
	}
	if len(rr.failed) > 0 {
		b.WriteString("The latest steps that failed, latest first:\n")
		for _, s := range rr.failed {
			fmt.Fprintf(&b, "- %s, %s (%s), %s: %s\n", where(s), s.name, s.kind, s.at.UTC().Format(toldTime), wholeText(s.err).cut(300))
		}
	}
	if len(rr.decided) > 0 {
		b.WriteString("The latest decisions people made, latest first:\n")
		for _, s := range rr.decided {
			fmt.Fprintf(&b, "- %s, %s", where(s), s.name)
			if s.decidedBy != "" {
				fmt.Fprintf(&b, " by %s", s.decidedBy)
			}
			switch {
			case s.kind == kindLoop && s.status == stepDone:
				b.WriteString(": go on round the loop")
			case s.kind == kindLoop:
				b.WriteString(": stop going round the loop")
			default:
				fmt.Fprintf(&b, ": %s", s.outcome)
			}
			if s.instructions != "" {
				fmt.Fprintf(&b, ", with the instructions %s", quote(wholeText(s.instructions).cut(300)))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// The tags the assistant's prompt wraps what it is given in, which what
// runs produced may not open or close.
var promptTags = regexp.MustCompile(`(?i)<(/?)(runs|run|recent_runs|current_setup|referred|skills|commands|credentials|conversation|message|setup_format|flows_guide|your_reply)\b`)

// What runs produced, with the prompt's tags taken out of it.
func defuseTags(s string) string {
	return promptTags.ReplaceAllString(s, "&lt;$1$2")
}

// What looks like a key or a token, which a step may have read and printed,
// and what it is blanked out with.
var secretShapes = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----(?s:.*?)(?:-----END [A-Z ]*PRIVATE KEY-----|$)`), redacted},
	{regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})`), redacted},
	{regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`), redacted},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), redacted},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), redacted},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), redacted},
	{regexp.MustCompile(`(?i)\b(bearer|basic|token)(\s+)[A-Za-z0-9._~+/=-]{16,}`), "$1$2" + redacted},
	{regexp.MustCompile(`(?i)((?:api[_-]?key|secret|password|passwd|token|access[_-]?key)["']?\s*[:=]\s*["']?)[^\s"',;]{8,}`), "$1" + redacted},
}

// s with what looks like a key or token blanked out.
func blankSecrets(s string) string {
	for _, sh := range secretShapes {
		s = sh.re.ReplaceAllString(s, sh.with)
	}
	return s
}

// What the assistant is told of the flow's runs for a reply.
type assistantRunsTold struct {
	// The runs the person asks about, each in a <run> tag, and the summary
	// of the latest runs; data, defused and blanked.
	runs, recent string
	// A run of the board as it is going, which holds the board, so the app
	// refuses a setup file until it ends; 0 for none.
	holding int64
}

func tellRuns(ctx context.Context, db *sql.DB, flow int64, history []assistantMessage) (assistantRunsTold, error) {
	var told assistantRunsTold
	ids, steps, about := askedRuns(history)
	var runs []toldRun
	for _, id := range ids {
		t, err := loadToldRun(ctx, db, flow, id, steps, about)
		if err != nil {
			return told, err
		}
		runs = append(runs, t)
	}
	// The runs within those asked about that failed, which a flow process's
	// failure comes from, told as runs of their own.
	for _, t := range slices.Clone(runs) {
		n := 0
		for _, c := range t.within {
			if c.Status != runFailed || n == maxAssistantRunsWithin || slices.Contains(ids, c.ID) {
				continue
			}
			ct, err := loadToldRun(ctx, db, flow, c.ID, steps, about)
			if err != nil {
				return told, err
			}
			runs, ids = append(runs, ct), append(ids, c.ID)
			n++
		}
	}
	if len(runs) > 0 {
		var b strings.Builder
		b.WriteString("<runs>\n")
		for _, t := range runs {
			fmt.Fprintf(&b, "<run id=\"%d\">\n%s</run>\n", t.id, defuseTags(blankSecrets(fitRun(t, maxAssistantRunBytes/len(runs)))))
		}
		b.WriteString("</runs>\n")
		told.runs = b.String()
	}
	rr, err := loadRecentRuns(ctx, db, flow)
	if err != nil {
		return told, err
	}
	recent := renderRecent(rr)
	if len(recent) > maxAssistantRecentBytes {
		recent = cutBytes(recent, maxAssistantRecentBytes) + "\n(The rest is cut, to fit.)\n"
	}
	told.recent = "<recent_runs>\n" + defuseTags(blankSecrets(recent)) + "</recent_runs>\n"
	err = db.QueryRowContext(ctx, `SELECT r.id FROM runs r JOIN flows f ON f.id = r.flow_id
		WHERE r.flow_id = ? AND r.status = ? AND NOT r.version_id <=> f.published_version_id ORDER BY r.id LIMIT 1`,
		flow, runRunning).Scan(&told.holding)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return told, err
}
