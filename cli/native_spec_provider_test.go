package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"bsl-flow/cli/internal/repository"
	"bsl-flow/cli/internal/specvalidate"
)

// The fixture spec mirrors the deterministic medium spec of the native
// controller integration fixture: classification M/low and every lint rule of
// the real Test-1CSpec.ps1 satisfied.
const nativeSpecStageFixtureSpec = `## Classification

- Complexity: M
- Risk: low

## Goal

Keep the deterministic fixture readme intact through the complete native spec stage.

## Required behavior

The fixture treats readme.txt as the single tracked source artifact of the change and never renames or relocates it.

## 1C context

Configuration/subsystem: none, this fixture is source-only and does not touch a 1C runtime.

## Non-goals

No runtime 1C changes, no metadata writes and no database access are part of this fixture.

## Acceptance criteria

- GIVEN the fixture readme exists with its marker WHEN the native spec stage completes THEN readme.txt still contains the original marker text.

## Required verification

- [x] Static: the deterministic file assertion re-reads readme.txt and proves the marker survives the native route.

## Uncertainties / assumptions

None; the fixture is fully deterministic and carries no external dependencies.
`

const (
	nativeSpecStageFixtureTaskID    = "01234567-89ab-cdef-0123-456789abcdef"
	nativeSpecStageFixtureAttemptID = "fedcba98-7654-3210-fedc-ba9876543210"
	nativeSpecStageFixturePrompt    = "Run the deterministic native spec stage fixture."
)

