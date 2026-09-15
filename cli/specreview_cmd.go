package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bsl-flow/cli/internal/councilengine"
	"bsl-flow/cli/internal/counciltransport"
	"bsl-flow/cli/internal/specreview"
	"bsl-flow/cli/internal/specvalidate"
)

// specreview_cmd.go implements the assisted review entrypoints:
//
//	bsl-flow spec review --project <path> --change <id> [--complexity S|M|L]
//	  [--risk low|medium|high] [--model id] [--variant id] [--force]
//	  [--force-replace] [--timeout n] [--max-output n] [--opencode path]
//	  [--evidence text] [--json]
//	bsl-flow spec metric --project <path> --change <id> [--author-model m]
//	  [--author-reasoning r] [--metrics-path path] [--json]
//
// `spec review` ports Invoke-1CSpecReview.ps1 routing: lint, then the council
// route (native engine) or the single-reviewer OpenCode route or a skip. The
// council route runs without PowerShell and without a managed execution
// profile (fallback roles block when the capability is unavailable).

func parseSpecReviewInvocation(args []string) (invocation, error) {
	in := invocation{command: "spec", action: "review", options: map[string]string{}}
	allowed := map[string]bool{
		"--project": true, "--change": true, "--complexity": true, "--risk": true,
		"--model": true, "--variant": true, "--force": true, "--force-replace": true,
		"--timeout": true, "--max-output": true, "--opencode": true, "--evidence": true, "--json": true,
	}
	for i := 2; i < len(args); i++ {
		key := args[i]
		if !allowed[key] || in.options[key] != "" {
			return in, fmt.Errorf("unknown, repeated, or inapplicable option %q", key)
		}
		if key == "--force" || key == "--force-replace" || key == "--json" {
			in.options[key] = "1"
			continue
		}
		if i+1 == len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") || strings.ContainsAny(args[i+1], "\x00\r\n") {
			return in, fmt.Errorf("missing or invalid value for %s", key)
		}
		in.options[key] = args[i+1]
		i++
	}
	if in.options["--project"] == "" {
		return in, errors.New("review requires --project")
	}
	if in.options["--change"] == "" {
		return in, errors.New("review requires --change")
	}
	if change := in.options["--change"]; !validChangeID(change) {
		return in, errors.New("--change must be a single directory name under openspec/changes")
	}
	return in, nil
}

func parseSpecMetricInvocation(args []string) (invocation, error) {
	in := invocation{command: "spec", action: "metric", options: map[string]string{}}
	allowed := map[string]bool{
		"--project": true, "--change": true, "--author-model": true, "--author-reasoning": true, "--metrics-path": true, "--json": true,
	}
	for i := 2; i < len(args); i++ {
		key := args[i]
		if !allowed[key] || in.options[key] != "" {
			return in, fmt.Errorf("unknown, repeated, or inapplicable option %q", key)
		}
		if key == "--json" {
			in.options[key] = "1"
			continue
		}
		if i+1 == len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") || strings.ContainsAny(args[i+1], "\x00\r\n") {
			return in, fmt.Errorf("missing or invalid value for %s", key)
		}
		in.options[key] = args[i+1]
		i++
	}
	if in.options["--project"] == "" {
		return in, errors.New("metric requires --project")
	}
	if in.options["--change"] == "" {
		return in, errors.New("metric requires --change")
	}
	if change := in.options["--change"]; !validChangeID(change) {
		return in, errors.New("--change must be a single directory name under openspec/changes")
	}
	return in, nil
}

type specReviewOutcome struct {
	Complexity     string `json:"complexity"`
	Risk           string `json:"risk"`
	Route          string `json:"route"`
	ReviewRequired bool   `json:"review_required"`
	LintPassed     bool   `json:"lint_passed"`
	ReviewPath     string `json:"review_path,omitempty"`
	Verdict        string `json:"verdict,omitempty"`
	RunID          string `json:"run_id,omitempty"`
}

func runSpecReview(in invocation, out io.Writer) int {
	project := in.options["--project"]
	change := in.options["--change"]
	asJSON := in.options["--json"] != ""
	outcome, err := executeSpecReview(in, project, change)
	if err != nil {
		return hostError(out, 11, "", err)
	}
	if asJSON {
		if err := encodeJSON(out, outcome); err != nil {
			return hostError(out, 11, "", err)
		}
	} else {
		fmt.Fprintf(out, "spec review %s\nroute=%s review_required=%v lint_passed=%v\n", change, outcome.Route, outcome.ReviewRequired, outcome.LintPassed)
		if outcome.ReviewPath != "" {
			fmt.Fprintf(out, "review=%s verdict=%s\n", outcome.ReviewPath, outcome.Verdict)
		}
	}
	return 0
}

