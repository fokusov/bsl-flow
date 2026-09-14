package memoryhost

// This file ports the request contract of Invoke-BFNativeMemory.ps1:
// Get-BFNativeMemoryObjectKeys, Test-BFNativeMemoryObjectProperty,
// Get-BFNativeMemoryValue and the Assert-BFNativeMemory* functions, with the
// exact BF_* message bytes. The decoded request is a strictjson document, so
// map lookups are exact-key and duplicate members were already rejected.

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

var nativeMemoryRootFields = []string{
	"schema_version", "operation", "state", "stage", "result", "result_hash",
	"receipt", "receipt_hash", "next", "memory_root", "pending_failure_result",
	"pending_failure_result_hash",
}

var nativeMemoryOperations = []string{"bind", "extract-attempt", "extract-acceptance", "projection"}

var nativeMemoryStages = []string{"inspect", "spec", "spec_review", "implement", "code_review", "verify", "diagnose", "acceptance"}

var nativeMemoryTerminalOutcomes = []string{"PASS", "FAIL", "BLOCKED", "NEEDS_INPUT", "REVISE", "REPAIR"}

// objectValue ports Get-BFNativeMemoryValue: exact, case-sensitive member
// lookup that yields nil for anything else.
func objectValue(value any, name string) any {
	if object, ok := value.(map[string]any); ok {
		if member, present := object[name]; present {
			return member
		}
	}
	return nil
}

func objectProperty(value any, name string) bool {
	if object, ok := value.(map[string]any); ok {
		_, present := object[name]
		return present
	}
	return false
}

// assertNativeMemoryObject ports Assert-BFNativeMemoryObject.
func assertNativeMemoryObject(value any, name string) error {
	if value == nil {
		return bfInvalid("%s must be an object.", name)
	}
	if _, ok := value.(map[string]any); !ok {
		return bfInvalid("%s must be an object.", name)
	}
	return nil
}

// assertNativeMemoryString ports Assert-BFNativeMemoryString.
func assertNativeMemoryString(value any, name string, maximum int, allowEmpty bool) (string, error) {
	text, isString := value.(string)
	if !isString || utf16Length(text) > maximum || (!allowEmpty && isNullOrWhiteSpace(text)) {
		return "", bfInvalid("%s must be a bounded string.", name)
	}
	return text, nil
}

// assertNativeMemoryExactFields ports Assert-BFNativeMemoryExactFields.
func assertNativeMemoryExactFields(value map[string]any, fields []string, name string) error {
	if err := assertNativeMemoryObject(value, name); err != nil {
		return err
	}
	for _, field := range fields {
		if _, present := value[field]; !present {
			return bfInvalid("%s.%s is required.", name, field)
		}
	}
	for key := range value {
		known := false
		for _, field := range fields {
			if key == field {
				known = true
				break
			}
		}
		if !known {
			return bfInvalid("Unknown field %s.%s.", name, key)
		}
	}
	return nil
}

// assertNativeMemoryMaybeObject ports Assert-BFNativeMemoryMaybeObject.
func assertNativeMemoryMaybeObject(value any, name string) error {
	if value != nil {
		return assertNativeMemoryObject(value, name)
	}
	return nil
}

// assertNativeMemoryHash ports Assert-BFNativeMemoryHash. The Go caller uses
// "" for the unused hash slots, which the wire contract treats as null.
func assertNativeMemoryHash(value any, name string, allowEmpty bool) error {
	if value == nil {
		return nil
	}
	text, isString := value.(string)
	if !isString {
		return bfInvalid("%s must be a SHA-256 string or null.", name)
	}
	if len(text) == 0 && allowEmpty {
		return nil
	}
	if !isHexHash(text) {
		return bfInvalid("%s must be a lower-case SHA-256 string.", name)
	}
	return nil
}

// equalsPSOne mirrors PowerShell's -cne comparison against the literal 1 for
// the schema_version gates: numbers compare numerically (1 and 1.0 match),
// the string "1" matches through right-side string coercion and $true
// matches through boolean coercion.
func equalsPSOne(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed == "1"
	case bool:
		return typed
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed == 1
		}
		if parsed, err := typed.Float64(); err == nil {
			return parsed == 1
		}
		return false
	case int:
		return typed == 1
	case int64:
		return typed == 1
	case float64:
		return typed == 1
	default:
		return false
	}
}

