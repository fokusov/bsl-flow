package repository

import (
	"context"
	"path/filepath"
	"strings"

	"bsl-flow/cli/internal/runner"
)

// NewRunnerTaskSource adapts canonical controller reads to the native runner
// supervision loop. The returned source serves activated canonical tasks
// only: planned, checkout-local, and adopted-rebind tasks surface their
// regular controller blockers so the loop classifies them instead of
// silently skipping the queue entry.
func NewRunnerTaskSource(project string) (runner.TaskSource, error) {
	if _, err := openReadOnly(project); err != nil {
		return nil, err
	}
	return runnerTaskSource{project: project}, nil
}

type runnerTaskSource struct {
	project string
}

// Snapshot projects one task into the decision ingredients of
// Get-BFRunnerDecision: the Read-BFTask state, the Get-BFNext action and the
// process ownership markers of the active attempt.
func (s runnerTaskSource) Snapshot(_ context.Context, taskID runner.TaskID) (runner.TaskSnapshot, error) {
	repository, err := openReadOnly(s.project)
	if err != nil {
		return runner.TaskSnapshot{}, err
	}
	task, err := repository.ReadTask(string(taskID))
	if err != nil {
		return runner.TaskSnapshot{}, err
	}
	if task.Lifecycle == "planned" {
		return runner.TaskSnapshot{}, blocked("planned task %s has no execution authorization; run `task activate` first", taskID)
	}
	payload, ok := task.State["controller"].(map[string]any)
	if !ok {
		return runner.TaskSnapshot{}, blocked("task %s has no native controller state", taskID)
	}
	next, err := controllerNext(task.State, payload)
	if err != nil {
		return runner.TaskSnapshot{}, err
	}
	snapshot := runner.TaskSnapshot{
		Revision:         int(task.Revision),
		Status:           asStringOr(payload["status"]),
		ActiveAttempt:    asStringOr(payload["active_attempt"]),
		UnresolvedEffect: payload["unresolved_effect"] != nil,
		NextAction:       asStringOr(next["action"]),
	}
	if snapshot.Status == "completed" {
		snapshot.AcceptanceStale = !runnerAcceptanceVerifies(payload)
	}
	if snapshot.ActiveAttempt != "" {
		if err := runnerAttemptOwnership(repository, task.ID, snapshot.ActiveAttempt, &snapshot); err != nil {
			return runner.TaskSnapshot{}, err
		}
	}
	return snapshot, nil
}

// runnerAcceptanceVerifies mirrors the New-BFEnvelope completed-status rule:
// a completed task keeps its status only when the next action is accept and
// the retained acceptance receipt still hashes to its recorded identity.
func runnerAcceptanceVerifies(payload map[string]any) bool {
	items := anyItems(payload["acceptances"])
	if len(items) == 0 {
		return false
	}
	receipt, ok := items[len(items)-1].(map[string]any)
	if !ok {
		return false
	}
	data, err := ReadFileBytes(asStringOr(receipt["path"]))
	if err != nil {
		return false
	}
	object, err := DecodeObject(data)
	if err != nil {
		return false
	}
	digest, err := Hash(object)
	if err != nil {
		return false
	}
	return digest == asStringOr(receipt["sha256"])
}

// runnerAttemptOwnership reads the active attempt's start.json controller
// identity and every process.json receipt under the attempt directory, the
// ownership markers of Test-BFRunnerAttemptIsDead.
func runnerAttemptOwnership(repository *Repository, taskID, attemptID string, snapshot *runner.TaskSnapshot) error {
	attemptDir := filepath.Join(repository.StorePath, "tasks", taskID, "attempts", attemptID)
	start, err := native1CReadJSON(filepath.Join(attemptDir, "start.json"))
	if err != nil {
		return err
	}
	snapshot.AttemptStage = asStringOr(start["stage"])
	if owner := asMap(start["controller_process"]); owner != nil {
		snapshot.Controller = &runner.ProcessIdentity{
			PID:          int64(asIntOr(owner["pid"])),
			StartTimeUTC: asStringOr(owner["start_time_utc"]),
		}
	}
	return native1CWalkFiles(attemptDir, func(path string) error {
		if !strings.EqualFold(filepath.Base(path), "process.json") {
			return nil
		}
		document, err := native1CReadJSON(path)
		if err != nil {
			return err
		}
		snapshot.OwnedProcesses = append(snapshot.OwnedProcesses, runner.ProcessIdentity{
			PID:          int64(asIntOr(document["pid"])),
			StartTimeUTC: asStringOr(document["start_time_utc"]),
		})
		return nil
	})
}
