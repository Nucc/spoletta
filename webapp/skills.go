package main

// Skills and commands people write for agents, in the settings. A skill is
// pi's (see the Agent Skills specification): a name, a description saying
// when it applies, and instructions the agent reads once a task calls for
// them. An agent process picks the skills it is given
// (processSettings.Skills), and the worker hands each to pi as a SKILL.md of
// its own. A command is a prompt kept under a name: /name in an agent
// process's prompt, at the start of a line or after a space, stands for it as
// the step runs. A command taking arguments, as pi's prompt templates do
// ($1, $@, ${1:-default}, ${@:2}, ...), takes what follows it on its line;
// one taking none leaves that be. Only the process's own prompt is expanded,
// never what its inputs or a reviewer bring, and a /word naming no command
// the flow may use, such as a path, stays as it is.
//
// Each is owned as a flow is (see owners.go): its maker's at first, whom
// only they see, then a team's or everyone's as they share it; those it
// belongs to see it and change it, and admins everything. A flow uses what
// its owner may: a person's flow, theirs, their teams' and everyone's; a
// team's flow, the team's and everyone's; a global one, only everyone's. The
// board offers those, and a run uses nothing else, so nobody's own skill
// reaches a flow others work on. Names are pi's, and one of a kind each, so
// a prompt and the setup file name them plainly.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	kindSkill   = "skill"
	kindCommand = "command"
)

// The kinds, by the route listing them.
var skillRoutes = map[string]string{"skills": kindSkill, "commands": kindCommand}

// A skill or command as the settings show it.
type skill struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// What a command takes, as pi's argument-hint: <required> [optional].
	ArgumentHint string    `json:"argumentHint"`
	Content      string    `json:"content"`
	Owner        flowOwner `json:"owner"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// A skill as the worker writes it for pi.
type runSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

// pi's limits on a skill's name and description.
const (
	maxSkillName        = 64
	maxSkillDescription = 1024
	maxSkillContent     = 64 << 10
)

var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func migrateSkills(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS skills (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		kind VARCHAR(16) NOT NULL,
		name VARCHAR(64) NOT NULL,
		description TEXT NOT NULL,
		argument_hint VARCHAR(255) NOT NULL DEFAULT '',
		content MEDIUMTEXT NOT NULL,
		owner_user_id BIGINT NULL,
		owner_team_id BIGINT NULL,
		updated_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
		UNIQUE KEY skills_name (kind, name),
		FOREIGN KEY (owner_user_id) REFERENCES users(id),
		FOREIGN KEY (owner_team_id) REFERENCES teams(id)
	)`)
	return err
}

// The columns of a skill s, for scanSkill.
var skillColumns = "s.id, s.kind, s.name, s.description, s.argument_hint, s.content, s.updated_at, " + ownerColumnsOf("s")

func scanSkill(rows interface{ Scan(...any) error }) (skill, error) {
	var s skill
	var user, team sql.NullInt64
	var owner string
	err := rows.Scan(&s.ID, &s.Kind, &s.Name, &s.Description, &s.ArgumentHint, &s.Content, &s.UpdatedAt, &user, &team, &owner)
	s.Owner = scanOwner(user, team, owner)
	return s, err
}

// The condition on skills s that only the ones the flow may use meet, by
// its owner, and its arguments.
func flowSkills(ctx context.Context, db querier, flow int64) (string, []any, error) {
	var person, team sql.NullInt64
	err := db.QueryRowContext(ctx, "SELECT owner_user_id, owner_team_id FROM flows WHERE id = ?", flow).Scan(&person, &team)
	if err != nil {
		return "", nil, err
	}
	global := "(s.owner_user_id IS NULL AND s.owner_team_id IS NULL)"
	switch {
	case person.Valid:
		// What the owner may change, as if they weren't an admin.
		cond, args := ownedBy("s", &user{ID: person.Int64})
		return cond, args, nil
	case team.Valid:
		return "(" + global + " OR s.owner_team_id = ?)", []any{team.Int64}, nil
	}
	return global, nil, nil
}

