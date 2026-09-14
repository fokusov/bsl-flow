package councilengine

import (
	"os"
	"strings"
)

// finalgate.go ports Test-BSLFlowCouncilFinalGate
// (Council.Validation.ps1:486-548): the deterministic final gate — structural
// review assertion, digest verification, live hash bindings, policy hash and
// the verdict downgrades. It never upgrades a model verdict.

// FinalGateInputs carries the change-dir and policy inputs of the gate.
type FinalGateInputs struct {
	Review           *ordered
	OriginalTaskPath string
	SpecPath         string
	DesignPath       string
	LintPassed       bool
	PolicyHash       *string // nil when no project policy check
}

// FinalGateResult is the deterministic gate outcome.
type FinalGateResult struct {
	Passed bool
	Errors []string
}

// TestCouncilFinalGate ports Test-BSLFlowCouncilFinalGate.
func TestCouncilFinalGate(inputs FinalGateInputs) (*FinalGateResult, error) {
	errorsList := []string{}
	if err := AssertCouncilReview(inputs.Review); err != nil {
		errorsList = append(errorsList, "Invalid council review: "+err.Error())
	}
	if !inputs.LintPassed {
		errorsList = append(errorsList, "Final specification lint failed.")
	}
	digest, err := CouncilReviewDigest(inputs.Review)
	if err != nil {
		errorsList = append(errorsList, "Could not verify review digest: "+err.Error())
	} else {
		reconciliation, _ := asOrdered(inputs.Review.get("reconciliation"))
		if digest != asStringOr(reconciliation.get("review_sha256")) {
			errorsList = append(errorsList, "reconciliation.review_sha256 does not match the review digest.")
		}
	}
	// Live hash bindings.
	reviewInputs, _ := asOrdered(inputs.Review.get("inputs"))
	reconciliation, _ := asOrdered(inputs.Review.get("reconciliation"))
	liveOriginal, originalErr := fileSHA256(inputs.OriginalTaskPath)
	liveSpec, specErr := fileSHA256(inputs.SpecPath)
	liveDesign := any(nil)
	if inputs.DesignPath != "" && isRegularFile(inputs.DesignPath) {
		if hash, err := fileSHA256(inputs.DesignPath); err == nil {
			liveDesign = hash
		}
	}
	if originalErr == nil && liveOriginal != asStringOr(reviewInputs.get("original_task_sha256")) {
		errorsList = append(errorsList, "original-task.md changed after review.")
	}
	if specErr == nil && liveSpec != asStringOr(reconciliation.get("final_spec_sha256")) {
		errorsList = append(errorsList, "reconciliation.final_spec_sha256 does not match current spec.md.")
	}
	if asStringOr(reconciliation.get("draft_spec_sha256")) != asStringOr(reviewInputs.get("spec_sha256")) {
		errorsList = append(errorsList, "reconciliation.draft_spec_sha256 does not match the reviewed draft.")
	}
	if !sameNullableString(liveDesign, reconciliation.get("final_design_sha256")) {
		errorsList = append(errorsList, "Design hash mismatch after review.")
	}
	// Policy hash.
	if inputs.PolicyHash != nil {
		if *inputs.PolicyHash != asStringOr(reviewInputs.get("policy_hash")) {
			errorsList = append(errorsList, "Council policy changed after review.")
		}
	}
	verdict := asStringOr(inputs.Review.get("verdict"))
	diversity := asStringOr(inputs.Review.get("diversity"))
	chair, _ := asOrdered(inputs.Review.get("chair"))
	chairVerdict := asStringOr(chair.get("verdict"))
	members, _ := asArray(inputs.Review.get("members"))
	questions, _ := asArray(inputs.Review.get("questions"))
	findings, _ := asArray(inputs.Review.get("findings"))
	protected, _ := asArray(inputs.Review.get("protected"))
	chairDecisions, _ := asArray(chair.get("decisions"))
	if diversity == "degraded" && verdict == "PASS" {
		errorsList = append(errorsList, "Degraded council cannot PASS.")
	}
	if diversity == "unknown" && verdict == "PASS" {
		errorsList = append(errorsList, "Unknown diversity cannot PASS as multi-model evidence.")
	}
	for _, raw := range members {
		member, _ := asOrdered(raw)
		if asStringOr(member.get("status")) != "completed" {
			if verdict == "PASS" {
				errorsList = append(errorsList, "Council with a terminal member failure cannot PASS.")
			}
			break
		}
	}
	if chairVerdict != "PASS" && verdict == "PASS" {
		errorsList = append(errorsList, "Deterministic gate cannot upgrade the chair verdict.")
	}
	if chairVerdict == "needs_input" && verdict != "needs_input" {
		errorsList = append(errorsList, "Chair needs_input must propagate.")
	}
	if len(questions) > 0 && verdict != "needs_input" {
		errorsList = append(errorsList, "Unresolved member questions require a needs_input verdict.")
	}
	// Published final bytes must be exactly the chair's revised text.
	liveSpecText, err := strictReadText(inputs.SpecPath)
	if err == nil {
		if normalizedReference(liveSpecText) != normalizedReference(asStringOr(chair.get("final_spec_text"))) {
			errorsList = append(errorsList, "Published spec.md is not the chair final specification text.")
		}
	} else {
		errorsList = append(errorsList, "Could not compare final specification text: "+err.Error())
	}
	if verdict != "PASS" && len(findings) == 0 && len(protected) == 0 && len(chairDecisions) == 0 {
		errorsList = append(errorsList, "A non-PASS council review must contain at least one reconciled decision.")
	}
	return &FinalGateResult{Passed: len(errorsList) == 0, Errors: errorsList}, nil
}

func sameNullableString(left any, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return asStringOr(left) == asStringOr(right)
}

func strictReadText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text, err := strictPSUTF8Decode(data)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(text, "\ufeff"), nil
}
