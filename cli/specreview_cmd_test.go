package main

import (
	"os"

	"bsl-flow/cli/internal/councilengine"
	"path/filepath"
	"strings"
	"testing"
)

// specreview_cmd_test.go covers the assisted spec review routing: option
// parsing and the deterministic skip path (no live dispatch).

func TestParseSpecReviewInvocation(t *testing.T) {
	in, err := parseSpecReviewInvocation([]string{"spec", "review", "--project", "p", "--change", "demo", "--force", "--json"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if in.action != "review" || in.options["--project"] != "p" || in.options["--change"] != "demo" {
		t.Fatalf("parsed: %+v", in)
	}
	if in.options["--force"] == "" || in.options["--json"] == "" {
		t.Fatalf("valueless flags: %+v", in.options)
	}
	if _, err := parseSpecReviewInvocation([]string{"spec", "review", "--project", "p"}); err == nil {
		t.Fatal("missing --change must fail")
	}
	if _, err := parseSpecReviewInvocation([]string{"spec", "review", "--project", "p", "--change", "../evil"}); err == nil {
		t.Fatal("unsafe change id must fail")
	}
}

func TestParseSpecMetricInvocation(t *testing.T) {
	in, err := parseSpecMetricInvocation([]string{"spec", "metric", "--project", "p", "--change", "demo"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if in.action != "metric" {
		t.Fatalf("action: %s", in.action)
	}
	if _, err := parseSpecMetricInvocation([]string{"spec", "metric", "--project", "p", "--change", "demo", "--bogus", "1"}); err == nil {
		t.Fatal("unknown option must fail")
	}
}

func writeRoutingFixture(t *testing.T, project string) {
	t.Helper()
	changeDir := filepath.Join(project, "openspec", "changes", "demo")
	if err := os.MkdirAll(changeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	spec := "# T\n\n## Классификация\n- Сложность: S\n- Риск: low\n\n## Требуемое поведение\n1. x\n"
	if err := os.WriteFile(filepath.Join(changeDir, "spec.md"), []byte(spec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	config := "review:\n  enabled: true\n  routing:\n    s_default: optional\n    m_default: required\n    l_default: required\n"
	if err := os.WriteFile(filepath.Join(project, "bsl-flow.yaml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestSpecReviewSkipRoute(t *testing.T) {
	project := t.TempDir()
	writeRoutingFixture(t, project)
	outcome, err := executeSpecReview(invocation{command: "spec", action: "review", options: map[string]string{"--project": project, "--change": "demo"}}, project, "demo")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if outcome.ReviewRequired {
		t.Fatal("S low/medium change must skip external review by default")
	}
	if outcome.Route != "optional" {
		t.Fatalf("outcome: %+v", outcome)
	}
}

func TestSpecReviewForcedRequiresOriginalTask(t *testing.T) {
	project := t.TempDir()
	writeRoutingFixture(t, project)
	// Force review without original-task.md -> error.
	_, err := executeSpecReview(invocation{command: "spec", action: "review", options: map[string]string{"--project": project, "--change": "demo", "--force": "1"}}, project, "demo")
	if err == nil || !strings.Contains(err.Error(), "original-task.md is required") {
		t.Fatalf("forced review must require original-task.md: %v", err)
	}
}

func TestSpecReviewConflictComplexity(t *testing.T) {
	project := t.TempDir()
	writeRoutingFixture(t, project)
	_, err := executeSpecReview(invocation{command: "spec", action: "review", options: map[string]string{"--project": project, "--change": "demo", "--complexity": "L"}}, project, "demo")
	if err == nil || !strings.Contains(err.Error(), "conflicts with spec.md") {
		t.Fatalf("explicit complexity conflict must fail: %v", err)
	}
}

func TestAssertCouncilRoutesStartRefusesTokenlessRolesBeforeDispatch(t *testing.T) {
	config := `
llm:
  providers:
    deepseek:
      protocol: openai_compatible
      base_url: https://api.deepseek.com
      token_env: BSL_TEST_MISSING_KEY
  models:
    flash:
      provider: deepseek
      model: deepseek-flash
review:
  council:
    enabled: true
    roles:
      intent_critic:
        model: flash
      architecture_critic:
        model: flash
      executability_critic:
        model: flash
      chair:
        model: flash
`
	policy, err := councilengine.ParseCouncilPolicy(config)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	project := t.TempDir()
	err = assertCouncilRoutesStart(project, policy)
	if err == nil {
		t.Fatal("tokenless council started without refusal")
	}
	for _, want := range []string{"BF_BLOCKED: council cannot start", "role intent_critic", "current-agent fallback", "BSL_TEST_MISSING_KEY", "providers.local.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("remedy message lost %q: %v", want, err)
		}
	}
	blockConfig := strings.Replace(config, "      intent_critic:\n        model: flash", "      intent_critic:\n        model: flash\n        fallback: block", 1)
	blockPolicy, err := councilengine.ParseCouncilPolicy(blockConfig)
	if err != nil {
		t.Fatalf("block policy: %v", err)
	}
	err = assertCouncilRoutesStart(project, blockPolicy)
	if err == nil || !strings.Contains(err.Error(), "fallback policy is block") {
		t.Fatalf("block fallback refusal diverged: %v", err)
	}
	t.Setenv("BSL_TEST_MISSING_KEY", "test-token")
	if err := assertCouncilRoutesStart(project, policy); err != nil {
		t.Fatalf("credentialed council refused: %v", err)
	}
}

func TestAssertEndpointURLDefaultsBasePath(t *testing.T) {
	config := `
llm:
  providers:
    deepseek:
      protocol: openai_compatible
      base_url: https://api.deepseek.com
  models:
    flash:
      provider: deepseek
      model: deepseek-flash
`
	policy, err := councilengine.ParseCouncilPolicy(config)
	if err != nil {
		t.Fatalf("bare host endpoint refused: %v", err)
	}
	endpoint := policy.Providers["deepseek"].Endpoint
	if endpoint.BasePath != "/" {
		t.Fatalf("bare host base_path = %q, want \"/\"", endpoint.BasePath)
	}
	if endpoint.Port != 443 || endpoint.Host != "api.deepseek.com" {
		t.Fatalf("unexpected decomposition: %+v", endpoint)
	}
	namedConfig := strings.Replace(config, "base_url: https://api.deepseek.com\n", "base_url: https://api.deepseek.com/v1\n", 1)
	namedPolicy, err := councilengine.ParseCouncilPolicy(namedConfig)
	if err != nil {
		t.Fatalf("named path refused: %v", err)
	}
	if namedPolicy.Providers["deepseek"].Endpoint.BasePath != "/v1" {
		t.Fatalf("explicit path diverged: %+v", namedPolicy.Providers["deepseek"].Endpoint)
	}
}
