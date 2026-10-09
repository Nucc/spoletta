package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func count(n int) *int { return &n }

func TestQuantityText(t *testing.T) {
	for _, c := range []struct {
		many bool
		q    quantity
		text string
	}{
		{false, quantity{}, "1"},
		{true, quantity{}, "*"},
		{true, quantity{min: 1}, "1..*"},
		{true, quantity{min: 4, max: count(4)}, "4"},
		{true, quantity{min: 2, max: count(5)}, "2..5"},
		{true, quantity{max: count(3)}, "0..3"},
	} {
		if got := c.q.text(c.many); got != c.text {
			t.Errorf("text(%v, %+v) = %q, want %q", c.many, c.q, got, c.text)
		}
	}
}

func TestCheckQuantity(t *testing.T) {
	for _, c := range []struct {
		many bool
		q    quantity
		ok   bool
	}{
		{false, quantity{}, true},
		{true, quantity{}, true},
		{true, quantity{min: 4, max: count(4)}, true},
		{true, quantity{min: 2, max: count(5)}, true},
		// One object has no bounds of a list.
		{false, quantity{min: 1}, false},
		{false, quantity{max: count(3)}, false},
		{true, quantity{min: -1}, false},
		// A list of at most one item would read as one object.
		{true, quantity{max: count(1)}, false},
		{true, quantity{min: 1, max: count(1)}, false},
		{true, quantity{min: 5, max: count(3)}, false},
	} {
		if msg := checkQuantity(c.many, c.q); (msg == "") != c.ok {
			t.Errorf("checkQuantity(%v, %+v) = %q", c.many, c.q, msg)
		}
	}
}

// A car with exactly 4 wheels, each with between 1 and 3 bolts listed.
func cars() structure {
	return structure{
		1: {id: 1, name: "Car", attrs: []attribute{{ID: 10, Object: 1, Name: "model"}}},
		2: {id: 2, name: "wheels", owner: ptr(1), relation: relationAggregation, many: true, quantity: quantity{min: 4, max: count(4)},
			attrs: []attribute{{ID: 11, Object: 2, Name: "size"}}},
		3: {id: 3, name: "bolts", owner: ptr(2), relation: relationAggregation, many: true, quantity: quantity{min: 1, max: count(3)}},
	}
}

func wheels(n int, bolts string) string {
	ws := make([]string, n)
	for i := range ws {
		ws[i] = `{"size": 17, "bolts": ` + bolts + `}`
	}
	return "[" + strings.Join(ws, ",") + "]"
}

func TestParseCounts(t *testing.T) {
	c := contractOf(t, cars(), 1, false)
	if _, _, err := c.parse(`{"model": "T", "wheels": ` + wheels(4, `[1, 2]`) + `}`); err != nil {
		t.Fatal(err)
	}
	for reply, problem := range map[string]string{
		`{"model": "T", "wheels": ` + wheels(3, `[1]`) + `}`:          `"wheels": it has 3 items; it must have exactly 4`,
		`{"model": "T", "wheels": ` + wheels(5, `[1]`) + `}`:          `"wheels": it has 5 items; it must have exactly 4`,
		`{"model": "T", "wheels": ` + wheels(4, `[]`) + `}`:           `"wheels": item 1: "bolts": it has 0 items; it must have between 1 and 3`,
		`{"model": "T", "wheels": ` + wheels(4, `[1, 2, 3, 4]`) + `}`: `"wheels": item 1: "bolts": it has 4 items; it must have between 1 and 3`,
	} {
		if _, _, err := c.parse(reply); err == nil || err.Error() != problem {
			t.Errorf("parse(%s) = %v, want %q", reply, err, problem)
		}
	}
}

func TestParseCountsOfItems(t *testing.T) {
	// A list of at least 2 cars, as an agent hands it in under items.
	c := contractOf(t, cars(), 1, true)
	c.shaped[0].quantity = quantity{min: 2}
	car := `{"model": "T", "wheels": ` + wheels(4, `[1]`) + `}`
	if _, _, err := c.parse("[" + car + "," + car + "]"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.parse("[" + car + "]"); err == nil || err.Error() != "it has 1 item; it must have at least 2" {
		t.Errorf("err = %v", err)
	}
	if spec := c.spec(); spec.Shape != "items" || spec.Objects[0].MinItems != 2 || spec.Objects[0].MaxItems != nil {
		t.Errorf("spec = %+v", spec.Objects[0])
	}
	if text := c.withContract("Do it."); strings.Contains(text, "an empty array") || !strings.Contains(text, `"Car", a list of items (at least 2), each with:`) {
		t.Errorf("contract:\n%s", text)
	}

	// Inside a flow process the list is one item, with no count to check.
	c = contractOf(t, cars(), 1, false)
	if _, _, err := c.parse(car); err != nil {
		t.Errorf("one item: %v", err)
	}
}

func TestContractCounts(t *testing.T) {
	c := contractOf(t, cars(), 1, false)
	text := c.withContract("Do it.")
	for _, want := range []string{
		`- "wheels": a list of JSON objects (exactly 4), one per item, each with:`,
		`    - "bolts": a JSON array of any values (between 1 and 3)`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("contract lacks %q:\n%s", want, text)
		}
	}
	w := c.spec().Objects[0].Attributes[1]
	if w.MinItems != 4 || w.MaxItems == nil || *w.MaxItems != 4 {
		t.Errorf("spec wheels = %+v", w)
	}
	b, _ := json.Marshal(c.spec().Objects[0].Attributes[0])
	if strings.Contains(string(b), "Items") {
		t.Errorf("an attribute carries counts: %s", b)
	}
}

func TestCheckEntriesCounts(t *testing.T) {
	fs, err := cars().fields(1)
	if err != nil {
		t.Fatal(err)
	}
	objects := []shapedObject{{id: 1, name: "Car", fields: fs}}
	_, _, err = checkEntries(objects, map[string]json.RawMessage{
		"1": json.RawMessage(`{"model": "T", "wheels": ` + wheels(2, `[1]`) + `}`),
	})
	if err == nil || err.Error() != `"Car": "wheels": it has 2 items; it must have exactly 4` {
		t.Errorf("err = %v", err)
	}
}
