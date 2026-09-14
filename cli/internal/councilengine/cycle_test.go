package councilengine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cycle_test.go runs the full council cycle offline with a canned dispatcher,
// verifying the review v2 assembly, prepared package and publication.

type fakeDispatcher struct {
	chairFinalSpec string
}

func (f *fakeDispatcher) Dispatch(ctx context.Context, attempt *Attempt, prompt string, route *Route, timeoutSeconds int) (DispatchResult, error) {
	switch attempt.Role {
	case councilRoleBrainstorm:
		return DispatchResult{
			Status:        "completed",
			Payload:       orderedFrom([]string{"role", "alternatives"}, []any{"brainstorm", []any{"option A"}}),
			Observed:      Observed{Provider: strPtr("deepseek"), Model: strPtr("deepseek-flash")},
			ExecutionMode: "direct_api",
		}, nil
	case councilRoleChair:
		return DispatchResult{
			Status: "completed",
			Payload: orderedFrom([]string{"verdict", "decisions", "protected_decisions", "requirement_refs", "final_spec_text", "final_design_text"},
				[]any{"PASS", []any{}, []any{}, chairRefs(), f.chairFinalSpec, nil}),
			Observed:      Observed{Provider: strPtr("deepseek"), Model: strPtr("gpt-chair")},
			ExecutionMode: "direct_api",
		}, nil
	default:
		return DispatchResult{
			Status: "completed",
			Payload: orderedFrom([]string{"role", "verdict", "findings", "do_not_change"},
				[]any{attempt.Role, "PASS", []any{}, []any{}}),
			Observed:      Observed{Provider: strPtr("deepseek"), Model: strPtr("deepseek-flash")},
			ExecutionMode: "direct_api",
		}, nil
	}
}

func strPtr(value string) *string { return &value }

func chairRefs() []any {
	return []any{
		orderedFrom([]string{"id", "final_refs"}, []any{"REQ-001", []any{"Требуемое поведение / 1"}}),
		orderedFrom([]string{"id", "final_refs"}, []any{"REQ-002", []any{"Требуемое поведение / 2"}}),
	}
}

func cyclePolicy() *CouncilPolicy {
	return &CouncilPolicy{
		Enabled:               true,
		MaxParallel:           2,
		LegacyMode:            "block",
		RequestTimeoutSeconds: 300,
		Providers: map[string]ProviderConfig{
			"deepseek": {Name: "deepseek", Protocol: "openai_compatible", TokenEnv: "COUNCIL_TEST_TOKEN", TransportCapabilityVersion: 1, Endpoint: Endpoint{Scheme: "https", Host: "api.deepseek.com", Port: 443, BasePath: "/"}},
		},
		Models: map[string]ModelConfig{
			"reviewer": {Name: "reviewer", Provider: "deepseek", Model: "deepseek-flash", Effort: "medium"},
			"chair":    {Name: "chair", Provider: "deepseek", Model: "gpt-chair", Effort: "medium"},
		},
		Roles: map[string]RoleConfig{
			councilRoleBrainstorm:    {Name: councilRoleBrainstorm, Enabled: false, Required: false},
			councilRoleIntentCritic:  {Name: councilRoleIntentCritic, Enabled: true, Required: true, Model: "reviewer", Fallback: "block"},
			councilRoleArchitecture:  {Name: councilRoleArchitecture, Enabled: true, Required: true, Model: "reviewer", Fallback: "block"},
			councilRoleExecutability: {Name: councilRoleExecutability, Enabled: true, Required: true, Model: "reviewer", Fallback: "block"},
			councilRoleChair:         {Name: councilRoleChair, Enabled: true, Required: true, Model: "chair", Fallback: "block"},
		},
	}
}

func TestRunCycleOffline(t *testing.T) {
	t.Setenv("COUNCIL_TEST_TOKEN", "test-token")
	engine := &Engine{
		Now: func() time.Time { return time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC) },
		FinalValidation: func(changeDir string) (map[string]any, error) {
			data, err := convertToJSON(orderedFrom([]string{"passed", "schema_version"}, []any{true, 2}), 20)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(changeDir, "final-validation.json"), append(data, '\r', '\n'), 0o644); err != nil {
				return nil, err
			}
			return map[string]any{"passed": true}, nil
		},
	}
	projectRoot := t.TempDir()
	changeDir := filepath.Join(projectRoot, "openspec", "changes", "fixture")
	writeChangeFixture(t, changeDir)
	if err := os.WriteFile(filepath.Join(projectRoot, "bsl-flow.yaml"), []byte("review:\n  enabled: true\n"), 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	policyHash, err := PolicyHash(filepath.Join(projectRoot, "bsl-flow.yaml"))
	if err != nil {
		t.Fatalf("policy hash: %v", err)
	}
	snapshot, err := NewSnapshot(changeDir, 262144, "", policyHash)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	runRoot := RunRoot(projectRoot, "fixture")
	opts := &CycleOptions{
		ProjectRoot: projectRoot, ChangeName: "fixture", Policy: cyclePolicy(), PolicyHash: policyHash,
		Snapshot: snapshot, RunRoot: runRoot, AllowLiveDispatch: true, Dispatcher: &fakeDispatcher{chairFinalSpec: engineSpec},
	}
	result, err := engine.RunCycle(context.Background(), opts)
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if asStringOr(result.Review.get("verdict")) != "PASS" {
		t.Fatalf("verdict: %s", asStringOr(result.Review.get("verdict")))
	}
	if asStringOr(result.Review.get("diversity")) != "multi_model" {
		t.Fatalf("diversity: %s", asStringOr(result.Review.get("diversity")))
	}
	members, _ := asArray(result.Review.get("members"))
	if len(members) != 4 {
		t.Fatalf("members: %d (3 critics + chair)", len(members))
	}
	// review.json must be written to the change dir with the digest.
	reviewPath := filepath.Join(changeDir, "review.json")
	if !isRegularFile(reviewPath) {
		t.Fatal("review.json must be published")
	}
	reviewBytes, _ := os.ReadFile(reviewPath)
	reviewOnDisk, err := readOrderedObject(reviewPath)
	if err != nil {
		t.Fatalf("read review.json: %v", err)
	}
	digest, _ := CouncilReviewDigest(reviewOnDisk)
	reconciliation, _ := asOrdered(reviewOnDisk.get("reconciliation"))
	if asStringOr(reconciliation.get("review_sha256")) != digest {
		t.Fatal("review.json must carry its digest")
	}
	_ = reviewBytes
	// completed.event.json must exist.
	if !isRegularFile(filepath.Join(runRoot, "publication", "completed.event.json")) {
		t.Fatal("completed event must exist")
	}
}

func TestRunCycleDryRunBlocked(t *testing.T) {
	engine := &Engine{Now: func() time.Time { return time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC) }}
	projectRoot := t.TempDir()
	changeDir := filepath.Join(projectRoot, "openspec", "changes", "fixture")
	writeChangeFixture(t, changeDir)
	snapshot, _ := NewSnapshot(changeDir, 262144, "", strings.Repeat("9", 64))
	opts := &CycleOptions{
		ProjectRoot: projectRoot, ChangeName: "fixture", Policy: cyclePolicy(), PolicyHash: strings.Repeat("9", 64),
		Snapshot: snapshot, RunRoot: RunRoot(projectRoot, "fixture"), AllowLiveDispatch: false,
		Dispatcher: &fakeDispatcher{chairFinalSpec: engineSpec},
	}
	if _, err := engine.RunCycle(context.Background(), opts); err == nil {
		t.Fatal("dry-run must block live dispatch")
	}
}
