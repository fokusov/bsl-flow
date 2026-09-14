package councilengine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// cycle.go ports Invoke-CouncilReview.ps1: role bindings, the live dispatch
// cycle, chair fan-in and review v2 assembly. The transport and the
// current-agent fallback are injected by the host through Dispatcher; this
// package owns every byte-parity-critical artifact.

// RoleBinding is one bound council role (Get-BSLFlowCouncilRoleBindings).
type RoleBinding struct {
	RoleName   string
	Role       RoleConfig
	Profile    ModelConfig
	Provider   ProviderConfig
	Credential Credential
	Binding    *Binding
	// Decorated by the cycle:
	Snapshot           *Snapshot
	CostEstimateUSD    *float64
	RequestTimeoutSecs int
}

// Route is the resolved dispatch route for a role.
type Route struct {
	Route      string // direct_api | current_agent_fallback | blocked
	Credential string
	Reason     string
	Capability any
}

// DispatchResult is a terminal provider dispatch result.
type DispatchResult struct {
	Status          string // completed | failed_before_acceptance | unknown_after_dispatch | cancelled | invalid_response
	Payload         any
	Observed        Observed
	Usage           *MemberUsage
	ExecutionMode   string
	FallbackReason  *string
	ReportedCostUSD *float64
}

// Dispatcher performs one live dispatch. The host wires the direct API
// transport and the current-agent fallback; a failure returns a classified
// error whose message carries one of the BF_* dispatch markers.
type Dispatcher interface {
	Dispatch(ctx context.Context, attempt *Attempt, prompt string, route *Route, timeoutSeconds int) (DispatchResult, error)
}

// CycleOptions carries the council cycle inputs and host seams.
type CycleOptions struct {
	ProjectRoot       string
	ChangeName        string
	EvidenceText      string
	MaxInputBytes     int
	Policy            *CouncilPolicy
	PolicyHash        string
	Snapshot          *Snapshot
	RunRoot           string
	AllowLiveDispatch bool
	Dispatcher        Dispatcher
	Cancelled         func() (bool, error)
	// BeforeDispatch/AfterDispatch are the provider-owned budget hooks; when
	// nil the council budget ledger owns admission/reservation/outcome.
	BeforeDispatch func(attempt *Attempt, route *Route) error
	AfterDispatch  func(attempt *Attempt, route *Route, result *DispatchResult, status, failure string) error
	// Capabilities is the per-role current-agent capability receipt; a
	// tokenless role needs one, otherwise the role is blocked.
	Capabilities map[string]any
}

// CycleResult is the outcome of RunCycle.
type CycleResult struct {
	Review          *ordered
	RunRoot         string
	FinalValidation map[string]any
}

