package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Action is the supervision decision vocabulary of Get-BFRunnerDecision.
type Action string

const (
	// ActionQuiet leaves the task untouched this poll.
	ActionQuiet Action = "quiet"
	// ActionRun claims a ready task whose next controller action is dispatch
	// or accept.
	ActionRun Action = "run"
	// ActionResumeReadonly resumes a dead read-only attempt.
	ActionResumeReadonly Action = "resume_readonly"
	// ActionCompletedStale marks a completed task without fresh acceptance.
	ActionCompletedStale Action = "completed_stale"
	// ActionRecoveryRequired marks an interrupted modifying attempt;
	// automatic replay is forbidden.
	ActionRecoveryRequired Action = "recovery_required"
)

// TaskSnapshot is the set of live task ingredients one supervision decision
// needs: the task state projection of Read-BFTask, the Get-BFNext result and
// the attempt ownership markers of the active attempt.
type TaskSnapshot struct {
	Revision int
	Status   string
	// ActiveAttempt is the active attempt id, or "" when none.
	ActiveAttempt string
	// UnresolvedEffect mirrors state.unresolved_effect.
	UnresolvedEffect bool
	// NextAction is the Get-BFNext action (dispatch, accept, recover,
	// needs_input, blocked, failed, cancelled).
	NextAction string
	// AcceptanceStale reports that a completed task would not survive a
	// fresh acceptance envelope (New-BFEnvelope on the current state).
	AcceptanceStale bool
	// AttemptStage is start.json stage of the active attempt.
	AttemptStage string
	// Controller is the start.json controller_process identity, nil when
	// absent.
	Controller *ProcessIdentity
	// OwnedProcesses are the process.json identities found under the
	// attempt directory.
	OwnedProcesses []ProcessIdentity
}

// TaskSource loads one task's decision ingredients. The production source
// reads the controller-owned task state; tests inject scripted snapshots.
type TaskSource interface {
	Snapshot(ctx context.Context, task TaskID) (TaskSnapshot, error)
}

// ServeOptions configures one queue supervision run. The required seams are
// the task source; store, dispatcher, liveness and clock default to the
// on-disk, self-spawn and OS implementations.
type ServeOptions struct {
	// Project is the absolute supervisor project root.
	Project string
	// Input is the trusted queue input document (already read from disk).
	Input []byte
	// CodexPath optionally pins the sandbox executable for run dispatches.
	CodexPath string
	// RuntimeAuthLine optionally forwards the private single-line JSON
	// credential to every child over stdin.
	RuntimeAuthLine string

	Tasks    TaskSource
	Store    Store
	Dispatch Dispatcher
	Alive    Liveness

	// Now defaults to time.Now; Sleep defaults to a context-aware timer.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	// GitRoot defaults to the Invoke-BFGit rev-parse probe.
	GitRoot func(ctx context.Context, project string) (string, error)

	// MaxOutputBytes bounds the combined child output of one dispatch
	// (SelfDispatch default); zero selects 16777216.
	MaxOutputBytes int64
	// DispatchTimeout optionally bounds one dispatch; zero applies no
	// supervisor-side bound beyond the context, like the in-process source
	// runner.
	DispatchTimeout time.Duration
}

// Decide computes the supervision action for one task snapshot, the exact
// port of Get-BFRunnerDecision.
func Decide(snapshot TaskSnapshot, alive Liveness) Action {
	if snapshot.Status == StatusCancelled || snapshot.UnresolvedEffect {
		return ActionQuiet
	}
	if snapshot.ActiveAttempt != "" {
		if snapshot.Controller != nil && alive.Alive(*snapshot.Controller) {
			return ActionQuiet
		}
		for _, identity := range snapshot.OwnedProcesses {
			if alive.Alive(identity) {
				return ActionQuiet
			}
		}
		if readonlyAttemptStage(snapshot.AttemptStage) {
			return ActionResumeReadonly
		}
		return ActionRecoveryRequired
	}
	if snapshot.Status == StatusCompleted && snapshot.AcceptanceStale {
		return ActionCompletedStale
	}
	if snapshot.Status == StatusReady && (snapshot.NextAction == "dispatch" || snapshot.NextAction == "accept") {
		return ActionRun
	}
	return ActionQuiet
}

