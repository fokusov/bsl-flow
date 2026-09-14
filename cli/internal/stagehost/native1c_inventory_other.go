//go:build !windows

package stagehost

import (
	"context"
)

// On every non-windows platform the COM inventory is a Windows-only
// capability (req 15); the verification-level gate surfaces the typed
// blocker before this function can be reached, and it fails closed here as
// defense in depth.

func native1CCOMInventory(ctx context.Context, deps Deps, target, executable string, credential native1cCredential, directory string) (map[string]any, error) {
	return nil, native1cPlatformBlocker("non-windows")
}
