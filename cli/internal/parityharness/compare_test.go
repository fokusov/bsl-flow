package parityharness

import (
	"strings"
	"testing"
)

// traceFixture builds a minimal frozen/live trace pair for comparator
// semantics: one input, one observation, no annotations.
func traceFixture(traceID, operation, digest string, document map[string]any) *TraceDocument {
	return &TraceDocument{
		SchemaVersion:  TraceSchemaVersion,
		TraceID:        traceID,
		Operation:      operation,
		Engine:         Engine{Kind: EngineNativeGo, Identity: "fixture"},
		InputSetSHA256: digest,
		Inputs:         []InputFile{{Name: "spec.md", SHA256: digest}},
		Observations:   []Observation{{Name: "artifact", Document: document}},
	}
}

const fixtureDigest = "1111111111111111111111111111111111111111111111111111111111111111"

func TestCompareMatchRequiresNoAnnotation(t *testing.T) {
	frozen := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"passed": true, "errors": []any{}})
	live := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"errors": []any{}, "passed": true})
	comparison := Compare(frozen, live)
	if comparison.Failed() {
		t.Fatalf("identical documents must classify as match: %s", comparison.Detail)
	}
	if comparison.Observations[0].Status != StatusMatch {
		t.Fatalf("status = %s, want match", comparison.Observations[0].Status)
	}
}

func TestCompareUnclassifiedDivergenceFailsGate(t *testing.T) {
	frozen := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"passed": true, "errors": []any{}})
	live := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"passed": false, "errors": []any{"boom"}})
	comparison := Compare(frozen, live)
	if !comparison.Failed() {
		t.Fatal("an unclassified divergence must fail the gate")
	}
	result := comparison.Observations[0]
	if result.Status != StatusBehaviorChange || result.Approved {
		t.Fatalf("status = %s approved = %v, want unapproved behavior-change", result.Status, result.Approved)
	}
	if len(result.Diffs) == 0 {
		t.Fatal("a divergent comparison must carry field diagnostics")
	}
	if !strings.Contains(result.Diffs[0].Path, "passed") && !strings.Contains(result.Diffs[0].Path, "errors") {
		t.Fatalf("diff path %q does not point at the divergence", result.Diffs[0].Path)
	}
}

func TestCompareApprovedSchemaChangeStripsAnnotatedFields(t *testing.T) {
	frozen := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{
		"passed": true, "errors": []any{}, "stats": map[string]any{"characters": 10},
	})
	live := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{
		"passed": true, "errors": []any{}, "stats": map[string]any{"characters": 11},
	})
	frozen.Classifications = []Classification{{
		Observation: "artifact",
		FieldPaths:  []string{"stats.characters"},
		Kind:        KindSchemaChange,
		Reason:      "native counts bytes where legacy counts code units",
		Approved:    true,
	}}
	comparison := Compare(frozen, live)
	if comparison.Failed() {
		t.Fatalf("an exactly annotated divergence is a schema change, not a gate failure: %s", comparison.Detail)
	}
	if comparison.Observations[0].Status != StatusSchemaChange {
		t.Fatalf("status = %s, want schema-change", comparison.Observations[0].Status)
	}
}

func TestCompareSchemaAnnotationMustBeLoadBearing(t *testing.T) {
	frozen := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"passed": true, "identical": 1})
	live := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"passed": true, "identical": 2})
	// The annotation names a field that is not part of the divergence, so it
	// masks nothing and must not turn the behavior change into a pass.
	frozen.Classifications = []Classification{{
		Observation: "artifact",
		FieldPaths:  []string{"passed"},
		Kind:        KindSchemaChange,
		Reason:      "stale",
		Approved:    true,
	}}
	comparison := Compare(frozen, live)
	if !comparison.Failed() {
		t.Fatal("an annotation that does not account for the divergence must fail the gate")
	}
	if comparison.Observations[0].Status != StatusBehaviorChange {
		t.Fatalf("status = %s, want behavior-change", comparison.Observations[0].Status)
	}
}

