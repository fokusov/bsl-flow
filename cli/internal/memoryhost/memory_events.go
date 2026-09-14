package memoryhost

// This file ports the journal half of Task.Memory.ps1: New-BFMemoryEventObject,
// Assert-BFMemoryEventShape, Invoke-BFMemoryEventOnRecords, Get-BFMemoryReplay,
// Add-BFMemoryEvents, the derived index object and its freshness/usable
// checks, and the candidate plan builder.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type memoryEventFile struct {
	name     string
	fullName string
	length   int64
	modTime  memoryFileTime
}

// memoryFileTime formats its UTC stamp exactly like
// LastWriteTimeUtc.ToString('o') (seven fractional digits).
type memoryFileTime struct{ raw string }

func newMemoryFileTime(unixNano int64) memoryFileTime {
	return memoryFileTime{raw: time.Unix(0, unixNano).UTC().Format("2006-01-02T15:04:05.0000000Z07:00")}
}

// memoryEventFiles ports Get-BFMemoryEventFiles.
func memoryEventFiles(projectPath string) ([]memoryEventFile, error) {
	directory, err := memoryDirectory(projectPath)
	if err != nil {
		return nil, err
	}
	eventsDirectory := filepath.Join(directory, "events")
	entries, err := os.ReadDir(eventsDirectory)
	if err != nil {
		if os.IsNotExist(err) {
			return []memoryEventFile{}, nil
		}
		return nil, err
	}
	files := make([]memoryEventFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		files = append(files, memoryEventFile{
			name:     name,
			fullName: filepath.Join(eventsDirectory, name),
			length:   info.Size(),
			modTime:  newMemoryFileTime(info.ModTime().UnixNano()),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	eventNamePattern := regexp.MustCompile(`^[0-9]{6}\.json$`)
	for _, file := range files {
		if !eventNamePattern.MatchString(file.name) {
			return nil, bfBlocked("Unexpected memory event filename: %s.", file.name)
		}
	}
	return files, nil
}

// memoryEventFilesStamp ports Get-BFMemoryEventFilesStamp.
func memoryEventFilesStamp(files []memoryEventFile) (string, error) {
	entries := make([]any, 0, len(files))
	for _, file := range files {
		entries = append(entries, map[string]any{
			"name":           file.name,
			"length":         file.length,
			"last_write_utc": file.modTime.raw,
		})
	}
	return hashValue(entries)
}

// jsonFailureKind ports Get-BFMemoryJsonFailureKind: "torn" marks a visibly
// truncated tail, "invalid" a complete or malformed value.
func jsonFailureKind(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "torn"
	}
	text, ok := strictUTF8Decode(data)
	if !ok {
		return "invalid"
	}
	text = stripBOM(text)
	trimmed := strings.TrimSpace(text)
	if isNullOrWhiteSpace(trimmed) {
		return "torn"
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return "invalid"
	}
	var stack []byte
	inString, escaped := false, false
	for index := 0; index < len(trimmed); index++ {
		character := trimmed[index]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			if character == '"' {
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, character)
		case '}', ']':
			if len(stack) == 0 {
				return "invalid"
			}
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if (character == '}' && open != '{') || (character == ']' && open != '[') {
				return "invalid"
			}
		}
	}
	if inString || escaped || len(stack) > 0 {
		return "torn"
	}
	return "invalid"
}

// newEventObject ports New-BFMemoryEventObject.
func newEventObject(projectID string, fingerprints map[string]any, entry map[string]any, previousEventID string) (map[string]any, error) {
	previous := any(nil)
	if previousEventID != "" {
		previous = previousEventID
	}
	payload := map[string]any{
		"schema_version":    1,
		"project_id":        projectID,
		"timestamp":         timestampNow(),
		"record_id":         valueOrPresent(entry, "record_id"),
		"event_type":        psString(valueOr(entry, "event_type", nil)),
		"knowledge_class":   valueOrPresent(entry, "knowledge_class"),
		"risk_class":        valueOrPresent(entry, "risk_class"),
		"scope":             valueOrPresent(entry, "scope"),
		"observation":       valueOrPresent(entry, "observation"),
		"action":            valueOrPresent(entry, "action"),
		"superseded_by":     valueOrPresent(entry, "superseded_by"),
		"evidence_refs":     arrayify(valueOr(entry, "evidence_refs", []any{})),
		"fingerprints":      fingerprints,
		"source_task_id":    valueOrPresent(entry, "source_task_id"),
		"source_attempt_id": valueOrPresent(entry, "source_attempt_id"),
		"reason":            valueOrPresent(entry, "reason"),
		"previous_event_id": previous,
		"task_kind":         valueOr(entry, "task_kind", ""),
		"error_signature":   valueOr(entry, "error_signature", ""),
		"evidence_kind":     valueOr(entry, "evidence_kind", ""),
		"provenance":        valueOr(entry, "provenance", ""),
	}
	eventID, err := hashValue(payload)
	if err != nil {
		return nil, err
	}
	payload["event_id"] = eventID
	contentHash, err := hashValue(payload)
	if err != nil {
		return nil, err
	}
	payload["content_hash"] = contentHash
	return payload, nil
}

func arrayify(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case nil:
		return []any{}
	default:
		return []any{value}
	}
}

