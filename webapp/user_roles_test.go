package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestRoleKey(t *testing.T) {
	for in, want := range map[string]string{
		"Finance":       "finance",
		"  Finance  ":   "finance",
		"FINANCE\t":     "finance",
		"Legal Review ": "legal review",
		"":              "",
	} {
		if got := roleKey(in); got != want {
			t.Errorf("roleKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// Only someone holding a role a human object handling the process names
// decides its reviews, matching names ignoring case and spaces around them;
// a process no human object handles is anyone's, and admins decide any.
func TestMayDecide(t *testing.T) {
	someone := &user{ID: 1}
	for _, c := range []struct {
		u                *user
		roles, handledBy []string
		may              bool
	}{
		{nil, nil, []string{"Finance"}, true},
		{&user{ID: 2, Admin: true}, nil, []string{"Finance"}, true},
		{someone, nil, nil, true},
		{someone, []string{"Legal"}, []string{}, true},
		{someone, []string{"Finance"}, []string{"Finance"}, true},
		{someone, []string{"finance "}, []string{" FINANCE"}, true},
		{someone, []string{"Legal"}, []string{"Finance", "Legal"}, true},
		{someone, []string{"Legal"}, []string{"Finance"}, false},
		{someone, nil, []string{"Finance"}, false},
		{someone, []string{"Fin"}, []string{"Finance"}, false},
		{someone, []string{"Finance Team"}, []string{"Finance"}, false},
	} {
		if got := mayDecide(c.u, c.roles, c.handledBy); got != c.may {
			t.Errorf("mayDecide(%+v, %q, %q) = %v, want %v", c.u, c.roles, c.handledBy, got, c.may)
		}
	}
}

// Roles are kept as given but trimmed, the first of those differing only in
// case, and without empty ones.
func TestCleanRoles(t *testing.T) {
	for _, c := range []struct {
		in, want []string
		problem  string
	}{
		{nil, []string{}, ""},
		{[]string{" Finance ", "Legal"}, []string{"Finance", "Legal"}, ""},
		{[]string{"", "  ", "Finance"}, []string{"Finance"}, ""},
		{[]string{"Finance", "finance", " FINANCE "}, []string{"Finance"}, ""},
		{[]string{strings.Repeat("a", maxRoleName)}, []string{strings.Repeat("a", maxRoleName)}, ""},
		{[]string{"  " + strings.Repeat("a", maxRoleName) + "  "}, []string{strings.Repeat("a", maxRoleName)}, ""},
		{[]string{"Finance", strings.Repeat("a", maxRoleName+1)}, nil, "at most 255"},
	} {
		got, msg := cleanRoles(c.in)
		if (c.problem == "") != (msg == "") || !strings.Contains(msg, c.problem) {
			t.Errorf("cleanRoles(%q) says %q, want %q", c.in, msg, c.problem)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("cleanRoles(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