func executeSpecReview(in invocation, project, change string) (specReviewOutcome, error) {
	changeDir := specChangeDir(project, change)
	specPath := filepath.Join(changeDir, "spec.md")
	specData, err := os.ReadFile(specPath)
	if err != nil {
		return specReviewOutcome{}, fmt.Errorf("spec.md not found: %s", specPath)
	}
	specText := strings.TrimPrefix(string(specData), "\uFEFF")
	complexity, risk, err := councilengine.ClassifySpec(specText)
	if err != nil {
		return specReviewOutcome{}, err
	}
	if explicit := in.options["--complexity"]; explicit != "" && explicit != complexity {
		return specReviewOutcome{}, errors.New("Explicit complexity conflicts with spec.md classification.")
	}
	if explicit := in.options["--risk"]; explicit != "" && explicit != risk {
		return specReviewOutcome{}, errors.New("Explicit risk conflicts with spec.md classification.")
	}
	configPath := filepath.Join(project, "bsl-flow.yaml")
	configText := ""
	if data, err := os.ReadFile(configPath); err == nil {
		configText = string(data)
	}
	councilPolicy, err := councilengine.ParseCouncilPolicy(configText)
	if err != nil {
		return specReviewOutcome{}, err
	}
	// A prepared council publication is a durable recovery record.
	runRoot := councilengine.RunRoot(project, change)
	if isRegularFile(filepath.Join(runRoot, "publication", "prepared.json")) {
		if !councilPolicy.Enabled || councilPolicy.LegacyMode == "opencode_compat" {
			return specReviewOutcome{}, errors.New("BF_BLOCKED: prepared council publication requires the council route to remain enabled.")
		}
		engine := &councilengine.Engine{Now: time.Now, FinalValidation: func(changeDir string) (map[string]any, error) {
			return assistedCouncilFinalValidation(changeDir, project)
		}}
		recovered, err := engine.ResumePreparedPublicationIfPresent(project, change)
		if err != nil || recovered == nil {
			return specReviewOutcome{}, errors.New("BF_BLOCKED: prepared council publication disappeared before recovery.")
		}
		return specReviewOutcome{Complexity: complexity, Risk: risk, Route: "council", ReviewRequired: true, LintPassed: true, ReviewPath: filepath.Join(changeDir, "review.json"), Verdict: "PASS"}, nil
	}
	// Lint first; the lint sidecar persists exactly like the legacy
	// Invoke-1CSpecReview.ps1 route so the estimate gate can read it.
	findings, lintErr := specvalidate.LintSpec(specData)
	if lintErr != nil {
		return specReviewOutcome{}, lintErr
	}
	if err := writeSpecLintSidecar(filepath.Join(changeDir, "spec-lint.json"), newSpecLintArtifact(specData, findings)); err != nil {
		return specReviewOutcome{}, err
	}
	lintPassed := true
	for _, finding := range findings {
		if finding.Severity == "error" {
			lintPassed = false
			break
		}
	}
	// Routing.
	reviewEnabled, err := yamlBool(configText, []string{"review", "enabled"}, "true", "review.enabled")
	if err != nil {
		return specReviewOutcome{}, err
	}
	route := ""
	if risk == "high" {
		route, err = councilengine.YamlValue(configText, []string{"review", "routing", "high_risk_override"}, "required")
	} else {
		route, err = councilengine.YamlValue(configText, []string{"review", "routing", strings.ToLower(complexity) + "_default"}, "optional")
		if strings.ToLower(complexity) == "s" {
			route, err = councilengine.YamlValue(configText, []string{"review", "routing", "s_default"}, "optional")
		}
	}
	if err != nil {
		return specReviewOutcome{}, err
	}
	if route != "required" && route != "optional" && route != "off" {
		return specReviewOutcome{}, fmt.Errorf("Invalid review route: %s", route)
	}
	policyRequired := (complexity == "M" || complexity == "L") || risk == "high"
	if policyRequired && route != "required" {
		return specReviewOutcome{}, errors.New("Project routing cannot weaken the mandatory M/L/high-risk review policy.")
	}
	forceReview := in.options["--force"] != ""
	reviewRequired := forceReview || route == "required"
	if reviewRequired && !reviewEnabled && !forceReview {
		return specReviewOutcome{}, errors.New("Review is required by routing but review.enabled is false.")
	}
	if !reviewRequired {
		return specReviewOutcome{Complexity: complexity, Risk: risk, Route: route, ReviewRequired: false, LintPassed: lintPassed}, nil
	}
	originalTaskPath := filepath.Join(changeDir, "original-task.md")
	if !isRegularFile(originalTaskPath) {
		return specReviewOutcome{}, errors.New("original-task.md is required for independent review: " + originalTaskPath)
	}
	reviewPath := filepath.Join(changeDir, "review.json")
	if isRegularFile(reviewPath) && in.options["--force-replace"] == "" {
		return specReviewOutcome{}, fmt.Errorf("review.json already exists; refusing to overwrite evidence: %s", reviewPath)
	}
	if councilPolicy.Enabled && councilPolicy.LegacyMode != "opencode_compat" {
		verdict, err := executeCouncilReview(project, change, changeDir, councilPolicy, in.options["--evidence"])
		if err != nil {
			return specReviewOutcome{}, err
		}
		return specReviewOutcome{Complexity: complexity, Risk: risk, Route: "council", ReviewRequired: true, LintPassed: lintPassed, ReviewPath: reviewPath, Verdict: verdict}, nil
	}
	return executeSingleReviewer(project, change, changeDir, configText, in, complexity, risk, lintPassed)
}

