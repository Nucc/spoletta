package main

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// OPL, OPM's Object-Process Language, says in sentences what a board's
// diagram shows: each object with its states, attributes and parts, then
// each process with what it takes and yields and how, and where the board
// starts and ends. A flow process's board follows, indented under it, as
// OPM zooms into a process. People read it beside the board, and an agent
// can be given it in its prompt, to know the flow it works in; see
// processSettings.DescribeFlow.

// The OPL of a flow's board as a snapshot has it. flowName names the flows
// subflow processes run.
func opl(s snapshot, flowName func(int64) string) string {
	o := oplWriter{s: s, flowName: flowName,
		objects: map[int64]snapshotObject{}, processes: map[int64]process{},
		states: map[int64]objectState{}, linkStates: map[int64][]int64{}}
	for _, ob := range s.Objects {
		o.objects[ob.ID] = ob
	}
	for _, p := range s.Processes {
		o.processes[p.ID] = p
	}
	for _, st := range s.States {
		o.states[st.ID] = st
	}
	for _, ls := range s.LinkStates {
		o.linkStates[ls.Link] = append(o.linkStates[ls.Link], ls.State)
	}
	o.board(nil, "")
	return strings.TrimRight(o.b.String(), "\n")
}

type oplWriter struct {
	s          snapshot
	flowName   func(int64) string
	objects    map[int64]snapshotObject
	processes  map[int64]process
	states     map[int64]objectState
	linkStates map[int64][]int64
	b          strings.Builder
}

func (o *oplWriter) line(indent, format string, args ...any) {
	o.b.WriteString(indent)
	fmt.Fprintf(&o.b, format, args...)
	o.b.WriteString("\n")
}

func sameBoard(a, b *int64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// Names, as OPL bolds them: in quotes, so a name reads as one.
func (o *oplWriter) object(id int64) string  { return strconv.Quote(o.objects[id].Name) }
func (o *oplWriter) process(id int64) string { return strconv.Quote(o.processes[id].Name) }
func (o *oplWriter) state(id int64) string   { return strconv.Quote(o.states[id].Name) }

// a, b and c; or with or.
func list(items []string, conj string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + conj + " " + items[len(items)-1]
}

func (o *oplWriter) stateList(ids []int64, conj string) string {
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = o.state(id)
	}
	return list(names, conj)
}

// The board of a flow process, nil for the top one, its sentences indented.
func (o *oplWriter) board(board *int64, indent string) {
	var objects []snapshotObject
	for _, ob := range o.s.Objects {
		if sameBoard(ob.Parent, board) && ob.Owner == nil {
			objects = append(objects, ob)
		}
	}
	var processes []process
	for _, p := range o.s.Processes {
		if sameBoard(p.Parent, board) {
			processes = append(processes, p)
		}
	}
	// Top to bottom, as the board reads.
	slices.SortStableFunc(processes, func(a, b process) int { return cmp.Or(cmp.Compare(a.Y, b.Y), cmp.Compare(a.X, b.X)) })

	for _, ob := range objects {
		o.objectSentences(ob, indent)
	}
	for _, p := range processes {
		o.processSentences(p, indent)
	}

	start := o.s.Start
	if board != nil {
		for _, b := range o.s.Boards {
			if b.Process == *board {
				start = b.Start
			}
		}
	}
	where := "The flow"
	if board != nil {
		where = o.process(*board)
	}
	if start.Target != nil {
		o.line(indent, "%s starts with %s.", where, o.process(*start.Target))
	}
	if board == nil {
		var ends []string
		for _, ob := range objects {
			if ob.End {
				ends = append(ends, o.object(ob.ID))
			}
		}
		if len(ends) > 0 {
			o.line(indent, "The flow ends with %s.", list(ends, "and"))
		}
	}

	for _, p := range processes {
		if p.Kind == kindFlow {
			o.line(indent, "%s zooms into:", o.process(p.ID))
			o.board(&p.ID, indent+"  ")
		}
	}
}

