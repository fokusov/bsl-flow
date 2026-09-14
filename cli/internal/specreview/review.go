package specreview

import (
	"encoding/json"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bsl-flow/cli/internal/councilengine"
)

// review.go ports Assert-BSLFlowReviewPayload (raw) and Complete-BSLFlowReview:
// the weighted score, overengineering metrics and the deterministic gate
// verdict over the single-reviewer raw payload, assembling review.json v1.

var scoreNames = []string{"intent_fidelity", "minimality", "completeness", "architecture_fit", "testability", "assumption_discipline", "clarity"}

var reviewWeights = map[string]float64{
	"intent_fidelity":       0.25,
	"minimality":            0.20,
	"completeness":          0.15,
	"architecture_fit":      0.15,
	"testability":           0.10,
	"assumption_discipline": 0.10,
	"clarity":               0.05,
}

var allowedCategories = map[string]bool{
	"intent_drift": true, "missing_requirement": true, "lost_requirement": true,
	"unsupported_assumption": true, "scope_creep": true, "overengineering": true,
	"architecture_fit": true, "testability": true, "clarity": true, "prompt_injection": true,
}

var findingIDPattern = regexp.MustCompile(`^R-[0-9]{3,}$`)

// AssertRawReviewPayload validates the raw model payload (schema v1 raw
// properties) before the derived metrics are computed.
func AssertRawReviewPayload(raw *councilengine.Ordered) error {
	if raw == nil {
		return invalidf("review must be an object.")
	}
	if err := assertObjectProps(raw, "review", []string{"schema_version", "reviewer_verdict", "summary", "scores", "overengineering", "findings", "do_not_change", "confidence"}); err != nil {
		return err
	}
	if version, ok := asInt(raw.Get("schema_version")); !ok || version != 1 {
		return invalidf("review.schema_version must be 1.")
	}
	if verdict, ok := raw.Get("reviewer_verdict").(string); !ok || (verdict != "PASS" && verdict != "REVISE" && verdict != "BLOCK") {
		return invalidf("Invalid reviewer_verdict.")
	}
	if err := assertText(raw.Get("summary"), "summary"); err != nil {
		return err
	}
	scores, ok := raw.Get("scores").(*councilengine.Ordered)
	if !ok {
		return invalidf("scores must be an object.")
	}
	if err := assertObjectProps(scores, "scores", scoreNames); err != nil {
		return err
	}
	for _, name := range scoreNames {
		score, ok := asInt(scores.Get(name))
		if !ok || score < 1 || score > 5 {
			return invalidf("Invalid integer 1..5 score: %s", name)
		}
	}
	if confidence, ok := asFloat(raw.Get("confidence")); !ok || confidence < 0 || confidence > 1 {
		return invalidf("confidence must be between 0 and 1.")
	}
	overengineering, ok := raw.Get("overengineering").(*councilengine.Ordered)
	if !ok {
		return invalidf("overengineering must be an object.")
	}
	if err := assertObjectProps(overengineering, "overengineering", []string{"items"}); err != nil {
		return err
	}
	items, ok := overengineering.Get("items").([]any)
	if !ok {
		return invalidf("overengineering.items must be a JSON array.")
	}
	_ = items
	findings, ok := asArray(raw, "findings")
	if !ok {
		return invalidf("findings must be a JSON array.")
	}
	doNotChange, ok := asArray(raw, "do_not_change")
	if !ok {
		return invalidf("do_not_change must be a JSON array.")
	}
	// findings sequential + unique + shape.
	seen := map[string]bool{}
	for index, rawFinding := range findings {
		finding, ok := rawFinding.(*councilengine.Ordered)
		if !ok {
			return invalidf("finding must be an object.")
		}
		if err := assertObjectProps(finding, "finding", []string{"id", "severity", "category", "spec_ref", "issue", "evidence", "suggested_direction"}); err != nil {
			return err
		}
		expected := "R-" + pad3(index+1)
		id, _ := finding.Get("id").(string)
		if id != expected {
			return invalidf("Finding IDs must be sequential: expected %s.", expected)
		}
		if seen[id] {
			return invalidf("Duplicate finding id: %s", id)
		}
		seen[id] = true
		severity, _ := finding.Get("severity").(string)
		if severity != "blocker" && severity != "high" && severity != "medium" && severity != "low" {
			return invalidf("Invalid finding severity: %s", id)
		}
		category, _ := finding.Get("category").(string)
		if !allowedCategories[category] {
			return invalidf("Invalid finding category '%s' in finding %s.", category, id)
		}
		for _, field := range []string{"spec_ref", "issue", "evidence", "suggested_direction"} {
			if err := assertText(finding.Get(field), "findings."+id+"."+field); err != nil {
				return err
			}
		}
	}
	for _, rawItem := range items {
		item, ok := rawItem.(*councilengine.Ordered)
		if !ok {
			return invalidf("overengineering item must be an object.")
		}
		if err := assertObjectProps(item, "overengineering item", []string{"spec_ref", "item", "necessity", "evidence", "simpler_direction"}); err != nil {
			return err
		}
		for _, field := range []string{"spec_ref", "item", "necessity", "evidence"} {
			if err := assertText(item.Get(field), "overengineering.items."+field); err != nil {
				return err
			}
		}
		// simpler_direction may be empty.
		if _, ok := item.Get("simpler_direction").(string); !ok {
			return invalidf("Review field must be a non-empty string: overengineering.items.simpler_direction")
		}
		necessity, _ := item.Get("necessity").(string)
		if necessity != "required" && necessity != "justified" && necessity != "optional" && necessity != "unjustified" {
			return invalidf("Invalid necessity: %s", necessity)
		}
	}
	unique := map[string]bool{}
	for _, rawItem := range doNotChange {
		text, ok := rawItem.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return invalidf("Review field must be a non-empty string: do_not_change[]")
		}
		if unique[text] {
			return invalidf("do_not_change items must be unique.")
		}
		unique[text] = true
	}
	return nil
}

