package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DispatchRequest is one claimed-task controller invocation. The PowerShell
// runner calls Invoke-BFRun / Resume-BFAttempt in-process; the native loop
// re-invokes this same trusted CLI binary with the equivalent public action
// and argv-as-data, never a shell string.
type DispatchRequest struct {
	Project string
	Task    TaskID
	// Action is ActionRun or ActionResumeReadonly.
	Action Action
	// CodexPath is forwarded to run dispatches only; the resume_readonly
	// path of the source runner passes no executable.
	CodexPath string
	// RuntimeAuthLine is the private single-line JSON credential forwarded
	// to the child over stdin (--runtime-auth stdin); empty disables it.
	RuntimeAuthLine string
}

// Dispatcher executes one claimed-task controller invocation.
type Dispatcher interface {
	Dispatch(ctx context.Context, req DispatchRequest, out, errOut io.Writer) error
}

// SelfDispatch spawns the trusted binary (os.Executable) for one claimed
// task, mirroring the spawn contract used across the CLI: data-only argv,
// bounded combined output, tree-kill on cancellation, and no PowerShell
// fallback ever. The child enforces its own stage and task wall-time limits
// exactly like the in-process controller of the source runner.
type SelfDispatch struct {
	// Executable overrides the trusted binary path; empty selects
	// os.Executable(). Tests inject a stub script here.
	Executable string
	// MaxOutputBytes bounds the combined child output passed through to the
	// serve writers; zero selects 16777216.
	MaxOutputBytes int64
	// Timeout optionally bounds one dispatch. Zero applies no supervisor-side
	// bound beyond the context, matching the in-process source runner.
	Timeout time.Duration
}

// dispatchArguments builds the child argv from the request.
func dispatchArguments(req DispatchRequest) []string {
	arguments := []string{"task", "run", "--project", req.Project, "--task", string(req.Task)}
	if req.Action == ActionResumeReadonly {
		arguments = []string{"task", "resume", "--project", req.Project, "--task", string(req.Task)}
	} else if req.CodexPath != "" {
		arguments = append(arguments, "--codex", req.CodexPath)
	}
	if req.RuntimeAuthLine != "" {
		arguments = append(arguments, "--runtime-auth", "stdin")
	}
	return arguments
}

func (d SelfDispatch) Dispatch(ctx context.Context, req DispatchRequest, out, errOut io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return blocked("cancelled before dispatch.")
	}
	limit := d.MaxOutputBytes
	if limit == 0 {
		limit = 16777216
	}
	if limit < 65536 || limit > 16777216 {
		return invalid("managed output bound is outside the supported range.")
	}
	executable := d.Executable
	if executable == "" {
		self, err := os.Executable()
		if err != nil {
			return invalid("trusted dispatch executable: %v", err)
		}
		if executable, err = filepath.Abs(self); err != nil {
			return invalid("trusted dispatch executable: %v", err)
		}
	}
	info, err := os.Lstat(executable)
	if err != nil || !info.Mode().IsRegular() {
		return blocked("trusted dispatch executable is missing or not a regular file.")
	}
	runCtx := ctx
	cancel := func() {}
	if d.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, d.Timeout)
	}
	defer cancel()
	arguments := dispatchArguments(req)
	for _, argument := range arguments {
		if strings.IndexByte(argument, 0) >= 0 {
			return invalid("NUL in native argument.")
		}
	}
	command := exec.Command(executable, arguments...)
	command.SysProcAttr = processAttributes()
	// Do not inherit a caller-supplied host identity, even with different
	// casing; the trusted parent identity is this same binary.
	for _, item := range os.Environ() {
		if !strings.EqualFold(strings.SplitN(item, "=", 2)[0], "BSL_FLOW_HOST_PATH") {
			command.Env = append(command.Env, item)
		}
	}
	command.Env = append(command.Env, "BSL_FLOW_HOST_PATH="+executable)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return err
	}
	var stdin io.WriteCloser
	if req.RuntimeAuthLine != "" {
		if stdin, err = command.StdinPipe(); err != nil {
			return err
		}
	}
	if err := command.Start(); err != nil {
		return blocked("trusted task controller did not start: %v", err)
	}
	shared := &sharedLimit{remaining: limit}
	var streams sync.WaitGroup
	streams.Add(2)
	go func() {
		defer streams.Done()
		_, _ = io.Copy(&limitWriter{w: out, shared: shared}, stdout)
	}()
	go func() {
		defer streams.Done()
		_, _ = io.Copy(&limitWriter{w: errOut, shared: shared}, stderr)
	}()
	if stdin != nil {
		go func() {
			_, _ = io.WriteString(stdin, req.RuntimeAuthLine+"\n")
			_ = stdin.Close()
		}()
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case waitErr := <-waitDone:
		streams.Wait()
		return classifyDispatchExit(waitErr)
	case <-runCtx.Done():
		if err := killProcessTree(command); err != nil {
			return blocked("owned process did not terminate; effects are unknown.")
		}
		select {
		case <-waitDone:
		case <-time.After(3 * time.Second):
			return blocked("owned process did not terminate; effects are unknown.")
		}
		streams.Wait()
		return blocked("dispatch stopped (%v); effects are unknown.", runCtx.Err())
	}
}

// classifyDispatchExit maps the child's terminal receipt to the runner's
// dispatch error contract. Status-mapped exit codes (0 completed, 10
// needs_input, 11 blocked, 12 failed, 13 cancelled) are normal controller
// outcomes observed through task state; only infrastructure failures
// (BF_INVALID, BF_CONFLICT, unknown crashes) are dispatch errors whose
// effects are unknown.
func classifyDispatchExit(waitErr error) error {
	if waitErr == nil {
		return nil
	}
	var exit *exec.ExitError
	if !errors.As(waitErr, &exit) {
		return blocked("trusted task controller could not be observed: %v", waitErr)
	}
	switch exit.ExitCode() {
	case 2:
		return invalid("trusted task controller rejected the dispatch: %s", strings.TrimSpace(string(exit.Stderr)))
	case 3:
		return conflict("trusted task controller reported a conflict: %s", strings.TrimSpace(string(exit.Stderr)))
	case 4:
		return blocked("trusted task controller failed: %s", strings.TrimSpace(string(exit.Stderr)))
	default:
		return nil
	}
}

// sharedLimit bounds the combined child output of one dispatch.
type sharedLimit struct {
	mu        sync.Mutex
	remaining int64
}

// limitWriter forwards output while the shared budget lasts and silently
// drops the rest; child output is diagnostic passthrough, never a receipt.
type limitWriter struct {
	w      io.Writer
	shared *sharedLimit
}

func (l *limitWriter) Write(p []byte) (int, error) {
	l.shared.mu.Lock()
	budget := l.shared.remaining
	l.shared.mu.Unlock()
	if budget > 0 {
		allowed := p
		if int64(len(allowed)) > budget {
			allowed = allowed[:budget]
		}
		_, _ = l.w.Write(allowed)
		l.shared.mu.Lock()
		l.shared.remaining -= int64(len(allowed))
		l.shared.mu.Unlock()
	}
	return len(p), nil
}

// defaultGitRoot resolves the Git worktree root like Invoke-BFGit: the exact
// supervisor project root check of Invoke-BFTaskQueue.
func defaultGitRoot(ctx context.Context, project string) (string, error) {
	command := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=NUL", "-c", "core.fsmonitor=false", "-C", project, "rev-parse", "--show-toplevel")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	command.SysProcAttr = processAttributes()
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(strings.Join([]string{stderr.String(), stdout.String()}, "\n"))
		return "", blocked("Git failed: %s", detail)
	}
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}
