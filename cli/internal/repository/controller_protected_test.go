package repository

import "testing"

func TestNativeProtectedTestsRejectModifiedAddedDeletedAndMissingInputs(t *testing.T) {
	payload := map[string]any{"request": map[string]any{"criteria": []any{map[string]any{"protected_paths": []any{"tests"}}}}}
	entry := func(path, hash string, deleted bool) any {
		return map[string]any{"path": path, "sha256": hash, "deleted": deleted}
	}
	manifest := func(files ...any) map[string]any { return map[string]any{"files": files} }
	before := manifest(entry("tests/test.go", "original", false), entry("src/main.go", "old", false))
	if err := validateNativeProtectedTests(payload, before, manifest(entry("tests/test.go", "original", false), entry("src/main.go", "new", false))); err != nil {
		t.Fatal(err)
	}
	for name, after := range map[string]map[string]any{
		"modified": manifest(entry("tests/test.go", "new", false)),
		"deleted":  manifest(entry("tests/test.go", "", true)),
		"missing":  manifest(),
		"added":    manifest(entry("tests/test.go", "original", false), entry("tests/skip.go", "bypass", false)),
	} {
		t.Run(name, func(t *testing.T) {
			if validateNativeProtectedTests(payload, before, after) == nil {
				t.Fatal("protected test mutation accepted")
			}
		})
	}
	if validateNativeProtectedTests(payload, manifest(), manifest()) == nil {
		t.Fatal("absent baseline accepted")
	}
}
