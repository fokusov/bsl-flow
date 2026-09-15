package parityharness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"bsl-flow/cli/internal/memoryhost"
	"bsl-flow/cli/internal/repository"
	"bsl-flow/cli/internal/runner"
	"bsl-flow/cli/internal/specvalidate"
)

// Operation is one entry of the shadow allow-list. Every operation computes
// read/decision output only; the list is closed and rejects everything else
// (requirement 21: no writes, no model/API calls, no test execution).
type Operation string

const (
	// OpSpecLint lints frozen spec.md bytes through the native port of
	// Test-1CSpec.ps1.
	OpSpecLint Operation = "spec_lint"
	// OpSpecFinal validates a frozen OpenSpec change directory through the
	// native port of Test-1CSpecFinal.ps1.
	OpSpecFinal Operation = "spec_final"
	// OpRunnerDecide computes supervision decisions over frozen scripted
	// task snapshots, the native port of Get-BFRunnerDecision.
	OpRunnerDecide Operation = "runner_decide"
	// OpMemoryProjection computes the read-only memory capsule projection
	// (Get-BFMemoryProjection: "Never writes") over a frozen event bundle.
	OpMemoryProjection Operation = "memory_projection"
)

// shadowOperations is the explicit allow-list; Shadow refuses any other
// operation even if the request is otherwise well-formed.
var shadowOperations = map[Operation]bool{
	OpSpecLint:         true,
	OpSpecFinal:        true,
	OpRunnerDecide:     true,
	OpMemoryProjection: true,
}

// NamedBytes is one named input blob of a shadow request.
type NamedBytes struct {
	Name string
	Data []byte
}

// MemoryShadowInput carries the frozen inputs of the memory projection.
// The request document must select the projection operation; package and
// event files are staged into a scratch directory because both engines read
// them from their on-disk locations.
type MemoryShadowInput struct {
	// Request is the canonical native-memory request document
	// (memory_root must name <project>/.bsl-flow/memory of the staged
	// fixture).
	Request []byte
	// PackageFiles are staged under the package root consumed for
	// controller/schema fingerprints (VERSION, package-manifest.json,
	// schemas/*).
	PackageFiles []NamedBytes
	// EventFiles are staged under the memory root of the staged project.
	EventFiles []NamedBytes
	// Scratch optionally names the staging directory. When set, the caller
	// owns it (the live differential points both engines at one directory);
	// when empty Shadow creates a private temp directory and removes it.
	Scratch string
	// BindStagedProject rewrites state.project_path and memory_root of the
	// request to the staged project before execution. Frozen requests carry a
	// symbolic path there (capture machines differ), exactly like the
	// runner-decide capture patches project_path and policy_hash to its
	// sandbox. The recorded input set still hashes the Request bytes as
	// given, so the frozen trace stays comparable across machines; the
	// rebinding is recorded in the trace provenance by the caller.
	BindStagedProject bool
}

// ShadowRequest is one shadow-mode run: a frozen input set plus the
// read-only operation to compute over it.
type ShadowRequest struct {
	TraceID    string
	Operation  Operation
	Engine     Engine
	CapturedAt string
	Provenance string

	// Spec supplies the spec.md bytes for OpSpecLint.
	Spec []byte
	// ChangeDir names the change directory inside PS-shaped messages for
	// OpSpecFinal; Files supplies the change directory bytes.
	ChangeDir string
	Files     []NamedBytes
	// Snapshots supplies the frozen runner snapshot documents (canonical
	// JSON, one per case) for OpRunnerDecide.
	Snapshots []NamedBytes
	// Memory carries the frozen memory inputs for OpMemoryProjection.
	Memory *MemoryShadowInput
}

