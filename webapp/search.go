package main

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// A search narrows a table page's rows, a flow's runs (rundata.go) or a
// shared struct's records (shared.go), by a query written the way
// Datadog's log explorer takes one, over every row stored rather than only
// those a page shows. Each column a row can be searched by is a facet,
// named by a key: the table's own ones bare (status, state, started), an
// object's attributes with an @ (@Invoice.amount), which may be left out
// where no bare key has the name, as may the object of an attribute
// (title for @Bug.title). A query is terms, all of which a row has to
// match:
//
//	timeout            any value holding the text, "a phrase" too
//	status:failed      a facet's value, ignoring case; * stands for any text
//	-status:done       a row that doesn't match; NOT status:done too
//	status:(failed OR interrupted)
//	a OR b, ( )        either, and grouping
//	@Invoice.amount:>100, :<=5, :[10 TO 20]
//	started:>now-1d    times as dates, or now less minutes, hours, days, weeks
//	@Bug.owner:*       a row with any value for it
//
// Typing never fails: what doesn't parse is taken as text, and a key no
// facet has matches nothing. A key with nothing after its colon yet doesn't
// narrow anything. The board's search bar (frontend query.ts) parses
// queries the same way, to colour them and to tick facets' values.

// What one search reads at most, so a flow's whole history can't make it
// unbounded; the page says when there was more.
const searchedRows = 20000

// How many values of a facet a search counts, the most common first, and
// how long one may be to be counted: longer ones, such as an agent's
// write-up, are found by searching for what they hold, and a facet of only
// such values isn't listed.
const (
	facetValues   = 50
	facetValueLen = 200
)

type searchTest struct {
	op       string // is, exists, <, <=, >, >=, range
	value    string
	from, to string
	literal  bool
}

type searchNode struct {
	kind  string // all, and, or, not, term
	nodes []*searchNode
	// For a term: its key, none for text in any value.
	key    string
	hasKey bool
	test   searchTest
}

var allRows = &searchNode{kind: "all"}

type searchParser struct {
	s []rune
	i int
	// The key a group of values after a colon is for, as in status:(a OR b).
	key *string
}

func parseSearch(text string) *searchNode {
	p := &searchParser{s: []rune(text)}
	var nodes []*searchNode
	for {
		if n := p.or(); n.kind != "all" {
			nodes = append(nodes, n)
		}
		p.ws()
		if p.i >= len(p.s) {
			break
		}
		p.i++ // A ) closing nothing.
	}
	switch len(nodes) {
	case 0:
		return allRows
	case 1:
		return nodes[0]
	}
	return &searchNode{kind: "and", nodes: nodes}
}

func (p *searchParser) at(i int) rune {
	if i < 0 || i >= len(p.s) {
		return 0
	}
	return p.s[i]
}

func (p *searchParser) ws() {
	for p.i < len(p.s) && unicode.IsSpace(p.s[p.i]) {
		p.i++
	}
}

// Whether an operator word starts here, on its own.
func (p *searchParser) word(w string) bool {
	if !strings.HasPrefix(string(p.s[p.i:]), w) {
		return false
	}
	next := p.at(p.i + len(w))
	return next == 0 || unicode.IsSpace(next) || next == '(' || (w != "NOT" && next == ')')
}

func (p *searchParser) or() *searchNode {
	var nodes []*searchNode
	if n := p.and(); n.kind != "all" {
		nodes = append(nodes, n)
	}
	for {
		p.ws()
		if !p.word("OR") {
			break
		}
		p.i += 2
		if n := p.and(); n.kind != "all" {
			nodes = append(nodes, n)
		}
	}
	switch len(nodes) {
	case 0:
		return allRows
	case 1:
		return nodes[0]
	}
	return &searchNode{kind: "or", nodes: nodes}
}

func (p *searchParser) and() *searchNode {
	var nodes []*searchNode
	for {
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] == ')' || p.word("OR") {
			break
		}
		if p.word("AND") {
			p.i += 3
			continue
		}
		if n := p.unary(); n.kind != "all" {
			nodes = append(nodes, n)
		}
	}
	switch len(nodes) {
	case 0:
		return allRows
	case 1:
		return nodes[0]
	}
	return &searchNode{kind: "and", nodes: nodes}
}

