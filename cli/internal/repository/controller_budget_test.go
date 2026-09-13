package repository

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeBudgetRetainsUnknownAndRejectsHistoryReplacement(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	dispatch := "attempts/" + id + "/provider/raw/worker"
	prior := map[string]any{"schema_version": int64(1), "task_id": id, "entries": []any{}}
	reservation := map[string]any{"kind": "reservation", "dispatch": dispatch, "reservation_usd": float64(1), "currency": "USD"}
	outcome := map[string]any{"kind": "outcome", "dispatch": dispatch, "reported_cost_usd": nil, "cost_state": "unknown", "currency": "USD"}
	next := map[string]any{"schema_version": int64(1), "task_id": id, "entries": []any{reservation, outcome}}
	if err := validateNativeBudgetExtension(prior, next, id); err != nil {
		t.Fatal(err)
	}
	spent, unknown, err := nativeBudgetTotals(next)
	if err != nil || spent != 0 || unknown != 1 {
		t.Fatalf("unknown dispatch lost: %v %d %v", spent, unknown, err)
	}
	if err := validateNativeBudgetExtension(next, prior, id); err == nil {
		t.Fatal("budget history erasure accepted")
	}
	changed, _ := cloneObject(next)
	asMap(anyItems(changed["entries"])[0])["reservation_usd"] = float64(0)
	if err := validateNativeBudgetExtension(next, changed, id); err == nil {
		t.Fatal("budget history mutation accepted")
	}
	duplicate, _ := cloneObject(next)
	duplicate["entries"] = append(anyItems(duplicate["entries"]), outcome)
	if _, _, err := nativeBudgetTotals(duplicate); err == nil {
		t.Fatal("duplicate outcome accepted")
	}
	foreign, _ := cloneObject(next)
	asMap(anyItems(foreign["entries"])[0])["dispatch"] = "attempts/22222222-2222-4222-8222-222222222222/provider/raw/worker"
	if err := validateNativeBudgetExtension(prior, foreign, id); err == nil {
		t.Fatal("foreign attempt budget accepted")
	}
}

func TestNativeBudgetLegacyOutcomeInheritsOnlyVerifiedReservationCurrency(t *testing.T) {
	reservation := map[string]any{"kind": "reservation", "dispatch": "attempts/legacy/raw/worker", "currency": "USD", "reservation_usd": float64(1)}
	outcome := map[string]any{"kind": "outcome", "dispatch": reservation["dispatch"], "cost_state": "known", "reported_cost_usd": 0.25}
	ledger := map[string]any{"entries": []any{reservation, outcome}}
	if spent, unknown, err := nativeBudgetTotals(ledger); err != nil || spent != 0.25 || unknown != 0 {
		t.Fatalf("legacy outcome rejected: %v %v %v", spent, unknown, err)
	}
	if _, present := outcome["currency"]; present {
		t.Fatal("legacy bytes mutated")
	}
	for _, currency := range []any{"EUR", nil} {
		outcome["currency"] = currency
		if _, _, err := nativeBudgetTotals(ledger); err == nil {
			t.Fatal("explicit invalid outcome currency accepted")
		}
	}
	delete(outcome, "currency")
	reservation["currency"] = "EUR"
	if _, _, err := nativeBudgetTotals(ledger); err == nil {
		t.Fatal("invalid reservation currency inherited")
	}
}

func TestNativeBudgetMissingSnapshotCannotEraseSpendAfterNewIntent(t *testing.T) {
	fixture := newNativeTestFixture(t, nil)
	if _, err := controllerRun(fixture.project, fixture.taskID, fixture.host); err != nil {
		t.Fatal(err)
	}
	payload, repository, _ := nativeTestTaskPayload(t, fixture)
	id := asStringOr(anyItems(payload["attempts"])[0])
	path, err := attemptDirectory(repository, fixture.taskID, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(path, "artifacts", "budget", "ledger.json")); err != nil {
		t.Fatal(err)
	}
	payload["intent_revision"] = int64(2)
	payload["authorization_revision"] = int64(2)
	if _, err := nativeBudgetAdmission(repository, fixture.taskID, payload); err == nil {
		t.Fatal("missing snapshot erased spend")
	}
	if err := os.Remove(filepath.Join(path, "result.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeBudgetAdmission(repository, fixture.taskID, payload); err == nil {
		t.Fatal("missing result and snapshot erased spend")
	}
}
