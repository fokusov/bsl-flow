package memoryhost

// This file ports the error surface shared by Task.Storage.ps1
// (New-BFError) and the strict-mode property access diagnostics that the
// PowerShell helper surfaces through its bounded disabled_reason text.

import (
	"encoding/json"
	"fmt"
)

// bfError mirrors New-BFError: the exception message is "<kind>: <message>".
type bfError struct {
	kind    string
	message string
}

func (e *bfError) Error() string { return e.kind + ": " + e.message }

func newBFError(kind, message string) error {
	return &bfError{kind: kind, message: message}
}

func bfInvalid(format string, args ...any) error {
	return newBFError("BF_INVALID", fmt.Sprintf(format, args...))
}

func bfBlocked(format string, args ...any) error {
	return newBFError("BF_BLOCKED", fmt.Sprintf(format, args...))
}

func bfConflict(format string, args ...any) error {
	return newBFError("BF_CONFLICT", fmt.Sprintf(format, args...))
}

// strictPropertyError reproduces the PowerShell StrictMode diagnostic that a
// direct "$obj.field" access raises when the property is absent. The helper
// catches it exactly like any other failure and bounds the text.
type strictPropertyError struct{ name string }

func (e *strictPropertyError) Error() string {
	return fmt.Sprintf("The property '%s' cannot be found on this object.", e.name)
}

// strictString mirrors a direct PowerShell property access under
// Set-StrictMode -Version Latest: absent properties raise instead of
// yielding $null.
func strictString(object map[string]any, name string) (string, error) {
	if object == nil {
		return "", &strictPropertyError{name: name}
	}
	value, present := object[name]
	if !present {
		return "", &strictPropertyError{name: name}
	}
	return psString(value), nil
}

// psString mirrors a PowerShell [string] cast: $null becomes "", numbers keep
// their literal form and booleans render as True/False.
func psString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprintf("%v", typed)
	}
}

// psInt mirrors a PowerShell [int] cast for the numeric fields used by the
// memory projection: $null becomes 0 and unconvertible values degrade to 0
// (the surrounding catch keeps parity for genuinely corrupt input).
func psInt(value any) int {
	switch typed := value.(type) {
	case nil:
		return 0
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
		if parsed, err := typed.Float64(); err == nil {
			return int(parsed)
		}
		return 0
	case float64:
		return int(typed)
	default:
		return 0
	}
}
