package repository

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func declaredStageArtifact(observation ExecuteObservation, root, relative string) ([]byte, error) {
	var descriptor *ArtifactRef
	for i := range observation.Artifacts {
		if observation.Artifacts[i].Path == relative {
			if descriptor != nil {
				return nil, invalid("duplicate stage artifact %s", relative)
			}
			descriptor = &observation.Artifacts[i]
		}
	}
	if descriptor == nil {
		return nil, blocked("required stage artifact is missing: %s", relative)
	}
	data, err := readProviderArtifactFile(root, relative)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != descriptor.SizeBytes || fileSHA256(data) != descriptor.SHA256 {
		return nil, blocked("stage artifact bytes changed: %s", relative)
	}
	return data, nil
}

func declaredStageJSON(observation ExecuteObservation, root, relative string) (map[string]any, error) {
	data, err := declaredStageArtifact(observation, root, relative)
	if err != nil {
		return nil, err
	}
	object, err := DecodeObject(data)
	if err != nil {
		return nil, blocked("invalid stage artifact %s: %v", relative, err)
	}
	return object, nil
}

func validateStageEvidence(payload map[string]any, observation ExecuteObservation, artifactRoot string) (string, error) {
	switch observation.Status {
	case "failed":
		return "FAIL", nil
	case "blocked":
		return "BLOCKED", nil
	case "needs_input":
		return "NEEDS_INPUT", nil
	case "completed":
	default:
		return "", invalid("invalid provider terminal status")
	}
	proposal := observation.Proposal
	switch observation.Stage {
	case "inspect":
		copy, err := cloneObject(payload)
		if err != nil {
			return "", err
		}
		if _, err := nativeObject(proposal, []string{"complexity", "risk", "impact_flags", "rationale"}, nil, "classification"); err != nil {
			return "", err
		}
		if err := strengthenClassification(copy, proposal); err != nil {
			return "", err
		}
		for _, flag := range anyItems(asMap(copy["classification"])["impact_flags"]) {
			if flag == "ambiguous_business_rule" {
				return "NEEDS_INPUT", nil
			}
		}
		return "PASS", nil
	case "spec":
		if err := validateNativeSpecEvidence(payload, observation, artifactRoot); err != nil {
			return "", err
		}
		return "PASS", nil
	case "spec_review":
		if err := validateNativeSpecReviewEvidence(payload, observation, artifactRoot); err != nil {
			return "", err
		}
		return "PASS", nil
	case "implement":
		object, err := nativeObject(proposal, []string{"changed_files"}, []string{"observations"}, "implementation")
		if err != nil {
			return "", err
		}
		paths, err := nativeArray(object["changed_files"], "changed_files", false)
		if err != nil {
			return "", err
		}
		for _, raw := range paths {
			if err := validateRelativeNativePath(asStringOr(raw), false); err != nil {
				return "", err
			}
		}
		return "PASS", nil
	case "code_review":
		return validateNativeCodeReviewEvidence(payload, observation, artifactRoot)
	case "verify":
		if err := validateNativeCriterionEvidence(payload, observation, artifactRoot); err != nil {
			return "", err
		}
		return "PASS", nil
	case "diagnose":
		object, err := nativeObject(proposal, []string{"failure_attempt_id", "category", "reason", "evidence", "fix_instructions"}, nil, "diagnosis")
		if err != nil {
			return "", err
		}
		if object["failure_attempt_id"] != asMap(payload["repair"])["pending_failure"] {
			return "", blocked("diagnosis is not bound to the pending failed verification")
		}
		for _, key := range []string{"reason", "evidence"} {
			if err := validateNativeText(object[key], "diagnosis."+key); err != nil {
				return "", err
			}
		}
		switch asStringOr(object["category"]) {
		case "implementation":
			if err := validateNativeText(object["fix_instructions"], "diagnosis.fix_instructions"); err != nil {
				return "", err
			}
			if observation.SideEffects != "none" {
				return "", blocked("repair diagnosis changed source or has an unknown effect")
			}
			return "REPAIR", nil
		case "business_rule":
			return "NEEDS_INPUT", nil
		case "test_contract", "environment", "unknown":
			return "BLOCKED", nil
		default:
			return "", invalid("unsupported diagnosis category")
		}
	default:
		return "", invalid("unsupported native stage %s", observation.Stage)
	}
}

