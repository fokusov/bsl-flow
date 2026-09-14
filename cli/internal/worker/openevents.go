package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"bsl-flow/cli/internal/strictjson"
)

// Port of Read-BFOpenCodeEvents (OpenCode.Events.ps1:5-115): the strict
// classifier of the `opencode run --format json` step/part event stream. Every
// structural violation is a BF_BLOCKED refusal.

// OpenCodeUsage accumulates the per-step token/cost counters. PowerShell sums
// in 96-bit decimal; the native port sums in float64, which is exact for the
// integral token counts and round-trip doubles of real cost reports.
type OpenCodeUsage struct {
	Total      float64
	Input      float64
	Output     float64
	Reasoning  float64
	CacheRead  float64
	CacheWrite float64
}

// Map renders the canonical usage shape {total,input,output,reasoning,
// cache:{read,write}} of the OpenCode receipt.
func (u OpenCodeUsage) Map() map[string]any {
	return map[string]any{
		"total":     u.Total,
		"input":     u.Input,
		"output":    u.Output,
		"reasoning": u.Reasoning,
		"cache":     map[string]any{"read": u.CacheRead, "write": u.CacheWrite},
	}
}

// OpenCodeToolCall is one completed, allowlisted tool invocation.
type OpenCodeToolCall struct {
	Name      string
	CallID    string
	MessageID string
	PartID    string
}

// Map renders the canonical tool_calls entry.
func (t OpenCodeToolCall) Map() map[string]any {
	return map[string]any{"name": t.Name, "call_id": t.CallID, "message_id": t.MessageID, "part_id": t.PartID}
}

// OpenCodeParsed is the parsed terminal evidence of one OpenCode worker
// stream: the structured result and the receipt metadata.
type OpenCodeParsed struct {
	Result          map[string]any
	Metadata        map[string]any
	SessionID       string
	Usage           OpenCodeUsage
	ReportedCostUSD float64
	StepCount       int
	ToolCalls       []OpenCodeToolCall
	Status          string
	Summary         string
	PayloadJSON     string
}

var (
	openCodeSessionPattern = regexp.MustCompile(`^ses_[A-Za-z0-9]+$`)
	openCodePartPattern    = regexp.MustCompile(`^prt_[A-Za-z0-9]+$`)
	openCodeMessagePattern = regexp.MustCompile(`^msg_[A-Za-z0-9]+$`)
	openCodeCallPattern    = regexp.MustCompile(`^call_[A-Za-z0-9_]+$`)
)

// counterName renders the dotted counter names of Get-BFOpenCodeNumber
// diagnostics ("cache.read", "cache.write").
func counterName(member string) string {
	if member == "read" || member == "write" {
		return "cache." + member
	}
	return member
}

// isOpenCodeAbsolutePath mirrors [IO.Path]::IsPathRooted.
func isOpenCodeAbsolutePath(path string) bool {
	return filepath.IsAbs(path)
}

func isValidUTF8(data []byte) bool {
	return utf8.Valid(data)
}

// jsonSyntaxKind classifies a validated JSON document like Test-BFJsonSyntax:
// "object", "array" or "scalar".
func jsonSyntaxKind(text string) string {
	value, err := parseJSONScalarDocument([]byte(text))
	if err != nil {
		return ""
	}
	switch value.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		return "scalar"
	}
}

