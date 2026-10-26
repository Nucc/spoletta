package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// A version is a saved snapshot of a flow's whole board: its processes, objects
// with their states and attributes, links, connections and start node, and
// the flow's settings. Saving always adds a version on top, and jumping to
// one rewrites the board as it was, leaving the settings, so no version is
// ever lost. The board
// keeps its rows' ids across jumps: runs refer to processes and objects by
// id, and persistent objects remember what runs stored by object and
// attribute id. A flow may publish one of its versions for the runs nobody
// starts by hand to take; see publish.go.
type version struct {
	ID int64 `json:"id"`
	// The version the board was on when this one was saved, if any; a save
	// after jumping back is based on an older version.
	BasedOn   *int64    `json:"basedOn"`
	CreatedAt time.Time `json:"createdAt"`
	// Who saved it, if someone did with everyone signing in, and why: empty
	// when a person saved it, or else what saved it on its own first; see
	// saving. The note says why, as the person saving it put it.
	SavedBy string `json:"savedBy"`
	Reason  string `json:"reason"`
	Note    string `json:"note"`
}

// How a version comes to be saved: by whom, if anyone signing in, with what
// note, and why, when not because a person saved it.
type saving struct {
	by     *int64
	note   string
	reason string
}

// Why a version was saved on its own: before a run on the board as it was,
// such as one a subflow process started, and before the assistant or a
// setup file changed the flow.
const (
	savedForRun       = "run"
	savedForAssistant = "assistant"
	savedForImport    = "import"
)

// How long a version's note may be.
const maxVersionNote = 2000

type snapshotObject struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	X          float64  `json:"x"`
	Y          float64  `json:"y"`
	Width      *float64 `json:"width"`
	Persistent bool     `json:"persistent"`
	// The shared object whose records it reads and writes: its own id for a
	// struct's object shared by its copies, and theirs; left out when not,
	// as in versions from before objects were shared.
	Store *int64 `json:"store,omitempty"`
	Many  bool   `json:"many"`
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
	// Versions from before roles have none.
	Role      string `json:"role,omitempty"`
	Condition bool   `json:"condition,omitempty"`
	// Versions from before queries have none.
	Query *linkQuery `json:"query,omitempty"`
}

type snapshotLinkState struct {
	Link  int64 `json:"link"`
	State int64 `json:"state"`
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
	// The flow's settings, its repositories say, which a run of the version
	// works with and a diff compares; versions from before they were kept
	// have none. Bringing the board back to a version, or undoing, leaves
	// the flow's settings as they are, and history steps keep none.
	Settings *flowSettings `json:"settings,omitempty"`
}

