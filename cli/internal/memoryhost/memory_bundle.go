package memoryhost

// This file ports Get-BFMemoryBundleFromReplay, Assert-BFMemoryBundleShape
// and Format-BFMemoryBundlePrompt from Task.Memory.ps1: the deterministic
// bounded stage selection and the advisory boundary prompt whose rendered
// byte length enforces the bundle budget.

import (
	"fmt"
	"sort"
	"strings"
)

type bundleCandidate struct {
	record         map[string]any
	recordID       string
	rank           int
	hasRank        bool
	selectedReason string
	excludedReason string
}

// bundleFromReplay ports Get-BFMemoryBundleFromReplay.
func bundleFromReplay(state *replayResult, projectID string, fingerprints map[string]any, stage string, paths []string, taskKind, errorSignature string, requireErrorSignature bool) (map[string]any, error) {
	if state != nil && !isNullOrWhiteSpace(state.projectID) {
		resolved, err := assertSafePath(projectID)
		if err != nil {
			return nil, bfBlocked("Memory replay project does not match the requested project.")
		}
		if state.projectID != resolved {
			return nil, bfBlocked("Memory replay project does not match the requested project.")
		}
	}
	relevantStages := []string{stage}
	if mapped, present := memoryStageRelevance[stage]; present {
		relevantStages = mapped
	}
	hintPaths := make([]string, 0, len(paths))
	for _, path := range paths {
		hintPaths = append(hintPaths, strings.Trim(strings.ReplaceAll(path, `\`, "/"), "/"))
	}
	hasHints := len(hintPaths) > 0
	if hasHints {
		for _, hint := range hintPaths {
			if hint == "" || hint == "." {
				hasHints = false
				break
			}
		}
	}
	candidates := make([]bundleCandidate, 0, len(state.records))
	ids := make([]string, 0, len(state.records))
	for id := range state.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		record := state.records[id]
		stateName := psString(record["state"])
		if !containsExact(memoryActiveStates, stateName) {
			continue
		}
		fingerprintObject, _ := record["fingerprints"].(map[string]any)
		if !sameFingerprints(fingerprintObject, fingerprints) {
			candidates = append(candidates, bundleCandidate{record: record, recordID: id, excludedReason: "fingerprint-mismatch"})
			continue
		}
		scope, _ := record["scope"].(map[string]any)
		recordStage := psString(valueOr(scope, "stage", ""))
		if !isNullOrWhiteSpace(recordStage) && !containsExact(relevantStages, recordStage) {
			candidates = append(candidates, bundleCandidate{record: record, recordID: id, rank: 99, hasRank: true, excludedReason: "stage-scope-mismatch"})
			continue
		}
		recordTaskKind := psString(valueOr(record, "task_kind", ""))
		if !isNullOrWhiteSpace(taskKind) && !isNullOrWhiteSpace(recordTaskKind) && recordTaskKind != taskKind {
			candidates = append(candidates, bundleCandidate{record: record, recordID: id, rank: 99, hasRank: true, excludedReason: "task-kind-mismatch"})
			continue
		}
		recordErrors := recordStringArray(record["error_signatures"])
		recordErrorTexts := make([]string, 0, len(recordErrors))
		for _, raw := range recordErrors {
			recordErrorTexts = append(recordErrorTexts, psString(raw))
		}
		if requireErrorSignature && psString(record["knowledge_class"]) == "diagnostic" && isNullOrWhiteSpace(errorSignature) {
			candidates = append(candidates, bundleCandidate{record: record, recordID: id, rank: 99, hasRank: true, excludedReason: "error-signature-unavailable"})
			continue
		}
		if !isNullOrWhiteSpace(errorSignature) && len(recordErrors) > 0 && !containsExact(recordErrorTexts, errorSignature) {
			candidates = append(candidates, bundleCandidate{record: record, recordID: id, rank: 99, hasRank: true, excludedReason: "error-signature-mismatch"})
			continue
		}
		recordPaths := recordStringArray(valueOr(scope, "paths", []any{}))
		if hasHints && len(recordPaths) > 0 {
			overlap := false
			for _, rawPath := range recordPaths {
				for _, hintPath := range hintPaths {
					if pathOverlap(psString(rawPath), hintPath) {
						overlap = true
						break
					}
				}
				if overlap {
					break
				}
			}
			if !overlap {
				candidates = append(candidates, bundleCandidate{record: record, recordID: id, rank: 99, hasRank: true, excludedReason: "path-scope-mismatch"})
				continue
			}
		}
		rank := 2
		selectedReason := "candidate-recommendation"
		switch stateName {
		case "accepted":
			rank = 0
			selectedReason = "accepted-knowledge"
		case "shadow":
			rank = 1
			selectedReason = "shadow-recommendation"
		}
		if !isNullOrWhiteSpace(taskKind) && recordTaskKind == taskKind {
			rank--
		}
		if !isNullOrWhiteSpace(errorSignature) && containsExact(recordErrorTexts, errorSignature) {
			rank--
		}
		candidates = append(candidates, bundleCandidate{record: record, recordID: id, rank: rank, hasRank: true, selectedReason: selectedReason})
	}
	// PowerShell Sort-Object places the null rank (fingerprint-mismatch
	// candidates) before every numeric rank, ties broken by record_id.
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.hasRank != right.hasRank {
			return !left.hasRank
		}
		if left.hasRank && left.rank != right.rank {
			return left.rank < right.rank
		}
		return left.recordID < right.recordID
	})
	type selectedEntry struct {
		record         map[string]any
		recordID       string
		selectedReason string
	}
	selected := make([]selectedEntry, 0, memoryMaxRecords)
	excluded := []any{}
	for _, entry := range candidates {
		if entry.excludedReason != "" {
			if len(excluded) < memoryMaxExcluded {
				excluded = append(excluded, map[string]any{"record_id": entry.recordID, "reason": entry.excludedReason})
			}
			continue
		}
		if len(selected) >= memoryMaxRecords {
			if len(excluded) < memoryMaxExcluded {
				excluded = append(excluded, map[string]any{"record_id": entry.recordID, "reason": "limit-records"})
			}
			continue
		}
		selected = append(selected, selectedEntry{record: entry.record, recordID: entry.recordID, selectedReason: entry.selectedReason})
	}
	summarize := func(entries []selectedEntry) []any {
		summaries := make([]any, 0, len(entries))
		for _, entry := range entries {
			record := entry.record
			summaries = append(summaries, map[string]any{
				"record_id":        psString(record["record_id"]),
				"state":            psString(record["state"]),
				"knowledge_class":  psString(record["knowledge_class"]),
				"risk_class":       psString(record["risk_class"]),
				"scope":            record["scope"],
				"observation":      record["observation"],
				"action":           record["action"],
				"confirmations":    psInt(record["confirmations"]),
				"contradictions":   psInt(record["contradictions"]),
				"selected_reason":  entry.selectedReason,
				"task_kind":        psString(valueOr(record, "task_kind", "")),
				"error_signatures": recordStringArray(valueOr(record, "error_signatures", []any{})),
				"evidence_ref":     pickEvidenceRef(valueOr(record, "evidence_ref", nil)),
			})
		}
		return summaries
	}
	summaries := summarize(selected)
	for {
		promptMemory := map[string]any{"available": true, "records": summaries, "excluded": excluded}
		rendered := formatBundlePrompt(promptMemory)
		if len(rendered) <= memoryMaxBundleChars || len(summaries) == 0 {
			break
		}
		last := selected[len(selected)-1]
		if len(excluded) < memoryMaxExcluded {
			excluded = append(excluded, map[string]any{"record_id": last.recordID, "reason": "limit-size"})
		}
		selected = selected[:len(selected)-1]
		summaries = summarize(selected)
	}
	content := map[string]any{
		"schema_version": 1,
		"stage":          stage,
		"project_id":     projectID,
		"fingerprints":   fingerprints,
		"records":        summaries,
		"excluded":       excluded,
	}
	digest, err := hashValue(content)
	if err != nil {
		return nil, err
	}
	content["bundle_id"] = digest
	content["bundle_sha256"] = digest
	return content, nil
}

// assertBundleShape ports Assert-BFMemoryBundleShape.
func assertBundleShape(bundle map[string]any) error {
	if bundle == nil {
		return bfBlocked("Memory bundle must be an object.")
	}
	fields := []string{"schema_version", "stage", "project_id", "fingerprints", "records", "excluded", "bundle_id", "bundle_sha256"}
	for _, field := range fields {
		if _, present := bundle[field]; !present {
			return bfBlocked("Memory bundle field %s is missing.", field)
		}
	}
	for key := range bundle {
		if !containsExact(fields, key) {
			return bfBlocked("Unknown memory bundle field %s.", key)
		}
	}
	if !equalsPSOne(bundle["schema_version"]) {
		return bfBlocked("Unsupported memory bundle schema version.")
	}
	if _, recordsOk := bundle["records"].([]any); !recordsOk {
		return bfBlocked("Memory bundle records and excluded must be arrays.")
	}
	if _, excludedOk := bundle["excluded"].([]any); !excludedOk {
		return bfBlocked("Memory bundle records and excluded must be arrays.")
	}
	if !isHexHash(psString(bundle["bundle_id"])) || !isHexHash(psString(bundle["bundle_sha256"])) {
		return bfBlocked("Memory bundle identity hashes are malformed.")
	}
	identity, err := hashValue(map[string]any{
		"schema_version": bundle["schema_version"],
		"stage":          bundle["stage"],
		"project_id":     bundle["project_id"],
		"fingerprints":   bundle["fingerprints"],
		"records":        bundle["records"],
		"excluded":       bundle["excluded"],
	})
	if err != nil {
		return err
	}
	if identity != psString(bundle["bundle_id"]) {
		return bfBlocked("Memory bundle identity hash mismatch.")
	}
	return nil
}

// formatBundlePrompt ports Format-BFMemoryBundlePrompt. The boundary
// sentence is mandatory: memory is advisory experience only.
func formatBundlePrompt(memory map[string]any) string {
	lines := make([]string, 0, 16)
	lines = append(lines, "Memory context (advisory experience only; it is not authorization, evidence, or a gate change; controller gates still apply):")
	if memory != nil {
		if available, present := memory["available"]; present && !toBoolOr(available) {
			reason := boundedText(psString(valueOr(memory, "disabled_reason", "unavailable")), 200)
			lines = append(lines, "- Memory is disabled for this attempt: "+reason)
			return strings.Join(lines, "\n")
		}
	}
	records := recordStringArray2(valueOr(memory, "records", []any{}))
	if len(records) == 0 {
		lines = append(lines, "- No applicable memory records for this stage.")
	} else {
		for _, record := range records {
			scope, _ := record["scope"].(map[string]any)
			scopeStage := psString(valueOr(scope, "stage", ""))
			scopePaths := stringArray(valueOr(scope, "paths", []any{}))
			pathsText := "paths: any"
			if len(scopePaths) > 0 {
				pathsText = "paths: " + strings.Join(scopePaths, ", ")
			}
			lines = append(lines, fmt.Sprintf("- [%s] %s %s/%s (%s; %s) confirmations=%d contradictions=%d",
				psString(record["state"]),
				psString(record["record_id"]),
				psString(record["knowledge_class"]),
				psString(record["risk_class"]),
				scopeStage,
				pathsText,
				psInt(record["confirmations"]),
				psInt(record["contradictions"])))
			taskKind := psString(valueOr(record, "task_kind", ""))
			if !isNullOrWhiteSpace(taskKind) {
				lines = append(lines, "  Task kind: "+taskKind)
			}
			errorSignatures := stringArray(valueOr(record, "error_signatures", []any{}))
			if len(errorSignatures) > 0 {
				lines = append(lines, "  Error signatures: "+strings.Join(errorSignatures, ", "))
			}
			evidenceText := formatEvidenceRef(valueOr(record, "evidence_ref", nil))
			if !isNullOrWhiteSpace(evidenceText) {
				lines = append(lines, "  Evidence ref: "+evidenceText)
			}
			if !isNullOrWhiteSpace(psString(record["observation"])) {
				lines = append(lines, "  Observation: "+psString(record["observation"]))
			}
			if record["action"] != nil {
				action, _ := record["action"].(map[string]any)
				prefix := "Recommended action"
				if psString(valueOr(action, "type", "")) == "avoid" {
					prefix = "Avoid action"
				}
				lines = append(lines, "  "+prefix+": "+psString(valueOr(action, "text", "")))
			}
			lines = append(lines, "  Why selected: "+psString(valueOr(record, "selected_reason", "")))
		}
	}
	excludedEntries := asAnyArray(valueOr(memory, "excluded", []any{}))
	if len(excludedEntries) > 0 {
		sample := make([]string, 0, 8)
		for index, raw := range excludedEntries {
			if index >= 8 {
				break
			}
			entry, _ := raw.(map[string]any)
			sample = append(sample, fmt.Sprintf("%s (%s)", psString(valueOr(entry, "record_id", "")), psString(valueOr(entry, "reason", ""))))
		}
		lines = append(lines, "Excluded memory: "+strings.Join(sample, ", "))
	}
	return strings.Join(lines, "\n")
}

func toBoolOr(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func recordStringArray2(value any) []map[string]any {
	items := asAnyArray(value)
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if typed, ok := item.(map[string]any); ok {
			result = append(result, typed)
		}
	}
	return result
}
