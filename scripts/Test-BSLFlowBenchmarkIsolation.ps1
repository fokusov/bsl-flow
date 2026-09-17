#Requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$CodexPath,
    [Parameter(Mandatory)][string]$OutputPath,
    [ValidateSet('unelevated','elevated')][string]$Backend='elevated'
)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$root=Split-Path $PSScriptRoot -Parent
. (Join-Path $root 'global/skills/1c-task/scripts/Task.Storage.ps1')
. (Join-Path $root 'global/skills/1c-task/scripts/Task.Contracts.ps1')
. (Join-Path $root 'global/skills/1c-task/scripts/Task.Process.ps1')
$CodexPath=Resolve-BFCodex $CodexPath
$output=Assert-BFSafePath $OutputPath
if(Test-Path -LiteralPath $output){throw 'OutputPath must be new; preserve previous probe evidence.'}
[void][IO.Directory]::CreateDirectory($output)
$worker=Join-Path $output 'worker'
[void][IO.Directory]::CreateDirectory($worker)
$privateRoot=Join-Path $output 'private'
[void][IO.Directory]::CreateDirectory($privateRoot)
$private=Join-Path $privateRoot 'reference.txt'
$controller=Join-Path $privateRoot 'controller.txt'
[IO.File]::WriteAllText($private,'synthetic-reference')
[IO.File]::WriteAllText($controller,'synthetic-controller')
$probe=Join-Path $worker 'probe.ps1'
$q={param($p) "'"+$p.Replace("'","''")+"'"}
$body=@'
param([switch]$Child)
$ErrorActionPreference='Stop'
$read='allowed';$controllerRead='allowed';$write='allowed';$source='denied'
try {[void][IO.File]::ReadAllText(REFERENCE_PATH)} catch [UnauthorizedAccessException] {$read='denied'}
try {[void][IO.File]::ReadAllText(CONTROLLER_PATH)} catch [UnauthorizedAccessException] {$controllerRead='denied'}
try {[IO.File]::WriteAllText(CONTROLLER_PATH,'tampered')} catch [UnauthorizedAccessException] {$write='denied'}
try {[IO.File]::WriteAllText(SOURCE_PATH,'probe');$source='allowed'} catch [UnauthorizedAccessException] {}
Write-Output ($read+':'+$controllerRead+':'+$write+':'+$source)
if(-not $Child){& (Join-Path $PSHOME 'pwsh.exe') -NoLogo -NoProfile -File $PSCommandPath -Child; if($LASTEXITCODE -ne 0){throw 'Child probe failed.'}}
'@
$body=$body.Replace('REFERENCE_PATH',(& $q $private)).Replace('CONTROLLER_PATH',(& $q $controller)).Replace('SOURCE_PATH',(& $q (Join-Path $worker 'source.txt')))
[IO.File]::WriteAllText($probe,$body)
$toml={param($p) ConvertTo-Json -InputObject $p.Replace('\','/') -Compress}
$profile='permissions.bsl_bench_probe={filesystem={":root"="read",'+(& $toml $worker)+'="write",'+(& $toml $privateRoot)+'="none"},network={enabled=true}}'
$argv=@('sandbox','-P','bsl_bench_probe','-c',$profile,'-c',('windows.sandbox="'+$Backend+'"'),'-C',$worker,(Join-Path $PSHOME 'pwsh.exe'),'-NoLogo','-NoProfile','-File',$probe)
$status='BLOCKED';$reason='';$process=$null
try {
    $process=Invoke-BFProcess $CodexPath $argv $worker '' (Join-Path $output 'process') 45
    $observed=[IO.File]::ReadAllText($process.stdout).Trim()
    $expected="denied:denied:denied:allowed`ndenied:denied:denied:allowed"
    if($process.exit_code -eq 0 -and -not $process.stop_reason -and ($observed -replace "`r`n","`n") -ceq $expected -and [IO.File]::ReadAllText($controller) -ceq 'synthetic-controller'){
        $status='PASS';$reason='Parent and child: synthetic reference/controller reads denied, controller write denied, source write allowed.'
    } else {$reason='Required filesystem observations not demonstrated. Inspect preserved process outputs; no model or database was invoked.'}
} catch {$reason=$_.Exception.Message}
$result=[ordered]@{schema_version=1;status=$status;backend=$Backend;codex_sha256=Get-BFFileHash $CodexPath;profile_sha256=Get-BFHash $profile;reason=$reason;provider_network='not_tested';tool_process_isolation=if($status -eq 'PASS'){'synthetic_pwsh_child_verified'}else{'not_verified'};production_capability=$false}
Write-BFJson (Join-Path $output 'result.json') $result
$result|ConvertTo-Json -Compress
if($status -ne 'PASS'){exit 11}
