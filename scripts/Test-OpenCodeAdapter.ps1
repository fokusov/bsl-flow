[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
function Assert-O { param([bool]$Condition,[string]$Message) if(-not $Condition){throw "ASSERTION FAILED: $Message"} }
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$package = [IO.Path]::GetFullPath($PackageRoot)
$installer = Join-Path $package 'scripts\Install-BSLFlowForOpenCode.ps1'
$diagnostic = Join-Path $package 'scripts\Test-BSLFlowOpenCode.ps1'
$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-opencode-test-' + [Guid]::NewGuid().ToString('N'))
$configRoot = Join-Path $tempRoot 'opencode'
$sharedSkillsRoot = Join-Path $tempRoot '.agents\skills'
try {
    New-Item -ItemType Directory -Path (Join-Path $configRoot 'skills\unrelated') -Force | Out-Null
    @'
---
name: unrelated
description: Must survive adapter installation.
---
'@ | Set-Content -LiteralPath (Join-Path $configRoot 'skills\unrelated\SKILL.md') -Encoding utf8
    $agentsPath=Join-Path $configRoot 'AGENTS.md'
    $retiredName='1'+'c-'+'lite'
    $legacyAgents="# User OpenCode rules`r`n`r`nKeep this line.`r`n`r`n<!-- $retiredName bootstrap:start -->`r`nRetired bootstrap.`r`n<!-- $retiredName bootstrap:end -->`r`n`r`n<!-- $retiredName opencode:start -->`r`nRetired adapter.`r`n<!-- $retiredName opencode:end -->`r`n"
    [IO.File]::WriteAllText($agentsPath,$legacyAgents,[Text.UTF8Encoding]::new($true))
    '{"keep":true}' | Set-Content -LiteralPath (Join-Path $configRoot 'opencode.json') -Encoding utf8

    & $installer -OpenCodeConfigRoot $configRoot -SharedSkillsRoot $sharedSkillsRoot -SkipCliValidation -Apply | Out-Null
    Assert-O (Test-Path -LiteralPath (Join-Path $configRoot 'skills\unrelated\SKILL.md')) 'Unrelated skill was removed.'
    Assert-O ((Get-Content -Raw -LiteralPath (Join-Path $configRoot 'opencode.json')) -match '"keep":true') 'opencode.json was changed.'
    $agents = Get-Content -Raw -LiteralPath (Join-Path $configRoot 'AGENTS.md')
    Assert-O ($agents -match 'Keep this line\.') 'User AGENTS content was lost.'
    Assert-O (([regex]::Matches($agents,'<!-- bsl-flow bootstrap:start -->')).Count -eq 1) 'Bootstrap block count is not one.'
    Assert-O (([regex]::Matches($agents,'<!-- bsl-flow opencode:start -->')).Count -eq 1) 'OpenCode block count is not one.'
    Assert-O ($agents -notmatch "<!-- $([regex]::Escape($retiredName)) (bootstrap|opencode):start -->") 'Retired managed AGENTS block survived migration.'
    $agentsBytes=[IO.File]::ReadAllBytes($agentsPath)
    Assert-O ($agentsBytes[0]-eq0xEF -and $agentsBytes[1]-eq0xBB -and $agentsBytes[2]-eq0xBF) 'AGENTS UTF-8 BOM was not preserved.'
    Assert-O ($agents.Contains("Keep this line.`r`n`r`n<!-- bsl-flow bootstrap:start -->")) 'AGENTS CRLF or user suffix was not preserved.'
    foreach($name in @('1c-init-project','1c-spec','1c-spec-review','1c-implement','1c-verify','1c-debug','1c-task')) { Assert-O (Test-Path -LiteralPath (Join-Path $sharedSkillsRoot "$name\SKILL.md")) "Shared skill missing: $name"; Assert-O (-not(Test-Path -LiteralPath (Join-Path $configRoot "skills\$name"))) "Duplicate OpenCode-local skill exists: $name" }
    Assert-O (Test-Path -LiteralPath (Join-Path $configRoot '.bsl-flow\manifest.json')) 'Adapter manifest was not created.'
    $adapterManifest = Get-Content -Raw -LiteralPath (Join-Path $configRoot '.bsl-flow\manifest.json') | ConvertFrom-Json
    Assert-O (@($adapterManifest.skills).Count -eq 7) 'Adapter manifest does not inventory exactly seven managed skills.'
    Assert-O (@($adapterManifest.skills | Where-Object name -eq '1c-task').Count -eq 1) 'Adapter manifest omitted 1c-task.'
    $first = Get-Content -Raw -LiteralPath (Join-Path $configRoot 'AGENTS.md')
    $secondPlan=(& $installer -OpenCodeConfigRoot $configRoot -SharedSkillsRoot $sharedSkillsRoot -SkipCliValidation -Apply | ConvertFrom-Json)
    $second = Get-Content -Raw -LiteralPath (Join-Path $configRoot 'AGENTS.md')
    Assert-O ($first -eq $second) 'Managed AGENTS merge is not idempotent.'
    Assert-O ($secondPlan.status -eq 'up_to_date') 'Second adapter run was not up_to_date.'

    $fixture = Join-Path $tempRoot 'effective.json'
    @'
{
  "default_agent":"build",
  "subagent_depth":1,
  "model":"provider/primary",
  "agent":{
    "build":{"mode":"primary","model":"provider/primary","reasoningEffort":"medium","permission":{"task":"allow","edit":"allow","write":"allow","bash":"allow"}},
    "explore":{"mode":"subagent","model":"provider/fast","reasoningEffort":"low","permission":{"edit":"deny","write":"deny","bash":"deny","task":"deny"}},
    "review":{"mode":"subagent","model":"provider/reviewer","reasoningEffort":"high","permission":{"edit":"deny","write":"deny","bash":"deny","task":"deny"}}
  }
}
'@ | Set-Content -LiteralPath $fixture -Encoding utf8
    $result = (& $diagnostic -OpenCodeConfigRoot $configRoot -SharedSkillsRoot $sharedSkillsRoot -EffectiveConfigPath $fixture -SkipReviewerProbe) | ConvertFrom-Json
    Assert-O ($result.status -eq 'PASS') 'Fixture diagnostic did not pass.'
    Assert-O ($result.routing.state -eq 'heterogeneous_configured') 'Heterogeneous routing was not detected.'
    Assert-O ($result.routing.distinct_models.Count -eq 3) 'Distinct model count is wrong.'
    Assert-O ($result.routing.configuration_only -eq $true) 'Diagnostic overstated live orchestration evidence.'

    $single = Get-Content -Raw -LiteralPath $fixture | ConvertFrom-Json
    $single.agent.explore.model='provider/primary'; $single.agent.review.model='provider/primary'
    $single | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $fixture -Encoding utf8
    $singleResult = (& $diagnostic -OpenCodeConfigRoot $configRoot -SharedSkillsRoot $sharedSkillsRoot -EffectiveConfigPath $fixture -SkipReviewerProbe) | ConvertFrom-Json
    Assert-O ($singleResult.routing.state -eq 'explicit_routing_required') 'Single-model config was reported as heterogeneous.'

    '{"model":"provider/primary"}' | Set-Content -LiteralPath $fixture -Encoding utf8
    $cleanResult=(& $diagnostic -OpenCodeConfigRoot $configRoot -SharedSkillsRoot $sharedSkillsRoot -EffectiveConfigPath $fixture -SkipReviewerProbe)|ConvertFrom-Json
    Assert-O ($cleanResult.routing.state -eq 'explicit_routing_required') 'Config without a custom agent map did not produce explicit_routing_required.'

    @'
{
  "default_agent":"build",
  "subagent_depth":1,
  "model":"provider/primary",
  "agent":{
    "build":{"mode":"primary","model":"provider/primary","permission":{"task":"deny","edit":"allow","write":"allow","bash":"allow"}},
    "alternate":{"mode":"primary","model":"provider/alternate","permission":{"task":"allow","edit":"allow","write":"allow","bash":"allow"}},
    "unsafe-review":{"mode":"subagent","model":"provider/reviewer","permission":{"edit":"deny","write":"allow","bash":"allow","task":"deny"}}
  }
}
'@ | Set-Content -LiteralPath $fixture -Encoding utf8
    $deceptive=(& $diagnostic -OpenCodeConfigRoot $configRoot -SharedSkillsRoot $sharedSkillsRoot -EffectiveConfigPath $fixture -SkipReviewerProbe)|ConvertFrom-Json
    Assert-O ($deceptive.routing.state -eq 'explicit_routing_required') 'Non-default delegation or shell-capable reviewer produced a false heterogeneous PASS.'
    Assert-O ($deceptive.routing.read_only_subagents -eq 0) 'Shell/write-capable reviewer was counted as read-only.'

    Add-Content -LiteralPath (Join-Path $sharedSkillsRoot '1c-spec\SKILL.md') -Value "`nchanged outside manifest"
    $tamperBlocked=$false;$tamperText=''
    try{& $installer -OpenCodeConfigRoot $configRoot -SharedSkillsRoot $sharedSkillsRoot -SkipCliValidation | Out-Null}catch{$tamperText=$_.Exception.Message;$tamperBlocked=$tamperText -match 'manifest|differs|changed outside'}
    Assert-O $tamperBlocked "Modified managed skill was not blocked by chain-of-custody. Actual: $tamperText"

    $rollbackTop=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-opencode-test-'+[Guid]::NewGuid().ToString('N'))
    $rollbackRoot=Join-Path $rollbackTop 'opencode';New-Item -ItemType Directory -Path $rollbackRoot -Force|Out-Null
    $rollbackShared=Join-Path $rollbackTop '.agents\skills'
    $rollbackAgents=Join-Path $rollbackRoot 'AGENTS.md';[IO.File]::WriteAllText($rollbackAgents,"original`r`n",[Text.UTF8Encoding]::new($false))
    $failed=$false
    try{& $installer -OpenCodeConfigRoot $rollbackRoot -SharedSkillsRoot $rollbackShared -SkipCliValidation -SimulatePostApplyFailure -Apply|Out-Null}catch{$failed=$_.Exception.Message -match 'previous managed files were restored'}
    Assert-O $failed 'Simulated post-apply failure did not fail.'
    Assert-O ((Get-Content -Raw -LiteralPath $rollbackAgents)-eq"original`r`n") 'Rollback did not restore AGENTS.md.'
    Assert-O (-not(Test-Path -LiteralPath (Join-Path $rollbackShared '1c-spec'))) 'Rollback left an installed shared skill.'
    Assert-O (-not(Test-Path -LiteralPath (Join-Path $rollbackShared '1c-task'))) 'Rollback left an installed 1c-task skill.'
    if(Test-Path -LiteralPath $rollbackTop){Remove-Item -LiteralPath $rollbackTop -Recurse -Force}

    $conflictTop=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-opencode-test-'+[Guid]::NewGuid().ToString('N'));$conflictRoot=Join-Path $conflictTop 'opencode'
    $conflictShared=Join-Path $conflictTop '.agents\skills';New-Item -ItemType Directory -Path (Join-Path $conflictShared '1c-spec') -Force|Out-Null
    'foreign skill'|Set-Content -LiteralPath (Join-Path $conflictShared '1c-spec\SKILL.md') -Encoding utf8
    $foreignBlocked=$false;try{& $installer -OpenCodeConfigRoot $conflictRoot -SharedSkillsRoot $conflictShared -SkipCliValidation|Out-Null}catch{$foreignBlocked=$_.Exception.Message-match'no bsl-flow manifest'}
    Assert-O $foreignBlocked 'Foreign same-name skill was not blocked.'
    if(Test-Path -LiteralPath $conflictTop){Remove-Item -LiteralPath $conflictTop -Recurse -Force}

    $markerTop=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-opencode-test-'+[Guid]::NewGuid().ToString('N'));$markerRoot=Join-Path $markerTop 'opencode';New-Item -ItemType Directory -Path $markerRoot -Force|Out-Null
    '<!-- bsl-flow bootstrap:start -->'|Set-Content -LiteralPath (Join-Path $markerRoot 'AGENTS.md') -Encoding utf8
    $markerShared=Join-Path $markerTop '.agents\skills';$markerBlocked=$false;try{& $installer -OpenCodeConfigRoot $markerRoot -SharedSkillsRoot $markerShared -SkipCliValidation|Out-Null}catch{$markerBlocked=$_.Exception.Message-match'Malformed'}
    Assert-O $markerBlocked 'Unpaired managed marker was not blocked.'
    if(Test-Path -LiteralPath $markerTop){Remove-Item -LiteralPath $markerTop -Recurse -Force}

    $junctionTop=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-opencode-test-'+[Guid]::NewGuid().ToString('N'));$junctionRoot=Join-Path $junctionTop 'opencode';$outside=Join-Path $junctionTop 'outside'
    $junctionShared=Join-Path $junctionTop '.agents\skills';New-Item -ItemType Directory -Path $junctionRoot,$outside,(Split-Path -Parent $junctionShared) -Force|Out-Null
    New-Item -ItemType Junction -Path $junctionShared -Target $outside|Out-Null
    $junctionBlocked=$false;try{& $installer -OpenCodeConfigRoot $junctionRoot -SharedSkillsRoot $junctionShared -SkipCliValidation|Out-Null}catch{$junctionBlocked=$_.Exception.Message-match'reparse point'}
    Assert-O $junctionBlocked 'Junction-backed skills target was not blocked.'
    if(Test-Path -LiteralPath $junctionTop){Remove-Item -LiteralPath $junctionTop -Recurse -Force}

    $nestedTop=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-opencode-test-'+[Guid]::NewGuid().ToString('N'));$nestedRoot=Join-Path $nestedTop 'opencode';$nestedOutside=Join-Path $nestedTop 'outside'
    $nestedShared=Join-Path $nestedTop '.agents\skills';New-Item -ItemType Directory -Path (Join-Path $nestedShared '1c-spec'),$nestedOutside -Force|Out-Null
    'foreign skill'|Set-Content -LiteralPath (Join-Path $nestedShared '1c-spec\SKILL.md') -Encoding utf8
    New-Item -ItemType Junction -Path (Join-Path $nestedShared '1c-spec\linked') -Target $nestedOutside|Out-Null
    $nestedBlocked=$false;try{& $installer -OpenCodeConfigRoot $nestedRoot -SharedSkillsRoot $nestedShared -SkipCliValidation|Out-Null}catch{$nestedBlocked=$_.Exception.Message-match'nested reparse point'}
    Assert-O $nestedBlocked 'Nested junction inside a managed skill was not blocked.'
    if(Test-Path -LiteralPath $nestedTop){Remove-Item -LiteralPath $nestedTop -Recurse -Force}

    $wrongRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-opencode-test-'+[Guid]::NewGuid().ToString('N'))
    $wrongRootBlocked=$false;try{& $installer -OpenCodeConfigRoot $wrongRoot|Out-Null}catch{$wrongRootBlocked=$_.Exception.Message-match"named 'opencode'"}
    Assert-O $wrongRootBlocked 'Ambiguous custom config root was not rejected.'
    Write-Host 'OpenCode adapter contracts passed; no model or 1C process was started.'
}
finally {
    $resolved=[IO.Path]::GetFullPath($tempRoot); $temp=[IO.Path]::GetFullPath([IO.Path]::GetTempPath())
    if($resolved.StartsWith($temp,[StringComparison]::OrdinalIgnoreCase) -and (Split-Path -Leaf $resolved) -like 'bsl-flow-opencode-test-*'){Remove-Item -LiteralPath $resolved -Recurse -Force -ErrorAction SilentlyContinue}
}
