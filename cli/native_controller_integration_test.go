package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"bsl-flow/cli/internal/repository"
)

// This test intentionally crosses the public Go dispatch boundary and the
// packaged PowerShell provider boundary. The copied provider fixture is the
// only place where the worker and capability functions are deterministic; the
// controller, provider contracts, stage validators, transport receipts and
// acceptance gates remain the installed implementations.
type nativeControllerIntegrationFixture struct {
	project     string
	taskID      string
	provider    *nativeProvider
	host        *repository.ControllerHost
	bundleRoot  string
	toolsetRoot string
	powerShell  string
	negative    bool
	medium      bool
	repair      bool
	breakMemory bool
}

type nativeControllerFixtureOptions struct {
	negative    bool
	medium      bool
	repair      bool
	breakMemory bool
}

var nativeControllerUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestNativeControllerPublicLifecycleUsesSeparateProviderProcess(t *testing.T) {
	fixture := newNativeControllerIntegrationFixture(t, nativeControllerFixtureOptions{})
	nativeControllerRunPublicLifecycle(t, fixture)
	nativeControllerAssertJournal(t, fixture, 3, false)
}

func TestNativeControllerPublicLifecycleFailsClosedAfterImplement(t *testing.T) {
	fixture := newNativeControllerIntegrationFixture(t, nativeControllerFixtureOptions{negative: true})
	nativeControllerRunPublicLifecycle(t, fixture)
	nativeControllerAssertJournal(t, fixture, 3, true)
}

func TestNativeControllerPublicMediumLifecycleRunsSpecReviewThroughValidators(t *testing.T) {
	fixture := newNativeControllerIntegrationFixture(t, nativeControllerFixtureOptions{medium: true})
	nativeControllerRunPublicLifecycle(t, fixture)
	nativeControllerAssertJournal(t, fixture, 5, false)
	nativeControllerAssertMediumReviewEvidence(t, fixture)
}

func TestNativeControllerRepairLifecycleHandsVerifiedFailureToMemoryBridge(t *testing.T) {
	fixture := newNativeControllerIntegrationFixture(t, nativeControllerFixtureOptions{negative: true, repair: true})
	nativeControllerRunRepairLifecycle(t, fixture)
	nativeControllerAssertJournal(t, fixture, 7, false)
	nativeControllerAssertMemoryBridgeOutcome(t, fixture, true)
}

func TestNativeControllerBrokenMemoryBridgeKeepsLifecycleAdvisory(t *testing.T) {
	fixture := newNativeControllerIntegrationFixture(t, nativeControllerFixtureOptions{breakMemory: true})
	nativeControllerRunPublicLifecycle(t, fixture)
	nativeControllerAssertJournal(t, fixture, 3, false)
	nativeControllerAssertMemoryBridgeOutcome(t, fixture, false)
}

func newNativeControllerIntegrationFixture(t *testing.T, options nativeControllerFixtureOptions) nativeControllerIntegrationFixture {
	t.Helper()
	project := newNativeControllerGitRepository(t)
	repositoryRoot := nativeControllerRepositoryRoot(t)
	powerShell, err := systemPowerShell()
	if err != nil {
		t.Fatalf("resolve PowerShell 7 for native provider: %v", err)
	}
	hostPath, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test host executable: %v", err)
	}
	if strings.ToLower(filepath.Ext(hostPath)) != ".exe" {
		t.Fatalf("native test host is not an .exe: %q", hostPath)
	}
	bundleRoot, bundleValue := newNativeControllerBundle(t, repositoryRoot, options)
	provider, err := newNativeProvider(bundleRoot, bundleValue, hostPath)
	if err != nil {
		t.Fatalf("construct packaged native provider: %v", err)
	}
	toolsetRoot, toolsetHash := newNativeControllerToolset(t)
	host := repository.NewControllerHost(provider, provider.engineIdentity())

	createInput := nativeControllerWriteJSON(t, map[string]any{
		"schema_version": int64(1),
		"title":          "native public lifecycle fixture",
	})
	created, code, stderr := nativeControllerDispatch(t, host, []string{
		"task", "create", "--project", project, "--input", createInput,
	})
	if code != 0 {
		t.Fatalf("public create exit=%d stderr=%q response=%#v", code, stderr, created)
	}
	taskID, ok := created["task_id"].(string)
	if !ok || !nativeControllerUUID.MatchString(taskID) {
		t.Fatalf("public create returned invalid task id: %#v", created)
	}

	complexity := "S"
	if options.medium {
		complexity = "M"
	}
	request := nativeControllerRequest(t, taskID, powerShell, toolsetRoot, toolsetHash, complexity, options.repair)
	activationInput := nativeControllerWriteJSON(t, request)
	activated, code, stderr := nativeControllerDispatch(t, host, []string{
		"task", "activate", "--project", project, "--task", taskID,
		"--expected-revision", "1", "--input", activationInput,
	})
	if code != 0 {
		t.Fatalf("public native activation exit=%d stderr=%q response=%#v", code, stderr, activated)
	}
	if activated["task_id"] != taskID || activated["status"] != "ready" || activated["next_action"] != "dispatch" || activated["next_stage"] != "inspect" {
		t.Fatalf("activation did not publish inspect dispatch: %#v", activated)
	}
	return nativeControllerIntegrationFixture{
		project: project, taskID: taskID, provider: provider, host: host,
		bundleRoot: bundleRoot, toolsetRoot: toolsetRoot, powerShell: powerShell,
		negative: options.negative, medium: options.medium, repair: options.repair,
		breakMemory: options.breakMemory,
	}
}

func nativeControllerWantStages(fixture nativeControllerIntegrationFixture) string {
	if fixture.medium {
		return "inspect,spec,spec_review,implement,verify"
	}
	return "inspect,implement,verify"
}

