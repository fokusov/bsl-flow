package stagehost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bsl-flow/cli/internal/councilengine"
	"bsl-flow/cli/internal/counciltransport"
	"bsl-flow/cli/internal/repository"
	"bsl-flow/cli/internal/specvalidate"
	"bsl-flow/cli/internal/worker"
)

// This file wires the native council engine into the managed spec_review
// stage: the direct API and current-agent dispatchers, the inspect+architecture
// evidence bundle, the deterministic final-validation seam and the council
// route of Invoke-BFProfileSpecCritic. The engine itself lives in
// cli/internal/councilengine; everything here supplies host seams.

// councilDispatcher implements councilengine.Dispatcher for the managed host:
// direct_api through counciltransport, current_agent_fallback through the
// existing managed worker with strict observed identity.
type councilDispatcher struct {
	runRoot     string
	maxOutput   int64
	clientFor   func(binding *councilengine.Binding, timeout int) counciltransport.Client
	fallbackFor func(ctx context.Context, attempt *councilengine.Attempt, prompt string, route *councilengine.Route, timeout int) (councilengine.DispatchResult, error)
}

func (d *councilDispatcher) Dispatch(ctx context.Context, attempt *councilengine.Attempt, prompt string, route *councilengine.Route, timeoutSeconds int) (councilengine.DispatchResult, error) {
	switch route.Route {
	case "current_agent_fallback":
		return d.fallbackFor(ctx, attempt, prompt, route, timeoutSeconds)
	default:
		return d.directAPIDispatch(ctx, attempt, prompt, route, timeoutSeconds)
	}
}

func (d *councilDispatcher) directAPIDispatch(ctx context.Context, attempt *councilengine.Attempt, prompt string, route *councilengine.Route, timeoutSeconds int) (councilengine.DispatchResult, error) {
	binding := attempt.Binding
	attemptDir := filepath.Join(d.runRoot, attempt.Role, fmt.Sprintf("dispatch-%d", attempt.Sequence))
	if err := os.MkdirAll(attemptDir, 0o755); err != nil {
		return councilengine.DispatchResult{}, councilblocked("%v", err)
	}
	client := d.clientFor(binding, timeoutSeconds)
	request := counciltransport.Request{Model: binding.Model, Prompt: prompt, Effort: binding.Effort}
	response, err := client.Chat(ctx, request, counciltransport.Credentials{APIKey: route.Credential})
	if err != nil {
		writeCouncilTransportFiles(attemptDir, binding, response, nil, err, timeoutSeconds, d.maxOutput)
		return councilengine.DispatchResult{}, classifyTransportError(err)
	}
	payload, err := councilengine.ParseCouncilPayload(response.Content)
	if err != nil {
		writeCouncilTransportFiles(attemptDir, binding, response, nil, err, timeoutSeconds, d.maxOutput)
		return councilengine.DispatchResult{}, err
	}
	writeCouncilTransportFiles(attemptDir, binding, response, payload, nil, timeoutSeconds, d.maxOutput)
	usage := &councilengine.MemberUsage{
		InputTokens:     int64Ptr(response.Usage.PromptTokens),
		OutputTokens:    int64Ptr(response.Usage.CompletionTokens),
		ReasoningTokens: int64Ptr(response.Usage.ReasoningTokens),
	}
	provider := binding.Provider
	model := response.ObservedModel
	return councilengine.DispatchResult{
		Status:        "completed",
		Payload:       payload,
		Observed:      councilengine.Observed{Provider: &provider, Model: &model, Effort: nil},
		Usage:         usage,
		ExecutionMode: "direct_api",
	}, nil
}

func int64Ptr(value int) *int64 {
	converted := int64(value)
	return &converted
}

