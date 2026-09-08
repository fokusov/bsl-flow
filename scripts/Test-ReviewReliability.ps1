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
    $compiler = Get-Command csc.exe -ErrorAction SilentlyContinue
    if (-not $compiler) {
        $compilerPath = Get-ChildItem -Path "$env:WINDIR\Microsoft.NET\Framework*" -Filter csc.exe -File -Recurse -ErrorAction SilentlyContinue | Select-Object -First 1 -ExpandProperty FullName
    } else { $compilerPath = $compiler.Source }
    $sourcePath = Join-Path $tempRoot 'fake-provider.cs'
    $providerPath = Join-Path $tempRoot 'fake-provider.exe'
    $source = @'
using System;
using System.Threading;
class FakeProvider {
  static string Escape(string s) { return s.Replace("\\", "\\\\").Replace("\"", "\\\"").Replace("\r", "\\r").Replace("\n", "\\n"); }
  static void Emit(string s) { Console.WriteLine("{\"type\":\"text\",\"part\":{\"text\":\"" + Escape(s) + "\"}}"); Console.Out.Flush(); }
  static void Main(string[] args) {
    Console.In.ReadToEnd();
    string mode = Environment.GetEnvironmentVariable("FAKE_REVIEW_MODE") ?? "valid";
    if (mode == "timeout") { Thread.Sleep(30000); return; }
    string valid = "{\"schema_version\":1,\"reviewer_verdict\":\"PASS\",\"summary\":\"fixture\",\"scores\":{\"intent_fidelity\":5,\"minimality\":5,\"completeness\":5,\"architecture_fit\":5,\"testability\":5,\"assumption_discipline\":5,\"clarity\":5},\"overengineering\":{\"items\":[]},\"findings\":[],\"do_not_change\":[],\"confidence\":0.9}";
    if (mode == "malformed") { Emit("{broken"); return; }
    if (mode == "output_limit") { Emit(new string('x', 70000)); return; }
    if (mode == "ambiguous") { Emit("```json\n" + valid + "\n```\n```json\n" + valid + "\n```"); return; }
    if (mode == "provider_error") { Emit(valid); Console.WriteLine("{\"type\":\"error\",\"error\":\"fixture provider failed\"}"); Console.Out.Flush(); return; }
    Emit(valid.Substring(0, valid.Length / 2)); Emit(valid.Substring(valid.Length / 2));
  }
}
'@
    [IO.File]::WriteAllText($sourcePath, $source, (New-Object Text.UTF8Encoding($false)))
    if (-not [string]::IsNullOrWhiteSpace($compilerPath)) {
        & $compilerPath /nologo /target:exe /out:$providerPath $sourcePath | Out-Null
    }
    else {
        $dotnet = Get-Command dotnet.exe -ErrorAction SilentlyContinue
        Assert-True ($null -ne $dotnet) 'dotnet available for fake provider'
        $projectPath = Join-Path $tempRoot 'fake-provider.csproj'
        $sdkLines = @(& $dotnet.Source --list-sdks)
        $sdkMajor = ($sdkLines | ForEach-Object { if ($_ -match '^\s*(\d+)\.') { [int]$Matches[1] } } | Sort-Object -Descending | Select-Object -First 1)
        Assert-True ($sdkMajor -ge 5) 'supported .NET SDK available for fake provider'
        $targetFramework = "net$sdkMajor.0"
        $project = '<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><OutputType>Exe</OutputType><TargetFramework>{0}</TargetFramework><AssemblyName>fake-provider</AssemblyName><EnableDefaultCompileItems>false</EnableDefaultCompileItems></PropertyGroup><ItemGroup><Compile Include="fake-provider.cs" /></ItemGroup></Project>' -f $targetFramework
        [IO.File]::WriteAllText($projectPath, $project, (New-Object Text.UTF8Encoding($false)))
        $buildOutput = @(& $dotnet.Source build $projectPath '--nologo' '--configuration' 'Release' '--output' $tempRoot)
        if ($LASTEXITCODE -ne 0) { throw "Fake provider build failed: $($buildOutput -join ' ')" }
        $providerPath = Join-Path $tempRoot 'fake-provider.exe'
    }
    Assert-True ((Test-Path -LiteralPath $providerPath -PathType Leaf)) 'fake provider compiled'

    $validSpec = @'
# review-fixture

## Classification
- Complexity: M
- Risk: medium

## Goal
Verify reliable independent reviewer execution.

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
    $modes = @('valid', 'malformed', 'ambiguous', 'provider_error', 'output_limit', 'timeout')
    foreach ($mode in $modes) {
        $project = Join-Path $tempRoot $mode
        $change = Join-Path $project 'openspec\changes\fixture'
        New-Item -ItemType Directory -Path $change -Force | Out-Null
        [IO.File]::WriteAllText((Join-Path $project 'bsl-flow.yaml'), $config, (New-Object Text.UTF8Encoding($false)))
        [IO.File]::WriteAllText((Join-Path $change 'spec.md'), $validSpec, (New-Object Text.UTF8Encoding($false)))
        [IO.File]::WriteAllText((Join-Path $change 'original-task.md'), 'Review fixture task.', (New-Object Text.UTF8Encoding($false)))
        $env:FAKE_REVIEW_MODE = $mode
        $failed = $false
        $startedAt = [DateTime]::UtcNow
        $modeTimeout = if ($mode -eq 'output_limit') { 15 } else { 1 }
        try { $result = & $invokePath -ProjectPath $project -ChangeName fixture -ForceReview -ForceReplaceReview -OpenCodePath $providerPath -TimeoutSeconds $modeTimeout } catch { $failed = $true; $failureText = $_.Exception.Message }
        $elapsedSeconds = ([DateTime]::UtcNow - $startedAt).TotalSeconds
        if ($mode -eq 'valid' -and $failed) {
            throw "Valid fixture unexpectedly failed: $failureText"
        }
        $reviewPath = Join-Path $change 'review.json'
        $runs = @(Get-ChildItem -LiteralPath (Join-Path $project '.bsl-flow\reports\spec-review') -Directory -ErrorAction SilentlyContinue)
        Assert-True (($mode -eq 'valid' -and -not $failed -and (Test-Path -LiteralPath $reviewPath -PathType Leaf)) -or ($mode -ne 'valid' -and $failed -and -not (Test-Path -LiteralPath $reviewPath -PathType Leaf))) "$mode publication gate"
        Assert-True ($runs.Count -eq 1) "$mode retained one run"
            $diagnostic = Join-Path $runs[0].FullName 'diagnostic.json'
            if ($mode -ne 'valid') {
            Assert-True (Test-Path -LiteralPath $diagnostic -PathType Leaf) "$mode diagnostic retained"
            $diagnosticValue = Get-Content -Raw -LiteralPath $diagnostic | ConvertFrom-Json
            $expectedPhase = if ($mode -eq 'timeout') { 'timeout' } elseif ($mode -eq 'output_limit') { 'output_limit' } else { 'parsing' }
            Assert-True ($diagnosticValue.phase -eq $expectedPhase) "$mode truthful failure phase (actual=$($diagnosticValue.phase))"
            Assert-True (Test-Path -LiteralPath (Join-Path $runs[0].FullName 'events.jsonl') -PathType Leaf) "$mode events retained"
            if ($mode -eq 'timeout') { Assert-True ($elapsedSeconds -lt 4) 'timeout termination and drain stay bounded' }
        }
    }
}
finally {
    Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item Env:FAKE_REVIEW_MODE -ErrorAction SilentlyContinue
}

"$passed reliability checks passed under PowerShell $($PSVersionTable.PSVersion)"
