//go:build !windows

package runner

import (
	"os/exec"
	"syscall"
)

// Self-contained ports of the shared process-tree idioms (cli/internal/worker
// and cli/internal/stagehost keep identical copies); the runner package must
// stay free of sibling imports. Keep the copies in sync.

func processAttributes() *syscall.SysProcAttr {
	// A dedicated process group makes the tree kill exact on unix platforms.
	return &syscall.SysProcAttr{Setpgid: true}
}

func killProcessTree(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil {
		return command.Process.Kill()
	}
	return nil
}
