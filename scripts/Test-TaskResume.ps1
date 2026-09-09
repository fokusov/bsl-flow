[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if (-not $PackageRoot) { $PackageRoot = Split-Path $PSScriptRoot -Parent }
foreach ($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1')) {
    . (Join-Path $PackageRoot ('global/skills/1c-task/scripts/' + $name))
}
. (Join-Path $PackageRoot 'global/skills/1c-task/adapters/Codex.ps1')
$script:checks = 0
$script:dispatches = 0
function Assert-R([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "ASSERTION FAILED: $Message" }
    $script:checks++
}
function Write-R([string]$Path, [string]$Text) {
    [void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent))
    [IO.File]::WriteAllText($Path, $Text, (New-Object Text.UTF8Encoding($false)))
}
function Failure-R([scriptblock]$Action) {
    try { & $Action | Out-Null; return '' } catch { return $_.Exception.Message }
}
function Save-CachedWorker-R($Directory, $Result, [string]$Model, [string]$Effort) {
    [void][IO.Directory]::CreateDirectory($Directory)
    $stdout = Join-Path $Directory 'stdout.txt'
    $session = [guid]::NewGuid().ToString()
    $events = @(
        Get-BFCanonicalJson ([ordered]@{type='thread.started';thread_id=$session})
        Get-BFCanonicalJson ([ordered]@{type='turn.completed';usage=$null})
    )
    Write-R $stdout (($events -join "`n") + "`n")
    Write-BFJson -Path (Join-Path $Directory 'exit.json') -Value ([ordered]@{exit_code=0;stop_reason=$null;stdout=$stdout})
    Write-BFJson -Path (Join-Path $Directory 'model-result.json') -Value $Result
    Write-BFJson -Path (Join-Path $Directory 'host-result.json') -Value ([ordered]@{
        session_id=$session;requested_model=$Model;requested_effort=$Effort;observed_model=$null;observed_effort=$null;usage=$null;usage_source=$stdout
    })
}

