package delivery

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testBinaryPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// newUnitPort builds a port without ever running git: the binary points at
// this test binary, which is only inspected for existence.
func newUnitPort(t *testing.T) *CLIGitPort {
	t.Helper()
	port, err := NewCLIGitPort(GitPortConfig{
		GitBinary:   testBinaryPath(t),
		WorkDir:     t.TempDir(),
		ScratchDir:  filepath.Join(t.TempDir(), "scratch"),
		Baseline:    CommitID(strings.Repeat("a", 40)),
		AuthorName:  "BSL Flow Controller",
		AuthorEmail: "controller@bsl-flow.invalid",
		CommitTime:  time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func TestNewCLIGitPortValidatesConfiguration(t *testing.T) {
	base := func() GitPortConfig {
		return GitPortConfig{
			GitBinary:   testBinaryPath(t),
			WorkDir:     t.TempDir(),
			ScratchDir:  filepath.Join(t.TempDir(), "scratch"),
			Baseline:    CommitID(strings.Repeat("a", 40)),
			AuthorName:  "n",
			AuthorEmail: "n@example.invalid",
		}
	}
	cases := []struct {
		name   string
		mutate func(*GitPortConfig)
		kind   string
		text   string
	}{
		{"short baseline", func(c *GitPortConfig) { c.Baseline = "abc" }, KindInvalid, "publication baseline is not an object identity"},
		{"relative workdir", func(c *GitPortConfig) { c.WorkDir = "relative" }, KindInvalid, "publication path must be an ordinary absolute filesystem path."},
		{"missing workdir", func(c *GitPortConfig) { c.WorkDir = filepath.Join(t.TempDir(), "missing") }, KindBlocked, "required publication directory is missing:"},
		{"timeout below one second", func(c *GitPortConfig) { c.Timeout = 500 * time.Millisecond }, KindInvalid, "Git timeout must be between 1 and 120 seconds."},
		{"timeout above 120 seconds", func(c *GitPortConfig) { c.Timeout = 121 * time.Second }, KindInvalid, "Git timeout must be between 1 and 120 seconds."},
		{"missing explicit git binary", func(c *GitPortConfig) { c.GitBinary = filepath.Join(t.TempDir(), "git-missing.exe") }, KindBlocked, "required publication file is missing:"},
		{"relative explicit git binary", func(c *GitPortConfig) { c.GitBinary = "git.exe" }, KindInvalid, "publication path must be an ordinary absolute filesystem path."},
	}
	for _, testCase := range cases {
		config := base()
		testCase.mutate(&config)
		_, err := NewCLIGitPort(config)
		if errorKind(err) != testCase.kind || !strings.Contains(err.Error(), testCase.text) {
			t.Fatalf("%s: expected %s containing %q, got %v", testCase.name, testCase.kind, testCase.text, err)
		}
	}
	success := base()
	success.CommitTime = time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	port, err := NewCLIGitPort(success)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{port.emptyConfig, port.emptyAttributes} {
		if info, statErr := os.Stat(path); statErr != nil || info.Size() != 0 {
			t.Fatalf("empty context file %s was not created empty", path)
		}
	}
	if entries, readErr := os.ReadDir(port.emptyHooks); readErr != nil || len(entries) != 0 {
		t.Fatalf("hooks directory is not empty: %v", readErr)
	}
	if entries, readErr := os.ReadDir(port.processCwd); readErr != nil || len(entries) != 0 {
		t.Fatalf("process working directory is not empty: %v", readErr)
	}
	if port.commitStamp != "2026-09-15T12:00:00Z" {
		t.Fatalf("commit stamp was not canonicalized to UTC RFC3339: %s", port.commitStamp)
	}
}

func TestGitScratchInvariantsFailClosed(t *testing.T) {
	port := newUnitPort(t)
	if err := os.WriteFile(port.emptyConfig, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := port.assertScratchInvariants()
	if errorKind(err) != KindBlocked || !strings.Contains(err.Error(), "publication empty config or attributes file changed.") {
		t.Fatalf("tampered empty config must block, got %v", err)
	}
	if err := os.WriteFile(port.emptyConfig, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(port.emptyHooks, "hook"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = port.assertScratchInvariants()
	if errorKind(err) != KindBlocked || !strings.Contains(err.Error(), "publication hooks directory is not empty.") {
		t.Fatalf("non-empty hooks must block, got %v", err)
	}
}

func TestGitBaseArgumentsMatchHardenedProfile(t *testing.T) {
	port := newUnitPort(t)
	expected := []string{
		"--no-replace-objects",
		"-c", "core.longpaths=true",
		"-c", "core.hooksPath=" + port.emptyHooks,
		"-c", "core.fsmonitor=false",
		"-c", "core.autocrlf=false",
		"-c", "core.safecrlf=false",
		"-c", "core.attributesFile=" + port.emptyAttributes,
		"-c", "commit.gpgSign=false",
		"-c", "tag.gpgSign=false",
		"-c", "credential.helper=",
		"-c", "http.followRedirects=false",
	}
	actual := port.baseArguments("none")
	if strings.Join(actual, "\x00") != strings.Join(expected, "\x00") {
		t.Fatalf("hardened base argv changed:\n got %v\nwant %v", actual, expected)
	}
	port.gitHubCLI = `C:\Program Files\GitHub CLI\gh.exe`
	withHelper := port.baseArguments("github_cli")
	if withHelper[len(withHelper)-2] != "-c" ||
		withHelper[len(withHelper)-1] != `credential.helper=!"C:/Program Files/GitHub CLI/gh.exe" auth git-credential` {
		t.Fatalf("github_cli credential helper argv is wrong: %v", withHelper)
	}
}

func TestGitEnvironmentScrubsAndInjects(t *testing.T) {
	for _, key := range []string{
		"GIT_DIR", "GIT_INDEX_FILE", "GITHUB_TOKEN", "GH_HOST", "SSH_AUTH_SOCK",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "CURL_CA_BUNDLE",
		"SSL_CERT_FILE", "BSL_FLOW_HOST_PATH",
	} {
		t.Setenv(key, "pollution")
	}
	port := newUnitPort(t)
	environment, err := port.environment("file", map[string]string{"GIT_AUTHOR_NAME": "author"})
	if err != nil {
		t.Fatal(err)
	}
	hasKey := func(name string) bool { return envHasKey(environment, name) }
	hasValue := func(name, value string) bool { return envHasValue(environment, name, value) }
	for _, key := range []string{
		"GIT_DIR", "GIT_INDEX_FILE", "GITHUB_TOKEN", "GH_HOST", "SSH_AUTH_SOCK",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "CURL_CA_BUNDLE",
		"SSL_CERT_FILE", "BSL_FLOW_HOST_PATH",
	} {
		if hasKey(key) {
			t.Fatalf("scrubbed key %s leaked into the git environment", key)
		}
	}
	if !hasValue("GIT_AUTHOR_NAME", "author") {
		t.Fatal("whitelisted override GIT_AUTHOR_NAME is missing")
	}
	indexEnv, err := port.environment("none", map[string]string{"GIT_INDEX_FILE": port.indexPath})
	if err != nil {
		t.Fatal(err)
	}
	if !envHasValue(indexEnv, "GIT_INDEX_FILE", port.indexPath) {
		t.Fatal("whitelisted override GIT_INDEX_FILE is missing")
	}
	if len(indexEnv) != len(environment) {
		t.Fatalf("override cardinality changed the environment shape: %d vs %d", len(indexEnv), len(environment))
	}
	if !hasValue("GIT_CONFIG_NOSYSTEM", "1") ||
		!hasValue("GIT_CONFIG_GLOBAL", port.emptyConfig) ||
		!hasValue("GIT_TERMINAL_PROMPT", "0") ||
		!hasValue("GIT_NO_REPLACE_OBJECTS", "1") ||
		!hasValue("GIT_NO_LAZY_FETCH", "1") ||
		!hasValue("GCM_INTERACTIVE", "never") ||
		!hasValue("GH_PROMPT_DISABLED", "1") ||
		!hasValue("GIT_ALLOW_PROTOCOL", "file") {
		t.Fatalf("pinned git isolation variables are missing: %v", environment)
	}
	if !hasKey("PATH") {
		t.Fatal("ambient PATH was dropped entirely")
	}
	httpsEnv, err := port.environment("https", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !envHasValue(httpsEnv, "GIT_ALLOW_PROTOCOL", "https") {
		t.Fatal("https transport must pin GIT_ALLOW_PROTOCOL=https")
	}
}

func envHasKey(environment []string, name string) bool {
	for _, item := range environment {
		if key, _, _ := strings.Cut(item, "="); strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func envHasValue(environment []string, name, value string) bool {
	for _, item := range environment {
		if key, content, _ := strings.Cut(item, "="); strings.EqualFold(key, name) && content == value {
			return true
		}
	}
	return false
}

func TestGitEnvironmentRejectsUnsupportedOverrideKeys(t *testing.T) {
	port := newUnitPort(t)
	if _, err := port.environment("none", map[string]string{"GIT_AUTHOR_NAME": "x"}); err != nil {
		t.Fatal(err)
	}
	_, err := port.environment("none", map[string]string{"GIT_ALLOW_PROTOCOL": "file"})
	if errorKind(err) != KindInvalid || !strings.Contains(err.Error(), "unsupported publication environment key.") {
		t.Fatalf("unsupported override must be invalid, got %v", err)
	}
}

func TestProtectDiagnosticRedactsURLUserinfo(t *testing.T) {
	text := "fatal: unable to access 'https://user:secret@github.com/owner/repo.git/'"
	redacted := protectDiagnostic(text)
	if strings.Contains(redacted, "user:secret") || !strings.Contains(redacted, "https://[redacted]@github.com/owner/repo.git/") {
		t.Fatalf("userinfo was not redacted: %s", redacted)
	}
	if protectDiagnostic("no urls here") != "no urls here" {
		t.Fatal("plain diagnostics must pass through")
	}
}

func TestGitAssertSuccessClassification(t *testing.T) {
	incomplete := gitResult{completed: false}
	err := gitAssertSuccess(incomplete, "tree creation")
	if errorKind(err) != KindBlocked || err.Error() != "BF_BLOCKED: tree creation did not complete within the bounded wait." {
		t.Fatalf("incomplete classification is wrong: %v", err)
	}
	failed := gitResult{completed: true, exitCode: 128, stderr: "fatal: boom\n"}
	err = gitAssertSuccess(failed, "worker HEAD read")
	if errorKind(err) != KindBlocked || err.Error() != "BF_BLOCKED: worker HEAD read failed: fatal: boom" {
		t.Fatalf("failure classification is wrong: %v", err)
	}
	if err := gitAssertSuccess(gitResult{completed: true, exitCode: 0}, "x"); err != nil {
		t.Fatalf("success must not error: %v", err)
	}
}

func TestClassifyRemoteHead(t *testing.T) {
	ref := "refs/heads/codex/x"
	cases := []struct {
		name    string
		result  gitResult
		head    CommitID
		known   bool
		errKind string
		errText string
	}{
		{
			name:   "single exact line is a known head",
			result: gitResult{completed: true, exitCode: 0, stdout: []byte(strings.Repeat("a", 40) + "\t" + ref + "\n")},
			head:   CommitID(strings.Repeat("a", 40)),
			known:  true,
		},
		{
			name:   "exit two with empty output is a known absent ref",
			result: gitResult{completed: true, exitCode: 2},
			known:  true,
		},
		{
			name:    "incomplete stays unknown",
			result:  gitResult{completed: false, stopReason: "timeout"},
			errKind: KindBlocked, errText: "BF_BLOCKED: ls-remote did not complete within the bounded wait.",
		},
		{
			name:    "transport exit is undeterminable",
			result:  gitResult{completed: true, exitCode: 128, stderr: "fatal: remote unavailable"},
			errKind: KindBlocked, errText: "BF_BLOCKED: remote query did not return a verified present or absent ref.",
		},
		{
			name:    "exit two with output is undeterminable",
			result:  gitResult{completed: true, exitCode: 2, stdout: []byte("something\n")},
			errKind: KindBlocked, errText: "remote query did not return a verified present or absent ref.",
		},
		{
			name:    "multiple lines are ambiguous",
			result:  gitResult{completed: true, exitCode: 0, stdout: []byte(strings.Repeat("a", 40) + "\t" + ref + "\n" + strings.Repeat("b", 40) + "\t" + ref + "\n")},
			errKind: KindBlocked, errText: "remote returned an ambiguous or unexpected ref.",
		},
		{
			name:    "foreign ref name is ambiguous",
			result:  gitResult{completed: true, exitCode: 0, stdout: []byte(strings.Repeat("a", 40) + "\trefs/heads/codex/other\n")},
			errKind: KindBlocked, errText: "remote returned an ambiguous or unexpected ref.",
		},
		{
			name:    "malformed identity is ambiguous",
			result:  gitResult{completed: true, exitCode: 0, stdout: []byte("zzzz\t" + ref + "\n")},
			errKind: KindBlocked, errText: "remote returned an ambiguous or unexpected ref.",
		},
		{
			name:    "non utf8 output is undeterminable",
			result:  gitResult{completed: true, exitCode: 0, stdout: []byte{0xff, 0xfe, '\n'}},
			errKind: KindBlocked, errText: "remote query did not return a verified present or absent ref.",
		},
	}
	for _, testCase := range cases {
		head, known, err := classifyRemoteHead(testCase.result, ref)
		if testCase.errKind != "" {
			if err == nil || errorKind(err) != testCase.errKind || !strings.Contains(err.Error(), testCase.errText) {
				t.Fatalf("%s: expected %s containing %q, got %v", testCase.name, testCase.errKind, testCase.errText, err)
			}
			continue
		}
		if err != nil || known != testCase.known || head != testCase.head {
			t.Fatalf("%s: expected (%s,%v,nil), got (%s,%v,%v)", testCase.name, testCase.head, testCase.known, head, known, err)
		}
	}
}

func TestParseLsTree(t *testing.T) {
	record := "100644 blob " + strings.Repeat("a", 40) + "\tsrc/main.go"
	entries, err := parseLsTree([]byte(record + "\x00"))
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := entries["src/main.go"]
	if !ok || entry.Mode != "100644" || entry.Type != "blob" || entry.OID != strings.Repeat("a", 40) {
		t.Fatalf("parsed entry is wrong: %+v", entry)
	}
	failureCases := []struct {
		name string
		data []byte
		text string
	}{
		{"unterminated", []byte(record), "unterminated ls-tree output."},
		{"no tab", []byte("100644 blob\x00"), "malformed ls-tree record."},
		{"bad identity", []byte("100644 blob zzz\tpath\x00"), "malformed ls-tree identity."},
		{"duplicate path", []byte(record + "\x00" + record + "\x00"), "duplicate path in baseline tree."},
		{"invalid utf8", append([]byte("100644 blob "), append([]byte(strings.Repeat("a", 40)), append([]byte{0xff}, '\t', 'p', 0)...)...), "ls-tree record is not valid UTF-8."},
	}
	for _, failure := range failureCases {
		_, err := parseLsTree(failure.data)
		if errorKind(err) != KindBlocked || !strings.Contains(err.Error(), failure.text) {
			t.Fatalf("%s: expected %q, got %v", failure.name, failure.text, err)
		}
	}
}

func TestBoundedSinkLimitsCaptureWithoutBlocking(t *testing.T) {
	sink := newBoundedSink(4)
	overflowed := false
	sink.onOverflow = func() { overflowed = true }
	chunk := bytes.Repeat([]byte("x"), 10)
	for write := 0; write < 3; write++ {
		if size, err := sink.Write(chunk); err != nil || size != len(chunk) {
			t.Fatalf("bounded sink must always accept writes: %d %v", size, err)
		}
	}
	if string(sink.bytes()) != "xxxx" {
		t.Fatalf("retained bytes are wrong: %q", sink.bytes())
	}
	if !overflowed {
		t.Fatal("overflow callback did not fire")
	}
}

func TestStageValidatesRelativePathsBeforeTouchingGit(t *testing.T) {
	port := newUnitPort(t)
	for _, path := range []string{"", "/absolute", `back\slash`, "../escape", "a/../b", ".git/config", "x/"} {
		if err := port.Stage(path); errorKind(err) != KindInvalid {
			t.Fatalf("path %q must be invalid, got %v", path, err)
		}
	}
}

func TestCollectGitResultRejectsInvalidUTF8Stderr(t *testing.T) {
	_, err := collectGitResult(nil, "", true, false, newBoundedSink(16), func() *boundedSink {
		sink := newBoundedSink(16)
		_, _ = sink.Write([]byte{0xff, 0xfe})
		return sink
	}())
	if errorKind(err) != KindBlocked || !strings.Contains(err.Error(), "Git stderr is not valid UTF-8.") {
		t.Fatalf("invalid stderr must block, got %v", err)
	}
}

func TestGitUnknownErrorsCarryCause(t *testing.T) {
	cause := errors.New("transport lost")
	unknown := &UnknownEffectError{Operation: "push", Cause: cause}
	if unknown.Error() != "unknown effect after push: transport lost" || !errors.Is(unknown, cause) {
		t.Fatalf("unknown effect error is wrong: %v", unknown)
	}
}
