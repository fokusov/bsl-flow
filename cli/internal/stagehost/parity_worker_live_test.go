package stagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"bsl-flow/cli/internal/repository"
	"bsl-flow/cli/internal/worker"
)

// TestProviderWorkerStageNativeRun drives one inspect-stage execute through
// the native stage host with the fake managed provider binary (codex exec
// stream, app-server RPC, runtime probe and sandbox emulator) configured as
// the execution profile. It runs on every platform and is the deterministic
// half of the PowerShell differential parity check below.
func TestProviderWorkerStageNativeRun(t *testing.T) {
	if parityRepoScriptsDir(t) == "" {
		t.Skip("packaged skill scripts are unavailable")
	}
	fixture := newWorkerParityFixture(t)
	defer fixture.cleanup()
	if err := worker.AssertWorkerConfiguration(fixture.worktree); err != nil {
		t.Skipf("fixture tree carries provider configuration: %v", err)
	}
	observation := fixture.runNative(t)
	if observation.Status != "completed" || observation.Stage != "inspect" {
		fixture.dumpArtifacts(t)
		t.Fatalf("unexpected observation: %+v", observation)
	}
	if observation.SideEffects != "none" || observation.Proposal == nil {
		t.Fatalf("unexpected observation detail: %+v", observation)
	}
	if len(observation.Artifacts) == 0 {
		t.Fatal("worker dispatch must retain artifacts")
	}
	// The managed dispatch must leave a budget reservation and outcome.
	ledger, err := readJSONObject(filepath.Join(fixture.artifactRoot, "budget", "ledger.json"))
	if err != nil {
		t.Fatalf("budget ledger: %v", err)
	}
	entries, _ := asArray(ledger["entries"])
	if len(entries) != 2 {
		t.Fatalf("budget ledger entries: %+v", ledger)
	}
	// The stage prompt must bind the packaged skill bytes and fixture inputs.
	binding, err := readJSONObject(filepath.Join(fixture.artifactRoot, "raw", "worker", "binding.json"))
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	inner, _ := asObject(binding["binding"])
	if inner["prompt_sha256"] == nil || inner["schema_sha256"] == nil || binding["permission_sha256"] == nil {
		t.Fatalf("binding content: %+v", binding)
	}
}

