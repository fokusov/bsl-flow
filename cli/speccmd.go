package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bsl-flow/cli/internal/specvalidate"
)

// The spec commands expose the native ports of the 1C OpenSpec spec
// validators (cli/internal/specvalidate) without PowerShell:
//
//	bsl-flow spec lint  --project <path> [--change <id>] [--json]
//	bsl-flow spec final --project <path> --change <id> [--json]
//
// `spec lint` reproduces the artifact shape of Test-1CSpec.ps1's
// spec-lint.json (schema_version, checked_at_utc, passed, errors, warnings,
// stats) and exits 0 only when no error-severity finding exists.  `spec
// final` wraps ValidateFinal with path-safe reads confined to the change
// directory and exits 0 only when every final check passes.  Usage problems
// keep the BF_INVALID envelope of parse(); unusable inputs report BF_BLOCKED.

// parseSpecInvocation parses the `spec lint` / `spec final` argument forms
// with the same strict option discipline as parse(): unknown or repeated
// options are rejected, option values may not be empty, start with "--", or
// carry control characters, and --json is a valueless flag.
func parseSpecInvocation(args []string) (invocation, error) {
	in := invocation{command: "spec", options: map[string]string{}}
	if args[1] != "lint" && args[1] != "final" {
		return in, errors.New("expected spec lint or spec final")
	}
	in.action = args[1]
	allowed := map[string]bool{"--project": true, "--change": true, "--json": true}
	for i := 2; i < len(args); i++ {
		key := args[i]
		if !allowed[key] || in.options[key] != "" {
			return in, fmt.Errorf("unknown, repeated, or inapplicable option %q", key)
		}
		if key == "--json" {
			in.options[key] = "1"
			continue
		}
		if i+1 == len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") || strings.ContainsAny(args[i+1], "\x00\r\n") {
			return in, fmt.Errorf("missing or invalid value for %s", key)
		}
		in.options[key] = args[i+1]
		i++
	}
	if in.options["--project"] == "" {
		return in, fmt.Errorf("%s requires --project", in.action)
	}
	if in.action == "final" && in.options["--change"] == "" {
		return in, errors.New("final requires --change")
	}
	if change := in.options["--change"]; change != "" && !validChangeID(change) {
		return in, errors.New("--change must be a single directory name under openspec/changes")
	}
	return in, nil
}

// validChangeID accepts only a plain directory name, so the change can never
// traverse outside <project>/openspec/changes through separators, parent
// segments, absolute paths, or Windows volume names.
func validChangeID(id string) bool {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) || strings.ContainsAny(id, "\x00\r\n") {
		return false
	}
	return !filepath.IsAbs(id) && filepath.VolumeName(id) == ""
}

// runSpecCommand dispatches a parsed spec invocation; it never touches the
// embedded bundle or PowerShell.
func runSpecCommand(in invocation, out io.Writer) int {
	asJSON := in.options["--json"] != ""
	if in.action == "lint" {
		return runSpecLint(in.options["--project"], in.options["--change"], asJSON, out)
	}
	return runSpecFinal(in.options["--project"], in.options["--change"], asJSON, out)
}

// specChangeDir is the change directory under <project>/openspec/changes.
func specChangeDir(project, change string) string {
	return filepath.Join(project, "openspec", "changes", change)
}

