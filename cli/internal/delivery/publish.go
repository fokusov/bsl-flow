package delivery

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	publicationSchema = int64(1)
	maxStateBytes     = 1 << 20
)

// Stage names the position of a publication in its state machine.
type Stage string

// The publication advances Idle→Prepared→Committed→Pushed→Verified and never
// moves backwards; every persisted state is sealed with an integrity hash.
const (
	StageIdle      Stage = "idle"
	StagePrepared  Stage = "prepared"
	StageCommitted Stage = "committed"
	StagePushed    Stage = "pushed"
	StageVerified  Stage = "verified"
)

// Outcome reports the safety classification of a Step or Resume result.
type Outcome string

const (
	// OutcomeInProgress means the machine advanced and further steps remain.
	OutcomeInProgress Outcome = "in_progress"
	// OutcomeVerified means the remote provably holds the planned commit.
	OutcomeVerified Outcome = "verified"
	// OutcomeBlockedNeedsReconciliation means the publication effect is
	// unknown or unsettled and automatic replay is forbidden.
	OutcomeBlockedNeedsReconciliation Outcome = "blocked_needs_reconciliation"
)

// PushOutcome records what is known about the single allowed dispatch.
type PushOutcome string

const (
	PushNotDispatched PushOutcome = ""
	PushUnknown       PushOutcome = "unknown"
	PushLanded        PushOutcome = "ok"
)

// State is the durable publication journal; Bytes persists it canonically and
// Resume continues from those bytes after a crash.
type State struct {
	SchemaVersion  int64
	TaskID         string
	Remote         string
	Ref            string
	AuthorizedBy   string
	EvidenceHash   string
	PlanHash       string
	Baseline       CommitID
	Message        string
	Paths          []string
	Stage          Stage
	Commit         CommitID
	PushDispatched bool
	PushOutcome    PushOutcome
	StateHash      string
}

func (s State) body() map[string]any {
	return map[string]any{
		"schema_version":  s.SchemaVersion,
		"task_id":         s.TaskID,
		"remote":          s.Remote,
		"ref":             s.Ref,
		"authorized_by":   s.AuthorizedBy,
		"evidence_hash":   s.EvidenceHash,
		"plan_hash":       s.PlanHash,
		"baseline":        string(s.Baseline),
		"message":         s.Message,
		"paths":           s.Paths,
		"stage":           string(s.Stage),
		"commit_oid":      string(s.Commit),
		"push_dispatched": s.PushDispatched,
		"push_outcome":    string(s.PushOutcome),
	}
}

// seal recomputes and stores the integrity hash over the canonical body.
func seal(state State) (State, error) {
	digest, err := hashValue(state.body())
	if err != nil {
		return State{}, err
	}
	state.StateHash = digest
	return state, nil
}

func verifyStateHash(state State) error {
	digest, err := hashValue(state.body())
	if err != nil {
		return err
	}
	if digest != state.StateHash {
		return blocked("persisted publication state failed its integrity hash")
	}
	return nil
}

// Bytes persists a sealed state as canonical JSON; a mutated, unsealed state
// fails closed instead of publishing unverified bytes.
func (s State) Bytes() ([]byte, error) {
	if err := verifyStateHash(s); err != nil {
		return nil, err
	}
	body := s.body()
	body["state_sha256"] = s.StateHash
	return canonicalJSON(body)
}

// Begin returns the sealed Idle state bound to a verified plan.
func Begin(plan Plan) (State, error) {
	if err := plan.verify(); err != nil {
		return State{}, err
	}
	return seal(State{
		SchemaVersion: publicationSchema,
		TaskID:        plan.TaskID,
		Remote:        plan.Target.Remote,
		Ref:           plan.Target.Ref,
		AuthorizedBy:  plan.Target.AuthorizedBy,
		EvidenceHash:  plan.EvidenceHash,
		PlanHash:      plan.PlanHash,
		Baseline:      plan.Baseline,
		Message:       plan.Message,
		Paths:         plan.Paths,
		Stage:         StageIdle,
	})
}

func withStage(state State, stage Stage) State {
	state.Stage = stage
	return state
}

