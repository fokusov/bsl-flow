package runner

import (
	"encoding/json"
	"strings"
	"time"
)

// Snapshot is the durable queue supervision state the runner persists as
// queue-<queue_id>-snapshot.json (Get-BFRunnerSnapshot / Save-BFRunnerJson).
// The on-disk form is the canonical JSON document
//
//	{"cycle":N,"cursor":N,"event_keys":[...],"queue_id":"...",
//	 "queue_sha256":"...","schema_version":1,"tasks":{...},"updated_at":...}
//
// byte-compatible with the PowerShell runner; Tasks order is the first-touch
// order (the canonical writer sorts object keys, so order only matters for
// the console envelope).
type Snapshot struct {
	QueueID     string
	QueueSHA256 string
	Cycle       int
	Cursor      int
	Tasks       []SnapshotTask
	EventKeys   []string
	UpdatedAt   time.Time // zero renders and persists as null
}

// SnapshotTask is one per-task entry of the snapshot tasks map. Error is the
// optional blocked/error summary; the empty string means the field is absent.
type SnapshotTask struct {
	TaskID   TaskID
	LastKey  string
	Revision int
	Status   string
	Action   string
	Error    string
}

var snapshotFields = []string{
	"schema_version", "queue_id", "queue_sha256", "cycle", "cursor", "tasks", "event_keys", "updated_at",
}

var snapshotTaskFields = []string{"last_key", "revision", "status", "action"}

const eventKeyCapacity = 256

// snapshotTimestamp renders like [DateTime]::UtcNow.ToString('o'): seven
// fractional digits and an explicit Z, the identity format of every runner
// timestamp on disk.
func snapshotTimestamp(moment time.Time) string {
	return moment.UTC().Format("2006-01-02T15:04:05.0000000Z")
}

// appendSnapshotCanonical writes the snapshot document in the exact canonical
// byte form of Save-BFRunnerJson (Write-BFJson writes canonical JSON without
// a trailing newline).
func appendSnapshotCanonical(dst []byte, snapshot Snapshot) ([]byte, error) {
	var err error
	dst = append(dst, `{"cycle":`...)
	dst = appendCanonicalInt(dst, int64(snapshot.Cycle))
	dst = append(dst, `,"cursor":`...)
	dst = appendCanonicalInt(dst, int64(snapshot.Cursor))
	dst = append(dst, `,"event_keys":[`...)
	for index, key := range snapshot.EventKeys {
		if index > 0 {
			dst = append(dst, ',')
		}
		if dst, err = appendCanonicalStringChecked(dst, key, "event key"); err != nil {
			return dst, err
		}
	}
	dst = append(dst, `],"queue_id":`...)
	if dst, err = appendCanonicalStringChecked(dst, snapshot.QueueID, "queue_id"); err != nil {
		return dst, err
	}
	dst = append(dst, `,"queue_sha256":`...)
	if dst, err = appendCanonicalStringChecked(dst, snapshot.QueueSHA256, "queue_sha256"); err != nil {
		return dst, err
	}
	dst = append(dst, `,"schema_version":1,"tasks":{`...)
	for index, task := range snapshot.Tasks {
		if index > 0 {
			dst = append(dst, ',')
		}
		if dst, err = appendCanonicalStringChecked(dst, string(task.TaskID), "snapshot task key"); err != nil {
			return dst, err
		}
		dst = append(dst, `:{`...)
		dst = append(dst, `"action":`...)
		if dst, err = appendCanonicalStringChecked(dst, task.Action, "snapshot task action"); err != nil {
			return dst, err
		}
		if task.Error != "" {
			dst = append(dst, `,"error":`...)
			if dst, err = appendCanonicalStringChecked(dst, task.Error, "snapshot task error"); err != nil {
				return dst, err
			}
		}
		dst = append(dst, `,"last_key":`...)
		if dst, err = appendCanonicalStringChecked(dst, task.LastKey, "snapshot task last_key"); err != nil {
			return dst, err
		}
		dst = append(dst, `,"revision":`...)
		dst = appendCanonicalInt(dst, int64(task.Revision))
		dst = append(dst, `,"status":`...)
		if dst, err = appendCanonicalStringChecked(dst, task.Status, "snapshot task status"); err != nil {
			return dst, err
		}
		dst = append(dst, '}')
	}
	dst = append(dst, `},"updated_at":`...)
	if snapshot.UpdatedAt.IsZero() {
		dst = append(dst, "null"...)
	} else {
		if dst, err = appendCanonicalStringChecked(dst, snapshotTimestamp(snapshot.UpdatedAt), "updated_at"); err != nil {
			return dst, err
		}
	}
	return append(dst, '}'), nil
}

