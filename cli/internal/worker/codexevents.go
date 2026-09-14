package worker

import (
	"encoding/json"
	"math"
	"os"
	"strings"
)

// Port of Read-BFProfiledCodexEvents (ProfiledCodex.ps1:161-198): the strict
// classifier of the codex exec --json stdout stream. Every structural
// violation is a BF_BLOCKED refusal — the stream is session evidence, never
// advisory telemetry.

// CodexSession is the parsed terminal evidence of one Codex worker stream:
// the session identity, the raw usage object (canonical-ready, number
// literals preserved) and the final agent message text.
type CodexSession struct {
	SessionID string
	Usage     map[string]any
	Final     string
}

// TypedUsage projects the validated raw usage counters onto the typed Usage
// shape. Counters beyond int64 range saturate; the canonical receipt keeps the
// raw literals.
func (s CodexSession) TypedUsage() *Usage {
	if s.Usage == nil {
		return nil
	}
	usage := &Usage{
		InputTokens:       counterOf(s.Usage["input_tokens"]),
		CachedInputTokens: counterOf(s.Usage["cached_input_tokens"]),
		OutputTokens:      counterOf(s.Usage["output_tokens"]),
	}
	if value, present := s.Usage["cache_write_input_tokens"]; present {
		counter := counterOf(value)
		usage.CacheWriteInputTokens = &counter
	}
	if value, present := s.Usage["reasoning_output_tokens"]; present {
		counter := counterOf(value)
		usage.ReasoningOutputTokens = &counter
	}
	return usage
}

// ReadProfiledCodexEvents mirrors Read-BFProfiledCodexEvents
// (ProfiledCodex.ps1:161-198) with the exact diagnostics and failure classes:
// stream-shape violations are BF_BLOCKED, the usage field contract is
// BF_INVALID (Assert-BFFields) and the usage counters are BF_BLOCKED.
func ReadProfiledCodexEvents(path string, exitCode int, allowedMcpTools []string, noTools bool) (CodexSession, error) {
	if exitCode != 0 {
		return CodexSession{}, blocked("Codex provider/process failed.")
	}
	resolved, err := workerSafePath(path)
	if err != nil {
		return CodexSession{}, err
	}
	info, statErr := os.Stat(resolved)
	if statErr != nil || info.IsDir() {
		return CodexSession{}, invalid("JSON file does not exist: %s", resolved)
	}
	if info.Size() > 16777216 {
		return CodexSession{}, blocked("Codex JSONL exceeds the output bound.")
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return CodexSession{}, blocked("%v", err)
	}
	allowed := make(map[string]struct{}, len(allowedMcpTools))
	for _, tool := range allowedMcpTools {
		allowed[tool] = struct{}{}
	}
	session := ""
	var usage map[string]any
	started := 0
	completed := 0
	final := ""
	items := make(map[string]struct{})
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		event, err := parseJSONObject([]byte(line))
		if err != nil {
			return CodexSession{}, blocked("malformed Codex JSONL stream.")
		}
		if completed != 0 {
			return CodexSession{}, blocked("Codex emitted events after completion.")
		}
		eventType, _ := asString(event["type"])
		switch eventType {
		case EventThreadStarted:
			if session != "" || started != 0 {
				return CodexSession{}, blocked("duplicate Codex session.")
			}
			session, _ = asString(event["thread_id"])
			if err := asText(session, "session_id"); err != nil {
				return CodexSession{}, err
			}
		case EventTurnStarted:
			started++
			if session == "" || started != 1 {
				return CodexSession{}, blocked("invalid Codex turn start.")
			}
		case EventTurnCompleted:
			completed++
			if started != 1 || completed != 1 {
				return CodexSession{}, blocked("invalid Codex completion.")
			}
			usage, _ = asObject(event["usage"])
		case EventError, EventTurnFailed:
			return CodexSession{}, blocked("Codex reported a provider failure.")
		case EventItemStarted, EventItemUpdated, EventItemCompleted:
			if started != 1 {
				return CodexSession{}, blocked("Codex item outside the turn.")
			}
			item, _ := asObject(event["item"])
			itemType, _ := asString(getValue(item, "type", nil))
			if noTools && itemType != ItemAgentMessage && itemType != ItemReasoning {
				return CodexSession{}, blocked("attached-only Codex critic emitted a tool event.")
			}
			switch itemType {
			case ItemAgentMessage, ItemReasoning, ItemCommandExecution, ItemFileChange, ItemMCPToolCall, ItemTodoList:
			default:
				return CodexSession{}, blocked("unregistered Codex item type.")
			}
			if itemType == ItemMCPToolCall {
				server, _ := asString(getValue(item, "server", nil))
				tool, _ := asString(getValue(item, "tool", nil))
				if server != "unica" {
					return CodexSession{}, blocked("Codex called an unregistered MCP tool.")
				}
				if _, registered := allowed[tool]; !registered {
					return CodexSession{}, blocked("Codex called an unregistered MCP tool.")
				}
			}
			if eventType == EventItemCompleted {
				itemID, _ := asString(getValue(item, "id", nil))
				if _, duplicate := items[itemID]; duplicate || itemID == "" {
					return CodexSession{}, blocked("duplicate Codex completed item.")
				}
				items[itemID] = struct{}{}
				if itemType == ItemAgentMessage {
					final, _ = asString(getValue(item, "text", nil))
				}
			}
		default:
			return CodexSession{}, blocked("unsupported Codex JSONL event.")
		}
	}
	if session == "" || completed != 1 || final == "" {
		return CodexSession{}, blocked("incomplete Codex session evidence.")
	}
	validated, err := assertFields(usage,
		[]string{"input_tokens", "cached_input_tokens", "output_tokens"},
		[]string{"cache_write_input_tokens", "reasoning_output_tokens"},
		"Codex usage")
	if err != nil {
		return CodexSession{}, err
	}
	for _, name := range []string{
		"input_tokens", "cached_input_tokens", "output_tokens",
		"cache_write_input_tokens", "reasoning_output_tokens",
	} {
		value, present := validated[name]
		if !present {
			continue
		}
		if !isUsageCounter(value) {
			return CodexSession{}, blocked("invalid Codex usage counters.")
		}
	}
	return CodexSession{SessionID: session, Usage: validated, Final: final}, nil
}

// isUsageCounter mirrors the ProfiledCodex counter domain
// (ProfiledCodex.ps1:195): a non-negative integral number; booleans, strings
// and fractional values are rejected. The check runs in double precision like
// the PowerShell Floor comparison, so integral values beyond int64 stay valid
// stream evidence.
func isUsageCounter(value any) bool {
	number, isNumber := value.(json.Number)
	if !isNumber {
		return false
	}
	parsed, err := number.Float64()
	if err != nil {
		return false
	}
	return parsed >= 0 && parsed == math.Floor(parsed)
}

// counterOf extracts the int64 value of an already validated counter.
func counterOf(value any) int64 {
	counter, _ := asCounter(value)
	return counter
}