func readonlyAttemptStage(stage string) bool {
	switch stage {
	case "inspect", "spec", "code_review", "diagnose":
		return true
	}
	return false
}

func quietObservable(status string) bool {
	switch status {
	case StatusNeedsInput, StatusBlocked, StatusCancelled, StatusCompleted:
		return true
	}
	return false
}

func blockedSummary(action Action) string {
	if action == ActionCompletedStale {
		return "Completed task has no fresh acceptance; operator review is required."
	}
	return "Interrupted modifying attempt requires recovery; automatic replay is forbidden."
}

// serveLoop carries the mutable supervision state of one queue run.
type serveLoop struct {
	opts        ServeOptions
	input       QueueInput
	store       Store
	alive       Liveness
	dispatcher  Dispatcher
	now         func() time.Time
	snapshot    Snapshot
	order       []TaskID
	entries     map[TaskID]*SnapshotTask
	index       map[string]Event
	outWritable io.Writer
	errOut      io.Writer
}

// Serve executes the native task queue supervision loop, the port of
// Invoke-BFTaskQueue: it validates the trusted queue input and the exact Git
// project root, holds the runner lock for the whole run, replays the journal,
// and supervises poll cycles of claim / dispatch / observe decisions until
// max_cycles are exhausted. On completion it prints the queue envelope and
// returns its exit code (0 completed, 10 needs_input, 11 otherwise); hard
// setup failures print the blocked envelope with the BF_* exit codes
// (2 invalid, 3 conflict, 11 blocked, 12 fail, 4 unknown).
func Serve(ctx context.Context, opts ServeOptions, out, errOut io.Writer) int {
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = io.Discard
	}
	input, err := ParseQueueInput(opts.Input)
	if err != nil {
		return serveFailure(out, err)
	}
	if opts.Tasks == nil {
		return serveFailure(out, invalid("ServeOptions.Tasks is required"))
	}
	store := opts.Store
	if store == nil {
		fileStore, err := NewFileStore(opts.Project)
		if err != nil {
			return serveFailure(out, err)
		}
		store = fileStore
	}
	alive := opts.Alive
	if alive == nil {
		alive = OSLiveness{}
	}
	dispatch := opts.Dispatch
	if dispatch == nil {
		dispatch = SelfDispatch{MaxOutputBytes: opts.MaxOutputBytes, Timeout: opts.DispatchTimeout}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = contextSleep
	}
	gitRoot := opts.GitRoot
	if gitRoot == nil {
		gitRoot = defaultGitRoot
	}

	project, err := safeProjectPath(opts.Project)
	if err != nil {
		return serveFailure(out, err)
	}
	if info, err := os.Stat(project); err != nil || !info.IsDir() {
		return serveFailure(out, invalid("project directory missing."))
	}
	root, err := gitRoot(ctx, project)
	if err != nil {
		return serveFailure(out, err)
	}
	// PowerShell string comparison is case-insensitive.
	if !strings.EqualFold(root, project) {
		return serveFailure(out, invalid("supervisor requires the exact Git project root."))
	}
	// Children receive the validated canonical root.
	opts.Project = project
	// The source runner validates every queued task before taking the lock.
	for _, id := range input.TaskIDs {
		if _, err := opts.Tasks.Snapshot(ctx, TaskID(id)); err != nil {
			return serveFailure(out, err)
		}
	}

	release, err := store.Lock()
	if err != nil {
		return serveFailure(out, err)
	}
	defer func() { _ = release() }()

	if err := store.EnsureQueueInput(input); err != nil {
		return serveFailure(out, err)
	}
	snapshot, found, err := store.LoadSnapshot(input)
	if err != nil {
		return serveFailure(out, err)
	}
	if !found {
		snapshot = Snapshot{QueueID: input.QueueID, QueueSHA256: input.Hash()}
	}
	events, err := store.Events()
	if err != nil {
		return serveFailure(out, err)
	}
	index := make(map[string]Event, len(events))
	for _, event := range events {
		index[event.Key()] = event
	}

	loop := &serveLoop{
		opts:        opts,
		input:       input,
		store:       store,
		alive:       alive,
		dispatcher:  dispatch,
		now:         now,
		snapshot:    snapshot,
		entries:     make(map[TaskID]*SnapshotTask, len(snapshot.Tasks)),
		index:       index,
		outWritable: out,
		errOut:      errOut,
	}
	for _, task := range snapshot.Tasks {
		loop.order = append(loop.order, task.TaskID)
		entry := task
		loop.entries[task.TaskID] = &entry
	}

	for cycle := 0; cycle < input.MaxCycles; cycle++ {
		loop.snapshot.Cycle++
		count := len(input.TaskIDs)
		cycleStartCursor := loop.snapshot.Cursor
		for offset := 0; offset < count; offset++ {
			position := (cycleStartCursor + offset) % count
			task := TaskID(input.TaskIDs[position])
			if fatal := loop.serveTask(ctx, task, position, count); fatal != nil {
				return serveFailure(out, fatal)
			}
		}
		loop.snapshot.UpdatedAt = loop.now().UTC()
		if err := loop.saveSnapshot(); err != nil {
			return serveFailure(out, err)
		}
		if cycle < input.MaxCycles-1 {
			if err := sleep(ctx, time.Duration(input.PollSeconds)*time.Second); err != nil {
				break
			}
		}
	}
	loop.materialize()
	_ = json.NewEncoder(out).Encode(loop.envelope())
	return loop.exitCode()
}