// BuildRoleBindings ports Get-BSLFlowCouncilRoleBindings: for every enabled
// role, resolve provider/model/endpoint (with local overlay), credential and
// the frozen binding with its input hashes.
func BuildRoleBindings(projectRoot string, policy *CouncilPolicy, snapshot *Snapshot) ([]*RoleBinding, error) {
	overlay, err := LocalProviderOverlay(projectRoot)
	if err != nil {
		return nil, err
	}
	var localText string
	localPath := filepath.Join(projectRoot, ".bsl-flow", "providers.local.yaml")
	if data, err := os.ReadFile(localPath); err == nil {
		localText = string(data)
	}
	entries := []*RoleBinding{}
	for _, roleName := range councilRoleOrder {
		role := policy.Roles[roleName]
		if !role.Enabled {
			continue
		}
		profile, ok := policy.Models[role.Model]
		if !ok {
			return nil, blocked("enabled role %s references an unknown model profile.", roleName)
		}
		provider, ok := policy.Providers[profile.Provider]
		if !ok {
			return nil, blocked("model profile %s references an unknown provider.", role.Model)
		}
		endpoint := provider.Endpoint
		localToken := ""
		if entry, present := overlay[profile.Provider]; present {
			if entry.HasToken {
				localToken, err = yamlValue(localText, []string{"providers", profile.Provider, "token"}, "")
				if err != nil {
					return nil, err
				}
			}
			if strings.TrimSpace(entry.BaseURL) != "" {
				localEndpoint, err := assertEndpointURL(entry.BaseURL, "providers.local."+profile.Provider, policy.AllowLocalHTTP)
				if err != nil {
					return nil, err
				}
				endpoint = localEndpoint
			}
		}
		credential := ResolveCredential(profile.Provider, provider.TokenEnv, localToken)
		rubricText := roleRubric(roleName)
		rubricHash := sha256Hex([]byte(rubricText))
		binding := &Binding{
			Provider:                   profile.Provider,
			Model:                      profile.Model,
			Effort:                     profile.Effort,
			Protocol:                   provider.Protocol,
			Endpoint:                   endpoint,
			TransportCapabilityVersion: provider.TransportCapabilityVersion,
			PromptVersion:              promptVersionV2,
			MemberSchemaVersion:        memberSchemaVersion,
			InputHashes: orderedFrom(
				[]string{"original_task_sha256", "spec_sha256", "design_sha256", "evidence_sha256", "policy_hash", "rubric_sha256"},
				[]any{
					snapshot.OriginalTaskSHA256, snapshot.SpecSHA256,
					nullableString(snapshot.DesignSHA256), nullableString(snapshot.EvidenceSHA256),
					snapshot.PolicyHash, rubricHash,
				},
			),
		}
		entries = append(entries, &RoleBinding{
			RoleName: roleName, Role: role, Profile: profile, Provider: provider,
			Credential: credential, Binding: binding,
		})
	}
	return entries, nil
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// resolveRoute ports the fallback policy resolution of
// Invoke-BSLFlowCouncilDispatchRole.
func resolveRoute(role RoleConfig, credential Credential, capability any) (*Route, string, error) {
	route := &Route{Route: "direct_api", Credential: credential.Token}
	executionMode := "direct_api"
	if credential.CredentialSource == "missing" {
		if role.Fallback == councilFallbackBlock {
			return nil, "", blocked("role credential is missing and fallback policy is block.")
		}
		if capability == nil {
			return nil, "", blocked("fallback needs a trusted capability receipt before dispatch.")
		}
		route.Route = "current_agent_fallback"
		route.Reason = "credential_missing"
		route.Capability = capability
		executionMode = "current_agent_fallback"
	}
	return route, executionMode, nil
}

// dispatchRole ports Invoke-BSLFlowCouncilDispatchRole: attempt resolution,
// retained-result reuse, route, prompt, dispatch and terminal classification.
func (e *Engine) dispatchRole(ctx context.Context, opts *CycleOptions, entry *RoleBinding, capability any) (*ordered, any, bool, error) {
	runRoot := opts.RunRoot
	roleName := entry.RoleName
	route, executionMode, err := resolveRoute(entry.Role, entry.Credential, capability)
	if err != nil {
		return nil, nil, false, err
	}
	binding := entry.Binding
	if executionMode == "current_agent_fallback" {
		capabilityBinding, err := capabilityBinding(route.Capability)
		if err != nil {
			return nil, nil, false, err
		}
		clone := *binding
		clone.FallbackCapability = capabilityBinding
		binding = &clone
	}
	attempt, err := e.NewAttempt(runRoot, roleName, binding)
	if err != nil {
		return nil, nil, false, err
	}
	retained, err := RoleResult(runRoot, roleName, attempt)
	if err != nil {
		return nil, nil, false, err
	}
	if retained != nil {
		envelope, _ := asOrdered(retained["envelope"])
		status := asStringOr(envelope.get("status"))
		if status == "completed" {
			return envelope, anyOrdered(retained["payload"]), true, nil
		}
		if status == "unknown_after_dispatch" {
			return nil, nil, false, blocked("retained %s attempt %s is unknown_after_dispatch; provider reconciliation is required before any new dispatch.", roleName, attempt.AttemptID)
		}
		if entry.Role.Required {
			return nil, nil, false, blocked("required role has a retained non-completed result: %s (%s)", roleName, status)
		}
		return envelope, nil, false, nil
	}
	view, err := entry.Snapshot.RoleView(roleName, roleRubric(roleName))
	if err != nil {
		return nil, nil, false, err
	}
	prompt := buildPrompt(roleName, roleViewValueFromOrdered(view))
	if opts.Cancelled != nil {
		if cancelled, err := opts.Cancelled(); err != nil {
			return nil, nil, false, err
		} else if cancelled {
			envelope, err := e.RegisterMemberResult(runRoot, MemberResult{
				Attempt: attempt, Status: "cancelled", Observed: Observed{},
				ExecutionMode: executionMode, Summary: "Council cancelled before role dispatch.",
			})
			if err != nil {
				return nil, nil, false, err
			}
			if entry.Role.Required {
				return nil, nil, false, blocked("council cancelled before %s dispatch.", roleName)
			}
			return envelope, nil, false, nil
		}
	}
	dispatchedAt := e.now()
	if opts.BeforeDispatch != nil {
		if err := opts.BeforeDispatch(attempt, route); err != nil {
			return nil, nil, false, err
		}
	} else {
		if _, err := e.ApproveBudgetDispatch(runRoot, roleName, attempt, entry.CostEstimateUSD, opts.Policy.Budget); err != nil {
			return nil, nil, false, err
		}
	}
	result, err := opts.Dispatcher.Dispatch(ctx, attempt, prompt, route, entry.RequestTimeoutSecs)
	completedAt := e.now()
	if err != nil {
		return nil, nil, false, e.classifyDispatchFailure(opts, entry, attempt, route, executionMode, dispatchedAt, completedAt, err)
	}
	if result.Status != "completed" {
		status := result.Status
		if !memberStatusSet[status] {
			status = "unknown_after_dispatch"
		}
		costState := "unknown"
		if status == "failed_before_acceptance" {
			costState = "no_usage_reported"
		}
		failureSummary := fmt.Sprintf("Dispatch returned %s.", status)
		if opts.AfterDispatch != nil {
			if err := opts.AfterDispatch(attempt, route, &result, status, failureSummary); err != nil {
				status = "unknown_after_dispatch"
				costState = "unknown"
				failureSummary = "AfterDispatch hook failed while recording the provider outcome."
			}
		}
		envelope, err := e.RegisterMemberResult(runRoot, MemberResult{
			Attempt: attempt, Status: status, Observed: Observed{},
			ExecutionMode: executionMode, Summary: truncate(failureSummary, 400),
			DispatchedAt: &dispatchedAt, CompletedAt: &completedAt, CostState: costState,
		})
		if err != nil {
			return nil, nil, false, err
		}
		if opts.BeforeDispatch == nil {
			if _, err := e.CompleteBudgetOutcome(runRoot, roleName, attempt, status, nil, costState); err != nil {
				return nil, nil, false, err
			}
		}
		if entry.Role.Required {
			return nil, nil, false, blocked("required role did not complete: %s (%s)", roleName, status)
		}
		return envelope, nil, false, nil
	}
	costState := "no_usage_reported"
	if result.Usage != nil {
		costState = "provider_usage_reported"
	}
	if opts.AfterDispatch != nil {
		if err := opts.AfterDispatch(attempt, route, &result, "completed", ""); err != nil {
			envelope, err := e.RegisterMemberResult(runRoot, MemberResult{
				Attempt: attempt, Status: "unknown_after_dispatch",
				Observed: Observed{}, ExecutionMode: executionMode,
				Summary:      "AfterDispatch hook failed while recording the provider outcome.",
				DispatchedAt: &dispatchedAt, CompletedAt: &completedAt, CostState: "unknown",
			})
			if err != nil {
				return nil, nil, false, err
			}
			if entry.Role.Required {
				return nil, nil, false, blocked("required role did not complete: %s (unknown_after_dispatch)", roleName)
			}
			return envelope, nil, false, nil
		}
	}
	envelope, err := e.RegisterMemberResult(runRoot, MemberResult{
		Attempt: attempt, Payload: result.Payload, Status: "completed",
		Observed: result.Observed, ExecutionMode: result.ExecutionMode, Summary: fmt.Sprintf("Role %s completed its structured result.", roleName),
		FallbackReason: result.FallbackReason, DispatchedAt: &dispatchedAt, CompletedAt: &completedAt,
		Usage: result.Usage, CostState: costState,
	})
	if err != nil {
		return nil, nil, false, err
	}
	if opts.BeforeDispatch == nil {
		if _, err := e.CompleteBudgetOutcome(runRoot, roleName, attempt, "completed", result.ReportedCostUSD, costState); err != nil {
			return nil, nil, false, err
		}
	}
	return envelope, result.Payload, false, nil
}

func roleViewValueFromOrdered(view *ordered) roleViewValue {
	value := roleViewValue{
		role:         asStringOr(view.get("role")),
		originalTask: asStringOr(view.get("original_task")),
		evidence:     asStringOr(view.get("evidence")),
		aggregates:   asStringOr(view.get("aggregates")),
	}
	if spec, ok := view.get("spec").(string); ok && spec != "" {
		value.spec = &spec
	}
	if design, ok := view.get("design").(string); ok && design != "" {
		value.design = &design
	}
	return value
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit]
}

