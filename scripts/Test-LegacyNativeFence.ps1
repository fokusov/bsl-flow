#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$PackageRoot = [IO.Path]::GetFullPath($PackageRoot)
$storage = Join-Path $PackageRoot 'global\skills\1c-task\scripts\Task.Storage.ps1'
if (-not (Test-Path -LiteralPath $storage -PathType Leaf)) { throw "Missing storage module: $storage" }
. $storage

$script:checks = 0
function Assert-Fence {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "ASSERTION FAILED: $Message" }
    $script:checks++
}

function Expect-FenceError {
    param([scriptblock]$Action, [string]$Prefix, [string]$Message)
    $observed = ''
    try { & $Action | Out-Null }
    catch { $observed = $_.Exception.Message }
    Assert-Fence ($observed.StartsWith($Prefix + ':')) ("$Message Observed: $observed")
}

function Write-FenceText {
    param([string]$Path, [string]$Text)
    $parent = Split-Path -Parent $Path
    if (-not [string]::IsNullOrEmpty($parent)) { [void][IO.Directory]::CreateDirectory($parent) }
    [IO.File]::WriteAllText($Path, $Text, [Text.UTF8Encoding]::new($false))
}

function Invoke-FenceGit {
    param([string]$Directory, [string[]]$Arguments)
    $output = & git -c core.hooksPath=NUL -c core.fsmonitor=false --no-optional-locks -C $Directory @Arguments 2>&1
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) { throw "Git fixture setup failed ($exitCode): $((@($output) -join "`n").Trim())" }
    return (@($output) -join "`n").Trim()
}

function Restore-FenceEnvironment {
    param([Collections.IDictionary]$Saved)
    foreach ($key in $Saved.Keys) {
        if ($null -eq $Saved[$key]) { Remove-Item -LiteralPath ('Env:' + $key) -ErrorAction SilentlyContinue }
        else { [Environment]::SetEnvironmentVariable($key, [string]$Saved[$key], 'Process') }
    }
}