// contextSleep waits for the poll interval or the context, whichever ends
// first.
func contextSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// serveTask supervises one task for one poll. A per-task failure is recorded
// as the durable error event; only a failure of that recording itself is
// fatal.
func (l *serveLoop) serveTask(ctx context.Context, task TaskID, position, count int) error {
	attemptKey, state, err := l.runTask(ctx, task, position, count)
	if err == nil {
		return nil
	}
	summary := truncateReason(err.Error())
	revision := -1
	if state != nil {
		revision = state.Revision
	}
	kind := KindError
	lastKey := fmt.Sprintf("%s|%d|error", task, revision)
	if attemptKey != "" {
		kind = KindExecutionError
		lastKey = attemptKey
	}
	// A failed dispatch was already persisted before the controller call;
	// keep that revision/action cursor so polling or a restart cannot retry
	// the identical blocker.
	l.setEntry(SnapshotTask{
		TaskID:   task,
		LastKey:  lastKey,
		Revision: revision,
		Status:   StatusBlocked,
		Action:   "error",
		Error:    summary,
	})
	if err := l.saveEvent(task, revision, kind, StatusBlocked); err != nil {
		return fmt.Errorf("recording the %s runner event failed: %w", kind, err)
	}
	return nil
}

// runTask is one decision application, mirroring the per-task body of
// Invoke-BFTaskQueue. The returned attempt key is non-empty when the failure
// happened after the dispatch marker was persisted.
func (l *serveLoop) runTask(ctx context.Context, task TaskID, position, count int) (string, *TaskSnapshot, error) {
	snapshot, err := l.opts.Tasks.Snapshot(ctx, task)
	if err != nil {
		return "", nil, err
	}
	state := &snapshot
	action := Decide(snapshot, l.alive)
	key := fmt.Sprintf("%s|%d|%s", task, snapshot.Revision, action)
	previous := l.entries[task]

	// An older runner may have saved a quiet snapshot without its
	// notification; reconcile against the journal on every poll.
	if action == ActionQuiet && quietObservable(snapshot.Status) {
		if err := l.saveEvent(task, snapshot.Revision, KindObserved, snapshot.Status); err != nil {
			return "", state, err
		}
	}
	// The durable error event may be newer than the last snapshot; do not
	// repeat that same failed dispatch after a restart.
	if (action == ActionRun || action == ActionResumeReadonly) && l.indexKey(task, snapshot.Revision, KindExecutionError) {
		if previous == nil || previous.LastKey != key || previous.Action != "error" {
			l.setEntry(SnapshotTask{
				TaskID:   task,
				LastKey:  key,
				Revision: snapshot.Revision,
				Status:   StatusBlocked,
				Action:   "error",
				Error:    "Recorded controller execution error requires an explicit task update.",
			})
		}
		return "", state, nil
	}
	// An unfinished dispatch marker can precede attempt creation; reconcile
	// it with the fresh decision.
	if (action == ActionRun || action == ActionResumeReadonly) &&
		(previous == nil || previous.LastKey != key || previous.Action == string(ActionRun) || previous.Action == string(ActionResumeReadonly)) {
		attemptKey := key
		l.setEntry(SnapshotTask{
			TaskID:   task,
			LastKey:  key,
			Revision: snapshot.Revision,
			Status:   snapshot.Status,
			Action:   string(action),
		})
		if err := l.saveEvent(task, snapshot.Revision, Kind(action), snapshot.Status); err != nil {
			return attemptKey, state, err
		}
		l.snapshot.Cursor = (position + 1) % count
		l.snapshot.UpdatedAt = l.now().UTC()
		if err := l.saveSnapshot(); err != nil {
			return attemptKey, state, err
		}
		if action == ActionResumeReadonly {
			err = l.dispatchTask(ctx, task, ActionResumeReadonly)
		} else {
			err = l.dispatchTask(ctx, task, ActionRun)
		}
		if err != nil {
			return attemptKey, state, err
		}
		// The action itself may have advanced several core stages; observe
		// the terminal/current revision before sleeping.
		after, err := l.opts.Tasks.Snapshot(ctx, task)
		if err != nil {
			return attemptKey, state, err
		}
		l.setEntry(SnapshotTask{
			TaskID:   task,
			LastKey:  fmt.Sprintf("%s|%d|quiet", task, after.Revision),
			Revision: after.Revision,
			Status:   after.Status,
			Action:   "quiet",
		})
		if err := l.saveEvent(task, after.Revision, KindObserved, after.Status); err != nil {
			return attemptKey, state, err
		}
		return "", state, nil
	}
	if previous == nil ||
		(previous.LastKey != key && (previous.Revision != snapshot.Revision || previous.Status != snapshot.Status || previous.Action != string(action))) {
		entry := SnapshotTask{
			TaskID:   task,
			LastKey:  key,
			Revision: snapshot.Revision,
			Status:   snapshot.Status,
			Action:   string(action),
		}
		if escalated := action == ActionCompletedStale || action == ActionRecoveryRequired; escalated {
			entry.Status = StatusBlocked
			entry.Error = blockedSummary(action)
			l.setEntry(entry)
			if err := l.saveEvent(task, snapshot.Revision, Kind(action), StatusBlocked); err != nil {
				return "", state, err
			}
			return "", state, nil
		}
		l.setEntry(entry)
	}
	return "", state, nil
}

