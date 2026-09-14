package stagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bsl-flow/cli/internal/repository"
)

func stagehostGitRepository(t *testing.T) (string, string) {
	t.Helper()
	project := t.TempDir()
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
	git("init", "--quiet")
	if err := os.WriteFile(filepath.Join(project, "README.md"), []byte("fixture"), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	git("add", "README.md")
	git("commit", "-m", "fixture", "--quiet")
	return project, git("rev-parse", "HEAD")
}

// stageWorktree creates the isolated worker worktree a managed attempt uses:
// separate from the project root so controller paths stay private.
func stageWorktree(t *testing.T, project, baseline string) string {
	t.Helper()
	worktree := filepath.Join(t.TempDir(), "worktree")
	command := exec.Command("git", "-c", "core.hooksPath=NUL", "-c", "core.fsmonitor=false", "-C", project, "worktree", "add", "--quiet", worktree, baseline)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v: %s", err, output)
	}
	return worktree
}

func testDeps(t *testing.T) Deps {
	t.Helper()
	self := copyHelper(t, t.TempDir(), "stagehost-self.exe")
	selfSHA, err := hashFile(self)
	if err != nil {
		t.Fatalf("self hash: %v", err)
	}
	skills := t.TempDir()
	skillsRoot := filepath.Join(skills, "global", "skills")
	providerScript := filepath.Join(skillsRoot, "1c-task", "scripts", "Task.Provider.ps1")
	if err := os.MkdirAll(filepath.Dir(providerScript), 0o755); err != nil {
		t.Fatalf("skills: %v", err)
	}
	if err := os.WriteFile(providerScript, []byte("# provider contract fixture\n"), 0o644); err != nil {
		t.Fatalf("skills: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsRoot, "SKILL.md"), []byte("# skill fixture"), 0o644); err != nil {
		t.Fatalf("skills: %v", err)
	}
	return Deps{
		SelfPath: self, SelfSHA256: selfSHA, SkillsRoot: skillsRoot,
		ProviderSHA256: strings.Repeat("b", 64), AssetManifestSHA256: strings.Repeat("c", 64),
		HostPolicyPath: self,
		RunProcess:     sandboxRunner(t),
	}
}

// sandboxRunner emulates the managed sandbox: version receipts and the
// enforced filesystem probe. The probe output mirrors what the codex sandbox
// would enforce: private canonical paths denied, scratch allowed, source
// writable only under a write permission entry for the worker root.
func sandboxRunner(t *testing.T) func(context.Context, ProcessOptions) (ProcessResult, error) {
	return func(ctx context.Context, opts ProcessOptions) (ProcessResult, error) {
		if err := os.MkdirAll(opts.OutputDirectory, 0o755); err != nil {
			return ProcessResult{}, err
		}
		stdoutPath := filepath.Join(opts.OutputDirectory, "stdout.txt")
		stderrPath := filepath.Join(opts.OutputDirectory, "stderr.txt")
		_ = os.WriteFile(stderrPath, nil, 0o644)
		stop := ""
		switch {
		case len(opts.Arguments) == 1 && opts.Arguments[0] == "--version":
			_ = os.WriteFile(stdoutPath, []byte("codex-cli 0.154.0"), 0o644)
		case len(opts.Arguments) > 2 && opts.Arguments[len(opts.Arguments)-2] == "__fs-probe":
			if err := emulateProbe(opts.Arguments, &stdoutWriter{path: stdoutPath}); err != nil {
				return ProcessResult{ExitCode: 1, StopReason: "probe"}, nil
			}
		case len(opts.Arguments) > 1 && opts.Arguments[0] == "sandbox":
			// The criterion process writes its original JUnit report into the
			// declared worktree path, exactly like a real test executable.
			worker := ""
			for index, argument := range opts.Arguments {
				if argument == "-C" && index+1 < len(opts.Arguments) {
					worker = opts.Arguments[index+1]
				}
			}
			if worker != "" {
				reportPath := filepath.Join(worker, ".bsl-flow-worker", "report.xml")
				if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
					return ProcessResult{}, err
				}
				junit := `<testsuite tests="1" failures="0" errors="0" skipped="0"><testcase name="TestOne"/></testsuite>`
				if err := os.WriteFile(reportPath, []byte(junit), 0o644); err != nil {
					return ProcessResult{}, err
				}
			}
			_ = os.WriteFile(stdoutPath, nil, 0o644)
		default:
			stop = "exit"
		}
		if stop == "" {
			result := ProcessResult{ExitCode: 0, ProcessID: 7, Executable: opts.Executable, Stdout: stdoutPath, Stderr: stderrPath}
			argumentsHash, err := hashValue(toAnySlice(opts.Arguments))
			if err != nil {
				return ProcessResult{}, err
			}
			if err := writeJSON(filepath.Join(opts.OutputDirectory, "process.json"), map[string]any{
				"pid": int64(7), "start_time_utc": startTimeUTC(time.Now().UTC()),
				"executable": opts.Executable, "arguments_sha256": argumentsHash,
			}, false); err != nil {
				return ProcessResult{}, err
			}
			if err := writeJSON(filepath.Join(opts.OutputDirectory, "exit.json"), result.ExitObject(), false); err != nil {
				return ProcessResult{}, err
			}
			return result, nil
		}
		return ProcessResult{ExitCode: 1, StopReason: stop}, nil
	}
}

