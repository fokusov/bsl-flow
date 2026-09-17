#Requires -Version 7.0
# Repository task registry suite: store identity across worktrees, journal
# validation, metadata constants, dependency cycles, list filters/sort/cursors,
# corruption/legacy taxonomy, staged activation (trusted request schema v1),
# run/next guard, thin CLI wrapper, output contract, and redaction.
# Standalone (no Pester), zero model/network calls.
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$registry = Join-Path $PackageRoot 'global\skills\1c-task\scripts\Task.Registry.ps1'
if (-not (Test-Path -LiteralPath $registry -PathType Leaf)) { throw "Missing registry module: $registry" }
. $registry
$entryPoint = Join-Path $PackageRoot 'global\skills\1c-task\scripts\Invoke-BSLFlowTask.ps1'
$wrapper = Join-Path $PackageRoot 'scripts\bsl-flow.ps1'

$script:checks = 0
function Assert-T {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "ASSERTION FAILED: $Message" }
    $script:checks++
}
function Expect-T {
    param([scriptblock]$Action, [string]$Prefix, [string]$Message)
    $observed = $null
    try { & $Action | Out-Null } catch { $observed = $_.Exception.Message }
    Assert-T ($null -ne $observed -and $observed.StartsWith($Prefix + ':')) ($Message + " Observed: $observed")
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

function Invoke-TRegistry {
    param(
        [string]$Action, [string]$Project, [string]$TaskId,
        [string]$Title, [string]$Description, [string]$Priority,
        [string[]]$Labels, [string[]]$DependsOn, [int]$ExpectedRevision = -1,
        [string[]]$Status, [string[]]$Stage, [string[]]$Label,
        [string]$UpdatedBefore, [string]$UpdatedAfter,
        [string]$Archived, [string]$Sort, [string]$Order,
        [int]$Limit = 0, [string]$Cursor,
        [string]$Format = 'Json', [string]$InputFile
    )
    return Invoke-BFRegistryCommand -Action $Action -ProjectPath $Project -TaskId $TaskId -Title $Title -Description $Description -Priority $Priority -Labels $Labels -DependsOn $DependsOn -ExpectedRevision $ExpectedRevision -Status $Status -Stage $Stage -Label $Label -UpdatedBefore $UpdatedBefore -UpdatedAfter $UpdatedAfter -Archived $Archived -Sort $Sort -Order $Order -Limit $Limit -Cursor $Cursor -Format $Format -InputFile $InputFile
}

function Get-TRegistryJson {
    param([string]$StdOut)
    return ($StdOut | ConvertFrom-Json)
}

function Assert-TError {
    # JSON-mode error contract: exactly { "error": { "class", "message" } }.
    param([object]$Document, [string]$Class, [string]$Message)
    $names = @($Document.PSObject.Properties | ForEach-Object { $_.Name })
    Assert-T (($names -join ',') -ceq 'error') "error document must have exactly the error field, got: $($names -join ',')."
    $errorNames = @($Document.error.PSObject.Properties | ForEach-Object { $_.Name })
    Assert-T (($errorNames -join ',') -ceq 'class,message') "error object must be exactly class/message, got: $($errorNames -join ',')."
    Assert-T ($Document.error.class -ceq $Class) "$Message Expected class $Class, got $($Document.error.class)."
    Assert-T (-not [string]::IsNullOrEmpty($Document.error.message)) "$Message Error message must not be empty."
}

function New-TTrustedRequest {
    # Trusted request schema v1 for staged activation (published requirement 5).
    param([string]$TaskId, [string]$Project, [switch]$MissingContract, [switch]$InvalidPriority)
    $request = [ordered]@{
        task_id             = $TaskId
        project_root        = $Project
        title               = 'Activate the planned repository task for the fixture.'
        priority            = 'medium'
        labels              = @()
        depends_on          = @()
        controller_contract = 'repository-store-aware/v1'
    }
    if ($MissingContract) { $request.Remove('controller_contract') }
    if ($InvalidPriority) { $request['priority'] = 'urgent' }
    return $request
}

function New-TLegacyTask {
    # Minimal checkout-local v1 journal. The request.prompt payload is private
    # data that must never surface in any registry output. With -Raw the bytes
    # are written directly, simulating a legacy copy created before canonical
    # adoption: the packaged legacy writer fence now refuses such writes.
    param([string]$Worktree, [string]$TaskId, [string]$Status = 'ready', [switch]$Raw, [string]$WorkerPath = '')
    $state = [ordered]@{
        schema_version = 1
        task_id        = $TaskId
        revision       = 1
        previous_sha256 = $null
        project_path   = $Worktree
        worker_path    = $WorkerPath
        baseline       = '0' * 40
        request        = [ordered]@{ prompt = 'SECRET-PROMPT-VALUE should never surface' }
        status         = $Status
        stage          = 'inspect'
        created_at     = '2026-01-15T08:30:00.0000000Z'
        updated_at     = '2026-01-15T08:30:00.0000000Z'
    }
    $revisionPath = Join-Path (Join-Path (Join-Path (Join-Path (Join-Path $Worktree '.bsl-flow') 'tasks') $TaskId) 'revisions') '000001.json'
    if ($Raw) { Write-TText $revisionPath (Get-BFCanonicalJson $state) }
    else { Write-BFJson -Path $revisionPath -Value $state }
    return $revisionPath
}

function Invoke-TEntry {
    param([string[]]$Arguments)
    $pwshPath = Join-Path $PSHOME 'pwsh.exe'
    $output = @(& $pwshPath -NoProfile -File $entryPoint @Arguments 2>&1)
    return [pscustomobject]@{
        ExitCode = $LASTEXITCODE
        Output   = @($output | ForEach-Object { $_.ToString() })
    }
}

$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-task-registry-' + [guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($testRoot)
$allOutputs = [System.Collections.Generic.List[string]]::new()
try {
    # --- Section 1: store resolution and clone identity ----------------------------------
    $repoA = New-TClone (Join-Path $testRoot 'clone-a')
    $wtA2 = New-TWorktree $repoA (Join-Path $testRoot 'clone-a-wt2') 'reg-a2'
    $repoB = New-TClone (Join-Path $testRoot 'clone-b')
    $storeA = Get-BFRegistryStore -ProjectRoot $repoA
    $storeA2 = Get-BFRegistryStore -ProjectRoot $wtA2
    $storeB = Get-BFRegistryStore -ProjectRoot $repoB
    Assert-T ($storeA.RepositoryId -cmatch '^[0-9a-f]{16}$') "repository_id is not 16 lowercase hex chars: $($storeA.RepositoryId)"
    Assert-T ($storeA.RepositoryId -ceq $storeA2.RepositoryId) 'Two worktrees of one clone must share one repository_id.'
    Assert-T ($storeA.TasksRoot -ceq $storeA2.TasksRoot) 'Two worktrees must resolve one store root.'
    Assert-T ($storeA.TasksRoot.StartsWith($storeA.CommonDir, [StringComparison]::OrdinalIgnoreCase)) 'Store root must live under the verified common dir.'
    Assert-T ($storeA.RepositoryId -cne $storeB.RepositoryId) 'Independent clones must have different repository_id values.'

    $createA1 = Invoke-TRegistry -Action Create -Project $repoA -Title 'Alpha one' -Description 'first' -Priority 'high' -Labels @('core', 'alpha')
    Assert-T ($createA1.ExitCode -eq 0) "create Alpha one failed: $($createA1.StdErr)"
    $docA1 = Get-TRegistryJson $createA1.StdOut
    foreach ($field in @('schema_version', 'repository_id', 'command', 'task_id', 'revision', 'status', 'archived', 'priority', 'title', 'next_action')) {
        Assert-T ($null -ne $docA1.PSObject.Properties[$field]) "create document is missing field $field."
    }
    Assert-T ($docA1.schema_version -eq 1 -and $docA1.command -ceq 'create') 'create document version/command mismatch.'
    Assert-T ($docA1.revision -cmatch '^[0-9a-f]{64}$') "write response revision must be the revision hash, got $($docA1.revision)."
    Assert-T ($docA1.status -ceq 'planned' -and $docA1.archived -eq $false) 'created task must be planned and unarchived.'
    Assert-T ($docA1.priority -ceq 'high' -and $docA1.title -ceq 'Alpha one') 'create document lost metadata.'
    Assert-T ($docA1.next_action -ceq 'activate') "planned task next_action must be activate."
    $taskA1 = $docA1.task_id
    Assert-T ($taskA1 -cmatch '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$') 'created task id is not a lowercase UUID.'
    $stateAfterCreate = Read-BFRegistryTaskState -TasksRoot $storeA.TasksRoot -TaskId $taskA1
    Assert-T ([string](Get-BFObjectProperty $stateAfterCreate['latest'] 'hash') -ceq $docA1.revision) 'write response revision must equal the stored first revision hash.'

    # Task created from the second worktree is visible from the first: one shared scope.
    Start-Sleep -Milliseconds 20
    $createA2 = Invoke-TRegistry -Action Create -Project $wtA2 -Title 'Alpha two' -Format Json
    Assert-T ($createA2.ExitCode -eq 0) "create from second worktree failed: $($createA2.StdErr)"
    $docA2 = Get-TRegistryJson $createA2.StdOut
    $taskA2 = $docA2.task_id
    Assert-T ($docA2.repository_id -ceq $storeA.RepositoryId) 'create from second worktree produced another repository identity.'
    $listFromRepo = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoA).StdOut
    $listFromWt = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $wtA2).StdOut
    Assert-T (@($listFromRepo.tasks).Count -eq 2 -and @($listFromWt.tasks).Count -eq 2) 'both worktrees must see both tasks in one repository scope.'
    Assert-T ((@($listFromWt.tasks).task_id -ccontains $taskA1) -and (@($listFromWt.tasks).task_id -ccontains $taskA2)) 'shared store lost task identity across worktrees.'

    # Clone isolation and clean Git status (registry files are clone-local data).
    $listB = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB).StdOut
    Assert-T (@($listB.tasks).Count -eq 0 -and $listB.repository_id -ceq $storeB.RepositoryId) 'clone B must not see clone A tasks.'
    Assert-T ([string]::IsNullOrEmpty((Invoke-TGit -C $wtA2 status --porcelain))) 'registry store must not appear in worktree Git status.'
    Assert-T ([string]::IsNullOrEmpty((Invoke-TGit -C $repoA status --porcelain))) 'registry store must not appear in repo Git status.'

    # Exact-root enforcement.
    Expect-T { Get-BFRegistryStore -ProjectRoot (Join-Path $repoA 'README.md') } 'BF_INVALID' 'Non-root project path was accepted.'
    $subDirectory = Join-Path $repoA 'sub'; [void][IO.Directory]::CreateDirectory($subDirectory)
    Expect-T { Get-BFRegistryStore -ProjectRoot $subDirectory } 'BF_INVALID' 'Subdirectory project path was accepted.'

    # --- Section 2: create validation (published requirement 3 constants) ------------------
    $emptyTitle = Invoke-TRegistry -Action Create -Project $repoA
    Assert-T ($emptyTitle.ExitCode -eq 2) 'empty title must be BF_INVALID through the dispatcher.'
    Assert-TError (Get-TRegistryJson $emptyTitle.StdOut) 'BF_INVALID' 'empty title error shape/class.'
    $emptyTitleHuman = Invoke-TRegistry -Action Create -Project $repoA -Format Human
    Assert-T ($emptyTitleHuman.ExitCode -eq 2 -and $emptyTitleHuman.StdErr.StartsWith('BF_INVALID:') -and [string]::IsNullOrEmpty($emptyTitleHuman.StdOut)) 'human error must be a concise stderr line with the class first.'
    Expect-T { New-BFRegistryPayload -Title '   ' } 'BF_INVALID' 'Whitespace title was accepted.'
    Expect-T { New-BFRegistryPayload -Title ('x' * 201) } 'BF_INVALID' 'Overlong title was accepted.'
    $trimmedTitle = New-BFRegistryPayload -Title '  padded  '
    Assert-T ([string]$trimmedTitle['title'] -ceq 'padded') 'title must be trimmed before validation/storage.'
    Expect-T { New-BFRegistryPayload -Title 't' -Priority 'urgent' } 'BF_INVALID' 'Unknown priority was accepted.'
    Expect-T { New-BFRegistryPayload -Title 't' -Labels @('a', 'a') } 'BF_INVALID' 'Duplicate labels were accepted.'
    Expect-T { New-BFRegistryPayload -Title 't' -Labels @('') } 'BF_INVALID' 'Empty label was accepted.'
    Expect-T { New-BFRegistryPayload -Title 't' -Labels @(('x' * 51)) } 'BF_INVALID' 'Overlong label was accepted.'
    Expect-T { New-BFRegistryPayload -Title 't' -Labels @(1..21 | ForEach-Object { "l$_" }) } 'BF_INVALID' 'More than 20 labels were accepted.'
    Expect-T { New-BFRegistryPayload -Title 't' -Description ('x' * 5001) } 'BF_INVALID' 'Overlong description was accepted.'
    Expect-T { New-BFRegistryPayload -Title 't' -DependsOn @($taskA1, $taskA1) } 'BF_CONFLICT' 'Duplicate depends_on ids were accepted.'
    Expect-T { New-BFRegistryPayload -Title 't' -DependsOn @('NOT-A-UUID') } 'BF_INVALID' 'Malformed depends_on id was accepted.'
    # depends_on entries must be UUID v4 (a well-formed v1 UUID is rejected).
    $versionOneUuid = 'c232ab00-9414-1f18-b1d6-011088749494'
    Expect-T { New-BFRegistryPayload -Title 't' -DependsOn @($versionOneUuid) } 'BF_INVALID' 'Non-v4 depends_on id was accepted.'

    # The default priority is medium; planned payloads are closed and carry no execution fields.
    $defaultPriority = New-BFRegistryPayload -Title 'default'
    Assert-T ($defaultPriority['priority'] -ceq 'medium' -and $defaultPriority['status'] -ceq 'planned') 'priority/status defaults are wrong.'
    $revisionFile = Join-Path (Join-Path (Join-Path $storeA.TasksRoot $taskA1) 'revisions') '000001.json'
    Assert-T ([IO.File]::Exists($revisionFile)) 'first revision file was not created.'
    $rawRevision = [IO.File]::ReadAllText($revisionFile)
    $parsedRevision = Read-BFJson $revisionFile
    Assert-T ($rawRevision -ceq (Get-BFCanonicalJson $parsedRevision)) 'revision file is not canonical JSON bytes.'
    Assert-T ([string](Get-BFObjectProperty $parsedRevision 'parent_hash') -ceq ('0' * 64)) 'first revision parent_hash must be the genesis hash.'
    $payloadKeys = @($parsedRevision.payload.PSObject.Properties | ForEach-Object { $_.Name }) | Sort-Object
    Assert-T ((($payloadKeys -join ',')) -ceq 'archived,depends_on,description,labels,priority,status,title') "planned payload is not the closed v1 shape: $($payloadKeys -join ',')"
    foreach ($forbidden in @('prompt', 'request', 'attempt', 'stage', 'worker', 'baseline', 'criteria')) {
        Assert-T ($forbidden -cnotin $payloadKeys) "planned payload leaks execution field $forbidden."
    }

    # --- Section 3: optimistic edit --------------------------------------------------------
    $missingRevision = Invoke-TRegistry -Action EditRegistry -Project $repoA -TaskId $taskA1 -Title 'no revision'
    Assert-T ($missingRevision.ExitCode -eq 2) 'edit without expected_revision must be BF_INVALID.'
    Assert-TError (Get-TRegistryJson $missingRevision.StdOut) 'BF_INVALID' 'missing expected_revision error class.'
    $staleEdit = Invoke-TRegistry -Action EditRegistry -Project $repoA -TaskId $taskA1 -ExpectedRevision 99 -Title 'stale'
    Assert-T ($staleEdit.ExitCode -eq 11) 'stale expected_revision must exit 11.'
    Assert-TError (Get-TRegistryJson $staleEdit.StdOut) 'BF_CONFLICT' 'stale edit error class.'
    Start-Sleep -Milliseconds 20
    $editA1 = Invoke-TRegistry -Action EditRegistry -Project $repoA -TaskId $taskA1 -ExpectedRevision 1 -Title 'Alpha one v2' -Priority 'medium' -Labels @('core') -DependsOn @($taskA2)
    Assert-T ($editA1.ExitCode -eq 0) "edit failed: $($editA1.StdErr)"
    $docEdit = Get-TRegistryJson $editA1.StdOut
    Assert-T ($docEdit.priority -ceq 'medium') 'edit did not append exactly one revision with new metadata.'
    $stateA1 = Read-BFRegistryTaskState -TasksRoot $storeA.TasksRoot -TaskId $taskA1
    Assert-T ($stateA1['health'] -ceq 'ok' -and @($stateA1['revisions']).Count -eq 2) 'journal does not hold two verified revisions after edit.'
    Assert-T ($docEdit.revision -ceq [string](Get-BFObjectProperty $stateA1['revisions'][1] 'hash')) 'edit response revision must be the appended revision hash.'
    Assert-T ([string](Get-BFObjectProperty $stateA1['revisions'][1] 'parent_hash') -ceq [string](Get-BFObjectProperty $stateA1['revisions'][0] 'hash')) 'parent_hash must chain the previous revision hash.'
    Assert-T ((Get-BFObjectProperty $stateA1['metadata'] 'depends_on') -ccontains $taskA2) 'edit lost depends_on metadata.'

    # --- Section 4: dependency rules --------------------------------------------------------
    $missingTarget = [guid]::NewGuid().ToString()
    $createA3 = Invoke-TRegistry -Action Create -Project $repoA -Title 'Alpha three' -DependsOn @($missingTarget)
    Assert-T ($createA3.ExitCode -eq 0) 'missing depends_on target must be allowed.'
    $taskA3 = (Get-TRegistryJson $createA3.StdOut).task_id
    $showA3 = Get-TRegistryJson (Invoke-TRegistry -Action Show -Project $repoA -TaskId $taskA3).StdOut
    Assert-T ($showA3.dependency_summary.total -eq 1 -and $showA3.dependency_summary.by_status.missing -eq 1) 'missing dependency must be counted under missing.'
    Assert-T (@($showA3.dependency_graph.edges).Count -eq 1 -and $showA3.dependency_graph.edges[0].from -ceq $taskA3 -and $showA3.dependency_graph.edges[0].to -ceq $missingTarget) 'missing dependency edge is not reported as-is.'

    $selfRef = Invoke-TRegistry -Action EditRegistry -Project $repoA -TaskId $taskA3 -ExpectedRevision 1 -Title 'self' -DependsOn @($taskA3)
    Assert-T ($selfRef.ExitCode -eq 11) 'self-reference must exit 11.'
    Assert-TError (Get-TRegistryJson $selfRef.StdOut) 'BF_CONFLICT' 'self-reference error class.'
    Assert-T ((Read-BFRegistryTaskState -TasksRoot $storeA.TasksRoot -TaskId $taskA3)['revision'] -eq 1) 'rejected self-reference wrote a revision.'

    $cycleEdit = Invoke-TRegistry -Action EditRegistry -Project $repoA -TaskId $taskA2 -ExpectedRevision 1 -Title 'cycle' -DependsOn @($taskA1)
    Assert-T ($cycleEdit.ExitCode -eq 11) 'cycle over existing journals must exit 11.'
    Assert-T ((Get-TRegistryJson $cycleEdit.StdOut).error.message -cmatch 'cycle') 'cycle blocker must name the cycle.'
    Assert-TError (Get-TRegistryJson $cycleEdit.StdOut) 'BF_CONFLICT' 'cycle error class.'
    Assert-T ((Read-BFRegistryTaskState -TasksRoot $storeA.TasksRoot -TaskId $taskA2)['revision'] -eq 1) 'rejected cycle wrote a revision.'

    $createA4 = Invoke-TRegistry -Action Create -Project $repoA -Title 'Alpha four' -DependsOn @($taskA1, $taskA2)
    Assert-T ($createA4.ExitCode -eq 0) 'diamond dependency graph must be allowed.'
    $taskA4 = (Get-TRegistryJson $createA4.StdOut).task_id

    # --- Section 5: archive/unarchive --------------------------------------------------------
    Start-Sleep -Milliseconds 20
    $archiveA4 = Invoke-TRegistry -Action ArchiveTask -Project $repoA -TaskId $taskA4
    Assert-T ($archiveA4.ExitCode -eq 0) 'archive failed.'
    $docArchive = Get-TRegistryJson $archiveA4.StdOut
    $stateA4Archive = Read-BFRegistryTaskState -TasksRoot $storeA.TasksRoot -TaskId $taskA4
    Assert-T ($docArchive.archived -eq $true -and $docArchive.status -ceq 'planned') 'archive must append one revision and keep the lifecycle status.'
    Assert-T ($stateA4Archive['revision'] -eq 2 -and $docArchive.revision -ceq [string](Get-BFObjectProperty $stateA4Archive['latest'] 'hash')) 'archive response revision must be the appended revision hash.'
    $listDefault = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoA).StdOut
    Assert-T (-not (@($listDefault.tasks).task_id -ccontains $taskA4)) 'archived task must be hidden from the default list.'
    $listArchivedOnly = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoA -Archived 'true').StdOut
    Assert-T (@($listArchivedOnly.tasks).task_id -ccontains $taskA4 -and @($listArchivedOnly.tasks).Count -eq 1) 'archived=true view must list archived tasks only.'
    $listArchivedAll = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoA -Archived 'all').StdOut
    Assert-T (@($listArchivedAll.tasks).task_id -ccontains $taskA4 -and @($listArchivedAll.tasks).Count -eq 4) 'archived=all view must list both archived and unarchived tasks.'
    $badArchived = Invoke-TRegistry -Action List -Project $repoA -Archived 'maybe'
    Assert-T ($badArchived.ExitCode -eq 2) 'archived outside false|true|all must be BF_INVALID.'
    $unarchiveA4 = Invoke-TRegistry -Action UnarchiveTask -Project $repoA -TaskId $taskA4
    Assert-T ($unarchiveA4.ExitCode -eq 0 -and (Get-TRegistryJson $unarchiveA4.StdOut).archived -eq $false) 'unarchive failed.'
    $doubleUnarchive = Invoke-TRegistry -Action UnarchiveTask -Project $repoA -TaskId $taskA4
    Assert-T ($doubleUnarchive.ExitCode -eq 11) 'duplicate unarchive must be a BF_CONFLICT without a revision.'
    Assert-TError (Get-TRegistryJson $doubleUnarchive.StdOut) 'BF_CONFLICT' 'duplicate unarchive error class.'
    Assert-T ((Read-BFRegistryTaskState -TasksRoot $storeA.TasksRoot -TaskId $taskA4)['revision'] -eq 3) 'rejected duplicate transition wrote a revision.'
    $historyA4 = Get-TRegistryJson (Invoke-TRegistry -Action History -Project $repoA -TaskId $taskA4).StdOut
    $archiveTypes = @(@($historyA4.timeline) | ForEach-Object { $_.type })
    Assert-T (($archiveTypes -join ',') -ceq 'metadata_change,archive_change,archive_change') "archive changes must be typed archive_change events, got: $($archiveTypes -join ',')."

    # --- Section 6: filters, sort, bounded limit, opaque cursor -------------------------------
    # Fixture: five planned tasks with staggered timestamps in clone B.
    # After the bump edit below, B one v2 has priority=medium and no labels
    # (edit replaces the full metadata snapshot).
    $fixture = @()
    foreach ($spec in @(
        @{ Title = 'B one'; Priority = 'low'; Labels = @('sea') },
        @{ Title = 'B two'; Priority = 'medium'; Labels = @('sea', 'land') },
        @{ Title = 'B three'; Priority = 'high'; Labels = @('land') },
        @{ Title = 'B four'; Priority = 'critical'; Labels = @() },
        @{ Title = 'B five'; Priority = 'low'; Labels = @('air') }
    )) {
        $created = Invoke-TRegistry -Action Create -Project $repoB -Title $spec.Title -Priority $spec.Priority -Labels $spec.Labels
        Assert-T ($created.ExitCode -eq 0) "fixture create failed: $($created.StdErr)"
        $fixture += (Get-TRegistryJson $created.StdOut).task_id
        Start-Sleep -Milliseconds 25
    }
    # Bump the first fixture task so updated_at ordering becomes observable.
    $bumpedAt = [DateTime]::UtcNow
    $editB1 = Invoke-TRegistry -Action EditRegistry -Project $repoB -TaskId $fixture[0] -ExpectedRevision 1 -Title 'B one v2'
    Assert-T ($editB1.ExitCode -eq 0) 'fixture bump failed.'
    $listBAll = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB).StdOut
    $orderedIds = @($listBAll.tasks | ForEach-Object { $_.task_id })
    Assert-T ($orderedIds[0] -ceq $fixture[0]) 'most recently updated task must sort first (updated desc).'
    Assert-T (@($orderedIds | Sort-Object -Unique).Count -eq 5 -and $orderedIds.Count -eq 5) 'list must return every fixture task exactly once.'
    $repeatList = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB).StdOut
    Assert-T ((($orderedIds -join ',')) -ceq ((@($repeatList.tasks | ForEach-Object { $_.task_id }) -join ','))) 'list sort must be deterministic across calls.'
    # Direct tie-break unit: identical sort key falls back to task_id ascending (ordinal).
    $tieRows = @(
        [ordered]@{ task_id = 'ffffffff-ffff-4fff-8fff-ffffffffffff'; updated_at = '2026-01-01T00:00:00.0000000Z' },
        [ordered]@{ task_id = '00000000-0000-4000-8000-000000000001'; updated_at = '2026-01-01T00:00:00.0000000Z' }
    )
    $tieSorted = @(Sort-BFRegistryRows -Rows $tieRows -Sort 'updated_at' -Order 'desc')
    Assert-T ($tieSorted[0]['task_id'] -ceq '00000000-0000-4000-8000-000000000001') 'equal-key rows must tie-break on task_id ascending.'
    $tieTitleSorted = @(Sort-BFRegistryRows -Rows @(
        [ordered]@{ task_id = '00000000-0000-4000-8000-000000000001'; title = 'AB'; created_at = ''; updated_at = ''; priority = 'medium'; status = 'planned' },
        [ordered]@{ task_id = '00000000-0000-4000-8000-000000000002'; title = 'A'; created_at = ''; updated_at = ''; priority = 'medium'; status = 'planned' }
    ) -Sort 'title' -Order 'desc')
    Assert-T ($tieTitleSorted[0]['task_id'] -ceq '00000000-0000-4000-8000-000000000001') 'descending title sort must order AB before A.'

    # Multi-value comma-separated status filter; invalid values are BF_INVALID.
    $filterStatus = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB -Status @('planned')).StdOut
    Assert-T (@($filterStatus.tasks).Count -eq 5) 'status filter planned must return every planned fixture task.'
    $filterStatusCombined = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB -Status 'planned,ready').StdOut
    Assert-T (@($filterStatusCombined.tasks).Count -eq 5) 'comma-separated multi-value status filter must behave like the array form.'
    $badStatus = Invoke-TRegistry -Action List -Project $repoB -Status @('teleported')
    Assert-T ($badStatus.ExitCode -eq 2) 'unknown status filter must be BF_INVALID.'
    # Priority multi-value filter.
    $filterPriority = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB -Priority @('high')).StdOut
    Assert-T (@($filterPriority.tasks).task_id -ceq $fixture[2] -and @($filterPriority.tasks).Count -eq 1) 'priority filter must match exactly.'
    $filterPriorityMulti = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB -Priority 'high,critical').StdOut
    $multiIds = @(@($filterPriorityMulti.tasks) | ForEach-Object { $_.task_id })
    Assert-T (@($multiIds | Sort-Object -Unique).Count -eq 2 -and -not ($multiIds -ccontains $fixture[0])) 'comma-separated priority filter must match both values.'
    # Label filter: OR semantics over the requested labels.
    $filterLabelOr = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB -Label 'sea,land').StdOut
    $labelOrIds = @(@($filterLabelOr.tasks) | ForEach-Object { $_.task_id })
    Assert-T ((@($labelOrIds | Sort-Object { $_ }) -join ',') -ceq ((@($fixture[1], $fixture[2]) | Sort-Object { $_ }) -join ',')) 'label filter must match at least one requested label (OR).'
    $filterLabelSingle = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB -Label @('air')).StdOut
    Assert-T (@($filterLabelSingle.tasks).task_id -ceq $fixture[4] -and @($filterLabelSingle.tasks).Count -eq 1) 'single label filter must match exactly.'
    # RFC3339 inclusive bounds.
    $filterAfter = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB -UpdatedAfter ($bumpedAt.ToString('o'))).StdOut
    Assert-T (@($filterAfter.tasks).task_id -ccontains $fixture[0]) 'updated-after filter lost the bumped task.'
    Assert-T (-not (@($filterAfter.tasks).task_id -ccontains $fixture[4])) 'updated-after filter leaked an older task.'
    $filterBefore = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB -UpdatedBefore ([DateTime]::UtcNow.AddMinutes(1).ToString('o'))).StdOut
    Assert-T (@($filterBefore.tasks).Count -eq 5) 'updated-before bound is inclusive and must keep every task in the past.'
    $badTime = Invoke-TRegistry -Action List -Project $repoB -UpdatedAfter 'yesterday'
    Assert-T ($badTime.ExitCode -eq 2) 'malformed updated-after must be BF_INVALID.'
    # Sort keys and per-field default orders.
    $sortedTitle = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB -Sort 'title' -Order 'asc').StdOut
    Assert-T (((@($sortedTitle.tasks) | ForEach-Object { $_.title }) -join '|') -ceq 'B five|B four|B one v2|B three|B two') 'ascending title sort must be lexicographic.'
    $sortedPriorityDesc = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB -Sort 'priority' -Order 'desc').StdOut
    Assert-T (((@($sortedPriorityDesc.tasks) | ForEach-Object { $_.priority }) -join '|') -ceq 'critical|high|medium|medium|low') 'descending priority sort must order critical..low with UUID tie-break.'
    $sortedPriorityAsc = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB -Sort 'priority').StdOut
    Assert-T (((@($sortedPriorityAsc.tasks) | ForEach-Object { $_.priority }) -join '|') -ceq 'low|medium|medium|high|critical') 'priority default order must be asc with low first.'
    $sortedStatusAsc = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB -Sort 'status' -Order 'asc').StdOut
    $expectedUuidOrder = @($fixture | Sort-Object { $_ })
    Assert-T (((@($sortedStatusAsc.tasks) | ForEach-Object { $_.task_id }) -join ',') -ceq ($expectedUuidOrder -join ',')) 'equal status keys must tie-break on ascending UUID.'
    $sortedCreatedDesc = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB -Sort 'created_at').StdOut
    Assert-T ((@($sortedCreatedDesc.tasks) | ForEach-Object { $_.task_id })[0] -ceq $fixture[4]) 'created_at default order must be desc.'
    $badSort = Invoke-TRegistry -Action List -Project $repoB -Sort 'banana'
    Assert-T ($badSort.ExitCode -eq 2) 'unknown sort must be BF_INVALID.'
    $badOrder = Invoke-TRegistry -Action List -Project $repoB -Order 'sideways'
    Assert-T ($badOrder.ExitCode -eq 2) 'order outside asc|desc must be BF_INVALID.'
    # Bounded limit and opaque cursor paging.
    $pageOne = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB -Limit 2).StdOut
    Assert-T (@($pageOne.tasks).Count -eq 2 -and $null -ne $pageOne.next_cursor) 'first page must be bounded with a next cursor.'
    $visited = @($pageOne.tasks | ForEach-Object { $_.task_id })
    $cursor = $pageOne.next_cursor
    while ($null -ne $cursor) {
        $page = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoB -Limit 2 -Cursor $cursor).StdOut
        $visited += @($page.tasks | ForEach-Object { $_.task_id })
        $cursor = $page.next_cursor
    }
    Assert-T ((($visited -join ',')) -ceq (($orderedIds -join ','))) "cursor paging must reproduce the same order: $($visited -join ',')"
    $tooLarge = Invoke-TRegistry -Action List -Project $repoB -Limit 201
    Assert-T ($tooLarge.ExitCode -eq 2) 'limit above 200 must be BF_INVALID.'
    $negativeLimit = Invoke-TRegistry -Action List -Project $repoB -Limit -3
    Assert-T ($negativeLimit.ExitCode -eq 2) 'negative limit must be BF_INVALID.'
    $foreignCursor = Invoke-TRegistry -Action List -Project $repoA -Limit 1 -Cursor $pageOne.next_cursor
    Assert-T ($foreignCursor.ExitCode -eq 2) 'cursor from another repository must be rejected.'
    $garbageCursor = Invoke-TRegistry -Action List -Project $repoB -Limit 1 -Cursor 'not-a-cursor'
    Assert-T ($garbageCursor.ExitCode -eq 2) 'garbage cursor must be BF_INVALID.'
    # Cursor is invalidated when filters or sort/order change.
    $filterChangedCursor = Invoke-TRegistry -Action List -Project $repoB -Limit 2 -Priority 'low' -Cursor $pageOne.next_cursor
    Assert-T ($filterChangedCursor.ExitCode -eq 2) 'cursor must be invalid when filters change.'
    $orderChangedCursor = Invoke-TRegistry -Action List -Project $repoB -Limit 2 -Order 'asc' -Cursor $pageOne.next_cursor
    Assert-T ($orderChangedCursor.ExitCode -eq 2) 'cursor must be invalid when order changes.'
    $sortChangedCursor = Invoke-TRegistry -Action List -Project $repoB -Limit 2 -Sort 'title' -Cursor $pageOne.next_cursor
    Assert-T ($sortChangedCursor.ExitCode -eq 2) 'cursor must be invalid when sort changes.'

    # --- Section 7: JSON vs Human parity -------------------------------------------------------
    $jsonView = Invoke-TRegistry -Action List -Project $repoB -Priority 'low'
    $humanView = Invoke-TRegistry -Action List -Project $repoB -Priority 'low' -Format Human
    Assert-T ($jsonView.ExitCode -eq 0) "json list failed: $($jsonView.StdErr)"
    Assert-T ($humanView.ExitCode -eq 0) 'human list failed.'
    $jsonIds = @((Get-TRegistryJson $jsonView.StdOut).tasks | ForEach-Object { $_.task_id })
    $humanLines = @($humanView.StdOut -split "`r?`n" | Where-Object { $_ -match '\S' })
    Assert-T ($humanLines[2].StartsWith("task_id`tstatus")) 'human list header is not the stable table header.'
    $humanIds = @($humanLines | Select-Object -Skip 3 | ForEach-Object { ($_ -split "`t")[0] })
    Assert-T ((($humanIds -join ',')) -ceq (($jsonIds -join ','))) 'JSON and Human views must share membership and order.'
    $allOutputs.Add($jsonView.StdOut); $allOutputs.Add($humanView.StdOut)

    # --- Section 8: show -------------------------------------------------------------------------
    $showA1Raw = (Invoke-TRegistry -Action Show -Project $repoA -TaskId $taskA1).StdOut
    $showA1 = Get-TRegistryJson $showA1Raw
    Assert-T ($showA1.schema_version -eq 1 -and $showA1.repository_id -ceq $storeA.RepositoryId) 'show lost envelope identity.'
    Assert-T ($showA1.task_id -ceq $taskA1) 'show lost identity.'
    Assert-T ($showA1.status -ceq 'planned' -and $showA1.next_action -ceq 'activate' -and $null -eq $showA1.stage) 'show card lost planned state.'
    Assert-T ($showA1.dependency_summary.total -eq 1 -and $showA1.dependency_summary.by_status.planned -eq 1) 'show dependency summary lost the existing target.'
    Assert-T ($showA1.dependency_graph.nodes -ccontains $taskA1 -and $showA1.dependency_graph.nodes -ccontains $taskA2) 'show dependency graph lost graph nodes.'
    Assert-T (@($showA1.dependency_graph.edges | Where-Object { $_.from -ceq $taskA1 -and $_.to -ceq $taskA2 }).Count -eq 1) 'show dependency graph lost the edge.'
    Assert-T ($showA1.revision -cmatch '^[0-9a-f]{64}$') 'show revision must be the latest revision hash.'
    Assert-T ($null -eq $showA1.request_summary -and @($showA1.criteria_ids).Count -eq 0 -and @($showA1.observations).Count -eq 0) 'planned show additions must be empty/null.'
    Assert-T (@($showA1.blockers).Count -eq 0 -and $null -eq $showA1.question) 'planned show must have no blockers or question.'
    Assert-T ($showA1.attempts_summary.total -eq 0 -and $showA1.attempts_summary.terminal -eq 0 -and $showA1.attempts_summary.active -eq 0) 'planned show attempts summary must be zeroed.'
    Assert-T ($showA1.acceptance_summary.criteria_passed -eq 0 -and $showA1.acceptance_summary.criteria_total -eq 0) 'planned show acceptance summary must be zeroed.'
    Assert-T (@($showA1.evidence_references).Count -eq 0) 'planned show must have no evidence references.'
    Assert-T ($null -eq $showA1.diagnostic_state -and $showA1.freshness -ceq 'fresh') 'healthy show must project null diagnostic state and fresh freshness.'
    $showMissing = Invoke-TRegistry -Action Show -Project $repoA -TaskId ([guid]::NewGuid().ToString())
    Assert-T ($showMissing.ExitCode -eq 2) 'show of an unknown task must be BF_INVALID.'
    $showBadId = Invoke-TRegistry -Action Show -Project $repoA -TaskId 'nope'
    Assert-T ($showBadId.ExitCode -eq 2) 'show with a malformed id must be BF_INVALID.'
    $allOutputs.Add($showA1Raw)

    # --- Section 9: history ------------------------------------------------------------------------
    $historyA1Raw = (Invoke-TRegistry -Action History -Project $repoA -TaskId $taskA1).StdOut
    $historyA1 = Get-TRegistryJson $historyA1Raw
    Assert-T ($historyA1.schema_version -eq 1 -and $historyA1.repository_id -ceq $storeA.RepositoryId) 'history lost envelope identity.'
    $timeline = @($historyA1.timeline)
    Assert-T ($timeline.Count -eq 2) 'history must list every revision as one timeline event.'
    Assert-T ($timeline[0].type -ceq 'metadata_change' -and $timeline[1].type -ceq 'metadata_change') 'create/edit events must be typed metadata_change.'
    Assert-T (-not [string]::IsNullOrEmpty($timeline[0].timestamp) -and -not [string]::IsNullOrEmpty($timeline[1].timestamp)) 'history events must carry timestamps.'
    foreach ($event in $timeline) {
        $eventNames = @($event.PSObject.Properties | ForEach-Object { $_.Name })
        Assert-T (((($eventNames | Sort-Object) -join ',')) -ceq 'evidence_reference,revision_hash,summary,timestamp,type') "history event is not the closed v1 shape: $($eventNames -join ',')"
    }
    $recomputed = Get-BFRegistryRevisionHash (Read-BFRegistryTaskState -TasksRoot $storeA.TasksRoot -TaskId $taskA1)['revisions'][1]
    Assert-T ($timeline[1].revision_hash -ceq $recomputed) 'history revision_hash does not match the journal.'
    Assert-T ($null -eq $timeline[0].evidence_reference) 'registry history events carry no evidence references.'
    Assert-T (-not [string]::IsNullOrEmpty($timeline[1].summary)) 'history must summarize payloads.'
    $historyMissing = Invoke-TRegistry -Action History -Project $repoB -TaskId ([guid]::NewGuid().ToString())
    Assert-T ($historyMissing.ExitCode -eq 2) 'history of an unknown task must be BF_INVALID.'
    $allOutputs.Add($historyA1Raw)

    # --- Section 10: overview ------------------------------------------------------------------------
    $overviewARaw = (Invoke-TRegistry -Action Overview -Project $repoA).StdOut
    $overviewA = Get-TRegistryJson $overviewARaw
    Assert-T ($overviewA.repository_id -ceq $storeA.RepositoryId) 'overview lost scope identity.'
    Assert-T ($overviewA.scope_identity -ceq $storeA.RepositoryId) 'overview scope_identity must be the repository_id.'
    Assert-T (-not [string]::IsNullOrEmpty($overviewA.generated_at)) 'overview must carry generated_at.'
    $countKeys = @($overviewA.counts.PSObject.Properties | ForEach-Object { $_.Name })
    Assert-T ((($countKeys | Sort-Object) -join ',') -ceq 'archived,blocked,completed,corrupt,needs_input,orphaned,planned,running') "overview counts must be exactly the eight published keys, got: $($countKeys -join ',')."
    Assert-T ($overviewA.counts.planned -eq 4 -and $overviewA.counts.archived -eq 0 -and $overviewA.counts.corrupt -eq 0 -and $overviewA.counts.orphaned -eq 0) 'overview counts are wrong.'
    Assert-T ($overviewA.totals_by_status.planned -eq 4 -and $overviewA.totals_by_status.completed -eq 0) 'overview status totals are wrong.'
    Assert-T ($overviewA.totals_by_priority.medium -eq 4 -and $overviewA.totals_by_priority.low -eq 0 -and $overviewA.totals_by_priority.high -eq 0) 'overview priority totals are wrong (default priority is medium).'
    Assert-T ($overviewA.totals_by_stage.PSObject.Properties['implement'] -ne $null -and $overviewA.totals_by_stage.implement -eq 0) 'overview stage totals must cover the stage enum.'
    foreach ($deferred in @('dependency_blocked', 'stale_completed', 'tasks', 'legacy', 'by_priority', 'scope', 'command')) {
        Assert-T ($null -eq $overviewA.PSObject.Properties[$deferred]) "deferred/removed overview field $deferred must be omitted in v1."
    }
    $allOutputs.Add($overviewARaw)

    # --- Section 11: corruption diagnostics -------------------------------------------------------------
    $repoC = New-TClone (Join-Path $testRoot 'clone-c')
    $storeC = Get-BFRegistryStore -ProjectRoot $repoC
    $healthyTask = (Get-TRegistryJson (Invoke-TRegistry -Action Create -Project $repoC -Title 'Healthy').StdOut).task_id
    Start-Sleep -Milliseconds 20
    $victimTask = (Get-TRegistryJson (Invoke-TRegistry -Action Create -Project $repoC -Title 'Victim').StdOut).task_id
    Start-Sleep -Milliseconds 20
    [void](Invoke-TRegistry -Action EditRegistry -Project $repoC -TaskId $victimTask -ExpectedRevision 1 -Title 'Victim v2')
    $victimFirst = Join-Path (Join-Path (Join-Path $storeC.TasksRoot $victimTask) 'revisions') '000001.json'
    Write-TText $victimFirst ([IO.File]::ReadAllText($victimFirst).Replace('Victim', 'Tamper'))
    $listC = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoC).StdOut
    $victimRows = @($listC.tasks | Where-Object { $_.task_id -ceq $victimTask })
    $healthyRows = @($listC.tasks | Where-Object { $_.task_id -ceq $healthyTask })
    Assert-T ($victimRows.Count -eq 1 -and $victimRows[0].diagnostic_state -ceq 'corrupt') 'corrupt journal must surface a diagnostic entry with health value.'
    Assert-T ($healthyRows.Count -eq 1 -and $healthyRows[0].diagnostic_state -eq $null) 'corrupt journal must not hide the other tasks.'
    $showVictim = Invoke-TRegistry -Action Show -Project $repoC -TaskId $victimTask
    Assert-T ($showVictim.ExitCode -eq 11) 'show of a corrupt task must exit 11.'
    Assert-TError (Get-TRegistryJson $showVictim.StdOut) 'BF_BLOCKED' 'corrupt show error class.'
    $historyVictim = Invoke-TRegistry -Action History -Project $repoC -TaskId $victimTask
    Assert-T ($historyVictim.ExitCode -eq 11) 'history of a corrupt task must exit 11.'
    Assert-TError (Get-TRegistryJson $historyVictim.StdOut) 'BF_BLOCKED' 'corrupt history error class.'
    $overviewC = Get-TRegistryJson (Invoke-TRegistry -Action Overview -Project $repoC).StdOut
    Assert-T ($overviewC.counts.corrupt -eq 1 -and $overviewC.counts.planned -eq 1) 'overview must count corrupt tasks without counting them completed.'

    $victimSecond = Join-Path (Join-Path (Join-Path $storeC.TasksRoot $victimTask) 'revisions') '000002.json'
    Move-Item -LiteralPath $victimSecond -Destination (Join-Path (Join-Path (Join-Path $storeC.TasksRoot $victimTask) 'revisions') '000009.json') -Force
    $listGap = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoC).StdOut
    Assert-T (@($listGap.tasks | Where-Object { $_.task_id -ceq $victimTask })[0].diagnostic_state -ceq 'corrupt') 'revision gap must stay a visible corrupt diagnostic.'

    $orphanId = [guid]::NewGuid().ToString()
    [void][IO.Directory]::CreateDirectory((Join-Path $storeC.TasksRoot $orphanId))
    $listOrphan = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoC).StdOut
    $orphanRows = @($listOrphan.tasks | Where-Object { $_.task_id -ceq $orphanId })
    Assert-T ($orphanRows[0].diagnostic_state -ceq 'orphaned') 'UUID directory without journal must be an orphaned diagnostic.'
    $showOrphan = Invoke-TRegistry -Action Show -Project $repoC -TaskId $orphanId
    Assert-T ($showOrphan.ExitCode -eq 11) 'show of an orphaned task must exit 11 (BF_BLOCKED, not invisible).'
    Assert-TError (Get-TRegistryJson $showOrphan.StdOut) 'BF_BLOCKED' 'orphaned show error class.'

    # Derived artifacts are never authoritative: garbage projections change nothing.
    $listBeforeDerived = (Invoke-TRegistry -Action List -Project $repoC).StdOut
    Write-TText (Join-Path (Join-Path $storeC.TasksRoot $healthyTask) 'current.json') '{"revision":999,"sha256":"stale"}'
    Write-TText (Join-Path $storeC.TasksRoot 'catalog') 'garbage'
    $listAfterDerived = (Invoke-TRegistry -Action List -Project $repoC).StdOut
    Assert-T ((($listBeforeDerived -ceq $listAfterDerived))) 'derived artifacts must not influence reads.'

    # --- Section 12: legacy read-only discovery -----------------------------------------------------------
    $repoD = New-TClone (Join-Path $testRoot 'clone-d')
    $wtD = New-TWorktree $repoD (Join-Path $testRoot 'clone-d-wt') 'reg-d'
    $legacyId = [guid]::NewGuid().ToString()
    $legacyFile = New-TLegacyTask -Worktree $wtD -TaskId $legacyId
    $legacyBytesBefore = (Get-BFFileHash $legacyFile)
    $listD = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $wtD).StdOut
    $legacyRows = @($listD.tasks | Where-Object { $_.task_id -ceq $legacyId })
    Assert-T ($legacyRows.Count -eq 1 -and $legacyRows[0].status -ceq 'ready') 'legacy task must surface read-only from discovery.'
    Assert-T ($legacyRows[0].current_worktree -ceq $wtD -and $legacyRows[0].origin_worktree -ceq $wtD) 'legacy row must carry the worktree identity.'
    Assert-T ($legacyRows[0].freshness -ceq 'fresh' -and $legacyRows[0].diagnostic_state -eq $null) 'healthy legacy row must project fresh with no diagnostic.'
    Assert-T ((Get-BFFileHash $legacyFile) -ceq $legacyBytesBefore) 'legacy discovery must never rewrite legacy bytes.'

    # Stale freshness: an absolute live-input path that no longer exists marks
    # the read projection stale without changing the historical status.
    $staleLegacyId = [guid]::NewGuid().ToString()
    New-TLegacyTask -Worktree $wtD -TaskId $staleLegacyId -WorkerPath (Join-Path $testRoot 'gone-worker') | Out-Null
    $listStale = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $wtD).StdOut
    $staleRows = @($listStale.tasks | Where-Object { $_.task_id -ceq $staleLegacyId })
    Assert-T ($staleRows[0].freshness -ceq 'stale' -and $staleRows[0].status -ceq 'ready') 'missing absolute live-input path must mark the projection freshness=stale (diagnostic only).'

    # Diverging same-UUID history is a visible conflict; show is blocked.
    $conflictDoc = Get-TRegistryJson (Invoke-TRegistry -Action Create -Project $wtD -Title 'Both stores').StdOut
    $conflictId = $conflictDoc.task_id
    $conflictJson = Get-TRegistryJson (Invoke-TRegistry -Action Show -Project $wtD -TaskId $conflictId).StdOut
    Assert-T ($conflictJson.status -ceq 'planned') 'conflict fixture precondition failed.'
    New-TLegacyTask -Worktree $wtD -TaskId $conflictId -Raw | Out-Null
    # The packaged legacy writer fence must refuse canonical UUIDs (fail closed);
    # only raw bytes written before adoption can create the diverging copy.
    Expect-T { New-TLegacyTask -Worktree $wtD -TaskId $conflictId } 'BF_CONFLICT' 'legacy write to a canonical uuid must be refused by the fence.'
    $listConflict = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $wtD).StdOut
    $conflictRows = @($listConflict.tasks | Where-Object { $_.task_id -ceq $conflictId })
    Assert-T ($conflictRows.Count -eq 1 -and $conflictRows[0].diagnostic_state -ceq 'conflict') 'diverging same-UUID verified history must be a single conflict diagnostic.'
    $showConflict = Invoke-TRegistry -Action Show -Project $wtD -TaskId $conflictId
    Assert-T ($showConflict.ExitCode -eq 11) 'conflicted UUID show must exit 11.'
    Assert-TError (Get-TRegistryJson $showConflict.StdOut) 'BF_BLOCKED' 'conflicted UUID show must be blocked with the exact reason.'
    $legacyBytesAfter = (Get-BFFileHash (Join-Path (Join-Path (Join-Path (Join-Path $wtD '.bsl-flow') 'tasks') $conflictId) 'revisions\000001.json'))
    Assert-T ($null -ne $legacyBytesAfter) 'conflict discovery must keep legacy bytes in place.'

    # Identical verified histories (identical revision-hash chains) deduplicate.
    $dedupeDoc = Get-TRegistryJson (Invoke-TRegistry -Action Create -Project $wtD -Title 'Dedupe me').StdOut
    $dedupeId = $dedupeDoc.task_id
    $storeD = Get-BFRegistryStore -ProjectRoot $wtD
    $repositoryRevision = Join-Path (Join-Path (Join-Path $storeD.TasksRoot $dedupeId) 'revisions') '000001.json'
    $dedupeLegacyDir = Join-Path (Join-Path (Join-Path (Join-Path $wtD '.bsl-flow') 'tasks') $dedupeId) 'revisions'
    [void][IO.Directory]::CreateDirectory($dedupeLegacyDir)
    Copy-Item -LiteralPath $repositoryRevision -Destination (Join-Path $dedupeLegacyDir '000001.json') -Force
    $listDedupe = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $wtD).StdOut
    $dedupeRows = @($listDedupe.tasks | Where-Object { $_.task_id -ceq $dedupeId })
    Assert-T ($dedupeRows.Count -eq 1 -and $dedupeRows[0].diagnostic_state -eq $null -and $dedupeRows[0].status -ceq 'planned') 'identical verified history must dedupe to one row.'

    # A corrupt legacy journal stays a diagnostic and never breaks listing.
    $corruptLegacyId = [guid]::NewGuid().ToString()
    $corruptLegacyDir = Join-Path (Join-Path (Join-Path $wtD '.bsl-flow') 'tasks') $corruptLegacyId
    Write-TText (Join-Path (Join-Path $corruptLegacyDir 'revisions') '000001.json') '{"torn":'
    $listCorruptLegacy = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $wtD).StdOut
    Assert-T (@($listCorruptLegacy.tasks | Where-Object { $_.task_id -ceq $corruptLegacyId })[0].diagnostic_state -ceq 'corrupt') 'corrupt legacy journal must be a diagnostic row.'

    # Redaction: no legacy prompt content, no secret-shaped keys in any output.
    $allOutputs.Add((Invoke-TRegistry -Action List -Project $wtD).StdOut)
    $allOutputs.Add((Invoke-TRegistry -Action List -Project $wtD -Format Json -Label @('land')).StdOut)
    $humanLegacyList = Invoke-TRegistry -Action List -Project $wtD -Format Human
    Assert-T ($humanLegacyList.ExitCode -eq 0) 'human list with legacy rows failed.'
    $allOutputs.Add($humanLegacyList.StdOut)
    $humanShowD = Invoke-TRegistry -Action Show -Project $wtD -TaskId $legacyId -Format Human
    Assert-T ($humanShowD.ExitCode -eq 2) 'show of a legacy-only task is repository-scoped and must be BF_INVALID.'
    foreach ($output in $allOutputs) {
        Assert-T ($output -notmatch 'SECRET-PROMPT-VALUE') 'registry output leaked the legacy prompt content.'
        Assert-T ($output -notmatch '(?i)"(prompt|authorization|password|token|api[_-]?key)"\s*:') 'registry JSON output must not contain secret-shaped keys.'
    }

    # --- Section 13: staged activation through the CLI (trusted request schema v1) ------------------------
    $activateTask = (Get-TRegistryJson (Invoke-TRegistry -Action Create -Project $wtD -Title 'Activate me').StdOut).task_id
    $requestPath = Join-Path $testRoot 'activate-request.json'
    Write-TText $requestPath (Get-BFCanonicalJson (New-TTrustedRequest -TaskId $activateTask -Project $wtD))
    $activateRun = Invoke-TEntry -Arguments @('-Action', 'Activate', '-ProjectPath', $wtD, '-TaskId', $activateTask, '-InputFile', $requestPath, '-Format', 'Json')
    Assert-T ($activateRun.ExitCode -eq 11) "activate must exit 11 (staged), got $($activateRun.ExitCode)."
    $activateDoc = ($activateRun.Output | Where-Object { $_ -match '^\{' } | Select-Object -First 1) | ConvertFrom-Json
    Assert-TError $activateDoc 'BF_BLOCKED' 'activate staged error class.'
    Assert-T ($activateDoc.error.message -cmatch 'staged BLOCKED' -and $activateDoc.error.message -cmatch [regex]::Escape($activateTask)) 'activate blocker must state the staged outcome with the task identity.'
    $activateState = Read-BFRegistryTaskState -TasksRoot (Get-BFRegistryStore -ProjectRoot $wtD).TasksRoot -TaskId $activateTask
    Assert-T (@($activateState['revisions'])).Count -eq 1 'activate wrote to the journal.'
    Assert-T ([string](Get-BFObjectProperty $activateState['metadata'] 'status') -ceq 'planned') 'activate must keep the task planned.'

    $invalidContractPath = Join-Path $testRoot 'activate-invalid.json'
    Write-TText $invalidContractPath (Get-BFCanonicalJson (New-TTrustedRequest -TaskId $activateTask -Project $wtD -MissingContract))
    $activateInvalid = Invoke-TEntry -Arguments @('-Action', 'Activate', '-ProjectPath', $wtD, '-TaskId', $activateTask, '-InputFile', $invalidContractPath, '-Format', 'Json')
    Assert-T ($activateInvalid.ExitCode -eq 2) 'activate without the declared controller_contract must exit 2 (BF_INVALID).'

    $badPriorityPath = Join-Path $testRoot 'activate-bad-priority.json'
    Write-TText $badPriorityPath (Get-BFCanonicalJson (New-TTrustedRequest -TaskId $activateTask -Project $wtD -InvalidPriority))
    $activateBadPriority = Invoke-TEntry -Arguments @('-Action', 'Activate', '-ProjectPath', $wtD, '-TaskId', $activateTask, '-InputFile', $badPriorityPath, '-Format', 'Json')
    Assert-T ($activateBadPriority.ExitCode -eq 2) 'activate with an invalid priority must exit 2.'

    $mismatchRequestPath = Join-Path $testRoot 'activate-mismatch.json'
    Write-TText $mismatchRequestPath (Get-BFCanonicalJson (New-TTrustedRequest -TaskId ([guid]::NewGuid().ToString()) -Project $wtD))
    $activateMismatch = Invoke-TEntry -Arguments @('-Action', 'Activate', '-ProjectPath', $wtD, '-TaskId', $activateTask, '-InputFile', $mismatchRequestPath, '-Format', 'Json')
    Assert-T ($activateMismatch.ExitCode -eq 2) 'activate with a mismatched task_id must exit 2.'

    $foreignRootPath = Join-Path $testRoot 'activate-foreign.json'
    Write-TText $foreignRootPath (Get-BFCanonicalJson (New-TTrustedRequest -TaskId $activateTask -Project $repoB))
    $activateForeign = Invoke-TEntry -Arguments @('-Action', 'Activate', '-ProjectPath', $wtD, '-TaskId', $activateTask, '-InputFile', $foreignRootPath, '-Format', 'Json')
    Assert-T ($activateForeign.ExitCode -eq 2) 'activate with a project_root from another clone must exit 2.'

    $missingRootPath = Join-Path $testRoot 'activate-missing-root.json'
    Write-TText $missingRootPath (Get-BFCanonicalJson (New-TTrustedRequest -TaskId $activateTask -Project (Join-Path $testRoot 'does-not-exist')))
    $activateMissingRoot = Invoke-TEntry -Arguments @('-Action', 'Activate', '-ProjectPath', $wtD, '-TaskId', $activateTask, '-InputFile', $missingRootPath, '-Format', 'Json')
    Assert-T ($activateMissingRoot.ExitCode -eq 2) 'activate with a nonexistent project_root must exit 2.'

    $activateNoFile = Invoke-TEntry -Arguments @('-Action', 'Activate', '-ProjectPath', $wtD, '-TaskId', $activateTask, '-Format', 'Json')
    Assert-T ($activateNoFile.ExitCode -eq 2) 'activate without -InputFile must exit 2.'

    $activateMissing = Invoke-TEntry -Arguments @('-Action', 'Activate', '-ProjectPath', $wtD, '-TaskId', ([guid]::NewGuid().ToString()), '-InputFile', $requestPath, '-Format', 'Json')
    Assert-T ($activateMissing.ExitCode -eq 2) 'activate of an unknown task must exit 2.'

    # --- Section 14: run/next guard against planned repository tasks ---------------------------------------
    $guardStore = Get-BFRegistryStore -ProjectRoot $wtD
    $runRun = Invoke-TEntry -Arguments @('-Action', 'Run', '-ProjectPath', $wtD, '-TaskId', $activateTask)
    Assert-T ($runRun.ExitCode -eq 11) "run against a planned repository task must exit 11, got $($runRun.ExitCode)."
    $runOutput = ($runRun.Output -join "`n")
    Assert-T ($runOutput -cmatch 'BF_BLOCKED' -and $runOutput -cmatch 'planned in the repository task registry') 'run must return an explicit activation blocker.'
    Assert-T (-not [IO.Directory]::Exists((Join-Path (Join-Path $wtD '.bsl-flow') ('worktrees\' + $activateTask)))) 'run must not create a worker attempt for a planned repository task.'
    Assert-T (@((Read-BFRegistryTaskState -TasksRoot $guardStore.TasksRoot -TaskId $activateTask)['revisions'])).Count -eq 1 'run mutated the planned journal.'
    # Next is a read-only action: the existing controller contract keeps the
    # exit at 0 and exposes the explicit activation blocker in the envelope.
    $runNext = Invoke-TEntry -Arguments @('-Action', 'Next', '-ProjectPath', $wtD, '-TaskId', $activateTask)
    $nextEnvelope = ($runNext.Output | Where-Object { $_ -match '^\{' } | Select-Object -First 1) | ConvertFrom-Json
    Assert-T (@($nextEnvelope.blockers)[0] -cmatch '^BF_BLOCKED:.*planned in the repository task registry') 'next against a planned repository task must expose the explicit activation blocker.'

    # --- Section 15: entry-point surface, thin wrapper, error contracts -------------------------------------
    $cliCreate = Invoke-TEntry -Arguments @('-Action', 'Create', '-ProjectPath', $wtD, '-Title', 'From CLI', '-Priority', 'critical', '-Format', 'Json')
    Assert-T ($cliCreate.ExitCode -eq 0) 'CLI create failed.'
    $cliCreateLines = @($cliCreate.Output | Where-Object { $_ -match '\S' })
    Assert-T ($cliCreateLines.Count -eq 1) 'CLI Json output must be exactly one document line.'
    $cliDoc = $cliCreateLines[0] | ConvertFrom-Json
    Assert-T ($cliDoc.schema_version -eq 1 -and $cliDoc.status -ceq 'planned' -and $cliDoc.priority -ceq 'critical') 'CLI create document is invalid.'
    Assert-T ($cliDoc.revision -cmatch '^[0-9a-f]{64}$') 'CLI create must report the revision hash.'
    $cliHuman = Invoke-TEntry -Arguments @('-Action', 'Show', '-ProjectPath', $wtD, '-TaskId', $cliDoc.task_id, '-Format', 'Human')
    Assert-T ($cliHuman.ExitCode -eq 0 -and (($cliHuman.Output -join "`n") -cmatch 'title: From CLI')) 'CLI human show failed.'
    $badFormat = Invoke-TEntry -Arguments @('-Action', 'List', '-ProjectPath', $wtD, '-Format', 'Xml')
    Assert-T ($badFormat.ExitCode -eq 2) 'unknown format must be BF_INVALID.'
    $noTitle = Invoke-TEntry -Arguments @('-Action', 'Create', '-ProjectPath', $wtD, '-Format', 'Json')
    Assert-T ($noTitle.ExitCode -eq 2) 'CLI create without a title must exit 2.'
    $humanError = Invoke-TEntry -Arguments @('-Action', 'Create', '-ProjectPath', $wtD)
    Assert-T ($humanError.ExitCode -eq 2 -and (($humanError.Output -join "`n") -cmatch '^BF_INVALID:')) 'human error output must be a concise BF_INVALID stderr line.'
    $jsonError = Invoke-TEntry -Arguments @('-Action', 'Create', '-ProjectPath', $wtD, '-Format', 'Json')
    $jsonErrorDoc = ($jsonError.Output | Where-Object { $_ -match '^\{' } | Select-Object -First 1) | ConvertFrom-Json
    Assert-TError $jsonErrorDoc 'BF_INVALID' 'CLI JSON error shape.'
    # Thin CLI wrapper: bsl-flow task <subcommand> forwards to the controller.
    Assert-T ([IO.File]::Exists($wrapper)) 'scripts/bsl-flow.ps1 wrapper is missing.'
    $wrapperList = @(& $wrapper 'task' 'list' '--project' $wtD '-Format' 'Json' 2>&1)
    Assert-T ($LASTEXITCODE -eq 0) "wrapper task list failed: $($wrapperList -join ' ')"
    $wrapperDoc = (@($wrapperList | Where-Object { $_ -match '^\{' }) | Select-Object -First 1) | ConvertFrom-Json
    Assert-T ($wrapperDoc.schema_version -eq 1 -and @($wrapperDoc.tasks).Count -ge 1) 'wrapper list must return the repository tasks.'
    $wrapperCreate = @(& $wrapper 'task' 'create' '-ProjectPath' $wtD '-Title' 'From wrapper' '-Format' 'Json' 2>&1)
    Assert-T ($LASTEXITCODE -eq 0) "wrapper task create failed: $($wrapperCreate -join ' ')"
    $wrapperCreated = (@($wrapperCreate | Where-Object { $_ -match '^\{' }) | Select-Object -First 1) | ConvertFrom-Json
    Assert-T ($wrapperCreated.status -ceq 'planned' -and $wrapperCreated.revision -cmatch '^[0-9a-f]{64}$') 'wrapper create document is invalid.'
    $wrapperBad = @(& $wrapper 'task' 'frobnicate' 2>&1)
    Assert-T ($LASTEXITCODE -eq 2) 'unknown wrapper subcommand must exit 2.'

    # --- Section 16: worktree deletion keeps the store ------------------------------------------------------
    Invoke-TGit -C $repoD worktree remove --force $wtD | Out-Null
    Assert-T (-not [IO.Directory]::Exists($wtD)) 'worktree removal fixture failed.'
    $storeDAfter = Get-BFRegistryStore -ProjectRoot $repoD
    Assert-T ($storeDAfter.RepositoryId -ceq $storeD.RepositoryId) 'store identity changed after worktree deletion.'
    $listAfterRemoval = Get-TRegistryJson (Invoke-TRegistry -Action List -Project $repoD).StdOut
    Assert-T (@($listAfterRemoval.tasks | Where-Object { $_.task_id -ceq $activateTask -and $_.diagnostic_state -eq $null }).Count -eq 1) 'store must survive worktree deletion with tasks intact.'
    Assert-T ([string]::IsNullOrEmpty((Invoke-TGit -C $repoD status --porcelain))) 'registry store must never appear in Git status after worktree deletion.'

    Write-Host ("Task registry suite passed ({0} checks) on PowerShell {1}." -f $script:checks, $PSVersionTable.PSVersion)
}
finally {
    $fullTestRoot = [IO.Path]::GetFullPath($testRoot)
    $tempParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar)
    $expectedPrefix = $tempParent + [IO.Path]::DirectorySeparatorChar + 'bsl-flow-task-registry-'
    if (-not $fullTestRoot.StartsWith($expectedPrefix, [StringComparison]::OrdinalIgnoreCase) -or [IO.Path]::GetDirectoryName($fullTestRoot).TrimEnd([IO.Path]::DirectorySeparatorChar) -ne $tempParent) {
        throw "Unsafe registry test cleanup target: $fullTestRoot"
    }
    git worktree prune 2>$null | Out-Null
    if (Test-Path -LiteralPath $fullTestRoot) { Remove-Item -LiteralPath $fullTestRoot -Recurse -Force }
}
