package worker

import (
	"context"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"bsl-flow/cli/internal/repository"
)

// This file is a self-contained worker-local port of Invoke-BFProcess
// (global/skills/1c-task/scripts/Task.Process.ps1). It intentionally
// duplicates cli/internal/stagehost/process.go: the stage host cannot be
// imported from this package boundary without inverting the dependency, and
// the worker adapters must own their spawn receipts. Keep both ports in sync
// when Invoke-BFProcess semantics change.

// ProcessOptions is the Invoke-BFProcess parameter surface. Arguments are data
// only: a []string argv passed through ArgumentList semantics, never a shell
// string.
type ProcessOptions struct {
	Executable       string
	Arguments        []string
	WorkingDirectory string
	InputText        string
	OutputDirectory  string
	// TimeoutSeconds bounds the run; zero selects the PowerShell parameter
	// default of 600 seconds.
	TimeoutSeconds int
	// Cancelled is polled while the process runs, mirroring the PS scriptblock
	// parameter; a true result stops the owned tree with stop_reason
	// "cancelled". Context cancellation is honored identically.
	Cancelled func() (bool, error)
	// Environment entries are added (or replace) the launch environment.
	Environment map[string]string
	// CleanEnvironment keeps only the trusted launch/policy inputs, never
	// arbitrary host credentials (Invoke-BFProcess -CleanEnvironment).
	CleanEnvironment bool
	// MaxOutputBytes bounds stdout.txt+stderr.txt; zero selects 16777216.
	MaxOutputBytes int64
	// Deadline is the task wall-time bound (BFRunDeadlineUtc). The zero value
	// means no additional bound beyond TimeoutSeconds.
	Deadline time.Time
}

// ProcessResult is the content of the immutable exit.json receipt. Stdout and
// Stderr are absolute paths of the retained stream files.
type ProcessResult struct {
	ExitCode       int
	StopReason     string // "", "cancelled", "timeout", "output_limit", "stdin_failure"
	ElapsedSeconds float64
	ProcessID      int
	Executable     string
	Stdout         string
	Stderr         string
}

// ExitObject renders the receipt exactly like the legacy exit.json document
// (Write-BFJson canonicalizes with key-sorted members).
func (r ProcessResult) ExitObject() map[string]any {
	result := map[string]any{
		"exit_code":       int64(r.ExitCode),
		"stop_reason":     nil,
		"elapsed_seconds": r.ElapsedSeconds,
		"process_id":      int64(r.ProcessID),
		"executable":      r.Executable,
		"stdout":          r.Stdout,
		"stderr":          r.Stderr,
	}
	if r.StopReason != "" {
		result["stop_reason"] = r.StopReason
	}
	return result
}

// isLaunchableExecutable keeps the managed-process launch boundary while
// accepting platform-native executable paths beside the historical .exe.
// Windows launches only its PATHEXT forms, so an extensionless path there is
// rejected up front; elsewhere a shebang marks an interpreter launcher the
// kernel would run instead of the file itself and stays rejected. Mirrors
// cli/internal/stagehost/process.go.
func isLaunchableExecutable(path string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	if extension != ".exe" && extension != "" {
		return false
	}
	if extension == ".exe" {
		return true
	}
	return runtime.GOOS != "windows" && !isInterpreterScript(path)
}

// isInterpreterScript reports a shebang line: the marker of an interpreter
// launcher the kernel would execute instead of the file itself.
func isInterpreterScript(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	header := make([]byte, 2)
	if _, err := io.ReadFull(file, header); err != nil {
		return false
	}
	return header[0] == '#' && header[1] == '!'
}