// Step performs exactly one publication transition through the injected Git
// port and returns the next sealed state. Completed side effects are never
// repeated: staging is idempotent, the commit is recorded before any push,
// and the push dispatch is attempted at most once.
func Step(plan Plan, state State, git GitPort) (State, Outcome, error) {
	if git == nil {
		return state, "", invalid("publication requires a Git port")
	}
	if err := plan.verify(); err != nil {
		return state, "", err
	}
	if err := verifyStateHash(state); err != nil {
		return state, "", err
	}
	if state.PlanHash != plan.PlanHash {
		return state, "", conflict("publication state belongs to a different plan")
	}
	switch state.Stage {
	case StageIdle:
		for _, path := range state.Paths {
			if err := git.Stage(path); err != nil {
				return state, "", blocked("staging %s failed: %v", path, err)
			}
		}
		next, err := seal(withStage(state, StagePrepared))
		if err != nil {
			return state, "", err
		}
		return next, OutcomeInProgress, nil
	case StagePrepared:
		commit, err := git.CommitAll(state.Message)
		if err != nil {
			return state, "", blocked("publication commit failed: %v", err)
		}
		if !validCommitID(commit) {
			return state, "", blocked("publication commit identity is malformed")
		}
		next := state
		next.Stage = StageCommitted
		next.Commit = commit
		if next, err = seal(next); err != nil {
			return state, "", err
		}
		return next, OutcomeInProgress, nil
	case StageCommitted:
		return dispatchTransition(state, git)
	case StagePushed:
		return verifyTransition(state, git)
	case StageVerified:
		return state, OutcomeVerified, nil
	}
	return state, "", invalid("unknown publication stage %q", string(state.Stage))
}

// dispatchTransition moves a committed publication toward its exact remote at
// most once. The remote head is read first: a foreign OID is a conflict that
// is never overwritten, a head equal to the planned commit completes
// idempotently, and only a definitively absent ref is pushed. Any push error
// leaves the outcome unknown and blocks automatic replay.
func dispatchTransition(state State, git GitPort) (State, Outcome, error) {
	head, known, err := git.ReadRemoteHead(state.Remote, state.Ref)
	if err != nil {
		return state, OutcomeBlockedNeedsReconciliation, &UnknownEffectError{Operation: "remote head read", Cause: err}
	}
	if !known {
		return state, OutcomeBlockedNeedsReconciliation, &UnknownEffectError{Operation: "remote head read"}
	}
	if head != "" {
		if head == state.Commit {
			next, sealErr := seal(withStage(state, StageVerified))
			if sealErr != nil {
				return state, "", sealErr
			}
			return next, OutcomeVerified, nil
		}
		return state, "", conflict("remote ref has another OID; publication will not overwrite it")
	}
	if state.PushDispatched {
		return state, OutcomeBlockedNeedsReconciliation, blocked("dispatched publication is absent or unsettled; automatic push replay is forbidden")
	}
	dispatched := state
	dispatched.PushDispatched = true
	dispatched.PushOutcome = PushUnknown
	if dispatched, err = seal(dispatched); err != nil {
		return state, "", err
	}
	if pushErr := git.Push(state.Remote, state.Ref); pushErr != nil {
		return dispatched, OutcomeBlockedNeedsReconciliation, &UnknownEffectError{Operation: "push", Cause: pushErr}
	}
	dispatched.Stage = StagePushed
	dispatched.PushOutcome = PushLanded
	if dispatched, err = seal(dispatched); err != nil {
		return state, "", err
	}
	return dispatched, OutcomeInProgress, nil
}

// verifyTransition confirms a pushed publication by reading the remote head;
// only the planned commit verifies, an absent or unknown head stays blocked,
// and a foreign OID is a conflict.
func verifyTransition(state State, git GitPort) (State, Outcome, error) {
	head, known, err := git.ReadRemoteHead(state.Remote, state.Ref)
	if err != nil {
		return state, OutcomeBlockedNeedsReconciliation, &UnknownEffectError{Operation: "remote head read", Cause: err}
	}
	if !known {
		return state, OutcomeBlockedNeedsReconciliation, &UnknownEffectError{Operation: "remote head read"}
	}
	switch {
	case head == state.Commit:
		next, sealErr := seal(withStage(state, StageVerified))
		if sealErr != nil {
			return state, "", sealErr
		}
		return next, OutcomeVerified, nil
	case head == "":
		return state, OutcomeBlockedNeedsReconciliation, blocked("dispatched publication is absent or unsettled; automatic push replay is forbidden")
	default:
		return state, "", conflict("remote ref has another OID; publication will not overwrite it")
	}
}

