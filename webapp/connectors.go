package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Connectors are services the webapp itself knows how to work with, such as
// GitHub, for what an MCP server doesn't offer, or offers only to read. Each
// is built into the webapp, as a set of operations, each of which reads or
// changes something on the service: list a repository's issues, comment on
// one, open a pull request. Nobody adds connectors but by changing the
// webapp; the directory in the settings lists them, with the MCP servers
// picked to go with them.
//
// An admin installs a connector as an MCP server taking a token (see
// mcp.go), set once for everyone: the operations call the service with it,
// at the server's URL, its API's, so a connector reaches a service's own
// installation too. Agents use a connector as they do any MCP server: the
// proxy answers their requests itself, its operations being the tools, of
// which an agent process has those that only read unless it picks others.
// A connector process carries out one operation as a step of a run, with
// arguments set in its panel in which {{Object}} and {{Object.attribute}}
// stand for what the step takes, and generates what the service answers:
// for an object with attributes, the answer's fields named as they are, a
// dotted name reaching into one, and for a list one item per result.

type connector struct {
	// As installed servers and setup files name it.
	Name     string
	Title    string
	Category string
	// What it does, which tells agents when to use it.
	Description string
	// Its service's API, where an installed connector calls it unless an
	// admin says otherwise.
	URL string
	// What token it takes, for whoever sets it.
	TokenHelp string
	// Headers every call to the service has.
	Headers    map[string]string
	Operations []operation
}

type operation struct {
	Name        string
	Title       string
	Description string
	// Whether it changes something, so an agent has it only once picked.
	Write  bool
	Params []param
	run    func(ctx context.Context, c connectorClient, a opArgs) (any, error)
}

// A parameter of an operation, and so a property of its tool's input.
type param struct {
	Name        string
	Description string
	Type        string
	Required    bool
	// The values a string may be, if only some.
	Enum []string
	// What a string must look like, such as a repository's name, since it
	// becomes part of a URL path.
	Pattern *regexp.Regexp
}

const (
	paramString  = "string"
	paramInteger = "integer"
	paramBoolean = "boolean"
	// A list of strings.
	paramStrings = "strings"
)

// The connectors built into the webapp.
var connectors = []*connector{githubConnector}

