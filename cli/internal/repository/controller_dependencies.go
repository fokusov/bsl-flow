package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// currentNativeDependencies mirrors Get-BFDependencies in Task.Gates.ps1.
// It deliberately performs only read-only binding: the returned object is a
// dependency snapshot and is not a controller transition or an authorization.
func currentNativeDependencies(payload map[string]any, stage string, manifest map[string]any) (map[string]any, error) {
	request, ok := payload["request"].(map[string]any)
	if !ok || request == nil {
		return nil, invalid("native dependency state request must be an object")
	}

	inputs := map[string]any{
		"intent": payload["intent_hash"],
		"policy": payload["policy_hash"],
	}
	if profile, present := request["execution_profile"]; present && profile != nil {
		execution, err := currentNativeExecutionDependencies(payload)
		if err != nil {
			return nil, err
		}
		executionHash, err := Hash(execution)
		if err != nil {
			return nil, err
		}
		inputs["execution"] = executionHash
	}

	packageRoot, err := nativeDependencyPackageRoot(payload)
	if err != nil {
		return nil, err
	}
	architectureRoot, err := nativeArchitectureContextRoot(payload, packageRoot)
	if err != nil {
		return nil, err
	}
	architectureHash, err := nativeArchitectureBundleHash(stage, architectureRoot, packageRoot)
	if err != nil {
		return nil, err
	}
	inputs["architecture"] = architectureHash

	if stage == "inspect" {
		inputs["baseline"] = payload["baseline"]
	}
	if stage != "inspect" {
		classificationHash, err := Hash(payload["classification"])
		if err != nil {
			return nil, err
		}
		inputs["classification"] = classificationHash
	}
	if nativeDependencySpecStages[stage] {
		specInputs, err := nativeDependencySpecInputs(payload)
		if err != nil {
			return nil, err
		}
		specHash, err := Hash(specInputs)
		if err != nil {
			return nil, err
		}
		inputs["spec"] = specHash
	}

	if nativeDependencySourceStages[stage] {
		if manifest == nil {
			worker := asStringOr(payload["worker_path"])
			baseline := asStringOr(payload["baseline"])
			manifest, err = sourceManifestWithBaseline(worker, baseline, []string{"."})
			if err != nil {
				return nil, err
			}
		}
		inputs["source"] = manifest["sha256"]
		criteria := request["criteria"]
		criteriaHash, err := Hash(criteria)
		if err != nil {
			return nil, err
		}
		inputs["criteria"] = criteriaHash
		if _, present := request["requirements"]; present {
			requirementsHash, err := Hash(request["requirements"])
			if err != nil {
				return nil, err
			}
			inputs["requirements"] = requirementsHash
		}
		for _, raw := range anyItems(request["criteria"]) {
			criterion, ok := raw.(map[string]any)
			if !ok || criterion == nil {
				continue
			}
			if native, present := criterion["native_1c"]; present && native != nil {
				return nil, blocked("native_1c criteria require an unsupported runtime capability")
			}
		}
		inputs["correction_round"] = payload["correction_rounds"]
		if asIntOr(request["max_source_repairs"]) > 0 {
			inputs["repair_round"] = asIntOr(asMap(payload["repair"])["rounds"])
			executableRows := make([]any, 0)
			for _, raw := range anyItems(request["criteria"]) {
				criterion, ok := raw.(map[string]any)
				if !ok || criterion == nil {
					continue
				}
				kind := asStringOr(criterion["kind"])
				if kind != "static" && kind != "unit" {
					continue
				}
				executable, ok := asString(criterion["executable"])
				if !ok || strings.TrimSpace(executable) == "" {
					return nil, invalid("criterion executable is required for test executable dependencies")
				}
				safe, err := SafePath(executable)
				if err != nil {
					return nil, err
				}
				data, err := ReadFileBytes(safe)
				if err != nil {
					return nil, blocked("test executable cannot be read: %v", err)
				}
				executableRows = append(executableRows, map[string]any{"path": executable, "sha256": fileSHA256(data)})
			}
			testExecutablesHash, err := Hash(executableRows)
			if err != nil {
				return nil, err
			}
			inputs["test_executables"] = testExecutablesHash
		}
	}
	if stage == "spec_review" {
		sidecars := map[string]any{}
		for _, name := range []string{"review.json", "review-reconciliation.json", "final-validation.json"} {
			path := filepath.Join(nativeDependencyChangePath(payload), name)
			data, err := nativeDependencyOptionalFile(path)
			if err != nil {
				return nil, err
			}
			sidecars[name] = data
		}
		// Replace optional-file values with their hashes/nulls. The helper above
		// returns the hash as a string and nil when a sidecar is absent.
		reviewHash, err := Hash(sidecars)
		if err != nil {
			return nil, err
		}
		inputs["review_binding"] = reviewHash
	}
	if stage == "diagnose" {
		inputs["failure_attempt"] = asMap(payload["repair"])["pending_failure"]
	}
	return inputs, nil
}

var (
	nativeDependencySpecStages   = map[string]bool{"spec": true, "spec_review": true, "implement": true, "code_review": true, "verify": true, "diagnose": true, "acceptance": true}
	nativeDependencySourceStages = map[string]bool{"implement": true, "code_review": true, "verify": true, "diagnose": true, "acceptance": true}
)

func nativeDependencyOptionalFile(path string) (any, error) {
	safe, err := SafePath(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(safe)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, blocked("cannot inspect dependency sidecar: %v", err)
	}
	if !info.Mode().IsRegular() {
		return nil, blocked("dependency sidecar is not a regular file: %s", path)
	}
	data, err := ReadFileBytes(safe)
	if err != nil {
		return nil, blocked("dependency sidecar cannot be read: %v", err)
	}
	return fileSHA256(data), nil
}

