package councilengine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// policy_test.go ports the Council.Common.ps1 config contract checks against
// the real committed bsl-flow.yaml and the packaged project template, plus the
// focused enum/error cases of Test-CouncilRouting.ps1.

func readFileOrEmpty(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestParseCouncilPolicyFromCommittedConfig(t *testing.T) {
	config := readFileOrEmpty(t, filepath.Join("..", "..", "..", "bsl-flow.yaml"))
	if config == "" {
		t.Skip("committed bsl-flow.yaml unavailable from test cwd")
	}
	policy, err := ParseCouncilPolicy(config)
	if err != nil {
		t.Fatalf("parse committed config: %v", err)
	}
	if !policy.Enabled {
		t.Fatal("committed config must enable the council")
	}
	if policy.LegacyMode != "block" {
		t.Fatalf("legacy_mode: %s", policy.LegacyMode)
	}
	chair := policy.Roles[councilRoleChair]
	if !chair.Enabled || !chair.Required {
		t.Fatal("chair must be enabled and required")
	}
	for _, role := range []string{councilRoleIntentCritic, councilRoleArchitecture, councilRoleExecutability} {
		if !policy.Roles[role].Enabled || !policy.Roles[role].Required {
			t.Fatalf("role %s must be enabled and required", role)
		}
		if policy.Roles[role].Fallback != councilFallbackBlock {
			t.Fatalf("role %s fallback: %s", role, policy.Roles[role].Fallback)
		}
	}
	if policy.Roles[councilRoleBrainstorm].Enabled {
		t.Fatal("brainstorm must be disabled by default")
	}
	deepseek, ok := policy.Providers["deepseek"]
	if !ok {
		t.Fatal("deepseek provider missing")
	}
	if deepseek.Protocol != "openai_compatible" || deepseek.Endpoint.Host != "api.deepseek.com" || deepseek.Endpoint.Port != 443 {
		t.Fatalf("deepseek provider: %+v", deepseek)
	}
	reviewer := policy.Models["reviewer"]
	if reviewer.Provider != "deepseek" || reviewer.Model != "deepseek-v4-pro" {
		t.Fatalf("reviewer model: %+v", reviewer)
	}
}

func TestParseCouncilPolicyFromPackageTemplate(t *testing.T) {
	config := readFileOrEmpty(t, filepath.Join("..", "..", "..", "global", "skills", "1c-init-project", "assets", "project", "bsl-flow.yaml"))
	if config == "" {
		t.Skip("package template unavailable")
	}
	policy, err := ParseCouncilPolicy(config)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	if !policy.Enabled || policy.MaxParallel != 2 {
		t.Fatalf("template council: enabled=%v maxParallel=%d", policy.Enabled, policy.MaxParallel)
	}
	if policy.Roles[councilRoleBrainstorm].Enabled {
		t.Fatal("brainstorm must default off in the template")
	}
	// The template wires sol (openai_responses) to the chair.
	chair := policy.Roles[councilRoleChair]
	sol := policy.Models[chair.Model]
	if sol.Provider != "openai" {
		t.Fatalf("chair model provider: %s", sol.Provider)
	}
	if policy.Providers["openai"].Protocol != "openai_responses" {
		t.Fatal("openai must use openai_responses in the template")
	}
	if policy.Providers["deepseek"].Protocol != "openai_compatible" {
		t.Fatal("deepseek must use openai_compatible in the template")
	}
}

func TestParseCouncilPolicyOffAndLegacy(t *testing.T) {
	policy, err := ParseCouncilPolicy("")
	if err != nil {
		t.Fatalf("empty config: %v", err)
	}
	if policy.Enabled {
		t.Fatal("empty config must disable the council")
	}
	for _, role := range CouncilRoleOrder {
		if policy.Roles[role].Enabled || policy.Roles[role].Required {
			t.Fatalf("role %s must be disabled when council off", role)
		}
	}
	// Explicit legacy reviewer without opencode_compat is a migration blocker.
	// The role/model references must be valid first (PS validates roles before
	// the legacy probe), so the fixture carries a complete llm section.
	legacy := "llm:\n  providers:\n    p:\n      protocol: openai_compatible\n      base_url: https://api.example.com\n  models:\n    m:\n      provider: p\n      model: x\nreview:\n  council:\n    enabled: true\n    legacy_mode: block\n    roles:\n      intent_critic:\n        model: m\n      architecture_critic:\n        model: m\n      executability_critic:\n        model: m\n      chair:\n        model: m\n  reviewer:\n    provider: opencode\n"
	if _, err := ParseCouncilPolicy(legacy); err == nil {
		t.Fatal("explicit legacy reviewer must raise the migration blocker")
	} else if kind, ok := err.(*KindError); !ok || kind.Kind != KindBlocked {
		t.Fatalf("legacy blocker must be BF_BLOCKED, got %v", err)
	}
	compat := "llm:\n  providers:\n    p:\n      protocol: openai_compatible\n      base_url: https://api.example.com\n  models:\n    m:\n      provider: p\n      model: x\nreview:\n  council:\n    enabled: true\n    legacy_mode: opencode_compat\n    roles:\n      intent_critic:\n        model: m\n      architecture_critic:\n        model: m\n      executability_critic:\n        model: m\n      chair:\n        model: m\n  reviewer:\n    provider: opencode\n"
	parsed, err := ParseCouncilPolicy(compat)
	if err != nil {
		t.Fatalf("opencode_compat config must parse: %v", err)
	}
	if parsed.LegacyMode != "opencode_compat" {
		t.Fatalf("legacy_mode: %s", parsed.LegacyMode)
	}
}

func TestParseCouncilPolicyRejectsUnknownKeys(t *testing.T) {
	cases := map[string]string{
		"unknown council field":  "review:\n  council:\n    enabled: true\n    bogus: 1\n",
		"unknown provider field": "llm:\n  providers:\n    deepseek:\n      protocol: openai_compatible\n      base_url: https://api.deepseek.com\n      bogus: 1\n",
		"unknown role field":     "review:\n  council:\n    enabled: true\n    roles:\n      chair:\n        bogus: 1\n",
		"unknown budget field":   "review:\n  council:\n    budget:\n      bogus: 1\n",
		"literal token":          "llm:\n  providers:\n    deepseek:\n      protocol: openai_compatible\n      base_url: https://api.deepseek.com\n      token: secret\n",
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCouncilPolicy(config); err == nil {
				t.Fatalf("config must be rejected: %s", config)
			}
		})
	}
}

