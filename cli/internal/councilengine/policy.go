package councilengine

import (
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// policy.go ports Council.Common.ps1: the portable council configuration
// contract (Get-BSLFlowCouncilPolicy, Assert-BSLFlowCouncilNoUnknownKeys,
// Get-BSLFlowLocalProviderOverlay, Assert-BSLFlowEndpointUrl) plus the
// credential resolution of Council.Transport.ps1
// (Resolve-BSLFlowCouncilCredential) and the policy hash
// (Get-BSLFlowCouncilPolicyHash). No network, no secrets in committed config.

// CouncilVersions mirrors Get-BSLFlowCouncilVersions.
const (
	councilSchemaVersion        = 1
	transportCapabilityVersion  = 1
	promptVersionV2             = "council-prompt-v2"
	memberSchemaVersion         = 1
	reviewSchemaVersion         = 2
	knownPromptVersion          = "council-prompt-v2"
	requestTimeoutDefault       = 300
	reviewInputMaxBytesDefault  = 262144
	reviewInputMaxBytesMin      = 1024
	reviewInputMaxBytesMax      = 1048576
	councilRoleBrainstorm       = "brainstorm"
	councilRoleIntentCritic     = "intent_critic"
	councilRoleArchitecture     = "architecture_critic"
	councilRoleExecutability    = "executability_critic"
	councilRoleChair            = "chair"
	councilFallbackCurrentAgent = "current_agent"
	councilFallbackBlock        = "block"
)

// FallbackCurrentAgent and FallbackBlock are the exported role fallback
// policy constants (aliases of the unexported ones used internally).
const (
	FallbackCurrentAgent = councilFallbackCurrentAgent
	FallbackBlock        = councilFallbackBlock
)

// YamlValue is the exported form of the committed-policy YAML reader
// (Get-BSLFlowYamlValue) for host consumers.
func YamlValue(text string, path []string, defaultValue string) (string, error) {
	return yamlValue(text, path, defaultValue)
}

// ParseEndpointURL is the exported endpoint decomposition
// (Assert-BSLFlowEndpointUrl) for host consumers; allowLocalHTTP permits
// loopback plain-HTTP test endpoints.
func ParseEndpointURL(raw, name string) (Endpoint, error) {
	return assertEndpointURL(raw, name, true)
}

// councilRoleOrder is the canonical member role enumeration.
var councilRoleOrder = []string{
	councilRoleBrainstorm, councilRoleIntentCritic, councilRoleArchitecture, councilRoleExecutability, councilRoleChair,
}

// Endpoint is the normalized endpoint decomposition of
// Assert-BSLFlowEndpointUrl.
type Endpoint struct {
	Scheme   string
	Host     string
	Port     int
	BasePath string
}

// ProviderConfig is one llm.providers entry.
type ProviderConfig struct {
	Name                       string
	Protocol                   string // openai_responses | openai_compatible
	BaseURL                    string
	TokenEnv                   string
	Endpoint                   Endpoint
	TransportCapabilityVersion int
}

// ModelConfig is one llm.models entry.
type ModelConfig struct {
	Name            string
	Provider        string
	Model           string
	Effort          string
	CostEstimateUSD *float64
}

// RoleConfig is one review.council.roles entry.
type RoleConfig struct {
	Name     string
	Enabled  bool
	Required bool
	Model    string
	Fallback string
}

// BudgetConfig mirrors review.council.budget.
type BudgetConfig struct {
	Currency    string
	Limit       *float64
	Reservation *float64
}

// CouncilPolicy is the parsed policy of Get-BSLFlowCouncilPolicy.
type CouncilPolicy struct {
	CouncilSchemaVersion       int
	TransportCapabilityVersion int
	Enabled                    bool
	MaxParallel                int
	AllowLocalHTTP             bool
	LegacyMode                 string
	RequestTimeoutSeconds      int
	Providers                  map[string]ProviderConfig
	ProviderOrder              []string
	Models                     map[string]ModelConfig
	ModelOrder                 []string
	Roles                      map[string]RoleConfig
	Budget                     *BudgetConfig
}

// parsePSBool mirrors ConvertTo-BSLFlowBoolean.
func parsePSBool(value, name string) (bool, error) {
	switch strings.ToLower(value) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, invalid("Expected true or false for %s, got: %s", name, value)
	}
}

