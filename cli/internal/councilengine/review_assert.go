package councilengine

import (
	"fmt"
	"regexp"
	"strings"
)

// review_assert.go ports Assert-BSLFlowCouncilReview
// (Council.Validation.ps1:277-484): the full structural contract of the
// sanitized v2 review artifact — closed field sets, enums, hash-shaped
// inputs, member envelope surface, canonical finding order, protected/
// question arrays, chair decisions/refs and the inline reconciliation/gate.
// Every controller-built provenance field is validated here before
// publication.

var costStates = map[string]bool{"unknown": true, "provider_usage_reported": true, "no_usage_reported": true}
var memberStatusSet = map[string]bool{
	"completed": true, "failed_before_acceptance": true, "unknown_after_dispatch": true,
	"cancelled": true, "invalid_response": true,
}
var executionModes = map[string]bool{"direct_api": true, "current_agent_fallback": true}
var verdicts = map[string]bool{"PASS": true, "REVISE": true, "BLOCK": true, "needs_input": true}
var diversities = map[string]bool{"multi_model": true, "multi_role_single_model": true, "degraded": true, "unknown": true}

// AssertCouncilReview validates the assembled review v2 object. It mirrors
// the PowerShell assertion; a failing check is a BF_INVALID error with the
// exact diagnostic.
func AssertCouncilReview(review any) error {
	root, err := assertObjectProps(review, "review", []string{
		"schema_version", "reviewed_at_utc", "council_schema_version", "verdict",
		"diversity", "fallback_visible", "inputs", "manifest", "members",
		"findings", "protected", "questions", "chair", "reconciliation", "gate",
	})
	if err != nil {
		return err
	}
	if version, ok := asIntegerJSON(root.get("schema_version")); !ok || version != 2 {
		return invalid("review.schema_version must be 2.")
	}
	if council, ok := asIntegerJSON(root.get("council_schema_version")); !ok || council != 1 {
		return invalid("review.council_schema_version must be 1.")
	}
	if !verdicts[asStringOr(root.get("verdict"))] {
		return invalid("Invalid council verdict.")
	}
	if !diversities[asStringOr(root.get("diversity"))] {
		return invalid("Invalid diversity status.")
	}
	if _, ok := asBoolJSON(root.get("fallback_visible")); !ok {
		return invalid("review.fallback_visible must be boolean.")
	}
	inputs, err := assertObjectProps(root.get("inputs"), "inputs", []string{"original_task_sha256", "spec_sha256", "design_sha256", "policy_hash"})
	if err != nil {
		return err
	}
	for _, name := range []string{"original_task_sha256", "spec_sha256", "policy_hash"} {
		if !sha256Pattern.MatchString(asStringOr(inputs.get(name))) {
			return invalid("Invalid inputs hash: %s", name)
		}
	}
	if design := inputs.get("design_sha256"); design != nil && !sha256Pattern.MatchString(asStringOr(design)) {
		return invalid("Invalid inputs.design_sha256.")
	}
	if err := assertRequirementManifest(root.get("manifest")); err != nil {
		return err
	}
	members, err := assertArray(root.get("members"), "members")
	if err != nil {
		return err
	}
	if len(members) == 0 {
		return invalid("Council review must contain at least one member.")
	}
	seenRoles := map[string]bool{}
	hasChair := false
	for _, raw := range members {
		member, err := assertObjectProps(raw, "member", []string{
			"schema_version", "role", "attempt_id", "status", "summary", "requested", "observed",
			"execution_mode", "fallback_reason", "input_hashes", "payload_sha256", "usage",
			"cost_state", "dispatched_at_utc", "completed_at_utc",
		})
		if err != nil {
			return err
		}
		role := asStringOr(member.get("role"))
		if !containsString([]string{"brainstorm", "intent_critic", "architecture_critic", "executability_critic", "chair"}, role) {
			return invalid("Unknown member role: %s", role)
		}
		if seenRoles[role] {
			return invalid("Member roles must be unique.")
		}
		seenRoles[role] = true
		if role == councilRoleChair {
			hasChair = true
		}
		if version, ok := asIntegerJSON(member.get("schema_version")); !ok || version != 1 {
			return invalid("Member envelope schema_version must be 1: %s", role)
		}
		if strings.TrimSpace(asStringOr(member.get("attempt_id"))) == "" {
			return invalid("Member attempt_id is required: %s", role)
		}
		if err := assertText(member.get("summary"), "member.summary ("+role+")"); err != nil {
			return err
		}
		status := asStringOr(member.get("status"))
		if !memberStatusSet[status] {
			return invalid("Invalid member status: %s", role)
		}
		requested, err := assertObjectProps(member.get("requested"), "member.requested", []string{"provider", "model", "effort"})
		if err != nil {
			return err
		}
		observed, err := assertObjectProps(member.get("observed"), "member.observed", []string{"provider", "model", "effort"})
		if err != nil {
			return err
		}
		for _, field := range []string{"provider", "model", "effort"} {
			if strings.TrimSpace(asStringOr(requested.get(field))) == "" {
				return invalid("Member requested.%s is required: %s", field, role)
			}
		}
		if !executionModes[asStringOr(member.get("execution_mode"))] {
			return invalid("Invalid execution_mode: %s", role)
		}
		if usage := member.get("usage"); usage != nil {
			if _, err := assertObjectProps(usage, "member.usage", []string{"input_tokens", "output_tokens", "reasoning_tokens"}); err != nil {
				return err
			}
		}
		if costState := member.get("cost_state"); costState != nil && !costStates[asStringOr(costState)] {
			return invalid("Invalid member cost_state: %s", role)
		}
		if status == "completed" && member.get("cost_state") == nil {
			return invalid("Completed member needs cost_state: %s", role)
		}
		if err := assertRFC3339(member.get("dispatched_at_utc"), "member.dispatched_at_utc ("+role+")"); err != nil {
			return err
		}
		if err := assertRFC3339(member.get("completed_at_utc"), "member.completed_at_utc ("+role+")"); err != nil {
			return err
		}
		allowedHashFields := []string{"original_task_sha256", "spec_sha256", "design_sha256", "evidence_sha256", "policy_hash", "rubric_sha256"}
		if role == councilRoleChair {
			allowedHashFields = append(allowedHashFields, "member_aggregate_sha256")
		}
		hashes, err := assertObjectProps(member.get("input_hashes"), "member.input_hashes", allowedHashFields)
		if err != nil {
			return err
		}
		if role == councilRoleChair && !hashes.has("member_aggregate_sha256") {
			return invalid("Chair member envelope needs member_aggregate_sha256.")
		}
		if !sha256Pattern.MatchString(asStringOr(hashes.get("original_task_sha256"))) {
			return invalid("Invalid member input hash: %s", role)
		}
		for _, field := range []string{"spec_sha256", "design_sha256", "evidence_sha256", "policy_hash", "rubric_sha256", "member_aggregate_sha256"} {
			if value := hashes.get(field); value != nil && !sha256Pattern.MatchString(asStringOr(value)) {
				return invalid("Invalid member %s : %s", field, role)
			}
		}
		if payloadHash := member.get("payload_sha256"); payloadHash != nil && !sha256Pattern.MatchString(asStringOr(payloadHash)) {
			return invalid("Invalid member payload hash: %s", role)
		}
		if status == "completed" && member.get("payload_sha256") == nil {
			return invalid("Completed member needs payload_sha256: %s", role)
		}
		// Frozen-input bindings: a member must have voted on this exact snapshot.
		if asStringOr(hashes.get("original_task_sha256")) != asStringOr(inputs.get("original_task_sha256")) {
			return invalid("Member voted on another original-task snapshot: %s", role)
		}
		if inputs.get("spec_sha256") != nil && asStringOr(hashes.get("spec_sha256")) != asStringOr(inputs.get("spec_sha256")) {
			return invalid("Member voted on another spec draft: %s", role)
		}
		liveDesign := hashes.get("design_sha256")
		if inputs.get("design_sha256") != nil || liveDesign != nil {
			if asStringOr(liveDesign) != asStringOr(inputs.get("design_sha256")) {
				return invalid("Member voted on another design draft: %s", role)
			}
		}
		if asStringOr(hashes.get("policy_hash")) != asStringOr(inputs.get("policy_hash")) {
			return invalid("Member voted under another council policy: %s", role)
		}
		if role != councilRoleChair {
			_ = observed
		}
	}
	if !hasChair {
		return invalid("Council review must contain the chair member record.")
	}
	// Canonical findings order.
	findings, err := assertArray(root.get("findings"), "findings")
	if err != nil {
		return err
	}
	canonical, err := GetCanonicalFindings(findings)
	if err != nil {
		return err
	}
	actualIDs := make([]string, 0, len(findings))
	canonicalIDs := make([]string, 0, len(canonical))
	for _, raw := range findings {
		finding, _ := asOrdered(raw)
		actualIDs = append(actualIDs, asStringOr(finding.get("composite_id")))
	}
	for _, raw := range canonical {
		finding, _ := asOrdered(raw)
		canonicalIDs = append(canonicalIDs, asStringOr(finding.get("composite_id")))
	}
	if strings.Join(actualIDs, "|") != strings.Join(canonicalIDs, "|") {
		return invalid("Review findings must be stored in canonical role/id order.")
	}
	for _, raw := range findings {
		finding, err := assertObjectProps(raw, "finding", []string{"composite_id", "role", "id", "severity", "category", "spec_ref", "issue", "evidence", "suggested_direction"})
		if err != nil {
			return err
		}
		if !regexp.MustCompile(`^F-[0-9]{3,}$`).MatchString(asStringOr(finding.get("id"))) {
			return invalid("Invalid aggregate finding id: %s", asStringOr(finding.get("composite_id")))
		}
	}
	// Protected items.
	protected, err := assertArray(root.get("protected"), "protected")
	if err != nil {
		return err
	}
	protectedIDs := map[string]bool{}
	for _, raw := range protected {
		item, err := assertObjectProps(raw, "protected", []string{"composite_id", "role", "item"})
		if err != nil {
			return err
		}
		composite := asStringOr(item.get("composite_id"))
		if protectedIDs[composite] {
			return invalid("Protected composite IDs must be unique.")
		}
		protectedIDs[composite] = true
		if err := assertText(item.get("item"), "protected."+composite); err != nil {
			return err
		}
	}
	// Questions.
	questions, err := assertArray(root.get("questions"), "questions")
	if err != nil {
		return err
	}
	for _, raw := range questions {
		question, err := assertObjectProps(raw, "question", []string{"role", "text"})
		if err != nil {
			return err
		}
		role := asStringOr(question.get("role"))
		if _, known := findingRoleOrder[role]; !known {
			return invalid("Unknown question role: %s", role)
		}
		if err := assertText(question.get("text"), "question."+role); err != nil {
			return err
		}
	}
	// Chair surface.
	chair, err := assertObjectProps(root.get("chair"), "chair", []string{"verdict", "decisions", "protected_decisions", "requirement_refs", "final_spec_text", "final_design_text"})
	if err != nil {
		return err
	}
	if !verdicts[asStringOr(chair.get("verdict"))] {
		return invalid("Invalid chair verdict.")
	}
	if err := assertText(chair.get("final_spec_text"), "chair.final_spec_text"); err != nil {
		return err
	}
	if chair.has("final_design_text") && chair.get("final_design_text") != nil {
		if err := assertText(chair.get("final_design_text"), "chair.final_design_text"); err != nil {
			return err
		}
	}
	decisions, err := assertArray(chair.get("decisions"), "chair.decisions")
	if err != nil {
		return err
	}
	protectedDecisions, err := assertArray(chair.get("protected_decisions"), "chair.protected_decisions")
	if err != nil {
		return err
	}
	requirementRefs, err := assertArray(chair.get("requirement_refs"), "chair.requirement_refs")
	if err != nil {
		return err
	}
	decisionIDs := map[string]bool{}
	for _, raw := range decisions {
		decision, err := assertObjectProps(raw, "chair decision", nil)
		if err != nil {
			return err
		}
		composite := asStringOr(decision.get("composite_id"))
		if decisionIDs[composite] {
			return invalid("Chair decisions must be unique.")
		}
		decisionIDs[composite] = true
	}
	for _, id := range actualIDs {
		if !decisionIDs[id] {
			return invalid("Chair must decide every finding exactly once: %s", id)
		}
	}
	for id := range decisionIDs {
		if !containsString(actualIDs, id) {
			return invalid("Unknown chair decision: %s", id)
		}
	}
	for _, raw := range decisions {
		decision, err := validateChairDecision(raw)
		if err != nil {
			return err
		}
		_ = decision
	}
	protectedDecisionIDs := map[string]bool{}
	for _, raw := range protectedDecisions {
		check, err := assertObjectProps(raw, "protected decision", []string{"composite_id", "decision", "reason", "evidence"})
		if err != nil {
			return err
		}
		composite := asStringOr(check.get("composite_id"))
		if protectedDecisionIDs[composite] {
			return invalid("Protected decisions must be unique.")
		}
		protectedDecisionIDs[composite] = true
		if !containsString([]string{"preserved", "rejected"}, asStringOr(check.get("decision"))) {
			return invalid("Invalid protected decision: %s", composite)
		}
		if err := assertText(check.get("reason"), "protected."+composite+".reason"); err != nil {
			return err
		}
		if err := assertText(check.get("evidence"), "protected."+composite+".evidence"); err != nil {
			return err
		}
	}
	for id := range protectedIDs {
		if !protectedDecisionIDs[id] {
			return invalid("Chair must decide every protected item exactly once: %s", id)
		}
	}
	for id := range protectedDecisionIDs {
		if !protectedIDs[id] {
			return invalid("Unknown protected decision: %s", id)
		}
	}
	refIDs := map[string]bool{}
	for _, raw := range requirementRefs {
		ref, err := assertObjectProps(raw, "requirement ref", []string{"id", "final_refs"})
		if err != nil {
			return err
		}
		id := asStringOr(ref.get("id"))
		if refIDs[id] {
			return invalid("Requirement refs must be unique.")
		}
		refIDs[id] = true
		finalRefs, err := assertArray(ref.get("final_refs"), "requirement."+id+".final_refs")
		if err != nil {
			return err
		}
		if len(finalRefs) == 0 {
			return invalid("Requirement needs at least one final ref: %s", id)
		}
		for _, final := range finalRefs {
			if err := assertText(final, "requirement."+id+".final_refs[]"); err != nil {
				return err
			}
		}
	}
	manifest, ok := asOrdered(root.get("manifest"))
	if !ok {
		return invalid("review.manifest must be an object.")
	}
	manifestRequirements, _ := asArray(manifest.get("requirements"))
	manifestIDs := map[string]bool{}
	for _, raw := range manifestRequirements {
		requirement, _ := asOrdered(raw)
		manifestIDs[asStringOr(requirement.get("id"))] = true
	}
	for id := range manifestIDs {
		if !refIDs[id] {
			return invalid("Chair must return final refs for every requirement: %s", id)
		}
	}
	for id := range refIDs {
		if !manifestIDs[id] {
			return invalid("Unknown requirement ref: %s", id)
		}
	}
	// Inline reconciliation + gate.
	reconciliation, err := assertObjectProps(root.get("reconciliation"), "reconciliation", []string{"review_sha256", "draft_spec_sha256", "final_spec_sha256", "draft_design_sha256", "final_design_sha256"})
	if err != nil {
		return err
	}
	if !sha256Pattern.MatchString(asStringOr(reconciliation.get("review_sha256"))) {
		return invalid("Invalid reconciliation.review_sha256.")
	}
	for _, field := range []string{"draft_spec_sha256", "final_spec_sha256"} {
		if !sha256Pattern.MatchString(asStringOr(reconciliation.get(field))) {
			return invalid("Invalid reconciliation.%s.", field)
		}
	}
	for _, field := range []string{"draft_design_sha256", "final_design_sha256"} {
		if value := reconciliation.get(field); value != nil && !sha256Pattern.MatchString(asStringOr(value)) {
			return invalid("Invalid reconciliation.%s.", field)
		}
	}
	gate, err := assertObjectProps(root.get("gate"), "gate", []string{"structural_only", "passed"})
	if err != nil {
		return err
	}
	if asBoolJSONValue(gate.get("structural_only")) != true {
		return invalid("Council gate is structural-only.")
	}
	if _, ok := asBoolJSON(gate.get("passed")); !ok {
		return invalid("gate.passed must be boolean.")
	}
	// Final references must point into the actual final specification text.
	finalSpec := asStringOr(chair.get("final_spec_text"))
	finalManifest, err := NewRequirementManifest(finalSpec)
	if err != nil {
		return invalid("chair.final_spec_text does not contain a valid Требуемое поведение requirement section.")
	}
	finalRequirements, _ := asArray(finalManifest.get("requirements"))
	if len(finalRequirements) < len(manifestRequirements) {
		return invalid("chair.final_spec_text covers fewer material requirements than the reviewed draft manifest.")
	}
	for _, raw := range requirementRefs {
		ref, _ := asOrdered(raw)
		id := asStringOr(ref.get("id"))
		finalRefs, _ := asArray(ref.get("final_refs"))
		for _, final := range finalRefs {
			if !ReferenceResolves(asStringOr(final), finalSpec, len(finalRequirements)) {
				return invalid("Requirement final ref does not resolve in the final specification: %s -> %s", id, asStringOr(final))
			}
		}
	}
	for _, raw := range decisions {
		decision, _ := asOrdered(raw)
		if asStringOr(decision.get("decision")) == "rejected" {
			continue
		}
		resolutionRefs, _ := asArray(decision.get("resolution_refs"))
		for _, ref := range resolutionRefs {
			if !ReferenceResolves(asStringOr(ref), finalSpec, len(finalRequirements)) {
				return invalid("Chair resolution ref does not resolve in the final specification: %s -> %s", asStringOr(decision.get("composite_id")), asStringOr(ref))
			}
		}
	}
	// Static secret guard for the metadata shape.
	metadata := orderedFrom(
		[]string{"members", "reconciliation", "gate", "inputs", "manifest"},
		[]any{members, reconciliation, gate, inputs, manifest},
	)
	serialized, err := convertToJSON(metadata, 20)
	if err != nil {
		return err
	}
	text := string(serialized)
	if regexp.MustCompile(`(?i)"token"\s*:`).MatchString(text) {
		return invalid("Sanitized council review must not contain a token field.")
	}
	if regexp.MustCompile(`(?i)Authorization`).MatchString(text) {
		return invalid("Sanitized council review must not contain Authorization material.")
	}
	if asStringOr(root.get("verdict")) == "PASS" && asStringOr(chair.get("verdict")) != "PASS" {
		return invalid("Review PASS cannot exceed the chair verdict.")
	}
	return nil
}

