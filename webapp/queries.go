package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// A query link takes a persistent object's stored instances rather than the
// object in this run: an agent or script process gets, as a list of that
// object, what the flow's runs that ended done stored of it, filtered by a
// rule on its fields (see rules.go) and a state, newest first or oldest
// first, at most a limit. So a process can look up what was handled before,
// such as the last approved review of a pull request. What the run itself
// has stored so far isn't among them.
//
// A query link doesn't wait: the process runs as if it took nothing by it,
// and reads the records once it starts. It neither names a state nor is a
// condition, can't consume, and is the process's only input link to its
// object, so the process takes the object one way only.
//
// A shared struct's query may also search its records as the struct's
// Records page does (search.go), such as state:open @created:>now-7d, and
// any query may rank what it selects by how like it is to what the process
// takes (similar.go), the most like first. Both may name what the process
// takes by a plain link as {@Object.attribute}, or {@Object} for all it
// holds, filled in as the step starts: into a search as a quoted value,
// into like as the text it holds. A query ranking records looks them up
// rather than picking what its run works on, so it leaves out the records
// its run holds (picks.go, references.go, record_runs.go), reads its store
// as it is in a run started on a record, and a task board or Run… on a
// record doesn't count it as taking records.
//
// The step is told how many records the query selected when its limit or
// how far it reads cut the list (runInput.Matched): an agent by the input's
// matched attribute, a script by a file named after the object in $MATCHED.
// A query reads at most searchedRows records to count or rank them, the
// latest, as the Records page does, and then says it selected at least so
// many; one with neither a search nor like nor a rule counts them all.
type linkQuery struct {
	// A rule the records' fields must hold, all of them when empty.
	Where string `json:"where,omitempty" yaml:"where,omitempty"`
	// The state they must be in, by name; any when empty.
	State string `json:"state,omitempty" yaml:"state,omitempty"`
	// At most this many; defaultQueryLimit when 0.
	Limit int `json:"limit,omitempty" yaml:"limit,omitempty"`
	// Oldest first rather than newest first; with like, of records as like.
	Oldest bool `json:"oldest,omitempty" yaml:"oldest,omitempty"`
	// A shared struct's records the Records page's search finds, all when
	// empty.
	Search string `json:"search,omitempty" yaml:"search,omitempty"`
	// Text the records are ranked by, the most like it first; those sharing
	// no word with it aren't selected. None ranks them by when they were
	// written.
	Like string `json:"like,omitempty" yaml:"like,omitempty"`
}

const (
	defaultQueryLimit = 20
	// So an agent's prompt stays a prompt.
	maxQueryLimit = 100
	// How long a query's search or like may be, as written.
	maxQueryText = 1 << 10
)

// How a query's search and like name a value, as an error shows it.
const queryValueExample = "{@Incoming.summary}"

// The states the query takes records in: its state, else those its search
// names, as state:new or state:(new OR open) do; none for any.
func (q *linkQuery) states() []string {
	if q.State != "" {
		return []string{q.State}
	}
	if q.Search == "" {
		return nil
	}
	return queryStates(parseSearch(blankValues(q.Search)))
}

// Whether the query looks records up, ranking them like what its process
// takes, rather than picks what its run works on.
func (q *linkQuery) lookup() bool {
	return strings.TrimSpace(q.Like) != ""
}

// A search with each value it names standing for any, as * does, to read
// what it says of the records without a run.
func blankValues(search string) string {
	return branchValue.ReplaceAllString(search, "*")
}

// What is wrong with a query's search or like as written: a brace not
// naming a value, or one naming no object.
func queryValues(what, text string) ([]branchPart, string) {
	if len(text) > maxQueryText {
		return nil, fmt.Sprintf("the query's %s is longer than %d characters", what, maxQueryText)
	}
	parts, err := parseValueParts(text, queryValueExample)
	if err != nil {
		return nil, fmt.Sprintf("the query's %s: %s", what, err.Error())
	}
	return parts, ""
}

func (q *linkQuery) limit() int {
	if q.Limit == 0 {
		return defaultQueryLimit
	}
	return q.Limit
}

// The query as stored: null for none.
func storedQuery(q *linkQuery) any {
	if q == nil {
		return nil
	}
	b, _ := json.Marshal(q)
	return string(b)
}

// A stored query; nil for none.
func queryOf(stored []byte) (*linkQuery, error) {
	if len(stored) == 0 {
		return nil, nil
	}
	var q linkQuery
	return &q, json.Unmarshal(stored, &q)
}

