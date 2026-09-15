package parityharness

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"bsl-flow/cli/internal/repository"
)

// The offline suite is the always-on differential gate: the committed frozen
// legacy traces are replayed against the native shadow with no pwsh and no
// network, every input file is re-hashed against its recorded digest, and
// every observation must classify as match or an explicitly declared,
// approved schema change. Any other outcome is a parity failure.

func parityTestdataDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the parityharness testdata directory")
	}
	return filepath.Join(filepath.Dir(thisFile), "testdata")
}

func fileSHA256Hex(data []byte) string {
	return repository.StageHostFileSHA256(data)
}

func canonicalBytes(value any) ([]byte, error) {
	return repository.StageHostCanonical(value)
}

func decodeTraceBytes(data []byte) (map[string]any, error) {
	return repository.DecodeObject(data)
}

type frozenTrace struct {
	trace *TraceDocument
	path  string
}

func loadFrozenTraces(t *testing.T) []frozenTrace {
	t.Helper()
	tracesDir := filepath.Join(parityTestdataDir(t), "traces")
	entries, err := os.ReadDir(tracesDir)
	if err != nil {
		t.Fatalf("frozen traces are missing: %v (run TestRefreshFrozenTraces with BSL_FLOW_PARITY_REFRESH=1 on a pwsh machine)", err)
	}
	var frozen []frozenTrace
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".trace.json") {
			continue
		}
		path := filepath.Join(tracesDir, entry.Name())
		trace, err := LoadTrace(parityRead(t, path))
		if err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		if trace.Engine.Kind != EngineLegacyPowerShell {
			t.Fatalf("%s: committed traces must be legacy captures, got engine kind %q", entry.Name(), trace.Engine.Kind)
		}
		frozen = append(frozen, frozenTrace{trace: trace, path: path})
	}
	if len(frozen) == 0 {
		t.Fatal("no frozen traces found under testdata/traces")
	}
	sort.Slice(frozen, func(i, j int) bool { return frozen[i].trace.TraceID < frozen[j].trace.TraceID })
	return frozen
}

// frozenInputBytes reads and re-hashes one recorded input file.
func frozenInputBytes(t *testing.T, frozen *TraceDocument, input InputFile) []byte {
	t.Helper()
	if input.Path == "" {
		t.Fatalf("trace %s input %s has no fixture path", frozen.TraceID, input.Name)
	}
	data := parityRead(t, filepath.Join(parityTestdataDir(t), filepath.FromSlash(input.Path)))
	if digest := fileSHA256Hex(data); digest != input.SHA256 {
		t.Fatalf("trace %s input %s drifted: recorded %s, file %s (%s)", frozen.TraceID, input.Name, input.SHA256, digest, input.Path)
	}
	return data
}

// shadowRequestForTrace rebuilds the shadow request for one frozen trace
// from its committed input files.
func shadowRequestForTrace(t *testing.T, frozen *TraceDocument) (ShadowRequest, error) {
	t.Helper()
	request := ShadowRequest{
		TraceID:    frozen.TraceID,
		Operation:  Operation(frozen.Operation),
		Engine:     Engine{Kind: EngineNativeGo, Identity: "bsl-flow native shadow"},
		CapturedAt: frozen.CapturedAt,
		Provenance: "native shadow replay over the frozen inputs of trace " + frozen.TraceID,
	}
	files := map[string][]byte{}
	for _, input := range frozen.Inputs {
		files[input.Name] = frozenInputBytes(t, frozen, input)
	}
	switch request.Operation {
	case OpSpecLint:
		spec, ok := files["spec.md"]
		if !ok {
			return request, fmt.Errorf("trace %s has no spec.md input", frozen.TraceID)
		}
		request.Spec = spec
	case OpSpecFinal:
		changeDir := ""
		for _, input := range frozen.Inputs {
			if input.Path != "" {
				changeDir = filepath.Base(filepath.Dir(filepath.FromSlash(input.Path)))
				break
			}
		}
		request.ChangeDir = changeDir
		for _, input := range frozen.Inputs {
			request.Files = append(request.Files, NamedBytes{Name: input.Name, Data: files[input.Name]})
		}
	case OpRunnerDecide:
		for _, input := range frozen.Inputs {
			request.Snapshots = append(request.Snapshots, NamedBytes{Name: input.Name, Data: files[input.Name]})
		}
	case OpMemoryProjection:
		memory := &MemoryShadowInput{BindStagedProject: true}
		for _, input := range frozen.Inputs {
			switch {
			case input.Name == "request.json":
				memory.Request = files[input.Name]
			case strings.HasPrefix(input.Name, "package/"):
				memory.PackageFiles = append(memory.PackageFiles, NamedBytes{Name: strings.TrimPrefix(input.Name, "package/"), Data: files[input.Name]})
			case strings.HasPrefix(input.Name, "memory/"):
				memory.EventFiles = append(memory.EventFiles, NamedBytes{Name: strings.TrimPrefix(input.Name, "memory/"), Data: files[input.Name]})
			default:
				return request, fmt.Errorf("trace %s has unknown memory input %s", frozen.TraceID, input.Name)
			}
		}
		request.Memory = memory
	default:
		return request, fmt.Errorf("trace %s has unknown operation %q", frozen.TraceID, frozen.Operation)
	}
	return request, nil
}

