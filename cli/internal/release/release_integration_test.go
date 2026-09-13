package release

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestBuildAllCrossCompiles builds the real CLI with the local go tool for
// every release target. It is opt-in because it compiles four full binaries;
// set BFN_RELEASE_BUILD=1 to run it.
func TestBuildAllCrossCompiles(t *testing.T) {
	if os.Getenv("BFN_RELEASE_BUILD") != "1" {
		t.Skip("set BFN_RELEASE_BUILD=1 to cross-compile the release matrix")
	}
	available, toolchain := CanCrossCompile()
	if !available {
		t.Skipf("cannot cross-compile: %s", toolchain)
	}
	t.Logf("toolchain: %s", toolchain)
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the cli module directory")
	}
	cliRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	opts := Options{
		SourceDir:           cliRoot,
		OutDir:              t.TempDir(),
		Version:             "0.0.0-release-test",
		TrimPath:            true,
		Now:                 time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		VerifyDeterministic: true,
	}
	manifest, err := BuildAll(opts)
	if err != nil {
		t.Fatalf("BuildAll: %v", err)
	}
	matrix := DefaultTargets()
	if len(manifest.Targets) != len(matrix) {
		t.Fatalf("built %d targets, want %d", len(manifest.Targets), len(matrix))
	}
	for index, entry := range manifest.Targets {
		want := matrix[index]
		if entry.GOOS != want.GOOS || entry.GOARCH != want.GOARCH {
			t.Fatalf("target %d is %s/%s, want %s/%s", index, entry.GOOS, entry.GOARCH, want.GOOS, want.GOARCH)
		}
		binary, err := os.Stat(filepath.Join(opts.OutDir, entry.Binary))
		if err != nil {
			t.Fatalf("%s/%s: binary missing: %v", entry.GOOS, entry.GOARCH, err)
		}
		archive, err := os.Stat(filepath.Join(opts.OutDir, entry.Archive))
		if err != nil {
			t.Fatalf("%s/%s: archive missing: %v", entry.GOOS, entry.GOARCH, err)
		}
		if binary.Size() == 0 || archive.Size() == 0 {
			t.Fatalf("%s/%s: empty artifact", entry.GOOS, entry.GOARCH)
		}
		t.Logf("%s/%s ok: binary %s (%d bytes, sha256 %s), archive %s (%d bytes, sha256 %s)",
			entry.GOOS, entry.GOARCH, entry.Binary, binary.Size(), entry.BinarySHA256, entry.Archive, archive.Size(), entry.ArchiveSHA256)
	}
	if _, err := os.Stat(filepath.Join(opts.OutDir, manifestName)); err != nil {
		t.Fatalf("release manifest missing: %v", err)
	}
	if len(manifest.GeneratedFrom) != 64 {
		t.Fatalf("source digest is not a sha256 hex string: %q", manifest.GeneratedFrom)
	}
}
