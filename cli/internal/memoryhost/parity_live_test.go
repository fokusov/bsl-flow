package memoryhost

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestPowerShellLiveParity is a differential parity check: it executes the
// real Invoke-BFNativeMemory.ps1 (from a sandboxed copy of the skill, with a
// fixture package layout) against a project, harvests the resulting
// envelopes, event files and index, wipes the memory store, then replays the
// identical request sequence through the Go host with the event clock frozen
// to the harvested timestamps. Both runs use the same absolute project path,
// so project-scoped hashes must match byte for byte. It skips when pwsh or
// the packaged scripts are unavailable.
func TestPowerShellLiveParity(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh is unavailable for the PowerShell parity run")
	}
	scriptsDir := repoScriptsDir(t)
	if scriptsDir == "" {
		t.Skip("packaged skill scripts are unavailable")
	}

	root := t.TempDir()
	packageRoot := filepath.Join(root, "pkg")
	sandboxScripts := filepath.Join(packageRoot, "global", "skills", "1c-task", "scripts")
	if err := os.MkdirAll(sandboxScripts, 0o755); err != nil {
		t.Fatal(err)
	}
	sources, err := filepath.Glob(filepath.Join(scriptsDir, "*.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) == 0 {
		t.Skip("packaged skill scripts are unavailable")
	}
	for _, source := range sources {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sandboxScripts, filepath.Base(source)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	schemas := filepath.Join(packageRoot, "global", "skills", "1c-task", "schemas")
	if err := os.MkdirAll(schemas, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"memory-event.schema.json", "memory-index.schema.json", "memory-bundle.schema.json", "context.schema.json"} {
		content := "{\"schema\":\"" + name + "\",\"fixture\":true}"
		if err := os.WriteFile(filepath.Join(schemas, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(packageRoot, "VERSION"), []byte("9.9.9-parity"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, "package-manifest.json"), []byte(`{"fixture":"parity"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	memoryRoot := filepath.Join(project, ".bsl-flow", "memory")

	state := parityState(project)
	state2 := parityState(project)
	state2["repair"] = map[string]any{"rounds": jsonNumberOf("1"), "pending_failure": testFailureID, "last_source_sha256": nil, "diagnosis_attempt": nil}
	state2["status"] = "running"
	state2["stage"] = "diagnose"
	result := parityResult()
	passResult := parityPassResult()
	pending := parityPendingFailure()
	receipt := parityReceipt()

	resultHash, err := hashValue(result)
	if err != nil {
		t.Fatal(err)
	}
	passResultHash, err := hashValue(passResult)
	if err != nil {
		t.Fatal(err)
	}
	receiptHash, err := hashValue(receipt)
	if err != nil {
		t.Fatal(err)
	}
	pendingHash, err := hashValue(pending)
	if err != nil {
		t.Fatal(err)
	}

	type parityStep struct {
		name        string
		state       map[string]any
		operation   string
		stage       string
		result      map[string]any
		resultHash  string
		receipt     map[string]any
		receiptHash string
		next        map[string]any
		pending     map[string]any
		pendingHash string
	}
	steps := []parityStep{
		{name: "bind1", state: state, operation: "bind", stage: "implement"},
		{name: "accept1", state: state, operation: "extract-acceptance", receipt: receipt, receiptHash: receiptHash},
		{name: "bind2", state: state, operation: "bind", stage: "implement"},
		{name: "attempt1", state: state, operation: "extract-attempt", result: result, resultHash: resultHash},
		{name: "proj1", state: state, operation: "projection", next: map[string]any{"stage": "verify", "action": "run"}},
		{name: "accept2", state: state, operation: "extract-acceptance", receipt: receipt, receiptHash: receiptHash},
		{name: "attempt2", state: state, operation: "extract-attempt", result: passResult, resultHash: passResultHash},
		{name: "bind3", state: state2, operation: "bind", stage: "diagnose", pending: pending, pendingHash: pendingHash},
		{name: "proj2", state: state2, operation: "projection", next: map[string]any{"stage": "diagnose"}, pending: pending, pendingHash: pendingHash},
		{name: "bogus", state: state, operation: "nope"},
	}

	entrypoint := filepath.Join(sandboxScripts, "Invoke-BFNativeMemory.ps1")
	powerShellEnvelopes := make(map[string]string, len(steps))
	for _, step := range steps {
		encoded, err := canonicalText(parityInput(step.state, step.operation, step.stage, step.result, step.resultHash, step.receipt, step.receiptHash, step.next, step.pending, step.pendingHash, memoryRoot))
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", entrypoint)
		command.Stdin = strings.NewReader(encoded)
		var stdout, stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		if err := command.Run(); err != nil {
			t.Fatalf("%s: pwsh failed: %v: %s", step.name, err, stderr.String())
		}
		if command.ProcessState.ExitCode() != 0 {
			t.Fatalf("%s: pwsh exited %d", step.name, command.ProcessState.ExitCode())
		}
		if stdout.Len() == 0 {
			t.Fatalf("%s: pwsh produced no envelope", step.name)
		}
		powerShellEnvelopes[step.name] = stdout.String()
	}

	harvestedEvents, timestamps := parityHarvestEvents(t, memoryRoot)
	powerShellIndex := parityReadIndexNormalized(t, memoryRoot)

	// Wipe the store and replay the identical sequence through the Go host
	// with the clock frozen to the harvested event timestamps.
	if err := os.RemoveAll(memoryRoot); err != nil {
		t.Fatal(err)
	}
	clockIndex := 0
	previousClock := nowUTC
	nowUTC = func() time.Time {
		stamp := timestamps[clockIndex%len(timestamps)]
		clockIndex++
		return stamp
	}
	defer func() { nowUTC = previousClock }()

	goEnvelopes := make(map[string]string, len(steps))
	for _, step := range steps {
		encoded, err := canonicalText(parityInput(step.state, step.operation, step.stage, step.result, step.resultHash, step.receipt, step.receiptHash, step.next, step.pending, step.pendingHash, memoryRoot))
		if err != nil {
			t.Fatal(err)
		}
		var stdout bytes.Buffer
		if code := Run(strings.NewReader(encoded), &stdout, packageRoot); code != 0 {
			t.Fatalf("%s: Go host exited %d", step.name, code)
		}
		goEnvelopes[step.name] = stdout.String()
	}
	for _, step := range steps {
		if goEnvelopes[step.name] != powerShellEnvelopes[step.name] {
			t.Fatalf("%s envelope mismatch:\n  go %s\n  ps %s", step.name, goEnvelopes[step.name], powerShellEnvelopes[step.name])
		}
	}

	goEvents, goTimestamps := parityHarvestEvents(t, memoryRoot)
	if len(goTimestamps) != len(timestamps) {
		t.Fatalf("event count mismatch: go %d ps %d", len(goTimestamps), len(timestamps))
	}
	if len(goEvents) != len(harvestedEvents) {
		t.Fatalf("event file mismatch: go %d ps %d", len(goEvents), len(harvestedEvents))
	}
	for name, golden := range harvestedEvents {
		actual, ok := goEvents[name]
		if !ok {
			t.Fatalf("event %s missing in the Go run", name)
		}
		if actual != golden {
			t.Fatalf("event %s mismatch:\n  go %s\n  ps %s", name, actual, golden)
		}
	}
	if got := parityReadIndexNormalized(t, memoryRoot); got != powerShellIndex {
		t.Fatalf("index mismatch:\n  go %s\n  ps %s", got, powerShellIndex)
	}
	if clockIndex != len(timestamps) {
		t.Fatalf("consumed %d timestamps want %d", clockIndex, len(timestamps))
	}
}

func repoScriptsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	dir := filepath.Dir(thisFile)
	scripts := filepath.Join(dir, "..", "..", "..", "global", "skills", "1c-task", "scripts")
	info, err := os.Stat(scripts)
	if err != nil || !info.IsDir() {
		return ""
	}
	return scripts
}

func parityInput(state map[string]any, operation, stage string, result map[string]any, resultHash string, receipt map[string]any, receiptHash string, next map[string]any, pending map[string]any, pendingHash string, memoryRoot string) map[string]any {
	return map[string]any{
		"schema_version":              jsonNumberOf("1"),
		"operation":                   operation,
		"state":                       state,
		"stage":                       stage,
		"result":                      result,
		"result_hash":                 resultHash,
		"receipt":                     receipt,
		"receipt_hash":                receiptHash,
		"next":                        next,
		"memory_root":                 memoryRoot,
		"pending_failure_result":      pending,
		"pending_failure_result_hash": pendingHash,
	}
}

func parityState(project string) map[string]any {
	return map[string]any{
		"schema_version":         jsonNumberOf("1"),
		"task_id":                testTaskID,
		"revision":               jsonNumberOf("7"),
		"previous_sha256":        nil,
		"project_path":           project,
		"worker_path":            nil,
		"baseline":               "abc123",
		"request":                parityRequest(),
		"request_hash":           strings.Repeat("0", 64),
		"intent_revision":        jsonNumberOf("1"),
		"authorization_revision": jsonNumberOf("1"),
		"intent_hash":            strings.Repeat("1", 64),
		"policy_hash":            testPolicyHash,
		"policy_files":           []any{},
		"policy_rules":           nil,
		"classification":         map[string]any{"complexity": "S", "risk": "low", "impact_flags": []any{}},
		"status":                 "completed",
		"stage":                  "acceptance",
		"active_attempt":         nil,
		"unresolved_effect":      nil,
		"attempts":               []any{},
		"evidence":               []any{},
		"events":                 []any{},
		"question":               nil,
		"blockers":               []any{},
		"acceptances":            []any{},
		"created_at":             "2026-01-01T00:00:00.0000000Z",
		"updated_at":             "2026-01-02T00:00:00.0000000Z",
		"correction_rounds":      jsonNumberOf("0"),
		"repair":                 nil,
	}
}

func parityRequest() map[string]any {
	return map[string]any{
		"schema_version": jsonNumberOf("1"),
		"request_id":     testTaskID,
		"prompt":         "do the thing",
		"mode":           "implement",
		"analysis_goal":  "analysis",
		"complexity":     "S",
		"risk":           "low",
		"impact_flags":   []any{},
		"criteria": []any{map[string]any{
			"id":          "crit-1",
			"observation": "file has marker",
			"kind":        "file_assertion",
			"path":        "src/a.bsl",
			"contains":    "MARKER",
		}},
		"provenance":   map[string]any{"source": "user", "reference": "ref-1", "text": "please"},
		"models":       map[string]any{"worker": "m1", "worker_effort": "low", "reviewer": "m2", "reviewer_effort": "low"},
		"source_paths": []any{"src", "lib"},
	}
}

func parityResult() map[string]any {
	return map[string]any{
		"schema_version": jsonNumberOf("1"),
		"task_id":        testTaskID,
		"attempt_id":     testAttemptID,
		"stage":          "verify",
		"outcome":        "FAIL",
		"side_effects":   "none",
		"proposal":       map[string]any{"criterion_id": "crit-1", "kind": "static", "category": "build"},
		"observations":   []any{},
	}
}

func parityPassResult() map[string]any {
	return map[string]any{
		"schema_version": jsonNumberOf("1"),
		"task_id":        testTaskID,
		"attempt_id":     testAttemptID,
		"stage":          "implement",
		"outcome":        "PASS",
		"side_effects":   "none",
		"proposal": map[string]any{"observations": []any{
			map[string]any{
				"scope":           []any{"src"},
				"observation":     "a valid worker observation about the flow",
				"action_type":     "recommended",
				"action":          "try the bounded flow",
				"knowledge_class": "procedural",
				"risk_class":      "low",
			},
			map[string]any{
				"scope":           []any{"..", "evil"},
				"observation":     "bad path",
				"action_type":     "recommended",
				"action":          "x",
				"knowledge_class": "procedural",
				"risk_class":      "low",
			},
		}},
	}
}

func parityPendingFailure() map[string]any {
	return map[string]any{
		"schema_version": jsonNumberOf("1"),
		"task_id":        testTaskID,
		"attempt_id":     testFailureID,
		"stage":          "verify",
		"outcome":        "FAIL",
		"side_effects":   "none",
		"proposal":       map[string]any{"criterion_id": "crit-1", "kind": "static", "category": "build"},
	}
}

func parityReceipt() map[string]any {
	return map[string]any{
		"schema_version": jsonNumberOf("1"),
		"task_id":        testTaskID,
		"verdict":        "PASS",
		"gates": []any{
			map[string]any{"stage": "implement", "outcome": "PASS"},
			map[string]any{"stage": "verify", "outcome": "PASS"},
		},
		"summary": "ok",
	}
}

// parityHarvestEvents returns the event file contents by name and their
// timestamps in sequence order.
func parityHarvestEvents(t *testing.T, memoryRoot string) (map[string]string, []time.Time) {
	t.Helper()
	events := map[string]string{}
	timestamps := []time.Time{}
	directory := filepath.Join(memoryRoot, "events")
	entries, err := os.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return events, timestamps
		}
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		events[name] = string(data)
		var event struct {
			Timestamp string `json:"timestamp"`
		}
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatalf("event %s: %v", name, err)
		}
		stamp, err := time.Parse("2006-01-02T15:04:05.0000000Z07:00", event.Timestamp)
		if err != nil {
			t.Fatalf("event %s timestamp %s: %v", name, event.Timestamp, err)
		}
		timestamps = append(timestamps, stamp)
	}
	return events, timestamps
}

// parityReadIndexNormalized reads index.json and replaces the mtime-derived
// event_files_stamp with a constant so both engines compare equal.
func parityReadIndexNormalized(t *testing.T, memoryRoot string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(memoryRoot, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	object := parityDecodeObject(t, string(data))
	object["event_files_stamp"] = "STAMP"
	text, err := canonicalText(object)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func parityDecodeObject(t *testing.T, text string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode: %v", err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatal("not an object")
	}
	return object
}
