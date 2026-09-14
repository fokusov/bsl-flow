package councilengine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// engine.go ports Council.Engine.ps1: the council engine core without
// network — snapshot, role views, attempt binding/resume, member result
// registration, readiness, the durable budget ledger and the prepared
// publication/resume. One writer persists aggregate artifacts.

var (
	complexityPattern = regexp.MustCompile(`(?im)^\s*-\s*(?:Сложность|Complexity):\s*(S|M|L)\s*$`)
	riskPattern       = regexp.MustCompile(`(?im)^\s*-\s*(?:Риск|Risk):\s*(low|medium|high)\s*$`)
	changeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	attemptIDPattern  = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	roleNamePattern   = regexp.MustCompile(`^(brainstorm|intent_critic|architecture_critic|executability_critic|chair)$`)
)

// Engine carries the council engine dependencies: a clock, the trusted skill
// root for role rubrics/prompts, and the final-validation seam the host wires
// to the deterministic spec validator.
type Engine struct {
	Now func() time.Time
	// FinalValidation runs the deterministic final validation and writes
	// final-validation.json, returning the parsed receipt. A non-passing
	// receipt (passed != true) must be an error.
	FinalValidation func(changeDir string) (map[string]any, error)
}

func (e *Engine) now() time.Time {
	if e != nil && e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// ClassifySpec extracts the complexity and risk classification from a spec
// document (Get-SpecClassification): exactly one complexity and one risk
// marker are required.
func ClassifySpec(specText string) (complexity, risk string, err error) {
	complexityMatches := complexityPattern.FindAllStringSubmatch(specText, -1)
	riskMatches := riskPattern.FindAllStringSubmatch(specText, -1)
	if len(complexityMatches) != 1 {
		return "", "", invalid("spec.md must contain exactly one complexity classification.")
	}
	if len(riskMatches) != 1 {
		return "", "", invalid("spec.md must contain exactly one risk classification.")
	}
	return strings.ToUpper(complexityMatches[0][1]), strings.ToLower(riskMatches[0][1]), nil
}

// RunRoot is the council attempt root for a project/change.
func RunRoot(projectRoot, changeName string) string {
	return filepath.Join(projectRoot, ".bsl-flow", "reports", "spec-review", changeName+".council")
}

// ChangeDir is the OpenSpec change directory.
func ChangeDir(projectRoot, changeName string) string {
	return filepath.Join(projectRoot, "openspec", "changes", changeName)
}

// --- snapshot ---

// Snapshot is the immutable bounded council input snapshot.
type Snapshot struct {
	OriginalTaskSHA256 string
	SpecSHA256         string
	DesignSHA256       *string
	OriginalTaskText   string
	SpecText           string
	DesignText         *string
	Complexity         string // uppercase S|M|L
	Risk               string // lowercase low|medium|high
	Manifest           *ordered
	EvidenceText       string
	EvidenceSHA256     *string
	PolicyHash         string
}

// NewSnapshot ports New-BSLFlowCouncilSnapshot: bounded UTF-8 reads of the
// original task and draft spec (design optional), classification and manifest
// extraction, evidence hashing and the policy hash.
func NewSnapshot(changeDir string, maxBytes int, evidenceText, policyHash string) (*Snapshot, error) {
	read := func(name string) ([]byte, string, string, error) {
		path := filepath.Join(changeDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, "", "", blocked("Council snapshot missing required input: %s", path)
		}
		if len(data) > maxBytes {
			return nil, "", "", invalid("Review input exceeds %d bytes: %s", maxBytes, path)
		}
		if !utf8ValidStrict(data) {
			return nil, "", "", invalid("Review input is not valid UTF-8: %s", path)
		}
		return data, string(data), sha256Hex(data), nil
	}
	_, originalText, originalHash, err := read("original-task.md")
	if err != nil {
		return nil, err
	}
	_, specText, specHash, err := read("spec.md")
	if err != nil {
		return nil, err
	}
	var designHash, designText *string
	if data, err := os.ReadFile(filepath.Join(changeDir, "design.md")); err == nil {
		if len(data) > maxBytes {
			return nil, invalid("Review input exceeds %d bytes: %s", maxBytes, filepath.Join(changeDir, "design.md"))
		}
		if !utf8ValidStrict(data) {
			return nil, invalid("Review input is not valid UTF-8: %s", filepath.Join(changeDir, "design.md"))
		}
		hash := sha256Hex(data)
		text := string(data)
		designHash = &hash
		designText = &text
	}
	complexityMatches := complexityPattern.FindAllStringSubmatch(specText, -1)
	riskMatches := riskPattern.FindAllStringSubmatch(specText, -1)
	if len(complexityMatches) != 1 || len(riskMatches) != 1 {
		return nil, invalid("Council snapshot requires exactly one complexity and one risk classification.")
	}
	manifest, err := NewRequirementManifest(specText)
	if err != nil {
		return nil, err
	}
	var evidenceHash *string
	if strings.TrimSpace(evidenceText) != "" {
		if utf8ByteCount(evidenceText) > maxBytes {
			return nil, invalid("Council evidence exceeds %d bytes.", maxBytes)
		}
		hash := sha256Hex([]byte(evidenceText))
		evidenceHash = &hash
	}
	if !sha256Pattern.MatchString(policyHash) {
		return nil, invalid("Council snapshot needs a policy hash.")
	}
	return &Snapshot{
		OriginalTaskSHA256: originalHash,
		SpecSHA256:         specHash,
		DesignSHA256:       designHash,
		OriginalTaskText:   originalText,
		SpecText:           specText,
		DesignText:         designText,
		Complexity:         strings.ToUpper(complexityMatches[0][1]),
		Risk:               strings.ToLower(riskMatches[0][1]),
		Manifest:           manifest,
		EvidenceText:       evidenceText,
		EvidenceSHA256:     evidenceHash,
		PolicyHash:         policyHash,
	}, nil
}

func utf8ByteCount(text string) int {
	return len([]byte(text))
}

// RoleView ports Get-BSLFlowCouncilRoleView.
func (s *Snapshot) RoleView(role, rubricText string) (*ordered, error) {
	classification := orderedFrom([]string{"complexity", "risk"}, []any{s.Complexity, s.Risk})
	if role == councilRoleBrainstorm {
		return orderedFrom(
			[]string{"role", "original_task", "evidence", "classification"},
			[]any{role, s.OriginalTaskText, s.EvidenceText, classification},
		), nil
	}
	designText := any(nil)
	if s.DesignText != nil {
		designText = *s.DesignText
	}
	return orderedFrom(
		[]string{"role", "original_task", "spec", "design", "evidence", "classification", "rubric"},
		[]any{role, s.OriginalTaskText, s.SpecText, designText, s.EvidenceText, classification, rubricText},
	), nil
}

// --- attempts ---

// Binding is the frozen council attempt binding.
type Binding struct {
	Provider                   string
	Model                      string
	Effort                     string
	Protocol                   string
	Endpoint                   Endpoint
	TransportCapabilityVersion int
	PromptVersion              string
	MemberSchemaVersion        int
	InputHashes                *ordered
	FallbackCapability         any // nil when absent
}

// BindingHash mirrors the New-BSLFlowCouncilAttempt binding hash: SHA-256 of
// the ConvertTo-Json -Depth 10 bytes of the binding object.
func (b *Binding) BindingHash() (string, error) {
	data, err := convertToJSON(b.toOrdered(), 10)
	if err != nil {
		return "", invalid("%v", err)
	}
	return sha256Hex(data), nil
}

func (b *Binding) toOrdered() *ordered {
	endpoint := orderedFrom([]string{"scheme", "host", "port", "base_path"},
		[]any{b.Endpoint.Scheme, b.Endpoint.Host, b.Endpoint.Port, b.Endpoint.BasePath})
	object := orderedFrom(
		[]string{"provider", "model", "effort", "protocol", "endpoint", "transport_capability_version", "prompt_version", "member_schema_version", "input_hashes"},
		[]any{b.Provider, b.Model, b.Effort, b.Protocol, endpoint, b.TransportCapabilityVersion, b.PromptVersion, b.MemberSchemaVersion, b.InputHashes},
	)
	if b.FallbackCapability != nil {
		object.set("fallback_capability", b.FallbackCapability)
	}
	return object
}

// AssertBinding ports the field validation of New-BSLFlowCouncilAttempt:
// every binding field, the endpoint decomposition and the input_hashes set.
func AssertBinding(binding *Binding) error {
	if binding == nil {
		return invalid("Attempt binding is missing.")
	}
	for name, value := range map[string]string{
		"provider": binding.Provider, "model": binding.Model, "effort": binding.Effort,
		"protocol": binding.Protocol, "prompt_version": binding.PromptVersion,
	} {
		if value == "" {
			return invalid("Attempt binding misses field: %s", name)
		}
	}
	if binding.Endpoint.Scheme == "" || binding.Endpoint.Host == "" || binding.Endpoint.Port < 1 || binding.Endpoint.BasePath == "" {
		return invalid("Attempt binding endpoint misses field.")
	}
	if binding.TransportCapabilityVersion < 1 || binding.MemberSchemaVersion < 1 {
		return invalid("Attempt binding misses field: transport_capability_version or member_schema_version.")
	}
	if binding.InputHashes == nil {
		return invalid("Attempt binding input_hashes misses field: input_hashes")
	}
	for _, field := range []string{"original_task_sha256", "spec_sha256", "design_sha256", "evidence_sha256", "policy_hash", "rubric_sha256"} {
		if !binding.InputHashes.has(field) {
			return invalid("Attempt binding input_hashes misses field: %s", field)
		}
	}
	// Secrets never belong in the binding.
	serialized, err := convertToJSON(binding.toOrdered(), 10)
	if err != nil {
		return err
	}
	text := string(serialized)
	if regexp.MustCompile(`(?i)"token"\s*:`).MatchString(text) {
		return invalid("Attempt binding must not contain a token.")
	}
	if regexp.MustCompile(`(?i)Authorization`).MatchString(text) {
		return invalid("Attempt binding must not contain Authorization material.")
	}
	if binding.FallbackCapability != nil {
		capability, ok := asOrdered(binding.FallbackCapability)
		if !ok {
			return invalid("Attempt binding fallback capability is incomplete.")
		}
		if !sha256Pattern.MatchString(asStringOr(capability.get("sha256"))) || capability.get("identity") == nil {
			return invalid("Attempt binding fallback capability is incomplete.")
		}
		identity, ok := asOrdered(capability.get("identity"))
		if !ok {
			return invalid("Attempt binding fallback capability is incomplete.")
		}
		for _, field := range []string{"capability_version", "provider", "model", "effort", "fresh_context", "sealed", "terminal"} {
			if !identity.has(field) {
				return invalid("Attempt binding fallback capability misses field: %s", field)
			}
		}
	}
	return nil
}

// Attempt is one persisted council attempt.
type Attempt struct {
	SchemaVersion int
	AttemptID     string
	Sequence      int
	Role          string
	CreatedAtUTC  string
	Binding       *Binding
	BindingSHA256 string
}

func (a *Attempt) toOrdered() *ordered {
	return orderedFrom(
		[]string{"schema_version", "attempt_id", "sequence", "role", "created_at_utc", "binding", "binding_sha256"},
		[]any{json.Number("1"), a.AttemptID, json.Number(fmt.Sprintf("%d", a.Sequence)), a.Role, a.CreatedAtUTC, a.Binding.toOrdered(), a.BindingSHA256},
	)
}

// NewAttempt ports New-BSLFlowCouncilAttempt: sequence-bound attempts preserve
// retained evidence — identical binding reuses the latest attempt, drift
// creates the next sequence entry.
func (e *Engine) NewAttempt(runRoot, role string, binding *Binding) (*Attempt, error) {
	if err := AssertBinding(binding); err != nil {
		return nil, err
	}
	bindingHash, err := binding.BindingHash()
	if err != nil {
		return nil, err
	}
	roleDir := filepath.Join(runRoot, role)
	if err := os.MkdirAll(roleDir, 0o755); err != nil {
		return nil, blocked("%v", err)
	}
	existing, err := listAttemptFiles(roleDir)
	if err != nil {
		return nil, err
	}
	sequence := 1
	if len(existing) > 0 {
		latest, err := readOrderedObject(existing[len(existing)-1])
		if err != nil {
			return nil, err
		}
		latestBinding, ok := asOrdered(latest.get("binding"))
		if !ok {
			return nil, blocked("retained council attempt has no binding.")
		}
		latestBytes, err := convertToJSON(latestBinding, 10)
		if err != nil {
			return nil, err
		}
		if sha256Hex(latestBytes) == bindingHash {
			return &Attempt{
				SchemaVersion: 1,
				AttemptID:     asStringOr(latest.get("attempt_id")),
				Sequence:      intValue(latest.get("sequence")),
				Role:          role,
				CreatedAtUTC:  asStringOr(latest.get("created_at_utc")),
				Binding:       binding,
				BindingSHA256: bindingHash,
			}, nil
		}
		sequence = len(existing) + 1
	}
	attemptID, err := guidN()
	if err != nil {
		return nil, err
	}
	attempt := &Attempt{
		SchemaVersion: 1,
		AttemptID:     attemptID,
		Sequence:      sequence,
		Role:          role,
		CreatedAtUTC:  roundTripUTCTime(e.now()),
		Binding:       binding,
		BindingSHA256: bindingHash,
	}
	path := filepath.Join(roleDir, fmt.Sprintf("attempt-%04d.json", sequence))
	if err := writeBSLFlowJSONAtomic(path, attempt.toOrdered()); err != nil {
		return nil, err
	}
	return attempt, nil
}

func listAttemptFiles(roleDir string) ([]string, error) {
	entries, err := os.ReadDir(roleDir)
	if err != nil {
		return []string{}, nil
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), "attempt-") && strings.HasSuffix(entry.Name(), ".json") {
			files = append(files, filepath.Join(roleDir, entry.Name()))
		}
	}
	sortStrings(files)
	return files, nil
}