func nativeChangePath(payload map[string]any) string {
	return filepath.Join(asStringOr(payload["project_path"]), "openspec", "changes", "bsl-flow-"+asStringOr(asMap(payload["request"])["request_id"]))
}

func validateNativeSpecEvidence(payload map[string]any, observation ExecuteObservation, root string) error {
	if observation.Proposal == nil {
		return blocked("completed specification stage omitted its proposal")
	}
	proposal, err := nativeObject(observation.Proposal, []string{"spec", "design"}, nil, "specification")
	if err != nil {
		return err
	}
	if err := validateNativeText(proposal["spec"], "specification.spec"); err != nil {
		return err
	}
	classification := asMap(payload["classification"])
	text := asStringOr(proposal["spec"])
	for _, field := range []struct{ key, labels string }{{"complexity", "Complexity|Сложность"}, {"risk", "Risk|Риск"}} {
		pattern := `(?m)^- (?:` + field.labels + `): ` + regexp.QuoteMeta(asStringOr(classification[field.key])) + `\s*$`
		if !regexp.MustCompile(pattern).MatchString(text) {
			return blocked("specification %s differs from trusted classification", field.key)
		}
	}
	if classification["complexity"] == "L" || classification["risk"] == "high" {
		if err := validateNativeText(proposal["design"], "required design"); err != nil {
			return err
		}
	}
	for _, name := range []string{"spec", "design"} {
		if proposal[name] == nil {
			continue
		}
		value, ok := asString(proposal[name])
		if !ok {
			return invalid("%s must be text or null", name)
		}
		retained, err := declaredStageArtifact(observation, root, "raw/"+name+".md")
		if err != nil {
			return err
		}
		current, err := ReadFileBytes(filepath.Join(nativeChangePath(payload), name+".md"))
		if err != nil || !bytes.Equal(current, retained) || string(retained) != value {
			return blocked("specification output %s differs from retained/current bytes", name)
		}
	}
	original, err := ReadFileBytes(filepath.Join(nativeChangePath(payload), "original-task.md"))
	if err != nil || string(original) != asStringOr(asMap(payload["request"])["prompt"]) {
		return blocked("original task binding changed")
	}
	lint, err := declaredStageJSON(observation, root, "raw/spec-lint.json")
	if err != nil {
		return err
	}
	if !asBoolOr(lint["passed"]) || len(anyItems(lint["errors"])) != 0 {
		return blocked("specification has no passing deterministic lint receipt")
	}
	return nil
}

func validateNativeSpecReviewEvidence(payload map[string]any, observation ExecuteObservation, root string) error {
	final, err := declaredStageJSON(observation, root, "raw/final-validation.json")
	if err != nil {
		return err
	}
	if asIntOr(final["schema_version"]) == 2 {
		return validateNativeCouncilSpecReviewEvidence(payload, observation, root, final)
	}
	if asIntOr(final["schema_version"]) != 1 || !asBoolOr(final["passed"]) || len(anyItems(final["errors"])) != 0 {
		return blocked("spec review final validation did not pass")
	}
	inputs, ok := final["inputs"].(map[string]any)
	if !ok {
		return blocked("spec review final validation lacks exact input bindings")
	}
	for _, pair := range []struct{ name, key string }{{"review.json", "review_sha256"}, {"review-reconciliation.json", "reconciliation_sha256"}} {
		retained, err := declaredStageArtifact(observation, root, "raw/"+pair.name)
		if err != nil {
			return err
		}
		current, err := ReadFileBytes(filepath.Join(nativeChangePath(payload), pair.name))
		if err != nil || !bytes.Equal(current, retained) || fileSHA256(retained) != asStringOr(inputs[pair.key]) {
			return blocked("spec review %s binding changed", pair.name)
		}
	}
	for _, pair := range []struct{ name, key string }{{"spec.md", "final_spec_sha256"}, {"design.md", "final_design_sha256"}, {"original-task.md", "original_task_sha256"}} {
		data, err := ReadFileBytes(filepath.Join(nativeChangePath(payload), pair.name))
		if inputs[pair.key] == nil && pair.name == "design.md" && os.IsNotExist(err) {
			continue
		}
		if err != nil || fileSHA256(data) != asStringOr(inputs[pair.key]) {
			return blocked("spec review final %s is stale", pair.name)
		}
	}
	return nil
}

