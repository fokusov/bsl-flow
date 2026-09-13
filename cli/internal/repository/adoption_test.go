package repository

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func adoptionFixtureState(t *testing.T, project, id string) map[string]any {
	t.Helper()
	state := fullV1State(id, map[string]any{
		"project_path": project,
		"worker_path":  project,
		"status":       "cancelled",
		"stage":        "acceptance",
		"request_hash": strings.Repeat("a", 64),
		"intent_hash":  strings.Repeat("b", 64),
		"policy_hash":  strings.Repeat("c", 64),
	})
	return state
}

func writeLegacyAdoptionFixture(t *testing.T, project, id string) (string, []byte) {
	t.Helper()
	state := adoptionFixtureState(t, project, id)
	data, err := Canonical(state)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(project, ".bsl-flow", "tasks", id)
	if err := os.MkdirAll(filepath.Join(root, "revisions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "revisions", "000001.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// These generated files are deliberately ignored by the migration manifest.
	if err := os.WriteFile(filepath.Join(root, "current.json"), []byte(`{"revision":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, data
}

func TestMigrationMetadataIsClosed(t *testing.T) {
	valid := map[string]any{
		"receipt_sha256":  strings.Repeat("a", 64),
		"manifest_sha256": strings.Repeat("b", 64),
		"prefix_revision": int64(1),
		"requires_rebind": true,
	}
	if err := validateMigrationMetadata(valid); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{
		"unknown":             map[string]any{"receipt_sha256": strings.Repeat("a", 64), "manifest_sha256": strings.Repeat("b", 64), "prefix_revision": int64(1), "requires_rebind": true, "unknown": true},
		"bad receipt hash":    map[string]any{"receipt_sha256": "short", "manifest_sha256": strings.Repeat("b", 64), "prefix_revision": int64(1), "requires_rebind": true},
		"bad prefix revision": map[string]any{"receipt_sha256": strings.Repeat("a", 64), "manifest_sha256": strings.Repeat("b", 64), "prefix_revision": int64(0), "requires_rebind": true},
	} {
		if err := validateMigrationMetadata(value); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestAdoptionPreviewApplyPreservesPrefixAndRepeats(t *testing.T) {
	project := newRepo(t)
	id := "11111111-2222-4333-8444-555555555555"
	legacyRoot, original := writeLegacyAdoptionFixture(t, project, id)
	repository, err := openReadOnly(project)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := commandAdopt(project, id, project, "", true, false)
	if err != nil {
		t.Fatal(err)
	}
	plan := preview.(map[string]any)
	eligibility := plan["eligibility"].(map[string]any)
	if !asBoolOr(eligibility["eligible"]) {
		t.Fatalf("fixture was unexpectedly ineligible: %+v", plan["blockers"])
	}
	if repository.HasTask(id) {
		t.Fatal("preview published a canonical task")
	}
	planBytes, err := Canonical(plan)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(tempDir(t), "adoption-plan.json")
	if err := os.WriteFile(planPath, planBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := commandAdopt(project, id, project, planPath, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if asBoolOr(result.(map[string]any)["idempotent"]) {
		t.Fatal("first adoption was reported as idempotent")
	}
	target := filepath.Join(repository.StorePath, "tasks", id)
	copied, err := os.ReadFile(filepath.Join(target, "revisions", "000001.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(copied, original) {
		t.Fatal("adoption changed original revision bytes")
	}
	if _, err := os.Stat(filepath.Join(target, "adoption-plan.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "adoption-receipt.json")); err != nil {
		t.Fatal(err)
	}
	repeat, err := commandAdopt(project, id, project, planPath, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !asBoolOr(repeat.(map[string]any)["idempotent"]) {
		t.Fatal("committed adoption repeat was not idempotent")
	}
	files, err := os.ReadDir(filepath.Join(target, "revisions"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("repeat changed revision count: %d", len(files))
	}
	_ = legacyRoot
}

func TestStrictMigrationDocumentRejectsDuplicateKeys(t *testing.T) {
	if _, err := decodeStrictObject([]byte(`{"schema_version":1,"schema_version":1}`)); err == nil {
		t.Fatal("duplicate migration document key was accepted")
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(`{"schema_version":1}`), &object); err != nil {
		t.Fatal(err)
	}
}
