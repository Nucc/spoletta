package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// A state can have a rule saying when an object is in it, such as
// confidence >= 0.8 and kind == "bug": comparisons of the object's fields,
// all of which must hold, joined by "and". Each names a field by its path,
// with a dot between an object's field and a field of what it is made of,
// and compares it with a JSON value; a value that isn't JSON is a string.
// An object without attributes holds its value under data. The operators
// are ==, !=, <, <=, >, >= and contains, which finds text in text or an
// item in a list. An object generated in no state comes out in the first of
// its states, in the order they were added, whose rule holds of it; see
// engine.go.
type rule []comparison

type comparison struct {
	path  []string
	op    string
	value any
}

// The operators, longest first so that <= isn't read as <.
var ruleOperators = []string{"==", "!=", "<=", ">=", "<", ">", " contains "}

// How long a rule may be.
const maxRuleBytes = 1 << 10

func parseRule(s string) (rule, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if len(s) > maxRuleBytes {
		return nil, fmt.Errorf("a rule is at most %d bytes", maxRuleBytes)
	}
	var r rule
	for _, part := range strings.Split(s, " and ") {
		c, err := parseComparison(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		r = append(r, c)
	}
	return r, nil
}

func parseComparison(s string) (comparison, error) {
	for _, op := range ruleOperators {
		i := strings.Index(s, op)
		if i < 0 {
			continue
		}
		path := strings.TrimSpace(s[:i])
		value := strings.TrimSpace(s[i+len(op):])
		if path == "" || value == "" {
			break
		}
		c := comparison{op: strings.TrimSpace(op)}
		for _, f := range strings.Split(path, ".") {
			if f = strings.TrimSpace(f); f == "" {
				return comparison{}, fmt.Errorf("%s names no field", quote(path))
			}
			c.path = append(c.path, f)
		}
		if err := json.Unmarshal([]byte(value), &c.value); err != nil {
			c.value = value
		}
		if _, ok := c.value.(float64); !ok && (c.op == "<" || c.op == "<=" || c.op == ">" || c.op == ">=") {
			if _, ok := c.value.(string); !ok {
				return comparison{}, fmt.Errorf("%s compares with %s, which is neither a number nor text", quote(s), c.op)
			}
		}
		return c, nil
	}
	return comparison{}, fmt.Errorf("%s isn't a comparison such as score >= 0.8", quote(s))
}

// Whether the rule holds of what an object holds, as JSON. A field it
// doesn't have holds no comparison but !=.
func (r rule) holds(content string) bool {
	if len(r) == 0 {
		return false
	}
	var v any
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return false
	}
	for _, c := range r {
		if !c.holds(v) {
			return false
		}
	}
	return true
}

func (c comparison) holds(v any) bool {
	got, ok := lookup(v, c.path)
	if !ok {
		return c.op == "!="
	}
	switch c.op {
	case "==":
		return reflect.DeepEqual(got, c.value)
	case "!=":
		return !reflect.DeepEqual(got, c.value)
	case "contains":
		switch g := got.(type) {
		case string:
			s, ok := c.value.(string)
			return ok && strings.Contains(g, s)
		case []any:
			for _, item := range g {
				if reflect.DeepEqual(item, c.value) {
					return true
				}
			}
		}
		return false
	}
	cmp, err := compare(got, c.value)
	if err != nil {
		return false
	}
	switch c.op {
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case ">":
		return cmp > 0
	default:
		return cmp >= 0
	}
}

func lookup(v any, path []string) (any, bool) {
	for _, f := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[f]; !ok {
			return nil, false
		}
	}
	return v, true
}

// Orders two numbers, or two texts.
func compare(a, b any) (int, error) {
	switch x := a.(type) {
	case float64:
		if y, ok := b.(float64); ok {
			switch {
			case x < y:
				return -1, nil
			case x > y:
				return 1, nil
			}
			return 0, nil
		}
	case string:
		if y, ok := b.(string); ok {
			return strings.Compare(x, y), nil
		}
	}
	return 0, errors.New("not comparable")
}
