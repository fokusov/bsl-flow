package worker

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"bsl-flow/cli/internal/repository"
)

// Port of the sealed critic model contract family of
// global/skills/1c-task/adapters/ProfiledCodex.ps1:13-159. The critic route is
// deliberately allowlisted: a profile or a mutable cache may select the model
// only after the current host has proved its identity; arbitrary model
// metadata must never widen this route.

// CriticContract is the sealed per-model contract of
// Get-BFCodexCriticModelContract (ProfiledCodex.ps1:13-33). An empty
// MultiAgentReasoningEffort is the PowerShell null of the Luna contract.
type CriticContract struct {
	Slug                       string
	ExperimentalSupportedTools []string
	MultiAgentVersion          string
	MultiAgentReasoningEffort  string
}

// CriticModelContract mirrors Get-BFCodexCriticModelContract. An unlisted
// model is a BF_BLOCKED refusal, never a weaker fallback contract.
func CriticModelContract(expectedModel string) (CriticContract, error) {
	switch expectedModel {
	case "gpt-5.6-luna":
		return CriticContract{
			Slug:                       "gpt-5.6-luna",
			ExperimentalSupportedTools: []string{},
			MultiAgentVersion:          "v1",
			MultiAgentReasoningEffort:  "",
		}, nil
	case "gpt-6-astra":
		return CriticContract{
			Slug:                       "gpt-6-astra",
			ExperimentalSupportedTools: []string{"send_user_message_async", "clock"},
			MultiAgentVersion:          "v2",
			MultiAgentReasoningEffort:  "xhigh",
		}, nil
	default:
		return CriticContract{}, blocked("sealed Codex critic model is not allowlisted: %s", expectedModel)
	}
}

// CriticCapabilityVersion mirrors Get-BFCodexCriticCapabilityVersion
// (ProfiledCodex.ps1:35-42): the sealed binary/version contract shared by the
// allowlisted models, with the model identity part of the capability key so a
// persisted receipt can never be replayed across models.
func CriticCapabilityVersion(expectedModel string) (string, error) {
	if _, err := CriticModelContract(expectedModel); err != nil {
		return "", err
	}
	return "codex-0.154.0-" + expectedModel + "-direct-empty-tools-v1", nil
}

// CriticCatalog is the sealed critic route data: the auditable raw source
// binding (source_cache_path/source_cache_sha256/model) and the inert-tool
// catalog ({models:[copy]}) consumed by the sealed process. Both members are
// the exact canonical shapes the PowerShell adapter persists and hashes.
type CriticCatalog struct {
	Source  map[string]any
	Catalog map[string]any
}

var criticSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// AssertCriticModelSource mirrors Assert-BFCodexCriticModelSource
// (ProfiledCodex.ps1:44-96): the model metadata must match the verified
// sealed-critic signature exactly, including the multi-agent and reasoning
// declarations Astra carries and the observed reasoning level.
func AssertCriticModelSource(model any, expectedModel, expectedEffort string) (CriticContract, error) {
	contract, err := CriticModelContract(expectedModel)
	if err != nil {
		return CriticContract{}, err
	}
	object, ok := asObject(model)
	if !ok {
		object = nil
	}
	requiredFields := []string{
		"slug", "use_responses_lite", "shell_type", "apply_patch_tool_type",
		"tool_mode", "experimental_supported_tools",
	}
	if expectedModel == "gpt-6-astra" {
		requiredFields = append(requiredFields,
			"multi_agent_version", "multi_agent_reasoning_effort", "supported_reasoning_levels")
	}
	for _, field := range requiredFields {
		if _, present := object[field]; !present {
			return CriticContract{}, blocked("%s model metadata is missing %s.", expectedModel, field)
		}
	}
	if object["slug"] != contract.Slug || !isTrueValue(object["use_responses_lite"]) ||
		stringOfValue(object["shell_type"]) != "unified_exec" ||
		stringOfValue(object["apply_patch_tool_type"]) != "freeform" ||
		stringOfValue(object["tool_mode"]) != "code_mode_only" {
		return CriticContract{}, blocked("%s model metadata differs from the verified sealed-critic signature.", expectedModel)
	}
	experimental := make([]string, 0, 4)
	experimentalValue, _ := asArray(getValue(object, "experimental_supported_tools", nil))
	if experimentalValue == nil {
		if raw, present := object["experimental_supported_tools"]; present && raw != nil {
			experimentalValue = []any{raw}
		}
	}
	for _, tool := range experimentalValue {
		name, isString := asString(tool)
		if !isString || strings.TrimSpace(name) == "" {
			return CriticContract{}, blocked("%s experimental tool metadata is invalid.", expectedModel)
		}
		experimental = append(experimental, name)
	}
	experimentalSorted := append([]string(nil), experimental...)
	contractTools := append([]string(nil), contract.ExperimentalSupportedTools...)
	sort.Strings(experimentalSorted)
	sort.Strings(contractTools)
	if strings.Join(experimentalSorted, "\x00") != strings.Join(contractTools, "\x00") {
		return CriticContract{}, blocked("%s experimental tool metadata differs from the verified sealed-critic signature.", expectedModel)
	}
	multiAgentVersion := getValue(object, "multi_agent_version", nil)
	if expectedModel == "gpt-6-astra" && multiAgentVersion == nil {
		return CriticContract{}, blocked("gpt-6-astra model metadata is missing multi_agent_version.")
	}
	if multiAgentVersion != nil && stringOfValue(multiAgentVersion) != contract.MultiAgentVersion {
		return CriticContract{}, blocked("%s multi-agent metadata differs from the verified sealed-critic signature.", expectedModel)
	}
	multiAgentEffort := getValue(object, "multi_agent_reasoning_effort", nil)
	if expectedModel == "gpt-6-astra" && multiAgentEffort == nil {
		return CriticContract{}, blocked("gpt-6-astra model metadata is missing multi_agent_reasoning_effort.")
	}
	if multiAgentEffort != nil && stringOfValue(multiAgentEffort) != contract.MultiAgentReasoningEffort {
		return CriticContract{}, blocked("%s multi-agent reasoning metadata differs from the verified sealed-critic signature.", expectedModel)
	}
	if strings.TrimSpace(expectedEffort) != "" {
		levels := getValue(object, "supported_reasoning_levels", nil)
		if levels == nil && expectedModel == "gpt-6-astra" {
			return CriticContract{}, blocked("gpt-6-astra catalog does not declare supported reasoning levels.")
		}
		if levels != nil {
			levelValues, isLevels := asArray(levels)
			if !isLevels {
				levelValues = []any{levels}
			}
			levelNames := make([]string, 0, len(levelValues))
			for _, level := range levelValues {
				levelObject, isObject := asObject(level)
				var name string
				if isObject {
					name = stringOfValue(getValue(levelObject, "effort", ""))
				}
				if strings.TrimSpace(name) == "" {
					return CriticContract{}, blocked("%s catalog contains an invalid reasoning level.", expectedModel)
				}
				levelNames = append(levelNames, name)
			}
			unique := make(map[string]struct{}, len(levelNames))
			for _, name := range levelNames {
				unique[name] = struct{}{}
			}
			if len(unique) != len(levelNames) {
				return CriticContract{}, blocked("%s catalog contains duplicate reasoning levels.", expectedModel)
			}
			matches := 0
			for _, name := range levelNames {
				if name == expectedEffort {
					matches++
				}
			}
			if matches != 1 {
				return CriticContract{}, blocked("%s catalog does not declare the observed reasoning effort.", expectedModel)
			}
		}
	}
	return contract, nil
}

// isTrueValue mirrors the PowerShell `-ne $true` comparison domain: only a
// real boolean true satisfies it.
func isTrueValue(value any) bool {
	flag, ok := value.(bool)
	return ok && flag
}