var nativeSpecStageCheckedAt = regexp.MustCompile(`"checked_at_utc": "([^"]+)"`)
var nativeSpecStageTimestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{7}Z$`)

// nativeSpecStageInner records every delegated call.
type nativeSpecStageInner struct {
	measureCalls  int
	executeCalls  int
	lastExecute   repository.ExecuteInput
	lastMeasure   repository.MeasureInput
	measureResult repository.MeasureObservation
	executeResult repository.ExecuteObservation
	executeErr    error
}

func (inner *nativeSpecStageInner) Measure(ctx context.Context, input repository.MeasureInput) (repository.MeasureObservation, error) {
	inner.measureCalls++
	inner.lastMeasure = input
	return inner.measureResult, nil
}

func (inner *nativeSpecStageInner) Execute(ctx context.Context, input repository.ExecuteInput) (repository.ExecuteObservation, error) {
	inner.executeCalls++
	inner.lastExecute = input
	return inner.executeResult, inner.executeErr
}

type nativeSpecStageFixture struct {
	project      string
	worker       string
	artifactRoot string
	contextRoot  string
	cancelSignal string
	changeDir    string
	manifestSHA  string
	input        repository.ExecuteInput
	inner        *nativeSpecStageInner
	workerResult nativeSpecWorkerResult
	workerErr    error
	workerCalls  int
}

func newNativeSpecStageFixture(t *testing.T) *nativeSpecStageFixture {
	t.Helper()
	base, err := os.MkdirTemp("", "bsl-flow-native-spec-stage-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	fixture := &nativeSpecStageFixture{
		project:      filepath.Join(base, "project"),
		worker:       newNativeControllerGitRepository(t),
		artifactRoot: filepath.Join(base, "artifacts"),
		contextRoot:  filepath.Join(base, "context"),
	}
	fixture.changeDir = nativeSpecChangePath(fixture.project, nativeSpecStageFixtureTaskID)
	fixture.cancelSignal = filepath.Join(fixture.contextRoot, "cancel.signal")
	for _, directory := range []string{fixture.project, fixture.artifactRoot, fixture.contextRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	baseline := nativeControllerRunGit(t, fixture.worker, "rev-parse", "HEAD")

	// Independent manifest binding over the three committed fixture files.
	entries := []any{}
	for _, name := range []string{".gitignore", "bsl-flow.yaml", "readme.txt"} {
		data, err := os.ReadFile(filepath.Join(fixture.worker, name))
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, map[string]any{"path": name, "sha256": bytesSHA256(data), "deleted": false})
	}
	manifestSHA, err := repository.Hash(entries)
	if err != nil {
		t.Fatal(err)
	}
	fixture.manifestSHA = manifestSHA

	specInputs, err := repository.Hash(map[string]any{"original-task.md": nil, "spec.md": nil, "design.md": nil})
	if err != nil {
		t.Fatal(err)
	}
	fixture.input = repository.ExecuteInput{
		SchemaVersion: 1,
		Contract:      nativeProviderContract,
		Operation:     "execute",
		TaskID:        nativeSpecStageFixtureTaskID,
		StateView: map[string]any{
			"schema_version": int64(1),
			"task_id":        nativeSpecStageFixtureTaskID,
			"stage":          "spec",
			"status":         "running",
			"active_attempt": nativeSpecStageFixtureAttemptID,
			"project_path":   fixture.project,
			"worker_path":    fixture.worker,
			"baseline":       baseline,
			"classification": map[string]any{"complexity": "M", "risk": "low"},
			"intent_hash":    "intent",
			"policy_hash":    "policy",
			"request":        map[string]any{"request_id": nativeSpecStageFixtureTaskID, "prompt": nativeSpecStageFixturePrompt},
		},
		Attempt: map[string]any{
			"schema_version": int64(1),
			"task_id":        nativeSpecStageFixtureTaskID,
			"attempt_id":     nativeSpecStageFixtureAttemptID,
			"stage":          "spec",
			"worker_path":    fixture.worker,
			"dependencies":   map[string]any{"intent": "intent", "policy": "policy", "spec": specInputs},
			"source_manifest": map[string]any{
				"schema_version": int64(1),
				"baseline":       baseline,
				"sha256":         manifestSHA,
			},
		},
		ContextRoot:        fixture.contextRoot,
		ArtifactRoot:       fixture.artifactRoot,
		CanonicalStoreRoot: filepath.Join(base, "store"),
		CancelSignal:       fixture.cancelSignal,
		ProviderContract: repository.ProviderContractIdentity{
			Name:                nativeProviderContract,
			Version:             1,
			HostSHA256:          strings.Repeat("a", 64),
			ProviderSHA256:      strings.Repeat("b", 64),
			AssetManifestSHA256: strings.Repeat("c", 64),
		},
		PriorArtifacts: []repository.ArtifactRef{},
	}
	fixture.inner = &nativeSpecStageInner{}
	fixture.workerResult = nativeSpecWorkerResult{
		Status:  "completed",
		Summary: "Deterministic native fixture worker completed spec.",
		Payload: map[string]any{"spec": nativeSpecStageFixtureSpec, "design": nil},
	}
	return fixture
}

func (fixture *nativeSpecStageFixture) provider() *nativeSpecStageProvider {
	return &nativeSpecStageProvider{
		inner: fixture.inner,
		worker: func(ctx context.Context, input repository.ExecuteInput) (nativeSpecWorkerResult, error) {
			fixture.workerCalls++
			return fixture.workerResult, fixture.workerErr
		},
	}
}

func (fixture *nativeSpecStageFixture) artifactMap(observation repository.ExecuteObservation) map[string]repository.ArtifactRef {
	refs := map[string]repository.ArtifactRef{}
	for _, artifact := range observation.Artifacts {
		refs[artifact.Path] = artifact
	}
	return refs
}

func TestNativeSpecStageExecuteCompletedWritesByteIdenticalArtifacts(t *testing.T) {
	fixture := newNativeSpecStageFixture(t)
	observation, err := fixture.provider().Execute(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("native spec execute failed: %v", err)
	}
	if fixture.workerCalls != 1 || fixture.inner.executeCalls != 0 {
		t.Fatalf("worker=%d inner=%d calls, want worker=1 inner=0", fixture.workerCalls, fixture.inner.executeCalls)
	}
	if observation.SchemaVersion != 1 || observation.Contract != nativeProviderContract ||
		observation.TaskID != nativeSpecStageFixtureTaskID || observation.AttemptID != nativeSpecStageFixtureAttemptID ||
		observation.Stage != "spec" || observation.Status != "completed" || observation.SideEffects != "none" {
		t.Fatalf("observation identity/shape is wrong: %+v", observation)
	}
	if observation.Summary != "Deterministic native fixture worker completed spec." {
		t.Fatalf("observation summary=%q", observation.Summary)
	}
	if !reflect.DeepEqual(observation.Proposal, map[string]any{"spec": nativeSpecStageFixtureSpec, "design": nil}) {
		t.Fatalf("observation proposal differs from the worker payload: %#v", observation.Proposal)
	}
	if observation.ProviderContract != fixture.input.ProviderContract {
		t.Fatalf("provider contract identity was not passed through: %+v", observation.ProviderContract)
	}
	if observation.Transport != nil {
		t.Fatalf("native execution must not manufacture transport evidence")
	}

	// Source manifest: fresh enumeration, bound to the attempt binding.
	if observation.SourceManifest == nil || observation.SourceManifest["sha256"] != fixture.manifestSHA {
		t.Fatalf("source manifest is missing or unbound: %#v", observation.SourceManifest)
	}

	// Dependencies: only the spec output key may refresh.
	expectedSpecInputs, err := repository.Hash(map[string]any{
		"original-task.md": bytesSHA256([]byte(nativeSpecStageFixturePrompt)),
		"spec.md":          bytesSHA256([]byte(nativeSpecStageFixtureSpec)),
		"design.md":        nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Dependencies["spec"] != expectedSpecInputs {
		t.Fatalf("dependency spec key=%v, want refreshed spec inputs hash", observation.Dependencies["spec"])
	}
	if observation.Dependencies["intent"] != "intent" || observation.Dependencies["policy"] != "policy" {
		t.Fatalf("non-spec dependency keys changed: %#v", observation.Dependencies)
	}

	// Change-directory artifacts (Save-BFSpec bytes).
	if data, err := os.ReadFile(filepath.Join(fixture.changeDir, "original-task.md")); err != nil || string(data) != nativeSpecStageFixturePrompt {
		t.Fatalf("original-task.md mismatch: %q %v", data, err)
	}
	specBytes := []byte(nativeSpecStageFixtureSpec)
	if data, err := os.ReadFile(filepath.Join(fixture.changeDir, "spec.md")); err != nil || !bytes.Equal(data, specBytes) {
		t.Fatalf("change spec.md mismatch: %d bytes %v", len(data), err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.changeDir, "design.md")); !os.IsNotExist(err) {
		t.Fatalf("design.md must not exist for a null design: %v", err)
	}

	rawLint, err := os.ReadFile(filepath.Join(fixture.artifactRoot, "raw", "spec-lint.json"))
	if err != nil {
		t.Fatalf("raw spec-lint.json was not written: %v", err)
	}
	changeLint, err := os.ReadFile(filepath.Join(fixture.changeDir, "spec-lint.json"))
	if err != nil {
		t.Fatalf("change spec-lint.json was not written: %v", err)
	}
	if !bytes.Equal(rawLint, changeLint) {
		t.Fatalf("spec-lint.json copies differ between change dir and raw dir")
	}

	// Byte parity: rebuild the artifact from the specvalidate findings with
	// the written timestamp and require identical bytes.
	match := nativeSpecStageCheckedAt.FindSubmatch(rawLint)
	if match == nil || !nativeSpecStageTimestamp.Match(match[1]) {
		t.Fatalf("spec-lint.json checked_at_utc is missing or malformed: %s", rawLint)
	}
	findings, lintErr := specvalidate.LintSpec(specBytes)
	if lintErr != nil {
		t.Fatal(lintErr)
	}
	for _, finding := range findings {
		if finding.Severity == "error" {
			t.Fatalf("fixture spec must lint clean, got %#v", finding)
		}
	}
	artifact := newSpecLintArtifact(specBytes, findings)
	artifact.CheckedAtUTC = string(match[1])
	expected, err := nativeSpecLintArtifactBytes(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expected, rawLint) {
		t.Fatalf("spec-lint.json byte parity failed:\nwant: %q\ngot:  %q", expected, rawLint)
	}

	// Declared artifact manifest binds the real files with PS kinds.
	refs := fixture.artifactMap(observation)
	lintRef, ok := refs["raw/spec-lint.json"]
	if !ok || lintRef.Kind != "review" || lintRef.SizeBytes != int64(len(rawLint)) || lintRef.SHA256 != bytesSHA256(rawLint) {
		t.Fatalf("raw/spec-lint.json artifact descriptor is wrong: %+v", lintRef)
	}
	specRef, ok := refs["raw/spec.md"]
	if !ok || specRef.Kind != "raw" || specRef.SizeBytes != int64(len(specBytes)) {
		t.Fatalf("raw/spec.md artifact descriptor is wrong: %+v", specRef)
	}
	payloadRef, ok := refs["raw/payload.json"]
	if !ok || payloadRef.Kind != "raw" {
		t.Fatalf("raw/payload.json artifact descriptor is wrong: %+v", payloadRef)
	}
	payloadData, err := os.ReadFile(filepath.Join(fixture.artifactRoot, "raw", "payload.json"))
	if err != nil {
		t.Fatal(err)
	}
	var payloadValue map[string]any
	if err := json.Unmarshal(payloadData, &payloadValue); err != nil {
		t.Fatal(err)
	}
	if payloadValue["spec"] != nativeSpecStageFixtureSpec || payloadValue["design"] != nil {
		t.Fatalf("raw payload.json content is wrong: %s", payloadData)
	}
	if _, err := os.Lstat(filepath.Join(fixture.artifactRoot, "raw", "failure.json")); !os.IsNotExist(err) {
		t.Fatalf("a completed spec stage must not retain failure.json: %v", err)
	}
	if len(observation.ProcessReceipt["processes"].([]any)) != 0 {
		t.Fatalf("no worker process receipts are expected from the seam: %#v", observation.ProcessReceipt)
	}
}

// TestNativeSpecStageLintArtifactMatchesPowerShellBytes proves the serialized
// artifact bytes equal a real Test-1CSpec.ps1-produced spec-lint.json for the
// repository's own change (modulo the checked_at_utc timestamp it reuses).
func TestNativeSpecStageLintArtifactMatchesPowerShellBytes(t *testing.T) {
	root := nativeControllerRepositoryRoot(t)
	change := filepath.Join(root, "openspec", "changes", "native-cross-platform-cli")
	written, err := os.ReadFile(filepath.Join(change, "spec-lint.json"))
	if err != nil {
		t.Skipf("repository spec-lint.json is unavailable: %v", err)
	}
	spec, err := os.ReadFile(filepath.Join(change, "spec.md"))
	if err != nil {
		t.Skipf("repository spec.md is unavailable: %v", err)
	}
	match := nativeSpecStageCheckedAt.FindSubmatch(written)
	if match == nil {
		t.Fatalf("PowerShell artifact has no checked_at_utc: %s", written)
	}
	findings, lintErr := specvalidate.LintSpec(spec)
	if lintErr != nil {
		t.Fatal(lintErr)
	}
	artifact := newSpecLintArtifact(spec, findings)
	artifact.CheckedAtUTC = string(match[1])
	expected, err := nativeSpecLintArtifactBytes(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expected, written) {
		t.Fatalf("native spec-lint.json differs from the PowerShell artifact:\nwant: %q\ngot:  %q", expected, written)
	}
}

func TestNativeSpecStageExecuteLintFailureIsBlocked(t *testing.T) {
	fixture := newNativeSpecStageFixture(t)
	lintFailing := "## Goal\n\n- Complexity: M\n- Risk: low\n\nMinimal body without the required template.\n"
	fixture.workerResult.Payload = map[string]any{"spec": lintFailing, "design": nil}
	observation, err := fixture.provider().Execute(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("lint failure must be an observation, not an error: %v", err)
	}
	if observation.Status != "blocked" || observation.Stage != "spec" || observation.SideEffects != "none" {
		t.Fatalf("lint failure must map to the PS blocked semantics: %+v", observation)
	}
	if observation.Proposal != nil {
		t.Fatalf("blocked spec stage must not carry a proposal: %#v", observation.Proposal)
	}

	// The blocked summary is the Save-BFSpec throw text with the artifact's
	// error entries joined exactly like the PowerShell catch sees them.
	findings, lintErr := specvalidate.LintSpec([]byte(lintFailing))
	if lintErr != nil {
		t.Fatal(lintErr)
	}
	artifact := newSpecLintArtifact([]byte(lintFailing), findings)
	if artifact.Passed {
		t.Fatalf("fixture spec must fail lint")
	}
	wantSummary := "BF_BLOCKED: generated specification failed mandatory lint: " + strings.Join(artifact.Errors, "; ")
	if observation.Summary != wantSummary {
		t.Fatalf("blocked summary=%q, want %q", observation.Summary, wantSummary)
	}

	rawLint, err := os.ReadFile(filepath.Join(fixture.artifactRoot, "raw", "spec-lint.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rawLint, []byte(`"passed": false`)) {
		t.Fatalf("raw spec-lint.json must retain the failing receipt: %s", rawLint)
	}
	if _, err := os.ReadFile(filepath.Join(fixture.artifactRoot, "raw", "spec.md")); err != nil {
		t.Fatalf("the failing draft must still be retained: %v", err)
	}
	failureData, err := os.ReadFile(filepath.Join(fixture.artifactRoot, "raw", "failure.json"))
	if err != nil {
		t.Fatalf("blocked stage must retain failure.json: %v", err)
	}
	expectedFailure, err := repository.Canonical(map[string]any{"reason": wantSummary, "side_effects": "none"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expectedFailure, failureData) {
		t.Fatalf("failure.json bytes=%q, want canonical %q", failureData, expectedFailure)
	}

	// A blocked attempt keeps the attempt dependency binding untouched.
	if observation.Dependencies["spec"] != fixture.input.Attempt["dependencies"].(map[string]any)["spec"] {
		t.Fatalf("blocked dependencies must stay bound to the attempt: %#v", observation.Dependencies)
	}
}

func TestNativeSpecStageExecuteWorkerStatusesPassThrough(t *testing.T) {
	for _, item := range []struct {
		status string
	}{
		{"needs_input"},
		{"failed"},
		{"blocked"},
	} {
		t.Run(item.status, func(t *testing.T) {
			fixture := newNativeSpecStageFixture(t)
			fixture.workerResult.Status = item.status
			fixture.workerResult.Summary = "worker stopped with " + item.status
			observation, err := fixture.provider().Execute(context.Background(), fixture.input)
			if err != nil {
				t.Fatalf("worker status must map to an observation: %v", err)
			}
			if observation.Status != item.status || observation.Summary != "worker stopped with "+item.status {
				t.Fatalf("observation did not pass the worker status through: %+v", observation)
			}
			if observation.Proposal != nil {
				t.Fatalf("non-completed worker status must not carry a proposal")
			}
			if _, err := os.Lstat(filepath.Join(fixture.artifactRoot, "raw", "failure.json")); !os.IsNotExist(err) {
				t.Fatalf("legitimate worker statuses are not catch failures: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(fixture.artifactRoot, "raw", "payload.json")); !os.IsNotExist(err) {
				t.Fatalf("payload.json is only written for completed results: %v", err)
			}
			if _, err := os.Lstat(fixture.changeDir); !os.IsNotExist(err) {
				t.Fatalf("non-completed results must not touch the change directory: %v", err)
			}
		})
	}
}

func TestNativeSpecStageExecuteWorkerErrorIsBlockedFailure(t *testing.T) {
	fixture := newNativeSpecStageFixture(t)
	fixture.workerErr = errors.New("BF_BLOCKED: fixture worker exploded.")
	observation, err := fixture.provider().Execute(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("worker error must become a blocked observation: %v", err)
	}
	if observation.Status != "blocked" || observation.Summary != "BF_BLOCKED: fixture worker exploded." {
		t.Fatalf("worker error did not mirror the PS catch block: %+v", observation)
	}
	failureData, err := os.ReadFile(filepath.Join(fixture.artifactRoot, "raw", "failure.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(failureData, []byte("BF_BLOCKED: fixture worker exploded.")) {
		t.Fatalf("failure.json lost the reason: %s", failureData)
	}
}

func TestNativeSpecStageExecuteRequiredDesignForLargeClassification(t *testing.T) {
	fixture := newNativeSpecStageFixture(t)
	fixture.input.StateView["classification"] = map[string]any{"complexity": "L", "risk": "low"}
	// The classification gate runs before the design requirement, so the L
	// fixture must itself carry the L classification line.
	largeSpec := strings.Replace(nativeSpecStageFixtureSpec, "- Complexity: M", "- Complexity: L", 1)
	fixture.workerResult.Payload = map[string]any{"spec": largeSpec, "design": nil}
	observation, err := fixture.provider().Execute(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("missing design must be an observation: %v", err)
	}
	if observation.Status != "blocked" || observation.Summary != "BF_INVALID: invalid required design." {
		t.Fatalf("missing L design did not fail like Save-BFSpec: %+v", observation)
	}
	if _, err := os.Lstat(filepath.Join(fixture.artifactRoot, "raw", "failure.json")); err != nil {
		t.Fatalf("missing design is a catch failure: %v", err)
	}
}

func TestNativeSpecStageExecuteSourceChangeIsBlocked(t *testing.T) {
	fixture := newNativeSpecStageFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worker, "readme.txt"), []byte("mutated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	observation, err := fixture.provider().Execute(context.Background(), fixture.input)
	if err != nil {
		t.Fatalf("source change must be an observation: %v", err)
	}
	if observation.Status != "blocked" ||
		observation.Summary != "BF_BLOCKED: source changed during a read-only or verification stage." {
		t.Fatalf("source change did not mirror the read-only gate: %+v", observation)
	}
	if observation.SourceManifest == nil || observation.SourceManifest["sha256"] == fixture.manifestSHA {
		t.Fatalf("blocked observation must retain the changed manifest: %#v", observation.SourceManifest)
	}
	if _, err := os.Lstat(filepath.Join(fixture.artifactRoot, "raw", "failure.json")); err != nil {
		t.Fatalf("source change is a catch failure: %v", err)
	}
}

func TestNativeSpecStagePreDispatchGatesFailClosed(t *testing.T) {
	t.Run("stale spec binding", func(t *testing.T) {
		fixture := newNativeSpecStageFixture(t)
		dependencies := fixture.input.Attempt["dependencies"].(map[string]any)
		dependencies["spec"] = strings.Repeat("0", 64)
		_, err := fixture.provider().Execute(context.Background(), fixture.input)
		if err == nil || err.Error() != "BF_BLOCKED: provider inputs changed before dispatch." {
			t.Fatalf("stale binding must fail closed with the PS message: %v", err)
		}
		if fixture.workerCalls != 0 {
			t.Fatalf("stale binding must stop before the worker runs")
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		fixture := newNativeSpecStageFixture(t)
		nativeControllerWriteFile(t, fixture.cancelSignal, []byte(
			`{"schema_version":1,"task_id":"`+nativeSpecStageFixtureTaskID+
				`","attempt_id":"`+nativeSpecStageFixtureAttemptID+`","cancelled":true,"reason":"test"}`))
		_, err := fixture.provider().Execute(context.Background(), fixture.input)
		if err == nil || err.Error() != "BF_BLOCKED: cancelled before provider dispatch." {
			t.Fatalf("cancellation must fail closed with the PS message: %v", err)
		}
		if fixture.workerCalls != 0 {
			t.Fatalf("cancellation must stop before the worker runs")
		}
	})
	t.Run("missing worker seam", func(t *testing.T) {
		fixture := newNativeSpecStageFixture(t)
		provider := &nativeSpecStageProvider{inner: fixture.inner}
		_, err := provider.Execute(context.Background(), fixture.input)
		var typed *nativeSpecStageError
		if err == nil || !errors.As(err, &typed) {
			t.Fatalf("missing seam must fail closed with a typed error: %v", err)
		}
		if fixture.inner.executeCalls != 0 {
			t.Fatalf("a spec-stage input must never silently fall back to the PowerShell provider")
		}
	})
}

