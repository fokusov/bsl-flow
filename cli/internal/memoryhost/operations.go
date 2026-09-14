package memoryhost

// This file ports the four operations the bridge dispatches to:
// Add-BFMemoryAttemptBinding (bind), Add-BFMemoryFromAttempt
// (extract-attempt), Add-BFMemoryFromAcceptance (extract-acceptance) and
// Get-BFMemoryProjection (projection). bind and projection never fail the
// envelope: their catch paths return an explicit disabled value, exactly
// like the PowerShell originals.

import (
	"os"
	"path/filepath"
	"sort"
)

// disabledBundle is the Add-BFMemoryAttemptBinding catch value.
func disabledBundle(message string) map[string]any {
	return map[string]any{
		"schema_version":  1,
		"available":       false,
		"bundle_id":       nil,
		"bundle_sha256":   nil,
		"records":         []any{},
		"excluded":        []any{},
		"disabled_reason": boundedText(message, 256),
	}
}

// opBind ports Add-BFMemoryAttemptBinding.
func opBind(state map[string]any, stage string, pendingFailure map[string]any, pendingFailureHash string, packageRoot string) map[string]any {
	value, err := opBindInner(state, stage, pendingFailure, pendingFailureHash, packageRoot)
	if err != nil {
		return disabledBundle(err.Error())
	}
	return value
}

func opBindInner(state map[string]any, stage string, pendingFailure map[string]any, pendingFailureHash string, packageRoot string) (map[string]any, error) {
	projectID, err := strictString(state, "project_path")
	if err != nil {
		return nil, err
	}
	directory, err := memoryDirectory(projectID)
	if err != nil {
		return nil, err
	}
	fingerprints, err := memoryFingerprints(state, packageRoot)
	if err != nil {
		return nil, err
	}
	current, err := replay(projectID)
	if err != nil {
		return nil, err
	}
	staleIDs := make([]string, 0, len(current.records))
	for id := range current.records {
		record := current.records[id]
		stateName := psString(record["state"])
		fingerprintObject, _ := record["fingerprints"].(map[string]any)
		if stateName != "" && containsExact(memoryActiveStates, stateName) && !sameFingerprints(fingerprintObject, fingerprints) {
			staleIDs = append(staleIDs, id)
		}
	}
	sort.Strings(staleIDs)
	if len(staleIDs) > 0 {
		plan := make([]map[string]any, 0, len(staleIDs))
		for _, id := range staleIDs {
			plan = append(plan, map[string]any{
				"event_type":        "quarantined",
				"record_id":         id,
				"knowledge_class":   nil,
				"risk_class":        nil,
				"scope":             nil,
				"observation":       nil,
				"action":            nil,
				"superseded_by":     nil,
				"source_task_id":    nil,
				"source_attempt_id": nil,
				"evidence_refs": []any{map[string]any{
					"kind":       "fingerprint-check",
					"policy":     psString(fingerprints["policy"]),
					"controller": psString(fingerprints["controller"]),
					"version":    psString(fingerprints["version"]),
				}},
				"reason": "fingerprint-mismatch",
			})
		}
		if _, err := addEvents(projectID, projectID, fingerprints, plan); err != nil {
			return nil, err
		}
		if current, err = replay(projectID); err != nil {
			return nil, err
		}
	}
	paths := sourcePaths(state)
	taskKind := taskKindOf(state)
	errorSignature := errorSignatureForState(state, stage, pendingFailure, pendingFailureHash)
	bundle, err := bundleFromReplay(current, projectID, fingerprints, stage, paths, taskKind, errorSignature, stage == "diagnose")
	if err != nil {
		return nil, err
	}
	if err := assertBundleShape(bundle); err != nil {
		return nil, err
	}
	indexPath := filepath.Join(directory, "index.json")
	needsPersist := true
	if info, statErr := os.Stat(indexPath); statErr == nil && !info.IsDir() {
		if index, readErr := readBFJSON(indexPath); readErr == nil {
			needsPersist = !indexFresh(index, current)
		}
	}
	if needsPersist && len(current.events) > 0 {
		index, err := newIndexObject(current.projectID, current.events, current.records, current.torn, current.eventFilesCount, current.lastEventSeq)
		if err != nil {
			return nil, err
		}
		if err := writeBFJSON(indexPath, index, true); err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"schema_version":  1,
		"available":       true,
		"bundle_id":       bundle["bundle_id"],
		"bundle_sha256":   bundle["bundle_sha256"],
		"records":         bundle["records"],
		"excluded":        bundle["excluded"],
		"disabled_reason": nil,
	}, nil
}

