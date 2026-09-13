package repository

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
)

func nativeVerificationFailureEligible(payload map[string]any, observation ExecuteObservation, root string) (bool, error) {
	if observation.Stage != "verify" || observation.Status != "failed" || observation.SideEffects != "none" || !asBoolOr(observation.Proposal["repair_eligible"]) {
		return false, nil
	}
	request := asMap(payload["request"])
	if asIntOr(request["max_source_repairs"]) <= asIntOr(asMap(payload["repair"])["rounds"]) {
		return false, nil
	}
	marker, err := nativeObject(observation.Proposal, []string{"repair_eligible", "criterion_id", "kind", "observation"}, nil, "failed verification marker")
	if err != nil {
		return false, nil
	}
	var criterion map[string]any
	for _, raw := range anyItems(request["criteria"]) {
		item := asMap(raw)
		switch item["kind"] {
		case "file_assertion":
		case "static", "unit":
			if !asBoolOr(item["retry_safe"]) {
				return false, nil
			}
		default:
			return false, nil
		}
		if item["id"] == marker["criterion_id"] {
			criterion = item
		}
	}
	if criterion == nil || criterion["kind"] != marker["kind"] || criterion["observation"] != marker["observation"] {
		return false, nil
	}
	failure, err := declaredStageJSON(observation, root, "raw/failure.json")
	if err != nil || failure["reason"] != observation.Summary || failure["side_effects"] != "none" {
		return false, nil
	}
	if criterion["kind"] == "file_assertion" {
		path := filepath.Join(asStringOr(payload["worker_path"]), filepath.FromSlash(asStringOr(criterion["path"])))
		data, err := ReadFileBytes(path)
		if os.IsNotExist(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return !bytes.Contains(data, []byte(asStringOr(criterion["contains"]))), nil
	}
	id := asStringOr(criterion["id"])
	data, err := declaredStageArtifact(observation, root, "raw/"+id+"/original.junit.xml")
	if err != nil {
		return false, nil
	}
	expected, ok := asStringSlice(criterion["expected_tests"])
	if !ok {
		return false, nil
	}
	passed, err := validateNativeJUnit(data, expected)
	if err != nil || passed {
		return false, nil
	}
	prefix := "raw/" + id + "/"
	for _, raw := range anyItems(observation.ProcessReceipt["processes"]) {
		process := asMap(raw)
		exitPath := asStringOr(process["exit_path"])
		if strings.HasPrefix(exitPath, prefix) && process["stop_reason"] == nil {
			if _, ok := asInt(process["exit_code"]); ok {
				return true, nil
			}
		}
	}
	return false, nil
}
func validateNativeRepairFailure(outer, payload map[string]any) error {
	repair := asMap(payload["repair"])
	id := asStringOr(repair["pending_failure"])
	if !isUUID(id) || asIntOr(repair["rounds"]) >= asIntOr(asMap(payload["request"])["max_source_repairs"]) {
		return blocked("source repair budget exhausted")
	}
	var entry map[string]any
	for _, raw := range anyItems(payload["evidence"]) {
		e := asMap(raw)
		if e["attempt_id"] == id {
			if entry != nil {
				return blocked("duplicate failed verification")
			}
			entry = e
		}
	}
	if entry == nil || entry["stage"] != "verify" || entry["outcome"] != "FAIL" {
		return blocked("repair requires registered failed verification")
	}
	current, err := currentNativeDependencies(payload, "verify", nil)
	if err != nil {
		return err
	}
	if !equalJSON(current, entry["dependencies"]) {
		return blocked("failed verification dependencies changed")
	}
	repository, err := OpenRepository(asStringOr(payload["project_path"]))
	if err != nil {
		return err
	}
	path, err := attemptDirectory(repository, asStringOr(outer["task_id"]), id)
	if err != nil {
		return err
	}
	data, err := ReadFileBytes(filepath.Join(path, "terminal.json"))
	if err != nil {
		return err
	}
	terminal, err := DecodeObject(data)
	if err != nil {
		return err
	}
	hash, err := Hash(terminal)
	if err != nil || hash != asStringOr(entry["result_sha256"]) || fileSHA256(data) != hash {
		return blocked("failed verification terminal changed")
	}
	observation, err := readStoredObservation(filepath.Join(path, "result.json"))
	if err != nil {
		return err
	}
	engine, err := boundNativeEngine(payload)
	if err != nil {
		return err
	}
	if err := validateNativeTransport(path, observation, engine); err != nil {
		return err
	}
	if observation.TaskID != asStringOr(outer["task_id"]) || observation.AttemptID != id || !equalJSON(observation.Dependencies, current) {
		return blocked("failed verification identity changed")
	}
	for _, artifact := range observation.Artifacts {
		data, err := readProviderArtifactFile(filepath.Join(path, "artifacts"), artifact.Path)
		if err != nil || fileSHA256(data) != artifact.SHA256 {
			return blocked("failed verification raw artifact changed")
		}
	}
	eligible, err := nativeVerificationFailureEligible(payload, observation, filepath.Join(path, "artifacts"))
	if err != nil {
		return err
	}
	if !eligible {
		return blocked("failed verification is not eligible for source repair")
	}
	return nil
}
