#Requires -Version 7.0
# Surface-area baseline for the remediation plan (Ф0.3): measures PowerShell size by area,
# the bytes of instructions loaded into every session (bootstrap + SKILL.md), how negation-heavy
# those instructions are, and total markdown outside openspec/. Deterministic: no timestamps, no
# machine-dependent values, stable ordering everywhere, so two runs against the same tree produce
# byte-identical JSON. Consumed by scripts/Test-SurfaceBudget.ps1 to enforce the Ф0.1 managed
# freeze and the instruction budgets.
[CmdletBinding()]
param(
    [string]$PackageRoot,
    [string]$OutputPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$root = [IO.Path]::GetFullPath($PackageRoot).TrimEnd('\', '/')
if (-not (Test-Path -LiteralPath $root -PathType Container)) { throw "Package root not found: $root" }

$negationPattern = '(?i)\b(never|must not|do not|does not|cannot|remains BLOCKED)\b'
# Directories whose content is never part of the measured surface: version control internals
# and the framework's own git worktree bookkeeping. Everything else in the package is in scope.
$excludedTopSegments = @('.git', '.claude', '.bsl-flow', '.build', 'work', 'outputs')

function ConvertTo-RelativePath {
    param([string]$FullPath)
    $rel = $FullPath.Substring($root.Length).TrimStart('\', '/')
    return ($rel -replace '\\', '/')
}

function Test-ExcludedRelativePath {
    param([string]$RelativePath)
    $firstSegment = ($RelativePath -split '/', 2)[0]
    return $excludedTopSegments -contains $firstSegment
}

function Get-AllFiles {
    # Prune generated roots before recursion: they can contain protected worker state.
    $entries = @(Get-ChildItem -LiteralPath $root -Force | Where-Object { $_.Name -notin $excludedTopSegments })
    $files = @($entries | Where-Object { -not $_.PSIsContainer })
    foreach ($directory in @($entries | Where-Object { $_.PSIsContainer })) {
        $files += @(Get-ChildItem -LiteralPath $directory.FullName -Recurse -File -Force)
    }
    $files |
        ForEach-Object {
            $relative = ConvertTo-RelativePath $_.FullName
            if (-not (Test-ExcludedRelativePath $relative)) {
                [pscustomobject]@{ Info = $_; Relative = $relative }
            }
        }
}

function Get-LineCount {
    param([string]$Path)
    $content = [IO.File]::ReadAllText($Path)
    if ($content.Length -eq 0) { return 0 }
    $lines = [regex]::Split($content, '\r\n|\r|\n')
    if ($lines.Length -gt 0 -and $lines[$lines.Length - 1] -eq '') { return $lines.Length - 1 }
    return $lines.Length
}

function New-FileRecord {
    param([System.IO.FileInfo]$Info, [string]$Relative)
    [ordered]@{
        path  = $Relative
        bytes = [int64]$Info.Length
        lines = (Get-LineCount $Info.FullName)
    }
}

function New-AreaSummary {
    param([object[]]$Records)
    $sorted = @($Records | Sort-Object -Property path)
    $totalBytes = 0L
    $totalLines = 0L
    foreach ($record in $sorted) { $totalBytes += $record.bytes; $totalLines += $record.lines }
    [ordered]@{
        file_count = $sorted.Count
        bytes      = $totalBytes
        lines      = $totalLines
        files      = $sorted
    }
}

$allFiles = @(Get-AllFiles)
$allPs1 = @($allFiles | Where-Object { $_.Info.Extension -ieq '.ps1' })

# --- core / managed: global/skills, split per the Ф2.2 package boundary. ---
$managedRecords = New-Object System.Collections.Generic.List[object]
$coreRecords = New-Object System.Collections.Generic.List[object]
foreach ($entry in $allPs1) {
    $relative = $entry.Relative
    if ($relative -notmatch '^global/skills/') { continue }
    $fileName = $entry.Info.Name
    $isTaskSkill = $relative -match '^global/skills/1c-task/'
    $isCouncilFile = ($fileName -match '^Council\..+\.ps1$') -or ($fileName -ceq 'Invoke-CouncilReview.ps1')
    if ($isTaskSkill -or $isCouncilFile) {
        $managedRecords.Add((New-FileRecord $entry.Info $relative))
    } else {
        $coreRecords.Add((New-FileRecord $entry.Info $relative))
    }
}

# --- scripts: top-level scripts/*.ps1 only (not suites.d, not skill scripts). ---
$scriptsRecords = New-Object System.Collections.Generic.List[object]
foreach ($entry in $allPs1) {
    if ($entry.Relative -match '^scripts/[^/]+\.ps1$') { $scriptsRecords.Add((New-FileRecord $entry.Info $entry.Relative)) }
}

# --- tests: any file named Test-*.ps1, anywhere in the package (overlaps the areas above). ---
$testsRecords = New-Object System.Collections.Generic.List[object]
foreach ($entry in $allPs1) {
    if ($entry.Info.Name -match '^Test-.*\.ps1$') { $testsRecords.Add((New-FileRecord $entry.Info $entry.Relative)) }
}

$areas = [ordered]@{
    core    = New-AreaSummary $coreRecords
    managed = New-AreaSummary $managedRecords
    scripts = New-AreaSummary $scriptsRecords
    tests   = New-AreaSummary $testsRecords
}

# --- Instruction surface: bootstrap + every SKILL.md, with negation-phrase counts. ---
function Get-NegationCount {
    param([string]$Path)
    $text = [IO.File]::ReadAllText($Path)
    return @([regex]::Matches($text, $negationPattern)).Count
}

$bootstrapPath = Join-Path $root 'global/AGENTS.bootstrap.md'
$bootstrap = $null
if (Test-Path -LiteralPath $bootstrapPath -PathType Leaf) {
    $bootstrapInfo = Get-Item -LiteralPath $bootstrapPath
    $bootstrap = [ordered]@{
        path            = 'global/AGENTS.bootstrap.md'
        bytes           = [int64]$bootstrapInfo.Length
        negation_count  = (Get-NegationCount $bootstrapPath)
    }
}

$skillEntries = @($allFiles | Where-Object { $_.Relative -match '^global/skills/([^/]+)/SKILL\.md$' } | Sort-Object -Property Relative)
$skills = [ordered]@{}
foreach ($entry in $skillEntries) {
    $skillName = [regex]::Match($entry.Relative, '^global/skills/([^/]+)/SKILL\.md$').Groups[1].Value
    $skills[$skillName] = [ordered]@{
        path           = $entry.Relative
        bytes          = [int64]$entry.Info.Length
        negation_count = (Get-NegationCount $entry.Info.FullName)
    }
}

# --- Markdown bytes outside openspec/. ---
$markdownRecords = @($allFiles | Where-Object { $_.Info.Extension -ieq '.md' -and ($_.Relative -split '/', 2)[0] -ne 'openspec' } | Sort-Object -Property Relative)
$markdownBytes = 0L
foreach ($entry in $markdownRecords) { $markdownBytes += $entry.Info.Length }
$markdown = [ordered]@{
    excluded   = 'openspec/'
    file_count = $markdownRecords.Count
    bytes      = $markdownBytes
}

$result = [ordered]@{
    schema_version = 1
    package_root   = ($root -replace '\\', '/')
    areas          = $areas
    bootstrap      = $bootstrap
    skills         = $skills
    markdown       = $markdown
}

$json = $result | ConvertTo-Json -Depth 12
if ($OutputPath) {
    $outFull = [IO.Path]::GetFullPath($OutputPath)
    $outDir = Split-Path -Parent $outFull
    if ($outDir -and -not (Test-Path -LiteralPath $outDir -PathType Container)) { New-Item -ItemType Directory -Path $outDir -Force | Out-Null }
    [IO.File]::WriteAllText($outFull, $json, [Text.UTF8Encoding]::new($false))
} else {
    Write-Output $json
}
