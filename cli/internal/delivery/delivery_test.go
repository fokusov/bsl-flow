package delivery

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const testTaskID = "11111111-1111-4111-8111-111111111111"

func remoteKey(remote, ref string) string {
	return remote + "\x00" + ref
}

// fakeGitPort records every call and models a remote whose head only changes
// through its own Push.
type fakeGitPort struct {
	calls     []string
	heads     map[string]CommitID
	headKnown bool
	headErr   error
	pushErr   error
	stageErr  error
	commitErr error
	commitID  CommitID
}

func newFakeGitPort() *fakeGitPort {
	return &fakeGitPort{
		heads:     map[string]CommitID{},
		headKnown: true,
		commitID:  CommitID(strings.Repeat("b", 40)),
	}
}

func (f *fakeGitPort) Stage(path string) error {
	f.calls = append(f.calls, "stage "+path)
	return f.stageErr
}

func (f *fakeGitPort) CommitAll(message string) (CommitID, error) {
	f.calls = append(f.calls, "commit")
	if f.commitErr != nil {
		return "", f.commitErr
	}
	return f.commitID, nil
}

func (f *fakeGitPort) Push(remote, ref string) error {
	f.calls = append(f.calls, "push")
	if f.pushErr != nil {
		return f.pushErr
	}
	if f.commitID != "" {
		f.heads[remoteKey(remote, ref)] = f.commitID
	}
	return nil
}

func (f *fakeGitPort) ReadRemoteHead(remote, ref string) (CommitID, bool, error) {
	f.calls = append(f.calls, "read")
	if f.headErr != nil {
		return "", false, f.headErr
	}
	if !f.headKnown {
		return "", false, nil
	}
	head, ok := f.heads[remoteKey(remote, ref)]
	if !ok {
		return "", true, nil
	}
	return head, true, nil
}

func (f *fakeGitPort) count(operation string) int {
	total := 0
	for _, call := range f.calls {
		if strings.HasPrefix(call, operation+" ") || call == operation {
			total++
		}
	}
	return total
}

func errorKind(err error) string {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	return ""
}

func testFiles() []ManifestFile {
	return []ManifestFile{
		{Path: "src/main.go", SHA256: sha256Hex([]byte("package main\n"))},
		{Path: "src/util/util.go", SHA256: sha256Hex([]byte("package util\n"))},
		{Path: "tests/main_test.go", SHA256: sha256Hex([]byte("package main_test\n"))},
		{Path: "legacy/old.txt", Deleted: true},
	}
}

