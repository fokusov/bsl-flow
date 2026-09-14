package stagehost

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"bsl-flow/cli/internal/repository"
	"bsl-flow/cli/internal/worker"
)

// This file ports the worker-dispatch half of Invoke-BFStageObservation
// (Task.Stages.ps1:359-379), Invoke-BFManagedWorker with its provider-context
// budget hooks (Task.Execution.ps1:565-583, 293-357), Test-BFRuntimePreflight
// (Task.Execution.ps1:80-141) and the completed-status stage calculation
// (Task.Stages.ps1:383-443): payload handling per stage, the code_reconcile
// re-dispatch, coverage gating and the protected-test/repair gates. The folded
// result carries the terminal observation fields the shared envelope renders:
// "outcome", "summary", "proposal", "side_effects" and optionally
// "bound_dependencies" for spec_review.

// workerStageResult is the Invoke-BFManagedWorker return the stage consumes:
// status/summary plus the sealed payload_json string.
type workerStageResult struct {
	Status      string
	Summary     string
	PayloadJSON string
}

// runStageWorkerBody is the else branch of Invoke-BFStageObservation: stage
// evidence assembly, the managed worker dispatch and the completed-status
// folding. Every throw of the PowerShell try-block is returned as an error so
// the shared observation envelope keeps one catch path.
func runStageWorkerBody(ctx context.Context, deps Deps, run *stageRun, raw string) (map[string]any, error) {
	state := run.state
	stage := asStringOr(run.attempt["stage"])
	contextRoot := run.providerContext.contextRoot
	extra := ""
	if stage == "implement" {
		if rounds, ok := asInteger(state["correction_rounds"]); ok && rounds > 0 {
			review := lastEvidenceEntry(state, "code_review")
			if review == nil {
				return nil, blockedf("correction round has no retained code review.")
			}
			text, err := getProviderContextArtifactText(contextRoot, "attempts/"+asStringOr(review["attempt_id"])+"/result.json", run.providerContext.priorArtifacts)
			if err != nil {
				return nil, err
			}
			extra = text
		}
	}
	if stage == "diagnose" {
		pendingFailure := asStringOr(getValue(asMap(state["repair"]), "pending_failure", nil))
		if err := assertProviderRepairFailure(state, pendingFailure, run.providerContext); err != nil {
			return nil, err
		}
		text, err := getProviderContextArtifactText(contextRoot, "attempts/"+pendingFailure+"/result.json", run.providerContext.priorArtifacts)
		if err != nil {
			return nil, err
		}
		extra = text
	}
	if stage == "implement" {
		if diagnosisAttempt := getValue(asMap(state["repair"]), "diagnosis_attempt", nil); diagnosisAttempt != nil {
			diagnosis, err := getProviderContextArtifactText(contextRoot, "attempts/"+asStringOr(diagnosisAttempt)+"/result.json", run.providerContext.priorArtifacts)
			if err != nil {
				return nil, err
			}
			extra += "\nRetained source repair diagnosis (criteria remain fixed):\n" + diagnosis
		}
	}
	prompt, err := stagePrompt(deps, state, stage, extra, run.attempt)
	if err != nil {
		return nil, err
	}
	outcome, err := runManagedWorkerDispatch(ctx, deps, run, stage, prompt, filepath.Join(raw, "worker"))
	if err != nil {
		return nil, err
	}
	return foldWorkerStageOutcome(ctx, deps, run, raw, stage, outcome)
}

// lastEvidenceEntry mirrors `@($state.evidence|Where-Object{...})[-1]`.
func lastEvidenceEntry(state map[string]any, stage string) map[string]any {
	entries, _ := asArray(state["evidence"])
	var latest map[string]any
	for _, raw := range entries {
		entry, _ := asObject(raw)
		if asStringOr(entry["stage"]) == stage {
			latest = entry
		}
	}
	return latest
}

