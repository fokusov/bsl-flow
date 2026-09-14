package repository

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// --- target identity ---

func TestNative1CTargetIdentityRejectsNonAbsolute(t *testing.T) {
	_, err := Native1CTargetIdentity("relative/path")
	if err == nil || !strings.Contains(err.Error(), "native 1C target must be an absolute FILE directory.") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNative1CTargetIdentityRequiresMarker(t *testing.T) {
	target := t.TempDir()
	_, err := Native1CTargetIdentity(target)
	if runtime.GOOS == "windows" {
		if err == nil || !strings.Contains(err.Error(), "FILE target marker is required to resolve physical target identity.") {
			t.Fatalf("unexpected error: %v", err)
		}
	} else {
		if err == nil || !strings.Contains(err.Error(), "BLOCKED_UNSUPPORTED_PLATFORM") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestNative1CTargetIdentityAndKeyWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only physical identity resolution")
	}
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "1Cv8.1CD"), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	identity, err := Native1CTargetIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(strings.TrimRight(identity, `\/`), strings.TrimRight(target, `\/`)) {
		t.Fatalf("identity = %q, want %q", identity, target)
	}
	key, err := Native1CTargetKey(target)
	if err != nil {
		t.Fatal(err)
	}
	want := hashUTF8(strings.ToLower(identity))
	if key != want {
		t.Fatalf("key = %s, want %s", key, want)
	}
}

func TestNative1CPlatformBlocker(t *testing.T) {
	if err := Native1CPlatformBlocker("windows"); err != nil {
		t.Fatalf("windows blocker: %v", err)
	}
	for _, goos := range []string{"linux", "darwin"} {
		err := Native1CPlatformBlocker(goos)
		if err == nil || !strings.Contains(err.Error(), "BLOCKED_UNSUPPORTED_PLATFORM") {
			t.Fatalf("unexpected blocker for %s: %v", goos, err)
		}
	}
}

// --- criterion shape ---

func TestNative1CCriterionShapeDiagnostics(t *testing.T) {
	valid := func() map[string]any {
		target := t.TempDir()
		if err := os.WriteFile(filepath.Join(target, "1Cv8.1CD"), []byte("db"), 0o644); err != nil {
			t.Fatal(err)
		}
		return map[string]any{
			"id": "native", "kind": "integration", "observation": "requires 1C",
			"executable":      filepath.Join(t.TempDir(), "1cv8.exe"),
			"arguments":       []any{},
			"protected_paths": []any{"tests"},
			"target":          target,
			"expected_tests":  []any{"Tests.Module.Test"},
			"native_1c": map[string]any{
				"source_root": "src", "extension": "Ext", "module": "Tests",
				"platform_version": "8.3.25.1445",
				"executable_sha256": strings.Repeat("a", 64),
				"authorized_operations": []any{"inventory", "load", "update", "test"},
				"authorization_reference": "operator",
			},
		}
	}
	cases := []struct {
		name           string
		mutate         func(map[string]any)
		message        string
		requiresTarget bool
	}{
		{"missing contract", func(c map[string]any) { delete(c, "native_1c") }, "native_1c contract is required.", false},
		{"null contract", func(c map[string]any) { c["native_1c"] = nil }, "native_1c contract is required.", false},
		{"wrong kind", func(c map[string]any) { c["kind"] = "unit" }, "native_1c is supported only for integration criteria.", false},
		{"missing source_root", func(c map[string]any) { delete(c["native_1c"].(map[string]any), "source_root") }, "criterion.native_1c.source_root is required.", false},
		{"unsafe extension", func(c map[string]any) { c["native_1c"].(map[string]any)["extension"] = "bad-name!" }, "unsafe native 1C extension or module name.", false},
		{"bad version", func(c map[string]any) { c["native_1c"].(map[string]any)["platform_version"] = "8.3" }, "invalid native 1C platform version.", false},
		{"bad sha", func(c map[string]any) { c["native_1c"].(map[string]any)["executable_sha256"] = "nope" }, "native executable SHA-256 must be lowercase hexadecimal.", false},
		{"bad executable", func(c map[string]any) { c["executable"] = "relative" }, "native executable must be an absolute 1cv8.exe path.", false},
		{"bad executable name", func(c map[string]any) { c["executable"] = filepath.Join(t.TempDir(), "1cv8s.exe") }, "native executable must be an absolute 1cv8.exe path.", false},
		{"free arguments", func(c map[string]any) { c["arguments"] = []any{"/F"} }, "native 1C criteria do not accept free arguments.", false},
		{"missing protected", func(c map[string]any) { c["protected_paths"] = []any{} }, "native 1C criteria require protected_paths for declared tests and fixtures.", false},
		{"relative target", func(c map[string]any) { c["target"] = "relative" }, "native 1C target must be an absolute FILE directory.", false},
		{"bad reuse", func(c map[string]any) {
			c["native_1c"].(map[string]any)["reuse_load_attempt"] = "not-a-uuid"
		}, "identity must be a canonical lower-case UUID.", true},
		{"operations mismatch", func(c map[string]any) {
			c["native_1c"].(map[string]any)["authorized_operations"] = []any{"inventory", "test"}
		}, "authorized_operations must be exactly inventory,load,update,test.", true},
		{"missing tests", func(c map[string]any) { c["expected_tests"] = []any{} }, "unique class-qualified expected tests are required.", true},
		{"bad test id", func(c map[string]any) { c["expected_tests"] = []any{"justonename"} }, "expected native test IDs must be classname.name.", true},
		{"duplicate tests", func(c map[string]any) { c["expected_tests"] = []any{"A.B", "a.b"} }, "unique class-qualified expected tests are required.", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.requiresTarget && runtime.GOOS != "windows" {
				t.Skip("physical target resolution is windows-only")
			}
			criterion := valid()
			tc.mutate(criterion)
			err := ValidateNative1CCriterionShape(criterion)
			if err == nil {
				t.Fatal("criterion was accepted")
			}
			if !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.message)
			}
		})
	}
}

