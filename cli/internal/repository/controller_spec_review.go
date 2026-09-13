package repository

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// validateNativeCouncilSpecReviewEvidence validates the public v2 council
// receipt independently from the PowerShell final validator.  The provider
// owns the files under raw/, but the controller owns the acceptance decision:
// the retained bytes, live OpenSpec files and all final hash bindings must
// describe the same review.
func validateNativeCouncilSpecReviewEvidence(payload map[string]any, observation ExecuteObservation, root string, final map[string]any) error {
	if err := validateNativeCouncilFinalValidation(final); err != nil {
		return err
	}

	reviewBytes, err := declaredStageArtifact(observation, root, "raw/review.json")
	if err != nil {
		return err
	}
	review, err := DecodeObject(reviewBytes)
	if err != nil {
		return blocked("spec review council artifact is invalid: %v", err)
	}
	if err := validateNativeCouncilReview(review); err != nil {
		return err
	}
	if asStringOr(final["diversity"]) != asStringOr(review["diversity"]) {
		return blocked("spec review final diversity differs from council review")
	}

	changeRoot := nativeChangePath(payload)
	currentReview, err := ReadFileBytes(filepath.Join(changeRoot, "review.json"))
	if err != nil || !bytes.Equal(currentReview, reviewBytes) {
		return blocked("spec review review.json binding changed")
	}
	reviewHash := fileSHA256(reviewBytes)

	finalInputs := asMap(final["inputs"])
	if err := validateNativeCouncilHash(finalInputs["review_sha256"], "final-validation.inputs.review_sha256", false); err != nil {
		return err
	}
	if asStringOr(finalInputs["review_sha256"]) != reviewHash {
		return blocked("spec review final review hash is stale")
	}

	reconciliation, reconciliationHash, err := readNativeCouncilReconciliation(observation, root, changeRoot, finalInputs)
	if err != nil {
		return err
	}
	if reconciliation != nil {
		if err := validateNativeCouncilReconciliationBinding(review, reconciliation); err != nil {
			return err
		}
		if asStringOr(reconciliation["review_sha256"]) != reviewHash {
			return blocked("review reconciliation is bound to another review.json")
		}
		if asStringOr(finalInputs["reconciliation_sha256"]) != reconciliationHash {
			return blocked("spec review final reconciliation hash is stale")
		}
	} else if finalInputs["reconciliation_sha256"] != nil {
		return blocked("spec review final validation claims a missing reconciliation artifact")
	}

	if err := validateNativeCouncilLiveBindings(payload, review, finalInputs, changeRoot); err != nil {
		return err
	}
	return nil
}

func validateNativeCouncilFinalValidation(final map[string]any) error {
	if final == nil {
		return blocked("spec review final validation is missing")
	}
	if _, err := nativeObject(final, []string{
		"schema_version", "checked_at_utc", "passed", "review_schema", "verdict", "diversity", "inputs", "errors",
	}, nil, "spec review final validation"); err != nil {
		return err
	}
	if asIntOr(final["schema_version"]) != 2 || asIntOr(final["review_schema"]) != 2 {
		return blocked("spec review final validation has an unsupported schema")
	}
	if err := validateNativeText(final["checked_at_utc"], "spec review final validation.checked_at_utc"); err != nil {
		return err
	}
	if !asBoolOr(final["passed"]) || asStringOr(final["verdict"]) != "PASS" {
		return blocked("spec review final validation did not pass the council verdict")
	}
	if diversity := asStringOr(final["diversity"]); diversity != "multi_model" && diversity != "multi_role_single_model" {
		return blocked("spec review final validation lacks accepted council diversity")
	}
	errors, err := nativeArray(final["errors"], "spec review final validation.errors", false)
	if err != nil {
		return err
	}
	if len(errors) != 0 {
		return blocked("spec review final validation contains errors")
	}
	inputs, err := nativeObject(final["inputs"], []string{
		"review_sha256", "reconciliation_sha256", "final_spec_sha256", "final_design_sha256", "original_task_sha256",
	}, nil, "spec review final validation.inputs")
	if err != nil {
		return err
	}
	for _, field := range []string{"review_sha256", "final_spec_sha256", "original_task_sha256"} {
		if err := validateNativeCouncilHash(inputs[field], "spec review final validation.inputs."+field, false); err != nil {
			return err
		}
	}
	for _, field := range []string{"reconciliation_sha256", "final_design_sha256"} {
		if err := validateNativeCouncilHash(inputs[field], "spec review final validation.inputs."+field, true); err != nil {
			return err
		}
	}
	return nil
}

