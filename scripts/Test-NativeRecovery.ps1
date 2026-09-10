#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$core=Join-Path $PackageRoot 'global\skills\1c-task\scripts'
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Process.ps1','Task.Runtime.ps1')){. (Join-Path $core $name)}

$script:checks=0
$script:failures=[Collections.Generic.List[string]]::new()
$script:fixtureRoot=$null
$script:sourceSha256=('a'*64)
$script:inventory=$null

function Assert-N([bool]$Value,[string]$Message){if(-not $Value){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Failure-N([scriptblock]$Body){try{& $Body|Out-Null;return ''}catch{return $_.Exception.Message}}
function Invoke-NCase([string]$Name,[scriptblock]$Body){
    try { & $Body; Write-Host "PASS  $Name" }
    catch { $script:failures.Add(('{0}: {1}' -f $Name,$_.Exception.Message)); Write-Host "FAIL  $Name :: $($_.Exception.Message)" }
}
function New-NTarget([string]$Name){
    $target=Join-Path $script:fixtureRoot ('targets/'+$Name)
    [void][IO.Directory]::CreateDirectory($target)
    [IO.File]::WriteAllBytes((Join-Path $target '1Cv8.1CD'),[byte[]](0))
    return [IO.Path]::GetFullPath($target)
}
function New-NAttempt([string]$Name){
    $dir=Join-Path $script:fixtureRoot ('attempts/'+$Name)
    [void][IO.Directory]::CreateDirectory($dir)
    Write-BFJson (Join-Path $dir 'start.json') ([ordered]@{stage='verify';controller_process=[ordered]@{pid=2147483647;start_time_utc='2000-01-01T00:00:00.0000000Z'}})
    return $dir
}
function New-NState([string]$Target,[string]$AttemptId,[bool]$UseUnresolved=$false,[string]$TaskId=([guid]::NewGuid().ToString())){
    $state=[ordered]@{task_id=$TaskId;active_attempt=$AttemptId;unresolved_effect=$null;worker_path=$script:fixtureRoot;request=[ordered]@{criteria=@([ordered]@{id='native';target=$Target;executable=(Join-Path $PSHOME 'pwsh.exe')})}}
    if($UseUnresolved){$state.active_attempt=$null;$state.unresolved_effect=[ordered]@{attempt_id=$AttemptId;scope='native_1c'}}
    return $state
}
function New-NResolution([string]$Target,[string]$AttemptId,[string]$InventoryHash){
    return [ordered]@{attempt_id=$AttemptId;scope='native_1c';target=$Target;source_sha256=$script:sourceSha256;inventory_sha256=$InventoryHash;observation='Offline control-read fixture observes the admitted target state.';retry_authorized=$true}
}
function Write-NPending($State,[string]$Target,[string]$AttemptId){
    $journal=Get-BFNativeJournalRoot (Get-BFRuntimeTargetKey $Target)
    [void][IO.Directory]::CreateDirectory($journal)
    $pending=[ordered]@{schema_version=1;state='dispatched_or_unknown';task_id=$State.task_id;attempt_id=$AttemptId;criterion_id='native';target=$Target;source_root='src/ext';full_source_sha256=$script:sourceSha256;extension_source_sha256=('b'*64);request_sha256=('c'*64);created_at_utc='2026-09-10T00:00:00.0000000Z'}
    Write-BFJson (Join-Path $journal 'pending.json') $pending -Replace
    return $journal
}
function Stop-NFixtureProcess([string]$ProcessPath){
    if(-not(Test-Path -LiteralPath $ProcessPath -PathType Leaf)){return}
    $identity=Read-BFJson $ProcessPath
    $owned=Get-BFOwnedProcess $identity
    if($null -eq $owned){return}
    try {
        Assert-N ($identity.pid -eq $owned.Id) 'Cleanup refused a process whose stored identity was not exact.'
        $owned.Kill($true);[void]$owned.WaitForExit(5000)
    } finally {$owned.Dispose()}
}

# Recovery uses these permitted offline seams only. It never reaches COM, a database,
# network, or a model; production recovery/ledger code remains the code under test.
function Get-BFNativeJournalRoot { param([string]$TargetKey) Join-Path $script:fixtureRoot ('journal/'+$TargetKey) }
function Get-BFSourceManifest { param($State) [ordered]@{sha256=$script:sourceSha256} }
function Read-BFNativeInventory { param([string]$Target,[string]$Executable,[Management.Automation.PSCredential]$Credential,[string]$Directory,[scriptblock]$Cancelled) return $script:inventory }
function Get-BFTaskDirectory { param([string]$ProjectPath,[string]$TaskId) return (Join-Path $ProjectPath ('.bsl-flow/tasks/'+$TaskId)) }
function New-NSavedSuccessFixture([string]$Name,[bool]$Pending=$true,[string]$PendingTaskId=$null){
    $target=New-NTarget ($Name+'-target')
    $attempt=('attempt-'+$Name)
    $taskId=[guid]::NewGuid().ToString()
    $state=[ordered]@{task_id=$taskId;worker_path=(Join-Path $script:fixtureRoot ('saved/'+$Name+'/worker'));request=$null}
    $sourceRoot=Join-Path $state.worker_path 'src/ext'
    [void][IO.Directory]::CreateDirectory($sourceRoot)
    $uuid='11111111-1111-1111-1111-111111111111'
    $configuration='<MetaDataObject><Configuration uuid="'+$uuid+'"><Properties><Name>Ext</Name><Version>1</Version></Properties></Configuration></MetaDataObject>'
    [IO.File]::WriteAllText((Join-Path $sourceRoot 'Configuration.xml'),$configuration,[Text.UTF8Encoding]::new($false))
    $native=[ordered]@{source_root='src/ext';extension='Ext';module='FixtureModule';platform_version='8.3.27.2074';executable_sha256=('1'*64);authorized_operations=@('inventory','load','update','test');authorization_reference='offline-fixture'}
    $criterion=[ordered]@{id='native';kind='integration';target=$target;executable=(Join-Path $PSHOME 'pwsh.exe');expected_tests=@('FixtureModule.ExactCase');native_1c=$native}
    $state.request=[ordered]@{criteria=@($criterion)}
    $directory=Join-Path $script:fixtureRoot ('saved/'+$Name+'/raw/native')
    [void][IO.Directory]::CreateDirectory($directory)
    Copy-Item -LiteralPath $sourceRoot -Destination (Join-Path $directory 'source-snapshot') -Recurse
    $source=Get-BFNativeSource $sourceRoot
    $request=[ordered]@{schema_version=1;kind='bsl-flow.native-1c-request';task_id=$taskId;attempt_id=$attempt;criterion_id='native';target=$target;target_key=(Get-BFRuntimeTargetKey $target);source=[ordered]@{extension=$source.extension;version=$source.version;uuid=$source.uuid;sha256=$source.sha256};platform=[ordered]@{version=$native.platform_version;executable_sha256=$native.executable_sha256};operations=@($native.authorized_operations);authorization_reference=$native.authorization_reference;expected_tests=@($criterion.expected_tests)}
    Write-BFJson (Join-Path $directory 'runtime-request.json') $request
    $inventory=[ordered]@{target=$target;platform=[ordered]@{executable='offline';com_connector='offline'};extensions=@([ordered]@{properties=[ordered]@{name='Ext';version='1';active=$true;purpose='Customization';scope='InfoBase';uuid=$uuid;hash_sum='AA'}})}
    Write-BFJson (Join-Path $directory 'inventory-before.json') $inventory
    Write-BFJson (Join-Path $directory 'inventory-after.json') $inventory
    $now=[DateTime]::UtcNow
    foreach($stepName in @('load','update','test')){
        $stepDir=Join-Path $directory ('steps/'+$stepName);[void][IO.Directory]::CreateDirectory($stepDir)
        $log=Join-Path $stepDir ($stepName+'.log');[IO.File]::WriteAllText($log,($stepName+' completed'),[Text.UTF8Encoding]::new($false))
        $identity=[ordered]@{pid=2147483647;start_time_utc=$now.AddSeconds(-1).ToString('o');request_sha256=(Get-BFHash $request);step=$stepName;non_interruptible=$true}
        Write-BFJson (Join-Path $stepDir 'terminal.json') ([ordered]@{request_sha256=(Get-BFHash $request);process=$identity;exit_code=0;finished_at_utc=$now.ToString('o');log=$log;log_sha256=(Get-BFFileHash $log)})
    }
    $junit=Join-Path $directory 'original.junit.xml'
    [IO.File]::WriteAllText($junit,'<testsuite tests="1" failures="0" errors="0" skipped="0"><testcase classname="FixtureModule" name="ExactCase"/></testsuite>',[Text.UTF8Encoding]::new($false));(Get-Item -LiteralPath $junit).LastWriteTimeUtc=$now
    $receipt=[ordered]@{schema_version=1;kind='bsl-flow.native-1c-success';task_id=$taskId;attempt_id=$attempt;request_sha256=(Get-BFHash $request);source_sha256=$source.sha256;inventory_before_sha256=(Get-BFNativeInventoryHash $inventory);inventory_after_sha256=(Get-BFNativeInventoryHash $inventory);junit_sha256=(Get-BFFileHash $junit);tests=@($criterion.expected_tests);completed_at_utc=$now.ToString('o')}
    Write-BFJson (Join-Path $directory 'runtime-success.json') $receipt
    $journal=Get-BFNativeJournalRoot $request.target_key
    if($Pending){
        [void][IO.Directory]::CreateDirectory($journal)
        $pendingId=if($PendingTaskId){$PendingTaskId}else{$taskId}
        Write-BFJson (Join-Path $journal 'pending.json') ([ordered]@{schema_version=1;state='dispatched_or_unknown';task_id=$pendingId;attempt_id=$attempt;criterion_id='native';target=$target;full_source_sha256=$script:sourceSha256;request_sha256=$receipt.request_sha256})
    }
    return [ordered]@{state=$state;criterion=$criterion;directory=$directory;attempt=$attempt;target=$target;journal=$journal;sourceRoot=$sourceRoot;junit=$junit}
}
function New-NRecordedSuccessFixture([string]$Name){
    $fixture=New-NSavedSuccessFixture $Name
    $project=Join-Path $script:fixtureRoot ('recorded/'+$Name+'/project')
    $taskDirectory=Join-Path $project ('.bsl-flow/tasks/'+$fixture.state.task_id)
    $attemptDirectory=Join-Path $taskDirectory ('attempts/'+$fixture.attempt)
    $rawRoot=Join-Path $attemptDirectory 'raw'
    [void][IO.Directory]::CreateDirectory($rawRoot)
    Copy-Item -LiteralPath $fixture.directory -Destination $rawRoot -Recurse
    $copiedNative=Join-Path $rawRoot 'native'
    $rawHashes=@(Get-ChildItem -LiteralPath $rawRoot -File -Recurse | Sort-Object FullName | ForEach-Object {[ordered]@{path=$_.FullName;sha256=(Get-BFFileHash $_.FullName)}})
    $result=[ordered]@{schema_version=1;stage='verify';outcome='PASS';summary='Recorded native fixture PASS.';raw_hashes=$rawHashes}
    Write-BFJson (Join-Path $attemptDirectory 'result.json') $result
    $fixture.state.project_path=$project
    $fixture.state.evidence=@([ordered]@{stage='verify';attempt_id=$fixture.attempt;outcome='PASS';result_sha256=(Get-BFHash $result);raw_hashes=$rawHashes})
    return [ordered]@{fixture=$fixture;attemptDirectory=$attemptDirectory;rawRoot=$rawRoot;nativeDirectory=$copiedNative;result=$result}
}

$testFailure=$null
$cleanupFailure=$null
$script:fixtureRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-native-recovery-'+[guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($script:fixtureRoot)
$secure=ConvertTo-SecureString 'offline-dummy-password' -AsPlainText -Force
$script:BFNativeCredential=[Management.Automation.PSCredential]::new('offline-user',$secure)
try {
    Invoke-NCase 'native process records real successful launch and terminal receipt' {
        $dir=Join-Path $script:fixtureRoot 'native-process-success';$log=Join-Path $dir 'success.log'
        $command="[IO.File]::WriteAllText('$($log.Replace("'","''"))','ok')"
        $step=[ordered]@{name='success';log=$log;argv=@('-NoProfile','-Command',$command)}
        $result=Invoke-BFNativeProcess (Join-Path $PSHOME 'pwsh.exe') $step $script:BFNativeCredential $dir 10 {$false} ('d'*64)
        $stepDir=Join-Path $dir 'steps/success'
        Assert-N ($result.exit_code -eq 0 -and (Test-Path -LiteralPath (Join-Path $stepDir 'prepared.json')) -and (Test-Path -LiteralPath (Join-Path $stepDir 'started.json')) -and (Test-Path -LiteralPath (Join-Path $stepDir 'terminal.json'))) 'Successful harmless subprocess did not preserve the native durable records.'
    }

    Invoke-NCase 'cancellation before native launch emits no process identity or child effect' {
        $dir=Join-Path $script:fixtureRoot 'native-process-cancel';$marker=Join-Path $dir 'started.marker'
        $command="[IO.File]::WriteAllText('$($marker.Replace("'","''"))','started');Start-Sleep -Seconds 30"
        $step=[ordered]@{name='cancel-before-launch';log=(Join-Path $dir 'cancel.log');argv=@('-NoProfile','-Command',$command)}
        try {
            $message=Failure-N {Invoke-BFNativeProcess (Join-Path $PSHOME 'pwsh.exe') $step $script:BFNativeCredential $dir 10 {$true} ('e'*64)}
            Assert-N ($message -match '^BF_BLOCKED: cancelled before native dispatch') ("Pre-launch cancellation did not fail closed: $message")
            $stepDir=Join-Path $dir 'steps/cancel-before-launch'
            Assert-N (-not(Test-Path -LiteralPath (Join-Path $stepDir 'process.json')) -and -not(Test-Path -LiteralPath $marker)) 'Cancellation before launch created a native process identity or child effect.'
        } finally { Stop-NFixtureProcess (Join-Path $dir 'steps/cancel-before-launch/process.json') }
    }

    Invoke-NCase 'timeout leaves harmless native child alive until explicit exact-identity cleanup' {
        $dir=Join-Path $script:fixtureRoot 'native-process-timeout'
        $step=[ordered]@{name='timeout';log=(Join-Path $dir 'timeout.log');argv=@('-NoProfile','-Command','Start-Sleep -Seconds 30')}
        $processPath=Join-Path $dir 'steps/timeout/process.json'
        try {
            $message=Failure-N {Invoke-BFNativeProcess (Join-Path $PSHOME 'pwsh.exe') $step $script:BFNativeCredential $dir 1 {$false} ('f'*64)}
            Assert-N ($message -match '^BF_BLOCKED: native process timed out and was not killed') 'Timeout did not retain the non-interruptible native process.'
            $identity=Read-BFJson $processPath
            Assert-N ($null -ne (Get-BFOwnedProcess $identity)) 'Timeout unexpectedly killed the harmless native child.'
        } finally { Stop-NFixtureProcess $processPath }
    }

    Invoke-NCase 'global expired task deadline creates no native process or launch receipt' {
        $dir=Join-Path $script:fixtureRoot 'native-process-deadline';$marker=Join-Path $dir 'deadline.marker'
        $command="[IO.File]::WriteAllText('$($marker.Replace("'","''"))','started')"
        $step=[ordered]@{name='deadline';log=(Join-Path $dir 'deadline.log');argv=@('-NoProfile','-Command',$command)}
        Set-Variable -Scope Script -Name BFRunDeadlineUtc -Value ([DateTime]::UtcNow.AddSeconds(-1))
        try {
            $message=Failure-N {Invoke-BFNativeProcess (Join-Path $PSHOME 'pwsh.exe') $step $script:BFNativeCredential $dir 10 {$false} ('9'*64)}
            Assert-N ($message -match '^BF_BLOCKED: task deadline reached before native dispatch') "Expired native deadline did not block dispatch: $message"
            Assert-N (-not(Test-Path -LiteralPath (Join-Path $dir 'steps/deadline/prepared.json')) -and -not(Test-Path -LiteralPath $marker)) 'Expired task deadline created a native launch record or child effect.'
        } finally {Remove-Variable -Scope Script -Name BFRunDeadlineUtc -ErrorAction SilentlyContinue}
    }

    Invoke-NCase 'saved completed native success is observed offline and clears only its exact pending latch' {
        $fixture=New-NSavedSuccessFixture 'saved-success'
        $observation=Get-BFNativeSavedObservation $fixture.state $fixture.criterion $fixture.directory $fixture.attempt
        Assert-N ($observation.outcome -eq 'PASS' -and $observation.tests.Count -eq 1 -and -not(Test-Path -LiteralPath (Join-Path $fixture.directory 'steps/test/process.json'))) 'Saved native success was not recovered from original durable evidence alone.'
        [void](Complete-BFNativeSavedSuccess $fixture.state $fixture.directory $fixture.attempt)
        Assert-N (-not(Test-Path -LiteralPath (Join-Path $fixture.journal 'pending.json')) -and (Test-Path -LiteralPath (Join-Path $fixture.journal ('history/'+$fixture.attempt+'.success.json')))) 'Exact saved success did not atomically archive its receipt and clear its pending latch.'
        [void](Complete-BFNativeSavedSuccess $fixture.state $fixture.directory $fixture.attempt)
        Assert-N $true 'Duplicate saved-success finalize was not idempotent.'
    }

    Invoke-NCase 'saved native success rejects source and terminal-hash tampering' {
        $fixture=New-NSavedSuccessFixture 'saved-tamper'
        [IO.File]::AppendAllText((Join-Path $fixture.sourceRoot 'Configuration.xml'),'<!-- changed -->',[Text.UTF8Encoding]::new($false))
        Assert-N ((Failure-N {Get-BFNativeSavedObservation $fixture.state $fixture.criterion $fixture.directory $fixture.attempt}) -match 'source snapshot is stale') 'Changed native source was accepted as a saved success.'
        $uuid='11111111-1111-1111-1111-111111111111';$configuration='<MetaDataObject><Configuration uuid="'+$uuid+'"><Properties><Name>Ext</Name><Version>1</Version></Properties></Configuration></MetaDataObject>'
        [IO.File]::WriteAllText((Join-Path $fixture.sourceRoot 'Configuration.xml'),$configuration,[Text.UTF8Encoding]::new($false))
        $log=Join-Path $fixture.directory 'steps/test/test.log';[IO.File]::AppendAllText($log,' tampered',[Text.UTF8Encoding]::new($false))
        Assert-N ((Failure-N {Get-BFNativeSavedObservation $fixture.state $fixture.criterion $fixture.directory $fixture.attempt}) -match 'incomplete saved native terminal evidence') 'Changed native terminal log hash was accepted as a saved success.'
    }

    Invoke-NCase 'saved success cannot clear another task pending latch' {
        $otherTask=[guid]::NewGuid().ToString();$fixture=New-NSavedSuccessFixture 'saved-other-task' $true $otherTask;$pending=Join-Path $fixture.journal 'pending.json';$before=Get-BFFileHash $pending
        [void](Complete-BFNativeSavedSuccess $fixture.state $fixture.directory $fixture.attempt)
        Assert-N ((Test-Path -LiteralPath $pending) -and (Get-BFFileHash $pending) -ceq $before) 'Saved success removed a pending latch owned by another task.'
    }

    Invoke-NCase 'recorded native PASS completes its own pending latch from verified result and raw hashes' {
        $recorded=New-NRecordedSuccessFixture 'recorded-success';$fixture=$recorded.fixture;$pending=Join-Path $fixture.journal 'pending.json'
        Complete-BFRecordedNativeSuccess $fixture.state $fixture.attempt
        Assert-N (-not(Test-Path -LiteralPath $pending) -and (Test-Path -LiteralPath (Join-Path $fixture.journal ('history/'+$fixture.attempt+'.success.json')))) 'Verified recorded native PASS did not clear its own durable pending latch.'
        $history=Get-BFFileHash (Join-Path $fixture.journal ('history/'+$fixture.attempt+'.success.json'))
        Complete-BFRecordedNativeSuccess $fixture.state $fixture.attempt
        Assert-N ((Get-BFFileHash (Join-Path $fixture.journal ('history/'+$fixture.attempt+'.success.json'))) -ceq $history) 'Repeated recorded native completion changed accepted history.'
    }

    Invoke-NCase 'recorded native PASS rejects changed raw evidence before pending completion' {
        $recorded=New-NRecordedSuccessFixture 'recorded-tamper';$fixture=$recorded.fixture;$pending=Join-Path $fixture.journal 'pending.json';$before=Get-BFFileHash $pending
        [IO.File]::AppendAllText((Join-Path $recorded.nativeDirectory 'steps/test/test.log'),' tampered',[Text.UTF8Encoding]::new($false))
        Assert-N ((Failure-N {Complete-BFRecordedNativeSuccess $fixture.state $fixture.attempt}) -match 'recorded native evidence changed') 'Changed raw evidence was accepted before recorded native completion.'
        Assert-N ((Test-Path -LiteralPath $pending) -and (Get-BFFileHash $pending) -ceq $before) 'Rejected recorded evidence changed or cleared the pending latch.'
    }

    Invoke-NCase 'recovery writes control receipt before Complete clears matching active latch and remains idempotent' {
        $target=New-NTarget 'active';$attempt='attempt-active';$state=New-NState $target $attempt;$attemptDir=New-NAttempt 'active';$script:inventory=[ordered]@{target=$target;platform=[ordered]@{executable='offline';com_connector='offline'};extensions=@()};$hash=Get-BFNativeInventoryHash $script:inventory;$resolution=New-NResolution $target $attempt $hash;$journal=Write-NPending $state $target $attempt
        $receipt=Resolve-BFNativeRecovery $state $resolution $attemptDir
        $pending=Read-BFJson (Join-Path $journal 'pending.json')
        Assert-N ($receipt.verdict -eq 'RECONCILED_NO_PASS' -and $pending.reconciled_identity_sha256 -match '^[0-9a-f]{64}$' -and $pending.reconciled_receipt_sha256 -match '^[0-9a-f]{64}$') 'Resolve did not durably mark the pending latch after the control read.'
        [void](Complete-BFNativeRecovery $state $resolution)
        Assert-N (-not(Test-Path -LiteralPath (Join-Path $journal 'pending.json'))) 'Complete cleared no pending latch after receipt validation.'
        [void](Complete-BFNativeRecovery $state $resolution)
        Assert-N $true 'Duplicate native finalize was not idempotent.'
    }

    Invoke-NCase 'receipt-first crash repair restores pending reconciliation markers before return' {
        $target=New-NTarget 'receipt-crash';$attempt='attempt-receipt-crash';$state=New-NState $target $attempt $true;$attemptDir=New-NAttempt 'receipt-crash';$script:inventory=[ordered]@{target=$target;platform=[ordered]@{executable='offline';com_connector='offline'};extensions=@()};$hash=Get-BFNativeInventoryHash $script:inventory;$resolution=New-NResolution $target $attempt $hash;$journal=Write-NPending $state $target $attempt
        [void](Resolve-BFNativeRecovery $state $resolution $attemptDir)
        # Simulate controller death after receipt write and before updating pending.json.
        [void](Write-NPending $state $target $attempt)
        [void](Resolve-BFNativeRecovery $state $resolution $attemptDir)
        $pending=Read-BFJson (Join-Path $journal 'pending.json')
        $reconciledIdentity=Get-BFValue $pending 'reconciled_identity_sha256'
        $reconciledReceipt=Get-BFValue $pending 'reconciled_receipt_sha256'
        Assert-N ($reconciledIdentity -match '^[0-9a-f]{64}$' -and $reconciledReceipt -match '^[0-9a-f]{64}$') 'Existing recovery receipt returned without repairing pending reconciliation markers.'
        [void](Complete-BFNativeRecovery $state $resolution)
        Assert-N (-not(Test-Path -LiteralPath (Join-Path $journal 'pending.json'))) 'Repaired receipt-first recovery could not finalize its latch.'
    }

    Invoke-NCase 'prepared native step without process identity fails closed' {
        $target=New-NTarget 'prepared-gap';$attempt='attempt-prepared-gap';$state=New-NState $target $attempt;$attemptDir=New-NAttempt 'prepared-gap';$stepDir=Join-Path $attemptDir 'steps/load';[void][IO.Directory]::CreateDirectory($stepDir);Write-BFJson (Join-Path $stepDir 'prepared.json') ([ordered]@{step='load'})
        $script:inventory=[ordered]@{target=$target;platform=[ordered]@{executable='offline';com_connector='offline'};extensions=@()};$resolution=New-NResolution $target $attempt (Get-BFNativeInventoryHash $script:inventory);[void](Write-NPending $state $target $attempt)
        Assert-N ((Failure-N {Resolve-BFNativeRecovery $state $resolution $attemptDir}) -match 'process identity gap') 'Prepared native launch without process identity was recovered automatically.'
    }

    Invoke-NCase 'live exact native process blocks recovery until test-owned cleanup' {
        $target=New-NTarget 'live-process';$attempt='attempt-live-process';$state=New-NState $target $attempt;$attemptDir=New-NAttempt 'live-process';$psi=[Diagnostics.ProcessStartInfo]::new();$psi.FileName=Join-Path $PSHOME 'pwsh.exe';$psi.UseShellExecute=$false;$psi.CreateNoWindow=$true;[void]$psi.ArgumentList.Add('-NoProfile');[void]$psi.ArgumentList.Add('-Command');[void]$psi.ArgumentList.Add('Start-Sleep -Seconds 30');$child=[Diagnostics.Process]::Start($psi)
        try {
            $identity=[ordered]@{pid=$child.Id;start_time_utc=$child.StartTime.ToUniversalTime().ToString('o');non_interruptible=$true};$stepDir=Join-Path $attemptDir 'steps/load';[void][IO.Directory]::CreateDirectory($stepDir);Write-BFJson (Join-Path $stepDir 'process.json') $identity
            $script:inventory=[ordered]@{target=$target;platform=[ordered]@{executable='offline';com_connector='offline'};extensions=@()};$resolution=New-NResolution $target $attempt (Get-BFNativeInventoryHash $script:inventory);[void](Write-NPending $state $target $attempt)
            Assert-N ((Failure-N {Resolve-BFNativeRecovery $state $resolution $attemptDir}) -match 'child process is still running') 'Live exact native child did not block recovery.'
        } finally {if(-not $child.HasExited){$identity=[ordered]@{pid=$child.Id;start_time_utc=$child.StartTime.ToUniversalTime().ToString('o')};$owned=Get-BFOwnedProcess $identity;if($null -ne $owned){try{$owned.Kill($true);[void]$owned.WaitForExit(5000)}finally{$owned.Dispose()}}};$child.Dispose()}
    }

    Invoke-NCase 'wrong target, full source hash, inventory hash, and cross-task pending all reject without ledger writes' {
        $target=New-NTarget 'mismatch';$attempt='attempt-mismatch';$state=New-NState $target $attempt;$attemptDir=New-NAttempt 'mismatch';$script:inventory=[ordered]@{target=$target;platform=[ordered]@{executable='offline';com_connector='offline'};extensions=@()};$hash=Get-BFNativeInventoryHash $script:inventory;$resolution=New-NResolution $target $attempt $hash;$journal=Write-NPending $state $target $attempt;$pendingPath=Join-Path $journal 'pending.json';$before=Get-BFFileHash $pendingPath
        $wrongTarget=New-NTarget 'other';$bad=New-NResolution $wrongTarget $attempt $hash;Assert-N ((Failure-N {Resolve-BFNativeRecovery $state $bad $attemptDir}) -match 'missing|mismatch') 'Wrong target was accepted.'
        $bad=New-NResolution $target $attempt $hash;$bad.source_sha256=('0'*64);Assert-N ((Failure-N {Resolve-BFNativeRecovery $state $bad $attemptDir}) -match 'identity mismatch') 'Non-full or wrong source hash was accepted.'
        $bad=New-NResolution $target $attempt ('0'*64);Assert-N ((Failure-N {Resolve-BFNativeRecovery $state $bad $attemptDir}) -match 'inventory control read') 'Wrong inventory hash was accepted.'
        $other=New-NState $target 'other-attempt' $false ([guid]::NewGuid().ToString());$bad=New-NResolution $target 'other-attempt' $hash;Assert-N ((Failure-N {Resolve-BFNativeRecovery $other $bad $attemptDir}) -match 'identity mismatch') 'Cross-task pending ledger did not block a new native recovery.'
        $receipts=@(Get-ChildItem -LiteralPath (Join-Path $journal 'recovery') -Filter '*.json' -File -ErrorAction SilentlyContinue)
        Assert-N ((Get-BFFileHash $pendingPath) -ceq $before -and $receipts.Count -eq 0) 'Rejected recovery changed the cross-task pending ledger.'
    }

    Invoke-NCase 'native error messages do not serialize a dummy password' {
        $dir=Join-Path $script:fixtureRoot 'secret';$credential=[Management.Automation.PSCredential]::new('offline-user',(ConvertTo-SecureString 'dummy-secret-123' -AsPlainText -Force));$step=[ordered]@{name='secret';log=(Join-Path $dir 'secret.log');argv=@('<password>')}
        $message=Failure-N {Invoke-BFNativeProcess (Join-Path $PSHOME 'pwsh.exe') $step $credential $dir 2 {$false} ('1'*64)}
        Assert-N ($message -notmatch 'dummy-secret-123') 'Native process error exposed the dummy password.'
    }
} catch {$testFailure=$_} finally {
    $script:BFNativeCredential=$null
    $safe=[IO.Path]::GetFullPath($script:fixtureRoot);$temp=[IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\','/')
    if($safe.StartsWith($temp+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)-and(Split-Path $safe -Leaf)-like'bsl-flow-native-recovery-*'){
        try{Remove-Item -LiteralPath $safe -Recurse -Force}catch{$cleanupFailure=$_}
    } else {$cleanupFailure=[InvalidOperationException]::new('Fixture cleanup refused a path outside the created temp fixture.')}
}

Write-Host "Native recovery offline: $script:checks checks; $($script:failures.Count) failures; COM=0; DB writes=0; model/network calls=0."
foreach($failure in $script:failures){Write-Host "  $failure"}
if($null -ne $testFailure){throw $testFailure}
if($null -ne $cleanupFailure){throw $cleanupFailure}
if($script:failures.Count -gt 0){throw ("Native recovery offline failures: "+($script:failures -join ' | '))}
