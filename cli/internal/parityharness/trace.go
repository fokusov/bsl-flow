// Package parityharness is the consolidated differential parity mechanism of
// the native-cross-platform-cli migration (spec requirements 20 and 21).
//
// Requirement 20: every migrated slice proves, over identical trusted
// inputs, that the legacy PowerShell engine and the native Go engine produce
// comparable envelopes/journals/hashes/decisions, and that every divergence
// is classified explicitly as a schema change or a behavior change instead
// of being normalized away. The mechanism is a frozen-trace document plus a
// comparator that yields classified results, never silent booleans.
//
// Requirement 21: Shadow executes read/decision computation only — no
// writes to project state, no model/API/network calls, no test execution and
// no re-execution of side-effecting actions. Comparisons that follow a
// modifying action compare frozen receipts; the shadow surface below is
// restricted to operations whose computation is observable without dispatch.
//
// The scattered differential tests (stagehost/memoryhost parity, the
// repository route golden) remain the per-slice evidence; this package is
// the shared mechanism they and future slices consolidate onto.
package parityharness

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"bsl-flow/cli/internal/repository"
)

// TraceSchemaVersion is the frozen-trace document schema. It changes only
// with an explicit migration of every frozen trace in testdata.
const TraceSchemaVersion = 1

// Engine kinds recorded in a trace.
const (
	EngineLegacyPowerShell = "legacy-powershell"
	EngineNativeGo         = "native-go"
)

// Engine identifies the engine that produced a trace.
type Engine struct {
	Kind     string `json:"kind"`
	Identity string `json:"identity"`
}

// InputFile is one trusted input of a trace: a logical name, the SHA-256 of
// its bytes and the optional provenance path the bytes were captured from.
// Path is informational; the comparison binds names to hashes only.
type InputFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Path   string `json:"path,omitempty"`
}

// Observation is one named canonical output document of an engine run. The
// document is decoded strict JSON (json.Number preserved) and is stored in
// the canonical encoding of repository.Canonical.
type Observation struct {
	Name     string         `json:"name"`
	Document map[string]any `json:"document"`
}

// ClassificationKind distinguishes the two approved divergence classes of
// requirement 20. Anything else fails the compatibility gate.
type ClassificationKind string

const (
	// KindSchemaChange marks a divergence that is exactly an enumerated,
	// approved difference of the output schema (fields present in one
	// engine only, or renamed members), with every unannotated field still
	// byte-identical.
	KindSchemaChange ClassificationKind = "schema-change"
	// KindBehaviorChange marks a known, asserted divergence of computed
	// behavior. It is surfaced, never approved implicitly by the harness.
	KindBehaviorChange ClassificationKind = "behavior-change"
)

// Classification annotates one observation of a frozen trace. FieldPaths
// uses the harness field-path syntax (see fields.go); an empty FieldPaths
// annotates the observation as a whole (additive observation present in one
// engine only). An annotation must be load-bearing: the comparator fails a
// trace whose annotation does not account for a real difference.
type Classification struct {
	Observation string             `json:"observation"`
	FieldPaths  []string           `json:"field_paths,omitempty"`
	Kind        ClassificationKind `json:"kind"`
	Reason      string             `json:"reason"`
	Approved    bool               `json:"approved"`
}

// TraceDocument is the frozen-trace document: one engine's outputs over one
// frozen input set. Frozen legacy traces are committed under testdata;
// native traces are produced by Shadow at verification time.
type TraceDocument struct {
	SchemaVersion   int              `json:"schema_version"`
	TraceID         string           `json:"trace_id"`
	Operation       string           `json:"operation"`
	Engine          Engine           `json:"engine"`
	CapturedAt      string           `json:"captured_at"`
	Provenance      string           `json:"provenance"`
	Inputs          []InputFile      `json:"inputs"`
	InputSetSHA256  string           `json:"input_set_sha256"`
	Observations    []Observation    `json:"observations"`
	Classifications []Classification `json:"classifications"`
}

// Observation returns the named observation and whether it exists.
func (t *TraceDocument) Observation(name string) (Observation, bool) {
	for _, observation := range t.Observations {
		if observation.Name == name {
			return observation, true
		}
	}
	return Observation{}, false
}

// Classification returns the annotations declared for one observation.
func (t *TraceDocument) Classification(name string) []Classification {
	var result []Classification
	for _, classification := range t.Classifications {
		if classification.Observation == name {
			result = append(result, classification)
		}
	}
	return result
}