// sourcePaths mirrors @(Get-BFValue (Get-BFValue $State 'request') 'source_paths' @('.')).
func sourcePaths(state map[string]any) []string {
	request := valueOrMap(state, "request")
	values := asAnyArray(valueOr(request, "source_paths", []any{"."}))
	paths := make([]string, 0, len(values))
	for _, value := range values {
		paths = append(paths, psString(value))
	}
	return paths
}

// opExtractAttempt ports Add-BFMemoryFromAttempt. It returns true when
// events were written and nil when the attempt is idempotent or has nothing
// to record; every failure degrades to nil.
func opExtractAttempt(state, result map[string]any, resultHash string, packageRoot string) any {
	value := func() any {
		projectID, err := strictString(state, "project_path")
		if err != nil {
			return nil
		}
		resultTaskID, err := strictString(result, "task_id")
		if err != nil {
			return nil
		}
		resultAttemptID, err := strictString(result, "attempt_id")
		if err != nil {
			return nil
		}
		resultOutcome, err := strictString(result, "outcome")
		if err != nil {
			return nil
		}
		resultStage, err := strictString(result, "stage")
		if err != nil {
			return nil
		}
		resultSideEffects, err := strictString(result, "side_effects")
		if err != nil {
			return nil
		}
		evidence := []any{map[string]any{
			"kind":       "attempt-result",
			"task_id":    resultTaskID,
			"attempt_id": resultAttemptID,
			"sha256":     resultHash,
		}}
		current, err := replay(projectID)
		if err != nil {
			return nil
		}
		for _, event := range current.events {
			if psString(valueOr(event, "source_attempt_id", "")) != resultAttemptID {
				continue
			}
			for _, ref := range recordStringArray(event["evidence_refs"]) {
				reference, _ := ref.(map[string]any)
				if psString(valueOr(reference, "sha256", "")) == resultHash {
					return nil
				}
			}
		}
		fingerprints, err := memoryFingerprints(state, packageRoot)
		if err != nil {
			return nil
		}
		items := make([]map[string]any, 0, 1)
		rejections := make([]string, 0)
		reason := "confirmed-error"
		taskKind := taskKindOf(state)
		if resultOutcome == "PASS" && resultStage == "implement" {
			proposal := valueOrMap(result, "proposal")
			for _, observation := range asAnyArray(valueOr(proposal, "observations", []any{})) {
				checked := convertItem(observation, "implement")
				if !checked.ok {
					rejections = append(rejections, checked.reason)
				} else {
					rejections = append(rejections, "worker-prose-untrusted")
				}
			}
		} else if resultOutcome == "FAIL" && resultSideEffects == "none" && resultStage == "verify" {
			proposal := valueOrMap(result, "proposal")
			criterionID := valueOr(proposal, "criterion_id", nil)
			criterionKind := valueOr(proposal, "kind", nil)
			typedFailure := criterionID != nil && !isNullOrWhiteSpace(psString(criterionID)) && !isNullOrWhiteSpace(psString(criterionKind))
			if typedFailure {
				observationText := memoryTemplateObservationConfirmedFail
				actionText := memoryTemplateActionConfirmedFailure
				if isNullOrWhiteSpace(observationText) || isNullOrWhiteSpace(actionText) {
					rejections = append(rejections, "invalid-shape")
				} else if isSecretLike(observationText) || isSecretLike(actionText) {
					rejections = append(rejections, "secret-like-content")
				} else {
					items = append(items, map[string]any{
						"scope":           convertScope(resultStage, []string{}),
						"observation":     observationText,
						"action":          map[string]any{"type": "avoid", "text": actionText},
						"knowledge_class": "diagnostic",
						"risk_class":      "low",
					})
				}
			}
		}
		if len(items) == 0 && len(rejections) == 0 {
			return nil
		}
		plan := make([]map[string]any, 0, 4)
		shown := 0
		for _, reasonCode := range rejections {
			if shown >= memoryMaxRejections {
				break
			}
			plan = append(plan, map[string]any{
				"event_type":        "rejected",
				"record_id":         nil,
				"knowledge_class":   nil,
				"risk_class":        nil,
				"scope":             nil,
				"observation":       nil,
				"action":            nil,
				"superseded_by":     nil,
				"source_task_id":    resultTaskID,
				"source_attempt_id": resultAttemptID,
				"evidence_refs":     evidence,
				"reason":            reasonCode,
			})
			shown++
		}
		for _, item := range items {
			evidenceKind := ""
			if psString(item["knowledge_class"]) == "diagnostic" {
				evidenceKind = "confirmed-error"
			}
			provenance := ""
			if psString(item["knowledge_class"]) == "diagnostic" {
				provenance = "controller-error"
			}
			errorSignature := ""
			if evidenceKind == "confirmed-error" {
				errorSignature = errorSignatureOf(result)
			}
			entries, err := candidatePlan(current.records, projectID, fingerprints, item, resultTaskID, resultAttemptID, evidence, reason, evidenceKind, provenance, taskKind, errorSignature)
			if err != nil {
				return nil
			}
			plan = append(plan, entries...)
		}
		if len(plan) == 0 {
			return nil
		}
		if _, err := addEvents(projectID, projectID, fingerprints, plan); err != nil {
			return nil
		}
		return true
	}()
	return value
}