func validateNativeCouncilReview(review map[string]any) error {
	if _, err := nativeObject(review, []string{
		"schema_version", "reviewed_at_utc", "council_schema_version", "verdict", "diversity", "fallback_visible", "inputs", "manifest", "members", "findings", "protected", "questions", "chair", "reconciliation", "gate",
	}, nil, "council review"); err != nil {
		return err
	}
	if asIntOr(review["schema_version"]) != 2 || asIntOr(review["council_schema_version"]) != 1 {
		return blocked("council review has an unsupported schema")
	}
	if err := validateNativeText(review["reviewed_at_utc"], "council review.reviewed_at_utc"); err != nil {
		return err
	}
	if asStringOr(review["verdict"]) != "PASS" {
		return blocked("council review verdict is not PASS")
	}
	diversity := asStringOr(review["diversity"])
	if diversity != "multi_model" && diversity != "multi_role_single_model" {
		return blocked("council review diversity is not accepted")
	}
	if _, ok := asBool(review["fallback_visible"]); !ok {
		return invalid("council review.fallback_visible must be boolean")
	}

	inputs, err := nativeObject(review["inputs"], []string{"original_task_sha256", "spec_sha256", "design_sha256", "policy_hash"}, nil, "council review.inputs")
	if err != nil {
		return err
	}
	for _, field := range []string{"original_task_sha256", "spec_sha256", "policy_hash"} {
		if err := validateNativeCouncilHash(inputs[field], "council review.inputs."+field, false); err != nil {
			return err
		}
	}
	if err := validateNativeCouncilHash(inputs["design_sha256"], "council review.inputs.design_sha256", true); err != nil {
		return err
	}

	if err := validateNativeCouncilManifest(review["manifest"]); err != nil {
		return err
	}
	for _, field := range []string{"findings", "protected", "questions"} {
		if _, err := nativeArray(review[field], "council review."+field, false); err != nil {
			return err
		}
	}
	if err := validateNativeCouncilMembers(review["members"], inputs, diversity); err != nil {
		return err
	}

	chair, err := nativeObject(review["chair"], []string{"verdict", "decisions", "protected_decisions", "requirement_refs", "final_spec_text", "final_design_text"}, nil, "council review.chair")
	if err != nil {
		return err
	}
	if asStringOr(chair["verdict"]) != "PASS" {
		return blocked("council chair verdict is not PASS")
	}
	if err := validateNativeText(chair["final_spec_text"], "council review.chair.final_spec_text"); err != nil {
		return err
	}
	if chair["final_design_text"] != nil {
		if err := validateNativeText(chair["final_design_text"], "council review.chair.final_design_text"); err != nil {
			return err
		}
	}
	for _, field := range []string{"decisions", "protected_decisions", "requirement_refs"} {
		if _, err := nativeArray(chair[field], "council review.chair."+field, false); err != nil {
			return err
		}
	}

	reconciliation, err := nativeObject(review["reconciliation"], []string{"review_sha256", "draft_spec_sha256", "final_spec_sha256", "draft_design_sha256", "final_design_sha256"}, nil, "council review.reconciliation")
	if err != nil {
		return err
	}
	for _, field := range []string{"review_sha256", "draft_spec_sha256", "final_spec_sha256"} {
		if err := validateNativeCouncilHash(reconciliation[field], "council review.reconciliation."+field, false); err != nil {
			return err
		}
	}
	for _, field := range []string{"draft_design_sha256", "final_design_sha256"} {
		if err := validateNativeCouncilHash(reconciliation[field], "council review.reconciliation."+field, true); err != nil {
			return err
		}
	}
	if asStringOr(reconciliation["draft_spec_sha256"]) != asStringOr(inputs["spec_sha256"]) {
		return blocked("council review draft spec binding changed")
	}
	if !sameNativeNullableHash(reconciliation["draft_design_sha256"], inputs["design_sha256"]) {
		return blocked("council review draft design binding changed")
	}

	gate, err := nativeObject(review["gate"], []string{"structural_only", "passed"}, nil, "council review.gate")
	if err != nil {
		return err
	}
	if !asBoolOr(gate["structural_only"]) || !asBoolOr(gate["passed"]) {
		return blocked("council review gate is not a passing structural gate")
	}
	return nil
}

