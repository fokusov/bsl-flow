package repository

import (
	"encoding/json"
	"path/filepath"
	"time"
)

// Exported seams consumed by the native Go stage host provider process
// (cli/internal/stagehost). The stage host is the in-binary port of the
// packaged PowerShell provider; it must compute exactly the same bindings the
// controller recomputes independently, so both sides share these functions
// instead of duplicating them.

// StageHostSourceManifest mirrors Get-BFSourceManifest for the provider side:
// the complete checkout under the worker root is bound to the exact baseline.
func StageHostSourceManifest(workerPath, baseline string) (map[string]any, error) {
	return sourceManifestWithBaseline(workerPath, baseline, []string{"."})
}

// StageHostDependencies mirrors Get-BFDependencies for the provider side.
func StageHostDependencies(state map[string]any, stage string, manifest map[string]any) (map[string]any, error) {
	return currentNativeDependencies(state, stage, manifest)
}

// StageHostPolicyInventory mirrors Get-BFPolicyFiles for the provider side.
func StageHostPolicyInventory(project, skillsRoot, hostPath string) ([]any, error) {
	return nativePolicyInventory(project, skillsRoot, hostPath)
}

// StageHostProjectRules mirrors Get-BFProviderProjectRules for the provider
// side: mandatory M/L/high review cannot be weakened by project policy.
func StageHostProjectRules(project string) (map[string]any, error) {
	return nativeProjectRules(project)
}

// StageHostSpecInputs mirrors Get-BFSpecInputs for an arbitrary project root.
func StageHostSpecInputs(projectPath, taskID string) (map[string]any, error) {
	return specInputsAt(projectPath, taskID)
}

// StageHostHash mirrors Get-BFHash on the provider side.
func StageHostHash(value any) (string, error) { return Hash(value) }

// StageHostCanonical mirrors Get-BFCanonicalJson on the provider side.
func StageHostCanonical(value any) ([]byte, error) { return Canonical(value) }

// StageHostFileSHA256 mirrors Get-BFFileHash for already-read bytes.
func StageHostFileSHA256(data []byte) string { return fileSHA256(data) }

// StageHostJUnit mirrors Test-BFJUnit -AllowFailure: it reports whether every
// expected test passed and rejects tampered or incomplete reports.
func StageHostJUnit(data []byte, expected []string) (bool, error) {
	return validateNativeJUnit(data, expected)
}

// StageHostGitOutput mirrors Invoke-BFGit for the provider side.
func StageHostGitOutput(directory string, args ...string) (string, error) {
	return gitOutput(directory, args...)
}

// StageHostSafePath mirrors Assert-BFSafePath for the provider side.
func StageHostSafePath(target string) (string, error) { return SafePath(target) }

// StageHostIsSHA256 reports whether the value is a lowercase SHA-256 hex string.
func StageHostIsSHA256(value string) bool { return isSHA256(value) }

// StageHostIsUUID reports whether the value is a canonical lowercase UUID.
func StageHostIsUUID(value string) bool { return isUUID(value) }

// StageHostReadFileBytes reads a file through the bounded controller reader.
func StageHostReadFileBytes(path string) ([]byte, error) { return ReadFileBytes(path) }

// StageHostExecutionDependencies mirrors Get-BFExecutionDependencies: the
// pinned provider/sandbox/toolset identity revalidation before any dispatch.
func StageHostExecutionDependencies(state map[string]any) (map[string]any, error) {
	return currentNativeExecutionDependencies(state)
}

// StageHostToolsetAggregateHash mirrors the deterministic toolset skill hash
// used by packaged toolset snapshot manifests.
func StageHostToolsetAggregateHash(skills []any) (string, error) {
	return nativeToolsetAggregateHash(skills)
}

// StageHostNowUTC returns the controller timestamp format.
func StageHostNowUTC() string { return nowUTC() }

// StageHostArchitectureContextRoot mirrors Resolve-BFArchitectureContext for
// the provider side: a project index wins, then the package root, then the
// project itself as the missing-scope fallback.
func StageHostArchitectureContextRoot(projectPath, packageRoot string) (string, error) {
	return nativeArchitectureContextRoot(map[string]any{"project_path": projectPath}, packageRoot)
}

