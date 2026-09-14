package repository

// This file contains the native activation bindings used by the canonical
// controller. Keeping the activation/rebind write path together makes its
// immutable input and preflight ordering easy to review as one unit.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// commandActivateNew performs the planned -> controller transition.  The
// provider is used only for the read-only measure operation; the provider can
// never publish the ready revision.
func commandActivateNew(project, id, expectedText, inputPath string, host *ControllerHost) (any, error) {
	if inputPath == "" {
		return nil, blocked("task activation requires a full trusted request")
	}
	expected, err := strconvParseRevision(expectedText)
	if err != nil {
		return nil, err
	}
	repository, err := openReady(project)
	if err != nil {
		return nil, err
	}
	// Hold the repository graph lock from the expected-revision check through
	// worker creation and the CAS.  A stale activation must not leave a worker
	// or a newly registered intent behind while another metadata writer wins.
	unlockGraph, err := repository.graphLock()
	if err != nil {
		return nil, conflict("dependency graph lock unavailable: %v", err)
	}
	defer unlockGraph()
	task, err := repository.ReadTask(id)
	if err != nil {
		return nil, err
	}
	if task.Lifecycle != "planned" {
		return nil, conflict("task %s is not planned", id)
	}
	if task.Revision != expected {
		return nil, conflict("expected revision %d, actual revision %d", expected, task.Revision)
	}
	if err := exactGitRoot(repository); err != nil {
		return nil, err
	}
	baseline, err := cleanBaseline(repository)
	if err != nil {
		return nil, err
	}
	request, err := readInput(inputPath)
	if err != nil {
		return nil, err
	}
	if err := validateNativeActivationRequest(request, id); err != nil {
		return nil, err
	}
	provider, engine, err := resolveControllerHost(host)
	if err != nil {
		return nil, err
	}
	policyFiles, policyRules, policyHash, err := localPolicy(repository, engine)
	if err != nil {
		return nil, err
	}
	provisional, err := provisionalController(task, request, repository.Worktree, baseline, policyFiles, policyRules, policyHash, engine)
	if err != nil {
		return nil, err
	}

	// Register the exact input binding before creating a worker.  If a prior
	// activation stopped after this point, retrying the same binding is safe;
	// a different request cannot silently take over the partial operation.
	worker := filepath.Join(repository.Worktree, ".bsl-flow", "native-worktrees", id, "a1")
	if _, err := persistFreshActivationIntent(repository, id, request, engine, baseline, worker, 1); err != nil {
		return nil, err
	}
	if err := runFreshActivationMeasure(repository, task, request, provisional, baseline, provider, engine); err != nil {
		return nil, err
	}
	worker, _, err = ensureWorker(repository, id, baseline, 1)
	if err != nil {
		return nil, err
	}
	payload, err := activatedController(task, request, repository.Worktree, worker, baseline, policyFiles, policyRules, policyHash, engine)
	if err != nil {
		return nil, err
	}
	card := copyMap(task.State)
	card["lifecycle"] = "controller"
	card["controller"] = payload
	card["updated_at"] = nowUTC()
	if err := validateStateForCandidate(card, id); err != nil {
		return nil, invalid("activation candidate is invalid: %v", err)
	}
	state, err := repository.writeRevision(task.ID, card, expected)
	if err != nil {
		return nil, err
	}
	payload, ok := state["controller"].(map[string]any)
	if !ok {
		return nil, blocked("activated revision has no controller payload")
	}
	next, err := controllerNext(state, payload)
	if err != nil {
		return nil, err
	}
	return controllerEnvelope(state, payload, next), nil
}