func validateNativeCouncilManifest(value any) error {
	manifest, err := nativeObject(value, []string{"version", "requirements"}, nil, "council review.manifest")
	if err != nil {
		return err
	}
	if asIntOr(manifest["version"]) != 1 {
		return blocked("council review manifest has an unsupported version")
	}
	requirements, err := nativeArray(manifest["requirements"], "council review.manifest.requirements", true)
	if err != nil {
		return err
	}
	for index, raw := range requirements {
		item, err := nativeObject(raw, []string{"id", "source_hash", "draft_ref"}, nil, "council review manifest requirement")
		if err != nil {
			return err
		}
		if asStringOr(item["id"]) != "REQ-"+formatNativeSequence(index+1) {
			return invalid("council review manifest requirement IDs are not sequential")
		}
		if err := validateNativeCouncilHash(item["source_hash"], "council review manifest requirement.source_hash", false); err != nil {
			return err
		}
		if err := validateNativeText(item["draft_ref"], "council review manifest requirement.draft_ref"); err != nil {
			return err
		}
	}
	return nil
}

func validateNativeCouncilMembers(value any, reviewInputs map[string]any, diversity string) error {
	members, err := nativeArray(value, "council review.members", true)
	if err != nil {
		return err
	}
	seenRoles := map[string]bool{}
	models := map[string]bool{}
	hasChair := false
	for _, raw := range members {
		member, err := nativeObject(raw, []string{
			"schema_version", "role", "attempt_id", "status", "summary", "requested", "observed", "execution_mode", "fallback_reason", "input_hashes", "payload_sha256", "usage", "cost_state", "dispatched_at_utc", "completed_at_utc",
		}, nil, "council review.member")
		if err != nil {
			return err
		}
		role := asStringOr(member["role"])
		if role != "brainstorm" && role != "intent_critic" && role != "architecture_critic" && role != "executability_critic" && role != "chair" {
			return invalid("council review member has an unsupported role")
		}
		if seenRoles[role] {
			return invalid("council review member roles are duplicated")
		}
		seenRoles[role] = true
		if role == "chair" {
			hasChair = true
		}
		if asIntOr(member["schema_version"]) != 1 || asStringOr(member["status"]) != "completed" {
			return blocked("council review member is not a completed v1 envelope")
		}
		if err := validateNativeText(member["attempt_id"], "council review.member.attempt_id"); err != nil {
			return err
		}
		if err := validateNativeText(member["summary"], "council review.member.summary"); err != nil {
			return err
		}
		requested, err := nativeObject(member["requested"], []string{"provider", "model", "effort"}, nil, "council review.member.requested")
		if err != nil {
			return err
		}
		for _, field := range []string{"provider", "model", "effort"} {
			if err := validateNativeText(requested[field], "council review.member.requested."+field); err != nil {
				return err
			}
		}
		observed, err := nativeObject(member["observed"], []string{"provider", "model", "effort"}, nil, "council review.member.observed")
		if err != nil {
			return err
		}
		for _, field := range []string{"provider", "model"} {
			if err := validateNativeText(observed[field], "council review.member.observed."+field); err != nil {
				return blocked("council review member lacks observed %s", field)
			}
		}
		if observed["effort"] != nil {
			if err := validateNativeText(observed["effort"], "council review.member.observed.effort"); err != nil {
				return err
			}
		}
		models[asStringOr(observed["model"])] = true
		mode := asStringOr(member["execution_mode"])
		if mode != "direct_api" && mode != "current_agent_fallback" {
			return invalid("council review member has an unsupported execution mode")
		}
		if member["fallback_reason"] != nil {
			if err := validateNativeText(member["fallback_reason"], "council review.member.fallback_reason"); err != nil {
				return err
			}
		}
		if err := validateNativeCouncilHash(member["payload_sha256"], "council review.member.payload_sha256", false); err != nil {
			return err
		}
		if err := validateNativeText(member["cost_state"], "council review.member.cost_state"); err != nil {
			return err
		}
		for _, field := range []string{"dispatched_at_utc", "completed_at_utc"} {
			if err := validateNativeText(member[field], "council review.member."+field); err != nil {
				return err
			}
		}
		if member["usage"] != nil {
			if _, err := nativeObject(member["usage"], []string{"input_tokens", "output_tokens", "reasoning_tokens"}, nil, "council review.member.usage"); err != nil {
				return err
			}
		}
		if err := validateNativeCouncilMemberInputHashes(member["input_hashes"], reviewInputs, role); err != nil {
			return err
		}
	}
	if !hasChair {
		return blocked("council review has no chair member")
	}
	if diversity == "multi_model" && len(models) < 2 {
		return blocked("council review claims multi_model diversity without two observed models")
	}
	if diversity == "multi_role_single_model" && len(models) != 1 {
		return blocked("council review claims single-model diversity with multiple observed models")
	}
	return nil
}

