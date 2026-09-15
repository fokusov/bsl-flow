package repository

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"bsl-flow/cli/internal/delivery"
)

// The native publish/publish-resume commands drive the delivery domain state
// machine over a real git CLI port. Compared with the PowerShell publication
// flow the evidence chain is narrower by design: the sealed per-publication
// state document and the immutable request replace the registration/
// prepared/intent/receipt document chain, and the push is bounded in-process
// (force-with-lease, create-only) instead of a non-interruptible receipted
// process. The safety semantics are identical: at most one push dispatch, a
// foreign remote OID is never overwritten, and an unknown push effect blocks
// automatic replay until the remote is reconciled.

var (
	publicationRefPattern  = regexp.MustCompile(`^refs/heads/codex/[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	publicationRefUnsafe   = regexp.MustCompile(`\.\.|//|/\.|\.lock(?:/|$)|[./]$`)
	publicationSHA256      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	publicationEmail       = regexp.MustCompile(`^[^\s<>\x00-\x1f]+@[A-Za-z0-9.-]+$`)
)

type publicationRequest struct {
	publicationID   string
	acceptanceID    string
	remote          string
	ref             string
	auth            string
	authorName      string
	authorEmail     string
	message         string
	baseline        string
	manifestSHA256  string
	manifestFiles   []delivery.ManifestFile
	allowedPaths    []string
	requestDocument map[string]any
}

// containsControlChars reports C0 control characters; the PowerShell input
// contract rejects them in remote and author text.
func containsControlChars(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

// parsePublicationRequest is the port of Assert-BFPublicationInput; the
// message shapes of every refusal are kept byte-identical.
func parsePublicationRequest(input map[string]any, taskID string) (*publicationRequest, error) {
	for _, field := range []string{"schema_version", "publication_id", "task_id", "acceptance_sha256", "remote", "ref", "auth", "author", "message", "provenance"} {
		if _, ok := input[field]; !ok {
			return nil, invalid("publication.%s is missing.", field)
		}
	}
	if asIntOr(input["schema_version"]) != 1 {
		return nil, invalid("unsupported publication schema.")
	}
	publicationID := asStringOr(input["publication_id"])
	if !isUUID(publicationID) {
		return nil, invalid("publication.publication_id is invalid.")
	}
	if asStringOr(input["task_id"]) != taskID {
		return nil, invalid("publication belongs to another task.")
	}
	acceptanceID := asStringOr(input["acceptance_sha256"])
	if !publicationSHA256.MatchString(acceptanceID) {
		return nil, invalid("exact acceptance SHA-256 is required.")
	}
	remote := asStringOr(input["remote"])
	if len(remote) > 4096 || containsControlChars(remote) || strings.HasPrefix(remote, "-") {
		return nil, invalid("invalid publication remote.")
	}
	ref := asStringOr(input["ref"])
	if !publicationRefPattern.MatchString(ref) || publicationRefUnsafe.MatchString(ref) {
		return nil, invalid("publication requires a new full refs/heads/codex/* branch.")
	}
	auth := asStringOr(input["auth"])
	if auth != "none" && auth != "github_cli" {
		return nil, invalid("unsupported publication auth profile.")
	}
	author, _ := input["author"].(map[string]any)
	if author == nil {
		return nil, invalid("publication.author is missing.")
	}
	authorName := asStringOr(author["name"])
	authorEmail := asStringOr(author["email"])
	if len(authorName) > 256 || strings.ContainsAny(authorName, "<>") || containsControlChars(authorName) || !publicationEmail.MatchString(authorEmail) {
		return nil, invalid("invalid publication author identity.")
	}
	message := asStringOr(input["message"])
	if len(message) > 16384 {
		return nil, invalid("publication.message exceeds 16384 characters.")
	}
	if strings.ContainsRune(message, 0) {
		return nil, invalid("NUL in publication message.")
	}
	provenance, _ := input["provenance"].(map[string]any)
	if provenance == nil {
		return nil, invalid("provenance is missing.")
	}
	for _, field := range []string{"source", "reference", "text"} {
		if _, ok := provenance[field]; !ok {
			return nil, invalid("provenance.%s is missing.", field)
		}
	}
	if asStringOr(provenance["source"]) != "user" {
		return nil, invalid("only a trusted operator can relay user input; worker output is not authorization.")
	}
	if len(asStringOr(provenance["reference"])) > 2048 {
		return nil, invalid("provenance.reference exceeds 2048 characters.")
	}
	return &publicationRequest{
		publicationID:   publicationID,
		acceptanceID:    acceptanceID,
		remote:          remote,
		ref:             ref,
		auth:            auth,
		authorName:      authorName,
		authorEmail:     authorEmail,
		message:         message,
		requestDocument: input,
	}, nil
}

// acceptedDeliveryReceipt is the port of Get-BFDeliveryReceipt: only the
// latest retained implementation PASS of a completed task qualifies, and the
// receipt file must still hash to its recorded identity.
func acceptedDeliveryReceipt(payload map[string]any, taskID string) (map[string]any, string, error) {
	if asStringOr(payload["status"]) != "completed" {
		return nil, "", blocked("delivery requires a completed accepted task")
	}
	items := anyItems(payload["acceptances"])
	if len(items) == 0 {
		return nil, "", blocked("delivery requires a completed accepted task")
	}
	accepted, ok := items[len(items)-1].(map[string]any)
	if !ok {
		return nil, "", blocked("delivery requires a completed accepted task")
	}
	if asStringOr(accepted["verdict"]) != "PASS" || asStringOr(accepted["mode"]) != "implement" || asIntOr(accepted["intent_revision"]) != asIntOr(payload["intent_revision"]) {
		return nil, "", blocked("latest acceptance is not a current implementation PASS.")
	}
	identity := asStringOr(accepted["sha256"])
	if !publicationSHA256.MatchString(identity) {
		return nil, "", blocked("acceptance identity is invalid.")
	}
	data, err := ReadFileBytes(asStringOr(accepted["path"]))
	if err != nil {
		return nil, "", blocked("acceptance receipt is missing.")
	}
	receipt, err := DecodeObject(data)
	if err != nil {
		return nil, "", blocked("acceptance receipt is unreadable.")
	}
	digest, err := Hash(receipt)
	if err != nil {
		return nil, "", err
	}
	if digest != identity {
		return nil, "", blocked("acceptance receipt hash does not match task state.")
	}
	if asStringOr(receipt["task_id"]) != taskID || asStringOr(receipt["mode"]) != "implement" || asStringOr(receipt["verdict"]) != "PASS" {
		return nil, "", blocked("acceptance receipt does not bind the current task identity.")
	}
	if asIntOr(receipt["intent_revision"]) != asIntOr(payload["intent_revision"]) || asStringOr(receipt["intent_hash"]) != asStringOr(payload["intent_hash"]) || asStringOr(receipt["policy_hash"]) != asStringOr(payload["policy_hash"]) || asStringOr(receipt["baseline"]) != asStringOr(payload["baseline"]) {
		return nil, "", blocked("acceptance receipt does not bind the current task identity.")
	}
	return receipt, identity, nil
}

// assertDeliveryCurrent is the port of Assert-BFDeliveryCurrent without the
// delivery-directory export: the accepted manifest must still describe the
// current worker source, the route gates must still be fresh, and the
// acceptance gates must match the receipt.
func assertDeliveryCurrent(repository *Repository, task *Task, payload, receipt map[string]any) error {
	next, err := controllerNext(task.State, payload)
	if err != nil {
		return err
	}
	if asStringOr(next["action"]) != "accept" || asStringOr(next["stage"]) != "acceptance" {
		return blocked("acceptance is stale or task is not ready for delivery.")
	}
	manifest, err := sourceManifestWithBaseline(asStringOr(payload["worker_path"]), asStringOr(payload["baseline"]), []string{"."})
	if err != nil {
		return blocked("cannot bind the current source manifest: %v", err)
	}
	acceptedManifest, _ := receipt["source_manifest"].(map[string]any)
	if acceptedManifest == nil {
		return blocked("acceptance receipt has no source manifest.")
	}
	if asStringOr(acceptedManifest["sha256"]) != asStringOr(manifest["sha256"]) {
		return blocked("current source does not match the accepted manifest.")
	}
	acceptedDigest, err := Hash(acceptedManifest)
	if err != nil {
		return err
	}
	currentDigest, err := Hash(manifest)
	if err != nil {
		return err
	}
	if acceptedDigest != currentDigest {
		return blocked("current source does not match the accepted manifest.")
	}
	gates, err := currentRouteGates(task, payload)
	if err != nil {
		return err
	}
	receiptGates, err := Hash(receipt["gates"])
	if err != nil {
		return err
	}
	currentGates, err := Hash(gates)
	if err != nil {
		return err
	}
	if receiptGates != currentGates {
		return blocked("acceptance gates do not match current evidence.")
	}
	return assertCoverageAccepted(repository, task, payload)
}

// currentRouteGates rebuilds the accepted gate list of the route the same way
// the acceptance receipt recorded it.
func currentRouteGates(task *Task, payload map[string]any) ([]any, error) {
	gates := []any{}
	for _, stage := range routeForController(payload) {
		if stage == "acceptance" {
			break
		}
		evidence, ok := latestEvidence(payload, stage)
		if !ok || asStringOr(evidence["outcome"]) != "PASS" || !controllerEvidenceFresh(task.State, payload, evidence) {
			return nil, blocked("stale or missing %s evidence at delivery", stage)
		}
		gates = append(gates, map[string]any{"stage": stage, "attempt_id": evidence["attempt_id"], "result_sha256": evidence["result_sha256"]})
	}
	return gates, nil
}

// assertCoverageAccepted is the focused port of Assert-BFCoverageAccepted:
// an implement request that declares coverage requirements needs a fresh
// independent code review whose result carries a PASS coverage review. The
// binding file revalidation stays at evidence-ingestion time, where the
// native controller already enforces it.
func assertCoverageAccepted(repository *Repository, task *Task, payload map[string]any) error {
	request, _ := payload["request"].(map[string]any)
	if request == nil || asStringOr(request["mode"]) != "implement" {
		return nil
	}
	if _, ok := request["requirements"]; !ok {
		return nil
	}
	evidence, ok := latestEvidence(payload, "code_review")
	if !ok || !controllerEvidenceFresh(task.State, payload, evidence) {
		return blocked("requirement coverage needs a fresh independent code review.")
	}
	attemptPath, err := attemptDirectory(repository, task.ID, asStringOr(evidence["attempt_id"]))
	if err != nil {
		return err
	}
	data, err := ReadFileBytes(filepath.Join(attemptPath, "result.json"))
	if err != nil {
		return blocked("coverage review result is unavailable: %v", err)
	}
	result, err := DecodeObject(data)
	if err != nil {
		return blocked("coverage review result is unreadable.")
	}
	digest, err := Hash(result)
	if err != nil {
		return err
	}
	if digest != asStringOr(evidence["result_sha256"]) {
		return blocked("coverage review result changed.")
	}
	proposal, _ := result["proposal"].(map[string]any)
	if proposal == nil {
		return blocked("independently sufficient requirement coverage is missing.")
	}
	if review, ok := proposal["review"].(map[string]any); ok {
		proposal = review
	}
	coverage, _ := proposal["coverage_review"].(map[string]any)
	if coverage == nil || asStringOr(coverage["verdict"]) != "PASS" {
		return blocked("independently sufficient requirement coverage is missing.")
	}
	return nil
}

// commandPublish is the native Publish action; resumeOnly switches it to the
// PublishResume behavior, which never dispatches a push and only settles the
// observation of an existing publication.
func commandPublish(project, id, inputPath string, resumeOnly bool) (any, error) {
	input, err := readInput(inputPath)
	if err != nil {
		return nil, err
	}
	request, err := parsePublicationRequest(input, id)
	if err != nil {
		return nil, err
	}
	repository, err := openReady(project)
	if err != nil {
		return nil, err
	}
	task, err := repository.ReadTask(id)
	if err != nil {
		return nil, err
	}
	if task.Lifecycle != "controller" {
		return nil, blocked("task %s must be activated before delivery", id)
	}
	payload, ok := task.State["controller"].(map[string]any)
	if !ok {
		return nil, blocked("task %s has no native controller state", id)
	}
	receipt, identity, err := acceptedDeliveryReceipt(payload, id)
	if err != nil {
		return nil, err
	}
	if identity != request.acceptanceID {
		return nil, blocked("publication does not name the current acceptance.")
	}
	if err := assertDeliveryCurrent(repository, task, payload, receipt); err != nil {
		return nil, err
	}
	manifest, _ := receipt["source_manifest"].(map[string]any)
	request.baseline = asStringOr(manifest["baseline"])
	request.manifestSHA256 = asStringOr(manifest["sha256"])
	for _, raw := range anyItems(manifest["files"]) {
		item, _ := raw.(map[string]any)
		if item == nil {
			return nil, blocked("acceptance manifest is malformed.")
		}
		deleted, _ := item["deleted"].(bool)
		file := delivery.ManifestFile{Path: asStringOr(item["path"]), SHA256: asStringOr(item["sha256"]), Deleted: deleted}
		request.manifestFiles = append(request.manifestFiles, file)
		if !deleted {
			request.allowedPaths = append(request.allowedPaths, file.Path)
		}
	}
	plan, err := buildPublicationPlan(request)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(repository.StorePath, "tasks", id, "publications", request.publicationID)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, blocked("publication directory is unavailable: %v", err)
	}
	statePath := filepath.Join(directory, "state.json")
	requestPath := filepath.Join(directory, "request.json")
	idempotent := false
	stateBytes, stateErr := os.ReadFile(statePath)
	if stateErr == nil {
		idempotent = true
		if err := assertPublicationRequestStable(requestPath, request); err != nil {
			return nil, err
		}
	} else {
		if resumeOnly {
			return nil, blocked("publication has not been prepared; resume cannot dispatch.")
		}
		if err := refuseForeignPublicationPending(request); err != nil {
			return nil, err
		}
		if _, err := writeImmutableJSON(requestPath, publicationRegistration(request)); err != nil {
			return nil, blocked("publication request could not be retained: %v", err)
		}
	}
	port, err := publicationGitPort(request, payload, directory)
	if err != nil {
		return nil, err
	}
	var state delivery.State
	var outcome delivery.Outcome
	if idempotent {
		state, outcome, err = delivery.Resume(stateBytes, port)
	} else {
		state, err = delivery.Begin(plan)
		outcome = delivery.OutcomeInProgress
	}
	if err != nil {
		return nil, err
	}
	if err := writePublicationState(statePath, state); err != nil {
		return nil, err
	}
	for cycle := 0; cycle < 8 && outcome == delivery.OutcomeInProgress; cycle++ {
		if state.Stage == delivery.StageCommitted && !state.PushDispatched {
			// The pending marker is claimed exactly once, before the single
			// allowed push transition, and released on the verified receipt.
			if err := claimPublicationPending(request); err != nil {
				return nil, err
			}
		}
		state, outcome, err = delivery.Step(plan, state, port)
		if err != nil {
			_ = writePublicationState(statePath, state)
			var unknown *delivery.UnknownEffectError
			if errors.As(err, &unknown) {
				return publicationEnvelope(request, string(state.Commit), "blocked", idempotent, statePath, err), nil
			}
			return nil, err
		}
		if err := writePublicationState(statePath, state); err != nil {
			return nil, err
		}
	}
	switch outcome {
	case delivery.OutcomeVerified:
		if err := clearPublicationPending(request); err != nil {
			return nil, err
		}
		return publicationEnvelope(request, string(state.Commit), "published", idempotent, statePath, nil), nil
	case delivery.OutcomeBlockedNeedsReconciliation:
		return publicationEnvelope(request, string(state.Commit), "blocked", idempotent, statePath, nil), nil
	default:
		return nil, blocked("publication did not reach a terminal outcome; retain its controller evidence.")
	}
}

// buildPublicationPlan converts the accepted receipt manifest into the
// delivery domain contract and derives the verified plan.
func buildPublicationPlan(request *publicationRequest) (delivery.Plan, error) {
	manifest := delivery.Manifest{SchemaVersion: 1, Baseline: delivery.CommitID(request.baseline), Files: request.manifestFiles}
	evidenceHash, err := delivery.HashManifest(manifest)
	if err != nil {
		return delivery.Plan{}, err
	}
	target := delivery.Target{Remote: request.remote, Ref: request.ref, AuthorizedBy: request.auth, AllowedPaths: request.allowedPaths}
	accepted := delivery.AcceptedSource{
		TaskID:       asStringOr(request.requestDocument["task_id"]),
		Status:       "completed",
		Verdict:      "PASS",
		Mode:         "implement",
		EvidenceHash: evidenceHash,
		Manifest:     manifest,
	}
	return delivery.BuildPlan(target, accepted)
}

// publicationRegistration is the retained immutable request evidence; its
// hash pins the trusted input for idempotent replays.
func publicationRegistration(request *publicationRequest) map[string]any {
	return map[string]any{
		"schema_version":  int64(1),
		"input":           request.requestDocument,
		"request_sha256":  requestHash(request),
		"baseline":        request.baseline,
		"manifest_sha256": request.manifestSHA256,
		"metadata":        map[string]any{"name": request.authorName, "email": request.authorEmail, "message": request.message},
	}
}

func requestHash(request *publicationRequest) string {
	digest, err := Hash(request.requestDocument)
	if err != nil {
		return ""
	}
	return digest
}

// assertPublicationRequestStable refuses to reuse a publication UUID with
// different input, commit metadata, or accepted baseline (BF_CONFLICT parity).
func assertPublicationRequestStable(requestPath string, request *publicationRequest) error {
	data, err := ReadFileBytes(requestPath)
	if err != nil {
		return blocked("retained publication request is unreadable: %v", err)
	}
	saved, err := DecodeObject(data)
	if err != nil {
		return conflict("retained publication request is invalid.")
	}
	if asStringOr(saved["request_sha256"]) != requestHash(request) {
		return conflict("publication UUID was used with different input.")
	}
	metadata, _ := saved["metadata"].(map[string]any)
	if metadata == nil || asStringOr(metadata["name"]) != request.authorName || asStringOr(metadata["email"]) != request.authorEmail || asStringOr(metadata["message"]) != request.message {
		return conflict("saved commit metadata differs from the trusted input.")
	}
	if asStringOr(saved["baseline"]) != request.baseline {
		return blocked("accepted publication inputs changed before dispatch.")
	}
	return nil
}

// publicationGitPort resolves the git CLI adapter. The scratch directory is
// publication-scoped so a crash between staging and commit resumes from the
// same staged records.
func publicationGitPort(request *publicationRequest, payload map[string]any, directory string) (*delivery.CLIGitPort, error) {
	config := delivery.GitPortConfig{
		WorkDir:     asStringOr(payload["worker_path"]),
		ScratchDir:  filepath.Join(directory, "scratch"),
		Baseline:    delivery.CommitID(request.baseline),
		AuthorName:  request.authorName,
		AuthorEmail: request.authorEmail,
	}
	if request.auth == "github_cli" {
		gh, err := exec.LookPath("gh")
		if err != nil {
			return nil, blocked("publication auth=github_cli requires the GitHub CLI in PATH.")
		}
		config.GitHubCLI = gh
	}
	return delivery.NewCLIGitPort(config)
}

// writePublicationState persists the sealed domain state atomically; a torn
// write can only leave the previous sealed revision in place.
func writePublicationState(path string, state delivery.State) error {
	bytes, err := state.Bytes()
	if err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, bytes, 0o644); err != nil {
		return blocked("publication state could not be retained: %v", err)
	}
	if err := os.Rename(temp, path); err != nil {
		return blocked("publication state could not be retained: %v", err)
	}
	return nil
}

