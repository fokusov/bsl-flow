package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var controllerExecutionFields = map[string]bool{
	"project_path": true, "worker_path": true, "baseline": true,
	"request": true, "request_hash": true, "intent_hash": true,
	"policy_hash": true, "intent_revision": true,
	"authorization_revision": true, "correction_rounds": true,
	"policy_files": true, "policy_rules": true, "classification": true,
	"status": true, "stage": true, "active_attempt": true,
	"unresolved_effect": true, "attempts": true, "evidence": true,
	"events": true, "question": true, "blockers": true,
	"acceptances": true, "repair": true, "engine": true,
}

var controllerRequiredFields = []string{
	"project_path", "worker_path", "baseline", "request", "request_hash",
	"intent_hash", "policy_hash", "intent_revision", "authorization_revision",
	"correction_rounds", "policy_files", "policy_rules", "classification",
	"status", "stage", "active_attempt", "unresolved_effect", "attempts",
	"evidence", "events", "question", "blockers", "acceptances", "engine",
}

var engineFields = map[string]bool{
	"name": true, "contract_version": true, "provider": true,
	"host_sha256": true, "provider_sha256": true, "asset_manifest_sha256": true,
}

var providerArtifactKinds = map[string]bool{
	"raw": true, "process": true, "model": true, "verification": true,
	"review": true, "budget": true, "failure": true,
}

var nativeUnsupportedKinds = map[string]bool{
	"integration": true, "ui": true, "external_artifact": true,
}

// controllerPayload returns the nested controller object for an activated
// repository card.  Legacy v1 states are returned unchanged so projections
// remain compatible with read-only legacy discovery.
func controllerPayload(state map[string]any) map[string]any {
	if payload, ok := state["controller"].(map[string]any); ok {
		return payload
	}
	return state
}

func isControllerState(state map[string]any) bool {
	lifecycle, _ := asString(state["lifecycle"])
	_, present := state["controller"]
	return lifecycle == "controller" && present
}

func validateControllerState(value any, taskID string) error {
	return validateControllerPayload(value, taskID, false)
}

func validateControllerPayload(value any, taskID string, historical bool) error {
	payload, ok := value.(map[string]any)
	if !ok || payload == nil {
		return errors.New("controller must be an object")
	}
	for field := range payload {
		if !controllerExecutionFields[field] {
			return fmt.Errorf("controller contains unsupported field %s", field)
		}
	}
	for _, field := range controllerRequiredFields {
		if _, present := payload[field]; !present {
			return fmt.Errorf("controller is missing required field %s", field)
		}
	}
	view := make(map[string]any, len(payload)+6)
	for field, item := range payload {
		if field == "engine" {
			continue
		}
		view[field] = item
	}
	// v1's closed execution schema uses outer-journal identity/timestamps. The
	// nested payload deliberately has no second revision counter.
	outerFields := map[string]any{
		"schema_version":  int64(1),
		"task_id":         taskID,
		"revision":        int64(1),
		"previous_sha256": nil,
		"created_at":      nowUTC(),
		"updated_at":      nowUTC(),
	}
	for field, item := range outerFields {
		view[field] = item
	}
	if err := validateV1State(view); err != nil {
		return err
	}
	if !historical || payload["engine"] != nil {
		if err := validateEngine(payload["engine"]); err != nil {
			return err
		}
	}
	if classification, ok := payload["classification"].(map[string]any); ok && !historical {
		if complexity, ok := asString(classification["complexity"]); !ok || (complexity != "S" && complexity != "M" && complexity != "L") {
			return errors.New("controller classification.complexity is invalid")
		}
		if risk, ok := asString(classification["risk"]); !ok || (risk != "low" && risk != "medium" && risk != "high") {
			return errors.New("controller classification.risk is invalid")
		}
		if flags := anyItems(classification["impact_flags"]); flags == nil {
			return errors.New("controller classification.impact_flags must be an array")
		} else if err := validateV1ImpactFlags(flags); err != nil {
			return err
		}
	}
	if status, _ := asString(payload["status"]); status == "running" && payload["active_attempt"] == nil {
		return errors.New("running controller state requires active_attempt")
	}
	if status, _ := asString(payload["status"]); status != "running" && status != "cancelled" {
		if active, present := payload["active_attempt"]; present && active != nil {
			return errors.New("non-running controller state cannot retain active_attempt")
		}
	}
	if active, ok := asString(payload["active_attempt"]); ok && active != "" {
		attempts := anyItems(payload["attempts"])
		found := false
		for _, item := range attempts {
			if id, ok := asString(item); ok && id == active {
				found = true
			}
			if itemMap, ok := item.(map[string]any); ok {
				if id, ok := asString(itemMap["attempt_id"]); ok && id == active {
					found = true
				}
			}
		}
		if !found {
			return errors.New("active_attempt is not registered in attempts")
		}
	}
	return nil
}

func validateEngine(value any) error {
	engine, ok := value.(map[string]any)
	if !ok || engine == nil {
		return errors.New("controller engine must be an object")
	}
	if len(engine) != len(engineFields) {
		return errors.New("controller engine contains unsupported fields")
	}
	for field := range engine {
		if !engineFields[field] {
			return fmt.Errorf("controller engine contains unsupported field %s", field)
		}
	}
	name, ok := asString(engine["name"])
	if !ok || name != "native" {
		return errors.New("controller engine.name must be native")
	}
	version, ok := asInt(engine["contract_version"])
	if !ok || version != 1 {
		return errors.New("controller engine.contract_version must be 1")
	}
	provider, ok := asString(engine["provider"])
	if !ok || provider != NativeProviderContract {
		return errors.New("controller engine.provider is unsupported")
	}
	for _, field := range []string{"host_sha256", "provider_sha256", "asset_manifest_sha256"} {
		hash, ok := asString(engine[field])
		if !ok || !isSHA256(hash) {
			return fmt.Errorf("controller engine.%s must be a lowercase SHA-256", field)
		}
	}
	return nil
}

func engineMap(engine EngineIdentity) map[string]any {
	return map[string]any{
		"name":                  engine.Name,
		"contract_version":      engine.ContractVersion,
		"provider":              engine.Provider,
		"host_sha256":           engine.HostSHA256,
		"provider_sha256":       engine.ProviderSHA256,
		"asset_manifest_sha256": engine.AssetManifestSHA256,
	}
}

func providerContract(engine EngineIdentity) ProviderContractIdentity {
	return ProviderContractIdentity{
		Name:                engine.Provider,
		Version:             engine.ContractVersion,
		HostSHA256:          engine.HostSHA256,
		ProviderSHA256:      engine.ProviderSHA256,
		AssetManifestSHA256: engine.AssetManifestSHA256,
	}
}

func validateEngineIdentity(engine EngineIdentity) error {
	return validateEngine(engineMap(engine))
}

func equalEngine(left, right EngineIdentity) bool {
	return left.Name == right.Name && left.ContractVersion == right.ContractVersion &&
		left.Provider == right.Provider && left.HostSHA256 == right.HostSHA256 &&
		left.ProviderSHA256 == right.ProviderSHA256 && left.AssetManifestSHA256 == right.AssetManifestSHA256
}

func resolveControllerHost(host *ControllerHost) (Provider, EngineIdentity, error) {
	if host == nil {
		return nil, EngineIdentity{}, blocked("native controller capability is unavailable")
	}
	if host.Resolve != nil && (host.Provider == nil || validateEngineIdentity(host.Engine) != nil) {
		provider, engine, err := host.Resolve()
		if err != nil {
			return nil, EngineIdentity{}, blocked("native controller capability unavailable: %v", err)
		}
		if provider == nil {
			return nil, EngineIdentity{}, blocked("native controller provider is unavailable")
		}
		if err := validateEngineIdentity(engine); err != nil {
			return nil, EngineIdentity{}, blocked("native controller engine identity is invalid: %v", err)
		}
		return provider, engine, nil
	}
	if host.Provider == nil {
		return nil, EngineIdentity{}, blocked("native controller provider is unavailable")
	}
	if err := validateEngineIdentity(host.Engine); err != nil {
		return nil, EngineIdentity{}, blocked("native controller engine identity is invalid: %v", err)
	}
	return host.Provider, host.Engine, nil
}

func controllerStateView(outer, payload map[string]any) map[string]any {
	view := map[string]any{}
	for field, value := range payload {
		if field != "engine" {
			view[field] = value
		}
	}
	for _, field := range []string{"schema_version", "task_id", "revision", "previous_sha256", "created_at", "updated_at"} {
		view[field] = outer[field]
	}
	view["schema_version"] = int64(1)
	return view
}

func cloneObject(value map[string]any) (map[string]any, error) {
	data, err := Canonical(value)
	if err != nil {
		return nil, err
	}
	return DecodeObject(data)
}

func requestIntentHash(request map[string]any) (string, error) {
	intent := map[string]any{}
	for _, field := range []string{"prompt", "analysis_goal", "criteria", "complexity", "risk", "impact_flags", "source_paths"} {
		if value, present := request[field]; present {
			intent[field] = value
		} else if field == "source_paths" {
			intent[field] = []any{"."}
		}
	}
	if repairs, ok := asInt(request["max_source_repairs"]); ok && repairs > 0 {
		intent["max_source_repairs"] = repairs
	}
	if requirements, present := request["requirements"]; present {
		intent["requirements"] = requirements
	}
	if profile, present := request["execution_profile"]; present {
		intent["execution_profile"] = profile
		intent["models"] = request["models"]
	}
	return Hash(intent)
}

func requestLimits(request map[string]any) (maxAttempts int64, timeoutSeconds int64, maxRepairs int64, err error) {
	maxAttempts, timeoutSeconds, maxRepairs = 16, 1800, 0
	if value, present := request["max_attempts"]; present {
		if maxAttempts, err = asRequiredInt(value, "max_attempts"); err != nil {
			return
		}
	}
	if value, present := request["timeout_seconds"]; present {
		if timeoutSeconds, err = asRequiredInt(value, "timeout_seconds"); err != nil {
			return
		}
	}
	if value, present := request["max_source_repairs"]; present {
		if maxRepairs, err = asRequiredInt(value, "max_source_repairs"); err != nil {
			return
		}
	}
	if maxAttempts < 1 || maxAttempts > 64 {
		err = invalid("max_attempts must be between 1 and 64")
	} else if timeoutSeconds < 1 || timeoutSeconds > 14400 {
		err = invalid("timeout_seconds must be between 1 and 14400")
	} else if maxRepairs < 0 || maxRepairs > 3 {
		err = invalid("max_source_repairs must be between 0 and 3")
	}
	return
}

func asRequiredInt(value any, name string) (int64, error) {
	parsed, ok := asInt(value)
	if !ok {
		return 0, invalid("%s must be an integer", name)
	}
	return parsed, nil
}

func validateRelativeNativePath(value string, allowDot bool) error {
	if value == "" || strings.ContainsAny(value, "\x00\r\n:*?\"<>|") || filepath.IsAbs(value) || strings.HasPrefix(value, `\\`) {
		return errors.New("path must be relative")
	}
	normalized := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if normalized == ".." || strings.HasPrefix(normalized, "../") || strings.Contains(normalized, "/../") {
		return errors.New("path escapes its root")
	}
	if !allowDot && normalized == "." {
		return errors.New("path must identify a file")
	}
	return nil
}

var nativeToolPattern = regexp.MustCompile(`^unica\.(?:(cf|cfe|meta|form|skd|dcs|mxl|role|subsystem|interface|template|code)\.(info|validate|diff|search|diagnostics|add|edit|remove|compile|init|borrow|patch_method)|project\.map|code\.patch)$`)
var nativeRuntimeVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
var nativePackageNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var nativeModelPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

func nativeObject(value any, required, optional []string, name string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, invalid("%s must be an object", name)
	}
	allowed := map[string]bool{}
	for _, field := range append(append([]string{}, required...), optional...) {
		allowed[field] = true
	}
	for field := range object {
		if !allowed[field] {
			return nil, invalid("%s contains unsupported field %s", name, field)
		}
	}
	for _, field := range required {
		if _, present := object[field]; !present {
			return nil, invalid("%s.%s is required", name, field)
		}
	}
	return object, nil
}

func nativeArray(value any, name string, nonEmpty bool) ([]any, error) {
	items, ok := nativeItems(value)
	if !ok || (nonEmpty && len(items) == 0) {
		if nonEmpty {
			return nil, invalid("%s must be a non-empty array", name)
		}
		return nil, invalid("%s must be an array", name)
	}
	return items, nil
}

func nativeItems(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	case []string:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			result = append(result, item)
		}
		return result, true
	default:
		return nil, false
	}
}

func validateNativeText(value any, name string) error {
	text, ok := asString(value)
	if !ok || strings.TrimSpace(text) == "" || len(text) > 262144 {
		return invalid("%s must be a non-empty string", name)
	}
	return nil
}

func validateNativeAbsolutePath(value any, name string) error {
	text, ok := asString(value)
	if !ok || strings.TrimSpace(text) == "" || strings.ContainsAny(text, "\x00\r\n") || !filepath.IsAbs(text) {
		return invalid("%s must be an absolute path", name)
	}
	return nil
}

func validateNativeExecutable(value any, name string) error {
	if err := validateNativeAbsolutePath(value, name); err != nil {
		return err
	}
	text := asStringOr(value)
	if filepath.Ext(text) != ".exe" {
		return invalid("%s must be an absolute .exe path", name)
	}
	return nil
}

func validateNativeHash(value any, name string) error {
	text, ok := asString(value)
	if !ok || !isSHA256(text) {
		return invalid("%s must be a lowercase SHA-256", name)
	}
	return nil
}

func validateNativeStringArray(value any, name string, nonEmpty bool, unique bool) ([]any, error) {
	items, err := nativeArray(value, name, nonEmpty)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, raw := range items {
		text, ok := asString(raw)
		if !ok || strings.ContainsAny(text, "\x00\r\n") {
			return nil, invalid("%s must contain single-line strings", name)
		}
		if unique {
			if seen[text] {
				return nil, invalid("%s must contain unique values", name)
			}
			seen[text] = true
		}
	}
	return items, nil
}