func validateNativeCouncilMemberInputHashes(value any, reviewInputs map[string]any, role string) error {
	hashes, err := nativeObject(value, []string{"original_task_sha256", "spec_sha256", "policy_hash"}, []string{"design_sha256", "evidence_sha256", "rubric_sha256", "member_aggregate_sha256"}, "council review.member.input_hashes")
	if err != nil {
		return err
	}
	for _, field := range []string{"original_task_sha256", "spec_sha256", "policy_hash"} {
		if err := validateNativeCouncilHash(hashes[field], "council review.member.input_hashes."+field, false); err != nil {
			return err
		}
	}
	if err := validateNativeCouncilHash(hashes["design_sha256"], "council review.member.input_hashes.design_sha256", true); err != nil {
		return err
	}
	if !sameNativeNullableHash(hashes["original_task_sha256"], reviewInputs["original_task_sha256"]) ||
		!sameNativeNullableHash(hashes["spec_sha256"], reviewInputs["spec_sha256"]) ||
		!sameNativeNullableHash(hashes["design_sha256"], reviewInputs["design_sha256"]) ||
		!sameNativeNullableHash(hashes["policy_hash"], reviewInputs["policy_hash"]) {
		return blocked("council review member input bindings changed")
	}
	if role == "chair" {
		if err := validateNativeCouncilHash(hashes["member_aggregate_sha256"], "council review.member.input_hashes.member_aggregate_sha256", false); err != nil {
			return err
		}
	}
	for _, field := range []string{"evidence_sha256", "rubric_sha256"} {
		if hashes[field] != nil {
			if err := validateNativeCouncilHash(hashes[field], "council review.member.input_hashes."+field, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func readNativeCouncilReconciliation(observation ExecuteObservation, root, changeRoot string, finalInputs map[string]any) (map[string]any, string, error) {
	declared := nativeCouncilArtifactDeclared(observation, "raw/review-reconciliation.json")
	livePath := filepath.Join(changeRoot, "review-reconciliation.json")
	_, liveErr := os.Stat(livePath)
	claimed := finalInputs["reconciliation_sha256"] != nil
	if !declared && !claimed && os.IsNotExist(liveErr) {
		return nil, "", nil
	}
	if !declared {
		return nil, "", blocked("spec review reconciliation artifact is not declared")
	}
	data, err := declaredStageArtifact(observation, root, "raw/review-reconciliation.json")
	if err != nil {
		return nil, "", err
	}
	current, err := ReadFileBytes(livePath)
	if err != nil || !bytes.Equal(current, data) {
		return nil, "", blocked("spec review review-reconciliation.json binding changed")
	}
	object, err := DecodeObject(data)
	if err != nil {
		return nil, "", blocked("spec review reconciliation artifact is invalid: %v", err)
	}
	if err := validateNativeCouncilReconciliation(object); err != nil {
		return nil, "", err
	}
	return object, fileSHA256(data), nil
}

func validateNativeCouncilReconciliation(value map[string]any) error {
	if _, err := nativeObject(value, []string{
		"schema_version", "review_sha256", "draft_spec_sha256", "final_spec_sha256", "draft_design_sha256", "final_design_sha256", "reconciled_at_utc", "summary", "decisions", "do_not_change_checks",
	}, nil, "review reconciliation"); err != nil {
		return err
	}
	if asIntOr(value["schema_version"]) != 2 {
		return blocked("review reconciliation has an unsupported schema")
	}
	for _, field := range []string{"review_sha256", "draft_spec_sha256", "final_spec_sha256"} {
		if err := validateNativeCouncilHash(value[field], "review reconciliation."+field, false); err != nil {
			return err
		}
	}
	for _, field := range []string{"draft_design_sha256", "final_design_sha256"} {
		if err := validateNativeCouncilHash(value[field], "review reconciliation."+field, true); err != nil {
			return err
		}
	}
	if err := validateNativeText(value["reconciled_at_utc"], "review reconciliation.reconciled_at_utc"); err != nil {
		return err
	}
	if err := validateNativeText(value["summary"], "review reconciliation.summary"); err != nil {
		return err
	}
	for _, field := range []string{"decisions", "do_not_change_checks"} {
		if _, err := nativeArray(value[field], "review reconciliation."+field, false); err != nil {
			return err
		}
	}
	return nil
}

func validateNativeCouncilReconciliationBinding(review, reconciliation map[string]any) error {
	reviewReconciliation := asMap(review["reconciliation"])
	if reviewReconciliation == nil {
		return blocked("council review has no inline reconciliation")
	}
	if reconciliation["review_sha256"] == nil || !isSHA256(asStringOr(reconciliation["review_sha256"])) {
		return invalid("review reconciliation.review_sha256 is invalid")
	}
	for _, field := range []string{"draft_spec_sha256", "final_spec_sha256", "draft_design_sha256", "final_design_sha256"} {
		if !sameNativeNullableHash(reconciliation[field], reviewReconciliation[field]) {
			return blocked("review reconciliation.%s differs from inline council reconciliation", field)
		}
	}
	return nil
}

func validateNativeCouncilLiveBindings(payload, review, finalInputs map[string]any, changeRoot string) error {
	paths := []struct {
		name     string
		inputKey string
		allowNil bool
	}{
		{name: "original-task.md", inputKey: "original_task_sha256"},
		{name: "spec.md", inputKey: "final_spec_sha256"},
		{name: "design.md", inputKey: "final_design_sha256", allowNil: true},
	}
	for _, item := range paths {
		data, err := ReadFileBytes(filepath.Join(changeRoot, item.name))
		if err != nil {
			if item.allowNil && finalInputs[item.inputKey] == nil && os.IsNotExist(err) {
				continue
			}
			return blocked("spec review final %s is missing or unreadable", item.name)
		}
		if item.allowNil && finalInputs[item.inputKey] == nil {
			return blocked("spec review final %s exists without a recorded hash", item.name)
		}
		if fileSHA256(data) != asStringOr(finalInputs[item.inputKey]) {
			return blocked("spec review final %s is stale", item.name)
		}
	}

	reviewReconciliation := asMap(review["reconciliation"])
	for _, pair := range []struct{ field, key string }{
		{field: "final_spec_sha256", key: "final_spec_sha256"},
		{field: "final_design_sha256", key: "final_design_sha256"},
	} {
		if !sameNativeNullableHash(reviewReconciliation[pair.field], finalInputs[pair.key]) {
			return blocked("council review final %s binding changed", pair.field)
		}
	}
	request := asMap(payload["request"])
	original, err := ReadFileBytes(filepath.Join(changeRoot, "original-task.md"))
	if err == nil {
		reviewInputs := asMap(review["inputs"])
		if fileSHA256(original) != asStringOr(reviewInputs["original_task_sha256"]) ||
			fileSHA256(original) != asStringOr(finalInputs["original_task_sha256"]) {
			return blocked("original task hash is stale")
		}
		prompt, ok := asString(request["prompt"])
		if ok && prompt != string(original) {
			return blocked("original task binding changed")
		}
	}
	return nil
}

func validateNativeCouncilHash(value any, name string, allowNil bool) error {
	if value == nil {
		if allowNil {
			return nil
		}
		return invalid("%s is required", name)
	}
	if err := validateNativeHash(value, name); err != nil {
		return err
	}
	return nil
}

func sameNativeNullableHash(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return asStringOr(left) == asStringOr(right)
}

func nativeCouncilArtifactDeclared(observation ExecuteObservation, relative string) bool {
	for _, artifact := range observation.Artifacts {
		if artifact.Path == relative {
			return true
		}
	}
	return false
}

func formatNativeSequence(value int) string {
	return fmt.Sprintf("%03d", value)
}
