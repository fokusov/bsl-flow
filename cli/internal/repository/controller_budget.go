package repository

import (
	"os"
	"path/filepath"
	"strings"
)

// Each provider attempt receives a verified cumulative ledger. A later
// snapshot may append entries, but cannot erase spent or unresolved dispatches.
func nativeBudgetLedger(repository *Repository, taskID string, payload map[string]any) (map[string]any, error) {
	ledger := map[string]any{"schema_version": int64(1), "task_id": taskID, "entries": []any{}}
	if historical, found, err := resolveHistoricalTaskFile(repository, taskID, "budget/ledger.json"); err != nil {
		return nil, err
	} else if found {
		data, err := ReadFileBytes(historical)
		if err != nil {
			return nil, err
		}
		retained, err := DecodeObject(data)
		if err != nil {
			return nil, err
		}
		if retained["task_id"] != taskID || asIntOr(retained["schema_version"]) != 1 {
			return nil, blocked("historical budget identity differs")
		}
		if _, _, err := nativeBudgetTotals(retained); err != nil {
			return nil, err
		}
		ledger = retained
	}
	for _, raw := range anyItems(payload["attempts"]) {
		id := asStringOr(raw)
		if !isUUID(id) {
			return nil, blocked("budget attempt identity is invalid")
		}
		path, err := attemptDirectory(repository, taskID, id)
		if err != nil {
			return nil, err
		}
		data, err := ReadFileBytes(filepath.Join(path, "artifacts", "budget", "ledger.json"))
		if os.IsNotExist(err) {
			for _, rawEvidence := range anyItems(payload["evidence"]) {
				evidence := asMap(rawEvidence)
				if evidence["attempt_id"] != id {
					continue
				}
				for _, rawArtifact := range anyItems(evidence["raw_hashes"]) {
					if asMap(rawArtifact)["path"] == "attempts/"+id+"/budget/ledger.json" {
						return nil, blocked("registered budget snapshot is missing")
					}
				}
			}
			observation, observationErr := readStoredObservation(filepath.Join(path, "result.json"))
			if observationErr != nil && !os.IsNotExist(observationErr) {
				return nil, blocked("cannot inspect retained budget observation: %v", observationErr)
			}
			if observationErr == nil {
				for _, artifact := range observation.Artifacts {
					if artifact.Path == "budget/ledger.json" {
						return nil, blocked("declared budget snapshot is missing")
					}
				}
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		observation, err := readStoredObservation(filepath.Join(path, "result.json"))
		if err != nil {
			return nil, blocked("budget snapshot has no confirmed provider observation")
		}
		bound := false
		for _, artifact := range observation.Artifacts {
			if artifact.Path == "budget/ledger.json" && artifact.SHA256 == fileSHA256(data) && artifact.SizeBytes == int64(len(data)) {
				bound = true
			}
		}
		if !bound {
			return nil, blocked("budget snapshot differs from its retained observation")
		}
		next, err := DecodeObject(data)
		if err != nil {
			return nil, blocked("retained budget ledger is corrupt")
		}
		if err := validateNativeBudgetExtension(ledger, next, id); err != nil {
			return nil, err
		}
		ledger = next
	}
	return ledger, nil
}

func validateNativeBudgetExtension(prior, next map[string]any, attemptID string) error {
	if _, err := nativeObject(next, []string{"schema_version", "task_id", "entries"}, nil, "provider budget ledger"); err != nil {
		return err
	}
	if asIntOr(next["schema_version"]) != 1 || next["task_id"] != prior["task_id"] {
		return blocked("provider budget ledger identity differs")
	}
	old := anyItems(prior["entries"])
	entries, err := nativeArray(next["entries"], "budget entries", false)
	if err != nil {
		return err
	}
	if len(entries) < len(old) {
		return blocked("provider budget ledger erased prior dispatches")
	}
	for i := range old {
		if !equalJSON(old[i], entries[i]) {
			return blocked("provider budget ledger changed prior dispatches")
		}
	}
	for _, raw := range entries[len(old):] {
		if !strings.HasPrefix(asStringOr(asMap(raw)["dispatch"]), "attempts/"+attemptID+"/provider/") {
			return blocked("budget entry belongs to another attempt")
		}
	}
	_, _, err = nativeBudgetTotals(next)
	return err
}

func nativeBudgetTotals(ledger map[string]any) (float64, int, error) {
	reservations := map[string]bool{}
	outcomes := map[string]map[string]any{}
	spent := float64(0)
	for _, raw := range anyItems(ledger["entries"]) {
		item, err := nativeObject(raw, []string{"kind", "dispatch"}, []string{"provider", "stage", "requested_model", "observed_model", "reservation_usd", "reported_cost_usd", "billed_cost_usd", "cost_state", "usage", "terminal_status", "currency", "at"}, "budget entry")
		if err != nil {
			return 0, 0, err
		}
		key := asStringOr(item["dispatch"])
		if validateRelativeNativePath(key, false) != nil {
			return 0, 0, blocked("invalid budget entry identity/currency")
		}
		switch item["kind"] {
		case "reservation":
			if item["currency"] != "USD" {
				return 0, 0, blocked("invalid budget reservation currency")
			}
			amount, ok := finiteNativeNumber(item["reservation_usd"])
			if !ok || amount < 0 || reservations[key] || outcomes[key] != nil {
				return 0, 0, blocked("invalid or duplicate budget reservation")
			}
			reservations[key] = true
		case "outcome":
			if !reservations[key] || outcomes[key] != nil {
				return 0, 0, blocked("budget outcome has no unique prior reservation")
			}
			// Legacy outcomes omit currency; their unique verified reservation
			// supplies USD without rewriting any retained journal bytes.
			if currency, present := item["currency"]; present && currency != "USD" {
				return 0, 0, blocked("invalid budget outcome currency")
			}
			outcomes[key] = item
			if item["cost_state"] == "known" {
				amount, ok := finiteNativeNumber(item["reported_cost_usd"])
				if !ok || amount < 0 {
					return 0, 0, blocked("invalid known budget cost")
				}
				spent += amount
			} else if item["cost_state"] != "unknown" {
				return 0, 0, blocked("invalid budget cost state")
			}
		default:
			return 0, 0, blocked("invalid budget entry kind")
		}
	}
	unknown := 0
	for key := range reservations {
		if outcomes[key] == nil || outcomes[key]["cost_state"] != "known" {
			unknown++
		}
	}
	return spent, unknown, nil
}

func nativeBudgetAdmission(repository *Repository, taskID string, payload map[string]any) (map[string]any, error) {
	ledger, err := nativeBudgetLedger(repository, taskID, payload)
	if err != nil {
		return nil, err
	}
	budget := asMap(asMap(payload["request"])["budget"])
	spent, unknown, err := nativeBudgetTotals(ledger)
	if err != nil {
		return nil, err
	}
	open := 0
	for _, raw := range anyItems(ledger["entries"]) {
		switch asMap(raw)["kind"] {
		case "reservation":
			open++
		case "outcome":
			open--
		}
	}
	if open > 0 {
		return nil, blocked("prior provider dispatch has no terminal budget outcome")
	}
	if _, limited := finiteNativeNumber(budget["limit"]); limited && unknown > 0 {
		return nil, blocked("prior provider dispatch has unresolved or unknown cost")
	}
	reservation, _ := finiteNativeNumber(budget["reservation"])
	if limit, ok := finiteNativeNumber(budget["limit"]); ok && spent+reservation > limit+1e-9 {
		return nil, blocked("provider budget limit would be exceeded")
	}
	return ledger, nil
}

func stageNativeBudget(repository *Repository, taskID string, payload map[string]any, contextRoot string) (ArtifactRef, error) {
	ledger, err := nativeBudgetLedger(repository, taskID, payload)
	if err != nil {
		return ArtifactRef{}, err
	}
	path := filepath.Join(contextRoot, "budget", "ledger.json")
	if _, err := writeImmutableJSON(path, ledger); err != nil {
		return ArtifactRef{}, err
	}
	data, err := ReadFileBytes(path)
	if err != nil {
		return ArtifactRef{}, err
	}
	return ArtifactRef{Path: "budget/ledger.json", SHA256: fileSHA256(data), SizeBytes: int64(len(data)), Kind: "budget"}, nil
}

func validateNativeBudgetObservation(payload map[string]any, observation ExecuteObservation, attemptPath, artifactRoot string) error {
	budget := asMap(asMap(payload["request"])["budget"])
	if len(budget) == 0 {
		return nil
	}
	data, err := ReadFileBytes(filepath.Join(attemptPath, "budget-admission.json"))
	if err != nil {
		return blocked("immutable budget admission is missing")
	}
	prior, err := DecodeObject(data)
	if err != nil {
		return err
	}
	declared := false
	for _, ref := range observation.Artifacts {
		if ref.Path == "budget/ledger.json" {
			declared = true
		}
	}
	if !declared {
		if observation.Status == "completed" && observation.Stage != "verify" {
			return blocked("completed model stage omitted its budget ledger")
		}
		return nil
	}
	ledger, err := declaredStageJSON(observation, artifactRoot, "budget/ledger.json")
	if err != nil {
		return err
	}
	if err := validateNativeBudgetExtension(prior, ledger, observation.AttemptID); err != nil {
		return err
	}
	oldCount := len(anyItems(prior["entries"]))
	for _, raw := range anyItems(ledger["entries"])[oldCount:] {
		entry := asMap(raw)
		if entry["kind"] == "reservation" {
			if !equalJSON(entry["reservation_usd"], budget["reservation"]) {
				return blocked("provider changed the authorized budget reservation")
			}
			continue
		}
		if entry["cost_state"] != "known" {
			continue
		}
		directory := strings.TrimPrefix(asStringOr(entry["dispatch"]), "attempts/"+observation.AttemptID+"/provider/")
		host, err := declaredStageJSON(observation, artifactRoot, directory+"/host-result.json")
		if err != nil {
			return err
		}
		if !equalJSON(entry["reported_cost_usd"], host["reported_cost_usd"]) || !equalJSON(entry["usage"], host["usage"]) || !equalJSON(entry["observed_model"], host["observed_model"]) {
			return blocked("provider budget cost differs from retained host evidence")
		}
	}
	return nil
}