// foldWorkerStageOutcome ports the status switch of Invoke-BFStageObservation
// (Task.Stages.ps1:381-443): the worker result maps onto the terminal outcome,
// completed stages validate and bind their payloads, and the post-stage
// protected-input gates run inside the same try-block.
func foldWorkerStageOutcome(ctx context.Context, deps Deps, run *stageRun, raw, stage string, outcome workerStageResult) (map[string]any, error) {
	state := run.state
	switch outcome.Status {
	case "needs_input", "failed", "blocked":
		return map[string]any{"outcome": stageOutcomeOf(outcome.Status), "summary": outcome.Summary}, nil
	case "completed":
	default:
		return nil, invalidf("invalid worker stage status.")
	}
	if stage == "spec_review" {
		// spec_review binds its own reconciliation result; its dedicated stage
		// path never reaches this fold.
		return map[string]any{"outcome": "PASS", "summary": outcome.Summary, "payload_json": outcome.PayloadJSON}, nil
	}
	proposal, err := readStagePayload(outcome, raw)
	if err != nil {
		return nil, err
	}
	var folded any = proposal
	outcomeText := "PASS"
	summary := outcome.Summary
	sideEffects := "none"
	switch stage {
	case "inspect":
		if err := setClassification(state, proposal); err != nil {
			return nil, err
		}
		flags, _ := asArray(asMap(state["classification"])["impact_flags"])
		for _, rawFlag := range flags {
			if rawFlag == "ambiguous_business_rule" {
				outcomeText = "NEEDS_INPUT"
				summary = asStringOr(proposal["rationale"])
			}
		}
	case "spec":
		if err := saveStageSpec(deps, state, proposal, raw); err != nil {
			return nil, err
		}
	case "implement":
		if _, err := assertFields(proposal, []string{"changed_files"}, []string{"observations"}, "implementation"); err != nil {
			return nil, err
		}
		if observations := getValue(proposal, "observations", nil); observations != nil {
			if err := assertMemoryObservations(observations, stage); err != nil {
				return nil, err
			}
		}
		changed, ok := asArray(proposal["changed_files"])
		if !ok {
			return nil, invalidf("changed_files must be an array.")
		}
		for _, rawPath := range changed {
			if err := assertRelativePath(asStringOr(rawPath)); err != nil {
				return nil, err
			}
		}
		sideEffects = "source_changed"
	case "diagnose":
		if err := assertDiagnosis(state, proposal); err != nil {
			return nil, err
		}
		summary = asStringOr(proposal["reason"])
		switch asStringOr(proposal["category"]) {
		case "implementation":
			outcomeText = "REPAIR"
		case "business_rule":
			outcomeText = "NEEDS_INPUT"
		default:
			outcomeText = "BLOCKED"
		}
	case "code_review":
		reviewProposal, reviewOutcome, reviewSummary, err := foldCodeReviewOutcome(ctx, deps, run, raw, proposal)
		if err != nil {
			return nil, err
		}
		folded = reviewProposal
		outcomeText = reviewOutcome
		if reviewSummary != "" {
			summary = reviewSummary
		}
	}
	// Post-stage protected-input gates (Task.Stages.ps1:437-443).
	request, _ := asObject(state["request"])
	if stage == "implement" {
		manifest, err := stageSourceManifest(state)
		if err != nil {
			return nil, err
		}
		if hasProperty(request, "requirements") || hasNativeCriterion(request) {
			before, err := protectedTestManifest(state, asMap(run.attempt["source_manifest"]))
			if err != nil {
				return nil, err
			}
			after, err := protectedTestManifest(state, manifest)
			if err != nil {
				return nil, err
			}
			beforeHash, err := hashValue(before)
			if err != nil {
				return nil, err
			}
			afterHash, err := hashValue(after)
			if err != nil {
				return nil, err
			}
			if beforeHash != afterHash {
				return nil, blockedf("implementation changed protected native test inputs; a trusted test-contract revision is required.")
			}
		}
		if rounds, ok := asInteger(getValue(asMap(state["repair"]), "rounds", int64(0))); ok && rounds > 0 {
			if err := assertProviderProtectedTests(state, run.providerContext); err != nil {
				return nil, err
			}
		}
	}
	return map[string]any{
		"outcome":      outcomeText,
		"summary":      summary,
		"proposal":     folded,
		"side_effects": sideEffects,
	}, nil
}

// stageOutcomeOf maps the worker status vocabulary onto stage outcomes.
func stageOutcomeOf(status string) string {
	switch status {
	case "needs_input":
		return "NEEDS_INPUT"
	case "failed":
		return "FAIL"
	case "blocked":
		return "BLOCKED"
	case "completed":
		return "PASS"
	}
	return "BLOCKED"
}

func hasNativeCriterion(request map[string]any) bool {
	criteria, _ := asArray(request["criteria"])
	for _, raw := range criteria {
		criterion, _ := asObject(raw)
		if getValue(criterion, "native_1c", nil) != nil {
			return true
		}
	}
	return false
}

