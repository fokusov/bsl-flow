package specreview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bsl-flow/cli/internal/councilengine"
)

func rawReviewFixture() *councilengine.Ordered {
	return councilengine.OrderedFrom(
		[]string{"schema_version", "reviewer_verdict", "summary", "scores", "overengineering", "findings", "do_not_change", "confidence"},
		[]any{
			1, "REVISE", "evidence-based summary",
			councilengine.OrderedFrom(
				[]string{"intent_fidelity", "minimality", "completeness", "architecture_fit", "testability", "assumption_discipline", "clarity"},
				[]any{4, 3, 4, 4, 3, 4, 4},
			),
			councilengine.OrderedFrom([]string{"items"}, []any{
				[]any{
					councilengine.OrderedFrom([]string{"spec_ref", "item", "necessity", "evidence", "simpler_direction"}, []any{"Требуемое поведение / 1", "a", "required", "e", ""}),
					councilengine.OrderedFrom([]string{"spec_ref", "item", "necessity", "evidence", "simpler_direction"}, []any{"Требуемое поведение / 2", "b", "unjustified", "e", "drop it"}),
				},
			}),
			[]any{
				councilengine.OrderedFrom([]string{"id", "severity", "category", "spec_ref", "issue", "evidence", "suggested_direction"}, []any{"R-001", "high", "clarity", "Требуемое поведение / 2", "i", "e", "d"}),
			},
			[]any{"keep this part"},
			0.9,
		},
	)
}

