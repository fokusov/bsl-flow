package stagehost

import (
	"path/filepath"

	"bsl-flow/cli/internal/repository"
)

// This file keeps the stage-host wrapper of the shared loaded-proof binding
// (repository.Native1CLoadedProof): the provider computes the current source
// manifest itself and converts repository diagnostics into the provider error
// taxonomy.

// nativeLoadedProof mirrors Get-BFNativeLoadedProof. It returns nil when the
// criterion carries no reuse_load_attempt.
func nativeLoadedProof(state map[string]any, criterion map[string]any, source map[string]any, beforeInventory map[string]any) (map[string]any, error) {
	manifest, err := stageSourceManifest(state)
	if err != nil {
		return nil, err
	}
	taskDirectory, err := safePath(filepath.Join(asStringOr(state["project_path"]), ".bsl-flow", "tasks", asStringOr(state["task_id"])))
	if err != nil {
		return nil, err
	}
	proof, err := repository.Native1CLoadedProof(state, criterion, source, beforeInventory, taskDirectory, manifest)
	if err != nil {
		return nil, native1cError(err)
	}
	return proof, nil
}
