package memoryhost

// This file ports the reachable subset of Task.Memory.ps1 for the four
// native-memory operations (bind / extract-attempt / extract-acceptance /
// projection): environment fingerprints, the record state machine, the
// append-only event journal, the derived index, the deterministic bundle
// selection and the bounded prompt renderer. Excluded (unreachable from the
// bridge): Add-BFMemoryFromRecovery, Move-BFMemoryRecord,
// Assert-BFMemoryObservations and Get-BFMemoryFingerprintsFromReplay.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	memoryKnowledgeClasses = []string{"procedural", "diagnostic", "business_rule", "authorization", "test_or_waiver", "architecture", "controller_policy", "model_routing", "runtime_or_external_effect"}
	memoryWorkerClasses    = []string{"procedural", "diagnostic", "architecture"}
	memoryRiskClasses      = []string{"low", "medium", "high"}
	memoryActionTypes      = []string{"recommended", "avoid"}
	memoryEventTypes       = []string{"candidate", "confirmed", "shadow", "promoted", "contradicted", "quarantined", "deprecated", "superseded", "reinstated", "rejected"}
	memoryActiveStates     = []string{"candidate", "shadow", "accepted"}
)

const (
	memoryMaxObservationChars  = 512
	memoryMaxActionChars       = 256
	memoryMaxReasonChars       = 256
	memoryMaxScopePaths        = 16
	memoryMaxScopePathChars    = 512
	memoryMaxObservationItems  = 16
	memoryMaxRecords           = 6
	memoryMaxBundleChars       = 4000
	memoryMaxExcluded          = 32
	memoryMaxEventBytes        = 65536
	memoryMaxEventFiles        = 8192
	memoryMaxRejections        = 8
	memoryMaxWorkingSet        = 24
	memoryMaxEvidenceRefs      = 8
	memoryMaxEvidenceRefChars  = 256
	memorySchemaFingerprintLen = 4
)

var memorySecretPattern = regexp.MustCompile(`(?i)(password|passwd|secret|api[_-]?key|credential|authorization\s*[:=]|bearer\s+[A-Za-z0-9._\-]{8,}|-----BEGIN [A-Z ]*PRIVATE KEY)`)

var memoryStageRelevance = map[string][]string{
	"diagnose":  {"verify", "diagnose"},
	"implement": {"implement", "recover"},
}

var memoryFingerprintFields = []string{"policy", "controller", "version", "schema", "toolchain"}

const (
	memoryTemplateAcceptedSourceOnly         = "accepted-source-only-v1"
	memoryTemplateObservationAcceptedSource  = "A low-risk source-only task completed through the declared controller stages and acceptance receipt."
	memoryTemplateActionAcceptedSource       = "Reuse the bounded controller workflow for matching low-risk source-only tasks."
	memoryTemplateObservationConfirmedFail   = "The controller verifier recorded a declared criterion failure with retained evidence."
	memoryTemplateActionConfirmedFailure     = "Address the retained controller failure evidence before retrying."
	memoryTemplateObservationSuccessfulRecov = "A source-only recovery control-read matched the current source manifest before retry."
	memoryTemplateActionSuccessfulRecovery   = "Repeat the bounded source control-read before retrying a source-only attempt."
)

// nowUTC is the clock used for event timestamps; tests freeze it to compare
// against PowerShell captures byte for byte.
var nowUTC = func() time.Time { return time.Now().UTC() }

// timestampNow mirrors [DateTime]::UtcNow.ToString('o'): seven fractional
// digits and a literal Z.
func timestampNow() string {
	return nowUTC().Format("2006-01-02T15:04:05.0000000Z07:00")
}

// memoryDirectory ports Get-BFMemoryDirectory.
func memoryDirectory(projectPath string) (string, error) {
	root, err := assertSafePath(projectPath)
	if err != nil {
		return "", err
	}
	return assertSafePath(filepath.Join(root, ".bsl-flow", "memory"))
}