func asBoolJSONValue(value any) bool {
	typed, ok := value.(bool)
	return ok && typed
}

func validateChairDecision(raw any) (*ordered, error) {
	decision, _ := asOrdered(raw)
	allowed := []string{"composite_id", "decision", "reason", "evidence", "resolution", "resolution_refs"}
	if asStringOr(decision.get("decision")) == "partially_accepted" {
		allowed = append(allowed, "accepted_scope", "rejected_scope")
	}
	if decision.has("accepted_scope") {
		allowed = append(allowed, "accepted_scope")
	}
	decision, err := assertObjectProps(raw, "chair decision", allowed)
	if err != nil {
		return nil, err
	}
	kind := asStringOr(decision.get("decision"))
	if !containsString([]string{"accepted", "rejected", "partially_accepted"}, kind) {
		return nil, invalid("Invalid chair decision: %s", asStringOr(decision.get("composite_id")))
	}
	for _, field := range []string{"reason", "evidence", "resolution"} {
		if err := assertText(decision.get(field), "chair."+asStringOr(decision.get("composite_id"))+"."+field); err != nil {
			return nil, err
		}
	}
	if kind == "partially_accepted" {
		if err := assertText(decision.get("accepted_scope"), "chair."+asStringOr(decision.get("composite_id"))+".accepted_scope"); err != nil {
			return nil, err
		}
		if err := assertText(decision.get("rejected_scope"), "chair."+asStringOr(decision.get("composite_id"))+".rejected_scope"); err != nil {
			return nil, err
		}
		resolutionRefs, _ := asArray(decision.get("resolution_refs"))
		if len(resolutionRefs) == 0 {
			return nil, invalid("Partially accepted finding needs resolution_refs: %s", asStringOr(decision.get("composite_id")))
		}
	}
	if kind == "accepted" {
		resolutionRefs, _ := asArray(decision.get("resolution_refs"))
		if len(resolutionRefs) == 0 {
			return nil, invalid("Accepted finding needs resolution_refs: %s", asStringOr(decision.get("composite_id")))
		}
	}
	for _, ref := range anyItems(decision.get("resolution_refs")) {
		if err := assertText(ref, "chair."+asStringOr(decision.get("composite_id"))+".resolution_refs[]"); err != nil {
			return nil, err
		}
	}
	return decision, nil
}

