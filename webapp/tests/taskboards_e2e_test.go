package tests

// Task boards: columns of shared structs' records, each a struct and a
// query, owned as flows are, each column showing only to those who see its
// struct.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"
)

type taskBoard struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Owner struct {
		Kind string `json:"kind"`
		ID   int64  `json:"id"`
	} `json:"owner"`
	Columns []taskColumn `json:"columns"`
}

type taskColumn struct {
	Name       string `json:"name"`
	Struct     *int64 `json:"struct"`
	StructName string `json:"structName"`
	Query      string `json:"query"`
	Sort       string `json:"sort"`
	Card       *struct {
		Fields  []string `json:"fields"`
		State   bool     `json:"state"`
		Updated bool     `json:"updated"`
		Run     bool     `json:"run"`
	} `json:"card"`
	Problem string `json:"problem"`
	Cards   *struct {
		Key     string         `json:"key"`
		States  []string       `json:"states"`
		Records []sharedRecord `json:"records"`
		Matched int            `json:"matched"`
	} `json:"cards"`
}

// A shared struct, its records told apart by key, with the other fields
// and states given; owned as owner says, or as a new flow is.
func taskStruct(a *app, name string, owner map[string]any, key string, fields []string, states ...string) structFlow {
	a.t.Helper()
	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": name, "struct": true, "owner": owner}, http.StatusCreated, &s)
	root := modObjects(a, s.ID)[0]
	a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": key, "unique": true}, http.StatusCreated, nil)
	for _, f := range fields {
		a.call("POST", fmt.Sprintf("/api/objects/%d/attributes", root.ID), map[string]any{"name": f}, http.StatusCreated, nil)
	}
	for _, st := range states {
		a.newState(root.ID, st)
	}
	a.call("PATCH", fmt.Sprintf("/api/objects/%d", root.ID), map[string]any{"shared": true}, http.StatusOK, nil)
	return s
}

func taskRecord(a *app, s int64, content map[string]any, state string) sharedRecord {
	a.t.Helper()
	var r sharedRecord
	a.call("POST", fmt.Sprintf("/api/structs/%d/records", s), map[string]any{"content": content, "state": state}, http.StatusCreated, &r)
	return r
}

// The values of a field of a column's cards, in the order they show.
func taskCards(t *testing.T, c taskColumn, field string) []string {
	t.Helper()
	if c.Cards == nil {
		t.Fatalf("column %s shows no cards: %q", c.Name, c.Problem)
	}
	var out []string
	for _, r := range c.Cards.Records {
		var fields map[string]any
		if err := json.Unmarshal(r.Content, &fields); err != nil {
			t.Fatalf("record %d holds %s: %v", r.ID, r.Content, err)
		}
		out = append(out, fmt.Sprint(fields[field]))
	}
	return out
}

// Sends a request answered with a board, which must get status want. Each
// answer is decoded afresh, as one leaves out a column's cards when it has
// none.
func taskBoardCall(a *app, method, path string, body any, want int) taskBoard {
	a.t.Helper()
	var b taskBoard
	a.call(method, path, body, want, &b)
	return b
}

func taskBoards(a *app) []taskBoard {
	a.t.Helper()
	var bs []taskBoard
	a.call("GET", "/api/taskboards", nil, http.StatusOK, &bs)
	return bs
}

func taskBoardIn(bs []taskBoard, id int64) (taskBoard, bool) {
	i := slices.IndexFunc(bs, func(b taskBoard) bool { return b.ID == id })
	if i < 0 {
		return taskBoard{}, false
	}
	return bs[i], true
}