func nativeControllerRunPublicLifecycle(t *testing.T, fixture nativeControllerIntegrationFixture) {
	t.Helper()
	seenStages := []string{}
	for step := 0; step < 8; step++ {
		nextResponse, code, stderr := nativeControllerDispatch(t, fixture.host, []string{
			"task", "next", "--project", fixture.project, "--task", fixture.taskID, "--engine", "native",
		})
		if code != 0 {
			t.Fatalf("public next step=%d exit=%d stderr=%q response=%#v", step, code, stderr, nextResponse)
		}
		next, ok := nextResponse["next"].(map[string]any)
		if !ok {
			t.Fatalf("public next has no closed next object: %#v", nextResponse)
		}
		action, _ := next["action"].(string)
		stage, _ := next["stage"].(string)
		switch action {
		case "dispatch":
			seenStages = append(seenStages, stage)
			runResponse, runCode, runStderr := nativeControllerDispatch(t, fixture.host, []string{
				"task", "run", "--project", fixture.project, "--task", fixture.taskID, "--engine", "native",
			})
			if runCode != 0 {
				t.Logf("native provider attempt diagnostics:\n%s", nativeControllerAttemptDiagnostics(t, fixture))
				t.Fatalf("public run stage=%s exit=%d stderr=%q response=%#v", stage, runCode, runStderr, runResponse)
			}
			if runResponse["task_id"] != fixture.taskID {
				t.Fatalf("public run stage=%s returned another task id: %#v", stage, runResponse)
			}
			controller := nativeControllerReadController(t, fixture)
			evidence := nativeControllerEvidence(t, controller, stage)
			wantOutcome := "PASS"
			if fixture.negative && stage == "verify" {
				wantOutcome = "FAIL"
			}
			if evidence["outcome"] != wantOutcome {
				t.Fatalf("stage %s outcome=%v, want %s; controller=%#v", stage, evidence["outcome"], wantOutcome, controller)
			}
		case "accept":
			if fixture.negative {
				t.Fatalf("negative lifecycle reached acceptance unexpectedly: %#v", next)
			}
			accepted, acceptCode, acceptStderr := nativeControllerDispatch(t, fixture.host, []string{
				"task", "accept", "--project", fixture.project, "--task", fixture.taskID, "--engine", "native",
			})
			if acceptCode != 0 || accepted["task_id"] != fixture.taskID || accepted["status"] != "completed" {
				t.Fatalf("public acceptance exit=%d stderr=%q response=%#v", acceptCode, acceptStderr, accepted)
			}
			controller := nativeControllerReadController(t, fixture)
			if controller["status"] != "completed" || len(nativeControllerItems(controller["acceptances"])) != 1 {
				t.Fatalf("acceptance was not persisted exactly once: %#v", controller)
			}
			if !strings.EqualFold(strings.Join(seenStages, ","), nativeControllerWantStages(fixture)) {
				t.Fatalf("positive route stages=%v, want %s", seenStages, nativeControllerWantStages(fixture))
			}
			return
		case "failed":
			if !fixture.negative {
				t.Fatalf("positive lifecycle failed: %#v", next)
			}
			if stage != "verify" || !strings.EqualFold(strings.Join(seenStages, ","), nativeControllerWantStages(fixture)) {
				t.Fatalf("negative route stopped at stage=%s with stages=%v", stage, seenStages)
			}
			controller := nativeControllerReadController(t, fixture)
			if controller["status"] != "failed" || len(nativeControllerItems(controller["acceptances"])) != 0 {
				t.Fatalf("negative failed state is not closed before acceptance: %#v", controller)
			}
			accepted, acceptCode, acceptStderr := nativeControllerDispatch(t, fixture.host, []string{
				"task", "accept", "--project", fixture.project, "--task", fixture.taskID, "--engine", "native",
			})
			if acceptCode == 0 || !strings.Contains(nativeControllerString(accepted["next_action"]), "inspect_blocker") || acceptStderr != "" {
				t.Fatalf("failed lifecycle unexpectedly accepted: exit=%d stderr=%q response=%#v", acceptCode, acceptStderr, accepted)
			}
			return
		default:
			t.Fatalf("unexpected public lifecycle action at step=%d: %#v", step, next)
		}
	}
	t.Fatalf("public native lifecycle did not reach acceptance or failed verification")
}

func nativeControllerDispatch(t *testing.T, host *repository.ControllerHost, args []string) (map[string]any, int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	handled, code := repository.DispatchWithHost(args, &stdout, &stderr, host)
	if !handled {
		t.Fatalf("public native dispatch did not handle args: %v", args)
	}
	if stdout.Len() == 0 {
		t.Fatalf("public native dispatch returned no JSON for args %v; stderr=%q", args, stderr.String())
	}
	var response map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("public native dispatch returned invalid JSON for args %v: %v\n%s", args, err, stdout.String())
	}
	return response, code, stderr.String()
}

func nativeControllerRequest(t *testing.T, taskID, executable, toolsetRoot, toolsetHash, complexity string, repair bool) map[string]any {
	t.Helper()
	executableHash := nativeControllerFileHash(t, executable)
	maxSourceRepairs := int64(0)
	if repair {
		maxSourceRepairs = int64(1)
	}
	return map[string]any{
		"schema_version": int64(1),
		"request_id":     taskID,
		"prompt":         "Run the deterministic native controller lifecycle fixture.",
		"mode":           "implement",
		"analysis_goal":  "analysis",
		"complexity":     complexity,
		"risk":           "low",
		"impact_flags":   []any{},
		"criteria": []any{map[string]any{
			"id":          "readme",
			"observation": "The fixture readme remains present with its marker.",
			"kind":        "file_assertion",
			"path":        "readme.txt",
			"contains":    "fixture",
		}},
		"provenance": map[string]any{
			"source":    "user",
			"reference": "native-public-lifecycle",
			"text":      "native public lifecycle fixture",
		},
		"models": map[string]any{
			"worker":          "gpt-5.6-luna",
			"worker_effort":   "medium",
			"reviewer":        "gpt-5.6-luna",
			"reviewer_effort": "high",
		},
		"execution_profile": map[string]any{
			"provider":            "codex",
			"executable":          executable,
			"executable_sha256":   executableHash,
			"codex_skills_sha256": strings.Repeat("0", 64),
			"sandbox": map[string]any{
				"executable": executable,
				"sha256":     executableHash,
			},
			"toolset": map[string]any{
				"name": "cc-1c-skills", "root": toolsetRoot, "sha256": toolsetHash,
			},
			"runtime": map[string]any{
				"executable": executable, "sha256": executableHash, "version": "7.4.0",
				"packages": []any{map[string]any{"name": "lxml", "version": "fixture"}},
			},
			"denied_read_roots": []any{filepath.Join(filepath.Dir(toolsetRoot), "private-native-input")},
		},
		"budget":             map[string]any{"currency": "USD", "limit": int64(10), "reservation": int64(0)},
		"max_attempts":       int64(16),
		"timeout_seconds":    int64(60),
		"max_source_repairs": maxSourceRepairs,
	}
}

