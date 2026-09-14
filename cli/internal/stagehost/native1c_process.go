package stagehost

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// This file ports Invoke-BFNativeProcess (Task.Runtime.ps1): one authorized
// 1cv8 dispatch with the prepared/process/started/terminal receipt chain. The
// managed process runner executes the platform executable; the adapter writes
// the legacy-shaped receipts beside it, so saved-observation and recovery
// reads stay byte-compatible.

// runNative1CProcess dispatches one native step. The step carries its own
// name, log path and argv template; <username>/<password> placeholders are
// substituted with the private controller credential and never surface in
// argv, logs or receipts.
func runNative1CProcess(ctx context.Context, deps Deps, executable string, step map[string]any, credential native1cCredential, directory string, timeoutSeconds int, cancelled func() (bool, error), requestHash string) (map[string]any, error) {
	name := asStringOr(step["name"])
	if cancelled != nil {
		flag, err := cancelled()
		if err != nil {
			return nil, err
		}
		if flag {
			return nil, blockedf("cancelled before native dispatch.")
		}
	}
	stepDir := filepath.Join(directory, "steps", name)
	if err := os.MkdirAll(stepDir, 0o755); err != nil {
		return nil, blockedf("%v", err)
	}
	preparedAt := startTimeUTC(deps.now())
	if err := writeJSON(filepath.Join(stepDir, "prepared.json"), map[string]any{
		"request_sha256": requestHash,
		"step":           step,
		"prepared_at_utc": preparedAt,
	}, false); err != nil {
		return nil, err
	}
	if err := native1cSafeCredential(credential); err != nil {
		return nil, err
	}
	arguments := make([]string, 0)
	for _, raw := range anyItemsOf(step["argv"]) {
		argument := asStringOr(raw)
		switch argument {
		case "<username>":
			argument = credential.username
		case "<password>":
			argument = credential.password
		}
		arguments = append(arguments, argument)
	}
	deadline := deps.now().Add(time.Duration(timeoutSeconds) * time.Second)
	if ctx != nil {
		if ctxDeadline, present := ctx.Deadline(); present && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
	}
	if !deadline.After(deps.now()) {
		return nil, blockedf("task deadline reached before native dispatch.")
	}
	if cancelled != nil {
		flag, err := cancelled()
		if err != nil {
			return nil, err
		}
		if flag || !deadline.After(deps.now()) {
			if err := writeJSON(filepath.Join(stepDir, "not-started.json"), map[string]any{
				"request_sha256": requestHash,
				"reason":         "cancelled_or_deadline_before_start",
			}, false); err != nil {
				return nil, err
			}
			return nil, blockedf("cancellation or deadline before native start.")
		}
	}
	managed := filepath.Join(stepDir, "managed")
	process, err := deps.runProcess(ctx, ProcessOptions{
		Executable:       executable,
		Arguments:        arguments,
		WorkingDirectory: directory,
		OutputDirectory:  managed,
		TimeoutSeconds:   timeoutSeconds,
		Cancelled:        cancelled,
		Deadline:         deadline,
	})
	if err != nil {
		return nil, blockedf("native process did not start.")
	}
	switch process.StopReason {
	case "":
	case "timeout":
		return nil, blockedf("native process timed out and was not killed.")
	case "cancelled":
		return nil, blockedf("cancellation observed after native dispatch; process was not killed.")
	default:
		return nil, blockedf("native process did not complete with a durable successful log.")
	}
	managedProcess, err := readJSONObject(filepath.Join(managed, "process.json"))
	if err != nil {
		return nil, err
	}
	identity := map[string]any{
		"pid":               managedProcess["pid"],
		"start_time_utc":    asStringOr(managedProcess["start_time_utc"]),
		"request_sha256":    requestHash,
		"step":              name,
		"non_interruptible": true,
	}
	for _, receipt := range []string{"process.json", "started.json"} {
		if err := writeJSON(filepath.Join(stepDir, receipt), identity, false); err != nil {
			return nil, err
		}
	}
	logPath := asStringOr(step["log"])
	var logSHA256 any
	if isRegularFile(logPath) {
		hash, err := hashFile(logPath)
		if err != nil {
			return nil, err
		}
		logSHA256 = hash
	}
	terminal := map[string]any{
		"request_sha256": requestHash,
		"process":        identity,
		"exit_code":      int64(process.ExitCode),
		"finished_at_utc": startTimeUTC(deps.now()),
		"log":            logPath,
		"log_sha256":     logSHA256,
	}
	if err := writeJSON(filepath.Join(stepDir, "terminal.json"), terminal, false); err != nil {
		return nil, err
	}
	if process.ExitCode != 0 || terminal["log_sha256"] == nil {
		return nil, blockedf("native step did not complete with a durable successful log.")
	}
	return terminal, nil
}
