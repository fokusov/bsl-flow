package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// opencodeFixture is the trusted controller state of one managed OpenCode
// dispatch.
type opencodeFixture struct {
	helper     string
	project    string
	workerPath string
	attempt    string
	profile    ExecutionProfile
}

const opencodeResult = `{"schema_version":1,"status":"completed","summary":"opencode fixture result","payload_json":"{\"done\":true}"}`

func newOpenCodeFixture(t *testing.T) *opencodeFixture {
	t.Helper()
	root := workerFixtureRoot(t)
	fixture := &opencodeFixture{
		helper:     workerHelper(t),
		project:    filepath.Join(root, "project"),
		workerPath: filepath.Join(root, "worktree"),
		attempt:    filepath.Join(root, "project", ".bsl-flow", "tasks", testTaskID, "attempts", "1"),
	}
	toolsetRoot := filepath.Join(root, "toolset")
	for _, directory := range []string{fixture.workerPath, toolsetRoot, filepath.Dir(fixture.attempt)} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(toolsetRoot, "toolset-manifest.json"), []byte(`{"skills":[{"name":"demo"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fixture.profile = ExecutionProfile{
		Provider:         "opencode",
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
	}
	writeFakeConfig(t, fixture.workerPath, fakeConfig{Result: opencodeResult})
	return fixture
}

func (f *opencodeFixture) request(stage, prompt string) OpenCodeRequest {
	permissions := "permissions.bsl_execution={filesystem={}}"
	return OpenCodeRequest{
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
		Models:         WorkerModels{Worker: "deepseek/deepseek-v4-flash"},
		UserProfile:    filepath.Dir(f.project),
		Dependencies: func() (map[string]any, error) {
			return map[string]any{"profile": testSHA256A}, nil
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

func TestRunOpenCodeWorkerLoop(t *testing.T) {
	t.Parallel()
	fixture := newOpenCodeFixture(t)
	authDirectory := filepath.Join(filepath.Dir(fixture.project), ".local", "share", "opencode")
	if err := os.MkdirAll(authDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authDirectory, "auth.json"), []byte(`{"deepseek":{"type":"api","key":"sk-fixture"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := RunOpenCode(context.Background(), fixture.request(StageImplement, "implement via opencode"))
	if err != nil {
		t.Fatalf("managed dispatch failed: %v", err)
	}
	if result.Status != StatusCompleted || result.Summary != "opencode fixture result" || result.PayloadJSON != `{"done":true}` {
		t.Fatalf("terminal result: %+v", result)
	}
	if result.SessionID != "ses_"+"fixture0123" {
		t.Fatalf("session: %q", result.SessionID)
	}
	if result.Usage.Total != 22 || result.Usage.Input != 14 || result.Usage.Output != 8 ||
		result.Usage.CacheRead != 2 || result.Usage.CacheWrite != 1 || result.ReportedCostUSD != 0.03 {
		t.Fatalf("usage evidence: %+v", result.Usage)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "read" {
		t.Fatalf("tool calls: %+v", result.ToolCalls)
	}
	hostResult, err := readJSONObjectFile(result.HostResultPath)
	if err != nil {
		t.Fatal(err)
	}
	if hostResult["session_id"] != "ses_"+"fixture0123" || hostResult["requested_model"] != "deepseek/deepseek-v4-flash" ||
		hostResult["requested_effort"] != nil || hostResult["observed_model"] != nil {
		t.Fatalf("host result identity: %+v", hostResult)
	}
	if hostResult["cost_source"] != "opencode.step_finish.part.cost.sum" {
		t.Fatalf("cost source: %v", hostResult["cost_source"])
	}
	usage, _ := asObject(hostResult["usage"])
	if value, _ := usage["total"]; value == nil {
		t.Fatalf("host result usage: %v", usage)
	}
	// The provider configuration receipt.
	hostRootHash, err := hashValue(workerSafePathOrFail(t, fixture.attempt))
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := readJSONObjectFile(filepath.Join(fixture.project, ".bsl-flow", "hosts", testTaskID, hostRootHash, "config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	if configuration["model"] != "deepseek/deepseek-v4-flash" || configuration["default_agent"] != "bsl-flow" {
		t.Fatalf("opencode configuration: %+v", configuration)
	}
	agent, _ := asObject(configuration["agent"])
	rules, _ := asObject(getValue(agent, "bsl-flow", nil))
	permission, _ := asObject(rules["permission"])
	if permission["read"] != "allow" || permission["bash"] != "allow" || permission["edit"] != "allow" || permission["*"] != "deny" {
		t.Fatalf("permission rules: %+v", permission)
	}
	external, _ := asObject(permission["external_directory"])
	if external["*"] != "deny" {
		t.Fatalf("external_directory rules: %+v", external)
	}
	if external[forwardSlash(fixture.profile.Toolset.Root)+"/*"] != "allow" {
		t.Fatalf("external_directory toolset root: %+v", external)
	}
	for _, name := range []string{"binding.json", "exit.json", "stdout.txt", "model-result.json", "capability/capability.json"} {
		if !fileLeafExists(filepath.Join(fixture.attempt, name)) {
			t.Fatalf("attempt receipt missing: %s", name)
		}
	}
	// A repeated dispatch resumes from the preserved receipts.
	resumed, err := RunOpenCode(context.Background(), fixture.request(StageImplement, "implement via opencode"))
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if resumed.SessionID != result.SessionID || resumed.Status != result.Status {
		t.Fatalf("resume diverged: %+v vs %+v", resumed, result)
	}
}

func TestRunOpenCodeRefusals(t *testing.T) {
	fixture := newOpenCodeFixture(t)
	authDirectory := filepath.Join(filepath.Dir(fixture.project), ".local", "share", "opencode")
	if err := os.MkdirAll(authDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authDirectory, "auth.json"), []byte(`{"deepseek":{"type":"api","key":"sk-fixture"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	base := fixture.request(StageImplement, "prompt")
	// Output bound is validated by the managed process layer (the bound case
	// must precede every case that leaves a partial attempt directory).
	bound := base
	bound.MaxOutputBytes = 1024
	if _, err := RunOpenCode(context.Background(), bound); err == nil ||
		err.Error() != "BF_INVALID: managed output bound is outside the supported range." {
		t.Fatalf("bound: %v", err)
	}
	// Typed Blocker: provider/sandbox identity mismatch.
	providerMismatch := base
	providerMismatch.Profile.Provider = "codex"
	_, err := RunOpenCode(context.Background(), providerMismatch)
	var blocker *Blocker
	if !errors.As(err, &blocker) || err.Error() != "BF_BLOCKED: managed provider/sandbox identity mismatch." {
		t.Fatalf("provider mismatch: %v", err)
	}
	pathMismatch := base
	pathMismatch.CodexPath = fixture.helper + ".other"
	if _, err := RunOpenCode(context.Background(), pathMismatch); !errors.As(err, &blocker) {
		t.Fatalf("sandbox mismatch: %v", err)
	}
	// Only the measured Flash route with no effort override.
	wrongModel := base
	wrongModel.Models.Worker = "deepseek/deepseek-r1"
	if _, err := RunOpenCode(context.Background(), wrongModel); err == nil ||
		err.Error() != "BF_BLOCKED: only the measured Flash route with no effort override is supported." {
		t.Fatalf("route pinning: %v", err)
	}
	withEffort := base
	withEffort.Models.WorkerEffort = "high"
	if _, err := RunOpenCode(context.Background(), withEffort); err == nil ||
		err.Error() != "BF_BLOCKED: only the measured Flash route with no effort override is supported." {
		t.Fatalf("effort pinning: %v", err)
	}
}

func TestRunOpenCodeAuthorizationRefusal(t *testing.T) {
	fixture := newOpenCodeFixture(t)
	authDirectory := filepath.Join(filepath.Dir(fixture.project), ".local", "share", "opencode")
	if err := os.MkdirAll(authDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authDirectory, "auth.json"), []byte(`{"deepseek":{"type":"oauth"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RunOpenCode(context.Background(), fixture.request(StageImplement, "prompt")); err == nil ||
		err.Error() != "BF_BLOCKED: existing DeepSeek API authorization is unavailable." {
		t.Fatalf("authorization type: %v", err)
	}
}

func TestRunManagedWorkerDispatch(t *testing.T) {
	t.Parallel()
	fixture := newCodexFixture(t)
	var order []string
	request := fixture.request(StageImplement, "dispatch through the shared gates")
	managed := ManagedWorkerRequest{
		Stage:          request.Stage,
		Prompt:         request.Prompt,
		Directory:      request.Directory,
		CodexPath:      request.CodexPath,
		MaxOutputBytes: 16777216,
		WorkerPath:     request.WorkerPath,
		ProjectPath:    request.ProjectPath,
		TaskID:         request.TaskID,
		TimeoutSeconds: request.TimeoutSeconds,
		Profile:        request.Profile,
		Models:         request.Models,
		SchemaPath:     request.SchemaPath,
		AdapterSHA256:  request.AdapterSHA256,
		RpcSHA256:      request.RpcSHA256,
		Dependencies:   request.Dependencies,
		PermissionProfile: func(scratch, config string, writable bool) (string, error) {
			return "permissions.bsl_execution={filesystem={}}", nil
		},
		TestExecutionCapability: request.TestExecutionCapability,
		RuntimePreflight: func(directory string) error {
			order = append(order, "preflight")
			return nil
		},
		Budget: &BudgetHooks{
			Admit: func(ctx context.Context, stage, model string) error {
				order = append(order, "admit:"+model)
				return nil
			},
			Reserve: func(ctx context.Context, stage, model string) error {
				order = append(order, "reserve:"+model)
				return nil
			},
			Complete: func(ctx context.Context, stage, model string) error {
				order = append(order, "complete:"+model)
				return nil
			},
		},
	}
	outcome, err := RunManagedWorker(context.Background(), managed)
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if outcome.Provider != "codex" || outcome.Status != StatusCompleted || outcome.SessionID != testSession {
		t.Fatalf("outcome: %+v", outcome)
	}
	expected := []string{"preflight", "admit:gpt-5.6-sol", "reserve:gpt-5.6-sol", "complete:gpt-5.6-sol"}
	if strings.Join(order, "|") != strings.Join(expected, "|") {
		t.Fatalf("budget order: %v", order)
	}
	// A reservation refusal skips the worker and the completion.
	failing := managed
	failing.Directory = fixture.attempt + "-failing"
	failing.Budget = &BudgetHooks{
		Admit: func(ctx context.Context, stage, model string) error { return nil },
		Reserve: func(ctx context.Context, stage, model string) error {
			return blocked("reservation refused")
		},
		Complete: func(ctx context.Context, stage, model string) error {
			t.Fatal("completion must not run after a refusal")
			return nil
		},
	}
	if _, err := RunManagedWorker(context.Background(), failing); err == nil || err.Error() != "BF_BLOCKED: reservation refused" {
		t.Fatalf("reservation refusal: %v", err)
	}
	// Provider selection routes opencode profiles to the OpenCode adapter.
	opencode := failing
	opencode.Profile.Provider = "opencode"
	opencode.Models = WorkerModels{Worker: "deepseek/deepseek-v4-flash"}
	opencode.UserProfile = filepath.Dir(opencode.ProjectPath)
	opencode.Budget = &BudgetHooks{
		Admit: func(ctx context.Context, stage, model string) error { return nil },
		Reserve: func(ctx context.Context, stage, model string) error {
			return nil
		},
	}
	if _, err := RunManagedWorker(context.Background(), opencode); err == nil ||
		err.Error() != "BF_INVALID: JSON file does not exist: "+filepath.Join(filepath.Dir(opencode.ProjectPath), ".local", "share", "opencode", "auth.json") {
		t.Fatalf("opencode routing: %v", err)
	}
	// No execution profile is a dispatch error (the unmanaged Codex route is
	// not ported).
	bare := managed
	bare.Profile = ExecutionProfile{}
	if _, err := RunManagedWorker(context.Background(), bare); err == nil ||
		err.Error() != "BF_INVALID: managed worker dispatch requires an execution profile." {
		t.Fatalf("profile requirement: %v", err)
	}
}