func finiteNativeNumber(value any) (float64, bool) {
	var parsed float64
	switch typed := value.(type) {
	case json.Number:
		var err error
		parsed, err = typed.Float64()
		if err != nil {
			return 0, false
		}
	case float64:
		parsed = typed
	case float32:
		parsed = float64(typed)
	case int:
		parsed = float64(typed)
	case int8:
		parsed = float64(typed)
	case int16:
		parsed = float64(typed)
	case int32:
		parsed = float64(typed)
	case int64:
		parsed = float64(typed)
	case uint:
		parsed = float64(typed)
	case uint8:
		parsed = float64(typed)
	case uint16:
		parsed = float64(typed)
	case uint32:
		parsed = float64(typed)
	case uint64:
		parsed = float64(typed)
	default:
		return 0, false
	}
	return parsed, !math.IsNaN(parsed) && !math.IsInf(parsed, 0) && parsed >= 0
}

func validateNativeBudget(value any) error {
	budget, err := nativeObject(value, []string{"currency", "limit", "reservation"}, nil, "budget")
	if err != nil {
		return err
	}
	if asStringOr(budget["currency"]) != "USD" {
		return invalid("budget.currency must be USD")
	}
	if budget["limit"] != nil {
		if _, ok := finiteNativeNumber(budget["limit"]); !ok {
			return invalid("budget.limit must be a non-negative finite number or null")
		}
	}
	reservation, ok := finiteNativeNumber(budget["reservation"])
	if !ok {
		return invalid("budget.reservation must be a non-negative finite number")
	}
	if budget["limit"] == nil && reservation != 0 {
		return invalid("budget.reservation must be zero when budget.limit is null")
	}
	return nil
}

func validateNativeExecutionProfile(value any) error {
	profile, err := nativeObject(value,
		[]string{"provider", "executable", "executable_sha256", "sandbox", "toolset", "denied_read_roots"},
		[]string{"unica", "codex_skills_sha256", "runtime"}, "execution_profile")
	if err != nil {
		return err
	}
	provider, ok := asString(profile["provider"])
	if !ok || (provider != "codex" && provider != "opencode") {
		return invalid("execution_profile.provider is unsupported")
	}
	if err := validateNativeExecutable(profile["executable"], "execution_profile.executable"); err != nil {
		return err
	}
	if err := validateNativeHash(profile["executable_sha256"], "execution_profile.executable_sha256"); err != nil {
		return err
	}
	if provider == "codex" {
		if err := validateNativeHash(profile["codex_skills_sha256"], "execution_profile.codex_skills_sha256"); err != nil {
			return err
		}
	} else if _, present := profile["codex_skills_sha256"]; present {
		return invalid("OpenCode execution profile cannot contain codex_skills_sha256")
	}
	sandbox, err := nativeObject(profile["sandbox"], []string{"executable", "sha256"}, nil, "execution_profile.sandbox")
	if err != nil {
		return err
	}
	if err := validateNativeExecutable(sandbox["executable"], "execution_profile.sandbox.executable"); err != nil {
		return err
	}
	if err := validateNativeHash(sandbox["sha256"], "execution_profile.sandbox.sha256"); err != nil {
		return err
	}
	toolset, err := nativeObject(profile["toolset"], []string{"name", "root", "sha256"}, nil, "execution_profile.toolset")
	if err != nil {
		return err
	}
	toolsetName, ok := asString(toolset["name"])
	if !ok || (toolsetName != "unica" && toolsetName != "cc-1c-skills") {
		return invalid("execution_profile.toolset.name is unsupported")
	}
	if err := validateNativeAbsolutePath(toolset["root"], "execution_profile.toolset.root"); err != nil {
		return err
	}
	if err := validateNativeHash(toolset["sha256"], "execution_profile.toolset.sha256"); err != nil {
		return err
	}
	denied, err := validateNativeStringArray(profile["denied_read_roots"], "execution_profile.denied_read_roots", true, false)
	if err != nil {
		return err
	}
	for _, raw := range denied {
		if err := validateNativeAbsolutePath(raw, "execution_profile.denied_read_roots"); err != nil {
			return err
		}
	}
	if toolsetName == "unica" {
		unica, err := nativeObject(profile["unica"], []string{"plugin_root", "bootstrap_sha256", "manifest_sha256", "runtime_cache", "allowed_tools"}, nil, "execution_profile.unica")
		if err != nil {
			return err
		}
		for _, field := range []string{"plugin_root", "runtime_cache"} {
			if err := validateNativeAbsolutePath(unica[field], "execution_profile.unica."+field); err != nil {
				return err
			}
		}
		for _, field := range []string{"bootstrap_sha256", "manifest_sha256"} {
			if err := validateNativeHash(unica[field], "execution_profile.unica."+field); err != nil {
				return err
			}
		}
		tools, err := validateNativeStringArray(unica["allowed_tools"], "execution_profile.unica.allowed_tools", true, false)
		if err != nil {
			return err
		}
		for _, raw := range tools {
			if !nativeToolPattern.MatchString(asStringOr(raw)) {
				return invalid("execution_profile.unica.allowed_tools contains an unsupported tool")
			}
		}
		if _, present := profile["runtime"]; present {
			return invalid("Unica execution profile cannot contain runtime")
		}
	} else {
		if _, present := profile["unica"]; present {
			return invalid("cc-1c-skills execution profile cannot contain unica")
		}
		runtime, err := nativeObject(profile["runtime"], []string{"executable", "sha256", "version", "packages"}, nil, "execution_profile.runtime")
		if err != nil {
			return err
		}
		if err := validateNativeExecutable(runtime["executable"], "execution_profile.runtime.executable"); err != nil {
			return err
		}
		if err := validateNativeHash(runtime["sha256"], "execution_profile.runtime.sha256"); err != nil {
			return err
		}
		version, ok := asString(runtime["version"])
		if !ok || !nativeRuntimeVersionPattern.MatchString(version) {
			return invalid("execution_profile.runtime.version is invalid")
		}
		packages, err := nativeArray(runtime["packages"], "execution_profile.runtime.packages", true)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		hasLXML := false
		for _, raw := range packages {
			pkg, err := nativeObject(raw, []string{"name", "version"}, nil, "execution_profile.runtime.package")
			if err != nil {
				return err
			}
			name, ok := asString(pkg["name"])
			if !ok || !nativePackageNamePattern.MatchString(name) || seen[name] {
				return invalid("execution_profile.runtime package names must be safe and unique")
			}
			if err := validateNativeText(pkg["version"], "execution_profile.runtime.package.version"); err != nil {
				return err
			}
			seen[name] = true
			if name == "lxml" {
				hasLXML = true
			}
		}
		if !hasLXML {
			return invalid("execution_profile.runtime must declare lxml")
		}
	}
	return nil
}

func validateNativeCriteria(request map[string]any) error {
	criteria, err := nativeArray(request["criteria"], "criteria", false)
	if err != nil {
		return err
	}
	criterionIDs := map[string]bool{}
	for _, raw := range criteria {
		criterion, err := nativeObject(raw, []string{"id", "observation", "kind"}, []string{"path", "contains", "executable", "arguments", "report", "expected_tests", "target", "profile", "retry_safe", "protected_paths", "native_1c"}, "criterion")
		if err != nil {
			return err
		}
		id, ok := asString(criterion["id"])
		if !ok || !nativePackageNamePattern.MatchString(id) || criterionIDs[id] {
			return invalid("criterion ids must be safe and unique")
		}
		criterionIDs[id] = true
		if err := validateNativeText(criterion["observation"], "criterion.observation"); err != nil {
			return err
		}
		kind, ok := asString(criterion["kind"])
		if !ok || !v1CriterionKinds[kind] {
			return invalid("criterion.kind is invalid")
		}
		for _, field := range []string{"path", "contains", "executable", "report", "target"} {
			if value, present := criterion[field]; present {
				if _, ok := asString(value); !ok {
					return invalid("criterion.%s must be a string", field)
				}
			}
		}
		if kind == "file_assertion" {
			if err := validateRelativeNativePath(asStringOr(criterion["path"]), false); err != nil {
				return invalid("invalid criterion path: %v", err)
			}
			if err := validateNativeText(criterion["contains"], "criterion.contains"); err != nil {
				return err
			}
		} else if kind != "external_artifact" {
			if err := validateNativeAbsolutePath(criterion["executable"], "criterion.executable"); err != nil {
				return err
			}
			if _, err := validateNativeStringArray(criterion["arguments"], "criterion.arguments", false, false); err != nil {
				return err
			}
			if err := validateRelativeNativePath(asStringOr(criterion["report"]), false); err != nil {
				return invalid("invalid criterion report: %v", err)
			}
			if !strings.HasPrefix(strings.ToLower(filepath.ToSlash(asStringOr(criterion["report"]))), ".bsl-flow-worker/") {
				return invalid("criterion.report must be under .bsl-flow-worker")
			}
			tests, err := validateNativeStringArray(criterion["expected_tests"], "criterion.expected_tests", true, true)
			if err != nil {
				return err
			}
			_ = tests
			if kind == "integration" || kind == "ui" {
				if err := validateNativeText(criterion["target"], "criterion.target"); err != nil {
					return err
				}
			}
		}
		if retry, present := criterion["retry_safe"]; present {
			if _, ok := asBool(retry); !ok {
				return invalid("criterion.retry_safe must be boolean")
			}
		}
		if protected, present := criterion["protected_paths"]; present {
			items, err := validateNativeStringArray(protected, "criterion.protected_paths", true, false)
			if err != nil {
				return err
			}
			for _, rawPath := range items {
				path := asStringOr(rawPath)
				if err := validateRelativeNativePath(path, false); err != nil || strings.HasPrefix(strings.ToLower(filepath.ToSlash(path)), ".bsl-flow/") || strings.HasPrefix(strings.ToLower(filepath.ToSlash(path)), ".git/") {
					return invalid("criterion.protected_paths contains an unsafe path")
				}
			}
		}
		if native, present := criterion["native_1c"]; present && native != nil {
			return blocked("native_1c criteria require an unsupported runtime capability")
		}
		if nativeUnsupportedKinds[kind] {
			return blocked("criterion kind %s requires an unsupported native capability", kind)
		}
	}
	return nil
}

func validateNativeRequirements(request map[string]any) error {
	requirements, present := request["requirements"]
	if !present {
		return nil
	}
	items, err := nativeArray(requirements, "requirements", true)
	if err != nil {
		return err
	}
	criteria, _ := nativeItems(request["criteria"])
	criterionIDs := map[string]bool{}
	for _, raw := range criteria {
		criterion, _ := raw.(map[string]any)
		criterionIDs[asStringOr(criterion["id"])] = true
	}
	mapped := map[string]bool{}
	requirementIDs := map[string]bool{}
	for _, raw := range items {
		requirement, err := nativeObject(raw, []string{"id", "text", "criterion_ids"}, nil, "requirement")
		if err != nil {
			return err
		}
		id := asStringOr(requirement["id"])
		if !nativePackageNamePattern.MatchString(id) || requirementIDs[id] {
			return invalid("requirement ids must be safe and unique")
		}
		requirementIDs[id] = true
		if err := validateNativeText(requirement["text"], "requirement.text"); err != nil {
			return err
		}
		criterionIDsForRequirement, err := validateNativeStringArray(requirement["criterion_ids"], "requirement.criterion_ids", true, true)
		if err != nil {
			return err
		}
		for _, rawID := range criterionIDsForRequirement {
			criterionID := asStringOr(rawID)
			if !criterionIDs[criterionID] {
				return invalid("requirement refers to an unknown criterion")
			}
			mapped[criterionID] = true
		}
	}
	for criterionID := range criterionIDs {
		if !mapped[criterionID] {
			return invalid("supplied requirements must map every criterion")
		}
	}
	for _, raw := range criteria {
		criterion, _ := raw.(map[string]any)
		kind := asStringOr(criterion["kind"])
		if kind != "file_assertion" && kind != "external_artifact" {
			protected, _ := nativeItems(criterion["protected_paths"])
			if len(protected) == 0 {
				return invalid("requirement review needs protected test inputs for executable criteria")
			}
		}
	}
	return nil
}

func validateNativeModels(request map[string]any, provider string) error {
	models, err := nativeObject(request["models"], []string{"worker", "worker_effort", "reviewer", "reviewer_effort"}, nil, "models")
	if err != nil {
		return err
	}
	if provider == "opencode" {
		for _, field := range []string{"worker", "reviewer"} {
			if asStringOr(models[field]) != "deepseek/deepseek-v4-flash" {
				return invalid("OpenCode model %s is unsupported", field)
			}
		}
		for _, field := range []string{"worker_effort", "reviewer_effort"} {
			if models[field] != nil {
				return invalid("OpenCode effort %s must be null", field)
			}
		}
		return nil
	}
	for _, field := range []string{"worker", "reviewer"} {
		value, ok := asString(models[field])
		if !ok || !nativeModelPattern.MatchString(value) {
			return invalid("invalid model %s", field)
		}
	}
	for _, field := range []string{"worker_effort", "reviewer_effort"} {
		value, ok := asString(models[field])
		if !ok || (value != "low" && value != "medium" && value != "high" && value != "xhigh") {
			return invalid("invalid effort %s", field)
		}
	}
	return nil
}

func validateNativeRequest(request map[string]any, taskID string) error {
	if err := validateV1Request(request); err != nil {
		return invalid("invalid activation request: %v", err)
	}
	requestID, _ := asString(request["request_id"])
	if requestID != taskID {
		return conflict("request_id %s does not match task %s", requestID, taskID)
	}
	if err := validateNativeText(request["prompt"], "prompt"); err != nil {
		return err
	}
	profile, present := request["execution_profile"]
	if !present || profile == nil {
		return blocked("native controller requires an explicit execution_profile")
	}
	if err := validateNativeExecutionProfile(profile); err != nil {
		return err
	}
	profileObject, _ := profile.(map[string]any)
	provider := asStringOr(profileObject["provider"])
	if err := validateNativeModels(request, provider); err != nil {
		return err
	}
	if budget, present := request["budget"]; !present || budget == nil {
		return invalid("a managed execution profile requires an explicit budget")
	} else if err := validateNativeBudget(budget); err != nil {
		return err
	}
	if err := validateNativeCriteria(request); err != nil {
		return err
	}
	if err := validateNativeRequirements(request); err != nil {
		return err
	}
	if mode := asStringOr(request["mode"]); mode == "implement" && len(anyItems(request["criteria"])) == 0 {
		return invalid("implementation requires observable acceptance criteria")
	}
	if paths, present := request["source_paths"]; present {
		items, err := validateNativeStringArray(paths, "source_paths", true, false)
		if err != nil {
			return err
		}
		for _, raw := range items {
			if err := validateRelativeNativePath(asStringOr(raw), true); err != nil {
				return invalid("invalid source path: %v", err)
			}
		}
	}
	if _, _, _, err := requestLimits(request); err != nil {
		return err
	}
	return nil
}