func TestNativeSpecStageDelegatesEverythingElse(t *testing.T) {
	fixture := newNativeSpecStageFixture(t)
	provider := fixture.provider()

	t.Run("measure", func(t *testing.T) {
		measure := fixture.input
		measure.Operation = "measure"
		measure.Attempt = nil
		if _, err := provider.Measure(context.Background(), measure); err != nil {
			t.Fatalf("measure passthrough failed: %v", err)
		}
		if fixture.inner.measureCalls != 1 || fixture.inner.lastMeasure.TaskID != nativeSpecStageFixtureTaskID {
			t.Fatalf("measure was not delegated unchanged: %+v", fixture.inner.lastMeasure)
		}
	})

	t.Run("non-spec stage", func(t *testing.T) {
		implement := fixture.input
		implement.Attempt = map[string]any{
			"attempt_id":  nativeSpecStageFixtureAttemptID,
			"task_id":     nativeSpecStageFixtureTaskID,
			"stage":       "implement",
			"worker_path": fixture.worker,
		}
		implement.StateView["stage"] = "implement"
		if _, err := provider.Execute(context.Background(), implement); err != nil {
			t.Fatalf("non-spec execute passthrough failed: %v", err)
		}
		if fixture.inner.executeCalls != 1 || fixture.inner.lastExecute.Attempt["stage"] != "implement" {
			t.Fatalf("implement stage was not delegated: %+v", fixture.inner.lastExecute.Attempt)
		}
		if fixture.workerCalls != 0 {
			t.Fatalf("the native worker must not run for other stages")
		}
	})

	t.Run("spec input without worker seam still delegates non-spec shapes", func(t *testing.T) {
		noSeam := &nativeSpecStageProvider{inner: fixture.inner}
		broken := fixture.input
		broken.Attempt["stage"] = "spec_review"
		broken.StateView["stage"] = "spec_review"
		if _, err := noSeam.Execute(context.Background(), broken); err != nil {
			t.Fatalf("unexpected shapes must delegate: %v", err)
		}
		if fixture.inner.executeCalls != 2 {
			t.Fatalf("unexpected shape was not delegated: %d calls", fixture.inner.executeCalls)
		}
	})
}

