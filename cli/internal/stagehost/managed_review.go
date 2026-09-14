package stagehost

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"bsl-flow/cli/internal/repository"
	"bsl-flow/cli/internal/specvalidate"
)

// This file ports Invoke-BFSpecReviewStage and the non-council half of
// Invoke-BFProfileSpecCritic (Task.ManagedReview.ps1:609-722): the sealed
// critic dispatch, Complete-BSLFlowReview, the spec_reconcile worker
// dispatch, the reconciliation sidecar and the final validator receipt.
//
// Porting boundary: the live council route (Invoke-BSLFlowCouncilReview with
// API transports and current-agent fallback) is not served by the native
// stage host yet. A council-enabled project fails closed with a typed
// blocker; there is no PowerShell fallback.

// yamlConfigValue mirrors Get-BSLFlowYamlValue: one bounded indentation-
// stack reader for the committed bsl-flow.yaml policy values.
func yamlConfigValue(text string, path []string, defaultValue string) (string, error) {
	type frame struct {
		indent int
		key    string
	}
	stack := []frame{}
	results := []string{}
	for _, line := range yamlSplitLines(text) {
		if regexp.MustCompile(`^\s*(?:#.*)?$`).MatchString(line) {
			continue
		}
		match := regexp.MustCompile(`^(?P<indent>\s*)(?P<key>[A-Za-z0-9_-]+):(?:\s*(?P<value>.*?))?\s*$`).FindStringSubmatch(line)
		if match == nil {
			continue
		}
		indent := match[1]
		if strings.Contains(indent, "\t") {
			return "", invalidf("Tabs are not supported in bsl-flow.yaml indentation.")
		}
		for len(stack) > 0 && stack[len(stack)-1].indent >= len(indent) {
			stack = stack[:len(stack)-1]
		}
		keys := make([]string, 0, len(stack)+1)
		for _, entry := range stack {
			keys = append(keys, entry.key)
		}
		keys = append(keys, match[2])
		value := strings.TrimSpace(match[3])
		if value != "" && strings.Join(keys, "/") == strings.Join(path, "/") {
			results = append(results, strings.Trim(value, `"'`))
		}
		if value == "" {
			stack = append(stack, frame{indent: len(indent), key: match[2]})
		}
	}
	if len(results) > 1 {
		return "", invalidf("Duplicate YAML value: %s", strings.Join(path, "."))
	}
	if len(results) == 1 {
		return results[0], nil
	}
	return defaultValue, nil
}

func yamlSplitLines(text string) []string {
	return regexp.MustCompile(`\r?\n`).Split(text, -1)
}

// reviewPolicy mirrors Get-BSLFlowReviewPolicy thresholds.
type reviewPolicy struct {
	PassWeightedScore           float64
	BlockBelowWeightedScore     float64
	MaxOverengineeringIndexPass int64
	MaxUnjustifiedRatioPass     float64
}

func parseReviewPolicy(configText string) (reviewPolicy, error) {
	policy := reviewPolicy{}
	var err error
	if policy.PassWeightedScore, err = yamlFloat(configText, []string{"review", "thresholds", "pass_weighted_score"}, 4.3); err != nil {
		return policy, err
	}
	if policy.BlockBelowWeightedScore, err = yamlFloat(configText, []string{"review", "thresholds", "block_below_weighted_score"}, 3.5); err != nil {
		return policy, err
	}
	index, err := yamlInteger(configText, []string{"review", "thresholds", "max_overengineering_index_for_pass"}, 1)
	if err != nil {
		return policy, err
	}
	policy.MaxOverengineeringIndexPass = index
	if policy.MaxUnjustifiedRatioPass, err = yamlFloat(configText, []string{"review", "thresholds", "max_unjustified_ratio_for_pass"}, 0); err != nil {
		return policy, err
	}
	if policy.PassWeightedScore < 1 || policy.PassWeightedScore > 5 || policy.BlockBelowWeightedScore < 1 || policy.BlockBelowWeightedScore > 5 {
		return policy, invalidf("Review score thresholds must be between 1 and 5.")
	}
	if policy.BlockBelowWeightedScore > policy.PassWeightedScore {
		return policy, invalidf("block_below_weighted_score must not exceed pass_weighted_score.")
	}
	if policy.MaxOverengineeringIndexPass < 0 {
		return policy, invalidf("max_overengineering_index_for_pass must be non-negative.")
	}
	if policy.MaxUnjustifiedRatioPass < 0 || policy.MaxUnjustifiedRatioPass > 1 {
		return policy, invalidf("max_unjustified_ratio_for_pass must be between 0 and 1.")
	}
	return policy, nil
}