// stringOfValue mirrors PowerShell's [string] conversion of JSON-materialized
// values: strings stay, null becomes ”, booleans render as True/False and
// numbers keep their literal.
func stringOfValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	default:
		if literal, ok := value.(interface{ String() string }); ok {
			return literal.String()
		}
		return ""
	}
}

// CachedCriticModel mirrors Get-BFCodexCachedCriticModel
// (ProfiledCodex.ps1:98-111): the bounded native models_cache.json snapshot
// with a read-stability (TOCTOU) hash check, returning the sealed source
// binding {source_cache_path, source_cache_sha256, model}.
func CachedCriticModel(expectedModel, expectedEffort string) (map[string]any, error) {
	codexHome := os.Getenv("CODEX_HOME")
	if strings.TrimSpace(codexHome) == "" {
		home := os.Getenv("USERPROFILE")
		if strings.TrimSpace(home) == "" {
			userHome, err := os.UserHomeDir()
			if err != nil {
				return nil, blocked("bounded native Codex model catalog is unavailable.")
			}
			home = userHome
		}
		codexHome = home + string(os.PathSeparator) + ".codex"
	}
	path := filepath.Join(codexHome, "models_cache.json")
	resolved, err := workerSafePath(path)
	if err != nil {
		return nil, err
	}
	info, statErr := os.Lstat(resolved)
	if statErr != nil || !info.Mode().IsRegular() || info.Size() > 16777216 {
		return nil, blocked("bounded native Codex model catalog is unavailable.")
	}
	before, err := fileHash(resolved)
	if err != nil {
		return nil, err
	}
	cache, err := readJSONObjectFile(resolved)
	if err != nil {
		return nil, err
	}
	after, err := fileHash(resolved)
	if err != nil {
		return nil, err
	}
	if after != before {
		return nil, blocked("native model catalog changed while taking the critic snapshot.")
	}
	contract, err := CriticModelContract(expectedModel)
	if err != nil {
		return nil, err
	}
	models, _ := asArray(cache["models"])
	matches := make([]any, 0, 1)
	for _, candidate := range models {
		candidateObject, ok := asObject(candidate)
		if !ok {
			continue
		}
		if slug, _ := asString(candidateObject["slug"]); slug == contract.Slug {
			matches = append(matches, candidate)
		}
	}
	if len(matches) != 1 {
		return nil, blocked("native model catalog has no unique %s entry.", expectedModel)
	}
	if _, err := AssertCriticModelSource(matches[0], expectedModel, expectedEffort); err != nil {
		return nil, err
	}
	return map[string]any{
		"source_cache_path":   resolved,
		"source_cache_sha256": before,
		"model":               matches[0],
	}, nil
}

// CriticCatalogFromDirectory mirrors Get-BFCodexCriticCatalog
// (ProfiledCodex.ps1:113-121): resumes a preserved attempt directory through
// its critic-catalog-source.json, otherwise takes a fresh bounded cache
// snapshot.
func CriticCatalogFromDirectory(directory, expectedModel, expectedEffort string) (CriticCatalog, error) {
	sourcePath := filepath.Join(directory, "critic-catalog-source.json")
	var source map[string]any
	if _, statErr := os.Stat(directory); statErr == nil {
		if !fileLeafExists(sourcePath) {
			return CriticCatalog{}, blocked("partial Codex critic catalog; reconcile without retry.")
		}
		parsed, err := readJSONObjectFile(sourcePath)
		if err != nil {
			return CriticCatalog{}, err
		}
		source = parsed
	} else {
		fresh, err := CachedCriticModel(expectedModel, expectedEffort)
		if err != nil {
			return CriticCatalog{}, err
		}
		source = fresh
	}
	return ConvertCriticCatalog(source, expectedModel, expectedEffort)
}

