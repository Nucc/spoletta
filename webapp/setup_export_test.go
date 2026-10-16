package main

import (
	"reflect"
	"testing"
)

func exportID(id int64) *int64 { return &id }

func exportInt(n int) *int { return &n }

func exportFloat(f float64) *float64 { return &f }

// A flow whose processes and objects share names: two Fetch processes and
// two Ticket objects, the first with two open states. Fetch yields the
// first Ticket in its second open state, then runs Fetch again, or Review
// once it fails. Review runs the Review procedure and takes the second
// Ticket, deciding between its states; Group is a flow process with Inner
// on its board. The flow also has a copy of the Customer struct's object
// and of its address part.
func exportedTriage() (*exportedFlow, map[int64]*exportedFlow) {
	triage := &exportedFlow{id: 1, name: "Triage", s: snapshot{
		Processes: []process{
			{ID: 10, Name: "Fetch", Kind: kindScript, Script: "echo hi", X: 1, Y: 2},
			{ID: 11, Name: "Fetch", Kind: kindScript, X: 3, Y: 4},
			{ID: 12, Name: "Review", Kind: kindSubflow, Settings: processSettings{Flow: exportID(2), MCPServers: []int64{5, 99}}},
			{ID: 13, Name: "Group", Kind: kindFlow},
			{ID: 14, Name: "Inner", Kind: kindAgent, Prompt: "think", Parent: exportID(13)},
		},
		Objects: []snapshotObject{
			{ID: 20, Name: "Ticket", X: 5, Y: 6, Persistent: true},
			{ID: 21, Name: "Ticket", Many: true, Min: 1, Max: exportInt(3)},
			{ID: 22, Name: "note", Owner: exportID(20), Relation: relationExhibition},
			{ID: 23, Name: "tags", Owner: exportID(20), Relation: relationAggregation},
			{ID: 24, Name: "Customer", Source: exportID(30)},
			{ID: 25, Name: "address", Source: exportID(31)},
			{ID: 26, Name: "Scratch", Parent: exportID(13), General: exportID(20)},
		},
		States: []objectState{
			{ID: 40, Object: 20, Name: "open"},
			{ID: 41, Object: 20, Name: "open", Rule: "x > 1"},
			{ID: 42, Object: 21, Name: "open"},
			{ID: 43, Object: 21, Name: "closed"},
		},
		Attributes: []attribute{{ID: 50, Object: 20, Name: "id", Unique: true}},
		Links: []snapshotLink{
			{ID: 60, Process: 10, Object: 20, Direction: directionOutput, State: 41},
			{ID: 61, Process: 12, Object: 21, Direction: directionInput, Role: "instrument", At: &point{7, 8}, Route: []byte(`[1]`)},
		},
		LinkStates:  []snapshotLinkState{{Link: 61, State: 42}, {Link: 61, State: 43}},
		Connections: []connection{{Source: 10, Target: 11}, {Source: 10, Target: 12, Kind: connectionFailure}},
		Start:       startNode{X: 9, Y: 9, Target: exportID(10), Route: []byte("null")},
		End:         &boardEnd{100, 200},
		Boards:      []innerBoard{{Process: 13, Start: startNode{X: 1, Y: 1, Target: exportID(14)}, End: boardEnd{2, 2}}},
	}}
	review := &exportedFlow{id: 2, name: "Review", module: true}
	customer := &exportedFlow{id: 3, name: "Customer", isStruct: true, s: snapshot{Objects: []snapshotObject{
		{ID: 30, Name: "Customer"},
		{ID: 31, Name: "address", Owner: exportID(30)},
	}}}
	other := &exportedFlow{id: 4, name: "Other"}
	byID := map[int64]*exportedFlow{1: triage, 2: review, 3: customer, 4: other}
	for _, e := range byID {
		e.identify()
	}
	return triage, byID
}

