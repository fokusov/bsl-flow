package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain doubles as the fake provider helper: a copy of this test binary
// re-executes itself as the native .exe the managed adapters spawn. The
// helper self-identifies from its argv (the managed dispatch scrubs the
// environment) and reads its script from bf-worker-fake.json in the working
// directory, mirroring the recorded codex exec --json, app-server JSON-RPC
// and opencode run --format json output shapes.
func TestMain(m *testing.M) {
	args := os.Args[1:]
	mode := os.Getenv("BF_WORKER_HELPER")
	if mode == "" {
		switch {
		case containsArgument(args, "--version"):
			mode = "version"
		case containsArgument(args, "app-server"):
			mode = "codex-rpc"
		case len(args) > 0 && args[0] == "exec":
			mode = "codex-exec"
		case containsArgument(args, "--pure"):
			mode = "opencode-run"
		}
	}
	switch mode {
	case "echo":
		fmt.Print("stdin:" + readHelperStdin())
		return
	case "sleep":
		time.Sleep(60 * time.Second)
		return
	case "version":
		fmt.Println("codex-cli 0.154.0")
		return
	case "codex-rpc":
		codexRPCHelper(args)
		return
	case "codex-exec":
		codexExecHelper(args)
		return
	case "opencode-run":
		opencodeRunHelper(args)
		return
	}
	code := m.Run()
	sharedWorkerHelperCleanup()
	os.Exit(code)
}

// sharedWorkerHelper caches one copy of the test binary: the first launch of
// a freshly written .exe pays a one-time scanner cost, while every later
// launch of the same file starts in milliseconds.
var (
	sharedWorkerHelperMu   sync.Mutex
	sharedWorkerHelperPath string
	sharedWorkerHelperRoot string
)

func sharedWorkerHelperCleanup() {
	if sharedWorkerHelperRoot != "" {
		_ = os.RemoveAll(sharedWorkerHelperRoot)
	}
}

func workerHelper(t *testing.T) string {
	t.Helper()
	sharedWorkerHelperMu.Lock()
	defer sharedWorkerHelperMu.Unlock()
	if sharedWorkerHelperPath != "" {
		return sharedWorkerHelperPath
	}
	root, err := os.MkdirTemp("", "bsl-flow-worker-helper-")
	if err != nil {
		t.Fatalf("helper root: %v", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot resolve test binary: %v", err)
	}
	target := filepath.Join(root, "worker-helper.exe")
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatalf("cannot read test binary: %v", err)
	}
	if err := os.WriteFile(target, data, 0o755); err != nil {
		t.Fatalf("cannot copy helper: %v", err)
	}
	sharedWorkerHelperRoot = root
	sharedWorkerHelperPath = target
	return target
}

// fakeConfig is the bf-worker-fake.json script of the provider helper.
type fakeConfig struct {
	Session      string          `json:"session"`
	Result       string          `json:"result"`
	FailExit     int             `json:"fail_exit"`
	SleepSeconds float64         `json:"sleep_seconds"`
	Skills       [][]string      `json:"skills"`
	McpTools     []string        `json:"mcp_tools"`
	McpServers   map[string]bool `json:"mcp_servers"`
}

func readFakeConfig() fakeConfig {
	config := fakeConfig{
		Session: "22222222-2222-4222-8222-222222222222",
		Result:  `{"schema_version":1,"status":"completed","summary":"fixture worker result","payload_json":"{}"}`,
	}
	data, err := os.ReadFile(filepath.Join(".", "bf-worker-fake.json"))
	if err != nil {
		return config
	}
	_ = json.Unmarshal(data, &config)
	if strings.TrimSpace(config.Session) == "" {
		config.Session = "22222222-2222-4222-8222-222222222222"
	}
	if strings.TrimSpace(config.Result) == "" {
		config.Result = `{"schema_version":1,"status":"completed","summary":"fixture worker result","payload_json":"{}"}`
	}
	return config
}

func readHelperStdin() string {
	data, _ := io.ReadAll(os.Stdin)
	return string(data)
}

func containsArgument(arguments []string, needle string) bool {
	for _, argument := range arguments {
		if argument == needle {
			return true
		}
	}
	return false
}