func (p *searchParser) unary() *searchNode {
	next := p.at(p.i + 1)
	if p.s[p.i] == '-' && next != 0 && !unicode.IsSpace(next) && next != ')' {
		p.i++
		n := p.primary()
		if n.kind == "all" {
			return n
		}
		return &searchNode{kind: "not", nodes: []*searchNode{n}}
	}
	if p.word("NOT") {
		p.i += 3
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] == ')' {
			return allRows
		}
		n := p.unary()
		if n.kind == "all" {
			return n
		}
		return &searchNode{kind: "not", nodes: []*searchNode{n}}
	}
	return p.primary()
}

func (p *searchParser) primary() *searchNode {
	if p.s[p.i] == '(' {
		p.i++
		n := p.or()
		p.ws()
		if p.at(p.i) == ')' {
			p.i++
		}
		return n
	}
	if p.key != nil {
		return p.value(*p.key)
	}
	if p.s[p.i] == '"' {
		return &searchNode{kind: "term", test: searchTest{op: "is", value: p.quoted(), literal: true}}
	}
	// A key runs up to its colon; a term without one is text.
	j := p.i
	for j < len(p.s) && !unicode.IsSpace(p.s[j]) && !strings.ContainsRune("():", p.s[j]) {
		if p.s[j] == '\\' {
			j++
		}
		j++
	}
	if j < len(p.s) && p.s[j] == ':' && j > p.i {
		key := unescapeSearch(string(p.s[p.i:j]))
		p.i = j + 1
		return p.value(key)
	}
	v := p.bare()
	// A dash on its own is a term being typed.
	if v == "-" {
		return allRows
	}
	return &searchNode{kind: "term", test: searchTest{op: "is", value: v}}
}

// What follows a key's colon.
func (p *searchParser) value(key string) *searchNode {
	term := func(t searchTest) *searchNode { return &searchNode{kind: "term", key: key, hasKey: true, test: t} }
	c := p.at(p.i)
	if c == 0 || unicode.IsSpace(c) || c == ')' {
		return allRows
	}
	switch c {
	case '(':
		outer := p.key
		p.key = &key
		n := p.primary()
		p.key = outer
		return n
	case '"':
		return term(searchTest{op: "is", value: p.quoted(), literal: true})
	case '[':
		rest := string(p.s[p.i+1:])
		inner, _, closed := strings.Cut(rest, "]")
		p.i += 1 + len([]rune(inner))
		if closed {
			p.i++
		}
		from, to, _ := strings.Cut(inner, " TO ")
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if from == "" {
			from = "*"
		}
		if to == "" {
			to = "*"
		}
		return term(searchTest{op: "range", from: from, to: to})
	}
	for _, op := range []string{">=", "<=", ">", "<"} {
		if strings.HasPrefix(string(p.s[p.i:]), op) {
			p.i += len(op)
			var v string
			if p.at(p.i) == '"' {
				v = p.quoted()
			} else {
				v = p.bare()
			}
			if v == "" {
				return allRows
			}
			return term(searchTest{op: op, value: v})
		}
	}
	v := p.bare()
	if v == "*" {
		return term(searchTest{op: "exists"})
	}
	return term(searchTest{op: "is", value: v})
}

// Text up to a space or a bracket, with a backslash keeping what follows it.
func (p *searchParser) bare() string {
	start := p.i
	for p.i < len(p.s) && !unicode.IsSpace(p.s[p.i]) && p.s[p.i] != '(' && p.s[p.i] != ')' {
		if p.s[p.i] == '\\' {
			p.i++
		}
		p.i++
	}
	p.i = min(p.i, len(p.s))
	if p.i == start {
		p.i++ // A stray character, taken as text of its own.
		if p.i > len(p.s) {
			return ""
		}
	}
	return unescapeSearch(string(p.s[start:p.i]))
}

