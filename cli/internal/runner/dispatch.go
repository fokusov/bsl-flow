package runner

import (
	"fmt"
)

// Cursor is the durable round-robin position over State.PendingOrder. The
// source runner persists it in the queue snapshot and advances it only after a
// dispatch, so a restarted supervisor resumes after the last dispatched task.
type Cursor struct {
	Position int
}

// Next selects the next dispatchable task in queue order starting just after
// the cursor, mirroring the round-robin scan of Invoke-BFTaskQueue:
//
//	for offset in 0..count-1: index = (cursor + offset) % count
//
// Tasks with a running attempt, unresolved execution_error, recovery_required
// and terminal or waiting observations are skipped; the first dispatchable
// task wins. The returned cursor points past it. When nothing is dispatchable
// ErrQuiet is returned with the input cursor unchanged, matching the quiet
// observation cycle of the source runner.
func Next(cur Cursor, state State) (TaskID, Cursor, error) {
	if state.NeedsReconciliation {
		return "", cur, blocked("runner state needs reconciliation before dispatch")
	}
	count := len(state.PendingOrder)
	if count == 0 {
		return "", cur, invalid("runner queue is empty")
	}
	position := ((cur.Position % count) + count) % count
	for offset := 0; offset < count; offset++ {
		index := (position + offset) % count
		task := state.PendingOrder[index]
		if ok, _ := ShouldDispatch(state, task); ok {
			return task, Cursor{Position: (index + 1) % count}, nil
		}
	}
	return "", cur, fmt.Errorf("no dispatchable task in queue: %w", ErrQuiet)
}

// ShouldDispatch reports whether the task may be dispatched now and, when it
// may not, a human-readable reason. It encodes the no-blind-retry contract of
// the source runner:
//
//   - a task with an unresolved dispatch never gets a second attempt;
//   - a recorded execution_error at the current revision is never repeated;
//     only an explicit task update (a greater revision) unblocks it;
//   - recovery_required and completed_stale stay blocked until operator
//     action; a decision-layer error is transient and may be retried.
func ShouldDispatch(state State, task TaskID) (bool, string) {
	if state.NeedsReconciliation {
		return false, "runner state needs reconciliation before dispatch"
	}
	outcome, tracked := state.LastOutcome[task]
	if !tracked {
		return false, "task is not part of the supervised queue"
	}
	if attempt, running := state.Running[task]; running {
		if attempt == "" {
			return false, "a dispatched attempt is still unresolved"
		}
		return false, fmt.Sprintf("attempt %s is still running", attempt)
	}
	switch outcome.Kind {
	case KindExecutionError:
		return false, fmt.Sprintf("recorded execution_error at revision %d requires an explicit task update", outcome.Revision)
	case KindRecoveryRequired:
		return false, "interrupted attempt requires recovery; automatic replay is forbidden"
	case KindCompletedStale:
		return false, "completed task has no fresh acceptance; operator review is required"
	case KindObserved:
		switch outcome.Status {
		case StatusReady:
			return true, ""
		case StatusRunning:
			return false, "attempt is still running"
		case StatusNeedsInput:
			return false, "task is waiting for input"
		case StatusBlocked:
			return false, "task is blocked"
		case StatusCompleted:
			return false, "task is completed"
		case StatusCancelled:
			return false, "task is cancelled"
		case StatusFailed:
			return false, "task failed"
		}
		return false, fmt.Sprintf("task has an unknown observation status %q", outcome.Status)
	case KindRun, KindResumeReadonly:
		return false, "dispatch outcome is unknown"
	case KindError:
		// Pre-dispatch decision failures are retried on the next poll, as in
		// the source runner's restart path.
		return true, ""
	}
	return false, fmt.Sprintf("task has an unknown outcome kind %q", string(outcome.Kind))
}
