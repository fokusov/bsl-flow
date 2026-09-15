# Freezes the legacy runner supervision decisions (Get-BFRunnerDecision,
# Task.Runner.ps1) over the frozen case set of runner-decide-cases.json.
# Each case stages one legacy v1 task journal in the temporary capture
# project through Save-BFTask, then computes the decision read-only. The
# staging writes touch only the temporary fixture tree; no lifecycle stage
# is executed and no process is spawned.
param([Parameter(Mandatory)][string]$ScriptsRoot,[Parameter(Mandatory)][string]$ProjectPath,[Parameter(Mandatory)][string]$CasesPath,[Parameter(Mandatory)][string]$OutPath)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
foreach ($module in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Memory.ps1','Task.Architecture.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Runner.ps1')) {
    . (Join-Path $ScriptsRoot $module)
}
$project = (Resolve-Path -LiteralPath $ProjectPath).Path
$policyHash = Get-BFHash (Get-BFPolicyFiles $project)

# ConvertFrom-Json parses ISO date literals into [DateTime], which the state
# contract and Write-BFJson reject; deep-restringify them in the exact
# round-trip form so the frozen bytes reach Save-BFTask unchanged.
function Convert-BFFrozenDates {
    param($Value)
    if ($Value -is [datetime]) { return $Value.ToUniversalTime().ToString('o') }
    if ($Value -is [System.Collections.IDictionary]) {
        $clone = [ordered]@{}
        foreach ($key in @($Value.Keys)) { $clone[[string]$key] = Convert-BFFrozenDates $Value[$key] }
        return $clone
    }
    if ($Value -is [System.Collections.IList]) {
        $items = @()
        foreach ($item in @($Value)) { $items += @(Convert-BFFrozenDates $item) }
        # The comma operator keeps empty arrays from unrolling to $null.
        return , $items
    }
    return $Value
}
$self = Get-Process -Id $PID
$selfIdentity = [ordered]@{ pid = $PID; start_time_utc = $self.StartTime.ToUniversalTime().ToString('o') }
# The symbolic identity recorded in the derived snapshot: the decision
# depends on liveness, not on the identity bytes, which are capture-process
# state and therefore must not enter the frozen input.
$symbolicIdentity = [ordered]@{ pid = 4711; start_time_utc = '2026-01-01T00:00:00.0000000Z' }

$results = @()
$cases = (Get-Content -Raw -LiteralPath $CasesPath | ConvertFrom-Json -AsHashtable).cases
foreach ($case in $cases) {
    # Re-read the case file so every iteration owns a fresh state object.
    $fresh = Get-Content -Raw -LiteralPath $CasesPath | ConvertFrom-Json -AsHashtable
    $entry = @($fresh.cases) | Where-Object { $_.name -ceq $case.name } | Select-Object -First 1
    $state = Convert-BFFrozenDates $entry.state
    $state.project_path = $project
    $state.policy_hash = $policyHash
    $taskDirectory = Get-BFTaskDirectory $project ([string]$state.task_id)
    $lock = Enter-BFLock $taskDirectory
    try { [void](Save-BFTask $state 0) } finally { $lock.Dispose() }

    $start = $entry.start
    $controllerAlive = $false
    $attemptStage = $null
    $snapshotController = $null
    if ($null -ne $start) {
        $attemptDirectory = Join-Path $taskDirectory ('attempts/' + [string]$state.active_attempt)
        [void][System.IO.Directory]::CreateDirectory($attemptDirectory)
        $startDocument = [ordered]@{ schema_version = 1; stage = [string]$start.stage; controller_process = $null }
        if ('self' -ceq [string]$start.controller) {
            $startDocument.controller_process = $selfIdentity
            $controllerAlive = $true
            $snapshotController = $symbolicIdentity
        }
        Write-BFJson -Path (Join-Path $attemptDirectory 'start.json') -Value $startDocument
        $attemptStage = [string]$start.stage
    }

    $decision = Get-BFRunnerDecision $project ([string]$state.task_id)
    $readState = $decision.state
    $next = Get-BFNext $readState
    $acceptanceStale = $false
    if ($readState.status -ceq 'completed') {
        $effective = New-BFEnvelope $readState ([string]$next.action) @($next.blockers) ([string]$next.stage)
        $acceptanceStale = ([string]$effective.status -cne 'completed')
    }
    $snapshot = [ordered]@{
        name = [string]$entry.name
        status = [string]$readState.status
        stage = [string]$readState.stage
        revision = [int]$readState.revision
        active_attempt = if ($null -ne $readState.active_attempt) { [string]$readState.active_attempt } else { $null }
        unresolved_effect = ($null -ne $readState.unresolved_effect)
        next_action = [string]$next.action
        acceptance_stale = $acceptanceStale
        attempt_stage = $attemptStage
        controller = $snapshotController
        controller_alive = $controllerAlive
        owned_processes = @()
    }
    $results += [ordered]@{ name = [string]$entry.name; snapshot = $snapshot; action = [string]$decision.action }
}
[IO.File]::WriteAllText($OutPath, (ConvertTo-Json -InputObject @{ schema_version = 1; cases = $results } -Depth 8), [Text.UTF8Encoding]::new($false))
