package main

// Playing a task board: a flow over flows. While a board plays, it is a
// query standing over its columns' records: each record no run is going on
// is given to the flow taking records in its state, which runs on it, a run
// per record as a person starts one from the Records page (record_runs.go),
// on the version the flow published if any. The flow taking a state is the
// one, of those whoever pressed Play may start on the struct's records,
// with a query naming that state whose rule holds of the record, else, when
// no flow names it, the one querying the struct in any state whose rule
// holds (takingFlows); a record in a state no flow takes rests there, and
// one in a state several take waits for its column to name the one it goes
// to. A column naming a flow gives every record it holds to that flow,
// whatever its state, and one resting (max_runs 0) gives them to none, as a
// column of done records does, which a flow of any state would otherwise
// take too. So a board drives records through their lifecycle, an
// exception to a ticket to a pull request, each flow moving them on to the
// next state and the flow taking that.
//
// It doesn't follow records as they move; each turn it looks at what its
// columns hold and starts what is due, so whatever moved a record, a run, a
// person correcting it or a flow published taking a state, it carries on
// from there, and a replica stopping loses nothing, as which boards play
// and the runs they started are in the database. Every replica looks each
// second, and each turn of a board is claimed by moving its dispatched_at
// on, which only one of them gets to do; a board's runs are started one at
// a time under a lock of its row, counting those going, so it never has
// more going than it allows.
//
// A board has at most max_runs of its runs going at once, and a column at
// most its own limit, if it has one, counting the runs going on the records
// it holds, none for one resting; a run waiting on a person keeps its
// place, so a column of reviews with a limit of its own can't take every
// place the board has.
// Columns further right go first, finishing work before starting more,
// each in the order it sorts its records. A record isn't started while a
// run is going on it, and is read again just before its run starts: still
// in the state it was seen in, and selected by the flow's query. Records in
// no column are none of the board's.
//
// No run claims a record (see shared.go), so a run that failed, or ended
// without moving its record on, would leave it in its column to be started
// again for ever, a ticket filed and a model paid each time. So a record
// whose latest run from the board ended with it in the state that run
// started it in is stuck: the board skips it, and its card says why, until
// a person tries it again or corrects it by hand. A record going round the
// board's flows, started from the same state boardRounds times with no
// person in between, is stuck too, as a loop is bounded (loops.go). A run
// interrupted, stopped by a person or by a replica stopping without
// RESUME_RUNS, leaves its record stuck as well, for a person to try again.
//
// A board's runs are for whoever pressed Play (started_by, which runUser
// reads), so their access, spending limits and connections apply; once
// they are removed, the board stops playing and says why, and a column
// whose flow they may not run says so and starts nothing. Two boards
// playing over the same records may both start a run on one, as two people
// may from the Records page.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// How often a board takes a turn: every second in the tests' webapps,
// which set SCHEDULE_SECONDS.
var boardTurn = func() time.Duration {
	if scheduleSeconds {
		return time.Second
	}
	return 5 * time.Second
}()

const (
	// How many runs a board, or a column, has going at once at most.
	maxBoardRuns = 50
	// How many times a board starts a record from one state with no person
	// in between before it is stuck, as a loop asks someone (loops.go).
	boardRounds = defaultLoops
	// How many records a turn reads the board's runs of at once, looking
	// for the next to start.
	boardChunk = 100
)

// Why a record is stuck, besides how its run ended (failed, interrupted,
// terminated): its run ended done but left it where it was, or it went
// round the board's flows boardRounds times.
const (
	stuckUnmoved = "unmoved"
	stuckRounds  = "rounds"
)

// Why a board playing wouldn't start the flow a column names, besides its
// being in the trash or unseen (columnTrash, columnHidden): its runs'
// person may not run it, or it doesn't query the column's struct.
const (
	flowDenied    = "denied"
	flowUnqueried = "unqueried"
)

