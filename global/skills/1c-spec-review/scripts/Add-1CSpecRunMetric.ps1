[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ProjectPath,
    [Parameter(Mandatory)][string]$ChangeName,
    [string]$AuthorModel,
    [string]$AuthorReasoning,
    [string]$MetricsPath = (Join-Path ([Environment]::GetFolderPath('UserProfile')) '.bsl-flow\evals\spec-runs.jsonl')
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Review.Common.ps1')

$projectRoot = [System.IO.Path]::GetFullPath($ProjectPath).TrimEnd('\', '/')
if ($ChangeName -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$') { throw "Unsafe OpenSpec change name: $ChangeName" }
$changeRoot = Join-Path $projectRoot "openspec\changes\$ChangeName"
$reviewPath = Join-Path $changeRoot 'review.json'
$reconciliationPath = Join-Path $changeRoot 'review-reconciliation.json'
$validationPath = Join-Path $changeRoot 'final-validation.json'
$specPath = Join-Path $changeRoot 'spec.md'
foreach ($required in @($reviewPath, $reconciliationPath, $validationPath, $specPath)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) { throw "Metric input missing: $required" }
}
$validation = & (Join-Path $PSScriptRoot 'Test-1CSpecFinal.ps1') -ProjectPath $projectRoot -ChangeName $ChangeName
$review = Get-Content -Raw -LiteralPath $reviewPath | ConvertFrom-Json
$reconciliation = Get-Content -Raw -LiteralPath $reconciliationPath | ConvertFrom-Json
if ($validation.passed -ne $true) { throw 'Metrics are appended only after final validation passes.' }
$specText = Get-Content -Raw -LiteralPath $specPath
$complexity = [regex]::Match($specText, '(?im)^\s*-\s*(?:Сложность|Complexity):\s*(S|M|L)\s*$').Groups[1].Value.ToUpperInvariant()
$risk = [regex]::Match($specText, '(?im)^\s*-\s*(?:Риск|Risk):\s*(low|medium|high)\s*$').Groups[1].Value.ToLowerInvariant()

$pathBytes = [System.Text.Encoding]::UTF8.GetBytes($projectRoot.ToLowerInvariant())
$sha = [System.Security.Cryptography.SHA256]::Create()
try { $projectId = ([BitConverter]::ToString($sha.ComputeHash($pathBytes))).Replace('-', '').ToLowerInvariant().Substring(0, 16) }
finally { $sha.Dispose() }
$runMaterial = "$projectId|$ChangeName|$(Get-BSLFlowSha256 $reviewPath)"
$runBytes = [System.Text.Encoding]::UTF8.GetBytes($runMaterial)
$sha = [System.Security.Cryptography.SHA256]::Create()
try { $runId = ([BitConverter]::ToString($sha.ComputeHash($runBytes))).Replace('-', '').ToLowerInvariant() }
finally { $sha.Dispose() }
$changeBytes = [System.Text.Encoding]::UTF8.GetBytes("$projectId|$ChangeName")
$sha = [System.Security.Cryptography.SHA256]::Create()
try { $changeId = ([BitConverter]::ToString($sha.ComputeHash($changeBytes))).Replace('-', '').ToLowerInvariant().Substring(0, 16) }
finally { $sha.Dispose() }

$decisions = @($reconciliation.decisions)
$accepted = @($decisions | Where-Object { $_.decision -eq 'accepted' }).Count
$rejected = @($decisions | Where-Object { $_.decision -eq 'rejected' }).Count
$record = [ordered]@{
    schema_version = 1
    run_id = $runId
    recorded_at_utc = [DateTime]::UtcNow.ToString('o')
    project_id = $projectId
    change_id = $changeId
    complexity = $complexity
    risk = $risk
    author = [ordered]@{ model = if ($AuthorModel) { $AuthorModel } else { $null }; reasoning = if ($AuthorReasoning) { $AuthorReasoning } else { $null } }
    reviewer = $review.reviewer
    reviewer_verdict = $review.reviewer_verdict
    gate_verdict = $review.verdict
    scores = $review.scores
    weighted_score = $review.weighted_score
    overengineering = [ordered]@{
        architectural_decision_count = $review.overengineering.architectural_decision_count
        required_count = $review.overengineering.required_count
        justified_count = $review.overengineering.justified_count
        optional_count = $review.overengineering.optional_count
        unjustified_count = $review.overengineering.unjustified_count
        index = $review.overengineering.index
        optional_ratio = $review.overengineering.optional_ratio
        unjustified_ratio = $review.overengineering.unjustified_ratio
        normalized_index = $review.overengineering.normalized_index
    }
    findings = [ordered]@{
        total = @($review.findings).Count
        accepted = $accepted
        rejected = $rejected
        acceptance_rate = if (@($review.findings).Count -eq 0) { $null } else { [math]::Round($accepted / @($review.findings).Count, 4) }
    }
    review_iterations = 1
    human = [ordered]@{ accepted = $null; edit_minutes = $null }
    implementation = [ordered]@{ passed = $null; clarifications = $null; rework_count = $null }
    usage = [ordered]@{ tokens = $null; duration_sec = $null; cost = $null }
}
$line = $record | ConvertTo-Json -Depth 20 -Compress
$metricsDirectory = Split-Path -Parent $MetricsPath
New-Item -ItemType Directory -Path $metricsDirectory -Force | Out-Null

$stream = [System.IO.File]::Open($MetricsPath, [System.IO.FileMode]::OpenOrCreate, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
try {
    $reader = [System.IO.StreamReader]::new($stream, [System.Text.UTF8Encoding]::new($false), $true, 1024, $true)
    $existing = $reader.ReadToEnd()
    $reader.Dispose()
    foreach ($existingLine in ($existing -split "`r?`n")) {
        if (-not $existingLine.Trim()) { continue }
        try { $existingRecord = $existingLine | ConvertFrom-Json -ErrorAction Stop }
        catch { throw "Metrics file contains invalid JSONL and was not changed: $MetricsPath" }
        if ($existingRecord.run_id -eq $runId) { throw "Metric run already recorded: $runId" }
    }
    $stream.Seek(0, [System.IO.SeekOrigin]::End) | Out-Null
    $writer = [System.IO.StreamWriter]::new($stream, [System.Text.UTF8Encoding]::new($false), 1024, $true)
    $writer.WriteLine($line)
    $writer.Flush()
    $writer.Dispose()
}
finally { $stream.Dispose() }

return [pscustomobject]@{ RunId = $runId; MetricsPath = $MetricsPath }
