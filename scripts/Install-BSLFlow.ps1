[CmdletBinding(SupportsShouldProcess)]
param(
    [string]$CodexHome,
    [string]$SharedSkillsRoot,
    [string]$OpenSpecSchemaRoot,
    [string]$MetricsPath,
    [switch]$SkipCliValidation,
    [switch]$SimulatePostApplyFailure
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-TestTargets {
    param([Parameter(Mandatory)][string]$CodexRoot, [Parameter(Mandatory)][string[]]$Targets)
    $temp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/')
    $resolvedCodex = [IO.Path]::GetFullPath($CodexRoot).TrimEnd('\', '/')
    if (-not $resolvedCodex.StartsWith($temp + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw 'Test-only targets must be below the system temporary directory.' }
    $relative = $resolvedCodex.Substring($temp.Length + 1)
    if ($relative -notmatch '^bsl-flow-install-test-[^\\/]+[\\/]codex$') { throw 'Test-only CodexHome must be <temp>/bsl-flow-install-test-*/codex.' }
    $testRoot = [IO.Path]::GetFullPath((Split-Path -Parent $resolvedCodex)).TrimEnd('\', '/')
    foreach ($target in $Targets) {
        $resolved = [IO.Path]::GetFullPath($target).TrimEnd('\', '/')
        if (-not $resolved.StartsWith($testRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw "Test target escapes its isolated root: $resolved" }
    }
}

function Invoke-NativeCommand {
    param(
        [Parameter(Mandatory)][string]$Command,
        [Parameter(Mandatory)][string[]]$Arguments
    )

    $previousErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $output = & $Command @Arguments 2>&1 | ForEach-Object { $_.ToString() } | Out-String
        $exitCode = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }

    if ($exitCode -ne 0) {
        throw "Command failed: $Command $($Arguments -join ' ')`n$($output.Trim())"
    }
    $ansiPattern = [string]([char]27) + '\[[0-?]*[ -/]*[@-~]'
    return ([regex]::Replace($output, $ansiPattern, '')).Trim()
}

function Assert-RealDirectoryTree {
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][string]$Purpose,
        [switch]$RootOnly
    )

    if (-not (Test-Path -LiteralPath $Path)) { return }
    $item = Get-Item -LiteralPath $Path -Force
    if (-not $item.PSIsContainer -or ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "$Purpose must be a real directory, not a file or reparse point: $Path"
    }
    if ($RootOnly) { return }
    $nestedReparse = @(Get-ChildItem -LiteralPath $Path -Recurse -Force |
        Where-Object { ($_.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0 } |
        Select-Object -First 1)
    if ($nestedReparse.Count -gt 0) {
        throw "$Purpose contains a nested reparse point: $($nestedReparse[0].FullName)"
    }
}

function Remove-RetiredManagedBlock {
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$Text,
        [Parameter(Mandatory)][string]$Marker
    )

    $start = "<!-- ${Marker}:start -->"
    $end = "<!-- ${Marker}:end -->"
    $startCount = [regex]::Matches($Text, [regex]::Escape($start), 'IgnoreCase').Count
    $endCount = [regex]::Matches($Text, [regex]::Escape($end), 'IgnoreCase').Count
    if ($startCount -ne $endCount -or $startCount -gt 1) {
        throw "Malformed or duplicate retired managed marker: $Marker"
    }
    if ($startCount -eq 0) { return $Text }
    $pattern = "(?ms)^$([regex]::Escape($start))[^\r\n]*(?:\r?\n).*?^$([regex]::Escape($end))[^\r\n]*(?:\r?\n)?"
    $match = [regex]::Match($Text, $pattern)
    if (-not $match.Success) { throw "Retired managed block cannot be parsed: $Marker" }
    return $Text.Remove($match.Index, $match.Length)
}

function Test-PackagedSchema {
    param(
        [Parameter(Mandatory)][string]$OpenSpecCommand,
        [Parameter(Mandatory)][string]$SchemaSource
    )

    $probeRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('bsl-flow-install-probe-' + [guid]::NewGuid().ToString('N'))
    try {
        $probeSchema = Join-Path $probeRoot 'openspec\schemas\bsl-flow'
        New-Item -ItemType Directory -Path $probeSchema -Force | Out-Null
        Copy-Item -Path (Join-Path $SchemaSource '*') -Destination $probeSchema -Recurse -Force
        Set-Content -LiteralPath (Join-Path $probeRoot 'openspec\config.yaml') -Value 'schema: bsl-flow' -Encoding utf8
        Push-Location $probeRoot
        try {
            [void](Invoke-NativeCommand -Command $OpenSpecCommand -Arguments @('schema', 'validate', 'bsl-flow', '--json'))
        }
        finally {
            Pop-Location
        }
    }
    finally {
        $resolvedProbe = [System.IO.Path]::GetFullPath($probeRoot)
        $resolvedTemp = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
        if ($resolvedProbe.StartsWith($resolvedTemp, [System.StringComparison]::OrdinalIgnoreCase) -and (Split-Path -Leaf $resolvedProbe) -like 'bsl-flow-install-probe-*') {
            Remove-Item -LiteralPath $resolvedProbe -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

function Test-ReviewerConfig {
    param(
        [Parameter(Mandatory)][string]$OpenCodeCommand,
        [Parameter(Mandatory)][string]$ReviewerConfig
    )
    $oldConfig = $env:OPENCODE_CONFIG
    $oldDisableProject = $env:OPENCODE_DISABLE_PROJECT_CONFIG
    $oldDisableClaude = $env:OPENCODE_DISABLE_CLAUDE_CODE
    try {
        $env:OPENCODE_CONFIG = $ReviewerConfig
        $env:OPENCODE_DISABLE_PROJECT_CONFIG = '1'
        $env:OPENCODE_DISABLE_CLAUDE_CODE = '1'
        $readSearch = Invoke-NativeCommand -Command $OpenCodeCommand -Arguments @('debug', 'agent', 'bsl-flow-spec-reviewer') | ConvertFrom-Json
        $sealed = Invoke-NativeCommand -Command $OpenCodeCommand -Arguments @('debug', 'agent', 'bsl-flow-spec-reviewer-sealed') | ConvertFrom-Json
    }
    finally {
        if ($null -eq $oldConfig) { Remove-Item Env:OPENCODE_CONFIG -ErrorAction SilentlyContinue } else { $env:OPENCODE_CONFIG = $oldConfig }
        if ($null -eq $oldDisableProject) { Remove-Item Env:OPENCODE_DISABLE_PROJECT_CONFIG -ErrorAction SilentlyContinue } else { $env:OPENCODE_DISABLE_PROJECT_CONFIG = $oldDisableProject }
        if ($null -eq $oldDisableClaude) { Remove-Item Env:OPENCODE_DISABLE_CLAUDE_CODE -ErrorAction SilentlyContinue } else { $env:OPENCODE_DISABLE_CLAUDE_CODE = $oldDisableClaude }
    }
    foreach ($tool in @('edit', 'write', 'bash', 'task', 'webfetch', 'skill')) {
        if ($readSearch.tools.$tool -ne $false -or $sealed.tools.$tool -ne $false) { throw "Reviewer config exposes forbidden tool: $tool" }
    }
    foreach ($tool in @('read', 'glob')) {
        if ($readSearch.tools.$tool -ne $true) { throw "Read/search reviewer is missing tool: $tool" }
        if ($sealed.tools.$tool -ne $false) { throw "Sealed reviewer unexpectedly exposes tool: $tool" }
    }
    if ($readSearch.tools.grep -ne $false) { throw 'Read/search reviewer exposes unrestricted content grep.' }
    if ($sealed.tools.grep -ne $false) { throw 'Sealed reviewer unexpectedly exposes grep.' }
}

$packageRoot = Split-Path -Parent $PSScriptRoot
$globalRoot = Join-Path $packageRoot 'global'
$sourceSkills = Join-Path $globalRoot 'skills'
$sourceSchema = Join-Path $globalRoot 'openspec\schemas\bsl-flow'
$sourceAgentsBlock = Join-Path $globalRoot 'AGENTS.bootstrap.md'
$sourceReviewerConfig = Join-Path $sourceSkills '1c-spec-review\reviewer\opencode-reviewer.json'
$frameworkVersion = (Get-Content -Raw -LiteralPath (Join-Path $packageRoot 'VERSION')).Trim()
if ($frameworkVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$') { throw "Invalid package VERSION: $frameworkVersion" }

$userProfile = [Environment]::GetFolderPath('UserProfile')
$retiredFrameworkName = '1' + 'c-' + 'lite'
$defaultCodexHome = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { Join-Path $userProfile '.codex' }
$codexHome = if ($CodexHome) { [IO.Path]::GetFullPath($CodexHome) } else { [IO.Path]::GetFullPath($defaultCodexHome) }
$localAppData = [Environment]::GetFolderPath('LocalApplicationData')
$targetSkills = if ($SharedSkillsRoot) { [IO.Path]::GetFullPath($SharedSkillsRoot) } else { Join-Path $userProfile '.agents\skills' }
$legacyCodexSkills = Join-Path $codexHome 'skills'
$targetSchema = if ($OpenSpecSchemaRoot) { [IO.Path]::GetFullPath($OpenSpecSchemaRoot) } else { Join-Path $localAppData 'openspec\schemas\bsl-flow' }
$targetSchemaParent = Split-Path -Parent $targetSchema
$retiredSchema = Join-Path $targetSchemaParent $retiredFrameworkName
$targetAgents = Join-Path $codexHome 'AGENTS.md'
$metricsPath = if ($MetricsPath) { [IO.Path]::GetFullPath($MetricsPath) } else { Join-Path $userProfile '.bsl-flow\evals\spec-runs.jsonl' }
$backupRoot = Join-Path $codexHome ('.backups\bsl-flow-v' + $frameworkVersion + '-' + (Get-Date -Format 'yyyyMMdd-HHmmssfff'))
$customTargets = $PSBoundParameters.ContainsKey('CodexHome') -or $PSBoundParameters.ContainsKey('SharedSkillsRoot') -or $PSBoundParameters.ContainsKey('OpenSpecSchemaRoot') -or $PSBoundParameters.ContainsKey('MetricsPath')
if ($customTargets -or $SkipCliValidation -or $SimulatePostApplyFailure) {
    if (-not $customTargets) { throw 'Test-only switches require explicit isolated target roots.' }
    Assert-TestTargets -CodexRoot $codexHome -Targets @($targetSkills, $targetSchema, $metricsPath)
}

foreach ($requiredPath in @($sourceSkills, $sourceSchema, $sourceAgentsBlock, $sourceReviewerConfig)) {
    if (-not (Test-Path -LiteralPath $requiredPath)) {
        throw "Package is incomplete: $requiredPath"
    }
}

$openSpecCommand = $null
$openCodeCommand = $null
if (-not $SkipCliValidation) {
    $openSpecCommand = Get-Command openspec -ErrorAction SilentlyContinue
    $openCodeCommand = Get-Command opencode -ErrorAction SilentlyContinue
    if (-not $openSpecCommand) { throw 'OpenSpec CLI is required but was not found in PATH.' }
    if (-not $openCodeCommand) { throw 'OpenCode CLI is required but was not found in PATH.' }
}

$skillNames = @('1c-init-project', '1c-spec', '1c-spec-review', '1c-implement', '1c-verify', '1c-debug', '1c-task')
foreach ($skillName in $skillNames) {
    if (-not (Test-Path -LiteralPath (Join-Path $sourceSkills "$skillName\SKILL.md") -PathType Leaf)) {
        throw "Package skill is incomplete: $skillName"
    }
}

if ($WhatIfPreference) {
    Write-Host "Would validate packaged schema: $sourceSchema"
    Write-Host "Would install skills to: $targetSkills"
    Write-Host "Would remove backed-up BSL Flow duplicates from: $legacyCodexSkills"
    Write-Host "Would replace schema at: $targetSchema"
    Write-Host "Would remove backed-up retired schema at: $retiredSchema"
    Write-Host "Would update global AGENTS.md: $targetAgents"
    Write-Host "Would initialize global metrics file if absent: $metricsPath"
    Write-Host "Would create backup under: $backupRoot"
    return
}

if (-not $SkipCliValidation) {
    Test-PackagedSchema -OpenSpecCommand $openSpecCommand.Source -SchemaSource $sourceSchema
    Test-ReviewerConfig -OpenCodeCommand $openCodeCommand.Source -ReviewerConfig $sourceReviewerConfig
}

foreach ($requiredContainer in @($codexHome, $targetSkills, $targetSchemaParent)) {
    if (Test-Path -LiteralPath $requiredContainer -PathType Leaf) {
        throw "A file exists where an installation directory is required: $requiredContainer"
    }
}
foreach ($managedContainer in @($targetSkills, $legacyCodexSkills)) {
    Assert-RealDirectoryTree -Path $managedContainer -Purpose 'Managed installation root' -RootOnly
}
foreach ($managedContainer in @($targetSchema, $retiredSchema)) {
    Assert-RealDirectoryTree -Path $managedContainer -Purpose 'Managed installation tree'
}
if (Test-Path -LiteralPath $targetAgents -PathType Container) {
    throw "A directory exists where global AGENTS.md is required: $targetAgents"
}
if (Test-Path -LiteralPath $targetSchema -PathType Leaf) {
    throw "A file exists where the bsl-flow schema directory is required: $targetSchema"
}
foreach ($skillName in $skillNames) {
    $targetSkill = Join-Path $targetSkills $skillName
    if (Test-Path -LiteralPath $targetSkill -PathType Leaf) {
        throw "A file exists where the skill directory is required: $targetSkill"
    }
    Assert-RealDirectoryTree -Path $targetSkill -Purpose 'Managed shared skill'
    Assert-RealDirectoryTree -Path (Join-Path $legacyCodexSkills $skillName) -Purpose 'Legacy Codex skill scheduled for migration'
}

$hadAgents = Test-Path -LiteralPath $targetAgents -PathType Leaf
$hadSchema = Test-Path -LiteralPath $targetSchema -PathType Container
$hadRetiredSchema = Test-Path -LiteralPath $retiredSchema -PathType Container
$hadMetrics = Test-Path -LiteralPath $metricsPath -PathType Leaf
$existingSkills = @{}
$legacyCodexSkillCopies = @{}
foreach ($skillName in $skillNames) {
    $existingSkills[$skillName] = Test-Path -LiteralPath (Join-Path $targetSkills $skillName) -PathType Container
    $legacyCodexSkillCopies[$skillName] = Test-Path -LiteralPath (Join-Path $legacyCodexSkills $skillName) -PathType Container
}

if (-not $PSCmdlet.ShouldProcess($codexHome, "Install BSL Flow v$frameworkVersion with backup and rollback")) {
    return
}

New-Item -ItemType Directory -Path $backupRoot -Force | Out-Null

if ($hadAgents) {
    New-Item -ItemType Directory -Path (Join-Path $backupRoot 'codex') -Force | Out-Null
    Copy-Item -LiteralPath $targetAgents -Destination (Join-Path $backupRoot 'codex\AGENTS.md')
}

foreach ($skillName in $skillNames) {
    if ($existingSkills[$skillName]) {
        $skillBackupParent = Join-Path $backupRoot 'skills'
        New-Item -ItemType Directory -Path $skillBackupParent -Force | Out-Null
        Copy-Item -LiteralPath (Join-Path $targetSkills $skillName) -Destination $skillBackupParent -Recurse
    }
    if ($legacyCodexSkillCopies[$skillName]) {
        $legacyBackupParent = Join-Path $backupRoot 'legacy-codex-skills'
        New-Item -ItemType Directory -Path $legacyBackupParent -Force | Out-Null
        Copy-Item -LiteralPath (Join-Path $legacyCodexSkills $skillName) -Destination $legacyBackupParent -Recurse
    }
}

if ($hadSchema) {
    $schemaBackupParent = Join-Path $backupRoot 'openspec\schemas'
    New-Item -ItemType Directory -Path $schemaBackupParent -Force | Out-Null
    Copy-Item -LiteralPath $targetSchema -Destination $schemaBackupParent -Recurse
}
if ($hadRetiredSchema) {
    $schemaBackupParent = Join-Path $backupRoot 'openspec\schemas'
    New-Item -ItemType Directory -Path $schemaBackupParent -Force | Out-Null
    Copy-Item -LiteralPath $retiredSchema -Destination $schemaBackupParent -Recurse
}

try {
    New-Item -ItemType Directory -Path $targetSkills -Force | Out-Null
    foreach ($skillName in $skillNames) {
        $targetSkill = Join-Path $targetSkills $skillName
        if (Test-Path -LiteralPath $targetSkill) {
            Remove-Item -LiteralPath $targetSkill -Recurse -Force
        }
        Copy-Item -LiteralPath (Join-Path $sourceSkills $skillName) -Destination $targetSkills -Recurse
        $legacySkill = Join-Path $legacyCodexSkills $skillName
        if (Test-Path -LiteralPath $legacySkill) {
            Remove-Item -LiteralPath $legacySkill -Recurse -Force
        }
    }

    New-Item -ItemType Directory -Path $targetSchemaParent -Force | Out-Null
    if (Test-Path -LiteralPath $targetSchema) {
        Remove-Item -LiteralPath $targetSchema -Recurse -Force
    }
    Copy-Item -LiteralPath $sourceSchema -Destination $targetSchemaParent -Recurse
    if (Test-Path -LiteralPath $retiredSchema) {
        Remove-Item -LiteralPath $retiredSchema -Recurse -Force
    }

    $newBlock = (Get-Content -Raw -LiteralPath $sourceAgentsBlock).Trim()
    if ($hadAgents) {
        $agentsText = Get-Content -Raw -LiteralPath $targetAgents
    }
    else {
        New-Item -ItemType Directory -Path (Split-Path -Parent $targetAgents) -Force | Out-Null
        $agentsText = ''
    }

    $agentsText = Remove-RetiredManagedBlock -Text $agentsText -Marker "$retiredFrameworkName bootstrap"
    $markedPattern = '(?ms)^<!-- bsl-flow bootstrap:start -->[^\r\n]*(?:\r?\n).*?^<!-- bsl-flow bootstrap:end -->[^\r\n]*'
    if ([regex]::IsMatch($agentsText, $markedPattern)) {
        $updatedAgents = [regex]::Replace($agentsText, $markedPattern, $newBlock + "`n`n", 1).TrimEnd()
    }
    elseif ([string]::IsNullOrWhiteSpace($agentsText)) {
        $updatedAgents = $newBlock
    }
    else {
        $updatedAgents = $agentsText.TrimEnd() + "`n`n" + $newBlock
    }
    Set-Content -LiteralPath $targetAgents -Value $updatedAgents -Encoding utf8

    if ($SimulatePostApplyFailure) { throw 'Simulated post-apply failure.' }
    if (-not $SkipCliValidation) {
        $whichOutput = Invoke-NativeCommand -Command $openSpecCommand.Source -Arguments @('schema', 'which', 'bsl-flow')
        $validateOutput = Invoke-NativeCommand -Command $openSpecCommand.Source -Arguments @('schema', 'validate', 'bsl-flow', '--json')
        Test-ReviewerConfig -OpenCodeCommand $openCodeCommand.Source -ReviewerConfig (Join-Path $targetSkills '1c-spec-review\reviewer\opencode-reviewer.json')
    }
    if (-not $hadMetrics) {
        New-Item -ItemType Directory -Path (Split-Path -Parent $metricsPath) -Force | Out-Null
        New-Item -ItemType File -Path $metricsPath | Out-Null
    }
}
catch {
    $installationError = $_
    try {
        foreach ($skillName in $skillNames) {
            $targetSkill = Join-Path $targetSkills $skillName
            if (Test-Path -LiteralPath $targetSkill) {
                Remove-Item -LiteralPath $targetSkill -Recurse -Force
            }
            if ($existingSkills[$skillName]) {
                Copy-Item -LiteralPath (Join-Path $backupRoot "skills\$skillName") -Destination $targetSkills -Recurse
            }
            $legacySkill = Join-Path $legacyCodexSkills $skillName
            if (Test-Path -LiteralPath $legacySkill) {
                Remove-Item -LiteralPath $legacySkill -Recurse -Force
            }
            if ($legacyCodexSkillCopies[$skillName]) {
                New-Item -ItemType Directory -Path $legacyCodexSkills -Force | Out-Null
                Copy-Item -LiteralPath (Join-Path $backupRoot "legacy-codex-skills\$skillName") -Destination $legacyCodexSkills -Recurse
            }
        }

        if (Test-Path -LiteralPath $targetSchema) {
            Remove-Item -LiteralPath $targetSchema -Recurse -Force
        }
        if ($hadSchema) {
            New-Item -ItemType Directory -Path $targetSchemaParent -Force | Out-Null
            Copy-Item -LiteralPath (Join-Path $backupRoot 'openspec\schemas\bsl-flow') -Destination $targetSchemaParent -Recurse
        }
        if (Test-Path -LiteralPath $retiredSchema) {
            Remove-Item -LiteralPath $retiredSchema -Recurse -Force
        }
        if ($hadRetiredSchema) {
            New-Item -ItemType Directory -Path $targetSchemaParent -Force | Out-Null
            Copy-Item -LiteralPath (Join-Path $backupRoot "openspec\schemas\$retiredFrameworkName") -Destination $targetSchemaParent -Recurse
        }

        if ($hadAgents) {
            Copy-Item -LiteralPath (Join-Path $backupRoot 'codex\AGENTS.md') -Destination $targetAgents -Force
        }
        elseif (Test-Path -LiteralPath $targetAgents -PathType Leaf) {
            Remove-Item -LiteralPath $targetAgents -Force
        }
        if (-not $hadMetrics -and (Test-Path -LiteralPath $metricsPath -PathType Leaf) -and (Get-Item -LiteralPath $metricsPath).Length -eq 0) {
            Remove-Item -LiteralPath $metricsPath -Force
        }
    }
    catch {
        throw "bsl-flow installation failed and automatic rollback also failed. Original error: $($installationError.Exception.Message). Rollback error: $($_.Exception.Message). Backup: $backupRoot"
    }
    throw "bsl-flow installation failed; the previous installation was restored. Error: $($installationError.Exception.Message). Backup: $backupRoot"
}

Write-Host "BSL Flow v$frameworkVersion installed."
Write-Host "Codex home: $codexHome"
Write-Host "Shared skills: $targetSkills"
Write-Host "Backup: $backupRoot"
if (-not $SkipCliValidation) { Write-Host $whichOutput; Write-Host $validateOutput }
Write-Host "Cross-project metrics: $metricsPath"
Write-Host 'Restart Codex before using the new global skills.'
