// Package bootstrap implements the native, idempotent, comment-preserving
// bootstrap and project upgrade for the BSL Flow managed project files.
//
// The contract is ported from the deterministic PowerShell bootstrap:
//
//   - Initialize-BSLFlowProject.ps1 (copy-if-missing for AGENTS.md,
//     bsl-flow.yaml and the sentinel; marked-block append for .gitignore);
//   - Update-BSLFlowProject.ps1 (Add-MissingTemplateNodes key merge for
//     bsl-flow.yaml, marked-block replace for .gitignore and AGENTS.md).
//
// Guarantees: every existing byte of user content (comments, values, order,
// detected line endings) survives a merge unchanged; only missing managed
// lines are inserted; a second run plans "skip" for every managed file
// (idempotence); writes are atomic (temp file + rename); no file is deleted,
// no permissions are changed, no external tool is installed and no
// credentials are read or written.
//
// Divergences from the PowerShell scripts are conservative and documented on
// the helpers below.
package bootstrap

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Planned actions for a managed file.
const (
	ActionCreate = "create"
	ActionMerge  = "merge"
	ActionSkip   = "skip"
)

// Managed file kinds.
const (
	kindAgents      = "agents"
	kindConfig      = "config"
	kindGitIgnore   = "gitignore"
	kindSentinel    = "sentinel"
	kindPlaceholder = "placeholder"
)

const utf8BOM = "\ufeff"

// Plan is the read-only bootstrap decision for one project root.
type Plan struct {
	ProjectRoot string
	Files       []FilePlan
}

// FilePlan describes the planned action for one managed file.
type FilePlan struct {
	RelPath string
	Action  string // "create" | "merge" | "skip"
	Reason  string
}

// AppliedFile reports what Apply actually performed for one managed file.
type AppliedFile struct {
	RelPath string
	Action  string
	Reason  string
}

// MissingTemplateError reports a packaged template that the template source
// could not provide; nothing is written when it is returned.
type MissingTemplateError struct {
	RelPath string
	Err     error
}

func (e *MissingTemplateError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("bootstrap template %q is unavailable: %v", e.RelPath, e.Err)
	}
	return fmt.Sprintf("bootstrap template %q is unavailable", e.RelPath)
}

// Unwrap exposes the underlying template-source error.
func (e *MissingTemplateError) Unwrap() error { return e.Err }

// managedFiles mirrors the file-level output of Initialize-BSLFlowProject.ps1
// (lines 282-343). Git, OpenSpec scaffolding and openspec/config.yaml are
// produced by external tools and stay outside this package.
var managedFiles = []struct {
	RelPath string
	Kind    string
}{
	{RelPath: "AGENTS.md", Kind: kindAgents},
	{RelPath: "bsl-flow.yaml", Kind: kindConfig},
	{RelPath: ".gitignore", Kind: kindGitIgnore},
	{RelPath: ".bsl-flow/project.yaml", Kind: kindSentinel},
	{RelPath: ".bsl-flow/reports/.gitkeep", Kind: kindPlaceholder},
	{RelPath: ".bsl-flow/evidence/.gitkeep", Kind: kindPlaceholder},
}

func managedKind(rel string) (string, bool) {
	for _, managed := range managedFiles {
		if managed.RelPath == rel {
			return managed.Kind, true
		}
	}
	return "", false
}

// Inspect plans the bootstrap for projectRoot without writing anything.
// A managed file is planned as "create" when absent, "merge" when present but
// missing managed content, and "skip" when it already satisfies the packaged
// template. It never inspects outside projectRoot: relative paths are
// validated and joined under the root.
func Inspect(projectRoot string, templatesFS func(rel string) ([]byte, error)) (Plan, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return Plan{}, err
	}
	if err := validateProjectRoot(root); err != nil {
		return Plan{}, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return Plan{}, fmt.Errorf("project root is not accessible: %w", err)
	}
	if !info.IsDir() {
		return Plan{}, fmt.Errorf("project root is not a directory: %s", root)
	}
	plan := Plan{ProjectRoot: root}
	for _, managed := range managedFiles {
		entry, err := inspectManagedFile(root, managed.RelPath, managed.Kind, templatesFS)
		if err != nil {
			return Plan{}, err
		}
		plan.Files = append(plan.Files, entry)
	}
	return plan, nil
}

// Apply executes the plan and reports what was performed for each managed
// file. Merge decisions are recomputed from the file content present at apply
// time, so the operation stays idempotent and comment-preserving even if the
// project changed after Inspect. Nothing outside the managed file set is
// read, written or deleted.
func Apply(plan Plan, templatesFS func(rel string) ([]byte, error)) ([]AppliedFile, error) {
	root, err := filepath.Abs(plan.ProjectRoot)
	if err != nil {
		return nil, err
	}
	if err := validateProjectRoot(root); err != nil {
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("project root is not an accessible directory: %s", root)
	}
	applied := make([]AppliedFile, 0, len(plan.Files))
	for _, file := range plan.Files {
		result, err := applyManagedFile(root, file, templatesFS)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file.RelPath, err)
		}
		applied = append(applied, result)
	}
	return applied, nil
}

