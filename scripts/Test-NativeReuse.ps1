#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$core=Join-Path $PackageRoot 'global\skills\1c-task\scripts'
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1')){. (Join-Path $core $name)}

$script:checks=0
$script:failures=[Collections.Generic.List[string]]::new()
$script:root=$null
$script:source=[ordered]@{extension='Ext';version='1';uuid='11111111-1111-1111-1111-111111111111';sha256=('a'*64)}
$script:manifest=[ordered]@{sha256=('b'*64)}
$script:dependencies=[ordered]@{executable_sha256=('c'*64);platform='fixture'}

function Assert-R([bool]$Value,[string]$Message){if(-not $Value){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Failure-R([scriptblock]$Body){try{& $Body|Out-Null;return ''}catch{return $_.Exception.Message}}
function Case-R([string]$Name,[scriptblock]$Body){try{& $Body;Write-Host "PASS  $Name"}catch{$script:failures.Add(('{0}: {1}' -f $Name,$_.Exception.Message));Write-Host "FAIL  $Name :: $($_.Exception.Message)"}}
function Get-BFTaskDirectory {param([string]$ProjectPath,[string]$TaskId) Join-Path $ProjectPath ('.bsl-flow/tasks/'+$TaskId)}
function Get-BFNativeSource {param([string]$Root) $script:source}
function Get-BFSourceManifest {param($State) $script:manifest}
function Get-BFNativeDependencies {param($Criterion) $script:dependencies}
function Write-Raw([string]$Path,[string]$Text){[void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent));[IO.File]::WriteAllText($Path,$Text,[Text.UTF8Encoding]::new($false))}
function Get-RawHashes([string]$Root){return @(Get-ChildItem -LiteralPath $Root -File -Recurse | Sort-Object FullName | ForEach-Object {[ordered]@{path=$_.FullName;sha256=Get-BFFileHash $_.FullName}})}
function New-RInventory([string]$Target,[string]$SelectedVersion='1',[string]$OtherVersion='1'){
    [ordered]@{target=$Target;platform=[ordered]@{executable='offline';com_connector='offline'};extensions=@(
        [ordered]@{properties=[ordered]@{name='Other';version=$OtherVersion;active=$true;purpose='Customization';scope='InfoBase';uuid='22222222-2222-2222-2222-222222222222';hash_sum='OTHER'}},
        [ordered]@{properties=[ordered]@{name='Ext';version=$SelectedVersion;active=$true;purpose='Customization';scope='InfoBase';uuid='33333333-3333-3333-3333-333333333333';hash_sum='EXT'}}
    )}
}
function New-ReuseFixture([string]$Name){
    $project=Join-Path $script:root ('project/'+$Name);$taskId=[guid]::NewGuid().ToString();$prior=[guid]::NewGuid().ToString();$target=Join-Path $script:root ('target/'+$Name);[void][IO.Directory]::CreateDirectory($target)
    $task=Get-BFTaskDirectory $project $taskId;$attempt=Join-Path $task ('attempts/'+$prior);$raw=Join-Path $attempt 'raw/native';[void][IO.Directory]::CreateDirectory($raw)
    $native=[ordered]@{source_root='src/ext';extension='Ext';module='FixtureModule';platform_version='8.3.27.2074';executable_sha256=('d'*64);authorized_operations=@('inventory','test');authorization_reference='fixture';reuse_load_attempt=$prior}
    $criterion=[ordered]@{id='native';kind='integration';target=$target;expected_tests=@('FixtureModule.ExactCase');native_1c=$native}
    $request=[ordered]@{schema_version=1;kind='bsl-flow.native-1c-request';task_id=$taskId;attempt_id=$prior;criterion_id='native';target=$target;target_key='fixture';source=[ordered]@{extension='Ext';version='1';uuid=$script:source.uuid;sha256=$script:source.sha256};operations=@('inventory','load','update','test');expected_tests=@('FixtureModule.ExactCase')}
    Write-BFJson (Join-Path $raw 'runtime-request.json') $request
    Write-BFJson (Join-Path $raw 'test-config.json') ([ordered]@{filter=[ordered]@{modules=@('FixtureModule')}})
    $before=New-RInventory $target '0';$current=New-RInventory $target '1'
    Write-BFJson (Join-Path $raw 'inventory-before.json') $before
    $requestHash=Get-BFHash $request
    foreach($step in @('load','update')){
        $dir=Join-Path $raw ('steps/'+$step);$log=Join-Path $dir ($step+'.log');Write-Raw $log ($step+' OK')
        Write-BFJson (Join-Path $dir 'terminal.json') ([ordered]@{request_sha256=$requestHash;exit_code=0;log=$log;log_sha256=(Get-BFFileHash $log)})
    }
    Write-Raw (Join-Path $raw 'source-snapshot/fixture.txt') 'same source identity is stubbed'
    $start=[ordered]@{stage='verify';source_manifest=$script:manifest;dependencies=[ordered]@{native_platform=(Get-BFHash @($script:dependencies))}}
    Write-BFJson (Join-Path $attempt 'start.json') $start
    $recoveryId=[guid]::NewGuid().ToString()
    $inventoryPath=Join-Path $task ('inputs/recovery-inventory-'+$recoveryId+'.json');Write-BFJson $inventoryPath $current
    $resolution=[ordered]@{attempt_id=$prior;scope='native_1c';target=$target;source_sha256=$script:manifest.sha256;inventory_sha256=(Get-BFNativeInventoryHash $current);observation='Recorded exact control read.';retry_authorized=$true}
    $event=[ordered]@{schema_version=1;input_event_id=$recoveryId;expected_revision=1;kind='recovery';provenance=[ordered]@{source='user';reference='fixture';text='Reuse only loaded extension.'};resolution=$resolution}
    Write-BFJson (Join-Path $task ('inputs/'+$recoveryId+'.json')) $event
    Write-BFJson (Join-Path $task ('inputs/recovery-'+$recoveryId+'.json')) ([ordered]@{resolution=$resolution;actual_source_manifest=$script:manifest;runtime_control_read=[ordered]@{inventory_path=$inventoryPath}})
    $result=[ordered]@{schema_version=1;stage='verify';outcome='BLOCKED';summary='Test receipt failed after load/update.';raw_hashes=(Get-RawHashes (Join-Path $attempt 'raw'))}
    Write-BFJson (Join-Path $attempt 'result.json') $result
    $state=[ordered]@{task_id=$taskId;project_path=$project;request=[ordered]@{criteria=@($criterion)};evidence=@([ordered]@{attempt_id=$prior;stage='verify';outcome='BLOCKED';result_sha256=(Get-BFHash $result)});events=@([ordered]@{input_event_id=$recoveryId;kind='recovery';sha256=(Get-BFHash $event)});unresolved_effect=$null}
    return [ordered]@{state=$state;criterion=$criterion;prior=$prior;attempt=$attempt;raw=$raw;target=$target;current=$current;task=$task;event=$event;recoveryId=$recoveryId}
}
function Update-RRecordedRawHashes($Fixture){
    $resultPath=Join-Path $Fixture.attempt 'result.json';$result=Read-BFJson $resultPath;$result.raw_hashes=Get-RawHashes (Join-Path $Fixture.attempt 'raw');Write-BFJson $resultPath $result -Replace
    $Fixture.state.evidence[0].result_sha256=Get-BFHash $result
}

$testFailure=$null;$cleanupFailure=$null
$script:root=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-native-reuse-'+[guid]::NewGuid().ToString('N'));[void][IO.Directory]::CreateDirectory($script:root)
try {
    Case-R 'accepted reuse binds exact failed native load, committed recovery, source, inventory, and receipts' {
        $f=New-ReuseFixture 'accepted';$proof=Get-BFNativeLoadedProof $f.state $f.criterion $script:source $f.current
        Assert-R ($proof.kind -eq 'bsl-flow.native-loaded-proof' -and $proof.loaded_attempt_id -eq $f.prior -and $proof.terminals.Count -eq 2) 'Exact recorded native load was not admitted for test-only reuse.'
    }
    Case-R 'normal native criterion without reuse returns null' {
        $f=New-ReuseFixture 'normal';$f.criterion.native_1c.Remove('reuse_load_attempt')
        Assert-R ($null -eq (Get-BFNativeLoadedProof $f.state $f.criterion $script:source $f.current)) 'Ordinary native verification unexpectedly required a reuse proof.'
    }
    Case-R 'missing or uncommitted recovery blocks reuse' {
        $f=New-ReuseFixture 'missing';$f.state.events=@();$message=Failure-R {Get-BFNativeLoadedProof $f.state $f.criterion $script:source $f.current};Assert-R ($message -ne '') 'Missing recovery unexpectedly succeeded.';Assert-R ($message -match 'test-only continuation requires committed control-read recovery') "Missing recovery rejected with an unexpected error: $message"
        $f=New-ReuseFixture 'uncommitted';Remove-Item -LiteralPath (Join-Path $f.task ('inputs/recovery-'+$f.recoveryId+'.json'));$message=Failure-R {Get-BFNativeLoadedProof $f.state $f.criterion $script:source $f.current};Assert-R ($message -ne '') 'Uncommitted recovery unexpectedly succeeded.';Assert-R ($message -match 'cannot find path|does not exist|JSON input.*missing|matching committed recovery') "Uncommitted recovery rejected with an unexpected error: $message"
    }
    Case-R 'selected or foreign inventory changes after recovery block reuse' {
        $f=New-ReuseFixture 'selected';Assert-R ((Failure-R {Get-BFNativeLoadedProof $f.state $f.criterion $script:source (New-RInventory $f.target '2')}) -match 'installed extension version|inventory changed') 'Changed selected extension inventory was accepted.'
        $f=New-ReuseFixture 'foreign';Assert-R ((Failure-R {Get-BFNativeLoadedProof $f.state $f.criterion $script:source (New-RInventory $f.target '1' '2')}) -match 'nonselected extension changed|inventory changed') 'Changed foreign extension inventory was accepted.'
    }
    Case-R 'changed raw log, source, platform, module, or test selection blocks reuse' {
        $f=New-ReuseFixture 'log';[IO.File]::AppendAllText((Join-Path $f.raw 'steps/load/load.log'),' changed',[Text.UTF8Encoding]::new($false));Assert-R ((Failure-R {Get-BFNativeLoadedProof $f.state $f.criterion $script:source $f.current}) -match 'prior native evidence changed') 'Changed raw load log was accepted.'
        $f=New-ReuseFixture 'source';$changed=[ordered]@{extension='Ext';version='1';uuid=$script:source.uuid;sha256=('e'*64)};Assert-R ((Failure-R {Get-BFNativeLoadedProof $f.state $f.criterion $changed $f.current}) -match 'loaded source differs') 'Changed source was accepted.'
        $f=New-ReuseFixture 'platform';$save=$script:dependencies;$script:dependencies=[ordered]@{executable_sha256=('f'*64);platform='changed'};try{Assert-R ((Failure-R {Get-BFNativeLoadedProof $f.state $f.criterion $script:source $f.current}) -match 'platform') 'Changed platform was accepted.'}finally{$script:dependencies=$save}
        $f=New-ReuseFixture 'module';$f.criterion.native_1c.module='OtherModule';Assert-R ((Failure-R {Get-BFNativeLoadedProof $f.state $f.criterion $script:source $f.current}) -match 'module changed') 'Changed module was accepted.'
        $f=New-ReuseFixture 'tests';$f.criterion.expected_tests=@('FixtureModule.OtherCase');Assert-R ((Failure-R {Get-BFNativeLoadedProof $f.state $f.criterion $script:source $f.current}) -match 'selected tests') 'Changed test selection was accepted.'
    }
    Case-R 'cross-task, cross-attempt, chained reuse, and missing own load receipt block reuse' {
        $f=New-ReuseFixture 'cross-task';$requestPath=Join-Path $f.raw 'runtime-request.json';$request=Read-BFJson $requestPath;$request.task_id=[guid]::NewGuid().ToString();Write-BFJson $requestPath $request -Replace;Update-RRecordedRawHashes $f;Assert-R ((Failure-R {Get-BFNativeLoadedProof $f.state $f.criterion $script:source $f.current}) -match 'original full native attempt') 'Cross-task request was accepted.'
        $f=New-ReuseFixture 'cross-attempt';$request=Read-BFJson (Join-Path $f.raw 'runtime-request.json');$request.attempt_id=[guid]::NewGuid().ToString();Write-BFJson (Join-Path $f.raw 'runtime-request.json') $request -Replace;Update-RRecordedRawHashes $f;Assert-R ((Failure-R {Get-BFNativeLoadedProof $f.state $f.criterion $script:source $f.current}) -match 'original full native attempt') 'Cross-attempt request was accepted.'
        $f=New-ReuseFixture 'chain';$request=Read-BFJson (Join-Path $f.raw 'runtime-request.json');$request.operations=@('inventory','test');Write-BFJson (Join-Path $f.raw 'runtime-request.json') $request -Replace;Update-RRecordedRawHashes $f;Assert-R ((Failure-R {Get-BFNativeLoadedProof $f.state $f.criterion $script:source $f.current}) -match 'original full native attempt') 'Chained reuse request was accepted.'
        $f=New-ReuseFixture 'missing-load';Remove-Item -LiteralPath (Join-Path $f.raw 'steps/load/terminal.json');Update-RRecordedRawHashes $f;$message=Failure-R {Get-BFNativeLoadedProof $f.state $f.criterion $script:source $f.current};Assert-R ($message -ne '') 'Missing own load receipt unexpectedly succeeded.';Assert-R ($message -match 'cannot find path|does not exist|JSON input.*missing') "Missing own load receipt rejected with an unexpected error: $message"
    }
} catch {$testFailure=$_} finally {
    $safe=[IO.Path]::GetFullPath($script:root);$temp=[IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\','/')
    if($safe.StartsWith($temp+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)-and(Split-Path $safe -Leaf)-like'bsl-flow-native-reuse-*'){try{Remove-Item -LiteralPath $safe -Recurse -Force}catch{$cleanupFailure=$_}}else{$cleanupFailure=[InvalidOperationException]::new('Fixture cleanup refused a path outside the created temp root.')}
}
Write-Host "Native reuse offline: $script:checks checks; $($script:failures.Count) failures; COM=0; DB writes=0; model/network calls=0."
foreach($failure in $script:failures){Write-Host "  $failure"}
if($null -ne $testFailure){throw $testFailure}
if($null -ne $cleanupFailure){throw $cleanupFailure}
if($script:failures.Count){throw ('Native reuse offline failures: '+($script:failures -join ' | '))}
