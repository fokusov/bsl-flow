package councilengine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// validation_test.go ports the manifest and payload checks of
// Test-CouncilValidation.ps1 and Test-CouncilEngine.ps1 against the real
// council spec and recorded pwsh manifest hashes.

func TestRequirementManifestAgainstRealSpec(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join("..", "..", "..", "openspec", "changes", "api-specification-council", "spec.md"))
	if err != nil {
		t.Skip("council spec unavailable")
	}
	manifest, err := NewRequirementManifest(string(spec))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	requirements, _ := asArray(manifest.get("requirements"))
	if len(requirements) != 26 {
		t.Fatalf("manifest count: %d", len(requirements))
	}
	first, _ := asOrdered(requirements[0])
	if asStringOr(first.get("id")) != "REQ-001" {
		t.Fatalf("first id: %s", asStringOr(first.get("id")))
	}
	if asStringOr(first.get("source_hash")) != "cbb71d6e5eabf8a0a17c553ed2fc11ebd56937609fae47beef5c1a57cc72b145" {
		t.Fatalf("first source_hash: %s", asStringOr(first.get("source_hash")))
	}
	last, _ := asOrdered(requirements[len(requirements)-1])
	if asStringOr(last.get("id")) != "REQ-026" {
		t.Fatalf("last id: %s", asStringOr(last.get("id")))
	}
	if asStringOr(last.get("source_hash")) != "960ee331b60e114a38c68447c568b95f563439fb6abfd6a34949166c7599ef70" {
		t.Fatalf("last source_hash: %s", asStringOr(last.get("source_hash")))
	}
}

func TestRequirementManifestErrors(t *testing.T) {
	if _, err := NewRequirementManifest("# spec\n\nNo required behavior section.\n"); err == nil {
		t.Fatal("missing required-behavior section must fail")
	}
	empty := "## Требуемое поведение\n\nПросто текст без списка.\n"
	if _, err := NewRequirementManifest(empty); err == nil {
		t.Fatal("no bulleted/numbered items must fail")
	}
}

func TestAssertCouncilModelPayload(t *testing.T) {
	valid := orderedFrom(
		[]string{"role", "verdict", "findings", "do_not_change"},
		[]any{
			"intent_critic", "REVISE",
			[]any{orderedFrom([]string{"id", "severity", "category", "spec_ref", "issue", "evidence", "suggested_direction"},
				[]any{"F-001", "medium", "clarity", "Требуемое поведение / 1", "i", "e", "d"})},
			[]any{},
		},
	)
	if err := AssertCouncilModelPayload(valid); err != nil {
		t.Fatalf("valid model payload: %v", err)
	}
	// Provenance field is forbidden.
	evil := orderedFrom(
		[]string{"role", "verdict", "findings", "do_not_change", "model"},
		[]any{"intent_critic", "PASS", []any{}, []any{}, "gpt-x"},
	)
	if err := AssertCouncilModelPayload(evil); err == nil {
		t.Fatal("model provenance claim must be rejected")
	}
	// needs_input requires a question.
	needsInput := orderedFrom(
		[]string{"role", "verdict", "findings", "do_not_change"},
		[]any{"intent_critic", "needs_input", []any{}, []any{}},
	)
	if err := AssertCouncilModelPayload(needsInput); err == nil {
		t.Fatal("needs_input without question must fail")
	}
	// Non-PASS requires a finding.
	nonPass := orderedFrom(
		[]string{"role", "verdict", "findings", "do_not_change"},
		[]any{"intent_critic", "REVISE", []any{}, []any{}},
	)
	if err := AssertCouncilModelPayload(nonPass); err == nil {
		t.Fatal("non-PASS without findings must fail")
	}
}

func TestAssertChairResultAndBrainstorm(t *testing.T) {
	chair := orderedFrom(
		[]string{"verdict", "decisions", "protected_decisions", "requirement_refs", "final_spec_text"},
		[]any{"PASS", []any{}, []any{}, []any{}, "final spec text"},
	)
	if err := AssertCouncilChairResult(chair); err != nil {
		t.Fatalf("chair payload: %v", err)
	}
	brainstorm := orderedFrom(
		[]string{"role", "alternatives"},
		[]any{"brainstorm", []any{"option A"}},
	)
	if err := AssertBrainstormPayload(brainstorm); err != nil {
		t.Fatalf("brainstorm payload: %v", err)
	}
	empty := orderedFrom([]string{"role"}, []any{"brainstorm"})
	if err := AssertBrainstormPayload(empty); err == nil {
		t.Fatal("empty brainstorm must fail")
	}
}

