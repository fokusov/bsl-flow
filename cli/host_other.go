//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// checkPath mirrors the windows host contract: the path must resolve to an
// absolute clean path whose every component exists and is not a symlink.
// Fail closed when a component cannot be inspected.
func checkPath(full string) error {
	absolute, err := filepath.Abs(full)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(absolute) {
		return fmt.Errorf("absolute path required: %s", full)
	}
	for current := absolute; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("cannot inspect path %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink rejected: %s", current)
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}

func safeMkdir(full string) error {
	if _, err := os.Lstat(full); err == nil {
		return checkPath(full)
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(full)
	if parent == full {
		return fmt.Errorf("missing filesystem root: %s", full)
	}
	if err := safeMkdir(parent); err != nil {
		return err
	}
	if err := os.Mkdir(full, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	return checkPath(full)
}

func lockCache(full string) (func(), error) {
	if err := checkPath(filepath.Dir(full)); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(full); err == nil {
		if err := checkPath(full); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(full, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("bundle extraction is locked or unavailable: %w", err)
	}
	if err := checkPath(full); err != nil {
		file.Close()
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("bundle extraction is locked or unavailable: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

// The legacy engine is rejected before state access on unix: there is no
// trusted machine-wide PowerShell location to resolve. Fail closed.
func systemPowerShell() (string, error) {
	return "", fmt.Errorf("PowerShell 7 is required but the legacy engine is unsupported on this platform")
}

func knownProgramFiles() (string, error) {
	return "", fmt.Errorf("no trusted program files location on this platform")
}

func powerShell7At(programFiles string) (string, error) {
	shell := filepath.Join(programFiles, "PowerShell", "7", "pwsh.exe")
	info, err := os.Stat(shell)
	if err != nil {
		return "", fmt.Errorf("PowerShell 7 is required at %s; install PowerShell 7: %w", shell, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("PowerShell 7 is required at %s; pwsh.exe is not a regular file", shell)
	}
	if err := checkPath(shell); err != nil {
		return "", err
	}
	return shell, nil
}
