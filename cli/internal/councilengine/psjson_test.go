package councilengine

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// psjson_test verifies the ConvertTo-Json replicator. When pwsh is available
// the round-trip cases run against the live engine (the W7 differential
// baseline); without pwsh the recorded expectations still execute.

func psjsonGolden(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath("pwsh"); err != nil {
		if runtime.GOOS != "windows" {
			return false
		}
		t.Fatalf("pwsh must be available for the psjson differential on Windows")
	}
	return true
}

func pwshConvertToJSON(t *testing.T, value any, depth int, compress bool) string {
	t.Helper()
	// Render the Go value to a JSON literal the PS harness can rebuild, then
	// let PowerShell materialize and re-serialize it: numbers stay literals
	// through ConvertFrom-Json -AsHashTable? No — ConvertFrom-Json widens
	// numbers. Instead the harness receives the already-rendered Go JSON and
	// only verifies formatting through the ordered key list.
	// Simplest reliable differential: compare against pwsh rendering of an
	// equivalent PS-native construction per case (see psjsonDiffCases).
	return ""
}

// TestPSJSONFormat verifies the replicator against recorded pwsh output.
func TestPSJSONFormat(t *testing.T) {
	cases := []struct {
		name     string
		value    any
		expected string
	}{
		{"empty object", newOrdered(), "{}"},
		{"empty array", []any{}, "[]"},
		{"null", nil, "null"},
		{"scalar string", "hi", "\"hi\""},
		{"integer", json.Number("42"), "42"},
		{"negative integer", json.Number("-7"), "-7"},
		{"big integer literal", json.Number("123456789012345678"), "123456789012345678"},
		{"double integral", 100.0, "100.0"},
		{"double zero", 0.0, "0.0"},
		{"double negative zero", mathNegZero(), "-0.0"},
		{"double fraction", 1.5, "1.5"},
		{"double pi", 3.141592653589793, "3.141592653589793"},
		{"double exponent high", 1e21, "1E+21"},
		{"double exponent low", 1e-7, "1E-07"},
		{"double fixed upper boundary", 1e16, "10000000000000000.0"},
		{"double fixed lower boundary", 9.9999e-5, "9.9999E-05"},
		{"double 1e-4 fixed", 1e-4, "0.0001"},
		{"double 1e-5 exponent", 1e-5, "1E-05"},
		{"double mantissa exponent", 1.234e-6, "1.234E-06"},
		{"double max", 1.7976931348623157e308, "1.7976931348623157E+308"},
		{"nested object", orderedFrom([]string{"a", "b"}, []any{json.Number("1"), orderedFrom([]string{"c"}, []any{json.Number("2")})}),
			"{\r\n  \"a\": 1,\r\n  \"b\": {\r\n    \"c\": 2\r\n  }\r\n}"},
		{"array of objects", []any{orderedFrom([]string{"x"}, []any{json.Number("1")}), orderedFrom([]string{"y"}, []any{[]any{json.Number("1"), json.Number("2")}})},
			"[\r\n  {\r\n    \"x\": 1\r\n  },\r\n  {\r\n    \"y\": [\r\n      1,\r\n      2\r\n    ]\r\n  }\r\n]"},
		{"string escapes", "line1\nline2\ttab \"q\" \\b", "\"line1\\nline2\\ttab \\\"q\\\" \\\\b\""},
		{"control char", "a\x01b", "\"a\\u0001b\""},
		{"raw unicode", "привет 中文 Ünïcödé 🎉", "\"привет 中文 Ünïcödé 🎉\""},
		{"html not escaped", "<a> & 'q' /s\\b", "\"<a> & 'q' /s\\\\b\""},
		{"booleans", true, "true"},
		{"nil member", orderedFrom([]string{"n"}, []any{nil}), "{\r\n  \"n\": null\r\n}"},
		{"nil map", map[string]any(nil), "null"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rendered, err := convertToJSON(testCase.value, 20)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if string(rendered) != testCase.expected {
				t.Fatalf("render mismatch:\n got %q\nwant %q", string(rendered), testCase.expected)
			}
		})
	}
}

func mathNegZero() float64 {
	negative, _ := strconv.ParseFloat("-0", 64)
	return negative
}