func yamlFloat(text string, path []string, defaultValue float64) (float64, error) {
	value, err := yamlConfigValue(text, path, "")
	if err != nil {
		return 0, err
	}
	if value == "" {
		return defaultValue, nil
	}
	parsed, ok := parseFloatInvariant(value)
	if !ok {
		return 0, invalidf("Invalid number for %s: %s", strings.Join(path, "."), value)
	}
	return parsed, nil
}

func yamlInteger(text string, path []string, defaultValue int64) (int64, error) {
	value, err := yamlConfigValue(text, path, "")
	if err != nil {
		return 0, err
	}
	if value == "" {
		return defaultValue, nil
	}
	parsed, ok := parseFloatInvariant(value)
	if !ok || parsed != math.Trunc(parsed) {
		return 0, invalidf("Invalid integer for %s: %s", strings.Join(path, "."), value)
	}
	return int64(parsed), nil
}

func parseFloatInvariant(text string) (float64, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

// councilRouteEnabled mirrors the council selection of
// Invoke-BFProfileSpecCritic: enabled defaults off, opencode_compat keeps the
// sealed critic route.
func councilRouteEnabled(configText string) (bool, error) {
	enabled, err := yamlConfigValue(configText, []string{"review", "council", "enabled"}, "false")
	if err != nil {
		return false, err
	}
	if enabled != "true" && enabled != "false" {
		return false, invalidf("Expected true or false for review.council.enabled, got: %s", enabled)
	}
	legacyMode, err := yamlConfigValue(configText, []string{"review", "council", "legacy_mode"}, "block")
	if err != nil {
		return false, err
	}
	if legacyMode != "block" && legacyMode != "opencode_compat" {
		return false, invalidf("Invalid review.council.legacy_mode: %s", legacyMode)
	}
	return enabled == "true" && legacyMode != "opencode_compat", nil
}

// boundedSnapshot mirrors Get-BSLFlowBoundedUtf8Snapshot.
func boundedSnapshot(path string, maxBytes int64) ([]byte, string, string, error) {
	data, err := repository.StageHostReadFileBytes(path)
	if err != nil {
		return nil, "", "", blockedf("Review input cannot be read: %s", path)
	}
	if int64(len(data)) > maxBytes {
		return nil, "", "", invalidf("Review input exceeds %d bytes: %s", maxBytes, path)
	}
	if !utf8.Valid(data) {
		return nil, "", "", invalidf("Review input is not valid UTF-8: %s", path)
	}
	return data, string(data), hashFileBytes(data), nil
}

// reviewWeights mirrors the Complete-BSLFlowReview score weights.
var reviewWeights = []struct {
	Name   string
	Weight float64
}{
	{"intent_fidelity", 0.25},
	{"minimality", 0.20},
	{"completeness", 0.15},
	{"architecture_fit", 0.15},
	{"testability", 0.10},
	{"assumption_discipline", 0.10},
	{"clarity", 0.05},
}

// completeReview mirrors Complete-BSLFlowReview: the deterministic folding of
// the sealed critic payload into the persisted review document.
func completeReview(deps Deps, raw map[string]any, originalTaskHash, specHash, designHash string, agent, model string, policy reviewPolicy) (map[string]any, error) {
	if err := assertReviewPayload(raw, false); err != nil {
		return nil, err
	}
	scores := asMap(raw["scores"])
	weighted := 0.0
	for _, entry := range reviewWeights {
		value, _ := asFloat(scores[entry.Name])
		weighted += value * entry.Weight
	}
	weighted = roundHalfEven(weighted, 2)
	items, _ := asArray(asMap(raw["overengineering"])["items"])
	counts := map[string]int64{}
	for _, necessity := range []string{"required", "justified", "optional", "unjustified"} {
		for _, rawItem := range items {
			item := asMap(rawItem)
			if asStringOr(item["necessity"]) == necessity {
				counts[necessity]++
			}
		}
	}
	decisionCount := int64(len(items))
	index := counts["optional"] + 3*counts["unjustified"]
	optionalRatio, unjustifiedRatio, normalizedIndex := 0.0, 0.0, 0.0
	if decisionCount != 0 {
		optionalRatio = roundHalfEven(float64(counts["optional"])/float64(decisionCount), 4)
		unjustifiedRatio = roundHalfEven(float64(counts["unjustified"])/float64(decisionCount), 4)
		normalizedIndex = roundHalfEven(float64(index)/(3*float64(decisionCount)), 4)
	}
	blocking := []any{}
	findings, _ := asArray(raw["findings"])
	materialFindings := 0
	for _, rawFinding := range findings {
		finding := asMap(rawFinding)
		if asStringOr(finding["severity"]) == "blocker" {
			blocking = append(blocking, finding["id"])
		}
		if asStringOr(finding["severity"]) == "high" || asStringOr(finding["severity"]) == "medium" {
			materialFindings++
		}
	}
	gateVerdict := "REVISE"
	reviewerVerdict := asStringOr(raw["reviewer_verdict"])
	switch {
	case reviewerVerdict == "BLOCK" || len(blocking) > 0 || weighted < policy.BlockBelowWeightedScore:
		gateVerdict = "BLOCK"
	case reviewerVerdict == "REVISE" || materialFindings > 0:
		gateVerdict = "REVISE"
	case weighted >= policy.PassWeightedScore && index <= policy.MaxOverengineeringIndexPass && unjustifiedRatio <= policy.MaxUnjustifiedRatioPass:
		gateVerdict = "PASS"
	}
	if gateVerdict != "PASS" && len(findings) == 0 {
		return nil, invalidf("A computed non-PASS review without findings is invalid.")
	}
	for _, hash := range []string{originalTaskHash, specHash} {
		if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(hash) {
			return nil, invalidf("Invalid captured review input hash.")
		}
	}
	if designHash != "" && !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(designHash) {
		return nil, invalidf("Invalid captured design hash.")
	}
	confidence := raw["confidence"]
	return map[string]any{
		"schema_version":    int64(1),
		"reviewed_at_utc":   deps.now().UTC().Format("2006-01-02T15:04:05.0000000Z"),
		"review_iteration":  int64(1),
		"reviewer_verdict":  reviewerVerdict,
		"verdict":           gateVerdict,
		"summary":           asStringOr(raw["summary"]),
		"scores":            raw["scores"],
		"weighted_score":    weighted,
		"overengineering":   map[string]any{"architectural_decision_count": decisionCount, "required_count": counts["required"], "justified_count": counts["justified"], "optional_count": counts["optional"], "unjustified_count": counts["unjustified"], "index": index, "optional_ratio": optionalRatio, "unjustified_ratio": unjustifiedRatio, "normalized_index": normalizedIndex, "items": items},
		"blocking_findings": blocking,
		"findings":          findings,
		"do_not_change":     raw["do_not_change"],
		"confidence":        confidence,
		"reviewer":          map[string]any{"provider": "opencode", "agent": agent, "model": model},
		"inputs":            map[string]any{"original_task_sha256": originalTaskHash, "spec_sha256": specHash, "design_sha256": nullableHash(designHash)},
		"gate": map[string]any{
			"pass_weighted_score":                policy.PassWeightedScore,
			"block_below_weighted_score":         policy.BlockBelowWeightedScore,
			"max_overengineering_index_for_pass": policy.MaxOverengineeringIndexPass,
			"max_unjustified_ratio_for_pass":     policy.MaxUnjustifiedRatioPass,
		},
	}, nil
}

func nullableHash(hash string) any {
	if hash == "" {
		return nil
	}
	return hash
}

// roundHalfEven mirrors [math]::Round's banker's rounding.
func roundHalfEven(value float64, digits int) float64 {
	factor := math.Pow(10, float64(digits))
	return math.RoundToEven(value*factor) / factor
}

var reviewFindingCategories = map[string]bool{
	"intent_drift": true, "missing_requirement": true, "lost_requirement": true,
	"unsupported_assumption": true, "scope_creep": true, "overengineering": true,
	"architecture_fit": true, "testability": true, "clarity": true, "prompt_injection": true,
}

// assertObjectProperties mirrors Assert-BSLFlowObjectProperties with its
// exact diagnostics.
func assertObjectProperties(value any, name string, required []string) (map[string]any, error) {
	object, ok := asObject(value)
	if !ok {
		return nil, invalidf("%s must be an object.", name)
	}
	for key := range object {
		known := false
		for _, candidate := range required {
			if candidate == key {
				known = true
				break
			}
		}
		if !known {
			return nil, invalidf("Unknown %s property: %s", name, key)
		}
	}
	for _, key := range required {
		if _, present := object[key]; !present {
			return nil, invalidf("Missing %s property: %s", name, key)
		}
	}
	return object, nil
}

// assertReviewPayload mirrors Assert-BSLFlowReviewPayload.
func assertReviewPayload(review map[string]any, completed bool) error {
	rawProperties := []string{"schema_version", "reviewer_verdict", "summary", "scores", "overengineering", "findings", "do_not_change", "confidence"}
	completedProperties := append(append([]string{}, rawProperties...),
		"reviewed_at_utc", "review_iteration", "verdict", "weighted_score", "blocking_findings", "reviewer", "inputs", "gate")
	allowed := rawProperties
	if completed {
		allowed = completedProperties
	}
	object, err := assertObjectProperties(review, "review", allowed)
	if err != nil {
		return err
	}
	if version, ok := asInteger(object["schema_version"]); !ok || version != 1 {
		return invalidf("review.schema_version must be 1.")
	}
	verdict := asStringOr(object["reviewer_verdict"])
	if verdict != "PASS" && verdict != "REVISE" && verdict != "BLOCK" {
		return invalidf("Invalid reviewer_verdict.")
	}
	if err := assertText(object["summary"], "summary"); err != nil {
		return err
	}
	scores, err := assertObjectProperties(object["scores"], "scores", []string{"intent_fidelity", "minimality", "completeness", "architecture_fit", "testability", "assumption_discipline", "clarity"})
	if err != nil {
		return err
	}
	for name := range scores {
		value, ok := asInteger(scores[name])
		if !ok || value < 1 || value > 5 {
			return invalidf("Invalid integer 1..5 score: %s", name)
		}
	}
	confidence, ok := asFloat(object["confidence"])
	if !ok || confidence < 0 || confidence > 1 {
		return invalidf("confidence must be between 0 and 1.")
	}
	overengineeringProperties := []string{"items"}
	if completed {
		overengineeringProperties = []string{"architectural_decision_count", "required_count", "justified_count", "optional_count", "unjustified_count", "index", "optional_ratio", "unjustified_ratio", "normalized_index", "items"}
	}
	overengineering, err := assertObjectProperties(object["overengineering"], "overengineering", overengineeringProperties)
	if err != nil {
		return err
	}
	items, itemsOK := asArray(overengineering["items"])
	findings, findingsOK := asArray(object["findings"])
	doNotChange, doNotChangeOK := asArray(object["do_not_change"])
	if !itemsOK {
		return invalidf("overengineering.items must be a JSON array.")
	}
	if !findingsOK {
		return invalidf("findings must be a JSON array.")
	}
	if !doNotChangeOK {
		return invalidf("do_not_change must be a JSON array.")
	}
	findingIDs := map[string]bool{}
	for index, rawFinding := range findings {
		finding, err := assertObjectProperties(rawFinding, "finding", []string{"id", "severity", "category", "spec_ref", "issue", "evidence", "suggested_direction"})
		if err != nil {
			return err
		}
		id := asStringOr(finding["id"])
		expected := fmt.Sprintf("R-%03d", index+1)
		if !regexp.MustCompile(`^R-[0-9]{3,}$`).MatchString(id) {
			return invalidf("Invalid finding id: %s", id)
		}
		if id != expected {
			return invalidf("Finding IDs must be sequential: expected R-%s.", expected[2:])
		}
		if findingIDs[id] {
			return invalidf("Duplicate finding id: %s", id)
		}
		findingIDs[id] = true
		severity := asStringOr(finding["severity"])
		if severity != "blocker" && severity != "high" && severity != "medium" && severity != "low" {
			return invalidf("Invalid finding severity: %s", id)
		}
		if !reviewFindingCategories[asStringOr(finding["category"])] {
			return invalidf("Invalid finding category '%s' in finding %s.", asStringOr(finding["category"]), id)
		}
		for _, field := range []string{"spec_ref", "issue", "evidence", "suggested_direction"} {
			if err := assertText(finding[field], "findings."+id+"."+field); err != nil {
				return err
			}
		}
	}
	for _, rawItem := range items {
		item, err := assertObjectProperties(rawItem, "overengineering item", []string{"spec_ref", "item", "necessity", "evidence", "simpler_direction"})
		if err != nil {
			return err
		}
		for _, field := range []string{"spec_ref", "item", "necessity", "evidence"} {
			if err := assertText(item[field], "overengineering.items."+field); err != nil {
				return err
			}
		}
		if _, isString := asString(item["simpler_direction"]); !isString {
			return invalidf("Review field must be a string: overengineering.items.simpler_direction")
		}
		necessity := asStringOr(item["necessity"])
		if necessity != "required" && necessity != "justified" && necessity != "optional" && necessity != "unjustified" {
			return invalidf("Invalid necessity: %s", necessity)
		}
	}
	seen := map[string]bool{}
	for _, rawItem := range doNotChange {
		item := asStringOr(rawItem)
		if strings.TrimSpace(item) == "" {
			return invalidf("Review field must be a non-empty string: do_not_change[]")
		}
		if seen[item] {
			return invalidf("do_not_change items must be unique.")
		}
		seen[item] = true
	}
	return nil
}

// runSpecReviewStage ports Invoke-BFSpecReviewStage for the managed profile
// route: sealed critic, spec_reconcile dispatch, reconciliation sidecar and
// the final validator receipt, returning the folded stage result with its
// bound dependencies.
func runSpecReviewStage(ctx context.Context, deps Deps, run *stageRun, raw string) (map[string]any, error) {
	state := run.state
	change, err := stageChangePath(state)
	if err != nil {
		return nil, err
	}
	review, err := invokeProfileSpecCritic(ctx, deps, run, raw)
	if err != nil {
		return nil, err
	}
	reviewPath := filepath.Join(change, "review.json")
	if err := copyRawFile(reviewPath, filepath.Join(raw, "review.json")); err != nil {
		return nil, err
	}
	for _, name := range []string{"spec.md", "design.md"} {
		path := filepath.Join(change, name)
		if isRegularFile(path) {
			if err := copyRawFile(path, filepath.Join(raw, "draft-"+name)); err != nil {
				return nil, err
			}
		}
	}
	if version, ok := asInteger(review["schema_version"]); ok && version == 2 {
		return nil, blockedf("council review publications are not served by the native stage host yet.")
	}
	reconcileDir := filepath.Join(raw, "reconciler")
	reviewText, err := readRawText(reviewPath)
	if err != nil {
		return nil, err
	}
	prompt, err := stagePrompt(deps, state, "spec_reconcile", reviewText, nil)
	if err != nil {
		return nil, err
	}
	recResult, err := runManagedWorkerDispatch(ctx, deps, run, "spec_reconcile", prompt, reconcileDir)
	if err != nil {
		return nil, err
	}
	if recResult.Status != "completed" {
		return map[string]any{"outcome": stageOutcomeOf(recResult.Status), "summary": recResult.Summary}, nil
	}
	payload, err := readStagePayload(recResult, reconcileDir)
	if err != nil {
		return nil, err
	}
	if _, err := assertFields(payload, []string{"spec", "design", "decisions", "do_not_change_checks"}, nil, "spec_reconciliation"); err != nil {
		return nil, err
	}
	before, err := stageSpecInputs(state)
	if err != nil {
		return nil, err
	}
	reviewInputs := asMap(review["inputs"])
	if before["spec.md"] != reviewInputs["spec_sha256"] || before["design.md"] != reviewInputs["design_sha256"] ||
		before["original-task.md"] != reviewInputs["original_task_sha256"] {
		return nil, blockedf("reviewed draft changed during reconciliation.")
	}
	if err := saveStageSpec(deps, state, map[string]any{"spec": payload["spec"], "design": payload["design"]}, reconcileDir); err != nil {
		return nil, err
	}
	specInputs, err := stageSpecInputs(state)
	if err != nil {
		return nil, err
	}
	reviewHash, err := hashFile(reviewPath)
	if err != nil {
		return nil, err
	}
	reconciliation := map[string]any{
		"schema_version": int64(1), "review_sha256": reviewHash,
		"draft_spec_sha256": reviewInputs["spec_sha256"], "final_spec_sha256": specInputs["spec.md"],
		"draft_design_sha256": reviewInputs["design_sha256"], "final_design_sha256": specInputs["design.md"],
		"reconciled_at_utc": deps.now().UTC().Format("2006-01-02T15:04:05.0000000Z"),
		"summary":           recResult.Summary,
		"decisions":         payload["decisions"], "do_not_change_checks": payload["do_not_change_checks"],
	}
	if err := writeJSON(filepath.Join(change, "review-reconciliation.json"), reconciliation, true); err != nil {
		return nil, err
	}
	if err := runSpecFinal(deps, state, change); err != nil {
		return nil, err
	}
	for _, name := range []string{"review-reconciliation.json", "final-validation.json"} {
		path := filepath.Join(change, name)
		if !isRegularFile(path) {
			return nil, blockedf("council review did not produce %s.", name)
		}
		if err := copyRawFile(path, filepath.Join(raw, name)); err != nil {
			return nil, err
		}
	}
	bound, err := stageDependencies(state, "spec_review")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"outcome":            "PASS",
		"summary":            recResult.Summary,
		"bound_dependencies": bound,
	}, nil
}

