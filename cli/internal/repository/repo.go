package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	storeDirName     = "bsl-flow"
	repositorySchema = 1
)

// Repository is a verified clone-local task store rooted at the Git common dir.
type Repository struct {
	Worktree  string
	CommonDir string
	StorePath string
	CloneID   string
}

// KindError carries a controller error class and message for exit mapping.
type KindError struct {
	Kind    string
	Message string
}

func (e *KindError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return &KindError{Kind: "BF_INVALID", Message: safeErrorMessage(fmt.Sprintf(format, args...))}
}

func blocked(format string, args ...any) error {
	return &KindError{Kind: "BF_BLOCKED", Message: safeErrorMessage(fmt.Sprintf(format, args...))}
}

func conflict(format string, args ...any) error {
	return &KindError{Kind: "BF_CONFLICT", Message: safeErrorMessage(fmt.Sprintf(format, args...))}
}

// OpenRepository resolves and validates the Git common dir of a worktree.
func OpenRepository(project string) (*Repository, error) {
	worktree, err := SafePath(project)
	if err != nil {
		return nil, err
	}
	inside, err := gitOutput(worktree, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(inside) != "true" {
		return nil, invalid("not a Git worktree: %s", project)
	}
	common, err := gitOutput(worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, blocked("cannot resolve Git common dir: %v", err)
	}
	common = strings.TrimSpace(common)
	if common == "" {
		return nil, blocked("empty Git common dir for %s", project)
	}
	common, err = SafePath(common)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(common)
	if err != nil || !info.IsDir() {
		return nil, blocked("Git common dir is not a directory: %s", common)
	}
	return &Repository{Worktree: worktree, CommonDir: common, StorePath: filepath.Join(common, storeDirName)}, nil
}

// EnsureIdentity validates the existing repository identity or creates one
// atomically under the repository lock.
func (r *Repository) EnsureIdentity() error {
	store, err := SafePath(r.StorePath)
	if err != nil {
		return err
	}
	identityPath := filepath.Join(store, "repository.json")
	if data, err := ReadFileBytes(identityPath); err == nil {
		object, decodeErr := DecodeObject(data)
		if decodeErr != nil {
			return blocked("corrupt repository identity: %v", decodeErr)
		}
		return r.acceptIdentity(object)
	} else if !os.IsNotExist(err) {
		return blocked("cannot read repository identity: %v", err)
	}

	unlock, err := Lock(filepath.Join(store, "locks", "repository.lock"))
	if err != nil {
		return conflict("repository store lock unavailable: %v", err)
	}
	defer unlock()
	if data, err := ReadFileBytes(identityPath); err == nil {
		object, decodeErr := DecodeObject(data)
		if decodeErr != nil {
			return blocked("corrupt repository identity: %v", decodeErr)
		}
		return r.acceptIdentity(object)
	} else if !os.IsNotExist(err) {
		return blocked("cannot read repository identity: %v", err)
	}
	id, err := randomUUID()
	if err != nil {
		return err
	}
	object := map[string]any{"schema_version": repositorySchema, "clone_id": id, "created_at": nowUTC()}
	data, err := Canonical(object)
	if err != nil {
		return err
	}
	if err := AtomicWrite(identityPath, data, true); err != nil {
		return blocked("cannot create repository identity: %v", err)
	}
	r.CloneID = id
	return nil
}

// LoadIdentity reads the persisted clone identity without creating any store
// directories or files. A repository that has never used a write command gets
// a deterministic process-local identity derived from its verified common dir;
// this keeps cursors scoped without turning a read into a mutation.
func (r *Repository) LoadIdentity() error {
	identityPath := filepath.Join(r.StorePath, "repository.json")
	data, err := ReadFileBytes(identityPath)
	if err == nil {
		object, decodeErr := DecodeObject(data)
		if decodeErr != nil {
			return blocked("corrupt repository identity: %v", decodeErr)
		}
		return r.acceptIdentity(object)
	}
	if !os.IsNotExist(err) {
		return blocked("cannot read repository identity: %v", err)
	}
	r.CloneID = readOnlyCloneID(r.CommonDir)
	return nil
}

func (r *Repository) acceptIdentity(object map[string]any) error {
	if len(object) != 3 {
		return blocked("repository identity contains unsupported fields")
	}
	version, ok := asInt(object["schema_version"])
	if !ok || version != repositorySchema {
		return blocked("unsupported repository schema version")
	}
	cloneID, ok := asString(object["clone_id"])
	if !ok || !isUUID(cloneID) {
		return blocked("repository identity has an invalid clone_id")
	}
	createdAt, ok := asString(object["created_at"])
	if !ok || strings.TrimSpace(createdAt) == "" {
		return blocked("repository identity has no created_at")
	}
	r.CloneID = cloneID
	return nil
}

func readOnlyCloneID(commonDir string) string {
	digest := sha256.Sum256([]byte("bsl-flow:read-only:" + strings.ToLower(filepath.Clean(commonDir))))
	return "readonly-" + hex.EncodeToString(digest[:])
}

// Worktrees returns every existing worktree of the repository, deduplicated
// case-insensitively, falling back to the current worktree.
func (r *Repository) Worktrees() []string {
	output, err := gitOutput(r.Worktree, "worktree", "list", "--porcelain")
	if err != nil {
		return []string{r.Worktree}
	}
	seen := map[string]bool{}
	paths := []string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		candidate := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		if candidate == "" {
			continue
		}
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		key := strings.ToLower(absolute)
		if seen[key] {
			continue
		}
		seen[key] = true
		info, err := os.Stat(absolute)
		if err != nil || !info.IsDir() {
			continue
		}
		if _, err := SafePath(absolute); err != nil {
			continue
		}
		paths = append(paths, absolute)
	}
	if len(paths) == 0 {
		return []string{r.Worktree}
	}
	return paths
}

