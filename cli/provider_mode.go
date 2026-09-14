package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"bsl-flow/cli/internal/stagehost"
)

// runProviderMode serves the hidden `bsl-flow __provider` subcommand: it
// re-derives the trusted bundle and host identity from its own embedded
// assets, then dispatches one provider request through the native Go stage
// host. PowerShell is never launched from this path.
func runProviderMode(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) int {
	deps, err := providerModeDeps()
	if err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 1
	}
	return stagehost.RunProvider(ctx, deps, stdin, stdout, stderr)
}

// providerModeDeps rebuilds the same trusted bindings the packaged adapter
// verifies on the controller side: own executable hash, the extracted bundle
// skill root, the packaged provider script hash and the asset manifest hash.
func providerModeDeps() (stagehost.Deps, error) {
	b, err := readEmbeddedBundle()
	if err != nil {
		return stagehost.Deps{}, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return stagehost.Deps{}, fmt.Errorf("native provider cache: %w", err)
	}
	root, err := ensureBundle(filepath.Join(cache, "BSLFlow", "bundles"), b)
	if err != nil {
		return stagehost.Deps{}, fmt.Errorf("native provider bundle: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return stagehost.Deps{}, fmt.Errorf("native provider host executable: %w", err)
	}
	self, err = filepath.Abs(self)
	if err != nil {
		return stagehost.Deps{}, fmt.Errorf("native provider host executable: %w", err)
	}
	if err := checkPath(self); err != nil {
		return stagehost.Deps{}, fmt.Errorf("native provider host executable: %w", err)
	}
	selfSHA, err := fileSHA256(self)
	if err != nil {
		return stagehost.Deps{}, fmt.Errorf("native provider host hash: %w", err)
	}
	script := filepath.Join(root, filepath.FromSlash(nativeProviderEntrypoint))
	if err := checkPath(script); err != nil {
		return stagehost.Deps{}, fmt.Errorf("native provider script: %w", err)
	}
	providerSHA, err := fileSHA256(script)
	if err != nil {
		return stagehost.Deps{}, fmt.Errorf("native provider script hash: %w", err)
	}
	return stagehost.Deps{
		SelfPath:            self,
		SelfSHA256:          selfSHA,
		SkillsRoot:          filepath.Join(root, "global", "skills"),
		ProviderSHA256:      providerSHA,
		AssetManifestSHA256: bundleManifestSHA256(b),
		HostPolicyPath:      os.Getenv("BSL_FLOW_HOST_PATH"),
	}, nil
}
