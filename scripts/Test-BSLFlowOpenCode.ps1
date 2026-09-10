#Requires -Version 7.0
[CmdletBinding()]
param(
    [string]$OpenCodeConfigRoot,
    [string]$OpenCodePath,
    [string]$SharedSkillsRoot,
    [string]$EffectiveConfigPath,
    [switch]$SkipReviewerProbe
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Invoke-CleanNative {
    param([Parameter(Mandatory)][string]$Command, [Parameter(Mandatory)][string[]]$Arguments)
    $previous = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $output = & $Command @Arguments 2>&1 | ForEach-Object { $_.ToString() } | Out-String
        $exitCode = $LASTEXITCODE
    }
    finally { $ErrorActionPreference = $previous }
    if ($exitCode -ne 0) { throw "Command failed: $Command $($Arguments -join ' ')`n$($output.Trim())" }
    $ansiPattern = [string]([char]27) + '\[[0-?]*[ -/]*[@-~]'
    ([regex]::Replace($output, $ansiPattern, '')).Trim()
}

function Get-Prop {
    param([object]$Object, [string]$Name)
    if ($null -eq $Object) { return $null }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    $property.Value
}

function Invoke-OpenCodeForRoot {
    param([string]$Command,[string[]]$Arguments,[string]$ConfigRoot)
    $oldConfig=$env:OPENCODE_CONFIG;$oldXdg=$env:XDG_CONFIG_HOME
    try {
        $env:XDG_CONFIG_HOME=Split-Path -Parent $ConfigRoot
        $configFile=Join-Path $ConfigRoot 'opencode.json'
        if(Test-Path -LiteralPath $configFile -PathType Leaf){$env:OPENCODE_CONFIG=$configFile}else{Remove-Item Env:OPENCODE_CONFIG -ErrorAction SilentlyContinue}
        Invoke-CleanNative $Command $Arguments
    }
    finally {
        if($null-eq$oldConfig){Remove-Item Env:OPENCODE_CONFIG -ErrorAction SilentlyContinue}else{$env:OPENCODE_CONFIG=$oldConfig}
        if($null-eq$oldXdg){Remove-Item Env:XDG_CONFIG_HOME -ErrorAction SilentlyContinue}else{$env:XDG_CONFIG_HOME=$oldXdg}
    }
}