// A flow as a column says it, its name empty for one whoever reads the page
// doesn't see.
type flowRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// The flows taking records in a state, as the board playing picks one, and
// whether they take it by querying any state, as none names it.
type stateFlows struct {
	State string    `json:"state"`
	Flows []flowRef `json:"flows"`
	Any   bool      `json:"any,omitempty"`
}

// How the flow takes records in state whose rule holds of content (any
// content when it is ""): by a query naming the state, by one of any state,
// both or neither.
func (f *recordFlow) takes(state, content string) (named, any bool) {
	for _, q := range f.queries {
		if q.State != "" && q.State != state {
			continue
		}
		where, err := parseRule(q.Where)
		if err != nil || content != "" && len(where) > 0 && !where.holds(content) {
			continue
		}
		if q.State == "" {
			any = true
		} else {
			named = true
		}
	}
	return named, any
}

// Of the flows, in order, those taking records in state whose rule holds of
// content (any content when it is "") by a query naming the state, else,
// when none does, those taking them by a query of any state, and which.
func takingFlows(flows []*recordFlow, state, content string) (taking []*recordFlow, any bool) {
	var anyState []*recordFlow
	for _, f := range flows {
		named, a := f.takes(state, content)
		switch {
		case named:
			taking = append(taking, f)
		case a:
			anyState = append(anyState, f)
		}
	}
	if len(taking) > 0 {
		return taking, false
	}
	return anyState, len(anyState) > 0
}

// Whether the column's records rest, the board starting no flow on them.
func (c *taskColumn) rests() bool {
	return c.MaxRuns != nil && *c.MaxRuns == 0
}

// The states a query names, as state:new or state:(new OR open) do, in
// its clauses; none for one naming none. Values with a * stand for many,
// so name none.
func queryStates(n *searchNode) []string {
	switch n.kind {
	case "term":
		if n.hasKey && strings.EqualFold(n.key, "state") && n.test.op == "is" && (n.test.literal || !strings.Contains(n.test.value, "*")) {
			return []string{n.test.value}
		}
	case "and", "or":
		var out []string
		for _, c := range n.nodes {
			out = append(out, queryStates(c)...)
		}
		return out
	}
	return nil
}

// What a card shows of a record's run: one going on it, running or waiting
// for a person, or the board's last, which left it stuck, and why. Of one
// going, when it started, and if its flow is one the board shows (of those
// its runs' person may start on the records) and whoever reads the page
// sees, its name and the processes whose steps are going or waiting.
type cardRun struct {
	Run      int64      `json:"run"`
	Flow     int64      `json:"flow"`
	Status   string     `json:"status"`
	Why      string     `json:"why,omitempty"`
	Started  *time.Time `json:"started,omitempty"`
	FlowName string     `json:"flowName,omitempty"`
	Steps    []string   `json:"steps,omitempty"`
}

// When each run started and the processes whose steps are going or
// waiting in it, in the order they started, by run id.
func goingRunSteps(ctx context.Context, db *sql.DB, runs []int64) (map[int64]time.Time, map[int64][]string, error) {
	started, steps := map[int64]time.Time{}, map[int64][]string{}
	if len(runs) == 0 {
		return started, steps, nil
	}
	err := scanAll(ctx, db, `SELECT r.id, r.started_at, s.process_name FROM runs r
		LEFT JOIN run_steps s ON s.run_id = r.id AND s.status IN (?, ?)
		WHERE r.id IN `+placeholders(len(runs))+` ORDER BY r.id, s.id`, func(rows *sql.Rows) error {
		var run int64
		var at time.Time
		var step sql.NullString
		if err := rows.Scan(&run, &at, &step); err != nil {
			return err
		}
		started[run] = at
		if step.Valid && !slices.Contains(steps[run], step.String) {
			steps[run] = append(steps[run], step.String)
		}
		return nil
	}, append([]any{stepActive, stepWaiting}, int64Args(runs)...)...)
	return started, steps, err
}

// A run a board started on a record: the flow it ran, how it is or ended,
// the state the record was in as it started, when it started and finished,
// and when a person asked for the record to go again after it, if they did.
type boardPass struct {
	run, flow         int64
	status, state     string
	created           time.Time
	finished, retried *time.Time
}

