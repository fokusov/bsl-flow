package repository

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func checkStreams(absolute string) error {
	volume := filepath.VolumeName(absolute)
	if volume == "" {
		return nil
	}
	if strings.Contains(absolute[len(volume):], ":") {
		return fmt.Errorf("alternate data stream paths are not allowed: %s", absolute)
	}
	return nil
}

// SafePath returns the absolute path after rejecting device prefixes, alternate
// data streams and reparse/symlink components.
func SafePath(target string) (string, error) {
	if strings.TrimSpace(target) == "" {
		return "", errors.New("path must not be empty")
	}
	if strings.HasPrefix(target, `\\?\`) || strings.HasPrefix(target, `\\.\`) || strings.Contains(target, "::") {
		return "", fmt.Errorf("device or provider-qualified paths are not allowed: %s", target)
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if err := checkPath(absolute); err != nil {
		return "", err
	}
	return absolute, nil
}

// SafeMkdir creates the directory and every missing parent below the drive root.
func SafeMkdir(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return checkPath(path)
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return fmt.Errorf("missing drive root: %s", path)
	}
	if err := SafeMkdir(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	return checkPath(path)
}

// ReadFileBytes reads a bounded regular file after validating its path.
func ReadFileBytes(path string) ([]byte, error) {
	full, err := SafePath(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(full)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", full)
	}
	if info.Size() > maxJSONBytes {
		return nil, fmt.Errorf("file exceeds the maximum allowed size: %s", full)
	}
	return os.ReadFile(full)
}

// AtomicWrite writes bytes to a unique temporary file, flushes them and
// publishes the file with a single rename.
func AtomicWrite(path string, data []byte, replace bool) error {
	full, err := SafePath(path)
	if err != nil {
		return err
	}
	parent := filepath.Dir(full)
	if err := SafeMkdir(parent); err != nil {
		return err
	}
	name, err := randomHex(16)
	if err != nil {
		return err
	}
	temporary := filepath.Join(parent, "."+filepath.Base(full)+"."+name+".tmp")
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(temporary)
		if writeErr != nil {
			return writeErr
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	}
	if !replace {
		if _, err := os.Lstat(full); err == nil {
			_ = os.Remove(temporary)
			return fmt.Errorf("refusing to overwrite existing file: %s", full)
		} else if !os.IsNotExist(err) {
			_ = os.Remove(temporary)
			return err
		}
	}
	if err := os.Rename(temporary, full); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return syncDirectory(parent)
}

// Lock takes an exclusive OS lock on path, creating it if needed. The returned
// function releases the lock.
func Lock(path string) (func(), error) {
	full, err := SafePath(path)
	if err != nil {
		return nil, err
	}
	if err := SafeMkdir(filepath.Dir(full)); err != nil {
		return nil, err
	}
	return lockFile(full)
}

func randomHex(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func randomUUID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	buffer[6] = (buffer[6] & 0x0f) | 0x40
	buffer[8] = (buffer[8] & 0x3f) | 0x80
	text := hex.EncodeToString(buffer)
	return text[0:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:32], nil
}
