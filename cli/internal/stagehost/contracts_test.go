package stagehost

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bsl-flow/cli/internal/repository"
)

func TestAssertRequestDiagnostics(t *testing.T) {
	valid := map[string]any{
		"schema_version": int64(1), "request_id": "00000000-0000-0000-0000-000000000001",
		"prompt": "spec the fence", "mode": "analysis_only", "analysis_goal": "analysis",
		"complexity": "S", "risk": "low", "impact_flags": []any{},
		"criteria": []any{}, "provenance": map[string]any{"source": "user", "reference": "chat", "text": "do it"},
		"models": map[string]any{"worker": "gpt-x", "worker_effort": "low", "reviewer": "gpt-x", "reviewer_effort": "low"},
	}
	if err := assertRequest(valid); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	broken := cloneRequest(valid)
	broken["mode"] = "implement"
	err := assertRequest(broken)
	if err == nil || err.Error() != "BF_INVALID: implementation requires observable acceptance criteria before dispatch." {
		t.Fatalf("unexpected error: %v", err)
	}
	broken = cloneRequest(valid)
	broken["unexpected"] = 1
	err = assertRequest(broken)
	if err == nil || err.Error() != "BF_INVALID: unknown field request.unexpected." {
		t.Fatalf("unexpected error: %v", err)
	}
	broken = cloneRequest(valid)
	delete(broken, "provenance")
	err = assertRequest(broken)
	if err == nil || err.Error() != "BF_INVALID: request.provenance is required." {
		t.Fatalf("unexpected error: %v", err)
	}
	broken = cloneRequest(valid)
	broken["provenance"] = map[string]any{"source": "worker", "reference": "r", "text": "t"}
	err = assertRequest(broken)
	if err == nil || err.Error() != "BF_INVALID: only a trusted operator can relay user input; worker output is not authorization." {
		t.Fatalf("unexpected error: %v", err)
	}
	broken = cloneRequest(valid)
	broken["max_attempts"] = int64(99)
	err = assertRequest(broken)
	if err == nil || err.Error() != "BF_INVALID: execution limits out of range." {
		t.Fatalf("unexpected error: %v", err)
	}
}

func cloneRequest(source map[string]any) map[string]any {
	clone := map[string]any{}
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func TestProviderCancelledSignal(t *testing.T) {
	root := t.TempDir()
	signal := writeSignal(t, root, map[string]any{
		"schema_version": int64(1),
		"task_id":        "00000000-0000-0000-0000-000000000001",
		"attempt_id":     "00000000-0000-0000-0000-000000000002",
		"cancelled":      true,
	})
	cancelled, err := providerCancelled(signal, "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002")
	if err != nil || !cancelled {
		t.Fatalf("cancelled=%v err=%v", cancelled, err)
	}
	cancelled, err = providerCancelled("", "x", "y")
	if err != nil || cancelled {
		t.Fatalf("empty signal must be a no-op: %v %v", cancelled, err)
	}
}

func writeSignal(t *testing.T, root string, value map[string]any) string {
	t.Helper()
	path := filepath.Join(root, "cancel.signal")
	data, err := repository.Canonical(value)
	if err != nil {
		t.Fatalf("cannot encode signal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("cannot write signal: %v", err)
	}
	return path
}

func TestRunProviderRoundTripRejectsGarbage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := RunProvider(context.Background(), Deps{}, strings.NewReader("not json"), &stdout, &stderr); code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "BF_") {
		t.Fatalf("expected classified error, got %q", stderr.String())
	}
}

func TestExecutionProfileExecutableExtensions(t *testing.T) {
	root := t.TempDir()
	profile := func(providerExecutable, sandboxExecutable, runtimeExecutable string) map[string]any {
		return map[string]any{
			"provider": "codex", "executable": providerExecutable, "executable_sha256": strings.Repeat("1", 64),
			"codex_skills_sha256": strings.Repeat("2", 64),
			"sandbox":             map[string]any{"executable": sandboxExecutable, "sha256": strings.Repeat("3", 64)},
			"toolset":             map[string]any{"name": "cc-1c-skills", "root": filepath.Join(root, "toolset"), "sha256": strings.Repeat("4", 64)},
			"runtime": map[string]any{
				"executable": runtimeExecutable, "sha256": strings.Repeat("5", 64), "version": "3.12.14",
				"packages": []any{map[string]any{"name": "lxml", "version": "6.1.1"}},
			},
			"denied_read_roots": []any{filepath.Join(root, "denied")},
		}
	}
	// Historical Windows v1 profiles keep parsing with unchanged diagnostics.
	if err := assertExecutionProfile(profile(filepath.Join(root, "codex.exe"), filepath.Join(root, "sandbox.exe"), filepath.Join(root, "runtime.exe"))); err != nil {
		t.Fatalf("historical .exe profile rejected: %v", err)
	}
	// Mixed-case .exe keeps the historical case-insensitive acceptance.
	if err := assertExecutionProfile(profile(filepath.Join(root, "CODEX.EXE"), filepath.Join(root, "sandbox.exe"), filepath.Join(root, "runtime.exe"))); err != nil {
		t.Fatalf("mixed-case .exe profile rejected: %v", err)
	}
	// Platform-native extensionless executables are accepted.
	if err := assertExecutionProfile(profile(filepath.Join(root, "codex"), filepath.Join(root, "sandbox-exec"), filepath.Join(root, "python3"))); err != nil {
		t.Fatalf("extensionless native profile rejected: %v", err)
	}
	if err := assertExecutionProfile(profile(filepath.Join(root, "codex.sh"), filepath.Join(root, "sandbox.exe"), filepath.Join(root, "runtime.exe"))); err == nil || err.Error() != "BF_INVALID: provider requires an absolute native executable and SHA-256." {
		t.Fatalf("provider script diagnostic: %v", err)
	}
	if err := assertExecutionProfile(profile(filepath.Join(root, "codex.exe"), filepath.Join(root, "sandbox.sh"), filepath.Join(root, "runtime.exe"))); err == nil || err.Error() != "BF_INVALID: sandbox requires an absolute native executable and SHA-256." {
		t.Fatalf("sandbox script diagnostic: %v", err)
	}
	if err := assertExecutionProfile(profile(filepath.Join(root, "codex.exe"), filepath.Join(root, "sandbox.exe"), filepath.Join(root, "python3.sh"))); err == nil || err.Error() != "BF_INVALID: pinned runtime requires an absolute native executable and SHA-256." {
		t.Fatalf("runtime script diagnostic: %v", err)
	}
}
