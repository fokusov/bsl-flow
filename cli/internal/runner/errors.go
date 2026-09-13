package runner

import (
	"errors"
	"fmt"
	"strings"
)

// ErrorClass carries a runner error class and message for exit mapping, using
// the same BF_* classes as the rest of the CLI. (The type is not named
// KindError because KindError is the journal event vocabulary value "error".)
type ErrorClass struct {
	Kind    string
	Message string
}

func (e *ErrorClass) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &ErrorClass{Kind: "BF_INVALID", Message: fmt.Sprintf(format, args...)}
}

func blocked(format string, args ...any) error {
	return &ErrorClass{Kind: "BF_BLOCKED", Message: fmt.Sprintf(format, args...)}
}

func conflict(format string, args ...any) error {
	return &ErrorClass{Kind: "BF_CONFLICT", Message: fmt.Sprintf(format, args...)}
}

// ErrTornEvent reports a truncated or incomplete final journal record. The
// events returned alongside it are complete; the caller must mark the replayed
// state for reconciliation instead of resuming dispatch blindly. Mirrors the
// PowerShell "incomplete final record; inspect before resuming" refusal.
var ErrTornEvent = errors.New("runner journal has an incomplete final record")

// ErrQuiet reports that a full round-robin scan found no dispatchable task.
// This is a normal polling outcome (the queue observation action is 'quiet'),
// not a failure: the supervisor keeps polling.
var ErrQuiet = errors.New("runner queue is quiet")

func truncateReason(message string) string {
	// Get-BFRunnerErrorSummary bounds the persisted error text to 1024 runes
	// after collapsing line breaks.
	text := strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(message))
	if len(text) > 1024 {
		text = text[:1024]
	}
	return text
}