$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-resume-' + [guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($testRoot)
$testFailure = $null
try {
    $project = Join-Path $testRoot 'project'
    [void][IO.Directory]::CreateDirectory($project)
    [void](Invoke-BFGit $project @('init'))
    Write-R (Join-Path $project 'hello.txt') "Initial greeting`n"
    Write-R (Join-Path $project '.gitignore') ".bsl-flow/`nopenspec/changes/`n"
    [void](Invoke-BFGit $project @('add','.'))
    [void](Invoke-BFGit $project @('-c','user.name=BSL Flow Test','-c','user.email=test@example.invalid','commit','-m','Resume fixture'))
    $request = [pscustomobject]@{
        schema_version=1;request_id=[guid]::NewGuid().ToString();prompt='Add Example greeting.';mode='implement';analysis_goal='analysis';complexity='S';risk='low';impact_flags=@();require_code_review=$true
        criteria=@([pscustomobject]@{id='greeting';observation='Greeting is present.';kind='file_assertion';path='hello.txt';contains='Example greeting'})
        provenance=[pscustomobject]@{source='user';reference='resume-fixture';text='Implement the fixture.'}
        models=[pscustomobject]@{worker='gpt-6-astra';worker_effort='medium';reviewer='gpt-6-astra';reviewer_effort='high'}
    }
    $task = Start-BFTask $project $request
    $executor = {
        param($run)
        if ($run.attempt.stage -eq 'inspect') {
            $payload = Get-BFCanonicalJson ([ordered]@{complexity='S';risk='low';impact_flags=@();rationale='Only a greeting changes.'})
        } elseif ($run.attempt.stage -eq 'implement') {
            Write-R (Join-Path $run.state.worker_path 'hello.txt') "Example greeting`n"
            $payload = '{"changed_files":["hello.txt"]}'
        } else { throw 'Unexpected setup stage.' }
        return [ordered]@{schema_version=1;status='completed';summary='Fixture completed.';payload_json=$payload}
    }
    foreach ($stage in @('inspect','implement')) {
        $run = New-BFAttempt $project $task.task_id ''
        Assert-R ($run.attempt.stage -eq $stage) 'Setup followed the declared route.'
        $task = Invoke-BFStage $run '' $executor
    }
    $run = New-BFAttempt $project $task.task_id ''
    Assert-R ($run.attempt.stage -eq 'code_review') 'A registered critic attempt is required.'
    $critic = [ordered]@{schema_version=1;status='completed';summary='Review has one finding.';payload_json=(Get-BFCanonicalJson ([ordered]@{
        verdict='REVISE';findings=@([ordered]@{id='F1';severity='low';file='hello.txt';line=1;scenario='The greeting might be missing.';evidence='Check the first source line.'})
    }))}
    $reconciler = [ordered]@{schema_version=1;status='completed';summary='The finding is disproved by the actual source.';payload_json=(Get-BFCanonicalJson ([ordered]@{
        decisions=@([ordered]@{finding_id='F1';decision='rejected';reason='The requested greeting is already present.';evidence='hello.txt line 1 contains Example greeting.'});fix_instructions=''
    }))}
    $workerDir = Join-Path $run.directory 'raw/worker'
    $reconcilerDir = Join-Path $run.directory 'raw/reconciler'
    Save-CachedWorker-R $workerDir $critic $request.models.reviewer $request.models.reviewer_effort
    Save-CachedWorker-R $reconcilerDir $reconciler $request.models.worker $request.models.worker_effort
    $savedHash = Get-BFFileHash (Join-Path $reconcilerDir 'host-result.json')
    # Any accidental second dispatch fails without starting a native process/model.
    function Invoke-BFProcess { $script:dispatches++; throw 'Unexpected native dispatch during cached recovery.' }
    # This offline fixture uses the user's temp directory; ancestor host config is
    # irrelevant to cache parsing, and every real process launch is forbidden above.
    function Assert-BFWorkerConfiguration { param([string]$WorkerPath) }
    $message = Failure-R { Resume-BFAttempt $project $task.task_id }
    Assert-R ($message -match 'controller is still running' -and $script:dispatches -eq 0) 'Resume raced a still-live attempt controller.'
    # Simulate the original controller crashing after both raw workers completed.
    $run.attempt.controller_process = [ordered]@{pid=2147483647;start_time_utc='2000-01-01T00:00:00.0000000Z'}
    Write-R (Join-Path $run.directory 'start.json') (Get-BFCanonicalJson $run.attempt)
    $task = Resume-BFAttempt $project $task.task_id
    Assert-R ($script:dispatches -eq 0) 'Resume dispatched a second critic or reconciler.'
    Assert-R ($task.status -eq 'ready' -and $null -eq $task.active_attempt) ('Cached critique/reconciliation was not recorded: ' + ($task.blockers -join '; '))
    Assert-R ($task.evidence[-1].stage -eq 'code_review' -and $task.evidence[-1].outcome -eq 'PASS') 'Rejected finding did not produce the reconciled review result.'
    Assert-R ((Get-BFFileHash (Join-Path $reconcilerDir 'host-result.json')) -eq $savedHash) 'Recovery overwrote cached host evidence.'
    $again = Resume-BFAttempt $project $task.task_id
    Assert-R ($again.revision -eq $task.revision -and $script:dispatches -eq 0) 'Repeated Resume was not idempotent.'
    $different = Read-BFJson (Join-Path $reconcilerDir 'host-result.json')
    $different.requested_effort = 'high'
    Write-R (Join-Path $reconcilerDir 'host-result.json') (Get-BFCanonicalJson $different)
    $message = Failure-R { Invoke-BFCodexWorker $task 'code_reconcile' 'Unused cached prompt.' $reconcilerDir '' $null }
    Assert-R ($message -match 'cached worker model/effort differs' -and $script:dispatches -eq 0) 'Wrong cached model/effort was reused or redispatched.'

    # Required spec output remains fresh only for its exact bytes, or one current
    # registered draft-to-final review transformation linked to those draft bytes.
    $change = Get-BFChangePath $task
    Write-R (Join-Path $change 'original-task.md') $task.request.prompt
    Write-R (Join-Path $change 'spec.md') 'Draft specification'
    $spec = [pscustomobject]@{stage='spec';outcome='PASS';dependencies=(Get-BFDependencies $task 'spec' $null);raw_hashes=@()}
    Assert-R (Test-BFEvidenceFresh $task $spec $null) 'Exact emitted spec is stale.'
    Remove-Item -LiteralPath (Join-Path $change 'spec.md')
    Assert-R (-not (Test-BFEvidenceFresh $task $spec $null)) 'Deleted required spec remained fresh.'
    Write-R (Join-Path $change 'spec.md') 'Unregistered replacement'
    Assert-R (-not (Test-BFEvidenceFresh $task $spec $null)) 'Unregistered replacement spec remained fresh.'
    $reviewId = [guid]::NewGuid().ToString()
    $startPath = Join-Path (Get-BFTaskDirectory $project $task.task_id) ('attempts/' + $reviewId + '/start.json')
    Write-BFJson -Path $startPath -Value ([ordered]@{dependencies=[ordered]@{spec=$spec.dependencies.spec}})
    foreach ($name in @('review.json','review-reconciliation.json','final-validation.json')) { Write-R (Join-Path $change $name) '{}' }
    $review = [pscustomobject]@{stage='spec_review';attempt_id=$reviewId;outcome='PASS';dependencies=(Get-BFDependencies $task 'spec_review' $null);raw_hashes=@()}
    $task.evidence = @($review)
    Assert-R (Test-BFEvidenceFresh $task $spec $null) 'Registered current draft-to-final binding was rejected.'
    Write-R $startPath (Get-BFCanonicalJson ([ordered]@{dependencies=[ordered]@{spec=('0'*64)}}))
    Assert-R (-not (Test-BFEvidenceFresh $task $spec $null)) 'Review of another draft authorized this spec.'
    Write-R $startPath (Get-BFCanonicalJson ([ordered]@{dependencies=[ordered]@{spec=$spec.dependencies.spec}}))
    Write-R (Join-Path $change 'review-reconciliation.json') '{"changed":true}'
    Assert-R (-not (Test-BFEvidenceFresh $task $spec $null)) 'Changed reconciliation sidecar retained spec freshness.'
    Write-Host "Task resume: $script:checks checks PASS. No native/model dispatch."
} catch { $testFailure = $_ } finally {
    $safe = [IO.Path]::GetFullPath($testRoot)
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\','/') + [IO.Path]::DirectorySeparatorChar
    if ($safe.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -and (Test-Path -LiteralPath $safe)) {
        try { Remove-Item -LiteralPath $safe -Recurse -Force } catch { if ($null -eq $testFailure) { $testFailure = $_ } }
    }
}
if ($null -ne $testFailure) { throw $testFailure }