// RunManagedProcess launches one native executable with data-only arguments,
// streams bounded output into immutable receipt files under OutputDirectory
// (stdout.txt, stderr.txt, process.json, exit.json) and returns the terminal
// exit receipt. It never fabricates an exit code for a process that did not
// terminate: when the owned tree cannot be stopped the effect stays unknown
// and the call refuses with BF_BLOCKED ("owned process did not terminate;
// effects are unknown"), mirroring Invoke-BFProcess exactly.
func RunManagedProcess(ctx context.Context, opts ProcessOptions) (ProcessResult, error) {
	if opts.MaxOutputBytes == 0 {
		opts.MaxOutputBytes = 16777216
	}
	if opts.MaxOutputBytes < 65536 || opts.MaxOutputBytes > 16777216 {
		return ProcessResult{}, invalid("managed output bound is outside the supported range.")
	}
	if opts.TimeoutSeconds <= 0 {
		opts.TimeoutSeconds = 600
	}
	executable, err := workerSafePath(opts.Executable)
	if err != nil {
		return ProcessResult{}, err
	}
	if !isRegularFile(executable) || !isLaunchableExecutable(executable) {
		return ProcessResult{}, blocked("managed process launch requires an existing native .exe, not a shell launcher.")
	}
	workingDirectory, err := workerSafePath(opts.WorkingDirectory)
	if err != nil {
		return ProcessResult{}, err
	}
	outputDirectory, err := workerSafePath(opts.OutputDirectory)
	if err != nil {
		return ProcessResult{}, err
	}
	if err := os.MkdirAll(outputDirectory, 0o755); err != nil {
		return ProcessResult{}, blocked("%v", err)
	}
	if opts.Cancelled != nil {
		cancelled, err := opts.Cancelled()
		if err != nil {
			return ProcessResult{}, err
		}
		if cancelled {
			return ProcessResult{}, blocked("cancelled before process dispatch.")
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ProcessResult{}, blocked("cancelled before process dispatch.")
	}
	timeoutSeconds := opts.TimeoutSeconds
	if !opts.Deadline.IsZero() {
		remaining := int(math.Floor(time.Until(opts.Deadline).Seconds()))
		if remaining < timeoutSeconds {
			timeoutSeconds = remaining
		}
		if timeoutSeconds <= 0 {
			return ProcessResult{}, blocked("task deadline reached before dispatch.")
		}
	}
	for _, argument := range opts.Arguments {
		if strings.IndexByte(argument, 0) >= 0 {
			return ProcessResult{}, invalid("NUL in native argument.")
		}
	}
	stdoutPath := filepath.Join(outputDirectory, "stdout.txt")
	stderrPath := filepath.Join(outputDirectory, "stderr.txt")
	stdoutFile, err := os.OpenFile(stdoutPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return ProcessResult{}, blocked("%v", err)
	}
	stderrFile, err := os.OpenFile(stderrPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		_ = stdoutFile.Close()
		return ProcessResult{}, blocked("%v", err)
	}
	command := exec.Command(executable, opts.Arguments...)
	command.Dir = workingDirectory
	command.Env = launchEnvironment(opts)
	command.SysProcAttr = processAttributes()
	stdin, err := command.StdinPipe()
	if err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return ProcessResult{}, err
	}
	stdoutPipe, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return ProcessResult{}, err
	}
	stderrPipe, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return ProcessResult{}, err
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return ProcessResult{}, err
	}
	startedAt := time.Now()
	argumentValues := make([]any, 0, len(opts.Arguments))
	for _, argument := range opts.Arguments {
		argumentValues = append(argumentValues, argument)
	}
	argumentsHash, err := hashValue(argumentValues)
	if err != nil {
		_ = killProcessTree(command)
		_, _ = command.Process.Wait()
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return ProcessResult{}, err
	}
	if err := writeJSONFile(filepath.Join(outputDirectory, "process.json"), map[string]any{
		"pid":              int64(command.Process.Pid),
		"start_time_utc":   startTimeUTC(startedAt),
		"executable":       opts.Executable,
		"arguments_sha256": argumentsHash,
	}, false); err != nil {
		_ = killProcessTree(command)
		_, _ = command.Process.Wait()
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return ProcessResult{}, err
	}
	var streamWG sync.WaitGroup
	streamWG.Add(2)
	go func() {
		defer streamWG.Done()
		_, _ = io.Copy(stdoutFile, stdoutPipe)
	}()
	go func() {
		defer streamWG.Done()
		_, _ = io.Copy(stderrFile, stderrPipe)
	}()
	var stdinErr error
	var stdinMu sync.Mutex
	inputDone := make(chan struct{})
	go func() {
		_, writeErr := stdin.Write([]byte(opts.InputText))
		closeErr := stdin.Close()
		stdinMu.Lock()
		if writeErr != nil {
			stdinErr = writeErr
		} else {
			stdinErr = closeErr
		}
		stdinMu.Unlock()
		close(inputDone)
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	reason := ""
	deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
dispatch:
	for {
		select {
		case <-waitDone:
			break dispatch
		case <-ctx.Done():
			// Mirror the PowerShell loop shape: a process that already
			// terminated during the poll window exits cleanly even when the
			// cancellation flag flipped concurrently.
			select {
			case <-waitDone:
			default:
				if reason == "" {
					reason = "cancelled"
				}
			}
			break dispatch
		case <-time.After(200 * time.Millisecond):
		}
		select {
		case <-inputDone:
			stdinMu.Lock()
			failed := stdinErr != nil
			stdinMu.Unlock()
			if failed && reason == "" {
				reason = "stdin_failure"
			}
		default:
		}
		if reason != "" {
			break dispatch
		}
		if opts.Cancelled != nil {
			cancelled, cancelErr := opts.Cancelled()
			if cancelErr != nil {
				_ = killProcessTree(command)
				_ = <-waitDone
				_ = stdoutFile.Close()
				_ = stderrFile.Close()
				return ProcessResult{}, cancelErr
			}
			if cancelled {
				reason = "cancelled"
			}
		}
		if reason == "" && !time.Now().Before(deadline) {
			reason = "timeout"
		}
		if reason == "" {
			stdoutSize, _ := fileLength(stdoutFile)
			stderrSize, _ := fileLength(stderrFile)
			if stdoutSize+stderrSize > opts.MaxOutputBytes {
				reason = "output_limit"
			}
		}
		if reason != "" {
			break dispatch
		}
	}
	if reason != "" {
		if err := killProcessTree(command); err != nil {
			_ = stdoutFile.Close()
			_ = stderrFile.Close()
			return ProcessResult{}, blocked("owned process did not terminate; effects are unknown.")
		}
		select {
		case <-waitDone:
		case <-time.After(3 * time.Second):
			_ = stdoutFile.Close()
			_ = stderrFile.Close()
			return ProcessResult{}, blocked("owned process did not terminate; effects are unknown.")
		}
	}
	streamsClosed := make(chan struct{})
	go func() {
		streamWG.Wait()
		close(streamsClosed)
	}()
	select {
	case <-streamsClosed:
	case <-time.After(2 * time.Second):
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return ProcessResult{}, blocked("subprocess output still open; reconcile the owned process tree.")
	}
	<-inputDone
	_ = stdoutFile.Sync()
	_ = stderrFile.Sync()
	_ = stdoutFile.Close()
	_ = stderrFile.Close()
	if reason == "" {
		stdoutSize, _ := fileSize(stdoutPath)
		stderrSize, _ := fileSize(stderrPath)
		if stdoutSize+stderrSize > opts.MaxOutputBytes {
			reason = "output_limit"
		}
	}
	exitCode := 0
	if command.ProcessState != nil {
		exitCode = command.ProcessState.ExitCode()
	}
	elapsed := math.RoundToEven(time.Since(startedAt).Seconds()*1000) / 1000
	result := ProcessResult{
		ExitCode:       exitCode,
		StopReason:     reason,
		ElapsedSeconds: elapsed,
		ProcessID:      command.Process.Pid,
		Executable:     opts.Executable,
		Stdout:         stdoutPath,
		Stderr:         stderrPath,
	}
	if err := writeJSONFile(filepath.Join(outputDirectory, "exit.json"), result.ExitObject(), false); err != nil {
		return ProcessResult{}, err
	}
	return result, nil
}

