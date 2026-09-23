#Requires -Version 7.0
[CmdletBinding()]
param(
    [string]$PackageRoot
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path) }
$reviewSkill = Join-Path $PackageRoot 'global\skills\1c-spec-review'
$commonPath = Join-Path $reviewSkill 'scripts\Review.Common.ps1'
$invokePath = Join-Path $reviewSkill 'scripts\Invoke-1CSpecReview.ps1'
. $commonPath

$passed = 0
function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "FAIL: $Message" }
    $script:passed++
    "PASS $Message"
}
function EventLine([string]$Text) {
    return (@{ type = 'text'; part = @{ text = $Text } } | ConvertTo-Json -Depth 4 -Compress)
}
Assert-True ((Get-BSLFlowYamlValue -Text '' -Path @('review','reviewer','model') -Default 'default-model') -eq 'default-model') 'absent project configuration uses the declared default'
Assert-True ((Get-BSLFlowSpecReviewMode -Complexity S -Risk low -ReviewRequired $false) -eq 'lint') 'S defaults to deterministic lint without model review'
Assert-True ((Get-BSLFlowSpecReviewMode -Complexity M -Risk medium -ReviewRequired $true) -eq 'single') 'M uses one independent reviewer'
Assert-True ((Get-BSLFlowSpecReviewMode -Complexity L -Risk low -ReviewRequired $true) -eq 'council') 'L uses the API Council'
Assert-True ((Get-BSLFlowSpecReviewMode -Complexity S -Risk high -ReviewRequired $true) -eq 'council') 'high risk overrides size and uses the API Council'
Assert-True ((Get-BSLFlowSpecReviewMode -Complexity S -Risk low -ReviewRequired $true) -eq 'single') 'explicit S review uses one reviewer rather than Council'
function Assert-ParserAccepted([string]$Name, [string[]]$Lines) {
    $parsed = Get-BSLFlowJsonFromOpenCodeEvents -Lines $Lines
    Assert-True ($parsed.marker -eq 'expected') $Name
}
function Assert-ParserRejected([string]$Name, [string[]]$Lines) {
    $rejected = $false
    try { $null = Get-BSLFlowJsonFromOpenCodeEvents -Lines $Lines } catch { $rejected = $true }
    Assert-True $rejected $Name
}

$object = '{"marker":"expected"}'
$fence = '```json' + "`n" + $object + "`n" + '```'
Assert-ParserAccepted 'raw object' @((EventLine $object))
Assert-ParserAccepted 'standalone fence' @((EventLine $fence))
Assert-ParserAccepted 'prose then fence' @((EventLine 'Checked.'), (EventLine ("Done.`n`n$fence")))
Assert-ParserAccepted 'prose without newline then fence' @((EventLine 'Here is my assessment.'), (EventLine $fence))
Assert-ParserAccepted 'unlabelled fence' @((EventLine ($fence.Replace('```json', '```'))))
Assert-ParserRejected 'two fenced candidates' @((EventLine ($fence + "`n" + $fence)))
Assert-ParserRejected 'raw beside fence' @((EventLine ($object + "`n" + $fence)))
Assert-ParserRejected 'two raw objects' @((EventLine ($object + "`n" + $object)))
Assert-ParserRejected 'array result' @((EventLine ('[' + $object + ']')))
Assert-ParserRejected 'primitive result' @((EventLine '1'))
Assert-ParserRejected 'malformed JSON' @((EventLine '{broken'))
Assert-ParserRejected 'missing final object' @((EventLine 'Still investigating'))
Assert-ParserRejected 'provider error event' @((EventLine $object), '{"type":"error","error":"provider failed"}')
Assert-ParserAccepted 'bracket punctuation in prose' @((EventLine ("Result [PASS].`n$fence`nDone")))
Assert-ParserRejected 'unclosed candidate beside fence' @((EventLine ('" ' + $object + "`n" + $fence)))
Assert-ParserRejected 'invalid outer candidate beside fence' @((EventLine ('[invalid ' + $object + ']' + "`n" + $fence)))