// Scans every row of query with scan, which reads the current row.
func scanAll(ctx context.Context, tx rowsQuerier, query string, scan func(*sql.Rows) error, args ...any) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func takeSnapshot(ctx context.Context, tx *sql.Tx, flow int64) (snapshot, error) {
	s := snapshot{
		Processes:   []process{},
		Objects:     []snapshotObject{},
		States:      []objectState{},
		Attributes:  []attribute{},
		Links:       []snapshotLink{},
		LinkStates:  []snapshotLinkState{},
		Connections: []connection{},
		Boards:      []innerBoard{},
	}
	var startRoute []byte
	var endX, endY *float64
	s.Settings = &flowSettings{}
	err := tx.QueryRowContext(ctx, `SELECT s.x, s.y, s.target_id, s.route, s.end_x, s.end_y, f.settings FROM flow_starts s
		JOIN flows f ON f.id = s.flow_id WHERE s.flow_id = ?`, flow).
		Scan(&s.Start.X, &s.Start.Y, &s.Start.Target, &startRoute, &endX, &endY, s.Settings)
	if err != nil {
		return s, err
	}
	s.Start.Route = routeJSON(startRoute)
	if endX != nil && endY != nil {
		s.End = &boardEnd{X: *endX, Y: *endY}
	}

	err = scanAll(ctx, tx, "SELECT "+processColumns+" FROM processes WHERE flow_id = ? ORDER BY id", func(rows *sql.Rows) error {
		var p process
		err := scanProcess(rows, &p)
		p.Flow = 0
		s.Processes = append(s.Processes, p)
		return err
	}, flow)
	if err != nil {
		return s, err
	}
	err = scanAll(ctx, tx, "SELECT id, name, x, y, width, persistent, store_id, many, min_items, max_items, human, at_end, parent_id, owner_id, relation, general_id, source_id FROM objects WHERE flow_id = ? ORDER BY id", func(rows *sql.Rows) error {
		var o snapshotObject
		err := rows.Scan(&o.ID, &o.Name, &o.X, &o.Y, &o.Width, &o.Persistent, &o.Store, &o.Many, &o.Min, &o.Max, &o.Human, &o.End, &o.Parent, &o.Owner, &o.Relation, &o.General, &o.Source)
		s.Objects = append(s.Objects, o)
		return err
	}, flow)
	if err != nil {
		return s, err
	}
	err = scanAll(ctx, tx, `SELECT st.id, st.object_id, st.name, COALESCE(st.rule, ''), st.source_id FROM object_states st
		JOIN objects o ON o.id = st.object_id WHERE o.flow_id = ? ORDER BY st.id`, func(rows *sql.Rows) error {
		var st objectState
		err := rows.Scan(&st.ID, &st.Object, &st.Name, &st.Rule, &st.Source)
		s.States = append(s.States, st)
		return err
	}, flow)
	if err != nil {
		return s, err
	}
	err = scanAll(ctx, tx, `SELECT a.id, a.object_id, a.name, a.definition, a.is_unique, a.is_file, a.source_id FROM object_attributes a
		JOIN objects o ON o.id = a.object_id WHERE o.flow_id = ? ORDER BY a.id`, func(rows *sql.Rows) error {
		var a attribute
		err := rows.Scan(&a.ID, &a.Object, &a.Name, &a.Definition, &a.Unique, &a.File, &a.Source)
		s.Attributes = append(s.Attributes, a)
		return err
	}, flow)
	if err != nil {
		return s, err
	}
	err = scanAll(ctx, tx, `SELECT l.id, l.process_id, l.object_id, l.direction, l.state_id, l.route, l.at_x, l.at_y, l.role, l.is_condition, l.query FROM object_links l
		JOIN processes p ON p.id = l.process_id WHERE p.flow_id = ? ORDER BY l.id`, func(rows *sql.Rows) error {
		var l snapshotLink
		var route, query []byte
		var atX, atY sql.NullFloat64
		if err := rows.Scan(&l.ID, &l.Process, &l.Object, &l.Direction, &l.State, &route, &atX, &atY, &l.Role, &l.Condition, &query); err != nil {
			return err
		}
		var err error
		l.Query, err = queryOf(query)
		l.Route = routeJSON(route)
		l.At = pointOf(atX, atY)
		s.Links = append(s.Links, l)
		return err
	}, flow)
	if err != nil {
		return s, err
	}
	err = scanAll(ctx, tx, `SELECT ls.link_id, ls.state_id FROM link_states ls
		JOIN object_links l ON l.id = ls.link_id JOIN processes p ON p.id = l.process_id
		WHERE p.flow_id = ? ORDER BY ls.link_id, ls.state_id`, func(rows *sql.Rows) error {
		var ls snapshotLinkState
		err := rows.Scan(&ls.Link, &ls.State)
		s.LinkStates = append(s.LinkStates, ls)
		return err
	}, flow)
	if err != nil {
		return s, err
	}
	err = scanAll(ctx, tx, `SELECT c.id, c.source_id, c.target_id, c.kind FROM connections c
		JOIN processes p ON p.id = c.source_id WHERE p.flow_id = ? ORDER BY c.id`, func(rows *sql.Rows) error {
		var c connection
		err := rows.Scan(&c.ID, &c.Source, &c.Target, &c.Kind)
		s.Connections = append(s.Connections, c)
		return err
	}, flow)
	if err != nil {
		return s, err
	}
	err = scanAll(ctx, tx, `SELECT b.process_id, b.x, b.y, b.target_id, b.route, b.end_x, b.end_y FROM inner_boards b
		JOIN processes p ON p.id = b.process_id WHERE p.flow_id = ? ORDER BY b.process_id`, func(rows *sql.Rows) error {
		var b innerBoard
		var route []byte
		err := rows.Scan(&b.Process, &b.Start.X, &b.Start.Y, &b.Start.Target, &route, &b.End.X, &b.End.Y)
		b.Start.Route = routeJSON(route)
		s.Boards = append(s.Boards, b)
		return err
	}, flow)
	return s, err
}