func TestExportedFlowIdentify(t *testing.T) {
	e, _ := exportedTriage()
	wantProcs := map[int64]string{10: "Fetch", 11: "Fetch (2)", 12: "Review", 13: "Group", 14: "Inner"}
	if !reflect.DeepEqual(e.procIDs, wantProcs) {
		t.Errorf("procIDs = %v, want %v", e.procIDs, wantProcs)
	}
	wantObjs := map[int64]string{20: "Ticket", 21: "Ticket (2)", 22: "note", 23: "tags", 24: "Customer", 25: "address", 26: "Scratch"}
	if !reflect.DeepEqual(e.objIDs, wantObjs) {
		t.Errorf("objIDs = %v, want %v", e.objIDs, wantObjs)
	}
	// States are told apart by object: the second Ticket's open is open.
	wantStates := map[int64]string{40: "open", 41: "open (2)", 42: "open", 43: "closed"}
	if !reflect.DeepEqual(e.stateIDs, wantStates) {
		t.Errorf("stateIDs = %v, want %v", e.stateIDs, wantStates)
	}
	if !reflect.DeepEqual(e.linkStates, map[int64][]int64{61: {42, 43}}) {
		t.Errorf("linkStates = %v", e.linkStates)
	}
}

func TestSetupKeyOf(t *testing.T) {
	for _, c := range []struct{ name, id, want string }{
		{"Fetch", "Fetch", ""},
		{"Fetch", "Fetch (2)", "Fetch (2)"},
	} {
		if got := setupKeyOf(c.name, c.id); got != c.want {
			t.Errorf("setupKeyOf(%q, %q) = %q, want %q", c.name, c.id, got, c.want)
		}
	}
}

// A stored route that isn't JSON is left out of the file.
func TestExportRoute(t *testing.T) {
	for _, c := range []struct {
		raw  string
		want any
	}{
		{"", nil},
		{"null", nil},
		{"[1,2]", []any{1.0, 2.0}},
		{"not json", nil},
	} {
		if got := exportRoute([]byte(c.raw)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("exportRoute(%q) = %#v, want %#v", c.raw, got, c.want)
		}
	}
}

// Exporting one flow takes the procedures it runs and the flows its copies
// follow, on down, by name; flows it doesn't refer to stay out.
func TestReferredFrom(t *testing.T) {
	triage, byID := exportedTriage()
	objectFlow := map[int64]*exportedFlow{30: byID[3], 31: byID[3]}
	var got []string
	for _, f := range referredFrom(triage, byID, objectFlow) {
		got = append(got, f.name)
	}
	if want := []string{"Customer", "Review", "Triage"}; !reflect.DeepEqual(got, want) {
		t.Errorf("referredFrom = %v, want %v", got, want)
	}

	// A procedure that runs another, which copies from the first, is
	// visited once each.
	a := &exportedFlow{id: 1, name: "A", s: snapshot{Processes: []process{{ID: 1, Settings: processSettings{Flow: exportID(2)}}}}}
	b := &exportedFlow{id: 2, name: "A", s: snapshot{Objects: []snapshotObject{{ID: 2, Source: exportID(1)}}}}
	c := &exportedFlow{id: 3, name: "C"}
	byID = map[int64]*exportedFlow{1: a, 2: b, 3: c}
	flows := referredFrom(b, byID, map[int64]*exportedFlow{1: a, 2: b})
	if len(flows) != 2 || flows[0] != a || flows[1] != b {
		t.Errorf("referredFrom in a circle = %v", flows)
	}
}