// Shadow executes exactly the requested read/decision computation and
// returns its result as a native trace document. It performs no writes
// outside its private staging directory, spawns no processes, and contacts
// no model, API or network service; a modifying action is never executed,
// so parity comparisons after one use frozen receipts instead (req 21).
func Shadow(request ShadowRequest) (*TraceDocument, error) {
	if !shadowOperations[request.Operation] {
		return nil, fmt.Errorf("parityharness: operation %q is outside the shadow allow-list", request.Operation)
	}
	if strings.TrimSpace(request.TraceID) == "" {
		return nil, errors.New("parityharness: shadow request has no trace id")
	}
	if request.Engine.Kind == "" {
		request.Engine = Engine{Kind: EngineNativeGo, Identity: "bsl-flow native shadow"}
	}
	trace := &TraceDocument{
		SchemaVersion: TraceSchemaVersion,
		TraceID:       request.TraceID,
		Operation:     string(request.Operation),
		Engine:        request.Engine,
		CapturedAt:    request.CapturedAt,
		Provenance:    request.Provenance,
	}
	var err error
	switch request.Operation {
	case OpSpecLint:
		err = shadowSpecLint(trace, request.Spec)
	case OpSpecFinal:
		err = shadowSpecFinal(trace, request.ChangeDir, request.Files)
	case OpRunnerDecide:
		err = shadowRunnerDecide(trace, request.Snapshots)
	case OpMemoryProjection:
		err = shadowMemoryProjection(trace, request.Memory)
	}
	if err != nil {
		return nil, err
	}
	if err := trace.SetInputSet(); err != nil {
		return nil, err
	}
	return trace, nil
}

func shadowSpecLint(trace *TraceDocument, spec []byte) error {
	if spec == nil {
		return errors.New("parityharness: spec_lint requires spec bytes")
	}
	findings, err := specvalidate.LintSpec(spec)
	if err != nil {
		return fmt.Errorf("parityharness: native spec lint: %w", err)
	}
	trace.Inputs = []InputFile{{Name: "spec.md", SHA256: repository.StageHostFileSHA256(spec)}}
	trace.Observations = []Observation{{
		Name:     "spec_lint",
		Document: lintArtifactDocument(spec, findings),
	}}
	return nil
}

// lintArtifactDocument reproduces the deterministic fields of the
// Test-1CSpec.ps1 spec-lint.json artifact (Test-1CSpec.ps1:117-124): the
// error strings carry the "spec.md line N: " prefix of Add-SpecError
// (Test-1CSpec.ps1:25-27), warnings are the plain messages, and stats count
// .NET UTF-16 code units and the "`r?`n" split elements over the
// BOM-stripped text. checked_at_utc is capture clock state, not a function
// of the trusted inputs, so it stays out of the observation on both engines.
// The shaping mirrors cli/speccmd.go newSpecLintArtifact, which cannot be
// imported from package main; the frozen traces validate both copies.
func lintArtifactDocument(spec []byte, findings []specvalidate.Finding) map[string]any {
	text := strings.TrimPrefix(string(spec), "\uFEFF")
	errorsList := []any{}
	warnings := []any{}
	for _, finding := range findings {
		if finding.Severity == "error" {
			errorsList = append(errorsList, fmt.Sprintf("spec.md line %d: %s", finding.Line, finding.Message))
			continue
		}
		warnings = append(warnings, finding.Message)
	}
	return map[string]any{
		"schema_version": 1,
		"passed":         len(errorsList) == 0,
		"errors":         errorsList,
		"warnings":       warnings,
		"stats": map[string]any{
			"characters": utf16CodeUnits(text),
			"lines":      splitLineCount(text),
		},
	}
}

