package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFilterFor(t *testing.T) {
	if f := filterFor(mcpServer{}, nil); f != nil {
		t.Errorf("an MCP server without picking: %v, want all its tools", f)
	}
	f := filterFor(mcpServer{Connector: "github"}, nil)
	if !f.allows("list_issues") || f.allows("create_issue") {
		t.Errorf("a connector without picking: %v", f)
	}
	f = filterFor(mcpServer{}, []string{"search"})
	if !f.allows("search") || f.allows("delete") {
		t.Errorf("picked: %v", f)
	}
	if f := filterFor(mcpServer{}, []string{}); f.allows("search") {
		t.Errorf("picking none allows %v", f)
	}
}

func TestToolFilterRefuse(t *testing.T) {
	f := toolFilter{"search": true}
	msgs, _ := parseRPC([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search"}}`))
	if f.refuse(msgs) != nil {
		t.Errorf("a picked tool was refused")
	}
	msgs, _ = parseRPC([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"delete"}}`))
	m, ok := f.refuse(msgs).(rpcMessage)
	if !ok || string(m.ID) != "3" || m.Error == nil || !strings.Contains(m.Error.Message, "delete") {
		t.Errorf("refusal %+v", f.refuse(msgs))
	}
	// A batch goes whole or not at all.
	msgs, batch := parseRPC([]byte(`[{"jsonrpc":"2.0","id":1,"method":"tools/list"},{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"delete"}},{"jsonrpc":"2.0","method":"notifications/x"}]`))
	out, ok := f.refuse(msgs).([]rpcMessage)
	if !batch || !ok || len(out) != 2 || !strings.Contains(out[0].Error.Message, "not sent") {
		t.Errorf("batch refusal %+v", f.refuse(msgs))
	}
	if toolFilter(nil).refuse(msgs) != nil {
		t.Errorf("all tools refused one")
	}
}

// A message is read by its keys as written, so a key in another case can't
// stand in for one the filter reads, and what isn't JSON-RPC, a batch with
// something other than a message in it among them, isn't read at all.
func TestParseRPCReadsKeysExactly(t *testing.T) {
	f := toolFilter{"search": true}
	for name, body := range map[string]string{
		"a Method decoy":        `{"jsonrpc":"2.0","id":3,"method":"tools/call","Method":"ping","params":{"name":"delete"}}`,
		"a decoy before":        `{"jsonrpc":"2.0","id":3,"METHOD":"ping","method":"tools/call","params":{"name":"delete"}}`,
		"a Params decoy":        `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"delete"},"Params":{"name":"search"}}`,
		"a batch with a number": `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete"}},5]`,
		"a batch with null":     `[null,{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete"}}]`,
		"garbage":               `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"delete"}`,
		"a method not a string": `{"jsonrpc":"2.0","id":3,"method":["tools/call"],"params":{"name":"delete"}}`,
		"a number":              `5`,
	} {
		if msgs, _ := parseRPC([]byte(body)); msgs != nil {
			t.Errorf("%s was read as %+v", name, msgs)
		}
	}
	for name, body := range map[string]string{
		"a NAME decoy":    `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"delete","NAME":"search"}}`,
		"a decoy before":  `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"Name":"search","name":"delete"}}`,
		"no name":         `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{}}`,
		"params a string": `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":"search"}`,
	} {
		msgs, _ := parseRPC([]byte(body))
		if msgs == nil || f.refuse(msgs) == nil {
			t.Errorf("%s wasn't refused: %+v", name, msgs)
		}
	}
	if msgs, _ := parseRPC([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search","arguments":{"Name":"x"}}}`)); len(msgs) != 1 || f.refuse(msgs) != nil {
		t.Errorf("a picked tool with an argument called Name was refused: %+v", msgs)
	}
	// The GET opening the server's stream of events has no body, and no
	// message to refuse.
	if msgs, batch := parseRPC([]byte("  ")); msgs != nil || batch || f.refuse(msgs) != nil {
		t.Errorf("an empty body: %+v %v", msgs, batch)
	}
}

const toolsResult = `{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"search","inputSchema":{}},{"name":"delete","inputSchema":{}}],"nextCursor":"c"}}`

func filtered(t *testing.T, contentType, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		io.WriteString(w, body)
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	w := httptest.NewRecorder()
	toolFilter{"search": true}.filterResponse(w, resp)
	return w.Body.String()
}

// The tools a step doesn't have are left out of a server's list, whether it
// answers with JSON or a stream of events, and the rest of the list stays.
func TestToolFilterResponse(t *testing.T) {
	out := filtered(t, "application/json", toolsResult)
	var m struct {
		Result struct {
			Tools      []map[string]any `json:"tools"`
			NextCursor string           `json:"nextCursor"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &m); err != nil || len(m.Result.Tools) != 1 || m.Result.Tools[0]["name"] != "search" || m.Result.NextCursor != "c" {
		t.Errorf("JSON: %s", out)
	}

	out = filtered(t, "text/event-stream", ": ping\n\nevent: message\nid: 9\ndata: "+toolsResult+"\n\n")
	if !strings.Contains(out, "event: message\nid: 9\ndata: {") || strings.Contains(out, "delete") || !strings.Contains(out, "search") || !strings.HasSuffix(out, "\n\n") {
		t.Errorf("events: %q", out)
	}
}
