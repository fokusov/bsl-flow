package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// nativeTestEngineFixture is a small, real-on-disk package identity.  Native
// activation must bind the package and host from bytes, even when the provider
// implementation injected by the test is only a deterministic fake.
type nativeTestEngineFixture struct {
	engine   EngineIdentity
	hostPath string
	pwshPath string
	root     string
	toolset  string
}

func newNativeTestEngine(t *testing.T) nativeTestEngineFixture {
	t.Helper()
	fixtureRoot := tempDir(t)
	root := filepath.Join(fixtureRoot, "bundle")
	toolsetRoot := filepath.Join(fixtureRoot, "toolset")
	script := filepath.Join(root, "global", "skills", "1c-task", "scripts", "Invoke-BFNativeProvider.ps1")
	providerScript := filepath.Join(root, "global", "skills", "1c-task", "scripts", "Task.Provider.ps1")
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		t.Fatal(err)
	}
	writeNativeTestFile(t, filepath.Join(root, "VERSION"), []byte("0.8.0-test\n"))
	writeNativeTestFile(t, script, []byte("# deterministic native provider fixture\n"))
	writeNativeTestFile(t, providerScript, []byte("# deterministic provider contract fixture\n"))
	toolsetFile := filepath.Join(toolsetRoot, "fixture-skill", "SKILL.md")
	writeNativeTestFile(t, toolsetFile, []byte("# deterministic toolset fixture\n"))
	toolsetFileHash := nativeTestFileHash(t, toolsetFile)
	toolsetSkillFiles := []any{map[string]any{"path": "SKILL.md", "sha256": toolsetFileHash}}
	toolsetSkillHash, err := nativeToolsetAggregateHash([]any{map[string]any{"name": "fixture-skill", "files": toolsetSkillFiles}})
	if err != nil {
		t.Fatal(err)
	}
	toolsetSkills := []any{map[string]any{
		"name": "fixture-skill", "files": toolsetSkillFiles,
		"sha256": toolsetSkillHash, "mcp_references": []any{},
	}}
	toolsetAggregate, err := nativeToolsetAggregateHash(toolsetSkills)
	if err != nil {
		t.Fatal(err)
	}
	toolsetManifest := map[string]any{
		"schema_version": int64(1), "toolset_name": "cc-1c-skills",
		"source": map[string]any{"identity": "local-private", "path": toolsetRoot},
		"skills": toolsetSkills, "aggregate_sha256": toolsetAggregate,
	}
	toolsetManifestBytes, err := Canonical(toolsetManifest)
	if err != nil {
		t.Fatal(err)
	}
	writeNativeTestFile(t, filepath.Join(toolsetRoot, "toolset-manifest.json"), toolsetManifestBytes)
	hostPath := filepath.Join(root, "bsl-flow-test.exe")
	writeNativeTestFile(t, hostPath, []byte("native host fixture\n"))
	// The outer native runner is PowerShell 7.  This fixture does not launch it,
	// but the retained transport receipt must still carry the same executable
	// identity and fixed argv shape as a real invocation.
	pwshPath := filepath.Join(root, "pwsh.exe")
	writeNativeTestFile(t, pwshPath, []byte("PowerShell 7 native fixture\n"))
	assets, err := nativeAssetManifestHash(filepath.Join(root, "global", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	engine := EngineIdentity{
		Name:                "native",
		ContractVersion:     1,
		Provider:            NativeProviderContract,
		HostSHA256:          nativeTestFileHash(t, hostPath),
		ProviderSHA256:      nativeTestFileHash(t, script),
		AssetManifestSHA256: assets,
		PolicyRoot:          filepath.Join(root, "global", "skills"),
		HostPath:            hostPath,
	}
	return nativeTestEngineFixture{engine: engine, hostPath: hostPath, pwshPath: pwshPath, root: root, toolset: toolsetRoot}
}

func writeNativeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func nativeTestFileHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fileSHA256(data)
}

type nativeTestFixture struct {
	project  string
	taskID   string
	engine   nativeTestEngineFixture
	provider *nativeTestProvider
	host     *ControllerHost
	request  map[string]any
}

// nativeTestProvider exercises the controller seam without invoking a model,
// PowerShell, a 1C runtime, or a network.  It still writes the same nested
// process receipts and artifact descriptors that a real provider must return.
type nativeTestProvider struct {
	project string
	engine  EngineIdentity

	mu           sync.Mutex
	measureCalls int
	executeCalls []string
	executeErr   error
}

