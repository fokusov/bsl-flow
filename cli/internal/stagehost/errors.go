// Package stagehost implements the native Go stage host: the in-binary port
// of the packaged PowerShell provider (Invoke-BFNativeProvider.ps1 ->
// Task.Provider.ps1). It runs as a hidden `bsl-flow __provider` subprocess of
// the same trusted host binary, receives one canonical provider input document
// on stdin and emits one canonical observation on stdout. PowerShell is never
// launched from this path.
//
// Migration boundary: this host serves the activation measure, the
// deterministic verify stage and every worker-dispatching stage (inspect,
// spec, spec_review, implement, code_review, diagnose) through the native
// worker library. The live council spec_review route stays a typed blocker
// until the council engine is ported; a native failure is never rerouted to
// PowerShell.
package stagehost

import "fmt"

// Contract is the provider wire contract served by this host. The name is
// retained from the packaged provider identity on purpose: the controller
// persists it in attempt/engine bindings, and the migration keeps one wire
// identity while the transport receipt identity check distinguishes the
// native host process from the PowerShell process.
const Contract = "bsl-flow.native-provider.windows-ps.v1"

// Error classes mirror the PowerShell provider taxonomy. The full prefixed
// text is what the host writes to stderr, matching the legacy process error
// surface byte for byte.
const (
	ClassInvalid  = "BF_INVALID"
	ClassBlocked  = "BF_BLOCKED"
	ClassConflict = "BF_CONFLICT"
	ClassFail     = "BF_FAIL"
)

// Error is a classified provider failure. Class is one of the BF_* constants.
type Error struct {
	Class   string
	Message string
}

func (e *Error) Error() string { return e.Class + ": " + e.Message }

func invalidf(format string, args ...any) error {
	return &Error{Class: ClassInvalid, Message: fmt.Sprintf(format, args...)}
}

func blockedf(format string, args ...any) error {
	return &Error{Class: ClassBlocked, Message: fmt.Sprintf(format, args...)}
}

func conflictf(format string, args ...any) error {
	return &Error{Class: ClassConflict, Message: fmt.Sprintf(format, args...)}
}

func failf(format string, args ...any) error {
	return &Error{Class: ClassFail, Message: fmt.Sprintf(format, args...)}
}

// VerificationFailure carries the typed marker the controller uses to decide
// repair eligibility. It mirrors the BF_VerificationFailure exception data of
// Stop-BFVerificationFailure: only the stage host sets it, never worker text.
type VerificationFailure struct {
	Err            error
	RepairEligible bool
	CriterionID    string
	Kind           string
	Observation    string
}

func (v *VerificationFailure) Error() string { return v.Err.Error() }

func (v *VerificationFailure) Unwrap() error { return v.Err }