// classifyDispatchFailure maps a dispatcher error onto a terminal member
// state, mirroring Invoke-BSLFlowCouncilDispatchRole catch blocks.
func (e *Engine) classifyDispatchFailure(opts *CycleOptions, entry *RoleBinding, attempt *Attempt, route *Route, executionMode string, dispatchedAt, completedAt time.Time, dispatchErr error) error {
	failure := dispatchErr.Error()
	status := "unknown_after_dispatch"
	if strings.Contains(failure, MarkerBeforeAcceptance) {
		status = "failed_before_acceptance"
	} else if strings.Contains(failure, MarkerInvalidResponse) {
		status = "invalid_response"
	} else if strings.Contains(failure, MarkerNotDispatched) {
		status = "failed_before_acceptance"
	}
	costState := "unknown"
	if status == "failed_before_acceptance" {
		costState = "no_usage_reported"
	}
	if opts.AfterDispatch != nil {
		if err := opts.AfterDispatch(attempt, route, nil, status, failure); err != nil {
			status = "unknown_after_dispatch"
			costState = "unknown"
			failure = "AfterDispatch hook failed while recording the provider outcome."
		}
	}
	summary := strings.ReplaceAll(failure, "\r", " ")
	summary = strings.ReplaceAll(summary, "\n", " ")
	if _, err := e.RegisterMemberResult(opts.RunRoot, MemberResult{
		Attempt: attempt, Status: status, Observed: Observed{}, ExecutionMode: executionMode,
		Summary: truncate(summary, 400), DispatchedAt: &dispatchedAt, CompletedAt: &completedAt, CostState: costState,
	}); err != nil {
		return err
	}
	if opts.BeforeDispatch == nil {
		if _, err := e.CompleteBudgetOutcome(opts.RunRoot, entry.RoleName, attempt, status, nil, costState); err != nil {
			return err
		}
	}
	if entry.Role.Required {
		return blocked("required role did not complete: %s (%s): %s", entry.RoleName, status, failure)
	}
	return nil
}