// What's wrong with the query alone, if anything.
func (q *linkQuery) problem() string {
	if _, err := parseRule(q.Where); err != nil {
		return "the query's rule: " + err.Error()
	}
	if q.Limit < 0 || q.Limit > maxQueryLimit {
		return fmt.Sprintf("a query takes at most %d records", maxQueryLimit)
	}
	if _, msg := queryValues("search", q.Search); msg != "" {
		return msg
	}
	if _, msg := queryValues("like", q.Like); msg != "" {
		return msg
	}
	return ""
}

// Why only a shared struct's query searches, as the link's check and an
// import say.
const searchUnshared = "only a shared struct's records are searched; select a persistent object's by its rule and state"

// What's wrong with the link as to queries, if anything: a query link is
// an agent's or script's input link to a persistent object, naming no state
// and not consuming it, and a process takes an object it queries by no
// other link.
func checkQuery(ctx context.Context, db *sql.DB, l link) (string, error) {
	if l.Direction != directionInput {
		if l.Query != nil {
			return "only an input link has a query", nil
		}
		return "", nil
	}
	var others, queried int
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*), COALESCE(SUM(query IS NOT NULL), 0) FROM object_links WHERE process_id = ? AND object_id = ? AND direction = 'input' AND id <> ?",
		l.Process, l.Object, l.ID).Scan(&others, &queried)
	if err != nil {
		return "", err
	}
	if queried > 0 {
		return "the process queries that object already; it takes it by its query alone", nil
	}
	if l.Query == nil {
		return "", nil
	}
	if others > 0 {
		return "the process takes that object already; it can't query it too", nil
	}
	if msg := l.Query.problem(); msg != "" {
		return msg, nil
	}
	switch {
	case l.State != nil || l.Condition || len(l.States) > 0:
		return "a query link names no state; the query says which records", nil
	case l.Role == roleConsumption:
		return "a query reads records; it can't consume them", nil
	}
	var kind string
	var persistent, shared bool
	err = db.QueryRowContext(ctx, "SELECT p.kind, o.persistent, o.store_id IS NOT NULL FROM processes p JOIN objects o ON o.id = ? WHERE p.id = ?", l.Object, l.Process).
		Scan(&kind, &persistent, &shared)
	if errors.Is(err, sql.ErrNoRows) {
		return "process or object not found", nil
	}
	if err != nil {
		return "", err
	}
	if kind != kindAgent && kind != kindScript {
		return "only an agent or a script queries records", nil
	}
	if !persistent && !shared {
		return "only a persistent or shared object has records to query", nil
	}
	if l.Query.Search != "" && !shared {
		return searchUnshared, nil
	}
	if l.Query.State != "" {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM object_states WHERE object_id = ? AND name = ?", l.Object, l.Query.State).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return fmt.Sprintf("the object has no state %s", quote(l.Query.State)), nil
		}
	}
	return "", nil
}

// What a query hands its step: the records it selected as a list of its
// object, at most its limit, and how many it selected, more than the list
// holds when the limit cut it; with more, at least so many, as it read no
// further.
type queryResult struct {
	records string
	matched int
	more    bool
}

// A record a query may select: its id among its store's records, 0 for a
// persistent object's stored instance, and what it holds.
type queryCandidate struct {
	id      int64
	content string
}

// The records of the object the query of a run in project selects, with
// its search and like as filled in (fillQuery): what runs other than run
// that ended done stored of it, or for a shared object its store's records
// (see shared.go), but those leaveOut names. Only the project's.
func queryRecords(ctx context.Context, db *sql.DB, project, run, object, store int64, q *linkQuery, search, like string, leaveOut map[int64]bool) (queryResult, error) {
	where, err := parseRule(q.Where)
	if err != nil {
		return queryResult{}, err
	}
	if store != 0 && search != "" {
		return searchRecords(ctx, db, store, q, search, like, where, leaveOut)
	}
	// Ranking, it reads the latest, as a search does, oldest then putting
	// the earliest first of those as like.
	order := "DESC"
	if q.Oldest && !q.lookup() {
		order = "ASC"
	}
	var from, orderBy string
	var args []any
	if store != 0 {
		from = "SELECT id, content FROM shared_records WHERE object_id = ? AND project_id = ? AND deleted_at IS NULL"
		args = []any{store, project}
		if q.State != "" {
			from += " AND state = ?"
			args = append(args, q.State)
		}
		orderBy = " ORDER BY updated_at " + order + ", id " + order
	} else {
		from = `SELECT 0, i.content FROM object_instances i JOIN runs r ON r.id = i.run_id
			WHERE i.object_id = ? AND i.project_id = ? AND r.status = ? AND r.id <> ?`
		args = []any{object, project, runDone, run}
		if q.State != "" {
			from += " AND i.state = ?"
			args = append(args, q.State)
		}
		orderBy = " ORDER BY r.finished_at " + order + ", r.id " + order
	}
	// Selecting by state and when they were written alone, it reads until
	// it has enough and counts the rest.
	plain := len(where) == 0 && !q.lookup() && len(leaveOut) == 0
	var count int
	if plain {
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM ("+from+") c", args...).Scan(&count); err != nil {
			return queryResult{}, err
		}
	}
	rows, err := db.QueryContext(ctx, from+orderBy, args...)
	if err != nil {
		return queryResult{}, err
	}
	defer rows.Close()
	var picked []queryCandidate
	read, more := 0, false
	for rows.Next() {
		if plain && len(picked) >= q.limit() {
			break
		}
		// Ranking, it reads only so far; with enough to hand in, it counts
		// on only so far.
		if read >= searchedRows && (q.lookup() || len(picked) >= q.limit()) {
			more = true
			break
		}
		read++
		var c queryCandidate
		if err := rows.Scan(&c.id, &c.content); err != nil {
			return queryResult{}, err
		}
		if !json.Valid([]byte(c.content)) || leaveOut[c.id] || (len(where) > 0 && !where.holds(c.content)) {
			continue
		}
		picked = append(picked, c)
	}
	if err := rows.Err(); err != nil {
		return queryResult{}, err
	}
	if q.lookup() && q.Oldest {
		slices.Reverse(picked)
	}
	res, err := selected(picked, q, like, more)
	if plain {
		res.matched = count
	}
	return res, err
}