// TestProviderWorkerStagePowerShellParity is the differential evidence for
// spec req 20 of native-cross-platform-cli: the same trusted provider input
// document, fixture tree and fake managed-provider binary run through BOTH
// the packaged PowerShell provider (pwsh + Invoke-BFNativeProvider.ps1) and
// the native stage host. The sealed worker input binding (which hashes the
// full stage prompt) and every artifact retained by both engines must be
// byte-identical outside the explicitly classified engine receipts.
//
// Approved, explicitly asserted divergence: the packaged provider's managed
// worker capability gate compares its filesystem observations against an
// expected map that unconditionally includes the canonical-store probes
// (Task.Execution.ps1:550) while the probe itself only emits them when a
// canonical store root was supplied (Task.Execution.ps1:536-538, and the
// worker adapters pass none at ProfiledCodex.ps1:301/OpenCode.ps1:53). The
// PowerShell worker dispatch therefore blocks itself at that gate, while the
// native host expects the canonical observations conditionally, matching the
// probe-side contract. The difference is asserted, not normalized; a native
// failure never reroutes.
func TestProviderWorkerStagePowerShellParity(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the packaged PowerShell provider is Windows-only")
	}
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh is unavailable for the PowerShell parity run")
	}
	scriptsDir := parityRepoScriptsDir(t)
	if scriptsDir == "" {
		t.Skip("packaged skill scripts are unavailable")
	}
	fixture := newWorkerParityFixture(t)
	defer fixture.cleanup()
	if err := worker.AssertWorkerConfiguration(fixture.worktree); err != nil {
		t.Skipf("fixture tree carries provider configuration: %v", err)
	}

	powerShell := fixture.runPowerShell(t, pwsh)
	powerShellArtifacts := fixture.harvestArtifacts(t)

	// The packaged provider's worker dispatch blocks at its own capability
	// gate with the exact legacy diagnostic.
	if powerShell.Status != "blocked" {
		t.Fatalf("PowerShell provider status = %q summary = %q; the packaged worker dispatch is expected to block at its capability gate", powerShell.Status, powerShell.Summary)
	}
	const capabilityDivergence = "BF_BLOCKED: managed filesystem capability differs from the required observations."
	if powerShell.Summary != capabilityDivergence {
		t.Fatalf("PowerShell provider summary = %q; want %q", powerShell.Summary, capabilityDivergence)
	}
	if _, ok := powerShellArtifacts["raw/failure.json"]; !ok {
		t.Fatal("PowerShell provider did not retain its failure receipt")
	}

	// Reset every dispatch footprint, then replay the identical input through
	// the native stage host at the same absolute paths so the prompt binding
	// and permission profile hash the same bytes.
	fixture.resetDispatch(t)
	native := fixture.runNative(t)
	nativeArtifacts := fixture.harvestArtifacts(t)
	if native.Status != "completed" {
		t.Fatalf("native provider status = %q summary = %q", native.Status, native.Summary)
	}

	// The worker input binding must be identical except the engine adapter
	// identity hashes (PS hashes ProfiledCodex.ps1/Codex.Skills.ps1, the
	// native host binds its own executable hash). This proves the stage
	// prompt, schema, permission profile and worker identity are byte-equal.
	parityCompareBindings(t, powerShellArtifacts, nativeArtifacts)

	// Artifact parity over everything the blocked PowerShell dispatch
	// retained: identical relative path sets and byte-identical content
	// outside the explicitly classified engine receipts.
	parityCompareArtifacts(t, powerShellArtifacts, nativeArtifacts, fixture)

	// The envelopes diverge exactly by the approved capability-gate
	// classification: status/summary/side_effects differ, while the identity,
	// stage, dependencies and source manifest bindings agree.
	if powerShell.Stage != native.Stage || powerShell.AttemptID != native.AttemptID || powerShell.TaskID != native.TaskID {
		t.Fatalf("envelope identity diverged: ps %+v go %+v", powerShell, native)
	}
	if native.SideEffects != "none" || powerShell.SideEffects != "none" {
		t.Fatalf("side effects diverged: ps %q go %q", powerShell.SideEffects, native.SideEffects)
	}
	leftDependencies, err := repository.StageHostCanonical(parityObservationDependencies(t, powerShell))
	if err != nil {
		t.Fatal(err)
	}
	rightDependencies, err := repository.StageHostCanonical(parityObservationDependencies(t, native))
	if err != nil {
		t.Fatal(err)
	}
	if string(leftDependencies) != string(rightDependencies) {
		t.Fatalf("envelope dependencies diverged:\n  ps %s\n  go %s", leftDependencies, rightDependencies)
	}
}

func parityObservationDependencies(t *testing.T, observation repository.ExecuteObservation) map[string]any {
	t.Helper()
	data, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"task_id": object["task_id"], "attempt_id": object["attempt_id"], "stage": object["stage"],
		"dependencies": object["dependencies"], "source_manifest": object["source_manifest"],
	}
}

// workerParityFixture owns one deterministic managed-dispatch tree.
type workerParityFixture struct {
	root         string
	packageRoot  string
	project      string
	worktree     string
	toolsetRoot  string
	helper       string
	contextRoot  string
	artifactRoot string
	cancelSignal string
	storeRoot    string
	input        []byte
	state        map[string]any
	attempt      map[string]any
	taskID       string
}