func anyItems(value any) []any {
	items, _ := asArray(value)
	return items
}

func assertRequirementManifest(value any) error {
	manifest, err := assertObjectProps(value, "manifest", []string{"version", "requirements"})
	if err != nil {
		return err
	}
	if version, ok := asIntegerJSON(manifest.get("version")); !ok || version != 1 {
		return invalid("manifest.version must be 1.")
	}
	requirements, err := assertArray(manifest.get("requirements"), "manifest.requirements")
	if err != nil {
		return err
	}
	if len(requirements) == 0 {
		return invalid("manifest.requirements must not be empty.")
	}
	seen := map[string]bool{}
	for index, raw := range requirements {
		requirement, err := assertObjectProps(raw, "manifest requirement", []string{"id", "source_hash", "draft_ref"})
		if err != nil {
			return err
		}
		expected := fmt.Sprintf("REQ-%03d", index+1)
		id := asStringOr(requirement.get("id"))
		if id != expected {
			return invalid("Manifest requirement IDs must be sequential: expected %s.", expected)
		}
		if seen[id] {
			return invalid("Duplicate manifest requirement id: %s", id)
		}
		seen[id] = true
		if !sha256Pattern.MatchString(asStringOr(requirement.get("source_hash"))) {
			return invalid("Invalid source_hash for %s.", id)
		}
		if err := assertText(requirement.get("draft_ref"), "manifest."+id+".draft_ref"); err != nil {
			return err
		}
	}
	return nil
}