// assertExpectedStatuses requires match everywhere except observations the
// frozen trace explicitly annotates, which must classify as the annotated
// schema change. An approved behavior-change annotation may exist only in a
// trace that names it here.
func assertExpectedStatuses(t *testing.T, frozen *TraceDocument, comparison Comparison) {
	t.Helper()
	justified := map[string]bool{}
	for _, result := range comparison.Observations {
		annotations := frozen.Classification(result.Observation)
		switch {
		case len(annotations) == 0:
			if result.Status != StatusMatch {
				t.Fatalf("observation %s classified %s (%s), want match with no annotation declared", result.Observation, result.Status, result.Detail)
			}
		default:
			if result.Status != StatusSchemaChange {
				t.Fatalf("annotated observation %s classified %s (%s), want schema-change", result.Observation, result.Status, result.Detail)
			}
			justified[result.Observation] = true
		}
	}
	for _, classification := range frozen.Classifications {
		if !justified[classification.Observation] {
			t.Fatalf("classification of %s accounts for no observation in the comparison", classification.Observation)
		}
	}
}

func TestShadowMatchesFrozenTraces(t *testing.T) {
	for _, frozen := range loadFrozenTraces(t) {
		t.Run(frozen.trace.TraceID, func(t *testing.T) {
			request, err := shadowRequestForTrace(t, frozen.trace)
			if err != nil {
				t.Fatal(err)
			}
			native, err := Shadow(request)
			if err != nil {
				t.Fatalf("native shadow: %v", err)
			}
			comparison := Compare(frozen.trace, native)
			if comparison.Failed() {
				t.Fatalf("native shadow diverged: %s %s", comparison.Detail, observationDetail(comparison))
			}
			assertExpectedStatuses(t, frozen.trace, comparison)
		})
	}
}

func TestShadowIsDeterministicOverFrozenInputs(t *testing.T) {
	for _, frozen := range loadFrozenTraces(t) {
		request, err := shadowRequestForTrace(t, frozen.trace)
		if err != nil {
			t.Fatal(err)
		}
		first, err := Shadow(request)
		if err != nil {
			t.Fatal(err)
		}
		second, err := Shadow(request)
		if err != nil {
			t.Fatal(err)
		}
		firstBytes, err := SaveTrace(first)
		if err != nil {
			t.Fatal(err)
		}
		secondBytes, err := SaveTrace(second)
		if err != nil {
			t.Fatal(err)
		}
		if string(firstBytes) != string(secondBytes) {
			t.Fatalf("trace %s: shadow output is not deterministic", frozen.trace.TraceID)
		}
	}
}

func TestShadowAllowListIsClosed(t *testing.T) {
	if _, err := Shadow(ShadowRequest{TraceID: "x", Operation: "dispatch"}); err == nil {
		t.Fatal("an operation outside the read/decision allow-list must be refused")
	}
	if _, err := Shadow(ShadowRequest{TraceID: "x", Operation: OpSpecLint}); err == nil {
		t.Fatal("spec_lint without spec bytes must fail")
	}
	if _, err := Shadow(ShadowRequest{Operation: OpSpecLint, Spec: []byte("# x")}); err == nil {
		t.Fatal("a shadow request without a trace id must fail")
	}
}

func TestShadowRefusesMemoryWriteOperations(t *testing.T) {
	request := []byte(`{"schema_version":1,"operation":"bind","state":{"project_path":"x"},"memory_root":"x/.bsl-flow/memory"}`)
	_, err := Shadow(ShadowRequest{
		TraceID:   "memory-write-refusal",
		Operation: OpMemoryProjection,
		Memory:    &MemoryShadowInput{Request: request},
	})
	if err == nil {
		t.Fatal("memory_projection shadow must refuse the write-side bind operation")
	}
}

func TestShadowSpecFinalRefusesCouncilReviews(t *testing.T) {
	files := []NamedBytes{
		{Name: "review.json", Data: []byte(`{"schema_version":2,"verdict":"PASS","diversity":"full"}`)},
		{Name: "spec.md", Data: []byte("# x")},
		{Name: "original-task.md", Data: []byte("task")},
	}
	if _, err := Shadow(ShadowRequest{
		TraceID:   "council-refusal",
		Operation: OpSpecFinal,
		ChangeDir: "council-fixture",
		Files:     files,
	}); err == nil {
		t.Fatal("council v2 final validation is outside the frozen parity scope and must be refused, not approximated")
	}
}
