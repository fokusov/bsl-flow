package councilengine

import (
	"strings"
)

// prompts.go ports Invoke-CouncilReview.ps1: the role rubrics, the sealed
// council prompt builder (New-BSLFlowCouncilPrompt) and the role contract
// strings. Trusted policy markers separate the frozen role contract from
// untrusted draft content.

// roleRubrics mirrors $script:BSLFlowCouncilRoleRubrics.
var roleRubrics = map[string]string{
	councilRoleBrainstorm:    "Generate alternatives, risks, unknown preconditions and questions from the original task and evidence only. You never see the draft specification. Return no verdict and no specification text.",
	councilRoleIntentCritic:  "Look for lost requirements, intent drift, unsupported assumptions and scope creep. Cite the original task as evidence for every finding. Allowed finding categories: lost_requirement, intent_drift, unsupported_assumption, scope_creep, missing_requirement, overengineering, clarity.",
	councilRoleArchitecture:  "Check fit with existing mechanisms, minimality, data, transactions/locks, integration and security boundaries. Cite draft design/spec lines as evidence for every finding. Allowed finding categories (choose exactly one per finding, no synonyms): architecture_fit, overengineering, missing_requirement, unsupported_assumption, clarity.",
	councilRoleExecutability: "Look for implementation ambiguity, missing decisions, untestable acceptance criteria and incomplete requirement-to-observation traceability. Every finding must name the affected requirement or criterion. Allowed finding categories: testability, clarity, missing_requirement, unsupported_assumption, architecture_fit.",
	councilRoleChair:         "You are the only model reconciler. Weigh every finding and protected item exactly once against the original task and trusted evidence. Produce the minimally revised complete final specification text.",
}

// roleRubric returns the frozen rubric text for a role.
func roleRubric(role string) string {
	if rubric, ok := roleRubrics[role]; ok && rubric != "" {
		return rubric
	}
	return "Follow the role contract exactly."
}

// roleContract mirrors the New-BSLFlowCouncilPrompt contract strings.
func roleContract(role string) string {
	switch role {
	case councilRoleBrainstorm:
		return "Return a JSON object with role, alternatives[], risks[], unknowns[], questions[]. No verdict, no specification text."
	case councilRoleChair:
		return "Return ONLY a JSON object with exactly these top-level fields: verdict (MUST be exactly one of the strings PASS, REVISE, BLOCK, needs_input), decisions[] (composite_id, decision accepted|rejected|partially_accepted, reason, evidence, resolution, accepted_scope/rejected_scope for partial, resolution_refs for accepted/partial), protected_decisions[] (composite_id, decision preserved|rejected, reason, evidence), requirement_refs[] (id, final_refs[]), final_spec_text (the complete minimally revised specification markdown), final_design_text (optional, only when the design needs changes). No other top-level fields such as type or summary. resolution_refs and final_refs must be verbatim fragments or \"Требуемое поведение / N\" (or N.M subsection) anchors of final_spec_text. The final specification must never contain the literal placeholder strings TODO, TBD, FIXME, XXX, PLACEHOLDER or {{...}} anywhere, including inside rule descriptions — describe the rule without spelling the marker. Cover every finding, protected item and requirement exactly once. Answer every member question or return needs_input with that question."
	default:
		return "Return ONLY a JSON object with exactly these top-level fields: role, verdict (MUST be exactly one of the strings PASS, REVISE, BLOCK, needs_input), findings[] (each with id F-NNN sequential, severity exactly blocker|high|medium|low, category EXACTLY one of the allowed categories listed in the rubric above with no other wording, spec_ref, issue, evidence, suggested_direction), do_not_change[] (strings), needs_input_questions[] (only when verdict is needs_input). Do not invent new categories. No other top-level fields (for example no type, schema_version or summary). Never include provider, model, status, usage, timestamps, hashes or execution mode."
	}
}

// roleViewValue is the input view for a role prompt (New-BSLFlowCouncilPrompt).
type roleViewValue struct {
	role         string
	originalTask string
	spec         *string
	design       *string
	evidence     string
	aggregates   string
}

// buildPrompt ports New-BSLFlowCouncilPrompt byte for byte: the sealed role
// contract, the untrusted data blocks and (for the chair) the trusted
// member aggregate.
func buildPrompt(role string, view roleViewValue) string {
	rubric := roleRubric(role)
	contract := roleContract(role)
	lines := []string{
		"<<<BEGIN TRUSTED REVIEW POLICY: ROLE CONTRACT>>>",
		"Role: " + role,
		"Rubric: " + rubric,
		contract,
		"<<<END TRUSTED REVIEW POLICY: ROLE CONTRACT>>>",
		"<<<BEGIN UNTRUSTED DATA: original-task.md>>>",
		view.originalTask,
		"<<<END UNTRUSTED DATA: original-task.md>>>",
	}
	if view.spec != nil {
		lines = append(lines,
			"<<<BEGIN UNTRUSTED DATA: spec.md>>>",
			*view.spec,
			"<<<END UNTRUSTED DATA: spec.md>>>",
		)
	}
	if view.design != nil {
		lines = append(lines,
			"<<<BEGIN UNTRUSTED DATA: design.md>>>",
			*view.design,
			"<<<END UNTRUSTED DATA: design.md>>>",
		)
	}
	if strings.TrimSpace(view.evidence) != "" {
		lines = append(lines,
			"<<<BEGIN UNTRUSTED DATA: evidence>>>",
			view.evidence,
			"<<<END UNTRUSTED DATA: evidence>>>",
		)
	}
	if view.aggregates != "" {
		lines = append(lines,
			"<<<BEGIN TRUSTED AGGREGATES: member results>>>",
			view.aggregates,
			"<<<END TRUSTED AGGREGATES: member results>>>",
		)
	}
	return strings.Join(lines, "\n")
}
