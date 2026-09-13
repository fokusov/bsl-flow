package repository

import "strings"

// Recheck the declared requirement/criterion/test mapping in Go. A provider
// cannot establish coverage just by hashing an incomplete review of itself.
func validateNativeCoverageAssessments(payload, coverage, binding map[string]any) error {
	if _, err := nativeObject(coverage, []string{"verdict", "assessments"}, nil, "coverage review"); err != nil {
		return err
	}
	if _, err := nativeObject(binding, []string{"schema_version", "kind", "requirements_sha256", "criteria_sha256", "coverage_sha256", "files"}, nil, "coverage binding"); err != nil {
		return err
	}
	if asIntOr(binding["schema_version"]) != 1 || binding["kind"] != "bsl-flow.requirement-coverage-binding" {
		return blocked("invalid coverage binding identity")
	}
	request := asMap(payload["request"])
	requirements := map[string]map[string]any{}
	criteria := map[string]map[string]any{}
	for _, raw := range anyItems(request["requirements"]) {
		value := asMap(raw)
		requirements[asStringOr(value["id"])] = value
	}
	for _, raw := range anyItems(request["criteria"]) {
		value := asMap(raw)
		criteria[asStringOr(value["id"])] = value
	}
	assessments, err := nativeArray(coverage["assessments"], "coverage assessments", true)
	if err != nil {
		return err
	}
	if len(assessments) != len(requirements) {
		return blocked("coverage omits a trusted requirement")
	}
	seenRequirements := map[string]bool{}
	coveredTests := map[string]map[string]bool{}
	expectedBindings := map[string]bool{}
	for _, raw := range assessments {
		assessment, err := nativeObject(raw, []string{"requirement_id", "verdict", "criterion_evidence", "rationale"}, nil, "coverage assessment")
		if err != nil {
			return err
		}
		rid := asStringOr(assessment["requirement_id"])
		requirement := requirements[rid]
		if requirement == nil || seenRequirements[rid] || assessment["verdict"] != "SUFFICIENT" {
			return blocked("coverage requirement is unknown, repeated or insufficient")
		}
		seenRequirements[rid] = true
		if err := validateNativeText(assessment["rationale"], "coverage rationale"); err != nil {
			return err
		}
		mapped, ok := asStringSlice(requirement["criterion_ids"])
		if !ok {
			return invalid("invalid trusted criterion mapping")
		}
		items, err := nativeArray(assessment["criterion_evidence"], "criterion evidence", true)
		if err != nil {
			return err
		}
		if len(items) != len(mapped) {
			return blocked("coverage criterion mapping is incomplete")
		}
		remaining := map[string]bool{}
		for _, id := range mapped {
			remaining[id] = true
		}
		for _, rawItem := range items {
			item, err := nativeObject(rawItem, []string{"criterion_id", "test_ids", "source_paths", "observation", "evidence"}, nil, "criterion evidence")
			if err != nil {
				return err
			}
			cid := asStringOr(item["criterion_id"])
			criterion := criteria[cid]
			if criterion == nil || !remaining[cid] {
				return blocked("coverage criterion is unknown or repeated")
			}
			delete(remaining, cid)
			for _, key := range []string{"observation", "evidence"} {
				if err := validateNativeText(item[key], "coverage "+key); err != nil {
					return err
				}
			}
			testIDs, err := validateNativeStringArray(item["test_ids"], "coverage test ids", false, false)
			if err != nil {
				return err
			}
			paths, err := validateNativeStringArray(item["source_paths"], "coverage source paths", true, false)
			if err != nil {
				return err
			}
			expectedTests, _ := asStringSlice(criterion["expected_tests"])
			allowedTests := map[string]bool{}
			for _, id := range expectedTests {
				allowedTests[id] = true
			}
			if coveredTests[cid] == nil {
				coveredTests[cid] = map[string]bool{}
			}
			for _, rawID := range testIDs {
				id := asStringOr(rawID)
				if !allowedTests[id] {
					return blocked("coverage claims an undeclared test")
				}
				coveredTests[cid][id] = true
			}
			if criterion["kind"] == "file_assertion" {
				if len(testIDs) != 0 || len(paths) != 1 || paths[0] != asStringOr(criterion["path"]) {
					return blocked("file assertion coverage differs from its declared path")
				}
			} else if len(testIDs) == 0 {
				return blocked("executable coverage has no concrete test")
			}
			for _, rawPath := range paths {
				path := asStringOr(rawPath)
				path = strings.ReplaceAll(path, `\`, "/")
				for _, segment := range strings.Split(path, "/") {
					if segment == ".." {
						return blocked("coverage source contains parent traversal")
					}
				}
				if err := validateRelativeNativePath(path, false); err != nil {
					return err
				}
				if criterion["kind"] != "file_assertion" {
					inside := false
					for _, rawScope := range anyItems(criterion["protected_paths"]) {
						scope := strings.TrimSuffix(strings.ReplaceAll(asStringOr(rawScope), `\`, "/"), "/")
						inside = inside || scope == "." || strings.EqualFold(path, scope) || strings.HasPrefix(strings.ToLower(path), strings.ToLower(scope)+"/")
					}
					if !inside {
						return blocked("coverage source is outside protected test inputs")
					}
				}
				key, _ := Hash([]any{rid, cid, strings.ReplaceAll(path, `\`, "/")})
				expectedBindings[key] = true
			}
		}
	}
	for cid, criterion := range criteria {
		expected, _ := asStringSlice(criterion["expected_tests"])
		for _, id := range expected {
			if !coveredTests[cid][id] {
				return blocked("coverage omits declared test %s", id)
			}
		}
	}
	files, err := nativeArray(binding["files"], "coverage files", true)
	if err != nil {
		return err
	}
	if len(files) != len(expectedBindings) {
		return blocked("coverage file binding count differs from the review")
	}
	for _, raw := range files {
		file, err := nativeObject(raw, []string{"requirement_id", "criterion_id", "path", "sha256"}, nil, "coverage file")
		if err != nil {
			return err
		}
		key, _ := Hash([]any{file["requirement_id"], file["criterion_id"], file["path"]})
		if !expectedBindings[key] {
			return blocked("coverage file binding is unknown or repeated")
		}
		delete(expectedBindings, key)
	}
	return nil
}
