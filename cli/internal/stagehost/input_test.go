package stagehost

import (
	"path/filepath"
	"testing"
)

func TestProviderAttemptExecutableExtensions(t *testing.T) {
	root := t.TempDir()
	worker, err := safePath(filepath.Join(root, "worker"))
	if err != nil {
		t.Fatalf("worker path: %v", err)
	}
	const taskID = "00000000-0000-0000-0000-000000000001"
	const attemptID = "00000000-0000-0000-0000-000000000002"
	stateView := map[string]any{"task_id": taskID, "active_attempt": attemptID, "stage": "verify", "worker_path": worker}
	object := map[string]any{}
	attemptWith := func(executable string) map[string]any {
		return map[string]any{
			"schema_version": int64(1), "task_id": taskID, "attempt_id": attemptID,
			"stage": "verify", "intent_revision": int64(1), "authorization_revision": int64(1),
			"dependencies": map[string]any{}, "source_manifest": map[string]any{},
			"worker_path": worker, "executable": executable,
			"requested_models": map[string]any{}, "started_at": "2026-09-13T00:00:00Z", "operation_id": attemptID,
		}
	}
	// Historical .exe attempts and extensionless native binaries both bind.
	for _, name := range []string{"codex.exe", "codex"} {
		object["attempt"] = attemptWith(filepath.Join(root, name))
		if _, err := assertProviderAttempt(object, "execute", taskID, stateView, worker); err != nil {
			t.Fatalf("attempt executable %q rejected: %v", name, err)
		}
	}
	object["attempt"] = attemptWith(filepath.Join(root, "codex.sh"))
	if _, err := assertProviderAttempt(object, "execute", taskID, stateView, worker); err == nil || err.Error() != "BF_BLOCKED: provider execution requires a native executable." {
		t.Fatalf("script attempt diagnostic: %v", err)
	}
}