// The records of a shared struct's store its search, state and rule select,
// but those leaveOut names, of the latest the Records page searches
// (readRecordTable), so more past those.
func searchRecords(ctx context.Context, db *sql.DB, store int64, q *linkQuery, search, like string, where rule, leaveOut map[int64]bool) (queryResult, error) {
	rt, err := readRecordTable(ctx, db, store)
	if err != nil {
		return queryResult{}, err
	}
	s := rt.table.search()
	n := parseSearch(search)
	var picked []queryCandidate
	for i, rec := range rt.records {
		content := string(rec.Content)
		if (q.State != "" && rec.State != q.State) || leaveOut[rec.ID] || !s.matches(n, rt.table.rows[i]) || (len(where) > 0 && !where.holds(content)) {
			continue
		}
		picked = append(picked, queryCandidate{rec.ID, content})
	}
	// The table holds the latest first.
	if q.Oldest {
		slices.Reverse(picked)
	}
	return selected(picked, q, like, rt.truncated)
}

// What the query hands in of the records it selected, in its order: if it
// ranks them, those sharing a word with like, as filled in, the most like
// it first; at most its limit.
func selected(picked []queryCandidate, q *linkQuery, like string, more bool) (queryResult, error) {
	if q.lookup() {
		texts := make([]string, len(picked))
		for i, c := range picked {
			texts[i] = contentText(c.content)
		}
		order := likeOrder(texts, like)
		ranked := make([]queryCandidate, len(order))
		for i, j := range order {
			ranked[i] = picked[j]
		}
		picked = ranked
	}
	list := []json.RawMessage{}
	for _, c := range picked[:min(len(picked), q.limit())] {
		list = append(list, json.RawMessage(c.content))
	}
	b, err := json.Marshal(list)
	return queryResult{records: string(b), matched: len(picked), more: more}, err
}

// What a query names of what its process takes isn't there, so its step
// fails saying so.
type queryProblem struct{ msg string }

func (p *queryProblem) Error() string { return p.msg }

// A query's search or like with each value it names filled in from what
// the process takes, given, matched by name as a search's facets are: into
// a search as a quoted value, a string or a number, into like as the text
// the value holds, at any depth. A *queryProblem for a value of an object
// not given, or in a search, holding no text or number.
func fillQuery(text string, given []runInput, search bool) (string, error) {
	what := "like"
	if search {
		what = "search"
	}
	parts, msg := queryValues(what, text)
	if msg != "" {
		return "", &queryProblem{"Its " + msg + "."}
	}
	var b strings.Builder
	for _, p := range parts {
		if p.object == "" {
			b.WriteString(p.text)
			continue
		}
		i := slices.IndexFunc(given, func(in runInput) bool { return strings.EqualFold(slug(in.Name), slug(p.object)) })
		if i < 0 {
			return "", &queryProblem{fmt.Sprintf("Its query's %s names %s, which it wasn't given.", what, quote(p.object))}
		}
		content := given[i].Content
		field := ""
		if p.attribute != "" {
			if fields, ok := contentValue(content).(map[string]any); ok {
				for k := range fields {
					if strings.EqualFold(slug(k), slug(p.attribute)) {
						field = k
					}
				}
			}
			if field == "" {
				field = p.attribute
			}
		}
		if !search {
			if field == "" {
				b.WriteString(contentText(content))
			} else if fields, ok := contentValue(content).(map[string]any); ok {
				var t strings.Builder
				valueText(&t, fields[field])
				b.WriteString(t.String())
			}
			continue
		}
		v, why := branchText(content, field)
		if why != "" {
			name := quote(given[i].Name)
			if field != "" {
				name += "'s " + quote(field)
			}
			return "", &queryProblem{fmt.Sprintf("Its query's search names %s, which %s.", name, why)}
		}
		b.WriteString(`"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`)
	}
	return b.String(), nil
}

