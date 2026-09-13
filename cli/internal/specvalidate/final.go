package specvalidate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// FinalCheck is one named final-validation invariant.  Name is a stable rule
// handle (a bracketed suffix binds the check to the artifact item it is
// about), Pass mirrors "no error was appended" in Test-1CSpecFinal.ps1 and
// Detail carries the exact PowerShell message on failure (empty on pass).
type FinalCheck struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
}

// ValidateFinal ports the deterministic, file-content-only rules of
// Test-1CSpecFinal.ps1 for one OpenSpec change directory.  read resolves a
// path relative to changeDir and must refuse anything escaping it (path
// safety stays with the caller, which is also why changeDir only feeds the
// PowerShell-shaped messages).  Checks are appended in PowerShell statement
// order; the overall final validation passed iff every returned check
// passes, mirroring result.passed over the accumulated error list.
//
// Deliberately not ported, and reported by the migration slice instead of
// being approximated:
//
//   - the v2 council review digest (Test-1CSpecFinal.ps1:52-57 via
//     Get-BSLFlowCouncilReviewDigest, Council.Validation.ps1:266-275,
//     verified at Council.Validation.ps1:500-504): the digest is bound to
//     the byte shape of PowerShell's ConvertTo-Json re-serialization;
//   - the council policy hash (Council.Validation.ps1:515-522): it reads
//     bsl-flow.yaml outside the change directory, beyond the read scope;
//   - the derived-value recompute of Complete-BSLFlowReview
//     (Test-1CSpecFinal.ps1:135-150): weighted scores, overengineering
//     ratios and the gate verdict are re-derived through .NET banker's
//     rounding, which has no bit-exact stdlib equivalent.
func ValidateFinal(changeDir string, read func(rel string) ([]byte, error)) ([]FinalCheck, error) {
	if read == nil {
		return nil, errors.New("specvalidate: final validation requires a read function")
	}
	validator := &finalValidator{changeDir: changeDir, read: read}
	validator.run()
	return validator.checks, nil
}

type finalValidator struct {
	changeDir string
	read      func(rel string) ([]byte, error)
	checks    []FinalCheck
}

func (v *finalValidator) run() {
	// Test-1CSpecFinal.ps1:32-34.  design.md is optional (line 106) and
	// verification.md is not a final-validation input at all.
	reviewPresent := v.requireFile("review.json", "input-review.json")
	specPresent := v.requireFile("spec.md", "input-spec.md")
	originalPresent := v.requireFile("original-task.md", "input-original-task.md")
	var reviewBytes []byte
	if reviewPresent {
		reviewBytes, _ = v.file("review.json")
	}
	// Dual reader peek (Test-1CSpecFinal.ps1:38-46): the schema is only
	// inspected once the shared required inputs exist, mirroring the
	// PowerShell guard on errors.Count.
	if reviewPresent && specPresent && originalPresent {
		review, err := decodeJSONObject(reviewBytes)
		if err == nil && finalSchemaPeek(review["schema_version"]) == 2 {
			specBytes, _ := v.file("spec.md")
			originalBytes, _ := v.file("original-task.md")
			v.runCouncil(review, specBytes, originalBytes)
			return
		}
	}
	v.runLegacy(reviewBytes, reviewPresent, specPresent, originalPresent)
}