// The runs a board started on a record, oldest first, with the record's
// state and when a person last corrected it by hand.
type recordPasses struct {
	passes []boardPass
	state  string
	edited *time.Time
}

// Why the record is stuck on a column whose flow is flow, and the board's
// latest run on it; "" if it isn't. A person trying it again, or
// correcting it, after that run ended frees it, and starts its rounds
// anew.
func (rp *recordPasses) stuck(flow int64) (string, boardPass) {
	p := rp.passes[len(rp.passes)-1]
	if p.status == runRunning {
		return "", p
	}
	touched := rp.edited
	for _, q := range rp.passes {
		if q.retried != nil && (touched == nil || q.retried.After(*touched)) {
			touched = q.retried
		}
	}
	rounds := 0
	for _, q := range rp.passes {
		if q.state == rp.state && (touched == nil || q.created.After(*touched)) {
			rounds++
		}
	}
	switch {
	case rounds >= boardRounds:
		return stuckRounds, p
	case touched != nil && p.finished != nil && touched.After(*p.finished):
		return "", p
	case p.state != rp.state, p.flow != flow:
		// It moved on, or the column runs another flow now.
		return "", p
	case p.status == runDone:
		return stuckUnmoved, p
	}
	return p.status, p
}

// The runs the board started on each of the records, by record id; a
// record it started none on is left out, and one deleted.
func boardPasses(ctx context.Context, db *sql.DB, board int64, records []int64) (map[int64]*recordPasses, error) {
	out := map[int64]*recordPasses{}
	if len(records) == 0 {
		return out, nil
	}
	args := []any{board}
	for _, r := range records {
		args = append(args, r)
	}
	err := scanAll(ctx, db, `SELECT t.record_id, t.run_id, r.flow_id, r.status, t.state, t.created_at, r.finished_at, t.retried_at, sr.state, sr.edited_at
		FROM task_board_runs t JOIN runs r ON r.id = t.run_id JOIN shared_records sr ON sr.id = t.record_id
		WHERE t.board_id = ? AND t.record_id IN (?`+strings.Repeat(", ?", len(records)-1)+`) ORDER BY t.record_id, t.run_id`,
		func(rows *sql.Rows) error {
			var id int64
			var p boardPass
			var finished, retried, edited sql.NullTime
			var state string
			if err := rows.Scan(&id, &p.run, &p.flow, &p.status, &p.state, &p.created, &finished, &retried, &state, &edited); err != nil {
				return err
			}
			p.finished, p.retried = nullTime(finished), nullTime(retried)
			rp := out[id]
			if rp == nil {
				rp = &recordPasses{state: state, edited: nullTime(edited)}
				out[id] = rp
			}
			rp.passes = append(rp.passes, p)
			return nil
		}, args...)
	return out, err
}

func nullTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}

// A run going on a record, and whether it waits for a person.
type goingRun struct {
	run, flow int64
	waiting   bool
}

// The runs going on the store's records, by record id: one started on a
// record, or the one that last moved it on, still running, as
// recordsGoing has them.
func storeGoing(ctx context.Context, db *sql.DB, store int64) (map[int64][]goingRun, error) {
	going := map[int64][]goingRun{}
	waiting := "EXISTS (SELECT 1 FROM run_steps ws WHERE ws.run_id = r.id AND ws.status = 'waiting')"
	err := scanAll(ctx, db, `SELECT sr.id, r.id, r.flow_id, `+waiting+` FROM runs r JOIN shared_records sr ON sr.id = r.record_id
			WHERE r.status = ? AND sr.object_id = ?
		UNION SELECT sr.id, r.id, r.flow_id, `+waiting+` FROM runs r JOIN shared_records sr ON sr.run_id = r.id
			WHERE r.status = ? AND sr.object_id = ?
		ORDER BY 1, 2`, func(rows *sql.Rows) error {
		var record int64
		var g goingRun
		if err := rows.Scan(&record, &g.run, &g.flow, &g.waiting); err != nil {
			return err
		}
		going[record] = append(going[record], g)
		return nil
	}, runRunning, store, runRunning, store)
	return going, err
}