var yamlLinePattern = regexp.MustCompile(`^(?P<indent>\s*)(?P<key>[A-Za-z0-9_-]+):(?:\s*(?P<value>.*?))?\s*$`)

type yamlLine struct {
	indent int
	key    string
	value  string
}

// parseYAMLLines materializes the meaningful config lines; a tab in the
// indentation is the exact PowerShell contract error.
func parseYAMLLines(text string) ([]yamlLine, error) {
	lines := []yamlLine{}
	for _, raw := range regexp.MustCompile(`\r?\n`).Split(text, -1) {
		if regexp.MustCompile(`^\s*(?:#.*)?$`).MatchString(raw) {
			continue
		}
		match := yamlLinePattern.FindStringSubmatch(raw)
		if match == nil {
			continue
		}
		if strings.Contains(match[1], "\t") {
			return nil, invalid("Tabs are not supported in bsl-flow.yaml indentation.")
		}
		lines = append(lines, yamlLine{indent: len(match[1]), key: match[2], value: strings.TrimSpace(match[3])})
	}
	return lines, nil
}

// yamlValue mirrors Get-BSLFlowYamlValue: one bounded indentation-stack
// reader for committed policy values.
func yamlValue(text string, path []string, defaultValue string) (string, error) {
	type frame struct {
		indent int
		key    string
	}
	lines := []yamlLine{}
	for _, raw := range regexp.MustCompile(`\r?\n`).Split(text, -1) {
		if regexp.MustCompile(`^\s*(?:#.*)?$`).MatchString(raw) {
			continue
		}
		match := yamlLinePattern.FindStringSubmatch(raw)
		if match == nil {
			continue
		}
		if strings.Contains(match[1], "\t") {
			return "", invalid("Tabs are not supported in bsl-flow.yaml indentation.")
		}
		lines = append(lines, yamlLine{indent: len(match[1]), key: match[2], value: strings.TrimSpace(match[3])})
	}
	stack := []frame{}
	results := []string{}
	for _, line := range lines {
		for len(stack) > 0 && stack[len(stack)-1].indent >= line.indent {
			stack = stack[:len(stack)-1]
		}
		keys := make([]string, 0, len(stack)+1)
		for _, entry := range stack {
			keys = append(keys, entry.key)
		}
		keys = append(keys, line.key)
		if line.value != "" && strings.Join(keys, "/") == strings.Join(path, "/") {
			results = append(results, strings.Trim(line.value, `"'`))
		}
		if line.value == "" {
			stack = append(stack, frame{indent: line.indent, key: line.key})
		}
	}
	if len(results) > 1 {
		return "", invalid("Duplicate YAML value: %s", strings.Join(path, "."))
	}
	if len(results) == 1 {
		return results[0], nil
	}
	return defaultValue, nil
}

// yamlChildren mirrors Get-BSLFlowYamlChildren: mapping child keys of path
// (value-less keys whose parent stack equals path).
func yamlChildren(text string, path []string) []string {
	type frame struct {
		indent int
		key    string
	}
	lines := []yamlLine{}
	for _, raw := range regexp.MustCompile(`\r?\n`).Split(text, -1) {
		if regexp.MustCompile(`^\s*(?:#.*)?$`).MatchString(raw) {
			continue
		}
		match := yamlLinePattern.FindStringSubmatch(raw)
		if match == nil {
			continue
		}
		if strings.Contains(match[1], "\t") {
			continue
		}
		lines = append(lines, yamlLine{indent: len(match[1]), key: match[2], value: strings.TrimSpace(match[3])})
	}
	children := []string{}
	stack := []frame{}
	for _, line := range lines {
		for len(stack) > 0 && stack[len(stack)-1].indent >= line.indent {
			stack = stack[:len(stack)-1]
		}
		keys := make([]string, 0, len(stack)+1)
		for _, entry := range stack {
			keys = append(keys, entry.key)
		}
		keys = append(keys, line.key)
		if line.value == "" && len(keys) == len(path)+1 && strings.Join(keys[:len(keys)-1], "/") == strings.Join(path, "/") {
			if !containsString(children, line.key) {
				children = append(children, line.key)
			}
		}
		if line.value == "" {
			stack = append(stack, frame{indent: line.indent, key: line.key})
		}
	}
	return children
}