func TestCouncilDiversity(t *testing.T) {
	member := func(role, model, status, mode string) any {
		return orderedFrom(
			[]string{"role", "status", "execution_mode", "fallback_reason", "observed"},
			[]any{role, status, mode, nil, orderedFrom([]string{"provider", "model", "effort"}, []any{"p", model, "medium"})},
		)
	}
	// Two distinct models, all completed.
	multi, err := GetCouncilDiversity([]any{
		member("intent_critic", "gpt-a", "completed", "direct_api"),
		member("architecture_critic", "gpt-b", "completed", "direct_api"),
	})
	if err != nil {
		t.Fatalf("diversity: %v", err)
	}
	if asStringOr(multi.get("diversity")) != "multi_model" {
		t.Fatalf("diversity: %s", asStringOr(multi.get("diversity")))
	}
	// One model, one role.
	single, _ := GetCouncilDiversity([]any{member("intent_critic", "gpt-a", "completed", "direct_api")})
	if asStringOr(single.get("diversity")) != "multi_role_single_model" {
		t.Fatalf("single diversity: %s", asStringOr(single.get("diversity")))
	}
	// A failed member degrades.
	degraded, _ := GetCouncilDiversity([]any{
		member("intent_critic", "gpt-a", "completed", "direct_api"),
		member("architecture_critic", "", "failed_before_acceptance", "direct_api"),
	})
	if asStringOr(degraded.get("diversity")) != "degraded" {
		t.Fatalf("degraded diversity: %s", asStringOr(degraded.get("diversity")))
	}
	// Unknown observed model.
	unknown, _ := GetCouncilDiversity([]any{member("intent_critic", "", "completed", "direct_api")})
	if asStringOr(unknown.get("diversity")) != "unknown" {
		t.Fatalf("unknown diversity: %s", asStringOr(unknown.get("diversity")))
	}
	// Fallback visibility.
	fallback, _ := GetCouncilDiversity([]any{member("intent_critic", "gpt-a", "completed", "current_agent_fallback")})
	if !asBoolJSONValue(fallback.get("fallback_visible")) {
		t.Fatal("fallback must be visible")
	}
}

func TestCanonicalFindings(t *testing.T) {
	finding := func(role, id string) any {
		return orderedFrom([]string{"composite_id", "role", "id"}, []any{role + ":" + id, role, id})
	}
	// Out of canonical order: executability before intent.
	findings := []any{
		finding("executability_critic", "F-001"),
		finding("intent_critic", "F-001"),
	}
	canonical, err := GetCanonicalFindings(findings)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	first, _ := asOrdered(canonical[0])
	if asStringOr(first.get("composite_id")) != "intent_critic:F-001" {
		t.Fatalf("canonical order: %s", asStringOr(first.get("composite_id")))
	}
	// Duplicate composite is rejected.
	if _, err := GetCanonicalFindings([]any{finding("intent_critic", "F-001"), finding("intent_critic", "F-001")}); err == nil {
		t.Fatal("duplicate composite must fail")
	}
}

func TestReferenceResolves(t *testing.T) {
	spec := "## Требуемое поведение\n1. Первое требование.\n2. Второе требование с 1.1 подпунктом.\n\n1.1 Подпункт."
	// Numbered anchor in range.
	if !ReferenceResolves("Требуемое поведение / 1", spec, 2) {
		t.Fatal("anchor /1 must resolve")
	}
	// Out of range.
	if ReferenceResolves("Требуемое поведение / 3", spec, 2) {
		t.Fatal("anchor /3 out of range")
	}
	// Verbatim fragment.
	if !ReferenceResolves("Первое требование", spec, 2) {
		t.Fatal("verbatim fragment must resolve")
	}
	// Absent fragment.
	if ReferenceResolves("Несуществующий текст", spec, 2) {
		t.Fatal("absent fragment must not resolve")
	}
}

func TestCouncilReviewDigestStability(t *testing.T) {
	review := orderedFrom(
		[]string{"schema_version", "reconciliation"},
		[]any{
			json.Number("2"),
			orderedFrom([]string{"review_sha256", "draft_spec_sha256", "final_spec_sha256"},
				[]any{stringsRepeat("0", 64), stringsRepeat("a", 64), stringsRepeat("b", 64)}),
		},
	)
	digest, err := CouncilReviewDigest(review)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if len(digest) != 64 {
		t.Fatalf("digest length: %d", len(digest))
	}
	// The digest is stable across two calls.
	digest2, _ := CouncilReviewDigest(review)
	if digest != digest2 {
		t.Fatal("digest must be stable")
	}
	// Changing the draft hash changes the digest.
	review.set("reconciliation", orderedFrom([]string{"review_sha256", "draft_spec_sha256", "final_spec_sha256"},
		[]any{stringsRepeat("0", 64), stringsRepeat("c", 64), stringsRepeat("b", 64)}))
	digest3, _ := CouncilReviewDigest(review)
	if digest3 == digest {
		t.Fatal("changed draft must change the digest")
	}
}

func stringsRepeat(text string, count int) string {
	result := ""
	for index := 0; index < count; index++ {
		result += text
	}
	return result
}
