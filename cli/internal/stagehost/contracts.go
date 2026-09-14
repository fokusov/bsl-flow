package stagehost

import (
	"math"
	"path/filepath"
	"regexp"
	"strings"
)

// This file ports the closed request/state contracts of Task.Contracts.ps1,
// Task.Coverage.ps1 and the native criterion shape of Task.Runtime.ps1 with
// their exact diagnostics, so a legacy and a native provider classify the
// same input identically.

var criterionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var modelPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)
var threePartVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
var nativeNamePattern = regexp.MustCompile(`^[A-Za-z\x{0410}-\x{042F}\x{0430}-\x{044F}_][A-Za-z\x{0410}-\x{042F}\x{0430}-\x{044F}0-9_]{0,127}$`)
var nativeTestIDPattern = regexp.MustCompile(`^[^\.\s]+(?:\.[^\.\s]+)+$`)

func assertProvenance(value any) error {
	provenance, err := assertFields(value, []string{"source", "reference", "text"}, nil, "provenance")
	if err != nil {
		return err
	}
	if provenance["source"] != "user" {
		return invalidf("only a trusted operator can relay user input; worker output is not authorization.")
	}
	if err := assertText(provenance["reference"], "provenance.reference", 2048); err != nil {
		return err
	}
	return assertText(provenance["text"], "provenance.text")
}

func assertBudget(budget any) error {
	object, err := assertFields(budget, []string{"currency", "limit", "reservation"}, nil, "budget")
	if err != nil {
		return err
	}
	if object["currency"] != "USD" {
		return invalidf("only a USD budget currency is supported.")
	}
	for _, name := range []string{"limit", "reservation"} {
		value := object[name]
		if value == nil {
			continue
		}
		number, ok := asFloat(value)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
			if !ok {
				return invalidf("budget.%s must be a number or null.", name)
			}
			return invalidf("budget.%s must be a non-negative finite number.", name)
		}
	}
	if object["limit"] == nil {
		if reservation, ok := asFloat(object["reservation"]); !ok || reservation != 0 {
			return invalidf("budget.reservation must be zero when no monetary limit is enforced.")
		}
	}
	return nil
}

func assertImpactFlags(value any) error {
	flags, ok := asArray(value)
	if !ok {
		return invalidf("impact_flags must be an array.")
	}
	known := map[string]bool{"permissions": true, "data_migration": true, "data_deletion": true, "posting": true, "data_exchange": true, "form_flow": true, "external_artifact": true, "ambiguous_business_rule": true}
	for _, raw := range flags {
		flag, isString := raw.(string)
		if !isString || !known[flag] {
			return invalidf("unknown impact flag %v", raw)
		}
	}
	return nil
}

