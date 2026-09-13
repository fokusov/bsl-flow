// Command bslflow-release builds the reproducible multi-target release
// archives from one source revision without requiring PowerShell. It is a
// build-time tool: the produced bsl-flow binaries are what users run.
package main

import (
	"flag"
	"fmt"
	"os"
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
	flag.Parse()
	if *out == "" || *version == "" {
		fmt.Fprintln(os.Stderr, "usage: bslflow-release -source <cli-dir> -out <dir> -version <semver> [-ldflags <flags>]")
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