// TestPSJSONDepthIsAdvisory documents the PowerShell 7 behavior: depth
// overruns warn rather than fail, so the replicator accepts any nesting.
func TestPSJSONDepthIsAdvisory(t *testing.T) {
	leaf := orderedFrom([]string{"outer"}, []any{orderedFrom([]string{"a"}, []any{json.Number("1")})})
	if _, err := convertToJSON(leaf, 1); err != nil {
		t.Fatalf("depth overruns are advisory in PowerShell 7: %v", err)
	}
}

// TestPSJSONCompress verifies the -Compress projection.
func TestPSJSONCompress(t *testing.T) {
	value := orderedFrom(
		[]string{"schema_version", "texts", "nested"},
		[]any{json.Number("1"), []any{"a", "b"}, orderedFrom([]string{"k"}, []any{json.Number("2")})},
	)
	rendered, err := convertToJSONCompress(value, 10)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	expected := `{"schema_version":1,"texts":["a","b"],"nested":{"k":2}}`
	if string(rendered) != expected {
		t.Fatalf("compress mismatch:\n got %s\nwant %s", string(rendered), expected)
	}
}

// TestPSJSONWriteArtifactShape verifies the Write-BSLFlowJsonAtomic byte
// shape: pretty bytes plus exactly one trailing CRLF, UTF-8 no BOM.
func TestPSJSONWriteArtifactShape(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "artifact.json")
	value := orderedFrom([]string{"a"}, []any{json.Number("1")})
	if err := writeBSLFlowJSONAtomic(path, value); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	expected := "{\r\n  \"a\": 1\r\n}\r\n"
	if string(data) != expected {
		t.Fatalf("artifact bytes:\n got %q\nwant %q", string(data), expected)
	}
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("artifact must not carry a BOM")
	}
}

