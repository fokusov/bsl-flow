#Requires -Version 7.0
[CmdletBinding(SupportsShouldProcess)]
param(
    [Parameter(Mandatory)][Alias('Host')][ValidateSet('codex','claude','opencode','agents')][string]$HostName,
    [string]$SkillsRoot, [string]$CodexHome, [string]$ClaudeHome, [string]$OpenCodeHome,
    [string]$OpenSpecSchemaRoot, [string]$MarkerPath,
    [switch]$SkipCliValidation, [switch]$SimulatePostApplyFailure
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Install.Package.ps1')
$timer = [Diagnostics.Stopwatch]::StartNew()
$root = Split-Path $PSScriptRoot -Parent
Assert-BFPackageIntegrity -Root $root -Package core
$version = (Get-Content -Raw (Join-Path $root 'VERSION')).Trim()
$profileRoot = [Environment]::GetFolderPath('UserProfile')
$isolated = [bool]($SkillsRoot -or $CodexHome -or $ClaudeHome -or $OpenCodeHome -or $OpenSpecSchemaRoot -or $MarkerPath)
if (($SkipCliValidation -or $SimulatePostApplyFailure) -and -not $isolated) { throw 'Test-only switches require explicit isolated targets.' }
if (-not $SkillsRoot) { $SkillsRoot = Join-Path $profileRoot $(if ($HostName -eq 'claude') { '.claude/skills' } else { '.agents/skills' }) }
if (-not $MarkerPath) { $MarkerPath = Join-Path $profileRoot '.bsl-flow/installed-core.json' }
$hostConfig = switch ($HostName) {
    codex { if ($CodexHome) { $CodexHome } elseif ($env:CODEX_HOME) { $env:CODEX_HOME } else { Join-Path $profileRoot '.codex' } }
    claude { if ($ClaudeHome) { $ClaudeHome } else { Join-Path $profileRoot '.claude' } }
    opencode { if ($OpenCodeHome) { $OpenCodeHome } elseif ($env:XDG_CONFIG_HOME) { Join-Path $env:XDG_CONFIG_HOME 'opencode' } else { Join-Path $profileRoot '.config/opencode' } }
    agents { Split-Path $SkillsRoot -Parent }
}
if (-not $SkipCliValidation) {
    if (-not (Get-Command git -ErrorAction SilentlyContinue)) { throw 'git is required in PATH.' }
    $openSpec = Get-Command openspec -ErrorAction Stop
}
if (-not $OpenSpecSchemaRoot) {
    if ($isolated) { throw 'Isolated installation requires OpenSpecSchemaRoot.' }
    $resolvedSchema = & $openSpec.Source schema which bsl-flow --json 2>$null
    if ($LASTEXITCODE -eq 0 -and $resolvedSchema) {
        $schemaInfo=($resolvedSchema -join "`n") | ConvertFrom-Json
        if ($schemaInfo.source -eq 'user' -and [IO.Path]::IsPathRooted([string]$schemaInfo.path)) { $OpenSpecSchemaRoot=[string]$schemaInfo.path }
    }
    if (-not $OpenSpecSchemaRoot) {
        $configRoot = if ($env:XDG_CONFIG_HOME) { $env:XDG_CONFIG_HOME } elseif ($IsWindows) { [Environment]::GetFolderPath('LocalApplicationData') } else { Join-Path $profileRoot '.config' }
        $OpenSpecSchemaRoot = Join-Path $configRoot 'openspec/schemas/bsl-flow'
    }
}
foreach ($target in @($SkillsRoot,$hostConfig,$OpenSpecSchemaRoot,$MarkerPath)) { Assert-BFInstallTarget $target -Isolated:$isolated }
$definition = Get-Content -Raw (Join-Path $root 'packaging/core.json') | ConvertFrom-Json
$copies = [Collections.Generic.List[object]]::new()
foreach ($file in Get-ChildItem (Join-Path $root 'global/skills') -Recurse -File) {
    $relative = [IO.Path]::GetRelativePath($root,$file.FullName).Replace('\','/')
    if (Test-BFPackageMember $relative $definition) { $copies.Add(@{ source=$file.FullName; target=Join-Path $SkillsRoot $relative.Substring('global/skills/'.Length) }) }
}
foreach ($name in @('1c-init-project','1c-spec','1c-spec-review','1c-implement','1c-verify','1c-debug','1c-estimate')) {
    if (-not ($copies.target -contains (Join-Path $SkillsRoot "$name/SKILL.md"))) { throw "Incomplete Core: $name" }
}
foreach ($file in Get-ChildItem (Join-Path $root 'global/openspec/schemas/bsl-flow') -Recurse -File) {
    $copies.Add(@{source=$file.FullName;target=Join-Path $OpenSpecSchemaRoot ([IO.Path]::GetRelativePath((Join-Path $root 'global/openspec/schemas/bsl-flow'),$file.FullName))})
}
$textWrites = @{}
$templatePath=Join-Path $root 'global/skills/1c-init-project/assets/project/bsl-flow.yaml'
$provider= switch ($HostName) { codex {'codex_exec'}; claude {'claude_subagent'}; default {'opencode'} }
$template=Get-Content -Raw $templatePath
# Host default affects only the installed new-project template, never existing project config.
if ($template -match '(?m)^  reviewer:\s*$') { throw 'Project template now declares a reviewer; update the host-default merge explicitly.' }
$textWrites[(Join-Path $SkillsRoot '1c-init-project/assets/project/bsl-flow.yaml')]=[regex]::Replace($template,'(?m)^review:\s*$',"review:`n  reviewer:`n    provider: $provider")
if ($HostName -in @('codex','opencode','agents')) {
    $agentsPath = Join-Path $hostConfig 'AGENTS.md'
    $old = if (Test-Path $agentsPath) { Get-Content -Raw $agentsPath } else { '' }
    $block = (Get-Content -Raw (Join-Path $root 'global/AGENTS.bootstrap.md')).Trim()
    $pattern = '(?ms)^<!-- bsl-flow bootstrap:start -->.*?^<!-- bsl-flow bootstrap:end -->'
    $textWrites[$agentsPath] = if ($old -match $pattern) { [regex]::Replace($old,$pattern,[Text.RegularExpressions.MatchEvaluator]{param($m) $block}) } else { ($old.TrimEnd()+"`n`n"+$block).TrimStart() }
}
$agentSource = if ($HostName -eq 'claude') { Join-Path $root 'hosts/claude-code/agents' } else { $null }
if ($agentSource) {
    foreach ($file in Get-ChildItem $agentSource -File) { $copies.Add(@{source=$file.FullName;target=Join-Path $hostConfig ('agents/'+$file.Name)}) }
}
if ($HostName -eq 'claude') {
    $hookTarget=Join-Path $hostConfig 'bsl-flow/hooks'
    foreach ($file in Get-ChildItem (Join-Path $root 'hosts/claude-code/hooks') -File -Filter '*.ps1') { $copies.Add(@{source=$file.FullName;target=Join-Path $hookTarget $file.Name}) }
    $settingsPath=Join-Path $hostConfig 'settings.json'
    $settings=if (Test-Path $settingsPath) { Get-Content -Raw $settingsPath | ConvertFrom-Json -AsHashtable } else { [ordered]@{} }
    if ($settings -isnot [Collections.IDictionary]) { throw 'Claude settings.json must be a JSON object.' }
    if (-not $settings.Contains('hooks')) { $settings['hooks']=[ordered]@{} }
    if ($settings.hooks -isnot [Collections.IDictionary]) { throw 'Claude settings.hooks must be a JSON object.' }
    $sourceHooks=Get-Content -Raw (Join-Path $root 'hosts/claude-code/hooks/hooks.json') | ConvertFrom-Json -AsHashtable
    foreach ($event in $sourceHooks.hooks.Keys) {
        $existing=@(if ($settings.hooks.Contains($event)) { $settings.hooks[$event] })
        foreach ($group in $sourceHooks.hooks[$event]) {
            foreach ($hook in $group.hooks) { $hook.command=$hook.command.Replace('${CLAUDE_PLUGIN_ROOT}/hosts/claude-code/hooks',$hookTarget.Replace('\','/')) }
            $wanted=@($group.hooks | ForEach-Object {$_.command})
            $kept=@(foreach ($groupOld in $existing) {
                if ($groupOld -isnot [Collections.IDictionary] -or -not $groupOld.Contains('hooks')) { throw "Invalid Claude hook group: $event" }
                $remaining=@($groupOld.hooks | Where-Object { -not $_.Contains('command') -or $_.command -notin $wanted })
                if ($remaining.Count) { $groupOld.hooks=$remaining; $groupOld }
            })
            $existing=@($kept)+@($group)
        }
        $settings.hooks[$event]=$existing
    }
    $textWrites[$settingsPath]=$settings | ConvertTo-Json -Depth 32
}
$legacy = (Test-Path (Join-Path $SkillsRoot '1c-task/SKILL.md')) -and -not (Test-Path $MarkerPath)
$textWrites[$MarkerPath] = ([ordered]@{package='core';version=$version;host=$HostName;skills_root=[IO.Path]::GetFullPath($SkillsRoot);schema_root=[IO.Path]::GetFullPath($OpenSpecSchemaRoot);migrated_legacy_08=$legacy;installed_at_utc=[DateTime]::UtcNow.ToString('o')} | ConvertTo-Json)
if (-not $PSCmdlet.ShouldProcess($SkillsRoot,"Install Core $version for $HostName")) { return }
$backup = Invoke-BFInstallTransaction -Copies $copies.ToArray() -TextWrites $textWrites -MarkerPath $MarkerPath -Isolated:$isolated -SimulatePostApplyFailure:$SimulatePostApplyFailure
$timer.Stop()
if ($legacy) { Write-Warning 'Legacy full installation preserved and backed up. Reinstall matching Managed before using its controller; see docs/MIGRATION_0.9_RU.md.' }
if ($HostName -eq 'claude') { Write-Host 'Offline skills, subagents and hooks installed; existing Claude settings preserved.' }
[pscustomobject]@{Package='core';Version=$version;HostName=$HostName;Backup=$backup;ElapsedSeconds=$timer.Elapsed.TotalSeconds;MarkerPath=$MarkerPath}
