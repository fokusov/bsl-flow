package repository

// This file contains the deliberately small, file-system-only legacy
// adoption boundary.  Adoption is a migration of an already verified v1
// journal; it does not run the controller, a provider, Git publication, or
// any external runtime.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const adoptionContract = "bsl-flow.legacy-adoption.v1"

var adoptionPlanFields = map[string]bool{
	"schema_version": true, "contract": true, "task_id": true,
	"source_root": true, "common_dir": true, "repository_id": true,
	"prefix_revision": true, "prefix_semantic_sha256": true, "sources": true,
	"manifest": true, "dependencies": true, "eligibility": true,
	"blockers": true, "binding_sha256": true,
}

var adoptionSourceFields = map[string]bool{
	"root": true, "prefix_semantic_sha256": true, "raw_manifest_sha256": true,
	"artifact_manifest_sha256": true,
}

var adoptionManifestFields = map[string]bool{
	"revisions": true, "artifacts": true, "external_refs": true, "excluded": true,
}

var adoptionRevisionEntryFields = map[string]bool{
	"source_rel": true, "canonical_rel": true, "size_bytes": true,
	"raw_sha256": true, "semantic_sha256": true,
}

var adoptionArtifactEntryFields = map[string]bool{
	"source_rel": true, "canonical_rel": true, "size_bytes": true,
	"raw_sha256": true,
}

var adoptionPathEntryFields = map[string]bool{"path": true, "reason": true}

var adoptionDependencyFields = map[string]bool{
	"baseline": true, "request_sha256": true, "intent_sha256": true,
	"policy_sha256": true, "execution_profile_sha256": true,
}

var adoptionEligibilityFields = map[string]bool{"eligible": true, "checks": true}

var adoptionEligibilityCheckFields = map[string]bool{
	"source_owner": true, "journal": true, "siblings": true, "inactive": true,
	"processes_terminal": true, "publications_clear": true,
	"native_targets_clear": true, "artifacts_complete": true,
	"target_available": true,
}

var adoptionReceiptFields = map[string]bool{
	"schema_version": true, "task_id": true, "plan_sha256": true,
	"manifest_sha256": true, "repository_id": true, "prefix_revision": true,
	"prefix_semantic_sha256": true, "continuation_revision": true,
	"committed_at": true,
}

// validateMigrationMetadata validates the only optional field that can make a
// v2 revision part of a mixed legacy/native chain.  The field is intentionally
// scalar and closed: the receipt and manifest themselves live beside the
// journal and are validated by validateAdoptionChain.
func validateMigrationMetadata(value any) error {
	metadata, ok := value.(map[string]any)
	if !ok || metadata == nil {
		return errors.New("migration must be an object")
	}
	if len(metadata) != 4 {
		return errors.New("migration contains unsupported or missing fields")
	}
	for field := range metadata {
		switch field {
		case "receipt_sha256", "manifest_sha256", "prefix_revision", "requires_rebind":
		default:
			return fmt.Errorf("migration contains unsupported field %s", field)
		}
	}
	for _, field := range []string{"receipt_sha256", "manifest_sha256"} {
		value, ok := asString(metadata[field])
		if !ok || !isSHA256(value) {
			return fmt.Errorf("migration.%s must be a lowercase SHA-256", field)
		}
	}
	revision, ok := asInt(metadata["prefix_revision"])
	if !ok || revision < 1 {
		return errors.New("migration.prefix_revision must be a positive integer")
	}
	if _, ok := asBool(metadata["requires_rebind"]); !ok {
		return errors.New("migration.requires_rebind must be a boolean")
	}
	return nil
}

// validateAdoptionChain is called after the ordinary outer journal reader has
// verified filenames, UUIDs and previous_sha256 links.  Pure v1 and ordinary
// v2 journals need no migration documents.  A mixed journal, however, must
// prove the complete immutable migration binding before it can be read as a
// canonical task.
func (r *Repository) validateAdoptionChain(directory string, chain []map[string]any) error {
	if len(chain) == 0 {
		return nil
	}
	firstV2 := -1
	for index, state := range chain {
		version, ok := asInt(state["schema_version"])
		if !ok {
			return errors.New("migration chain has a revision without schema_version")
		}
		switch version {
		case 1:
			if firstV2 >= 0 {
				return fmt.Errorf("v1 revision appears after the v2 migration transition at revision %d", index+1)
			}
		case 2:
			if firstV2 < 0 {
				firstV2 = index
			}
		default:
			return fmt.Errorf("unsupported schema version %d in migration chain", version)
		}
	}
	if firstV2 < 0 {
		return nil
	}
	// An all-v2 journal without migration metadata is the ordinary native
	// journal.  Migration metadata on an all-v2 journal is never meaningful:
	// there is no verified v1 prefix to which it can refer.
	if firstV2 == 0 {
		for _, state := range chain {
			if _, present := state["migration"]; present {
				return errors.New("migration metadata requires a v1 prefix")
			}
		}
		return nil
	}
	prefix := chain[:firstV2]
	if len(prefix) == 0 {
		return errors.New("migration prefix is empty")
	}
	prefixRevision := int64(len(prefix))
	prefixHash, err := Hash(prefix[len(prefix)-1])
	if err != nil {
		return fmt.Errorf("cannot hash v1 migration prefix: %w", err)
	}
	first := chain[firstV2]
	if revision, ok := asInt(first["revision"]); !ok || revision != prefixRevision+1 {
		return errors.New("migration transition revision is not immediately after the v1 prefix")
	}
	if linked, ok := asString(first["previous_sha256"]); !ok || linked != prefixHash {
		return errors.New("migration transition does not link the v1 prefix")
	}
	firstMetadata, present := first["migration"]
	if !present {
		return errors.New("mixed chain transition is missing migration metadata")
	}
	if err := validateMigrationMetadata(firstMetadata); err != nil {
		return err
	}
	firstMigration := firstMetadata.(map[string]any)
	firstReceipt := asStringOr(firstMigration["receipt_sha256"])
	firstManifest := asStringOr(firstMigration["manifest_sha256"])
	firstPrefix := asIntOr(firstMigration["prefix_revision"])
	firstRebind := asBoolOr(firstMigration["requires_rebind"])
	if firstPrefix != prefixRevision || !firstRebind {
		return errors.New("initial migration must reference the complete v1 prefix and require rebind")
	}
	requiresRebind := firstRebind
	for index := firstV2; index < len(chain); index++ {
		state := chain[index]
		version, _ := asInt(state["schema_version"])
		if version != 2 {
			return errors.New("mixed chain contains a non-v2 continuation")
		}
		metadataValue, present := state["migration"]
		if !present {
			return fmt.Errorf("v2 continuation revision %d is missing migration metadata", index+1)
		}
		if err := validateMigrationMetadata(metadataValue); err != nil {
			return err
		}
		metadata := metadataValue.(map[string]any)
		if asStringOr(metadata["receipt_sha256"]) != firstReceipt ||
			asStringOr(metadata["manifest_sha256"]) != firstManifest ||
			asIntOr(metadata["prefix_revision"]) != firstPrefix {
			return fmt.Errorf("migration references changed at revision %d", index+1)
		}
		// Once a trusted rebind has made the task current, later revisions may
		// not silently return it to the historical state.
		currentRequiresRebind := asBoolOr(metadata["requires_rebind"])
		if !requiresRebind && currentRequiresRebind {
			return fmt.Errorf("migration requires_rebind reverted at revision %d", index+1)
		}
		requiresRebind = currentRequiresRebind
	}
	return r.validateMigrationDocuments(directory, chain, firstV2, firstReceipt, firstManifest, prefixRevision, prefixHash)
}

