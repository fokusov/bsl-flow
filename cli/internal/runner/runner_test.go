package runner

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	task1 = "11111111-1111-4111-8111-111111111111"
	task2 = "22222222-2222-4222-8222-222222222222"
	task3 = "33333333-3333-4333-8333-333333333333"
)

var ownershipBase = time.Date(2026, time.September, 12, 9, 0, 0, 0, time.UTC)

type fixtureScenario struct {
	Description        string            `json:"description"`
	Journal            string            `json:"journal"`
	ParseJournalError  string            `json:"parse_journal_error"`
	ReplayError        string            `json:"replay_error"`
	LeaseTTLMinutes    float64           `json:"lease_ttl_minutes"`
	Expect             fixtureExpect     `json:"expect"`
	Dispatch           []fixtureDispatch `json:"dispatch"`
	Next               []fixtureNext     `json:"next"`
	Ownership          []fixtureOwner    `json:"ownership"`
	NextAfterOwnership []fixtureNext     `json:"next_after_ownership"`
}

type fixtureExpect struct {
	NeedsReconciliation bool              `json:"needs_reconciliation"`
	Running             map[string]string `json:"running"`
	Pending             []string          `json:"pending"`
	Recovery            []string          `json:"recovery"`
	Cursor              int               `json:"cursor"`
	LastKind            map[string]string `json:"last_kind"`
	LastStatus          map[string]string `json:"last_status"`
}

type fixtureDispatch struct {
	Task           string `json:"task"`
	OK             bool   `json:"ok"`
	ReasonContains string `json:"reason_contains"`
}

type fixtureNext struct {
	Cursor        int    `json:"cursor"`
	Task          string `json:"task"`
	NextCursor    int    `json:"next_cursor"`
	ErrorContains string `json:"error_contains"`
}

type fixtureOwner struct {
	Op            string  `json:"op"`
	Holder        string  `json:"holder"`
	AfterMinutes  float64 `json:"after_minutes"`
	OK            bool    `json:"ok"`
	ErrorContains string  `json:"error_contains"`
}

// TestRunnerScenarios replays every journal fixture in testdata and checks the
// reconstructed queue state, dispatch decisions, fairness cursor behavior and
// ownership transitions against the scenario expectations.
func TestRunnerScenarios(t *testing.T) {
	scenarios := loadScenarios(t)
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.Description, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", scenario.Journal))
			if err != nil {
				t.Fatal(err)
			}
			events, parseErr := ParseJournal(data)
			if scenario.ParseJournalError == "torn" {
				if !errors.Is(parseErr, ErrTornEvent) {
					t.Fatalf("expected a torn journal error, got %v", parseErr)
				}
			} else if parseErr != nil {
				t.Fatalf("journal parse failed: %v", parseErr)
			}
			state, replayErr := Replay(events)
			if scenario.ReplayError == "torn" {
				if !errors.Is(replayErr, ErrTornEvent) {
					t.Fatalf("expected a torn replay error, got %v", replayErr)
				}
			} else if replayErr != nil {
				t.Fatalf("replay failed: %v", replayErr)
			}
			state.LeaseTTL = time.Duration(scenario.LeaseTTLMinutes * float64(time.Minute))

			checkExpectations(t, state, scenario.Expect)
			for _, check := range scenario.Dispatch {
				ok, reason := ShouldDispatch(state, TaskID(check.Task))
				if ok != check.OK {
					t.Fatalf("dispatch(%s) = %v (%q), want %v", check.Task, ok, reason, check.OK)
				}
				if check.ReasonContains != "" && !strings.Contains(reason, check.ReasonContains) {
					t.Fatalf("dispatch(%s) reason %q must contain %q", check.Task, reason, check.ReasonContains)
				}
			}
			checkNextCases(t, scenario.Next, state)
			runOwnershipSteps(t, scenario.Ownership, &state)
			checkNextCases(t, scenario.NextAfterOwnership, state)
		})
	}
}