// runCouncil ports the schema v2 branch of Test-1CSpecFinal.ps1:46-85 plus
// the deterministic parts of Test-BSLFlowCouncilFinalGate
// (Council.Validation.ps1:486-548) that depend only on change-dir content.
func (v *finalValidator) runCouncil(review map[string]any, specBytes, originalBytes []byte) {
	schemaOK := true
	if err := assertCouncilReview(review); err != nil {
		v.fail("council-review-schema", "Invalid council review.json v2: "+err.Error())
		schemaOK = false
	} else {
		v.pass("council-review-schema")
	}
	// Test-1CSpecFinal.ps1:52-53 and Council.Validation.ps1:499.
	lintFindings, lintErr := LintSpec(specBytes)
	if lintErr != nil {
		v.fail("final-spec-lint", "Final spec lint could not run: "+lintErr.Error())
		return
	}
	v.record("final-spec-lint", !hasLintErrors(lintFindings), "Final specification lint failed.")
	if !schemaOK {
		return // Test-1CSpecFinal.ps1:54 gates the deterministic gate.
	}
	verdict := finalString(review["verdict"])
	diversity := finalString(review["diversity"])
	chair, _ := review["chair"].(map[string]any)
	chairVerdict := finalString(chair["verdict"])
	members, _ := review["members"].([]any)
	questions, _ := review["questions"].([]any)
	findings, _ := review["findings"].([]any)
	protected, _ := review["protected"].([]any)
	chairDecisions, _ := chair["decisions"].([]any)

	// Live hash bindings (Council.Validation.ps1:505-513).
	inputs, _ := review["inputs"].(map[string]any)
	reconciliation, _ := review["reconciliation"].(map[string]any)
	specHash := sha256Hex(specBytes)
	originalHash := sha256Hex(originalBytes)
	var designHash any
	if data, ok := v.file("design.md"); ok {
		designHash = sha256Hex(data)
	}
	v.record("binding-original-task", originalHash == finalString(inputs["original_task_sha256"]), "original-task.md changed after review.")
	v.record("binding-final-spec", finalString(reconciliation["final_spec_sha256"]) == specHash, "reconciliation.final_spec_sha256 does not match current spec.md.")
	v.record("binding-draft-spec", finalString(reconciliation["draft_spec_sha256"]) == finalString(inputs["spec_sha256"]), "reconciliation.draft_spec_sha256 does not match the reviewed draft.")
	v.record("binding-final-design", nullableEqual(reconciliation["final_design_sha256"], designHash), "Design hash mismatch after review.")

	// Deterministic verdict gate (Council.Validation.ps1:525-546).
	var problems []string
	if diversity == "degraded" && verdict == "PASS" {
		problems = append(problems, "Degraded council cannot PASS.")
	}
	if diversity == "unknown" && verdict == "PASS" {
		problems = append(problems, "Unknown diversity cannot PASS as multi-model evidence.")
	}
	terminalFailure := false
	for _, raw := range members {
		if member, ok := raw.(map[string]any); ok && finalString(member["status"]) != "completed" {
			terminalFailure = true
			break
		}
	}
	if terminalFailure && verdict == "PASS" {
		problems = append(problems, "Council with a terminal member failure cannot PASS.")
	}
	if chairVerdict != "PASS" && verdict == "PASS" {
		problems = append(problems, "Deterministic gate cannot upgrade the chair verdict.")
	}
	if chairVerdict == "needs_input" && verdict != "needs_input" {
		problems = append(problems, "Chair needs_input must propagate.")
	}
	if len(questions) > 0 && verdict != "needs_input" {
		problems = append(problems, "Unresolved member questions require a needs_input verdict.")
	}
	if verdict != "PASS" && len(findings) == 0 && len(protected) == 0 && len(chairDecisions) == 0 {
		problems = append(problems, "A non-PASS council review must contain at least one reconciled decision.")
	}
	v.record("council-verdict", len(problems) == 0, strings.Join(problems, "; "))

	// Published bytes must be the chair text (Council.Validation.ps1:534-541).
	if normalizedReference(string(specBytes)) != normalizedReference(finalString(chair["final_spec_text"])) {
		v.fail("final-spec-text", "Published spec.md is not the chair final specification text.")
	} else {
		v.pass("final-spec-text")
	}

	// Controller acceptance contract that the native path adds on top of the
	// PowerShell gate (cli/internal/repository/controller_spec_review.go:89,
	// 133, 173, 215-217).
	var acceptance []string
	if verdict != "PASS" {
		acceptance = append(acceptance, "council review verdict is not PASS")
	}
	if chairVerdict != "PASS" {
		acceptance = append(acceptance, "council chair verdict is not PASS")
	}
	v.record("verdict-pass", len(acceptance) == 0, strings.Join(acceptance, "; "))
	gate, _ := review["gate"].(map[string]any)
	v.record("gate-passed", gate["passed"] == true, "council review gate is not a passing structural gate")
}

