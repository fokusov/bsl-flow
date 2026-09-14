package stagehost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bsl-flow/cli/internal/repository"
)

func coverageState(t *testing.T, worker string, criteria []any, requirements any) map[string]any {
	t.Helper()
	request := map[string]any{"mode": "implement", "criteria": criteria}
	if requirements != nil {
		request["requirements"] = requirements
	}
	return map[string]any{
		"request":        request,
		"worker_path":    worker,
		"classification": map[string]any{"impact_flags": []any{}},
	}
}

func TestAssertCoverageReviewBinding(t *testing.T) {
	worker := t.TempDir()
	source := filepath.Join(worker, "tests", "unit.go")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if err := os.WriteFile(source, []byte("package tests"), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	criteria := []any{
		map[string]any{"id": "check", "observation": "tests pass", "kind": "unit", "executable": `C:\x\go.exe`, "arguments": []any{}, "report": ".bsl-flow-worker/report.xml", "expected_tests": []any{"TestOne", "TestTwo"}, "protected_paths": []any{"tests"}},
	}
	requirements := []any{
		map[string]any{"id": "REQ-1", "text": "tests must run", "criterion_ids": []any{"check"}},
	}
	state := coverageState(t, worker, criteria, requirements)
	coverage := map[string]any{
		"verdict": "PASS",
		"assessments": []any{
			map[string]any{
				"requirement_id": "REQ-1", "verdict": "SUFFICIENT", "rationale": "both tests observe the requirement",
				"criterion_evidence": []any{
					map[string]any{
						"criterion_id": "check", "test_ids": []any{"TestOne", "TestTwo"},
						"source_paths": []any{"tests/unit.go"},
						"observation":  "assertions exist", "evidence": "tests/unit.go:1",
					},
				},
			},
		},
	}
	directory := t.TempDir()
	binding, err := assertCoverageReview(state, coverage, directory)
	if err != nil {
		t.Fatalf("assertCoverageReview: %v", err)
	}
	if binding["kind"] != "bsl-flow.requirement-coverage-binding" {
		t.Fatalf("binding kind: %v", binding["kind"])
	}
	files, _ := asArray(binding["files"])
	if len(files) != 1 {
		t.Fatalf("binding files: %v", files)
	}
	if _, err := os.Stat(filepath.Join(directory, "coverage-review-binding.json")); err != nil {
		t.Fatalf("binding file: %v", err)
	}
	if _, err := assertCoverageReview(state, coverage, directory); err != nil {
		t.Fatalf("idempotent recheck: %v", err)
	}
	// A byte-different stored binding conflicts.
	if err := os.WriteFile(filepath.Join(directory, "coverage-review-binding.json"), []byte(`{"schema_version":1}`), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if _, err := assertCoverageReview(state, coverage, directory); err == nil || err.Error() != "BF_CONFLICT: coverage evidence binding is stale or conflicting." {
		t.Fatalf("conflict diagnostic: %v", err)
	}
}

func TestAssertCoverageReviewErrors(t *testing.T) {
	worker := t.TempDir()
	source := filepath.Join(worker, "tests", "unit.go")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if err := os.WriteFile(source, []byte("package tests"), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	criteria := []any{
		map[string]any{"id": "check", "observation": "tests pass", "kind": "unit", "executable": `C:\x\go.exe`, "arguments": []any{}, "report": ".bsl-flow-worker/report.xml", "expected_tests": []any{"TestOne"}, "protected_paths": []any{"tests"}},
	}
	requirements := []any{
		map[string]any{"id": "REQ-1", "text": "tests must run", "criterion_ids": []any{"check"}},
	}
	state := coverageState(t, worker, criteria, requirements)
	unknownRequirement := map[string]any{
		"verdict": "PASS",
		"assessments": []any{
			map[string]any{
				"requirement_id": "OTHER", "verdict": "SUFFICIENT", "rationale": "r",
				"criterion_evidence": []any{map[string]any{"criterion_id": "check", "test_ids": []any{"TestOne"}, "source_paths": []any{"tests/unit.go"}, "observation": "o", "evidence": "e"}},
			},
		},
	}
	if _, err := assertCoverageReview(state, unknownRequirement, t.TempDir()); err == nil || err.Error() != "BF_INVALID: coverage assessments must identify every requirement exactly once." {
		t.Fatalf("unknown requirement: %v", err)
	}
	undeclaredTest := map[string]any{
		"verdict": "PASS",
		"assessments": []any{
			map[string]any{
				"requirement_id": "REQ-1", "verdict": "SUFFICIENT", "rationale": "r",
				"criterion_evidence": []any{map[string]any{"criterion_id": "check", "test_ids": []any{"TestUnknown"}, "source_paths": []any{"tests/unit.go"}, "observation": "o", "evidence": "e"}},
			},
		},
	}
	if _, err := assertCoverageReview(state, undeclaredTest, t.TempDir()); err == nil || err.Error() != "BF_INVALID: coverage review refers to an undeclared test." {
		t.Fatalf("undeclared test: %v", err)
	}
	verdictConflict := map[string]any{
		"verdict": "BLOCK",
		"assessments": []any{
			map[string]any{
				"requirement_id": "REQ-1", "verdict": "SUFFICIENT", "rationale": "r",
				"criterion_evidence": []any{map[string]any{"criterion_id": "check", "test_ids": []any{"TestOne"}, "source_paths": []any{"tests/unit.go"}, "observation": "o", "evidence": "e"}},
			},
		},
	}
	if _, err := assertCoverageReview(state, verdictConflict, t.TempDir()); err == nil || err.Error() != "BF_INVALID: coverage verdict contradicts requirement assessments." {
		t.Fatalf("verdict conflict: %v", err)
	}
}

func TestProtectedTestManifestSelection(t *testing.T) {
	manifest := map[string]any{"files": []any{
		map[string]any{"path": "tests/unit.go", "sha256": "a", "deleted": false},
		map[string]any{"path": "tests/data/fixture.json", "sha256": "b", "deleted": false},
		map[string]any{"path": "src/main.go", "sha256": "c", "deleted": false},
	}}
	state := coverageState(t, t.TempDir(), []any{
		map[string]any{"id": "check", "observation": "o", "kind": "unit", "protected_paths": []any{"tests"}},
	}, nil)
	selected, err := protectedTestManifest(state, manifest)
	if err != nil {
		t.Fatalf("protectedTestManifest: %v", err)
	}
	if len(selected) != 2 {
		t.Fatalf("selected: %v", selected)
	}
	missing := map[string]any{"files": []any{map[string]any{"path": "src/main.go", "sha256": "c", "deleted": false}}}
	if _, err := protectedTestManifest(state, missing); err == nil || !strings.HasPrefix(err.Error(), "BF_BLOCKED: protected test input is missing") {
		t.Fatalf("missing scope: %v", err)
	}
}

func TestAssertVerificationCoverageImpact(t *testing.T) {
	worker := t.TempDir()
	state := coverageState(t, worker, []any{
		map[string]any{"id": "static", "observation": "o", "kind": "static"},
	}, nil)
	state["request"].(map[string]any)["mode"] = "implement"
	state["classification"] = map[string]any{"impact_flags": []any{"posting"}}
	if err := assertVerificationCoverage(state); err == nil || err.Error() != "BF_BLOCKED: impact posting requires integration evidence selected from the original requirements." {
		t.Fatalf("impact coverage: %v", err)
	}
	state["request"].(map[string]any)["criteria"] = []any{map[string]any{"id": "integration", "observation": "o", "kind": "integration"}}
	if err := assertVerificationCoverage(state); err != nil {
		t.Fatalf("satisfied impact: %v", err)
	}
	_ = repository.Canonical
}