// classifyTransportError maps counciltransport errors onto the BF_* dispatch
// markers the engine classifies into terminal member states.
func classifyTransportError(err error) error {
	switch {
	case errors.Is(err, counciltransport.ErrBeforeDispatch):
		return fmt.Errorf("%s: %s", councilengine.MarkerNotDispatched, err)
	case errors.Is(err, counciltransport.ErrRedirectRefused):
		return fmt.Errorf("%s: council redirect was refused.", councilengine.MarkerBeforeAcceptance)
	case errors.Is(err, counciltransport.ErrInvalidResponse):
		return fmt.Errorf("%s: %s", councilengine.MarkerInvalidResponse, err)
	case errors.Is(err, counciltransport.ErrResponseBound):
		return fmt.Errorf("%s: council response exceeds the output limit.", councilengine.MarkerInvalidResponse)
	case errors.Is(err, counciltransport.ErrUnknownAfterDispatch):
		return fmt.Errorf("BF_UNKNOWN_AFTER_DISPATCH: %s", err)
	}
	var statusErr *counciltransport.ProviderStatusError
	if errors.As(err, &statusErr) {
		if statusErr.Code >= 500 {
			return fmt.Errorf("BF_UNKNOWN_AFTER_DISPATCH: council provider returned a server error.")
		}
		return fmt.Errorf("%s: council request was not accepted by the provider.", councilengine.MarkerBeforeAcceptance)
	}
	return fmt.Errorf("BF_UNKNOWN_AFTER_DISPATCH: council transport failed after dispatch: %s", err)
}

// writeCouncilTransportFiles persists the sanitized transport evidence into
// the ignored attempt directory (request-meta, raw-response, model-text,
// diagnostic). These files are audit evidence, not acceptance contracts.
func writeCouncilTransportFiles(attemptDir string, binding *councilengine.Binding, response counciltransport.Response, payload any, transportErr error, timeout int, maxOutput int64) {
	requestMeta := map[string]any{
		"provider": binding.Provider, "model": binding.Model, "effort": binding.Effort,
		"protocol": binding.Protocol,
		"endpoint": map[string]any{
			"scheme": binding.Endpoint.Scheme, "host": binding.Endpoint.Host,
			"port": binding.Endpoint.Port, "base_path": binding.Endpoint.BasePath,
		},
		"transport_capability_version": binding.TransportCapabilityVersion,
		"prompt_version":               binding.PromptVersion,
		"member_schema_version":        binding.MemberSchemaVersion,
	}
	writeJSON(filepath.Join(attemptDir, "request-meta.json"), requestMeta, true)
	if response.Content != "" {
		if err := os.WriteFile(filepath.Join(attemptDir, "model-text.txt"), []byte(response.Content), 0o644); err == nil {
			// raw-response carries the same text for the chat route.
			_ = os.WriteFile(filepath.Join(attemptDir, "raw-response.txt"), []byte(response.Content), 0o644)
		}
	}
	if transportErr != nil {
		writeJSON(filepath.Join(attemptDir, "diagnostic.json"), map[string]any{
			"schema_version":   1,
			"code":             transportMarker(transportErr),
			"message":          redactSecrets(transportErr.Error(), binding),
			"status":           0,
			"dispatch_started": true,
			"input_bytes":      0,
			"output_bytes":     0,
			"max_output_bytes": maxOutput,
			"timestamp_utc":    time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z"),
		}, true)
	}
	_ = payload
	_ = timeout
}

func transportMarker(err error) string {
	text := err.Error()
	for _, marker := range []string{councilengine.MarkerInvalidResponse, councilengine.MarkerBeforeAcceptance, councilengine.MarkerNotDispatched} {
		if strings.Contains(text, marker) {
			return marker
		}
	}
	return "BF_UNKNOWN_AFTER_DISPATCH"
}

func redactSecrets(text string, binding *councilengine.Binding) string {
	// The binding never carries a token; the transport already redacts the
	// credential. Keep the diagnostic projection minimal.
	return text
}