// opExtractAcceptance ports Add-BFMemoryFromAcceptance.
func opExtractAcceptance(state, receipt map[string]any, receiptHash string, packageRoot string) any {
	value := func() any {
		if isNullOrWhiteSpace(receiptHash) {
			digest, err := hashValue(receipt)
			if err != nil {
				return nil
			}
			receiptHash = digest
		}
		if !isHexHash(receiptHash) {
			return nil
		}
		template, ok := acceptedTemplateItem(state, receipt)
		if !ok {
			return nil
		}
		projectID, err := strictString(state, "project_path")
		if err != nil {
			return nil
		}
		current, err := replay(projectID)
		if err != nil {
			return nil
		}
		for _, event := range current.events {
			for _, ref := range recordStringArray(event["evidence_refs"]) {
				reference, _ := ref.(map[string]any)
				if psString(valueOr(reference, "sha256", "")) == receiptHash && psString(valueOr(reference, "kind", "")) == "acceptance-receipt" {
					return nil
				}
			}
		}
		fingerprints, err := memoryFingerprints(state, packageRoot)
		if err != nil {
			return nil
		}
		taskID, err := strictString(state, "task_id")
		if err != nil {
			return nil
		}
		evidence := []any{map[string]any{
			"kind":    "acceptance-receipt",
			"task_id": taskID,
			"sha256":  receiptHash,
		}}
		entries, err := candidatePlan(current.records, projectID, fingerprints, asMapOr(template["item"]), taskID, "acceptance:"+receiptHash, evidence,
			"controller-template:"+psString(template["template_id"]),
			psString(template["evidence_kind"]), psString(template["provenance"]), psString(template["task_kind"]), psString(template["error_signature"]))
		if err != nil {
			return nil
		}
		if len(entries) == 0 {
			return nil
		}
		if _, err := addEvents(projectID, projectID, fingerprints, entries); err != nil {
			return nil
		}
		return true
	}()
	return value
}