func TestParseCouncilPolicyRoleConstraints(t *testing.T) {
	// roleBlock supplies the complete roles section so no duplicate keys occur.
	base := func(roleBlock string) string {
		return "llm:\n  models:\n    m:\n      provider: p\n      model: x\n  providers:\n    p:\n      protocol: openai_compatible\n      base_url: https://api.example.com\nreview:\n  council:\n    enabled: true\n    roles:\n" + roleBlock
	}
	full := func(intent string) string {
		return "      intent_critic:\n" + intent +
			"      architecture_critic:\n        model: m\n" +
			"      executability_critic:\n        model: m\n" +
			"      chair:\n        model: m\n"
	}
	if _, err := ParseCouncilPolicy(base(full("        enabled: true\n        required: false\n        model: m\n"))); err != nil {
		t.Fatalf("optional critic must parse: %v", err)
	}
	// required without enabled is invalid.
	if _, err := ParseCouncilPolicy(base(full("        enabled: false\n        required: true\n        model: m\n"))); err == nil {
		t.Fatal("required while disabled must be rejected")
	}
	// enabled role referencing an unknown model is invalid.
	if _, err := ParseCouncilPolicy(base(full("        enabled: true\n        model: nope\n"))); err == nil {
		t.Fatal("unknown model profile must be rejected")
	}
}

func TestAssertEndpointURL(t *testing.T) {
	if _, err := assertEndpointURL("https://api.example.com/v1", "test", false); err != nil {
		t.Fatalf("https endpoint: %v", err)
	}
	loopback, err := assertEndpointURL("http://127.0.0.1:8080/v1", "test", true)
	if err != nil {
		t.Fatalf("loopback http with allow: %v", err)
	}
	if loopback.Port != 8080 || loopback.Host != "127.0.0.1" {
		t.Fatalf("loopback endpoint: %+v", loopback)
	}
	if _, err := assertEndpointURL("http://api.example.com/v1", "test", false); err == nil {
		t.Fatal("non-loopback http must be rejected")
	}
	if _, err := assertEndpointURL("https://user:pass@api.example.com/v1", "test", false); err == nil {
		t.Fatal("userinfo must be rejected")
	}
	if _, err := assertEndpointURL("https://api.example.com/v1?q=1", "test", false); err == nil {
		t.Fatal("query must be rejected")
	}
	// Custom provider without base_url has no known endpoint.
	if knownProviderEndpoint("custom-provider") != "" {
		t.Fatal("unknown provider must have no default endpoint")
	}
	if knownProviderEndpoint("openai") != "https://api.openai.com/v1" {
		t.Fatal("openai default endpoint mismatch")
	}
	if knownProviderEndpoint("deepseek") != "https://api.deepseek.com" {
		t.Fatal("deepseek default endpoint mismatch")
	}
}

