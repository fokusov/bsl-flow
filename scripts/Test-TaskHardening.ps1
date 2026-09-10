[CmdletBinding()]param([string]$PackageRoot)
Set-StrictMode -Version Latest;$ErrorActionPreference='Stop'
if(-not$PackageRoot){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$core=Join-Path $PackageRoot 'global\skills\1c-task\scripts'
foreach($n in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1')){. (Join-Path $core $n)}
$script:checks=0
function Assert-H([bool]$ok,[string]$message){if(-not$ok){throw "ASSERTION FAILED: $message"};$script:checks++}
function Failure-H([scriptblock]$action){try{&$action|Out-Null;return ''}catch{return $_.Exception.Message}}
function Write-H([string]$path,[string]$text){[void][IO.Directory]::CreateDirectory((Split-Path -Parent $path));[IO.File]::WriteAllText($path,$text,[Text.UTF8Encoding]::new($false))}
function New-Project-H([string]$root,[string]$name,[string]$s='optional'){
 $nl=[Environment]::NewLine;$p=Join-Path $root $name;[void][IO.Directory]::CreateDirectory((Join-Path $p 'src'));[void](Invoke-BFGit $p @('init'))
 Write-H (Join-Path $p 'src\main.bsl') ('Procedure Example()'+$nl+'EndProcedure'+$nl);Write-H (Join-Path $p 'outside.txt') ('outside'+$nl)
 Write-H (Join-Path $p '.gitignore') ('.bsl-flow/'+$nl+'openspec/changes/'+$nl)
 Write-H (Join-Path $p 'bsl-flow.yaml') ('review:'+$nl+'  routing:'+$nl+'    s_default: '+$s+$nl+'    m_default: required'+$nl+'    l_default: required'+$nl+'    high_risk_override: required'+$nl)
 [void](Invoke-BFGit $p @('add','.'));[void](Invoke-BFGit $p @('-c','user.name=BSL Flow Test','-c','user.email=test@example.invalid','commit','-m','Fixture'));return $p
}
function New-Request-H([string]$source='src'){
 [pscustomobject]@{schema_version=1;request_id=[guid]::NewGuid().ToString();prompt='Change the example procedure.';mode='implement';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();criteria=@([pscustomobject]@{id='example';observation='The source contains the requested token.';kind='file_assertion';path='src/main.bsl';contains='Example'});provenance=[pscustomobject]@{source='user';reference='hardening-fixture';text='Implement the bounded fixture change.'};models=[pscustomobject]@{worker='gpt-6-astra';worker_effort='medium';reviewer='gpt-6-astra';reviewer_effort='high'};source_paths=@($source);timeout_seconds=30}
}
function Inspect-H($run){
 $payload=Get-BFCanonicalJson ([ordered]@{complexity=$run.state.classification.complexity;risk=$run.state.classification.risk;impact_flags=@();rationale='The bounded fixture remains small and low risk.'})
 [ordered]@{schema_version=1;status='completed';summary='Inspection complete.';payload_json=$payload}
}
$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-hardening-'+[guid]::NewGuid().ToString('N'));[void][IO.Directory]::CreateDirectory($testRoot);$testFailure=$null;$cleanupFailure=$null
try{
 # A source discovery hint is never the worker write boundary.
 $p=New-Project-H $testRoot 'manifest';$request=New-Request-H 'src';$task=Start-BFTask $p $request;$manifest=Get-BFSourceManifest $task
 Assert-H ($manifest.source_paths.Count-eq1-and$manifest.source_paths[0]-eq'.') 'Manifest retained a discovery hint as its security boundary.'
 Assert-H (@($manifest.files|?{$_.path-eq'outside.txt'-and-not$_.deleted}).Count-eq1) 'Worker-writable file outside source_paths was omitted.'

 # Stored paths can be internally fresh while belonging to another controller installation.
 Assert-H ((Failure-H {Assert-BFPolicyFresh $task})-eq'') 'Unchanged current controller policy was rejected.'
 $foreignPolicy=Join-Path $testRoot 'foreign-install\global\skills\1c-task\scripts\Task.Gates.ps1'
 [void][IO.Directory]::CreateDirectory((Split-Path -Parent $foreignPolicy));Copy-Item -LiteralPath (Join-Path $core 'Task.Gates.ps1') -Destination $foreignPolicy
 $foreignState=Read-BFTask $p $task.task_id
 $foreignState.policy_files=@([ordered]@{path=[IO.Path]::GetFullPath($foreignPolicy);sha256=Get-BFFileHash $foreignPolicy})
 $foreignState.policy_hash=Get-BFHash $foreignState.policy_files
 $savedPolicyFresh=$true
 foreach($savedPolicy in @($foreignState.policy_files)){if(-not(Test-Path -LiteralPath $savedPolicy.path -PathType Leaf)-or(Get-BFFileHash $savedPolicy.path)-ne$savedPolicy.sha256){$savedPolicyFresh=$false}}
 Assert-H $savedPolicyFresh 'Foreign policy fixture was not internally fresh under the legacy saved-path check.'
 Assert-H ((Failure-H {Assert-BFPolicyFresh $foreignState})-match'^BF_BLOCKED: policy/package changed: current controller installation') 'A fresh snapshot from another controller installation was accepted.'

 # Saved read-only PASS evidence must become stale after either protected input changes.
 $deps=Get-BFDependencies $task 'code_review' $manifest;$e=[pscustomobject]@{stage='code_review';outcome='PASS';dependencies=$deps;raw_hashes=@()}
 Write-H (Join-Path $task.worker_path 'outside.txt') ('mutated'+[Environment]::NewLine);$changed=Get-BFSourceManifest $task
 Assert-H (-not(Test-BFEvidenceFresh $task $e $changed)) 'Source mutation during code review remained a fresh PASS.'
 $change=Get-BFChangePath $task;Write-H (Join-Path $change 'original-task.md') $task.request.prompt;Write-H (Join-Path $change 'spec.md') '# Fixture'
 $deps=Get-BFDependencies $task 'spec_review' $null;$e=[pscustomobject]@{stage='spec_review';outcome='PASS';dependencies=$deps;raw_hashes=@()}
 Write-H (Join-Path $change 'spec.md') '# Mutated fixture'
 Assert-H (-not(Test-BFEvidenceFresh $task $e $null)) 'Spec mutation during spec review remained a fresh PASS.'

 # Local YAML strengthens the S route.
 $routeProject=New-Project-H $testRoot 'route' 'required';$route=Start-BFTask $routeProject (New-Request-H)
 Assert-H ((@(Get-BFRoute $route)-join',')-eq'inspect,spec,spec_review,implement,verify,acceptance') 'Required S review from YAML was not applied.'

 # An interrupted source mutation survives authorization and needs an exact control-read hash.
 $effectProject=New-Project-H $testRoot 'effect';$effect=Start-BFTask $effectProject (New-Request-H)
 $executor={param($run)if($run.attempt.stage-eq'inspect'){return Inspect-H $run};if($run.attempt.stage-eq'implement'){Write-H (Join-Path $run.state.worker_path 'src\main.bsl') 'uncertain';throw'simulated interrupted implementation'};throw'unexpected stage'}
 $effect=Invoke-BFRun $effectProject $effect.task_id '' $executor
 Assert-H ($null-ne$effect.unresolved_effect-and$effect.unresolved_effect.scope-eq'source_only') 'Interrupted mutation lost its unresolved effect.'
 $auth=[pscustomobject]@{schema_version=1;input_event_id=[guid]::NewGuid().ToString();expected_revision=$effect.revision;kind='authorization';resume=$true;provenance=$effect.request.provenance}
 $message=Failure-H {Update-BFTask $effectProject $effect.task_id $auth}
 Assert-H ($message-match'^BF_BLOCKED:'-and$null-ne(Read-BFTask $effectProject $effect.task_id).unresolved_effect) 'Authorization cleared an unknown effect.'
 $bad=[pscustomobject]@{schema_version=1;input_event_id=[guid]::NewGuid().ToString();expected_revision=$effect.revision;kind='recovery';provenance=$effect.request.provenance;resolution=[pscustomobject]@{attempt_id=$effect.unresolved_effect.attempt_id;scope='source_only';source_sha256=('0'*64);observation='Exact source control read.'}}
 Assert-H ((Failure-H {Update-BFTask $effectProject $effect.task_id $bad})-match'^BF_CONFLICT:') 'Recovery accepted a stale source hash.'
 $source=Get-BFSourceManifest $effect
 $good=[pscustomobject]@{schema_version=1;input_event_id=[guid]::NewGuid().ToString();expected_revision=$effect.revision;kind='recovery';provenance=$effect.request.provenance;resolution=[pscustomobject]@{attempt_id=$effect.unresolved_effect.attempt_id;scope='source_only';source_sha256=$source.sha256;observation='Exact source control read.'}}
 $recovered=Update-BFTask $effectProject $effect.task_id $good
 Assert-H ($null-eq$recovered.unresolved_effect-and$recovered.revision-eq($effect.revision+1)) 'Exact recovery did not clear its matching effect.'

 # Cancellation observed before dispatch prevents executor and native process start.
 $cancelProject=New-Project-H $testRoot 'cancel';$cancel=Start-BFTask $cancelProject (New-Request-H);$run=New-BFAttempt $cancelProject $cancel.task_id '';[void](Cancel-BFTask $cancelProject $cancel.task_id)
 $marker=Join-Path $cancelProject 'stage.marker';$cancelExecutor={param($r)Write-H $marker 'ran';Inspect-H $r}.GetNewClosure();$cancelled=Invoke-BFStage $run '' $cancelExecutor $null
 Assert-H ($cancelled.status-eq'cancelled'-and-not(Test-Path $marker)) 'Cancelled stage invoked its executor.'
 $shell=Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe';$marker=Join-Path $cancelProject 'native.marker';$out=Join-Path $testRoot 'cancel-out'
 $code='[IO.File]::WriteAllText('+[char]34+$marker+[char]34+','+[char]34+'ran'+[char]34+')'
    $message=Failure-H {Invoke-BFProcess $shell @('-NoProfile','-Command',$code) $cancelProject '' $out 10 {$true}}
    Assert-H ($message-match'^BF_BLOCKED: cancelled before process dispatch'-and-not(Test-Path $marker)) 'Pre-dispatch cancellation started a process.'

    # Native stdin bytes are UTF-8 in both host PowerShell versions.
    $unicode=([string]([char[]]@(0x0422,0x0435,0x0441,0x0442)))+' '+[char]::ConvertFromUtf32(0x1F600)
    $readBytes='$s=[Console]::OpenStandardInput();$m=New-Object IO.MemoryStream;$s.CopyTo($m);[Console]::Out.Write([Convert]::ToBase64String($m.ToArray()))'
    $savedConsoleInputEncoding=[Console]::InputEncoding;$forcedBomEncoding=New-Object Text.UTF8Encoding($true);$forcedBomCodePage=$forcedBomEncoding.CodePage;$forcedBomPreamble=[Convert]::ToBase64String($forcedBomEncoding.GetPreamble())
    try {
        # Reproduce the UTF-8 system-locale default that gives PS5's hidden
        # StreamWriter a BOM preamble, without changing the machine locale.
        [Console]::InputEncoding=$forcedBomEncoding
        $out=Join-Path $testRoot 'unicode-stdin';$unicodeResult=Invoke-BFProcess $shell @('-NoProfile','-Command',$readBytes) $testRoot $unicode $out 10 {$false}
        Assert-H ([Console]::InputEncoding.CodePage-eq$forcedBomCodePage-and[Convert]::ToBase64String([Console]::InputEncoding.GetPreamble())-ceq$forcedBomPreamble) 'Native process setup did not restore Console.InputEncoding after start.'
        $failedStartOutput=Join-Path $testRoot 'unicode-stdin-failed-start'
        $failedStart=Failure-H {Invoke-BFProcess $shell @('-NoProfile','-Command',$readBytes) $shell $unicode $failedStartOutput 10 {$false}}
        Assert-H ($failedStart-ne'') 'Invalid working directory did not fail process start.'
        Assert-H ([Console]::InputEncoding.CodePage-eq$forcedBomCodePage-and[Convert]::ToBase64String([Console]::InputEncoding.GetPreamble())-ceq$forcedBomPreamble) 'Native process setup did not restore Console.InputEncoding after failed start.'
    } finally {[Console]::InputEncoding=$savedConsoleInputEncoding}
    $expected=[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($unicode));$observed=[IO.File]::ReadAllText($unicodeResult.stdout)
    Assert-H ($unicodeResult.exit_code-eq0-and$null-eq$unicodeResult.stop_reason-and$observed-ceq$expected) 'Native stdin bytes were not exact UTF-8.'

 # A child that never reads one megabyte of stdin must still time out.
 $out=Join-Path $testRoot 'stdin-out';$watch=[Diagnostics.Stopwatch]::StartNew()
 $process=Invoke-BFProcess $shell @('-NoProfile','-Command','Start-Sleep -Seconds 30') $testRoot ('x'*1048576) $out 1 {$false};$watch.Stop()
 Assert-H ($process.stop_reason-eq'timeout'-and$watch.Elapsed.TotalSeconds-lt12) 'Blocking stdin escaped the bounded timeout.'
 Assert-H ($null-eq(Get-Process -Id $process.process_id -ErrorAction SilentlyContinue)) 'Timed-out stdin process remained alive.'

 # Applicable Git filters are rejected before their command executes.
 $filterProject=New-Project-H $testRoot 'filter';$filter=Join-Path $filterProject 'filter.cmd';$filterMarker=Join-Path $filterProject 'filter.marker';$nl=[Environment]::NewLine;$q=[char]34
 Write-H $filter ('@echo off'+$nl+'> '+$q+$filterMarker+$q+' echo ran'+$nl+'more'+$nl);Write-H (Join-Path $filterProject '.gitattributes') ('*.txt filter=hardening'+$nl)
 [void](Invoke-BFGit $filterProject @('add','filter.cmd','.gitattributes'));[void](Invoke-BFGit $filterProject @('-c','user.name=BSL Flow Test','-c','user.email=test@example.invalid','commit','-m','Filter attributes'))
 [void](Invoke-BFGit $filterProject @('config','filter.hardening.clean',('cmd.exe /d /q /c '+$q+$filter+$q)))
 $filterRequest=New-Request-H;$message=Failure-H {Start-BFTask $filterProject $filterRequest}
 Assert-H ($message-match'^BF_BLOCKED: executable Git filter applies'-and-not(Test-Path $filterMarker)) 'Start executed or failed to reject an applicable Git filter.'
 Assert-H (-not(Test-Path (Join-Path $filterProject ('.bsl-flow\worktrees\'+$filterRequest.request_id)))) 'Start created a worktree after detecting a filter.'
    Write-Host ("Task hardening: $script:checks checks PASS on PowerShell $($PSVersionTable.PSVersion).")
}catch{$testFailure=$_}finally{
 $safe=[IO.Path]::GetFullPath($testRoot);$temp=[IO.Path]::GetFullPath([IO.Path]::GetTempPath())
 if($safe.StartsWith($temp,[StringComparison]::OrdinalIgnoreCase)-and(Test-Path $safe)){
  try{Remove-Item -LiteralPath $safe -Recurse -Force}catch{$cleanupFailure=$_}
 }
}
if($null-ne$testFailure){
 if($null-ne$cleanupFailure){throw ($testFailure.Exception.Message+' Cleanup also failed for owned fixture '+$testRoot+': '+$cleanupFailure.Exception.Message)}
 throw $testFailure
}
if($null-ne$cleanupFailure){throw $cleanupFailure}
