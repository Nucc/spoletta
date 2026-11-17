package main

// The inbox: every step waiting on the person signed in, across every
// project they may enter (projects.go) and every flow they see there,
// newest first: the reviews and inputs they may decide, and the loop
// questions they may answer, as decidesStep says of them as they are in
// each project (an owner, holding the roles they hold there, a viewer
// answering no loop question), so it lists just what the board would let
// them decide. Each names its project, and opens the board there on its run
// with its review showing. Without sign-in everyone decides everything, so
// it lists every step waiting in the request's project. The page counts
// them in the tab bar, asking again every so often.
//
// Each person also says here what they are told of, and how; see notify.go.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
)

// The most steps the inbox lists.
const maxInbox = 500

// A step waiting on the person.
type inboxItem struct {
	Step int64 `json:"step"`
	Run  int64 `json:"run"`
	// The project its run is in, which the board opens in, and its name.
	Project     int64  `json:"project"`
	ProjectName string `json:"projectName"`
	// The run's flow, which the board opens on, and the run it is within,
	// for a flow process's or a subflow's run.
	Flow     int64  `json:"flow"`
	FlowName string `json:"flowName"`
	Parent   *int64 `json:"parent"`
	// The process, kept after it is deleted, and its name then.
	Process     *int64 `json:"process"`
	ProcessName string `json:"processName"`
	// What it waits for: review, input or loop; see notify.go.
	Waits string `json:"waits"`
	// A loop step's question, or the human process's instructions.
	Detail string `json:"detail"`
	// The roles that decide it, none when anyone does, and whether it was
	// handed on to more, which they include; see notify.go.
	HandledBy []string `json:"handledBy"`
	Escalated bool     `json:"escalated"`
	// Who started its run, when someone did.
	StartedBy string    `json:"startedBy"`
	Since     time.Time `json:"since"`
	// When it fails if nobody decides it, if its process says.
	Expires *time.Time `json:"expires"`
}

// The projects whose steps wait on whoever asks: every one they may enter,
// as they are there; with nobody signing in, the request's, or every one
// if it names none, as a page outside the projects asks.
func inboxProjects(ctx context.Context, db *sql.DB, r *http.Request) ([]projectView, error) {
	u := requestUser(r)
	project, named := requestProjectIf(r)
	if u != nil || !named {
		return enterableProjects(ctx, db, u)
	}
	v, found, err := projectOf(ctx, db, nil, project)
	if err != nil || !found {
		return nil, err
	}
	return []projectView{v}, nil
}

// The steps waiting on the user, nil for nobody signed in, newest first, in
// the projects given, as they are there, and in those the flows given, or
// every flow without any. Who decides a review, and what it asks, are its
// run's board's to say.
func inbox(ctx context.Context, db *sql.DB, u *user, projects []projectView, flows ...int64) ([]inboxItem, error) {
	items := []inboxItem{}
	if len(projects) == 0 {
		return items, nil
	}
	// The flows they see, and whether they may change each, read along as
	// flowAccess would say it, so no flow takes a query of its own: every
	// flow of a project they own, and in the others what anyone in a
	// project sees there, each project's own teams and roles counting only
	// in it (ownedBy, visibleFlows, with nobody's project).
	in := map[int64]projectView{}
	var owned, others []int64
	for _, p := range projects {
		in[p.ID] = p
		if p.Level == levelOwner {
			owned = append(owned, p.ID)
		} else {
			others = append(others, p.ID)
		}
	}
	see, edit := "FALSE", "FALSE"
	var seeArgs, editArgs []any
	if len(owned) > 0 {
		see, edit = "f.project_id IN "+placeholders(len(owned)), "f.project_id IN "+placeholders(len(owned))
		seeArgs, editArgs = int64Args(owned), int64Args(owned)
	}
	if len(others) > 0 && u != nil {
		anyone := &user{ID: u.ID}
		s, sArgs := visibleFlows(anyone)
		e, eArgs := editableFlows(anyone)
		see = "(" + see + " OR (f.project_id IN " + placeholders(len(others)) + " AND " + s + "))"
		edit = "(" + edit + " OR (f.project_id IN " + placeholders(len(others)) + " AND " + e + "))"
		seeArgs = append(append(seeArgs, int64Args(others)...), sArgs...)
		editArgs = append(append(editArgs, int64Args(others)...), eArgs...)
	}
	see = liveFlows + " AND " + see
	args := append(append(editArgs, stepWaiting, kindHuman, kindLoop, runRunning), seeArgs...)
	if len(flows) > 0 {
		see += " AND r.flow_id IN " + placeholders(len(flows))
		args = append(args, int64Args(flows)...)
	}
	type waiting struct {
		inboxItem
		kind string
		// The run's version, which runs share; nil for a run from before
		// versions were recorded.
		version  *int64
		editable bool
	}
	var all []waiting
	err := scanAll(ctx, db,
		`SELECT s.id, s.run_id, f.project_id, r.flow_id, f.name, r.parent_run_id, r.version_id, s.process_ref, s.process_name, s.kind, s.prompt,
			`+userLabelColumn("r.started_by")+`, s.started_at, s.expires_at, s.escalated, `+edit+`
		FROM run_steps s JOIN runs r ON r.id = s.run_id JOIN flows f ON f.id = r.flow_id
		WHERE s.status = ? AND s.kind IN (?, ?) AND r.status = ? AND `+see+`
		ORDER BY s.started_at DESC, s.id DESC LIMIT ?`, func(rows *sql.Rows) error {
			var it waiting
			// A loop step asks its question.
			if err := rows.Scan(&it.Step, &it.Run, &it.Project, &it.Flow, &it.FlowName, &it.Parent, &it.version, &it.Process, &it.ProcessName, &it.kind, &it.Detail,
				&it.StartedBy, &it.Since, &it.Expires, &it.Escalated, &it.editable); err != nil {
				return err
			}
			all = append(all, it)
			return nil
		}, append(args, maxInbox)...)
	if err != nil {
		return nil, err
	}
	// The roles they hold, in each project.
	roles := map[int64][]string{}
	if u != nil {
		err := scanAll(ctx, db, "SELECT project_id, name FROM user_roles WHERE user_id = ? ORDER BY name", func(rows *sql.Rows) error {
			var p int64
			var n string
			err := rows.Scan(&p, &n)
			roles[p] = append(roles[p], n)
			return err
		}, u.ID)
		if err != nil {
			return nil, err
		}
	}
	// The boards of the runs, by version, or by the run, negated, for one
	// without a version.
	boards := map[int64]*runBoard{}
	for _, it := range all {
		p := in[it.Project]
		it.ProjectName = p.Name
		a := levelAccess(p.Level, accessReview)
		if it.editable {
			a = levelAccess(p.Level, accessEdit)
		}
		// As they are in the run's project.
		var as *user
		if u != nil {
			as = &user{ID: u.ID, Email: u.Email, Name: u.Name, Super: u.Super, TeamManager: u.TeamManager,
				Admin: p.Level == levelOwner, Project: p.ID}
		}
		it.HandledBy, it.Waits = []string{}, waitsLoop
		if it.kind == kindHuman {
			key := -it.Run
			if it.version != nil {
				key = *it.version
			}
			b, ok := boards[key]
			if !ok {
				if b, err = runBoardOf(ctx, db, it.Run); err != nil {
					return nil, err
				}
				boards[key] = b
			}
			var process int64
			if it.Process != nil {
				process = *it.Process
			}
			// A review has its process's instructions.
			it.HandledBy, it.Waits, it.Detail = b.deciders(process, it.Escalated), reviewWaits(b, process), b.processes[process].Prompt
			if it.HandledBy == nil {
				it.HandledBy = []string{}
			}
		}
		if decidesStep(as, roles[it.Project], a, it.kind, it.HandledBy) {
			items = append(items, it.inboxItem)
		}
	}
	return items, nil
}