func loadScenarios(t *testing.T) []fixtureScenario {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	scenarios := make([]fixtureScenario, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var scenario fixtureScenario
		if err := json.Unmarshal(data, &scenario); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if scenario.Journal == "" {
			t.Fatalf("%s: scenario has no journal", path)
		}
		scenarios = append(scenarios, scenario)
	}
	if len(scenarios) == 0 {
		t.Fatal("no runner scenarios found")
	}
	return scenarios
}

func checkExpectations(t *testing.T, state State, expect fixtureExpect) {
	t.Helper()
	if state.NeedsReconciliation != expect.NeedsReconciliation {
		t.Fatalf("NeedsReconciliation = %v, want %v", state.NeedsReconciliation, expect.NeedsReconciliation)
	}
	if len(state.Running) != len(expect.Running) {
		t.Fatalf("running %v, want %v", state.Running, expect.Running)
	}
	for id, attempt := range expect.Running {
		if got, ok := state.Running[TaskID(id)]; !ok || string(got) != attempt {
			t.Fatalf("running[%s] = %q (%v present), want %q", id, got, ok, attempt)
		}
	}
	assertTaskList(t, "PendingOrder", state.PendingOrder, expect.Pending)
	assertTaskList(t, "RecoveryRequired", state.RecoveryRequired, expect.Recovery)
	if state.CursorPosition != expect.Cursor {
		t.Fatalf("CursorPosition = %d, want %d", state.CursorPosition, expect.Cursor)
	}
	for id, kind := range expect.LastKind {
		outcome, ok := state.LastOutcome[TaskID(id)]
		if !ok {
			t.Fatalf("task %s missing from LastOutcome", id)
		}
		if string(outcome.Kind) != kind {
			t.Fatalf("LastOutcome[%s].Kind = %q, want %q", id, outcome.Kind, kind)
		}
	}
	for id, status := range expect.LastStatus {
		outcome, ok := state.LastOutcome[TaskID(id)]
		if !ok {
			t.Fatalf("task %s missing from LastOutcome", id)
		}
		if outcome.Status != status {
			t.Fatalf("LastOutcome[%s].Status = %q, want %q", id, outcome.Status, status)
		}
	}
}

func assertTaskList(t *testing.T, name string, got []TaskID, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
	for index, id := range want {
		if string(got[index]) != id {
			t.Fatalf("%s = %v, want %v", name, got, want)
		}
	}
}