// runLegacy ports the schema v1 branch of Test-1CSpecFinal.ps1:86-165.
func (v *finalValidator) runLegacy(reviewBytes []byte, reviewPresent, specPresent, originalPresent bool) {
	// Test-1CSpecFinal.ps1:86-88.
	reconciliationPresent := v.requireFile("review-reconciliation.json", "input-review-reconciliation.json")
	if !(reviewPresent && specPresent && originalPresent && reconciliationPresent) {
		return // Test-1CSpecFinal.ps1:89 gates every later check.
	}
	baseOK := true
	review, decodeErr := decodeJSONObject(reviewBytes)
	if decodeErr != nil {
		v.fail("review-schema", "Invalid review.json: "+decodeErr.Error())
		baseOK = false
	} else if err := assertReviewPayload(review); err != nil {
		v.fail("review-schema", "Invalid review.json: "+err.Error())
		baseOK = false
	} else {
		v.pass("review-schema")
	}
	var reconciliation map[string]any
	if data, ok := v.file("review-reconciliation.json"); ok {
		object, err := decodeJSONObject(data)
		if err != nil {
			v.fail("reconciliation-schema", "Invalid review-reconciliation.json: "+err.Error())
			baseOK = false
		} else if err := assertReconciliationPayload(object); err != nil {
			v.fail("reconciliation-schema", "Invalid review-reconciliation.json: "+err.Error())
			baseOK = false
		} else {
			reconciliation = object
			v.pass("reconciliation-schema")
		}
	}
	if !baseOK {
		return
	}
	specBytes, _ := v.file("spec.md")
	originalBytes, _ := v.file("original-task.md")
	// Test-1CSpecFinal.ps1:97-98: a lint that cannot run gates the whole
	// semantic phase; its failure is recorded at the PowerShell position
	// (line 165) below.
	lintFindings, lintErr := LintSpec(specBytes)
	if lintErr != nil {
		v.fail("final-spec-lint", "Final spec lint could not run: "+lintErr.Error())
		return
	}
	// Test-1CSpecFinal.ps1:102.
	iteration, _ := finalInteger(review["review_iteration"], "review_iteration")
	v.record("review-iteration", iteration == 1, "Only one content review iteration is allowed in the normal workflow.")
	// Test-1CSpecFinal.ps1:103-113 hash bindings.
	reviewHash := sha256Hex(reviewBytes)
	specHash := sha256Hex(specBytes)
	originalHash := sha256Hex(originalBytes)
	var designHash any
	if data, ok := v.file("design.md"); ok {
		designHash = sha256Hex(data)
	}
	inputs, _ := review["inputs"].(map[string]any)
	v.record("binding-review-hash", finalString(reconciliation["review_sha256"]) == reviewHash, "reconciliation.review_sha256 does not match review.json.")
	v.record("binding-draft-spec", finalString(reconciliation["draft_spec_sha256"]) == finalString(inputs["spec_sha256"]), "reconciliation.draft_spec_sha256 does not match the reviewed draft.")
	v.record("binding-final-spec", finalString(reconciliation["final_spec_sha256"]) == specHash, "reconciliation.final_spec_sha256 does not match current spec.md.")
	v.record("binding-draft-design", nullableEqual(reconciliation["draft_design_sha256"], inputs["design_sha256"]), "reconciliation.draft_design_sha256 does not match the reviewed design.")
	v.record("binding-final-design", nullableEqual(reconciliation["final_design_sha256"], designHash), "reconciliation.final_design_sha256 does not match current design.md.")
	v.record("binding-original-task", originalHash == finalString(inputs["original_task_sha256"]), "original-task.md changed after review.")

	// Findings reconciliation (Test-1CSpecFinal.ps1:114-133).
	reviewFindings, _ := review["findings"].([]any)
	decisions, _ := reconciliation["decisions"].([]any)
	countByFinding := map[string]int{}
	decisionByFinding := map[string]map[string]any{}
	for _, raw := range decisions {
		decision, _ := raw.(map[string]any)
		id := finalString(decision["finding_id"])
		countByFinding[id]++
		decisionByFinding[id] = decision
	}
	duplicate := false
	for _, count := range countByFinding {
		if count > 1 {
			duplicate = true
			break
		}
	}
	v.record("findings-unique", !duplicate, "A finding is reconciled more than once.")
	knownFindings := map[string]bool{}
	for _, raw := range reviewFindings {
		finding, _ := raw.(map[string]any)
		id := finalString(finding["id"])
		knownFindings[id] = true
		if countByFinding[id] != 1 {
			v.fail("finding-reconciled["+id+"]", "Finding must be reconciled exactly once: "+id)
			continue
		}
		v.pass("finding-reconciled[" + id + "]")
		decision := decisionByFinding[id]
		if !finalEnum(decision["decision"], "accepted", "rejected") {
			v.fail("decision-value["+id+"]", "Invalid decision for "+id+".")
		} else {
			v.pass("decision-value[" + id + "]")
		}
		var missing []string
		for _, field := range []string{"reason", "evidence", "resolution", "spec_ref_after"} {
			if isBlank(decision[field]) {
				missing = append(missing, "Missing "+field+" for "+id+".")
			}
		}
		v.record("decision-fields["+id+"]", len(missing) == 0, strings.Join(missing, "; "))
		var status []string
		if finalString(decision["decision"]) == "accepted" && finalString(decision["status"]) != "addressed" {
			status = append(status, "Accepted finding is not addressed: "+id)
		}
		if finalString(decision["decision"]) == "rejected" && finalString(decision["status"]) != "not_applicable" {
			status = append(status, "Rejected finding must be not_applicable: "+id)
		}
		v.record("decision-status["+id+"]", len(status) == 0, strings.Join(status, "; "))
	}
	for _, raw := range decisions {
		id := finalString(raw.(map[string]any)["finding_id"])
		if !knownFindings[id] {
			v.fail("decision-known["+id+"]", "Unknown finding in reconciliation: "+id)
		}
	}
	acceptedCount := 0
	for _, raw := range decisions {
		if finalString(raw.(map[string]any)["decision"]) == "accepted" {
			acceptedCount++
		}
	}
	v.record("accepted-changes",
		!(acceptedCount > 0 && specHash == finalString(inputs["spec_sha256"]) && nullableEqual(designHash, inputs["design_sha256"])),
		"Accepted findings exist but neither spec.md nor design.md changed.")

	// do_not_change reconciliation (Test-1CSpecFinal.ps1:152-164).
	doNotChange, _ := review["do_not_change"].([]any)
	checks, _ := reconciliation["do_not_change_checks"].([]any)
	checkByItem := map[string]map[string]any{}
	for _, raw := range checks {
		check, _ := raw.(map[string]any)
		checkByItem[finalString(check["item"])] = check
	}
	knownItems := map[string]bool{}
	for _, raw := range doNotChange {
		item := finalString(raw)
		knownItems[item] = true
		check, reconciled := checkByItem[item]
		if !reconciled {
			v.fail("do-not-change-reconciled["+item+"]", "do_not_change item must be reconciled exactly once: "+item)
			continue
		}
		v.pass("do-not-change-reconciled[" + item + "]")
		if !finalEnum(check["decision"], "preserved", "rejected") {
			v.fail("do-not-change-decision["+item+"]", "Invalid do_not_change decision: "+item)
		} else {
			v.pass("do-not-change-decision[" + item + "]")
		}
		complete := !isBlank(check["reason"]) && !isBlank(check["evidence"])
		v.record("do-not-change-fields["+item+"]", complete, "do_not_change decision lacks reason/evidence: "+item)
	}
	for _, raw := range checks {
		check, _ := raw.(map[string]any)
		item := finalString(check["item"])
		if !knownItems[item] {
			v.fail("do-not-change-unknown["+item+"]", "Unknown do_not_change check: "+item)
		}
	}
	// Test-1CSpecFinal.ps1:165.
	v.record("final-spec-lint", !hasLintErrors(lintFindings), "Final specification lint failed.")
}