func validateNativeActivationRequest(request map[string]any, taskID string) error {
	return validateNativeRequest(request, taskID)
}

func routeForController(payload map[string]any) []string {
	request, _ := payload["request"].(map[string]any)
	classification, _ := payload["classification"].(map[string]any)
	complexity, _ := asString(classification["complexity"])
	risk, _ := asString(classification["risk"])
	high := risk == "high" || complexity == "L"
	spec := high || complexity == "M" || risk == "medium"
	if request != nil {
		if value, _ := asString(request["analysis_goal"]); value == "specification" && asStringOr(request["mode"]) == "analysis_only" {
			spec = true
		}
		if value, ok := asBool(request["require_spec_review"]); ok && value {
			spec = true
		}
	}
	review := high || complexity == "M"
	if request != nil {
		if value, ok := asBool(request["require_spec_review"]); ok && value {
			review = true
		}
	}
	if rules, ok := payload["policy_rules"].(map[string]any); ok && complexity == "S" {
		if required, ok := asBool(rules["s_review_required"]); ok && required {
			review = true
		}
	}
	if review {
		spec = true
	}
	route := []string{"inspect"}
	mode := asStringOr(request["mode"])
	goal := asStringOr(request["analysis_goal"])
	if mode == "implement" || goal == "specification" {
		if spec {
			route = append(route, "spec")
		}
		if review {
			route = append(route, "spec_review")
		}
	}
	if mode == "implement" {
		route = append(route, "implement")
		needsCode := high || asBoolOr(request["require_code_review"]) || len(anyItems(request["requirements"])) > 0 || asIntOr(asMap(payload["repair"])["rounds"]) > 0
		if !needsCode {
			for _, raw := range anyItems(request["criteria"]) {
				if criterion, ok := raw.(map[string]any); ok && asStringOr(criterion["kind"]) == "native_1c" {
					needsCode = true
				}
			}
		}
		if needsCode {
			route = append(route, "code_review")
		}
		route = append(route, "verify")
	}
	return append(route, "acceptance")
}

func asStringOr(value any) string {
	parsed, _ := asString(value)
	return parsed
}

func asBoolOr(value any) bool {
	parsed, _ := asBool(value)
	return parsed
}

func controllerNext(outer map[string]any, payload map[string]any) (map[string]any, error) {
	if asBoolOr(asMap(outer["migration"])["requires_rebind"]) {
		return map[string]any{"stage": payload["stage"], "action": "blocked", "blockers": []any{"adopted history requires an explicit task rebind"}}, nil
	}
	if err := validateControllerState(payload, asStringOr(outer["task_id"])); err != nil {
		return nil, blocked("invalid controller state: %v", err)
	}
	status := asStringOr(payload["status"])
	stage := asStringOr(payload["stage"])
	if status == "cancelled" {
		return map[string]any{"stage": stage, "action": "cancelled", "blockers": []any{"task is cancelled; explicit update is required"}}, nil
	}
	if payload["unresolved_effect"] != nil {
		return map[string]any{"stage": stage, "action": "recover", "blockers": []any{"uncertain effect requires explicit reconciliation"}}, nil
	}
	if active, ok := asString(payload["active_attempt"]); ok && active != "" {
		return map[string]any{"stage": stage, "action": "recover", "attempt_id": active, "blockers": []any{"unfinished attempt requires reconciliation"}}, nil
	}
	if payload["question"] != nil {
		question, _ := payload["question"].(map[string]any)
		text := asStringOr(question["text"])
		return map[string]any{"stage": stage, "action": "needs_input", "blockers": []any{text}}, nil
	}
	if status == "failed" {
		return map[string]any{"stage": stage, "action": "failed", "blockers": payload["blockers"]}, nil
	}
	if status == "blocked" {
		return map[string]any{"stage": stage, "action": "blocked", "blockers": payload["blockers"]}, nil
	}
	if err := assertNativePolicyFresh(payload); err != nil {
		return nil, err
	}
	if asMap(payload["repair"])["pending_failure"] != nil {
		if err := validateNativeRepairFailure(outer, payload); err != nil {
			return nil, err
		}
		return map[string]any{"stage": "diagnose", "action": "dispatch", "blockers": []any{}}, nil
	}
	route := routeForController(payload)
	for _, candidateStage := range route {
		if candidateStage == "acceptance" {
			return map[string]any{"stage": candidateStage, "action": "accept", "blockers": []any{}}, nil
		}
		if evidence, ok := latestEvidence(payload, candidateStage); ok && controllerEvidenceFresh(outer, payload, evidence) {
			continue
		}
		return map[string]any{"stage": candidateStage, "action": "dispatch", "blockers": []any{}}, nil
	}
	return map[string]any{"stage": stage, "action": "blocked", "blockers": []any{"controller route has no next action"}}, nil
}

func latestEvidence(payload map[string]any, stage string) (map[string]any, bool) {
	var found map[string]any
	for _, raw := range anyItems(payload["evidence"]) {
		item, ok := raw.(map[string]any)
		if !ok || asStringOr(item["stage"]) != stage {
			continue
		}
		found = item
	}
	return found, found != nil
}

func sourcePaths(payload map[string]any) []string {
	// source_paths are discovery hints in the request. Freshness and source
	// drift are bound to the complete checkout, matching the legacy controller;
	// narrowing the manifest to a hint would let an edit outside that hint evade
	// a read-only freshness check.
	return []string{"."}
}

func controllerEnvelope(outer, payload map[string]any, next map[string]any) map[string]any {
	result := map[string]any{
		"schema_version": int64(1),
		"task_id":        outer["task_id"],
		"revision":       outer["revision"],
		"status":         payload["status"],
		"stage":          payload["stage"],
		"next_stage":     next["stage"],
		"next_action":    next["action"],
		"blockers":       projectionStrings(next["blockers"]),
		"evidence_refs":  evidenceReferences(payload),
		"worker_path":    safeProjectionText(asStringOr(payload["worker_path"])),
	}
	if active, ok := asString(payload["active_attempt"]); ok && active != "" {
		result["active_attempt"] = active
	}
	if payload["unresolved_effect"] != nil {
		result["unresolved_effect"] = projectionUnknownEffect(payload["unresolved_effect"])
	}
	return result
}

func evidenceReferences(payload map[string]any) []string {
	refs := []string{}
	for _, raw := range anyItems(payload["evidence"]) {
		if item, ok := raw.(map[string]any); ok {
			if id := asStringOr(item["attempt_id"]); id != "" {
				refs = append(refs, id)
			}
		}
	}
	return refs
}

// sourceManifest derives the baseline from the worker's current HEAD.  The
// compatibility wrapper is useful for read-only freshness checks; execution
// bindings call sourceManifestWithBaseline so a HEAD drift is observable.
func sourceManifest(root string, paths []string) (map[string]any, error) {
	current, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		return nil, blocked("cannot resolve source HEAD: %v", err)
	}
	baseline := strings.TrimSpace(current)
	if baseline == "" {
		return nil, blocked("source HEAD is empty")
	}
	return sourceManifestWithBaseline(root, baseline, paths)
}

// sourceManifestWithBaseline mirrors Get-BFSourceManifest.  It binds the
// complete requested source set to the activation baseline, includes deleted
// baseline files, and omits controller/runtime directories from both the
// current inventory and the deletion set.  The `current` field makes the
// observed Git ref explicit even though a valid binding requires it to equal
// `baseline`.
func sourceManifestWithBaseline(root, baseline string, paths []string) (map[string]any, error) {
	fullRoot, err := SafePath(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(fullRoot)
	if err != nil {
		return nil, blocked("source root is unavailable: %v", err)
	}
	if !info.IsDir() {
		return nil, blocked("source root is not a directory")
	}
	currentText, err := gitOutput(fullRoot, "rev-parse", "HEAD")
	if err != nil {
		return nil, blocked("cannot resolve source HEAD: %v", err)
	}
	current := strings.TrimSpace(currentText)
	if current == "" {
		return nil, blocked("source HEAD is empty")
	}
	if strings.TrimSpace(baseline) == "" {
		baseline = current
	}
	if current != baseline {
		return nil, blocked("worker HEAD changed; explicit scope reconciliation is required")
	}

	type manifestEntry struct {
		path    string
		sha256  any
		deleted bool
	}
	entries := map[string]manifestEntry{}
	for _, requested := range paths {
		if err := validateRelativeNativePath(requested, true); err != nil {
			return nil, err
		}
		start := fullRoot
		if requested != "." {
			start = filepath.Join(fullRoot, filepath.FromSlash(requested))
		}
		start, err = SafePath(start)
		if err != nil {
			return nil, err
		}
		item, statErr := os.Lstat(start)
		if os.IsNotExist(statErr) {
			return nil, blocked("required source path missing: %s", requested)
		}
		if statErr != nil {
			return nil, statErr
		}
		if item.Mode().IsRegular() {
			if isExcludedSourcePath(fullRoot, start) {
				continue
			}
			entry, entryErr := manifestFile(fullRoot, start)
			if entryErr != nil {
				return nil, entryErr
			}
			entries[entry.path] = manifestEntry{path: entry.path, sha256: entry.hash}
			continue
		}
		if !item.IsDir() {
			return nil, fmt.Errorf("source path is not a regular file or directory: %s", requested)
		}
		walkErr := filepath.WalkDir(start, func(full string, dirEntry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if isExcludedSourcePath(fullRoot, full) {
				if dirEntry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if dirEntry.IsDir() {
				return nil
			}
			if !dirEntry.Type().IsRegular() {
				return fmt.Errorf("source manifest contains a non-regular file: %s", full)
			}
			entry, entryErr := manifestFile(fullRoot, full)
			if entryErr != nil {
				return entryErr
			}
			entries[entry.path] = manifestEntry{path: entry.path, sha256: entry.hash}
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}

	baselineOutput, err := gitOutput(fullRoot, "-c", "core.quotePath=false", "ls-tree", "-r", "--name-only", baseline)
	if err != nil {
		return nil, blocked("cannot enumerate baseline source files: %v", err)
	}
	for _, line := range strings.Split(strings.ReplaceAll(baselineOutput, "\r\n", "\n"), "\n") {
		path := strings.TrimSuffix(line, "\r")
		if path == "" || isExcludedSourceRelative(path) {
			continue
		}
		if strings.ContainsAny(path, "\x00\r\n") {
			return nil, blocked("baseline contains an unsafe source path")
		}
		if _, present := entries[path]; !present {
			entries[path] = manifestEntry{path: path, sha256: nil, deleted: true}
		}
	}
	if len(entries) == 0 {
		return nil, blocked("empty source manifest")
	}
	ordered := make([]manifestEntry, 0, len(entries))
	for _, entry := range entries {
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].path < ordered[j].path })
	files := make([]any, 0, len(ordered))
	for _, entry := range ordered {
		files = append(files, map[string]any{"path": entry.path, "sha256": entry.sha256, "deleted": entry.deleted})
	}
	hash, err := Hash(files)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"schema_version": int64(1), "baseline": baseline,
		"source_paths": []any{"."}, "files": files, "sha256": hash,
	}, nil
}

func isExcludedSourceRelative(relative string) bool {
	normalized := filepath.ToSlash(relative)
	return normalized == ".git" || strings.HasPrefix(normalized, ".git/") ||
		normalized == ".bsl-flow" || strings.HasPrefix(normalized, ".bsl-flow/") ||
		normalized == ".bsl-flow-worker" || strings.HasPrefix(normalized, ".bsl-flow-worker/")
}

func isExcludedSourcePath(root, full string) bool {
	relative, err := filepath.Rel(root, full)
	if err != nil {
		return true
	}
	return isExcludedSourceRelative(relative)
}

type manifestFileEntry struct {
	path string
	hash string
	size int64
}

func manifestFile(root, full string) (manifestFileEntry, error) {
	info, err := os.Lstat(full)
	if err != nil {
		return manifestFileEntry{}, err
	}
	if !info.Mode().IsRegular() {
		return manifestFileEntry{}, fmt.Errorf("manifest file is not regular: %s", full)
	}
	data, err := ReadFileBytes(full)
	if err != nil {
		return manifestFileEntry{}, err
	}
	// The source manifest contract records the SHA-256 of the actual bytes,
	// while the outer manifest hash is over the sorted file descriptors.
	actual := sha256Hex(data)
	relative, err := filepath.Rel(root, full)
	if err != nil {
		return manifestFileEntry{}, err
	}
	return manifestFileEntry{path: filepath.ToSlash(relative), hash: actual, size: info.Size()}, nil
}

func sha256Hex(data []byte) string {
	// Hashing through the canonical helper would hash a JSON string, not file
	// bytes. Keep this tiny wrapper local to avoid exposing a second public API.
	imported := sha256Sum(data)
	return imported
}

func sha256Sum(data []byte) string {
	// Implemented in controller_hash.go to keep platform-neutral imports small.
	return fileSHA256(data)
}

func ensureControllerPath(path string) error {
	if _, err := SafePath(path); err != nil {
		return err
	}
	return SafeMkdir(filepath.Dir(path))
}

func writeImmutableJSON(path string, value any) (string, error) {
	data, err := Canonical(value)
	if err != nil {
		return "", err
	}
	if existing, err := ReadFileBytes(path); err == nil {
		if string(existing) != string(data) {
			return "", conflict("immutable artifact differs: %s", path)
		}
		return Hash(value)
	} else if !os.IsNotExist(err) {
		return "", blocked("cannot inspect immutable artifact: %v", err)
	}
	if err := ensureControllerPath(path); err != nil {
		return "", err
	}
	if err := AtomicWrite(path, data, false); err != nil {
		return "", err
	}
	return Hash(value)
}

func attemptDirectory(repository *Repository, taskID, attemptID string) (string, error) {
	if !isUUID(taskID) || !isUUID(attemptID) {
		return "", invalid("attempt identity must be a lowercase UUID")
	}
	path := filepath.Join(repository.StorePath, "tasks", taskID, "attempts", attemptID)
	if _, err := SafePath(path); err != nil {
		return "", blocked("unsafe attempt path: %v", err)
	}
	return path, nil
}

func contextDirectory(repository *Repository, taskID, attemptID string) (string, error) {
	path := filepath.Join(repository.Worktree, ".bsl-flow", "hosts", "native", taskID, attemptID, "context")
	if _, err := SafePath(path); err != nil {
		return "", blocked("unsafe provider context path: %v", err)
	}
	return path, nil
}

func artifactDirectory(repository *Repository, taskID, attemptID string) (string, error) {
	path := filepath.Join(repository.Worktree, ".bsl-flow", "hosts", "native", taskID, attemptID, "artifacts")
	if _, err := SafePath(path); err != nil {
		return "", blocked("unsafe provider artifact path: %v", err)
	}
	return path, nil
}

func createCancelSignal(repository *Repository, taskID, attemptID string) (string, error) {
	root, err := contextDirectory(repository, taskID, attemptID)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, "cancel.signal")
	if err := ensureControllerPath(path); err != nil {
		return "", err
	}
	return path, nil
}