func checkNextCases(t *testing.T, cases []fixtureNext, state State) {
	t.Helper()
	for _, check := range cases {
		task, cursor, err := Next(Cursor{Position: check.Cursor}, state)
		if check.ErrorContains != "" {
			if err == nil {
				t.Fatalf("Next(cursor %d) succeeded with %q, want error containing %q", check.Cursor, task, check.ErrorContains)
			}
			if !strings.Contains(err.Error(), check.ErrorContains) {
				t.Fatalf("Next(cursor %d) error %q must contain %q", check.Cursor, err, check.ErrorContains)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Next(cursor %d) failed: %v", check.Cursor, err)
		}
		if string(task) != check.Task {
			t.Fatalf("Next(cursor %d) = %q, want %q", check.Cursor, task, check.Task)
		}
		if cursor.Position != check.NextCursor {
			t.Fatalf("Next(cursor %d) cursor = %d, want %d", check.Cursor, cursor.Position, check.NextCursor)
		}
	}
}

func runOwnershipSteps(t *testing.T, steps []fixtureOwner, state *State) {
	t.Helper()
	for _, step := range steps {
		at := ownershipBase.Add(time.Duration(step.AfterMinutes * float64(time.Minute)))
		var err error
		switch step.Op {
		case "acquire":
			var ok bool
			ok, err = state.Acquire(step.Holder, at)
			if ok != step.OK {
				t.Fatalf("Acquire(%s) = %v, want %v (err %v)", step.Holder, ok, step.OK, err)
			}
		case "recover":
			err = state.RecoverOwnership(step.Holder, at)
		case "release":
			err = state.Release(step.Holder)
		case "mark_reconciled":
			state.MarkReconciled()
		default:
			t.Fatalf("unknown ownership op %q", step.Op)
		}
		if step.OK && err != nil {
			t.Fatalf("%s(%s) failed: %v", step.Op, step.Holder, err)
		}
		if !step.OK {
			if err == nil {
				t.Fatalf("%s(%s) unexpectedly succeeded", step.Op, step.Holder)
			}
			if step.ErrorContains != "" && !strings.Contains(err.Error(), step.ErrorContains) {
				t.Fatalf("%s(%s) error %q must contain %q", step.Op, step.Holder, err, step.ErrorContains)
			}
		}
	}
}

func journalRecord(overrides map[string]any) string {
	record := map[string]any{
		"schema_version": 1,
		"event_key":      task1 + "|1|run",
		"at":             "2026-09-12T08:00:00.0000000Z",
		"task_id":        task1,
		"revision":       1,
		"action":         "run",
		"status":         "ready",
	}
	for key, value := range overrides {
		record[key] = value
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func TestParseJournalRejectsInvalidRecords(t *testing.T) {
	cases := []struct {
		name     string
		line     string
		contains string
	}{
		{"null status field", journalRecord(map[string]any{"status": nil}), "status"},
		{"extra field", journalRecord(map[string]any{"extra": "x"}) + " ", "runner_event fields"},
		{"unknown action", journalRecord(map[string]any{"action": "enqueued", "event_key": task1 + "|1|enqueued"}), "invalid event action"},
		{"unknown status", journalRecord(map[string]any{"status": "paused"}), "invalid event status"},
		{"identity mismatch", journalRecord(map[string]any{"event_key": task1 + "|2|run"}), "invalid event identity"},
		{"uppercase task", journalRecord(map[string]any{"task_id": "33333333-3333-4333-8333-33333333333F", "event_key": "33333333-3333-4333-8333-33333333333F|1|run"}), "lower-case UUID"},
		{"fractional revision", journalRecord(map[string]any{"revision": 1.5}), "revision"},
		{"revision below minus one", journalRecord(map[string]any{"revision": -2, "event_key": task1 + "|-2|run"}), "revision"},
		{"wrong schema", journalRecord(map[string]any{"schema_version": 2}), "schema_version"},
		{"bad timestamp", journalRecord(map[string]any{"at": "2026-09-12 08:00:00"}), "timestamp"},
		{"not an object", "[1,2]", "invalid record"},
		{"trailing data", journalRecord(nil) + journalRecord(nil), "invalid record"},
	}
	for _, check := range cases {
		check := check
		t.Run(check.name, func(t *testing.T) {
			events, err := ParseJournal([]byte(check.line + "\n"))
			if err == nil {
				t.Fatalf("invalid record accepted: %v", events)
			}
			if errors.Is(err, ErrTornEvent) {
				t.Fatalf("complete invalid record must be a hard error, not torn: %v", err)
			}
			if !strings.Contains(err.Error(), check.contains) {
				t.Fatalf("error %q must contain %q", err, check.contains)
			}
		})
	}
}

func TestParseJournalRejectsNonUTF8(t *testing.T) {
	if _, err := ParseJournal([]byte{'{', 0xff, 0xfe, '}', '\n'}); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("strict UTF-8 not enforced: %v", err)
	}
}

func TestReplayTornFinalTypedEventKeepsStateUsable(t *testing.T) {
	events := []Event{
		{Kind: KindRun, TaskID: task1, Revision: 1, Timestamp: ownershipBase, Payload: Payload{{Key: "status", Value: "ready"}}},
		{Kind: KindObserved, TaskID: task1, Revision: 2, Timestamp: ownershipBase.Add(time.Minute), Payload: Payload{{Key: "status", Value: "completed"}}},
		{Kind: KindRun, TaskID: task2, Revision: 1},
	}
	state, err := Replay(events)
	if !errors.Is(err, ErrTornEvent) {
		t.Fatalf("expected torn replay error, got %v", err)
	}
	if !state.NeedsReconciliation {
		t.Fatal("torn tail must demand reconciliation")
	}
	if outcome := state.LastOutcome[task1]; outcome.Kind != KindObserved || outcome.Status != "completed" {
		t.Fatalf("complete events lost: %+v", outcome)
	}
	if _, _, err := Next(Cursor{}, state); err == nil || !strings.Contains(err.Error(), "reconciliation") {
		t.Fatalf("dispatch refused on unreconciled state: %v", err)
	}
}

func TestReplayInvalidMiddleEventIsHardError(t *testing.T) {
	events := []Event{
		{Kind: KindRun, TaskID: task2, Revision: 1},
		{Kind: KindRun, TaskID: task1, Revision: 1, Timestamp: ownershipBase, Payload: Payload{{Key: "status", Value: "ready"}}},
	}
	if _, err := Replay(events); err == nil || errors.Is(err, ErrTornEvent) {
		t.Fatalf("invalid middle event must be a hard error, got %v", err)
	}
}

func TestReplayDuplicateIdentityConflict(t *testing.T) {
	events := []Event{
		{Kind: KindRun, TaskID: task1, AttemptID: "attempt-1", Revision: 1, Timestamp: ownershipBase},
		{Kind: KindRun, TaskID: task1, AttemptID: "attempt-2", Revision: 1, Timestamp: ownershipBase.Add(time.Minute)},
	}
	_, err := Replay(events)
	if err == nil {
		t.Fatal("conflicting duplicate identity accepted")
	}
	var kindError *ErrorClass
	if !errors.As(err, &kindError) || kindError.Kind != "BF_CONFLICT" {
		t.Fatalf("expected BF_CONFLICT, got %v", err)
	}
}

func TestObservedRunningKeepsAttemptOpen(t *testing.T) {
	events := []Event{
		{Kind: KindRun, TaskID: task1, AttemptID: "attempt-1", Revision: 1, Timestamp: ownershipBase},
		{Kind: KindObserved, TaskID: task1, Revision: 1, Timestamp: ownershipBase.Add(time.Minute), Payload: Payload{{Key: "status", Value: "running"}}},
	}
	state, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	if attempt, running := state.Running[task1]; !running || string(attempt) != "attempt-1" {
		t.Fatalf("running attempt lost: %q %v", attempt, running)
	}
	if ok, reason := ShouldDispatch(state, task1); ok || !strings.Contains(reason, "running") {
		t.Fatalf("running attempt is dispatchable: %v %q", ok, reason)
	}
}

func TestShouldDispatchRejectsUnknownTask(t *testing.T) {
	state, err := Replay(nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := ShouldDispatch(state, "not-a-queued-task"); ok || !strings.Contains(reason, "not part of the supervised queue") {
		t.Fatalf("unknown task dispatchable: %v %q", ok, reason)
	}
}

func TestRecoveredLeaseBlocksDispatchUntilReconciled(t *testing.T) {
	events := []Event{
		{Kind: KindError, TaskID: task1, Revision: -1, Timestamp: ownershipBase, Payload: Payload{{Key: "status", Value: "blocked"}}},
	}
	state, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	state.LeaseTTL = 10 * time.Minute
	if ok, err := state.Acquire("runner-a", ownershipBase); !ok || err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	if err := state.RecoverOwnership("runner-b", ownershipBase.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !state.NeedsReconciliation {
		t.Fatal("recovered lease must demand reconciliation")
	}
	if _, _, err := Next(Cursor{}, state); err == nil {
		t.Fatal("dispatch allowed on recovered ownership without reconciliation")
	}
	state.MarkReconciled()
	task, cursor, err := Next(Cursor{}, state)
	if err != nil {
		t.Fatal(err)
	}
	if task != task1 || cursor.Position != 0 {
		t.Fatalf("Next after reconciliation = %q %d", task, cursor.Position)
	}
}

func TestContendedAcquireReturnsConflictNotTakeover(t *testing.T) {
	state, err := Replay(nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := state.Acquire("runner-a", ownershipBase); !ok || err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	ok, err := state.Acquire("runner-b", ownershipBase.Add(time.Minute))
	if ok || err == nil {
		t.Fatal("second holder took over a live ownership")
	}
	var kindError *ErrorClass
	if !errors.As(err, &kindError) || kindError.Kind != "BF_CONFLICT" {
		t.Fatalf("expected BF_CONFLICT, got %v", err)
	}
}
