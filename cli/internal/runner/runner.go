// Package runner is the native task queue supervisor. It ports the
// supervision contract of the PowerShell runner
// (global/skills/1c-task/scripts/Task.Runner.ps1, Invoke-BFTaskQueue) in two
// layers. The decision and replay layer computes journals, fairness cursors
// and no-blind-retry decisions purely from replayed state, with queue
// ownership as a value-level single-holder lease. The serve loop (Serve)
// composes that contract with caller-supplied persistence, task-state,
// liveness and dispatch seams: the on-disk defaults persist
// byte-compatible journals, snapshots and queue inputs, hold the exclusive
// runner lock for a whole queue run and re-invoke the trusted CLI binary per
// claimed task. No code path ever falls back to PowerShell.
package runner

import (
	"fmt"
	"math"
	"time"
)

// TaskID identifies a queued task (a canonical lower-case UUID on the wire).
type TaskID string

// AttemptID identifies a controller attempt. The durable journal does not
// record attempt ids (the PowerShell event key is task|revision|action), so a
// journal-only replay leaves attempts unidentified but still tracked as
// running; supervision code that knows the attempt overlays the id.
type AttemptID string

// Kind is the closed runner journal action vocabulary from Task.Runner.ps1.
type Kind string

const (
	// KindRun marks a fresh dispatch decision (Get-BFRunnerDecision action
	// 'run': next action dispatch/accept on a ready task).
	KindRun Kind = "run"
	// KindResumeReadonly marks a resumed read-only attempt after its owning
	// process died in an inspect/spec/code_review/diagnose stage.
	KindResumeReadonly Kind = "resume_readonly"
	// KindObserved is a quiet observation. It never counts as a dispatch and
	// never advances the fairness cursor.
	KindObserved Kind = "observed"
	// KindCompletedStale marks a completed task whose acceptance is no longer
	// fresh; operator review is required.
	KindCompletedStale Kind = "completed_stale"
	// KindRecoveryRequired marks an interrupted modifying attempt; automatic
	// replay is forbidden.
	KindRecoveryRequired Kind = "recovery_required"
	// KindExecutionError marks a failed dispatch whose attempt marker was
	// already persisted. The same task/revision is never dispatched again.
	KindExecutionError Kind = "execution_error"
	// KindError marks a decision-layer failure before any dispatch marker was
	// persisted. Unlike execution_error it does not permanently block the
	// task: the source runner retries the decision on the next poll.
	KindError Kind = "error"
)

func knownKind(kind Kind) bool {
	switch kind {
	case KindRun, KindResumeReadonly, KindObserved, KindCompletedStale,
		KindRecoveryRequired, KindExecutionError, KindError:
		return true
	}
	return false
}

// The closed task status vocabulary shared by journal records and snapshots.
const (
	StatusReady      = "ready"
	StatusRunning    = "running"
	StatusNeedsInput = "needs_input"
	StatusBlocked    = "blocked"
	StatusFailed     = "failed"
	StatusCompleted  = "completed"
	StatusCancelled  = "cancelled"
)

func knownStatus(status string) bool {
	switch status {
	case StatusReady, StatusRunning, StatusNeedsInput, StatusBlocked,
		StatusFailed, StatusCompleted, StatusCancelled:
		return true
	}
	return false
}

// Field is one ordered payload pair. Ordering keeps event payloads
// deterministic for comparison without relying on map iteration.
type Field struct {
	Key   string
	Value string
}

// Payload is an ordered key/value list attached to an event.
type Payload []Field

// Get returns the first value stored under key.
func (p Payload) Get(key string) (string, bool) {
	for _, field := range p {
		if field.Key == key {
			return field.Value, true
		}
	}
	return "", false
}

// Status returns the recorded status payload or "" when absent.
func (p Payload) Status() string {
	status, _ := p.Get("status")
	return status
}

// Reason returns the recorded error summary payload or "" when absent.
func (p Payload) Reason() string {
	reason, _ := p.Get("error")
	return reason
}

