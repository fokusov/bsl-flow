package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Port of Invoke-BFOpenCodeWorker (OpenCode.ps1:6-82): the managed OpenCode
// worker loop — sealed binding, sandboxed dispatch through the sandbox
// executable, strict event classification and the host-result receipt.

// opencodeFlashModel is the only measured route the adapter supports
// (OpenCode.ps1:13).
const opencodeFlashModel = "deepseek/deepseek-v4-flash"

var opencodeToolSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// OpenCodeRequest is the sealed Invoke-BFOpenCodeWorker parameter surface
// plus the controller state the adapter reads. The function-valued members
// are the controller-owned seams the stage host wires (see
// ProfiledCodexRequest).
type OpenCodeRequest struct {
	// Invoke-BFOpenCodeWorker parameters.
	Stage          string
	Prompt         string
	Directory      string
	CodexPath      string
	MaxOutputBytes int64 // 0 selects the 16777216 default
	// Controller state.
	WorkerPath     string
	ProjectPath    string
	TaskID         string
	TimeoutSeconds int // 0 selects the 1800 default
	Profile        ExecutionProfile
	Models         WorkerModels
	// UserProfile locates .local/share/opencode/auth.json
	// ([Environment]::GetFolderPath('UserProfile')); empty uses the ambient
	// profile.
	UserProfile string

	// Controller seams (required).
	Dependencies            func() (map[string]any, error)
	PermissionProfile       func(scratch, config string, writable bool) (string, error)
	TestExecutionCapability func(capabilityDir, scratch, config, permissions string, writable bool) error
	// Deadline is the task wall-time bound (BFRunDeadlineUtc).
	Deadline time.Time
}

// OpenCodeResult carries the terminal worker result, the usage/cost evidence
// and the persisted artifact paths.
type OpenCodeResult struct {
	Status          string
	Summary         string
	PayloadJSON     string
	Raw             map[string]any
	HostResult      map[string]any
	SessionID       string
	Usage           OpenCodeUsage
	ReportedCostUSD float64
	StepCount       int
	ToolCalls       []OpenCodeToolCall
	Process         ProcessResult
	StdoutPath      string
	ResultPath      string
	HostResultPath  string
}

// sanitizeOpenCodeTool mirrors the tool name rewrite of the unica allowlist
// (OpenCode.ps1:22).
func sanitizeOpenCodeTool(tool string) string {
	return opencodeToolSanitizer.ReplaceAllString(tool, "_")
}

