package stagehost

import (
	"context"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// runManagedProcess is the Go port of Invoke-BFProcess (Task.Process.ps1):
// one native executable, data-only arguments, bounded streamed output and
// immutable process/exit receipts under the output directory.
func runManagedProcess(ctx context.Context, opts ProcessOptions) (ProcessResult, error) {
	if opts.MaxOutputBytes == 0 {
		opts.MaxOutputBytes = 16777216
	}
	if opts.MaxOutputBytes < 65536 || opts.MaxOutputBytes > 16777216 {
		return ProcessResult{}, invalidf("managed output bound is outside the supported range.")
	}
	executable, err := safePath(opts.Executable)
	if err != nil {
		return ProcessResult{}, err
	}
	if !isRegularFile(executable) || pathExtension(executable) != ".exe" {
		return ProcessResult{}, blockedf("managed process launch requires an existing native .exe, not a shell launcher.")
	}
	workingDirectory, err := safePath(opts.WorkingDirectory)
	if err != nil {
		return ProcessResult{}, err
	}
	outputDirectory, err := safePath(opts.OutputDirectory)
	if err != nil {
		return ProcessResult{}, err
	}
	if err := os.MkdirAll(outputDirectory, 0o755); err != nil {
		return ProcessResult{}, blockedf("%v", err)
	}
	if opts.Cancelled != nil {
		cancelled, err := opts.Cancelled()
		if err != nil {
			return ProcessResult{}, err
		}
		if cancelled {
			return ProcessResult{}, blockedf("cancelled before process dispatch.")
		}
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return ProcessResult{}, blockedf("cancelled before process dispatch.")
		}
	} else {
		ctx = context.Background()
	}
	timeoutSeconds := opts.TimeoutSeconds
	if !opts.Deadline.IsZero() {
		remaining := int(math.Floor(time.Until(opts.Deadline).Seconds()))
		if remaining < timeoutSeconds {
			timeoutSeconds = remaining
		}
		if timeoutSeconds <= 0 {
			return ProcessResult{}, blockedf("task deadline reached before dispatch.")
		}
	}
	for _, argument := range opts.Arguments {
		if strings.IndexByte(argument, 0) >= 0 {
			return ProcessResult{}, invalidf("NUL in native argument.")
		}
	}
	stdoutPath := filepath.Join(outputDirectory, "stdout.txt")
	stderrPath := filepath.Join(outputDirectory, "stderr.txt")
	stdoutFile, err := os.OpenFile(stdoutPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return ProcessResult{}, blockedf("%v", err)
	}
	stderrFile, err := os.OpenFile(stderrPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		_ = stdoutFile.Close()
		return ProcessResult{}, blockedf("%v", err)
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
	if err := writeJSON(filepath.Join(outputDirectory, "process.json"), map[string]any{
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
		case err := <-waitDone:
			_ = err
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
		if reason == "" && opts.Cancelled != nil {
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
			return ProcessResult{}, blockedf("owned process did not terminate; effects are unknown.")
		}
		select {
		case <-waitDone:
		case <-time.After(3 * time.Second):
			_ = stdoutFile.Close()
			_ = stderrFile.Close()
			return ProcessResult{}, blockedf("owned process did not terminate; effects are unknown.")
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
		return ProcessResult{}, blockedf("subprocess output still open; reconcile the owned process tree.")
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
	if err := writeJSON(filepath.Join(outputDirectory, "exit.json"), result.ExitObject(), false); err != nil {
		return ProcessResult{}, err
	}
	return result, nil
}

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