// parseSnapshot decodes and strictly validates one snapshot document. The
// PowerShell reader validates the top-level field set only; unknown or
// mistyped task entries are additionally rejected here because a malformed
// snapshot must fail closed instead of supervising from guessed state.
func parseSnapshot(data []byte, expected QueueInput) (Snapshot, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return Snapshot{}, invalid("runner snapshot must be an object")
	}
	for _, name := range snapshotFields {
		if _, ok := fields[name]; !ok {
			return Snapshot{}, invalid("runner snapshot is missing field %s", name)
		}
	}
	if len(fields) != len(snapshotFields) {
		return Snapshot{}, invalid("runner snapshot must contain exactly the runner_snapshot fields")
	}
	schema, err := decodeInteger(fields["schema_version"], "schema_version")
	if err != nil || schema != 1 {
		return Snapshot{}, invalid("runner snapshot has an unsupported schema_version")
	}
	queueID, err := decodeString(fields["queue_id"], "queue_id")
	if err != nil {
		return Snapshot{}, err
	}
	queueHash, err := decodeString(fields["queue_sha256"], "queue_sha256")
	if err != nil {
		return Snapshot{}, err
	}
	if queueID != expected.QueueID || queueHash != expected.Hash() {
		return Snapshot{}, conflict("queue_id belongs to a different immutable queue input.")
	}
	cycle, err := decodeInteger(fields["cycle"], "cycle")
	if err != nil || cycle < 0 {
		return Snapshot{}, invalid("runner snapshot has an invalid cycle")
	}
	cursor, err := decodeInteger(fields["cursor"], "cursor")
	if err != nil || cursor < 0 {
		return Snapshot{}, invalid("runner snapshot has an invalid cursor")
	}
	var taskFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["tasks"], &taskFields); err != nil || taskFields == nil {
		return Snapshot{}, invalid("runner snapshot tasks must be an object")
	}
	tasks := make([]SnapshotTask, 0, len(taskFields))
	for id, entry := range taskFields {
		if !taskIDPattern.MatchString(id) {
			return Snapshot{}, invalid("runner snapshot task key must be a canonical lower-case UUID")
		}
		task, err := parseSnapshotTask(id, entry)
		if err != nil {
			return Snapshot{}, err
		}
		tasks = append(tasks, task)
	}
	// The canonical writer sorts object keys, so document order is the
	// deterministic load order.
	sortSnapshotTasks(tasks)
	var eventKeys []string
	if raw := strings.TrimSpace(string(fields["event_keys"])); raw != "null" {
		if err := json.Unmarshal(fields["event_keys"], &eventKeys); err != nil {
			return Snapshot{}, invalid("runner snapshot event_keys must be an array of strings")
		}
		for _, key := range eventKeys {
			if strings.TrimSpace(key) == "" {
				return Snapshot{}, invalid("runner snapshot event_keys must be non-empty strings")
			}
		}
	}
	snapshot := Snapshot{
		QueueID:     queueID,
		QueueSHA256: queueHash,
		Cycle:       cycle,
		Cursor:      cursor,
		Tasks:       tasks,
		EventKeys:   eventKeys,
	}
	if raw := strings.TrimSpace(string(fields["updated_at"])); raw != "null" {
		text, err := decodeString(fields["updated_at"], "updated_at")
		if err != nil {
			return Snapshot{}, err
		}
		moment, err := time.Parse(time.RFC3339, text)
		if err != nil {
			return Snapshot{}, invalid("runner snapshot has an invalid updated_at timestamp")
		}
		snapshot.UpdatedAt = moment.UTC()
	}
	return snapshot, nil
}

func parseSnapshotTask(id string, entry json.RawMessage) (SnapshotTask, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(entry, &fields); err != nil || fields == nil {
		return SnapshotTask{}, invalid("runner snapshot task %s must be an object", id)
	}
	for _, name := range snapshotTaskFields {
		if _, ok := fields[name]; !ok {
			return SnapshotTask{}, invalid("runner snapshot task %s is missing field %s", id, name)
		}
	}
	if len(fields) > len(snapshotTaskFields)+1 {
		return SnapshotTask{}, invalid("runner snapshot task %s holds unknown fields", id)
	}
	task := SnapshotTask{TaskID: TaskID(id)}
	var err error
	if task.LastKey, err = decodeString(fields["last_key"], "last_key"); err != nil {
		return task, err
	}
	if task.Status, err = decodeString(fields["status"], "status"); err != nil {
		return task, err
	}
	if task.Action, err = decodeString(fields["action"], "action"); err != nil {
		return task, err
	}
	if task.Revision, err = decodeInteger(fields["revision"], "revision"); err != nil || task.Revision < -1 {
		return task, invalid("runner snapshot task %s has an invalid revision", id)
	}
	if raw, ok := fields["error"]; ok {
		if task.Error, err = decodeString(raw, "error"); err != nil {
			return task, err
		}
	}
	if task.LastKey == "" || task.Status == "" || task.Action == "" {
		return task, invalid("runner snapshot task %s holds empty required fields", id)
	}
	return task, nil
}

func sortSnapshotTasks(tasks []SnapshotTask) {
	for i := 1; i < len(tasks); i++ {
		for j := i; j > 0 && tasks[j].TaskID < tasks[j-1].TaskID; j-- {
			tasks[j], tasks[j-1] = tasks[j-1], tasks[j]
		}
	}
}
