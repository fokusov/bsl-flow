package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// adoptionSpecFixture is the committed real spec of the
// native-task-activation-adoption change; its PowerShell-verified artifact
// stats are 8222 characters and 69 lines.
const adoptionSpecFixturePath = "internal/specvalidate/testdata/native-task-activation-adoption-spec.md"

func loadAdoptionSpecFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(adoptionSpecFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// writeChange creates <project>/openspec/changes/<name> with the given files
// and returns the project root.
func writeChange(t *testing.T, name string, files map[string][]byte) string {
	t.Helper()
	project := t.TempDir()
	dir := filepath.Join(project, "openspec", "changes", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, data := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return project
}

type lintCommandOutput struct {
	SchemaVersion int      `json:"schema_version"`
	Passed        bool     `json:"passed"`
	Errors        []string `json:"errors"`
	Warnings      []string `json:"warnings"`
	Stats         struct {
		Characters int `json:"characters"`
		Lines      int `json:"lines"`
	} `json:"stats"`
}

func runCommand(t *testing.T, args []string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, &out, &errOut)
	if errOut.Len() > 0 {
		t.Fatalf("unexpected stderr: %s", errOut.String())
	}
	return code, out.String()
}

func parseLintJSON(t *testing.T, raw string) lintCommandOutput {
	t.Helper()
	var parsed lintCommandOutput
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("invalid lint JSON %q: %v", raw, err)
	}
	return parsed
}

