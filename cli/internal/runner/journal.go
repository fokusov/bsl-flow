package runner

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The journal wire format is the events.jsonl contract of the PowerShell
// runner (Task.Runner.ps1 Read-BFRunnerJournal / Save-BFRunnerEvent): one
// canonical JSON object per line with the exact field set
//
//	schema_version, event_key, at, task_id, revision, action, status
//
// where event_key is "<task_id>|<revision>|<action>", action is the closed
// event vocabulary, status is the closed status vocabulary and at is a UTC
// ISO-8601 round-trip timestamp. This reader validates exactly that shape; it
// never rewrites history and it keeps duplicate records in document order
// (deduplication happens during replay, like the indexed journal read).

var taskIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var journalFields = []string{
	"schema_version", "event_key", "at", "task_id", "revision", "action", "status",
}

// ParseJournal decodes runner journal bytes into ordered events. A torn final
// line (the document does not end with a newline, the durability proof of the
// append path) is represented as a trailing zero Event plus an error wrapping
// ErrTornEvent: Replay recognizes that incomplete tail, ignores it and marks
// the state for reconciliation. Any complete-but-invalid record is a hard
// error, matching the inspect-before-resume refusal of the source runner.
func ParseJournal(data []byte) ([]Event, error) {
	if !utf8.Valid(data) {
		return nil, blocked("runner journal cannot be read as strict UTF-8; inspect before resuming")
	}
	text := string(data)
	torn := len(text) > 0 && !strings.HasSuffix(text, "\n")
	lines := strings.Split(text, "\n")
	if torn {
		// The source runner refuses a journal whose final record is not
		// newline-terminated before parsing anything; the incomplete tail is
		// never decoded, only reported.
		lines = lines[:len(lines)-1]
	}
	events := make([]Event, 0, 16)
	for _, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		event, err := parseJournalLine(line)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if torn {
		events = append(events, Event{})
		return events, fmt.Errorf("runner journal has an incomplete final record; inspect before resuming: %w", ErrTornEvent)
	}
	return events, nil
}

func parseJournalLine(line string) (Event, error) {
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(strings.NewReader(line))
	if err := decoder.Decode(&raw); err != nil {
		return Event{}, blocked("runner journal contains an invalid record; inspect before resuming: %v", err)
	}
	if decoder.More() {
		return Event{}, blocked("runner journal contains an invalid record; inspect before resuming: trailing data")
	}
	if raw == nil || len(raw) != len(journalFields) {
		return Event{}, blocked("runner journal record must contain exactly the runner_event fields")
	}
	var fields Event
	var eventKey, at, action, status string
	var schema, revision json.RawMessage
	for _, name := range journalFields {
		value, ok := raw[name]
		if !ok {
			return Event{}, blocked("runner journal record is missing field %s", name)
		}
		var err error
		switch name {
		case "schema_version":
			schema = value
		case "revision":
			revision = value
		case "event_key":
			eventKey, err = decodeString(value, "event_key")
		case "at":
			at, err = decodeString(value, "at")
		case "task_id":
			var task string
			if task, err = decodeString(value, "task_id"); err == nil {
				fields.TaskID = TaskID(task)
			}
		case "action":
			action, err = decodeString(value, "action")
		case "status":
			status, err = decodeString(value, "status")
		}
		if err != nil {
			return Event{}, err
		}
	}
	if number, err := decodeInteger(schema, "schema_version"); err != nil || number != 1 {
		return Event{}, blocked("runner journal record has an unsupported schema_version")
	}
	parsedRevision, err := decodeInteger(revision, "revision")
	if err != nil || parsedRevision < -1 {
		return Event{}, blocked("runner journal record has an invalid revision")
	}
	if !taskIDPattern.MatchString(string(fields.TaskID)) {
		return Event{}, blocked("runner journal record task_id must be a canonical lower-case UUID")
	}
	if !knownKind(Kind(action)) {
		return Event{}, blocked("runner journal record has an invalid event action")
	}
	if !knownStatus(status) {
		return Event{}, blocked("runner journal record has an invalid event status")
	}
	if eventKey != fmt.Sprintf("%s|%d|%s", fields.TaskID, parsedRevision, action) {
		return Event{}, blocked("runner journal record has an invalid event identity")
	}
	timestamp, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return Event{}, blocked("runner journal record has an invalid event timestamp")
	}
	fields.Kind = Kind(action)
	fields.Revision = parsedRevision
	fields.Timestamp = timestamp.UTC()
	fields.Payload = Payload{{Key: "status", Value: status}}
	return fields, nil
}

func decodeString(value json.RawMessage, name string) (string, error) {
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return "", blocked("runner journal field %s must be a string", name)
	}
	return text, nil
}

func decodeInteger(value json.RawMessage, name string) (int, error) {
	text := strings.TrimSpace(string(value))
	number, err := strconv.Atoi(text)
	if err != nil {
		return 0, blocked("runner journal field %s must be an integer", name)
	}
	return number, nil
}