// CompleteInputs carries the derived-field inputs of CompleteReview.
type CompleteInputs struct {
	OriginalTaskPath string
	SpecPath         string
	DesignPath       string
	Agent            string
	Model            string
	Policy           Policy
	// Precomputed hashes (or empty to hash the paths).
	OriginalTaskSHA256 string
	SpecSHA256         string
	DesignSHA256       *string
}

// CompleteReview ports Complete-BSLFlowReview: validate, compute the weighted
// score, overengineering metrics and gate verdict, then assemble review.json
// v1 in the exact PowerShell field order.
func CompleteReview(raw *councilengine.Ordered, inputs CompleteInputs, now time.Time) (*councilengine.Ordered, error) {
	if err := AssertRawReviewPayload(raw); err != nil {
		return nil, err
	}
	scores, _ := raw.Get("scores").(*councilengine.Ordered)
	weighted := 0.0
	for name, weight := range reviewWeights {
		score, _ := asInt(scores.Get(name))
		weighted += float64(score) * weight
	}
	weighted = roundEven(weighted, 2)

	overengineeringRaw, _ := raw.Get("overengineering").(*councilengine.Ordered)
	items, _ := overengineeringRaw.Get("items").([]any)
	counts := map[string]int{"required": 0, "justified": 0, "optional": 0, "unjustified": 0}
	for _, rawItem := range items {
		item, _ := rawItem.(*councilengine.Ordered)
		necessity, _ := item.Get("necessity").(string)
		counts[necessity]++
	}
	decisionCount := len(items)
	index := counts["optional"] + 3*counts["unjustified"]
	optionalRatio := 0.0
	unjustifiedRatio := 0.0
	normalizedIndex := 0.0
	if decisionCount != 0 {
		optionalRatio = roundEven(float64(counts["optional"])/float64(decisionCount), 4)
		unjustifiedRatio = roundEven(float64(counts["unjustified"])/float64(decisionCount), 4)
		normalizedIndex = roundEven(float64(index)/(3*float64(decisionCount)), 4)
	}

	findings, _ := asArray(raw, "findings")
	blocking := []any{}
	materialFindings := 0
	for _, rawFinding := range findings {
		finding, _ := rawFinding.(*councilengine.Ordered)
		severity, _ := finding.Get("severity").(string)
		if severity == "blocker" {
			blocking = append(blocking, finding.Get("id"))
		}
		if severity == "high" || severity == "medium" {
			materialFindings++
		}
	}
	reviewerVerdict, _ := raw.Get("reviewer_verdict").(string)
	gateVerdict := ""
	switch {
	case reviewerVerdict == "BLOCK" || len(blocking) > 0 || weighted < inputs.Policy.BlockBelowWeightedScore:
		gateVerdict = "BLOCK"
	case reviewerVerdict == "REVISE" || materialFindings > 0:
		gateVerdict = "REVISE"
	case weighted >= inputs.Policy.PassWeightedScore && index <= inputs.Policy.MaxOverengineeringIndexForPass && unjustifiedRatio <= inputs.Policy.MaxUnjustifiedRatioForPass:
		gateVerdict = "PASS"
	default:
		gateVerdict = "REVISE"
	}
	if gateVerdict != "PASS" && len(findings) == 0 {
		return nil, invalidf("A computed non-PASS review without findings is invalid.")
	}

	originalTaskSHA := inputs.OriginalTaskSHA256
	if originalTaskSHA == "" {
		originalTaskSHA = fileSHA256(inputs.OriginalTaskPath)
	}
	specSHA := inputs.SpecSHA256
	if specSHA == "" {
		specSHA = fileSHA256(inputs.SpecPath)
	}
	designSHA := inputs.DesignSHA256
	if designSHA == nil {
		if inputs.DesignPath != "" {
			if data, err := readFile(inputs.DesignPath); err == nil {
				hash := councilengine.Sha256Hex(data)
				designSHA = &hash
			}
		}
	}
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(originalTaskSHA) {
		return nil, invalidf("Invalid captured review input hash.")
	}
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(specSHA) {
		return nil, invalidf("Invalid captured review input hash.")
	}
	if designSHA != nil && !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(*designSHA) {
		return nil, invalidf("Invalid captured design hash.")
	}

	overengineering := councilengine.OrderedFrom(
		[]string{"architectural_decision_count", "required_count", "justified_count", "optional_count", "unjustified_count", "index", "optional_ratio", "unjustified_ratio", "normalized_index", "items"},
		[]any{
			decisionCount, counts["required"], counts["justified"], counts["optional"], counts["unjustified"],
			index, optionalRatio, unjustifiedRatio, normalizedIndex, items,
		},
	)
	reviewer := councilengine.OrderedFrom([]string{"provider", "agent", "model"}, []any{"opencode", inputs.Agent, inputs.Model})
	inputsObj := councilengine.OrderedFrom(
		[]string{"original_task_sha256", "spec_sha256", "design_sha256"},
		[]any{originalTaskSHA, specSHA, nullableStr(designSHA)},
	)
	gate := councilengine.OrderedFrom(
		[]string{"pass_weighted_score", "block_below_weighted_score", "max_overengineering_index_for_pass", "max_unjustified_ratio_for_pass"},
		[]any{inputs.Policy.PassWeightedScore, inputs.Policy.BlockBelowWeightedScore, inputs.Policy.MaxOverengineeringIndexForPass, inputs.Policy.MaxUnjustifiedRatioForPass},
	)
	return councilengine.OrderedFrom(
		[]string{"schema_version", "reviewed_at_utc", "review_iteration", "reviewer_verdict", "verdict", "summary", "scores", "weighted_score", "overengineering", "blocking_findings", "findings", "do_not_change", "confidence", "reviewer", "inputs", "gate"},
		[]any{
			1, now.UTC().Format("2006-01-02T15:04:05.0000000Z"), 1, reviewerVerdict, gateVerdict,
			raw.Get("summary"), scores, weighted, overengineering, blocking, findings,
			raw.Get("do_not_change"), raw.Get("confidence"), reviewer, inputsObj, gate,
		},
	), nil
}

