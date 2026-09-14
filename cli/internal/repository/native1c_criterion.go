package repository

import (
	"path/filepath"
	"regexp"
	"strings"
)

// This file ports Assert-BFNativeCriterion from Task.Runtime.ps1 with its
// exact diagnostics, shared by the controller request validation and the
// native stage host so both classify the same criterion identically. The
// physical target identity (Get-BFRuntimeTargetKey) is resolved on windows
// here exactly like the legacy validator does; on every other platform the
// typed BLOCKED_UNSUPPORTED_PLATFORM blocker fires first.

var (
	native1CExtensionNamePattern   = regexp.MustCompile(`^[A-Za-zА-Яа-яЁё_][A-Za-zА-Яа-яЁё0-9_]{0,127}$`)
	native1CPlatformVersionPattern = regexp.MustCompile(`^8\.3\.\d+\.\d+$`)
	native1CTestIDPattern          = regexp.MustCompile(`^[^\.\s]+(?:\.[^\.\s]+)+$`)
	native1CRelativeDotSegment     = regexp.MustCompile(`(^|[\\/])\.\.([\\/]|$)`)
)

// ValidateNative1CCriterionShape mirrors Assert-BFNativeCriterion.
func ValidateNative1CCriterionShape(criterion map[string]any) error {
	nativeValue, present := criterion["native_1c"]
	if !present || nativeValue == nil {
		return invalid("native_1c contract is required.")
	}
	if asStringOr(criterion["kind"]) != "integration" {
		return invalid("native_1c is supported only for integration criteria.")
	}
	native, err := native1CFields(nativeValue, []string{"source_root", "extension", "module", "platform_version", "executable_sha256", "authorized_operations", "authorization_reference"}, []string{"reuse_load_attempt"}, "criterion.native_1c")
	if err != nil {
		return err
	}
	if err := native1CRelativePath(asStringOr(native["source_root"])); err != nil {
		return err
	}
	for _, name := range []string{"extension", "module", "platform_version", "authorization_reference"} {
		if err := native1CText(native[name], "criterion.native_1c."+name); err != nil {
			return err
		}
	}
	if !native1CExtensionNamePattern.MatchString(asStringOr(native["extension"])) || !native1CExtensionNamePattern.MatchString(asStringOr(native["module"])) {
		return invalid("unsafe native 1C extension or module name.")
	}
	if !native1CPlatformVersionPattern.MatchString(asStringOr(native["platform_version"])) {
		return invalid("invalid native 1C platform version.")
	}
	if !isSHA256(asStringOr(native["executable_sha256"])) {
		return invalid("native executable SHA-256 must be lowercase hexadecimal.")
	}
	executable, executableIsString := criterion["executable"].(string)
	if !executableIsString || !filepath.IsAbs(executable) || !strings.EqualFold(filepath.Base(executable), "1cv8.exe") {
		return invalid("native executable must be an absolute 1cv8.exe path.")
	}
	if arguments, ok := nativeItems(criterion["arguments"]); !ok || len(arguments) != 0 {
		return invalid("native 1C criteria do not accept free arguments.")
	}
	protected, protectedOK := nativeItems(criterion["protected_paths"])
	if !protectedOK || len(protected) == 0 {
		return invalid("native 1C criteria require protected_paths for declared tests and fixtures.")
	}
	for _, rawPath := range protected {
		if err := native1CRelativePath(asStringOr(rawPath)); err != nil {
			return err
		}
	}
	if target, ok := criterion["target"].(string); !ok || !filepath.IsAbs(target) {
		return invalid("native 1C target must be an absolute FILE directory.")
	}
	// Get-BFRuntimeTargetKey: the physical target identity binds the criterion
	// on windows; other platforms surface the typed blocker here.
	if _, err := Native1CTargetKey(asStringOr(criterion["target"])); err != nil {
		return err
	}
	operations := "inventory,load,update,test"
	if reuse := native["reuse_load_attempt"]; reuse != nil {
		if !isUUID(asStringOr(reuse)) {
			return invalid("identity must be a canonical lower-case UUID.")
		}
		operations = "inventory,test"
	}
	authorized, authorizedOK := nativeItems(native["authorized_operations"])
	if !authorizedOK || joinNative1CStrings(authorized) != operations {
		return invalid("authorized_operations must be exactly " + operations + ".")
	}
	expected, expectedOK := nativeItems(criterion["expected_tests"])
	if !expectedOK || len(expected) == 0 {
		return invalid("unique class-qualified expected tests are required.")
	}
	seen := map[string]bool{}
	for _, raw := range expected {
		id, isString := raw.(string)
		if !isString || !native1CTestIDPattern.MatchString(id) {
			return invalid("expected native test IDs must be classname.name.")
		}
		key := strings.ToLower(id)
		if seen[key] {
			return invalid("unique class-qualified expected tests are required.")
		}
		seen[key] = true
	}
	return nil
}

// native1CFields mirrors Assert-BFFields with its exact diagnostics.
func native1CFields(value any, required, optional []string, name string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, invalid("%s must be an object.", name)
	}
	allowed := map[string]bool{}
	for _, key := range required {
		allowed[key] = true
		if _, present := object[key]; !present {
			return nil, invalid("%s.%s is required.", name, key)
		}
	}
	for _, key := range optional {
		allowed[key] = true
	}
	for key := range object {
		if !allowed[key] {
			return nil, invalid("unknown field %s.%s.", name, key)
		}
	}
	return object, nil
}

// native1CText mirrors Assert-BFText.
func native1CText(value any, name string) error {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" || len([]rune(text)) > 262144 {
		return invalid("invalid %s.", name)
	}
	return nil
}

// native1CRelativePath mirrors Assert-BFRelativePath.
func native1CRelativePath(value string) error {
	if strings.TrimSpace(value) == "" || filepath.IsAbs(value) ||
		strings.ContainsAny(value, ":*?\"<>|\x00-\x1f") ||
		native1CRelativeDotSegment.MatchString(value) {
		return invalid("unsafe relative path: %s", value)
	}
	return nil
}

func joinNative1CStrings(values []any) string {
	parts := make([]string, 0, len(values))
	for _, raw := range values {
		parts = append(parts, asStringOr(raw))
	}
	return strings.Join(parts, ",")
}