func writeCancelSignal(repository *Repository, taskID, attemptID string) error {
	path, err := createCancelSignal(repository, taskID, attemptID)
	if err != nil {
		return err
	}
	data, err := Canonical(map[string]any{
		"schema_version": int64(1), "task_id": taskID, "attempt_id": attemptID,
		"cancelled": true, "reason": "operator_requested",
	})
	if err != nil {
		return err
	}
	return AtomicWrite(path, data, true)
}

func appendControllerRevision(repository *Repository, task *Task, payload map[string]any, expected int64) (map[string]any, error) {
	card := make(map[string]any, len(task.State)+1)
	for key, value := range task.State {
		card[key] = value
	}
	card["lifecycle"] = "controller"
	card["controller"] = payload
	card["updated_at"] = nowUTC()
	return repository.writeRevision(task.ID, card, expected)
}

func ensureGeneratedPathIgnored(repository *Repository, path string) error {
	relative, err := filepath.Rel(repository.Worktree, path)
	if err != nil || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return blocked("generated native path is outside the project worktree")
	}
	ignored, err := gitCheckIgnored(repository.Worktree, filepath.ToSlash(relative))
	if err != nil {
		return blocked("cannot verify ignore policy for generated native path: %v", err)
	}
	if !ignored {
		return blocked("generated native path is not ignored by the project Git policy: %s", relative)
	}
	return nil
}

func ensureWorker(repository *Repository, taskID, baseline string, authorizationRevision int64) (string, bool, error) {
	// Scope workers to this project, task and execution binding. A rebind gets a
	// fresh target and leaves a dirty prior worker available for inspection.
	worker := filepath.Join(repository.Worktree, ".bsl-flow", "native-worktrees", taskID, fmt.Sprintf("a%d", authorizationRevision))
	if _, err := SafePath(worker); err != nil {
		return "", false, blocked("unsafe worker path: %v", err)
	}
	if err := ensureGeneratedPathIgnored(repository, worker); err != nil {
		return "", false, err
	}
	if info, err := os.Lstat(worker); err == nil {
		if !info.IsDir() {
			return "", false, conflict("worker target exists and is not a directory: %s", worker)
		}
		root, rootErr := gitOutput(worker, "rev-parse", "--show-toplevel")
		if rootErr != nil {
			return "", false, conflict("existing worker target is not a Git worktree: %s", worker)
		}
		resolved, rootErr := SafePath(strings.TrimSpace(root))
		if rootErr != nil || !strings.EqualFold(resolved, worker) {
			return "", false, conflict("existing worker target belongs to another worktree: %s", worker)
		}
		current, rootErr := gitOutput(worker, "rev-parse", "HEAD")
		if rootErr != nil || strings.TrimSpace(current) != baseline {
			return "", false, conflict("existing worker target baseline differs from activation baseline")
		}
		if dirty, dirtyErr := gitOutput(worker, "status", "--porcelain", "--untracked-files=all"); dirtyErr != nil || strings.TrimSpace(dirty) != "" {
			return "", false, conflict("existing worker target is not clean")
		}
		return worker, false, nil
	} else if !os.IsNotExist(err) {
		return "", false, blocked("cannot inspect worker target: %v", err)
	}
	if err := SafeMkdir(filepath.Dir(worker)); err != nil {
		return "", false, err
	}
	if _, err := gitOutput(repository.Worktree, "worktree", "add", "--detach", worker, baseline); err != nil {
		return "", false, blocked("cannot create native worker: %v", err)
	}
	return worker, true, nil
}

func exactGitRoot(repository *Repository) error {
	root, err := gitOutput(repository.Worktree, "rev-parse", "--show-toplevel")
	if err != nil {
		return blocked("cannot resolve exact Git worktree root: %v", err)
	}
	resolved, err := SafePath(strings.TrimSpace(root))
	if err != nil || !strings.EqualFold(resolved, repository.Worktree) {
		return invalid("activation requires the exact Git worktree root")
	}
	return nil
}

func cleanBaseline(repository *Repository) (string, error) {
	dirty, err := gitOutput(repository.Worktree, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return "", blocked("cannot inspect clean baseline: %v", err)
	}
	if strings.TrimSpace(dirty) != "" {
		return "", blocked("activation requires a clean baseline")
	}
	baseline, err := gitOutput(repository.Worktree, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(baseline) == "" {
		return "", blocked("cannot resolve activation baseline")
	}
	return strings.TrimSpace(baseline), nil
}

func localPolicy(repository *Repository, engine EngineIdentity) ([]any, map[string]any, string, error) {
	policyFiles, err := nativePolicyInventory(repository.Worktree, engine.PolicyRoot, engine.HostPath)
	if err != nil {
		return nil, nil, "", err
	}
	rules, err := nativeProjectRules(repository.Worktree)
	if err != nil {
		return nil, nil, "", err
	}
	hash, err := Hash(policyFiles)
	if err != nil {
		return nil, nil, "", err
	}
	return policyFiles, rules, hash, nil
}

// localSpecInputs mirrors the provider's Get-BFSpecInputs contract.  Missing
// files are represented by null so a provider cannot turn an omitted input
// into an indistinguishable empty string.
func localSpecInputs(repository *Repository, taskID string) (map[string]any, error) {
	if !isUUID(taskID) {
		return nil, invalid("task id must be a lowercase UUID")
	}
	changeRoot := filepath.Join(repository.Worktree, "openspec", "changes", "bsl-flow-"+taskID)
	if _, err := SafePath(changeRoot); err != nil {
		return nil, err
	}
	result := map[string]any{}
	for _, name := range []string{"original-task.md", "spec.md", "design.md"} {
		path := filepath.Join(changeRoot, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			result[name] = nil
			continue
		}
		if err != nil {
			return nil, blocked("cannot inspect specification input %s: %v", name, err)
		}
		if !info.Mode().IsRegular() {
			return nil, blocked("specification input is not a regular file: %s", name)
		}
		data, err := ReadFileBytes(path)
		if err != nil {
			return nil, blocked("cannot read specification input %s: %v", name, err)
		}
		result[name] = fileSHA256(data)
	}
	return result, nil
}

// validateProviderPolicyFiles validates the returned policy inventory without
// trusting it as authority.  The provider may include packaged skill files;
// the controller independently requires the project policy entry and checks
// every listed non-null digest against the bytes on disk.
func validateProviderPolicyFiles(repository *Repository, values []any) error {
	if values == nil {
		return blocked("provider measure omitted policy files")
	}
	projectPolicy, err := SafePath(filepath.Join(repository.Worktree, "bsl-flow.yaml"))
	if err != nil {
		return err
	}
	expectedHash := any(nil)
	if info, statErr := os.Lstat(projectPolicy); statErr == nil {
		if !info.Mode().IsRegular() {
			return blocked("project policy is not a regular file")
		}
		data, readErr := ReadFileBytes(projectPolicy)
		if readErr != nil {
			return blocked("cannot read project policy: %v", readErr)
		}
		expectedHash = fileSHA256(data)
	} else if !os.IsNotExist(statErr) {
		return blocked("cannot inspect project policy: %v", statErr)
	}
	foundProject := false
	seen := map[string]bool{}
	for _, raw := range values {
		entry, ok := raw.(map[string]any)
		if !ok || entry == nil {
			return invalid("provider policy file descriptor must be an object")
		}
		if len(entry) != 2 {
			return invalid("provider policy file descriptor contains unsupported fields")
		}
		path, ok := asString(entry["path"])
		if !ok || strings.TrimSpace(path) == "" {
			return invalid("provider policy file path is invalid")
		}
		resolved, pathErr := SafePath(path)
		if pathErr != nil {
			return blocked("provider policy file path is unsafe: %v", pathErr)
		}
		key := strings.ToLower(resolved)
		if seen[key] {
			return invalid("provider policy file inventory contains duplicates")
		}
		seen[key] = true
		value, present := entry["sha256"]
		if !present {
			return invalid("provider policy file sha256 is required")
		}
		if value != nil {
			hash, hashOK := asString(value)
			if !hashOK || !isSHA256(hash) {
				return invalid("provider policy file sha256 is invalid")
			}
			data, readErr := ReadFileBytes(resolved)
			if readErr != nil || fileSHA256(data) != hash {
				return blocked("provider policy file bytes do not match the listed hash")
			}
		} else if _, statErr := os.Lstat(resolved); statErr == nil {
			return blocked("provider policy file is present despite a null hash")
		} else if !os.IsNotExist(statErr) {
			return blocked("cannot inspect provider policy file: %v", statErr)
		}
		if strings.EqualFold(resolved, projectPolicy) {
			foundProject = true
			if !equalJSON(value, expectedHash) {
				return blocked("provider project policy does not match current bytes")
			}
		}
	}
	if !foundProject {
		return blocked("provider policy inventory omitted the project policy")
	}
	return nil
}

func validateMeasureBindings(repository *Repository, taskID, baseline string, request map[string]any, measurement MeasureObservation, policyRules map[string]any, policyHash string) error {
	if err := validateProviderPolicyFiles(repository, measurement.PolicyFiles); err != nil {
		return err
	}
	if actualHash, err := Hash(measurement.PolicyFiles); err != nil || actualHash != policyHash {
		return blocked("provider policy inventory differs from independently enumerated host policy")
	}
	if measurement.PolicyRules == nil || !equalJSON(measurement.PolicyRules, policyRules) {
		return blocked("provider policy rules do not match the controller snapshot")
	}
	expectedSource, err := sourceManifestWithBaseline(repository.Worktree, baseline, []string{"."})
	if err != nil {
		return blocked("cannot bind activation source manifest: %v", err)
	}
	if measurement.SourceManifest == nil || !equalJSON(measurement.SourceManifest, expectedSource) {
		return blocked("provider source manifest does not match the activation baseline")
	}
	expectedSpec, err := localSpecInputs(repository, taskID)
	if err != nil {
		return err
	}
	if measurement.SpecInputs == nil || !equalJSON(measurement.SpecInputs, expectedSpec) {
		return blocked("provider specification inputs do not match current bytes")
	}
	if measurement.Dependencies == nil {
		return blocked("provider measure omitted dependencies")
	}
	if asStringOr(measurement.Dependencies["intent"]) != requestHashOrEmpty(request) {
		return blocked("provider measure intent dependency is stale")
	}
	if asStringOr(measurement.Dependencies["policy"]) != policyHash {
		return blocked("provider measure policy dependency is stale")
	}
	if asStringOr(measurement.Dependencies["baseline"]) != baseline {
		return blocked("provider measure baseline dependency is stale")
	}
	return nil
}

func requestHashOrEmpty(request map[string]any) string {
	value, err := requestIntentHash(request)
	if err != nil {
		return ""
	}
	return value
}

func provisionalController(task *Task, request map[string]any, project, baseline string, policyFiles []any, policyRules map[string]any, policyHash string, engine EngineIdentity) (map[string]any, error) {
	requestHash, err := Hash(request)
	if err != nil {
		return nil, err
	}
	intentHash, err := requestIntentHash(request)
	if err != nil {
		return nil, err
	}
	classification := map[string]any{"complexity": request["complexity"], "risk": request["risk"], "impact_flags": request["impact_flags"], "rationale": "Initial trusted task classification; inspect can only strengthen it."}
	return map[string]any{
		"project_path": project, "worker_path": project, "baseline": baseline,
		"request": request, "request_hash": requestHash, "intent_hash": intentHash,
		"policy_hash": policyHash, "intent_revision": int64(1), "authorization_revision": int64(1),
		"correction_rounds": int64(0), "policy_files": policyFiles, "policy_rules": policyRules,
		"classification": classification, "status": "ready", "stage": "inspect",
		"active_attempt": nil, "unresolved_effect": nil, "attempts": []any{}, "evidence": []any{},
		"events": []any{}, "question": nil, "blockers": []any{}, "acceptances": []any{},
		"repair": map[string]any{"rounds": int64(0), "pending_failure": nil, "last_source_sha256": nil, "diagnosis_attempt": nil},
		"engine": engineMap(engine),
	}, nil
}

func activatedController(task *Task, request map[string]any, project, worker, baseline string, policyFiles []any, policyRules map[string]any, policyHash string, engine EngineIdentity) (map[string]any, error) {
	payload, err := provisionalController(task, request, project, baseline, policyFiles, policyRules, policyHash, engine)
	if err != nil {
		return nil, err
	}
	payload["worker_path"] = worker
	return payload, nil
}

func toMeasureObservation(value MeasureObservation) map[string]any {
	return map[string]any{
		"schema_version": value.SchemaVersion, "contract": value.Contract, "task_id": value.TaskID,
		"operation": value.Operation, "request_valid": value.RequestValid, "policy_files": value.PolicyFiles,
		"policy_rules": value.PolicyRules, "source_manifest": value.SourceManifest, "spec_inputs": value.SpecInputs,
		"dependencies": value.Dependencies, "capability": value.Capability, "blockers": value.Blockers,
	}
}

func toExecuteObservation(value ExecuteObservation) map[string]any {
	return map[string]any{
		"schema_version": value.SchemaVersion, "contract": value.Contract, "task_id": value.TaskID,
		"attempt_id": value.AttemptID, "stage": value.Stage, "status": value.Status, "summary": value.Summary,
		"proposal": value.Proposal, "side_effects": value.SideEffects, "dependencies": value.Dependencies,
		"source_manifest": value.SourceManifest, "artifacts": artifactMaps(value.Artifacts), "process_receipt": value.ProcessReceipt,
		"provider_contract": map[string]any{"name": value.ProviderContract.Name, "version": value.ProviderContract.Version, "host_sha256": value.ProviderContract.HostSHA256, "provider_sha256": value.ProviderContract.ProviderSHA256, "asset_manifest_sha256": value.ProviderContract.AssetManifestSHA256},
	}
}

func artifactMaps(values []ArtifactRef) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, map[string]any{"path": value.Path, "sha256": value.SHA256, "size_bytes": value.SizeBytes, "kind": value.Kind})
	}
	return result
}