func readRawText(path string) (string, error) {
	resolved, err := safePath(path)
	if err != nil {
		return "", err
	}
	data, err := repository.StageHostReadFileBytes(resolved)
	if err != nil {
		return "", blockedf("%v", err)
	}
	return string(data), nil
}

// runSpecFinal runs Test-1CSpecFinal natively: the deterministic invariant
// checks are ported; the derived-value recompute stays a documented
// migration boundary of the specvalidate slice.
func runSpecFinal(deps Deps, state map[string]any, change string) error {
	reader := func(relative string) ([]byte, error) {
		return repository.StageHostReadFileBytes(filepath.Join(change, filepath.FromSlash(relative)))
	}
	checks, err := specvalidate.ValidateFinal(change, reader)
	if err != nil {
		return blockedf("Final spec lint could not run: %v", err)
	}
	errors := []string{}
	passed := true
	for _, check := range checks {
		if !check.Pass {
			errors = append(errors, check.Detail)
			passed = false
		}
	}
	inputs := map[string]any{
		"review_sha256":         nullableFileHash(filepath.Join(change, "review.json")),
		"reconciliation_sha256": nullableFileHash(filepath.Join(change, "review-reconciliation.json")),
		"final_spec_sha256":     nullableFileHash(filepath.Join(change, "spec.md")),
		"final_design_sha256":   nullableFileHash(filepath.Join(change, "design.md")),
		"original_task_sha256":  nullableFileHash(filepath.Join(change, "original-task.md")),
	}
	if err := writeJSON(filepath.Join(change, "final-validation.json"), map[string]any{
		"schema_version": int64(1),
		"checked_at_utc": deps.now().UTC().Format("2006-01-02T15:04:05.0000000Z"),
		"passed":         passed,
		"review_iteration": func() any {
			review, err := readJSONObject(filepath.Join(change, "review.json"))
			if err != nil {
				return nil
			}
			return review["review_iteration"]
		}(),
		"inputs": inputs,
		"errors": errors,
	}, true); err != nil {
		return err
	}
	if !passed {
		return blockedf("Final specification invariant validation failed. See: %s", filepath.Join(change, "final-validation.json"))
	}
	return nil
}

