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
