package parityharness

import (
	"bytes"
	"fmt"
	"sort"

	"bsl-flow/cli/internal/repository"
)

// ComparisonStatus is the classified outcome for one observation pair.
type ComparisonStatus string

const (
	// StatusMatch means the canonical documents are byte-identical and no
	// annotation was declared.
	StatusMatch ComparisonStatus = "match"
	// StatusSchemaChange means the documents diverge exactly at the
	// annotated, approved field paths (or the observation exists in one
	// engine only per an observation-level annotation) and are identical
	// everywhere else.
	StatusSchemaChange ComparisonStatus = "schema-change"
	// StatusBehaviorChange means the documents diverge outside the approved
	// annotations. A behavior-change classification on a real divergence is
	// surfaced for owner approval; an unclassified divergence fails the
	// compatibility gate.
	StatusBehaviorChange ComparisonStatus = "behavior-change"
)

// GateStatus is the overall verdict of one trace comparison.
type GateStatus string

const (
	// GateFailed is every condition that must fail the differential gate:
	// input mismatch, unclassified or unapproved divergence, stale or
	// blanket annotations.
	GateFailed GateStatus = "failed"
	// GateClassified means every difference was classified explicitly
	// (match, schema-change, or an approved behavior-change) and every
	// annotation was load-bearing.
	GateClassified GateStatus = "classified"
)

// FieldDiff is one field-level diagnostic of a divergence.
type FieldDiff struct {
	Path    string `json:"path"`
	Frozen  string `json:"frozen"`
	Live    string `json:"live"`
	Problem string `json:"problem,omitempty"`
}

// ObservationResult is the classified comparison of one observation.
// Approved marks a behavior-change that carries an approved annotation: the
// classification is explicit, but the caller must still assert the exact
// divergence before treating the trace as compatible.
type ObservationResult struct {
	Observation string           `json:"observation"`
	Status      ComparisonStatus `json:"status"`
	Approved    bool             `json:"approved,omitempty"`
	Detail      string           `json:"detail,omitempty"`
	Diffs       []FieldDiff      `json:"diffs,omitempty"`
}

// Comparison is the classified result of comparing a frozen trace with a
// live one. It is never a bare boolean: every observation carries a status
// and, on divergence, field-level diagnostics.
type Comparison struct {
	TraceID      string              `json:"trace_id"`
	Gate         GateStatus          `json:"gate"`
	Detail       string              `json:"detail,omitempty"`
	Observations []ObservationResult `json:"observations"`
}

// Failed reports whether the differential gate failed.
func (c Comparison) Failed() bool { return c.Gate == GateFailed }

