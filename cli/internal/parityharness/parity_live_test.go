package parityharness

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The live differential runs the legacy PowerShell engine read-only over the
// committed frozen inputs, twice-checked against the committed frozen traces
// (engine drift) and against the native shadow (parity). It follows the
// repo's parity convention: no build tag, a runtime skip when pwsh or the
// packaged scripts are unavailable, so `go test ./...` stays green and fast
// everywhere else. Requirement 21 holds throughout: every captured operation
// is read/decision computation; nothing writes outside the temp sandboxes,
// no model/API/network is contacted and no side-effecting action runs twice.

// parityRepoRoot resolves the repository root from this file's location.
func parityRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	root := filepath.Dir(filepath.Join(thisFile, "..", "..", ".."))
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return ""
	}
	return root
}

func parityPwsh(t *testing.T) string {
	t.Helper()
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh is unavailable for the PowerShell parity run")
	}
	return pwsh
}

func parityScriptsDir(t *testing.T, area string) string {
	t.Helper()
	root := parityRepoRoot(t)
	if root == "" {
		t.Skip("repository scripts are unavailable")
	}
	scripts := filepath.Join(root, "global", "skills", area, "scripts")
	info, err := os.Stat(scripts)
	if err != nil || !info.IsDir() {
		t.Skipf("packaged %s scripts are unavailable", area)
	}
	return scripts
}

// parityRunPwsh executes one capture script and fails with its stderr.
func parityRunPwsh(t *testing.T, pwsh string, arguments ...string) {
	t.Helper()
	command := exec.Command(pwsh, append([]string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File"}, arguments...)...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("pwsh %s: %v: %s", filepath.Base(arguments[0]), err, stderr.String())
	}
}

func parityRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// parityDecode decodes strict JSON object bytes.
func parityDecode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	object, err := decodeTraceBytes(data)
	if err != nil {
		t.Fatalf("decode %s: %v", truncateForLog(data), err)
	}
	return object
}

func truncateForLog(data []byte) string {
	text := string(data)
	if len(text) > 200 {
		return text[:200] + "..."
	}
	return text
}

