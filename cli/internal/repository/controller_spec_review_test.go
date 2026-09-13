package repository

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type nativeCouncilSpecReviewFixture struct {
	payload      map[string]any
	observation  ExecuteObservation
	final        map[string]any
	changeRoot   string
	artifactRoot string
	review       map[string]any
	reviewBytes  []byte
	reconcile    map[string]any
	reconcileRaw []byte
}

func TestNativeSpecReviewDispatchesCouncilV2(t *testing.T) {
	fixture := newNativeCouncilSpecReviewFixture(t)
	data := marshalNativeCouncilJSON(t, fixture.final)
	if err := os.WriteFile(filepath.Join(fixture.artifactRoot, "raw", "final-validation.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.observation.Artifacts = append(fixture.observation.Artifacts, ArtifactRef{Path: "raw/final-validation.json", SHA256: fileSHA256(data), SizeBytes: int64(len(data)), Kind: "raw"})
	if err := validateNativeSpecReviewEvidence(fixture.payload, fixture.observation, fixture.artifactRoot); err != nil {
		t.Fatal(err)
	}
}

func newNativeCouncilSpecReviewFixture(t *testing.T) nativeCouncilSpecReviewFixture {
	t.Helper()
	project := filepath.Join(tempDir(t), "project")
	changeRoot := filepath.Join(project, "openspec", "changes", "bsl-flow-council")
	artifactRoot := filepath.Join(tempDir(t), "artifacts")
	if err := os.MkdirAll(changeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(artifactRoot, "raw"), 0o700); err != nil {
		t.Fatal(err)
	}

	original := []byte("Original council task\n")
	spec := []byte("# Specification\n\n## Требуемое поведение\n\n1. Publish the reviewed result.\n")
	design := []byte("# Design\n\nUse the existing controller boundary.\n")
	for name, data := range map[string][]byte{"original-task.md": original, "spec.md": spec, "design.md": design} {
		if err := os.WriteFile(filepath.Join(changeRoot, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	originalHash := fileSHA256(original)
	specHash := fileSHA256(spec)
	designHash := fileSHA256(design)
	policyHash := repeatedNativeCouncilHash('d')
	modelA, modelB := "council-model-a", "council-model-b"
	member := func(role, attemptID, model string, chair bool) map[string]any {
		hashes := map[string]any{
			"original_task_sha256": originalHash,
			"spec_sha256":          specHash,
			"design_sha256":        designHash,
			"evidence_sha256":      repeatedNativeCouncilHash('e'),
			"policy_hash":          policyHash,
			"rubric_sha256":        repeatedNativeCouncilHash('f'),
		}
		if chair {
			hashes["member_aggregate_sha256"] = repeatedNativeCouncilHash('a')
		}
		return map[string]any{
			"schema_version": 1, "role": role, "attempt_id": attemptID, "status": "completed",
			"summary":        "Completed council member.",
			"requested":      map[string]any{"provider": "fixture-provider", "model": model, "effort": "high"},
			"observed":       map[string]any{"provider": "fixture-provider", "model": model, "effort": "high"},
			"execution_mode": "direct_api", "fallback_reason": nil, "input_hashes": hashes,
			"payload_sha256": strings.Repeat("b", 64), "usage": nil, "cost_state": "no_usage_reported",
			"dispatched_at_utc": "2026-09-12T10:00:00Z", "completed_at_utc": "2026-09-12T10:00:01Z",
		}
	}

	review := map[string]any{
		"schema_version": 2, "reviewed_at_utc": "2026-09-12T10:00:02Z", "council_schema_version": 1,
		"verdict": "PASS", "diversity": "multi_model", "fallback_visible": false,
		"inputs": map[string]any{
			"original_task_sha256": originalHash, "spec_sha256": specHash,
			"design_sha256": designHash, "policy_hash": policyHash,
		},
		"manifest": map[string]any{
			"version": 1, "requirements": []any{map[string]any{
				"id": "REQ-001", "source_hash": repeatedNativeCouncilHash('c'), "draft_ref": "Требуемое поведение / 1",
			}},
		},
		"members": []any{
			member("intent_critic", "attempt-intent", modelA, false),
			member("chair", "attempt-chair", modelB, true),
		},
		"findings": []any{}, "protected": []any{}, "questions": []any{},
		"chair": map[string]any{
			"verdict": "PASS", "decisions": []any{}, "protected_decisions": []any{},
			"requirement_refs": []any{map[string]any{"id": "REQ-001", "final_refs": []any{"Требуемое поведение / 1"}}},
			"final_spec_text":  string(spec), "final_design_text": string(design),
		},
		"reconciliation": map[string]any{
			"review_sha256": repeatedNativeCouncilHash('9'), "draft_spec_sha256": specHash,
			"final_spec_sha256": specHash, "draft_design_sha256": designHash, "final_design_sha256": designHash,
		},
		"gate": map[string]any{"structural_only": true, "passed": true},
	}
	reviewBytes := marshalNativeCouncilJSON(t, review)
	if err := os.WriteFile(filepath.Join(changeRoot, "review.json"), reviewBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactRoot, "raw", "review.json"), reviewBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	reviewHash := fileSHA256(reviewBytes)
	reconciliation := map[string]any{
		"schema_version": 2, "review_sha256": reviewHash, "draft_spec_sha256": specHash,
		"final_spec_sha256": specHash, "draft_design_sha256": designHash, "final_design_sha256": designHash,
		"reconciled_at_utc": "2026-09-12T10:00:03Z", "summary": "Fixture reconciliation.",
		"decisions": []any{}, "do_not_change_checks": []any{},
	}
	reconcileRaw := marshalNativeCouncilJSON(t, reconciliation)
	for _, root := range []string{changeRoot, filepath.Join(artifactRoot, "raw")} {
		if err := os.WriteFile(filepath.Join(root, "review-reconciliation.json"), reconcileRaw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reconcileHash := fileSHA256(reconcileRaw)
	final := map[string]any{
		"schema_version": 2, "checked_at_utc": "2026-09-12T10:00:04Z", "passed": true,
		"review_schema": 2, "verdict": "PASS", "diversity": "multi_model",
		"inputs": map[string]any{
			"review_sha256": reviewHash, "reconciliation_sha256": reconcileHash,
			"final_spec_sha256": specHash, "final_design_sha256": designHash, "original_task_sha256": originalHash,
		},
		"errors": []any{},
	}
	observation := ExecuteObservation{
		Stage: "spec_review", Status: "completed",
		Artifacts: []ArtifactRef{
			{Path: "raw/review.json", SHA256: reviewHash, SizeBytes: int64(len(reviewBytes)), Kind: "raw"},
			{Path: "raw/review-reconciliation.json", SHA256: reconcileHash, SizeBytes: int64(len(reconcileRaw)), Kind: "raw"},
		},
	}
	payload := map[string]any{
		"project_path": project,
		"request":      map[string]any{"request_id": "council", "prompt": string(original)},
	}
	return nativeCouncilSpecReviewFixture{
		payload: payload, observation: observation, final: final, changeRoot: changeRoot, artifactRoot: artifactRoot,
		review: review, reviewBytes: reviewBytes, reconcile: reconciliation, reconcileRaw: reconcileRaw,
	}
}

func repeatedNativeCouncilHash(char byte) string {
	return strings.Repeat(string(char), 64)
}

func marshalNativeCouncilJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestValidateNativeCouncilSpecReviewV2Valid(t *testing.T) {
	fixture := newNativeCouncilSpecReviewFixture(t)
	if err := validateNativeCouncilSpecReviewEvidence(fixture.payload, fixture.observation, fixture.artifactRoot, fixture.final); err != nil {
		t.Fatalf("valid council v2 evidence rejected: %v", err)
	}
}

func TestValidateNativeCouncilSpecReviewV2RejectsStaleSpec(t *testing.T) {
	fixture := newNativeCouncilSpecReviewFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.changeRoot, "spec.md"), []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateNativeCouncilSpecReviewEvidence(fixture.payload, fixture.observation, fixture.artifactRoot, fixture.final); err == nil {
		t.Fatal("stale spec was accepted")
	} else {
		requireNativeKind(t, err, "BF_BLOCKED")
	}
}

func TestValidateNativeCouncilSpecReviewV2RejectsChangedReview(t *testing.T) {
	fixture := newNativeCouncilSpecReviewFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.changeRoot, "review.json"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateNativeCouncilSpecReviewEvidence(fixture.payload, fixture.observation, fixture.artifactRoot, fixture.final); err == nil {
		t.Fatal("changed review was accepted")
	} else {
		requireNativeKind(t, err, "BF_BLOCKED")
	}
}

func TestValidateNativeCouncilSpecReviewV2RejectsNonPassFinal(t *testing.T) {
	fixture := newNativeCouncilSpecReviewFixture(t)
	fixture.final["verdict"] = "BLOCK"
	fixture.final["passed"] = true
	if err := validateNativeCouncilSpecReviewEvidence(fixture.payload, fixture.observation, fixture.artifactRoot, fixture.final); err == nil {
		t.Fatal("non-PASS final validation was accepted")
	} else {
		requireNativeKind(t, err, "BF_BLOCKED")
	}
}

// nativeCouncilRewriteReview re-marshals a mutated review and restores every
// byte/hash linkage so the mutation is the only inconsistency left.
func nativeCouncilRewriteReview(t *testing.T, fixture *nativeCouncilSpecReviewFixture) {
	t.Helper()
	reviewBytes := marshalNativeCouncilJSON(t, fixture.review)
	reviewHash := fileSHA256(reviewBytes)
	for _, root := range []string{fixture.changeRoot, filepath.Join(fixture.artifactRoot, "raw")} {
		if err := os.WriteFile(filepath.Join(root, "review.json"), reviewBytes, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixture.reviewBytes = reviewBytes
	fixture.observation.Artifacts[0] = ArtifactRef{Path: "raw/review.json", SHA256: reviewHash, SizeBytes: int64(len(reviewBytes)), Kind: "raw"}
	fixture.reconcile["review_sha256"] = reviewHash
	reconcileRaw := marshalNativeCouncilJSON(t, fixture.reconcile)
	reconcileHash := fileSHA256(reconcileRaw)
	for _, root := range []string{fixture.changeRoot, filepath.Join(fixture.artifactRoot, "raw")} {
		if err := os.WriteFile(filepath.Join(root, "review-reconciliation.json"), reconcileRaw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixture.reconcileRaw = reconcileRaw
	fixture.observation.Artifacts[1] = ArtifactRef{Path: "raw/review-reconciliation.json", SHA256: reconcileHash, SizeBytes: int64(len(reconcileRaw)), Kind: "raw"}
	inputs := fixture.final["inputs"].(map[string]any)
	inputs["review_sha256"] = reviewHash
	inputs["reconciliation_sha256"] = reconcileHash
}

func TestValidateNativeCouncilSpecReviewV2Negatives(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, fixture *nativeCouncilSpecReviewFixture)
	}{
		{
			name: "retained review bytes differ from the declared descriptor",
			mutate: func(t *testing.T, fixture *nativeCouncilSpecReviewFixture) {
				if err := os.WriteFile(filepath.Join(fixture.artifactRoot, "raw", "review.json"), []byte("tampered\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "retained reconciliation bytes differ from the declared descriptor",
			mutate: func(t *testing.T, fixture *nativeCouncilSpecReviewFixture) {
				if err := os.WriteFile(filepath.Join(fixture.artifactRoot, "raw", "review-reconciliation.json"), []byte("tampered\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "declared review artifact is missing",
			mutate: func(t *testing.T, fixture *nativeCouncilSpecReviewFixture) {
				if err := os.Remove(filepath.Join(fixture.artifactRoot, "raw", "review.json")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "live original task is missing",
			mutate: func(t *testing.T, fixture *nativeCouncilSpecReviewFixture) {
				if err := os.Remove(filepath.Join(fixture.changeRoot, "original-task.md")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "review artifact is declared twice",
			mutate: func(t *testing.T, fixture *nativeCouncilSpecReviewFixture) {
				fixture.observation.Artifacts = append(fixture.observation.Artifacts, fixture.observation.Artifacts[0])
			},
		},
		{
			name: "inline final spec hash differs from the reconciliation sidecar",
			mutate: func(t *testing.T, fixture *nativeCouncilSpecReviewFixture) {
				reconciliation := fixture.review["reconciliation"].(map[string]any)
				reconciliation["final_spec_sha256"] = repeatedNativeCouncilHash('z')
				nativeCouncilRewriteReview(t, fixture)
			},
		},
		{
			name: "sidecar review linkage does not match the current review",
			mutate: func(t *testing.T, fixture *nativeCouncilSpecReviewFixture) {
				// Rewrite review bytes with a new shape-valid timestamp while
				// leaving the sidecar linkage bound to the previous bytes.
				fixture.review["reviewed_at_utc"] = "2026-09-12T11:00:00Z"
				reviewBytes := marshalNativeCouncilJSON(t, fixture.review)
				reviewHash := fileSHA256(reviewBytes)
				for _, root := range []string{fixture.changeRoot, filepath.Join(fixture.artifactRoot, "raw")} {
					if err := os.WriteFile(filepath.Join(root, "review.json"), reviewBytes, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				fixture.observation.Artifacts[0] = ArtifactRef{Path: "raw/review.json", SHA256: reviewHash, SizeBytes: int64(len(reviewBytes)), Kind: "raw"}
				fixture.final["inputs"].(map[string]any)["review_sha256"] = reviewHash
			},
		},
		{
			name: "final validation schema is not v2",
			mutate: func(t *testing.T, fixture *nativeCouncilSpecReviewFixture) {
				fixture.final["schema_version"] = 3
			},
		},
		{
			name: "final validation declares a non-v2 review schema",
			mutate: func(t *testing.T, fixture *nativeCouncilSpecReviewFixture) {
				fixture.final["review_schema"] = 1
			},
		},
		{
			name: "review schema version is not 2",
			mutate: func(t *testing.T, fixture *nativeCouncilSpecReviewFixture) {
				fixture.review["schema_version"] = 1
				nativeCouncilRewriteReview(t, fixture)
			},
		},
		{
			name: "chair verdict is not PASS",
			mutate: func(t *testing.T, fixture *nativeCouncilSpecReviewFixture) {
				fixture.review["chair"].(map[string]any)["verdict"] = "REVISE"
				nativeCouncilRewriteReview(t, fixture)
			},
		},
		{
			name: "final validation carries errors",
			mutate: func(t *testing.T, fixture *nativeCouncilSpecReviewFixture) {
				fixture.final["errors"] = []any{"fixture error"}
			},
		},
		{
			name: "final validation is not passed",
			mutate: func(t *testing.T, fixture *nativeCouncilSpecReviewFixture) {
				fixture.final["passed"] = false
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newNativeCouncilSpecReviewFixture(t)
			testCase.mutate(t, &fixture)
			if err := validateNativeCouncilSpecReviewEvidence(fixture.payload, fixture.observation, fixture.artifactRoot, fixture.final); err == nil {
				t.Fatalf("%s was accepted", testCase.name)
			}
		})
	}
}
