// Package release builds reproducible per-target release artifacts for the
// BSL Flow CLI: cross-compiled binaries, deterministic zip archives, a
// SHA-256 release manifest and a packaging-time verification of the embedded
// inventory. All artifacts derive from one source revision and fixed
// timestamps, so identical inputs produce byte-identical outputs using only
// the go tool and no PowerShell or shell build scripts.
package release

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Target is a single cross-compilation target of the release matrix.
type Target struct {
	GOOS   string
	GOARCH string
}

// DefaultTargets returns the supported release targets in manifest order.
func DefaultTargets() []Target {
	return []Target{
		{GOOS: "windows", GOARCH: "amd64"},
		{GOOS: "darwin", GOARCH: "arm64"},
		{GOOS: "darwin", GOARCH: "amd64"},
		{GOOS: "linux", GOARCH: "amd64"},
	}
}

// ManifestTarget describes one built target inside release-manifest.json.
type ManifestTarget struct {
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	Binary        string `json:"binary"`
	BinarySHA256  string `json:"binary_sha256"`
	Archive       string `json:"archive"`
	ArchiveSHA256 string `json:"archive_sha256"`
}

// Manifest is the release manifest written next to the release archives.
type Manifest struct {
	SchemaVersion int              `json:"schema_version"`
	Version       string           `json:"version"`
	Targets       []ManifestTarget `json:"targets"`
	GeneratedFrom string           `json:"generated_from"`
}

const (
	manifestName  = "release-manifest.json"
	binaryPrefix  = "bsl-flow"
	fixedZipEpoch = 1980
)

// versionPattern mirrors the embedded bundle version contract in cli/bundle.go.
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

// Options controls one BuildAll run. Now and Build exist so tests and callers
// can pin time and substitute the compiler step; defaults use the local go
// tool and a fixed 1980-01-01 UTC timestamp.
type Options struct {
	// SourceDir is the module directory (the repo cli directory) the release
	// is generated from.
	SourceDir string
	// OutDir receives binaries, archives and release-manifest.json.
	OutDir string
	// Version is the release semver stored in the manifest.
	Version string
	// LDFlags are passed to the default go build as a single -ldflags value.
	LDFlags []string
	// TrimPath adds -trimpath to the default go build command.
	TrimPath bool
	// Env holds extra KEY=VALUE entries appended after GOOS/GOARCH/CGO_ENABLED.
	Env []string
	// Now is the fixed timestamp baked into archives; truncated to a second.
	Now time.Time
	// Build compiles one target binary. When nil, the default invokes the go
	// tool with GOOS/GOARCH/CGO_ENABLED=0, optional -trimpath and -ldflags.
	Build func(t Target, exePath string) error
	// Targets overrides the release matrix; nil means DefaultTargets.
	Targets []Target
	// VerifyDeterministic re-creates every archive and the manifest from the
	// built binaries and fails on any byte difference.
	VerifyDeterministic bool
}