func (l *serveLoop) dispatchTask(ctx context.Context, task TaskID, action Action) error {
	req := DispatchRequest{
		Project:         l.opts.Project,
		Task:            task,
		Action:          action,
		RuntimeAuthLine: l.opts.RuntimeAuthLine,
	}
	if action == ActionRun {
		req.CodexPath = l.opts.CodexPath
	}
	return l.dispatcher.Dispatch(ctx, req, l.outWritable, l.errOut)
}

func (l *serveLoop) indexKey(task TaskID, revision int, kind Kind) bool {
	_, ok := l.index[fmt.Sprintf("%s|%d|%s", task, revision, kind)]
	return ok
}

// saveEvent appends one journal record unless its durable identity already
// exists, mirroring Save-BFRunnerEvent including the 256-key memory window.
func (l *serveLoop) saveEvent(task TaskID, revision int, kind Kind, status string) error {
	key := fmt.Sprintf("%s|%d|%s", task, revision, kind)
	if _, exists := l.index[key]; exists {
		return nil
	}
	event := Event{
		Kind:      kind,
		TaskID:    task,
		Revision:  revision,
		Timestamp: l.now().UTC(),
		Payload:   Payload{{Key: "status", Value: status}},
	}
	if err := l.store.AppendEvent(event); err != nil {
		return err
	}
	l.index[key] = event
	l.snapshot.EventKeys = append(l.snapshot.EventKeys, key)
	if len(l.snapshot.EventKeys) > eventKeyCapacity {
		l.snapshot.EventKeys = l.snapshot.EventKeys[len(l.snapshot.EventKeys)-eventKeyCapacity:]
	}
	return nil
}

