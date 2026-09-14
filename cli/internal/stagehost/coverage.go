package stagehost

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// This file ports the coverage validators the verify stage needs:
// Test-BFCoveragePathInScope, Assert-BFCoverageStringArray and
// Assert-BFCoverageReview from Task.Coverage.ps1, Get-BFProtectedTestManifest
// from Task.Stages.ps1 and Assert-BFVerificationCoverage from Task.Gates.ps1.

var coverageGeneratedPath = regexp.MustCompile(`(^|[\\/])\.(git|bsl-flow|bsl-flow-worker)([\\/]|$)`)

// coveragePathInScope mirrors Test-BFCoveragePathInScope.
func coveragePathInScope(path string, scopes []string) (bool, error) {
	if err := assertRelativePath(path); err != nil {
		return false, err
	}
	if coverageGeneratedPath.MatchString(path) {
		return false, nil
	}
	candidate := strings.TrimRight(strings.ReplaceAll(path, `\`, `/`), "/")
	for _, scopeValue := range scopes {
		scope := strings.TrimRight(strings.ReplaceAll(scopeValue, `\`, `/`), "/")
		if scope == "." || candidate == scope || strings.HasPrefix(candidate, scope+"/") {
			return true, nil
		}
	}
	return false, nil
}

// assertCoverageStringArray mirrors Assert-BFCoverageStringArray.
func assertCoverageStringArray(value any, name string) error {
	items, ok := asArray(value)
	if !ok {
		return invalidf("%s must be an array.", name)
	}
	seen := map[string]bool{}
	for _, item := range items {
		text, isText := item.(string)
		if !isText || strings.TrimSpace(text) == "" || seen[text] {
			return invalidf("%s must contain unique nonempty strings.", name)
		}
		seen[text] = true
	}
	return nil
}

// coverageInsensitiveLess orders strings the way PowerShell's Sort-Object
// orders them for the coverage joins: case-insensitive with a stable order
// for exact ties.
func coverageInsensitiveLess(left, right string) bool {
	return strings.ToLower(left) < strings.ToLower(right)
}

// sortedJoin joins values the way the legacy provider compared id sets:
// case-insensitively sorted, newline-joined.
func sortedJoin(values []string) string {
	sorted := append([]string(nil), values...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return coverageInsensitiveLess(sorted[i], sorted[j])
	})
	return strings.Join(sorted, "\n")
}

func findCriterion(criteria []any, id string) map[string]any {
	for _, raw := range criteria {
		criterion, _ := asObject(raw)
		if criterion["id"] == id {
			return criterion
		}
	}
	return nil
}

func coverageBindingLess(left, right map[string]any) bool {
	if left["requirement_id"] != right["requirement_id"] {
		return coverageInsensitiveLess(asStringOr(left["requirement_id"]), asStringOr(right["requirement_id"]))
	}
	if left["criterion_id"] != right["criterion_id"] {
		return coverageInsensitiveLess(asStringOr(left["criterion_id"]), asStringOr(right["criterion_id"]))
	}
	return coverageInsensitiveLess(asStringOr(left["path"]), asStringOr(right["path"]))
}

// assertCoverageReview mirrors Assert-BFCoverageReview and returns the
// requirement coverage binding it registers under the review directory.
func assertCoverageReview(state map[string]any, coverage any, directory string) (map[string]any, error) {
	request, _ := asObject(state["request"])
	if !hasProperty(request, "requirements") {
		if coverage != nil {
			return nil, invalidf("coverage_review requires trusted requirements.")
		}
		return nil, nil
	}
	requirements, ok := asArray(request["requirements"])
	if !ok {
		return nil, invalidf("requirements must be a nonempty array when supplied.")
	}
	if err := assertRequirements(request); err != nil {
		return nil, err
	}
	coverageObject, err := assertFields(coverage, []string{"verdict", "assessments"}, nil, "coverage_review")
	if err != nil {
		return nil, err
	}
	verdict, _ := coverageObject["verdict"].(string)
	assessments, assessmentsOK := asArray(coverageObject["assessments"])
	if (verdict != "PASS" && verdict != "BLOCK") || !assessmentsOK {
		return nil, invalidf("invalid coverage review verdict or assessments.")
	}
	criteria, _ := asArray(request["criteria"])
	assessmentIDs := map[string]bool{}
	coveredTests := map[string]map[string]bool{}
	var bindings []map[string]any
	insufficient := 0
	for _, raw := range assessments {
		assessment, err := assertFields(raw, []string{"requirement_id", "verdict", "criterion_evidence", "rationale"}, nil, "coverage_assessment")
		if err != nil {
			return nil, err
		}
		requirementID := asStringOr(assessment["requirement_id"])
		var requirement map[string]any
		for _, rawRequirement := range requirements {
			candidate, _ := asObject(rawRequirement)
			if candidate["id"] == assessment["requirement_id"] {
				requirement = candidate
				break
			}
		}
		if requirement == nil || assessmentIDs[requirementID] {
			return nil, invalidf("coverage assessments must identify every requirement exactly once.")
		}
		assessmentIDs[requirementID] = true
		assessmentVerdict, _ := assessment["verdict"].(string)
		if assessmentVerdict != "SUFFICIENT" && assessmentVerdict != "INSUFFICIENT" {
			return nil, invalidf("invalid requirement coverage verdict.")
		}
		if err := assertText(assessment["rationale"], "coverage_assessment.rationale"); err != nil {
			return nil, err
		}
		if assessmentVerdict == "INSUFFICIENT" {
			insufficient++
		}
		evidenceItems, ok := asArray(assessment["criterion_evidence"])
		if !ok {
			return nil, invalidf("criterion_evidence must be an array.")
		}
		evidenceIDs := map[string]bool{}
		for _, rawEvidence := range evidenceItems {
			evidence, err := assertFields(rawEvidence, []string{"criterion_id", "test_ids", "source_paths", "observation", "evidence"}, nil, "criterion_evidence")
			if err != nil {
				return nil, err
			}
			criterionID := asStringOr(evidence["criterion_id"])
			mapped := false
			for _, id := range stringList(requirement["criterion_ids"]) {
				if id == criterionID {
					mapped = true
					break
				}
			}
			if !mapped || evidenceIDs[criterionID] {
				return nil, invalidf("criterion_evidence must match the trusted requirement mapping exactly.")
			}
			evidenceIDs[criterionID] = true
			if err := assertCoverageStringArray(evidence["test_ids"], "criterion_evidence.test_ids"); err != nil {
				return nil, err
			}
			if err := assertCoverageStringArray(evidence["source_paths"], "criterion_evidence.source_paths"); err != nil {
				return nil, err
			}
			if err := assertText(evidence["observation"], "criterion_evidence.observation"); err != nil {
				return nil, err
			}
			if err := assertText(evidence["evidence"], "criterion_evidence.evidence"); err != nil {
				return nil, err
			}
			criterion := findCriterion(criteria, criterionID)
			expectedTests := stringList(getValue(criterion, "expected_tests", []any{}))
			testIDs := stringList(evidence["test_ids"])
			for _, testID := range testIDs {
				declared := false
				for _, expected := range expectedTests {
					if expected == testID {
						declared = true
						break
					}
				}
				if !declared {
					return nil, invalidf("coverage review refers to an undeclared test.")
				}
				if coveredTests[criterionID] == nil {
					coveredTests[criterionID] = map[string]bool{}
				}
				coveredTests[criterionID][testID] = true
			}
			sourcePaths := stringList(evidence["source_paths"])
			if asStringOr(criterion["kind"]) == "file_assertion" {
				if len(testIDs) != 0 {
					return nil, invalidf("file assertions cannot claim test ids.")
				}
				if assessmentVerdict == "SUFFICIENT" && strings.Join(sourcePaths, "\n") != asStringOr(criterion["path"]) {
					return nil, invalidf("sufficient file assertion coverage must bind its declared path.")
				}
			} else {
				protected := stringList(getValue(criterion, "protected_paths", []any{}))
				if assessmentVerdict == "SUFFICIENT" && (len(testIDs) == 0 || len(sourcePaths) == 0) {
					return nil, invalidf("sufficient executable coverage needs concrete tests and protected source paths.")
				}
				for _, sourcePath := range sourcePaths {
					inScope, err := coveragePathInScope(sourcePath, protected)
					if err != nil {
						return nil, err
					}
					if !inScope {
						return nil, invalidf("coverage source path is outside criterion protected_paths.")
					}
				}
			}
			for _, sourcePath := range sourcePaths {
				if err := assertRelativePath(sourcePath); err != nil {
					return nil, err
				}
				absolute, err := safePath(filepath.Join(asStringOr(state["worker_path"]), filepath.FromSlash(sourcePath)))
				if err != nil {
					return nil, err
				}
				if !isRegularFile(absolute) {
					return nil, blockedf("coverage source evidence file is missing.")
				}
				hash, err := hashFile(absolute)
				if err != nil {
					return nil, err
				}
				bindings = append(bindings, map[string]any{
					"requirement_id": requirementID,
					"criterion_id":   criterionID,
					"path":           strings.ReplaceAll(sourcePath, `\`, "/"),
					"sha256":         hash,
				})
			}
		}
		evidenceList := make([]string, 0, len(evidenceIDs))
		for id := range evidenceIDs {
			evidenceList = append(evidenceList, id)
		}
		if sortedJoin(evidenceList) != sortedJoin(stringList(requirement["criterion_ids"])) {
			return nil, invalidf("criterion_evidence must match the trusted requirement mapping exactly.")
		}
	}
	assessmentList := make([]string, 0, len(assessmentIDs))
	for id := range assessmentIDs {
		assessmentList = append(assessmentList, id)
	}
	var requirementIDs []string
	for _, raw := range requirements {
		requirement, _ := asObject(raw)
		requirementIDs = append(requirementIDs, asStringOr(requirement["id"]))
	}
	if sortedJoin(assessmentList) != sortedJoin(requirementIDs) {
		return nil, invalidf("coverage assessments must identify every requirement exactly once.")
	}
	if (verdict == "PASS" && insufficient != 0) || (verdict == "BLOCK" && insufficient == 0) {
		return nil, invalidf("coverage verdict contradicts requirement assessments.")
	}
	if verdict == "PASS" {
		for _, raw := range criteria {
			criterion, _ := asObject(raw)
			expected := stringList(getValue(criterion, "expected_tests", []any{}))
			if len(expected) == 0 {
				continue
			}
			actual := make([]string, 0)
			for testID := range coveredTests[asStringOr(criterion["id"])] {
				actual = append(actual, testID)
			}
			if sortedJoin(actual) != sortedJoin(expected) {
				return nil, invalidf("PASS coverage must collectively bind every declared test id.")
			}
		}
	}
	requirementsHash, err := hashValue(requirements)
	if err != nil {
		return nil, err
	}
	criteriaHash, err := hashValue(request["criteria"])
	if err != nil {
		return nil, err
	}
	coverageHash, err := hashValue(coverage)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(bindings, func(i, j int) bool {
		return coverageBindingLess(bindings[i], bindings[j])
	})
	files := make([]any, 0, len(bindings))
	for _, binding := range bindings {
		files = append(files, binding)
	}
	binding := map[string]any{
		"schema_version":      int64(1),
		"kind":                "bsl-flow.requirement-coverage-binding",
		"requirements_sha256": requirementsHash,
		"criteria_sha256":     criteriaHash,
		"coverage_sha256":     coverageHash,
		"files":               files,
	}
	path, err := safePath(filepath.Join(directory, "coverage-review-binding.json"))
	if err != nil {
		return nil, err
	}
	bindingHash, err := hashValue(binding)
	if err != nil {
		return nil, err
	}
	if isRegularFile(path) {
		existing, err := readJSONObject(path)
		if err != nil {
			return nil, err
		}
		existingHash, err := hashValue(existing)
		if err != nil {
			return nil, err
		}
		if existingHash != bindingHash {
			return nil, conflictf("coverage evidence binding is stale or conflicting.")
		}
	} else {
		if err := writeJSON(path, binding, false); err != nil {
			return nil, err
		}
	}
	return binding, nil
}

// protectedTestManifest mirrors Get-BFProtectedTestManifest.
func protectedTestManifest(state map[string]any, manifest map[string]any) ([]any, error) {
	selected := map[string]map[string]any{}
	request, _ := asObject(state["request"])
	criteria, _ := asArray(request["criteria"])
	for _, raw := range criteria {
		criterion, _ := asObject(raw)
		for _, rawScope := range stringList(getValue(criterion, "protected_paths", []any{})) {
			scope := strings.TrimRight(strings.ReplaceAll(rawScope, `\`, "/"), "/")
			files, _ := asArray(manifest["files"])
			anyLive := false
			var matching []map[string]any
			for _, rawFile := range files {
				file, _ := asObject(rawFile)
				path := strings.ReplaceAll(asStringOr(file["path"]), `\`, "/")
				scopeMatches := scope == "." || strings.EqualFold(path, scope) ||
					strings.HasPrefix(strings.ToLower(path), strings.ToLower(scope+"/"))
				if !scopeMatches {
					continue
				}
				matching = append(matching, file)
				if deleted, ok := asBool(file["deleted"]); !ok || !deleted {
					anyLive = true
				}
			}
			if !anyLive {
				return nil, blockedf("protected test input is missing: %s", scope)
			}
			for _, file := range matching {
				selected[strings.ToLower(asStringOr(file["path"]))] = file
			}
		}
	}
	keys := make([]string, 0, len(selected))
	for key := range selected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]any, 0, len(keys))
	for _, key := range keys {
		result = append(result, selected[key])
	}
	return result, nil
}

func asBoolOr(value any) bool {
	flag, _ := asBool(value)
	return flag
}

// assertVerificationCoverage mirrors Assert-BFVerificationCoverage.
func assertVerificationCoverage(state map[string]any) error {
	request, _ := asObject(state["request"])
	if asStringOr(request["mode"]) != "implement" {
		return nil
	}
	criteria, _ := asArray(request["criteria"])
	kinds := map[string]bool{}
	for _, raw := range criteria {
		criterion, _ := asObject(raw)
		kinds[asStringOr(criterion["kind"])] = true
	}
	classification, _ := asObject(state["classification"])
	for _, raw := range anyItemsFrom(classification["impact_flags"]) {
		flag := asStringOr(raw)
		required := map[string]string{
			"posting": "integration", "data_exchange": "integration", "permissions": "integration",
			"data_migration": "integration", "data_deletion": "integration", "form_flow": "ui",
			"external_artifact": "external_artifact",
		}[flag]
		if required != "" && !kinds[required] {
			return blockedf("impact %s requires %s evidence selected from the original requirements.", flag, required)
		}
	}
	return nil
}

func anyItemsFrom(value any) []any {
	items, _ := asArray(value)
	return items
}
