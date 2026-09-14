package memoryhost

import (
	"os"
	"path/filepath"
	"testing"
)

// paritySandbox recreates the package layout used by the PowerShell parity
// capture: fixed schema files, VERSION and package-manifest.json under one
// root.
func paritySandbox(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	schemas := filepath.Join(root, "global", "skills", "1c-task", "schemas")
	if err := os.MkdirAll(schemas, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"memory-event.schema.json", "memory-index.schema.json", "memory-bundle.schema.json", "context.schema.json"} {
		content := "{\"schema\":\"" + name + "\",\"fixture\":true}"
		if err := os.WriteFile(filepath.Join(schemas, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("9.9.9-parity"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package-manifest.json"), []byte(`{"fixture":"parity"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// The fingerprint components below were captured from the PowerShell helper
// running against the identical sandbox layout.
func TestFingerprintsMatchPowerShell(t *testing.T) {
	root := paritySandbox(t)
	state := baseState(filepath.Join(t.TempDir(), "project"))
	state["policy_hash"] = testPolicyHash
	fingerprints, err := memoryFingerprints(state, root)
	if err != nil {
		t.Fatal(err)
	}
	if got := fingerprints["schema"]; got != "739faeed3ecfa9107b77cf6cdd04f0df2ac51a67b23d8dfddae20ce052824c87" {
		t.Fatalf("schema fingerprint mismatch: %s", got)
	}
	if got := fingerprints["controller"]; got != "cc51d59df0576130923642273b92f682d8765528434bb3259015c04e804ad22d" {
		t.Fatalf("controller fingerprint mismatch: %s", got)
	}
	if got := fingerprints["version"]; got != "9.9.9-parity" {
		t.Fatalf("version mismatch: %s", got)
	}
	if got := fingerprints["toolchain"]; got != "unbound" {
		t.Fatalf("toolchain fingerprint mismatch: %s", got)
	}
	if got := fingerprints["policy"]; got != testPolicyHash {
		t.Fatalf("policy mismatch: %s", got)
	}
	// An execution profile whose members are all null hashes to the captured
	// toolchain golden.
	state["request"].(map[string]any)["execution_profile"] = map[string]any{}
	fingerprints, err = memoryFingerprints(state, root)
	if err != nil {
		t.Fatal(err)
	}
	if got := fingerprints["toolchain"]; got != "f327213ee07cc7bb33441d277f5dacf0c1fb14bc5f9ffc57703e0c615a8bea7e" {
		t.Fatalf("bound toolchain fingerprint mismatch: %s", got)
	}
}

func TestSchemaFingerprintMissingPackage(t *testing.T) {
	if got := schemaFingerprint(""); got != "" {
		t.Fatalf("empty package root produced %q", got)
	}
	if got := schemaFingerprint(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Fatalf("missing package root produced %q", got)
	}
}

func TestTaskKind(t *testing.T) {
	state := map[string]any{
		"request": map[string]any{
			"mode":          "implement",
			"analysis_goal": "analysis",
			"criteria":      []any{map[string]any{"kind": "file_assertion"}, map[string]any{"kind": "static"}},
			"impact_flags":  []any{},
		},
	}
	if got := taskKindOf(state); got != "implement|analysis|criteria=file_assertion,static|flags=" {
		t.Fatalf("task kind mismatch: %s", got)
	}
	if got := taskKindOf(map[string]any{}); got != "legacy" {
		t.Fatalf("legacy task kind mismatch: %s", got)
	}
}

func TestErrorSignatureGolden(t *testing.T) {
	result := map[string]any{
		"outcome":  "FAIL",
		"stage":    "verify",
		"proposal": map[string]any{"criterion_id": "crit-1", "kind": "static", "category": "build"},
	}
	const want = "949066907829358079d39b87d10c6f086e9178a4f4871d8ac65291f544909569"
	if got := errorSignatureOf(result); got != want {
		t.Fatalf("error signature mismatch: %s", got)
	}
	empty := map[string]any{"outcome": "FAIL", "stage": "verify", "proposal": map[string]any{}}
	if got := errorSignatureOf(empty); got != "" {
		t.Fatalf("untyped failure produced a signature: %s", got)
	}
	pass := map[string]any{"outcome": "PASS", "stage": "verify"}
	if got := errorSignatureOf(pass); got != "" {
		t.Fatalf("PASS produced a signature: %s", got)
	}
}

func TestSecretLike(t *testing.T) {
	cases := map[string]bool{
		"":                           true,
		"   ":                        true,
		"plain observation":          false,
		"uses a password here":       true,
		"Authorization: Basic abc":   true,
		"bearer abcdefghijklmno":     true,
		"-----BEGIN RSA PRIVATE KEY": true,
		"api_key was leaked":         true,
	}
	for text, want := range cases {
		if got := isSecretLike(text); got != want {
			t.Fatalf("isSecretLike(%q) = %v want %v", text, got, want)
		}
	}
}

func TestConvertEvidenceRef(t *testing.T) {
	good := map[string]any{"kind": "attempt-result", "task_id": testTaskID, "sha256": strings64hex()}
	converted := convertEvidenceRef(good)
	if converted == nil || converted["kind"] != "attempt-result" {
		t.Fatalf("valid ref rejected: %v", converted)
	}
	if convertEvidenceRef(map[string]any{"kind": "x", "extra": "1"}) != nil {
		t.Fatal("unknown member accepted")
	}
	if convertEvidenceRef(map[string]any{"kind": "x", "controller": ""}) != nil {
		t.Fatal("empty member value accepted (secret-like)")
	}
	if convertEvidenceRef(map[string]any{"kind": "attempt-result", "sha256": "uses a password"}) != nil {
		t.Fatal("secret-like member accepted")
	}
	if pickEvidenceRef([]any{nil, good}) == nil {
		t.Fatal("null entries not skipped")
	}
	if formatEvidenceRef(good) != "kind=attempt-result; sha256="+strings64hex()+"; task_id="+testTaskID {
		t.Fatalf("format mismatch: %s", formatEvidenceRef(good))
	}
}

func strings64hex() string {
	const hex = "0123456789abcdef"
	out := make([]byte, 64)
	for i := range out {
		out[i] = hex[i%16]
	}
	return string(out)
}

func TestConvertScopeNormalization(t *testing.T) {
	scope := convertScope("implement", []string{`src\sub`, "lib/", "/tmp/x/", ""})
	text, err := canonicalText(scope)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"paths":["lib","src/sub","tmp/x"],"stage":"implement"}`
	if text != want {
		t.Fatalf("scope mismatch: %s want %s", text, want)
	}
}

func TestPathOverlap(t *testing.T) {
	if !pathOverlap("src", "src/sub") || !pathOverlap("src/sub", "src") {
		t.Fatal("ancestor overlap missed")
	}
	if !pathOverlap(".", "src") || !pathOverlap("", "src") {
		t.Fatal("root hints must overlap everything")
	}
	if pathOverlap("src", "lib") {
		t.Fatal("distinct trees overlap")
	}
	if !pathOverlap(`src\x`, "src/x") {
		t.Fatal("separator-insensitive equality missed")
	}
	if pathOverlap(`src\x`, "src/y") {
		t.Fatal("siblings overlap")
	}
}

func TestConvertItemRejections(t *testing.T) {
	valid := map[string]any{
		"scope":           []any{"src"},
		"observation":     "bounded worker observation",
		"action_type":     "recommended",
		"action":          "try again",
		"knowledge_class": "procedural",
		"risk_class":      "low",
	}
	if checked := convertItem(valid, "implement"); !checked.ok {
		t.Fatalf("valid item rejected: %s", checked.reason)
	}
	cases := []struct {
		mutate func(m map[string]any)
		reason string
	}{
		{func(m map[string]any) { delete(m, "action") }, "invalid-shape"},
		{func(m map[string]any) { m["extra"] = "1" }, "invalid-shape"},
		{func(m map[string]any) { m["scope"] = "src" }, "invalid-shape"},
		{func(m map[string]any) { m["scope"] = []any{} }, "invalid-shape"},
		{func(m map[string]any) {
			m["scope"] = toAnyArray(func() []string {
				s := make([]string, 17)
				for i := range s {
					s[i] = "p"
				}
				return s
			}())
		}, "scope-too-large"},
		{func(m map[string]any) { m["scope"] = []any{makeString(513)} }, "scope-path-too-long"},
		{func(m map[string]any) { m["scope"] = []any{"..", "evil"} }, "forbidden-scope-path"},
		{func(m map[string]any) { m["scope"] = []any{".bsl-flow/x"} }, "forbidden-scope-path"},
		{func(m map[string]any) { m["observation"] = makeString(513) }, "observation-too-long"},
		{func(m map[string]any) { m["observation"] = "uses a password" }, "secret-like-content"},
		{func(m map[string]any) { m["action"] = makeString(257) }, "action-too-long"},
		{func(m map[string]any) { m["action_type"] = "maybe" }, "invalid-action-type"},
		{func(m map[string]any) { m["knowledge_class"] = "authorization" }, "class-not-proposable"},
		{func(m map[string]any) { m["risk_class"] = "extreme" }, "invalid-risk-class"},
	}
	for _, test := range cases {
		item := map[string]any{}
		for key, value := range valid {
			item[key] = value
		}
		test.mutate(item)
		if checked := convertItem(item, "implement"); checked.ok || checked.reason != test.reason {
			t.Fatalf("rejection mismatch: got ok=%v reason=%q want %q", checked.ok, checked.reason, test.reason)
		}
	}
}

func makeString(length int) string {
	out := make([]byte, length)
	for i := range out {
		out[i] = 'x'
	}
	return string(out)
}

func TestTornTailDetection(t *testing.T) {
	project := t.TempDir()
	events := filepath.Join(project, ".bsl-flow", "memory", "events")
	if err := os.MkdirAll(events, 0o755); err != nil {
		t.Fatal(err)
	}
	// A visibly truncated file counts as torn, not invalid.
	if err := os.WriteFile(filepath.Join(events, "000001.json"), []byte(`{"schema_version":1,`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := jsonFailureKind(filepath.Join(events, "000001.json")); got != "torn" {
		t.Fatalf("truncated file classified %s", got)
	}
	if err := os.WriteFile(filepath.Join(events, "000001.json"), []byte(`nonsense`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := jsonFailureKind(filepath.Join(events, "000001.json")); got != "invalid" {
		t.Fatalf("scalar file classified %s", got)
	}
}

func TestWriteBFJSONRefusesOverwriteAndAtomicReplace(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "data.json")
	if err := writeBFJSON(path, map[string]any{"a": 1}, false); err != nil {
		t.Fatal(err)
	}
	if err := writeBFJSON(path, map[string]any{"a": 2}, false); err == nil || err.Error() != "BF_CONFLICT: Refusing to overwrite JSON file: "+path {
		t.Fatalf("overwrite not refused: %v", err)
	}
	if err := writeBFJSON(path, map[string]any{"a": 3}, true); err != nil {
		t.Fatal(err)
	}
	object, err := readBFJSON(path)
	if err != nil {
		t.Fatal(err)
	}
	if object["a"] != jsonNumberOf("3") {
		t.Fatalf("replace wrote wrong value: %v", object["a"])
	}
	// No temporary files remain after publication.
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "data.json" {
		t.Fatalf("temporary files leaked: %v", entries)
	}
}

func TestWriterLockReentry(t *testing.T) {
	directory := t.TempDir()
	lock, err := enterLock(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enterLock(directory); err == nil || err.Error() != "BF_CONFLICT: Writer lock is already held." {
		t.Fatalf("re-entry not detected: %v", err)
	}
	lock.release()
	second, err := enterLock(directory)
	if err != nil {
		t.Fatalf("lock not reusable after release: %v", err)
	}
	second.release()
}

func TestBoundedTextTrimsEnd(t *testing.T) {
	if got := boundedText("abcdef", 3); got != "abc" {
		t.Fatalf("bounded text mismatch: %q", got)
	}
	// Only over-limit text is trimmed; at-limit text passes through intact.
	if got := boundedText("abcdef   ", 6); got != "abcdef" {
		t.Fatalf("trailing whitespace not trimmed: %q", got)
	}
	if got := boundedText("abc   ", 6); got != "abc   " {
		t.Fatalf("at-limit text modified: %q", got)
	}
	if got := boundedText("abc", 10); got != "abc" {
		t.Fatalf("short text modified: %q", got)
	}
}