func listResultFiles(roleDir string) ([]string, error) {
	entries, err := os.ReadDir(roleDir)
	if err != nil {
		return []string{}, nil
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), "result-") && strings.HasSuffix(entry.Name(), ".json") {
			files = append(files, filepath.Join(roleDir, entry.Name()))
		}
	}
	sortStrings(files)
	return files, nil
}

func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for position := index; position > 0 && values[position] < values[position-1]; position-- {
			values[position], values[position-1] = values[position-1], values[position]
		}
	}
}

func intValue(value any) int {
	switch typed := value.(type) {
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	case int:
		return typed
	case int64:
		return int(typed)
	}
	return 0
}

// LatestAttempt ports Get-BSLFlowCouncilLatestAttempt.
func LatestAttempt(runRoot, role string) (*Attempt, error) {
	roleDir := filepath.Join(runRoot, role)
	files, err := listAttemptFiles(roleDir)
	if err != nil || len(files) == 0 {
		return nil, nil
	}
	object, err := readJSONObject(files[len(files)-1])
	if err != nil {
		return nil, err
	}
	binding, err := bindingFromObject(object["binding"])
	if err != nil {
		return nil, err
	}
	return &Attempt{
		SchemaVersion: intValue(object["schema_version"]),
		AttemptID:     asStringOr(object["attempt_id"]),
		Sequence:      intValue(object["sequence"]),
		Role:          asStringOr(object["role"]),
		CreatedAtUTC:  asStringOr(object["created_at_utc"]),
		Binding:       binding,
		BindingSHA256: asStringOr(object["binding_sha256"]),
	}, nil
}