func validateNativeCodeReviewEvidence(payload map[string]any, observation ExecuteObservation, root string) (string, error) {
	proposal := observation.Proposal
	review := proposal
	var reconciliation map[string]any
	if nested, ok := proposal["review"].(map[string]any); ok {
		if _, err := nativeObject(proposal, []string{"review", "reconciliation"}, nil, "code review result"); err != nil {
			return "", err
		}
		review = nested
		reconciliation = asMap(proposal["reconciliation"])
	}
	if _, err := nativeObject(review, []string{"verdict", "findings"}, []string{"coverage_review"}, "code review"); err != nil {
		return "", err
	}
	verdict := asStringOr(review["verdict"])
	findings, err := nativeArray(review["findings"], "findings", false)
	if err != nil {
		return "", err
	}
	if (verdict != "PASS" && verdict != "REVISE" && verdict != "BLOCK") || (verdict == "PASS") != (len(findings) == 0) {
		return "", invalid("contradictory code review verdict/findings")
	}
	ids := map[string]bool{}
	for _, raw := range findings {
		finding, err := nativeObject(raw, []string{"id", "severity", "file", "line", "scenario", "evidence"}, nil, "finding")
		if err != nil {
			return "", err
		}
		for _, key := range []string{"id", "scenario", "evidence"} {
			if err := validateNativeText(finding[key], "finding."+key); err != nil {
				return "", err
			}
		}
		id := asStringOr(finding["id"])
		if ids[id] || asIntOr(finding["line"]) < 1 || validateRelativeNativePath(asStringOr(finding["file"]), false) != nil {
			return "", invalid("invalid finding identity/location")
		}
		ids[id] = true
		if severity := asStringOr(finding["severity"]); severity != "critical" && severity != "high" && severity != "medium" && severity != "low" {
			return "", invalid("invalid finding severity")
		}
	}
	if _, required := asMap(payload["request"])["requirements"]; required {
		if err := validateNativeCoverageBinding(payload, observation, root, asMap(review["coverage_review"])); err != nil {
			return "", err
		}
	}
	if verdict == "PASS" {
		return "PASS", nil
	}
	if reconciliation == nil {
		return "", blocked("non-PASS code review requires independent reconciliation")
	}
	if _, err := nativeObject(reconciliation, []string{"decisions", "fix_instructions"}, nil, "reconciliation"); err != nil {
		return "", err
	}
	decisions, err := nativeArray(reconciliation["decisions"], "decisions", false)
	if err != nil {
		return "", err
	}
	if len(decisions) != len(findings) {
		return "", invalid("incomplete finding reconciliation")
	}
	accepted := false
	for _, raw := range decisions {
		d, err := nativeObject(raw, []string{"finding_id", "decision", "reason", "evidence"}, nil, "decision")
		if err != nil {
			return "", err
		}
		id := asStringOr(d["finding_id"])
		if !ids[id] {
			return "", invalid("unknown or repeated finding decision")
		}
		delete(ids, id)
		for _, key := range []string{"reason", "evidence"} {
			if err := validateNativeText(d[key], "decision."+key); err != nil {
				return "", err
			}
		}
		switch asStringOr(d["decision"]) {
		case "accepted":
			accepted = true
		case "rejected":
		default:
			return "", invalid("invalid finding decision")
		}
	}
	if accepted {
		if err := validateNativeText(reconciliation["fix_instructions"], "fix_instructions"); err != nil {
			return "", err
		}
		if asIntOr(payload["correction_rounds"]) >= 1 {
			return "FAIL", nil
		}
		return "REVISE", nil
	}
	return "PASS", nil
}

