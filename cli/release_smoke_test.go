package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"bsl-flow/cli/internal/release"
)

// TestReleaseCleanInstallSmoke is the Windows-scope requirement-22
// clean-install smoke: it drives the real packaging tool (cmd/bslflow-release,
// the same PS-free lane CI ships), extracts the windows/amd64 release archive
// with pure Go, and exercises the extracted bsl-flow.exe end to end on a
// throwaway project.
//
// The smoke itself never invokes PowerShell: every step is either a Go
// function call or a direct exec of the release tool / the extracted binary
// with data-only argv. The extracted binary is the exact release artifact
// built from this source revision, not a developer build, so help/version/
// capability, project bootstrap and task create/list here are the
// clean-machine install behavior requirement 22 asks for.
//
// The test compiles real binaries and is skipped under -short; it still runs
// in the default suite. Only the windows/amd64 target is built (use the
// -targets flag added to bslflow-release) to keep the runtime bounded, and
// execution of the extracted binary requires a Windows host — other hosts
// still verify the packaging and archive inventory.
func TestReleaseCleanInstallSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("clean-install smoke compiles real binaries; skipped under -short")
	}
	if available, reason := release.CanCrossCompile(); !available {
		t.Skipf("clean-install smoke needs the go toolchain: %s", reason)
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the cli module directory")
	}
	cliRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile)))
	base := t.TempDir()

	// Step 1: build the release tool itself with the go tool.
	releaseTool := filepath.Join(base, "bslflow-release-test")
	if runtime.GOOS == "windows" {
		releaseTool += ".exe"
	}
	buildReleaseSmokeTool(t, cliRoot, "./cmd/bslflow-release", releaseTool)

	// Step 2: produce the windows/amd64 release archive in a temp directory.
	releaseDir := filepath.Join(base, "release")
	manifest := runReleaseSmokeTool(t, releaseTool, cliRoot, releaseDir)
	if len(manifest.Targets) != 1 {
		t.Fatalf("smoke release built %d targets, want exactly windows/amd64", len(manifest.Targets))
	}
	target := manifest.Targets[0]
	if target.GOOS != "windows" || target.GOARCH != "amd64" {
		t.Fatalf("smoke release target is %s/%s, want windows/amd64", target.GOOS, target.GOARCH)
	}
	archivePath := filepath.Join(releaseDir, target.Archive)
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("release archive missing: %v", err)
	}
	archiveSum := sha256.Sum256(archiveBytes)
	if got := hex.EncodeToString(archiveSum[:]); got != target.ArchiveSHA256 {
		t.Fatalf("release archive hash %s does not match the manifest %s", got, target.ArchiveSHA256)
	}

	// Step 3: extract the archive into a fresh install directory (pure Go).
	installDir := filepath.Join(base, "install")
	extractReleaseArchive(t, archiveBytes, installDir)
	extracted := filepath.Join(installDir, target.Binary)
	if info, err := os.Stat(extracted); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("extracted binary is missing or not a regular file: %v", err)
	}

	// Steps 4: run the extracted binary. Only a Windows host can execute the
	// windows/amd64 artifact; packaging and inventory are verified everywhere.
	if runtime.GOOS != "windows" {
		t.Skipf("extracted %s/%s binary cannot execute on %s; packaging verified only", target.GOOS, target.GOARCH, runtime.GOOS)
	}
	stdout := runSmokeBinary(t, extracted, nil, "help")
	if !strings.Contains(stdout, "bsl-flow task") {
		t.Fatalf("help output lost the command list: %q", stdout)
	}
	versionDoc := runSmokeJSON(t, extracted, nil, "version").(map[string]any)
	version, _ := versionDoc["version"].(string)
	if versionDoc["package"] != "bsl-flow" || strings.TrimSpace(version) == "" {
		t.Fatalf("version document is not a packaged bsl-flow identity: %#v", versionDoc)
	}
	capabilityDoc := runSmokeJSON(t, extracted, nil, "capability").(map[string]any)
	if len(capabilityDoc) == 0 {
		t.Fatal("capability document is empty")
	}

	// Step 5: project bootstrap on a temp git-initialized project.
	project := filepath.Join(base, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	smokeGit(t, project, "init")
	writeSmokeFile(t, filepath.Join(project, "readme.txt"), []byte("clean install smoke\n"))
	smokeGit(t, project, "add", ".")
	smokeGit(t, project, "commit", "-m", "clean install smoke")
	stdout = runSmokeBinary(t, extracted, nil, "init", "--project", project, "--explicit-1c-project")
	if !strings.Contains(stdout, "BSL Flow project bootstrap complete") {
		t.Fatalf("init did not complete: %q", stdout)
	}
	// The managed file set owned by native init (bootstrap.go managedFiles);
	// git init and openspec scaffolding stay manual per the bootstrap contract.
	for _, managed := range []string{
		"AGENTS.md", "bsl-flow.yaml", ".gitignore",
		".bsl-flow/project.yaml", ".bsl-flow/reports/.gitkeep", ".bsl-flow/evidence/.gitkeep",
	} {
		if _, err := os.Stat(filepath.Join(project, filepath.FromSlash(managed))); err != nil {
			t.Fatalf("init did not create managed file %s: %v", managed, err)
		}
	}
	stdout = runSmokeBinary(t, extracted, nil, "init", "--project", project, "--explicit-1c-project")
	if !strings.Contains(stdout, "up_to_date") {
		t.Fatalf("second init is not up_to_date: %q", stdout)
	}

	// Step 6: task create/list in the bootstrapped project.
	inputPath := filepath.Join(base, "create.json")
	writeSmokeFile(t, inputPath, []byte(`{"schema_version":1,"title":"clean install smoke task"}`+"\n"))
	stdout = runSmokeBinary(t, extracted, nil, "task", "create", "--project", project, "--input", inputPath)
	var created struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &created); err != nil || created.TaskID == "" {
		t.Fatalf("task create output has no task_id: %q (err %v)", stdout, err)
	}
	stdout = runSmokeBinary(t, extracted, nil, "task", "list", "--project", project)
	if !strings.Contains(stdout, created.TaskID) {
		t.Fatalf("task list lost the created task %s: %q", created.TaskID, stdout)
	}
}