func validateMeasureObservation(observation MeasureObservation, taskID string, request map[string]any) error {
	if observation.SchemaVersion != nativeControllerSchema || observation.Contract != NativeProviderContract || observation.Operation != "measure" || observation.TaskID != taskID {
		return blocked("provider measure identity or contract mismatch")
	}
	if len(observation.Blockers) != 0 {
		return blocked("provider preflight blocked: %s", observation.Blockers[0])
	}
	if !observation.RequestValid {
		if len(observation.Blockers) > 0 {
			return blocked("provider measure blocked: %s", observation.Blockers[0])
		}
		return blocked("provider measure rejected the request")
	}
	if observation.PolicyRules == nil {
		return blocked("provider measure omitted policy rules")
	}
	if observation.Capability == nil {
		return blocked("provider measure omitted capability observation")
	}
	capability, err := nativeObject(observation.Capability, []string{"observations", "permissions_sha256", "sandbox_sha256", "provider_sha256", "network", "database"}, nil, "provider capability")
	if err != nil {
		return err
	}
	expected := map[string]any{"config_read": "allowed", "config_write": "denied", "scratch_write": "allowed", "source_write": "denied", "canonical_current_read": "denied", "canonical_revision_read": "denied", "canonical_inputs_read": "denied", "canonical_current_write": "denied", "canonical_revision_write": "denied", "canonical_inputs_write": "denied"}
	profile := asMap(request["execution_profile"])
	if !equalJSON(capability["observations"], expected) || capability["sandbox_sha256"] != asMap(profile["sandbox"])["sha256"] || capability["provider_sha256"] != profile["executable_sha256"] || !isSHA256(asStringOr(capability["permissions_sha256"])) || capability["network"] != "not_probed" || capability["database"] != "not_accessed" {
		return blocked("provider capability lacks the exact required sandbox observations and pins")
	}
	return nil
}

func validateExecuteObservation(observation ExecuteObservation, taskID, attemptID, stage string, engine EngineIdentity, artifactRoot string, expectedDependencies map[string]any, beforeManifest map[string]any, worker string) (map[string]any, error) {
	if observation.SchemaVersion != nativeControllerSchema || observation.Contract != NativeProviderContract || observation.TaskID != taskID || observation.AttemptID != attemptID || observation.Stage != stage {
		return nil, blocked("provider observation identity or contract mismatch")
	}
	if observation.Status != "completed" && observation.Status != "failed" && observation.Status != "blocked" && observation.Status != "needs_input" {
		return nil, invalid("provider observation status is invalid")
	}
	if strings.TrimSpace(observation.Summary) == "" || controlPattern.MatchString(observation.Summary) || secretPattern.MatchString(observation.Summary) {
		return nil, blocked("provider observation summary is invalid")
	}
	if observation.SideEffects != "none" && observation.SideEffects != "source_changed" && observation.SideEffects != "unknown" {
		return nil, invalid("provider observation side_effects is invalid")
	}
	if observation.SideEffects == "source_changed" && stage != "implement" {
		return nil, blocked("read-only provider stage reported source changes")
	}
	contract := observation.ProviderContract
	expectedContract := providerContract(engine)
	if contract != expectedContract {
		return nil, blocked("provider observation contract identity changed")
	}
	if observation.Proposal != nil {
		for _, key := range []string{"schema_version", "task_id", "revision", "status", "stage", "next_action", "acceptance", "accepted", "controller", "engine"} {
			if _, present := observation.Proposal[key]; present {
				return nil, blocked("provider proposal contains controller-owned field %s", key)
			}
		}
	}
	comparison, err := cloneObject(observation.Dependencies)
	if err != nil || len(comparison) == 0 {
		return nil, blocked("provider dependency object is missing")
	}
	if observation.Status == "completed" {
		switch stage {
		case "implement":
			comparison["source"] = expectedDependencies["source"]
		case "spec":
			comparison["spec"] = expectedDependencies["spec"]
		case "spec_review":
			comparison["spec"] = expectedDependencies["spec"]
			comparison["review_binding"] = expectedDependencies["review_binding"]
		}
	}
	if !equalJSON(comparison, expectedDependencies) {
		return nil, blocked("provider dependencies are incomplete or stale")
	}
	manifest := beforeManifest
	current, currentErr := sourceManifestWithBaseline(worker, asStringOr(beforeManifest["baseline"]), []string{"."})
	if currentErr != nil {
		return nil, blocked("cannot verify source manifest after provider stage: %v", currentErr)
	}
	if stage == "implement" {
		manifest = current
		if !equalJSON(current, beforeManifest) && observation.SideEffects != "source_changed" {
			return nil, blocked("implementation changed source without declaring source_changed")
		}
	} else {
		if !equalJSON(current, beforeManifest) || observation.SideEffects == "source_changed" {
			return nil, blocked("provider changed source during a read-only stage")
		}
	}
	if observation.SourceManifest == nil || !equalJSON(observation.SourceManifest, manifest) {
		return nil, blocked("provider source manifest does not match current bytes")
	}
	if err := validateProviderProcessReceipt(observation.ProcessReceipt, artifactRoot); err != nil {
		return nil, err
	}
	seenArtifacts := map[string]bool{}
	for _, artifact := range observation.Artifacts {
		key := strings.ToLower(filepath.ToSlash(artifact.Path))
		if seenArtifacts[key] {
			return nil, invalid("duplicate provider artifact path")
		}
		seenArtifacts[key] = true
		if !isSHA256(artifact.SHA256) || artifact.SizeBytes < 0 || !providerArtifactKinds[artifact.Kind] {
			return nil, invalid("provider artifact descriptor is invalid")
		}
		if err := validateRelativeNativePath(artifact.Path, false); err != nil {
			return nil, invalid("provider artifact path is invalid: %v", err)
		}
		path := filepath.Join(artifactRoot, filepath.FromSlash(artifact.Path))
		resolved, err := SafePath(path)
		if err != nil || !withinRoot(artifactRoot, resolved) {
			return nil, blocked("provider artifact escaped its root")
		}
		info, err := os.Lstat(resolved)
		if err != nil || !info.Mode().IsRegular() || info.Size() != artifact.SizeBytes {
			return nil, blocked("provider artifact is missing or changed")
		}
		data, err := ReadFileBytes(resolved)
		if err != nil || fileSHA256(data) != artifact.SHA256 {
			return nil, blocked("provider artifact hash does not match")
		}
	}
	return manifest, nil
}

func withinRoot(root, path string) bool {
	base, err := SafePath(root)
	if err != nil {
		return false
	}
	target, err := SafePath(path)
	if err != nil {
		return false
	}
	if strings.EqualFold(base, target) {
		return true
	}
	prefix := strings.TrimSuffix(base, string(filepath.Separator)) + string(filepath.Separator)
	return strings.HasPrefix(strings.ToLower(target), strings.ToLower(prefix))
}

var providerProcessReceiptFields = map[string]bool{
	"process_path": true, "process_sha256": true, "exit_path": true,
	"exit_sha256": true, "stdout_path": true, "stdout_sha256": true,
	"stderr_path": true, "stderr_sha256": true, "exit_code": true,
	"stop_reason": true,
}

// validateProviderProcessReceipt checks the provider's nested process receipts
// against the actual process/exit/stream files.  The outer Go process receipt
// remains a separate transport artifact; this function only validates the
// provider's internal runner evidence.
func validateProviderProcessReceipt(value map[string]any, artifactRoot string) error {
	if value == nil {
		return blocked("provider process receipt is missing")
	}
	if len(value) != 1 {
		return invalid("provider process receipt contains unsupported fields")
	}
	processes, err := validateNativeStringArrayValue(value["processes"], "process_receipt.processes")
	if err != nil {
		// The process list is an array of objects, not strings. Reuse the closed
		// array shape check while keeping the public diagnostic precise.
		items, ok := nativeItems(value["processes"])
		if !ok {
			return err
		}
		processes = items
	}
	seen := map[string]bool{}
	for _, raw := range processes {
		entry, ok := raw.(map[string]any)
		if !ok || entry == nil {
			return invalid("provider process receipt entry must be an object")
		}
		if len(entry) != len(providerProcessReceiptFields) {
			return invalid("provider process receipt entry contains unsupported fields")
		}
		for field := range entry {
			if !providerProcessReceiptFields[field] {
				return invalid("provider process receipt entry contains unsupported field %s", field)
			}
		}
		paths := make([]string, 4)
		for index, field := range []string{"process_path", "exit_path", "stdout_path", "stderr_path"} {
			path, ok := asString(entry[field])
			if !ok || validateRelativeNativePath(path, false) != nil || providerArtifactStatePath(path) {
				return invalid("provider process receipt %s is invalid", field)
			}
			paths[index] = path
		}
		identity := strings.ToLower(paths[0] + "\x00" + paths[1] + "\x00" + paths[2] + "\x00" + paths[3])
		if seen[identity] {
			return invalid("provider process receipt contains duplicate process paths")
		}
		seen[identity] = true
		for index, field := range []string{"process_sha256", "exit_sha256", "stdout_sha256", "stderr_sha256"} {
			hash, ok := asString(entry[field])
			if !ok || !isSHA256(hash) {
				return invalid("provider process receipt %s is invalid", field)
			}
			data, readErr := readProviderArtifactFile(artifactRoot, paths[index])
			if readErr != nil || fileSHA256(data) != hash {
				return blocked("provider process receipt %s does not match artifact bytes", field)
			}
		}
		processObject, err := readProviderArtifactObject(artifactRoot, paths[0], "process")
		if err != nil {
			return err
		}
		exitObject, err := readProviderArtifactObject(artifactRoot, paths[1], "exit")
		if err != nil {
			return err
		}
		if err := validateProviderProcessIdentity(processObject); err != nil {
			return err
		}
		if err := validateProviderExitObject(exitObject, artifactRoot, paths[2], paths[3], entry); err != nil {
			return err
		}
	}
	return nil
}

// validateNativeStringArrayValue is intentionally limited to the process
// receipt's top-level array. It returns the original []any for object arrays
// after checking that the field is present and non-null.
func validateNativeStringArrayValue(value any, name string) ([]any, error) {
	items, ok := nativeItems(value)
	if !ok || items == nil {
		return nil, invalid("%s must be an array", name)
	}
	return items, nil
}

func providerArtifactStatePath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		switch strings.ToLower(part) {
		case ".git", ".bsl-flow", "revisions", "inputs", "current", "acceptance", "current.json", "acceptance.json":
			return true
		}
	}
	return false
}