// captureLegacyLint freezes Test-1CSpec.ps1 over one committed lint fixture.
func captureLegacyLint(t *testing.T, pwsh, fixture string) *TraceDocument {
	t.Helper()
	scripts := parityScriptsDir(t, "1c-spec-review")
	root := parityRepoRoot(t)
	inputPath := filepath.Join(root, "cli", "internal", "parityharness", "testdata", "inputs", fixture, "spec.md")
	spec := parityRead(t, inputPath)
	scratch := t.TempDir()
	changePath := filepath.Join(scratch, "change")
	if err := os.MkdirAll(changePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(changePath, "spec.md"), spec, 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(scratch, "spec-lint.json")
	parityRunPwsh(t, pwsh,
		filepath.Join(root, "cli", "internal", "parityharness", "testdata", "capture", "spec-lint.ps1"),
		"-ScriptsRoot", scripts, "-ChangePath", changePath, "-OutPath", outPath)
	artifact := parityDecode(t, parityRead(t, outPath))
	// checked_at_utc is capture clock state, not a function of the trusted
	// inputs; it is excluded from the observation on both engines.
	delete(artifact, "checked_at_utc")
	return &TraceDocument{
		SchemaVersion: TraceSchemaVersion,
		TraceID:       fixture,
		Operation:     string(OpSpecLint),
		Engine:        Engine{Kind: EngineLegacyPowerShell, Identity: "global/skills/1c-spec-review/scripts/Test-1CSpec.ps1"},
		CapturedAt:    parityCapturedAt,
		Provenance:    "pwsh -File testdata/capture/spec-lint.ps1 -ScriptsRoot global/skills/1c-spec-review/scripts over testdata/inputs/" + fixture + "/spec.md; observation = spec-lint.json minus checked_at_utc",
		Inputs: []InputFile{{
			Name:   "spec.md",
			SHA256: fileSHA256Hex(spec),
			Path:   filepath.ToSlash(filepath.Join("inputs", fixture, "spec.md")),
		}},
		Observations: []Observation{{Name: "spec_lint", Document: artifact}},
	}
}

// captureLegacyFinal freezes Test-1CSpecFinal.ps1 over one committed change
// fixture staged into a temporary project.
func captureLegacyFinal(t *testing.T, pwsh, fixture string) *TraceDocument {
	t.Helper()
	scripts := parityScriptsDir(t, "1c-spec-review")
	root := parityRepoRoot(t)
	inputDir := filepath.Join(root, "cli", "internal", "parityharness", "testdata", "inputs", fixture)
	entries, err := os.ReadDir(inputDir)
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	changeRoot := filepath.Join(scratch, "project", "openspec", "changes", fixture)
	if err := os.MkdirAll(changeRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	inputs := make([]InputFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data := parityRead(t, filepath.Join(inputDir, entry.Name()))
		if err := os.WriteFile(filepath.Join(changeRoot, entry.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, InputFile{
			Name:   entry.Name(),
			SHA256: fileSHA256Hex(data),
			Path:   filepath.ToSlash(filepath.Join("inputs", fixture, entry.Name())),
		})
	}
	outPath := filepath.Join(scratch, "final-validation.json")
	parityRunPwsh(t, pwsh,
		filepath.Join(root, "cli", "internal", "parityharness", "testdata", "capture", "spec-final.ps1"),
		"-ScriptsRoot", scripts, "-ProjectPath", filepath.Join(scratch, "project"), "-ChangeName", fixture, "-OutPath", outPath)
	sidecar := parityDecode(t, parityRead(t, outPath))
	delete(sidecar, "checked_at_utc")
	return &TraceDocument{
		SchemaVersion: TraceSchemaVersion,
		TraceID:       fixture,
		Operation:     string(OpSpecFinal),
		Engine:        Engine{Kind: EngineLegacyPowerShell, Identity: "global/skills/1c-spec-review/scripts/Test-1CSpecFinal.ps1"},
		CapturedAt:    parityCapturedAt,
		Provenance: "pwsh -File testdata/capture/spec-final.ps1 -ScriptsRoot global/skills/1c-spec-review/scripts over testdata/inputs/" + fixture +
			" staged as openspec/changes/" + fixture + "; observation = final-validation.json minus checked_at_utc",
		Inputs:       inputs,
		Observations: []Observation{{Name: "spec_final", Document: sidecar}},
		Classifications: []Classification{{
			Observation: "spec_final.checks",
			Kind:        KindSchemaChange,
			Reason:      "the native validator reports named per-check results; Test-1CSpecFinal.ps1 reports only the accumulated error list",
			Approved:    true,
		}},
	}
}

// captureLegacyRunnerDecide freezes Get-BFRunnerDecision over the committed
// case set and derives the frozen per-case snapshot inputs from its output.
func captureLegacyRunnerDecide(t *testing.T, pwsh string) (*TraceDocument, []NamedBytes) {
	t.Helper()
	scripts := parityScriptsDir(t, "1c-task")
	root := parityRepoRoot(t)
	base := filepath.Join(root, "cli", "internal", "parityharness", "testdata")
	scratch := t.TempDir()
	project := filepath.Join(scratch, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(scratch, "runner-decide.json")
	// Save-BFTask/Read-BFTask verify the exact Git worktree root and probe
	// repository identity with sanitized environment, so the capture project
	// is a real throwaway repository with repo-local identity (the same
	// convention as the stagehost parity fixtures).
	parityGitInitFixture(t, project)
	parityRunPwsh(t, pwsh,
		filepath.Join(base, "capture", "runner-decide.ps1"),
		"-ScriptsRoot", scripts, "-ProjectPath", project,
		"-CasesPath", filepath.Join(base, "capture", "runner-decide-cases.json"),
		"-OutPath", outPath)
	document := parityDecode(t, parityRead(t, outPath))
	cases, ok := document["cases"].([]any)
	if !ok || len(cases) == 0 {
		t.Fatalf("runner capture has no cases: %s", truncateForLog(parityRead(t, outPath)))
	}
	snapshots := make([]NamedBytes, 0, len(cases))
	inputs := make([]InputFile, 0, len(cases))
	for _, raw := range cases {
		entry, ok := raw.(map[string]any)
		if !ok {
			t.Fatal("runner capture case is not an object")
		}
		name, _ := entry["name"].(string)
		snapshot, ok := entry["snapshot"].(map[string]any)
		if !ok {
			t.Fatalf("runner capture case %s has no snapshot", name)
		}
		data, err := canonicalBytes(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, NamedBytes{Name: name, Data: data})
		inputs = append(inputs, InputFile{
			Name:   name,
			SHA256: fileSHA256Hex(data),
			Path:   filepath.ToSlash(filepath.Join("inputs", "runner-decide", name+".snapshot.json")),
		})
	}
	return &TraceDocument{
		SchemaVersion: TraceSchemaVersion,
		TraceID:       "runner-decide",
		Operation:     string(OpRunnerDecide),
		Engine:        Engine{Kind: EngineLegacyPowerShell, Identity: "global/skills/1c-task/scripts/Task.Runner.ps1 Get-BFRunnerDecision"},
		CapturedAt:    parityCapturedAt,
		Provenance: "pwsh -File testdata/capture/runner-decide.ps1 -ScriptsRoot global/skills/1c-task/scripts -CasesPath testdata/capture/runner-decide-cases.json; " +
			"inputs are the per-case snapshots derived from the capture output (PROJECT_PATH and POLICY_HASH are patched to the sandbox inside the script and never enter a snapshot)",
		Inputs:       inputs,
		Observations: []Observation{{Name: "runner_decide", Document: document}},
	}, snapshots
}

// captureLegacyMemoryProjection freezes the read-only PS memory projection
// (Invoke-BFNativeMemory.ps1, operation=projection, "Never writes") over the
// committed empty-store fixture in a sandboxed package copy. The symbolic
// %PARITY_PROJECT% token of the frozen request is bound to the sandbox
// project for the run; the recorded input set keeps the frozen symbolic
// bytes.
func captureLegacyMemoryProjection(t *testing.T, pwsh string) (*TraceDocument, string) {
	t.Helper()
	root := parityRepoRoot(t)
	scripts := parityScriptsDir(t, "1c-task")
	fixture := filepath.Join(root, "cli", "internal", "parityharness", "testdata", "inputs", "memory-projection")
	scratch := t.TempDir()
	packageRoot := filepath.Join(scratch, "pkg")
	sandboxScripts := filepath.Join(packageRoot, "global", "skills", "1c-task", "scripts")
	if err := os.MkdirAll(sandboxScripts, 0o755); err != nil {
		t.Fatal(err)
	}
	// The sandbox carries every packaged script, like the memoryhost parity
	// fixture: the entrypoint's dot-source chain (Task.Memory.ps1 ->
	// Task.Contracts.ps1 -> Task.Runtime.ps1 -> Task.Execution.ps1 ...) is a
	// moving target of the legacy engine.
	sources, err := filepath.Glob(filepath.Join(scripts, "*.ps1"))
	if err != nil || len(sources) == 0 {
		t.Skip("packaged skill scripts are unavailable")
	}
	for _, source := range sources {
		data := parityRead(t, source)
		if err := os.WriteFile(filepath.Join(sandboxScripts, filepath.Base(source)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	schemas := filepath.Join(packageRoot, "global", "skills", "1c-task", "schemas")
	if err := os.MkdirAll(schemas, 0o755); err != nil {
		t.Fatal(err)
	}
	frozenRequest := parityRead(t, filepath.Join(fixture, "request.json"))
	frozenPackage := map[string][]byte{
		"VERSION":               parityRead(t, filepath.Join(fixture, "package", "VERSION")),
		"package-manifest.json": parityRead(t, filepath.Join(fixture, "package", "package-manifest.json")),
	}
	for _, name := range []string{"memory-event.schema.json", "memory-index.schema.json", "memory-bundle.schema.json", "context.schema.json"} {
		frozenPackage[filepath.ToSlash(filepath.Join("global", "skills", "1c-task", "schemas", name))] = parityRead(t, filepath.Join(fixture, "package", "global", "skills", "1c-task", "schemas", name))
	}
	for name, data := range frozenPackage {
		target := filepath.Join(packageRoot, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	project := filepath.Join(scratch, "project")
	memoryRoot := filepath.Join(project, ".bsl-flow", "memory")
	if err := os.MkdirAll(memoryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	// Rebind the symbolic project token through decode-and-re-encode, never
	// raw byte substitution: a Windows path carries backslashes that would
	// break the JSON string escapes.
	requestObject := parityDecode(t, frozenRequest)
	requestState, ok := requestObject["state"].(map[string]any)
	if !ok {
		t.Fatal("frozen memory request has no state object")
	}
	requestState["project_path"] = project
	requestObject["memory_root"] = memoryRoot
	runRequest, err := canonicalBytes(requestObject)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(sandboxScripts, "Invoke-BFNativeMemory.ps1"))
	command.Stdin = bytes.NewReader(runRequest)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("pwsh Invoke-BFNativeMemory.ps1: %v: %s", err, stderr.String())
	}
	envelope := parityDecode(t, stdout.Bytes())
	inputs := []InputFile{{Name: "request.json", SHA256: fileSHA256Hex(frozenRequest), Path: "inputs/memory-projection/request.json"}}
	for name, data := range frozenPackage {
		inputs = append(inputs, InputFile{Name: "package/" + name, SHA256: fileSHA256Hex(data), Path: "inputs/memory-projection/package/" + name})
	}
	return &TraceDocument{
		SchemaVersion: TraceSchemaVersion,
		TraceID:       "memory-projection-empty",
		Operation:     string(OpMemoryProjection),
		Engine:        Engine{Kind: EngineLegacyPowerShell, Identity: "global/skills/1c-task/scripts/Invoke-BFNativeMemory.ps1 projection"},
		CapturedAt:    parityCapturedAt,
		Provenance: "pwsh -File global/skills/1c-task/scripts/Invoke-BFNativeMemory.ps1 (sandboxed copy) with the frozen request on stdin; " +
			"the %PARITY_PROJECT% token of the frozen request is bound to the sandbox project for the run only (read-only projection, empty store)",
		Inputs:       inputs,
		Observations: []Observation{{Name: "memory_projection", Document: envelope}},
	}, scratch
}

// parityGitInitFixture creates a throwaway git repository with repo-local
// identity for capture projects that must satisfy the storage-layer git
// verification. Fixture staging only: nothing here touches any real project.
func parityGitInitFixture(t *testing.T, project string) {
	t.Helper()
	git := func(arguments ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-c", "core.hooksPath=NUL", "-c", "core.fsmonitor=false", "-C", project}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	git("init", "--quiet")
	git("config", "user.name", "parity-fixture")
	git("config", "user.email", "parity-fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("parity capture fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("commit", "-m", "fixture", "--quiet")
}

// parityCapturedAt stamps refreshed traces; it is documentation, not evidence.
const parityCapturedAt = "2026-09-15T00:00:00.0000000Z"

// TestPowerShellLiveParity is the consolidated live differential: the frozen
// legacy traces must still match a fresh read-only run of the legacy engine
// over the committed inputs (engine drift check), and the native shadow over
// the same inputs must classify as compatible with the frozen traces. The
// memory projection is a same-path A/B comparison instead (see below): its
// bundle ids are project-scoped hashes and cannot be frozen portably.
func TestPowerShellLiveParity(t *testing.T) {
	if os.Getenv("BSL_FLOW_PARITY_REFRESH") != "" {
		t.Skip("refresh run writes the frozen traces; the live differential re-runs without it")
	}
	pwsh := parityPwsh(t)
	frozen := loadFrozenTracesForLive(t)

	fresh := map[string]*TraceDocument{}
	for _, fixture := range []string{"spec-lint-clean", "spec-lint-violations"} {
		fresh[fixture] = captureLegacyLint(t, pwsh, fixture)
	}
	for _, fixture := range []string{"spec-final-pass", "spec-final-lint-fail"} {
		fresh[fixture] = captureLegacyFinal(t, pwsh, fixture)
	}
	runnerTrace, snapshots := captureLegacyRunnerDecide(t, pwsh)
	fresh["runner-decide"] = runnerTrace
	memoryTrace, memoryScratch := captureLegacyMemoryProjection(t, pwsh)

	for traceID, legacy := range fresh {
		t.Run(traceID, func(t *testing.T) {
			frozenTrace := frozen[traceID]
			if frozenTrace == nil {
				t.Fatalf("no committed frozen trace for %s", traceID)
			}
			legacy.Inputs = withFrozenInputPaths(legacy, frozenTrace)
			drift := Compare(frozenTrace, legacy)
			if drift.Failed() {
				t.Fatalf("legacy engine drifted from the frozen trace: %s %s", drift.Detail, observationDetail(drift))
			}

			native, err := shadowTraceFor(t, frozenTrace, snapshots)
			if err != nil {
				t.Fatalf("native shadow: %v", err)
			}
			parity := Compare(frozenTrace, native)
			if parity.Failed() {
				t.Fatalf("native shadow diverged from the frozen trace: %s %s", parity.Detail, observationDetail(parity))
			}
			assertExpectedStatuses(t, frozenTrace, parity)
		})
	}

	// The memory projection bundle hashes project_id (the staging path) by
	// contract (Get-BFMemoryBundleFromReplay: bundle content includes
	// project_id), so its outputs are only comparable when both engines run
	// over one staged root. The harness runs the legacy engine first, then
	// the native shadow at the same root, and requires a full match with no
	// classification declared — nothing may be masked here.
	t.Run("memory-projection-empty", func(t *testing.T) {
		request, err := shadowRequestForTrace(t, memoryTrace)
		if err != nil {
			t.Fatal(err)
		}
		request.Memory.Scratch = memoryScratch
		native, err := Shadow(request)
		if err != nil {
			t.Fatalf("native shadow: %v", err)
		}
		comparison := Compare(memoryTrace, native)
		if comparison.Failed() {
			t.Fatalf("native memory projection diverged: %s %s", comparison.Detail, observationDetail(comparison))
		}
		assertExpectedStatuses(t, memoryTrace, comparison)
	})
}

// withFrozenInputPaths copies the committed input paths onto a fresh capture
// so the drift comparison is not confused by capture-time path metadata
// (fresh captures stage copies in temp directories).
func withFrozenInputPaths(fresh, frozen *TraceDocument) []InputFile {
	for index := range fresh.Inputs {
		for _, frozenInput := range frozen.Inputs {
			if fresh.Inputs[index].Name == frozenInput.Name {
				fresh.Inputs[index].Path = frozenInput.Path
				break
			}
		}
	}
	return fresh.Inputs
}

func observationDetail(comparison Comparison) string {
	parts := make([]string, 0, len(comparison.Observations))
	for _, result := range comparison.Observations {
		if result.Status == StatusMatch {
			continue
		}
		entry := result.Observation + ": " + string(result.Status) + " " + result.Detail
		for _, diff := range result.Diffs {
			entry += fmt.Sprintf(" [%s frozen=%s live=%s]", diff.Path, diff.Frozen, diff.Live)
		}
		parts = append(parts, entry)
	}
	return strings.Join(parts, "; ")
}

// loadFrozenTracesForLive loads every committed trace document.
func loadFrozenTracesForLive(t *testing.T) map[string]*TraceDocument {
	t.Helper()
	traces := map[string]*TraceDocument{}
	for _, frozen := range loadFrozenTraces(t) {
		traces[frozen.trace.TraceID] = frozen.trace
	}
	return traces
}

// shadowTraceFor rebuilds the native shadow trace for one frozen trace from
// its committed input files. runnerSnapshots are reused from the live
// capture when present so the differential binds both engines to one frozen
// snapshot set.
func shadowTraceFor(t *testing.T, frozen *TraceDocument, runnerSnapshots []NamedBytes) (*TraceDocument, error) {
	t.Helper()
	request, err := shadowRequestForTrace(t, frozen)
	if err != nil {
		return nil, err
	}
	if frozen.Operation == string(OpRunnerDecide) && runnerSnapshots != nil {
		request.Snapshots = runnerSnapshots
	}
	return Shadow(request)
}

// TestRefreshFrozenTraces regenerates the committed frozen traces from a
// fresh read-only legacy run. It never runs implicitly: set
// BSL_FLOW_PARITY_REFRESH=1 and run this test alone; it writes
// testdata/traces (and the runner snapshot inputs) and then verifies each
// refreshed trace round-trips through LoadTrace.
func TestRefreshFrozenTraces(t *testing.T) {
	if os.Getenv("BSL_FLOW_PARITY_REFRESH") == "" {
		t.Skip("frozen traces are refreshed only with BSL_FLOW_PARITY_REFRESH=1")
	}
	pwsh := parityPwsh(t)
	root := parityRepoRoot(t)
	testdata := filepath.Join(root, "cli", "internal", "parityharness", "testdata")

	var refreshed []*TraceDocument
	for _, fixture := range []string{"spec-lint-clean", "spec-lint-violations"} {
		refreshed = append(refreshed, captureLegacyLint(t, pwsh, fixture))
	}
	for _, fixture := range []string{"spec-final-pass", "spec-final-lint-fail"} {
		refreshed = append(refreshed, captureLegacyFinal(t, pwsh, fixture))
	}
	runnerTrace, snapshots := captureLegacyRunnerDecide(t, pwsh)
	refreshed = append(refreshed, runnerTrace)
	for index, snapshot := range snapshots {
		path := filepath.Join(testdata, runnerTrace.Inputs[index].Path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, snapshot.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The memory projection is intentionally NOT frozen: its bundle ids hash
	// the staging project path (project-scoped by contract), so a frozen
	// document could only stay portable by masking those fields — which the
	// harness forbids. Its parity evidence is the same-path live A/B of
	// TestPowerShellLiveParity/memory-projection-empty plus the memoryhost
	// byte-parity suite.

	tracesDir := filepath.Join(testdata, "traces")
	if err := os.MkdirAll(tracesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, trace := range refreshed {
		if err := trace.SetInputSet(); err != nil {
			t.Fatal(err)
		}
		data, err := SaveTrace(trace)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(tracesDir, trace.TraceID+".trace.json")
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadTrace(data); err != nil {
			t.Fatalf("refreshed trace %s does not load: %v", trace.TraceID, err)
		}
		t.Logf("refreshed %s", path)
	}
}