// assertEventShape ports Assert-BFMemoryEventShape.
func assertEventShape(event map[string]any) error {
	requiredFields := []string{"schema_version", "project_id", "event_id", "timestamp", "record_id", "event_type", "knowledge_class", "risk_class", "scope", "observation", "action", "superseded_by", "evidence_refs", "fingerprints", "source_task_id", "source_attempt_id", "reason", "previous_event_id", "content_hash"}
	knownFields := append(append([]string{}, requiredFields...), "task_kind", "error_signature", "evidence_kind", "provenance")
	for _, field := range requiredFields {
		if _, present := event[field]; !present {
			return bfBlocked("Memory event field %s is missing.", field)
		}
	}
	for key := range event {
		if !containsExact(knownFields, key) {
			return bfBlocked("Unknown memory event field %s.", key)
		}
	}
	if !equalsPSOne(event["schema_version"]) {
		return bfBlocked("Unsupported memory event schema version.")
	}
	if !containsExact(memoryEventTypes, psString(event["event_type"])) {
		return bfBlocked("Unknown memory event type.")
	}
	if event["knowledge_class"] != nil && !containsExact(memoryKnowledgeClasses, psString(event["knowledge_class"])) {
		return bfBlocked("Unknown memory knowledge class.")
	}
	if event["risk_class"] != nil && !containsExact(memoryRiskClasses, psString(event["risk_class"])) {
		return bfBlocked("Unknown memory risk class.")
	}
	if !isHexHash(psString(event["event_id"])) || !isHexHash(psString(event["content_hash"])) {
		return bfBlocked("Memory event identity hashes are malformed.")
	}
	refs, refsOk := event["evidence_refs"].([]any)
	if !refsOk {
		return bfBlocked("Memory event evidence_refs must be an array.")
	}
	if len(refs) > memoryMaxEvidenceRefs {
		return bfBlocked("Memory event has too many evidence references.")
	}
	for _, reference := range refs {
		if convertEvidenceRef(reference) == nil {
			return bfBlocked("Memory event evidence reference is malformed.")
		}
	}
	for _, name := range []string{"task_kind", "error_signature", "evidence_kind", "provenance"} {
		value, present := event[name]
		if present && value != nil {
			if _, isString := value.(string); !isString {
				return bfBlocked("Memory event %s must be a string or null.", name)
			}
		}
	}
	if utf16Length(psString(event["task_kind"])) > 256 {
		return bfBlocked("Memory event task_kind is too long.")
	}
	if utf16Length(psString(event["error_signature"])) > 64 && psString(event["error_signature"]) != "" {
		return bfBlocked("Memory event error_signature is malformed.")
	}
	if !containsExact([]string{"", "accepted-result", "confirmed-error", "successful-recovery"}, psString(event["evidence_kind"])) {
		return bfBlocked("Unknown memory evidence kind.")
	}
	if !containsExact([]string{"", "controller-template", "controller-error"}, psString(event["provenance"])) {
		return bfBlocked("Unknown memory provenance.")
	}
	if event["scope"] != nil {
		scope, _ := event["scope"].(map[string]any)
		if scope == nil {
			scope = map[string]any{}
		}
		for _, field := range []string{"stage", "paths"} {
			if _, present := scope[field]; !present {
				return bfBlocked("Memory event scope requires stage and paths.")
			}
		}
		paths, pathsOk := scope["paths"].([]any)
		if !pathsOk {
			return bfBlocked("Memory event scope.paths must be an array.")
		}
		if len(paths) > memoryMaxScopePaths {
			return bfBlocked("Memory event scope has too many paths.")
		}
		for _, path := range paths {
			if utf16Length(psString(path)) > memoryMaxScopePathChars {
				return bfBlocked("Memory event scope path is too long.")
			}
		}
	}
	if event["action"] != nil {
		action, _ := event["action"].(map[string]any)
		if action == nil {
			action = map[string]any{}
		}
		for _, field := range []string{"type", "text"} {
			if _, present := action[field]; !present {
				return bfBlocked("Memory event action requires type and text.")
			}
		}
		if !containsExact(memoryActionTypes, psString(action["type"])) {
			return bfBlocked("Unknown memory action type.")
		}
	}
	if psString(event["event_type"]) == "superseded" &&
		(event["superseded_by"] == nil || psString(event["superseded_by"]) == psString(event["record_id"])) {
		return bfBlocked("Superseding event requires a different replacement record.")
	}
	payload := map[string]any{
		"schema_version":    event["schema_version"],
		"project_id":        event["project_id"],
		"timestamp":         event["timestamp"],
		"record_id":         event["record_id"],
		"event_type":        event["event_type"],
		"knowledge_class":   event["knowledge_class"],
		"risk_class":        event["risk_class"],
		"scope":             event["scope"],
		"observation":       event["observation"],
		"action":            event["action"],
		"superseded_by":     event["superseded_by"],
		"evidence_refs":     arrayify(event["evidence_refs"]),
		"fingerprints":      event["fingerprints"],
		"source_task_id":    event["source_task_id"],
		"source_attempt_id": event["source_attempt_id"],
		"reason":            event["reason"],
		"previous_event_id": event["previous_event_id"],
	}
	for _, name := range []string{"task_kind", "error_signature", "evidence_kind", "provenance"} {
		if value, present := event[name]; present {
			payload[name] = value
		}
	}
	identity, err := hashValue(payload)
	if err != nil {
		return err
	}
	if identity != psString(event["event_id"]) {
		return bfBlocked("Memory event %s failed its identity hash.", psString(event["event_id"]))
	}
	payload["event_id"] = event["event_id"]
	content, err := hashValue(payload)
	if err != nil {
		return err
	}
	if content != psString(event["content_hash"]) {
		return bfBlocked("Memory event %s failed its content hash.", psString(event["event_id"]))
	}
	return nil
}