func (r *Repository) validateMigrationDocuments(directory string, chain []map[string]any, firstV2 int, receiptHash, manifestHash string, prefixRevision int64, prefixHash string) error {
	safeDirectory, err := SafePath(directory)
	if err != nil {
		return err
	}
	planBytes, err := ReadFileBytes(filepath.Join(safeDirectory, "adoption-plan.json"))
	if err != nil {
		return blocked("adoption plan is unavailable: %v", err)
	}
	plan, err := decodeStrictObject(planBytes)
	if err != nil {
		return blocked("adoption plan is invalid: %v", err)
	}
	if err := validateAdoptionPlanShape(plan); err != nil {
		return blocked("adoption plan is invalid: %v", err)
	}
	canonicalPlan, err := Canonical(plan)
	if err != nil || !bytes.Equal(canonicalPlan, bytes.TrimSpace(planBytes)) {
		return blocked("adoption plan bytes are not canonical")
	}
	manifestBytes, err := ReadFileBytes(filepath.Join(safeDirectory, "legacy-artifact-manifest.json"))
	if err != nil {
		return blocked("legacy artifact manifest is unavailable: %v", err)
	}
	manifest, err := decodeStrictObject(manifestBytes)
	if err != nil {
		return blocked("legacy artifact manifest is invalid: %v", err)
	}
	if err := validateAdoptionManifestShape(manifest); err != nil {
		return blocked("legacy artifact manifest is invalid: %v", err)
	}
	canonicalManifest, err := Canonical(manifest)
	if err != nil || !bytes.Equal(canonicalManifest, bytes.TrimSpace(manifestBytes)) {
		return blocked("legacy artifact manifest bytes are not canonical")
	}
	receiptBytes, err := ReadFileBytes(filepath.Join(safeDirectory, "adoption-receipt.json"))
	if err != nil {
		return blocked("adoption receipt is unavailable: %v", err)
	}
	receipt, err := decodeStrictObject(receiptBytes)
	if err != nil {
		return blocked("adoption receipt is invalid: %v", err)
	}
	if err := validateAdoptionReceiptShape(receipt); err != nil {
		return blocked("adoption receipt is invalid: %v", err)
	}
	canonicalReceipt, err := Canonical(receipt)
	if err != nil || !bytes.Equal(canonicalReceipt, bytes.TrimSpace(receiptBytes)) {
		return blocked("adoption receipt bytes are not canonical")
	}
	if sha256Sum(canonicalReceipt) != receiptHash {
		return blocked("migration receipt hash does not match adoption-receipt.json")
	}
	if sha256Sum(canonicalManifest) != manifestHash {
		return blocked("migration manifest hash does not match legacy-artifact-manifest.json")
	}
	if sha256Sum(canonicalPlan) != asStringOr(receipt["plan_sha256"]) {
		return blocked("adoption receipt does not bind the approved plan bytes")
	}
	if asStringOr(receipt["manifest_sha256"]) != sha256Sum(canonicalManifest) {
		return blocked("adoption receipt does not bind the manifest bytes")
	}
	if asStringOr(receipt["task_id"]) != asStringOr(chain[0]["task_id"]) {
		return blocked("adoption receipt task_id differs from the journal")
	}
	if asIntOr(receipt["prefix_revision"]) != prefixRevision || asStringOr(receipt["prefix_semantic_sha256"]) != prefixHash {
		return blocked("adoption receipt does not bind the v1 prefix")
	}
	if asIntOr(receipt["continuation_revision"]) != int64(firstV2+1) {
		return blocked("adoption receipt continuation revision is invalid")
	}
	if err := validateAdoptionPlanBinding(plan, asStringOr(receipt["task_id"]), prefixRevision, prefixHash, manifest); err != nil {
		return blocked("adoption plan binding is invalid: %v", err)
	}
	if asStringOr(receipt["repository_id"]) == "" {
		return blocked("adoption receipt repository_id is missing")
	}
	if !isUUID(asStringOr(receipt["repository_id"])) {
		return blocked("adoption receipt repository_id is invalid")
	}
	if identity, identityErr := ReadFileBytes(filepath.Join(r.StorePath, "repository.json")); identityErr != nil {
		return blocked("canonical repository identity is unavailable: %v", identityErr)
	} else if object, decodeErr := decodeStrictObject(identity); decodeErr != nil || asStringOr(object["clone_id"]) != asStringOr(receipt["repository_id"]) {
		return blocked("adoption receipt repository_id differs from the canonical repository")
	}
	if err := validateMigratedPrefixBytes(safeDirectory, manifest, chain[:firstV2]); err != nil {
		return err
	}
	if err := validateMigratedArtifactBytes(safeDirectory, manifest); err != nil {
		return err
	}
	return nil
}

func validateMigratedPrefixBytes(directory string, manifest map[string]any, chain []map[string]any) error {
	entries := anyItems(manifest["revisions"])
	if len(entries) != len(chain) {
		return blocked("legacy revision manifest count differs from the v1 prefix")
	}
	for index, raw := range entries {
		entry := raw.(map[string]any)
		relative := asStringOr(entry["canonical_rel"])
		data, err := ReadFileBytes(filepath.Join(directory, filepath.FromSlash(relative)))
		if err != nil {
			return blocked("copied legacy revision is unavailable: %v", err)
		}
		if sha256Sum(data) != asStringOr(entry["raw_sha256"]) {
			return blocked("copied legacy revision bytes differ at %s", relative)
		}
		semantic, err := Hash(chain[index])
		if err != nil || semantic != asStringOr(entry["semantic_sha256"]) {
			return blocked("copied legacy revision semantic hash differs at %s", relative)
		}
	}
	return nil
}

func validateMigratedArtifactBytes(directory string, manifest map[string]any) error {
	for _, raw := range anyItems(manifest["artifacts"]) {
		entry := raw.(map[string]any)
		relative := asStringOr(entry["canonical_rel"])
		data, err := ReadFileBytes(filepath.Join(directory, filepath.FromSlash(relative)))
		if err != nil {
			return blocked("copied legacy artifact is unavailable: %v", err)
		}
		if int64(len(data)) != asIntOr(entry["size_bytes"]) || sha256Sum(data) != asStringOr(entry["raw_sha256"]) {
			return blocked("copied legacy artifact bytes differ at %s", relative)
		}
	}
	return nil
}

// commandAdopt is intentionally independent from the native provider.  It
// creates a plan from the exact source worktree during preview and accepts
// only that plan during apply.  A successful apply creates a single mixed
// journal with the original v1 bytes followed by one historical continuation.
func commandAdopt(project, id, source, inputPath string, preview, apply bool) (any, error) {
	if !isUUID(id) {
		return nil, invalid("task id must be a lowercase UUID")
	}
	if preview == apply {
		return nil, invalid("adopt requires exactly one of --preview or --apply")
	}
	if strings.TrimSpace(source) == "" {
		return nil, invalid("--source is required")
	}
	if preview && inputPath != "" {
		return nil, invalid("--input is only valid with --apply")
	}
	if apply && strings.TrimSpace(inputPath) == "" {
		return nil, invalid("--input is required with --apply")
	}
	repository, err := OpenRepository(project)
	if err != nil {
		return nil, err
	}
	if preview {
		if err := repository.LoadIdentity(); err != nil {
			return nil, err
		}
		plan, err := buildAdoptionPlan(repository, id, source)
		if err != nil {
			return nil, err
		}
		return plan, nil
	}
	if err := repository.LoadIdentity(); err != nil {
		return nil, err
	}
	planBytes, err := ReadFileBytes(inputPath)
	if err != nil {
		return nil, invalid("cannot read adoption plan %s: %v", inputPath, err)
	}
	plan, err := decodeStrictObject(bytes.TrimSpace(planBytes))
	if err != nil {
		return nil, invalid("invalid adoption plan: %v", err)
	}
	if err := validateAdoptionPlanShape(plan); err != nil {
		return nil, invalid("invalid adoption plan: %v", err)
	}
	if err := validatePlanIdentity(repository, plan, id, source); err != nil {
		return nil, err
	}
	if plannedRepositoryID := asStringOr(plan["repository_id"]); plannedRepositoryID != "" {
		actualRepositoryID := asStringOr(adoptionRepositoryID(repository))
		if actualRepositoryID != plannedRepositoryID {
			return nil, conflict("adoption plan repository_id differs from the current repository")
		}
	}
	// A committed repeat is allowed to return the prior result after the
	// selected source has been removed.  If the source is still present, the
	// normal path below rechecks it (including all saved sibling bindings).
	target, targetErr := repository.taskDir(id)
	if targetErr != nil {
		return nil, targetErr
	}
	legacyRoot := filepath.Join(asStringOr(plan["source_root"]), ".bsl-flow", "tasks", id)
	if _, sourceErr := os.Lstat(legacyRoot); os.IsNotExist(sourceErr) {
		if targetInfo, statErr := os.Lstat(target); statErr == nil && targetInfo.IsDir() {
			if err := repository.validateCommittedAdoption(target, plan); err == nil {
				return adoptionResult(plan, id, asIntOr(plan["prefix_revision"])+1, true, target), nil
			}
		}
	}
	// A read-only preview may legitimately have no repository.json.  Keep its
	// null repository_id in the binding comparison, then create the canonical
	// identity only once all source checks and locks are in place.
	planRepositoryID := plan["repository_id"]
	if err := validatePlanSourceNow(repository, plan, id, source); err != nil {
		return nil, err
	}
	if err := ensureAdoptionInactive(repository, plan, id, source); err != nil {
		return nil, err
	}
	unlockGraph, err := repository.graphLock()
	if err != nil {
		return nil, conflict("dependency graph lock unavailable: %v", err)
	}
	defer unlockGraph()
	// Recompute under the graph lock before taking source locks.  A source
	// mutation after preview is a conflict, never a winner-selection event.
	currentPlan, err := buildAdoptionPlan(repository, id, source)
	if err != nil {
		return nil, err
	}
	if err := compareAdoptionPlans(plan, currentPlan, true); err != nil {
		return nil, err
	}
	copyRoots := planSourceRoots(plan)
	locks := []func(){}
	defer func() {
		for index := len(locks) - 1; index >= 0; index-- {
			locks[index]()
		}
	}()
	for _, root := range copyRoots {
		taskRoot := filepath.Join(root, ".bsl-flow", "tasks", id)
		if info, statErr := os.Lstat(taskRoot); statErr != nil || !info.IsDir() {
			continue
		}
		unlock, lockErr := Lock(filepath.Join(taskRoot, ".writer.lock"))
		if lockErr != nil {
			return nil, conflict("legacy task journal is locked: %v", lockErr)
		}
		locks = append(locks, unlock)
	}
	underLockPlan, err := buildAdoptionPlan(repository, id, source)
	if err != nil {
		return nil, err
	}
	if err := compareAdoptionPlans(plan, underLockPlan, true); err != nil {
		return nil, err
	}
	if err := validatePlanSourceNow(repository, plan, id, source); err != nil {
		return nil, err
	}
	if err := ensureAdoptionInactive(repository, plan, id, source); err != nil {
		return nil, err
	}
	if err := repository.ensureIdentityForAdoption(planRepositoryID); err != nil {
		return nil, err
	}
	// The target check is deliberately after both graph and legacy locks.  No
	// creator can claim the UUID while this operation is publishing.
	if existing, existsErr := os.Lstat(target); existsErr == nil {
		if !existing.IsDir() {
			return nil, conflict("canonical task target is not a directory")
		}
		if err := repository.validateCommittedAdoption(target, plan); err == nil {
			return adoptionResult(plan, id, int64(asIntOr(plan["prefix_revision"])+1), true, target), nil
		} else {
			return nil, conflict("canonical task target already exists with a different or incomplete adoption: %v", err)
		}
	} else if !os.IsNotExist(existsErr) {
		return nil, blocked("cannot inspect canonical task target: %v", existsErr)
	}
	if err := publishAdoption(repository, target, source, id, plan); err != nil {
		return nil, err
	}
	return adoptionResult(plan, id, int64(asIntOr(plan["prefix_revision"])+1), false, target), nil
}