func (o *oplWriter) objectSentences(ob snapshotObject, indent string) {
	name := o.object(ob.ID)
	switch {
	case ob.Human:
		o.line(indent, "%s is a person.", name)
	case ob.Many:
		if w := (quantity{min: ob.Min, max: ob.Max}).words(); w != "" {
			o.line(indent, "%s is a list of items, %s.", name, w)
		} else {
			o.line(indent, "%s is a list of items.", name)
		}
	}
	if ob.General != nil {
		o.line(indent, "%s is a %s.", name, o.object(*ob.General))
	}
	if ob.Persistent {
		o.line(indent, "%s is stored, so each run keeps what it generated of it.", name)
	}
	if ob.Store != nil {
		o.line(indent, "%s is shared, so every flow using it reads and writes the same records.", name)
	}
	var states []int64
	var rules []string
	for _, st := range o.s.States {
		if st.Object == ob.ID {
			states = append(states, st.ID)
			if st.Rule != "" {
				rules = append(rules, fmt.Sprintf("%s is %s when %s.", name, o.state(st.ID), st.Rule))
			}
		}
	}
	if len(states) > 0 {
		o.line(indent, "%s can be %s.", name, o.stateList(states, "or"))
	}
	for _, r := range rules {
		o.line(indent, "%s", r)
	}
	o.structureSentences(ob.ID, indent)
}

// What an object exhibits and consists of, and so on for what it is made of.
func (o *oplWriter) structureSentences(id int64, indent string) {
	name := o.object(id)
	var exhibits, parts []string
	for _, a := range o.s.Attributes {
		if a.Object == id {
			exhibits = append(exhibits, strconv.Quote(a.Name))
		}
	}
	var owned []int64
	for _, ob := range o.s.Objects {
		if ob.Owner == nil || *ob.Owner != id {
			continue
		}
		owned = append(owned, ob.ID)
		n := o.object(ob.ID)
		if ob.Many {
			n = "a list of " + n
		}
		if ob.Relation == relationAggregation {
			parts = append(parts, n)
		} else {
			exhibits = append(exhibits, n)
		}
	}
	if len(exhibits) > 0 {
		o.line(indent, "%s exhibits %s.", name, list(exhibits, "and"))
	}
	if len(parts) > 0 {
		o.line(indent, "%s consists of %s.", name, list(parts, "and"))
	}
	for _, ob := range owned {
		o.structureSentences(ob, indent)
	}
}

var kindPhrases = map[string]string{
	kindAgent:     "an agent process, which an AI agent carries out",
	kindScript:    "a script process",
	kindHuman:     "a human process, which a person carries out",
	kindFlow:      "a flow process, made of processes of its own",
	kindSentry:    "a Sentry process, which fetches issues",
	kindConnector: "a connector process, which carries out an operation on a service such as GitHub",
}