// A board's columns hold the records their queries match, sorted as each
// says, and follow a record as it moves on; a column of a struct in the
// trash, or deleted for good, says so in place of its records.
func TestTaskBoardColumnsHoldRecords(t *testing.T) {
	a := newApp(t)
	n := time.Now().UnixNano()
	ticket := taskStruct(a, fmt.Sprint("Ticket", n), nil, "key", []string{"summary"}, "new", "review", "done")
	pr := taskStruct(a, fmt.Sprint("Pull request", n), nil, "number", []string{"title"}, "open", "in_review")
	var plain structFlow
	a.call("POST", "/api/flows", map[string]any{"name": fmt.Sprint("Note", n), "struct": true}, http.StatusCreated, &plain)

	t1 := taskRecord(a, ticket.ID, map[string]any{"key": "T-1", "summary": "Crash on start"}, "new")
	taskRecord(a, ticket.ID, map[string]any{"key": "T-2", "summary": "Hang"}, "new")
	taskRecord(a, ticket.ID, map[string]any{"key": "T-3", "summary": "Crash on exit"}, "done")
	taskRecord(a, pr.ID, map[string]any{"number": "7", "title": "Fix the crash"}, "in_review")
	taskRecord(a, pr.ID, map[string]any{"number": "8", "title": "Bump"}, "open")

	var b taskBoard
	b = taskBoardCall(a, "POST", "/api/taskboards", map[string]any{"name": " Sprint "}, http.StatusCreated)
	if b.Name != "Sprint" || len(b.Columns) != 0 {
		t.Fatalf("made %+v", b)
	}
	a.refused("POST", "/api/taskboards", map[string]any{"name": " "}, http.StatusBadRequest, "needs a name")

	path := fmt.Sprintf("/api/taskboards/%d", b.ID)
	columns := []map[string]any{
		{"name": "To do", "struct": ticket.ID, "query": "state:new", "sort": "-@key",
			"card": map[string]any{"fields": []string{"summary", "key", "summary"}, "updated": true, "run": true}},
		{"name": "Review", "struct": pr.ID, "query": "state:in_review"},
		{"name": "Crashes", "struct": ticket.ID, "query": "crash", "sort": "@key"},
	}
	b = taskBoardCall(a, "PUT", path, map[string]any{"name": "Sprint 1", "columns": columns}, http.StatusOK)
	if b.Name != "Sprint 1" || len(b.Columns) != 3 {
		t.Fatalf("saved %+v", b)
	}
	todo, review, crashes := b.Columns[0], b.Columns[1], b.Columns[2]
	if got := taskCards(t, todo, "key"); !slices.Equal(got, []string{"T-2", "T-1"}) || todo.Cards.Matched != 2 {
		t.Errorf("to do holds %v of %d", got, todo.Cards.Matched)
	}
	if todo.StructName != ticket.Name || todo.Cards.Key != "key" || !slices.Equal(todo.Cards.States, []string{"new", "review", "done"}) {
		t.Errorf("to do is of %q keyed by %q in %v", todo.StructName, todo.Cards.Key, todo.Cards.States)
	}
	if got := taskCards(t, review, "number"); !slices.Equal(got, []string{"7"}) || review.Cards.Key != "number" {
		t.Errorf("review holds %v keyed by %q", got, review.Cards.Key)
	}
	if got := taskCards(t, crashes, "key"); !slices.Equal(got, []string{"T-1", "T-3"}) {
		t.Errorf("crashes holds %v", got)
	}
	// What a column's cards show is kept as picked, each field once; a
	// column told nothing shows what cards show unless told.
	if c := todo.Card; c == nil || !slices.Equal(c.Fields, []string{"summary", "key"}) || c.State || !c.Updated || !c.Run {
		t.Errorf("to do's cards show %+v", c)
	}
	if review.Card != nil {
		t.Errorf("review's cards show %+v, told nothing", review.Card)
	}

	bad := func(c map[string]any, msg string) {
		t.Helper()
		a.refused("PUT", path, map[string]any{"name": "Sprint 1", "columns": []any{c}}, http.StatusBadRequest, msg)
	}
	bad(map[string]any{"name": "Notes", "struct": plain.ID}, "isn't a shared struct")
	bad(map[string]any{"name": "Lost", "struct": 1 << 40}, "isn't there")
	bad(map[string]any{"name": "Nothing"}, "pick the struct")
	bad(map[string]any{"name": " ", "struct": ticket.ID}, "needs a name")
	bad(map[string]any{"name": "Blank", "struct": ticket.ID, "card": map[string]any{"fields": []string{""}}}, "fields named by")
	a.refused("PUT", path, map[string]any{"name": "", "columns": columns}, http.StatusBadRequest, "needs a name")

	// A record moved on by hand leaves the column for another.
	a.call("PUT", fmt.Sprintf("/api/structs/%d/records/%d", ticket.ID, t1.ID),
		map[string]any{"content": map[string]any{"key": "T-1", "summary": "Crash on start"}, "state": "review"}, http.StatusOK, nil)
	b = taskBoardCall(a, "GET", path, nil, http.StatusOK)
	if got := taskCards(t, b.Columns[0], "key"); !slices.Equal(got, []string{"T-2"}) {
		t.Errorf("to do holds %v once T-1 is in review", got)
	}

	// The list names each column's struct, without its records.
	listed, ok := taskBoardIn(taskBoards(a), b.ID)
	if !ok || len(listed.Columns) != 3 || listed.Columns[1].StructName != pr.Name || listed.Columns[1].Cards != nil ||
		listed.Columns[0].Card == nil {
		t.Errorf("listed %+v", listed)
	}
	// The structs to pick from, with their fields to pick what cards show.
	type option struct {
		ID     int64    `json:"id"`
		Fields []string `json:"fields"`
		Key    string   `json:"key"`
	}
	var structs []option
	a.call("GET", "/api/taskboards/structs", nil, http.StatusOK, &structs)
	of := func(id int64) *option {
		if i := slices.IndexFunc(structs, func(s option) bool { return s.ID == id }); i >= 0 {
			return &structs[i]
		}
		return nil
	}
	if s := of(ticket.ID); s == nil || !slices.Equal(s.Fields, []string{"key", "summary"}) || s.Key != "key" || of(pr.ID) == nil || of(plain.ID) != nil {
		t.Errorf("the structs to pick from are %+v", structs)
	}

	// A struct in the trash shows none of its records; the column stays as
	// it is, but no board takes it anew.
	a.call("DELETE", fmt.Sprintf("/api/flows/%d", pr.ID), nil, http.StatusNoContent, nil)
	b = taskBoardCall(a, "GET", path, nil, http.StatusOK)
	if c := b.Columns[1]; c.Problem != "trash" || c.Cards != nil || c.StructName != pr.Name {
		t.Errorf("review in the trash is %+v", c)
	}
	a.call("PUT", path, map[string]any{"name": "Sprint 1", "columns": columns}, http.StatusOK, nil)
	var other taskBoard
	a.call("POST", "/api/taskboards", map[string]any{"name": "Other"}, http.StatusCreated, &other)
	otherPath := fmt.Sprintf("/api/taskboards/%d", other.ID)
	a.refused("PUT", otherPath, map[string]any{"name": "Other", "columns": []any{columns[1]}}, http.StatusBadRequest, "in the trash")

	// Deleted for good, its column holds nothing at all.
	a.call("DELETE", fmt.Sprintf("/api/trash/%d", pr.ID), nil, http.StatusNoContent, nil)
	b = taskBoardCall(a, "GET", path, nil, http.StatusOK)
	if c := b.Columns[1]; c.Struct != nil || c.Problem != "gone" || c.Cards != nil {
		t.Errorf("review deleted for good is %+v", c)
	}
	if got := taskCards(t, b.Columns[0], "key"); !slices.Equal(got, []string{"T-2"}) {
		t.Errorf("to do holds %v beside a column deleted for good", got)
	}
	// The board still saves with the column as it came, but no column takes
	// no struct anew.
	var kept []map[string]any
	for _, c := range b.Columns {
		kept = append(kept, map[string]any{"name": c.Name, "struct": c.Struct, "query": c.Query, "sort": c.Sort, "card": c.Card})
	}
	b = taskBoardCall(a, "PUT", path, map[string]any{"name": "Sprint 2", "columns": kept}, http.StatusOK)
	if c := b.Columns[1]; b.Name != "Sprint 2" || c.Struct != nil || c.Problem != "gone" || b.Columns[0].Card == nil {
		t.Errorf("renamed beside a column deleted for good: %q with %+v", b.Name, b.Columns)
	}
	a.refused("PUT", otherPath, map[string]any{"name": "Other", "columns": []any{map[string]any{"name": "Nothing"}}}, http.StatusBadRequest, "pick the struct")

	a.call("DELETE", path, nil, http.StatusNoContent, nil)
	a.refused("GET", path, nil, http.StatusNotFound, "task board not found")
	if _, ok := taskBoardIn(taskBoards(a), b.ID); ok {
		t.Errorf("a board deleted is still listed")
	}
	a.call("DELETE", otherPath, nil, http.StatusNoContent, nil)
}