func buildAdoptionPlan(repository *Repository, id, source string) (map[string]any, error) {
	sourceRoot, err := SafePath(source)
	if err != nil {
		return nil, invalid("invalid source worktree: %v", err)
	}
	sourceRepository, err := OpenRepository(sourceRoot)
	if err != nil {
		return nil, err
	}
	if !samePath(sourceRepository.CommonDir, repository.CommonDir) {
		return nil, conflict("source and project belong to different Git common dirs")
	}
	legacyRoot := filepath.Join(sourceRoot, ".bsl-flow", "tasks", id)
	if err := ensureDirectory(legacyRoot, "legacy task journal"); err != nil {
		return nil, err
	}
	chain, err := repository.readChain(legacyRoot)
	if err != nil {
		return nil, err
	}
	if len(chain) == 0 {
		return nil, blocked("legacy task journal has no revisions")
	}
	for index, state := range chain {
		version, ok := asInt(state["schema_version"])
		if !ok || version != 1 {
			return nil, blocked("legacy source must contain only v1 revisions")
		}
		if taskID := asStringOr(state["task_id"]); taskID != id {
			return nil, blocked("legacy revision %d has a different task_id", index+1)
		}
		projectPath := asStringOr(state["project_path"])
		if !samePath(projectPath, sourceRoot) {
			return nil, blocked("legacy project_path does not match the exact source worktree")
		}
	}
	prefixRevision := int64(len(chain))
	prefixHash, err := Hash(chain[len(chain)-1])
	if err != nil {
		return nil, blocked("cannot hash legacy prefix: %v", err)
	}
	manifest, err := buildLegacyManifest(legacyRoot, id, chain)
	if err != nil {
		return nil, err
	}
	sources, sourceBlockers := discoverAdoptionSources(repository, id, sourceRoot, prefixHash, manifest)
	dependencies := adoptionDependencies(chain[len(chain)-1])
	checks := map[string]any{
		"source_owner":         len(sourceBlockers) == 0,
		"journal":              true,
		"siblings":             len(sourceBlockers) == 0,
		"inactive":             adoptionStateInactive(chain[len(chain)-1]),
		"processes_terminal":   adoptionProcessesTerminal(legacyRoot, chain[len(chain)-1]),
		"publications_clear":   adoptionPublicationsClear(legacyRoot),
		"native_targets_clear": adoptionNativeTargetsClear(legacyRoot),
		"artifacts_complete":   adoptionArtifactsComplete(manifest),
		"target_available":     adoptionTargetAvailable(repository, id, prefixHash, manifest),
	}
	blockers := append([]string{}, sourceBlockers...)
	if !asBoolOr(checks["inactive"]) {
		blockers = append(blockers, "BF_BLOCKED: legacy task has an active attempt, unresolved effect, or running status")
	}
	if !asBoolOr(checks["processes_terminal"]) {
		blockers = append(blockers, "BF_BLOCKED: retained legacy process state is live or ambiguous")
	}
	if !asBoolOr(checks["publications_clear"]) {
		blockers = append(blockers, "BF_BLOCKED: legacy publication has an unresolved intent or pending marker")
	}
	if !asBoolOr(checks["native_targets_clear"]) {
		blockers = append(blockers, "BF_BLOCKED: legacy native target has an unresolved pending marker")
	}
	if !asBoolOr(checks["artifacts_complete"]) {
		blockers = append(blockers, "BF_BLOCKED: required legacy task artifact is missing, unsafe, or excluded")
	}
	if !asBoolOr(checks["target_available"]) {
		blockers = append(blockers, "BF_CONFLICT: canonical task target is occupied by a different or incomplete task")
	}
	blockers = uniqueStrings(blockers)
	eligible := true
	for _, raw := range adoptionEligibilityCheckFields {
		_ = raw
	}
	for _, name := range []string{"source_owner", "journal", "siblings", "inactive", "processes_terminal", "publications_clear", "native_targets_clear", "artifacts_complete", "target_available"} {
		if !asBoolOr(checks[name]) {
			eligible = false
		}
	}
	plan := map[string]any{
		"schema_version":         int64(1),
		"contract":               adoptionContract,
		"task_id":                id,
		"source_root":            sourceRoot,
		"common_dir":             repository.CommonDir,
		"repository_id":          adoptionRepositoryID(repository),
		"prefix_revision":        prefixRevision,
		"prefix_semantic_sha256": prefixHash,
		"sources":                sources,
		"manifest":               manifest,
		"dependencies":           dependencies,
		"eligibility":            map[string]any{"eligible": eligible, "checks": checks},
		"blockers":               blockers,
		"binding_sha256":         "",
	}
	withoutBinding := cloneMap(plan)
	delete(withoutBinding, "binding_sha256")
	binding, err := Hash(withoutBinding)
	if err != nil {
		return nil, err
	}
	plan["binding_sha256"] = binding
	return plan, nil
}

