package repository

import (
	"path/filepath"
	"runtime"
	"strings"
)

// This file ports the target identity of the native 1C runtime adapter
// (Get-BFRuntimeTargetIdentity/Get-BFRuntimeTargetKey from Task.Runtime.ps1)
// shared between the controller (stage dependency computation) and the native
// stage host (verification). The physical identity resolution itself is a
// Windows-only capability: every other platform gets the typed
// BLOCKED_UNSUPPORTED_PLATFORM blocker, never a relabelled PASS.

// Native1CUnsupportedPlatformCode is the typed blocker code of req 15: a
// criterion that needs the native 1C runtime cannot run outside windows.
const Native1CUnsupportedPlatformCode = "BLOCKED_UNSUPPORTED_PLATFORM"

const native1CMarkerName = "1Cv8.1CD"

// Native1CPlatformBlocker returns nil on windows. On every other platform it
// returns the typed blocker a native 1C criterion must surface.
func Native1CPlatformBlocker(goos string) error {
	if goos == "windows" {
		return nil
	}
	return blocked(Native1CUnsupportedPlatformCode + ": native 1C runtime requires windows")
}

// Native1CTargetIdentity mirrors Get-BFRuntimeTargetIdentity: the absolute
// FILE target must carry the database marker, and its physical identity must
// equal the requested path (no aliased/substituted targets).
func Native1CTargetIdentity(target string) (string, error) {
	if !filepath.IsAbs(target) {
		return "", invalid("native 1C target must be an absolute FILE directory.")
	}
	if err := Native1CPlatformBlocker(runtime.GOOS); err != nil {
		return "", err
	}
	resolved, err := SafePath(target)
	if err != nil {
		return "", err
	}
	marker := filepath.Join(resolved, native1CMarkerName)
	if !nativeDependencyRegularFile(marker) {
		return "", blocked("FILE target marker is required to resolve physical target identity.")
	}
	return native1CPhysicalTargetIdentity(strings.TrimRight(resolved, `\/`), marker)
}

// Native1CTargetKey mirrors Get-BFRuntimeTargetKey: SHA-256 over the UTF-8
// bytes of the lowercased physical identity. It is deliberately not a
// canonical-JSON string hash.
func Native1CTargetKey(target string) (string, error) {
	identity, err := Native1CTargetIdentity(target)
	if err != nil {
		return "", err
	}
	return hashUTF8(strings.ToLower(identity)), nil
}
