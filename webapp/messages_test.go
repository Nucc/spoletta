package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestClip(t *testing.T) {
	short := strings.Repeat("a", maxMessageText)
	if got := clip(short); got != short {
		t.Errorf("a text of the limit is cut")
	}
	long := strings.Repeat("b", maxMessageText+5)
	if got := clip(long); got != strings.Repeat("b", maxMessageText)+"\n… (5 more bytes)" {
		t.Errorf("clip(long) ends %q", got.(string)[maxMessageText:])
	}
	for _, v := range []any{nil, 3.0, []any{"x"}} {
		if got, _ := json.Marshal(clip(v)); string(got) != string(marshalled(json.Marshal(v))) {
			t.Errorf("clip(%v) = %v", v, got)
		}
	}
}

func marshalled(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// A message keeps what the conversation shows: signatures and image data,
// which only the provider reads, are dropped, and long texts cut.
func TestTrimMessage(t *testing.T) {
	long := strings.Repeat("x", maxMessageText+10)
	raw := marshalled(json.Marshal(map[string]any{
		"role": "assistant",
		"content": []any{
			map[string]any{"type": "text", "text": "hi", "textSignature": "sig"},
			map[string]any{"type": "thinking", "thinking": long, "thinkingSignature": "sig", "thoughtSignature": "sig"},
			map[string]any{"type": "image", "data": "aGVsbG8=", "mimeType": "image/png"},
			map[string]any{"type": "toolCall", "name": "bash", "arguments": map[string]any{"command": "ls"}},
			map[string]any{"type": "toolCall", "name": "write", "arguments": map[string]any{"content": long}},
			"not a block",
		},
		"details": map[string]any{"diff": "small"},
	}))
	out, err := trimMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Role    string         `json:"role"`
		Content []any          `json:"content"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	blocks := m.Content
	if m.Role != "assistant" || len(blocks) != 6 || m.Details["diff"] != "small" {
		t.Fatalf("trimMessage = %s", out)
	}
	if strings.Contains(string(out), "Signature") || strings.Contains(string(out), "aGVsbG8=") {
		t.Errorf("signatures or image data kept: %s", out)
	}
	block := func(i int) map[string]any { return blocks[i].(map[string]any) }
	if block(0)["text"] != "hi" || block(2)["mimeType"] != "image/png" {
		t.Errorf("blocks = %v %v", block(0), block(2))
	}
	if th := block(1)["thinking"].(string); !strings.HasSuffix(th, "\n… (10 more bytes)") {
		t.Errorf("thinking not cut: ends %q", th[len(th)-20:])
	}
	if args := block(3)["arguments"].(map[string]any); args["command"] != "ls" {
		t.Errorf("short arguments = %v", args)
	}
	if args := block(4)["arguments"].(map[string]any); len(args) != 1 || !strings.HasSuffix(args["truncated"].(string), "more bytes)") {
		t.Errorf("long arguments not cut")
	}

	// Large details are dropped; the content says what the model saw.
	out, err = trimMessage(marshalled(json.Marshal(map[string]any{"role": "toolResult", "details": map[string]any{"diff": long}})))
	if err != nil || strings.Contains(string(out), "details") {
		t.Errorf("large details kept: %.200s, %v", out, err)
	}

	// A message still too large after cutting is kept by its role only.
	var many []any
	for range maxMessageBytes/maxMessageText + 1 {
		many = append(many, map[string]any{"type": "text", "text": strings.Repeat("y", maxMessageText)})
	}
	out, err = trimMessage(marshalled(json.Marshal(map[string]any{"role": "user", "content": many})))
	if err != nil {
		t.Fatal(err)
	}
	var omitted struct {
		Role    string `json:"role"`
		Omitted int    `json:"omitted"`
	}
	if err := json.Unmarshal(out, &omitted); err != nil || omitted.Role != "user" || omitted.Omitted <= maxMessageBytes || strings.Contains(string(out), "content") {
		t.Errorf("an oversized message = %.200s", out)
	}

	for _, bad := range []string{`not json`, `[1, 2]`, `"text"`} {
		if _, err := trimMessage(json.RawMessage(bad)); err == nil {
			t.Errorf("trimMessage(%s) took it", bad)
		}
	}
}

// A Claude Code step's conversation, as the worker translates it into pi's
// shape, is kept as pi's is: every message whole, tool calls with their
// arguments and results with their patches.
func TestTrimMessageOfClaudeCode(t *testing.T) {
	conversation := []string{
		`{"role":"user","content":"Fix the greeting."}`,
		`{"role":"assistant","model":"claude-sonnet-4-6","stopReason":"toolUse","content":[{"type":"thinking","thinking":"Look first."},{"type":"text","text":"Listing the files."},{"type":"toolCall","id":"toolu_1","name":"bash","arguments":{"command":"ls"}}]}`,
		`{"role":"toolResult","toolCallId":"toolu_1","toolName":"bash","isError":false,"content":[{"type":"text","text":"a.txt"}]}`,
		`{"role":"assistant","model":"claude-sonnet-4-6","stopReason":"toolUse","content":[{"type":"toolCall","id":"toolu_2","name":"edit","arguments":{"path":"/data/workspaces/run-7/repo/a.txt","oldText":"hi","newText":"hello"}}]}`,
		`{"role":"toolResult","toolCallId":"toolu_2","toolName":"edit","isError":false,"content":[{"type":"text","text":"Edited a.txt"}],"details":{"patch":"@@ -1 +1 @@\n-hi\n+hello"}}`,
		`{"role":"assistant","content":[],"stopReason":"error","errorMessage":"API Error: 403 out of budget"}`,
	}
	for _, raw := range conversation {
		out, err := trimMessage(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("trimMessage(%s): %v", raw, err)
		}
		var got, want any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		json.Unmarshal([]byte(raw), &want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("trimMessage(%s)\n= %s", raw, out)
		}
	}
}