// assertStateAndRoot ports Assert-BFNativeMemoryStateAndRoot.
func assertStateAndRoot(input map[string]any) (map[string]any, error) {
	stateAny := objectValue(input, "state")
	if err := assertNativeMemoryObject(stateAny, "state"); err != nil {
		return nil, err
	}
	state := stateAny.(map[string]any)
	if !objectProperty(state, "schema_version") || !equalsPSOne(state["schema_version"]) {
		return nil, bfInvalid("state.schema_version must be 1.")
	}
	if !objectProperty(state, "task_id") {
		return nil, bfInvalid("state.task_id is required.")
	}
	taskID, err := assertNativeMemoryString(state["task_id"], "state.task_id", 64, false)
	if err != nil {
		return nil, err
	}
	if err := assertUUID(taskID); err != nil {
		return nil, err
	}
	if !objectProperty(state, "project_path") {
		return nil, bfInvalid("state.project_path is required.")
	}
	projectPathText, err := assertNativeMemoryString(state["project_path"], "state.project_path", 4096, false)
	if err != nil {
		return nil, err
	}
	projectPath, err := assertSafePath(projectPathText)
	if err != nil {
		return nil, err
	}
	declaredRootText, err := assertNativeMemoryString(objectValue(input, "memory_root"), "memory_root", 4096, false)
	if err != nil {
		return nil, err
	}
	declaredRoot, err := assertSafePath(declaredRootText)
	if err != nil {
		return nil, err
	}
	expectedRoot, err := assertSafePath(filepath.Join(projectPath, ".bsl-flow", "memory"))
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(trimPathSeparators(declaredRoot), trimPathSeparators(expectedRoot)) {
		return nil, bfInvalid("memory_root must resolve to state.project_path/.bsl-flow/memory.")
	}
	if info, err := statFile(declaredRoot); err == nil && !info.IsDir() {
		return nil, bfBlocked("memory_root is occupied by a file.")
	}
	return state, nil
}

// assertNativeMemoryResult ports Assert-BFNativeMemoryResult.
func assertNativeMemoryResult(state, input map[string]any) (map[string]any, error) {
	resultAny := objectValue(input, "result")
	if err := assertNativeMemoryObject(resultAny, "result"); err != nil {
		return nil, err
	}
	result := resultAny.(map[string]any)
	for _, field := range []string{"schema_version", "task_id", "attempt_id", "stage", "outcome"} {
		if !objectProperty(result, field) {
			return nil, bfInvalid("result.%s is required.", field)
		}
	}
	if !equalsPSOne(result["schema_version"]) {
		return nil, bfInvalid("result.schema_version must be 1.")
	}
	stateTaskID := psString(objectValue(state, "task_id"))
	resultTaskID, err := assertNativeMemoryString(result["task_id"], "result.task_id", 64, false)
	if err != nil {
		return nil, err
	}
	if err := assertUUID(resultTaskID); err != nil {
		return nil, err
	}
	if resultTaskID != stateTaskID {
		return nil, bfConflict("result.task_id does not match state.task_id.")
	}
	attemptID, err := assertNativeMemoryString(result["attempt_id"], "result.attempt_id", 64, false)
	if err != nil {
		return nil, err
	}
	if err := assertUUID(attemptID); err != nil {
		return nil, err
	}
	resultStage, err := assertNativeMemoryString(result["stage"], "result.stage", 64, false)
	if err != nil {
		return nil, err
	}
	if !containsExact(nativeMemoryStages, resultStage) {
		return nil, bfInvalid("result.stage is not a controller stage.")
	}
	outcome, err := assertNativeMemoryString(result["outcome"], "result.outcome", 32, false)
	if err != nil {
		return nil, err
	}
	if !containsExact(nativeMemoryTerminalOutcomes, outcome) {
		return nil, bfInvalid("result.outcome is not terminal.")
	}
	return result, nil
}

