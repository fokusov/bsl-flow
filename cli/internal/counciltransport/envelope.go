package counciltransport

import (
	"fmt"
	"regexp"
	"strings"
)

// Member envelope contract: members[] of council-review-schema.json (v2),
// emitted by Register-BSLFlowCouncilMemberResult (Council.Engine.ps1). The
// closed field and enum sets come from the schema (additionalProperties
// false); identity, usage and cost state are controller-built, never model
// claims.
const (
	CostStateUnknown               = "unknown"
	CostStateProviderUsageReported = "provider_usage_reported"
	CostStateNoUsageReported       = "no_usage_reported"
	memberSchemaVersion            = 1
)

var memberCostStates = map[string]bool{
	CostStateUnknown:               true,
	CostStateProviderUsageReported: true,
	CostStateNoUsageReported:       true,
}

var memberRoles = map[string]bool{
	"brainstorm":           true,
	"intent_critic":        true,
	"architecture_critic":  true,
	"executability_critic": true,
	"chair":                true,
}

var memberStatuses = map[string]bool{
	"completed":                true,
	"failed_before_acceptance": true,
	"unknown_after_dispatch":   true,
	"cancelled":                true,
	"invalid_response":         true,
}

var memberExecutionModes = map[string]bool{
	"direct_api":             true,
	"current_agent_fallback": true,
}

var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Identity is the requested triple (all fields required non-empty).
type Identity struct {
	Provider string
	Model    string
	Effort   string
}

// Observed is the provider-observed triple; every field is nullable.
type Observed struct {
	Provider *string
	Model    *string
	Effort   *string
}

// InputHashes are the controller-computed input provenance hashes.
type InputHashes struct {
	OriginalTaskSHA256 string
	SpecSHA256         *string
}

// MemberUsage is the nullable per-field usage projection of the member
// envelope; a nil MemberUsage means the provider reported no usage at all.
type MemberUsage struct {
	InputTokens     *int
	OutputTokens    *int
	ReasoningTokens *int
}

// MemberUsageFrom projects a parsed transport usage into the member envelope
// vocabulary (input_tokens/output_tokens/reasoning_tokens).
func MemberUsageFrom(usage Usage) *MemberUsage {
	prompt, completion, reasoning := usage.PromptTokens, usage.CompletionTokens, usage.ReasoningTokens
	return &MemberUsage{InputTokens: &prompt, OutputTokens: &completion, ReasoningTokens: &reasoning}
}

// Member is the controller-built member envelope record.
type Member struct {
	SchemaVersion   int
	Role            string
	AttemptID       string
	Status          string
	Summary         string
	Requested       Identity
	Observed        Observed
	ExecutionMode   string
	FallbackReason  *string
	InputHashes     InputHashes
	PayloadSHA256   *string
	Usage           *MemberUsage
	CostState       string
	DispatchedAtUTC *string
	CompletedAtUTC  *string
}

func (m Member) Canonical() ([]byte, error) {
	wire, err := m.wire()
	if err != nil {
		return nil, err
	}
	return marshalDeterministic(wire)
}

