package stagehost

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"bsl-flow/cli/internal/worker"
)

// TestProviderStagePromptPowerShellParity renders the full stage prompt
// (Get-BFStagePrompt + Get-BFToolsetPrompt) through both engines over one
// fixture and requires byte-identical text: the prompt hash binds the sealed
// worker input, so this is the read-only differential evidence for the
// migrated stage calculation. It skips when pwsh or the scripts are absent.
func TestProviderStagePromptPowerShellParity(t *testing.T) {
	if parityRepoScriptsDir(t) == "" {
		t.Skip("packaged skill scripts are unavailable")
	}
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("pwsh unavailable")
	}
	fixture := newWorkerParityFixture(t)
	defer fixture.cleanup()
	if err := worker.AssertWorkerConfiguration(fixture.worktree); err != nil {
		t.Skipf("fixture: %v", err)
	}
	deps := fixture.deps()
	prompt, err := stagePrompt(deps, fixture.state, "inspect", "", fixture.attempt)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := workerProfileFromState(asMap(asMap(fixture.state["request"]))["execution_profile"])
	if err != nil {
		t.Fatal(err)
	}
	toolset, err := worker.ToolsetPrompt(profile)
	if err != nil {
		t.Fatal(err)
	}
	goText := prompt + toolset

	script := `
param([string]$ScriptsRoot,[string]$StateFile,[string]$OutFile)
Set-StrictMode -Version Latest
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Memory.ps1','Task.Architecture.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Execution.ps1','Task.Stages.ps1')){ . (Join-Path $ScriptsRoot $name) }
$doc = Get-Content -Raw -LiteralPath $StateFile | ConvertFrom-Json -AsHashtable
$state = $doc.state_view
$attempt = $doc.attempt
$text = Get-BFStagePrompt $state 'inspect' '' $attempt ''
[IO.File]::WriteAllText($OutFile, $text + (Get-BFToolsetPrompt $state), [Text.UTF8Encoding]::new($false))
`
	scriptPath := filepath.Join(fixture.root, "render.ps1")
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(fixture.root, "input.json")
	if err := os.WriteFile(stateFile, fixture.input, 0o644); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(fixture.root, "ps-prompt.txt")
	command := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath,
		filepath.Join(fixture.packageRoot, "global", "skills", "1c-task", "scripts"), stateFile, outFile)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("pwsh render: %v: %s", err, stderr.String())
	}
	psBytes, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatal(err)
	}
	psText := string(psBytes)
	if psText == goText {
		t.Log("prompts identical")
		return
	}
	offset := 0
	for offset < len(psText) && offset < len(goText) && psText[offset] == goText[offset] {
		offset++
	}
	start := offset - 200
	if start < 0 {
		start = 0
	}
	psWindow := psText[start:]
	goWindow := goText[start:]
	if len(psWindow) > 400 {
		psWindow = psWindow[:400]
	}
	if len(goWindow) > 400 {
		goWindow = goWindow[:400]
	}
	t.Fatalf("prompt diverged at offset %d (ps len %d, go len %d):\n--- ps ---\n%q\n--- go ---\n%q", offset, len(psText), len(goText), psWindow, goWindow)
}