func (v *finalValidator) file(rel string) ([]byte, bool) {
	data, err := v.read(rel)
	if err != nil || data == nil {
		return nil, false
	}
	return data, true
}

func (v *finalValidator) path(rel string) string {
	return filepath.Join(v.changeDir, rel)
}

func (v *finalValidator) pass(name string) {
	v.checks = append(v.checks, FinalCheck{Name: name, Pass: true})
}

func (v *finalValidator) fail(name, detail string) {
	v.checks = append(v.checks, FinalCheck{Name: name, Pass: false, Detail: detail})
}

func (v *finalValidator) record(name string, pass bool, detail string) {
	if pass {
		v.pass(name)
	} else {
		v.fail(name, detail)
	}
}

// requireFile mirrors the required-input loop of Test-1CSpecFinal.ps1:32-34.
func (v *finalValidator) requireFile(rel, check string) bool {
	if _, ok := v.file(rel); ok {
		v.pass(check)
		return true
	}
	v.fail(check, "Missing required final-validation input: "+v.path(rel))
	return false
}

func hasLintErrors(findings []Finding) bool {
	for _, finding := range findings {
		if finding.Severity == "error" {
			return true
		}
	}
	return false
}

func finalString(value any) string {
	text, _ := value.(string)
	return text
}

func nullableEqual(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return finalString(left) == finalString(right)
}

func isBlank(value any) bool {
	text, ok := value.(string)
	return !ok || strings.TrimSpace(text) == ""
}

func finalEnum(value any, allowed ...string) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	for _, candidate := range allowed {
		if text == candidate {
			return true
		}
	}
	return false
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// normalizedReference ports Get-BSLFlowCouncilNormalizedReference
// (Council.Validation.ps1:32-36): collapse whitespace runs to single spaces
// and trim the edges.
func normalizedReference(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// finalSchemaPeek mirrors the PowerShell "-eq 2" peek of
// Test-1CSpecFinal.ps1:44-46, including its string coercion of an exact
// "2".
func finalSchemaPeek(value any) int64 {
	if number, err := finalInteger(value, "schema_version"); err == nil {
		return number
	}
	if text, ok := value.(string); ok && text == "2" {
		return 2
	}
	return 0
}

// decodeJSONObject parses one strict top-level JSON object with number
// literals preserved, mirroring what ConvertFrom-Json feeds the
// PowerShell asserts.
func decodeJSONObject(data []byte) (map[string]any, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("input is not valid UTF-8")
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimPrefix(string(data), "\uFEFF")))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid JSON: %v", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after the JSON value")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("top-level JSON value must be an object")
	}
	return object, nil
}

// finalObjectProps ports Assert-BSLFlowObjectProperties
// (Review.Common.ps1:82-92) with the exact property set required and no
// optional extras.  Unknown properties are reported in sorted order so the
// first message is deterministic (PowerShell reports them in document
// order).
func finalObjectProps(value any, name string, required []string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, fmt.Errorf("%s must be an object.", name)
	}
	fields := make([]string, 0, len(object))
	for field := range object {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		allowed := false
		for _, candidate := range required {
			if candidate == field {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("Unknown %s property: %s", name, field)
		}
	}
	for _, field := range required {
		if _, present := object[field]; !present {
			return nil, fmt.Errorf("Missing %s property: %s", name, field)
		}
	}
	return object, nil
}

// finalText ports Assert-BSLFlowText (Review.Common.ps1:75-80).
func finalText(value any, name string, allowEmpty bool) error {
	text, ok := value.(string)
	if !ok || (!allowEmpty && strings.TrimSpace(text) == "") {
		return fmt.Errorf("Review field must be a non-empty string: %s", name)
	}
	return nil
}

// finalArray ports Assert-BSLFlowArray (Review.Common.ps1:103-106).
func finalArray(value any, name string) ([]any, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a JSON array.", name)
	}
	return items, nil
}

// finalNumber ports Get-BSLFlowJsonNumber (Review.Common.ps1:94-101) minus
// the NaN/Infinity branches that JSON cannot produce.
func finalNumber(value any, name string) (float64, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("%s must be a JSON number.", name)
	}
	parsed, err := number.Float64()
	if err != nil {
		return 0, fmt.Errorf("%s must be a JSON number.", name)
	}
	return parsed, nil
}

func finalInteger(value any, name string) (int64, error) {
	parsed, err := finalNumber(value, name)
	if err != nil {
		return 0, err
	}
	if parsed != math.Floor(parsed) {
		return 0, fmt.Errorf("%s must be an integer.", name)
	}
	return int64(parsed), nil
}

// finalRFC3339 ports the reviewed_at_utc / reconciled_at_utc check of
// Review.Common.ps1:183-193, 248-257.  Go's RFC 3339 parser requires the
// "T" separator that .NET's looser TryParse also accepted; artifacts are
// machine-written with "T", so the stricter read stays deterministic.
func finalRFC3339(value any, name string) error {
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s must be an RFC 3339 date-time string with an offset.", name)
	}
	if _, err := time.Parse(time.RFC3339, text); err != nil {
		return fmt.Errorf("%s must be an RFC 3339 date-time string with an offset.", name)
	}
	return nil
}

var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var findingIDPattern = regexp.MustCompile(`^R-[0-9]{3,}$`)
var councilFindingIDPattern = regexp.MustCompile(`^F-[0-9]{3,}$`)