// BinaryName returns the release binary file name for the target.
func BinaryName(t Target) string {
	name := binaryPrefix + "-" + t.GOOS + "-" + t.GOARCH
	if t.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// ArchiveName returns the release zip file name for the target.
func ArchiveName(t Target) string {
	return binaryPrefix + "-" + t.GOOS + "-" + t.GOARCH + ".zip"
}

// BuildAll builds every target binary, packages each into a deterministic
// archive and writes release-manifest.json into OutDir. Targets are processed
// in the given order and the manifest encoding is canonical, so two runs from
// the same source revision with the same Now and binaries are byte-identical.
func BuildAll(opts Options) (Manifest, error) {
	if err := opts.validate(); err != nil {
		return Manifest{}, fmt.Errorf("release options: %w", err)
	}
	targets := opts.Targets
	if targets == nil {
		targets = DefaultTargets()
	}
	modTime := fixedTimestamp(opts.Now)
	build := opts.Build
	if build == nil {
		build = func(t Target, exePath string) error { return goBuild(opts, t, exePath) }
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("release output directory: %w", err)
	}
	generatedFrom, err := SourceDigest(opts.SourceDir, opts.OutDir)
	if err != nil {
		return Manifest{}, fmt.Errorf("release source digest: %w", err)
	}
	manifest := Manifest{SchemaVersion: 1, Version: opts.Version, GeneratedFrom: generatedFrom}
	for _, target := range targets {
		entry, err := buildTarget(opts, build, target, modTime)
		if err != nil {
			return Manifest{}, err
		}
		manifest.Targets = append(manifest.Targets, entry)
	}
	manifestBytes, err := manifest.MarshalCanonical()
	if err != nil {
		return Manifest{}, fmt.Errorf("encode release manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(opts.OutDir, manifestName), manifestBytes, 0o644); err != nil {
		return Manifest{}, fmt.Errorf("write release manifest: %w", err)
	}
	if opts.VerifyDeterministic {
		if err := verifyDeterministic(opts, targets, manifest, modTime); err != nil {
			return Manifest{}, err
		}
	}
	return manifest, nil
}

// MarshalCanonical returns the canonical manifest bytes: fixed field order,
// no HTML escaping, LF line endings and exactly one trailing newline.
func (m Manifest) MarshalCanonical() ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(m); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// SourceDigest returns a deterministic lowercase SHA-256 identity of the
// regular files under dir: sorted slash paths paired with their content
// hashes. The .git directory and every excludeDirs subtree are skipped, so a
// build that writes into an output directory inside the source tree does not
// change the identity of the revision being released.
func SourceDigest(dir string, excludeDirs ...string) (string, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	excluded := make([]string, 0, len(excludeDirs))
	for _, item := range excludeDirs {
		if item == "" {
			continue
		}
		full, err := filepath.Abs(item)
		if err != nil {
			return "", err
		}
		excluded = append(excluded, filepath.Clean(full))
	}
	var paths []string
	err = filepath.WalkDir(root, func(full string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		if item.IsDir() {
			return nil
		}
		if !item.Type().IsRegular() || excludedPath(full, excluded) {
			return nil
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	digest := sha256.New()
	for _, rel := range paths {
		sum, err := hashFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		digest.Write([]byte(rel))
		digest.Write([]byte{0})
		digest.Write([]byte(sum))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// goBuild compiles one target binary with the local go tool. CGO is disabled
// and GOOS/GOARCH are pinned, so pure-Go cross-compilation never needs a
// platform toolchain or a network.
func goBuild(opts Options, t Target, exePath string) error {
	args := []string{"build", "-o", exePath}
	if opts.TrimPath {
		args = append(args, "-trimpath")
	}
	if len(opts.LDFlags) > 0 {
		args = append(args, "-ldflags", strings.Join(opts.LDFlags, " "))
	}
	args = append(args, ".")
	cmd := exec.Command("go", args...)
	cmd.Dir = opts.SourceDir
	cmd.Env = buildEnv(t, opts.Env)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build %s/%s: %w: %s", t.GOOS, t.GOARCH, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func buildEnv(t Target, extra []string) []string {
	env := os.Environ()
	env = append(env, "GOOS="+t.GOOS, "GOARCH="+t.GOARCH, "CGO_ENABLED=0")
	return append(env, extra...)
}

func buildTarget(opts Options, build func(Target, string) error, t Target, modTime time.Time) (ManifestTarget, error) {
	binary := BinaryName(t)
	exePath := filepath.Join(opts.OutDir, binary)
	if err := build(t, exePath); err != nil {
		return ManifestTarget{}, fmt.Errorf("build %s/%s: %w", t.GOOS, t.GOARCH, err)
	}
	binarySHA, err := hashFile(exePath)
	if err != nil {
		return ManifestTarget{}, fmt.Errorf("hash binary %s: %w", binary, err)
	}
	archive := ArchiveName(t)
	archivePath := filepath.Join(opts.OutDir, archive)
	if err := writeArchive(archivePath, exePath, binary, modTime); err != nil {
		return ManifestTarget{}, fmt.Errorf("archive %s: %w", archive, err)
	}
	archiveSHA, err := hashFile(archivePath)
	if err != nil {
		return ManifestTarget{}, fmt.Errorf("hash archive %s: %w", archive, err)
	}
	return ManifestTarget{GOOS: t.GOOS, GOARCH: t.GOARCH, Binary: binary, BinarySHA256: binarySHA, Archive: archive, ArchiveSHA256: archiveSHA}, nil
}

// verifyDeterministic re-creates every archive and the manifest bytes from
// the binaries already on disk and compares them with the stored artifacts.
// The binaries themselves are deliberately not rebuilt: the contract under
// check is that equal binaries and a fixed Now yield equal artifacts.
func verifyDeterministic(opts Options, targets []Target, manifest Manifest, modTime time.Time) error {
	for index, t := range targets {
		exePath := filepath.Join(opts.OutDir, BinaryName(t))
		rebuilt, err := hashArchive(exePath, BinaryName(t), modTime)
		if err != nil {
			return fmt.Errorf("determinism check %s: %w", ArchiveName(t), err)
		}
		if rebuilt != manifest.Targets[index].ArchiveSHA256 {
			return &NonDeterministicError{Artifact: ArchiveName(t), Want: manifest.Targets[index].ArchiveSHA256, Got: rebuilt}
		}
	}
	stored, err := os.ReadFile(filepath.Join(opts.OutDir, manifestName))
	if err != nil {
		return fmt.Errorf("determinism check manifest: %w", err)
	}
	fresh, err := manifest.MarshalCanonical()
	if err != nil {
		return fmt.Errorf("determinism check manifest: %w", err)
	}
	if !bytes.Equal(stored, fresh) {
		return &NonDeterministicError{Artifact: manifestName}
	}
	return nil
}

// NonDeterministicError reports a release artifact whose bytes are not a
// stable function of its inputs.
type NonDeterministicError struct {
	Artifact string
	Want     string
	Got      string
}

func (e *NonDeterministicError) Error() string {
	if e.Want == "" && e.Got == "" {
		return "non-deterministic release artifact " + fmt.Sprintf("%q", e.Artifact)
	}
	return fmt.Sprintf("non-deterministic release artifact %q: want sha256 %s, got %s", e.Artifact, e.Want, e.Got)
}

// fixedTimestamp pins archive timestamps: zero Now falls back to the 1980
// UTC epoch (the oldest DOS zip date), otherwise Now is normalized to UTC
// whole seconds.
func fixedTimestamp(now time.Time) time.Time {
	if now.IsZero() {
		return time.Date(fixedZipEpoch, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return now.UTC().Truncate(time.Second)
}

func excludedPath(full string, excluded []string) bool {
	for _, item := range excluded {
		if full == item || strings.HasPrefix(full, item+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func (o Options) validate() error {
	if o.SourceDir == "" {
		return errors.New("source directory is required")
	}
	info, err := os.Stat(o.SourceDir)
	if err != nil {
		return fmt.Errorf("source directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("source directory is not a directory")
	}
	if o.OutDir == "" {
		return errors.New("output directory is required")
	}
	if !versionPattern.MatchString(o.Version) || len(o.Version) > 64 {
		return fmt.Errorf("invalid release version %q", o.Version)
	}
	targets := o.Targets
	if targets == nil {
		targets = DefaultTargets()
	}
	if len(targets) == 0 {
		return errors.New("at least one release target is required")
	}
	seen := map[string]bool{}
	for _, t := range targets {
		if t.GOOS == "" || t.GOARCH == "" {
			return errors.New("target GOOS and GOARCH are required")
		}
		key := t.GOOS + "/" + t.GOARCH
		if seen[key] {
			return fmt.Errorf("duplicate release target %s", key)
		}
		seen[key] = true
	}
	return nil
}
