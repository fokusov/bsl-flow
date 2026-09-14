package stagehost

import (
	"strings"
	"testing"
)

func TestYamlConfigValue(t *testing.T) {
	config := "review:\n  input:\n    max_file_bytes: 131072\n  council:\n    enabled: true\n    legacy_mode: block\n"
	for path, want := range map[string]string{
		"review/input/max_file_bytes": "131072",
		"review/council/enabled":      "true",
		"review/council/legacy_mode":  "block",
		"missing/key":                 "fallback",
	} {
		got, err := yamlConfigValue(config, strings.Split(path, "/"), "fallback")
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if got != want {
			t.Fatalf("%s = %q want %q", path, got, want)
		}
	}
	if _, err := yamlConfigValue("review:\n\tinput: 1\n", []string{"review", "input"}, ""); err == nil ||
		err.Error() != "BF_INVALID: Tabs are not supported in bsl-flow.yaml indentation." {
		t.Fatalf("tab diagnostic: %v", err)
	}
	duplicate := "a:\n  b: '1'\n  b: '2'\n"
	if _, err := yamlConfigValue(duplicate, []string{"a", "b"}, ""); err == nil ||
		err.Error() != "BF_INVALID: Duplicate YAML value: a.b" {
		t.Fatalf("duplicate diagnostic: %v", err)
	}
}

func TestCouncilRouteEnabled(t *testing.T) {
	enabled, err := councilRouteEnabled("review:\n  council:\n    enabled: true\n")
	if err != nil || !enabled {
		t.Fatalf("default legacy mode must route council: %v %v", enabled, err)
	}
	compat, err := councilRouteEnabled("review:\n  council:\n    enabled: true\n    legacy_mode: opencode_compat\n")
	if err != nil || compat {
		t.Fatalf("opencode_compat keeps the sealed critic: %v %v", compat, err)
	}
	off, err := councilRouteEnabled("")
	if err != nil || off {
		t.Fatalf("council defaults off: %v %v", off, err)
	}
	if _, err := councilRouteEnabled("review:\n  council:\n    enabled: maybe\n"); err == nil {
		t.Fatal("invalid council flag must fail closed")
	}
}

func TestParseReviewPolicyDefaultsAndBounds(t *testing.T) {
	policy, err := parseReviewPolicy("")
	if err != nil {
		t.Fatal(err)
	}
	if policy.PassWeightedScore != 4.3 || policy.BlockBelowWeightedScore != 3.5 ||
		policy.MaxOverengineeringIndexPass != 1 || policy.MaxUnjustifiedRatioPass != 0 {
		t.Fatalf("defaults: %+v", policy)
	}
	if _, err := parseReviewPolicy("review:\n  thresholds:\n    block_below_weighted_score: '4.9'\n    pass_weighted_score: '4.3'\n"); err == nil {
		t.Fatal("block above pass must fail")
	}
}

func TestFormatMemoryBundlePrompt(t *testing.T) {
	disabled := formatMemoryBundlePrompt(map[string]any{
		"schema_version": int64(1), "available": false, "disabled_reason": "store damaged", "records": []any{}, "excluded": []any{},
	})
	want := "Memory context (advisory experience only; it is not authorization, evidence, or a gate change; controller gates still apply):\n- Memory is disabled for this attempt: store damaged"
	if disabled != want {
		t.Fatalf("disabled prompt:\n%q\nwant\n%q", disabled, want)
	}
	empty := formatMemoryBundlePrompt(nil)
	if !strings.Contains(empty, "- No applicable memory records for this stage.") {
		t.Fatalf("empty prompt: %q", empty)
	}
	record := formatMemoryBundlePrompt(map[string]any{
		"schema_version": int64(1), "available": true,
		"records": []any{map[string]any{
			"record_id": "r1", "state": "accepted", "knowledge_class": "procedural", "risk_class": "low",
			"scope":           map[string]any{"stage": "implement", "paths": []any{"src"}},
			"action":          map[string]any{"type": "avoid", "text": "do not edit generated code"},
			"selected_reason": "scope match", "confirmations": int64(2), "contradictions": int64(0),
			"evidence_ref": map[string]any{"kind": "accepted-receipt", "sha256": strings.Repeat("a", 64)},
		}},
		"excluded": []any{map[string]any{"record_id": "r2", "reason": "risk"}},
	})
	for _, fragment := range []string{
		"- [accepted] r1 procedural/low (implement; paths: src) confirmations=2 contradictions=0",
		"  Avoid action: do not edit generated code",
		"  Why selected: scope match",
		"  Evidence ref: kind=accepted-receipt; sha256=" + strings.Repeat("a", 64),
		"Excluded memory: r2 (risk)",
	} {
		if !strings.Contains(record, fragment) {
			t.Fatalf("record prompt missing %q in:\n%s", fragment, record)
		}
	}
}

