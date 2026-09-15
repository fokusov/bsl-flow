package delivery

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// gitInvocation is one hardened git subprocess. The legacy publication runner
// always spawned git with argv-as-data, a scrubbed environment, an empty
// working directory, bounded output capture and a per-call timeout; this type
// carries the per-call slice of those rules. operation names the receipt
// operation and label names the assertion label used in BF_* messages.
type gitInvocation struct {
	operation   string
	label       string
	arguments   []string
	input       []byte
	transport   string // "none", "file" or "https"
	auth        string // "none" or "github_cli"
	environment map[string]string
	// keepAlive marks the single dispatched push: it is never interrupted, a
	// watchdog only gives up waiting and the effect stays unknown.
	keepAlive bool
}

// gitResult captures one git subprocess outcome. completed with exit code
// zero is the only success shape; stderr is already redacted.
type gitResult struct {
	completed  bool
	exitCode   int
	stdout     []byte
	stderr     string
	stopReason string // "timeout" or "output_limit" when the call was bounded
}

// scrubbedEnvPattern is the exact strip list of the legacy runner: ambient
// Git, SSH, GitHub CLI and proxy/transport trust variables never reach the
// publication subprocess.
var scrubbedEnvPattern = regexp.MustCompile(`^(?i:(GIT_|SSH_|GH_|GITHUB_|HTTP_PROXY$|HTTPS_PROXY$|ALL_PROXY$|NO_PROXY$|CURL_|SSL_CERT_))`)

// gitEnvironmentOverrideKeys is the closed set of per-call environment keys
// the legacy runner admitted for index and commit identity control.
var gitEnvironmentOverrideKeys = map[string]bool{
	"GIT_INDEX_FILE":      true,
	"GIT_AUTHOR_NAME":     true,
	"GIT_AUTHOR_EMAIL":    true,
	"GIT_AUTHOR_DATE":     true,
	"GIT_COMMITTER_NAME":  true,
	"GIT_COMMITTER_EMAIL": true,
	"GIT_COMMITTER_DATE":  true,
}