// foldCodeReviewOutcome ports the code_review arm of the completed switch:
// review validation, coverage gating and the code_reconcile re-dispatch.
func foldCodeReviewOutcome(ctx context.Context, deps Deps, run *stageRun, raw string, proposal map[string]any) (any, string, string, error) {
	state := run.state
	if err := assertCodeReview(proposal); err != nil {
		return nil, "", "", err
	}
	if _, err := assertCoverageReview(state, getValue(proposal, "coverage_review", nil), raw); err != nil {
		return nil, "", "", err
	}
	coverage := getValue(proposal, "coverage_review", nil)
	if coverage != nil && asMap(coverage)["verdict"] == "BLOCK" {
		gaps := []string{}
		assessments, _ := asArray(asMap(coverage)["assessments"])
		for _, rawAssessment := range assessments {
			assessment, _ := asObject(rawAssessment)
			if asStringOr(assessment["verdict"]) == "INSUFFICIENT" {
				gaps = append(gaps, asStringOr(assessment["requirement_id"])+": "+asStringOr(assessment["rationale"]))
			}
		}
		return proposal, "BLOCKED", "Insufficient requirement coverage; trusted scope update required. " + strings.Join(gaps, "; "), nil
	}
	if asStringOr(proposal["verdict"]) == "PASS" {
		return proposal, "PASS", "", nil
	}
	reconcileDir := filepath.Join(raw, "reconciler")
	extra, err := canonicalText(proposal)
	if err != nil {
		return nil, "", "", err
	}
	prompt, err := stagePrompt(deps, state, "code_reconcile", extra, run.attempt)
	if err != nil {
		return nil, "", "", err
	}
	recResult, err := runManagedWorkerDispatch(ctx, deps, run, "code_reconcile", prompt, reconcileDir)
	if err != nil {
		return nil, "", "", err
	}
	if recResult.Status != "completed" {
		return nil, "", "", blockedf("code reconciliation incomplete.")
	}
	reconciled, err := readStagePayload(recResult, reconcileDir)
	if err != nil {
		return nil, "", "", err
	}
	if _, err := assertFields(reconciled, []string{"decisions", "fix_instructions"}, nil, "code_reconciliation"); err != nil {
		return nil, "", "", err
	}
	decisions, ok := asArray(reconciled["decisions"])
	findings, _ := asArray(proposal["findings"])
	if !ok || len(decisions) != len(findings) {
		return nil, "", "", invalidf("incomplete code reconciliation.")
	}
	accepted := 0
	for _, rawFinding := range findings {
		finding, _ := asObject(rawFinding)
		var decision map[string]any
		count := 0
		for _, rawDecision := range decisions {
			candidate, _ := asObject(rawDecision)
			if candidate["finding_id"] == finding["id"] {
				decision = candidate
				count++
			}
		}
		if count != 1 {
			return nil, "", "", invalidf("finding requires exactly one decision.")
		}
		if _, err := assertFields(decision, []string{"finding_id", "decision", "reason", "evidence"}, nil, "decision"); err != nil {
			return nil, "", "", err
		}
		if asStringOr(decision["decision"]) != "accepted" && asStringOr(decision["decision"]) != "rejected" {
			return nil, "", "", invalidf("invalid finding decision.")
		}
		if err := assertText(decision["reason"], "decision.reason"); err != nil {
			return nil, "", "", err
		}
		if err := assertText(decision["evidence"], "decision.evidence"); err != nil {
			return nil, "", "", err
		}
		if asStringOr(decision["decision"]) == "accepted" {
			accepted++
		}
	}
	wrapped := any(map[string]any{"review": proposal, "reconciliation": reconciled})
	if accepted > 0 {
		if err := assertText(reconciled["fix_instructions"], "fix_instructions"); err != nil {
			return nil, "", "", err
		}
		rounds, _ := asInteger(state["correction_rounds"])
		outcome := "FAIL"
		if rounds < 1 {
			outcome = "REVISE"
		}
		return wrapped, outcome, "Accepted code findings require a correction and independent review of the updated full diff.", nil
	}
	return wrapped, "PASS", "", nil
}

func canonicalText(value any) (string, error) {
	data, err := repository.StageHostCanonical(value)
	if err != nil {
		return "", invalidf("%v", err)
	}
	return string(data), nil
}