func TestCompareIdenticalDocumentsWithAnnotationFails(t *testing.T) {
	frozen := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"passed": true})
	live := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"passed": true})
	frozen.Classifications = []Classification{{
		Observation: "artifact",
		FieldPaths:  []string{"passed"},
		Kind:        KindSchemaChange,
		Reason:      "stale on identical documents",
		Approved:    true,
	}}
	comparison := Compare(frozen, live)
	if !comparison.Failed() {
		t.Fatal("a divergent annotation on identical documents is stale and must fail")
	}
}

func TestCompareUnapprovedSchemaChangeFails(t *testing.T) {
	frozen := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 1})
	live := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 2})
	frozen.Classifications = []Classification{{
		Observation: "artifact",
		FieldPaths:  []string{"a"},
		Kind:        KindSchemaChange,
		Reason:      "not yet approved",
		Approved:    false,
	}}
	comparison := Compare(frozen, live)
	if !comparison.Failed() {
		t.Fatal("an unapproved schema-change annotation must fail the gate")
	}
}

func TestCompareApprovedBehaviorChangeSurfacesApproval(t *testing.T) {
	frozen := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 1})
	live := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 2})
	frozen.Classifications = []Classification{{
		Observation: "artifact",
		Kind:        KindBehaviorChange,
		Reason:      "native treats invalid UTF-8 as an input error",
		Approved:    true,
	}}
	comparison := Compare(frozen, live)
	if comparison.Failed() {
		t.Fatalf("an approved behavior change keeps the gate classified: %s", comparison.Detail)
	}
	result := comparison.Observations[0]
	if result.Status != StatusBehaviorChange || !result.Approved {
		t.Fatalf("status = %s approved = %v, want approved behavior-change", result.Status, result.Approved)
	}
}

func TestCompareUnapprovedBehaviorChangeFails(t *testing.T) {
	frozen := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 1})
	live := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 2})
	frozen.Classifications = []Classification{{
		Observation: "artifact",
		Kind:        KindBehaviorChange,
		Reason:      "pending owner approval",
		Approved:    false,
	}}
	if comparison := Compare(frozen, live); !comparison.Failed() {
		t.Fatal("an unapproved behavior-change annotation must fail the gate")
	}
}

func TestCompareAdditiveObservationNeedsApprovedAnnotation(t *testing.T) {
	frozen := traceFixture("t1", "spec_final", fixtureDigest, map[string]any{"passed": true})
	frozen.Observations = []Observation{{Name: "spec_final", Document: map[string]any{"passed": true}}}
	live := traceFixture("t1", "spec_final", fixtureDigest, map[string]any{"passed": true})
	live.Observations = []Observation{
		{Name: "spec_final", Document: map[string]any{"passed": true}},
		{Name: "spec_final.checks", Document: map[string]any{"checks": []any{}}},
	}

	unannotated := Compare(frozen, live)
	if !unannotated.Failed() {
		t.Fatal("a native-only observation without an annotation must fail the gate")
	}

	frozen.Classifications = []Classification{{
		Observation: "spec_final.checks",
		Kind:        KindSchemaChange,
		Reason:      "the native validator reports named checks",
		Approved:    true,
	}}
	annotated := Compare(frozen, live)
	if annotated.Failed() {
		t.Fatalf("an approved additive observation is a schema change: %s", annotated.Detail)
	}
	statuses := map[string]ComparisonStatus{}
	for _, result := range annotated.Observations {
		statuses[result.Observation] = result.Status
	}
	if statuses["spec_final"] != StatusMatch || statuses["spec_final.checks"] != StatusSchemaChange {
		t.Fatalf("statuses = %v, want match and schema-change", statuses)
	}
}

func TestCompareMissingLiveObservationFails(t *testing.T) {
	frozen := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 1})
	live := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 1})
	live.Observations = nil
	comparison := Compare(frozen, live)
	if !comparison.Failed() {
		t.Fatal("a frozen observation missing from the live trace must fail the gate")
	}
}

