package delivery

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Integration coverage drives the real git CLI through CLIGitPort and the
// existing Begin/Step/Resume state machine against local file remotes only.

var integrationFileContents = map[string]string{
	"src/main.go":        "package main\n",
	"src/util/util.go":   "package util\n",
	"tests/main_test.go": "package main_test\n",
	"legacy/old.txt":     "legacy\n",
}

// tempDirWithRetry creates a directory whose cleanup tolerates the Windows
// antivirus "directory not empty" flake on freshly written repositories.
func tempDirWithRetry(t *testing.T, label string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bslflow-"+label+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(10 * time.Second)
		for {
			err := os.RemoveAll(dir)
			if err == nil || time.Now().After(deadline) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	return dir
}

func testGitBinary(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git is not available in PATH: %v", err)
	}
	return path
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command(testGitBinary(t), args...)
	if dir != "" {
		command.Dir = dir
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	stdout, err := command.Output()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, stderr.String())
	}
	return strings.TrimSpace(string(stdout))
}

func gitCommandFails(t *testing.T, dir string, args ...string) bool {
	t.Helper()
	command := exec.Command(testGitBinary(t), args...)
	if dir != "" {
		command.Dir = dir
	}
	return command.Run() != nil
}

// newWorkerRepo builds the worker worktree: a real repository whose HEAD is
// the baseline and whose files already reflect the accepted source (the
// deleted legacy file is removed from the worktree).
func newWorkerRepo(t *testing.T) (string, CommitID) {
	t.Helper()
	testGitBinary(t)
	dir := tempDirWithRetry(t, "worker")
	runGit(t, dir, "init", ".")
	runGit(t, dir, "symbolic-ref", "HEAD", "refs/heads/master")
	runGit(t, dir, "config", "user.name", "Worker")
	runGit(t, dir, "config", "user.email", "worker@example.invalid")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	runGit(t, dir, "config", "core.autocrlf", "false")
	for path, content := range integrationFileContents {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "baseline")
	baseline := runGit(t, dir, "rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(dir, "legacy", "old.txt")); err != nil {
		t.Fatal(err)
	}
	return dir, CommitID(baseline)
}

