#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }

$generator = Join-Path $PackageRoot 'scripts\New-BSLFlowUserConfig.ps1'
$skill = Join-Path $PackageRoot 'global\skills\1c-spec-review'
. (Join-Path $skill 'scripts\Review.Common.ps1')
. (Join-Path $skill 'scripts\Council.Common.ps1')
. (Join-Path $skill 'scripts\Council.Engine.ps1')
. (Join-Path $skill 'scripts\Council.Profile.ps1')

$passed = 0
function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "FAIL: $Message" }
    $script:passed++
    "PASS $Message"
}

$savedEnvOverride = [System.Environment]::GetEnvironmentVariable('BSL_FLOW_USER_CONFIG')
$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ('user-config-gen-' + [guid]::NewGuid().ToString('N'))
try {
    [System.Environment]::SetEnvironmentVariable('BSL_FLOW_USER_CONFIG', $null)
    New-Item -ItemType Directory -Path $tempRoot -Force | Out-Null

    foreach ($template in @('openai-deepseek', 'anthropic-deepseek', 'anthropic-only')) {
        $outPath = Join-Path $tempRoot ($template + '.yaml')
        $result = & $generator -Template $template -Path $outPath
        Assert-True ([bool]$result.written -and $result.path -ceq $outPath) "generator writes $template to the requested path"
        Assert-True (Test-Path -LiteralPath $outPath -PathType Leaf) "$template config file was created"
        $text = [System.IO.File]::ReadAllText($outPath, [System.Text.UTF8Encoding]::new($false))
        Assert-True ($text.Contains('REPLACE-ME')) "$template config leaves placeholder model ids for the operator to edit"
        Assert-True ($text -notmatch '(?im)^\s*Authorization\s*:') "$template config never emits a literal Authorization header"

        # Shape: must pass the profile allowlist unchanged (only allowlisted
        # keys, no literal token).
        Assert-BSLFlowUserProfileConfig -Text $text -Path $outPath
        $script:passed++
        "PASS $template config passes the user profile allowlist"

        # End-to-end: a project that only references the symbolic profiles
        # (no llm section of its own) must resolve every enabled role once
        # this profile is applied — unless the provider's protocol
        # (anthropic_messages) is not yet accepted by the transport, which is
        # landing in a parallel change; that specific, named gap is tolerated
        # so this suite does not hard-fail on a sibling change's timing.
        $project = Join-Path $tempRoot ($template + '-project')
        $change = Join-Path $project 'openspec\changes\demo'
        New-Item -ItemType Directory -Path $change -Force | Out-Null
        Copy-Item -LiteralPath (Join-Path $PackageRoot 'openspec\changes\api-specification-council\original-task.md') -Destination (Join-Path $change 'original-task.md')
        Copy-Item -LiteralPath (Join-Path $PackageRoot 'openspec\changes\api-specification-council\spec.md') -Destination (Join-Path $change 'spec.md')
        $projectText = @(
            'review:',
            '  council:',
            '    enabled: true',
            '    roles:',
            '      intent_critic:',
            '        enabled: true',
            '        required: true',
            '        model: review-fast',
            '      architecture_critic:',
            '        enabled: true',
            '        required: true',
            '        model: review-fast',
            '      executability_critic:',
            '        enabled: true',
            '        required: true',
            '        model: review-fast',
            '      chair:',
            '        enabled: true',
            '        required: true',
            '        model: review-chair'
        ) -join "`n"
        $projectText += "`n"
        [System.IO.File]::WriteAllText((Join-Path $project 'bsl-flow.yaml'), $projectText, [System.Text.UTF8Encoding]::new($false))

        $effective = $null
        $protocolGapMessage = $null
        try { $effective = Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $project -UserProfilePath $outPath }
        catch { $protocolGapMessage = [string]$_.Exception.Message }

        if ($null -ne $effective) {
            Assert-True ([string]$effective.policy.roles.intent_critic.model -ceq 'review-fast') "$template profile resolves critics to review-fast"
            Assert-True ([string]$effective.policy.roles.chair.model -ceq 'review-chair') "$template profile resolves the chair to review-chair"
            $criticModelId = [string]$effective.policy.models.'review-fast'.model
            $chairModelId = [string]$effective.policy.models.'review-chair'.model
            Assert-True ($criticModelId -cne $chairModelId) "$template keeps critics on a model distinct from the chair"
        }
        else {
            Assert-True ($protocolGapMessage -match '(?i)protocol' -and $protocolGapMessage -match '(?i)anthropic') "$template only fails on the pending anthropic_messages transport protocol, not on profile shape: $protocolGapMessage"
        }
    }

    # -Force / overwrite protection.
    $forcePath = Join-Path $tempRoot 'force-check.yaml'
    & $generator -Template 'openai-deepseek' -Path $forcePath | Out-Null
    $blocked = $false
    try { & $generator -Template 'anthropic-only' -Path $forcePath | Out-Null }
    catch { $blocked = $_.Exception.Message -match 'BF_BLOCKED' -and $_.Exception.Message.Contains($forcePath) }
    Assert-True $blocked 'regenerating an existing profile without -Force is refused with BF_BLOCKED naming the path'
    $beforeForce = [System.IO.File]::ReadAllText($forcePath)
    Assert-True ($beforeForce.Contains('openai-deepseek')) 'unforced regeneration left the original template in place'
    & $generator -Template 'anthropic-only' -Path $forcePath -Force | Out-Null
    $afterForce = [System.IO.File]::ReadAllText($forcePath)
    Assert-True ($afterForce.Contains('anthropic-only') -and -not $afterForce.Contains('openai-deepseek')) '-Force overwrites an existing profile config'

    # -WhatIf never writes.
    $whatIfPath = Join-Path $tempRoot 'whatif-check.yaml'
    & $generator -Template 'openai-deepseek' -Path $whatIfPath -WhatIf | Out-Null
    Assert-True (-not (Test-Path -LiteralPath $whatIfPath -PathType Leaf)) '-WhatIf does not create the profile config file'

    # A directory at the target path is refused, not overwritten.
    $dirPath = Join-Path $tempRoot 'dir-check.yaml'
    New-Item -ItemType Directory -Path $dirPath -Force | Out-Null
    $dirBlocked = $false
    try { & $generator -Template 'openai-deepseek' -Path $dirPath | Out-Null }
    catch { $dirBlocked = $true }
    Assert-True $dirBlocked 'a directory at the target path is refused rather than overwritten'

    # BSL_FLOW_USER_CONFIG selects the default path when -Path is omitted.
    $envPath = Join-Path $tempRoot 'env-selected.yaml'
    [System.Environment]::SetEnvironmentVariable('BSL_FLOW_USER_CONFIG', $envPath)
    $envResult = & $generator -Template 'openai-deepseek'
    Assert-True ($envResult.path -ceq [System.IO.Path]::GetFullPath($envPath) -and (Test-Path -LiteralPath $envPath -PathType Leaf)) 'BSL_FLOW_USER_CONFIG selects the default target path when -Path is omitted'

    "ALL_USER_CONFIG_GENERATOR_PASSED=$passed"
}
finally {
    if ($null -ne $savedEnvOverride) { [System.Environment]::SetEnvironmentVariable('BSL_FLOW_USER_CONFIG', $savedEnvOverride) }
    else { [System.Environment]::SetEnvironmentVariable('BSL_FLOW_USER_CONFIG', $null) }
    if (Test-Path -LiteralPath $tempRoot -PathType Container) { Remove-Item -LiteralPath $tempRoot -Recurse -Force }
}