func TestNativeSpecStageInputDiscrimination(t *testing.T) {
	base := newNativeSpecStageFixture(t)
	cases := []struct {
		name   string
		mutate func(input *repository.ExecuteInput)
		want   bool
	}{
		{"well-formed spec stage", func(input *repository.ExecuteInput) {}, true},
		{"wrong operation", func(input *repository.ExecuteInput) { input.Operation = "measure" }, false},
		{"wrong contract", func(input *repository.ExecuteInput) { input.Contract = "other" }, false},
		{"wrong provider contract name", func(input *repository.ExecuteInput) {
			identity := input.ProviderContract
			identity.Name = "bsl-flow.other.v1"
			input.ProviderContract = identity
		}, false},
		{"attempt stage differs", func(input *repository.ExecuteInput) { input.Attempt["stage"] = "inspect" }, false},
		{"state stage differs", func(input *repository.ExecuteInput) { input.StateView["stage"] = "inspect" }, false},
		{"stage mismatch between attempt and state", func(input *repository.ExecuteInput) { input.Attempt["stage"] = "implement" }, false},
		{"non-uuid attempt", func(input *repository.ExecuteInput) { input.Attempt["attempt_id"] = "not-a-uuid" }, false},
		{"active attempt mismatch", func(input *repository.ExecuteInput) {
			input.StateView["active_attempt"] = "00000000-0000-0000-0000-000000000000"
		}, false},
		{"missing spec dependency key", func(input *repository.ExecuteInput) {
			delete(input.Attempt["dependencies"].(map[string]any), "spec")
		}, false},
		{"missing source manifest", func(input *repository.ExecuteInput) { delete(input.Attempt, "source_manifest") }, false},
		{"missing prompt", func(input *repository.ExecuteInput) {
			delete(input.StateView["request"].(map[string]any), "prompt")
		}, false},
		{"invalid classification", func(input *repository.ExecuteInput) {
			input.StateView["classification"] = map[string]any{"complexity": "XL", "risk": "low"}
		}, false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			input := base.input
			item.mutate(&input)
			if got := nativeSpecStageInput(input); got != item.want {
				t.Fatalf("nativeSpecStageInput=%v, want %v", got, item.want)
			}
		})
	}
}

func TestNativeSpecStageIdentityMarker(t *testing.T) {
	provider := &nativeSpecStageProvider{inner: &nativeSpecStageInner{}}
	if !provider.isNativeSpecStageProvider() {
		t.Fatalf("marker method must distinguish the composing provider")
	}
	if name := provider.nativeSpecStageContractName(); name != nativeProviderContract+nativeSpecStageContractSuffix {
		t.Fatalf("contract marker=%q", name)
	}
	// The accepted wire contract stays the plain PowerShell identity.
	if nativeSpecStageInput(newNativeSpecStageFixture(t).input) != true {
		t.Fatalf("plain windows-ps.v1 inputs must remain acceptable")
	}
}
