package repository

import "path/filepath"

func recordedNativeResult(repository *Repository, task *Task, payload map[string]any, attemptID, inputPath string, hosts ...*ControllerHost) (any, error) {
	var evidence map[string]any
	for _, raw := range anyItems(payload["evidence"]) {
		entry := asMap(raw)
		if entry["attempt_id"] == attemptID {
			if evidence != nil {
				return nil, blocked("duplicate recorded attempt")
			}
			evidence = entry
		}
	}
	if evidence == nil {
		return nil, blocked("record requires the exact registered active attempt")
	}
	path, err := attemptDirectory(repository, task.ID, attemptID)
	if err != nil {
		return nil, err
	}
	observation, err := readStoredObservation(filepath.Join(path, "result.json"))
	if err != nil {
		return nil, err
	}
	engine, err := boundNativeEngine(payload)
	if err != nil {
		return nil, err
	}
	if err := validateNativeTransport(path, observation, engine); err != nil {
		return nil, err
	}
	if inputPath != "" {
		candidate, err := readStoredObservation(inputPath)
		if err != nil {
			return nil, err
		}
		if !equalJSON(toExecuteObservation(candidate), toExecuteObservation(observation)) {
			return nil, conflict("recorded terminal result differs from input")
		}
	}
	data, err := ReadFileBytes(filepath.Join(path, "terminal.json"))
	if err != nil {
		return nil, err
	}
	terminal, err := DecodeObject(data)
	if err != nil {
		return nil, err
	}
	hash, err := Hash(terminal)
	if err != nil || hash != asStringOr(evidence["result_sha256"]) || fileSHA256(data) != hash {
		return nil, blocked("recorded terminal result changed")
	}
	for _, key := range []string{"stage", "attempt_id", "outcome", "dependencies", "raw_hashes"} {
		if !equalJSON(terminal[key], evidence[key]) {
			return nil, blocked("recorded terminal binding changed")
		}
	}
	for _, artifact := range observation.Artifacts {
		data, err := readProviderArtifactFile(filepath.Join(path, "artifacts"), artifact.Path)
		if err != nil || fileSHA256(data) != artifact.SHA256 || int64(len(data)) != artifact.SizeBytes {
			return nil, blocked("recorded raw artifact changed")
		}
	}
	if evidence["outcome"] == "PASS" && !controllerEvidenceFresh(task.State, payload, evidence) {
		return nil, blocked("recorded evidence is stale")
	}
	next, err := controllerNext(task.State, payload)
	if err != nil {
		return nil, err
	}
	invokeNativeMemory(task.State, payload, "extract-attempt", "", terminal, hash, nil, "", nil, hosts...)
	return controllerEnvelope(task.State, payload, next), nil
}

// All terminal entry points share the same checks and publication order.
// No result revision is written before the original evidence is validated.
func recordNativeObservation(repository *Repository, task *Task, payload map[string]any, observation ExecuteObservation, start map[string]any, artifactRoot string, hosts ...*ControllerHost) (any, error) {
	id := task.ID
	attemptID := observation.AttemptID
	if asStringOr(payload["active_attempt"]) != attemptID {
		return nil, blocked("terminal result is not for the active attempt")
	}
	if start["task_id"] != id || start["attempt_id"] != attemptID || !equalJSON(start["intent_revision"], payload["intent_revision"]) || !equalJSON(start["authorization_revision"], payload["authorization_revision"]) {
		return nil, blocked("attempt execution binding changed")
	}
	path, err := attemptDirectory(repository, id, attemptID)
	if err != nil {
		return nil, err
	}
	engine, err := boundNativeEngine(payload)
	if err != nil {
		return nil, err
	}
	if err := validateNativeTransport(path, observation, engine); err != nil {
		return nil, err
	}
	manifest, err := validateExecuteObservation(observation, id, attemptID, asStringOr(start["stage"]), engine, artifactRoot, asMap(start["dependencies"]), asMap(start["source_manifest"]), asStringOr(payload["worker_path"]))
	if err != nil {
		return nil, err
	}
	if observation.Stage == "implement" {
		if err := validateNativeProtectedTests(payload, asMap(start["source_manifest"]), manifest); err != nil {
			return nil, err
		}
	}
	if err := validateNativeBudgetObservation(payload, observation, path, artifactRoot); err != nil {
		return nil, err
	}
	outcome, err := validateStageEvidence(payload, observation, artifactRoot)
	if err != nil {
		return nil, stageEvidenceError(observation.Stage, err)
	}
	dependencies, err := validateNativeTerminalDependencies(payload, observation, asMap(start["dependencies"]), manifest)
	if err != nil {
		return nil, err
	}
	terminal, err := normalizedTerminalResult(observation, outcome)
	if err != nil {
		return nil, err
	}
	hash, err := Hash(terminal)
	if err != nil {
		return nil, err
	}
	evidence := makeEvidence(observation, dependencies, observation.Artifacts, hash, outcome)
	repairEligible, err := nativeVerificationFailureEligible(payload, observation, artifactRoot)
	if err != nil {
		return nil, err
	}
	if outcome == "REPAIR" {
		if err := validateNativeRepairFailure(task.State, payload); err != nil {
			return nil, err
		}
	}
	if err := applyObservation(payload, observation, evidence, repairEligible); err != nil {
		return nil, err
	}
	if err := persistProviderArtifacts(path, artifactRoot, observation.Artifacts); err != nil {
		return nil, err
	}
	if _, err := writeImmutableJSON(filepath.Join(path, "result.json"), toExecuteObservation(observation)); err != nil {
		return nil, err
	}
	if _, err := writeImmutableJSON(filepath.Join(path, "terminal.json"), terminal); err != nil {
		return nil, err
	}
	state, err := appendControllerRevision(repository, task, payload, task.Revision)
	if err != nil {
		return nil, err
	}
	payload = asMap(state["controller"])
	invokeNativeMemory(state, payload, "extract-attempt", "", terminal, hash, nil, "", nil, hosts...)
	next, err := controllerNext(state, payload)
	if err != nil {
		return nil, err
	}
	return controllerEnvelope(state, payload, next), nil
}