// launchEnvironment keeps exactly the trusted launch inputs under
// CleanEnvironment (the Invoke-BFProcess allowlist) and applies the explicit
// Environment entries on top.
func launchEnvironment(opts ProcessOptions) []string {
	entries := map[string]string{}
	if !opts.CleanEnvironment {
		for _, item := range os.Environ() {
			if key, _, found := strings.Cut(item, "="); found {
				entries[key] = item
			}
		}
	} else {
		for _, name := range cleanEnvironmentNames {
			if value, ok := os.LookupEnv(name); ok {
				entries[name] = name + "=" + value
			}
		}
	}
	for name, value := range opts.Environment {
		entries[name] = name + "=" + value
	}
	result := make([]string, 0, len(entries))
	for _, item := range entries {
		result = append(result, item)
	}
	return result
}

var cleanEnvironmentNames = []string{
	"SystemRoot", "WINDIR", "SystemDrive", "COMSPEC", "PATH", "PATHEXT",
	"USERPROFILE", "APPDATA", "LOCALAPPDATA", "ProgramFiles", "ProgramFiles(x86)",
	"ProgramW6432", "ProgramData", "CommonProgramFiles", "CommonProgramFiles(x86)",
	"CommonProgramW6432", "COMPUTERNAME", "USERNAME", "USERDOMAIN",
	"HOMEDRIVE", "HOMEPATH", "OS", "PROCESSOR_ARCHITECTURE",
	"NUMBER_OF_PROCESSORS", "__PSLockDownPolicy",
}

func workerSafePath(path string) (string, error) {
	resolved, err := repository.SafePath(path)
	if err != nil {
		return "", invalid("%v", err)
	}
	return resolved, nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func fileLength(file *os.File) (int64, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func isRegularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// startTimeUTC formats a process start like .NET's round-trip "o" specifier
// (seven fractional digits, always Z-suffixed for UTC times).
func startTimeUTC(moment time.Time) string {
	return moment.UTC().Format("2006-01-02T15:04:05.0000000Z")
}