func TestCompleteReviewDerivedMetrics(t *testing.T) {
	dir := t.TempDir()
	originalTask := filepath.Join(dir, "original-task.md")
	spec := filepath.Join(dir, "spec.md")
	if err := os.WriteFile(originalTask, []byte("task"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(spec, []byte("spec"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	policy := Policy{ReadMode: "read_search", PassWeightedScore: 4.3, BlockBelowWeightedScore: 3.5, MaxOverengineeringIndexForPass: 1, MaxUnjustifiedRatioForPass: 0}
	review, err := CompleteReview(rawReviewFixture(), CompleteInputs{
		OriginalTaskPath: originalTask, SpecPath: spec, DesignPath: "", Agent: "bsl-flow-spec-reviewer", Model: "deepseek/deepseek-v4-pro", Policy: policy,
	}, time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	// weighted = 4*0.25 + 3*0.20 + 4*0.15 + 4*0.15 + 3*0.10 + 4*0.10 + 4*0.05 = 1.0+0.6+0.6+0.6+0.3+0.4+0.2 = 3.7
	if review.Get("weighted_score") != 3.7 {
		t.Fatalf("weighted_score: %v", review.Get("weighted_score"))
	}
	overengineering, _ := review.Get("overengineering").(*councilengine.Ordered)
	if v, _ := asInt(overengineering.Get("architectural_decision_count")); v != 2 {
		t.Fatalf("decision count: %v", overengineering.Get("architectural_decision_count"))
	}
	if v, _ := asInt(overengineering.Get("unjustified_count")); v != 1 {
		t.Fatalf("unjustified count: %v", overengineering.Get("unjustified_count"))
	}
	// index = optional(0) + 3*unjustified(1) = 3
	if v, _ := asInt(overengineering.Get("index")); v != 3 {
		t.Fatalf("index: %v", overengineering.Get("index"))
	}
	// unjustified ratio = 1/2 = 0.5
	if v, _ := asFloat(overengineering.Get("unjustified_ratio")); v != 0.5 {
		t.Fatalf("unjustified ratio: %v", overengineering.Get("unjustified_ratio"))
	}
	// verdict: reviewer REVISE → REVISE (material finding high)
	if review.Get("verdict") != "REVISE" {
		t.Fatalf("verdict: %v", review.Get("verdict"))
	}
	if v, _ := asInt(review.Get("review_iteration")); v != 1 {
		t.Fatalf("review_iteration: %v", review.Get("review_iteration"))
	}
}

func TestCompleteReviewGatePass(t *testing.T) {
	dir := t.TempDir()
	originalTask := filepath.Join(dir, "original-task.md")
	spec := filepath.Join(dir, "spec.md")
	os.WriteFile(originalTask, []byte("task"), 0o644)
	os.WriteFile(spec, []byte("spec"), 0o644)
	raw := councilengine.OrderedFrom(
		[]string{"schema_version", "reviewer_verdict", "summary", "scores", "overengineering", "findings", "do_not_change", "confidence"},
		[]any{
			1, "PASS", "clean",
			councilengine.OrderedFrom([]string{"intent_fidelity", "minimality", "completeness", "architecture_fit", "testability", "assumption_discipline", "clarity"},
				[]any{5, 5, 5, 5, 5, 5, 5}),
			councilengine.OrderedFrom([]string{"items"}, []any{[]any{}}),
			[]any{}, []any{}, 1.0,
		},
	)
	policy := Policy{ReadMode: "read_search", PassWeightedScore: 4.3, BlockBelowWeightedScore: 3.5, MaxOverengineeringIndexForPass: 1, MaxUnjustifiedRatioForPass: 0}
	review, err := CompleteReview(raw, CompleteInputs{OriginalTaskPath: originalTask, SpecPath: spec, Agent: "bsl-flow-spec-reviewer", Model: "m/v", Policy: policy}, time.Now())
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if review.Get("verdict") != "PASS" {
		t.Fatalf("verdict: %v", review.Get("verdict"))
	}
}

func TestReviewPayloadFromOpenCodeText(t *testing.T) {
	// Fenced block with prose outside.
	text := "Here is the review:\n```json\n{\"schema_version\":1,\"reviewer_verdict\":\"PASS\"}\n```\nDone."
	payload, err := ReviewPayloadFromOpenCodeText(text)
	if err != nil {
		t.Fatalf("fenced: %v", err)
	}
	if payload.Get("schema_version") != json.Number("1") {
		t.Fatalf("payload: %v", payload.Get("schema_version"))
	}
	// Plain object.
	plain, err := ReviewPayloadFromOpenCodeText(`{"schema_version":1}`)
	if err != nil || plain.Get("schema_version") != json.Number("1") {
		t.Fatalf("plain: %v", err)
	}
	// Array is rejected.
	if _, err := ReviewPayloadFromOpenCodeText(`[1,2]`); err == nil {
		t.Fatal("array must be rejected")
	}
	// Multiple fences are ambiguous.
	if _, err := ReviewPayloadFromOpenCodeText("```json\n{}\n```\n```json\n{}\n```"); err == nil {
		t.Fatal("multiple fences must be rejected")
	}
	// Structured candidate outside the fence is rejected.
	if _, err := ReviewPayloadFromOpenCodeText("```json\n{}\n```\n{\"extra\":1}"); err == nil {
		t.Fatal("outside structured candidate must be rejected")
	}
}

func TestReviewFromOpenCodeEvents(t *testing.T) {
	events := []string{
		`{"type":"text","part":{"text":"{\"schema_version\":"}}`,
		`{"type":"text","part":{"text":"1}"}}`,
	}
	payload, err := ReviewFromOpenCodeEvents(events)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if payload.Get("schema_version") != json.Number("1") {
		t.Fatalf("payload: %v", payload.Get("schema_version"))
	}
	// error event.
	if _, err := ReviewFromOpenCodeEvents([]string{`{"type":"error","part":{}}`}); err == nil {
		t.Fatal("error event must fail")
	}
}

func TestRecordMetric(t *testing.T) {
	dir := t.TempDir()
	changeDir := filepath.Join(dir, "openspec", "changes", "demo")
	if err := os.MkdirAll(changeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	review := councilengine.OrderedFrom(
		[]string{"schema_version", "verdict", "reviewer_verdict", "chair", "findings"},
		[]any{1, "PASS", "PASS", councilengine.OrderedFrom([]string{"verdict"}, []any{"PASS"}), []any{}},
	)
	reviewBytes, _ := councilengine.ConvertToJSON(review, 20)
	os.WriteFile(filepath.Join(changeDir, "review.json"), reviewBytes, 0o644)
	reconciliation := councilengine.OrderedFrom([]string{"decisions"}, []any{[]any{
		councilengine.OrderedFrom([]string{"finding_id", "decision"}, []any{"R-001", "accepted"}),
	}})
	recBytes, _ := councilengine.ConvertToJSON(reconciliation, 20)
	os.WriteFile(filepath.Join(changeDir, "review-reconciliation.json"), recBytes, 0o644)
	validation := councilengine.OrderedFrom([]string{"passed"}, []any{true})
	valBytes, _ := councilengine.ConvertToJSON(validation, 20)
	os.WriteFile(filepath.Join(changeDir, "final-validation.json"), valBytes, 0o644)
	specText := "# T\n\n## Классификация\n- Сложность: S\n- Риск: low\n\n## Требуемое поведение\n1. x\n"
	os.WriteFile(filepath.Join(changeDir, "spec.md"), []byte(specText), 0o644)

	metricsPath := filepath.Join(dir, "metrics.jsonl")
	result, err := RecordMetric(MetricInputs{ProjectRoot: dir, ChangeName: "demo", MetricsPath: metricsPath}, time.Now)
	if err != nil {
		t.Fatalf("metric: %v", err)
	}
	if result.RunID == "" {
		t.Fatal("run_id must be present")
	}
	data, _ := os.ReadFile(metricsPath)
	if !strings.Contains(string(data), `"run_id":"`+result.RunID+`"`) {
		t.Fatalf("metrics line: %s", string(data))
	}
	// Dedup: the same run_id must be rejected.
	if _, err := RecordMetric(MetricInputs{ProjectRoot: dir, ChangeName: "demo", MetricsPath: metricsPath}, time.Now); err == nil {
		t.Fatal("duplicate run_id must be rejected")
	}
}
