#Requires -Version 7.0
# Cross-platform installer for the BSL Flow Core package (docs/plans/2026-09-26-remediation-plan.md
# Ф2.4, ADR-12 in docs/ARCHITECTURE_RU.md). Runs on Windows, Linux and macOS with pwsh 7 from PATH.
# Requires only git and the OpenSpec CLI; does not require OpenCode, Codex, API keys, or a .NET SDK.
[CmdletBinding(SupportsShouldProcess)]
param(
    [Parameter(Mandatory)]
    [ValidateSet('codex', 'claude', 'opencode', 'agents')]
    [string]$HostName,
    [string]$SkillsRoot,
    [string]$CodexHome,
    [string]$ClaudeHome,
    [string]$OpenSpecSchemaRoot,
    [string]$MarkerPath,
    [switch]$SkipCliValidation
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$installStopwatch = [Diagnostics.Stopwatch]::StartNew()

function Assert-BFTestOnlyTarget {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Purpose)
    $temp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/')
    $resolved = [IO.Path]::GetFullPath($Path).TrimEnd('\', '/')
    if (-not $resolved.StartsWith($temp + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Test-only $Purpose override must be below the system temporary directory: $resolved"
    }
}

function Assert-BFRealDirectoryTree {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Purpose, [switch]$RootOnly)
    if (-not (Test-Path -LiteralPath $Path)) { return }
    $item = Get-Item -LiteralPath $Path -Force
    if (-not $item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "$Purpose must be a real directory, not a file or reparse point: $Path"
    }
    if ($RootOnly) { return }
    $nestedReparse = @(Get-ChildItem -LiteralPath $Path -Recurse -Force -ErrorAction SilentlyContinue |
        Where-Object { ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 } | Select-Object -First 1)
    if ($nestedReparse.Count -gt 0) { throw "$Purpose contains a nested reparse point: $($nestedReparse[0].FullName)" }
}

function Set-BFMarkedBlock {
    # Replaces (or appends) a <!-- marker:start/end --> block in $Text with $Block, matching the
    # convention used by global/AGENTS.bootstrap.md and scripts/Install-BSLFlow.ps1.
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Text, [Parameter(Mandatory)][string]$Marker, [Parameter(Mandatory)][string]$Block)
    $start = "<!-- ${Marker}:start -->"
    $end = "<!-- ${Marker}:end -->"
    $pattern = "(?ms)^$([regex]::Escape($start))[^\r\n]*(?:\r?\n).*?^$([regex]::Escape($end))[^\r\n]*"
    $trimmedBlock = $Block.Trim()
    if ([regex]::IsMatch($Text, $pattern)) {
        return ([regex]::Replace($Text, $pattern, $trimmedBlock, 1)).TrimEnd()
    }
    elseif ([string]::IsNullOrWhiteSpace($Text)) {
        return $trimmedBlock
    }
    else {
        return $Text.TrimEnd() + "`n`n" + $trimmedBlock
    }
}

function Get-BFOpenSpecDefaultSchemaRoot {
    # Falls back to the platform default OpenSpec schema location when `openspec schema which`
    # cannot resolve one yet (first-ever install on this machine).
    if ($IsWindows) {
        $localAppData = [Environment]::GetFolderPath('LocalApplicationData')
        return (Join-Path $localAppData 'openspec\schemas\bsl-flow')
    }
    $xdgConfig = $env:XDG_CONFIG_HOME
    $base = if ($xdgConfig) { $xdgConfig } else { Join-Path ([Environment]::GetFolderPath('UserProfile')) '.config' }
    return (Join-Path $base 'openspec/schemas/bsl-flow')
}

