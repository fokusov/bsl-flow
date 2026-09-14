package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Port of Invoke-BFProfiledCodexWorker (ProfiledCodex.ps1:200-375): the full
// sealed Codex worker loop — sealed prompt binding, critic catalog route,
// immutable attempt receipts, RPC-guarded inventories, argv-as-data dispatch,
// strict event classification and the host-result receipt.

// criticCodexSHA256 is the exact verified native executable the no-tools
// critic route requires (ProfiledCodex.ps1:214).
const criticCodexSHA256 = "be96b992178b1e467c225800da0d65f2c86d5eba1ef0b14632f65db381cbdfde"

var criticBindingSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ProfiledCodexOverrides mirrors Get-BFProfiledCodexOverrides
// (ProfiledCodex.ps1:6-11): the sealed argv data of every profiled Codex
// process. Arguments are data; no shell interpolation anywhere.
func ProfiledCodexOverrides(permissions, logDirectory string) []string {
	arguments := []string{
		"-c", `approval_policy="never"`,
		"-c", `default_permissions="bsl_execution"`,
		"-c", permissions,
		"-c", `windows.sandbox="elevated"`,
		"-c", `skills.include_instructions=false`,
		"-c", `agents.enabled=false`,
		"-c", `mcp_servers={}`,
		"-c", `web_search="disabled"`,
		"-c", "log_dir=" + jsonQuote(forwardSlash(logDirectory)),
	}
	for _, feature := range []string{
		"plugins", "apps", "multi_agent", "multi_agent_v2", "memories", "shell_snapshot",
		"hooks", "browser_use", "computer_use", "in_app_browser", "skill_mcp_dependency_install",
	} {
		arguments = append(arguments, "--disable", feature)
	}
	return arguments
}

// ProfiledCodexRequest is the sealed Invoke-BFProfiledCodexWorker parameter
// surface plus the controller state the adapter reads. The function-valued
// members are the controller-owned seams (Get-BFExecutionDependencies,
// Get-BFExecutionPermissionProfile, Test-BFExecutionCapability) the stage
// host wires; they stay outside this package because they belong to the
// controller state machine, not to the worker adapter.
type ProfiledCodexRequest struct {
	// Invoke-BFProfiledCodexWorker parameters.
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
	// RequireObservedIdentity is the strict current-agent fallback flag
	// (request.require_observed_identity).
	RequireObservedIdentity bool
	// FallbackCatalogSourcePath/FallbackCatalogSHA256 are the
	// request.fallback_catalog_* binding of the critic routes.
	FallbackCatalogSourcePath string
	FallbackCatalogSHA256     string
	// SchemaPath is worker-result.schema.json (copied and hashed into the
	// binding).
	SchemaPath string
	// AdapterSHA256/RpcSHA256 replace the PowerShell self-hashes
	// ($PSCommandPath, Codex.Skills.ps1): the native host binds its own
	// compiled adapter identity.
	AdapterSHA256 string
	RpcSHA256     string
	// CodexHome overrides CODEX_HOME for the rollout lookup and the child
	// environment; empty uses the ambient variable.
	CodexHome string

	// Controller seams (required).
	Dependencies            func() (map[string]any, error)
	PermissionProfile       func(scratch, config string, writable bool) (string, error)
	TestExecutionCapability func(capabilityDir, scratch, config, permissions string, writable bool) error
	// Deadline is the task wall-time bound (BFRunDeadlineUtc).
	Deadline time.Time
}

// ProfiledCodexResult carries the terminal worker result, the usage/identity
// evidence and the persisted artifact paths.
type ProfiledCodexResult struct {
	Status      string // completed | needs_input | blocked | failed
	Summary     string
	PayloadJSON string
	// Raw is the parsed model-result.json object.
	Raw map[string]any
	// HostResult is the canonical-ready host-result.json map.
	HostResult      map[string]any
	SessionID       string
	Usage           *Usage
	RequestedModel  string
	RequestedEffort string
	ObservedModel   string
	ObservedEffort  string
	RolloutPath     string
	TurnID          string
	Process         ProcessResult
	// Artifact paths.
	StdoutPath     string
	ResultPath     string
	HostResultPath string
}

