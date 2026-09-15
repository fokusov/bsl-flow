//go:build windows

package runner

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32Lock     = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32Lock.NewProc("LockFileEx")
	procUnlockFileEx = kernel32Lock.NewProc("UnlockFileEx")
)

const (
	lockfileExclusiveLock   = 0x00000002
	lockfileFailImmediately = 0x00000001
)

// lockRunnerDirectory holds an exclusive fail-fast byte-range lock over the
// runner .writer.lock file, the Go equivalent of the FileStream FileShare::None
// hold of Enter-BFLock: a second supervisor instance fails with BF_CONFLICT
// for the whole queue run.
func lockRunnerDirectory(path string) (func() error, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, conflict("Writer lock is held by another controller.")
	}
	handle := syscall.Handle(file.Fd())
	overlapped := new(syscall.Overlapped)
	// Lock the whole file regardless of size; locking past EOF is allowed.
	result, _, callErr := procLockFileEx.Call(
		uintptr(handle),
		lockfileExclusiveLock|lockfileFailImmediately,
		0,
		0xFFFFFFFF,
		0,
		uintptr(unsafe.Pointer(overlapped)),
	)
	if result == 0 {
		file.Close()
		if callErr != nil {
			return nil, conflict("Writer lock is held by another controller.")
		}
		return nil, blocked("%v", callErr)
	}
	return func() error {
		defer file.Close()
		result, _, callErr := procUnlockFileEx.Call(
			uintptr(handle),
			0,
			0xFFFFFFFF,
			0,
			uintptr(unsafe.Pointer(overlapped)),
		)
		if result == 0 && callErr != nil {
			return blocked("%v", callErr)
		}
		return nil
	}, nil
}
