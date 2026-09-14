package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCriticModelContract(t *testing.T) {
	luna, err := CriticModelContract("gpt-5.6-luna")
	if err != nil {
		t.Fatal(err)
	}
	if luna.Slug != "gpt-5.6-luna" || len(luna.ExperimentalSupportedTools) != 0 ||
		luna.MultiAgentVersion != "v1" || luna.MultiAgentReasoningEffort != "" {
		t.Fatalf("luna contract: %+v", luna)
	}
	astra, err := CriticModelContract("gpt-6-astra")
	if err != nil {
		t.Fatal(err)
	}
	if astra.Slug != "gpt-6-astra" || len(astra.ExperimentalSupportedTools) != 2 ||
		astra.MultiAgentVersion != "v2" || astra.MultiAgentReasoningEffort != "xhigh" {
		t.Fatalf("astra contract: %+v", astra)
	}
	if _, err := CriticModelContract("gpt-5.6-sol"); err == nil ||
		err.Error() != "BF_BLOCKED: sealed Codex critic model is not allowlisted: gpt-5.6-sol" {
		t.Fatalf("unlisted model: %v", err)
	}
}

func TestCriticCapabilityVersion(t *testing.T) {
	version, err := CriticCapabilityVersion("gpt-6-astra")
	if err != nil {
		t.Fatal(err)
	}
	if version != "codex-0.154.0-gpt-6-astra-direct-empty-tools-v1" {
		t.Fatalf("capability version: %s", version)
	}
	if _, err := CriticCapabilityVersion("other"); err == nil ||
		ClassOf(err) != "BF_BLOCKED" {
		t.Fatalf("unlisted capability: %v", err)
	}
}

func TestConvertCriticCatalogSealsTools(t *testing.T) {
	source, err := readJSONObjectFile("testdata/critic-catalog-source.json")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := ConvertCriticCatalog(source, "gpt-6-astra", "xhigh")
	if err != nil {
		t.Fatal(err)
	}
	models, _ := asArray(catalog.Catalog["models"])
	if len(models) != 1 {
		t.Fatalf("catalog models: %v", catalog.Catalog["models"])
	}
	sealed, _ := asObject(models[0])
	expectations := map[string]any{
		"shell_type":                        "disabled",
		"apply_patch_tool_type":             nil,
		"web_search_tool_type":              nil,
		"tool_mode":                         "direct",
		"multi_agent_version":               nil,
		"multi_agent_reasoning_effort":      nil,
		"supports_search_tool":              false,
		"node_repl_disabled":                true,
		"node_repl_auto_review_required":    false,
		"include_skills_usage_instructions": false,
		"include_plugin_usage_instructions": false,
		"include_apps_usage_instructions":   false,
	}
	for field, expected := range expectations {
		actual, present := sealed[field]
		if !present {
			t.Fatalf("sealed model lost optional field %s", field)
		}
		switch expected.(type) {
		case nil:
			if actual != nil {
				t.Fatalf("field %s: got %v, want null", field, actual)
			}
		case bool:
			flag, _ := actual.(bool)
			if flag != expected.(bool) {
				t.Fatalf("field %s: got %v, want %v", field, actual, expected)
			}
		case string:
			text, _ := actual.(string)
			if text != expected.(string) {
				t.Fatalf("field %s: got %v, want %v", field, actual, expected)
			}
		}
	}
	if tools, _ := asArray(sealed["experimental_supported_tools"]); len(tools) != 0 {
		t.Fatalf("experimental tools not sealed: %v", tools)
	}
	// The raw source binding is retained for auditability.
	if _, err := assertFields(catalog.Source, []string{"source_cache_path", "source_cache_sha256", "model"}, nil, "critic catalog source"); err != nil {
		t.Fatalf("source shape: %v", err)
	}
}

func TestConvertCriticCatalogRefusals(t *testing.T) {
	readSource := func() map[string]any {
		source, err := readJSONObjectFile("testdata/critic-catalog-source.json")
		if err != nil {
			t.Fatal(err)
		}
		return source
	}
	cases := []struct {
		name    string
		mutate  func(map[string]any)
		model   string
		effort  string
		wantErr string
	}{
		{
			name:    "unknown source field",
			mutate:  func(source map[string]any) { source["extra"] = true },
			wantErr: "BF_INVALID: unknown field critic catalog source.extra.",
		},
		{
			name:    "invalid source identity",
			mutate:  func(source map[string]any) { source["source_cache_sha256"] = "not-a-hash" },
			wantErr: "BF_BLOCKED: invalid critic source catalog identity.",
		},
		{
			name: "signature mismatch",
			mutate: func(source map[string]any) {
				model, _ := asObject(source["model"])
				model["shell_type"] = "legacy"
			},
			wantErr: "BF_BLOCKED: gpt-6-astra model metadata differs from the verified sealed-critic signature.",
		},
		{
			name: "missing astra field",
			mutate: func(source map[string]any) {
				model, _ := asObject(source["model"])
				delete(model, "multi_agent_reasoning_effort")
			},
			wantErr: "BF_BLOCKED: gpt-6-astra model metadata is missing multi_agent_reasoning_effort.",
		},
		{
			name: "experimental tool drift",
			mutate: func(source map[string]any) {
				model, _ := asObject(source["model"])
				model["experimental_supported_tools"] = []any{"clock"}
			},
			wantErr: "BF_BLOCKED: gpt-6-astra experimental tool metadata differs from the verified sealed-critic signature.",
		},
		{
			name: "undeclared reasoning effort",
			mutate: func(source map[string]any) {
				model, _ := asObject(source["model"])
				model["supported_reasoning_levels"] = []any{map[string]any{"effort": "low"}}
			},
			effort:  "xhigh",
			wantErr: "BF_BLOCKED: gpt-6-astra catalog does not declare the observed reasoning effort.",
		},
		{
			name: "duplicate reasoning levels",
			mutate: func(source map[string]any) {
				model, _ := asObject(source["model"])
				model["supported_reasoning_levels"] = []any{map[string]any{"effort": "xhigh"}, map[string]any{"effort": "xhigh"}}
			},
			effort:  "xhigh",
			wantErr: "BF_BLOCKED: gpt-6-astra catalog contains duplicate reasoning levels.",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			// Fresh parse per case: the mutations reach nested model maps.
			mutated := readSource()
			test.mutate(mutated)
			model := test.model
			if model == "" {
				model = "gpt-6-astra"
			}
			_, err := ConvertCriticCatalog(mutated, model, test.effort)
			if err == nil || err.Error() != test.wantErr {
				t.Fatalf("diagnostic mismatch:\n got %v\nwant %s", err, test.wantErr)
			}
		})
	}
}