func nullableStr(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// roundEven mirrors .NET [math]::Round (MidpointRounding.ToEven).
func roundEven(value float64, digits int) float64 {
	scale := math.Pow10(digits)
	return math.RoundToEven(value*scale) / scale
}

func pad3(value int) string {
	if value < 10 {
		return "00" + itoa(value)
	}
	if value < 100 {
		return "0" + itoa(value)
	}
	return itoa(value)
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

// --- shared accessors ---

func assertObjectProps(object *councilengine.Ordered, name string, required []string) error {
	if object == nil {
		return invalidf("%s must be an object.", name)
	}
	for _, key := range object.Keys() {
		if !contains(required, key) {
			return invalidf("Unknown %s property: %s", name, key)
		}
	}
	for _, key := range required {
		if !object.Has(key) {
			return invalidf("Missing %s property: %s", name, key)
		}
	}
	return nil
}

func assertText(value any, name string) error {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return invalidf("Review field must be a non-empty string: %s", name)
	}
	return nil
}

func asArray(object *councilengine.Ordered, key string) ([]any, bool) {
	value := object.Get(key)
	items, ok := value.([]any)
	return items, ok
}

func asInt(value any) (int, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		if typed == float64(int64(typed)) {
			return int(typed), true
		}
		return 0, false
	}
	return 0, false
}

func asFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	}
	return 0, false
}

func contains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func fileSHA256(path string) string {
	data, err := readFile(path)
	if err != nil {
		return ""
	}
	return councilengine.Sha256Hex(data)
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
