package main

// What an attribute holds, beyond any JSON value: text, maybe matching a
// pattern, an integer, a number, a yes or no, or a date and time. A value of
// another type is refused wherever one is handed in: an agent is told what
// is wrong and fixes it, a script's step fails, and a person entering one, a
// request starting a run and a hand edit of a record are refused (checkTyped,
// through shapeItem). An attribute without a type holds any JSON value, as
// every attribute did before types. A file attribute holds the path of a
// file, and one referring to records their keys, so neither has a type.
//
// A pattern is a regular expression the whole of the text must match, as an
// HTML form field's pattern does: [A-Z]+-\d+ takes TALK-123 but not
// "TALK-123 again". It is Go's syntax (RE2), which every check here uses;
// the worker checks it again in JavaScript as an agent hands its output in,
// skipping one JavaScript can't read, since this check has the last word.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The types an attribute can have; typeAny is none.
const (
	typeAny      = ""
	typeText     = "text"
	typeInteger  = "integer"
	typeNumber   = "number"
	typeBoolean  = "boolean"
	typeDatetime = "datetime"
)

var attributeTypes = []string{typeText, typeInteger, typeNumber, typeBoolean, typeDatetime}

// The longest pattern, as its column holds it.
const maxPatternLength = 1000

// The layouts a date and time is written in: RFC 3339, or a date alone.
const dateLayout = "2006-01-02"

// Puts an attribute's type and pattern in their form, a pattern alone making
// it text, and says what is wrong with them, if anything.
func checkAttributeType(a *attribute) string {
	a.Type = strings.TrimSpace(a.Type)
	a.Pattern = strings.TrimSpace(a.Pattern)
	if a.Pattern != "" && a.Type == typeAny {
		a.Type = typeText
	}
	if a.Type != typeAny && !slices.Contains(attributeTypes, a.Type) {
		return fmt.Sprintf("%s isn't a type of attribute: it is one of %s", quote(a.Type), strings.Join(attributeTypes, ", "))
	}
	if a.Pattern != "" {
		if a.Type != typeText {
			return "only text has a pattern"
		}
		if len(a.Pattern) > maxPatternLength {
			return fmt.Sprintf("a pattern is at most %d characters", maxPatternLength)
		}
		// Read as written first, so what is wrong names the person's text.
		_, err := regexp.Compile(a.Pattern)
		if err == nil {
			_, err = compilePattern(a.Pattern)
		}
		if err != nil {
			return "the pattern isn't a regular expression: " + strings.TrimPrefix(err.Error(), "error parsing regexp: ")
		}
	}
	if a.Type != typeAny && a.File {
		return "a file attribute holds the path of a file, so it has no type"
	}
	if a.Type != typeAny && a.Ref != nil {
		return "an attribute referring to records holds their keys, so it has no type"
	}
	return ""
}

// The pattern as it matches the whole of a value.
func anchored(pattern string) string {
	return "^(?:" + pattern + ")$"
}

func compilePattern(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile(anchored(pattern))
}

// Says what is wrong with a value of an attribute of the type, if anything.
func checkTyped(kind, pattern string, v json.RawMessage) error {
	if v = bytes.TrimSpace(v); len(v) == 0 || kind == typeAny {
		return nil
	}
	switch kind {
	case typeText:
		var s string
		if v[0] != '"' || json.Unmarshal(v, &s) != nil {
			return errors.New("it must be text, a JSON string")
		}
		if pattern == "" {
			return nil
		}
		if re, err := compilePattern(pattern); err == nil && !re.MatchString(s) {
			return fmt.Errorf("%s doesn't match its pattern %s", quote(s), pattern)
		}
	case typeInteger:
		if n, ok := jsonNumber(v); !ok || !isInteger(n) {
			return errors.New("it must be an integer, a JSON number such as 3")
		}
	case typeNumber:
		if _, ok := jsonNumber(v); !ok {
			return errors.New("it must be a number, a JSON number such as 0.8")
		}
	case typeBoolean:
		if s := string(v); s != "true" && s != "false" {
			return errors.New("it must be true or false")
		}
	case typeDatetime:
		var s string
		if v[0] != '"' || json.Unmarshal(v, &s) != nil || !isDatetime(s) {
			return errors.New("it must be a date such as \"2026-11-03\", or a date and time in RFC 3339 such as \"2026-11-03T09:00:00Z\"")
		}
	}
	return nil
}