// TestPSJSONAgainstLivePwsh runs the full differential against real pwsh:
// the harness converts PowerShell-native values and compares byte for byte.
func TestPSJSONAgainstLivePwsh(t *testing.T) {
	if !psjsonGolden(t) {
		t.Skip("pwsh unavailable")
	}
	harness := `$ErrorActionPreference = 'Stop'
$cases = @(
  @{ name = 'binding'; value = [pscustomobject][ordered]@{ provider = 'p'; model = 'm'; effort = 'high'; endpoint = [pscustomobject][ordered]@{ scheme = 'https'; host = 'h'; port = 443; base_path = '/' }; input_hashes = [pscustomobject][ordered]@{ a = ('a' * 64); b = $null } } },
  @{ name = 'attempt'; value = [pscustomobject][ordered]@{ schema_version = 1; attempt_id = 'ab12'; sequence = 2; role = 'chair'; created_at_utc = '2026-09-14T00:00:00.0000000Z'; binding_sha256 = ('f' * 64) } },
  @{ name = 'numbers'; value = [pscustomobject][ordered]@{ flt = 0.0; f = 1.5; pi = [math]::PI; exp = 1e21; small = 0.0000001; big = [double]123456789012345678 } },
  @{ name = 'cyrillic'; value = [pscustomobject][ordered]@{ s = -join @([char]0x043F,[char]0x0440,[char]0x0438,[char]0x0432) } },
  @{ name = 'arrays'; value = [pscustomobject][ordered]@{ empty = @(); one = @('x'); nested = @([pscustomobject][ordered]@{ b = @(1, 2) }) } },
  @{ name = 'escapes'; value = [pscustomobject][ordered]@{ s = ('a' + [char]1 + 'b' + [char]10 + [char]9 + [char]34 + 'q' + [char]92 + 'z'); h = "<a> & 'q'" } }
)
foreach ($case in $cases) {
  $json = $case.value | ConvertTo-Json -Depth 20
  $bytes = [System.Text.Encoding]::UTF8.GetBytes($json)
  $hash = [System.Security.Cryptography.SHA256]::Create()
  try { $digest = ([BitConverter]::ToString($hash.ComputeHash($bytes))).Replace('-', '').ToLowerInvariant() } finally { $hash.Dispose() }
  $body64 = [Convert]::ToBase64String($bytes)
  Write-Output ("CASE=" + $case.name + "|HASH=" + $digest + "|BODY=" + $body64)
}
`
	script := filepath.Join(t.TempDir(), "harness.ps1")
	if err := os.WriteFile(script, []byte(harness), 0o644); err != nil {
		t.Fatalf("write harness: %v", err)
	}
	output, err := exec.Command("pwsh", "-NoProfile", "-File", script).Output()
	if err != nil {
		t.Fatalf("pwsh harness: %v\n%s", err, output)
	}
	expected := map[string]struct {
		digest string
		body   string
	}{}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(line, "CASE=") {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		name := strings.TrimPrefix(parts[0], "CASE=")
		digest := strings.TrimPrefix(parts[1], "HASH=")
		body64 := strings.TrimPrefix(parts[2], "BODY=")
		bodyBytes, err := base64.StdEncoding.DecodeString(body64)
		if err != nil {
			t.Fatalf("decode body for %s: %v", name, err)
		}
		expected[name] = struct {
			digest string
			body   string
		}{digest: digest, body: string(bodyBytes)}
	}
	goCases := map[string]any{
		"binding": orderedFrom([]string{"provider", "model", "effort", "endpoint", "input_hashes"}, []any{
			"p", "m", "high",
			orderedFrom([]string{"scheme", "host", "port", "base_path"}, []any{"https", "h", 443, "/"}),
			orderedFrom([]string{"a", "b"}, []any{strings.Repeat("a", 64), nil}),
		}),
		"attempt": orderedFrom([]string{"schema_version", "attempt_id", "sequence", "role", "created_at_utc", "binding_sha256"}, []any{
			json.Number("1"), "ab12", json.Number("2"), "chair", "2026-09-14T00:00:00.0000000Z", strings.Repeat("f", 64),
		}),
		"numbers": orderedFrom([]string{"flt", "f", "pi", "exp", "small", "big"}, []any{
			0.0, 1.5, 3.141592653589793, 1e21, 1e-7, float64(123456789012345678),
		}),
		"cyrillic": orderedFrom([]string{"s"}, []any{"прив"}),
		"arrays": orderedFrom([]string{"empty", "one", "nested"}, []any{
			[]any{}, []any{"x"}, []any{orderedFrom([]string{"b"}, []any{[]any{json.Number("1"), json.Number("2")}})},
		}),
		"escapes": orderedFrom([]string{"s", "h"}, []any{"a\x01b\n\t\"q\\z", "<a> & 'q'"}),
	}
	for name, value := range goCases {
		want, ok := expected[name]
		if !ok {
			t.Fatalf("pwsh produced no case %s", name)
		}
		rendered, err := convertToJSON(value, 20)
		if err != nil {
			t.Fatalf("%s: render: %v", name, err)
		}
		if string(rendered) != want.body {
			t.Fatalf("%s body mismatch:\n got %q\nwant %q", name, string(rendered), want.body)
		}
		sum := sha256.Sum256(rendered)
		if hex.EncodeToString(sum[:]) != want.digest {
			t.Fatalf("%s digest mismatch", name)
		}
	}
}

// TestPSJSONHashHelpers verifies hashPSJSON uses the pretty bytes.
func TestPSJSONHashHelpers(t *testing.T) {
	value := orderedFrom([]string{"a"}, []any{json.Number("1")})
	digest, err := hashPSJSON(value, 10)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	rendered, _ := convertToJSON(value, 10)
	sum := sha256.Sum256(rendered)
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatal("hashPSJSON must hash the pretty bytes")
	}
}

func TestOrderedSemantics(t *testing.T) {
	object := newOrdered()
	object.set("b", json.Number("1"))
	object.set("a", json.Number("2"))
	object.set("b", json.Number("3"))
	if strings.Join(object.keysOf(), ",") != "b,a" {
		t.Fatalf("insertion order not preserved: %v", object.keysOf())
	}
	if object.get("b") != json.Number("3") {
		t.Fatal("set must overwrite")
	}
	if _, ok := asOrdered(42); ok {
		t.Fatal("asOrdered must reject scalars")
	}
	if converted, ok := asOrdered(map[string]any{"z": json.Number("1"), "a": json.Number("2")}); !ok || strings.Join(converted.keysOf(), ",") != "a,z" {
		t.Fatal("asOrdered of a plain map sorts keys")
	}
}

func TestDecodeOrderedDocument(t *testing.T) {
	document := []byte(`{"b": 1, "a": {"c": [2, 3]}}`)
	value, err := decodeOrderedDocument(document)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	object, ok := value.(*ordered)
	if !ok {
		t.Fatalf("expected *ordered, got %T", value)
	}
	if strings.Join(object.keysOf(), ",") != "b,a" {
		t.Fatalf("decoded order must preserve file member order, got %v", object.keysOf())
	}
	rendered, err := convertToJSON(value, 10)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.Contains(rendered, []byte("\"c\": [\r\n      2,\r\n      3\r\n    ]")) {
		t.Fatalf("nested array render: %s", rendered)
	}
}