// capabilityBinding ports Get-BSLFlowCouncilCapabilityBinding (stable,
// non-secret portion of a host capability receipt).
func capabilityBinding(capability any) (*ordered, error) {
	object, ok := asOrdered(capability)
	if !ok {
		return nil, blocked("fallback capability is not an object.")
	}
	// Assert the capability fields.
	for _, field := range []string{"capability_version", "provider", "model", "effort", "fresh_context", "sealed", "terminal", "source", "executable_sha256", "sandbox_sha256", "catalog_sha256", "skills_sha256", "catalog_source_path", "catalog_source_sha256"} {
		if object.get(field) == nil {
			return nil, blocked("fallback capability misses field: %s.", field)
		}
	}
	if !asBoolJSONValue(object.get("fresh_context")) {
		return nil, blocked("fallback requires a fresh model context.")
	}
	if !asBoolJSONValue(object.get("sealed")) {
		return nil, blocked("fallback requires a sealed no-tools input.")
	}
	if !asBoolJSONValue(object.get("terminal")) {
		return nil, blocked("fallback requires a terminal capability receipt.")
	}
	if asStringOr(object.get("provider")) != "current_agent" {
		return nil, blocked("fallback capability provider is not the current-agent adapter.")
	}
	for _, field := range []string{"executable_sha256", "sandbox_sha256", "catalog_sha256", "skills_sha256"} {
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(asStringOr(object.get(field))) {
			return nil, blocked("fallback capability has no valid %s proof.", field)
		}
	}
	stable := orderedFrom(
		[]string{"capability_version", "provider", "model", "effort", "fresh_context", "sealed", "terminal", "source", "executable_sha256", "sandbox_sha256", "catalog_sha256", "skills_sha256", "catalog_source_path", "catalog_source_sha256"},
		[]any{
			asStringOr(object.get("capability_version")), asStringOr(object.get("provider")),
			asStringOr(object.get("model")), asStringOr(object.get("effort")),
			object.get("fresh_context"), object.get("sealed"), object.get("terminal"),
			asStringOr(object.get("source")), asStringOr(object.get("executable_sha256")),
			asStringOr(object.get("sandbox_sha256")), asStringOr(object.get("catalog_sha256")),
			asStringOr(object.get("skills_sha256")), asStringOr(object.get("catalog_source_path")),
			asStringOr(object.get("catalog_source_sha256")),
		},
	)
	jsonBytes, err := convertToJSONCompress(stable, 10)
	if err != nil {
		return nil, invalid("%v", err)
	}
	text := string(jsonBytes)
	if regexp.MustCompile(`(?i)token|authorization|password|secret`).MatchString(text) {
		return nil, blocked("fallback capability binding contains credential material.")
	}
	return orderedFrom(
		[]string{"sha256", "identity"},
		[]any{sha256Hex([]byte(text)), stable},
	), nil
}