// copyRecord ports Copy-BFMemoryRecord: a shallow member copy.
func copyRecord(record map[string]any) map[string]any {
	duplicate := make(map[string]any, len(record))
	for key, value := range record {
		duplicate[key] = value
	}
	return duplicate
}

func recordsCopy(records map[string]map[string]any) map[string]map[string]any {
	duplicate := make(map[string]map[string]any, len(records))
	for key, value := range records {
		duplicate[key] = copyRecord(value)
	}
	return duplicate
}

func recordStringArray(value any) []any {
	return asAnyArray(value)
}

func appendStringUnique(values []any, candidate string) []any {
	for _, existing := range values {
		if psString(existing) == candidate {
			return values
		}
	}
	return append(append([]any{}, values...), candidate)
}

// applyEventOnRecords ports Invoke-BFMemoryEventOnRecords.
func applyEventOnRecords(records map[string]map[string]any, event map[string]any) error {
	recordID := psString(event["record_id"])
	record := records[recordID]
	switch psString(event["event_type"]) {
	case "rejected":
		if recordID != "" {
			return bfConflict("Rejected marker events carry no record identity.")
		}
	case "candidate":
		if record != nil {
			return bfConflict("Candidate event duplicates an existing record identity.")
		}
		evidenceKind := psString(valueOr(event, "evidence_kind", ""))
		initialConfirmations := 0
		if evidenceKind == "accepted-result" || isNullOrWhiteSpace(evidenceKind) {
			initialConfirmations = 1
		}
		initialTasks := []any{}
		if initialConfirmations > 0 && !isNullOrWhiteSpace(psString(valueOr(event, "source_task_id", ""))) {
			initialTasks = append(initialTasks, psString(valueOr(event, "source_task_id", "")))
		}
		taskKinds := []any{}
		if !isNullOrWhiteSpace(psString(valueOr(event, "task_kind", ""))) {
			taskKinds = append(taskKinds, psString(valueOr(event, "task_kind", "")))
		}
		errorSignatures := []any{}
		if !isNullOrWhiteSpace(psString(valueOr(event, "error_signature", ""))) {
			errorSignatures = append(errorSignatures, psString(valueOr(event, "error_signature", "")))
		}
		evidenceKinds := []any{}
		if !isNullOrWhiteSpace(psString(valueOr(event, "evidence_kind", ""))) {
			evidenceKinds = append(evidenceKinds, psString(valueOr(event, "evidence_kind", "")))
		}
		records[recordID] = map[string]any{
			"record_id":          recordID,
			"state":              "candidate",
			"knowledge_class":    psString(event["knowledge_class"]),
			"risk_class":         psString(event["risk_class"]),
			"scope":              event["scope"],
			"observation":        event["observation"],
			"action":             event["action"],
			"confirmations":      initialConfirmations,
			"confirmation_tasks": initialTasks,
			"contradictions":     0,
			"superseded_by":      nil,
			"fingerprints":       event["fingerprints"],
			"evidence_count":     1,
			"first_event_id":     psString(event["event_id"]),
			"last_event_id":      psString(event["event_id"]),
			"last_reason":        psString(event["reason"]),
			"task_kind":          psString(valueOr(event, "task_kind", "")),
			"task_kinds":         taskKinds,
			"error_signatures":   errorSignatures,
			"evidence_kinds":     evidenceKinds,
			"evidence_ref":       pickEvidenceRef(event["evidence_refs"]),
			"provenance":         psString(valueOr(event, "provenance", "")),
		}
	case "confirmed":
		if record == nil || !containsExact(memoryActiveStates, psString(record["state"])) {
			return bfConflict("Confirmation requires an active record.")
		}
		record["confirmations"] = psInt(record["confirmations"]) + 1
		task := psString(valueOr(event, "source_task_id", ""))
		record["confirmation_tasks"] = appendStringUnique(recordStringArray(record["confirmation_tasks"]), task)
		record["evidence_count"] = psInt(record["evidence_count"]) + 1
		record["last_event_id"] = psString(event["event_id"])
		record["last_reason"] = psString(event["reason"])
		evidenceKind := psString(valueOr(event, "evidence_kind", ""))
		if !isNullOrWhiteSpace(evidenceKind) {
			record["evidence_kinds"] = appendStringUnique(recordStringArray(record["evidence_kinds"]), evidenceKind)
		}
		if evidenceRef := pickEvidenceRef(event["evidence_refs"]); evidenceRef != nil {
			record["evidence_ref"] = evidenceRef
		}
		taskKind := psString(valueOr(event, "task_kind", ""))
		if !isNullOrWhiteSpace(taskKind) && isNullOrWhiteSpace(psString(valueOr(record, "task_kind", ""))) {
			record["task_kind"] = taskKind
		}
		if !isNullOrWhiteSpace(taskKind) {
			record["task_kinds"] = appendStringUnique(recordStringArray(record["task_kinds"]), taskKind)
		}
		errorSignature := psString(valueOr(event, "error_signature", ""))
		if !isNullOrWhiteSpace(errorSignature) {
			record["error_signatures"] = appendStringUnique(recordStringArray(record["error_signatures"]), errorSignature)
		}
	case "shadow":
		if record == nil || !containsExact([]string{"candidate", "shadow"}, psString(record["state"])) {
			return bfConflict("Shadow transition requires an unpromoted active record.")
		}
		record["state"] = "shadow"
		record["last_event_id"] = psString(event["event_id"])
		record["last_reason"] = psString(event["reason"])
	case "promoted":
		if record == nil || !containsExact([]string{"candidate", "shadow"}, psString(record["state"])) {
			return bfConflict("Promotion requires an unpromoted active record.")
		}
		if psString(record["knowledge_class"]) != "procedural" || psString(record["risk_class"]) != "low" {
			return bfConflict("Promotion is restricted to procedural low-risk records.")
		}
		_, provenanceOnEvent := event["provenance"]
		legacyPromotion := !provenanceOnEvent && isNullOrWhiteSpace(psString(valueOr(record, "provenance", "")))
		if !legacyPromotion {
			accepted := false
			for _, kind := range recordStringArray(record["evidence_kinds"]) {
				if psString(kind) == "accepted-result" || psString(kind) == "successful-recovery" {
					accepted = true
					break
				}
			}
			if psString(valueOr(record, "provenance", "")) != "controller-template" || !accepted {
				return bfConflict("Promotion requires a controller-generated procedural template with accepted evidence.")
			}
		}
		if psInt(record["contradictions"]) > 0 {
			return bfConflict("Promotion requires a contradiction-free record.")
		}
		if psInt(record["confirmations"]) < 3 || len(recordStringArray(record["confirmation_tasks"])) < 2 {
			return bfConflict("Promotion requires three confirmations from two different tasks.")
		}
		record["state"] = "accepted"
		record["last_event_id"] = psString(event["event_id"])
		record["last_reason"] = psString(event["reason"])
	case "contradicted":
		if record == nil || !containsExact(memoryActiveStates, psString(record["state"])) {
			return bfConflict("Contradiction requires an active record.")
		}
		record["contradictions"] = psInt(record["contradictions"]) + 1
		record["last_event_id"] = psString(event["event_id"])
		record["last_reason"] = psString(event["reason"])
	case "quarantined":
		if record == nil || !containsExact(memoryActiveStates, psString(record["state"])) {
			return bfConflict("Quarantine requires an active record.")
		}
		record["state"] = "quarantined"
		record["last_event_id"] = psString(event["event_id"])
		record["last_reason"] = psString(event["reason"])
	case "deprecated":
		if record == nil || !containsExact(memoryActiveStates, psString(record["state"])) {
			return bfConflict("Deprecation requires an active record.")
		}
		record["state"] = "deprecated"
		record["last_event_id"] = psString(event["event_id"])
		record["last_reason"] = psString(event["reason"])
	case "superseded":
		if record == nil || psString(record["state"]) != "accepted" {
			return bfConflict("Superseding requires an accepted record.")
		}
		if psString(event["superseded_by"]) == "" {
			return bfConflict("Superseding requires a replacement record identity.")
		}
		record["state"] = "superseded"
		record["superseded_by"] = psString(event["superseded_by"])
		record["last_event_id"] = psString(event["event_id"])
		record["last_reason"] = psString(event["reason"])
	case "reinstated":
		if record == nil || psString(record["state"]) != "quarantined" {
			return bfConflict("Reinstatement requires a quarantined record.")
		}
		record["state"] = "shadow"
		record["last_event_id"] = psString(event["event_id"])
		record["last_reason"] = psString(event["reason"])
	default:
		return bfBlocked("Unknown memory event type.")
	}
	return nil
}