func executeCouncilReview(project, change, changeDir string, policy *councilengine.CouncilPolicy, evidenceText string) (string, error) {
	configPath := filepath.Join(project, "bsl-flow.yaml")
	policyHash, err := councilengine.PolicyHash(configPath)
	if err != nil {
		return "", err
	}
	maxInput := 262144
	if raw, err := councilengine.YamlValue(readProjectConfigText(project), []string{"review", "input", "max_file_bytes"}, "262144"); err == nil {
		if parsed, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			maxInput = parsed
		}
	}
	snapshot, err := councilengine.NewSnapshot(changeDir, maxInput, evidenceText, policyHash)
	if err != nil {
		return "", err
	}
	dispatcher := &assistedDispatcher{runRoot: councilengine.RunRoot(project, change)}
	engine := &councilengine.Engine{
		Now: time.Now,
		FinalValidation: func(changeDir string) (map[string]any, error) {
			return assistedCouncilFinalValidation(changeDir, project)
		},
	}
	result, err := engine.RunCycle(context.Background(), &councilengine.CycleOptions{
		ProjectRoot: project, ChangeName: change, EvidenceText: evidenceText,
		MaxInputBytes: maxInput, Policy: policy, PolicyHash: policyHash, Snapshot: snapshot,
		RunRoot: councilengine.RunRoot(project, change), AllowLiveDispatch: true, Dispatcher: dispatcher,
	})
	if err != nil {
		return "", err
	}
	return result.Review.Get("verdict").(string), nil
}

