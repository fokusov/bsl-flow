package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"bsl-flow/cli/internal/repository"
	"bsl-flow/cli/internal/specvalidate"
)

// nativeSpecStageProvider composes the packaged PowerShell provider with a
// native, no-PowerShell execution path for the "spec" stage. Measure and every
// non-spec execution are delegated unchanged to the wrapped provider; only a
// well-formed spec-stage input is executed natively.
//
// Contract notes (source of truth:
// global/skills/1c-task/scripts/Invoke-BFNativeProvider.ps1 ->
// Task.Provider.ps1 -> Task.Stages.ps1):
//
//   - The PowerShell spec stage RUNS A WORKER SUBPROCESS first
//     (Task.Stages.ps1:378, Invoke-BFManagedWorker) to produce the
//     {spec,design} payload; the spec text is never part of the provider
//     input. This provider therefore exposes the worker as an injectable
//     seam (nativeSpecWorker). A nil seam fails closed with a typed error
//     instead of silently falling back to the PowerShell provider (the
//     native-cross-platform-cli design explicitly rejects automatic
//     native->PowerShell fallback).
//   - On a completed worker result the stage writes the same artifacts as
//     Save-BFSpec (Task.Stages.ps1:114-133): change-dir original-task.md,
//     spec.md, optional design.md and spec-lint.json, plus raw/ copies
//     payload.json, spec.md, design.md and spec-lint.json under
//     <artifact_root>/raw.
//   - Lint errors do NOT fail the provider process: Save-BFSpec's BF_BLOCKED
//     throw is caught by Invoke-BFStageObservation (Task.Stages.ps1:445-464)
//     and becomes a status "blocked" observation with raw/failure.json.
//   - Pre-dispatch failures (cancellation, changed inputs) escape
//     Invoke-BFProviderExecute (Task.Provider.ps1:456-458) as process errors;
//     this provider mirrors them as typed nativeSpecStageError values.
const (
	// nativeSpecStageContractSuffix distinguishes the composing provider in
	// diagnostics without changing the accepted wire contract: inputs still
	// carry the plain windows-ps.v1 identity and are validated as such.
	nativeSpecStageContractSuffix = "+native-spec"

	// nativeSpecStageName is the only stage executed natively; it matches the
	// provider stage list of Task.Provider.ps1:8.
	nativeSpecStageName = "spec"

	// nativeSpecTextLimit mirrors Assert-BFText's default (Task.Contracts.ps1).
	nativeSpecTextLimit = 262144
)

// nativeSpecStageError is the typed fail-closed error for failures the
// PowerShell provider surfaces as a failed provider process (pre-dispatch
// gates, observation assembly) rather than as an observation.
type nativeSpecStageError struct {
	Message string
}

func (e *nativeSpecStageError) Error() string { return e.Message }

// nativeSpecWorkerResult is the managed worker result consumed by the spec
// stage: the status/summary of Invoke-BFManagedWorker's return plus the
// materialized payload_json object ({spec: string, design: string|null}).
type nativeSpecWorkerResult struct {
	Status  string
	Summary string
	Payload map[string]any
}

// nativeSpecWorker is the injectable worker seam behind the spec stage. It
// owns every worker-side effect under <artifact_root>/raw/worker (prompt,
// process receipts, model-result.json) exactly like Invoke-BFManagedWorker;
// the stage body only consumes the returned result. An error mirrors a worker
// exception inside the stage try-block: a blocked observation.
type nativeSpecWorker func(ctx context.Context, input repository.ExecuteInput) (nativeSpecWorkerResult, error)

// nativeSpecStageProvider wraps a PowerShell provider and executes only the
// spec stage natively. It is constructible as
// nativeSpecStageProvider{inner: <ps provider>}; the worker seam is optional
// and a missing seam only fails the native path closed.
type nativeSpecStageProvider struct {
	inner  repository.Provider
	worker nativeSpecWorker
}

// nativeSpecStageContractName is a diagnostic identity marker; input/output
// contract identity itself stays the wrapped provider's.
func (p *nativeSpecStageProvider) nativeSpecStageContractName() string {
	return nativeProviderContract + nativeSpecStageContractSuffix
}

// isNativeSpecStageProvider is the marker method separating the composing
// provider from the packaged PowerShell provider in identity checks.
func (p *nativeSpecStageProvider) isNativeSpecStageProvider() bool { return true }