func assertNativeCriterion(criterion map[string]any) error {
	native, err := assertFields(getValue(criterion, "native_1c", nil), []string{"source_root", "extension", "module", "platform_version", "executable_sha256", "authorized_operations", "authorization_reference"}, []string{"reuse_load_attempt"}, "criterion.native_1c")
	if err != nil {
		return err
	}
	if native == nil {
		return invalidf("native_1c contract is required.")
	}
	if criterion["kind"] != "integration" {
		return invalidf("native_1c is supported only for integration criteria.")
	}
	if err := assertRelativePath(asStringOr(native["source_root"])); err != nil {
		return err
	}
	for _, name := range []string{"extension", "module", "platform_version", "authorization_reference"} {
		if err := assertText(native[name], "criterion.native_1c."+name); err != nil {
			return err
		}
	}
	if !nativeNamePattern.MatchString(asStringOr(native["extension"])) || !nativeNamePattern.MatchString(asStringOr(native["module"])) {
		return invalidf("unsafe native 1C extension or module name.")
	}
	if !regexp.MustCompile(`^8\.3\.\d+\.\d+$`).MatchString(asStringOr(native["platform_version"])) {
		return invalidf("invalid native 1C platform version.")
	}
	if err := assertSHA256(native["executable_sha256"], "criterion.native_1c.executable_sha256"); err != nil {
		return invalidf("native executable SHA-256 must be lowercase hexadecimal.")
	}
	executable, ok := criterion["executable"].(string)
	if !ok || !isAbsolutePath(executable) || !strings.EqualFold(filepath.Base(executable), "1cv8.exe") {
		return invalidf("native executable must be an absolute 1cv8.exe path.")
	}
	arguments, ok := asArray(criterion["arguments"])
	if !ok || len(arguments) != 0 {
		return invalidf("native 1C criteria do not accept free arguments.")
	}
	protected, ok := asArray(criterion["protected_paths"])
	if !ok || len(protected) == 0 {
		return invalidf("native 1C criteria require protected_paths for declared tests and fixtures.")
	}
	for _, raw := range protected {
		if err := assertRelativePath(asStringOr(raw)); err != nil {
			return err
		}
	}
	target, ok := criterion["target"].(string)
	if !ok || !isAbsolutePath(target) {
		return invalidf("native 1C target must be an absolute FILE directory.")
	}
	if _, err := assertRuntimeTargetKey(target); err != nil {
		return err
	}
	operations := "inventory,load,update,test"
	if reuse := getValue(native, "reuse_load_attempt", nil); reuse != nil {
		if err := assertUUID(reuse); err != nil {
			return err
		}
		operations = "inventory,test"
	}
	authorized, ok := asArray(native["authorized_operations"])
	if !ok || strings.Join(anyToStrings(authorized), ",") != operations {
		return invalidf("authorized_operations must be exactly %s.", operations)
	}
	expected, ok := asArray(criterion["expected_tests"])
	if !ok || len(expected) == 0 {
		return invalidf("unique class-qualified expected tests are required.")
	}
	seen := map[string]bool{}
	for _, raw := range expected {
		id, isString := raw.(string)
		if !isString || !nativeTestIDPattern.MatchString(id) {
			return invalidf("expected native test IDs must be classname.name.")
		}
		if seen[id] {
			return invalidf("unique class-qualified expected tests are required.")
		}
		seen[id] = true
	}
	return nil
}

// assertRuntimeTargetKey mirrors the observable gates of
// Get-BFRuntimeTargetKey: the target must be rooted and carry the FILE
// database marker. The physical identity resolution behind it is a
// Windows-only runtime concern and never runs in the source-only provider.
func assertRuntimeTargetKey(target string) (string, error) {
	if !isAbsolutePath(target) {
		return "", invalidf("native 1C target must be an absolute FILE directory.")
	}
	resolved, err := safePath(target)
	if err != nil {
		return "", err
	}
	marker := filepath.Join(resolved, "1Cv8.1CD")
	if !isRegularFile(marker) {
		return "", blockedf("FILE target marker is required to resolve physical target identity.")
	}
	hash, err := hashValue(strings.ToLower(resolved))
	if err != nil {
		return "", err
	}
	return hash, nil
}

