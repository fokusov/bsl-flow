package stagehost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// This file ports the verify path: Invoke-BFProviderExecute,
// Invoke-BFStageObservation (verify branch), Invoke-BFVerification,
// Invoke-BFExecutionCheck and Stop-BFVerificationFailure. Worker-dispatching
// stages are rejected with a typed blocker; they remain on the packaged
// compatibility provider during this migration increment.

type providerContextInfo struct {
	taskID         string
	contextRoot    string
	artifactRoot   string
	cancelSignal   string
	canonicalStore string
	priorArtifacts map[string]map[string]any
}

// providerExecute mirrors Invoke-BFProviderExecute.
func providerExecute(ctx context.Context, deps Deps, input *providerInput) (map[string]any, error) {
	state := input.stateView
	attempt := input.attempt
	state = cloneWithCanonicalStore(state, input.canonicalStore)
	cancelled, err := providerCancelled(input.cancelSignal, input.taskID, asStringOr(attempt["attempt_id"]))
	if err != nil {
		return nil, err
	}
	if cancelled {
		return nil, blockedf("cancelled before provider dispatch.")
	}
	current, err := stageDependencies(state, asStringOr(attempt["stage"]))
	if err != nil {
		return nil, err
	}
	currentHash, err := hashValue(current)
	if err != nil {
		return nil, err
	}
	attemptDependencyHash, err := hashValue(attempt["dependencies"])
	if err != nil {
		return nil, err
	}
	if currentHash != attemptDependencyHash {
		return nil, blockedf("provider inputs changed before dispatch.")
	}
	run := &stageRun{
		state:       state,
		attempt:     attempt,
		directory:   input.artifactRoot,
		contextRoot: input.contextRoot,
		cancel: func() (bool, error) {
			return providerCancelled(input.cancelSignal, input.taskID, asStringOr(attempt["attempt_id"]))
		},
		providerContext: &providerContextInfo{
			taskID:         input.taskID,
			contextRoot:    input.contextRoot,
			artifactRoot:   input.artifactRoot,
			cancelSignal:   input.cancelSignal,
			canonicalStore: input.canonicalStore,
			priorArtifacts: input.priorArtifacts,
		},
	}
	terminal, err := stageObservation(ctx, deps, run, input)
	if err != nil {
		return nil, err
	}
	return convertToProviderOutput(deps, input, terminal)
}

type stageRun struct {
	state           map[string]any
	attempt         map[string]any
	directory       string
	contextRoot     string
	cancel          func() (bool, error)
	providerContext *providerContextInfo
}

