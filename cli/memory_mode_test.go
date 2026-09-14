package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunMemoryModeInvalidInput(t *testing.T) {
	var stdout bytes.Buffer
	code := runMemoryMode(strings.NewReader("not json"), &stdout)
	if code != 0 {
		t.Fatalf("memory mode exit code = %d, want 0 (the envelope carries failures)", code)
	}
	var envelope map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("memory mode stdout is not one JSON envelope: %v", err)
	}
	if available, _ := envelope["available"].(bool); available {
		t.Fatal("invalid input must produce an unavailable envelope")
	}
	if reason, _ := envelope["disabled_reason"].(string); !strings.HasPrefix(reason, "BF_INVALID") {
		t.Fatalf("unexpected disabled_reason: %q", reason)
	}
}

func TestRunMemoryModeGarbageOperation(t *testing.T) {
	var stdout bytes.Buffer
	request := `{"schema_version":1,"operation":"nope"}`
	code := runMemoryMode(strings.NewReader(request), &stdout)
	if code != 0 {
		t.Fatalf("memory mode exit code = %d, want 0", code)
	}
	var envelope map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("memory mode stdout is not one JSON envelope: %v", err)
	}
	if reason, _ := envelope["disabled_reason"].(string); reason != "BF_INVALID: Unsupported native memory operation." &&
		!strings.HasPrefix(reason, "BF_INVALID") {
		t.Fatalf("unexpected disabled_reason: %q", reason)
	}
}