func acceptedWith(t *testing.T, files ...ManifestFile) AcceptedSource {
	t.Helper()
	manifest := Manifest{SchemaVersion: 1, Baseline: CommitID(strings.Repeat("a", 40)), Files: files}
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

func testTarget(t *testing.T) Target {
	t.Helper()
	return Target{
		Remote:       filepath.Join(t.TempDir(), "remote.git"),
		Ref:          "refs/heads/codex/native-delivery-test",
		AuthorizedBy: "none",
		AllowedPaths: []string{"src", "tests", "legacy"},
	}
}

func driveTo(t *testing.T, plan Plan, git GitPort, stop Stage) State {
	t.Helper()
	state, err := Begin(plan)
	if err != nil {
		t.Fatal(err)
	}
	for state.Stage != stop {
		next, _, err := Step(plan, state, git)
		if err != nil {
			t.Fatal(err)
		}
		state = next
	}
	return state
}

func TestPublicationHappyPathReachesVerified(t *testing.T) {
	accepted := acceptedWith(t, testFiles()...)
	target := testTarget(t)
	plan, err := BuildPlan(target, accepted)
	if err != nil {
		t.Fatal(err)
	}
	state, err := Begin(plan)
	if err != nil {
		t.Fatal(err)
	}
	git := newFakeGitPort()
	var outcome Outcome
	for state.Stage != StageVerified {
		state, outcome, err = Step(plan, state, git)
		if err != nil {
			t.Fatal(err)
		}
	}
	if outcome != OutcomeVerified || state.Stage != StageVerified {
		t.Fatalf("publication finished with %s/%s", state.Stage, outcome)
	}
	if git.count("stage") != 3 || git.count("commit") != 1 || git.count("push") != 1 {
		t.Fatalf("unexpected side effects: %v", git.calls)
	}
	staged := []string{}
	for _, call := range git.calls {
		if strings.HasPrefix(call, "stage ") {
			staged = append(staged, strings.TrimPrefix(call, "stage "))
		}
	}
	expected := []string{"src/main.go", "src/util/util.go", "tests/main_test.go"}
	if strings.Join(staged, ",") != strings.Join(expected, ",") {
		t.Fatalf("staging order changed: %v", staged)
	}
	persisted, err := state.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	again, err := state.Bytes()
	if err != nil || !bytes.Equal(persisted, again) {
		t.Fatalf("state bytes are not deterministic")
	}
	idle := newFakeGitPort()
	resumed, outcome, err := Resume(persisted, idle)
	if err != nil || outcome != OutcomeVerified || resumed.Stage != StageVerified {
		t.Fatalf("verified publication did not replay idempotently: %v %s %s", err, outcome, resumed.Stage)
	}
	if len(idle.calls) != 0 {
		t.Fatalf("verified replay touched the remote: %v", idle.calls)
	}
}

func TestResumeAfterCrashBetweenCommitAndPushCompletesWithoutSecondCommit(t *testing.T) {
	accepted := acceptedWith(t, testFiles()...)
	plan, err := BuildPlan(testTarget(t), accepted)
	if err != nil {
		t.Fatal(err)
	}
	git := newFakeGitPort()
	state := driveTo(t, plan, git, StageCommitted)
	if git.count("commit") != 1 {
		t.Fatalf("expected exactly one commit before the crash")
	}
	persisted, err := state.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	recovery := newFakeGitPort()
	recovery.commitID = state.Commit
	resumed, outcome, err := Resume(persisted, recovery)
	if err != nil || outcome != OutcomeVerified || resumed.Stage != StageVerified {
		t.Fatalf("resume did not complete: %v %s %s", err, outcome, resumed.Stage)
	}
	if recovery.count("stage") != 0 {
		t.Fatalf("resume re-staged files: %v", recovery.calls)
	}
	if recovery.count("commit") != 0 {
		t.Fatalf("resume committed twice: %v", recovery.calls)
	}
	if recovery.count("push") != 1 {
		t.Fatalf("resume did not push exactly once: %v", recovery.calls)
	}
	if strings.Join(recovery.calls, ",") != "read,push,read" {
		t.Fatalf("unexpected resume call log: %v", recovery.calls)
	}
}

func TestPushUnknownEffectBlocksReconciliationWithoutReplay(t *testing.T) {
	accepted := acceptedWith(t, testFiles()...)
	plan, err := BuildPlan(testTarget(t), accepted)
	if err != nil {
		t.Fatal(err)
	}
	git := newFakeGitPort()
	git.pushErr = errors.New("transport lost after dispatch")
	state := driveTo(t, plan, git, StageCommitted)
	blockedState, outcome, err := Step(plan, state, git)
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
	unsettled := newFakeGitPort()
	_, outcome, err = Resume(persisted, unsettled)
	if outcome != OutcomeBlockedNeedsReconciliation || err == nil {
		t.Fatalf("unsettled remote must stay blocked: %s %v", outcome, err)
	}
	if unsettled.count("push") != 0 {
		t.Fatalf("blocked resume re-pushed: %v", unsettled.calls)
	}
	unknownHead := newFakeGitPort()
	unknownHead.headKnown = false
	_, outcome, err = Resume(persisted, unknownHead)
	if outcome != OutcomeBlockedNeedsReconciliation || err == nil {
		t.Fatalf("unknown remote head must stay blocked: %s %v", outcome, err)
	}
	if unknownHead.count("push") != 0 {
		t.Fatalf("unknown-head resume re-pushed: %v", unknownHead.calls)
	}
	reconciled := newFakeGitPort()
	reconciled.heads[remoteKey(plan.Target.Remote, plan.Target.Ref)] = state.Commit
	resumed, outcome, err := Resume(persisted, reconciled)
	if err != nil || outcome != OutcomeVerified || resumed.Stage != StageVerified {
		t.Fatalf("reconciled remote did not complete idempotently: %v %s %s", err, outcome, resumed.Stage)
	}
	if reconciled.count("push") != 0 {
		t.Fatalf("reconciliation re-pushed: %v", reconciled.calls)
	}
}

func TestResumeTreatsMatchingRemoteHeadAsIdempotentVerification(t *testing.T) {
	accepted := acceptedWith(t, testFiles()...)
	target := testTarget(t)
	plan, err := BuildPlan(target, accepted)
	if err != nil {
		t.Fatal(err)
	}
	state := driveTo(t, plan, newFakeGitPort(), StageCommitted)
	persisted, err := state.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	remote := newFakeGitPort()
	remote.heads[remoteKey(target.Remote, target.Ref)] = state.Commit
	resumed, outcome, err := Resume(persisted, remote)
	if err != nil || outcome != OutcomeVerified || resumed.Stage != StageVerified {
		t.Fatalf("matching remote head must verify idempotently: %v %s %s", err, outcome, resumed.Stage)
	}
	if remote.count("push") != 0 || remote.count("commit") != 0 {
		t.Fatalf("idempotent completion repeated side effects: %v", remote.calls)
	}
}

func TestForeignRemoteOIDIsConflictWithoutOverwrite(t *testing.T) {
	accepted := acceptedWith(t, testFiles()...)
	target := testTarget(t)
	plan, err := BuildPlan(target, accepted)
	if err != nil {
		t.Fatal(err)
	}
	state := driveTo(t, plan, newFakeGitPort(), StageCommitted)
	persisted, err := state.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	remote := newFakeGitPort()
	remote.heads[remoteKey(target.Remote, target.Ref)] = CommitID(strings.Repeat("c", 40))
	_, outcome, err := Resume(persisted, remote)
	if err == nil || errorKind(err) != KindConflict {
		t.Fatalf("foreign OID must conflict, got %v", err)
	}
	if outcome == OutcomeVerified {
		t.Fatal("foreign OID was treated as verified")
	}
	if remote.count("push") != 0 {
		t.Fatalf("conflicting remote was overwritten: %v", remote.calls)
	}
}

func TestTamperedEvidenceAndStateAreRejected(t *testing.T) {
	target := testTarget(t)
	tampered := acceptedWith(t, testFiles()...)
	tampered.Manifest.Files[0].SHA256 = sha256Hex([]byte("tampered bytes\n"))
	if _, err := BuildPlan(target, tampered); errorKind(err) != KindBlocked {
		t.Fatalf("tampered manifest must be rejected, got %v", err)
	}
	forged := acceptedWith(t, testFiles()...)
	forged.EvidenceHash = strings.Repeat("0", 64)
	if _, err := BuildPlan(target, forged); errorKind(err) != KindBlocked {
		t.Fatalf("forged evidence hash must be rejected, got %v", err)
	}

	accepted := acceptedWith(t, testFiles()...)
	plan, err := BuildPlan(target, accepted)
	if err != nil {
		t.Fatal(err)
	}
	state := driveTo(t, plan, newFakeGitPort(), StageCommitted)
	persisted, err := state.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(persisted, &object); err != nil {
		t.Fatal(err)
	}
	object["task_id"] = "22222222-2222-4222-8222-222222222222"
	mutated, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Resume(mutated, newFakeGitPort()); errorKind(err) != KindBlocked {
		t.Fatalf("mutated state must fail its integrity hash, got %v", err)
	}
	if _, _, err := Resume([]byte("{"), newFakeGitPort()); err == nil {
		t.Fatal("garbage state bytes were accepted")
	}
	if _, _, err := Resume(append(append([]byte{}, persisted...), 'x'), newFakeGitPort()); err == nil {
		t.Fatal("trailing state bytes were accepted")
	}
}

func TestTargetAndManifestPathValidation(t *testing.T) {
	accepted := acceptedWith(t, testFiles()...)
	localRemote := filepath.Join(t.TempDir(), "repo.git")
	cases := []struct {
		name   string
		target Target
		files  []ManifestFile
		kind   string
	}{
		{
			name:   "traversal inside allowed paths",
			target: Target{Remote: localRemote, Ref: "refs/heads/codex/x", AuthorizedBy: "none", AllowedPaths: []string{"tests/../src"}},
			files:  testFiles(),
			kind:   KindInvalid,
		},
		{
			name:   "case-colliding allowed paths",
			target: Target{Remote: localRemote, Ref: "refs/heads/codex/x", AuthorizedBy: "none", AllowedPaths: []string{"src", "SRC"}},
			files:  testFiles(),
			kind:   KindInvalid,
		},
		{
			name:   "scp-style remote",
			target: Target{Remote: "git@github.com:owner/repo.git", Ref: "refs/heads/codex/x", AuthorizedBy: "none", AllowedPaths: []string{"src"}},
			files:  testFiles(),
			kind:   KindInvalid,
		},
		{
			name:   "https remote without github_cli authorization",
			target: Target{Remote: "https://github.com/owner/repo.git", Ref: "refs/heads/codex/x", AuthorizedBy: "none", AllowedPaths: []string{"src"}},
			files:  testFiles(),
			kind:   KindInvalid,
		},
		{
			name:   "userinfo remote is not the exact target",
			target: Target{Remote: "https://user@github.com/owner/repo.git", Ref: "refs/heads/codex/x", AuthorizedBy: "github_cli", AllowedPaths: []string{"src"}},
			files:  testFiles(),
			kind:   KindInvalid,
		},
		{
			name:   "ref outside the codex namespace",
			target: Target{Remote: localRemote, Ref: "refs/heads/master", AuthorizedBy: "none", AllowedPaths: []string{"src"}},
			files:  testFiles(),
			kind:   KindInvalid,
		},
		{
			name:   "ref with dotdot",
			target: Target{Remote: localRemote, Ref: "refs/heads/codex/a..b", AuthorizedBy: "none", AllowedPaths: []string{"src"}},
			files:  testFiles(),
			kind:   KindInvalid,
		},
		{
			name:   "empty remote",
			target: Target{Ref: "refs/heads/codex/x", AuthorizedBy: "none", AllowedPaths: []string{"src"}},
			files:  testFiles(),
			kind:   KindInvalid,
		},
		{
			name:   "manifest path escapes the allowed set",
			target: Target{Remote: localRemote, Ref: "refs/heads/codex/x", AuthorizedBy: "none", AllowedPaths: []string{"src", "tests"}},
			files:  []ManifestFile{{Path: "docs/readme.md", SHA256: sha256Hex([]byte("docs\n"))}},
			kind:   KindBlocked,
		},
		{
			name:   "manifest traversal path",
			target: Target{Remote: localRemote, Ref: "refs/heads/codex/x", AuthorizedBy: "none", AllowedPaths: []string{"."}},
			files:  []ManifestFile{{Path: "tests/../src/x.go", SHA256: sha256Hex([]byte("x\n"))}},
			kind:   KindInvalid,
		},
		{
			name:   "manifest .git path",
			target: Target{Remote: localRemote, Ref: "refs/heads/codex/x", AuthorizedBy: "none", AllowedPaths: []string{"."}},
			files:  []ManifestFile{{Path: ".git/config", SHA256: sha256Hex([]byte("x\n"))}},
			kind:   KindInvalid,
		},
		{
			name:   "absolute manifest path",
			target: Target{Remote: localRemote, Ref: "refs/heads/codex/x", AuthorizedBy: "none", AllowedPaths: []string{"."}},
			files:  []ManifestFile{{Path: "/etc/passwd", SHA256: sha256Hex([]byte("x\n"))}},
			kind:   KindInvalid,
		},
	}
	for _, testCase := range cases {
		source := acceptedWith(t, testCase.files...)
		_, err := BuildPlan(testCase.target, source)
		if errorKind(err) != testCase.kind {
			t.Fatalf("%s: expected %s, got %v", testCase.name, testCase.kind, err)
		}
	}
	github := Target{Remote: "https://github.com/Owner/Repo.git", Ref: "refs/heads/codex/release", AuthorizedBy: "github_cli", AllowedPaths: []string{"."}}
	if _, err := BuildPlan(github, accepted); err != nil {
		t.Fatalf("exact github target with its authorization must be accepted: %v", err)
	}
}

func TestNonAcceptedSourcesAreRejected(t *testing.T) {
	files := testFiles()
	for _, mutate := range []func(*AcceptedSource){
		func(s *AcceptedSource) { s.Status = "running" },
		func(s *AcceptedSource) { s.Verdict = "FAIL" },
		func(s *AcceptedSource) { s.Mode = "spec" },
	} {
		source := acceptedWith(t, files...)
		mutate(&source)
		if _, err := BuildPlan(testTarget(t), source); errorKind(err) != KindBlocked {
			t.Fatalf("non-accepted source must be rejected with a typed blocker, got %v", err)
		}
		if _, err := Handoff(source, nil); errorKind(err) != KindBlocked {
			t.Fatalf("non-accepted source must not reach handoff, got %v", err)
		}
	}
}

func TestPlanAndStateCanonicalBytesAreDeterministic(t *testing.T) {
	target := testTarget(t)
	accepted := acceptedWith(t, testFiles()...)
	first, err := BuildPlan(target, accepted)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildPlan(target, accepted)
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, err := first.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := second.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("equal plans produced different canonical bytes")
	}
	state := driveTo(t, first, newFakeGitPort(), StageCommitted)
	left, err := state.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	right, err := state.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, right) {
		t.Fatalf("state bytes changed between marshals")
	}
	markup, err := canonicalJSON(map[string]any{"markup": "<a>&amp;</a>"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markup), "<a>") || strings.Contains(string(markup), `\u003`) {
		t.Fatalf("canonical JSON HTML-escaped markup: %s", markup)
	}
}

