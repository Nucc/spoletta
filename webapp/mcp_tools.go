package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// The tools of an MCP server an agent process keeps to. It picks them in its
// panel, from the tools the server lists, and the proxy holds each step to
// them: the server's list of tools leaves out the others, and a call of one
// is refused before it reaches the server. A process that picks none of a
// server's tools has all of them, as before processes could pick, except of
// a connector, whose operations that change something it has only once it
// picks them.

// The tools a step has of a server, by name; nil is all of them.
type toolFilter map[string]bool

// The filter for a server's tools as a process picked them: those, or
// without picking, all of an MCP server's and a connector's reading ones.
func filterFor(s mcpServer, picked []string) toolFilter {
	if picked == nil {
		c := findConnector(s.Connector)
		if c == nil {
			return nil
		}
		f := toolFilter{}
		for _, op := range c.Operations {
			if !op.Write {
				f[op.Name] = true
			}
		}
		return f
	}
	f := toolFilter{}
	for _, t := range picked {
		f[t] = true
	}
	return f
}

func (f toolFilter) allows(tool string) bool {
	return f == nil || f[tool]
}

// A JSON-RPC message, as MCP sends them.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC's codes for a method there is none of and for parameters it
// doesn't take, such as a tool it doesn't have.
const (
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
)

func rpcFailure(id json.RawMessage, code int, msg string) rpcMessage {
	return rpcMessage{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// The messages of a request's body, one or a batch of them; whether it was
// a batch.
func parseRPC(body []byte) ([]rpcMessage, bool) {
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		var batch []rpcMessage
		if json.Unmarshal(body, &batch) == nil {
			return batch, true
		}
		return nil, true
	}
	var m rpcMessage
	if json.Unmarshal(body, &m) != nil {
		return nil, false
	}
	return []rpcMessage{m}, false
}

// The tool a tools/call message calls.
func calledTool(m rpcMessage) string {
	var p struct {
		Name string `json:"name"`
	}
	json.Unmarshal(m.Params, &p)
	return p.Name
}

// What to answer, instead of passing the request on, when it calls a tool
// the step doesn't have: an error for each of its requests, since a batch
// is sent whole or not at all. nil lets it through.
func (f toolFilter) refuse(messages []rpcMessage) any {
	if f == nil {
		return nil
	}
	refused := ""
	for _, m := range messages {
		if m.Method == "tools/call" && !f.allows(calledTool(m)) {
			refused = calledTool(m)
			break
		}
	}
	if refused == "" {
		return nil
	}
	var out []rpcMessage
	for _, m := range messages {
		if len(m.ID) == 0 {
			continue
		}
		msg := fmt.Sprintf("the tool %s isn't one this process uses; pick it in the process's MCP servers to use it", refused)
		if m.Method != "tools/call" || calledTool(m) != refused {
			msg = fmt.Sprintf("not sent, since the batch calls %s, a tool this process doesn't use", refused)
		}
		out = append(out, rpcFailure(m.ID, rpcInvalidParams, msg))
	}
	if len(messages) == 1 && len(out) == 1 {
		return out[0]
	}
	return out
}

// Whether the request asks for the server's tools.
func listsTools(messages []rpcMessage) bool {
	return slices.ContainsFunc(messages, func(m rpcMessage) bool { return m.Method == "tools/list" })
}

// A response message with the tools the step doesn't have left out of a
// list of tools, or as it is.
func (f toolFilter) filterMessage(raw []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m["result"] == nil {
		return raw
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(m["result"], &result) != nil || result["tools"] == nil {
		return raw
	}
	var tools []json.RawMessage
	if json.Unmarshal(result["tools"], &tools) != nil {
		return raw
	}
	kept := []json.RawMessage{}
	for _, t := range tools {
		var tool struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(t, &tool) == nil && f.allows(tool.Name) {
			kept = append(kept, t)
		}
	}
	result["tools"], _ = json.Marshal(kept)
	m["result"], _ = json.Marshal(result)
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// Passes on a server's response to a request listing tools, with the tools
// the step doesn't have left out: a JSON body whole, or a stream of events
// an event at a time.
func (f toolFilter) filterResponse(w http.ResponseWriter, resp *http.Response) {
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxRunRequestBytes))
		if err != nil {
			return
		}
		if bytes.HasPrefix(bytes.TrimSpace(body), []byte("[")) {
			var batch []json.RawMessage
			if json.Unmarshal(body, &batch) == nil {
				for i, m := range batch {
					batch[i] = f.filterMessage(m)
				}
				body, _ = json.Marshal(batch)
			}
		} else {
			body = f.filterMessage(body)
		}
		w.Write(body)
		return
	}
	flusher, _ := w.(http.Flusher)
	r := bufio.NewReader(resp.Body)
	var event []string
	for {
		line, err := r.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed != "" {
			event = append(event, trimmed)
		}
		if len(event) > 0 && ((trimmed == "" && line != "") || err != nil) {
			if _, werr := io.WriteString(w, f.filterEvent(event)+"\n"); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
			event = nil
		}
		if err != nil {
			return
		}
	}
}

