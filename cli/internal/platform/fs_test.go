package platform

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAtomicWriteFileReplacesContent(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "state.json")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewOSFS().AtomicWriteFile(target, []byte(`{"schema_version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"schema_version":2}` {
		t.Fatalf("content = %q", data)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary files leaked: %v", entries)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("published target is not a regular file: %v", info.Mode())
	}
}

func TestAtomicWriteFileFailsOnMissingParent(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "missing", "state.json")
	if err := NewOSFS().AtomicWriteFile(missing, []byte("x"), 0o600); err == nil {
		t.Fatal("write into a missing directory accepted")
	}
	if _, err := os.Stat(filepath.Join(base, "missing")); err == nil {
		t.Fatal("missing parent directory was created")
	}
}

func acquireLock(fs FS, path string) <-chan error {
	result := make(chan error, 1)
	go func() {
		lock, err := fs.ExclusiveLock(path)
		if err == nil {
			err = lock.Unlock()
		}
		result <- err
	}()
	return result
}

func TestExclusiveLockIsExclusive(t *testing.T) {
	base := t.TempDir()
	lockPath := filepath.Join(base, "task.lock")
	fs := NewOSFS()
	first, err := fs.ExclusiveLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-acquireLock(fs, lockPath):
		if err == nil {
			t.Fatal("second lock acquired while held")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second lock neither errored nor blocked")
	}
	if err := first.Unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-acquireLock(fs, lockPath):
		if err != nil {
			t.Fatalf("lock was not released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("released lock was not acquirable")
	}
}

func TestProbeFilesystemReportsLocalTempCapabilities(t *testing.T) {
	base := t.TempDir()
	capability, err := ProbeFilesystem(base)
	if err != nil {
		t.Fatal(err)
	}
	if !capability.AtomicRenameReliable || !capability.LockSupported {
		t.Fatalf("local temp directory capabilities: %+v", capability)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("probe files leaked: %v", entries)
	}
}

func TestProbeFilesystemRequiresUsableRoot(t *testing.T) {
	base := t.TempDir()
	if _, err := ProbeFilesystem(filepath.Join(base, "missing")); err == nil {
		t.Fatal("probe accepted a missing root")
	}
}

func TestCanonicalizeRejectsSymlinkedComponent(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are unavailable on this host: %v", err)
	}
	if _, err := NewOSFS().Canonicalize(filepath.Join(link, "state.json")); err == nil {
		t.Fatal("symlinked component canonicalized")
	}
	canonical, err := NewOSFS().Canonicalize(filepath.Join(real, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if canonical != filepath.Join(real, "state.json") {
		t.Fatalf("canonical = %q", canonical)
	}
}
