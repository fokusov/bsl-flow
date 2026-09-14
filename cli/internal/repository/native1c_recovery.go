package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This file ports the native 1C recovery ledger of Task.Runtime.ps1:
// Resolve/Complete-BFNativeRecovery (control-read reconciliation of an
// uncertain attempt), Get-BFNativeSavedObservation / Complete-BFNativeSavedSuccess
// (resume of a retained success) and Complete-BFRecordedNativeSuccess (record
// completion). The offline seams mirror the overrides of
// Test-NativeRecovery.ps1: recovery never reaches COM, a database, network or
// a model in tests.

// Native1CRecoveryRuntime carries the injectable offline seams. Production
// wires the trusted local app data journal and the platform COM control read.
type Native1CRecoveryRuntime struct {
	JournalRoot func(key string) (string, error)
	ControlRead func(ctx context.Context, target, executable string, credential Native1CRuntimeAuth, directory string) (map[string]any, error)
	Now         func() time.Time
}

func (r Native1CRecoveryRuntime) journal(key string) (string, error) {
	if r.JournalRoot != nil {
		return r.JournalRoot(key)
	}
	return Native1CJournalRoot(key)
}

func (r Native1CRecoveryRuntime) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r Native1CRecoveryRuntime) lock(journal string) (func(), error) {
	unlock, err := Lock(filepath.Join(journal, ".writer.lock"))
	if err != nil {
		return nil, conflict("Writer lock is held by another controller.")
	}
	return unlock, nil
}