// Verify re-inspects the project and reports whether every managed file is
// present and already satisfies the packaged template. Problems lists the
// remaining create/merge actions (or the inspection error).
func Verify(projectRoot string, templatesFS func(rel string) ([]byte, error)) (bool, []string) {
	plan, err := Inspect(projectRoot, templatesFS)
	if err != nil {
		return false, []string{fmt.Sprintf("bootstrap verification failed: %v", err)}
	}
	var problems []string
	for _, file := range plan.Files {
		if file.Action == ActionSkip {
			continue
		}
		problems = append(problems, fmt.Sprintf("%s requires %s: %s", file.RelPath, file.Action, file.Reason))
	}
	return len(problems) == 0, problems
}

func inspectManagedFile(root, rel, kind string, templatesFS func(rel string) ([]byte, error)) (FilePlan, error) {
	full, err := resolveManagedPath(root, rel)
	if err != nil {
		return FilePlan{}, err
	}
	info, err := os.Stat(full)
	switch {
	case err == nil && info.IsDir():
		return FilePlan{}, fmt.Errorf("a directory exists where a managed file is required: %s", full)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return FilePlan{}, fmt.Errorf("cannot inspect managed file %s: %w", full, err)
	case err != nil:
		return FilePlan{RelPath: rel, Action: ActionCreate, Reason: "managed file is absent"}, nil
	}
	switch kind {
	case kindSentinel:
		// Initialize-BSLFlowProject.ps1:311-323 preserves an existing
		// sentinel; the version bump belongs to the upgrade command.
		return FilePlan{RelPath: rel, Action: ActionSkip, Reason: "existing sentinel is preserved and never merged"}, nil
	case kindPlaceholder:
		return FilePlan{RelPath: rel, Action: ActionSkip, Reason: "existing placeholder is preserved"}, nil
	}
	current, err := os.ReadFile(full)
	if err != nil {
		return FilePlan{}, fmt.Errorf("cannot read managed file %s: %w", full, err)
	}
	merged, detail, err := mergeManagedContent(kind, rel, current, templatesFS)
	if err != nil {
		return FilePlan{}, err
	}
	if merged == nil {
		return FilePlan{RelPath: rel, Action: ActionSkip, Reason: "managed content already satisfies the packaged template"}, nil
	}
	return FilePlan{RelPath: rel, Action: ActionMerge, Reason: detail}, nil
}

func applyManagedFile(root string, file FilePlan, templatesFS func(rel string) ([]byte, error)) (AppliedFile, error) {
	kind, ok := managedKind(file.RelPath)
	if !ok {
		return AppliedFile{}, fmt.Errorf("not a BSL Flow managed file: %q", file.RelPath)
	}
	full, err := resolveManagedPath(root, file.RelPath)
	if err != nil {
		return AppliedFile{}, err
	}
	info, statErr := os.Stat(full)
	if statErr == nil && info.IsDir() {
		return AppliedFile{}, fmt.Errorf("a directory exists where a managed file is required: %s", full)
	}
	exists := statErr == nil
	switch file.Action {
	case ActionSkip:
		if !exists {
			return AppliedFile{}, fmt.Errorf("planned skip but the managed file is absent: %s", full)
		}
		return AppliedFile{RelPath: file.RelPath, Action: ActionSkip, Reason: "already satisfied"}, nil
	case ActionCreate:
		if exists {
			return AppliedFile{RelPath: file.RelPath, Action: ActionSkip, Reason: "file already exists and is preserved"}, nil
		}
		data, err := createContent(kind, file.RelPath, templatesFS)
		if err != nil {
			return AppliedFile{}, err
		}
		if err := atomicWrite(full, data, 0o644); err != nil {
			return AppliedFile{}, err
		}
		return AppliedFile{RelPath: file.RelPath, Action: ActionCreate, Reason: "created from the packaged template"}, nil
	case ActionMerge:
		if !exists {
			data, err := createContent(kind, file.RelPath, templatesFS)
			if err != nil {
				return AppliedFile{}, err
			}
			if err := atomicWrite(full, data, 0o644); err != nil {
				return AppliedFile{}, err
			}
			return AppliedFile{RelPath: file.RelPath, Action: ActionCreate, Reason: "file was absent at apply time; created from the packaged template"}, nil
		}
		current, err := os.ReadFile(full)
		if err != nil {
			return AppliedFile{}, fmt.Errorf("cannot read managed file: %w", err)
		}
		merged, detail, err := mergeManagedContent(kind, file.RelPath, current, templatesFS)
		if err != nil {
			return AppliedFile{}, err
		}
		if merged == nil {
			return AppliedFile{RelPath: file.RelPath, Action: ActionSkip, Reason: "already satisfied"}, nil
		}
		mode := info.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := atomicWrite(full, merged, mode); err != nil {
			return AppliedFile{}, err
		}
		return AppliedFile{RelPath: file.RelPath, Action: ActionMerge, Reason: detail}, nil
	default:
		return AppliedFile{}, fmt.Errorf("unsupported plan action %q for %s", file.Action, file.RelPath)
	}
}

