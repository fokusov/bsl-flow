package repository

import (
	"os"
	"path/filepath"
)

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
	if observation.Stage == "verify" && outcome == "PASS" {
		// Task.Engine.ps1 Record flow: a verified native PASS completes its own
		// pending latch from the retained result and raw evidence. The scan
		// no-ops for source-only verifications.
		if err := Native1CCompleteRecordedSuccess(payload, attemptID, path, nativeRecoveryRuntime(hosts...)); err != nil {
			return nil, err
		}
	}
	invokeNativeMemory(state, payload, "extract-attempt", "", terminal, hash, nil, "", nil, hosts...)
	next, err := controllerNext(state, payload)
	if err != nil {
		return nil, err
	}
	return controllerEnvelope(state, payload, next), nil
}

// recordRecoveredNativeSuccess mirrors the Resume saved-success branch of
// Task.Engine.ps1: a retained runtime-success.json re-validates offline and
// completes its own pending latch without repeating any database operation.
func recordRecoveredNativeSuccess(repository *Repository, task *Task, payload map[string]any, start map[string]any, attemptPath, artifactRoot string, criterion map[string]any, hosts ...*ControllerHost) (any, error) {
	attemptID := asStringOr(start["attempt_id"])
	rawRoot := filepath.Join(artifactRoot, "raw")
	rawDir := filepath.Join(rawRoot, asStringOr(criterion["id"]))
	manifest, err := sourceManifestWithBaseline(asStringOr(payload["worker_path"]), asStringOr(payload["baseline"]), []string{"."})
	if err != nil {
		return nil, err
	}
	current, err := currentNativeDependencies(payload, "verify", manifest)
	if err != nil {
		return nil, err
	}
	currentHash, err := Hash(current)
	if err != nil {
		return nil, err
	}
	startDepsHash, err := Hash(asMap(start["dependencies"]))
	if err != nil {
		return nil, err
	}
	if currentHash != startDepsHash {
		return nil, blocked("saved native verification dependencies changed.")
	}
	taskDir := filepath.Join(repository.StorePath, "tasks", task.ID)
	observation, err := Native1CSavedObservation(payload, criterion, rawDir, attemptID, taskDir, manifest)
	if err != nil {
		return nil, err
	}
	observationsPath := filepath.Join(rawRoot, "observations.json")
	observations := map[string]any{"criteria": []any{observation}}
	if nativeDependencyRegularFile(observationsPath) {
		saved, err := native1CReadJSON(observationsPath)
		if err != nil {
			return nil, err
		}
		savedHash, err := Hash(saved)
		if err != nil {
			return nil, err
		}
		observationsHash, err := Hash(observations)
		if err != nil {
			return nil, err
		}
		if savedHash != observationsHash {
			return nil, blocked("saved native observations differ from original reports.")
		}
	} else if err := AtomicWriteCanonical(observationsPath, observations); err != nil {
		return nil, err
	}
	artifacts, err := native1CRawArtifacts(artifactRoot)
	if err != nil {
		return nil, err
	}
	wire := ExecuteObservation{
		SchemaVersion:    1,
		Contract:         NativeProviderContract,
		TaskID:           task.ID,
		AttemptID:        attemptID,
		Stage:            "verify",
		Status:           "completed",
		Summary:          "Recovered original completed native verification without repeating database operations.",
		SideEffects:      "none",
		Dependencies:     current,
		SourceManifest:   manifest,
		Artifacts:        artifacts,
		ProviderContract: providerContract(engineFromMap(asMap(payload["engine"]))),
	}
	outcome := "PASS"
	evidence := makeEvidence(wire, current, artifacts, "", outcome)
	terminal, err := normalizedTerminalResult(wire, outcome)
	if err != nil {
		return nil, err
	}
	terminalHash, err := Hash(terminal)
	if err != nil {
		return nil, err
	}
	evidence["result_sha256"] = terminalHash
	if err := applyObservation(payload, wire, evidence, false); err != nil {
		return nil, err
	}
	if err := persistProviderArtifacts(attemptPath, artifactRoot, artifacts); err != nil {
		return nil, err
	}
	if _, err := writeImmutableJSON(filepath.Join(attemptPath, "result.json"), toExecuteObservation(wire)); err != nil {
		return nil, err
	}
	if _, err := writeImmutableJSON(filepath.Join(attemptPath, "terminal.json"), terminal); err != nil {
		return nil, err
	}
	state, err := appendControllerRevision(repository, task, payload, task.Revision)
	if err != nil {
		return nil, err
	}
	payload = asMap(state["controller"])
	if err := Native1CCompleteSavedSuccess(task.ID, attemptID, rawDir, nativeRecoveryRuntime(hosts...)); err != nil {
		return nil, err
	}
	invokeNativeMemory(state, payload, "extract-attempt", "", terminal, terminalHash, nil, "", nil, hosts...)
	next, err := controllerNext(state, payload)
	if err != nil {
		return nil, err
	}
	return controllerEnvelope(state, payload, next), nil
}

// native1CRawArtifacts walks the retained raw evidence of one attempt and
// projects it into the provider artifact shape (paths relative to the
// artifact root), mirroring Get-BFRawHashes on the native path.
func native1CRawArtifacts(artifactRoot string) ([]ArtifactRef, error) {
	refs := []ArtifactRef{}
	if err := filepath.WalkDir(artifactRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(artifactRoot, path)
		if err != nil {
			return err
		}
		data, err := ReadFileBytes(path)
		if err != nil {
			return err
		}
		refs = append(refs, ArtifactRef{Path: filepath.ToSlash(relative), SHA256: fileSHA256(data), SizeBytes: int64(len(data)), Kind: "raw"})
		return nil
	}); err != nil {
		return nil, blocked("retained raw evidence cannot be bound: %v", err)
	}
	return refs, nil
}

// native1CRawHashes projects the retained raw evidence into the absolute
// path+hash shape of the PS recovery ledger.
func native1CRawHashes(root string) ([]any, error) {
	rows := []any{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		data, err := ReadFileBytes(path)
		if err != nil {
			return err
		}
		rows = append(rows, map[string]any{"path": path, "sha256": fileSHA256(data)})
		return nil
	}); err != nil {
		return nil, blocked("retained raw evidence cannot be bound: %v", err)
	}
	return rows, nil
}