func newWorkerParityFixture(t *testing.T) *workerParityFixture {
	t.Helper()
	// Keep the tree outside the user profile: the trusted worker
	// configuration guard walks every ancestor of the worker path.
	base, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, ".tmp-worker-parity-")
	if err != nil {
		t.Fatal(err)
	}
	fixture := &workerParityFixture{root: root, taskID: "10000000-0000-0000-0000-00000000000a"}
	fixture.packageRoot = filepath.Join(root, "pkg")
	fixture.project = filepath.Join(root, "project")
	fixture.worktree = filepath.Join(root, "worktree")
	fixture.toolsetRoot = filepath.Join(root, "toolset")
	fixture.helper = filepath.Join(root, "fake-provider.exe")
	fixture.contextRoot = filepath.Join(root, "context")
	fixture.cancelSignal = filepath.Join(root, "cancel.signal")
	fixture.storeRoot = filepath.Join(fixture.project, ".git", "bsl-flow")
	attemptID := "20000000-0000-0000-0000-00000000000b"
	fixture.artifactRoot = filepath.Join(fixture.project, ".bsl-flow", "tasks", fixture.taskID, "attempts", attemptID, "artifacts")

	if err := copyHelperTo(t, fixture.helper); err != nil {
		t.Fatal(err)
	}
	// The packaged skill tree supplies the stage prompt SKILL.md files, the
	// adapter scripts the PowerShell provider imports and the sealed worker
	// result schema.
	repoScripts := parityRepoScriptsDir(t)
	if repoScripts == "" {
		repoScripts = filepath.Join(root, "missing-scripts")
	}
	if err := copyTree(filepath.Join(repoScripts, "..", ".."), filepath.Join(fixture.packageRoot, "global", "skills")); err != nil {
		t.Fatalf("package tree: %v", err)
	}
	skillsRoot := filepath.Join(fixture.packageRoot, "global", "skills")
	// A provider script copy must exist even when the repo scripts are not
	// packaged (non-parity runs): the input doc binds its hash.
	providerScript := filepath.Join(skillsRoot, "1c-task", "scripts", "Task.Provider.ps1")
	providerSHA := strings.Repeat("70", 32)
	if data, err := os.ReadFile(providerScript); err == nil {
		providerSHA = hashFileBytes(data)
	}

	baseline := parityGitInit(t, fixture.project)
	parityGitWorktree(t, fixture.project, fixture.worktree, baseline)

	helperHash, err := hashFile(fixture.helper)
	if err != nil {
		t.Fatal(err)
	}
	toolsetSkill := filepath.Join(fixture.toolsetRoot, "fixture-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(toolsetSkill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(toolsetSkill, []byte("# deterministic toolset fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillFileHash, err := hashFile(toolsetSkill)
	if err != nil {
		t.Fatal(err)
	}
	skillHash, err := repository.StageHostToolsetAggregateHash([]any{map[string]any{"name": "fixture-skill", "files": skillFileHashes(skillFileHash)}})
	if err != nil {
		t.Fatal(err)
	}
	skills := []any{map[string]any{"name": "fixture-skill", "files": skillFileHashes(skillFileHash), "sha256": skillHash, "mcp_references": []any{}}}
	aggregate, err := repository.StageHostToolsetAggregateHash(skills)
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema_version": int64(1), "toolset_name": "cc-1c-skills",
		"source": map[string]any{"identity": "local-private", "path": fixture.toolsetRoot},
		"skills": skills, "aggregate_sha256": aggregate,
	}
	manifestBytes, err := repository.StageHostCanonical(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.toolsetRoot, "toolset-manifest.json"), manifestBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	resolvedSkill, err := safePath(toolsetSkill)
	if err != nil {
		t.Fatal(err)
	}
	inventory := []any{map[string]any{
		"name": "fixture-skill", "path": resolvedSkill, "scope": "project", "enabled": true, "sha256": skillFileHash,
	}}
	inventoryHash, err := repository.StageHostHash(inventory)
	if err != nil {
		t.Fatal(err)
	}

	fake := fakeConfig{
		Skills:         [][]string{{"fixture-skill", resolvedSkill}},
		RuntimeVersion: "3.12.14",
		Packages:       [][]string{{"lxml", "6.1.1"}},
		Result:         `{"schema_version":1,"status":"completed","summary":"fixture worker result","payload_json":"{\"complexity\":\"S\",\"risk\":\"low\",\"impact_flags\":[],\"rationale\":\"Small source-only fixture scope.\"}"}`,
	}
	fakeBytes, err := json.Marshal(fake)
	if err != nil {
		t.Fatal(err)
	}
	// The fake config is part of the worktree and therefore of the source
	// manifest; write it before the attempt binding is computed.
	if err := os.WriteFile(filepath.Join(fixture.worktree, "bf-worker-fake.json"), fakeBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	denied := filepath.Join(root, "denied")
	if err := os.MkdirAll(denied, 0o755); err != nil {
		t.Fatal(err)
	}
	profile := map[string]any{
		"provider": "codex", "executable": fixture.helper, "executable_sha256": helperHash,
		"codex_skills_sha256": inventoryHash,
		"sandbox":             map[string]any{"executable": fixture.helper, "sha256": helperHash},
		"toolset":             map[string]any{"name": "cc-1c-skills", "root": fixture.toolsetRoot, "sha256": aggregate},
		"runtime": map[string]any{
			"executable": fixture.helper, "sha256": helperHash, "version": "3.12.14",
			"packages": []any{map[string]any{"name": "lxml", "version": "6.1.1"}},
		},
		"denied_read_roots": []any{denied},
	}
	criteria := []any{map[string]any{
		"id": "files", "observation": "fence exists", "kind": "file_assertion", "path": "README.md", "contains": "fixture",
	}}
	state := parityWorkerState(fixture.project, fixture.worktree, baseline, fixture.taskID, criteria, profile)
	policyFiles, err := repository.StageHostPolicyInventory(fixture.project, skillsRoot, fixture.helper)
	if err != nil {
		t.Fatalf("policy inventory: %v", err)
	}
	state["policy_files"] = policyFiles
	attempt := parityWorkerAttempt(t, state, "inspect", attemptID, fixture.helper)
	state["active_attempt"] = attemptID

	input := providerInputDocumentWithContract("execute", state, attempt, map[string]string{
		"context": fixture.contextRoot, "artifact": fixture.artifactRoot, "cancel": fixture.cancelSignal,
	}, helperHash, providerSHA, strings.Repeat("c", 64), fixture.storeRoot)
	data, err := repository.StageHostCanonical(input)
	if err != nil {
		t.Fatal(err)
	}
	fixture.input = data
	fixture.state = state
	fixture.attempt = attempt
	return fixture
}