func (p *searchParser) quoted() string {
	var out []rune
	p.i++
	for p.i < len(p.s) && p.s[p.i] != '"' {
		if p.s[p.i] == '\\' && p.i+1 < len(p.s) {
			p.i++
		}
		out = append(out, p.s[p.i])
		p.i++
	}
	if p.i < len(p.s) {
		p.i++
	}
	return string(out)
}

var searchEscape = regexp.MustCompile(`\\(.)`)

func unescapeSearch(s string) string { return searchEscape.ReplaceAllString(s, "$1") }

// A facet key out of a name, with what would end a term in a query, such
// as spaces and colons, made underscores.
var slugBreaks = regexp.MustCompile(`[\s:()"\\\[\]]+`)

func slug(name string) string {
	if s := slugBreaks.ReplaceAllString(strings.TrimSpace(name), "_"); s != "" {
		return s
	}
	return "_"
}

// A column rows are searched by, as the search bar and the facet list show
// it. Object, kind and field say which of the page's columns it is.
type facet struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Group  string `json:"group"`
	Object string `json:"object,omitempty"`
	Kind   string `json:"kind,omitempty"` // state, field or whole, for an object's
	Field  string `json:"field,omitempty"`
	// Dates compare as times. Hidden facets, such as dates and those whose
	// every row has its own value, aren't listed.
	Date    bool `json:"date,omitempty"`
	Hidden  bool `json:"hidden,omitempty"`
	Numeric bool `json:"numeric"`
	// Its most common values among the rows the rest of the query matches,
	// with any the query ticks; how many values it has among all rows; and
	// how many rows the rest matches have none.
	Values   []facetCount `json:"values"`
	Distinct int          `json:"distinct"`
	None     int          `json:"none"`
}

type facetCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// The facets of the rows searched, in the order they were met, and each
// row's values of them.
type searchTable struct {
	facets []*facet
	byID   map[string]int
	byKey  map[string][]int
	rows   []map[int][]string
}

func newSearchTable() *searchTable {
	return &searchTable{byID: map[string]int{}, byKey: map[string][]int{}}
}

// The facet of the id, added as given when it is new.
func (t *searchTable) facet(id string, f facet) int {
	if i, ok := t.byID[id]; ok {
		return i
	}
	i := len(t.facets)
	f.Values = []facetCount{}
	t.facets = append(t.facets, &f)
	t.byID[id] = i
	k := strings.ToLower(f.Key)
	t.byKey[k] = append(t.byKey[k], i)
	return i
}

// A row, its values put in with set.
func (t *searchTable) row() map[int][]string {
	r := map[int][]string{}
	t.rows = append(t.rows, r)
	return r
}

// The facets a key names, ignoring case, with or without its @, or else
// the attributes of that name of any object: title for @Bug.title.
func (t *searchTable) resolve(key string) []int {
	k := strings.ToLower(key)
	if is, ok := t.byKey[k]; ok {
		return is
	}
	if other, ok := strings.CutPrefix(k, "@"); ok {
		if is, ok := t.byKey[other]; ok {
			return is
		}
	} else if is, ok := t.byKey["@"+k]; ok {
		return is
	}
	tail := "." + strings.TrimPrefix(k, "@")
	var out []int
	for i, f := range t.facets {
		if fk := strings.ToLower(f.Key); strings.HasPrefix(fk, "@") && strings.HasSuffix(fk, tail) {
			out = append(out, i)
		}
	}
	return out
}

// A value's text for searching: none for nothing or empty text, each item
// of a list, and fields held together as JSON.
func scalars(v any) []string {
	switch v := v.(type) {
	case nil:
		return nil
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case json.Number:
		return []string{v.String()}
	case bool:
		return []string{strconv.FormatBool(v)}
	case float64:
		return []string{strconv.FormatFloat(v, 'f', -1, 64)}
	case int64:
		return []string{strconv.FormatInt(v, 10)}
	case []any:
		var out []string
		for _, e := range v {
			switch e.(type) {
			case map[string]any, []any:
				b, _ := json.Marshal(e)
				out = append(out, string(b))
			default:
				out = append(out, scalars(e)...)
			}
		}
		return out
	default:
		b, _ := json.Marshal(v)
		return []string{string(b)}
	}
}

// How deep strings holding JSON are unwrapped, so odd content can't recurse forever.
const maxUnwrap = 8