// councilFinalValidation is the Engine.FinalValidation seam: run the spec
// lint, the deterministic council gate, write final-validation.json (schema v2)
// and return the receipt. A non-passing result is an error.
func councilFinalValidation(changeDir, projectPath string) (map[string]any, error) {
	reviewPath := filepath.Join(changeDir, "review.json")
	reviewBytes, err := repository.StageHostReadFileBytes(reviewPath)
	if err != nil {
		return nil, blockedf("Final spec lint could not run: %v", err)
	}
	review, err := councilengine.ParseCouncilPayload(string(reviewBytes))
	if err != nil {
		return nil, blockedf("Final spec lint could not run: %v", err)
	}
	specPath := filepath.Join(changeDir, "spec.md")
	specBytes, err := repository.StageHostReadFileBytes(specPath)
	if err != nil {
		return nil, blockedf("Final spec lint could not run: %v", err)
	}
	lintFindings, lintErr := specvalidate.LintSpec(specBytes)
	if lintErr != nil {
		return nil, blockedf("Final spec lint could not run: %v", lintErr)
	}
	lintPassed := true
	for _, finding := range lintFindings {
		if finding.Severity == "error" {
			lintPassed = false
			break
		}
	}
	var policyHash *string
	if isRegularFile(filepath.Join(projectPath, "bsl-flow.yaml")) {
		data, err := repository.StageHostReadFileBytes(filepath.Join(projectPath, "bsl-flow.yaml"))
		if err == nil {
			hash := hashFileBytes(data)
			policyHash = &hash
		}
	}
	gate, err := councilengine.TestCouncilFinalGate(councilengine.FinalGateInputs{
		Review:           review,
		OriginalTaskPath: filepath.Join(changeDir, "original-task.md"),
		SpecPath:         specPath,
		DesignPath:       filepath.Join(changeDir, "design.md"),
		LintPassed:       lintPassed,
		PolicyHash:       policyHash,
	})
	if err != nil {
		return nil, blockedf("Final specification invariant validation failed: %v", err)
	}
	chair, _ := councilengine.AsOrdered(review.Get("chair"))
	_ = chair
	verdict := councilReviewVerdict(review)
	diversity := councilReviewDiversity(review)
	inputs := map[string]any{
		"review_sha256":         nullableFileHash2(filepath.Join(changeDir, "review.json")),
		"reconciliation_sha256": nullableFileHash2(filepath.Join(changeDir, "review-reconciliation.json")),
		"final_spec_sha256":     nullableFileHash2(specPath),
		"final_design_sha256":   nullableFileHash2(filepath.Join(changeDir, "design.md")),
		"original_task_sha256":  nullableFileHash2(filepath.Join(changeDir, "original-task.md")),
	}
	errorsList := gate.Errors
	if errorsList == nil {
		errorsList = []string{}
	}
	receipt := map[string]any{
		"schema_version": int64(2),
		"checked_at_utc": time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z"),
		"passed":         gate.Passed,
		"review_schema":  int64(2),
		"verdict":        verdict,
		"diversity":      diversity,
		"inputs":         inputs,
		"errors":         toAny(errorsList),
	}
	if err := writeJSON(filepath.Join(changeDir, "final-validation.json"), receipt, true); err != nil {
		return nil, err
	}
	if !gate.Passed {
		return receipt, blockedf("Final specification invariant validation failed. See: %s", filepath.Join(changeDir, "final-validation.json"))
	}
	return receipt, nil
}

func toAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func nullableFileHash2(path string) any {
	if !isRegularFile(path) {
		return nil
	}
	data, err := repository.StageHostReadFileBytes(path)
	if err != nil {
		return nil
	}
	return hashFileBytes(data)
}

func councilReviewVerdict(review *councilengine.Ordered) string {
	if review == nil {
		return ""
	}
	if verdict, ok := review.Get("verdict").(string); ok {
		return verdict
	}
	return ""
}

func councilReviewDiversity(review *councilengine.Ordered) string {
	if review == nil {
		return ""
	}
	if diversity, ok := review.Get("diversity").(string); ok {
		return diversity
	}
	return ""
}

