//go:build !windows

package worker

import (
	"os/exec"
	"syscall"
)

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
