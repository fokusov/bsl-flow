package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testSHA256A = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testSHA256B = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testSHA256C = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testTaskID  = "11111111-2222-4333-8444-555555555555"
	testSession = "22222222-2222-4222-8222-222222222222"
)

// workerFixtureRoot creates the fixture tree outside the user profile: the
// trusted worker-configuration guard walks every ancestor of the worker path,
// and developer machines carry a real .codex home under the profile that
// t.TempDir() would inherit.
func workerFixtureRoot(t *testing.T) string {
	t.Helper()
	base, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if err := AssertWorkerConfiguration(base); err != nil {
		t.Skipf("fixture base carries project execution configuration: %v", err)
	}
	root, err := os.MkdirTemp(base, ".bf-worker-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

// codexFixture is the trusted controller state of one managed Codex dispatch.
type codexFixture struct {
	helper     string
	project    string
	workerPath string
	attempt    string
	schema     string
	profile    ExecutionProfile
	skillsSHA  string
}

func newCodexFixture(t *testing.T) *codexFixture {
	t.Helper()
	root := workerFixtureRoot(t)
	fixture := &codexFixture{
		helper:     workerHelper(t),
		project:    filepath.Join(root, "project"),
		workerPath: filepath.Join(root, "worktree"),
		attempt:    filepath.Join(root, "project", ".bsl-flow", "tasks", testTaskID, "attempts", "1"),
		schema:     filepath.Join(root, "worker-result.schema.json"),
	}
	toolsetRoot := filepath.Join(root, "toolset")
	skillPath := filepath.Join(toolsetRoot, "demo", "SKILL.md")
	for _, directory := range []string{fixture.workerPath, filepath.Dir(skillPath), filepath.Dir(fixture.attempt)} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(skillPath, []byte("# demo skill instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolsetRoot, "toolset-manifest.json"), []byte(`{"skills":[{"name":"demo"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.schema, []byte(`{"type":"object"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	resolvedSkill, err := workerSafePath(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	skillDigest, err := fileHash(resolvedSkill)
	if err != nil {
		t.Fatal(err)
	}
	inventory := []any{map[string]any{
		"name": "demo", "path": resolvedSkill, "scope": "project", "enabled": true, "sha256": skillDigest,
	}}
	skillsSHA, err := hashValue(inventory)
	if err != nil {
		t.Fatal(err)
	}
	fixture.skillsSHA = skillsSHA
	fixture.profile = ExecutionProfile{
		Provider:         "codex",
		Executable:       fixture.helper,
		ExecutableSHA256: testSHA256A,
		Sandbox:          SandboxIdentity{Executable: fixture.helper, SHA256: testSHA256B},
		Toolset:          ToolsetIdentity{Name: "cc-1c-skills", Root: toolsetRoot, SHA256: testSHA256C},
		Runtime: &RuntimePin{
			Executable: filepath.Join(root, "runtime", "python.exe"),
			SHA256:     testSHA256A,
			Version:    "3.12.1",
			Packages:   []RuntimePackage{{Name: "lxml", Version: "5.2.1"}},
		},
		CodexSkillsSHA256: skillsSHA,
	}
	writeFakeConfig(t, fixture.workerPath, fakeConfig{Skills: [][]string{{"demo", resolvedSkill}}})
	return fixture
}

func (f *codexFixture) request(stage, prompt string) ProfiledCodexRequest {
	permissions := "permissions.bsl_execution={filesystem={}}"
	return ProfiledCodexRequest{
		Stage:          stage,
		Prompt:         prompt,
		Directory:      f.attempt,
		CodexPath:      f.helper,
		MaxOutputBytes: 16777216,
		WorkerPath:     f.workerPath,
		ProjectPath:    f.project,
		TaskID:         testTaskID,
		TimeoutSeconds: 120,
		Profile:        f.profile,
		Models:         WorkerModels{Worker: "gpt-5.6-sol", WorkerEffort: "medium", Reviewer: "gpt-5.6-luna", ReviewerEffort: "high"},
		SchemaPath:     f.schema,
		AdapterSHA256:  testSHA256A,
		RpcSHA256:      testSHA256B,
		Dependencies: func() (map[string]any, error) {
			return map[string]any{"profile": testSHA256A, "models": "gpt-5.6-sol"}, nil
		},
		PermissionProfile: func(scratch, config string, writable bool) (string, error) {
			return permissions, nil
		},
		TestExecutionCapability: func(capabilityDir, scratch, config, permissions string, writable bool) error {
			if err := os.MkdirAll(capabilityDir, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(capabilityDir, "capability.json"), []byte(`{"probe":"ok"}`), 0o644)
		},
	}
}

func TestRunProfiledCodexWorkerLoop(t *testing.T) {
	t.Parallel()
	fixture := newCodexFixture(t)
	request := fixture.request(StageImplement, "implement the fixture")
	result, err := RunProfiledCodex(context.Background(), request)
	if err != nil {
		t.Fatalf("managed dispatch failed: %v", err)
	}
	if result.Status != StatusCompleted || result.Summary != "fixture worker result" || result.PayloadJSON != "{}" {
		t.Fatalf("terminal result: %+v", result)
	}
	if result.SessionID != testSession || result.RequestedModel != "gpt-5.6-sol" || result.RequestedEffort != "medium" {
		t.Fatalf("identity: %+v", result)
	}
	if result.Usage == nil || result.Usage.InputTokens != 4 || result.Usage.OutputTokens != 2 {
		t.Fatalf("usage: %+v", result.Usage)
	}
	if result.ObservedModel != "" || result.ObservedEffort != "" || result.RolloutPath != "" {
		t.Fatalf("ephemeral run must keep nullable identity: %+v", result)
	}
	// Immutable attempt receipts.
	for _, name := range []string{
		"binding.json", "exit.json", "model-result.json", "inventory.json", "disabled.json",
		"post-inventory.json", "mcp-inventory.json", "mcp-config-names.json", "stdout.txt",
		"capability/capability.json",
	} {
		if !fileLeafExists(filepath.Join(fixture.attempt, filepath.FromSlash(name))) {
			t.Fatalf("attempt receipt missing: %s", name)
		}
	}
	exit, err := readJSONObjectFile(filepath.Join(fixture.attempt, "exit.json"))
	if err != nil {
		t.Fatal(err)
	}
	if value, present := exit["stop_reason"]; !present || value != nil {
		t.Fatalf("exit stop_reason: %v", exit["stop_reason"])
	}
	// Host-result receipt shape and canonical determinism.
	hostResult, err := readJSONObjectFile(result.HostResultPath)
	if err != nil {
		t.Fatal(err)
	}
	if hostResult["session_id"] != testSession || hostResult["requested_model"] != "gpt-5.6-sol" ||
		hostResult["requested_effort"] != "medium" || hostResult["observed_model"] != nil {
		t.Fatalf("host result identity: %+v", hostResult)
	}
	usage, _ := asObject(hostResult["usage"])
	if value, _ := asInt64(usage["input_tokens"]); value != 4 {
		t.Fatalf("host result usage: %v", usage)
	}
	if hostResult["usage_source"] != filepath.Join(fixture.attempt, "stdout.txt") {
		t.Fatalf("usage source: %v", hostResult["usage_source"])
	}
	expectedStdoutHash, err := fileHash(filepath.Join(fixture.attempt, "stdout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if hostResult["stdout_sha256"] != expectedStdoutHash {
		t.Fatalf("stdout hash: %v", hostResult["stdout_sha256"])
	}
	same, err := hashValueEqual(hostResult, result.HostResult)
	if err != nil || !same {
		t.Fatalf("host result map must be canonical-ready: %v", err)
	}
	// The attempt directory layout keeps the controller-private shape.
	binding, err := readJSONObjectFile(filepath.Join(fixture.attempt, "binding.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assertFields(binding, []string{"sha256", "binding", "permission_sha256"}, nil, "binding"); err != nil {
		t.Fatalf("binding shape: %v", err)
	}
	inner, _ := asObject(binding["binding"])
	if inner["stage"] != StageImplement || inner["codex_skills_sha256"] != nil {
		t.Fatalf("binding content: %+v", inner)
	}
	if inner["prompt_sha256"] == "" || inner["worker_path"] != fixture.workerPath {
		t.Fatalf("binding content: %+v", inner)
	}
}

func TestRunProfiledCodexWorkerLoopResume(t *testing.T) {
	t.Parallel()
	fixture := newCodexFixture(t)
	request := fixture.request(StageImplement, "resume the fixture")
	first, err := RunProfiledCodex(context.Background(), request)
	if err != nil {
		t.Fatalf("first dispatch failed: %v", err)
	}
	// A repeated identical dispatch resumes from the preserved receipts.
	second, err := RunProfiledCodex(context.Background(), request)
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if second.Status != first.Status || second.SessionID != first.SessionID || second.HostResultPath != first.HostResultPath {
		t.Fatalf("resume diverged: %+v vs %+v", first, second)
	}
	// A different prompt computes a different binding and refuses the resume.
	changed := fixture.request(StageImplement, "a different prompt entirely")
	_, err = RunProfiledCodex(context.Background(), changed)
	if err == nil || err.Error() != "BF_BLOCKED: cached Codex binding differs." {
		t.Fatalf("binding mismatch diagnostic: %v", err)
	}
	// A partially preserved attempt cannot be retried.
	if err := os.Remove(filepath.Join(fixture.attempt, "disabled.json")); err != nil {
		t.Fatal(err)
	}
	_, err = RunProfiledCodex(context.Background(), request)
	if err == nil || err.Error() != "BF_BLOCKED: partial Codex dispatch; reconcile the preserved attempt without retry." {
		t.Fatalf("partial attempt diagnostic: %v", err)
	}
}

func TestRunProfiledCodexWorkerLoopStrictCritic(t *testing.T) {
	t.Parallel()
	fixture := newCodexFixture(t)
	fixture.profile.ExecutableSHA256 = criticCodexSHA256
	codexHome := t.TempDir()
	rolloutDirectory := filepath.Join(codexHome, "sessions", "2026", "09", "14")
	if err := os.MkdirAll(rolloutDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(rolloutDirectory, "rollout-2026-09-14T10-00-00.000-"+testSession+".jsonl")
	rolloutText := strings.Join([]string{
		`{"timestamp":"2026-09-14T10:00:00.000Z","ordinal":0,"type":"session_meta","payload":{"session_id":"` + testSession + `"}}`,
		`{"timestamp":"2026-09-14T10:00:01.000Z","ordinal":1,"type":"turn_context","payload":{"turn_id":"turn_fixture1","model":"gpt-6-astra","effort":"xhigh"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(rollout, []byte(rolloutText), 0o644); err != nil {
		t.Fatal(err)
	}
	sourcePath, err := filepath.Abs("testdata/critic-catalog-source.json")
	if err != nil {
		t.Fatal(err)
	}
	sourceDigest, err := fileHash(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	request := fixture.request(StageSpecReview, "review the specification strictly")
	request.Models.Reviewer = "gpt-6-astra"
	request.Models.ReviewerEffort = "xhigh"
	request.RequireObservedIdentity = true
	request.FallbackCatalogSourcePath = sourcePath
	request.FallbackCatalogSHA256 = sourceDigest
	request.CodexHome = codexHome
	result, err := RunProfiledCodex(context.Background(), request)
	if err != nil {
		t.Fatalf("strict critic dispatch failed: %v", err)
	}
	if result.RequestedModel != "gpt-6-astra" || result.ObservedModel != "gpt-6-astra" ||
		result.ObservedEffort != "xhigh" || result.RolloutPath != rollout || result.TurnID != "turn_fixture1" {
		t.Fatalf("strict identity: %+v", result)
	}
	hostResult, err := readJSONObjectFile(result.HostResultPath)
	if err != nil {
		t.Fatal(err)
	}
	if hostResult["rollout_path"] != rollout || hostResult["turn_id"] != "turn_fixture1" ||
		hostResult["observed_model"] != "gpt-6-astra" {
		t.Fatalf("strict receipt: %+v", hostResult)
	}
	for _, name := range []string{"critic-catalog.json", "critic-catalog-source.json"} {
		if !fileLeafExists(filepath.Join(fixture.attempt, name)) {
			t.Fatalf("critic receipt missing: %s", name)
		}
	}
	// A rollout that disagrees with the strict request refuses the dispatch.
	strictMismatch := strings.Replace(rolloutText, `"model":"gpt-6-astra"`, `"model":"gpt-5.6-luna"`, 1)
	if err := os.WriteFile(rollout, []byte(strictMismatch), 0o644); err != nil {
		t.Fatal(err)
	}
	attempt := fixture.attempt + "-mismatch"
	fixture.attempt = attempt
	request.Directory = attempt
	_, err = RunProfiledCodex(context.Background(), request)
	if err == nil || err.Error() != "BF_BLOCKED: observed rollout model/effort differs from the strict request." {
		t.Fatalf("strict mismatch diagnostic: %v", err)
	}
}

func TestRunProfiledCodexWorkerLoopTimeoutRefusal(t *testing.T) {
	t.Parallel()
	fixture := newCodexFixture(t)
	writeFakeConfig(t, fixture.workerPath, fakeConfig{
		Skills:       [][]string{{"demo", skillPathOf(t, fixture)}},
		SleepSeconds: 30,
	})
	request := fixture.request(StageImplement, "sleep past the bound")
	request.TimeoutSeconds = 1
	_, err := RunProfiledCodex(context.Background(), request)
	if err == nil || err.Error() != "BF_BLOCKED: Codex attempt failed; preserve sources and reconcile without retry." {
		t.Fatalf("timeout diagnostic: %v", err)
	}
}

func skillPathOf(t *testing.T, fixture *codexFixture) string {
	t.Helper()
	return filepath.Join(fixture.profile.Toolset.Root, "demo", "SKILL.md")
}

func TestRunProfiledCodexRefusals(t *testing.T) {
	fixture := newCodexFixture(t)
	base := fixture.request(StageImplement, "prompt")
	// Typed Blocker: provider/sandbox identity mismatch.
	providerMismatch := base
	providerMismatch.Profile.Provider = "opencode"
	_, err := RunProfiledCodex(context.Background(), providerMismatch)
	var blocker *Blocker
	if !errors.As(err, &blocker) || err.Error() != "BF_BLOCKED: managed Codex/sandbox identity mismatch." {
		t.Fatalf("provider mismatch: %v", err)
	}
	pathMismatch := base
	pathMismatch.CodexPath = fixture.helper + ".other"
	if _, err := RunProfiledCodex(context.Background(), pathMismatch); !errors.As(err, &blocker) {
		t.Fatalf("sandbox mismatch: %v", err)
	}
	// Output bound.
	bound := base
	bound.MaxOutputBytes = 1024
	if _, err := RunProfiledCodex(context.Background(), bound); err == nil ||
		err.Error() != "BF_INVALID: managed output bound is outside the supported range." {
		t.Fatalf("bound: %v", err)
	}
	// Trusted worker configuration guard.
	guarded := base
	if err := os.MkdirAll(filepath.Join(fixture.workerPath, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	configToml := filepath.Join(fixture.workerPath, ".codex", "config.toml")
	if err := os.WriteFile(configToml, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RunProfiledCodex(context.Background(), guarded); err == nil ||
		err.Error() != "BF_BLOCKED: managed adapter does not load project execution configuration: "+configToml {
		t.Fatalf("worker configuration: %v", err)
	}
	if err := os.Remove(configToml); err != nil {
		t.Fatal(err)
	}
	// Controller-private directory rule.
	outside := base
	outside.Directory = filepath.Join(fixture.project, "elsewhere", "attempt")
	if _, err := RunProfiledCodex(context.Background(), outside); err == nil ||
		err.Error() != "BF_INVALID: Codex controller attempt must be under the private task directory." {
		t.Fatalf("controller directory: %v", err)
	}
	// Critic capability pinning.
	critic := fixture.request(StageSpecReview, "review")
	if _, err := RunProfiledCodex(context.Background(), critic); err == nil ||
		err.Error() != "BF_BLOCKED: no-tools critic capability requires the exact verified native executable." {
		t.Fatalf("critic executable: %v", err)
	}
	fixture.profile.ExecutableSHA256 = criticCodexSHA256
	nonStrict := fixture.request(StageSpecReview, "review")
	nonStrict.Models.Reviewer = "gpt-6-astra"
	if _, err := RunProfiledCodex(context.Background(), nonStrict); err == nil ||
		err.Error() != "BF_BLOCKED: non-strict sealed critic is pinned to gpt-5.6-luna." {
		t.Fatalf("critic pinning: %v", err)
	}
	strict := fixture.request(StageSpecReview, "review")
	strict.RequireObservedIdentity = true
	if _, err := RunProfiledCodex(context.Background(), strict); err == nil ||
		err.Error() != "BF_BLOCKED: strict current-agent critic requires an exact catalog source binding." {
		t.Fatalf("strict binding: %v", err)
	}
	// Unregistered host directory.
	other := fixture.request(StageCodeReview, "review code")
	directoryHash, err := hashValue(workerSafePathOrFail(t, other.Directory))
	if err != nil {
		t.Fatal(err)
	}
	hostRoot := filepath.Join(fixture.project, ".bsl-flow", "hosts", testTaskID, directoryHash)
	if err := os.MkdirAll(hostRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := RunProfiledCodex(context.Background(), other); err == nil ||
		err.Error() != "BF_BLOCKED: unregistered Codex host directory exists." {
		t.Fatalf("host directory: %v", err)
	}
}

func TestRunProfiledCodexSkillInventoryRefusal(t *testing.T) {
	fixture := newCodexFixture(t)
	// The registered inventory must match the discovered one exactly.
	drifted := fixture.profile
	drifted.CodexSkillsSHA256 = testSHA256C
	request := fixture.request(StageImplement, "prompt")
	request.Profile = drifted
	_, err := RunProfiledCodex(context.Background(), request)
	if err == nil || err.Error() != "BF_BLOCKED: discovered Codex skills differ from the registered inventory." {
		t.Fatalf("inventory diagnostic: %v", err)
	}
}

func workerSafePathOrFail(t *testing.T, path string) string {
	t.Helper()
	resolved, err := workerSafePath(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestObservedModelEffortLookup(t *testing.T) {
	home := t.TempDir()
	day := filepath.Join(home, "sessions", "2026", "09", "14")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(day, "rollout-2026-09-14T10-00-00.000-"+testSession+".jsonl")
	lines := []string{
		`{"type":"session_meta","payload":{"session_id":"` + testSession + `"}}`,
		`{"type":"turn_context","payload":{"turn_id":"turn_a","model":"gpt-5.6-sol","effort":"medium"}}`,
		`{"type":"turn_context","payload":{"turn_id":"turn_b","model":"gpt-5.6-luna","effort":"high"}}`,
	}
	if err := os.WriteFile(rollout, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity, err := ObservedModelEffort(testSession, "", home)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ObservedModel != "gpt-5.6-luna" || identity.ObservedEffort != "high" ||
		identity.RolloutPath != rollout || identity.TurnID != "turn_b" {
		t.Fatalf("latest identity: %+v", identity)
	}
	selected, err := ObservedModelEffort(testSession, "turn_a", home)
	if err != nil {
		t.Fatal(err)
	}
	if selected.ObservedModel != "gpt-5.6-sol" || selected.TurnID != "turn_a" {
		t.Fatalf("selected identity: %+v", selected)
	}
	if _, err := ObservedModelEffort("not-a-uuid", "", home); err == nil ||
		err.Error() != "BF_INVALID: Codex session id has an invalid format." {
		t.Fatalf("session format: %v", err)
	}
	if _, err := ObservedModelEffort(testSession, "", filepath.Join(home, "missing")); err == nil ||
		!strings.HasPrefix(err.Error(), "BF_BLOCKED: Codex sessions directory is missing: ") {
		t.Fatalf("missing sessions root: %v", err)
	}
	if _, err := ObservedModelEffort("99999999-9999-4999-8999-999999999999", "", home); err == nil ||
		!strings.HasPrefix(err.Error(), "BF_BLOCKED: Codex rollout session file not found for ") {
		t.Fatalf("missing rollout: %v", err)
	}
	if _, err := ObservedModelEffort(testSession, "turn_missing", home); err == nil ||
		!strings.HasPrefix(err.Error(), "BF_BLOCKED: Codex rollout turn identity turn_missing was not found") {
		t.Fatalf("missing turn: %v", err)
	}
	// Multiple matching rollout files are ambiguous without selection.
	if err := os.WriteFile(filepath.Join(day, "rollout-2026-09-14T11-00-00.000-"+testSession+".jsonl"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ObservedModelEffort(testSession, "", home); err == nil ||
		!strings.HasPrefix(err.Error(), "BF_BLOCKED: Multiple Codex rollout files match session ") {
		t.Fatalf("ambiguous rollout: %v", err)
	}
}
