//go:build !windows

package repository

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func checkPath(full string) error {
	absolute, err := filepath.Abs(full)
	if err != nil {
		return err
	}
	if err := checkStreams(absolute); err != nil {
		return err
	}
	for current := absolute; ; {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink rejected: %s", current)
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
	file, err := os.OpenFile(full, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("lock is held or unavailable: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