// RunCycle ports Invoke-BSLFlowCouncilCycle: member fan-out, readiness, chair
// fan-in, review v2 assembly, prepared package and resume. The Dispatcher is
// the only live-call seam.
func (e *Engine) RunCycle(ctx context.Context, opts *CycleOptions) (*CycleResult, error) {
	if opts.BeforeDispatch == nil && opts.AfterDispatch != nil {
		return nil, invalid("council dispatch hooks must be supplied as a pair.")
	}
	if opts.BeforeDispatch != nil && opts.AfterDispatch == nil {
		return nil, invalid("council dispatch hooks must be supplied as a pair.")
	}
	if !opts.AllowLiveDispatch {
		return nil, blocked("live council dispatch needs explicit opt-in; dry-run plan recorded.")
	}
	bindings, err := BuildRoleBindings(opts.ProjectRoot, opts.Policy, opts.Snapshot)
	if err != nil {
		return nil, err
	}
	for _, entry := range bindings {
		entry.Snapshot = opts.Snapshot
		entry.CostEstimateUSD = entry.Profile.CostEstimateUSD
		entry.RequestTimeoutSecs = opts.Policy.RequestTimeoutSeconds
	}
	// Capabilities: the host supplies one capability per tokenless role.
	// Split member entries into fallback (sequential) and direct (parallel).
	var memberEntries []*RoleBinding
	for _, entry := range bindings {
		if entry.RoleName != councilRoleChair {
			memberEntries = append(memberEntries, entry)
		}
	}
	results := map[string]*ordered{}
	payloads := map[string]any{}
	maxParallel := opts.Policy.MaxParallel
	if maxParallel < 1 {
		maxParallel = 1
	}
	if opts.BeforeDispatch != nil {
		maxParallel = 1
	}
	dispatchOne := func(entry *RoleBinding) error {
		envelope, payload, _, err := e.dispatchRole(ctx, opts, entry, opts.capabilityFor(entry.RoleName))
		if err != nil {
			return err
		}
		results[entry.RoleName] = envelope
		payloads[entry.RoleName] = payload
		return nil
	}
	// Fallback roles run sequentially (the host runner needs the live host
	// session, mirroring the PowerShell controller runspace).
	for _, entry := range memberEntries {
		if entry.Credential.CredentialSource == "missing" {
			if err := dispatchOne(entry); err != nil {
				return nil, err
			}
		}
	}
	// Direct roles run with bounded parallelism.
	var parallel []*RoleBinding
	for _, entry := range memberEntries {
		if entry.Credential.CredentialSource != "missing" {
			parallel = append(parallel, entry)
		}
	}
	if maxParallel <= 1 || len(parallel) <= 1 {
		for _, entry := range parallel {
			if err := dispatchOne(entry); err != nil {
				return nil, err
			}
		}
	} else {
		var resultLock sync.Mutex
		if err := runParallel(parallel, maxParallel, func(entry *RoleBinding) error {
			envelope, payload, _, err := e.dispatchRole(ctx, opts, entry, nil)
			if err != nil {
				return err
			}
			resultLock.Lock()
			results[entry.RoleName] = envelope
			payloads[entry.RoleName] = payload
			resultLock.Unlock()
			return nil
		}); err != nil {
			return nil, err
		}
	}
	readiness, err := ReadinessFor(opts.Policy, opts.RunRoot)
	if err != nil {
		return nil, err
	}
	if !readiness.ChairAllowed {
		return nil, blocked("chair cannot run: %s", strings.Join(readiness.Blockers, "; "))
	}
	// Chair fan-in over the canonical aggregate.
	aggregate, err := e.buildAggregate(opts, memberEntries, results, payloads)
	if err != nil {
		return nil, err
	}
	chairEntry, err := findChair(bindings)
	if err != nil {
		return nil, err
	}
	chairEntry.Snapshot = opts.Snapshot
	chairEntry.CostEstimateUSD = chairEntry.Profile.CostEstimateUSD
	chairEntry.RequestTimeoutSecs = opts.Policy.RequestTimeoutSeconds
	aggregateJSON, err := convertToJSON(aggregate, 10)
	if err != nil {
		return nil, invalid("%v", err)
	}
	aggregateHash := sha256Hex(aggregateJSON)
	chairBinding := *chairEntry.Binding
	chairInputHashes := orderedFrom(
		[]string{"original_task_sha256", "spec_sha256", "design_sha256", "evidence_sha256", "policy_hash", "rubric_sha256", "member_aggregate_sha256"},
		[]any{
			chairEntry.Binding.InputHashes.get("original_task_sha256"),
			chairEntry.Binding.InputHashes.get("spec_sha256"),
			chairEntry.Binding.InputHashes.get("design_sha256"),
			chairEntry.Binding.InputHashes.get("evidence_sha256"),
			chairEntry.Binding.InputHashes.get("policy_hash"),
			chairEntry.Binding.InputHashes.get("rubric_sha256"),
			aggregateHash,
		},
	)
	chairBinding.InputHashes = chairInputHashes
	chairRoute, chairExecutionMode, err := resolveRoute(chairEntry.Role, chairEntry.Credential, opts.capabilityFor(councilRoleChair))
	if err != nil {
		return nil, err
	}
	if chairExecutionMode == "current_agent_fallback" {
		capabilityBinding, err := capabilityBinding(chairRoute.Capability)
		if err != nil {
			return nil, err
		}
		chairBinding.FallbackCapability = capabilityBinding
	}
	chairAttempt, err := e.NewAttempt(opts.RunRoot, councilRoleChair, &chairBinding)
	if err != nil {
		return nil, err
	}
	chairPayload := any(nil)
	chairEnvelope := any(nil)
	retainedChair, err := RoleResult(opts.RunRoot, councilRoleChair, chairAttempt)
	if err != nil {
		return nil, err
	}
	if retainedChair != nil {
		envelope, _ := asOrdered(retainedChair["envelope"])
		status := asStringOr(envelope.get("status"))
		if status == "completed" {
			chairPayload = retainedChair["payload"]
			chairEnvelope = envelope
		} else if status == "unknown_after_dispatch" {
			return nil, blocked("retained chair attempt %s is unknown_after_dispatch; provider reconciliation is required before any new dispatch.", chairAttempt.AttemptID)
		} else {
			return nil, blocked("retained chair result is %s; a new allowed attempt is required.", status)
		}
	}
	if chairPayload == nil {
		chairView := orderedFrom(
			[]string{"original_task", "spec", "design", "evidence", "aggregates"},
			[]any{
				opts.Snapshot.OriginalTaskText, opts.Snapshot.SpecText, nullableString(opts.Snapshot.DesignText),
				opts.Snapshot.EvidenceText, string(aggregateJSON),
			},
		)
		chairPrompt := buildPrompt(councilRoleChair, roleViewValueFromOrdered(chairView))
		if opts.Cancelled != nil {
			if cancelled, err := opts.Cancelled(); err != nil {
				return nil, err
			} else if cancelled {
				if _, err := e.RegisterMemberResult(opts.RunRoot, MemberResult{
					Attempt: chairAttempt, Status: "cancelled", Observed: Observed{}, ExecutionMode: chairExecutionMode, Summary: "Chair dispatch cancelled before the model call.",
				}); err != nil {
					return nil, err
				}
				return nil, blocked("council cancelled before the chair dispatch.")
			}
		}
		dispatchedAt := e.now()
		if opts.BeforeDispatch != nil {
			if err := opts.BeforeDispatch(chairAttempt, chairRoute); err != nil {
				return nil, err
			}
		} else {
			if _, err := e.ApproveBudgetDispatch(opts.RunRoot, councilRoleChair, chairAttempt, chairEntry.CostEstimateUSD, opts.Policy.Budget); err != nil {
				return nil, err
			}
		}
		result, err := opts.Dispatcher.Dispatch(ctx, chairAttempt, chairPrompt, chairRoute, chairEntry.RequestTimeoutSecs)
		completedAt := e.now()
		if err != nil {
			return nil, e.classifyChairFailure(opts, chairEntry, chairAttempt, chairRoute, chairExecutionMode, dispatchedAt, completedAt, err)
		}
		if result.Status != "completed" {
			status := result.Status
			if !memberStatusSet[status] {
				status = "unknown_after_dispatch"
			}
			if opts.AfterDispatch != nil {
				_ = opts.AfterDispatch(chairAttempt, chairRoute, &result, status, fmt.Sprintf("Chair dispatch returned %s.", status))
			}
			costState := "unknown"
			if status == "failed_before_acceptance" {
				costState = "no_usage_reported"
			}
			if _, err := e.RegisterMemberResult(opts.RunRoot, MemberResult{
				Attempt: chairAttempt, Status: status, Observed: Observed{}, ExecutionMode: chairExecutionMode,
				Summary: fmt.Sprintf("Chair dispatch returned %s.", status), DispatchedAt: &dispatchedAt, CompletedAt: &completedAt, CostState: costState,
			}); err != nil {
				return nil, err
			}
			if opts.BeforeDispatch == nil {
				if _, err := e.CompleteBudgetOutcome(opts.RunRoot, councilRoleChair, chairAttempt, status, nil, costState); err != nil {
					return nil, err
				}
			}
			return nil, blocked("chair did not complete: %s", status)
		}
		chairCostState := "no_usage_reported"
		if result.Usage != nil {
			chairCostState = "provider_usage_reported"
		}
		if opts.AfterDispatch != nil {
			if err := opts.AfterDispatch(chairAttempt, chairRoute, &result, "completed", ""); err != nil {
				return nil, blocked("chair provider outcome is unknown_after_dispatch.")
			}
		}
		envelope, err := e.RegisterMemberResult(opts.RunRoot, MemberResult{
			Attempt: chairAttempt, Payload: result.Payload, Status: "completed",
			Observed: result.Observed, ExecutionMode: result.ExecutionMode, Summary: "Chair reconciliation completed.",
			FallbackReason: result.FallbackReason, DispatchedAt: &dispatchedAt, CompletedAt: &completedAt,
			Usage: result.Usage, CostState: chairCostState,
		})
		if err != nil {
			return nil, err
		}
		if opts.BeforeDispatch == nil {
			if _, err := e.CompleteBudgetOutcome(opts.RunRoot, councilRoleChair, chairAttempt, "completed", result.ReportedCostUSD, chairCostState); err != nil {
				return nil, err
			}
		}
		chairPayload = result.Payload
		chairEnvelope = envelope
	}
	chair, ok := asOrdered(chairPayload)
	if !ok {
		return nil, blocked("chair payload is not an object.")
	}
	for _, field := range []string{"verdict", "decisions", "protected_decisions", "requirement_refs", "final_spec_text"} {
		if !chair.has(field) {
			return nil, blocked("chair payload misses field: %s.", field)
		}
	}
	// Member envelopes in role order + chair.
	memberEnvelopes := []any{}
	for _, roleName := range []string{councilRoleBrainstorm, councilRoleIntentCritic, councilRoleArchitecture, councilRoleExecutability} {
		if !opts.Policy.Roles[roleName].Enabled {
			continue
		}
		roleDir := filepath.Join(opts.RunRoot, roleName)
		files, err := listResultFiles(roleDir)
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			continue
		}
		stored, err := readOrderedObject(files[len(files)-1])
		if err != nil {
			return nil, err
		}
		memberEnvelopes = append(memberEnvelopes, stored.get("envelope"))
	}
	chairEnvelopeOrdered, _ := asOrdered(chairEnvelope)
	memberEnvelopes = append(memberEnvelopes, chairEnvelopeOrdered)
	diversity, err := GetCouncilDiversity(memberEnvelopes)
	if err != nil {
		return nil, err
	}
	finalSpecText := asStringOr(chair.get("final_spec_text"))
	if strings.TrimSpace(finalSpecText) == "" {
		return nil, blocked("chair payload misses final_spec_text.")
	}
	finalSpecBytes := []byte(finalSpecText)
	finalDesignBytes := []byte(nil)
	designPath := filepath.Join(ChangeDir(opts.ProjectRoot, opts.ChangeName), "design.md")
	if isRegularFile(designPath) {
		finalDesignBytes, err = readFileBytes(designPath)
		if err != nil {
			return nil, err
		}
	}
	if chairDesign := chair.get("final_design_text"); chairDesign != nil {
		finalDesignBytes = []byte(asStringOr(chairDesign))
	}
	review := e.buildReview(opts, chair, aggregate, memberEnvelopes, diversity, finalSpecBytes, finalDesignBytes)
	digest, err := CouncilReviewDigest(review)
	if err != nil {
		return nil, err
	}
	reconciliation, _ := asOrdered(review.get("reconciliation"))
	reconciliation.set("review_sha256", digest)
	if err := AssertCouncilReview(review); err != nil {
		return nil, err
	}
	var existingReviewBytes []byte
	reviewPath := filepath.Join(ChangeDir(opts.ProjectRoot, opts.ChangeName), "review.json")
	if isRegularFile(reviewPath) {
		existingReviewBytes, _ = readFileBytes(reviewPath)
	}
	if _, err := e.NewPreparedPackage(opts.RunRoot, review, finalSpecBytes, finalDesignBytes, existingReviewBytes); err != nil {
		return nil, err
	}
	resumed, err := e.ResumePublication(ChangeDir(opts.ProjectRoot, opts.ChangeName), opts.RunRoot, review, opts.ProjectRoot)
	if err != nil {
		return nil, err
	}
	return &CycleResult{
		Review:          review,
		RunRoot:         opts.RunRoot,
		FinalValidation: orderedToMap(resumed.get("final_validation")),
	}, nil
}