// InputSetHash hashes the ordered (name, sha256) pairs of the input set so
// the comparator can prove both engines ran over identical trusted inputs
// before any output comparison is meaningful.
func InputSetHash(inputs []InputFile) (string, error) {
	ordered := make([]any, 0, len(inputs))
	for _, input := range inputs {
		if strings.TrimSpace(input.Name) == "" {
			return "", errors.New("parityharness: input name must not be empty")
		}
		if !repository.StageHostIsSHA256(input.SHA256) {
			return "", fmt.Errorf("parityharness: input %s has no valid sha256", input.Name)
		}
		ordered = append(ordered, map[string]any{"name": input.Name, "sha256": input.SHA256})
	}
	return repository.StageHostHash(ordered)
}

// SetInputSet computes and records the input-set hash.
func (t *TraceDocument) SetInputSet() error {
	digest, err := InputSetHash(t.Inputs)
	if err != nil {
		return err
	}
	t.InputSetSHA256 = digest
	return nil
}

// Canonical renders the trace document in the repo canonical JSON encoding.
// The struct is marshalled through encoding/json first because the repo
// canonical encoder accepts decoded JSON values only; the canonical pass
// then sorts keys and preserves json.Number literals.
func (t *TraceDocument) Canonical() ([]byte, error) {
	data, err := json.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("parityharness: trace marshal: %w", err)
	}
	object, err := repository.DecodeObject(data)
	if err != nil {
		return nil, fmt.Errorf("parityharness: trace decode: %w", err)
	}
	return repository.StageHostCanonical(object)
}

// SaveTrace renders a trace document canonically for committing.
func SaveTrace(trace *TraceDocument) ([]byte, error) {
	if trace.SchemaVersion != TraceSchemaVersion {
		return nil, fmt.Errorf("parityharness: trace schema_version %d is not %d", trace.SchemaVersion, TraceSchemaVersion)
	}
	return trace.Canonical()
}