// A team's board shows to its members alone, each column only to those who
// see its struct; a member keeps a column they don't see as it is, but
// adds none of a struct they don't see. A team owning a board stays until
// it goes elsewhere.
func TestTaskBoardOwners(t *testing.T) {
	a, p := usersApp(t)
	usersSignIn(a, p, usersAda)
	bob, carl, dave := usersNewPerson("Bob"), usersNewPerson("Carl"), usersNewPerson("Dave")
	bobRow, carlRow := usersAdd(a, bob.email), usersAdd(a, carl.email)
	usersAdd(a, dave.email)
	var tm teamsTeam
	a.call("POST", "/api/teams", map[string]any{"name": fmt.Sprint("Ops", bobRow.ID), "members": []int64{bobRow.ID, carlRow.ID}}, http.StatusCreated, &tm)

	usersSignIn(a, p, bob)
	ticket := taskStruct(a, fmt.Sprint("Ticket", bobRow.ID), map[string]any{"kind": "global"}, "key", nil, "new")
	secret := taskStruct(a, fmt.Sprint("Secret", bobRow.ID), map[string]any{"kind": "user", "id": bobRow.ID}, "key", nil, "new")
	taskRecord(a, ticket.ID, map[string]any{"key": "T-1"}, "new")
	taskRecord(a, secret.ID, map[string]any{"key": "S-1"}, "new")
	var b taskBoard
	b = taskBoardCall(a, "POST", "/api/taskboards", map[string]any{"name": "Ops", "owner": map[string]any{"kind": "team", "id": tm.ID}}, http.StatusCreated)
	if b.Owner.Kind != "team" || b.Owner.ID != tm.ID {
		t.Fatalf("a new board is owned by %+v", b.Owner)
	}
	path := fmt.Sprintf("/api/taskboards/%d", b.ID)
	columns := []map[string]any{
		{"name": "Tickets", "struct": ticket.ID, "query": "state:new"},
		{"name": "Secrets", "struct": secret.ID, "query": ""},
	}
	b = taskBoardCall(a, "PUT", path, map[string]any{"name": "Ops", "columns": columns}, http.StatusOK)
	if got := taskCards(t, b.Columns[1], "key"); !slices.Equal(got, []string{"S-1"}) {
		t.Errorf("bob's secrets column holds %v", got)
	}

	usersSignIn(a, p, carl)
	if _, ok := taskBoardIn(taskBoards(a), b.ID); !ok {
		t.Fatalf("carl doesn't see his team's board")
	}
	b = taskBoardCall(a, "GET", path, nil, http.StatusOK)
	if got := taskCards(t, b.Columns[0], "key"); !slices.Equal(got, []string{"T-1"}) {
		t.Errorf("carl's tickets column holds %v", got)
	}
	if c := b.Columns[1]; c.Problem != "hidden" || c.StructName != "" || c.Cards != nil {
		t.Errorf("carl sees bob's secrets column as %+v", c)
	}
	// Renaming it keeps the column Carl doesn't see.
	b = taskBoardCall(a, "PUT", path, map[string]any{"name": "Operations", "columns": columns}, http.StatusOK)
	if b.Name != "Operations" || len(b.Columns) != 2 || b.Columns[1].Struct == nil || *b.Columns[1].Struct != secret.ID {
		t.Errorf("carl's rename made %+v", b)
	}
	var mine taskBoard
	a.call("POST", "/api/taskboards", map[string]any{"name": "Carl's", "owner": map[string]any{"kind": "user", "id": carlRow.ID}}, http.StatusCreated, &mine)
	a.refused("PUT", fmt.Sprintf("/api/taskboards/%d", mine.ID), map[string]any{"name": "Carl's", "columns": columns[1:]}, http.StatusBadRequest, "isn't there")

	usersSignIn(a, p, dave)
	if _, ok := taskBoardIn(taskBoards(a), b.ID); ok {
		t.Errorf("dave sees the team's board")
	}
	a.refused("GET", path, nil, http.StatusNotFound, "task board not found")
	a.refused("PUT", path, map[string]any{"name": "Dave's", "columns": columns}, http.StatusNotFound, "task board not found")
	a.refused("DELETE", path, nil, http.StatusNotFound, "task board not found")
	a.refused("PUT", path+"/owner", map[string]any{"kind": "global"}, http.StatusNotFound, "task board not found")

	usersSignIn(a, p, usersAda)
	a.refused("DELETE", fmt.Sprintf("/api/teams/%d", tm.ID), nil, http.StatusConflict, "1 task boards")

	// Carl takes the team's board for himself; Bob no longer sees it.
	usersSignIn(a, p, carl)
	b = taskBoardCall(a, "PUT", path+"/owner", map[string]any{"kind": "user", "id": carlRow.ID}, http.StatusOK)
	if b.Owner.Kind != "user" || b.Owner.ID != carlRow.ID {
		t.Errorf("carl's board is owned by %+v", b.Owner)
	}
	usersSignIn(a, p, bob)
	a.refused("GET", path, nil, http.StatusNotFound, "task board not found")
}