// CriticCatalogFromSourcePath mirrors Get-BFCodexCriticCatalogFromSourcePath
// (ProfiledCodex.ps1:123-128): the exact source file must exist and parse.
func CriticCatalogFromSourcePath(sourcePath, expectedModel, expectedEffort string) (CriticCatalog, error) {
	resolved, err := workerSafePath(sourcePath)
	if err != nil {
		return CriticCatalog{}, err
	}
	if !fileLeafExists(resolved) {
		return CriticCatalog{}, blocked("exact Codex critic catalog source is missing.")
	}
	source, err := readJSONObjectFile(resolved)
	if err != nil {
		return CriticCatalog{}, err
	}
	return ConvertCriticCatalog(source, expectedModel, expectedEffort)
}

// ConvertCriticCatalog mirrors ConvertTo-BFCodexCriticCatalog
// (ProfiledCodex.ps1:130-151). The raw cache declaration is retained in
// Source for auditability while every model-owned tool switch is made inert
// in the catalog consumed by the sealed process; optional fields are
// normalized only when present so old Luna fixtures stay byte-compatible.
func ConvertCriticCatalog(source any, expectedModel, expectedEffort string) (CriticCatalog, error) {
	object, err := assertFields(source,
		[]string{"source_cache_path", "source_cache_sha256", "model"}, nil, "critic catalog source")
	if err != nil {
		return CriticCatalog{}, err
	}
	sourceCacheSHA256 := stringOfValue(object["source_cache_sha256"])
	if !criticSHA256Pattern.MatchString(sourceCacheSHA256) {
		return CriticCatalog{}, blocked("invalid critic source catalog identity.")
	}
	if _, err := AssertCriticModelSource(object["model"], expectedModel, expectedEffort); err != nil {
		return CriticCatalog{}, err
	}
	canonicalModel, err := repository.Canonical(object["model"])
	if err != nil {
		return CriticCatalog{}, invalid("%v", err)
	}
	copied, err := parseJSONObject(canonicalModel)
	if err != nil {
		return CriticCatalog{}, invalid("Cannot materialize JSON object: %v", err)
	}
	zeroToolValues := map[string]any{
		"shell_type":                        "disabled",
		"apply_patch_tool_type":             nil,
		"web_search_tool_type":              nil,
		"tool_mode":                         "direct",
		"experimental_supported_tools":      []any{},
		"multi_agent_version":               nil,
		"multi_agent_reasoning_effort":      nil,
		"supports_search_tool":              false,
		"node_repl_disabled":                true,
		"node_repl_auto_review_required":    false,
		"include_skills_usage_instructions": false,
		"include_plugin_usage_instructions": false,
		"include_apps_usage_instructions":   false,
	}
	for name, zero := range zeroToolValues {
		if _, present := copied[name]; present {
			copied[name] = zero
		}
	}
	return CriticCatalog{
		Source:  object,
		Catalog: map[string]any{"models": []any{copied}},
	}, nil
}

// CriticOverrides mirrors Get-BFCodexCriticOverrides
// (ProfiledCodex.ps1:153-159): the pinned catalog route and disabled feature
// switches of the sealed critic process.
func CriticOverrides(catalogPath string) []string {
	settings := []string{
		`model_provider="openai"`,
		`project_doc_max_bytes=0`,
		`agents.enabled=false`,
		`tools.update_plan.enabled=false`,
		`tools.experimental_request_user_input.enabled=false`,
		`model_catalog_json=` + jsonQuote(forwardSlash(catalogPath)),
	}
	for _, feature := range []string{
		"shell_tool", "view_image", "deferred_executor", "request_permissions_tool",
		"token_budget", "current_time_reminder", "sleep_tool", "tool_suggest",
		"image_generation", "goals", "remote_models", "remote_plugin",
		"enable_request_compression",
	} {
		settings = append(settings, "features."+feature+"=false")
	}
	arguments := make([]string, 0, len(settings)*2)
	for _, setting := range settings {
		arguments = append(arguments, "-c", setting)
	}
	return arguments
}