func (o *oplWriter) processSentences(p process, indent string) {
	name := o.process(p.ID)
	if p.Kind == kindSubflow {
		flow := "another flow"
		if p.Settings.Flow != nil && o.flowName != nil {
			if n := o.flowName(*p.Settings.Flow); n != "" {
				flow = "the flow " + strconv.Quote(n)
			}
		}
		o.line(indent, "%s is a procedure, which runs %s.", name, flow)
	} else {
		o.line(indent, "%s is %s.", name, kindPhrases[p.Kind])
	}

	var ins, outs []snapshotLink
	for _, l := range o.s.Links {
		if l.Process != p.ID {
			continue
		}
		if l.Direction == directionInput {
			ins = append(ins, l)
		} else {
			outs = append(outs, l)
		}
	}
	generates := map[int64]bool{}
	for _, l := range outs {
		generates[l.Object] = true
	}
	takes := map[int64]bool{}
	conditions := map[int64][]int64{}
	var conditionOrder []int64
	for _, l := range ins {
		ob := o.object(l.Object)
		if l.Query != nil {
			o.line(indent, "%s looks up %s.", name, queryPhrase(ob, l.Query))
			continue
		}
		switch {
		case o.objects[l.Object].Human:
			o.line(indent, "%s handles %s.", ob, name)
		case l.Condition && l.State != 0:
			if _, ok := conditions[l.Object]; !ok {
				conditionOrder = append(conditionOrder, l.Object)
			}
			conditions[l.Object] = append(conditions[l.Object], l.State)
		case l.State != 0:
			o.line(indent, "%s entering %s triggers %s.", ob, o.state(l.State), name)
		case l.Role == roleInstrument:
			o.line(indent, "%s requires %s, which it doesn't change.", name, ob)
		case l.Role == roleConsumption:
			o.line(indent, "%s consumes %s.", name, ob)
		case generates[l.Object]:
			// Said as it affects it, with what it yields.
		default:
			o.line(indent, "%s takes %s.", name, ob)
		}
		if l.State == 0 || l.Condition {
			takes[l.Object] = true
		}
	}
	// With a quorum each condition only counts towards it.
	for _, ob := range conditionOrder {
		if p.Settings.Quorum != nil {
			o.line(indent, "%s takes %s if it is %s.", name, o.object(ob), o.stateList(conditions[ob], "or"))
		} else {
			o.line(indent, "%s occurs if %s is %s, and is skipped otherwise.", name, o.object(ob), o.stateList(conditions[ob], "or"))
		}
	}

	for _, l := range outs {
		ob := o.object(l.Object)
		decides := o.linkStates[l.ID]
		takesIt := slices.ContainsFunc(ins, func(i snapshotLink) bool { return i.Object == l.Object })
		switch {
		case len(decides) > 0 && takesIt:
			o.line(indent, "%s decides whether %s is %s.", name, ob, o.stateList(decides, "or"))
		case len(decides) > 0:
			o.line(indent, "%s yields %s as %s, deciding which.", name, ob, o.stateList(decides, "or"))
		case p.Kind == kindHuman && !takesIt:
			o.line(indent, "%s has a person fill in %s.", name, ob)
		case p.Kind == kindHuman:
			// A review without states to decide between ends there.
		case takes[l.Object] && l.State != 0:
			o.line(indent, "%s affects %s, changing it to %s.", name, ob, o.state(l.State))
		case takes[l.Object]:
			o.line(indent, "%s affects %s.", name, ob)
		case l.State != 0:
			o.line(indent, "%s yields %s as %s.", name, ob, o.state(l.State))
		default:
			o.line(indent, "%s yields %s.", name, ob)
		}
	}

	st := p.Settings
	if st.Quorum != nil {
		o.line(indent, "%s needs %d of what it takes, and is skipped once it can't get them.", name, *st.Quorum)
	}
	// Rounds bound only a review, which can send work back.
	reviews := slices.ContainsFunc(outs, func(l snapshotLink) bool {
		return len(o.linkStates[l.ID]) > 0 && slices.ContainsFunc(ins, func(i snapshotLink) bool { return i.Object == l.Object })
	})
	if p.Kind == kindAgent && reviews {
		o.line(indent, "%s runs at most %d rounds.", name, st.rounds())
	}
	if st.Loops != nil {
		o.line(indent, "%s asks before going round a loop more than %d times.", name, *st.Loops)
	}
	if st.Timeout != nil {
		o.line(indent, "%s fails after %d seconds.", name, *st.Timeout)
	}
	if st.Retries != nil && *st.Retries > 0 {
		times := "times"
		if *st.Retries == 1 {
			times = "time"
		}
		o.line(indent, "%s is tried again up to %d %s when it fails.", name, *st.Retries, times)
	}
	for _, c := range o.s.Connections {
		if c.Source != p.ID {
			continue
		}
		if c.Kind == connectionFailure {
			o.line(indent, "%s handles a failure of %s.", o.process(c.Target), name)
		} else {
			o.line(indent, "%s follows %s.", o.process(c.Target), name)
		}
	}
}

// The OPL of a flow as it is now.
func flowOPL(ctx context.Context, db *sql.DB, flow int64) (string, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	s, err := takeSnapshot(ctx, tx, flow)
	if err != nil {
		return "", err
	}
	return opl(s, func(id int64) string { return flowNameOf(ctx, db, id) }), nil
}

// The name of a flow, or "" if there is none by that id.
func flowNameOf(ctx context.Context, db *sql.DB, id int64) string {
	var name string
	if err := db.QueryRowContext(ctx, "SELECT name FROM flows WHERE id = ?", id).Scan(&name); err != nil {
		return ""
	}
	return name
}

func registerOPLRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/flows/{id}/opl", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return
		}
		if name := flowNameOf(r.Context(), db, id); name == "" {
			http.Error(w, "flow not found", http.StatusNotFound)
			return
		}
		text, err := flowOPL(r.Context(), db, id)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"text": text})
	})
}

// What a query link looks up, such as the latest 5 stored records of
// Review in approved where score >= 4.
func queryPhrase(object string, q *linkQuery) string {
	which := "latest"
	if q.Oldest {
		which = "earliest"
	}
	s := fmt.Sprintf("the %s %d stored records of %s", which, q.limit(), object)
	if q.State != "" {
		s += " in " + q.State
	}
	if q.Where != "" {
		s += " where " + q.Where
	}
	return s
}