// replayResult mirrors the ordered dictionary returned by Get-BFMemoryReplay
// and Get-BFMemoryReadSource.
type replayResult struct {
	events          []map[string]any
	eventsCount     int
	torn            []string
	records         map[string]map[string]any
	lastEventID     any
	nextSeq         int
	projectID       string
	eventFilesCount int
	lastEventSeq    int
	eventFilesStamp string
	recordsSHA256   string
	replayed        bool
	indexed         bool
}

// replay ports Get-BFMemoryReplay.
func replay(projectPath string) (*replayResult, error) {
	files, err := memoryEventFiles(projectPath)
	if err != nil {
		return nil, err
	}
	if len(files) > memoryMaxEventFiles {
		return nil, bfBlocked("Memory event file count exceeds the supported limit of %d.", memoryMaxEventFiles)
	}
	stamp, err := memoryEventFilesStamp(files)
	if err != nil {
		return nil, err
	}
	events := make([]map[string]any, 0, len(files))
	torn := []string{}
	records := map[string]map[string]any{}
	var previous any
	maxSeq := 0
	expectedSeq := 1
	sawTorn := false
	for _, file := range files {
		sequence := atoiOrDefault(file.name[:6], -1)
		if sequence != expectedSeq {
			return nil, bfBlocked("Memory event sequence has a gap before %s.", file.name)
		}
		maxSeq = sequence
		expectedSeq++
		if file.length > memoryMaxEventBytes {
			if jsonFailureKind(file.fullName) == "torn" {
				torn = append(torn, file.name)
				sawTorn = true
				continue
			}
			return nil, bfBlocked("Memory event exceeds the supported size and is complete or invalid at %s.", file.name)
		}
		event, readErr := readBFJSON(file.fullName)
		if readErr != nil {
			if jsonFailureKind(file.fullName) == "torn" {
				torn = append(torn, file.name)
				sawTorn = true
				continue
			}
			return nil, bfBlocked("Memory event JSON is complete but invalid at %s: %s", file.name, readErr.Error())
		}
		if shapeErr := assertEventShape(event); shapeErr != nil {
			return nil, bfBlocked("Memory event is complete but invalid at %s: %s", file.name, shapeErr.Error())
		}
		if sawTorn {
			return nil, bfBlocked("Memory event follows a torn event at %s; the append-only tail cannot be reconciled.", file.name)
		}
		if psString(event["previous_event_id"]) != psString(previous) {
			return nil, bfBlocked("Memory event chain is broken at %s.", file.name)
		}
		if len(events) > 0 && psString(event["project_id"]) != psString(events[0]["project_id"]) {
			return nil, bfBlocked("Memory event project changed at %s.", file.name)
		}
		resolvedProject, err := assertSafePath(projectPath)
		if err != nil {
			return nil, err
		}
		if psString(event["project_id"]) != resolvedProject {
			return nil, bfBlocked("Memory event project does not match the requested project at %s.", file.name)
		}
		if err := applyEventOnRecords(records, event); err != nil {
			return nil, err
		}
		events = append(events, event)
		previous = event["event_id"]
	}
	projectID := ""
	if len(events) > 0 {
		projectID = psString(events[0]["project_id"])
	}
	recordsDigest, err := hashValue(records)
	if err != nil {
		return nil, err
	}
	return &replayResult{
		events:          events,
		eventsCount:     len(events),
		torn:            torn,
		records:         records,
		lastEventID:     previous,
		nextSeq:         maxSeq + 1,
		projectID:       projectID,
		eventFilesCount: len(files),
		lastEventSeq:    maxSeq,
		eventFilesStamp: stamp,
		recordsSHA256:   recordsDigest,
		replayed:        true,
	}, nil
}

