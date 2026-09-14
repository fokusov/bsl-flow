package stagehost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"bsl-flow/cli/internal/repository"
)

// This file ports Resolve-BFExecutionCanonicalStoreRoot,
// Get-BFExecutionPermissionProfile, Get-BFExecutionCanonicalProbePaths and
// Test-BFExecutionCapability from Task.Execution.ps1. The filesystem probe
// inside the sandbox is the trusted host binary itself (`__fs-probe`) instead
// of an encoded PowerShell command.

// canonicalStoreRoot mirrors Resolve-BFExecutionCanonicalStoreRoot.
func canonicalStoreRoot(state map[string]any, fallback string) (string, error) {
	if strings.TrimSpace(fallback) != "" {
		return safePath(fallback)
	}
	registered := asStringOr(state["canonical_store_root"])
	if strings.TrimSpace(registered) != "" {
		return safePath(registered)
	}
	project, err := safePath(asStringOr(state["project_path"]))
	if err != nil {
		return "", err
	}
	common, err := repository.StageHostGitOutput(project, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", blockedf("unable to resolve the Git common directory for execution fencing.")
	}
	common = strings.TrimSpace(common)
	commonPath := common
	if !isAbsolutePath(commonPath) {
		commonPath = filepath.Join(project, common)
	}
	commonPath, err = safePath(commonPath)
	if err != nil {
		return "", err
	}
	return safePath(filepath.Join(commonPath, "bsl-flow"))
}

type profileEntry struct {
	path   string
	access string
}

