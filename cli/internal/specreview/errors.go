package specreview

import "fmt"

// errors carries the BF_* classified error helpers for the single-reviewer
// route. The class prefix mirrors the PowerShell surface; the message text is
// what the CLI surfaces to the caller.

func invalidf(format string, args ...any) error {
	return fmt.Errorf("BF_INVALID: "+format, args...)
}

func blockedf(format string, args ...any) error {
	return fmt.Errorf("BF_BLOCKED: "+format, args...)
}