func (p *nativeTestProvider) Measure(_ context.Context, input MeasureInput) (MeasureObservation, error) {
	p.mu.Lock()
	p.measureCalls++
	p.mu.Unlock()
	repository, err := OpenRepository(p.project)
	if err != nil {
		return MeasureObservation{}, err
	}
	project := p.project
	if value := asStringOr(input.StateView["project_path"]); value != "" {
		project = value
	}
	policyFiles, err := nativePolicyInventory(project, p.engine.PolicyRoot, p.engine.HostPath)
	if err != nil {
		return MeasureObservation{}, err
	}
	policyRules, err := nativeProjectRules(project)
	if err != nil {
		return MeasureObservation{}, err
	}
	worker := asStringOr(input.StateView["worker_path"])
	if worker == "" {
		worker = project
	}
	baseline := asStringOr(input.StateView["baseline"])
	manifest, err := sourceManifestWithBaseline(worker, baseline, []string{"."})
	if err != nil {
		return MeasureObservation{}, err
	}
	specInputs, err := localSpecInputs(repository, input.TaskID)
	if err != nil {
		return MeasureObservation{}, err
	}
	dependencies, err := currentNativeDependencies(input.StateView, "inspect", nil)
	if err != nil {
		return MeasureObservation{}, err
	}
	measurement := MeasureObservation{
		SchemaVersion:  1,
		Contract:       NativeProviderContract,
		TaskID:         input.TaskID,
		Operation:      "measure",
		RequestValid:   true,
		PolicyFiles:    policyFiles,
		PolicyRules:    policyRules,
		SourceManifest: manifest,
		SpecInputs:     specInputs,
		Dependencies:   dependencies,
		Capability: map[string]any{
			"observations": map[string]any{
				"config_read": "allowed", "config_write": "denied",
				"scratch_write": "allowed", "source_write": "denied",
				"canonical_current_read": "denied", "canonical_revision_read": "denied",
				"canonical_inputs_read": "denied", "canonical_current_write": "denied",
				"canonical_revision_write": "denied", "canonical_inputs_write": "denied",
			},
			"permissions_sha256": strings.Repeat("d", 64),
			"sandbox_sha256":     p.engine.HostSHA256,
			"provider_sha256":    p.engine.HostSHA256,
			"network":            "not_probed",
			"database":           "not_accessed",
		},
		Blockers: []string{},
	}
	stdout, err := Canonical(toMeasureObservation(measurement))
	if err != nil {
		return MeasureObservation{}, err
	}
	measurement.Transport = nativeTestTransport(p.engine, stdout, []byte("native test provider measure stderr\n"))
	return measurement, nil
}

func (p *nativeTestProvider) Execute(_ context.Context, input ExecuteInput) (ExecuteObservation, error) {
	stage := asStringOr(input.Attempt["stage"])
	p.mu.Lock()
	p.executeCalls = append(p.executeCalls, stage)
	err := p.executeErr
	p.mu.Unlock()
	if err != nil {
		return ExecuteObservation{}, err
	}
	return p.executeObservation(input, stage)
}