// The JSON object or array an object's content holds, inside a code fence
// or not, with strings that hold JSON again replaced by what they hold,
// as agents often hand values in encoded twice; nil for other text. The
// board's cells read content the same way (Data.tsx).
func contentValue(content string) any {
	s := stripFence(content)
	if !strings.HasPrefix(s, "{") && !strings.HasPrefix(s, "[") {
		return nil
	}
	v, ok := decodeJSON(s)
	if !ok {
		return nil
	}
	switch v := unwrapJSON(v, 0).(type) {
	case map[string]any, []any:
		return v
	}
	return nil
}

func decodeJSON(s string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return nil, false
	}
	return v, true
}

func unwrapJSON(v any, depth int) any {
	if depth > maxUnwrap {
		return v
	}
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if !strings.HasPrefix(s, "{") && !strings.HasPrefix(s, "[") && !strings.HasPrefix(s, `"`) {
			return x
		}
		inner, ok := decodeJSON(s)
		if !ok {
			return x
		}
		switch inner.(type) {
		case string, map[string]any, []any:
			return unwrapJSON(inner, depth+1)
		}
		return x
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = unwrapJSON(e, depth+1)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = unwrapJSON(e, depth+1)
		}
		return out
	}
	return v
}

var numberText = regexp.MustCompile(`(?i)^-?(\d+\.?\d*|\.\d+)(e[+-]?\d+)?$`)

func isNumber(s string) bool { return numberText.MatchString(strings.TrimSpace(s)) }

var (
	relativeTime = regexp.MustCompile(`(?i)^now(?:-(\d+(?:\.\d+)?)([smhdw]))?$`)
	timeUnits    = map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}
	timeLayouts  = []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15", "2006-01-02", "2006-01", "2006"}
)

