package repository

import "strings"

// controllerProjection is the closed, read-only view of controller state.
// It deliberately copies only identifiers, statuses, hashes and short human
// text; request prompts, provider bodies, raw evidence and runtime inputs are
// never part of this projection.
func controllerProjection(state map[string]any, row Row) map[string]any {
	state = controllerPayload(state)
	status := row.Status
	if value, ok := asString(state["status"]); ok && value != "" {
		status = value
	}
	stage := row.Stage
	if value, ok := asString(state["stage"]); ok && value != "" {
		stage = value
	}
	nextAction := row.NextAction
	if nextAction == nil {
		version, _ := asInt(state["schema_version"])
		if version == 1 {
			nextAction = legacyNextAction(state)
		} else if status == "planned" {
			nextAction = "activate"
		}
	}
	blockers := projectionStrings(state["blockers"])
	if len(blockers) == 0 && len(row.Blockers) > 0 {
		blockers = projectionStrings(row.Blockers)
	}
	question := projectionQuestion(state["question"])
	if question == nil && row.Question != "" {
		question = map[string]any{"text": safeProjectionText(row.Question)}
	}

	return map[string]any{
		"request_summary":  projectionRequest(state),
		"criteria":         projectionCriteria(state),
		"status":           status,
		"stage":            stage,
		"next_action":      nextAction,
		"blockers":         blockers,
		"question":         question,
		"dependency_graph": map[string]any{"depends_on": append([]string{}, row.DependsOn...), "missing": []string{}},
		"attempts":         projectionAttempts(state["attempts"]),
		"acceptances":      projectionAcceptances(state["acceptances"]),
		"evidence":         projectionEvidence(state["evidence"]),
		"unknown_effect":   projectionUnknownEffect(state["unresolved_effect"]),
	}
}

func projectionRequest(state map[string]any) map[string]any {
	result := map[string]any{
		"request_id":             nil,
		"mode":                   nil,
		"analysis_goal":          nil,
		"complexity":             nil,
		"risk":                   nil,
		"impact_flags":           []string{},
		"request_hash":           nil,
		"intent_hash":            nil,
		"policy_hash":            nil,
		"intent_revision":        nil,
		"authorization_revision": nil,
	}
	if request, ok := state["request"].(map[string]any); ok {
		for _, key := range []string{"request_id", "mode", "analysis_goal", "complexity", "risk"} {
			if value, ok := asString(request[key]); ok && value != "" {
				result[key] = safeProjectionText(value)
			}
		}
		result["impact_flags"] = projectionStrings(request["impact_flags"])
	}
	for _, key := range []string{"request_hash", "intent_hash", "policy_hash"} {
		if value, ok := asString(state[key]); ok && value != "" {
			result[key] = safeProjectionText(value)
		}
	}
	for _, key := range []string{"intent_revision", "authorization_revision"} {
		if value, ok := asInt(state[key]); ok {
			result[key] = value
		}
	}
	return result
}

func projectionCriteria(state map[string]any) []map[string]any {
	request, _ := state["request"].(map[string]any)
	items, _ := request["criteria"].([]any)
	result := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, idOK := asString(item["id"])
		kind, kindOK := asString(item["kind"])
		observation, observationOK := asString(item["observation"])
		if !idOK || id == "" {
			continue
		}
		entry := map[string]any{"id": safeProjectionText(id)}
		if kindOK && kind != "" {
			entry["kind"] = safeProjectionText(kind)
		}
		if observationOK {
			entry["observation"] = safeProjectionText(observation)
		}
		result = append(result, entry)
	}
	return result
}

func projectionAttempts(value any) []map[string]any {
	items := anyItems(value)
	result := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		entry := map[string]any{}
		switch typed := raw.(type) {
		case string:
			if typed != "" {
				entry["attempt_id"] = safeProjectionText(typed)
			}
		case map[string]any:
			for _, key := range []string{"attempt_id", "stage", "outcome", "started_at"} {
				if text, ok := asString(typed[key]); ok && text != "" {
					entry[key] = safeProjectionText(text)
				}
			}
		}
		if len(entry) > 0 {
			result = append(result, entry)
		}
	}
	return result
}

func projectionAcceptances(value any) []map[string]any {
	items := anyItems(value)
	result := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		entry := map[string]any{}
		for _, key := range []string{"sha256", "verdict", "mode"} {
			if text, ok := asString(item[key]); ok && text != "" {
				entry[key] = safeProjectionText(text)
			}
		}
		if value, ok := asInt(item["intent_revision"]); ok {
			entry["intent_revision"] = value
		}
		if len(entry) > 0 {
			result = append(result, entry)
		}
	}
	return result
}

func projectionEvidence(value any) []map[string]any {
	items := anyItems(value)
	result := make([]map[string]any, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		entry := map[string]any{}
		for _, key := range []string{"attempt_id", "stage", "outcome", "result_sha256"} {
			if text, ok := asString(item[key]); ok && text != "" {
				entry[key] = safeProjectionText(text)
			}
		}
		if len(entry) > 0 {
			result = append(result, entry)
		}
	}
	return result
}

func projectionQuestion(value any) map[string]any {
	item, ok := value.(map[string]any)
	if !ok || item == nil {
		return nil
	}
	result := map[string]any{}
	for _, key := range []string{"question_id", "text"} {
		if text, ok := asString(item[key]); ok && text != "" {
			result[key] = safeProjectionText(text)
		}
	}
	if value, ok := asInt(item["intent_revision"]); ok {
		result["intent_revision"] = value
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func projectionUnknownEffect(value any) map[string]any {
	item, ok := value.(map[string]any)
	if !ok || item == nil {
		return nil
	}
	result := map[string]any{}
	for _, key := range []string{"attempt_id", "stage", "state", "scope", "source_before_sha256"} {
		if text, ok := asString(item[key]); ok && text != "" {
			result[key] = safeProjectionText(text)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func projectionStrings(value any) []string {
	result := []string{}
	switch typed := value.(type) {
	case []string:
		for _, text := range typed {
			result = append(result, safeProjectionText(text))
		}
	case []any:
		for _, raw := range typed {
			if text, ok := asString(raw); ok {
				result = append(result, safeProjectionText(text))
			}
		}
	}
	return result
}

func anyItems(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []string:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			result = append(result, item)
		}
		return result
	default:
		return nil
	}
}

// safeProjectionText removes terminal controls and redacts any free-text field
// containing a credential marker as a whole. Redacting the complete field is
// deliberate: a PEM header or a quoted value may be only the first match while
// the remaining private material continues on later lines.
func safeProjectionText(value string) string {
	if value == "" {
		return ""
	}
	if secretPattern.MatchString(value) {
		return "[REDACTED]"
	}
	return sanitize(value)
}

// safeErrorMessage applies the same redaction boundary to diagnostics. Error
// messages may include an untrusted path, field name or parser detail; keeping
// this at the common error-construction/output boundary prevents those values
// from reappearing in human or JSON envelopes.
func safeErrorMessage(value string) string {
	return safeProjectionText(value)
}

func sensitiveLabel(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "api_key", "api-key", "access_token", "access-token", "refresh_token", "refresh-token", "private_key", "private-key":
		return true
	default:
		return false
	}
}
