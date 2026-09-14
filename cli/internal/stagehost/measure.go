package stagehost

import (
	"context"
	"os"
	"path/filepath"

	"bsl-flow/cli/internal/repository"
)

// providerMeasure mirrors Invoke-BFProviderMeasure: a closed, read-only
// preflight observation. Every computed binding is independently rechecked by
// the controller, so this host must compute them through the exact same
// shared functions instead of inventing values.
func providerMeasure(ctx context.Context, deps Deps, input *providerInput) (map[string]any, error) {
	state := input.stateView
	blockers := []any{}
	requestValid := true
	policyFiles := []any{}
	var policyRules map[string]any
	var manifest, specInputs, dependencies, capability map[string]any
	project := asStringOr(state["project_path"])
	if err := assertRequest(state["request"]); err != nil {
		requestValid = false
		blockers = append(blockers, err.Error())
	}
	inventory, err := repository.StageHostPolicyInventory(project, deps.SkillsRoot, deps.HostPolicyPath)
	if err != nil {
		blockers = append(blockers, providerBlocker(err))
	} else {
		policyFiles = inventory
	}
	rules, err := repository.StageHostProjectRules(project)
	if err != nil {
		blockers = append(blockers, providerBlocker(err))
	} else {
		policyRules = rules
	}
	manifest, err = stageSourceManifest(state)
	if err != nil {
		manifest = nil
		blockers = append(blockers, providerBlocker(err))
	}
	specInputs, err = stageSpecInputs(state)
	if err != nil {
		specInputs = nil
		blockers = append(blockers, providerBlocker(err))
	}
	// Initial activation measures the inspect inputs. A stage-bound measure
	// carries its registered attempt and preserves that stage in the binding.
	measureStage := "inspect"
	if input.attempt != nil {
		measureStage = asStringOr(input.attempt["stage"])
	}
	dependencies, err = stageDependencies(state, measureStage)
	if err != nil {
		dependencies = nil
		blockers = append(blockers, providerBlocker(err))
	}
	request, _ := asObject(state["request"])
	if profile := getValue(request, "execution_profile", nil); profile != nil {
		capabilityRoot, err := safePath(filepath.Join(input.artifactRoot, "capability"))
		if err != nil {
			blockers = append(blockers, providerBlocker(err))
		} else {
			capabilitySource := filepath.Join(capabilityRoot, "source")
			capabilityScratch := filepath.Join(capabilityRoot, "scratch")
			capabilityConfig := filepath.Join(capabilityRoot, "config")
			// The activation view points at the real project; only the host
			// capability probe receives a cloned state with an isolated source
			// root, because the permission profile must never reopen the
			// project's controller paths as a worker tree.
			cloned, err := cloneStateView(state)
			if err != nil {
				blockers = append(blockers, providerBlocker(err))
			} else {
				cloned["worker_path"] = capabilitySource
				if err := os.MkdirAll(capabilitySource, 0o755); err != nil {
					blockers = append(blockers, blockedf("%v", err).Error())
				} else if permissions, err := permissionProfile(cloned, capabilityScratch, capabilityConfig, false, input.canonicalStore); err != nil {
					blockers = append(blockers, providerBlocker(err))
				} else if observed, err := executionCapability(ctx, deps, cloned, capabilityRoot, capabilityScratch, capabilityConfig, permissions, false, input.canonicalStore); err != nil {
					blockers = append(blockers, providerBlocker(err))
				} else {
					capability = observed
				}
			}
		}
	} else {
		blockers = append(blockers, "BF_BLOCKED: measure requires a trusted execution_profile.")
	}
	// Re-enumerate the original project after the isolated probe. This closes
	// the provider-side source TOCTOU window and keeps the capability clone
	// from becoming the measured source manifest.
	if manifest != nil {
		after, err := stageSourceManifest(state)
		if err != nil {
			blockers = append(blockers, providerBlocker(err))
		} else {
			beforeHash, err := hashValue(manifest)
			if err != nil {
				return nil, err
			}
			afterHash, err := hashValue(after)
			if err != nil {
				return nil, err
			}
			if beforeHash != afterHash {
				blockers = append(blockers, "BF_BLOCKED: source changed during provider measure.")
			}
		}
	}
	return map[string]any{
		"schema_version":  int64(1),
		"contract":        Contract,
		"task_id":         input.taskID,
		"operation":       "measure",
		"request_valid":   requestValid,
		"policy_files":    policyFiles,
		"policy_rules":    policyRules,
		"source_manifest": manifest,
		"spec_inputs":     specInputs,
		"dependencies":    dependencies,
		"capability":      capability,
		"blockers":        blockers,
	}, nil
}

// providerBlocker renders an error as the blocker text the controller
// surfaces, keeping the legacy BF_ classification visible.
func providerBlocker(err error) string {
	if typed, ok := err.(*Error); ok {
		return typed.Error()
	}
	return blockedf("%v", err).Error()
}

// stageSourceManifest mirrors Get-BFSourceManifest $State.
func stageSourceManifest(state map[string]any) (map[string]any, error) {
	worker := asStringOr(state["worker_path"])
	if !isDirectory(worker) {
		return nil, blockedf("worktree is missing.")
	}
	return repository.StageHostSourceManifest(worker, asStringOr(state["baseline"]))
}

// stageSpecInputs mirrors Get-BFSpecInputs $State.
func stageSpecInputs(state map[string]any) (map[string]any, error) {
	return repository.StageHostSpecInputs(asStringOr(state["project_path"]), asStringOr(state["task_id"]))
}

// cloneStateView deep-copies a state view through the canonical encoder.
func cloneStateView(state map[string]any) (map[string]any, error) {
	data, err := repository.Canonical(state)
	if err != nil {
		return nil, invalidf("%v", err)
	}
	cloned, err := repository.DecodeObject(data)
	if err != nil {
		return nil, invalidf("Cannot materialize JSON object: %v", err)
	}
	return cloned, nil
}

// stageDependencies mirrors Get-BFDependencies $State $Stage $null.
func stageDependencies(state map[string]any, stage string) (map[string]any, error) {
	return repository.StageHostDependencies(state, stage, nil)
}