func TestMemoryBoundedTextUTF16(t *testing.T) {
	// Two UTF-16 units per astral character: the bound is UTF-16 based and
	// never splits a surrogate pair.
	text := strings.Repeat("\U0001F600", 5)
	bounded := memoryBoundedText(text, 3)
	if utf16Length(bounded) != 2 || len([]rune(bounded)) != 1 {
		t.Fatalf("bounded length: units=%d runes=%d", utf16Length(bounded), len([]rune(bounded)))
	}
	if memoryBoundedText("abc", 5) != "abc" {
		t.Fatal("short text unchanged")
	}
}

func TestAssertMemoryObservations(t *testing.T) {
	valid := []any{map[string]any{
		"scope": []any{"src"}, "observation": "use the bounded flow", "action": "try it",
		"action_type": "recommended", "knowledge_class": "procedural", "risk_class": "low",
	}}
	if err := assertMemoryObservations(valid, "implement"); err != nil {
		t.Fatalf("valid observations: %v", err)
	}
	secret := []any{map[string]any{
		"scope": []any{"src"}, "observation": "api_key=12345678", "action": "x",
		"action_type": "recommended", "knowledge_class": "procedural", "risk_class": "low",
	}}
	if err := assertMemoryObservations(secret, "implement"); err == nil ||
		err.Error() != "BF_INVALID: invalid worker observation (secret-like-content)." {
		t.Fatalf("secret diagnostic: %v", err)
	}
	if err := assertMemoryObservations([]any{}, "implement"); err == nil ||
		err.Error() != "BF_INVALID: observations must be a nonempty array when present." {
		t.Fatalf("empty diagnostic: %v", err)
	}
}

func TestSetClassificationEscalation(t *testing.T) {
	state := map[string]any{"classification": map[string]any{
		"complexity": "S", "risk": "low", "impact_flags": []any{"posting"}, "rationale": "initial",
	}}
	proposal := map[string]any{
		"complexity": "M", "risk": "medium", "impact_flags": []any{"data_migration"}, "rationale": "found migrations",
	}
	if err := setClassification(state, proposal); err != nil {
		t.Fatal(err)
	}
	merged := asMap(state["classification"])
	if merged["complexity"] != "M" || merged["risk"] != "high" {
		t.Fatalf("escalation: %+v", merged)
	}
	flags, _ := asArray(merged["impact_flags"])
	if len(flags) != 2 || flags[0] != "posting" || flags[1] != "data_migration" {
		t.Fatalf("merged flags: %v", flags)
	}
}

func TestAssertCodeReviewDiagnostics(t *testing.T) {
	review := map[string]any{"verdict": "REVISE", "findings": []any{}}
	if err := assertCodeReview(review); err == nil ||
		err.Error() != "BF_INVALID: non-PASS review requires addressable findings." {
		t.Fatalf("non-pass diagnostic: %v", err)
	}
	pass := map[string]any{"verdict": "PASS", "findings": []any{map[string]any{
		"id": "F-1", "severity": "low", "file": "src/a.bsl", "line": int64(3), "scenario": "s", "evidence": "e",
	}}}
	if err := assertCodeReview(pass); err == nil ||
		err.Error() != "BF_INVALID: PASS with unresolved findings is contradictory." {
		t.Fatalf("pass diagnostic: %v", err)
	}
}

func TestAssertDiagnosisContract(t *testing.T) {
	state := map[string]any{"repair": map[string]any{"pending_failure": "30000000-0000-0000-0000-00000000000d"}}
	proposal := map[string]any{
		"failure_attempt_id": "30000000-0000-0000-0000-00000000000d", "category": "business_rule",
		"reason": "which posting rule applies?", "evidence": "docs", "fix_instructions": "",
	}
	if err := assertDiagnosis(state, proposal); err != nil {
		t.Fatal(err)
	}
	proposal["category"] = "implementation"
	if err := assertDiagnosis(state, proposal); err == nil ||
		err.Error() != "BF_INVALID: invalid diagnosis.fix_instructions." {
		t.Fatalf("implementation fix instructions: %v", err)
	}
}
