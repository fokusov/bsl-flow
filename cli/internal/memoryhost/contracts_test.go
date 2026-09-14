package memoryhost

import (
	"path/filepath"
	"strings"
	"testing"
)

const (
	testTaskID     = "11111111-2222-3333-4444-555555555555"
	testAttemptID  = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testFailureID  = "99999999-8888-7777-6666-555555555555"
	testPolicyHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func baseState(project string) map[string]any {
	return map[string]any{
		"schema_version": jsonNumberOf("1"),
		"task_id":        testTaskID,
		"project_path":   project,
		"policy_hash":    testPolicyHash,
		"status":         "completed",
		"stage":          "acceptance",
		"request": map[string]any{
			"mode":          "implement",
			"analysis_goal": "analysis",
			"criteria":      []any{map[string]any{"kind": "file_assertion"}},
			"impact_flags":  []any{},
			"source_paths":  []any{"src"},
		},
		"classification": map[string]any{"risk": "low", "impact_flags": []any{}},
	}
}

func baseResult() map[string]any {
	return map[string]any{
		"schema_version": jsonNumberOf("1"),
		"task_id":        testTaskID,
		"attempt_id":     testAttemptID,
		"stage":          "verify",
		"outcome":        "FAIL",
		"side_effects":   "none",
	}
}

func baseReceipt() map[string]any {
	return map[string]any{
		"schema_version": jsonNumberOf("1"),
		"task_id":        testTaskID,
		"verdict":        "PASS",
	}
}

func basePendingFailure() map[string]any {
	return map[string]any{
		"schema_version": jsonNumberOf("1"),
		"task_id":        testTaskID,
		"attempt_id":     testFailureID,
		"stage":          "verify",
		"outcome":        "FAIL",
		"side_effects":   "none",
	}
}

func memoryInput(project string, operation, stage string) map[string]any {
	return map[string]any{
		"schema_version":              jsonNumberOf("1"),
		"operation":                   operation,
		"state":                       baseState(project),
		"stage":                       stage,
		"result":                      nil,
		"result_hash":                 "",
		"receipt":                     nil,
		"receipt_hash":                "",
		"next":                        nil,
		"memory_root":                 filepath.Join(project, ".bsl-flow", "memory"),
		"pending_failure_result":      nil,
		"pending_failure_result_hash": "",
	}
}

func assertInputError(t *testing.T, input map[string]any, want string) {
	t.Helper()
	_, err := assertNativeMemoryInput(input)
	if err == nil {
		t.Fatalf("expected error %q, got none", want)
	}
	if err.Error() != want {
		t.Fatalf("error mismatch:\n got %q\nwant %q", err.Error(), want)
	}
}

func TestAssertInputAcceptsValidOperations(t *testing.T) {
	project := t.TempDir()
	input := memoryInput(project, "bind", "implement")
	if _, err := assertNativeMemoryInput(input); err != nil {
		t.Fatalf("bind rejected: %v", err)
	}
	result := baseResult()
	resultHash, err := hashValue(result)
	if err != nil {
		t.Fatal(err)
	}
	input = memoryInput(project, "extract-attempt", "")
	input["result"] = result
	input["result_hash"] = resultHash
	if _, err := assertNativeMemoryInput(input); err != nil {
		t.Fatalf("extract-attempt rejected: %v", err)
	}
	receipt := baseReceipt()
	receiptHash, err := hashValue(receipt)
	if err != nil {
		t.Fatal(err)
	}
	input = memoryInput(project, "extract-acceptance", "")
	input["receipt"] = receipt
	input["receipt_hash"] = receiptHash
	if _, err := assertNativeMemoryInput(input); err != nil {
		t.Fatalf("extract-acceptance rejected: %v", err)
	}
	input = memoryInput(project, "projection", "")
	if _, err := assertNativeMemoryInput(input); err != nil {
		t.Fatalf("projection rejected: %v", err)
	}
}

func TestAssertInputExactErrors(t *testing.T) {
	project := t.TempDir()
	result := baseResult()
	resultHash, err := hashValue(result)
	if err != nil {
		t.Fatal(err)
	}
	receipt := baseReceipt()
	receiptHash, err := hashValue(receipt)
	if err != nil {
		t.Fatal(err)
	}
	pending := basePendingFailure()
	pendingHash, err := hashValue(pending)
	if err != nil {
		t.Fatal(err)
	}

	// Root shape.
	input := memoryInput(project, "bind", "implement")
	input["extra"] = nil
	assertInputError(t, input, "BF_INVALID: Unknown field native memory input.extra.")
	delete(input, "extra")
	delete(input, "receipt")
	assertInputError(t, input, "BF_INVALID: native memory input.receipt is required.")
	input["receipt"] = nil
	input["schema_version"] = jsonNumberOf("2")
	assertInputError(t, input, "BF_INVALID: Unsupported native memory input schema_version.")
	input["schema_version"] = jsonNumberOf("1")
	input["operation"] = "nope"
	assertInputError(t, input, "BF_INVALID: Unsupported native memory operation.")
	input["operation"] = "bind"

	// State contract.
	input["state"] = nil
	assertInputError(t, input, "BF_INVALID: state must be an object.")
	input["state"] = map[string]any{"schema_version": jsonNumberOf("2"), "task_id": testTaskID, "project_path": project}
	assertInputError(t, input, "BF_INVALID: state.schema_version must be 1.")
	state := baseState(project)
	delete(state, "task_id")
	input["state"] = state
	assertInputError(t, input, "BF_INVALID: state.task_id is required.")
	state["task_id"] = "not-a-uuid"
	assertInputError(t, input, "BF_INVALID: identity must be a canonical lower-case UUID.")
	state["task_id"] = strings.Repeat("x", 65)
	assertInputError(t, input, "BF_INVALID: state.task_id must be a bounded string.")
	state["task_id"] = testTaskID
	delete(state, "project_path")
	assertInputError(t, input, "BF_INVALID: state.project_path is required.")
	state["project_path"] = project

	// Memory root binding.
	input["memory_root"] = strings.Repeat(" ", 20)
	assertInputError(t, input, "BF_INVALID: memory_root must be a bounded string.")
	input["memory_root"] = "relative/path"
	assertInputError(t, input, "BF_INVALID: Path must be an absolute filesystem path.")
	input["memory_root"] = filepath.Join(project, ".bsl-flow", "other")
	assertInputError(t, input, "BF_INVALID: memory_root must resolve to state.project_path/.bsl-flow/memory.")
	occupied := filepath.Join(project, ".bsl-flow", "memory")
	if err := mkdirAllForTest(filepath.Join(project, ".bsl-flow")); err != nil {
		t.Fatal(err)
	}
	if err := writeFileForTest(occupied, "x"); err != nil {
		t.Fatal(err)
	}
	input["memory_root"] = occupied
	assertInputError(t, input, "BF_BLOCKED: memory_root is occupied by a file.")
	if err := removeForTest(occupied); err != nil {
		t.Fatal(err)
	}
	input["memory_root"] = filepath.Join(project, ".bsl-flow", "memory")

	// Stage shape.
	input["stage"] = jsonNumberOf("5")
	assertInputError(t, input, "BF_INVALID: stage must be a bounded string.")
	input["stage"] = "bogus"
	assertInputError(t, input, "BF_INVALID: stage is not a controller stage.")
	input["stage"] = ""
	assertInputError(t, input, "BF_INVALID: bind requires a non-empty stage.")
	input["operation"] = "projection"
	input["stage"] = "implement"
	input["result"] = result
	assertInputError(t, input, "BF_INVALID: projection accepts no result or receipt fields.")
	input["result"] = nil
	input["stage"] = ""

	// Hash shape.
	input["result_hash"] = jsonNumberOf("1")
	assertInputError(t, input, "BF_INVALID: result_hash must be a SHA-256 string or null.")
	input["result_hash"] = "ABC"
	assertInputError(t, input, "BF_INVALID: result_hash must be a lower-case SHA-256 string.")
	input["result_hash"] = ""

	// Pending failure contract.
	input["pending_failure_result_hash"] = pendingHash
	assertInputError(t, input, "BF_INVALID: pending_failure_result_hash requires pending_failure_result.")
	input["pending_failure_result"] = pending
	// A null hash reaches the "requires" gate; an empty string is rejected by
	// the SHA-256 shape check first, exactly like Assert-BFNativeMemoryHash.
	input["pending_failure_result_hash"] = nil
	assertInputError(t, input, "BF_INVALID: pending_failure_result requires pending_failure_result_hash.")
	input["pending_failure_result_hash"] = ""
	assertInputError(t, input, "BF_INVALID: pending_failure_result_hash must be a lower-case SHA-256 string.")
	input["pending_failure_result_hash"] = "zz" + pendingHash[2:]
	assertInputError(t, input, "BF_INVALID: pending_failure_result_hash must be a lower-case SHA-256 string.")
	broken := basePendingFailure()
	broken["task_id"] = testAttemptID
	brokenHash, err := hashValue(broken)
	if err != nil {
		t.Fatal(err)
	}
	input["pending_failure_result"] = broken
	input["pending_failure_result_hash"] = brokenHash
	assertInputError(t, input, "BF_CONFLICT: pending_failure_result.task_id does not match state.task_id.")
	input["pending_failure_result"] = pending
	assertInputError(t, input, "BF_CONFLICT: pending_failure_result.attempt_id does not match state.repair.pending_failure.")
	state["repair"] = map[string]any{"rounds": 1, "pending_failure": testFailureID}
	dirty := basePendingFailure()
	dirty["side_effects"] = "unknown"
	dirtyHash, err := hashValue(dirty)
	if err != nil {
		t.Fatal(err)
	}
	input["pending_failure_result"] = dirty
	input["pending_failure_result_hash"] = dirtyHash
	assertInputError(t, input, "BF_BLOCKED: pending_failure_result is not a clean failed verification.")
	input["pending_failure_result"] = pending
	input["pending_failure_result_hash"] = strings.Repeat("0", 64)
	assertInputError(t, input, "BF_CONFLICT: pending_failure_result_hash does not match pending_failure_result.")
	input["pending_failure_result"] = nil
	input["pending_failure_result_hash"] = ""
	delete(state, "repair")

	// bind slot rules.
	input["operation"] = "bind"
	input["stage"] = "implement"
	input["receipt"] = receipt
	assertInputError(t, input, "BF_INVALID: bind accepts no result or receipt fields.")
	input["receipt"] = nil
	input["receipt_hash"] = receiptHash
	assertInputError(t, input, "BF_INVALID: bind accepts no result or receipt fields.")
	input["receipt_hash"] = ""

	// extract-attempt contract.
	input["operation"] = "extract-attempt"
	input["stage"] = ""
	input["pending_failure_result"] = pending
	input["pending_failure_result_hash"] = pendingHash
	state["repair"] = map[string]any{"rounds": 1, "pending_failure": testFailureID}
	assertInputError(t, input, "BF_INVALID: extract-attempt accepts no pending failure result.")
	input["pending_failure_result"] = nil
	input["pending_failure_result_hash"] = ""
	delete(state, "repair")
	assertInputError(t, input, "BF_INVALID: extract-attempt requires result and result_hash.")
	input["result"] = result
	input["result_hash"] = ""
	assertInputError(t, input, "BF_INVALID: extract-attempt requires result and result_hash.")
	input["result_hash"] = resultHash
	input["receipt"] = receipt
	assertInputError(t, input, "BF_INVALID: extract-attempt accepts no receipt fields.")
	input["receipt"] = nil
	mismatched := baseResult()
	mismatched["task_id"] = testFailureID
	mismatchedHash, err := hashValue(mismatched)
	if err != nil {
		t.Fatal(err)
	}
	input["result"] = mismatched
	input["result_hash"] = mismatchedHash
	assertInputError(t, input, "BF_CONFLICT: result.task_id does not match state.task_id.")
	wrongStage := baseResult()
	wrongStage["stage"] = "bogus"
	wrongStageHash, err := hashValue(wrongStage)
	if err != nil {
		t.Fatal(err)
	}
	input["result"] = wrongStage
	input["result_hash"] = wrongStageHash
	assertInputError(t, input, "BF_INVALID: result.stage is not a controller stage.")
	nonTerminal := baseResult()
	nonTerminal["outcome"] = "RETRY"
	nonTerminalHash, err := hashValue(nonTerminal)
	if err != nil {
		t.Fatal(err)
	}
	input["result"] = nonTerminal
	input["result_hash"] = nonTerminalHash
	assertInputError(t, input, "BF_INVALID: result.outcome is not terminal.")
	input["result"] = result
	input["result_hash"] = strings.Repeat("1", 64)
	assertInputError(t, input, "BF_CONFLICT: result_hash does not match result.")
	input["result_hash"] = resultHash
	input["stage"] = "implement"
	assertInputError(t, input, "BF_CONFLICT: result.stage does not match stage.")
	input["stage"] = ""

	// extract-acceptance contract.
	input["operation"] = "extract-acceptance"
	assertInputError(t, input, "BF_INVALID: extract-acceptance requires receipt and receipt_hash.")
	input["result"] = nil
	input["result_hash"] = ""
	input["receipt"] = receipt
	input["receipt_hash"] = receiptHash
	rejected := baseReceipt()
	rejected["verdict"] = "FAIL"
	rejectedHash, err := hashValue(rejected)
	if err != nil {
		t.Fatal(err)
	}
	input["receipt"] = rejected
	input["receipt_hash"] = rejectedHash
	assertInputError(t, input, "BF_BLOCKED: acceptance receipt verdict must be PASS.")
	input["receipt"] = receipt
	input["receipt_hash"] = strings.Repeat("2", 64)
	assertInputError(t, input, "BF_CONFLICT: receipt_hash does not match receipt.")
	input["receipt_hash"] = receiptHash
	state["status"] = "running"
	assertInputError(t, input, "BF_BLOCKED: acceptance extraction requires completed controller state.")
	state["status"] = "completed"
	input["result"] = result
	input["result_hash"] = resultHash
	assertInputError(t, input, "BF_INVALID: extract-acceptance accepts no result fields.")
}

func TestAssertStateAndRootCaseInsensitiveMatch(t *testing.T) {
	project := t.TempDir()
	input := memoryInput(project, "bind", "implement")
	// A trailing separator and different case must still bind on Windows.
	root := filepath.Join(project, ".bsl-flow", "memory") + string(filepath.Separator)
	if filepath.Separator == '\\' {
		input["memory_root"] = strings.ToUpper(root)
	} else {
		input["memory_root"] = root
	}
	if _, err := assertStateAndRoot(input); err != nil {
		t.Fatalf("case-insensitive root rejected: %v", err)
	}
}

func TestAssertSafePathErrors(t *testing.T) {
	if _, err := assertSafePath("relative"); err == nil || err.Error() != "BF_INVALID: Path must be an absolute filesystem path." {
		t.Fatalf("relative path: %v", err)
	}
	if _, err := assertSafePath(""); err == nil || err.Error() != "BF_INVALID: Path must be an absolute filesystem path." {
		t.Fatalf("empty path: %v", err)
	}
	qualified := `\\?\C:\dev`
	if _, err := assertSafePath(qualified); err == nil || err.Error() != "BF_INVALID: Device and provider-qualified paths are not allowed." {
		t.Fatalf("device path: %v", err)
	}
	// A provider-qualified absolute path is rejected as not rooted, the same
	// order Assert-BFSafePath uses; only rooted qualified paths reach the
	// device/provider check.
	if _, err := assertSafePath("Registry::Machine"); err == nil || err.Error() != "BF_INVALID: Path must be an absolute filesystem path." {
		t.Fatalf("provider path: %v", err)
	}
}