// --- source snapshot ---

func writeNativeFixtureTree(t *testing.T, root string) {
	t.Helper()
	config := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses">
	<Configuration uuid="A1B2C3D4-0000-0000-0000-000000000001">
		<Properties>
			<Name>Ext</Name>
			<Version>1.2.3</Version>
		</Properties>
	</Configuration>
</MetaDataObject>`
	module := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject xmlns="http://v8.1c.ru/8.3/MDClasses">
	<Catalog uuid="A1B2C3D4-0000-0000-0000-000000000002">
		<Properties/>
	</Catalog>
</MetaDataObject>`
	configDump := `<?xml version="1.0" encoding="UTF-8"?>
<ConfigDumpInfo format="Hierarchical" version="2.9">
	<Metadata name="Ext" id="A1B2C3D4-0000-0000-0000-000000000001" xmlns="http://v8.1c.ru/8.3/config/dump"/>
</ConfigDumpInfo>`
	if err := os.WriteFile(filepath.Join(root, "Configuration.xml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Catalogs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Catalogs", "Goods.xml"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ConfigDumpInfo.xml"), []byte(configDump), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNative1CSourceSnapshot(t *testing.T) {
	root := t.TempDir()
	if _, err := Native1CSourceSnapshot(root); err == nil || !strings.Contains(err.Error(), "native source snapshot would be empty.") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "Configuration.xml"), []byte("<root/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Native1CSourceSnapshot(root); err == nil || !strings.Contains(err.Error(), "extension Configuration node is missing.") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "Configuration.xml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Native1CSourceSnapshot(root); err == nil || !strings.Contains(err.Error(), "Configuration.xml is missing.") {
		t.Fatalf("unexpected error: %v", err)
	}
	root = t.TempDir()
	writeNativeFixtureTree(t, root)
	snapshot, err := Native1CSourceSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot["extension"] != "Ext" || snapshot["version"] != "1.2.3" ||
		snapshot["uuid"] != "a1b2c3d4-0000-0000-0000-000000000001" {
		t.Fatalf("identity = %v", snapshot)
	}
	files, _ := nativeItems(snapshot["files"])
	if len(files) != 3 {
		t.Fatalf("files = %v", files)
	}
	first := asMap(files[0])
	if first["path"] != "ConfigDumpInfo.xml" {
		t.Fatalf("first row = %v", first)
	}
	paths := map[string]bool{}
	for _, raw := range files {
		paths[asStringOr(asMap(raw)["path"])] = true
	}
	for _, path := range []string{"Configuration.xml", "ConfigDumpInfo.xml", "Catalogs/Goods.xml"} {
		if !paths[path] {
			t.Fatalf("missing path %s in %v", path, files)
		}
	}
	wantHash, err := Hash(files)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot["sha256"] != wantHash {
		t.Fatalf("sha256 = %v", snapshot["sha256"])
	}
	// Snapshot copy must re-bind byte-identically.
	copy, err := Native1CSnapshotCopy(snapshot, filepath.Join(t.TempDir(), "copy"))
	if err != nil {
		t.Fatal(err)
	}
	if copy["sha256"] != snapshot["sha256"] {
		t.Fatalf("copy hash differs")
	}
}

func TestNative1CSourceSnapshotRejectsOwnedIdentityViolations(t *testing.T) {
	root := t.TempDir()
	writeNativeFixtureTree(t, root)
	// Zero configuration UUID.
	config := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject><Configuration uuid="00000000-0000-0000-0000-000000000000"><Properties><Name>Ext</Name><Version>1.0</Version></Properties></Configuration></MetaDataObject>`
	if err := os.WriteFile(filepath.Join(root, "Configuration.xml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Native1CSourceSnapshot(root); err == nil || !strings.Contains(err.Error(), "extension UUID is missing or zero.") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Duplicate owned UUID across metadata files.
	root = t.TempDir()
	writeNativeFixtureTree(t, root)
	duplicate := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject><Document uuid="A1B2C3D4-0000-0000-0000-000000000002"><Properties/></Document></MetaDataObject>`
	if err := os.WriteFile(filepath.Join(root, "Catalogs", "Extra.xml"), []byte(duplicate), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Native1CSourceSnapshot(root); err == nil || !strings.Contains(err.Error(), "owned metadata UUIDs must be unique and free of scaffold placeholders.") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Zero ExtendedConfigurationObject reference.
	root = t.TempDir()
	writeNativeFixtureTree(t, root)
	zero := `<?xml version="1.0" encoding="UTF-8"?>
<MetaDataObject><Catalog uuid="A1B2C3D4-0000-0000-0000-000000000002"><ExtendedConfigurationObject>00000000-0000-0000-0000-000000000000</ExtendedConfigurationObject></Catalog></MetaDataObject>`
	if err := os.WriteFile(filepath.Join(root, "Catalogs", "Goods.xml"), []byte(zero), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Native1CSourceSnapshot(root); err == nil || !strings.Contains(err.Error(), "ExtendedConfigurationObject UUID is zero.") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- JUnit ---

func writeJUnit(t *testing.T, path, content string, mod time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestNative1CJUnit(t *testing.T) {
	now := time.Now().UTC()
	report := `<?xml version="1.0" encoding="UTF-8"?>
<testsuite tests="1" failures="0" errors="0" skipped="0" disabled="0">
	<testcase classname="Tests.Module" name="Test"/>
</testsuite>`
	path := filepath.Join(t.TempDir(), "original.junit.xml")
	writeJUnit(t, path, report, now)
	parsed, err := Native1CJUnit(path, []string{"Tests.Module.Test"}, now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if parsed["outcome"] != "PASS" {
		t.Fatalf("outcome = %v", parsed["outcome"])
	}
	tests, _ := nativeItems(parsed["tests"])
	if len(tests) != 1 || asStringOr(tests[0]) != "Tests.Module.Test" {
		t.Fatalf("tests = %v", tests)
	}
	if parsed["sha256"] != fileSHA256(mustRead(t, path)) {
		t.Fatalf("sha256 mismatch")
	}
	cases := []struct {
		name    string
		content string
		message string
		kind    string
	}{
		{"empty", "", "original native JUnit report missing or empty.", "BF_BLOCKED"},
		{"bad root", `<root/>`, "unsupported native JUnit root.", "BF_BLOCKED"},
		{"selection", `<testsuite tests="1"><testcase classname="Other" name="Case"/></testsuite>`, "native JUnit selection differs from exact expected tests.", "BF_BLOCKED"},
		{"skipped", `<testsuite tests="1"><testcase classname="Tests.Module" name="Test"><skipped/></testcase></testsuite>`, "required native tests were skipped.", "BF_BLOCKED"},
		{"failed", `<testsuite tests="1"><testcase classname="Tests.Module" name="Test"><failure message="x"/></testcase></testsuite>`, "required native integration tests failed.", "BF_FAIL"},
		{"aggregate format", `<testsuite tests="x"><testcase classname="Tests.Module" name="Test"/></testsuite>`, "invalid native JUnit aggregate.", "BF_BLOCKED"},
		{"aggregate mismatch", `<testsuite tests="2"><testcase classname="Tests.Module" name="Test"/></testsuite>`, "inconsistent native JUnit aggregate.", "BF_BLOCKED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			casePath := filepath.Join(t.TempDir(), "report.xml")
			writeJUnit(t, casePath, tc.content, now)
			_, err := Native1CJUnit(casePath, []string{"Tests.Module.Test"}, now.Add(-time.Minute), now.Add(time.Minute))
			if err == nil {
				t.Fatal("report was accepted")
			}
			kind := ""
			if typed, ok := err.(*KindError); ok {
				kind = typed.Kind
			}
			if kind != tc.kind || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("error = %v (%s), want kind %s with %q", err, kind, tc.kind, tc.message)
			}
		})
	}
	// Freshness window.
	stalePath := filepath.Join(t.TempDir(), "stale.xml")
	writeJUnit(t, stalePath, report, now.Add(-time.Hour))
	if _, err := Native1CJUnit(stalePath, []string{"Tests.Module.Test"}, now.Add(-time.Minute), now.Add(time.Minute)); err == nil ||
		!strings.Contains(err.Error(), "native JUnit is not fresh for this attempt.") {
		t.Fatalf("unexpected freshness error: %v", err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// --- PE version ---

func TestNative1CPEFileVersion(t *testing.T) {
	blob := make([]byte, 64)
	binary.LittleEndian.PutUint32(blob[0:4], 0xFEEF04BD)
	binary.LittleEndian.PutUint32(blob[4:8], 0x00080003)
	binary.LittleEndian.PutUint32(blob[8:12], 0x00190005)
	pe := buildMinimalPE(t, blob)
	version, err := native1CPEFileVersion(pe)
	if err != nil {
		t.Fatal(err)
	}
	if version != "8.3.25.5" {
		t.Fatalf("version = %q", version)
	}
	// A PE without a version resource fails closed.
	empty := buildMinimalPE(t, nil)
	if _, err := native1CPEFileVersion(empty); err == nil {
		t.Fatal("versionless PE was accepted")
	}
}

// buildMinimalPE assembles a tiny PE32 with one .rsrc section holding the
// given bytes, so the version extraction can be exercised without a real
// Windows executable.
func buildMinimalPE(t *testing.T, rsrc []byte) []byte {
	t.Helper()
	raw := make([]byte, 0x400)
	copy(raw[0:2], "MZ")
	binary.LittleEndian.PutUint32(raw[0x3C:0x40], 0x80)
	header := raw[0x80:]
	copy(header[0:4], "PE\x00\x00")
	binary.LittleEndian.PutUint16(header[4:6], 0x014C)   // Machine: i386
	binary.LittleEndian.PutUint16(header[6:8], 1)        // NumberOfSections
	binary.LittleEndian.PutUint32(header[8:12], 0)       // TimeDateStamp
	binary.LittleEndian.PutUint16(header[20:22], 0xE0)   // SizeOfOptionalHeader
	binary.LittleEndian.PutUint16(header[22:24], 0x0102) // Characteristics: EXECUTABLE_IMAGE | 32BIT
	optional := header[24:]
	binary.LittleEndian.PutUint16(optional[0:2], 0x010B)  // Magic: PE32
	binary.LittleEndian.PutUint32(optional[32:36], 0x200) // SizeOfHeaders
	binary.LittleEndian.PutUint32(optional[36:40], 0x200) // FileAlignment
	binary.LittleEndian.PutUint32(optional[40:44], 0x1000) // SectionAlignment
	binary.LittleEndian.PutUint32(optional[92:96], 16)    // NumberOfRvaAndSizes
	section := header[24+0xE0:]
	copy(section[0:8], ".rsrc\x00\x00\x00")
	binary.LittleEndian.PutUint32(section[8:12], 0x200)   // VirtualSize
	binary.LittleEndian.PutUint32(section[12:16], 0x1000) // VirtualAddress
	binary.LittleEndian.PutUint32(section[16:20], 0x200)  // SizeOfRawData
	binary.LittleEndian.PutUint32(section[20:24], 0x200)  // PointerToRawData
	binary.LittleEndian.PutUint32(section[36:40], 0x40000040)
	if rsrc != nil {
		copy(raw[0x200:], rsrc)
	}
	return raw
}

// --- inventory transition ---

func observed(value any) map[string]any {
	return map[string]any{"status": "observed", "value": value}
}

func inventoryFixture(extensionName string, active bool, uuid string) map[string]any {
	return map[string]any{
		"target":   "T",
		"platform": map[string]any{"executable": "E"},
		"extensions": []any{
			map[string]any{"properties": map[string]any{
				"name": extensionName, "version": "1.2.3", "active": active,
				"purpose": "Основная", "scope": "Configuration",
				"uuid": uuid, "hash_sum": "h",
			}},
			map[string]any{"properties": map[string]any{
				"name": "Other", "version": "9.9", "active": true,
				"purpose": "Основная", "scope": "Configuration",
				"uuid": "b1b2c3d4-0000-0000-0000-000000000001", "hash_sum": "h2",
			}},
		},
	}
}

func TestNative1CInventoryTransition(t *testing.T) {
	source := map[string]any{"extension": "Ext", "version": "1.2.3"}
	before := inventoryFixture("Ext", false, "a1b2c3d4-0000-0000-0000-000000000001")
	after := inventoryFixture("Ext", true, "a1b2c3d4-0000-0000-0000-000000000001")
	if err := Native1CInventoryTransition(before, after, source); err != nil {
		t.Fatalf("transition rejected: %v", err)
	}
	// Changed other extension.
	changedOther := inventoryFixture("Ext", true, "a1b2c3d4-0000-0000-0000-000000000001")
	changedOther["extensions"].([]any)[1] = map[string]any{"properties": map[string]any{
		"name": "Other", "version": "10.0", "active": true,
		"purpose": "Основная", "scope": "Configuration",
		"uuid": "b1b2c3d4-0000-0000-0000-000000000001", "hash_sum": "h2",
	}}
	if err := Native1CInventoryTransition(before, changedOther, source); err == nil ||
		!strings.Contains(err.Error(), "a nonselected extension changed during native verification.") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Version mismatch.
	wrongVersion := inventoryFixture("Ext", true, "a1b2c3d4-0000-0000-0000-000000000001")
	wrongVersion["extensions"].([]any)[0].(map[string]any)["properties"].(map[string]any)["version"] = "9.9"
	if err := Native1CInventoryTransition(before, wrongVersion, source); err == nil ||
		!strings.Contains(err.Error(), "installed extension version or active state differs from the authorized source XML.") {
		t.Fatalf("unexpected error: %v", err)
	}
	// UUID change.
	wrongUUID := inventoryFixture("Ext", true, "c1c2c3d4-0000-0000-0000-000000000001")
	if err := Native1CInventoryTransition(before, wrongUUID, source); err == nil ||
		!strings.Contains(err.Error(), "installed extension instance UUID changed unexpectedly.") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Unobserved property.
	unobserved := inventoryFixture("Ext", true, "a1b2c3d4-0000-0000-0000-000000000001")
	unobserved["extensions"].([]any)[0].(map[string]any)["properties"].(map[string]any)["version"] = map[string]any{"status": "unobserved"}
	if _, err := Native1CNormalizeInventoryRow(unobserved["extensions"].([]any)[0].(map[string]any)); err == nil ||
		!strings.Contains(err.Error(), "inventory property version is unobserved.") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Missing property.
	missing := inventoryFixture("Ext", true, "a1b2c3d4-0000-0000-0000-000000000001")
	delete(missing["extensions"].([]any)[0].(map[string]any)["properties"].(map[string]any), "hash_sum")
	if _, err := Native1CNormalizeInventoryRow(missing["extensions"].([]any)[0].(map[string]any)); err == nil ||
		!strings.Contains(err.Error(), "inventory property hash_sum is missing.") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNative1CInventoryRowObservedWrapper(t *testing.T) {
	item := map[string]any{"properties": map[string]any{
		"name": observed("Ext"), "version": observed("1.0"), "active": observed(true),
		"purpose": observed("Основная"), "scope": observed("Configuration"),
		"uuid": observed("a1b2c3d4-0000-0000-0000-000000000001"), "hash_sum": observed("h"),
	}}
	row, err := Native1CNormalizeInventoryRow(item)
	if err != nil {
		t.Fatal(err)
	}
	if row["name"] != "Ext" || row["active"] != true {
		t.Fatalf("row = %v", row)
	}
}
