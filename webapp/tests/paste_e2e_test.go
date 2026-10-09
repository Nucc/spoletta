package tests

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
)

type pasted struct {
	Processes []int64 `json:"processes"`
	Objects   []int64 `json:"objects"`
}

func pasteInto(a *app, from int64, processes, objects []int64, to int64, board *int64, dx, dy float64) pasted {
	a.t.Helper()
	var out pasted
	a.call("POST", "/api/paste", map[string]any{
		"from": from, "processes": processes, "objects": objects,
		"flow": to, "board": board, "dx": dx, "dy": dy,
	}, http.StatusCreated, &out)
	return out
}

// Pasting into another flow copies what was selected with all it takes
// along, and leaves what was copied as it was.
func TestPasteIntoAnotherFlow(t *testing.T) {
	a := newApp(t)
	src := a.newFlow("Copied from")
	outer := editFlowProcess(a, src.ID, "Outer")
	var inner process
	a.call("POST", "/api/processes", map[string]any{"flow": src.ID, "name": "Inner", "kind": kindAgent, "prompt": "Write it", "parent": outer.ID, "x": 7, "y": 9}, http.StatusCreated, &inner)
	a.call("PATCH", fmt.Sprintf("/api/start?board=%d", outer.ID), map[string]any{"target": inner.ID}, http.StatusOK, nil)
	var draft object
	a.call("POST", "/api/objects", map[string]any{"flow": src.ID, "name": "Draft", "parent": outer.ID}, http.StatusCreated, &draft)
	ticket := a.newObject(src.ID, "Ticket")
	editPatchObject(a, ticket.ID, map[string]any{"x": 300, "y": 400})
	open := a.newState(ticket.ID, "open")
	closed := a.newState(ticket.ID, "closed")
	editAttribute(a, ticket.ID, "title")
	address := editOwned(a, ticket.ID, "address", "")
	editAttribute(a, address.ID, "street")
	review := a.newProcess(src.ID, map[string]any{"name": "Review", "kind": kindHuman, "x": 100, "y": 200})
	a.link(map[string]any{"process": review.ID, "object": ticket.ID, "direction": directionOutput, "states": []int64{closed.ID}})
	a.link(map[string]any{"process": outer.ID, "object": ticket.ID, "direction": directionInput, "state": open.ID})
	a.input(inner.ID, ticket.ID)
	a.output(inner.ID, draft.ID)
	vehicle := a.newObject(src.ID, "Vehicle")
	editAttribute(a, vehicle.ID, "wheels")
	car := a.newObject(src.ID, "Car")
	editPatchObject(a, car.ID, map[string]any{"general": vehicle.ID})
	unselected := a.newObject(src.ID, "Left behind")
	a.input(review.ID, unselected.ID)

	beforeObjs, beforeLinks := editObjects(a, src.ID), editLinks(a, src.ID)

	dst := a.newFlow("Pasted into")
	res := pasteInto(a, src.ID, []int64{outer.ID, review.ID, inner.ID}, []int64{ticket.ID, car.ID, address.ID}, dst.ID, nil, 100, 50)
	if len(res.Processes) != 2 || len(res.Objects) != 2 {
		t.Fatalf("pasted = %+v, want Outer and Review, Ticket and Car; the rest come along", res)
	}

	ps := editProcesses(a, dst.ID)
	objs := editObjects(a, dst.ID)
	if len(ps) != 3 {
		t.Fatalf("processes pasted = %+v", ps)
	}
	pOuter, pInner, pReview := ps["Outer"], ps["Inner"], ps["Review"]
	if pOuter.ID == outer.ID || pOuter.Parent != nil || pOuter.Kind != kindFlow || pOuter.X != outer.X+100 || pOuter.Y != outer.Y+50 {
		t.Errorf("Outer pasted = %+v, from %+v", pOuter, outer)
	}
	if pInner.Parent == nil || *pInner.Parent != pOuter.ID || pInner.X != 7 || pInner.Y != 9 || pInner.Prompt != "Write it" {
		t.Errorf("Inner pasted = %+v, want on Outer's copy where it was", pInner)
	}
	if pReview.X != 200 || pReview.Y != 250 || pReview.Kind != kindHuman {
		t.Errorf("Review pasted = %+v", pReview)
	}
	if !slices.Contains(res.Processes, pOuter.ID) || !slices.Contains(res.Processes, pReview.ID) {
		t.Errorf("pasted processes %v aren't Outer's and Review's copies", res.Processes)
	}

	pTicket, pDraft, pCar, pAddress := objs["Ticket"], objs["Draft"], objs["Car"], objs["address"]
	if pTicket.ID == ticket.ID || pTicket.X != 400 || pTicket.Y != 450 || pTicket.Parent != nil || pTicket.Source != nil {
		t.Errorf("Ticket pasted = %+v", pTicket)
	}
	if len(pTicket.States) != 2 || pTicket.States[0].Name != "open" || pTicket.States[1].Name != "closed" ||
		len(pTicket.Attributes) != 1 || pTicket.Attributes[0].Name != "title" {
		t.Errorf("Ticket's states and attributes = %+v %+v", pTicket.States, pTicket.Attributes)
	}
	if pAddress.Owner == nil || *pAddress.Owner != pTicket.ID || len(pAddress.Attributes) != 1 || pAddress.Attributes[0].Name != "street" {
		t.Errorf("address pasted = %+v, want making up Ticket's copy once", pAddress)
	}
	if pDraft.Parent == nil || *pDraft.Parent != pOuter.ID {
		t.Errorf("Draft pasted = %+v, want on Outer's copy", pDraft)
	}
	// The flow pasted into has no Vehicle: Car takes over what it inherited.
	if pCar.General != nil || len(pCar.Attributes) != 1 || pCar.Attributes[0].Name != "wheels" {
		t.Errorf("Car pasted = %+v, want wheels of its own", pCar)
	}
	if _, ok := objs["Vehicle"]; ok {
		t.Error("Vehicle came along uncopied")
	}
	if _, ok := objs["Left behind"]; ok {
		t.Error("an object not selected came along")
	}

	ls := editLinks(a, dst.ID)
	if len(ls) != 4 {
		t.Errorf("links pasted = %+v, want 4", ls)
	}
	pOpen, pClosed := pTicket.States[0].ID, pTicket.States[1].ID
	for _, l := range ls {
		switch {
		case l.Process == pReview.ID:
			if l.Object != pTicket.ID || l.Direction != directionOutput || !slices.Equal(l.States, []int64{pClosed}) {
				t.Errorf("Review's link = %+v, want deciding on Ticket's copy's closed", l)
			}
		case l.Process == pOuter.ID:
			if l.Object != pTicket.ID || l.State == nil || *l.State != pOpen {
				t.Errorf("Outer's link = %+v, want taking Ticket's copy open", l)
			}
		case l.Process == pInner.ID:
			if (l.Direction == directionInput && l.Object != pTicket.ID) || (l.Direction == directionOutput && l.Object != pDraft.ID) {
				t.Errorf("Inner's link = %+v", l)
			}
		default:
			t.Errorf("link = %+v, of no process pasted", l)
		}
	}
	bs := editBoards(a, dst.ID)
	if len(bs) != 1 || bs[0].Process != pOuter.ID || bs[0].Start.Target == nil || *bs[0].Start.Target != pInner.ID {
		t.Errorf("boards pasted = %+v, want Outer's copy's starting at Inner's", bs)
	}

	// The copies are the flow's own, and what was copied is as it was.
	editPatchObject(a, pTicket.ID, map[string]any{"name": "Issue"})
	if objs, ls := editObjects(a, src.ID), editLinks(a, src.ID); len(objs) != len(beforeObjs) || len(ls) != len(beforeLinks) || objs["Ticket"].ID != ticket.ID {
		t.Errorf("the flow copied from changed: %+v %+v", objs, ls)
	}
}