// RunOpenCode executes the managed OpenCode worker contract of
// Invoke-BFOpenCodeWorker: resume through immutable receipts or dispatch the
// sandboxed provider run, then classify the step/part stream exactly like the
// PowerShell adapter. An unsupported provider/sandbox identity returns the
// typed *Blocker (BF_BLOCKED), never a weakened launch; cancellation through
// ctx kills the owned process tree and the stop reason is refused with the
// adapter's exact reconciliation diagnostic.
func RunOpenCode(ctx context.Context, req OpenCodeRequest) (OpenCodeResult, error) {
	if req.Dependencies == nil {
		return OpenCodeResult{}, invalid("execution dependency source is required.")
	}
	dependencies, err := req.Dependencies()
	if err != nil {
		return OpenCodeResult{}, err
	}
	profile := req.Profile
	if req.MaxOutputBytes == 0 {
		req.MaxOutputBytes = 16777216
	}
	codexIdentity, err := workerSafePath(req.CodexPath)
	if err != nil {
		return OpenCodeResult{}, err
	}
	sandboxIdentity, err := workerSafePath(profile.Sandbox.Executable)
	if err != nil {
		return OpenCodeResult{}, err
	}
	if profile.Provider != "opencode" || codexIdentity != sandboxIdentity {
		return OpenCodeResult{}, &Blocker{Reason: "managed provider/sandbox identity mismatch."}
	}
	model, effort := req.Models.Selection(req.Stage)
	if model != opencodeFlashModel || effort != "" {
		return OpenCodeResult{}, blocked("only the measured Flash route with no effort override is supported.")
	}
	directory, err := workerSafePath(req.Directory)
	if err != nil {
		return OpenCodeResult{}, err
	}
	promptSHA256, err := hashValue(req.Prompt)
	if err != nil {
		return OpenCodeResult{}, err
	}
	binding := map[string]any{
		"dependencies":  dependencies,
		"stage":         req.Stage,
		"prompt_sha256": promptSHA256,
		"worker_path":   req.WorkerPath,
	}
	if req.MaxOutputBytes != 16777216 {
		binding["max_output_bytes"] = req.MaxOutputBytes
	}
	bindingHash, err := hashValue(binding)
	if err != nil {
		return OpenCodeResult{}, err
	}
	directoryHash, err := hashValue(directory)
	if err != nil {
		return OpenCodeResult{}, err
	}
	hostRoot, err := workerSafePath(filepath.Join(req.ProjectPath, ".bsl-flow", "hosts", req.TaskID, directoryHash))
	if err != nil {
		return OpenCodeResult{}, err
	}
	scratch := filepath.Join(hostRoot, "scratch")
	config := filepath.Join(hostRoot, "config")
	if req.PermissionProfile == nil {
		return OpenCodeResult{}, invalid("execution permission profile source is required.")
	}
	permissions, err := req.PermissionProfile(scratch, config, req.Stage == StageImplement)
	if err != nil {
		return OpenCodeResult{}, err
	}
	unica := profile.Toolset.Name == "unica"
	allowed := []string{"read", "glob", "grep", "skill", "bash"}
	if unica {
		allowed = []string{"read", "glob", "grep", "skill"}
		for _, tool := range profile.Unica.AllowedTools {
			allowed = append(allowed, "unica_"+sanitizeOpenCodeTool(tool))
		}
	}
	if req.Stage == StageImplement {
		allowed = append(allowed, "edit", "write", "apply_patch")
	}
	if req.Stage == StageSpecReview {
		allowed = []string{}
	}
	hostPath := filepath.Join(directory, "host-result.json")
	exitPath := filepath.Join(directory, "exit.json")
	var process ProcessResult
	if directoryExists(directory) {
		// A partial dispatch can already have changed sources or spent money.
		// Resume parses complete saved evidence; it never repeats the model
		// call.
		if !fileLeafExists(filepath.Join(directory, "binding.json")) || !fileLeafExists(exitPath) {
			return OpenCodeResult{}, blocked("partial OpenCode dispatch; reconcile the preserved attempt without retry.")
		}
		persistedBinding, err := readJSONObjectFile(filepath.Join(directory, "binding.json"))
		if err != nil {
			return OpenCodeResult{}, err
		}
		if value, _ := asString(persistedBinding["sha256"]); value != bindingHash {
			return OpenCodeResult{}, blocked("cached OpenCode invocation binding differs.")
		}
		process, err = processFromExitReceipt(exitPath)
		if err != nil {
			return OpenCodeResult{}, err
		}
	} else {
		if directoryExists(hostRoot) {
			return OpenCodeResult{}, blocked("unregistered OpenCode host directory exists.")
		}
		dispatch, err := dispatchOpenCode(ctx, req, profile, directory, scratch, config,
			binding, bindingHash, permissions, unica, model)
		if err != nil {
			return OpenCodeResult{}, err
		}
		process = dispatch
	}
	if process.StopReason != "" {
		return OpenCodeResult{}, blocked("OpenCode %s; reconcile the attempt before retry.", process.StopReason)
	}
	parsed, err := ReadOpenCodeEvents(process.Stdout, process.ExitCode, allowed)
	if err != nil {
		return OpenCodeResult{}, err
	}
	current, err := req.Dependencies()
	if err != nil {
		return OpenCodeResult{}, err
	}
	if same, err := hashValueEqual(current, dependencies); err != nil {
		return OpenCodeResult{}, err
	} else if !same {
		return OpenCodeResult{}, blocked("host/toolset inputs changed during execution.")
	}
	modelPath := filepath.Join(directory, "model-result.json")
	if fileLeafExists(modelPath) {
		saved, err := readJSONObjectFile(modelPath)
		if err != nil {
			return OpenCodeResult{}, err
		}
		if same, err := hashValueEqual(saved, parsed.Result); err != nil {
			return OpenCodeResult{}, err
		} else if !same {
			return OpenCodeResult{}, blocked("cached model result differs from raw OpenCode evidence.")
		}
	} else if err := writeJSONFile(modelPath, parsed.Result, false); err != nil {
		return OpenCodeResult{}, err
	}
	metadata := make(map[string]any, len(parsed.Metadata)+4)
	for key, value := range parsed.Metadata {
		metadata[key] = value
	}
	// reported_cost_usd already carries the double normalization the
	// PowerShell adapter applies before writing the receipt
	// (OpenCode.ps1:77).
	metadata["requested_model"] = model
	metadata["requested_effort"] = nilIfEmpty(effort)
	metadata["binding_sha256"] = bindingHash
	metadata["usage_source"] = process.Stdout
	if fileLeafExists(hostPath) {
		saved, err := readJSONObjectFile(hostPath)
		if err != nil {
			return OpenCodeResult{}, err
		}
		if same, err := hashValueEqual(saved, metadata); err != nil {
			return OpenCodeResult{}, err
		} else if !same {
			return OpenCodeResult{}, blocked("cached OpenCode receipt differs from raw evidence.")
		}
	} else if err := writeJSONFile(hostPath, metadata, false); err != nil {
		return OpenCodeResult{}, err
	}
	return OpenCodeResult{
		Status:          parsed.Status,
		Summary:         parsed.Summary,
		PayloadJSON:     parsed.PayloadJSON,
		Raw:             parsed.Result,
		HostResult:      metadata,
		SessionID:       parsed.SessionID,
		Usage:           parsed.Usage,
		ReportedCostUSD: parsed.ReportedCostUSD,
		StepCount:       parsed.StepCount,
		ToolCalls:       parsed.ToolCalls,
		Process:         process,
		StdoutPath:      process.Stdout,
		ResultPath:      modelPath,
		HostResultPath:  hostPath,
	}, nil
}