// utf16CodeUnits counts UTF-16 code units the way .NET string.Length (the
// artifact's stats.characters) does.
func utf16CodeUnits(text string) int {
	units := 0
	for _, r := range text {
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return units
}

// splitLineCount mirrors @($text -split "`r?`n").Count: every newline splits,
// a lone carriage return does not, and empty text is one line.
func splitLineCount(text string) int {
	return 1 + strings.Count(text, "\n")
}

func shadowSpecFinal(trace *TraceDocument, changeDir string, files []NamedBytes) error {
	if len(files) == 0 {
		return errors.New("parityharness: spec_final requires change directory files")
	}
	if strings.TrimSpace(changeDir) == "" {
		return errors.New("parityharness: spec_final requires the change directory name used in PS-shaped messages")
	}
	byName := map[string][]byte{}
	inputs := make([]InputFile, 0, len(files))
	for _, file := range files {
		if _, exists := byName[file.Name]; exists {
			return fmt.Errorf("parityharness: duplicate change file %s", file.Name)
		}
		byName[file.Name] = file.Data
		inputs = append(inputs, InputFile{Name: file.Name, SHA256: repository.StageHostFileSHA256(file.Data)})
	}
	reader := func(rel string) ([]byte, error) {
		if data, ok := byName[rel]; ok {
			return data, nil
		}
		return nil, fs.ErrNotExist
	}
	// The frozen parity scope is the legacy v1 review schema. Council v2
	// final validation depends on checks the native port deliberately does
	// not approximate (specvalidate: ConvertTo-Json digest binding, policy
	// files outside the change directory, banker's-rounding recompute), so a
	// v2 fixture is refused instead of compared through a partial port.
	if schemaVersion, ok := reviewSchemaVersion(reader); ok && schemaVersion == 2 {
		return errors.New("parityharness: spec_final shadow accepts only legacy v1 review fixtures; council v2 final validation is outside the frozen parity scope")
	}
	checks, err := specvalidate.ValidateFinal(changeDir, reader)
	if err != nil {
		return fmt.Errorf("parityharness: native final validation: %w", err)
	}
	trace.Inputs = inputs
	trace.Observations = []Observation{
		{
			Name:     "spec_final",
			Document: finalValidationDocument(reader, checks),
		},
		{
			Name:     "spec_final.checks",
			Document: nativeChecksDocument(checks),
		},
	}
	return nil
}

// finalValidationDocument reproduces the deterministic fields of the v1
// final-validation.json sidecar of Test-1CSpecFinal.ps1:117-127 — passed
// over the accumulated error list, review_iteration peeked from review.json
// (nil when no readable review), the five nullable input hashes and every
// failing check's message in check order. checked_at_utc is capture clock
// state and stays out of the observation on both engines. The shaping
// mirrors cli/speccmd.go newFinalValidationSidecar (v1 branch), which cannot
// be imported from package main; the frozen traces validate both copies.
func finalValidationDocument(read func(string) ([]byte, error), checks []specvalidate.FinalCheck) map[string]any {
	errorsList := make([]any, 0, len(checks))
	for _, check := range checks {
		if !check.Pass {
			errorsList = append(errorsList, check.Detail)
		}
	}
	fileHash := func(rel string) any {
		data, err := read(rel)
		if err != nil {
			return nil
		}
		return repository.StageHostFileSHA256(data)
	}
	var iteration any
	if review, err := repository.DecodeObject(mustReadFile(read, "review.json")); err == nil {
		if number, ok := review["review_iteration"].(json.Number); ok {
			if value, err := number.Int64(); err == nil {
				iteration = int(value)
			}
		}
	}
	return map[string]any{
		"schema_version":   1,
		"passed":           len(errorsList) == 0,
		"review_iteration": iteration,
		"errors":           errorsList,
		"inputs": map[string]any{
			"review_sha256":         fileHash("review.json"),
			"reconciliation_sha256": fileHash("review-reconciliation.json"),
			"final_spec_sha256":     fileHash("spec.md"),
			"final_design_sha256":   fileHash("design.md"),
			"original_task_sha256":  fileHash("original-task.md"),
		},
	}
}

// nativeChecksDocument is the additive native observation of spec_final:
// the native validator reports named checks, while Test-1CSpecFinal.ps1
// reports only the accumulated error list. Frozen traces annotate this
// observation with an approved schema-change classification; it must not
// exist silently.
func nativeChecksDocument(checks []specvalidate.FinalCheck) map[string]any {
	checkDocuments := make([]any, 0, len(checks))
	for _, check := range checks {
		checkDocuments = append(checkDocuments, map[string]any{
			"name":   check.Name,
			"pass":   check.Pass,
			"detail": check.Detail,
		})
	}
	return map[string]any{"checks": checkDocuments}
}

// reviewSchemaVersion peeks review.json the way specvalidate picks the
// council branch: schema_version exactly 2 (numeric or the string "2").
// The second return is false when review.json is unreadable or unparseable,
// which ValidateFinal reports through its own checks.
func reviewSchemaVersion(read func(string) ([]byte, error)) (int, bool) {
	data, err := read("review.json")
	if err != nil {
		return 0, false
	}
	review, err := repository.DecodeObject(data)
	if err != nil {
		return 0, false
	}
	switch schema := review["schema_version"].(type) {
	case json.Number:
		if value, err := schema.Int64(); err == nil {
			return int(value), true
		}
	case string:
		if value, err := strconv.Atoi(schema); err == nil {
			return value, true
		}
	}
	return 0, false
}

func mustReadFile(read func(string) ([]byte, error), rel string) []byte {
	data, err := read(rel)
	if err != nil {
		return nil
	}
	return data
}

// scriptedLiveness answers from the snapshot script: the frozen input
// records which stored identities are live, so no process table is probed
// and the decision is reproducible offline.
type scriptedLiveness struct {
	live map[string]bool
}

func (l scriptedLiveness) Alive(identity runner.ProcessIdentity) bool {
	return l.live[processIdentityKey(identity)]
}

func processIdentityKey(identity runner.ProcessIdentity) string {
	return fmt.Sprintf("%d@%s", identity.PID, identity.StartTimeUTC)
}

func shadowRunnerDecide(trace *TraceDocument, snapshots []NamedBytes) error {
	if len(snapshots) == 0 {
		return errors.New("parityharness: runner_decide requires snapshot documents")
	}
	cases := make([]any, 0, len(snapshots))
	inputs := make([]InputFile, 0, len(snapshots))
	for _, named := range snapshots {
		snapshot, err := decodeSnapshot(named.Data)
		if err != nil {
			return fmt.Errorf("parityharness: snapshot %s: %w", named.Name, err)
		}
		alive := map[string]bool{}
		if live, ok := snapshot.document["controller_alive"].(bool); ok && live && snapshot.controller != nil {
			alive[processIdentityKey(*snapshot.controller)] = true
		}
		action := runner.Decide(snapshot.task, scriptedLiveness{live: alive})
		cases = append(cases, map[string]any{
			"name":     named.Name,
			"snapshot": snapshot.document,
			"action":   string(action),
		})
		inputs = append(inputs, InputFile{Name: named.Name, SHA256: repository.StageHostFileSHA256(named.Data)})
	}
	trace.Inputs = inputs
	trace.Observations = []Observation{{
		Name: "runner_decide",
		// The document mirrors the capture runner-decide.ps1 output shape so
		// the frozen legacy observation and the native observation are the
		// same view: one case per frozen snapshot with the echoed snapshot
		// and the engine's decision.
		Document: map[string]any{"schema_version": 1, "cases": cases},
	}}
	return nil
}

// runnerSnapshotDocument is the frozen input form of one scripted
// supervision snapshot: the runner.TaskSnapshot fields plus the scripted
// liveness answer for the stored controller identity.
type runnerSnapshotDocument struct {
	document   map[string]any
	task       runner.TaskSnapshot
	controller *runner.ProcessIdentity
}

func decodeSnapshot(data []byte) (*runnerSnapshotDocument, error) {
	object, err := repository.DecodeObject(data)
	if err != nil {
		return nil, err
	}
	stringOf := func(field string) string {
		text, _ := object[field].(string)
		return text
	}
	snapshot := &runnerSnapshotDocument{document: object}
	status := stringOf("status")
	if status == "" {
		return nil, errors.New("snapshot has no status")
	}
	// The frozen snapshot stores the normalized booleans of the capture
	// script (unresolved_effect is false, not null), so presence is not
	// truth here: only an explicit JSON true marks the effect.
	unresolved, _ := object["unresolved_effect"].(bool)
	snapshot.task = runner.TaskSnapshot{
		Status:           status,
		ActiveAttempt:    stringOf("active_attempt"),
		NextAction:       stringOf("next_action"),
		AttemptStage:     stringOf("attempt_stage"),
		UnresolvedEffect: unresolved,
	}
	if revision, err := intOf(object["revision"]); err == nil {
		snapshot.task.Revision = revision
	}
	if value, ok := object["acceptance_stale"].(bool); ok {
		snapshot.task.AcceptanceStale = value
	}
	if owner, ok := object["controller"].(map[string]any); ok {
		pid, err := intOf(owner["pid"])
		if err != nil {
			return nil, fmt.Errorf("snapshot controller pid: %w", err)
		}
		start, _ := owner["start_time_utc"].(string)
		snapshot.controller = &runner.ProcessIdentity{PID: int64(pid), StartTimeUTC: start}
		snapshot.task.Controller = snapshot.controller
	}
	if rawProcesses, ok := object["owned_processes"].([]any); ok {
		for _, raw := range rawProcesses {
			process, ok := raw.(map[string]any)
			if !ok {
				return nil, errors.New("snapshot owned_processes entries must be objects")
			}
			pid, err := intOf(process["pid"])
			if err != nil {
				return nil, fmt.Errorf("snapshot owned process pid: %w", err)
			}
			start, _ := process["start_time_utc"].(string)
			snapshot.task.OwnedProcesses = append(snapshot.task.OwnedProcesses, runner.ProcessIdentity{PID: int64(pid), StartTimeUTC: start})
		}
	}
	return snapshot, nil
}

func shadowMemoryProjection(trace *TraceDocument, input *MemoryShadowInput) error {
	if input == nil {
		return errors.New("parityharness: memory_projection requires memory inputs")
	}
	if len(input.Request) == 0 {
		return errors.New("parityharness: memory_projection requires a request document")
	}
	request, err := repository.DecodeObject(input.Request)
	if err != nil {
		return fmt.Errorf("parityharness: memory request decode: %w", err)
	}
	// The projection operation is read-only in both engines; refuse every
	// other memory operation before anything is staged.
	if operation, _ := request["operation"].(string); operation != "projection" {
		return fmt.Errorf("parityharness: memory_projection shadow accepts only the projection operation, got %q", operation)
	}
	state, _ := request["state"].(map[string]any)
	if state == nil {
		return errors.New("parityharness: memory request has no state object")
	}
	projectPath, _ := state["project_path"].(string)
	if strings.TrimSpace(projectPath) == "" {
		return errors.New("parityharness: memory request state has no project_path")
	}
	scratch := input.Scratch
	if scratch == "" {
		temporary, err := os.MkdirTemp("", "parity-shadow-")
		if err != nil {
			return fmt.Errorf("parityharness: staging: %w", err)
		}
		defer os.RemoveAll(temporary)
		scratch = temporary
	}
	packageRoot := filepath.Join(scratch, "pkg")
	projectRoot := filepath.Join(scratch, "project")
	for _, file := range input.PackageFiles {
		if err := stageFile(packageRoot, file); err != nil {
			return err
		}
	}
	memoryRoot := filepath.Join(projectRoot, ".bsl-flow", "memory")
	for _, file := range input.EventFiles {
		if err := stageFile(memoryRoot, file); err != nil {
			return err
		}
	}
	runRequest := input.Request
	if input.BindStagedProject {
		// Rebind the symbolic frozen path to the staging root; the recorded
		// input set still hashes the frozen bytes.
		state["project_path"] = projectRoot
		request["memory_root"] = memoryRoot
		rebound, err := repository.StageHostCanonical(request)
		if err != nil {
			return fmt.Errorf("parityharness: memory request rebind: %w", err)
		}
		runRequest = rebound
	} else if filepath.Clean(projectPath) != filepath.Clean(projectRoot) {
		// The staged project must sit exactly at the request's project_path so
		// the sealed memory_root contract (<project>/.bsl-flow/memory) resolves.
		return fmt.Errorf("parityharness: memory request project_path %s does not match the staged fixture root %s", projectPath, projectRoot)
	}
	var stdout bytes.Buffer
	if code := memoryhost.Run(bytes.NewReader(runRequest), &stdout, packageRoot); code != 0 {
		return fmt.Errorf("parityharness: native memory host exited %d: %s", code, stdout.String())
	}
	envelope, err := repository.DecodeObject(stdout.Bytes())
	if err != nil {
		return fmt.Errorf("parityharness: native memory envelope decode: %w", err)
	}
	inputs := []InputFile{{Name: "request.json", SHA256: repository.StageHostFileSHA256(input.Request)}}
	for _, file := range input.PackageFiles {
		inputs = append(inputs, InputFile{Name: "package/" + file.Name, SHA256: repository.StageHostFileSHA256(file.Data)})
	}
	for _, file := range input.EventFiles {
		inputs = append(inputs, InputFile{Name: "memory/" + file.Name, SHA256: repository.StageHostFileSHA256(file.Data)})
	}
	trace.Inputs = inputs
	trace.Observations = []Observation{{Name: "memory_projection", Document: envelope}}
	return nil
}

// stageFile writes one fixture file into its staging root; the staged bytes
// are the frozen inputs and no engine writes them back (projection is
// read-only in both engines).
func stageFile(root string, file NamedBytes) error {
	clean := filepath.Clean(file.Name)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return fmt.Errorf("parityharness: staged file %s escapes its root", file.Name)
	}
	target := filepath.Join(root, clean)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("parityharness: staging %s: %w", file.Name, err)
	}
	if err := os.WriteFile(target, file.Data, 0o644); err != nil {
		return fmt.Errorf("parityharness: staging %s: %w", file.Name, err)
	}
	return nil
}