func nativeDependencyChangePath(payload map[string]any) string {
	request, _ := payload["request"].(map[string]any)
	project := asStringOr(payload["project_path"])
	taskID := asStringOr(request["request_id"])
	return filepath.Join(project, "openspec", "changes", "bsl-flow-"+taskID)
}

func nativeDependencySpecInputs(payload map[string]any) (map[string]any, error) {
	request, ok := payload["request"].(map[string]any)
	if !ok || request == nil {
		return nil, invalid("native dependency state request must be an object")
	}
	project := asStringOr(payload["project_path"])
	taskID := asStringOr(request["request_id"])
	if _, err := SafePath(project); err != nil {
		return nil, err
	}
	if !isUUID(taskID) {
		return nil, invalid("task id must be a lowercase UUID")
	}
	changeRoot := nativeDependencyChangePath(payload)
	if _, err := SafePath(changeRoot); err != nil {
		return nil, err
	}
	result := map[string]any{}
	for _, name := range []string{"original-task.md", "spec.md", "design.md"} {
		path := filepath.Join(changeRoot, name)
		data, err := nativeDependencyOptionalFile(path)
		if err != nil {
			return nil, err
		}
		result[name] = data
	}
	return result, nil
}

func nativeDependencyPackageRoot(payload map[string]any) (string, error) {
	files, ok := nativeItems(payload["policy_files"])
	if !ok || len(files) == 0 {
		return "", blocked("incomplete native policy inventory")
	}
	const suffix = "/global/skills/1c-task/scripts/task.provider.ps1"
	matches := make([]string, 0, 1)
	for _, raw := range files {
		entry, err := nativeObject(raw, []string{"path"}, []string{"sha256", "size_bytes"}, "policy file")
		if err != nil {
			return "", err
		}
		path, ok := asString(entry["path"])
		if !ok || strings.TrimSpace(path) == "" {
			return "", invalid("policy file path is invalid")
		}
		normalized := strings.ToLower(strings.TrimRight(filepath.ToSlash(path), "/"))
		if !strings.HasSuffix(normalized, suffix) {
			continue
		}
		safe, err := SafePath(path)
		if err != nil {
			return "", err
		}
		data, err := ReadFileBytes(safe)
		if err != nil {
			return "", blocked("provider entrypoint cannot be read: %v", err)
		}
		if listed, present := entry["sha256"]; present && listed != nil {
			hash, ok := asString(listed)
			if !ok || !isSHA256(hash) || fileSHA256(data) != hash {
				return "", blocked("provider entrypoint bytes do not match the policy inventory")
			}
		}
		matches = append(matches, safe)
	}
	if len(matches) != 1 {
		return "", blocked("native policy must contain exactly one Task.Provider.ps1 entrypoint")
	}
	root := matches[0]
	// Task.Provider.ps1 lives at <package>/global/skills/1c-task/scripts.
	// Walk past `global` as well so the architecture fallback and subject
	// references resolve against the package root, matching
	// Get-BFArchitectureRoot in Task.Architecture.ps1.
	for i := 0; i < 5; i++ {
		root = filepath.Dir(root)
	}
	return SafePath(root)
}