func (l *serveLoop) setEntry(entry SnapshotTask) {
	if existing, ok := l.entries[entry.TaskID]; ok {
		*existing = entry
		return
	}
	allocated := entry
	l.entries[entry.TaskID] = &allocated
	l.order = append(l.order, entry.TaskID)
}

func (l *serveLoop) materialize() {
	tasks := make([]SnapshotTask, 0, len(l.order))
	for _, id := range l.order {
		tasks = append(tasks, *l.entries[id])
	}
	l.snapshot.Tasks = tasks
}

func (l *serveLoop) saveSnapshot() error {
	l.materialize()
	return l.store.SaveSnapshot(l.snapshot)
}

// envelope builds the console envelope of the Serve action.
func (l *serveLoop) envelope() serveEnvelopeDocument {
	l.materialize()
	tasks := make(map[string]serveEnvelopeTask, len(l.snapshot.Tasks))
	for _, task := range l.snapshot.Tasks {
		entry := serveEnvelopeTask{
			LastKey:  task.LastKey,
			Revision: task.Revision,
			Status:   task.Status,
			Action:   task.Action,
		}
		if task.Error != "" {
			summary := task.Error
			entry.Error = &summary
		}
		tasks[string(task.TaskID)] = entry
	}
	var updatedAt *string
	if !l.snapshot.UpdatedAt.IsZero() {
		stamp := snapshotTimestamp(l.snapshot.UpdatedAt)
		updatedAt = &stamp
	}
	eventKeys := l.snapshot.EventKeys
	if eventKeys == nil {
		eventKeys = []string{}
	}
	return serveEnvelopeDocument{
		SchemaVersion: 1,
		QueueID:       l.snapshot.QueueID,
		Status:        l.queueStatus(),
		Snapshot: serveEnvelopeSnapshot{
			SchemaVersion: 1,
			QueueID:       l.snapshot.QueueID,
			QueueSHA256:   l.snapshot.QueueSHA256,
			Cycle:         l.snapshot.Cycle,
			Cursor:        l.snapshot.Cursor,
			Tasks:         tasks,
			EventKeys:     eventKeys,
			UpdatedAt:     updatedAt,
		},
	}
}

// queueStatus aggregates the per-task statuses exactly like the Serve action
// of the PowerShell entrypoint.
func (l *serveLoop) queueStatus() string {
	blocked, needsInput, waiting := false, false, false
	for _, task := range l.snapshot.Tasks {
		switch task.Status {
		case StatusBlocked, StatusFailed:
			blocked = true
		case StatusNeedsInput:
			needsInput = true
		case StatusCompleted:
		default:
			waiting = true
		}
	}
	switch {
	case blocked:
		return "blocked"
	case needsInput:
		return StatusNeedsInput
	case !waiting:
		// No task is anything but completed (an untouched empty queue
		// included, matching the PowerShell aggregation).
		return StatusCompleted
	default:
		return "waiting"
	}
}

func (l *serveLoop) exitCode() int {
	switch l.queueStatus() {
	case StatusCompleted:
		return 0
	case StatusNeedsInput:
		return 10
	default:
		return 11
	}
}

