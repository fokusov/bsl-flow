#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$root=if($PackageRoot){[IO.Path]::GetFullPath($PackageRoot)}else{[IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))}
. (Join-Path $root 'global/skills/1c-task/scripts/Task.Storage.ps1')
. (Join-Path $root 'global/skills/1c-task/scripts/Task.Contracts.ps1')
. (Join-Path $root 'global/skills/1c-task/scripts/Task.Runtime.ps1')
$script:checks=0
function Check([bool]$Value,[string]$Message){if(!$Value){throw $Message};$script:checks++}
function Reject([scriptblock]$Action,[string]$Pattern){try{&$Action;throw 'Expected rejection'}catch{if($_.Exception.Message -notmatch $Pattern){throw}};$script:checks++}
$tmp=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-runtime-test-'+[guid]::NewGuid().ToString('N'));[void][IO.Directory]::CreateDirectory($tmp)
try{
    $exe=Join-Path $tmp '1cv8.exe';[IO.File]::WriteAllBytes($exe,[byte[]](1,2,3));$hash=Get-BFFileHash $exe
    $target=Join-Path $tmp 'base';[void][IO.Directory]::CreateDirectory($target);[IO.File]::WriteAllBytes((Join-Path $target '1Cv8.1CD'),[byte[]](0))
    $criterion=[pscustomobject]@{id='native';kind='integration';observation='two tests';executable=$exe;arguments=@();report='.bsl-flow-worker/native.xml';expected_tests=@('M.A.A','M.B.B');protected_paths=@('src/ext/Ext/ObjectModule.bsl');target=$target;native_1c=[pscustomobject]@{source_root='src/ext';extension='Ext';module='M';platform_version='8.3.27.2074';executable_sha256=$hash;authorized_operations=@('inventory','load','update','test');authorization_reference='user-current-task'}}
    Assert-BFNativeCriterion $criterion;Check $true 'Valid fixture native criterion was rejected.'
    $bad=$criterion.PSObject.Copy();$bad.target='relative';Reject {Assert-BFNativeCriterion $bad} 'absolute FILE'
    $bad=$criterion.PSObject.Copy();$bad.arguments=@('/P','secret');Reject {Assert-BFNativeCriterion $bad} 'free arguments'
    Check ((Get-BFRuntimeTargetKey $target) -ceq (Get-BFRuntimeTargetKey ($target+'\'))) 'Target key is not canonical.'
    $junit=Join-Path $tmp 'junit.xml';[IO.File]::WriteAllText($junit,'<testsuite tests="2" failures="0" errors="0" skipped="0"><testcase classname="M" name="A"/><testcase classname="M" name="B"/></testsuite>');$now=[DateTime]::UtcNow;(Get-Item $junit).LastWriteTimeUtc=$now
    $parsed=Test-BFNativeJUnit $junit @('M.A','M.B') $now.AddSeconds(-1) $now.AddSeconds(1);Check ($parsed.tests.Count -eq 2) 'Exact class-qualified JUnit was rejected.'
    [IO.File]::WriteAllText($junit,'<testsuite tests="0" failures="0" errors="0" skipped="0"></testsuite>');(Get-Item $junit).LastWriteTimeUtc=$now;Reject {Test-BFNativeJUnit $junit @('M.A','M.B') $now.AddSeconds(-1) $now.AddSeconds(1)} 'missing|selection'
    $sourceRoot=Join-Path $tmp 'source';[void][IO.Directory]::CreateDirectory($sourceRoot)
    $metadata='<MetaDataObject><Configuration uuid="12345678-1234-4567-8123-123456789abc"><InternalInfo><ContainedObject><ClassId>00000000-0000-0000-0000-000000000000</ClassId><ObjectId>22345678-1234-4567-8123-123456789abc</ObjectId></ContainedObject></InternalInfo><Properties><Name>Ext</Name><Version>1</Version></Properties></Configuration></MetaDataObject>'
    $sourceXml=Join-Path $sourceRoot 'Configuration.xml'
    [IO.File]::WriteAllText($sourceXml,$metadata)
    Check ((Get-BFNativeSource $sourceRoot).extension -eq 'Ext') 'Owned source UUIDs are valid; ClassId is a reference.'
    [IO.File]::WriteAllText($sourceXml,$metadata.Replace('22345678-1234-4567-8123-123456789abc','00000000-0000-0000-0000-000000000017'))
    Reject {Get-BFNativeSource $sourceRoot} 'scaffold placeholders'
    [IO.File]::WriteAllText($sourceXml,$metadata.Replace('22345678-1234-4567-8123-123456789abc','12345678-1234-4567-8123-123456789abc'))
    Reject {Get-BFNativeSource $sourceRoot} 'unique'
    $source=[ordered]@{extension='Ext';uuid='11111111-1111-1111-1111-111111111111';version='1'};$comUuid='879d4a2e-ac60-11f1-9ef1-78465c3a941f';$before=[ordered]@{extensions=@([ordered]@{properties=[ordered]@{name='Other';version='1';active=$true;purpose='Customization';scope='InfoBase';uuid='22222222-2222-2222-2222-222222222222';hash_sum='AA'}},[ordered]@{properties=[ordered]@{name='Ext';version='0';active=$true;purpose='Customization';scope='InfoBase';uuid=$comUuid;hash_sum='00'}})};$after=[ordered]@{extensions=@($before.extensions[0],[ordered]@{properties=[ordered]@{name='Ext';version='1';active=$true;purpose='Customization';scope='InfoBase';uuid=$comUuid;hash_sum='BB'}})};Assert-BFNativeInventoryTransition $before $after $source;Check ($comUuid -cne $source.uuid) 'Regression fixture does not preserve distinct COM/source UUID domains.'
    $changed=[ordered]@{extensions=@([ordered]@{properties=[ordered]@{name='Other';version='2';active=$true;purpose='Customization';scope='InfoBase';uuid='22222222-2222-2222-2222-222222222222';hash_sum='CC'}},$after.extensions[1])};Reject {Assert-BFNativeInventoryTransition $before $changed $source} 'nonselected extension changed'
    $script:BFNativeCredential=[Management.Automation.PSCredential]::new('test',[Security.SecureString]::new());Check ((Get-BFNativeCredential) -is [Management.Automation.PSCredential]) 'Controller-memory credential was rejected.';$script:BFNativeCredential=$null;Reject {Get-BFNativeCredential} 'private controller input'
    $moduleText=[IO.File]::ReadAllText((Join-Path $root 'global/skills/1c-task/scripts/Task.Runtime.ps1'));Check ($moduleText -notmatch '(?i)bp1-native|C:\\BASES\\DEMO|Import-Clixml|credentials/') 'Production runtime module contains pilot history, target, or credential-file hardcodes.';Check ($moduleText -notmatch 'Write-BFJson[^\r\n]*(Credential|Password)|environment.*password') 'Production runtime module appears to serialize credentials.'
    Write-Host "Task runtime offline contracts passed: $script:checks checks; native starts=0; COM=0; DB writes=0."
}finally{
    $safeTemp=[IO.Path]::GetFullPath($tmp)
    $tempParent=[IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\')+'\'
    if(-not $safeTemp.StartsWith($tempParent,[StringComparison]::OrdinalIgnoreCase) -or (Split-Path $safeTemp -Leaf) -notlike 'bsl-flow-runtime-test-*'){throw 'Unsafe fixture cleanup target.'}
    if(Test-Path -LiteralPath $safeTemp){Remove-Item -LiteralPath $safeTemp -Recurse -Force}
}