func (p *nativeSpecStageProvider) Measure(ctx context.Context, input repository.MeasureInput) (repository.MeasureObservation, error) {
	if p == nil || p.inner == nil {
		return repository.MeasureObservation{}, errors.New("native spec stage provider is nil")
	}
	return p.inner.Measure(ctx, input)
}

func (p *nativeSpecStageProvider) Execute(ctx context.Context, input repository.ExecuteInput) (repository.ExecuteObservation, error) {
	if p == nil || p.inner == nil {
		return repository.ExecuteObservation{}, errors.New("native spec stage provider is nil")
	}
	if !nativeSpecStageInput(input) {
		// Any shape the native path does not fully understand is delegated
		// unchanged: the wrapped provider fails closed on malformed inputs
		// exactly as it does today.
		return p.inner.Execute(ctx, input)
	}
	if p.worker == nil {
		return repository.ExecuteObservation{}, &nativeSpecStageError{
			Message: "BF_BLOCKED: native spec stage worker is not configured.",
		}
	}
	return p.executeSpecStage(ctx, input)
}

var nativeSpecUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// nativeSpecStageInput reports whether the input names the spec stage with
// the exact binding fields the PowerShell provider dispatches on
// (Assert-BFProviderInput/Assert-BFProviderAttempt, Task.Provider.ps1:
// 185-241, and Invoke-BFProviderExecute, Task.Provider.ps1:451-462). Anything
// absent or inconsistent here would be rejected by the wrapped provider as a
// process error, so the composing provider delegates instead of inventing a
// native failure.
func nativeSpecStageInput(input repository.ExecuteInput) bool {
	if input.SchemaVersion != 1 || input.Contract != nativeProviderContract || input.Operation != "execute" {
		return false
	}
	if strings.TrimSpace(input.TaskID) == "" {
		return false
	}
	if input.ProviderContract.Name != nativeProviderContract || input.ProviderContract.Version != 1 {
		return false
	}
	state := input.StateView
	if len(state) == 0 || len(input.Attempt) == 0 {
		return false
	}
	if nativeSpecString(state["stage"]) != nativeSpecStageName || nativeSpecString(state["task_id"]) != input.TaskID {
		return false
	}
	attemptID, ok := input.Attempt["attempt_id"].(string)
	if !ok || !nativeSpecUUIDPattern.MatchString(attemptID) {
		return false
	}
	if nativeSpecString(input.Attempt["task_id"]) != input.TaskID {
		return false
	}
	if nativeSpecString(input.Attempt["stage"]) != nativeSpecStageName {
		return false
	}
	if nativeSpecString(state["active_attempt"]) != attemptID {
		return false
	}
	request, ok := state["request"].(map[string]any)
	if !ok || nativeSpecString(request["request_id"]) != input.TaskID {
		return false
	}
	if strings.TrimSpace(nativeSpecString(request["prompt"])) == "" {
		return false
	}
	classification, ok := state["classification"].(map[string]any)
	if !ok {
		return false
	}
	complexity := nativeSpecString(classification["complexity"])
	risk := nativeSpecString(classification["risk"])
	if complexity != "S" && complexity != "M" && complexity != "L" {
		return false
	}
	if risk != "low" && risk != "medium" && risk != "high" {
		return false
	}
	dependencies, ok := input.Attempt["dependencies"].(map[string]any)
	if !ok {
		return false
	}
	if _, ok := dependencies["spec"].(string); !ok {
		return false
	}
	manifest, ok := input.Attempt["source_manifest"].(map[string]any)
	if !ok {
		return false
	}
	if _, ok := manifest["sha256"].(string); !ok {
		return false
	}
	workerPath := nativeSpecString(state["worker_path"])
	baseline := nativeSpecString(state["baseline"])
	projectPath := nativeSpecString(state["project_path"])
	if workerPath == "" || baseline == "" || projectPath == "" {
		return false
	}
	if nativeSpecString(input.Attempt["worker_path"]) != workerPath {
		return false
	}
	if strings.TrimSpace(input.ArtifactRoot) == "" || strings.TrimSpace(input.ContextRoot) == "" {
		return false
	}
	return true
}

