// Package worker implements the native Codex worker-adapter contracts for the
// staged PowerShell migration (openspec/changes/native-cross-platform-cli
// requirement 10): structured parsing of the worker JSONL event stream and the
// host rollout session file, the host-result receipt, sealed input hashing and
// the capability gate that must produce BLOCKED instead of a weakened launch.
// The package only classifies parsed data; process spawning and transports are
// out of scope and stay injected by callers.
package worker

import (
	"errors"
	"fmt"
)

// ErrorClass carries the BF_* blocker class of a worker failure, mirroring the
// New-BFError kind prefixes of the PowerShell adapters. An optional cause
// keeps sentinel classification composable with the class.
type ErrorClass struct {
	Kind    string
	Message string
	cause   error
}

func (e *ErrorClass) Error() string { return e.Kind + ": " + e.Message }

func (e *ErrorClass) Unwrap() error { return e.cause }

// ClassOf reports the BF_* class of err, or an empty string for foreign errors.
func ClassOf(err error) string {
	var classified *ErrorClass
	if errors.As(err, &classified) {
		return classified.Kind
	}
	return ""
}

func blocked(format string, args ...any) error {
	return &ErrorClass{Kind: "BF_BLOCKED", Message: fmt.Sprintf(format, args...)}
}

// blockedCause classifies a BLOCKED refusal that wraps a sentinel error, so
// both errors.Is (sentinel) and errors.As (class) succeed.
func blockedCause(cause error, format string, args ...any) error {
	return &ErrorClass{Kind: "BF_BLOCKED", Message: fmt.Sprintf(format, args...), cause: cause}
}

func invalid(format string, args ...any) error {
	return &ErrorClass{Kind: "BF_INVALID", Message: fmt.Sprintf(format, args...)}
}

// Sentinel classification errors. Every ParseRolloutSession failure wraps one
// of them so callers can branch on the failure mode without matching text.
var (
	// ErrRolloutTooLarge reports a rollout stream above the byte or line bound.
	ErrRolloutTooLarge = errors.New("rollout session exceeds the parsing bound")
	// ErrRolloutMalformed reports an invalid JSON line in the rollout stream.
	ErrRolloutMalformed = errors.New("malformed rollout JSONL record")
	// ErrRolloutSessionMeta reports a missing, duplicated or unidentified
	// session_meta record.
	ErrRolloutSessionMeta = errors.New("rollout session metadata is unusable")
	// ErrRolloutMissingTurn reports that no usable turn_context payload exists.
	ErrRolloutMissingTurn = errors.New("rollout has no usable turn_context payload")
	// ErrRolloutAmbiguousTurn reports more than one usable turn_context payload.
	ErrRolloutAmbiguousTurn = errors.New("rollout has ambiguous turn_context payloads")
	// ErrRolloutIncompleteIdentity reports a usable turn_context payload without
	// a resolved model and effort.
	ErrRolloutIncompleteIdentity = errors.New("rollout turn_context is missing resolved model/effort")
)
