package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// FS is the cross-platform filesystem contract: canonical paths without
// symlink/reparse components, atomic publication with a durability boundary,
// exclusive locks and directory sync. Implementations fail closed.
type FS interface {
	// Canonicalize returns the absolute clean path after rejecting
	// symlink/reparse components. Missing trailing components are allowed so
	// publication targets can be canonicalized before they exist.
	Canonicalize(path string) (string, error)
	LstatNoFollow(path string) (os.FileInfo, error)
	AtomicWriteFile(path string, data []byte, perm os.FileMode) error
	SyncDir(path string) error
	ExclusiveLock(path string) (LockHandle, error)
}

// LockHandle releases exactly one exclusive lock acquired through FS.
type LockHandle interface {
	Unlock() error
}

// Capability reports durability heuristics for one filesystem root.
type Capability struct {
	AtomicRenameReliable bool
	LockSupported        bool
}

// ProbeFilesystem exercises the primitives authoritative writes depend on in
// root: a same-directory atomic rename and a create/lock/unlock probe of a
// lock file. Any probe failure reports the capability as false (fail closed);
// the error return is reserved for a root that cannot host probe files at all.
func ProbeFilesystem(root string) (Capability, error) {
	var capability Capability
	fs := NewOSFS()
	canonical, err := fs.Canonicalize(root)
	if err != nil {
		return capability, fmt.Errorf("filesystem probe root: %w", err)
	}
	source, err := os.CreateTemp(canonical, ".platform-probe-*")
	if err != nil {
		return capability, fmt.Errorf("filesystem probe: %w", err)
	}
	sourceName := source.Name()
	if _, err = source.Write([]byte("probe")); err == nil {
		err = source.Sync()
	}
	if closeErr := source.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(sourceName)
		return capability, fmt.Errorf("filesystem probe: %w", err)
	}
	target := sourceName + ".renamed"
	if renameErr := os.Rename(sourceName, target); renameErr == nil {
		if _, statErr := os.Lstat(sourceName); os.IsNotExist(statErr) {
			capability.AtomicRenameReliable = true
		}
		_ = os.Remove(target)
	} else {
		_ = os.Remove(sourceName)
	}
	lockPath := filepath.Join(canonical, ".platform-probe.lock")
	if lock, lockErr := fs.ExclusiveLock(lockPath); lockErr == nil {
		if unlockErr := lock.Unlock(); unlockErr == nil {
			capability.LockSupported = true
		}
	}
	_ = os.Remove(lockPath)
	return capability, nil
}

// atomicWriteFile publishes data at path through write-to-temp in the same
// directory, fsync, rename and a directory fsync, so a crash never leaves a
// partial authoritative file behind. Temporary files are removed on failure.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	directory := filepath.Dir(path)
	temp, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("atomic write temp: %w", err)
	}
	name := temp.Name()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("atomic write temp: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("atomic write temp: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("atomic write temp: %w", err)
	}
	if err := os.Chmod(name, perm); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("atomic write temp permissions: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("atomic write publish: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("atomic write sync: %w", err)
	}
	return nil
}