func findConnector(name string) *connector {
	if name == "" {
		return nil
	}
	for _, c := range connectors {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (c *connector) operation(name string) *operation {
	for i := range c.Operations {
		if c.Operations[i].Name == name {
			return &c.Operations[i]
		}
	}
	return nil
}

// An operation's arguments, checked against its parameters: strings,
// int64s, bools and string slices, by name.
type opArgs map[string]any

func (a opArgs) str(name string) string {
	s, _ := a[name].(string)
	return s
}

func (a opArgs) int(name string) int64 {
	n, _ := a[name].(int64)
	return n
}

func (a opArgs) bool(name string) bool {
	b, _ := a[name].(bool)
	return b
}

func (a opArgs) strings(name string) []string {
	s, _ := a[name].([]string)
	return s
}

func (a opArgs) has(name string) bool {
	_, ok := a[name]
	return ok
}

// The arguments of a call of the operation as a tool, in JSON.
func (op *operation) check(raw map[string]json.RawMessage) (opArgs, error) {
	a := opArgs{}
	for name, v := range raw {
		p := op.param(name)
		if p == nil {
			return nil, fmt.Errorf("%s takes no argument %s", op.Name, name)
		}
		if string(v) == "null" {
			continue
		}
		var err error
		switch p.Type {
		case paramString:
			var s string
			err = json.Unmarshal(v, &s)
			a[name] = s
		case paramInteger:
			var f float64
			if err = json.Unmarshal(v, &f); err == nil && f != float64(int64(f)) {
				err = errors.New("not a whole number")
			}
			a[name] = int64(f)
		case paramBoolean:
			var b bool
			err = json.Unmarshal(v, &b)
			a[name] = b
		case paramStrings:
			var ss []string
			err = json.Unmarshal(v, &ss)
			a[name] = ss
		}
		if err != nil {
			return nil, fmt.Errorf("%s must be %s", name, p.typeName())
		}
	}
	return a, op.valid(a)
}

// The arguments of a connector process, from the text its panel holds:
// a list as JSON or separated by commas.
func (op *operation) coerce(text map[string]string) (opArgs, error) {
	a := opArgs{}
	for name, v := range text {
		p := op.param(name)
		if p == nil {
			return nil, fmt.Errorf("%s takes no argument %s", op.Name, name)
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		switch p.Type {
		case paramString:
			a[name] = v
		case paramInteger:
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%s must be %s, not %s", name, p.typeName(), quote(v))
			}
			a[name] = n
		case paramBoolean:
			b, err := strconv.ParseBool(v)
			if err != nil {
				return nil, fmt.Errorf("%s must be %s, not %s", name, p.typeName(), quote(v))
			}
			a[name] = b
		case paramStrings:
			var ss []string
			if strings.HasPrefix(v, "[") {
				if err := json.Unmarshal([]byte(v), &ss); err != nil {
					return nil, fmt.Errorf("%s must be %s", name, p.typeName())
				}
			} else {
				for _, s := range strings.Split(v, ",") {
					if s = strings.TrimSpace(s); s != "" {
						ss = append(ss, s)
					}
				}
			}
			a[name] = ss
		}
	}
	return a, op.valid(a)
}

// Says what is wrong with checked arguments: one missing, or a string that
// isn't one of its values or doesn't look as it must.
func (op *operation) valid(a opArgs) error {
	for _, p := range op.Params {
		v, ok := a[p.Name]
		if !ok {
			if p.Required {
				return fmt.Errorf("%s needs %s", op.Name, p.Name)
			}
			continue
		}
		s, isString := v.(string)
		switch {
		case isString && len(p.Enum) > 0 && !slices.Contains(p.Enum, s):
			return fmt.Errorf("%s must be one of %s", p.Name, strings.Join(p.Enum, ", "))
		case isString && p.Pattern != nil && !p.Pattern.MatchString(s):
			return fmt.Errorf("%s isn't a valid %s: %s", quote(s), p.Name, p.Description)
		case isString && len(s) > maxConnectorArg:
			return fmt.Errorf("%s may be up to %d characters", p.Name, maxConnectorArg)
		}
	}
	return nil
}

func (op *operation) param(name string) *param {
	for i := range op.Params {
		if op.Params[i].Name == name {
			return &op.Params[i]
		}
	}
	return nil
}

func (p param) typeName() string {
	switch p.Type {
	case paramInteger:
		return "a whole number"
	case paramBoolean:
		return "true or false"
	case paramStrings:
		return "a list of strings"
	}
	return "a string"
}

// The operation's arguments as the JSON schema of its tool's input.
func (op *operation) inputSchema() map[string]any {
	props := map[string]any{}
	required := []string{}
	for _, p := range op.Params {
		prop := map[string]any{"description": p.Description}
		switch p.Type {
		case paramStrings:
			prop["type"] = "array"
			prop["items"] = map[string]any{"type": "string"}
		default:
			prop["type"] = p.Type
		}
		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}
		props[p.Name] = prop
		if p.Required {
			required = append(required, p.Name)
		}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

// The longest argument, a comment's or an issue's body, and the most
// arguments a connector process sets.
const (
	maxConnectorArg  = 65536
	maxConnectorArgs = 32
)

// Calls a connector's service, at its installed server's URL, with its token.
type connectorClient struct {
	url, token string
	headers    map[string]string
}

// How long a call to a service may take; the proxy has no limit of its own.
const connectorTimeout = time.Minute

// Sends a request to the service's API at the path, the body as JSON if
// there is one, and decodes what it answers into out.
func (c connectorClient) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, connectorTimeout)
	defer cancel()
	u := strings.TrimSuffix(c.url, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxRunRequestBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
		}
		msg := strings.TrimSpace(string(answer))
		if json.Unmarshal(answer, &e) == nil && e.Message != "" {
			msg = e.Message
		}
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		return fmt.Errorf("%s %s answered %s: %s", method, path, resp.Status, msg)
	}
	if out == nil || len(answer) == 0 {
		return nil
	}
	if err := json.Unmarshal(answer, out); err != nil {
		return fmt.Errorf("%s %s answered what isn't JSON: %w", method, path, err)
	}
	return nil
}

func (c *connector) client(serverURL, token string) connectorClient {
	return connectorClient{url: serverURL, token: token, headers: c.Headers}
}