// userinfoURLPattern redacts userinfo in diagnostic URLs as defense in depth.
var userinfoURLPattern = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`)

func protectDiagnostic(text string) string {
	return userinfoURLPattern.ReplaceAllString(text, "${1}[redacted]@")
}

// baseArguments replicates the legacy hardened profile: no replace objects,
// long paths, disabled hooks/fsmonitor/CRLF/GPG/redirects, an empty attributes
// file, no ambient credential helper and — for the github_cli profile — the
// pinned GitHub CLI credential helper.
func (p *CLIGitPort) baseArguments(auth string) []string {
	arguments := []string{
		"--no-replace-objects",
		"-c", "core.longpaths=true",
		"-c", "core.hooksPath=" + p.emptyHooks,
		"-c", "core.fsmonitor=false",
		"-c", "core.autocrlf=false",
		"-c", "core.safecrlf=false",
		"-c", "core.attributesFile=" + p.emptyAttributes,
		"-c", "commit.gpgSign=false",
		"-c", "tag.gpgSign=false",
		"-c", "credential.helper=",
		"-c", "http.followRedirects=false",
	}
	if auth == "github_cli" {
		arguments = append(arguments, "-c", "credential.helper=!\""+filepath.ToSlash(p.gitHubCLI)+"\" auth git-credential")
	}
	return arguments
}

// environment builds the child environment the way the legacy runner did:
// the ambient environment minus every scrubbed key (and the host policy
// variable this CLI injects into its own children), plus the pinned Git
// isolation variables and the sorted per-call overrides.
func (p *CLIGitPort) environment(transport string, overrides map[string]string) ([]string, error) {
	for key := range overrides {
		if !gitEnvironmentOverrideKeys[key] {
			return nil, invalid("unsupported publication environment key.")
		}
	}
	environment := make([]string, 0, len(os.Environ())+len(overrides)+8)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if scrubbedEnvPattern.MatchString(key) || strings.EqualFold(key, "BSL_FLOW_HOST_PATH") {
			continue
		}
		environment = append(environment, item)
	}
	allowProtocol := "file"
	if transport == "https" {
		allowProtocol = "https"
	}
	environment = append(environment,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+p.emptyConfig,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_NO_LAZY_FETCH=1",
		"GCM_INTERACTIVE=never",
		"GH_PROMPT_DISABLED=1",
		"GIT_ALLOW_PROTOCOL="+allowProtocol,
	)
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		environment = append(environment, key+"="+overrides[key])
	}
	return environment, nil
}

// boundedSink retains at most limit bytes while always accepting writes, so a
// chatty child never blocks on a full pipe; bytes past the limit are counted
// and dropped exactly like the legacy bounded stream.
type boundedSink struct {
	mu         sync.Mutex
	limit      int64
	stored     int64
	observed   int64
	buffer     []byte
	onOverflow func()
	overflowed sync.Once
}

func newBoundedSink(limit int64) *boundedSink {
	return &boundedSink{limit: limit}
}

func (s *boundedSink) Write(chunk []byte) (int, error) {
	s.mu.Lock()
	s.observed += int64(len(chunk))
	room := s.limit - s.stored
	if room > 0 {
		size := int64(len(chunk))
		if size > room {
			size = room
		}
		s.buffer = append(s.buffer, chunk[:size]...)
		s.stored += size
	}
	over := s.observed > s.limit
	s.mu.Unlock()
	if over && s.onOverflow != nil {
		s.overflowed.Do(s.onOverflow)
	}
	return len(chunk), nil
}

func (s *boundedSink) bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.buffer...)
}

// run spawns one git subprocess under the hardened profile. It returns the
// classified result for every process that started (including failures —
// callers assert success the way the legacy runner did) and a typed error
// only when the invocation could not be run or decoded at all.
func (p *CLIGitPort) run(invocation gitInvocation) (gitResult, error) {
	for _, argument := range invocation.arguments {
		if strings.ContainsRune(argument, 0) {
			return gitResult{}, invalid("NUL in Git argument.")
		}
	}
	if err := p.assertScratchInvariants(); err != nil {
		return gitResult{}, err
	}
	arguments := append(p.baseArguments(invocation.auth), invocation.arguments...)
	environment, err := p.environment(invocation.transport, invocation.environment)
	if err != nil {
		return gitResult{}, err
	}

	var ctx context.Context
	var command *exec.Cmd
	if invocation.keepAlive {
		command = exec.Command(p.gitBinary, arguments...)
	} else {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), p.timeout)
		defer cancel()
		command = exec.CommandContext(ctx, p.gitBinary, arguments...)
	}
	command.Dir = p.processCwd
	command.Env = environment

	var monitor sync.Mutex
	stopReason := ""
	setStop := func(reason string) {
		monitor.Lock()
		defer monitor.Unlock()
		if stopReason == "" {
			stopReason = reason
		}
	}
	currentStop := func() string {
		monitor.Lock()
		defer monitor.Unlock()
		return stopReason
	}
	var killOnce sync.Once
	kill := func() {
		killOnce.Do(func() {
			if command.Process != nil {
				_ = command.Process.Kill()
			}
		})
	}
	stdout := newBoundedSink(p.maxOutputBytes)
	stderr := newBoundedSink(p.maxOutputBytes)
	install := func(sink *boundedSink) {
		sink.onOverflow = func() {
			setStop("output_limit")
			if !invocation.keepAlive {
				kill()
			}
		}
	}
	install(stdout)
	install(stderr)
	command.Stdout = stdout
	command.Stderr = stderr

	stdin, err := command.StdinPipe()
	if err != nil {
		return gitResult{}, blocked("Git stdin pipe is unavailable: %v", err)
	}
	if err := command.Start(); err != nil {
		return gitResult{}, blocked("Git executable could not be started: %v", err)
	}
	go func() {
		if len(invocation.input) > 0 {
			_, _ = stdin.Write(invocation.input)
		}
		_ = stdin.Close()
	}()

	done := make(chan error, 1)
	go func() { done <- command.Wait() }()

	if invocation.keepAlive {
		timer := time.NewTimer(p.timeout)
		select {
		case waitErr := <-done:
			timer.Stop()
			return collectGitResult(waitErr, currentStop(), true, invocation.keepAlive, stdout, stderr)
		case <-timer.C:
			// The dispatched push is never interrupted; the watchdog only
			// gives up waiting while a reaper keeps draining the pipes.
			setStop("timeout")
			go func() { <-done }()
			return collectGitResult(nil, "timeout", false, invocation.keepAlive, stdout, stderr)
		}
	}
	waitErr := <-done
	reason := currentStop()
	if reason == "" && ctx != nil && ctx.Err() != nil {
		reason = "timeout"
	}
	return collectGitResult(waitErr, reason, true, invocation.keepAlive, stdout, stderr)
}

func collectGitResult(waitErr error, stopReason string, exited bool, neverInterrupted bool, stdout, stderr *boundedSink) (gitResult, error) {
	result := gitResult{stopReason: stopReason, stdout: stdout.bytes()}
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
	case errors.As(waitErr, &exitErr):
		result.exitCode = exitErr.ExitCode()
	default:
		return gitResult{}, blocked("Git process wait failed: %v", waitErr)
	}
	// A never-interrupted push still counts as completed when it exited by
	// itself, even if an output limit was observed; every interrupted or
	// abandoned invocation stays incomplete.
	result.completed = exited && (stopReason == "" || neverInterrupted)
	stderrBytes := stderr.bytes()
	if !utf8.Valid(stderrBytes) {
		return gitResult{}, blocked("Git stderr is not valid UTF-8.")
	}
	result.stderr = protectDiagnostic(string(stderrBytes))
	return result, nil
}

// runAssert runs an invocation and asserts the legacy success contract.
func (p *CLIGitPort) runAssert(invocation gitInvocation) error {
	result, err := p.run(invocation)
	if err != nil {
		return err
	}
	return gitAssertSuccess(result, invocation.label)
}

// gitAssertSuccess mirrors Assert-BFPublicationGitSuccess byte-for-byte.
func gitAssertSuccess(result gitResult, label string) error {
	if !result.completed {
		return blocked("%s did not complete within the bounded wait.", label)
	}
	if result.exitCode != 0 {
		return blocked("%s failed: %s", label, strings.TrimSpace(result.stderr))
	}
	return nil
}

// strictGitOutput decodes captured stdout under the legacy strict-UTF-8 rule.
func strictGitOutput(result gitResult, label string) (string, error) {
	if !utf8.Valid(result.stdout) {
		return "", blocked("%s is not valid UTF-8.", label)
	}
	return string(result.stdout), nil
}
