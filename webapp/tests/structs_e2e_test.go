package tests

import (
	"fmt"
	"net/http"
	"testing"
)

// A flow as the main page lists it, a struct or not.
type structFlow struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Module bool   `json:"module"`
	Struct bool   `json:"struct"`
	UsedBy int    `json:"usedBy"`
}

func structFlowOf(a *app, id int64) structFlow {
	a.t.Helper()
	var f structFlow
	a.call("GET", fmt.Sprintf("/api/flows/%d", id), nil, http.StatusOK, &f)
	return f
}

// A struct is made with its object, named after it, and holds nothing else
// but what makes that object up.
func TestStructs(t *testing.T) {
	a := newApp(t)
	var s structFlow
	a.call("POST", "/api/flows", map[string]any{"name": "Jira ticket", "struct": true}, http.StatusCreated, &s)
	if !s.Struct || s.Module || s.UsedBy != 0 {
		t.Errorf("a new struct = %+v", s)
	}
	objs := modObjects(a, s.ID)
	if len(objs) != 1 || objs[0].Name != "Jira ticket" {
		t.Fatalf("a new struct's objects = %+v, want its one object", objs)
	}
	ticket := objs[0]
	a.refused("POST", "/api/flows", map[string]any{"name": "Both", "struct": true, "module": true}, http.StatusBadRequest, "can't be a procedure")

	// What makes the object up is the struct's own.
	a.newState(ticket.ID, "open")
	verAttribute(a, ticket.ID, "id")
	var meta object
	a.call("POST", "/api/objects", map[string]any{"name": "meta", "owner": ticket.ID}, http.StatusCreated, &meta)

	// Nothing else goes in.
	a.refused("POST", "/api/objects", map[string]any{"name": "Other", "flow": s.ID}, http.StatusBadRequest, "a struct holds a single object")
	a.refused("POST", "/api/processes", map[string]any{"name": "Work", "flow": s.ID}, http.StatusBadRequest, "a struct holds a single object")
	a.refused("POST", fmt.Sprintf("/api/runs?flow=%d", s.ID), nil, http.StatusBadRequest, "a struct holds a single object")
	a.refused("DELETE", fmt.Sprintf("/api/objects/%d", ticket.ID), nil, http.StatusBadRequest, "delete the struct instead")
	a.refused("PATCH", fmt.Sprintf("/api/objects/%d", ticket.ID), map[string]any{"human": true}, http.StatusBadRequest, "up to each copy")
	a.refused("PATCH", fmt.Sprintf("/api/flows/%d", s.ID), map[string]any{"module": true}, http.StatusBadRequest, "can't be a procedure")
	other := a.newFlow("Other")
	note := a.newObject(other.ID, "Note")
	a.refused("POST", "/api/paste", map[string]any{"from": other.ID, "objects": []int64{note.ID}, "flow": s.ID}, http.StatusBadRequest, "a struct holds a single object")

	// A flow runs no struct; it uses its object.
	job := a.newFlow("Nightly")
	a.refused("POST", "/api/processes", map[string]any{"name": "Ticket", "flow": job.ID, "kind": kindSubflow, "settings": map[string]any{"flow": s.ID}}, http.StatusBadRequest, "a struct is an object")
	if f := structFlowOf(a, s.ID); f.Module {
		t.Errorf("a struct a subflow process tried to run = %+v, want no procedure", f)
	}

	var cp modObject
	a.call("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": job.ID, "x": 300, "y": 200}, http.StatusCreated, &cp)
	if cp.Source == nil || *cp.Source != ticket.ID || cp.Name != "Jira ticket" {
		t.Fatalf("the copy = %+v, want it from %d", cp, ticket.ID)
	}
	cp = modNamed(t, modObjects(a, job.ID), "Jira ticket")
	if modStateNames(cp) != "open" || modAttrNames(cp) != "id=The id" || len(modPartsOf(modObjects(a, job.ID), cp.ID)) != 1 {
		t.Errorf("the copy = %+v, want the struct's states, attributes and parts", cp)
	}
	if f := structFlowOf(a, s.ID); f.UsedBy != 1 {
		t.Errorf("the struct is used by %d flows, want 1", f.UsedBy)
	}
	a.refused("POST", fmt.Sprintf("/api/structs/%d/copies", s.ID), map[string]any{"flow": s.ID}, http.StatusBadRequest, "a struct holds a single object")
	a.refused("POST", fmt.Sprintf("/api/structs/%d/copies", job.ID), map[string]any{"flow": other.ID}, http.StatusNotFound, "struct not found")

	// The copy changes only in the struct, and with it.
	p := fmt.Sprintf("/api/objects/%d", cp.ID)
	a.refused("POST", p+"/states", map[string]any{"name": "mine"}, http.StatusBadRequest, `comes from the struct "Jira ticket"`)
	verPatch(a, p, map[string]any{"persistent": true})
	a.newState(ticket.ID, "closed")
	if got := modStateNames(modNamed(t, modObjects(a, job.ID), "Jira ticket")); got != "open,closed" {
		t.Errorf("the copy's states = %s", got)
	}

	// Renaming the struct renames its object and the copies, and back.
	verPatch(a, fmt.Sprintf("/api/flows/%d", s.ID), map[string]any{"name": "Ticket"})
	if objs := modObjects(a, s.ID); modNamed(t, objs, "Ticket").ID != ticket.ID {
		t.Errorf("the struct's object after renaming the struct = %+v", objs)
	}
	modNamed(t, modObjects(a, job.ID), "Ticket")
	verPatch(a, fmt.Sprintf("/api/objects/%d", ticket.ID), map[string]any{"name": "Issue"})
	if f := structFlowOf(a, s.ID); f.Name != "Issue" {
		t.Errorf("the struct after renaming its object = %+v", f)
	}
	modNamed(t, modObjects(a, job.ID), "Issue")

	// Deleting the struct leaves the copy an object of its own.
	a.call("DELETE", fmt.Sprintf("/api/flows/%d", s.ID), nil, http.StatusNoContent, nil)
	cp = modNamed(t, modObjects(a, job.ID), "Issue")
	if cp.Source != nil || !cp.Persistent {
		t.Errorf("the copy of a deleted struct = %+v", cp)
	}
	a.call("POST", fmt.Sprintf("/api/objects/%d/states", cp.ID), map[string]any{"name": "mine"}, http.StatusCreated, nil)
}
