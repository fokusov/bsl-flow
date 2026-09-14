package stagehost

import (
	"path/filepath"
	"strings"
	"testing"

	"bsl-flow/cli/internal/repository"
)

func verifyState(t *testing.T, project, worker, baseline, attemptID string, criteria []any, profile map[string]any) map[string]any {
	t.Helper()
	request := map[string]any{
		"schema_version": int64(1), "request_id": "10000000-0000-0000-0000-00000000000a",
		"prompt": "implement the fence", "mode": "implement", "analysis_goal": "analysis",
		"complexity": "S", "risk": "low", "impact_flags": []any{},
		"criteria": criteria, "provenance": map[string]any{"source": "user", "reference": "chat", "text": "do it"},
		"models":       map[string]any{"worker": "gpt-6-astra", "worker_effort": "medium", "reviewer": "gpt-6-astra", "reviewer_effort": "high"},
		"max_attempts": int64(4), "timeout_seconds": int64(60), "max_source_repairs": int64(0),
	}
	if profile != nil {
		request["execution_profile"] = profile
		request["budget"] = map[string]any{"currency": "USD", "limit": int64(10), "reservation": int64(0)}
	}
	activeAttempt := any(nil)
	if attemptID != "" {
		activeAttempt = attemptID
	}
	return map[string]any{
		"schema_version": int64(1), "task_id": "10000000-0000-0000-0000-00000000000a",
		"revision": int64(2), "previous_sha256": strings.Repeat("d", 64),
		"project_path": project, "worker_path": worker, "baseline": baseline,
		"request": request, "request_hash": strings.Repeat("e", 64),
		"intent_revision": int64(1), "authorization_revision": int64(1),
		"intent_hash": strings.Repeat("f", 64), "policy_hash": strings.Repeat("0", 64),
		"policy_files": []any{}, "policy_rules": map[string]any{"s_review_required": false},
		"classification": map[string]any{"complexity": "S", "risk": "low", "impact_flags": []any{}, "rationale": "small"},
		"status":         "running", "stage": "verify", "active_attempt": activeAttempt,
		"unresolved_effect": nil, "attempts": []any{}, "evidence": []any{}, "events": []any{},
		"question": nil, "blockers": []any{}, "acceptances": []any{},
		"created_at": "2026-09-13T00:00:00Z", "updated_at": "2026-09-13T00:00:00Z",
		"correction_rounds": int64(0),
	}
}

func providerInputDocument(operation string, state map[string]any, attempt map[string]any, roots map[string]string, deps Deps) map[string]any {
	return map[string]any{
		"schema_version": int64(1), "contract": Contract, "operation": operation,
		"task_id": state["task_id"], "state_view": state, "attempt": attempt,
		"context_root": roots["context"], "artifact_root": roots["artifact"],
		"canonical_store_root": filepath.Join(state["project_path"].(string), ".git", "bsl-flow"),
		"cancel_signal":        roots["cancel"],
		"provider_contract": map[string]any{
			"name": Contract, "version": int64(1),
			"host_sha256": deps.SelfSHA256, "provider_sha256": deps.ProviderSHA256, "asset_manifest_sha256": deps.AssetManifestSHA256,
		},
		"prior_artifacts": []any{},
	}
}

func testAttempt(t *testing.T, state map[string]any, stage string) map[string]any {
	t.Helper()
	attemptID := "20000000-0000-0000-0000-00000000000b"
	bound, err := repository.StageHostDependencies(state, stage, nil)
	if err != nil {
		t.Fatalf("dependencies: %v", err)
	}
	manifest, err := repository.StageHostSourceManifest(asStringOr(state["worker_path"]), asStringOr(state["baseline"]))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return map[string]any{
		"schema_version": int64(1), "task_id": state["task_id"], "attempt_id": attemptID,
		"stage": stage, "intent_revision": int64(1), "authorization_revision": int64(1),
		"dependencies": bound, "source_manifest": manifest, "worker_path": state["worker_path"],
		"executable":       state["request"].(map[string]any)["execution_profile"].(map[string]any)["executable"],
		"requested_models": map[string]any{}, "started_at": "2026-09-13T00:00:00Z", "operation_id": attemptID,
	}
}

// stagePolicyInventory builds the policy_files snapshot the provider state
// view must carry for dependency computation.
func stagePolicyInventory(t *testing.T, project, skillsRoot, hostPath string) []any {
	t.Helper()
	inventory, err := repository.StageHostPolicyInventory(project, skillsRoot, hostPath)
	if err != nil {
		t.Fatalf("policy inventory: %v", err)
	}
	return inventory
}