// nativeSpecString projects map values to strings for discrimination.
func nativeSpecString(value any) string {
	text, _ := value.(string)
	return text
}

// executeSpecStage mirrors Invoke-BFProviderExecute + Invoke-BFStageObservation
// + Save-BFSpec + ConvertTo-BFProviderOutput for the spec stage.
func (p *nativeSpecStageProvider) executeSpecStage(ctx context.Context, input repository.ExecuteInput) (repository.ExecuteObservation, error) {
	attemptID := nativeSpecString(input.Attempt["attempt_id"])
	state := input.StateView
	request := state["request"].(map[string]any)
	classification := state["classification"].(map[string]any)
	attemptDependencies := input.Attempt["dependencies"].(map[string]any)
	attemptManifestSHA := input.Attempt["source_manifest"].(map[string]any)["sha256"].(string)

	// Pre-dispatch cancellation (Task.Provider.ps1:456). The throw escapes the
	// provider process in PowerShell; mirror it as a typed error.
	cancelled, err := nativeSpecCancelled(input.CancelSignal, input.TaskID, attemptID)
	if err != nil {
		return repository.ExecuteObservation{}, &nativeSpecStageError{Message: err.Error()}
	}
	if cancelled {
		return repository.ExecuteObservation{}, &nativeSpecStageError{
			Message: "BF_BLOCKED: cancelled before provider dispatch.",
		}
	}

	// Pre-dispatch input binding (Task.Provider.ps1:457-458), restricted to
	// the spec-input key the native path can recompute without the
	// architecture/execution dependency ports. The controller independently
	// revalidates the full dependency object after execution
	// (repository validateExecuteObservation).
	changeDir := nativeSpecChangePath(nativeSpecString(state["project_path"]), input.TaskID)
	beforeSpecHash, err := nativeSpecInputsHash(changeDir)
	if err != nil {
		return repository.ExecuteObservation{}, &nativeSpecStageError{Message: err.Error()}
	}
	if beforeSpecHash != nativeSpecString(attemptDependencies["spec"]) {
		return repository.ExecuteObservation{}, &nativeSpecStageError{
			Message: "BF_BLOCKED: provider inputs changed before dispatch.",
		}
	}

	rawDir := filepath.Join(input.ArtifactRoot, "raw")

	outcome := "BLOCKED"
	summary := "Attempt did not complete."
	caught := false
	var proposal map[string]any
	stageErr := func() error {
		// raw/ is created by the stage observation itself
		// (Task.Stages.ps1:339).
		if err := repository.SafeMkdir(rawDir); err != nil {
			return err
		}

		result, workerErr := p.worker(ctx, input)
		if workerErr != nil {
			message := workerErr.Error()
			if strings.TrimSpace(message) == "" {
				message = "BF_BLOCKED: native spec stage failed."
			}
			return errors.New(message)
		}
		summary = result.Summary

		switch result.Status {
		case "needs_input":
			outcome = "NEEDS_INPUT"
			return nil
		case "failed":
			outcome = "FAIL"
			return nil
		case "blocked":
			outcome = "BLOCKED"
			return nil
		case "completed":
			outcome = "PASS"
		default:
			return errors.New("BF_INVALID: invalid worker stage status.")
		}

		// Read-BFPayload (Task.Stages.ps1:4-9, 386): raw/payload.json is
		// written only when missing. The PowerShell bytes are the worker's
		// own payload_json string; the native seam supplies the materialized
		// object, so canonical bytes are written (content-equal).
		payload := result.Payload
		if payload == nil {
			return errors.New("BF_INVALID: spec must be an object.")
		}
		if _, present := payload["spec"]; !present {
			return errors.New("BF_INVALID: spec.spec is required.")
		}
		if _, present := payload["design"]; !present {
			return errors.New("BF_INVALID: spec.design is required.")
		}
		for key := range payload {
			if key != "spec" && key != "design" {
				return fmt.Errorf("BF_INVALID: unknown field spec.%s.", key)
			}
		}
		spec, ok := payload["spec"].(string)
		if !ok || !nativeSpecValidText(spec) {
			return errors.New("BF_INVALID: invalid spec.")
		}
		design, hasDesign := payload["design"].(string)
		if !hasDesign && payload["design"] != nil {
			return errors.New("BF_INVALID: invalid spec.")
		}
		payloadPath := filepath.Join(rawDir, "payload.json")
		if _, statErr := os.Lstat(payloadPath); os.IsNotExist(statErr) {
			data, err := repository.Canonical(payload)
			if err != nil {
				return err
			}
			if err := os.WriteFile(payloadPath, data, 0o644); err != nil {
				return err
			}
		}

		// Save-BFSpec (Task.Stages.ps1:114-133).
		complexity := nativeSpecString(classification["complexity"])
		risk := nativeSpecString(classification["risk"])
		if !nativeSpecClassificationLine(spec, "Complexity", "Сложность", complexity) {
			return errors.New("BF_BLOCKED: spec classification differs from controller route.")
		}
		if !nativeSpecClassificationLine(spec, "Risk", "Риск", risk) {
			return errors.New("BF_BLOCKED: spec risk differs from controller route.")
		}
		if (complexity == "L" || risk == "high") && !nativeSpecValidText(design) {
			return errors.New("BF_INVALID: invalid required design.")
		}
		if err := repository.SafeMkdir(changeDir); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(changeDir, "original-task.md"), []byte(nativeSpecString(request["prompt"])), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(changeDir, "spec.md"), []byte(spec), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(rawDir, "spec.md"), []byte(spec), 0o644); err != nil {
			return err
		}
		if hasDesign {
			if err := os.WriteFile(filepath.Join(changeDir, "design.md"), []byte(design), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(rawDir, "design.md"), []byte(design), 0o644); err != nil {
				return err
			}
		} else if _, statErr := os.Lstat(filepath.Join(changeDir, "design.md")); statErr == nil {
			return errors.New("BF_BLOCKED: removing an existing design requires an explicit scope update.")
		} else if !os.IsNotExist(statErr) {
			return statErr
		}

		// Mandatory lint (Task.Stages.ps1:129-132): lint the written bytes,
		// persist the Test-1CSpec.ps1 artifact into the change directory and
		// the raw copy, then fail blocked on any error finding.
		written, err := repository.ReadFileBytes(filepath.Join(changeDir, "spec.md"))
		if err != nil {
			return err
		}
		findings, lintErr := specvalidate.LintSpec(written)
		if lintErr != nil {
			return lintErr
		}
		artifact := newSpecLintArtifact(written, findings)
		lintBytes, err := nativeSpecLintArtifactBytes(artifact)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(changeDir, "spec-lint.json"), lintBytes, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(rawDir, "spec-lint.json"), lintBytes, 0o644); err != nil {
			return err
		}
		if !artifact.Passed {
			return fmt.Errorf("BF_BLOCKED: generated specification failed mandatory lint: %s", strings.Join(artifact.Errors, "; "))
		}

		proposal = map[string]any{"spec": spec, "design": payload["design"]}
		return nil
	}()
	if stageErr != nil {
		// Catch block (Task.Stages.ps1:445-464): any throw becomes BLOCKED
		// with the message as summary and a failure.json sidecar.
		outcome = "BLOCKED"
		summary = stageErr.Error()
		caught = true
		proposal = nil
	}

	// Post-stage source gate (Task.Stages.ps1:437-444) inside the try, then
	// ConvertTo-BFProviderOutput's manifest recovery (Task.Provider.ps1:
	// 390-397): a completed observation requires the manifest (its failure is
	// a process error), any other outcome carries it when computable.
	manifest, manifestErr := nativeSpecSourceManifest(nativeSpecString(state["worker_path"]), nativeSpecString(state["baseline"]))
	if manifestErr != nil {
		if stageErr == nil && outcome == "PASS" {
			outcome = "BLOCKED"
			summary = manifestErr.Error()
			caught = true
		}
		manifest = nil
	} else if stageErr == nil {
		if nativeSpecString(manifest["sha256"]) != attemptManifestSHA {
			outcome = "BLOCKED"
			summary = "BF_BLOCKED: source changed during a read-only or verification stage."
			caught = true
			proposal = nil
		}
	}

	if caught {
		if err := nativeSpecWriteFailureJSON(rawDir, summary); err != nil {
			return repository.ExecuteObservation{}, &nativeSpecStageError{Message: err.Error()}
		}
	}

	dependencies := attemptDependencies
	if outcome == "PASS" {
		// Final dependency binding (Task.Stages.ps1:465-477): only the spec
		// output key may differ from the attempt binding, and it carries the
		// post-save spec inputs hash. The PowerShell provider re-derives every
		// dependency key here; the native slice clones the attempt binding
		// and refreshes the spec key, relying on the controller's independent
		// full-dependency equality check.
		updated := make(map[string]any, len(attemptDependencies))
		for key, value := range attemptDependencies {
			updated[key] = value
		}
		afterSpecHash, err := nativeSpecInputsHash(changeDir)
		if err != nil {
			return repository.ExecuteObservation{}, &nativeSpecStageError{Message: err.Error()}
		}
		updated["spec"] = afterSpecHash
		dependencies = updated
	}

	status := "blocked"
	switch outcome {
	case "PASS":
		status = "completed"
	case "NEEDS_INPUT":
		status = "needs_input"
	case "FAIL":
		status = "failed"
	}

	artifacts, err := nativeSpecArtifactManifest(input.ArtifactRoot)
	if err != nil {
		return repository.ExecuteObservation{}, &nativeSpecStageError{Message: err.Error()}
	}
	processes, err := nativeSpecProcessReceipts(input.ArtifactRoot)
	if err != nil {
		return repository.ExecuteObservation{}, &nativeSpecStageError{Message: err.Error()}
	}

	return repository.ExecuteObservation{
		SchemaVersion:    1,
		Contract:         nativeProviderContract,
		TaskID:           input.TaskID,
		AttemptID:        attemptID,
		Stage:            nativeSpecStageName,
		Status:           status,
		Summary:          summary,
		Proposal:         proposal,
		SideEffects:      "none",
		Dependencies:     dependencies,
		SourceManifest:   manifest,
		Artifacts:        artifacts,
		ProcessReceipt:   map[string]any{"processes": processes},
		ProviderContract: input.ProviderContract,
	}, nil
}