func TestResolveCredential(t *testing.T) {
	if resolved := ResolveCredential("p", "", "local-token"); resolved.Token != "local-token" || resolved.CredentialSource != "local" {
		t.Fatalf("local token wins: %+v", resolved)
	}
	t.Setenv("COUNCIL_TEST_TOKEN", "env-token")
	if resolved := ResolveCredential("p", "COUNCIL_TEST_TOKEN", ""); resolved.Token != "env-token" || resolved.CredentialSource != "env" {
		t.Fatalf("env token: %+v", resolved)
	}
	if resolved := ResolveCredential("p", "", ""); resolved.CredentialSource != "missing" || resolved.Token != "" {
		t.Fatalf("missing credential: %+v", resolved)
	}
}

func TestPolicyHashBOMStripping(t *testing.T) {
	directory := t.TempDir()
	plain := filepath.Join(directory, "plain.yaml")
	if err := os.WriteFile(plain, []byte("review:\n  enabled: true\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	bomPath := filepath.Join(directory, "bom.yaml")
	if err := os.WriteFile(bomPath, append([]byte{0xEF, 0xBB, 0xBF}, []byte("review:\n  enabled: true\n")...), 0o644); err != nil {
		t.Fatalf("write bom: %v", err)
	}
	plainHash, err := PolicyHash(plain)
	if err != nil {
		t.Fatalf("plain hash: %v", err)
	}
	bomHash, err := PolicyHash(bomPath)
	if err != nil {
		t.Fatalf("bom hash: %v", err)
	}
	if plainHash != bomHash {
		t.Fatal("a UTF-8 BOM is encoding metadata, not a policy change")
	}
}

func TestLocalProviderOverlay(t *testing.T) {
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, ".bsl-flow"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Empty (no overlay file) is fine.
	overlay, err := LocalProviderOverlay(directory)
	if err != nil || len(overlay) != 0 {
		t.Fatalf("empty overlay: %v %+v", err, overlay)
	}
	local := filepath.Join(directory, ".bsl-flow", "providers.local.yaml")
	content := "providers:\n  deepseek:\n    token: sk-local\n    base_url: https://local.example.com/v1\n"
	if err := os.WriteFile(local, []byte(content), 0o644); err != nil {
		t.Fatalf("write local: %v", err)
	}
	overlay, err = LocalProviderOverlay(directory)
	if err != nil {
		t.Fatalf("overlay: %v", err)
	}
	entry := overlay["deepseek"]
	if !entry.HasToken || !entry.HasBaseURL || entry.BaseURL != "https://local.example.com/v1" {
		t.Fatalf("overlay entry: %+v", entry)
	}
	// token_env inside the local overlay is refused.
	bad := "providers:\n  deepseek:\n    token_env: DEEPSEEK_API_KEY\n"
	if err := os.WriteFile(local, []byte(bad), 0o644); err != nil {
		t.Fatalf("write bad local: %v", err)
	}
	if _, err := LocalProviderOverlay(directory); err == nil {
		t.Fatal("token_env inside providers.local.yaml must be refused")
	}
}

func TestYamlValueReader(t *testing.T) {
	text := "review:\n  council:\n    enabled: true\n    max_parallel: 2\n    roles:\n      chair:\n        model: m\n"
	value, err := yamlValue(text, []string{"review", "council", "enabled"}, "")
	if err != nil || value != "true" {
		t.Fatalf("enabled: %q %v", value, err)
	}
	value, err = yamlValue(text, []string{"review", "council", "max_parallel"}, "")
	if err != nil || value != "2" {
		t.Fatalf("max_parallel: %q %v", value, err)
	}
	// Children are only value-less mapping nodes (roles/budget), never scalar
	// keys (enabled/max_parallel) — mirrors Get-BSLFlowYamlChildren.
	if children := strings.Join(yamlChildren(text, []string{"review", "council"}), ","); children != "roles" {
		t.Fatalf("children: %s", children)
	}
	if keys := strings.Join(yamlDirectKeys(text, []string{"review", "council"}), ","); keys != "enabled,max_parallel,roles" {
		t.Fatalf("direct keys: %s", keys)
	}
	// Duplicate value is a contract error.
	dup := "review:\n  council:\n    enabled: true\n    enabled: false\n"
	if _, err := yamlValue(dup, []string{"review", "council", "enabled"}, ""); err == nil {
		t.Fatal("duplicate yaml value must be rejected")
	}
}
