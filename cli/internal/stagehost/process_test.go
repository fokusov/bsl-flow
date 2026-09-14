package stagehost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bsl-flow/cli/internal/repository"
)

// TestMain doubles as the managed-process helper: a copy of this test binary
// re-executes itself with BF_STAGEHOST_PROCESS_HELPER set, so a real native
// executable exists under the mandatory .exe name on every platform. With
// BF_STAGEHOST_PROBE_HELPER set it mirrors the hidden `__fs-probe` CLI entry:
// exactly one JSON path document, non-zero exit otherwise.
func TestMain(m *testing.M) {
	switch os.Getenv("BF_STAGEHOST_PROCESS_HELPER") {
	case "echo":
		fmt.Print("stdin:" + readHelperStdin())
		return
	case "sleep":
		time.Sleep(60 * time.Second)
		return
	}
	if os.Getenv(probeHelperEnv) == "1" {
		if len(os.Args) != 2 {
			fmt.Fprintln(os.Stderr, "probe helper requires exactly one JSON path document")
			os.Exit(2)
		}
		if err := FSProbe(os.Args[1], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func readHelperStdin() string {
	data := make([]byte, 4096)
	read, _ := os.Stdin.Read(data)
	return string(data[:read])
}

func copyHelper(t *testing.T, directory, name string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot resolve test binary: %v", err)
	}
	target := filepath.Join(directory, name)
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatalf("cannot read test binary: %v", err)
	}
	if err := os.WriteFile(target, data, 0o755); err != nil {
		t.Fatalf("cannot copy helper: %v", err)
	}
	return target
}

func TestRunManagedProcessReceipt(t *testing.T) {
	root := t.TempDir()
	helper := copyHelper(t, root, "helper.exe")
	output := filepath.Join(root, "out")
	result, err := runManagedProcess(context.Background(), ProcessOptions{
		Executable:       helper,
		Arguments:        []string{"one", "two"},
		WorkingDirectory: root,
		InputText:        "hello helper",
		OutputDirectory:  output,
		TimeoutSeconds:   60,
		Environment:      map[string]string{"BF_STAGEHOST_PROCESS_HELPER": "echo"},
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if result.ExitCode != 0 || result.StopReason != "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	stdout, err := repository.StageHostReadFileBytes(result.Stdout)
	if err != nil || !strings.Contains(string(stdout), "stdin:hello helper") {
		t.Fatalf("stdin not delivered: %q err=%v", stdout, err)
	}
	identity, err := readJSONObject(filepath.Join(output, "process.json"))
	if err != nil {
		t.Fatalf("process.json: %v", err)
	}
	if _, err := assertFields(identity, []string{"pid", "start_time_utc", "executable", "arguments_sha256"}, nil, "process_identity"); err != nil {
		t.Fatalf("process identity shape: %v", err)
	}
	argumentsHash, err := hashValue([]any{"one", "two"})
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if asStringOr(identity["arguments_sha256"]) != argumentsHash {
		t.Fatalf("arguments_sha256 mismatch: %s", asStringOr(identity["arguments_sha256"]))
	}
	exit, err := readJSONObject(filepath.Join(output, "exit.json"))
	if err != nil {
		t.Fatalf("exit.json: %v", err)
	}
	if _, err := assertFields(exit, []string{"exit_code", "stop_reason", "elapsed_seconds", "process_id", "executable", "stdout", "stderr"}, nil, "process_exit"); err != nil {
		t.Fatalf("exit shape: %v", err)
	}
	if value, present := exit["stop_reason"]; !present || value != nil {
		t.Fatalf("stop_reason must be JSON null on success: %v", exit["stop_reason"])
	}
	if value, ok := asInteger(exit["exit_code"]); !ok || value != 0 {
		t.Fatalf("exit_code: %v", exit["exit_code"])
	}
}

func TestRunManagedProcessRejections(t *testing.T) {
	root := t.TempDir()
	helper := copyHelper(t, root, "helper.exe")
	if _, err := runManagedProcess(context.Background(), ProcessOptions{
		Executable: helper, Arguments: []string{"bad"}, WorkingDirectory: root,
		OutputDirectory: filepath.Join(root, "o1"), TimeoutSeconds: 10, MaxOutputBytes: 100,
	}); err == nil || err.Error() != "BF_INVALID: managed output bound is outside the supported range." {
		t.Fatalf("bound diagnostic: %v", err)
	}
	launcher := filepath.Join(root, "launcher")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if _, err := runManagedProcess(context.Background(), ProcessOptions{
		Executable: launcher, WorkingDirectory: root,
		OutputDirectory: filepath.Join(root, "o2"), TimeoutSeconds: 10,
	}); err == nil || err.Error() != "BF_BLOCKED: managed process launch requires an existing native .exe, not a shell launcher." {
		t.Fatalf("exe diagnostic: %v", err)
	}
	if _, err := runManagedProcess(context.Background(), ProcessOptions{
		Executable: filepath.Join(root, "missing.exe"), WorkingDirectory: root,
		OutputDirectory: filepath.Join(root, "o3"), TimeoutSeconds: 10,
	}); err == nil || !strings.HasPrefix(err.Error(), "BF_BLOCKED: managed process launch") {
		t.Fatalf("missing diagnostic: %v", err)
	}
	if _, err := runManagedProcess(context.Background(), ProcessOptions{
		Executable: helper, Arguments: []string{"a\x00b"}, WorkingDirectory: root,
		OutputDirectory: filepath.Join(root, "o4"), TimeoutSeconds: 10,
		Environment: map[string]string{"BF_STAGEHOST_PROCESS_HELPER": "echo"},
	}); err == nil || err.Error() != "BF_INVALID: NUL in native argument." {
		t.Fatalf("NUL diagnostic: %v", err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	if _, err := runManagedProcess(context.Background(), ProcessOptions{
		Executable: helper, WorkingDirectory: root,
		OutputDirectory: filepath.Join(root, "o5"), TimeoutSeconds: 10, Deadline: past,
		Environment: map[string]string{"BF_STAGEHOST_PROCESS_HELPER": "echo"},
	}); err == nil || err.Error() != "BF_BLOCKED: task deadline reached before dispatch." {
		t.Fatalf("deadline diagnostic: %v", err)
	}
	if _, err := runManagedProcess(context.Background(), ProcessOptions{
		Executable: helper, WorkingDirectory: root,
		OutputDirectory: filepath.Join(root, "o5"), TimeoutSeconds: 10,
		Cancelled:   func() (bool, error) { return true, nil },
		Environment: map[string]string{"BF_STAGEHOST_PROCESS_HELPER": "echo"},
	}); err == nil || err.Error() != "BF_BLOCKED: cancelled before process dispatch." {
		t.Fatalf("cancel diagnostic: %v", err)
	}
}

func TestRunManagedProcessTimeout(t *testing.T) {
	root := t.TempDir()
	helper := copyHelper(t, root, "helper.exe")
	output := filepath.Join(root, "out")
	result, err := runManagedProcess(context.Background(), ProcessOptions{
		Executable: helper, WorkingDirectory: root, OutputDirectory: output,
		TimeoutSeconds: 1, MaxOutputBytes: 16777216,
		Environment: map[string]string{"BF_STAGEHOST_PROCESS_HELPER": "sleep"},
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if result.StopReason != "timeout" {
		t.Fatalf("expected timeout stop reason, got %q", result.StopReason)
	}
}
