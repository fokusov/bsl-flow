package stagehost

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"bsl-flow/cli/internal/repository"
)

// TestMain doubles as the managed-process helper: a copy of this test binary
// re-executes itself with BF_STAGEHOST_PROCESS_HELPER set, so a real native
// executable exists under the mandatory .exe name on every platform. With
// BF_STAGEHOST_PROBE_HELPER set it mirrors the hidden `__fs-probe` CLI entry:
// exactly one JSON path document, non-zero exit otherwise.
//
// The argv-driven modes below mirror the fake managed-provider helper of the
// worker package: a copy of this binary self-identifies from its argv and
// serves the recorded codex exec/RPC shapes, the pinned runtime probe and a
// policy-enforcing sandbox emulator, so both the packaged PowerShell
// provider and the native stage host can drive one worker-stage dispatch
// end to end in tests.
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
	if len(os.Args) > 1 {
		switch fakeProviderMode(os.Args[1:]) {
		case "fake-version":
			fmt.Println("codex-cli 0.154.0")
			return
		case "fake-runtime-probe":
			fakeRuntimeProbe(os.Args[1:])
			return
		case "fake-codex-rpc":
			fakeCodexRPC(os.Args[1:])
			return
		case "fake-codex-exec":
			fakeCodexExec(os.Args[1:])
			return
		case "fake-sandbox":
			fakeSandbox(os.Args[1:])
			return
		case "fake-fs-probe":
			fakeFSProbe(os.Args[1:])
			return
		}
	}
	os.Exit(m.Run())
}

