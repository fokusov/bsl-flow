package stagehost

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// This file ports ConvertTo-BFProviderOutput, Get-BFProviderArtifactManifest,
// Get-BFProviderProcessReceipts and Get-BFProviderArtifactKind from
// Task.Provider.ps1: the closed observation the controller validates.

var artifactKindBudget = regexp.MustCompile(`(^|/)budget(/|$)`)
var artifactKindProcess = regexp.MustCompile(`(^|/)(process|exit)\.json$`)
var artifactKindStreams = regexp.MustCompile(`(^|/)(stdout|stderr)\.txt$`)
var artifactKindModel = regexp.MustCompile(`model-result\.json$`)
var artifactKindReview = regexp.MustCompile(`review|reconciliation|spec-lint`)
var artifactKindVerification = regexp.MustCompile(`verification|observations|junit|coverage`)
var artifactKindFailure = regexp.MustCompile(`failure\.json$`)

// artifactKind mirrors Get-BFProviderArtifactKind.
func artifactKind(relative string) string {
	lower := strings.ToLower(relative)
	switch {
	case artifactKindBudget.MatchString(lower):
		return "budget"
	case artifactKindProcess.MatchString(lower) || artifactKindStreams.MatchString(lower):
		return "process"
	case artifactKindModel.MatchString(lower):
		return "model"
	case artifactKindReview.MatchString(lower):
		return "review"
	case artifactKindVerification.MatchString(lower):
		return "verification"
	case artifactKindFailure.MatchString(lower):
		return "failure"
	default:
		return "raw"
	}
}

var artifactStateSegments = map[string]bool{
	"current": true, "revisions": true, "inputs": true, "acceptance": true,
	"current.json": true, "acceptance.json": true,
}

// artifactManifest mirrors Get-BFProviderArtifactManifest.
func artifactManifest(artifactRoot string) ([]any, error) {
	root, err := safePath(artifactRoot)
	if err != nil {
		return nil, err
	}
	if !isDirectory(root) {
		return []any{}, nil
	}
	var entries []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(entry.Name(), ".tmp") {
			return nil
		}
		entries = append(entries, path)
		return nil
	})
	if err != nil {
		return nil, blockedf("%v", err)
	}
	sort.Strings(entries)
	manifest := make([]any, 0, len(entries))
	for _, path := range entries {
		resolved, err := safePath(path)
		if err != nil {
			return nil, err
		}
		relative := filepath.ToSlash(strings.TrimPrefix(resolved, root))
		relative = strings.TrimPrefix(relative, "/")
		if err := assertRelativePath(relative); err != nil {
			return nil, err
		}
		for _, segment := range strings.Split(relative, "/") {
			if artifactStateSegments[segment] {
				return nil, blockedf("provider artifact contains controller state.")
			}
		}
		info, err := os.Lstat(resolved)
		if err != nil {
			return nil, blockedf("%v", err)
		}
		hash, err := hashFile(resolved)
		if err != nil {
			return nil, err
		}
		manifest = append(manifest, map[string]any{
			"path":       relative,
			"sha256":     hash,
			"size_bytes": info.Size(),
			"kind":       artifactKind(relative),
		})
	}
	return manifest, nil
}