func (o *CycleOptions) capabilityFor(role string) any {
	if o.Capabilities == nil {
		return nil
	}
	return o.Capabilities[role]
}

func runParallel(entries []*RoleBinding, maxParallel int, fn func(*RoleBinding) error) error {
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	errs := make([]error, len(entries))
	for index, entry := range entries {
		wg.Add(1)
		go func(index int, entry *RoleBinding) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			errs[index] = fn(entry)
		}(index, entry)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func findChair(bindings []*RoleBinding) (*RoleBinding, error) {
	for _, entry := range bindings {
		if entry.RoleName == councilRoleChair {
			return entry, nil
		}
	}
	return nil, blocked("council chair must be enabled.")
}

func orderedToMap(value any) map[string]any {
	object, ok := asOrdered(value)
	if !ok {
		return nil
	}
	result := map[string]any{}
	for _, key := range object.keysOf() {
		result[key] = object.get(key)
	}
	return result
}

// classifyChairFailure mirrors the chair catch block of
// Invoke-BSLFlowCouncilCycle.
func (e *Engine) classifyChairFailure(opts *CycleOptions, entry *RoleBinding, attempt *Attempt, route *Route, executionMode string, dispatchedAt, completedAt time.Time, dispatchErr error) error {
	failure := dispatchErr.Error()
	status := "unknown_after_dispatch"
	if strings.Contains(failure, MarkerBeforeAcceptance) {
		status = "failed_before_acceptance"
	} else if strings.Contains(failure, MarkerInvalidResponse) {
		status = "invalid_response"
	} else if strings.Contains(failure, MarkerNotDispatched) {
		status = "failed_before_acceptance"
	}
	costState := "unknown"
	if status == "failed_before_acceptance" {
		costState = "no_usage_reported"
	}
	if opts.AfterDispatch != nil {
		if err := opts.AfterDispatch(attempt, route, nil, status, failure); err != nil {
			status = "unknown_after_dispatch"
			costState = "unknown"
			failure = "AfterDispatch hook failed while recording the provider outcome."
		}
	}
	summary := strings.ReplaceAll(strings.ReplaceAll(failure, "\r", " "), "\n", " ")
	if _, err := e.RegisterMemberResult(opts.RunRoot, MemberResult{
		Attempt: attempt, Status: status, Observed: Observed{}, ExecutionMode: executionMode,
		Summary: truncate(summary, 400), DispatchedAt: &dispatchedAt, CompletedAt: &completedAt, CostState: costState,
	}); err != nil {
		return err
	}
	if opts.BeforeDispatch == nil {
		if _, err := e.CompleteBudgetOutcome(opts.RunRoot, councilRoleChair, attempt, status, nil, costState); err != nil {
			return err
		}
	}
	return blocked("chair did not complete (%s): %s", status, failure)
}

// buildAggregate ports the chair fan-in aggregation of
// Invoke-BSLFlowCouncilCycle (lines 557-604).
func (e *Engine) buildAggregate(opts *CycleOptions, memberEntries []*RoleBinding, results map[string]*ordered, payloads map[string]any) (*ordered, error) {
	aggregate := orderedFrom(
		[]string{"findings", "protected", "requirements", "brainstorm", "questions", "classification"},
		[]any{[]any{}, []any{}, opts.Snapshot.Manifest.get("requirements"), nil, []any{},
			orderedFrom([]string{"complexity", "risk"}, []any{opts.Snapshot.Complexity, opts.Snapshot.Risk})},
	)
	questions := []any{}
	findings := []any{}
	protected := []any{}
	for _, roleName := range []string{councilRoleBrainstorm, councilRoleIntentCritic, councilRoleArchitecture, councilRoleExecutability} {
		if !opts.Policy.Roles[roleName].Enabled {
			continue
		}
		roleDir := filepath.Join(opts.RunRoot, roleName)
		files, err := listResultFiles(roleDir)
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			continue
		}
		stored, err := readOrderedObject(files[len(files)-1])
		if err != nil {
			return nil, err
		}
		envelope, _ := asOrdered(stored.get("envelope"))
		if asStringOr(envelope.get("status")) != "completed" || stored.get("payload") == nil {
			continue
		}
		payload, _ := asOrdered(stored.get("payload"))
		if roleName == councilRoleBrainstorm {
			alternatives, _ := asArray(payload.get("alternatives"))
			risks, _ := asArray(payload.get("risks"))
			unknowns, _ := asArray(payload.get("unknowns"))
			brainstormQuestions, _ := asArray(payload.get("questions"))
			aggregate.set("brainstorm", orderedFrom(
				[]string{"alternatives", "risks", "unknowns", "questions"},
				[]any{orEmpty(alternatives), orEmpty(risks), orEmpty(unknowns), orEmpty(brainstormQuestions)},
			))
			for _, question := range brainstormQuestions {
				questions = append(questions, orderedFrom([]string{"role", "text"}, []any{roleName, asStringOr(question)}))
			}
			continue
		}
		for _, question := range orEmpty(asArrayOr(payload.get("needs_input_questions"))) {
			questions = append(questions, orderedFrom([]string{"role", "text"}, []any{roleName, asStringOr(question)}))
		}
		for _, raw := range orEmpty(asArrayOr(payload.get("findings"))) {
			finding, _ := asOrdered(raw)
			findings = append(findings, orderedFrom(
				[]string{"composite_id", "role", "id", "severity", "category", "spec_ref", "issue", "evidence", "suggested_direction"},
				[]any{
					roleName + ":" + asStringOr(finding.get("id")), roleName, asStringOr(finding.get("id")),
					asStringOr(finding.get("severity")), asStringOr(finding.get("category")), asStringOr(finding.get("spec_ref")),
					asStringOr(finding.get("issue")), asStringOr(finding.get("evidence")), asStringOr(finding.get("suggested_direction")),
				},
			))
		}
		protectedIndex := 0
		for _, item := range orEmpty(asArrayOr(payload.get("do_not_change"))) {
			text := asStringOr(item)
			if strings.TrimSpace(text) == "" {
				continue
			}
			protectedIndex++
			protected = append(protected, orderedFrom(
				[]string{"composite_id", "role", "item"},
				[]any{fmt.Sprintf("%s:do-not-change-%03d", roleName, protectedIndex), roleName, text},
			))
		}
	}
	aggregate.set("questions", questions)
	canonicalFindings, err := GetCanonicalFindings(findings)
	if err != nil {
		return nil, err
	}
	aggregate.set("findings", canonicalFindings)
	aggregate.set("protected", protected)
	return aggregate, nil
}