// assertNativeMemoryReceipt ports Assert-BFNativeMemoryReceipt.
func assertNativeMemoryReceipt(state, input map[string]any) (map[string]any, error) {
	receiptAny := objectValue(input, "receipt")
	if err := assertNativeMemoryObject(receiptAny, "receipt"); err != nil {
		return nil, err
	}
	receipt := receiptAny.(map[string]any)
	for _, field := range []string{"schema_version", "task_id", "verdict"} {
		if !objectProperty(receipt, field) {
			return nil, bfInvalid("receipt.%s is required.", field)
		}
	}
	if !equalsPSOne(receipt["schema_version"]) {
		return nil, bfInvalid("receipt.schema_version must be 1.")
	}
	stateTaskID := psString(objectValue(state, "task_id"))
	receiptTaskID, err := assertNativeMemoryString(receipt["task_id"], "receipt.task_id", 64, false)
	if err != nil {
		return nil, err
	}
	if err := assertUUID(receiptTaskID); err != nil {
		return nil, err
	}
	if receiptTaskID != stateTaskID {
		return nil, bfConflict("receipt.task_id does not match state.task_id.")
	}
	verdict, err := assertNativeMemoryString(receipt["verdict"], "receipt.verdict", 32, false)
	if err != nil {
		return nil, err
	}
	if verdict != "PASS" {
		return nil, bfBlocked("acceptance receipt verdict must be PASS.")
	}
	if psString(objectValue(state, "status")) != "completed" {
		return nil, bfBlocked("acceptance extraction requires completed controller state.")
	}
	return receipt, nil
}

// assertNativeMemoryPendingFailure ports Assert-BFNativeMemoryPendingFailure.
func assertNativeMemoryPendingFailure(state, input map[string]any) (map[string]any, error) {
	failure := objectValue(input, "pending_failure_result")
	hash := objectValue(input, "pending_failure_result_hash")
	if failure == nil {
		if hash != nil && psString(hash) != "" {
			return nil, bfInvalid("pending_failure_result_hash requires pending_failure_result.")
		}
		return nil, nil
	}
	if err := assertNativeMemoryObject(failure, "pending_failure_result"); err != nil {
		return nil, err
	}
	failureObject := failure.(map[string]any)
	if err := assertNativeMemoryHash(hash, "pending_failure_result_hash", false); err != nil {
		return nil, err
	}
	if isNullOrWhiteSpace(psString(hash)) {
		return nil, bfInvalid("pending_failure_result requires pending_failure_result_hash.")
	}
	for _, field := range []string{"schema_version", "task_id", "attempt_id", "stage", "outcome", "side_effects"} {
		if !objectProperty(failure, field) {
			return nil, bfInvalid("pending_failure_result.%s is required.", field)
		}
	}
	if !equalsPSOne(failureObject["schema_version"]) {
		return nil, bfInvalid("pending_failure_result.schema_version must be 1.")
	}
	taskID, err := assertNativeMemoryString(failureObject["task_id"], "pending_failure_result.task_id", 64, false)
	if err != nil {
		return nil, err
	}
	if err := assertUUID(taskID); err != nil {
		return nil, err
	}
	if taskID != psString(objectValue(state, "task_id")) {
		return nil, bfConflict("pending_failure_result.task_id does not match state.task_id.")
	}
	attemptID, err := assertNativeMemoryString(failureObject["attempt_id"], "pending_failure_result.attempt_id", 64, false)
	if err != nil {
		return nil, err
	}
	if err := assertUUID(attemptID); err != nil {
		return nil, err
	}
	pendingID := psString(objectValue(objectValue(state, "repair"), "pending_failure"))
	if isNullOrWhiteSpace(pendingID) || attemptID != pendingID {
		return nil, bfConflict("pending_failure_result.attempt_id does not match state.repair.pending_failure.")
	}
	if psString(objectValue(failureObject, "stage")) != "verify" ||
		psString(objectValue(failureObject, "outcome")) != "FAIL" ||
		psString(objectValue(failureObject, "side_effects")) != "none" {
		return nil, bfBlocked("pending_failure_result is not a clean failed verification.")
	}
	hashText, err := hashValue(failureObject)
	if err != nil {
		return nil, err
	}
	if hashText != psString(hash) {
		return nil, bfConflict("pending_failure_result_hash does not match pending_failure_result.")
	}
	return failureObject, nil
}

type validatedMemoryInput struct {
	state              map[string]any
	pendingFailure     map[string]any
	pendingFailureHash string
}