func currentNativeExecutionDependencies(payload map[string]any) (map[string]any, error) {
	request, ok := payload["request"].(map[string]any)
	if !ok || request == nil {
		return nil, invalid("native dependency state request must be an object")
	}
	profile, err := nativeObject(request["execution_profile"], []string{"provider", "executable", "executable_sha256", "sandbox", "toolset", "denied_read_roots"}, []string{"unica", "codex_skills_sha256", "runtime"}, "execution_profile")
	if err != nil {
		return nil, err
	}
	if err := validateNativeExecutionProfile(profile); err != nil {
		return nil, err
	}
	for _, field := range []string{"executable", "sandbox"} {
		value := profile[field]
		if field == "sandbox" {
			value = asMap(value)["executable"]
		}
		path, ok := asString(value)
		if !ok {
			return nil, invalid("execution profile %s executable is invalid", field)
		}
		data, err := ReadFileBytes(path)
		if err != nil {
			return nil, blocked("execution profile %s executable cannot be read: %v", field, err)
		}
		expected := asStringOr(profile["executable_sha256"])
		if field == "sandbox" {
			expected = asStringOr(asMap(profile["sandbox"])["sha256"])
		}
		if fileSHA256(data) != expected {
			return nil, blocked("execution profile %s executable changed", field)
		}
	}
	snapshot, err := currentNativeToolsetSnapshot(profile)
	if err != nil {
		return nil, err
	}
	if asStringOr(snapshot["aggregate_sha256"]) != asStringOr(asMap(profile["toolset"])["sha256"]) {
		return nil, blocked("toolset differs from the registered snapshot")
	}
	toolsetName := asStringOr(asMap(profile["toolset"])["name"])
	if toolsetName == "cc-1c-skills" {
		runtime := asMap(profile["runtime"])
		data, err := ReadFileBytes(asStringOr(runtime["executable"]))
		if err != nil {
			return nil, blocked("pinned runtime executable cannot be read: %v", err)
		}
		if fileSHA256(data) != asStringOr(runtime["sha256"]) {
			return nil, blocked("pinned cc-1c-skills runtime executable changed")
		}
	}
	if toolsetName == "unica" {
		unica := asMap(profile["unica"])
		pluginRoot := asStringOr(unica["plugin_root"])
		bootstrap := filepath.Join(pluginRoot, "bootstrap", "bin", "win-x64", "unica-bootstrap.exe")
		manifestPath := filepath.Join(pluginRoot, "runtime-manifest.json")
		bootstrapData, err := ReadFileBytes(bootstrap)
		if err != nil {
			return nil, blocked("Unica bootstrap cannot be read: %v", err)
		}
		manifestData, err := ReadFileBytes(manifestPath)
		if err != nil {
			return nil, blocked("Unica runtime manifest cannot be read: %v", err)
		}
		if fileSHA256(bootstrapData) != asStringOr(unica["bootstrap_sha256"]) || fileSHA256(manifestData) != asStringOr(unica["manifest_sha256"]) {
			return nil, blocked("Unica bootstrap or manifest changed")
		}
		runtimeManifest, err := DecodeObject(manifestData)
		if err != nil {
			return nil, blocked("Unica runtime manifest is invalid: %v", err)
		}
		if asStringOr(runtimeManifest["pluginVersion"]) != "0.12.3" {
			return nil, blocked("unverified Unica runtime version")
		}
		targets := asMap(runtimeManifest["targets"])
		win := asMap(targets["win-x64"])
		for _, raw := range anyItems(win["files"]) {
			file, err := nativeObject(raw, []string{"path", "sha256"}, nil, "Unica runtime file")
			if err != nil {
				return nil, err
			}
			relative, ok := asString(file["path"])
			if !ok || validateRelativeNativePath(relative, false) != nil {
				return nil, invalid("Unica runtime manifest contains an unsafe file path")
			}
			path := filepath.Join(asStringOr(unica["runtime_cache"]), "0.12.3", "win-x64", filepath.FromSlash(relative))
			data, err := ReadFileBytes(path)
			if err != nil {
				return nil, blocked("pinned Unica runtime file cannot be read: %v", err)
			}
			if fileSHA256(data) != asStringOr(file["sha256"]) {
				return nil, blocked("pinned Unica runtime file differs")
			}
		}
	}
	return map[string]any{"profile": profile, "models": request["models"]}, nil
}