func (m Member) wire() (*wireMember, error) {
	version := m.SchemaVersion
	if version == 0 {
		version = memberSchemaVersion
	}
	if version != memberSchemaVersion {
		return nil, fmt.Errorf("counciltransport: member envelope schema_version must be %d", memberSchemaVersion)
	}
	if !memberRoles[m.Role] {
		return nil, fmt.Errorf("counciltransport: unknown member role %q", m.Role)
	}
	if strings.TrimSpace(m.AttemptID) == "" {
		return nil, fmt.Errorf("counciltransport: member attempt_id must not be empty")
	}
	if !memberStatuses[m.Status] {
		return nil, fmt.Errorf("counciltransport: unknown member status %q", m.Status)
	}
	if strings.TrimSpace(m.Summary) == "" {
		return nil, fmt.Errorf("counciltransport: member summary must not be empty")
	}
	for name, value := range map[string]string{
		"requested.provider": m.Requested.Provider,
		"requested.model":    m.Requested.Model,
		"requested.effort":   m.Requested.Effort,
	} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("counciltransport: member %s must not be empty", name)
		}
	}
	if !memberExecutionModes[m.ExecutionMode] {
		return nil, fmt.Errorf("counciltransport: unknown member execution_mode %q", m.ExecutionMode)
	}
	if !sha256Pattern.MatchString(m.InputHashes.OriginalTaskSHA256) {
		return nil, fmt.Errorf("counciltransport: member input_hashes.original_task_sha256 is not a sha256")
	}
	if m.InputHashes.SpecSHA256 != nil && !sha256Pattern.MatchString(*m.InputHashes.SpecSHA256) {
		return nil, fmt.Errorf("counciltransport: member input_hashes.spec_sha256 is not a sha256")
	}
	if m.PayloadSHA256 != nil && !sha256Pattern.MatchString(*m.PayloadSHA256) {
		return nil, fmt.Errorf("counciltransport: member payload_sha256 is not a sha256")
	}
	costState := m.resolveCostState()
	if !memberCostStates[costState] {
		return nil, fmt.Errorf("counciltransport: unknown member cost_state %q (closed set: unknown, provider_usage_reported, no_usage_reported)", m.CostState)
	}
	return &wireMember{
		SchemaVersion:   version,
		Role:            m.Role,
		AttemptID:       m.AttemptID,
		Status:          m.Status,
		Summary:         m.Summary,
		Requested:       wireIdentity(m.Requested),
		Observed:        wireObserved(m.Observed),
		ExecutionMode:   m.ExecutionMode,
		FallbackReason:  m.FallbackReason,
		InputHashes:     wireInputHashes(m.InputHashes),
		PayloadSHA256:   m.PayloadSHA256,
		Usage:           toWireMemberUsage(m.Usage),
		CostState:       costState,
		DispatchedAtUTC: m.DispatchedAtUTC,
		CompletedAtUTC:  m.CompletedAtUTC,
	}, nil
}

// resolveCostState mirrors the legacy resolution: a completed attempt derives
// provider_usage_reported or no_usage_reported from usage presence; anything
// else without an explicit state is unknown.
func (m Member) resolveCostState() string {
	if m.CostState != "" {
		return m.CostState
	}
	if m.Status == "completed" {
		if m.Usage != nil {
			return CostStateProviderUsageReported
		}
		return CostStateNoUsageReported
	}
	return CostStateUnknown
}

type wireIdentity struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
}

type wireObserved struct {
	Provider *string `json:"provider"`
	Model    *string `json:"model"`
	Effort   *string `json:"effort"`
}

type wireInputHashes struct {
	OriginalTaskSHA256 string  `json:"original_task_sha256"`
	SpecSHA256         *string `json:"spec_sha256"`
}

type wireMemberUsage struct {
	InputTokens     *int `json:"input_tokens"`
	OutputTokens    *int `json:"output_tokens"`
	ReasoningTokens *int `json:"reasoning_tokens"`
}

func toWireMemberUsage(usage *MemberUsage) *wireMemberUsage {
	if usage == nil {
		return nil
	}
	return &wireMemberUsage{
		InputTokens:     usage.InputTokens,
		OutputTokens:    usage.OutputTokens,
		ReasoningTokens: usage.ReasoningTokens,
	}
}

// wireMember field order mirrors the legacy envelope emission order and is
// part of the deterministic canonical bytes.
type wireMember struct {
	SchemaVersion   int              `json:"schema_version"`
	Role            string           `json:"role"`
	AttemptID       string           `json:"attempt_id"`
	Status          string           `json:"status"`
	Summary         string           `json:"summary"`
	Requested       wireIdentity     `json:"requested"`
	Observed        wireObserved     `json:"observed"`
	ExecutionMode   string           `json:"execution_mode"`
	FallbackReason  *string          `json:"fallback_reason"`
	InputHashes     wireInputHashes  `json:"input_hashes"`
	PayloadSHA256   *string          `json:"payload_sha256"`
	Usage           *wireMemberUsage `json:"usage"`
	CostState       string           `json:"cost_state"`
	DispatchedAtUTC *string          `json:"dispatched_at_utc"`
	CompletedAtUTC  *string          `json:"completed_at_utc"`
}