func (p *nativeTestProvider) executeObservation(input ExecuteInput, stage string) (ExecuteObservation, error) {
	if err := os.MkdirAll(input.ArtifactRoot, 0o700); err != nil {
		return ExecuteObservation{}, err
	}
	executable := asStringOr(input.Attempt["executable"])
	processID := int64(1000 + len(p.executeCalls))
	stdout := []byte("native test provider stdout\n")
	stderr := []byte("native test provider stderr\n")
	process := map[string]any{
		"pid": processID, "start_time_utc": nowUTC(), "executable": executable,
		"arguments_sha256": strings.Repeat("0", 64),
	}
	exit := map[string]any{
		"exit_code": int64(0), "stop_reason": "completed", "elapsed_seconds": float64(0),
		"process_id": processID, "executable": executable,
		"stdout": filepath.Join(input.ArtifactRoot, "stdout.txt"),
		"stderr": filepath.Join(input.ArtifactRoot, "stderr.txt"),
	}
	processData, err := Canonical(process)
	if err != nil {
		return ExecuteObservation{}, err
	}
	exitData, err := Canonical(exit)
	if err != nil {
		return ExecuteObservation{}, err
	}
	files := map[string][]byte{
		"process.json": processData,
		"exit.json":    exitData,
		"stdout.txt":   stdout,
		"stderr.txt":   stderr,
	}
	// A managed request carries a budget even though this deterministic fake
	// never dispatches a model.  The real provider projects the controller's
	// immutable prior ledger into its artifact manifest on every completed
	// stage; retain the same binding so the Go budget gate sees an explicit
	// ledger rather than treating this as a missing model receipt.
	budgetPath := filepath.Join(input.ContextRoot, "budget", "ledger.json")
	if budgetData, budgetErr := os.ReadFile(budgetPath); budgetErr == nil {
		files["budget/ledger.json"] = budgetData
	} else if !os.IsNotExist(budgetErr) {
		return ExecuteObservation{}, budgetErr
	}
	for relative, data := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(input.ArtifactRoot, relative)), 0o700); err != nil {
			return ExecuteObservation{}, err
		}
		if err := os.WriteFile(filepath.Join(input.ArtifactRoot, relative), data, 0o600); err != nil {
			return ExecuteObservation{}, err
		}
	}
	artifacts := make([]ArtifactRef, 0, len(files))
	for _, relative := range []string{"process.json", "exit.json", "stdout.txt", "stderr.txt"} {
		data := files[relative]
		kind := "raw"
		if relative == "process.json" || relative == "exit.json" {
			kind = "process"
		}
		artifacts = append(artifacts, ArtifactRef{Path: relative, SHA256: fileSHA256(data), SizeBytes: int64(len(data)), Kind: kind})
	}
	if data, ok := files["budget/ledger.json"]; ok {
		artifacts = append(artifacts, ArtifactRef{Path: "budget/ledger.json", SHA256: fileSHA256(data), SizeBytes: int64(len(data)), Kind: "budget"})
	}
	processReceipt := map[string]any{"processes": []any{map[string]any{
		"process_path": "process.json", "process_sha256": fileSHA256(processData),
		"exit_path": "exit.json", "exit_sha256": fileSHA256(exitData),
		"stdout_path": "stdout.txt", "stdout_sha256": fileSHA256(stdout),
		"stderr_path": "stderr.txt", "stderr_sha256": fileSHA256(stderr),
		"exit_code": int64(0), "stop_reason": "completed",
	}}}
	worker := asStringOr(input.StateView["worker_path"])
	if worker == "" {
		worker = p.project
	}
	baseline := asStringOr(asMap(input.Attempt["dependencies"])["baseline"])
	manifest, err := sourceManifestWithBaseline(worker, baseline, []string{"."})
	if err != nil {
		return ExecuteObservation{}, err
	}
	dependencies := asMap(input.Attempt["dependencies"])
	proposal := map[string]any{}
	switch stage {
	case "inspect":
		proposal = map[string]any{
			"complexity": "S", "risk": "low", "impact_flags": []any{},
			"rationale": "The deterministic fixture has a small source-only scope.",
		}
	case "implement":
		proposal = map[string]any{"changed_files": []any{}}
	case "verify":
		observations := map[string]any{"criteria": []any{map[string]any{
			"criterion_id": "readme", "kind": "file_assertion", "file": "readme.txt",
			"sha256":  nativeTestFileHashFromPath(filepath.Join(worker, "readme.txt")),
			"outcome": "PASS",
		}}}
		observationData, err := Canonical(observations)
		if err != nil {
			return ExecuteObservation{}, err
		}
		observationPath := filepath.Join(input.ArtifactRoot, "raw", "observations.json")
		if err := os.MkdirAll(filepath.Dir(observationPath), 0o700); err != nil {
			return ExecuteObservation{}, err
		}
		if err := os.WriteFile(observationPath, observationData, 0o600); err != nil {
			return ExecuteObservation{}, err
		}
		files["raw/observations.json"] = observationData
		artifacts = append(artifacts, ArtifactRef{Path: "raw/observations.json", SHA256: fileSHA256(observationData), SizeBytes: int64(len(observationData)), Kind: "verification"})
		proposal = nil
	default:
		proposal = map[string]any{"verdict": "PASS"}
	}
	observation := ExecuteObservation{
		SchemaVersion: 1, Contract: NativeProviderContract, TaskID: input.TaskID,
		AttemptID: asStringOr(input.Attempt["attempt_id"]), Stage: stage,
		Status: "completed", Summary: fmt.Sprintf("native test provider completed %s", stage),
		Proposal: proposal, SideEffects: "none", Dependencies: dependencies,
		SourceManifest: manifest, Artifacts: artifacts, ProcessReceipt: processReceipt,
		ProviderContract: input.ProviderContract,
	}
	transportStdout, err := Canonical(toExecuteObservation(observation))
	if err != nil {
		return ExecuteObservation{}, err
	}
	observation.Transport = nativeTestTransport(p.engine, transportStdout, []byte("native test provider transport stderr\n"))
	return observation, nil
}

