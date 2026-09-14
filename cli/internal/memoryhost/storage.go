package memoryhost

// This file ports the storage primitives the memory plane needs from
// Task.Storage.ps1: Read-BFJson, Write-BFJson (atomic publication with a
// same-directory temporary), Get-BFFileHash and Enter-BFLock. The legacy
// task-adoption checks in those functions key on .bsl-flow/tasks/<uuid>
// paths, which the memory store (.bsl-flow/memory) never produces, so they
// are unreachable here by construction.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const jsonMaximumBytes = 16 << 20 // $script:BFJsonMaximumBytes

// readBFJSON ports Read-BFJson for object documents.
func readBFJSON(path string) (map[string]any, error) {
	fullPath, err := assertSafePath(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		if err != nil && !os.IsNotExist(err) {
			return nil, bfInvalid("Cannot read JSON file: %s", err.Error())
		}
		return nil, bfInvalid("JSON file does not exist: %s", fullPath)
	}
	if info.Size() > jsonMaximumBytes {
		return nil, bfInvalid("JSON file exceeds the maximum allowed size.")
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, bfInvalid("Cannot read JSON file: %s", err.Error())
	}
	text, ok := strictUTF8Decode(data)
	if !ok {
		return nil, bfInvalid("JSON file is not valid UTF-8.")
	}
	text = stripBOM(text)
	kind, err := testJSONSyntax(text)
	if err != nil {
		return nil, err
	}
	if kind != "object" {
		return nil, bfInvalid("Top-level JSON value must be an object.")
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, bfInvalid("Cannot materialize JSON object: %s", err.Error())
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, bfInvalid("Cannot materialize JSON object: top-level value is not an object.")
	}
	return object, nil
}

func stripBOM(text string) string {
	return strings.TrimPrefix(text, "\ufeff")
}

// writeBFJSON ports Write-BFJson (without the unreachable legacy task
// adoption branches): canonical bytes, same-directory temporary, exclusive
// creation, optional atomic replace.
func writeBFJSON(path string, value any, replace bool) error {
	fullPath, err := assertSafePath(path)
	if err != nil {
		return err
	}
	parent := filepath.Dir(fullPath)
	if parent == "" {
		return bfInvalid("JSON path must have a parent directory.")
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return bfInvalid("Cannot create JSON parent directory: %s", err.Error())
	}
	if _, err := assertSafePath(parent); err != nil {
		return err
	}
	if !replace {
		if info, err := os.Stat(fullPath); err == nil && !info.IsDir() {
			return bfConflict("Refusing to overwrite JSON file: %s", fullPath)
		}
	}
	data, err := canonicalBytes(value)
	if err != nil {
		return err
	}
	temporary := filepath.Join(parent, "."+filepath.Base(fullPath)+"."+randomHex(16)+".tmp")
	defer func() {
		if _, statErr := os.Stat(temporary); statErr == nil {
			_ = os.Remove(temporary)
		}
	}()
	if err := writeTempFile(temporary, data); err != nil {
		return bfConflict("Could not publish JSON file: %s", err.Error())
	}
	if err := os.Rename(temporary, fullPath); err != nil {
		return bfConflict("Could not publish JSON file: %s", err.Error())
	}
	return nil
}

func writeTempFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func randomHex(bytes int) string {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		// Mirror the GUID-format temporary name length even if the system
		// entropy source is unavailable at this exact moment.
		for index := range buffer {
			buffer[index] = byte(index * 7)
		}
	}
	return hex.EncodeToString(buffer)
}

// statFile is a small os.Stat wrapper shared by the path contract checks.
func statFile(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

// fileSHA ports Get-BFFileHash: the plain lowercase SHA-256 of the file bytes.
func fileSHA(path string) (string, error) {
	fullPath, err := assertSafePath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		return "", bfInvalid("File does not exist: %s", fullPath)
	}
	file, err := os.Open(fullPath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// The writer lock mirrors Enter-BFLock for the memory directory: an
// exclusive .writer.lock handle, re-entry detection inside the process and
// cross-process exclusion through the platform byte-range/flock primitive.
type writerLock struct {
	directory string
	file      *os.File
}

var (
	heldLocksMutex sync.Mutex
	heldLocks      = map[string]*writerLock{}
)

func enterLock(directory string) (*writerLock, error) {
	fullDirectory, err := assertSafePath(directory)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(fullDirectory); err == nil && !info.IsDir() {
		return nil, bfInvalid("Lock path is a file, not a directory.")
	}
	if err := os.MkdirAll(fullDirectory, 0o755); err != nil {
		return nil, bfInvalid("Cannot create lock directory: %s", err.Error())
	}
	fullDirectory, err = assertSafePath(fullDirectory)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(fullDirectory); err != nil || !info.IsDir() {
		return nil, bfInvalid("Lock path is not a directory.")
	}
	heldLocksMutex.Lock()
	if _, held := heldLocks[fullDirectory]; held {
		heldLocksMutex.Unlock()
		return nil, bfConflict("Writer lock is already held.")
	}
	heldLocksMutex.Unlock()
	file, err := os.OpenFile(filepath.Join(fullDirectory, ".writer.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, bfConflict("Writer lock is held by another controller.")
	}
	if err := lockFileExclusive(file); err != nil {
		file.Close()
		return nil, bfConflict("Writer lock is held by another controller.")
	}
	lock := &writerLock{directory: fullDirectory, file: file}
	heldLocksMutex.Lock()
	heldLocks[fullDirectory] = lock
	heldLocksMutex.Unlock()
	return lock, nil
}

func (lock *writerLock) release() {
	if lock == nil {
		return
	}
	heldLocksMutex.Lock()
	delete(heldLocks, lock.directory)
	heldLocksMutex.Unlock()
	unlockFile(lock.file)
	lock.file.Close()
}