// The skills or commands of a kind meeting cond, by name.
func listSkills(ctx context.Context, db rowsQuerier, kind, cond string, args ...any) ([]skill, error) {
	out := []skill{}
	rows, err := db.QueryContext(ctx, "SELECT "+skillColumns+" FROM skills s WHERE s.kind = ? AND "+cond+" ORDER BY s.name",
		append([]any{kind}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		s, err := scanSkill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// The skills an agent process picked, for its step, if its flow may use them
// all; else why not.
func stepSkills(ctx context.Context, db querier, flow int64, ids []int64) ([]runSkill, string, error) {
	if len(ids) == 0 {
		return nil, "", nil
	}
	cond, args, err := flowSkills(ctx, db, flow)
	if err != nil {
		return nil, "", err
	}
	var out []runSkill
	for _, id := range ids {
		var s runSkill
		var usable bool
		err := db.QueryRowContext(ctx, "SELECT s.name, s.description, s.content, "+cond+" FROM skills s WHERE s.id = ? AND s.kind = ?",
			append(args, id, kindSkill)...).Scan(&s.Name, &s.Description, &s.Content, &usable)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil, "a skill the process uses was deleted; pick its skills again", nil
		case err != nil:
			return nil, "", err
		case !usable:
			return nil, fmt.Sprintf("the skill %s isn't one this flow may use: it is neither everyone's nor its owner's; share it with them", s.Name), nil
		}
		out = append(out, s)
	}
	return out, "", nil
}

// Where a command may stand in a prompt: at the start of a line or after a
// space. Its name must end there too, at a space, the line's end or
// punctuation, so /usr/bin names none.
var commandUse = regexp.MustCompile(`(?:^|[\t ])/([a-z0-9]+(?:-[a-z0-9]+)*)`)

// Argument placeholders, as pi's prompt templates have them.
var commandArgs = regexp.MustCompile(`\$\{(\d+|ARGUMENTS|@):-([^}]*)\}|\$\{@:(\d+)(?::(\d+))?\}|\$(ARGUMENTS|@|\d+)`)

// The prompt with each command it names that the flow may use spelled out.
// A /name the flow may not use stays as it is, as one naming none does: a
// prompt can't be told from a path, and someone's own command mustn't break,
// or show itself in, the flows of others.
func expandCommands(ctx context.Context, db *sql.DB, flow int64, prompt string) (string, error) {
	if !strings.Contains(prompt, "/") {
		return prompt, nil
	}
	cond, args, err := flowSkills(ctx, db, flow)
	if err != nil {
		return "", err
	}
	usable := map[string]string{}
	err = scanAll(ctx, db, "SELECT s.name, s.content FROM skills s WHERE s.kind = ? AND "+cond, func(rows *sql.Rows) error {
		var name, content string
		err := rows.Scan(&name, &content)
		usable[name] = content
		return err
	}, append([]any{kindCommand}, args...)...)
	if err != nil {
		return "", err
	}
	lines := strings.Split(prompt, "\n")
	for i, line := range lines {
		lines[i] = expandLine(line, usable)
	}
	return strings.Join(lines, "\n"), nil
}

// One line of a prompt with its commands spelled out.
func expandLine(line string, commands map[string]string) string {
	var b strings.Builder
	done := 0
	for _, m := range commandUse.FindAllStringSubmatchIndex(line, -1) {
		slash, end := m[2]-1, m[3]
		if end < len(line) && !strings.ContainsRune(" \t.,;:!?)", rune(line[end])) {
			continue
		}
		content, ok := commands[line[m[2]:end]]
		if !ok {
			continue
		}
		b.WriteString(line[done:slash])
		content = strings.TrimSpace(content)
		if commandArgs.MatchString(content) {
			b.WriteString(substituteArgs(content, commandArguments(line[end:])))
			return b.String()
		}
		b.WriteString(content)
		done = end
	}
	b.WriteString(line[done:])
	return b.String()
}

// Splits a command's arguments as pi does: at spaces, but not within quotes.
func commandArguments(s string) []string {
	var args []string
	var cur strings.Builder
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if cur.Len() > 0 {
				args = append(args, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		args = append(args, cur.String())
	}
	return args
}

// Fills a command's placeholders with its arguments, as pi does; what the
// arguments hold isn't filled in again.
func substituteArgs(content string, args []string) string {
	all := strings.Join(args, " ")
	return commandArgs.ReplaceAllStringFunc(content, func(match string) string {
		m := commandArgs.FindStringSubmatch(match)
		switch {
		case m[1] != "":
			v := all
			if m[1] != "@" && m[1] != "ARGUMENTS" {
				v = argAt(args, m[1])
			}
			if v == "" {
				return m[2]
			}
			return v
		case m[3] != "":
			start, _ := strconv.Atoi(m[3])
			start = max(start-1, 0)
			if start > len(args) {
				return ""
			}
			end := len(args)
			if m[4] != "" {
				n, _ := strconv.Atoi(m[4])
				end = min(start+n, end)
			}
			return strings.Join(args[start:end], " ")
		case m[5] == "@" || m[5] == "ARGUMENTS":
			return all
		}
		return argAt(args, m[5])
	})
}

// The argument at a position counted from 1, or "".
func argAt(args []string, pos string) string {
	i, err := strconv.Atoi(pos)
	if err != nil || i < 1 || i > len(args) {
		return ""
	}
	return args[i-1]
}

// What a request makes or changes a skill or command to.
type skillFields struct {
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	ArgumentHint string     `json:"argumentHint"`
	Content      string     `json:"content"`
	Owner        *flowOwner `json:"owner"`
}

// Trims what a request gives and says what is wrong with it, if anything.
func (f *skillFields) check(kind string) string {
	f.Name = strings.TrimPrefix(strings.TrimSpace(f.Name), "/")
	f.Description = strings.TrimSpace(f.Description)
	f.ArgumentHint = strings.TrimSpace(f.ArgumentHint)
	switch {
	case len(f.Name) > maxSkillName || !skillName.MatchString(f.Name):
		return fmt.Sprintf("a %s's name is lowercase letters, digits and single hyphens between them, at most %d", kind, maxSkillName)
	case kind == kindSkill && f.Description == "":
		return "a skill needs a description saying what it does and when it applies, which the agent picks it by"
	case utf8.RuneCountInString(f.Description) > maxSkillDescription:
		return fmt.Sprintf("a description is at most %d characters", maxSkillDescription)
	case len(f.ArgumentHint) > 255:
		return "the arguments it takes are at most 255 characters"
	case strings.TrimSpace(f.Content) == "":
		if kind == kindSkill {
			return "a skill needs its instructions"
		}
		return "a command needs the prompt it stands for"
	case len(f.Content) > maxSkillContent:
		return fmt.Sprintf("it is at most %d KB", maxSkillContent>>10)
	}
	return ""
}

// The skill or command of a kind with the id, if the user sees it.
func visibleSkill(ctx context.Context, db querier, u *user, kind string, id int64) (skill, error) {
	cond, args := ownedBy("s", u)
	return scanSkill(db.QueryRowContext(ctx, "SELECT "+skillColumns+" FROM skills s WHERE s.id = ? AND s.kind = ? AND "+cond,
		append([]any{id, kind}, args...)...))
}

func registerSkillRoutes(mux *http.ServeMux, db *sql.DB) {
	for route, kind := range skillRoutes {
		registerSkillKind(mux, db, route, kind)
	}
}

func registerSkillKind(mux *http.ServeMux, db *sql.DB, route, kind string) {
	base := "/api/" + route
	notFound := func(w http.ResponseWriter) { http.Error(w, kind+" not found", http.StatusNotFound) }
	taken := func(w http.ResponseWriter, name string) {
		http.Error(w, fmt.Sprintf("there is a %s named %s already; name it apart", kind, name), http.StatusConflict)
	}
	get := func(w http.ResponseWriter, r *http.Request) (skill, bool) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", http.StatusBadRequest)
			return skill{}, false
		}
		s, err := visibleSkill(r.Context(), db, requestUser(r), kind, id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			notFound(w)
			return s, false
		case err != nil:
			serverError(w, err)
			return s, false
		}
		return s, true
	}

	// The ones whoever asks sees; with ?flow=, of those, the ones the flow
	// may use, which its board offers.
	mux.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		cond, args := ownedBy("s", requestUser(r))
		if v := r.URL.Query().Get("flow"); v != "" {
			flow, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				http.Error(w, "invalid flow", http.StatusBadRequest)
				return
			}
			fc, fargs, err := flowSkills(r.Context(), db, flow)
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "flow not found", http.StatusNotFound)
				return
			}
			if err != nil {
				serverError(w, err)
				return
			}
			cond, args = cond+" AND "+fc, append(args, fargs...)
		}
		out, err := listSkills(r.Context(), db, kind, cond, args...)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})

	// Makes one, its maker's unless they give it to one of their teams or
	// everyone: {"name", "description", "argumentHint", "content", "owner"}.
	mux.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		var f skillFields
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if msg := f.check(kind); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		u := requestUser(r)
		asked := f.Owner
		if asked == nil && u != nil {
			asked = &flowOwner{Kind: ownerUser, ID: u.ID}
		}
		userID, teamID, msg, err := newOwner(r.Context(), db, u, asked)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg != "" {
			http.Error(w, msg, http.StatusForbidden)
			return
		}
		res, err := db.ExecContext(r.Context(),
			"INSERT INTO skills (kind, name, description, argument_hint, content, owner_user_id, owner_team_id) VALUES (?, ?, ?, ?, ?, ?, ?)",
			kind, f.Name, f.Description, f.ArgumentHint, f.Content, userID, teamID)
		switch {
		case mysqlErrorIs(err, mysqlErrDuplicate):
			taken(w, f.Name)
			return
		case mysqlErrorIs(err, mysqlErrForeignKey):
			http.Error(w, "no such "+asked.Kind, http.StatusBadRequest)
			return
		case err != nil:
			serverError(w, err)
			return
		}
		id, err := res.LastInsertId()
		var s skill
		if err == nil {
			s, err = visibleSkill(r.Context(), db, nil, kind, id)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, s)
	})

	// Changes one whoever asks sees: its name, description, arguments and content.
	mux.HandleFunc("PUT "+base+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		s, ok := get(w, r)
		if !ok {
			return
		}
		var f skillFields
		if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if msg := f.check(kind); msg != "" {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		_, err := db.ExecContext(r.Context(), "UPDATE skills SET name = ?, description = ?, argument_hint = ?, content = ? WHERE id = ?",
			f.Name, f.Description, f.ArgumentHint, f.Content, s.ID)
		if mysqlErrorIs(err, mysqlErrDuplicate) {
			taken(w, f.Name)
			return
		}
		if err == nil {
			s, err = visibleSkill(r.Context(), db, nil, kind, s.ID)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	})

	// Deletes one whoever asks sees. The processes picking a skill fail to
	// run until they no longer do; prompts naming a command pass /name on as
	// written.
	mux.HandleFunc("DELETE "+base+"/{id}", func(w http.ResponseWriter, r *http.Request) {
		s, ok := get(w, r)
		if !ok {
			return
		}
		if _, err := db.ExecContext(r.Context(), "DELETE FROM skills WHERE id = ?", s.ID); err != nil {
			serverError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// Gives one to someone, a team, or everyone, as a flow is given.
	mux.HandleFunc("PUT "+base+"/{id}/owner", func(w http.ResponseWriter, r *http.Request) {
		s, ok := get(w, r)
		if !ok {
			return
		}
		var to flowOwner
		if err := json.NewDecoder(r.Body).Decode(&to); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		msg, err := refuseOwner(r.Context(), db, requestUser(r), s.Owner, to)
		if err != nil {
			serverError(w, err)
			return
		}
		if msg != "" {
			http.Error(w, msg, http.StatusForbidden)
			return
		}
		var userID, teamID *int64
		switch to.Kind {
		case ownerUser:
			userID = &to.ID
		case ownerTeam:
			teamID = &to.ID
		}
		_, err = db.ExecContext(r.Context(), "UPDATE skills SET owner_user_id = ?, owner_team_id = ? WHERE id = ?", userID, teamID, s.ID)
		if mysqlErrorIs(err, mysqlErrForeignKey) {
			http.Error(w, "no such "+to.Kind, http.StatusBadRequest)
			return
		}
		if err == nil {
			s, err = visibleSkill(r.Context(), db, nil, kind, s.ID)
		}
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	})
}
