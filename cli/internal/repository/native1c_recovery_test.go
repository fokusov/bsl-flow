package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// This file mirrors the offline cases of scripts/Test-NativeRecovery.ps1:
// recovery never reaches COM, a database, network or a model. The injected
// seams replace the journal root and the control read exactly like the
// PowerShell fixture overrides.

func nativeRecoveryFixture(t *testing.T) (root, journal string) {
	t.Helper()
	root = t.TempDir()
	retryRecoveryCleanup(t, root)
	journal = filepath.Join(root, "journal")
	if err := os.MkdirAll(journal, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, journal
}

func retryRecoveryCleanup(t *testing.T, path string) {
	t.Helper()
	t.Cleanup(func() {
		for attempt := 0; attempt < 20; attempt++ {
			if err := os.RemoveAll(path); err == nil {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	})
}

func nativeRecoveryTarget(t *testing.T, root, name string) string {
	t.Helper()
	target := filepath.Join(root, "targets", name)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "1Cv8.1CD"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	return target
}

func nativeRecoveryRuntimeFor(journal string) Native1CRecoveryRuntime {
	return Native1CRecoveryRuntime{
		JournalRoot: func(key string) (string, error) { return journal, nil },
		Now:         func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) },
	}
}

func nativeRecoveryState(target, attemptID, taskID string, unresolved bool) map[string]any {
	state := map[string]any{
		"task_id":           taskID,
		"active_attempt":    attemptID,
		"unresolved_effect": nil,
		"request": map[string]any{
			"criteria": []any{map[string]any{"id": "native", "target": target, "executable": filepath.Join(os.TempDir(), "1cv8.exe")}},
		},
	}
	if unresolved {
		state["active_attempt"] = nil
		state["unresolved_effect"] = map[string]any{"attempt_id": attemptID, "scope": "native_1c"}
	}
	return state
}

func nativeRecoveryResolution(target, attemptID, inventoryHash string) map[string]any {
	return map[string]any{
		"attempt_id": attemptID, "scope": "native_1c", "target": target,
		"source_sha256":    strings.Repeat("a", 64),
		"inventory_sha256": inventoryHash,
		"observation":      "Offline control-read fixture observes the admitted target state.",
		"retry_authorized": true,
	}
}

func nativeRecoveryAttempt(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, "attempts", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteCanonical(filepath.Join(dir, "start.json"), map[string]any{
		"stage":              "verify",
		"controller_process": map[string]any{"pid": int64(2147483647), "start_time_utc": "2000-01-01T00:00:00.0000000Z"},
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func nativeRecoveryWritePending(t *testing.T, journal string, state map[string]any, target, attemptID string, replace ...bool) string {
	t.Helper()
	if err := os.MkdirAll(journal, 0o755); err != nil {
		t.Fatal(err)
	}
	pending := map[string]any{
		"schema_version":          int64(1),
		"state":                   "dispatched_or_unknown",
		"task_id":                 asStringOr(state["task_id"]),
		"attempt_id":              attemptID,
		"criterion_id":            "native",
		"target":                  target,
		"source_root":             "src/ext",
		"full_source_sha256":      strings.Repeat("a", 64),
		"extension_source_sha256": strings.Repeat("b", 64),
		"request_sha256":          strings.Repeat("c", 64),
		"created_at_utc":          "2026-09-10T00:00:00.0000000Z",
	}
	data, err := Canonical(pending)
	if err != nil {
		t.Fatal(err)
	}
	allowReplace := len(replace) > 0 && replace[0]
	if err := AtomicWrite(filepath.Join(journal, "pending.json"), data, allowReplace); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(journal, "pending.json")
}

func nativeRecoveryInventory(target string) map[string]any {
	return map[string]any{
		"target":     target,
		"platform":   map[string]any{"executable": "offline", "com_connector": "offline"},
		"extensions": []any{},
	}
}

func TestNative1CRecoveryResolveAndComplete(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("physical target identity is windows-only")
	}
	root, journal := nativeRecoveryFixture(t)
	target := nativeRecoveryTarget(t, root, "active")
	attemptID := "attempt-active"
	taskID := randomUUIDForTest(t)
	state := nativeRecoveryState(target, attemptID, taskID, false)
	attemptDir := nativeRecoveryAttempt(t, root, "active")
	inventory := nativeRecoveryInventory(target)
	hash, err := Native1CInventoryHash(inventory)
	if err != nil {
		t.Fatal(err)
	}
	resolution := nativeRecoveryResolution(target, attemptID, hash)
	pendingPath := nativeRecoveryWritePending(t, journal, state, target, attemptID)
	runtime := nativeRecoveryRuntimeFor(journal)
	runtime.ControlRead = func(ctx context.Context, target, executable string, credential Native1CRuntimeAuth, directory string) (map[string]any, error) {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return nil, err
		}
		return inventory, nil
	}
	credential := &Native1CRuntimeAuth{Username: "offline-user", Password: "offline-dummy-password"}
	receipt, err := Native1CResolveRecovery(context.Background(), state, resolution, attemptDir, map[string]any{"sha256": strings.Repeat("a", 64)}, credential, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if receipt["verdict"] != "RECONCILED_NO_PASS" {
		t.Fatalf("verdict = %v", receipt["verdict"])
	}
	pending, err := native1CReadJSON(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	if !isSHA256(asStringOr(pending["reconciled_identity_sha256"])) || !isSHA256(asStringOr(pending["reconciled_receipt_sha256"])) {
		t.Fatalf("pending markers missing: %v", pending)
	}
	if _, err := Native1CCompleteRecovery(state, resolution, runtime); err != nil {
		t.Fatal(err)
	}
	if nativeDependencyRegularFile(pendingPath) {
		t.Fatal("pending latch not cleared")
	}
	// Duplicate finalize is idempotent.
	if _, err := Native1CCompleteRecovery(state, resolution, runtime); err != nil {
		t.Fatal(err)
	}
}

func TestNative1CRecoveryReceiptFirstCrashRepair(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("physical target identity is windows-only")
	}
	root, journal := nativeRecoveryFixture(t)
	target := nativeRecoveryTarget(t, root, "receipt-crash")
	attemptID := "attempt-receipt-crash"
	taskID := randomUUIDForTest(t)
	state := nativeRecoveryState(target, attemptID, taskID, true)
	attemptDir := nativeRecoveryAttempt(t, root, "receipt-crash")
	inventory := nativeRecoveryInventory(target)
	hash, err := Native1CInventoryHash(inventory)
	if err != nil {
		t.Fatal(err)
	}
	resolution := nativeRecoveryResolution(target, attemptID, hash)
	pendingPath := nativeRecoveryWritePending(t, journal, state, target, attemptID)
	runtime := nativeRecoveryRuntimeFor(journal)
	runtime.ControlRead = func(ctx context.Context, target, executable string, credential Native1CRuntimeAuth, directory string) (map[string]any, error) {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return nil, err
		}
		return inventory, nil
	}
	credential := &Native1CRuntimeAuth{Username: "offline-user", Password: "offline-dummy-password"}
	manifest := map[string]any{"sha256": strings.Repeat("a", 64)}
	if _, err := Native1CResolveRecovery(context.Background(), state, resolution, attemptDir, manifest, credential, runtime); err != nil {
		t.Fatal(err)
	}
	// Simulate controller death after the receipt write and before the
	// pending update: rewrite the unreconciled pending record.
	nativeRecoveryWritePending(t, journal, state, target, attemptID, true)
	if _, err := Native1CResolveRecovery(context.Background(), state, resolution, attemptDir, manifest, credential, runtime); err != nil {
		t.Fatal(err)
	}
	pending, err := native1CReadJSON(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	if !isSHA256(asStringOr(pending["reconciled_identity_sha256"])) || !isSHA256(asStringOr(pending["reconciled_receipt_sha256"])) {
		t.Fatalf("repaired markers missing: %v", pending)
	}
	if _, err := Native1CCompleteRecovery(state, resolution, runtime); err != nil {
		t.Fatal(err)
	}
	if nativeDependencyRegularFile(pendingPath) {
		t.Fatal("repaired recovery could not finalize its latch")
	}
}

func TestNative1CRecoveryRejectsMismatchWithoutLedgerWrites(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("physical target identity is windows-only")
	}
	root, journal := nativeRecoveryFixture(t)
	target := nativeRecoveryTarget(t, root, "mismatch")
	attemptID := "attempt-mismatch"
	taskID := randomUUIDForTest(t)
	state := nativeRecoveryState(target, attemptID, taskID, false)
	attemptDir := nativeRecoveryAttempt(t, root, "mismatch")
	inventory := nativeRecoveryInventory(target)
	hash, err := Native1CInventoryHash(inventory)
	if err != nil {
		t.Fatal(err)
	}
	pendingPath := nativeRecoveryWritePending(t, journal, state, target, attemptID)
	runtime := nativeRecoveryRuntimeFor(journal)
	runtime.ControlRead = func(ctx context.Context, target, executable string, credential Native1CRuntimeAuth, directory string) (map[string]any, error) {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return nil, err
		}
		return inventory, nil
	}
	credential := &Native1CRuntimeAuth{Username: "offline-user", Password: "offline-dummy-password"}
	manifest := map[string]any{"sha256": strings.Repeat("a", 64)}
	before, err := native1CFileSHA256(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	// Wrong target.
	otherTarget := nativeRecoveryTarget(t, root, "other")
	bad := nativeRecoveryResolution(otherTarget, attemptID, hash)
	if _, err := Native1CResolveRecovery(context.Background(), state, bad, attemptDir, manifest, credential, runtime); err == nil {
		t.Fatal("wrong target was accepted")
	}
	// Wrong source hash.
	bad = nativeRecoveryResolution(target, attemptID, hash)
	bad["source_sha256"] = strings.Repeat("0", 64)
	if _, err := Native1CResolveRecovery(context.Background(), state, bad, attemptDir, manifest, credential, runtime); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("wrong source hash: %v", err)
	}
	// Wrong inventory hash.
	bad = nativeRecoveryResolution(target, attemptID, strings.Repeat("0", 64))
	if _, err := Native1CResolveRecovery(context.Background(), state, bad, attemptDir, manifest, credential, runtime); err == nil || !strings.Contains(err.Error(), "inventory control read") {
		t.Fatalf("wrong inventory hash: %v", err)
	}
	// Cross-task pending ledger blocks a new recovery.
	otherState := nativeRecoveryState(target, "other-attempt", randomUUIDForTest(t), false)
	bad = nativeRecoveryResolution(target, "other-attempt", hash)
	if _, err := Native1CResolveRecovery(context.Background(), otherState, bad, attemptDir, manifest, credential, runtime); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("cross-task pending: %v", err)
	}
	after, err := native1CFileSHA256(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("rejected recovery changed the pending ledger")
	}
	receipts := filepath.Join(journal, "recovery")
	if isNativeDir(receipts) {
		entries, err := os.ReadDir(receipts)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
				t.Fatal("rejected recovery wrote a receipt")
			}
		}
	}
}

