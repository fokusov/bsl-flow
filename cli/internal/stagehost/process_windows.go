//go:build windows

package stagehost

import (
	"os/exec"
	"strconv"
	"syscall"
)

func processAttributes() *syscall.SysProcAttr {
	// CreateNoWindow parity: keep child console applications from opening a
	// window; the managed receipt owns every visible byte.
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// killProcessTree terminates the owned process and every descendant, mirroring
// Stop-BFOwnedProcess's Kill($true) for the exact process this runner owns.
func killProcessTree(command *exec.Cmd) error {
	if command == nil || command.Process == nil {
		return nil
	}
	kill := exec.Command("taskkill", "/PID", itoa(command.Process.Pid), "/T", "/F")
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := kill.Run(); err != nil {
		return command.Process.Kill()
	}
	return nil
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