// commandControllerUpdateNew replaces the current execution binding with a
// fresh trusted request.  Existing attempts and evidence remain in the
// journal for audit, but their old intent/policy/source dependencies cannot
// satisfy the new route.
func commandControllerUpdateNew(project, id, inputPath string, host *ControllerHost) (any, error) {
	if inputPath == "" {
		return nil, invalid("--input is required for native task update")
	}
	repository, err := openReady(project)
	if err != nil {
		return nil, err
	}
	unlockGraph, err := repository.graphLock()
	if err != nil {
		return nil, conflict("dependency graph lock unavailable: %v", err)
	}
	defer unlockGraph()
	task, err := repository.ReadTask(id)
	if err != nil {
		return nil, err
	}
	if task.Lifecycle != "controller" {
		return nil, blocked("planned task %s must be activated before update", id)
	}
	previous, err := cloneObject(asMap(task.State["controller"]))
	if err != nil {
		return nil, blocked("invalid controller state: %v", err)
	}
	request, err := readInput(inputPath)
	if err != nil {
		return nil, err
	}
	if kind, _ := request["kind"].(string); kind == "recovery" {
		return commandControllerNativeRecovery(repository, task, previous, request, host)
	}
	if previous["active_attempt"] != nil || previous["unresolved_effect"] != nil {
		return nil, blocked("reconcile the active or uncertain attempt before changing task input")
	}
	if rebind, ok := migrationRequiresRebind(task.State); ok && rebind {
		return nil, blocked("task %s requires explicit execution rebind before update", id)
	}
	if err := validateNativeActivationRequest(request, id); err != nil {
		return nil, err
	}
	if err := exactGitRoot(repository); err != nil {
		return nil, err
	}
	baseline, err := cleanBaseline(repository)
	if err != nil {
		return nil, err
	}
	provider, engine, err := resolveControllerHost(host)
	if err != nil {
		return nil, err
	}
	policyFiles, policyRules, policyHash, err := localPolicy(repository, engine)
	if err != nil {
		return nil, err
	}
	provisional, err := provisionalController(task, request, repository.Worktree, baseline, policyFiles, policyRules, policyHash, engine)
	if err != nil {
		return nil, err
	}
	authorizationRevision, err := nextBindingRevision(previous["authorization_revision"], "authorization_revision")
	if err != nil {
		return nil, err
	}
	intentRevision, err := nextBindingRevision(previous["intent_revision"], "intent_revision")
	if err != nil {
		return nil, err
	}
	worker := filepath.Join(repository.Worktree, ".bsl-flow", "native-worktrees", id, fmt.Sprintf("a%d", authorizationRevision))
	if _, err := persistFreshActivationIntent(repository, id, request, engine, baseline, worker, authorizationRevision); err != nil {
		return nil, err
	}
	if err := runFreshActivationMeasure(repository, task, request, provisional, baseline, provider, engine); err != nil {
		return nil, err
	}
	worker, _, err = ensureWorker(repository, id, baseline, authorizationRevision)
	if err != nil {
		return nil, blocked("updated execution binding could not be prepared: %v", err)
	}

	payload := provisional
	payload["worker_path"] = worker
	payload["intent_revision"] = intentRevision
	payload["authorization_revision"] = authorizationRevision
	payload["attempts"] = previous["attempts"]
	payload["evidence"] = previous["evidence"]
	payload["events"] = previous["events"]
	// A scope update invalidates the prior acceptance.  It remains available in
	// the immutable prior revision, while the new binding starts without a
	// current acceptance receipt.
	payload["acceptances"] = []any{}
	payload["correction_rounds"] = int64(0)
	payload["repair"] = freshRepairState()
	payload["status"] = "ready"
	payload["stage"] = "inspect"
	payload["active_attempt"] = nil
	payload["unresolved_effect"] = nil
	payload["question"] = nil
	payload["blockers"] = []any{}

	card := copyMap(task.State)
	card["lifecycle"] = "controller"
	card["controller"] = payload
	card["updated_at"] = nowUTC()
	if err := validateStateForCandidate(card, id); err != nil {
		return nil, invalid("updated activation candidate is invalid: %v", err)
	}
	state, err := repository.writeRevision(task.ID, card, task.Revision)
	if err != nil {
		return nil, err
	}
	payload, ok := state["controller"].(map[string]any)
	if !ok {
		return nil, blocked("updated revision has no controller payload")
	}
	next, err := controllerNext(state, payload)
	if err != nil {
		return nil, err
	}
	return controllerEnvelope(state, payload, next), nil
}