func TestNative1CRecoveryPreparedGapFailsClosed(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("physical target identity is windows-only")
	}
	root, journal := nativeRecoveryFixture(t)
	target := nativeRecoveryTarget(t, root, "prepared-gap")
	attemptID := "attempt-prepared-gap"
	taskID := randomUUIDForTest(t)
	state := nativeRecoveryState(target, attemptID, taskID, false)
	attemptDir := nativeRecoveryAttempt(t, root, "prepared-gap")
	stepDir := filepath.Join(attemptDir, "steps", "load")
	if err := os.MkdirAll(stepDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteCanonical(filepath.Join(stepDir, "prepared.json"), map[string]any{"step": "load"}); err != nil {
		t.Fatal(err)
	}
	inventory := nativeRecoveryInventory(target)
	hash, err := Native1CInventoryHash(inventory)
	if err != nil {
		t.Fatal(err)
	}
	resolution := nativeRecoveryResolution(target, attemptID, hash)
	nativeRecoveryWritePending(t, journal, state, target, attemptID)
	runtime := nativeRecoveryRuntimeFor(journal)
	credential := &Native1CRuntimeAuth{Username: "offline-user", Password: "offline-dummy-password"}
	_, err = Native1CResolveRecovery(context.Background(), state, resolution, attemptDir, map[string]any{"sha256": strings.Repeat("a", 64)}, credential, runtime)
	if err == nil || !strings.Contains(err.Error(), "process identity gap") {
		t.Fatalf("prepared gap accepted: %v", err)
	}
}

