package main

// Headers an MCP server takes beside, or instead of, a bearer token, such as
// an API key and an application key each in a header of the server's own:
// its project's owners set them, each a name and a value, and the proxy puts
// them on every request it passes on to the server (mcp.go), as the webapp
// does asking the server for its tools (mcp_tools.go). They are everyone's
// in the project, whoever a run is for, as a token an owner sets is. A
// server taking nothing but them (authHeaders) is reached with no bearer
// token at all, and is ready once one is set; there a header may be
// Authorization itself, for a server wanting a scheme other than Bearer.
//
// Values are sealed with SECRET_KEY (secrets.go) and never sent back: the
// settings list only the names. Changing a server's URL drops them, as it
// signs everyone out, so a key set for one host never goes to another
// unless an owner sets it again. A connector takes none, as the webapp calls
// its service itself, and setup files never hold them, as they hold no
// tokens.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// The most headers a server takes.
const maxMCPHeaders = 20

// What a header's name may be: letters, digits, -, _ and ., which every
// name in use keeps to, though HTTP takes a few more.
var mcpHeaderNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// Headers the proxy sets itself or passes on from the agent, or that say
// how the request travels or where it came from, which no server's headers
// may stand in for; by canonical name, _ read as -. Authorization is the
// proxy's but for a server taking only headers.
var mcpHeldHeaders = map[string]bool{
	"Host": true, "Content-Length": true, "Content-Type": true, "Content-Encoding": true,
	"Accept": true, "Accept-Encoding": true, "Transfer-Encoding": true, "Te": true, "Trailer": true,
	"Connection": true, "Keep-Alive": true, "Upgrade": true, "Expect": true, "Cookie": true,
	"Forwarded": true, "Via": true, "Mcp-Session-Id": true, "Mcp-Protocol-Version": true, "Last-Event-Id": true,
}

var mcpHeldPrefixes = []string{"Proxy-", "X-Forwarded-", "Sec-"}

// Why the header can't be set on a server connected as auth says, or "".
func mcpHeaderProblem(name, value, auth string) string {
	if !mcpHeaderNamePattern.MatchString(name) {
		return fmt.Sprintf("%s isn't a header's name: letters, digits, -, _ and . only, up to 128", quote(name))
	}
	key := http.CanonicalHeaderKey(strings.ReplaceAll(name, "_", "-"))
	held := mcpHeldHeaders[key] || slices.ContainsFunc(mcpHeldPrefixes, func(p string) bool { return strings.HasPrefix(key, p) })
	if held {
		return fmt.Sprintf("the proxy sets %s itself", name)
	}
	if key == "Authorization" && auth != authHeaders {
		return fmt.Sprintf("the proxy sets %s itself, with the server's token; a server connected by nothing but its headers may take one of its own", name)
	}
	if value == "" || len(value) > maxModelKey {
		return fmt.Sprintf("the value of %s must be from 1 to %d characters", name, maxModelKey)
	}
	if strings.ContainsFunc(value, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
		return fmt.Sprintf("the value of %s can't hold a line break or another control character", name)
	}
	return ""
}

// Where a server's header's value is kept, whatever the case of its name.
func mcpHeaderPlace(server int64, name string) string {
	return fmt.Sprintf("mcp_server_headers/%d/%s", server, strings.ToLower(name))
}

// The names of the headers set on each server of the project, in order.
func mcpHeaderNames(ctx context.Context, db rowsQuerier, project int64) (map[int64][]string, error) {
	names := map[int64][]string{}
	err := scanAll(ctx, db, "SELECT server_id, name FROM mcp_server_headers WHERE project_id = ? ORDER BY server_id, name", func(rows *sql.Rows) error {
		var server int64
		var name string
		err := rows.Scan(&server, &name)
		names[server] = append(names[server], name)
		return err
	}, project)
	return names, err
}

// Gives each server its headers' names, none as an empty list.
func withHeaderNames(servers []mcpServer, names map[int64][]string) {
	for i := range servers {
		servers[i].Headers = names[servers[i].ID]
		if servers[i].Headers == nil {
			servers[i].Headers = []string{}
		}
	}
}