// Event is one durable runner journal record.
type Event struct {
	Kind      Kind
	TaskID    TaskID
	AttemptID AttemptID
	// Revision is part of the durable event identity in the source journal
	// (event_key = task|revision|action) and drives no-blind-retry
	// resolution, so it is an explicit field rather than payload data.
	Revision  int
	Timestamp time.Time
	Payload   Payload
}

// Key returns the durable journal identity of the event.
func (e Event) Key() string {
	return fmt.Sprintf("%s|%d|%s", e.TaskID, e.Revision, e.Kind)
}

// Validate checks the closed vocabulary and required event fields. Full wire
// validation (UUID shape, exact field set, event_key binding) happens in
// ParseJournal; Replay only requires structurally usable events.
func (e Event) Validate() error {
	if !knownKind(e.Kind) {
		return invalid("unknown runner event kind %q", string(e.Kind))
	}
	if e.TaskID == "" {
		return invalid("runner event is missing task identity")
	}
	if e.Revision < -1 {
		return invalid("runner event revision must be at least -1")
	}
	if e.Timestamp.IsZero() {
		return invalid("runner event is missing its timestamp")
	}
	if e.Kind == KindObserved && !knownStatus(e.Payload.Status()) {
		return invalid("observed runner event requires a known status payload")
	}
	return nil
}

// Outcome is the last applied journal outcome for one task.
type Outcome struct {
	Kind      Kind
	Status    string
	Revision  int
	AttemptID AttemptID
	Reason    string
	Timestamp time.Time
}

// taskProgress is the replay-internal per-task bookkeeping.
type taskProgress struct {
	// running reports an unresolved dispatch; attempt identifies it when
	// known ("" for journal-only replay).
	running bool
	attempt AttemptID
	outcome Outcome
	// errorRevision is the revision of an unresolved execution_error block
	// (math.MinInt when nothing is blocked).
	errorRevision int
	// recoveryBlocked marks an unresolved recovery_required block.
	recoveryBlocked bool
	// staleRevision is the revision of an unresolved completed_stale block.
	staleRevision int
}

// State is the queue state reconstructed from a journal.
type State struct {
	// Running holds tasks whose last dispatch has not been observed as
	// finished; at most one attempt per task may ever appear here.
	Running map[TaskID]AttemptID
	// PendingOrder is the supervised queue membership in enqueue order. The
	// durable journal has no enqueue record, so membership order is the order
	// of first appearance in the journal (the queue input order of the source
	// runner is validated up front and never changes).
	PendingOrder []TaskID
	// LastOutcome is the last applied event outcome per task.
	LastOutcome map[TaskID]Outcome
	// CursorPosition is the round-robin position after the last dispatch,
	// modulo the queue size. Observations never move it.
	CursorPosition int
	// RecoveryRequired lists tasks with unresolved execution_error or
	// recovery_required blocks, in queue order. They are never auto-retried.
	RecoveryRequired []TaskID
	// NeedsReconciliation marks a torn journal or a recovered ownership
	// lease; dispatch decisions refuse until it is explicitly cleared.
	NeedsReconciliation bool
	// LeaseTTL bounds ownership. Zero means an unexpired exclusive hold, like
	// the source runner's file lock for the whole queue run.
	LeaseTTL time.Duration
	// Owner is the current queue owner, if any.
	Owner *Ownership

	lastDispatch TaskID
	tasks        map[TaskID]*taskProgress
}