func anyToStrings(values []any) []string {
	result := make([]string, 0, len(values))
	for _, raw := range values {
		if text, ok := raw.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func assertCriteria(criteria any) error {
	items, ok := asArray(criteria)
	if !ok {
		return invalidf("criteria must be an array.")
	}
	ids := map[string]bool{}
	nativeCount := 0
	for _, raw := range items {
		criterion, err := assertFields(raw, []string{"id", "observation", "kind"}, []string{"path", "contains", "executable", "arguments", "report", "expected_tests", "target", "profile", "retry_safe", "protected_paths", "native_1c"}, "criterion")
		if err != nil {
			return err
		}
		id, isString := criterion["id"].(string)
		if !isString || !criterionIDPattern.MatchString(id) || ids[id] {
			return invalidf("criterion ids must be safe and unique.")
		}
		ids[id] = true
		if err := assertText(criterion["observation"], "criterion.observation"); err != nil {
			return err
		}
		if retry, present := criterion["retry_safe"]; present && retry != nil {
			if _, isBool := asBool(retry); !isBool {
				return invalidf("criterion.retry_safe must be boolean.")
			}
		}
		if _, present := criterion["protected_paths"]; present {
			protected, ok := asArray(criterion["protected_paths"])
			if !ok || len(protected) == 0 {
				return invalidf("protected_paths must be a nonempty array.")
			}
			generated := regexp.MustCompile(`(^|[\\/])\.(bsl-flow|bsl-flow-worker|git)([\\/]|$)`)
			for _, rawPath := range protected {
				path, isPathString := rawPath.(string)
				if !isPathString {
					return invalidf("protected_paths must be a nonempty array.")
				}
				if err := assertRelativePath(path); err != nil {
					return err
				}
				if generated.MatchString(path) {
					return invalidf("protected test inputs must be source files, not generated/admin paths.")
				}
			}
		}
		kind, _ := criterion["kind"].(string)
		switch kind {
		case "file_assertion", "static", "unit", "integration", "ui", "external_artifact":
		default:
			return invalidf("unsupported criterion kind.")
		}
		if kind == "file_assertion" {
			if err := assertRelativePath(asStringOr(getValue(criterion, "path", ""))); err != nil {
				return err
			}
			if err := assertText(getValue(criterion, "contains", nil), "criterion.contains"); err != nil {
				return err
			}
		} else if kind != "external_artifact" {
			if err := assertText(criterion["executable"], "criterion.executable"); err != nil {
				return err
			}
			executable, _ := criterion["executable"].(string)
			if !isAbsolutePath(executable) {
				return invalidf("test executable must be absolute.")
			}
			arguments, present := criterion["arguments"]
			argumentList, ok := asArray(arguments)
			if !present || !ok {
				return invalidf("test arguments must be an array.")
			}
			for _, argument := range argumentList {
				text, isText := argument.(string)
				if !isText || strings.ContainsAny(text, "\x00\r\n") {
					return invalidf("test arguments must be single-line strings.")
				}
			}
			report := asStringOr(getValue(criterion, "report", ""))
			if err := assertRelativePath(report); err != nil {
				return err
			}
			if !regexp.MustCompile(`^\.bsl-flow-worker[/\\]`).MatchString(report) {
				return invalidf("raw test reports belong under .bsl-flow-worker/ in the worktree.")
			}
			expected, present := criterion["expected_tests"]
			expectedList, ok := asArray(expected)
			if !present || !ok || len(expectedList) == 0 {
				return invalidf("exact expected test names are required.")
			}
			if kind == "integration" || kind == "ui" {
				if err := assertText(getValue(criterion, "target", nil), "criterion.target"); err != nil {
					return err
				}
			}
		}
		if _, present := criterion["native_1c"]; present {
			if err := assertNativeCriterion(criterion); err != nil {
				return err
			}
			nativeCount++
		}
	}
	if nativeCount > 1 {
		return invalidf("the native route supports one target operation per task.")
	}
	return nil
}

func assertRequirements(request map[string]any) error {
	if !hasProperty(request, "requirements") {
		return nil
	}
	requirements, ok := asArray(getValue(request, "requirements", nil))
	if getValue(request, "requirements", nil) == nil {
		return invalidf("requirements cannot be null when supplied.")
	}
	if !ok || len(requirements) == 0 {
		return invalidf("requirements must be a nonempty array when supplied.")
	}
	criteria, _ := asArray(request["criteria"])
	criterionIDs := make([]string, 0, len(criteria))
	for _, raw := range criteria {
		criterion, _ := asObject(raw)
		criterionIDs = append(criterionIDs, asStringOr(criterion["id"]))
	}
	known := map[string]bool{}
	for _, id := range criterionIDs {
		known[id] = true
	}
	requirementIDs := map[string]bool{}
	mapped := map[string]bool{}
	for _, raw := range requirements {
		requirement, err := assertFields(raw, []string{"id", "text", "criterion_ids"}, nil, "requirement")
		if err != nil {
			return err
		}
		id, isString := requirement["id"].(string)
		if !isString || !criterionIDPattern.MatchString(id) || requirementIDs[id] {
			return invalidf("requirement ids must be safe and unique.")
		}
		requirementIDs[id] = true
		if err := assertText(requirement["text"], "requirement.text"); err != nil {
			return err
		}
		ids, ok := asArray(requirement["criterion_ids"])
		if !ok || len(ids) == 0 {
			return invalidf("every requirement needs nonempty criterion_ids.")
		}
		local := map[string]bool{}
		for _, rawID := range ids {
			criterionID, isString := rawID.(string)
			if !isString || local[criterionID] {
				return invalidf("requirement criterion_ids must be unique strings.")
			}
			local[criterionID] = true
			if !known[criterionID] {
				return invalidf("requirement refers to an unknown criterion.")
			}
			mapped[criterionID] = true
		}
	}
	for _, id := range criterionIDs {
		if !mapped[id] {
			return invalidf("supplied requirements must map every criterion.")
		}
	}
	for _, raw := range criteria {
		criterion, _ := asObject(raw)
		kind := asStringOr(criterion["kind"])
		if kind != "file_assertion" && kind != "external_artifact" {
			protected, _ := asArray(getValue(criterion, "protected_paths", []any{}))
			if len(protected) == 0 {
				return invalidf("requirement review needs protected test inputs for executable criteria.")
			}
		}
	}
	return nil
}

func assertRuntimePin(runtime any) error {
	object, err := assertFields(runtime, []string{"executable", "sha256", "version", "packages"}, nil, "execution_profile.runtime")
	if err != nil {
		return err
	}
	if err := assertText(object["executable"], "execution_profile.runtime.executable"); err != nil {
		return err
	}
	executable := asStringOr(object["executable"])
	if !isAbsolutePath(executable) || pathExtension(executable) != ".exe" || !sha256Pattern.MatchString(asStringOr(object["sha256"])) {
		return invalidf("pinned runtime requires an absolute native executable and SHA-256.")
	}
	if !threePartVersion.MatchString(asStringOr(object["version"])) {
		return invalidf("pinned runtime version must be an exact three-part version.")
	}
	packages, ok := asArray(object["packages"])
	if !ok || len(packages) == 0 {
		return invalidf("pinned runtime requires at least one exact package.")
	}
	seen := map[string]bool{}
	hasLxml := false
	for _, raw := range packages {
		pkg, err := assertFields(raw, []string{"name", "version"}, nil, "execution_profile.runtime.package")
		if err != nil {
			return err
		}
		name, isString := pkg["name"].(string)
		if !isString || !criterionIDPattern.MatchString(name) || seen[name] {
			return invalidf("invalid or duplicate pinned runtime package.")
		}
		seen[name] = true
		if name == "lxml" {
			hasLxml = true
		}
		if err := assertText(pkg["version"], "execution_profile.runtime.package.version", 64); err != nil {
			return err
		}
	}
	if !hasLxml {
		return invalidf("the pinned cc-1c-skills runtime must declare the mandatory lxml package.")
	}
	return nil
}

var unicaToolPattern = regexp.MustCompile(`^unica\.(?:(cf|cfe|meta|form|skd|dcs|mxl|role|subsystem|interface|template|code)\.(info|validate|diff|search|diagnostics|add|edit|remove|compile|init|borrow|patch_method)|project\.map|code\.patch)$`)

func assertExecutionProfile(profile any) error {
	object, err := assertFields(profile, []string{"provider", "executable", "executable_sha256", "sandbox", "toolset", "denied_read_roots"}, []string{"unica", "codex_skills_sha256", "runtime"}, "execution_profile")
	if err != nil {
		return err
	}
	provider, _ := object["provider"].(string)
	if provider != "codex" && provider != "opencode" {
		return invalidf("unsupported managed provider.")
	}
	if provider == "codex" {
		if !sha256Pattern.MatchString(asStringOr(getValue(object, "codex_skills_sha256", ""))) {
			return invalidf("Codex requires a pinned automatic skill inventory.")
		}
	} else if hasProperty(object, "codex_skills_sha256") {
		return invalidf("OpenCode cannot use a Codex skill inventory.")
	}
	if err := assertText(object["executable"], "execution_profile.executable"); err != nil {
		return err
	}
	executable := asStringOr(object["executable"])
	if !isAbsolutePath(executable) || pathExtension(executable) != ".exe" || !sha256Pattern.MatchString(asStringOr(object["executable_sha256"])) {
		return invalidf("provider requires an absolute native executable and SHA-256.")
	}
	sandbox, err := assertFields(object["sandbox"], []string{"executable", "sha256"}, nil, "sandbox")
	if err != nil {
		return err
	}
	if err := assertText(sandbox["executable"], "sandbox.executable"); err != nil {
		return err
	}
	sandboxExecutable := asStringOr(sandbox["executable"])
	if !isAbsolutePath(sandboxExecutable) || pathExtension(sandboxExecutable) != ".exe" || !sha256Pattern.MatchString(asStringOr(sandbox["sha256"])) {
		return invalidf("sandbox requires an absolute native executable and SHA-256.")
	}
	toolset, err := assertFields(object["toolset"], []string{"name", "root", "sha256"}, nil, "toolset")
	if err != nil {
		return err
	}
	toolsetName, _ := toolset["name"].(string)
	toolsetRoot, rootIsString := toolset["root"].(string)
	if (toolsetName != "unica" && toolsetName != "cc-1c-skills") || !rootIsString || !isAbsolutePath(toolsetRoot) || !sha256Pattern.MatchString(asStringOr(toolset["sha256"])) {
		return invalidf("invalid toolset identity.")
	}
	denied, ok := asArray(object["denied_read_roots"])
	if !ok || len(denied) == 0 {
		return invalidf("benchmark profiles require explicit private read-denied roots.")
	}
	for _, raw := range denied {
		path, isString := raw.(string)
		if !isString || !isAbsolutePath(path) {
			return invalidf("denied roots must be absolute paths.")
		}
	}
	unica := getValue(object, "unica", nil)
	if toolsetName == "unica" {
		unicaObject, err := assertFields(unica, []string{"plugin_root", "bootstrap_sha256", "manifest_sha256", "runtime_cache", "allowed_tools"}, nil, "unica")
		if err != nil {
			return err
		}
		for _, field := range []string{"plugin_root", "runtime_cache"} {
			if err := assertText(unicaObject[field], field); err != nil {
				return err
			}
			if !isAbsolutePath(asStringOr(unicaObject[field])) {
				return invalidf("Unica paths must be absolute.")
			}
		}
		for _, field := range []string{"bootstrap_sha256", "manifest_sha256"} {
			if !sha256Pattern.MatchString(asStringOr(unicaObject[field])) {
				return invalidf("missing Unica executable/manifest identity.")
			}
		}
		tools, ok := asArray(unicaObject["allowed_tools"])
		if !ok || len(tools) == 0 {
			return invalidf("exact Unica source tool allowlist required.")
		}
		for _, raw := range tools {
			name, isString := raw.(string)
			if !isString || !unicaToolPattern.MatchString(name) {
				return invalidf("Unica tool is outside the source-only profile: %v", raw)
			}
		}
	} else if hasProperty(object, "unica") {
		return invalidf("cc-1c-skills profile cannot load Unica MCP.")
	}
	runtime := getValue(object, "runtime", nil)
	if toolsetName == "cc-1c-skills" {
		if runtime == nil {
			return invalidf("cc-1c-skills requires a pinned runtime.")
		}
		if err := assertRuntimePin(runtime); err != nil {
			return err
		}
	} else if hasProperty(object, "runtime") {
		return invalidf("Unica has a separate runtime contract; a pinned runtime block is forbidden.")
	}
	return nil
}

// assertRequest mirrors Assert-BFRequest with exact legacy diagnostics.
func assertRequest(request any) error {
	object, err := assertFields(request,
		[]string{"schema_version", "request_id", "prompt", "mode", "analysis_goal", "complexity", "risk", "impact_flags", "criteria", "provenance", "models"},
		[]string{"source_paths", "require_spec_review", "require_code_review", "max_attempts", "timeout_seconds", "max_source_repairs", "requirements", "execution_profile", "budget"},
		"request")
	if err != nil {
		return err
	}
	if version, ok := asInteger(object["schema_version"]); !ok || version != 1 {
		return invalidf("unsupported request schema_version.")
	}
	if err := assertUUID(object["request_id"]); err != nil {
		return err
	}
	if err := assertText(object["prompt"], "prompt"); err != nil {
		return err
	}
	if err := assertProvenance(object["provenance"]); err != nil {
		return err
	}
	mode, _ := object["mode"].(string)
	analysisGoal, _ := object["analysis_goal"].(string)
	if (mode != "analysis_only" && mode != "implement") || (analysisGoal != "analysis" && analysisGoal != "specification") {
		return invalidf("unsupported task mode or analysis goal.")
	}
	complexity, _ := object["complexity"].(string)
	risk, _ := object["risk"].(string)
	if complexity != "S" && complexity != "M" && complexity != "L" || risk != "low" && risk != "medium" && risk != "high" {
		return invalidf("unsupported classification.")
	}
	if err := assertImpactFlags(object["impact_flags"]); err != nil {
		return err
	}
	if err := assertCriteria(object["criteria"]); err != nil {
		return err
	}
	if err := assertRequirements(object); err != nil {
		return err
	}
	criteria, _ := asArray(object["criteria"])
	if mode == "implement" && len(criteria) == 0 {
		return invalidf("implementation requires observable acceptance criteria before dispatch.")
	}
	models, err := assertFields(object["models"], []string{"worker", "worker_effort", "reviewer", "reviewer_effort"}, nil, "models")
	if err != nil {
		return err
	}
	profile := getValue(object, "execution_profile", nil)
	if hasProperty(object, "execution_profile") {
		if err := assertExecutionProfile(profile); err != nil {
			return err
		}
	}
	budget := getValue(object, "budget", nil)
	if profile != nil {
		if budget == nil {
			return invalidf("a managed execution profile requires an explicit budget.")
		}
		if err := assertBudget(budget); err != nil {
			return err
		}
	} else if hasProperty(object, "budget") {
		return invalidf("budget is only valid with a managed execution profile.")
	}
	if profileObject, ok := asObject(profile); ok && profileObject["provider"] == "opencode" {
		for _, field := range []string{"worker", "reviewer"} {
			if models[field] != "deepseek/deepseek-v4-flash" {
				return invalidf("invalid OpenCode model %s.", field)
			}
		}
		for _, field := range []string{"worker_effort", "reviewer_effort"} {
			if models[field] != nil {
				return invalidf("OpenCode effort %s must be null.", field)
			}
		}
	} else {
		for _, field := range []string{"worker", "reviewer"} {
			model, isString := models[field].(string)
			if !isString || !modelPattern.MatchString(model) {
				return invalidf("invalid model %s.", field)
			}
		}
		for _, field := range []string{"worker_effort", "reviewer_effort"} {
			effort, _ := models[field].(string)
			if effort != "low" && effort != "medium" && effort != "high" && effort != "xhigh" {
				return invalidf("invalid effort %s", field)
			}
		}
	}
	for _, flag := range []string{"require_spec_review", "require_code_review"} {
		if value := getValue(object, flag, nil); value != nil {
			if _, ok := asBool(value); !ok {
				return invalidf("%s must be boolean.", flag)
			}
		}
	}
	sourcePaths, _ := asArray(getValue(object, "source_paths", []any{}))
	for _, raw := range sourcePaths {
		if err := assertRelativePath(asStringOr(raw)); err != nil {
			return err
		}
	}
	maxAttempts, maxOK := asInteger(getValue(object, "max_attempts", int64(16)))
	timeout, timeoutOK := asInteger(getValue(object, "timeout_seconds", int64(1800)))
	if !maxOK {
		return invalidf("max_attempts must be an integer.")
	}
	if !timeoutOK {
		return invalidf("timeout_seconds must be an integer.")
	}
	if maxAttempts < 1 || maxAttempts > 64 || timeout < 1 || timeout > 14400 {
		return invalidf("execution limits out of range.")
	}
	repairs, repairsOK := asInteger(getValue(object, "max_source_repairs", int64(0)))
	if !repairsOK || repairs < 0 || repairs > 3 {
		return invalidf("max_source_repairs must be an integer from 0 to 3.")
	}
	if repairs > 0 {
		for _, raw := range criteria {
			criterion, _ := asObject(raw)
			kind := asStringOr(criterion["kind"])
			if kind == "static" || kind == "unit" {
				retrySafe, _ := asBool(getValue(criterion, "retry_safe", false))
				if retrySafe && !hasProperty(criterion, "protected_paths") {
					return invalidf("repairable command checks require protected_paths covering their test code and fixtures.")
				}
			}
		}
	}
	return nil
}

// assertState mirrors Assert-BFState with exact legacy diagnostics.
func assertState(state any) error {
	object, err := assertFields(state,
		[]string{"schema_version", "task_id", "revision", "previous_sha256", "project_path", "worker_path", "baseline", "request", "request_hash", "intent_revision", "authorization_revision", "intent_hash", "policy_hash", "policy_files", "policy_rules", "classification", "status", "stage", "active_attempt", "unresolved_effect", "attempts", "evidence", "events", "question", "blockers", "acceptances", "created_at", "updated_at", "correction_rounds"},
		[]string{"repair"}, "state")
	if err != nil {
		return err
	}
	if version, ok := asInteger(object["schema_version"]); !ok || version != 1 {
		return blockedf("unsupported state schema.")
	}
	if err := assertUUID(object["task_id"]); err != nil {
		return err
	}
	if err := assertRequest(object["request"]); err != nil {
		return err
	}
	request, _ := asObject(object["request"])
	if request["request_id"] != object["task_id"] {
		return blockedf("request/task identity mismatch.")
	}
	switch asStringOr(object["status"]) {
	case "ready", "running", "needs_input", "blocked", "failed", "completed", "cancelled":
	default:
		return blockedf("invalid state status.")
	}
	switch asStringOr(object["stage"]) {
	case "inspect", "spec", "spec_review", "implement", "code_review", "verify", "diagnose", "acceptance":
	default:
		return blockedf("invalid state stage.")
	}
	if repair := getValue(object, "repair", nil); repair != nil {
		repairObject, err := assertFields(repair, []string{"rounds", "pending_failure", "last_source_sha256", "diagnosis_attempt"}, nil, "repair")
		if err != nil {
			return blockedf("%v", unwrapMessage(err))
		}
		rounds, ok := asInteger(repairObject["rounds"])
		if !ok || rounds < 0 || rounds > 3 {
			return blockedf("invalid source repair count.")
		}
		for _, name := range []string{"pending_failure", "diagnosis_attempt"} {
			if value := repairObject[name]; value != nil {
				if err := assertUUID(value); err != nil {
					return err
				}
			}
		}
	}
	for _, field := range []string{"attempts", "evidence", "events", "blockers", "acceptances", "policy_files"} {
		if _, ok := asArray(object[field]); !ok {
			return blockedf("%s must be an array.", field)
		}
	}
	return nil
}

func unwrapMessage(err error) string {
	if typed, ok := err.(*Error); ok {
		return typed.Message
	}
	return err.Error()
}