// runManagedWorkerDispatch ports Invoke-BFManagedWorker on the provider
// context path: runtime preflight, provider budget admission/reservation,
// provider dispatch and budget completion, in the exact legacy order.
func runManagedWorkerDispatch(ctx context.Context, deps Deps, run *stageRun, stage, prompt, directory string) (workerStageResult, error) {
	state := run.state
	request, _ := asObject(state["request"])
	profileMap := getValue(request, "execution_profile", nil)
	if profileMap == nil {
		return workerStageResult{}, blockedf("stage %s requires a managed execution profile.", stage)
	}
	profile, err := workerProfileFromState(profileMap)
	if err != nil {
		return workerStageResult{}, err
	}
	models := workerModelsFromState(request)
	timeoutSeconds := 1800
	if value, ok := asInteger(getValue(request, "timeout_seconds", int64(1800))); ok {
		timeoutSeconds = int(value)
	}
	pc := run.providerContext
	requestedModel, _ := models.Selection(stage)
	hasBudget := hasProperty(request, "budget")
	if err := runtimePreflight(ctx, deps, state, directory, filepath.Join(pc.artifactRoot, "runtime")); err != nil {
		return workerStageResult{}, err
	}
	var budget *worker.BudgetHooks
	if hasBudget {
		budget = &worker.BudgetHooks{
			Admit: func(context.Context, string, string) error {
				return assertProviderBudgetAdmission(state, pc, directory)
			},
			Reserve: func(context.Context, string, string) error {
				return addProviderBudgetReservation(deps, state, pc, directory, profile.Provider, stage, requestedModel)
			},
			Complete: func(context.Context, string, string) error {
				return completeProviderBudgetDispatch(deps, state, pc, directory, profile.Provider, stage, requestedModel)
			},
		}
	}
	outcome, err := worker.RunManagedWorker(ctx, worker.ManagedWorkerRequest{
		Stage:          stage,
		Prompt:         prompt,
		Directory:      directory,
		CodexPath:      asStringOr(run.attempt["executable"]),
		WorkerPath:     asStringOr(state["worker_path"]),
		ProjectPath:    asStringOr(state["project_path"]),
		TaskID:         asStringOr(state["task_id"]),
		TimeoutSeconds: timeoutSeconds,
		Profile:        profile,
		Models:         models,
		Budget:         budget,
		Dependencies: func() (map[string]any, error) {
			return stageExecutionDependencies(state)
		},
		PermissionProfile: func(scratch, config string, writable bool) (string, error) {
			return permissionProfile(state, scratch, config, writable, pc.canonicalStore)
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
		return workerStageResult{}, translateWorkerError(err)
	}
	return workerStageResult{
		Status:      outcome.Status,
		Summary:     outcome.Summary,
		PayloadJSON: outcome.PayloadJSON,
	}, nil
}

// translateWorkerError maps worker-library failures onto the provider error
// taxonomy so the shared catch path renders the legacy BF_* text byte for byte.
func translateWorkerError(err error) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	for _, class := range []string{ClassInvalid, ClassBlocked, ClassConflict, ClassFail} {
		if strings.HasPrefix(text, class+": ") {
			return &Error{Class: class, Message: strings.TrimPrefix(text, class+": ")}
		}
	}
	return blockedf("%v", err)
}

// workerProfileFromState decodes the validated execution_profile view into the
// worker library's sealed profile type.
func workerProfileFromState(profile any) (worker.ExecutionProfile, error) {
	object, ok := asObject(profile)
	if !ok {
		return worker.ExecutionProfile{}, invalidf("execution_profile must be an object.")
	}
	sandbox := asMap(object["sandbox"])
	toolset := asMap(object["toolset"])
	decoded := worker.ExecutionProfile{
		Provider:          asStringOr(object["provider"]),
		Executable:        asStringOr(object["executable"]),
		ExecutableSHA256:  asStringOr(object["executable_sha256"]),
		Sandbox:           worker.SandboxIdentity{Executable: asStringOr(sandbox["executable"]), SHA256: asStringOr(sandbox["sha256"])},
		Toolset:           worker.ToolsetIdentity{Name: asStringOr(toolset["name"]), Root: asStringOr(toolset["root"]), SHA256: asStringOr(toolset["sha256"])},
		DeniedReadRoots:   stringList(object["denied_read_roots"]),
		CodexSkillsSHA256: asStringOr(getValue(object, "codex_skills_sha256", nil)),
	}
	if unica := getValue(object, "unica", nil); unica != nil {
		unicaObject := asMap(unica)
		decoded.Unica = &worker.UnicaIdentity{
			PluginRoot:      asStringOr(unicaObject["plugin_root"]),
			BootstrapSHA256: asStringOr(unicaObject["bootstrap_sha256"]),
			ManifestSHA256:  asStringOr(unicaObject["manifest_sha256"]),
			RuntimeCache:    asStringOr(unicaObject["runtime_cache"]),
			AllowedTools:    stringList(unicaObject["allowed_tools"]),
		}
	}
	if runtime := getValue(object, "runtime", nil); runtime != nil {
		runtimeObject := asMap(runtime)
		pin := &worker.RuntimePin{
			Executable: asStringOr(runtimeObject["executable"]),
			SHA256:     asStringOr(runtimeObject["sha256"]),
			Version:    asStringOr(runtimeObject["version"]),
		}
		packages, _ := asArray(runtimeObject["packages"])
		for _, rawPackage := range packages {
			pkg, _ := asObject(rawPackage)
			pin.Packages = append(pin.Packages, worker.RuntimePackage{
				Name: asStringOr(pkg["name"]), Version: asStringOr(pkg["version"]),
			})
		}
		decoded.Runtime = pin
	}
	return decoded, nil
}

func workerModelsFromState(request map[string]any) worker.WorkerModels {
	models := asMap(request["models"])
	return worker.WorkerModels{
		Worker:         asStringOr(models["worker"]),
		WorkerEffort:   asStringOr(models["worker_effort"]),
		Reviewer:       asStringOr(models["reviewer"]),
		ReviewerEffort: asStringOr(models["reviewer_effort"]),
	}
}

// runtimePreflight ports Test-BFRuntimePreflight: only the exact pinned
// interpreter runs before a paid dispatch, with content-addressed evidence
// under the provider artifact root.
func runtimePreflight(ctx context.Context, deps Deps, state map[string]any, directory, evidenceRoot string) error {
	request, _ := asObject(state["request"])
	profile := asMap(getValue(request, "execution_profile", nil))
	if profile == nil || asStringOr(asMap(profile["toolset"])["name"]) != "cc-1c-skills" {
		return nil
	}
	runtime := asMap(profile["runtime"])
	executable, err := safePath(asStringOr(runtime["executable"]))
	if err != nil {
		return err
	}
	executableHash, err := hashFile(executable)
	if err != nil {
		return err
	}
	if executableHash != asStringOr(runtime["sha256"]) {
		return blockedf("pinned cc-1c-skills runtime executable changed before preflight.")
	}
	packages, _ := asArray(runtime["packages"])
	packageValues := make([]any, 0, len(packages))
	names := make([]any, 0, len(packages))
	for _, rawPackage := range packages {
		pkg, _ := asObject(rawPackage)
		packageValues = append(packageValues, map[string]any{"name": pkg["name"], "version": pkg["version"]})
		names = append(names, pkg["name"])
	}
	expected := map[string]any{"executable": executable, "sha256": runtime["sha256"], "version": runtime["version"], "packages": packageValues}
	identity, err := hashValue(expected)
	if err != nil {
		return err
	}
	root, err := safePath(evidenceRoot)
	if err != nil {
		return err
	}
	evidencePath := filepath.Join(root, "preflight-"+identity+".json")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return blockedf("%v", err)
	}
	probeDirectory, err := os.MkdirTemp(root, "probe-"+identity+"-")
	if err != nil {
		return blockedf("%v", err)
	}
	probe := "import json,sys\ntry:\n    from importlib import metadata\nexcept Exception:\n    metadata=None\nrequired=json.loads(sys.argv[1])\nversions={}\nfor name in required:\n    try:\n        versions[name]=metadata.version(name) if metadata is not None else None\n    except Exception:\n        versions[name]=None\nsys.stdout.write(json.dumps({\"sys_executable\":sys.executable,\"version\":\"%d.%d.%d\"%sys.version_info[:3],\"packages\":versions}))"
	probePath := filepath.Join(probeDirectory, "runtime-probe.py")
	if err := os.WriteFile(probePath, []byte(probe), 0o644); err != nil {
		return blockedf("%v", err)
	}
	probeOutput := filepath.Join(probeDirectory, "runtime-probe")
	namesJSON, err := json.Marshal(names)
	if err != nil {
		return invalidf("%v", err)
	}
	process, err := deps.runProcess(ctx, ProcessOptions{
		Executable:       executable,
		Arguments:        []string{"-I", probePath, string(namesJSON)},
		WorkingDirectory: asStringOr(state["worker_path"]),
		OutputDirectory:  probeOutput,
		TimeoutSeconds:   60,
		CleanEnvironment: true,
		MaxOutputBytes:   65536,
	})
	if err != nil {
		return err
	}
	if process.StopReason != "" || process.ExitCode != 0 {
		return blockedf("pinned cc-1c-skills runtime preflight did not finish.")
	}
	output, err := repository.StageHostReadFileBytes(process.Stdout)
	if err != nil {
		return blockedf("%v", err)
	}
	text := strings.TrimSpace(string(output))
	if text == "" {
		return blockedf("pinned runtime preflight produced no evidence.")
	}
	observed, err := repository.DecodeObject([]byte(text))
	if err != nil {
		return blockedf("pinned runtime preflight produced invalid JSON.")
	}
	if _, err := assertFields(observed, []string{"sys_executable", "version", "packages"}, nil, "runtime_preflight_observation"); err != nil {
		return err
	}
	observedExe, err := safePath(asStringOr(observed["sys_executable"]))
	if err != nil {
		return err
	}
	if !strings.EqualFold(observedExe, executable) {
		return blockedf("pinned runtime reported a different interpreter.")
	}
	if asStringOr(observed["version"]) != asStringOr(runtime["version"]) {
		return blockedf("pinned runtime version differs from the trusted request.")
	}
	observedPackages := []any{}
	for _, rawPackage := range packages {
		pkg, _ := asObject(rawPackage)
		observedMap := asMap(observed["packages"])
		actual, present := observedMap[asStringOr(pkg["name"])]
		if !present || asStringOr(actual) != asStringOr(pkg["version"]) {
			return blockedf("pinned runtime package %s differs from the trusted request.", asStringOr(pkg["name"]))
		}
		observedPackages = append(observedPackages, map[string]any{"name": pkg["name"], "version": asStringOr(actual)})
	}
	probeHash, err := hashFile(probePath)
	if err != nil {
		return err
	}
	return writeJSON(evidencePath, map[string]any{
		"declared": expected,
		"observed": map[string]any{
			"sys_executable": observedExe, "version": asStringOr(observed["version"]), "packages": observedPackages,
		},
		"probe_sha256":   probeHash,
		"checked_at_utc": deps.now().UTC().Format("2006-01-02T15:04:05.0000000Z"),
	}, true)
}