func TestRoundTripUTCTime(t *testing.T) {
	moment := time.Date(2026, 9, 14, 10, 11, 12, 123456700, time.UTC)
	rendered := roundTripUTCTime(moment)
	if !strings.HasSuffix(rendered, "Z") || !strings.Contains(rendered, ".") {
		t.Fatalf("round-trip time shape: %s", rendered)
	}
	if len(rendered) != len("2026-09-14T10:11:12.1234567Z") {
		t.Fatalf("round-trip time must carry 7 fractional digits: %s", rendered)
	}
}

func TestGuidN(t *testing.T) {
	guid, err := guidN()
	if err != nil {
		t.Fatalf("guid: %v", err)
	}
	if len(guid) != 32 {
		t.Fatalf("guid length: %d", len(guid))
	}
	if _, err := hex.DecodeString(guid); err != nil {
		t.Fatalf("guid hex: %v", err)
	}
	// version nibble 4 and variant bits 10
	if guid[12] != '4' {
		t.Fatalf("guid version nibble: %c", guid[12])
	}
	variant, _ := hex.DecodeString(guid[16:18])
	if variant[0]&0xC0 != 0x80 {
		t.Fatalf("guid variant bits: %02x", variant[0])
	}
}

func TestFormatPSDoubleAgainstPwsh(t *testing.T) {
	if !psjsonGolden(t) {
		t.Skip("pwsh unavailable")
	}
	values := []float64{0.0, 1.0, -1.0, 0.5, 1.5, 2.5, 100.0, 0.1, 3.141592653589793,
		1e15, 1e16, 1e17, 123456789012345678, -0.25, 1e-4, 1e-5, 1e-6, 1.234e-6,
		1e21, 1e100, 1.7976931348623157e308, 5e-324, 1.5e300, 123456.789, 9999999999999998,
		9.9999e-5, 0.000123456789012345,
	}
	var harness strings.Builder
	harness.WriteString("$ErrorActionPreference='Stop'\n$vs=@(")
	for index, value := range values {
		if index > 0 {
			harness.WriteString(",")
		}
		harness.WriteString(psDoubleLiteral(value))
	}
	harness.WriteString(")\nforeach($v in $vs){ Write-Output ($v | ConvertTo-Json -Compress) }")
	script := filepath.Join(t.TempDir(), "doubles.ps1")
	if err := os.WriteFile(script, []byte(harness.String()), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	output, err := exec.Command("pwsh", "-NoProfile", "-File", script).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("pwsh doubles: %v\nstderr: %s", err, exitErr.Stderr)
		}
		t.Fatalf("pwsh doubles: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != len(values) {
		t.Fatalf("pwsh returned %d lines for %d values", len(lines), len(values))
	}
	for index, value := range values {
		want := strings.TrimSpace(lines[index])
		got := formatPSDouble(value)
		if got != want {
			t.Fatalf("double %v: got %s want %s", value, got, want)
		}
	}
}

// psDoubleLiteral renders a Go double as a PowerShell literal that stays a
// double (never an int): integral values (including zero and plain exponent
// forms) gain an explicit ".0" fixed rendering, everything else uses the
// shortest form.
func psDoubleLiteral(value float64) string {
	if value == float64(int64(value)) && mathAbs(value) < 1e15 {
		return strconv.FormatFloat(value, 'f', 1, 64)
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func mathAbs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func TestJSONNumberValidation(t *testing.T) {
	for _, valid := range []string{"0", "-1", "1.5", "1e21", "-2.5E-3"} {
		if err := writePSJSONNumber(valid); err != nil {
			t.Fatalf("valid literal %s rejected: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "0x10", "1_000", "abc"} {
		if err := writePSJSONNumber(invalid); err == nil {
			t.Fatalf("invalid literal %s accepted", invalid)
		}
	}
}

func writePSNumber(text string) error {
	var buffer bytes.Buffer
	return writePSJSON(&buffer, json.Number(text), 0, 20)
}

func writePSJSONNumber(text string) error { return writePSNumber(text) }