// stageObservation mirrors Invoke-BFStageObservation for the verify stage:
// the stage host owns the try/catch envelope, dependency rebinding and
// failure receipt, never a task transition.
func stageObservation(ctx context.Context, deps Deps, run *stageRun, input *providerInput) (map[string]any, error) {
	state := run.state
	stage := asStringOr(run.attempt["stage"])
	raw := filepath.Join(run.directory, "raw")
	if err := os.MkdirAll(raw, 0o755); err != nil {
		return nil, blockedf("%v", err)
	}
	outcome := "BLOCKED"
	summary := "Attempt did not complete."
	sideEffects := "none"
	var proposal any
	result, verifyErr := runStageBody(ctx, deps, run, raw)
	if verifyErr != nil {
		summary = verifyErr.Error()
		if strings.HasPrefix(summary, ClassFail+":") {
			outcome = "FAIL"
		} else {
			outcome = "BLOCKED"
		}
		if stage == "implement" || stage == "verify" {
			sideEffects = "unknown"
		}
		var failure *VerificationFailure
		if asVerificationFailure(verifyErr, &failure) {
			current, err := stageDependencies(state, stage)
			if err != nil {
				summary = err.Error()
				outcome = "BLOCKED"
				sideEffects = "unknown"
			} else {
				currentHash, hashErr := hashValue(current)
				attemptHash, attemptErr := hashValue(run.attempt["dependencies"])
				if hashErr != nil || attemptErr != nil {
					summary = firstError(hashErr, attemptErr).Error()
					outcome = "BLOCKED"
					sideEffects = "unknown"
				} else if currentHash != attemptHash {
					summary = "BF_BLOCKED: inputs changed during failed verification."
					outcome = "BLOCKED"
					sideEffects = "unknown"
				} else if _, err := protectedTestManifest(state, asMap(run.attempt["source_manifest"])); err != nil {
					summary = err.Error()
					outcome = "BLOCKED"
					sideEffects = "unknown"
				} else {
					sideEffects = "none"
					proposal = failureMarker(failure)
				}
			}
		}
		if err := writeJSON(filepath.Join(raw, "failure.json"), map[string]any{
			"reason":       summary,
			"side_effects": sideEffects,
		}, true); err != nil {
			return nil, err
		}
	} else if result != nil {
		summary = asStringOr(result["summary"])
		switch asStringOr(result["status"]) {
		case "needs_input":
			outcome = "NEEDS_INPUT"
		case "failed":
			outcome = "FAIL"
		case "blocked":
			outcome = "BLOCKED"
		case "completed":
			outcome = "PASS"
		default:
			return nil, invalidf("invalid worker stage status.")
		}
	}
	dependencies := run.attempt["dependencies"]
	if outcome == "PASS" || outcome == "REVISE" || outcome == "REPAIR" {
		current, err := stageDependencies(state, stage)
		if err != nil {
			return nil, err
		}
		currentHash, err := hashValue(current)
		if err != nil {
			return nil, err
		}
		attemptHash, err := hashValue(dependencies)
		if err != nil {
			return nil, err
		}
		if currentHash != attemptHash {
			outcome = "BLOCKED"
			summary = "Inputs changed during a read-only stage."
		}
	}
	if stage != "implement" {
		manifest, err := stageSourceManifest(state)
		if err != nil {
			return nil, err
		}
		if asStringOr(manifest["sha256"]) != asStringOr(asMap(run.attempt["source_manifest"])["sha256"]) {
			outcome = "BLOCKED"
			summary = "BF_BLOCKED: source changed during a read-only or verification stage."
			proposal = nil
			sideEffects = "unknown"
			if err := writeJSON(filepath.Join(raw, "failure.json"), map[string]any{
				"reason":       summary,
				"side_effects": sideEffects,
			}, true); err != nil {
				return nil, err
			}
		}
	}
	return map[string]any{
		"schema_version": int64(1),
		"task_id":        asStringOr(state["task_id"]),
		"attempt_id":     asStringOr(run.attempt["attempt_id"]),
		"stage":          stage,
		"outcome":        outcome,
		"summary":        summary,
		"dependencies":   dependencies,
		"proposal":       proposal,
		"side_effects":   sideEffects,
	}, nil
}

func firstError(errors ...error) error {
	for _, err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}

// runStageBody is the try-block of Invoke-BFStageObservation: pre-dispatch
// gates, the stage body dispatch and nothing else. Every failure here is a
// stage outcome, never a controller decision.
func runStageBody(ctx context.Context, deps Deps, run *stageRun, raw string) (map[string]any, error) {
	state := run.state
	stage := asStringOr(run.attempt["stage"])
	if cancelled, err := run.cancel(); err != nil {
		return nil, err
	} else if cancelled {
		return nil, blockedf("cancelled before dispatch.")
	}
	bound, err := stageDependencies(state, stage)
	if err != nil {
		return nil, err
	}
	boundHash, err := hashValue(bound)
	if err != nil {
		return nil, err
	}
	attemptDependencyHash, err := hashValue(run.attempt["dependencies"])
	if err != nil {
		return nil, err
	}
	if boundHash != attemptDependencyHash {
		return nil, blockedf("inputs changed before dispatch.")
	}
	if stage != "verify" {
		return nil, blockedf("stage %s is not served by the native provider process yet.", stage)
	}
	return runVerification(ctx, deps, state, raw, asStringOr(run.attempt["executable"]), run)
}

// failureMarker renders the BF_VerificationFailure exception data the
// controller uses to decide repair eligibility.
func failureMarker(failure *VerificationFailure) map[string]any {
	return map[string]any{
		"repair_eligible": failure.RepairEligible,
		"criterion_id":    failure.CriterionID,
		"kind":            failure.Kind,
		"observation":     failure.Observation,
	}
}

func asVerificationFailure(err error, target **VerificationFailure) bool {
	typed, ok := err.(*VerificationFailure)
	if !ok {
		return false
	}
	*target = typed
	return true
}

func cloneWithCanonicalStore(state map[string]any, canonicalStore string) map[string]any {
	cloned, err := cloneStateView(state)
	if err != nil {
		return state
	}
	cloned["canonical_store_root"] = canonicalStore
	return cloned
}

var _ = os.Getpid