// nativeSpecChangePath mirrors Get-BFChangePath (Task.Gates.ps1:90-93).
func nativeSpecChangePath(projectPath, taskID string) string {
	return filepath.Join(projectPath, "openspec", "changes", "bsl-flow-"+taskID)
}

// nativeSpecInputsHash mirrors Get-BFSpecInputs + Get-BFHash
// (Task.Gates.ps1:95-104): sha256 per existing sidecar, null when absent,
// hashed as canonical JSON.
func nativeSpecInputsHash(changeDir string) (string, error) {
	inputs := map[string]any{}
	for _, name := range []string{"original-task.md", "spec.md", "design.md"} {
		data, err := repository.ReadFileBytes(filepath.Join(changeDir, name))
		if err != nil {
			if os.IsNotExist(err) {
				inputs[name] = nil
				continue
			}
			return "", err
		}
		inputs[name] = bytesSHA256(data)
	}
	return repository.Hash(inputs)
}

// nativeSpecValidText mirrors Assert-BFText (Task.Contracts.ps1): non-blank
// string within the UTF-16 length bound.
func nativeSpecValidText(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	units := 0
	for _, r := range value {
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return units <= nativeSpecTextLimit
}

// nativeSpecClassificationLine mirrors the Save-BFSpec classification checks
// (Task.Stages.ps1:119-120): a "- Label: value" line with the English or
// Russian label, PowerShell's default case-insensitive matching included.
func nativeSpecClassificationLine(spec, label, russianLabel, value string) bool {
	for _, name := range []string{label, russianLabel} {
		pattern := regexp.MustCompile(`(?im)^- ` + regexp.QuoteMeta(name) + `: ` + regexp.QuoteMeta(value) + `\s*$`)
		if pattern.MatchString(spec) {
			return true
		}
	}
	return false
}

// nativeSpecLintArtifactBytes serializes the spec-lint.json document exactly
// like Test-1CSpec.ps1:117-126 through Write-BSLFlowJsonAtomic: PowerShell 7
// ConvertTo-Json formatting (two-space indent, no HTML escaping, empty arrays
// as [], trailing newline) with CRLF line separators as Set-Content writes
// them for the Windows-targeted provider contract.
func nativeSpecLintArtifactBytes(artifact specLintArtifact) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(artifact); err != nil {
		return nil, err
	}
	return bytes.ReplaceAll(buffer.Bytes(), []byte("\n"), []byte("\r\n")), nil
}