$workRoot = Join-Path (Join-Path $PackageRoot 'work') ('legacy-native-fence-' + [guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($workRoot)
$project = Join-Path $workRoot 'project'
$sourceTask = $null
$sourceLock = $null
try {
    [void][IO.Directory]::CreateDirectory($project)
    [void](Invoke-FenceGit $project @('init', '--quiet'))
    Write-FenceText (Join-Path $project 'README.md') "legacy fence fixture`n"
    Write-FenceText (Join-Path $project '.gitignore') ".bsl-flow/`nordinary/`n"
    [void](Invoke-FenceGit $project @('add', '.'))
    [void](Invoke-FenceGit $project @('-c', 'user.name=BSL Flow Fence Test', '-c', 'user.email=fence@example.invalid', 'commit', '--quiet', '-m', 'fixture'))

    $hookMarker = Join-Path $workRoot 'hook-ran.txt'
    $hookDirectory = Join-Path $workRoot 'hostile-hooks'
    [void][IO.Directory]::CreateDirectory($hookDirectory)
    # The guard overrides hooksPath/fsmonitor for the identity probe.  This
    # hook is intentionally hostile and must never run during the test.
    Write-FenceText (Join-Path $hookDirectory 'pre-commit') "Set-Content -LiteralPath '$hookMarker' hook-ran`nexit 1`n"
    [void](Invoke-FenceGit $project @('config', 'core.hooksPath', $hookDirectory))
    [void](Invoke-FenceGit $project @('config', 'core.fsmonitor', 'true'))

    $taskId = [guid]::NewGuid().ToString().ToLowerInvariant()
    $legacyTask = Join-Path $project ('.bsl-flow\tasks\' + $taskId)
    $verified = Assert-BFLegacyTaskWriteAllowed $legacyTask
    Assert-Fence ($verified.CommonDir -ceq (Assert-BFSafePath (Join-Path $project '.git'))) 'Guard resolved the actual Git common dir.'
    Assert-Fence ($verified.CanonicalTask -ceq (Assert-BFSafePath (Join-Path $verified.CanonicalTasks $taskId))) 'Guard resolved the canonical UUID path.'

    # Ambient Git routing/configuration must not redirect the verification to a
    # decoy repository or execute a configured credential helper/hook.
    $environmentNames = @('GIT_DIR', 'GIT_WORK_TREE', 'GIT_COMMON_DIR', 'GIT_INDEX_FILE', 'GIT_OBJECT_DIRECTORY', 'GIT_ALTERNATE_OBJECT_DIRECTORIES', 'GIT_CONFIG_COUNT', 'GIT_CONFIG_KEY_0', 'GIT_CONFIG_VALUE_0')
    $savedEnvironment = [ordered]@{}
    foreach ($name in $environmentNames) { $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
    try {
        $env:GIT_DIR = Join-Path $workRoot 'decoy.git'
        $env:GIT_WORK_TREE = Join-Path $workRoot 'decoy-worktree'
        $env:GIT_COMMON_DIR = Join-Path $workRoot 'decoy-common'
        $env:GIT_INDEX_FILE = Join-Path $workRoot 'decoy-index'
        $env:GIT_OBJECT_DIRECTORY = Join-Path $workRoot 'decoy-objects'
        $env:GIT_ALTERNATE_OBJECT_DIRECTORIES = Join-Path $workRoot 'decoy-alternates'
        $env:GIT_CONFIG_COUNT = '1'
        $env:GIT_CONFIG_KEY_0 = 'credential.helper'
        $env:GIT_CONFIG_VALUE_0 = ('!powershell.exe -NoProfile -Command "Set-Content -LiteralPath ''{0}'' helper-ran"' -f $hookMarker)
        $underHostileEnvironment = Assert-BFLegacyTaskWriteAllowed $legacyTask
        Assert-Fence ($underHostileEnvironment.CommonDir -ceq $verified.CommonDir) 'Inherited Git routing variables did not redirect common-dir identity.'
    }
    finally { Restore-FenceEnvironment $savedEnvironment }
    Assert-Fence (-not (Test-Path -LiteralPath $hookMarker)) 'Hooks and injected credential helpers did not execute during identity verification.'

    # A normal non-task path retains generic Write-BFJson behavior.
    $genericPath = Join-Path $project 'ordinary\value.json'
    [void](Write-BFJson -Path $genericPath -Value ([ordered]@{ value = 1 }))
    Assert-Fence ((Read-BFJson $genericPath).value -eq 1) 'Non-task JSON writes remain available.'

    # With no canonical target, a real legacy task root may be created and
    # written.  Enter-BFLock performs the second check under source lock.
    $sourceLock = Enter-BFLock $legacyTask
    try {
        [void](Write-BFJson -Path (Join-Path $legacyTask 'inputs\initial-request.json') -Value ([ordered]@{ task_id = $taskId }))
    }
    finally { $sourceLock.Dispose(); $sourceLock = $null }
    Assert-Fence (Test-Path -LiteralPath (Join-Path $legacyTask 'inputs\initial-request.json') -PathType Leaf) 'Legacy task write succeeds while canonical UUID is absent.'

    # Direct task-local JSON writes must take and release the exact source
    # lock themselves when no caller lock is held.
    $implicitId = [guid]::NewGuid().ToString().ToLowerInvariant()
    $implicitLegacy = Join-Path $project ('.bsl-flow\tasks\' + $implicitId)
    [void](Write-BFJson -Path (Join-Path $implicitLegacy 'inputs\direct.json') -Value ([ordered]@{ task_id = $implicitId }))
    Assert-Fence (Test-Path -LiteralPath (Join-Path $implicitLegacy 'inputs\direct.json') -PathType Leaf) 'Unlocked direct legacy JSON write did not complete.'
    Assert-Fence (-not (Test-BFStorageTaskLockHeld $implicitLegacy)) 'Implicit source lock was not released after direct JSON write.'

    # Any canonical UUID leaf owns the identity, regardless of whether its
    # contents are valid JSON, a directory, or a regular file.  The preflight
    # occurs before source task directories or child files are created.
    foreach ($targetKind in @('directory', 'file')) {
        $blockedId = [guid]::NewGuid().ToString().ToLowerInvariant()
        $blockedLegacy = Join-Path $project ('.bsl-flow\tasks\' + $blockedId)
        $blockedCanonical = Join-Path $verified.CanonicalTasks $blockedId
        if ($targetKind -eq 'directory') {
            [void][IO.Directory]::CreateDirectory((Join-Path $blockedCanonical 'revisions'))
            Write-FenceText (Join-Path $blockedCanonical 'current.json') '{broken'
        }
        else {
            [void][IO.Directory]::CreateDirectory($verified.CanonicalTasks)
            [IO.File]::WriteAllText($blockedCanonical, 'not a task directory', [Text.UTF8Encoding]::new($false))
        }
        Expect-FenceError { Write-BFJson -Path (Join-Path $blockedLegacy 'inputs\must-not-exist.json') -Value @{ blocked = $true } } 'BF_CONFLICT' ("Canonical $targetKind target did not fence Write-BFJson")
        Expect-FenceError { Enter-BFLock $blockedLegacy } 'BF_CONFLICT' ("Canonical $targetKind target did not fence Enter-BFLock")
        Expect-FenceError { Write-BFRevision -Directory $blockedLegacy -State @{ task_id = $blockedId } -ExpectedRevision 0 } 'BF_CONFLICT' ("Canonical $targetKind target did not fence Write-BFRevision")
        Assert-Fence (-not (Test-Path -LiteralPath $blockedLegacy)) ("$targetKind owner check created a legacy task directory")
    }

    # Start a genuine checkout-local legacy task, retain the old state object,
    # then simulate native adoption by occupying the canonical UUID.  A stale
    # captured Save-BFTask must fail before appending a revision.
    foreach ($name in @('Task.Contracts.ps1', 'Task.Gates.ps1', 'Task.Engine.ps1')) { . (Join-Path $PackageRoot ('global\skills\1c-task\scripts\' + $name)) }
    $request = [pscustomobject]@{
        schema_version = 1
        request_id = ([guid]::NewGuid().ToString().ToLowerInvariant())
        prompt = 'Fence fixture task.'
        mode = 'analysis_only'
        analysis_goal = 'analysis'
        complexity = 'S'
        risk = 'low'
        impact_flags = @()
        criteria = @([pscustomobject]@{ id = 'fixture'; observation = 'The fixture remains source-only.'; kind = 'file_assertion'; path = 'README.md'; contains = 'legacy fence fixture' })
        provenance = [pscustomobject]@{ source = 'user'; reference = 'legacy-fence-fixture'; text = 'Create a source-only fence fixture.' }
        models = [pscustomobject]@{ worker = 'gpt-6-astra'; worker_effort = 'medium'; reviewer = 'gpt-6-astra'; reviewer_effort = 'high' }
    }
    $sourceState = Start-BFTask $project $request
    $sourceTask = Get-BFTaskDirectory $project $request.request_id
    $oldRevision = [int64]$sourceState.revision
    $oldRevisionFiles = @(Get-ChildItem -LiteralPath (Join-Path $sourceTask 'revisions') -File -Filter '*.json').Count
    $sourceOwner = Assert-BFLegacyTaskWriteAllowed $sourceTask
    $canonicalSourceTask = $sourceOwner.CanonicalTask
    [void][IO.Directory]::CreateDirectory($canonicalSourceTask)
    Expect-FenceError { Save-BFTask $sourceState $oldRevision } 'BF_CONFLICT' 'A stale captured legacy Save-BFTask was accepted after canonical ownership appeared.'
    Assert-Fence (@(Get-ChildItem -LiteralPath (Join-Path $sourceTask 'revisions') -File -Filter '*.json').Count -eq $oldRevisionFiles) 'Blocked captured save appended no legacy revision.'
    Expect-FenceError { Enter-BFLock $sourceTask } 'BF_CONFLICT' 'A later legacy lock acquisition ignored canonical ownership.'

    # Deterministic race-boundary check: acquire the source lock while the
    # canonical target is absent, pass the first check, publish the target, and
    # require the under-lock recheck to reject the stale writer.
    $raceId = [guid]::NewGuid().ToString().ToLowerInvariant()
    $raceLegacy = Join-Path $project ('.bsl-flow\tasks\' + $raceId)
    $raceOwner = Assert-BFLegacyTaskWriteAllowed $raceLegacy
    $sourceLock = Enter-BFLock $raceLegacy
    try {
        [void](Assert-BFLegacyTaskWriteAllowed $raceLegacy -UnderSourceLock)
        $raceCanonical = $raceOwner.CanonicalTask
        [void][IO.Directory]::CreateDirectory($raceCanonical)
        Expect-FenceError { Assert-BFLegacyTaskWriteAllowed $raceLegacy -UnderSourceLock } 'BF_CONFLICT' 'Under-lock ownership recheck missed a canonical target published at the race boundary.'
        Expect-FenceError { Write-BFRevision -Directory $raceLegacy -State @{ task_id = $raceId } -ExpectedRevision 0 } 'BF_CONFLICT' 'Write-BFRevision crossed the under-lock ownership race boundary.'
    }
    finally { $sourceLock.Dispose(); $sourceLock = $null }

    Write-Host ("Legacy/native fence contracts passed with {0} checks on PowerShell {1}." -f $script:checks, $PSVersionTable.PSVersion)
}
finally {
    if ($null -ne $sourceLock) { $sourceLock.Dispose() }
    $fullRoot = [IO.Path]::GetFullPath($workRoot)
    $workParent = [IO.Path]::GetFullPath((Join-Path $PackageRoot 'work')).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
    $expectedPrefix = $workParent + [IO.Path]::DirectorySeparatorChar + 'legacy-native-fence-'
    if (-not $fullRoot.StartsWith($expectedPrefix, [StringComparison]::OrdinalIgnoreCase) -or [IO.Path]::GetDirectoryName($fullRoot).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar) -ne $workParent) {
        throw "Unsafe fence test cleanup target: $fullRoot"
    }
    if (Test-Path -LiteralPath $fullRoot) { Remove-Item -LiteralPath $fullRoot -Recurse -Force }
}
