package repository

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func dependencyPackageFixture(t *testing.T) (string, map[string]any) {
	t.Helper()
	packageRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	provider := filepath.Join(packageRoot, "global", "skills", "1c-task", "scripts", "Task.Provider.ps1")
	if _, err := os.Stat(provider); err != nil {
		t.Skipf("packaged Task.Provider.ps1 is unavailable: %v", err)
	}
	data, err := os.ReadFile(provider)
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	taskID := "11111111-1111-4111-8111-111111111111"
	payload := map[string]any{
		"project_path":      project,
		"intent_hash":       "intent",
		"policy_hash":       "policy",
		"baseline":          "baseline",
		"classification":    map[string]any{"complexity": "S", "risk": "low"},
		"correction_rounds": int64(2),
		"repair":            map[string]any{"rounds": int64(1), "pending_failure": map[string]any{"attempt_id": "a"}},
		"policy_files": []any{map[string]any{
			"path":   provider,
			"sha256": fileSHA256(data),
		}},
		"request": map[string]any{
			"request_id":         taskID,
			"criteria":           []any{map[string]any{"id": "static", "kind": "file_assertion", "path": "readme.txt"}},
			"max_source_repairs": int64(0),
		},
	}
	return packageRoot, payload
}

func TestCurrentNativeDependenciesBindsStageSpecificInputs(t *testing.T) {
	_, payload := dependencyPackageFixture(t)
	manifest := map[string]any{"sha256": "source"}
	cases := []struct {
		stage string
		keys  []string
	}{
		{stage: "inspect", keys: []string{"architecture", "baseline", "intent", "policy"}},
		{stage: "spec", keys: []string{"architecture", "classification", "intent", "policy", "spec"}},
		{stage: "spec_review", keys: []string{"architecture", "classification", "intent", "policy", "review_binding", "spec"}},
		{stage: "implement", keys: []string{"architecture", "classification", "correction_round", "criteria", "intent", "policy", "source", "spec"}},
		{stage: "code_review", keys: []string{"architecture", "classification", "correction_round", "criteria", "intent", "policy", "source", "spec"}},
		{stage: "verify", keys: []string{"architecture", "classification", "correction_round", "criteria", "intent", "policy", "source", "spec"}},
		{stage: "diagnose", keys: []string{"architecture", "classification", "correction_round", "criteria", "failure_attempt", "intent", "policy", "source", "spec"}},
		{stage: "acceptance", keys: []string{"architecture", "classification", "correction_round", "criteria", "intent", "policy", "source", "spec"}},
	}
	for _, tc := range cases {
		t.Run(tc.stage, func(t *testing.T) {
			dependencies, err := currentNativeDependencies(payload, tc.stage, manifest)
			if err != nil {
				t.Fatal(err)
			}
			actual := make([]string, 0, len(dependencies))
			for key := range dependencies {
				actual = append(actual, key)
			}
			if !reflect.DeepEqual(sortedStrings(actual), sortedStrings(tc.keys)) {
				t.Fatalf("dependency keys = %v, want %v", sortedStrings(actual), sortedStrings(tc.keys))
			}
		})
	}
}

func TestCurrentNativeDependenciesBindsNativePlatformForNativeCriterion(t *testing.T) {
	_, payload := dependencyPackageFixture(t)
	nativeCriterion := func(kind string) map[string]any {
		return map[string]any{
			"id": "native", "kind": kind, "observation": "requires 1C",
			"executable":       filepath.Join(t.TempDir(), "1cv8.exe"),
			"arguments":        []any{},
			"protected_paths":  []any{"tests"},
			"target":           t.TempDir(),
			"expected_tests":   []any{"Suite.Test"},
			"native_1c": map[string]any{
				"source_root": "src", "extension": "Ext", "module": "Tests",
				"platform_version": "8.3.25.1445",
				"executable_sha256": strings.Repeat("a", 64),
				"authorized_operations": []any{"inventory", "load", "update", "test"},
				"authorization_reference": "operator",
			},
		}
	}
	// A native criterion on a kind other than integration keeps the exact
	// legacy shape diagnostic.
	payload["request"].(map[string]any)["criteria"] = []any{nativeCriterion("static")}
	_, err := currentNativeDependencies(payload, "verify", map[string]any{"sha256": "source"})
	requireNativeKind(t, err, "BF_INVALID")
	if err == nil || !strings.Contains(err.Error(), "native_1c is supported only for integration criteria.") {
		t.Fatalf("unexpected shape error: %v", err)
	}
	// A valid-shape native criterion without an authorized FILE target blocks
	// the dependency computation on windows, and every non-windows platform
	// surfaces the typed capability blocker.
	payload["request"].(map[string]any)["criteria"] = []any{nativeCriterion("integration")}
	_, err = currentNativeDependencies(payload, "verify", map[string]any{"sha256": "source"})
	requireNativeKind(t, err, "BF_BLOCKED")
	if err == nil || (!strings.Contains(err.Error(), "FILE target marker is required to resolve physical target identity.") &&
		!strings.Contains(err.Error(), "BLOCKED_UNSUPPORTED_PLATFORM")) {
		t.Fatalf("unexpected blocker: %v", err)
	}
}