// providerDispatchKey mirrors Get-BFProviderDispatchKey.
func providerDispatchKey(pc *providerContextInfo, directory string) (string, error) {
	root, err := safePath(pc.artifactRoot)
	if err != nil {
		return "", err
	}
	full, err := safePath(directory)
	if err != nil {
		return "", err
	}
	rootTrimmed := strings.TrimRight(root, `\/`)
	if !strings.EqualFold(full, rootTrimmed) && !nestedPath(full, rootTrimmed) {
		return "", invalidf("provider dispatch directory escaped artifact_root.")
	}
	relative := filepath.ToSlash(strings.TrimPrefix(full[len(rootTrimmed):], string(filepath.Separator)))
	if strings.TrimSpace(relative) == "" {
		return "", invalidf("provider dispatch directory is empty.")
	}
	return "attempts/" + pc.attemptID + "/provider/" + relative, nil
}

// providerBudgetLedger mirrors Get-BFProviderBudgetLedger.
func providerBudgetLedger(pc *providerContextInfo) (map[string]any, error) {
	artifactPath := filepath.Join(pc.artifactRoot, "budget", "ledger.json")
	var ledger map[string]any
	if isRegularFile(artifactPath) {
		object, err := readJSONObject(artifactPath)
		if err != nil {
			return nil, err
		}
		ledger = object
	} else if isRegularFile(filepath.Join(pc.contextRoot, "budget", "ledger.json")) {
		object, err := getProviderContextArtifact(pc.contextRoot, "budget/ledger.json", pc.priorArtifacts)
		if err != nil {
			return nil, err
		}
		ledger = object
	} else {
		ledger = map[string]any{"schema_version": int64(1), "task_id": pc.taskID, "entries": []any{}}
	}
	if _, err := assertFields(ledger, []string{"schema_version", "task_id", "entries"}, nil, "provider_budget_ledger"); err != nil {
		return nil, blockedf("%s", unwrapMessage(err))
	}
	version, _ := asInteger(ledger["schema_version"])
	entries, entriesOK := asArray(ledger["entries"])
	if version != 1 || ledger["task_id"] != pc.taskID || !entriesOK {
		return nil, blockedf("corrupt provider budget ledger.")
	}
	for _, raw := range entries {
		entry, err := assertFields(raw, []string{"kind", "dispatch"}, []string{"provider", "stage", "requested_model", "observed_model", "reservation_usd", "reported_cost_usd", "billed_cost_usd", "cost_state", "usage", "terminal_status", "currency", "at"}, "provider_budget_entry")
		if err != nil {
			return nil, blockedf("%s", unwrapMessage(err))
		}
		if asStringOr(entry["kind"]) != "reservation" && asStringOr(entry["kind"]) != "outcome" {
			return nil, blockedf("invalid provider budget entry kind.")
		}
	}
	return ledger, nil
}

