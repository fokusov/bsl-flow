#Requires -Version 7.0
[CmdletBinding()]param([string]$PackageRoot)
Set-StrictMode -Version Latest;$ErrorActionPreference='Stop'
function Assert-U([bool]$Condition,[string]$Message){if(-not$Condition){throw $Message}}
if(-not$PackageRoot){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$script=Join-Path $PackageRoot 'global\skills\1c-init-project\scripts\Update-BSLFlowProject.ps1'
$root=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-upgrade-'+[guid]::NewGuid().ToString('N'))
try{
 New-Item -ItemType Directory -Path (Join-Path $root '.bsl-flow') -Force|Out-Null
 @'
# user comment
version: 1
source:
  paths:
    - src
policy:
  computer_use: never # user choice
custom_extension:
  answer: 42
review:
  reviewer:
    model: local/established-model # keep project routing
'@|Set-Content -LiteralPath (Join-Path $root 'bsl-flow.yaml') -Encoding UTF8
 @'
format_version: 1
framework: bsl-flow
framework_version: "0.3.0"
initialized_at: "2026-01-01T00:00:00Z"
'@|Set-Content -LiteralPath (Join-Path $root '.bsl-flow\project.yaml') -Encoding UTF8
 @'
# user ignore
# bsl-flow managed:start
.bsl-flow/reports/*
# bsl-flow managed:end
secret-folder/
'@|Set-Content -LiteralPath (Join-Path $root '.gitignore') -Encoding UTF8
 @'
# Local project instructions

- Keep this local build command and local model-routing policy.
'@|Set-Content -LiteralPath (Join-Path $root 'AGENTS.md') -Encoding UTF8
 $before=Get-Content -LiteralPath (Join-Path $root 'bsl-flow.yaml') -Raw
 $agentsBefore=Get-Content -LiteralPath (Join-Path $root 'AGENTS.md') -Raw
 $ignoreBefore=Get-Content -LiteralPath (Join-Path $root '.gitignore') -Raw
 $sentinelBefore=Get-Content -LiteralPath (Join-Path $root '.bsl-flow\project.yaml') -Raw
 $plan1=&$script -ProjectPath $root;$plan2=&$script -ProjectPath $root
 Assert-U (($plan1.actions|ConvertTo-Json -Compress)-eq($plan2.actions|ConvertTo-Json -Compress)) 'Upgrade plan is not deterministic.'
 Assert-U ((Get-Content -LiteralPath (Join-Path $root 'bsl-flow.yaml') -Raw)-eq$before) 'Plan mode changed project files.'
 Assert-U ((Get-Content -LiteralPath (Join-Path $root 'AGENTS.md') -Raw)-eq$agentsBefore) 'Plan mode changed AGENTS.md.'
 Assert-U ((Get-Content -LiteralPath (Join-Path $root '.gitignore') -Raw)-eq$ignoreBefore) 'Plan mode changed .gitignore.'
 Assert-U ((Get-Content -LiteralPath (Join-Path $root '.bsl-flow\project.yaml') -Raw)-eq$sentinelBefore) 'Plan mode changed the sentinel.'
 Assert-U (@($plan1.actions|Where-Object{$_.action-eq'update_managed_agents'}).Count-eq1) 'Existing AGENTS.md managed-block update was not planned.'
 $applied=&$script -ProjectPath $root -Apply
 $after=Get-Content -LiteralPath (Join-Path $root 'bsl-flow.yaml') -Raw
 Assert-U ($after.Contains('# user comment') -and $after.Contains('computer_use: never # user choice') -and $after.Contains('custom_extension:')) 'User YAML content was not preserved.'
 Assert-U (($after -match '(?m)^test_setup:') -and ($after -match '(?m)^review:') -and $after.Contains('readiness: not_configured')) 'Managed additions were not merged.'
 Assert-U ($after.Contains('mode: assisted') -and $after.Contains('entrypoint: 1c-task') -and $after.Contains('readiness: not_verified_by_bootstrap') -and $after.Contains('- codex') -and $after.Contains('registered_tasks_through_confirmed_adapter_only') -and $after.Contains('requires_explicit_authorized_route_and_target')) 'Managed workflow defaults and boundaries were not added.'
 Assert-U ($after.Contains('model: local/established-model # keep project routing')) 'Established project model routing was replaced.'
 $agentsAfter=Get-Content -LiteralPath (Join-Path $root 'AGENTS.md') -Raw
 Assert-U ($agentsAfter.Contains('# Local project instructions') -and $agentsAfter.Contains('local model-routing policy')) 'Local AGENTS.md instructions were overwritten.'
 Assert-U (([regex]::Matches($agentsAfter,'(?m)^<!-- bsl-flow managed:start -->[ \t]*\r?$').Count-eq1) -and $agentsAfter.Contains('`1c-task`') -and $agentsAfter.Contains('does not prove host isolation')) 'Managed AGENTS.md block was not added exactly once with readiness limits.'
 Assert-U ((Get-Content -LiteralPath (Join-Path $root '.bsl-flow\project.yaml') -Raw)-match 'framework_version: "0.8.0-dev.2"') 'Sentinel version was not updated last.'
 $ignoreAfter=Get-Content -LiteralPath (Join-Path $root '.gitignore') -Raw
 Assert-U ($ignoreAfter.Contains('.bsl-flow/local/*') -and $ignoreAfter.Contains('.bsl-flow/tasks/') -and $ignoreAfter.Contains('.bsl-flow/worktrees/')) 'Standalone upgrade did not migrate managed Git exclusions.'
 Assert-U ((Get-Content -LiteralPath (Join-Path $root '.gitignore') -Raw) -match '(?m)^secret-folder/\s*$') 'Standalone upgrade corrupted a user ignore rule after the managed block.'
 $again=&$script -ProjectPath $root -Apply;Assert-U ($again.status-eq'up_to_date') 'Upgrade is not idempotent.'
 $oldManagedBlock="<!-- bsl-flow managed:start -->`n## Old managed text`n<!-- bsl-flow managed:end -->"
 $agentsWithOldBlock=[regex]::Replace($agentsAfter,'(?ms)^<!-- bsl-flow managed:start -->[ \t]*\r?\n.*?^<!-- bsl-flow managed:end -->[ \t]*\r?$',$oldManagedBlock,1)
 [IO.File]::WriteAllText((Join-Path $root 'AGENTS.md'),$agentsWithOldBlock,[Text.UTF8Encoding]::new($false))
 $agentsRefreshPlan=&$script -ProjectPath $root
 Assert-U (@($agentsRefreshPlan.actions|Where-Object{$_.action-eq'update_managed_agents'}).Count-eq1) 'Stale managed AGENTS.md block was not planned for refresh.'
 [void](&$script -ProjectPath $root -Apply)
 $agentsRefreshed=Get-Content -LiteralPath (Join-Path $root 'AGENTS.md') -Raw
 Assert-U ($agentsRefreshed.Contains('# Local project instructions') -and -not$agentsRefreshed.Contains('Old managed text') -and $agentsRefreshed.Contains('does not prove host isolation')) 'Managed AGENTS.md refresh did not preserve local instructions.'

 $partial=Join-Path $root 'partial';New-Item -ItemType Directory -Path (Join-Path $partial '.bsl-flow') -Force|Out-Null
 "version: 1"|Set-Content -LiteralPath (Join-Path $partial 'bsl-flow.yaml') -Encoding UTF8
 "framework: bsl-flow`nframework_version: `"0.5.0`""|Set-Content -LiteralPath (Join-Path $partial '.bsl-flow\project.yaml') -Encoding UTF8
 $partialPlan1=&$script -ProjectPath $partial;$partialPlan2=&$script -ProjectPath $partial
 Assert-U (($partialPlan1.actions|ConvertTo-Json -Compress)-eq($partialPlan2.actions|ConvertTo-Json -Compress)) 'Partial-bootstrap recovery plan is not deterministic.'
 [void](&$script -ProjectPath $partial -Apply)
 Assert-U ((Test-Path -LiteralPath (Join-Path $partial 'AGENTS.md') -PathType Leaf) -and (Get-Content -LiteralPath (Join-Path $partial '.gitignore') -Raw).Contains('.bsl-flow/tasks/')) 'Partial-bootstrap recovery did not create missing managed files.'
 Assert-U ((&$script -ProjectPath $partial).status-eq'up_to_date') 'Recovered partial bootstrap is not idempotent.'
 $bad=Join-Path $root 'bad';New-Item -ItemType Directory -Path (Join-Path $bad '.bsl-flow') -Force|Out-Null
 "policy: disabled"|Set-Content -LiteralPath (Join-Path $bad 'bsl-flow.yaml') -Encoding UTF8
 "framework: bsl-flow`nframework_version: `"0.3.0`""|Set-Content -LiteralPath (Join-Path $bad '.bsl-flow\project.yaml') -Encoding UTF8
 $badBefore=Get-Content -LiteralPath (Join-Path $bad 'bsl-flow.yaml') -Raw;$blocked=$false
 try{&$script -ProjectPath $bad -Apply|Out-Null}catch{$blocked=$_.Exception.Message-match"Managed path 'policy' must be a mapping"}
 Assert-U $blocked 'Scalar managed-section conflict was not blocked.';Assert-U ((Get-Content -LiteralPath (Join-Path $bad 'bsl-flow.yaml') -Raw)-eq$badBefore) 'Blocked upgrade changed YAML.'

 $badAgents=Join-Path $root 'bad-agents';New-Item -ItemType Directory -Path (Join-Path $badAgents '.bsl-flow') -Force|Out-Null
 "version: 2"|Set-Content -LiteralPath (Join-Path $badAgents 'bsl-flow.yaml') -Encoding UTF8
 "framework: bsl-flow`nframework_version: `"0.6.1`""|Set-Content -LiteralPath (Join-Path $badAgents '.bsl-flow\project.yaml') -Encoding UTF8
 "# local`n<!-- bsl-flow managed:start -->`nunfinished"|Set-Content -LiteralPath (Join-Path $badAgents 'AGENTS.md') -Encoding UTF8
 $badAgentsBefore=Get-Content -LiteralPath (Join-Path $badAgents 'AGENTS.md') -Raw;$agentsBlocked=$false
 try{&$script -ProjectPath $badAgents -Apply|Out-Null}catch{$agentsBlocked=$_.Exception.Message-match'incomplete or duplicate'}
 Assert-U $agentsBlocked 'Malformed managed AGENTS.md block was not blocked.'
 Assert-U ((Get-Content -LiteralPath (Join-Path $badAgents 'AGENTS.md') -Raw)-eq$badAgentsBefore) 'Blocked AGENTS.md upgrade changed local instructions.'

 $newer=Join-Path $root 'newer';New-Item -ItemType Directory -Path (Join-Path $newer '.bsl-flow') -Force|Out-Null
 "version: 2"|Set-Content -LiteralPath (Join-Path $newer 'bsl-flow.yaml') -Encoding UTF8
 "framework: bsl-flow`nframework_version: `"0.8.0`""|Set-Content -LiteralPath (Join-Path $newer '.bsl-flow\project.yaml') -Encoding UTF8
 $newerBlocked=$false
 try{&$script -ProjectPath $newer|Out-Null}catch{$newerBlocked=$_.Exception.Message-match'newer than installed framework'}
 Assert-U $newerBlocked 'Stable 0.8.0 project was not recognized as newer than 0.8.0-dev.2.'
 Write-Host 'Project upgrade contracts passed.'
}finally{if(Test-Path $root){Remove-Item -LiteralPath $root -Recurse -Force}}