// fakeConfig is the bf-worker-fake.json script of the provider helper.
type fakeConfig struct {
	Session        string          `json:"session"`
	Result         string          `json:"result"`
	Skills         [][]string      `json:"skills"`
	McpTools       []string        `json:"mcp_tools"`
	McpServers     map[string]bool `json:"mcp_servers"`
	RuntimeVersion string          `json:"runtime_version"`
	Packages       [][]string      `json:"packages"`
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

func fakeProviderMode(args []string) string {
	contains := func(needle string) bool {
		for _, argument := range args {
			if argument == needle {
				return true
			}
		}
		return false
	}
	switch {
	case contains("--version"):
		return "fake-version"
	case len(args) > 0 && args[0] == "-I":
		return "fake-runtime-probe"
	case contains("app-server"):
		return "fake-codex-rpc"
	case len(args) > 0 && args[0] == "exec":
		return "fake-codex-exec"
	case len(args) > 0 && args[0] == "sandbox":
		return "fake-sandbox"
	case len(args) > 0 && args[0] == "__fs-probe":
		return "fake-fs-probe"
	}
	return ""
}

// fakeRuntimeProbe mirrors the pinned interpreter probe script the preflight
// runs: it reports its own executable identity and the package versions from
// the fixture script.
func fakeRuntimeProbe(args []string) {
	config := readFakeConfig()
	exe, _ := os.Executable()
	packages := map[string]any{}
	for _, pair := range config.Packages {
		if len(pair) == 2 {
			packages[pair[0]] = pair[1]
		}
	}
	if config.RuntimeVersion == "" {
		config.RuntimeVersion = "3.12.14"
	}
	_ = args
	data, _ := json.Marshal(map[string]any{
		"sys_executable": exe, "version": config.RuntimeVersion, "packages": packages,
	})
	fmt.Println(string(data))
}

// fakeCodexExec emits the recorded codex exec --json stream and writes the
// model result to the --output-last-message artifact.
func fakeCodexExec(args []string) {
	config := readFakeConfig()
	_, _ = io.ReadAll(os.Stdin)
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
	emit(`{"type":"item.completed","item":{"id":"answer","type":"agent_message","text":` + jsonStringOf(config.Result) + `}}`)
	emit(`{"type":"turn.completed","usage":{"input_tokens":4,"cached_input_tokens":0,"output_tokens":2,"cache_write_input_tokens":0,"reasoning_output_tokens":1}}`)
	os.Exit(0)
}

// fakeCodexRPC speaks the app-server JSON-RPC handshake the read-only client
// drives: initialize, then config/read / skills/list / mcpServerStatus/list.
func fakeCodexRPC(args []string) {
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
					"name": skill[0], "path": skill[1], "scope": "project", "enabled": !denied,
				})
			}
			respond(request.ID, map[string]any{"data": []any{map[string]any{
				"cwd": cwd, "errors": []any{}, "skills": skills,
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

// fakeFSProbe evaluates the native filesystem probe against the sandbox
// policy the fake sandbox would enforce.
func fakeFSProbe(args []string) {
	paths := probePathsFromArgument(args)
	observations := evaluateSandboxPolicy(paths)
	data, _ := json.Marshal(observations)
	fmt.Println(string(data))
}

// fakeSandbox emulates the managed sandbox for the capability probe children:
// the PowerShell encoded probe and the native __fs-probe receive the exact
// observations the declared permission profile enforces.
func fakeSandbox(args []string) {
	permissions := ""
	for index, argument := range args {
		if argument == "-c" && index+1 < len(args) {
			if permissions == "" {
				permissions = args[index+1]
			}
		}
	}
	encoded := ""
	for index, argument := range args {
		if argument == "-EncodedCommand" && index+1 < len(args) {
			encoded = args[index+1]
		}
		if argument == "__fs-probe" && index+1 < len(args) {
			paths := probePathsFromArgument(args[index:])
			observations := evaluatePolicy(permissions, paths)
			data, _ := json.Marshal(observations)
			fmt.Println(string(data))
			os.Exit(0)
		}
	}
	if encoded != "" {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			os.Exit(1)
		}
		script := string(utf16.Decode(bytesToUTF16(decoded)))
		paths := extractProbePaths(script)
		observations := evaluatePolicy(permissions, paths)
		data, _ := json.Marshal(observations)
		fmt.Println(string(data))
		os.Exit(0)
	}
	os.Exit(0)
}

func bytesToUTF16(data []byte) []uint16 {
	units := make([]uint16, 0, len(data)/2)
	for index := 0; index+1 < len(data); index += 2 {
		units = append(units, uint16(data[index])|uint16(data[index+1])<<8)
	}
	return units
}

// extractProbePaths pulls the PATHS_JSON literal out of the decoded
// PowerShell probe script.
func extractProbePaths(script string) map[string]string {
	start := strings.Index(script, "$paths='")
	if start < 0 {
		return map[string]string{}
	}
	rest := script[start+len("$paths='"):]
	end := strings.Index(rest, "' | ConvertFrom-Json")
	if end < 0 {
		end = strings.Index(rest, "'|ConvertFrom-Json")
	}
	if end < 0 {
		return map[string]string{}
	}
	var paths map[string]string
	if json.Unmarshal([]byte(rest[:end]), &paths) != nil {
		return map[string]string{}
	}
	return paths
}

func probePathsFromArgument(args []string) map[string]string {
	for index, argument := range args {
		if argument == "__fs-probe" && index+1 < len(args) {
			var paths map[string]string
			if json.Unmarshal([]byte(args[index+1]), &paths) != nil {
				return map[string]string{}
			}
			return paths
		}
	}
	return map[string]string{}
}

// evaluateSandboxPolicy evaluates probe paths with a permissive default
// (used when no policy argument accompanies the probe).
func evaluateSandboxPolicy(paths map[string]string) map[string]string {
	return evaluatePolicy("", paths)
}

// evaluatePolicy classifies each probe path against the declared filesystem
// permission entries the way the managed sandbox enforces them: the most
// specific root wins and ":root" is the default access.
func evaluatePolicy(permissions string, paths map[string]string) map[string]string {
	entries := map[string]string{}
	if strings.Contains(permissions, "filesystem={") {
		section := permissions[strings.Index(permissions, "filesystem={")+len("filesystem={"):]
		if end := strings.Index(section, "}"); end >= 0 {
			section = section[:end]
		}
		matcher := regexp.MustCompile(`"((?:[^"\\]|\\.)*)"\s*=\s*"(read|write|none)"`)
		for _, match := range matcher.FindAllStringSubmatch(section, -1) {
			var key string
			if err := json.Unmarshal([]byte(`"`+match[1]+`"`), &key); err == nil {
				entries[key] = match[2]
			}
		}
	}
	accessOf := func(path string) string {
		normalized := strings.ReplaceAll(strings.ToLower(strings.TrimRight(path, `\/`)), `\`, "/")
		best := ""
		bestLength := -1
		for root, access := range entries {
			if root == ":root" {
				continue
			}
			prefix := strings.ToLower(strings.TrimRight(root, "/")) + "/"
			if normalized == strings.ToLower(strings.TrimRight(root, "/")) || strings.HasPrefix(normalized, prefix) {
				if len(prefix) > bestLength {
					best = access
					bestLength = len(prefix)
				}
			}
		}
		if best == "" {
			best = entries[":root"]
		}
		if best == "" {
			best = "read"
		}
		return best
	}
	result := map[string]string{}
	for name, path := range paths {
		access := accessOf(path)
		switch {
		case strings.HasSuffix(name, "_write"):
			if access == "write" {
				result[name] = "allowed"
			} else {
				result[name] = "denied"
			}
		default:
			if access == "none" {
				result[name] = "denied"
			} else {
				result[name] = "allowed"
			}
		}
	}
	return result
}

func jsonStringOf(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
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
