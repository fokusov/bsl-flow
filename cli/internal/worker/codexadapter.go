package worker

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// Port of global/skills/1c-task/adapters/Codex.ps1: the trusted
// worker-configuration guard, the bsl_flow sandbox permission profile and the
// host capability probe. Behavior and diagnostics are byte-exact ports.

// AssertWorkerConfiguration mirrors Assert-BFWorkerConfiguration
// (Codex.ps1:12-23): project-controlled execution configuration must never be
// loaded by the managed adapter, so any .codex configuration file from the
// worker root up to the drive root is a BF_BLOCKED refusal.
func AssertWorkerConfiguration(workerPath string) error {
	dir := workerPath
	for dir != "" {
		for _, relative := range []string{".codex/config.toml", ".codex/hooks.json", ".codex/config.json"} {
			candidate := filepath.Join(dir, relative)
			if _, err := os.Lstat(candidate); err == nil {
				return blocked("managed adapter does not load project execution configuration: %s", candidate)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil
}

// PermissionProfile mirrors Get-BFPermissionProfile (Codex.ps1:4-10): the
// sealed bsl_flow sandbox permission string. The path is serialized as a TOML
// basic string; JSON escaping is valid for this restricted value.
func PermissionProfile(workerPath string, writable bool) string {
	path := jsonQuote(forwardSlash(workerPath))
	access := "read"
	if writable {
		access = "write"
	}
	return `permissions.bsl_flow={filesystem={":root"="read",` + path + `="` + access + `"},network={enabled=false}}`
}

// CodexCapability is the receipt of TestCodexCapability (the return shape of
// Test-BFCodexCapability, Codex.ps1:48).
type CodexCapability struct {
	Version          string
	ExecutableSHA256 string
	SourceWrite      bool
	ControllerWrite  bool
	CheckedAt        string
}

// ExitObject renders the receipt as the canonical JSON map the controller
// persists.
func (c CodexCapability) ExitObject() map[string]any {
	return map[string]any{
		"version":           c.Version,
		"executable_sha256": c.ExecutableSHA256,
		"source_write":      c.SourceWrite,
		"controller_write":  c.ControllerWrite,
		"checked_at":        c.CheckedAt,
	}
}

// CodexCapabilityOptions is the Test-BFCodexCapability parameter surface. The
// PowerShell probe runs pwsh.exe from $PSHOME inside the sandbox; the native
// port receives the exact interpreter path instead of discovering one.
type CodexCapabilityOptions struct {
	CodexPath  string
	WorkerPath string
	Directory  string
	PwshPath   string
	Now        func() string // checked_at; defaults to the round-trip UTC format
}

var codexVerifiedVersions = []string{"codex-cli 0.153.0", "codex-cli 0.154.0"}

// TestCodexCapability mirrors Test-BFCodexCapability (Codex.ps1:25-49): the
// host version gate and the read/write sandbox probes. A capability that was
// not demonstrated exactly is a BF_BLOCKED refusal — never a weakened launch.
func TestCodexCapability(ctx context.Context, opts CodexCapabilityOptions) (CodexCapability, error) {
	if err := AssertWorkerConfiguration(opts.WorkerPath); err != nil {
		return CodexCapability{}, err
	}
	if err := os.MkdirAll(opts.Directory, 0o755); err != nil {
		return CodexCapability{}, blocked("%v", err)
	}
	version, err := RunManagedProcess(ctx, ProcessOptions{
		Executable:       opts.CodexPath,
		Arguments:        []string{"--version"},
		WorkingDirectory: opts.WorkerPath,
		OutputDirectory:  filepath.Join(opts.Directory, "version"),
		TimeoutSeconds:   30,
	})
	if err != nil {
		return CodexCapability{}, err
	}
	versionText := strings.TrimSpace(readAllText(version.Stdout))
	verified := false
	for _, candidate := range codexVerifiedVersions {
		if versionText == candidate {
			verified = true
			break
		}
	}
	if version.ExitCode != 0 || !verified {
		return CodexCapability{}, blocked("unverified Codex host version: %s. Run and review the host capability suite before supporting it.", versionText)
	}
	sentinel := filepath.Join(opts.Directory, "controller-sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("controller"), 0o644); err != nil {
		return CodexCapability{}, blocked("%v", err)
	}
	probeRoot := filepath.Join(opts.WorkerPath, ".bsl-flow-worker", "capability")
	if _, err := workerSafePath(probeRoot); err != nil {
		return CodexCapability{}, err
	}
	if err := os.MkdirAll(probeRoot, 0o755); err != nil {
		return CodexCapability{}, blocked("%v", err)
	}
	guid, err := guidN()
	if err != nil {
		return CodexCapability{}, invalid("%v", err)
	}
	allowed := filepath.Join(probeRoot, "write-"+guid+".txt")
	script := `$ErrorActionPreference="Stop"; $a="denied"; $b="denied"; try {Set-Content -LiteralPath ` +
		quotePowerShellString(allowed) +
		` -Value "probe" -ErrorAction Stop; $a="allowed"} catch [System.UnauthorizedAccessException] {}; try {Set-Content -LiteralPath ` +
		quotePowerShellString(sentinel) +
		` -Value "tampered" -ErrorAction Stop; $b="allowed"} catch [System.UnauthorizedAccessException] {}; Write-Output ($a+":"+$b)`
	encoded := base64.StdEncoding.EncodeToString(utf16LEBytes(script))
	for _, write := range []bool{false, true} {
		name := "read"
		if write {
			name = "write"
		}
		process, err := RunManagedProcess(ctx, ProcessOptions{
			Executable: opts.CodexPath,
			Arguments: []string{
				"sandbox", "-P", "bsl_flow",
				"-c", PermissionProfile(opts.WorkerPath, write),
				"-c", `windows.sandbox="unelevated"`,
				"-C", opts.WorkerPath,
				opts.PwshPath, "-NoProfile", "-EncodedCommand", encoded,
			},
			WorkingDirectory: opts.WorkerPath,
			OutputDirectory:  filepath.Join(opts.Directory, name),
			TimeoutSeconds:   60,
		})
		if err != nil {
			return CodexCapability{}, err
		}
		expected := "denied:denied"
		if write {
			expected = "allowed:denied"
		}
		if process.ExitCode != 0 || strings.TrimSpace(readAllText(process.Stdout)) != expected ||
			readAllText(sentinel) != "controller" {
			return CodexCapability{}, blocked("%s sandbox capability was not demonstrated.", name)
		}
	}
	executableHash, err := fileHash(opts.CodexPath)
	if err != nil {
		return CodexCapability{}, err
	}
	checkedAt := roundTripUTCTime(nowTime())
	if opts.Now != nil {
		checkedAt = opts.Now()
	}
	return CodexCapability{
		Version:          versionText,
		ExecutableSHA256: executableHash,
		SourceWrite:      true,
		ControllerWrite:  false,
		CheckedAt:        checkedAt,
	}, nil
}

// quotePowerShellString mirrors the single-quote PS escaping of the probe
// script builder (” for embedded single quotes).
func quotePowerShellString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func utf16LEBytes(text string) []byte {
	// [Text.Encoding]::Unicode.GetBytes: UTF-16LE with surrogate pairs for
	// astral characters; unpaired surrogates in Go strings are replaced like
	// the .NET encoder does.
	encoded := utf16.Encode([]rune(text))
	buffer := make([]byte, 0, len(encoded)*2)
	for _, unit := range encoded {
		buffer = append(buffer, byte(unit), byte(unit>>8))
	}
	return buffer
}

func readAllText(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func nowUTCFunc(supplier func() string) func() string {
	if supplier != nil {
		return supplier
	}
	return func() string { return roundTripUTCTime(nowTime()) }
}
