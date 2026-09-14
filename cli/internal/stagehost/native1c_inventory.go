package stagehost

import (
	"context"
	"strings"
)

// This file ports Read-BFNativeInventory on the native path. The legacy
// wrapper spawned a pwsh helper process (Read-NativeInventory.ps1) whose
// receipts lived under the owned evidence directory; the native adapter
// performs the COM read in-process and writes the inventory document itself
// (classified divergence: no helper process, no helper receipts).

// readNative1CInventory mirrors Read-BFNativeInventory: one bounded read-only
// inventory for an authorized FILE target, with the envelope identity check.
func readNative1CInventory(ctx context.Context, deps Deps, target, executable string, credential native1cCredential, directory string) (map[string]any, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, blockedf("inventory requires an owned evidence directory.")
	}
	var inventory map[string]any
	var err error
	if deps.Native1CInventory != nil {
		inventory, err = deps.Native1CInventory(ctx, target, executable, credential, directory)
	} else {
		inventory, err = native1CCOMInventory(ctx, deps, target, executable, credential, directory)
	}
	if err != nil {
		return nil, err
	}
	if asStringOr(inventory["kind"]) != "bsl-flow.native-inventory" ||
		!strings.EqualFold(strings.TrimRight(asStringOr(inventory["target"]), `\/`), strings.TrimRight(target, `\/`)) {
		return nil, blockedf("native inventory identity mismatch.")
	}
	return inventory, nil
}