func validateNativeCriterionEvidence(payload map[string]any, observation ExecuteObservation, root string) error {
	report, err := declaredStageJSON(observation, root, "raw/observations.json")
	if err != nil {
		return err
	}
	if _, err := nativeObject(report, []string{"criteria"}, nil, "verification observations"); err != nil {
		return err
	}
	observations, err := nativeArray(report["criteria"], "verification criteria", true)
	if err != nil {
		return err
	}
	criteria := anyItems(asMap(payload["request"])["criteria"])
	if len(criteria) != len(observations) {
		return blocked("verification does not cover every declared criterion")
	}
	byID := map[string]map[string]any{}
	for _, raw := range observations {
		item := asMap(raw)
		id := asStringOr(item["criterion_id"])
		if id == "" || byID[id] != nil {
			return invalid("duplicate/invalid verification criterion id")
		}
		byID[id] = item
	}
	for _, raw := range criteria {
		criterion := asMap(raw)
		id := asStringOr(criterion["id"])
		item := byID[id]
		if item == nil || item["kind"] != criterion["kind"] || item["outcome"] != "PASS" {
			return blocked("criterion %s lacks a passing original observation", id)
		}
		if criterion["kind"] == "file_assertion" {
			if _, err := nativeObject(item, []string{"criterion_id", "kind", "file", "sha256", "outcome"}, nil, "file observation"); err != nil {
				return err
			}
			if item["file"] != criterion["path"] {
				return blocked("criterion %s refers to another source file", id)
			}
			path := filepath.Join(asStringOr(payload["worker_path"]), filepath.FromSlash(asStringOr(criterion["path"])))
			data, err := ReadFileBytes(path)
			if err != nil || !bytes.Contains(data, []byte(asStringOr(criterion["contains"]))) || fileSHA256(data) != asStringOr(item["sha256"]) {
				return blocked("criterion %s source assertion is stale or false", id)
			}
			continue
		}
		if criterion["kind"] != "static" && criterion["kind"] != "unit" {
			return blocked("unsupported verification criterion %s", id)
		}
		if _, err := nativeObject(item, []string{"criterion_id", "kind", "tests", "sha256", "outcome"}, nil, "test observation"); err != nil {
			return err
		}
		data, err := declaredStageArtifact(observation, root, "raw/"+id+"/original.junit.xml")
		if err != nil {
			return err
		}
		expected, ok := asStringSlice(criterion["expected_tests"])
		if !ok {
			return invalid("expected_tests must be strings")
		}
		passed, err := validateNativeJUnit(data, expected)
		if err != nil {
			return err
		}
		if !passed {
			return blocked("criterion %s original tests failed", id)
		}
		if fileSHA256(data) != asStringOr(item["sha256"]) || !sameNativeStrings(anyItems(item["tests"]), expected) {
			return blocked("criterion %s report binding/selection differs", id)
		}
		if err := requireCriterionProcess(observation, root, id); err != nil {
			return err
		}
	}
	return nil
}

func sameNativeStrings(values []any, expected []string) bool {
	if len(values) != len(expected) {
		return false
	}
	set := map[string]bool{}
	for _, s := range expected {
		set[s] = true
	}
	for _, raw := range values {
		s, ok := asString(raw)
		if !ok || !set[s] {
			return false
		}
		delete(set, s)
	}
	return len(set) == 0
}

func requireCriterionProcess(observation ExecuteObservation, root, id string) error {
	for _, raw := range anyItems(observation.ProcessReceipt["processes"]) {
		item := asMap(raw)
		path := asStringOr(item["exit_path"])
		if !strings.HasPrefix(path, "raw/"+id+"/") {
			continue
		}
		code, ok := asInt(item["exit_code"])
		if !ok || code != 0 || item["stop_reason"] != nil {
			continue
		}
		if _, err := declaredStageArtifact(observation, root, path); err != nil {
			return err
		}
		return nil
	}
	return blocked("criterion %s lacks a completed successful test process receipt", id)
}

func validateNativeCoverageBinding(payload map[string]any, observation ExecuteObservation, root string, coverage map[string]any) error {
	if coverage["verdict"] != "PASS" {
		return blocked("independent requirement coverage is not sufficient")
	}
	binding, err := declaredStageJSON(observation, root, "raw/coverage-review-binding.json")
	if err != nil {
		return err
	}
	if err := validateNativeCoverageAssessments(payload, coverage, binding); err != nil {
		return err
	}
	request := asMap(payload["request"])
	for key, value := range map[string]any{"requirements_sha256": request["requirements"], "criteria_sha256": request["criteria"], "coverage_sha256": coverage} {
		h, err := Hash(value)
		if err != nil || h != asStringOr(binding[key]) {
			return blocked("coverage %s is stale", key)
		}
	}
	files, err := nativeArray(binding["files"], "coverage files", true)
	if err != nil {
		return err
	}
	for _, raw := range files {
		entry := asMap(raw)
		relative := asStringOr(entry["path"])
		if err := validateRelativeNativePath(relative, false); err != nil {
			return err
		}
		data, err := ReadFileBytes(filepath.Join(asStringOr(payload["worker_path"]), filepath.FromSlash(relative)))
		if err != nil || fileSHA256(data) != asStringOr(entry["sha256"]) {
			return blocked("coverage source binding changed: %s", relative)
		}
	}
	return nil
}

func stageEvidenceError(stage string, err error) error {
	return blocked("%s evidence is not acceptable: %v", stage, err)
}