func TestHandoffVerifiesArtifactBytesAgainstManifest(t *testing.T) {
	accepted := acceptedWith(t, testFiles()...)
	artifacts := []Artifact{
		{Path: "src/main.go", Bytes: []byte("package main\n")},
		{Path: "src/util/util.go", Bytes: []byte("package util\n")},
		{Path: "tests/main_test.go", Bytes: []byte("package main_test\n")},
	}
	receipt, err := Handoff(accepted, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SchemaVersion != 1 || receipt.SourceHash != accepted.EvidenceHash {
		t.Fatalf("receipt identity is wrong: %+v", receipt)
	}
	if receipt.SourceFileCount != 3 || receipt.DeletedFileCount != 1 {
		t.Fatalf("receipt counts are wrong: %+v", receipt)
	}
	if len(receipt.ArtifactHashes) != 3 {
		t.Fatalf("receipt artifact hashes are wrong: %v", receipt.ArtifactHashes)
	}
	for _, file := range accepted.Manifest.Files {
		if file.Deleted {
			continue
		}
		if receipt.ArtifactHashes[file.Path] != file.SHA256 {
			t.Fatalf("receipt hash mismatch for %s", file.Path)
		}
	}
	tampered := append([]Artifact{}, artifacts...)
	tampered[1] = Artifact{Path: "src/util/util.go", Bytes: []byte("package util // tampered\n")}
	if _, err := Handoff(accepted, tampered); errorKind(err) != KindBlocked {
		t.Fatalf("changed artifact bytes must block, got %v", err)
	}
	if _, err := Handoff(accepted, artifacts[:2]); errorKind(err) != KindBlocked {
		t.Fatalf("missing artifact must block, got %v", err)
	}
	extra := append(append([]Artifact{}, artifacts...), Artifact{Path: "docs/readme.md", Bytes: []byte("docs\n")})
	if _, err := Handoff(accepted, extra); errorKind(err) != KindBlocked {
		t.Fatalf("extra artifact must block, got %v", err)
	}
	duplicate := append(append([]Artifact{}, artifacts...), Artifact{Path: "src/main.go", Bytes: []byte("package main\n")})
	if _, err := Handoff(accepted, duplicate); errorKind(err) != KindInvalid {
		t.Fatalf("duplicate artifact must be invalid, got %v", err)
	}
}

func TestHandoffReceiptDeterministicBytes(t *testing.T) {
	accepted := acceptedWith(t, testFiles()...)
	artifacts := []Artifact{
		{Path: "src/main.go", Bytes: []byte("package main\n")},
		{Path: "src/util/util.go", Bytes: []byte("package util\n")},
		{Path: "tests/main_test.go", Bytes: []byte("package main_test\n")},
	}
	HandoffClock = func() string { return "2026-09-13T00:00:00Z" }
	defer func() { HandoffClock = nil }()
	first, err := Handoff(accepted, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Handoff(accepted, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, err := first.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := second.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("receipt bytes are not deterministic")
	}
	if !strings.Contains(string(firstBytes), "2026-09-13T00:00:00Z") {
		t.Fatalf("injected clock value missing from receipt: %s", firstBytes)
	}
	HandoffClock = nil
	third, err := Handoff(accepted, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	thirdBytes, err := third.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(thirdBytes), "handed_off_at") {
		t.Fatalf("nil clock must omit handed_off_at: %s", thirdBytes)
	}
}

func TestStepRejectsStateFromAnotherPlan(t *testing.T) {
	accepted := acceptedWith(t, testFiles()...)
	target := testTarget(t)
	plan, err := BuildPlan(target, accepted)
	if err != nil {
		t.Fatal(err)
	}
	otherTarget := target
	otherTarget.Ref = "refs/heads/codex/another-plan"
	other, err := BuildPlan(otherTarget, accepted)
	if err != nil {
		t.Fatal(err)
	}
	state := driveTo(t, plan, newFakeGitPort(), StageCommitted)
	if _, _, err := Step(other, state, newFakeGitPort()); errorKind(err) != KindConflict {
		t.Fatalf("foreign plan must conflict, got %v", err)
	}
}
