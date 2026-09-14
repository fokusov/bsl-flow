package stagehost

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"bsl-flow/cli/internal/repository"
)

// This file ports Invoke-BFVerification, Invoke-BFExecutionCheck,
// Assert-BFProviderCoverageAccepted and Stop-BFVerificationFailure for the
// native stage host. Native 1C criteria dispatch through the runtime adapter
// (native1c_verify.go); the un-adapted integration/ui/external_artifact
// kinds stay blocked.

// permissionProfileUnmanaged mirrors Get-BFPermissionProfile (Codex.ps1) for
// the legacy non-profiled verification route.
func permissionProfileUnmanaged(workerPath string, writable bool) (string, error) {
	resolved, err := safePath(workerPath)
	if err != nil {
		return "", err
	}
	access := "read"
	if writable {
		access = "write"
	}
	return `permissions.bsl_flow={filesystem={":root"="read",` + jsonQuoted(strings.ReplaceAll(resolved, `\`, "/")) + `="` + access + `"},network={enabled=false}}`, nil
}

func jsonQuoted(value string) string {
	data, err := json.Marshal(value)
	if err != nil {
		return `"` + strings.ReplaceAll(strings.ReplaceAll(value, `\`, `\\`), `"`, `\"`) + `"`
	}
	return string(data)
}

// stageExecutionDependencies revalidates the pinned execution identity the
// same way Get-BFExecutionDependencies does before every paid dispatch.
func stageExecutionDependencies(state map[string]any) (map[string]any, error) {
	return repository.StageHostExecutionDependencies(state)
}

// runVerification mirrors Invoke-BFVerification.
func runVerification(ctx context.Context, deps Deps, state map[string]any, raw, codexPath string, run *stageRun) (map[string]any, error) {
	if err := assertVerificationCoverage(state); err != nil {
		return nil, err
	}
	request, _ := asObject(state["request"])
	if asStringOr(request["mode"]) == "implement" && hasProperty(request, "requirements") {
		// Provider variant of Assert-BFCoverageAccepted: the registered
		// code-review evidence from the provider context must still bind the
		// current requirements and protected test inputs.
		if err := assertProviderCoverageAccepted(state, run); err != nil {
			return nil, err
		}
	}
	observations := []any{}
	criteria, _ := asArray(request["criteria"])
	for _, rawCriterion := range criteria {
		criterion, _ := asObject(rawCriterion)
		id := asStringOr(criterion["id"])
		checkDir := filepath.Join(raw, id)
		if err := os.MkdirAll(checkDir, 0o755); err != nil {
			return nil, blockedf("%v", err)
		}
		kind := asStringOr(criterion["kind"])
		if kind == "file_assertion" {
			path, err := safePath(filepath.Join(asStringOr(state["worker_path"]), filepath.FromSlash(asStringOr(criterion["path"]))))
			if err != nil {
				return nil, err
			}
			if !isRegularFile(path) {
				return nil, stopVerificationFailure(state, criterion, fmt.Sprintf("BF_FAIL: %s: expected file is absent.", id))
			}
			data, err := repository.StageHostReadFileBytes(path)
			if err != nil {
				return nil, blockedf("%v", err)
			}
			if !strings.Contains(string(data), asStringOr(criterion["contains"])) {
				return nil, stopVerificationFailure(state, criterion, fmt.Sprintf("BF_FAIL: %s: expected content is absent.", id))
			}
			hash, err := hashFile(path)
			if err != nil {
				return nil, err
			}
			observations = append(observations, map[string]any{
				"criterion_id": id, "kind": kind, "file": asStringOr(criterion["path"]),
				"sha256": hash, "outcome": "PASS",
			})
		} else if hasProperty(criterion, "native_1c") {
			observation, err := runNativeVerification(ctx, deps, state, criterion, checkDir, run.providerContext.nativeCredential, run)
			if err != nil {
				return nil, err
			}
			observations = append(observations, observation)
		} else if kind == "integration" || kind == "ui" || kind == "external_artifact" {
			return nil, blockedf("%s requires a confirmed 1C runtime adapter and exact authorized target. The temporary runtime restriction remains active.", id)
		} else {
			report, err := safePath(filepath.Join(asStringOr(state["worker_path"]), filepath.FromSlash(asStringOr(criterion["report"]))))
			if err != nil {
				return nil, err
			}
			if fileExists(report) {
				if !isRegularFile(report) {
					return nil, blockedf("expected generated JUnit path is not a file.")
				}
				// Keep the previous bytes for diagnosis, then require this
				// attempt to produce a new report. Only the validated generated
				// path is removed.
				data, err := repository.StageHostReadFileBytes(report)
				if err != nil {
					return nil, blockedf("%v", err)
				}
				if err := os.WriteFile(filepath.Join(checkDir, "preexisting.junit.xml"), data, 0o644); err != nil {
					return nil, blockedf("%v", err)
				}
				if err := os.Remove(report); err != nil {
					return nil, blockedf("%v", err)
				}
			}
			process, err := runCriterionCheck(ctx, deps, state, criterion, checkDir, codexPath, run)
			if err != nil {
				return nil, err
			}
			if process.StopReason != "" {
				return nil, blockedf("test process %s; do not repeat uncertain effects.", process.StopReason)
			}
			if !isRegularFile(report) {
				return nil, blockedf("test process produced no original JUnit report.")
			}
			reportBytes, err := repository.StageHostReadFileBytes(report)
			if err != nil {
				return nil, blockedf("%v", err)
			}
			if err := os.WriteFile(filepath.Join(checkDir, "original.junit.xml"), reportBytes, 0o644); err != nil {
				return nil, blockedf("%v", err)
			}
			expected := stringList(criterion["expected_tests"])
			passed, err := repository.StageHostJUnit(reportBytes, expected)
			if err != nil {
				return nil, err
			}
			if !passed {
				return nil, stopVerificationFailure(state, criterion, fmt.Sprintf("BF_FAIL: %s: required tests failed.", id))
			}
			if process.ExitCode != 0 {
				return nil, blockedf("test process failed despite a passing report.")
			}
			actualTests, err := junitTestNames(reportBytes)
			if err != nil {
				return nil, err
			}
			observations = append(observations, map[string]any{
				"criterion_id": id, "kind": kind, "tests": actualTests,
				"sha256": hashFileBytes(reportBytes), "outcome": "PASS",
			})
		}
	}
	if err := writeJSON(filepath.Join(raw, "observations.json"), map[string]any{"criteria": observations}, false); err != nil {
		return nil, err
	}
	return map[string]any{
		"schema_version": int64(1),
		"status":         "completed",
		"summary":        "Every declared criterion has current deterministic evidence.",
		"payload_json":   "{}",
	}, nil
}

// junitTestNames collects the testcase name attributes in document order,
// matching the legacy Test-BFJUnit selection projection.
func junitTestNames(data []byte) ([]any, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	decoder.Strict = false
	names := []any{}
	depth := 0
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch typed := token.(type) {
		case xml.StartElement:
			depth++
			if typed.Name.Local == "testcase" {
				for _, attribute := range typed.Attr {
					if attribute.Name.Local == "name" {
						names = append(names, attribute.Value)
						break
					}
				}
			}
		case xml.EndElement:
			depth--
		}
	}
	return names, nil
}

// runCriterionCheck dispatches one executable criterion through the sandbox.
func runCriterionCheck(ctx context.Context, deps Deps, state map[string]any, criterion map[string]any, checkDir, codexPath string, run *stageRun) (ProcessResult, error) {
	request, _ := asObject(state["request"])
	timeoutSeconds := int64(1800)
	if value, ok := asInteger(getValue(request, "timeout_seconds", int64(1800))); ok {
		timeoutSeconds = value
	}
	if profile := getValue(request, "execution_profile", nil); profile != nil {
		return executionCheck(ctx, deps, state, criterion, checkDir, codexPath, run, int(timeoutSeconds))
	}
	permissions, err := permissionProfileUnmanaged(asStringOr(state["worker_path"]), true)
	if err != nil {
		return ProcessResult{}, err
	}
	arguments := []string{"sandbox", "-P", "bsl_flow", "-c", permissions, "-c", `windows.sandbox="unelevated"`, "-C", asStringOr(state["worker_path"]), asStringOr(criterion["executable"])}
	arguments = append(arguments, stringList(criterion["arguments"])...)
	return deps.runProcess(ctx, ProcessOptions{
		Executable:       codexPath,
		Arguments:        arguments,
		WorkingDirectory: asStringOr(state["worker_path"]),
		OutputDirectory:  checkDir,
		TimeoutSeconds:   int(timeoutSeconds),
		Cancelled:        run.cancel,
	})
}

// executionCheck mirrors Invoke-BFExecutionCheck: pinned identity, isolated
// verifier scratch, re-probed capability and the sandboxed check process.
func executionCheck(ctx context.Context, deps Deps, state map[string]any, criterion map[string]any, directory, codexPath string, run *stageRun, timeoutSeconds int) (ProcessResult, error) {
	if _, err := stageExecutionDependencies(state); err != nil {
		return ProcessResult{}, err
	}
	request, _ := asObject(state["request"])
	profile, _ := asObject(request["execution_profile"])
	sandbox, _ := asObject(profile["sandbox"])
	codexResolved, err := safePath(codexPath)
	if err != nil {
		return ProcessResult{}, err
	}
	sandboxPath, err := safePath(asStringOr(sandbox["executable"]))
	if err != nil {
		return ProcessResult{}, err
	}
	if !strings.EqualFold(codexResolved, sandboxPath) {
		return ProcessResult{}, blockedf("verifier sandbox identity mismatch.")
	}
	artifactRoot := run.providerContext.artifactRoot
	directoryHash, err := hashValue(directory)
	if err != nil {
		return ProcessResult{}, err
	}
	hostRoot, err := safePath(filepath.Join(artifactRoot, "execution", directoryHash))
	if err != nil {
		return ProcessResult{}, err
	}
	if fileExists(hostRoot) {
		return ProcessResult{}, blockedf("existing verifier scratch requires attempt reconciliation.")
	}
	scratch := filepath.Join(hostRoot, "scratch")
	config := filepath.Join(hostRoot, "config")
	for _, path := range []string{scratch, config} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return ProcessResult{}, blockedf("%v", err)
		}
	}
	permissions, err := permissionProfile(state, scratch, config, true, run.providerContext.canonicalStore)
	if err != nil {
		return ProcessResult{}, err
	}
	permissions = strings.Replace(permissions, "network={enabled=true}", "network={enabled=false}", 1)
	if _, err := executionCapability(ctx, deps, state, filepath.Join(directory, "capability"), scratch, config, permissions, true, run.providerContext.canonicalStore); err != nil {
		return ProcessResult{}, err
	}
	arguments := []string{"sandbox", "-P", "bsl_execution", "-c", permissions, "-c", `windows.sandbox="elevated"`, "-C", asStringOr(state["worker_path"]), asStringOr(criterion["executable"])}
	arguments = append(arguments, stringList(criterion["arguments"])...)
	return deps.runProcess(ctx, ProcessOptions{
		Executable:       codexPath,
		Arguments:        arguments,
		WorkingDirectory: asStringOr(state["worker_path"]),
		OutputDirectory:  directory,
		TimeoutSeconds:   timeoutSeconds,
		Cancelled:        run.cancel,
		Environment:      map[string]string{"TEMP": scratch, "TMP": scratch, "GIT_OPTIONAL_LOCKS": "0"},
		CleanEnvironment: true,
	})
}

// getProviderContextArtifact mirrors Get-BFProviderContextArtifact: a
// prior-artifact-bound read from the immutable context projection.
func getProviderContextArtifact(contextRoot, relative string, declared map[string]map[string]any) (map[string]any, error) {
	resolved, err := safePath(contextRoot)
	if err != nil {
		return nil, err
	}
	if err := assertRelativePath(relative); err != nil {
		return nil, err
	}
	if regexp.MustCompile(`(^|[\\/]).(git|bsl-flow)([\\/]|$)`).MatchString(relative) {
		return nil, blockedf("provider context may not expose controller paths.")
	}
	full, err := safePath(filepath.Join(resolved, filepath.FromSlash(relative)))
	if err != nil {
		return nil, err
	}
	root := strings.TrimRight(resolved, `\/`)
	if !strings.EqualFold(full, root) && !insideCanonical(full, root) {
		return nil, invalidf("context artifact escaped context_root.")
	}
	key := strings.ReplaceAll(relative, `\`, "/")
	entry, ok := declared[key]
	if !ok {
		return nil, blockedf("context artifact was not declared by core: %s", key)
	}
	if !isRegularFile(full) {
		return nil, blockedf("declared context artifact is missing: %s", key)
	}
	info, err := os.Lstat(full)
	if err != nil {
		return nil, blockedf("%v", err)
	}
	size, _ := asInteger(entry["size_bytes"])
	if info.Size() != size {
		return nil, blockedf("context artifact size changed: %s", key)
	}
	hash, err := hashFile(full)
	if err != nil {
		return nil, err
	}
	if hash != asStringOr(entry["sha256"]) {
		return nil, blockedf("context artifact bytes changed: %s", key)
	}
	return readJSONObject(full)
}

// assertProviderCoverageAccepted mirrors Assert-BFProviderCoverageAccepted.
func assertProviderCoverageAccepted(state map[string]any, run *stageRun) error {
	evidence, _ := asArray(state["evidence"])
	var latest map[string]any
	for _, raw := range evidence {
		entry, _ := asObject(raw)
		if asStringOr(entry["stage"]) == "code_review" {
			latest = entry
		}
	}
	if latest == nil {
		return blockedf("requirement coverage needs a fresh independent code review.")
	}
	reviewID := asStringOr(latest["attempt_id"])
	if err := assertUUID(reviewID); err != nil {
		return err
	}
	result, err := getProviderContextArtifact(run.providerContext.contextRoot, "attempts/"+reviewID+"/result.json", run.providerContext.priorArtifacts)
	if err != nil {
		return err
	}
	resultHash, err := hashValue(result)
	if err != nil {
		return err
	}
	if resultHash != asStringOr(latest["result_sha256"]) {
		return blockedf("coverage review result changed.")
	}
	proposal, _ := asObject(result["proposal"])
	if hasProperty(proposal, "review") {
		proposal, _ = asObject(proposal["review"])
	}
	coverage := getValue(proposal, "coverage_review", nil)
	if coverage == nil || asMap(coverage)["verdict"] != "PASS" {
		return blockedf("independently sufficient requirement coverage is missing.")
	}
	// Coverage inspection is read-only from the source perspective. Its
	// generated binding stays below the existing worker-admin exclusion so a
	// fresh source manifest cannot mistake it for an implementation change.
	rawDir := filepath.Join(asStringOr(state["worker_path"]), ".bsl-flow-worker", "provider", "coverage-"+reviewID)
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		return blockedf("%v", err)
	}
	binding, err := getProviderContextArtifact(run.providerContext.contextRoot, "attempts/"+reviewID+"/raw/coverage-review-binding.json", run.providerContext.priorArtifacts)
	if err != nil {
		return err
	}
	computed, err := assertCoverageReview(state, coverage, rawDir)
	if err != nil {
		return err
	}
	bindingHash, err := hashValue(binding)
	if err != nil {
		return err
	}
	computedHash, err := hashValue(computed)
	if err != nil {
		return err
	}
	if bindingHash != computedHash {
		return blockedf("registered coverage binding is stale.")
	}
	return nil
}

// stopVerificationFailure mirrors Stop-BFVerificationFailure: only the
// completed verifier sets this typed marker; worker text cannot. The marker
// travels to the controller through the provider proposal and decides repair
// eligibility there.
func stopVerificationFailure(state map[string]any, criterion map[string]any, message string) error {
	request, _ := asObject(state["request"])
	maxRepairs, _ := asInteger(getValue(request, "max_source_repairs", int64(0)))
	safe := maxRepairs > 0
	criteria, _ := asArray(request["criteria"])
	for _, raw := range criteria {
		check, _ := asObject(raw)
		checkKind := asStringOr(check["kind"])
		if checkKind == "file_assertion" {
			continue
		}
		if checkKind != "static" && checkKind != "unit" {
			safe = false
			continue
		}
		if retrySafe, _ := asBool(getValue(check, "retry_safe", false)); !retrySafe {
			safe = false
		}
	}
	return &VerificationFailure{
		Err:            &Error{Class: ClassFail, Message: strings.TrimPrefix(message, ClassFail+": ")},
		RepairEligible: safe,
		CriterionID:    asStringOr(criterion["id"]),
		Kind:           asStringOr(criterion["kind"]),
		Observation:    asStringOr(criterion["observation"]),
	}
}