// codexExecHelper emits the recorded codex exec --json stream and writes the
// model result to the --output-last-message artifact.
func codexExecHelper(args []string) {
	config := readFakeConfig()
	_ = readHelperStdin()
	if config.SleepSeconds > 0 {
		time.Sleep(time.Duration(config.SleepSeconds * float64(time.Second)))
		return
	}
	lastMessage := ""
	for index, argument := range args {
		if argument == "--output-last-message" && index+1 < len(args) {
			lastMessage = args[index+1]
		}
	}
	if lastMessage != "" {
		_ = os.WriteFile(lastMessage, []byte(config.Result), 0o644)
	}
	emit := func(line string) { fmt.Println(line) }
	emit(`{"type":"thread.started","thread_id":"` + config.Session + `"}`)
	emit(`{"type":"turn.started"}`)
	emit(`{"type":"item.started","item":{"id":"reason-1","type":"reasoning"}}`)
	emit(`{"type":"item.completed","item":{"id":"reason-1","type":"reasoning"}}`)
	emit(`{"type":"item.completed","item":{"id":"answer","type":"agent_message","text":` + jsonString(config.Result) + `}}`)
	emit(`{"type":"turn.completed","usage":{"input_tokens":4,"cached_input_tokens":0,"output_tokens":2,"cache_write_input_tokens":0,"reasoning_output_tokens":1}}`)
	if config.FailExit != 0 {
		os.Exit(config.FailExit)
	}
	os.Exit(0)
}