// HasTask reports whether a task directory exists in the repository store.
func (r *Repository) HasTask(id string) bool {
	if !isUUID(id) {
		return false
	}
	directory := filepath.Join(r.StorePath, "tasks", id)
	if _, err := SafePath(directory); err != nil {
		return false
	}
	info, err := os.Lstat(directory)
	return err == nil && info.IsDir()
}

// resolveOrigin canonicalizes saved provenance without replacing it with the
// current reader worktree when the original checkout has been deleted.
func (r *Repository) resolveOrigin(saved string) string {
	if saved == "" {
		return r.Worktree
	}
	if resolved, err := SafePath(saved); err == nil {
		// Preserve the persisted provenance even when the originating worktree
		// has since been deleted. The reader's current worktree is a separate
		// binding and must not replace historical origin metadata.
		return resolved
	}
	for _, worktree := range r.Worktrees() {
		if strings.EqualFold(strings.TrimSuffix(worktree, string(filepath.Separator)), strings.TrimSuffix(saved, string(filepath.Separator))) {
			return worktree
		}
	}
	return r.Worktree
}

func gitOutput(directory string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	// Repository resolution must be scoped by the explicit -C path. Ambient
	// Git routing variables can otherwise redirect rev-parse to another clone
	// before the common-dir identity is checked.
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch {
		case strings.EqualFold(key, "GIT_DIR"),
			strings.EqualFold(key, "GIT_WORK_TREE"),
			strings.EqualFold(key, "GIT_COMMON_DIR"),
			strings.EqualFold(key, "GIT_INDEX_FILE"),
			strings.EqualFold(key, "GIT_OBJECT_DIRECTORY"),
			strings.EqualFold(key, "GIT_ALTERNATE_OBJECT_DIRECTORIES"):
			continue
		default:
			env = append(env, entry)
		}
	}
	command.Env = env
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// gitCheckIgnored checks one generated path without requiring it to exist.
// Exit status 1 means the path is not ignored; other failures indicate that
// Git could not evaluate the repository policy and must not be treated as a
// safe write boundary.
func gitCheckIgnored(directory, relative string) (bool, error) {
	command := exec.Command("git", "-C", directory, "check-ignore", "--no-index", "--quiet", "--", relative)
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch {
		case strings.EqualFold(key, "GIT_DIR"), strings.EqualFold(key, "GIT_WORK_TREE"), strings.EqualFold(key, "GIT_COMMON_DIR"), strings.EqualFold(key, "GIT_INDEX_FILE"), strings.EqualFold(key, "GIT_OBJECT_DIRECTORY"), strings.EqualFold(key, "GIT_ALTERNATE_OBJECT_DIRECTORIES"):
			continue
		default:
			env = append(env, entry)
		}
	}
	command.Env = env
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func nowUTC() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

func isUUID(text string) bool {
	if len(text) != 36 {
		return false
	}
	for index, character := range text {
		switch index {
		case 8, 13, 18, 23:
			if character != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdef", character) {
				return false
			}
		}
	}
	return true
}

func isSHA256(text string) bool {
	if len(text) != 64 {
		return false
	}
	for _, character := range text {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func fail(message string) error {
	return errors.New(message)
}
