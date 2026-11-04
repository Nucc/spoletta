package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"regexp"
	"strings"
)

// A flow may name the branch its runs work on after what they work on,
// rather than give each a branch of its own: with feature/{@Ticket.key} in
// its settings (flowSettings.Branch), every run given ticket PROJ-123, or
// taking its record, works on feature/PROJ-123, so each flow working on that
// ticket works on the same branch, the one its pull request is from. Each
// {@Object.attribute} is that attribute of an object on the top board the
// run holds as it starts: what it was given (by a person, the API, a
// webhook, a subflow process) or a record it took (picks.go, references.go);
// {@Object} is a plain value whole. A run a person or a task board started
// on a record holds it too, for a flow querying its struct (record_runs.go).
// Names are matched as the search's facets are, so @Pull_request.branch
// reads Pull request's branch.
//
// The name is worked out once, as the run starts, and kept on the run
// (runs.branch), so a run taken over, re-run or followed up works on the
// same one; a subflow process's run works out its own flow's from what it
// is given. Trials, and items of a list, which run side by side, keep
// branches of their own. A value missing, or not a branch name git takes,
// leaves the run on a branch of its own, which its log says.
//
// The worker still keeps the run's own branch, <prefix>/run-<id>, which
// releasing, deleting and re-running work by: only the run's checkouts are
// on the name, which starts from origin's latest commit of it if origin has
// it, else from the base branch, and which a push updates. Two runs on the
// same name so never meet in the worker; whichever pushes second pulls
// first. See worker/workspace.go.

// A part of a branch rule: text as it is, or a value the run holds.
type branchPart struct {
	text string
	// The object's name and attribute as the rule writes them, for a value;
	// no attribute for the object's value whole.
	object, attribute string
}

var branchValue = regexp.MustCompile(`\{@([^{}]*)\}`)

// The rule's parts, or what is wrong with it.
func parseBranchRule(rule string) ([]branchPart, error) {
	var parts []branchPart
	at := 0
	for _, m := range branchValue.FindAllStringSubmatchIndex(rule, -1) {
		if m[0] > at {
			parts = append(parts, branchPart{text: rule[at:m[0]]})
		}
		key := strings.TrimSpace(rule[m[2]:m[3]])
		if key == "" {
			return nil, fmt.Errorf("%q names no object", rule[m[0]:m[1]])
		}
		object, attribute, _ := strings.Cut(key, ".")
		parts = append(parts, branchPart{object: object, attribute: attribute})
		at = m[1]
	}
	if at < len(rule) {
		parts = append(parts, branchPart{text: rule[at:]})
	}
	for _, p := range parts {
		if strings.ContainsAny(p.text, "{}") {
			return nil, fmt.Errorf("write each value as {@Object.attribute}, such as feature/{@Ticket.key}")
		}
	}
	return parts, nil
}

// Tidies the rule and says what is wrong with it, if anything: the branch
// it names, with each value standing for one, must be a branch name.
func checkBranchRule(rule *string) string {
	*rule = strings.TrimSpace(*rule)
	if *rule == "" {
		return ""
	}
	parts, err := parseBranchRule(*rule)
	if err != nil {
		return "the branch: " + err.Error()
	}
	var b strings.Builder
	for _, p := range parts {
		if p.object != "" {
			b.WriteString("x")
		}
		b.WriteString(p.text)
	}
	if !gitBranch(b.String()) {
		return fmt.Sprintf("the branch %q isn't a valid branch name", *rule)
	}
	return ""
}

// What git takes as a branch name, as far as validBranch goes and a part
// not starting with a dot.
func gitBranch(b string) bool {
	return validBranch(b) && !strings.Contains(b, "/.")
}

// The objects on the top board a rule's value names: the object, matched
// by name as a search facet is, and its attribute, nil for its value whole.
func ruleObject(board snapshot, p branchPart) (*snapshotObject, *attribute) {
	for i, o := range board.Objects {
		if o.Parent != nil || o.Owner != nil || !strings.EqualFold(slug(o.Name), slug(p.object)) {
			continue
		}
		if p.attribute == "" {
			return &board.Objects[i], nil
		}
		for j, a := range board.Attributes {
			if a.Object == o.ID && strings.EqualFold(slug(a.Name), slug(p.attribute)) {
				return &board.Objects[i], &board.Attributes[j]
			}
		}
		return &board.Objects[i], nil
	}
	return nil, nil
}