// assertReviewPayload ports Assert-BSLFlowReviewPayload -Completed
// (Review.Common.ps1:123-225); the throw of the first failed check becomes
// the returned error, mirroring how Test-1CSpecFinal.ps1:90-91 surfaces it.
func assertReviewPayload(review map[string]any) error {
	object, err := finalObjectProps(review, "review", []string{
		"schema_version", "reviewer_verdict", "summary", "scores", "overengineering", "findings", "do_not_change", "confidence",
		"reviewed_at_utc", "review_iteration", "verdict", "weighted_score", "blocking_findings", "reviewer", "inputs", "gate",
	})
	if err != nil {
		return err
	}
	if schema, err := finalInteger(object["schema_version"], "review.schema_version"); err != nil || schema != 1 {
		if err != nil {
			return err
		}
		return errors.New("review.schema_version must be 1.")
	}
	if !finalEnum(object["reviewer_verdict"], "PASS", "REVISE", "BLOCK") {
		return errors.New("Invalid reviewer_verdict.")
	}
	if err := finalText(object["summary"], "summary", false); err != nil {
		return err
	}
	scoreNames := []string{"intent_fidelity", "minimality", "completeness", "architecture_fit", "testability", "assumption_discipline", "clarity"}
	scores, err := finalObjectProps(object["scores"], "scores", scoreNames)
	if err != nil {
		return err
	}
	for _, name := range scoreNames {
		score, err := finalInteger(scores[name], "scores."+name)
		if err != nil {
			return err
		}
		if score < 1 || score > 5 {
			return fmt.Errorf("Invalid integer 1..5 score: %s", name)
		}
	}
	confidence, err := finalNumber(object["confidence"], "confidence")
	if err != nil {
		return err
	}
	if confidence < 0 || confidence > 1 {
		return errors.New("confidence must be between 0 and 1.")
	}
	overengineering, err := finalObjectProps(object["overengineering"], "overengineering", []string{
		"architectural_decision_count", "required_count", "justified_count", "optional_count", "unjustified_count",
		"index", "optional_ratio", "unjustified_ratio", "normalized_index", "items",
	})
	if err != nil {
		return err
	}
	items, err := finalArray(overengineering["items"], "overengineering.items")
	if err != nil {
		return err
	}
	findings, err := finalArray(object["findings"], "findings")
	if err != nil {
		return err
	}
	doNotChange, err := finalArray(object["do_not_change"], "do_not_change")
	if err != nil {
		return err
	}
	seenFindings := map[string]bool{}
	for index, raw := range findings {
		finding, err := finalObjectProps(raw, "finding", []string{"id", "severity", "category", "spec_ref", "issue", "evidence", "suggested_direction"})
		if err != nil {
			return err
		}
		id := finalString(finding["id"])
		if !findingIDPattern.MatchString(id) {
			return fmt.Errorf("Invalid finding id: %v", finding["id"])
		}
		if id != fmt.Sprintf("R-%03d", index+1) {
			return fmt.Errorf("Finding IDs must be sequential: expected R-%03d.", index+1)
		}
		if seenFindings[id] {
			return fmt.Errorf("Duplicate finding id: %s", id)
		}
		seenFindings[id] = true
		if !finalEnum(finding["severity"], "blocker", "high", "medium", "low") {
			return fmt.Errorf("Invalid finding severity: %s", id)
		}
		if !finalEnum(finding["category"],
			"intent_drift", "missing_requirement", "lost_requirement", "unsupported_assumption", "scope_creep",
			"overengineering", "architecture_fit", "testability", "clarity", "prompt_injection") {
			return fmt.Errorf("Invalid finding category '%v' in finding %s.", finding["category"], id)
		}
		for _, field := range []string{"spec_ref", "issue", "evidence", "suggested_direction"} {
			if err := finalText(finding[field], fmt.Sprintf("findings.%s.%s", id, field), false); err != nil {
				return err
			}
		}
	}
	for _, raw := range items {
		item, err := finalObjectProps(raw, "overengineering item", []string{"spec_ref", "item", "necessity", "evidence", "simpler_direction"})
		if err != nil {
			return err
		}
		for _, field := range []string{"spec_ref", "item", "necessity", "evidence"} {
			if err := finalText(item[field], "overengineering.items."+field, false); err != nil {
				return err
			}
		}
		if err := finalText(item["simpler_direction"], "overengineering.items.simpler_direction", true); err != nil {
			return err
		}
		if !finalEnum(item["necessity"], "required", "justified", "optional", "unjustified") {
			return fmt.Errorf("Invalid necessity: %v", item["necessity"])
		}
	}
	uniqueItems := map[string]bool{}
	for _, raw := range doNotChange {
		text, ok := raw.(string)
		if !ok {
			return errors.New("Review field must be a non-empty string: do_not_change[]")
		}
		if strings.TrimSpace(text) == "" {
			return errors.New("Review field must be a non-empty string: do_not_change[]")
		}
		if uniqueItems[text] {
			return errors.New("do_not_change items must be unique.")
		}
		uniqueItems[text] = true
	}
	// Completed-only section (Review.Common.ps1:178-224).
	if iteration, err := finalInteger(object["review_iteration"], "review_iteration"); err != nil || iteration != 1 {
		if err != nil {
			return err
		}
		return errors.New("review_iteration must be 1.")
	}
	if !finalEnum(object["verdict"], "PASS", "REVISE", "BLOCK") {
		return errors.New("Invalid gate verdict.")
	}
	weighted, err := finalNumber(object["weighted_score"], "weighted_score")
	if err != nil {
		return err
	}
	if weighted < 1 || weighted > 5 {
		return errors.New("weighted_score must be between 1 and 5.")
	}
	if err := finalRFC3339(object["reviewed_at_utc"], "reviewed_at_utc"); err != nil {
		return err
	}
	for _, name := range []string{"architectural_decision_count", "required_count", "justified_count", "optional_count", "unjustified_count", "index"} {
		count, err := finalInteger(overengineering[name], "overengineering."+name)
		if err != nil {
			return err
		}
		if count < 0 {
			return fmt.Errorf("Invalid overengineering count: %s", name)
		}
	}
	for _, name := range []string{"optional_ratio", "unjustified_ratio", "normalized_index"} {
		ratio, err := finalNumber(overengineering[name], "overengineering."+name)
		if err != nil {
			return err
		}
		if ratio < 0 || ratio > 1 {
			return fmt.Errorf("Invalid overengineering ratio: %s", name)
		}
	}
	reviewer, err := finalObjectProps(object["reviewer"], "reviewer", []string{"provider", "agent", "model"})
	if err != nil {
		return err
	}
	if finalString(reviewer["provider"]) != "opencode" {
		return errors.New("reviewer.provider must be opencode.")
	}
	for _, name := range []string{"agent", "model"} {
		if err := finalText(reviewer[name], "reviewer."+name, false); err != nil {
			return err
		}
	}
	inputs, err := finalObjectProps(object["inputs"], "inputs", []string{"original_task_sha256", "spec_sha256", "design_sha256"})
	if err != nil {
		return err
	}
	for _, name := range []string{"original_task_sha256", "spec_sha256"} {
		value := inputs[name]
		text, ok := value.(string)
		if !ok || !sha256Pattern.MatchString(text) {
			return fmt.Errorf("Invalid input hash: %s", name)
		}
	}
	if design, present := inputs["design_sha256"]; present && design != nil {
		text, ok := design.(string)
		if !ok || !sha256Pattern.MatchString(text) {
			return errors.New("Invalid design_sha256.")
		}
	}
	gate, err := finalObjectProps(object["gate"], "gate", []string{"pass_weighted_score", "block_below_weighted_score", "max_overengineering_index_for_pass", "max_unjustified_ratio_for_pass"})
	if err != nil {
		return err
	}
	for _, name := range []string{"pass_weighted_score", "block_below_weighted_score"} {
		threshold, err := finalNumber(gate[name], "gate."+name)
		if err != nil {
			return err
		}
		if threshold < 1 || threshold > 5 {
			return fmt.Errorf("Invalid gate threshold: %s", name)
		}
	}
	if index, err := finalInteger(gate["max_overengineering_index_for_pass"], "gate.max_overengineering_index_for_pass"); err != nil || index < 0 {
		if err != nil {
			return err
		}
		return errors.New("Invalid max_overengineering_index_for_pass.")
	}
	maxRatio, err := finalNumber(gate["max_unjustified_ratio_for_pass"], "gate.max_unjustified_ratio_for_pass")
	if err != nil {
		return err
	}
	if maxRatio < 0 || maxRatio > 1 {
		return errors.New("Invalid max_unjustified_ratio_for_pass.")
	}
	blocking, err := finalArray(object["blocking_findings"], "blocking_findings")
	if err != nil {
		return err
	}
	seenBlocking := map[string]bool{}
	for _, raw := range blocking {
		text, ok := raw.(string)
		if !ok || !findingIDPattern.MatchString(text) {
			return errors.New("Invalid blocking_findings item.")
		}
		if seenBlocking[text] {
			return errors.New("blocking_findings must be unique.")
		}
		seenBlocking[text] = true
	}
	if finalString(object["verdict"]) != "PASS" && len(findings) == 0 {
		return errors.New("A non-PASS review must contain at least one finding.")
	}
	return nil
}

