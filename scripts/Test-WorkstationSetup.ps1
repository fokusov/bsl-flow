#Requires -Version 7.0
[CmdletBinding()]param([string]$PackageRoot)
Set-StrictMode -Version Latest;$ErrorActionPreference='Stop'
function Assert-W([bool]$Condition,[string]$Message){if(-not$Condition){throw $Message}}
if(-not$PackageRoot){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$skill=Join-Path $PackageRoot 'global\skills\1c-init-project';$enable=Join-Path $skill 'scripts\Enable-BSLFlowWorkstationProfile.ps1';$setup=Join-Path $skill 'scripts\Initialize-1CTestEnvironment.ps1'
$root=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-workstation-'+[guid]::NewGuid().ToString('N'))
try{
 $project1=Join-Path $root 'p1';$project2=Join-Path $root 'p2';$db1=Join-Path $root 'db1';$db2=Join-Path $root 'db2';$bin=Join-Path $root 'bin';$yax=Join-Path $root 'yax';$va=Join-Path $root 'va'
 foreach($d in @($project1,$project2,$db1,$db2,$bin,$yax,$va)){New-Item -ItemType Directory -Path $d -Force|Out-Null}
 foreach($d in @($db1,$db2)){Set-Content -LiteralPath (Join-Path $d '1Cv8.1CD') -Value 'fixture' -Encoding UTF8}
 Set-Content -LiteralPath (Join-Path $yax 'YAxUnit-1.cfe') -Value 'fixture' -Encoding UTF8;Set-Content -LiteralPath (Join-Path $va 'vanessa-automation.epf') -Value 'fixture' -Encoding UTF8
 $profile=Join-Path $root 'workstation.json';&$enable -DevelopmentDatabasePath @($db1,$db2) -PlatformBin $bin -YaxunitDirectory $yax -VanessaDirectory $va -ProfilePath $profile|Out-Null
 $profileData=Get-Content -LiteralPath $profile -Raw|ConvertFrom-Json
 Assert-W ([string]$profileData.catalogs.yaxunit -eq ([IO.Path]::GetFullPath($yax)).TrimEnd('\','/')) 'Workstation profile lost the explicit YAxUnit catalog.'
 Assert-W ([string]$profileData.catalogs.vanessa -eq ([IO.Path]::GetFullPath($va)).TrimEnd('\','/')) 'Workstation profile lost the explicit Vanessa catalog.'
 $r1=&$setup -ProjectPath $project1 -DevelopmentDatabasePath $db1 -ProfilePath $profile;$r2=&$setup -ProjectPath $project2 -DevelopmentDatabasePath $db2 -ProfilePath $profile
 Assert-W (($r1.status -eq 'BLOCKED') -and (-not $r1.runtime_mutation_performed)) 'Offline setup invented runtime readiness.'
 Assert-W ($r1.development_db -eq $r1.test_db) 'Development DB was not bound as test target.'
 Assert-W (($r1.providers.yaxunit.state -eq 'files_found') -and (-not $r1.providers.yaxunit.enabled)) 'Files were mistaken for a ready provider.'
 Assert-W ($r1.providers.vanessa.installed_in_database -eq 'not_applicable_external_runner') 'Vanessa EPF was mistaken for an installed database extension.'
 Assert-W ($r1.testclient_port -ne $r2.testclient_port) 'Two projects received the same stable port.'
 $r1Again=&$setup -ProjectPath $project1 -DevelopmentDatabasePath $db1 -ProfilePath $profile
 Assert-W ($r1Again.testclient_port -eq $r1.testclient_port) 'Project did not retain its reserved TestClient port.'
 $runtimeBefore=(Get-FileHash -LiteralPath $r1.runtime_config -Algorithm SHA256).Hash;$listener=[Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,[int]$r1.testclient_port);$listener.Start();$portBlocked=$false
 try{try{&$setup -ProjectPath $project1 -DevelopmentDatabasePath $db1 -ProfilePath $profile|Out-Null}catch{$portBlocked=$_.Exception.Message-match'refusing to rewrite'}}finally{$listener.Stop()}
 Assert-W $portBlocked 'Occupied reserved port did not fail closed.';Assert-W ((Get-FileHash -LiteralPath $r1.runtime_config -Algorithm SHA256).Hash -eq $runtimeBefore) 'Failed port check rewrote runtime state.'
 $registry=Get-Content -LiteralPath (Join-Path $root 'testclient-ports.json') -Raw|ConvertFrom-Json
 Assert-W (@($registry.entries|Select-Object -ExpandProperty port -Unique).Count -eq @($registry.entries).Count) 'Port registry contains a cross-project collision.'
 $local=Get-Content -LiteralPath $r1.runtime_config -Raw;Assert-W ($local -notmatch 'password\s*:\s*(?!not_stored)') 'Password leaked to local config.'
 $other=Join-Path $root 'other';New-Item -ItemType Directory -Path $other|Out-Null;Set-Content -LiteralPath (Join-Path $other '1Cv8.1CD') -Value 'fixture' -Encoding UTF8
 $blocked=$false;try{&$setup -ProjectPath $project1 -DevelopmentDatabasePath $other -ProfilePath $profile|Out-Null}catch{$blocked=$_.Exception.Message-match'not trusted'}
 Assert-W $blocked 'Untrusted database target was accepted.'
 $noVaRoot=Join-Path $root 'no-va';New-Item -ItemType Directory -Path $noVaRoot|Out-Null
 $noVaProfile=Join-Path $root 'workstation-no-va.json';&$enable -DevelopmentDatabasePath $db1 -PlatformBin $bin -YaxunitDirectory $yax -VanessaDirectory $noVaRoot -ProfilePath $noVaProfile|Out-Null
 $noVa=&$setup -ProjectPath $project1 -DevelopmentDatabasePath $db1 -ProfilePath $noVaProfile
 Assert-W ($noVa.providers.vanessa.state -eq 'not_configured') 'Missing Vanessa did not produce a durable not_configured state.'
 Assert-W ($noVa.providers.vanessa.blocked_reason -eq 'local_catalog_or_required_artifact_missing') 'Missing Vanessa reported an unrelated database-inventory blocker.'
 Assert-W ($noVa.providers.vanessa.next_action -match 'does not silently download') 'Missing Vanessa lacks an actionable no-download remediation.'
 $noVaReport=Get-Content -LiteralPath $noVa.report -Raw|ConvertFrom-Json
 Assert-W (-not $noVaReport.dependency_policy.automatic_download_performed -and -not $noVaReport.dependency_policy.automatic_install_performed) 'Offline setup overstated dependency installation.'
 Set-Content -LiteralPath (Join-Path $va 'vanessa-automation-second.epf') -Value 'second fixture' -Encoding UTF8
 $ambiguousVa=&$setup -ProjectPath $project1 -DevelopmentDatabasePath $db1 -ProfilePath $profile
 $ambiguousRuntime=Get-Content -LiteralPath $ambiguousVa.runtime_config -Raw|ConvertFrom-Json
 Assert-W ($ambiguousVa.providers.vanessa.state -eq 'blocked') 'Multiple Vanessa candidates were not blocked.'
 Assert-W ($null -eq $ambiguousRuntime.vanessa_epf) 'Blocked Vanessa selection leaked a runnable candidate path.'
 Remove-Item -LiteralPath (Join-Path $va 'vanessa-automation-second.epf') -Force
 Clear-Content -LiteralPath (Join-Path $va 'vanessa-automation.epf')
 $emptyVa=&$setup -ProjectPath $project1 -DevelopmentDatabasePath $db1 -ProfilePath $profile
 $emptyRuntime=Get-Content -LiteralPath $emptyVa.runtime_config -Raw|ConvertFrom-Json
 Assert-W ($emptyVa.providers.vanessa.state -eq 'blocked') 'Empty Vanessa artifact was not blocked.'
 Assert-W ($null -eq $emptyRuntime.vanessa_epf) 'Empty Vanessa artifact leaked a runnable path.'
 Write-Host 'Workstation setup contracts passed; no 1C process was started.'
}finally{if(Test-Path $root){Remove-Item -LiteralPath $root -Recurse -Force}}