// How many of the runs the board started are going on each record, by
// record id.
func boardGoing(ctx context.Context, db rowsQuerier, board int64) (map[int64]int, error) {
	going := map[int64]int{}
	err := scanAll(ctx, db, `SELECT t.record_id, COUNT(*) FROM runs r JOIN task_board_runs t ON t.run_id = r.id
		WHERE r.status = ? AND t.board_id = ? GROUP BY t.record_id`, func(rows *sql.Rows) error {
		var record int64
		var n int
		err := rows.Scan(&record, &n)
		going[record] = n
		return err
	}, runRunning, board)
	return going, err
}

// A person by id, with whether they were removed; nil for nobody.
func boardPlayer(ctx context.Context, db querier, id int64) (*user, bool, error) {
	u := &user{ID: id}
	var removed bool
	err := db.QueryRowContext(ctx, "SELECT email, name, admin, team_manager, removed_at IS NOT NULL FROM users WHERE id = ?", id).
		Scan(&u.Email, &u.Name, &u.Admin, &u.TeamManager, &removed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, true, nil
	}
	return u, removed, err
}

// Whom a board's runs are for, or why it plays no more: whoever pressed
// Play, until they are removed; nobody without sign-in.
func boardRunner(ctx context.Context, db *sql.DB, signIn bool, playedBy sql.NullInt64) (*user, string, error) {
	if !signIn {
		return nil, "", nil
	}
	if !playedBy.Valid {
		return nil, "it was played before people signed in, so its runs were nobody's; press Play to have them be yours", nil
	}
	u, removed, err := boardPlayer(ctx, db, playedBy.Int64)
	if err != nil {
		return nil, "", err
	}
	if removed {
		return nil, "whoever pressed Play was removed; press Play to have its runs be yours", nil
	}
	return u, "", nil
}

// Why a board whose runs are u's wouldn't start the flow on a column's
// records, given it isn't among the flows u may start on them.
func columnFlowProblem(ctx context.Context, db *sql.DB, u *user, flow int64) (string, error) {
	var trashed bool
	err := db.QueryRowContext(ctx, "SELECT deleted_at IS NOT NULL FROM flows WHERE id = ?", flow).Scan(&trashed)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return columnGone, nil
	case err != nil:
		return "", err
	case trashed:
		return columnTrash, nil
	}
	access, err := flowAccess(ctx, db, u, flow)
	switch {
	case err != nil:
		return "", err
	case access == "":
		return columnHidden, nil
	case access != accessEdit:
		return flowDenied, nil
	}
	return flowUnqueried, nil
}

// The flow with the id among the flows, if it is.
func flowByID(flows []*recordFlow, id int64) *recordFlow {
	if i := slices.IndexFunc(flows, func(f *recordFlow) bool { return f.ID == id }); i >= 0 {
		return flows[i]
	}
	return nil
}

// The flow a column gives a record in state holding content to, of the
// flows that may be started on its records: the one it names, if it
// selects the record, else the one taking records in that state, by naming
// it or, as none does, by querying any state; nil for none, or several.
func recordGoesTo(flows []*recordFlow, named *int64, state, content string) *recordFlow {
	if named != nil {
		f := flowByID(flows, *named)
		if f == nil || offQuery(f.queries, content, state) != "" {
			return nil
		}
		return f
	}
	if taking, _ := takingFlows(flows, state, content); len(taking) == 1 {
		return taking[0]
	}
	return nil
}

// Plays the boards playing, each turn due claimed by one replica, every
// second until ctx is done.
func playTaskBoards(ctx context.Context, db *sql.DB, eng *engine, signIn bool) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := playDue(ctx, db, eng, signIn, time.Now()); err != nil && ctx.Err() == nil {
				log.Printf("task boards: %v", err)
			}
		}
	}
}