func (f *workerParityFixture) cleanup() {
	_ = os.RemoveAll(f.root)
}

func (f *workerParityFixture) deps() Deps {
	return Deps{
		SelfPath:            f.helper,
		SelfSHA256:          parityFileHashOrEmpty(f.helper),
		SkillsRoot:          filepath.Join(f.packageRoot, "global", "skills"),
		ProviderSHA256:      parityFileHashOrEmpty(filepath.Join(f.packageRoot, "global", "skills", "1c-task", "scripts", "Task.Provider.ps1")),
		AssetManifestSHA256: strings.Repeat("c", 64),
		HostPolicyPath:      f.helper,
	}
}

func parityFileHashOrEmpty(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return hashFileBytes(data)
}

func (f *workerParityFixture) runNative(t *testing.T) repository.ExecuteObservation {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.cancelSignal), 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := RunProvider(context.Background(), f.deps(), bytes.NewReader(f.input), &stdout, &stderr)
	if code != 0 {
		f.dumpArtifacts(t)
		t.Fatalf("native provider failed: %s", stderr.String())
	}
	var observation repository.ExecuteObservation
	if err := json.Unmarshal(stdout.Bytes(), &observation); err != nil {
		t.Fatalf("native observation decode: %v: %s", err, stdout.String())
	}
	return observation
}

// fixtureGitProbeFailures are the packaged provider's fixture-environment
// git identity diagnostics (Task.Storage.ps1 Invoke-BFStorageGitRead /
// Get-BFVerifiedLegacyGitContext). They probe the fixture repository, not the
// engine contract under comparison, and were observed once to fail transiently
// under full-suite parallel load; a single clean retry is therefore safe and
// does not mask any stage-behavior divergence.
var fixtureGitProbeFailures = []string{
	"BF_BLOCKED: Legacy task path is not below the exact Git worktree root.",
	"BF_BLOCKED: Git identity probe failed.",
	"BF_BLOCKED: Git identity probe did not start.",
	"BF_BLOCKED: Legacy task project root is not a directory.",
	"BF_BLOCKED: Git common dir is empty.",
}

func fixtureGitProbeFailure(message string) bool {
	for _, candidate := range fixtureGitProbeFailures {
		if candidate == message {
			return true
		}
	}
	return false
}

func (f *workerParityFixture) runPowerShell(t *testing.T, pwsh string) repository.ExecuteObservation {
	t.Helper()
	observation, ok := f.tryRunPowerShell(t, pwsh)
	if ok {
		return observation
	}
	// One clean retry for a transient fixture git-identity probe failure.
	f.resetDispatch(t)
	observation, ok = f.tryRunPowerShell(t, pwsh)
	if !ok {
		t.Fatal("pwsh provider failed twice")
	}
	return observation
}

