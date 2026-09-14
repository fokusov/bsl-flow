package repository

import (
	"path/filepath"
	"strings"
)

// This file ports Get-BFNativeLoadedProof (Task.NativeReuse.ps1) for the
// controller side: the test-only continuation proof over a prior unsuccessful
// full native verification, bound to the committed control-read recovery.
// The native stage host wraps this same computation (stagehost/native1c_loaded.go)
// so both surfaces share one binding.

// Native1CLoadedProof mirrors Get-BFNativeLoadedProof. It returns nil when
// the criterion carries no reuse_load_attempt. taskDir is the task evidence
// directory; manifest is the current source manifest (Get-BFSourceManifest).
func Native1CLoadedProof(state map[string]any, criterion map[string]any, source map[string]any, beforeInventory map[string]any, taskDir string, manifest map[string]any) (map[string]any, error) {
	native := asMap(criterion["native_1c"])
	priorID := native["reuse_load_attempt"]
	if priorID == nil {
		return nil, nil
	}
	if !isUUID(asStringOr(priorID)) {
		return nil, invalid("identity must be a canonical lower-case UUID.")
	}
	prior := filepath.Join(taskDir, "attempts", asStringOr(priorID))
	var entry map[string]any
	count := 0
	for _, raw := range anyItems(state["evidence"]) {
		candidate := asMap(raw)
		if asStringOr(candidate["attempt_id"]) == asStringOr(priorID) &&
			asStringOr(candidate["stage"]) == "verify" &&
			(asStringOr(candidate["outcome"]) == "BLOCKED" || asStringOr(candidate["outcome"]) == "FAIL") {
			entry = candidate
			count++
		}
	}
	if count != 1 {
		return nil, blocked("reuse requires an exact recorded unsuccessful native verification.")
	}
	result, err := native1CReadJSON(filepath.Join(prior, "result.json"))
	if err != nil {
		return nil, err
	}
	resultHash, err := Hash(result)
	if err != nil {
		return nil, err
	}
	if resultHash != asStringOr(entry["result_sha256"]) {
		return nil, blocked("prior native result changed.")
	}
	priorPrefix := strings.ToLower(strings.TrimRight(prior, `\/`) + string(filepath.Separator))
	for _, raw := range anyItems(entry["raw_hashes"]) {
		rawHash := asMap(raw)
		path, err := SafePath(asStringOr(rawHash["path"]))
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(strings.ToLower(path), priorPrefix) {
			return nil, blocked("prior native evidence changed.")
		}
		hash, err := native1CFileSHA256(path)
		if err != nil {
			return nil, blocked("prior native evidence changed.")
		}
		if hash != asStringOr(rawHash["sha256"]) {
			return nil, blocked("prior native evidence changed.")
		}
	}
	rawRoot := filepath.Join(prior, "raw", asStringOr(criterion["id"]))
	request, err := native1CReadJSON(filepath.Join(rawRoot, "runtime-request.json"))
	if err != nil {
		return nil, err
	}
	start, err := native1CReadJSON(filepath.Join(prior, "start.json"))
	if err != nil {
		return nil, err
	}
	snapshot, err := Native1CSourceSnapshot(filepath.Join(rawRoot, "source-snapshot"))
	if err != nil {
		return nil, err
	}
	requestSource := asMap(request["source"])
	if asStringOr(request["task_id"]) != asStringOr(state["task_id"]) ||
		asStringOr(request["attempt_id"]) != asStringOr(priorID) ||
		!strings.EqualFold(asStringOr(request["target"]), asStringOr(criterion["target"])) ||
		joinNative1CRawStrings(anyItems(request["operations"])) != "inventory,load,update,test" {
		return nil, blocked("reuse must refer to an original full native attempt in this task and target.")
	}
	if asStringOr(requestSource["extension"]) != asStringOr(native["extension"]) ||
		asStringOr(snapshot["sha256"]) != asStringOr(source["sha256"]) ||
		asStringOr(requestSource["sha256"]) != asStringOr(source["sha256"]) ||
		asStringOr(asMap(start["source_manifest"])["sha256"]) != asStringOr(manifest["sha256"]) {
		return nil, blocked("loaded source differs from current source.")
	}
	dependencies, err := Native1CDependencies(criterion)
	if err != nil {
		return nil, err
	}
	platformHash, err := Hash([]any{dependencies})
	if err != nil {
		return nil, err
	}
	expectedTestsHash, err := Hash(criterion["expected_tests"])
	if err != nil {
		return nil, err
	}
	requestExpectedHash, err := Hash(request["expected_tests"])
	if err != nil {
		return nil, err
	}
	if asStringOr(asMap(start["dependencies"])["native_platform"]) != platformHash || requestExpectedHash != expectedTestsHash {
		return nil, blocked("native platform or selected tests changed since load.")
	}
	config, err := native1CReadJSON(filepath.Join(rawRoot, "test-config.json"))
	if err != nil {
		return nil, err
	}
	if joinNative1CRawStrings(anyItems(asMap(config["filter"])["modules"])) != asStringOr(native["module"]) {
		return nil, blocked("native test module changed since load.")
	}
	requestHash, err := Hash(request)
	if err != nil {
		return nil, err
	}
	terminals := []any{}
	for _, stepName := range []string{"load", "update"} {
		terminalPath := filepath.Join(rawRoot, "steps", stepName, "terminal.json")
		terminal, err := native1CReadJSON(terminalPath)
		if err != nil {
			return nil, err
		}
		logPath, err := SafePath(asStringOr(terminal["log"]))
		if err != nil {
			return nil, err
		}
		logHash, err := native1CFileSHA256(logPath)
		if err != nil {
			return nil, blocked("successful original load/update receipts are required.")
		}
		rawPrefix := strings.ToLower(strings.TrimRight(rawRoot, `\/`) + string(filepath.Separator))
		if asStringOr(terminal["request_sha256"]) != requestHash ||
			asIntOr(terminal["exit_code"]) != 0 ||
			!strings.HasPrefix(strings.ToLower(logPath), rawPrefix) ||
			logHash != asStringOr(terminal["log_sha256"]) {
			return nil, blocked("successful original load/update receipts are required.")
		}
		terminalFileHash, err := native1CFileSHA256(terminalPath)
		if err != nil {
			return nil, err
		}
		terminals = append(terminals, map[string]any{"step": stepName, "sha256": terminalFileHash})
	}
	var matched map[string]any
	for _, raw := range anyItems(state["events"]) {
		event := asMap(raw)
		if asStringOr(event["kind"]) != "recovery" {
			continue
		}
		inputPath := filepath.Join(taskDir, "inputs", asStringOr(event["input_event_id"])+".json")
		input, err := native1CReadJSON(inputPath)
		if err != nil {
			return nil, err
		}
		inputHash, err := Hash(input)
		if err != nil {
			return nil, err
		}
		if inputHash != asStringOr(event["sha256"]) {
			return nil, blocked("registered recovery input changed.")
		}
		if asStringOr(asMap(input["resolution"])["attempt_id"]) != asStringOr(priorID) {
			continue
		}
		receiptPath := filepath.Join(taskDir, "inputs", "recovery-"+asStringOr(event["input_event_id"])+".json")
		receipt, err := native1CReadJSON(receiptPath)
		if err != nil {
			return nil, err
		}
		resolution := asMap(receipt["resolution"])
		inputResolution := asMap(input["resolution"])
		resolutionHash, err := Hash(resolution)
		if err != nil {
			return nil, err
		}
		inputResolutionHash, err := Hash(inputResolution)
		if err != nil {
			return nil, err
		}
		if resolutionHash != inputResolutionHash ||
			asStringOr(resolution["scope"]) != "native_1c" ||
			!native1CBool(resolution["retry_authorized"]) ||
			asStringOr(asMap(receipt["actual_source_manifest"])["sha256"]) != asStringOr(manifest["sha256"]) {
			return nil, blocked("loaded source has no matching committed recovery.")
		}
		observed, err := native1CReadJSON(asStringOr(asMap(receipt["runtime_control_read"])["inventory_path"]))
		if err != nil {
			return nil, err
		}
		inventoryHash, err := Native1CInventoryHash(observed)
		if err != nil {
			return nil, err
		}
		beforeHash, err := Native1CInventoryHash(beforeInventory)
		if err != nil {
			return nil, err
		}
		if inventoryHash != asStringOr(resolution["inventory_sha256"]) || inventoryHash != beforeHash {
			return nil, blocked("inventory changed after native recovery.")
		}
		priorBefore, err := native1CReadJSON(filepath.Join(rawRoot, "inventory-before.json"))
		if err != nil {
			return nil, err
		}
		if err := Native1CInventoryTransition(priorBefore, observed, source); err != nil {
			return nil, err
		}
		receiptFileHash, err := native1CFileSHA256(receiptPath)
		if err != nil {
			return nil, err
		}
		matched = map[string]any{
			"path":             receiptPath,
			"sha256":           receiptFileHash,
			"inventory_sha256": inventoryHash,
		}
	}
	if matched == nil || state["unresolved_effect"] != nil {
		return nil, blocked("test-only continuation requires committed control-read recovery.")
	}
	return map[string]any{
		"schema_version":     int64(1),
		"kind":               "bsl-flow.native-loaded-proof",
		"task_id":            asStringOr(state["task_id"]),
		"loaded_attempt_id":  asStringOr(priorID),
		"result_sha256":      asStringOr(entry["result_sha256"]),
		"request_sha256":     requestHash,
		"source_sha256":      asStringOr(source["sha256"]),
		"full_source_sha256": asStringOr(manifest["sha256"]),
		"terminals":          terminals,
		"recovery":           matched,
	}, nil
}

func joinNative1CRawStrings(values []any) string {
	parts := make([]string, 0, len(values))
	for _, raw := range values {
		parts = append(parts, asStringOr(raw))
	}
	return strings.Join(parts, ",")
}

func native1CReadJSON(path string) (map[string]any, error) {
	safe, err := SafePath(path)
	if err != nil {
		return nil, err
	}
	data, err := ReadFileBytes(safe)
	if err != nil {
		return nil, invalid("JSON file does not exist: %s", safe)
	}
	object, err := DecodeObject(data)
	if err != nil {
		return nil, invalid("Cannot materialize JSON object: %v", err)
	}
	return object, nil
}

func native1CFileSHA256(path string) (string, error) {
	data, err := ReadFileBytes(path)
	if err != nil {
		return "", err
	}
	return fileSHA256(data), nil
}
