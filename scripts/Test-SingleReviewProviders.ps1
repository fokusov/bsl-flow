#Requires -Version 7.0
[CmdletBinding()]
param(
    [string]$PackageRoot
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path) }
$reviewSkill = Join-Path $PackageRoot 'global\skills\1c-spec-review'
. (Join-Path $reviewSkill 'scripts\Review.Common.ps1')
. (Join-Path $reviewSkill 'scripts\Review.Providers.ps1')
$invokePath = Join-Path $reviewSkill 'scripts\Invoke-1CSpecReview.ps1'

$passed = 0
function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "FAIL: $Message" }
    $script:passed++
    "PASS $Message"
}
function Assert-Throws([scriptblock]$Block, [string]$Needle, [string]$Message) {
    try { & $Block } catch {
        if ([string]$_.Exception.Message -notmatch [regex]::Escape($Needle)) { throw "FAIL ${Message}: wrong error: $($_.Exception.Message)" }
        $script:passed++; "PASS $Message"; return
    }
    throw "FAIL (no throw): $Message"
}

$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-single-review-providers-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tempRoot -Force | Out-Null
try {
    # ---------------------------------------------------------------------
    # A single fake CLI stands in for both `claude` and `claude`-like tools:
    # its behaviour is switched by FAKE_CLI_ROLE/FAKE_CLI_MODE environment
    # variables, so one compiled binary covers every claude_cli/codex_exec
    # fixture without a paid model or network access, like the fake OpenCode
    # provider in Test-ReviewReliability.ps1.
    # ---------------------------------------------------------------------
    $sourcePath = Join-Path $tempRoot 'fake-cli.cs'
    $source = @'
using System;
using System.IO;
using System.Text;
class FakeCli {
  static string Valid = "{\"schema_version\":1,\"reviewer_verdict\":\"PASS\",\"summary\":\"fixture\",\"scores\":{\"intent_fidelity\":5,\"minimality\":5,\"completeness\":5,\"architecture_fit\":5,\"testability\":5,\"assumption_discipline\":5,\"clarity\":5},\"overengineering\":{\"items\":[]},\"findings\":[],\"do_not_change\":[],\"confidence\":0.9}";
  static void Main(string[] args) {
    Console.OutputEncoding = new UTF8Encoding(false);
    string role = Environment.GetEnvironmentVariable("FAKE_CLI_ROLE") ?? "claude";
    string mode = Environment.GetEnvironmentVariable("FAKE_CLI_MODE") ?? "valid";
    if (args.Length > 0 && args[0] == "--version") { Console.WriteLine(role + " 1.0.0 (fake)"); return; }
    if (args.Length > 0 && args[0] == "--help") {
      string help;
      if (role == "claude") {
        help = "Usage: claude [options]\n  -p, --print\n  --output-format <format>\n  --model <model>\n  --allowedTools, --allowed-tools <tools...>\n  --disallowedTools, --disallowed-tools <tools...>\n  --strict-mcp-config\n";
        if (mode == "missing_flag") help = help.Replace("--strict-mcp-config\n", "");
      } else {
        help = "Usage: codex exec [OPTIONS]\n  --sandbox <MODE>\n  --skip-git-repo-check\n  --ignore-user-config\n  --json\n  --output-schema <FILE>\n  --output-last-message <FILE>\n  -m, --model <MODEL>\n";
        if (mode == "missing_flag") help = help.Replace("--output-schema <FILE>\n", "");
      }
      Console.WriteLine(help);
      return;
    }
    using (var input = Console.OpenStandardInput())
    using (var buffer = new MemoryStream()) { input.CopyTo(buffer); }
    if (mode == "failure") { Console.Error.WriteLine("fixture failure"); Environment.Exit(1); return; }
    if (role == "claude") {
      if (mode == "is_error") { Console.WriteLine("{\"type\":\"result\",\"is_error\":true,\"result\":\"boom\",\"session_id\":\"s1\"}"); return; }
      if (mode == "not_json") { Console.WriteLine("not one JSON envelope at all"); return; }
      string escaped = Valid.Replace("\\", "\\\\").Replace("\"", "\\\"");
      Console.WriteLine("{\"type\":\"result\",\"is_error\":false,\"result\":\"" + escaped + "\",\"session_id\":\"s1\",\"usage\":{\"input_tokens\":10,\"output_tokens\":5},\"modelUsage\":{\"claude-fake-observed\":{}}}");
      return;
    }
    string lastMessagePath = null;
    for (int i = 0; i < args.Length - 1; i++) { if (args[i] == "--output-last-message") { lastMessagePath = args[i + 1]; break; } }
    if (mode == "not_json") { if (lastMessagePath != null) File.WriteAllText(lastMessagePath, "not one JSON object"); return; }
    if (lastMessagePath != null) File.WriteAllText(lastMessagePath, Valid, new UTF8Encoding(false));
    Console.WriteLine("{\"type\":\"event\"}");
  }
}
'@
    [IO.File]::WriteAllText($sourcePath, $source, (New-Object Text.UTF8Encoding($false)))
    $dotnet = Get-Command dotnet.exe -ErrorAction SilentlyContinue
    Assert-True ($null -ne $dotnet) 'dotnet available for fake CLI'
    $sdkLines = @(& $dotnet.Source --list-sdks)
    $sdkMajor = ($sdkLines | ForEach-Object { if ($_ -match '^\s*(\d+)\.') { [int]$Matches[1] } } | Sort-Object -Descending | Select-Object -First 1)
    Assert-True ($sdkMajor -ge 5) 'supported .NET SDK available for fake CLI'
    $targetFramework = "net$sdkMajor.0"
    $projectPath = Join-Path $tempRoot 'fake-cli.csproj'
    $project = '<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>{0}</TargetFramework><AssemblyName>fake-cli</AssemblyName><EnableDefaultCompileItems>false</EnableDefaultCompileItems></PropertyGroup><ItemGroup><Compile Include="fake-cli.cs" /></ItemGroup></Project>' -f $targetFramework
    [IO.File]::WriteAllText($projectPath, $project, (New-Object Text.UTF8Encoding($false)))
    $nugetConfig = Join-Path $tempRoot 'NuGet.Config'
    [IO.File]::WriteAllText($nugetConfig, '<configuration><packageSources><clear /></packageSources></configuration>')
    $buildOutput = @(& $dotnet.Source build $projectPath '--nologo' '--configuration' 'Release' '--output' $tempRoot "-p:RestoreConfigFile=$nugetConfig")
    if ($LASTEXITCODE -ne 0) { throw "Fake CLI build failed: $($buildOutput -join ' ')" }
    $fakeCliPath = Join-Path $tempRoot 'fake-cli.exe'
    Assert-True (Test-Path -LiteralPath $fakeCliPath -PathType Leaf) 'fake CLI compiled'

    function Set-FakeCli([string]$Role, [string]$Mode) {
        $env:FAKE_CLI_ROLE = $Role
        $env:FAKE_CLI_MODE = $Mode
    }

    $runRoot = Join-Path $tempRoot 'run'
    New-Item -ItemType Directory -Path $runRoot -Force | Out-Null
    $rawResponsePath = Join-Path $runRoot 'raw-response.txt'
    $stderrPath = Join-Path $runRoot 'provider-stderr.log'
    $envelope = '<<<BEGIN UNTRUSTED DATA: DRAFT SPEC>>>' + "`nfixture`n" + '<<<END UNTRUSTED DATA: DRAFT SPEC>>>'

    # --- claude_cli ------------------------------------------------------
    Set-FakeCli 'claude' 'valid'
    $claudeOk = Invoke-BSLFlowClaudeCliSingleReview -ProjectRoot $tempRoot -ContextEnvelope $envelope -Model 'claude-fake' -TimeoutSeconds 10 -MaxOutputBytes 1048576 -RawResponsePath $rawResponsePath -StderrPath $stderrPath -ClaudeCliPath $fakeCliPath
    Assert-True (-not [bool]$claudeOk.Failed) 'claude_cli valid envelope succeeds'
    Assert-True ($claudeOk.RawReview.reviewer_verdict -eq 'PASS') 'claude_cli extracts the raw review payload from the result field'
    Assert-True ($claudeOk.Provider -eq 'claude_cli') 'claude_cli success reports its provider name'
    Assert-True ($claudeOk.ObservedModel -eq 'claude-fake-observed') 'claude_cli reports the observed model from modelUsage'
    Assert-True ((Get-Content -Raw -LiteralPath $rawResponsePath).Trim().Length -gt 0) 'claude_cli retains the raw stdout envelope'

    Set-FakeCli 'claude' 'is_error'
    $claudeErr = Invoke-BSLFlowClaudeCliSingleReview -ProjectRoot $tempRoot -ContextEnvelope $envelope -Model 'claude-fake' -TimeoutSeconds 10 -MaxOutputBytes 1048576 -RawResponsePath $rawResponsePath -StderrPath $stderrPath -ClaudeCliPath $fakeCliPath
    Assert-True ([bool]$claudeErr.Failed -and $claudeErr.FailurePhase -eq 'provider_failed') 'claude_cli is_error envelope fails with provider_failed'

    Set-FakeCli 'claude' 'missing_flag'
    Assert-Throws { Invoke-BSLFlowClaudeCliSingleReview -ProjectRoot $tempRoot -ContextEnvelope $envelope -Model 'claude-fake' -TimeoutSeconds 10 -MaxOutputBytes 1048576 -RawResponsePath $rawResponsePath -StderrPath $stderrPath -ClaudeCliPath $fakeCliPath } 'BF_BLOCKED' 'claude_cli missing --help flag is BF_BLOCKED, not a guess'

    # --- codex_exec -------------------------------------------------------
    $reviewSchemaPath = Join-Path $reviewSkill 'references\review-schema.json'
    Set-FakeCli 'codex' 'valid'
    $codexOk = Invoke-BSLFlowCodexExecSingleReview -ProjectRoot $tempRoot -ContextEnvelope $envelope -Model 'codex-fake' -TimeoutSeconds 10 -MaxOutputBytes 1048576 -RawResponsePath $rawResponsePath -StderrPath $stderrPath -ReviewSchemaPath $reviewSchemaPath -CodexCliPath $fakeCliPath
    Assert-True (-not [bool]$codexOk.Failed) 'codex_exec valid last-message succeeds'
    Assert-True ($codexOk.RawReview.reviewer_verdict -eq 'PASS') 'codex_exec extracts the raw review payload from --output-last-message'
    Assert-True ($codexOk.Isolation -eq 'os_sandbox') 'codex_exec reports os_sandbox isolation'

    Set-FakeCli 'codex' 'failure'
    $codexFail = Invoke-BSLFlowCodexExecSingleReview -ProjectRoot $tempRoot -ContextEnvelope $envelope -Model 'codex-fake' -TimeoutSeconds 10 -MaxOutputBytes 1048576 -RawResponsePath $rawResponsePath -StderrPath $stderrPath -ReviewSchemaPath $reviewSchemaPath -CodexCliPath $fakeCliPath
    Assert-True ([bool]$codexFail.Failed -and $codexFail.FailurePhase -eq 'provider_failed') 'codex_exec non-zero exit fails with provider_failed'

    Remove-Item Env:FAKE_CLI_ROLE, Env:FAKE_CLI_MODE -ErrorAction SilentlyContinue

    # --- api ---------------------------------------------------------------
    . (Join-Path $reviewSkill 'scripts\Council.Common.ps1')
    . (Join-Path $reviewSkill 'scripts\Council.Transport.ps1')
    $apiChatBody = '{"id":"chatcmpl-1","model":"deepseek-chat-resolved","usage":{"prompt_tokens":10,"completion_tokens":5},"choices":[{"message":{"role":"assistant","content":"{\"schema_version\":1,\"reviewer_verdict\":\"PASS\",\"summary\":\"fixture\",\"scores\":{\"intent_fidelity\":5,\"minimality\":5,\"completeness\":5,\"architecture_fit\":5,\"testability\":5,\"assumption_discipline\":5,\"clarity\":5},\"overengineering\":{\"items\":[]},\"findings\":[],\"do_not_change\":[],\"confidence\":0.9}"},"finish_reason":"stop"}]}'
    $fakeSend = { param($u, $b, $t, $to, $m, $c) [pscustomobject][ordered]@{ status = 200; body = $apiChatBody } }.GetNewClosure()
    $councilRouting = [ordered]@{
        allow_local_http = $false
        providers = [ordered]@{ deepseek = [ordered]@{ protocol = 'openai_compatible'; token_env = 'BSL_FLOW_TEST_REVIEWER_TOKEN'; endpoint = [pscustomobject][ordered]@{ scheme = 'https'; host = 'api.deepseek.com'; port = 443; base_path = '/' } } }
        models = [ordered]@{ 'review-strong' = [ordered]@{ provider = 'deepseek'; model = 'deepseek-chat'; effort = 'medium' } }
    }
    $env:BSL_FLOW_TEST_REVIEWER_TOKEN = 'fixture-token'
    $apiAttemptDir = Join-Path $tempRoot 'api-attempt'
    $apiOk = Invoke-BSLFlowApiSingleReview -ProjectRoot $tempRoot -ContextEnvelope $envelope -Model 'review-strong' -CouncilRouting $councilRouting -TimeoutSeconds 10 -MaxOutputBytes 1048576 -RawResponsePath (Join-Path $tempRoot 'api-raw.txt') -AttemptDir $apiAttemptDir -HttpSend $fakeSend
    Assert-True (-not [bool]$apiOk.Failed) 'api provider succeeds against a mocked council transport'
    Assert-True ($apiOk.RawReview.reviewer_verdict -eq 'PASS') 'api provider extracts the raw review payload from the chat envelope'
    Assert-True ($apiOk.ObservedModel -eq 'deepseek-chat-resolved') 'api provider reports the provider-observed model'
    Assert-True ($apiOk.Isolation -eq 'attached_only') 'api provider reports attached_only isolation'
    Remove-Item Env:BSL_FLOW_TEST_REVIEWER_TOKEN -ErrorAction SilentlyContinue

    Assert-Throws { Invoke-BSLFlowApiSingleReview -ProjectRoot $tempRoot -ContextEnvelope $envelope -Model 'unbound-profile' -CouncilRouting $councilRouting -TimeoutSeconds 10 -MaxOutputBytes 1048576 -RawResponsePath (Join-Path $tempRoot 'api-raw2.txt') -AttemptDir (Join-Path $tempRoot 'api-attempt2') -HttpSend $fakeSend } 'BF_BLOCKED' 'api provider with an unbound model profile is BF_BLOCKED'

    $env:BSL_FLOW_TEST_REVIEWER_TOKEN = $null
    Assert-Throws { Invoke-BSLFlowApiSingleReview -ProjectRoot $tempRoot -ContextEnvelope $envelope -Model 'review-strong' -CouncilRouting $councilRouting -TimeoutSeconds 10 -MaxOutputBytes 1048576 -RawResponsePath (Join-Path $tempRoot 'api-raw3.txt') -AttemptDir (Join-Path $tempRoot 'api-attempt3') -HttpSend $fakeSend } 'BF_BLOCKED' 'api provider without a credential is BF_BLOCKED'

    # --- claude_subagent -----------------------------------------------
    $blocked = Invoke-BSLFlowClaudeSubagentSingleReview -ChangeName 'fixture-change'
    Assert-True ([bool]$blocked.Failed -and $blocked.FailureMessage -match 'BF_BLOCKED' -and $blocked.FailureMessage.Contains('.bsl-flow/reports/spec-review/fixture-change.subagent/raw.json') -and $blocked.FailureMessage.Contains('-ImportRaw')) 'claude_subagent without -ImportRaw is BF_BLOCKED and names the exact expected path'

    $subagentDir = Join-Path $tempRoot 'subagent'
    New-Item -ItemType Directory -Path $subagentDir -Force | Out-Null
    $validRawPath = Join-Path $subagentDir 'raw.json'
    $validRaw = [ordered]@{
        schema_version = 1; reviewer_verdict = 'PASS'; summary = 'fixture'
        scores = [ordered]@{ intent_fidelity = 5; minimality = 5; completeness = 5; architecture_fit = 5; testability = 5; assumption_discipline = 5; clarity = 5 }
        overengineering = [ordered]@{ items = @() }; findings = @(); do_not_change = @(); confidence = 0.9
        observed_model = 'claude-opus-fixture'
    }
    Write-BSLFlowJsonAtomic -Value $validRaw -Path $validRawPath
    $imported = Invoke-BSLFlowClaudeSubagentSingleReview -ChangeName 'fixture-change' -ImportRawPath $validRawPath
    Assert-True (-not [bool]$imported.Failed) 'claude_subagent -ImportRaw with a valid raw review succeeds'
    Assert-True ($imported.RawReview.reviewer_verdict -eq 'PASS') 'claude_subagent imports the exact raw review payload'
    Assert-True ($imported.Isolation -eq 'host_subagent') 'claude_subagent reports host_subagent isolation'
    Assert-True ($imported.ObservedModel -eq 'claude-opus-fixture') 'claude_subagent surfaces the observed_model the subagent recorded'

    $invalidRawPath = Join-Path $subagentDir 'invalid-raw.json'
    [IO.File]::WriteAllText($invalidRawPath, '{not json', (New-Object Text.UTF8Encoding($false)))
    $invalidImport = Invoke-BSLFlowClaudeSubagentSingleReview -ChangeName 'fixture-change' -ImportRawPath $invalidRawPath
    Assert-True ([bool]$invalidImport.Failed -and $invalidImport.FailurePhase -eq 'parsing') 'claude_subagent -ImportRaw with malformed JSON fails at parsing, not silently'

    # --- end-to-end: missing model is a clear configuration error, not a
    # guessed default (Ф1.1/Ф1.4 removed the deepseek/deepseek-v4-pro default) --
    $missingModelProject = Join-Path $tempRoot 'missing-model-project'
    $missingModelChange = Join-Path $missingModelProject 'openspec\changes\fixture'
    New-Item -ItemType Directory -Path $missingModelChange -Force | Out-Null
    $missingModelSpec = @'
# review-fixture

## Classification
- Complexity: M
- Risk: medium

## Goal
Fixture.

## Current behavior
Fixture.

## Required behavior
1. Fixture.

## 1C context
- Configuration/subsystem: fixture
- Affected objects: none
- Client/server: local process
- Extension or main configuration: none
- Existing extension points/mechanisms: none
- Material constraints: local files only

## Non-goals
- Fixture.

## Acceptance criteria
- GIVEN fixture WHEN fixture THEN fixture.

## Required verification
- [x] Static validates the fixture and result.

## Uncertainties / assumptions
None.
'@
    [IO.File]::WriteAllText((Join-Path $missingModelChange 'spec.md'), $missingModelSpec, (New-Object Text.UTF8Encoding($false)))
    [IO.File]::WriteAllText((Join-Path $missingModelChange 'original-task.md'), 'Fixture task.', (New-Object Text.UTF8Encoding($false)))
    [IO.File]::WriteAllText((Join-Path $missingModelProject 'bsl-flow.yaml'), @'
version: 2
review:
  enabled: true
  routing:
    m_default: required
  reviewer:
    provider: claude_cli
'@, (New-Object Text.UTF8Encoding($false)))
    $missingModelFailed = $false
    $missingModelMessage = $null
    try { & $invokePath -ProjectPath $missingModelProject -ChangeName fixture -ForceReview -ForceReplaceReview } catch { $missingModelFailed = $true; $missingModelMessage = $_.Exception.Message }
    Assert-True ($missingModelFailed -and $missingModelMessage -match 'review\.reviewer\.model') 'missing review.reviewer.model is a clear configuration error naming the key'
    Assert-True (-not (Test-Path -LiteralPath (Join-Path $missingModelChange 'review.json') -PathType Leaf)) 'a missing-model configuration error never publishes review.json'

    "$passed single-review-provider checks passed"
}
finally {
    Remove-Item Env:FAKE_CLI_ROLE, Env:FAKE_CLI_MODE, Env:BSL_FLOW_TEST_REVIEWER_TOKEN -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
}
