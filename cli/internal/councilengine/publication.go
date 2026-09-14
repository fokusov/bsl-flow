package councilengine

import (
	"os"
	"path/filepath"
	"strings"
)

// publication.go ports New-BSLFlowCouncilPreparedPackage resume half:
// Resume-BSLFlowCouncilPublication and
// Resume-BSLFlowCouncilPreparedPublicationIfPresent. A durable `prepared`
// event precedes the first live write; recovery completes the remaining files
// without model calls only when every live artifact matches the draft or the
// intended final bytes; any third content is BLOCKED.

// ResumePreparedPublicationIfPresent ports
// Resume-BSLFlowCouncilPreparedPublicationIfPresent: the shared public
// recovery seam. Returns nil when no prepared package exists.
func (e *Engine) ResumePreparedPublicationIfPresent(projectRoot, changeName string) (*ordered, error) {
	if !changeNamePattern.MatchString(changeName) {
		return nil, invalid("Unsafe OpenSpec change name: %s", changeName)
	}
	runRoot := RunRoot(projectRoot, changeName)
	packagePath := filepath.Join(runRoot, "publication", "prepared.json")
	if !isRegularFile(packagePath) {
		return nil, nil
	}
	reviewPath := filepath.Join(runRoot, "publication", "canonical-review.json")
	if !isRegularFile(reviewPath) {
		return nil, blocked("prepared council publication has no canonical review.")
	}
	reviewObject, err := readOrderedObject(reviewPath)
	if err != nil {
		return nil, blocked("prepared council publication has no canonical review.")
	}
	changeDir := ChangeDir(projectRoot, changeName)
	return e.ResumePublication(changeDir, runRoot, reviewObject, projectRoot)
}