func atoiOrDefault(text string, fallback int) int {
	value := 0
	for _, digit := range text {
		if digit < '0' || digit > '9' {
			return fallback
		}
		value = value*10 + int(digit-'0')
	}
	return value
}

// newIndexObject ports New-BFMemoryIndexObject.
func newIndexObject(projectID string, events []map[string]any, records map[string]map[string]any, torn []string, eventFilesCount, lastEventSeq int) (map[string]any, error) {
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	quarantined, deprecated, superseded := []any{}, []any{}, []any{}
	for _, id := range ids {
		switch psString(records[id]["state"]) {
		case "quarantined":
			quarantined = append(quarantined, id)
		case "deprecated":
			deprecated = append(deprecated, id)
		case "superseded":
			superseded = append(superseded, id)
		}
	}
	var lastEventID any
	if len(events) > 0 {
		lastEventID = psString(events[len(events)-1]["event_id"])
	}
	recordsDigest, err := hashValue(records)
	if err != nil {
		return nil, err
	}
	maxSeq := lastEventSeq
	if maxSeq < 0 {
		maxSeq = 0
		pattern := regexp.MustCompile(`^([0-9]{6})\.json$`)
		for _, name := range torn {
			if match := pattern.FindStringSubmatch(name); match != nil {
				if value := atoiOrDefault(match[1], 0); value > maxSeq {
					maxSeq = value
				}
			}
		}
	}
	filesCount := eventFilesCount
	if filesCount < 0 {
		filesCount = len(events) + len(torn)
	}
	stamp := ""
	if files, err := memoryEventFiles(projectID); err == nil {
		stamp, err = memoryEventFilesStamp(files)
		if err != nil {
			stamp, _ = hashValue([]any{})
		}
	} else {
		stamp, _ = hashValue([]any{})
	}
	var tornEvents = []any{}
	for _, name := range torn {
		tornEvents = append(tornEvents, name)
	}
	recordsAsAny := map[string]any{}
	for id, record := range records {
		recordsAsAny[id] = record
	}
	return map[string]any{
		"schema_version":    1,
		"project_id":        projectID,
		"events_count":      len(events),
		"event_files_count": filesCount,
		"last_event_seq":    maxSeq,
		"event_files_stamp": stamp,
		"last_event_id":     lastEventID,
		"torn_events":       tornEvents,
		"quarantined_ids":   quarantined,
		"deprecated_ids":    deprecated,
		"superseded_ids":    superseded,
		"records_sha256":    recordsDigest,
		"records":           recordsAsAny,
	}, nil
}

