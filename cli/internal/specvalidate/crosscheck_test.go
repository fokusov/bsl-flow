//go:build crosscheck

package specvalidate

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const pwshLintScript = `C:\DEV\BSL Flow\global\skills\1c-spec-review\scripts\Test-1CSpec.ps1`

// TestPwshLintParity replays every lint fixture through the real
// Test-1CSpec.ps1 and compares the emitted errors/warnings (including their
// order and line numbers) with the Go port.  It never runs in the default
// test suite; invoke it explicitly with:
//
//	go test -tags crosscheck ./internal/specvalidate -run TestPwshLintParity -count=1
func TestPwshLintParity(t *testing.T) {
	if _, err := exec.LookPath("pwsh"); err != nil {
		t.Skip("pwsh is not available")
	}
	if _, err := os.Stat(pwshLintScript); err != nil {
		t.Skipf("lint script not found: %v", err)
	}
	specs := map[string]string{"baseline": lintBaselineSpec}
	for _, tt := range lintNegativeCases {
		specs[tt.name] = tt.spec
	}
	specs["english heading"] = strings.Replace(lintBaselineSpec, "## Классификация", "## Classification", 1)
	specs["acceptance bullet structured"] = strings.Replace(lintBaselineSpec,
		"- GIVEN заполненная форма WHEN пользователь сохраняет THEN документ записывается без ошибок.",
		"- Проверить вручную что форма открывается, сохраняется и закрывается без ошибок интерфейса.", 1)
	specs["crlf baseline"] = strings.ReplaceAll(lintBaselineSpec, "\n", "\r\n")
	specs["length warning"] = strings.Replace(lintBaselineSpec,
		"Исправить ошибку в форме документа.",
		"Исправить ошибку. "+strings.Repeat("подробности ", 3000), 1)
	real, err := os.ReadFile(filepath.Join("testdata", "native-task-activation-adoption-spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	specs["real adoption spec"] = string(real)

	for name, spec := range specs {
		t.Run(name, func(t *testing.T) {
			dir, err := os.MkdirTemp(".", ".crosscheck-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			abs, err := filepath.Abs(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "spec.md"), []byte(spec), 0o600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("pwsh", "-NoProfile", "-File", pwshLintScript, "-ChangePath", abs, "-NoThrow")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("pwsh lint failed: %v\n%s", err, output)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "spec-lint.json"))
			if err != nil {
				t.Fatal(err)
			}
			var artifact struct {
				Errors   []string `json:"errors"`
				Warnings []string `json:"warnings"`
			}
			if err := json.Unmarshal(raw, &artifact); err != nil {
				t.Fatal(err)
			}
			findings, err := LintSpec([]byte(spec))
			if err != nil {
				t.Fatal(err)
			}
			goLines := []string{}
			for _, finding := range findings {
				if finding.Severity == "error" {
					goLines = append(goLines, fmt.Sprintf("E|spec.md line %d: %s", finding.Line, finding.Message))
				} else {
					goLines = append(goLines, "W|"+finding.Message)
				}
			}
			psLines := []string{}
			for _, message := range artifact.Errors {
				psLines = append(psLines, "E|"+message)
			}
			for _, message := range artifact.Warnings {
				psLines = append(psLines, "W|"+message)
			}
			if strings.Join(goLines, "\n") != strings.Join(psLines, "\n") {
				t.Fatalf("parity mismatch\nGo:\n  %s\nPowerShell:\n  %s",
					strings.Join(goLines, "\n  "), strings.Join(psLines, "\n  "))
			}
		})
	}
}

const pwshFinalScript = `C:\DEV\BSL Flow\global\skills\1c-spec-review\scripts\Test-1CSpecFinal.ps1`

// TestPwshFinalV1Parity runs the real Test-1CSpecFinal.ps1 over the same
// schema v1 fixtures the unit tests use (kept consistent with the
// PowerShell derived-value recompute: five 5-scores yield weighted 4.25,
// which passes the fixture's gate of 4.0) and compares the final verdict
// with the Go port.  Invoke with:
//
//	go test -tags crosscheck ./internal/specvalidate -run TestPwshFinalV1Parity -count=1
func TestPwshFinalV1Parity(t *testing.T) {
	if _, err := exec.LookPath("pwsh"); err != nil {
		t.Skip("pwsh is not available")
	}
	if _, err := os.Stat(pwshFinalScript); err != nil {
		t.Skipf("final script not found: %v", err)
	}
	spec := []byte(lintBaselineSpec)
	tests := []struct {
		name             string
		files            map[string][]byte
		psPassed         bool
		psErrorSubstring string
		goFailing        []string
	}{
		{
			name:     "happy path",
			files:    buildV1Change(t, nil, nil, spec),
			psPassed: true,
		},
		{
			name:             "spec changed after review",
			files:            withSpecChanged(buildV1Change(t, nil, nil, spec)),
			psErrorSubstring: "reconciliation.final_spec_sha256 does not match current spec.md.",
			goFailing:        []string{"binding-final-spec"},
		},
		{
			name:             "missing review artifact",
			files:            buildV1Change(t, nil, nil, spec, "review.json"),
			psErrorSubstring: "Missing required final-validation input:",
			goFailing:        []string{"input-review.json"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, err := os.MkdirTemp(".", ".crosscheck-final-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			change := filepath.Join(root, "openspec", "changes", "demo")
			if err := os.MkdirAll(change, 0o700); err != nil {
				t.Fatal(err)
			}
			for name, data := range tt.files {
				if err := os.WriteFile(filepath.Join(change, name), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			absolute, err := filepath.Abs(root)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command("pwsh", "-NoProfile", "-File", pwshFinalScript, "-ProjectPath", absolute, "-ChangeName", "demo")
			output, _ := command.CombinedOutput()
			raw, err := os.ReadFile(filepath.Join(change, "final-validation.json"))
			if err != nil {
				t.Fatalf("final-validation.json was not written\n%s", output)
			}
			var artifact struct {
				Passed bool     `json:"passed"`
				Errors []string `json:"errors"`
			}
			if err := json.Unmarshal(raw, &artifact); err != nil {
				t.Fatal(err)
			}
			if artifact.Passed != tt.psPassed {
				t.Fatalf("PowerShell passed=%v, want %v; errors: %v\n%s", artifact.Passed, tt.psPassed, artifact.Errors, output)
			}
			if tt.psErrorSubstring != "" && !containsSubstring(artifact.Errors, tt.psErrorSubstring) {
				t.Fatalf("PowerShell errors missing %q: %v", tt.psErrorSubstring, artifact.Errors)
			}
			checks, err := ValidateFinal(change, mapRead(tt.files))
			if err != nil {
				t.Fatal(err)
			}
			assertFailing(t, checks, tt.goFailing...)
		})
	}
}

func withSpecChanged(files map[string][]byte) map[string][]byte {
	files["spec.md"] = []byte(strings.Replace(lintBaselineSpec, "Исправить ошибку", "Исправим ошибку", 1))
	return files
}

func containsSubstring(messages []string, substring string) bool {
	for _, message := range messages {
		if strings.Contains(message, substring) {
			return true
		}
	}
	return false
}
