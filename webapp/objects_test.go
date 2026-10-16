package main

import (
	"database/sql"
	"testing"
)

func TestPointOf(t *testing.T) {
	x, y := sql.NullFloat64{Float64: 1.5, Valid: true}, sql.NullFloat64{Float64: -2, Valid: true}
	if p := pointOf(x, y); p == nil || *p != (point{1.5, -2}) {
		t.Errorf("pointOf = %v", p)
	}
	if pointOf(x, sql.NullFloat64{}) != nil || pointOf(sql.NullFloat64{}, y) != nil {
		t.Error("a point without both coordinates should be none")
	}
}

func TestIsTrue(t *testing.T) {
	yes, no := true, false
	if !isTrue(&yes) || isTrue(&no) || isTrue(nil) {
		t.Error("isTrue")
	}
}

// Only an input link naming a state is a condition.
func TestCheckCondition(t *testing.T) {
	for _, c := range []struct {
		l  link
		ok bool
	}{
		{link{Direction: directionInput, State: ptr(1), Condition: true}, true},
		{link{Direction: directionInput, Condition: true}, false},
		{link{Direction: directionOutput, State: ptr(1), Condition: true}, false},
		{link{Direction: directionOutput}, true},
		{link{Direction: directionInput}, true},
	} {
		if got := checkCondition(c.l); (got == "") != c.ok {
			t.Errorf("checkCondition(%+v) = %q", c.l, got)
		}
	}
}