// A time a query names: now, now less some minutes, hours, days or weeks
// (now-15m, now-7d), or a date, with or without its time, in UTC.
func timeOf(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if m := relativeTime.FindStringSubmatch(s); m != nil {
		now := time.Now()
		if m[1] == "" {
			return now, true
		}
		n, _ := strconv.ParseFloat(m[1], 64)
		return now.Add(-time.Duration(n * float64(timeUnits[strings.ToLower(m[2])]))), true
	}
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// How a value compares with another of its facet: as times, numbers or text.
func compareValues(a, b string, date bool) int {
	if date {
		if x, ok := timeOf(a); ok {
			if y, ok := timeOf(b); ok {
				return x.Compare(y)
			}
		}
	}
	if isNumber(a) && isNumber(b) {
		x, _ := strconv.ParseFloat(strings.TrimSpace(a), 64)
		y, _ := strconv.ParseFloat(strings.TrimSpace(b), 64)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	}
	return naturalCompare(strings.ToLower(a), strings.ToLower(b))
}

// Text order with runs of digits as numbers, so item 2 comes before item 10.
func naturalCompare(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	i, j := 0, 0
	for i < len(ra) && j < len(rb) {
		if unicode.IsDigit(ra[i]) && unicode.IsDigit(rb[j]) {
			si, sj := i, j
			for i < len(ra) && unicode.IsDigit(ra[i]) {
				i++
			}
			for j < len(rb) && unicode.IsDigit(rb[j]) {
				j++
			}
			x := strings.TrimLeft(string(ra[si:i]), "0")
			y := strings.TrimLeft(string(rb[sj:j]), "0")
			if len(x) != len(y) {
				return len(x) - len(y)
			}
			if c := strings.Compare(x, y); c != 0 {
				return c
			}
			continue
		}
		if ra[i] != rb[j] {
			if ra[i] < rb[j] {
				return -1
			}
			return 1
		}
		i++
		j++
	}
	return (len(ra) - i) - (len(rb) - j)
}

func wildcard(pattern string, anchored bool) *regexp.Regexp {
	parts := strings.Split(pattern, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	body := strings.Join(parts, ".*")
	if anchored {
		body = "^" + body + "$"
	}
	return regexp.MustCompile("(?is)" + body)
}

// Whether one of a facet's values passes the test; re is its wildcards
// compiled, if it has any.
func (t searchTest) passes(values []string, f *facet, re *regexp.Regexp) bool {
	if t.op == "exists" {
		return len(values) > 0
	}
	want := strings.ToLower(t.value)
	for _, v := range values {
		switch t.op {
		case "is":
			if re != nil {
				if re.MatchString(v) {
					return true
				}
				continue
			}
			lv := strings.ToLower(v)
			if lv == want {
				return true
			}
			// A day, or a day and an hour, names every time within it.
			if f.Date && len(want) >= 4 && strings.HasPrefix(lv, want) {
				return true
			}
			if f.Numeric && isNumber(t.value) && isNumber(v) && compareValues(v, t.value, false) == 0 {
				return true
			}
		case "range":
			if (t.from == "*" || compareValues(v, t.from, f.Date) >= 0) && (t.to == "*" || compareValues(v, t.to, f.Date) <= 0) {
				return true
			}
		default:
			c := compareValues(v, t.value, f.Date)
			if (t.op == "<" && c < 0) || (t.op == "<=" && c <= 0) || (t.op == ">" && c > 0) || (t.op == ">=" && c >= 0) {
				return true
			}
		}
	}
	return false
}

// A search ready to match rows: its terms' keys resolved, and wildcards compiled.
type search struct {
	t    *searchTable
	keys map[string][]int
	res  map[*searchNode]*regexp.Regexp
}

func (t *searchTable) search() *search {
	return &search{t: t, keys: map[string][]int{}, res: map[*searchNode]*regexp.Regexp{}}
}

func (s *search) resolve(key string) []int {
	if is, ok := s.keys[key]; ok {
		return is
	}
	is := s.t.resolve(key)
	s.keys[key] = is
	return is
}

func (s *search) matches(n *searchNode, row map[int][]string) bool {
	switch n.kind {
	case "all":
		return true
	case "and":
		for _, c := range n.nodes {
			if !s.matches(c, row) {
				return false
			}
		}
		return true
	case "or":
		for _, c := range n.nodes {
			if s.matches(c, row) {
				return true
			}
		}
		return false
	case "not":
		return !s.matches(n.nodes[0], row)
	}
	if n.hasKey {
		re, ok := s.res[n]
		if !ok && n.test.op == "is" && !n.test.literal && strings.Contains(n.test.value, "*") {
			re = wildcard(n.test.value, true)
			s.res[n] = re
		}
		for _, i := range s.resolve(n.key) {
			if n.test.passes(row[i], s.t.facets[i], re) {
				return true
			}
		}
		return false
	}
	// Text: any value holding it, or matching its wildcards.
	if n.test.op != "is" {
		return false
	}
	re, ok := s.res[n]
	if !ok && !n.test.literal && strings.Contains(n.test.value, "*") {
		re = wildcard(n.test.value, false)
		s.res[n] = re
	}
	want := strings.ToLower(n.test.value)
	for _, vs := range row {
		for _, v := range vs {
			if re != nil && re.MatchString(v) || re == nil && strings.Contains(strings.ToLower(v), want) {
				return true
			}
		}
	}
	return false
}

// The terms all of which a row has to match.
func clauses(n *searchNode) []*searchNode {
	switch n.kind {
	case "all":
		return nil
	case "and":
		return n.nodes
	}
	return []*searchNode{n}
}

// Whether the term only ticks values of the facets, as status:done,
// status:(done OR failed) or -status:* do, which the facet's own counts
// leave out so ticking one of its values doesn't hide the others.
func (s *search) ticks(n *searchNode, facets string) bool {
	switch n.kind {
	case "not":
		c := n.nodes[0]
		return c.kind == "term" && c.hasKey && c.test.op == "exists" && s.idOf(c.key) == facets
	case "term":
		return n.hasKey && n.test.op == "is" && (n.test.literal || !strings.Contains(n.test.value, "*")) && s.idOf(n.key) == facets
	case "or":
		for _, c := range n.nodes {
			if !s.ticks(c, facets) {
				return false
			}
		}
		return true
	}
	return false
}

// The values the term ticks.
func ticked(n *searchNode) []string {
	switch n.kind {
	case "term":
		return []string{n.test.value}
	case "or":
		var out []string
		for _, c := range n.nodes {
			out = append(out, ticked(c)...)
		}
		return out
	}
	return nil
}

func (s *search) idOf(key string) string {
	is := s.resolve(key)
	out := make([]string, len(is))
	for i, x := range is {
		out[i] = strconv.Itoa(x)
	}
	if len(out) == 0 {
		return "?" + strings.ToLower(key)
	}
	return strings.Join(out, ",")
}

// The rows the query matches, by index, in the order the sort asks, rows
// with no value for it last either way; in the order they came without one.
func (s *search) run(q *searchNode, sortKey string, desc bool) []int {
	var out []int
	for i, r := range s.t.rows {
		if s.matches(q, r) {
			out = append(out, i)
		}
	}
	is := s.resolve(sortKey)
	if sortKey == "" || len(is) == 0 {
		return out
	}
	f := is[0]
	date := s.t.facets[f].Date
	sort.SliceStable(out, func(a, b int) bool {
		x, y := s.t.rows[out[a]][f], s.t.rows[out[b]][f]
		if len(x) == 0 || len(y) == 0 {
			return len(x) > 0
		}
		c := compareValues(x[0], y[0], date)
		if desc {
			return c > 0
		}
		return c < 0
	})
	return out
}

// Counts each listed facet's values among the rows the rest of the query
// matches, and how many values each has among all rows. matched is the
// rows the whole query matches.
func (s *search) count(q *searchNode, matched []int) {
	t := s.t
	for i, f := range t.facets {
		distinct := map[string]bool{}
		for _, r := range t.rows {
			for _, v := range r[i] {
				distinct[v] = true
			}
		}
		f.Distinct = len(distinct)
		f.Numeric = len(distinct) > 0
		short := false
		for v := range distinct {
			if !isNumber(v) {
				f.Numeric = false
			}
			short = short || len(v) <= facetValueLen
		}
		if !short && f.Distinct > 0 {
			f.Hidden = true
		}
	}
	for i, f := range t.facets {
		if f.Hidden || f.Distinct == 0 {
			continue
		}
		id := s.idOf(f.Key)
		rows := matched
		var rest []*searchNode
		var tick []string
		for _, c := range clauses(q) {
			if s.ticks(c, id) {
				tick = append(tick, ticked(c)...)
			} else {
				rest = append(rest, c)
			}
		}
		if len(tick) > 0 {
			rows = nil
			restNode := &searchNode{kind: "and", nodes: rest}
			for j, r := range t.rows {
				if s.matches(restNode, r) {
					rows = append(rows, j)
				}
			}
		}
		counts := map[string]int{}
		for _, j := range rows {
			vs := t.rows[j][i]
			if len(vs) == 0 {
				f.None++
				continue
			}
			seen := map[string]bool{}
			for _, v := range vs {
				if len(v) <= facetValueLen && !seen[v] {
					seen[v] = true
					counts[v]++
				}
			}
		}
		list := make([]facetCount, 0, len(counts))
		for v, n := range counts {
			list = append(list, facetCount{v, n})
		}
		sort.Slice(list, func(a, b int) bool {
			if list[a].Count != list[b].Count {
				return list[a].Count > list[b].Count
			}
			return naturalCompare(list[a].Value, list[b].Value) < 0
		})
		if len(list) > facetValues {
			kept := list[:facetValues]
			// Values the query ticks stay listed, so they can be unticked.
			for _, v := range list[facetValues:] {
				for _, w := range tick {
					if strings.EqualFold(v.Value, w) {
						kept = append(kept, v)
						break
					}
				}
			}
			list = kept
		}
		f.Values = list
	}
}

// The facets as a page gets them.
func (t *searchTable) list() []facet {
	out := make([]facet, len(t.facets))
	for i, f := range t.facets {
		out[i] = *f
	}
	return out
}

// The sort a page asks for: a facet's key, with a - before it for the
// other way.
func sortParam(s string) (string, bool) {
	if k, ok := strings.CutPrefix(s, "-"); ok {
		return k, true
	}
	return s, false
}