// Resume recovers a publication from its persisted canonical bytes. The state
// integrity hash is re-verified before anything runs, local preparation is
// completed without repeating finished side effects, and once a push may have
// been dispatched the remote is the only authority: an unknown or unsettled
// remote blocks with OutcomeBlockedNeedsReconciliation and never re-pushes,
// while a remote head equal to the planned commit completes idempotently.
func Resume(stateBytes []byte, git GitPort) (State, Outcome, error) {
	if git == nil {
		return State{}, "", invalid("publication resume requires a Git port")
	}
	state, err := decodeState(stateBytes)
	if err != nil {
		return State{}, "", err
	}
	for {
		switch state.Stage {
		case StageIdle:
			for _, path := range state.Paths {
				if err := git.Stage(path); err != nil {
					return state, "", blocked("staging %s failed: %v", path, err)
				}
			}
			if state, err = seal(withStage(state, StagePrepared)); err != nil {
				return state, "", err
			}
		case StagePrepared:
			commit, commitErr := git.CommitAll(state.Message)
			if commitErr != nil {
				return state, "", blocked("publication commit failed: %v", commitErr)
			}
			if !validCommitID(commit) {
				return state, "", blocked("publication commit identity is malformed")
			}
			next := state
			next.Stage = StageCommitted
			next.Commit = commit
			if state, err = seal(next); err != nil {
				return state, "", err
			}
		case StageCommitted:
			next, outcome, dispatchErr := dispatchTransition(state, git)
			if dispatchErr != nil {
				return next, outcome, dispatchErr
			}
			state = next
		case StagePushed:
			return verifyTransition(state, git)
		case StageVerified:
			return state, OutcomeVerified, nil
		default:
			return state, "", invalid("unknown publication stage %q", string(state.Stage))
		}
	}
}

var stateFields = map[string]bool{
	"schema_version": true, "task_id": true, "remote": true, "ref": true, "authorized_by": true,
	"evidence_hash": true, "plan_hash": true, "baseline": true, "message": true, "paths": true,
	"stage": true, "commit_oid": true, "push_dispatched": true, "push_outcome": true, "state_sha256": true,
}

var stageValues = map[Stage]bool{
	StageIdle: true, StagePrepared: true, StageCommitted: true, StagePushed: true, StageVerified: true,
}

var pushOutcomeValues = map[PushOutcome]bool{
	PushNotDispatched: true, PushUnknown: true, PushLanded: true,
}

