//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	lockFileFailImmediately = 0x00000001
	lockFileExclusiveLock   = 0x00000002
)

var (
	kernel32LockFileEx   = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")
	kernel32UnlockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("UnlockFileEx")
)

type osFS struct{}

// NewOSFS returns the process filesystem implementation for the build target.
func NewOSFS() FS { return osFS{} }

// checkPath rejects device-qualified paths and every reparse point (including
// junctions) on existing components; missing trailing components are allowed.
func (osFS) checkPath(absolute string) error {
	if len(filepath.VolumeName(absolute)) != 2 {
		return fmt.Errorf("local drive path required: %s", absolute)
	}
	for current := absolute; ; {
		pointer, err := syscall.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attributes, err := syscall.GetFileAttributes(pointer)
		if err == nil {
			if attributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
				return fmt.Errorf("reparse point rejected: %s", current)
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

// Directory fsync is not supported by the Windows handle API (write access to
// directory handles is denied); NTFS metadata journaling covers the rename
// durability boundary. The operation stays in the interface so callers do not
// branch per OS.
func (osFS) SyncDir(string) error {
	return nil
}

type windowsLock struct {
	handle syscall.Handle
}

func (l windowsLock) Unlock() error {
	var overlapped syscall.Overlapped
	result, _, callErr := kernel32UnlockFileEx.Call(uintptr(l.handle), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	closeErr := syscall.CloseHandle(l.handle)
	if result == 0 {
		if callErr == nil {
			callErr = errors.New("UnlockFileEx failed")
		}
		return fmt.Errorf("unlock: %w", callErr)
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
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(pointer, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, fmt.Errorf("lock is held or unavailable: %w", err)
	}
	var overlapped syscall.Overlapped
	result, _, callErr := kernel32LockFileEx.Call(uintptr(handle), lockFileExclusiveLock|lockFileFailImmediately, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		_ = syscall.CloseHandle(handle)
		if callErr == nil {
			callErr = errors.New("LockFileEx failed")
		}
		return nil, fmt.Errorf("lock is held or unavailable: %w", callErr)
	}
	return windowsLock{handle: handle}, nil
}

func syncDirectory(string) error {
	return nil
}
