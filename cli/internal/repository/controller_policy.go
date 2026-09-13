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
)

var nativeProjectPolicyPaths = []string{"AGENTS.md", ".ai/model-routing.md", "bsl-flow.yaml", "openspec/config.yaml"}

func nativePolicyInventory(project, skillsRoot, hostPath string) ([]any, error) {
	root, err := SafePath(skillsRoot)
	if err != nil || skillsRoot == "" {
		return nil, blocked("trusted provider policy root is unavailable")
	}
	paths := []string{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
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
			return blocked("policy inventory contains a non-regular file")
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil || len(paths) == 0 {
		return nil, blocked("cannot enumerate provider policy: %v", err)
	}
	sort.Strings(paths)
	for _, relative := range nativeProjectPolicyPaths {
		paths = append(paths, filepath.Join(project, filepath.FromSlash(relative)))
	}
	if hostPath == "" {
		return nil, blocked("trusted compiled host path is unavailable")
	}
	paths = append(paths, hostPath)
	files := make([]any, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		safe, err := SafePath(path)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(safe)
		if seen[key] {
			return nil, blocked("duplicate policy path")
		}
		seen[key] = true
		var hash any
		data, err := ReadFileBytes(safe)
		if err == nil {
			hash = fileSHA256(data)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		files = append(files, map[string]any{"path": safe, "sha256": hash})
	}
	return files, nil
}

// The YAML reader follows the packaged review helper's small mapping subset;
// it rejects duplicate routing values and never guesses from arbitrary text.
func nativeYamlValue(text, wanted, fallback string) (string, error) {
	pattern := regexp.MustCompile(`^(\s*)([A-Za-z0-9_-]+):\s*(.*?)\s*$`)
	type level struct {
		indent int
		key    string
	}
	stack := []level{}
	values := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		match := pattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		if strings.Contains(match[1], "\t") {
			return "", invalid("tabs are unsupported in policy indentation")
		}
		indent := len(match[1])
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		keys := []string{}
		for _, parent := range stack {
			keys = append(keys, parent.key)
		}
		keys = append(keys, match[2])
		value := strings.TrimSpace(match[3])
		if value != "" && strings.Join(keys, "/") == wanted {
			values = append(values, strings.Trim(value, `"'`))
		}
		if value == "" {
			stack = append(stack, level{indent, match[2]})
		}
	}
	if len(values) > 1 {
		return "", invalid("duplicate policy value %s", wanted)
	}
	if len(values) == 1 {
		return values[0], nil
	}
	return fallback, nil
}

func nativeProjectRules(project string) (map[string]any, error) {
	data, err := ReadFileBytes(filepath.Join(project, "bsl-flow.yaml"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	s, err := nativeYamlValue(string(data), "review/routing/s_default", "optional")
	if err != nil {
		return nil, err
	}
	if s != "optional" && s != "required" && s != "off" {
		return nil, invalid("invalid S review policy")
	}
	for _, key := range []string{"m_default", "l_default", "high_risk_override"} {
		value, err := nativeYamlValue(string(data), "review/routing/"+key, "required")
		if err != nil {
			return nil, err
		}
		if value != "required" {
			return nil, blocked("project policy weakens mandatory M/L/high review")
		}
	}
	return map[string]any{"s_review_required": s == "required"}, nil
}

func policyRootsFromPayload(payload map[string]any) (string, string, error) {
	files, ok := nativeItems(payload["policy_files"])
	if !ok || len(files) < 6 {
		return "", "", blocked("incomplete native policy inventory")
	}
	root := ""
	for _, raw := range files {
		entry := asMap(raw)
		path := asStringOr(entry["path"])
		if strings.HasSuffix(filepath.ToSlash(path), "/global/skills/1c-task/scripts/Invoke-BFNativeProvider.ps1") {
			if root != "" {
				return "", "", blocked("ambiguous provider policy root")
			}
			root = filepath.Dir(filepath.Dir(filepath.Dir(path)))
		}
	}
	if root == "" {
		return "", "", blocked("native policy has no bound provider entrypoint")
	}
	host := asStringOr(asMap(files[len(files)-1])["path"])
	return root, host, nil
}

// Match the host bundle inventory hash, including paths, lengths and every
// global asset. A newly added file is a policy change even when old files match.
func nativeAssetManifestHash(skillsRoot string) (string, error) {
	bundleRoot := filepath.Dir(filepath.Dir(skillsRoot))
	paths := []string{"VERSION"}
	globalRoot := filepath.Join(bundleRoot, "global")
	err := filepath.WalkDir(globalRoot, func(path string, entry os.DirEntry, walkErr error) error {
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
			return blocked("bundle contains a non-regular file")
		}
		relative, err := filepath.Rel(bundleRoot, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(paths, func(i, j int) bool { return strings.ToLower(paths[i]) < strings.ToLower(paths[j]) })
	hash := sha256.New()
	for _, relative := range paths {
		data, err := ReadFileBytes(filepath.Join(bundleRoot, filepath.FromSlash(relative)))
		if err != nil {
			return "", err
		}
		fileHash := sha256.Sum256(data)
		fmt.Fprint(hash, relative)
		hash.Write([]byte{0})
		hash.Write(fileHash[:])
		fmt.Fprint(hash, strconv.Itoa(len(data)), "\n")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func assertNativePolicyFresh(payload map[string]any) error {
	root, host, err := policyRootsFromPayload(payload)
	if err != nil {
		return err
	}
	files, err := nativePolicyInventory(asStringOr(payload["project_path"]), root, host)
	if err != nil {
		return err
	}
	hash, err := Hash(files)
	if err != nil || hash != asStringOr(payload["policy_hash"]) || !equalJSON(files, payload["policy_files"]) {
		return blocked("policy/package changed; explicit execution rebind is required")
	}
	rules, err := nativeProjectRules(asStringOr(payload["project_path"]))
	if err != nil || !equalJSON(rules, payload["policy_rules"]) {
		return blocked("project routing policy changed")
	}
	engine := asMap(payload["engine"])
	for path, expected := range map[string]string{
		host: asStringOr(engine["host_sha256"]),
		filepath.Join(root, "1c-task", "scripts", "Invoke-BFNativeProvider.ps1"): asStringOr(engine["provider_sha256"]),
	} {
		data, err := ReadFileBytes(path)
		if err != nil || fileSHA256(data) != expected {
			return blocked("native execution host/provider identity changed")
		}
	}
	assets, err := nativeAssetManifestHash(root)
	if err != nil || assets != asStringOr(engine["asset_manifest_sha256"]) {
		return blocked("native provider asset inventory changed")
	}
	return nil
}