$allowedCategories = @(Get-BSLFlowAllowedFindingCategories)
$schema = Get-Content -Raw -LiteralPath (Join-Path $reviewSkill 'references\review-schema.json') | ConvertFrom-Json
$schemaCategories = @($schema.'$defs'.finding.properties.category.enum)
Assert-True (($allowedCategories -join '|') -eq ($schemaCategories -join '|')) 'finding category enum matches JSON schema'
$reviewerPrompt = Get-Content -Raw -LiteralPath (Join-Path $reviewSkill 'reviewer\spec-reviewer-prompt.md')
$categoryContract = '"category": "' + ($allowedCategories -join '|') + '"'
Assert-True $reviewerPrompt.Contains($categoryContract) 'finding category enum matches reviewer prompt'
Assert-True $reviewerPrompt.Contains('`completeness` is a score name, not a finding category') 'reviewer prompt distinguishes completeness score from finding category'

$invalidCategoryReview = @'
{"schema_version":1,"reviewer_verdict":"REVISE","summary":"fixture","scores":{"intent_fidelity":5,"minimality":5,"completeness":4,"architecture_fit":5,"testability":5,"assumption_discipline":5,"clarity":5},"overengineering":{"items":[]},"findings":[{"id":"R-001","severity":"low","category":"completeness","spec_ref":"Goal","issue":"missing detail","evidence":"fixture","suggested_direction":"clarify"}],"do_not_change":[],"confidence":0.8}
'@ | ConvertFrom-Json
$invalidCategoryMessage = $null
try { Assert-BSLFlowReviewPayload -Review $invalidCategoryReview } catch { $invalidCategoryMessage = $_.Exception.Message }
Assert-True ($invalidCategoryMessage -eq "Invalid finding category 'completeness' in finding R-001.") 'invalid category diagnostic includes value and finding id'