func readProviderArtifactFile(root, relative string) ([]byte, error) {
	if err := validateRelativeNativePath(relative, false); err != nil {
		return nil, invalid("provider artifact path is invalid: %v", err)
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	resolved, err := SafePath(path)
	if err != nil || !withinRoot(root, resolved) {
		return nil, blocked("provider artifact escaped its root")
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return nil, blocked("provider process receipt file is missing or not regular")
	}
	data, err := ReadFileBytes(resolved)
	if err != nil {
		return nil, blocked("provider process receipt file cannot be read: %v", err)
	}
	return data, nil
}

func readProviderArtifactObject(root, relative, name string) (map[string]any, error) {
	data, err := readProviderArtifactFile(root, relative)
	if err != nil {
		return nil, err
	}
	object, err := DecodeObject(data)
	if err != nil {
		return nil, blocked("provider %s receipt is invalid: %v", name, err)
	}
	return object, nil
}

func validateProviderProcessIdentity(value map[string]any) error {
	allowed := map[string]bool{"pid": true, "start_time_utc": true, "executable": true, "arguments_sha256": true}
	if len(value) != len(allowed) {
		return invalid("provider process identity contains unsupported fields")
	}
	for field := range value {
		if !allowed[field] {
			return invalid("provider process identity contains unsupported field %s", field)
		}
	}
	pid, ok := asInt(value["pid"])
	if !ok || pid < 0 {
		return invalid("provider process identity pid is invalid")
	}
	if text, ok := asString(value["start_time_utc"]); !ok || strings.TrimSpace(text) == "" {
		return invalid("provider process identity start_time_utc is invalid")
	}
	if text, ok := asString(value["executable"]); !ok || strings.TrimSpace(text) == "" {
		return invalid("provider process identity executable is invalid")
	}
	if err := validateNativeHash(value["arguments_sha256"], "provider process identity arguments_sha256"); err != nil {
		return err
	}
	return nil
}

func validateProviderExitObject(value map[string]any, artifactRoot, stdoutPath, stderrPath string, descriptor map[string]any) error {
	allowed := map[string]bool{"exit_code": true, "stop_reason": true, "elapsed_seconds": true, "process_id": true, "executable": true, "stdout": true, "stderr": true}
	if len(value) != len(allowed) {
		return invalid("provider process exit receipt contains unsupported fields")
	}
	for field := range value {
		if !allowed[field] {
			return invalid("provider process exit receipt contains unsupported field %s", field)
		}
	}
	if exitCode, present := value["exit_code"]; present && exitCode != nil {
		if _, ok := asInt(exitCode); !ok {
			return invalid("provider process exit_code is invalid")
		}
	}
	if stop, present := value["stop_reason"]; present && stop != nil {
		if text, ok := asString(stop); !ok || strings.TrimSpace(text) == "" {
			return invalid("provider process stop_reason is invalid")
		}
	}
	if elapsed, ok := finiteNativeNumber(value["elapsed_seconds"]); !ok || elapsed < 0 {
		return invalid("provider process elapsed_seconds is invalid")
	}
	if processID, ok := asInt(value["process_id"]); !ok || processID < 0 {
		return invalid("provider process process_id is invalid")
	}
	if executable, ok := asString(value["executable"]); !ok || strings.TrimSpace(executable) == "" {
		return invalid("provider process executable is invalid")
	}
	for field, expected := range map[string]string{"stdout": stdoutPath, "stderr": stderrPath} {
		absolute, ok := asString(value[field])
		if !ok {
			return invalid("provider process %s path is invalid", field)
		}
		resolved, err := SafePath(absolute)
		if err != nil || !withinRoot(artifactRoot, resolved) {
			return blocked("provider process %s path escaped artifact root", field)
		}
		relative, err := filepath.Rel(artifactRoot, resolved)
		if err != nil || !strings.EqualFold(filepath.ToSlash(relative), expected) {
			return blocked("provider process %s path does not match its receipt descriptor", field)
		}
	}
	if !equalJSON(value["exit_code"], descriptor["exit_code"]) || !equalJSON(value["stop_reason"], descriptor["stop_reason"]) {
		return blocked("provider process terminal values do not match exit receipt")
	}
	return nil
}

func observationStatus(status string) string {
	switch status {
	case "completed":
		return "PASS"
	case "failed":
		return "FAIL"
	case "needs_input":
		return "NEEDS_INPUT"
	default:
		return "BLOCKED"
	}
}

func makeEvidence(observation ExecuteObservation, dependencies map[string]any, artifactRefs []ArtifactRef, resultHash, outcome string) map[string]any {
	raw := make([]any, 0, len(artifactRefs))
	for _, artifact := range artifactRefs {
		path := artifact.Path
		if observation.AttemptID != "" && !strings.HasPrefix(filepath.ToSlash(path), "attempts/") {
			path = filepath.ToSlash(filepath.Join("attempts", observation.AttemptID, filepath.FromSlash(path)))
		}
		raw = append(raw, map[string]any{"path": path, "sha256": artifact.SHA256, "size_bytes": artifact.SizeBytes, "kind": artifact.Kind})
	}
	return map[string]any{"stage": observation.Stage, "attempt_id": observation.AttemptID, "outcome": outcome, "dependencies": dependencies, "raw_hashes": raw, "result_sha256": resultHash, "summary": observation.Summary}
}

// normalizedTerminalResult is the controller-owned stage result consumed by
// the compatibility provider on a later attempt. The wire observation remains
// separately retained in result.json; this normalized shape preserves the
// existing stage helper contract without granting the provider state-writing
// authority.
func normalizedTerminalResult(observation ExecuteObservation, outcome string) (map[string]any, error) {

	raw := make([]any, 0, len(observation.Artifacts))
	for _, artifact := range observation.Artifacts {
		raw = append(raw, map[string]any{
			"path":   filepath.ToSlash(filepath.Join("attempts", observation.AttemptID, filepath.FromSlash(artifact.Path))),
			"sha256": artifact.SHA256, "size_bytes": artifact.SizeBytes, "kind": artifact.Kind,
		})
	}
	return map[string]any{
		"schema_version": int64(1), "task_id": observation.TaskID, "attempt_id": observation.AttemptID,
		"stage": observation.Stage, "outcome": outcome, "summary": observation.Summary,
		"dependencies": observation.Dependencies, "raw_hashes": raw, "proposal": observation.Proposal,
		"side_effects": observation.SideEffects,
	}, nil
}

func applyObservation(payload map[string]any, observation ExecuteObservation, evidence map[string]any, repairEligible bool) error {
	wasCancelled := payload["status"] == "cancelled"
	outcome := asStringOr(evidence["outcome"])
	if observation.Stage == "inspect" && outcome == "PASS" && observation.Proposal != nil {
		if err := strengthenClassification(payload, observation.Proposal); err != nil {
			return err
		}
	}
	evidenceList := anyItems(payload["evidence"])
	for _, raw := range evidenceList {
		if item, ok := raw.(map[string]any); ok && asStringOr(item["attempt_id"]) == observation.AttemptID {
			if !equalJSON(item, evidence) {
				return conflict("conflicting terminal result for attempt %s", observation.AttemptID)
			}
			payload["active_attempt"] = nil
			return nil
		}
	}
	payload["evidence"] = append(evidenceList, evidence)
	payload["active_attempt"] = nil
	payload["question"] = nil
	payload["unresolved_effect"] = nil
	payload["blockers"] = []any{}
	if observation.SideEffects == "unknown" {
		payload["unresolved_effect"] = map[string]any{"attempt_id": observation.AttemptID, "stage": observation.Stage, "source_before_sha256": asStringOr(dependenciesValue(evidence, "source")), "state": "unknown", "scope": "source_only"}
	}
	switch outcome {
	case "PASS":
		payload["status"] = "ready"
	case "NEEDS_INPUT":
		payload["status"] = "needs_input"
		questionID, _ := randomUUID()
		payload["question"] = map[string]any{"question_id": questionID, "text": observation.Summary, "intent_revision": payload["intent_revision"]}
		payload["blockers"] = []any{observation.Summary}
	case "FAIL":
		payload["status"] = "failed"
		payload["blockers"] = []any{observation.Summary}
		if repairEligible {
			repair := asMap(payload["repair"])
			if repair["last_source_sha256"] != asMap(evidence["dependencies"])["source"] {
				repair["pending_failure"] = observation.AttemptID
				payload["repair"] = repair
				payload["status"] = "ready"
				payload["blockers"] = []any{}
			}
		}
	case "REVISE":
		if observation.Stage != "code_review" || asIntOr(payload["correction_rounds"]) >= 1 {
			return blocked("invalid code correction transition")
		}
		payload["correction_rounds"] = asIntOr(payload["correction_rounds"]) + 1
		payload["status"] = "ready"
	case "REPAIR":
		if observation.Stage != "diagnose" || observation.SideEffects != "none" {
			return blocked("invalid repair transition")
		}
		repair := asMap(payload["repair"])
		if repair["pending_failure"] == nil || asIntOr(repair["rounds"]) >= asIntOr(asMap(payload["request"])["max_source_repairs"]) {
			return blocked("repair budget exhausted")
		}
		repair["rounds"] = asIntOr(repair["rounds"]) + 1
		repair["last_source_sha256"] = asMap(evidence["dependencies"])["source"]
		repair["pending_failure"] = nil
		repair["diagnosis_attempt"] = observation.AttemptID
		payload["repair"] = repair
		payload["status"] = "ready"
	default:
		payload["status"] = "blocked"
		payload["blockers"] = []any{observation.Summary}
	}
	if events := anyItems(payload["events"]); events != nil {
		payload["events"] = append(events, map[string]any{"kind": "attempt_recorded", "attempt_id": observation.AttemptID, "stage": observation.Stage, "outcome": outcome, "result_sha256": evidence["result_sha256"]})
	}
	if wasCancelled {
		payload["status"] = "cancelled"
	}
	return nil
}

func dependenciesValue(evidence map[string]any, key string) any {
	dependencies, _ := evidence["dependencies"].(map[string]any)
	return dependencies[key]
}

func strengthenClassification(payload map[string]any, proposal map[string]any) error {
	complexity := asStringOr(proposal["complexity"])
	risk := asStringOr(proposal["risk"])
	flags := anyItems(proposal["impact_flags"])
	if complexity == "" || risk == "" || flags == nil {
		return invalid("inspect proposal must contain complexity, risk and impact_flags")
	}
	if complexity != "S" && complexity != "M" && complexity != "L" {
		return invalid("inspect proposal complexity is invalid")
	}
	if risk != "low" && risk != "medium" && risk != "high" {
		return invalid("inspect proposal risk is invalid")
	}
	if err := validateV1ImpactFlags(flags); err != nil {
		return err
	}
	classification, _ := payload["classification"].(map[string]any)
	if classification == nil {
		classification = map[string]any{}
		payload["classification"] = classification
	}
	if complexityRank(complexity) > complexityRank(asStringOr(classification["complexity"])) {
		classification["complexity"] = complexity
	}
	if riskRank(risk) > riskRank(asStringOr(classification["risk"])) {
		classification["risk"] = risk
	}
	merged := anyItems(classification["impact_flags"])
	seen := map[string]bool{}
	mergedStrings := []any{}
	for _, item := range append(merged, flags...) {
		text, _ := asString(item)
		if text != "" && !seen[text] {
			seen[text] = true
			mergedStrings = append(mergedStrings, text)
		}
	}
	classification["impact_flags"] = mergedStrings
	for _, flag := range mergedStrings {
		if flag == "permissions" || flag == "data_migration" || flag == "data_deletion" {
			classification["risk"] = "high"
		}
	}
	if rationale := asStringOr(proposal["rationale"]); rationale != "" {
		if controlPattern.MatchString(rationale) || secretPattern.MatchString(rationale) {
			return invalid("inspect rationale is invalid")
		}
		classification["rationale"] = rationale
	}
	return nil
}

func complexityRank(value string) int {
	return map[string]int{"S": 1, "M": 2, "L": 3}[value]
}

func riskRank(value string) int {
	return map[string]int{"low": 1, "medium": 2, "high": 3}[value]
}

func prepareAttempt(repository *Repository, outer map[string]any, payload map[string]any, engine EngineIdentity, stage string, hosts ...*ControllerHost) (map[string]any, string, string, map[string]any, error) {
	if err := assertNativePolicyFresh(payload); err != nil {
		return nil, "", "", nil, err
	}
	ledger, err := nativeBudgetAdmission(repository, asStringOr(outer["task_id"]), payload)
	if err != nil {
		return nil, "", "", nil, err
	}
	maxAttempts, _, _, err := requestLimits(payload["request"].(map[string]any))
	if err != nil {
		return nil, "", "", nil, err
	}
	if int64(len(anyItems(payload["attempts"]))) >= maxAttempts {
		return nil, "", "", nil, blocked("finite task attempt limit reached")
	}
	worker := asStringOr(payload["worker_path"])
	manifest, err := sourceManifestWithBaseline(worker, asStringOr(payload["baseline"]), sourcePaths(payload))
	if err != nil {
		return nil, "", "", nil, blocked("cannot bind source manifest: %v", err)
	}
	attemptID, err := randomUUID()
	if err != nil {
		return nil, "", "", nil, err
	}
	started := nowUTC()
	dependencies, err := currentNativeDependencies(payload, stage, manifest)
	if err != nil {
		return nil, "", "", nil, err
	}
	attempt := map[string]any{
		"schema_version": int64(1), "task_id": outer["task_id"], "attempt_id": attemptID,
		"stage": stage, "intent_revision": payload["intent_revision"], "authorization_revision": payload["authorization_revision"],
		"dependencies": dependencies, "source_manifest": manifest, "worker_path": worker,
		"executable": asMap(asMap(payload["request"])["execution_profile"])["executable"], "requested_models": payload["request"].(map[string]any)["models"],
		"started_at": started, "operation_id": attemptID,
		"memory": nativeAttemptMemory(outer, payload, stage, hosts...),
	}
	attemptPath, err := attemptDirectory(repository, asStringOr(outer["task_id"]), attemptID)
	if err != nil {
		return nil, "", "", nil, err
	}
	startHash, err := writeImmutableJSON(filepath.Join(attemptPath, "start.json"), attempt)
	if err != nil {
		return nil, "", "", nil, blocked("cannot register attempt: %v", err)
	}
	_ = startHash
	if _, err := writeImmutableJSON(filepath.Join(attemptPath, "budget-admission.json"), ledger); err != nil {
		return nil, "", "", nil, err
	}
	admission := map[string]any{"schema_version": int64(1), "task_id": outer["task_id"], "attempt_id": attemptID, "stage": stage, "intent_revision": payload["intent_revision"], "authorization_revision": payload["authorization_revision"], "policy_hash": payload["policy_hash"], "request_hash": payload["request_hash"], "budget": asMap(payload["request"])["budget"], "max_attempts": maxAttempts, "timeout_seconds": asMap(payload["request"])["timeout_seconds"], "prior_ledger": ledger}
	if _, err := writeImmutableJSON(filepath.Join(attemptPath, "dispatch-admission.json"), admission); err != nil {
		return nil, "", "", nil, err
	}
	return attempt, attemptID, attemptPath, manifest, nil
}

func prepareProviderRoots(repository *Repository, taskID, attemptID string) (string, string, string, error) {
	contextRoot, err := contextDirectory(repository, taskID, attemptID)
	if err != nil {
		return "", "", "", err
	}
	artifactRoot, err := artifactDirectory(repository, taskID, attemptID)
	if err != nil {
		return "", "", "", err
	}
	if err := ensureGeneratedPathIgnored(repository, filepath.Join(repository.Worktree, ".bsl-flow", "hosts", "native", taskID, attemptID)); err != nil {
		return "", "", "", err
	}
	if err := SafeMkdir(contextRoot); err != nil {
		return "", "", "", err
	}
	if err := SafeMkdir(artifactRoot); err != nil {
		return "", "", "", err
	}
	cancelSignal := filepath.Join(contextRoot, "cancel.signal")
	return contextRoot, artifactRoot, cancelSignal, nil
}

func providerInput(repository *Repository, outer, payload, attempt map[string]any, engine EngineIdentity, operation, contextRoot, artifactRoot, cancelSignal string, prior []ArtifactRef) (ProviderInput, error) {
	if prior == nil {
		prior = []ArtifactRef{}
	}
	stateView := controllerStateView(outer, payload)
	if err := validateV1State(stateView); err != nil {
		return ProviderInput{}, blocked("cannot create provider state view: %v", err)
	}
	return ProviderInput{
		SchemaVersion: 1, Contract: NativeProviderContract, Operation: operation,
		TaskID: asStringOr(outer["task_id"]), StateView: stateView, Attempt: attempt,
		ContextRoot: contextRoot, ArtifactRoot: artifactRoot,
		CanonicalStoreRoot: repository.StorePath, CancelSignal: cancelSignal,
		ProviderContract: providerContract(engine), PriorArtifacts: prior,
	}, nil
}

// stagePriorArtifacts projects only verified immutable artifacts from previous
// attempts into the provider context. The provider deliberately receives the
// legacy-compatible attempts/<id>/ layout, while the canonical store remains
// inaccessible to the worker.
func stagePriorArtifacts(repository *Repository, taskID string, payload map[string]any, contextRoot string) ([]ArtifactRef, error) {
	refs := []ArtifactRef{}
	seen := map[string]bool{}
	for _, raw := range anyItems(payload["evidence"]) {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, blocked("controller evidence contains an invalid prior artifact entry")
		}
		attemptID := asStringOr(entry["attempt_id"])
		if !isUUID(attemptID) {
			return nil, blocked("controller evidence contains an invalid attempt id")
		}
		canonicalAttempt, err := attemptDirectory(repository, taskID, attemptID)
		if err != nil {
			return nil, blocked("cannot resolve prior attempt %s: %v", attemptID, err)
		}
		priorStart, err := nativePriorAttemptStart(repository, taskID, attemptID)
		if err != nil {
			return nil, err
		}
		if !equalJSON(priorStart["authorization_revision"], payload["authorization_revision"]) || !equalJSON(priorStart["intent_revision"], payload["intent_revision"]) {
			continue
		}
		resultHash := asStringOr(entry["result_sha256"])
		if !isSHA256(resultHash) {
			return nil, blocked("prior attempt %s has an invalid result hash", attemptID)
		}
		// The compatibility provider consumes the normalized terminal result.
		// Older native attempts may have only result.json; accept it when it has
		// the normalized shape and reject a wire-only result instead of exposing
		// an unverifiable projection.
		terminalPath := filepath.Join(canonicalAttempt, "terminal.json")
		if _, statErr := os.Lstat(terminalPath); os.IsNotExist(statErr) {
			terminalPath = filepath.Join(canonicalAttempt, "result.json")
		}
		if err := stagePriorFile(contextRoot, filepath.Join("attempts", attemptID, "result.json"), terminalPath, resultHash, "prior terminal result", seen); err != nil {
			return nil, err
		}
		startPath := filepath.Join(canonicalAttempt, "start.json")
		startData, readErr := ReadFileBytes(startPath)
		if readErr != nil {
			return nil, blocked("prior attempt %s start binding is unavailable: %v", attemptID, readErr)
		}
		if err := stagePriorBytes(contextRoot, filepath.Join("attempts", attemptID, "start.json"), startData, fileSHA256(startData), "prior attempt start", seen); err != nil {
			return nil, err
		}
		resultData, readErr := ReadFileBytes(terminalPath)
		if readErr != nil {
			return nil, blocked("prior attempt %s terminal result is unavailable: %v", attemptID, readErr)
		}
		refs = append(refs, ArtifactRef{Path: filepath.ToSlash(filepath.Join("attempts", attemptID, "result.json")), SHA256: resultHash, SizeBytes: int64(len(resultData)), Kind: "raw"})
		refs = append(refs, ArtifactRef{Path: filepath.ToSlash(filepath.Join("attempts", attemptID, "start.json")), SHA256: fileSHA256(startData), SizeBytes: int64(len(startData)), Kind: "raw"})
		for _, item := range anyItems(entry["raw_hashes"]) {
			artifact, ok := item.(map[string]any)
			if !ok {
				return nil, blocked("prior attempt %s contains an invalid raw artifact", attemptID)
			}
			path := asStringOr(artifact["path"])
			relative, err := priorArtifactRelativePath(attemptID, path)
			if err != nil {
				return nil, err
			}
			hash := asStringOr(artifact["sha256"])
			if !isSHA256(hash) {
				return nil, blocked("prior artifact %s has an invalid hash", path)
			}
			size, ok := asInt(artifact["size_bytes"])
			if !ok || size < 0 {
				return nil, blocked("prior artifact %s has an invalid size", path)
			}
			kind := asStringOr(artifact["kind"])
			if !providerArtifactKinds[kind] {
				return nil, blocked("prior artifact %s has an invalid kind", path)
			}
			canonicalPath := filepath.Join(canonicalAttempt, "artifacts", filepath.FromSlash(strings.TrimPrefix(relative, "attempts/"+attemptID+"/")))
			if err := stagePriorFile(contextRoot, relative, canonicalPath, hash, "prior provider artifact", seen); err != nil {
				return nil, err
			}
			refs = append(refs, ArtifactRef{Path: relative, SHA256: hash, SizeBytes: size, Kind: kind})
		}
	}
	budgetRef, err := stageNativeBudget(repository, taskID, payload, contextRoot)
	if err != nil {
		return nil, err
	}
	return append(refs, budgetRef), nil
}

