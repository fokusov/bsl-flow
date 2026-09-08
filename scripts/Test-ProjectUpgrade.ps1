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
 $before=Get-Content -LiteralPath (Join-Path $root 'bsl-flow.yaml') -Raw
 $plan1=&$script -ProjectPath $root;$plan2=&$script -ProjectPath $root
 Assert-U (($plan1.actions|ConvertTo-Json -Compress)-eq($plan2.actions|ConvertTo-Json -Compress)) 'Upgrade plan is not deterministic.'
 Assert-U ((Get-Content -LiteralPath (Join-Path $root 'bsl-flow.yaml') -Raw)-eq$before) 'Plan mode changed project files.'
 $applied=&$script -ProjectPath $root -Apply
 $after=Get-Content -LiteralPath (Join-Path $root 'bsl-flow.yaml') -Raw
 Assert-U ($after.Contains('# user comment') -and $after.Contains('computer_use: never # user choice') -and $after.Contains('custom_extension:')) 'User YAML content was not preserved.'
 Assert-U (($after -match '(?m)^test_setup:') -and ($after -match '(?m)^review:') -and $after.Contains('readiness: not_configured')) 'Managed additions were not merged.'
 Assert-U ((Get-Content -LiteralPath (Join-Path $root '.bsl-flow\project.yaml') -Raw)-match 'framework_version: "0.6.1"') 'Sentinel version was not updated last.'
 Assert-U ((Get-Content -LiteralPath (Join-Path $root '.gitignore') -Raw).Contains('.bsl-flow/local/*')) 'Standalone upgrade did not migrate managed Git exclusions.'
 Assert-U ((Get-Content -LiteralPath (Join-Path $root '.gitignore') -Raw) -match '(?m)^secret-folder/\s*$') 'Standalone upgrade corrupted a user ignore rule after the managed block.'
 $again=&$script -ProjectPath $root -Apply;Assert-U ($again.status-eq'up_to_date') 'Upgrade is not idempotent.'
 $bad=Join-Path $root 'bad';New-Item -ItemType Directory -Path (Join-Path $bad '.bsl-flow') -Force|Out-Null
 "policy: disabled"|Set-Content -LiteralPath (Join-Path $bad 'bsl-flow.yaml') -Encoding UTF8
 "framework: bsl-flow`nframework_version: `"0.3.0`""|Set-Content -LiteralPath (Join-Path $bad '.bsl-flow\project.yaml') -Encoding UTF8
 $badBefore=Get-Content -LiteralPath (Join-Path $bad 'bsl-flow.yaml') -Raw;$blocked=$false
 try{&$script -ProjectPath $bad -Apply|Out-Null}catch{$blocked=$_.Exception.Message-match"Managed path 'policy' must be a mapping"}
 Assert-U $blocked 'Scalar managed-section conflict was not blocked.';Assert-U ((Get-Content -LiteralPath (Join-Path $bad 'bsl-flow.yaml') -Raw)-eq$badBefore) 'Blocked upgrade changed YAML.'
 Write-Host 'Project upgrade contracts passed.'
}finally{if(Test-Path $root){Remove-Item -LiteralPath $root -Recurse -Force}}