// The branch the rule names for a run holding given as it starts, by
// object, or "" and why when it names none: a value it lacks, or one that
// isn't a branch name.
func ruleBranch(rule string, board snapshot, given map[int64]value) (string, string) {
	if rule == "" {
		return "", ""
	}
	parts, err := parseBranchRule(rule)
	if err != nil {
		return "", err.Error()
	}
	var b strings.Builder
	for _, p := range parts {
		if p.object == "" {
			b.WriteString(p.text)
			continue
		}
		o, a := ruleObject(board, p)
		switch {
		case o == nil:
			return "", fmt.Sprintf("the top board has no object %s", quote(p.object))
		case p.attribute != "" && a == nil:
			return "", fmt.Sprintf("%s has no attribute %s", quote(o.Name), quote(p.attribute))
		}
		v, ok := given[o.ID]
		if !ok {
			return "", fmt.Sprintf("the run wasn't given %s and took no record of it", quote(o.Name))
		}
		field := ""
		if a != nil {
			field = a.Name
		}
		s, why := branchText(v.content, field)
		if why == "" {
			if s = branchSafe(s); s == "" {
				why = "holds nothing a branch name takes"
			}
		}
		if why != "" {
			if a != nil {
				return "", fmt.Sprintf("%s's %s %s", quote(o.Name), quote(a.Name), why)
			}
			return "", fmt.Sprintf("%s %s", quote(o.Name), why)
		}
		b.WriteString(s)
	}
	name := tidyBranch(b.String())
	if !gitBranch(name) {
		return "", fmt.Sprintf("%q isn't a branch name git takes", name)
	}
	return name, ""
}

var (
	branchSpaces = regexp.MustCompile(`\s+`)
	// What a branch name doesn't take, as validBranch has it: all but ASCII
	// letters and digits, dots, dashes, underscores and slashes, and the
	// spaces that become underscores.
	branchIllegal = regexp.MustCompile(`[^A-Za-z0-9._/\s-]+`)
)

// A value as a branch name takes it: its spaces as underscores, and the
// characters git doesn't take, or validBranch doesn't, left out, so a
// ticket's title "Fix: the login (urgent)" names Fix_the_login_urgent.
// Letters outside ASCII are left out too, accents and all.
func branchSafe(s string) string {
	s = branchIllegal.ReplaceAllString(s, "")
	return branchSpaces.ReplaceAllString(strings.TrimSpace(s), "_")
}

// The branch name made of the rule's text and its values, tidied as git
// would have it: no empty parts, no part starting with a dot, no "..", not
// ending in a dot, a slash or .lock, starting with a letter or digit, and
// at most as long as validBranch takes. The rule's own text, checked as
// it was saved, passes as it is.
func tidyBranch(name string) string {
	for strings.Contains(name, "..") {
		name = strings.ReplaceAll(name, "..", ".")
	}
	var parts []string
	for _, p := range strings.Split(name, "/") {
		if p = strings.TrimLeft(p, "."); p != "" {
			parts = append(parts, p)
		}
	}
	name = strings.TrimLeft(strings.Join(parts, "/"), "-_./")
	if len(name) > 200 {
		name = name[:200]
	}
	for {
		trimmed := strings.TrimSuffix(strings.TrimRight(name, "./"), ".lock")
		if trimmed == name {
			return name
		}
		name = trimmed
	}
}

// The text of a value, or of its field if named, a string or a number, or
// why there is none.
func branchText(content, field string) (string, string) {
	var v any
	d := json.NewDecoder(strings.NewReader(content))
	d.UseNumber()
	if err := d.Decode(&v); err != nil || d.More() {
		if field != "" {
			return "", "isn't there, as the value isn't a JSON object"
		}
		// Plain text, as an object without attributes holds.
		v = strings.TrimSpace(content)
	}
	if field != "" {
		fields, ok := v.(map[string]any)
		if !ok {
			return "", "isn't there, as the value isn't a JSON object"
		}
		if v, ok = fields[field]; !ok || v == nil {
			return "", "is empty"
		}
	}
	switch x := v.(type) {
	case string:
		switch {
		case x == "":
			return "", "is empty"
		case strings.TrimSpace(x) == "":
			return "", "is blank"
		}
		return strings.TrimSpace(x), ""
	case json.Number:
		return x.String(), ""
	}
	return "", "isn't text or a number; name an attribute holding one"
}

