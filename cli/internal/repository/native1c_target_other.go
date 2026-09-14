//go:build !windows

package repository

// On every non-windows platform the physical FILE identity resolution is a
// Windows-only capability (req 15). The shared gate in Native1CTargetIdentity
// surfaces the typed blocker before these functions can be reached; they fail
// closed with the same typed blocker as defense in depth.

func native1CPhysicalTargetIdentity(target, marker string) (string, error) {
	return "", blocked(Native1CUnsupportedPlatformCode + ": native 1C runtime requires windows")
}

// Native1CJournalRoot mirrors the windows implementation; it is unreachable
// behind the platform gate and fails closed here.
func Native1CJournalRoot(key string) (string, error) {
	return "", blocked(Native1CUnsupportedPlatformCode + ": native 1C runtime requires windows")
}