// nativeSpecWriteFailureJSON mirrors the catch block's Write-BFJson
// (Task.Stages.ps1:463): canonical JSON, refusing to overwrite an existing
// failure receipt exactly like Write-BFJson without -Replace.
func nativeSpecWriteFailureJSON(rawDir, summary string) error {
	data, err := repository.Canonical(map[string]any{"reason": summary, "side_effects": "none"})
	if err != nil {
		return err
	}
	return repository.AtomicWrite(filepath.Join(rawDir, "failure.json"), data, false)
}

// nativeSpecCancelled mirrors Test-BFProviderCancelled
// (Task.Provider.ps1:159-169).
func nativeSpecCancelled(signalPath, taskID, attemptID string) (bool, error) {
	if strings.TrimSpace(signalPath) == "" {
		return false, nil
	}
	full, err := repository.SafePath(signalPath)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	data, err := repository.ReadFileBytes(full)
	if err != nil {
		return false, err
	}
	trimmed, err := strictJSONDocument(data)
	if err != nil {
		return false, errors.New("BF_BLOCKED: cancellation signal is malformed.")
	}
	signal, err := nativeSpecStrictObject(trimmed, []string{"schema_version", "task_id", "attempt_id", "cancelled"}, []string{"reason"}, "cancel_signal")
	if err != nil {
		return false, errors.New("BF_BLOCKED: cancellation signal is malformed.")
	}
	version, ok := signal["schema_version"].(json.Number)
	if !ok || version.String() != "1" ||
		nativeSpecString(signal["task_id"]) != taskID ||
		nativeSpecString(signal["attempt_id"]) != attemptID {
		return false, errors.New("BF_BLOCKED: cancellation signal identity mismatch.")
	}
	cancelled, ok := signal["cancelled"].(bool)
	if !ok {
		return false, errors.New("BF_BLOCKED: cancellation signal has an invalid cancelled flag.")
	}
	return cancelled, nil
}