// ResumePublication ports Resume-BSLFlowCouncilPublication. review is the
// decoded canonical review object.
func (e *Engine) ResumePublication(changeDir, runRoot string, review *ordered, projectPath string) (*ordered, error) {
	publicationDir := filepath.Join(runRoot, "publication")
	packagePath := filepath.Join(publicationDir, "prepared.json")
	if !isRegularFile(packagePath) {
		return nil, blocked("No prepared publication package to resume.")
	}
	preparedEventPath := filepath.Join(publicationDir, "prepared.event.json")
	if !isRegularFile(preparedEventPath) {
		return nil, blocked("prepared council publication has no durable prepared event.")
	}
	preparedEvent, err := readJSONObject(preparedEventPath)
	if err != nil {
		return nil, blocked("prepared council publication has no durable prepared event.")
	}
	if asStringOr(preparedEvent["event"]) != "prepared" || !sha256Pattern.MatchString(asStringOr(preparedEvent["package_sha256"])) {
		return nil, blocked("prepared council publication event is invalid.")
	}
	packageFileSHA, err := fileSHA256(packagePath)
	if err != nil {
		return nil, err
	}
	if packageFileSHA != asStringOr(preparedEvent["package_sha256"]) {
		return nil, blocked("prepared council publication package changed after its durable event.")
	}
	pkg, err := readJSONObject(packagePath)
	if err != nil {
		return nil, blocked("prepared council publication package changed after its durable event.")
	}
	for _, field := range []string{"expected_draft_spec_sha256", "expected_draft_original_sha256", "intended_final_spec_sha256", "review_sha256", "review_file_sha256"} {
		if !sha256Pattern.MatchString(asStringOr(pkg[field])) {
			return nil, blocked("prepared council publication misses a valid %s.", field)
		}
	}
	for _, field := range []string{"expected_draft_design_sha256", "intended_final_design_sha256"} {
		if pkg[field] != nil && !sha256Pattern.MatchString(asStringOr(pkg[field])) {
			return nil, blocked("prepared council publication has an invalid %s.", field)
		}
	}
	intendedSpec, err := readFileBytes(filepath.Join(publicationDir, "intended-spec.md"))
	if err != nil {
		return nil, err
	}
	intendedDesignPath := filepath.Join(publicationDir, "intended-design.md")
	var intendedDesign []byte
	if isRegularFile(intendedDesignPath) {
		intendedDesign, err = readFileBytes(intendedDesignPath)
		if err != nil {
			return nil, err
		}
	}
	if sha256Hex(intendedSpec) != asStringOr(pkg["intended_final_spec_sha256"]) {
		return nil, blocked("prepared intended spec bytes do not match their recorded hash.")
	}
	if intendedDesign != nil {
		if pkg["intended_final_design_sha256"] == nil || sha256Hex(intendedDesign) != asStringOr(pkg["intended_final_design_sha256"]) {
			return nil, blocked("prepared intended design bytes do not match their recorded hash.")
		}
	} else if pkg["intended_final_design_sha256"] != nil {
		return nil, blocked("prepared intended design bytes are missing.")
	}
	canonicalReviewPath := filepath.Join(publicationDir, "canonical-review.json")
	if !isRegularFile(canonicalReviewPath) {
		return nil, blocked("prepared council publication has no canonical review.")
	}
	canonicalReview, err := readFileBytes(canonicalReviewPath)
	if err != nil {
		return nil, err
	}
	if sha256Hex(canonicalReview) != asStringOr(pkg["review_file_sha256"]) {
		return nil, blocked("prepared canonical review bytes do not match their recorded hash.")
	}
	canonicalObject, err := readOrderedObject(canonicalReviewPath)
	if err != nil {
		return nil, blocked("prepared canonical review bytes do not match their recorded hash.")
	}
	orderedReview := canonicalObject
	if err := AssertCouncilReview(orderedReview); err != nil {
		return nil, err
	}
	digest, err := CouncilReviewDigest(orderedReview)
	if err != nil {
		return nil, err
	}
	if digest != asStringOr(pkg["review_sha256"]) {
		return nil, blocked("prepared canonical review digest does not match its recorded hash.")
	}
	if review != nil {
		suppliedDigest, err := CouncilReviewDigest(review)
		if err != nil {
			return nil, err
		}
		if suppliedDigest != asStringOr(pkg["review_sha256"]) {
			return nil, blocked("supplied council review does not match the prepared publication.")
		}
	}
	// expected_draft_* must equal the canonical review inputs.
	canonicalInputs, _ := asOrdered(orderedReview.get("inputs"))
	for _, pair := range []struct{ field, key string }{
		{"expected_draft_spec_sha256", "spec_sha256"},
		{"expected_draft_original_sha256", "original_task_sha256"},
		{"expected_draft_design_sha256", "design_sha256"},
	} {
		packageValue := pkg[pair.field]
		inputValue := any(nil)
		if canonicalInputs != nil {
			inputValue = canonicalInputs.get(pair.key)
		}
		if packageValue == nil && inputValue == nil {
			continue
		}
		if asStringOr(packageValue) != asStringOr(inputValue) {
			return nil, blocked("prepared publication input hash mismatch: %s.", pair.field)
		}
	}
	specPath := filepath.Join(changeDir, "spec.md")
	designPath := filepath.Join(changeDir, "design.md")
	reviewPath := filepath.Join(changeDir, "review.json")
	originalPath := filepath.Join(changeDir, "original-task.md")
	if !isRegularFile(originalPath) {
		return nil, blocked("live original-task.md is missing; refusing to publish recovery bytes.")
	}
	originalSHA, err := fileSHA256(originalPath)
	if err != nil {
		return nil, err
	}
	if originalSHA != asStringOr(pkg["expected_draft_original_sha256"]) {
		return nil, blocked("live original-task.md changed after the council publication was prepared.")
	}
	if projectPath != "" {
		policyPath := filepath.Join(strings.TrimRight(projectPath, `\/`), "bsl-flow.yaml")
		if !isRegularFile(policyPath) {
			return nil, blocked("current council policy is missing; refusing to publish recovery bytes.")
		}
		policyHash, err := PolicyHash(policyPath)
		if err != nil {
			return nil, err
		}
		if policyHash != asStringOr(canonicalInputs.get("policy_hash")) {
			return nil, blocked("council policy changed after the publication was prepared.")
		}
	}
	liveSpec, err := readFileBytes(specPath)
	if err != nil {
		return nil, err
	}
	liveSpecHash := sha256Hex(liveSpec)
	if liveSpecHash != asStringOr(pkg["expected_draft_spec_sha256"]) && liveSpecHash != asStringOr(pkg["intended_final_spec_sha256"]) {
		return nil, blocked("live spec.md matches neither draft nor intended final bytes; refusing to overwrite.")
	}
	if isRegularFile(designPath) {
		liveDesignHash := sha256Hex(readFileBytesUnsafe(designPath))
		draftOK := pkg["expected_draft_design_sha256"] != nil && liveDesignHash == asStringOr(pkg["expected_draft_design_sha256"])
		finalOK := pkg["intended_final_design_sha256"] != nil && liveDesignHash == asStringOr(pkg["intended_final_design_sha256"])
		if !(draftOK || finalOK) {
			return nil, blocked("live design.md matches neither draft nor intended final bytes; refusing to overwrite.")
		}
	} else if pkg["expected_draft_design_sha256"] != nil || intendedDesign != nil {
		return nil, blocked("live design.md state is ambiguous; refusing to create it.")
	}
	if isRegularFile(reviewPath) {
		liveReviewHash := sha256Hex(readFileBytesUnsafe(reviewPath))
		reviewDraftOK := pkg["expected_draft_review_file_sha256"] != nil && liveReviewHash == asStringOr(pkg["expected_draft_review_file_sha256"])
		reviewFinalOK := liveReviewHash == asStringOr(pkg["review_file_sha256"])
		if !(reviewDraftOK || reviewFinalOK) {
			return nil, blocked("live review.json matches neither draft nor intended final bytes; refusing to overwrite.")
		}
	} else if pkg["expected_draft_review_file_sha256"] != nil {
		return nil, blocked("live review.json state is ambiguous; refusing to create it.")
	}
	// Single writer completes the remaining files without model calls.
	if err := os.WriteFile(specPath, intendedSpec, 0o644); err != nil {
		return nil, blocked("%v", err)
	}
	if intendedDesign != nil {
		if err := os.WriteFile(designPath, intendedDesign, 0o644); err != nil {
			return nil, blocked("%v", err)
		}
	}
	if err := os.WriteFile(reviewPath, canonicalReview, 0o644); err != nil {
		return nil, blocked("%v", err)
	}
	finalSpecHash, err := fileSHA256(specPath)
	if err != nil {
		return nil, err
	}
	finalReviewHash, err := fileSHA256(reviewPath)
	if err != nil {
		return nil, err
	}
	if finalSpecHash != asStringOr(pkg["intended_final_spec_sha256"]) {
		return nil, blocked("Publication failed to converge on intended final spec bytes.")
	}
	if finalReviewHash != asStringOr(pkg["review_file_sha256"]) {
		return nil, blocked("Publication failed to converge on intended review bytes.")
	}
	// Completion is recorded only after the deterministic final validation
	// passes.
	if e.FinalValidation == nil {
		return nil, blocked("final validation seam is unavailable.")
	}
	finalValidation, err := e.FinalValidation(changeDir)
	if err != nil {
		return nil, err
	}
	finalValidationPath := filepath.Join(changeDir, "final-validation.json")
	if !isRegularFile(finalValidationPath) {
		return nil, blocked("final validation receipt is missing after recovery.")
	}
	finalValidationSHA, err := fileSHA256(finalValidationPath)
	if err != nil {
		return nil, err
	}
	event := orderedFrom(
		[]string{"event", "at_utc", "review_sha256"},
		[]any{"completed", roundTripUTCTime(e.now()), finalReviewHash},
	)
	if err := writeBSLFlowJSONAtomic(filepath.Join(publicationDir, "completed.event.json"), event); err != nil {
		return nil, err
	}
	return orderedFrom(
		[]string{"resumed", "review_sha256", "final_validation", "final_validation_sha256"},
		[]any{true, finalReviewHash, finalValidation, finalValidationSHA},
	), nil
}

func readFileBytesUnsafe(path string) []byte {
	data, _ := os.ReadFile(path)
	return data
}

func asOrderedAny(value any) (*ordered, bool) {
	return asOrdered(value)
}
