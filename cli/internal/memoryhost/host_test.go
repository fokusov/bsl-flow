package memoryhost

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func runHelper(t *testing.T, input string, packageRoot string) (string, int) {
	t.Helper()
	var stdout bytes.Buffer
	code := Run(strings.NewReader(input), &stdout, packageRoot)
	return stdout.String(), code
}

func TestRunDecodeErrors(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", `{"available":false,"disabled_reason":"BF_INVALID: Native memory input is empty.","schema_version":1,"value":null}`},
		{"whitespace", "   ", `{"available":false,"disabled_reason":"BF_INVALID: Native memory input is empty.","schema_version":1,"value":null}`},
		{"invalid utf8", "\xff\xfe", `{"available":false,"disabled_reason":"BF_INVALID: Native memory input is not valid UTF-8.","schema_version":1,"value":null}`},
		{"two documents", `{"a":1}{"b":2}`, `{"available":false,"disabled_reason":"BF_INVALID: Native memory input is not one valid JSON document.","schema_version":1,"value":null}`},
		{"trailing junk", `{"a":1} x`, `{"available":false,"disabled_reason":"BF_INVALID: Native memory input is not one valid JSON document.","schema_version":1,"value":null}`},
		{"duplicate key", `{"a":1,"a":2}`, `{"available":false,"disabled_reason":"BF_INVALID: Native memory input is not one valid JSON document.","schema_version":1,"value":null}`},
		{"top-level array", `[1,2]`, `{"available":false,"disabled_reason":"BF_INVALID: Native memory input is not one valid JSON document.","schema_version":1,"value":null}`},
		{"scalar", `null`, `{"available":false,"disabled_reason":"BF_INVALID: Native memory input is not one valid JSON document.","schema_version":1,"value":null}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			output, code := runHelper(t, test.input, "")
			if code != 0 {
				t.Fatalf("exit code %d want 0", code)
			}
			if output != test.want {
				t.Fatalf("envelope mismatch:\n got %s\nwant %s", output, test.want)
			}
		})
	}
}

func TestRunBOMPrefixedRequest(t *testing.T) {
	project := t.TempDir()
	input := memoryInput(project, "projection", "")
	encoded, err := canonicalText(input)
	if err != nil {
		t.Fatal(err)
	}
	output, code := runHelper(t, "\ufeff"+encoded, "")
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if !strings.Contains(output, `"available":true`) || !strings.Contains(output, `"disabled_reason":""`) {
		t.Fatalf("BOM-prefixed request failed: %s", output)
	}
}

func TestRunUnsupportedOperationEnvelope(t *testing.T) {
	project := t.TempDir()
	input := memoryInput(project, "nope", "")
	encoded, err := canonicalText(input)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"available":false,"disabled_reason":"BF_INVALID: Unsupported native memory operation.","schema_version":1,"value":null}`
	output, code := runHelper(t, encoded, "")
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if output != want {
		t.Fatalf("envelope mismatch:\n got %s\nwant %s", output, want)
	}
}

func TestErrorTextTruncation(t *testing.T) {
	if got := errorText(nil); got != "BF_BLOCKED: native memory helper failed." {
		t.Fatalf("empty fallback mismatch: %s", got)
	}
	if got := errorText(errString("   ")); got != "BF_BLOCKED: native memory helper failed." {
		t.Fatalf("whitespace fallback mismatch: %s", got)
	}
	long := errString(strings.Repeat("x", 300))
	if got := errorText(long); len(got) != 256 {
		t.Fatalf("truncated length %d want 256", len(got))
	}
	// UTF-16 bound: a cut landing exactly on a pair boundary keeps the
	// complete pair (PowerShell's 256-code-unit Substring does the same).
	paired := errString(strings.Repeat("a", 254) + "\U0001F600\U0001F600")
	got := errorText(paired)
	if got != strings.Repeat("a", 254)+"\U0001F600" {
		t.Fatalf("surrogate cut mismatch: %q", got)
	}
	if utf16Length(got) != 256 {
		t.Fatalf("cut length mismatch: %d", utf16Length(got))
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestWriteEnvelopeOutputLimit(t *testing.T) {
	huge := newEnvelope(true, strings.Repeat("x", 2<<20), "")
	var stdout bytes.Buffer
	writeEnvelope(&stdout, huge)
	want := `{"available":false,"disabled_reason":"BF_BLOCKED: native memory output exceeds the 1 MiB limit.","schema_version":1,"value":null}`
	if stdout.String() != want {
		t.Fatalf("limited envelope mismatch: %s", stdout.String())
	}
	// A bounded envelope below the limit passes through untouched.
	small := newEnvelope(true, "ok", "")
	stdout.Reset()
	writeEnvelope(&stdout, small)
	smallBytes, err := canonicalBytes(small)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stdout.Bytes(), smallBytes) {
		t.Fatalf("bounded envelope was replaced: %s", stdout.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errString("closed") }

func TestWriteEnvelopeFallback(t *testing.T) {
	// A canonical failure (unpaired-surrogate-safe strings cannot occur from
	// decoded JSON, so force it through an invalid UTF-8 value) must still
	// emit the constant fallback envelope.
	envelope := newEnvelope(true, "bad\xff\xfe", "")
	var stdout bytes.Buffer
	writeEnvelope(&stdout, envelope)
	const fallback = `{"available":false,"disabled_reason":"BF_BLOCKED: native memory output failed.","schema_version":1,"value":null}`
	if stdout.String() != fallback {
		t.Fatalf("fallback mismatch: %s", stdout.String())
	}
	// A failing writer must not panic; the fallback write is best effort.
	writeEnvelope(failingWriter{}, newEnvelope(true, nil, ""))
}

func TestReadInputBytesLimit(t *testing.T) {
	chunk := bytes.Repeat([]byte("x"), 65536)
	reader := &chunkReader{chunks: 300, chunk: chunk} // ~19.6 MiB
	_, err := readInputBytes(reader)
	if err == nil || err.Error() != "BF_INVALID: Native memory input exceeds the 16 MiB limit." {
		t.Fatalf("input limit mismatch: %v", err)
	}
	exact := &chunkReader{chunks: 256, chunk: chunk}
	data, err := readInputBytes(exact)
	if err != nil {
		t.Fatalf("exactly 16 MiB rejected: %v", err)
	}
	if len(data) != nativeMemoryInputLimit {
		t.Fatalf("read %d bytes want %d", len(data), nativeMemoryInputLimit)
	}
}

type chunkReader struct {
	chunks int
	chunk  []byte
}

func (r *chunkReader) Read(buffer []byte) (int, error) {
	if r.chunks == 0 {
		return 0, io.EOF
	}
	r.chunks--
	copy(buffer, r.chunk)
	return len(r.chunk), nil
}

func TestRunMemoryHelperUsesPackageRootVariable(t *testing.T) {
	original := PackageRoot
	defer func() { PackageRoot = original }()
	root := t.TempDir()
	PackageRoot = root
	var stdout bytes.Buffer
	code := RunMemoryHelper(strings.NewReader(""), &stdout)
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if !strings.Contains(stdout.String(), "Native memory input is empty.") {
		t.Fatalf("unexpected envelope: %s", stdout.String())
	}
	if PackageRoot != root {
		t.Fatal("package root mutated")
	}
}

func TestRunBindEnvelopesThroughTempProject(t *testing.T) {
	project := t.TempDir()
	input := memoryInput(project, "bind", "implement")
	encoded, err := canonicalText(input)
	if err != nil {
		t.Fatal(err)
	}
	output, code := runHelper(t, encoded, "")
	if code != 0 {
		t.Fatalf("exit code %d", code)
	}
	wantPrefix := `{"available":true,"disabled_reason":"","schema_version":1,"value":{"available":true,`
	if !strings.HasPrefix(output, wantPrefix) {
		t.Fatalf("bind envelope mismatch: %s", output)
	}
	if !strings.HasSuffix(output, `,"disabled_reason":null,"excluded":[],"records":[],"schema_version":1}}`) {
		t.Fatalf("bind envelope tail mismatch: %s", output)
	}
	// The empty store never creates the directory: the write path is only
	// entered when there is something to append or persist.
	if _, err := statFile(filepath.Join(project, ".bsl-flow", "memory", "index.json")); err == nil {
		t.Fatal("empty bind persisted an index")
	}
}
