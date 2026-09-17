#Requires -Version 7.0
# Repository task registry concurrency suite: real child pwsh processes mutate
# one shared store. Opposite dependency edges (A→B and B→A) must produce at
# most one winner at the graph critical section, and concurrent creates must
# all publish without torn journals. Standalone, zero model/network calls.
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$registry = Join-Path $PackageRoot 'global\skills\1c-task\scripts\Task.Registry.ps1'
if (-not (Test-Path -LiteralPath $registry -PathType Leaf)) { throw "Missing registry module: $registry" }
. $registry
$entryPoint = Join-Path $PackageRoot 'global\skills\1c-task\scripts\Invoke-BSLFlowTask.ps1'

$script:checks = 0
function Assert-T {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "ASSERTION FAILED: $Message" }
    $script:checks++
}
function Write-TText { param([string]$Path, [string]$Text) [void][IO.Directory]::CreateDirectory((Split-Path $Path -Parent)); [IO.File]::WriteAllText($Path, $Text, [Text.UTF8Encoding]::new($false)) }

function Invoke-TGit {
    $output = & git @args 2>&1
    if ($LASTEXITCODE -ne 0) { throw "git @args failed: $(@($output) -join ' ')" }
    return (@($output | Where-Object { $_ -isnot [System.Management.Automation.ErrorRecord] }) -join "`n").Trim()
}

function New-TClone {
    param([string]$Path)
    [void][IO.Directory]::CreateDirectory($Path)
    Invoke-TGit init -q $Path | Out-Null
    Write-TText (Join-Path $Path 'README.md') "fixture`n"
    Invoke-TGit -C $Path add README.md | Out-Null
    Invoke-TGit -C $Path -c user.name='BSL Flow Test' -c user.email='test@example.invalid' commit -q -m Fixture | Out-Null
    return $Path
}

function New-TWorktree {
    param([string]$Repo, [string]$Path, [string]$Name)
    Invoke-TGit -C $Repo worktree add -q -b $Name $Path | Out-Null
    return $Path
}

function Start-TChild {
    # Starts one real pwsh child running the packaged entry point against the
    # shared store; output goes to files so nothing is lost on contention.
    # Start-Process would not quote array items, and the package root contains
    # spaces, so the child command line is built as one verbatim string.
    param([string]$StdOutPath, [string]$StdErrPath, [string[]]$Arguments)
    $pwshPath = Join-Path $PSHOME 'pwsh.exe'
    $quotedArguments = (@($Arguments | ForEach-Object { '"' + ($_ -replace '"', '""') + '"' }) -join ' ')
    $commandLine = '-NoProfile -NoLogo -NonInteractive -File "' + $entryPoint + '" ' + $quotedArguments
    return Start-Process -FilePath $pwshPath -ArgumentList $commandLine -RedirectStandardOutput $StdOutPath -RedirectStandardError $StdErrPath -PassThru -WindowStyle Hidden
}

function Wait-TChild {
    param($Process, [int]$TimeoutSeconds = 180)
    if (-not $Process.WaitForExit($TimeoutSeconds * 1000)) { $Process.Kill(); throw 'child pwsh process timed out.' }
    return [pscustomobject]@{ ExitCode = $Process.ExitCode }
}

function Read-TFile {
    param([string]$Path)
    if (-not [IO.File]::Exists($Path)) { return '' }
    return [IO.File]::ReadAllText($Path)
}

function Assert-TStoreHealthy {
    param([string]$TasksRoot, [string[]]$ExpectedTaskIds)
    foreach ($taskId in $ExpectedTaskIds) {
        $state = Read-BFRegistryTaskState -TasksRoot $TasksRoot -TaskId $taskId
        Assert-T ($null -ne $state -and $state['health'] -ceq 'ok') "journal of $taskId must stay healthy after concurrent writes ($($state['diagnostic']))."
        Assert-T (@($state['revisions']).Count -eq [int]$state['revision']) "journal of $taskId lost revision/file agreement."
    }
    foreach ($taskId in $ExpectedTaskIds) {
        $revisionDirectory = Join-Path (Join-Path $TasksRoot $taskId) 'revisions'
        if ([IO.Directory]::Exists($revisionDirectory)) {
            $leftovers = @(Get-ChildItem -LiteralPath $revisionDirectory -File -Force | Where-Object { $_.Name -cmatch '\.tmp$' })
            Assert-T ($leftovers.Count -eq 0) "temporary revision files were left behind for $taskId."
        }
    }
}