// RunProfiledCodex executes the managed Codex worker contract of
// Invoke-BFProfiledCodexWorker. It resumes a preserved attempt through its
// immutable receipts, dispatches codex exec with sealed argv data otherwise,
// and classifies the stream evidence exactly like the PowerShell adapter —
// including the typed capability refusals: an unsupported sandbox/tool
// capability returns a *Blocker (BF_BLOCKED), never a weakened launch.
// Cancellation through ctx kills the owned process tree; a stop reason is
// reported through the receipt and refused with the adapter's exact
// diagnostics, so an unknown external effect stays unknown.
func RunProfiledCodex(ctx context.Context, req ProfiledCodexRequest) (ProfiledCodexResult, error) {
	if req.Dependencies == nil {
		return ProfiledCodexResult{}, invalid("execution dependency source is required.")
	}
	dependencies, err := req.Dependencies()
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	profile := req.Profile
	if req.MaxOutputBytes == 0 {
		req.MaxOutputBytes = 16777216
	}
	if req.MaxOutputBytes < 65536 || req.MaxOutputBytes > 16777216 {
		return ProfiledCodexResult{}, invalid("managed output bound is outside the supported range.")
	}
	codexIdentity, err := workerSafePath(req.CodexPath)
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	sandboxIdentity, err := workerSafePath(profile.Sandbox.Executable)
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	if profile.Provider != "codex" || codexIdentity != sandboxIdentity {
		return ProfiledCodexResult{}, &Blocker{Reason: "managed Codex/sandbox identity mismatch."}
	}
	if err := AssertWorkerConfiguration(req.WorkerPath); err != nil {
		return ProfiledCodexResult{}, err
	}
	model, effort := req.Models.Selection(req.Stage)
	critic := req.Stage == StageSpecReview
	requireObserved := req.RequireObservedIdentity
	if critic && profile.ExecutableSHA256 != criticCodexSHA256 {
		return ProfiledCodexResult{}, blocked("no-tools critic capability requires the exact verified native executable.")
	}
	if critic {
		if !requireObserved && model != "gpt-5.6-luna" {
			return ProfiledCodexResult{}, blocked("non-strict sealed critic is pinned to gpt-5.6-luna.")
		}
		if _, err := CriticModelContract(model); err != nil {
			return ProfiledCodexResult{}, err
		}
	}
	directory, err := workerSafePath(req.Directory)
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	controllerRoot, err := workerSafePath(filepath.Join(req.ProjectPath, ".bsl-flow", "tasks"))
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	if !hasPrefixFold(directory, strings.TrimRight(controllerRoot, `\/`+string(filepath.Separator))+string(filepath.Separator)) {
		return ProfiledCodexResult{}, invalid("Codex controller attempt must be under the private task directory.")
	}
	schemaSHA256, err := fileHash(req.SchemaPath)
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	inputText := req.Prompt
	if !critic {
		prompt, err := ToolsetPrompt(profile)
		if err != nil {
			return ProfiledCodexResult{}, err
		}
		inputText += prompt
	}
	promptSHA256, err := hashValue(inputText)
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	binding := map[string]any{
		"dependencies":     dependencies,
		"stage":            req.Stage,
		"prompt_sha256":    promptSHA256,
		"schema_sha256":    schemaSHA256,
		"adapter_sha256":   req.AdapterSHA256,
		"rpc_sha256":       req.RpcSHA256,
		"worker_path":      req.WorkerPath,
		"max_output_bytes": req.MaxOutputBytes,
	}
	var criticCatalog CriticCatalog
	if critic {
		catalogSourcePath := req.FallbackCatalogSourcePath
		catalogSourceSHA256 := req.FallbackCatalogSHA256
		switch {
		case requireObserved:
			if strings.TrimSpace(catalogSourcePath) == "" || !criticBindingSHA256Pattern.MatchString(catalogSourceSHA256) {
				return ProfiledCodexResult{}, blocked("strict current-agent critic requires an exact catalog source binding.")
			}
			sourcePath, err := workerSafePath(catalogSourcePath)
			if err != nil {
				return ProfiledCodexResult{}, err
			}
			if !fileLeafExists(sourcePath) {
				return ProfiledCodexResult{}, blocked("strict current-agent critic catalog source bytes changed.")
			}
			digest, err := fileHash(sourcePath)
			if err != nil {
				return ProfiledCodexResult{}, err
			}
			if digest != catalogSourceSHA256 {
				return ProfiledCodexResult{}, blocked("strict current-agent critic catalog source bytes changed.")
			}
			criticCatalog, err = CriticCatalogFromSourcePath(sourcePath, model, effort)
			if err != nil {
				return ProfiledCodexResult{}, err
			}
		case strings.TrimSpace(catalogSourcePath) != "":
			if !criticBindingSHA256Pattern.MatchString(catalogSourceSHA256) {
				return ProfiledCodexResult{}, blocked("supplied critic catalog source binding is invalid.")
			}
			sourcePath, err := workerSafePath(catalogSourcePath)
			if err != nil {
				return ProfiledCodexResult{}, err
			}
			digest, err := fileHash(sourcePath)
			if err != nil {
				return ProfiledCodexResult{}, err
			}
			if digest != catalogSourceSHA256 {
				return ProfiledCodexResult{}, blocked("supplied critic catalog source bytes changed.")
			}
			criticCatalog, err = CriticCatalogFromSourcePath(sourcePath, model, effort)
			if err != nil {
				return ProfiledCodexResult{}, err
			}
		default:
			criticCatalog, err = CriticCatalogFromDirectory(directory, model, effort)
			if err != nil {
				return ProfiledCodexResult{}, err
			}
		}
		catalogSHA256, err := hashValue(criticCatalog.Catalog)
		if err != nil {
			return ProfiledCodexResult{}, err
		}
		sourceSHA256, err := hashValue(criticCatalog.Source)
		if err != nil {
			return ProfiledCodexResult{}, err
		}
		binding["critic_catalog_sha256"] = catalogSHA256
		binding["critic_catalog_source_sha256"] = sourceSHA256
		capability, err := CriticCapabilityVersion(model)
		if err != nil {
			return ProfiledCodexResult{}, err
		}
		binding["critic_capability"] = capability
	}
	bindingHash, err := hashValue(binding)
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	directoryHash, err := hashValue(directory)
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	hostRoot, err := workerSafePath(filepath.Join(req.ProjectPath, ".bsl-flow", "hosts", req.TaskID, directoryHash))
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	scratch := filepath.Join(hostRoot, "scratch")
	config := filepath.Join(hostRoot, "config")
	if req.PermissionProfile == nil {
		return ProfiledCodexResult{}, invalid("execution permission profile source is required.")
	}
	permissions, err := req.PermissionProfile(scratch, config, req.Stage == StageImplement)
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	exitPath := filepath.Join(directory, "exit.json")
	hostPath := filepath.Join(directory, "host-result.json")
	resultPath := filepath.Join(directory, "model-result.json")
	var process ProcessResult
	if directoryExists(directory) {
		for _, name := range []string{
			"binding.json", "exit.json", "model-result.json", "inventory.json", "disabled.json",
			"post-inventory.json", "mcp-inventory.json", "mcp-config-names.json", "capability/capability.json",
		} {
			if !fileLeafExists(filepath.Join(directory, filepath.FromSlash(name))) {
				return ProfiledCodexResult{}, blocked("partial Codex dispatch; reconcile the preserved attempt without retry.")
			}
		}
		persistedBinding, err := readJSONObjectFile(filepath.Join(directory, "binding.json"))
		if err != nil {
			return ProfiledCodexResult{}, err
		}
		if value, _ := asString(persistedBinding["sha256"]); value != bindingHash {
			return ProfiledCodexResult{}, blocked("cached Codex binding differs.")
		}
		process, err = processFromExitReceipt(exitPath)
		if err != nil {
			return ProfiledCodexResult{}, err
		}
	} else {
		if directoryExists(hostRoot) {
			return ProfiledCodexResult{}, blocked("unregistered Codex host directory exists.")
		}
		dispatch, err := dispatchProfiledCodex(ctx, req, profile, directory, scratch, config, hostRoot,
			binding, bindingHash, permissions, criticCatalog, critic, model, effort, requireObserved, inputText)
		if err != nil {
			return ProfiledCodexResult{}, err
		}
		process = dispatch
	}
	inventory, err := readJSONObjectFile(filepath.Join(directory, "inventory.json"))
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	postInventory, err := readJSONObjectFile(filepath.Join(directory, "post-inventory.json"))
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	for _, evidence := range []map[string]any{inventory, postInventory} {
		digest, err := hashValue(evidence["skills"])
		if err != nil {
			return ProfiledCodexResult{}, err
		}
		if digest != profile.CodexSkillsSHA256 {
			return ProfiledCodexResult{}, blocked("Codex skill inventory changed during execution.")
		}
	}
	inventorySkills, _ := asArray(inventory["skills"])
	for _, skillValue := range inventorySkills {
		skill, ok := asObject(skillValue)
		if !ok {
			continue
		}
		path, _ := asString(skill["path"])
		digest, _ := asString(skill["sha256"])
		actual, err := fileHash(path)
		if err != nil {
			return ProfiledCodexResult{}, err
		}
		if actual != digest {
			return ProfiledCodexResult{}, blocked("cached Codex skill instructions changed.")
		}
	}
	disabled, err := readJSONObjectFile(filepath.Join(directory, "disabled.json"))
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	if err := assertSkillDenialFromRaw(inventory["skills"], disabled["skills"]); err != nil {
		return ProfiledCodexResult{}, err
	}
	if process.Stdout != filepath.Join(directory, "stdout.txt") || process.Executable != profile.Executable || process.StopReason != "" {
		return ProfiledCodexResult{}, blocked("invalid Codex process receipt.")
	}
	if critic {
		for _, catalogPath := range []string{
			filepath.Join(directory, "critic-catalog.json"),
			filepath.Join(config, "critic-catalog.json"),
		} {
			expected, _ := asString(binding["critic_catalog_sha256"])
			digest, err := fileHash(catalogPath)
			if err != nil {
				return ProfiledCodexResult{}, err
			}
			if digest != expected {
				return ProfiledCodexResult{}, blocked("retained critic catalog bytes changed.")
			}
		}
		expectedSource, _ := asString(binding["critic_catalog_source_sha256"])
		digest, err := fileHash(filepath.Join(directory, "critic-catalog-source.json"))
		if err != nil {
			return ProfiledCodexResult{}, err
		}
		if digest != expectedSource {
			return ProfiledCodexResult{}, blocked("retained critic source catalog bytes changed.")
		}
	}
	allowed := []string{}
	if !critic && profile.Toolset.Name == "unica" {
		allowed = append([]string(nil), profile.Unica.AllowedTools...)
	}
	expectedMcp := make([]any, 0, len(allowed))
	for _, tool := range allowed {
		expectedMcp = append(expectedMcp, map[string]any{"server": "unica", "name": tool})
	}
	mcpInventory, err := readJSONObjectFile(filepath.Join(directory, "mcp-inventory.json"))
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	mcpTools, _ := asArray(mcpInventory["tools"])
	if same, err := hashValueEqual(sortToolInventory(mcpTools), sortToolInventory(expectedMcp)); err != nil {
		return ProfiledCodexResult{}, err
	} else if !same {
		return ProfiledCodexResult{}, blocked("cached MCP inventory differs from the exact allowlist.")
	}
	parsed, err := ReadProfiledCodexEvents(process.Stdout, process.ExitCode, allowed, critic)
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	result, err := readJSONObjectFile(resultPath)
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	if _, err := assertFields(result, []string{"schema_version", "status", "summary", "payload_json"}, nil, "worker_result"); err != nil {
		return ProfiledCodexResult{}, err
	}
	status, _ := asString(result["status"])
	schemaVersion := asInt64OrZero(result["schema_version"])
	if schemaVersion != 1 || !isTerminalStatus(status) {
		return ProfiledCodexResult{}, blocked("invalid Codex result.")
	}
	if err := assertTextValue(result["summary"], "worker summary"); err != nil {
		return ProfiledCodexResult{}, err
	}
	payloadJSON, isString := asString(result["payload_json"])
	if !isString {
		return ProfiledCodexResult{}, blocked("Codex payload_json must be a JSON string.")
	}
	if !isValidJSONValue(payloadJSON) {
		return ProfiledCodexResult{}, blocked("invalid Codex payload JSON.")
	}
	// The result object must equal the raw session evidence byte-for-byte
	// under the canonical encoder (ProfiledCodex.ps1:354).
	finalObject, finalErr := parseJSONObject([]byte(parsed.Final))
	if finalErr != nil {
		return ProfiledCodexResult{}, blocked("Codex result differs from raw session evidence.")
	}
	if same, err := hashValueEqual(finalObject, result); err != nil {
		return ProfiledCodexResult{}, err
	} else if !same {
		return ProfiledCodexResult{}, blocked("Codex result differs from raw session evidence.")
	}
	current, err := req.Dependencies()
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	if same, err := hashValueEqual(current, dependencies); err != nil {
		return ProfiledCodexResult{}, err
	} else if !same {
		return ProfiledCodexResult{}, blocked("managed inputs changed during execution.")
	}
	// Controller-observed identity comes from the rollout session file the
	// host persisted for this exact session (ProfiledCodex.ps1:356-367).
	codexHome := req.CodexHome
	if strings.TrimSpace(codexHome) == "" {
		codexHome = os.Getenv("CODEX_HOME")
	}
	observed := ObservedIdentity{SessionID: parsed.SessionID}
	if requireObserved {
		observed, err = ObservedModelEffort(parsed.SessionID, "", codexHome)
		if err != nil {
			return ProfiledCodexResult{}, err
		}
		if observed.TurnID == "" {
			return ProfiledCodexResult{}, blocked("required host identity has no persisted rollout turn.")
		}
		selected, err := ObservedModelEffort(parsed.SessionID, observed.TurnID, codexHome)
		if err != nil {
			return ProfiledCodexResult{}, err
		}
		if selected.RolloutPath != observed.RolloutPath || selected.ObservedModel != observed.ObservedModel ||
			selected.ObservedEffort != observed.ObservedEffort {
			return ProfiledCodexResult{}, blocked("persisted rollout turn identity changed while reading the strict receipt.")
		}
		if observed.ObservedModel != model || observed.ObservedEffort != effort {
			return ProfiledCodexResult{}, blocked("observed rollout model/effort differs from the strict request.")
		}
	}
	stdoutSHA256, err := fileHash(process.Stdout)
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	resultSHA256, err := fileHash(resultPath)
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	capabilitySHA256, err := fileHash(filepath.Join(directory, "capability", "capability.json"))
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	mcpNamesSHA256, err := fileHash(filepath.Join(directory, "mcp-config-names.json"))
	if err != nil {
		return ProfiledCodexResult{}, err
	}
	metadata := map[string]any{
		"session_id":              parsed.SessionID,
		"requested_model":         model,
		"requested_effort":        nilIfEmpty(effort),
		"observed_model":          nilIfEmpty(observed.ObservedModel),
		"observed_effort":         nilIfEmpty(observed.ObservedEffort),
		"usage":                   canonicalUsage(parsed.Usage),
		"usage_source":            process.Stdout,
		"binding_sha256":          bindingHash,
		"stdout_sha256":           stdoutSHA256,
		"result_sha256":           resultSHA256,
		"capability_sha256":       capabilitySHA256,
		"mcp_config_names_sha256": mcpNamesSHA256,
	}
	if requireObserved {
		metadata["rollout_path"] = observed.RolloutPath
		metadata["turn_id"] = observed.TurnID
	}
	if fileLeafExists(hostPath) {
		saved, err := readJSONObjectFile(hostPath)
		if err != nil {
			return ProfiledCodexResult{}, err
		}
		if same, err := hashValueEqual(saved, metadata); err != nil {
			return ProfiledCodexResult{}, err
		} else if !same {
			return ProfiledCodexResult{}, blocked("cached Codex receipt differs from raw evidence.")
		}
	} else if err := writeJSONFile(hostPath, metadata, false); err != nil {
		return ProfiledCodexResult{}, err
	}
	return ProfiledCodexResult{
		Status:          status,
		Summary:         stringOfValue(result["summary"]),
		PayloadJSON:     payloadJSON,
		Raw:             result,
		HostResult:      metadata,
		SessionID:       parsed.SessionID,
		Usage:           parsed.TypedUsage(),
		RequestedModel:  model,
		RequestedEffort: effort,
		ObservedModel:   observed.ObservedModel,
		ObservedEffort:  observed.ObservedEffort,
		RolloutPath:     observed.RolloutPath,
		TurnID:          observed.TurnID,
		Process:         process,
		StdoutPath:      process.Stdout,
		ResultPath:      resultPath,
		HostResultPath:  hostPath,
	}, nil
}