// Native1CResolveRecovery mirrors Resolve-BFNativeRecovery: one authorized
// control read against the pending ledger, with the reconciled receipt.
func Native1CResolveRecovery(ctx context.Context, state map[string]any, resolution map[string]any, attemptDir string, manifest map[string]any, credential *Native1CRuntimeAuth, runtime Native1CRecoveryRuntime) (map[string]any, error) {
	resolved, err := native1CResolutionFields(resolution)
	if err != nil {
		return nil, err
	}
	var unresolvedAttempt any
	if state["active_attempt"] != nil {
		unresolvedAttempt = state["active_attempt"]
	} else if asMap(state["unresolved_effect"])["attempt_id"] != nil {
		unresolvedAttempt = asMap(state["unresolved_effect"])["attempt_id"]
	}
	if asStringOr(resolved["scope"]) != "native_1c" || asStringOr(resolved["attempt_id"]) != asStringOr(unresolvedAttempt) || !native1CBool(resolved["retry_authorized"]) {
		return nil, blocked("exact native recovery with explicit retry authorization is required.")
	}
	if strings.TrimSpace(asStringOr(resolved["observation"])) == "" {
		return nil, invalid("invalid resolution.observation.")
	}
	target, err := SafePath(asStringOr(resolved["target"]))
	if err != nil {
		return nil, err
	}
	target = strings.TrimRight(target, `\/`)
	key, err := Native1CTargetKey(target)
	if err != nil {
		return nil, err
	}
	journal, err := runtime.journal(key)
	if err != nil {
		return nil, err
	}
	unlock, err := runtime.lock(journal)
	if err != nil {
		return nil, err
	}
	defer unlock()
	identity := map[string]any{
		"task_id":          asStringOr(state["task_id"]),
		"attempt_id":       asStringOr(resolved["attempt_id"]),
		"target":           target,
		"source_sha256":    asStringOr(resolved["source_sha256"]),
		"inventory_sha256": asStringOr(resolved["inventory_sha256"]),
		"observation":      asStringOr(resolved["observation"]),
		"retry_authorized": true,
	}
	receiptID, err := Hash(identity)
	if err != nil {
		return nil, err
	}
	history := filepath.Join(journal, "recovery")
	if err := SafeMkdir(history); err != nil {
		return nil, blocked("%v", err)
	}
	receiptPath := filepath.Join(history, receiptID+".json")
	pendingPath := filepath.Join(journal, "pending.json")
	if !nativeDependencyRegularFile(pendingPath) {
		return nil, blocked("native pending record is missing and no matching recovery receipt exists.")
	}
	pending, err := native1CReadJSON(pendingPath)
	if err != nil {
		return nil, err
	}
	if asStringOr(pending["task_id"]) != asStringOr(state["task_id"]) ||
		asStringOr(pending["attempt_id"]) != asStringOr(resolved["attempt_id"]) ||
		!strings.EqualFold(asStringOr(pending["target"]), target) ||
		asStringOr(pending["full_source_sha256"]) != asStringOr(resolved["source_sha256"]) {
		return nil, conflict("native recovery identity mismatch.")
	}
	if nativeDependencyRegularFile(receiptPath) {
		receipt, err := native1CReadJSON(receiptPath)
		if err != nil {
			return nil, err
		}
		identityHash, err := Hash(asMap(receipt["identity"]))
		if err != nil {
			return nil, err
		}
		if identityHash != receiptID {
			return nil, conflict("retained recovery identity changed.")
		}
		control, err := native1CReadJSON(asStringOr(receipt["inventory_path"]))
		if err != nil {
			return nil, err
		}
		controlHash, err := Native1CInventoryHash(control)
		if err != nil {
			return nil, err
		}
		if controlHash != asStringOr(receipt["inventory_sha256"]) {
			return nil, blocked("retained control-read inventory changed.")
		}
		marked := pending["reconciled_identity_sha256"]
		if marked == nil {
			pendingHash, err := native1CFileSHA256(pendingPath)
			if err != nil {
				return nil, err
			}
			if pendingHash != asStringOr(receipt["pending_sha256"]) {
				return nil, conflict("unreconciled pending record changed after control read.")
			}
		} else if asStringOr(marked) != receiptID {
			return nil, conflict("pending record has another recovery.")
		}
		receiptFileHash, err := native1CFileSHA256(receiptPath)
		if err != nil {
			return nil, err
		}
		pending["reconciled_receipt_sha256"] = receiptFileHash
		pending["reconciled_identity_sha256"] = receiptID
		if err := native1CReplaceJSON(pendingPath, pending); err != nil {
			return nil, err
		}
		return receipt, nil
	}
	start, err := native1CReadJSON(filepath.Join(attemptDir, "start.json"))
	if err != nil {
		return nil, err
	}
	owner := asMap(start["controller_process"])
	if owner != nil && !native1CProcessDead(asIntOr(owner["pid"]), asStringOr(owner["start_time_utc"])) {
		return nil, blocked("original native controller is still running.")
	}
	if err := native1CWalkFiles(attemptDir, func(path string) error {
		if !strings.EqualFold(filepath.Base(path), "prepared.json") {
			return nil
		}
		dir := filepath.Dir(path)
		if !nativeDependencyRegularFile(filepath.Join(dir, "process.json")) && !nativeDependencyRegularFile(filepath.Join(dir, "not-started.json")) {
			return blocked("native process identity gap requires independent operator investigation.")
		}
		notStarted := filepath.Join(dir, "not-started.json")
		if nativeDependencyRegularFile(notStarted) {
			document, err := native1CReadJSON(notStarted)
			if err != nil {
				return err
			}
			if asStringOr(document["request_sha256"]) != asStringOr(pending["request_sha256"]) {
				return blocked("no-start receipt has a different native request.")
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := native1CWalkFiles(attemptDir, func(path string) error {
		if !strings.EqualFold(filepath.Base(path), "process.json") {
			return nil
		}
		document, err := native1CReadJSON(path)
		if err != nil {
			return err
		}
		if !native1CProcessDead(asIntOr(document["pid"]), asStringOr(document["start_time_utc"])) {
			return blocked("native child process is still running.")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	var criterion map[string]any
	count := 0
	for _, raw := range anyItems(asMap(state["request"])["criteria"]) {
		candidate := asMap(raw)
		if asStringOr(candidate["id"]) == asStringOr(pending["criterion_id"]) {
			criterion = candidate
			count++
		}
	}
	if count != 1 || !strings.EqualFold(asStringOr(criterion["target"]), target) {
		return nil, blocked("pending native criterion is unavailable.")
	}
	if asStringOr(manifest["sha256"]) != asStringOr(resolved["source_sha256"]) {
		return nil, conflict("recovery source control read is stale.")
	}
	if credential == nil || runtime.ControlRead == nil {
		return nil, blocked("native credential must be supplied through private controller input for this process.")
	}
	inventoryDirectory := filepath.Join(history, receiptID+"-read-"+strings.ToLower(randomRecoveryID()))
	inventory, err := runtime.ControlRead(ctx, target, asStringOr(criterion["executable"]), *credential, inventoryDirectory)
	if err != nil {
		return nil, err
	}
	actualHash, err := Native1CInventoryHash(inventory)
	if err != nil {
		return nil, err
	}
	if actualHash != asStringOr(resolved["inventory_sha256"]) {
		return nil, conflict("recovery inventory control read differs from the trusted resolution.")
	}
	control := filepath.Join(history, receiptID+".inventory.json")
	if err := AtomicWriteCanonical(control, inventory); err != nil {
		return nil, err
	}
	pendingHash, err := native1CFileSHA256(pendingPath)
	if err != nil {
		return nil, err
	}
	receipt := map[string]any{
		"schema_version":   int64(1),
		"kind":             "bsl-flow.native-1c-recovery",
		"identity":         identity,
		"inventory_path":   control,
		"inventory_sha256": actualHash,
		"pending_sha256":   pendingHash,
		"resolved_at_utc":  runtime.now().UTC().Format("2006-01-02T15:04:05.0000000Z"),
		"verdict":          "RECONCILED_NO_PASS",
	}
	if err := AtomicWriteCanonical(receiptPath, receipt); err != nil {
		return nil, err
	}
	receiptFileHash, err := native1CFileSHA256(receiptPath)
	if err != nil {
		return nil, err
	}
	pending["reconciled_receipt_sha256"] = receiptFileHash
	pending["reconciled_identity_sha256"] = receiptID
	if err := native1CReplaceJSON(pendingPath, pending); err != nil {
		return nil, err
	}
	return receipt, nil
}

// Native1CCompleteRecovery mirrors Complete-BFNativeRecovery.
func Native1CCompleteRecovery(state map[string]any, resolution map[string]any, runtime Native1CRecoveryRuntime) (map[string]any, error) {
	target, err := SafePath(asStringOr(resolution["target"]))
	if err != nil {
		return nil, err
	}
	target = strings.TrimRight(target, `\/`)
	key, err := Native1CTargetKey(target)
	if err != nil {
		return nil, err
	}
	journal, err := runtime.journal(key)
	if err != nil {
		return nil, err
	}
	unlock, err := runtime.lock(journal)
	if err != nil {
		return nil, err
	}
	defer unlock()
	identity := map[string]any{
		"task_id":          asStringOr(state["task_id"]),
		"attempt_id":       asStringOr(resolution["attempt_id"]),
		"target":           target,
		"source_sha256":    asStringOr(resolution["source_sha256"]),
		"inventory_sha256": asStringOr(resolution["inventory_sha256"]),
		"observation":      asStringOr(resolution["observation"]),
		"retry_authorized": true,
	}
	receiptID, err := Hash(identity)
	if err != nil {
		return nil, err
	}
	receiptPath := filepath.Join(journal, "recovery", receiptID+".json")
	if !nativeDependencyRegularFile(receiptPath) {
		return nil, blocked("durable native recovery receipt is missing.")
	}
	receipt, err := native1CReadJSON(receiptPath)
	if err != nil {
		return nil, err
	}
	receiptIdentityHash, err := Hash(asMap(receipt["identity"]))
	if err != nil {
		return nil, err
	}
	identityHash, err := Hash(identity)
	if err != nil {
		return nil, err
	}
	if receiptIdentityHash != identityHash {
		return nil, conflict("native recovery receipt identity mismatch.")
	}
	pendingPath := filepath.Join(journal, "pending.json")
	if !nativeDependencyRegularFile(pendingPath) {
		return receipt, nil
	}
	pending, err := native1CReadJSON(pendingPath)
	if err != nil {
		return nil, err
	}
	receiptFileHash, err := native1CFileSHA256(receiptPath)
	if err != nil {
		return nil, err
	}
	if asStringOr(pending["task_id"]) != asStringOr(state["task_id"]) ||
		asStringOr(pending["attempt_id"]) != asStringOr(resolution["attempt_id"]) ||
		asStringOr(pending["reconciled_identity_sha256"]) != receiptID ||
		asStringOr(pending["reconciled_receipt_sha256"]) != receiptFileHash {
		return nil, conflict("native pending latch differs from the committed recovery.")
	}
	if err := os.Remove(pendingPath); err != nil {
		return nil, blocked("%v", err)
	}
	return receipt, nil
}

// Native1CSavedObservation mirrors Get-BFNativeSavedObservation: the retained
// success is re-validated from its own durable evidence without any runtime
// dispatch. rawDir is the per-criterion raw evidence directory.
func Native1CSavedObservation(state map[string]any, criterion map[string]any, rawDir, attemptID, taskDir string, manifest map[string]any) (map[string]any, error) {
	receipt, err := native1CReadJSON(filepath.Join(rawDir, "runtime-success.json"))
	if err != nil {
		return nil, err
	}
	request, err := native1CReadJSON(filepath.Join(rawDir, "runtime-request.json"))
	if err != nil {
		return nil, err
	}
	requestHash, err := Hash(request)
	if err != nil {
		return nil, err
	}
	if asStringOr(receipt["kind"]) != "bsl-flow.native-1c-success" ||
		asStringOr(receipt["task_id"]) != asStringOr(state["task_id"]) ||
		asStringOr(receipt["attempt_id"]) != attemptID ||
		asStringOr(receipt["request_sha256"]) != requestHash {
		return nil, blocked("saved native success identity mismatch.")
	}
	expectedTestsHash, err := Hash(criterion["expected_tests"])
	if err != nil {
		return nil, err
	}
	requestExpectedHash, err := Hash(request["expected_tests"])
	if err != nil {
		return nil, err
	}
	if asStringOr(request["task_id"]) != asStringOr(state["task_id"]) ||
		asStringOr(request["attempt_id"]) != attemptID ||
		asStringOr(request["criterion_id"]) != asStringOr(criterion["id"]) ||
		!strings.EqualFold(asStringOr(request["target"]), asStringOr(criterion["target"])) ||
		requestExpectedHash != expectedTestsHash {
		return nil, blocked("saved native request differs from current criterion.")
	}
	native := asMap(criterion["native_1c"])
	sourceRoot, err := SafePath(filepath.Join(asStringOr(state["worker_path"]), filepath.FromSlash(asStringOr(native["source_root"]))))
	if err != nil {
		return nil, err
	}
	source, err := Native1CSourceSnapshot(sourceRoot)
	if err != nil {
		return nil, err
	}
	snapshot, err := Native1CSourceSnapshot(filepath.Join(rawDir, "source-snapshot"))
	if err != nil {
		return nil, err
	}
	if asStringOr(source["sha256"]) != asStringOr(asMap(request["source"])["sha256"]) ||
		asStringOr(snapshot["sha256"]) != asStringOr(source["sha256"]) ||
		asStringOr(receipt["source_sha256"]) != asStringOr(source["sha256"]) {
		return nil, blocked("saved native source snapshot is stale.")
	}
	before, err := native1CReadJSON(filepath.Join(rawDir, "inventory-before.json"))
	if err != nil {
		return nil, err
	}
	loadedProof, err := Native1CLoadedProof(state, criterion, source, before, taskDir, manifest)
	if err != nil {
		return nil, err
	}
	steps := []string{"load", "update", "test"}
	if loadedProof != nil {
		proofHash, err := Hash(loadedProof)
		if err != nil {
			return nil, err
		}
		savedProof, err := native1CReadJSON(filepath.Join(rawDir, "loaded-proof.json"))
		if err != nil {
			return nil, err
		}
		savedProofHash, err := Hash(savedProof)
		if err != nil {
			return nil, err
		}
		if savedProofHash != proofHash {
			return nil, blocked("saved loaded source proof changed.")
		}
		steps = []string{"test"}
	}
	for _, name := range steps {
		terminal, err := native1CReadJSON(filepath.Join(rawDir, "steps", name, "terminal.json"))
		if err != nil {
			return nil, err
		}
		logPath, err := SafePath(asStringOr(terminal["log"]))
		if err != nil {
			return nil, err
		}
		logHash, err := native1CFileSHA256(logPath)
		if err != nil {
			return nil, blocked("incomplete saved native terminal evidence.")
		}
		if asStringOr(terminal["request_sha256"]) != requestHash || asIntOr(terminal["exit_code"]) != 0 || logHash != asStringOr(terminal["log_sha256"]) {
			return nil, blocked("incomplete saved native terminal evidence.")
		}
	}
	started, err := native1CTerminalStart(filepath.Join(rawDir, "steps", "test", "terminal.json"))
	if err != nil {
		return nil, blocked("incomplete saved native terminal evidence.")
	}
	finished, err := native1CTerminalFinish(filepath.Join(rawDir, "steps", "test", "terminal.json"))
	if err != nil {
		return nil, blocked("incomplete saved native terminal evidence.")
	}
	expected := make([]string, 0)
	for _, raw := range anyItems(criterion["expected_tests"]) {
		expected = append(expected, asStringOr(raw))
	}
	parsed, err := Native1CJUnit(filepath.Join(rawDir, "original.junit.xml"), expected, started, finished)
	if err != nil {
		return nil, err
	}
	after, err := native1CReadJSON(filepath.Join(rawDir, "inventory-after.json"))
	if err != nil {
		return nil, err
	}
	if asStringOr(parsed["sha256"]) != asStringOr(receipt["junit_sha256"]) {
		return nil, blocked("saved native reports changed.")
	}
	beforeHash, err := Native1CInventoryHash(before)
	if err != nil {
		return nil, err
	}
	afterHash, err := Native1CInventoryHash(after)
	if err != nil {
		return nil, err
	}
	if beforeHash != asStringOr(receipt["inventory_before_sha256"]) || afterHash != asStringOr(receipt["inventory_after_sha256"]) {
		return nil, blocked("saved native reports changed.")
	}
	if err := Native1CInventoryTransition(before, after, source); err != nil {
		return nil, err
	}
	if loadedProof != nil && beforeHash != afterHash {
		return nil, blocked("saved test-only inventory changed.")
	}
	return map[string]any{
		"criterion_id": asStringOr(criterion["id"]),
		"kind":         asStringOr(criterion["kind"]),
		"tests":        parsed["tests"],
		"sha256":       asStringOr(parsed["sha256"]),
		"outcome":      "PASS",
		"runtime": map[string]any{
			"target_key":              asStringOr(request["target_key"]),
			"target":                  asStringOr(request["target"]),
			"source_sha256":           asStringOr(source["sha256"]),
			"extension":               asStringOr(source["extension"]),
			"version":                 asStringOr(source["version"]),
			"uuid":                    asStringOr(source["uuid"]),
			"inventory_before_sha256": beforeHash,
			"inventory_after_sha256":  afterHash,
			"request_sha256":          requestHash,
		},
	}, nil
}

// Native1CCompleteSavedSuccess mirrors Complete-BFNativeSavedSuccess: the
// retained success atomically archives its receipt and clears only its exact
// pending latch.
func Native1CCompleteSavedSuccess(taskID, attemptID, rawDir string, runtime Native1CRecoveryRuntime) error {
	request, err := native1CReadJSON(filepath.Join(rawDir, "runtime-request.json"))
	if err != nil {
		return err
	}
	receipt, err := native1CReadJSON(filepath.Join(rawDir, "runtime-success.json"))
	if err != nil {
		return err
	}
	journal, err := runtime.journal(asStringOr(request["target_key"]))
	if err != nil {
		return err
	}
	unlock, err := runtime.lock(journal)
	if err != nil {
		return err
	}
	defer unlock()
	pendingPath := filepath.Join(journal, "pending.json")
	if !nativeDependencyRegularFile(pendingPath) {
		return nil
	}
	pending, err := native1CReadJSON(pendingPath)
	if err != nil {
		return err
	}
	if asStringOr(pending["task_id"]) != taskID || asStringOr(pending["attempt_id"]) != attemptID {
		return nil
	}
	requestHash, err := Hash(request)
	if err != nil {
		return err
	}
	if asStringOr(pending["request_sha256"]) != asStringOr(receipt["request_sha256"]) || asStringOr(receipt["request_sha256"]) != requestHash {
		return blocked("saved native success does not resolve the target latch.")
	}
	history := filepath.Join(journal, "history")
	if err := SafeMkdir(history); err != nil {
		return blocked("%v", err)
	}
	path := filepath.Join(history, attemptID+".success.json")
	if nativeDependencyRegularFile(path) {
		saved, err := native1CReadJSON(path)
		if err != nil {
			return err
		}
		savedHash, err := Hash(saved)
		if err != nil {
			return err
		}
		receiptHash, err := Hash(receipt)
		if err != nil {
			return err
		}
		if savedHash != receiptHash {
			return conflict("conflicting native success history.")
		}
		return nil
	}
	if err := AtomicWriteCanonical(path, receipt); err != nil {
		return err
	}
	if err := os.Remove(pendingPath); err != nil {
		return blocked("%v", err)
	}
	return nil
}

// Native1CCompleteRecordedSuccess mirrors Complete-BFRecordedNativeSuccess:
// a verified recorded PASS completes its own pending latch from the retained
// terminal result and the raw evidence persisted under the attempt
// directory's artifacts root.
func Native1CCompleteRecordedSuccess(state map[string]any, attemptID, attemptDir string, runtime Native1CRecoveryRuntime) error {
	artifactsRoot := filepath.Join(attemptDir, "artifacts")
	rawRoot := filepath.Join(artifactsRoot, "raw")
	var entry map[string]any
	count := 0
	for _, raw := range anyItems(state["evidence"]) {
		candidate := asMap(raw)
		if asStringOr(candidate["attempt_id"]) == attemptID &&
			asStringOr(candidate["stage"]) == "verify" &&
			asStringOr(candidate["outcome"]) == "PASS" {
			entry = candidate
			count++
		}
	}
	if count != 1 {
		return nil
	}
	terminal, err := native1CReadJSON(filepath.Join(attemptDir, "terminal.json"))
	if err != nil {
		return nil
	}
	resultHash, err := Hash(terminal)
	if err != nil {
		return err
	}
	if resultHash != asStringOr(entry["result_sha256"]) {
		return blocked("recorded native result changed before completion.")
	}
	prefix := strings.ToLower("attempts/" + attemptID + "/")
	for _, raw := range anyItems(entry["raw_hashes"]) {
		rawHash := asMap(raw)
		relative := asStringOr(rawHash["path"])
		if !strings.HasPrefix(strings.ToLower(filepath.ToSlash(relative)), prefix) {
			return blocked("recorded native evidence changed before completion.")
		}
		relative = relative[len("attempts/"+attemptID+"/"):]
		data, err := readProviderArtifactFile(artifactsRoot, filepath.ToSlash(relative))
		if err != nil || fileSHA256(data) != asStringOr(rawHash["sha256"]) {
			return blocked("recorded native evidence changed before completion.")
		}
	}
	successes := []string{}
	if err := native1CWalkDirs(rawRoot, func(path string) error {
		if !strings.EqualFold(filepath.Base(path), "runtime-success.json") {
			return nil
		}
		if !strings.EqualFold(filepath.Dir(filepath.Dir(path)), rawRoot) {
			return nil
		}
		successes = append(successes, filepath.Dir(path))
		return nil
	}); err != nil {
		return err
	}
	for _, success := range successes {
		if err := Native1CCompleteSavedSuccess(asStringOr(state["task_id"]), attemptID, success, runtime); err != nil {
			return err
		}
	}
	return nil
}

// --- shared helpers ---

func native1CResolutionFields(resolution map[string]any) (map[string]any, error) {
	required := []string{"attempt_id", "scope", "target", "source_sha256", "inventory_sha256", "observation", "retry_authorized"}
	if resolution == nil {
		return nil, invalid("resolution must be an object.")
	}
	allowed := map[string]bool{}
	for _, field := range required {
		allowed[field] = true
		if _, present := resolution[field]; !present {
			return nil, invalid("resolution.%s is required.", field)
		}
	}
	for field := range resolution {
		if !allowed[field] {
			return nil, invalid("unknown field resolution.%s.", field)
		}
	}
	return resolution, nil
}

// AtomicWriteCanonical publishes one canonical JSON document atomically.
func AtomicWriteCanonical(path string, value any) error {
	data, err := Canonical(value)
	if err != nil {
		return err
	}
	return AtomicWrite(path, data, false)
}

// native1CReplaceJSON mirrors Write-BFJson -Replace: an atomic republish of
// one canonical document.
func native1CReplaceJSON(path string, value any) error {
	data, err := Canonical(value)
	if err != nil {
		return err
	}
	return AtomicWrite(path, data, true)
}

func native1CWalkFiles(root string, visit func(path string) error) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		return visit(path)
	})
}

func native1CWalkDirs(root string, visit func(path string) error) error {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		// The raw root may legitimately be absent before the first native run.
		return nil
	}
	return native1CWalkFiles(root, visit)
}

func native1CBool(value any) bool {
	flag, ok := value.(bool)
	return ok && flag
}

func native1CTerminalStart(terminalPath string) (time.Time, error) {
	terminal, err := native1CReadJSON(terminalPath)
	if err != nil {
		return time.Time{}, err
	}
	process := asMap(terminal["process"])
	return time.Parse("2006-01-02T15:04:05.0000000Z", asStringOr(process["start_time_utc"]))
}

func native1CTerminalFinish(terminalPath string) (time.Time, error) {
	terminal, err := native1CReadJSON(terminalPath)
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse("2006-01-02T15:04:05.0000000Z", asStringOr(terminal["finished_at_utc"]))
}

func randomRecoveryID() string {
	value, err := randomHex(16)
	if err != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return value
}
