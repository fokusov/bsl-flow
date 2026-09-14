package councilengine

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// validation.go ports Council.Validation.ps1: requirement manifest, member/
// chair/brainstorm payload schemas, canonical findings, diversity, the review
// v2 digest and the deterministic final gate's structural assertions. No
// network; every provenance field is controller-built, never model-claimed.

var forbiddenPayloadFields = []string{
	"type", "provider", "model", "effort", "status", "usage", "cost_state",
	"reviewed_at_utc", "reconciled_at_utc", "timestamps", "input_hashes",
	"payload_sha256", "execution_mode", "requested", "observed",
	"schema_version", "reviewer", "inputs", "gate", "confidence",
	"weighted_score", "scores", "overengineering", "token", "Authorization",
}

var allowedFindingCategories = map[string]bool{
	"intent_drift": true, "missing_requirement": true, "lost_requirement": true,
	"unsupported_assumption": true, "scope_creep": true, "overengineering": true,
	"architecture_fit": true, "testability": true, "clarity": true, "prompt_injection": true,
}

var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var rfc3339Pattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:[0-9]{2})$`)

// --- requirement manifest ---

var requiredBehaviorHeading = regexp.MustCompile(`(?im)^##\s+(Требуемое поведение|Required behavior)\s*$`)

// RequirementSection extracts the "## Требуемое поведение" / "## Required
// behavior" section body (Get-BSLFlowRequirementSection): everything from the
// heading line until the next "## " heading or the end of text.
func RequirementSection(specText string) (string, error) {
	lines := regexp.MustCompile(`\r?\n`).Split(specText, -1)
	start := -1
	for index, line := range lines {
		if requiredBehaviorHeading.MatchString(line) {
			start = index
			break
		}
	}
	if start < 0 {
		return "", invalid("Required behavior section not found.")
	}
	body := []string{}
	for index := start + 1; index < len(lines); index++ {
		if regexp.MustCompile(`^##\s`).MatchString(lines[index]) {
			break
		}
		body = append(body, lines[index])
	}
	return strings.Join(body, "\n"), nil
}

// NewRequirementManifest ports New-BSLFlowRequirementManifest: one manifest
// entry per numbered/bulleted item in the required-behavior section, with a
// SHA-256 of the trimmed item text.
func NewRequirementManifest(specText string) (*ordered, error) {
	body, err := RequirementSection(specText)
	if err != nil {
		return nil, err
	}
	items := []string{}
	for _, line := range regexp.MustCompile(`\r?\n`).Split(body, -1) {
		match := regexp.MustCompile(`^\s*(?:\d+\.\s+|[-*]\s+)(?P<item>\S.*\S|\S)\s*$`).FindStringSubmatch(line)
		if match == nil {
			continue
		}
		text := strings.TrimSpace(match[1])
		if text != "" && !strings.HasPrefix(text, "<!--") {
			items = append(items, text)
		}
	}
	if len(items) == 0 {
		return nil, invalid("Requirement manifest needs at least one numbered or bulleted behavior item.")
	}
	requirements := make([]any, 0, len(items))
	for index, item := range items {
		requirements = append(requirements, orderedFrom(
			[]string{"id", "source_hash", "draft_ref"},
			[]any{
				fmt.Sprintf("REQ-%03d", index+1),
				sha256Hex([]byte(item)),
				fmt.Sprintf("Требуемое поведение / %d", index+1),
			},
		))
	}
	return orderedFrom([]string{"version", "requirements"}, []any{json.Number("1"), requirements}), nil
}

// --- payload assertions ---

// noProvenanceClaims ports Assert-BSLFlowCouncilNoProvenanceClaims.
func noProvenanceClaims(payload any, name string) error {
	object, ok := asOrdered(payload)
	if !ok {
		return invalid("%s must be an object.", name)
	}
	for _, field := range forbiddenPayloadFields {
		if object.has(field) {
			return invalid("Model payload must not contain %s.%s; provenance is built by the controller.", name, field)
		}
	}
	return nil
}