// Pasted into its own flow, an object keeps what it specializes; a subflow
// process can't be pasted into the flow it runs.
func TestPasteIntoTheSameFlow(t *testing.T) {
	a := newApp(t)
	f := a.newFlow("Same")
	vehicle := a.newObject(f.ID, "Vehicle")
	editAttribute(a, vehicle.ID, "wheels")
	car := a.newObject(f.ID, "Car")
	editPatchObject(a, car.ID, map[string]any{"general": vehicle.ID})
	res := pasteInto(a, f.ID, nil, []int64{car.ID}, f.ID, nil, 40, 40)
	if len(res.Objects) != 1 {
		t.Fatalf("pasted = %+v", res)
	}
	var cars []object
	for _, o := range allObjects(a, f.ID) {
		if o.Name == "Car" {
			cars = append(cars, o)
		}
	}
	if len(cars) != 2 || cars[1].General == nil || *cars[1].General != vehicle.ID || len(cars[1].Attributes) != 0 {
		t.Errorf("cars = %+v, want the copy specializing Vehicle", cars)
	}

	module := a.newFlow("Module")
	runner := a.newProcess(f.ID, map[string]any{"name": "Run it", "kind": kindSubflow, "settings": map[string]any{"flow": module.ID}})
	a.refused("POST", "/api/paste", map[string]any{"from": f.ID, "processes": []int64{runner.ID}, "flow": module.ID}, http.StatusBadRequest, "a flow can't run itself")

	a.call("DELETE", fmt.Sprintf("/api/objects/%d", car.ID), nil, http.StatusNoContent, nil)
	a.refused("POST", "/api/paste", map[string]any{"from": f.ID, "objects": []int64{car.ID}, "flow": f.ID}, http.StatusBadRequest, "nothing to paste")
}

