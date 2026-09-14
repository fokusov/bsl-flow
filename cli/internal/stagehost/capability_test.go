package stagehost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const probeHelperEnv = "BF_STAGEHOST_PROBE_HELPER"

func capabilityFixture(t *testing.T) (map[string]any, string, string, string) {
	t.Helper()
	root := t.TempDir()
	project := filepath.Join(root, "project")
	worker := filepath.Join(project, "worktree")
	canonical := filepath.Join(root, "store", "bsl-flow")
	toolsetRoot := filepath.Join(root, "toolset")
	denied := filepath.Join(root, "denied")
	for _, path := range []string{project, worker, filepath.Dir(canonical), canonical, toolsetRoot, denied} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	// Common-dir siblings the gitRoots enumeration picks up.
	for _, name := range []string{"HEAD", "config", "index", "packed-refs", "commondir", "description"} {
		if err := os.WriteFile(filepath.Join(filepath.Dir(canonical), name), []byte("x"), 0o644); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	sandbox := copyHelper(t, root, "fake-sandbox.exe")
	provider := copyHelper(t, root, "fake-codex.exe")
	profile := map[string]any{
		"provider":            "codex",
		"executable":          provider,
		"executable_sha256":   strings.Repeat("1", 64),
		"sandbox":             map[string]any{"executable": sandbox, "sha256": strings.Repeat("2", 64)},
		"toolset":             map[string]any{"name": "cc-1c-skills", "root": toolsetRoot, "sha256": strings.Repeat("3", 64)},
		"denied_read_roots":   []any{denied},
		"codex_skills_sha256": strings.Repeat("4", 64),
		"runtime": map[string]any{
			"executable": provider, "sha256": strings.Repeat("5", 64), "version": "3.12.0",
			"packages": []any{map[string]any{"name": "lxml", "version": "5.0.0"}},
		},
	}
	state := map[string]any{
		"task_id":              "00000000-0000-0000-0000-000000000001",
		"project_path":         project,
		"worker_path":          worker,
		"baseline":             "baseline",
		"canonical_store_root": canonical,
		"request":              map[string]any{"execution_profile": profile},
	}
	return state, project, worker, canonical
}

func TestPermissionProfileString(t *testing.T) {
	state, project, worker, canonical := capabilityFixture(t)
	scratch := filepath.Join(t.TempDir(), "scratch")
	config := filepath.Join(t.TempDir(), "config")
	profile, err := permissionProfile(state, scratch, config, false, canonical)
	if err != nil {
		t.Fatalf("permissionProfile: %v", err)
	}
	prefix := `permissions.bsl_execution={filesystem={":root"="read",` +
		jsonQuotedKey(filepath.Join(project, ".bsl-flow", "tasks")) + `="none",` +
		jsonQuotedKey(canonical) + `="none",` +
		jsonQuotedKey(worker) + `="read",`
	if !strings.HasPrefix(profile, prefix) {
		t.Fatalf("profile prefix mismatch:\n%s", profile)
	}
	if !strings.Contains(profile, jsonQuotedKey(filepath.Join(filepath.Dir(canonical), "HEAD"))) {
		t.Fatalf("git root entries missing:\n%s", profile)
	}
	if !strings.HasSuffix(profile, `},network={enabled=true}}`) {
		t.Fatalf("profile suffix mismatch:\n%s", profile)
	}
	if !strings.Contains(profile, jsonQuotedKey(scratch)+`="write"`) {
		t.Fatalf("scratch must stay writable in a read-only worker profile:\n%s", profile)
	}
}

func jsonQuotedKey(path string) string {
	data, _ := json.Marshal(strings.ReplaceAll(path, `\`, "/"))
	return string(data)
}

func TestCanonicalProbePathsFixture(t *testing.T) {
	_, _, _, canonical := capabilityFixture(t)
	taskID := "00000000-0000-0000-0000-000000000001"
	// No real canonical task files: the synthetic probe tree must be used.
	for _, name := range []string{
		filepath.Join(canonical, "native-provider-probe", taskID, "current", "read.json"),
		filepath.Join(canonical, "native-provider-probe", taskID, "revisions", "read.json"),
		filepath.Join(canonical, "native-provider-probe", taskID, "inputs", "read.json"),
		filepath.Join(canonical, "native-provider-probe", taskID, "current", "write.txt"),
		filepath.Join(canonical, "native-provider-probe", taskID, "revisions", "write.txt"),
		filepath.Join(canonical, "native-provider-probe", taskID, "inputs", "write.txt"),
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatalf("fixture: %v", err)
		}
		if err := os.WriteFile(name, []byte("{}"), 0o644); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	state := map[string]any{"task_id": taskID}
	paths, err := canonicalProbePaths(state, canonical)
	if err != nil {
		t.Fatalf("canonicalProbePaths: %v", err)
	}
	if paths["current_read"] != filepath.Join(canonical, "native-provider-probe", taskID, "current", "read.json") {
		t.Fatalf("current_read: %v", paths["current_read"])
	}
	if _, err := canonicalProbePaths(map[string]any{"task_id": taskID}, filepath.Join(t.TempDir(), "empty")); err == nil {
		t.Fatalf("missing probe fixtures must be rejected")
	}
}

func fakeProcessRunner(t *testing.T, selfPath string) func(context.Context, ProcessOptions) (ProcessResult, error) {
	return func(ctx context.Context, opts ProcessOptions) (ProcessResult, error) {
		if err := os.MkdirAll(opts.OutputDirectory, 0o755); err != nil {
			return ProcessResult{}, err
		}
		stdoutPath := filepath.Join(opts.OutputDirectory, "stdout.txt")
		stderrPath := filepath.Join(opts.OutputDirectory, "stderr.txt")
		stdoutText := ""
		switch {
		case len(opts.Arguments) == 1 && opts.Arguments[0] == "--version":
			stdoutText = "codex-cli 0.154.0"
		case len(opts.Arguments) > 2 && opts.Arguments[len(opts.Arguments)-2] == "__fs-probe":
			if err := FSProbe(opts.Arguments[len(opts.Arguments)-1], &stdoutWriter{path: stdoutPath}); err != nil {
				return ProcessResult{ExitCode: 1, StopReason: "probe"}, nil
			}
		default:
			return ProcessResult{ExitCode: 1, StopReason: "exit"}, nil
		}
		if err := os.WriteFile(stdoutPath, []byte(stdoutText), 0o644); err != nil {
			return ProcessResult{}, err
		}
		if err := os.WriteFile(stderrPath, nil, 0o644); err != nil {
			return ProcessResult{}, err
		}
		return ProcessResult{ExitCode: 0, ProcessID: 1, Executable: opts.Executable, Stdout: stdoutPath, Stderr: stderrPath}, nil
	}
}

type stdoutWriter struct{ path string }

func (w *stdoutWriter) Write(p []byte) (int, error) {
	if err := os.WriteFile(w.path, p, 0o644); err != nil {
		return 0, err
	}
	return len(p), nil
}

func TestFSProbeClassification(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "read.json")
	if err := os.WriteFile(file, []byte("{}"), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	paths := map[string]string{"config_read": file, "config_write": filepath.Join(root, "sentinel.txt"), "scratch_write": filepath.Join(root, "scratch.txt")}
	data, err := json.Marshal(paths)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out strings.Builder
	if err := FSProbe(string(data), &out); err != nil {
		t.Fatalf("FSProbe: %v", err)
	}
	var observed map[string]any
	if err := json.Unmarshal([]byte(out.String()), &observed); err != nil {
		t.Fatalf("output: %v", err)
	}
	if observed["config_read"] != "allowed" || observed["config_write"] != "allowed" {
		t.Fatalf("unexpected observations: %v", observed)
	}
}

func TestRunManagedProcessExecutesProbeHelper(t *testing.T) {
	root := t.TempDir()
	helper := copyHelper(t, root, "helper.exe")
	output := filepath.Join(root, "out")
	// The helper binary in probe mode calls FSProbe with the JSON argument.
	result, err := runManagedProcess(context.Background(), ProcessOptions{
		Executable: helper, Arguments: []string{}, WorkingDirectory: root,
		OutputDirectory: output, TimeoutSeconds: 30,
		Environment: map[string]string{probeHelperEnv: "1"},
	})
	if err != nil {
		t.Fatalf("managed dispatch must surface a helper failure as a receipt: %v", err)
	}
	if result.ExitCode == 0 && result.StopReason == "" {
		t.Fatalf("probe helper without arguments must fail, got %+v", result)
	}
	file := filepath.Join(root, "read.json")
	if err := os.WriteFile(file, []byte("{}"), 0o644); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	paths := map[string]string{"config_read": file, "config_write": filepath.Join(root, "sentinel.txt")}
	data, err := json.Marshal(paths)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	result, err = runManagedProcess(context.Background(), ProcessOptions{
		Executable: helper, Arguments: []string{string(data)}, WorkingDirectory: root,
		OutputDirectory: filepath.Join(root, "out-ok"), TimeoutSeconds: 30,
		Environment: map[string]string{probeHelperEnv: "1"},
	})
	if err != nil {
		t.Fatalf("probe dispatch: %v", err)
	}
	if result.ExitCode != 0 || result.StopReason != "" {
		t.Fatalf("probe helper with a JSON argument must succeed, got %+v", result)
	}
	stdout, err := os.ReadFile(result.Stdout)
	if err != nil {
		t.Fatalf("read probe stdout: %v", err)
	}
	var observed map[string]any
	if err := json.Unmarshal(stdout, &observed); err != nil {
		t.Fatalf("probe output: %v", err)
	}
	if observed["config_read"] != "allowed" || observed["config_write"] != "allowed" {
		t.Fatalf("unexpected probe observations: %v", observed)
	}
}
