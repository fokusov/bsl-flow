package repository

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
