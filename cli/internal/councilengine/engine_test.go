package councilengine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// engine_test.go ports the scenarios of Test-CouncilEngine.ps1 without
// network: snapshot, attempt binding/reuse, member result registration,
// readiness and the prepared publication/resume cycle.

const engineSpec = "# Test\n\n## Классификация\n- Сложность: S\n- Риск: low\n\n## Требуемое поведение\n1. Первое требование.\n2. Второе требование.\n"

const engineOriginal = "Original task text for the fixture.\n"

func writeChangeFixture(t *testing.T, changeDir string) {
	t.Helper()
	if err := os.MkdirAll(changeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(changeDir, "spec.md"), []byte(engineSpec), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	if err := os.WriteFile(filepath.Join(changeDir, "original-task.md"), []byte(engineOriginal), 0o644); err != nil {
		t.Fatalf("write original: %v", err)
	}
}

func testEngine() *Engine {
	return &Engine{Now: func() time.Time { return time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC) }}
}

func testBinding(policyHash string) *Binding {
	return &Binding{
		Provider:                   "deepseek",
		Model:                      "deepseek-flash",
		Effort:                     "medium",
		Protocol:                   "openai_compatible",
		Endpoint:                   Endpoint{Scheme: "https", Host: "api.deepseek.com", Port: 443, BasePath: "/"},
		TransportCapabilityVersion: 1,
		PromptVersion:              "council-prompt-v2",
		MemberSchemaVersion:        1,
		InputHashes: orderedFrom(
			[]string{"original_task_sha256", "spec_sha256", "design_sha256", "evidence_sha256", "policy_hash", "rubric_sha256"},
			[]any{strings.Repeat("a", 64), strings.Repeat("b", 64), nil, nil, policyHash, strings.Repeat("c", 64)},
		),
	}
}

func TestSnapshot(t *testing.T) {
	changeDir := filepath.Join(t.TempDir(), "openspec", "changes", "fixture")
	writeChangeFixture(t, changeDir)
	snapshot, err := NewSnapshot(changeDir, 262144, "evidence bundle", strings.Repeat("9", 64))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.Complexity != "S" || snapshot.Risk != "low" {
		t.Fatalf("classification: %s/%s", snapshot.Complexity, snapshot.Risk)
	}
	requirements, _ := asArray(snapshot.Manifest.get("requirements"))
	if len(requirements) != 2 {
		t.Fatalf("manifest count: %d", len(requirements))
	}
	if snapshot.EvidenceSHA256 == nil || !sha256Pattern.MatchString(*snapshot.EvidenceSHA256) {
		t.Fatal("evidence hash must be present")
	}
	view, err := snapshot.RoleView("brainstorm", "")
	if err != nil {
		t.Fatalf("brainstorm view: %v", err)
	}
	if view.has("spec") {
		t.Fatal("brainstorm view must not carry the draft spec")
	}
	critic, _ := snapshot.RoleView("intent_critic", "rubric")
	if asStringOr(critic.get("spec")) != snapshot.SpecText {
		t.Fatal("critic view must carry the draft spec")
	}
}