func toAnySlice(values []string) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

// emulateProbe renders the observation the managed sandbox enforces instead of
// actually probing the host filesystem, which has no sandbox. Writable source
// access is inferred from the permission profile argument: a write entry for
// the worker root appears next to the always-writable scratch entry.
func emulateProbe(arguments []string, stdout io.Writer) error {
	var paths map[string]string
	for index, argument := range arguments {
		if argument == "__fs-probe" && index+1 < len(arguments) {
			if err := json.Unmarshal([]byte(arguments[index+1]), &paths); err != nil {
				return err
			}
		}
	}
	writable := false
	for _, argument := range arguments {
		if strings.Contains(argument, "filesystem={") && strings.Count(argument, `="write"`) > 1 {
			writable = true
		}
	}
	result := map[string]string{
		"config_read":   "allowed",
		"config_write":  "denied",
		"scratch_write": "allowed",
		"source_write":  "denied",
	}
	if _, provided := paths["canonical_current_read"]; provided {
		for _, name := range []string{"current", "revision", "inputs"} {
			result["canonical_"+name+"_read"] = "denied"
			result["canonical_"+name+"_write"] = "denied"
		}
	}
	if writable {
		result["source_write"] = "allowed"
		if paths["source_write"] != "" {
			if err := os.WriteFile(paths["source_write"], []byte("probe"), 0o644); err != nil {
				return err
			}
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = stdout.Write(data)
	return err
}

// managedProfileFixture builds a deterministic cc-1c-skills execution profile
// with real executable hashes and a matching toolset snapshot manifest.
func managedProfileFixture(t *testing.T, deps Deps) map[string]any {
	t.Helper()
	toolsetRoot := t.TempDir()
	skillFile := filepath.Join(toolsetRoot, "fixture-skill", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillFile), 0o755); err != nil {
		t.Fatalf("toolset: %v", err)
	}
	if err := os.WriteFile(skillFile, []byte("# deterministic toolset fixture\n"), 0o644); err != nil {
		t.Fatalf("toolset: %v", err)
	}
	skillFileHash, err := hashFile(skillFile)
	if err != nil {
		t.Fatalf("toolset hash: %v", err)
	}
	skillHash, err := repository.StageHostToolsetAggregateHash([]any{map[string]any{"name": "fixture-skill", "files": skillFileHashes(skillFileHash)}})
	if err != nil {
		t.Fatalf("skill hash: %v", err)
	}
	skills := []any{map[string]any{"name": "fixture-skill", "files": skillFileHashes(skillFileHash), "sha256": skillHash, "mcp_references": []any{}}}
	aggregate, err := repository.StageHostToolsetAggregateHash(skills)
	if err != nil {
		t.Fatalf("aggregate hash: %v", err)
	}
	manifest := map[string]any{
		"schema_version": int64(1), "toolset_name": "cc-1c-skills",
		"source": map[string]any{"identity": "local-private", "path": toolsetRoot},
		"skills": skills, "aggregate_sha256": aggregate,
	}
	manifestBytes, err := repository.StageHostCanonical(manifest)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(toolsetRoot, "toolset-manifest.json"), manifestBytes, 0o644); err != nil {
		t.Fatalf("manifest write: %v", err)
	}
	denied := t.TempDir()
	return map[string]any{
		"provider": "codex", "executable": deps.SelfPath, "executable_sha256": deps.SelfSHA256,
		"codex_skills_sha256": strings.Repeat("c", 64),
		"sandbox":             map[string]any{"executable": deps.SelfPath, "sha256": deps.SelfSHA256},
		"toolset":             map[string]any{"name": "cc-1c-skills", "root": toolsetRoot, "sha256": aggregate},
		"runtime": map[string]any{
			"executable": deps.SelfPath, "sha256": deps.SelfSHA256, "version": "3.12.14",
			"packages": []any{map[string]any{"name": "lxml", "version": "6.1.1"}},
		},
		"denied_read_roots": []any{denied},
	}
}