// councilEvidence builds the bounded inspect+architecture evidence text
// (Get-BFManagedCouncilEvidence) that the council snapshot hashes.
func councilEvidence(deps Deps, state map[string]any, maxBytes int) (string, error) {
	project := asStringOr(state["project_path"])
	taskID := asStringOr(state["task_id"])
	entries, _ := asArray(state["evidence"])
	var inspectEntry map[string]any
	for _, raw := range entries {
		entry, _ := asObject(raw)
		if asStringOr(entry["stage"]) == "inspect" && asStringOr(entry["outcome"]) == "PASS" {
			inspectEntry = entry
		}
	}
	attemptID := ""
	resultSHA := ""
	outcome := "missing"
	dependencies := any(nil)
	rawHashes := []any{}
	proposal := any(nil)
	missingContext := []any{}
	if inspectEntry != nil {
		attemptID = asStringOr(inspectEntry["attempt_id"])
		if err := assertUUID(attemptID); err != nil {
			return "", blockedf("inspect evidence has an invalid attempt identity.")
		}
		resultSHA = asStringOr(inspectEntry["result_sha256"])
		outcome = "PASS"
		dependencies = inspectEntry["dependencies"]
		rawHashes = anyItemsOf(inspectEntry["raw_hashes"])
		proposal = asMap(inspectEntry["proposal"])
	} else {
		missingContext = append(missingContext, "verified inspect evidence")
	}
	packageRoot, err := repository.StageHostPackageRootOfSkillsRoot(deps.SkillsRoot)
	if err != nil {
		return "", err
	}
	architectureRoot, err := repository.StageHostArchitectureContextRoot(project, packageRoot)
	if err != nil {
		return "", err
	}
	architecture, err := repository.StageHostArchitectureBundleContent("spec_review", architectureRoot, packageRoot)
	if err != nil {
		return "", err
	}
	bundle := map[string]any{
		"schema_version":  int64(2),
		"source":          "bsl-flow.inspect+architecture",
		"task_id":         taskID,
		"attempt_id":      attemptID,
		"outcome":         outcome,
		"result_sha256":   resultSHA,
		"dependencies":    dependencies,
		"raw_hashes":      rawHashes,
		"proposal":        proposal,
		"missing_context": missingContext,
		"architecture":    architecture,
	}
	text, err := repository.StageHostCanonical(bundle)
	if err != nil {
		return "", invalidf("%v", err)
	}
	if len(text) > maxBytes {
		return "", blockedf("verified inspect evidence exceeds the council input bound.")
	}
	return string(text), nil
}

func councilblocked(format string, args ...any) error {
	return fmt.Errorf("BF_BLOCKED: "+format, args...)
}

// invokeCouncilReview runs the native council cycle for the managed
// spec_review stage, returning the assembled review v2 object.
func invokeCouncilReview(ctx context.Context, deps Deps, run *stageRun, raw, change, configText string, maxInput int, policy *councilengine.CouncilPolicy) (*councilengine.Ordered, error) {
	state := run.state
	projectRoot := asStringOr(state["project_path"])
	changeName := filepath.Base(change)
	runRoot := councilengine.RunRoot(projectRoot, changeName)
	engine := &councilengine.Engine{
		Now: func() time.Time { return deps.now() },
		FinalValidation: func(changeDir string) (map[string]any, error) {
			return councilFinalValidation(changeDir, projectRoot)
		},
	}
	// A prepared council publication is a durable recovery record: finish it
	// before creating any new attempt.
	recovered, err := engine.ResumePreparedPublicationIfPresent(projectRoot, changeName)
	if err != nil {
		return nil, blockedf("%s", unwrapMessage(err))
	}
	if recovered != nil {
		reviewPath := filepath.Join(change, "review.json")
		review, err := councilengine.ParseCouncilPayload(mustReadFile(reviewPath))
		if err != nil {
			return nil, blockedf("prepared council publication recovery produced no review.json.")
		}
		return review, nil
	}
	configPath := filepath.Join(projectRoot, "bsl-flow.yaml")
	policyHash, err := councilengine.PolicyHash(configPath)
	if err != nil {
		return nil, blockedf("%s", unwrapMessage(err))
	}
	evidenceText, err := councilEvidence(deps, state, maxInput)
	if err != nil {
		return nil, err
	}
	snapshot, err := councilengine.NewSnapshot(change, maxInput, evidenceText, policyHash)
	if err != nil {
		return nil, blockedf("%s", unwrapMessage(err))
	}
	dispatcher, capabilities, err := newCouncilDispatcher(deps, state, run, runRoot)
	if err != nil {
		return nil, err
	}
	opts := &councilengine.CycleOptions{
		ProjectRoot:       projectRoot,
		ChangeName:        changeName,
		EvidenceText:      evidenceText,
		MaxInputBytes:     maxInput,
		Policy:            policy,
		PolicyHash:        policyHash,
		Snapshot:          snapshot,
		RunRoot:           runRoot,
		AllowLiveDispatch: true,
		Dispatcher:        dispatcher,
		Capabilities:      capabilities,
		Cancelled:         run.cancel,
	}
	result, err := engine.RunCycle(ctx, opts)
	if err != nil {
		return nil, blockedf("%s", unwrapMessage(err))
	}
	return result.Review, nil
}

