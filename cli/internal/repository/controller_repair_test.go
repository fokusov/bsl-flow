package repository

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeRepairRequiresObservedFailedAssertion(t *testing.T) {
	root := t.TempDir()
	worker := filepath.Join(root, "worker")
	raw := filepath.Join(root, "artifacts", "raw")
	if err := os.MkdirAll(worker, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(raw, 0700); err != nil {
		t.Fatal(err)
	}
	criterion := map[string]any{"id": "text", "kind": "file_assertion", "observation": "expected text", "path": "result.txt", "contains": "expected"}
	payload := map[string]any{"worker_path": worker, "request": map[string]any{"criteria": []any{criterion}, "max_source_repairs": int64(1)}, "repair": map[string]any{"rounds": int64(0)}}
	observation := ExecuteObservation{Stage: "verify", Status: "failed", SideEffects: "none", Summary: "BF_FAIL: text: expected content is absent.", Proposal: map[string]any{"repair_eligible": true, "criterion_id": "text", "kind": "file_assertion", "observation": "expected text"}}
	failure, _ := Canonical(map[string]any{"reason": observation.Summary, "side_effects": "none"})
	if err := os.WriteFile(filepath.Join(raw, "failure.json"), failure, 0600); err != nil {
		t.Fatal(err)
	}
	observation.Artifacts = []ArtifactRef{{Path: "raw/failure.json", SHA256: fileSHA256(failure), SizeBytes: int64(len(failure)), Kind: "failure"}}
	path := filepath.Join(worker, "result.txt")
	if err := os.WriteFile(path, []byte("expected"), 0600); err != nil {
		t.Fatal(err)
	}
	if eligible, err := nativeVerificationFailureEligible(payload, observation, filepath.Dir(raw)); err != nil || eligible {
		t.Fatalf("worker failure claim accepted despite passing source: %v %v", eligible, err)
	}
	if err := os.WriteFile(path, []byte("wrong"), 0600); err != nil {
		t.Fatal(err)
	}
	if eligible, err := nativeVerificationFailureEligible(payload, observation, filepath.Dir(raw)); err != nil || !eligible {
		t.Fatalf("actual assertion failure rejected: %v %v", eligible, err)
	}
	observation.Proposal["criterion_id"] = "undeclared"
	if eligible, err := nativeVerificationFailureEligible(payload, observation, filepath.Dir(raw)); err != nil || eligible {
		t.Fatalf("undeclared criterion admitted for repair: %v %v", eligible, err)
	}
}