// yamlDirectKeys mirrors Get-BSLFlowYamlDirectKeys: direct child keys of a
// mapping node, scalar or map valued.
func yamlDirectKeys(text string, path []string) []string {
	type frame struct {
		indent int
		key    string
	}
	lines := []yamlLine{}
	for _, raw := range regexp.MustCompile(`\r?\n`).Split(text, -1) {
		if regexp.MustCompile(`^\s*(?:#.*)?$`).MatchString(raw) {
			continue
		}
		match := yamlLinePattern.FindStringSubmatch(raw)
		if match == nil {
			continue
		}
		if strings.Contains(match[1], "\t") {
			continue
		}
		lines = append(lines, yamlLine{indent: len(match[1]), key: match[2], value: strings.TrimSpace(match[3])})
	}
	keys := []string{}
	stack := []frame{}
	for _, line := range lines {
		for len(stack) > 0 && stack[len(stack)-1].indent >= line.indent {
			stack = stack[:len(stack)-1]
		}
		parent := make([]string, 0, len(stack))
		for _, entry := range stack {
			parent = append(parent, entry.key)
		}
		same := len(parent) == len(path)
		if same {
			for index := range path {
				if parent[index] != path[index] {
					same = false
					break
				}
			}
		}
		if same && !containsString(keys, line.key) {
			keys = append(keys, line.key)
		}
		if line.value == "" {
			stack = append(stack, frame{indent: line.indent, key: line.key})
		}
	}
	return keys
}

func containsString(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// yamlOptionalNumber mirrors Get-BSLFlowCouncilOptionalNumber.
func yamlOptionalNumber(text string, path []string, name string) (*float64, error) {
	raw, err := yamlValue(text, path, "")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parsed, ok := parseInvariantFloat(raw)
	if !ok || !finiteNonNegative(parsed) {
		return nil, invalid("Invalid %s: must be a non-negative finite number.", name)
	}
	return &parsed, nil
}

func parseInvariantFloat(text string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func parseInvariantInt(text string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0, err
	}
	return value, nil
}

func parseIntStrict(text string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(text))
}

// parsedURL is the decomposed [System.Uri] surface the endpoint check needs.
type parsedURL struct {
	Scheme   string
	Host     string
	Port     int
	Path     string
	Userinfo string
	Fragment string
}

func parseURL(raw string) (parsedURL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return parsedURL{}, err
	}
	if !parsed.IsAbs() {
		return parsedURL{}, invalid("relative URL")
	}
	port := 0
	if parsed.Port() != "" {
		port, err = strconv.Atoi(parsed.Port())
		if err != nil {
			return parsedURL{}, err
		}
	}
	userinfo := ""
	if parsed.User != nil {
		userinfo = parsed.User.String()
	}
	return parsedURL{
		Scheme:   parsed.Scheme,
		Host:     parsed.Hostname(),
		Port:     port,
		Path:     parsed.Path,
		Userinfo: userinfo,
		Fragment: parsed.Fragment,
	}, nil
}

// knownProviderEndpoint mirrors Get-BSLFlowKnownProviderEndpoint.
func knownProviderEndpoint(name string) string {
	switch strings.ToLower(name) {
	case "openai":
		return "https://api.openai.com/v1"
	case "deepseek":
		return "https://api.deepseek.com"
	default:
		return ""
	}
}

// assertEndpointURL mirrors Assert-BSLFlowEndpointUrl with the exact
// diagnostics.
func assertEndpointURL(url, name string, allowLocalHTTP bool) (Endpoint, error) {
	parsed, err := parseURL(url)
	if err != nil {
		return Endpoint{}, invalid("Invalid endpoint URL for %s: %s", name, url)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return Endpoint{}, invalid("Endpoint must use https for %s: %s", name, url)
	}
	if parsed.Userinfo != "" {
		return Endpoint{}, invalid("Endpoint must not contain userinfo for %s.", name)
	}
	if parsed.Fragment != "" {
		return Endpoint{}, invalid("Endpoint must not contain fragment for %s.", name)
	}
	if strings.Contains(url, "?") {
		return Endpoint{}, invalid("Endpoint must not contain query for %s.", name)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return Endpoint{}, invalid("Endpoint must have a host for %s.", name)
	}
	loopback := parsed.Host == "localhost" || parsed.Host == "127.0.0.1" || parsed.Host == "::1"
	if parsed.Scheme == "http" && !(loopback && allowLocalHTTP) {
		return Endpoint{}, invalid("Plain HTTP endpoint is allowed only for loopback with an explicit local-development flag: %s.", name)
	}
	port := parsed.Port
	if port == 0 {
		if parsed.Scheme == "https" {
			port = 443
		} else {
			port = 80
		}
	}
	return Endpoint{Scheme: parsed.Scheme, Host: strings.ToLower(parsed.Host), Port: port, BasePath: parsed.Path}, nil
}

var safeProviderIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)
var safeModelIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,128}$`)
var tokenEnvPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var authorizationLinePattern = regexp.MustCompile(`(?im)^\s*Authorization\s*:`)

// ParseCouncilPolicy ports Get-BSLFlowCouncilPolicy.
func ParseCouncilPolicy(configText string) (*CouncilPolicy, error) {
	enabled, err := yamlBool(configText, []string{"review", "council", "enabled"}, "false", "review.council.enabled")
	if err != nil {
		return nil, err
	}
	maxParallelRaw, err := yamlValue(configText, []string{"review", "council", "max_parallel"}, "2")
	if err != nil {
		return nil, err
	}
	maxParallel, err := parseInvariantInt(maxParallelRaw)
	if err != nil {
		return nil, invalid("Invalid review.council.max_parallel.")
	}
	if maxParallel < 1 || maxParallel > 8 {
		return nil, invalid("review.council.max_parallel must be between 1 and 8.")
	}
	allowLocalHTTP, err := yamlBool(configText, []string{"review", "council", "allow_local_http"}, "false", "review.council.allow_local_http")
	if err != nil {
		return nil, err
	}
	legacyMode, err := yamlValue(configText, []string{"review", "council", "legacy_mode"}, "block")
	if err != nil {
		return nil, err
	}
	if legacyMode != "block" && legacyMode != "opencode_compat" {
		return nil, invalid("Invalid review.council.legacy_mode: %s", legacyMode)
	}
	if err := assertCouncilNoUnknownKeys(configText); err != nil {
		return nil, err
	}
	// Committed config must never carry a literal secret.
	for _, providerName := range yamlChildren(configText, []string{"llm", "providers"}) {
		literal, err := yamlValue(configText, []string{"llm", "providers", providerName, "token"}, "")
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(literal) != "" {
			return nil, invalid("Literal token is forbidden in committed config: llm.providers.%s.token", providerName)
		}
	}
	if authorizationLinePattern.MatchString(configText) {
		return nil, invalid("Authorization header must not appear in committed config.")
	}

	policy := &CouncilPolicy{
		CouncilSchemaVersion:       councilSchemaVersion,
		TransportCapabilityVersion: transportCapabilityVersion,
		Enabled:                    enabled,
		MaxParallel:                maxParallel,
		AllowLocalHTTP:             allowLocalHTTP,
		LegacyMode:                 legacyMode,
		Providers:                  map[string]ProviderConfig{},
		Models:                     map[string]ModelConfig{},
		Roles:                      map[string]RoleConfig{},
	}
	for _, providerName := range yamlChildren(configText, []string{"llm", "providers"}) {
		if !safeProviderIDPattern.MatchString(providerName) {
			return nil, invalid("Unsafe provider id: %s", providerName)
		}
		protocol, err := yamlValue(configText, []string{"llm", "providers", providerName, "protocol"}, "")
		if err != nil {
			return nil, err
		}
		if protocol != "openai_responses" && protocol != "openai_compatible" {
			return nil, invalid("Invalid protocol for llm.providers.%s.", providerName)
		}
		baseURL, err := yamlValue(configText, []string{"llm", "providers", providerName, "base_url"}, "")
		if err != nil {
			return nil, err
		}
		tokenEnv, err := yamlValue(configText, []string{"llm", "providers", providerName, "token_env"}, "")
		if err != nil {
			return nil, err
		}
		if tokenEnv != "" && !tokenEnvPattern.MatchString(tokenEnv) {
			return nil, invalid("Invalid token_env for llm.providers.%s.", providerName)
		}
		known := knownProviderEndpoint(providerName)
		if strings.TrimSpace(baseURL) == "" {
			if known == "" {
				return nil, invalid("Custom provider requires base_url: %s.", providerName)
			}
			baseURL = known
		}
		endpoint, err := assertEndpointURL(baseURL, "llm.providers."+providerName, allowLocalHTTP)
		if err != nil {
			return nil, err
		}
		policy.Providers[providerName] = ProviderConfig{
			Name: providerName, Protocol: protocol, BaseURL: baseURL, TokenEnv: tokenEnv,
			Endpoint: endpoint, TransportCapabilityVersion: transportCapabilityVersion,
		}
		policy.ProviderOrder = append(policy.ProviderOrder, providerName)
	}
	for _, modelName := range yamlChildren(configText, []string{"llm", "models"}) {
		if !safeProviderIDPattern.MatchString(modelName) {
			return nil, invalid("Unsafe model profile id: %s", modelName)
		}
		provider, err := yamlValue(configText, []string{"llm", "models", modelName, "provider"}, "")
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(provider) == "" {
			return nil, invalid("Model profile references unknown provider: %s.", modelName)
		}
		if _, known := policy.Providers[provider]; !known {
			return nil, invalid("Model profile references unknown provider: %s.", modelName)
		}
		modelID, err := yamlValue(configText, []string{"llm", "models", modelName, "model"}, "")
		if err != nil {
			return nil, err
		}
		if !safeModelIDPattern.MatchString(modelID) {
			return nil, invalid("Unsafe model id for llm.models.%s.", modelName)
		}
		effort, err := yamlValue(configText, []string{"llm", "models", modelName, "effort"}, "medium")
		if err != nil {
			return nil, err
		}
		if !validEffort(effort) {
			return nil, invalid("Invalid effort for llm.models.%s. Use low/medium/high/xhigh or a positive integer.", modelName)
		}
		estimate, err := yamlOptionalNumber(configText, []string{"llm", "models", modelName, "cost_estimate_usd"}, "llm.models."+modelName+".cost_estimate_usd")
		if err != nil {
			return nil, err
		}
		policy.Models[modelName] = ModelConfig{
			Name: modelName, Provider: provider, Model: modelID, Effort: effort, CostEstimateUSD: estimate,
		}
		policy.ModelOrder = append(policy.ModelOrder, modelName)
	}
	if keys := yamlDirectKeys(configText, []string{"review", "council", "budget"}); len(keys) > 0 {
		currency, err := yamlValue(configText, []string{"review", "council", "budget", "currency"}, "USD")
		if err != nil {
			return nil, err
		}
		if currency != "USD" {
			return nil, invalid("review.council.budget.currency must be USD.")
		}
		limit, err := yamlOptionalNumber(configText, []string{"review", "council", "budget", "limit"}, "review.council.budget.limit")
		if err != nil {
			return nil, err
		}
		reservation, err := yamlOptionalNumber(configText, []string{"review", "council", "budget", "reservation"}, "review.council.budget.reservation")
		if err != nil {
			return nil, err
		}
		if limit == nil && reservation != nil && *reservation != 0 {
			return nil, invalid("review.council.budget.reservation must be zero when no monetary limit is enforced.")
		}
		policy.Budget = &BudgetConfig{Currency: "USD", Limit: limit, Reservation: reservation}
	}
	for _, role := range councilRoleOrder {
		defaultEnabled := "false"
		defaultRequired := "false"
		if role != councilRoleBrainstorm {
			defaultEnabled = "true"
			defaultRequired = "true"
		}
		if !enabled {
			defaultEnabled = "false"
			defaultRequired = "false"
		}
		roleEnabled, err := yamlBool(configText, []string{"review", "council", "roles", role, "enabled"}, defaultEnabled, "review.council.roles."+role+".enabled")
		if err != nil {
			return nil, err
		}
		roleRequired, err := yamlBool(configText, []string{"review", "council", "roles", role, "required"}, defaultRequired, "review.council.roles."+role+".required")
		if err != nil {
			return nil, err
		}
		if !enabled {
			roleEnabled = false
			roleRequired = false
		}
		model, err := yamlValue(configText, []string{"review", "council", "roles", role, "model"}, "")
		if err != nil {
			return nil, err
		}
		fallback, err := yamlValue(configText, []string{"review", "council", "roles", role, "fallback"}, "")
		if err != nil {
			return nil, err
		}
		if fallback == "" {
			fallback = councilFallbackCurrentAgent
		}
		if fallback != councilFallbackCurrentAgent && fallback != councilFallbackBlock {
			return nil, invalid("Invalid fallback for role %s.", role)
		}
		if roleRequired && !roleEnabled {
			return nil, invalid("Role %s cannot be required while disabled.", role)
		}
		if roleEnabled {
			if strings.TrimSpace(model) == "" {
				return nil, invalid("Enabled role %s must reference llm.models.<profile>.", role)
			}
			if _, known := policy.Models[model]; !known {
				return nil, invalid("Role %s references unknown model profile: %s.", role, model)
			}
		}
		policy.Roles[role] = RoleConfig{Name: role, Enabled: roleEnabled, Required: roleRequired, Model: model, Fallback: fallback}
	}
	if enabled {
		chair := policy.Roles[councilRoleChair]
		if !chair.Enabled || !chair.Required {
			return nil, invalid("Council chair must be enabled and required when council.enabled is true.")
		}
		if explicitLegacyReviewer(configText) && legacyMode != "opencode_compat" {
			return nil, blocked("explicit review.reviewer.provider opencode cannot be silently reinterpreted. Set review.council.legacy_mode to opencode_compat for the separate compatibility route, or remove the legacy reviewer block to use the API council.")
		}
	}
	timeout, err := yamlOptionalNumber(configText, []string{"review", "council", "request_timeout_seconds"}, "review.council.request_timeout_seconds")
	if err != nil {
		return nil, err
	}
	if timeout == nil {
		value := float64(requestTimeoutDefault)
		timeout = &value
	}
	if *timeout < 60 || *timeout > 900 {
		return nil, invalid("review.council.request_timeout_seconds must be between 60 and 900.")
	}
	policy.RequestTimeoutSeconds = int(*timeout)
	return policy, nil
}

// yamlBool is the defaulted boolean probe of the policy reader: contract
// errors (tab indentation, duplicates) fail closed, the default never masks
// a malformed committed config.
func yamlBool(text string, path []string, defaultValue string, name string) (bool, error) {
	value, err := yamlValue(text, path, defaultValue)
	if err != nil {
		return false, err
	}
	return parsePSBool(value, name)
}

func validEffort(effort string) bool {
	switch effort {
	case "low", "medium", "high", "xhigh":
		return true
	}
	value, err := parseIntStrict(effort)
	return err == nil && value >= 1 && value <= 10000
}

// assertCouncilNoUnknownKeys ports Assert-BSLFlowCouncilNoUnknownKeys.
func assertCouncilNoUnknownKeys(text string) error {
	allowed := map[string][]string{
		"llm":            {"providers", "models"},
		"review.council": {"enabled", "max_parallel", "allow_local_http", "legacy_mode", "roles", "budget", "request_timeout_seconds"},
	}
	for _, path := range [][]string{{"llm"}, {"review", "council"}} {
		name := strings.Join(path, ".")
		present, err := yamlValue(text, append(path, "__missing__"), "__absent__")
		if err != nil {
			return err
		}
		if present == "__absent__" && len(yamlDirectKeys(text, path)) == 0 {
			continue
		}
		for _, key := range yamlDirectKeys(text, path) {
			if !containsString(allowed[name], key) {
				return invalid("Unknown council configuration field: %s.%s.", name, key)
			}
		}
	}
	for _, providerName := range yamlChildren(text, []string{"llm", "providers"}) {
		for _, key := range yamlDirectKeys(text, []string{"llm", "providers", providerName}) {
			if !containsString([]string{"protocol", "base_url", "token_env"}, key) {
				return invalid("Unknown provider field: llm.providers.%s.%s.", providerName, key)
			}
		}
	}
	for _, modelName := range yamlChildren(text, []string{"llm", "models"}) {
		for _, key := range yamlDirectKeys(text, []string{"llm", "models", modelName}) {
			if !containsString([]string{"provider", "model", "effort", "cost_estimate_usd"}, key) {
				return invalid("Unknown model field: llm.models.%s.%s.", modelName, key)
			}
		}
	}
	for _, role := range councilRoleOrder {
		for _, key := range yamlDirectKeys(text, []string{"review", "council", "roles", role}) {
			if !containsString([]string{"enabled", "required", "model", "fallback"}, key) {
				return invalid("Unknown role field: review.council.roles.%s.%s.", role, key)
			}
		}
	}
	for _, key := range yamlDirectKeys(text, []string{"review", "council", "budget"}) {
		if !containsString([]string{"currency", "limit", "reservation"}, key) {
			return invalid("Unknown budget field: review.council.budget.%s.", key)
		}
	}
	return nil
}

var explicitReviewerPattern = regexp.MustCompile(`^["']?opencode["']?\s*$`)

// explicitLegacyReviewer ports Test-BSLFlowExplicitYamlValue for the
// review.reviewer.provider opencode probe.
func explicitLegacyReviewer(text string) bool {
	for _, raw := range regexp.MustCompile(`\r?\n`).Split(text, -1) {
		if regexp.MustCompile(`^\s*(?:#.*)?$`).MatchString(raw) {
			continue
		}
		match := regexp.MustCompile(`^(?P<indent>\s*)(?P<key>[A-Za-z0-9_-]+):(?:\s*(?P<value>.*?))?\s*$`).FindStringSubmatch(raw)
		if match == nil {
			continue
		}
		if match[2] == "provider" && explicitReviewerPattern.MatchString(strings.TrimSpace(match[3])) {
			return true
		}
	}
	return false
}

// LocalOverlayEntry is one providers.local.yaml overlay record.
type LocalOverlayEntry struct {
	HasToken   bool
	HasBaseURL bool
	BaseURL    string
}

// LocalProviderOverlay mirrors Get-BSLFlowLocalProviderOverlay. The file is
// read as-is (never logged); token_env inside it is refused.
func LocalProviderOverlay(projectPath string) (map[string]LocalOverlayEntry, error) {
	localPath := projectPath + string(os.PathSeparator) + joinPath(".bsl-flow", "providers.local.yaml")
	data, err := os.ReadFile(localPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]LocalOverlayEntry{}, nil
		}
		return nil, blocked("%v", err)
	}
	text := string(data)
	if regexp.MustCompile(`(?im)^\s*token_env\s*:`).MatchString(text) {
		return nil, invalid("token_env belongs in committed config, not in providers.local.yaml.")
	}
	overlay := map[string]LocalOverlayEntry{}
	for _, providerName := range yamlChildren(text, []string{"providers"}) {
		token, err := yamlValue(text, []string{"providers", providerName, "token"}, "")
		if err != nil {
			return nil, err
		}
		baseURL, err := yamlValue(text, []string{"providers", providerName, "base_url"}, "")
		if err != nil {
			return nil, err
		}
		overlay[providerName] = LocalOverlayEntry{
			HasToken:   strings.TrimSpace(token) != "",
			HasBaseURL: strings.TrimSpace(baseURL) != "",
			BaseURL:    baseURL,
		}
	}
	return overlay, nil
}

