package release

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var releaseNow = time.Date(2026, 5, 4, 13, 45, 30, 500, time.UTC)

func fakeBuild(t Target, exePath string) error {
	return os.WriteFile(exePath, []byte("fake-bsl-flow-"+t.GOOS+"-"+t.GOARCH+"\x00"), 0o755)
}

func contentSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestDefaultTargetsMatchReleaseMatrix(t *testing.T) {
	want := []Target{
		{GOOS: "windows", GOARCH: "amd64"},
		{GOOS: "darwin", GOARCH: "arm64"},
		{GOOS: "darwin", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "amd64"},
	}
	if got := DefaultTargets(); !reflect.DeepEqual(got, want) {
		t.Fatalf("release matrix changed: %+v", got)
	}
}

func TestBuildAllDeterministicOutput(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.test/bsl-flow\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The output directory lives inside the source tree to prove that release
	// artifacts are excluded from the source revision identity.
	opts := Options{
		SourceDir:           source,
		OutDir:              filepath.Join(source, "dist"),
		Version:             "1.2.3-test.1",
		Now:                 releaseNow,
		Build:               fakeBuild,
		VerifyDeterministic: true,
	}
	first, err := BuildAll(opts)
	if err != nil {
		t.Fatal(err)
	}
	firstManifest, err := os.ReadFile(filepath.Join(opts.OutDir, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	firstArchives := map[string]string{}
	for _, entry := range first.Targets {
		data, err := os.ReadFile(filepath.Join(opts.OutDir, entry.Archive))
		if err != nil {
			t.Fatal(err)
		}
		got := contentSHA(data)
		if got != entry.ArchiveSHA256 {
			t.Fatalf("manifest archive hash mismatch for %s: %s != %s", entry.Archive, entry.ArchiveSHA256, got)
		}
		firstArchives[entry.Archive] = got
	}
	second, err := BuildAll(opts)
	if err != nil {
		t.Fatal(err)
	}
	secondManifest, err := os.ReadFile(filepath.Join(opts.OutDir, manifestName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstManifest, secondManifest) {
		t.Fatal("release manifest bytes changed between identical runs")
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("release manifest value changed between identical runs: %+v != %+v", first, second)
	}
	for _, entry := range second.Targets {
		data, err := os.ReadFile(filepath.Join(opts.OutDir, entry.Archive))
		if err != nil {
			t.Fatal(err)
		}
		if got := contentSHA(data); got != firstArchives[entry.Archive] {
			t.Fatalf("archive %s changed between identical runs: %s != %s", entry.Archive, firstArchives[entry.Archive], got)
		}
	}
	if len(first.Targets) != 4 || first.SchemaVersion != 1 || first.Version != "1.2.3-test.1" {
		t.Fatalf("unexpected manifest header: %+v", first)
	}
	matrix := DefaultTargets()
	for index, entry := range first.Targets {
		if entry.GOOS != matrix[index].GOOS || entry.GOARCH != matrix[index].GOARCH {
			t.Fatalf("target order changed: %+v", first.Targets)
		}
		if entry.Binary != BinaryName(matrix[index]) || entry.Archive != ArchiveName(matrix[index]) {
			t.Fatalf("artifact names diverged: %+v", entry)
		}
	}
	if len(first.GeneratedFrom) != 64 {
		t.Fatalf("source digest is not a sha256 hex string: %q", first.GeneratedFrom)
	}
	if !bytes.HasSuffix(firstManifest, []byte("\n")) || bytes.Contains(firstManifest, []byte("\r")) {
		t.Fatal("manifest must be LF terminated without CR")
	}
	if !bytes.HasPrefix(firstManifest, []byte(`{"schema_version":1,"version":"1.2.3-test.1","targets":[`)) {
		t.Fatalf("manifest is not canonical JSON: %s", firstManifest)
	}
}

func TestBuildAllZipContent(t *testing.T) {
	opts := Options{
		SourceDir: t.TempDir(),
		OutDir:    t.TempDir(),
		Version:   "2.0.0",
		// Non-UTC zone proves timestamps are normalized before storage.
		Now:   time.Date(2026, 7, 8, 9, 10, 30, 0, time.FixedZone("offset", 2*60*60)),
		Build: fakeBuild,
	}
	manifest, err := BuildAll(opts)
	if err != nil {
		t.Fatal(err)
	}
	wantTime := opts.Now.UTC().Truncate(time.Second)
	for _, entry := range manifest.Targets {
		path := filepath.Join(opts.OutDir, entry.Archive)
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		checkErr := func() error {
			info, err := f.Stat()
			if err != nil {
				return err
			}
			reader, err := zip.NewReader(f, info.Size())
			if err != nil {
				return err
			}
			if len(reader.File) != 1 {
				return fmt.Errorf("archive %s holds %d entries, want exactly the binary", entry.Archive, len(reader.File))
			}
			file := reader.File[0]
			if file.Name != entry.Binary || strings.Contains(file.Name, "/") {
				return fmt.Errorf("archive entry %q is not the binary at archive root", file.Name)
			}
			if file.Method != zip.Deflate {
				return fmt.Errorf("archive entry %q method %d, want Deflate", file.Name, file.Method)
			}
			if file.Modified.Unix() != wantTime.Unix() {
				return fmt.Errorf("archive entry %q mtime %s, want fixed %s", file.Name, file.Modified.UTC(), wantTime)
			}
			if file.Mode()&0o111 == 0 {
				return fmt.Errorf("archive entry %q lost the executable bits: %v", file.Name, file.Mode())
			}
			rc, err := file.Open()
			if err != nil {
				return err
			}
			data := make([]byte, file.UncompressedSize64)
			_, readErr := io.ReadFull(rc, data)
			closeErr := rc.Close()
			if readErr != nil {
				return readErr
			}
			if closeErr != nil {
				return closeErr
			}
			if got := contentSHA(data); got != entry.BinarySHA256 {
				return fmt.Errorf("archive entry %q content hash %s, want %s", file.Name, got, entry.BinarySHA256)
			}
			return nil
		}()
		closeErr := f.Close()
		if checkErr != nil {
			t.Fatal(checkErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}

func TestBuildAllRejectsInvalidOptions(t *testing.T) {
	valid := Options{
		SourceDir: t.TempDir(),
		OutDir:    t.TempDir(),
		Version:   "1.2.3",
		Build:     fakeBuild,
	}
	cases := []struct {
		name   string
		mutate func(*Options)
	}{
		{"missing source", func(o *Options) { o.SourceDir = "" }},
		{"nonexistent source", func(o *Options) { o.SourceDir = filepath.Join(o.OutDir, "absent") }},
		{"missing output", func(o *Options) { o.OutDir = "" }},
		{"empty version", func(o *Options) { o.Version = "" }},
		{"invalid version", func(o *Options) { o.Version = "1.2" }},
		{"empty targets", func(o *Options) { o.Targets = []Target{} }},
		{"incomplete target", func(o *Options) { o.Targets = []Target{{GOOS: "linux"}} }},
		{"duplicate targets", func(o *Options) {
			o.Targets = []Target{{GOOS: "linux", GOARCH: "amd64"}, {GOOS: "linux", GOARCH: "amd64"}}
		}},
	}
	for _, item := range cases {
		opts := valid
		item.mutate(&opts)
		if _, err := BuildAll(opts); err == nil {
			t.Fatalf("%s: invalid options accepted", item.name)
		}
	}
}

func TestBuildAllReportsBuildFailure(t *testing.T) {
	opts := Options{
		SourceDir: t.TempDir(),
		OutDir:    t.TempDir(),
		Version:   "1.2.3",
		Build:     func(t Target, exePath string) error { return errors.New("compiler exploded") },
	}
	if _, err := BuildAll(opts); err == nil || !strings.Contains(err.Error(), "build windows/amd64") || !strings.Contains(err.Error(), "compiler exploded") {
		t.Fatalf("build failure not reported with target context: %v", err)
	}
}

func TestVerifyEmbeddedInventory(t *testing.T) {
	files := map[string][]byte{
		"global/VERSION":         []byte("1.2.3\n"),
		"global/skills/amd64.md": []byte("release notes"),
	}
	resolver := func(actual map[string][]byte) func(string) ([]byte, error) {
		return func(rel string) ([]byte, error) {
			data, ok := actual[rel]
			if !ok {
				return nil, os.ErrNotExist
			}
			return data, nil
		}
	}
	tampered := map[string][]byte{}
	for name, data := range files {
		tampered[name] = append([]byte{}, data...)
	}
	tampered["global/skills/amd64.md"][0] ^= 0x20
	incomplete := map[string][]byte{
		"global/VERSION": files["global/VERSION"],
	}
	extra := map[string][]byte{}
	for name, data := range files {
		extra[name] = data
	}
	extra["global/legacy/runner.ps1"] = []byte("powershell")
	full := map[string]string{
		"global/VERSION":         contentSHA(files["global/VERSION"]),
		"global/skills/amd64.md": contentSHA(files["global/skills/amd64.md"]),
	}
	cases := []struct {
		name      string
		inventory map[string]string
		actual    map[string][]byte
		check     func(*testing.T, error)
	}{
		{"happy path", full, files, func(t *testing.T, err error) {
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"tampered entry", full, tampered, func(t *testing.T, err error) {
			var typed *TamperedInventoryEntryError
			if !errors.As(err, &typed) || typed.Path != "global/skills/amd64.md" {
				t.Fatalf("want tampered error for global/skills/amd64.md, got %v", err)
			}
		}},
		{"missing entry", full, incomplete, func(t *testing.T, err error) {
			var typed *MissingInventoryEntryError
			if !errors.As(err, &typed) || typed.Path != "global/skills/amd64.md" {
				t.Fatalf("want missing error for global/skills/amd64.md, got %v", err)
			}
		}},
		{"extra entry", map[string]string{
			"global/VERSION":           full["global/VERSION"],
			"global/skills/amd64.md":   full["global/skills/amd64.md"],
			"global/legacy/runner.ps1": "",
		}, extra, func(t *testing.T, err error) {
			var typed *ExtraInventoryEntryError
			if !errors.As(err, &typed) || typed.Path != "global/legacy/runner.ps1" {
				t.Fatalf("want extra error for global/legacy/runner.ps1, got %v", err)
			}
		}},
		{"forbidden absent entry", map[string]string{
			"global/VERSION":           full["global/VERSION"],
			"global/legacy/runner.ps1": "",
		}, files, func(t *testing.T, err error) {
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"invalid path", map[string]string{
			"../escape.txt": contentSHA([]byte("x")),
		}, files, func(t *testing.T, err error) {
			var typed *InvalidInventoryEntryError
			if !errors.As(err, &typed) {
				t.Fatalf("want invalid path error, got %v", err)
			}
		}},
		{"case duplicate", map[string]string{
			"global/VERSION": full["global/VERSION"],
			"global/version": full["global/VERSION"],
		}, files, func(t *testing.T, err error) {
			var typed *InvalidInventoryEntryError
			if !errors.As(err, &typed) {
				t.Fatalf("want case duplicate error, got %v", err)
			}
		}},
	}
	for _, item := range cases {
		err := VerifyEmbeddedInventory(item.inventory, resolver(item.actual))
		item.check(t, err)
	}
	if err := VerifyEmbeddedInventory(map[string]string{"global/VERSION": "x"}, nil); err == nil {
		t.Fatal("nil resolver accepted")
	}
}

func TestSourceDigestIgnoresOutputArtifacts(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.test/bsl-flow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(source, "dist")
	first, err := SourceDigest(source, out)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "noise.bin"), []byte("late artifact"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := SourceDigest(source, out)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("output artifact changed the source digest: %s != %s", first, second)
	}
	ignored, err := SourceDigest(source, out, filepath.Join(source, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if ignored == first {
		t.Fatal("explicit exclusions did not change the digest")
	}
}

func TestCanCrossCompileReportsToolchain(t *testing.T) {
	available, toolchain := CanCrossCompile()
	if toolchain == "" {
		t.Fatal("toolchain report must never be empty")
	}
	if available && !strings.Contains(toolchain, "go version") {
		t.Fatalf("unexpected toolchain identity: %q", toolchain)
	}
}