func adoptionRepositoryID(repository *Repository) any {
	data, err := ReadFileBytes(filepath.Join(repository.StorePath, "repository.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return nil
	}
	object, err := decodeStrictObject(data)
	if err != nil || !isUUID(asStringOr(object["clone_id"])) {
		return nil
	}
	return asStringOr(object["clone_id"])
}

func discoverAdoptionSources(repository *Repository, id, selectedRoot, prefixHash string, selectedManifest map[string]any) ([]any, []string) {
	roots := repository.Worktrees()
	roots = append(roots, selectedRoot)
	seen := map[string]bool{}
	orderedRoots := []string{}
	for _, root := range roots {
		resolved, err := SafePath(root)
		if err != nil {
			continue
		}
		key := strings.ToLower(filepath.Clean(resolved))
		if !seen[key] {
			seen[key] = true
			orderedRoots = append(orderedRoots, resolved)
		}
	}
	sort.Slice(orderedRoots, func(i, j int) bool { return strings.ToLower(orderedRoots[i]) < strings.ToLower(orderedRoots[j]) })
	selectedManifestHash, _ := Hash(selectedManifest["artifacts"])
	selectedRevisionHash, _ := Hash(selectedManifest["revisions"])
	entries := []any{}
	blockers := []string{}
	for _, root := range orderedRoots {
		taskRoot := filepath.Join(root, ".bsl-flow", "tasks", id)
		info, statErr := os.Lstat(taskRoot)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil || !info.IsDir() {
			blockers = append(blockers, "BF_BLOCKED: same-ID legacy copy is unreadable")
			continue
		}
		chain, chainErr := repository.readChain(taskRoot)
		if chainErr != nil || len(chain) == 0 {
			blockers = append(blockers, "BF_CONFLICT: same-ID legacy copy has a corrupt or empty journal")
			continue
		}
		for _, state := range chain {
			if version, _ := asInt(state["schema_version"]); version != 1 {
				blockers = append(blockers, "BF_CONFLICT: same-ID legacy copy is not a v1 history")
				continue
			}
		}
		semantic, hashErr := Hash(chain[len(chain)-1])
		if hashErr != nil {
			blockers = append(blockers, "BF_BLOCKED: same-ID legacy copy cannot be hashed")
			continue
		}
		manifest, manifestErr := buildLegacyManifest(taskRoot, id, chain)
		if manifestErr != nil {
			blockers = append(blockers, "BF_CONFLICT: same-ID legacy copy artifact manifest differs or is invalid")
			continue
		}
		revisionHash, _ := Hash(manifest["revisions"])
		artifactHash, _ := Hash(manifest["artifacts"])
		if semantic != prefixHash || revisionHash != selectedRevisionHash || artifactHash != selectedManifestHash {
			blockers = append(blockers, "BF_CONFLICT: same-ID legacy copies have divergent semantic or artifact history")
		}
		entries = append(entries, map[string]any{
			"root":                     root,
			"prefix_semantic_sha256":   semantic,
			"raw_manifest_sha256":      revisionHash,
			"artifact_manifest_sha256": artifactHash,
		})
	}
	if len(entries) == 0 {
		entries = append(entries, map[string]any{"root": selectedRoot, "prefix_semantic_sha256": prefixHash, "raw_manifest_sha256": selectedRevisionHash, "artifact_manifest_sha256": selectedManifestHash})
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(asStringOr(entries[i].(map[string]any)["root"])) < strings.ToLower(asStringOr(entries[j].(map[string]any)["root"]))
	})
	return entries, uniqueStrings(blockers)
}

func adoptionDependencies(state map[string]any) map[string]any {
	dependencies := map[string]any{
		"baseline":                 state["baseline"],
		"request_sha256":           state["request_hash"],
		"intent_sha256":            state["intent_hash"],
		"policy_sha256":            state["policy_hash"],
		"execution_profile_sha256": nil,
	}
	if request, ok := state["request"].(map[string]any); ok {
		if profile, present := request["execution_profile"]; present {
			if hash, err := Hash(profile); err == nil {
				dependencies["execution_profile_sha256"] = hash
			}
		}
	}
	return dependencies
}

func adoptionStateInactive(state map[string]any) bool {
	status := asStringOr(state["status"])
	return status != "running" && state["active_attempt"] == nil && state["unresolved_effect"] == nil
}

func adoptionProcessesTerminal(taskRoot string, state map[string]any) bool {
	if !adoptionStateInactive(state) {
		return false
	}
	// A terminal v1 task can still contain a retained process marker.  Only
	// explicit terminal marker files are accepted; missing/unknown markers are
	// treated as a blocker instead of guessed quiescence.
	processes := []string{}
	_ = filepath.WalkDir(taskRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		name := strings.ToLower(entry.Name())
		if name == "process.json" || name == "exit.json" {
			processes = append(processes, path)
		}
		return nil
	})
	for _, path := range processes {
		if strings.EqualFold(filepath.Base(path), "exit.json") {
			continue
		}
		// process.json without a sibling terminal receipt is ambiguous.
		directory := filepath.Dir(path)
		if _, err := os.Stat(filepath.Join(directory, "exit.json")); err != nil {
			return false
		}
	}
	return true
}

func adoptionPublicationsClear(taskRoot string) bool {
	publicationRoot := filepath.Join(taskRoot, "publications")
	info, err := os.Stat(publicationRoot)
	if os.IsNotExist(err) {
		return true
	}
	if err != nil || !info.IsDir() {
		return false
	}
	clear := true
	_ = filepath.WalkDir(publicationRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			clear = false
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(entry.Name(), "intent.json") {
			return nil
		}
		directory := filepath.Dir(path)
		if _, publishedErr := os.Stat(filepath.Join(directory, "published.json")); publishedErr != nil {
			clear = false
		}
		return nil
	})
	return clear
}

func adoptionNativeTargetsClear(taskRoot string) bool {
	clear := true
	runtimeRoot := filepath.Join(taskRoot, "runtime")
	if _, err := os.Lstat(runtimeRoot); os.IsNotExist(err) {
		return true
	} else if err != nil {
		return false
	}
	_ = filepath.WalkDir(runtimeRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			clear = false
			return walkErr
		}
		if !entry.IsDir() && strings.EqualFold(entry.Name(), "pending.json") {
			clear = false
		}
		return nil
	})
	return clear
}

func adoptionArtifactsComplete(manifest map[string]any) bool {
	for _, raw := range anyItems(manifest["excluded"]) {
		entry, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		switch asStringOr(entry["reason"]) {
		case "derived-current-projection", "writer-lock", "temporary-or-unfinished-file", "credential-or-auth-asset":
			continue
		default:
			// A reparse point, directory where a file was expected, or another
			// non-portable entry makes the claimed evidence incomplete.
			return false
		}
	}
	return true
}

func adoptionTargetAvailable(repository *Repository, id, prefixHash string, manifest map[string]any) bool {
	target, err := repository.taskDir(id)
	if err != nil {
		return false
	}
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return true
	}
	if err != nil || !info.IsDir() {
		return false
	}
	// The full receipt binding is checked by validateCommittedAdoption during
	// apply.  For preview, an existing target is available only when it is an
	// already committed exact adoption.
	chain, err := repository.readChain(target)
	if err != nil || len(chain) == 0 {
		return false
	}
	if len(chain) < 2 {
		return false
	}
	latestPrefix, err := Hash(chain[0])
	if err != nil || latestPrefix != prefixHash {
		return false
	}
	return repository.validateCommittedAdoption(target, map[string]any{"manifest": manifest}) == nil
}

func ensureAdoptionInactive(repository *Repository, plan map[string]any, id, source string) error {
	eligibility, _ := plan["eligibility"].(map[string]any)
	checks, _ := eligibility["checks"].(map[string]any)
	for _, key := range []string{"inactive", "processes_terminal", "publications_clear", "native_targets_clear", "artifacts_complete"} {
		if !asBoolOr(checks[key]) {
			return blocked("adoption eligibility check %s failed", key)
		}
	}
	return nil
}

func validatePlanIdentity(repository *Repository, plan map[string]any, id, source string) error {
	if asStringOr(plan["task_id"]) != id {
		return invalid("adoption plan task_id differs from --task")
	}
	if asStringOr(plan["contract"]) != adoptionContract {
		return invalid("unsupported adoption plan contract")
	}
	root, err := SafePath(source)
	if err != nil || !samePath(root, asStringOr(plan["source_root"])) {
		return conflict("adoption plan source_root differs from --source")
	}
	if !samePath(repository.CommonDir, asStringOr(plan["common_dir"])) {
		return conflict("adoption plan common_dir differs from the current repository")
	}
	return nil
}

func validatePlanSourceNow(repository *Repository, plan map[string]any, id, source string) error {
	current, err := buildAdoptionPlan(repository, id, source)
	if err != nil {
		return err
	}
	return compareAdoptionPlans(plan, current, true)
}

func compareAdoptionPlans(saved, current map[string]any, allowMissingRepositoryID bool) error {
	savedCopy := cloneMap(saved)
	currentCopy := cloneMap(current)
	if allowMissingRepositoryID && savedCopy["repository_id"] == nil {
		currentCopy["repository_id"] = nil
	}
	if err := compareJSONWithout(savedCopy, currentCopy, "eligibility", "blockers", "binding_sha256"); err != nil {
		return conflict("adoption source changed since preview: %v", err)
	}
	if asStringOr(saved["binding_sha256"]) == "" {
		return conflict("adoption plan has no binding hash")
	}
	return nil
}