func publicationEnvelope(request *publicationRequest, commitOID, status string, idempotent bool, statePath string, err error) map[string]any {
	blockers := []any{}
	if err != nil {
		blockers = append(blockers, err.Error())
	}
	return map[string]any{
		"schema_version":    int64(1),
		"publication_id":    request.publicationID,
		"task_id":           asStringOr(request.requestDocument["task_id"]),
		"status":            status,
		"commit_oid":        nullableString(commitOID),
		"remote":            request.remote,
		"ref":               request.ref,
		"acceptance_sha256": request.acceptanceID,
		"request_sha256":    requestHash(request),
		"receipt_path":      statePath,
		"idempotent":        idempotent,
		"blockers":          blockers,
	}
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// publicationPendingPath mirrors the shared remote/ref pending guard: one
// unresolved publication per remote/ref, keyed by the same canonical hash the
// PowerShell shared directory uses.
func publicationPendingPath(request *publicationRequest) (string, error) {
	key, err := Hash(map[string]any{"remote": request.remote, "ref": request.ref})
	if err != nil {
		return "", err
	}
	root, err := os.UserCacheDir()
	if err != nil {
		return "", blocked("publication shared directory is unavailable: %v", err)
	}
	return filepath.Join(root, "BSLFlow", "publication", key, "pending.json"), nil
}

func refuseForeignPublicationPending(request *publicationRequest) error {
	path, err := publicationPendingPath(request)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return blocked("cannot inspect the shared publication pending marker: %v", err)
	}
	pending, err := DecodeObject(data)
	if err != nil {
		return blocked("this remote/ref has another unresolved publication; no write is allowed.")
	}
	if asStringOr(pending["publication_id"]) == request.publicationID && asStringOr(pending["task_id"]) == asStringOr(request.requestDocument["task_id"]) {
		return nil
	}
	return blocked("this remote/ref has another unresolved publication; no write is allowed.")
}

// claimPublicationPending reserves the remote/ref for this publication before
// the single allowed push dispatch.
func claimPublicationPending(request *publicationRequest) error {
	path, err := publicationPendingPath(request)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return blocked("publication shared directory is unavailable: %v", err)
	}
	if _, err := writeImmutableJSON(path, map[string]any{
		"schema_version": int64(1),
		"publication_id": request.publicationID,
		"task_id":        asStringOr(request.requestDocument["task_id"]),
		"request_sha256": requestHash(request),
	}); err != nil {
		return err
	}
	return nil
}

func clearPublicationPending(request *publicationRequest) error {
	path, err := publicationPendingPath(request)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return blocked("publication pending marker could not be released: %v", err)
	}
	return nil
}
