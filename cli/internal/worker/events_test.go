package worker

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

func TestParseEventFixtureStreamKinds(t *testing.T) {
	data, err := os.ReadFile("testdata/worker-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	var events []Event
	for scanner.Scan() {
		event, err := ParseEvent(scanner.Bytes())
		if err != nil {
			t.Fatalf("fixture line rejected: %q: %v", scanner.Text(), err)
		}
		if !event.KnownKind() {
			t.Fatalf("fixture event kind not in vocabulary: %q", event.Type)
		}
		events = append(events, event)
	}
	if len(events) != 5 {
		t.Fatalf("expected 5 fixture events, got %d", len(events))
	}
	if events[0].Type != EventThreadStarted || events[0].ThreadID != rolloutFixtureSession {
		t.Fatalf("thread.started identity lost: %+v", events[0])
	}
	if events[1].Type != EventTurnStarted {
		t.Fatalf("turn.start classification lost: %+v", events[1])
	}
	if events[2].Type != EventItemStarted || events[2].Item == nil || events[2].Item.Type != ItemReasoning || !events[2].Item.KnownItemType() {
		t.Fatalf("item.started reasoning classification lost: %+v", events[2])
	}
	final := events[3]
	if final.Type != EventItemCompleted || final.Item.ID != "answer" || final.Item.Type != ItemAgentMessage ||
		!strings.Contains(final.Item.Text, `"status":"completed"`) {
		t.Fatalf("final agent_message lost: %+v", final)
	}
	completed := events[4]
	if completed.Type != EventTurnCompleted || completed.Usage == nil ||
		completed.Usage.InputTokens != 4 || completed.Usage.CachedInputTokens != 0 || completed.Usage.OutputTokens != 2 ||
		completed.Usage.CacheWriteInputTokens == nil || *completed.Usage.CacheWriteInputTokens != 0 ||
		completed.Usage.ReasoningOutputTokens == nil || *completed.Usage.ReasoningOutputTokens != 1 {
		t.Fatalf("turn.completed usage lost: %+v", completed)
	}
}

func TestParseEventCases(t *testing.T) {
	cases := []struct {
		name       string
		line       string
		want       Event
		wantErr    bool
		wantKnown  bool
		wantItemOK bool
	}{
		{
			name:      "thread started",
			line:      `{"type":"thread.started","thread_id":"22222222-2222-4222-8222-222222222222"}`,
			want:      Event{Type: EventThreadStarted, ThreadID: "22222222-2222-4222-8222-222222222222"},
			wantKnown: true,
		},
		{
			name:      "turn failed marker",
			line:      `{"type":"turn.failed"}`,
			want:      Event{Type: EventTurnFailed},
			wantKnown: true,
		},
		{
			name:      "error marker",
			line:      `{"type":"error"}`,
			want:      Event{Type: EventError},
			wantKnown: true,
		},
		{
			name:       "mcp tool call item",
			line:       `{"type":"item.completed","item":{"id":"tool","type":"mcp_tool_call","server":"unica","tool":"unica.runtime.job.start"}}`,
			want:       Event{Type: EventItemCompleted, Item: &Item{ID: "tool", Type: ItemMCPToolCall, Server: "unica", Tool: "unica.runtime.job.start"}},
			wantKnown:  true,
			wantItemOK: true,
		},
		{
			name:       "command execution item",
			line:       `{"type":"item.updated","item":{"id":"exec","type":"command_execution","command":"pwsh -NoProfile"}}`,
			want:       Event{Type: EventItemUpdated, Item: &Item{ID: "exec", Type: ItemCommandExecution}},
			wantKnown:  true,
			wantItemOK: true,
		},
		{
			name:      "unknown event kind tolerated",
			line:      `{"type":"token_count","total":42}`,
			want:      Event{Type: "token_count"},
			wantKnown: false,
		},
		{
			name:       "unknown item kind tolerated",
			line:       `{"type":"item.completed","item":{"id":"future","type":"web_search"}}`,
			want:       Event{Type: EventItemCompleted, Item: &Item{ID: "future", Type: "web_search"}},
			wantKnown:  true,
			wantItemOK: false,
		},
		{
			name:    "thread started without thread id",
			line:    `{"type":"thread.started"}`,
			wantErr: true,
		},
		{
			name:    "turn completed without usage",
			line:    `{"type":"turn.completed"}`,
			wantErr: true,
		},
		{
			name:    "usage missing required counter",
			line:    `{"type":"turn.completed","usage":{"input_tokens":4,"output_tokens":2}}`,
			wantErr: true,
		},
		{
			name:    "usage fractional counter",
			line:    `{"type":"turn.completed","usage":{"input_tokens":4,"cached_input_tokens":0.5,"output_tokens":2}}`,
			wantErr: true,
		},
		{
			name:    "usage boolean counter",
			line:    `{"type":"turn.completed","usage":{"input_tokens":true,"cached_input_tokens":0,"output_tokens":2}}`,
			wantErr: true,
		},
		{
			name:    "usage negative counter",
			line:    `{"type":"turn.completed","usage":{"input_tokens":-1,"cached_input_tokens":0,"output_tokens":2}}`,
			wantErr: true,
		},
		{
			name:    "usage string counter",
			line:    `{"type":"turn.completed","usage":{"input_tokens":"4","cached_input_tokens":0,"output_tokens":2}}`,
			wantErr: true,
		},
		{
			name:    "duplicate json key",
			line:    `{"type":"turn.started","type":"turn.completed"}`,
			wantErr: true,
		},
		{
			name:    "trailing data after object",
			line:    `{"type":"turn.started"} {"type":"error"}`,
			wantErr: true,
		},
		{
			name:    "malformed json",
			line:    `{"type":"turn.started"`,
			wantErr: true,
		},
		{
			name:    "top level array",
			line:    `[{"type":"turn.started"}]`,
			wantErr: true,
		},
		{
			name:    "event without type",
			line:    `{"thread_id":"22222222-2222-4222-8222-222222222222"}`,
			wantErr: true,
		},
		{
			name:    "item missing",
			line:    `{"type":"item.completed"}`,
			wantErr: true,
		},
		{
			name:    "item without type",
			line:    `{"type":"item.started","item":{"id":"x"}}`,
			wantErr: true,
		},
		{
			name:    "completed item without id",
			line:    `{"type":"item.completed","item":{"type":"reasoning"}}`,
			wantErr: true,
		},
		{
			name:    "mcp tool call without server",
			line:    `{"type":"item.started","item":{"id":"t","type":"mcp_tool_call","tool":"unica.meta.validate"}}`,
			wantErr: true,
		},
		{
			name:    "completed agent message without text",
			line:    `{"type":"item.completed","item":{"id":"answer","type":"agent_message"}}`,
			wantErr: true,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			event, err := ParseEvent([]byte(test.line))
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", event)
				}
				if ClassOf(err) != "BF_INVALID" {
					t.Fatalf("stream parse failures are BF_INVALID, got %q: %v", ClassOf(err), err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if event.Type != test.want.Type || event.ThreadID != test.want.ThreadID {
				t.Fatalf("classification mismatch: got %+v, want %+v", event, test.want)
			}
			if event.KnownKind() != test.wantKnown {
				t.Fatalf("KnownKind mismatch for %q", event.Type)
			}
			if test.want.Item != nil {
				if event.Item == nil || *event.Item != *test.want.Item {
					t.Fatalf("item mismatch: got %+v, want %+v", event.Item, test.want.Item)
				}
				if event.Item.KnownItemType() != test.wantItemOK {
					t.Fatalf("KnownItemType mismatch for %q", event.Item.Type)
				}
			}
		})
	}
}