// schemaFingerprint ports Get-BFMemorySchemaFingerprint: the hash over the
// four packaged memory schemas. Any missing file yields the empty legacy
// fingerprint.
func schemaFingerprint(packageRoot string) string {
	if packageRoot == "" {
		return ""
	}
	root, err := assertSafePath(packageRoot)
	if err != nil {
		return ""
	}
	entries := make([]any, 0, memorySchemaFingerprintLen)
	for _, name := range []string{"memory-event.schema.json", "memory-index.schema.json", "memory-bundle.schema.json", "context.schema.json"} {
		path := filepath.Join(root, "global", "skills", "1c-task", "schemas", name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			return ""
		}
		digest, err := fileSHA(path)
		if err != nil {
			return ""
		}
		entries = append(entries, map[string]any{"name": name, "sha256": digest})
	}
	digest, err := hashValue(entries)
	if err != nil {
		return ""
	}
	return digest
}

// toolchainFingerprint ports Get-BFMemoryToolchainFingerprint.
func toolchainFingerprint(state map[string]any) string {
	profile := valueOrMap(valueOrMap(state, "request"), "execution_profile")
	if profile == nil {
		return "unbound"
	}
	selected := map[string]any{
		"provider":            psString(valueOr(profile, "provider", "")),
		"executable_sha256":   psString(valueOr(profile, "executable_sha256", "")),
		"sandbox":             valueOrPresent(profile, "sandbox"),
		"toolset":             valueOrPresent(profile, "toolset"),
		"runtime":             valueOrPresent(profile, "runtime"),
		"unica":               valueOrPresent(profile, "unica"),
		"codex_skills_sha256": psString(valueOr(profile, "codex_skills_sha256", "")),
	}
	digest, err := hashValue(selected)
	if err != nil {
		return ""
	}
	return digest
}

// packageIdentity ports Get-BFPackageIdentity from Task.Architecture.ps1.
func packageIdentity(state map[string]any, packageRoot string) (map[string]any, error) {
	version := ""
	if packageRoot != "" {
		versionPath := filepath.Join(packageRoot, "VERSION")
		if info, err := os.Stat(versionPath); err == nil && !info.IsDir() {
			data, err := os.ReadFile(versionPath)
			if err != nil {
				return nil, err
			}
			version = strings.TrimSpace(string(data))
		}
		manifestPath := filepath.Join(packageRoot, "package-manifest.json")
		if info, err := os.Stat(manifestPath); err == nil && !info.IsDir() {
			digest, err := fileSHA(manifestPath)
			if err != nil {
				return nil, err
			}
			return map[string]any{"version": version, "sha256": digest, "source": "manifest"}, nil
		}
	}
	files := asAnyArray(valueOr(state, "policy_files", []any{}))
	hostPattern := regexp.MustCompile(`(?i)bsl-flow\.exe$`)
	entryPattern := regexp.MustCompile(`(?i)Invoke-BSLFlowTask\.ps1$`)
	for _, pattern := range []*regexp.Regexp{hostPattern, entryPattern} {
		source := "host"
		if pattern == entryPattern {
			source = "entrypoint"
		}
		for _, raw := range files {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if pattern.MatchString(psString(valueOr(entry, "path", ""))) {
				return map[string]any{"version": version, "sha256": psString(valueOr(entry, "sha256", "")), "source": source}, nil
			}
		}
	}
	return map[string]any{"version": "", "sha256": "", "source": ""}, nil
}

// memoryFingerprints ports Get-BFMemoryFingerprints.
func memoryFingerprints(state map[string]any, packageRoot string) (map[string]any, error) {
	identity, err := packageIdentity(state, packageRoot)
	if err != nil {
		return nil, err
	}
	policyHash, err := strictString(state, "policy_hash")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"policy":     policyHash,
		"controller": psString(valueOr(identity, "sha256", "")),
		"version":    psString(valueOr(identity, "version", "")),
		"schema":     schemaFingerprint(packageRoot),
		"toolchain":  toolchainFingerprint(state),
	}, nil
}

