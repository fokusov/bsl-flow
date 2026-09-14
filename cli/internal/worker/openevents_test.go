package worker

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func strconvFormatInt(value int64) string {
	return strconv.FormatInt(value, 10)
}

func writeOpenCodeFixture(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadOpenCodeEventsFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/opencode-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ReadOpenCodeEvents(writeOpenCodeFixture(t, string(data)), 0, []string{"read", "glob", "grep", "skill", "bash"})
	if err != nil {
		t.Fatalf("valid fixture rejected: %v", err)
	}
	if parsed.SessionID != "ses_fixture0123" || parsed.Status != StatusCompleted || parsed.Summary != "fixture" {
		t.Fatalf("terminal classification lost: %+v", parsed)
	}
	if parsed.PayloadJSON != `{"done":true}` {
		t.Fatalf("payload: %q", parsed.PayloadJSON)
	}
	if parsed.Usage.Total != 22 || parsed.Usage.Input != 14 || parsed.Usage.Output != 8 ||
		parsed.Usage.Reasoning != 1 || parsed.Usage.CacheRead != 2 || parsed.Usage.CacheWrite != 1 {
		t.Fatalf("usage aggregation lost: %+v", parsed.Usage)
	}
	if parsed.ReportedCostUSD != 0.03 {
		t.Fatalf("cost aggregation lost: %v", parsed.ReportedCostUSD)
	}
	if parsed.StepCount != 2 || len(parsed.ToolCalls) != 1 ||
		parsed.ToolCalls[0].Name != "read" || parsed.ToolCalls[0].CallID != "call_0001" {
		t.Fatalf("step/tool evidence lost: %+v", parsed.ToolCalls)
	}
	if parsed.Metadata["session_id"] != "ses_fixture0123" ||
		parsed.Metadata["cost_source"] != "opencode.step_finish.part.cost.sum" ||
		parsed.Metadata["observed_model"] != nil {
		t.Fatalf("metadata shape lost: %+v", parsed.Metadata)
	}
	stepCount, _ := asInt64(parsed.Metadata["step_count"])
	if stepCount != 2 {
		t.Fatalf("metadata step count: %v", parsed.Metadata["step_count"])
	}
}

