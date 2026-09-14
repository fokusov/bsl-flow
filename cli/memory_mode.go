package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"bsl-flow/cli/internal/memoryhost"
)

// runMemoryMode serves the hidden `bsl-flow __memory` subcommand: it
// re-derives the trusted bundle root from the embedded assets and serves
// exactly one native memory helper request. PowerShell is never launched
// from this path, so advisory memory works on every platform. The helper's
// process contract is preserved: one canonical envelope on stdout and exit
// code 0 even when the bundle cannot be resolved.
func runMemoryMode(stdin io.Reader, stdout io.Writer) int {
	root, err := memoryModeRoot()
	if err != nil {
		memoryhost.WriteDisabledEnvelope(stdout, "BF_BLOCKED: native memory helper bundle is unavailable.")
		return 0
	}
	return memoryhost.Run(stdin, stdout, root)
}

// memoryModeRoot extracts the embedded bundle into the shared cache and
// returns its root, mirroring the provider-mode bindings so both hidden
// helpers observe the same installed package: the memory fingerprints bind
// its VERSION, package manifest and memory schemas.
func memoryModeRoot() (string, error) {
	b, err := readEmbeddedBundle()
	if err != nil {
		return "", err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("native memory cache: %w", err)
	}
	root, err := ensureBundle(filepath.Join(cache, "BSLFlow", "bundles"), b)
	if err != nil {
		return "", fmt.Errorf("native memory bundle: %w", err)
	}
	return root, nil
}