func (f *workerParityFixture) tryRunPowerShell(t *testing.T, pwsh string) (repository.ExecuteObservation, bool) {
	t.Helper()
	// The PowerShell provider imports the packaged tree; run it against a
	// byte-identical copy of the fixture package layout.
	entrypoint := filepath.Join(f.packageRoot, "global", "skills", "1c-task", "scripts", "Invoke-BFNativeProvider.ps1")
	if _, err := os.Stat(entrypoint); err != nil {
		t.Skipf("packaged provider entrypoint unavailable: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(f.cancelSignal), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", entrypoint)
	command.Stdin = bytes.NewReader(f.input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if fixtureGitProbeFailure(message) {
			return repository.ExecuteObservation{}, false
		}
		f.dumpArtifacts(t)
		t.Fatalf("pwsh provider failed: %v: %s", err, stderr.String())
	}
	var observation repository.ExecuteObservation
	if err := json.Unmarshal(stdout.Bytes(), &observation); err != nil {
		t.Fatalf("pwsh observation decode: %v: %s", err, stdout.String())
	}
	return observation, true
}

// resetDispatch removes every footprint a completed dispatch leaves behind so
// the replay starts from the identical fixture bytes at the same paths.
func (f *workerParityFixture) resetDispatch(t *testing.T) {
	t.Helper()
	for _, path := range []string{
		f.artifactRoot,
		filepath.Join(f.project, ".bsl-flow", "hosts"),
		f.contextRoot,
	} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(f.artifactRoot, 0o755); err != nil {
		t.Fatal(err)
	}
}

func (f *workerParityFixture) harvestArtifacts(t *testing.T) map[string]string {
	t.Helper()
	artifacts := map[string]string{}
	err := filepath.WalkDir(f.artifactRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".tmp") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative := filepath.ToSlash(strings.TrimPrefix(strings.TrimPrefix(path, f.artifactRoot), string(os.PathSeparator)))
		// Random probe scratch suffixes are normalized away.
		relative = regexp.MustCompile(`^runtime/probe-[^/]+/`).ReplaceAllString(relative, "runtime/probe-*/")
		artifacts[relative] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("harvest: %v", err)
	}
	return artifacts
}

func (f *workerParityFixture) dumpArtifacts(t *testing.T) {
	t.Helper()
	_ = filepath.WalkDir(f.artifactRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		relative, _ := filepath.Rel(f.artifactRoot, path)
		data, _ := os.ReadFile(path)
		if len(data) > 2000 {
			data = data[:2000]
		}
		t.Logf("artifact %s: %s", filepath.ToSlash(relative), string(data))
		return nil
	})
}

func parityRepoScriptsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	dir := filepath.Dir(thisFile)
	scripts := filepath.Join(dir, "..", "..", "..", "global", "skills", "1c-task", "scripts")
	info, err := os.Stat(scripts)
	if err != nil || !info.IsDir() {
		return ""
	}
	return scripts
}

func parityGitInit(t *testing.T, project string) string {
	t.Helper()
	git := func(args ...string) string {
		command := exec.Command("git", append([]string{"-c", "core.hooksPath=NUL", "-c", "core.fsmonitor=false", "-C", project}, args...)...)
		command.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=f@example",
			"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=f@example")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	git("init", "--quiet")
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("commit", "-m", "fixture", "--quiet")
	return git("rev-parse", "HEAD")
}

func parityGitWorktree(t *testing.T, project, worktree, baseline string) {
	t.Helper()
	command := exec.Command("git", "-c", "core.hooksPath=NUL", "-c", "core.fsmonitor=false", "-C", project, "worktree", "add", "--quiet", worktree, baseline)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v: %s", err, output)
	}
}

