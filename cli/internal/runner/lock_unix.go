//go:build !windows

package runner

import (
	"os"
	"syscall"
)

// lockRunnerDirectory holds an exclusive non-blocking flock over the runner
// .writer.lock file, matching the FileShare::None hold of Enter-BFLock: a
// second supervisor instance fails with BF_CONFLICT for the whole queue run.
func lockRunnerDirectory(path string) (func() error, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, conflict("Writer lock is held by another controller.")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, conflict("Writer lock is held by another controller.")
	}
	return func() error {
		defer file.Close()
		return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	}, nil
}