// indexFresh ports Test-BFMemoryIndexFresh.
func indexFresh(index map[string]any, state *replayResult) bool {
	if index == nil {
		return false
	}
	if numberOrZero(index["schema_version"]) != 1 || !isJSONInt(index["schema_version"]) {
		return false
	}
	if numberOrZero(index["events_count"]) != int64(len(state.events)) || !isJSONInt(index["events_count"]) {
		return false
	}
	if value, present := index["event_files_count"]; present {
		if numberOrZero(value) != int64(state.eventFilesCount) || !isJSONInt(value) {
			return false
		}
	}
	if value, present := index["last_event_seq"]; present {
		if numberOrZero(value) != int64(state.lastEventSeq) || !isJSONInt(value) {
			return false
		}
	}
	if psString(index["last_event_id"]) != psString(state.lastEventID) {
		return false
	}
	if strings.Join(stringArray(index["torn_events"]), "|") != strings.Join(state.torn, "|") {
		return false
	}
	if value, present := index["records_sha256"]; present && psString(value) != state.recordsSHA256 {
		return false
	}
	if value, present := index["event_files_stamp"]; present && psString(value) != state.eventFilesStamp {
		return false
	}
	return true
}

func isJSONInt(value any) bool {
	switch typed := value.(type) {
	case int, int64:
		return true
	case json.Number:
		_, err := typed.Int64()
		return err == nil
	}
	return false
}

func numberOrZero(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed
		}
	}
	return 0
}

func stringArray(value any) []string {
	items := asAnyArray(value)
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, psString(item))
	}
	return result
}

// indexUsable ports Test-BFMemoryIndexUsable.
func indexUsable(index map[string]any, projectPath string) bool {
	return func() bool {
		if index == nil || numberOrZero(index["schema_version"]) != 1 || !isJSONInt(index["schema_version"]) {
			return false
		}
		for _, field := range []string{"project_id", "events_count", "event_files_count", "last_event_seq", "event_files_stamp", "last_event_id", "torn_events", "records_sha256", "records"} {
			if _, present := index[field]; !present {
				return false
			}
		}
		if isNullOrWhiteSpace(psString(index["project_id"])) {
			return false
		}
		resolvedProject, err := assertSafePath(projectPath)
		if err != nil || resolvedProject != assertSafePathOrEmpty(psString(index["project_id"])) {
			return false
		}
		records, ok := index["records"].(map[string]any)
		if !ok {
			return false
		}
		if !isHexHash(psString(index["records_sha256"])) {
			return false
		}
		if !isHexHash(psString(index["event_files_stamp"])) {
			return false
		}
		recordsAsAny := map[string]any{}
		for id, record := range records {
			recordsAsAny[id] = record
		}
		digest, err := hashValue(recordsAsAny)
		if err != nil || digest != psString(index["records_sha256"]) {
			return false
		}
		files, err := memoryEventFiles(projectPath)
		if err != nil {
			return false
		}
		if len(files) != int(numberOrZero(index["event_files_count"])) {
			return false
		}
		if len(stringArray(index["torn_events"])) > 0 {
			return false
		}
		if numberOrZero(index["events_count"]) != int64(len(files)) {
			return false
		}
		lastSeq := 0
		if len(files) > 0 {
			lastSeq = atoiOrDefault(files[len(files)-1].name[:6], 0)
		}
		if lastSeq != int(numberOrZero(index["last_event_seq"])) {
			return false
		}
		if len(files) > 0 {
			head, err := readBFJSON(files[len(files)-1].fullName)
			if err != nil {
				return false
			}
			if err := assertEventShape(head); err != nil {
				return false
			}
			if psString(head["project_id"]) != psString(index["project_id"]) || psString(head["event_id"]) != psString(index["last_event_id"]) {
				return false
			}
		} else if index["last_event_id"] != nil {
			return false
		}
		stamp, err := memoryEventFilesStamp(files)
		if err != nil || stamp != psString(index["event_files_stamp"]) {
			return false
		}
		return true
	}()
}

func assertSafePathOrEmpty(path string) string {
	resolved, err := assertSafePath(path)
	if err != nil {
		return "\x00invalid:" + path
	}
	return resolved
}