func TestCriticCatalogFromSourcePathAndCache(t *testing.T) {
	// Exact source path route.
	catalog, err := CriticCatalogFromSourcePath("testdata/critic-catalog-source.json", "gpt-6-astra", "xhigh")
	if err != nil {
		t.Fatal(err)
	}
	catalogHash, err := hashValue(catalog.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	if catalogHash == "" {
		t.Fatal("catalog hash missing")
	}
	if _, err := CriticCatalogFromSourcePath(filepath.Join(t.TempDir(), "missing.json"), "gpt-6-astra", ""); err == nil ||
		err.Error() != "BF_BLOCKED: exact Codex critic catalog source is missing." {
		t.Fatalf("missing source: %v", err)
	}
	// Bounded cache route with a copied fixture and a TOCTOU-stable read.
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	data, err := os.ReadFile("testdata/models-cache.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "models_cache.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	cached, err := CachedCriticModel("gpt-5.6-luna", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assertFields(cached, []string{"source_cache_path", "source_cache_sha256", "model"}, nil, "critic cache"); err != nil {
		t.Fatalf("cached shape: %v", err)
	}
	// The allowlist gate runs before the cache scan
	// (Get-BFCodexCachedCriticModel, ProfiledCodex.ps1:106).
	if _, err := CachedCriticModel("gpt-5.6-sol", ""); err == nil ||
		err.Error() != "BF_BLOCKED: sealed Codex critic model is not allowlisted: gpt-5.6-sol" {
		t.Fatalf("allowlist gate: %v", err)
	}
	// An allowlisted model absent from the cache has no unique entry.
	emptyHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(emptyHome, "models_cache.json"), []byte(`{"models":[{"slug":"gpt-5.6-sol"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", emptyHome)
	if _, err := CachedCriticModel("gpt-5.6-luna", ""); err == nil ||
		err.Error() != "BF_BLOCKED: native model catalog has no unique gpt-5.6-luna entry." {
		t.Fatalf("unique entry: %v", err)
	}
	t.Setenv("CODEX_HOME", home)
	// Attempt directory route: a preserved attempt directory must carry its
	// critic-catalog-source.json.
	attempt := t.TempDir()
	if _, err := CriticCatalogFromDirectory(attempt, "gpt-5.6-luna", ""); err == nil ||
		err.Error() != "BF_BLOCKED: partial Codex critic catalog; reconcile without retry." {
		t.Fatalf("partial attempt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(attempt, "critic-catalog-source.json"), mustCanonical(t, cached), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CriticCatalogFromDirectory(attempt, "gpt-5.6-luna", ""); err != nil {
		t.Fatalf("resumed attempt: %v", err)
	}
}

func TestCriticOverrides(t *testing.T) {
	overrides := CriticOverrides(`C:\codex config\catalog.json`)
	joined := strings.Join(overrides, " ")
	for _, required := range []string{
		`-c model_provider="openai"`,
		`-c model_catalog_json="C:/codex config/catalog.json"`,
		`-c features.shell_tool=false`,
		`-c features.enable_request_compression=false`,
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("overrides missing %s in %v", required, overrides)
		}
	}
	if len(overrides) != 2*(6+13) {
		t.Fatalf("override count: %d", len(overrides))
	}
}

func TestProfiledCodexOverrides(t *testing.T) {
	overrides := ProfiledCodexOverrides("permissions.bsl_execution={filesystem={}}", `C:\scratch\logs`)
	if overrides[0] != "-c" || overrides[1] != `approval_policy="never"` {
		t.Fatalf("first override: %v", overrides[:2])
	}
	joined := strings.Join(overrides, " ")
	if !strings.Contains(joined, `-c log_dir="C:/scratch/logs"`) {
		t.Fatalf("log dir override: %v", overrides)
	}
	if !strings.Contains(joined, "--disable skill_mcp_dependency_install") {
		t.Fatalf("feature disable: %v", overrides)
	}
}

func mustCanonical(t *testing.T, value any) []byte {
	t.Helper()
	data, err := repositoryCanonical(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
