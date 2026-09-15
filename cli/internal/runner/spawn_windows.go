//go:build windows

package runner

import (
	"os/exec"
	"strconv"
	"syscall"
)

// Self-contained ports of the shared process-tree idioms (cli/internal/worker
// and cli/internal/stagehost keep identical copies); the runner package must
// stay free of sibling imports. Keep the copies in sync.

func processAttributes() *syscall.SysProcAttr {
	// CreateNoWindow parity: keep child console applications from opening a
	// window.
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// killProcessTree terminates the owned process and every descendant, the
// Kill($true) equivalent of Stop-BFOwnedProcess.
func killProcessTree(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	kill := exec.Command("taskkill", "/PID", strconv.Itoa(command.Process.Pid), "/T", "/F")
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := kill.Run(); err != nil {
		return command.Process.Kill()
	}
	return nil
}