// ReadOpenCodeEvents mirrors Read-BFOpenCodeEvents with the exact diagnostics
// and ordering (OpenCode.Events.ps1:5-115).
func ReadOpenCodeEvents(path string, exitCode int, allowedTools []string) (OpenCodeParsed, error) {
	if exitCode != 0 {
		return OpenCodeParsed{}, blocked("OpenCode process failed.")
	}
	if !isOpenCodeAbsolutePath(path) {
		return OpenCodeParsed{}, blocked("OpenCode events path must be absolute.")
	}
	fullPath, err := workerSafePath(path)
	if err != nil {
		return OpenCodeParsed{}, err
	}
	raw, err := os.ReadFile(fullPath)
	if err != nil {
		return OpenCodeParsed{}, blocked("OpenCode events file is missing.")
	}
	if len(raw) == 0 || len(raw) > 16777216 {
		return OpenCodeParsed{}, blocked("empty or oversized OpenCode event stream.")
	}
	if !isValidUTF8(raw) {
		return OpenCodeParsed{}, blocked("OpenCode event stream is not UTF-8.")
	}
	text := string(raw)
	if !strings.HasSuffix(text, "\n") {
		return OpenCodeParsed{}, blocked("torn OpenCode event stream.")
	}
	events := make([]map[string]any, 0, 16)
	for _, line := range strings.Split(strings.TrimRight(text, "\r\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			return OpenCodeParsed{}, blocked("blank OpenCode event line.")
		}
		// Test-BFJsonSyntax phase (duplicate keys included) is BF_INVALID;
		// a valid non-object document is the BF_BLOCKED vocabulary refusal.
		if err := strictjson.RejectDuplicateKeys([]byte(line)); err != nil {
			return OpenCodeParsed{}, invalid("%v", err)
		}
		value, err := parseJSONScalarDocument([]byte(line))
		if err != nil {
			return OpenCodeParsed{}, invalid("%v", err)
		}
		event, isObject := asObject(value)
		if !isObject {
			return OpenCodeParsed{}, blocked("OpenCode event must be a JSON object.")
		}
		events = append(events, event)
	}
	allowed := make(map[string]struct{}, len(allowedTools))
	for _, tool := range allowedTools {
		if strings.TrimSpace(tool) == "" {
			return OpenCodeParsed{}, blocked("invalid allowed OpenCode tool.")
		}
		allowed[tool] = struct{}{}
	}
	partIDs := make(map[string]struct{})
	callIDs := make(map[string]struct{})
	messageIDs := make(map[string]struct{})
	session := ""
	var previousTimestamp *int64
	type stepState struct {
		messageID  string
		activities []map[string]any
		finish     string
	}
	var current *stepState
	stopped := false
	steps := make([]*stepState, 0, 4)
	toolCalls := make([]OpenCodeToolCall, 0, 4)
	usage := OpenCodeUsage{}
	cost := float64(0)
	assertID := func(value any, pattern *regexp.Regexp, prefix, name string) (string, error) {
		text, isString := asString(value)
		if !isString || len(text) > 256 || !pattern.MatchString(text) {
			return "", blocked("invalid OpenCode %s identity.", name)
		}
		return text, nil
	}
	field := func(object map[string]any, name, context string) (any, error) {
		if object == nil {
			return nil, blocked("missing OpenCode %s.%s.", context, name)
		}
		if value, present := object[name]; present {
			return value, nil
		}
		return nil, blocked("missing OpenCode %s.%s.", context, name)
	}
	number := func(value any, name string) (float64, error) {
		// Get-BFOpenCodeNumber accepts every numeric JSON materialization
		// and rejects booleans, strings and nulls.
		literal, isNumber := value.(json.Number)
		if !isNumber {
			return 0, blocked("invalid OpenCode %s.", name)
		}
		parsed, err := literal.Float64()
		if err != nil {
			return 0, blocked("invalid OpenCode %s.", name)
		}
		if parsed < 0 {
			return 0, blocked("negative OpenCode %s.", name)
		}
		return parsed, nil
	}
	for _, event := range events {
		eventTypeValue, err := field(event, "type", "event")
		if err != nil {
			return OpenCodeParsed{}, err
		}
		partValue, err := field(event, "part", "event")
		if err != nil {
			return OpenCodeParsed{}, err
		}
		timestampValue, err := field(event, "timestamp", "event")
		if err != nil {
			return OpenCodeParsed{}, err
		}
		eventSessionValue, err := field(event, "sessionID", "event")
		if err != nil {
			return OpenCodeParsed{}, err
		}
		eventType, typeIsString := asString(eventTypeValue)
		part, partIsObject := asObject(partValue)
		if !typeIsString || !partIsObject {
			return OpenCodeParsed{}, blocked("malformed OpenCode event.")
		}
		timestamp, timestampOK := asInt64(timestampValue)
		if !timestampOK || timestamp < 0 {
			return OpenCodeParsed{}, blocked("invalid OpenCode timestamp.")
		}
		if previousTimestamp != nil && timestamp < *previousTimestamp {
			return OpenCodeParsed{}, blocked("non-monotonic OpenCode timestamp.")
		}
		previousTimestamp = &timestamp
		eventSession, _ := asString(eventSessionValue)
		if session == "" {
			sessionID, err := assertID(eventSessionValue, openCodeSessionPattern, "ses_", "session")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			session = sessionID
		}
		partSessionValue, err := field(part, "sessionID", "part")
		if err != nil {
			return OpenCodeParsed{}, err
		}
		partSession, _ := asString(partSessionValue)
		if eventSession != session || partSession != session {
			return OpenCodeParsed{}, blocked("mixed OpenCode sessions.")
		}
		partIDValue, err := field(part, "id", "part")
		if err != nil {
			return OpenCodeParsed{}, err
		}
		partID, err := assertID(partIDValue, openCodePartPattern, "prt_", "part")
		if err != nil {
			return OpenCodeParsed{}, err
		}
		if _, duplicate := partIDs[partID]; duplicate {
			return OpenCodeParsed{}, blocked("duplicate OpenCode part identity.")
		}
		partIDs[partID] = struct{}{}
		if stopped {
			return OpenCodeParsed{}, blocked("OpenCode event follows terminal stop.")
		}
		switch eventType {
		case "step_start":
			messageIDValue, err := field(part, "messageID", "part")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			partType, _ := asString(part["type"])
			if current != nil || partType != "step-start" {
				return OpenCodeParsed{}, blocked("invalid OpenCode step start.")
			}
			messageID, err := assertID(messageIDValue, openCodeMessagePattern, "msg_", "message")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			if _, duplicate := messageIDs[messageID]; duplicate {
				return OpenCodeParsed{}, blocked("duplicate OpenCode step message.")
			}
			messageIDs[messageID] = struct{}{}
			current = &stepState{messageID: messageID}
		case "text":
			messageIDValue, err := field(part, "messageID", "part")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			partTextValue, err := field(part, "text", "part")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			partType, _ := asString(part["type"])
			messageID, _ := asString(messageIDValue)
			partText, textIsString := asString(partTextValue)
			if current == nil || partType != "text" || messageID != current.messageID || !textIsString {
				return OpenCodeParsed{}, blocked("invalid OpenCode text event.")
			}
			current.activities = append(current.activities, map[string]any{"kind": "text", "text": partText})
		case "tool_use":
			messageIDValue, err := field(part, "messageID", "part")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			nameValue, err := field(part, "tool", "part")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			callIDValue, err := field(part, "callID", "part")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			stateValue, err := field(part, "state", "part")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			partType, _ := asString(part["type"])
			messageID, _ := asString(messageIDValue)
			name, _ := asString(nameValue)
			if current == nil || partType != "tool" || messageID != current.messageID {
				return OpenCodeParsed{}, blocked("invalid OpenCode tool event.")
			}
			callID, err := assertID(callIDValue, openCodeCallPattern, "call_", "tool call")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			if _, isAllowed := allowed[name]; !isAllowed {
				return OpenCodeParsed{}, blocked("OpenCode tool is not allowlisted.")
			}
			if _, duplicate := callIDs[callID]; duplicate {
				return OpenCodeParsed{}, blocked("duplicate OpenCode tool call identity.")
			}
			callIDs[callID] = struct{}{}
			// Tool error states are retained in raw JSONL but rejected until a
			// controller contract demonstrates safe error/result
			// reconciliation.
			state, _ := asObject(stateValue)
			statusValue, err := field(state, "status", "tool state")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			status, _ := asString(statusValue)
			if status != "completed" {
				return OpenCodeParsed{}, blocked("OpenCode tool did not complete successfully.")
			}
			current.activities = append(current.activities, map[string]any{"kind": "tool", "name": name, "call_id": callID})
			toolCalls = append(toolCalls, OpenCodeToolCall{Name: name, CallID: callID, MessageID: current.messageID, PartID: partID})
		case "step_finish":
			messageIDValue, err := field(part, "messageID", "part")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			reasonValue, err := field(part, "reason", "part")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			tokensValue, err := field(part, "tokens", "part")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			partType, _ := asString(part["type"])
			messageID, _ := asString(messageIDValue)
			if current == nil || partType != "step-finish" || messageID != current.messageID || len(current.activities) == 0 {
				return OpenCodeParsed{}, blocked("invalid OpenCode step finish.")
			}
			reason, _ := asString(reasonValue)
			if reason != "tool-calls" && reason != "stop" {
				return OpenCodeParsed{}, blocked("unknown OpenCode step finish reason.")
			}
			tokens, _ := asObject(tokensValue)
			cacheValue, err := field(tokens, "cache", "tokens")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			cache, _ := asObject(cacheValue)
			counterValues := make(map[string]any, 6)
			for _, counter := range []struct {
				object  map[string]any
				member  string
				context string
			}{
				{tokens, "total", "tokens"},
				{tokens, "input", "tokens"},
				{tokens, "output", "tokens"},
				{tokens, "reasoning", "tokens"},
				{cache, "read", "tokens.cache"},
				{cache, "write", "tokens.cache"},
			} {
				value, err := field(counter.object, counter.member, counter.context)
				if err != nil {
					return OpenCodeParsed{}, err
				}
				counterValues[counter.member] = value
			}
			for _, name := range []string{"total", "input", "output", "reasoning", "read", "write"} {
				value, err := number(counterValues[name], counterName(name))
				if err != nil {
					return OpenCodeParsed{}, err
				}
				switch name {
				case "total":
					usage.Total += value
				case "input":
					usage.Input += value
				case "output":
					usage.Output += value
				case "reasoning":
					usage.Reasoning += value
				case "read":
					usage.CacheRead += value
				case "write":
					usage.CacheWrite += value
				}
			}
			costValue, err := field(part, "cost", "part")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			costDelta, err := number(costValue, "cost")
			if err != nil {
				return OpenCodeParsed{}, err
			}
			cost += costDelta
			current.finish = reason
			steps = append(steps, current)
			if reason == "stop" {
				stopped = true
			}
			current = nil
		default:
			return OpenCodeParsed{}, blocked("unknown OpenCode event type.")
		}
	}
	if current != nil || !stopped || len(steps) == 0 {
		return OpenCodeParsed{}, blocked("incomplete OpenCode step sequence.")
	}
	for index := 0; index < len(steps)-1; index++ {
		if steps[index].finish != "tool-calls" {
			return OpenCodeParsed{}, blocked("non-terminal OpenCode step must end in tool-calls.")
		}
	}
	last := steps[len(steps)-1]
	if last.finish != "stop" || len(last.activities) != 1 {
		return OpenCodeParsed{}, blocked("terminal OpenCode step must contain exactly one text result.")
	}
	finalActivity := last.activities[0]
	if kind, _ := asString(finalActivity["kind"]); kind != "text" {
		return OpenCodeParsed{}, blocked("terminal OpenCode step must contain exactly one text result.")
	}
	finalText, _ := asString(finalActivity["text"])
	// Final text must be a JSON object (Test-BFJsonSyntax kind 'object').
	if err := strictjson.RejectDuplicateKeys([]byte(finalText)); err != nil {
		return OpenCodeParsed{}, invalid("%v", err)
	}
	if kind := jsonSyntaxKind(finalText); kind != "object" {
		return OpenCodeParsed{}, blocked("final OpenCode text is not a JSON object.")
	}
	resultValue, err := parseJSONScalarDocument([]byte(finalText))
	if err != nil {
		return OpenCodeParsed{}, blocked("final OpenCode result cannot be materialized.")
	}
	result, isObject := asObject(resultValue)
	if !isObject {
		return OpenCodeParsed{}, blocked("final OpenCode result cannot be materialized.")
	}
	if _, err := assertFields(result, []string{"schema_version", "status", "summary", "payload_json"}, nil, "opencode_result"); err != nil {
		return OpenCodeParsed{}, err
	}
	schemaVersion, schemaOK := asInt64(result["schema_version"])
	status, _ := asString(result["status"])
	if !schemaOK || schemaVersion != 1 || !isTerminalStatus(status) {
		return OpenCodeParsed{}, blocked("invalid structured OpenCode result.")
	}
	if err := assertTextValue(result["summary"], "opencode_result.summary"); err != nil {
		return OpenCodeParsed{}, err
	}
	payloadJSON, isString := asString(result["payload_json"])
	if !isString {
		return OpenCodeParsed{}, blocked("invalid OpenCode payload_json.")
	}
	if err := strictjson.RejectDuplicateKeys([]byte(payloadJSON)); err != nil {
		return OpenCodeParsed{}, invalid("%v", err)
	}
	if kind := jsonSyntaxKind(payloadJSON); kind != "object" && kind != "array" {
		return OpenCodeParsed{}, blocked("invalid OpenCode payload_json.")
	}
	if _, err := parseJSONScalarDocument([]byte(payloadJSON)); err != nil {
		return OpenCodeParsed{}, blocked("OpenCode payload_json cannot be materialized.")
	}
	toolCallValues := make([]any, 0, len(toolCalls))
	for _, call := range toolCalls {
		toolCallValues = append(toolCallValues, call.Map())
	}
	metadata := map[string]any{
		"session_id":        session,
		"usage":             usage.Map(),
		"reported_cost_usd": cost,
		"cost_source":       "opencode.step_finish.part.cost.sum",
		"observed_model":    nil,
		"observed_effort":   nil,
		"step_count":        int64(len(steps)),
		"tool_calls":        toolCallValues,
	}
	summary, _ := asString(result["summary"])
	return OpenCodeParsed{
		Result:          result,
		Metadata:        metadata,
		SessionID:       session,
		Usage:           usage,
		ReportedCostUSD: cost,
		StepCount:       len(steps),
		ToolCalls:       toolCalls,
		Status:          status,
		Summary:         summary,
		PayloadJSON:     payloadJSON,
	}, nil
}