// The headers set on a server, opened, by name as the owner spelled it.
func mcpHeaders(ctx context.Context, db rowsQuerier, server int64) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT name, value FROM mcp_server_headers WHERE server_id = ?", server)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	headers := map[string]string{}
	for rows.Next() {
		var name, stored string
		if err := rows.Scan(&name, &stored); err != nil {
			return nil, err
		}
		if headers[name], err = secrets.open(mcpHeaderPlace(server, name), stored); err != nil {
			return nil, fmt.Errorf("the header %s: %w", name, err)
		}
	}
	return headers, rows.Err()
}

// Puts a server's headers on a request to it, each as the owner spelled it.
func putMCPHeaders(h http.Header, headers map[string]string) {
	for name, value := range headers {
		for k := range h {
			if strings.EqualFold(k, name) {
				delete(h, k)
			}
		}
		h[name] = []string{value}
	}
}

// Why a change to a server's headers is refused, said as it is.
type mcpHeadersRefused string

func (e mcpHeadersRefused) Error() string { return string(e) }

func registerMCPHeaderRoutes(mux *http.ServeMux, db *sql.DB) {
	// Sets and removes a server's headers: each name given a value is set,
	// one given null removed, and those not named are left as they are.
	// Values are never sent back.
	mux.HandleFunc("PUT /api/mcp-servers/{id}/headers", func(w http.ResponseWriter, r *http.Request) {
		if refuseMCPConfig(w, r) {
			return
		}
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		project := requestProject(r)
		var body struct {
			Headers map[string]*string `json:"headers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if len(body.Headers) == 0 {
			http.Error(w, "name the headers to set or remove", http.StatusBadRequest)
			return
		}
		// One name in two cases would be the same header twice.
		seen := map[string]string{}
		for name := range body.Headers {
			if other, ok := seen[strings.ToLower(name)]; ok {
				http.Error(w, fmt.Sprintf("%s and %s are the same header", other, name), http.StatusBadRequest)
				return
			}
			seen[strings.ToLower(name)] = name
		}
		names := make([]string, 0, len(body.Headers))
		for name := range body.Headers {
			names = append(names, name)
		}
		slices.Sort(names)
		var server string
		var set, removed []string
		err := inTx(r.Context(), db, func(tx *sql.Tx) error {
			var auth, connector string
			err := tx.QueryRowContext(r.Context(), "SELECT name, auth, connector FROM mcp_servers WHERE id = ? AND project_id = ? FOR UPDATE", id, project).
				Scan(&server, &auth, &connector)
			if errors.Is(err, sql.ErrNoRows) {
				return errNotFound
			}
			if err != nil {
				return err
			}
			if connector != "" {
				return mcpHeadersRefused("a connector takes no headers, as the webapp calls its service itself")
			}
			for _, name := range names {
				value := body.Headers[name]
				if value == nil {
					res, err := tx.ExecContext(r.Context(), "DELETE FROM mcp_server_headers WHERE server_id = ? AND name = ?", id, name)
					if err != nil {
						return err
					}
					if n, _ := res.RowsAffected(); n > 0 {
						removed = append(removed, name)
					}
					continue
				}
				v := strings.TrimSpace(*value)
				if msg := mcpHeaderProblem(name, v, auth); msg != "" {
					return mcpHeadersRefused(msg)
				}
				sealedValue, err := secrets.seal(mcpHeaderPlace(id, name), v)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(r.Context(), `INSERT INTO mcp_server_headers (project_id, server_id, name, value) VALUES (?, ?, ?, ?)
					ON DUPLICATE KEY UPDATE name = VALUES(name), value = VALUES(value)`, project, id, name, sealedValue); err != nil {
					return err
				}
				set = append(set, name)
			}
			var count int
			if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM mcp_server_headers WHERE server_id = ?", id).Scan(&count); err != nil {
				return err
			}
			if count > maxMCPHeaders {
				return mcpHeadersRefused(fmt.Sprintf("a server takes up to %d headers", maxMCPHeaders))
			}
			return nil
		})
		var refused mcpHeadersRefused
		switch {
		case errors.Is(err, errNotFound):
			http.Error(w, "server not found", http.StatusNotFound)
			return
		case errors.As(err, &refused):
			http.Error(w, refused.Error(), http.StatusBadRequest)
			return
		case err != nil:
			serverError(w, err)
			return
		}
		if len(set) > 0 {
			auditChange(r, "mcp.headers.set", "the headers %s of the MCP server %s", strings.Join(set, ", "), server)
		}
		if len(removed) > 0 {
			auditChange(r, "mcp.headers.remove", "the headers %s of the MCP server %s", strings.Join(removed, ", "), server)
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
