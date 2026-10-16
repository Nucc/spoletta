package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestDuplicateMessage(t *testing.T) {
	d := duplicate{Object: "Exception", Attribute: "fingerprint", Value: `"abc"`, Run: 42}
	if got, want := d.message(), `Exception with fingerprint "abc" was already stored by run #42.`; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

// Agents word the same value differently from run to run, so strings are
// the same whatever their case and spacing, and objects whatever their key
// order; missing, null and empty values hold nothing and never collide.
func TestKeyOf(t *testing.T) {
	for _, raw := range []string{"null", `""`, `"  \t "`, "not json", "{"} {
		if k, ok := keyOf([]byte(raw)); ok {
			t.Errorf("keyOf(%s) = %q, want none", raw, k)
		}
	}
	if k, ok := keyOf(nil); ok {
		t.Errorf("keyOf(nil) = %q, want none", k)
	}
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{`"Hello  World"`, `" hello world "`, true},
		{`"Hello\nWorld"`, `"hello world"`, true},
		{`"hello world"`, `"helloworld"`, false},
		{`{"a":"X","b":[" Y "]}`, `{"b":["y"],"a":"x"}`, true},
		{`{"a":1}`, `{"a":2}`, false},
		{`["a","b"]`, `["b","a"]`, false},
		{`1`, `"1"`, false},
		{`12345678901234567890`, `12345678901234567891`, false},
		{`{}`, `[]`, false},
		{`true`, `true`, true},
	} {
		ka, oka := keyOf([]byte(c.a))
		kb, okb := keyOf([]byte(c.b))
		if !oka || !okb {
			t.Errorf("keyOf(%s), keyOf(%s) hold nothing", c.a, c.b)
			continue
		}
		if (ka == kb) != c.same {
			t.Errorf("keyOf(%s) == keyOf(%s) is %v, want %v", c.a, c.b, ka == kb, c.same)
		}
	}
}

func TestNormalize(t *testing.T) {
	got := normalize(map[string]any{"a": "  Big\tDeal ", "b": []any{"X  Y", 1.5, nil, map[string]any{"c": "Z"}}})
	want := map[string]any{"a": "big deal", "b": []any{"x y", 1.5, nil, map[string]any{"c": "z"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("normalize = %#v, want %#v", got, want)
	}
	if got := normalize(true); got != true {
		t.Errorf("normalize(true) = %#v", got)
	}
}

// What collided is said on one line, cut short past 200 characters.
func TestCompact(t *testing.T) {
	long := strings.Repeat("é", 250)
	for _, c := range []struct{ raw, want string }{
		{`"abc"`, `"abc"`},
		{"{\n  \"a\":   1,\n\t\"b\": 2\n}", `{ "a": 1, "b": 2 }`},
		{"", ""},
		{strings.Repeat("x", 200), strings.Repeat("x", 200)},
		{long, strings.Repeat("é", 200) + "…"},
	} {
		if got := compact([]byte(c.raw)); got != c.want {
			t.Errorf("compact(%.20q) = %.30q, want %.30q", c.raw, got, c.want)
		}
	}
}
