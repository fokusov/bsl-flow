package repository

import (
	"bytes"
	"path/filepath"
)

func boundNativeEngine(payload map[string]any) (EngineIdentity, error) {
	engine := engineFromMap(payload["engine"])
	root, host, err := policyRootsFromPayload(payload)
	if err != nil {
		return EngineIdentity{}, err
	}
	engine.PolicyRoot = root
	engine.HostPath = host
	return engine, nil
}

// Output stages may update only their declared dependency; every other input
// remains bound to the immutable attempt start.
func validateNativeTerminalDependencies(payload map[string]any, observation ExecuteObservation, before, manifest map[string]any) (map[string]any, error) {
	if err := assertNativePolicyFresh(payload); err != nil {
		return nil, err
	}
	current, err := currentNativeDependencies(payload, observation.Stage, manifest)
	if err != nil {
		return nil, err
	}
	expected := before
	if observation.Status == "completed" {
		comparison, err := cloneObject(current)
		if err != nil {
			return nil, err
		}
		switch observation.Stage {
		case "implement":
			comparison["source"] = before["source"]
		case "spec":
			comparison["spec"] = before["spec"]
		case "spec_review":
			comparison["spec"] = before["spec"]
			comparison["review_binding"] = before["review_binding"]
		}
		if !equalJSON(comparison, before) {
			return nil, blocked("stage inputs changed outside its declared output")
		}
		expected = current
	}
	if !equalJSON(observation.Dependencies, expected) {
		return nil, blocked("provider dependency object differs from independently bound inputs")
	}
	return expected, nil
}

func controllerEvidenceFresh(outer, payload, evidence map[string]any) bool {
	if evidence["outcome"] != "PASS" || assertNativePolicyFresh(payload) != nil {
		return false
	}
	stage := asStringOr(evidence["stage"])
	current, err := currentNativeDependencies(payload, stage, nil)
	if err != nil {
		return false
	}
	saved, ok := evidence["dependencies"].(map[string]any)
	if !ok || len(saved) == 0 {
		return false
	}
	repository, err := OpenRepository(asStringOr(payload["project_path"]))
	if err != nil {
		return false
	}
	id := asStringOr(outer["task_id"])
	attemptID := asStringOr(evidence["attempt_id"])
	if !isUUID(attemptID) {
		return false
	}
	registered := false
	for _, raw := range anyItems(payload["attempts"]) {
		if raw == attemptID {
			registered = true
		}
	}
	if !registered {
		return false
	}
	path, err := attemptDirectory(repository, id, attemptID)
	if err != nil {
		return false
	}
	start, err := readStoredAttempt(path)
	if err != nil {
		return false
	}
	if start["task_id"] != id || start["attempt_id"] != attemptID || start["stage"] != stage || !equalJSON(start["intent_revision"], payload["intent_revision"]) || !equalJSON(start["authorization_revision"], payload["authorization_revision"]) {
		return false
	}
	if !equalJSON(saved, current) {
		if stage != "spec" {
			return false
		}
		current["spec"] = saved["spec"]
		if !equalJSON(saved, current) {
			return false
		}
		review, ok := latestEvidence(payload, "spec_review")
		if !ok || !controllerEvidenceFresh(outer, payload, review) {
			return false
		}
		reviewPath, err := attemptDirectory(repository, id, asStringOr(review["attempt_id"]))
		if err != nil {
			return false
		}
		reviewStart, err := readStoredAttempt(reviewPath)
		if err != nil || !equalJSON(asMap(reviewStart["dependencies"])["spec"], saved["spec"]) {
			return false
		}
	}
	terminalData, err := ReadFileBytes(filepath.Join(path, "terminal.json"))
	if err != nil {
		return false
	}
	terminal, err := DecodeObject(terminalData)
	if err != nil {
		return false
	}
	// Hashing only the decoded object would accept whitespace or a BOM added to
	// immutable terminal evidence. Require the exact canonical bytes retained
	// for the registered result hash.
	canonicalTerminal, err := Canonical(terminal)
	if err != nil || !bytes.Equal(canonicalTerminal, terminalData) {
		return false
	}
	hash, err := Hash(terminal)
	if err != nil || hash != asStringOr(evidence["result_sha256"]) {
		return false
	}
	for _, key := range []string{"stage", "attempt_id", "outcome", "dependencies", "raw_hashes"} {
		if !equalJSON(terminal[key], evidence[key]) {
			return false
		}
	}
	if terminal["task_id"] != id {
		return false
	}
	observation, err := readStoredObservation(filepath.Join(path, "result.json"))
	if err != nil || observation.TaskID != id || observation.AttemptID != attemptID || observation.Stage != stage || !equalJSON(observation.Dependencies, saved) {
		return false
	}
	engine, err := boundNativeEngine(payload)
	if err != nil || validateNativeTransport(path, observation, engine) != nil {
		return false
	}
	refs := makeEvidence(observation, saved, observation.Artifacts, hash, "PASS")
	if !equalJSON(refs["raw_hashes"], evidence["raw_hashes"]) || !equalJSON(observation.Proposal, terminal["proposal"]) {
		return false
	}
	for _, artifact := range observation.Artifacts {
		data, err := readProviderArtifactFile(filepath.Join(path, "artifacts"), artifact.Path)
		if err != nil || fileSHA256(data) != artifact.SHA256 || int64(len(data)) != artifact.SizeBytes {
			return false
		}
	}
	return true
}