// dispatchOpenCode is the fresh-dispatch half of the worker loop
// (OpenCode.ps1:33-66).
func dispatchOpenCode(ctx context.Context, req OpenCodeRequest, profile ExecutionProfile,
	directory, scratch, config string, binding map[string]any, bindingHash, permissions string, unica bool, model string) (ProcessResult, error) {
	for _, path := range []string{
		directory, scratch,
		filepath.Join(scratch, "data"), filepath.Join(scratch, "cache"),
		filepath.Join(scratch, "state"), filepath.Join(scratch, "tmp"),
		filepath.Join(config, "opencode"),
	} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return ProcessResult{}, blocked("%v", err)
		}
	}
	permissionSHA256, err := hashValue(permissions)
	if err != nil {
		return ProcessResult{}, err
	}
	if err := writeJSONFile(filepath.Join(directory, "binding.json"), map[string]any{
		"sha256":            bindingHash,
		"binding":           binding,
		"permission_sha256": permissionSHA256,
	}, false); err != nil {
		return ProcessResult{}, err
	}
	specReview := req.Stage == StageSpecReview
	rules := map[string]any{
		"*":     "deny",
		"read":  "allow",
		"glob":  "allow",
		"grep":  "allow",
		"skill": "allow",
		"bash":  "allow",
	}
	external := map[string]any{"*": "deny"}
	external[strings.TrimRight(forwardSlash(profile.Toolset.Root), "/")+"/*"] = "allow"
	external[strings.TrimRight(forwardSlash(scratch), "/")+"/*"] = "allow"
	rules["external_directory"] = external
	if unica {
		rules["bash"] = "deny"
		for _, tool := range profile.Unica.AllowedTools {
			rules["unica_"+sanitizeOpenCodeTool(tool)] = "allow"
		}
	}
	if req.Stage == StageImplement {
		rules["edit"] = "allow"
	}
	if specReview {
		rules = map[string]any{"*": "deny"}
	}
	skillsPaths := []any{profile.Toolset.Root}
	if specReview {
		skillsPaths = []any{}
	}
	configuration := map[string]any{
		"model":         model,
		"default_agent": "bsl-flow",
		"plugin":        []any{},
		"skills":        map[string]any{"paths": skillsPaths},
		"agent": map[string]any{
			"bsl-flow": map[string]any{
				"mode":       "primary",
				"model":      model,
				"permission": rules,
			},
		},
	}
	if unica && !specReview {
		bootstrap := filepath.Join(profile.Unica.PluginRoot, "bootstrap", "bin", "win-x64", "unica-bootstrap.exe")
		configuration["mcp"] = map[string]any{
			"unica": map[string]any{
				"type":    "local",
				"command": []any{bootstrap, "run", "--plugin-root", profile.Unica.PluginRoot},
				"environment": map[string]any{
					"UNICA_RUNTIME_CACHE_DIR": profile.Unica.RuntimeCache,
				},
				"timeout": json.Number("60000"),
				"enabled": true,
			},
		}
	}
	if err := writeJSONFile(filepath.Join(config, "opencode", "opencode.json"), configuration, false); err != nil {
		return ProcessResult{}, err
	}
	if err := os.WriteFile(filepath.Join(config, "opencode", ".gitignore"), []byte{}, 0o644); err != nil {
		return ProcessResult{}, blocked("%v", err)
	}
	if req.TestExecutionCapability == nil {
		return ProcessResult{}, invalid("execution capability probe is required.")
	}
	if err := req.TestExecutionCapability(filepath.Join(directory, "capability"), scratch, config, permissions, req.Stage == StageImplement); err != nil {
		return ProcessResult{}, err
	}
	userProfile := req.UserProfile
	if strings.TrimSpace(userProfile) == "" {
		if home := os.Getenv("USERPROFILE"); strings.TrimSpace(home) != "" {
			userProfile = home
		} else if home, err := os.UserHomeDir(); err == nil {
			userProfile = home
		}
	}
	authPath := filepath.Join(userProfile, ".local", "share", "opencode", "auth.json")
	auth, err := readJSONObjectFile(authPath)
	if err != nil {
		return ProcessResult{}, err
	}
	deepseek, _ := asObject(auth["deepseek"])
	authType, _ := asString(getValue(deepseek, "type", nil))
	if authType != "api" {
		return ProcessResult{}, blocked("existing DeepSeek API authorization is unavailable.")
	}
	key, isString := asString(getValue(deepseek, "key", nil))
	if !isString || strings.TrimSpace(key) == "" {
		return ProcessResult{}, blocked("existing DeepSeek API authorization is unavailable.")
	}
	environment := map[string]string{
		"XDG_CONFIG_HOME":                  config,
		"XDG_DATA_HOME":                    filepath.Join(scratch, "data"),
		"XDG_CACHE_HOME":                   filepath.Join(scratch, "cache"),
		"XDG_STATE_HOME":                   filepath.Join(scratch, "state"),
		"TEMP":                             filepath.Join(scratch, "tmp"),
		"TMP":                              filepath.Join(scratch, "tmp"),
		"OPENCODE_DISABLE_PROJECT_CONFIG":  "1",
		"OPENCODE_DISABLE_CLAUDE_CODE":     "1",
		"OPENCODE_DISABLE_EXTERNAL_SKILLS": "1",
		"DEEPSEEK_API_KEY":                 key,
		"GIT_OPTIONAL_LOCKS":               "0",
	}
	arguments := []string{
		"sandbox", "-P", "bsl_execution",
		"-c", permissions,
		"-c", `windows.sandbox="elevated"`,
		"-C", req.WorkerPath,
		profile.Executable, "run", "--pure", "--format", "json",
		"--model", model, "--agent", "bsl-flow", "--dir", req.WorkerPath,
	}
	toolsetPrompt := ""
	if !specReview {
		toolsetPrompt, err = ToolsetPrompt(profile)
		if err != nil {
			return ProcessResult{}, err
		}
	}
	inputText := "The exact source working directory is: " + req.WorkerPath +
		". Resolve relative source paths here; do not search parent projects.\n" + req.Prompt + toolsetPrompt +
		"\nReturn exactly one JSON object with schema_version:1, status:completed|needs_input|blocked|failed, summary:string, payload_json:string containing valid JSON. Do not wrap the final answer in Markdown."
	current, err := req.Dependencies()
	if err != nil {
		return ProcessResult{}, err
	}
	if same, err := hashValueEqual(current, binding["dependencies"]); err != nil {
		return ProcessResult{}, err
	} else if !same {
		return ProcessResult{}, blocked("host/toolset inputs changed before dispatch.")
	}
	timeoutSeconds := req.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 1800
	}
	process, err := RunManagedProcess(ctx, ProcessOptions{
		Executable:       req.CodexPath,
		Arguments:        arguments,
		WorkingDirectory: req.WorkerPath,
		InputText:        inputText,
		OutputDirectory:  directory,
		TimeoutSeconds:   timeoutSeconds,
		Environment:      environment,
		CleanEnvironment: true,
		MaxOutputBytes:   req.MaxOutputBytes,
		Deadline:         req.Deadline,
	})
	if err != nil {
		return ProcessResult{}, err
	}
	// The DeepSeek key never leaves this frame; nothing is persisted from the
	// environment map (the PowerShell finally-block scrubbing).
	return process, nil
}