func bindingFromObject(value any) (*Binding, error) {
	object, ok := asOrdered(value)
	if !ok {
		return nil, blocked("retained council attempt has no binding.")
	}
	endpointObject, _ := asOrdered(object.get("endpoint"))
	endpoint := Endpoint{
		Scheme:   asStringOr(endpointObject.get("scheme")),
		Host:     asStringOr(endpointObject.get("host")),
		Port:     intValue(endpointObject.get("port")),
		BasePath: asStringOr(endpointObject.get("base_path")),
	}
	binding := &Binding{
		Provider:                   asStringOr(object.get("provider")),
		Model:                      asStringOr(object.get("model")),
		Effort:                     asStringOr(object.get("effort")),
		Protocol:                   asStringOr(object.get("protocol")),
		Endpoint:                   endpoint,
		TransportCapabilityVersion: intValue(object.get("transport_capability_version")),
		PromptVersion:              asStringOr(object.get("prompt_version")),
		MemberSchemaVersion:        intValue(object.get("member_schema_version")),
		InputHashes:                anyOrdered(object.get("input_hashes")),
		FallbackCapability:         object.get("fallback_capability"),
	}
	return binding, nil
}

func anyOrdered(value any) *ordered {
	object, _ := asOrdered(value)
	return object
}

// --- member results ---