func currentNativeToolsetSnapshot(profile map[string]any) (map[string]any, error) {
	toolset := asMap(profile["toolset"])
	root, err := SafePath(asStringOr(toolset["root"]))
	if err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(root, "toolset-manifest.json")
	manifestData, err := ReadFileBytes(manifestPath)
	if err != nil {
		return nil, blocked("toolset snapshot manifest cannot be read: %v", err)
	}
	manifest, err := DecodeObject(manifestData)
	if err != nil {
		return nil, blocked("toolset snapshot manifest is invalid: %v", err)
	}
	if _, err := nativeObject(manifest, []string{"schema_version", "toolset_name", "source", "skills", "aggregate_sha256"}, nil, "toolset snapshot manifest"); err != nil {
		return nil, err
	}
	if asIntOr(manifest["schema_version"]) != 1 || asStringOr(manifest["toolset_name"]) != asStringOr(toolset["name"]) || !isSHA256(asStringOr(manifest["aggregate_sha256"])) {
		return nil, blocked("toolset snapshot manifest has an invalid identity or aggregate hash")
	}
	if _, err := nativeObject(manifest["source"], []string{"identity", "path"}, nil, "toolset manifest source"); err != nil {
		return nil, err
	}
	source := asMap(manifest["source"])
	if asStringOr(source["identity"]) != "local-private" || strings.TrimSpace(asStringOr(source["path"])) == "" {
		return nil, invalid("toolset snapshot manifest source is invalid")
	}
	skills, err := nativeArray(manifest["skills"], "toolset manifest skills", true)
	if err != nil {
		return nil, err
	}
	expectedFiles := map[string]bool{}
	for _, raw := range skills {
		skill, err := nativeObject(raw, []string{"name", "files", "sha256", "mcp_references"}, nil, "toolset manifest skill")
		if err != nil {
			return nil, err
		}
		name := asStringOr(skill["name"])
		if !nativeToolsetSkillName(name) || !isSHA256(asStringOr(skill["sha256"])) {
			return nil, invalid("toolset manifest contains an invalid skill")
		}
		files, err := nativeArray(skill["files"], "toolset manifest skill files", true)
		if err != nil {
			return nil, err
		}
		if _, err := nativeArray(skill["mcp_references"], "toolset manifest MCP references", false); err != nil {
			return nil, err
		}
		rebuiltFiles := make([]any, 0, len(files))
		for _, rawFile := range files {
			file, err := nativeObject(rawFile, []string{"path", "sha256"}, nil, "toolset manifest file")
			if err != nil {
				return nil, err
			}
			relative := asStringOr(file["path"])
			if !nativeToolsetManifestPath(relative) || nativeToolsetForbiddenAsset(relative) || !isSHA256(asStringOr(file["sha256"])) {
				return nil, invalid("toolset manifest contains an invalid or forbidden asset")
			}
			key := name + "/" + relative
			if expectedFiles[key] {
				return nil, invalid("toolset manifest repeats file %s", key)
			}
			expectedFiles[key] = true
			rebuiltFiles = append(rebuiltFiles, map[string]any{"path": relative, "sha256": file["sha256"]})
		}
		skillHash, err := nativeToolsetAggregateHash([]any{map[string]any{"name": name, "files": rebuiltFiles}})
		if err != nil {
			return nil, err
		}
		if skillHash != asStringOr(skill["sha256"]) {
			return nil, blocked("toolset skill hash differs: %s", name)
		}
		if err := nativeValidateMCPReferences(skill["mcp_references"]); err != nil {
			return nil, err
		}
	}
	actualFiles := map[string]bool{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
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
			return blocked("toolset snapshot contains a non-regular file")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative != "toolset-manifest.json" {
			actualFiles[relative] = true
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if len(actualFiles) != len(expectedFiles) {
		return nil, blocked("toolset snapshot tree differs from its manifest")
	}
	for path := range expectedFiles {
		if !actualFiles[path] {
			return nil, blocked("toolset snapshot tree differs from its manifest")
		}
	}

	rebuiltSkills := make([]any, 0, len(skills))
	for _, raw := range skills {
		skill := asMap(raw)
		name := asStringOr(skill["name"])
		files := make([]any, 0)
		for _, rawFile := range anyItems(skill["files"]) {
			file := asMap(rawFile)
			relative := asStringOr(file["path"])
			path := filepath.Join(root, name, filepath.FromSlash(relative))
			data, err := ReadFileBytes(path)
			if err != nil {
				return nil, blocked("toolset snapshot file is missing: %s/%s", name, relative)
			}
			if fileSHA256(data) != asStringOr(file["sha256"]) {
				return nil, blocked("toolset snapshot file hash differs: %s/%s", name, relative)
			}
			files = append(files, map[string]any{"path": relative, "sha256": file["sha256"]})
		}
		mcp, err := nativeToolsetMCPReferences(filepath.Join(root, name))
		if err != nil {
			return nil, err
		}
		if !equalJSON(mcp, skill["mcp_references"]) {
			return nil, blocked("toolset MCP text inventory differs: %s", name)
		}
		rebuiltSkills = append(rebuiltSkills, map[string]any{"name": name, "files": files})
	}
	aggregate, err := nativeToolsetAggregateHash(rebuiltSkills)
	if err != nil {
		return nil, err
	}
	if aggregate != asStringOr(manifest["aggregate_sha256"]) {
		return nil, blocked("toolset snapshot aggregate hash differs")
	}
	return map[string]any{"aggregate_sha256": aggregate}, nil
}

func nativeToolsetSkillName(name string) bool {
	if strings.TrimSpace(name) == "" || name == "." || name == ".." || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	if strings.ContainsAny(name, "<>:\"/\\|?*") {
		return false
	}
	for _, r := range name {
		if r < 0x20 {
			return false
		}
	}
	lower := strings.ToLower(name)
	if lower == "con" || lower == "prn" || lower == "aux" || lower == "nul" {
		return false
	}
	if len(lower) == 4 && (strings.HasPrefix(lower, "com") || strings.HasPrefix(lower, "lpt")) && lower[3] >= '1' && lower[3] <= '9' {
		return false
	}
	return true
}

func nativeToolsetManifestPath(path string) bool {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "\\") {
		return false
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

var nativeToolsetForbiddenAssetPattern = regexp.MustCompile(`(?i)^(?:\.env(?:\..*)?|id_(?:rsa|dsa|ecdsa|ed25519)|.*(?:private[-_]?key|credential|secret).*|(?:auth|oauth|credentials?)\.(?:json|ya?ml|ini|config|txt))$`)

func nativeToolsetForbiddenAsset(path string) bool {
	return nativeToolsetForbiddenAssetPattern.MatchString(filepath.Base(filepath.FromSlash(path)))
}

func nativeValidateMCPReferences(value any) error {
	items, err := nativeArray(value, "toolset manifest MCP references", false)
	if err != nil {
		return err
	}
	for _, raw := range items {
		ref, err := nativeObject(raw, []string{"name", "evidence_files"}, nil, "toolset manifest MCP reference")
		if err != nil {
			return err
		}
		name := asStringOr(ref["name"])
		if !regexp.MustCompile(`^(?:mcp__[a-z0-9_]+|unica\.[a-z0-9][a-z0-9.-]*)$`).MatchString(name) {
			return invalid("toolset manifest contains an invalid MCP reference")
		}
		files, err := nativeArray(ref["evidence_files"], "toolset MCP evidence files", true)
		if err != nil {
			return err
		}
		for _, rawFile := range files {
			path, ok := asString(rawFile)
			if !ok || !nativeToolsetManifestPath(path) {
				return invalid("toolset MCP evidence path is invalid")
			}
		}
	}
	return nil
}

var nativeToolsetMCPPattern = regexp.MustCompile(`(?i)\b(?:mcp__[a-z0-9_]+|unica\.[a-z0-9][a-z0-9.-]*)\b`)

func nativeToolsetMCPReferences(skillRoot string) ([]any, error) {
	root, err := SafePath(skillRoot)
	if err != nil {
		return nil, err
	}
	type evidence struct {
		name  string
		files map[string]bool
	}
	found := map[string]*evidence{}
	paths := make([]string, 0)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
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
			return blocked("toolset skill tree contains a non-regular file")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if nativeToolsetForbiddenAsset(relative) {
			return blocked("refusing obvious secret or authentication asset: %s", relative)
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".md" || ext == ".ps1" || ext == ".txt" || ext == ".json" || ext == ".yaml" || ext == ".yml" {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Slice(paths, func(i, j int) bool {
		left, _ := filepath.Rel(root, paths[i])
		right, _ := filepath.Rel(root, paths[j])
		return filepath.ToSlash(left) < filepath.ToSlash(right)
	})
	for _, path := range paths {
		data, err := ReadFileBytes(path)
		if err != nil {
			return nil, err
		}
		relative, _ := filepath.Rel(root, path)
		relative = filepath.ToSlash(relative)
		for _, match := range nativeToolsetMCPPattern.FindAllString(string(data), -1) {
			name := strings.ToLower(match)
			entry := found[name]
			if entry == nil {
				entry = &evidence{name: name, files: map[string]bool{}}
				found[name] = entry
			}
			entry.files[relative] = true
		}
	}
	names := make([]string, 0, len(found))
	for name := range found {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]any, 0, len(names))
	for _, name := range names {
		files := make([]string, 0, len(found[name].files))
		for path := range found[name].files {
			files = append(files, path)
		}
		sort.Strings(files)
		result = append(result, map[string]any{"name": name, "evidence_files": toAnySlice(files)})
	}
	return result, nil
}

func nativeToolsetAggregateHash(skills []any) (string, error) {
	lines := make([]string, 0)
	for _, raw := range skills {
		skill := asMap(raw)
		name := asStringOr(skill["name"])
		for _, rawFile := range anyItems(skill["files"]) {
			file := asMap(rawFile)
			lines = append(lines, name+"/"+asStringOr(file["path"])+"\x00"+asStringOr(file["sha256"])+"\n")
		}
	}
	sort.Strings(lines)
	return hashUTF8(strings.Join(lines, "")), nil
}

func hashUTF8(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func nativeArchitectureContextRoot(payload map[string]any, packageRoot string) (string, error) {
	project := asStringOr(payload["project_path"])
	if project != "" {
		projectSafe, err := SafePath(project)
		if err != nil {
			return "", err
		}
		candidate := filepath.Join(projectSafe, "docs", "architecture", "adr-index.json")
		if nativeDependencyRegularFile(candidate) {
			return projectSafe, nil
		}
	}
	packageSafe, err := SafePath(packageRoot)
	if err != nil {
		return "", err
	}
	if nativeDependencyRegularFile(filepath.Join(packageSafe, "docs", "architecture", "adr-index.json")) {
		return packageSafe, nil
	}
	if project != "" {
		return SafePath(project)
	}
	return packageSafe, nil
}

func nativeDependencyRegularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

var nativeArchitectureSubjects = map[string][]string{
	"inspect":        {"controller.state", "controller.gates"},
	"spec":           {"controller.gates"},
	"spec_review":    {"controller.gates", "adapter.codex", "adapter.opencode"},
	"spec_reconcile": {"controller.gates", "adapter.codex", "adapter.opencode"},
	"implement":      {"controller.gates", "controller.execution"},
	"code_review":    {"controller.gates", "adapter.codex", "adapter.opencode"},
	"code_reconcile": {"controller.gates", "adapter.codex", "adapter.opencode"},
	"verify":         {"controller.gates", "runtime.native-1c"},
	"diagnose":       {"controller.recovery", "controller.gates"},
	"acceptance":     {"controller.gates", "publication.git"},
}

type nativeArchitectureIndex struct {
	value     map[string]any
	subjects  map[string]map[string]any
	decisions map[string]map[string]any
}

func nativeArchitectureBundleHash(stage, root, packageRoot string) (string, error) {
	content, err := nativeArchitectureBundleContent(stage, root, packageRoot)
	if err != nil {
		return "", err
	}
	return Hash(content)
}

// nativeArchitectureBundleContent mirrors Get-BFArchitectureBundle: the full
// deterministic stage bundle before bundle_sha256 is folded in. The stage
// prompt renders this content through Format-BFArchitectureBundlePrompt, so
// the prompt and the dependency hash always derive from one selection.
func nativeArchitectureBundleContent(stage, root, packageRoot string) (map[string]any, error) {
	root, err := SafePath(root)
	if err != nil {
		return nil, err
	}
	packageRoot, err = SafePath(packageRoot)
	if err != nil {
		return nil, err
	}
	subjects := nativeArchitectureSubjects[stage]
	if subjects == nil {
		subjects = []string{}
	}
	missing := []any{}
	subjectRefs := []any{}
	records := []map[string]any{}
	indexPath := filepath.Join(root, "docs", "architecture", "adr-index.json")
	var index *nativeArchitectureIndex
	if nativeDependencyRegularFile(indexPath) {
		index, err = nativeReadArchitectureIndex(root, packageRoot)
		if err != nil {
			return nil, err
		}
		for _, subject := range subjects {
			definition, ok := index.subjects[subject]
			if !ok {
				subjectRefs = append(subjectRefs, map[string]any{"id": subject, "kind": "missing", "refs": []any{}})
				missing = append(missing, "subject:"+subject)
				continue
			}
			subjectRefs = append(subjectRefs, map[string]any{"id": definition["id"], "kind": definition["kind"], "refs": definition["refs"]})
		}
		for _, decision := range index.decisions {
			if asStringOr(decision["status"]) != "accepted" || !nativeArchitectureApplies(decision, subjects) {
				continue
			}
			sectionHash, excerpt, err := nativeArchitectureDecisionText(root, decision)
			if err != nil {
				return nil, err
			}
			records = append(records, map[string]any{
				"id": decision["id"], "title": decision["title"], "status": decision["status"],
				"source": decision["source"], "section_sha256": sectionHash, "excerpt": excerpt,
			})
		}
		sort.Slice(records, func(i, j int) bool { return asStringOr(records[i]["id"]) < asStringOr(records[j]["id"]) })
	}
	if index == nil {
		// Get-BFArchitectureBundle deliberately degrades only the missing index
		// case. Keep this marker in the hashed content so a later index creation
		// invalidates any evidence that was bound without it.
		missing = append(missing, "adr-index")
	}
	presentation := append([]map[string]any(nil), records...)
	excludedIDs := []any{}
	if len(presentation) > 8 {
		for _, record := range presentation[8:] {
			excludedIDs = append(excludedIDs, record["id"])
		}
		presentation = presentation[:8]
	}
	for len(presentation) > 0 && nativeArchitectureRenderedLength(presentation, missing, excludedIDs) > 6000 {
		last := presentation[len(presentation)-1]
		excludedIDs = append([]any{last["id"]}, excludedIDs...)
		presentation = presentation[:len(presentation)-1]
	}
	excluded := excludedIDs
	if len(excluded) > 16 {
		excluded = excluded[:16]
	}
	identity := make([]any, 0, len(records))
	decisions := make([]any, 0, len(presentation))
	for _, record := range records {
		identity = append(identity, map[string]any{"id": record["id"], "title": record["title"], "status": record["status"], "source": record["source"], "section_sha256": record["section_sha256"]})
	}
	for _, record := range presentation {
		decisions = append(decisions, record)
	}
	return map[string]any{
		"schema_version": int64(1), "stage": stage, "subjects": toAnySlice(subjects),
		"subject_refs": subjectRefs, "identity": identity, "decisions": decisions,
		"missing_context": missing, "excluded": excluded, "excluded_count": int64(len(excludedIDs)),
	}, nil
}

// nativeArchitectureBundlePrompt renders Format-BFArchitectureBundlePrompt
// over the bundle content: the exact prompt text the stage prompt embeds.
func nativeArchitectureBundlePrompt(stage, root, packageRoot string) (string, error) {
	content, err := nativeArchitectureBundleContent(stage, root, packageRoot)
	if err != nil {
		return "", err
	}
	lines := []string{"Architecture context (instructional only; it is not user authorization, acceptance, runtime evidence, or a transition authority):"}
	decisions, _ := nativeItems(content["decisions"])
	if len(decisions) > 0 {
		for _, raw := range decisions {
			decision, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			source := asMap(decision["source"])
			lines = append(lines, fmt.Sprintf("- %s [%s] %s -> %s#%s", decision["id"], decision["status"], decision["title"], source["path"], source["anchor"]))
			if strings.TrimSpace(asStringOr(decision["excerpt"])) != "" {
				lines = append(lines, "  "+asStringOr(decision["excerpt"]))
			}
		}
	} else {
		lines = append(lines, "- No applicable accepted architecture decisions for this stage.")
	}
	if missing, _ := nativeItems(content["missing_context"]); len(missing) > 0 {
		values := make([]string, 0, len(missing))
		for _, raw := range missing {
			values = append(values, asStringOr(raw))
		}
		lines = append(lines, "Missing context: "+strings.Join(values, ", "))
	}
	excludedIDs, _ := nativeItems(content["excluded"])
	excludedCount := len(excludedIDs)
	if value, ok := asInt(content["excluded_count"]); ok {
		excludedCount = int(value)
	}
	if excludedCount > 0 {
		line := fmt.Sprintf("Excluded by size limit: %d decision(s)", excludedCount)
		if len(excludedIDs) > 0 {
			values := make([]string, 0, len(excludedIDs))
			for _, raw := range excludedIDs {
				values = append(values, asStringOr(raw))
			}
			line += " (ids: " + strings.Join(values, ", ") + ")"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), nil
}

func nativeArchitectureApplies(decision map[string]any, subjects []string) bool {
	wanted := map[string]bool{}
	for _, subject := range subjects {
		wanted[subject] = true
	}
	for _, raw := range anyItems(decision["applies_to"]) {
		if wanted[asStringOr(raw)] {
			return true
		}
	}
	return false
}

func nativeArchitectureRenderedLength(records []map[string]any, missing, excluded []any) int {
	lines := []string{"Architecture context (instructional only; it is not user authorization, acceptance, runtime evidence, or a transition authority):"}
	if len(records) > 0 {
		for _, record := range records {
			source := asMap(record["source"])
			lines = append(lines, fmt.Sprintf("- %s [%s] %s -> %s#%s", record["id"], record["status"], record["title"], source["path"], source["anchor"]))
			if strings.TrimSpace(asStringOr(record["excerpt"])) != "" {
				lines = append(lines, "  "+asStringOr(record["excerpt"]))
			}
		}
	} else {
		lines = append(lines, "- No applicable accepted architecture decisions for this stage.")
	}
	if len(missing) > 0 {
		values := make([]string, 0, len(missing))
		for _, raw := range missing {
			values = append(values, asStringOr(raw))
		}
		lines = append(lines, "Missing context: "+strings.Join(values, ", "))
	}
	if len(excluded) > 0 {
		// The renderer reports the complete excluded count but only includes the
		// first 16 IDs, exactly as Format-BFArchitectureBundle does.
		sample := excluded
		if len(sample) > 16 {
			sample = sample[:16]
		}
		values := make([]string, 0, len(sample))
		for _, raw := range sample {
			values = append(values, asStringOr(raw))
		}
		line := fmt.Sprintf("Excluded by size limit: %d decision(s)", len(excluded))
		if len(values) > 0 {
			line += " (ids: " + strings.Join(values, ", ") + ")"
		}
		lines = append(lines, line)
	}
	return len(strings.Join(lines, "\n"))
}

func nativeReadArchitectureIndex(root, packageRoot string) (*nativeArchitectureIndex, error) {
	indexPath := filepath.Join(root, "docs", "architecture", "adr-index.json")
	indexData, err := ReadFileBytes(indexPath)
	if err != nil {
		return nil, blocked("ADR index cannot be read: %v", err)
	}
	indexValue, err := DecodeObject(indexData)
	if err != nil {
		return nil, invalid("ADR index is not valid JSON: %v", err)
	}
	if _, err := nativeObject(indexValue, []string{"schema_version", "subjects", "decisions"}, nil, "ADR index"); err != nil {
		return nil, err
	}
	if asIntOr(indexValue["schema_version"]) != 1 {
		return nil, invalid("ADR index schema_version is invalid")
	}
	if !nativeDependencyRegularFile(filepath.Join(root, "docs", "architecture", "adr-index.schema.json")) {
		return nil, invalid("ADR index schema is missing")
	}
	subjects, err := nativeArray(indexValue["subjects"], "ADR subjects", true)
	if err != nil {
		return nil, err
	}
	decisions, err := nativeArray(indexValue["decisions"], "ADR decisions", true)
	if err != nil {
		return nil, err
	}
	result := &nativeArchitectureIndex{value: indexValue, subjects: map[string]map[string]any{}, decisions: map[string]map[string]any{}}
	for _, raw := range subjects {
		subject, err := nativeObject(raw, []string{"id", "kind", "refs"}, nil, "ADR subject")
		if err != nil {
			return nil, err
		}
		id := asStringOr(subject["id"])
		if !nativeArchitectureID(id) || result.subjects[id] != nil {
			return nil, invalid("ADR subject id is invalid or duplicated")
		}
		if kind := asStringOr(subject["kind"]); kind != "module" && kind != "command" && kind != "function" && kind != "document" {
			return nil, invalid("ADR subject kind is invalid")
		}
		refs, err := nativeStringArrayBounded(subject["refs"], "ADR subject refs", 240, true)
		if err != nil {
			return nil, err
		}
		for _, reference := range refs {
			if err := nativeValidateArchitectureReference(reference, root, packageRoot); err != nil {
				return nil, err
			}
		}
		subject["refs"] = toAnySlice(refs)
		result.subjects[id] = subject
	}
	for _, raw := range decisions {
		decision, err := nativeObject(raw, []string{"id", "title", "status", "applies_to", "source", "informed_by", "supersedes"}, []string{"revisit_triggers"}, "ADR decision")
		if err != nil {
			return nil, err
		}
		id := asStringOr(decision["id"])
		if !regexp.MustCompile(`^ADR-[0-9]+$`).MatchString(id) || result.decisions[id] != nil {
			return nil, invalid("ADR decision id is invalid or duplicated")
		}
		if strings.TrimSpace(asStringOr(decision["title"])) == "" || len([]rune(asStringOr(decision["title"]))) > 200 {
			return nil, invalid("ADR decision title is invalid")
		}
		status := asStringOr(decision["status"])
		if status != "proposed" && status != "accepted" && status != "superseded" && status != "deprecated" {
			return nil, invalid("ADR decision status is invalid")
		}
		applies, err := nativeStringArrayBounded(decision["applies_to"], "ADR applies_to", 64, true)
		if err != nil {
			return nil, err
		}
		for _, subject := range applies {
			if result.subjects[subject] == nil {
				return nil, invalid("ADR decision refers to an unknown subject")
			}
		}
		informed, err := nativeStringArrayBounded(decision["informed_by"], "ADR informed_by", 64, false)
		if err != nil {
			return nil, err
		}
		supersedes, err := nativeStringArrayBounded(decision["supersedes"], "ADR supersedes", 64, false)
		if err != nil {
			return nil, err
		}
		for _, reference := range append(append([]string{}, informed...), supersedes...) {
			if reference == id {
				return nil, invalid("ADR decision references itself")
			}
		}
		if triggers, present := decision["revisit_triggers"]; present {
			if _, err := nativeStringArrayBounded(triggers, "ADR revisit_triggers", 262144, false); err != nil {
				return nil, err
			}
		}
		decision["applies_to"] = toAnySlice(applies)
		decision["informed_by"] = toAnySlice(informed)
		decision["supersedes"] = toAnySlice(supersedes)
		// Assert-BFADRIndex validates every decision's normative source before
		// selecting the stage-specific applicable subset. Do the same here so an
		// unrelated malformed ADR cannot silently pass through the bundle hash.
		if _, _, err := nativeArchitectureDecisionText(root, decision); err != nil {
			return nil, err
		}
		result.decisions[id] = decision
	}
	for _, decision := range result.decisions {
		for _, field := range []string{"informed_by", "supersedes"} {
			for _, raw := range anyItems(decision[field]) {
				if result.decisions[asStringOr(raw)] == nil {
					return nil, invalid("ADR decision has an invalid %s reference", field)
				}
			}
		}
		for _, raw := range anyItems(decision["supersedes"]) {
			if asStringOr(result.decisions[asStringOr(raw)]["status"]) != "superseded" {
				return nil, invalid("superseded ADR status is inconsistent")
			}
		}
	}
	state := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return invalid("ADR supersedes cycle at %s", id)
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		for _, raw := range anyItems(result.decisions[id]["supersedes"]) {
			if err := visit(asStringOr(raw)); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range result.decisions {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	for id, decision := range result.decisions {
		if asStringOr(decision["status"]) != "superseded" {
			continue
		}
		count := 0
		for _, candidate := range result.decisions {
			if asStringOr(candidate["status"]) == "accepted" {
				for _, raw := range anyItems(candidate["supersedes"]) {
					if asStringOr(raw) == id {
						count++
					}
				}
			}
		}
		if count != 1 {
			return nil, invalid("superseded ADR requires exactly one accepted superseder")
		}
	}
	return result, nil
}

func nativeArchitectureID(value string) bool {
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(value) {
		return false
	}
	return true
}

func nativeStringArrayBounded(value any, name string, maxLength int, nonEmpty bool) ([]string, error) {
	items, err := nativeArray(value, name, nonEmpty)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, raw := range items {
		value, ok := asString(raw)
		if !ok || strings.TrimSpace(value) == "" || len([]rune(value)) > maxLength {
			return nil, invalid("%s contains an invalid string", name)
		}
		if seen[value] {
			return nil, invalid("%s contains duplicate values", name)
		}
		seen[value] = true
		result = append(result, value)
	}
	return result, nil
}

func nativeValidateArchitectureReference(reference, root, packageRoot string) error {
	parts := strings.SplitN(reference, "#", 2)
	relative := parts[0]
	if err := validateRelativeNativePath(relative, true); err != nil {
		return invalid("architecture reference path is unsafe")
	}
	for _, base := range []string{root, packageRoot} {
		candidate := filepath.Join(base, filepath.FromSlash(relative))
		resolved, err := SafePath(candidate)
		if err != nil {
			return err
		}
		if !nativeDependencyRegularFile(resolved) {
			continue
		}
		if len(parts) == 2 && parts[1] != "" {
			data, err := ReadFileBytes(resolved)
			if err != nil || !strings.Contains(string(data), parts[1]) {
				return invalid("architecture subject symbol is missing")
			}
		}
		return nil
	}
	return invalid("architecture subject reference is missing: %s", reference)
}

func nativeArchitectureDecisionText(root string, decision map[string]any) (string, string, error) {
	source, err := nativeObject(decision["source"], []string{"path", "anchor"}, nil, "ADR source")
	if err != nil {
		return "", "", err
	}
	relative := asStringOr(source["path"])
	if len([]rune(relative)) > 240 {
		return "", "", invalid("ADR source path is too long")
	}
	if err := validateRelativeNativePath(relative, false); err != nil {
		return "", "", invalid("ADR source path is unsafe")
	}
	anchor := asStringOr(source["anchor"])
	if strings.TrimSpace(anchor) == "" || len([]rune(anchor)) > 128 {
		return "", "", invalid("ADR source anchor is invalid")
	}
	path, err := SafePath(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", "", err
	}
	data, err := ReadFileBytes(path)
	if err != nil {
		return "", "", invalid("ADR source is missing: %s", relative)
	}
	sections, err := nativeArchitectureSections(string(data))
	if err != nil {
		return "", "", err
	}
	id := asStringOr(decision["id"])
	section, ok := sections[id]
	if !ok {
		return "", "", invalid("ADR heading is missing for %s", id)
	}
	if anchor != nativeArchitectureAnchor(id+": "+section.title) || asStringOr(decision["title"]) != section.title {
		return "", "", invalid("ADR source heading does not match index for %s", id)
	}
	return hashUTF8(section.body), nativeArchitectureExcerpt(section.body), nil
}

type nativeArchitectureSection struct{ title, body string }

var nativeArchitectureHeadingPattern = regexp.MustCompile(`(?m)^##[ \t]+ADR-([0-9]+):[ \t]*(.*?)[ \t]*\r?\n`)
var nativeArchitectureAnyH2Pattern = regexp.MustCompile(`(?m)^##[ \t]`)

func nativeArchitectureSections(text string) (map[string]nativeArchitectureSection, error) {
	matches := nativeArchitectureHeadingPattern.FindAllStringSubmatchIndex(text, -1)
	result := map[string]nativeArchitectureSection{}
	for _, match := range matches {
		number, err := strconv.Atoi(text[match[2]:match[3]])
		if err != nil {
			return nil, invalid("ADR heading number is invalid")
		}
		id := "ADR-" + strconv.Itoa(number)
		if _, exists := result[id]; exists {
			return nil, invalid("duplicate ADR heading: %s", id)
		}
		title := text[match[4]:match[5]]
		bodyStart := match[1]
		bodyEnd := len(text)
		if next := nativeArchitectureAnyH2Pattern.FindStringIndex(text[bodyStart:]); next != nil {
			bodyEnd = bodyStart + next[0]
		}
		result[id] = nativeArchitectureSection{title: title, body: text[bodyStart:bodyEnd]}
	}
	return result, nil
}

func nativeArchitectureExcerpt(body string) string {
	text := body
	marker := strings.Index(text, "**Решение.**")
	if marker >= 0 {
		text = text[marker+len("**Решение.**"):]
		end := len(text)
		for index := 0; index+1 < len(text); index++ {
			if text[index] == '\n' {
				cursor := index + 1
				for cursor < len(text) && (text[cursor] == ' ' || text[cursor] == '\t' || text[cursor] == '\r') {
					cursor++
				}
				if cursor+1 < len(text) && text[cursor] == '*' && text[cursor+1] == '*' {
					end = index
					break
				}
			}
		}
		text = text[:end]
	}
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > 500 {
		text = strings.TrimRightFunc(string(runes[:500]), unicode.IsSpace) + "..."
	}
	return text
}

func nativeArchitectureAnchor(heading string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(heading) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == ' ' {
			builder.WriteRune(r)
		}
	}
	return strings.Trim(strings.Join(strings.Fields(builder.String()), "-"), "-")
}
