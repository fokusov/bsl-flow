package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Serve loop keeps its dispatcher behind a seam (loop_test.go injects a
// recording fake), so the serve journal carries no process receipts to walk.
// The runner's real spawn boundary is SelfDispatch, audited below with the
// same test-binary re-entry idiom the stagehost and worker packages use.

const dispatchHelperEnv = "BF_RUNNER_DISPATCH_HELPER"
const dispatchHelperRecordEnv = "BF_RUNNER_DISPATCH_HELPER_RECORD"

// TestMain doubles as the SelfDispatch child: the trusted dispatch executable
// is os.Executable() — here the test binary itself — so the re-executed copy
// records the exact argv it received and exits 0 instead of running tests.
func TestMain(m *testing.M) {
	if os.Getenv(dispatchHelperEnv) == "1" {
		record := map[string]any{"executable": os.Args[0], "argv": os.Args[1:]}
		encoded, err := json.Marshal(record)
		if err == nil {
			_ = os.WriteFile(os.Getenv(dispatchHelperRecordEnv), encoded, 0o600)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestSelfDispatchAudit audits the runner
// self-dispatch path of requirement 22: the spawned executable is the running
// trusted binary resolved through os.Executable(), the child argv is pure
// data, and neither the executable nor any recorded argument ever references
// PowerShell.
func TestSelfDispatchAudit(t *testing.T) {
	trusted, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(t.TempDir(), "dispatch-argv.json")
	t.Setenv(dispatchHelperEnv, "1")
	t.Setenv(dispatchHelperRecordEnv, recordPath)

	project := t.TempDir()
	request := DispatchRequest{Project: project, Task: task1, Action: ActionRun}
	dispatch := SelfDispatch{}
	var out, errOut strings.Builder
	if err := dispatch.Dispatch(context.Background(), request, &out, &errOut); err != nil {
		t.Fatalf("self dispatch: %v\nchild stderr: %s", err, errOut.String())
	}
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("dispatch child did not record its invocation: %v", err)
	}
	var record struct {
		Executable string   `json:"executable"`
		Argv       []string `json:"argv"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("dispatch record is invalid: %v", err)
	}
	if !strings.EqualFold(filepath.Clean(record.Executable), filepath.Clean(trusted)) {
		t.Fatalf("self dispatch spawned %q, want the trusted binary %q", record.Executable, trusted)
	}
	want := dispatchArguments(request)
	if strings.Join(record.Argv, "\n") != strings.Join(want, "\n") {
		t.Fatalf("self dispatch argv = %v, want %v", record.Argv, want)
	}
	for _, argument := range append(record.Argv, record.Executable) {
		lower := strings.ToLower(argument)
		if strings.Contains(lower, "pwsh") || strings.Contains(lower, "powershell") {
			t.Fatalf("self dispatch invocation references PowerShell: %q", argument)
		}
	}
}
