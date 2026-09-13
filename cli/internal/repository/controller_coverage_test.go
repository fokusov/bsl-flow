package repository

import "testing"

func TestNativeCoverageRejectsIncompleteOrUnboundAssessments(t *testing.T) {
	payload := map[string]any{"request": map[string]any{
		"requirements": []any{map[string]any{"id": "R1", "criterion_ids": []any{"C1"}}},
		"criteria":     []any{map[string]any{"id": "C1", "kind": "unit", "expected_tests": []any{"positive", "negative"}, "protected_paths": []any{"tests"}}},
	}}
	coverage := map[string]any{"verdict": "PASS", "assessments": []any{map[string]any{
		"requirement_id": "R1", "verdict": "SUFFICIENT", "rationale": "Both directions are asserted.",
		"criterion_evidence": []any{map[string]any{"criterion_id": "C1", "test_ids": []any{"positive", "negative"}, "source_paths": []any{"tests/check.go"}, "observation": "Positive and negative branches", "evidence": "Concrete assertions in tests/check.go"}},
	}}}
	binding := map[string]any{"schema_version": int64(1), "kind": "bsl-flow.requirement-coverage-binding", "requirements_sha256": "bound", "criteria_sha256": "bound", "coverage_sha256": "bound", "files": []any{map[string]any{"requirement_id": "R1", "criterion_id": "C1", "path": "tests/check.go", "sha256": "bound"}}}
	if err := validateNativeCoverageAssessments(payload, coverage, binding); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"missing_requirement", "undeclared_test", "missing_negative", "outside_protected", "parent_traversal", "windows_parent_traversal", "forged_file", "missing_file"} {
		t.Run(field, func(t *testing.T) {
			review, _ := cloneObject(coverage)
			files, _ := cloneObject(binding)
			assessment := asMap(anyItems(review["assessments"])[0])
			item := asMap(anyItems(assessment["criterion_evidence"])[0])
			switch field {
			case "missing_requirement":
				review["assessments"] = []any{}
			case "undeclared_test":
				item["test_ids"] = []any{"positive", "invented"}
			case "missing_negative":
				item["test_ids"] = []any{"positive"}
			case "outside_protected":
				item["source_paths"] = []any{"src/check.go"}
			case "parent_traversal", "windows_parent_traversal":
				path := "tests/../src/check.go"
				if field == "windows_parent_traversal" {
					path = `tests\..\src\check.go`
				}
				item["source_paths"] = []any{path}
				asMap(anyItems(files["files"])[0])["path"] = path
			case "forged_file":
				asMap(anyItems(files["files"])[0])["requirement_id"] = "R2"
			case "missing_file":
				files["files"] = []any{}
			}
			if validateNativeCoverageAssessments(payload, review, files) == nil {
				t.Fatal("incomplete or unbound coverage accepted")
			}
		})
	}
}