# A tiny local executable provider gives deterministic streaming, error and timeout
# fixtures without a paid model, network access, or a 1C runtime.
$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-review-reliability-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tempRoot -Force | Out-Null
try {
    $sourcePath = Join-Path $tempRoot 'fake-provider.cs'
    $providerPath = Join-Path $tempRoot 'fake-provider.exe'
    $source = @'
using System;
using System.IO;
using System.Text;
using System.Threading;
class FakeProvider {
  static string Escape(string s) { return s.Replace("\\", "\\\\").Replace("\"", "\\\"").Replace("\r", "\\r").Replace("\n", "\\n"); }
  static void Emit(string s) { Console.WriteLine("{\"type\":\"text\",\"part\":{\"text\":\"" + Escape(s) + "\"}}"); Console.Out.Flush(); }
  static void Main(string[] args) {
    Console.OutputEncoding = new UTF8Encoding(false);
    var errorWriter = new StreamWriter(Console.OpenStandardError(), new UTF8Encoding(false));
    errorWriter.AutoFlush = true; Console.SetError(errorWriter);
    byte[] inputBytes;
    using (var input = Console.OpenStandardInput())
    using (var buffer = new MemoryStream()) { input.CopyTo(buffer); inputBytes = buffer.ToArray(); }
    string mode = Environment.GetEnvironmentVariable("FAKE_REVIEW_MODE") ?? "valid";
    if (mode == "timeout") { Thread.Sleep(30000); return; }
    string valid = "{\"schema_version\":1,\"reviewer_verdict\":\"PASS\",\"summary\":\"fixture\",\"scores\":{\"intent_fidelity\":5,\"minimality\":5,\"completeness\":5,\"architecture_fit\":5,\"testability\":5,\"assumption_discipline\":5,\"clarity\":5},\"overengineering\":{\"items\":[]},\"findings\":[],\"do_not_change\":[],\"confidence\":0.9}";
    string emptyBlock = valid.Replace("\"reviewer_verdict\":\"PASS\"", "\"reviewer_verdict\":\"BLOCK\"");
    string lowPass = valid.Replace("\"intent_fidelity\":5", "\"intent_fidelity\":1").Replace("\"minimality\":5", "\"minimality\":1").Replace("\"completeness\":5", "\"completeness\":1").Replace("\"architecture_fit\":5", "\"architecture_fit\":1").Replace("\"testability\":5", "\"testability\":1").Replace("\"assumption_discipline\":5", "\"assumption_discipline\":1").Replace("\"clarity\":5", "\"clarity\":1");
    if (mode == "malformed") { Emit("{broken"); return; }
    if (mode == "output_limit") { Emit(new string('x', 70000)); return; }
    if (mode == "ambiguous") { Emit("```json\n" + valid + "\n```\n```json\n" + valid + "\n```"); return; }
    if (mode == "provider_error") { Emit(valid); Console.WriteLine("{\"type\":\"error\",\"error\":\"fixture provider failed\"}"); Console.Out.Flush(); return; }
    if (mode == "block_empty") { Emit(emptyBlock); return; }
    if (mode == "downgraded_empty") { Emit(lowPass); return; }
    if (mode == "unicode_transport") {
      string inputText;
      try { inputText = new UTF8Encoding(false, true).GetString(inputBytes); }
      catch { Environment.Exit(41); return; }
      if (!inputText.Contains("\u041f\u0440\u043e\u0432\u0435\u0440\u044c \u0437\u0430\u0434\u0430\u0447\u0443 \U0001F680") || !inputText.Contains("\u041a\u0438\u0440\u0438\u043b\u043b\u0438\u0446\u0430 \U0001F9EA")) { Environment.Exit(42); return; }
      string summary = "\u0422\u043e\u0447\u043d\u044b\u0439 UTF-8 \U0001F680";
      Console.Error.WriteLine("\u0414\u0438\u0430\u0433\u043d\u043e\u0441\u0442\u0438\u043a\u0430 \U0001F9EA"); Console.Error.Flush();
      Emit(valid.Replace("\"summary\":\"fixture\"", "\"summary\":\"" + summary + "\"")); return;
    }
    if (mode.StartsWith("mutate_")) {
      File.AppendAllText(Environment.GetEnvironmentVariable("FAKE_REVIEW_MUTATION_PATH"), "\nchanged during review");
      Emit(valid);
      return;
    }
    Emit(valid.Substring(0, valid.Length / 2)); Emit(valid.Substring(valid.Length / 2));
  }
}
'@
    [IO.File]::WriteAllText($sourcePath, $source, (New-Object Text.UTF8Encoding($false)))
        $dotnet = Get-Command dotnet.exe -ErrorAction SilentlyContinue
        Assert-True ($null -ne $dotnet) 'dotnet available for fake provider'
        $projectPath = Join-Path $tempRoot 'fake-provider.csproj'
        $sdkLines = @(& $dotnet.Source --list-sdks)
        $sdkMajor = ($sdkLines | ForEach-Object { if ($_ -match '^\s*(\d+)\.') { [int]$Matches[1] } } | Sort-Object -Descending | Select-Object -First 1)
        Assert-True ($sdkMajor -ge 5) 'supported .NET SDK available for fake provider'
        $targetFramework = "net$sdkMajor.0"
        $project = '<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>{0}</TargetFramework><AssemblyName>fake-provider</AssemblyName><EnableDefaultCompileItems>false</EnableDefaultCompileItems></PropertyGroup><ItemGroup><Compile Include="fake-provider.cs" /></ItemGroup></Project>' -f $targetFramework
        [IO.File]::WriteAllText($projectPath, $project, (New-Object Text.UTF8Encoding($false)))
        $nugetConfig = Join-Path $tempRoot 'NuGet.Config'
        [IO.File]::WriteAllText($nugetConfig, '<configuration><packageSources><clear /></packageSources></configuration>')
        $buildOutput = @(& $dotnet.Source build $projectPath '--nologo' '--configuration' 'Release' '--output' $tempRoot "-p:RestoreConfigFile=$nugetConfig")
        if ($LASTEXITCODE -ne 0) { throw "Fake provider build failed: $($buildOutput -join ' ')" }
        $providerPath = Join-Path $tempRoot 'fake-provider.exe'
    Assert-True ((Test-Path -LiteralPath $providerPath -PathType Leaf)) 'fake provider compiled'

    $validSpec = @'
# review-fixture

## Classification
- Complexity: M
- Risk: medium

## Goal
Verify reliable independent reviewer execution.

Кириллица 🧪 must reach the provider as UTF-8 bytes.

## Current behavior
Reviewer result can be lost when parsing fails.

## Required behavior
1. Retain events before parsing.
2. Publish review only after validation.

## 1C context
- Configuration/subsystem: fixture
- Affected objects: none
- Client/server: local process
- Extension or main configuration: none
- Existing extension points/mechanisms: none
- Material constraints: local files only

## Non-goals
- Do not start 1C or an external service.

## Acceptance criteria
- GIVEN provider emits events WHEN review completes THEN review.json is valid.

## Required verification
- [x] Static validates the fixture and result.
- [x] Unit validates parser rejection.

## Uncertainties / assumptions
None.
'@
    $config = @'
version: 2
review:
  enabled: true
  routing:
    m_default: required
    high_risk_override: required
  reviewer:
    provider: opencode
    agent: bsl-flow-spec-reviewer
    model: deepseek/deepseek-v4-pro
    variant: high
  permissions:
    project_read_mode: read_search
    edit: false
    shell: false
    subagents: false
    web: false
    external_directory: false
  runtime:
    timeout_seconds: 2
    max_output_bytes: 65536
'@
    $successfulModes = @('valid', 'unicode_transport')
    $modes = @('valid', 'unicode_transport', 'malformed', 'ambiguous', 'provider_error', 'output_limit', 'timeout', 'block_empty', 'downgraded_empty', 'mutate_spec', 'mutate_original', 'mutate_design')
    foreach ($mode in $modes) {
        $project = Join-Path $tempRoot $mode
        $change = Join-Path $project 'openspec\changes\fixture'
        New-Item -ItemType Directory -Path $change -Force | Out-Null
        [IO.File]::WriteAllText((Join-Path $project 'bsl-flow.yaml'), $config, (New-Object Text.UTF8Encoding($false)))
        [IO.File]::WriteAllText((Join-Path $change 'spec.md'), $validSpec, (New-Object Text.UTF8Encoding($false)))
        [IO.File]::WriteAllText((Join-Path $change 'original-task.md'), 'Проверь задачу 🚀.', (New-Object Text.UTF8Encoding($false)))
        if ($mode -eq 'mutate_design') { [IO.File]::WriteAllText((Join-Path $change 'design.md'), 'Fixture design.', (New-Object Text.UTF8Encoding($false))) }
        $env:FAKE_REVIEW_MODE = $mode
        if ($mode -like 'mutate_*') {
            $mutationName = if ($mode -eq 'mutate_spec') { 'spec.md' } elseif ($mode -eq 'mutate_original') { 'original-task.md' } else { 'design.md' }
            $env:FAKE_REVIEW_MUTATION_PATH = Join-Path $change $mutationName
        }
        else { Remove-Item Env:FAKE_REVIEW_MUTATION_PATH -ErrorAction SilentlyContinue }
        $failed = $false
        $startedAt = [DateTime]::UtcNow
        $modeTimeout = if ($mode -eq 'output_limit') { 15 } else { 1 }
        try { $result = & $invokePath -ProjectPath $project -ChangeName fixture -ForceReview -ForceReplaceReview -OpenCodePath $providerPath -TimeoutSeconds $modeTimeout } catch { $failed = $true; $failureText = $_.Exception.Message }
        $elapsedSeconds = ([DateTime]::UtcNow - $startedAt).TotalSeconds
        if ($mode -in $successfulModes -and $failed) {
            throw "Valid fixture unexpectedly failed: $failureText"
        }
        if($mode -eq 'valid'){Assert-True (@($result).Count -eq 1 -and $result.Verdict -eq 'PASS') 'review wrapper emits exactly one contracted result without VoidTaskResult'}
        $reviewPath = Join-Path $change 'review.json'
        $runs = @(Get-ChildItem -LiteralPath (Join-Path $project '.bsl-flow\reports\spec-review') -Directory -ErrorAction SilentlyContinue)
        Assert-True (($mode -in $successfulModes -and -not $failed -and (Test-Path -LiteralPath $reviewPath -PathType Leaf)) -or ($mode -notin $successfulModes -and $failed -and -not (Test-Path -LiteralPath $reviewPath -PathType Leaf))) "$mode publication gate"
        Assert-True ($runs.Count -eq 1) "$mode retained one run"
        if ($mode -eq 'unicode_transport') {
            $unicodeReview = Get-Content -Raw -LiteralPath $reviewPath | ConvertFrom-Json
            Assert-True ($unicodeReview.summary -eq 'Точный UTF-8 🚀') "Unicode provider summary is preserved exactly (actual=$($unicodeReview.summary))"
            $unicodeStderr = [IO.File]::ReadAllText((Join-Path $runs[0].FullName 'provider-stderr.log'), (New-Object Text.UTF8Encoding($false))).Trim()
            Assert-True ($unicodeStderr -eq 'Диагностика 🧪') "Unicode provider stderr is preserved exactly (actual=$unicodeStderr)"
        }
            $diagnostic = Join-Path $runs[0].FullName 'diagnostic.json'
            if ($mode -notin $successfulModes) {
            Assert-True (Test-Path -LiteralPath $diagnostic -PathType Leaf) "$mode diagnostic retained"
            $diagnosticValue = Get-Content -Raw -LiteralPath $diagnostic | ConvertFrom-Json
            $expectedPhase = if ($mode -eq 'timeout') { 'timeout' } elseif ($mode -eq 'output_limit') { 'output_limit' } elseif ($mode -in @('block_empty', 'downgraded_empty')) { 'validating' } elseif ($mode -like 'mutate_*') { 'freshness' } else { 'parsing' }
            Assert-True ($diagnosticValue.phase -eq $expectedPhase) "$mode truthful failure phase (actual=$($diagnosticValue.phase))"
            Assert-True (Test-Path -LiteralPath (Join-Path $runs[0].FullName 'events.jsonl') -PathType Leaf) "$mode events retained"
            Assert-True (Test-Path -LiteralPath $diagnosticValue.input_snapshot_path -PathType Leaf) "$mode input snapshot retained"
            Assert-True (Test-Path -LiteralPath $diagnosticValue.raw_response_path -PathType Leaf) "$mode raw response retained"
            if ($mode -eq 'timeout') { Assert-True ($elapsedSeconds -lt 4) 'timeout termination and drain stay bounded' }
        }
    }

    $finalProject = Join-Path $tempRoot 'final-validation'
    $finalChange = Join-Path $finalProject 'openspec\changes\fixture'
    New-Item -ItemType Directory -Path $finalChange -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $finalProject 'bsl-flow.yaml'), $config, (New-Object Text.UTF8Encoding($false)))
    [IO.File]::WriteAllText((Join-Path $finalChange 'spec.md'), $validSpec, (New-Object Text.UTF8Encoding($false)))
    [IO.File]::WriteAllText((Join-Path $finalChange 'original-task.md'), 'Review fixture task.', (New-Object Text.UTF8Encoding($false)))
    $env:FAKE_REVIEW_MODE = 'valid'
    $null = & $invokePath -ProjectPath $finalProject -ChangeName fixture -ForceReview -ForceReplaceReview -OpenCodePath $providerPath -TimeoutSeconds 10
    $finalReviewPath = Join-Path $finalChange 'review.json'
    $finalReconciliationPath = Join-Path $finalChange 'review-reconciliation.json'
    $finalScript = Join-Path $reviewSkill 'scripts\Test-1CSpecFinal.ps1'
    $review = Get-Content -Raw -LiteralPath $finalReviewPath | ConvertFrom-Json
    $validReconciliation = [ordered]@{
        schema_version = 1
        review_sha256 = Get-BSLFlowSha256 $finalReviewPath
        draft_spec_sha256 = $review.inputs.spec_sha256
        final_spec_sha256 = Get-BSLFlowSha256 (Join-Path $finalChange 'spec.md')
        draft_design_sha256 = $null
        final_design_sha256 = $null
        reconciled_at_utc = [DateTime]::UtcNow.ToString('o')
        summary = 'No findings required specification changes.'
        decisions = @()
        do_not_change_checks = @()
    }
    $reconciliationJson = $validReconciliation | ConvertTo-Json -Depth 20
    [IO.File]::WriteAllText($finalReconciliationPath, $reconciliationJson, (New-Object Text.UTF8Encoding($false, $true)))
    $initialFinal = & $finalScript -ProjectPath $finalProject -ChangeName fixture
    Assert-True ($initialFinal.passed -eq $true) 'valid PASS review and reconciliation pass final validation'

    $protectedItem = 'Сохрани правило — без подмены 🧪'
    $review.do_not_change = @($protectedItem)
    Write-BSLFlowJsonAtomic -Value $review -Path $finalReviewPath
    $validReconciliation.review_sha256 = Get-BSLFlowSha256 $finalReviewPath
    $validReconciliation.summary = 'Правило с Unicode сохранено точно.'
    $validReconciliation.do_not_change_checks = @([ordered]@{
        item = $protectedItem
        decision = 'preserved'
        reason = 'Требование пользователя остаётся обязательным.'
        evidence = 'Точное совпадение с review.json — 🧪.'
    })
    $reconciliationJson = $validReconciliation | ConvertTo-Json -Depth 20
    [IO.File]::WriteAllText($finalReconciliationPath, $reconciliationJson, (New-Object Text.UTF8Encoding($false, $true)))
    $reconciliationBytes = [IO.File]::ReadAllBytes($finalReconciliationPath)
    Assert-True (-not ($reconciliationBytes.Length -ge 3 -and $reconciliationBytes[0] -eq 0xEF -and $reconciliationBytes[1] -eq 0xBB -and $reconciliationBytes[2] -eq 0xBF)) 'Unicode reconciliation fixture is UTF-8 without BOM'
    $unicodeFinal = & $finalScript -ProjectPath $finalProject -ChangeName fixture
    Assert-True ($unicodeFinal.passed -eq $true) 'BOM-less Unicode protected item passes exact final validation'

    $decisionShape = ([pscustomobject]$validReconciliation | ConvertTo-Json -Depth 10 | ConvertFrom-Json)
    $decisionShape.decisions = @([pscustomobject]@{ finding_id = 'R-001'; decision = 'rejected'; reason = 'Fixture reason.'; evidence = 'Fixture evidence.'; status = 'not_applicable'; resolution = 'No change.'; spec_ref_after = 'Goal' })
    foreach ($requiredProperty in @('finding_id', 'decision', 'reason', 'evidence', 'status', 'resolution', 'spec_ref_after')) {
        $damaged = ($decisionShape | ConvertTo-Json -Depth 10 | ConvertFrom-Json)
        $damaged.decisions[0].PSObject.Properties.Remove($requiredProperty)
        $rejected = $false
        try { Assert-BSLFlowReviewReconciliationPayload $damaged } catch { $rejected = $true }
        Assert-True $rejected "missing reconciliation decision.$requiredProperty is rejected"
    }
    $checkShape = ([pscustomobject]$validReconciliation | ConvertTo-Json -Depth 10 | ConvertFrom-Json)
    $checkShape.do_not_change_checks = @([pscustomobject]@{ item = 'Keep behavior.'; decision = 'preserved'; reason = 'Fixture reason.'; evidence = 'Fixture evidence.' })
    foreach ($requiredProperty in @('item', 'decision', 'reason', 'evidence')) {
        $damaged = ($checkShape | ConvertTo-Json -Depth 10 | ConvertFrom-Json)
        $damaged.do_not_change_checks[0].PSObject.Properties.Remove($requiredProperty)
        $rejected = $false
        try { Assert-BSLFlowReviewReconciliationPayload $damaged } catch { $rejected = $true }
        Assert-True $rejected "missing do_not_change check.$requiredProperty is rejected"
    }

    foreach ($requiredProperty in @('schema_version', 'review_sha256', 'draft_spec_sha256', 'final_spec_sha256', 'draft_design_sha256', 'final_design_sha256', 'reconciled_at_utc', 'summary', 'decisions', 'do_not_change_checks')) {
        $damaged = ([pscustomobject]$validReconciliation | ConvertTo-Json -Depth 10 | ConvertFrom-Json)
        $damaged.PSObject.Properties.Remove($requiredProperty)
        Write-BSLFlowJsonAtomic -Value $damaged -Path $finalReconciliationPath
        $failed = $false
        try { $null = & $finalScript -ProjectPath $finalProject -ChangeName fixture } catch { $failed = $true }
        $failedFinal = Get-Content -Raw -LiteralPath (Join-Path $finalChange 'final-validation.json') | ConvertFrom-Json
        Assert-True ($failed -and $failedFinal.passed -eq $false) "missing reconciliation.$requiredProperty replaces old final PASS"
    }

    Write-BSLFlowJsonAtomic -Value $validReconciliation -Path $finalReconciliationPath
    Remove-Item -LiteralPath $finalReconciliationPath -Force
    $failed = $false
    try { $null = & $finalScript -ProjectPath $finalProject -ChangeName fixture } catch { $failed = $true }
    $failedFinal = Get-Content -Raw -LiteralPath (Join-Path $finalChange 'final-validation.json') | ConvertFrom-Json
    Assert-True ($failed -and $failedFinal.passed -eq $false) 'missing reconciliation replaces old final PASS'

    $legacyInvalid = Get-Content -Raw -LiteralPath $finalReviewPath | ConvertFrom-Json
    $legacyInvalid.reviewer_verdict = 'BLOCK'
    $legacyInvalid.verdict = 'BLOCK'
    Write-BSLFlowJsonAtomic -Value $legacyInvalid -Path $finalReviewPath
    $legacyReconciliation = [ordered]@{} + $validReconciliation
    $legacyReconciliation.review_sha256 = Get-BSLFlowSha256 $finalReviewPath
    Write-BSLFlowJsonAtomic -Value $legacyReconciliation -Path $finalReconciliationPath
    $failed = $false
    try { $null = & $finalScript -ProjectPath $finalProject -ChangeName fixture } catch { $failed = $true }
    $failedFinal = Get-Content -Raw -LiteralPath (Join-Path $finalChange 'final-validation.json') | ConvertFrom-Json
    Assert-True ($failed -and $failedFinal.passed -eq $false) 'legacy non-PASS review without findings is rejected fail closed'
}
finally {
    Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item Env:FAKE_REVIEW_MODE -ErrorAction SilentlyContinue
    Remove-Item Env:FAKE_REVIEW_MUTATION_PATH -ErrorAction SilentlyContinue
}

"$passed reliability checks passed under PowerShell $($PSVersionTable.PSVersion)"
