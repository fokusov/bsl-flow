#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$root = [IO.Path]::GetFullPath($PackageRoot)
$lint = Join-Path $root 'global\skills\1c-spec-review\scripts\Test-1CSpec.ps1'
$final = Join-Path $root 'global\skills\1c-spec-review\scripts\Test-1CSpecFinal.ps1'
$common = Join-Path $root 'global\skills\1c-spec-review\scripts\Review.Common.ps1'
foreach ($path in @($lint, $final, $common)) { if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Required onboarding check is missing: $path" } }

$temp = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-onboarding-' + [guid]::NewGuid().ToString('N'))
$checks = 0
function Assert-Onboarding([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "FAIL: $Message" }
    $script:checks++
    Write-Host "PASS $Message"
}
try {
    foreach ($example in @('s-print-form', 'm-attribute-and-form')) {
        $source = Join-Path $root "examples\$example"
        foreach ($name in @('original-task.md', 'spec.md', 'spec-lint.json', 'review.json', 'review-reconciliation.json', 'final-validation.json', 'diff.md', 'verification.md', 'transcript.md', 'verdict.md')) {
            Assert-Onboarding (Test-Path -LiteralPath (Join-Path $source $name) -PathType Leaf) "$example contains $name"
        }
        $change = Join-Path $temp "openspec\changes\$example"
        New-Item -ItemType Directory -Path $change -Force | Out-Null
        Copy-Item -Path (Join-Path $source '*') -Destination $change -Recurse -Force
        $lintResult = & $lint -ChangePath $change -NoThrow
        Assert-Onboarding ([bool]$lintResult.passed) "$example spec lint passes in an isolated project"
        . $common
        $review = Get-Content -Raw -LiteralPath (Join-Path $change 'review.json') | ConvertFrom-Json
        $reconciliation = Get-Content -Raw -LiteralPath (Join-Path $change 'review-reconciliation.json') | ConvertFrom-Json
        Assert-BSLFlowReviewPayload -Review $review -Completed
        Assert-BSLFlowReviewReconciliationPayload -Reconciliation $reconciliation
        Assert-Onboarding $true "$example review and reconciliation satisfy Review.Common"
        $finalResult = & $final -ProjectPath $temp -ChangeName $example
        Assert-Onboarding ([bool]$finalResult.passed) "$example final validation passes in an isolated project"
        Assert-Onboarding (Test-Path -LiteralPath (Join-Path $change 'final-validation.json') -PathType Leaf) "$example produces final-validation.json"
    }
    Write-Host "Onboarding examples passed: $checks checks."
}
finally {
    $resolved = [IO.Path]::GetFullPath($temp)
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    if (-not $resolved.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or [IO.Path]::GetFileName($resolved) -notmatch '^bsl-flow-onboarding-') { throw "Unsafe test cleanup target: $resolved" }
    if (Test-Path -LiteralPath $resolved) { Remove-Item -LiteralPath $resolved -Recurse -Force -ErrorAction SilentlyContinue }
}