// permissionProfile mirrors Get-BFExecutionPermissionProfile: an ordered,
// TOML-shaped permission string whose exact bytes are hashed into bindings.
func permissionProfile(state map[string]any, scratch, config string, writable bool, canonicalFallback string) (string, error) {
	request, _ := asObject(state["request"])
	profile, ok := asObject(request["execution_profile"])
	if !ok {
		return "", invalidf("execution_profile must be an object.")
	}
	access := "read"
	if writable {
		access = "write"
	}
	projectPath, err := safePath(asStringOr(state["project_path"]))
	if err != nil {
		return "", err
	}
	controller, err := safePath(filepath.Join(projectPath, ".bsl-flow", "tasks"))
	if err != nil {
		return "", err
	}
	resolvedCanonical, err := canonicalStoreRoot(state, canonicalFallback)
	if err != nil {
		return "", err
	}
	privateRoots := []string{controller}
	privateRoots = append(privateRoots, stringList(profile["denied_read_roots"])...)
	entries := []profileEntry{{path: ":root", access: "read"}, {path: controller, access: "none"}}
	if strings.TrimSpace(resolvedCanonical) != "" {
		privateRoots = append(privateRoots, resolvedCanonical)
		entries = append(entries, profileEntry{path: resolvedCanonical, access: "none"})
	}
	worker, err := safePath(asStringOr(state["worker_path"]))
	if err != nil {
		return "", err
	}
	gitPath, err := safePath(filepath.Join(projectPath, ".git"))
	if err != nil {
		return "", err
	}
	var gitRoots []string
	if strings.TrimSpace(resolvedCanonical) == "" {
		gitRoots = []string{gitPath}
	} else {
		roots := []string{}
		if isRegularFile(gitPath) {
			roots = append(roots, gitPath)
		}
		common := filepath.Dir(resolvedCanonical)
		for _, name := range []string{"HEAD", "config", "index", "packed-refs", "commondir", "description", "refs", "objects", "logs"} {
			candidate, err := safePath(filepath.Join(common, name))
			if err != nil {
				return "", err
			}
			if candidate != resolvedCanonical && !insideCanonical(candidate, resolvedCanonical) {
				roots = append(roots, candidate)
			}
		}
		gitRoots = uniqueStrings(roots)
	}
	toolset, _ := asObject(profile["toolset"])
	toolsetRoot, err := safePath(asStringOr(toolset["root"]))
	if err != nil {
		return "", err
	}
	profileExecutable, err := safePath(asStringOr(profile["executable"]))
	if err != nil {
		return "", err
	}
	sandbox, _ := asObject(profile["sandbox"])
	sandboxExecutable, err := safePath(asStringOr(sandbox["executable"]))
	if err != nil {
		return "", err
	}
	scratchPath, err := safePath(scratch)
	if err != nil {
		return "", err
	}
	configPath, err := safePath(config)
	if err != nil {
		return "", err
	}
	reopened := []string{worker, toolsetRoot, profileExecutable, sandboxExecutable, scratchPath, configPath}
	immutable := append([]string{configPath, toolsetRoot, profileExecutable, sandboxExecutable}, gitRoots...)
	toolsetName := asStringOr(toolset["name"])
	var unicaPaths []string
	if toolsetName == "cc-1c-skills" {
		runtime, _ := asObject(profile["runtime"])
		runtimeExecutable, err := safePath(asStringOr(runtime["executable"]))
		if err != nil {
			return "", err
		}
		reopened = append(reopened, runtimeExecutable)
		immutable = append(immutable, runtimeExecutable)
	} else if toolsetName == "unica" {
		unica, _ := asObject(profile["unica"])
		pluginRoot, err := safePath(asStringOr(unica["plugin_root"]))
		if err != nil {
			return "", err
		}
		runtimeCache, err := safePath(asStringOr(unica["runtime_cache"]))
		if err != nil {
			return "", err
		}
		locks, err := safePath(filepath.Join(runtimeCache, ".locks"))
		if err != nil {
			return "", err
		}
		unicaPaths = []string{pluginRoot, runtimeCache, locks}
		reopened = append(reopened, pluginRoot, runtimeCache, locks)
		immutable = append(immutable, pluginRoot, runtimeCache)
	}
	for _, private := range privateRoots {
		deny := strings.TrimRight(private, `\/`)
		for _, path := range reopened {
			allow := strings.TrimRight(path, `\/`)
			if strings.EqualFold(deny, allow) || overlapsPath(allow, deny) || overlapsPath(deny, allow) {
				return "", invalidf("a reopened execution root overlaps a declared private root.")
			}
		}
	}
	for _, outside := range immutable {
		outsidePath := strings.TrimRight(outside, `\/`)
		for _, writableRoot := range []string{worker, scratchPath} {
			writablePath := strings.TrimRight(writableRoot, `\/`)
			if strings.EqualFold(outsidePath, writablePath) || overlapsPath(outsidePath, writablePath) || overlapsPath(writablePath, outsidePath) {
				return "", invalidf("immutable host inputs overlap writable execution roots.")
			}
		}
	}
	for _, path := range []string{toolsetRoot, profileExecutable, scratchPath, configPath} {
		if strings.EqualFold(path, projectPath) || strings.EqualFold(path, worker) {
			return "", invalidf("host/config/toolset roots must be separate from source and project roots.")
		}
	}
	entries = append(entries, profileEntry{path: worker, access: access})
	for _, gitRoot := range gitRoots {
		entries = append(entries, profileEntry{path: gitRoot, access: "read"})
	}
	entries = append(entries,
		profileEntry{path: toolsetRoot, access: "read"},
		profileEntry{path: profileExecutable, access: "read"},
		profileEntry{path: scratchPath, access: "write"},
		profileEntry{path: configPath, access: "read"})
	if toolsetName == "cc-1c-skills" {
		runtime, _ := asObject(profile["runtime"])
		runtimeExecutable, err := safePath(asStringOr(runtime["executable"]))
		if err != nil {
			return "", err
		}
		entries = append(entries, profileEntry{path: runtimeExecutable, access: "read"})
	} else if toolsetName == "unica" && len(unicaPaths) == 3 {
		entries = append(entries,
			profileEntry{path: unicaPaths[0], access: "read"},
			profileEntry{path: unicaPaths[1], access: "read"},
			profileEntry{path: unicaPaths[2], access: "write"})
	}
	fields := make([]string, 0, len(entries))
	for _, entry := range entries {
		encoded, err := json.Marshal(strings.ReplaceAll(entry.path, `\`, `/`))
		if err != nil {
			return "", invalidf("%v", err)
		}
		fields = append(fields, string(encoded)+`="`+entry.access+`"`)
	}
	return "permissions.bsl_execution={filesystem={" + strings.Join(fields, ",") + "},network={enabled=true}}", nil
}

// overlapsPath reports whether candidate lies at or below root, matching the
// legacy nested-path check for permission entries.
func overlapsPath(candidate, root string) bool {
	candidate = strings.TrimRight(candidate, `\/`)
	root = strings.TrimRight(root, `\/`)
	if strings.EqualFold(candidate, root) {
		return true
	}
	return strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(root+string(filepath.Separator))) ||
		strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(root+"/"))
}

func stringList(value any) []string {
	items, ok := asArray(value)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

// canonicalProbePaths mirrors Get-BFExecutionCanonicalProbePaths: read probes
// use real canonical files when present, writes always target synthetic files.
func canonicalProbePaths(state map[string]any, canonicalStoreRoot string) (map[string]string, error) {
	canonical, err := safePath(canonicalStoreRoot)
	if err != nil {
		return nil, err
	}
	if err := assertUUID(state["task_id"]); err != nil {
		return nil, err
	}
	taskRoot, err := safePath(filepath.Join(canonical, "tasks", asStringOr(state["task_id"])))
	if err != nil {
		return nil, err
	}
	probeRoot, err := safePath(filepath.Join(canonical, "native-provider-probe", asStringOr(state["task_id"])))
	if err != nil {
		return nil, err
	}
	paths := map[string]string{}
	current := filepath.Join(taskRoot, "current.json")
	revisionDirectory := filepath.Join(taskRoot, "revisions")
	revision := ""
	if isDirectory(revisionDirectory) {
		files, err := os.ReadDir(revisionDirectory)
		if err != nil {
			return nil, blockedf("%v", err)
		}
		var names []string
		for _, file := range files {
			if !file.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(file.Name()), ".json") {
				continue
			}
			names = append(names, file.Name())
		}
		sort.Strings(names)
		if len(names) > 0 {
			revision = filepath.Join(revisionDirectory, names[len(names)-1])
		}
	}
	input := filepath.Join(taskRoot, "inputs", "activation.json")
	if !isRegularFile(input) {
		input = filepath.Join(probeRoot, "inputs", "read.json")
	}
	if !isRegularFile(current) {
		current = filepath.Join(probeRoot, "current", "read.json")
	}
	if revision == "" || !isRegularFile(revision) {
		revision = filepath.Join(probeRoot, "revisions", "read.json")
	}
	paths["current_read"] = current
	paths["revision_read"] = revision
	paths["inputs_read"] = input
	paths["current_write"] = filepath.Join(probeRoot, "current", "write.txt")
	paths["revision_write"] = filepath.Join(probeRoot, "revisions", "write.txt")
	paths["inputs_write"] = filepath.Join(probeRoot, "inputs", "write.txt")
	for _, name := range []string{"current_read", "revision_read", "inputs_read", "current_write", "revision_write", "inputs_write"} {
		path, err := safePath(paths[name])
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(path, canonical) && !insideCanonical(path, canonical) {
			return nil, invalidf("canonical capability probe escaped the store: %s", name)
		}
		if !isRegularFile(path) {
			return nil, blockedf("canonical capability probe fixture is missing: %s", name)
		}
		paths[name] = path
	}
	return paths, nil
}

func insideCanonical(path, canonical string) bool {
	return strings.HasPrefix(strings.ToLower(path), strings.ToLower(strings.TrimRight(canonical, `\/`)+string(filepath.Separator)))
}

// executionCapability mirrors Test-BFExecutionCapability.
func executionCapability(ctx context.Context, deps Deps, state map[string]any, directory, scratch, config, permissions string, writable bool, canonicalStoreRoot string) (map[string]any, error) {
	request, _ := asObject(state["request"])
	profile, ok := asObject(request["execution_profile"])
	if !ok {
		return nil, invalidf("execution_profile must be an object.")
	}
	sandbox, _ := asObject(profile["sandbox"])
	sandboxExecutable, err := safePath(asStringOr(sandbox["executable"]))
	if err != nil {
		return nil, err
	}
	worker, err := safePath(asStringOr(state["worker_path"]))
	if err != nil {
		return nil, err
	}
	for _, path := range []string{directory, scratch, config} {
		resolved, err := safePath(path)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(resolved, 0o755); err != nil {
			return nil, blockedf("%v", err)
		}
	}
	directoryPath, err := safePath(directory)
	if err != nil {
		return nil, err
	}
	scratchPath, err := safePath(scratch)
	if err != nil {
		return nil, err
	}
	configPath, err := safePath(config)
	if err != nil {
		return nil, err
	}
	version, err := deps.runProcess(ctx, ProcessOptions{
		Executable:       sandboxExecutable,
		Arguments:        []string{"--version"},
		WorkingDirectory: worker,
		OutputDirectory:  filepath.Join(directoryPath, "sandbox-version"),
		TimeoutSeconds:   30,
	})
	if err != nil {
		return nil, err
	}
	versionBytes, err := repository.StageHostReadFileBytes(version.Stdout)
	if err != nil {
		return nil, blockedf("%v", err)
	}
	if version.ExitCode != 0 || strings.TrimSpace(string(versionBytes)) != "codex-cli 0.154.0" {
		return nil, blockedf("unverified sandbox version.")
	}
	providerVersion, err := deps.runProcess(ctx, ProcessOptions{
		Executable:       asStringOr(profile["executable"]),
		Arguments:        []string{"--version"},
		WorkingDirectory: worker,
		OutputDirectory:  filepath.Join(directoryPath, "provider-version"),
		TimeoutSeconds:   30,
	})
	if err != nil {
		return nil, err
	}
	providerBytes, err := repository.StageHostReadFileBytes(providerVersion.Stdout)
	if err != nil {
		return nil, blockedf("%v", err)
	}
	expectedVersion := "codex-cli 0.154.0"
	if asStringOr(profile["provider"]) == "opencode" {
		expectedVersion = "1.18.30"
	}
	if providerVersion.ExitCode != 0 || strings.TrimSpace(string(providerBytes)) != expectedVersion {
		return nil, blockedf("unverified provider version.")
	}
	configSentinel := filepath.Join(configPath, "sentinel.txt")
	if err := os.WriteFile(configSentinel, []byte("config"), 0o644); err != nil {
		return nil, blockedf("%v", err)
	}
	if _, err := safePath(configSentinel); err != nil {
		return nil, err
	}
	source, err := safePath(filepath.Join(worker, ".bsl-flow-worker", "host-probe.txt"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		return nil, blockedf("%v", err)
	}
	sourceBefore := ""
	sourceBeforeExists := isRegularFile(source)
	if sourceBeforeExists {
		data, err := repository.StageHostReadFileBytes(source)
		if err != nil {
			return nil, blockedf("%v", err)
		}
		sourceBefore = string(data)
	}
	paths := map[string]string{
		"config_read":   configSentinel,
		"config_write":  configSentinel,
		"scratch_write": filepath.Join(scratchPath, "sentinel.txt"),
		"source_write":  source,
	}
	if strings.TrimSpace(canonicalStoreRoot) != "" {
		probePaths, err := canonicalProbePaths(state, canonicalStoreRoot)
		if err != nil {
			return nil, err
		}
		for _, name := range []string{"current", "revision", "inputs"} {
			paths["canonical_"+name+"_read"] = probePaths[name+"_read"]
			paths["canonical_"+name+"_write"] = probePaths[name+"_write"]
		}
	}
	pathsJSON, err := json.Marshal(paths)
	if err != nil {
		return nil, invalidf("%v", err)
	}
	probe, err := deps.runProcess(ctx, ProcessOptions{
		Executable:       sandboxExecutable,
		Arguments:        []string{"sandbox", "-P", "bsl_execution", "-c", permissions, "-c", `windows.sandbox="elevated"`, "-C", worker, deps.SelfPath, "__fs-probe", string(pathsJSON)},
		WorkingDirectory: worker,
		OutputDirectory:  filepath.Join(directoryPath, "filesystem"),
		TimeoutSeconds:   45,
	})
	if err != nil {
		return nil, err
	}
	if probe.ExitCode != 0 || probe.StopReason != "" {
		return nil, blockedf("managed filesystem capability did not finish.")
	}
	probeBytes, err := repository.StageHostReadFileBytes(probe.Stdout)
	if err != nil {
		return nil, blockedf("%v", err)
	}
	actual, err := repository.DecodeObject(probeBytes)
	if err != nil {
		return nil, invalidf("Cannot materialize JSON object: %v", err)
	}
	expected := map[string]any{
		"config_read":   "allowed",
		"config_write":  "denied",
		"scratch_write": "allowed",
		"source_write":  "denied",
	}
	if writable {
		expected["source_write"] = "allowed"
	}
	if strings.TrimSpace(canonicalStoreRoot) != "" {
		for _, name := range []string{"current", "revision", "inputs"} {
			expected["canonical_"+name+"_read"] = "denied"
			expected["canonical_"+name+"_write"] = "denied"
		}
	}
	actualHash, err := hashValue(actual)
	if err != nil {
		return nil, err
	}
	expectedHash, err := hashValue(expected)
	if err != nil {
		return nil, err
	}
	sentinelBytes, err := repository.StageHostReadFileBytes(configSentinel)
	if err != nil {
		return nil, blockedf("%v", err)
	}
	if actualHash != expectedHash || string(sentinelBytes) != "config" {
		return nil, blockedf("managed filesystem capability differs from the required observations.")
	}
	// Source probe restoration mirrors Test-BFExecutionCapability's tail: a
	// writable probe must demonstrate its write and be rolled back, and a
	// read-only probe must leave the source tree byte-identical.
	if sourceBeforeExists {
		afterBytes, err := repository.StageHostReadFileBytes(source)
		sourceAfter := ""
		if err == nil {
			sourceAfter = string(afterBytes)
		}
		if writable {
			if sourceAfter != "probe" {
				return nil, blockedf("writable source capability probe did not write the probe file.")
			}
			if err := os.WriteFile(source, []byte(sourceBefore), 0o644); err != nil {
				return nil, blockedf("%v", err)
			}
		} else if sourceAfter != sourceBefore {
			return nil, blockedf("read-only source capability probe changed an existing protected probe file.")
		}
	} else if !writable && isRegularFile(source) {
		return nil, blockedf("read-only source capability probe created a file.")
	} else if writable && isRegularFile(source) {
		if err := os.Remove(source); err != nil {
			return nil, blockedf("%v", err)
		}
	}
	permissionsHash, err := hashValue(permissions)
	if err != nil {
		return nil, err
	}
	capability := map[string]any{
		"observations":       actual,
		"permissions_sha256": permissionsHash,
		"sandbox_sha256":     sandbox["sha256"],
		"provider_sha256":    profile["executable_sha256"],
		"network":            "not_probed",
		"database":           "not_accessed",
	}
	if err := writeJSON(filepath.Join(directoryPath, "capability.json"), capability, false); err != nil {
		return nil, err
	}
	return capability, nil
}