func newBareRemote(t *testing.T, workDir string) string {
	t.Helper()
	testGitBinary(t)
	parent := tempDirWithRetry(t, "remote")
	remote := filepath.Join(parent, "remote.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, "", "--git-dir", remote, "symbolic-ref", "HEAD", "refs/heads/master")
	runGit(t, workDir, "push", remote, "master")
	return remote
}

func integrationAccepted(t *testing.T, baseline CommitID) AcceptedSource {
	t.Helper()
	files := []ManifestFile{
		{Path: "src/main.go", SHA256: sha256Hex([]byte(integrationFileContents["src/main.go"]))},
		{Path: "src/util/util.go", SHA256: sha256Hex([]byte(integrationFileContents["src/util/util.go"]))},
		{Path: "tests/main_test.go", SHA256: sha256Hex([]byte(integrationFileContents["tests/main_test.go"]))},
		{Path: "legacy/old.txt", Deleted: true},
	}
	manifest := Manifest{SchemaVersion: 1, Baseline: baseline, Files: files}
	digest, err := hashValue(manifest.value())
	if err != nil {
		t.Fatal(err)
	}
	return AcceptedSource{
		TaskID:       testTaskID,
		Status:       "completed",
		Verdict:      "PASS",
		Mode:         "implement",
		EvidenceHash: digest,
		Manifest:     manifest,
	}
}

func realTarget(remote string) Target {
	return Target{
		Remote:       remote,
		Ref:          "refs/heads/codex/native-cli",
		AuthorizedBy: "none",
		AllowedPaths: []string{"src", "tests", "legacy"},
	}
}

func newTestGitPort(t *testing.T, workDir, scratch string, baseline CommitID) *CLIGitPort {
	t.Helper()
	port, err := NewCLIGitPort(GitPortConfig{
		WorkDir:     workDir,
		ScratchDir:  scratch,
		Baseline:    baseline,
		AuthorName:  "BSL Flow Controller",
		AuthorEmail: "controller@bsl-flow.invalid",
		CommitTime:  time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC),
		Timeout:     120 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// failingPushGitPort simulates a lost transport after the single dispatch:
// every other operation delegates to a real port.
type failingPushGitPort struct {
	inner GitPort
	fail  error
}

func (f *failingPushGitPort) Stage(path string) error { return f.inner.Stage(path) }

func (f *failingPushGitPort) CommitAll(message string) (CommitID, error) {
	return f.inner.CommitAll(message)
}

func (f *failingPushGitPort) ReadRemoteHead(remote, ref string) (CommitID, bool, error) {
	return f.inner.ReadRemoteHead(remote, ref)
}

func (f *failingPushGitPort) Push(remote, ref string) error { return f.fail }

func TestCLIGitPortPublishesAcceptedSourceToBareRemote(t *testing.T) {
	testGitBinary(t)
	workDir, baseline := newWorkerRepo(t)
	remote := newBareRemote(t, workDir)
	target := realTarget(remote)
	plan, err := BuildPlan(target, integrationAccepted(t, baseline))
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(tempDirWithRetry(t, "scratch"), "publication")
	port := newTestGitPort(t, workDir, scratch, baseline)
	state, err := Begin(plan)
	if err != nil {
		t.Fatal(err)
	}
	var outcome Outcome
	for state.Stage != StageVerified {
		state, outcome, err = Step(plan, state, port)
		if err != nil {
			t.Fatal(err)
		}
	}
	if outcome != OutcomeVerified || state.Stage != StageVerified {
		t.Fatalf("publication finished with %s/%s", state.Stage, outcome)
	}
	head := runGit(t, "", "--git-dir", remote, "rev-parse", target.Ref)
	if head != string(state.Commit) {
		t.Fatalf("remote head %s is not the planned commit %s", head, state.Commit)
	}
	listing := runGit(t, "", "--git-dir", remote, "ls-tree", "-r", "--name-only", string(state.Commit))
	if listing != "src/main.go\nsrc/util/util.go\ntests/main_test.go" {
		t.Fatalf("published tree does not match the accepted manifest:\n%s", listing)
	}
	parent := runGit(t, "", "--git-dir", remote, "rev-parse", string(state.Commit)+"^")
	if parent != string(baseline) {
		t.Fatalf("published parent %s is not the baseline %s", parent, baseline)
	}
	persisted, err := state.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	resumed, outcome, err := Resume(persisted, newTestGitPort(t, workDir, scratch, baseline))
	if err != nil || outcome != OutcomeVerified || resumed.Stage != StageVerified {
		t.Fatalf("verified publication did not replay idempotently: %v %s %s", err, outcome, resumed.Stage)
	}
}

func TestCLIGitPortRebuiltPublicationIsDeterministicAndNeverRedispatches(t *testing.T) {
	testGitBinary(t)
	workDir, baseline := newWorkerRepo(t)
	remote := newBareRemote(t, workDir)
	target := realTarget(remote)
	plan, err := BuildPlan(target, integrationAccepted(t, baseline))
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(tempDirWithRetry(t, "scratch"), "publication")
	first := driveTo(t, plan, newTestGitPort(t, workDir, scratch, baseline), StageVerified)

	// A second controller process rebuilds the same plan over the same
	// scratch: the commit identity is stable and the remote head already
	// equals it, so the flow verifies without a second dispatch.
	rebuilt, err := BuildPlan(target, integrationAccepted(t, baseline))
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.PlanHash != plan.PlanHash {
		t.Fatal("rebuilt plan identity changed")
	}
	second := driveTo(t, rebuilt, newTestGitPort(t, workDir, scratch, baseline), StageVerified)
	if second.Commit != first.Commit {
		t.Fatalf("rebuilt commit %s differs from the first %s", second.Commit, first.Commit)
	}
	head := runGit(t, "", "--git-dir", remote, "rev-parse", target.Ref)
	if head != string(first.Commit) {
		t.Fatalf("remote head %s moved away from %s", head, first.Commit)
	}
}

func TestCLIGitPortResumeAfterCrashBetweenStageAndCommit(t *testing.T) {
	testGitBinary(t)
	workDir, baseline := newWorkerRepo(t)
	remote := newBareRemote(t, workDir)
	target := realTarget(remote)
	plan, err := BuildPlan(target, integrationAccepted(t, baseline))
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(tempDirWithRetry(t, "scratch"), "publication")
	prepared := driveTo(t, plan, newTestGitPort(t, workDir, scratch, baseline), StagePrepared)
	persisted, err := prepared.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	// A fresh process continues from the persisted staging: the records and
	// index survive in the scratch directory and the commit is rebuilt
	// deterministically without re-staging.
	resumed, outcome, err := Resume(persisted, newTestGitPort(t, workDir, scratch, baseline))
	if err != nil || outcome != OutcomeVerified || resumed.Stage != StageVerified {
		t.Fatalf("resume from prepared failed: %v %s %s", err, outcome, resumed.Stage)
	}
	head := runGit(t, "", "--git-dir", remote, "rev-parse", target.Ref)
	if head != string(resumed.Commit) {
		t.Fatalf("remote head %s is not the resumed commit %s", head, resumed.Commit)
	}
}

func TestCLIGitPortPushUnknownEffectRequiresManualReconciliation(t *testing.T) {
	testGitBinary(t)
	workDir, baseline := newWorkerRepo(t)
	remote := newBareRemote(t, workDir)
	target := realTarget(remote)
	plan, err := BuildPlan(target, integrationAccepted(t, baseline))
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(tempDirWithRetry(t, "scratch"), "publication")
	committed := driveTo(t, plan, newTestGitPort(t, workDir, scratch, baseline), StageCommitted)

	// The dispatch transport is lost after the push left the process.
	failing := &failingPushGitPort{
		inner: newTestGitPort(t, workDir, scratch, baseline),
		fail:  errors.New("simulated transport loss after dispatch"),
	}
	blockedState, outcome, err := Step(plan, committed, failing)
	if outcome != OutcomeBlockedNeedsReconciliation {
		t.Fatalf("unknown push must block reconciliation, got %s", outcome)
	}
	var unknown *UnknownEffectError
	if !errors.As(err, &unknown) {
		t.Fatalf("push failure must classify as unknown effect: %v", err)
	}
	if blockedState.Stage != StageCommitted || !blockedState.PushDispatched || blockedState.PushOutcome != PushUnknown {
		t.Fatalf("dispatched state was not recorded: %+v", blockedState)
	}
	persisted, err := blockedState.Bytes()
	if err != nil {
		t.Fatal(err)
	}

	// Automatic resume never re-pushes while the effect is unsettled.
	_, outcome, err = Resume(persisted, newTestGitPort(t, workDir, scratch, baseline))
	if outcome != OutcomeBlockedNeedsReconciliation || err == nil {
		t.Fatalf("unsettled resume must stay blocked: %s %v", outcome, err)
	}
	if !gitCommandFails(t, "", "--git-dir", remote, "rev-parse", target.Ref) {
		t.Fatal("blocked resume must not have created the remote ref")
	}

	// Out-of-band reconciliation lands exactly the planned commit; the next
	// resume verifies idempotently without any further dispatch.
	runGit(t, workDir, "push", remote, string(blockedState.Commit)+":"+target.Ref)
	resumed, outcome, err := Resume(persisted, newTestGitPort(t, workDir, scratch, baseline))
	if err != nil || outcome != OutcomeVerified || resumed.Stage != StageVerified {
		t.Fatalf("reconciled remote did not complete idempotently: %v %s %s", err, outcome, resumed.Stage)
	}
	head := runGit(t, "", "--git-dir", remote, "rev-parse", target.Ref)
	if head != string(blockedState.Commit) {
		t.Fatalf("remote head %s is not the dispatched commit %s", head, blockedState.Commit)
	}
}

func TestCLIGitPortUnknownRemoteHeadReadBlocks(t *testing.T) {
	testGitBinary(t)
	workDir, baseline := newWorkerRepo(t)
	missing := filepath.Join(tempDirWithRetry(t, "remote-missing"), "absent.git")
	target := realTarget(missing)
	plan, err := BuildPlan(target, integrationAccepted(t, baseline))
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(tempDirWithRetry(t, "scratch"), "publication")
	port := newTestGitPort(t, workDir, scratch, baseline)
	committed := driveTo(t, plan, port, StageCommitted)
	_, outcome, err := Step(plan, committed, port)
	if outcome != OutcomeBlockedNeedsReconciliation {
		t.Fatalf("unreadable remote must block reconciliation, got %s", outcome)
	}
	var unknown *UnknownEffectError
	if !errors.As(err, &unknown) || unknown.Operation != "remote head read" {
		t.Fatalf("expected unknown effect after the remote head read, got %v", err)
	}
	if _, _, err = port.ReadRemoteHead(missing, target.Ref); errorKind(err) != KindBlocked ||
		!strings.Contains(err.Error(), "remote query did not return a verified present or absent ref.") {
		t.Fatalf("unreadable remote classification is wrong: %v", err)
	}
}

func TestCLIGitPortForeignRemoteOIDConflictsAndLeaseRejectsOverwrite(t *testing.T) {
	testGitBinary(t)
	workDir, baseline := newWorkerRepo(t)
	remote := newBareRemote(t, workDir)
	target := realTarget(remote)
	plan, err := BuildPlan(target, integrationAccepted(t, baseline))
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(tempDirWithRetry(t, "scratch"), "publication")
	port := newTestGitPort(t, workDir, scratch, baseline)
	committed := driveTo(t, plan, port, StageCommitted)

	// A foreign commit lands on the publication ref out of band.
	baselineTree := runGit(t, workDir, "rev-parse", string(baseline)+"^{tree}")
	foreign := runGit(t, workDir, "commit-tree", baselineTree, "-m", "foreign")
	runGit(t, workDir, "push", remote, foreign+":"+target.Ref)

	_, outcome, err := Step(plan, committed, port)
	if err == nil || errorKind(err) != KindConflict || outcome == OutcomeVerified {
		t.Fatalf("foreign OID must conflict, got %v / %s", err, outcome)
	}
	// The create-only lease also rejects a direct push onto the existing ref.
	if err := port.Push(remote, target.Ref); err == nil || errorKind(err) != KindBlocked {
		t.Fatalf("lease must reject pushing over an existing ref, got %v", err)
	}
	head := runGit(t, "", "--git-dir", remote, "rev-parse", target.Ref)
	if head != foreign {
		t.Fatalf("remote head %s was overwritten; expected %s", head, foreign)
	}
}

func TestCLIGitPortStageFailsClosedOnMissingFile(t *testing.T) {
	testGitBinary(t)
	workDir, baseline := newWorkerRepo(t)
	port := newTestGitPort(t, workDir, filepath.Join(tempDirWithRetry(t, "scratch"), "publication"), baseline)
	err := port.Stage("src/absent.go")
	if errorKind(err) != KindBlocked || !strings.Contains(err.Error(), "required publication file is missing:") {
		t.Fatalf("missing worktree file must block, got %v", err)
	}
}

func TestCLIGitPortCommitMetadataAndDeterminism(t *testing.T) {
	testGitBinary(t)
	workDir, baseline := newWorkerRepo(t)
	scratch := filepath.Join(tempDirWithRetry(t, "scratch"), "publication")
	port := newTestGitPort(t, workDir, scratch, baseline)
	for _, path := range []string{"src/main.go", "src/util/util.go", "tests/main_test.go"} {
		if err := port.Stage(path); err != nil {
			t.Fatal(err)
		}
	}
	message := "publication of the accepted source"
	first, err := port.CommitAll(message)
	if err != nil {
		t.Fatal(err)
	}
	raw := runGit(t, workDir, "cat-file", "commit", string(first))
	if !strings.Contains(raw, "author BSL Flow Controller <controller@bsl-flow.invalid> ") ||
		!strings.Contains(raw, "committer BSL Flow Controller <controller@bsl-flow.invalid> ") ||
		!strings.Contains(raw, "+0000") ||
		!strings.Contains(raw, "\n"+message) {
		t.Fatalf("commit metadata does not match the pinned identity:\n%s", raw)
	}
	second, err := port.CommitAll(message)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("commit identity is not deterministic: %s vs %s", second, first)
	}
}

func TestGitRunTimeoutClassifiesIncomplete(t *testing.T) {
	testGitBinary(t)
	port, err := NewCLIGitPort(GitPortConfig{
		WorkDir:     t.TempDir(),
		ScratchDir:  filepath.Join(t.TempDir(), "scratch"),
		Baseline:    CommitID(strings.Repeat("a", 40)),
		AuthorName:  "n",
		AuthorEmail: "n@example.invalid",
	})
	if err != nil {
		t.Fatal(err)
	}
	port.timeout = time.Nanosecond
	var result gitResult
	for attempt := 0; attempt < 3; attempt++ {
		// The deadline is expired before start, so the kill lands while git
		// is still spawning; the retry only guards the scheduling race.
		result, err = port.run(gitInvocation{operation: "probe", label: "probe", arguments: []string{"--version"}})
		if err != nil {
			t.Fatal(err)
		}
		if !result.completed {
			break
		}
	}
	if result.completed || result.stopReason != "timeout" {
		t.Fatalf("expired deadline must classify as timeout, got %+v", result)
	}
	if err := gitAssertSuccess(result, "probe"); err == nil || err.Error() != "BF_BLOCKED: probe did not complete within the bounded wait." {
		t.Fatalf("timeout classification message is wrong: %v", err)
	}
}

func TestGitRunPushWatchdogAbandonsWithoutKilling(t *testing.T) {
	testGitBinary(t)
	port, err := NewCLIGitPort(GitPortConfig{
		WorkDir:     t.TempDir(),
		ScratchDir:  filepath.Join(t.TempDir(), "scratch"),
		Baseline:    CommitID(strings.Repeat("a", 40)),
		AuthorName:  "n",
		AuthorEmail: "n@example.invalid",
	})
	if err != nil {
		t.Fatal(err)
	}
	port.timeout = time.Nanosecond
	result, err := port.run(gitInvocation{
		operation: "push-create-only",
		label:     "push-create-only",
		arguments: []string{"--version"},
		keepAlive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.completed || result.stopReason != "timeout" {
		t.Fatalf("push watchdog must abandon the wait, got %+v", result)
	}
	if err := gitAssertSuccess(result, "push-create-only"); err == nil ||
		err.Error() != "BF_BLOCKED: push-create-only did not complete within the bounded wait." {
		t.Fatalf("abandoned push classification message is wrong: %v", err)
	}
}

func TestGitRunOutputLimitKills(t *testing.T) {
	testGitBinary(t)
	port, err := NewCLIGitPort(GitPortConfig{
		WorkDir:     t.TempDir(),
		ScratchDir:  filepath.Join(t.TempDir(), "scratch"),
		Baseline:    CommitID(strings.Repeat("a", 40)),
		AuthorName:  "n",
		AuthorEmail: "n@example.invalid",
	})
	if err != nil {
		t.Fatal(err)
	}
	port.maxOutputBytes = 4
	result, err := port.run(gitInvocation{operation: "probe", label: "probe", arguments: []string{"--version"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.completed || result.stopReason != "output_limit" {
		t.Fatalf("output overflow must classify as an incomplete call, got %+v", result)
	}
	if err := gitAssertSuccess(result, "probe"); err == nil ||
		err.Error() != "BF_BLOCKED: probe did not complete within the bounded wait." {
		t.Fatalf("output-limit classification message is wrong: %v", err)
	}
}