// commandControllerRebind is the only command that clears migration's
// requires_rebind marker.  It keeps the adopted UUID and every historical
// attempt/evidence/acceptance entry, then starts a new current route at
// inspect under a newly measured provider/worktree binding.
func commandControllerRebind(project, id, expectedText, inputPath string, host *ControllerHost) (any, error) {
	if inputPath == "" {
		return nil, invalid("--input is required for native task rebind")
	}
	expected, err := strconvParseRevision(expectedText)
	if err != nil {
		return nil, err
	}
	repository, err := openReady(project)
	if err != nil {
		return nil, err
	}
	unlockGraph, err := repository.graphLock()
	if err != nil {
		return nil, conflict("dependency graph lock unavailable: %v", err)
	}
	defer unlockGraph()
	task, err := repository.ReadTask(id)
	if err != nil {
		return nil, err
	}
	if task.Lifecycle != "controller" {
		return nil, blocked("task %s must be an adopted controller task before rebind", id)
	}
	if task.Revision != expected {
		return nil, conflict("expected revision %d, actual revision %d", expected, task.Revision)
	}
	previous, err := cloneObject(asMap(task.State["controller"]))
	if err != nil {
		return nil, blocked("invalid controller state: %v", err)
	}
	if previous["active_attempt"] != nil || previous["unresolved_effect"] != nil {
		return nil, blocked("reconcile the active or uncertain attempt before rebind")
	}
	migration, ok := task.State["migration"].(map[string]any)
	if !ok || migration == nil {
		return nil, blocked("task %s has no legacy adoption binding", id)
	}
	if err := validateMigrationMetadata(migration); err != nil {
		return nil, blocked("invalid legacy adoption binding: %v", err)
	}
	if !asBoolOr(migration["requires_rebind"]) {
		return nil, conflict("task %s already has a current execution binding", id)
	}
	request, err := readInput(inputPath)
	if err != nil {
		return nil, err
	}
	if err := validateNativeActivationRequest(request, id); err != nil {
		return nil, err
	}
	if err := exactGitRoot(repository); err != nil {
		return nil, err
	}
	baseline, err := cleanBaseline(repository)
	if err != nil {
		return nil, err
	}
	provider, engine, err := resolveControllerHost(host)
	if err != nil {
		return nil, err
	}
	policyFiles, policyRules, policyHash, err := localPolicy(repository, engine)
	if err != nil {
		return nil, err
	}
	provisional, err := provisionalController(task, request, repository.Worktree, baseline, policyFiles, policyRules, policyHash, engine)
	if err != nil {
		return nil, err
	}
	authorizationRevision, err := nextBindingRevision(previous["authorization_revision"], "authorization_revision")
	if err != nil {
		return nil, err
	}
	intentRevision, err := nextBindingRevision(previous["intent_revision"], "intent_revision")
	if err != nil {
		return nil, err
	}
	worker := filepath.Join(repository.Worktree, ".bsl-flow", "native-worktrees", id, fmt.Sprintf("a%d", authorizationRevision))
	if _, err := persistFreshActivationIntent(repository, id, request, engine, baseline, worker, authorizationRevision); err != nil {
		return nil, err
	}
	if err := runFreshActivationMeasure(repository, task, request, provisional, baseline, provider, engine); err != nil {
		return nil, err
	}
	worker, _, err = ensureWorker(repository, id, baseline, authorizationRevision)
	if err != nil {
		return nil, blocked("rebound execution binding could not be prepared: %v", err)
	}

	payload := provisional
	payload["worker_path"] = worker
	payload["intent_revision"] = intentRevision
	payload["authorization_revision"] = authorizationRevision
	for _, field := range []string{"attempts", "evidence", "events", "acceptances"} {
		// These are intentionally the original slices.  They are immutable
		// history and are not silently replaced by a fresh binding.
		payload[field] = previous[field]
	}
	payload["correction_rounds"] = int64(0)
	payload["repair"] = freshRepairState()
	payload["status"] = "ready"
	payload["stage"] = "inspect"
	payload["active_attempt"] = nil
	payload["unresolved_effect"] = nil
	payload["question"] = nil
	payload["blockers"] = []any{}

	updatedMigration := copyMap(migration)
	updatedMigration["requires_rebind"] = false
	card := copyMap(task.State)
	card["lifecycle"] = "controller"
	card["controller"] = payload
	card["migration"] = updatedMigration
	card["updated_at"] = nowUTC()
	if err := validateStateForCandidate(card, id); err != nil {
		return nil, invalid("rebind candidate is invalid: %v", err)
	}
	state, err := repository.writeRevision(task.ID, card, expected)
	if err != nil {
		return nil, err
	}
	payload, ok = state["controller"].(map[string]any)
	if !ok {
		return nil, blocked("rebound revision has no controller payload")
	}
	next, err := controllerNext(state, payload)
	if err != nil {
		return nil, err
	}
	return controllerEnvelope(state, payload, next), nil
}