// decodeState parses persisted state bytes under a closed schema and
// re-verifies the integrity hash before the state may act.
func decodeState(data []byte) (State, error) {
	if len(data) > maxStateBytes {
		return State{}, invalid("publication state exceeds the maximum allowed size")
	}
	if !utf8.Valid(data) || bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		return State{}, invalid("publication state must be UTF-8 without a BOM")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return State{}, blocked("persisted publication state is not valid JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return State{}, blocked("unexpected data after publication state")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return State{}, blocked("publication state must be a JSON object")
	}
	if len(object) != len(stateFields) {
		return State{}, blocked("publication state fields are missing or unsupported")
	}
	for key := range object {
		if !stateFields[key] {
			return State{}, blocked("publication state fields are missing or unsupported")
		}
	}
	schema, err := intField(object, "schema_version")
	if err != nil {
		return State{}, err
	}
	if schema != publicationSchema {
		return State{}, blocked("unsupported publication state schema version")
	}
	taskID, err := stringField(object, "task_id")
	if err != nil {
		return State{}, err
	}
	if !validUUID(taskID) {
		return State{}, invalid("publication state task identity is malformed")
	}
	remote, err := stringField(object, "remote")
	if err != nil {
		return State{}, err
	}
	ref, err := stringField(object, "ref")
	if err != nil {
		return State{}, err
	}
	authorizedBy, err := stringField(object, "authorized_by")
	if err != nil {
		return State{}, err
	}
	if err := validateRemote(remote, authorizedBy); err != nil {
		return State{}, err
	}
	if err := validateRef(ref); err != nil {
		return State{}, err
	}
	evidenceHash, err := stringField(object, "evidence_hash")
	if err != nil {
		return State{}, err
	}
	if !validSHA256(evidenceHash) {
		return State{}, invalid("publication state evidence identity is malformed")
	}
	planHash, err := stringField(object, "plan_hash")
	if err != nil {
		return State{}, err
	}
	if !validSHA256(planHash) {
		return State{}, invalid("publication state plan identity is malformed")
	}
	baseline, err := stringField(object, "baseline")
	if err != nil {
		return State{}, err
	}
	if !validCommitID(CommitID(baseline)) {
		return State{}, invalid("publication state baseline is not an object identity")
	}
	message, err := stringField(object, "message")
	if err != nil {
		return State{}, err
	}
	if strings.TrimSpace(message) == "" || strings.Contains(message, "\x00") {
		return State{}, invalid("publication state message is malformed")
	}
	paths, err := stringsField(object, "paths")
	if err != nil {
		return State{}, err
	}
	for _, path := range paths {
		if !validRelativePath(path) {
			return State{}, invalid("unsafe persisted path: %s", path)
		}
	}
	stageText, err := stringField(object, "stage")
	if err != nil {
		return State{}, err
	}
	stage := Stage(stageText)
	if !stageValues[stage] {
		return State{}, blocked("publication state has an unknown stage")
	}
	commit, err := stringField(object, "commit_oid")
	if err != nil {
		return State{}, err
	}
	pushed, err := boolField(object, "push_dispatched")
	if err != nil {
		return State{}, err
	}
	outcomeText, err := stringField(object, "push_outcome")
	if err != nil {
		return State{}, err
	}
	outcome := PushOutcome(outcomeText)
	if !pushOutcomeValues[outcome] {
		return State{}, blocked("publication state has an unknown push outcome")
	}
	if stage == StageIdle || stage == StagePrepared {
		if commit != "" || pushed || outcome != PushNotDispatched {
			return State{}, blocked("unstarted publication state records side effects")
		}
	} else if !validCommitID(CommitID(commit)) {
		return State{}, blocked("publication state lacks a valid commit identity")
	}
	if !pushed && outcome != PushNotDispatched {
		return State{}, blocked("publication state records an outcome without a dispatch")
	}
	if (stage == StagePushed || stage == StageVerified) && (!pushed || outcome != PushLanded) {
		return State{}, blocked("publication state records an unsettled dispatch")
	}
	stateHash, err := stringField(object, "state_sha256")
	if err != nil {
		return State{}, err
	}
	state := State{
		SchemaVersion:  schema,
		TaskID:         taskID,
		Remote:         remote,
		Ref:            ref,
		AuthorizedBy:   authorizedBy,
		EvidenceHash:   evidenceHash,
		PlanHash:       planHash,
		Baseline:       CommitID(baseline),
		Message:        message,
		Paths:          paths,
		Stage:          stage,
		Commit:         CommitID(commit),
		PushDispatched: pushed,
		PushOutcome:    outcome,
		StateHash:      stateHash,
	}
	if err := verifyStateHash(state); err != nil {
		return State{}, err
	}
	return state, nil
}

func stringField(object map[string]any, key string) (string, error) {
	value, ok := object[key].(string)
	if !ok {
		return "", blocked("publication state field %s is malformed", key)
	}
	return value, nil
}

func boolField(object map[string]any, key string) (bool, error) {
	value, ok := object[key].(bool)
	if !ok {
		return false, blocked("publication state field %s is malformed", key)
	}
	return value, nil
}

func intField(object map[string]any, key string) (int64, error) {
	number, ok := object[key].(json.Number)
	if !ok {
		return 0, blocked("publication state field %s is malformed", key)
	}
	value, err := number.Int64()
	if err != nil {
		return 0, blocked("publication state field %s is malformed", key)
	}
	return value, nil
}

func stringsField(object map[string]any, key string) ([]string, error) {
	items, ok := object[key].([]any)
	if !ok {
		return nil, blocked("publication state field %s is malformed", key)
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, blocked("publication state field %s is malformed", key)
		}
		result = append(result, text)
	}
	return result, nil
}
