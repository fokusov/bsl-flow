//go:build !windows

package memoryhost

import (
	"os"
	"syscall"
)

// lockFileExclusive takes a non-blocking exclusive flock over the lock file.
func lockFileExclusive(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockFile(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