func TestReadOpenCodeEventsCases(t *testing.T) {
	const session = "ses_fixture0123"
	event := func(kind string, timestamp int64, part string) string {
		return `{"type":"` + kind + `","timestamp":` + itoa64(timestamp) + `,"sessionID":"` + session +
			`","part":` + strings.ReplaceAll(part, "SESSION", session) + `}`
	}
	stepStart := func(timestamp int64, partID, messageID string) string {
		return event("step_start", timestamp, `{"type":"step-start","id":"`+partID+`","sessionID":"SESSION","messageID":"`+messageID+`"}`)
	}
	textPart := func(timestamp int64, partID, messageID, text string) string {
		return event("text", timestamp, `{"type":"text","id":"`+partID+`","sessionID":"SESSION","messageID":"`+messageID+`","text":`+jsonString(text)+`}`)
	}
	stepFinish := func(timestamp int64, partID, messageID, reason string) string {
		return event("step_finish", timestamp, `{"type":"step-finish","id":"`+partID+`","sessionID":"SESSION","messageID":"`+messageID+
			`","reason":"`+reason+`","tokens":{"total":1,"input":1,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"cost":0}`)
	}
	result := `{"schema_version":1,"status":"completed","summary":"ok","payload_json":"[]"}`
	terminal := strings.Join([]string{
		stepStart(1, "prt_1", "msg_1"),
		textPart(2, "prt_2", "msg_1", result),
		stepFinish(3, "prt_3", "msg_1", "stop"),
	}, "\n") + "\n"
	cases := []struct {
		name      string
		text      string
		allowed   []string
		wantErr   string
		wantClass string
	}{
		{name: "exit code failure", text: terminal, wantErr: "BF_BLOCKED: OpenCode process failed."},
		{
			name:    "torn stream",
			text:    strings.TrimSuffix(terminal, "\n"),
			wantErr: "BF_BLOCKED: torn OpenCode event stream.",
		},
		{
			name:    "empty stream",
			text:    "",
			wantErr: "BF_BLOCKED: empty or oversized OpenCode event stream.",
		},
		{
			name:    "invalid utf8",
			text:    "{\"type\":\"x\",\"timestamp\":1}\xff\n",
			wantErr: "BF_BLOCKED: OpenCode event stream is not UTF-8.",
		},
		{
			name:    "blank line",
			text:    strings.ReplaceAll(terminal, "\n", "\n\n"),
			wantErr: "BF_BLOCKED: blank OpenCode event line.",
		},
		{
			name:    "scalar event",
			text:    "5\n",
			wantErr: "BF_BLOCKED: OpenCode event must be a JSON object.",
		},
		{
			name:    "relative path",
			text:    terminal,
			wantErr: "BF_BLOCKED: OpenCode events path must be absolute.",
		},
		{
			name:    "blank allowed tool",
			text:    terminal,
			allowed: []string{"  "},
			wantErr: "BF_BLOCKED: invalid allowed OpenCode tool.",
		},
		{
			name:    "missing part member",
			text:    `{"type":"text","timestamp":1,"sessionID":"` + session + `","part":{"type":"text","id":"prt_1","sessionID":"` + session + `","text":"x"}}` + "\n",
			wantErr: "BF_BLOCKED: missing OpenCode part.messageID.",
		},
		{
			name:    "mixed sessions",
			text:    strings.Replace(terminal, `"sessionID":"ses_fixture0123","part":{"type":"step-start","id":"prt_1"`, `"sessionID":"ses_other99","part":{"type":"step-start","id":"prt_1"`, 1),
			wantErr: "BF_BLOCKED: mixed OpenCode sessions.",
		},
		{
			name:    "non-monotonic timestamps",
			text:    strings.Replace(terminal, `"timestamp":2`, `"timestamp":0`, 1),
			wantErr: "BF_BLOCKED: non-monotonic OpenCode timestamp.",
		},
		{
			name:    "fractional timestamp",
			text:    strings.Replace(terminal, `"timestamp":1`, `"timestamp":1.5`, 1),
			wantErr: "BF_BLOCKED: invalid OpenCode timestamp.",
		},
		{
			name:    "duplicate part identity",
			text:    stepStart(1, "prt_1", "msg_1") + "\n" + textPart(2, "prt_1", "msg_1", result) + "\n",
			wantErr: "BF_BLOCKED: duplicate OpenCode part identity.",
		},
		{
			name:    "text outside step",
			text:    event("text", 1, `{"type":"text","id":"prt_1","sessionID":"SESSION","messageID":"msg_1","text":"x"}`) + "\n",
			wantErr: "BF_BLOCKED: invalid OpenCode text event.",
		},
		{
			name: "unknown tool",
			text: strings.Join([]string{
				stepStart(1, "prt_1", "msg_1"),
				event("tool_use", 2, `{"type":"tool","id":"prt_2","sessionID":"SESSION","messageID":"msg_1","tool":"write","callID":"call_1","state":{"status":"completed"}}`),
				stepFinish(3, "prt_3", "msg_1", "tool-calls"),
				stepStart(4, "prt_4", "msg_2"),
				textPart(5, "prt_5", "msg_2", result),
				stepFinish(6, "prt_6", "msg_2", "stop"),
			}, "\n") + "\n",
			allowed: []string{"read"},
			wantErr: "BF_BLOCKED: OpenCode tool is not allowlisted.",
		},
		{
			name: "tool did not complete",
			text: strings.Join([]string{
				stepStart(1, "prt_1", "msg_1"),
				event("tool_use", 2, `{"type":"tool","id":"prt_2","sessionID":"SESSION","messageID":"msg_1","tool":"read","callID":"call_1","state":{"status":"error"}}`),
				stepFinish(3, "prt_3", "msg_1", "tool-calls"),
				stepStart(4, "prt_4", "msg_2"),
				textPart(5, "prt_5", "msg_2", result),
				stepFinish(6, "prt_6", "msg_2", "stop"),
			}, "\n") + "\n",
			allowed: []string{"read"},
			wantErr: "BF_BLOCKED: OpenCode tool did not complete successfully.",
		},
		{
			name:    "event after stop",
			text:    terminal + stepStart(4, "prt_4", "msg_2") + "\n",
			wantErr: "BF_BLOCKED: OpenCode event follows terminal stop.",
		},
		{
			// A middle step ending in "stop" trips the earlier "event follows
			// terminal stop" refusal for every following event; the
			// non-terminal-step diagnostic itself stays defensive exactly like
			// the PowerShell reader.
			name:    "terminal step with extra activity",
			text:    strings.Replace(terminal, textPart(2, "prt_2", "msg_1", result), textPart(2, "prt_2", "msg_1", result)+"\n"+textPart(3, "prt_2b", "msg_1", result), 1),
			wantErr: "BF_BLOCKED: terminal OpenCode step must contain exactly one text result.",
		},
		{
			name:    "final text not an object",
			text:    strings.Replace(terminal, jsonString(result), jsonString("5"), 1),
			wantErr: "BF_BLOCKED: final OpenCode text is not a JSON object.",
		},
		{
			name:      "structured result missing field",
			text:      strings.Replace(terminal, jsonString(result), jsonString(`{"schema_version":1,"status":"completed","summary":"ok"}`), 1),
			wantErr:   "BF_INVALID: opencode_result.payload_json is required.",
			wantClass: "BF_INVALID",
		},
		{
			name:    "invalid status",
			text:    strings.Replace(terminal, jsonString(result), jsonString(`{"schema_version":1,"status":"maybe","summary":"ok","payload_json":"[]"}`), 1),
			wantErr: "BF_BLOCKED: invalid structured OpenCode result.",
		},
		{
			name:    "fractional schema version",
			text:    strings.Replace(terminal, jsonString(result), jsonString(`{"schema_version":1.0,"status":"completed","summary":"ok","payload_json":"[]"}`), 1),
			wantErr: "BF_BLOCKED: invalid structured OpenCode result.",
		},
		{
			name:    "scalar payload json",
			text:    strings.Replace(terminal, jsonString(result), jsonString(`{"schema_version":1,"status":"completed","summary":"ok","payload_json":"5"}`), 1),
			wantErr: "BF_BLOCKED: invalid OpenCode payload_json.",
		},
		{
			name:      "blank summary",
			text:      strings.Replace(terminal, jsonString(result), jsonString(`{"schema_version":1,"status":"completed","summary":"  ","payload_json":"[]"}`), 1),
			wantErr:   "BF_INVALID: invalid opencode_result.summary.",
			wantClass: "BF_INVALID",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			exitCode := 0
			if test.name == "exit code failure" {
				exitCode = 3
			}
			path := "relative.jsonl"
			if test.name != "relative path" {
				path = writeOpenCodeFixture(t, test.text)
			}
			_, err := ReadOpenCodeEvents(path, exitCode, test.allowed)
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

func itoa64(value int64) string {
	return strconvFormatInt(value)
}