// How many steps wait on the user in each of the flows of the project, by
// id, as their inbox lists them; a flow none waits on in isn't there.
func waitingOn(ctx context.Context, db *sql.DB, u *user, project int64, flows []int64) (map[int64]int, error) {
	waiting := map[int64]int{}
	if len(flows) == 0 {
		return waiting, nil
	}
	v, found, err := projectOf(ctx, db, u, project)
	if err != nil || !found || v.Level == "" {
		return waiting, err
	}
	items, err := inbox(ctx, db, u, []projectView{v}, flows...)
	for _, it := range items {
		waiting[it.Flow]++
	}
	return waiting, err
}

// What a person is told of, and how; and whether the webapp can tell them
// by email and on Slack at all.
type noticeSettings struct {
	Email    bool `json:"email"`
	Slack    bool `json:"slack"`
	Failures bool `json:"failures"`
	CanEmail bool `json:"canEmail"`
	CanSlack bool `json:"canSlack"`
}

// The person's settings, and whether the webapp can tell them of what is in
// the project the request names, whose Slack connector tells them there;
// none on Slack if it names none.
func getNoticeSettings(r *http.Request, db *sql.DB, n *notifier, id int64) (noticeSettings, error) {
	ctx := r.Context()
	project, _ := requestProjectIf(r)
	var s noticeSettings
	err := db.QueryRowContext(ctx, "SELECT notify_email, notify_slack, notify_failures FROM users WHERE id = ?", id).Scan(&s.Email, &s.Slack, &s.Failures)
	if err != nil {
		return s, err
	}
	s.CanEmail, s.CanSlack, err = n.channels(ctx, project)
	return s, err
}

func registerInboxRoutes(mux *http.ServeMux, db *sql.DB, n *notifier, a *auth) {
	// What waits on whoever asks in every project they may enter. The flows
	// it names are filtered as it lists them, so it needs no guard.
	mux.HandleFunc("GET /api/inbox", func(w http.ResponseWriter, r *http.Request) {
		projects, err := inboxProjects(r.Context(), db, r)
		if err != nil {
			serverError(w, err)
			return
		}
		items, err := inbox(r.Context(), db, requestUser(r), projects)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	})
	if a == nil {
		return
	}

	mux.HandleFunc("GET /api/me/notifications", func(w http.ResponseWriter, r *http.Request) {
		s, err := getNoticeSettings(r, db, n, requestUser(r).ID)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	})

	// Says what the person signed in is told of, and how; a field left out
	// stays as it was.
	mux.HandleFunc("PATCH /api/me/notifications", func(w http.ResponseWriter, r *http.Request) {
		var patch struct {
			Email    *bool `json:"email"`
			Slack    *bool `json:"slack"`
			Failures *bool `json:"failures"`
		}
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := requestUser(r).ID
		for _, f := range []struct {
			column string
			value  *bool
		}{{"notify_email", patch.Email}, {"notify_slack", patch.Slack}, {"notify_failures", patch.Failures}} {
			if f.value == nil {
				continue
			}
			if _, err := db.ExecContext(r.Context(), "UPDATE users SET "+f.column+" = ? WHERE id = ?", *f.value, id); err != nil {
				serverError(w, err)
				return
			}
		}
		s, err := getNoticeSettings(r, db, n, id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	})
}