// Takes a turn of each board due at now that this replica claims.
func playDue(ctx context.Context, db *sql.DB, eng *engine, signIn bool, now time.Time) error {
	type due struct {
		id int64
		at sql.NullTime
	}
	var dues []due
	err := scanAll(ctx, db, "SELECT id, dispatched_at FROM task_boards WHERE playing AND (dispatched_at IS NULL OR dispatched_at <= ?)",
		func(rows *sql.Rows) error {
			var d due
			err := rows.Scan(&d.id, &d.at)
			dues = append(dues, d)
			return err
		}, now.Add(-boardTurn).UTC())
	if err != nil {
		return err
	}
	for _, d := range dues {
		res, err := db.ExecContext(ctx, "UPDATE task_boards SET dispatched_at = ? WHERE id = ? AND playing AND dispatched_at <=> ?",
			now.UTC().Truncate(time.Millisecond), d.id, d.at)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			// Another replica claimed it, or it stopped since.
			continue
		}
		if err := playTurn(ctx, db, eng, signIn, d.id); err != nil && ctx.Err() == nil {
			log.Printf("task board %d: %v", d.id, err)
		}
	}
	return nil
}

// Stops the board playing by itself, saying why.
func stopBoard(ctx context.Context, db *sql.DB, board int64, why string) error {
	log.Printf("task board %d stopped playing: %s", board, why)
	_, err := db.ExecContext(ctx, "UPDATE task_boards SET playing = FALSE, stopped = ? WHERE id = ? AND playing", why, board)
	return err
}

// One turn of a board: starts the flow each record due goes to (recordGoesTo),
// the rightmost columns' records first, as many as it has room for.
func playTurn(ctx context.Context, db *sql.DB, eng *engine, signIn bool, board int64) error {
	var playedBy sql.NullInt64
	var free int
	err := db.QueryRowContext(ctx, "SELECT played_by, max_runs FROM task_boards WHERE id = ? AND playing", board).Scan(&playedBy, &free)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	runner, msg, err := boardRunner(ctx, db, signIn, playedBy)
	if err != nil {
		return err
	}
	if msg != "" {
		return stopBoard(ctx, db, board, msg)
	}
	mine, err := boardGoing(ctx, db, board)
	if err != nil {
		return err
	}
	for _, n := range mine {
		free -= n
	}
	if free <= 0 {
		return nil
	}
	var columns []taskColumn
	err = scanAll(ctx, db, "SELECT "+columnColumns+" FROM task_board_columns WHERE board_id = ? ORDER BY position", func(rows *sql.Rows) error {
		var c taskColumn
		err := scanColumn(rows, &c)
		columns = append(columns, c)
		return err
	}, board)
	if err != nil {
		return err
	}
	structs, err := describeColumns(ctx, db, runner, columns)
	if err != nil {
		return err
	}
	// What each store's columns need, read once however many columns hold
	// its records.
	flows := map[int64][]*recordFlow{}
	tables := map[int64]*recordTable{}
	going := map[int64]map[int64][]goingRun{}
	for i := len(columns) - 1; i >= 0 && free > 0; i-- {
		c := columns[i]
		if c.Problem != "" || c.rests() {
			continue
		}
		root := structs[*c.Struct].root
		if _, ok := flows[root]; !ok {
			if flows[root], err = recordFlows(ctx, db, runner, root); err != nil {
				return err
			}
		}
		if len(flows[root]) == 0 {
			continue
		}
		if _, ok := tables[root]; !ok {
			if tables[root], err = readRecordTable(ctx, db, root); err != nil {
				return err
			}
			if going[root], err = storeGoing(ctx, db, root); err != nil {
				return err
			}
		}
		rt := tables[root]
		key, desc := sortParam(c.Sort)
		matched := rt.table.search().run(parseSearch(c.Query), key, desc)
		room := free
		if c.MaxRuns != nil {
			busy := 0
			for _, m := range matched {
				busy += mine[rt.records[m].ID]
			}
			room = min(room, *c.MaxRuns-busy)
		}
		// Each record no run is going on, with the flow it goes to.
		type dueRecord struct {
			sharedRecord
			flow *recordFlow
		}
		var due []dueRecord
		for _, m := range matched {
			r := rt.records[m]
			if len(going[root][r.ID]) > 0 {
				continue
			}
			if f := recordGoesTo(flows[root], c.Flow, r.State, string(r.Content)); f != nil {
				due = append(due, dueRecord{r, f})
			}
		}
		for len(due) > 0 && room > 0 {
			chunk := due[:min(len(due), boardChunk)]
			due = due[len(chunk):]
			ids := make([]int64, len(chunk))
			for i, r := range chunk {
				ids[i] = r.ID
			}
			passes, err := boardPasses(ctx, db, board, ids)
			if err != nil {
				return err
			}
			for _, r := range chunk {
				if room == 0 {
					break
				}
				if rp := passes[r.ID]; rp != nil {
					if why, _ := rp.stuck(r.flow.ID); why != "" {
						continue
					}
				}
				started, more, err := startBoardRun(ctx, db, eng, board, r.flow, root, r.ID, r.State, runner)
				if err != nil {
					return err
				}
				if !more {
					return nil
				}
				if started {
					room--
					free--
					mine[r.ID]++
				}
			}
		}
	}
	return nil
}