func TestCompareAnnotationAbsentFromBothTracesFails(t *testing.T) {
	frozen := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 1})
	frozen.Engine = Engine{Kind: EngineLegacyPowerShell, Identity: "fixture"}
	live := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 1})
	frozen.Classifications = []Classification{{
		Observation: "ghost",
		Kind:        KindSchemaChange,
		Reason:      "names an observation neither trace carries",
		Approved:    true,
	}}
	comparison := Compare(frozen, live)
	if !comparison.Failed() {
		t.Fatal("a cross-engine annotation naming an observation absent from both traces must fail the gate")
	}

	// The same inert annotation is tolerated between two runs of one engine
	// (the drift check): it documents the other engine's schema.
	sameEngine := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 1})
	sameEngine.Engine = Engine{Kind: EngineLegacyPowerShell, Identity: "fixture"}
	sameEngine.Classifications = frozen.Classifications
	if drift := Compare(frozen, sameEngine); drift.Failed() {
		t.Fatalf("same-engine drift comparison must ignore other-engine annotations: %s", drift.Detail)
	}
}

func TestCompareInputMismatchRefusesOutputComparison(t *testing.T) {
	frozen := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 1})
	live := traceFixture("t1", "spec_lint", "2222222222222222222222222222222222222222222222222222222222222222", map[string]any{"a": 2})
	comparison := Compare(frozen, live)
	if !comparison.Failed() {
		t.Fatal("differing input sets must fail the gate before any output comparison")
	}
	if !strings.Contains(comparison.Detail, "identical trusted inputs") {
		t.Fatalf("detail %q must name the input-set precondition", comparison.Detail)
	}
	if len(comparison.Observations) != 0 {
		t.Fatal("no observation may be compared over differing inputs")
	}
}

func TestCompareRejectsMismatchedTraceIdentity(t *testing.T) {
	frozen := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 1})
	wrongID := traceFixture("t2", "spec_lint", fixtureDigest, map[string]any{"a": 1})
	if comparison := Compare(frozen, wrongID); !comparison.Failed() {
		t.Fatal("trace ids must bind the comparison")
	}
	wrongOperation := traceFixture("t1", "spec_final", fixtureDigest, map[string]any{"a": 1})
	if comparison := Compare(frozen, wrongOperation); !comparison.Failed() {
		t.Fatal("operations must bind the comparison")
	}
}

func TestCompareArrayWildcardAnnotation(t *testing.T) {
	frozen := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{
		"findings": []any{
			map[string]any{"line": 1, "message": "one", "rule": "a"},
			map[string]any{"line": 2, "message": "two", "rule": "b"},
		},
	})
	live := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{
		"findings": []any{
			map[string]any{"line": 1, "message": "one", "rule": "go"},
			map[string]any{"line": 2, "message": "two", "rule": "go"},
		},
	})
	frozen.Classifications = []Classification{{
		Observation: "artifact",
		FieldPaths:  []string{"findings[].rule"},
		Kind:        KindSchemaChange,
		Reason:      "rule ids are a native-side naming of the plain legacy messages",
		Approved:    true,
	}}
	comparison := Compare(frozen, live)
	if comparison.Failed() {
		t.Fatalf("an array-wildcard schema annotation must cover every element: %s", comparison.Detail)
	}
	if comparison.Observations[0].Status != StatusSchemaChange {
		t.Fatalf("status = %s, want schema-change", comparison.Observations[0].Status)
	}
}

func TestCompareClassifiedSummaryCounts(t *testing.T) {
	frozen := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 1})
	live := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 1})
	comparison := Compare(frozen, live)
	if !strings.Contains(comparison.Detail, "1 observations: 1 match, 0 schema-change, 0 behavior-change") {
		t.Fatalf("detail %q must summarize the classified counts", comparison.Detail)
	}
}