// runFreshActivationMeasure keeps activation, update and rebind on the same
// provider preflight path.  The provisional state deliberately uses the
// current exact project root as worker_path: no worker is created until the
// read-only source/policy/capability checks have passed.
func runFreshActivationMeasure(repository *Repository, task *Task, request, provisional map[string]any, baseline string, provider Provider, engine EngineIdentity) (resultErr error) {
	measureRoot, measureContext, measureArtifact, cancelSignal, err := temporaryFreshMeasureRoots(task.ID)
	if err != nil {
		return err
	}
	var transport *ProviderTransportEvidence
	defer func() {
		if resultErr == nil {
			removeTemporaryFreshMeasureRoot(measureRoot)
			return
		}
		// A failed measure has no attempt directory yet. Keep its isolated
		// context/artifacts available for reconciliation and retain the real
		// outer process receipt when the provider supplied one.
		transportErr := error(nil)
		if transport != nil {
			transportErr = retainNativeTransport(measureRoot, transport)
		}
		_, failureErr := writeImmutableJSON(filepath.Join(measureRoot, "failure.json"), map[string]any{
			"schema_version": int64(1),
			"task_id":        task.ID,
			"operation":      "measure",
			"error":          safeErrorMessage(resultErr.Error()),
			"retained_path":  measureRoot,
		})
		message := fmt.Sprintf("%s; retained measure evidence at %s", resultErr.Error(), measureRoot)
		if transportErr != nil {
			message += fmt.Sprintf("; transport retention failed: %v", transportErr)
		}
		if failureErr != nil {
			message += fmt.Sprintf("; failure marker retention failed: %v", failureErr)
		}
		resultErr = blocked("%s", message)
	}()
	outer := map[string]any{
		"schema_version":  int64(2),
		"task_id":         task.ID,
		"revision":        task.Revision,
		"previous_sha256": task.State["previous_sha256"],
		"created_at":      task.CreatedAt,
		"updated_at":      task.UpdatedAt,
	}
	// Capture the independent source and policy snapshots before crossing the
	// provider process boundary. Validation after Measure must prove that the
	// same bytes are still present; checking only the provider's post-measure
	// observation would accept a transient source/policy replacement.
	sourceBefore, err := sourceManifestWithBaseline(repository.Worktree, baseline, []string{"."})
	if err != nil {
		resultErr = blocked("cannot bind activation source before measure: %v", err)
		return resultErr
	}
	policyFilesBefore, err := Canonical(provisional["policy_files"])
	if err != nil {
		resultErr = blocked("cannot snapshot activation policy files: %v", err)
		return resultErr
	}
	policyRulesBefore, err := Canonical(provisional["policy_rules"])
	if err != nil {
		resultErr = blocked("cannot snapshot activation policy rules: %v", err)
		return resultErr
	}
	policyHashBefore := asStringOr(provisional["policy_hash"])
	policyRules, err := cloneObject(asMap(provisional["policy_rules"]))
	if err != nil {
		resultErr = blocked("cannot freeze activation policy rules: %v", err)
		return resultErr
	}
	input, err := providerInput(repository, outer, provisional, nil, engine, "measure", measureContext, measureArtifact, cancelSignal, nil, nil)
	if err != nil {
		resultErr = err
		return resultErr
	}
	input.StateView["worker_path"] = repository.Worktree
	_, timeoutSeconds, _, err := requestLimits(request)
	if err != nil {
		resultErr = err
		return resultErr
	}
	if err := prepareNativeCapabilityProbe(repository, task.ID); err != nil {
		resultErr = err
		return resultErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	measurement, measureErr := measureProvider(ctx, provider, input)
	if measureErr != nil {
		transport = transportFromError(measureErr)
		resultErr = blocked("provider measure failed: %v", measureErr)
		return resultErr
	}
	transport = measurement.Transport
	// Source and policy are independently re-enumerated after the provider has
	// returned. The provider observation is checked against the original
	// snapshot below, while this pair closes the host/provider TOCTOU window.
	sourceAfter, err := sourceManifestWithBaseline(repository.Worktree, baseline, []string{"."})
	if err != nil {
		resultErr = blocked("activation source changed during measure: %v", err)
		return resultErr
	}
	if !equalJSON(sourceBefore, sourceAfter) {
		resultErr = blocked("activation source changed during measure")
		return resultErr
	}
	policyFilesAfter, policyRulesAfter, policyHashAfter, err := localPolicy(repository, engine)
	if err != nil {
		resultErr = blocked("cannot re-enumerate activation policy after measure: %v", err)
		return resultErr
	}
	policyFilesAfterData, err := Canonical(policyFilesAfter)
	if err != nil {
		resultErr = blocked("cannot snapshot activation policy files after measure: %v", err)
		return resultErr
	}
	policyRulesAfterData, err := Canonical(policyRulesAfter)
	if err != nil {
		resultErr = blocked("cannot snapshot activation policy rules after measure: %v", err)
		return resultErr
	}
	if !bytes.Equal(policyFilesBefore, policyFilesAfterData) ||
		!bytes.Equal(policyRulesBefore, policyRulesAfterData) ||
		policyHashAfter != policyHashBefore {
		resultErr = blocked("activation policy changed during measure")
		return resultErr
	}
	if err := validateMeasureObservation(measurement, task.ID, request); err != nil {
		resultErr = err
		return resultErr
	}
	if err := validateMeasureBindings(repository, task.ID, baseline, request, measurement, policyRules, policyHashBefore); err != nil {
		resultErr = err
		return resultErr
	}
	expectedDependencies, err := currentNativeDependencies(provisional, "inspect", nil)
	if err != nil {
		resultErr = blocked("cannot compute fresh measure dependencies: %v", err)
		return resultErr
	}
	if !equalJSON(measurement.Dependencies, expectedDependencies) {
		resultErr = blocked("provider measure dependencies do not match the current inspect binding")
		return resultErr
	}
	if err := persistActivationMeasurement(repository, task.ID, request, provisional, baseline, engine, measurement, measureArtifact); err != nil {
		resultErr = err
		return resultErr
	}
	resultErr = nil
	return nil
}

// persistActivationMeasurement retains the successful activation preflight as
// an immutable, canonical evidence set. The directory name is a commitment to
// the exact request/policy/engine/baseline and current provisional
// authorization; the request and execution profile themselves are deliberately
// not copied into this evidence path.
func persistActivationMeasurement(repository *Repository, taskID string, request, provisional map[string]any, baseline string, engine EngineIdentity, measurement MeasureObservation, artifactRoot string) error {
	if repository == nil {
		return blocked("activation measurement repository is unavailable")
	}
	if !isUUID(taskID) {
		return invalid("task id must be a lowercase UUID")
	}
	if err := validateEngineIdentity(engine); err != nil {
		return blocked("activation measurement engine identity is invalid: %v", err)
	}
	transport := measurement.Transport
	if transport == nil {
		return blocked("activation measure has no host transport evidence")
	}
	if err := validateNativeTransportEvidence(transport); err != nil {
		return blocked("activation measure transport evidence is invalid: %v", err)
	}
	if err := validateNativeTransportSuccess(transport.Receipt); err != nil {
		return err
	}
	if err := validateNativeTransportIdentity(transport.Receipt, engine); err != nil {
		return err
	}

	observation := toMeasureObservation(measurement)
	expectedHash, err := Hash(observation)
	if err != nil {
		return blocked("activation measure observation could not be hashed: %v", err)
	}
	parsed, err := DecodeObject(transport.Stdout)
	if err != nil {
		return blocked("activation measure transport stdout is not a JSON observation: %v", err)
	}
	parsedHash, err := Hash(parsed)
	if err != nil {
		return blocked("activation measure transport stdout could not be hashed: %v", err)
	}
	if parsedHash != expectedHash {
		return blocked("activation measure transport stdout does not match the provider observation")
	}
	measurementBytes, err := Canonical(observation)
	if err != nil {
		return blocked("activation measure observation could not be canonicalized: %v", err)
	}

	bindingHash, err := activationMeasurementBindingHash(taskID, request, provisional, baseline, engine)
	if err != nil {
		return err
	}
	transportHash, err := Hash(transport.Receipt)
	if err != nil {
		return err
	}
	// A fresh recheck after an interrupted activation has a new process
	// receipt. Keep both observations instead of conflicting with the first.
	root := filepath.Join(repository.StorePath, "tasks", taskID, "activation-measurements", bindingHash, transportHash)
	if _, err := SafePath(root); err != nil {
		return blocked("activation measurement path is unsafe: %v", err)
	}
	// retainNativeTransport validates and writes the exact process receipt and
	// streams. Its immutable retry semantics make a partial directory safe to
	// complete after an interrupted activation.
	if err := retainNativeTransport(root, transport); err != nil {
		return blocked("activation measure transport could not be retained: %v", err)
	}
	if err := writeImmutableActivationMeasurement(filepath.Join(root, "measurement.json"), measurementBytes); err != nil {
		return err
	}
	if err := retainActivationProbeArtifacts(root, artifactRoot); err != nil {
		return err
	}
	return nil
}

func retainActivationProbeArtifacts(destination, source string) error {
	refs := []ArtifactRef{}
	total := int64(0)
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if _, err := SafePath(path); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return blocked("activation probe contains a non-regular file")
		}
		data, err := ReadFileBytes(path)
		if err != nil {
			return err
		}
		total += int64(len(data))
		if len(refs) >= 500 || total > 64<<20 {
			return blocked("activation probe evidence exceeds its retention bound")
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		refs = append(refs, ArtifactRef{Path: filepath.ToSlash(relative), SHA256: fileSHA256(data), SizeBytes: int64(len(data)), Kind: "raw"})
		return nil
	})
	if err != nil {
		return err
	}
	if err := persistProviderArtifacts(destination, source, refs); err != nil {
		return err
	}
	_, err = writeImmutableJSON(filepath.Join(destination, "probe-artifacts.json"), artifactMaps(refs))
	return err
}