$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-task-registry-conc-' + [guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($testRoot)
try {
    $repo = New-TClone (Join-Path $testRoot 'clone')
    $wtA = New-TWorktree $repo (Join-Path $testRoot 'wt-a') 'conc-a'
    $wtB = New-TWorktree $repo (Join-Path $testRoot 'wt-b') 'conc-b'
    $store = Get-BFRegistryStore -ProjectRoot $repo

    function New-TRegistryTask {
        param([string]$Project, [string]$Title)
        $result = Invoke-BFRegistryCommand -Action Create -ProjectPath $Project -Title $Title -Format Json
        if ($result.ExitCode -ne 0) { throw "fixture create failed: $($result.StdErr)" }
        return ((Invoke-TJson $result.StdOut).task_id)
    }
    function Invoke-TJson { param([string]$Text) return ($Text | ConvertFrom-Json) }

    # --- Scenario 1: opposite edges A→B and B→A from two child processes --------------------------------
    $taskA = New-TRegistryTask -Project $wtA -Title 'Node A'
    $taskB = New-TRegistryTask -Project $wtB -Title 'Node B'

    $scratch = Join-Path $testRoot 'scratch'
    [void][IO.Directory]::CreateDirectory($scratch)
    $childOne = Start-TChild -StdOutPath (Join-Path $scratch 'a.out') -StdErrPath (Join-Path $scratch 'a.err') -Arguments @('-Action', 'EditRegistry', '-ProjectPath', $wtA, '-TaskId', $taskA, '-ExpectedRevision', '1', '-DependsOn', $taskB, '-Title', 'Node A edge', '-Format', 'Json')
    $childTwo = Start-TChild -StdOutPath (Join-Path $scratch 'b.out') -StdErrPath (Join-Path $scratch 'b.err') -Arguments @('-Action', 'EditRegistry', '-ProjectPath', $wtB, '-TaskId', $taskB, '-ExpectedRevision', '1', '-DependsOn', $taskA, '-Title', 'Node B edge', '-Format', 'Json')
    $oneResult = Wait-TChild $childOne
    $twoResult = Wait-TChild $childTwo

    $exitCodes = @($oneResult.ExitCode, $twoResult.ExitCode) | Sort-Object
    Assert-T ($exitCodes[0] -eq 0 -and $exitCodes[1] -eq 11) "exactly one opposite-edge writer must win (0) and one must get 11; observed: $($oneResult.ExitCode), $($twoResult.ExitCode)."
    $winnerOutput = if ($oneResult.ExitCode -eq 0) { Read-TFile (Join-Path $scratch 'a.out') } else { Read-TFile (Join-Path $scratch 'b.out') }
    $loserOutput = if ($oneResult.ExitCode -eq 0) { (Read-TFile (Join-Path $scratch 'a.err')) + (Read-TFile (Join-Path $scratch 'b.out')) + (Read-TFile (Join-Path $scratch 'b.err')) } else { (Read-TFile (Join-Path $scratch 'b.err')) + (Read-TFile (Join-Path $scratch 'a.out')) + (Read-TFile (Join-Path $scratch 'a.err')) }
    Assert-T ($winnerOutput -match '"revision":"[0-9a-f]{64}"') 'the winning writer appended exactly one revision and reports the revision hash.'
    Assert-T ($loserOutput -cmatch '"class":"BF_(CONFLICT|BLOCKED)"') 'the losing writer must receive a BF_CONFLICT/BF_BLOCKED-class error document.'

    $stateA = Read-BFRegistryTaskState -TasksRoot $store.TasksRoot -TaskId $taskA
    $stateB = Read-BFRegistryTaskState -TasksRoot $store.TasksRoot -TaskId $taskB
    $revisionsA = @($stateA['revisions']).Count
    $revisionsB = @($stateB['revisions']).Count
    Assert-T (($revisionsA -eq 2 -and $revisionsB -eq 1) -or ($revisionsA -eq 1 -and $revisionsB -eq 2)) "no torn state: exactly one journal holds two revisions (A=$revisionsA, B=$revisionsB)."
    Assert-T (($stateA['health'] -ceq 'ok') -and ($stateB['health'] -ceq 'ok')) 'both journals must remain valid and chain-verified.'
    if ($revisionsA -eq 2) { Assert-T (@(Get-BFObjectProperty $stateA['metadata'] 'depends_on') -ccontains $taskB) 'the winner edge A→B must be stored.' }
    else { Assert-T (@(Get-BFObjectProperty $stateB['metadata'] 'depends_on') -ccontains $taskA) 'the winner edge B→A must be stored.' }
    Assert-T ([IO.File]::Exists((Join-Path $store.TasksRoot 'bsl-flow-graph.lock'))) 'the repository graph lock file must exist in the store root.'
    Assert-TStoreHealthy -TasksRoot $store.TasksRoot -ExpectedTaskIds @($taskA, $taskB)

    # --- Scenario 2: concurrent creates from three child processes --------------------------------------
    $before = @((Invoke-TJson (Invoke-BFRegistryCommand -Action List -ProjectPath $repo -Limit 200 -Format Json).StdOut).tasks).Count
    $children = @()
    for ($index = 1; $index -le 3; $index++) {
        $children += Start-TChild -StdOutPath (Join-Path $scratch "c$index.out") -StdErrPath (Join-Path $scratch "c$index.err") -Arguments @('-Action', 'Create', '-ProjectPath', ((@($wtA, $wtB, $repo))[$index - 1]), '-Title', "Concurrent $index", '-Format', 'Json')
    }
    $index = 0
    foreach ($child in $children) {
        $index++
        $result = Wait-TChild $child
        Assert-T ($result.ExitCode -eq 0) "concurrent create $index must succeed, got $($result.ExitCode): $(Read-TFile (Join-Path $scratch "c$index.err"))"
    }
    $afterList = Invoke-TJson (Invoke-BFRegistryCommand -Action List -ProjectPath $repo -Limit 200 -Format Json).StdOut
    Assert-T (@($afterList.tasks).Count -eq ($before + 3)) 'all three concurrent creates must publish distinct tasks.'
    $allIds = @(@($afterList.tasks) | ForEach-Object { $_.task_id })
    Assert-T (@($allIds | Sort-Object -Unique).Count -eq @($allIds).Count) 'concurrent creates must never reuse a task UUID.'
    Assert-TStoreHealthy -TasksRoot $store.TasksRoot -ExpectedTaskIds $allIds

    # Registry store stays out of Git status under concurrent load.
    Assert-T ([string]::IsNullOrEmpty((Invoke-TGit -C $wtA status --porcelain))) 'registry store must not appear in worktree Git status.'

    Write-Host ("Task registry concurrency suite passed ({0} checks) on PowerShell {1}." -f $script:checks, $PSVersionTable.PSVersion)
}
finally {
    $fullTestRoot = [IO.Path]::GetFullPath($testRoot)
    $tempParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar)
    $expectedPrefix = $tempParent + [IO.Path]::DirectorySeparatorChar + 'bsl-flow-task-registry-conc-'
    if (-not $fullTestRoot.StartsWith($expectedPrefix, [StringComparison]::OrdinalIgnoreCase) -or [IO.Path]::GetDirectoryName($fullTestRoot).TrimEnd([IO.Path]::DirectorySeparatorChar) -ne $tempParent) {
        throw "Unsafe registry concurrency test cleanup target: $fullTestRoot"
    }
    git worktree prune 2>$null | Out-Null
    if (Test-Path -LiteralPath $fullTestRoot) { Remove-Item -LiteralPath $fullTestRoot -Recurse -Force }
}
