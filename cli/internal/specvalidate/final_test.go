package specvalidate

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
)

const finalChangeDir = `C:\project\openspec\changes\demo`

func mapRead(files map[string][]byte) func(string) ([]byte, error) {
	return func(rel string) ([]byte, error) {
		if data, ok := files[rel]; ok {
			return data, nil
		}
		return nil, fs.ErrNotExist
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func runFinal(t *testing.T, files map[string][]byte) []FinalCheck {
	t.Helper()
	checks, err := ValidateFinal(finalChangeDir, mapRead(files))
	if err != nil {
		t.Fatalf("ValidateFinal returned error: %v", err)
	}
	return checks
}

func checkNames(checks []FinalCheck) []string {
	names := make([]string, 0, len(checks))
	for _, check := range checks {
		names = append(names, check.Name)
	}
	return names
}

func failingNames(checks []FinalCheck) []string {
	names := []string{}
	for _, check := range checks {
		if !check.Pass {
			names = append(names, check.Name)
		}
	}
	return names
}

func assertAllPass(t *testing.T, checks []FinalCheck) []string {
	t.Helper()
	if failures := failingNames(checks); len(failures) > 0 {
		t.Fatalf("expected all checks to pass, failing: %v", failures)
	}
	return checkNames(checks)
}

func assertFailing(t *testing.T, checks []FinalCheck, expected ...string) {
	t.Helper()
	actual := strings.Join(failingNames(checks), "\n")
	if actual != strings.Join(expected, "\n") {
		t.Fatalf("failing checks mismatch\nexpected:\n  %s\ngot:\n  %s", strings.Join(expected, "\n  "), strings.Join(failingNames(checks), "\n  "))
	}
}

func newV1Review(spec, original []byte) map[string]any {
	return map[string]any{
		"schema_version":    1,
		"reviewed_at_utc":   "2026-09-01T10:00:00Z",
		"review_iteration":  1,
		"reviewer_verdict":  "PASS",
		"verdict":           "PASS",
		"summary":           "Consistent with the task.",
		"scores":            map[string]any{"intent_fidelity": 5, "minimality": 5, "completeness": 5, "architecture_fit": 5, "testability": 5, "assumption_discipline": 5, "clarity": 5},
		"weighted_score":    5.0,
		"overengineering":   map[string]any{"architectural_decision_count": 0, "required_count": 0, "justified_count": 0, "optional_count": 0, "unjustified_count": 0, "index": 0, "optional_ratio": 0.0, "unjustified_ratio": 0.0, "normalized_index": 0.0, "items": []any{}},
		"blocking_findings": []any{},
		"findings": []any{map[string]any{
			"id": "R-001", "severity": "low", "category": "clarity",
			"spec_ref": "Цель / 1", "issue": "Minor wording.", "evidence": "Spec text.",
			"suggested_direction": "Tighten wording.",
		}},
		"do_not_change": []any{},
		"confidence":    0.9,
		"reviewer":      map[string]any{"provider": "opencode", "agent": "build", "model": "test-model"},
		"inputs": map[string]any{
			"original_task_sha256": sha256Hex(original),
			"spec_sha256":          sha256Hex(spec),
			"design_sha256":        nil,
		},
		"gate": map[string]any{"pass_weighted_score": 4.0, "block_below_weighted_score": 3.5, "max_overengineering_index_for_pass": 1, "max_unjustified_ratio_for_pass": 0.0},
	}
}

func newV1Reconciliation(reviewBytes []byte, review map[string]any, spec []byte) map[string]any {
	inputs := review["inputs"].(map[string]any)
	return map[string]any{
		"schema_version":      1,
		"review_sha256":       sha256Hex(reviewBytes),
		"draft_spec_sha256":   inputs["spec_sha256"],
		"final_spec_sha256":   sha256Hex(spec),
		"draft_design_sha256": nil,
		"final_design_sha256": nil,
		"reconciled_at_utc":   "2026-09-01T11:00:00Z",
		"summary":             "All findings reconciled.",
		"decisions": []any{map[string]any{
			"finding_id": "R-001", "decision": "rejected", "reason": "Not material.",
			"evidence": "Checked the goal text.", "status": "not_applicable",
			"resolution": "Kept as is.", "spec_ref_after": "Цель / 1",
		}},
		"do_not_change_checks": []any{},
	}
}

func buildV1Change(t *testing.T, mutateReview, mutateReconciliation func(map[string]any), spec []byte, omit ...string) map[string][]byte {
	t.Helper()
	original := []byte("Original task text.\n")
	review := newV1Review(spec, original)
	if mutateReview != nil {
		mutateReview(review)
	}
	reviewBytes := mustMarshal(t, review)
	reconciliation := newV1Reconciliation(reviewBytes, review, spec)
	if mutateReconciliation != nil {
		mutateReconciliation(reconciliation)
	}
	files := map[string][]byte{
		"review.json":                reviewBytes,
		"spec.md":                    spec,
		"original-task.md":           original,
		"review-reconciliation.json": mustMarshal(t, reconciliation),
	}
	for _, name := range omit {
		delete(files, name)
	}
	return files
}

func TestValidateFinalV1HappyPath(t *testing.T) {
	files := buildV1Change(t, nil, nil, []byte(lintBaselineSpec))
	names := assertAllPass(t, runFinal(t, files))
	expected := strings.Join([]string{
		"input-review.json", "input-spec.md", "input-original-task.md", "input-review-reconciliation.json",
		"review-schema", "reconciliation-schema", "review-iteration",
		"binding-review-hash", "binding-draft-spec", "binding-final-spec", "binding-draft-design", "binding-final-design", "binding-original-task",
		"findings-unique", "finding-reconciled[R-001]", "decision-value[R-001]", "decision-fields[R-001]", "decision-status[R-001]",
		"accepted-changes", "final-spec-lint",
	}, "\n")
	if strings.Join(names, "\n") != expected {
		t.Fatalf("check order mismatch\nexpected:\n  %s\ngot:\n  %s", expected, strings.Join(names, "\n  "))
	}
}

func TestValidateFinalV1MissingReviewArtifact(t *testing.T) {
	files := buildV1Change(t, nil, nil, []byte(lintBaselineSpec), "review.json")
	assertFailing(t, runFinal(t, files), "input-review.json")
}

func TestValidateFinalV1MissingReconciliationArtifact(t *testing.T) {
	files := buildV1Change(t, nil, nil, []byte(lintBaselineSpec), "review-reconciliation.json")
	assertFailing(t, runFinal(t, files), "input-review-reconciliation.json")
}

func TestValidateFinalV1SpecLintFailure(t *testing.T) {
	// The review hashes are bound to this spec, so only the lint check fails.
	files := buildV1Change(t, nil, nil, []byte("# broken\n"))
	assertFailing(t, runFinal(t, files), "final-spec-lint")
}

func TestValidateFinalV1SpecChangedAfterReview(t *testing.T) {
	files := buildV1Change(t, nil, nil, []byte(lintBaselineSpec))
	files["spec.md"] = []byte(strings.Replace(lintBaselineSpec, "Исправить ошибку", "Исправим ошибку", 1))
	assertFailing(t, runFinal(t, files), "binding-final-spec")
}

func TestValidateFinalV1UnreconciledFinding(t *testing.T) {
	files := buildV1Change(t, nil, func(reconciliation map[string]any) {
		reconciliation["decisions"] = []any{}
	}, []byte(lintBaselineSpec))
	assertFailing(t, runFinal(t, files), "finding-reconciled[R-001]")
}

func TestValidateFinalV1UnknownReconciledFinding(t *testing.T) {
	files := buildV1Change(t, nil, func(reconciliation map[string]any) {
		reconciliation["decisions"] = append(reconciliation["decisions"].([]any), map[string]any{
			"finding_id": "R-009", "decision": "rejected", "reason": "Not present.",
			"evidence": "No such finding.", "status": "not_applicable",
			"resolution": "Ignored.", "spec_ref_after": "Цель / 1",
		})
	}, []byte(lintBaselineSpec))
	assertFailing(t, runFinal(t, files), "decision-known[R-009]")
}

func TestValidateFinalV1DuplicateDecision(t *testing.T) {
	files := buildV1Change(t, nil, func(reconciliation map[string]any) {
		reconciliation["decisions"] = append(reconciliation["decisions"].([]any), map[string]any{
			"finding_id": "R-001", "decision": "rejected", "reason": "Again.",
			"evidence": "Duplicate.", "status": "not_applicable",
			"resolution": "Kept.", "spec_ref_after": "Цель / 1",
		})
	}, []byte(lintBaselineSpec))
	assertFailing(t, runFinal(t, files), "findings-unique", "finding-reconciled[R-001]")
}

func TestValidateFinalV1AcceptedFindingNotAddressed(t *testing.T) {
	files := buildV1Change(t, nil, func(reconciliation map[string]any) {
		decision := reconciliation["decisions"].([]any)[0].(map[string]any)
		decision["decision"] = "accepted"
	}, []byte(lintBaselineSpec))
	assertFailing(t, runFinal(t, files), "decision-status[R-001]", "accepted-changes")
}

func TestValidateFinalV1DoNotChangeNotReconciled(t *testing.T) {
	files := buildV1Change(t, func(review map[string]any) {
		review["do_not_change"] = []any{"Не менять структуру метаданных"}
	}, nil, []byte(lintBaselineSpec))
	assertFailing(t, runFinal(t, files), "do-not-change-reconciled[Не менять структуру метаданных]")
}

func TestValidateFinalV1SecondReviewIteration(t *testing.T) {
	// The completed-review assert already rejects an iteration other than 1
	// (Review.Common.ps1:179), so the defensive semantic check of
	// Test-1CSpecFinal.ps1:102 stays green behind it.
	files := buildV1Change(t, func(review map[string]any) {
		review["review_iteration"] = 2
	}, nil, []byte(lintBaselineSpec))
	checks := runFinal(t, files)
	assertFailing(t, checks, "review-schema")
	for _, check := range checks {
		if check.Name == "review-schema" && check.Detail != "Invalid review.json: review_iteration must be 1." {
			t.Fatalf("unexpected detail: %q", check.Detail)
		}
	}
}

func TestValidateFinalV1InvalidReviewSchema(t *testing.T) {
	files := buildV1Change(t, func(review map[string]any) {
		review["verdict"] = "MAYBE"
	}, nil, []byte(lintBaselineSpec))
	checks := runFinal(t, files)
	assertFailing(t, checks, "review-schema")
	for _, check := range checks {
		if check.Name == "review-schema" && check.Detail != "Invalid review.json: Invalid gate verdict." {
			t.Fatalf("unexpected detail: %q", check.Detail)
		}
	}
}

func newCouncilReview(spec, original []byte) map[string]any {
	return map[string]any{
		"schema_version":         2,
		"reviewed_at_utc":        "2026-09-01T10:00:00Z",
		"council_schema_version": 1,
		"verdict":                "PASS",
		"diversity":              "multi_role_single_model",
		"fallback_visible":       false,
		"inputs": map[string]any{
			"original_task_sha256": sha256Hex(original),
			"spec_sha256":          sha256Hex(spec),
			"design_sha256":        nil,
			"policy_hash":          strings.Repeat("a", 64),
		},
		"manifest": map[string]any{"version": 1, "requirements": []any{map[string]any{
			"id": "REQ-001", "source_hash": strings.Repeat("b", 64), "draft_ref": "Требуемое поведение / 1",
		}}},
		"members": []any{
			map[string]any{"role": "intent_critic", "status": "completed"},
			map[string]any{"role": "chair", "status": "completed"},
		},
		"findings":  []any{},
		"protected": []any{},
		"questions": []any{},
		"chair": map[string]any{
			"verdict": "PASS", "decisions": []any{}, "protected_decisions": []any{}, "requirement_refs": []any{},
			"final_spec_text": string(spec), "final_design_text": nil,
		},
		"reconciliation": map[string]any{
			"review_sha256": strings.Repeat("c", 64), "draft_spec_sha256": sha256Hex(spec),
			"final_spec_sha256": sha256Hex(spec), "draft_design_sha256": nil, "final_design_sha256": nil,
		},
		"gate": map[string]any{"structural_only": true, "passed": true},
	}
}

func buildV2Change(t *testing.T, mutate func(map[string]any), spec []byte, omit ...string) map[string][]byte {
	t.Helper()
	original := []byte("Original council task.\n")
	review := newCouncilReview(spec, original)
	if mutate != nil {
		mutate(review)
	}
	files := map[string][]byte{
		"review.json":      mustMarshal(t, review),
		"spec.md":          spec,
		"original-task.md": original,
	}
	for _, name := range omit {
		delete(files, name)
	}
	return files
}

func TestValidateFinalV2HappyPath(t *testing.T) {
	files := buildV2Change(t, nil, []byte(lintBaselineSpec))
	names := assertAllPass(t, runFinal(t, files))
	expected := strings.Join([]string{
		"input-review.json", "input-spec.md", "input-original-task.md",
		"council-review-schema", "final-spec-lint",
		"binding-original-task", "binding-final-spec", "binding-draft-spec", "binding-final-design",
		"council-verdict", "final-spec-text", "verdict-pass", "gate-passed",
	}, "\n")
	if strings.Join(names, "\n") != expected {
		t.Fatalf("check order mismatch\nexpected:\n  %s\ngot:\n  %s", expected, strings.Join(names, "\n  "))
	}
}

func TestValidateFinalV2ReviseVerdict(t *testing.T) {
	files := buildV2Change(t, func(review map[string]any) {
		review["verdict"] = "REVISE"
		// Keep the non-PASS decision rule satisfied so only the controller
		// verdict contract fails.
		chair := review["chair"].(map[string]any)
		chair["decisions"] = []any{map[string]any{"composite_id": "intent_critic:F-001"}}
	}, []byte(lintBaselineSpec))
	assertFailing(t, runFinal(t, files), "verdict-pass")
}

func TestValidateFinalV2GateNotPassed(t *testing.T) {
	files := buildV2Change(t, func(review map[string]any) {
		review["gate"].(map[string]any)["passed"] = false
	}, []byte(lintBaselineSpec))
	assertFailing(t, runFinal(t, files), "gate-passed")
}

func TestValidateFinalV2ChairTextMismatch(t *testing.T) {
	files := buildV2Change(t, func(review map[string]any) {
		review["chair"].(map[string]any)["final_spec_text"] = "Another specification entirely."
	}, []byte(lintBaselineSpec))
	assertFailing(t, runFinal(t, files), "final-spec-text")
}

func TestValidateFinalV2DegradedDiversityCannotPass(t *testing.T) {
	files := buildV2Change(t, func(review map[string]any) {
		review["diversity"] = "degraded"
		review["members"] = []any{
			map[string]any{"role": "intent_critic", "status": "invalid_response"},
			map[string]any{"role": "chair", "status": "completed"},
		}
	}, []byte(lintBaselineSpec))
	checks := runFinal(t, files)
	assertFailing(t, checks, "council-verdict")
	for _, check := range checks {
		if check.Name == "council-verdict" && check.Detail != "Degraded council cannot PASS.; Council with a terminal member failure cannot PASS." {
			t.Fatalf("unexpected detail: %q", check.Detail)
		}
	}
}

func TestValidateFinalV2MissingOriginalTask(t *testing.T) {
	// Without every shared input the dual reader falls back to the legacy
	// branch, which demands the reconciliation sidecar (Test-1CSpecFinal.ps1:86-88).
	files := buildV2Change(t, nil, []byte(lintBaselineSpec), "original-task.md")
	assertFailing(t, runFinal(t, files), "input-original-task.md", "input-review-reconciliation.json")
}

func TestValidateFinalV2SpecChangedAfterReview(t *testing.T) {
	files := buildV2Change(t, nil, []byte(lintBaselineSpec))
	files["spec.md"] = []byte(strings.Replace(lintBaselineSpec, "Исправить ошибку", "Исправим ошибку", 1))
	assertFailing(t, runFinal(t, files), "binding-final-spec", "final-spec-text")
}

func TestValidateFinalRequiresReadFunction(t *testing.T) {
	if _, err := ValidateFinal(finalChangeDir, nil); err == nil {
		t.Fatal("expected an error for a nil read function")
	}
}

func TestValidateFinalDeterministicAcrossRuns(t *testing.T) {
	files := buildV1Change(t, nil, nil, []byte(lintBaselineSpec))
	first := runFinal(t, files)
	for i := 0; i < 20; i++ {
		again := runFinal(t, files)
		if strings.Join(checkNames(first), "\n") != strings.Join(checkNames(again), "\n") {
			t.Fatalf("final validation is not deterministic:\n%+v\nvs\n%+v", first, again)
		}
	}
}