func createContent(kind, rel string, templatesFS func(rel string) ([]byte, error)) ([]byte, error) {
	if kind == kindPlaceholder {
		// Placeholders are created empty, matching New-Item in
		// Initialize-BSLFlowProject.ps1:332-343; no template is involved.
		return []byte{}, nil
	}
	template, err := loadTemplate(templatesFS, rel)
	if err != nil {
		return nil, err
	}
	return []byte(template), nil
}

// mergeManagedContent returns the merged bytes for a present managed file,
// or nil when the content already satisfies the packaged template.
func mergeManagedContent(kind, rel string, current []byte, templatesFS func(rel string) ([]byte, error)) ([]byte, string, error) {
	template, err := loadTemplate(templatesFS, rel)
	if err != nil {
		return nil, "", err
	}
	text, bom := stripBOM(string(current))
	var merged, detail string
	var mergeErr error
	switch kind {
	case kindGitIgnore:
		merged, detail, mergeErr = mergeGitIgnoreText(text, template)
	case kindAgents:
		merged, detail, mergeErr = mergeAgentsText(text, template)
	case kindConfig:
		merged, detail, mergeErr = mergeBSLFlowYAML(text, template)
	default:
		return nil, "", fmt.Errorf("unsupported managed kind: %s", kind)
	}
	if mergeErr != nil {
		return nil, "", mergeErr
	}
	if merged == text {
		return nil, "", nil
	}
	if bom != "" {
		// A byte-order mark on the user file is part of its bytes and is
		// preserved exactly.
		return []byte(bom + merged), detail, nil
	}
	return []byte(merged), detail, nil
}

func loadTemplate(templatesFS func(rel string) ([]byte, error), rel string) (string, error) {
	data, err := templatesFS(rel)
	if err != nil {
		return "", &MissingTemplateError{RelPath: rel, Err: err}
	}
	// The PowerShell scripts read templates as text (BOM stripped by
	// Get-Content); the port strips the mark here for the same semantics.
	data = bytes.TrimPrefix(data, []byte(utf8BOM))
	if len(data) == 0 {
		return "", &MissingTemplateError{RelPath: rel}
	}
	return string(data), nil
}

func stripBOM(text string) (string, string) {
	if strings.HasPrefix(text, utf8BOM) {
		return text[len(utf8BOM):], utf8BOM
	}
	return text, ""
}

// validateProjectRoot mirrors the PowerShell safety boundary
// (Initialize-BSLFlowProject.ps1:137-147): no filesystem root and no user
// profile directory may be bootstrapped.
func validateProjectRoot(root string) error {
	if parent := filepath.Dir(root); parent == root {
		return fmt.Errorf("refusing to bootstrap a filesystem root: %s", root)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.EqualFold(filepath.Clean(root), filepath.Clean(home)) {
		return fmt.Errorf("refusing to bootstrap the user profile: %s", root)
	}
	return nil
}

func resolveManagedPath(root, rel string) (string, error) {
	if err := validateRelPath(rel); err != nil {
		return "", err
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	if full != root && !strings.HasPrefix(full, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("managed path escapes the project root: %q", rel)
	}
	return full, nil
}

// validateRelPath rejects empty, absolute, drive-qualified, upward-traversing
// and malformed relative paths so inspection and writes stay inside the
// project root.
func validateRelPath(rel string) error {
	if rel == "" {
		return errors.New("managed relative path must not be empty")
	}
	if strings.HasPrefix(rel, `\`) || strings.HasPrefix(rel, "/") {
		return fmt.Errorf("managed relative path must not be absolute: %q", rel)
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return fmt.Errorf("managed relative path must not be absolute or drive-qualified: %q", rel)
	}
	for _, segment := range strings.Split(strings.ReplaceAll(rel, `\`, "/"), "/") {
		switch segment {
		case "":
			return fmt.Errorf("managed relative path contains an empty segment: %q", rel)
		case ".", "..":
			return fmt.Errorf("managed relative path must not traverse upward: %q", rel)
		}
	}
	return nil
}

// atomicWrite writes data through a temporary file in the target directory
// followed by a rename, so a reader never observes a partial file. An
// existing file's permission bits are passed through mode; no other file
// attribute is changed and nothing is deleted.
func atomicWrite(full string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(full)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("cannot create parent directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(full)+".*.tmp")
	if err != nil {
		return fmt.Errorf("cannot create temporary file: %w", err)
	}
	name := temporary.Name()
	_, writeErr := temporary.Write(data)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(name)
		if writeErr != nil {
			return writeErr
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr
	}
	if err := os.Chmod(name, mode); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("cannot set file mode: %w", err)
	}
	if err := os.Rename(name, full); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("cannot replace file atomically: %w", err)
	}
	return nil
}