// processReceipts mirrors Get-BFProviderProcessReceipts.
func processReceipts(artifactRoot string) ([]any, error) {
	root, err := safePath(artifactRoot)
	if err != nil {
		return nil, err
	}
	var exitFiles []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.EqualFold(entry.Name(), "exit.json") {
			exitFiles = append(exitFiles, path)
		}
		return nil
	})
	if err != nil {
		return nil, blockedf("%v", err)
	}
	sort.Strings(exitFiles)
	receipts := []any{}
	for _, exitFile := range exitFiles {
		resolvedExit, err := safePath(exitFile)
		if err != nil {
			return nil, err
		}
		directory := filepath.Dir(resolvedExit)
		processFile := filepath.Join(directory, "process.json")
		if !isRegularFile(processFile) {
			return nil, blockedf("native process exit receipt has no process identity.")
		}
		exit, err := readJSONObject(resolvedExit)
		if err != nil {
			return nil, err
		}
		process, err := readJSONObject(processFile)
		if err != nil {
			return nil, err
		}
		if _, err := assertFields(exit, []string{"exit_code", "stop_reason", "elapsed_seconds", "process_id", "executable", "stdout", "stderr"}, nil, "process_exit"); err != nil {
			return nil, err
		}
		if _, err := assertFields(process, []string{"pid", "start_time_utc", "executable", "arguments_sha256"}, nil, "process_identity"); err != nil {
			return nil, err
		}
		var streamPaths []string
		for _, field := range []string{"stdout", "stderr"} {
			stream := asStringOr(exit[field])
			resolvedStream, err := safePath(stream)
			if err != nil || !isRegularFile(resolvedStream) {
				return nil, blockedf("native process stream receipt is missing.")
			}
			streamPaths = append(streamPaths, resolvedStream)
		}
		processRelative, err := artifactRelative(root, processFile)
		if err != nil {
			return nil, err
		}
		exitRelative, err := artifactRelative(root, resolvedExit)
		if err != nil {
			return nil, err
		}
		stdoutRelative, err := artifactRelative(root, streamPaths[0])
		if err != nil {
			return nil, err
		}
		stderrRelative, err := artifactRelative(root, streamPaths[1])
		if err != nil {
			return nil, err
		}
		processHash, err := hashFile(processFile)
		if err != nil {
			return nil, err
		}
		exitHash, err := hashFile(resolvedExit)
		if err != nil {
			return nil, err
		}
		stdoutHash, err := hashFile(streamPaths[0])
		if err != nil {
			return nil, err
		}
		stderrHash, err := hashFile(streamPaths[1])
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, map[string]any{
			"process_path":   processRelative,
			"process_sha256": processHash,
			"exit_path":      exitRelative,
			"exit_sha256":    exitHash,
			"stdout_path":    stdoutRelative,
			"stdout_sha256":  stdoutHash,
			"stderr_path":    stderrRelative,
			"stderr_sha256":  stderrHash,
			"exit_code":      exit["exit_code"],
			"stop_reason":    exit["stop_reason"],
		})
	}
	return receipts, nil
}

// artifactRelative converts an absolute path under the artifact root into the
// slash-separated relative descriptor.
func artifactRelative(root, path string) (string, error) {
	root = strings.TrimRight(root, `\/`)
	full, err := safePath(path)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(full, root) && !insideCanonical(full, root) {
		return "", invalidf("process receipt escaped artifact_root.")
	}
	relative := strings.TrimPrefix(full[len(root):], string(filepath.Separator))
	relative = strings.TrimPrefix(relative, "/")
	relative = filepath.ToSlash(relative)
	if err := assertRelativePath(relative); err != nil {
		return "", err
	}
	return relative, nil
}

// statusFor maps a terminal stage outcome onto the provider status set.
func statusFor(outcome string) string {
	switch outcome {
	case "PASS", "REVISE", "REPAIR":
		return "completed"
	case "NEEDS_INPUT":
		return "needs_input"
	case "FAIL":
		return "failed"
	default:
		return "blocked"
	}
}

// convertToProviderOutput mirrors ConvertTo-BFProviderOutput: the terminal
// stage observation plus the immutable artifact and process evidence.
func convertToProviderOutput(deps Deps, input *providerInput, terminal map[string]any) (map[string]any, error) {
	status := statusFor(asStringOr(terminal["outcome"]))
	manifest, err := artifactManifest(input.artifactRoot)
	if err != nil {
		return nil, err
	}
	sourceManifest, sourceErr := stageSourceManifest(input.stateView)
	if sourceErr != nil {
		// A failed/blocked attempt may have lost access to the worktree. Keep
		// the closed observation with an explicit null so the controller can
		// retain the outer receipt and independently fail closed; never
		// manufacture a stale source manifest from the attempt start.
		if status == "completed" {
			return nil, sourceErr
		}
		sourceManifest = nil
	}
	receipts, err := processReceipts(input.artifactRoot)
	if err != nil {
		return nil, err
	}
	proposal := terminal["proposal"]
	if proposal == nil {
		proposal = nil
	}
	dependencies := terminal["dependencies"]
	return map[string]any{
		"schema_version":    int64(1),
		"contract":          Contract,
		"task_id":           input.taskID,
		"attempt_id":        asStringOr(input.attempt["attempt_id"]),
		"stage":             asStringOr(input.attempt["stage"]),
		"status":            status,
		"summary":           asStringOr(terminal["summary"]),
		"proposal":          proposal,
		"side_effects":      asStringOr(terminal["side_effects"]),
		"dependencies":      dependencies,
		"source_manifest":   sourceManifest,
		"artifacts":         manifest,
		"process_receipt":   map[string]any{"processes": receipts},
		"provider_contract": input.providerContract,
	}, nil
}
