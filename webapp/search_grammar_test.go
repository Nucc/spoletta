package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The search's two parsers, this one and the search bar's (frontend
// query.ts), have to read every query the same way, or the facets the bar
// ticks and the rows the server finds part ways. testdata/search_grammar.json
// lists queries and the tree each has to be read as, written in a form
// neither parser's own shape shows through; this checks the server's parser
// against it, and runs the bar's check (frontend scripts/query-parity.ts)
// when Node can.

type searchGrammar struct {
	Cases []struct {
		Query string `json:"query"`
		AST   string `json:"ast"`
	} `json:"cases"`
	// Values as quote() in query.ts writes them, which have to read back
	// as the very value.
	Quotes []struct {
		Value  string `json:"value"`
		Quoted string `json:"quoted"`
	} `json:"quotes"`
}

func loadSearchGrammar(t *testing.T) searchGrammar {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "search_grammar.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g searchGrammar
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// Text in double quotes, with only backslashes and double quotes escaped,
// as the bar's check writes it.
func grammarQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// A query's tree as the fixture writes it.
func grammarTree(n *searchNode) string {
	q := grammarQuote
	switch n.kind {
	case "all":
		return "all"
	case "and", "or", "not":
		parts := []string{n.kind}
		for _, c := range n.nodes {
			parts = append(parts, grammarTree(c))
		}
		return "(" + strings.Join(parts, " ") + ")"
	}
	key := "_"
	if n.hasKey {
		key = q(n.key)
	}
	var test string
	switch t := n.test; t.op {
	case "is":
		op := "is"
		if t.literal {
			op = "is-literal"
		}
		test = "(" + op + " " + q(t.value) + ")"
	case "exists":
		test = "exists"
	case "range":
		test = "(range " + q(t.from) + " " + q(t.to) + ")"
	default:
		test = "(" + t.op + " " + q(t.value) + ")"
	}
	return "(term " + key + " " + test + ")"
}

func TestSearchGrammar(t *testing.T) {
	g := loadSearchGrammar(t)
	for _, c := range g.Cases {
		if got := grammarTree(parseSearch(c.Query)); got != c.AST {
			t.Errorf("%q\n  got  %s\n  want %s", c.Query, got, c.AST)
		}
	}
	for _, c := range g.Quotes {
		op := "is"
		if strings.HasPrefix(c.Quoted, `"`) {
			op = "is-literal"
		}
		want := "(term \"k\" (" + op + " " + grammarQuote(c.Value) + "))"
		if got := grammarTree(parseSearch("k:" + c.Quoted)); got != want {
			t.Errorf("k:%s\n  got  %s\n  want %s", c.Quoted, got, want)
		}
	}
}

// Node from 22.6 on loads TypeScript with --experimental-strip-types.
var nodeVersion = regexp.MustCompile(`^v(\d+)\.(\d+)\.`)

func TestSearchGrammarInTheBrowser(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node isn't installed")
	}
	out, err := exec.Command(node, "--version").Output()
	if err != nil {
		t.Skipf("node --version: %v", err)
	}
	m := nodeVersion.FindStringSubmatch(strings.TrimSpace(string(out)))
	if m == nil {
		t.Skipf("node's version %q isn't known", out)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 22 || major == 22 && minor < 6 {
		t.Skipf("node %s can't load TypeScript", strings.TrimSpace(string(out)))
	}
	cmd := exec.Command(node, "--experimental-strip-types", "--disable-warning=ExperimentalWarning", "scripts/query-parity.ts")
	cmd.Dir = "frontend"
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the search bar's parser reads queries differently: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}