// An event of a stream with its data filtered, as lines ending in a blank one.
func (f toolFilter) filterEvent(lines []string) string {
	var data []string
	var out []string
	for _, l := range lines {
		if d, ok := strings.CutPrefix(l, "data:"); ok {
			data = append(data, strings.TrimPrefix(d, " "))
		} else {
			out = append(out, l)
		}
	}
	if len(data) > 0 {
		out = append(out, "data: "+string(f.filterMessage([]byte(strings.Join(data, "\n")))))
	}
	return strings.Join(out, "\n") + "\n"
}

// A tool of a server, as a process picks it.
type mcpTool struct {
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// Whether it says it changes nothing, as an operation of a connector
	// that only reads does.
	ReadOnly bool `json:"readOnly"`
}

// The tools of a server, as whoever asks has it connected: a connector's
// operations, or what an MCP server lists, asked as an agent would.
func (b *mcpBroker) tools(ctx context.Context, id, user int64) ([]mcpTool, error) {
	var serverURL, connector string
	err := b.db.QueryRowContext(ctx, "SELECT url, connector FROM mcp_servers WHERE id = ?", id).Scan(&serverURL, &connector)
	if err != nil {
		return nil, errNotFound
	}
	if c := findConnector(connector); c != nil {
		tools := []mcpTool{}
		for _, op := range c.Operations {
			tools = append(tools, mcpTool{Name: op.Name, Title: op.Title, Description: op.Description, ReadOnly: !op.Write})
		}
		return tools, nil
	}
	access, err := b.accessToken(ctx, id, user, "")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := &mcpClient{url: serverURL, token: access}
	if _, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "spoletta", "version": "1"},
	}); err != nil {
		return nil, err
	}
	if err := c.notify(ctx, "notifications/initialized"); err != nil {
		return nil, err
	}
	defer c.close()
	tools := []mcpTool{}
	cursor := ""
	for page := 0; page < 20; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Tools []struct {
				Name        string `json:"name"`
				Title       string `json:"title"`
				Description string `json:"description"`
				Annotations struct {
					ReadOnlyHint bool `json:"readOnlyHint"`
				} `json:"annotations"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("the server's tools aren't a list of tools: %w", err)
		}
		for _, t := range res.Tools {
			tools = append(tools, mcpTool{Name: t.Name, Title: t.Title, Description: t.Description, ReadOnly: t.Annotations.ReadOnlyHint})
		}
		if cursor = res.NextCursor; cursor == "" {
			break
		}
	}
	return tools, nil
}

// The version of MCP the webapp speaks when it asks a server for its tools.
const mcpProtocolVersion = "2025-06-18"

// Talks to an MCP server over streamable HTTP, for the webapp's own requests.
type mcpClient struct {
	url, token, session string
	next                int
}

func (c *mcpClient) post(ctx context.Context, m rpcMessage) (*http.Response, error) {
	body, _ := json.Marshal(m)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Mcp-Protocol-Version", mcpProtocolVersion)
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		c.session = s
	}
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, errMCPNotConnected
		}
		return nil, fmt.Errorf("the server answered %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return resp, nil
}

// Sends a request and returns its result, from a JSON body or the stream of
// events the server answers with.
func (c *mcpClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.next++
	id := json.RawMessage(fmt.Sprint(c.next))
	p, _ := json.Marshal(params)
	resp, err := c.post(ctx, rpcMessage{JSONRPC: "2.0", ID: id, Method: method, Params: p})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	answer := func(raw []byte) (json.RawMessage, bool, error) {
		var m rpcMessage
		if json.Unmarshal(raw, &m) != nil || string(m.ID) != string(id) {
			return nil, false, nil
		}
		if m.Error != nil {
			return nil, true, errors.New(m.Error.Message)
		}
		return m.Result, true, nil
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxRunRequestBytes))
		if err != nil {
			return nil, err
		}
		if res, ok, err := answer(body); ok {
			return res, err
		}
		return nil, fmt.Errorf("the server didn't answer %s", method)
	}
	r := bufio.NewReader(io.LimitReader(resp.Body, maxRunRequestBytes))
	var data []string
	for {
		line, err := r.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if d, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(d, " "))
		} else if line == "" && len(data) > 0 {
			if res, ok, err := answer([]byte(strings.Join(data, "\n"))); ok {
				return res, err
			}
			data = nil
		}
		if err != nil {
			return nil, fmt.Errorf("the server didn't answer %s", method)
		}
	}
}

func (c *mcpClient) notify(ctx context.Context, method string) error {
	resp, err := c.post(ctx, rpcMessage{JSONRPC: "2.0", Method: method})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Ends the session, if the server keeps one.
func (c *mcpClient) close() {
	if c.session == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Mcp-Session-Id", c.session)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
}
