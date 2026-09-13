//go:build !windows

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type osFS struct{}

// NewOSFS returns the process filesystem implementation for the build target.
func NewOSFS() FS { return osFS{} }

// checkPath rejects symlinks on existing components; missing trailing
// components are allowed.
func (osFS) checkPath(absolute string) error {
	if !filepath.IsAbs(absolute) {
		return fmt.Errorf("absolute path required: %s", absolute)
	}
	for current := absolute; ; {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink rejected: %s", current)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("cannot inspect path %s: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return nil
}

func (f osFS) Canonicalize(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := f.checkPath(absolute); err != nil {
		return "", err
	}
	return absolute, nil
}

func (osFS) LstatNoFollow(path string) (os.FileInfo, error) {
	return os.Lstat(path)
}

func (osFS) AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	return atomicWriteFile(path, data, perm)
}

func (osFS) SyncDir(path string) error {
	return syncDirectory(path)
}

type unixLock struct{ file *os.File }

func (l unixLock) Unlock() error {
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return fmt.Errorf("unlock: %w", unlockErr)
	}
	return closeErr
}

func (f osFS) ExclusiveLock(path string) (LockHandle, error) {
	if err := f.checkPath(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		if err := f.checkPath(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock is held or unavailable: %w", err)
	}
	if err := f.checkPath(path); err != nil {
		file.Close()
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("lock is held or unavailable: %w", err)
	}
	return unixLock{file: file}, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