func nullableFileHash(path string) any {
	if !isRegularFile(path) {
		return nil
	}
	hash, err := hashFile(path)
	if err != nil {
		return nil
	}
	return hash
}

// invokeProfileSpecCritic ports the non-council half of
// Invoke-BFProfileSpecCritic: pre-lint, bounded inputs, the sealed critic
// dispatch through Invoke-BFManagedWorker and Complete-BSLFlowReview.
func invokeProfileSpecCritic(ctx context.Context, deps Deps, run *stageRun, raw string) (map[string]any, error) {
	state := run.state
	change, err := stageChangePath(state)
	if err != nil {
		return nil, err
	}
	configText := ""
	configPath := filepath.Join(asStringOr(state["project_path"]), "bsl-flow.yaml")
	if isRegularFile(configPath) {
		text, err := readRawText(configPath)
		if err != nil {
			return nil, err
		}
		configText = text
	}
	maxInput, err := yamlInteger(configText, []string{"review", "input", "max_file_bytes"}, 262144)
	if err != nil {
		return nil, err
	}
	if maxInput < 1024 || maxInput > 1048576 {
		return nil, invalidf("review input bound is outside the supported range.")
	}
	if enabled, err := councilRouteEnabled(configText); err != nil {
		return nil, err
	} else if enabled {
		return nil, blockedf("managed council spec review is not served by the native stage host yet.")
	}
	specBytes, err := repository.StageHostReadFileBytes(filepath.Join(change, "spec.md"))
	if err != nil {
		return nil, blockedf("%v", err)
	}
	findings, lintErr := specvalidate.LintSpec(specBytes)
	if lintErr != nil {
		return nil, lintErr
	}
	for _, finding := range findings {
		if finding.Severity == "error" {
			return nil, blockedf("specification lint failed before the profiled critic.")
		}
	}
	policy, err := parseReviewPolicy(configText)
	if err != nil {
		return nil, err
	}
	timeout, err := yamlInteger(configText, []string{"review", "runtime", "timeout_seconds"}, 600)
	if err != nil {
		return nil, err
	}
	maxOutput, err := yamlInteger(configText, []string{"review", "runtime", "max_output_bytes"}, 1048576)
	if err != nil {
		return nil, err
	}
	if timeout < 1 || timeout > 3600 || maxOutput < 65536 || maxOutput > 16777216 {
		return nil, invalidf("review input/runtime limits are outside the supported range.")
	}
	inputDirectory := filepath.Join(raw, "critic-inputs")
	if err := os.MkdirAll(inputDirectory, 0o755); err != nil {
		return nil, blockedf("%v", err)
	}
	blocks := []string{}
	inputs := map[string]any{}
	for _, name := range []string{"original-task.md", "spec.md", "design.md"} {
		path := filepath.Join(change, name)
		if name == "design.md" && !isRegularFile(path) {
			inputs[name] = nil
			continue
		}
		data, text, hash, err := boundedSnapshot(path, maxInput)
		if err != nil {
			return nil, err
		}
		inputs[name] = hash
		saved := filepath.Join(inputDirectory, name)
		if isRegularFile(saved) {
			existing, err := hashFile(saved)
			if err != nil {
				return nil, err
			}
			if existing != hash {
				return nil, blockedf("retained critic input differs; use a new attempt.")
			}
		} else if err := writeRawBytes(saved, data); err != nil {
			return nil, err
		}
		blocks = append(blocks, "<<<BEGIN UNTRUSTED DATA: "+name+">>>\n"+text+"\n<<<END UNTRUSTED DATA: "+name+">>>")
	}
	reviewRoot := filepath.Join(deps.SkillsRoot, "1c-spec-review")
	_, promptText, _, err := boundedSnapshot(filepath.Join(reviewRoot, "reviewer", "spec-reviewer-prompt.md"), maxInput)
	if err != nil {
		return nil, err
	}
	_, rubricText, _, err := boundedSnapshot(filepath.Join(reviewRoot, "references", "reviewer-rubric.md"), maxInput)
	if err != nil {
		return nil, err
	}
	prompt := promptText + "\n<<<BEGIN TRUSTED REVIEW POLICY: RUBRIC>>>\n" + rubricText + "\n<<<END TRUSTED REVIEW POLICY: RUBRIC>>>\n" +
		strings.Join(blocks, "\n\n") +
		"\nThis is an attached-only independent review. Do not use tools. Place the contracted review JSON object inside payload_json of the BSL Flow worker response; outer status completed means only that the review was produced, never that the task was accepted."
	criticState, err := cloneStateView(state)
	if err != nil {
		return nil, err
	}
	request := asMap(criticState["request"])
	deadline := int64(1800)
	if value, ok := asInteger(getValue(request, "timeout_seconds", int64(1800))); ok {
		deadline = value
	}
	if timeout < deadline {
		request["timeout_seconds"] = timeout
	} else {
		request["timeout_seconds"] = deadline
	}
	criticRun := &stageRun{state: criticState, attempt: run.attempt, directory: run.directory, contextRoot: run.contextRoot, cancel: run.cancel, providerContext: run.providerContext}
	directory := filepath.Join(raw, "critic")
	outcome, err := runManagedWorkerDispatch(ctx, deps, criticRun, "spec_review", prompt, directory)
	if err != nil {
		return nil, err
	}
	if outcome.Status != "completed" {
		return nil, blockedf("profiled independent critic did not return a completed review.")
	}
	payloadPath := filepath.Join(directory, "payload.json")
	if isRegularFile(payloadPath) {
		existing, err := readRawText(payloadPath)
		if err != nil {
			return nil, err
		}
		if existing != outcome.PayloadJSON {
			return nil, blockedf("cached critic payload differs from the verified model result.")
		}
	}
	rawReview, err := readStagePayload(outcome, directory)
	if err != nil {
		return nil, err
	}
	models := workerModelsFromState(asMap(state["request"]))
	review, err := completeReview(deps, rawReview,
		asStringOr(inputs["original-task.md"]), asStringOr(inputs["spec.md"]), asStringOr(inputs["design.md"]),
		"bsl-flow-spec-reviewer-sealed", models.Reviewer, policy)
	if err != nil {
		return nil, err
	}
	for name, declared := range inputs {
		path := filepath.Join(change, name)
		actual := any(nil)
		if isRegularFile(path) {
			if hash, err := hashFile(path); err == nil {
				actual = hash
			}
		}
		if actual != declared {
			return nil, blockedf("specification input changed during independent review.")
		}
	}
	if err := writeJSON(filepath.Join(change, "review.json"), review, true); err != nil {
		return nil, err
	}
	return review, nil
}