func compareJSONWithout(left, right map[string]any, ignored ...string) error {
	ignore := map[string]bool{}
	for _, key := range ignored {
		ignore[key] = true
	}
	leftCopy, rightCopy := cloneMap(left), cloneMap(right)
	for key := range ignore {
		delete(leftCopy, key)
		delete(rightCopy, key)
	}
	leftBytes, err := Canonical(leftCopy)
	if err != nil {
		return err
	}
	rightBytes, err := Canonical(rightCopy)
	if err != nil {
		return err
	}
	if !bytes.Equal(leftBytes, rightBytes) {
		return errors.New("immutable plan fields differ")
	}
	return nil
}

func planSourceRoots(plan map[string]any) []string {
	roots := []string{}
	for _, raw := range anyItems(plan["sources"]) {
		if root := asStringOr(raw.(map[string]any)["root"]); root != "" {
			roots = append(roots, root)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return strings.ToLower(roots[i]) < strings.ToLower(roots[j]) })
	return roots
}

func publishAdoption(repository *Repository, target, source, id string, plan map[string]any) error {
	planBytes, err := Canonical(plan)
	if err != nil {
		return err
	}
	legacyRoot := filepath.Join(asStringOr(plan["source_root"]), ".bsl-flow", "tasks", id)
	chain, err := repository.readChain(legacyRoot)
	if err != nil || len(chain) == 0 {
		return blocked("legacy source changed before staging")
	}
	manifest := plan["manifest"].(map[string]any)
	manifestBytes, err := Canonical(manifest)
	if err != nil {
		return err
	}
	operation, err := randomHex(16)
	if err != nil {
		return err
	}
	staging := filepath.Join(repository.StorePath, "adoption", asStringOr(plan["binding_sha256"]), "staging", operation, "tasks", id)
	if err := SafeMkdir(filepath.Dir(staging)); err != nil {
		return blocked("cannot create adoption staging: %v", err)
	}
	// A staging path is private and can be discarded only when it is known to
	// be ours.  Never use a shared task directory as a staging area.
	defer func() { _ = os.RemoveAll(filepath.Dir(filepath.Dir(filepath.Dir(staging)))) }()
	if err := copyAdoptionFiles(legacyRoot, staging, manifest); err != nil {
		return err
	}
	receipt := map[string]any{
		"schema_version":         int64(1),
		"task_id":                id,
		"plan_sha256":            sha256Sum(planBytes),
		"manifest_sha256":        sha256Sum(manifestBytes),
		"repository_id":          repository.CloneID,
		"prefix_revision":        plan["prefix_revision"],
		"prefix_semantic_sha256": plan["prefix_semantic_sha256"],
		"continuation_revision":  int64(len(chain) + 1),
		"committed_at":           nowUTC(),
	}
	receiptBytes, err := Canonical(receipt)
	if err != nil {
		return err
	}
	continuation := migratedContinuation(id, chain[len(chain)-1], plan, receipt)
	continuationData, err := Canonical(continuation)
	if err != nil {
		return err
	}
	if err := AtomicWrite(filepath.Join(staging, fmt.Sprintf("revisions/%06d.json", len(chain)+1)), continuationData, false); err != nil {
		return blocked("cannot stage migration continuation: %v", err)
	}
	if err := AtomicWrite(filepath.Join(staging, "adoption-plan.json"), planBytes, false); err != nil {
		return blocked("cannot stage adoption plan: %v", err)
	}
	if err := AtomicWrite(filepath.Join(staging, "legacy-artifact-manifest.json"), manifestBytes, false); err != nil {
		return blocked("cannot stage legacy artifact manifest: %v", err)
	}
	if err := AtomicWrite(filepath.Join(staging, "adoption-receipt.json"), receiptBytes, false); err != nil {
		return blocked("cannot stage adoption receipt: %v", err)
	}
	continuationHash := sha256Sum(continuationData)
	current := map[string]any{"revision": int64(len(chain) + 1), "sha256": continuationHash}
	currentData, err := Canonical(current)
	if err != nil {
		return err
	}
	if err := AtomicWrite(filepath.Join(staging, "current.json"), currentData, false); err != nil {
		return blocked("cannot stage current projection: %v", err)
	}
	if err := validateStagedAdoption(staging, chain, continuation, plan, receipt); err != nil {
		return err
	}
	if err := SafeMkdir(filepath.Dir(target)); err != nil {
		return err
	}
	if _, statErr := os.Lstat(target); statErr == nil {
		return conflict("canonical task target appeared while adoption was staged")
	} else if !os.IsNotExist(statErr) {
		return blocked("cannot inspect canonical task target before publish: %v", statErr)
	}
	if err := os.Rename(staging, target); err != nil {
		return conflict("cannot atomically publish adopted task: %v", err)
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return blocked("adopted task published but directory durability is unknown: %v", err)
	}
	return nil
}

func copyAdoptionFiles(sourceTask, targetTask string, manifest map[string]any) error {
	for _, raw := range anyItems(manifest["revisions"]) {
		entry := raw.(map[string]any)
		if err := copyRegularFile(filepath.Join(sourceTask, filepath.FromSlash(asStringOr(entry["source_rel"]))), filepath.Join(targetTask, filepath.FromSlash(asStringOr(entry["canonical_rel"]))), asStringOr(entry["raw_sha256"]), asIntOr(entry["size_bytes"])); err != nil {
			return err
		}
	}
	for _, raw := range anyItems(manifest["artifacts"]) {
		entry := raw.(map[string]any)
		if err := copyRegularFile(filepath.Join(sourceTask, filepath.FromSlash(asStringOr(entry["source_rel"]))), filepath.Join(targetTask, filepath.FromSlash(asStringOr(entry["canonical_rel"]))), asStringOr(entry["raw_sha256"]), asIntOr(entry["size_bytes"])); err != nil {
			return err
		}
	}
	return nil
}

func copyRegularFile(source, target, expectedHash string, expectedSize int64) error {
	data, err := ReadFileBytes(source)
	if err != nil {
		return blocked("cannot read legacy artifact %s: %v", source, err)
	}
	if int64(len(data)) != expectedSize || sha256Sum(data) != expectedHash {
		return conflict("legacy artifact changed before copy: %s", source)
	}
	if err := AtomicWrite(target, data, false); err != nil {
		return blocked("cannot stage legacy artifact: %v", err)
	}
	return nil
}

func migratedContinuation(id string, previous map[string]any, plan map[string]any, receipt map[string]any) map[string]any {
	controller := map[string]any{}
	for _, field := range []string{"project_path", "worker_path", "baseline", "request", "request_hash", "intent_hash", "policy_hash", "intent_revision", "authorization_revision", "correction_rounds", "policy_files", "policy_rules", "classification", "status", "stage", "active_attempt", "unresolved_effect", "attempts", "evidence", "events", "question", "blockers", "acceptances", "repair"} {
		if value, present := previous[field]; present {
			controller[field] = value
		}
	}
	controller["engine"] = nil
	receiptBytes, _ := Canonical(receipt)
	manifest := plan["manifest"]
	manifestBytes, _ := Canonical(manifest)
	receiptHash := sha256Sum(receiptBytes)
	manifestHash := sha256Sum(manifestBytes)
	return map[string]any{
		"schema_version": int64(2), "task_id": id,
		"revision":        int64(asIntOr(previous["revision"]) + 1),
		"previous_sha256": asStringOr(plan["prefix_semantic_sha256"]),
		"title":           legacyLabel, "description": "", "priority": "low",
		"labels": []any{}, "depends_on": []any{}, "lifecycle": "controller",
		"archived": false, "created_at": asStringOr(previous["created_at"]),
		"updated_at": asStringOr(receipt["committed_at"]),
		"provenance": map[string]any{"source": "legacy_adoption", "reference": "task:" + id, "created_in": asStringOr(plan["source_root"])},
		"controller": controller,
		"migration":  map[string]any{"receipt_sha256": receiptHash, "manifest_sha256": manifestHash, "prefix_revision": plan["prefix_revision"], "requires_rebind": true},
	}
}