func skillFileHashes(hash string) []any {
	return []any{map[string]any{"path": "SKILL.md", "sha256": hash}}
}

func TestProviderMeasureRoundTrip(t *testing.T) {
	project, baseline := stagehostGitRepository(t)
	deps := testDeps(t)
	profile := managedProfileFixture(t, deps)
	canonical := filepath.Join(project, ".git", "bsl-flow")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatalf("canonical: %v", err)
	}
	contextRoot := t.TempDir()
	artifactRoot := t.TempDir()
	prepareProbeFixtures(t, canonical, "10000000-0000-0000-0000-00000000000a")
	criteria := []any{map[string]any{"id": "files", "observation": "fence exists", "kind": "file_assertion", "path": "README.md", "contains": "fixture"}}
	state := verifyState(t, project, project, baseline, "", criteria, profile)
	state["stage"] = "inspect"
	policyFiles, err := repository.StageHostPolicyInventory(project, deps.SkillsRoot, deps.HostPolicyPath)
	if err != nil {
		t.Fatalf("policy inventory: %v", err)
	}
	state["policy_files"] = policyFiles
	roots := map[string]string{"context": contextRoot, "artifact": artifactRoot, "cancel": filepath.Join(contextRoot, "cancel.signal")}
	input := providerInputDocument("measure", state, nil, roots, deps)
	data, err := repository.StageHostCanonical(input)
	if err != nil {
		t.Fatalf("input: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := RunProvider(context.Background(), deps, bytes.NewReader(data), &stdout, &stderr); code != 0 {
		t.Fatalf("measure failed: %s", stderr.String())
	}
	var observation repository.MeasureObservation
	if err := json.Unmarshal(stdout.Bytes(), &observation); err != nil {
		t.Fatalf("observation: %v: %s", err, stdout.String())
	}
	if !observation.RequestValid || observation.SourceManifest == nil || observation.Capability == nil {
		t.Fatalf("measure observation incomplete: valid=%v manifest=%v capability=%v\nblockers=%v\nraw=%s", observation.RequestValid, observation.SourceManifest, observation.Capability, observation.Blockers, stdout.String())
	}
	if len(observation.PolicyFiles) == 0 || observation.PolicyRules == nil || observation.Dependencies == nil {
		t.Fatalf("measure bindings incomplete: %v", observation)
	}
	if len(observation.Blockers) != 0 {
		t.Fatalf("unexpected blockers: %v", observation.Blockers)
	}
}