func mustReadFile(path string) string {
	data, err := repository.StageHostReadFileBytes(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// newCouncilDispatcher builds the direct API + fallback dispatcher and the
// per-role current-agent capabilities for tokenless roles.
func newCouncilDispatcher(deps Deps, state map[string]any, run *stageRun, runRoot string) (councilengine.Dispatcher, map[string]any, error) {
	maxOutput, err := yamlIntegerConfig(state, []string{"review", "runtime", "max_output_bytes"}, 1048576)
	if err != nil {
		return nil, nil, err
	}
	dispatcher := &councilDispatcher{
		runRoot:   runRoot,
		maxOutput: maxOutput,
	}
	dispatcher.clientFor = func(binding *councilengine.Binding, timeout int) counciltransport.Client {
		baseURL := fmt.Sprintf("%s://%s:%d%s", binding.Endpoint.Scheme, binding.Endpoint.Host, binding.Endpoint.Port, binding.Endpoint.BasePath)
		return counciltransport.Client{
			BaseURL: baseURL,
			Timeout: time.Duration(timeout) * time.Second,
		}
	}
	capabilities, err := councilFallbackCapabilities(deps, state, run)
	if err != nil {
		return nil, nil, err
	}
	dispatcher.fallbackFor = func(ctx context.Context, attempt *councilengine.Attempt, prompt string, route *councilengine.Route, timeout int) (councilengine.DispatchResult, error) {
		return councilFallbackDispatch(ctx, deps, state, run, attempt, prompt, route, timeout)
	}
	return dispatcher, capabilities, nil
}

func yamlIntegerConfig(state map[string]any, path []string, defaultValue int64) (int64, error) {
	projectPath := asStringOr(state["project_path"])
	configPath := filepath.Join(projectPath, "bsl-flow.yaml")
	configText := ""
	if isRegularFile(configPath) {
		text, err := readRawText(configPath)
		if err != nil {
			return 0, err
		}
		configText = text
	}
	return yamlInteger(configText, path, defaultValue)
}

// councilFallbackCapabilities builds one current-agent capability receipt for
// every tokenless role. It returns an empty map when no role needs fallback.
func councilFallbackCapabilities(deps Deps, state map[string]any, run *stageRun) (map[string]any, error) {
	request, _ := asObject(state["request"])
	profileMap := getValue(request, "execution_profile", nil)
	if profileMap == nil {
		return map[string]any{}, nil
	}
	profile, err := workerProfileFromState(profileMap)
	if err != nil {
		return nil, err
	}
	if profile.Provider != "codex" {
		return map[string]any{}, nil
	}
	// Determine which roles need fallback (missing credential + current_agent).
	policy, err := councilengine.ParseCouncilPolicy(readProjectConfig(state))
	if err != nil {
		return nil, err
	}
	overlay, err := councilengine.LocalProviderOverlay(asStringOr(state["project_path"]))
	if err != nil {
		return nil, err
	}
	needsFallback := false
	for _, roleName := range []string{"brainstorm", "intent_critic", "architecture_critic", "executability_critic", "chair"} {
		role := policy.Roles[roleName]
		if !role.Enabled || role.Fallback != councilengine.FallbackCurrentAgent {
			continue
		}
		model, ok := policy.Models[role.Model]
		if !ok {
			continue
		}
		provider := policy.Providers[model.Provider]
		localToken := ""
		if entry, present := overlay[model.Provider]; present && entry.HasToken {
			localToken = overlayToken(state, model.Provider)
		}
		credential := councilengine.ResolveCredential(model.Provider, provider.TokenEnv, localToken)
		if credential.CredentialSource == "missing" {
			needsFallback = true
		}
	}
	if !needsFallback {
		return map[string]any{}, nil
	}
	capability, err := councilHostCapability(deps, state, run, filepath.Join(run.directory, "managed-council", "host-capability"))
	if err != nil {
		return nil, err
	}
	capabilities := map[string]any{}
	for _, roleName := range []string{"brainstorm", "intent_critic", "architecture_critic", "executability_critic", "chair"} {
		role := policy.Roles[roleName]
		if role.Enabled && role.Fallback == councilengine.FallbackCurrentAgent {
			capabilities[roleName] = capability
		}
	}
	return capabilities, nil
}

func readProjectConfig(state map[string]any) string {
	configPath := filepath.Join(asStringOr(state["project_path"]), "bsl-flow.yaml")
	if !isRegularFile(configPath) {
		return ""
	}
	text, err := readRawText(configPath)
	if err != nil {
		return ""
	}
	return text
}

func overlayToken(state map[string]any, provider string) string {
	localPath := filepath.Join(asStringOr(state["project_path"]), ".bsl-flow", "providers.local.yaml")
	if !isRegularFile(localPath) {
		return ""
	}
	text, err := readRawText(localPath)
	if err != nil {
		return ""
	}
	token, err := councilengine.YamlValue(text, []string{"providers", provider, "token"}, "")
	if err != nil {
		return ""
	}
	return token
}

// councilHostCapability builds one content-addressed current-agent capability
// receipt (Test-BFProfiledCodexHostCapability): the observed current-host
// model/effort, the sealed critic catalog and the profile hashes. It refuses
// when the host identity cannot be proven.
func councilHostCapability(deps Deps, state map[string]any, run *stageRun, directory string) (map[string]any, error) {
	request, _ := asObject(state["request"])
	profileMap := getValue(request, "execution_profile", nil)
	if profileMap == nil {
		return nil, blockedf("current-agent fallback has no supported sealed Codex profile.")
	}
	profile, err := workerProfileFromState(profileMap)
	if err != nil {
		return nil, err
	}
	if profile.Provider != "codex" {
		return nil, blockedf("current-agent fallback has no supported sealed Codex profile.")
	}
	sessionID := os.Getenv("CODEX_SESSION_ID")
	if strings.TrimSpace(sessionID) == "" {
		return nil, blockedf("Current Codex host identity is unavailable: CODEX_SESSION_ID is required; CODEX_THREAD_ID is not a trusted host mapping.")
	}
	observed, err := worker.ObservedModelEffort(sessionID, "", "")
	if err != nil {
		return nil, blockedf("Current Codex host identity is unavailable; the exact CODEX_SESSION_ID rollout is required. %s", unwrapMessage(err))
	}
	if _, err := worker.CriticCapabilityVersion(observed.ObservedModel); err != nil {
		return nil, blockedf("%s", unwrapMessage(err))
	}
	resolvedDirectory, err := safePath(directory)
	if err != nil {
		return nil, err
	}
	catalogDirectory := filepath.Join(resolvedDirectory, "catalog")
	if err := os.MkdirAll(catalogDirectory, 0o755); err != nil {
		return nil, blockedf("%v", err)
	}
	catalog, err := worker.CriticCatalogFromDirectory(catalogDirectory, observed.ObservedModel, observed.ObservedEffort)
	if err != nil {
		return nil, blockedf("%s", unwrapMessage(err))
	}
	catalogSourcePath := filepath.Join(catalogDirectory, "critic-catalog-source.json")
	catalogPath := filepath.Join(catalogDirectory, "critic-catalog.json")
	sourceBytes, err := repository.StageHostCanonical(catalog.Source)
	if err != nil {
		return nil, invalidf("%v", err)
	}
	catalogBytes, err := repository.StageHostCanonical(catalog.Catalog)
	if err != nil {
		return nil, invalidf("%v", err)
	}
	if err := os.WriteFile(catalogSourcePath, sourceBytes, 0o644); err != nil {
		return nil, blockedf("%v", err)
	}
	if err := os.WriteFile(catalogPath, catalogBytes, 0o644); err != nil {
		return nil, blockedf("%v", err)
	}
	catalogSHA := hashFileBytes(catalogBytes)
	catalogSourceSHA := hashFileBytes(sourceBytes)
	capabilityVersion, err := worker.CriticCapabilityVersion(observed.ObservedModel)
	if err != nil {
		return nil, blockedf("%s", unwrapMessage(err))
	}
	return map[string]any{
		"capability_version":    capabilityVersion,
		"provider":              "current_agent",
		"model":                 observed.ObservedModel,
		"effort":                observed.ObservedEffort,
		"fresh_context":         true,
		"sealed":                true,
		"terminal":              true,
		"source":                "current_host_rollout",
		"executable_sha256":     profile.ExecutableSHA256,
		"sandbox_sha256":        profile.Sandbox.SHA256,
		"catalog_sha256":        catalogSHA,
		"skills_sha256":         profile.CodexSkillsSHA256,
		"catalog_source_path":   catalogSourcePath,
		"catalog_source_sha256": catalogSourceSHA,
	}, nil
}

// councilFallbackDispatch runs one current-agent role through the managed
// worker with strict observed identity, returning the terminal result and
// the controller-observed provenance.
func councilFallbackDispatch(ctx context.Context, deps Deps, state map[string]any, run *stageRun, attempt *councilengine.Attempt, prompt string, route *councilengine.Route, timeout int) (councilengine.DispatchResult, error) {
	capability, ok := asObject(route.Capability)
	if !ok {
		return councilengine.DispatchResult{}, blockedf("fallback capability is not an object.")
	}
	model := asStringOr(capability["model"])
	effort := asStringOr(capability["effort"])
	catalogSourcePath := asStringOr(capability["catalog_source_path"])
	catalogSourceSHA := asStringOr(capability["catalog_source_sha256"])
	if strings.TrimSpace(model) == "" || strings.TrimSpace(effort) == "" ||
		strings.TrimSpace(catalogSourcePath) == "" || !isRegularFile(catalogSourcePath) ||
		!repository.StageHostIsSHA256(catalogSourceSHA) {
		return councilengine.DispatchResult{}, blockedf("fallback capability has no exact critic catalog source binding.")
	}
	roleDir := filepath.Join(run.directory, "managed-council", fmt.Sprintf("fallback-%s-%d", attempt.Role, attempt.Sequence))
	request, _ := asObject(state["request"])
	profileMap := getValue(request, "execution_profile", nil)
	profile, err := workerProfileFromState(profileMap)
	if err != nil {
		return councilengine.DispatchResult{}, err
	}
	models := workerModelsFromState(request)
	models.Reviewer = model
	models.ReviewerEffort = effort
	outcome, err := worker.RunManagedWorker(ctx, worker.ManagedWorkerRequest{
		Stage:                     "spec_review",
		Prompt:                    prompt,
		Directory:                 roleDir,
		CodexPath:                 asStringOr(run.attempt["executable"]),
		WorkerPath:                asStringOr(state["worker_path"]),
		ProjectPath:               asStringOr(state["project_path"]),
		TaskID:                    asStringOr(state["task_id"]),
		TimeoutSeconds:            timeout,
		Profile:                   profile,
		Models:                    models,
		RequireObservedIdentity:   true,
		FallbackCatalogSourcePath: catalogSourcePath,
		FallbackCatalogSHA256:     catalogSourceSHA,
		Dependencies: func() (map[string]any, error) {
			return stageExecutionDependencies(state)
		},
		PermissionProfile: func(scratch, config string, writable bool) (string, error) {
			return permissionProfile(state, scratch, config, writable, run.providerContext.canonicalStore)
		},
		TestExecutionCapability: func(capabilityDir, scratch, config, permissions string, writable bool) error {
			_, err := executionCapability(ctx, deps, state, capabilityDir, scratch, config, permissions, writable, "")
			return err
		},
		SchemaPath:    filepath.Join(deps.SkillsRoot, "1c-task", "schemas", "worker-result.schema.json"),
		AdapterSHA256: deps.SelfSHA256,
		RpcSHA256:     deps.SelfSHA256,
	})
	if err != nil {
		return councilengine.DispatchResult{}, blockedf("%s", unwrapMessage(err))
	}
	if outcome.Status != "completed" {
		return councilengine.DispatchResult{Status: "failed_before_acceptance"}, nil
	}
	hostReceipt := outcome.HostResult
	if hostReceipt == nil {
		return councilengine.DispatchResult{}, blockedf("fallback host receipt is missing; current-agent provenance is unproven.")
	}
	observedModel := asStringOr(hostReceipt["observed_model"])
	observedEffort := asStringOr(hostReceipt["observed_effort"])
	if strings.TrimSpace(observedModel) == "" || strings.TrimSpace(observedEffort) == "" {
		return councilengine.DispatchResult{}, blockedf("fallback host receipt carries no observed model/effort; requested values are not provenance.")
	}
	if observedModel != model || observedEffort != effort {
		return councilengine.DispatchResult{}, blockedf("fallback host receipt resolved a different model/effort than the current host capability.")
	}
	payload, err := councilengine.ParseCouncilPayload(outcome.PayloadJSON)
	if err != nil {
		return councilengine.DispatchResult{}, err
	}
	provider := "current_agent"
	reason := route.Reason
	return councilengine.DispatchResult{
		Status:         "completed",
		Payload:        payload,
		Observed:       councilengine.Observed{Provider: &provider, Model: &observedModel, Effort: &observedEffort},
		ExecutionMode:  "current_agent_fallback",
		FallbackReason: &reason,
	}, nil
}