// dispatchProfiledCodex is the fresh-dispatch half of the worker loop
// (ProfiledCodex.ps1:267-335).
func dispatchProfiledCodex(ctx context.Context, req ProfiledCodexRequest, profile ExecutionProfile,
	directory, scratch, config, hostRoot string, binding map[string]any, bindingHash, permissions string,
	criticCatalog CriticCatalog, critic bool, model, effort string, requireObserved bool, inputText string) (ProcessResult, error) {
	for _, path := range []string{directory, scratch, config} {
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
	if err := copyFile(req.SchemaPath, filepath.Join(config, "result.schema.json")); err != nil {
		return ProcessResult{}, blocked("%v", err)
	}
	overrides := ProfiledCodexOverrides(permissions, filepath.Join(scratch, "logs"))
	if critic {
		if err := writeJSONFile(filepath.Join(directory, "critic-catalog-source.json"), criticCatalog.Source, false); err != nil {
			return ProcessResult{}, err
		}
		if err := writeJSONFile(filepath.Join(directory, "critic-catalog.json"), criticCatalog.Catalog, false); err != nil {
			return ProcessResult{}, err
		}
		if err := writeJSONFile(filepath.Join(config, "critic-catalog.json"), criticCatalog.Catalog, false); err != nil {
			return ProcessResult{}, err
		}
		overrides = append(overrides, CriticOverrides(filepath.Join(config, "critic-catalog.json"))...)
	}
	workerOverrides := append([]string(nil), overrides...)
	rpcRequest := func(method, attemptDirectory string, params map[string]any, extraOverrides []string, guard bool, expected, enabled []string) CodexRPCRequest {
		return CodexRPCRequest{
			Executable:         profile.Executable,
			Overrides:          extraOverrides,
			WorkingDirectory:   req.WorkerPath,
			Directory:          attemptDirectory,
			Method:             method,
			Params:             params,
			MaxOutputBytes:     req.MaxOutputBytes,
			Deadline:           req.Deadline,
			GuardMcp:           guard,
			ExpectedMcpServers: expected,
			EnabledMcpServers:  enabled,
		}
	}
	// config/read resolves configuration only; unlike MCP status, it does not
	// initialize MCP transports. Never persist the returned configuration.
	configuration, err := CodexReadOnlyRPC(ctx, rpcRequest(RPCConfigRead, filepath.Join(directory, "mcp-config-rpc"),
		map[string]any{"cwd": req.WorkerPath, "includeLayers": false}, overrides, false, nil, nil))
	if err != nil {
		return ProcessResult{}, err
	}
	mcpNames, err := CodexMcpServerNames(configuration)
	if err != nil {
		return ProcessResult{}, err
	}
	configuration = nil
	unica := !critic && profile.Toolset.Name == "unica"
	if unica && containsString(mcpNames, "unica") {
		return ProcessResult{}, blocked("global MCP alias unica collides with the managed registration.")
	}
	namesValue := any(nil)
	if mcpNames != nil {
		namesValue = mcpNames
	}
	if err := writeJSONFile(filepath.Join(directory, "mcp-config-names.json"), map[string]any{"names": namesValue}, false); err != nil {
		return ProcessResult{}, err
	}
	denyOverrides, err := CodexMcpDenyOverrides(mcpNames)
	if err != nil {
		return ProcessResult{}, err
	}
	overrides = append(overrides, denyOverrides...)
	// Recheck config before any inventory that can start a transport. This
	// detects observed drift; it does not lock concurrent global config edits.
	skillsParams := map[string]any{"cwds": []any{req.WorkerPath}, "forceReload": true}
	response, err := CodexReadOnlyRPC(ctx, rpcRequest(RPCSkillsList, filepath.Join(directory, "inventory-rpc"),
		skillsParams, overrides, true, mcpNames, nil))
	if err != nil {
		return ProcessResult{}, err
	}
	inventory, err := CodexSkillInventory(response, req.WorkerPath)
	if err != nil {
		return ProcessResult{}, err
	}
	if err := writeJSONFile(filepath.Join(directory, "inventory.json"), map[string]any{"skills": inventoryAsValue(inventory)}, false); err != nil {
		return ProcessResult{}, err
	}
	inventoryDigest, err := hashValue(inventoryAsValue(inventory))
	if err != nil {
		return ProcessResult{}, err
	}
	if inventoryDigest != profile.CodexSkillsSHA256 {
		return ProcessResult{}, blocked("discovered Codex skills differ from the registered inventory.")
	}
	managedOverrides := append(append([]string(nil), overrides...), "-c", CodexSkillDenyOverride(inventory))
	workerOverrides = append(workerOverrides, "-c", CodexSkillDenyOverride(inventory))
	response, err = CodexReadOnlyRPC(ctx, rpcRequest(RPCSkillsList, filepath.Join(directory, "disabled-rpc"),
		skillsParams, managedOverrides, true, mcpNames, nil))
	if err != nil {
		return ProcessResult{}, err
	}
	disabled, err := CodexSkillInventory(response, req.WorkerPath)
	if err != nil {
		return ProcessResult{}, err
	}
	if err := AssertCodexSkillDenial(inventory, disabled); err != nil {
		return ProcessResult{}, err
	}
	if err := writeJSONFile(filepath.Join(directory, "disabled.json"), map[string]any{"skills": inventoryAsValue(disabled)}, false); err != nil {
		return ProcessResult{}, err
	}
	if req.TestExecutionCapability == nil {
		return ProcessResult{}, invalid("execution capability probe is required.")
	}
	if err := req.TestExecutionCapability(filepath.Join(directory, "capability"), scratch, config, permissions, req.Stage == StageImplement); err != nil {
		return ProcessResult{}, err
	}
	if unica {
		bootstrap := filepath.Join(profile.Unica.PluginRoot, "bootstrap", "bin", "win-x64", "unica-bootstrap.exe")
		mcpArguments := []string{
			"sandbox", "-P", "bsl_execution", "-c", permissions, "-c", `windows.sandbox="elevated"`,
			"-C", req.WorkerPath, bootstrap, "run", "--plugin-root", profile.Unica.PluginRoot,
		}
		values := make([]string, 0, len(mcpArguments))
		for _, argument := range mcpArguments {
			values = append(values, jsonQuote(argument))
		}
		tools := make([]string, 0, len(profile.Unica.AllowedTools))
		for _, tool := range profile.Unica.AllowedTools {
			tools = append(tools, jsonQuote(tool))
		}
		registration := `mcp_servers={unica={enabled=true,command=` + jsonQuote(forwardSlash(profile.Sandbox.Executable)) +
			`,args=[` + strings.Join(values, ",") + `],env={UNICA_RUNTIME_CACHE_DIR=` + jsonQuote(forwardSlash(profile.Unica.RuntimeCache)) +
			`},enabled_tools=[` + strings.Join(tools, ",") + `],startup_timeout_sec=45,tool_timeout_sec=60}}`
		// The whole-table registration replaces preceding dotted CLI entries.
		// Reapply global denials after it; exec ignores global config entirely.
		managedOverrides = append(managedOverrides, "-c", registration)
		managedOverrides = append(managedOverrides, denyOverrides...)
		workerOverrides = append(workerOverrides, "-c", registration)
	}
	mcpExpected := append([]string(nil), mcpNames...)
	mcpEnabled := []string{}
	if unica {
		mcpExpected = append(mcpExpected, "unica")
		mcpEnabled = append(mcpEnabled, "unica")
	}
	mcp, err := CodexReadOnlyRPC(ctx, rpcRequest(RPCMcpServerStatusGet, filepath.Join(directory, "mcp-rpc"),
		map[string]any{"limit": json.Number("100"), "detail": "toolsAndAuthOnly"}, managedOverrides, true, mcpExpected, mcpEnabled))
	if err != nil {
		return ProcessResult{}, err
	}
	mcpObject, _ := asObject(mcp)
	if value := getValue(mcpObject, "nextCursor", nil); value != nil {
		return ProcessResult{}, blocked("incomplete MCP inventory.")
	}
	actual := make([]any, 0, 8)
	mcpData, _ := asArray(getValue(mcpObject, "data", nil))
	for _, serverValue := range mcpData {
		server, ok := asObject(serverValue)
		if !ok {
			continue
		}
		serverName, _ := asString(server["name"])
		tools, _ := asObject(server["tools"])
		for _, toolValue := range tools {
			tool, ok := asObject(toolValue)
			if !ok {
				continue
			}
			toolName, _ := asString(tool["name"])
			actual = append(actual, map[string]any{"server": serverName, "name": toolName})
		}
	}
	expectedTools := make([]any, 0, 8)
	if unica {
		for _, tool := range profile.Unica.AllowedTools {
			expectedTools = append(expectedTools, map[string]any{"server": "unica", "name": tool})
		}
	}
	if same, err := hashValueEqual(sortToolInventory(actual), sortToolInventory(expectedTools)); err != nil {
		return ProcessResult{}, err
	} else if !same {
		return ProcessResult{}, blocked("Codex MCP tools differ from the exact registered allowlist.")
	}
	if err := writeJSONFile(filepath.Join(directory, "mcp-inventory.json"), map[string]any{"tools": actual}, false); err != nil {
		return ProcessResult{}, err
	}
	nativeResult := filepath.Join(scratch, "model-result.json")
	ephemeral := []string{}
	if !requireObserved {
		ephemeral = []string{"--ephemeral"}
	}
	arguments := []string{"exec", "--ignore-user-config", "--ignore-rules"}
	arguments = append(arguments, ephemeral...)
	arguments = append(arguments,
		"--skip-git-repo-check",
		"--model", model,
		"-c", "model_reasoning_effort="+jsonQuote(effort),
	)
	arguments = append(arguments, workerOverrides...)
	arguments = append(arguments,
		"--json",
		"--output-schema", filepath.Join(config, "result.schema.json"),
		"--output-last-message", nativeResult,
		"--cd", req.WorkerPath,
		"-",
	)
	configuration, err = CodexReadOnlyRPC(ctx, rpcRequest(RPCConfigRead, filepath.Join(directory, "pre-dispatch-config-rpc"),
		map[string]any{"cwd": req.WorkerPath, "includeLayers": false}, overrides, true, mcpNames, nil))
	if err != nil {
		return ProcessResult{}, err
	}
	if err := AssertCodexMcpConfiguration(configuration, mcpNames, nil); err != nil {
		return ProcessResult{}, err
	}
	configuration = nil
	if req.Dependencies == nil {
		return ProcessResult{}, invalid("execution dependency source is required.")
	}
	currentDependencies, err := req.Dependencies()
	if err != nil {
		return ProcessResult{}, err
	}
	// The dispatch-time comparison re-hashes the freshly captured value
	// against the value bound at entry (ProfiledCodex.ps1:325).
	if same, err := hashValueEqual(currentDependencies, binding["dependencies"]); err != nil {
		return ProcessResult{}, err
	} else if !same {
		return ProcessResult{}, blocked("managed inputs changed before dispatch.")
	}
	if critic {
		expected, _ := asString(binding["critic_catalog_sha256"])
		digest, err := fileHash(filepath.Join(config, "critic-catalog.json"))
		if err != nil {
			return ProcessResult{}, err
		}
		if digest != expected {
			return ProcessResult{}, blocked("critic host catalog changed before dispatch.")
		}
	}
	environment := map[string]string{
		"TEMP":               scratch,
		"TMP":                scratch,
		"GIT_OPTIONAL_LOCKS": "0",
	}
	codexHome := req.CodexHome
	if strings.TrimSpace(codexHome) == "" {
		codexHome = os.Getenv("CODEX_HOME")
	}
	if codexHome != "" {
		environment["CODEX_HOME"] = codexHome
	}
	timeoutSeconds := req.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 1800
	}
	process, err := RunManagedProcess(ctx, ProcessOptions{
		Executable:       profile.Executable,
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
	if process.StopReason != "" || process.ExitCode != 0 {
		return ProcessResult{}, blocked("Codex attempt failed; preserve sources and reconcile without retry.")
	}
	if !fileLeafExists(nativeResult) {
		return ProcessResult{}, blocked("Codex omitted the result artifact.")
	}
	if err := copyFile(nativeResult, filepath.Join(directory, "model-result.json")); err != nil {
		return ProcessResult{}, blocked("%v", err)
	}
	response, err = CodexReadOnlyRPC(ctx, rpcRequest(RPCSkillsList, filepath.Join(directory, "post-inventory-rpc"),
		skillsParams, overrides, true, mcpNames, nil))
	if err != nil {
		return ProcessResult{}, err
	}
	postInventory, err := CodexSkillInventory(response, req.WorkerPath)
	if err != nil {
		return ProcessResult{}, err
	}
	if err := writeJSONFile(filepath.Join(directory, "post-inventory.json"), map[string]any{"skills": inventoryAsValue(postInventory)}, false); err != nil {
		return ProcessResult{}, err
	}
	return process, nil
}

// processFromExitReceipt rebuilds the immutable exit receipt of a preserved
// attempt (Read-BFJson $exitPath).
func processFromExitReceipt(exitPath string) (ProcessResult, error) {
	receipt, err := readJSONObjectFile(exitPath)
	if err != nil {
		return ProcessResult{}, err
	}
	if _, err := assertFields(receipt,
		[]string{"exit_code", "stop_reason", "elapsed_seconds", "process_id", "executable", "stdout", "stderr"},
		nil, "process_exit"); err != nil {
		return ProcessResult{}, err
	}
	process := ProcessResult{
		ExitCode:       int(asInt64OrZero(receipt["exit_code"])),
		ElapsedSeconds: asFloat64OrZero(receipt["elapsed_seconds"]),
		ProcessID:      int(asInt64OrZero(receipt["process_id"])),
	}
	process.StopReason, _ = asString(receipt["stop_reason"])
	process.Executable, _ = asString(receipt["executable"])
	process.Stdout, _ = asString(receipt["stdout"])
	process.Stderr, _ = asString(receipt["stderr"])
	return process, nil
}

func asInt64OrZero(value any) int64 {
	number, ok := asInt64(value)
	if !ok {
		return 0
	}
	return number
}

func asFloat64OrZero(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int64:
		return float64(typed)
	}
	return 0
}

// isTerminalStatus mirrors the closed worker-result status vocabulary
// (ProfiledCodex.ps1:350).
func isTerminalStatus(status string) bool {
	switch status {
	case StatusCompleted, StatusNeedsInput, StatusBlocked, StatusFailed:
		return true
	}
	return false
}

// isValidJSONValue mirrors ConvertFrom-Json materialization of the payload
// string: any valid JSON document (scalars included) is accepted.
func isValidJSONValue(text string) bool {
	_, err := parseJSONScalarDocument([]byte(text))
	return err == nil
}

// sortToolInventory orders {server,name} entries like
// Sort-Object server,name before the exact hash comparison
// (ProfiledCodex.ps1:318,346).
func sortToolInventory(tools []any) []any {
	sorted := append([]any(nil), tools...)
	sort.SliceStable(sorted, func(left, right int) bool {
		leftTool, _ := asObject(sorted[left])
		rightTool, _ := asObject(sorted[right])
		leftServer, _ := asString(leftTool["server"])
		rightServer, _ := asString(rightTool["server"])
		if leftServer != rightServer {
			return leftServer < rightServer
		}
		leftName, _ := asString(leftTool["name"])
		rightName, _ := asString(rightTool["name"])
		return leftName < rightName
	})
	return sorted
}

// inventoryAsValue renders an inventory as the canonical-comparable value.
func inventoryAsValue(inventory []map[string]any) any {
	if inventory == nil {
		return nil
	}
	values := make([]any, 0, len(inventory))
	for _, skill := range inventory {
		values = append(values, skill)
	}
	return values
}

// assertSkillDenialFromRaw applies AssertCodexSkillDenial to parsed receipt
// arrays (cached attempt validation, ProfiledCodex.ps1:338).
func assertSkillDenialFromRaw(inventoryValue, disabledValue any) error {
	inventoryRaw, _ := asArray(inventoryValue)
	disabledRaw, _ := asArray(disabledValue)
	inventory := make([]map[string]any, 0, len(inventoryRaw))
	for _, skill := range inventoryRaw {
		if object, ok := asObject(skill); ok {
			inventory = append(inventory, object)
		}
	}
	disabled := make([]map[string]any, 0, len(disabledRaw))
	for _, skill := range disabledRaw {
		if object, ok := asObject(skill); ok {
			disabled = append(disabled, object)
		}
	}
	return AssertCodexSkillDenial(inventory, disabled)
}

// canonicalUsage keeps the raw usage object for receipt parity while ensuring
// a nil map canonicalizes as null.
func canonicalUsage(usage map[string]any) any {
	if usage == nil {
		return nil
	}
	return usage
}

// hasPrefixFold mirrors StartsWith with StringComparison.OrdinalIgnoreCase.
func hasPrefixFold(value, prefix string) bool {
	if len(prefix) > len(value) {
		return false
	}
	return strings.EqualFold(value[:len(prefix)], prefix)
}

// copyFile mirrors Copy-Item for byte-exact artifact copies.
func copyFile(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o644)
}