func TestNativeArchitectureBundleHashesMatchFrozenPowerShellGolden(t *testing.T) {
	packageRoot, _ := dependencyPackageFixture(t)
	expected := map[string]string{
		"inspect":        "bb9e618a60092ea2873d9aada8cd8093f9f5ac5452eb33d6f6255ffef8ec8cd8",
		"spec":           "810ea5c3c030416c435d90fb9ce997cd0847727ef79b970c2207c3610241d847",
		"spec_review":    "3078ff1edb92cd629fb7d3fa937b147fca68962ca33dd95cd8245d10cf1eb86e",
		"spec_reconcile": "e1979cfbf1a2f485d64f84334f9eeb93b6ff4e6b70d05355481f7b1de3ddce81",
		"implement":      "816b108348e7709ec0b571f9180faecedaa30736e305f2c0d9cd48ae050052ce",
		"code_review":    "8f0531e0dd64617e17269f84f43acf415a7f1f91697fea96907bc341d45a7972",
		"code_reconcile": "485ec085ba5985b8fc2eeb312dabe772fce40687c644f6a3db9fb41ee7ff0131",
		"verify":         "65c7554e71f009cad365c1c2b0f1925205338047c059bafa1a2b086e0521c92d",
		"diagnose":       "c831945a22852f04aebd3c0b263bd4d3851d1e520910afb2acecc28388af8674",
		"acceptance":     "e0b536ee063a4399497a732a3ffaf5973429525caf96eb548a9c40d68a840fbf",
	}
	for stage, want := range expected {
		t.Run(stage, func(t *testing.T) {
			got, err := nativeArchitectureBundleHash(stage, packageRoot, packageRoot)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("architecture hash = %s, want frozen PowerShell golden %s", got, want)
			}
		})
	}
}

func TestCurrentNativeToolsetSnapshotBindsRegisteredAggregate(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "provider.exe")
	sandbox := filepath.Join(root, "sandbox.exe")
	if err := os.WriteFile(executable, []byte("provider"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sandbox, []byte("sandbox"), 0o600); err != nil {
		t.Fatal(err)
	}
	toolsetRoot := filepath.Join(root, "toolset")
	skillRoot := filepath.Join(toolsetRoot, "skill")
	if err := os.MkdirAll(skillRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	skillData := []byte("skill body\n")
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), skillData, 0o600); err != nil {
		t.Fatal(err)
	}
	files := []any{map[string]any{"path": "SKILL.md", "sha256": fileSHA256(skillData)}}
	skillHash, err := nativeToolsetAggregateHash([]any{map[string]any{"name": "skill", "files": files}})
	if err != nil {
		t.Fatal(err)
	}
	aggregate := skillHash
	manifest := map[string]any{
		"schema_version":   int64(1),
		"toolset_name":     "cc-1c-skills",
		"source":           map[string]any{"identity": "local-private", "path": root},
		"skills":           []any{map[string]any{"name": "skill", "files": files, "sha256": skillHash, "mcp_references": []any{}}},
		"aggregate_sha256": aggregate,
	}
	manifestData, err := Canonical(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(toolsetRoot, "toolset-manifest.json"), manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := filepath.Join(root, "runtime.exe")
	if err := os.WriteFile(runtime, []byte("runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeHash := fileSHA256([]byte("runtime"))
	payload := map[string]any{"request": map[string]any{
		"execution_profile": map[string]any{
			"provider": "codex", "executable": executable, "executable_sha256": fileSHA256([]byte("provider")),
			"sandbox":           map[string]any{"executable": sandbox, "sha256": fileSHA256([]byte("sandbox"))},
			"toolset":           map[string]any{"name": "cc-1c-skills", "root": toolsetRoot, "sha256": aggregate},
			"denied_read_roots": []any{root}, "codex_skills_sha256": strings.Repeat("a", 64),
			"runtime": map[string]any{"executable": runtime, "sha256": runtimeHash, "version": "1.0.0", "packages": []any{map[string]any{"name": "lxml", "version": "5.0.0"}}},
		},
		"models": map[string]any{"worker": "test"},
	}}
	execution, err := currentNativeExecutionDependencies(payload)
	if err != nil {
		t.Fatal(err)
	}
	if asStringOr(asMap(execution["profile"])["toolset"].(map[string]any)["sha256"]) != aggregate {
		t.Fatalf("execution profile toolset hash changed unexpectedly: %#v", execution)
	}
	payload["request"].(map[string]any)["execution_profile"].(map[string]any)["toolset"].(map[string]any)["sha256"] = strings.Repeat("b", 64)
	if _, err := currentNativeExecutionDependencies(payload); err == nil {
		t.Fatal("registered toolset aggregate mismatch was accepted")
	}
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
