package worker

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// Rollout parsing bounds. The PowerShell rollout reader had no explicit size
// bound; the native parser keeps memory bounded with a 64 MiB per-record byte
// bound (records are not retained, so the largest record bounds memory) and a
// record-count bound mirroring $script:BFJsonMaximumValues
// (Task.Storage.ps1:6).
const (
	MaxRolloutBytes = 64 << 20
	MaxRolloutLines = 1000000
)

// RolloutIdentity is the controller-observed identity of a Codex host run. It
// is derived only from the rollout session file the host itself persisted
// (CODEX_HOME/sessions/YYYY/MM/DD/rollout-<ts>-<session_id>.jsonl), never from
// request values — the identity rule of Invoke-BFCodexWorker (Codex.ps1:91-99)
// and Get-BFObservedModelEffort (Task.Storage.ps1:374-498).
type RolloutIdentity struct {
	Model     string
	Effort    string
	SessionID string
}

// ParseRolloutSession scans a rollout session JSONL stream and extracts the
// observed model/effort/session identity.
//
// Contract mirrored from Get-BFObservedModelEffort with two documented
// resolutions:
//
//   - turn_context: the PowerShell reader keeps the latest complete payload
//     because a live session may append new turns; this parser reads finished
//     rollout files, so it requires exactly one usable turn_context payload —
//     zero is ErrRolloutMissingTurn, more than one is ErrRolloutAmbiguousTurn,
//     and a payload without both model and effort is
//     ErrRolloutIncompleteIdentity (the PS "missing resolved model/effort"
//     refusal, Task.Storage.ps1:493-496).
//   - malformed lines: the PS reader skips a partial final line a live writer
//     may expose (Task.Storage.ps1:439-443); the native parser reads completed
//     files only and rejects invalid JSON records with ErrRolloutMalformed.
//
// Unknown record kinds (anything other than session_meta/turn_context) are
// tolerated for forward compatibility, matching the PS reader which only
// inspects the two known record types. A session_meta record is required
// exactly once and must carry the session id in payload.session_id with
// payload.id as fallback for records that expose only id
// (Task.Storage.ps1:445-452, Test-BSLFlowProfiledCodex.ps1:149-156). All
// provenance refusals are BF_BLOCKED-classed and wrap their sentinel error.
func ParseRolloutSession(r io.Reader) (RolloutIdentity, error) {
	reader := bufio.NewReaderSize(r, 64<<10)
	var identity RolloutIdentity
	var sessionMetaSeen bool
	var usableTurnContexts int
	var payloadModel, payloadEffort string
	lines := 0
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > MaxRolloutBytes {
			return RolloutIdentity{}, blockedCause(ErrRolloutTooLarge, "rollout record exceeds the %d byte parsing bound", MaxRolloutBytes)
		}
		line = append(line, fragment...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if len(bytes.TrimSpace(line)) != 0 {
			lines++
			if lines > MaxRolloutLines {
				return RolloutIdentity{}, blockedCause(ErrRolloutTooLarge, "rollout exceeds the %d record parsing bound", MaxRolloutLines)
			}
			if err := parseRolloutLine(line, &identity, &sessionMetaSeen, &usableTurnContexts, &payloadModel, &payloadEffort); err != nil {
				return RolloutIdentity{}, err
			}
		}
		if err == io.EOF {
			break
		}
		line = line[:0]
	}
	if !sessionMetaSeen {
		return RolloutIdentity{}, blockedCause(ErrRolloutSessionMeta, "rollout session metadata is missing")
	}
	switch {
	case usableTurnContexts == 0:
		return RolloutIdentity{}, blockedCause(ErrRolloutMissingTurn, "rollout for %s has no usable turn_context payload", identity.SessionID)
	case usableTurnContexts > 1:
		return RolloutIdentity{}, blockedCause(ErrRolloutAmbiguousTurn, "rollout for %s has ambiguous turn_context payloads", identity.SessionID)
	}
	if payloadModel == "" || payloadEffort == "" {
		return RolloutIdentity{}, blockedCause(ErrRolloutIncompleteIdentity, "rollout turn_context for %s is missing resolved model/effort", identity.SessionID)
	}
	identity.Model = payloadModel
	identity.Effort = payloadEffort
	return identity, nil
}

func parseRolloutLine(line []byte, identity *RolloutIdentity, sessionMetaSeen *bool, usableTurnContexts *int, payloadModel, payloadEffort *string) error {
	record, err := parseJSONObject(line)
	if err != nil {
		return blockedCause(ErrRolloutMalformed, "malformed rollout JSONL record: %v", err)
	}
	switch record["type"] {
	case "session_meta":
		if *sessionMetaSeen {
			return blockedCause(ErrRolloutSessionMeta, "rollout contains duplicate session metadata for %s", identity.SessionID)
		}
		*sessionMetaSeen = true
		payload, _ := asObject(record["payload"])
		if payload == nil {
			return blockedCause(ErrRolloutSessionMeta, "rollout session_meta payload is missing")
		}
		sessionID, _ := asString(payload["session_id"])
		if sessionID == "" {
			sessionID, _ = asString(payload["id"])
		}
		if strings.TrimSpace(sessionID) == "" {
			return blockedCause(ErrRolloutSessionMeta, "rollout session metadata carries no session id")
		}
		identity.SessionID = sessionID
	case "turn_context":
		payload, _ := asObject(record["payload"])
		if payload == nil {
			// A null payload is not usable identity evidence; the PS reader
			// resets its accumulated identity on it (Task.Storage.ps1:458).
			return nil
		}
		*usableTurnContexts++
		if model, ok := asString(payload["model"]); ok && strings.TrimSpace(model) != "" {
			*payloadModel = model
		}
		if effort, ok := asString(payload["effort"]); ok && strings.TrimSpace(effort) != "" {
			*payloadEffort = effort
		}
	}
	return nil
}