func assertFinding(finding any, name string) error {
	object, err := assertObjectProps(finding, name, []string{"id", "severity", "category", "spec_ref", "issue", "evidence", "suggested_direction"})
	if err != nil {
		return err
	}
	id := asStringOr(object.get("id"))
	if !regexp.MustCompile(`^F-[0-9]{3,}$`).MatchString(id) {
		return invalid("Invalid finding id in %s: %s", name, id)
	}
	severity := asStringOr(object.get("severity"))
	if !containsString([]string{"blocker", "high", "medium", "low"}, severity) {
		return invalid("Invalid severity in %s: %s", name, id)
	}
	if !allowedFindingCategories[asStringOr(object.get("category"))] {
		return invalid("Invalid category in %s: %s", name, id)
	}
	for _, field := range []string{"spec_ref", "issue", "evidence", "suggested_direction"} {
		if err := assertText(object.get(field), name+"."+id+"."+field); err != nil {
			return err
		}
	}
	return nil
}

// AssertCouncilModelPayload ports Assert-BSLFlowCouncilModelPayload.
func AssertCouncilModelPayload(payload any) error {
	if err := noProvenanceClaims(payload, "member"); err != nil {
		return err
	}
	object, _ := asOrdered(payload)
	allowed := []string{"role", "verdict", "findings", "do_not_change"}
	if object.has("needs_input_questions") {
		allowed = append(allowed, "needs_input_questions")
	}
	object, err := assertObjectProps(payload, "member", allowed)
	if err != nil {
		return err
	}
	role := asStringOr(object.get("role"))
	if !containsString([]string{"intent_critic", "architecture_critic", "executability_critic"}, role) {
		return invalid("Invalid critic role: %s", role)
	}
	verdict := asStringOr(object.get("verdict"))
	if !containsString([]string{"PASS", "REVISE", "BLOCK", "needs_input"}, verdict) {
		return invalid("Invalid member verdict.")
	}
	findings, ok := asArray(object.get("findings"))
	if !ok {
		return invalid("member.findings must be a JSON array.")
	}
	doNotChange, ok := asArray(object.get("do_not_change"))
	if !ok {
		return invalid("member.do_not_change must be a JSON array.")
	}
	seen := map[string]bool{}
	for index, raw := range findings {
		if err := assertFinding(raw, "member.findings"); err != nil {
			return err
		}
		finding, _ := asOrdered(raw)
		expected := fmt.Sprintf("F-%03d", index+1)
		id := asStringOr(finding.get("id"))
		if id != expected {
			return invalid("Member finding IDs must be sequential per role: expected %s.", expected)
		}
		if seen[id] {
			return invalid("Duplicate member finding id: %s", id)
		}
		seen[id] = true
	}
	for _, raw := range doNotChange {
		if err := assertText(raw, "member.do_not_change[]"); err != nil {
			return err
		}
	}
	unique := map[string]bool{}
	for _, raw := range doNotChange {
		item := asStringOr(raw)
		if unique[item] {
			return invalid("member.do_not_change items must be unique.")
		}
		unique[item] = true
	}
	questions := []any{}
	if object.has("needs_input_questions") {
		if value, ok := asArray(object.get("needs_input_questions")); ok {
			questions = value
		}
	}
	if verdict == "needs_input" && len(questions) == 0 {
		return invalid("needs_input verdict requires at least one question.")
	}
	for _, question := range questions {
		if err := assertText(question, "member.needs_input_questions[]"); err != nil {
			return err
		}
	}
	if verdict != "PASS" && len(findings) == 0 && verdict != "needs_input" {
		return invalid("A non-PASS member payload must contain at least one finding.")
	}
	return nil
}

// AssertCouncilChairResult ports Assert-BSLFlowCouncilChairResult.
func AssertCouncilChairResult(payload any) error {
	if err := noProvenanceClaims(payload, "chair"); err != nil {
		return err
	}
	object, _ := asOrdered(payload)
	for _, field := range []string{"verdict", "decisions", "protected_decisions", "requirement_refs", "final_spec_text"} {
		if !object.has(field) {
			return invalid("chair payload misses field: %s.", field)
		}
	}
	if !containsString([]string{"PASS", "REVISE", "BLOCK", "needs_input"}, asStringOr(object.get("verdict"))) {
		return invalid("Invalid chair verdict.")
	}
	if err := assertText(object.get("final_spec_text"), "chair.final_spec_text"); err != nil {
		return err
	}
	if object.has("final_design_text") && object.get("final_design_text") != nil {
		if err := assertText(object.get("final_design_text"), "chair.final_design_text"); err != nil {
			return err
		}
	}
	for _, name := range []string{"decisions", "protected_decisions", "requirement_refs"} {
		if _, ok := asArray(object.get(name)); !ok {
			return invalid("chair.%s must be a JSON array.", name)
		}
	}
	return nil
}