// StageHostArchitectureBundlePrompt mirrors Get-BFArchitectureBundle +
// Format-BFArchitectureBundlePrompt: the exact prompt text a stage prompt
// embeds for its architecture context.
func StageHostArchitectureBundlePrompt(stage, root, packageRoot string) (string, error) {
	return nativeArchitectureBundlePrompt(stage, root, packageRoot)
}

// StageHostArchitectureBundleContent mirrors Get-BFArchitectureBundle: the
// full deterministic bundle content (identity, decisions, missing_context,
// excluded) the council evidence text embeds and hashes.
func StageHostArchitectureBundleContent(stage, root, packageRoot string) (map[string]any, error) {
	return nativeArchitectureBundleContent(stage, root, packageRoot)
}

// StageHostPackageRootOfSkillsRoot derives the trusted package root from the
// extracted skill root (<package>/global/skills), matching
// Get-BFArchitectureRoot's four-level walk above the 1c-task scripts.
func StageHostPackageRootOfSkillsRoot(skillsRoot string) (string, error) {
	root, err := SafePath(skillsRoot)
	if err != nil {
		return "", err
	}
	// skills -> global -> package
	return SafePath(filepath.Dir(filepath.Dir(root)))
}

// StageHostCanonicalNumber converts a canonical JSON number token into the
// json.Number representation the canonical encoder preserves, so callers can
// rebuild state values without changing their hash.
func StageHostCanonicalNumber(text string) any { return json.Number(text) }

// The native 1C runtime adapter bindings below are shared read-only
// computations: the controller computes stage dependencies and recovery
// control reads, the stage host re-binds the same identities before dispatch.
// Messages and hashes must stay byte-identical across both surfaces.

// StageHostNative1CPlatformBlocker mirrors the typed Windows-only capability
// gate: nil on windows, BLOCKED_UNSUPPORTED_PLATFORM everywhere else.
func StageHostNative1CPlatformBlocker(goos string) error {
	return Native1CPlatformBlocker(goos)
}

// StageHostNative1CCriterion mirrors Assert-BFNativeCriterion.
func StageHostNative1CCriterion(criterion map[string]any) error {
	return ValidateNative1CCriterionShape(criterion)
}

// StageHostNative1CTargetIdentity mirrors Get-BFRuntimeTargetIdentity.
func StageHostNative1CTargetIdentity(target string) (string, error) {
	return Native1CTargetIdentity(target)
}

// StageHostNative1CTargetKey mirrors Get-BFRuntimeTargetKey.
func StageHostNative1CTargetKey(target string) (string, error) {
	return Native1CTargetKey(target)
}

// StageHostNative1CJournalRoot mirrors Get-BFNativeJournalRoot.
func StageHostNative1CJournalRoot(key string) (string, error) {
	return Native1CJournalRoot(key)
}

// StageHostNative1CSourceSnapshot mirrors Get-BFNativeSource.
func StageHostNative1CSourceSnapshot(root string) (map[string]any, error) {
	return Native1CSourceSnapshot(root)
}

// StageHostNative1CSnapshotCopy mirrors Copy-BFNativeSnapshot.
func StageHostNative1CSnapshotCopy(source map[string]any, destination string) (map[string]any, error) {
	return Native1CSnapshotCopy(source, destination)
}

// StageHostNative1CDependencies mirrors Get-BFNativeDependencies.
func StageHostNative1CDependencies(criterion map[string]any) (map[string]any, error) {
	return Native1CDependencies(criterion)
}

// StageHostNative1CInventoryHash mirrors Get-BFNativeInventoryHash.
func StageHostNative1CInventoryHash(inventory map[string]any) (string, error) {
	return Native1CInventoryHash(inventory)
}

// StageHostNative1CNormalizeInventoryRow mirrors ConvertTo-BFNativeInventoryRow.
func StageHostNative1CNormalizeInventoryRow(item map[string]any) (map[string]any, error) {
	return Native1CNormalizeInventoryRow(item)
}

// StageHostNative1CInventoryTransition mirrors Assert-BFNativeInventoryTransition.
func StageHostNative1CInventoryTransition(before, after, source map[string]any) error {
	return Native1CInventoryTransition(before, after, source)
}

// StageHostNative1CJUnit mirrors Test-BFNativeJUnit.
func StageHostNative1CJUnit(path string, expected []string, started, finished time.Time) (map[string]any, error) {
	return Native1CJUnit(path, expected, started, finished)
}