// nativeSpecGitOutput mirrors Invoke-BFGit (Task.Gates.ps1:5-20).
func nativeSpecGitOutput(root string, arguments ...string) (string, error) {
	command := exec.Command("git", append([]string{"-c", "core.hooksPath=NUL", "-c", "core.fsmonitor=false", "-C", root}, arguments...)...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String() + "\n" + stdout.String())
		return "", fmt.Errorf("BF_BLOCKED: Git failed: %s", message)
	}
	return strings.Trim(stdout.String(), "\r\n"), nil
}

var nativeSpecExcludedPath = regexp.MustCompile(`(^|/)(\.git|\.bsl-flow|\.bsl-flow-worker)(/|$)`)

// nativeSpecSourceManifest mirrors Get-BFSourceManifest
// (Task.Gates.ps1:22-60): bind every worker file to the baseline, include
// baseline deletions, exclude controller/runtime directories, and hash the
// ordered entry array canonically.
func nativeSpecSourceManifest(root, baseline string) (map[string]any, error) {
	fullRoot, err := repository.SafePath(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(fullRoot)
	if err != nil || !info.IsDir() {
		return nil, errors.New("BF_BLOCKED: worktree is missing.")
	}
	head, err := nativeSpecGitOutput(fullRoot, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	if head != baseline {
		return nil, errors.New("BF_BLOCKED: worker HEAD changed; explicit scope reconciliation is required.")
	}
	entries := map[string]map[string]any{}
	walkErr := filepath.WalkDir(fullRoot, func(full string, dirEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relErr := filepath.Rel(fullRoot, full)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		if nativeSpecExcludedPath.MatchString(relative) {
			if dirEntry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if dirEntry.IsDir() {
			return nil
		}
		if !dirEntry.Type().IsRegular() {
			return fmt.Errorf("BF_BLOCKED: source path is not a regular file: %s", relative)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		entries[relative] = map[string]any{"path": relative, "sha256": bytesSHA256(data), "deleted": false}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	baselineText, err := nativeSpecGitOutput(fullRoot, "-c", "core.quotePath=false", "ls-tree", "-r", "--name-only", baseline)
	if err != nil {
		return nil, err
	}
	for _, path := range strings.Split(baselineText, "\n") {
		path = strings.TrimSuffix(path, "\r")
		if path == "" || nativeSpecExcludedPath.MatchString(path) {
			continue
		}
		if _, present := entries[path]; !present {
			entries[path] = map[string]any{"path": path, "sha256": nil, "deleted": true}
		}
	}
	if len(entries) == 0 {
		return nil, errors.New("BF_BLOCKED: empty source manifest.")
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	files := make([]any, 0, len(names))
	for _, name := range names {
		files = append(files, entries[name])
	}
	hash, err := repository.Hash(files)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"schema_version": int64(1),
		"baseline":       baseline,
		"source_paths":   []any{"."},
		"files":          files,
		"sha256":         hash,
	}, nil
}

var (
	nativeSpecKindBudget       = regexp.MustCompile(`(^|/)budget(/|$)`)
	nativeSpecKindProcessJSON  = regexp.MustCompile(`(^|/)(process|exit)\.json$`)
	nativeSpecKindProcessText  = regexp.MustCompile(`(^|/)(stdout|stderr)\.txt$`)
	nativeSpecKindModel        = regexp.MustCompile(`model-result\.json$`)
	nativeSpecKindReview       = regexp.MustCompile(`review|reconciliation|spec-lint`)
	nativeSpecKindVerification = regexp.MustCompile(`verification|observations|junit|coverage`)
	nativeSpecKindFailure      = regexp.MustCompile(`failure\.json$`)
)

// nativeSpecArtifactKind mirrors Get-BFProviderArtifactKind
// (Task.Provider.ps1:328-338).
func nativeSpecArtifactKind(relative string) string {
	lower := strings.ToLower(relative)
	if nativeSpecKindBudget.MatchString(lower) {
		return "budget"
	}
	if nativeSpecKindProcessJSON.MatchString(lower) || nativeSpecKindProcessText.MatchString(lower) {
		return "process"
	}
	if nativeSpecKindModel.MatchString(lower) {
		return "model"
	}
	if nativeSpecKindReview.MatchString(lower) {
		return "review"
	}
	if nativeSpecKindVerification.MatchString(lower) {
		return "verification"
	}
	if nativeSpecKindFailure.MatchString(lower) {
		return "failure"
	}
	return "raw"
}

var nativeSpecStateSegments = map[string]bool{
	"current": true, "revisions": true, "inputs": true, "acceptance": true,
	"current.json": true, "acceptance.json": true,
}

// nativeSpecArtifactManifest mirrors Get-BFProviderArtifactManifest
// (Task.Provider.ps1:340-352): every non-temporary file under the artifact
// root, hash-bound, with the provider kind classification, rejecting any
// controller-state path.
func nativeSpecArtifactManifest(root string) ([]repository.ArtifactRef, error) {
	fullRoot, err := repository.SafePath(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(fullRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return []repository.ArtifactRef{}, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("BF_INVALID: artifact root is not a directory.")
	}
	var refs []repository.ArtifactRef
	walkErr := filepath.WalkDir(fullRoot, func(full string, dirEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if dirEntry.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(dirEntry.Name()), ".tmp") {
			return nil
		}
		relative, relErr := filepath.Rel(fullRoot, full)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		for _, segment := range strings.Split(relative, "/") {
			if nativeSpecStateSegments[segment] {
				return fmt.Errorf("BF_BLOCKED: provider artifact contains controller state: %s", relative)
			}
		}
		if !dirEntry.Type().IsRegular() {
			return fmt.Errorf("BF_INVALID: provider artifact is not a regular file: %s", relative)
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		refs = append(refs, repository.ArtifactRef{
			Path:      relative,
			SHA256:    bytesSHA256(data),
			SizeBytes: int64(len(data)),
			Kind:      nativeSpecArtifactKind(relative),
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(refs, func(i, j int) bool {
		left, right := strings.ToLower(refs[i].Path), strings.ToLower(refs[j].Path)
		if left != right {
			return left < right
		}
		return refs[i].Path < refs[j].Path
	})
	return refs, nil
}

// nativeSpecProcessReceipts mirrors Get-BFProviderProcessReceipts
// (Task.Provider.ps1:354-369): every exit.json with its process identity and
// stream receipts, hash-bound and path-confined.
func nativeSpecProcessReceipts(root string) ([]any, error) {
	fullRoot, err := repository.SafePath(root)
	if err != nil {
		return nil, err
	}
	var exits []string
	walkErr := filepath.WalkDir(fullRoot, func(full string, dirEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if dirEntry.IsDir() {
			return nil
		}
		if strings.EqualFold(dirEntry.Name(), "exit.json") {
			exits = append(exits, full)
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Strings(exits)
	receipts := []any{}
	for _, exitPath := range exits {
		processPath := filepath.Join(filepath.Dir(exitPath), "process.json")
		exitData, err := repository.ReadFileBytes(exitPath)
		if err != nil {
			return nil, err
		}
		exitObject, err := nativeSpecStrictObject(exitData, []string{"exit_code", "stop_reason", "elapsed_seconds", "process_id", "executable", "stdout", "stderr"}, nil, "process_exit")
		if err != nil {
			return nil, err
		}
		processData, err := repository.ReadFileBytes(processPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, errors.New("BF_BLOCKED: native process exit receipt has no process identity.")
			}
			return nil, err
		}
		// Assert-BFFields on the process identity (Task.Provider.ps1:362); the
		// receipt itself carries only the file hash.
		if _, err := nativeSpecStrictObject(processData, []string{"pid", "start_time_utc", "executable", "arguments_sha256"}, nil, "process_identity"); err != nil {
			return nil, err
		}
		stdoutPath, stderrPath := nativeSpecString(exitObject["stdout"]), nativeSpecString(exitObject["stderr"])
		var stdoutData, stderrData []byte
		for _, stream := range []struct {
			path  string
			value *[]byte
		}{{stdoutPath, &stdoutData}, {stderrPath, &stderrData}} {
			data, err := repository.ReadFileBytes(stream.path)
			if err != nil {
				return nil, errors.New("BF_BLOCKED: native process stream receipt is missing.")
			}
			*stream.value = data
		}
		processRelative, err := nativeSpecArtifactRelativePath(fullRoot, processPath)
		if err != nil {
			return nil, err
		}
		exitRelative, err := nativeSpecArtifactRelativePath(fullRoot, exitPath)
		if err != nil {
			return nil, err
		}
		stdoutRelative, err := nativeSpecArtifactRelativePath(fullRoot, stdoutPath)
		if err != nil {
			return nil, err
		}
		stderrRelative, err := nativeSpecArtifactRelativePath(fullRoot, stderrPath)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, map[string]any{
			"process_path":   processRelative,
			"process_sha256": bytesSHA256(processData),
			"exit_path":      exitRelative,
			"exit_sha256":    bytesSHA256(exitData),
			"stdout_path":    stdoutRelative,
			"stdout_sha256":  bytesSHA256(stdoutData),
			"stderr_path":    stderrRelative,
			"stderr_sha256":  bytesSHA256(stderrData),
			"exit_code":      exitObject["exit_code"],
			"stop_reason":    exitObject["stop_reason"],
		})
	}
	return receipts, nil
}

// nativeSpecArtifactRelativePath mirrors
// ConvertTo-BFProviderArtifactRelativePath (Task.Provider.ps1:372-380).
func nativeSpecArtifactRelativePath(root, path string) (string, error) {
	full, err := repository.SafePath(path)
	if err != nil {
		return "", err
	}
	relative, relErr := filepath.Rel(root, full)
	if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("BF_INVALID: process receipt escaped artifact_root.")
	}
	return filepath.ToSlash(relative), nil
}

// nativeSpecStrictObject mirrors Read-BFJson + Assert-BFFields: one strict
// JSON object with exactly the required and optional fields.
func nativeSpecStrictObject(data []byte, required, optional []string, name string) (map[string]any, error) {
	trimmed, err := strictJSONDocument(data)
	if err != nil {
		return nil, fmt.Errorf("BF_INVALID: %s is malformed: %v", name, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, fmt.Errorf("BF_INVALID: %s must be an object.", name)
	}
	for _, key := range required {
		if _, present := object[key]; !present {
			return nil, fmt.Errorf("BF_INVALID: %s.%s is required.", name, key)
		}
	}
	allowed := map[string]bool{}
	for _, key := range append(append([]string{}, required...), optional...) {
		allowed[key] = true
	}
	for key := range object {
		if !allowed[key] {
			return nil, fmt.Errorf("BF_INVALID: unknown field %s.%s.", name, key)
		}
	}
	return object, nil
}