func priorArtifactRelativePath(attemptID, value string) (string, error) {
	if err := validateRelativeNativePath(value, false); err == nil && !strings.HasPrefix(filepath.ToSlash(value), "attempts/") {
		return filepath.ToSlash(filepath.Join("attempts", attemptID, filepath.FromSlash(value))), nil
	}
	prefix := "attempts/" + attemptID + "/"
	normalized := filepath.ToSlash(value)
	if !strings.HasPrefix(normalized, prefix) {
		return "", blocked("prior artifact path is not bound to attempt %s", attemptID)
	}
	if err := validateRelativeNativePath(strings.TrimPrefix(normalized, prefix), false); err != nil {
		return "", blocked("prior artifact path is invalid: %v", err)
	}
	return normalized, nil
}

func stagePriorFile(contextRoot, relative, source, expectedHash, label string, seen map[string]bool) error {
	data, err := ReadFileBytes(source)
	if err != nil {
		return blocked("%s is unavailable: %v", label, err)
	}
	if fileSHA256(data) != expectedHash {
		return blocked("%s bytes changed", label)
	}
	return stagePriorBytes(contextRoot, relative, data, expectedHash, label, seen)
}

func stagePriorBytes(contextRoot, relative string, data []byte, expectedHash, label string, seen map[string]bool) error {
	if err := validateRelativeNativePath(filepath.ToSlash(relative), false); err != nil {
		return blocked("%s path is invalid: %v", label, err)
	}
	key := strings.ToLower(filepath.ToSlash(relative))
	if seen[key] {
		return nil
	}
	seen[key] = true
	destination := filepath.Join(contextRoot, filepath.FromSlash(relative))
	if !withinRoot(contextRoot, destination) {
		return blocked("%s escaped context root", label)
	}
	if existing, err := ReadFileBytes(destination); err == nil {
		if fileSHA256(existing) != expectedHash || string(existing) != string(data) {
			return conflict("conflicting staged %s", label)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return blocked("cannot inspect staged %s: %v", label, err)
	}
	if err := ensureControllerPath(destination); err != nil {
		return err
	}
	if err := AtomicWrite(destination, data, false); err != nil {
		return blocked("cannot stage %s: %v", label, err)
	}
	return nil
}

func persistProviderArtifacts(attemptPath, artifactRoot string, artifacts []ArtifactRef) error {
	for _, artifact := range artifacts {
		if err := validateRelativeNativePath(artifact.Path, false); err != nil || providerArtifactStatePath(artifact.Path) {
			return invalid("provider artifact path is invalid")
		}
		if !isSHA256(artifact.SHA256) || artifact.SizeBytes < 0 || !providerArtifactKinds[artifact.Kind] {
			return invalid("provider artifact descriptor is invalid")
		}
		data, err := readProviderArtifactFile(artifactRoot, artifact.Path)
		if err != nil {
			return err
		}
		if int64(len(data)) != artifact.SizeBytes || fileSHA256(data) != artifact.SHA256 {
			return blocked("provider artifact bytes changed before canonical retention")
		}
		destination := filepath.Join(attemptPath, "artifacts", filepath.FromSlash(artifact.Path))
		if !withinRoot(filepath.Join(attemptPath, "artifacts"), destination) {
			return blocked("provider artifact escaped canonical attempt root")
		}
		if existing, err := ReadFileBytes(destination); err == nil {
			if string(existing) != string(data) {
				return conflict("conflicting canonical provider artifact: %s", artifact.Path)
			}
			continue
		} else if !os.IsNotExist(err) {
			return blocked("cannot inspect canonical provider artifact: %v", err)
		}
		if err := ensureControllerPath(destination); err != nil {
			return err
		}
		if err := AtomicWrite(destination, data, false); err != nil {
			return blocked("cannot retain provider artifact: %v", err)
		}
	}
	return nil
}

func runProvider(ctx context.Context, provider Provider, input ProviderInput) (ExecuteObservation, error) {
	if provider == nil {
		return ExecuteObservation{}, blocked("native controller provider is unavailable")
	}
	return provider.Execute(ctx, input)
}

func measureProvider(ctx context.Context, provider Provider, input MeasureInput) (MeasureObservation, error) {
	if provider == nil {
		return MeasureObservation{}, blocked("native controller provider is unavailable")
	}
	return provider.Measure(ctx, input)
}

func readStoredObservation(path string) (ExecuteObservation, error) {
	data, err := ReadFileBytes(path)
	if err != nil {
		return ExecuteObservation{}, err
	}
	object, err := DecodeObject(data)
	if err != nil {
		return ExecuteObservation{}, err
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return ExecuteObservation{}, err
	}
	var observation ExecuteObservation
	if err := json.Unmarshal(encoded, &observation); err != nil {
		return ExecuteObservation{}, err
	}
	return observation, nil
}

func executeTimeout(payload map[string]any) time.Duration {
	_, timeout, _, err := requestLimits(payload["request"].(map[string]any))
	if err != nil {
		return 30 * time.Minute
	}
	return time.Duration(timeout) * time.Second
}

// commandActivate implements the first native controller transition.  All
// validation, provider measure, worker binding, and candidate-state checks
// happen before the ready revision is published.
func commandActivate(project, id, expectedText, inputPath string, host *ControllerHost) (any, error) {
	return commandActivateNew(project, id, expectedText, inputPath, host)
}

func persistTransportEvidence(attemptPath string, transport *ProviderTransportEvidence) error {
	return retainNativeTransport(attemptPath, transport)
}

func appendAttemptID(payload map[string]any, attemptID string) error {
	items := anyItems(payload["attempts"])
	for _, item := range items {
		if asStringOr(item) == attemptID {
			return conflict("attempt %s is already registered", attemptID)
		}
	}
	payload["attempts"] = append(items, attemptID)
	payload["active_attempt"] = attemptID
	payload["status"] = "running"
	payload["blockers"] = []any{}
	payload["question"] = nil
	return nil
}

func controllerRun(project, id string, host *ControllerHost) (any, error) {
	repository, err := openReady(project)
	if err != nil {
		return nil, err
	}
	task, err := repository.ReadTask(id)
	if err != nil {
		return nil, err
	}
	if task.Lifecycle == "planned" {
		return nil, blocked("planned task %s has no execution authorization; run `task activate` first", id)
	}
	if task.Lifecycle != "controller" {
		return nil, blocked("task %s has no native controller state", id)
	}
	payload, err := cloneObject(task.State["controller"].(map[string]any))
	if err != nil {
		return nil, blocked("cannot copy controller state: %v", err)
	}
	next, err := controllerNext(task.State, payload)
	if err != nil {
		return nil, err
	}
	if asStringOr(next["action"]) != "dispatch" {
		return controllerEnvelope(task.State, payload, next), nil
	}
	provider, engine, err := resolveControllerHost(host)
	if err != nil {
		return nil, err
	}
	if err := validateEngine(payload["engine"]); err != nil {
		return nil, blocked("invalid stored engine binding: %v", err)
	}
	if !equalEngine(engine, engineFromMap(payload["engine"])) {
		return nil, blocked("native engine identity differs from the stored execution binding")
	}
	stage := asStringOr(next["stage"])
	if stage == "implement" || stage == "code_review" || stage == "verify" {
		if err := validateVerificationEligibility(payload); err != nil {
			return nil, err
		}
	}
	// The dispatched attempt and its state view must name the same stage: the
	// provider rejects a view that still carries the previous stage while the
	// controller dispatches the next one.
	payload["stage"] = stage
	attempt, attemptID, attemptPath, _, err := prepareAttempt(repository, task.State, payload, engine, stage, host)
	if err != nil {
		return nil, err
	}
	if err := appendAttemptID(payload, attemptID); err != nil {
		return nil, err
	}
	runningOuter, err := appendControllerRevision(repository, task, payload, task.Revision)
	if err != nil {
		return nil, err
	}
	runningPayload, _ := runningOuter["controller"].(map[string]any)
	contextRoot, artifactRoot, cancelSignal, err := prepareProviderRoots(repository, id, attemptID)
	if err != nil {
		return nil, blocked("attempt registered but provider roots could not be prepared: %v", err)
	}
	if _, err := writeImmutableJSON(filepath.Join(contextRoot, "attempt.json"), attempt); err != nil {
		return nil, blocked("attempt registered but provider context could not be prepared: %v", err)
	}
	priorRefs, err := stagePriorArtifacts(repository, id, payload, contextRoot)
	if err != nil {
		return nil, err
	}
	input, err := providerInput(repository, runningOuter, runningPayload, attempt, engine, "execute", contextRoot, artifactRoot, cancelSignal, priorRefs)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), executeTimeout(payload))
	defer cancel()
	observation, providerErr := runProvider(ctx, provider, input)
	if providerErr != nil {
		_ = persistTransportEvidence(attemptPath, transportFromError(providerErr))
		return nil, blocked("provider execution has no confirmed terminal observation; reconcile attempt %s before retry", attemptID)
	}
	if err := persistTransportEvidence(attemptPath, observation.Transport); err != nil {
		return nil, blocked("provider observation received but transport receipt could not be retained: %v", err)
	}
	return recordNativeObservation(repository, taskFromOuter(runningOuter), payload, observation, attempt, artifactRoot, host)
}

func taskFromOuter(state map[string]any) *Task {
	task, err := taskFromState(state)
	if err != nil {
		return &Task{ID: asStringOr(state["task_id"]), Revision: asIntOr(state["revision"]), State: state}
	}
	return task
}

func asIntOr(value any) int64 {
	parsed, _ := asInt(value)
	return parsed
}

func engineFromMap(value any) EngineIdentity {
	engine, _ := value.(map[string]any)
	return EngineIdentity{Name: asStringOr(engine["name"]), ContractVersion: asIntOr(engine["contract_version"]), Provider: asStringOr(engine["provider"]), HostSHA256: asStringOr(engine["host_sha256"]), ProviderSHA256: asStringOr(engine["provider_sha256"]), AssetManifestSHA256: asStringOr(engine["asset_manifest_sha256"])}
}

