//go:build windows

package memoryhost

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32LockFileEx   = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")
	kernel32UnlockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("UnlockFileEx")
)

// lockFileExclusive takes an exclusive, fail-immediately byte-range lock so
// two helper processes cannot hold the same .writer.lock, mirroring the
// FileStream(FileShare.None) acquisition in Enter-BFLock.
func lockFileExclusive(file *os.File) error {
	const lockFileExclusiveLock = 0x00000002
	const lockFileFailImmediately = 0x00000001
	var overlapped syscall.Overlapped
	handle := syscall.Handle(file.Fd())
	result, _, err := kernel32LockFileEx.Call(
		uintptr(handle),
		lockFileExclusiveLock|lockFileFailImmediately,
		0,
		1, 0,
		uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return err
	}
	return nil
}

func unlockFile(file *os.File) {
	var overlapped syscall.Overlapped
	handle := syscall.Handle(file.Fd())
	_, _, _ = kernel32UnlockFileEx.Call(
		uintptr(handle),
		0,
		1, 0,
		uintptr(unsafe.Pointer(&overlapped)))
}
