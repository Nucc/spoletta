package main

import (
	"reflect"
	"strings"
	"testing"
)

// A team's name is trimmed and required when given, and its members come
// sorted without repeats; what isn't given stays so.
func TestTeamBodyCheck(t *testing.T) {
	str := func(s string) *string { return &s }
	ids := func(n ...int64) *[]int64 { return &n }
	for _, c := range []struct {
		name, wantName   *string
		members, wantIDs *[]int64
		problem          string
	}{
		{nil, nil, nil, nil, ""},
		{str("  Payments "), str("Payments"), nil, nil, ""},
		{str(""), nil, nil, nil, "name is required"},
		{str("   "), nil, nil, nil, "name is required"},
		{str(strings.Repeat("a", maxTeamName)), str(strings.Repeat("a", maxTeamName)), nil, nil, ""},
		{str(" " + strings.Repeat("a", maxTeamName) + " "), str(strings.Repeat("a", maxTeamName)), nil, nil, ""},
		{str(strings.Repeat("a", maxTeamName+1)), nil, nil, nil, "at most 255"},
		{nil, nil, ids(3, 1, 2, 3, 1), ids(1, 2, 3), ""},
		{nil, nil, ids(), ids(), ""},
		{str("Platform"), str("Platform"), ids(5, 5), ids(5), ""},
	} {
		b := teamBody{Name: c.name, Members: c.members}
		msg := b.check()
		if (c.problem == "") != (msg == "") || !strings.Contains(msg, c.problem) {
			t.Errorf("check(%+v) = %q, want %q", b, msg, c.problem)
			continue
		}
		if msg != "" {
			continue
		}
		if !reflect.DeepEqual(b.Name, c.wantName) {
			t.Errorf("name = %v, want %v", b.Name, c.wantName)
		}
		if !reflect.DeepEqual(b.Members, c.wantIDs) {
			t.Errorf("members = %v, want %v", b.Members, c.wantIDs)
		}
	}
}