$packageRoot = Split-Path -Parent $PSScriptRoot
$globalRoot = Join-Path $packageRoot 'global'
$sourceSkillsRoot = Join-Path $globalRoot 'skills'
$sourceSchema = Join-Path $globalRoot 'openspec/schemas/bsl-flow'
$sourceAgentsBlock = Join-Path $globalRoot 'AGENTS.bootstrap.md'
$sourceClaudeAgents = Join-Path $packageRoot 'hosts/claude-code/agents'
$frameworkVersion = (Get-Content -Raw -LiteralPath (Join-Path $packageRoot 'VERSION')).Trim()
if ($frameworkVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$') { throw "Invalid package VERSION: $frameworkVersion" }

$coreSkillNames = @('1c-init-project', '1c-spec', '1c-spec-review', '1c-implement', '1c-verify', '1c-debug', '1c-estimate')
foreach ($skillName in $coreSkillNames) {
    if (-not (Test-Path -LiteralPath (Join-Path $sourceSkillsRoot "$skillName/SKILL.md") -PathType Leaf)) {
        throw "Core package is incomplete: missing skill $skillName"
    }
}
if (-not (Test-Path -LiteralPath $sourceSchema -PathType Container)) { throw "Core package is incomplete: missing OpenSpec schema at $sourceSchema" }

$userProfile = [Environment]::GetFolderPath('UserProfile')
$isTestRun = [bool]($SkillsRoot -or $CodexHome -or $ClaudeHome -or $OpenSpecSchemaRoot -or $MarkerPath)

$targetSkills = if ($SkillsRoot) { [IO.Path]::GetFullPath($SkillsRoot) }
    elseif ($HostName -eq 'claude') { Join-Path $userProfile '.claude/skills' }
    else { Join-Path $userProfile '.agents/skills' }

$defaultCodexHome = if ($env:CODEX_HOME) { $env:CODEX_HOME } else { Join-Path $userProfile '.codex' }
$codexHome = if ($CodexHome) { [IO.Path]::GetFullPath($CodexHome) } else { [IO.Path]::GetFullPath($defaultCodexHome) }
$targetAgentsMd = Join-Path $codexHome 'AGENTS.md'

$claudeHomeResolved = if ($ClaudeHome) { [IO.Path]::GetFullPath($ClaudeHome) } else { Join-Path $userProfile '.claude' }
$targetClaudeAgents = Join-Path $claudeHomeResolved 'agents'

$markerPath = if ($MarkerPath) { [IO.Path]::GetFullPath($MarkerPath) } else { Join-Path $userProfile '.bsl-flow/installed-core.json' }

if ($isTestRun) {
    foreach ($testTarget in @($targetSkills, $codexHome, $claudeHomeResolved, $markerPath)) {
        Assert-BFTestOnlyTarget -Path $testTarget -Purpose 'Install-BSLFlowCore target'
    }
}

$openSpecCommand = $null
$gitCommand = $null
if (-not $SkipCliValidation) {
    $gitCommand = Get-Command git -ErrorAction SilentlyContinue
    if (-not $gitCommand) { throw 'git is required but was not found in PATH.' }
    $openSpecCommand = Get-Command openspec -ErrorAction SilentlyContinue
    if (-not $openSpecCommand) { throw 'OpenSpec CLI is required but was not found in PATH.' }
}

$targetSchema = if ($OpenSpecSchemaRoot) { [IO.Path]::GetFullPath($OpenSpecSchemaRoot) }
    else {
        $resolvedFromCli = $null
        if ($openSpecCommand) {
            try {
                $whichRaw = & $openSpecCommand.Source 'schema' 'which' 'bsl-flow' 2>$null
                if ($LASTEXITCODE -eq 0 -and $whichRaw) {
                    $whichPath = ($whichRaw | Select-Object -Last 1).ToString().Trim()
                    if ($whichPath) { $resolvedFromCli = Split-Path -Parent ([IO.Path]::GetFullPath($whichPath)) }
                }
            }
            catch { $resolvedFromCli = $null }
        }
        if ($resolvedFromCli) { $resolvedFromCli } else { Get-BFOpenSpecDefaultSchemaRoot }
    }
$targetSchemaParent = Split-Path -Parent $targetSchema

# Migration detection: an old 0.8.x full install put 1c-task next to the Core skills and wrote the
# large bootstrap block directly (no bsl-flow-core marker). Recognize it so the operator gets a
# clear migration notice instead of a silent partial overwrite.
$looksLikeLegacy08Install = (Test-Path -LiteralPath (Join-Path $targetSkills '1c-task') -PathType Container) -or
    (Test-Path -LiteralPath (Join-Path $targetSkills '1c-init-project/SKILL.md') -PathType Leaf)

if (-not $PSCmdlet.ShouldProcess($targetSkills, "Install BSL Flow Core v$frameworkVersion for host '$HostName'")) {
    return
}

if ($WhatIfPreference) {
    Write-Host "Would install Core v$frameworkVersion for host: $HostName"
    Write-Host "Would copy skills ($($coreSkillNames -join ', ')) to: $targetSkills"
    Write-Host "Would install OpenSpec schema to: $targetSchema"
    if ($HostName -eq 'codex') { Write-Host "Would update bootstrap block in: $targetAgentsMd" }
    if ($HostName -eq 'claude') {
        Write-Host "Would copy Claude subagents to: $targetClaudeAgents"
        Write-Host 'Would print plugin-marketplace instructions for hooks (no automatic settings.json edits).'
    }
    Write-Host "Would write install marker to: $markerPath"
    if ($looksLikeLegacy08Install) { Write-Host 'Would detect and migrate a legacy 0.8.x full install (see docs/MIGRATION_0.9_RU.md).' }
    return
}

foreach ($requiredContainer in @($targetSkills, $targetSchemaParent)) {
    if (Test-Path -LiteralPath $requiredContainer -PathType Leaf) { throw "A file exists where an installation directory is required: $requiredContainer" }
}
Assert-BFRealDirectoryTree -Path $targetSkills -Purpose 'Core skills root' -RootOnly
Assert-BFRealDirectoryTree -Path $targetSchema -Purpose 'OpenSpec schema tree'
foreach ($skillName in $coreSkillNames) {
    Assert-BFRealDirectoryTree -Path (Join-Path $targetSkills $skillName) -Purpose 'Core skill'
}

$backupBaseRoot = Join-Path $userProfile '.bsl-flow/backups'
if ($isTestRun) { $backupBaseRoot = Join-Path (Split-Path -Parent $targetSkills) '.bsl-flow-core-backups' }
$backupRoot = Join-Path $backupBaseRoot ('core-v' + $frameworkVersion + '-' + (Get-Date -Format 'yyyyMMdd-HHmmssfff'))

$hadAgentsMd = Test-Path -LiteralPath $targetAgentsMd -PathType Leaf
$existingSkills = @{}
foreach ($skillName in $coreSkillNames) { $existingSkills[$skillName] = Test-Path -LiteralPath (Join-Path $targetSkills $skillName) -PathType Container }
$hadSchema = Test-Path -LiteralPath $targetSchema -PathType Container
$hadClaudeAgents = Test-Path -LiteralPath $targetClaudeAgents -PathType Container

New-Item -ItemType Directory -Path $backupRoot -Force | Out-Null
try {
    if ($hadAgentsMd) {
        New-Item -ItemType Directory -Path (Join-Path $backupRoot 'codex') -Force | Out-Null
        Copy-Item -LiteralPath $targetAgentsMd -Destination (Join-Path $backupRoot 'codex/AGENTS.md')
    }
    foreach ($skillName in $coreSkillNames) {
        if ($existingSkills[$skillName]) {
            $skillBackupParent = Join-Path $backupRoot 'skills'
            New-Item -ItemType Directory -Path $skillBackupParent -Force | Out-Null
            Copy-Item -LiteralPath (Join-Path $targetSkills $skillName) -Destination $skillBackupParent -Recurse
        }
    }
    if ($hadSchema) {
        $schemaBackupParent = Join-Path $backupRoot 'openspec/schemas'
        New-Item -ItemType Directory -Path $schemaBackupParent -Force | Out-Null
        Copy-Item -LiteralPath $targetSchema -Destination $schemaBackupParent -Recurse
    }
    if ($hadClaudeAgents) {
        New-Item -ItemType Directory -Path (Join-Path $backupRoot 'claude') -Force | Out-Null
        Copy-Item -LiteralPath $targetClaudeAgents -Destination (Join-Path $backupRoot 'claude/agents') -Recurse
    }

    New-Item -ItemType Directory -Path $targetSkills -Force | Out-Null
    foreach ($skillName in $coreSkillNames) {
        $targetSkill = Join-Path $targetSkills $skillName
        if (Test-Path -LiteralPath $targetSkill) { Remove-Item -LiteralPath $targetSkill -Recurse -Force }
        Copy-Item -LiteralPath (Join-Path $sourceSkillsRoot $skillName) -Destination $targetSkills -Recurse
    }

    New-Item -ItemType Directory -Path $targetSchemaParent -Force | Out-Null
    if (Test-Path -LiteralPath $targetSchema) { Remove-Item -LiteralPath $targetSchema -Recurse -Force }
    Copy-Item -LiteralPath $sourceSchema -Destination $targetSchemaParent -Recurse

    if ($HostName -eq 'codex') {
        $newBlock = (Get-Content -Raw -LiteralPath $sourceAgentsBlock)
        $existingAgentsText = if ($hadAgentsMd) { Get-Content -Raw -LiteralPath $targetAgentsMd } else { New-Item -ItemType Directory -Path (Split-Path -Parent $targetAgentsMd) -Force | Out-Null; '' }
        $updatedAgentsText = Set-BFMarkedBlock -Text $existingAgentsText -Marker 'bsl-flow bootstrap' -Block $newBlock
        Set-Content -LiteralPath $targetAgentsMd -Value $updatedAgentsText -Encoding utf8
    }

    if ($HostName -eq 'claude') {
        if (Test-Path -LiteralPath $sourceClaudeAgents -PathType Container) {
            New-Item -ItemType Directory -Path $targetClaudeAgents -Force | Out-Null
            Copy-Item -LiteralPath (Join-Path $sourceClaudeAgents '*') -Destination $targetClaudeAgents -Recurse -Force
        }
    }

    if (-not $SkipCliValidation -and $openSpecCommand) {
        Push-Location $targetSchemaParent
        try {
            $probeDir = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-core-schema-probe-' + [guid]::NewGuid().ToString('N'))
            New-Item -ItemType Directory -Path (Join-Path $probeDir 'openspec') -Force | Out-Null
            Set-Content -LiteralPath (Join-Path $probeDir 'openspec/config.yaml') -Value 'schema: bsl-flow' -Encoding utf8
        }
        finally { Pop-Location }
    }

    New-Item -ItemType Directory -Path (Split-Path -Parent $markerPath) -Force | Out-Null
    $marker = [ordered]@{
        version = $frameworkVersion
        host = $HostName
        skills_root = $targetSkills
        schema_root = $targetSchema
        installed_at_utc = (Get-Date -AsUTC).ToString('o')
    }
    ($marker | ConvertTo-Json -Depth 4) | Set-Content -LiteralPath $markerPath -Encoding utf8
}
catch {
    $installationError = $_
    try {
        foreach ($skillName in $coreSkillNames) {
            $targetSkill = Join-Path $targetSkills $skillName
            if (Test-Path -LiteralPath $targetSkill) { Remove-Item -LiteralPath $targetSkill -Recurse -Force }
            if ($existingSkills[$skillName]) { Copy-Item -LiteralPath (Join-Path $backupRoot "skills/$skillName") -Destination $targetSkills -Recurse }
        }
        if (Test-Path -LiteralPath $targetSchema) { Remove-Item -LiteralPath $targetSchema -Recurse -Force }
        if ($hadSchema) {
            New-Item -ItemType Directory -Path $targetSchemaParent -Force | Out-Null
            Copy-Item -LiteralPath (Join-Path $backupRoot 'openspec/schemas/bsl-flow') -Destination $targetSchemaParent -Recurse
        }
        if ($HostName -eq 'codex') {
            if ($hadAgentsMd) { Copy-Item -LiteralPath (Join-Path $backupRoot 'codex/AGENTS.md') -Destination $targetAgentsMd -Force }
            elseif (Test-Path -LiteralPath $targetAgentsMd -PathType Leaf) { Remove-Item -LiteralPath $targetAgentsMd -Force }
        }
        if ($HostName -eq 'claude') {
            if (Test-Path -LiteralPath $targetClaudeAgents) { Remove-Item -LiteralPath $targetClaudeAgents -Recurse -Force }
            if ($hadClaudeAgents) { Copy-Item -LiteralPath (Join-Path $backupRoot 'claude/agents') -Destination $targetClaudeAgents -Recurse }
        }
    }
    catch {
        throw "Install-BSLFlowCore failed and automatic rollback also failed. Original error: $($installationError.Exception.Message). Rollback error: $($_.Exception.Message). Backup: $backupRoot"
    }
    throw "Install-BSLFlowCore failed; the previous installation was restored. Error: $($installationError.Exception.Message). Backup: $backupRoot"
}

$installStopwatch.Stop()

Write-Host "BSL Flow Core v$frameworkVersion installed for host: $HostName"
Write-Host "Skills: $targetSkills"
Write-Host "OpenSpec schema: $targetSchema"
if ($HostName -eq 'codex') { Write-Host "Codex bootstrap: $targetAgentsMd" }
if ($HostName -eq 'claude') {
    Write-Host "Claude subagents: $targetClaudeAgents"
    Write-Host 'To enable enforcement hooks, install the Claude Code plugin instead (recommended):'
    Write-Host '  /plugin marketplace add <github-owner>/bsl-flow'
    Write-Host '  /plugin install bsl-flow@bsl-flow'
    Write-Host 'This installer does not modify ~/.claude/settings.json automatically.'
}
if ($looksLikeLegacy08Install) {
    Write-Host 'A legacy 0.8.x full install was detected and backed up; see docs/MIGRATION_0.9_RU.md for the Core/Managed migration.'
}
Write-Host "Backup: $backupRoot"
Write-Host ("Elapsed: {0:N1}s" -f $installStopwatch.Elapsed.TotalSeconds)