func executeSingleReviewer(project, change, changeDir, configText string, in invocation, complexity, risk string, lintPassed bool) (specReviewOutcome, error) {
	policy, err := specreview.ParsePolicy(configText)
	if err != nil {
		return specReviewOutcome{}, err
	}
	model := in.options["--model"]
	if model == "" {
		model, _ = councilengine.YamlValue(configText, []string{"review", "reviewer", "model"}, "deepseek/deepseek-v4-pro")
	}
	variant := in.options["--variant"]
	if variant == "" {
		variant, _ = councilengine.YamlValue(configText, []string{"review", "reviewer", "variant"}, "high")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._:#-]+$`).MatchString(model) {
		return specReviewOutcome{}, errors.New("Unsafe or invalid reviewer model id: " + model)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(variant) {
		return specReviewOutcome{}, errors.New("Unsafe or invalid reviewer variant: " + variant)
	}
	configuredAgent, _ := councilengine.YamlValue(configText, []string{"review", "reviewer", "agent"}, "bsl-flow-spec-reviewer")
	agent := configuredAgent
	if policy.ReadMode == "attached_only" {
		agent = "bsl-flow-spec-reviewer-sealed"
	}
	if agent != "bsl-flow-spec-reviewer" && agent != "bsl-flow-spec-reviewer-sealed" {
		return specReviewOutcome{}, errors.New("Only packaged hard-deny reviewer agents are allowed: " + agent)
	}
	skillRoot, err := resolveSkillRoot()
	if err != nil {
		return specReviewOutcome{}, err
	}
	files, err := specreview.ResolveReviewerFiles(filepath.Join(skillRoot, "1c-spec-review"))
	if err != nil {
		return specReviewOutcome{}, err
	}
	maxInput := 262144
	if raw, err := councilengine.YamlValue(configText, []string{"review", "input", "max_file_bytes"}, "262144"); err == nil {
		if parsed, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			maxInput = parsed
		}
	}
	if maxInput < 1024 || maxInput > 1048576 {
		return specReviewOutcome{}, errors.New("review.input.max_file_bytes must be between 1024 and 1048576.")
	}
	timeout := 600
	if raw, err := councilengine.YamlValue(configText, []string{"review", "runtime", "timeout_seconds"}, "600"); err == nil {
		if parsed, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			timeout = parsed
		}
	}
	maxOutput := int64(1048576)
	if raw, err := councilengine.YamlValue(configText, []string{"review", "runtime", "max_output_bytes"}, "1048576"); err == nil {
		if parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil {
			maxOutput = parsed
		}
	}
	if in.options["--timeout"] != "" {
		if parsed, err := strconv.Atoi(in.options["--timeout"]); err == nil {
			timeout = parsed
		}
	}
	if in.options["--max-output"] != "" {
		if parsed, err := strconv.ParseInt(in.options["--max-output"], 10, 64); err == nil {
			maxOutput = parsed
		}
	}
	// Capture input snapshots for the freshness check.
	originalTaskData, err := boundedRead(filepath.Join(changeDir, "original-task.md"), maxInput)
	if err != nil {
		return specReviewOutcome{}, err
	}
	specData, err := boundedRead(filepath.Join(changeDir, "spec.md"), maxInput)
	if err != nil {
		return specReviewOutcome{}, err
	}
	var designData []byte
	if isRegularFile(filepath.Join(changeDir, "design.md")) {
		designData, err = boundedRead(filepath.Join(changeDir, "design.md"), maxInput)
		if err != nil {
			return specReviewOutcome{}, err
		}
	}
	rubricData, err := boundedRead(files.RubricPath, maxInput)
	if err != nil {
		return specReviewOutcome{}, err
	}
	envelope := buildContextEnvelope(originalTaskData, specData, designData, rubricData)
	rawReview, err := specreview.RunReviewer(context.Background(), specreview.ReviewerRequest{
		ProjectRoot: project, Agent: agent, Model: model, Variant: variant,
		OpenCodePath: in.options["--opencode"], TimeoutSeconds: timeout, MaxOutputBytes: maxOutput,
		ContextEnvelope: envelope,
	}, files, time.Now)
	if err != nil {
		return specReviewOutcome{}, err
	}
	// Freshness check.
	for _, entry := range []struct {
		name string
		data []byte
	}{
		{"original-task.md", originalTaskData}, {"spec.md", specData}, {"design.md", designData},
	} {
		if entry.data == nil {
			if isRegularFile(filepath.Join(changeDir, entry.name)) {
				return specReviewOutcome{}, fmt.Errorf("Review input changed during provider execution: %s", entry.name)
			}
			continue
		}
		current, err := boundedRead(filepath.Join(changeDir, entry.name), maxInput)
		if err != nil || councilengine.Sha256Hex(current) != councilengine.Sha256Hex(entry.data) {
			return specReviewOutcome{}, fmt.Errorf("Review input changed during provider execution: %s", entry.name)
		}
	}
	var designSHA *string
	if designData != nil {
		hash := councilengine.Sha256Hex(designData)
		designSHA = &hash
	}
	review, err := specreview.CompleteReview(rawReview, specreview.CompleteInputs{
		OriginalTaskPath: filepath.Join(changeDir, "original-task.md"),
		SpecPath:         filepath.Join(changeDir, "spec.md"),
		DesignPath:       filepath.Join(changeDir, "design.md"),
		Agent:            agent, Model: model, Policy: policy,
		OriginalTaskSHA256: councilengine.Sha256Hex(originalTaskData),
		SpecSHA256:         councilengine.Sha256Hex(specData),
		DesignSHA256:       designSHA,
	}, time.Now())
	if err != nil {
		return specReviewOutcome{}, err
	}
	if err := councilengine.WriteJSONAtomic(filepath.Join(changeDir, "review.json"), review); err != nil {
		return specReviewOutcome{}, err
	}
	return specReviewOutcome{Complexity: complexity, Risk: risk, Route: "single_reviewer", ReviewRequired: true, LintPassed: lintPassed, ReviewPath: filepath.Join(changeDir, "review.json"), Verdict: review.Get("verdict").(string)}, nil
}