func TestSpecLintCommandTable(t *testing.T) {
	fixture := loadAdoptionSpecFixture(t)
	demoChange := map[string][]byte{"spec.md": fixture}

	t.Run("clean spec json mirrors artifact shape", func(t *testing.T) {
		project := writeChange(t, "demo-change", demoChange)
		code, raw := runCommand(t, []string{"spec", "lint", "--project", project, "--change", "demo-change", "--json"})
		if code != 0 {
			t.Fatalf("exit %d, output %s", code, raw)
		}
		if !strings.HasPrefix(raw, "{\n  \"schema_version\": 1,\n  \"checked_at_utc\": \"") {
			t.Fatalf("artifact key order/indent drifted: %q", raw)
		}
		parsed := parseLintJSON(t, raw)
		if !parsed.Passed || parsed.Errors == nil || parsed.Warnings == nil || len(parsed.Errors) != 0 || len(parsed.Warnings) != 0 {
			t.Fatalf("expected pass with empty error/warning arrays, got %+v", parsed)
		}
		if parsed.Stats.Characters != 8222 || parsed.Stats.Lines != 69 {
			t.Fatalf("stats drifted from the PowerShell artifact values: %+v", parsed.Stats)
		}
	})

	t.Run("clean spec human summary", func(t *testing.T) {
		project := writeChange(t, "demo-change", demoChange)
		code, raw := runCommand(t, []string{"spec", "lint", "--project", project, "--change", "demo-change"})
		if code != 0 {
			t.Fatalf("exit %d, output %s", code, raw)
		}
		for _, want := range []string{"spec lint demo-change", "PASS: 0 errors, 0 warnings, 8222 characters, 69 lines"} {
			if !strings.Contains(raw, want) {
				t.Fatalf("human summary %q missing %q", raw, want)
			}
		}
	})

	t.Run("single change is the default", func(t *testing.T) {
		project := writeChange(t, "only-change", demoChange)
		code, raw := runCommand(t, []string{"spec", "lint", "--project", project, "--json"})
		if code != 0 {
			t.Fatalf("exit %d, output %s", code, raw)
		}
		if parsed := parseLintJSON(t, raw); !parsed.Passed {
			t.Fatalf("default change lint failed: %+v", parsed)
		}
	})

	t.Run("ambiguous default lists every change", func(t *testing.T) {
		project := t.TempDir()
		for _, name := range []string{"alpha-change", "beta-change"} {
			dir := filepath.Join(project, "openspec", "changes", name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "spec.md"), fixture, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		code, raw := runCommand(t, []string{"spec", "lint", "--project", project, "--json"})
		if code != 2 || !strings.Contains(raw, "BF_INVALID") {
			t.Fatalf("ambiguous default must be BF_INVALID exit 2, got %d %s", code, raw)
		}
		if !strings.Contains(raw, "alpha-change, beta-change") || !strings.Contains(raw, "found 2") {
			t.Fatalf("error must list the changes: %s", raw)
		}
	})

	t.Run("error finding exits 1 with line prefix", func(t *testing.T) {
		bad := strings.Replace(string(fixture), "Дать planned-задачам", "TODO Дать planned-задачам", 1)
		project := writeChange(t, "demo-change", map[string][]byte{"spec.md": []byte(bad)})
		code, raw := runCommand(t, []string{"spec", "lint", "--project", project, "--change", "demo-change", "--json"})
		if code != 1 {
			t.Fatalf("error finding must exit 1, got %d %s", code, raw)
		}
		parsed := parseLintJSON(t, raw)
		if parsed.Passed || len(parsed.Errors) != 1 || parsed.Errors[0] != "spec.md line 10: Unresolved template placeholder: TODO" {
			t.Fatalf("unexpected findings: %+v", parsed)
		}
	})

	t.Run("warning keeps exit 0", func(t *testing.T) {
		warned := strings.Replace(string(fixture), "Поддержка Astra fallback", "на будущее. Поддержка Astra fallback", 1)
		project := writeChange(t, "demo-change", map[string][]byte{"spec.md": []byte(warned)})
		code, raw := runCommand(t, []string{"spec", "lint", "--project", project, "--change", "demo-change", "--json"})
		if code != 0 {
			t.Fatalf("warning must not fail lint, got %d %s", code, raw)
		}
		parsed := parseLintJSON(t, raw)
		if !parsed.Passed || len(parsed.Errors) != 0 || len(parsed.Warnings) != 1 ||
			parsed.Warnings[0] != "Potential speculative design language found; verify concrete justification." {
			t.Fatalf("unexpected findings: %+v", parsed)
		}
	})

	t.Run("missing spec.md is blocked", func(t *testing.T) {
		project := writeChange(t, "empty-change", nil)
		code, raw := runCommand(t, []string{"spec", "lint", "--project", project, "--change", "empty-change"})
		if code != 11 || !strings.Contains(raw, "BF_BLOCKED") || !strings.Contains(raw, "spec.md not found") {
			t.Fatalf("missing spec.md must be BF_BLOCKED exit 11, got %d %s", code, raw)
		}
	})

	t.Run("invalid utf-8 spec.md is blocked", func(t *testing.T) {
		project := writeChange(t, "demo-change", map[string][]byte{"spec.md": []byte("# x\n\xff\xfe\n")})
		code, raw := runCommand(t, []string{"spec", "lint", "--project", project, "--change", "demo-change"})
		if code != 11 || !strings.Contains(raw, "BF_BLOCKED") || !strings.Contains(raw, "valid UTF-8") {
			t.Fatalf("invalid UTF-8 must be BF_BLOCKED exit 11, got %d %s", code, raw)
		}
	})

	invalid := [][]string{
		{"spec"},
		{"spec", "lint"},
		{"spec", "bogus", "--project", "."},
		{"spec", "lint", "--project", "."},
		{"spec", "lint", "--project", ".", "--change", "a/b"},
		{"spec", "lint", "--project", ".", "--change", ".."},
		{"spec", "lint", "--project", ".", "--change", "x", "--change", "y"},
		{"spec", "lint", "--project", ".", "--json", "value"},
		{"spec", "lint", "--project", ".", "--json", "--json"},
		{"spec", "final", "--project", "."},
		{"spec", "lint", "--project", ".", "--extra", "x"},
		{"spec", "lint", "--project", ".", "--change"},
	}
	for _, args := range invalid {
		if code, raw := func() (int, string) {
			var out bytes.Buffer
			code := run(args, &out, &out)
			return code, out.String()
		}(); code != 2 || !strings.Contains(raw, "BF_INVALID") {
			t.Fatalf("args %v must be BF_INVALID exit 2, got %d %s", args, code, raw)
		}
	}
}

// v1FinalReview builds a structurally valid schema v1 review.json over the
// given spec/original bytes (mirrors the shape asserted by
// cli/internal/specvalidate/final_test.go).
func v1FinalReview(t *testing.T, spec, original []byte) []byte {
	t.Helper()
	review := map[string]any{
		"schema_version": 1, "reviewed_at_utc": "2026-09-01T10:00:00Z", "review_iteration": 1,
		"reviewer_verdict": "PASS", "verdict": "PASS", "summary": "Consistent with the task.",
		"scores": map[string]any{"intent_fidelity": 5, "minimality": 5, "completeness": 5,
			"architecture_fit": 5, "testability": 5, "assumption_discipline": 5, "clarity": 5},
		"weighted_score": 5.0,
		"overengineering": map[string]any{"architectural_decision_count": 0, "required_count": 0,
			"justified_count": 0, "optional_count": 0, "unjustified_count": 0, "index": 0,
			"optional_ratio": 0.0, "unjustified_ratio": 0.0, "normalized_index": 0.0, "items": []any{}},
		"blocking_findings": []any{},
		"findings": []any{map[string]any{
			"id": "R-001", "severity": "low", "category": "clarity", "spec_ref": "Цель / 1",
			"issue": "Minor wording.", "evidence": "Spec text.", "suggested_direction": "Tighten wording.",
		}},
		"do_not_change": []any{}, "confidence": 0.9,
		"reviewer": map[string]any{"provider": "opencode", "agent": "build", "model": "test-model"},
		"inputs": map[string]any{
			"original_task_sha256": specSHA256(t, original), "spec_sha256": specSHA256(t, spec), "design_sha256": nil,
		},
		"gate": map[string]any{"pass_weighted_score": 4.0, "block_below_weighted_score": 3.5,
			"max_overengineering_index_for_pass": 1, "max_unjustified_ratio_for_pass": 0.0},
	}
	data, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func v1FinalReconciliation(t *testing.T, reviewBytes, spec []byte, specHash string) []byte {
	t.Helper()
	reconciliation := map[string]any{
		"schema_version": 1, "review_sha256": specSHA256(t, reviewBytes),
		"draft_spec_sha256": specHash, "final_spec_sha256": specSHA256(t, spec),
		"draft_design_sha256": nil, "final_design_sha256": nil,
		"reconciled_at_utc": "2026-09-01T11:00:00Z", "summary": "All findings reconciled.",
		"decisions": []any{map[string]any{
			"finding_id": "R-001", "decision": "rejected", "reason": "Not material.",
			"evidence": "Checked the goal text.", "status": "not_applicable",
			"resolution": "Kept as is.", "spec_ref_after": "Цель / 1",
		}},
		"do_not_change_checks": []any{},
	}
	data, err := json.Marshal(reconciliation)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func specSHA256(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type finalCommandOutput struct {
	SchemaVersion int    `json:"schema_version"`
	Change        string `json:"change"`
	Checks        []struct {
		Name   string `json:"name"`
		Pass   bool   `json:"pass"`
		Detail string `json:"detail"`
	} `json:"checks"`
	Passed bool `json:"passed"`
}

func TestSpecFinalCommandTable(t *testing.T) {
	fixture := loadAdoptionSpecFixture(t)

	t.Run("missing inputs fail every required check", func(t *testing.T) {
		project := writeChange(t, "demo-change", nil)
		code, raw := runCommand(t, []string{"spec", "final", "--project", project, "--change", "demo-change", "--json"})
		if code != 1 {
			t.Fatalf("missing inputs must exit 1, got %d %s", code, raw)
		}
		var parsed finalCommandOutput
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			t.Fatalf("invalid final JSON: %s", raw)
		}
		if parsed.SchemaVersion != 1 || parsed.Change != "demo-change" || parsed.Passed || parsed.Checks == nil {
			t.Fatalf("unexpected summary: %+v", parsed)
		}
		var names []string
		for _, check := range parsed.Checks {
			if !check.Pass {
				names = append(names, check.Name)
			}
		}
		want := "input-review.json\ninput-spec.md\ninput-original-task.md\ninput-review-reconciliation.json"
		if strings.Join(names, "\n") != want {
			t.Fatalf("failing checks mismatch\nexpected:\n  %s\ngot:\n  %s", want, strings.Join(names, "\n  "))
		}
	})

	t.Run("human mode prints FAIL lines", func(t *testing.T) {
		project := writeChange(t, "demo-change", nil)
		code, raw := runCommand(t, []string{"spec", "final", "--project", project, "--change", "demo-change"})
		if code != 1 {
			t.Fatalf("missing inputs must exit 1, got %d %s", code, raw)
		}
		if !strings.Contains(raw, "spec final demo-change") || !strings.Contains(raw, "FAIL input-review.json: Missing required final-validation input:") ||
			!strings.Contains(raw, "result: 4 checks, 4 failed") {
			t.Fatalf("human output drifted: %s", raw)
		}
	})

	t.Run("valid v1 change passes", func(t *testing.T) {
		review := v1FinalReview(t, fixture, []byte("# original task\n"))
		files := map[string][]byte{
			"spec.md":                    fixture,
			"original-task.md":           []byte("# original task\n"),
			"review.json":                review,
			"review-reconciliation.json": v1FinalReconciliation(t, review, fixture, specSHA256(t, fixture)),
		}
		project := writeChange(t, "demo-change", files)
		code, raw := runCommand(t, []string{"spec", "final", "--project", project, "--change", "demo-change", "--json"})
		if code != 0 {
			t.Fatalf("valid v1 change must exit 0, got %d %s", code, raw)
		}
		var parsed finalCommandOutput
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			t.Fatalf("invalid final JSON: %s", raw)
		}
		if len(parsed.Checks) == 0 || !parsed.Passed {
			t.Fatalf("expected a full passing check list, got: %s", raw)
		}
		var failed []string
		for _, check := range parsed.Checks {
			if !check.Pass {
				failed = append(failed, check.Name+": "+check.Detail)
			}
		}
		if len(failed) != 0 {
			t.Fatalf("expected every check to pass, failing: %v", failed)
		}
		code, raw = runCommand(t, []string{"spec", "final", "--project", project, "--change", "demo-change"})
		if code != 0 || !strings.Contains(raw, "0 failed") {
			t.Fatalf("valid v1 human output drifted (exit %d): %s", code, raw)
		}
	})
}

func TestSpecFinalSafeChangeReaderRejectsTraversal(t *testing.T) {
	project := writeChange(t, "demo-change", map[string][]byte{"spec.md": []byte("# spec\n")})
	neighbor := filepath.Join(project, "openspec", "changes", "neighbor")
	if err := os.MkdirAll(neighbor, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(neighbor, "secret.md"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(project, "openspec", "changes", "demo-change")
	read := safeChangeReader(dir)
	if data, err := read("spec.md"); err != nil || string(data) != "# spec\n" {
		t.Fatalf("in-directory read failed: %q %v", data, err)
	}
	for _, rel := range []string{"", "..", "../neighbor/secret.md", "a/../../neighbor/secret.md", "./../../spec.md"} {
		if data, err := read(rel); err == nil {
			t.Fatalf("read(%q) escaped the change directory: %q", rel, data)
		}
	}
}

// TestSpecLintMatchesCommittedArtifacts is the native-versus-PowerShell
// differential: every real change under openspec/changes is linted by the
// native command and its error/warning counts must equal the committed
// spec-lint.json artifact written by Test-1CSpec.ps1.
func TestSpecLintMatchesCommittedArtifacts(t *testing.T) {
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	changesDir := filepath.Join(repoRoot, "openspec", "changes")
	entries, err := os.ReadDir(changesDir)
	if err != nil {
		t.Skipf("repo change layout absent: %v", err)
	}
	checked := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		change := entry.Name()
		artifactPath := filepath.Join(changesDir, change, "spec-lint.json")
		if _, statErr := os.Stat(filepath.Join(changesDir, change, "spec.md")); statErr != nil {
			continue
		}
		artifactRaw, readErr := os.ReadFile(artifactPath)
		if readErr != nil {
			t.Fatalf("%s: committed spec-lint.json unreadable: %v", change, readErr)
		}
		var artifact lintCommandOutput
		if err := json.Unmarshal(artifactRaw, &artifact); err != nil {
			t.Fatalf("%s: invalid committed artifact: %v", change, err)
		}
		code, raw := runCommand(t, []string{"spec", "lint", "--project", repoRoot, "--change", change, "--json"})
		parsed := parseLintJSON(t, raw)
		if len(parsed.Errors) != len(artifact.Errors) {
			t.Errorf("%s: error count native=%d powershell=%d\nnative: %v", change, len(parsed.Errors), len(artifact.Errors), parsed.Errors)
		}
		if len(parsed.Warnings) != len(artifact.Warnings) {
			t.Errorf("%s: warning count native=%d powershell=%d\nnative: %v", change, len(parsed.Warnings), len(artifact.Warnings), parsed.Warnings)
		}
		wantCode := 0
		if len(artifact.Errors) > 0 {
			wantCode = 1
		}
		if code != wantCode {
			t.Errorf("%s: exit code %d, expected %d (artifact errors=%d)", change, code, wantCode, len(artifact.Errors))
		}
		if parsed.Passed != artifact.Passed {
			t.Errorf("%s: passed native=%v powershell=%v", change, parsed.Passed, artifact.Passed)
		}
		checked++
	}
	if checked == 0 {
		t.Skipf("no changes with spec.md and spec-lint.json under %s", changesDir)
	}
	t.Logf("differential verified %d changes against committed artifacts", checked)
}