// readSource ports Get-BFMemoryReadSource.
func readSource(projectPath string) (*replayResult, error) {
	directory, err := memoryDirectory(projectPath)
	if err != nil {
		return nil, err
	}
	indexPath := filepath.Join(directory, "index.json")
	if info, err := os.Stat(indexPath); err == nil && !info.IsDir() {
		if index, err := readBFJSON(indexPath); err == nil {
			if indexUsable(index, projectPath) {
				records := map[string]map[string]any{}
				if rawRecords, ok := index["records"].(map[string]any); ok {
					for id, record := range rawRecords {
						if typed, ok := record.(map[string]any); ok {
							records[id] = typed
						}
					}
				}
				return &replayResult{
					events:          []map[string]any{},
					eventsCount:     int(numberOrZero(index["events_count"])),
					torn:            stringArray(index["torn_events"]),
					records:         records,
					lastEventID:     index["last_event_id"],
					nextSeq:         int(numberOrZero(index["last_event_seq"])) + 1,
					projectID:       psString(index["project_id"]),
					eventFilesCount: int(numberOrZero(index["event_files_count"])),
					lastEventSeq:    int(numberOrZero(index["last_event_seq"])),
					eventFilesStamp: psString(index["event_files_stamp"]),
					recordsSHA256:   psString(index["records_sha256"]),
					replayed:        false,
					indexed:         true,
				}, nil
			}
		}
	}
	result, err := replay(projectPath)
	if err != nil {
		return nil, err
	}
	result.indexed = false
	return result, nil
}

// addEvents ports Add-BFMemoryEvents: one locked write session that replays,
// validates the whole plan, appends deterministically and republishes the
// derived index.
func addEvents(projectPath, projectID string, fingerprints map[string]any, plan []map[string]any) (map[string]any, error) {
	directory, err := memoryDirectory(projectPath)
	if err != nil {
		return nil, err
	}
	lock, err := enterLock(directory)
	if err != nil {
		return nil, err
	}
	defer lock.release()
	state, err := replay(projectPath)
	if err != nil {
		return nil, err
	}
	if state.projectID != "" && state.projectID != projectID {
		return nil, bfConflict("Memory store belongs to a different project.")
	}
	if len(state.torn) > 0 {
		return nil, bfBlocked("Memory store has an unresolved torn tail; repair or quarantine it before appending.")
	}
	records := recordsCopy(state.records)
	previous := psString(state.lastEventID)
	appended := make([]map[string]any, 0, len(plan))
	for _, entry := range plan {
		event, err := newEventObject(projectID, fingerprints, entry, previous)
		if err != nil {
			return nil, err
		}
		if err := applyEventOnRecords(records, event); err != nil {
			return nil, err
		}
		appended = append(appended, event)
		previous = psString(event["event_id"])
	}
	writeSeq := state.nextSeq
	for _, event := range appended {
		if err := writeBFJSON(filepath.Join(directory, fmt.Sprintf("events/%06d.json", writeSeq)), event, false); err != nil {
			return nil, err
		}
		writeSeq++
	}
	allEvents := append(append([]map[string]any{}, state.events...), appended...)
	index, err := newIndexObject(projectID, allEvents, records, state.torn, state.eventFilesCount+len(appended), writeSeq-1)
	if err != nil {
		return nil, err
	}
	if err := writeBFJSON(filepath.Join(directory, "index.json"), index, true); err != nil {
		return nil, err
	}
	return index, nil
}

// promotionEligible ports Test-BFMemoryPromotionEligible.
func promotionEligible(record, fingerprints map[string]any) bool {
	if record == nil {
		return false
	}
	if !containsExact([]string{"candidate", "shadow"}, psString(record["state"])) {
		return false
	}
	if psString(record["knowledge_class"]) != "procedural" || psString(record["risk_class"]) != "low" {
		return false
	}
	if psString(valueOr(record, "provenance", "")) != "controller-template" {
		return false
	}
	accepted := false
	for _, kind := range recordStringArray(record["evidence_kinds"]) {
		if psString(kind) == "accepted-result" || psString(kind) == "successful-recovery" {
			accepted = true
			break
		}
	}
	if !accepted {
		return false
	}
	if psInt(record["contradictions"]) > 0 {
		return false
	}
	if psInt(record["confirmations"]) < 3 {
		return false
	}
	if len(recordStringArray(record["confirmation_tasks"])) < 2 {
		return false
	}
	fingerprintObject, _ := record["fingerprints"].(map[string]any)
	if !sameFingerprints(fingerprintObject, fingerprints) {
		return false
	}
	return true
}