// The processes with the ones on the top board first, then the ones inside
// them, and so on, so each is saved after the flow process it is in.
func parentsFirst(ps []process) []process {
	placed := map[int64]bool{}
	var ordered []process
	for len(ordered) < len(ps) {
		before := len(ordered)
		for _, p := range ps {
			if !placed[p.ID] && (p.Parent == nil || placed[*p.Parent]) {
				placed[p.ID] = true
				ordered = append(ordered, p)
			}
		}
		// Processes whose parent isn't saved can't be placed; they go last,
		// and the foreign key turns them down.
		if len(ordered) == before {
			for _, p := range ps {
				if !placed[p.ID] {
					placed[p.ID] = true
					ordered = append(ordered, p)
				}
			}
		}
	}
	return ordered
}

// The objects with the ones owned by none first, then the ones they own,
// and so on, so each is saved after its owner.
func ownersFirst(os []snapshotObject) []snapshotObject {
	placed := map[int64]bool{}
	var ordered []snapshotObject
	for len(ordered) < len(os) {
		before := len(ordered)
		for _, o := range os {
			if !placed[o.ID] && (o.Owner == nil || placed[*o.Owner]) {
				placed[o.ID] = true
				ordered = append(ordered, o)
			}
		}
		// Objects whose owner isn't saved go last, and the foreign key turns
		// them down.
		if len(ordered) == before {
			for _, o := range os {
				if !placed[o.ID] {
					placed[o.ID] = true
					ordered = append(ordered, o)
				}
			}
		}
	}
	return ordered
}

// A route as stored: NULL for the default route.
func storedRoute(r json.RawMessage) any {
	if len(r) == 0 || string(r) == "null" {
		return nil
	}
	return []byte(r)
}