func runSpecMetric(in invocation, out io.Writer) int {
	project := in.options["--project"]
	change := in.options["--change"]
	result, err := specreview.RecordMetric(specreview.MetricInputs{
		ProjectRoot: project, ChangeName: change,
		AuthorModel: in.options["--author-model"], AuthorReasoning: in.options["--author-reasoning"],
		MetricsPath: in.options["--metrics-path"],
	}, time.Now)
	if err != nil {
		return hostError(out, 11, "", err)
	}
	if in.options["--json"] != "" {
		if err := encodeJSON(out, map[string]string{"run_id": result.RunID, "metrics_path": result.MetricsPath}); err != nil {
			return hostError(out, 11, "", err)
		}
	} else {
		fmt.Fprintf(out, "spec metric %s\nrun_id=%s\nmetrics=%s\n", change, result.RunID, result.MetricsPath)
	}
	return 0
}

// assistedDispatcher implements councilengine.Dispatcher for the assisted
// route: direct API transport, fallback blocks (no managed capability).
type assistedDispatcher struct {
	runRoot string
}

func (d *assistedDispatcher) Dispatch(ctx context.Context, attempt *councilengine.Attempt, prompt string, route *councilengine.Route, timeoutSeconds int) (councilengine.DispatchResult, error) {
	if route.Route == "current_agent_fallback" {
		return councilengine.DispatchResult{}, errors.New("BF_BLOCKED: current-agent fallback requires a managed host capability receipt.")
	}
	binding := attempt.Binding
	baseURL := fmt.Sprintf("%s://%s:%d%s", binding.Endpoint.Scheme, binding.Endpoint.Host, binding.Endpoint.Port, binding.Endpoint.BasePath)
	client := counciltransport.Client{BaseURL: baseURL, Timeout: time.Duration(timeoutSeconds) * time.Second}
	response, err := client.Chat(ctx, counciltransport.Request{Model: binding.Model, Prompt: prompt, Effort: binding.Effort}, counciltransport.Credentials{APIKey: route.Credential})
	if err != nil {
		return councilengine.DispatchResult{}, councilengine.ClassifyTransportError(err)
	}
	payload, err := councilengine.ParseCouncilPayload(response.Content)
	if err != nil {
		return councilengine.DispatchResult{}, err
	}
	provider := binding.Provider
	model := response.ObservedModel
	usage := &councilengine.MemberUsage{
		InputTokens:     intPtr(response.Usage.PromptTokens),
		OutputTokens:    intPtr(response.Usage.CompletionTokens),
		ReasoningTokens: intPtr(response.Usage.ReasoningTokens),
	}
	return councilengine.DispatchResult{
		Status: "completed", Payload: payload,
		Observed: councilengine.Observed{Provider: &provider, Model: &model},
		Usage:    usage, ExecutionMode: "direct_api",
	}, nil
}

func intPtr(value int) *int64 {
	converted := int64(value)
	return &converted
}