func transportFromError(err error) *ProviderTransportEvidence {
	var typed interface {
		TransportEvidence() ProviderTransportEvidence
	}
	if errors.As(err, &typed) {
		evidence := typed.TransportEvidence()
		return &evidence
	}
	return nil
}

func validateVerificationEligibility(payload map[string]any) error {
	request, _ := payload["request"].(map[string]any)
	if request == nil {
		return blocked("controller request is missing")
	}
	if asStringOr(request["mode"]) != "implement" {
		return nil
	}
	classification, _ := payload["classification"].(map[string]any)
	flags := anyItems(classification["impact_flags"])
	kinds := map[string]bool{}
	for _, raw := range anyItems(request["criteria"]) {
		if criterion, ok := raw.(map[string]any); ok {
			kinds[asStringOr(criterion["kind"])] = true
		}
	}
	for _, raw := range flags {
		flag, _ := asString(raw)
		required := map[string]string{"posting": "integration", "data_exchange": "integration", "permissions": "integration", "data_migration": "integration", "data_deletion": "integration", "form_flow": "ui", "external_artifact": "external_artifact"}[flag]
		if required != "" && !kinds[required] {
			return blocked("impact %s requires %s evidence", flag, required)
		}
	}
	return nil
}

func commandControllerNext(project, id string) (any, error) {
	repository, err := openReadOnly(project)
	if err != nil {
		return nil, err
	}
	task, err := repository.ReadTask(id)
	if err != nil {
		return nil, err
	}
	if task.Lifecycle == "planned" {
		return nil, blocked("planned task %s has no execution authorization; run `task activate` first", id)
	}
	payload, ok := task.State["controller"].(map[string]any)
	if !ok {
		return nil, blocked("task %s has no native controller state", id)
	}
	next, err := controllerNext(task.State, payload)
	if err != nil {
		return nil, err
	}
	return map[string]any{"schema_version": int64(1), "task_id": id, "revision": task.Revision, "next": next}, nil
}

func commandControllerStatus(project, id string) (any, error) {
	repository, err := openReadOnly(project)
	if err != nil {
		return nil, err
	}
	row, err := repository.ResolveRow(id)
	if err != nil {
		return nil, err
	}
	if row.Source != "repository" {
		return nil, blocked("task %s is owned by the legacy controller", id)
	}
	task, err := repository.ReadTask(id)
	if err != nil {
		return nil, err
	}
	payload, ok := task.State["controller"].(map[string]any)
	if !ok {
		return nil, blocked("planned repository task requires activation before status")
	}
	next, err := controllerNext(task.State, payload)
	if err != nil {
		return nil, err
	}
	return controllerEnvelope(task.State, payload, next), nil
}

func commandControllerCancel(project, id string) (any, error) {
	repository, err := openReady(project)
	if err != nil {
		return nil, err
	}
	task, err := repository.ReadTask(id)
	if err != nil {
		return nil, err
	}
	if task.Lifecycle == "planned" {
		return nil, blocked("planned task %s has no execution authorization; run `task activate` first", id)
	}
	payload, err := cloneObject(task.State["controller"].(map[string]any))
	if err != nil {
		return nil, blocked("invalid controller state: %v", err)
	}
	if asStringOr(payload["status"]) != "cancelled" {
		if active := asStringOr(payload["active_attempt"]); active != "" {
			if err := writeCancelSignal(repository, id, active); err != nil {
				return nil, blocked("cancel signal could not be recorded: %v", err)
			}
		}
		payload["status"] = "cancelled"
		payload["blockers"] = []any{"new dispatch is disabled; cancellation is not rollback"}
		payload["question"] = nil
		state, err := appendControllerRevision(repository, task, payload, task.Revision)
		if err != nil {
			return nil, err
		}
		task, _ = taskFromState(state)
		payload, _ = state["controller"].(map[string]any)
	}
	next, err := controllerNext(task.State, payload)
	if err != nil {
		return nil, err
	}
	return controllerEnvelope(task.State, payload, next), nil
}

func commandControllerAccept(project, id string, hosts ...*ControllerHost) (any, error) {
	repository, err := openReady(project)
	if err != nil {
		return nil, err
	}
	task, err := repository.ReadTask(id)
	if err != nil {
		return nil, err
	}
	if task.Lifecycle != "controller" {
		return nil, blocked("task %s must be activated before acceptance", id)
	}
	payload, err := cloneObject(task.State["controller"].(map[string]any))
	if err != nil {
		return nil, err
	}
	next, err := controllerNext(task.State, payload)
	if err != nil {
		return nil, err
	}
	if asStringOr(next["action"]) != "accept" {
		return nil, blocked("acceptance is not available at %s: %s", asStringOr(next["stage"]), asStringOr(next["action"]))
	}
	manifest, err := sourceManifestWithBaseline(asStringOr(payload["worker_path"]), asStringOr(payload["baseline"]), []string{"."})
	if err != nil {
		return nil, blocked("cannot bind acceptance source manifest: %v", err)
	}
	gates := []any{}
	for _, stage := range routeForController(payload) {
		if stage == "acceptance" {
			break
		}
		evidence, ok := latestEvidence(payload, stage)
		if !ok || asStringOr(evidence["outcome"]) != "PASS" || !controllerEvidenceFresh(task.State, payload, evidence) {
			return nil, blocked("stale or missing %s evidence at acceptance", stage)
		}
		gates = append(gates, map[string]any{"stage": stage, "attempt_id": evidence["attempt_id"], "result_sha256": evidence["result_sha256"]})
	}
	currentManifest, err := sourceManifestWithBaseline(asStringOr(payload["worker_path"]), asStringOr(payload["baseline"]), []string{"."})
	if err != nil || !equalJSON(currentManifest, manifest) {
		return nil, blocked("acceptance source changed while checking gates")
	}
	if err := assertNativePolicyFresh(payload); err != nil {
		return nil, err
	}
	receipt := map[string]any{"schema_version": int64(1), "task_id": id, "intent_revision": payload["intent_revision"], "authorization_revision": payload["authorization_revision"], "engine": payload["engine"], "mode": asStringOr(payload["request"].(map[string]any)["mode"]), "intent_hash": payload["intent_hash"], "policy_hash": payload["policy_hash"], "baseline": payload["baseline"], "source_manifest": manifest, "gates": gates, "verdict": "PASS", "scope": acceptanceScope(payload)}
	receiptHash, err := Hash(receipt)
	if err != nil {
		return nil, err
	}
	receiptPath := filepath.Join(repository.StorePath, "tasks", id, "acceptance", receiptHash+".json")
	if _, err := writeImmutableJSON(receiptPath, receipt); err != nil {
		return nil, blocked("acceptance receipt could not be retained: %v", err)
	}
	for _, raw := range anyItems(payload["acceptances"]) {
		if item, ok := raw.(map[string]any); ok && asStringOr(item["sha256"]) == receiptHash {
			payload["status"] = "completed"
			payload["stage"] = "acceptance"
			next, _ := controllerNext(task.State, payload)
			invokeNativeMemory(task.State, payload, "extract-acceptance", "", nil, "", receipt, receiptHash, nil, hosts...)
			return controllerEnvelope(task.State, payload, next), nil
		}
	}
	acceptance := map[string]any{"sha256": receiptHash, "path": receiptPath, "verdict": "PASS", "mode": asStringOr(payload["request"].(map[string]any)["mode"]), "intent_revision": payload["intent_revision"]}
	payload["acceptances"] = append(anyItems(payload["acceptances"]), acceptance)
	payload["status"] = "completed"
	payload["stage"] = "acceptance"
	payload["blockers"] = []any{}
	state, err := appendControllerRevision(repository, task, payload, task.Revision)
	if err != nil {
		return nil, err
	}
	payload, _ = state["controller"].(map[string]any)
	invokeNativeMemory(state, payload, "extract-acceptance", "", nil, "", receipt, receiptHash, nil, hosts...)
	next, err = controllerNext(state, payload)
	if err != nil {
		return nil, err
	}
	return controllerEnvelope(state, payload, next), nil
}

func acceptanceScope(payload map[string]any) string {
	request, _ := payload["request"].(map[string]any)
	if asStringOr(request["mode"]) == "analysis_only" {
		return "analysis"
	}
	return "source-and-declared-checks"
}

func commandControllerResume(project, id string, host *ControllerHost) (any, error) {
	repository, err := openReady(project)
	if err != nil {
		return nil, err
	}
	task, err := repository.ReadTask(id)
	if err != nil {
		return nil, err
	}
	if task.Lifecycle != "controller" {
		return nil, blocked("planned task %s has no execution authorization; run `task activate` first", id)
	}
	payload, err := cloneObject(task.State["controller"].(map[string]any))
	if err != nil {
		return nil, blocked("invalid controller state: %v", err)
	}
	active := asStringOr(payload["active_attempt"])
	if active != "" {
		attemptPath, err := attemptDirectory(repository, id, active)
		if err != nil {
			return nil, err
		}
		resultPath := filepath.Join(attemptPath, "result.json")
		if _, statErr := os.Lstat(resultPath); os.IsNotExist(statErr) {
			resultPath = filepath.Join(attemptPath, nativeTransportStdoutFile)
		} else if statErr != nil {
			return nil, blocked("cannot inspect retained result: %v", statErr)
		}
		if _, statErr := os.Lstat(resultPath); statErr == nil {
			observation, readErr := readStoredObservation(resultPath)
			if readErr != nil {
				return nil, blocked("retained terminal result is unreadable; reconciliation is required: %v", readErr)
			}
			start, err := readStoredAttempt(attemptPath)
			if err != nil {
				return nil, err
			}
			artifactRoot, err := artifactDirectory(repository, id, active)
			if err != nil {
				return nil, err
			}
			return recordNativeObservation(repository, task, payload, observation, start, artifactRoot, host)
		}
		// A running attempt with no retained terminal result is intentionally not
		// dispatched again. The operator must reconcile the exact effect first.
		return nil, blocked("attempt %s has no confirmed terminal result; reconcile before retry", active)
	}
	if asStringOr(payload["status"]) == "cancelled" {
		return nil, blocked("cancelled task requires an explicit update before resume")
	}
	return controllerRun(project, id, host)
}

func readStoredAttempt(attemptPath string) (map[string]any, error) {
	data, err := ReadFileBytes(filepath.Join(attemptPath, "start.json"))
	if err != nil {
		return nil, err
	}
	return DecodeObject(data)
}

func asMap(value any) map[string]any {
	if result, ok := value.(map[string]any); ok {
		return result
	}
	return map[string]any{}
}

func commandControllerUpdate(project, id, inputPath string, host *ControllerHost) (any, error) {
	return commandControllerUpdateNew(project, id, inputPath, host)
}

func commandControllerRecord(project, id, attemptID, inputPath string, hosts ...*ControllerHost) (any, error) {
	repository, err := openReady(project)
	if err != nil {
		return nil, err
	}
	task, err := repository.ReadTask(id)
	if err != nil {
		return nil, err
	}
	if task.Lifecycle != "controller" {
		return nil, blocked("task %s has no native controller state", id)
	}
	payload, err := cloneObject(task.State["controller"].(map[string]any))
	if err != nil {
		return nil, err
	}
	if attemptID == "" {
		attemptID = asStringOr(payload["active_attempt"])
	}
	if !isUUID(attemptID) {
		return nil, invalid("--attempt must be a lowercase UUID")
	}
	registered := false
	for _, raw := range anyItems(payload["attempts"]) {
		if raw == attemptID {
			registered = true
		}
	}
	if !registered {
		return nil, blocked("record requires the exact registered active attempt")
	}
	if asStringOr(payload["active_attempt"]) != attemptID {
		return recordedNativeResult(repository, task, payload, attemptID, inputPath, hosts...)
	}
	attemptPath, err := attemptDirectory(repository, id, attemptID)
	if err != nil {
		return nil, err
	}
	observationPath := filepath.Join(attemptPath, "result.json")
	if inputPath != "" {
		observationPath = inputPath
	} else if _, statErr := os.Lstat(observationPath); os.IsNotExist(statErr) {
		observationPath = filepath.Join(attemptPath, nativeTransportStdoutFile)
	} else if statErr != nil {
		return nil, blocked("cannot inspect retained result: %v", statErr)
	}
	observation, err := readStoredObservation(observationPath)
	if err != nil {
		return nil, blocked("terminal provider observation is missing: %v", err)
	}
	start, err := readStoredAttempt(attemptPath)
	if err != nil {
		return nil, blocked("attempt binding is missing: %v", err)
	}
	if start["task_id"] != id || start["attempt_id"] != attemptID || !equalJSON(start["intent_revision"], payload["intent_revision"]) || !equalJSON(start["authorization_revision"], payload["authorization_revision"]) {
		return nil, blocked("attempt belongs to another execution binding")
	}
	artifactRoot, err := artifactDirectory(repository, id, attemptID)
	if err != nil {
		return nil, err
	}
	return recordNativeObservation(repository, task, payload, observation, start, artifactRoot, hosts...)
}

func temporaryMeasureRoots(repository *Repository) (string, string, string, error) {
	base := filepath.Join(repository.Worktree, ".bsl-flow", "hosts", "native", "measure")
	if err := ensureGeneratedPathIgnored(repository, base); err != nil {
		return "", "", "", err
	}
	if err := SafeMkdir(base); err != nil {
		return "", "", "", err
	}
	root, err := os.MkdirTemp(base, ".measure-")
	if err != nil {
		return "", "", "", err
	}
	contextRoot := filepath.Join(root, "context")
	artifactRoot := filepath.Join(root, "artifacts")
	if err := SafeMkdir(contextRoot); err != nil {
		return "", "", "", err
	}
	if err := SafeMkdir(artifactRoot); err != nil {
		return "", "", "", err
	}
	return contextRoot, artifactRoot, filepath.Join(contextRoot, "cancel.signal"), nil
}

func strconvParseRevision(value string) (int64, error) {
	if value == "" {
		return 0, invalid("--expected-revision is required")
	}
	parsed := int64(0)
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, invalid("--expected-revision must be a non-negative integer")
		}
		if parsed > 99999999999999999 {
			return 0, invalid("--expected-revision is too large")
		}
		parsed = parsed*10 + int64(character-'0')
	}
	return parsed, nil
}

func validateStateForCandidate(state map[string]any, id string) error {
	return validateState(state, id)
}
