// Command bslflow-release builds the reproducible multi-target release
// archives from one source revision without requiring PowerShell. It is a
// build-time tool: the produced bsl-flow binaries are what users run.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"bsl-flow/cli/internal/release"
)

// releaseEpoch is the fixed archive timestamp so identical source revisions
// produce byte-identical archives.
var releaseEpoch = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)

func main() {
	source := flag.String("source", ".", "module directory (the repository cli directory)")
	out := flag.String("out", "", "output directory for binaries, archives and release-manifest.json")
	version := flag.String("version", "", "release semver recorded in the manifest")
	ldflags := flag.String("ldflags", "", "extra linker flags passed as a single -ldflags value")
	targets := flag.String("targets", "", "optional comma-separated GOOS/GOARCH subset (for example windows/amd64); default builds the full release matrix")
	flag.Parse()
	if *out == "" || *version == "" {
		fmt.Fprintln(os.Stderr, "usage: bslflow-release -source <cli-dir> -out <dir> -version <semver> [-ldflags <flags>] [-targets <goos/goarch,...>]")
		os.Exit(2)
	}
	opts := release.Options{
		SourceDir:           *source,
		OutDir:              *out,
		Version:             *version,
		TrimPath:            true,
		Now:                 releaseEpoch,
		VerifyDeterministic: true,
	}
	if *targets != "" {
		selected, err := parseTargets(*targets)
		if err != nil {
			fmt.Fprintln(os.Stderr, "BF_INVALID: "+err.Error())
			os.Exit(2)
		}
		opts.Targets = selected
	}
	if *ldflags != "" {
		opts.LDFlags = []string{*ldflags}
	}
	manifest, err := release.BuildAll(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "BF_BLOCKED: "+err.Error())
		os.Exit(1)
	}
	encoded, err := manifest.MarshalCanonical()
	if err != nil {
		fmt.Fprintln(os.Stderr, "BF_BLOCKED: "+err.Error())
		os.Exit(1)
	}
	if _, err = os.Stdout.Write(encoded); err != nil {
		fmt.Fprintln(os.Stderr, "BF_BLOCKED: "+err.Error())
		os.Exit(1)
	}
}

// parseTargets narrows the release matrix to the listed GOOS/GOARCH pairs so
// a single target can be built without cross-compiling the whole matrix.
func parseTargets(value string) ([]release.Target, error) {
	seen := map[string]bool{}
	targets := make([]release.Target, 0, 4)
	for _, item := range strings.Split(value, ",") {
		goos, goarch, found := strings.Cut(strings.TrimSpace(item), "/")
		if !found || goos == "" || goarch == "" || strings.ContainsAny(goos+goarch, " /\t") {
			return nil, fmt.Errorf("invalid release target %q; expected GOOS/GOARCH", item)
		}
		key := goos + "/" + goarch
		if seen[key] {
			return nil, fmt.Errorf("repeated release target %q", item)
		}
		seen[key] = true
		targets = append(targets, release.Target{GOOS: goos, GOARCH: goarch})
	}
	if len(targets) == 0 {
		return nil, errors.New("at least one release target is required")
	}
	return targets, nil
}