func providerBudgetEntries(ledger map[string]any) []any {
	entries, _ := asArray(ledger["entries"])
	return entries
}

// providerBudgetSummary mirrors Get-BFProviderBudgetSummary.
func providerBudgetSummary(ledger map[string]any) (map[string]any, error) {
	reservations := map[string]bool{}
	outcomes := map[string]map[string]any{}
	spent := 0.0
	open := 0
	for _, raw := range providerBudgetEntries(ledger) {
		entry, _ := asObject(raw)
		key := asStringOr(entry["dispatch"])
		if asStringOr(entry["kind"]) == "reservation" {
			reservations[key] = true
		} else if _, exists := outcomes[key]; exists {
			return nil, blockedf("duplicate provider budget outcome.")
		} else {
			outcomes[key] = entry
		}
	}
	for key := range reservations {
		if _, exists := outcomes[key]; !exists {
			open++
		}
	}
	unknown := 0
	for _, entry := range outcomes {
		if asStringOr(entry["cost_state"]) == "known" && entry["reported_cost_usd"] != nil {
			value, _ := asFloat(entry["reported_cost_usd"])
			spent += value
		} else {
			unknown++
		}
	}
	return map[string]any{
		"spent_usd": math.Round(spent*1e10) / 1e10, "open": int64(open), "unknown": int64(unknown),
		"reservations": int64(len(reservations)), "outcomes": int64(len(outcomes)),
	}, nil
}

// budgetEntryCore mirrors Get-BFBudgetEntryCore: the 'at' timestamp is not
// part of reservation/outcome idempotency.
func budgetEntryCore(entry map[string]any) map[string]any {
	core := map[string]any{}
	for key, value := range entry {
		if key == "at" {
			continue
		}
		core[key] = value
	}
	return core
}

func addProviderBudgetEntry(pc *providerContextInfo, entry map[string]any) error {
	ledger, err := providerBudgetLedger(pc)
	if err != nil {
		return err
	}
	ledger["entries"] = append(providerBudgetEntries(ledger), entry)
	return writeJSON(filepath.Join(pc.artifactRoot, "budget", "ledger.json"), ledger, true)
}

// assertProviderBudgetAdmission mirrors Assert-BFProviderBudgetAdmission.
func assertProviderBudgetAdmission(state map[string]any, pc *providerContextInfo, directory string) error {
	request, _ := asObject(state["request"])
	budget := getValue(request, "budget", nil)
	if budget == nil {
		return nil
	}
	key, err := providerDispatchKey(pc, directory)
	if err != nil {
		return err
	}
	ledger, err := providerBudgetLedger(pc)
	if err != nil {
		return err
	}
	existing := false
	for _, raw := range providerBudgetEntries(ledger) {
		entry, _ := asObject(raw)
		if asStringOr(entry["dispatch"]) == key {
			existing = true
		}
	}
	if !existing {
		summary, err := providerBudgetSummary(ledger)
		if err != nil {
			return err
		}
		open, _ := asInteger(summary["open"])
		if open > 0 {
			return blockedf("an unresolved provider paid dispatch exists.")
		}
		if limit := getValue(asMap(budget), "limit", nil); limit != nil {
			unknown, _ := asInteger(summary["unknown"])
			if unknown > 0 {
				return blockedf("a provider paid dispatch has unknown cost.")
			}
			spent, _ := asFloat(summary["spent_usd"])
			limitValue, limitOK := asFloat(limit)
			reservation, reservationOK := asFloat(getValue(asMap(budget), "reservation", int64(0)))
			if limitOK && reservationOK && spent+reservation > limitValue+1e-9 {
				return blockedf("provider budget limit would be exceeded.")
			}
		}
	}
	return nil
}

// addProviderBudgetReservation mirrors Add-BFProviderBudgetReservation.
func addProviderBudgetReservation(deps Deps, state map[string]any, pc *providerContextInfo, directory, providerName, stage, requestedModel string) error {
	request, _ := asObject(state["request"])
	budget := getValue(request, "budget", nil)
	if budget == nil {
		return nil
	}
	key, err := providerDispatchKey(pc, directory)
	if err != nil {
		return err
	}
	entry := map[string]any{
		"kind": "reservation", "dispatch": key, "provider": providerName, "stage": stage,
		"requested_model": requestedModel, "reservation_usd": getValue(asMap(budget), "reservation", int64(0)),
		"currency": getValue(asMap(budget), "currency", nil),
		"at":       deps.now().UTC().Format("2006-01-02T15:04:05.0000000Z"),
	}
	ledger, err := providerBudgetLedger(pc)
	if err != nil {
		return err
	}
	for _, raw := range providerBudgetEntries(ledger) {
		existing, _ := asObject(raw)
		if asStringOr(existing["kind"]) == "reservation" && asStringOr(existing["dispatch"]) == key {
			existingHash, err := hashValue(budgetEntryCore(existing))
			if err != nil {
				return err
			}
			entryHash, err := hashValue(budgetEntryCore(entry))
			if err != nil {
				return err
			}
			if existingHash != entryHash {
				return blockedf("conflicting provider budget reservation.")
			}
			return nil
		}
	}
	return addProviderBudgetEntry(pc, entry)
}