// assertReconciliationPayload ports Assert-BSLFlowReviewReconciliationPayload
// (Review.Common.ps1:227-276).
func assertReconciliationPayload(reconciliation map[string]any) error {
	object, err := finalObjectProps(reconciliation, "reconciliation", []string{
		"schema_version", "review_sha256", "draft_spec_sha256", "final_spec_sha256",
		"draft_design_sha256", "final_design_sha256", "reconciled_at_utc", "summary",
		"decisions", "do_not_change_checks",
	})
	if err != nil {
		return err
	}
	if schema, err := finalInteger(object["schema_version"], "reconciliation.schema_version"); err != nil || schema != 1 {
		if err != nil {
			return err
		}
		return errors.New("reconciliation.schema_version must be 1.")
	}
	for _, name := range []string{"review_sha256", "draft_spec_sha256", "final_spec_sha256"} {
		value := object[name]
		text, ok := value.(string)
		if !ok || !sha256Pattern.MatchString(text) {
			return fmt.Errorf("Invalid reconciliation hash: %s", name)
		}
	}
	for _, name := range []string{"draft_design_sha256", "final_design_sha256"} {
		if value, present := object[name]; present && value != nil {
			text, ok := value.(string)
			if !ok || !sha256Pattern.MatchString(text) {
				return fmt.Errorf("Invalid reconciliation hash: %s", name)
			}
		}
	}
	if err := finalRFC3339(object["reconciled_at_utc"], "reconciliation.reconciled_at_utc"); err != nil {
		return err
	}
	if err := finalText(object["summary"], "reconciliation.summary", false); err != nil {
		return err
	}
	decisions, err := finalArray(object["decisions"], "reconciliation.decisions")
	if err != nil {
		return err
	}
	checks, err := finalArray(object["do_not_change_checks"], "reconciliation.do_not_change_checks")
	if err != nil {
		return err
	}
	for _, raw := range decisions {
		decision, err := finalObjectProps(raw, "reconciliation decision", []string{"finding_id", "decision", "reason", "evidence", "status", "resolution", "spec_ref_after"})
		if err != nil {
			return err
		}
		id, ok := decision["finding_id"].(string)
		if !ok || !findingIDPattern.MatchString(id) {
			return errors.New("Invalid reconciliation decision finding_id.")
		}
		if !finalEnum(decision["decision"], "accepted", "rejected") {
			return fmt.Errorf("Invalid reconciliation decision: %s", id)
		}
		if !finalEnum(decision["status"], "addressed", "not_applicable") {
			return fmt.Errorf("Invalid reconciliation status: %s", id)
		}
		for _, name := range []string{"reason", "evidence", "resolution", "spec_ref_after"} {
			if err := finalText(decision[name], fmt.Sprintf("reconciliation.decisions.%s.%s", id, name), false); err != nil {
				return err
			}
		}
	}
	for _, raw := range checks {
		check, err := finalObjectProps(raw, "do_not_change check", []string{"item", "decision", "reason", "evidence"})
		if err != nil {
			return err
		}
		for _, name := range []string{"item", "reason", "evidence"} {
			if err := finalText(check[name], "reconciliation.do_not_change_checks."+name, false); err != nil {
				return err
			}
		}
		if !finalEnum(check["decision"], "preserved", "rejected") {
			return fmt.Errorf("Invalid do_not_change decision: %v", check["item"])
		}
	}
	return nil
}

