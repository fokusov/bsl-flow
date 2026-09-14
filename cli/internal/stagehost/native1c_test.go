package stagehost

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"bsl-flow/cli/internal/repository"
)

var native1CTestNow = time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)

// retryRemoveAll clears a fixture tree early with a short retry window: on
// Windows a virus scanner can transiently lock freshly written files, and the
// framework cleanup runs immediately after the test body.
func retryRemoveAll(t *testing.T, path string) {
	t.Helper()
	t.Cleanup(func() {
		for attempt := 0; attempt < 20; attempt++ {
			if err := os.RemoveAll(path); err == nil {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	})
}

// --- credential ---

func TestNative1CCredentialInputValidation(t *testing.T) {
	if _, err := assertNative1CCredential(nil); err != nil {
		t.Fatalf("nil credential: %v", err)
	}
	if _, err := assertNative1CCredential(map[string]any{"username": "user", "password": "pass"}); err != nil {
		t.Fatalf("valid credential: %v", err)
	}
	cases := []map[string]any{
		{"username": "user"},
		{"username": "   ", "password": "pass"},
		{"username": "user", "password": 42},
		{"username": strings.Repeat("u", 2000), "password": "pass"},
		{"username": "user", "password": strings.Repeat("p", 9000)},
		{"username": "user", "password": "pass", "extra": true},
	}
	for _, raw := range cases {
		if _, err := assertNative1CCredential(raw); err == nil {
			t.Fatalf("accepted invalid credential: %v", raw)
		}
	}
}

func TestNative1CSafeCredential(t *testing.T) {
	if err := native1cSafeCredential(native1cCredential{username: "user", password: "p w"}); err != nil {
		t.Fatalf("valid credential: %v", err)
	}
	for _, credential := range []native1cCredential{
		{username: "", password: "x"},
		{username: "  ", password: "x"},
		{username: "us\"er", password: "x"},
		{username: "user", password: "p\"x"},
		{username: "user", password: "p\x01x"},
	} {
		err := native1cSafeCredential(credential)
		if err == nil || !strings.Contains(err.Error(), "unsupported credential characters.") {
			t.Fatalf("unexpected error for %+v: %v", credential, err)
		}
	}
}

func TestNative1CPlatformBlockerTyped(t *testing.T) {
	err := native1cPlatformBlocker("linux")
	if err == nil || !strings.Contains(err.Error(), "BLOCKED_UNSUPPORTED_PLATFORM") {
		t.Fatalf("unexpected blocker: %v", err)
	}
	if err := native1cPlatformBlocker("windows"); err != nil {
		t.Fatalf("windows blocker: %v", err)
	}
}

// --- process dispatcher ---

type native1CProcessFixture struct {
	deps     Deps
	step     map[string]any
	captured *ProcessOptions
	logPath  string
	root     string
}

func newNative1CProcessFixture(t *testing.T, runner ProcessRunner) *native1CProcessFixture {
	t.Helper()
	root := t.TempDir()
	retryRemoveAll(t, root)
	logPath := filepath.Join(root, "load.out.log")
	fixture := &native1CProcessFixture{
		root: root,
		step: map[string]any{
			"name": "load",
			"log":  logPath,
			"argv": []any{"/F", "C:\\db", "/N", "<username>", "/P", "<password>"},
		},
		logPath: logPath,
	}
	fixture.deps = Deps{Now: func() time.Time { return native1CTestNow }, RunProcess: func(ctx context.Context, opts ProcessOptions) (ProcessResult, error) {
		if fixture.captured == nil {
			copy := opts
			fixture.captured = &copy
		}
		if runner != nil {
			return runner(ctx, opts)
		}
		if err := os.MkdirAll(opts.OutputDirectory, 0o755); err != nil {
			return ProcessResult{}, err
		}
		if err := writeJSON(filepath.Join(opts.OutputDirectory, "process.json"), map[string]any{
			"pid":            int64(4242),
			"start_time_utc": "2026-09-14T09:59:30.0000000Z",
		}, false); err != nil {
			return ProcessResult{}, err
		}
		if err := os.WriteFile(logPath, []byte("designer log"), 0o644); err != nil {
			return ProcessResult{}, err
		}
		return ProcessResult{ExitCode: 0, ProcessID: 4242}, nil
	}}
	return fixture
}

func TestNative1CProcessReceiptsAndSubstitution(t *testing.T) {
	fixture := newNative1CProcessFixture(t, nil)
	terminal, err := runNative1CProcess(context.Background(), fixture.deps, filepath.Join(t.TempDir(), "1cv8.exe"), fixture.step, native1cCredential{username: "user", password: "pa ss"}, fixture.root, 60, nil, "request-hash")
	if err != nil {
		t.Fatal(err)
	}
	if fixture.captured == nil {
		t.Fatal("runner was not invoked")
	}
	want := []string{"/F", "C:\\db", "/N", "user", "/P", "pa ss"}
	if len(fixture.captured.Arguments) != len(want) {
		t.Fatalf("arguments = %v", fixture.captured.Arguments)
	}
	for index, argument := range want {
		if fixture.captured.Arguments[index] != argument {
			t.Fatalf("argument %d = %q, want %q", index, fixture.captured.Arguments[index], argument)
		}
	}
	stepDir := filepath.Join(fixture.root, "steps", "load")
	for _, name := range []string{"prepared.json", "process.json", "started.json", "terminal.json"} {
		if !isRegularFile(filepath.Join(stepDir, name)) {
			t.Fatalf("missing receipt %s", name)
		}
	}
	prepared, err := readJSONObject(filepath.Join(stepDir, "prepared.json"))
	if err != nil {
		t.Fatal(err)
	}
	if asStringOr(prepared["request_sha256"]) != "request-hash" || prepared["step"] == nil {
		t.Fatalf("prepared = %v", prepared)
	}
	identity, err := readJSONObject(filepath.Join(stepDir, "process.json"))
	if err != nil {
		t.Fatal(err)
	}
	if identity["non_interruptible"] != true || asStringOr(identity["step"]) != "load" {
		t.Fatalf("identity = %v", identity)
	}
	if asStringOr(identity["pid"]) != "4242" {
		// json.Number canonical round-trip
		t.Logf("pid = %v", identity["pid"])
	}
	terminalDoc, err := readJSONObject(filepath.Join(stepDir, "terminal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if terminalDoc["log"] != fixture.logPath || terminalDoc["log_sha256"] == nil {
		t.Fatalf("terminal = %v", terminalDoc)
	}
	if terminalDoc["process"].(map[string]any)["request_sha256"] != "request-hash" {
		t.Fatalf("terminal process = %v", terminalDoc["process"])
	}
	if terminal["exit_code"] != int64(0) {
		t.Fatalf("terminal = %v", terminal)
	}
	// Managed receipts live beside the adapter receipts without collision.
	if !isRegularFile(filepath.Join(stepDir, "managed", "process.json")) {
		t.Fatal("managed process receipt missing")
	}
}

func TestNative1CProcessFailureSurfaces(t *testing.T) {
	// Stop reason timeout keeps the legacy message.
	fixture := newNative1CProcessFixture(t, func(ctx context.Context, opts ProcessOptions) (ProcessResult, error) {
		return ProcessResult{StopReason: "timeout", ExitCode: 1}, nil
	})
	_, err := runNative1CProcess(context.Background(), fixture.deps, "exe", fixture.step, native1cCredential{username: "u", password: "p"}, fixture.root, 60, nil, "h")
	if err == nil || !strings.Contains(err.Error(), "native process timed out and was not killed.") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Cancellation before dispatch.
	fixture = newNative1CProcessFixture(t, nil)
	cancelled := func() (bool, error) { return true, nil }
	_, err = runNative1CProcess(context.Background(), fixture.deps, "exe", fixture.step, native1cCredential{username: "u", password: "p"}, fixture.root, 60, cancelled, "h")
	if err == nil || !strings.Contains(err.Error(), "cancelled before native dispatch.") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Unsafe credential characters.
	fixture = newNative1CProcessFixture(t, nil)
	_, err = runNative1CProcess(context.Background(), fixture.deps, "exe", fixture.step, native1cCredential{username: "u", password: "p\"x"}, fixture.root, 60, nil, "h")
	if err == nil || !strings.Contains(err.Error(), "unsupported credential characters.") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Non-zero exit with a durable log still blocks the step.
	fixture = newNative1CProcessFixture(t, func(ctx context.Context, opts ProcessOptions) (ProcessResult, error) {
		if err := os.MkdirAll(opts.OutputDirectory, 0o755); err != nil {
			return ProcessResult{}, err
		}
		if err := writeJSON(filepath.Join(opts.OutputDirectory, "process.json"), map[string]any{
			"pid": int64(4242), "start_time_utc": "2026-09-14T09:59:30.0000000Z",
		}, false); err != nil {
			return ProcessResult{}, err
		}
		if err := os.WriteFile(fixture.logPath, []byte("designer log"), 0o644); err != nil {
			return ProcessResult{}, err
		}
		return ProcessResult{ExitCode: 7}, nil
	})
	_, err = runNative1CProcess(context.Background(), fixture.deps, "exe", fixture.step, native1cCredential{username: "u", password: "p"}, fixture.root, 60, nil, "h")
	if err == nil || !strings.Contains(err.Error(), "native step did not complete with a durable successful log.") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- verification orchestration ---

type native1CVerificationFixture struct {
	root       string
	worker     string
	target     string
	executable string
	state      map[string]any
	criterion  map[string]any
	checkDir   string
	journal    string
	report     []byte
	inventory  []map[string]any
	run        *stageRun
}

func newNative1CVerificationFixture(t *testing.T) *native1CVerificationFixture {
	t.Helper()
	project, baseline := stagehostGitRepository(t)
	worker := stageWorktree(t, project, baseline)
	root := t.TempDir()
	retryRemoveAll(t, root)
	target := filepath.Join(root, "target")
	executable := filepath.Join(root, "bin", "1cv8.exe")
	for _, dir := range []string{filepath.Join(worker, "src"), target, filepath.Dir(executable), filepath.Join(root, "journal")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	config := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses">
	<Configuration uuid="A1B2C3D4-0000-0000-0000-000000000001">
		<Properties><Name>Ext</Name><Version>1.2.3</Version></Properties>
	</Configuration>
</MetaDataObject>`
	if err := os.WriteFile(filepath.Join(worker, "src", "Configuration.xml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worker, "src", "Module.bsl"), []byte("module"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "1Cv8.1CD"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	exeBytes := []byte("fake-1cv8-platform")
	if err := os.WriteFile(executable, exeBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	taskID := "11111111-1111-4111-8111-111111111111"
	attemptID := "22222222-2222-4222-8222-222222222222"
	report := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<testsuite tests="1" failures="0" errors="0" skipped="0" disabled="0">
	<testcase classname="Tests.Module" name="Test"/>
</testsuite>`)
	criterion := map[string]any{
		"id": "native", "kind": "integration", "observation": "requires 1C",
		"executable":      executable,
		"arguments":       []any{},
		"protected_paths": []any{"tests"},
		"target":          target,
		"expected_tests":  []any{"Tests.Module.Test"},
		"native_1c": map[string]any{
			"source_root": "src", "extension": "Ext", "module": "Tests",
			"platform_version":      "8.3.25.1445",
			"executable_sha256":     repository.StageHostFileSHA256(exeBytes),
			"authorized_operations": []any{"inventory", "load", "update", "test"},
			"authorization_reference": "operator",
		},
	}
	fixture := &native1CVerificationFixture{
		root: root, worker: worker, target: target, executable: executable,
		checkDir: filepath.Join(root, "check"), journal: filepath.Join(root, "journal"),
		report:   report,
		state: map[string]any{
			"task_id": taskID, "active_attempt": attemptID,
			"project_path": project, "worker_path": worker,
			"baseline": baseline,
			"request": map[string]any{
				"criteria":        []any{criterion},
				"timeout_seconds": int64(60),
			},
			"evidence": []any{}, "events": []any{},
		},
		criterion: criterion,
		run: &stageRun{
			directory: filepath.Join(root, "check"),
			cancel:    func() (bool, error) { return false, nil },
		},
	}
	fixture.inventory = []map[string]any{
		{ // before: extension installed but inactive
			"schema_version": int64(1), "kind": "bsl-flow.native-inventory",
			"target":   target,
			"platform": map[string]any{"executable": executable},
			"extensions": []any{map[string]any{"properties": map[string]any{
				"name": "Ext", "version": "1.2.3", "active": false,
				"purpose": "Основная", "scope": "Configuration",
				"uuid": "a1b2c3d4-0000-0000-0000-000000000001", "hash_sum": "h",
			}}},
		},
		{ // loaded/after: extension active
			"schema_version": int64(1), "kind": "bsl-flow.native-inventory",
			"target":   target,
			"platform": map[string]any{"executable": executable},
			"extensions": []any{map[string]any{"properties": map[string]any{
				"name": "Ext", "version": "1.2.3", "active": true,
				"purpose": "Основная", "scope": "Configuration",
				"uuid": "a1b2c3d4-0000-0000-0000-000000000001", "hash_sum": "h",
			}}},
		},
	}
	return fixture
}

func (f *native1CVerificationFixture) deps(t *testing.T) Deps {
	t.Helper()
	invocation := 0
	return Deps{
		Now: func() time.Time { return native1CTestNow },
		Native1CJournalRoot: func(key string) (string, error) {
			return f.journal, nil
		},
		Native1CInventory: func(ctx context.Context, target, executable string, credential native1cCredential, directory string) (map[string]any, error) {
			index := invocation
			invocation++
			if index >= len(f.inventory) {
				index = len(f.inventory) - 1
			}
			if asStringOr(f.inventory[index]["target"]) != target {
				return nil, blockedf("unexpected inventory target")
			}
			if err := os.MkdirAll(directory, 0o755); err != nil {
				return nil, err
			}
			return f.inventory[index], nil
		},
		RunProcess: func(ctx context.Context, opts ProcessOptions) (ProcessResult, error) {
			if err := os.MkdirAll(opts.OutputDirectory, 0o755); err != nil {
				return ProcessResult{}, err
			}
			if err := writeJSON(filepath.Join(opts.OutputDirectory, "process.json"), map[string]any{
				"pid": int64(99), "start_time_utc": "2026-09-14T09:59:30.0000000Z",
			}, false); err != nil {
				return ProcessResult{}, err
			}
			// 1cv8 writes its own /Out log; locate the log path from argv.
			var logPath string
			for index, argument := range opts.Arguments {
				if argument == "/Out" && index+1 < len(opts.Arguments) {
					logPath = opts.Arguments[index+1]
				}
			}
			if logPath != "" {
				if err := os.WriteFile(logPath, []byte("step log"), 0o644); err != nil {
					return ProcessResult{}, err
				}
			}
			// The ENTERPRISE step consumes the test config and produces the
			// original JUnit report at the declared reportPath.
			if strings.HasPrefix(opts.Arguments[0], "ENTERPRISE") {
				config, err := readJSONObject(filepath.Join(opts.WorkingDirectory, "test-config.json"))
				if err != nil {
					return ProcessResult{}, err
				}
				reportPath := asStringOr(config["reportPath"])
				if err := os.WriteFile(reportPath, f.report, 0o644); err != nil {
					return ProcessResult{}, err
				}
				if err := os.Chtimes(reportPath, native1CTestNow, native1CTestNow); err != nil {
					return ProcessResult{}, err
				}
				if err := os.WriteFile(asStringOr(asMap(config["logging"])["file"]), []byte("runner"), 0o644); err != nil {
					return ProcessResult{}, err
				}
			}
			return ProcessResult{ExitCode: 0, ProcessID: 99}, nil
		},
	}
}

func TestNative1CVerificationEndToEnd(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native 1C runtime is a windows-only capability")
	}
	fixture := newNative1CVerificationFixture(t)
	observation, err := runNativeVerification(context.Background(), fixture.deps(t), fixture.state, fixture.criterion, fixture.checkDir, &native1cCredential{username: "user", password: "pass"}, fixture.run)
	if err != nil {
		t.Fatal(err)
	}
	if asStringOr(observation["outcome"]) != "PASS" || asStringOr(observation["criterion_id"]) != "native" {
		t.Fatalf("observation = %v", observation)
	}
	tests, _ := asArray(observation["tests"])
	if len(tests) != 1 || asStringOr(tests[0]) != "Tests.Module.Test" {
		t.Fatalf("tests = %v", tests)
	}
	runtime := asMap(observation["runtime"])
	if asStringOr(runtime["extension"]) != "Ext" || asStringOr(runtime["version"]) != "1.2.3" {
		t.Fatalf("runtime = %v", runtime)
	}
	for _, name := range []string{"inventory-before.json", "inventory-loaded.json", "inventory-after.json", "runtime-request.json", "runtime-success.json", "test-config.json", "original.junit.xml", "source-snapshot"} {
		path := filepath.Join(fixture.checkDir, name)
		if name == "source-snapshot" {
			if !isDirectory(path) {
				t.Fatalf("missing %s", name)
			}
			continue
		}
		if !isRegularFile(path) {
			t.Fatalf("missing %s", name)
		}
	}
	for _, step := range []string{"load", "update", "test"} {
		if !isRegularFile(filepath.Join(fixture.checkDir, "steps", step, "terminal.json")) {
			t.Fatalf("missing terminal receipt for %s", step)
		}
	}
	attemptID := asStringOr(fixture.state["active_attempt"])
	if !isRegularFile(filepath.Join(fixture.journal, "history", attemptID+".success.json")) {
		t.Fatal("journal history receipt missing")
	}
	if fileExists(filepath.Join(fixture.journal, "pending.json")) {
		t.Fatal("pending latch was not cleared")
	}
	if !isRegularFile(filepath.Join(fixture.checkDir, "runtime-success.json")) {
		t.Fatal("runtime-success.json missing")
	}
	success, err := readJSONObject(filepath.Join(fixture.checkDir, "runtime-success.json"))
	if err != nil {
		t.Fatal(err)
	}
	if asStringOr(success["kind"]) != "bsl-flow.native-1c-success" || success["request_sha256"] == nil {
		t.Fatalf("success = %v", success)
	}
}

func TestNative1CVerificationInventoryTransitionFailure(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native 1C runtime is a windows-only capability")
	}
	fixture := newNative1CVerificationFixture(t)
	// The after-inventory differs from the loaded one: the transition gate
	// must block and the sanitized failure receipt must exist.
	after := map[string]any{}
	for key, value := range fixture.inventory[len(fixture.inventory)-1] {
		after[key] = value
	}
	after["extensions"] = []any{map[string]any{"properties": map[string]any{
		"name": "Ext", "version": "9.9", "active": true,
		"purpose": "Основная", "scope": "Configuration",
		"uuid": "a1b2c3d4-0000-0000-0000-000000000001", "hash_sum": "h",
	}}}
	fixture.inventory = append(fixture.inventory, after)
	_, err := runNativeVerification(context.Background(), fixture.deps(t), fixture.state, fixture.criterion, fixture.checkDir, &native1cCredential{username: "user", password: "pass"}, fixture.run)
	if err == nil {
		t.Fatal("changed inventory was accepted")
	}
	if !strings.Contains(err.Error(), "installed extension version or active state differs from the authorized source XML.") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isRegularFile(filepath.Join(fixture.checkDir, "runtime-failure.json")) {
		t.Fatal("runtime-failure.json missing")
	}
	if !fileExists(filepath.Join(fixture.journal, "pending.json")) {
		t.Fatal("pending latch missing after an uncertain native failure")
	}
}

func TestNative1CVerificationRequiresCredential(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native 1C runtime is a windows-only capability")
	}
	fixture := newNative1CVerificationFixture(t)
	_, err := runNativeVerification(context.Background(), fixture.deps(t), fixture.state, fixture.criterion, fixture.checkDir, nil, fixture.run)
	if err == nil || !strings.Contains(err.Error(), "native credential must be supplied through private controller input for this process.") {
		t.Fatalf("unexpected error: %v", err)
	}
	if fileExists(filepath.Join(fixture.journal, "pending.json")) {
		t.Fatal("pending latch written without credential")
	}
}