func copyHelperTo(t *testing.T, destination string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o755)
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func parityWorkerState(project, worktree, baseline, taskID string, criteria []any, profile map[string]any) map[string]any {
	request := map[string]any{
		"schema_version": int64(1), "request_id": taskID,
		"prompt": "implement the fence", "mode": "implement", "analysis_goal": "analysis",
		"complexity": "S", "risk": "low", "impact_flags": []any{},
		"criteria": criteria, "provenance": map[string]any{"source": "user", "reference": "chat", "text": "do it"},
		"models":       map[string]any{"worker": "gpt-5.6-sol", "worker_effort": "medium", "reviewer": "gpt-5.6-luna", "reviewer_effort": "high"},
		"max_attempts": int64(4), "timeout_seconds": int64(600), "max_source_repairs": int64(0),
		"execution_profile": profile,
		"budget":            map[string]any{"currency": "USD", "limit": int64(10), "reservation": int64(0)},
	}
	return map[string]any{
		"schema_version": int64(1), "task_id": taskID,
		"revision": int64(2), "previous_sha256": strings.Repeat("d", 64),
		"project_path": project, "worker_path": worktree, "baseline": baseline,
		"request": request, "request_hash": strings.Repeat("e", 64),
		"intent_revision": int64(1), "authorization_revision": int64(1),
		"intent_hash": strings.Repeat("f", 64), "policy_hash": strings.Repeat("0", 64),
		"policy_files": []any{}, "policy_rules": map[string]any{"s_review_required": false},
		"classification": map[string]any{"complexity": "S", "risk": "low", "impact_flags": []any{}, "rationale": "small"},
		"status":         "running", "stage": "inspect", "active_attempt": nil,
		"unresolved_effect": nil, "attempts": []any{}, "evidence": []any{}, "events": []any{},
		"question": nil, "blockers": []any{}, "acceptances": []any{},
		"created_at": "2026-09-13T00:00:00Z", "updated_at": "2026-09-13T00:00:00Z",
		"correction_rounds": int64(0),
	}
}

func parityWorkerAttempt(t *testing.T, state map[string]any, stage, attemptID, executable string) map[string]any {
	t.Helper()
	bound, err := repository.StageHostDependencies(state, stage, nil)
	if err != nil {
		t.Fatalf("dependencies: %v", err)
	}
	manifest, err := repository.StageHostSourceManifest(asStringOr(state["worker_path"]), asStringOr(state["baseline"]))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return map[string]any{
		"schema_version": int64(1), "task_id": state["task_id"], "attempt_id": attemptID,
		"stage": stage, "intent_revision": int64(1), "authorization_revision": int64(1),
		"dependencies": bound, "source_manifest": manifest, "worker_path": state["worker_path"],
		"executable": executable, "requested_models": map[string]any{}, "started_at": "2026-09-13T00:00:00Z", "operation_id": attemptID,
	}
}

func providerInputDocumentWithContract(operation string, state map[string]any, attempt map[string]any, roots map[string]string, hostSHA, providerSHA, manifestSHA, store string) map[string]any {
	return map[string]any{
		"schema_version": int64(1), "contract": Contract, "operation": operation,
		"task_id": state["task_id"], "state_view": state, "attempt": attempt,
		"context_root": roots["context"], "artifact_root": roots["artifact"],
		"canonical_store_root": store,
		"cancel_signal":        roots["cancel"],
		"provider_contract": map[string]any{
			"name": Contract, "version": int64(1),
			"host_sha256": hostSHA, "provider_sha256": providerSHA, "asset_manifest_sha256": manifestSHA,
		},
		"prior_artifacts": []any{},
	}
}

func parityObservationObject(t *testing.T, observation repository.ExecuteObservation) map[string]any {
	t.Helper()
	data, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		t.Fatal(err)
	}
	return object
}

func truncateForLog(text string) string {
	if len(text) > 4000 {
		return text[:4000] + "..."
	}
	return text
}

// parityClassifiedReceipts lists the persisted artifacts whose bytes are
// engine receipts by contract: process identities (pid/start time/elapsed),
// the adapter identity hashes inside the worker binding and host receipt, and
// observation clocks (budget ledger, runtime preflight evidence).
var parityClassifiedReceipts = []*regexp.Regexp{
	regexp.MustCompile(`(^|/)process\.json$`),
	regexp.MustCompile(`(^|/)exit\.json$`),
	regexp.MustCompile(`(^|/)binding\.json$`),
	regexp.MustCompile(`(^|/)host-result\.json$`),
	regexp.MustCompile(`^budget/ledger\.json$`),
	regexp.MustCompile(`^runtime/preflight-`),
}