// The branch a run's checkouts are on as its flow's rule names it; or, with
// no name, why the rule names none for the run, which the worker logs as it
// makes the run's own branch, <prefix>/run-<id>, instead. Neither without
// a rule.
type namedBranch struct {
	name, unnamed string
}

// The branch fr's rule names for a run holding given as it starts, and the
// record a person started it on, or "" and why it names none.
func resolveBranch(db *sql.DB, fr *flowRun, given map[int64]value) (string, string) {
	holding, err := withStartRecord(db, fr, given)
	if err != nil {
		return "", fmt.Sprintf("the branch %s couldn't be named, as the record the run was started on couldn't be read: %v", fr.rule, err)
	}
	name, why := ruleBranch(fr.rule, fr.board, holding)
	if name == "" {
		return "", fmt.Sprintf("the branch %s couldn't be named: %s", fr.rule, why)
	}
	return name, ""
}

// Works out the branch run works on from what it holds as it starts, given,
// as fr's rule names it, and keeps it on the run, saying so in its log; or
// says there why it works on a branch of its own. None without a rule or
// repositories.
func nameBranch(db *sql.DB, run int64, fr *flowRun, given map[int64]value) namedBranch {
	if fr.rule == "" || len(fr.repos) == 0 {
		return namedBranch{}
	}
	name, why := resolveBranch(db, fr, given)
	if name == "" {
		runLog(db, run, 0, logInfo, "works on a branch of its own, as %s", why)
		return namedBranch{unnamed: why}
	}
	keepBranch(db, run, name)
	return namedBranch{name: name}
}

// The branch a run taken over, or a re-run, works on: the one kept, else,
// if its flow has a rule, why it names none, said again for its clones. A
// re-run works on the branch the run it re-runs did, never one named anew.
func keptOrUnnamed(db *sql.DB, fr *flowRun, given map[int64]value, kept string, rerun bool) namedBranch {
	switch {
	case len(fr.repos) == 0:
		return namedBranch{}
	case kept != "":
		return namedBranch{name: kept}
	case fr.rule == "":
		return namedBranch{}
	case rerun:
		return namedBranch{unnamed: "the run it re-runs worked on a branch of its own, which a re-run keeps to"}
	}
	if name, why := resolveBranch(db, fr, given); name == "" {
		return namedBranch{unnamed: why}
	}
	return namedBranch{unnamed: fmt.Sprintf("the branch %s named none as the run started", fr.rule)}
}

// What the run holds as it starts, with the record a person started it on
// (record_runs.go), which a flow querying its struct rather than picking a
// record isn't given, for each copy of its struct on the top board.
func withStartRecord(db *sql.DB, fr *flowRun, given map[int64]value) (map[int64]value, error) {
	if fr.record == 0 {
		return given, nil
	}
	var store int64
	var content string
	err := db.QueryRowContext(context.Background(), "SELECT object_id, content FROM shared_records WHERE id = ? AND deleted_at IS NULL", fr.record).Scan(&store, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return given, nil
	}
	if err != nil {
		return nil, err
	}
	holding := map[int64]value{}
	maps.Copy(holding, given)
	for _, o := range fr.board.Objects {
		if _, ok := holding[o.ID]; !ok && o.Store != nil && *o.Store == store && o.Parent == nil && o.Owner == nil && !o.Many {
			holding[o.ID] = value{content: content}
		}
	}
	return holding, nil
}

// Keeps on the run the branch its checkouts are on and says so in its log,
// once: a run taken over names it again.
func keepBranch(db *sql.DB, run int64, name string) {
	res, err := db.ExecContext(context.Background(), "UPDATE runs SET branch = ? WHERE id = ? AND branch <> ?", name, run, name)
	if err != nil {
		log.Printf("keep the branch of run %d: %v", run, err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		runLog(db, run, 0, logInfo, "works on the branch %s, starting from origin's latest commit of it if origin has it", name)
	}
}

// The branch a run's checkouts are on, as kept; "" for its own.
func keptBranch(ctx context.Context, db *sql.DB, run int64) (string, error) {
	var name string
	err := db.QueryRowContext(ctx, "SELECT branch FROM runs WHERE id = ?", run).Scan(&name)
	return name, err
}
