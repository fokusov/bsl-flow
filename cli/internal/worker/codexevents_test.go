package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEventFixture(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadProfiledCodexEventsHappyPath(t *testing.T) {
	result := `{"schema_version":1,"status":"needs_input","summary":"awaiting input","payload_json":"{}"}`
	path := writeEventFixture(t,
		`{"type":"thread.started","thread_id":"33333333-3333-4333-8333-333333333333"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.started","item":{"id":"reason-1","type":"reasoning"}}`,
		`{"type":"item.completed","item":{"id":"reason-1","type":"reasoning"}}`,
		`{"type":"item.completed","item":{"id":"answer","type":"agent_message","text":`+jsonString(result)+`}}`,
		`{"type":"turn.completed","usage":{"input_tokens":9,"cached_input_tokens":1,"output_tokens":5,"cache_write_input_tokens":2,"reasoning_output_tokens":7}}`,
	)
	session, err := ReadProfiledCodexEvents(path, 0, nil, false)
	if err != nil {
		t.Fatalf("valid stream rejected: %v", err)
	}
	if session.SessionID != "33333333-3333-4333-8333-333333333333" || session.Final != result {
		t.Fatalf("session evidence lost: %+v", session)
	}
	if session.Usage == nil {
		t.Fatal("usage lost")
	}
	if value, _ := asInt64(session.Usage["input_tokens"]); value != 9 {
		t.Fatalf("input tokens: %v", session.Usage["input_tokens"])
	}
	typed := session.TypedUsage()
	if typed.InputTokens != 9 || typed.CachedInputTokens != 1 || typed.OutputTokens != 5 ||
		typed.CacheWriteInputTokens == nil || *typed.CacheWriteInputTokens != 2 ||
		typed.ReasoningOutputTokens == nil || *typed.ReasoningOutputTokens != 7 {
		t.Fatalf("typed usage: %+v", typed)
	}
}

const threadLine = `{"type":"thread.started","thread_id":"33333333-3333-4333-8333-333333333333"}`
const turnLine = `{"type":"turn.started"}`
const answerLine = `{"type":"item.completed","item":{"id":"answer","type":"agent_message","text":"\"result\""}}`
const usageLine = `{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1}}`

func TestReadProfiledCodexEventsCases(t *testing.T) {
	// stream builds a fresh slice per case (never append onto a shared
	// prefix, which would alias and corrupt later cases).
	stream := func(lines ...string) []string { return lines }
	cases := []struct {
		name      string
		lines     []string
		allowed   []string
		noTools   bool
		wantErr   string
		wantClass string
	}{
		{
			name:    "provider failure exit code",
			lines:   stream(threadLine, turnLine, answerLine, usageLine),
			wantErr: "BF_BLOCKED: Codex provider/process failed.",
		},
		{
			name:    "error event",
			lines:   stream(threadLine, `{"type":"error"}`),
			wantErr: "BF_BLOCKED: Codex reported a provider failure.",
		},
		{
			name:    "turn failed event",
			lines:   stream(threadLine, `{"type":"turn.failed"}`),
			wantErr: "BF_BLOCKED: Codex reported a provider failure.",
		},
		{
			name:    "malformed line",
			lines:   stream(threadLine, `{"type":`),
			wantErr: "BF_BLOCKED: malformed Codex JSONL stream.",
		},
		{
			name:    "duplicate session",
			lines:   stream(threadLine, `{"type":"thread.started","thread_id":"44444444-4444-4444-8444-444444444444"}`),
			wantErr: "BF_BLOCKED: duplicate Codex session.",
		},
		{
			name:    "second turn start",
			lines:   stream(threadLine, turnLine, turnLine),
			wantErr: "BF_BLOCKED: invalid Codex turn start.",
		},
		{
			name:    "item outside turn",
			lines:   stream(threadLine, answerLine),
			wantErr: "BF_BLOCKED: Codex item outside the turn.",
		},
		{
			name:    "unregistered item type",
			lines:   stream(threadLine, turnLine, `{"type":"item.completed","item":{"id":"x","type":"web_search"}}`),
			wantErr: "BF_BLOCKED: unregistered Codex item type.",
		},
		{
			name:    "critic tool event",
			lines:   stream(threadLine, turnLine, `{"type":"item.completed","item":{"id":"cmd","type":"command_execution"}}`),
			noTools: true,
			wantErr: "BF_BLOCKED: attached-only Codex critic emitted a tool event.",
		},
		{
			name:    "unregistered mcp tool",
			lines:   stream(threadLine, turnLine, `{"type":"item.completed","item":{"id":"t","type":"mcp_tool_call","server":"unica","tool":"unica.unknown"}}`),
			allowed: []string{"unica.project.map"},
			wantErr: "BF_BLOCKED: Codex called an unregistered MCP tool.",
		},
		{
			name:    "wrong mcp server",
			lines:   stream(threadLine, turnLine, `{"type":"item.completed","item":{"id":"t","type":"mcp_tool_call","server":"other","tool":"unica.project.map"}}`),
			allowed: []string{"unica.project.map"},
			wantErr: "BF_BLOCKED: Codex called an unregistered MCP tool.",
		},
		{
			name:    "duplicate completed item",
			lines:   stream(threadLine, turnLine, answerLine, answerLine),
			wantErr: "BF_BLOCKED: duplicate Codex completed item.",
		},
		{
			name:    "events after completion",
			lines:   stream(threadLine, turnLine, answerLine, usageLine, `{"type":"turn.started"}`),
			wantErr: "BF_BLOCKED: Codex emitted events after completion.",
		},
		{
			name:    "unsupported event kind",
			lines:   stream(threadLine, `{"type":"token_count"}`),
			wantErr: "BF_BLOCKED: unsupported Codex JSONL event.",
		},
		{
			name:    "incomplete evidence",
			lines:   stream(threadLine, turnLine, usageLine),
			wantErr: "BF_BLOCKED: incomplete Codex session evidence.",
		},
		{
			name:      "usage missing counter is invalid class",
			lines:     stream(threadLine, turnLine, answerLine, `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`),
			wantErr:   "BF_INVALID: Codex usage.cached_input_tokens is required.",
			wantClass: "BF_INVALID",
		},
		{
			name:    "usage fractional counter",
			lines:   stream(threadLine, turnLine, answerLine, `{"type":"turn.completed","usage":{"input_tokens":1.5,"cached_input_tokens":0,"output_tokens":1}}`),
			wantErr: "BF_BLOCKED: invalid Codex usage counters.",
		},
		{
			name:    "usage boolean counter",
			lines:   stream(threadLine, turnLine, answerLine, `{"type":"turn.completed","usage":{"input_tokens":true,"cached_input_tokens":0,"output_tokens":1}}`),
			wantErr: "BF_BLOCKED: invalid Codex usage counters.",
		},
		{
			name:    "usage string counter",
			lines:   stream(threadLine, turnLine, answerLine, `{"type":"turn.completed","usage":{"input_tokens":"1","cached_input_tokens":0,"output_tokens":1}}`),
			wantErr: "BF_BLOCKED: invalid Codex usage counters.",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			exitCode := 0
			if test.name == "provider failure exit code" {
				exitCode = 1
			}
			_, err := ReadProfiledCodexEvents(writeEventFixture(t, test.lines...), exitCode, test.allowed, test.noTools)
			if err == nil {
				t.Fatal("expected failure")
			}
			if err.Error() != test.wantErr {
				t.Fatalf("diagnostic mismatch:\n got %s\nwant %s", err, test.wantErr)
			}
			wantClass := "BF_BLOCKED"
			if test.wantClass != "" {
				wantClass = test.wantClass
			}
			if ClassOf(err) != wantClass {
				t.Fatalf("class mismatch: %q", ClassOf(err))
			}
		})
	}
}

func TestReadProfiledCodexEventsOutputBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	big := make([]byte, 16777217)
	if err := os.WriteFile(path, big, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProfiledCodexEvents(path, 0, nil, false); err == nil ||
		err.Error() != "BF_BLOCKED: Codex JSONL exceeds the output bound." {
		t.Fatalf("bound diagnostic: %v", err)
	}
}
