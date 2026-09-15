//go:build !windows

package runner

import (
	"errors"
	"os"
	"syscall"
)

// osProcessAlive fails closed on platforms without a portable start-time
// probe: a pid that still exists is conservatively reported as the owner even
// though its start-time identity cannot be verified. The supervisor can only
// under-dispatch (stay quiet) on such a platform; it never resumes or
// re-dispatches an attempt that might still be running.
func osProcessAlive(pid int64, startTimeUTC string) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(int(pid))
	if err != nil {
		return false
	}
	if err := process.Signal(syscall.Signal(0)); err != nil {
		// A live process owned by another account reports EPERM; only a
		// resolved-dead pid reports not alive.
		return errors.Is(err, syscall.EPERM)
	}
	return true
}
