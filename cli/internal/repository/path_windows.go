//go:build windows

package repository

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// checkPath rejects device paths, alternate data streams and every reparse
// point (including junctions) on the deepest existing component of the path.
func checkPath(full string) error {
	absolute, err := filepath.Abs(full)
	if err != nil {
		return err
	}
	if len(filepath.VolumeName(absolute)) != 2 {
		return fmt.Errorf("local drive path required: %s", full)
	}
	if err := checkStreams(absolute); err != nil {
		return err
	}
	for current := absolute; ; {
		pointer, err := syscall.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attributes, err := syscall.GetFileAttributes(pointer)
		if err == nil {
			if attributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
				return fmt.Errorf("reparse point rejected: %s", current)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("cannot inspect path %s: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return nil
}

func lockFile(full string) (func(), error) {
	if err := checkPath(filepath.Dir(full)); err != nil {
		return nil, err
	}
	pointer, err := syscall.UTF16PtrFromString(full)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(pointer, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, fmt.Errorf("lock is held or unavailable: %w", err)
	}
	if err := checkPath(full); err != nil {
		syscall.CloseHandle(handle)
		return nil, err
	}
	return func() { _ = syscall.CloseHandle(handle) }, nil
}

func syncDirectory(string) error {
	return nil
}
