package stagehost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"bsl-flow/cli/internal/repository"
)

// This file ports Invoke-BFNativeVerification (Task.Runtime.ps1): the full
// native 1C verify flow — journal latch, source snapshot, inventory control
// reads, the authorized DESIGNER load/update steps, the ENTERPRISE test run
// and the terminal success receipt.

// runNativeVerification dispatches one native 1C criterion through the native
// adapter. The credential arrives through the private provider input channel
// and never touches argv or receipts.
func runNativeVerification(ctx context.Context, deps Deps, state map[string]any, criterion map[string]any, directory string, credential *native1cCredential, run *stageRun) (map[string]any, error) {
	if err := assertNativeCriterion(criterion); err != nil {
		return nil, err
	}
	if err := native1cPlatformBlocker(runtime.GOOS); err != nil {
		return nil, err
	}
	if credential == nil {
		return nil, blockedf("native credential must be supplied through private controller input for this process.")
	}
	attemptID, isAttempt := state["active_attempt"].(string)
	if !isAttempt {
		return nil, conflictf("registered active attempt is required for native verification.")
	}
	target, err := safePath(asStringOr(criterion["target"]))
	if err != nil {
		return nil, err
	}
	target = strings.TrimRight(target, `\/`)
	key, err := repository.StageHostNative1CTargetKey(target)
	if err != nil {
		return nil, native1cError(err)
	}
	journal, err := native1CJournalRootOf(deps, key)
	if err != nil {
		return nil, native1cError(err)
	}
	unlock, err := repository.Lock(filepath.Join(journal, ".writer.lock"))
	if err != nil {
		return nil, conflictf("Writer lock is held by another controller.")
	}
	defer unlock()
	pendingWritten := false
	result, err := runNativeVerificationBody(ctx, deps, state, criterion, credential, run, directory, target, key, journal, attemptID, &pendingWritten)
	if err == nil {
		return result, nil
	}
	// The sanitized catch of Invoke-BFNativeVerification: only classified BF_*
	// errors pass through; everything else collapses into the sanitized
	// adapter failure so COM/process internals never leak.
	sanitized := err
	if typed, ok := err.(*Error); ok && (typed.Class == ClassInvalid || typed.Class == ClassBlocked || typed.Class == ClassConflict || typed.Class == ClassFail) {
		sanitized = typed
	} else {
		sanitized = blockedf("native adapter failed; inspect sanitized attempt evidence.")
	}
	code := ClassBlocked
	if typed, ok := sanitized.(*Error); ok {
		code = typed.Class
	}
	failure := map[string]any{
		"code":          code,
		"error_type":    fmt.Sprintf("%T", err),
		"failed_at_utc": startTimeUTC(deps.now()),
	}
	if writeErr := writeJSON(filepath.Join(directory, "runtime-failure.json"), failure, false); writeErr != nil {
		return nil, writeErr
	}
	if !pendingWritten {
		noDispatch := map[string]any{
			"schema_version": int64(1),
			"kind":           "bsl-flow.native-1c-no-dispatch",
			"task_id":        asStringOr(state["task_id"]),
			"attempt_id":     attemptID,
			"criterion_id":   asStringOr(criterion["id"]),
			"state":          "preflight_failed_before_pending",
			"failure":        failure,
		}
		if writeErr := writeJSON(filepath.Join(directory, "runtime-no-dispatch.json"), noDispatch, false); writeErr != nil {
			return nil, writeErr
		}
	}
	return nil, sanitized
}

