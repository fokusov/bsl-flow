package runner

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// Store is the persistence seam of the serve loop. Callers may substitute an
// in-memory fake for tests; the on-disk default (FileStore) keeps every
// artifact byte-compatible with the PowerShell runner under
// <project>/.bsl-flow/runner.
type Store interface {
	// Events replays the runner journal (Read-BFRunnerJournal parity):
	// strict UTF-8, newline-terminated records, exact field validation.
	Events() ([]Event, error)
	// AppendEvent appends one canonical journal record with an
	// OS-appropriate newline and flushes it to disk.
	AppendEvent(event Event) error
	// LoadSnapshot returns the stored queue snapshot; found is false when no
	// snapshot exists yet.
	LoadSnapshot(expected QueueInput) (Snapshot, bool, error)
	// SaveSnapshot atomically replaces the queue snapshot.
	SaveSnapshot(snapshot Snapshot) error
	// EnsureQueueInput persists the immutable queue input once and refuses a
	// different document under the same queue id.
	EnsureQueueInput(input QueueInput) error
	// Lock takes the exclusive runner lock for the whole queue run
	// (Enter-BFLock parity); release drops it.
	Lock() (release func() error, err error)
}

// FileStore is the on-disk Store over a runner directory.
type FileStore struct {
	RunnerDir string
}

// NewFileStore derives the runner directory from an absolute project path.
func NewFileStore(projectPath string) (*FileStore, error) {
	project, err := safeProjectPath(projectPath)
	if err != nil {
		return nil, err
	}
	return &FileStore{RunnerDir: filepath.Join(project, ".bsl-flow", "runner")}, nil
}

func (s *FileStore) eventsPath() string {
	return filepath.Join(s.RunnerDir, "events.jsonl")
}

func (s *FileStore) snapshotPath(queueID string) string {
	return filepath.Join(s.RunnerDir, "queue-"+queueID+"-snapshot.json")
}

func (s *FileStore) queuePath(queueID string) string {
	return filepath.Join(s.RunnerDir, "queues", queueID+".json")
}

// journalNewline mirrors [Environment]::NewLine of the append path: CRLF on
// Windows, LF elsewhere. The reader accepts both.
func journalNewline() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}

func (s *FileStore) Events() ([]Event, error) {
	data, err := os.ReadFile(s.eventsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, invalid("%v", err)
	}
	// The serve loop inherits the strict inspect-before-resume refusal of
	// Read-BFRunnerJournal, including the torn-final-record case.
	return ParseJournal(data)
}

// AppendEvent writes the exact Save-BFRunnerEvent record:
// Get-BFCanonicalJson(event) + newline, appended and flushed.
func (s *FileStore) AppendEvent(event Event) error {
	if err := event.Validate(); err != nil {
		return err
	}
	status := event.Payload.Status()
	if status == "" {
		return invalid("runner event requires a status payload to persist")
	}
	// The strict reader validates the closed status vocabulary of every
	// record; refuse at the write boundary so this runner can never append
	// a journal its own replay would reject.
	if !knownStatus(status) {
		return invalid("runner event status %q is outside the closed vocabulary", status)
	}
	if event.Revision < -1 {
		return invalid("runner event revision must be at least -1")
	}
	dst := make([]byte, 0, 256)
	dst = append(dst, `{"action":`...)
	var err error
	if dst, err = appendCanonicalStringChecked(dst, string(event.Kind), "event action"); err != nil {
		return err
	}
	dst = append(dst, `,"at":`...)
	if dst, err = appendCanonicalStringChecked(dst, snapshotTimestamp(event.Timestamp), "event at"); err != nil {
		return err
	}
	dst = append(dst, `,"event_key":`...)
	if dst, err = appendCanonicalStringChecked(dst, event.Key(), "event_key"); err != nil {
		return err
	}
	dst = append(dst, `,"revision":`...)
	dst = appendCanonicalInt(dst, int64(event.Revision))
	dst = append(dst, `,"schema_version":1,"status":`...)
	if dst, err = appendCanonicalStringChecked(dst, status, "event status"); err != nil {
		return err
	}
	dst = append(dst, `,"task_id":`...)
	if dst, err = appendCanonicalStringChecked(dst, string(event.TaskID), "event task_id"); err != nil {
		return err
	}
	dst = append(dst, '}')
	dst = append(dst, journalNewline()...)
	if err := os.MkdirAll(s.RunnerDir, 0o755); err != nil {
		return blocked("%v", err)
	}
	file, err := os.OpenFile(s.eventsPath(), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return blocked("%v", err)
	}
	defer file.Close()
	if _, err := file.Write(dst); err != nil {
		return blocked("%v", err)
	}
	if err := file.Sync(); err != nil {
		return blocked("%v", err)
	}
	return nil
}

func (s *FileStore) LoadSnapshot(expected QueueInput) (Snapshot, bool, error) {
	data, err := os.ReadFile(s.snapshotPath(expected.QueueID))
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, invalid("%v", err)
	}
	snapshot, err := parseSnapshot(data, expected)
	if err != nil {
		return Snapshot{}, false, err
	}
	return snapshot, true, nil
}

// SaveSnapshot publishes the canonical snapshot document atomically:
// temp file in the same directory, fsync, rename over the destination.
// This mirrors Write-BFJson -Replace (Flush($true) + MoveFileEx).
func (s *FileStore) SaveSnapshot(snapshot Snapshot) error {
	dst, err := appendSnapshotCanonical(make([]byte, 0, 512), snapshot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.RunnerDir, 0o755); err != nil {
		return blocked("%v", err)
	}
	return atomicWrite(s.snapshotPath(snapshot.QueueID), dst)
}

// EnsureQueueInput implements the immutable-queue contract: the first run
// persists the canonical document, any later run must observe the same
// canonical hash for the same queue id.
func (s *FileStore) EnsureQueueInput(input QueueInput) error {
	path := s.queuePath(input.QueueID)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		encoded, err := input.Canonical()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return blocked("%v", err)
		}
		return atomicWrite(path, encoded)
	}
	if err != nil {
		return invalid("%v", err)
	}
	// Read-BFJson strips a UTF-8 BOM before materializing the document.
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	stored, err := canonicalRaw(make([]byte, 0, 128), data)
	if err != nil {
		return err
	}
	if sha256Hex(stored) != input.Hash() {
		return conflict("queue_id belongs to a different immutable queue input.")
	}
	return nil
}

// Lock mirrors Enter-BFLock over the runner directory: the .writer.lock file
// is opened and exclusively locked for the whole queue run; a second holder
// receives BF_CONFLICT, never a takeover.
func (s *FileStore) Lock() (func() error, error) {
	info, err := os.Lstat(s.RunnerDir)
	if err == nil && !info.IsDir() {
		return nil, invalid("Lock path is a file, not a directory.")
	}
	if err := os.MkdirAll(s.RunnerDir, 0o755); err != nil {
		return nil, blocked("%v", err)
	}
	return lockRunnerDirectory(filepath.Join(s.RunnerDir, ".writer.lock"))
}

// atomicWrite writes bytes to a fresh temporary file in the destination
// directory, syncs it and renames it over the destination.
func atomicWrite(path string, data []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return blocked("%v", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return blocked("%v", err)
	}
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return blocked("%v", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return blocked("%v", err)
	}
	if err := temporary.Close(); err != nil {
		return blocked("%v", err)
	}
	if err := os.Rename(name, path); err != nil {
		return blocked("could not publish JSON file: %v", err)
	}
	return nil
}