func newNativeControllerGitRepository(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "bsl-flow-native-public-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	nativeControllerRunGit(t, root, "init")
	nativeControllerRunGit(t, root, "config", "user.name", "BSL Flow Native Test")
	nativeControllerRunGit(t, root, "config", "user.email", "native-test@example.invalid")
	nativeControllerWriteFile(t, filepath.Join(root, ".gitignore"), []byte(".bsl-flow/\n"))
	nativeControllerWriteFile(t, filepath.Join(root, "readme.txt"), []byte("fixture\n"))
	nativeControllerWriteFile(t, filepath.Join(root, "bsl-flow.yaml"), []byte("review:\n  routing:\n    s_default: optional\n    m_default: required\n    l_default: required\n    high_risk_override: required\n"))
	nativeControllerRunGit(t, root, "add", ".")
	nativeControllerRunGit(t, root, "commit", "-m", "native public lifecycle fixture")
	return root
}

func nativeControllerRunGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = nativeControllerGitEnvironment()
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

func nativeControllerGitEnvironment() []string {
	env := make([]string, 0, len(os.Environ())+5)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		switch strings.ToUpper(key) {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES":
			continue
		default:
			env = append(env, item)
		}
	}
	env = append(env,
		"GIT_AUTHOR_NAME=BSL Flow Native Test",
		"GIT_AUTHOR_EMAIL=native-test@example.invalid",
		"GIT_COMMITTER_NAME=BSL Flow Native Test",
		"GIT_COMMITTER_EMAIL=native-test@example.invalid",
		"GIT_TERMINAL_PROMPT=0",
	)
	return env
}

func nativeControllerRepositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "cli", "go.mod")); err == nil {
			return directory
		}
		if _, err := os.Stat(filepath.Join(directory, "global", "skills", "1c-task", "scripts", "Invoke-BFNativeProvider.ps1")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatalf("cannot locate BSL Flow repository root from %q", directory)
		}
		directory = parent
	}
}

