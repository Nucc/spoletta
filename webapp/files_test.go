package main

import (
	"slices"
	"testing"
)

// Only strings under the files root are files a generated object names, at
// any depth of what it holds, in the order they appear.
func TestFilesIn(t *testing.T) {
	a, b, c := filesRoot+"/runs/1/steps/2/a.pdf", filesRoot+"/runs/1/steps/2/b.png", filesRoot+"/runs/1/steps/3/c.txt"
	for _, tc := range []struct {
		content string
		want    []string
	}{
		{`not json`, nil},
		{`{"title": "x", "n": 3, "ok": true, "none": null}`, nil},
		{quote(a), []string{a}},
		{`[` + quote(a) + `, ` + quote(b) + `]`, []string{a, b}},
		{`{"report": {"pages": [{"img": ` + quote(b) + `}], "pdf": ` + quote(a) + `}, "other": [[` + quote(c) + `]]}`, []string{b, a, c}},
		{`{"z": ` + quote(c) + `, "m": ` + quote(a) + `, "a": ` + quote(b) + `}`, []string{c, a, b}},
		// A key is never a file, even one that looks like a path.
		{`{` + quote(a) + `: "x", "y": {` + quote(b) + `: ` + quote(c) + `}}`, []string{c}},
		{`{"a": [` + quote(a) + `, {}], "b": ` + quote(b), nil},
		// The root itself, a sibling sharing its prefix, and a relative path aren't files under it.
		{`[` + quote(filesRoot) + `, ` + quote(filesRoot+"-old/x") + `, "runs/1/x", ` + quote("see "+a) + `]`, nil},
	} {
		got := filesIn(tc.content)
		if !slices.Equal(got, tc.want) {
			t.Errorf("filesIn(%s) = %v, want %v", tc.content, got, tc.want)
		}
	}
}
