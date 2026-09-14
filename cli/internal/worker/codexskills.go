package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"bsl-flow/cli/internal/repository"
)

// Port of global/skills/1c-task/adapters/Codex.Skills.ps1: the read-only
// app-server RPC client and the skill/MCP inventory contracts the profiled
// worker consumes. Diagnostics are byte-exact ports.

// Closed vocabulary of supported read-only RPC methods
// (Codex.Skills.ps1:6).
const (
	RPCSkillsList         = "skills/list"
	RPCConfigRead         = "config/read"
	RPCMcpServerStatusGet = "mcpServerStatus/list"
)

// CodexRPCRequest is the Invoke-BFCodexReadOnlyRpc parameter surface. The
// client intentionally keeps the caller's environment: the real Codex home
// and SQLite state are inputs, never managed copies (Codex.Skills.ps1:18).
type CodexRPCRequest struct {
	Executable       string
	Overrides        []string
	WorkingDirectory string
	// Directory is the RPC attempt directory (immutable receipts below it).
	Directory string
	Method    string
	Params    map[string]any
	// TimeoutSeconds defaults to 60 like the PowerShell parameter.
	TimeoutSeconds int
	// MaxOutputBytes defaults to 16777216 and bounds stdout+stderr.
	MaxOutputBytes int64
	// GuardMcp binds ExpectedMcpServers: the MCP inventory method refuses to
	// run without the same-process configuration guard
	// (Codex.Skills.ps1:12).
	GuardMcp           bool
	ExpectedMcpServers []string
	EnabledMcpServers  []string
	// Deadline is the task wall-time bound (BFRunDeadlineUtc).
	Deadline time.Time
}

// rpcLine is one stdout chunk in arrival order; eof marks the closed stream
// after every complete line was delivered.
type rpcLine struct {
	text string
	eof  bool
}