// taskKindOf ports Get-BFMemoryTaskKind.
func taskKindOf(state map[string]any) string {
	request := valueOrMap(state, "request")
	if request == nil {
		return "legacy"
	}
	criteria := asAnyArray(valueOr(request, "criteria", []any{}))
	kinds := make([]string, 0, len(criteria))
	for _, raw := range criteria {
		kind := psString(valueOr(asMapOr(raw), "kind", ""))
		if kind != "" {
			kinds = append(kinds, kind)
		}
	}
	flags := asAnyArray(valueOr(request, "impact_flags", []any{}))
	flagValues := make([]string, 0, len(flags))
	for _, raw := range flags {
		flag := psString(raw)
		if flag != "" {
			flagValues = append(flagValues, flag)
		}
	}
	mode := psString(valueOr(request, "mode", ""))
	goal := psString(valueOr(request, "analysis_goal", ""))
	kinds = uniqueSortedStrings(kinds)
	flagValues = uniqueSortedStrings(flagValues)
	return boundedText(fmt.Sprintf("%s|%s|criteria=%s|flags=%s", mode, goal, strings.Join(kinds, ","), strings.Join(flagValues, ",")), 256)
}

// errorSignatureOf ports Get-BFMemoryErrorSignature.
func errorSignatureOf(result map[string]any) string {
	if result == nil || psString(valueOr(result, "outcome", "")) != "FAIL" {
		return ""
	}
	proposal := valueOrMap(result, "proposal")
	criterion := psString(valueOr(proposal, "criterion_id", ""))
	kind := psString(valueOr(proposal, "kind", ""))
	category := psString(valueOr(proposal, "category", ""))
	if isNullOrWhiteSpace(criterion) && isNullOrWhiteSpace(kind) && isNullOrWhiteSpace(category) {
		return ""
	}
	signature, err := hashValue(map[string]any{
		"stage":        psString(valueOr(result, "stage", "")),
		"criterion_id": criterion,
		"kind":         kind,
		"category":     category,
	})
	if err != nil {
		return ""
	}
	return signature
}

// errorSignatureForState ports Get-BFMemoryErrorSignatureForState.
func errorSignatureForState(state map[string]any, stage string, pendingFailure map[string]any, pendingFailureHash string) string {
	if stage != "diagnose" {
		return ""
	}
	failureID := psString(valueOr(valueOrMap(state, "repair"), "pending_failure", nil))
	if isNullOrWhiteSpace(failureID) {
		return ""
	}
	if pendingFailure != nil {
		if !isHexHash(pendingFailureHash) {
			return ""
		}
		hash, err := hashValue(pendingFailure)
		if err != nil || hash != pendingFailureHash {
			return ""
		}
		if psInt(valueOr(pendingFailure, "schema_version", 0)) != 1 {
			return ""
		}
		if psString(valueOr(pendingFailure, "task_id", "")) != psString(valueOr(state, "task_id", "")) {
			return ""
		}
		if psString(valueOr(pendingFailure, "attempt_id", "")) != failureID {
			return ""
		}
		if psString(valueOr(pendingFailure, "stage", "")) != "verify" ||
			psString(valueOr(pendingFailure, "outcome", "")) != "FAIL" ||
			psString(valueOr(pendingFailure, "side_effects", "")) != "none" {
			return ""
		}
		return errorSignatureOf(pendingFailure)
	}
	if !isNullOrWhiteSpace(pendingFailureHash) {
		return ""
	}
	projectPath, err := strictString(state, "project_path")
	if err != nil {
		return ""
	}
	taskID, err := strictString(state, "task_id")
	if err != nil {
		return ""
	}
	path := filepath.Join(projectPath, ".bsl-flow", "tasks", taskID, "attempts", failureID, "result.json")
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return ""
	}
	result, err := readBFJSON(path)
	if err != nil {
		return ""
	}
	return errorSignatureOf(result)
}

// boundedText ports Get-BFMemoryBoundedText.
func boundedText(text string, limit int) string {
	if utf16Length(text) > limit {
		return trimEndUnicode(substringUTF16(text, limit))
	}
	return text
}

