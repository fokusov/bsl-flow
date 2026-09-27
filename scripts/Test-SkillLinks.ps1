#Requires -Version 7.0
# Skill link integrity suite (plan Ф1.3): every relative markdown link inside
# an installed SKILL.md / references/*.md must resolve to a file that stays
# inside global/skills (the installer only copies global/skills/* to
# ~/.agents/skills), and the thin CLI wrapper must still work once the skill
# tree is copied out on its own, without the rest of the repository beside it.
# Standalone (no Pester), zero model/network calls.
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$skillsRoot = Join-Path $PackageRoot 'global\skills'
if (-not (Test-Path -LiteralPath $skillsRoot -PathType Container)) { throw "Missing skills root: $skillsRoot" }

$script:checks = 0
function Assert-L {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "ASSERTION FAILED: $Message" }
    $script:checks++
}

function Get-LMarkdownLinkTargets {
    # Extracts `](target)` markdown link targets from one file's raw text.
    # Ignores http(s)/mailto schemes and pure in-page anchors.
    param([string]$Path)
    $text = [IO.File]::ReadAllText($Path)
    $targets = [System.Collections.Generic.List[string]]::new()
    foreach ($match in [Regex]::Matches($text, '\]\(([^)\s]+)\)')) {
        $target = $match.Groups[1].Value
        if ($target -match '^(?:https?|mailto):') { continue }
        if ($target.StartsWith('#')) { continue }
        $targets.Add($target)
    }
    return $targets
}

function Test-LPathInsideRoot {
    param([string]$Root, [string]$Candidate)
    $rootFull = ([IO.Path]::GetFullPath($Root)).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    $candidateFull = [IO.Path]::GetFullPath($Candidate)
    return $candidateFull.StartsWith($rootFull, [StringComparison]::OrdinalIgnoreCase)
}

# --- Section 1: every relative link in every SKILL.md and references/*.md must resolve inside global/skills and exist.
$sourceFiles = [System.Collections.Generic.List[string]]::new()
foreach ($skillMd in @(Get-ChildItem -LiteralPath $skillsRoot -Filter 'SKILL.md' -Recurse -File)) { $sourceFiles.Add($skillMd.FullName) }
foreach ($skillDir in @(Get-ChildItem -LiteralPath $skillsRoot -Directory)) {
    $referencesDir = Join-Path $skillDir.FullName 'references'
    if (Test-Path -LiteralPath $referencesDir -PathType Container) {
        foreach ($referenceMd in @(Get-ChildItem -LiteralPath $referencesDir -Filter '*.md' -Recurse -File)) { $sourceFiles.Add($referenceMd.FullName) }
    }
}
Assert-L ($sourceFiles.Count -gt 0) 'No SKILL.md/references markdown files were found under global/skills.'

$linkCount = 0
foreach ($sourceFile in $sourceFiles) {
    $sourceDir = Split-Path -Parent $sourceFile
    foreach ($target in @(Get-LMarkdownLinkTargets -Path $sourceFile)) {
        # Anchors inside a file target ("file.md#section") address a heading,
        # not a separate resource; only the path before '#' is a file target.
        $withoutAnchor = $target.Split('#')[0]
        if ([string]::IsNullOrWhiteSpace($withoutAnchor)) { continue }
        $resolved = $null
        try { $resolved = [IO.Path]::GetFullPath((Join-Path $sourceDir $withoutAnchor)) }
        catch { throw "ASSERTION FAILED: link target '$target' in $sourceFile could not be resolved: $($_.Exception.Message)" }
        Assert-L (Test-LPathInsideRoot -Root $skillsRoot -Candidate $resolved) "Link target '$target' in $sourceFile resolves outside global/skills ($resolved); it will break once only global/skills is installed."
        Assert-L (Test-Path -LiteralPath $resolved -PathType Leaf) "Link target '$target' in $sourceFile does not exist ($resolved)."
        $linkCount++
    }
}
Assert-L ($linkCount -gt 0) 'No relative markdown links were checked; the extraction regex may be broken.'
Write-Host "Skill link integrity: checked $linkCount relative link(s) across $($sourceFiles.Count) markdown file(s)."

# --- Section 2: installed layout. Copy global/skills alone (nothing else from the repo) to a temp
# directory, the way the installer does, and confirm the thin CLI wrapper still resolves the controller
# and runs against a fixture project.
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-skill-links-' + [guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($testRoot)
try {
    $installedSkills = Join-Path $testRoot 'skills'
    Copy-Item -LiteralPath $skillsRoot -Destination $installedSkills -Recurse -Force

    $fixtureProject = Join-Path $testRoot 'fixture-project'
    [void][IO.Directory]::CreateDirectory($fixtureProject)
    $gitOutput = & git -C $fixtureProject init 2>&1
    if ($LASTEXITCODE -ne 0) { throw "git init failed for fixture project: $($gitOutput -join ' ')" }

    $installedWrapper = Join-Path $installedSkills '1c-task\scripts\bsl-flow.ps1'
    Assert-L (Test-Path -LiteralPath $installedWrapper -PathType Leaf) "Installed wrapper is missing after copying only global/skills: $installedWrapper"
    $installedController = Join-Path $installedSkills '1c-task\scripts\Invoke-BSLFlowTask.ps1'
    Assert-L (Test-Path -LiteralPath $installedController -PathType Leaf) "Installed controller is missing after copying only global/skills: $installedController"

    $listOutput = @(& $installedWrapper 'task' 'list' '--project' $fixtureProject '-Format' 'Json' 2>&1)
    Assert-L ($LASTEXITCODE -eq 0) "Installed wrapper 'task list' failed with exit $LASTEXITCODE against a fresh fixture project: $($listOutput -join ' ')"
    $listDoc = (@($listOutput | Where-Object { $_ -match '^\{' }) | Select-Object -First 1) | ConvertFrom-Json
    Assert-L ($listDoc.schema_version -eq 1) 'Installed wrapper task list did not return a schema_version=1 document.'
    $script:checks++
    Write-Host "Skill link integrity: installed-layout 'bsl-flow.ps1 task list' returned exit 0 from a copy of global/skills alone."
} finally {
    $safe = [IO.Path]::GetFullPath($testRoot)
    $temp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/') + [IO.Path]::DirectorySeparatorChar
    if (-not $safe.StartsWith($temp, [StringComparison]::OrdinalIgnoreCase) -or [IO.Path]::GetFileName($safe) -notmatch '^bsl-flow-skill-links-') { throw "Unsafe skill-links test cleanup target: $safe" }
    if (Test-Path -LiteralPath $safe) { Remove-Item -LiteralPath $safe -Recurse -Force }
}

Write-Host "Skill links suite passed ($script:checks checks) on PowerShell $($PSVersionTable.PSVersion)."