// CodexReadOnlyRPC mirrors Invoke-BFCodexReadOnlyRpc (Codex.Skills.ps1:4-63):
// an app-server --stdio child speaking JSON-RPC, initialized once, then either
// one direct request or a guarded config/read -> method sequence. The returned
// configuration is never persisted by this client.
func CodexReadOnlyRPC(ctx context.Context, req CodexRPCRequest) (result any, err error) {
	switch req.Method {
	case RPCSkillsList, RPCConfigRead, RPCMcpServerStatusGet:
	default:
		return nil, invalid("unsupported read-only Codex RPC.")
	}
	if req.MaxOutputBytes == 0 {
		req.MaxOutputBytes = 16777216
	}
	if req.MaxOutputBytes < 65536 || req.MaxOutputBytes > 16777216 {
		return nil, invalid("invalid RPC output bound.")
	}
	if req.TimeoutSeconds <= 0 {
		req.TimeoutSeconds = 60
	}
	if !req.Deadline.IsZero() {
		remaining := int(time.Until(req.Deadline).Seconds())
		if remaining < req.TimeoutSeconds {
			req.TimeoutSeconds = remaining
		}
		if req.TimeoutSeconds <= 0 {
			return nil, blocked("task deadline reached before RPC dispatch.")
		}
	}
	if directoryExists(req.Directory) {
		return nil, blocked("existing RPC attempt requires reconciliation.")
	}
	if req.Method == RPCMcpServerStatusGet && !req.GuardMcp {
		return nil, blocked("MCP inventory requires a same-process configuration guard.")
	}
	if err := os.MkdirAll(req.Directory, 0o755); err != nil {
		return nil, blocked("%v", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, blocked("RPC cancelled before dispatch.")
	}
	arguments := make([]string, 0, len(req.Overrides)+2)
	arguments = append(arguments, req.Overrides...)
	arguments = append(arguments, "app-server", "--stdio")
	command := exec.Command(req.Executable, arguments...)
	command.Dir = req.WorkingDirectory
	command.SysProcAttr = processAttributes()
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := command.StderrPipe()
	if err != nil {
		return nil, err
	}
	stderrFile, err := os.OpenFile(filepath.Join(req.Directory, "stderr.txt"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, blocked("%v", err)
	}
	if err := command.Start(); err != nil {
		_ = stderrFile.Close()
		return nil, err
	}
	startedAt := time.Now()
	if err := writeJSONFile(filepath.Join(req.Directory, "process.json"), map[string]any{
		"pid":            int64(command.Process.Pid),
		"start_time_utc": startTimeUTC(startedAt),
		"executable":     req.Executable,
	}, false); err != nil {
		_ = killProcessTree(command)
		_, _ = command.Process.Wait()
		_ = stderrFile.Close()
		return nil, err
	}
	stderrCopied := make(chan struct{})
	go func() {
		_, _ = io.Copy(stderrFile, stderrPipe)
		close(stderrCopied)
	}()
	var stdoutBytes atomic.Int64
	lines := make(chan rpcLine)
	go func() {
		reader := bufio.NewReaderSize(stdoutPipe, 64<<10)
		for {
			line, readErr := reader.ReadString('\n')
			if strings.HasSuffix(line, "\n") {
				stdoutBytes.Add(int64(len(line)))
				lines <- rpcLine{text: strings.TrimRight(line, "\r\n")}
			}
			if readErr != nil {
				lines <- rpcLine{eof: true}
				return
			}
		}
	}()
	// closeCodexRpc mirrors Close-BFCodexRpcProcess: close stdin, bound the
	// wait, kill the owned tree when it lingers, then bound the stderr
	// copier. A cleanup failure is surfaced only without a primary error.
	closeCodexRpc := func(primary error) error {
		cleanupError := error(nil)
		_ = stdin.Close()
		if command.Process != nil {
			done := make(chan struct{})
			go func() {
				_, _ = command.Process.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(1 * time.Second):
				if err := killProcessTree(command); err != nil && cleanupError == nil {
					cleanupError = err
				}
				<-done
			}
		}
		select {
		case <-stderrCopied:
		case <-time.After(2 * time.Second):
			if cleanupError == nil {
				cleanupError = blocked("owned RPC stderr remained open.")
			}
		}
		_ = stderrFile.Close()
		if cleanupError != nil && primary == nil {
			return cleanupError
		}
		return primary
	}
	defer func() {
		if cleanupError := closeCodexRpc(err); cleanupError != nil && err == nil {
			result = nil
			err = cleanupError
		}
	}()
	writeLine := func(value map[string]any) error {
		data, encodeErr := repository.Canonical(value)
		if encodeErr != nil {
			return invalid("%v", encodeErr)
		}
		if _, writeErr := stdin.Write(append(data, '\n')); writeErr != nil {
			return blocked("%v", writeErr)
		}
		return nil
	}
	if err := writeLine(map[string]any{
		"id":     json.Number("1"),
		"method": "initialize",
		"params": map[string]any{
			"clientInfo": map[string]any{
				"name":    "bsl_flow_inventory",
				"title":   "BSL Flow inventory",
				"version": "1",
			},
			"capabilities": map[string]any{"experimentalApi": true},
		},
	}); err != nil {
		return nil, err
	}
	watch := time.Now()
	phase := 1
	for result == nil {
		var message map[string]any
		received := false
		select {
		case <-ctx.Done():
			return nil, blocked("RPC cancelled.")
		case chunk := <-lines:
			if chunk.eof {
				return nil, blocked("RPC output closed before response.")
			}
			if stdoutBytes.Load() > req.MaxOutputBytes {
				return nil, blocked("RPC output limit.")
			}
			parsed, parseErr := convertFromCodexRpcLine(chunk.text, req.Method, phase, req.Directory)
			if parseErr != nil {
				return nil, parseErr
			}
			message = parsed
			received = true
		case <-time.After(100 * time.Millisecond):
			if time.Since(watch).Seconds() >= float64(req.TimeoutSeconds) {
				return nil, blocked("RPC timeout; preserve the attempt.")
			}
			if stderrSize(stderrFile) > req.MaxOutputBytes {
				return nil, blocked("RPC output limit.")
			}
			if stdoutBytes.Load() > req.MaxOutputBytes {
				return nil, blocked("RPC output limit.")
			}
		}
		if !received {
			continue
		}
		if value, isNotification := message["method"]; isNotification && value != nil {
			continue
		}
		id, idOK := asInt64(message["id"])
		if !idOK || id != int64(phase) {
			return nil, blocked("unexpected Codex RPC identity.")
		}
		if value, present := message["error"]; present && value != nil {
			return nil, blocked("Codex RPC rejected the request.")
		}
		if phase == 1 {
			if err := writeLine(map[string]any{"method": "initialized"}); err != nil {
				return nil, err
			}
			request := map[string]any{
				"id":     json.Number("2"),
				"method": req.Method,
				"params": req.Params,
			}
			if req.GuardMcp {
				request = map[string]any{
					"id":     json.Number("2"),
					"method": RPCConfigRead,
					"params": map[string]any{"cwd": req.WorkingDirectory, "includeLayers": false},
				}
			}
			if err := writeLine(request); err != nil {
				return nil, err
			}
			phase = 2
			continue
		}
		if phase == 2 && req.GuardMcp {
			if err := AssertCodexMcpConfiguration(message["result"], req.ExpectedMcpServers, req.EnabledMcpServers); err != nil {
				return nil, err
			}
			if err := writeLine(map[string]any{
				"id":     json.Number("3"),
				"method": req.Method,
				"params": req.Params,
			}); err != nil {
				return nil, err
			}
			phase = 3
			continue
		}
		value, present := message["result"]
		if !present || value == nil {
			return nil, blocked("missing RPC result.")
		}
		result = value
	}
	return result, nil
}

// convertFromCodexRpcLine mirrors ConvertFrom-BFCodexRpcLine
// (Codex.Skills.ps1:65-82). Only the post-guard MCP phase persists metadata
// about a malformed line, and never the line text itself.
func convertFromCodexRpcLine(line, method string, phase int, directory string) (map[string]any, error) {
	message, err := parseJSONObject([]byte(line))
	if err == nil {
		return message, nil
	}
	if method == RPCMcpServerStatusGet && phase == 3 {
		failure := map[string]any{
			"phase":           int64(phase),
			"method":          method,
			"line_characters": int64(len([]rune(line))),
			"line_utf8_bytes": int64(len(line)),
			"line_sha256":     sha256Text(line),
			"error_id":        "native JSON decode failure",
			"exception_type":  sprintf("%T", err),
			"json_line":       nil,
			"json_position":   nil,
		}
		var syntax *json.SyntaxError
		if errorsAs(err, &syntax) {
			failure["json_position"] = int64(syntax.Offset)
		}
		_ = writeJSONFile(filepath.Join(directory, "malformed-response.json"), failure, false)
	}
	return nil, blocked("malformed Codex RPC.")
}

// CodexMcpServerNames mirrors Get-BFCodexMcpServerNames
// (Codex.Skills.ps1:102-118): the case-sensitively sorted server names of the
// native configuration. An empty native table returns no names.
func CodexMcpServerNames(response any) ([]string, error) {
	object, ok := asObject(response)
	if !ok {
		return nil, blocked("missing native Codex configuration.")
	}
	configValue := getValue(object, "config", nil)
	if configValue == nil {
		return nil, blocked("missing native Codex configuration.")
	}
	config, _ := asObject(configValue)
	serversValue := getValue(config, "mcp_servers", nil)
	if serversValue == nil {
		return nil, nil
	}
	servers, ok := asObject(serversValue)
	if !ok {
		return nil, blocked("malformed native MCP configuration.")
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		if err := assertTextValue(name, "MCP server name"); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, nil
	}
	sort.Strings(names)
	return names, nil
}

// CodexMcpDenyOverrides mirrors Get-BFCodexMcpDenyOverrides
// (Codex.Skills.ps1:120-129): one -c override disabling each server.
func CodexMcpDenyOverrides(names []string) ([]string, error) {
	overrides := make([]string, 0, len(names)*2)
	for _, name := range names {
		// Codex CLI splits dotted keys literally; quoted TOML keys would
		// become part of the server name.
		if !isSafeMcpName(name) {
			return nil, blocked("MCP server name cannot be safely addressed by the pinned CLI override parser.")
		}
		overrides = append(overrides, "-c", "mcp_servers."+name+".enabled=false")
	}
	return overrides, nil
}

func isSafeMcpName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		if !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

// AssertCodexMcpConfiguration mirrors Assert-BFCodexMcpConfiguration
// (Codex.Skills.ps1:131-141): the exact server inventory and enablement.
func AssertCodexMcpConfiguration(response any, expected []string, enabled []string) error {
	actual, err := CodexMcpServerNames(response)
	if err != nil {
		return err
	}
	expectedSorted := append([]string(nil), expected...)
	sort.Strings(expectedSorted)
	same, err := hashValueEqual(actual, expectedSorted)
	if err != nil {
		return err
	}
	if !same {
		return blocked("native MCP configuration inventory changed before dispatch.")
	}
	object, _ := asObject(response)
	config, _ := asObject(getValue(object, "config", nil))
	servers, _ := asObject(getValue(config, "mcp_servers", nil))
	for _, name := range actual {
		server, _ := asObject(getValue(servers, name, nil))
		enabledValue := getValue(server, "enabled", true)
		flag, isBool := enabledValue.(bool)
		if !isBool || flag != containsString(enabled, name) {
			return blocked("native MCP server enablement differs from the exact profile.")
		}
	}
	return nil
}

// CodexSkillInventory mirrors ConvertTo-BFCodexSkillInventory
// (Codex.Skills.ps1:143-154): one unambiguous per-directory skill inventory
// with instruction hashes, sorted by path.
func CodexSkillInventory(response any, workingDirectory string) ([]map[string]any, error) {
	object, ok := asObject(response)
	if !ok {
		return nil, blocked("incomplete Codex skill inventory.")
	}
	data, ok := asArray(object["data"])
	if !ok || len(data) != 1 {
		return nil, blocked("incomplete Codex skill inventory.")
	}
	entry, ok := asObject(data[0])
	if !ok {
		return nil, blocked("incomplete Codex skill inventory.")
	}
	cwd, _ := asString(entry["cwd"])
	if cwd != workingDirectory {
		return nil, blocked("incomplete Codex skill inventory.")
	}
	if errorsValue, ok := asArray(entry["errors"]); !ok || len(errorsValue) != 0 {
		return nil, blocked("incomplete Codex skill inventory.")
	}
	seen := make(map[string]struct{})
	inventory := make([]map[string]any, 0, 8)
	skills, _ := asArray(entry["skills"])
	for _, skillValue := range skills {
		skill, ok := asObject(skillValue)
		if !ok {
			return nil, blocked("ambiguous Codex skill inventory.")
		}
		skillPath, _ := asString(skill["path"])
		resolved, err := workerSafePath(skillPath)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[resolved]; duplicate || filepath.Base(resolved) != "SKILL.md" {
			return nil, blocked("ambiguous Codex skill inventory.")
		}
		if _, isBool := skill["enabled"].(bool); !isBool {
			return nil, blocked("ambiguous Codex skill inventory.")
		}
		seen[resolved] = struct{}{}
		digest, err := fileHash(resolved)
		if err != nil {
			return nil, err
		}
		inventory = append(inventory, map[string]any{
			"name":    skill["name"],
			"path":    resolved,
			"scope":   skill["scope"],
			"enabled": skill["enabled"],
			"sha256":  digest,
		})
	}
	sort.Slice(inventory, func(left, right int) bool {
		leftPath, _ := asString(inventory[left]["path"])
		rightPath, _ := asString(inventory[right]["path"])
		return leftPath < rightPath
	})
	return inventory, nil
}

// CodexSkillDenyOverride mirrors Get-BFCodexSkillDenyOverride
// (Codex.Skills.ps1:156-160): the single skills.config override that disables
// exactly the discovered skills.
func CodexSkillDenyOverride(inventory []map[string]any) string {
	entries := make([]string, 0, len(inventory))
	for _, skill := range inventory {
		path, _ := asString(skill["path"])
		entries = append(entries, "{path="+jsonQuote(forwardSlash(path))+",enabled=false}")
	}
	return "skills.config=[" + strings.Join(entries, ",") + "]"
}

// AssertCodexSkillDenial mirrors Assert-BFCodexSkillDenial
// (Codex.Skills.ps1:162-166): the disabled inventory must be exactly the
// registered inventory with every enablement flipped to false.
func AssertCodexSkillDenial(inventory, disabled []map[string]any) error {
	expected := make([]any, 0, len(inventory))
	for _, skill := range inventory {
		expected = append(expected, map[string]any{
			"name":    skill["name"],
			"path":    skill["path"],
			"scope":   skill["scope"],
			"enabled": false,
			"sha256":  skill["sha256"],
		})
	}
	observed := make([]any, 0, len(disabled))
	for _, skill := range disabled {
		observed = append(observed, skill)
	}
	same, err := hashValueEqual(expected, observed)
	if err != nil {
		return err
	}
	if !same {
		return blocked("Codex did not disable exactly the registered discovered skills.")
	}
	return nil
}

// getValue mirrors Get-BFValue: a present key wins even when its value is
// null, and only a missing key yields the default.
func getValue(object map[string]any, name string, def any) any {
	if object == nil {
		return def
	}
	if value, present := object[name]; present {
		return value
	}
	return def
}

func asArray(value any) ([]any, bool) {
	array, ok := value.([]any)
	return array, ok && array != nil
}

// asInt64 extracts an integral JSON number identity.
func asInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	default:
		return 0, false
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileLeafExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func stderrSize(file *os.File) int64 {
	info, err := file.Stat()
	if err != nil {
		return 0
	}
	return info.Size()
}

func sha256Text(text string) string {
	return sha256Hex([]byte(text))
}