// candidatePlan ports Get-BFMemoryCandidatePlan.
func candidatePlan(records map[string]map[string]any, projectID string, fingerprints map[string]any, item map[string]any, sourceTaskID, sourceAttemptID string, evidenceRefs []any, reason, evidenceKind, provenance, taskKind, errorSignature string) ([]map[string]any, error) {
	plan := make([]map[string]any, 0, 4)
	var id string
	var err error
	if evidenceKind == "confirmed-error" && !isNullOrWhiteSpace(errorSignature) {
		id, err = recordID(projectID, asMapOr(item["scope"]), asMapOr(item["action"]), fingerprints, errorSignature)
	} else {
		id, err = recordID(projectID, asMapOr(item["scope"]), asMapOr(item["action"]), fingerprints, "")
	}
	if err != nil {
		return nil, err
	}
	existing := records[id]
	transitionEntry := func(eventType string, overrides map[string]any) map[string]any {
		entry := map[string]any{
			"event_type":        eventType,
			"record_id":         nil,
			"knowledge_class":   nil,
			"risk_class":        nil,
			"scope":             nil,
			"observation":       nil,
			"action":            nil,
			"superseded_by":     nil,
			"source_task_id":    nil,
			"source_attempt_id": nil,
			"evidence_refs":     evidenceRefs,
			"reason":            "",
			"evidence_kind":     evidenceKind,
			"provenance":        provenance,
			"task_kind":         taskKind,
			"error_signature":   errorSignature,
		}
		for key, value := range overrides {
			entry[key] = value
		}
		return entry
	}
	if existing != nil {
		if evidenceKind == "confirmed-error" {
			return plan, nil
		}
		existingTaskKind := psString(valueOr(existing, "task_kind", ""))
		if !isNullOrWhiteSpace(taskKind) && !isNullOrWhiteSpace(existingTaskKind) && taskKind != existingTaskKind {
			return plan, nil
		}
		plan = append(plan, transitionEntry("confirmed", map[string]any{
			"record_id":         id,
			"source_task_id":    sourceTaskID,
			"source_attempt_id": sourceAttemptID,
			"reason":            reason,
		}))
		newConfirmations := psInt(existing["confirmations"]) + 1
		tasks := appendStringUnique(recordStringArray(existing["confirmation_tasks"]), sourceTaskID)
		probe := copyRecord(existing)
		probe["confirmations"] = newConfirmations
		probe["confirmation_tasks"] = tasks
		crossTask := psString(existing["state"]) == "candidate"
		for _, existingTask := range recordStringArray(existing["confirmation_tasks"]) {
			if psString(existingTask) == sourceTaskID {
				crossTask = false
			}
		}
		if psString(existing["state"]) == "candidate" && crossTask {
			plan = append(plan, transitionEntry("shadow", map[string]any{
				"record_id": id,
				"reason":    "first cross-task confirmation",
			}))
			probe["state"] = "shadow"
		}
		if promotionEligible(probe, fingerprints) {
			plan = append(plan, transitionEntry("promoted", map[string]any{
				"record_id": id,
				"reason":    "promotion policy satisfied",
			}))
		}
		return plan, nil
	}
	itemScopeKey, err := scopeKey(projectID, asMapOr(item["scope"]))
	if err != nil {
		return nil, err
	}
	itemAction := asMapOr(item["action"])
	ids := make([]string, 0, len(records))
	for candidateID := range records {
		ids = append(ids, candidateID)
	}
	sort.Strings(ids)
	for _, candidateID := range ids {
		other := records[candidateID]
		if !containsExact(memoryActiveStates, psString(other["state"])) {
			continue
		}
		otherScopeKey, err := scopeKey(projectID, asMapOr(other["scope"]))
		if err != nil || otherScopeKey != itemScopeKey {
			continue
		}
		otherAction := asMapOr(other["action"])
		if psString(valueOr(otherAction, "text", "")) != psString(valueOr(itemAction, "text", "")) {
			continue
		}
		if psString(valueOr(otherAction, "type", "")) == psString(valueOr(itemAction, "type", "")) {
			continue
		}
		plan = append(plan, transitionEntry("contradicted", map[string]any{
			"record_id":         candidateID,
			"source_task_id":    sourceTaskID,
			"source_attempt_id": sourceAttemptID,
			"reason":            "contradicting confirmed evidence",
		}))
		if psString(other["state"]) == "accepted" {
			plan = append(plan, transitionEntry("superseded", map[string]any{
				"record_id":     candidateID,
				"superseded_by": id,
				"reason":        "superseded by contradicting candidate",
			}))
		} else {
			plan = append(plan, transitionEntry("quarantined", map[string]any{
				"record_id": candidateID,
				"reason":    "quarantined by contradicting candidate",
			}))
		}
	}
	plan = append(plan, map[string]any{
		"event_type":        "candidate",
		"record_id":         id,
		"knowledge_class":   valueOr(item, "knowledge_class", nil),
		"risk_class":        valueOr(item, "risk_class", nil),
		"scope":             valueOr(item, "scope", nil),
		"observation":       valueOr(item, "observation", nil),
		"action":            valueOr(item, "action", nil),
		"superseded_by":     nil,
		"source_task_id":    sourceTaskID,
		"source_attempt_id": sourceAttemptID,
		"evidence_refs":     evidenceRefs,
		"reason":            reason,
		"evidence_kind":     evidenceKind,
		"provenance":        provenance,
		"task_kind":         taskKind,
		"error_signature":   errorSignature,
	})
	return plan, nil
}