// prepareProbeFixtures writes the synthetic canonical probe tree the
// controller prepares before a measure (prepareNativeCapabilityProbe).
func prepareProbeFixtures(t *testing.T, canonical, taskID string) {
	t.Helper()
	root := filepath.Join(canonical, "native-provider-probe", taskID)
	for _, directory := range []string{"current", "revisions", "inputs"} {
		folder := filepath.Join(root, directory)
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatalf("probe fixtures: %v", err)
		}
		if err := os.WriteFile(filepath.Join(folder, "read.json"), []byte(`{"schema_version":1}`), 0o644); err != nil {
			t.Fatalf("probe fixtures: %v", err)
		}
		if err := os.WriteFile(filepath.Join(folder, "write.txt"), []byte("probe target"), 0o644); err != nil {
			t.Fatalf("probe fixtures: %v", err)
		}
	}
}

func TestProviderExecuteVerifyRoundTrip(t *testing.T) {
	project, baseline := stagehostGitRepository(t)
	worktree := stageWorktree(t, project, baseline)
	deps := testDeps(t)
	profile := managedProfileFixture(t, deps)
	attemptID := "20000000-0000-0000-0000-00000000000b"
	criteria := []any{
		map[string]any{"id": "files", "observation": "fence exists", "kind": "file_assertion", "path": "README.md", "contains": "fixture"},
		map[string]any{
			"id": "tests", "observation": "tests pass", "kind": "unit",
			"executable": deps.SelfPath, "arguments": []any{},
			"report": ".bsl-flow-worker/report.xml", "expected_tests": []any{"TestOne"},
			"retry_safe": true, "protected_paths": []any{"README.md"},
		},
	}
	state := verifyState(t, project, worktree, baseline, attemptID, criteria, profile)
	state["policy_files"] = stagePolicyInventory(t, project, deps.SkillsRoot, deps.HostPolicyPath)
	prepareProbeFixtures(t, filepath.Join(project, ".git", "bsl-flow"), asStringOr(state["task_id"]))
	roots := map[string]string{"context": t.TempDir(), "artifact": t.TempDir(), "cancel": filepath.Join(project, ".bsl-flow", "cancel.signal")}
	if err := os.MkdirAll(filepath.Dir(roots["cancel"]), 0o755); err != nil {
		t.Fatalf("cancel parent: %v", err)
	}
	attempt := testAttempt(t, state, "verify")
	input := providerInputDocument("execute", state, attempt, roots, deps)
	data, err := repository.StageHostCanonical(input)
	if err != nil {
		t.Fatalf("input: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := RunProvider(context.Background(), deps, bytes.NewReader(data), &stdout, &stderr); code != 0 {
		t.Fatalf("execute failed: %s", stderr.String())
	}
	var observation repository.ExecuteObservation
	if err := json.Unmarshal(stdout.Bytes(), &observation); err != nil {
		t.Fatalf("observation: %v: %s", err, stdout.String())
	}
	if observation.Status != "completed" || observation.Stage != "verify" || observation.SideEffects != "none" {
		t.Fatalf("unexpected observation: %+v", observation)
	}
	if observation.SourceManifest == nil || len(observation.Artifacts) == 0 {
		t.Fatalf("missing artifacts/source manifest: %+v", observation)
	}
	declared := map[string]bool{}
	for _, artifact := range observation.Artifacts {
		declared[artifact.Path] = true
	}
	for _, required := range []string{"raw/observations.json", "raw/tests/original.junit.xml", "raw/tests/process.json", "raw/tests/exit.json"} {
		if !declared[required] {
			t.Fatalf("missing declared artifact %s: %v", required, declared)
		}
	}
	receipts, _ := asArray(observation.ProcessReceipt["processes"])
	if len(receipts) < 2 {
		t.Fatalf("expected version+probe+criterion receipts, got %d", len(receipts))
	}
}
