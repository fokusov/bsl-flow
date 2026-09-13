package worker

import (
	"fmt"
)

// Closed vocabulary of the Codex worker JSONL stream (`codex exec --json`
// stdout). Sources: Invoke-BFCodexWorker (Codex.ps1:79-85) and
// Read-BFProfiledCodexEvents (ProfiledCodex.ps1:171-190).
const (
	EventThreadStarted = "thread.started" // carries thread_id
	EventTurnStarted   = "turn.started"
	EventTurnCompleted = "turn.completed" // carries usage
	EventItemStarted   = "item.started"   // carries item
	EventItemUpdated   = "item.updated"   // carries item
	EventItemCompleted = "item.completed" // carries item
	EventError         = "error"          // provider failure marker
	EventTurnFailed    = "turn.failed"    // provider failure marker
)

// Closed vocabulary of item.type inside item.* events
// (ProfiledCodex.ps1:180).
const (
	ItemAgentMessage     = "agent_message" // final message text
	ItemReasoning        = "reasoning"
	ItemCommandExecution = "command_execution"
	ItemFileChange       = "file_change"
	ItemMCPToolCall      = "mcp_tool_call" // carries server + tool
	ItemTodoList         = "todo_list"
)

// Usage mirrors the turn.completed usage object. input_tokens,
// cached_input_tokens and output_tokens are required; the two remaining
// counters are optional and nil when the host omitted them
// (ProfiledCodex.ps1:192-196).
type Usage struct {
	InputTokens           int64
	CachedInputTokens     int64
	OutputTokens          int64
	CacheWriteInputTokens *int64
	ReasoningOutputTokens *int64
}

// Item mirrors the item payload of item.started/item.updated/item.completed.
type Item struct {
	ID     string
	Type   string
	Text   string // agent_message
	Server string // mcp_tool_call
	Tool   string // mcp_tool_call
}

// KnownItemType reports whether the item type is inside the closed registered
// vocabulary. The strict profiled reader blocks unregistered item types
// ("unregistered Codex item type", ProfiledCodex.ps1:180); the base adapter
// never inspects items.
func (i Item) KnownItemType() bool {
	switch i.Type {
	case ItemAgentMessage, ItemReasoning, ItemCommandExecution, ItemFileChange, ItemMCPToolCall, ItemTodoList:
		return true
	}
	return false
}

// Event is one classified worker stream record.
type Event struct {
	Type     string
	ThreadID string // thread.started
	Usage    *Usage // turn.completed
	Item     *Item  // item.started/item.updated/item.completed
}

// KnownKind reports whether the event type is inside the closed stream
// vocabulary. ParseEvent tolerates unknown kinds for forward compatibility,
// mirroring the base adapter, which ignores unrecognized event types
// (Codex.ps1:79-85 has no default branch); the strict profiled reader rejects
// them ("unsupported Codex JSONL event", ProfiledCodex.ps1:188).
func (e Event) KnownKind() bool {
	switch e.Type {
	case EventThreadStarted, EventTurnStarted, EventTurnCompleted, EventItemStarted, EventItemUpdated, EventItemCompleted, EventError, EventTurnFailed:
		return true
	}
	return false
}

// ParseEvent parses one worker stream JSONL line.
//
// Leniency mirrors the PowerShell adapters: unknown JSON fields are tolerated
// (no DisallowUnknownFields), duplicate keys and trailing data are rejected,
// and unknown event or item kinds are tolerated and reported through
// KnownKind/KnownItemType so strict consumers can mirror the profiled route.
// Structure of known kinds is validated where the PowerShell readers are
// strict:
//
//   - thread.started requires a non-empty thread_id
//     (Assert-BFText, ProfiledCodex.ps1:172);
//   - turn.completed requires a usage object whose three mandatory counters
//     are present non-negative integers, with optional counters validated
//     when present (ProfiledCodex.ps1:192-196; the base adapter also accesses
//     $event.usage unconditionally, Codex.ps1:83);
//   - item.* requires an item object with a non-empty type; item.completed
//     additionally requires a non-empty item id (ProfiledCodex.ps1:184 derefs
//     it under StrictMode for the duplicate-item set), a known-type
//     agent_message requires non-empty text (ProfiledCodex.ps1:185), and a
//     known-type mcp_tool_call requires non-empty server and tool
//     (ProfiledCodex.ps1:181).
func ParseEvent(line []byte) (Event, error) {
	object, err := parseJSONObject(line)
	if err != nil {
		return Event{}, fmt.Errorf("malformed worker stream line: %w", err)
	}
	eventType, _ := asString(object["type"])
	if eventType == "" {
		return Event{}, invalid("worker stream event has no type")
	}
	event := Event{Type: eventType}
	switch eventType {
	case EventThreadStarted:
		threadID, _ := asString(object["thread_id"])
		event.ThreadID = threadID
		if err := asText(threadID, "session_id"); err != nil {
			return Event{}, err
		}
	case EventTurnCompleted:
		usage, err := parseUsage(object["usage"])
		if err != nil {
			return Event{}, err
		}
		event.Usage = usage
	case EventItemStarted, EventItemUpdated, EventItemCompleted:
		item, err := parseItem(object["item"], eventType == EventItemCompleted)
		if err != nil {
			return Event{}, err
		}
		event.Item = item
	}
	return event, nil
}

func parseUsage(value any) (*Usage, error) {
	object, ok := asObject(value)
	if !ok {
		return nil, invalid("Codex usage is missing")
	}
	usage := &Usage{}
	required := []struct {
		key   string
		value *int64
	}{
		{"input_tokens", &usage.InputTokens},
		{"cached_input_tokens", &usage.CachedInputTokens},
		{"output_tokens", &usage.OutputTokens},
	}
	for _, counter := range required {
		raw, present := object[counter.key]
		if !present {
			return nil, invalid("Codex usage.%s is required", counter.key)
		}
		value, valid := asCounter(raw)
		if !valid {
			return nil, invalid("invalid Codex usage counters")
		}
		*counter.value = value
	}
	optional := []struct {
		key   string
		value **int64
	}{
		{"cache_write_input_tokens", &usage.CacheWriteInputTokens},
		{"reasoning_output_tokens", &usage.ReasoningOutputTokens},
	}
	for _, counter := range optional {
		raw, present := object[counter.key]
		if !present {
			continue
		}
		value, valid := asCounter(raw)
		if !valid {
			return nil, invalid("invalid Codex usage counters")
		}
		*counter.value = &value
	}
	return usage, nil
}

func parseItem(value any, completed bool) (*Item, error) {
	object, ok := asObject(value)
	if !ok {
		return nil, invalid("worker stream item is missing")
	}
	item := &Item{}
	item.ID, _ = asString(object["id"])
	item.Type, _ = asString(object["type"])
	item.Text, _ = asString(object["text"])
	item.Server, _ = asString(object["server"])
	item.Tool, _ = asString(object["tool"])
	if item.Type == "" {
		return nil, invalid("worker stream item has no type")
	}
	if completed && item.ID == "" {
		return nil, invalid("completed worker stream item has no id")
	}
	if item.Type == ItemMCPToolCall {
		if err := asText(item.Server, "MCP tool server"); err != nil {
			return nil, err
		}
		if err := asText(item.Tool, "MCP tool name"); err != nil {
			return nil, err
		}
	}
	if completed && item.Type == ItemAgentMessage && item.Text == "" {
		return nil, invalid("completed agent_message has no text")
	}
	return item, nil
}
