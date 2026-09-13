package worker

import (
	"strings"
	"unicode/utf8"
)

// Identity sources accepted by ValidateObserved. Observed identity must come
// only from persisted host evidence — the rollout session file or a provider
// envelope — never from request values (the identity rule documented at
// Codex.ps1:91-93 and Task.Storage.ps1:375-378).
const (
	IdentitySourceRollout  = "rollout_session"
	IdentitySourceProvider = "provider_envelope"
)

// Terminal result statuses of the sealed worker result
// (Codex.ps1:89, ProfiledCodex.ps1:350).
const (
	StatusCompleted  = "completed"
	StatusNeedsInput = "needs_input"
	StatusBlocked    = "blocked"
	StatusFailed     = "failed"
)

// HostResult mirrors the adapter host-result receipt (host-result.json)
// consolidated with the terminal worker-result boundary. The PS writer emits
// canonical key-sorted JSON through Write-BFJson → Get-BFCanonicalJson
// (Task.Storage.ps1:683,108-112), with observed_model/observed_effort always
// present but null for ephemeral runs (Codex.ps1:99-101) and
// rollout_path/turn_id present only on the strict route (Codex.ps1:102-107).
// status/exit_code/summary consolidate model-result.json and exit.json of the
// same attempt directory.
type HostResult struct {
	SessionID       string
	RequestedModel  string
	RequestedEffort string
	ObservedModel   string
	ObservedEffort  string
	// IdentitySource records where the observed identity came from
	// (rollout_session|provider_envelope). It is validation metadata for
	// ValidateObserved and is not part of the canonical receipt bytes.
	IdentitySource string
	Usage          *Usage
	UsageSource    string
	RolloutPath    string
	TurnID         string
	Status         string
	ExitCode       int
	Summary        string
}

// Canonical returns the deterministic receipt encoding: member keys sorted
// byte-wise (Get-BFCanonicalJson sorts every object level with
// StringComparer.Ordinal), nullable identity fields encoded as null when
// empty, rollout_path/turn_id omitted when empty, and minimal string
// escaping. It fails only when a member string is not valid UTF-8, mirroring
// the surrogate refusal of ConvertTo-BFJsonString.
func (h HostResult) Canonical() ([]byte, error) {
	for _, value := range []string{
		h.SessionID, h.RequestedModel, h.RequestedEffort, h.ObservedModel,
		h.ObservedEffort, h.UsageSource, h.RolloutPath, h.TurnID, h.Status, h.Summary,
	} {
		if !utf8.ValidString(value) {
			return nil, invalid("strings must contain valid UTF-8")
		}
	}
	var dst []byte
	dst = append(dst, `{"exit_code":`...)
	dst = appendCanonicalInt(dst, int64(h.ExitCode))
	nullable := []struct {
		key   string
		value string
	}{
		{"observed_effort", h.ObservedEffort},
		{"observed_model", h.ObservedModel},
		{"requested_effort", h.RequestedEffort},
		{"requested_model", h.RequestedModel},
	}
	for _, field := range nullable {
		dst = append(dst, ',', '"')
		dst = append(dst, field.key...)
		dst = append(dst, '"', ':')
		if field.value == "" {
			dst = append(dst, `null`...)
			continue
		}
		dst = appendCanonicalString(dst, field.value)
	}
	if h.RolloutPath != "" {
		dst = append(dst, `,"rollout_path":`...)
		dst = appendCanonicalString(dst, h.RolloutPath)
	}
	dst = append(dst, `,"session_id":`...)
	dst = appendCanonicalString(dst, h.SessionID)
	dst = append(dst, `,"status":`...)
	dst = appendCanonicalString(dst, h.Status)
	dst = append(dst, `,"summary":`...)
	dst = appendCanonicalString(dst, h.Summary)
	if h.TurnID != "" {
		dst = append(dst, `,"turn_id":`...)
		dst = appendCanonicalString(dst, h.TurnID)
	}
	dst = append(dst, `,"usage":`...)
	dst = appendUsageCanonical(dst, h.Usage)
	dst = append(dst, `,"usage_source":`...)
	if h.UsageSource == "" {
		dst = append(dst, `null`...)
	} else {
		dst = appendCanonicalString(dst, h.UsageSource)
	}
	return append(dst, '}'), nil
}

// ValidateObserved enforces the observed-identity boundary of the strict
// receipt (ProfiledCodex.ps1:360-367):
//
//   - observed model and effort must be resolved and non-empty — the rollout
//     refusal "turn_context is missing resolved model/effort"
//     (Task.Storage.ps1:493-496);
//   - the identity must carry provenance: IdentitySource must be a known
//     evidence source, so a bare echo of the requested values without rollout
//     or provider evidence is rejected;
//   - when the request declares model/effort, the observed identity must
//     equal them exactly ("observed rollout model/effort differs from the
//     strict request", ProfiledCodex.ps1:366).
func ValidateObserved(h HostResult, requestedModel, requestedEffort string) error {
	if strings.TrimSpace(h.ObservedModel) == "" || strings.TrimSpace(h.ObservedEffort) == "" {
		return blocked("observed host identity is missing resolved model/effort")
	}
	switch h.IdentitySource {
	case IdentitySourceRollout, IdentitySourceProvider:
	default:
		return blocked("observed host identity has no trusted evidence source: %q", h.IdentitySource)
	}
	if requestedModel != "" && h.ObservedModel != requestedModel {
		return blocked("observed host model differs from the strict request")
	}
	if requestedEffort != "" && h.ObservedEffort != requestedEffort {
		return blocked("observed host effort differs from the strict request")
	}
	return nil
}
