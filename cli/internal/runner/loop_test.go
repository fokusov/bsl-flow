package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const queueID = "44444444-4444-4444-8444-444444444444"

var serveBase = time.Date(2026, time.September, 15, 10, 0, 0, 0, time.UTC)

// fakeClock advances one second per call so journal timestamps are
// deterministic without depending on wall time.
type fakeClock struct {
	mu     sync.Mutex
	moment time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.moment = c.moment.Add(time.Second)
	return c.moment
}

// scriptedSource is the TaskSource fake: a mutable map of task snapshots and
// optional per-task lookup failures.
type scriptedSource struct {
	mu     sync.Mutex
	states map[TaskID]TaskSnapshot
	fail   map[TaskID]error
}

func (s *scriptedSource) Snapshot(_ context.Context, task TaskID) (TaskSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.fail[task]; ok {
		return TaskSnapshot{}, err
	}
	state, ok := s.states[task]
	if !ok {
		return TaskSnapshot{}, invalid("task %s does not exist", task)
	}
	return state, nil
}

func (s *scriptedSource) update(task TaskID, state TaskSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[task] = state
}

// recordingDispatcher records every claimed-task invocation and applies an
// optional scripted effect or error against its source.
type recordingDispatcher struct {
	mu       sync.Mutex
	requests []DispatchRequest
	source   *scriptedSource
	effect   func(req DispatchRequest, source *scriptedSource) error
}

func (d *recordingDispatcher) Dispatch(_ context.Context, req DispatchRequest, _, _ io.Writer) error {
	d.mu.Lock()
	d.requests = append(d.requests, req)
	d.mu.Unlock()
	if d.effect != nil {
		return d.effect(req, d.source)
	}
	return nil
}

func (d *recordingDispatcher) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.requests)
}

func (d *recordingDispatcher) request(index int) DispatchRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.requests[index]
}

// completeTask is the standard dispatch effect: the controller finishes the
// claimed task at revision 2.
func completeTask(req DispatchRequest, source *scriptedSource) error {
	source.update(req.Task, TaskSnapshot{Revision: 2, Status: StatusCompleted})
	return nil
}

// serveFixture wires Serve against fakes and a real FileStore in a temporary
// project directory.
type serveFixture struct {
	project    string
	tasks      []string
	source     *scriptedSource
	dispatcher *recordingDispatcher
	options    ServeOptions
}

func newServeFixture(t *testing.T, input QueueInput) *serveFixture {
	t.Helper()
	project := t.TempDir()
	source := &scriptedSource{states: map[TaskID]TaskSnapshot{}}
	dispatcher := &recordingDispatcher{source: source}
	encoded, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"queue_id":       input.QueueID,
		"task_ids":       input.TaskIDs,
		"poll_seconds":   input.PollSeconds,
		"max_cycles":     input.MaxCycles,
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(project)
	if err != nil {
		t.Fatal(err)
	}
	return &serveFixture{
		project:    project,
		tasks:      input.TaskIDs,
		source:     source,
		dispatcher: dispatcher,
		options: ServeOptions{
			Project:  project,
			Input:    encoded,
			Tasks:    source,
			Store:    store,
			Dispatch: dispatcher,
			Now:      (&fakeClock{moment: serveBase}).now,
			Sleep:    func(context.Context, time.Duration) error { return nil },
			GitRoot:  func(context.Context, string) (string, error) { return project, nil },
		},
	}
}

func (f *serveFixture) run(t *testing.T) (int, string) {
	t.Helper()
	var out strings.Builder
	var errOut strings.Builder
	code := Serve(context.Background(), f.options, &out, &errOut)
	return code, out.String()
}

func (f *serveFixture) runnerDir() string {
	return filepath.Join(f.project, ".bsl-flow", "runner")
}

func (f *serveFixture) journalText(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.runnerDir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (f *serveFixture) journalKeys(t *testing.T) []string {
	t.Helper()
	events, err := ParseJournal([]byte(f.journalText(t)))
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(events))
	for _, event := range events {
		keys = append(keys, event.Key())
	}
	return keys
}

func (f *serveFixture) storedSnapshot(t *testing.T) Snapshot {
	t.Helper()
	input := mustParseQueueInput(t, f.options.Input)
	snapshot, found, err := f.options.Store.LoadSnapshot(input)
	if err != nil || !found {
		t.Fatalf("stored snapshot missing: found=%v err=%v", found, err)
	}
	return snapshot
}