function Assert-TempFixtureMode {
    param([Parameter(Mandatory)][string]$ConfigRoot)
    $temp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\','/')
    $resolved = [IO.Path]::GetFullPath($ConfigRoot).TrimEnd('\','/')
    if (-not $resolved.StartsWith($temp + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Fixture-only switches are allowed only for a config root below the system temporary directory.'
    }
    $relative = $resolved.Substring($temp.Length + 1)
    if ($relative -notmatch '^bsl-flow-opencode-test-[^\\/]+[\\/]opencode$') {
        throw 'Fixture-only config root must be <temp>/bsl-flow-opencode-test-*/opencode.'
    }
}

$userProfile = [Environment]::GetFolderPath('UserProfile')
if ([string]::IsNullOrWhiteSpace($OpenCodeConfigRoot)) { $OpenCodeConfigRoot = Join-Path $userProfile '.config\opencode' }
if ([string]::IsNullOrWhiteSpace($SharedSkillsRoot)) { $SharedSkillsRoot = Join-Path $userProfile '.agents\skills' }
$configRoot = [IO.Path]::GetFullPath($OpenCodeConfigRoot).TrimEnd('\','/')
$sharedSkills = [IO.Path]::GetFullPath($SharedSkillsRoot).TrimEnd('\','/')
if ((Split-Path -Leaf $configRoot) -ine 'opencode') { throw "OpenCode config root must be a directory named 'opencode': $configRoot" }
if (-not (Test-Path -LiteralPath $configRoot -PathType Container)) { throw "OpenCode config directory not found: $configRoot" }

$skillNames = @('1c-init-project','1c-spec','1c-spec-review','1c-implement','1c-verify','1c-debug','1c-task')
$skillRoot = $sharedSkills
$skillRows = @()
foreach ($name in $skillNames) {
    $skillFile = Join-Path $skillRoot "$name\SKILL.md"
    if (-not (Test-Path -LiteralPath $skillFile -PathType Leaf)) { throw "Installed OpenCode skill is missing: $skillFile" }
    $text = Get-Content -Raw -LiteralPath $skillFile
    if ($text -notmatch "(?ms)^---\s*\r?\nname:\s*$([regex]::Escape($name))\s*\r?\ndescription:\s*\S.+?\r?\n---") { throw "Invalid installed skill frontmatter: $name" }
    $skillRows += [ordered]@{name=$name;path=$skillFile;status='file_discovery_ready'}
}

$agentsPath = Join-Path $configRoot 'AGENTS.md'
if (-not (Test-Path -LiteralPath $agentsPath -PathType Leaf)) { throw "OpenCode global AGENTS.md not found: $agentsPath" }
$agentsText = Get-Content -Raw -LiteralPath $agentsPath
foreach ($marker in @('bsl-flow bootstrap','bsl-flow opencode')) {
    $startCount = ([regex]::Matches($agentsText, "<!-- ${marker}:start -->", 'IgnoreCase')).Count
    $endCount = ([regex]::Matches($agentsText, "<!-- ${marker}:end -->", 'IgnoreCase')).Count
    if ($startCount -ne 1 -or $endCount -ne 1) { throw "Expected exactly one managed '$marker' block in $agentsPath" }
}

$fixtureMode = -not [string]::IsNullOrWhiteSpace($EffectiveConfigPath)
if ($fixtureMode -or $SkipReviewerProbe) { Assert-TempFixtureMode $configRoot }

if ($fixtureMode) {
    $effectiveConfigFile = [IO.Path]::GetFullPath($EffectiveConfigPath)
    if (-not (Test-Path -LiteralPath $effectiveConfigFile -PathType Leaf)) { throw "Effective config fixture not found: $effectiveConfigFile" }
    $effective = Get-Content -Raw -LiteralPath $effectiveConfigFile | ConvertFrom-Json -ErrorAction Stop
    $openCodeVersion = $null
    $configSource = 'fixture'
    $catalogModels = @()
}
else {
    if ([string]::IsNullOrWhiteSpace($OpenCodePath)) {
        $command = Get-Command opencode -ErrorAction SilentlyContinue
        if (-not $command) { throw 'OpenCode CLI was not found in PATH.' }
        $OpenCodePath = $command.Source
    }
    $providerPath = [IO.Path]::GetFullPath($OpenCodePath)
    if (-not (Test-Path -LiteralPath $providerPath -PathType Leaf)) { throw "OpenCode executable not found: $providerPath" }
    $openCodeVersion = Invoke-OpenCodeForRoot $providerPath @('--version') $configRoot
    $effective = (Invoke-OpenCodeForRoot $providerPath @('debug','config') $configRoot) | ConvertFrom-Json -ErrorAction Stop
    $catalogModels = @((Invoke-OpenCodeForRoot $providerPath @('models') $configRoot) -split "`r?`n" | ForEach-Object {$_.Trim()} | Where-Object {$_})
    $configSource = 'opencode_debug_config'
}

$agentMap = Get-Prop $effective 'agent'
$fallbackModel = [string](Get-Prop $effective 'model')
$rows = @()
$agentProperties=if($null-ne$agentMap){@($agentMap.PSObject.Properties)}else{@()}
foreach ($property in $agentProperties) {
    $agent = $property.Value
    $mode = [string](Get-Prop $agent 'mode')
    if ([string]::IsNullOrWhiteSpace($mode)) { continue }
    $model = [string](Get-Prop $agent 'model')
    if ([string]::IsNullOrWhiteSpace($model)) { $model = $fallbackModel }
    $effort = [string](Get-Prop $agent 'reasoningEffort')
    if ([string]::IsNullOrWhiteSpace($effort)) { $effort = [string](Get-Prop (Get-Prop $agent 'options') 'reasoningEffort') }
    $permission = Get-Prop $agent 'permission'
    $editPermission = [string](Get-Prop $permission 'edit')
    $taskPermission = [string](Get-Prop $permission 'task')
    $effectiveEdit=$null;$effectiveTask=$null;$effectiveWrite=$null;$effectiveBash=$null
    if(-not $fixtureMode){
        $resolvedAgent=(Invoke-OpenCodeForRoot $providerPath @('debug','agent',$property.Name) $configRoot) | ConvertFrom-Json -ErrorAction Stop
        $effectiveEdit=[bool](Get-Prop (Get-Prop $resolvedAgent 'tools') 'edit')
        $effectiveTask=[bool](Get-Prop (Get-Prop $resolvedAgent 'tools') 'task')
        $effectiveWrite=[bool](Get-Prop (Get-Prop $resolvedAgent 'tools') 'write')
        $effectiveBash=[bool](Get-Prop (Get-Prop $resolvedAgent 'tools') 'bash')
    }else{
        if($editPermission){$effectiveEdit=$editPermission -ne 'deny'}
        if($taskPermission){$effectiveTask=$taskPermission -ne 'deny'}
        $writePermission=[string](Get-Prop $permission 'write');$bashPermission=[string](Get-Prop $permission 'bash')
        if($writePermission){$effectiveWrite=$writePermission -ne 'deny'}
        if($bashPermission){$effectiveBash=$bashPermission -ne 'deny'}
    }
    $rows += [ordered]@{name=$property.Name;mode=$mode;model=if($model){$model}else{$null};reasoning_effort=if($effort){$effort}else{$null};declared_edit=if($editPermission){$editPermission}else{$null};declared_task=if($taskPermission){$taskPermission}else{$null};effective_edit=$effectiveEdit;effective_write=$effectiveWrite;effective_bash=$effectiveBash;effective_task=$effectiveTask}
}

$primary = @($rows | Where-Object mode -eq 'primary')
$subagents = @($rows | Where-Object mode -eq 'subagent')
$distinctModels = @($rows | ForEach-Object model | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Sort-Object -Unique)
$readOnlySubagents = @($subagents | Where-Object { $_.effective_edit -eq $false -and $_.effective_write -eq $false -and $_.effective_bash -eq $false -and $_.effective_task -eq $false })
$defaultAgent=[string](Get-Prop $effective 'default_agent')
$defaultIsPrimary=@($primary|Where-Object name -eq $defaultAgent).Count -eq 1
$subagentDepthValue=Get-Prop $effective 'subagent_depth'
$subagentDepth=if($null-ne$subagentDepthValue){[int]$subagentDepthValue}else{0}
$defaultPrimary=@($primary|Where-Object name -eq $defaultAgent)
$primaryCanDelegate=$defaultPrimary.Count -eq 1 -and $defaultPrimary[0].effective_task -eq $true
$missingCatalogModels=@(if(-not $fixtureMode){$distinctModels|Where-Object {$catalogModels -notcontains $_}})
$heterogeneous = $primary.Count -gt 0 -and $subagents.Count -gt 0 -and $distinctModels.Count -gt 1 -and $defaultIsPrimary -and $subagentDepth -gt 0 -and $primaryCanDelegate -and $readOnlySubagents.Count -gt 0 -and $missingCatalogModels.Count -eq 0
$routingState = if ($heterogeneous) { 'heterogeneous_configured' } else { 'explicit_routing_required' }

$reviewerState = 'not_probed'
$reviewerModel=$null
$reviewerModelInCatalog=$null
if (-not $SkipReviewerProbe) {
    $reviewerConfig = Join-Path $skillRoot '1c-spec-review\reviewer\opencode-reviewer.json'
    if (-not (Test-Path -LiteralPath $reviewerConfig -PathType Leaf)) { throw "Packaged reviewer config missing: $reviewerConfig" }
    $reviewerConfigObject=Get-Content -Raw -LiteralPath $reviewerConfig|ConvertFrom-Json -ErrorAction Stop
    $reviewerModel=[string]$reviewerConfigObject.agent.'bsl-flow-spec-reviewer'.model
    $reviewerModelInCatalog=$catalogModels -contains $reviewerModel
    $oldConfig = $env:OPENCODE_CONFIG
    $oldProject = $env:OPENCODE_DISABLE_PROJECT_CONFIG
    $oldClaude = $env:OPENCODE_DISABLE_CLAUDE_CODE
    try {
        $env:OPENCODE_CONFIG = $reviewerConfig
        $env:OPENCODE_DISABLE_PROJECT_CONFIG = '1'
        $env:OPENCODE_DISABLE_CLAUDE_CODE = '1'
        foreach ($name in @('bsl-flow-spec-reviewer','bsl-flow-spec-reviewer-sealed')) {
            $resolved = (Invoke-CleanNative $providerPath @('debug','agent',$name)) | ConvertFrom-Json -ErrorAction Stop
            $tools = Get-Prop $resolved 'tools'
            foreach ($forbidden in @('edit','write','bash','task','webfetch','skill')) {
                if ((Get-Prop $tools $forbidden) -ne $false) { throw "Reviewer '$name' exposes forbidden tool: $forbidden" }
            }
        }
        $reviewerState = 'effective_permissions_verified'
    }
    finally {
        if ($null -eq $oldConfig) { Remove-Item Env:OPENCODE_CONFIG -ErrorAction SilentlyContinue } else { $env:OPENCODE_CONFIG=$oldConfig }
        if ($null -eq $oldProject) { Remove-Item Env:OPENCODE_DISABLE_PROJECT_CONFIG -ErrorAction SilentlyContinue } else { $env:OPENCODE_DISABLE_PROJECT_CONFIG=$oldProject }
        if ($null -eq $oldClaude) { Remove-Item Env:OPENCODE_DISABLE_CLAUDE_CODE -ErrorAction SilentlyContinue } else { $env:OPENCODE_DISABLE_CLAUDE_CODE=$oldClaude }
    }
}

[ordered]@{
    schema_version=1
    status='PASS'
    config_root=$configRoot
    config_source=$configSource
    opencode_version=if($openCodeVersion){$openCodeVersion}else{$null}
    skills=$skillRows
    routing=[ordered]@{
        state=$routingState
        configuration_only=$true
        default_agent=$defaultAgent
        default_agent_is_primary=$defaultIsPrimary
        subagent_depth=$subagentDepth
        primary_can_delegate=$primaryCanDelegate
        primary_agents=$primary.Count
        subagents=$subagents.Count
        distinct_models=$distinctModels
        missing_catalog_models=$missingCatalogModels
        read_only_subagents=$readOnlySubagents.Count
        agents=$rows
    }
    specification_reviewer=[ordered]@{state=$reviewerState;model=$reviewerModel;model_in_catalog=$reviewerModelInCatalog}
    limitations=@('Configuration does not prove that a live primary agent selected the correct delegate.','No paid model call was performed.','Observed model and effort for a concrete delegation require attributable run evidence.')
} | ConvertTo-Json -Depth 12
