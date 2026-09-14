package stagehost

import (
	"path/filepath"
	"strings"

	"bsl-flow/cli/internal/repository"
)

// This file ports Get-BFNativeLoadedProof (Task.NativeReuse.ps1): the
// test-only continuation proof over a prior unsuccessful full native
// verification, bound to the committed control-read recovery.

// nativeLoadedProof mirrors Get-BFNativeLoadedProof. It returns nil when the
// criterion carries no reuse_load_attempt.
func nativeLoadedProof(state map[string]any, criterion map[string]any, source map[string]any, beforeInventory map[string]any) (map[string]any, error) {
	native := asMap(criterion["native_1c"])
	priorID := getValue(native, "reuse_load_attempt", nil)
	if priorID == nil {
		return nil, nil
	}
	if err := assertUUID(priorID); err != nil {
		return nil, err
	}
	taskDirectory, err := safePath(filepath.Join(asStringOr(state["project_path"]), ".bsl-flow", "tasks", asStringOr(state["task_id"])))
	if err != nil {
		return nil, err
	}
	prior := filepath.Join(taskDirectory, "attempts", asStringOr(priorID))
	var entry map[string]any
	count := 0
	for _, raw := range anyItemsOf(state["evidence"]) {
		candidate := asMap(raw)
		if asStringOr(candidate["attempt_id"]) == asStringOr(priorID) &&
			asStringOr(candidate["stage"]) == "verify" &&
			(asStringOr(candidate["outcome"]) == "BLOCKED" || asStringOr(candidate["outcome"]) == "FAIL") {
			entry = candidate
			count++
		}
	}
	if count != 1 {
		return nil, blockedf("reuse requires an exact recorded unsuccessful native verification.")
	}
	result, err := readJSONObject(filepath.Join(prior, "result.json"))
	if err != nil {
		return nil, err
	}
	resultHash, err := hashValue(result)
	if err != nil {
		return nil, err
	}
	if resultHash != asStringOr(entry["result_sha256"]) {
		return nil, blockedf("prior native result changed.")
	}
	priorPrefix := strings.ToLower(strings.TrimRight(prior, `\/`) + string(filepath.Separator))
	for _, raw := range anyItemsOf(entry["raw_hashes"]) {
		rawHash := asMap(raw)
		path, err := safePath(asStringOr(rawHash["path"]))
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(strings.ToLower(path), priorPrefix) {
			return nil, blockedf("prior native evidence changed.")
		}
		hash, err := hashFile(path)
		if err != nil {
			return nil, blockedf("prior native evidence changed.")
		}
		if hash != asStringOr(rawHash["sha256"]) {
			return nil, blockedf("prior native evidence changed.")
		}
	}
	rawRoot := filepath.Join(prior, "raw", asStringOr(criterion["id"]))
	request, err := readJSONObject(filepath.Join(rawRoot, "runtime-request.json"))
	if err != nil {
		return nil, err
	}
	start, err := readJSONObject(filepath.Join(prior, "start.json"))
	if err != nil {
		return nil, err
	}
	snapshot, err := repository.StageHostNative1CSourceSnapshot(filepath.Join(rawRoot, "source-snapshot"))
	if err != nil {
		return nil, native1cError(err)
	}
	manifest, err := stageSourceManifest(state)
	if err != nil {
		return nil, err
	}
	requestSource := asMap(request["source"])
	if asStringOr(request["task_id"]) != asStringOr(state["task_id"]) ||
		asStringOr(request["attempt_id"]) != asStringOr(priorID) ||
		!strings.EqualFold(asStringOr(request["target"]), asStringOr(criterion["target"])) ||
		joinAnyStrings(anyItemsOf(request["operations"])) != "inventory,load,update,test" {
		return nil, blockedf("reuse must refer to an original full native attempt in this task and target.")
	}
	if asStringOr(requestSource["extension"]) != asStringOr(native["extension"]) ||
		asStringOr(snapshot["sha256"]) != asStringOr(source["sha256"]) ||
		asStringOr(requestSource["sha256"]) != asStringOr(source["sha256"]) ||
		asStringOr(asMap(start["source_manifest"])["sha256"]) != asStringOr(manifest["sha256"]) {
		return nil, blockedf("loaded source differs from current source.")
	}
	dependencies, err := repository.StageHostNative1CDependencies(criterion)
	if err != nil {
		return nil, native1cError(err)
	}
	platformHash, err := hashValue([]any{dependencies})
	if err != nil {
		return nil, err
	}
	expectedTestsHash, err := hashValue(criterion["expected_tests"])
	if err != nil {
		return nil, err
	}
	requestExpectedHash, err := hashValue(request["expected_tests"])
	if err != nil {
		return nil, err
	}
	if asStringOr(asMap(start["dependencies"])["native_platform"]) != platformHash || requestExpectedHash != expectedTestsHash {
		return nil, blockedf("native platform or selected tests changed since load.")
	}
	config, err := readJSONObject(filepath.Join(rawRoot, "test-config.json"))
	if err != nil {
		return nil, err
	}
	if joinAnyStrings(anyItemsOf(asMap(config["filter"])["modules"])) != asStringOr(native["module"]) {
		return nil, blockedf("native test module changed since load.")
	}
	requestHash, err := hashValue(request)
	if err != nil {
		return nil, err
	}
	terminals := []any{}
	for _, stepName := range []string{"load", "update"} {
		terminalPath := filepath.Join(rawRoot, "steps", stepName, "terminal.json")
		terminal, err := readJSONObject(terminalPath)
		if err != nil {
			return nil, err
		}
		logPath, err := safePath(asStringOr(terminal["log"]))
		if err != nil {
			return nil, err
		}
		logHash, err := hashFile(logPath)
		if err != nil {
			return nil, blockedf("successful original load/update receipts are required.")
		}
		rawPrefix := strings.ToLower(strings.TrimRight(rawRoot, `\/`) + string(filepath.Separator))
		if asStringOr(terminal["request_sha256"]) != requestHash ||
			asIntegerOrZero(terminal["exit_code"]) != 0 ||
			!strings.HasPrefix(strings.ToLower(logPath), rawPrefix) ||
			logHash != asStringOr(terminal["log_sha256"]) {
			return nil, blockedf("successful original load/update receipts are required.")
		}
		terminalFileHash, err := hashFile(terminalPath)
		if err != nil {
			return nil, err
		}
		terminals = append(terminals, map[string]any{"step": stepName, "sha256": terminalFileHash})
	}
	var matched map[string]any
	for _, raw := range anyItemsOf(state["events"]) {
		event := asMap(raw)
		if asStringOr(event["kind"]) != "recovery" {
			continue
		}
		inputPath := filepath.Join(taskDirectory, "inputs", asStringOr(event["input_event_id"])+".json")
		input, err := readJSONObject(inputPath)
		if err != nil {
			return nil, err
		}
		inputHash, err := hashValue(input)
		if err != nil {
			return nil, err
		}
		if inputHash != asStringOr(event["sha256"]) {
			return nil, blockedf("registered recovery input changed.")
		}
		if asStringOr(asMap(input["resolution"])["attempt_id"]) != asStringOr(priorID) {
			continue
		}
		receiptPath := filepath.Join(taskDirectory, "inputs", "recovery-"+asStringOr(event["input_event_id"])+".json")
		receipt, err := readJSONObject(receiptPath)
		if err != nil {
			return nil, err
		}
		resolution := asMap(receipt["resolution"])
		inputResolution := asMap(input["resolution"])
		resolutionHash, err := hashValue(resolution)
		if err != nil {
			return nil, err
		}
		inputResolutionHash, err := hashValue(inputResolution)
		if err != nil {
			return nil, err
		}
		if resolutionHash != inputResolutionHash ||
			asStringOr(resolution["scope"]) != "native_1c" ||
			asStringOr(resolution["retry_authorized"]) != "true" ||
			asStringOr(asMap(receipt["actual_source_manifest"])["sha256"]) != asStringOr(manifest["sha256"]) {
			return nil, blockedf("loaded source has no matching committed recovery.")
		}
		observed, err := readJSONObject(asStringOr(asMap(receipt["runtime_control_read"])["inventory_path"]))
		if err != nil {
			return nil, err
		}
		inventoryHash, err := repository.StageHostNative1CInventoryHash(observed)
		if err != nil {
			return nil, err
		}
		beforeHash, err := repository.StageHostNative1CInventoryHash(beforeInventory)
		if err != nil {
			return nil, err
		}
		if inventoryHash != asStringOr(resolution["inventory_sha256"]) || inventoryHash != beforeHash {
			return nil, blockedf("inventory changed after native recovery.")
		}
		priorBefore, err := readJSONObject(filepath.Join(rawRoot, "inventory-before.json"))
		if err != nil {
			return nil, err
		}
		if err := repository.StageHostNative1CInventoryTransition(priorBefore, observed, source); err != nil {
			return nil, native1cError(err)
		}
		receiptFileHash, err := hashFile(receiptPath)
		if err != nil {
			return nil, err
		}
		matched = map[string]any{
			"path":             receiptPath,
			"sha256":           receiptFileHash,
			"inventory_sha256": inventoryHash,
		}
	}
	if matched == nil || getValue(state, "unresolved_effect", nil) != nil {
		return nil, blockedf("test-only continuation requires committed control-read recovery.")
	}
	return map[string]any{
		"schema_version":      int64(1),
		"kind":                "bsl-flow.native-loaded-proof",
		"task_id":             asStringOr(state["task_id"]),
		"loaded_attempt_id":   asStringOr(priorID),
		"result_sha256":       asStringOr(entry["result_sha256"]),
		"request_sha256":      requestHash,
		"source_sha256":       asStringOr(source["sha256"]),
		"full_source_sha256":  asStringOr(manifest["sha256"]),
		"terminals":           terminals,
		"recovery":            matched,
	}, nil
}

func joinAnyStrings(values []any) string {
	parts := make([]string, 0, len(values))
	for _, raw := range values {
		parts = append(parts, asStringOr(raw))
	}
	return strings.Join(parts, ",")
}

func asIntegerOrZero(value any) int64 {
	parsed, _ := asInteger(value)
	return parsed
}