func mustParseQueueInput(t *testing.T, data []byte) QueueInput {
	t.Helper()
	input, err := ParseQueueInput(data)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func readyState(revision int) TaskSnapshot {
	return TaskSnapshot{Revision: revision, Status: StatusReady, NextAction: "dispatch"}
}

func queueInputFor(cycles int, tasks ...string) QueueInput {
	return QueueInput{SchemaVersion: 1, QueueID: queueID, TaskIDs: tasks, PollSeconds: 1, MaxCycles: cycles}
}

// enqueue registers the fixture tasks in the scripted source.
func (f *serveFixture) enqueue(states ...TaskSnapshot) {
	for index, id := range f.tasks {
		f.source.update(TaskID(id), states[index])
	}
}

// funcLiveness adapts a probe function to the Liveness seam.
type funcLiveness func(ProcessIdentity) bool

func (f funcLiveness) Alive(identity ProcessIdentity) bool { return f(identity) }

func TestDecideActions(t *testing.T) {
	live := funcLiveness(func(ProcessIdentity) bool { return true })
	dead := funcLiveness(func(ProcessIdentity) bool { return false })
	cases := []struct {
		name     string
		snapshot TaskSnapshot
		alive    Liveness
		action   Action
	}{
		{"cancelled is quiet", TaskSnapshot{Status: StatusCancelled, NextAction: "cancelled"}, dead, ActionQuiet},
		{"unresolved effect is quiet", TaskSnapshot{Status: StatusReady, UnresolvedEffect: true}, dead, ActionQuiet},
		{"live controller keeps quiet", TaskSnapshot{Status: StatusRunning, ActiveAttempt: "a", Controller: &ProcessIdentity{PID: 1}}, live, ActionQuiet},
		{"live owned process keeps quiet", TaskSnapshot{Status: StatusRunning, ActiveAttempt: "a", AttemptStage: "code", OwnedProcesses: []ProcessIdentity{{PID: 2}}}, live, ActionQuiet},
		{"dead readonly attempt resumes", TaskSnapshot{Status: StatusRunning, ActiveAttempt: "a", AttemptStage: "inspect"}, dead, ActionResumeReadonly},
		{"dead modifying attempt requires recovery", TaskSnapshot{Status: StatusRunning, ActiveAttempt: "a", AttemptStage: "code"}, dead, ActionRecoveryRequired},
		{"dead attempt without stage requires recovery", TaskSnapshot{Status: StatusRunning, ActiveAttempt: "a"}, dead, ActionRecoveryRequired},
		{"completed without fresh acceptance is stale", TaskSnapshot{Status: StatusCompleted, AcceptanceStale: true}, dead, ActionCompletedStale},
		{"ready dispatch is claimed", TaskSnapshot{Status: StatusReady, NextAction: "dispatch"}, dead, ActionRun},
		{"ready accept is claimed", TaskSnapshot{Status: StatusReady, NextAction: "accept"}, dead, ActionRun},
		{"ready recover stays quiet", TaskSnapshot{Status: StatusReady, NextAction: "recover"}, dead, ActionQuiet},
		{"blocked stays quiet", TaskSnapshot{Status: StatusBlocked, NextAction: "blocked"}, dead, ActionQuiet},
		{"dispatch on non-ready stays quiet", TaskSnapshot{Status: StatusNeedsInput, NextAction: "dispatch"}, dead, ActionQuiet},
		{"completed fresh stays quiet", TaskSnapshot{Status: StatusCompleted}, dead, ActionQuiet},
	}
	for _, check := range cases {
		check := check
		t.Run(check.name, func(t *testing.T) {
			if got := Decide(check.snapshot, check.alive); got != check.action {
				t.Fatalf("Decide = %q, want %q", got, check.action)
			}
		})
	}
}

func TestParseQueueInputValidation(t *testing.T) {
	valid := func(overrides map[string]any) []byte {
		document := map[string]any{
			"schema_version": 1,
			"queue_id":       queueID,
			"task_ids":       []string{task1},
			"poll_seconds":   5,
			"max_cycles":     10,
		}
		for key, value := range overrides {
			if value == nil {
				delete(document, key)
				continue
			}
			document[key] = value
		}
		encoded, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	extraField := []byte(`{"schema_version":1,"queue_id":"` + queueID + `","task_ids":["` + task1 + `"],"poll_seconds":5,"max_cycles":10,"extra":1}`)
	cases := []struct {
		name     string
		data     []byte
		contains string
	}{
		{"not an object", []byte("[]"), "task_queue must be an object"},
		{"missing field", valid(map[string]any{"poll_seconds": nil}), "poll_seconds"},
		{"extra field", extraField, "task_queue fields"},
		{"schema two", valid(map[string]any{"schema_version": 2}), "schema_version"},
		{"bad queue uuid", valid(map[string]any{"queue_id": "not-a-uuid"}), "UUID"},
		{"empty tasks", valid(map[string]any{"task_ids": []string{}}), "non-empty array"},
		{"bad task uuid", valid(map[string]any{"task_ids": []string{"x"}}), "UUID"},
		{"duplicate tasks", valid(map[string]any{"task_ids": []string{task1, task1}}), "unique"},
		{"fractional poll", []byte(`{"schema_version":1,"queue_id":"` + queueID + `","task_ids":["` + task1 + `"],"poll_seconds":1.5,"max_cycles":1}`), "poll_seconds"},
		{"poll zero", valid(map[string]any{"poll_seconds": 0}), "poll_seconds"},
		{"poll too large", valid(map[string]any{"poll_seconds": 61}), "poll_seconds"},
		{"cycles zero", valid(map[string]any{"max_cycles": 0}), "max_cycles"},
		{"cycles too large", valid(map[string]any{"max_cycles": 10001}), "max_cycles"},
	}
	for _, check := range cases {
		check := check
		t.Run(check.name, func(t *testing.T) {
			if _, err := ParseQueueInput(check.data); err == nil {
				t.Fatal("invalid queue input accepted")
			} else if !strings.Contains(err.Error(), check.contains) {
				t.Fatalf("error %q must contain %q", err, check.contains)
			}
		})
	}
}

func TestQueueInputCanonicalAndHash(t *testing.T) {
	// Field order in the input document must not matter: the canonical form
	// is the key-sorted shape the PowerShell writer persists.
	input, err := ParseQueueInput([]byte(`{"task_ids":["` + task1 + `","` + task2 + `"],"queue_id":"` + queueID + `","max_cycles":10,"poll_seconds":5,"schema_version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"max_cycles":10,"poll_seconds":5,"queue_id":"` + queueID + `","schema_version":1,"task_ids":["` + task1 + `","` + task2 + `"]}`
	encoded, err := input.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != want {
		t.Fatalf("canonical = %s, want %s", encoded, want)
	}
	if hash := input.Hash(); len(hash) != 64 || strings.ToUpper(hash) == hash {
		t.Fatalf("hash must be 64 lowercase hex chars: %q", hash)
	}
}

func TestSnapshotCanonicalGolden(t *testing.T) {
	input := queueInputFor(2, task1, task2)
	updatedAt := time.Date(2026, time.September, 15, 9, 30, 0, 0, time.UTC)
	snapshot := Snapshot{
		QueueID:     queueID,
		QueueSHA256: input.Hash(),
		Cycle:       2,
		Cursor:      1,
		Tasks: []SnapshotTask{
			{TaskID: task1, LastKey: task1 + "|3|quiet", Revision: 3, Status: StatusCompleted, Action: "quiet"},
			{TaskID: task2, LastKey: task2 + "|1|run", Revision: 1, Status: StatusBlocked, Action: "error", Error: `he said "stop"\now`},
		},
		EventKeys: []string{task1 + "|1|run", task1 + "|3|observed"},
		UpdatedAt: updatedAt,
	}
	want := `{"cycle":2,"cursor":1,"event_keys":["` + task1 + `|1|run","` + task1 + `|3|observed"],` +
		`"queue_id":"` + queueID + `","queue_sha256":"` + input.Hash() + `","schema_version":1,` +
		`"tasks":{"` + task1 + `":{"action":"quiet","last_key":"` + task1 + `|3|quiet","revision":3,"status":"completed"},` +
		`"` + task2 + `":{"action":"error","error":"he said \"stop\"\\now","last_key":"` + task2 + `|1|run","revision":1,"status":"blocked"}},` +
		`"updated_at":"2026-09-15T09:30:00.0000000Z"}`
	encoded, err := appendSnapshotCanonical(nil, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != want {
		t.Fatalf("canonical snapshot:\n%s\nwant:\n%s", encoded, want)
	}
	parsed, err := parseSnapshot(encoded, input)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Cycle != 2 || parsed.Cursor != 1 || len(parsed.Tasks) != 2 || parsed.Tasks[0].TaskID != TaskID(task1) {
		t.Fatalf("round trip lost fields: %+v", parsed)
	}
	if parsed.Tasks[1].Error != snapshot.Tasks[1].Error {
		t.Fatalf("round trip lost error text: %q", parsed.Tasks[1].Error)
	}
}

func TestFileStoreAppendEventByteParity(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	event := Event{
		Kind:      KindRun,
		TaskID:    task1,
		Revision:  7,
		Timestamp: time.Date(2026, time.September, 15, 8, 0, 0, 0, time.UTC),
		Payload:   Payload{{Key: "status", Value: StatusReady}},
	}
	if err := store.AppendEvent(event); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(store.RunnerDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"action":"run","at":"2026-09-15T08:00:00.0000000Z","event_key":"` + task1 + `|7|run","revision":7,"schema_version":1,"status":"ready","task_id":"` + task1 + `"}` + journalNewline()
	if string(data) != want {
		t.Fatalf("journal bytes:\n%q\nwant:\n%q", data, want)
	}
	events, err := store.Events()
	if err != nil || len(events) != 1 || events[0].Key() != event.Key() {
		t.Fatalf("journal reload failed: %v %v", events, err)
	}
}

func TestRunnerLockIsExclusive(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	release, err := store.Lock()
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Lock()
	if err == nil {
		t.Fatal("second lock acquired")
	}
	var classError *ErrorClass
	if !errors.As(err, &classError) || classError.Kind != "BF_CONFLICT" {
		t.Fatalf("expected BF_CONFLICT, got %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	release, err = store.Lock()
	if err != nil {
		t.Fatalf("lock not reusable after release: %v", err)
	}
	_ = release()
}

func TestDispatchArguments(t *testing.T) {
	run := dispatchArguments(DispatchRequest{Project: `C:\p`, Task: task1, Action: ActionRun, CodexPath: `C:\codex.exe`})
	want := []string{"task", "run", "--project", `C:\p`, "--task", task1, "--codex", `C:\codex.exe`}
	if strings.Join(run, " ") != strings.Join(want, " ") {
		t.Fatalf("run argv = %v, want %v", run, want)
	}
	resume := dispatchArguments(DispatchRequest{Project: `C:\p`, Task: task1, Action: ActionResumeReadonly, CodexPath: `C:\codex.exe`})
	want = []string{"task", "resume", "--project", `C:\p`, "--task", task1}
	if strings.Join(resume, " ") != strings.Join(want, " ") {
		t.Fatalf("resume argv = %v, want %v (resume carries no codex)", resume, want)
	}
	auth := dispatchArguments(DispatchRequest{Project: `C:\p`, Task: task1, Action: ActionRun, RuntimeAuthLine: `{"u":"p"}`})
	if auth[len(auth)-2] != "--runtime-auth" || auth[len(auth)-1] != "stdin" {
		t.Fatalf("runtime auth argv = %v", auth)
	}
}

func TestLimitWriterBoundsCombinedOutput(t *testing.T) {
	var builder strings.Builder
	shared := &sharedLimit{remaining: 5}
	writer := &limitWriter{w: &builder, shared: shared}
	if n, err := writer.Write([]byte("1234567890")); n != 10 || err != nil {
		t.Fatalf("write = %d %v", n, err)
	}
	if n, err := writer.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("write = %d %v", n, err)
	}
	if builder.String() != "12345" {
		t.Fatalf("bounded output = %q", builder.String())
	}
}

func TestServeFailureClassifiesByPrefix(t *testing.T) {
	cases := []struct {
		err  error
		code int
	}{
		{errors.New("BF_INVALID: bad input"), 2},
		{errors.New("BF_CONFLICT: someone else"), 3},
		{errors.New("BF_BLOCKED: refused"), 11},
		{errors.New("BF_FAIL: verification"), 12},
		{errors.New("plain failure"), 4},
		{invalid("typed invalid"), 2},
	}
	for _, check := range cases {
		var out strings.Builder
		code := serveFailure(&out, check.err)
		if code != check.code {
			t.Fatalf("serveFailure(%v) = %d, want %d (%s)", check.err, code, check.code, out.String())
		}
		var envelope struct {
			Status   string   `json:"status"`
			Blockers []string `json:"blockers"`
		}
		if err := json.Unmarshal([]byte(out.String()), &envelope); err != nil {
			t.Fatalf("envelope %q: %v", out.String(), err)
		}
		wantStatus := "blocked"
		if check.code == 12 {
			wantStatus = "failed"
		}
		if envelope.Status != wantStatus || len(envelope.Blockers) != 1 {
			t.Fatalf("envelope %q must carry one %s blocker", out.String(), wantStatus)
		}
		if check.code != 4 && !strings.HasPrefix(envelope.Blockers[0], "BF_") {
			t.Fatalf("envelope %q must carry the BF blocker", out.String())
		}
	}
}

func TestSafeProjectPath(t *testing.T) {
	if _, err := safeProjectPath("relative/path"); err == nil {
		t.Fatal("relative path accepted")
	}
	if _, err := safeProjectPath(`\\?\C:\dev`); err == nil {
		t.Fatal("device path accepted")
	}
	if _, err := safeProjectPath("registry::HKLM"); err == nil {
		t.Fatal("provider path accepted")
	}
	temp := t.TempDir()
	resolved, err := safeProjectPath(temp)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != filepath.Clean(temp) {
		t.Fatalf("resolved = %q, want %q", resolved, filepath.Clean(temp))
	}
}

// TestServeDrainsQueueAndDedupsOnRestart is the happy-path integration: two
// ready tasks are claimed and observed, the fairness cursor persists, and a
// second serve run replays the journal without duplicating any event.
func TestServeDrainsQueueAndDedupsOnRestart(t *testing.T) {
	fixture := newServeFixture(t, queueInputFor(2, task1, task2))
	fixture.dispatcher.effect = completeTask
	fixture.enqueue(readyState(1), readyState(1))
	code, output := fixture.run(t)
	if code != 0 {
		t.Fatalf("exit code = %d, output %s", code, output)
	}
	if fixture.dispatcher.count() != 2 {
		t.Fatalf("dispatch count = %d, want 2", fixture.dispatcher.count())
	}
	if got := fixture.dispatcher.request(0).Task; got != TaskID(task1) {
		t.Fatalf("first dispatch = %s", got)
	}
	if got := fixture.dispatcher.request(1).Task; got != TaskID(task2) {
		t.Fatalf("second dispatch = %s", got)
	}
	wantKeys := []string{
		task1 + "|1|run",
		task1 + "|2|observed",
		task2 + "|1|run",
		task2 + "|2|observed",
	}
	if keys := fixture.journalKeys(t); strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("journal keys = %v, want %v", keys, wantKeys)
	}
	snapshot := fixture.storedSnapshot(t)
	if snapshot.Cycle != 2 || snapshot.Cursor != 0 {
		t.Fatalf("snapshot cycle/cursor = %d/%d", snapshot.Cycle, snapshot.Cursor)
	}
	for _, task := range snapshot.Tasks {
		if task.Action != "quiet" || task.Status != StatusCompleted {
			t.Fatalf("task %+v must be observed completed", task)
		}
	}
	var envelope struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil || envelope.Status != "completed" {
		t.Fatalf("envelope status parse failed: %v (%s)", err, output)
	}
	before := fixture.journalText(t)

	// Restart: everything is completed and deduplicated, so nothing new is
	// appended and no dispatch happens.
	code, output = fixture.run(t)
	if code != 0 {
		t.Fatalf("restart exit code = %d, output %s", code, output)
	}
	if fixture.dispatcher.count() != 2 {
		t.Fatalf("restart dispatch count = %d, want 2", fixture.dispatcher.count())
	}
	if after := fixture.journalText(t); after != before {
		t.Fatalf("journal changed on quiet restart:\n%s\n%s", before, after)
	}
}

// TestServeNoBlindRetryAfterExecutionError proves the no-blind-retry gate: a
// failed dispatch records execution_error at the claimed revision and the
// same revision is never dispatched again; only an explicit task update (a
// greater revision) re-arms the task.
func TestServeNoBlindRetryAfterExecutionError(t *testing.T) {
	fixture := newServeFixture(t, queueInputFor(2, task1))
	fixture.dispatcher.effect = func(req DispatchRequest, source *scriptedSource) error {
		return fmt.Errorf("BF_BLOCKED: controller exploded for %s", req.Task)
	}
	fixture.enqueue(readyState(4))
	code, _ := fixture.run(t)
	if code != 11 {
		t.Fatalf("exit code = %d, want 11", code)
	}
	if fixture.dispatcher.count() != 1 {
		t.Fatalf("dispatch count = %d, want exactly one", fixture.dispatcher.count())
	}
	keys := fixture.journalKeys(t)
	if strings.Join(keys, ",") != task1+"|4|run,"+task1+"|4|execution_error" {
		t.Fatalf("journal keys = %v", keys)
	}
	snapshot := fixture.storedSnapshot(t)
	if len(snapshot.Tasks) != 1 || snapshot.Tasks[0].Action != "error" || snapshot.Tasks[0].Status != StatusBlocked {
		t.Fatalf("snapshot after failure: %+v", snapshot.Tasks)
	}

	// Second run at the same revision must stay suppressed.
	code, _ = fixture.run(t)
	if code != 11 || fixture.dispatcher.count() != 1 {
		t.Fatalf("suppressed run dispatched again: code=%d count=%d", code, fixture.dispatcher.count())
	}

	// An explicit task update (operator revision bump) re-arms dispatch.
	fixture.dispatcher.effect = completeTask
	fixture.source.update(task1, readyState(5))
	code, _ = fixture.run(t)
	if code != 0 || fixture.dispatcher.count() != 2 {
		t.Fatalf("post-update run: code=%d count=%d", code, fixture.dispatcher.count())
	}
	keys = fixture.journalKeys(t)
	if strings.Join(keys, ",") != task1+"|4|run,"+task1+"|4|execution_error,"+task1+"|5|run,"+task1+"|2|observed" {
		t.Fatalf("journal keys after update = %v", keys)
	}
}

// TestServeRecoveryAfterCrashedDispatch covers resume after an unknown
// effect: a crash between the persisted run marker and the observation leaves
// an active attempt; a dead modifying attempt blocks with recovery_required
// and is never dispatched, while a dead read-only attempt resumes.
func TestServeRecoveryAfterCrashedDispatch(t *testing.T) {
	fixture := newServeFixture(t, queueInputFor(1, task1))
	// Seed the durable artifacts of a runner that crashed after persisting
	// the dispatch marker but before observing the result.
	input := mustParseQueueInput(t, fixture.options.Input)
	crashed := Event{
		Kind:      KindRun,
		TaskID:    task1,
		Revision:  3,
		Timestamp: serveBase.Add(time.Minute),
		Payload:   Payload{{Key: "status", Value: StatusReady}},
	}
	if err := fixture.options.Store.AppendEvent(crashed); err != nil {
		t.Fatal(err)
	}
	marker := Snapshot{
		QueueID:     input.QueueID,
		QueueSHA256: input.Hash(),
		Cycle:       1,
		Cursor:      1,
		Tasks:       []SnapshotTask{{TaskID: task1, LastKey: crashed.Key(), Revision: 3, Status: StatusReady, Action: "run"}},
		EventKeys:   []string{crashed.Key()},
		UpdatedAt:   serveBase.Add(time.Minute),
	}
	if err := fixture.options.Store.SaveSnapshot(marker); err != nil {
		t.Fatal(err)
	}

	// The interrupted attempt is modifying and dead: recovery_required.
	fixture.enqueue(TaskSnapshot{Revision: 3, Status: StatusRunning, ActiveAttempt: "a1", AttemptStage: "code"})
	code, output := fixture.run(t)
	if code != 11 {
		t.Fatalf("exit code = %d, want 11 (%s)", code, output)
	}
	if fixture.dispatcher.count() != 0 {
		t.Fatal("recovery_required must not dispatch")
	}
	keys := fixture.journalKeys(t)
	if strings.Join(keys, ",") != crashed.Key()+","+task1+"|3|recovery_required" {
		t.Fatalf("journal keys = %v", keys)
	}
	snapshot := fixture.storedSnapshot(t)
	if snapshot.Tasks[0].Status != StatusBlocked || !strings.Contains(snapshot.Tasks[0].Error, "automatic replay is forbidden") {
		t.Fatalf("recovery entry: %+v", snapshot.Tasks[0])
	}

	// A dead read-only attempt is resumed and observed instead.
	readonlyFixture := newServeFixture(t, queueInputFor(1, task1))
	readonlyFixture.dispatcher.effect = completeTask
	readonlyFixture.enqueue(TaskSnapshot{Revision: 3, Status: StatusRunning, ActiveAttempt: "a1", AttemptStage: "inspect"})
	code, output = readonlyFixture.run(t)
	if code != 0 {
		t.Fatalf("readonly resume exit code = %d (%s)", code, output)
	}
	if readonlyFixture.dispatcher.count() != 1 {
		t.Fatalf("resume dispatch count = %d", readonlyFixture.dispatcher.count())
	}
	if req := readonlyFixture.dispatcher.request(0); req.Action != ActionResumeReadonly || req.CodexPath != "" {
		t.Fatalf("resume request = %+v", req)
	}
	keys = readonlyFixture.journalKeys(t)
	if strings.Join(keys, ",") != task1+"|3|resume_readonly,"+task1+"|2|observed" {
		t.Fatalf("readonly journal keys = %v", keys)
	}
}

func TestServeTornJournalRefusesToResume(t *testing.T) {
	fixture := newServeFixture(t, queueInputFor(1, task1))
	fixture.enqueue(readyState(1))
	torn := `{"action":"run","at":"2026-09-15T10:00:00.0000000Z","event_key":"` + task1 + `|1|run","revision":1,"schema_version":1,"status":"ready","task_id":"` + task1 + `"}`
	if err := os.MkdirAll(fixture.runnerDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.runnerDir(), "events.jsonl"), []byte(torn), 0o644); err != nil {
		t.Fatal(err)
	}
	code, output := fixture.run(t)
	if code != 11 {
		t.Fatalf("exit code = %d, want 11", code)
	}
	if !strings.Contains(output, "BF_BLOCKED") || !strings.Contains(output, "incomplete final record") {
		t.Fatalf("failure envelope: %s", output)
	}
	if fixture.dispatcher.count() != 0 {
		t.Fatal("torn journal must not dispatch")
	}
}

func TestServeQueueInputImmutabilityConflict(t *testing.T) {
	fixture := newServeFixture(t, queueInputFor(1, task1))
	fixture.dispatcher.effect = completeTask
	fixture.enqueue(readyState(1))
	if code, output := fixture.run(t); code != 0 {
		t.Fatalf("first run failed: %d %s", code, output)
	}
	// Same queue id, different content.
	fixture.options.Input = []byte(`{"schema_version":1,"queue_id":"` + queueID + `","task_ids":["` + task1 + `"],"poll_seconds":2,"max_cycles":1}`)
	code, output := fixture.run(t)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	if !strings.Contains(output, "queue_id belongs to a different immutable queue input") {
		t.Fatalf("conflict envelope: %s", output)
	}
}

// TestServeCursorFairnessAndNeedsInput checks the observed needs_input path,
// the persisted fairness cursor and quiet-observation dedup across restarts.
func TestServeCursorFairnessAndNeedsInput(t *testing.T) {
	fixture := newServeFixture(t, queueInputFor(1, task1, task2))
	fixture.dispatcher.effect = completeTask
	fixture.enqueue(
		TaskSnapshot{Revision: 1, Status: StatusNeedsInput, NextAction: "needs_input"},
		readyState(1),
	)
	code, output := fixture.run(t)
	if code != 10 {
		t.Fatalf("exit code = %d, want 10 (%s)", code, output)
	}
	if fixture.dispatcher.count() != 1 || fixture.dispatcher.request(0).Task != TaskID(task2) {
		t.Fatalf("only task2 may dispatch: %+v", fixture.dispatcher.requests)
	}
	snapshot := fixture.storedSnapshot(t)
	if snapshot.Cursor != 0 {
		t.Fatalf("cursor = %d, want 0", snapshot.Cursor)
	}
	quietJournal := fixture.journalText(t)

	// The operator answers; the next run resumes after the cursor at task1.
	fixture.source.update(task1, readyState(2))
	code, output = fixture.run(t)
	if code != 0 {
		t.Fatalf("exit code = %d (%s)", code, output)
	}
	if fixture.dispatcher.count() != 2 || fixture.dispatcher.request(1).Task != TaskID(task1) {
		t.Fatalf("cursor fairness broken: %+v", fixture.dispatcher.requests)
	}
	events := fixture.journalKeys(t)
	joined := strings.Join(events, ",")
	if !strings.Contains(joined, task1+"|1|observed") {
		t.Fatalf("needs_input observation missing: %v", events)
	}
	if strings.Count(joined, task1+"|1|observed") != 1 {
		t.Fatalf("quiet observation duplicated: %v", events)
	}
	if fixture.journalText(t) == quietJournal {
		t.Fatal("second run appended nothing")
	}
}