// completeProviderBudgetDispatch mirrors Complete-BFProviderBudgetDispatch.
func completeProviderBudgetDispatch(deps Deps, state map[string]any, pc *providerContextInfo, directory, providerName, stage, requestedModel string) error {
	request, _ := asObject(state["request"])
	budget := getValue(request, "budget", nil)
	if budget == nil {
		return nil
	}
	key, err := providerDispatchKey(pc, directory)
	if err != nil {
		return err
	}
	reported := any(nil)
	observed := any(nil)
	usage := any(nil)
	costState := "unknown"
	terminal := "unknown"
	hostPath := filepath.Join(directory, "host-result.json")
	if isRegularFile(hostPath) {
		metadata, err := readJSONObject(hostPath)
		if err != nil {
			return err
		}
		usage = getValue(metadata, "usage", nil)
		observed = getValue(metadata, "observed_model", nil)
		if cost := getValue(metadata, "reported_cost_usd", nil); cost != nil {
			if _, ok := asFloat(cost); !ok {
				return blockedf("invalid reported provider cost.")
			}
			reported = cost
			costState = "known"
		}
		terminal = "completed"
	}
	candidate := map[string]any{
		"kind": "outcome", "dispatch": key, "provider": providerName, "stage": stage,
		"requested_model": requestedModel, "observed_model": observed,
		"reported_cost_usd": reported, "billed_cost_usd": nil, "cost_state": costState,
		"usage": usage, "terminal_status": terminal, "currency": getValue(asMap(budget), "currency", nil),
		"at": deps.now().UTC().Format("2006-01-02T15:04:05.0000000Z"),
	}
	ledger, err := providerBudgetLedger(pc)
	if err != nil {
		return err
	}
	for _, raw := range providerBudgetEntries(ledger) {
		existing, _ := asObject(raw)
		if asStringOr(existing["kind"]) == "outcome" && asStringOr(existing["dispatch"]) == key {
			existingHash, err := hashValue(budgetEntryCore(existing))
			if err != nil {
				return err
			}
			candidateHash, err := hashValue(budgetEntryCore(candidate))
			if err != nil {
				return err
			}
			if existingHash != candidateHash {
				return blockedf("conflicting provider budget outcome.")
			}
			return nil
		}
	}
	return addProviderBudgetEntry(pc, candidate)
}