func TestNative1CRecoveryLiveChildBlocks(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("live process identity is windows-only")
	}
	root, journal := nativeRecoveryFixture(t)
	target := nativeRecoveryTarget(t, root, "live-process")
	attemptID := "attempt-live-process"
	taskID := randomUUIDForTest(t)
	state := nativeRecoveryState(target, attemptID, taskID, false)
	attemptDir := nativeRecoveryAttempt(t, root, "live-process")
	command := exec.Command("ping", "-n", "30", "127.0.0.1")
	if err := command.Start(); err != nil {
		t.Skipf("ping unavailable: %v", err)
	}
	defer func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	}()
	startTime, err := native1CProcessStartTime(int64(command.Process.Pid))
	if err != nil || startTime.IsZero() {
		t.Skipf("process start time unavailable: %v", err)
	}
	stepDir := filepath.Join(attemptDir, "steps", "load")
	if err := os.MkdirAll(stepDir, 0o755); err != nil {
		t.Fatal(err)
	}
	identity := map[string]any{
		"pid":               int64(command.Process.Pid),
		"start_time_utc":    startTime.UTC().Format("2006-01-02T15:04:05.0000000Z"),
		"non_interruptible": true,
	}
	if err := AtomicWriteCanonical(filepath.Join(stepDir, "process.json"), identity); err != nil {
		t.Fatal(err)
	}
	inventory := nativeRecoveryInventory(target)
	hash, err := Native1CInventoryHash(inventory)
	if err != nil {
		t.Fatal(err)
	}
	resolution := nativeRecoveryResolution(target, attemptID, hash)
	nativeRecoveryWritePending(t, journal, state, target, attemptID)
	runtime := nativeRecoveryRuntimeFor(journal)
	credential := &Native1CRuntimeAuth{Username: "offline-user", Password: "offline-dummy-password"}
	_, err = Native1CResolveRecovery(context.Background(), state, resolution, attemptDir, map[string]any{"sha256": strings.Repeat("a", 64)}, credential, runtime)
	if err == nil || !strings.Contains(err.Error(), "child process is still running") {
		t.Fatalf("live child did not block: %v", err)
	}
}