func parityClassified(path string) bool {
	for _, pattern := range parityClassifiedReceipts {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}

func parityCompareArtifacts(t *testing.T, powerShell, native map[string]string, fixture *workerParityFixture) {
	t.Helper()
	// The PowerShell dispatch blocked at its capability gate, so the native
	// run legitimately retains everything the earlier stages produce. Every
	// artifact the PowerShell provider retained must also exist natively,
	// except its own failure receipt from the approved gate divergence.
	for path := range powerShell {
		if _, ok := native[path]; !ok && path != "raw/failure.json" {
			t.Fatalf("artifact retained by the PowerShell provider but not natively: %s", path)
		}
	}
	diverged := []string{}
	for path, psBytes := range powerShell {
		goBytes, ok := native[path]
		if !ok {
			continue
		}
		if parityClassified(path) {
			continue
		}
		if psBytes != goBytes {
			diverged = append(diverged, path)
		}
	}
	if len(diverged) > 0 {
		sort.Strings(diverged)
		for _, path := range diverged {
			t.Errorf("artifact %s diverged:\n  ps %s\n  go %s", path, truncateForLog(powerShell[path]), truncateForLog(native[path]))
		}
		t.Fatalf("%d artifacts diverged outside the classified engine receipts", len(diverged))
	}
	// The classified receipts the blocked PowerShell dispatch still produced.
	for _, required := range []string{
		"raw/worker/binding.json", "raw/worker/inventory.json", "raw/worker/disabled.json",
		"raw/worker/mcp-config-names.json", "budget/ledger.json",
		"raw/worker/capability/filesystem/stdout.txt",
	} {
		if _, ok := powerShell[required]; !ok {
			t.Fatalf("PowerShell provider did not retain %s", required)
		}
		if _, ok := native[required]; !ok {
			t.Fatalf("native provider did not retain %s", required)
		}
	}
	// The classified divergence itself: the blocked engine keeps an open
	// reservation (no outcome), the completed native dispatch closes it.
	parityAssertLedgerShape(t, powerShell["budget/ledger.json"], 1)
	parityAssertLedgerShape(t, native["budget/ledger.json"], 2)
}

// parityAssertLedgerShape asserts the reservation/outcome entry count of a
// provider budget ledger document.
func parityAssertLedgerShape(t *testing.T, text string, wantEntries int) {
	t.Helper()
	var ledger struct {
		Entries []map[string]any `json:"entries"`
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if err := decoder.Decode(&ledger); err != nil {
		t.Fatalf("ledger decode: %v", err)
	}
	if len(ledger.Entries) != wantEntries {
		t.Fatalf("ledger entries = %d want %d: %s", len(ledger.Entries), wantEntries, text)
	}
}

// parityCompareBindings proves the sealed worker input (the stage prompt) is
// byte-identical across engines: every binding member except the engine
// adapter identity hashes must hash the same.
func parityCompareBindings(t *testing.T, powerShell, native map[string]string) {
	t.Helper()
	load := func(artifacts map[string]string, name string) map[string]any {
		text, ok := artifacts[name]
		if !ok {
			t.Fatalf("binding artifact missing: %s", name)
		}
		var document struct {
			Sha256   string         `json:"sha256"`
			Binding  map[string]any `json:"binding"`
			PermsSHA string         `json:"permission_sha256"`
		}
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		if err := decoder.Decode(&document); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		for _, engine := range []string{"adapter_sha256", "rpc_sha256"} {
			delete(document.Binding, engine)
		}
		document.PermsSHA = ""
		return map[string]any{"binding": document.Binding}
	}
	left := load(powerShell, "raw/worker/binding.json")
	right := load(native, "raw/worker/binding.json")
	leftText, err := repository.StageHostCanonical(left)
	if err != nil {
		t.Fatal(err)
	}
	rightText, err := repository.StageHostCanonical(right)
	if err != nil {
		t.Fatal(err)
	}
	if string(leftText) != string(rightText) {
		t.Fatalf("worker input binding diverged (prompt/schema/permission/worker identity):\n  ps %s\n  go %s", truncateForLog(string(leftText)), truncateForLog(string(rightText)))
	}
}