// buildReleaseSmokeTool compiles one tool of this module with the local go
// tool for the host platform.
func buildReleaseSmokeTool(t *testing.T, dir, packagePath, output string) {
	t.Helper()
	command := exec.Command("go", "build", "-o", output, packagePath)
	command.Dir = dir
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", packagePath, err, out)
	}
}

// runReleaseSmokeTool runs the built release tool for the windows/amd64
// target and returns the decoded manifest it printed.
func runReleaseSmokeTool(t *testing.T, tool, sourceDir, outDir string) release.Manifest {
	t.Helper()
	command := exec.Command(tool, "-source", sourceDir, "-out", outDir, "-version", "0.0.0-smoke", "-targets", "windows/amd64")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("bslflow-release: %v\n%s%s", err, stderr.String(), stdout.String())
	}
	var manifest release.Manifest
	if err := json.Unmarshal(stdout.Bytes(), &manifest); err != nil {
		t.Fatalf("release tool output is not a manifest: %v\n%s", err, stdout.String())
	}
	return manifest
}

// extractReleaseArchive unpacks the release zip with the standard library,
// refusing any archive entry that carries PowerShell artifacts.
func extractReleaseArchive(t *testing.T, data []byte, destination string) {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("release archive is not a zip: %v", err)
	}
	for _, entry := range reader.File {
		name := strings.ToLower(entry.Name)
		if strings.Contains(name, ".ps1") || strings.Contains(name, "pwsh") || strings.Contains(name, "powershell") {
			t.Fatalf("release archive carries a PowerShell artifact: %q", entry.Name)
		}
		clean := filepath.Clean(filepath.Join(destination, entry.Name))
		if !strings.HasPrefix(clean, filepath.Clean(destination)+string(os.PathSeparator)) {
			t.Fatalf("release archive entry escapes the install directory: %q", entry.Name)
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(clean, 0o700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(clean), 0o700); err != nil {
			t.Fatal(err)
		}
		source, err := entry.Open()
		if err != nil {
			t.Fatalf("cannot open archive entry %q: %v", entry.Name, err)
		}
		extracted, err := os.OpenFile(clean, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
		if err != nil {
			_ = source.Close()
			t.Fatalf("cannot extract archive entry %q: %v", entry.Name, err)
		}
		_, copyErr := io.Copy(extracted, source)
		closeErr := extracted.Close()
		_ = source.Close()
		if copyErr != nil {
			t.Fatalf("cannot extract archive entry %q: %v", entry.Name, copyErr)
		}
		if closeErr != nil {
			t.Fatalf("cannot extract archive entry %q: %v", entry.Name, closeErr)
		}
	}
}

// runSmokeBinary executes the extracted release binary and requires exit 0.
func runSmokeBinary(t *testing.T, executable string, extraEnv []string, args ...string) string {
	t.Helper()
	command := exec.Command(executable, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	command.Env = append(os.Environ(), extraEnv...)
	if err := command.Run(); err != nil {
		t.Fatalf("bsl-flow %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// runSmokeJSON executes the extracted binary and decodes its JSON document.
func runSmokeJSON(t *testing.T, executable string, extraEnv []string, args ...string) any {
	t.Helper()
	output := runSmokeBinary(t, executable, extraEnv, args...)
	var document any
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &document); err != nil {
		t.Fatalf("bsl-flow %s did not print JSON: %v\n%s", strings.Join(args, " "), err, output)
	}
	return document
}

func smokeGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=BSL Flow Smoke", "GIT_AUTHOR_EMAIL=smoke@example.invalid",
		"GIT_COMMITTER_NAME=BSL Flow Smoke", "GIT_COMMITTER_EMAIL=smoke@example.invalid",
	)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeSmokeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