// --- saved success fixtures ---

type nativeSavedFixture struct {
	state     map[string]any
	criterion map[string]any
	rawDir    string
	attempt   string
	target    string
	journal   string
	sourceDir string
	junit     string
	taskID    string
}

func newNativeSavedFixture(t *testing.T, root, journal, name string, pending bool, pendingTaskID string) *nativeSavedFixture {
	t.Helper()
	target := nativeRecoveryTarget(t, root, name+"-target")
	attemptID := "attempt-" + name
	taskID := randomUUIDForTest(t)
	sourceDir := filepath.Join(root, "saved", name, "worker", "src", "ext")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	uuidValue := "11111111-1111-1111-1111-111111111111"
	configuration := `<MetaDataObject><Configuration uuid="` + uuidValue + `"><Properties><Name>Ext</Name><Version>1</Version></Properties></Configuration></MetaDataObject>`
	if err := os.WriteFile(filepath.Join(sourceDir, "Configuration.xml"), []byte(configuration), 0o644); err != nil {
		t.Fatal(err)
	}
	native := map[string]any{
		"source_root": "src/ext", "extension": "Ext", "module": "FixtureModule",
		"platform_version": "8.3.27.2074", "executable_sha256": strings.Repeat("1", 64),
		"authorized_operations":   []any{"inventory", "load", "update", "test"},
		"authorization_reference": "offline-fixture",
	}
	criterion := map[string]any{
		"id": "native", "kind": "integration", "target": target,
		"executable":     filepath.Join(os.TempDir(), "1cv8.exe"),
		"expected_tests": []any{"FixtureModule.ExactCase"},
		"native_1c":      native,
	}
	state := map[string]any{
		"task_id":     taskID,
		"worker_path": filepath.Join(root, "saved", name, "worker"),
		"evidence":    []any{},
		"events":      []any{},
		"request":     map[string]any{"criteria": []any{criterion}},
	}
	rawDir := filepath.Join(root, "saved", name, "raw", "native")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Source snapshot copy.
	snapshotDir := filepath.Join(rawDir, "source-snapshot")
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(sourceDir, "Configuration.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "Configuration.xml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := Native1CSourceSnapshot(sourceDir)
	if err != nil {
		t.Fatal(err)
	}
	targetKey := strings.Repeat("d", 64)
	if runtime.GOOS == "windows" {
		targetKey, err = Native1CTargetKey(target)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := map[string]any{
		"schema_version": int64(1), "kind": "bsl-flow.native-1c-request",
		"task_id": taskID, "attempt_id": attemptID, "criterion_id": "native",
		"target": target, "target_key": targetKey,
		"source": map[string]any{
			"extension": asStringOr(source["extension"]),
			"version":   asStringOr(source["version"]),
			"uuid":      asStringOr(source["uuid"]),
			"sha256":    asStringOr(source["sha256"]),
		},
		"platform": map[string]any{
			"version":           native["platform_version"],
			"executable_sha256": native["executable_sha256"],
		},
		"operations":              native["authorized_operations"],
		"authorization_reference": native["authorization_reference"],
		"expected_tests":          criterion["expected_tests"],
	}
	if err := AtomicWriteCanonical(filepath.Join(rawDir, "runtime-request.json"), request); err != nil {
		t.Fatal(err)
	}
	inventory := map[string]any{
		"target":   target,
		"platform": map[string]any{"executable": "offline", "com_connector": "offline"},
		"extensions": []any{map[string]any{"properties": map[string]any{
			"name": "Ext", "version": "1", "active": true,
			"purpose": "Customization", "scope": "InfoBase",
			"uuid": uuidValue, "hash_sum": "AA",
		}}},
	}
	for _, doc := range []string{"inventory-before.json", "inventory-after.json"} {
		if err := AtomicWriteCanonical(filepath.Join(rawDir, doc), inventory); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	requestHash, err := Hash(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, stepName := range []string{"load", "update", "test"} {
		stepDir := filepath.Join(rawDir, "steps", stepName)
		if err := os.MkdirAll(stepDir, 0o755); err != nil {
			t.Fatal(err)
		}
		log := filepath.Join(stepDir, stepName+".log")
		if err := os.WriteFile(log, []byte(stepName+" completed"), 0o644); err != nil {
			t.Fatal(err)
		}
		logHash, err := native1CFileSHA256(log)
		if err != nil {
			t.Fatal(err)
		}
		identity := map[string]any{
			"pid":               int64(2147483647),
			"start_time_utc":    now.Add(-time.Second).Format("2006-01-02T15:04:05.0000000Z"),
			"request_sha256":    requestHash,
			"step":              stepName,
			"non_interruptible": true,
		}
		terminal := map[string]any{
			"request_sha256":  requestHash,
			"process":         identity,
			"exit_code":       int64(0),
			"finished_at_utc": now.Format("2006-01-02T15:04:05.0000000Z"),
			"log":             log,
			"log_sha256":      logHash,
		}
		if err := AtomicWriteCanonical(filepath.Join(stepDir, "terminal.json"), terminal); err != nil {
			t.Fatal(err)
		}
	}
	junit := filepath.Join(rawDir, "original.junit.xml")
	junitContent := `<testsuite tests="1" failures="0" errors="0" skipped="0"><testcase classname="FixtureModule" name="ExactCase"/></testsuite>`
	if err := os.WriteFile(junit, []byte(junitContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(junit, now, now); err != nil {
		t.Fatal(err)
	}
	junitHash := fileSHA256([]byte(junitContent))
	beforeHash, err := Native1CInventoryHash(inventory)
	if err != nil {
		t.Fatal(err)
	}
	receipt := map[string]any{
		"schema_version":          int64(1),
		"kind":                    "bsl-flow.native-1c-success",
		"task_id":                 taskID,
		"attempt_id":              attemptID,
		"request_sha256":          requestHash,
		"source_sha256":           asStringOr(source["sha256"]),
		"inventory_before_sha256": beforeHash,
		"inventory_after_sha256":  beforeHash,
		"junit_sha256":            junitHash,
		"tests":                   criterion["expected_tests"],
		"completed_at_utc":        now.Format("2006-01-02T15:04:05.0000000Z"),
	}
	if err := AtomicWriteCanonical(filepath.Join(rawDir, "runtime-success.json"), receipt); err != nil {
		t.Fatal(err)
	}
	if pending {
		if err := os.MkdirAll(journal, 0o755); err != nil {
			t.Fatal(err)
		}
		pendingID := taskID
		if pendingTaskID != "" {
			pendingID = pendingTaskID
		}
		if err := AtomicWriteCanonical(filepath.Join(journal, "pending.json"), map[string]any{
			"schema_version": int64(1), "state": "dispatched_or_unknown",
			"task_id": pendingID, "attempt_id": attemptID, "criterion_id": "native",
			"target": target, "full_source_sha256": strings.Repeat("a", 64),
			"request_sha256": requestHash,
		}); err != nil {
			t.Fatal(err)
		}
	}
	return &nativeSavedFixture{
		state: state, criterion: criterion, rawDir: rawDir, attempt: attemptID,
		target: target, journal: journal, sourceDir: sourceDir, junit: junit, taskID: taskID,
	}
}

func TestNative1CSavedObservationAndCompletion(t *testing.T) {
	root, journal := nativeRecoveryFixture(t)
	fixture := newNativeSavedFixture(t, root, journal, "saved-success", true, "")
	manifest := map[string]any{"sha256": "saved"}
	observation, err := Native1CSavedObservation(fixture.state, fixture.criterion, fixture.rawDir, fixture.attempt, filepath.Join(root, "tasks", fixture.taskID), manifest)
	if err != nil {
		t.Fatal(err)
	}
	if observation["outcome"] != "PASS" {
		t.Fatalf("outcome = %v", observation["outcome"])
	}
	runtime := nativeRecoveryRuntimeFor(journal)
	if err := Native1CCompleteSavedSuccess(fixture.taskID, fixture.attempt, fixture.rawDir, runtime); err != nil {
		t.Fatal(err)
	}
	if nativeDependencyRegularFile(filepath.Join(journal, "pending.json")) {
		t.Fatal("pending latch not cleared")
	}
	if !nativeDependencyRegularFile(filepath.Join(journal, "history", fixture.attempt+".success.json")) {
		t.Fatal("history receipt missing")
	}
	// Duplicate finalize is idempotent.
	if err := Native1CCompleteSavedSuccess(fixture.taskID, fixture.attempt, fixture.rawDir, runtime); err != nil {
		t.Fatal(err)
	}
}

func TestNative1CSavedObservationRejectsTampering(t *testing.T) {
	root, journal := nativeRecoveryFixture(t)
	fixture := newNativeSavedFixture(t, root, journal, "saved-tamper", false, "")
	manifest := map[string]any{"sha256": "saved"}
	// Changed source.
	file, err := os.OpenFile(filepath.Join(fixture.sourceDir, "Configuration.xml"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("<!-- changed -->"); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := Native1CSavedObservation(fixture.state, fixture.criterion, fixture.rawDir, fixture.attempt, filepath.Join(root, "tasks", fixture.taskID), manifest); err == nil ||
		!strings.Contains(err.Error(), "source snapshot is stale") {
		t.Fatalf("changed source accepted: %v", err)
	}
	// Restore source, tamper with a terminal log.
	configuration := `<MetaDataObject><Configuration uuid="11111111-1111-1111-1111-111111111111"><Properties><Name>Ext</Name><Version>1</Version></Properties></Configuration></MetaDataObject>`
	if err := os.WriteFile(filepath.Join(fixture.sourceDir, "Configuration.xml"), []byte(configuration), 0o644); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(fixture.rawDir, "steps", "test", "test.log"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := logFile.WriteString(" tampered"); err != nil {
		t.Fatal(err)
	}
	logFile.Close()
	if _, err := Native1CSavedObservation(fixture.state, fixture.criterion, fixture.rawDir, fixture.attempt, filepath.Join(root, "tasks", fixture.taskID), manifest); err == nil ||
		!strings.Contains(err.Error(), "incomplete saved native terminal evidence") {
		t.Fatalf("tampered terminal accepted: %v", err)
	}
}

func TestNative1CSavedSuccessOtherTaskLatchUntouched(t *testing.T) {
	root, journal := nativeRecoveryFixture(t)
	otherTask := randomUUIDForTest(t)
	fixture := newNativeSavedFixture(t, root, journal, "saved-other-task", true, otherTask)
	pendingPath := filepath.Join(journal, "pending.json")
	before, err := native1CFileSHA256(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	runtime := nativeRecoveryRuntimeFor(journal)
	if err := Native1CCompleteSavedSuccess(fixture.taskID, fixture.attempt, fixture.rawDir, runtime); err != nil {
		t.Fatal(err)
	}
	after, err := native1CFileSHA256(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	if before != after || !nativeDependencyRegularFile(pendingPath) {
		t.Fatal("saved success touched another task pending latch")
	}
}

type nativeRecordedFixture struct {
	fixture     *nativeSavedFixture
	attemptDir  string
	rawRoot     string
	contextRoot string
	entryResult map[string]any
}

func newNativeRecordedFixture(t *testing.T, root, journal, name string) *nativeRecordedFixture {
	t.Helper()
	fixture := newNativeSavedFixture(t, root, journal, name, true, "")
	attemptDir := filepath.Join(root, "recorded", name, "attempt")
	artifactsRoot := filepath.Join(attemptDir, "artifacts")
	rawRoot := filepath.Join(artifactsRoot, "raw")
	nativeDir := filepath.Join(rawRoot, "native")
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, doc := range []string{"runtime-request.json", "runtime-success.json"} {
		data, err := os.ReadFile(filepath.Join(fixture.rawDir, doc))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nativeDir, doc), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rawHashes := []any{}
	for _, relative := range []string{"runtime-request.json", "runtime-success.json"} {
		hash, err := native1CFileSHA256(filepath.Join(nativeDir, relative))
		if err != nil {
			t.Fatal(err)
		}
		rawHashes = append(rawHashes, map[string]any{
			"path":   "attempts/" + fixture.attempt + "/raw/native/" + relative,
			"sha256": hash,
		})
	}
	terminal := map[string]any{
		"schema_version": int64(1), "task_id": fixture.taskID, "attempt_id": fixture.attempt,
		"stage": "verify", "outcome": "PASS", "summary": "Recorded native fixture PASS.",
		"raw_hashes": rawHashes,
	}
	if err := AtomicWriteCanonical(filepath.Join(attemptDir, "terminal.json"), terminal); err != nil {
		t.Fatal(err)
	}
	resultHash, err := Hash(terminal)
	if err != nil {
		t.Fatal(err)
	}
	fixture.state["project_path"] = root
	fixture.state["evidence"] = []any{map[string]any{
		"stage": "verify", "attempt_id": fixture.attempt, "outcome": "PASS",
		"result_sha256": resultHash, "raw_hashes": rawHashes,
	}}
	return &nativeRecordedFixture{fixture: fixture, attemptDir: attemptDir, rawRoot: rawRoot, contextRoot: attemptDir, entryResult: terminal}
}

func TestNative1CRecordedSuccessCompletesLatch(t *testing.T) {
	root, journal := nativeRecoveryFixture(t)
	recorded := newNativeRecordedFixture(t, root, journal, "recorded-success")
	runtime := nativeRecoveryRuntimeFor(journal)
	if err := Native1CCompleteRecordedSuccess(recorded.fixture.state, recorded.fixture.attempt, recorded.attemptDir, runtime); err != nil {
		t.Fatal(err)
	}
	if nativeDependencyRegularFile(filepath.Join(journal, "pending.json")) {
		t.Fatal("pending latch not cleared")
	}
	if !nativeDependencyRegularFile(filepath.Join(journal, "history", recorded.fixture.attempt+".success.json")) {
		t.Fatal("history receipt missing")
	}
	history := filepath.Join(journal, "history", recorded.fixture.attempt+".success.json")
	before, err := native1CFileSHA256(history)
	if err != nil {
		t.Fatal(err)
	}
	if err := Native1CCompleteRecordedSuccess(recorded.fixture.state, recorded.fixture.attempt, recorded.attemptDir, runtime); err != nil {
		t.Fatal(err)
	}
	after, err := native1CFileSHA256(history)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("repeated completion changed accepted history")
	}
}

func TestNative1CRecordedSuccessRejectsTamperedEvidence(t *testing.T) {
	root, journal := nativeRecoveryFixture(t)
	recorded := newNativeRecordedFixture(t, root, journal, "recorded-tamper")
	pendingPath := filepath.Join(journal, "pending.json")
	before, err := native1CFileSHA256(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	requestFile, err := os.OpenFile(filepath.Join(recorded.rawRoot, "native", "runtime-request.json"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := requestFile.WriteString(" tampered"); err != nil {
		t.Fatal(err)
	}
	requestFile.Close()
	runtime := nativeRecoveryRuntimeFor(journal)
	if err := Native1CCompleteRecordedSuccess(recorded.fixture.state, recorded.fixture.attempt, recorded.attemptDir, runtime); err == nil ||
		!strings.Contains(err.Error(), "recorded native evidence changed") {
		t.Fatalf("tampered evidence accepted: %v", err)
	}
	after, err := native1CFileSHA256(pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("rejected recorded evidence changed the pending latch")
	}
}

func randomUUIDForTest(t *testing.T) string {
	t.Helper()
	value, err := randomUUID()
	if err != nil {
		t.Fatal(err)
	}
	return value
}