// Credential is the resolved provider credential.
type Credential struct {
	Token            string
	CredentialSource string // local | env | missing
}

// ResolveCredential ports Resolve-BSLFlowCouncilCredential: local token wins
// over the token environment variable; no source is "missing".
func ResolveCredential(providerName, tokenEnv, localToken string) Credential {
	if strings.TrimSpace(localToken) != "" {
		return Credential{Token: localToken, CredentialSource: "local"}
	}
	if strings.TrimSpace(tokenEnv) != "" {
		if value, present := os.LookupEnv(tokenEnv); present && strings.TrimSpace(value) != "" {
			return Credential{Token: value, CredentialSource: "env"}
		}
	}
	return Credential{CredentialSource: "missing"}
}

// PolicyHash ports Get-BSLFlowCouncilPolicyHash: the snapshot hashes the
// decoded (BOM-stripped) policy text. A UTF-8 BOM is encoding metadata, not a
// policy change.
func PolicyHash(policyPath string) (string, error) {
	data, err := os.ReadFile(policyPath)
	if err != nil {
		return "", invalid("Council policy file not found: %s", policyPath)
	}
	text, err := strictPSUTF8Decode(data)
	if err != nil {
		return "", invalid("Council policy file is not valid UTF-8: %s", policyPath)
	}
	text = strings.TrimPrefix(text, "\ufeff")
	return sha256Hex([]byte(text)), nil
}

func joinPath(elements ...string) string {
	parts := make([]string, 0, len(elements))
	for _, element := range elements {
		parts = append(parts, element)
	}
	return strings.Join(parts, string(os.PathSeparator))
}
