#Requires -Version 7.0
# BFI-005 offline contract: durable reservation, cumulative ledger, unknown
# cost and admission control. No model or provider process is launched.
[CmdletBinding()]param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$core=Join-Path $PackageRoot 'global/skills/1c-task/scripts'
foreach($name in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Process.ps1','Task.Gates.ps1','Task.Engine.ps1','Task.Stages.ps1')){. (Join-Path $core $name)}
$script:checks=0
function Assert-B([bool]$Condition,[string]$Message){if(-not $Condition){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Failure-B([scriptblock]$Action){try{& $Action|Out-Null;return ''}catch{return $_.Exception.Message}}
function Write-B([string]$Path,[string]$Text){[void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent));[IO.File]::WriteAllText($Path,$Text,[Text.UTF8Encoding]::new($false))}
function New-Dispatch([string]$TaskDirectory,[string]$Name){$directory=Join-Path $TaskDirectory ('attempts/'+$Name+'/raw/worker');[void][IO.Directory]::CreateDirectory($directory);return $directory}
function Write-Receipt([string]$Directory,[string]$Json){Write-B (Join-Path $Directory 'host-result.json') $Json}
function New-BudgetState([string]$Project,[string]$TaskId,[double]$Limit,[double]$Reservation){
    [pscustomobject]@{project_path=$Project;task_id=$TaskId;request=[pscustomobject]@{models=[pscustomobject]@{worker='deepseek/deepseek-v4-flash';reviewer='deepseek/deepseek-v4-flash'};execution_profile=[pscustomobject]@{provider='opencode'};budget=[pscustomobject]@{currency='USD';limit=$Limit;reservation=$Reservation}}}
}
$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-budget-gate-'+[guid]::NewGuid().ToString('N'))
$project=Join-Path $testRoot 'project'
$taskId=[guid]::NewGuid().ToString()
$taskDirectory=Join-Path $project ('.bsl-flow/tasks/'+$taskId)
[void][IO.Directory]::CreateDirectory($taskDirectory)
# The storage fence verifies task-path writes against a Git worktree root;
# give the fixture project a repository like every real task project has.
$null=& git -C $project init 2>&1
if($LASTEXITCODE -ne 0){throw 'budget gate fixture repository was not created.'}
try {
    $state=New-BudgetState $project $taskId 10.0 0.05
    $ledgerPath=Get-BFBudgetLedgerPath $state

    # A. Empty ledger is a valid zero state.
    $summary=Get-BFBudgetSummary $state
    Assert-B ($summary.spent_usd -eq 0 -and $summary.open -eq 0 -and $summary.unknown -eq 0) 'Empty ledger was not zero.'
    Assert-B (-not (Test-Path -LiteralPath $ledgerPath)) 'Empty ledger should not create a file before the first entry.'

    # B. Admission passes, reservation makes dispatch open and blocks the next one.
    $d1=New-Dispatch $taskDirectory 'a1'
    Assert-B ((Failure-B {Assert-BFBudgetAdmission $state $d1}) -eq '') 'First admission was rejected.'
    Add-BFBudgetReservation $state $d1 'opencode' 'implement' 'deepseek/deepseek-v4-flash'
    $summary=Get-BFBudgetSummary $state
    Assert-B ($summary.reservations -eq 1 -and $summary.open -eq 1) 'Reservation was not durable before dispatch.'
    $d2=New-Dispatch $taskDirectory 'a2'
    Assert-B ((Failure-B {Assert-BFBudgetAdmission $state $d2}) -match 'unresolved paid dispatch') 'Open reservation did not block the next dispatch.'
    Add-BFBudgetReservation $state $d1 'opencode' 'implement' 'deepseek/deepseek-v4-flash'
    Assert-B ((Get-BFBudgetSummary $state).reservations -eq 1) 'Duplicate reservation was recorded twice.'

    # C. A completed receipt resolves the reservation exactly once.
    Write-Receipt $d1 '{"session_id":"ses_1","usage":{"total":10},"reported_cost_usd":0.25,"observed_model":null}'
    Complete-BFBudgetDispatch $state $d1 'opencode' 'implement' 'deepseek/deepseek-v4-flash'
    Complete-BFBudgetDispatch $state $d1 'opencode' 'implement' 'deepseek/deepseek-v4-flash'
    $summary=Get-BFBudgetSummary $state
    Assert-B ($summary.spent_usd -eq 0.25 -and $summary.known -eq 1 -and $summary.open -eq 0) 'Resolved outcome was missing or double counted.'
    Assert-B ((Failure-B {Assert-BFBudgetAdmission $state $d2}) -eq '') 'Replay blocked a dispatch after a known outcome.'
    $restored=Get-BFBudgetSummary $state
    Assert-B ((Get-BFHash $restored) -ceq (Get-BFHash $summary)) 'Ledger summary was not reproducible.'

    # D. A crash leaves the reservation open and no later dispatch proceeds.
    #    A separate task isolates the unknown-cost route from the main ledger.
    $lunaId=[guid]::NewGuid().ToString()
    $lunaDirectory=Join-Path $project ('.bsl-flow/tasks/'+$lunaId);[void][IO.Directory]::CreateDirectory($lunaDirectory)
    $lunaState=New-BudgetState $project $lunaId 10.0 0.05
    $d3=New-Dispatch $lunaDirectory 'a3'
    Add-BFBudgetReservation $lunaState $d3 'codex' 'inspect' 'gpt-5.6-luna'
    Assert-B ((Failure-B {Assert-BFBudgetAdmission $lunaState (New-Dispatch $lunaDirectory 'a3b')}) -match 'unresolved paid dispatch') 'Crash reservation did not block.'

    # E. A recovered receipt without a reported cost stays unknown and blocks a
    #    monetary budget, while a no-limit route records usage without inventing USD.
    Write-Receipt $d3 '{"session_id":"ses_3","usage":{"input_tokens":73},"observed_model":null}'
    $lunaState.request.budget.limit=$null;$lunaState.request.budget.reservation=0
    Assert-B ((Failure-B {Assert-BFBudgetAdmission $lunaState (New-Dispatch $lunaDirectory 'a4')}) -eq '') 'No-limit route rejected an unknown-cost receipt.'
    $summary=Get-BFBudgetSummary $lunaState
    Assert-B ($summary.unknown -eq 1 -and $summary.spent_usd -eq 0) 'Unknown usage outcome was not retained without invented cost.'
    $lunaState.request.budget.limit=10.0
    Assert-B ((Failure-B {Assert-BFBudgetAdmission $lunaState (New-Dispatch $lunaDirectory 'a5')}) -match 'unknown cost') 'Unknown prior cost did not block a monetary budget.'

    # F. Exhaustion is checked before the model call.
    $state.request.budget.limit=0.30;$state.request.budget.reservation=0.10
    Assert-B ((Failure-B {Assert-BFBudgetAdmission $state (New-Dispatch $taskDirectory 'a6')}) -match 'budget limit would be exceeded') 'Insufficient remaining budget accepted.'
    $state.request.budget.reservation=0.05
    Assert-B ((Failure-B {Assert-BFBudgetAdmission $state (New-Dispatch $taskDirectory 'a7')}) -eq '') 'Reservation within the limit was rejected.'
    $state.request.budget.limit=10.0;$state.request.budget.reservation=0.05

    # G. Conflicting outcome for the same dispatch is refused, not averaged.
    Write-Receipt $d1 '{"session_id":"ses_1","usage":{"total":10},"reported_cost_usd":9.99,"observed_model":null}'
    Assert-B ((Failure-B {Complete-BFBudgetDispatch $state $d1 'opencode' 'implement' 'deepseek/deepseek-v4-flash'}) -match 'conflicting budget outcome') 'Conflicting outcome accepted.'
    Write-Receipt $d1 '{"session_id":"ses_1","usage":{"total":10},"reported_cost_usd":0.25,"observed_model":null}'

    # H. The shared managed path reserves before dispatch and records after it.
    #    A no-limit route isolates this from the retained unknown-cost outcome.
    $state.request.budget.limit=$null;$state.request.budget.reservation=0
    & {
        function Test-BFRuntimePreflight {param($State,$Directory);$null}
        function Invoke-BFCodexWorker {throw 'unexpected legacy worker'}
        function Invoke-BFProfiledCodexWorker {param($State,$Stage,$Prompt,$Directory,$CodexPath,$Cancelled,$MaxOutputBytes);throw 'unexpected codex worker'}
        function Invoke-BFOpenCodeWorker {param($State,$Stage,$Prompt,$Directory,$CodexPath,$Cancelled,$MaxOutputBytes);Write-Receipt $Directory ('{"session_id":"ses_h","usage":{"total":5},"reported_cost_usd":'+$script:hostCost.ToString([Globalization.CultureInfo]::InvariantCulture)+'}');[pscustomobject]@{status='completed'}}
        $script:hostCost=0.40
        $spentBefore=(Get-BFBudgetSummary $state).spent_usd
        $runDirectory=Join-Path $taskDirectory 'attempts/integration/raw/worker';[void][IO.Directory]::CreateDirectory($runDirectory)
        $result=Invoke-BFManagedWorker $state 'implement' 'prompt' $runDirectory $runDirectory $null
        Assert-B ($result.status -ceq 'completed') 'Managed worker integration did not complete.'
        $summary=Get-BFBudgetSummary $state
        Assert-B ($summary.spent_usd -eq ($spentBefore+0.40) -and $summary.open -eq 0) 'Managed dispatch spend was not recorded exactly once.'
        $replay=Invoke-BFManagedWorker $state 'implement' 'prompt' $runDirectory $runDirectory $null
        Assert-B ((Get-BFBudgetSummary $state).spent_usd -eq ($spentBefore+0.40)) 'Managed replay added cost twice.'
        function Invoke-BFOpenCodeWorker {param($State,$Stage,$Prompt,$Directory,$CodexPath,$Cancelled,$MaxOutputBytes);throw 'provider failed'}
        $failedDirectory=Join-Path $taskDirectory 'attempts/failed/raw/worker';[void][IO.Directory]::CreateDirectory($failedDirectory)
        Assert-B ((Failure-B {Invoke-BFManagedWorker $state 'implement' 'prompt' $failedDirectory $failedDirectory $null}) -match 'provider failed') 'Provider failure did not surface.'
        $summary=Get-BFBudgetSummary $state
        Assert-B ($summary.open -eq 1) 'Failed provider left no open reservation.'
        Assert-B ((Failure-B {Assert-BFBudgetAdmission $state (New-Dispatch $taskDirectory 'blocked-after-failure')}) -match 'unresolved paid dispatch') 'Failed provider did not block the next dispatch.'
    }

    Write-Output "TASK_BUDGET_GATE_OK checks=$script:checks; model/sandbox/database processes=0"
} finally {
    $resolved=[IO.Path]::GetFullPath($testRoot)
    $temp=[IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\','/')+[IO.Path]::DirectorySeparatorChar
    if(-not $resolved.StartsWith($temp,[StringComparison]::OrdinalIgnoreCase) -or (Split-Path $resolved -Leaf) -notlike 'bsl-flow-budget-gate-*'){throw 'Unsafe test cleanup target.'}
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