// Starts the flow on the record for the board, the run u's: whether it
// started, and whether the board goes on starting more, which it doesn't
// once it stopped playing or has no room. It starts none on a record no
// longer in state, nor selected by the flow's queries, nor one a run is
// going on. The board's row is locked while it counts its runs going and
// records the new one, so its starts are one at a time.
func startBoardRun(ctx context.Context, db *sql.DB, eng *engine, board int64, f *recordFlow, store, record int64, state string, u *user) (bool, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback()
	var playing bool
	var free int
	err = tx.QueryRowContext(ctx, "SELECT playing, max_runs FROM task_boards WHERE id = ? FOR UPDATE", board).Scan(&playing, &free)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil || !playing {
		return false, false, err
	}
	mine, err := boardGoing(ctx, tx, board)
	if err != nil {
		return false, false, err
	}
	for _, n := range mine {
		free -= n
	}
	if free <= 0 {
		return false, false, nil
	}
	var content, now string
	err = tx.QueryRowContext(ctx, "SELECT content, state FROM shared_records WHERE id = ? AND object_id = ?", record, store).Scan(&content, &now)
	if errors.Is(err, sql.ErrNoRows) {
		return false, true, nil
	}
	if err != nil {
		return false, false, err
	}
	if now != state || offQuery(f.queries, content, now) != "" {
		return false, true, nil
	}
	var going bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM runs WHERE record_id = ? AND status = ?)
		OR EXISTS (SELECT 1 FROM shared_records sr JOIN runs r ON r.id = sr.run_id WHERE sr.id = ? AND r.status = ?)`,
		record, runRunning, record, runRunning).Scan(&going)
	if err != nil || going {
		return false, err == nil, err
	}
	var by *int64
	if u != nil {
		by = &u.ID
	}
	nr, err := newRun(ctx, tx, f.ID, nil, by, true)
	if errors.Is(err, sql.ErrNoRows) {
		// The flow went to the trash since.
		return false, true, nil
	}
	if err != nil {
		return false, false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE runs SET record_id = ? WHERE id = ?", record, nr.id); err != nil {
		return false, false, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO task_board_runs (run_id, board_id, record_id, state) VALUES (?, ?, ?, ?)", nr.id, board, record, state); err != nil {
		return false, false, err
	}
	if err := tx.Commit(); err != nil {
		return false, false, err
	}
	nr.record = record
	eng.start(nr, f.ID, nil)
	return true, true, nil
}

// Fills in what playing the board does, for its page: how many of its runs
// are going; for a column naming a flow, why the board wouldn't start it,
// if it wouldn't, and for one naming none and not resting, the flows taking
// each state it names or its records shown are in; and on each card shown
// the run going on its record, or why the board skips it as stuck. The
// runs are whoever pressed Play's while it plays, else u's, who would press
// it.
func fillPlay(ctx context.Context, db *sql.DB, u *user, b *taskBoard, structs map[int64]taskStruct) error {
	runner := u
	if b.Playing && b.playedBy.Valid {
		p, removed, err := boardPlayer(ctx, db, b.playedBy.Int64)
		if err != nil {
			return err
		}
		if p != nil && !removed {
			runner = p
		}
	}
	// The name of a flow of runner's, as u may read it: empty for one u
	// doesn't see, as the board plays for another.
	seen := map[int64]bool{}
	nameOf := func(f *recordFlow) (string, error) {
		if runner == u || u == nil || runner != nil && runner.ID == u.ID {
			return f.Name, nil
		}
		if _, ok := seen[f.ID]; !ok {
			access, err := flowAccess(ctx, db, u, f.ID)
			if err != nil {
				return "", err
			}
			seen[f.ID] = access != ""
		}
		if !seen[f.ID] {
			return "", nil
		}
		return f.Name, nil
	}
	mine, err := boardGoing(ctx, db, b.ID)
	if err != nil {
		return err
	}
	for _, n := range mine {
		b.Running += n
	}
	flows := map[int64][]*recordFlow{}
	going := map[int64]map[int64][]goingRun{}
	// The cards of records with runs going, filled in once every column's are
	// known.
	type goingCard struct {
		runs  map[string]cardRun
		key   string
		named bool
	}
	var goings []goingCard
	var goingRuns []int64
	for i := range b.Columns {
		c := &b.Columns[i]
		if c.Struct == nil || c.Problem != "" || c.Cards == nil {
			continue
		}
		root := structs[*c.Struct].root
		if _, ok := flows[root]; !ok {
			if flows[root], err = recordFlows(ctx, db, runner, root); err != nil {
				return err
			}
			if going[root], err = storeGoing(ctx, db, root); err != nil {
				return err
			}
		}
		fs := flows[root]
		switch {
		case c.rests():
		case c.Flow != nil:
			if flowByID(fs, *c.Flow) == nil {
				if c.FlowProblem, err = columnFlowProblem(ctx, db, runner, *c.Flow); err != nil {
					return err
				}
			}
		default:
			// The states it names or holds, in the struct's order.
			held := map[string]bool{}
			for _, st := range queryStates(parseSearch(c.Query)) {
				held[strings.ToLower(st)] = true
			}
			for _, r := range c.Cards.Records {
				held[strings.ToLower(r.State)] = true
			}
			c.ByState = []stateFlows{}
			for _, st := range c.Cards.States {
				if !held[strings.ToLower(st)] {
					continue
				}
				taking, any := takingFlows(fs, st, "")
				sf := stateFlows{State: st, Flows: []flowRef{}, Any: any}
				for _, f := range taking {
					name, err := nameOf(f)
					if err != nil {
						return err
					}
					sf.Flows = append(sf.Flows, flowRef{f.ID, name})
				}
				c.ByState = append(c.ByState, sf)
			}
		}
		// The records shown with no run going, with the flow each goes to.
		idle := map[int64]int64{}
		var ids []int64
		for _, r := range c.Cards.Records {
			rs := going[root][r.ID]
			if len(rs) == 0 {
				if c.rests() {
					continue
				}
				if f := recordGoesTo(fs, c.Flow, r.State, string(r.Content)); f != nil {
					idle[r.ID] = f.ID
					ids = append(ids, r.ID)
				}
				continue
			}
			g := rs[0]
			if at := slices.IndexFunc(rs, func(g goingRun) bool { return g.waiting }); at >= 0 {
				g = rs[at]
			}
			status := "running"
			if g.waiting {
				status = "waiting"
			}
			cr := cardRun{Run: g.run, Flow: g.flow, Status: status}
			if f := flowByID(fs, g.flow); f != nil {
				if cr.FlowName, err = nameOf(f); err != nil {
					return err
				}
			}
			key := strconv.FormatInt(r.ID, 10)
			c.Cards.Runs[key] = cr
			goings = append(goings, goingCard{c.Cards.Runs, key, cr.FlowName != ""})
			goingRuns = append(goingRuns, g.run)
		}
		passes, err := boardPasses(ctx, db, b.ID, ids)
		if err != nil {
			return err
		}
		for id, rp := range passes {
			if why, p := rp.stuck(idle[id]); why != "" {
				c.Cards.Runs[strconv.FormatInt(id, 10)] = cardRun{Run: p.run, Flow: p.flow, Status: "stuck", Why: why}
			}
		}
	}
	started, steps, err := goingRunSteps(ctx, db, goingRuns)
	if err != nil {
		return err
	}
	for _, g := range goings {
		cr := g.runs[g.key]
		if at, ok := started[cr.Run]; ok {
			cr.Started = &at
		}
		if g.named {
			cr.Steps = steps[cr.Run]
		}
		g.runs[g.key] = cr
	}
	return nil
}

func registerTaskBoardPlayRoutes(mux *http.ServeMux, db *sql.DB) {
	// The board as it is now, with its records, as whoever asks sees it.
	answer := func(w http.ResponseWriter, r *http.Request, id int64) {
		b, err := visibleTaskBoard(r.Context(), db, nil, id)
		if err != nil {
			serverError(w, err)
			return
		}
		answerTaskBoard(w, r, db, http.StatusOK, b, true)
	}

	// Plays the board, its runs for whoever asks, from its next turn, at
	// once.
	mux.HandleFunc("POST /api/taskboards/{id}/play", func(w http.ResponseWriter, r *http.Request) {
		b, ok := taskBoardOf(w, r, db)
		if !ok {
			return
		}
		_, err := db.ExecContext(r.Context(), "UPDATE task_boards SET playing = TRUE, played_by = ?, stopped = '', dispatched_at = NULL WHERE id = ?",
			requestUserID(r), b.ID)
		if err != nil {
			serverError(w, err)
			return
		}
		answer(w, r, b.ID)
	})

	// Stops the board starting runs; those going go on.
	mux.HandleFunc("POST /api/taskboards/{id}/pause", func(w http.ResponseWriter, r *http.Request) {
		b, ok := taskBoardOf(w, r, db)
		if !ok {
			return
		}
		if _, err := db.ExecContext(r.Context(), "UPDATE task_boards SET playing = FALSE, stopped = '' WHERE id = ?", b.ID); err != nil {
			serverError(w, err)
			return
		}
		answer(w, r, b.ID)
	})

	// Frees the records named, {"records": [1, 2]}, which the board skips
	// as stuck, so it starts their column's flow on them again, from its
	// next turn, at once.
	mux.HandleFunc("POST /api/taskboards/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		b, ok := taskBoardOf(w, r, db)
		if !ok {
			return
		}
		var body struct {
			Records []int64 `json:"records"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		switch {
		case len(body.Records) == 0:
			http.Error(w, "name the records to try again", http.StatusBadRequest)
			return
		case len(body.Records) > maxColumnCards:
			http.Error(w, fmt.Sprintf("try at most %d records again at once", maxColumnCards), http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		args := []any{b.ID}
		for _, id := range body.Records {
			args = append(args, id)
		}
		var latest []any
		err := scanAll(ctx, db, "SELECT MAX(run_id) FROM task_board_runs WHERE board_id = ? AND record_id IN (?"+strings.Repeat(", ?", len(body.Records)-1)+") GROUP BY record_id",
			func(rows *sql.Rows) error {
				var run int64
				err := rows.Scan(&run)
				latest = append(latest, run)
				return err
			}, args...)
		if err == nil && len(latest) > 0 {
			_, err = db.ExecContext(ctx, "UPDATE task_board_runs SET retried_at = CURRENT_TIMESTAMP(3) WHERE run_id IN (?"+strings.Repeat(", ?", len(latest)-1)+")", latest...)
		}
		if err == nil {
			_, err = db.ExecContext(ctx, "UPDATE task_boards SET dispatched_at = NULL WHERE id = ?", b.ID)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		answer(w, r, b.ID)
	})
}
