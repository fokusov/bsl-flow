#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if (-not $PackageRoot) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$checks = 0
$bootstrap = Join-Path $PackageRoot 'global/AGENTS.bootstrap.md'
if ((Get-Item -LiteralPath $bootstrap).Length -gt 1024) { throw 'Bootstrap exceeds 1024 bytes.' }
$checks++
$headings = @('When to use', 'Inputs', 'Steps', 'Outputs', 'Checks', 'Stop and ask when', 'Managed mode')
foreach ($file in Get-ChildItem -LiteralPath (Join-Path $PackageRoot 'global/skills') -Filter SKILL.md -Recurse -File) {
    $text = [IO.File]::ReadAllText($file.FullName)
    if ($file.Length -gt 4096) { throw "$($file.FullName) exceeds 4096 bytes." }
    if ([regex]::Matches($text, '(?i)\b(never|must not|do not|does not|cannot|remains BLOCKED)\b').Count -gt 5) { throw "$($file.FullName) exceeds five negations." }
    if ($text -notmatch '(?m)^description: .*(?:Use (?:when|for|after|before)|Initialize|Start|Diagnose|Verify|Implement|Estimate|Lint|Write)') { throw "$($file.FullName) lacks a description trigger." }
    foreach ($heading in $headings) {
        if ($text -notmatch ('(?m)^## ' + [regex]::Escape($heading) + '\r?$')) { throw "$($file.FullName) lacks heading: $heading" }
        $checks++
    }
    $checks += 3
}
Write-Host "INSTRUCTION_BUDGET_OK checks=$checks"
