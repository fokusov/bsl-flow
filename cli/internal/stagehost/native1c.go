package stagehost

import (
	"strings"

	"bsl-flow/cli/internal/repository"
)

// This file ports the credential and error surfaces of the native 1C runtime
// adapter (Task.Runtime.ps1). The adapter is a Windows-only capability: on
// every other platform a native 1C criterion surfaces the typed
// BLOCKED_UNSUPPORTED_PLATFORM blocker instead of a relabelled outcome.

// native1cCredential is the private controller input relayed to the provider
// process: it never appears in argv, logs or persisted evidence. The
// verification only substitutes <username>/<password> placeholders with it.
type native1cCredential struct {
	username string
	password string
}

// native1cPlatformBlocker mirrors the typed Windows-only capability gate.
func native1cPlatformBlocker(goos string) error {
	if goos == "windows" {
		return nil
	}
	return blockedf("%s: native 1C runtime requires windows", repository.Native1CUnsupportedPlatformCode)
}

// native1CError converts a shared repository binding error into the stage
// host error taxonomy, preserving the BF_* class byte for byte.
func native1cError(err error) error {
	if err == nil {
		return nil
	}
	if kind, ok := err.(*repository.KindError); ok {
		return &Error{Class: kind.Kind, Message: kind.Message}
	}
	return blockedf("%v", err)
}

// native1CJournalRootOf dispatches through the injected seam or the trusted
// local app data journal.
func native1CJournalRootOf(deps Deps, key string) (string, error) {
	if deps.Native1CJournalRoot != nil {
		return deps.Native1CJournalRoot(key)
	}
	return repository.StageHostNative1CJournalRoot(key)
}

// native1cSafeCredential mirrors New-BFNativeArguments: only a non-blank
// username without quote/control characters is accepted for the FILE
// connection builder.
func native1cSafeCredential(credential native1cCredential) error {
	if strings.TrimSpace(credential.username) == "" ||
		native1cUnsafeCredentialText(credential.username) ||
		native1cUnsafeCredentialText(credential.password) {
		return blockedf("unsupported credential characters.")
	}
	return nil
}

// native1cUnsafeCredentialText mirrors the legacy `["\x00-\x1f]` pattern:
// a double quote or any control character is rejected.
func native1cUnsafeCredentialText(text string) bool {
	for _, character := range text {
		if character == '"' || character < 0x20 {
			return true
		}
	}
	return false
}