// LoadTrace decodes strict canonical JSON bytes into a trace document. It
// rejects unknown schema versions and duplicate observation/input names.
func LoadTrace(data []byte) (*TraceDocument, error) {
	object, err := repository.DecodeObject(data)
	if err != nil {
		return nil, fmt.Errorf("parityharness: trace decode: %w", err)
	}
	decoder := func(field string) (any, bool) { value, ok := object[field]; return value, ok }
	rawVersion, ok := decoder("schema_version")
	if !ok {
		return nil, errors.New("parityharness: trace has no schema_version")
	}
	version, err := intOf(rawVersion)
	if err != nil {
		return nil, fmt.Errorf("parityharness: trace schema_version: %w", err)
	}
	if version != TraceSchemaVersion {
		return nil, fmt.Errorf("parityharness: trace schema_version %d is not %d", version, TraceSchemaVersion)
	}
	stringOf := func(field string) (string, error) {
		value, ok := decoder(field)
		if !ok {
			return "", fmt.Errorf("parityharness: trace has no %s", field)
		}
		text, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("parityharness: trace %s is not a string", field)
		}
		return text, nil
	}
	trace := &TraceDocument{SchemaVersion: version}
	for _, field := range []string{"trace_id", "operation", "captured_at", "provenance", "input_set_sha256"} {
		text, err := stringOf(field)
		if err != nil {
			return nil, err
		}
		switch field {
		case "trace_id":
			trace.TraceID = text
		case "operation":
			trace.Operation = text
		case "captured_at":
			trace.CapturedAt = text
		case "provenance":
			trace.Provenance = text
		case "input_set_sha256":
			trace.InputSetSHA256 = text
		}
	}
	engineObject, ok := object["engine"].(map[string]any)
	if !ok {
		return nil, errors.New("parityharness: trace has no engine object")
	}
	kind, err := stringOfMap(engineObject, "kind")
	if err != nil {
		return nil, fmt.Errorf("parityharness: trace engine: %w", err)
	}
	identity, err := stringOfMap(engineObject, "identity")
	if err != nil {
		return nil, fmt.Errorf("parityharness: trace engine: %w", err)
	}
	trace.Engine = Engine{Kind: kind, Identity: identity}

	seenInputs := map[string]bool{}
	inputs, err := arrayField(object, "inputs")
	if err != nil {
		return nil, err
	}
	for _, raw := range inputs {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("parityharness: trace input is not an object")
		}
		name, err := stringOfMap(item, "name")
		if err != nil {
			return nil, fmt.Errorf("parityharness: trace input: %w", err)
		}
		digest, err := stringOfMap(item, "sha256")
		if err != nil {
			return nil, fmt.Errorf("parityharness: trace input %s: %w", name, err)
		}
		if seenInputs[name] {
			return nil, fmt.Errorf("parityharness: duplicate trace input %s", name)
		}
		seenInputs[name] = true
		input := InputFile{Name: name, SHA256: digest}
		if path, ok := item["path"].(string); ok {
			input.Path = path
		}
		trace.Inputs = append(trace.Inputs, input)
	}
	digest, err := InputSetHash(trace.Inputs)
	if err != nil {
		return nil, err
	}
	if digest != trace.InputSetSHA256 {
		return nil, errors.New("parityharness: trace input_set_sha256 does not match its inputs")
	}

	seenObservations := map[string]bool{}
	observations, err := arrayField(object, "observations")
	if err != nil {
		return nil, err
	}
	for _, raw := range observations {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("parityharness: trace observation is not an object")
		}
		name, err := stringOfMap(item, "name")
		if err != nil {
			return nil, fmt.Errorf("parityharness: trace observation: %w", err)
		}
		document, ok := item["document"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("parityharness: trace observation %s has no document object", name)
		}
		if seenObservations[name] {
			return nil, fmt.Errorf("parityharness: duplicate trace observation %s", name)
		}
		seenObservations[name] = true
		trace.Observations = append(trace.Observations, Observation{Name: name, Document: document})
	}

	classifications, err := arrayField(object, "classifications")
	if err != nil {
		return nil, err
	}
	for _, raw := range classifications {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("parityharness: trace classification is not an object")
		}
		observation, err := stringOfMap(item, "observation")
		if err != nil {
			return nil, fmt.Errorf("parityharness: trace classification: %w", err)
		}
		// A classification may name an observation this trace does not carry:
		// observation-level annotations mark additive observations that exist
		// only in the compared engine's trace. The comparator rejects an
		// annotation that accounts for no real divergence in either trace.
		kindText, err := stringOfMap(item, "kind")
		if err != nil {
			return nil, fmt.Errorf("parityharness: trace classification of %s: %w", observation, err)
		}
		kind := ClassificationKind(kindText)
		if kind != KindSchemaChange && kind != KindBehaviorChange {
			return nil, fmt.Errorf("parityharness: trace classification of %s has unknown kind %q", observation, kindText)
		}
		reason, err := stringOfMap(item, "reason")
		if err != nil {
			return nil, fmt.Errorf("parityharness: trace classification of %s: %w", observation, err)
		}
		approved, ok := item["approved"].(bool)
		if !ok {
			return nil, fmt.Errorf("parityharness: trace classification of %s has no approved flag", observation)
		}
		classification := Classification{Observation: observation, Kind: kind, Reason: reason, Approved: approved}
		if rawPaths, ok := item["field_paths"].([]any); ok {
			for _, rawPath := range rawPaths {
				path, ok := rawPath.(string)
				if !ok {
					return nil, fmt.Errorf("parityharness: trace classification of %s has a non-string field path", observation)
				}
				classification.FieldPaths = append(classification.FieldPaths, path)
			}
		}
		trace.Classifications = append(trace.Classifications, classification)
	}
	return trace, nil
}

// ObservationNames returns the observation names in trace order.
func (t *TraceDocument) ObservationNames() []string {
	names := make([]string, 0, len(t.Observations))
	for _, observation := range t.Observations {
		names = append(names, observation.Name)
	}
	return names
}

// SortedObservationNames returns the observation names sorted, for set
// comparisons across engines.
func (t *TraceDocument) SortedObservationNames() []string {
	names := t.ObservationNames()
	sort.Strings(names)
	return names
}

func intOf(value any) (int, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, errors.New("not a number")
	}
	parsed, err := number.Int64()
	if err != nil {
		return 0, err
	}
	return int(parsed), nil
}

func stringOfMap(object map[string]any, field string) (string, error) {
	value, ok := object[field]
	if !ok {
		return "", fmt.Errorf("no %s", field)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s is not a string", field)
	}
	return text, nil
}

func arrayField(object map[string]any, field string) ([]any, error) {
	value, ok := object[field]
	if !ok || value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s is not an array", field)
	}
	return items, nil
}