// isSecretLike ports Test-BFMemorySecretLike.
func isSecretLike(text string) bool {
	if isNullOrWhiteSpace(text) {
		return true
	}
	return memorySecretPattern.MatchString(text)
}

// sameFingerprints ports Test-BFMemorySameFingerprints.
func sameFingerprints(left, right map[string]any) bool {
	if left == nil || right == nil {
		return false
	}
	for _, name := range memoryFingerprintFields {
		leftPresent, rightPresent := false, false
		if _, ok := left[name]; ok {
			leftPresent = true
		}
		if _, ok := right[name]; ok {
			rightPresent = true
		}
		if !leftPresent || !rightPresent {
			return false
		}
		leftValue := psString(left[name])
		rightValue := psString(right[name])
		if isNullOrWhiteSpace(leftValue) || isNullOrWhiteSpace(rightValue) || leftValue != rightValue {
			return false
		}
	}
	return true
}

// convertScope ports ConvertTo-BFMemoryScope.
func convertScope(stage string, paths []string) map[string]any {
	normalized := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		value := strings.Trim(strings.ReplaceAll(path, `\`, "/"), "/")
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return map[string]any{"stage": stage, "paths": toAnyArray(normalized)}
}

// recordID ports Get-BFMemoryRecordId.
func recordID(projectID string, scope, action, fingerprints map[string]any, identityVariant string) (string, error) {
	identity := map[string]any{
		"project_id":   projectID,
		"scope":        scope,
		"action":       action,
		"fingerprints": fingerprints,
	}
	if !isNullOrWhiteSpace(identityVariant) {
		identity["identity_variant"] = identityVariant
	}
	return hashValue(identity)
}

// scopeKey ports Get-BFMemoryScopeKey.
func scopeKey(projectID string, scope map[string]any) (string, error) {
	return hashValue(map[string]any{"project_id": projectID, "scope": scope})
}

// pathOverlap ports Test-BFMemoryPathOverlap.
func pathOverlap(left, right string) bool {
	leftPath := strings.Trim(strings.ReplaceAll(left, `\`, "/"), "/")
	rightPath := strings.Trim(strings.ReplaceAll(right, `\`, "/"), "/")
	if leftPath == "." || rightPath == "." || isNullOrWhiteSpace(leftPath) || isNullOrWhiteSpace(rightPath) {
		return true
	}
	return leftPath == rightPath ||
		strings.HasPrefix(leftPath, rightPath+"/") ||
		strings.HasPrefix(rightPath, leftPath+"/")
}

// convertEvidenceRef ports ConvertTo-BFMemoryEvidenceRef (nil means reject).
func convertEvidenceRef(reference any) map[string]any {
	object, ok := reference.(map[string]any)
	if !ok {
		return nil
	}
	allowed := []string{"kind", "task_id", "attempt_id", "sha256", "policy", "controller", "version"}
	for key := range object {
		if !containsExact(allowed, key) {
			return nil
		}
	}
	kind := psString(valueOr(object, "kind", ""))
	if isNullOrWhiteSpace(kind) || utf16Length(kind) > 64 || isSecretLike(kind) {
		return nil
	}
	result := map[string]any{"kind": kind}
	for _, name := range []string{"task_id", "attempt_id", "sha256", "policy", "controller", "version"} {
		if _, present := object[name]; !present {
			continue
		}
		value := object[name]
		if value == nil {
			result[name] = nil
			continue
		}
		text, isString := value.(string)
		if !isString || utf16Length(text) > memoryMaxEvidenceRefChars || isSecretLike(text) {
			return nil
		}
		result[name] = text
	}
	return result
}

// pickEvidenceRef ports Get-BFMemoryEvidenceRef.
func pickEvidenceRef(references any) map[string]any {
	switch typed := references.(type) {
	case []any:
		for _, reference := range typed {
			if reference == nil {
				continue
			}
			if converted := convertEvidenceRef(reference); converted != nil {
				return converted
			}
		}
	case map[string]any:
		return convertEvidenceRef(typed)
	}
	return nil
}

// formatEvidenceRef ports Format-BFMemoryEvidenceRef.
func formatEvidenceRef(reference any) string {
	converted := convertEvidenceRef(reference)
	if converted == nil {
		return ""
	}
	parts := []string{"kind=" + psString(converted["kind"])}
	for _, name := range []string{"sha256", "task_id", "attempt_id", "policy", "controller", "version"} {
		value := psString(converted[name])
		if converted[name] != nil && !isNullOrWhiteSpace(value) {
			parts = append(parts, name+"="+value)
		}
	}
	return boundedText(strings.Join(parts, "; "), 768)
}

// templateScopePaths ports Get-BFMemoryTemplateScopePaths.
func templateScopePaths(state map[string]any) ([]string, bool) {
	request := valueOrMap(state, "request")
	paths := asAnyArray(valueOr(request, "source_paths", []any{"."}))
	if len(paths) == 0 {
		paths = []any{"."}
	}
	result := make([]string, 0, len(paths))
	for _, raw := range paths {
		path := psString(raw)
		if isNullOrWhiteSpace(path) || utf16Length(path) > memoryMaxScopePathChars {
			return nil, false
		}
		if err := assertRelativePath(path); err != nil {
			return nil, false
		}
		result = append(result, path)
	}
	return result, true
}

// isLowRiskSourceOnly ports Test-BFMemoryLowRiskSourceOnlyTask.
func isLowRiskSourceOnly(state map[string]any) bool {
	request := valueOrMap(state, "request")
	classification := valueOrMap(state, "classification")
	if request == nil || classification == nil {
		return false
	}
	if psString(valueOr(request, "mode", "")) != "implement" || psString(valueOr(classification, "risk", "")) != "low" {
		return false
	}
	if len(asAnyArray(valueOr(classification, "impact_flags", []any{}))) > 0 || len(asAnyArray(valueOr(request, "impact_flags", []any{}))) > 0 {
		return false
	}
	criteria := asAnyArray(valueOr(request, "criteria", []any{}))
	if len(criteria) == 0 {
		return false
	}
	for _, raw := range criteria {
		if psString(valueOr(asMapOr(raw), "kind", "")) != "file_assertion" {
			return false
		}
	}
	return true
}

// acceptedTemplateItem ports Get-BFMemoryAcceptedTemplateItem.
func acceptedTemplateItem(state, receipt map[string]any) (map[string]any, bool) {
	if receipt == nil || psString(valueOr(receipt, "verdict", "")) != "PASS" {
		return nil, false
	}
	if !isLowRiskSourceOnly(state) {
		return nil, false
	}
	gates := asAnyArray(valueOr(receipt, "gates", []any{}))
	hasImplement, hasVerify := false, false
	for _, raw := range gates {
		gate := asMapOr(raw)
		stage := psString(valueOr(gate, "stage", ""))
		if stage == "implement" {
			hasImplement = true
		}
		if stage == "verify" {
			hasVerify = true
		}
	}
	if !hasImplement || !hasVerify {
		return nil, false
	}
	paths, ok := templateScopePaths(state)
	if !ok {
		return nil, false
	}
	return map[string]any{
		"template_id":     memoryTemplateAcceptedSourceOnly,
		"evidence_kind":   "accepted-result",
		"provenance":      "controller-template",
		"task_kind":       taskKindOf(state),
		"error_signature": "",
		"item": map[string]any{
			"scope":           convertScope("implement", paths),
			"observation":     memoryTemplateObservationAcceptedSource,
			"action":          map[string]any{"type": "recommended", "text": memoryTemplateActionAcceptedSource},
			"knowledge_class": "procedural",
			"risk_class":      "low",
		},
	}, true
}

// checkedItem is the ConvertTo-BFMemoryItem result.
type checkedItem struct {
	ok     bool
	reason string
	item   map[string]any
}

// convertItem ports ConvertTo-BFMemoryItem.
func convertItem(item any, stage string) checkedItem {
	reject := func(reason string) checkedItem { return checkedItem{ok: false, reason: reason} }
	object, ok := item.(map[string]any)
	if !ok {
		return reject("invalid-shape")
	}
	required := []string{"scope", "observation", "action_type", "action", "knowledge_class", "risk_class"}
	for _, key := range required {
		if _, present := object[key]; !present {
			return reject("invalid-shape")
		}
	}
	for key := range object {
		if !containsExact(required, key) {
			return reject("invalid-shape")
		}
	}
	scopePaths, isPathArray := object["scope"].([]any)
	if !isPathArray || len(scopePaths) == 0 {
		return reject("invalid-shape")
	}
	if len(scopePaths) > memoryMaxScopePaths {
		return reject("scope-too-large")
	}
	pathValues := make([]string, 0, len(scopePaths))
	forbidden := regexp.MustCompile(`(^|/)\.(bsl-flow|bsl-flow-worker|git)(/|$)`)
	for _, raw := range scopePaths {
		path := psString(raw)
		if isNullOrWhiteSpace(path) {
			return reject("invalid-shape")
		}
		if utf16Length(path) > memoryMaxScopePathChars {
			return reject("scope-path-too-long")
		}
		if err := assertRelativePath(path); err != nil {
			return reject("forbidden-scope-path")
		}
		if forbidden.MatchString(strings.ReplaceAll(path, `\`, "/")) {
			return reject("forbidden-scope-path")
		}
		pathValues = append(pathValues, path)
	}
	observation := psString(object["observation"])
	if isNullOrWhiteSpace(observation) || utf16Length(observation) > memoryMaxObservationChars {
		return reject("observation-too-long")
	}
	if isSecretLike(observation) {
		return reject("secret-like-content")
	}
	actionText := psString(object["action"])
	if isNullOrWhiteSpace(actionText) || utf16Length(actionText) > memoryMaxActionChars {
		return reject("action-too-long")
	}
	if isSecretLike(actionText) {
		return reject("secret-like-content")
	}
	if !containsExact(memoryActionTypes, psString(object["action_type"])) {
		return reject("invalid-action-type")
	}
	if !containsExact(memoryWorkerClasses, psString(object["knowledge_class"])) {
		return reject("class-not-proposable")
	}
	if !containsExact(memoryRiskClasses, psString(object["risk_class"])) {
		return reject("invalid-risk-class")
	}
	return checkedItem{ok: true, item: map[string]any{
		"scope":           convertScope(stage, pathValues),
		"observation":     observation,
		"action":          map[string]any{"type": psString(object["action_type"]), "text": actionText},
		"knowledge_class": psString(object["knowledge_class"]),
		"risk_class":      psString(object["risk_class"]),
	}}
}

// Small map/slice accessors mirroring Get-BFValue's null-tolerant lookups.

func valueOr(object map[string]any, name string, fallback any) any {
	if object == nil {
		return fallback
	}
	if value, present := object[name]; present {
		return value
	}
	return fallback
}

// valueOrPresent yields the member when present (including explicit nulls)
// and nil otherwise, mirroring Get-BFValue without a default.
func valueOrPresent(object map[string]any, name string) any {
	if object == nil {
		return nil
	}
	if value, present := object[name]; present {
		return value
	}
	return nil
}

func valueOrMap(object map[string]any, name string) map[string]any {
	if object == nil {
		return nil
	}
	if value, present := object[name]; present {
		if typed, ok := value.(map[string]any); ok {
			return typed
		}
	}
	return nil
}

func asMapOr(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return nil
}

func asAnyArray(value any) []any {
	if typed, ok := value.([]any); ok {
		return typed
	}
	return nil
}

func toAnyArray(values []string) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func uniqueSortedStrings(values []string) []string {
	sorted := append([]string(nil), values...)
	sort.Strings(sorted)
	result := sorted[:0]
	for index, value := range sorted {
		if index == 0 || sorted[index-1] != value {
			result = append(result, value)
		}
	}
	return result
}