func orEmpty(items []any) []any {
	if items == nil {
		return []any{}
	}
	return items
}

func asArrayOr(value any) []any {
	items, _ := asArray(value)
	return items
}

// buildReview assembles the review v2 object (Invoke-CouncilReview.ps1:774-800).
func (e *Engine) buildReview(opts *CycleOptions, chair *ordered, aggregate *ordered, memberEnvelopes []any, diversity *ordered, finalSpecBytes, finalDesignBytes []byte) *ordered {
	chairOrdered := orderedFrom(
		[]string{"verdict", "decisions", "protected_decisions", "requirement_refs", "final_spec_text", "final_design_text"},
		[]any{
			asStringOr(chair.get("verdict")), chair.get("decisions"), chair.get("protected_decisions"),
			chair.get("requirement_refs"), asStringOr(chair.get("final_spec_text")), chair.get("final_design_text"),
		},
	)
	reconciliation := orderedFrom(
		[]string{"review_sha256", "draft_spec_sha256", "final_spec_sha256", "draft_design_sha256", "final_design_sha256"},
		[]any{
			strings.Repeat("0", 64), opts.Snapshot.SpecSHA256, sha256Hex(finalSpecBytes),
			nullableString(opts.Snapshot.DesignSHA256), nullableHash(finalDesignBytes),
		},
	)
	return orderedFrom(
		[]string{"schema_version", "reviewed_at_utc", "council_schema_version", "verdict", "diversity", "fallback_visible", "inputs", "manifest", "members", "findings", "protected", "questions", "chair", "reconciliation", "gate"},
		[]any{
			jsonNumber(2), roundTripUTCTime(e.now()), jsonNumber(1),
			asStringOr(chair.get("verdict")), diversity.get("diversity"), diversity.get("fallback_visible"),
			orderedFrom([]string{"original_task_sha256", "spec_sha256", "design_sha256", "policy_hash"},
				[]any{opts.Snapshot.OriginalTaskSHA256, opts.Snapshot.SpecSHA256, nullableString(opts.Snapshot.DesignSHA256), opts.Snapshot.PolicyHash}),
			opts.Snapshot.Manifest, memberEnvelopes,
			aggregate.get("findings"), aggregate.get("protected"), aggregate.get("questions"),
			chairOrdered, reconciliation,
			orderedFrom([]string{"structural_only", "passed"}, []any{true, true}),
		},
	)
}

func nullableHash(bytes []byte) any {
	if bytes == nil {
		return nil
	}
	return sha256Hex(bytes)
}

func jsonNumber(value int) json.Number {
	return json.Number(fmt.Sprintf("%d", value))
}