// The MCP versions a connector speaks, the latest it knows first, and
// answers in the one the agent asks for if it is one of them.
var connectorProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// Answers an agent's MCP request to a connector, over streamable HTTP: as
// JSON, with no session and no stream of its own.
func serveConnector(w http.ResponseWriter, r *http.Request, c *connector, client connectorClient, filter toolFilter, body []byte) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "a connector has no stream of events", http.StatusMethodNotAllowed)
		return
	case http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
		return
	}
	messages, batch := parseRPC(body)
	if messages == nil {
		writeJSON(w, http.StatusBadRequest, rpcFailure(json.RawMessage("null"), -32700, "the request isn't JSON-RPC"))
		return
	}
	var out []rpcMessage
	for _, m := range messages {
		// Notifications and responses get no answer.
		if len(m.ID) == 0 || m.Method == "" {
			continue
		}
		out = append(out, c.answer(r.Context(), client, filter, m))
	}
	switch {
	case len(out) == 0:
		w.WriteHeader(http.StatusAccepted)
	case batch:
		writeJSON(w, http.StatusOK, out)
	default:
		writeJSON(w, http.StatusOK, out[0])
	}
}

func (c *connector) answer(ctx context.Context, client connectorClient, filter toolFilter, m rpcMessage) rpcMessage {
	result := func(v any) rpcMessage {
		raw, _ := json.Marshal(v)
		return rpcMessage{JSONRPC: "2.0", ID: m.ID, Result: raw}
	}
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(m.Params, &p)
		version := connectorProtocolVersions[1]
		if slices.Contains(connectorProtocolVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return result(map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": c.Name, "title": c.Title, "version": "1"},
			"instructions":    c.Description,
		})
	case "ping":
		return result(map[string]any{})
	case "tools/list":
		tools := []map[string]any{}
		for i := range c.Operations {
			op := &c.Operations[i]
			if !filter.allows(op.Name) {
				continue
			}
			tools = append(tools, map[string]any{
				"name":        op.Name,
				"title":       op.Title,
				"description": op.Description,
				"inputSchema": op.inputSchema(),
				"annotations": map[string]any{"title": op.Title, "readOnlyHint": !op.Write, "openWorldHint": true},
			})
		}
		return result(map[string]any{"tools": tools})
	case "tools/call":
		var p struct {
			Name      string                     `json:"name"`
			Arguments map[string]json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return rpcFailure(m.ID, rpcInvalidParams, "the call names no tool")
		}
		op := c.operation(p.Name)
		if op == nil {
			return rpcFailure(m.ID, rpcInvalidParams, fmt.Sprintf("%s has no tool %s", c.Name, p.Name))
		}
		if !filter.allows(op.Name) {
			return rpcFailure(m.ID, rpcInvalidParams, fmt.Sprintf("the tool %s isn't one this process uses; pick it in the process's MCP servers to use it", op.Name))
		}
		failed := func(err error) rpcMessage {
			return result(map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true})
		}
		a, err := op.check(p.Arguments)
		if err != nil {
			return failed(err)
		}
		v, err := op.run(ctx, client, a)
		if err != nil {
			return failed(err)
		}
		text, _ := json.MarshalIndent(v, "", "  ")
		return result(map[string]any{"content": []map[string]any{{"type": "text", "text": string(text)}}})
	}
	return rpcFailure(m.ID, rpcMethodNotFound, fmt.Sprintf("a connector doesn't answer %s", m.Method))
}

// What a connector process carries out.
type connectorSettings struct {
	// The connector's installed server, by id.
	Server    int64  `json:"server"`
	Operation string `json:"operation"`
	// Its arguments, by name, as text in which {{Object}} and
	// {{Object.attribute}} stand for what the step takes.
	Args map[string]string `json:"args,omitempty"`
}

// Why the settings can't be saved, or "".
func (s connectorSettings) problem() string {
	if len(s.Operation) > 64 {
		return "the operation's name is too long"
	}
	if len(s.Args) > maxConnectorArgs {
		return fmt.Sprintf("a connector process sets up to %d arguments", maxConnectorArgs)
	}
	for name, v := range s.Args {
		if name == "" || len(name) > 64 || len(v) > maxConnectorArg {
			return fmt.Sprintf("an argument's name is up to 64 characters, and its value up to %d", maxConnectorArg)
		}
	}
	return ""
}

// {{Object}} or {{Object.attribute}}, a dotted attribute reaching into one.
var placeholder = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