func validateStagedAdoption(directory string, chain []map[string]any, continuation map[string]any, plan, receipt map[string]any) error {
	if err := validateMigrationMetadata(continuation["migration"]); err != nil {
		return blocked("staged continuation migration is invalid: %v", err)
	}
	if asStringOr(continuation["task_id"]) != asStringOr(plan["task_id"]) || asIntOr(continuation["revision"]) != int64(len(chain)+1) {
		return blocked("staged continuation identity is invalid")
	}
	if asStringOr(continuation["previous_sha256"]) != asStringOr(plan["prefix_semantic_sha256"]) {
		return blocked("staged continuation does not link the v1 prefix")
	}
	// The regular reader is deliberately the final validator once the parent
	// branch supports the mixed closed controller schema.  During a staged
	// validation, validateAdoptionChain checks the migration documents and
	// transition without publishing the target.
	combined := append([]map[string]any{}, chain...)
	combined = append(combined, continuation)
	if err := (&Repository{StorePath: filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(directory))))}).validateAdoptionChain(directory, combined); err != nil {
		// The temporary repository value above has no identity.  Document checks
		// that require a persisted clone id are intentionally deferred to the
		// post-publish read; all structural checks still run below.
		if strings.Contains(err.Error(), "canonical repository identity is unavailable") {
			return nil
		}
		return err
	}
	_ = receipt
	return nil
}

func adoptionResult(plan map[string]any, id string, revision int64, idempotent bool, target string) map[string]any {
	return map[string]any{
		"schema_version": int64(1), "task_id": id, "revision": revision,
		"status": "adopted", "source": "legacy", "next_action": "rebind",
		"binding_state": "rebind_required", "binding_sha256": plan["binding_sha256"],
		"idempotent": idempotent, "target": target,
	}
}

func (r *Repository) ensureIdentityForAdoption(planRepositoryID any) error {
	if planRepositoryID != nil {
		if _, ok := planRepositoryID.(string); !ok {
			return invalid("adoption plan repository_id must be a string or null")
		}
	}
	if err := r.EnsureIdentity(); err != nil {
		return err
	}
	if !isUUID(r.CloneID) {
		return blocked("canonical repository identity is invalid")
	}
	return nil
}

func (r *Repository) validateCommittedAdoption(target string, plan map[string]any) error {
	chain, err := r.readChain(target)
	if err != nil {
		return err
	}
	if len(chain) < 2 {
		return errors.New("canonical target is not an adopted mixed chain")
	}
	if err := r.validateAdoptionChain(target, chain); err != nil {
		return err
	}
	if expectedManifest, ok := plan["manifest"].(map[string]any); ok {
		manifestBytes, err := ReadFileBytes(filepath.Join(target, "legacy-artifact-manifest.json"))
		if err != nil {
			return err
		}
		manifest, err := decodeStrictObject(manifestBytes)
		if err != nil {
			return err
		}
		if compareJSONWithout(expectedManifest, manifest) != nil {
			return errors.New("committed artifact manifest differs")
		}
	}
	return nil
}

func buildLegacyManifest(taskRoot, id string, chain []map[string]any) (map[string]any, error) {
	root, err := SafePath(taskRoot)
	if err != nil {
		return nil, err
	}
	revisionEntries := []any{}
	for index, state := range chain {
		relative := fmt.Sprintf("revisions/%06d.json", index+1)
		entry, err := legacyManifestFile(root, relative, true, state)
		if err != nil {
			return nil, err
		}
		revisionEntries = append(revisionEntries, entry)
	}
	artifactEntries := []any{}
	externalRefs := []any{}
	// The writer lock is a reserved, generated coordination file.  Record its
	// exclusion even during preview, before apply creates the lock, so the
	// approved manifest remains byte-for-byte stable across the lock boundary.
	excluded := []any{map[string]any{"path": ".writer.lock", "reason": "writer-lock"}}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return nil, blocked("legacy task root is unavailable")
	}
	walkErr := filepath.WalkDir(root, func(full string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, relErr := filepath.Rel(root, full)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		if entry.IsDir() {
			if entry.Type()&os.ModeSymlink != 0 {
				excluded = append(excluded, map[string]any{"path": relative, "reason": "symlink-or-reparse-directory"})
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			excluded = append(excluded, map[string]any{"path": relative, "reason": "non-regular-or-reparse-file"})
			return nil
		}
		if strings.HasPrefix(relative, "revisions/") && strings.HasSuffix(relative, ".json") {
			return nil
		}
		lower := strings.ToLower(relative)
		switch {
		case relative == "current.json":
			excluded = append(excluded, map[string]any{"path": relative, "reason": "derived-current-projection"})
		case relative == ".writer.lock":
			excluded = append(excluded, map[string]any{"path": relative, "reason": "writer-lock"})
		case strings.HasSuffix(lower, ".tmp") || strings.Contains(lower, "/.tmp/"):
			excluded = append(excluded, map[string]any{"path": relative, "reason": "temporary-or-unfinished-file"})
		case adoptionCredentialPath(relative):
			excluded = append(excluded, map[string]any{"path": relative, "reason": "credential-or-auth-asset"})
		default:
			file, fileErr := legacyManifestFile(root, relative, false, nil)
			if fileErr != nil {
				return fileErr
			}
			artifactEntries = append(artifactEntries, file)
			if data, readErr := ReadFileBytes(full); readErr == nil {
				externalRefs = append(externalRefs, discoverExternalRefs(data, root)...)
			}
		}
		return nil
	})
	if walkErr != nil {
		return nil, blocked("cannot enumerate legacy task artifacts: %v", walkErr)
	}
	// Include absolute references from the validated state even when the
	// referenced artifact itself is missing.  The reference remains historical
	// and is never dereferenced by adoption.
	for _, state := range chain {
		data, _ := Canonical(state)
		externalRefs = append(externalRefs, discoverExternalRefs(data, root)...)
	}
	sortManifestEntries(revisionEntries, true)
	sortManifestEntries(artifactEntries, false)
	externalRefs = uniquePathEntries(externalRefs)
	excluded = uniquePathEntries(excluded)
	return map[string]any{"revisions": revisionEntries, "artifacts": artifactEntries, "external_refs": externalRefs, "excluded": excluded}, nil
}

func legacyManifestFile(root, relative string, revision bool, state map[string]any) (map[string]any, error) {
	if err := validateRelativeNativePath(relative, false); err != nil {
		return nil, blocked("unsafe legacy manifest path: %v", err)
	}
	full := filepath.Join(root, filepath.FromSlash(relative))
	data, err := ReadFileBytes(full)
	if err != nil {
		return nil, blocked("legacy manifest file is unavailable: %v", err)
	}
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		return nil, blocked("legacy manifest file is not regular: %s", relative)
	}
	entry := map[string]any{
		"source_rel": relative,
		// The migration contract pins adopted artifacts under
		// legacy/artifacts/<source-relative-path>; only revisions keep their
		// canonical revisions/%06d.json location.
		"canonical_rel": map[bool]string{true: relative, false: "legacy/artifacts/" + relative}[revision],
		"size_bytes":    int64(len(data)),
		"raw_sha256":    sha256Sum(data),
	}
	if revision {
		hash, err := Hash(state)
		if err != nil {
			return nil, err
		}
		entry["semantic_sha256"] = hash
	}
	return entry, nil
}

func sortManifestEntries(values []any, revisions bool) {
	sort.Slice(values, func(i, j int) bool {
		left, right := values[i].(map[string]any), values[j].(map[string]any)
		if revisions {
			return asStringOr(left["source_rel"]) < asStringOr(right["source_rel"])
		}
		return asStringOr(left["source_rel"]) < asStringOr(right["source_rel"])
	})
}

func adoptionCredentialPath(relative string) bool {
	for _, component := range strings.Split(strings.ToLower(filepath.ToSlash(relative)), "/") {
		if component == "auth" || component == "credentials" || component == "secrets" || component == "tokens" || strings.Contains(component, "credential") {
			return true
		}
	}
	name := strings.ToLower(filepath.Base(relative))
	return name == "credentials.json" || name == "auth.json" || name == "token.json" || strings.Contains(name, ".secret.")
}

func discoverExternalRefs(data []byte, root string) []any {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil
	}
	refs := []any{}
	var visit func(any)
	visit = func(item any) {
		switch typed := item.(type) {
		case string:
			if isAbsoluteReference(typed) {
				if candidate, err := SafePath(typed); err == nil && withinRoot(root, candidate) {
					return
				}
				refs = append(refs, map[string]any{"path": typed, "reason": "historical-absolute-reference"})
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				visit(typed[key])
			}
		}
	}
	visit(value)
	return refs
}

func isAbsoluteReference(value string) bool {
	if filepath.IsAbs(value) {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}

func uniquePathEntries(values []any) []any {
	type item struct{ path, reason string }
	items := map[string]item{}
	for _, raw := range values {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path, reason := asStringOr(entry["path"]), asStringOr(entry["reason"])
		if path == "" || reason == "" {
			continue
		}
		key := path + "\x00" + reason
		items[key] = item{path, reason}
	}
	ordered := make([]item, 0, len(items))
	for _, value := range items {
		ordered = append(ordered, value)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].path != ordered[j].path {
			return ordered[i].path < ordered[j].path
		}
		return ordered[i].reason < ordered[j].reason
	})
	result := make([]any, 0, len(ordered))
	for _, value := range ordered {
		result = append(result, map[string]any{"path": value.path, "reason": value.reason})
	}
	return result
}