func runNativeVerificationBody(ctx context.Context, deps Deps, state map[string]any, criterion map[string]any, credential *native1cCredential, run *stageRun, directory, target, key, journal, attemptID string, pendingWritten *bool) (map[string]any, error) {
	native := asMap(criterion["native_1c"])
	pendingPath := filepath.Join(journal, "pending.json")
	if fileExists(pendingPath) {
		return nil, blockedf("target has an unresolved native attempt; automatic replay is forbidden.")
	}
	marker := filepath.Join(target, "1Cv8.1CD")
	if !isRegularFile(marker) {
		return nil, blockedf("authorized FILE target marker is missing.")
	}
	executable, err := safePath(asStringOr(criterion["executable"]))
	if err != nil {
		return nil, err
	}
	executableHash, err := hashFile(executable)
	if err != nil {
		return nil, blockedf("authorized 1cv8.exe identity changed.")
	}
	if executableHash != asStringOr(native["executable_sha256"]) {
		return nil, blockedf("authorized 1cv8.exe identity changed.")
	}
	physical, err := repository.StageHostNative1CTargetIdentity(target)
	if err != nil {
		return nil, native1cError(err)
	}
	if !strings.EqualFold(strings.TrimRight(physical, `\/`), target) {
		return nil, blockedf("native target does not match its physical identity.")
	}
	fullSource, err := stageSourceManifest(state)
	if err != nil {
		return nil, err
	}
	sourceRoot, err := safePath(filepath.Join(asStringOr(state["worker_path"]), filepath.FromSlash(asStringOr(native["source_root"]))))
	if err != nil {
		return nil, err
	}
	source, err := repository.StageHostNative1CSourceSnapshot(sourceRoot)
	if err != nil {
		return nil, native1cError(err)
	}
	if asStringOr(source["extension"]) != asStringOr(native["extension"]) {
		return nil, blockedf("source extension name differs from criterion.")
	}
	if _, err := repository.StageHostNative1CSnapshotCopy(source, filepath.Join(directory, "source-snapshot")); err != nil {
		return nil, native1cError(err)
	}
	before, err := readNative1CInventory(ctx, deps, target, asStringOr(criterion["executable"]), *credential, filepath.Join(directory, "inventory-process-before"))
	if err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(directory, "inventory-before.json"), before, false); err != nil {
		return nil, err
	}
	loadedProof, err := nativeLoadedProof(state, criterion, source, before)
	if err != nil {
		return nil, err
	}
	if loadedProof != nil {
		if err := writeJSON(filepath.Join(directory, "loaded-proof.json"), loadedProof, false); err != nil {
			return nil, err
		}
	}
	request := map[string]any{
		"schema_version": int64(1),
		"kind":           "bsl-flow.native-1c-request",
		"task_id":        asStringOr(state["task_id"]),
		"attempt_id":     attemptID,
		"criterion_id":   asStringOr(criterion["id"]),
		"target":         target,
		"target_key":     key,
		"source": map[string]any{
			"extension": asStringOr(source["extension"]),
			"version":   asStringOr(source["version"]),
			"uuid":      asStringOr(source["uuid"]),
			"sha256":    asStringOr(source["sha256"]),
		},
		"platform": map[string]any{
			"version":           asStringOr(native["platform_version"]),
			"executable_sha256": asStringOr(native["executable_sha256"]),
		},
		"operations":              native["authorized_operations"],
		"authorization_reference": asStringOr(native["authorization_reference"]),
		"expected_tests":          criterion["expected_tests"],
	}
	requestHash, err := hashValue(request)
	if err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(directory, "runtime-request.json"), request, false); err != nil {
		return nil, err
	}
	pending := map[string]any{
		"schema_version":         int64(1),
		"state":                  "dispatched_or_unknown",
		"task_id":                asStringOr(state["task_id"]),
		"attempt_id":             attemptID,
		"criterion_id":           asStringOr(criterion["id"]),
		"target":                 target,
		"source_root":            asStringOr(native["source_root"]),
		"full_source_sha256":     asStringOr(fullSource["sha256"]),
		"extension_source_sha256": asStringOr(source["sha256"]),
		"request_sha256":         requestHash,
		"created_at_utc":         startTimeUTC(deps.now()),
	}
	if err := writeJSON(pendingPath, pending, false); err != nil {
		return nil, err
	}
	*pendingWritten = true
	timeoutSeconds := 1800
	if value, ok := asInteger(getValue(asMap(state["request"]), "timeout_seconds", int64(1800))); ok {
		timeoutSeconds = int(value)
	}
	base := []any{"DESIGNER", "/DisableStartupDialogs", "/DisableStartupMessages", "/F", target, "/N", "<username>", "/P", "<password>"}
	steps := []map[string]any{}
	if loadedProof == nil {
		steps = append(steps,
			map[string]any{"name": "load", "log": filepath.Join(directory, "load.out.log"), "argv": append(append([]any{}, base...), "/Out", filepath.Join(directory, "load.out.log"), "-NoTruncate", "/LoadConfigFromFiles", filepath.Join(directory, "source-snapshot"), "-Extension", asStringOr(source["extension"]))},
			map[string]any{"name": "update", "log": filepath.Join(directory, "update.out.log"), "argv": append(append([]any{}, base...), "/Out", filepath.Join(directory, "update.out.log"), "-NoTruncate", "/UpdateDBCfg", "-Extension", asStringOr(source["extension"]))},
		)
	}
	for _, step := range steps {
		if _, err := runNative1CProcess(ctx, deps, executable, step, *credential, directory, timeoutSeconds, run.cancel, requestHash); err != nil {
			return nil, err
		}
		check, err := repository.StageHostNative1CSourceSnapshot(asStringOr(source["root"]))
		if err != nil {
			return nil, native1cError(err)
		}
		if asStringOr(check["sha256"]) != asStringOr(source["sha256"]) {
			return nil, blockedf("worker source changed during native execution.")
		}
	}
	if loadedProof == nil {
		loaded, err := readNative1CInventory(ctx, deps, target, asStringOr(criterion["executable"]), *credential, filepath.Join(directory, "inventory-process-loaded"))
		if err != nil {
			return nil, err
		}
		if err := writeJSON(filepath.Join(directory, "inventory-loaded.json"), loaded, false); err != nil {
			return nil, err
		}
		if err := repository.StageHostNative1CInventoryTransition(before, loaded, source); err != nil {
			return nil, native1cError(err)
		}
	}
	reportPath := filepath.Join(directory, "original.junit.xml")
	config := map[string]any{
		"filter":          map[string]any{"modules": []any{asStringOr(native["module"])}},
		"reportFormat":    "jUnit",
		"reportPath":      reportPath,
		"closeAfterTests": true,
		"showReport":      false,
		"logging": map[string]any{
			"file":    filepath.Join(directory, "runner.log"),
			"console": false,
			"level":   "info",
		},
	}
	configPath := filepath.Join(directory, "test-config.json")
	if err := writeJSON(configPath, config, false); err != nil {
		return nil, err
	}
	testStep := map[string]any{
		"name": "test",
		"log":  filepath.Join(directory, "enterprise.out.log"),
		"argv": []any{"ENTERPRISE", "/DisableStartupDialogs", "/F", target, "/N", "<username>", "/P", "<password>", "/C", "RunUnitTests=" + strings.ReplaceAll(configPath, `\`, "/"), "/Out", filepath.Join(directory, "enterprise.out.log")},
	}
	terminal, err := runNative1CProcess(ctx, deps, executable, testStep, *credential, directory, timeoutSeconds, run.cancel, requestHash)
	if err != nil {
		return nil, err
	}
	check, err := repository.StageHostNative1CSourceSnapshot(asStringOr(source["root"]))
	if err != nil {
		return nil, native1cError(err)
	}
	if asStringOr(check["sha256"]) != asStringOr(source["sha256"]) {
		return nil, blockedf("worker source changed during native execution.")
	}
	started, err := parseNative1CTime(asStringOr(asMap(terminal["process"])["start_time_utc"]))
	if err != nil {
		return nil, blockedf("native step did not complete with a durable successful log.")
	}
	finished, err := parseNative1CTime(asStringOr(terminal["finished_at_utc"]))
	if err != nil {
		return nil, blockedf("native step did not complete with a durable successful log.")
	}
	expected := make([]string, 0)
	for _, raw := range anyItemsOf(criterion["expected_tests"]) {
		expected = append(expected, asStringOr(raw))
	}
	parsed, err := repository.StageHostNative1CJUnit(reportPath, expected, started, finished)
	if err != nil {
		return nil, native1cError(err)
	}
	after, err := readNative1CInventory(ctx, deps, target, asStringOr(criterion["executable"]), *credential, filepath.Join(directory, "inventory-process-after"))
	if err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(directory, "inventory-after.json"), after, false); err != nil {
		return nil, err
	}
	if err := repository.StageHostNative1CInventoryTransition(before, after, source); err != nil {
		return nil, native1cError(err)
	}
	beforeHash, err := repository.StageHostNative1CInventoryHash(before)
	if err != nil {
		return nil, err
	}
	afterHash, err := repository.StageHostNative1CInventoryHash(after)
	if err != nil {
		return nil, err
	}
	if loadedProof != nil && beforeHash != afterHash {
		return nil, blockedf("test-only run changed installed extension inventory.")
	}
	receipt := map[string]any{
		"schema_version":           int64(1),
		"kind":                     "bsl-flow.native-1c-success",
		"task_id":                  asStringOr(state["task_id"]),
		"attempt_id":               attemptID,
		"request_sha256":           requestHash,
		"source_sha256":            asStringOr(source["sha256"]),
		"inventory_before_sha256":  beforeHash,
		"inventory_after_sha256":   afterHash,
		"junit_sha256":             asStringOr(parsed["sha256"]),
		"tests":                    parsed["tests"],
		"completed_at_utc":         startTimeUTC(deps.now()),
	}
	if err := writeJSON(filepath.Join(directory, "runtime-success.json"), receipt, false); err != nil {
		return nil, err
	}
	history := filepath.Join(journal, "history")
	if err := os.MkdirAll(history, 0o755); err != nil {
		return nil, blockedf("%v", err)
	}
	if err := writeJSON(filepath.Join(history, attemptID+".success.json"), receipt, false); err != nil {
		return nil, err
	}
	if err := os.Remove(pendingPath); err != nil {
		return nil, blockedf("%v", err)
	}
	return map[string]any{
		"criterion_id": asStringOr(criterion["id"]),
		"kind":         asStringOr(criterion["kind"]),
		"tests":        parsed["tests"],
		"sha256":       asStringOr(parsed["sha256"]),
		"outcome":      "PASS",
		"runtime": map[string]any{
			"target_key":               key,
			"target":                   target,
			"source_sha256":            asStringOr(source["sha256"]),
			"extension":                asStringOr(source["extension"]),
			"version":                  asStringOr(source["version"]),
			"uuid":                     asStringOr(source["uuid"]),
			"inventory_before_sha256":  beforeHash,
			"inventory_after_sha256":   afterHash,
			"request_sha256":           requestHash,
		},
	}, nil
}

// parseNative1CTime parses the "o"-format timestamps of the terminal receipt.
func parseNative1CTime(value string) (time.Time, error) {
	return time.Parse("2006-01-02T15:04:05.0000000Z", value)
}
