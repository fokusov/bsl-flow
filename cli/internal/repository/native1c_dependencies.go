package repository

import (
	"path/filepath"
	"strings"
)

// This file ports Get-BFNativeDependencies and the inventory transition
// contract (ConvertTo-BFNativeInventoryRow, Get-BFNativeInventoryHash,
// Assert-BFNativeInventoryTransition) from Task.Runtime.ps1. The dependency
// binding is read-only and shared: the controller computes it for the verify
// stage dependencies, the native stage host re-validates it before dispatch.

// Native1CDependencies mirrors Get-BFNativeDependencies. The criterion must
// already pass ValidateNative1CCriterionShape; this function binds the live
// authorized identities (target, executable, version, COM connector) exactly
// like the legacy computation.
func Native1CDependencies(criterion map[string]any) (map[string]any, error) {
	if err := ValidateNative1CCriterionShape(criterion); err != nil {
		return nil, err
	}
	native := asMap(criterion["native_1c"])
	target, err := Native1CTargetIdentity(asStringOr(criterion["target"]))
	if err != nil {
		return nil, err
	}
	key, err := Native1CTargetKey(asStringOr(criterion["target"]))
	if err != nil {
		return nil, err
	}
	executable, err := SafePath(asStringOr(criterion["executable"]))
	if err != nil {
		return nil, err
	}
	executableData, err := ReadFileBytes(executable)
	if err != nil {
		return nil, blocked("authorized 1cv8.exe identity changed.")
	}
	actualExecutableHash := fileSHA256(executableData)
	if actualExecutableHash != asStringOr(native["executable_sha256"]) {
		return nil, blocked("authorized 1cv8.exe identity changed.")
	}
	version, err := native1CPEFileVersion(executableData)
	if err != nil || version != asStringOr(native["platform_version"]) {
		return nil, blocked("actual 1cv8.exe version differs from the criterion.")
	}
	comDll, err := SafePath(filepath.Join(filepath.Dir(executable), "comcntr.dll"))
	if err != nil {
		return nil, err
	}
	if !nativeDependencyRegularFile(comDll) {
		return nil, blocked("platform COM connector is missing.")
	}
	comData, err := ReadFileBytes(comDll)
	if err != nil {
		return nil, blocked("platform COM connector is missing.")
	}
	return map[string]any{
		"criterion_id":           criterion["id"],
		"target_key":             key,
		"target":                 target,
		"platform_version":       version,
		"executable_sha256":      actualExecutableHash,
		"com_connector_sha256":   fileSHA256(comData),
		"expected_tests":         criterion["expected_tests"],
		"authorization_reference": native["authorization_reference"],
	}, nil
}

// Native1CInventoryHash mirrors Get-BFNativeInventoryHash.
func Native1CInventoryHash(inventory map[string]any) (string, error) {
	return Hash(map[string]any{
		"target":     inventory["target"],
		"platform":   inventory["platform"],
		"extensions": inventory["extensions"],
	})
}

var native1CInventoryPropertyNames = []string{"name", "version", "active", "purpose", "scope", "uuid", "hash_sum"}

// Native1CNormalizeInventoryRow mirrors ConvertTo-BFNativeInventoryRow.
func Native1CNormalizeInventoryRow(item map[string]any) (map[string]any, error) {
	properties := asMap(item["properties"])
	row := map[string]any{}
	for _, name := range native1CInventoryPropertyNames {
		value, present := properties[name]
		if !present {
			return nil, blocked("inventory property %s is missing.", name)
		}
		if wrapped, ok := value.(map[string]any); ok && wrapped != nil {
			if asStringOr(wrapped["status"]) != "observed" {
				return nil, blocked("inventory property %s is unobserved.", name)
			}
			value = wrapped["value"]
		}
		if value == nil {
			return nil, blocked("inventory property %s is missing.", name)
		}
		row[name] = value
	}
	return row, nil
}

// Native1CInventoryTransition mirrors Assert-BFNativeInventoryTransition.
func Native1CInventoryTransition(before, after, source map[string]any) error {
	normalizedBefore, err := normalizeNative1CInventory(before)
	if err != nil {
		return err
	}
	normalizedAfter, err := normalizeNative1CInventory(after)
	if err != nil {
		return err
	}
	extension := asStringOr(source["extension"])
	otherBefore := []any{}
	otherAfter := []any{}
	for _, row := range normalizedBefore {
		if !strings.EqualFold(asStringOr(row["name"]), extension) {
			otherBefore = append(otherBefore, row)
		}
	}
	for _, row := range normalizedAfter {
		if !strings.EqualFold(asStringOr(row["name"]), extension) {
			otherAfter = append(otherAfter, row)
		}
	}
	otherBeforeHash, err := Hash(otherBefore)
	if err != nil {
		return err
	}
	otherAfterHash, err := Hash(otherAfter)
	if err != nil {
		return err
	}
	if otherBeforeHash != otherAfterHash {
		return blocked("a nonselected extension changed during native verification.")
	}
	selectedBefore := []map[string]any{}
	selectedAfter := []map[string]any{}
	for _, row := range normalizedBefore {
		if strings.EqualFold(asStringOr(row["name"]), extension) {
			selectedBefore = append(selectedBefore, row)
		}
	}
	for _, row := range normalizedAfter {
		if strings.EqualFold(asStringOr(row["name"]), extension) {
			selectedAfter = append(selectedAfter, row)
		}
	}
	if len(selectedAfter) != 1 || asStringOr(selectedAfter[0]["version"]) != asStringOr(source["version"]) || !native1CInventoryActive(selectedAfter[0]["active"]) {
		return blocked("installed extension version or active state differs from the authorized source XML.")
	}
	if len(selectedBefore) > 1 || (len(selectedBefore) == 1 && asStringOr(selectedBefore[0]["uuid"]) != asStringOr(selectedAfter[0]["uuid"])) {
		return blocked("installed extension instance UUID changed unexpectedly.")
	}
	return nil
}

func normalizeNative1CInventory(inventory map[string]any) ([]map[string]any, error) {
	rows := []map[string]any{}
	for _, raw := range anyItems(inventory["extensions"]) {
		item := asMap(raw)
		row, err := Native1CNormalizeInventoryRow(item)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// native1CInventoryActive mirrors the COM Boolean of the active property:
// only a JSON true marks an extension active.
func native1CInventoryActive(value any) bool {
	flag, ok := value.(bool)
	return ok && flag
}