func TestAttemptBindingReuseAndDrift(t *testing.T) {
	engine := testEngine()
	runRoot := filepath.Join(t.TempDir(), ".bsl-flow", "reports", "spec-review", "fixture.council")
	policyHash := strings.Repeat("9", 64)
	binding := testBinding(policyHash)

	a1, err := engine.NewAttempt(runRoot, "intent_critic", binding)
	if err != nil {
		t.Fatalf("attempt 1: %v", err)
	}
	a2, err := engine.NewAttempt(runRoot, "intent_critic", binding)
	if err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	if a1.AttemptID != a2.AttemptID {
		t.Fatal("identical binding must reuse the attempt")
	}
	drifted := *binding
	drifted.InputHashes = orderedFrom(
		[]string{"original_task_sha256", "spec_sha256", "design_sha256", "evidence_sha256", "policy_hash", "rubric_sha256"},
		[]any{strings.Repeat("a", 64), strings.Repeat("b", 64), nil, nil, policyHash, strings.Repeat("d", 64)},
	)
	a3, err := engine.NewAttempt(runRoot, "intent_critic", &drifted)
	if err != nil {
		t.Fatalf("drifted attempt: %v", err)
	}
	if a3.AttemptID == a1.AttemptID || a3.Sequence != 2 {
		t.Fatalf("drift must create a new sequenced attempt: %d", a3.Sequence)
	}
	// Token in the serialized binding is rejected.
	evil := *binding
	evil.FallbackCapability = orderedFrom(
		[]string{"sha256", "identity"},
		[]any{strings.Repeat("a", 64), orderedFrom([]string{"capability_version", "provider", "model", "effort", "fresh_context", "sealed", "terminal", "token"}, []any{"v1", "current_agent", "m", "e", true, true, true, "secret"})},
	)
	if err := AssertBinding(&evil); err == nil {
		t.Fatal("token binding must be rejected")
	}
	latest, err := LatestAttempt(runRoot, "intent_critic")
	if err != nil || latest.AttemptID != a3.AttemptID {
		t.Fatalf("latest attempt: %v", err)
	}
}

func evilTokenBinding(binding *Binding) *Binding {
	clone := *binding
	clone.Model = "evil"
	clone.toOrdered().set("token", "secret")
	return &clone
}

func TestMemberResultEnvelope(t *testing.T) {
	engine := testEngine()
	runRoot := filepath.Join(t.TempDir(), "run")
	binding := testBinding(strings.Repeat("9", 64))
	attempt, err := engine.NewAttempt(runRoot, "intent_critic", binding)
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	payload := orderedFrom(
		[]string{"role", "verdict", "findings", "do_not_change"},
		[]any{"intent_critic", "REVISE",
			[]any{orderedFrom([]string{"id", "severity", "category", "spec_ref", "issue", "evidence", "suggested_direction"},
				[]any{"F-001", "medium", "clarity", "Требуемое поведение / 1", "i", "e", "d"})},
			[]any{}},
	)
	model := "deepseek-flash"
	provider := "deepseek"
	dispatched := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	completed := dispatched.Add(time.Minute)
	envelope, err := engine.RegisterMemberResult(runRoot, MemberResult{
		Attempt:       attempt,
		Payload:       payload,
		Status:        "completed",
		Observed:      Observed{Provider: &provider, Model: &model, Effort: nil},
		ExecutionMode: "direct_api",
		Summary:       "fixture member result",
		DispatchedAt:  &dispatched,
		CompletedAt:   &completed,
	})
	if err != nil {
		t.Fatalf("register result: %v", err)
	}
	if asStringOr(envelope.get("role")) != "intent_critic" {
		t.Fatalf("role: %s", asStringOr(envelope.get("role")))
	}
	if !sha256Pattern.MatchString(asStringOr(envelope.get("payload_sha256"))) {
		t.Fatal("payload_sha256 must be a sha256")
	}
	if asStringOr(envelope.get("cost_state")) != "no_usage_reported" {
		t.Fatalf("cost_state: %s", asStringOr(envelope.get("cost_state")))
	}
	// Re-registering with the same attempt returns the same terminal result.
	stored, err := RoleResult(runRoot, "intent_critic", attempt)
	if err != nil || stored == nil {
		t.Fatalf("role result: %v", err)
	}
}