// Deletes the flow's rows of table whose id isn't among keep.
func deleteOthers(ctx context.Context, tx *sql.Tx, flow int64, table string, keep []int64) error {
	args := []any{flow}
	cond := ""
	if len(keep) > 0 {
		args = append(args, int64Args(keep)...)
		cond = " AND id NOT IN (?" + strings.Repeat(", ?", len(keep)-1) + ")"
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE flow_id = ?"+cond, args...)
	return err
}

// Rewrites the flow's board as s has it. Processes and objects are updated
// in place rather than recreated, so the runs referring to them keep doing
// so. Rows nothing outside the board refers to are recreated with their
// saved ids. Other flows stay as they are.
func restoreSnapshot(ctx context.Context, tx *sql.Tx, flow int64, s snapshot) error {
	// Cleared first so their unique keys can't collide while being rewritten.
	for _, del := range []string{
		`DELETE ls FROM link_states ls JOIN object_links l ON l.id = ls.link_id
			JOIN processes p ON p.id = l.process_id WHERE p.flow_id = ?`,
		"DELETE l FROM object_links l JOIN processes p ON p.id = l.process_id WHERE p.flow_id = ?",
		"DELETE c FROM connections c JOIN processes p ON p.id = c.source_id WHERE p.flow_id = ?",
		"DELETE a FROM object_attributes a JOIN objects o ON o.id = a.object_id WHERE o.flow_id = ?",
		"DELETE st FROM object_states st JOIN objects o ON o.id = st.object_id WHERE o.flow_id = ?",
		"DELETE b FROM inner_boards b JOIN processes p ON p.id = b.process_id WHERE p.flow_id = ?",
	} {
		if _, err := tx.ExecContext(ctx, del, flow); err != nil {
			return err
		}
	}

	keep := make([]int64, len(s.Processes))
	for i, p := range s.Processes {
		keep[i] = p.ID
	}
	if err := deleteOthers(ctx, tx, flow, "processes", keep); err != nil {
		return err
	}
	keep = make([]int64, len(s.Objects))
	for i, o := range s.Objects {
		keep[i] = o.ID
	}
	if err := deleteOthers(ctx, tx, flow, "objects", keep); err != nil {
		return err
	}

	// Ids are never shared between flows, so these only ever update the flow's own rows.
	for _, p := range parentsFirst(s.Processes) {
		// Sentry processes, which versions from before they were retired
		// hold, come back as agent processes, as the database's became.
		if p.Kind == "sentry" {
			p.Kind = kindAgent
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO processes (id, flow_id, name, kind, prompt, script, model, thinking, x, y, width, parent_id, recording, settings)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) AS saved
			ON DUPLICATE KEY UPDATE name = saved.name, kind = saved.kind, prompt = saved.prompt, script = saved.script,
				model = saved.model, thinking = saved.thinking, x = saved.x, y = saved.y, width = saved.width,
				parent_id = saved.parent_id, recording = saved.recording, settings = saved.settings`,
			p.ID, flow, p.Name, p.Kind, p.Prompt, p.Script, p.Model, p.Thinking, p.X, p.Y, p.Width, p.Parent, p.Recording, p.Settings)
		if err != nil {
			return err
		}
	}
	// Generals go in once every object is there, since one may come later.
	for _, o := range ownersFirst(s.Objects) {
		_, err := tx.ExecContext(ctx, `INSERT INTO objects (id, flow_id, name, x, y, width, persistent, many, min_items, max_items, human, at_end, parent_id, owner_id, relation, general_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL) AS saved
			ON DUPLICATE KEY UPDATE name = saved.name, x = saved.x, y = saved.y, width = saved.width, persistent = saved.persistent,
				many = saved.many, min_items = saved.min_items, max_items = saved.max_items, human = saved.human, at_end = saved.at_end, parent_id = saved.parent_id,
				owner_id = saved.owner_id, relation = saved.relation, general_id = NULL`,
			o.ID, flow, o.Name, o.X, o.Y, o.Width, o.Persistent, o.Many, o.Min, o.Max, o.Human, o.End, o.Parent, o.Owner, o.Relation)
		if err != nil {
			return err
		}
	}
	for _, o := range s.Objects {
		if o.General != nil {
			if _, err := tx.ExecContext(ctx, "UPDATE objects SET general_id = ? WHERE id = ?", o.General, o.ID); err != nil {
				return err
			}
		}
	}
	// A copy whose source is gone since is an object of its own.
	for _, o := range s.Objects {
		var source *int64
		if o.Source != nil {
			if err := tx.QueryRowContext(ctx, "SELECT id FROM objects WHERE id = ?", *o.Source).Scan(&source); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE objects SET source_id = ? WHERE id = ?", source, o.ID); err != nil {
			return err
		}
		// A copy's store is its source's again once copies are synced.
		if _, err := tx.ExecContext(ctx, "UPDATE objects SET store_id = ? WHERE id = ?", o.Store, o.ID); err != nil {
			return err
		}
	}
	for _, st := range s.States {
		if _, err := tx.ExecContext(ctx, "INSERT INTO object_states (id, object_id, name, rule, source_id) VALUES (?, ?, ?, ?, ?)", st.ID, st.Object, st.Name, st.Rule, st.Source); err != nil {
			return err
		}
	}
	for _, a := range s.Attributes {
		_, err := tx.ExecContext(ctx, "INSERT INTO object_attributes (id, object_id, name, definition, is_unique, is_file, source_id) VALUES (?, ?, ?, ?, ?, ?, ?)",
			a.ID, a.Object, a.Name, a.Definition, a.Unique, a.File, a.Source)
		if err != nil {
			return err
		}
	}
	for _, l := range s.Links {
		var atX, atY sql.NullFloat64
		if l.At != nil {
			atX, atY = sql.NullFloat64{Float64: l.At.X, Valid: true}, sql.NullFloat64{Float64: l.At.Y, Valid: true}
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO object_links (id, process_id, object_id, direction, state_id, route, at_x, at_y, role, is_condition, query) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			l.ID, l.Process, l.Object, l.Direction, l.State, storedRoute(l.Route), atX, atY, l.Role, l.Condition, storedQuery(l.Query))
		if err != nil {
			return err
		}
	}
	for _, ls := range s.LinkStates {
		if _, err := tx.ExecContext(ctx, "INSERT INTO link_states (link_id, state_id) VALUES (?, ?)", ls.Link, ls.State); err != nil {
			return err
		}
	}
	for _, c := range s.Connections {
		if _, err := tx.ExecContext(ctx, "INSERT INTO connections (id, source_id, target_id, kind) VALUES (?, ?, ?, ?)", c.ID, c.Source, c.Target, c.Kind); err != nil {
			return err
		}
	}
	for _, b := range s.Boards {
		_, err := tx.ExecContext(ctx, "INSERT INTO inner_boards (process_id, x, y, target_id, route, end_x, end_y) VALUES (?, ?, ?, ?, ?, ?, ?)",
			b.Process, b.Start.X, b.Start.Y, b.Start.Target, storedRoute(b.Start.Route), b.End.X, b.End.Y)
		if err != nil {
			return err
		}
	}
	// A flow process saved before boards were kept still needs one.
	for _, p := range s.Processes {
		if p.Kind == kindFlow {
			if err := ensureBoard(ctx, tx, p.ID); err != nil {
				return err
			}
		}
	}
	var endX, endY *float64
	if s.End != nil {
		endX, endY = &s.End.X, &s.End.Y
	}
	_, err := tx.ExecContext(ctx, "UPDATE flow_starts SET x = ?, y = ?, target_id = ?, route = ?, end_x = ?, end_y = ? WHERE flow_id = ?",
		s.Start.X, s.Start.Y, s.Start.Target, storedRoute(s.Start.Route), endX, endY, flow)
	return err
}

// Locks the flow, against a run starting on it or another rewrite of its
// board meanwhile, and says whether a run of the board is going. Rewriting
// the board while a run that started on it goes is refused, as it was when
// runs read the board as they went. A run of the version published doesn't
// hold the board, which is its draft; nor does a run started on the board
// once it was published unchanged, which runs that version too.
func lockBoard(ctx context.Context, tx *sql.Tx, flow int64) (bool, error) {
	var published *int64
	if err := tx.QueryRowContext(ctx, "SELECT published_version_id FROM flows WHERE id = ? FOR UPDATE", flow).Scan(&published); err != nil {
		return false, err
	}
	var running int
	err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM runs WHERE flow_id = ? AND status = 'running' AND NOT version_id <=> ?", flow, published).Scan(&running)
	return running > 0, err
}

// Rewrites the flow's board as s has it, locked by lockBoard. Its copies of
// modules' objects follow them as they are now, and the copies of its own
// objects follow them as they were.
func rewriteBoard(ctx context.Context, tx *sql.Tx, flow int64, s snapshot) error {
	if err := restoreSnapshot(ctx, tx, flow, s); err != nil {
		return err
	}
	return syncCopies(ctx, tx, flow, true)
}

// Whether two snapshots hold the same board. Compared as decoded JSON, since
// the database rewrites stored JSON's spacing, key order and numbers.
func sameSnapshot(a, b []byte) (bool, error) {
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, &y); err != nil {
		return false, err
	}
	return reflect.DeepEqual(x, y), nil
}

// Saves the flow's board as a new version on top of the one it is on, and
// makes it current, as saved says. With reuse, a board unchanged since its
// current version stays on it instead; the version saved or kept is
// returned.
func saveVersion(ctx context.Context, tx *sql.Tx, flow int64, reuse bool, saved saving) (int64, error) {
	var current *int64
	// Locked so that two saves can't both be based on the same version unnoticed.
	if err := tx.QueryRowContext(ctx, "SELECT version_id FROM flows WHERE id = ? FOR UPDATE", flow).Scan(&current); err != nil {
		return 0, err
	}
	s, err := takeSnapshot(ctx, tx, flow)
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(s)
	if err != nil {
		return 0, err
	}
	if reuse && current != nil {
		var saved []byte
		if err := tx.QueryRowContext(ctx, "SELECT snapshot FROM flow_versions WHERE id = ?", *current).Scan(&saved); err != nil {
			return 0, err
		}
		same, err := sameSnapshot(body, saved)
		if err != nil || same {
			return *current, err
		}
	}
	res, err := tx.ExecContext(ctx, "INSERT INTO flow_versions (flow_id, based_on, snapshot, saved_by, reason, note) VALUES (?, ?, ?, ?, ?, ?)",
		flow, current, body, saved.by, saved.reason, saved.note)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := saveVersionRoles(ctx, tx, id, s); err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE flows SET version_id = ? WHERE id = ?", id, flow)
	return id, err
}

func registerVersionRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/versions", func(w http.ResponseWriter, r *http.Request) {
		f, ok := flowParam(w, r)
		if !ok {
			return
		}
		res := struct {
			// The version the board was last saved as or jumped to; null before the first save.
			Current *int64 `json:"current"`
			publishedState
			Versions []version `json:"versions"`
		}{Versions: []version{}}
		ctx := r.Context()
		err := inTx(ctx, db, func(tx *sql.Tx) error {
			if err := tx.QueryRowContext(ctx, "SELECT version_id FROM flows WHERE id = ?", f).Scan(&res.Current); err != nil {
				return err
			}
			var err error
			if res.publishedState, err = publishedStateOf(ctx, tx, f); err != nil {
				return err
			}
			return scanAll(ctx, tx, "SELECT id, based_on, created_at, "+userLabelColumn("saved_by")+", reason, note FROM flow_versions WHERE flow_id = ? ORDER BY id DESC",
				func(rows *sql.Rows) error {
					var v version
					err := rows.Scan(&v.ID, &v.BasedOn, &v.CreatedAt, &v.SavedBy, &v.Reason, &v.Note)
					res.Versions = append(res.Versions, v)
					return err
				}, f)
		})
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})

	// Saves the board as a new version, with the note the body gives, if
	// any: {"note": "..."}. With "publish": true, it publishes the board
	// instead: saved as a version if it changed since its last, which the
	// note goes with, and that version published; see publish.go.
	mux.HandleFunc("POST /api/versions", func(w http.ResponseWriter, r *http.Request) {
		f, ok := flowParam(w, r)
		if !ok {
			return
		}
		var body struct {
			Note    string `json:"note"`
			Publish bool   `json:"publish"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		body.Note = strings.TrimSpace(body.Note)
		if len(body.Note) > maxVersionNote {
			http.Error(w, "a note holds at most "+strconv.Itoa(maxVersionNote)+" characters", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()
		if body.Publish {
			if s, err := isStruct(ctx, tx, f); err != nil || s {
				if err != nil {
					serverError(w, err)
				} else {
					http.Error(w, notInStruct, http.StatusBadRequest)
				}
				return
			}
		}

		var v version
		if v.ID, err = saveVersion(ctx, tx, f, body.Publish, saving{by: requestUserID(r), note: body.Note}); errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		} else if err != nil {
			serverError(w, err)
			return
		}
		if body.Publish {
			// A version kept as the board hadn't changed takes the note
			// unless it has one of its own.
			if _, err := tx.ExecContext(ctx, "UPDATE flow_versions SET note = ? WHERE id = ? AND note = ''", body.Note, v.ID); err != nil {
				serverError(w, err)
				return
			}
			if err := publishVersion(ctx, tx, f, v.ID, requestUserID(r)); err != nil {
				serverError(w, err)
				return
			}
		}
		err = tx.QueryRowContext(ctx, "SELECT based_on, created_at, "+userLabelColumn("saved_by")+", reason, note FROM flow_versions WHERE id = ?", v.ID).
			Scan(&v.BasedOn, &v.CreatedAt, &v.SavedBy, &v.Reason, &v.Note)
		if err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, v)
	})

	// The board as the version has it, such as for showing a run on the board it ran on.
	mux.HandleFunc("GET /api/versions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		var body []byte
		err = db.QueryRowContext(r.Context(), "SELECT snapshot FROM flow_versions WHERE id = ?", id).Scan(&body)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "version not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		var s snapshot
		if err := json.Unmarshal(body, &s); err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	})

	mux.HandleFunc("POST /api/versions/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()

		var f int64
		var body []byte
		err = tx.QueryRowContext(ctx, "SELECT flow_id, snapshot FROM flow_versions WHERE id = ?", id).Scan(&f, &body)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "version not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if running, err := lockBoard(ctx, tx, f); err != nil {
			serverError(w, err)
			return
		} else if running {
			http.Error(w, "can't switch versions while the flow is running", http.StatusConflict)
			return
		}

		var s snapshot
		if err := json.Unmarshal(body, &s); err != nil {
			serverError(w, err)
			return
		}
		if err := rewriteBoard(ctx, tx, f, s); err != nil {
			serverError(w, err)
			return
		}
		if _, err := tx.ExecContext(ctx, "UPDATE flows SET version_id = ? WHERE id = ?", id, f); err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
