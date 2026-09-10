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
	programFiles, err := knownProgramFiles()
	if err != nil {
		return "", err
	}
	return powerShell7At(programFiles)
}

func knownProgramFiles() (string, error) {
	// CSIDL_PROGRAM_FILES through the Windows Shell standard-folder API, rather than a
	// caller-controlled environment variable or the current directory.
	buf := make([]uint16, 32768)
	proc := syscall.NewLazyDLL("shell32.dll").NewProc("SHGetFolderPathW")
	result, _, callErr := proc.Call(0, 0x0026, 0, 0, uintptr(unsafe.Pointer(&buf[0])))
	if result != 0 {
		return "", fmt.Errorf("PowerShell 7 is required but Windows Program Files could not be resolved (HRESULT 0x%x): %w", result, callErr)
	}
	path := syscall.UTF16ToString(buf)
	if path == "" {
		return "", fmt.Errorf("PowerShell 7 is required but Windows Program Files returned an empty path")
	}
	return path, nil
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