func TestExporterBoard(t *testing.T) {
	triage, byID := exportedTriage()
	x := exporter{
		e:          triage,
		objectFlow: map[int64]*exportedFlow{30: byID[3], 31: byID[3]},
		flowNames:  map[int64]string{2: "Review"},
		mcpNames:   map[int64]string{5: "Linear"},
	}
	b := x.board(nil)

	if b.Start == nil || b.Start.Next != "Fetch" || *b.Start.X != 9 || b.Start.Route != nil {
		t.Errorf("start = %+v", b.Start)
	}
	if b.End == nil || *b.End != (setupPoint{100, 200}) {
		t.Errorf("end = %+v", b.End)
	}
	var procs []string
	for _, p := range b.Processes {
		procs = append(procs, ident(p.Name, p.Key))
	}
	if want := []string{"Fetch", "Fetch (2)", "Review", "Group"}; !reflect.DeepEqual(procs, want) {
		t.Fatalf("processes = %v, want %v", procs, want)
	}

	fetch := b.Processes[0]
	if fetch.Key != "" || fetch.Script != "echo hi" || *fetch.X != 1 || *fetch.Y != 2 {
		t.Errorf("Fetch = %+v", fetch)
	}
	if !reflect.DeepEqual(fetch.Yields, []setupLink{{Object: "Ticket", State: "open (2)"}}) {
		t.Errorf("Fetch yields %+v", fetch.Yields)
	}
	if !reflect.DeepEqual(fetch.Next, []string{"Fetch (2)"}) || !reflect.DeepEqual(fetch.OnFailure, []string{"Review"}) {
		t.Errorf("Fetch next %v, on failure %v", fetch.Next, fetch.OnFailure)
	}
	if b.Processes[1].Key != "Fetch (2)" {
		t.Errorf("second Fetch's key = %q", b.Processes[1].Key)
	}

	// MCP servers the app no longer has are left out.
	review := b.Processes[2]
	if review.Runs != "Review" || !reflect.DeepEqual(review.MCPServers, []string{"Linear"}) {
		t.Errorf("Review runs %q with %v", review.Runs, review.MCPServers)
	}
	wantTakes := []setupLink{{Object: "Ticket (2)", States: []string{"open", "closed"}, Role: "instrument", At: &setupPoint{7, 8}, Route: []any{1.0}}}
	if !reflect.DeepEqual(review.Takes, wantTakes) {
		t.Errorf("Review takes %+v, want %+v", review.Takes, wantTakes)
	}

	group := b.Processes[3]
	if group.Board == nil {
		t.Fatal("Group has no board")
	}
	if group.Board.Start == nil || group.Board.Start.Next != "Inner" || *group.Board.End != (setupPoint{2, 2}) {
		t.Errorf("Group's board starts %+v, ends %+v", group.Board.Start, group.Board.End)
	}
	if len(group.Board.Processes) != 1 || group.Board.Processes[0].Prompt != "think" {
		t.Errorf("Group's processes = %+v", group.Board.Processes)
	}
	if len(group.Board.Objects) != 1 || group.Board.Objects[0].Specializes != "Ticket" {
		t.Errorf("Group's objects = %+v", group.Board.Objects)
	}

	// Parts are written under their owner, not on the board.
	var objs []string
	for _, o := range b.Objects {
		objs = append(objs, ident(o.Name, o.Key))
	}
	if want := []string{"Ticket", "Ticket (2)", "Customer", "address"}; !reflect.DeepEqual(objs, want) {
		t.Fatalf("objects = %v, want %v", objs, want)
	}
}

func TestExporterObject(t *testing.T) {
	triage, byID := exportedTriage()
	x := exporter{e: triage, objectFlow: map[int64]*exportedFlow{30: byID[3], 31: byID[3]}}
	objects := map[int64]snapshotObject{}
	for _, o := range triage.s.Objects {
		objects[o.ID] = o
	}

	ticket := x.object(objects[20])
	want := setupObject{
		Name: "Ticket", Persistent: true, X: exportFloat(5), Y: exportFloat(6),
		States:     []setupState{{Name: "open"}, {Name: "open", Key: "open (2)", Rule: "x > 1"}},
		Attributes: []setupAttribute{{Name: "id", Unique: true}},
		Parts: []setupObject{
			{Name: "note", X: exportFloat(0), Y: exportFloat(0)},
			{Name: "tags", Relation: relationAggregation, X: exportFloat(0), Y: exportFloat(0)},
		},
	}
	if !reflect.DeepEqual(ticket, want) {
		t.Errorf("Ticket = %+v, want %+v", ticket, want)
	}

	second := x.object(objects[21])
	if second.Key != "Ticket (2)" || !second.Many || second.Min != 1 || *second.Max != 3 {
		t.Errorf("second Ticket = %+v", second)
	}

	// A copy of a struct's own object names the struct alone; a copy of
	// one of its parts names the part too. Copies have nothing of their own.
	for _, c := range []struct {
		id   int64
		want setupSource
	}{
		{24, setupSource{Flow: "Customer"}},
		{25, setupSource{Flow: "Customer", Object: "address"}},
	} {
		got := x.object(objects[c.id])
		if got.CopyOf == nil || *got.CopyOf != c.want || got.States != nil || got.Parts != nil {
			t.Errorf("copy %d = %+v, want a copy of %+v", c.id, got, c.want)
		}
	}

	// A copy of a procedure's object always names the object.
	review := &exportedFlow{id: 2, name: "Review", module: true, s: snapshot{Objects: []snapshotObject{{ID: 70, Name: "Report"}}}}
	review.identify()
	x.objectFlow[70] = review
	got := x.object(snapshotObject{ID: 24, Name: "Customer", Source: exportID(70)})
	if got.CopyOf == nil || *got.CopyOf != (setupSource{Flow: "Review", Object: "Report"}) {
		t.Errorf("copy of a procedure's object = %+v", got.CopyOf)
	}
}