// Replay folds journal events into a queue state.
//
// Duplicates: exact repeat records (same kind, task, attempt and timestamp)
// and historical durable-key repeats (same task|revision|action, different
// timestamp, as appended by older runners before saving the cursor) are
// applied once; the durable identity is task|revision|action, so a repeat can
// never re-apply an older transition over a newer one.
//
// A torn final event (an invalid event in the last position, e.g. the tail of
// a truncated line) does not corrupt the state: replay stops before it, marks
// NeedsReconciliation and returns the state together with an error wrapping
// ErrTornEvent. An invalid event anywhere else is a hard error.
func Replay(events []Event) (State, error) {
	state := State{
		Running:     map[TaskID]AttemptID{},
		LastOutcome: map[TaskID]Outcome{},
		tasks:       map[TaskID]*taskProgress{},
	}
	seen := map[string]Event{}
	for index, event := range events {
		if err := event.Validate(); err != nil {
			if index == len(events)-1 {
				state.NeedsReconciliation = true
				state.finalize()
				return state, fmt.Errorf("%w: dropping the final event for %s: %v", ErrTornEvent, event.TaskID, err)
			}
			return State{}, blocked("runner journal contains an invalid record; inspect before resuming: %v", err)
		}
		previous, duplicate := seen[event.Key()]
		if duplicate {
			if previous.AttemptID != event.AttemptID {
				return State{}, conflict("runner journal holds conflicting events for identity %s", event.Key())
			}
			// Last write wins for the recorded timestamp only; the durable
			// transition itself is applied exactly once.
			if !event.Timestamp.Before(previous.Timestamp) {
				seen[event.Key()] = event
				if progress, ok := state.tasks[event.TaskID]; ok && progress.outcome.Kind == event.Kind && progress.outcome.Revision == event.Revision {
					progress.outcome.Timestamp = event.Timestamp
				}
			}
			continue
		}
		seen[event.Key()] = event
		state.apply(event)
	}
	state.finalize()
	return state, nil
}

func (s *State) task(id TaskID) *taskProgress {
	progress, ok := s.tasks[id]
	if ok {
		return progress
	}
	progress = &taskProgress{
		errorRevision: math.MinInt,
		staleRevision: math.MinInt,
	}
	s.tasks[id] = progress
	s.PendingOrder = append(s.PendingOrder, id)
	return progress
}

// apply folds one deduplicated event into the state.
func (s *State) apply(event Event) {
	progress := s.task(event.TaskID)
	// A strictly greater revision means the task was explicitly updated,
	// which resolves revision-scoped blocks (execution_error, completed_stale).
	if event.Revision > progress.errorRevision {
		progress.errorRevision = math.MinInt
	}
	if event.Revision > progress.staleRevision {
		progress.staleRevision = math.MinInt
	}
	switch event.Kind {
	case KindRun, KindResumeReadonly:
		// A dispatch (or an operator-driven resume) supersedes an unresolved
		// recovery block; it never clears an execution_error block at the
		// same revision because that resolution requires a greater revision.
		progress.recoveryBlocked = false
		progress.running = true
		progress.attempt = event.AttemptID
		s.lastDispatch = event.TaskID
	case KindObserved:
		// An observation of a still-running attempt keeps the attempt open;
		// every other observation closes it.
		if event.Payload.Status() != StatusRunning {
			progress.running = false
			progress.attempt = ""
		}
		// Any observed change after a recovery block means the operator or
		// the controller moved the attempt (for example a read-only resume).
		progress.recoveryBlocked = false
	case KindExecutionError:
		progress.running = false
		progress.attempt = ""
		progress.errorRevision = event.Revision
	case KindError:
		progress.running = false
		progress.attempt = ""
	case KindRecoveryRequired:
		progress.running = false
		progress.attempt = ""
		progress.recoveryBlocked = true
	case KindCompletedStale:
		progress.running = false
		progress.attempt = ""
		progress.staleRevision = event.Revision
	}
	progress.outcome = Outcome{
		Kind:      event.Kind,
		Status:    event.Payload.Status(),
		Revision:  event.Revision,
		AttemptID: event.AttemptID,
		Reason:    truncateReason(event.Payload.Reason()),
		Timestamp: event.Timestamp,
	}
}

func (s *State) finalize() {
	s.RecoveryRequired = s.RecoveryRequired[:0]
	for _, id := range s.PendingOrder {
		progress := s.tasks[id]
		s.LastOutcome[id] = progress.outcome
		if progress.running {
			s.Running[id] = progress.attempt
		}
		if progress.errorRevision != math.MinInt || progress.recoveryBlocked {
			s.RecoveryRequired = append(s.RecoveryRequired, id)
		}
	}
	s.CursorPosition = 0
	if s.lastDispatch != "" && len(s.PendingOrder) > 0 {
		for index, id := range s.PendingOrder {
			if id == s.lastDispatch {
				s.CursorPosition = (index + 1) % len(s.PendingOrder)
				break
			}
		}
	}
}