// assertNativeMemoryInput ports Assert-BFNativeMemoryInput.
func assertNativeMemoryInput(input map[string]any) (*validatedMemoryInput, error) {
	if err := assertNativeMemoryExactFields(input, nativeMemoryRootFields, "native memory input"); err != nil {
		return nil, err
	}
	if !equalsPSOne(input["schema_version"]) {
		return nil, bfInvalid("Unsupported native memory input schema_version.")
	}
	if _, err := assertNativeMemoryString(input["operation"], "operation", 32, false); err != nil {
		return nil, err
	}
	operation := input["operation"].(string)
	if !containsExact(nativeMemoryOperations, operation) {
		return nil, bfInvalid("Unsupported native memory operation.")
	}
	state, err := assertStateAndRoot(input)
	if err != nil {
		return nil, err
	}
	stageValue, stageIsString := input["stage"].(string)
	if !stageIsString || utf16Length(stageValue) > 64 {
		return nil, bfInvalid("stage must be a bounded string.")
	}
	if !isNullOrWhiteSpace(stageValue) && !containsExact(nativeMemoryStages, stageValue) {
		return nil, bfInvalid("stage is not a controller stage.")
	}
	for _, name := range []string{"result", "receipt", "next"} {
		if err := assertNativeMemoryMaybeObject(objectValue(input, name), name); err != nil {
			return nil, err
		}
	}
	if err := assertNativeMemoryHash(objectValue(input, "result_hash"), "result_hash", true); err != nil {
		return nil, err
	}
	if err := assertNativeMemoryHash(objectValue(input, "receipt_hash"), "receipt_hash", true); err != nil {
		return nil, err
	}
	pendingFailure, err := assertNativeMemoryPendingFailure(state, input)
	if err != nil {
		return nil, err
	}
	result := objectValue(input, "result")
	receipt := objectValue(input, "receipt")
	resultHash := objectValue(input, "result_hash")
	receiptHash := objectValue(input, "receipt_hash")
	hasResultSlot := result != nil || psString(resultHash) != ""
	hasReceiptSlot := receipt != nil || psString(receiptHash) != ""
	switch operation {
	case "bind":
		if isNullOrWhiteSpace(stageValue) {
			return nil, bfInvalid("bind requires a non-empty stage.")
		}
		if hasResultSlot || hasReceiptSlot {
			return nil, bfInvalid("bind accepts no result or receipt fields.")
		}
	case "projection":
		if hasResultSlot || hasReceiptSlot {
			return nil, bfInvalid("projection accepts no result or receipt fields.")
		}
	case "extract-attempt":
		if pendingFailure != nil {
			return nil, bfInvalid("extract-attempt accepts no pending failure result.")
		}
		if result == nil || isNullOrWhiteSpace(psString(resultHash)) {
			return nil, bfInvalid("extract-attempt requires result and result_hash.")
		}
		if receipt != nil || psString(receiptHash) != "" {
			return nil, bfInvalid("extract-attempt accepts no receipt fields.")
		}
		validatedResult, err := assertNativeMemoryResult(state, input)
		if err != nil {
			return nil, err
		}
		if !isNullOrWhiteSpace(stageValue) && stageValue != psString(objectValue(validatedResult, "stage")) {
			return nil, bfConflict("result.stage does not match stage.")
		}
		hashText, err := hashValue(validatedResult)
		if err != nil {
			return nil, err
		}
		if hashText != psString(resultHash) {
			return nil, bfConflict("result_hash does not match result.")
		}
	case "extract-acceptance":
		if pendingFailure != nil {
			return nil, bfInvalid("extract-acceptance accepts no pending failure result.")
		}
		if receipt == nil || isNullOrWhiteSpace(psString(receiptHash)) {
			return nil, bfInvalid("extract-acceptance requires receipt and receipt_hash.")
		}
		if result != nil || psString(resultHash) != "" {
			return nil, bfInvalid("extract-acceptance accepts no result fields.")
		}
		validatedReceipt, err := assertNativeMemoryReceipt(state, input)
		if err != nil {
			return nil, err
		}
		hashText, err := hashValue(validatedReceipt)
		if err != nil {
			return nil, err
		}
		if hashText != psString(receiptHash) {
			return nil, bfConflict("receipt_hash does not match receipt.")
		}
	}
	return &validatedMemoryInput{
		state:              state,
		pendingFailure:     pendingFailure,
		pendingFailureHash: psString(objectValue(input, "pending_failure_result_hash")),
	}, nil
}

func containsExact(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