// Observed is the nullable observed identity triple.
type Observed struct {
	Provider *string
	Model    *string
	Effort   *string
}

// MemberResult registration options.
type MemberResult struct {
	Attempt        *Attempt
	Payload        any // nil when not completed
	Status         string
	Observed       Observed
	ExecutionMode  string
	Summary        string
	FallbackReason *string
	DispatchedAt   *time.Time
	CompletedAt    *time.Time
	Usage          *MemberUsage
	CostState      string
}

// MemberUsage is the provider usage projection.
type MemberUsage struct {
	InputTokens     *int64
	OutputTokens    *int64
	ReasoningTokens *int64
}

// RegisterMemberResult ports Register-BSLFlowCouncilMemberResult.
func (e *Engine) RegisterMemberResult(runRoot string, result MemberResult) (*ordered, error) {
	role := result.Attempt.Role
	if result.Status == "completed" {
		switch role {
		case councilRoleBrainstorm:
			if err := AssertBrainstormPayload(result.Payload); err != nil {
				return nil, err
			}
		case councilRoleChair:
			if err := AssertCouncilChairResult(result.Payload); err != nil {
				return nil, err
			}
		default:
			if err := AssertCouncilModelPayload(result.Payload); err != nil {
				return nil, err
			}
		}
	}
	var payloadHash *string
	if result.Status == "completed" {
		data, err := convertToJSON(result.Payload, 20)
		if err != nil {
			return nil, invalid("%v", err)
		}
		hash := sha256Hex(data)
		payloadHash = &hash
	}
	costState := result.CostState
	if result.Status == "completed" && costState == "" {
		if result.Usage != nil {
			costState = "provider_usage_reported"
		} else {
			costState = "no_usage_reported"
		}
	}
	if costState == "" {
		costState = "unknown"
	}
	var usage any
	if result.Usage != nil {
		usage = orderedFrom(
			[]string{"input_tokens", "output_tokens", "reasoning_tokens"},
			[]any{nullableInt(result.Usage.InputTokens), nullableInt(result.Usage.OutputTokens), nullableInt(result.Usage.ReasoningTokens)},
		)
	}
	observed := orderedFrom(
		[]string{"provider", "model", "effort"},
		[]any{nullableStr(result.Observed.Provider), nullableStr(result.Observed.Model), nullableStr(result.Observed.Effort)},
	)
	requested := orderedFrom(
		[]string{"provider", "model", "effort"},
		[]any{result.Attempt.Binding.Provider, result.Attempt.Binding.Model, result.Attempt.Binding.Effort},
	)
	var dispatchedAt, completedAt *string
	if result.DispatchedAt != nil {
		value := roundTripUTCTime(*result.DispatchedAt)
		dispatchedAt = &value
	}
	if result.CompletedAt != nil {
		value := roundTripUTCTime(*result.CompletedAt)
		completedAt = &value
	}
	envelope := orderedFrom(
		[]string{"schema_version", "role", "attempt_id", "status", "summary", "requested", "observed", "execution_mode", "fallback_reason", "input_hashes", "payload_sha256", "usage", "cost_state", "dispatched_at_utc", "completed_at_utc"},
		[]any{
			json.Number("1"), role, result.Attempt.AttemptID, result.Status, result.Summary,
			requested, observed, result.ExecutionMode, nullableStr(result.FallbackReason),
			result.Attempt.Binding.InputHashes, nullableStr(payloadHash), usage, costState,
			nullableStr(dispatchedAt), nullableStr(completedAt),
		},
	)
	roleDir := filepath.Join(runRoot, role)
	if err := os.MkdirAll(roleDir, 0o755); err != nil {
		return nil, blocked("%v", err)
	}
	resultPath := filepath.Join(roleDir, fmt.Sprintf("result-%04d.json", result.Attempt.Sequence))
	if isRegularFile(resultPath) {
		existing, err := readJSONObject(resultPath)
		if err != nil {
			return nil, err
		}
		existingEnvelope, _ := asOrdered(existing["envelope"])
		if asStringOr(existingEnvelope.get("attempt_id")) != result.Attempt.AttemptID {
			return nil, blocked("terminal result for another attempt already occupies this sequence.")
		}
	} else {
		payload := any(nil)
		if result.Status == "completed" {
			payload = result.Payload
		}
		document := orderedFrom([]string{"envelope", "payload"}, []any{envelope, payload})
		if err := writeBSLFlowJSONAtomic(resultPath, document); err != nil {
			return nil, err
		}
	}
	return envelope, nil
}

func nullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableStr(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// RoleResult reads the stored {envelope, payload} for an attempt.
func RoleResult(runRoot, role string, attempt *Attempt) (map[string]any, error) {
	resultPath := filepath.Join(runRoot, role, fmt.Sprintf("result-%04d.json", attempt.Sequence))
	if !isRegularFile(resultPath) {
		return nil, nil
	}
	object, err := readJSONObject(resultPath)
	if err != nil {
		return nil, err
	}
	envelope, _ := asOrdered(object["envelope"])
	if asStringOr(envelope.get("attempt_id")) != attempt.AttemptID {
		return nil, nil
	}
	return object, nil
}

// Readiness ports Get-BSLFlowCouncilReadiness.
type Readiness struct {
	ChairAllowed   bool
	CompletedRoles int
	Blockers       []string
}

func ReadinessFor(policy *CouncilPolicy, runRoot string) (*Readiness, error) {
	blockers := []string{}
	completed := 0
	for _, name := range []string{councilRoleBrainstorm, councilRoleIntentCritic, councilRoleArchitecture, councilRoleExecutability} {
		role := policy.Roles[name]
		if !role.Enabled {
			continue
		}
		roleDir := filepath.Join(runRoot, name)
		files, err := listResultFiles(roleDir)
		if err != nil {
			return nil, err
		}
		if len(files) == 0 {
			blockers = append(blockers, "Role has no terminal result: "+name)
			continue
		}
		stored, err := readJSONObject(files[len(files)-1])
		if err != nil {
			return nil, err
		}
		envelope, _ := asOrdered(stored["envelope"])
		status := asStringOr(envelope.get("status"))
		if status != "completed" {
			if role.Required {
				blockers = append(blockers, fmt.Sprintf("Required role is not completed: %s (%s)", name, status))
			}
			continue
		}
		completed++
	}
	return &Readiness{ChairAllowed: len(blockers) == 0, CompletedRoles: completed, Blockers: blockers}, nil
}

// --- budget ledger ---

var ledgerMutexes sync.Map // keyed by runRoot -> *sync.Mutex

func ledgerMutex(runRoot string) *sync.Mutex {
	value, _ := ledgerMutexes.LoadOrStore(runRoot, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func budgetLedgerDir(runRoot string) string {
	return filepath.Join(runRoot, "budget")
}

func reservationPath(runRoot, role string, sequence int) string {
	return filepath.Join(budgetLedgerDir(runRoot), fmt.Sprintf("reservation-%s-%04d.json", role, sequence))
}

// AddBudgetReservation ports Add-BSLFlowCouncilBudgetReservation.
func (e *Engine) AddBudgetReservation(runRoot, role string, attempt *Attempt, estimateUSD *float64) (string, error) {
	var estimate any
	if estimateUSD != nil {
		estimate = *estimateUSD
	}
	ledgerDir := budgetLedgerDir(runRoot)
	if err := os.MkdirAll(ledgerDir, 0o755); err != nil {
		return "", blocked("%v", err)
	}
	path := reservationPath(runRoot, role, attempt.Sequence)
	document := orderedFrom(
		[]string{"role", "attempt_id", "sequence", "estimated_usd", "outcome", "outcome_usd", "cost_state", "recorded_at_utc"},
		[]any{role, attempt.AttemptID, attempt.Sequence, estimate, "open", nil, "unknown", roundTripUTCTime(e.now())},
	)
	if err := writeBSLFlowJSONAtomic(path, document); err != nil {
		return "", err
	}
	return path, nil
}

// ledgerTotals mirrors Get-LedgerTotals: prior spent (outcome_usd, else
// estimated_usd) and whether any reservation still has unknown cost.
func ledgerTotals(runRoot string) (float64, bool, error) {
	ledgerDir := budgetLedgerDir(runRoot)
	entries, err := os.ReadDir(ledgerDir)
	if err != nil {
		return 0, false, nil
	}
	prior := 0.0
	hasUnknown := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "reservation-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		object, err := readJSONObject(filepath.Join(ledgerDir, entry.Name()))
		if err != nil {
			return 0, false, err
		}
		if asStringOr(object["cost_state"]) == "unknown" {
			hasUnknown = true
		}
		if spent := object["outcome_usd"]; spent != nil {
			prior += floatValue(spent)
			continue
		}
		if estimate := object["estimated_usd"]; estimate != nil {
			prior += floatValue(estimate)
		}
	}
	return prior, hasUnknown, nil
}

func floatValue(value any) float64 {
	switch typed := value.(type) {
	case json.Number:
		if parsed, err := typed.Float64(); err == nil {
			return parsed
		}
	case float64:
		return typed
	case int64:
		return float64(typed)
	case int:
		return float64(typed)
	}
	return 0
}

// ApproveBudgetDispatch ports Approve-BSLFlowCouncilBudgetDispatch: one
// atomic ledger transaction — admission check and reservation creation.
func (e *Engine) ApproveBudgetDispatch(runRoot, role string, attempt *Attempt, estimateUSD *float64, budget *BudgetConfig) (*ordered, error) {
	lock := ledgerMutex(runRoot)
	lock.Lock()
	defer lock.Unlock()
	estimate := 0.0
	if estimateUSD != nil {
		estimate = *estimateUSD
	}
	prior, _, err := ledgerTotals(runRoot)
	if err != nil {
		return nil, err
	}
	if budget != nil && budget.Limit != nil {
		limit := *budget.Limit
		reservationFloor := 0.0
		if budget.Reservation != nil {
			reservationFloor = *budget.Reservation
		}
		total := prior + estimate
		if !finiteNonNegative(total) {
			return nil, blocked("dispatch cost estimate must be a non-negative finite number.")
		}
		if total+reservationFloor > limit {
			return nil, blocked("council budget admission refused for this dispatch.")
		}
	}
	var estimateValue any
	if estimateUSD != nil {
		estimateValue = *estimateUSD
	}
	ledgerDir := budgetLedgerDir(runRoot)
	if err := os.MkdirAll(ledgerDir, 0o755); err != nil {
		return nil, blocked("%v", err)
	}
	path := reservationPath(runRoot, role, attempt.Sequence)
	document := orderedFrom(
		[]string{"role", "attempt_id", "sequence", "estimated_usd", "outcome", "outcome_usd", "cost_state", "recorded_at_utc"},
		[]any{role, attempt.AttemptID, attempt.Sequence, estimateValue, "open", nil, "unknown", roundTripUTCTime(e.now())},
	)
	if err := writeBSLFlowJSONAtomic(path, document); err != nil {
		return nil, err
	}
	hasUnknown := false
	_, hasUnknown, _ = ledgerTotals(runRoot)
	return orderedFrom(
		[]string{"reservation_path", "prior_usd", "has_unknown", "admitted_total_usd"},
		[]any{path, prior, hasUnknown, prior + estimate},
	), nil
}

// CompleteBudgetOutcome ports Complete-BSLFlowCouncilBudgetOutcome.
func (e *Engine) CompleteBudgetOutcome(runRoot, role string, attempt *Attempt, status string, providerReportedUSD *float64, costState string) (*ordered, error) {
	lock := ledgerMutex(runRoot)
	lock.Lock()
	defer lock.Unlock()
	path := reservationPath(runRoot, role, attempt.Sequence)
	if !isRegularFile(path) {
		return nil, blocked("budget reservation is missing for %s.", role)
	}
	entry, err := readJSONObject(path)
	if err != nil {
		return nil, err
	}
	if asStringOr(entry["attempt_id"]) != attempt.AttemptID {
		return nil, blocked("budget reservation belongs to another attempt for %s.", role)
	}
	var outcomeUSD any
	if providerReportedUSD != nil {
		outcomeUSD = *providerReportedUSD
	}
	object := newOrdered()
	for _, key := range []string{"role", "attempt_id", "sequence", "estimated_usd", "outcome", "outcome_usd", "cost_state", "recorded_at_utc"} {
		object.set(key, entry[key])
	}
	object.set("outcome", status)
	object.set("outcome_usd", outcomeUSD)
	object.set("cost_state", costState)
	object.set("outcome_recorded_at_utc", roundTripUTCTime(e.now()))
	if err := writeBSLFlowJSONAtomic(path, object); err != nil {
		return nil, err
	}
	return object, nil
}

// TestLedgerAdmission ports Test-BSLFlowCouncilLedgerAdmission.
func TestLedgerAdmission(runRoot string, budget *BudgetConfig, dispatches []float64) (*ordered, error) {
	lock := ledgerMutex(runRoot)
	lock.Lock()
	defer lock.Unlock()
	prior, hasUnknown, err := ledgerTotals(runRoot)
	if err != nil {
		return nil, err
	}
	total := prior
	for _, estimate := range dispatches {
		if !finiteNonNegative(estimate) {
			return nil, blocked("dispatch cost estimate must be a non-negative finite number.")
		}
		total += estimate
	}
	if budget != nil && budget.Limit != nil {
		limit := *budget.Limit
		reservation := 0.0
		if budget.Reservation != nil {
			reservation = *budget.Reservation
		}
		if total+reservation > limit {
			return nil, blocked("council budget admission refused for the full dispatch cycle.")
		}
	}
	return orderedFrom(
		[]string{"admitted", "estimated_total_usd", "has_unknown_outcome"},
		[]any{true, total, hasUnknown},
	), nil
}

// --- prepared publication ---

// NewPreparedPackage ports New-BSLFlowCouncilPreparedPackage.
func (e *Engine) NewPreparedPackage(runRoot string, review *ordered, finalSpecBytes []byte, finalDesignBytes, existingReviewBytes []byte) (*ordered, error) {
	if err := AssertCouncilReview(review); err != nil {
		return nil, err
	}
	canonicalReviewBytes, err := convertToJSON(review, 20)
	if err != nil {
		return nil, invalid("%v", err)
	}
	reviewDigest, err := CouncilReviewDigest(review)
	if err != nil {
		return nil, err
	}
	inputs, _ := asOrdered(review.get("inputs"))
	reconciliation, _ := asOrdered(review.get("reconciliation"))
	var expectedDraftDesign, expectedDraftReview, intendedFinalDesign any
	if designHash := inputs.get("design_sha256"); designHash != nil {
		expectedDraftDesign = designHash
	}
	if existingReviewBytes != nil {
		expectedDraftReview = sha256Hex(existingReviewBytes)
	}
	if finalDesignBytes != nil {
		intendedFinalDesign = sha256Hex(finalDesignBytes)
	}
	packageObject := orderedFrom(
		[]string{"schema_version", "prepared_at_utc", "expected_draft_spec_sha256", "expected_draft_original_sha256", "expected_draft_design_sha256", "expected_draft_review_file_sha256", "intended_final_spec_sha256", "intended_final_design_sha256", "review_sha256", "review_file_sha256"},
		[]any{
			json.Number("1"), roundTripUTCTime(e.now()),
			asStringOr(inputs.get("spec_sha256")), asStringOr(inputs.get("original_task_sha256")),
			expectedDraftDesign, expectedDraftReview,
			sha256Hex(finalSpecBytes), intendedFinalDesign,
			reviewDigest, sha256Hex(canonicalReviewBytes),
		},
	)
	_ = reconciliation
	publicationDir := filepath.Join(runRoot, "publication")
	if err := os.MkdirAll(publicationDir, 0o755); err != nil {
		return nil, blocked("%v", err)
	}
	if err := os.WriteFile(filepath.Join(publicationDir, "intended-spec.md"), finalSpecBytes, 0o644); err != nil {
		return nil, blocked("%v", err)
	}
	if finalDesignBytes != nil {
		if err := os.WriteFile(filepath.Join(publicationDir, "intended-design.md"), finalDesignBytes, 0o644); err != nil {
			return nil, blocked("%v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(publicationDir, "canonical-review.json"), canonicalReviewBytes, 0o644); err != nil {
		return nil, blocked("%v", err)
	}
	if err := writeBSLFlowJSONAtomic(filepath.Join(publicationDir, "prepared.json"), packageObject); err != nil {
		return nil, err
	}
	preparedPath := filepath.Join(publicationDir, "prepared.json")
	packageSHA256, err := fileSHA256(preparedPath)
	if err != nil {
		return nil, err
	}
	event := orderedFrom(
		[]string{"event", "at_utc", "package_sha256"},
		[]any{"prepared", asStringOr(packageObject.get("prepared_at_utc")), packageSHA256},
	)
	if err := writeBSLFlowJSONAtomic(filepath.Join(publicationDir, "prepared.event.json"), event); err != nil {
		return nil, err
	}
	return packageObject, nil
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", blocked("%v", err)
	}
	return sha256Hex(data), nil
}

func isRegularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func readFileBytes(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, blocked("File is missing: %s", path)
	}
	return data, nil
}
