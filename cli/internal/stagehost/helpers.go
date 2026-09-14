package stagehost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bsl-flow/cli/internal/repository"
)

// getValue mirrors Get-BFValue: a present key wins even when its value is
// null, and only a missing key yields the default.
func getValue(object map[string]any, name string, def any) any {
	if object == nil {
		return def
	}
	if value, present := object[name]; present {
		return value
	}
	return def
}

// hasProperty mirrors Test-BFCoverageProperty: the key exists, null or not.
func hasProperty(object map[string]any, name string) bool {
	if object == nil {
		return false
	}
	_, present := object[name]
	return present
}

func asObject(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	return object, ok && object != nil
}

// asMap mirrors the controller's tolerant map accessor for JSON objects.
func asMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func asString(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok
}

func asStringOr(value any) string {
	text, _ := value.(string)
	return text
}

func asArray(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	default:
		return nil, false
	}
}

// asInteger reports whether the value is a JSON integer (no fraction), the
// same domain PowerShell's ConvertFrom-Json materializes as int/long.
func asInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		return parsed, err == nil
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	default:
		return 0, false
	}
}

func asFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(string(typed), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	case float64:
		return typed, true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func asBool(value any) (bool, bool) {
	flag, ok := value.(bool)
	return flag, ok
}

// assertFields mirrors Assert-BFFields with its exact diagnostics.
func assertFields(value any, required, optional []string, name string) (map[string]any, error) {
	object, ok := asObject(value)
	if !ok {
		return nil, invalidf("%s must be an object.", name)
	}
	for _, key := range required {
		if _, present := object[key]; !present {
			return nil, invalidf("%s.%s is required.", name, key)
		}
	}
	for key := range object {
		allowed := false
		for _, candidate := range required {
			if candidate == key {
				allowed = true
				break
			}
		}
		if !allowed {
			for _, candidate := range optional {
				if candidate == key {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			return nil, invalidf("unknown field %s.%s.", name, key)
		}
	}
	return object, nil
}

// assertText mirrors Assert-BFText.
func assertText(value any, name string, limit ...int) error {
	maximum := 262144
	if len(limit) > 0 {
		maximum = limit[0]
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" || len([]rune(text)) > maximum {
		return invalidf("invalid %s.", name)
	}
	return nil
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func assertUUID(value any) error {
	text, ok := value.(string)
	if !ok || !uuidPattern.MatchString(text) {
		return invalidf("identity must be a canonical lower-case UUID.")
	}
	return nil
}

var relativePathForbidden = regexp.MustCompile(`[:*?"<>|\x00-\x1f]`)
var relativePathDotSegment = regexp.MustCompile(`(^|[\\/])\.\.([\\/]|$)`)

// assertRelativePath mirrors Assert-BFRelativePath.
func assertRelativePath(value string) error {
	if strings.TrimSpace(value) == "" || filepath.IsAbs(value) ||
		relativePathForbidden.MatchString(value) || relativePathDotSegment.MatchString(value) {
		return invalidf("unsafe relative path: %s", value)
	}
	return nil
}

// safePath mirrors Assert-BFSafePath through the shared controller scanner.
func safePath(path string) (string, error) {
	resolved, err := repository.StageHostSafePath(path)
	if err != nil {
		if kind, ok := err.(*repository.KindError); ok {
			return "", &Error{Class: kind.Kind, Message: kind.Message}
		}
		return "", invalidf("%v", err)
	}
	return resolved, nil
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func assertSHA256(value any, name string) error {
	text, ok := value.(string)
	if !ok || !sha256Pattern.MatchString(text) {
		return invalidf("%s must be a lower-case SHA-256.", name)
	}
	return nil
}

// hashValue mirrors Get-BFHash.
func hashValue(value any) (string, error) {
	hash, err := repository.StageHostHash(value)
	if err != nil {
		return "", invalidf("%v", err)
	}
	return hash, nil
}

func hashFileBytes(data []byte) string { return repository.StageHostFileSHA256(data) }

func hashFile(path string) (string, error) {
	data, err := repository.StageHostReadFileBytes(path)
	if err != nil {
		return "", blockedf("%v", err)
	}
	return hashFileBytes(data), nil
}

// readJSONObject mirrors Read-BFJson: one strict JSON object document under
// the controller bounds.
func readJSONObject(path string) (map[string]any, error) {
	resolved, err := safePath(path)
	if err != nil {
		return nil, err
	}
	data, err := repository.StageHostReadFileBytes(resolved)
	if err != nil {
		return nil, invalidf("JSON file does not exist: %s", resolved)
	}
	object, err := repository.DecodeObject(data)
	if err != nil {
		return nil, invalidf("Cannot materialize JSON object: %v", err)
	}
	return object, nil
}

// writeJSON mirrors Write-BFJson: canonical bytes published atomically.
func writeJSON(path string, value any, replace bool) error {
	resolved, err := safePath(path)
	if err != nil {
		return err
	}
	data, err := repository.StageHostCanonical(value)
	if err != nil {
		return invalidf("%v", err)
	}
	if err := repository.AtomicWrite(resolved, data, replace); err != nil {
		if !replace && strings.HasPrefix(err.Error(), "refusing to overwrite existing file") {
			return conflictf("Refusing to overwrite JSON file: %s", resolved)
		}
		return conflictf("Could not publish JSON file: %v", err)
	}
	return nil
}

func isRegularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func isDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

// startTimeUTC formats a process start like .NET's round-trip "o" specifier.
func startTimeUTC(moment time.Time) string {
	return moment.UTC().Format("2006-01-02T15:04:05.0000000Z")
}

// isAbsolutePath mirrors [IO.Path]::IsPathRooted closely enough for the
// provider contract: both Windows roots and rooted UNC forms are absolute.
func isAbsolutePath(path string) bool {
	if path == "" {
		return false
	}
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, "//") {
		return true
	}
	if len(path) >= 2 && path[1] == ':' {
		return true
	}
	return strings.HasPrefix(path, "/")
}

func pathExtension(path string) string {
	return strings.ToLower(filepath.Ext(path))
}

func nestedPath(candidate, root string) bool {
	candidate = strings.TrimRight(candidate, `\/`)
	root = strings.TrimRight(root, `\/`)
	if strings.EqualFold(candidate, root) {
		return true
	}
	prefix := root + string(filepath.Separator)
	return strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(prefix)) ||
		strings.HasPrefix(strings.ToLower(candidate), strings.ToLower(root+"/"))
}

func disjointPath(left, right, name string) error {
	left = strings.TrimRight(left, `\/`)
	right = strings.TrimRight(right, `\/`)
	if strings.EqualFold(left, right) || nestedPath(left, right) || nestedPath(right, left) {
		return invalidf("%s paths overlap.", name)
	}
	return nil
}