// Compare classifies the live (native) trace against the frozen (legacy)
// trace of requirement 20.
//
// Precondition: both traces must bind the identical trusted input set;
// otherwise the result is a failed input-mismatch and no output comparison
// runs — outputs over different inputs are not parity evidence.
//
// Per observation:
//
//   - identical canonical bytes and no annotations → match;
//   - divergence with approved schema-change annotations: the annotated
//     field paths are stripped from both documents; the remainder must be
//     byte-identical and every annotation must be load-bearing (dropping
//     any single annotation must break equality) → schema-change;
//   - observation present in one engine only, with an approved
//     observation-level schema-change annotation → schema-change;
//   - any other divergence → behavior-change (gate failed unless the
//     divergence carries an approved behavior-change annotation, which the
//     caller must assert explicitly — the harness never approves silently);
//   - annotations that do not account for a real difference (stale or
//     blanket) fail the gate so classifications stay honest.
func Compare(frozen, live *TraceDocument) Comparison {
	comparison := Comparison{TraceID: frozen.TraceID, Gate: GateClassified}
	fail := func(format string, arguments ...any) Comparison {
		comparison.Gate = GateFailed
		comparison.Detail = fmt.Sprintf(format, arguments...)
		return comparison
	}
	if frozen.TraceID != live.TraceID {
		return fail("trace ids differ: frozen %q live %q", frozen.TraceID, live.TraceID)
	}
	if frozen.Operation != live.Operation {
		return fail("trace %s operations differ: frozen %q live %q", frozen.TraceID, frozen.Operation, live.Operation)
	}
	frozenInputs := inputMap(frozen.Inputs)
	liveInputs := inputMap(live.Inputs)
	for name, digest := range frozenInputs {
		liveDigest, ok := liveInputs[name]
		if !ok {
			return fail("trace %s input %s missing from the live trace", frozen.TraceID, name)
		}
		if liveDigest != digest {
			return fail("trace %s input %s differs: frozen %s live %s (identical trusted inputs are the comparison precondition)", frozen.TraceID, name, digest, liveDigest)
		}
	}
	for name := range liveInputs {
		if _, ok := frozenInputs[name]; !ok {
			return fail("trace %s live input %s is not part of the frozen input set", frozen.TraceID, name)
		}
	}

	names := map[string]bool{}
	for _, name := range frozen.ObservationNames() {
		names[name] = true
	}
	for _, name := range live.ObservationNames() {
		names[name] = true
	}
	// A cross-engine comparison demands load-bearing annotations: a
	// classification that names an observation neither trace carries accounts
	// for nothing and fails. A same-engine drift check (frozen and fresh runs
	// of one engine) ignores annotations about the other engine's schema, so
	// the same inert classification is tolerated there.
	crossEngine := frozen.Engine.Kind != live.Engine.Kind
	if crossEngine {
		for _, classification := range frozen.Classifications {
			if !names[classification.Observation] {
				return fail("trace %s classification names observation %s, which neither engine produced", frozen.TraceID, classification.Observation)
			}
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)

	for _, name := range ordered {
		result := compareObservation(frozen, live, name)
		comparison.Observations = append(comparison.Observations, result)
		if result.Status == StatusBehaviorChange && !result.Approved {
			comparison.Gate = GateFailed
		}
	}
	if comparison.Detail == "" && comparison.Gate == GateClassified {
		comparison.Detail = classifiedSummary(comparison.Observations)
	}
	return comparison
}

func compareObservation(frozen, live *TraceDocument, name string) ObservationResult {
	result := ObservationResult{Observation: name}
	annotations := frozen.Classification(name)
	frozenObservation, frozenPresent := frozen.Observation(name)
	liveObservation, livePresent := live.Observation(name)

	if frozenPresent && !livePresent {
		result.Status = StatusBehaviorChange
		result.Detail = "observation is absent from the live trace"
		return result
	}
	if !frozenPresent && livePresent {
		if annotation, ok := observationLevelAnnotation(annotations, KindSchemaChange); ok && annotation.Approved {
			result.Status = StatusSchemaChange
			result.Detail = fmt.Sprintf("additive native observation: %s", annotation.Reason)
			return result
		}
		result.Status = StatusBehaviorChange
		result.Detail = "observation exists only in the live trace without an approved schema-change annotation"
		return result
	}
	if !frozenPresent && !livePresent {
		result.Status = StatusBehaviorChange
		result.Detail = "annotation names an observation absent from both traces"
		return result
	}

	frozenBytes, err := repository.StageHostCanonical(frozenObservation.Document)
	if err != nil {
		result.Status = StatusBehaviorChange
		result.Detail = fmt.Sprintf("frozen document does not canonicalize: %v", err)
		return result
	}
	liveBytes, err := repository.StageHostCanonical(liveObservation.Document)
	if err != nil {
		result.Status = StatusBehaviorChange
		result.Detail = fmt.Sprintf("live document does not canonicalize: %v", err)
		return result
	}
	if bytes.Equal(frozenBytes, liveBytes) {
		if len(annotations) > 0 {
			result.Status = StatusBehaviorChange
			result.Detail = "documents are identical but divergent annotations are declared (stale classification)"
			return result
		}
		result.Status = StatusMatch
		return result
	}

	result.Diffs = documentDiffs(frozenObservation.Document, liveObservation.Document)

	fieldAnnotations := fieldLevelAnnotations(annotations, KindSchemaChange)
	for _, annotation := range fieldAnnotations {
		if !annotation.Approved {
			result.Status = StatusBehaviorChange
			result.Detail = fmt.Sprintf("schema-change annotation of observation %s is not approved", name)
			return result
		}
	}
	if len(fieldAnnotations) > 0 {
		if outcome, ok := compareWithSchemaAnnotations(frozenObservation.Document, liveObservation.Document, fieldAnnotations); ok {
			if outcome.staleAnnotation != nil {
				result.Status = StatusBehaviorChange
				result.Detail = fmt.Sprintf("schema-change annotation is not load-bearing (its fields account for no real divergence): %s", outcome.staleAnnotation.Reason)
				return result
			}
			result.Status = StatusSchemaChange
			result.Detail = schemaChangeDetail(fieldAnnotations)
			return result
		}
	}

	if annotation, ok := observationLevelAnnotation(annotations, KindBehaviorChange); ok {
		if !annotation.Approved {
			result.Status = StatusBehaviorChange
			result.Detail = "behavior-change annotation is not approved"
			return result
		}
		result.Status = StatusBehaviorChange
		result.Approved = true
		result.Detail = fmt.Sprintf("approved behavior change (owner assertion required): %s", annotation.Reason)
		return result
	}
	result.Status = StatusBehaviorChange
	result.Detail = "documents diverge outside every approved annotation"
	return result
}

// schemaComparisonOutcome reports whether approved schema-change
// annotations account for the whole divergence. staleAnnotation is non-nil
// when an annotated field removed nothing from either document.
type schemaComparisonOutcome struct {
	staleAnnotation *Classification
}

// compareWithSchemaAnnotations strips the annotated field paths from both
// documents and requires the remainders to be byte-identical. Every
// annotated path must remove at least one member from at least one
// document, and every annotation must be load-bearing: dropping it (keeping
// the others) must break the equality the full set achieves.
func compareWithSchemaAnnotations(frozen, live map[string]any, annotations []Classification) (schemaComparisonOutcome, bool) {
	var allPaths []string
	for _, annotation := range annotations {
		allPaths = append(allPaths, annotation.FieldPaths...)
	}
	strippedFrozen, frozenRemovals, err := stripFields(frozen, allPaths)
	if err != nil {
		return schemaComparisonOutcome{}, false
	}
	strippedLive, liveRemovals, err := stripFields(live, allPaths)
	if err != nil {
		return schemaComparisonOutcome{}, false
	}
	frozenBytes, err := repository.StageHostCanonical(strippedFrozen)
	if err != nil {
		return schemaComparisonOutcome{}, false
	}
	liveBytes, err := repository.StageHostCanonical(strippedLive)
	if err != nil {
		return schemaComparisonOutcome{}, false
	}
	if !bytes.Equal(frozenBytes, liveBytes) {
		return schemaComparisonOutcome{}, false
	}
	for index, annotation := range annotations {
		removed := 0
		for _, path := range annotation.FieldPaths {
			removed += frozenRemovals[path] + liveRemovals[path]
		}
		if removed == 0 {
			stale := annotation
			return schemaComparisonOutcome{staleAnnotation: &stale}, true
		}
		var without []string
		for other := range annotations {
			if other != index {
				without = append(without, annotations[other].FieldPaths...)
			}
		}
		if equalAfterStrip(frozen, live, without) {
			stale := annotation
			return schemaComparisonOutcome{staleAnnotation: &stale}, true
		}
	}
	return schemaComparisonOutcome{}, true
}

func equalAfterStrip(frozen, live map[string]any, paths []string) bool {
	strippedFrozen, _, err := stripFields(frozen, paths)
	if err != nil {
		return false
	}
	strippedLive, _, err := stripFields(live, paths)
	if err != nil {
		return false
	}
	frozenBytes, err := repository.StageHostCanonical(strippedFrozen)
	if err != nil {
		return false
	}
	liveBytes, err := repository.StageHostCanonical(strippedLive)
	if err != nil {
		return false
	}
	return bytes.Equal(frozenBytes, liveBytes)
}

func observationLevelAnnotation(annotations []Classification, kind ClassificationKind) (Classification, bool) {
	for _, annotation := range annotations {
		if annotation.Kind == kind && len(annotation.FieldPaths) == 0 {
			return annotation, true
		}
	}
	return Classification{}, false
}

func fieldLevelAnnotations(annotations []Classification, kind ClassificationKind) []Classification {
	var result []Classification
	for _, annotation := range annotations {
		if annotation.Kind == kind && len(annotation.FieldPaths) > 0 {
			result = append(result, annotation)
		}
	}
	return result
}

func schemaChangeDetail(annotations []Classification) string {
	reasons := make([]string, 0, len(annotations))
	for _, annotation := range annotations {
		reasons = append(reasons, annotation.Reason)
	}
	return fmt.Sprintf("divergence is exactly the approved schema fields: %s", joinSentences(reasons))
}

func joinSentences(parts []string) string {
	merged := ""
	for index, part := range parts {
		if index > 0 {
			merged += " | "
		}
		merged += part
	}
	return merged
}

func classifiedSummary(results []ObservationResult) string {
	counts := map[ComparisonStatus]int{}
	for _, result := range results {
		counts[result.Status]++
	}
	return fmt.Sprintf("%d observations: %d match, %d schema-change, %d behavior-change",
		len(results), counts[StatusMatch], counts[StatusSchemaChange], counts[StatusBehaviorChange])
}

func inputMap(inputs []InputFile) map[string]string {
	byName := make(map[string]string, len(inputs))
	for _, input := range inputs {
		byName[input.Name] = input.SHA256
	}
	return byName
}

// documentDiffs produces field-level diagnostics for the first divergences
// between two decoded documents: shared members with different values,
// members present on one side only, and array length mismatches. It is
// bounded so a fully divergent pair still yields a readable diagnostic.
func documentDiffs(frozen, live map[string]any) []FieldDiff {
	diffs := []FieldDiff{}
	seen := map[string]bool{}
	for _, key := range sortedKeys(frozen) {
		seen[key] = true
		liveValue, ok := live[key]
		if !ok {
			diffs = append(diffs, FieldDiff{Path: key, Frozen: formatValue(frozen[key]), Live: "<absent>"})
			continue
		}
		diffs = append(diffs, valueDiffs(key, frozen[key], liveValue)...)
	}
	for _, key := range sortedKeys(live) {
		if seen[key] {
			continue
		}
		diffs = append(diffs, FieldDiff{Path: key, Frozen: "<absent>", Live: formatValue(live[key])})
	}
	if len(diffs) > 12 {
		diffs = diffs[:12]
	}
	return diffs
}

func valueDiffs(path string, frozen, live any) []FieldDiff {
	frozenObject, frozenIsObject := frozen.(map[string]any)
	liveObject, liveIsObject := live.(map[string]any)
	if frozenIsObject && liveIsObject {
		return documentDiffs(frozenObject, liveObject)
	}
	frozenArray, frozenIsArray := frozen.([]any)
	liveArray, liveIsArray := live.([]any)
	if frozenIsArray && liveIsArray {
		diffs := []FieldDiff{}
		if len(frozenArray) != len(liveArray) {
			diffs = append(diffs, FieldDiff{Path: path + ".length", Frozen: fmt.Sprintf("%d", len(frozenArray)), Live: fmt.Sprintf("%d", len(liveArray))})
		}
		shared := len(frozenArray)
		if len(liveArray) < shared {
			shared = len(liveArray)
		}
		for index := 0; index < shared && len(diffs) < 12; index++ {
			diffs = append(diffs, valueDiffs(fmt.Sprintf("%s[%d]", path, index), frozenArray[index], liveArray[index])...)
		}
		return diffs
	}
	frozenBytes, frozenErr := repository.StageHostCanonical(frozen)
	liveBytes, liveErr := repository.StageHostCanonical(live)
	if frozenErr == nil && liveErr == nil && bytes.Equal(frozenBytes, liveBytes) {
		return nil
	}
	return []FieldDiff{{Path: path, Frozen: truncateDiff(formatValue(frozen)), Live: truncateDiff(formatValue(live))}}
}

func truncateDiff(text string) string {
	if len(text) > 200 {
		return text[:200] + "..."
	}
	return text
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