// AssertBrainstormPayload ports Assert-BSLFlowBrainstormPayload.
func AssertBrainstormPayload(payload any) error {
	if err := noProvenanceClaims(payload, "brainstorm"); err != nil {
		return err
	}
	object, _ := asOrdered(payload)
	allowed := []string{"role"}
	for _, name := range []string{"alternatives", "risks", "unknowns", "questions"} {
		if object.has(name) {
			allowed = append(allowed, name)
		}
	}
	object, err := assertObjectProps(payload, "brainstorm", allowed)
	if err != nil {
		return err
	}
	if asStringOr(object.get("role")) != "brainstorm" {
		return invalid("Invalid brainstorm role.")
	}
	total := 0
	for _, name := range []string{"alternatives", "risks", "unknowns", "questions"} {
		value := object.get(name)
		if !object.has(name) {
			value = []any{}
		}
		items, ok := asArray(value)
		if !ok {
			return invalid("brainstorm.%s must be a JSON array.", name)
		}
		for _, item := range items {
			if err := assertText(item, "brainstorm."+name+"[]"); err != nil {
				return err
			}
		}
		total += len(items)
	}
	if total == 0 {
		return invalid("Brainstorm payload must contain at least one alternative, risk, unknown or question.")
	}
	return nil
}

// --- diversity ---

// GetCouncilDiversity ports Get-BSLFlowCouncilDiversity.
func GetCouncilDiversity(members []any) (*ordered, error) {
	fallbackVisible := false
	failed := 0
	completed := 0
	observedModels := map[string]bool{}
	hasUnknown := false
	for _, raw := range members {
		member, _ := asOrdered(raw)
		if asStringOr(member.get("execution_mode")) == "current_agent_fallback" {
			fallbackVisible = true
		}
		if reason := asStringOr(member.get("fallback_reason")); strings.TrimSpace(reason) != "" {
			fallbackVisible = true
		}
		if asStringOr(member.get("status")) != "completed" {
			failed++
			continue
		}
		completed++
		observed, _ := asOrdered(member.get("observed"))
		model := asStringOr(observed.get("model"))
		if strings.TrimSpace(model) == "" {
			hasUnknown = true
		} else {
			observedModels[model] = true
		}
	}
	diversity := "multi_role_single_model"
	if failed > 0 {
		diversity = "degraded"
	} else if hasUnknown || completed == 0 {
		diversity = "unknown"
	} else if len(observedModels) >= 2 {
		diversity = "multi_model"
	}
	return orderedFrom([]string{"diversity", "fallback_visible"}, []any{diversity, fallbackVisible}), nil
}

// --- canonical findings ---

var findingRoleOrder = map[string]int{
	councilRoleBrainstorm: 0, councilRoleIntentCritic: 1, councilRoleArchitecture: 2, councilRoleExecutability: 3,
}

// GetCanonicalFindings ports Get-BSLFlowCanonicalFindings.
func GetCanonicalFindings(findings []any) ([]any, error) {
	seen := map[string]bool{}
	validated := make([]any, 0, len(findings))
	for _, raw := range findings {
		finding, ok := asOrdered(raw)
		if !ok {
			return nil, invalid("Finding must be an object.")
		}
		role := asStringOr(finding.get("role"))
		if _, known := findingRoleOrder[role]; !known {
			return nil, invalid("Unknown finding role: %s", role)
		}
		id := asStringOr(finding.get("id"))
		composite := asStringOr(finding.get("composite_id"))
		expected := role + ":" + id
		if composite != expected {
			return nil, invalid("Finding composite_id must equal role:id: %s.", expected)
		}
		if seen[composite] {
			return nil, invalid("Duplicate composite finding: %s", composite)
		}
		seen[composite] = true
		validated = append(validated, finding)
	}
	sort.SliceStable(validated, func(i, j int) bool {
		left, _ := asOrdered(validated[i])
		right, _ := asOrdered(validated[j])
		if findingRoleOrder[asStringOr(left.get("role"))] != findingRoleOrder[asStringOr(right.get("role"))] {
			return findingRoleOrder[asStringOr(left.get("role"))] < findingRoleOrder[asStringOr(right.get("role"))]
		}
		if asStringOr(left.get("id")) != asStringOr(right.get("id")) {
			return asStringOr(left.get("id")) < asStringOr(right.get("id"))
		}
		return asStringOr(left.get("composite_id")) < asStringOr(right.get("composite_id"))
	})
	return validated, nil
}