func TestBudgetLedger(t *testing.T) {
	engine := testEngine()
	runRoot := filepath.Join(t.TempDir(), "run")
	binding := testBinding(strings.Repeat("9", 64))
	attempt, err := engine.NewAttempt(runRoot, "intent_critic", binding)
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	estimate := 0.5
	admitted, err := engine.ApproveBudgetDispatch(runRoot, "intent_critic", attempt, &estimate, &BudgetConfig{Currency: "USD", Limit: ptrFloat(10.0)})
	if err != nil {
		t.Fatalf("admission: %v", err)
	}
	if asBoolJSONValue(admitted.get("admitted_total_usd")) || floatValue(admitted.get("admitted_total_usd")) != 0.5 {
		t.Fatalf("admitted total: %v", floatValue(admitted.get("admitted_total_usd")))
	}
	// Outcome reconciliation preserves unknown cost as unknown.
	_, err = engine.CompleteBudgetOutcome(runRoot, "intent_critic", attempt, "completed", nil, "no_usage_reported")
	if err != nil {
		t.Fatalf("outcome: %v", err)
	}
	// A dispatch over the limit is refused.
	estimate2 := 20.0
	attempt2, _ := engine.NewAttempt(runRoot, "architecture_critic", binding)
	if _, err := engine.ApproveBudgetDispatch(runRoot, "architecture_critic", attempt2, &estimate2, &BudgetConfig{Currency: "USD", Limit: ptrFloat(10.0)}); err == nil {
		t.Fatal("over-limit admission must be refused")
	}
}

func ptrFloat(value float64) *float64 { return &value }