func TestInputSetHashBindsNameAndDigest(t *testing.T) {
	base := []InputFile{{Name: "spec.md", SHA256: fixtureDigest}}
	first, err := InputSetHash(base)
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := InputSetHash([]InputFile{{Name: "other.md", SHA256: fixtureDigest}})
	if err != nil {
		t.Fatal(err)
	}
	if first == renamed {
		t.Fatal("the input-set hash must bind input names")
	}
	reordered, err := InputSetHash([]InputFile{
		{Name: "a.md", SHA256: fixtureDigest},
		{Name: "spec.md", SHA256: fixtureDigest},
	})
	if err != nil {
		t.Fatal(err)
	}
	ordered, err := InputSetHash([]InputFile{
		{Name: "spec.md", SHA256: fixtureDigest},
		{Name: "a.md", SHA256: fixtureDigest},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reordered == ordered {
		t.Fatal("the input-set hash must depend on the declared input order")
	}
	if _, err := InputSetHash([]InputFile{{Name: "", SHA256: fixtureDigest}}); err == nil {
		t.Fatal("an empty input name must be rejected")
	}
	if _, err := InputSetHash([]InputFile{{Name: "spec.md", SHA256: "nothex"}}); err == nil {
		t.Fatal("a non-sha256 digest must be rejected")
	}
}

func TestTraceRoundTripThroughCanonicalBytes(t *testing.T) {
	trace := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"passed": true})
	trace.Engine = Engine{Kind: EngineLegacyPowerShell, Identity: "Test-1CSpec.ps1"}
	trace.Provenance = "fixture provenance"
	trace.CapturedAt = "2026-09-15T00:00:00.0000000Z"
	if err := trace.SetInputSet(); err != nil {
		t.Fatal(err)
	}
	if trace.InputSetSHA256 != fixtureDigestHash(t) {
		t.Fatalf("input set hash %s does not cover the declared inputs", trace.InputSetSHA256)
	}
	data, err := SaveTrace(trace)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadTrace(data)
	if err != nil {
		t.Fatalf("round trip load: %v", err)
	}
	if loaded.TraceID != trace.TraceID || loaded.Operation != trace.Operation {
		t.Fatal("round trip lost trace identity")
	}
	again, err := SaveTrace(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(again) {
		t.Fatal("canonical save must be stable across a load cycle")
	}
}

func fixtureDigestHash(t *testing.T) string {
	t.Helper()
	digest, err := InputSetHash([]InputFile{{Name: "spec.md", SHA256: fixtureDigest}})
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestLoadTraceRejectsCorruptDocuments(t *testing.T) {
	trace := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 1})
	if err := trace.SetInputSet(); err != nil {
		t.Fatal(err)
	}
	good, err := SaveTrace(trace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTrace(good); err != nil {
		t.Fatalf("the unmutated trace must round-trip: %v", err)
	}
	corrupt := func(mutate func(*TraceDocument) string) {
		t.Helper()
		mutated := traceFixture("t1", "spec_lint", fixtureDigest, map[string]any{"a": 1})
		mutated.Inputs = append([]InputFile(nil), trace.Inputs...)
		mutated.InputSetSHA256 = trace.InputSetSHA256
		reason := mutate(mutated)
		data, err := SaveTrace(mutated)
		if err != nil {
			t.Fatalf("%s: save mutated trace: %v", reason, err)
		}
		if _, err := LoadTrace(data); err == nil {
			t.Fatalf("%s: LoadTrace must reject the mutated document", reason)
		}
	}
	// SaveTrace refuses to write a foreign schema version, so that variant is
	// patched into the raw bytes.
	patched := strings.Replace(string(good), `"schema_version":1`, `"schema_version":2`, 1)
	if patched == string(good) {
		t.Fatal("patching the schema version found no target")
	}
	if _, err := LoadTrace([]byte(patched)); err == nil {
		t.Fatal("LoadTrace must reject an unknown schema version")
	}
	corrupt(func(doc *TraceDocument) string {
		doc.Inputs = append(doc.Inputs, InputFile{Name: "spec.md", SHA256: fixtureDigest})
		return "duplicate input name"
	})
	corrupt(func(doc *TraceDocument) string {
		doc.Inputs = []InputFile{{Name: "spec.md", SHA256: "2222222222222222222222222222222222222222222222222222222222222222"}}
		return "input set hash does not cover inputs"
	})
	corrupt(func(doc *TraceDocument) string {
		doc.Observations = append(doc.Observations, Observation{Name: "artifact", Document: map[string]any{}})
		return "duplicate observation"
	})
	corrupt(func(doc *TraceDocument) string {
		doc.Classifications = append(doc.Classifications, Classification{Observation: "artifact", Kind: "mystery", Reason: "x", Approved: true})
		return "unknown classification kind"
	})
}
