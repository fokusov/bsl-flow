package runner

import (
	"encoding/json"
)

// QueueInput is the trusted task_queue document the supervisor serves. The
// validation mirrors Assert-BFRunnerQueue (Task.Runner.ps1) exactly: a closed
// field set, schema 1, a canonical queue UUID and unique canonical task UUIDs,
// integer poll_seconds from 1 to 60 and integer max_cycles from 1 to 10000.
type QueueInput struct {
	SchemaVersion int
	QueueID       string
	TaskIDs       []string
	PollSeconds   int
	MaxCycles     int
}

var queueFields = []string{"schema_version", "queue_id", "task_ids", "poll_seconds", "max_cycles"}

// ParseQueueInput decodes and validates the queue document bytes. Like
// Assert-BFFields it names the first missing required field before refusing
// unknown ones.
func ParseQueueInput(data []byte) (QueueInput, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return QueueInput{}, invalid("task_queue must be an object")
	}
	for _, name := range queueFields {
		if _, ok := fields[name]; !ok {
			return QueueInput{}, invalid("task_queue.%s is required", name)
		}
	}
	if len(fields) != len(queueFields) {
		return QueueInput{}, invalid("task_queue must contain exactly the task_queue fields")
	}
	schema, err := decodeInteger(fields["schema_version"], "schema_version")
	if err != nil || schema != 1 {
		return QueueInput{}, invalid("unsupported task_queue schema_version")
	}
	queueID, err := decodeString(fields["queue_id"], "queue_id")
	if err != nil {
		return QueueInput{}, err
	}
	if !taskIDPattern.MatchString(queueID) {
		return QueueInput{}, invalid("identity must be a canonical lower-case UUID")
	}
	var taskIDs []string
	if err := json.Unmarshal(fields["task_ids"], &taskIDs); err != nil || taskIDs == nil || len(taskIDs) == 0 {
		return QueueInput{}, invalid("task_queue.task_ids must be a non-empty array")
	}
	seen := make(map[string]bool, len(taskIDs))
	for _, id := range taskIDs {
		if !taskIDPattern.MatchString(id) {
			return QueueInput{}, invalid("task_queue.task_ids must contain canonical lower-case UUIDs")
		}
		if seen[id] {
			return QueueInput{}, invalid("task_queue.task_ids must be unique")
		}
		seen[id] = true
	}
	poll, err := decodeInteger(fields["poll_seconds"], "poll_seconds")
	if err != nil || poll < 1 || poll > 60 {
		return QueueInput{}, invalid("task_queue.poll_seconds must be from 1 to 60")
	}
	cycles, err := decodeInteger(fields["max_cycles"], "max_cycles")
	if err != nil || cycles < 1 || cycles > 10000 {
		return QueueInput{}, invalid("task_queue.max_cycles must be from 1 to 10000")
	}
	return QueueInput{
		SchemaVersion: schema,
		QueueID:       queueID,
		TaskIDs:       taskIDs,
		PollSeconds:   poll,
		MaxCycles:     cycles,
	}, nil
}

// appendCanonical writes the canonical form of the parsed queue input, the
// exact byte shape Write-BFJson persists under queues/<queue_id>.json and
// Get-BFHash hashes.
func (q QueueInput) appendCanonical(dst []byte) ([]byte, error) {
	dst = append(dst, `{"max_cycles":`...)
	dst = appendCanonicalInt(dst, int64(q.MaxCycles))
	dst = append(dst, `,"poll_seconds":`...)
	dst = appendCanonicalInt(dst, int64(q.PollSeconds))
	var err error
	dst = append(dst, `,"queue_id":`...)
	if dst, err = appendCanonicalStringChecked(dst, q.QueueID, "queue_id"); err != nil {
		return dst, err
	}
	dst = append(dst, `,"schema_version":`...)
	dst = appendCanonicalInt(dst, int64(q.SchemaVersion))
	dst = append(dst, `,"task_ids":[`...)
	for index, id := range q.TaskIDs {
		if index > 0 {
			dst = append(dst, ',')
		}
		if dst, err = appendCanonicalStringChecked(dst, id, "task id"); err != nil {
			return dst, err
		}
	}
	return append(dst, `]}`...), nil
}

// Canonical returns the canonical JSON bytes of the queue input.
func (q QueueInput) Canonical() ([]byte, error) {
	return q.appendCanonical(make([]byte, 0, 128))
}

// Hash returns the lowercase hex SHA-256 of the canonical queue input
// (Get-BFHash parity).
func (q QueueInput) Hash() string {
	encoded, err := q.Canonical()
	if err != nil {
		// AppendCanonicalStringChecked only fails on invalid UTF-8, which the
		// UUID-only validated shape cannot produce.
		panic(err)
	}
	return sha256Hex(encoded)
}