// The arguments with what the step takes put in for their placeholders.
func fillArgs(args map[string]string, inputs []runInput) (map[string]string, error) {
	filled := map[string]string{}
	for name, v := range args {
		var problem error
		filled[name] = placeholder.ReplaceAllStringFunc(v, func(m string) string {
			ref := placeholder.FindStringSubmatch(m)[1]
			s, err := inputValue(ref, inputs)
			if err != nil && problem == nil {
				problem = fmt.Errorf("%s: %w", name, err)
			}
			return s
		})
		if problem != nil {
			return nil, problem
		}
	}
	return filled, nil
}

// What a placeholder stands for: the object it names, or its attribute, as
// text: a string as it is, anything else as JSON.
func inputValue(ref string, inputs []runInput) (string, error) {
	var in *runInput
	var path string
	// The longest name that matches, since an object's name may hold dots.
	for i := range inputs {
		name := inputs[i].Name
		if (ref == name || strings.HasPrefix(ref, name+".")) && (in == nil || len(name) > len(in.Name)) {
			in = &inputs[i]
			path = strings.TrimPrefix(strings.TrimPrefix(ref, name), ".")
		}
	}
	if in == nil {
		return "", fmt.Errorf("the step takes no object %s", quote(strings.SplitN(ref, ".", 2)[0]))
	}
	raw := json.RawMessage(in.Content)
	if !json.Valid(raw) {
		if path != "" {
			return "", fmt.Errorf("%s has no attributes", quote(in.Name))
		}
		return in.Content, nil
	}
	if strings.HasPrefix(strings.TrimSpace(in.Content), "[") {
		return "", fmt.Errorf("%s is a list; a connector process takes one item of it at a time", quote(in.Name))
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil {
		if path == "" {
			if data, ok := fields[dataAttribute]; ok && len(fields) == 1 {
				raw = data
			}
		} else {
			v, ok := jsonField(fields, path)
			if !ok {
				return "", fmt.Errorf("%s has no attribute %s", quote(in.Name), quote(path))
			}
			raw = v
		}
	} else if path != "" {
		return "", fmt.Errorf("%s has no attributes", quote(in.Name))
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	return string(raw), nil
}

// A field of a JSON object by a possibly dotted name.
func jsonField(fields map[string]json.RawMessage, name string) (json.RawMessage, bool) {
	if v, ok := fields[name]; ok {
		return v, true
	}
	head, rest, nested := strings.Cut(name, ".")
	raw, ok := fields[head]
	if !ok || !nested {
		return nil, false
	}
	var inner map[string]json.RawMessage
	if json.Unmarshal(raw, &inner) != nil || inner == nil {
		return nil, false
	}
	return jsonField(inner, rest)
}

// What an operation answered as the object a connector process generates:
// for one with attributes the answer's fields they are named after, and
// for a list one item per result. A field no result has is most likely
// misnamed, so it is an error rather than left empty.
func connectorObject(o shapedObject, answer json.RawMessage) (string, error) {
	var items []json.RawMessage
	if o.many {
		if json.Unmarshal(answer, &items) != nil {
			items = []json.RawMessage{answer}
		}
	} else {
		items = []json.RawMessage{answer}
	}
	seen := map[string]bool{}
	var known []string
	parts := make([]string, len(items))
	for i, item := range items {
		if o.data {
			parts[i] = ordered([]string{dataAttribute}, []json.RawMessage{item})
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(item, &fields) != nil {
			return "", fmt.Errorf("the answer isn't an object with fields to fill %s's attributes from", quote(o.name))
		}
		if known == nil {
			for k := range fields {
				known = append(known, k)
			}
			slices.Sort(known)
		}
		keys := make([]string, len(o.fields))
		values := make([]json.RawMessage, len(o.fields))
		for j, f := range o.fields {
			keys[j] = f.name
			v, ok := jsonField(fields, f.name)
			if !ok {
				v = json.RawMessage("null")
			} else {
				seen[f.name] = true
			}
			values[j] = v
		}
		parts[i] = ordered(keys, values)
	}
	if !o.data && len(items) > 0 {
		for _, f := range o.fields {
			if !seen[f.name] {
				return "", fmt.Errorf("the answer has no field %s for %s's attribute; name attributes after fields such as %s", quote(f.name), quote(o.name), strings.Join(known, ", "))
			}
		}
	}
	if !o.many {
		return parts[0], nil
	}
	return "[" + strings.Join(parts, ",") + "]", nil
}

// The operation a connector process carries out, with its server, or why
// it can't be carried out.
func connectorOperation(ctx context.Context, db *sql.DB, s *connectorSettings) (*connector, *operation, mcpServer, string, error) {
	var server mcpServer
	if s == nil || s.Server == 0 {
		return nil, nil, server, "the process uses no connector; pick one in its panel", nil
	}
	err := scanMCPServer(db.QueryRowContext(ctx, "SELECT "+mcpServerColumns+" FROM mcp_servers WHERE id = ?", 0, s.Server), &server)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, server, "the connector the process uses was removed; pick one again", nil
	}
	if err != nil {
		return nil, nil, server, "", err
	}
	c := findConnector(server.Connector)
	if c == nil {
		return nil, nil, server, fmt.Sprintf("%s isn't a connector but an MCP server, which an agent process uses", server.Name), nil
	}
	op := c.operation(s.Operation)
	if op == nil {
		return c, nil, server, fmt.Sprintf("pick what the process does with %s", c.Title), nil
	}
	return c, op, server, "", nil
}

// Executes a connector process as a step of the run, which records the
// operation and its arguments as the step's prompt. started gets the
// step's id once it is recorded.
func runConnectorStep(ctx context.Context, db *sql.DB, runID, id int64, inputs []runInput, started func(int64)) (runResult, error) {
	var res runResult
	var settings processSettings
	err := db.QueryRowContext(ctx, "SELECT settings FROM processes WHERE id = ?", id).Scan(&settings)
	if errors.Is(err, sql.ErrNoRows) {
		return res, errNotFound
	}
	if err != nil {
		return res, err
	}
	c, op, server, msg, err := connectorOperation(ctx, db, settings.Connector)
	if err != nil {
		return res, err
	}
	res.Error = msg
	contract, msg, err := contractFor(ctx, db, id, true)
	if err != nil {
		return res, err
	}
	switch {
	case res.Error != "":
	case msg != "":
		res.Error = msg
	case contract != nil && len(contract.shaped) != 1:
		res.Error = "a connector process generates one object, which holds what the operation answers"
	}
	prompt := ""
	var a opArgs
	if res.Error == "" {
		var filled map[string]string
		if filled, err = fillArgs(settings.Connector.Args, inputs); err == nil {
			a, err = op.coerce(filled)
		}
		args, _ := json.MarshalIndent(filled, "", "  ")
		prompt = fmt.Sprintf("%s %s\n%s", server.Name, op.Name, args)
		if err != nil {
			res.Error = err.Error()
		}
	}
	var token string
	if res.Error == "" {
		token, err = adminToken(ctx, db, server.ID, "")
		if errors.Is(err, errMCPNotConnected) {
			res.Error = fmt.Sprintf("The connector %s has no token. An admin sets it in Settings, under MCP servers, then run again.", server.Name)
		} else if err != nil {
			return res, err
		}
	}
	stepID, err := startStep(ctx, db, runID, id, stepActive, prompt, inputs, nil)
	if err != nil {
		return res, err
	}
	started(stepID)
	if res.Error == "" {
		v, err := op.run(ctx, c.client(server.URL, token), a)
		switch {
		case ctx.Err() != nil:
			res.Error = stopReason(ctx)
		case err != nil:
			res.Error = err.Error()
		default:
			answer, _ := json.Marshal(v)
			res.Output, res.OK = indentJSON(string(answer)), true
			if contract != nil {
				out, err := connectorObject(contract.shaped[0], answer)
				if err == nil {
					contract.filesDir = stepFilesDir(runID, stepID)
					out, res.Objects, err = contract.parse(out)
				}
				if err != nil {
					res = runResult{Output: res.Output, Error: "What " + c.Title + " answered doesn't fit what the process generates: " + err.Error() + "."}
				} else {
					res.Output = out
				}
			}
		}
	}
	status := stepError
	if res.OK {
		status = stepDone
	}
	// The run may have been stopped, but the step still has to end.
	res.Duplicate, err = finishStep(context.Background(), db, stepID, status, res.Output, res.Error, res.Objects)
	return res, err
}

// An entry of the directory: a built-in connector, or an MCP server picked
// to go with them, which an admin installs as an MCP server.
type directoryEntry struct {
	// What the server is named once installed.
	Name        string `json:"name"`
	Title       string `json:"title"`
	Category    string `json:"category"`
	Description string `json:"description"`
	// Whether it is built in, a connector, rather than an MCP server.
	Connector bool   `json:"connector"`
	URL       string `json:"url"`
	Auth      string `json:"auth"`
	TokenHelp string `json:"tokenHelp,omitempty"`
	// Where the service documents it.
	Docs       string            `json:"docs,omitempty"`
	Operations []directoryAction `json:"operations,omitempty"`
	// The installed server it is, if any.
	Installed *int64 `json:"installed"`
}

// An operation of a connector, as a process's panel sets it up.
type directoryAction struct {
	Name        string           `json:"name"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	Write       bool             `json:"write"`
	Params      []directoryParam `json:"params"`
}

type directoryParam struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// The remote MCP servers the directory offers besides the connectors: ones
// whose sign-in works for any client, as the webapp signs in.
var directoryServers = []directoryEntry{
	{Name: "atlassian", Title: "Jira and Confluence", Category: "Ticketing", URL: "https://mcp.atlassian.com/v2/mcp",
		Description: "Atlassian's Jira issues and Confluence pages: search, read, create and update them.",
		Docs:        "https://support.atlassian.com/atlassian-rovo-mcp-server/docs/getting-started-with-the-atlassian-remote-mcp-server/"},
	{Name: "linear", Title: "Linear", Category: "Ticketing", URL: "https://mcp.linear.app/mcp",
		Description: "Linear's issues, projects and comments: find, create and update them.",
		Docs:        "https://linear.app/docs/mcp"},
	{Name: "intercom", Title: "Intercom", Category: "Support", URL: "https://mcp.intercom.com/mcp",
		Description: "Intercom's conversations, contacts and help center articles.",
		Docs:        "https://developers.intercom.com/docs/guides/mcp"},
	{Name: "sentry", Title: "Sentry", Category: "Monitoring", URL: "https://mcp.sentry.dev/mcp",
		Description: "Sentry's issues, events and releases: search and triage errors.",
		Docs:        "https://mcp.sentry.dev"},
	{Name: "grafana", Title: "Grafana Cloud", Category: "Monitoring", URL: "https://mcp.grafana.com/mcp",
		Description: "Grafana Cloud's dashboards, metrics, logs and alerts.",
		Docs:        "https://grafana.com/docs/grafana-cloud/ai-tools/mcp-servers/cloud-mcp/"},
	{Name: "notion", Title: "Notion", Category: "Docs", URL: "https://mcp.notion.com/mcp",
		Description: "Notion's pages and databases: search, read and write them.",
		Docs:        "https://developers.notion.com/docs/get-started-with-mcp"},
}

// The directory, with what of it is installed: a connector by its name, an
// MCP server by its URL.
func directory(ctx context.Context, db *sql.DB) ([]directoryEntry, error) {
	byConnector, byURL := map[string]int64{}, map[string]int64{}
	err := scanAll(ctx, db, "SELECT id, url, connector FROM mcp_servers", func(rows *sql.Rows) error {
		var id int64
		var u, c string
		err := rows.Scan(&id, &u, &c)
		if c != "" {
			byConnector[c] = id
		} else {
			byURL[strings.TrimSuffix(u, "/")] = id
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	entries := []directoryEntry{}
	for _, c := range connectors {
		e := directoryEntry{Name: c.Name, Title: c.Title, Category: c.Category, Description: c.Description,
			Connector: true, URL: c.URL, Auth: authToken, TokenHelp: c.TokenHelp, Operations: []directoryAction{}}
		for _, op := range c.Operations {
			a := directoryAction{Name: op.Name, Title: op.Title, Description: op.Description, Write: op.Write, Params: []directoryParam{}}
			for _, p := range op.Params {
				a.Params = append(a.Params, directoryParam{Name: p.Name, Description: p.Description, Type: p.Type, Required: p.Required, Enum: p.Enum})
			}
			e.Operations = append(e.Operations, a)
		}
		if id, ok := byConnector[c.Name]; ok {
			e.Installed = &id
		}
		entries = append(entries, e)
	}
	for _, e := range directoryServers {
		e.Auth = authOAuth
		if id, ok := byURL[strings.TrimSuffix(e.URL, "/")]; ok {
			e.Installed = &id
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func registerConnectorRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /api/connectors", func(w http.ResponseWriter, r *http.Request) {
		entries, err := directory(r.Context(), db)
		if err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, entries)
	})
}