// assistedCouncilFinalValidation is the council final-validation seam for the
// assisted route (lint + deterministic gate + final-validation.json v2).
func assistedCouncilFinalValidation(changeDir, projectPath string) (map[string]any, error) {
	reviewPath := filepath.Join(changeDir, "review.json")
	reviewData, err := os.ReadFile(reviewPath)
	if err != nil {
		return nil, fmt.Errorf("Final spec lint could not run: %v", err)
	}
	review, err := councilengine.ParseCouncilPayload(string(reviewData))
	if err != nil {
		return nil, fmt.Errorf("Final spec lint could not run: %v", err)
	}
	specData, err := os.ReadFile(filepath.Join(changeDir, "spec.md"))
	if err != nil {
		return nil, fmt.Errorf("Final spec lint could not run: %v", err)
	}
	findings, err := specvalidate.LintSpec(specData)
	if err != nil {
		return nil, fmt.Errorf("Final spec lint could not run: %v", err)
	}
	lintPassed := true
	for _, finding := range findings {
		if finding.Severity == "error" {
			lintPassed = false
			break
		}
	}
	var policyHash *string
	if isRegularFile(filepath.Join(projectPath, "bsl-flow.yaml")) {
		if data, err := os.ReadFile(filepath.Join(projectPath, "bsl-flow.yaml")); err == nil {
			hash := councilengine.Sha256Hex([]byte(strings.TrimPrefix(string(data), "\uFEFF")))
			policyHash = &hash
		}
	}
	gate, err := councilengine.TestCouncilFinalGate(councilengine.FinalGateInputs{
		Review: review, OriginalTaskPath: filepath.Join(changeDir, "original-task.md"),
		SpecPath: filepath.Join(changeDir, "spec.md"), DesignPath: filepath.Join(changeDir, "design.md"),
		LintPassed: lintPassed, PolicyHash: policyHash,
	})
	if err != nil {
		return nil, fmt.Errorf("Final specification invariant validation failed: %v", err)
	}
	verdict, _ := review.Get("verdict").(string)
	diversity, _ := review.Get("diversity").(string)
	inputs := map[string]any{
		"review_sha256":         nullableFileHash(filepath.Join(changeDir, "review.json")),
		"reconciliation_sha256": nullableFileHash(filepath.Join(changeDir, "review-reconciliation.json")),
		"final_spec_sha256":     nullableFileHash(filepath.Join(changeDir, "spec.md")),
		"final_design_sha256":   nullableFileHash(filepath.Join(changeDir, "design.md")),
		"original_task_sha256":  nullableFileHash(filepath.Join(changeDir, "original-task.md")),
	}
	errorsList := gate.Errors
	if errorsList == nil {
		errorsList = []string{}
	}
	errorsAny := make([]any, len(errorsList))
	for index, value := range errorsList {
		errorsAny[index] = value
	}
	receipt := councilengine.OrderedFrom(
		[]string{"schema_version", "checked_at_utc", "passed", "review_schema", "verdict", "diversity", "inputs", "errors"},
		[]any{2, time.Now().UTC().Format("2006-01-02T15:04:05.0000000Z"), gate.Passed, 2, verdict, diversity, inputs, errorsAny},
	)
	if err := councilengine.WriteJSONAtomic(filepath.Join(changeDir, "final-validation.json"), receipt); err != nil {
		return nil, err
	}
	if !gate.Passed {
		return councilengine.OrderedToMap(receipt), fmt.Errorf("Final specification invariant validation failed. See: %s", filepath.Join(changeDir, "final-validation.json"))
	}
	return councilengine.OrderedToMap(receipt), nil
}

func nullableFileHash(path string) any {
	if !isRegularFile(path) {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return councilengine.Sha256Hex(data)
}

func buildContextEnvelope(originalTask, spec, design, rubric []byte) string {
	blocks := []string{}
	if originalTask != nil {
		blocks = append(blocks, "<<<BEGIN UNTRUSTED DATA: ORIGINAL TASK>>>\n"+string(originalTask)+"\n<<<END UNTRUSTED DATA: ORIGINAL TASK>>>")
	}
	if spec != nil {
		blocks = append(blocks, "<<<BEGIN UNTRUSTED DATA: DRAFT SPEC>>>\n"+string(spec)+"\n<<<END UNTRUSTED DATA: DRAFT SPEC>>>")
	}
	if design != nil {
		blocks = append(blocks, "<<<BEGIN UNTRUSTED DATA: TECHNICAL DESIGN>>>\n"+string(design)+"\n<<<END UNTRUSTED DATA: TECHNICAL DESIGN>>>")
	}
	if rubric != nil {
		blocks = append(blocks, "<<<BEGIN TRUSTED REVIEW POLICY: REVIEW RUBRIC>>>\n"+string(rubric)+"\n<<<END TRUSTED REVIEW POLICY: REVIEW RUBRIC>>>")
	}
	return strings.Join(blocks, "\n\n")
}

func boundedRead(path string, maxBytes int) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("Review input missing: %s", path)
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("Review input exceeds %d bytes: %s", maxBytes, path)
	}
	return data, nil
}

func yamlBool(text string, path []string, defaultValue, name string) (bool, error) {
	raw, err := councilengine.YamlValue(text, path, defaultValue)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(raw) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("Expected true or false for %s, got: %s", name, raw)
	}
}

func readProjectConfigText(project string) string {
	data, err := os.ReadFile(filepath.Join(project, "bsl-flow.yaml"))
	if err != nil {
		return ""
	}
	return string(data)
}

// resolveSkillRoot locates the extracted bundle's global/skills directory for
// the assisted single-reviewer assets.
func resolveSkillRoot() (string, error) {
	if explicit := os.Getenv("BSL_FLOW_SKILLS_ROOT"); explicit != "" {
		return explicit, nil
	}
	bundle, err := readEmbeddedBundle()
	if err != nil {
		return "", err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	root, err := ensureBundle(filepath.Join(cache, "BSLFlow", "bundles"), bundle)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "global", "skills"), nil
}

func isRegularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}