func validateAdoptionPlanShape(plan map[string]any) error {
	if err := exactFields(plan, adoptionPlanFields, "adoption plan"); err != nil {
		return err
	}
	if version, ok := asInt(plan["schema_version"]); !ok || version != 1 {
		return errors.New("adoption plan schema_version must be 1")
	}
	if asStringOr(plan["contract"]) != adoptionContract {
		return errors.New("adoption plan contract is unsupported")
	}
	if !isUUID(asStringOr(plan["task_id"])) {
		return errors.New("adoption plan task_id is invalid")
	}
	for _, field := range []string{"source_root", "common_dir"} {
		if err := validateAbsolutePlanPath(plan[field], field); err != nil {
			return err
		}
	}
	if value := plan["repository_id"]; value != nil && !isUUID(asStringOr(value)) {
		return errors.New("adoption plan repository_id must be a UUID or null")
	}
	for _, field := range []string{"prefix_revision"} {
		if value, ok := asInt(plan[field]); !ok || value < 1 {
			return fmt.Errorf("%s must be a positive integer", field)
		}
	}
	for _, field := range []string{"prefix_semantic_sha256", "binding_sha256"} {
		if !isSHA256(asStringOr(plan[field])) {
			return fmt.Errorf("%s must be a lowercase SHA-256", field)
		}
	}
	if err := validateAdoptionSources(plan["sources"]); err != nil {
		return err
	}
	manifest, ok := plan["manifest"].(map[string]any)
	if !ok {
		return errors.New("adoption plan manifest must be an object")
	}
	if err := validateAdoptionManifestShape(manifest); err != nil {
		return err
	}
	if err := validateAdoptionDependencies(plan["dependencies"]); err != nil {
		return err
	}
	if err := validateAdoptionEligibility(plan["eligibility"], plan["blockers"]); err != nil {
		return err
	}
	without := cloneMap(plan)
	delete(without, "binding_sha256")
	binding, err := Hash(without)
	if err != nil {
		return err
	}
	if binding != asStringOr(plan["binding_sha256"]) {
		return errors.New("adoption plan binding_sha256 is incorrect")
	}
	return nil
}

func validateAdoptionSources(value any) error {
	items := anyItems(value)
	if items == nil || len(items) == 0 {
		return errors.New("adoption plan sources must be a non-empty array")
	}
	seen := map[string]bool{}
	for _, raw := range items {
		entry, ok := raw.(map[string]any)
		if !ok {
			return errors.New("adoption source must be an object")
		}
		if err := exactFields(entry, adoptionSourceFields, "adoption source"); err != nil {
			return err
		}
		root := asStringOr(entry["root"])
		if err := validateAbsolutePlanPath(root, "source.root"); err != nil {
			return err
		}
		key := strings.ToLower(filepath.Clean(root))
		if seen[key] {
			return errors.New("adoption sources contain a duplicate root")
		}
		seen[key] = true
		for _, field := range []string{"prefix_semantic_sha256", "raw_manifest_sha256", "artifact_manifest_sha256"} {
			if !isSHA256(asStringOr(entry[field])) {
				return fmt.Errorf("source.%s must be a lowercase SHA-256", field)
			}
		}
	}
	return nil
}

func validateAdoptionManifestShape(manifest map[string]any) error {
	if err := exactFields(manifest, adoptionManifestFields, "legacy artifact manifest"); err != nil {
		return err
	}
	if err := validateManifestFiles(manifest["revisions"], true); err != nil {
		return err
	}
	if err := validateManifestFiles(manifest["artifacts"], false); err != nil {
		return err
	}
	for _, field := range []string{"external_refs", "excluded"} {
		items := anyItems(manifest[field])
		if items == nil {
			return fmt.Errorf("manifest.%s must be an array", field)
		}
		seen := map[string]bool{}
		for _, raw := range items {
			entry, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("manifest.%s entry must be an object", field)
			}
			if err := exactFields(entry, adoptionPathEntryFields, "manifest path entry"); err != nil {
				return err
			}
			path, reason := asStringOr(entry["path"]), asStringOr(entry["reason"])
			if path == "" || reason == "" || strings.ContainsAny(path+reason, "\x00\r\n") {
				return fmt.Errorf("manifest.%s entries require safe path and reason", field)
			}
			key := path + "\x00" + reason
			if seen[key] {
				return fmt.Errorf("manifest.%s contains duplicate entries", field)
			}
			seen[key] = true
		}
	}
	return nil
}

func validateManifestFiles(value any, revisions bool) error {
	items := anyItems(value)
	if items == nil || (revisions && len(items) == 0) {
		return errors.New("manifest file list is invalid")
	}
	seenSource, seenCanonical := map[string]bool{}, map[string]bool{}
	for index, raw := range items {
		entry, ok := raw.(map[string]any)
		if !ok {
			return errors.New("manifest file entry must be an object")
		}
		fields := adoptionArtifactEntryFields
		if revisions {
			fields = adoptionRevisionEntryFields
		}
		if err := exactFields(entry, fields, "manifest file entry"); err != nil {
			return err
		}
		sourceRel, canonicalRel := asStringOr(entry["source_rel"]), asStringOr(entry["canonical_rel"])
		if err := validateRelativeNativePath(sourceRel, false); err != nil {
			return err
		}
		if err := validateRelativeNativePath(canonicalRel, false); err != nil {
			return err
		}
		sourceKey, canonicalKey := strings.ToLower(filepath.ToSlash(sourceRel)), strings.ToLower(filepath.ToSlash(canonicalRel))
		if seenSource[sourceKey] || seenCanonical[canonicalKey] {
			return errors.New("manifest contains duplicate or case-colliding paths")
		}
		seenSource[sourceKey], seenCanonical[canonicalKey] = true, true
		if revisions {
			expected := fmt.Sprintf("revisions/%06d.json", index+1)
			if sourceRel != expected || canonicalRel != expected {
				return errors.New("manifest revisions must be contiguous and canonical")
			}
		} else if !strings.HasPrefix(filepath.ToSlash(canonicalRel), "legacy/artifacts/") {
			return errors.New("manifest artifact canonical_rel must be under legacy/artifacts")
		}
		if size, ok := asInt(entry["size_bytes"]); !ok || size < 0 {
			return errors.New("manifest size_bytes must be non-negative")
		}
		if !isSHA256(asStringOr(entry["raw_sha256"])) {
			return errors.New("manifest raw_sha256 must be a lowercase SHA-256")
		}
		if revisions && !isSHA256(asStringOr(entry["semantic_sha256"])) {
			return errors.New("manifest semantic_sha256 must be a lowercase SHA-256")
		}
	}
	return nil
}

func validateAdoptionDependencies(value any) error {
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return errors.New("adoption dependencies must be an object")
	}
	if err := exactFields(object, adoptionDependencyFields, "adoption dependencies"); err != nil {
		return err
	}
	if asStringOr(object["baseline"]) == "" {
		return errors.New("dependencies.baseline must be non-empty")
	}
	for _, field := range []string{"request_sha256", "intent_sha256", "policy_sha256"} {
		if !isSHA256(asStringOr(object[field])) {
			return fmt.Errorf("dependencies.%s must be a lowercase SHA-256", field)
		}
	}
	if value := object["execution_profile_sha256"]; value != nil && !isSHA256(asStringOr(value)) {
		return errors.New("dependencies.execution_profile_sha256 must be a SHA-256 or null")
	}
	return nil
}

