package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// Check every existing component, including Windows junctions and other
// reparse points that are not necessarily reported as portable symlinks.
func checkPath(full string) error {
	absolute, err := filepath.Abs(full)
	if err != nil {
		return err
	}
	if filepath.VolumeName(absolute) == "" || len(filepath.VolumeName(absolute)) != 2 {
		return fmt.Errorf("local drive path required: %s", full)
	}
	for current := absolute; ; current = filepath.Dir(current) {
		p, err := syscall.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attributes, err := syscall.GetFileAttributes(p)
		if err != nil {
			return fmt.Errorf("cannot inspect path %s: %w", current, err)
		}
		if attributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("reparse point rejected: %s", current)
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
		return fmt.Errorf("missing drive root: %s", full)
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
	p, err := syscall.UTF16PtrFromString(full)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, fmt.Errorf("bundle extraction is locked or unavailable: %w", err)
	}
	if err := checkPath(full); err != nil {
		syscall.CloseHandle(h)
		return nil, err
	}
	return func() { _ = syscall.CloseHandle(h) }, nil
}

func systemPowerShell() (string, error) {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemDirectoryW")
	buf := make([]uint16, 32768)
	n, _, err := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || n >= uintptr(len(buf)) {
		return "", fmt.Errorf("GetSystemDirectoryW failed: %w", err)
	}
	shell := filepath.Join(syscall.UTF16ToString(buf[:n]), "WindowsPowerShell", "v1.0", "powershell.exe")
	if err := checkPath(shell); err != nil {
		return "", err
	}
	return shell, nil
}