// The records of the store run holds, or a run it is in: those it took or
// followed (picks.go, references.go) and the one it was started on
// (record_runs.go), by id, with records of other stores, which no query of
// this one selects anyway.
func heldRecords(ctx context.Context, db *sql.DB, run int64) (map[int64]bool, error) {
	held := map[int64]bool{}
	for run != 0 {
		var record, parent sql.NullInt64
		err := db.QueryRowContext(ctx, "SELECT record_id, parent_run_id FROM runs WHERE id = ?", run).Scan(&record, &parent)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		if record.Valid {
			held[record.Int64] = true
		}
		err = scanAll(ctx, db, "SELECT record_id FROM taken_records WHERE run_id = ?", func(rows *sql.Rows) error {
			var id int64
			err := rows.Scan(&id)
			held[id] = true
			return err
		}, run)
		if err != nil {
			return nil, err
		}
		run = parent.Int64
	}
	return held, nil
}

// The inputs a process's query links give it, in object order, their
// search and like filled in from what it takes, given. A run started on a
// shared record reads that record alone of its store, whatever the query,
// but one looking records up (see record_runs.go). A *recordConflict,
// saying so, for a query of another project's store, which a run doesn't
// reach, and a *queryProblem for a value it names that isn't given.
func (w *walk) queried(id int64, given []runInput) ([]runInput, error) {
	var ins []runInput
	for _, qe := range w.queries[id] {
		store := w.fr.rb.store(qe.object)
		if store != 0 {
			project, err := objectProject(w.ctx, w.e.db, store)
			if err != nil {
				return nil, err
			}
			if project != 0 && project != w.fr.project {
				return nil, &recordConflict{fmt.Sprintf("Its query of %s reads another project's struct, which a run here can't reach.", quote(w.objects[qe.object]))}
			}
		}
		w.mu.Lock()
		name := w.objects[qe.object]
		w.mu.Unlock()
		q := qe.query
		in := runInput{Name: name, Role: inputRecords}
		var pinned bool
		var err error
		if !q.lookup() {
			in.Content, pinned, err = w.fr.pinnedRecord(w.ctx, w.e.db, qe.object)
			if err != nil {
				return nil, err
			}
		}
		if !pinned {
			search, err := fillQuery(q.Search, given, true)
			if err != nil {
				return nil, err
			}
			like, err := fillQuery(q.Like, given, false)
			if err != nil {
				return nil, err
			}
			var leaveOut map[int64]bool
			if q.lookup() && store != 0 {
				if leaveOut, err = heldRecords(w.ctx, w.e.db, w.run); err != nil {
					return nil, err
				}
			}
			res, err := queryRecords(w.ctx, w.e.db, w.fr.project, w.run, qe.object, store, q, search, like, leaveOut)
			if err != nil {
				return nil, err
			}
			in.Content, in.Matched, in.MatchedMore = res.records, res.matched, res.more
		}
		ins = append(ins, in)
	}
	return ins, nil
}

// What a step whose query couldn't read its records says: why, when it
// reads what a run doesn't reach or names what the process wasn't given.
func queryFailure(err error) string {
	var c *recordConflict
	if errors.As(err, &c) {
		return c.msg
	}
	var p *queryProblem
	if errors.As(err, &p) {
		return p.msg
	}
	return "Its query couldn't read the records."
}

type queryEnd struct {
	object int64
	query  *linkQuery
}

func registerQueryRoutes(mux *http.ServeMux, db *sql.DB) {
	// Sets an input link's query, or with null makes it a plain link again.
	mux.HandleFunc("PUT /api/links/{id}/query", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body struct {
			Query *linkQuery `json:"query"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		l := link{ID: id, Query: body.Query}
		var state int64
		err = db.QueryRowContext(r.Context(), "SELECT process_id, object_id, direction, state_id, role, is_condition FROM object_links WHERE id = ?", id).
			Scan(&l.Process, &l.Object, &l.Direction, &state, &l.Role, &l.Condition)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "link not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if state != 0 {
			l.State = &state
		}
		if msg, err := checkQuery(r.Context(), db, l); err != nil {
			serverError(w, err)
			return
		} else if msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		if _, err := db.ExecContext(r.Context(), "UPDATE object_links SET query = ? WHERE id = ?", storedQuery(l.Query), id); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