// opProjection ports Get-BFMemoryProjection.
func opProjection(state, next map[string]any, pendingFailure map[string]any, pendingFailureHash string, packageRoot string) map[string]any {
	value, err := opProjectionInner(state, next, pendingFailure, pendingFailureHash, packageRoot)
	if err != nil {
		return map[string]any{
			"schema_version": 1,
			"available":      false,
			"blocker":        boundedText(err.Error(), 256),
			"index": map[string]any{
				"events_count":      0,
				"event_files_count": 0,
				"last_event_seq":    0,
				"last_event_id":     nil,
				"replayed":          false,
			},
			"working_set": []any{},
			"records":     []any{},
			"bundle":      nil,
		}
	}
	return value
}

func opProjectionInner(state, next map[string]any, pendingFailure map[string]any, pendingFailureHash string, packageRoot string) (map[string]any, error) {
	projectID, err := strictString(state, "project_path")
	if err != nil {
		return nil, err
	}
	stage := psString(valueOr(next, "stage", ""))
	if isNullOrWhiteSpace(stage) {
		stage, err = strictString(state, "stage")
		if err != nil {
			return nil, err
		}
	}
	current, err := readSource(projectID)
	if err != nil {
		return nil, err
	}
	paths := sourcePaths(state)
	taskKind := taskKindOf(state)
	errorSignature := errorSignatureForState(state, stage, pendingFailure, pendingFailureHash)
	fingerprints, err := memoryFingerprints(state, packageRoot)
	if err != nil {
		return nil, err
	}
	bundle, err := bundleFromReplay(current, projectID, fingerprints, stage, paths, taskKind, errorSignature, stage == "diagnose")
	if err != nil {
		return nil, err
	}
	working := []string{}
	seen := map[string]bool{}
	for _, raw := range asAnyArray(bundle["records"]) {
		record, _ := raw.(map[string]any)
		scope, _ := record["scope"].(map[string]any)
		for _, path := range stringArray(valueOr(scope, "paths", []any{})) {
			if !seen[path] && len(working) < memoryMaxWorkingSet {
				seen[path] = true
				working = append(working, path)
			}
		}
	}
	sort.Strings(working)
	summaries := []any{}
	for _, raw := range asAnyArray(bundle["records"]) {
		record, _ := raw.(map[string]any)
		summaries = append(summaries, map[string]any{
			"record_id":        psString(record["record_id"]),
			"state":            psString(record["state"]),
			"knowledge_class":  psString(record["knowledge_class"]),
			"risk_class":       psString(record["risk_class"]),
			"scope":            record["scope"],
			"action":           record["action"],
			"selected_reason":  psString(record["selected_reason"]),
			"confirmations":    psInt(record["confirmations"]),
			"contradictions":   psInt(record["contradictions"]),
			"evidence_ref":     pickEvidenceRef(valueOr(record, "evidence_ref", nil)),
			"task_kind":        psString(valueOr(record, "task_kind", "")),
			"error_signatures": recordStringArray(valueOr(record, "error_signatures", []any{})),
		})
	}
	recordIDs := []any{}
	for _, raw := range asAnyArray(bundle["records"]) {
		record, _ := raw.(map[string]any)
		recordIDs = append(recordIDs, psString(record["record_id"]))
	}
	return map[string]any{
		"schema_version": 1,
		"available":      true,
		"blocker":        nil,
		"index": map[string]any{
			"events_count":      current.eventsCount,
			"event_files_count": current.eventFilesCount,
			"last_event_seq":    current.lastEventSeq,
			"last_event_id":     current.lastEventID,
			"replayed":          !current.indexed,
		},
		"working_set": toAnyArray(working),
		"records":     summaries,
		"bundle": map[string]any{
			"bundle_id":     bundle["bundle_id"],
			"bundle_sha256": bundle["bundle_sha256"],
			"record_ids":    recordIDs,
			"excluded":      bundle["excluded"],
		},
	}, nil
}