// The value as a JSON number, if it is one.
func jsonNumber(v json.RawMessage) (json.Number, bool) {
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.UseNumber()
	var x any
	if err := dec.Decode(&x); err != nil {
		return "", false
	}
	n, ok := x.(json.Number)
	return n, ok
}

// Whether a JSON number is whole, as 3, 3.0 and 1e3 are.
func isInteger(n json.Number) bool {
	if _, err := n.Int64(); err == nil {
		return true
	}
	f, err := n.Float64()
	return err == nil && !math.IsInf(f, 0) && f == math.Trunc(f)
}

func isDatetime(s string) bool {
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return true
	}
	_, err := time.Parse(dateLayout, s)
	return err == nil
}

// What a value of the type is, in words for an agent, or nothing without a
// type.
func typeWords(kind, pattern string) string {
	switch kind {
	case typeText:
		if pattern != "" {
			return "text matching the regular expression " + anchored(pattern)
		}
		return "text"
	case typeInteger:
		return "an integer"
	case typeNumber:
		return "a number"
	case typeBoolean:
		return "true or false"
	case typeDatetime:
		return "a date such as 2026-11-03, or a date and time in RFC 3339 such as 2026-11-03T09:00:00Z"
	}
	return ""
}

// The type as the diff names it.
func typeLabel(kind string) string {
	switch kind {
	case typeAny:
		return "any"
	case typeBoolean:
		return "yes or no"
	case typeDatetime:
		return "date and time"
	}
	return kind
}

// A JSON Schema of a value of the type, which its definition describes.
func typeSchema(kind, pattern string) map[string]any {
	switch kind {
	case typeText:
		if pattern != "" {
			return map[string]any{"type": "string", "pattern": anchored(pattern)}
		}
		return map[string]any{"type": "string"}
	case typeInteger:
		return map[string]any{"type": "integer"}
	case typeNumber:
		return map[string]any{"type": "number"}
	case typeBoolean:
		return map[string]any{"type": "boolean"}
	case typeDatetime:
		return map[string]any{"type": "string", "anyOf": []any{map[string]any{"format": "date"}, map[string]any{"format": "date-time"}}}
	}
	return map[string]any{}
}

// Says what is wrong with the typed fields of a record written by hand, at
// any depth, leaving out those it doesn't hold or holds empty, as a hand
// edit may, and whether the field's type is written as JSON rather than
// text: a number, or a yes or no.
func typedProblems(fs []field, content map[string]json.RawMessage) (string, bool) {
	for _, f := range fs {
		v, ok := content[f.name]
		if !ok || isEmptyValue(v) {
			continue
		}
		var msg string
		var asJSON bool
		switch {
		case len(f.fields) > 0 && f.many:
			var items []map[string]json.RawMessage
			if json.Unmarshal(v, &items) == nil {
				for i, item := range items {
					if msg, asJSON = typedProblems(f.fields, item); msg != "" {
						msg = fmt.Sprintf("item %d: %s", i+1, msg)
						break
					}
				}
			}
		case len(f.fields) > 0:
			var inner map[string]json.RawMessage
			if json.Unmarshal(v, &inner) == nil {
				msg, asJSON = typedProblems(f.fields, inner)
			}
		default:
			if err := checkTyped(f.kind, f.pattern, v); err != nil {
				msg = err.Error()
				asJSON = f.kind == typeInteger || f.kind == typeNumber || f.kind == typeBoolean
			}
		}
		if msg != "" {
			return quote(f.name) + ": " + msg, asJSON
		}
	}
	return "", false
}

// Whether a value written by hand holds nothing: null or empty text.
func isEmptyValue(v json.RawMessage) bool {
	s := string(bytes.TrimSpace(v))
	return s == "null" || s == `""`
}
