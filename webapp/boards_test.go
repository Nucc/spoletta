package main

import (
	"reflect"
	"testing"
)

// The top board's start and end are the flow's; a flow process's board's
// are its own.
func TestStartRow(t *testing.T) {
	table, cond, args := startRow(7, nil)
	if table != "flow_starts" || cond != "WHERE flow_id = ?" || !reflect.DeepEqual(args, []any{int64(7)}) {
		t.Errorf("startRow(7, nil) = %q, %q, %v", table, cond, args)
	}
	board := int64(12)
	table, cond, args = startRow(7, &board)
	if table != "inner_boards" || cond != "WHERE process_id = ?" || !reflect.DeepEqual(args, []any{int64(12)}) {
		t.Errorf("startRow(7, &12) = %q, %q, %v", table, cond, args)
	}
}

func TestPlaceholders(t *testing.T) {
	for n, want := range map[int]string{1: "(?)", 2: "(?, ?)", 4: "(?, ?, ?, ?)"} {
		if got := placeholders(n); got != want {
			t.Errorf("placeholders(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestInt64Args(t *testing.T) {
	if got := int64Args(nil); len(got) != 0 {
		t.Errorf("int64Args(nil) = %v", got)
	}
	if got, want := int64Args([]int64{3, 1, 2}), []any{int64(3), int64(1), int64(2)}; !reflect.DeepEqual(got, want) {
		t.Errorf("int64Args = %v, want %v", got, want)
	}
}