// assertCouncilReview ports the structural subset of
// Assert-BSLFlowCouncilReview (Council.Validation.ps1:277-484) that the
// final gate itself consumes: top-level shape, enums, hash-shaped inputs,
// manifest presence, member role/status surface, canonical finding order,
// protected/question arrays, chair verdict and reconciliation/gate shape.
// Deep member envelope, manifest item and chair reference validation stay
// owned by cli/internal/repository/controller_spec_review.go.
func assertCouncilReview(review map[string]any) error {
	object, err := finalObjectProps(review, "review", []string{
		"schema_version", "reviewed_at_utc", "council_schema_version", "verdict",
		"diversity", "fallback_visible", "inputs", "manifest", "members",
		"findings", "protected", "questions", "chair", "reconciliation", "gate",
	})
	if err != nil {
		return err
	}
	if schema, err := finalInteger(object["schema_version"], "review.schema_version"); err != nil || schema != 2 {
		if err != nil {
			return err
		}
		return errors.New("review.schema_version must be 2.")
	}
	if council, err := finalInteger(object["council_schema_version"], "review.council_schema_version"); err != nil || council != 1 {
		if err != nil {
			return err
		}
		return errors.New("review.council_schema_version must be 1.")
	}
	if !finalEnum(object["verdict"], "PASS", "REVISE", "BLOCK", "needs_input") {
		return errors.New("Invalid council verdict.")
	}
	if !finalEnum(object["diversity"], "multi_model", "multi_role_single_model", "degraded", "unknown") {
		return errors.New("Invalid diversity status.")
	}
	if _, ok := object["fallback_visible"].(bool); !ok {
		return errors.New("review.fallback_visible must be boolean.")
	}
	inputs, err := finalObjectProps(object["inputs"], "inputs", []string{"original_task_sha256", "spec_sha256", "design_sha256", "policy_hash"})
	if err != nil {
		return err
	}
	for _, name := range []string{"original_task_sha256", "spec_sha256", "policy_hash"} {
		text, ok := inputs[name].(string)
		if !ok || !sha256Pattern.MatchString(text) {
			return fmt.Errorf("Invalid inputs hash: %s", name)
		}
	}
	if design, present := inputs["design_sha256"]; present && design != nil {
		text, ok := design.(string)
		if !ok || !sha256Pattern.MatchString(text) {
			return errors.New("Invalid inputs.design_sha256.")
		}
	}
	manifest, err := finalObjectProps(object["manifest"], "manifest", []string{"version", "requirements"})
	if err != nil {
		return err
	}
	if version, err := finalInteger(manifest["version"], "manifest.version"); err != nil || version != 1 {
		if err != nil {
			return err
		}
		return errors.New("manifest.version must be 1.")
	}
	requirements, err := finalArray(manifest["requirements"], "manifest.requirements")
	if err != nil {
		return err
	}
	if len(requirements) == 0 {
		return errors.New("manifest.requirements must not be empty.")
	}
	// Members surface (Council.Validation.ps1:296-312 subset).
	members, err := finalArray(object["members"], "members")
	if err != nil {
		return err
	}
	if len(members) == 0 {
		return errors.New("Council review must contain at least one member.")
	}
	seenRoles := map[string]bool{}
	hasChair := false
	for _, raw := range members {
		member, ok := raw.(map[string]any)
		if !ok {
			return errors.New("member must be an object.")
		}
		role := finalString(member["role"])
		if !finalEnum(role, "brainstorm", "intent_critic", "architecture_critic", "executability_critic", "chair") {
			return fmt.Errorf("Unknown member role: %v", member["role"])
		}
		if seenRoles[role] {
			return errors.New("Member roles must be unique.")
		}
		seenRoles[role] = true
		if role == "chair" {
			hasChair = true
		}
		if !finalEnum(member["status"], "completed", "failed_before_acceptance", "unknown_after_dispatch", "cancelled", "invalid_response") {
			return fmt.Errorf("Invalid member status: %s", role)
		}
	}
	if !hasChair {
		return errors.New("Council review must contain the chair member record.")
	}
	// Canonical findings order (Council.Validation.ps1:252-264, 356-363).
	findings, err := finalArray(object["findings"], "findings")
	if err != nil {
		return err
	}
	roleOrder := map[string]int{"brainstorm": 0, "intent_critic": 1, "architecture_critic": 2, "executability_critic": 3}
	type findingRef struct{ role, id, composite string }
	refs := make([]findingRef, 0, len(findings))
	seenComposite := map[string]bool{}
	for _, raw := range findings {
		finding, ok := raw.(map[string]any)
		if !ok {
			return errors.New("finding must be an object.")
		}
		role := finalString(finding["role"])
		if _, known := roleOrder[role]; !known {
			return fmt.Errorf("Unknown finding role: %v", finding["role"])
		}
		id := finalString(finding["id"])
		composite := finalString(finding["composite_id"])
		if composite != role+":"+id {
			return fmt.Errorf("Finding composite_id must equal role:id: %s:%s.", role, id)
		}
		if seenComposite[composite] {
			return fmt.Errorf("Duplicate composite finding: %s", composite)
		}
		seenComposite[composite] = true
		refs = append(refs, findingRef{role: role, id: id, composite: composite})
	}
	sorted := make([]findingRef, len(refs))
	copy(sorted, refs)
	sort.SliceStable(sorted, func(i, j int) bool {
		if roleOrder[sorted[i].role] != roleOrder[sorted[j].role] {
			return roleOrder[sorted[i].role] < roleOrder[sorted[j].role]
		}
		if sorted[i].id != sorted[j].id {
			return sorted[i].id < sorted[j].id
		}
		return sorted[i].composite < sorted[j].composite
	})
	for index := range refs {
		if refs[index].composite != sorted[index].composite {
			return errors.New("Review findings must be stored in canonical role/id order.")
		}
	}
	for _, raw := range findings {
		finding := raw.(map[string]any)
		if _, err := finalObjectProps(finding, "finding", []string{"composite_id", "role", "id", "severity", "category", "spec_ref", "issue", "evidence", "suggested_direction"}); err != nil {
			return err
		}
		if !councilFindingIDPattern.MatchString(finalString(finding["id"])) {
			return fmt.Errorf("Invalid aggregate finding id: %v", finding["composite_id"])
		}
	}
	// Protected items (Council.Validation.ps1:364-370).
	protected, err := finalArray(object["protected"], "protected")
	if err != nil {
		return err
	}
	seenProtected := map[string]bool{}
	for _, raw := range protected {
		item, err := finalObjectProps(raw, "protected", []string{"composite_id", "role", "item"})
		if err != nil {
			return err
		}
		composite := finalString(item["composite_id"])
		if seenProtected[composite] {
			return errors.New("Protected composite IDs must be unique.")
		}
		seenProtected[composite] = true
		if err := finalText(item["item"], fmt.Sprintf("protected.%s", composite), false); err != nil {
			return err
		}
	}
	// Questions (Council.Validation.ps1:373-380).
	questions, err := finalArray(object["questions"], "questions")
	if err != nil {
		return err
	}
	for _, raw := range questions {
		question, err := finalObjectProps(raw, "question", []string{"role", "text"})
		if err != nil {
			return err
		}
		role := finalString(question["role"])
		if _, known := roleOrder[role]; !known {
			return fmt.Errorf("Unknown question role: %v", question["role"])
		}
		if err := finalText(question["text"], fmt.Sprintf("question.%s", role), false); err != nil {
			return err
		}
	}
	// Chair surface (Council.Validation.ps1:381-387).
	chair, err := finalObjectProps(object["chair"], "chair", []string{"verdict", "decisions", "protected_decisions", "requirement_refs", "final_spec_text", "final_design_text"})
	if err != nil {
		return err
	}
	if !finalEnum(chair["verdict"], "PASS", "REVISE", "BLOCK", "needs_input") {
		return errors.New("Invalid chair verdict.")
	}
	if err := finalText(chair["final_spec_text"], "chair.final_spec_text", false); err != nil {
		return err
	}
	for _, name := range []string{"decisions", "protected_decisions", "requirement_refs"} {
		if _, err := finalArray(chair[name], "chair."+name); err != nil {
			return err
		}
	}
	// Inline reconciliation and gate (Council.Validation.ps1:444-447).
	if _, err := finalObjectProps(object["reconciliation"], "reconciliation", []string{"review_sha256", "draft_spec_sha256", "final_spec_sha256", "draft_design_sha256", "final_design_sha256"}); err != nil {
		return err
	}
	gate, err := finalObjectProps(object["gate"], "gate", []string{"structural_only", "passed"})
	if err != nil {
		return err
	}
	if gate["structural_only"] != true {
		return errors.New("Council gate is structural-only.")
	}
	if _, ok := gate["passed"].(bool); !ok {
		return errors.New("gate.passed must be boolean.")
	}
	return nil
}