// getProviderContextArtifactText is the -AsText form of
// Get-BFProviderContextArtifact: a prior-artifact-bound raw read.
func getProviderContextArtifactText(contextRoot, relative string, declared map[string]map[string]any) (string, error) {
	resolved, err := safePath(contextRoot)
	if err != nil {
		return "", err
	}
	if err := assertRelativePath(relative); err != nil {
		return "", err
	}
	if regexp.MustCompile(`(^|[\\/]).(git|bsl-flow)([\\/]|$)`).MatchString(relative) {
		return "", blockedf("provider context may not expose controller paths.")
	}
	full, err := safePath(filepath.Join(resolved, filepath.FromSlash(relative)))
	if err != nil {
		return "", err
	}
	root := strings.TrimRight(resolved, `\/`)
	if !strings.EqualFold(full, root) && !insideCanonical(full, root) {
		return "", invalidf("context artifact escaped context_root.")
	}
	key := strings.ReplaceAll(relative, `\`, "/")
	entry, ok := declared[key]
	if !ok {
		return "", blockedf("context artifact was not declared by core: %s", key)
	}
	if !isRegularFile(full) {
		return "", blockedf("declared context artifact is missing: %s", key)
	}
	info, err := os.Lstat(full)
	if err != nil {
		return "", blockedf("%v", err)
	}
	size, _ := asInteger(entry["size_bytes"])
	if info.Size() != size {
		return "", blockedf("context artifact size changed: %s", key)
	}
	data, err := repository.StageHostReadFileBytes(full)
	if err != nil {
		return "", blockedf("%v", err)
	}
	if hashFileBytes(data) != asStringOr(entry["sha256"]) {
		return "", blockedf("context artifact bytes changed: %s", key)
	}
	return string(data), nil
}

// assertProviderRepairFailure mirrors Assert-BFProviderRepairFailure.
func assertProviderRepairFailure(state map[string]any, attemptID string, pc *providerContextInfo) error {
	if err := assertUUID(attemptID); err != nil {
		return err
	}
	request, _ := asObject(state["request"])
	repair := getValue(state, "repair", nil)
	maxRepairs, _ := asInteger(getValue(request, "max_source_repairs", int64(0)))
	rounds := int64(-1)
	if repair != nil {
		if value, ok := asInteger(getValue(asMap(repair), "rounds", int64(0))); ok {
			rounds = value
		}
	}
	if repair == nil || rounds >= maxRepairs {
		return blockedf("source repair budget exhausted.")
	}
	var entry map[string]any
	count := 0
	evidence, _ := asArray(state["evidence"])
	for _, raw := range evidence {
		candidate, _ := asObject(raw)
		if asStringOr(candidate["attempt_id"]) == attemptID {
			entry = candidate
			count++
		}
	}
	if count != 1 || asStringOr(entry["stage"]) != "verify" || asStringOr(entry["outcome"]) != "FAIL" {
		return blockedf("repair requires a registered failed verification.")
	}
	failure, err := getProviderContextArtifact(pc.contextRoot, "attempts/"+attemptID+"/result.json", pc.priorArtifacts)
	if err != nil {
		return err
	}
	failureHash, err := hashValue(failure)
	if err != nil {
		return err
	}
	if failureHash != asStringOr(entry["result_sha256"]) || asStringOr(failure["side_effects"]) != "none" ||
		getValue(asMap(failure["proposal"]), "repair_eligible", false) != true {
		return blockedf("failed verification is not a trusted safe repair input.")
	}
	current, err := stageDependencies(state, "verify")
	if err != nil {
		return err
	}
	currentHash, err := hashValue(current)
	if err != nil {
		return err
	}
	failureDependencyHash, err := hashValue(failure["dependencies"])
	if err != nil {
		return err
	}
	if currentHash != failureDependencyHash {
		return blockedf("failed verification inputs changed before diagnosis.")
	}
	for _, raw := range anyItemsOf(entry["raw_hashes"]) {
		rawHash, _ := asObject(raw)
		relative := asStringOr(rawHash["path"])
		if !isAbsolutePath(relative) {
			if err := assertRelativePath(relative); err != nil {
				return err
			}
			relative = strings.ReplaceAll(relative, `\`, "/")
		} else {
			root, err := safePath(pc.contextRoot)
			if err != nil {
				return err
			}
			full, err := safePath(relative)
			if err != nil {
				return err
			}
			if !strings.EqualFold(full, root) && !insideCanonical(full, root) {
				return blockedf("absolute provider artifact escaped the declared context root.")
			}
			relative = filepath.ToSlash(strings.TrimPrefix(strings.TrimPrefix(full, strings.TrimRight(root, `\/`)), string(filepath.Separator)))
			if err := assertRelativePath(relative); err != nil {
				return err
			}
		}
		expectedPrefix := "attempts/" + attemptID + "/"
		if !strings.HasPrefix(strings.ToLower(relative), strings.ToLower(expectedPrefix)) {
			return blockedf("provider artifact is not bound to the requested attempt.")
		}
		declaredEntry, ok := pc.priorArtifacts[relative]
		if !ok {
			return blockedf("provider artifact was not declared by core: %s", relative)
		}
		expected := asStringOr(rawHash["sha256"])
		if expected != "" {
			if err := assertSHA256(expected, "provider artifact sha256"); err != nil {
				return err
			}
			if asStringOr(declaredEntry["sha256"]) != expected {
				return blockedf("provider artifact hash binding differs: %s", relative)
			}
		}
		fullPath := filepath.Join(pc.contextRoot, filepath.FromSlash(relative))
		hash, err := hashFile(fullPath)
		if err != nil {
			return err
		}
		if hash != asStringOr(rawHash["sha256"]) {
			return blockedf("retained failed verification evidence changed.")
		}
	}
	return nil
}

func anyItemsOf(value any) []any {
	items, _ := asArray(value)
	return items
}

// assertProviderProtectedTests mirrors Assert-BFProviderProtectedTests.
func assertProviderProtectedTests(state map[string]any, pc *providerContextInfo) error {
	id := getValue(asMap(getValue(state, "repair", nil)), "diagnosis_attempt", nil)
	if id == nil {
		return nil
	}
	if err := assertUUID(id); err != nil {
		return err
	}
	var entry map[string]any
	count := 0
	evidence, _ := asArray(state["evidence"])
	for _, raw := range evidence {
		candidate, _ := asObject(raw)
		if asStringOr(candidate["attempt_id"]) == asStringOr(id) {
			entry = candidate
			count++
		}
	}
	diagnosis, err := getProviderContextArtifact(pc.contextRoot, "attempts/"+asStringOr(id)+"/result.json", pc.priorArtifacts)
	if err != nil {
		return err
	}
	diagnosisHash, err := hashValue(diagnosis)
	if err != nil {
		return err
	}
	if count != 1 || diagnosisHash != asStringOr(entry["result_sha256"]) {
		return blockedf("retained repair diagnosis changed.")
	}
	failureID := asStringOr(asMap(diagnosis["proposal"])["failure_attempt_id"])
	if err := assertUUID(failureID); err != nil {
		return err
	}
	failure, err := getProviderContextArtifact(pc.contextRoot, "attempts/"+failureID+"/start.json", pc.priorArtifacts)
	if err != nil {
		return err
	}
	failureManifest := asMap(failure["source_manifest"])
	manifestFilesHash, err := hashValue(failureManifest["files"])
	if err != nil {
		return err
	}
	if failure["task_id"] != state["task_id"] || failure["attempt_id"] != failureID ||
		asStringOr(failureManifest["sha256"]) != asStringOr(asMap(diagnosis["dependencies"])["source"]) ||
		manifestFilesHash != asStringOr(failureManifest["sha256"]) {
		return blockedf("protected test baseline does not match the diagnosed failure.")
	}
	before, err := protectedTestManifest(state, failureManifest)
	if err != nil {
		return err
	}
	current, err := stageSourceManifest(state)
	if err != nil {
		return err
	}
	after, err := protectedTestManifest(state, current)
	if err != nil {
		return err
	}
	beforeHash, err := hashValue(before)
	if err != nil {
		return err
	}
	afterHash, err := hashValue(after)
	if err != nil {
		return err
	}
	if beforeHash != afterHash {
		return blockedf("automatic repair changed protected test inputs; a trusted test-contract revision is required.")
	}
	return nil
}