func newNativeControllerBundle(t *testing.T, repositoryRoot string, options nativeControllerFixtureOptions) (string, bundle) {
	t.Helper()
	fixtureRoot := filepath.Join(nativeControllerTempDirectory(t), "bundle")
	if err := os.MkdirAll(fixtureRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, skill := range []string{"1c-task", "1c-spec-review", "1c-spec", "1c-implement", "1c-verify"} {
		source := filepath.Join(repositoryRoot, "global", "skills", skill)
		destination := filepath.Join(fixtureRoot, "global", "skills", skill)
		nativeControllerCopyTree(t, source, destination)
	}
	nativeControllerWriteFile(t, filepath.Join(fixtureRoot, "VERSION"), []byte("0.8.0-test\n"))
	if options.breakMemory {
		// Break only the copied memory helper: the real provider process must
		// surface the failed helper as a disabled advisory envelope while the
		// lifecycle itself still completes. The failure is injected before the
		// script's own envelope/exit tail, otherwise it would never run.
		memoryScript := filepath.Join(fixtureRoot, "global", "skills", "1c-task", "scripts", "Invoke-BFNativeMemory.ps1")
		memoryData, err := os.ReadFile(memoryScript)
		if err != nil {
			t.Fatal(err)
		}
		memoryText := strings.ReplaceAll(string(memoryData), "\r\n", "\n")
		needle := "$envelope = $null"
		if !strings.Contains(memoryText, needle) {
			t.Fatalf("native memory fixture has no envelope seam")
		}
		memoryText = strings.Replace(memoryText, needle, needle+"\nthrow 'FIXTURE_MEMORY_HELPER_BROKEN'\n", 1)
		nativeControllerWriteFile(t, memoryScript, []byte(memoryText))
	}
	entrypoint := filepath.Join(fixtureRoot, filepath.FromSlash(nativeProviderEntrypoint))
	entrypointData, err := os.ReadFile(entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	entrypointText := strings.ReplaceAll(string(entrypointData), "\r\n", "\n")
	needle := ". Import-BFNativeProviderAdapter $inputObject\n"
	if !strings.Contains(entrypointText, needle) {
		t.Fatalf("native provider fixture entrypoint has no adapter seam")
	}
	seam := nativeControllerProviderSeam(options.negative)
	entrypointText = strings.Replace(entrypointText, needle, needle+seam+"\n", 1)
	nativeControllerWriteFile(t, entrypoint, []byte(entrypointText))

	files := []bundleFile{}
	err = filepath.WalkDir(fixtureRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("fixture bundle contains a non-regular file: %s", path)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		relative, relErr := filepath.Rel(fixtureRoot, path)
		if relErr != nil {
			return relErr
		}
		digest := sha256.Sum256(data)
		files = append(files, bundleFile{name: filepath.ToSlash(relative), data: data, hash: digest})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return fixtureRoot, bundle{version: "0.8.0-test", hash: "native-public-fixture", files: files}
}

func nativeControllerProviderSeam(negative bool) string {
	negativeLiteral := "$false"
	if negative {
		negativeLiteral = "$true"
	}
	return fmt.Sprintf(`
# Test-only seam: it is injected into a copied fixture after the packaged
# adapter import. Production entrypoints have no equivalent injection point.
$script:BFNativeFixtureNegative = %s

# Deterministic medium-complexity specification. The spec stage only runs on
# the medium route, so the classification lines are fixed to M/low and must
# satisfy the real Test-1CSpec lint that Save-BFSpec invokes.
$script:BFNativeFixtureSpec = @'
## Classification

- Complexity: M
- Risk: low

## Goal

Keep the deterministic fixture readme intact through the complete native medium lifecycle.

## Required behavior

The fixture treats readme.txt as the single tracked source artifact of the change and never renames or relocates it.

## 1C context

Configuration/subsystem: none, this fixture is source-only and does not touch a 1C runtime.

## Non-goals

No runtime 1C changes, no metadata writes and no database access are part of this fixture.

## Acceptance criteria

- GIVEN the fixture readme exists with its marker WHEN the medium lifecycle completes THEN readme.txt still contains the original marker text.

## Required verification

- [x] Static: the deterministic file assertion re-reads readme.txt and proves the marker survives the full native route.

## Uncertainties / assumptions

None; the fixture is fully deterministic and carries no external dependencies.
'@

function Read-BFTask {
    throw 'NATIVE_FIXTURE_LEGACY_READ_TRIPWIRE'
}

function Save-BFTask {
    throw 'NATIVE_FIXTURE_LEGACY_SAVE_TRIPWIRE'
}

function Test-BFExecutionCapability {
    param($State,[string]$Directory,[string]$Scratch,[string]$Config,[string]$Permissions,[bool]$Writable,[string]$CanonicalStoreRoot='')
    foreach($path in @($Directory,$Scratch,$Config)) {
        [void][IO.Directory]::CreateDirectory((Assert-BFSafePath $path))
    }
    $observations=[ordered]@{
        config_read='allowed'; config_write='denied'; scratch_write='allowed'; source_write='denied';
        canonical_current_read='denied'; canonical_revision_read='denied'; canonical_inputs_read='denied';
        canonical_current_write='denied'; canonical_revision_write='denied'; canonical_inputs_write='denied'
    }
    $capability=[ordered]@{
        observations=$observations; permissions_sha256=(Get-BFHash $Permissions);
        sandbox_sha256=$State.request.execution_profile.sandbox.sha256;
        provider_sha256=$State.request.execution_profile.executable_sha256;
        network='not_probed'; database='not_accessed'
    }
    Write-BFJson -Path (Join-Path (Assert-BFSafePath $Directory) 'capability.json') -Value $capability
    return $capability
}

function Invoke-BFManagedWorker {
    param($State,[string]$Stage,[string]$Prompt,[string]$Directory,[string]$CodexPath,[scriptblock]$Cancelled,[int]$MaxOutputBytes=16777216,[object]$ProviderContext=$null)
    $directory=Assert-BFSafePath $Directory
    # Mirror the production budget contract: admission and reservation are
    # recorded before the worker process starts, the outcome is completed from
    # retained host evidence after it finishes.
    $budget=Get-BFValue $State.request 'budget'
    $provider=$null;$requestedModel=$null
    if($null -ne $budget -and $null -ne $ProviderContext){
        $profile=Get-BFValue $State.request 'execution_profile'
        $provider=[string]$profile.provider
        $requestedModel=if($Stage -in @('code_review','spec_review')){[string]$State.request.models.reviewer}else{[string]$State.request.models.worker}
        [void](Assert-BFProviderBudgetAdmission $State $ProviderContext $directory)
        [void](Add-BFProviderBudgetReservation $State $ProviderContext $directory $provider $Stage $requestedModel)
    }
    $processDirectory=Assert-BFSafePath (Join-Path $directory 'native-process')
    [void][IO.Directory]::CreateDirectory($processDirectory)
    $process=Invoke-BFProcess -Executable $CodexPath -Arguments @('-NoLogo','-NoProfile','-NonInteractive','-Command','Write-Output native-fixture-worker') -WorkingDirectory $State.worker_path -OutputDirectory $processDirectory -TimeoutSeconds 30 -Cancelled $Cancelled -MaxOutputBytes $MaxOutputBytes
    if($process.stop_reason -or $process.exit_code -ne 0){throw 'BF_BLOCKED: deterministic native fixture worker process did not finish.'}
    if($Stage -eq 'inspect'){
        $payload=[ordered]@{complexity='S';risk='low';impact_flags=@();rationale='The deterministic fixture has a small source-only scope.'}
    }elseif($Stage -eq 'spec'){
        $payload=[ordered]@{spec=$script:BFNativeFixtureSpec;design=$null}
    }elseif($Stage -eq 'spec_review'){
        # Raw reviewer payload: all-passing deterministic review consumed by the
        # real Complete-BSLFlowReview gate on the profiled critic route.
        $payload=[ordered]@{
            schema_version=1;reviewer_verdict='PASS';summary='Deterministic fixture review accepts the specification.';
            scores=[ordered]@{intent_fidelity=5;minimality=5;completeness=5;architecture_fit=5;testability=5;assumption_discipline=5;clarity=5};
            overengineering=[ordered]@{items=@()};findings=@();do_not_change=@();confidence=0.9
        }
    }elseif($Stage -eq 'spec_reconcile'){
        $payload=[ordered]@{spec=$script:BFNativeFixtureSpec;design=$null;decisions=@();do_not_change_checks=@()}
    }elseif($Stage -eq 'implement'){
        # The heal decision is state-driven: after a registered repair the state
        # view carries repair.diagnosis_attempt, and provider processes do not
        # share script scope between attempts.
        $repairDiagnosis=Get-BFValue (Get-BFValue $State 'repair') 'diagnosis_attempt'
        $changed=@()
        if($script:BFNativeFixtureNegative -and $null -eq $repairDiagnosis){
            $readme=Assert-BFSafePath (Join-Path $State.worker_path 'readme.txt')
            [IO.File]::WriteAllText($readme,'broken'+[Environment]::NewLine,(New-Object Text.UTF8Encoding($false)))
            $changed=@('readme.txt')
        }elseif($null -ne $repairDiagnosis){
            $readme=Assert-BFSafePath (Join-Path $State.worker_path 'readme.txt')
            [IO.File]::WriteAllText($readme,'fixture'+[Environment]::NewLine,(New-Object Text.UTF8Encoding($false)))
            $changed=@('readme.txt')
        }
        $payload=[ordered]@{changed_files=$changed}
    }elseif($Stage -eq 'code_review'){
        # A repaired implementation route requires an independent review; the
        # deterministic review accepts the healed single-file change.
        $payload=[ordered]@{verdict='PASS';findings=@()}
    }elseif($Stage -eq 'diagnose'){
        # Deterministic safe repair: accept the controller-verified failure.
        $payload=[ordered]@{
            failure_attempt_id=[string](Get-BFValue (Get-BFValue $State 'repair') 'pending_failure')
            category='implementation'
            reason='The deterministic implementation replaced the fixture marker with broken content.'
            evidence='The failed verification retained the exact broken readme evidence for this attempt.'
            fix_instructions='Restore the original fixture marker line in readme.txt.'
        }
    }else{throw 'BF_BLOCKED: deterministic native fixture worker was called for an unexpected stage.'}
    if($null -ne $budget -and $null -ne $ProviderContext){
        Write-BFJson -Path (Join-Path $directory 'host-result.json') -Value ([ordered]@{observed_model='native-fixture-worker';usage=[ordered]@{input_tokens=12;output_tokens=34};reported_cost_usd=0.25})
        [void](Complete-BFProviderBudgetDispatch $State $ProviderContext $directory $provider $Stage $requestedModel)
    }
    $payloadJson=Get-BFCanonicalJson $payload
    $modelResult=[ordered]@{schema_version=1;status='completed';summary=('Deterministic native fixture worker completed '+$Stage+'.');payload_json=$payloadJson}
    Write-BFJson -Path (Join-Path $directory 'model-result.json') -Value $modelResult
    return [pscustomobject]$modelResult
}
`, negativeLiteral)
}

func newNativeControllerToolset(t *testing.T) (string, string) {
	t.Helper()
	root := filepath.Join(nativeControllerTempDirectory(t), "toolset")
	skillRoot := filepath.Join(root, "fixture")
	if err := os.MkdirAll(skillRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	skillData := []byte("# deterministic native toolset fixture\n")
	nativeControllerWriteFile(t, filepath.Join(skillRoot, "README.md"), skillData)
	fileHash := nativeControllerSHA256(skillData)
	line := "fixture/README.md\x00" + fileHash + "\n"
	aggregate := nativeControllerSHA256([]byte(line))
	manifest := map[string]any{
		"schema_version": int64(1),
		"toolset_name":   "cc-1c-skills",
		"source":         map[string]any{"identity": "local-private", "path": root},
		"skills": []any{map[string]any{
			"name":   "fixture",
			"files":  []any{map[string]any{"path": "README.md", "sha256": fileHash}},
			"sha256": aggregate, "mcp_references": []any{},
		}},
		"aggregate_sha256": aggregate,
	}
	nativeControllerWriteFile(t, filepath.Join(root, "toolset-manifest.json"), nativeControllerJSON(t, manifest))
	return root, aggregate
}

func nativeControllerCopyTree(t *testing.T, source, destination string) {
	t.Helper()
	if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("source skill contains a non-regular file: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
}

func nativeControllerTempDirectory(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "bsl-flow-native-fixture-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func nativeControllerWriteJSON(t *testing.T, value any) string {
	t.Helper()
	path := filepath.Join(nativeControllerTempDirectory(t), "input.json")
	nativeControllerWriteFile(t, path, nativeControllerJSON(t, value))
	return path
}

func nativeControllerJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func nativeControllerWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func nativeControllerFileHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return nativeControllerSHA256(data)
}

func nativeControllerSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func nativeControllerReadController(t *testing.T, fixture nativeControllerIntegrationFixture) map[string]any {
	t.Helper()
	store, err := repository.OpenRepository(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.ReadTask(fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	controller, ok := task.State["controller"].(map[string]any)
	if !ok {
		t.Fatalf("task has no controller payload: %#v", task.State)
	}
	return controller
}

func nativeControllerEvidence(t *testing.T, controller map[string]any, stage string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, raw := range nativeControllerItems(controller["evidence"]) {
		item, ok := raw.(map[string]any)
		if ok && item["stage"] == stage {
			if found != nil {
				t.Fatalf("duplicate evidence for stage %s: %#v", stage, controller["evidence"])
			}
			found = item
		}
	}
	if found == nil {
		t.Fatalf("missing evidence for stage %s: %#v", stage, controller["evidence"])
	}
	return found
}

func nativeControllerItems(value any) []any {
	if value == nil {
		return nil
	}
	items, _ := value.([]any)
	return items
}

func nativeControllerString(value any) string {
	valueString, _ := value.(string)
	return valueString
}

func nativeControllerAssertJournal(t *testing.T, fixture nativeControllerIntegrationFixture, wantAttempts int, negative bool) {
	t.Helper()
	store, err := repository.OpenRepository(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.ReadTask(fixture.taskID)
	if err != nil {
		t.Fatal(err)
	}
	controller, ok := task.State["controller"].(map[string]any)
	if !ok {
		t.Fatalf("journal has no controller payload: %#v", task.State)
	}
	attemptValues := nativeControllerItems(controller["attempts"])
	if len(attemptValues) != wantAttempts {
		t.Fatalf("journal attempts=%d, want %d: %#v", len(attemptValues), wantAttempts, attemptValues)
	}
	attemptIDs := map[string]bool{}
	for _, raw := range attemptValues {
		attemptID, ok := raw.(string)
		if !ok || !nativeControllerUUID.MatchString(attemptID) || attemptIDs[attemptID] {
			t.Fatalf("journal has invalid or duplicate attempt id: %#v", raw)
		}
		attemptIDs[attemptID] = true
		attemptRoot := filepath.Join(store.StorePath, "tasks", fixture.taskID, "attempts", attemptID)
		for _, name := range []string{"start.json", "dispatch-admission.json", "budget-admission.json", "result.json", "terminal.json", "transport.json", "transport.stdout", "transport.stderr"} {
			path := filepath.Join(attemptRoot, name)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("attempt %s is missing %s: %v", attemptID, name, err)
			}
		}
		if info, err := os.Stat(filepath.Join(attemptRoot, "artifacts")); err != nil || !info.IsDir() {
			t.Fatalf("attempt %s has no artifact directory: %v", attemptID, err)
		}
		nativeControllerAssertTransport(t, fixture, attemptRoot, attemptID)
	}
	if negative {
		if controller["status"] != "failed" || len(nativeControllerItems(controller["acceptances"])) != 0 {
			t.Fatalf("negative journal crossed acceptance boundary: %#v", controller)
		}
	} else if controller["status"] != "completed" || len(nativeControllerItems(controller["acceptances"])) != 1 {
		t.Fatalf("positive journal is not completed exactly once: %#v", controller)
	}
	journalRoot := filepath.Join(store.StorePath, "tasks", fixture.taskID)
	err = filepath.WalkDir(journalRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), "NATIVE_FIXTURE_LEGACY_READ_TRIPWIRE") || strings.Contains(string(data), "NATIVE_FIXTURE_LEGACY_SAVE_TRIPWIRE") {
			return fmt.Errorf("legacy Read/Save tripwire reached: %s", path)
		}
		trimmed := bytes.TrimSpace(data)
		if len(trimmed) == 0 {
			return nil
		}
		var value any
		if json.Unmarshal(trimmed, &value) != nil {
			return nil // Native process stdout is intentionally plain text.
		}
		if err := nativeControllerAssertTaskIDs(value, fixture.taskID, path); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	worker, ok := controller["worker_path"].(string)
	if !ok || strings.TrimSpace(worker) == "" {
		t.Fatalf("controller worker path is missing: %#v", controller["worker_path"])
	}
	readme, err := os.ReadFile(filepath.Join(worker, "readme.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if negative && strings.Contains(string(readme), "fixture") {
		t.Fatalf("negative implementation did not change the worker source: %q", readme)
	}
	if !negative && !strings.Contains(string(readme), "fixture") {
		t.Fatalf("positive implementation changed the worker source unexpectedly: %q", readme)
	}
}

func nativeControllerAssertTaskIDs(value any, taskID, source string) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "task_id" || key == "request_id" {
				if got, ok := child.(string); !ok || got != taskID {
					return fmt.Errorf("journal %s contains %s=%v, want %s", source, key, child, taskID)
				}
			}
			if err := nativeControllerAssertTaskIDs(child, taskID, source); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := nativeControllerAssertTaskIDs(child, taskID, source); err != nil {
				return err
			}
		}
	}
	return nil
}

func nativeControllerAssertTransport(t *testing.T, fixture nativeControllerIntegrationFixture, attemptRoot, attemptID string) {
	t.Helper()
	startData, err := os.ReadFile(filepath.Join(attemptRoot, "start.json"))
	if err != nil {
		t.Fatal(err)
	}
	var start map[string]any
	if err := json.Unmarshal(startData, &start); err != nil {
		t.Fatalf("invalid attempt start for %s: %v", attemptID, err)
	}
	stage := nativeControllerString(start["stage"])
	transportData, err := os.ReadFile(filepath.Join(attemptRoot, "transport.json"))
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(transportData, &envelope); err != nil {
		t.Fatalf("invalid native transport envelope for %s: %v", attemptID, err)
	}
	receipt, ok := envelope["receipt"].(map[string]any)
	if !ok {
		t.Fatalf("native transport has no receipt for %s: %#v", attemptID, envelope)
	}
	if receipt["started"] != true || receipt["terminal"] != true || nativeControllerNumber(receipt["exit_code"]) != 0 || nativeControllerString(receipt["stop_reason"]) != "" {
		t.Fatalf("native outer provider receipt is not a successful terminal process for %s: %#v", attemptID, receipt)
	}
	executable, ok := receipt["executable"].(string)
	if !ok || !strings.EqualFold(filepath.Clean(executable), filepath.Clean(fixture.provider.shell)) || !strings.HasSuffix(strings.ToLower(executable), "pwsh.exe") {
		t.Fatalf("outer receipt executable=%q, want provider PowerShell %q", executable, fixture.provider.shell)
	}
	argv, ok := receipt["argv"].([]any)
	if !ok {
		t.Fatalf("outer provider argv is missing for %s: %#v", attemptID, receipt)
	}
	hasFile := false
	for _, raw := range argv {
		if raw == "-File" {
			hasFile = true
		}
	}
	if !hasFile {
		t.Fatalf("outer provider did not use the fixed -File entrypoint for %s: %#v", attemptID, argv)
	}
	stdout, err := os.ReadFile(filepath.Join(attemptRoot, "transport.stdout"))
	if err != nil {
		t.Fatal(err)
	}
	if receipt["stdout_sha256"] != nativeControllerSHA256(stdout) {
		t.Fatalf("outer stdout hash does not bind retained bytes for %s", attemptID)
	}
	var observation map[string]any
	if err := json.Unmarshal(stdout, &observation); err != nil {
		t.Fatalf("native provider stdout is not a JSON observation for %s: %v", attemptID, err)
	}
	if observation["task_id"] != fixture.taskID || observation["attempt_id"] != attemptID {
		t.Fatalf("native provider observation identity mismatch for %s: %#v", attemptID, observation)
	}
	result, err := os.ReadFile(filepath.Join(attemptRoot, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var resultObject map[string]any
	if err := json.Unmarshal(result, &resultObject); err != nil {
		t.Fatalf("invalid retained provider result for %s: %v", attemptID, err)
	}
	if _, ok := resultObject["artifacts"].([]any); !ok {
		t.Fatalf("provider result artifacts must be an array for %s: %#v", attemptID, resultObject["artifacts"])
	}
	processReceipt, ok := resultObject["process_receipt"].(map[string]any)
	if !ok {
		t.Fatalf("provider result lacks a process receipt object for %s: %#v", attemptID, resultObject)
	}
	artifacts := nativeControllerItems(resultObject["artifacts"])
	if stage == "verify" {
		// Deterministic verification of file-assertion criteria runs no worker
		// process; its binding evidence is the retained observations artifact,
		// or the failure record when a criterion did not hold.
		wantPath := "raw/observations.json"
		if nativeControllerString(resultObject["status"]) == "failed" {
			wantPath = "raw/failure.json"
		}
		hasEvidence := false
		for _, raw := range artifacts {
			artifact, ok := raw.(map[string]any)
			if ok && nativeControllerString(artifact["path"]) == wantPath {
				hasEvidence = true
			}
		}
		if !hasEvidence {
			t.Fatalf("deterministic verify result did not retain %s for %s: %#v", wantPath, attemptID, artifacts)
		}
		return
	}
	if len(nativeControllerItems(processReceipt["processes"])) == 0 {
		t.Fatalf("provider result lacks an inner process receipt for %s: %#v", attemptID, resultObject)
	}
	for _, raw := range nativeControllerItems(processReceipt["processes"]) {
		process, ok := raw.(map[string]any)
		if !ok || nativeControllerNumber(process["exit_code"]) != 0 || (process["stop_reason"] != nil && nativeControllerString(process["stop_reason"]) != "") {
			t.Fatalf("inner provider worker process is not successful for %s: %#v", attemptID, process)
		}
	}
	hasProcessArtifact := false
	for _, raw := range artifacts {
		artifact, ok := raw.(map[string]any)
		if ok && strings.Contains(strings.ToLower(nativeControllerString(artifact["path"])), "process.json") {
			hasProcessArtifact = true
		}
	}
	if !hasProcessArtifact {
		t.Fatalf("provider result did not retain the real worker process artifact for %s: %#v", attemptID, artifacts)
	}
}

func nativeControllerNumber(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	default:
		return -1
	}
}

func nativeControllerAssertMediumReviewEvidence(t *testing.T, fixture nativeControllerIntegrationFixture) {
	t.Helper()
	change := filepath.Join(fixture.project, "openspec", "changes", "bsl-flow-"+fixture.taskID)
	review := nativeControllerReadChangeJSON(t, fixture, filepath.Join(change, "review.json"))
	if asIntOr(review["schema_version"]) != 1 || review["verdict"] != "PASS" || asIntOr(review["review_iteration"]) != 1 {
		t.Fatalf("medium review is not a first-pass accepted v1 review: %#v", review)
	}
	reconciliation := nativeControllerReadChangeJSON(t, fixture, filepath.Join(change, "review-reconciliation.json"))
	if reconciliation["draft_spec_sha256"] != reconciliation["final_spec_sha256"] || len(nativeControllerItems(reconciliation["decisions"])) != 0 {
		t.Fatalf("medium reconciliation changed the accepted specification: %#v", reconciliation)
	}
	final := nativeControllerReadChangeJSON(t, fixture, filepath.Join(change, "final-validation.json"))
	if asIntOr(final["schema_version"]) != 1 || final["passed"] != true || len(nativeControllerItems(final["errors"])) != 0 {
		t.Fatalf("medium final validation did not pass: %#v", final)
	}
	spec, err := os.ReadFile(filepath.Join(change, "spec.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(spec, []byte("- Complexity: M")) || !bytes.Contains(spec, []byte("- Risk: low")) || !bytes.Contains(spec, []byte("## Goal")) {
		t.Fatalf("retained medium specification is incomplete: %q", spec)
	}
	store, err := repository.OpenRepository(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	controller := nativeControllerReadController(t, fixture)
	reservations, outcomes := 0, 0
	for _, raw := range nativeControllerItems(controller["attempts"]) {
		attemptID, ok := raw.(string)
		if !ok {
			t.Fatalf("controller attempts contain a non-string id: %#v", raw)
		}
		data, err := os.ReadFile(filepath.Join(store.StorePath, "tasks", fixture.taskID, "attempts", attemptID, "artifacts", "budget", "ledger.json"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		var ledger map[string]any
		if err := json.Unmarshal(data, &ledger); err != nil {
			t.Fatalf("invalid retained budget ledger for %s: %v", attemptID, err)
		}
		reservations, outcomes = 0, 0
		for _, entryRaw := range nativeControllerItems(ledger["entries"]) {
			entry, ok := entryRaw.(map[string]any)
			if !ok {
				t.Fatalf("invalid budget entry in ledger for %s: %#v", attemptID, entryRaw)
			}
			switch entry["kind"] {
			case "reservation":
				reservations++
			case "outcome":
				outcomes++
				if entry["cost_state"] != "known" || nativeControllerNumber(entry["reported_cost_usd"]) != 0.25 {
					t.Fatalf("budget outcome is not the deterministic known fixture cost: %#v", entry)
				}
			default:
				t.Fatalf("unexpected budget entry kind: %#v", entry)
			}
		}
	}
	// inspect, spec, spec_review critic and reconciler, implement: five paid
	// dispatches, each admitted, reserved and resolved with retained evidence.
	if reservations != 5 || outcomes != 5 {
		t.Fatalf("cumulative provider budget has %d reservations and %d outcomes, want 5/5", reservations, outcomes)
	}
}

func nativeControllerReadChangeJSON(t *testing.T, fixture nativeControllerIntegrationFixture, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("medium change artifact is missing (%s): %v", path, err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("invalid medium change artifact %s: %v", path, err)
	}
	return value
}

// nativeControllerRunRepairLifecycle drives inspect → implement (broken) →
// verify FAIL → diagnose REPAIR → implement (healed) → code_review PASS →
// verify PASS → accept. The repair round adds the mandatory code review.
func nativeControllerRunRepairLifecycle(t *testing.T, fixture nativeControllerIntegrationFixture) {
	t.Helper()
	wantOutcomes := []string{"PASS", "PASS", "FAIL", "REPAIR", "PASS", "PASS", "PASS"}
	seenStages := []string{}
	for step := 0; step < 12; step++ {
		nextResponse, code, stderr := nativeControllerDispatch(t, fixture.host, []string{
			"task", "next", "--project", fixture.project, "--task", fixture.taskID, "--engine", "native",
		})
		if code != 0 {
			t.Fatalf("repair next step=%d exit=%d stderr=%q response=%#v", step, code, stderr, nextResponse)
		}
		next, ok := nextResponse["next"].(map[string]any)
		if !ok {
			t.Fatalf("repair next has no closed next object: %#v", nextResponse)
		}
		switch action := next["action"]; action {
		case "dispatch":
			stage := nativeControllerString(next["stage"])
			seenStages = append(seenStages, stage)
			if len(seenStages) > len(wantOutcomes) {
				t.Fatalf("repair route dispatched more stages than expected: %v", seenStages)
			}
			runResponse, runCode, runStderr := nativeControllerDispatch(t, fixture.host, []string{
				"task", "run", "--project", fixture.project, "--task", fixture.taskID, "--engine", "native",
			})
			if runCode != 0 {
				t.Logf("native provider attempt diagnostics:\n%s", nativeControllerAttemptDiagnostics(t, fixture))
				t.Fatalf("repair run stage=%s exit=%d stderr=%q response=%#v", stage, runCode, runStderr, runResponse)
			}
			outcome, summary := nativeControllerLatestEvidence(t, fixture, stage)
			if want := wantOutcomes[len(seenStages)-1]; outcome != want {
				t.Fatalf("repair stage %s occurrence %d outcome=%s, want %s; summary=%s", stage, len(seenStages), outcome, want, summary)
			}
		case "accept":
			accepted, acceptCode, acceptStderr := nativeControllerDispatch(t, fixture.host, []string{
				"task", "accept", "--project", fixture.project, "--task", fixture.taskID, "--engine", "native",
			})
			if acceptCode != 0 || accepted["task_id"] != fixture.taskID || accepted["status"] != "completed" {
				t.Fatalf("repair acceptance exit=%d stderr=%q response=%#v", acceptCode, acceptStderr, accepted)
			}
			if want := "inspect,implement,verify,diagnose,implement,code_review,verify"; !strings.EqualFold(strings.Join(seenStages, ","), want) {
				t.Fatalf("repair route stages=%v, want %s", seenStages, want)
			}
			return
		default:
			t.Fatalf("unexpected repair lifecycle action at step=%d: %#v", step, next)
		}
	}
	t.Fatalf("repair lifecycle did not reach acceptance")
}

func nativeControllerLatestEvidence(t *testing.T, fixture nativeControllerIntegrationFixture, stage string) (string, string) {
	t.Helper()
	controller := nativeControllerReadController(t, fixture)
	outcome := ""
	summary := ""
	for _, raw := range nativeControllerItems(controller["evidence"]) {
		entry, ok := raw.(map[string]any)
		if ok && entry["stage"] == stage {
			outcome = nativeControllerString(entry["outcome"])
			summary = nativeControllerString(entry["summary"])
		}
	}
	if outcome == "" {
		t.Fatalf("no evidence recorded for stage %s: %#v", stage, controller["evidence"])
	}
	return outcome, summary
}

// nativeControllerAssertMemoryBridgeOutcome verifies the advisory memory
// envelopes retained in every attempt start. With wantAvailable the real
// PowerShell bridge must have accepted each bind — including the diagnose bind
// that carries the controller-verified failed result. Without it the helper is
// broken on purpose and every attempt must retain a disabled envelope with the
// helper failure reason while the lifecycle still completed.
func nativeControllerAssertMemoryBridgeOutcome(t *testing.T, fixture nativeControllerIntegrationFixture, wantAvailable bool) {
	t.Helper()
	store, err := repository.OpenRepository(fixture.project)
	if err != nil {
		t.Fatal(err)
	}
	controller := nativeControllerReadController(t, fixture)
	for _, raw := range nativeControllerItems(controller["attempts"]) {
		attemptID, ok := raw.(string)
		if !ok {
			t.Fatalf("controller attempts contain a non-string id: %#v", raw)
		}
		data, err := os.ReadFile(filepath.Join(store.StorePath, "tasks", fixture.taskID, "attempts", attemptID, "start.json"))
		if err != nil {
			t.Fatal(err)
		}
		var start map[string]any
		if err := json.Unmarshal(data, &start); err != nil {
			t.Fatalf("invalid attempt start for %s: %v", attemptID, err)
		}
		memory, ok := start["memory"].(map[string]any)
		if !ok {
			t.Fatalf("attempt %s start has no memory envelope: %#v", attemptID, start["memory"])
		}
		if asBoolOr(memory["available"]) != wantAvailable {
			t.Fatalf("attempt %s memory envelope available=%v want %v: %#v", attemptID, memory["available"], wantAvailable, memory)
		}
		if wantAvailable {
			if reason := nativeControllerString(memory["disabled_reason"]); reason != "" {
				t.Fatalf("available memory envelope kept a disabled reason: %#v", memory)
			}
		} else if reason := nativeControllerString(memory["disabled_reason"]); !strings.Contains(reason, "FIXTURE_MEMORY_HELPER_BROKEN") && !strings.Contains(reason, "memory helper did not complete") {
			// The real bridge converts a helper failure into a closed
			// available=false envelope with the exact reason; a process-level
			// failure surfaces through Go as a helper-completion error. Both
			// must keep the envelope advisory, never block the lifecycle.
			t.Fatalf("broken memory bridge did not surface its failure reason: %#v", memory)
		}
	}
}

func asBoolOr(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func asIntOr(value any) int64 {
	number, ok := value.(float64)
	if !ok {
		return -1
	}
	return int64(number)
}

func nativeControllerAttemptDiagnostics(t *testing.T, fixture nativeControllerIntegrationFixture) string {
	t.Helper()
	store, err := repository.OpenRepository(fixture.project)
	if err != nil {
		return fmt.Sprintf("open repository: %v", err)
	}
	root := filepath.Join(store.StorePath, "tasks", fixture.taskID, "attempts")
	var lines []string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		name := filepath.Base(path)
		if name != "transport.stderr" && name != "transport.stdout" && name != "failure.json" && name != "result.json" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if len(data) > 8000 {
			data = data[:8000]
		}
		lines = append(lines, fmt.Sprintf("%s:\n%s", path, string(data)))
		return nil
	})
	if err != nil {
		lines = append(lines, fmt.Sprintf("walk attempts: %v", err))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}