// singleChange resolves the default change for `spec lint`: the only change
// directory under <project>/openspec/changes.  Any other count is a usage
// error that lists what was found.
func singleChange(project string) (string, error) {
	changesDir := filepath.Join(project, "openspec", "changes")
	entries, err := os.ReadDir(changesDir)
	if err != nil {
		return "", fmt.Errorf("cannot read changes directory %s: %w", changesDir, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	if len(names) != 1 {
		found := strings.Join(names, ", ")
		if found == "" {
			found = "none"
		}
		return "", fmt.Errorf("expected exactly one change under %s, found %d: %s", changesDir, len(names), found)
	}
	return names[0], nil
}

// runSpecLint lints <change>/spec.md through LintSpec and reports the
// Test-1CSpec.ps1 artifact shape.  Exit codes: 0 no error findings, 1 at
// least one error finding, 2 invalid usage, 11 unusable input.
func runSpecLint(project, change string, asJSON bool, out io.Writer) int {
	if change == "" {
		resolved, err := singleChange(project)
		if err != nil {
			return hostError(out, 2, "", err)
		}
		change = resolved
	}
	dir := specChangeDir(project, change)
	data, err := os.ReadFile(filepath.Join(dir, "spec.md"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return hostError(out, 11, "", fmt.Errorf("spec.md not found: %s", filepath.Join(dir, "spec.md")))
		}
		return hostError(out, 11, "", err)
	}
	findings, lintErr := specvalidate.LintSpec(data)
	if lintErr != nil {
		return hostError(out, 11, "", lintErr)
	}
	artifact := newSpecLintArtifact(data, findings)
	if asJSON {
		if err := encodeJSON(out, artifact); err != nil {
			return hostError(out, 11, "", err)
		}
	} else {
		printSpecLintSummary(out, change, artifact)
	}
	if artifact.Passed {
		return 0
	}
	return 1
}

// specLintArtifact mirrors the spec-lint.json document written by
// Test-1CSpec.ps1:117-124; field order and JSON shape must stay identical.
type specLintArtifact struct {
	SchemaVersion int                   `json:"schema_version"`
	CheckedAtUTC  string                `json:"checked_at_utc"`
	Passed        bool                  `json:"passed"`
	Errors        []string              `json:"errors"`
	Warnings      []string              `json:"warnings"`
	Stats         specLintArtifactStats `json:"stats"`
}

type specLintArtifactStats struct {
	Characters int `json:"characters"`
	Lines      int `json:"lines"`
}

// newSpecLintArtifact folds lint findings into the PowerShell artifact shape:
// errors carry the "spec.md line N: " prefix of Add-SpecError
// (Test-1CSpec.ps1:25-27), warnings are the plain message strings, and stats
// reproduce .NET string.Length plus the "`r?`n" split line count
// (Test-1CSpec.ps1:20, 123) over the BOM-stripped text.
func newSpecLintArtifact(spec []byte, findings []specvalidate.Finding) specLintArtifact {
	text := strings.TrimPrefix(string(spec), "\uFEFF")
	artifact := specLintArtifact{
		SchemaVersion: 1,
		// [DateTime]::UtcNow.ToString('o') always emits seven fractional
		// digits and the UTC suffix.
		CheckedAtUTC: time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z"),
		Passed:       true,
		Errors:       []string{},
		Warnings:     []string{},
		Stats:        specLintArtifactStats{Characters: utf16CodeUnits(text), Lines: splitLineCount(text)},
	}
	for _, finding := range findings {
		if finding.Severity == "error" {
			artifact.Errors = append(artifact.Errors, fmt.Sprintf("spec.md line %d: %s", finding.Line, finding.Message))
			artifact.Passed = false
		} else {
			artifact.Warnings = append(artifact.Warnings, finding.Message)
		}
	}
	return artifact
}

func printSpecLintSummary(out io.Writer, change string, artifact specLintArtifact) {
	fmt.Fprintf(out, "spec lint %s\n", change)
	for _, message := range artifact.Errors {
		fmt.Fprintf(out, "error %s\n", message)
	}
	for _, message := range artifact.Warnings {
		fmt.Fprintf(out, "warning %s\n", message)
	}
	status := "FAIL"
	if artifact.Passed {
		status = "PASS"
	}
	fmt.Fprintf(out, "%s: %d errors, %d warnings, %d characters, %d lines\n",
		status, len(artifact.Errors), len(artifact.Warnings), artifact.Stats.Characters, artifact.Stats.Lines)
}

// utf16CodeUnits counts UTF-16 code units the way .NET string.Length (the
// artifact's stats.characters) does.
func utf16CodeUnits(text string) int {
	units := 0
	for _, r := range text {
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return units
}

// splitLineCount mirrors @($text -split "`r?`n").Count: every newline splits,
// a lone carriage return does not, and empty text is one line.
func splitLineCount(text string) int {
	return 1 + strings.Count(text, "\n")
}

// specFinalSummary is the machine summary of `spec final --json`.
type specFinalSummary struct {
	SchemaVersion int                       `json:"schema_version"`
	Change        string                    `json:"change"`
	Checks        []specvalidate.FinalCheck `json:"checks"`
	Passed        bool                      `json:"passed"`
}

// runSpecFinal runs ValidateFinal over the change directory with reads
// confined to it.  Exit codes: 0 every check passes, 1 otherwise, 2 invalid
// usage.
func runSpecFinal(project, change string, asJSON bool, out io.Writer) int {
	dir := specChangeDir(project, change)
	checks, err := specvalidate.ValidateFinal(dir, safeChangeReader(dir))
	if err != nil {
		return hostError(out, 11, "", err)
	}
	summary := specFinalSummary{SchemaVersion: 1, Change: change, Checks: checks, Passed: true}
	failed := 0
	for _, check := range checks {
		if !check.Pass {
			summary.Passed = false
			failed++
		}
	}
	if asJSON {
		if err := encodeJSON(out, summary); err != nil {
			return hostError(out, 11, "", err)
		}
	} else {
		printSpecFinalSummary(out, change, checks, failed)
	}
	if summary.Passed {
		return 0
	}
	return 1
}

func printSpecFinalSummary(out io.Writer, change string, checks []specvalidate.FinalCheck, failed int) {
	fmt.Fprintf(out, "spec final %s\n", change)
	for _, check := range checks {
		if check.Pass {
			fmt.Fprintf(out, "PASS %s\n", check.Name)
		} else {
			fmt.Fprintf(out, "FAIL %s: %s\n", check.Name, check.Detail)
		}
	}
	fmt.Fprintf(out, "result: %d checks, %d failed\n", len(checks), failed)
}

// safeChangeReader is the read function ValidateFinal requires: it resolves
// only plain relative names inside the change directory and refuses absolute
// paths, volume names, and any `..` traversal that would escape it.
func safeChangeReader(changeDir string) func(rel string) ([]byte, error) {
	return func(rel string) ([]byte, error) {
		if rel == "" || strings.ContainsRune(rel, 0) || filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
			return nil, fs.ErrInvalid
		}
		clean := filepath.Clean(rel)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fs.ErrInvalid
		}
		return os.ReadFile(filepath.Join(changeDir, clean))
	}
}

// encodeJSON writes an indented, HTML-unescaped document followed by one
// newline, matching how ConvertTo-Json artifacts are shaped.
func encodeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