// Every object of a flow, in the order they were added, names repeating.
func allObjects(a *app, f int64) []object {
	a.t.Helper()
	var os []object
	a.call("GET", fmt.Sprintf("/api/objects?flow=%d", f), nil, http.StatusOK, &os)
	return os
}

// A subflow process pasted with its outcome generates the copy of it, not
// the namesake already on the board.
func TestPasteASubflowProcessWithItsOutcome(t *testing.T) {
	a := newApp(t)
	module := a.newFlow("Makes results")
	result := a.newObject(module.ID, "Result")
	editPatchObject(a, result.ID, map[string]any{"end": true})
	f := a.newFlow("Runs it")
	runner := a.newProcess(f.ID, map[string]any{"name": "Run it", "kind": kindSubflow, "settings": map[string]any{"flow": module.ID}})
	brought := editObjects(a, f.ID)["Result"]
	if brought.ID == 0 {
		t.Fatal("the subflow process brought no Result")
	}

	generators := func(ls []link, object int64) []int64 {
		var ps []int64
		for _, l := range ls {
			if l.Object == object && l.Direction == directionOutput {
				ps = append(ps, l.Process)
			}
		}
		return ps
	}
	res := pasteInto(a, f.ID, []int64{runner.ID}, []int64{brought.ID}, f.ID, nil, 300, 0)
	if len(res.Processes) != 1 || len(res.Objects) != 1 {
		t.Fatalf("pasted = %+v", res)
	}
	ls := editLinks(a, f.ID)
	if g := generators(ls, brought.ID); !slices.Equal(g, []int64{runner.ID}) {
		t.Errorf("the first Result is generated by %v, want only Run it", g)
	}
	if g := generators(ls, res.Objects[0]); !slices.Equal(g, []int64{res.Processes[0]}) {
		t.Errorf("the pasted Result is generated by %v, want only Run it's copy", g)
	}

	// Pasted alone, it brings its outcome in again, as a new one does.
	other := a.newFlow("Runs it too")
	alone := pasteInto(a, f.ID, []int64{runner.ID}, nil, other.ID, nil, 0, 0)
	objs := allObjects(a, other.ID)
	if len(objs) != 1 || objs[0].Name != "Result" || objs[0].Source == nil || *objs[0].Source != result.ID {
		t.Fatalf("objects pasted alongside = %+v, want a copy of the module's Result", objs)
	}
	if g := generators(editLinks(a, other.ID), objs[0].ID); !slices.Equal(g, alone.Processes) {
		t.Errorf("Result is generated by %v, want %v", g, alone.Processes)
	}
}