func validateAdoptionEligibility(value, blockersValue any) error {
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return errors.New("adoption eligibility must be an object")
	}
	if err := exactFields(object, adoptionEligibilityFields, "adoption eligibility"); err != nil {
		return err
	}
	checks, ok := object["checks"].(map[string]any)
	if !ok || checks == nil {
		return errors.New("adoption eligibility checks must be an object")
	}
	if err := exactFields(checks, adoptionEligibilityCheckFields, "adoption eligibility checks"); err != nil {
		return err
	}
	eligible, ok := asBool(object["eligible"])
	if !ok {
		return errors.New("adoption eligibility.eligible must be boolean")
	}
	computed := true
	for _, key := range []string{"source_owner", "journal", "siblings", "inactive", "processes_terminal", "publications_clear", "native_targets_clear", "artifacts_complete", "target_available"} {
		value, ok := asBool(checks[key])
		if !ok {
			return fmt.Errorf("eligibility check %s must be boolean", key)
		}
		computed = computed && value
	}
	if eligible != computed {
		return errors.New("eligibility.eligible does not match checks")
	}
	blockers := anyItems(blockersValue)
	if blockers == nil {
		return errors.New("adoption blockers must be an array")
	}
	if eligible && len(blockers) != 0 {
		return errors.New("eligible adoption cannot contain blockers")
	}
	if !eligible && len(blockers) == 0 {
		return errors.New("blocked adoption must contain blockers")
	}
	for _, raw := range blockers {
		text, ok := asString(raw)
		if !ok || strings.TrimSpace(text) == "" || len(text) > 1024 || (!strings.HasPrefix(text, "BF_BLOCKED:") && !strings.HasPrefix(text, "BF_CONFLICT:") && !strings.HasPrefix(text, "BF_INVALID:")) {
			return errors.New("adoption blockers must be bounded BF diagnostics")
		}
	}
	return nil
}

func validateAdoptionReceiptShape(receipt map[string]any) error {
	if err := exactFields(receipt, adoptionReceiptFields, "adoption receipt"); err != nil {
		return err
	}
	if version, ok := asInt(receipt["schema_version"]); !ok || version != 1 {
		return errors.New("adoption receipt schema_version must be 1")
	}
	if !isUUID(asStringOr(receipt["task_id"])) {
		return errors.New("adoption receipt task_id is invalid")
	}
	for _, field := range []string{"plan_sha256", "manifest_sha256", "prefix_semantic_sha256"} {
		if !isSHA256(asStringOr(receipt[field])) {
			return fmt.Errorf("adoption receipt %s must be a lowercase SHA-256", field)
		}
	}
	if repositoryID := asStringOr(receipt["repository_id"]); !isUUID(repositoryID) {
		return errors.New("adoption receipt repository_id is invalid")
	}
	for _, field := range []string{"prefix_revision", "continuation_revision"} {
		value, ok := asInt(receipt[field])
		if !ok || value < 1 {
			return fmt.Errorf("adoption receipt %s must be a positive integer", field)
		}
	}
	committed, ok := asString(receipt["committed_at"])
	if err := validateTimestamp(committed, ok, "adoption receipt committed_at"); err != nil {
		return err
	}
	return nil
}

func validateAdoptionPlanBinding(plan map[string]any, id string, prefixRevision int64, prefixHash string, manifest map[string]any) error {
	if asStringOr(plan["task_id"]) != id || asIntOr(plan["prefix_revision"]) != prefixRevision || asStringOr(plan["prefix_semantic_sha256"]) != prefixHash {
		return errors.New("plan prefix binding differs from chain")
	}
	plannedManifest, ok := plan["manifest"].(map[string]any)
	if !ok {
		return errors.New("plan manifest is missing")
	}
	left, _ := Canonical(plannedManifest)
	right, _ := Canonical(manifest)
	if !bytes.Equal(left, right) {
		return errors.New("plan manifest differs from saved manifest")
	}
	return nil
}

func validateAbsolutePlanPath(value any, name string) error {
	text, ok := asString(value)
	if !ok || strings.TrimSpace(text) == "" || strings.ContainsAny(text, "\x00\r\n") {
		return fmt.Errorf("%s must be a non-empty path", name)
	}
	if _, err := SafePath(text); err != nil {
		return fmt.Errorf("%s is unsafe: %v", name, err)
	}
	return nil
}

func exactFields(object map[string]any, allowed map[string]bool, name string) error {
	for field := range object {
		if !allowed[field] {
			return fmt.Errorf("%s contains unsupported field %s", name, field)
		}
	}
	return nil
}

func cloneMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

func ensureDirectory(path, name string) error {
	full, err := SafePath(path)
	if err != nil {
		return blocked("unsafe %s path: %v", name, err)
	}
	info, statErr := os.Lstat(full)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return blocked("%s is missing", name)
		}
		return blocked("cannot inspect %s: %v", name, statErr)
	}
	if !info.IsDir() {
		return blocked("%s is not a directory", name)
	}
	return nil
}

func samePath(left, right string) bool {
	left, leftErr := SafePath(left)
	right, rightErr := SafePath(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSuffix(filepath.Clean(left), string(filepath.Separator)), strings.TrimSuffix(filepath.Clean(right), string(filepath.Separator)))
}

func (r *Repository) resolveAdoptedArtifact(taskID, originalPath string) (string, error) {
	if !isUUID(taskID) {
		return "", invalid("task id must be a lowercase UUID")
	}
	original, err := SafePath(originalPath)
	if err != nil {
		return "", blocked("unsafe historical artifact reference: %v", err)
	}
	target, err := r.taskDir(taskID)
	if err != nil {
		return "", err
	}
	chain, err := r.readChain(target)
	if err != nil {
		return "", err
	}
	if len(chain) < 2 {
		return "", blocked("task has no adopted artifact manifest")
	}
	planBytes, err := ReadFileBytes(filepath.Join(target, "adoption-plan.json"))
	if err != nil {
		return "", blocked("adoption plan is unavailable: %v", err)
	}
	plan, err := decodeStrictObject(planBytes)
	if err != nil {
		return "", blocked("adoption plan is invalid: %v", err)
	}
	manifest, err := readStrictObjectAt(filepath.Join(target, "legacy-artifact-manifest.json"))
	if err != nil {
		return "", blocked("legacy artifact manifest is unavailable: %v", err)
	}
	if err := validateAdoptionPlanShape(plan); err != nil {
		return "", blocked("adoption plan is invalid: %v", err)
	}
	if err := validateAdoptionManifestShape(manifest); err != nil {
		return "", blocked("legacy artifact manifest is invalid: %v", err)
	}
	sourceRoot := asStringOr(plan["source_root"])
	legacyRoot := filepath.Join(sourceRoot, ".bsl-flow", "tasks", taskID)
	for _, raw := range anyItems(manifest["artifacts"]) {
		entry := raw.(map[string]any)
		sourceRel := asStringOr(entry["source_rel"])
		candidate := filepath.Join(legacyRoot, filepath.FromSlash(sourceRel))
		if !samePath(candidate, original) {
			continue
		}
		canonicalRel := asStringOr(entry["canonical_rel"])
		canonical := filepath.Join(target, filepath.FromSlash(canonicalRel))
		if !withinRoot(target, canonical) {
			return "", blocked("adopted artifact escapes the canonical task root")
		}
		data, readErr := ReadFileBytes(canonical)
		if readErr != nil {
			return "", blocked("adopted artifact is unavailable: %v", readErr)
		}
		if int64(len(data)) != asIntOr(entry["size_bytes"]) || sha256Sum(data) != asStringOr(entry["raw_sha256"]) {
			return "", conflict("adopted artifact bytes differ from its manifest")
		}
		return canonical, nil
	}
	return "", blocked("historical artifact reference is not declared by the adoption manifest")
}

func readStrictObjectAt(path string) (map[string]any, error) {
	data, err := ReadFileBytes(path)
	if err != nil {
		return nil, err
	}
	return decodeStrictObject(data)
}

// decodeStrictObject keeps duplicate-key rejection local to migration
// documents.  DecodeObject intentionally remains the compatibility decoder
// for the older controller state and inputs.
func decodeStrictObject(data []byte) (map[string]any, error) {
	if len(data) > maxJSONBytes {
		return nil, errors.New("JSON input exceeds the maximum allowed size")
	}
	if !bytes.Equal(bytes.TrimSpace(data), data) {
		return nil, errors.New("JSON document must be canonical without surrounding whitespace")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeStrictValue(decoder)
	if err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("unexpected data after JSON value")
		}
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("top-level JSON value must be an object")
	}
	return object, nil
}

func decodeStrictValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			object := map[string]any{}
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return nil, errors.New("JSON object contains a duplicate key")
				}
				seen[key] = true
				value, err := decodeStrictValue(decoder)
				if err != nil {
					return nil, err
				}
				object[key] = value
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return nil, errors.New("malformed JSON object")
			}
			return object, nil
		case '[':
			values := []any{}
			for decoder.More() {
				value, err := decodeStrictValue(decoder)
				if err != nil {
					return nil, err
				}
				values = append(values, value)
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return nil, errors.New("malformed JSON array")
			}
			return values, nil
		default:
			return nil, errors.New("unexpected JSON delimiter")
		}
	default:
		return typed, nil
	}
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