// nativeTestTransport is the host-owned outer process receipt used by both
// Measure and Execute.  It deliberately follows nativeTransportReceiptFields
// so lifecycle tests cannot pass with a provider-manufactured pseudo-receipt.
func nativeTestTransport(engine EngineIdentity, stdout, stderr []byte) *ProviderTransportEvidence {
	providerScript := filepath.Join(engine.PolicyRoot, "1c-task", "scripts", "Invoke-BFNativeProvider.ps1")
	// The fixture keeps pwsh.exe beside the trusted host.  The transport
	// validator binds the provider script and compiled host to engine hashes;
	// executable identity is checked by the fixed basename and argv contract.
	pwshPath := filepath.Join(filepath.Dir(engine.HostPath), "pwsh.exe")
	receipt := map[string]any{
		"schema_version": int64(1),
		"executable":     pwshPath,
		"argv":           []any{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", providerScript},
		"started":        true,
		"terminal":       true,
		"exit_code":      int64(0),
		"stop_reason":    "",
		"stdout_bytes":   int64(len(stdout)),
		"stderr_bytes":   int64(len(stderr)),
		"stdout_sha256":  fileSHA256(stdout),
		"stderr_sha256":  fileSHA256(stderr),
		"duration_ms":    int64(1),
	}
	return &ProviderTransportEvidence{Receipt: receipt, Stdout: stdout, Stderr: stderr}
}

// This helper deliberately has no testing.T parameter because it is called
// from the fake provider after all fixture files already exist.
func nativeTestFileHashFromPath(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return fileSHA256(data)
}

func nativeTestRequest(t *testing.T, taskID, executable, toolsetRoot, toolsetHash string) map[string]any {
	t.Helper()
	executableHash := nativeTestFileHash(t, executable)
	deniedRoot := filepath.Join(filepath.Dir(executable), "denied")
	return map[string]any{
		"schema_version": 1, "request_id": taskID,
		"prompt": "Run the deterministic native controller lifecycle fixture.",
		"mode":   "implement", "analysis_goal": "analysis", "complexity": "S", "risk": "low",
		"impact_flags": []any{},
		"criteria": []any{map[string]any{
			"id": "readme", "observation": "The fixture readme remains present.",
			"kind": "file_assertion", "path": "readme.txt", "contains": "fixture",
		}},
		"provenance": map[string]any{"source": "user", "reference": "native-test", "text": "native test fixture"},
		"models": map[string]any{
			"worker": "gpt-6-astra", "worker_effort": "medium",
			"reviewer": "gpt-6-astra", "reviewer_effort": "high",
		},
		"execution_profile": map[string]any{
			"provider": "codex", "executable": executable, "executable_sha256": executableHash,
			"codex_skills_sha256": strings.Repeat("c", 64),
			"sandbox":             map[string]any{"executable": executable, "sha256": executableHash},
			"toolset":             map[string]any{"name": "cc-1c-skills", "root": toolsetRoot, "sha256": toolsetHash},
			"runtime": map[string]any{
				"executable": executable, "sha256": executableHash, "version": "3.12.14",
				"packages": []any{map[string]any{"name": "lxml", "version": "6.1.1"}},
			},
			"denied_read_roots": []any{deniedRoot},
		},
		"budget":       map[string]any{"currency": "USD", "limit": int64(10), "reservation": int64(0)},
		"max_attempts": int64(16), "timeout_seconds": int64(60), "max_source_repairs": int64(0),
	}
}

func newNativeTestFixture(t *testing.T, executeErr error) nativeTestFixture {
	t.Helper()
	project := newRepo(t)
	writeNativeTestFile(t, filepath.Join(project, ".gitignore"), []byte(".bsl-flow/\n"))
	runGit(t, project, "add", ".gitignore")
	runGit(t, project, "commit", "-m", "ignore native controller scratch")
	engine := newNativeTestEngine(t)
	provider := &nativeTestProvider{project: project, engine: engine.engine, executeErr: executeErr}
	created := createTask(t, project, map[string]any{"schema_version": 1, "title": "native controller fixture"})
	taskID := created["task_id"].(string)
	toolsetData, err := os.ReadFile(filepath.Join(engine.toolset, "toolset-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	toolsetManifest, err := DecodeObject(toolsetData)
	if err != nil {
		t.Fatal(err)
	}
	request := nativeTestRequest(t, taskID, engine.hostPath, engine.toolset, asStringOr(toolsetManifest["aggregate_sha256"]))
	inputPath := writeJSON(t, tempDir(t), "native-activation.json", request)
	host := NewControllerHost(provider, engine.engine)
	if _, err := commandActivate(project, taskID, "1", inputPath, host); err != nil {
		t.Fatalf("activate native fixture: %v", err)
	}
	return nativeTestFixture{project: project, taskID: taskID, engine: engine, provider: provider, host: host, request: request}
}

func nativeTestTaskPayload(t *testing.T, fixture nativeTestFixture) (map[string]any, *Repository, *Task) {
	t.Helper()
	repository, err := OpenRepository(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	task, err := repository.ReadTask(fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := cloneObject(asMap(task.State["controller"]))
	if err != nil {
		t.Fatal(err)
	}
	return payload, repository, task
}

func nativeTestRunUntilAccept(t *testing.T, fixture nativeTestFixture) {
	t.Helper()
	for step := 0; step < 8; step++ {
		payload, repository, task := nativeTestTaskPayload(t, fixture)
		next, err := controllerNext(task.State, payload)
		if err != nil {
			t.Fatalf("next at step %d: %v", step, err)
		}
		switch asStringOr(next["action"]) {
		case "dispatch":
			if _, err := controllerRun(fixture.project, fixture.taskID, fixture.host); err != nil {
				t.Fatalf("run %s: %v", asStringOr(next["stage"]), err)
			}
		case "accept":
			_ = repository
			return
		default:
			t.Fatalf("unexpected lifecycle action at step %d: %#v", step, next)
		}
	}
	t.Fatal("native lifecycle did not reach acceptance")
}

func requireNativeKind(t *testing.T, err error, kind string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error", kind)
	}
	var typed *KindError
	if !errors.As(err, &typed) {
		t.Fatalf("expected KindError %s, got %T: %v", kind, err, err)
	}
	if typed.Kind != kind {
		t.Fatalf("expected error kind %s, got %s: %v", kind, typed.Kind, err)
	}
}

func TestNativeControllerLifecycleUsesBoundProviderAndAcceptance(t *testing.T) {
	fixture := newNativeTestFixture(t, nil)
	nativeTestRunUntilAccept(t, fixture)
	accepted, err := commandControllerAccept(fixture.project, fixture.taskID)
	if err != nil {
		t.Fatalf("accept native fixture: %v", err)
	}
	acceptedMap, ok := accepted.(map[string]any)
	if !ok || asStringOr(acceptedMap["status"]) != "completed" {
		t.Fatalf("acceptance result is not completed: %#v", accepted)
	}
	payload, repository, task := nativeTestTaskPayload(t, fixture)
	if asStringOr(payload["status"]) != "completed" || len(anyItems(payload["acceptances"])) != 1 {
		t.Fatalf("completed state is not persisted: %#v", payload)
	}
	if asStringOr(payload["active_attempt"]) != "" {
		t.Fatal("completed lifecycle retained an active attempt")
	}
	if len(fixture.provider.executeCalls) != 3 {
		t.Fatalf("provider execute calls = %v, want inspect/implement/verify", fixture.provider.executeCalls)
	}
	if _, err := repository.ReadTask(task.ID); err != nil {
		t.Fatal(err)
	}
}

func TestNativeAttemptBindsTrustedExecutablePath(t *testing.T) {
	fixture := newNativeTestFixture(t, nil)
	payload, repository, task := nativeTestTaskPayload(t, fixture)
	request := asMap(payload["request"])
	profile := asMap(request["execution_profile"])
	attempt, _, _, _, err := prepareAttempt(repository, task.State, payload, fixture.engine.engine, "inspect")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := asStringOr(attempt["executable"]), asStringOr(profile["executable"]); got != want {
		t.Fatalf("attempt executable = %q, want trusted profile executable %q", got, want)
	}
	if got := asStringOr(attempt["executable"]); got == fixture.engine.engine.Provider {
		t.Fatalf("attempt executable contains provider contract instead of a path: %q", got)
	}
}

func TestNativeCancelPreservesRegisteredActiveAttempt(t *testing.T) {
	fixture := newNativeTestFixture(t, errors.New("simulated provider interruption"))
	if _, err := controllerRun(fixture.project, fixture.taskID, fixture.host); err == nil {
		t.Fatal("interrupted provider unexpectedly returned success")
	} else {
		requireNativeKind(t, err, "BF_BLOCKED")
	}
	payload, repository, task := nativeTestTaskPayload(t, fixture)
	active := asStringOr(payload["active_attempt"])
	if !isUUID(active) {
		t.Fatalf("interrupted run did not persist active attempt: %#v", payload["active_attempt"])
	}
	if _, err := commandControllerCancel(fixture.project, fixture.taskID); err != nil {
		t.Fatalf("cancel active attempt: %v", err)
	}
	payload, _, task = nativeTestTaskPayload(t, fixture)
	if asStringOr(payload["status"]) != "cancelled" || asStringOr(payload["active_attempt"]) != active {
		t.Fatalf("cancel lost recovery identity: status=%q active=%q want=%q", payload["status"], payload["active_attempt"], active)
	}
	if err := validateControllerState(payload, fixture.taskID); err != nil {
		t.Fatalf("cancelled active state is invalid: %v", err)
	}
	if _, err := commandControllerResume(fixture.project, fixture.taskID, fixture.host); err == nil {
		t.Fatal("resume replayed a cancelled active attempt")
	} else {
		requireNativeKind(t, err, "BF_BLOCKED")
	}
	if len(fixture.provider.executeCalls) != 1 {
		t.Fatalf("cancel/resume dispatched provider again: %v", fixture.provider.executeCalls)
	}
	if task == nil || repository == nil {
		t.Fatal("fixture task or repository unexpectedly nil")
	}
}

func TestNativeCompletedAcceptanceRechecksCurrentSource(t *testing.T) {
	fixture := newNativeTestFixture(t, nil)
	nativeTestRunUntilAccept(t, fixture)
	if _, err := commandControllerAccept(fixture.project, fixture.taskID); err != nil {
		t.Fatalf("initial acceptance: %v", err)
	}
	payload, _, _ := nativeTestTaskPayload(t, fixture)
	worker := asStringOr(payload["worker_path"])
	writeNativeTestFile(t, filepath.Join(worker, "readme.txt"), []byte("tampered after acceptance\n"))
	if _, err := commandControllerAccept(fixture.project, fixture.taskID); err == nil {
		t.Fatal("completed task accepted after worker source drift")
	} else {
		requireNativeKind(t, err, "BF_BLOCKED")
	}
}

func TestNativeEvidenceFreshFailsClosedOnIncompleteBindings(t *testing.T) {
	project := newRepo(t)
	baseline := runGit(t, project, "rev-parse", "HEAD")
	manifest, err := sourceManifestWithBaseline(project, baseline, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	fullDependencies := map[string]any{
		"source": manifest["sha256"], "policy": "policy-hash", "intent": "intent-hash", "baseline": baseline,
	}
	base := map[string]any{
		"outcome": "PASS", "result_sha256": strings.Repeat("a", 64),
		"dependencies": fullDependencies, "raw_hashes": []any{},
	}
	payload := map[string]any{
		"worker_path": project, "baseline": baseline,
		"policy_hash": "policy-hash", "intent_hash": "intent-hash",
	}
	cases := []struct {
		name string
		edit func(map[string]any)
	}{
		{name: "missing source dependency", edit: func(value map[string]any) { delete(asMap(value["dependencies"]), "source") }},
		{name: "missing policy dependency", edit: func(value map[string]any) { delete(asMap(value["dependencies"]), "policy") }},
		{name: "missing intent dependency", edit: func(value map[string]any) { delete(asMap(value["dependencies"]), "intent") }},
		{name: "missing baseline dependency", edit: func(value map[string]any) { delete(asMap(value["dependencies"]), "baseline") }},
		{name: "missing raw hashes", edit: func(value map[string]any) { delete(value, "raw_hashes") }},
		{name: "missing result hash", edit: func(value map[string]any) { delete(value, "result_sha256") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evidence, err := cloneObject(base)
			if err != nil {
				t.Fatal(err)
			}
			tc.edit(evidence)
			if controllerEvidenceFresh(nil, payload, evidence) {
				t.Fatalf("incomplete evidence was treated as fresh: %#v", evidence)
			}
		})
	}
}

func TestNativeAcceptanceRejectsTamperedTerminalEvidence(t *testing.T) {
	fixture := newNativeTestFixture(t, nil)
	nativeTestRunUntilAccept(t, fixture)
	if _, err := commandControllerAccept(fixture.project, fixture.taskID); err != nil {
		t.Fatalf("initial acceptance: %v", err)
	}
	payload, repository, _ := nativeTestTaskPayload(t, fixture)
	for _, raw := range anyItems(payload["evidence"]) {
		entry := asMap(raw)
		attemptID := asStringOr(entry["attempt_id"])
		attemptPath, err := attemptDirectory(repository, fixture.taskID, attemptID)
		if err != nil {
			t.Fatal(err)
		}
		terminalPath := filepath.Join(attemptPath, "terminal.json")
		data, err := os.ReadFile(terminalPath)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, []byte(" ")...)
		if err := os.WriteFile(terminalPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		break
	}
	if _, err := commandControllerAccept(fixture.project, fixture.taskID); err == nil {
		t.Fatal("acceptance ignored changed canonical terminal bytes")
	} else {
		requireNativeKind(t, err, "BF_BLOCKED")
	}
}

func TestNativeRecordRejectsUnregisteredExplicitAttempt(t *testing.T) {
	fixture := newNativeTestFixture(t, nil)
	payload, repository, task := nativeTestTaskPayload(t, fixture)
	attempt, attemptID, attemptPath, beforeManifest, err := prepareAttempt(repository, task.State, payload, fixture.engine.engine, "inspect")
	if err != nil {
		t.Fatal(err)
	}
	// prepareAttempt created a real start binding, but this attempt was never
	// appended to payload.attempts and is not active.  A fabricated terminal
	// result must therefore remain unusable as controller evidence.
	if err := writeNativeTestStoredObservation(t, attemptPath, fixture, attempt, beforeManifest, "inspect"); err != nil {
		t.Fatal(err)
	}
	if _, err := commandControllerRecord(fixture.project, fixture.taskID, attemptID, ""); err == nil {
		t.Fatal("record accepted an unregistered explicit attempt")
	} else {
		requireNativeKind(t, err, "BF_BLOCKED")
	}
	latest, _, _ := nativeTestTaskPayload(t, fixture)
	if len(anyItems(latest["evidence"])) != 0 || asStringOr(latest["active_attempt"]) != "" {
		t.Fatalf("unregistered record changed controller state: %#v", latest)
	}
}

func writeNativeTestStoredObservation(t *testing.T, attemptPath string, fixture nativeTestFixture, attempt map[string]any, beforeManifest map[string]any, stage string) error {
	t.Helper()
	artifactRoot, err := artifactDirectoryForAttempt(attemptPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		return err
	}
	// Reuse the same receipt shape as the fake provider.  The fabricated result
	// is otherwise valid, so the test isolates registration rather than another
	// observation contract failure.
	processID := int64(77)
	executable := asStringOr(attempt["executable"])
	process := map[string]any{"pid": processID, "start_time_utc": nowUTC(), "executable": executable, "arguments_sha256": strings.Repeat("0", 64)}
	exit := map[string]any{"exit_code": int64(0), "stop_reason": "completed", "elapsed_seconds": float64(0), "process_id": processID, "executable": executable, "stdout": filepath.Join(artifactRoot, "stdout.txt"), "stderr": filepath.Join(artifactRoot, "stderr.txt")}
	processData, err := Canonical(process)
	if err != nil {
		return err
	}
	exitData, err := Canonical(exit)
	if err != nil {
		return err
	}
	files := map[string][]byte{"process.json": processData, "exit.json": exitData, "stdout.txt": []byte("record fixture\n"), "stderr.txt": []byte{}}
	for relative, data := range files {
		if err := os.WriteFile(filepath.Join(artifactRoot, relative), data, 0o600); err != nil {
			return err
		}
	}
	artifacts := make([]ArtifactRef, 0, len(files))
	for _, relative := range []string{"process.json", "exit.json", "stdout.txt", "stderr.txt"} {
		data := files[relative]
		kind := "raw"
		if relative == "process.json" || relative == "exit.json" {
			kind = "process"
		}
		artifacts = append(artifacts, ArtifactRef{Path: relative, SHA256: fileSHA256(data), SizeBytes: int64(len(data)), Kind: kind})
	}
	observation := ExecuteObservation{
		SchemaVersion: 1, Contract: NativeProviderContract,
		TaskID: fixture.taskID, AttemptID: asStringOr(attempt["attempt_id"]), Stage: stage,
		Status: "completed", Summary: "fabricated unregistered terminal result",
		Proposal:    map[string]any{"complexity": "S", "risk": "low", "impact_flags": []any{}, "rationale": "fixture"},
		SideEffects: "none", Dependencies: asMap(attempt["dependencies"]), SourceManifest: beforeManifest,
		Artifacts: artifacts,
		ProcessReceipt: map[string]any{"processes": []any{map[string]any{
			"process_path": "process.json", "process_sha256": fileSHA256(processData),
			"exit_path": "exit.json", "exit_sha256": fileSHA256(exitData),
			"stdout_path": "stdout.txt", "stdout_sha256": fileSHA256(files["stdout.txt"]),
			"stderr_path": "stderr.txt", "stderr_sha256": fileSHA256(files["stderr.txt"]),
			"exit_code": int64(0), "stop_reason": "completed",
		}}},
		ProviderContract: providerContract(fixture.engine.engine),
	}
	_, err = writeImmutableJSON(filepath.Join(attemptPath, "result.json"), toExecuteObservation(observation))
	return err
}

func artifactDirectoryForAttempt(attemptPath string) (string, error) {
	// Canonical attempt artifacts are adjacent to start/result.  This helper
	// keeps the fabricated record fixture independent of provider scratch roots.
	return filepath.Join(attemptPath, "artifacts"), nil
}

func TestNativeVerifyRequiresEveryDeclaredCriterion(t *testing.T) {
	fixture := newNativeTestFixture(t, nil)
	payload, _, _ := nativeTestTaskPayload(t, fixture)
	request := asMap(payload["request"])
	request["criteria"] = append(anyItems(request["criteria"]), map[string]any{
		"id": "second", "observation": "A second criterion is independently observed.",
		"kind": "file_assertion", "path": "readme.txt", "contains": "fixture",
	})
	proposal := map[string]any{
		"passed": true,
		"criteria": []any{map[string]any{
			"criterion_id": "readme", "kind": "file_assertion", "file": "readme.txt",
			"sha256": nativeTestFileHash(t, filepath.Join(asStringOr(payload["worker_path"]), "readme.txt")), "outcome": "PASS",
		}},
	}
	observation := ExecuteObservation{
		SchemaVersion: 1, Contract: NativeProviderContract, TaskID: fixture.taskID,
		AttemptID: "00000000-0000-0000-0000-000000000001", Stage: "verify", Status: "completed",
		Summary: "verification fixture", Proposal: proposal, SideEffects: "none",
	}
	if _, err := validateStageEvidence(payload, observation, filepath.Join(tempDir(t), "artifacts")); err == nil {
		t.Fatal("verify accepted an observation that omitted a declared criterion")
	} else {
		requireNativeKind(t, err, "BF_BLOCKED")
	}
}

func TestNativeSpecRequiresCurrentSpecificationProof(t *testing.T) {
	fixture := newNativeTestFixture(t, nil)
	payload, _, _ := nativeTestTaskPayload(t, fixture)
	observation := ExecuteObservation{
		SchemaVersion: 1, Contract: NativeProviderContract, TaskID: fixture.taskID,
		AttemptID: "00000000-0000-0000-0000-000000000002", Stage: "spec", Status: "completed",
		Summary: "specification fixture completed", Proposal: nil, SideEffects: "none",
	}
	if _, err := validateStageEvidence(payload, observation, filepath.Join(tempDir(t), "artifacts")); err == nil {
		t.Fatal("spec completed without a proposal/proof was accepted")
	} else {
		requireNativeKind(t, err, "BF_BLOCKED")
	}
}