// codexRPCHelper speaks the app-server JSON-RPC handshake the read-only
// client drives: initialize, then config/read / skills/list /
// mcpServerStatus/list requests.
func codexRPCHelper(args []string) {
	config := readFakeConfig()
	denied := false
	for _, argument := range args {
		if strings.HasPrefix(argument, "skills.config=") {
			denied = strings.Contains(argument, "enabled=false")
		}
	}
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	respond := func(id json.RawMessage, result any) {
		payload, err := json.Marshal(map[string]any{"id": id, "result": result})
		if err != nil {
			return
		}
		_, _ = writer.Write(payload)
		_ = writer.WriteByte('\n')
		_ = writer.Flush()
	}
	for {
		line, err := reader.ReadString('\n')
		if strings.TrimSpace(line) == "" {
			if err != nil {
				return
			}
			continue
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if json.Unmarshal([]byte(line), &request) != nil {
			if err != nil {
				return
			}
			continue
		}
		switch request.Method {
		case "initialize":
			respond(request.ID, map[string]any{"ok": true})
		case "initialized":
			// notification: no response
		case "config/read":
			servers := map[string]any{}
			for name, enabled := range config.McpServers {
				servers[name] = map[string]any{"enabled": enabled}
			}
			respond(request.ID, map[string]any{"config": map[string]any{"mcp_servers": servers}})
		case "skills/list":
			cwd := ""
			if cwds, ok := request.Params["cwds"].([]any); ok && len(cwds) > 0 {
				cwd, _ = cwds[0].(string)
			}
			skills := make([]any, 0, len(config.Skills))
			for _, skill := range config.Skills {
				if len(skill) < 2 {
					continue
				}
				skills = append(skills, map[string]any{
					"name":    skill[0],
					"path":    skill[1],
					"scope":   "project",
					"enabled": !denied,
				})
			}
			respond(request.ID, map[string]any{"data": []any{map[string]any{
				"cwd":    cwd,
				"errors": []any{},
				"skills": skills,
			}}})
		case "mcpServerStatus/list":
			data := []any{}
			if len(config.McpTools) > 0 {
				tools := map[string]any{}
				for _, tool := range config.McpTools {
					tools[tool] = map[string]any{"name": tool}
				}
				data = append(data, map[string]any{"name": "unica", "tools": tools})
			}
			respond(request.ID, map[string]any{"data": data})
		}
		if err != nil {
			return
		}
	}
}

// opencodeRunHelper emits the recorded opencode run --format json step/part
// stream: one tool step, then one terminal text step.
func opencodeRunHelper(args []string) {
	config := readFakeConfig()
	_ = readHelperStdin()
	if config.SleepSeconds > 0 {
		time.Sleep(time.Duration(config.SleepSeconds * float64(time.Second)))
		return
	}
	session := "ses_" + "fixture0123"
	emit := func(line string) { fmt.Println(line) }
	emit(`{"type":"step_start","timestamp":1,"sessionID":"` + session + `","part":{"type":"step-start","id":"prt_0001","sessionID":"` + session + `","messageID":"msg_0001"}}`)
	emit(`{"type":"tool_use","timestamp":2,"sessionID":"` + session + `","part":{"type":"tool","id":"prt_0002","sessionID":"` + session + `","messageID":"msg_0001","tool":"read","callID":"call_0001","state":{"status":"completed"}}}`)
	emit(`{"type":"step_finish","timestamp":3,"sessionID":"` + session + `","part":{"type":"step-finish","id":"prt_0003","sessionID":"` + session + `","messageID":"msg_0001","reason":"tool-calls","tokens":{"total":12,"input":8,"output":4,"reasoning":0,"cache":{"read":2,"write":1}},"cost":0.01}}`)
	emit(`{"type":"step_start","timestamp":4,"sessionID":"` + session + `","part":{"type":"step-start","id":"prt_0004","sessionID":"` + session + `","messageID":"msg_0002"}}`)
	emit(`{"type":"text","timestamp":5,"sessionID":"` + session + `","part":{"type":"text","id":"prt_0005","sessionID":"` + session + `","messageID":"msg_0002","text":` + jsonString(config.Result) + `}}`)
	emit(`{"type":"step_finish","timestamp":6,"sessionID":"` + session + `","part":{"type":"step-finish","id":"prt_0006","sessionID":"` + session + `","messageID":"msg_0002","reason":"stop","tokens":{"total":10,"input":6,"output":4,"reasoning":1,"cache":{"read":0,"write":0}},"cost":0.02}}`)
	if config.FailExit != 0 {
		os.Exit(config.FailExit)
	}
	os.Exit(0)
}

func jsonString(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func writeFakeConfig(t *testing.T, workerPath string, config fakeConfig) {
	t.Helper()
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("fake config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workerPath, "bf-worker-fake.json"), data, 0o644); err != nil {
		t.Fatalf("fake config: %v", err)
	}
}

func TestRunManagedProcessReceipt(t *testing.T) {
	root := t.TempDir()
	helper := workerHelper(t)
	output := filepath.Join(root, "out")
	result, err := RunManagedProcess(context.Background(), ProcessOptions{
		Executable:       helper,
		Arguments:        []string{"one", "two"},
		WorkingDirectory: root,
		InputText:        "hello helper",
		OutputDirectory:  output,
		TimeoutSeconds:   60,
		Environment:      map[string]string{"BF_WORKER_HELPER": "echo"},
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if result.ExitCode != 0 || result.StopReason != "" {
		t.Fatalf("unexpected result: %+v", result)
	}
	stdout, err := os.ReadFile(result.Stdout)
	if err != nil || !strings.Contains(string(stdout), "stdin:hello helper") {
		t.Fatalf("stdin not delivered: %q err=%v", stdout, err)
	}
	identity, err := readJSONObjectFile(filepath.Join(output, "process.json"))
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
	if value, _ := asString(identity["arguments_sha256"]); value != argumentsHash {
		t.Fatalf("arguments_sha256 mismatch: %v", identity["arguments_sha256"])
	}
	exit, err := readJSONObjectFile(filepath.Join(output, "exit.json"))
	if err != nil {
		t.Fatalf("exit.json: %v", err)
	}
	if value, present := exit["stop_reason"]; !present || value != nil {
		t.Fatalf("stop_reason must be JSON null on success: %v", exit["stop_reason"])
	}
	if value, ok := asInt64(exit["exit_code"]); !ok || value != 0 {
		t.Fatalf("exit_code: %v", exit["exit_code"])
	}
}

func TestRunManagedProcessRejections(t *testing.T) {
	root := t.TempDir()
	helper := workerHelper(t)
	if _, err := RunManagedProcess(context.Background(), ProcessOptions{
		Executable: helper, Arguments: []string{"bad"}, WorkingDirectory: root,
		OutputDirectory: filepath.Join(root, "o1"), TimeoutSeconds: 10, MaxOutputBytes: 100,
	}); err == nil || err.Error() != "BF_INVALID: managed output bound is outside the supported range." {
		t.Fatalf("bound diagnostic: %v", err)
	}
	launcher := filepath.Join(root, "launcher")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if _, err := RunManagedProcess(context.Background(), ProcessOptions{
		Executable: launcher, WorkingDirectory: root,
		OutputDirectory: filepath.Join(root, "o2"), TimeoutSeconds: 10,
	}); err == nil || err.Error() != "BF_BLOCKED: managed process launch requires an existing native .exe, not a shell launcher." {
		t.Fatalf("launcher diagnostic: %v", err)
	}
	if _, err := RunManagedProcess(context.Background(), ProcessOptions{
		Executable: helper, Arguments: []string{"a\x00b"}, WorkingDirectory: root,
		OutputDirectory: filepath.Join(root, "o3"), TimeoutSeconds: 10,
		Environment: map[string]string{"BF_WORKER_HELPER": "echo"},
	}); err == nil || err.Error() != "BF_INVALID: NUL in native argument." {
		t.Fatalf("NUL diagnostic: %v", err)
	}
	past := time.Now().UTC().Add(-time.Minute)
	if _, err := RunManagedProcess(context.Background(), ProcessOptions{
		Executable: helper, WorkingDirectory: root,
		OutputDirectory: filepath.Join(root, "o4"), TimeoutSeconds: 10, Deadline: past,
		Environment: map[string]string{"BF_WORKER_HELPER": "echo"},
	}); err == nil || err.Error() != "BF_BLOCKED: task deadline reached before dispatch." {
		t.Fatalf("deadline diagnostic: %v", err)
	}
	if _, err := RunManagedProcess(context.Background(), ProcessOptions{
		Executable: helper, WorkingDirectory: root,
		OutputDirectory: filepath.Join(root, "o5"), TimeoutSeconds: 10,
		Cancelled:   func() (bool, error) { return true, nil },
		Environment: map[string]string{"BF_WORKER_HELPER": "echo"},
	}); err == nil || err.Error() != "BF_BLOCKED: cancelled before process dispatch." {
		t.Fatalf("cancel diagnostic: %v", err)
	}
}

func TestRunManagedProcessTimeoutAndContextKill(t *testing.T) {
	root := t.TempDir()
	helper := workerHelper(t)
	result, err := RunManagedProcess(context.Background(), ProcessOptions{
		Executable: helper, WorkingDirectory: root,
		OutputDirectory: filepath.Join(root, "timeout"),
		TimeoutSeconds:  1, MaxOutputBytes: 16777216,
		Environment: map[string]string{"BF_WORKER_HELPER": "sleep"},
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if result.StopReason != "timeout" {
		t.Fatalf("expected timeout stop reason, got %q", result.StopReason)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunManagedProcess(ctx, ProcessOptions{
		Executable: helper, WorkingDirectory: root,
		OutputDirectory: filepath.Join(root, "cancelled-before"),
		TimeoutSeconds:  10,
		Environment:     map[string]string{"BF_WORKER_HELPER": "echo"},
	}); err == nil || err.Error() != "BF_BLOCKED: cancelled before process dispatch." {
		t.Fatalf("pre-cancel diagnostic: %v", err)
	}
	liveContext, liveCancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond)
		liveCancel()
	}()
	started := time.Now()
	result, err = RunManagedProcess(liveContext, ProcessOptions{
		Executable: helper, WorkingDirectory: root,
		OutputDirectory: filepath.Join(root, "cancelled"),
		TimeoutSeconds:  30, MaxOutputBytes: 16777216,
		Environment: map[string]string{"BF_WORKER_HELPER": "sleep"},
	})
	if err != nil {
		t.Fatalf("cancelled run failed: %v", err)
	}
	if result.StopReason != "cancelled" {
		t.Fatalf("expected cancelled stop reason, got %q", result.StopReason)
	}
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatalf("tree kill did not terminate the child promptly: %v", elapsed)
	}
}

func TestRunManagedProcessHelperVersion(t *testing.T) {
	root := t.TempDir()
	helper := workerHelper(t)
	result, err := RunManagedProcess(context.Background(), ProcessOptions{
		Executable:       helper,
		Arguments:        []string{"--version"},
		WorkingDirectory: root,
		OutputDirectory:  filepath.Join(root, "version"),
		TimeoutSeconds:   30,
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	stdout, err := os.ReadFile(result.Stdout)
	if err != nil || strings.TrimSpace(string(stdout)) != "codex-cli 0.154.0" {
		t.Fatalf("version stream: %q err=%v", stdout, err)
	}
}

func TestRunManagedProcessExtensionlessNativeExecutable(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot resolve test binary: %v", err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatalf("cannot read test binary: %v", err)
	}
	native := filepath.Join(root, "worker-helper-native")
	if err := os.WriteFile(native, data, 0o755); err != nil {
		t.Fatalf("cannot copy helper: %v", err)
	}
	if runtime.GOOS == "windows" {
		// Windows launches only its PATHEXT forms: an extensionless binary is
		// not a platform-native executable here and keeps the classified
		// rejection instead of a later os/exec lookup failure.
		if _, err := RunManagedProcess(context.Background(), ProcessOptions{
			Executable: native, WorkingDirectory: root,
			OutputDirectory: filepath.Join(root, "o-win"), TimeoutSeconds: 10,
		}); err == nil || err.Error() != "BF_BLOCKED: managed process launch requires an existing native .exe, not a shell launcher." {
			t.Fatalf("windows extensionless diagnostic: %v", err)
		}
		return
	}
	result, err := RunManagedProcess(context.Background(), ProcessOptions{
		Executable:       native,
		Arguments:        []string{"--version"},
		WorkingDirectory: root,
		OutputDirectory:  filepath.Join(root, "out"),
		TimeoutSeconds:   60,
	})
	if err != nil {
		t.Fatalf("extensionless native executable rejected: %v", err)
	}
	stdout, readErr := os.ReadFile(result.Stdout)
	if readErr != nil || strings.TrimSpace(string(stdout)) != "codex-cli 0.154.0" {
		t.Fatalf("unexpected helper stdout: %q %v", stdout, readErr)
	}
}
