#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot,[string]$CorePackageRoot,[string]$ManagedPackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if (-not $PackageRoot) { $PackageRoot=Split-Path $PSScriptRoot -Parent }
if (-not $CorePackageRoot) { $CorePackageRoot=$PackageRoot }
if (-not $ManagedPackageRoot) { $ManagedPackageRoot=$PackageRoot }
$core=Join-Path $CorePackageRoot 'scripts/Install-BSLFlowCore.ps1'
$managed=Join-Path $ManagedPackageRoot 'scripts/Install-BSLFlowManaged.ps1'
$version=(Get-Content -Raw (Join-Path $PackageRoot 'VERSION')).Trim()
$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-install-core-'+[guid]::NewGuid().ToString('N'))
$checks=0
function Assert-I([bool]$Value,[string]$Message) { if (-not $Value) { throw $Message }; $script:checks++ }
function Assert-Fails([scriptblock]$Action,[string]$Pattern) {
    $message=$null; try { & $Action | Out-Null } catch { $message=$_.Exception.Message }
    Assert-I ($null -ne $message -and $message -match $Pattern) "Expected '$Pattern'; got '$message'"
}
try {
    New-Item -ItemType Directory $testRoot | Out-Null
    foreach ($hostName in @('codex','claude','opencode','agents')) {
        $target=Join-Path $testRoot $hostName
        $skills=Join-Path $target 'skills'
        $marker=Join-Path $target 'state/installed-core.json'
        $config=Join-Path $target 'config'
        $installArgs=@{HostName=$hostName;SkillsRoot=$skills;MarkerPath=$marker;OpenSpecSchemaRoot=Join-Path $target 'schema';SkipCliValidation=$true}
        if ($hostName -eq 'codex') { $installArgs.CodexHome=$config }
        if ($hostName -eq 'claude') { $installArgs.ClaudeHome=$config }
        if ($hostName -eq 'opencode') { $installArgs.OpenCodeHome=$config }
        if ($hostName -eq 'claude') {
            New-Item -ItemType Directory $config -Force | Out-Null
            @{theme='preserved';hooks=@{SessionStart=@(@{hooks=@(@{type='command';command='echo user-hook'})})}} | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $config 'settings.json')
        }
        & $core @installArgs -WhatIf | Out-Null
        Assert-I (-not (Test-Path $marker)) 'WhatIf wrote receipt.'
        $result=& $core @installArgs
        Assert-I ($result.ElapsedSeconds -le 60) 'Core installation exceeded 60 seconds.'
        $receipt=Get-Content -Raw $marker | ConvertFrom-Json
        Assert-I ($receipt.version -eq $version -and $receipt.host -eq $hostName) 'Bad Core receipt.'
        Assert-I (Test-Path (Join-Path $skills '1c-spec/SKILL.md')) 'Core skill missing.'
        $expectedProvider=switch($hostName) {codex{'codex_exec'};claude{'claude_subagent'};default{'opencode'}}
        $installedTemplate=Get-Content -Raw (Join-Path $skills '1c-init-project/assets/project/bsl-flow.yaml')
        Assert-I ($installedTemplate -match "(?m)^    provider: $expectedProvider$") 'New-project template lacks the host-native reviewer default.'
        Assert-I (-not (Test-Path (Join-Path $skills '1c-task/SKILL.md'))) 'Fresh Core installed managed controller.'
        Assert-I (-not (Test-Path (Join-Path $skills '1c-spec-review/scripts/Council.Profile.ps1'))) 'Core installed Council.'
        if ($hostName -eq 'claude') { Assert-I (Test-Path (Join-Path $config 'agents/bsl-flow-spec-reviewer.md')) 'Claude subagent missing.' }
        $settingsHash=if ($hostName -eq 'claude') { (Get-FileHash (Join-Path $config 'settings.json')).Hash } else { $null }
        $snapshot=(Get-FileHash $marker).Hash
        $skillPath=Join-Path $skills '1c-spec/SKILL.md'
        Set-Content $skillPath 'custom prior content'
        $beforeSkill=(Get-FileHash $skillPath).Hash
        Assert-Fails { & $core @installArgs -SimulatePostApplyFailure } 'previous file contents restored'
        Assert-I ((Get-FileHash $marker).Hash -eq $snapshot) 'Rollback changed Core receipt.'
        Assert-I ((Get-FileHash $skillPath).Hash -eq $beforeSkill) 'Rollback lost existing skill bytes.'
        if ($hostName -eq 'claude') { Assert-I ((Get-FileHash (Join-Path $config 'settings.json')).Hash -eq $settingsHash) 'Rollback changed Claude settings.' }
        & $core @installArgs | Out-Null
        if ($hostName -eq 'claude') {
            $settings=Get-Content -Raw (Join-Path $config 'settings.json') | ConvertFrom-Json
            $commands=@($settings.hooks.SessionStart | ForEach-Object {$_.hooks} | ForEach-Object {$_.command})
            Assert-I ($settings.theme -eq 'preserved' -and $commands -contains 'echo user-hook') 'Claude install lost existing configuration.'
            Assert-I (@($commands | Where-Object {$_ -like '*SessionStart.ps1*'}).Count -eq 1) 'Claude hooks duplicated after reinstall.'
            Assert-I (Test-Path (Join-Path $config 'bsl-flow/hooks/SessionStart.ps1')) 'Installed hook command target missing.'
        }
        if ($IsWindows) {
            Assert-Fails { & $managed -MarkerPath (Join-Path $target 'absent.json') } 'requires_core'
            $receipt.version='0.0.0'; $receipt | ConvertTo-Json | Set-Content $marker
            Assert-Fails { & $managed -MarkerPath $marker } 'requires_core'
            & $core @installArgs | Out-Null
            Assert-Fails { & $managed -MarkerPath $marker -SimulatePostApplyFailure } 'previous file contents restored'
            Assert-I (-not (Test-Path (Join-Path $target 'state/installed-managed.json'))) 'Managed rollback left receipt.'
            & $managed -MarkerPath $marker | Out-Null
            Assert-I (Test-Path (Join-Path $skills '1c-task/SKILL.md')) 'Managed skill missing.'
            Assert-I (Test-Path (Join-Path $skills '1c-spec-review/scripts/Council.Profile.ps1')) 'Managed Council missing.'
        }
    }
    # Legacy full install: preserve unrelated text/files and snapshot overwritten bytes.
    $legacy=Join-Path $testRoot 'legacy'; $skills=Join-Path $legacy 'skills'; $config=Join-Path $legacy 'codex'; $marker=Join-Path $legacy 'state/installed-core.json'
    New-Item -ItemType Directory (Join-Path $skills '1c-task'),(Join-Path $skills '1c-spec'),$config -Force | Out-Null
    Set-Content (Join-Path $skills '1c-task/SKILL.md') 'legacy managed'
    Set-Content (Join-Path $skills '1c-spec/SKILL.md') 'legacy core'
    Set-Content (Join-Path $config 'AGENTS.md') "Personal rules`n<!-- bsl-flow bootstrap:start -->`nOld 0.8 bootstrap`n<!-- bsl-flow bootstrap:end -->`nTail"
    $result=& $core -Host codex -SkillsRoot $skills -CodexHome $config -MarkerPath $marker -OpenSpecSchemaRoot (Join-Path $legacy 'schema') -SkipCliValidation
    $text=Get-Content -Raw (Join-Path $config 'AGENTS.md')
    Assert-I ($text.Contains('Personal rules') -and $text.Contains('Tail') -and -not $text.Contains('Old 0.8 bootstrap')) 'Migration damaged user text or kept legacy bootstrap.'
    Assert-I ((Get-Content -Raw $marker | ConvertFrom-Json).migrated_legacy_08) 'Legacy migration not detected.'
    Assert-I (Test-Path (Join-Path $result.Backup 'restore.json')) 'Backup inventory missing.'
    Assert-I ((Get-Content -Raw (Join-Path $skills '1c-task/SKILL.md')).Trim() -eq 'legacy managed') 'Migration silently removed legacy controller.'
}
finally {
    $resolved=[IO.Path]::GetFullPath($testRoot)
    if ($resolved.StartsWith([IO.Path]::GetFullPath([IO.Path]::GetTempPath()),[StringComparison]::OrdinalIgnoreCase) -and (Split-Path $resolved -Leaf) -like 'bsl-flow-install-core-*') { Remove-Item -LiteralPath $resolved -Recurse -Force -ErrorAction SilentlyContinue }
}
Write-Host "INSTALL_CORE_OK checks=$checks"
