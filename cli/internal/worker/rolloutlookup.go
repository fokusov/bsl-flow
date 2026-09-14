package worker

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Port of Get-BFObservedModelEffort (Task.Storage.ps1:374-498) restricted to
// the surface the worker adapters consume: identity lookup by exact session
// id, optionally selecting an exact persisted turn for the strict receipt.

var observedSessionPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ObservedIdentity is the controller-observed identity of a Codex host run:
// resolved model/effort, the exact rollout session file and (when a turn was
// selected) the persisted turn id. Empty strings are the PowerShell nulls.
type ObservedIdentity struct {
	ObservedModel  string
	ObservedEffort string
	RolloutPath    string
	SessionID      string
	TurnID         string
}

// Map renders the PowerShell identity object shape (the worker's fallback
// branch keeps the missing marker out of persisted receipts).
func (o ObservedIdentity) Map() map[string]any {
	return map[string]any{
		"observed_model":  nilIfEmpty(o.ObservedModel),
		"observed_effort": nilIfEmpty(o.ObservedEffort),
		"rollout_path":    nilIfEmpty(o.RolloutPath),
		"session_id":      o.SessionID,
	}
}

func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// ObservedModelEffort mirrors Get-BFObservedModelEffort for the worker call
// sites (no -AllowMissing, no -SelectLatest): the rollout session file the
// host itself persisted for this exact session id proves the resolved
// identity. A non-empty turnID selects one exact persisted turn, mirroring
// the strict receipt re-read.
func ObservedModelEffort(sessionID, turnID, codexHome string) (ObservedIdentity, error) {
	if !observedSessionPattern.MatchString(sessionID) {
		return ObservedIdentity{}, invalid("Codex session id has an invalid format.")
	}
	if strings.TrimSpace(codexHome) == "" {
		codexHome = os.Getenv("CODEX_HOME")
	}
	if strings.TrimSpace(codexHome) == "" {
		home := os.Getenv("USERPROFILE")
		if strings.TrimSpace(home) == "" {
			userHome, err := os.UserHomeDir()
			if err != nil {
				return ObservedIdentity{}, blocked("Codex sessions path is not a trusted local directory.")
			}
			home = userHome
		}
		codexHome = filepath.Join(home, ".codex")
	}
	homeRoot, err := workerSafePath(codexHome)
	if err != nil {
		return ObservedIdentity{}, blocked("Codex sessions path is not a trusted local directory.")
	}
	sessionsRoot, err := workerSafePath(filepath.Join(homeRoot, "sessions"))
	if err != nil {
		return ObservedIdentity{}, blocked("Codex sessions path is not a trusted local directory.")
	}
	if !directoryExists(sessionsRoot) {
		return ObservedIdentity{}, blocked("Codex sessions directory is missing: %s", sessionsRoot)
	}
	// Locate the exact session suffix across the complete local session tree.
	suffix := "-" + sessionID + ".jsonl"
	candidates := make([]string, 0, 1)
	err = filepath.WalkDir(sessionsRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), suffix) {
			candidates = append(candidates, path)
		}
		return nil
	})
	if err != nil {
		return ObservedIdentity{}, blocked("Codex sessions path is not a trusted local directory.")
	}
	if len(candidates) == 0 {
		return ObservedIdentity{}, blocked("Codex rollout session file not found for %s.", sessionID)
	}
	if len(candidates) > 1 {
		return ObservedIdentity{}, blocked("Multiple Codex rollout files match session %s.", sessionID)
	}
	rolloutPath, err := workerSafePath(candidates[0])
	if err != nil {
		return ObservedIdentity{}, err
	}
	// A live writer may expose a partial final line; it cannot serve as
	// identity evidence, while complete earlier records remain inspectable.
	data, err := os.ReadFile(rolloutPath)
	if err != nil {
		return ObservedIdentity{}, blocked("Cannot read Codex rollout for %s: %v", sessionID, err)
	}
	model := ""
	effort := ""
	lastTurnID := ""
	selectedModel := ""
	selectedEffort := ""
	selectedTurnID := ""
	selectedTurnCount := 0
	sessionMetaSeen := false
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		record, parseErr := parseJSONObject([]byte(line))
		if parseErr != nil {
			continue
		}
		recordType, _ := asString(record["type"])
		switch recordType {
		case "session_meta":
			if sessionMetaSeen {
				return ObservedIdentity{}, blocked("Codex rollout contains duplicate session metadata for %s.", sessionID)
			}
			sessionMetaSeen = true
			payload, _ := asObject(record["payload"])
			metaID, _ := asString(getValue(payload, "session_id", nil))
			if strings.TrimSpace(metaID) == "" {
				metaID, _ = asString(getValue(payload, "id", nil))
			}
			if metaID != sessionID {
				return ObservedIdentity{}, blocked("Codex rollout session metadata does not match %s.", sessionID)
			}
		case "turn_context":
			// Keep the latest complete turn_context: old turns must not
			// become provenance after a model/effort change in the current
			// session.
			payload, _ := asObject(record["payload"])
			if payload == nil {
				model = ""
				effort = ""
				lastTurnID = ""
				continue
			}
			model = stringOfValue(getValue(payload, "model", nil))
			effort = stringOfValue(getValue(payload, "effort", nil))
			turnIDValue := stringOfValue(getValue(payload, "turn_id", nil))
			lastTurnID = turnIDValue
			if turnID != "" && turnIDValue == turnID {
				selectedTurnCount++
				if selectedTurnCount > 1 {
					return ObservedIdentity{}, blocked("Codex rollout contains duplicate turn identity %s.", turnID)
				}
				selectedModel = model
				selectedEffort = effort
				selectedTurnID = lastTurnID
			}
		}
	}
	if !sessionMetaSeen {
		return ObservedIdentity{}, blocked("Codex rollout session metadata is missing for %s.", sessionID)
	}
	if turnID != "" {
		if selectedTurnCount == 0 {
			return ObservedIdentity{}, blocked("Codex rollout turn identity %s was not found for %s.", turnID, sessionID)
		}
		model = selectedModel
		effort = selectedEffort
		lastTurnID = selectedTurnID
	}
	if strings.TrimSpace(model) == "" || strings.TrimSpace(effort) == "" {
		selection := "latest"
		if turnID != "" {
			selection = "selected"
		}
		return ObservedIdentity{}, blocked("Codex rollout %s turn_context is missing resolved model/effort for %s.", selection, sessionID)
	}
	return ObservedIdentity{
		ObservedModel:  model,
		ObservedEffort: effort,
		RolloutPath:    rolloutPath,
		SessionID:      sessionID,
		TurnID:         lastTurnID,
	}, nil
}