func writeImmutableActivationMeasurement(path string, data []byte) error {
	if existing, err := ReadFileBytes(path); err == nil {
		if !bytes.Equal(existing, data) {
			return conflict("immutable activation measurement differs: %s", path)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return blocked("cannot inspect immutable activation measurement: %v", err)
	}
	if err := ensureControllerPath(path); err != nil {
		return err
	}
	if err := AtomicWrite(path, data, false); err != nil {
		return blocked("activation measurement could not be retained: %v", err)
	}
	return nil
}

func activationMeasurementBindingHash(taskID string, request, provisional map[string]any, baseline string, engine EngineIdentity) (string, error) {
	if !isUUID(taskID) {
		return "", invalid("task id must be a lowercase UUID")
	}
	if err := validateEngineIdentity(engine); err != nil {
		return "", blocked("activation measurement engine identity is invalid: %v", err)
	}
	if request == nil || provisional == nil {
		return "", blocked("activation measurement binding is incomplete")
	}
	requestHash, err := Hash(request)
	if err != nil {
		return "", blocked("activation measure request could not be hashed: %v", err)
	}
	policyHash := asStringOr(provisional["policy_hash"])
	if !isSHA256(policyHash) {
		return "", blocked("activation measure policy binding is invalid")
	}
	intentHash := asStringOr(provisional["intent_hash"])
	if !isSHA256(intentHash) {
		return "", blocked("activation measure intent binding is invalid")
	}
	intentRevision, ok := asInt(provisional["intent_revision"])
	if !ok || intentRevision < 1 {
		return "", blocked("activation measure intent revision is invalid")
	}
	authorizationRevision, ok := asInt(provisional["authorization_revision"])
	if !ok || authorizationRevision < 1 {
		return "", blocked("activation measure authorization revision is invalid")
	}
	if strings.TrimSpace(baseline) == "" {
		return "", blocked("activation measure baseline is missing")
	}

	// This commitment contains hashes and revision counters only. In
	// particular, neither request contents (which may include runtime paths) nor
	// any credential-bearing material is copied into the retained evidence.
	binding := map[string]any{
		"schema_version": int64(1),
		"task_id":        taskID,
		"request_sha256": requestHash,
		"policy_sha256":  policyHash,
		"engine":         engineMap(engine),
		"baseline":       baseline,
		"provisional_authorization": map[string]any{
			"intent_hash":            intentHash,
			"intent_revision":        intentRevision,
			"authorization_revision": authorizationRevision,
		},
	}
	return Hash(binding)
}

func persistFreshActivationIntent(repository *Repository, id string, request map[string]any, engine EngineIdentity, baseline, worker string, authorizationRevision int64) (string, error) {
	if !isUUID(id) {
		return "", invalid("task id must be a lowercase UUID")
	}
	if err := validateEngineIdentity(engine); err != nil {
		return "", err
	}
	if _, err := SafePath(worker); err != nil {
		return "", err
	}
	intent := map[string]any{
		"schema_version": int64(1),
		"task_id":        id,
		"request":        request,
		"engine":         engineMap(engine),
		"baseline":       baseline,
		"worker_path":    worker,
	}
	data, err := Canonical(intent)
	if err != nil {
		return "", err
	}
	base := filepath.Join(repository.StorePath, "tasks", id, "inputs", "activation.json")
	// Revision one is the initial activation binding. If activation reached the
	// intent boundary and then failed before publishing the ready revision, a
	// retry must compare with that exact activation.json rather than silently
	// creating activation-1.json. Later bindings use their own deterministic,
	// immutable revision name.
	path := base
	if authorizationRevision > 1 {
		path = filepath.Join(repository.StorePath, "tasks", id, "inputs", fmt.Sprintf("activation-%d.json", authorizationRevision))
	}
	if existing, readErr := ReadFileBytes(path); readErr == nil {
		if !bytes.Equal(existing, data) {
			return "", conflict("activation intent already exists with a different binding")
		}
		return path, nil
	} else if !os.IsNotExist(readErr) {
		return "", blocked("cannot inspect activation intent: %v", readErr)
	}
	if _, err := writeImmutableJSON(path, intent); err != nil {
		return "", blocked("activation intent could not be retained: %v", err)
	}
	return path, nil
}

// prepareNativeCapabilityProbe creates the six synthetic canonical-store
// fixtures consumed by the provider's read/write denial probe. The real task
// control files remain untouched; every write target is outside tasks/<id>.
func prepareNativeCapabilityProbe(repository *Repository, taskID string) error {
	if !isUUID(taskID) {
		return invalid("task id must be a lowercase UUID")
	}
	root := filepath.Join(repository.StorePath, "native-provider-probe", taskID)
	if _, err := SafePath(root); err != nil {
		return err
	}
	readJSON, err := Canonical(map[string]any{
		"schema_version": int64(1),
		"task_id":        taskID,
		"probe":          "read",
	})
	if err != nil {
		return err
	}
	for _, directory := range []string{"current", "revisions", "inputs"} {
		folder := filepath.Join(root, directory)
		if err := SafeMkdir(folder); err != nil {
			return blocked("cannot prepare canonical capability probe: %v", err)
		}
		if err := ensureCapabilityProbeFile(filepath.Join(folder, "read.json"), readJSON); err != nil {
			return err
		}
		if err := ensureCapabilityProbeFile(filepath.Join(folder, "write.txt"), []byte("probe target")); err != nil {
			return err
		}
	}
	return nil
}

func ensureCapabilityProbeFile(path string, expected []byte) error {
	if existing, err := ReadFileBytes(path); err == nil {
		// The fixture is an access sentinel. Preserve any prior evidence instead
		// of rewriting it on a retry, while SafePath/ReadFileBytes still reject
		// non-regular or reparse targets.
		_ = existing
		return nil
	} else if !os.IsNotExist(err) {
		return blocked("cannot inspect canonical capability probe fixture: %v", err)
	}
	if err := AtomicWrite(path, expected, false); err != nil {
		return blocked("cannot create canonical capability probe fixture: %v", err)
	}
	return nil
}

// temporaryFreshMeasureRoots deliberately lives outside the checkout. The
// provider sandbox treats context/artifacts as disjoint from worker_path, and
// an activation failure retains this private directory for reconciliation.
func temporaryFreshMeasureRoots(taskID string) (string, string, string, string, error) {
	if !isUUID(taskID) {
		return "", "", "", "", invalid("task id must be a lowercase UUID")
	}
	tempRoot, err := SafePath(os.TempDir())
	if err != nil {
		return "", "", "", "", blocked("unsafe temporary measure root: %v", err)
	}
	root, err := os.MkdirTemp(tempRoot, "bsl-flow-measure-"+taskID+"-")
	if err != nil {
		return "", "", "", "", blocked("cannot create temporary measure root: %v", err)
	}
	createdRoot := root
	root, err = SafePath(createdRoot)
	if err != nil || !samePath(filepath.Dir(root), tempRoot) || !strings.HasPrefix(strings.ToLower(filepath.Base(root)), "bsl-flow-measure-") {
		removeTemporaryFreshMeasureRoot(createdRoot)
		if err != nil {
			return "", "", "", "", blocked("temporary measure root is unsafe: %v", err)
		}
		return "", "", "", "", blocked("temporary measure root escaped the OS temp directory")
	}
	contextRoot := filepath.Join(root, "context")
	artifactRoot := filepath.Join(root, "artifacts")
	if err := SafeMkdir(contextRoot); err != nil {
		removeTemporaryFreshMeasureRoot(root)
		return "", "", "", "", blocked("cannot create temporary measure context: %v", err)
	}
	if err := SafeMkdir(artifactRoot); err != nil {
		removeTemporaryFreshMeasureRoot(root)
		return "", "", "", "", blocked("cannot create temporary measure artifacts: %v", err)
	}
	return root, contextRoot, artifactRoot, filepath.Join(contextRoot, "cancel.signal"), nil
}

func removeTemporaryFreshMeasureRoot(root string) {
	tempRoot, err := SafePath(os.TempDir())
	if err != nil {
		return
	}
	resolved, err := SafePath(root)
	if err != nil || !samePath(filepath.Dir(resolved), tempRoot) || !strings.HasPrefix(strings.ToLower(filepath.Base(resolved)), "bsl-flow-measure-") {
		return
	}
	_ = os.RemoveAll(resolved)
}

func nextBindingRevision(value any, name string) (int64, error) {
	current, ok := asInt(value)
	if !ok || current < 0 || current >= 999999999 {
		return 0, blocked("%s is invalid or exhausted", name)
	}
	return current + 1, nil
}

func freshRepairState() map[string]any {
	return map[string]any{
		"rounds":             int64(0),
		"pending_failure":    nil,
		"last_source_sha256": nil,
		"diagnosis_attempt":  nil,
	}
}

func copyMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func migrationRequiresRebind(state map[string]any) (bool, bool) {
	migration, ok := state["migration"].(map[string]any)
	if !ok || migration == nil {
		return false, false
	}
	rebind, ok := asBool(migration["requires_rebind"])
	if !ok {
		return false, true
	}
	return rebind, true
}

// commandControllerNativeRecovery mirrors the 'recovery' branch of
// Update-BFTask for runtime attempts: one authorized control read against
// the pending ledger, the retained receipt, and the latch completion. The
// runtime credential arrives through the private reader exactly like a
// dispatch does.
func commandControllerNativeRecovery(repository *Repository, task *Task, payload map[string]any, event map[string]any, host *ControllerHost) (any, error) {
	if _, err := nativeObject(event, []string{"kind", "input_event_id", "resolution"}, nil, "recovery_event"); err != nil {
		return nil, err
	}
	eventID := asStringOr(event["input_event_id"])
	if !isUUID(eventID) {
		return nil, invalid("recovery event identity must be a lowercase UUID")
	}
	resolutionRaw, err := nativeObject(event["resolution"], []string{"attempt_id", "scope", "source_sha256", "observation"}, []string{"target", "inventory_sha256", "retry_authorized"}, "resolution")
	if err != nil {
		return nil, err
	}
	wasCancelled := payload["status"] == "cancelled"
	var unresolvedID string
	if payload["active_attempt"] != nil {
		unresolvedID = asStringOr(payload["active_attempt"])
	} else if asMap(payload["unresolved_effect"])["attempt_id"] != nil {
		unresolvedID = asStringOr(asMap(payload["unresolved_effect"])["attempt_id"])
	}
	if unresolvedID == "" {
		return nil, conflict("no unresolved effect or interrupted attempt.")
	}
	if asStringOr(resolutionRaw["attempt_id"]) != unresolvedID {
		return nil, blocked("recovery must identify the exact unresolved attempt.")
	}
	attemptDir := filepath.Join(repository.StorePath, "tasks", task.ID, "attempts", unresolvedID)
	start, err := native1CReadJSON(filepath.Join(attemptDir, "start.json"))
	if err != nil {
		return nil, err
	}
	runtimeAttempt := asStringOr(start["stage"]) == "verify" && native1CRequestHasNative(payload)
	requiredScope := "source_only"
	if runtimeAttempt {
		requiredScope = "native_1c"
	}
	if asStringOr(resolutionRaw["scope"]) != requiredScope {
		return nil, blocked("this attempt requires %s control-read recovery.", requiredScope)
	}
	if !runtimeAttempt {
		return nil, blocked("this attempt requires source_only control-read recovery.")
	}
	if payload["active_attempt"] != nil {
		if nativeDependencyRegularFile(filepath.Join(attemptDir, "result.json")) {
			return nil, blocked("saved terminal result must be imported with Resume before recovery.")
		}
		owner := asMap(start["controller_process"])
		if owner != nil && !native1CProcessDead(asIntOr(owner["pid"]), asStringOr(owner["start_time_utc"])) {
			return nil, blocked("attempt controller is still running; cancel or wait before recovery.")
		}
	}
	artifactRoot, err := artifactDirectory(repository, task.ID, unresolvedID)
	if err != nil {
		return nil, err
	}
	for _, root := range []string{attemptDir, artifactRoot} {
		if err := native1CWalkFiles(root, func(path string) error {
			if !strings.EqualFold(filepath.Base(path), "process.json") {
				return nil
			}
			document, err := native1CReadJSON(path)
			if err != nil {
				return err
			}
			if !native1CProcessDead(asIntOr(document["pid"]), asStringOr(document["start_time_utc"])) {
				return blocked("owned child is still running; cancel or wait before recovery.")
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(asStringOr(resolutionRaw["observation"])) == "" {
		return nil, invalid("invalid resolution.observation.")
	}
	manifest, err := sourceManifestWithBaseline(asStringOr(payload["worker_path"]), asStringOr(payload["baseline"]), []string{"."})
	if err != nil {
		return nil, err
	}
	if asStringOr(resolutionRaw["source_sha256"]) != asStringOr(manifest["sha256"]) {
		return nil, conflict("recovery control-read source hash is stale.")
	}
	if host == nil || host.RuntimeAuthReader == nil {
		return nil, blocked("native credential must be supplied through private controller input for this process.")
	}
	credential, err := host.RuntimeAuthReader()
	if err != nil {
		return nil, invalid("%v", err)
	}
	runtimeRead, err := Native1CResolveRecovery(context.Background(), payload, resolutionRaw, attemptDir, manifest, credential, nativeRecoveryRuntime(host))
	if err != nil {
		return nil, err
	}
	rawHashes, err := native1CRawHashes(artifactRoot)
	if err != nil {
		return nil, err
	}
	recovery := map[string]any{
		"resolution":             resolutionRaw,
		"actual_source_manifest": manifest,
		"abandoned_attempt":      payload["active_attempt"],
		"retained_raw_hashes":    rawHashes,
		"runtime_control_read":   runtimeRead,
	}
	inputsDir := filepath.Join(repository.StorePath, "tasks", task.ID, "inputs")
	if err := SafeMkdir(inputsDir); err != nil {
		return nil, blocked("%v", err)
	}
	recoveryPath := filepath.Join(inputsDir, "recovery-"+eventID+".json")
	if nativeDependencyRegularFile(recoveryPath) {
		saved, err := native1CReadJSON(recoveryPath)
		if err != nil {
			return nil, err
		}
		savedResolutionHash, err := Hash(asMap(saved["resolution"]))
		if err != nil {
			return nil, err
		}
		resolutionHash, err := Hash(resolutionRaw)
		if err != nil {
			return nil, err
		}
		if savedResolutionHash != resolutionHash || asStringOr(asMap(saved["actual_source_manifest"])["sha256"]) != asStringOr(manifest["sha256"]) {
			return nil, conflict("conflicting recovery receipt.")
		}
	} else if err := AtomicWriteCanonical(recoveryPath, recovery); err != nil {
		return nil, err
	}
	payload["active_attempt"] = nil
	payload["unresolved_effect"] = nil
	eventHash, err := Hash(event)
	if err != nil {
		return nil, err
	}
	payload["events"] = append(anyItems(payload["events"]), map[string]any{
		"input_event_id":    eventID,
		"sha256":            eventHash,
		"kind":              "recovery",
		"accepted_revision": asIntOr(task.Revision) + 1,
	})
	payload["question"] = nil
	payload["blockers"] = []any{}
	payload["status"] = "ready"
	if wasCancelled {
		payload["status"] = "cancelled"
		payload["blockers"] = []any{"Effects reconciled. An explicit resume authorization is still required after cancellation."}
	}
	if err := AtomicWriteCanonical(filepath.Join(inputsDir, eventID+".json"), event); err != nil {
		return nil, err
	}
	state, err := appendControllerRevision(repository, task, payload, task.Revision)
	if err != nil {
		return nil, err
	}
	if _, err := Native1CCompleteRecovery(asMap(state["controller"]), resolutionRaw, nativeRecoveryRuntime(host)); err != nil {
		return nil, err
	}
	next, err := controllerNext(state, asMap(state["controller"]))
	if err != nil {
		return nil, err
	}
	return controllerEnvelope(state, asMap(state["controller"]), next), nil
}

// native1CRequestHasNative reports whether any criterion of the request
// carries a native 1C runtime contract.
func native1CRequestHasNative(payload map[string]any) bool {
	request := asMap(payload["request"])
	for _, raw := range anyItems(request["criteria"]) {
		criterion, ok := raw.(map[string]any)
		if ok && criterion != nil && criterion["native_1c"] != nil {
			return true
		}
	}
	return false
}