func TestPreparedPackageAndResume(t *testing.T) {
	engine := testEngine()
	engine.FinalValidation = func(changeDir string) (map[string]any, error) {
		receipt := map[string]any{"passed": true, "schema_version": 2}
		data, err := convertToJSON(mapOrdered(map[string]any{"passed": true, "schema_version": 2}), 20)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(changeDir, "final-validation.json"), append(data, '\r', '\n'), 0o644); err != nil {
			return nil, err
		}
		return receipt, nil
	}
	projectRoot := t.TempDir()
	changeDir := filepath.Join(projectRoot, "openspec", "changes", "fixture")
	writeChangeFixture(t, changeDir)
	runRoot := filepath.Join(projectRoot, ".bsl-flow", "reports", "spec-review", "fixture.council")
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
	finalSpecBytes := []byte(engineSpec + "\n")
	// Build a minimal passing review.
	member := func(role string, model string) any {
		return orderedFrom(
			[]string{"schema_version", "role", "attempt_id", "status", "summary", "requested", "observed", "execution_mode", "fallback_reason", "input_hashes", "payload_sha256", "usage", "cost_state", "dispatched_at_utc", "completed_at_utc"},
			[]any{
				1, role, strings.Repeat("1", 32), "completed", "fixture " + role,
				orderedFrom([]string{"provider", "model", "effort"}, []any{"deepseek", model, "medium"}),
				orderedFrom([]string{"provider", "model", "effort"}, []any{"deepseek", model, "medium"}),
				"direct_api", nil,
				orderedFrom([]string{"original_task_sha256", "spec_sha256", "design_sha256", "evidence_sha256", "policy_hash", "rubric_sha256"},
					[]any{snapshot.OriginalTaskSHA256, snapshot.SpecSHA256, nil, nil, policyHash, strings.Repeat("c", 64)}),
				strings.Repeat("a", 64), nil, "no_usage_reported", "2026-09-14T10:00:00Z", "2026-09-14T10:01:00Z",
			},
		)
	}
	chairMember := func() any {
		return orderedFrom(
			[]string{"schema_version", "role", "attempt_id", "status", "summary", "requested", "observed", "execution_mode", "fallback_reason", "input_hashes", "payload_sha256", "usage", "cost_state", "dispatched_at_utc", "completed_at_utc"},
			[]any{
				1, "chair", strings.Repeat("2", 32), "completed", "fixture chair",
				orderedFrom([]string{"provider", "model", "effort"}, []any{"deepseek", "gpt-chair", "medium"}),
				orderedFrom([]string{"provider", "model", "effort"}, []any{"deepseek", "gpt-chair", "medium"}),
				"direct_api", nil,
				orderedFrom([]string{"original_task_sha256", "spec_sha256", "design_sha256", "evidence_sha256", "policy_hash", "rubric_sha256", "member_aggregate_sha256"},
					[]any{snapshot.OriginalTaskSHA256, snapshot.SpecSHA256, nil, nil, policyHash, strings.Repeat("c", 64), strings.Repeat("b", 64)}),
				strings.Repeat("d", 64), nil, "no_usage_reported", "2026-09-14T10:00:00Z", "2026-09-14T10:01:00Z",
			},
		)
	}
	reqRefs := []any{}
	requirements, _ := asArray(snapshot.Manifest.get("requirements"))
	for _, raw := range requirements {
		requirement, _ := asOrdered(raw)
		reqRefs = append(reqRefs, orderedFrom([]string{"id", "final_refs"}, []any{asStringOr(requirement.get("id")), []any{"Требуемое поведение / 1"}}))
	}
	review := orderedFrom(
		[]string{"schema_version", "reviewed_at_utc", "council_schema_version", "verdict", "diversity", "fallback_visible", "inputs", "manifest", "members", "findings", "protected", "questions", "chair", "reconciliation", "gate"},
		[]any{
			2, "2026-09-14T10:00:00Z", 1, "PASS", "multi_role_single_model", false,
			orderedFrom([]string{"original_task_sha256", "spec_sha256", "design_sha256", "policy_hash"},
				[]any{snapshot.OriginalTaskSHA256, snapshot.SpecSHA256, nil, policyHash}),
			snapshot.Manifest,
			[]any{member("intent_critic", "deepseek-flash"), chairMember()},
			[]any{}, []any{}, []any{},
			orderedFrom([]string{"verdict", "decisions", "protected_decisions", "requirement_refs", "final_spec_text", "final_design_text"},
				[]any{"PASS", []any{}, []any{}, reqRefs, engineSpec, nil}),
			orderedFrom([]string{"review_sha256", "draft_spec_sha256", "final_spec_sha256", "draft_design_sha256", "final_design_sha256"},
				[]any{strings.Repeat("0", 64), snapshot.SpecSHA256, sha256Hex(finalSpecBytes), nil, nil}),
			orderedFrom([]string{"structural_only", "passed"}, []any{true, true}),
		},
	)
	digest, err := CouncilReviewDigest(review)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	reconciliation, _ := asOrdered(review.get("reconciliation"))
	reconciliation.set("review_sha256", digest)
	if err := AssertCouncilReview(review); err != nil {
		t.Fatalf("assert review: %v", err)
	}
	if _, err := engine.NewPreparedPackage(runRoot, review, finalSpecBytes, nil, nil); err != nil {
		t.Fatalf("prepared package: %v", err)
	}
	if !isRegularFile(filepath.Join(runRoot, "publication", "prepared.event.json")) {
		t.Fatal("prepared event must exist")
	}
	// Live spec currently equals the draft; resume publishes the intended
	// final bytes.
	resumed, err := engine.ResumePreparedPublicationIfPresent(projectRoot, "fixture")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !asBoolJSONValue(resumed.get("resumed")) {
		t.Fatal("resume must complete")
	}
	liveSpec, _ := os.ReadFile(filepath.Join(changeDir, "spec.md"))
	if sha256Hex(liveSpec) != sha256Hex(finalSpecBytes) {
		t.Fatal("live spec must converge on the intended bytes")
	}
	// Third content blocks.
	if err := os.WriteFile(filepath.Join(changeDir, "spec.md"), []byte("third content"), 0o644); err != nil {
		t.Fatalf("write third content: %v", err)
	}
	if _, err := engine.ResumePreparedPublicationIfPresent(projectRoot, "fixture"); err == nil {
		t.Fatal("third content must be blocked")
	}
}

func mapOrdered(value map[string]any) *ordered {
	object := newOrdered()
	for _, key := range []string{"passed", "schema_version"} {
		object.set(key, value[key])
	}
	return object
}