type serveEnvelopeDocument struct {
	SchemaVersion int                   `json:"schema_version"`
	QueueID       string                `json:"queue_id"`
	Status        string                `json:"status"`
	Snapshot      serveEnvelopeSnapshot `json:"snapshot"`
}

type serveEnvelopeSnapshot struct {
	SchemaVersion int                          `json:"schema_version"`
	QueueID       string                       `json:"queue_id"`
	QueueSHA256   string                       `json:"queue_sha256"`
	Cycle         int                          `json:"cycle"`
	Cursor        int                          `json:"cursor"`
	Tasks         map[string]serveEnvelopeTask `json:"tasks"`
	EventKeys     []string                     `json:"event_keys"`
	UpdatedAt     *string                      `json:"updated_at"`
}

type serveEnvelopeTask struct {
	LastKey  string  `json:"last_key"`
	Revision int     `json:"revision"`
	Status   string  `json:"status"`
	Action   string  `json:"action"`
	Error    *string `json:"error,omitempty"`
}

// serveFailure prints the blocked envelope of a hard Serve failure and maps
// the BF_* error class to the entrypoint exit codes.
func serveFailure(out io.Writer, err error) int {
	code := 4
	status := "blocked"
	kind := ""
	message := err.Error()
	var classError *ErrorClass
	switch {
	case errors.As(err, &classError):
		kind = classError.Kind
		message = classError.Message
		switch kind {
		case "BF_INVALID":
			code = 2
		case "BF_CONFLICT":
			code = 3
		case "BF_BLOCKED":
			code = 11
		case "BF_FAIL":
			code = 12
			status = "failed"
		}
	case errors.Is(err, ErrTornEvent):
		kind = "BF_BLOCKED"
		code = 11
	default:
		// The entrypoint catch classifies by message prefix; seam-supplied
		// errors from the controller layer keep that contract.
		for _, prefix := range []string{"BF_INVALID:", "BF_CONFLICT:", "BF_BLOCKED:", "BF_FAIL:"} {
			if !strings.HasPrefix(message, prefix) {
				continue
			}
			kind = strings.TrimSuffix(prefix, ":")
			message = strings.TrimSpace(strings.TrimPrefix(message, prefix))
			switch kind {
			case "BF_INVALID":
				code = 2
			case "BF_CONFLICT":
				code = 3
			case "BF_BLOCKED":
				code = 11
			case "BF_FAIL":
				code = 12
				status = "failed"
			}
			break
		}
	}
	reason := message
	if kind != "" {
		reason = kind + ": " + message
	}
	_ = json.NewEncoder(out).Encode(map[string]any{
		"schema_version": 1,
		"task_id":        nil,
		"revision":       nil,
		"status":         status,
		"stage":          nil,
		"next_action":    "inspect_blocker",
		"blockers":       []string{reason},
		"evidence_refs":  []string{},
	})
	return code
}

var providerPathPattern = regexp.MustCompile(`^[^:]+::`)

// safeProjectPath is the runner-local port of Assert-BFSafePath for the
// supervisor project root: an ordinary absolute path without device or
// provider qualifiers, alternate data streams (Windows) or reparse points on
// existing components.
func safeProjectPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return "", invalid("Path must be an absolute filesystem path.")
	}
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) || providerPathPattern.MatchString(path) {
		return "", invalid("Device and provider-qualified paths are not allowed.")
	}
	full := filepath.Clean(path)
	remaining := strings.TrimPrefix(full, filepath.VolumeName(full))
	if strings.ContainsRune(remaining, ':') {
		return "", invalid("Alternate data stream paths are not allowed.")
	}
	cursor := full
	for cursor != "" {
		if info, err := os.Lstat(cursor); err == nil && (info.Mode()&fs.ModeSymlink != 0 || info.Mode()&fs.ModeIrregular != 0) {
			return "", invalid("Path contains a reparse point: %s", cursor)
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			break
		}
		cursor = parent
	}
	return full, nil
}