// --- review digest ---

// CouncilReviewDigest ports Get-BSLFlowCouncilReviewDigest: the digest covers
// the canonical review bytes with reconciliation.review_sha256 zeroed, so the
// builder and the gate compute the identical value without regress.
func CouncilReviewDigest(review any) (string, error) {
	data, err := convertToJSON(review, 20)
	if err != nil {
		return "", invalid("%v", err)
	}
	clone, err := decodeOrderedDocument(data)
	if err != nil {
		return "", invalid("%v", err)
	}
	root, ok := asOrdered(clone)
	if !ok {
		return "", invalid("council review must be an object.")
	}
	reconciliation, ok := asOrdered(root.get("reconciliation"))
	if !ok {
		return "", invalid("council review must carry an inline reconciliation.")
	}
	reconciliation.set("review_sha256", strings.Repeat("0", 64))
	serialized, err := convertToJSON(clone, 20)
	if err != nil {
		return "", invalid("%v", err)
	}
	return sha256Hex(serialized), nil
}

// --- reference resolution ---

func normalizedReference(text string) string {
	if text == "" {
		return ""
	}
	return strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(text, " "))
}

// ReferenceResolves ports Test-BSLFlowCouncilReferenceResolves: a reference
// resolves against the final text as a numbered requirement anchor or a
// verbatim (whitespace-normalized) fragment.
func ReferenceResolves(ref, finalSpecText string, finalRequirementCount int) bool {
	if strings.TrimSpace(ref) == "" {
		return false
	}
	numberMatch := regexp.MustCompile(`^\s*(?:Требуемое поведение|Required behavior)\s*/\s*([0-9]{1,4})(?:\.([0-9]{1,4}))?\s*$`).FindStringSubmatch(ref)
	if numberMatch != nil {
		number := atoiOrZero(numberMatch[1])
		if number < 1 || number > finalRequirementCount {
			return false
		}
		if numberMatch[2] == "" {
			return true
		}
		section, err := RequirementSection(finalSpecText)
		if err != nil {
			return false
		}
		subMarker := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(numberMatch[1]+"."+numberMatch[2]) + `[\s.]`)
		return subMarker.MatchString(section)
	}
	final := normalizedReference(finalSpecText)
	if final == "" {
		return false
	}
	needle := normalizedReference(ref)
	if needle == "" {
		return false
	}
	return strings.Contains(final, needle)
}

func atoiOrZero(text string) int {
	value := 0
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0
		}
		value = value*10 + int(r-'0')
	}
	return value
}

// --- shared assertion helpers ---

func assertObjectProps(value any, name string, required []string) (*ordered, error) {
	object, ok := asOrdered(value)
	if !ok {
		return nil, invalid("%s must be an object.", name)
	}
	for _, key := range object.keysOf() {
		if !containsString(required, key) {
			return nil, invalid("Unknown %s property: %s", name, key)
		}
	}
	for _, key := range required {
		if !object.has(key) {
			return nil, invalid("Missing %s property: %s", name, key)
		}
	}
	return object, nil
}

func assertText(value any, name string) error {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" || len([]rune(text)) > 262144 {
		return invalid("invalid %s.", name)
	}
	return nil
}

func assertArray(value any, name string) ([]any, error) {
	items, ok := asArray(value)
	if !ok {
		return nil, invalid("%s must be a JSON array.", name)
	}
	return items, nil
}

func assertRFC3339(value any, name string) error {
	if value == nil {
		return nil
	}
	text, ok := value.(string)
	if !ok || !rfc3339Pattern.MatchString(text) {
		return invalid("%s must be an RFC 3339 date-time string with an offset.", name)
	}
	return nil
}

// asStringOr returns the string value of a JSON string or "".
func asStringOr(value any) string {
	text, _ := value.(string)
	return text
}

// asArray returns the array value or nil.
func asArray(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	default:
		return nil, false
	}
}

// asIntegerJSON returns the int64 value of a JSON integer literal.
func asIntegerJSON(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	default:
		return 0, false
	}
}

// asBoolJSON returns the bool value.
func asBoolJSON(value any) (bool, bool) {
	typed, ok := value.(bool)
	return typed, ok
}
