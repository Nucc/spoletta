package main

// Task boards: shared structs' records laid out in columns, to follow the
// work flows and people move on, such as a column of Jira tickets still new
// beside one of pull requests in review. Each column is a shared struct and
// a query of its records in the Records page's search (search.go), with how
// its cards sort, so it holds the records that query matches as the Records
// page would list them, and whatever moves a record on, a run or a person
// correcting it, moves it from column to column. A record two columns'
// queries match shows in both. Each column picks what its cards show of a
// record (taskCard). Cards aren't ranked or dragged: a record's state is
// changed as on the Records page (records.go).
//
// Each is owned as a flow is (see owners.go): its maker's, a team's or
// everyone's; those it belongs to see it and change it, and admins
// everything. A column shows its records only to those who see its struct,
// so a team's board shows each member what they may see: a column of a
// struct they don't see, one in the trash, one deleted for good or one no
// longer shared says so in place of its records. Whoever saves a board may
// keep such a column's struct, but picks only shared structs they see. Task
// boards are people's, as owners are, so they stay out of versions and the
// setup file.

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
	"time"
	"unicode/utf8"
)

// A task board as its page and the main page show it.
type taskBoard struct {
	ID    int64     `json:"id"`
	Name  string    `json:"name"`
	Owner flowOwner `json:"owner"`
	// How far whoever asks may go with it: those who see it change it.
	Access    string       `json:"access"`
	Columns   []taskColumn `json:"columns"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

// A column of a task board: the struct whose records it holds, null once
// that is deleted for good, and the query picking them, with how they sort,
// as the Records page takes them.
type taskColumn struct {
	Name   string `json:"name"`
	Struct *int64 `json:"struct"`
	// The struct's name, empty for one whoever asks doesn't see.
	StructName string `json:"structName"`
	Query      string `json:"query"`
	Sort       string `json:"sort"`
	// What its cards show of each record; null for what they show unless
	// told: its key, the first other field, its state and when it was last
	// written.
	Card *taskCard `json:"card"`
	// Why it shows no records, if it doesn't: its struct is gone, in the
	// trash, not seen by whoever asks, or not a shared struct.
	Problem string `json:"problem,omitempty"`
	// Its records, on the board's own page.
	Cards *taskCards `json:"cards,omitempty"`
}

// What a column's cards show of each record: the fields picked, in order,
// the key heading the card and the first other field read as its title;
// and whether its state, when it was last written, and the run that last
// moved it on show. A field the struct no longer has shows nothing.
type taskCard struct {
	Fields  []string `json:"fields"`
	State   bool     `json:"state"`
	Updated bool     `json:"updated"`
	Run     bool     `json:"run"`
}

// How many fields a card shows at most.
const maxCardFields = 50

// Reads a column as stored, with what its cards show, after the columns
// before it in extra.
func scanColumn(rows *sql.Rows, c *taskColumn, extra ...any) error {
	var card sql.NullString
	if err := rows.Scan(append(extra, &c.Name, &c.Struct, &c.Query, &c.Sort, &card)...); err != nil {
		return err
	}
	if card.Valid {
		c.Card = &taskCard{}
		return json.Unmarshal([]byte(card.String), c.Card)
	}
	return nil
}

// The columns scanColumn reads, after those before them.
const columnColumns = "name, struct_id, query, sort, card"

// What a column shows problem says.
const (
	columnGone     = "gone"
	columnTrash    = "trash"
	columnHidden   = "hidden"
	columnUnshared = "unshared"
)

// The records a column's query matches, the first maxColumnCards of them,
// with what its cards and the dialog correcting one need of its struct.
type taskCards struct {
	Fields []string `json:"fields"`
	States []string `json:"states"`
	// The struct's first unique attribute, which names a record.
	Key     string         `json:"key"`
	Records []sharedRecord `json:"records"`
	// How many records the query matched among those searched, and whether
	// more are stored than a search reads.
	Matched   int  `json:"matched"`
	Truncated bool `json:"truncated"`
}

// How many cards a column shows at most; the Records page shows the rest.
const maxColumnCards = 200

// How many columns a board has at most, and how long a column's query and
// sort, and a name, may be.
const (
	maxTaskColumns = 30
	maxTaskQuery   = 4096
	maxTaskName    = 255
)

// The columns of a task board b, for scanTaskBoard.
var taskBoardColumns = "b.id, b.name, b.updated_at, " + ownerColumnsOf("b")

func scanTaskBoard(rows interface{ Scan(...any) error }) (taskBoard, error) {
	b := taskBoard{Access: accessEdit, Columns: []taskColumn{}}
	var user, team sql.NullInt64
	var owner string
	err := rows.Scan(&b.ID, &b.Name, &b.UpdatedAt, &user, &team, &owner)
	b.Owner = scanOwner(user, team, owner)
	return b, err
}

// The task board with the id, if the user sees it.
func visibleTaskBoard(ctx context.Context, db *sql.DB, u *user, id int64) (taskBoard, error) {
	cond, args := ownedBy("b", u)
	b, err := scanTaskBoard(db.QueryRowContext(ctx, "SELECT "+taskBoardColumns+" FROM task_boards b WHERE b.id = ? AND "+cond,
		append([]any{id}, args...)...))
	if err != nil {
		return b, err
	}
	err = scanAll(ctx, db, "SELECT "+columnColumns+" FROM task_board_columns WHERE board_id = ? ORDER BY position",
		func(rows *sql.Rows) error {
			var c taskColumn
			err := scanColumn(rows, &c)
			b.Columns = append(b.Columns, c)
			return err
		}, b.ID)
	return b, err
}

// The name of the first unique attribute of a struct's object, which names
// its records; none for one without.
func recordKey(ctx context.Context, db querier, root int64) (string, error) {
	var key string
	err := db.QueryRowContext(ctx, "SELECT name FROM object_attributes WHERE object_id = ? AND is_unique ORDER BY id LIMIT 1", root).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return key, err
}

// A struct as a task board's columns see it: its name, the object holding
// its records, and why its records don't show to whoever asks, if they
// don't (problem). A struct nobody shows them is named only to those who
// see it, or who may restore it from the trash.
type taskStruct struct {
	name, problem string
	root          int64
}

// The structs with the ids, as the user sees them; one that doesn't exist
// is gone.
func taskStructs(ctx context.Context, db *sql.DB, u *user, ids []int64) (map[int64]taskStruct, error) {
	out := map[int64]taskStruct{}
	if len(ids) == 0 {
		return out, nil
	}
	edit, editArgs := editableFlows(u)
	see, seeArgs := visibleFlows(u)
	args := append(append([]any{}, editArgs...), seeArgs...)
	for _, id := range ids {
		args = append(args, id)
	}
	err := scanAll(ctx, db, `SELECT f.id, f.name, f.deleted_at IS NOT NULL, `+edit+`, `+see+`,
			COALESCE(o.id, 0), COALESCE(f.is_struct AND o.store_id = o.id, FALSE)
		FROM flows f LEFT JOIN objects o ON o.id = (SELECT MIN(r.id) FROM objects r WHERE r.flow_id = f.id AND r.parent_id IS NULL AND r.owner_id IS NULL)
		WHERE f.id IN (?`+strings.Repeat(", ?", len(ids)-1)+`)`,
		func(rows *sql.Rows) error {
			var id int64
			var s taskStruct
			var trashed, editable, visible, shared bool
			if err := rows.Scan(&id, &s.name, &trashed, &editable, &visible, &s.root, &shared); err != nil {
				return err
			}
			switch {
			case trashed && editable:
				s.problem = columnTrash
			case !visible:
				s.name, s.problem = "", columnHidden
			case !shared:
				s.problem = columnUnshared
			}
			out[id] = s
			return nil
		}, args...)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, ok := out[id]; !ok {
			out[id] = taskStruct{problem: columnGone}
		}
	}
	return out, nil
}

// Names each column's struct and says why its records don't show to the
// user, if they don't.
func describeColumns(ctx context.Context, db *sql.DB, u *user, columns []taskColumn) (map[int64]taskStruct, error) {
	var ids []int64
	for _, c := range columns {
		if c.Struct != nil {
			ids = append(ids, *c.Struct)
		}
	}
	structs, err := taskStructs(ctx, db, u, ids)
	if err != nil {
		return nil, err
	}
	for i := range columns {
		c := &columns[i]
		if c.Struct == nil {
			c.Problem = columnGone
			continue
		}
		s := structs[*c.Struct]
		c.StructName, c.Problem = s.name, s.problem
	}
	return structs, nil
}

// Fills in the cards of each column showing its records, reading each
// struct's records once however many columns hold them. Matching doesn't
// count facets' values, as the Records page matches before it does, so a
// query matches the same records on both.
func fillCards(ctx context.Context, db *sql.DB, columns []taskColumn, structs map[int64]taskStruct) error {
	tables := map[int64]*recordTable{}
	keys := map[int64]string{}
	for i := range columns {
		c := &columns[i]
		if c.Problem != "" {
			continue
		}
		root := structs[*c.Struct].root
		rt, ok := tables[root]
		if !ok {
			var err error
			if rt, err = readRecordTable(ctx, db, root); err != nil {
				return err
			}
			tables[root] = rt
			if keys[root], err = recordKey(ctx, db, root); err != nil {
				return err
			}
		}
		key, desc := sortParam(c.Sort)
		matched := rt.table.search().run(parseSearch(c.Query), key, desc)
		cards := &taskCards{Fields: rt.fields, States: rt.states, Key: keys[root], Records: []sharedRecord{},
			Matched: len(matched), Truncated: rt.truncated}
		for _, i := range matched[:min(len(matched), maxColumnCards)] {
			cards.Records = append(cards.Records, rt.records[i])
		}
		c.Cards = cards
	}
	return nil
}

// What a person saves of a board: its name and all its columns, in order.
type taskBoardEdit struct {
	Name    string `json:"name"`
	Columns []struct {
		Name   string    `json:"name"`
		Struct *int64    `json:"struct"`
		Query  string    `json:"query"`
		Sort   string    `json:"sort"`
		Card   *taskCard `json:"card"`
	} `json:"columns"`
}

// Trims what a person saves of a board and says what is wrong with it, if
// anything, but for its columns' structs.
func (e *taskBoardEdit) check() string {
	e.Name = strings.TrimSpace(e.Name)
	switch {
	case e.Name == "":
		return "a task board needs a name"
	case utf8.RuneCountInString(e.Name) > maxTaskName:
		return fmt.Sprintf("a name is at most %d characters", maxTaskName)
	case len(e.Columns) > maxTaskColumns:
		return fmt.Sprintf("a task board has at most %d columns", maxTaskColumns)
	}
	for i := range e.Columns {
		c := &e.Columns[i]
		c.Name, c.Query, c.Sort = strings.TrimSpace(c.Name), strings.TrimSpace(c.Query), strings.TrimSpace(c.Sort)
		switch {
		case c.Name == "":
			return fmt.Sprintf("column %d needs a name", i+1)
		case utf8.RuneCountInString(c.Name) > maxTaskName:
			return fmt.Sprintf("a column's name is at most %d characters", maxTaskName)
		case len(c.Query) > maxTaskQuery:
			return fmt.Sprintf("the query of %s is at most %d characters", c.Name, maxTaskQuery)
		case len(c.Sort) > maxTaskName:
			return fmt.Sprintf("the sort of %s is at most %d characters", c.Name, maxTaskName)
		}
		if c.Card == nil {
			continue
		}
		if len(c.Card.Fields) > maxCardFields {
			return fmt.Sprintf("the cards of %s show at most %d fields", c.Name, maxCardFields)
		}
		// Each field once, in the order picked.
		fields := []string{}
		for _, f := range c.Card.Fields {
			if f == "" || utf8.RuneCountInString(f) > maxTaskName {
				return fmt.Sprintf("the cards of %s show fields named by up to %d characters", c.Name, maxTaskName)
			}
			if !slices.Contains(fields, f) {
				fields = append(fields, f)
			}
		}
		c.Card.Fields = fields
	}
	return ""
}

func registerTaskBoardRoutes(mux *http.ServeMux, db *sql.DB) {
	notFound := func(w http.ResponseWriter) { http.Error(w, "task board not found", http.StatusNotFound) }
	get := func(w http.ResponseWriter, r *http.Request) (taskBoard, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return taskBoard{}, false
		}
		b, err := visibleTaskBoard(r.Context(), db, requestUser(r), id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			notFound(w)
			return b, false
		case err != nil:
			serverError(w, err)
			return b, false
		}
		return b, true
	}
	// The board as whoever asks sees it, its columns described, and with
	// cards, each column's records.
	answer := func(w http.ResponseWriter, r *http.Request, status int, b taskBoard, cards bool) {
		structs, err := describeColumns(r.Context(), db, requestUser(r), b.Columns)
		if err == nil && cards {
			err = fillCards(r.Context(), db, b.Columns, structs)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, status, b)
	}

	// The boards whoever asks sees, by name, their columns described but
	// without their records.
	mux.HandleFunc("GET /api/taskboards", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		u := requestUser(r)
		cond, args := ownedBy("b", u)
		out := []taskBoard{}
		err := scanAll(ctx, db, "SELECT "+taskBoardColumns+" FROM task_boards b WHERE "+cond+" ORDER BY b.name, b.id",
			func(rows *sql.Rows) error {
				b, err := scanTaskBoard(rows)
				out = append(out, b)
				return err
			}, args...)
		if err != nil {
			serverError(w, err)
			return
		}
		at := map[int64]int{}
		var ids []any
		for i, b := range out {
			at[b.ID] = i
			ids = append(ids, b.ID)
		}
		if len(ids) > 0 {
			err = scanAll(ctx, db, "SELECT board_id, "+columnColumns+" FROM task_board_columns WHERE board_id IN (?"+
				strings.Repeat(", ?", len(ids)-1)+") ORDER BY board_id, position",
				func(rows *sql.Rows) error {
					var board int64
					var c taskColumn
					if err := scanColumn(rows, &c, &board); err != nil {
						return err
					}
					b := &out[at[board]]
					b.Columns = append(b.Columns, c)
					return nil
				}, ids...)
		}
		// Every board's columns are described at once, then handed back.
		var all []taskColumn
		for _, b := range out {
			all = append(all, b.Columns...)
		}
		if err == nil {
			_, err = describeColumns(ctx, db, u, all)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		for i := range out {
			if n := len(out[i].Columns); n > 0 {
				out[i].Columns, all = all[:n], all[n:]
			}
		}
		writeJSON(w, http.StatusOK, out)
	})

	// The shared structs whoever asks sees, by name, to pick a column's
	// from, each with the fields its records have, in the board's order, and
	// the one naming them, to pick what its cards show.
	mux.HandleFunc("GET /api/taskboards/structs", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		cond, args := visibleFlows(requestUser(r))
		type option struct {
			ID     int64    `json:"id"`
			Name   string   `json:"name"`
			Fields []string `json:"fields"`
			Key    string   `json:"key"`
			root   int64
		}
		out := []option{}
		err := scanAll(ctx, db, `SELECT f.id, f.name, o.id FROM flows f
			JOIN objects o ON o.id = (SELECT MIN(r.id) FROM objects r WHERE r.flow_id = f.id AND r.parent_id IS NULL AND r.owner_id IS NULL)
			WHERE f.is_struct AND o.store_id = o.id AND `+cond+` ORDER BY f.name, f.id`,
			func(rows *sql.Rows) error {
				var o option
				err := rows.Scan(&o.ID, &o.Name, &o.root)
				out = append(out, o)
				return err
			}, args...)
		for i := range out {
			if err != nil {
				break
			}
			if out[i].Fields, err = recordFields(ctx, db, out[i].root); err == nil {
				out[i].Key, err = recordKey(ctx, db, out[i].root)
			}
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})

	// Makes one with no columns yet, its maker's team's when they are in one
	// only, else theirs, unless they give it to another: {"name", "owner"}.
	mux.HandleFunc("POST /api/taskboards", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name  string     `json:"name"`
			Owner *flowOwner `json:"owner"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		e := taskBoardEdit{Name: body.Name}
		if msg := e.check(); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		u := requestUser(r)
		userID, teamID, msg, err := newOwner(r.Context(), db, u, body.Owner)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg != "" {
			http.Error(w, msg, http.StatusForbidden)
			return
		}
		res, err := db.ExecContext(r.Context(), "INSERT INTO task_boards (name, owner_user_id, owner_team_id) VALUES (?, ?, ?)", e.Name, userID, teamID)
		if mysqlErrorIs(err, mysqlErrForeignKey) && body.Owner != nil {
			http.Error(w, "no such "+body.Owner.Kind, http.StatusBadRequest)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		id, err := res.LastInsertId()
		var b taskBoard
		if err == nil {
			b, err = visibleTaskBoard(r.Context(), db, nil, id)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, b)
	})

	// The board with each column's records.
	mux.HandleFunc("GET /api/taskboards/{id}", func(w http.ResponseWriter, r *http.Request) {
		if b, ok := get(w, r); ok {
			answer(w, r, http.StatusOK, b, true)
		}
	})

	// Saves the board's name and all its columns, in place of those it had:
	// {"name", "columns": [{"name", "struct", "query", "sort"}]}. A column
	// may keep a struct the board has that whoever saves doesn't see, or
	// that no longer keeps records, and none once it was deleted for good,
	// so the board can still be saved around it; only a struct new to the
	// board has to be a shared one they see.
	mux.HandleFunc("PUT /api/taskboards/{id}", func(w http.ResponseWriter, r *http.Request) {
		b, ok := get(w, r)
		if !ok {
			return
		}
		var e taskBoardEdit
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if msg := e.check(); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		kept, keptGone := map[int64]bool{}, false
		for _, c := range b.Columns {
			if c.Struct != nil {
				kept[*c.Struct] = true
			} else {
				keptGone = true
			}
		}
		var ids []int64
		for _, c := range e.Columns {
			switch {
			case c.Struct != nil:
				ids = append(ids, *c.Struct)
			case !keptGone:
				http.Error(w, fmt.Sprintf("pick the struct whose records %s holds", c.Name), http.StatusBadRequest)
				return
			}
		}
		structs, err := taskStructs(ctx, db, requestUser(r), ids)
		if err != nil {
			serverError(w, err)
			return
		}
		for _, c := range e.Columns {
			if c.Struct == nil {
				continue
			}
			s := structs[*c.Struct]
			if s.problem == "" || kept[*c.Struct] {
				continue
			}
			switch s.problem {
			case columnTrash:
				http.Error(w, fmt.Sprintf("%s holds the records of %s, which is in the trash", c.Name, s.name), http.StatusBadRequest)
			case columnUnshared:
				http.Error(w, fmt.Sprintf("%s isn't a shared struct, so it keeps no records for %s to hold; share its object first", s.name, c.Name), http.StatusBadRequest)
			default:
				http.Error(w, fmt.Sprintf("the struct %s holds the records of isn't there", c.Name), http.StatusBadRequest)
			}
			return
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, "UPDATE task_boards SET name = ?, updated_at = CURRENT_TIMESTAMP(3) WHERE id = ?", e.Name, b.ID); err != nil {
			serverError(w, err)
			return
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM task_board_columns WHERE board_id = ?", b.ID); err != nil {
			serverError(w, err)
			return
		}
		for i, c := range e.Columns {
			var card []byte
			if c.Card != nil {
				if card, err = json.Marshal(c.Card); err != nil {
					serverError(w, err)
					return
				}
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO task_board_columns (board_id, position, name, struct_id, query, sort, card) VALUES (?, ?, ?, ?, ?, ?, ?)",
				b.ID, i, c.Name, c.Struct, c.Query, c.Sort, card)
			if mysqlErrorIs(err, mysqlErrForeignKey) {
				http.Error(w, fmt.Sprintf("the struct %s holds the records of isn't there", c.Name), http.StatusBadRequest)
				return
			}
			if err != nil {
				serverError(w, err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		if b, err = visibleTaskBoard(ctx, db, nil, b.ID); err != nil {
			serverError(w, err)
			return
		}
		answer(w, r, http.StatusOK, b, true)
	})

	// Deletes one for good; the records its columns held stay their structs'.
	mux.HandleFunc("DELETE /api/taskboards/{id}", func(w http.ResponseWriter, r *http.Request) {
		b, ok := get(w, r)
		if !ok {
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM task_boards WHERE id = ?", b.ID); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Gives one to someone, a team, or everyone, as a flow is given.
	mux.HandleFunc("PUT /api/taskboards/{id}/owner", func(w http.ResponseWriter, r *http.Request) {
		b, ok := get(w, r)
		if !ok {
			return
		}
		var to flowOwner
		if err := json.NewDecoder(r.Body).Decode(&to); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		msg, err := refuseOwner(r.Context(), db, requestUser(r), b.Owner, to)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg != "" {
			http.Error(w, msg, http.StatusForbidden)
			return
		}
		var userID, teamID *int64
		switch to.Kind {
		case ownerUser:
			userID = &to.ID
		case ownerTeam:
			teamID = &to.ID
		}
		_, err = db.ExecContext(r.Context(), "UPDATE task_boards SET owner_user_id = ?, owner_team_id = ? WHERE id = ?", userID, teamID, b.ID)
		if mysqlErrorIs(err, mysqlErrForeignKey) {
			http.Error(w, "no such "+to.Kind, http.StatusBadRequest)
			return
		}
		if err == nil {
			b, err = visibleTaskBoard(r.Context(), db, nil, b.ID)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		answer(w, r, http.StatusOK, b, false)
	})
}
