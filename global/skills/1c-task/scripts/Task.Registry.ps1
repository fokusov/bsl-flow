#Requires -Version 7.0
# Repository task registry (schema/store version v1).
#
# Store path (frozen by the 2026-09-16 re-anchor):
#   <verified absolute git common dir>/bsl-flow/tasks/<uuid>/revisions/000001.json ...
# Every worktree of one clone resolves the same store through the verified
# common dir. The store is clone-local data and must never be committed to Git.
#
# repository_id (frozen): the lowercase first 16 hex chars of the SHA-256 over
# the UTF-8 bytes of the full normalized common-dir path (Assert-BFSafePath
# form, no trailing separator). It identifies the clone, not the Git remote.
#
# Revision journal: every revision file is one canonical JSON document with
#   schema_version=1, task_id (lowercase UUID), revision (1-based int),
#   op (create|edit|archive|unarchive), parent_hash ('0'*64 for revision 1),
#   hash = lowercase SHA-256 hex over Get-BFCanonicalJson of the revision
#   object without the hash field, timestamp_utc (UTC ISO-8601 with a fixed
#   7-digit fraction) and a full metadata snapshot as payload:
#   title, description, priority, labels, depends_on, status, archived.
# Metadata constants (published spec, requirement 3): title trimmed 1-200,
#   description 0-5000, priority default 'medium', labels trimmed 1-50 chars
#   case-sensitively unique with at most 20 entries, depends_on unique
#   UUID-v4 strings whose existence is deliberately not verified.
# v1 creates planned tasks only; payloads carry no execution fields. UUID
# deletion and reuse are forbidden. current.json or any other derived artifact
# is never authoritative: every read rebuilds from the journals, and the
# engine itself creates no derived files.
#
# Graph safety: dependency cycle validation and revision publication run inside
# <tasks root>/bsl-flow-graph.lock (FileShare.None, ~10s retry with stale-age
# detection), so concurrent writers validate and publish on one snapshot.
# Missing depends_on targets are allowed and stored as-is; only self-reference,
# duplicate ids in one depends_on, and cycles over existing valid journals are
# rejected (BF_CONFLICT, no revision written).
#
# Corruption taxonomy (requirement 15): a task is `corrupt` on JSON parse
# failure, missing required revision fields, or a hash-chain mismatch;
# `conflict` when the same task_id has a differing verified history between
# the canonical store and legacy discovery (verified history hash = SHA-256
# over the concatenation of the 64-hex revision hashes in chronological
# order, requirement 17); `orphaned` when a task directory or legacy path has
# no verifiable chain root. Diagnostic entries keep their identity and health
# value visible; show/history of a damaged task fail BF_BLOCKED with the
# exact reason.
#
# Activation: `Activate` validates the trusted request schema v1 (requirement
# 5: task_id, project_root inside this clone, title/priority/labels/depends_on
# per requirement 3, controller_contract 'repository-store-aware/v1') against
# the planned task, then returns a staged BF_BLOCKED without appending a
# revision; enabling the controller write slice is a later increment. The
# stale-completed overview concept is deferred by the specification
# (requirement 7) and therefore omitted in v1.
#
# Output contract: JSON mode emits exactly one versioned document per command
# (normative schemas v1); errors and the staged activation emit exactly
# { "error": { "class": "BF_INVALID|BF_BLOCKED|BF_CONFLICT", "message": ... } };
# human mode keeps a concise stderr line whose first field is the error class.
# Exit codes: 0 success, 2 BF_INVALID, 11 BF_BLOCKED/BF_CONFLICT.
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Task.Storage.ps1')

$script:BFRegistryUuidPattern = '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
$script:BFRegistryUuidV4Pattern = '^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$'
$script:BFRegistryPriorities = @('low', 'medium', 'high', 'critical')
$script:BFRegistryStatuses = @('planned', 'ready', 'running', 'needs_input', 'blocked', 'failed', 'completed', 'cancelled')
$script:BFRegistryStages = @('inspect', 'spec', 'spec_review', 'implement', 'code_review', 'verify', 'diagnose', 'acceptance')
$script:BFRegistryOps = @('create', 'edit', 'archive', 'unarchive')
$script:BFRegistryTitleLimit = 200
$script:BFRegistryDescriptionLimit = 5000
$script:BFRegistryLabelLimit = 50
$script:BFRegistryLabelMaximum = 20
$script:BFRegistryListDefaultLimit = 50
$script:BFRegistryListMaximumLimit = 200
$script:BFRegistryGenesisHash = '0' * 64
$script:BFRegistrySortFields = @('created_at', 'updated_at', 'priority', 'status', 'title')
$script:BFRegistryArchivedModes = @('false', 'true', 'all')
$script:BFRegistryControllerContract = 'repository-store-aware/v1'
$script:BFRegistryFreshnessFields = @('baseline_path', 'worker_path', 'evidence_path')
$script:BFRegistryOverviewCountKeys = @('needs_input', 'blocked', 'running', 'completed', 'planned', 'archived', 'corrupt', 'orphaned')

function Assert-BFRegistryUuid {
    [CmdletBinding()]
    param([AllowNull()][string]$Value)
    if ([string]::IsNullOrWhiteSpace($Value) -or $Value -cnotmatch $script:BFRegistryUuidPattern) {
        throw (New-BFError 'BF_INVALID' 'Task id must be a canonical lower-case UUID.')
    }
}

function Get-BFRegistryTimestamp {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][DateTimeOffset]$Value)
    return $Value.ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ss.fffffff'Z'", [System.Globalization.CultureInfo]::InvariantCulture)
}

function ConvertTo-BFRegistryTimestamp {
    # Strict parsing for caller-supplied filter bounds; a bad value is a contract error.
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][AllowNull()][string]$Value)
    if ([string]::IsNullOrWhiteSpace($Value)) { throw (New-BFError 'BF_INVALID' 'Timestamp must not be empty.') }
    try {
        $parsed = [DateTimeOffset]::Parse($Value, [System.Globalization.CultureInfo]::InvariantCulture, [System.Globalization.DateTimeStyles]::RoundtripKind)
    }
    catch { throw (New-BFError 'BF_INVALID' ("Timestamp is not valid ISO-8601: {0}" -f $Value)) }
    return (Get-BFRegistryTimestamp $parsed)
}

function ConvertTo-BFRegistryNormalizedTimestamp {
    # Lenient parsing for stored metadata; unparseable stored values normalize
    # to the empty string so projections stay deterministic.
    [CmdletBinding()]
    param([AllowNull()][string]$Value)
    if ([string]::IsNullOrWhiteSpace($Value)) { return '' }
    try {
        $parsed = [DateTimeOffset]::Parse($Value, [System.Globalization.CultureInfo]::InvariantCulture, [System.Globalization.DateTimeStyles]::RoundtripKind)
        return (Get-BFRegistryTimestamp $parsed)
    }
    catch { return '' }
}

function Get-BFRegistryStore {
    # One clone-local store per verified Git common dir; all worktrees of the
    # clone share it. Mirrors the hardened common-dir resolution of the legacy
    # fence (Get-BFVerifiedLegacyGitContext) without weakening it.
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$ProjectRoot)
    $root = Assert-BFSafePath $ProjectRoot
    if (-not [System.IO.Directory]::Exists($root)) { throw (New-BFError 'BF_INVALID' 'Registry project root is not a directory.') }
    $topLevel = Assert-BFSafePath (Invoke-BFStorageGitRead $root @('rev-parse', '--show-toplevel'))
    if (-not [string]::Equals($topLevel, $root, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw (New-BFError 'BF_INVALID' 'Registry commands require the exact Git worktree root.')
    }
    $commonText = Invoke-BFStorageGitRead $root @('rev-parse', '--path-format=absolute', '--git-common-dir')
    if ([string]::IsNullOrWhiteSpace($commonText)) { throw (New-BFError 'BF_BLOCKED' 'Git common dir is empty.') }
    $commonDir = if ([System.IO.Path]::IsPathRooted($commonText)) { Assert-BFSafePath $commonText } else { Assert-BFSafePath (Join-Path $root $commonText) }
    if (-not [System.IO.Directory]::Exists($commonDir)) { throw (New-BFError 'BF_BLOCKED' 'Git common dir is not a directory.') }
    $tasksRoot = Assert-BFSafePath (Join-Path (Join-Path $commonDir 'bsl-flow') 'tasks')
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { $identityBytes = $sha.ComputeHash([System.Text.UTF8Encoding]::new($false, $true).GetBytes($commonDir)) }
    finally { $sha.Dispose() }
    $repositoryId = [System.BitConverter]::ToString($identityBytes).Replace('-', '').ToLowerInvariant().Substring(0, 16)
    return [pscustomobject]@{
        ProjectRoot  = $root
        CommonDir    = $commonDir
        TasksRoot    = $tasksRoot
        RepositoryId = $repositoryId
    }
}

function Test-BFRegistryPlannedTask {
    # Run/Next guard. Best-effort by design: any resolution problem falls back
    # to legacy controller behavior, which then produces its own error for the
    # unknown task. Only a verified healthy planned repository task blocks.
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$ProjectPath,
        [Parameter(Mandatory = $true)][string]$TaskId
    )
    try {
        if ($TaskId -cnotmatch $script:BFRegistryUuidPattern) { return $false }
        $store = Get-BFRegistryStore -ProjectRoot $ProjectPath
        $state = Read-BFRegistryTaskState -TasksRoot $store.TasksRoot -TaskId $TaskId
        if ($null -eq $state -or $state['health'] -cne 'ok') { return $false }
        return ([string](Get-BFObjectProperty $state['metadata'] 'status') -ceq 'planned')
    }
    catch { return $false }
}

function Enter-BFRegistryGraphLock {
    # Serializes dependency validation with revision publication so concurrent
    # writers cannot both pass cycle validation on the same snapshot. The lock
    # file is held with FileShare.None; a crashed writer's lock is detected by
    # age and (only when no process holds it anymore) removed.
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$TasksRoot)
    try { [void][System.IO.Directory]::CreateDirectory($TasksRoot) }
    catch { throw (New-BFError 'BF_BLOCKED' ("Cannot create the repository store directory: {0}" -f $_.Exception.Message)) }
    $lockPath = Join-Path $TasksRoot 'bsl-flow-graph.lock'
    $deadline = [DateTime]::UtcNow.AddSeconds(10)
    $staleAge = [TimeSpan]::FromMinutes(10)
    while ($true) {
        try {
            return [System.IO.File]::Open($lockPath, [System.IO.FileMode]::OpenOrCreate, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
        }
        catch [System.IO.IOException] {
            if ([DateTime]::UtcNow -ge $deadline) { throw (New-BFError 'BF_CONFLICT' 'Repository graph lock is held by another writer.') }
            try {
                $info = [System.IO.FileInfo]::new($lockPath)
                if ($info.Exists -and ([DateTime]::UtcNow - $info.LastWriteTimeUtc) -gt $staleAge) { [System.IO.File]::Delete($lockPath) }
            }
            catch [System.IO.IOException] { }
            Start-Sleep -Milliseconds 250
        }
    }
}

function Get-BFRegistryValueList {
    # Normalizes forwarded CLI string arrays: an omitted [string[]] parameter
    # binds as @($null), which must behave exactly like an absent filter or
    # field, not like one empty entry. -SplitComma additionally expands
    # comma-separated filter values ('planned,ready') into distinct entries.
    [CmdletBinding()]
    param(
        [AllowNull()][object[]]$Values,
        [switch]$SplitComma
    )
    $list = [System.Collections.Generic.List[string]]::new()
    foreach ($value in @($Values)) {
        if ($null -eq $value) { continue }
        $text = [string]$value
        if ($SplitComma -and $text.Contains(',')) {
            foreach ($part in $text.Split(',')) {
                $trimmed = $part.Trim()
                if (-not [string]::IsNullOrEmpty($trimmed)) { $list.Add($trimmed) }
            }
            continue
        }
        if (-not [string]::IsNullOrEmpty($text)) { $list.Add($text) }
    }
    if ($list.Count -eq 0) { return $null }
    return @($list.ToArray())
}

function New-BFRegistryPayload {
    # Validates user metadata against the published requirement-3 constants and
    # returns the closed v1 payload snapshot.
    [CmdletBinding()]
    param(
        [AllowNull()][string]$Title,
        [AllowNull()][string]$Description,
        [AllowNull()][string]$Priority,
        [AllowNull()][string[]]$Labels,
        [AllowNull()][string[]]$DependsOn,
        [string]$Status = 'planned',
        [bool]$Archived = $false
    )
    $titleValue = if ($null -eq $Title) { '' } else { $Title.Trim() }
    if ([string]::IsNullOrEmpty($titleValue)) { throw (New-BFError 'BF_INVALID' 'A non-empty title is required.') }
    if ($titleValue.Length -gt $script:BFRegistryTitleLimit) { throw (New-BFError 'BF_INVALID' ("Title exceeds {0} characters." -f $script:BFRegistryTitleLimit)) }
    $descriptionValue = if ($null -eq $Description) { '' } else { $Description }
    if ($descriptionValue.Length -gt $script:BFRegistryDescriptionLimit) { throw (New-BFError 'BF_INVALID' ("Description exceeds {0} characters." -f $script:BFRegistryDescriptionLimit)) }
    $priorityValue = if ([string]::IsNullOrWhiteSpace($Priority)) { 'medium' } else { $Priority }
    if ($priorityValue -cnotin $script:BFRegistryPriorities) { throw (New-BFError 'BF_INVALID' ("Priority must be one of: {0}." -f ($script:BFRegistryPriorities -join ', '))) }
    $statusValue = if ([string]::IsNullOrWhiteSpace($Status)) { 'planned' } else { $Status }
    if ($statusValue -cnotin $script:BFRegistryStatuses) { throw (New-BFError 'BF_INVALID' ("Status must be one of: {0}." -f ($script:BFRegistryStatuses -join ', '))) }
    $labelValues = [System.Collections.Generic.List[string]]::new()
    $seenLabels = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($label in @($Labels)) {
        if ($null -eq $label) { continue }
        $labelText = ([string]$label).Trim()
        if ([string]::IsNullOrEmpty($labelText)) { throw (New-BFError 'BF_INVALID' 'Labels must be non-empty.') }
        if ($labelText.Length -gt $script:BFRegistryLabelLimit) { throw (New-BFError 'BF_INVALID' ("Label exceeds {0} characters." -f $script:BFRegistryLabelLimit)) }
        if (-not $seenLabels.Add($labelText)) { throw (New-BFError 'BF_INVALID' ("Duplicate label: {0}" -f $labelText)) }
        $labelValues.Add($labelText)
    }
    if ($labelValues.Count -gt $script:BFRegistryLabelMaximum) { throw (New-BFError 'BF_INVALID' ("More than {0} labels are not supported." -f $script:BFRegistryLabelMaximum)) }
    $dependencyValues = [System.Collections.Generic.List[string]]::new()
    $seenDependencies = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($dependency in @($DependsOn)) {
        if ($null -eq $dependency) { continue }
        $dependencyText = [string]$dependency
        if ($dependencyText -cnotmatch $script:BFRegistryUuidV4Pattern) { throw (New-BFError 'BF_INVALID' 'depends_on entries must be canonical lower-case UUID v4 strings.') }
        if (-not $seenDependencies.Add($dependencyText)) {
            # Duplicate ids in one depends_on are a dependency-validation conflict
            # per the registry contract, not a plain input error.
            throw (New-BFError 'BF_CONFLICT' ("Duplicate depends_on id: {0}" -f $dependencyText))
        }
        $dependencyValues.Add($dependencyText)
    }
    return [ordered]@{
        title       = $titleValue
        description = $descriptionValue
        priority    = $priorityValue
        labels      = @($labelValues.ToArray())
        depends_on  = @($dependencyValues.ToArray())
        status      = $statusValue
        archived    = $Archived
    }
}

function Assert-BFRegistryPayload {
    # Closed v1 payload shape used by the journal reader; any deviation makes
    # the journal corrupt instead of silently changing task identity.
    [CmdletBinding()]
    param([AllowNull()]$Payload)
    if ($null -eq $Payload -or ($Payload -isnot [System.Collections.IDictionary] -and $Payload -isnot [pscustomobject])) {
        throw (New-BFError 'BF_INVALID' 'Revision payload must be an object.')
    }
    $required = @('title', 'description', 'priority', 'labels', 'depends_on', 'status', 'archived')
    $keys = if ($Payload -is [System.Collections.IDictionary]) { @($Payload.Keys) } else { @($Payload.PSObject.Properties | ForEach-Object { $_.Name }) }
    foreach ($name in $required) { if ($name -cnotin $keys) { throw (New-BFError 'BF_INVALID' ("Payload field {0} is missing." -f $name)) } }
    $title = ([string](Get-BFObjectProperty $Payload 'title')).Trim()
    if ([string]::IsNullOrEmpty($title) -or $title.Length -gt $script:BFRegistryTitleLimit) { throw (New-BFError 'BF_INVALID' 'Payload title is invalid.') }
    $description = [string](Get-BFObjectProperty $Payload 'description')
    if ($null -eq (Get-BFObjectProperty $Payload 'description') -or $description.Length -gt $script:BFRegistryDescriptionLimit) { throw (New-BFError 'BF_INVALID' 'Payload description is invalid.') }
    if ([string](Get-BFObjectProperty $Payload 'priority') -cnotin $script:BFRegistryPriorities) { throw (New-BFError 'BF_INVALID' 'Payload priority is invalid.') }
    if ([string](Get-BFObjectProperty $Payload 'status') -cnotin $script:BFRegistryStatuses) { throw (New-BFError 'BF_INVALID' 'Payload status is invalid.') }
    $archived = Get-BFObjectProperty $Payload 'archived'
    if ($archived -isnot [bool]) { throw (New-BFError 'BF_INVALID' 'Payload archived must be boolean.') }
    # Raw property access preserves empty and single-element JSON arrays;
    # Get-BFObjectProperty and if-expression output unroll one-element
    # collections into scalars, so these must be direct assignments.
    $labelsRaw = $null
    if ($Payload -is [System.Collections.IDictionary]) { $labelsRaw = $Payload['labels'] } else { $labelsRaw = $Payload.PSObject.Properties['labels'].Value }
    if ($null -eq $labelsRaw -or $labelsRaw -isnot [array]) { throw (New-BFError 'BF_INVALID' 'Payload labels must be an array.') }
    $seenLabels = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($label in $labelsRaw) {
        $labelText = [string]$label
        if ([string]::IsNullOrEmpty($labelText) -or $labelText.Length -gt $script:BFRegistryLabelLimit) { throw (New-BFError 'BF_INVALID' 'Payload label is invalid.') }
        if (-not $seenLabels.Add($labelText)) { throw (New-BFError 'BF_INVALID' 'Payload labels must be unique.') }
    }
    $dependenciesRaw = $null
    if ($Payload -is [System.Collections.IDictionary]) { $dependenciesRaw = $Payload['depends_on'] } else { $dependenciesRaw = $Payload.PSObject.Properties['depends_on'].Value }
    if ($null -eq $dependenciesRaw -or $dependenciesRaw -isnot [array]) { throw (New-BFError 'BF_INVALID' 'Payload depends_on must be an array.') }
    $seenDependencies = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
    foreach ($dependency in $dependenciesRaw) {
        $dependencyText = [string]$dependency
        if ($dependencyText -cnotmatch $script:BFRegistryUuidV4Pattern) { throw (New-BFError 'BF_INVALID' 'Payload depends_on entry is not a canonical lower-case UUID v4 string.') }
        if (-not $seenDependencies.Add($dependencyText)) { throw (New-BFError 'BF_INVALID' 'Payload depends_on entries must be unique.') }
    }
}

function Get-BFRegistryRevisionHash {
    # SHA-256 hex over the canonical serialization of the revision object
    # without the hash field (same SHA-256 discipline as Get-BFHash).
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)]$Revision)
    $withoutHash = [ordered]@{}
    if ($Revision -is [System.Collections.IDictionary]) {
        foreach ($key in @($Revision.Keys)) { $keyText = [string]$key; if ($keyText -cne 'hash') { $withoutHash[$keyText] = $Revision[$key] } }
    }
    else {
        foreach ($property in $Revision.PSObject.Properties) { if ($property.Name -cne 'hash') { $withoutHash[$property.Name] = $property.Value } }
    }
    return (Get-BFHash $withoutHash)
}

function Get-BFRegistryHistoryHash {
    # Verified history hash (requirement 17): SHA-256 hex over the UTF-8
    # concatenation of the 64-hex revision hashes in chronological order.
    [CmdletBinding()]
    param([AllowNull()][string[]]$RevisionHashes)
    $builder = [System.Text.StringBuilder]::new()
    foreach ($hash in @($RevisionHashes)) { [void]$builder.Append([string]$hash) }
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { return [System.BitConverter]::ToString($sha.ComputeHash([System.Text.UTF8Encoding]::new($false, $true).GetBytes($builder.ToString()))).Replace('-', '').ToLowerInvariant() }
    finally { $sha.Dispose() }
}

function Read-BFRegistryTaskState {
    # Rebuilds one task from its authoritative journal. Never throws for a
    # damaged journal: it returns a diagnostic state so list/overview keep the
    # remaining tasks visible. Returns $null when the task directory is absent.
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$TasksRoot,
        [Parameter(Mandatory = $true)][string]$TaskId
    )
    Assert-BFRegistryUuid $TaskId
    $taskDirectory = Join-Path $TasksRoot $TaskId
    if (-not [System.IO.Directory]::Exists($taskDirectory)) { return $null }
    $revisionDirectory = Join-Path $taskDirectory 'revisions'
    if (-not [System.IO.Directory]::Exists($revisionDirectory)) {
        return [ordered]@{ task_id = $TaskId; health = 'orphaned'; diagnostic = 'task directory has no revisions directory'; revisions = @(); latest = $null; metadata = $null; created_at = ''; updated_at = ''; revision = $null }
    }
    if ([System.IO.File]::Exists($revisionDirectory)) {
        return [ordered]@{ task_id = $TaskId; health = 'corrupt'; diagnostic = 'revisions is a file, not a directory'; revisions = @(); latest = $null; metadata = $null; created_at = ''; updated_at = ''; revision = $null }
    }
    $corrupt = {
        param([string]$Reason)
        return [ordered]@{ task_id = $TaskId; health = 'corrupt'; diagnostic = $Reason; revisions = @(); latest = $null; metadata = $null; created_at = ''; updated_at = ''; revision = $null }
    }
    $files = @()
    try { $allFiles = @(Get-ChildItem -LiteralPath $revisionDirectory -File -Force -ErrorAction Stop) }
    catch { return & $corrupt ("cannot enumerate revisions: {0}" -f $_.Exception.Message) }
    $foreign = @()
    foreach ($file in $allFiles) {
        if ($file.Name -cmatch '^([0-9]{6})\.json$') { $files += $file; continue }
        if ($file.Name -cmatch '\.tmp$') { continue }
        if ($file.Extension -ieq '.json') { $foreign += $file.Name }
    }
    if ($foreign.Count -gt 0) { return & $corrupt ("unexpected revision filename: {0}" -f $foreign[0]) }
    $files = @($files | Sort-Object Name)
    if ($files.Count -eq 0) {
        return [ordered]@{ task_id = $TaskId; health = 'orphaned'; diagnostic = 'task journal contains no revisions'; revisions = @(); latest = $null; metadata = $null; created_at = ''; updated_at = ''; revision = $null }
    }
    $revisions = [System.Collections.Generic.List[object]]::new()
    $expectedNumber = 1
    $previousHash = $null
    foreach ($file in $files) {
        $fileNumber = [int]$file.BaseName
        if ($fileNumber -ne $expectedNumber) { return & $corrupt ("revision chain has a gap before {0}" -f $file.Name) }
        try { $document = Read-BFJson $file.FullName }
        catch { return & $corrupt ("revision {0} is not readable: {1}" -f $file.Name, $_.Exception.Message) }
        $reason = $null
        foreach ($field in @('schema_version', 'task_id', 'revision', 'op', 'parent_hash', 'hash', 'timestamp_utc')) {
            if ($null -eq (Get-BFObjectProperty $document $field)) { $reason = ("required field {0} is missing in {1}" -f $field, $file.Name); break }
        }
        if ($null -eq $reason) {
            if ([int](Get-BFObjectProperty $document 'schema_version') -ne 1) { $reason = ("unsupported schema_version in {0}" -f $file.Name) }
            elseif ([string](Get-BFObjectProperty $document 'task_id') -cne $TaskId) { $reason = ("task_id does not match the directory identity in {0}" -f $file.Name) }
            elseif ([int64](Get-BFObjectProperty $document 'revision') -ne $fileNumber) { $reason = ("revision number disagrees with the filename in {0}" -f $file.Name) }
            elseif ([string](Get-BFObjectProperty $document 'op') -cnotin $script:BFRegistryOps) { $reason = ("unknown op in {0}" -f $file.Name) }
        }
        if ($null -eq $reason) {
            $op = [string](Get-BFObjectProperty $document 'op')
            if ($expectedNumber -eq 1 -and $op -cne 'create') { $reason = "first revision op must be create" }
            if ($null -eq $reason) {
                $parentHash = [string](Get-BFObjectProperty $document 'parent_hash')
                if ($expectedNumber -eq 1) { if ($parentHash -cne $script:BFRegistryGenesisHash) { $reason = "first revision parent_hash must be the genesis hash" } }
                elseif ($parentHash -cne $previousHash) { $reason = ("parent_hash does not link to the previous revision in {0}" -f $file.Name) }
            }
        }
        if ($null -eq $reason) {
            try { [void](Assert-BFRegistryPayload (Get-BFObjectProperty $document 'payload')) }
            catch { $reason = ("invalid payload in {0}: {1}" -f $file.Name, $_.Exception.Message) }
        }
        if ($null -eq $reason) {
            if ([string]::IsNullOrEmpty((ConvertTo-BFRegistryNormalizedTimestamp ([string](Get-BFObjectProperty $document 'timestamp_utc'))))) { $reason = ("timestamp_utc is invalid in {0}" -f $file.Name) }
        }
        if ($null -eq $reason) {
            $storedHash = [string](Get-BFObjectProperty $document 'hash')
            if ($storedHash -cnotmatch '^[0-9a-f]{64}$') { $reason = ("hash is malformed in {0}" -f $file.Name) }
            elseif ($storedHash -cne (Get-BFRegistryRevisionHash $document)) { $reason = ("revision hash mismatch in {0}" -f $file.Name) }
        }
        if ($null -ne $reason) { return & $corrupt $reason }
        $previousHash = [string](Get-BFObjectProperty $document 'hash')
        [void]$revisions.Add($document)
        $expectedNumber++
    }
    $latest = $revisions[$revisions.Count - 1]
    return [ordered]@{
        task_id    = $TaskId
        health     = 'ok'
        diagnostic = ''
        revisions  = @($revisions.ToArray())
        latest     = $latest
        metadata   = (Get-BFObjectProperty $latest 'payload')
        created_at = (ConvertTo-BFRegistryNormalizedTimestamp ([string](Get-BFObjectProperty $revisions[0] 'timestamp_utc')))
        updated_at = (ConvertTo-BFRegistryNormalizedTimestamp ([string](Get-BFObjectProperty $latest 'timestamp_utc')))
        revision   = [int](Get-BFObjectProperty $latest 'revision')
    }
}

function Add-BFRegistryRevision {
    # Appends exactly one canonical revision file; publication is atomic and
    # refuses to overwrite, so a lost race can never tear the journal.
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]$Store,
        [Parameter(Mandatory = $true)][string]$TaskId,
        [Parameter(Mandatory = $true)][int]$Revision,
        [Parameter(Mandatory = $true)][string]$Op,
        [Parameter(Mandatory = $true)]$Payload,
        [Parameter(Mandatory = $true)][string]$ParentHash
    )
    $document = [ordered]@{
        schema_version = 1
        task_id        = $TaskId
        revision       = $Revision
        op             = $Op
        parent_hash    = $ParentHash
        timestamp_utc  = (Get-BFRegistryTimestamp ([DateTimeOffset]::UtcNow))
        payload        = $Payload
    }
    $hashInput = [ordered]@{}
    foreach ($key in @($document.Keys)) { $hashInput[[string]$key] = $document[$key] }
    $document['hash'] = Get-BFHash $hashInput
    $revisionPath = Join-Path (Join-Path (Join-Path $Store.TasksRoot $TaskId) 'revisions') ('{0:D6}.json' -f $Revision)
    Write-BFJson -Path $revisionPath -Value $document
    return $document
}

function Get-BFRegistryEdges {
    # Dependency edge snapshot over existing valid journals only.
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$TasksRoot)
    $edges = [System.Collections.Generic.Dictionary[string, System.Collections.Generic.List[string]]]::new([System.StringComparer]::Ordinal)
    if (-not [System.IO.Directory]::Exists($TasksRoot)) { return $edges }
    foreach ($directory in @(Get-ChildItem -LiteralPath $TasksRoot -Directory -Force)) {
        if ($directory.Name -cnotmatch $script:BFRegistryUuidPattern) { continue }
        $state = Read-BFRegistryTaskState -TasksRoot $TasksRoot -TaskId $directory.Name
        if ($null -eq $state -or $state['health'] -cne 'ok') { continue }
        $list = [System.Collections.Generic.List[string]]::new()
        foreach ($dependency in @(Get-BFObjectProperty $state['metadata'] 'depends_on')) { $list.Add([string]$dependency) }
        $edges[$directory.Name] = $list
    }
    return $edges
}

function Assert-BFRegistryGraphAcyclic {
    # Iterative DFS over the whole proposed graph; reports the cycle path.
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)]$Edges)
    $color = @{}
    foreach ($root in @($Edges.Keys)) {
        if ($color[$root] -eq 2) { continue }
        $color[$root] = 1
        $path = [System.Collections.Generic.List[string]]::new()
        $path.Add($root)
        $stack = [System.Collections.Generic.Stack[object]]::new()
        $stack.Push(@($root, 0))
        while ($stack.Count -gt 0) {
            $frame = $stack.Peek()
            $node = [string]$frame[0]
            $neighbors = @()
            if ($Edges.ContainsKey($node)) { $neighbors = @($Edges[$node].ToArray()) }
            if ([int]$frame[1] -lt $neighbors.Count) {
                $frame[1] = [int]$frame[1] + 1
                $next = [string]$neighbors[[int]$frame[1] - 1]
                if ($color[$next] -eq 1) {
                    $start = $path.IndexOf($next)
                    if ($start -lt 0) { $start = 0 }
                    $cycleNodes = @($path.ToArray())[$start..($path.Count - 1)] + $next
                    throw (New-BFError 'BF_CONFLICT' ("dependency cycle over existing journals: {0}" -f ($cycleNodes -join ' -> ')))
                }
                if ($color[$next] -ne 2) {
                    $color[$next] = 1
                    $path.Add($next)
                    $stack.Push(@($next, 0))
                }
            }
            else {
                $color[$node] = 2
                [void]$stack.Pop()
                if ($path.Count -gt 0) { $path.RemoveAt($path.Count - 1) }
            }
        }
    }
}

function Invoke-BFRegistryCreate {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]$Store,
        [AllowNull()][string]$Title,
        [AllowNull()][string]$Description,
        [AllowNull()][string]$Priority,
        [AllowNull()][string[]]$Labels,
        [AllowNull()][string[]]$DependsOn
    )
    $payload = New-BFRegistryPayload -Title $Title -Description $Description -Priority $Priority -Labels $Labels -DependsOn $DependsOn
    $lock = Enter-BFRegistryGraphLock -TasksRoot $Store.TasksRoot
    try {
        $taskId = $null
        for ($attempt = 0; $attempt -lt 8 -and $null -eq $taskId; $attempt++) {
            $candidate = [guid]::NewGuid().ToString()
            if (-not [System.IO.Directory]::Exists((Join-Path $Store.TasksRoot $candidate))) { $taskId = $candidate }
        }
        if ($null -eq $taskId) { throw (New-BFError 'BF_BLOCKED' 'Cannot reserve a fresh task UUID in the repository store.') }
        $edges = Get-BFRegistryEdges -TasksRoot $Store.TasksRoot
        $proposed = [System.Collections.Generic.List[string]]::new()
        foreach ($dependency in @(Get-BFObjectProperty $payload 'depends_on')) { $proposed.Add([string]$dependency) }
        if ($proposed.Contains($taskId)) { throw (New-BFError 'BF_CONFLICT' 'depends_on self-reference is rejected.') }
        $edges[$taskId] = $proposed
        Assert-BFRegistryGraphAcyclic -Edges $edges
        $document = Add-BFRegistryRevision -Store $Store -TaskId $taskId -Revision 1 -Op 'create' -Payload $payload -ParentHash $script:BFRegistryGenesisHash
    }
    finally { $lock.Dispose() }
    return [pscustomobject]@{ TaskId = $taskId; Revision = 1; Payload = $payload; Document = $document }
}

function Invoke-BFRegistryEdit {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]$Store,
        [Parameter(Mandatory = $true)][string]$TaskId,
        [Parameter(Mandatory = $true)][int]$ExpectedRevision,
        [AllowNull()][string]$Title,
        [AllowNull()][string]$Description,
        [AllowNull()][string]$Priority,
        [AllowNull()][string[]]$Labels,
        [AllowNull()][string[]]$DependsOn
    )
    Assert-BFRegistryUuid $TaskId
    if ($ExpectedRevision -lt 1) { throw (New-BFError 'BF_INVALID' 'edit requires a positive expected_revision.') }
    $edited = New-BFRegistryPayload -Title $Title -Description $Description -Priority $Priority -Labels $Labels -DependsOn $DependsOn
    $lock = Enter-BFRegistryGraphLock -TasksRoot $Store.TasksRoot
    try {
        $state = Read-BFRegistryTaskState -TasksRoot $Store.TasksRoot -TaskId $TaskId
        if ($null -eq $state) { throw (New-BFError 'BF_INVALID' ("task {0} does not exist in the repository store." -f $TaskId)) }
        if ($state['health'] -ceq 'orphaned') { throw (New-BFError 'BF_BLOCKED' ("task journal is orphaned: {0}" -f $state['diagnostic'])) }
        if ($state['health'] -ceq 'corrupt') { throw (New-BFError 'BF_BLOCKED' ("task journal is corrupt: {0}" -f $state['diagnostic'])) }
        $legacyConflict = Get-BFRegistryLegacyConflict -Store $Store -TaskId $TaskId
        if ($null -ne $legacyConflict) { throw (New-BFError 'BF_BLOCKED' $legacyConflict) }
        if ([int]$state['revision'] -ne $ExpectedRevision) { throw (New-BFError 'BF_CONFLICT' ("expected_revision {0} does not match repository revision {1}." -f $ExpectedRevision, [int]$state['revision'])) }
        $edges = Get-BFRegistryEdges -TasksRoot $Store.TasksRoot
        $proposed = [System.Collections.Generic.List[string]]::new()
        foreach ($dependency in @(Get-BFObjectProperty $edited 'depends_on')) { $proposed.Add([string]$dependency) }
        if ($proposed.Contains($TaskId)) { throw (New-BFError 'BF_CONFLICT' 'depends_on self-reference is rejected.') }
        $edges[$TaskId] = $proposed
        Assert-BFRegistryGraphAcyclic -Edges $edges
        $current = $state['metadata']
        $payload = [ordered]@{
            title       = [string](Get-BFObjectProperty $edited 'title')
            description = [string](Get-BFObjectProperty $edited 'description')
            priority    = [string](Get-BFObjectProperty $edited 'priority')
            labels      = @(Get-BFObjectProperty $edited 'labels')
            depends_on  = @(Get-BFObjectProperty $edited 'depends_on')
            status      = [string](Get-BFObjectProperty $current 'status')
            archived    = [bool](Get-BFObjectProperty $current 'archived')
        }
        $document = Add-BFRegistryRevision -Store $Store -TaskId $TaskId -Revision ($ExpectedRevision + 1) -Op 'edit' -Payload $payload -ParentHash ([string](Get-BFObjectProperty $state['latest'] 'hash'))
    }
    finally { $lock.Dispose() }
    return [pscustomobject]@{ TaskId = $TaskId; Revision = $ExpectedRevision + 1; Payload = $payload; Document = $document }
}

function Invoke-BFRegistrySetArchived {
    # Archive/unarchive is a presentation flag via a new revision; it never
    # changes execution semantics, and duplicate transitions are rejected.
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]$Store,
        [Parameter(Mandatory = $true)][string]$TaskId,
        [Parameter(Mandatory = $true)][bool]$Archived
    )
    Assert-BFRegistryUuid $TaskId
    $lock = Enter-BFRegistryGraphLock -TasksRoot $Store.TasksRoot
    try {
        $state = Read-BFRegistryTaskState -TasksRoot $Store.TasksRoot -TaskId $TaskId
        if ($null -eq $state) { throw (New-BFError 'BF_INVALID' ("task {0} does not exist in the repository store." -f $TaskId)) }
        if ($state['health'] -ceq 'orphaned') { throw (New-BFError 'BF_BLOCKED' ("task journal is orphaned: {0}" -f $state['diagnostic'])) }
        if ($state['health'] -ceq 'corrupt') { throw (New-BFError 'BF_BLOCKED' ("task journal is corrupt: {0}" -f $state['diagnostic'])) }
        $legacyConflict = Get-BFRegistryLegacyConflict -Store $Store -TaskId $TaskId
        if ($null -ne $legacyConflict) { throw (New-BFError 'BF_BLOCKED' $legacyConflict) }
        $current = $state['metadata']
        if ([bool](Get-BFObjectProperty $current 'archived') -eq $Archived) {
            throw (New-BFError 'BF_CONFLICT' ("task is already {0}." -f $(if ($Archived) { 'archived' } else { 'unarchived' })))
        }
        $payload = [ordered]@{
            title       = [string](Get-BFObjectProperty $current 'title')
            description = [string](Get-BFObjectProperty $current 'description')
            priority    = [string](Get-BFObjectProperty $current 'priority')
            labels      = @(Get-BFObjectProperty $current 'labels')
            depends_on  = @(Get-BFObjectProperty $current 'depends_on')
            status      = [string](Get-BFObjectProperty $current 'status')
            archived    = $Archived
        }
        $op = if ($Archived) { 'archive' } else { 'unarchive' }
        $nextRevision = [int]$state['revision'] + 1
        $document = Add-BFRegistryRevision -Store $Store -TaskId $TaskId -Revision $nextRevision -Op $op -Payload $payload -ParentHash ([string](Get-BFObjectProperty $state['latest'] 'hash'))
    }
    finally { $lock.Dispose() }
    return [pscustomobject]@{ TaskId = $TaskId; Revision = $nextRevision; Payload = $payload; Document = $document }
}

function Assert-BFRegistryActivationRequest {
    # Trusted request schema v1 for `task activate` (requirement 5). Validates
    # field presence, types, metadata constants, the declared controller
    # contract, and that project_root is an existing directory inside a
    # worktree of the same clone. Any failure is BF_INVALID without a revision.
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]$Store,
        [Parameter(Mandatory = $true)][string]$TaskId,
        [Parameter(Mandatory = $true)]$Request
    )
    if ($null -eq $Request -or ($Request -isnot [System.Collections.IDictionary] -and $Request -isnot [pscustomobject])) {
        throw (New-BFError 'BF_INVALID' 'Trusted activation request must be an object.')
    }
    $requestTaskId = [string](Get-BFObjectProperty $Request 'task_id')
    if ($requestTaskId -cnotmatch $script:BFRegistryUuidPattern) { throw (New-BFError 'BF_INVALID' 'Trusted request task_id must be a canonical lower-case UUID.') }
    if ($requestTaskId -cne $TaskId) { throw (New-BFError 'BF_INVALID' 'Trusted request task_id does not match the addressed repository task.') }
    $contract = Get-BFObjectProperty $Request 'controller_contract'
    if ($contract -isnot [string] -or $contract -cne $script:BFRegistryControllerContract) {
        throw (New-BFError 'BF_INVALID' ("Trusted request controller_contract must be '{0}'." -f $script:BFRegistryControllerContract))
    }
    $projectRoot = Get-BFObjectProperty $Request 'project_root'
    if ($projectRoot -isnot [string] -or [string]::IsNullOrWhiteSpace($projectRoot)) { throw (New-BFError 'BF_INVALID' 'Trusted request project_root is required.') }
    $requestedAbsolute = Assert-BFSafePath $projectRoot
    if (-not [System.IO.Directory]::Exists($requestedAbsolute)) { throw (New-BFError 'BF_INVALID' 'Trusted request project_root does not exist.') }
    $requestedTopLevel = Assert-BFSafePath (Invoke-BFStorageGitRead $requestedAbsolute @('rev-parse', '--show-toplevel'))
    $requestedCommonText = Invoke-BFStorageGitRead $requestedTopLevel @('rev-parse', '--path-format=absolute', '--git-common-dir')
    if ([string]::IsNullOrWhiteSpace($requestedCommonText)) { throw (New-BFError 'BF_INVALID' 'Trusted request project_root has an empty Git common dir.') }
    $requestedCommon = if ([System.IO.Path]::IsPathRooted($requestedCommonText)) { Assert-BFSafePath $requestedCommonText } else { Assert-BFSafePath (Join-Path $requestedTopLevel $requestedCommonText) }
    if (-not [string]::Equals($requestedCommon, $Store.CommonDir, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw (New-BFError 'BF_INVALID' 'Trusted request project_root belongs to a different clone.')
    }
    $title = Get-BFObjectProperty $Request 'title'
    if ($title -isnot [string]) { throw (New-BFError 'BF_INVALID' 'Trusted request title must be a string.') }
    $priority = Get-BFObjectProperty $Request 'priority'
    if ($priority -isnot [string]) { throw (New-BFError 'BF_INVALID' 'Trusted request priority must be a string.') }
    $labelsRaw = $null
    if ($Request -is [System.Collections.IDictionary]) { $labelsRaw = $Request['labels'] } else { $labelsRaw = $Request.PSObject.Properties['labels'].Value }
    if ($null -eq $labelsRaw -or $labelsRaw -isnot [array]) { throw (New-BFError 'BF_INVALID' 'Trusted request labels must be an array.') }
    $dependenciesRaw = $null
    if ($Request -is [System.Collections.IDictionary]) { $dependenciesRaw = $Request['depends_on'] } else { $dependenciesRaw = $Request.PSObject.Properties['depends_on'].Value }
    if ($null -eq $dependenciesRaw -or $dependenciesRaw -isnot [array]) { throw (New-BFError 'BF_INVALID' 'Trusted request depends_on must be an array.') }
    # Reuses the requirement-3 metadata validation (title/priority/labels/depends_on).
    [void](New-BFRegistryPayload -Title $title -Priority $priority -Labels $labelsRaw -DependsOn $dependenciesRaw)
}

function Invoke-BFRegistryActivate {
    # v1 staged activation: full trusted-request validation, then BLOCKED
    # without a revision. Returns the staged outcome; validation problems throw.
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]$Store,
        [Parameter(Mandatory = $true)][string]$TaskId,
        [AllowNull()][string]$InputFile
    )
    Assert-BFRegistryUuid $TaskId
    $state = Read-BFRegistryTaskState -TasksRoot $Store.TasksRoot -TaskId $TaskId
    if ($null -eq $state) { throw (New-BFError 'BF_INVALID' ("task {0} does not exist in the repository store." -f $TaskId)) }
    if ($state['health'] -ceq 'orphaned') { throw (New-BFError 'BF_BLOCKED' ("task journal is orphaned: {0}" -f $state['diagnostic'])) }
    if ($state['health'] -ceq 'corrupt') { throw (New-BFError 'BF_BLOCKED' ("task journal is corrupt: {0}" -f $state['diagnostic'])) }
    $legacyConflict = Get-BFRegistryLegacyConflict -Store $Store -TaskId $TaskId
    if ($null -ne $legacyConflict) { throw (New-BFError 'BF_BLOCKED' $legacyConflict) }
    if ([string](Get-BFObjectProperty $state['metadata'] 'status') -cne 'planned') {
        throw (New-BFError 'BF_CONFLICT' 'only planned repository tasks can be activated.')
    }
    if ([string]::IsNullOrWhiteSpace($InputFile)) { throw (New-BFError 'BF_INVALID' 'activate requires -InputFile with a full trusted request.') }
    $request = Read-BFJson $InputFile
    Assert-BFRegistryActivationRequest -Store $Store -TaskId $TaskId -Request $request
    # The store context already verified the exact worktree root; the controller
    # write slice that would append the ready revision is not declared yet.
    $reason = "Repository task activation is not yet declared for the PowerShell controller (schema/store v1). Full trusted-request validation passed and the command is staged BLOCKED: task {0} remains planned and no revision was written." -f $TaskId
    return [pscustomobject]@{
        Staged       = $true
        Reason       = $reason
        Revision     = [int]$state['revision']
        Metadata     = $state['metadata']
        RepositoryId = $Store.RepositoryId
    }
}

function Read-BFLegacyTaskSnapshot {
    # Neutral read-only view of one checkout-local legacy task journal. Bytes
    # are never rewritten. Verifies whichever chain linkage the journal
    # declares (legacy previous_sha256 discipline or repository-style
    # hash/parent_hash discipline) and records one revision hash per revision
    # using the registry hash discipline, so identical logical histories
    # dedupe across stores (requirement 17).
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$LegacyTaskDirectory)
    $revisionDirectory = Join-Path $LegacyTaskDirectory 'revisions'
    if (-not [System.IO.Directory]::Exists($revisionDirectory)) { return [ordered]@{ health = 'orphaned'; diagnostic = 'legacy task has no revisions directory'; hashes = @(); count = 0 } }
    $files = @()
    try { $allFiles = @(Get-ChildItem -LiteralPath $revisionDirectory -File -Force -ErrorAction Stop) }
    catch { return [ordered]@{ health = 'corrupt'; diagnostic = ("cannot enumerate legacy revisions: {0}" -f $_.Exception.Message); hashes = @(); count = 0 } }
    foreach ($file in $allFiles) {
        if ($file.Name -cmatch '^([0-9]{6})\.json$') { $files += $file; continue }
        if ($file.Name -cmatch '\.tmp$') { continue }
        if ($file.Extension -ieq '.json') { return [ordered]@{ health = 'corrupt'; diagnostic = ("unexpected legacy revision filename: {0}" -f $file.Name); hashes = @(); count = 0 } }
    }
    $files = @($files | Sort-Object Name)
    if ($files.Count -eq 0) { return [ordered]@{ health = 'orphaned'; diagnostic = 'legacy journal contains no revisions'; hashes = @(); count = 0 } }
    $hashes = [System.Collections.Generic.List[string]]::new()
    $documents = [System.Collections.Generic.List[object]]::new()
    $expected = 1
    foreach ($file in $files) {
        if ([int]$file.BaseName -ne $expected) { return [ordered]@{ health = 'corrupt'; diagnostic = ("legacy revision chain has a gap before {0}" -f $file.Name); hashes = @(); count = 0 } }
        try { $document = Read-BFJson $file.FullName }
        catch { return [ordered]@{ health = 'corrupt'; diagnostic = ("legacy revision {0} is not readable: {1}" -f $file.Name, $_.Exception.Message); hashes = @(); count = 0 } }
        $previousDocument = if ($documents.Count -gt 0) { $documents[$documents.Count - 1] } else { $null }
        if (Test-BFObjectProperty $document 'previous_sha256') {
            $linkedHash = Get-BFObjectProperty $document 'previous_sha256'
            if ($expected -eq 1) {
                if ($null -ne $linkedHash) { return [ordered]@{ health = 'corrupt'; diagnostic = 'legacy first revision must have null previous_sha256'; hashes = @(); count = 0 } }
            }
            elseif ($linkedHash -isnot [string] -or $null -eq $previousDocument -or $linkedHash -cne (Get-BFHash $previousDocument)) {
                return [ordered]@{ health = 'corrupt'; diagnostic = ("legacy revision chain is broken at {0}" -f $file.Name); hashes = @(); count = 0 }
            }
        }
        elseif ((Test-BFObjectProperty $document 'hash') -and (Test-BFObjectProperty $document 'parent_hash')) {
            $parentHash = [string](Get-BFObjectProperty $document 'parent_hash')
            $storedHash = [string](Get-BFObjectProperty $document 'hash')
            $expectedParent = if ($expected -eq 1) { $script:BFRegistryGenesisHash } else { [string](Get-BFObjectProperty $previousDocument 'hash') }
            if ($parentHash -cne $expectedParent) { return [ordered]@{ health = 'corrupt'; diagnostic = ("repository-style legacy chain is broken at {0}" -f $file.Name); hashes = @(); count = 0 } }
            if ($storedHash -cnotmatch '^[0-9a-f]{64}$' -or $storedHash -cne (Get-BFRegistryRevisionHash $document)) {
                return [ordered]@{ health = 'corrupt'; diagnostic = ("repository-style legacy revision hash mismatch at {0}" -f $file.Name); hashes = @(); count = 0 }
            }
        }
        else {
            return [ordered]@{ health = 'corrupt'; diagnostic = ("legacy revision {0} has no verifiable chain linkage" -f $file.Name); hashes = @(); count = 0 }
        }
        [void]$hashes.Add((Get-BFRegistryRevisionHash $document))
        [void]$documents.Add($document)
        $expected++
    }
    return [ordered]@{ health = 'ok'; diagnostic = ''; hashes = @($hashes.ToArray()); count = $documents.Count; documents = @($documents.ToArray()) }
}

function Get-BFRegistryLegacyConflict {
    # Returns the conflict reason when a legacy checkout-local copy of the same
    # UUID has a diverging verified history hash; $null means no legacy copy,
    # an identical verified history (dedupe), or a legacy copy that is itself
    # damaged (surfaced separately as its own diagnostic row).
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)]$Store, [Parameter(Mandatory = $true)][string]$TaskId)
    $legacyDirectory = Join-Path (Join-Path (Join-Path $Store.ProjectRoot '.bsl-flow') 'tasks') $TaskId
    if (-not [System.IO.Directory]::Exists($legacyDirectory)) { return $null }
    $legacy = Read-BFLegacyTaskSnapshot -LegacyTaskDirectory $legacyDirectory
    if ($legacy['health'] -cne 'ok') { return $null }
    $state = Read-BFRegistryTaskState -TasksRoot $Store.TasksRoot -TaskId $TaskId
    if ($null -eq $state -or $state['health'] -cne 'ok') { return $null }
    $repositoryHashes = @(foreach ($revision in @($state['revisions'])) { [string](Get-BFObjectProperty $revision 'hash') })
    $repositoryHistory = Get-BFRegistryHistoryHash -RevisionHashes $repositoryHashes
    $legacyHistory = Get-BFRegistryHistoryHash -RevisionHashes @($legacy['hashes'])
    if ($repositoryHistory -ceq $legacyHistory) { return $null }
    return ("the same UUID exists in the repository store and the legacy checkout-local store with diverging verified histories (repository history hash {0}, legacy history hash {1}); show/history are blocked until resolved" -f $repositoryHistory, $legacyHistory)
}

function Test-BFRegistryFreshness {
    # Requirement 18: controller-sourced tasks carry absolute live inputs. If
    # any declared absolute live-input path no longer exists, the read
    # projection is marked freshness=stale (diagnostic only; it never changes
    # the historical status). Registry payloads carry no execution fields and
    # therefore always project as fresh.
    [CmdletBinding()]
    param([AllowNull()]$Document)
    foreach ($field in $script:BFRegistryFreshnessFields) {
        $value = Get-BFObjectProperty $Document $field
        if ($null -eq $value) { continue }
        $pathText = [string]$value
        if ([string]::IsNullOrWhiteSpace($pathText) -or -not [System.IO.Path]::IsPathRooted($pathText)) { continue }
        if (-not [System.IO.File]::Exists($pathText) -and -not [System.IO.Directory]::Exists($pathText)) { return 'stale' }
    }
    return 'fresh'
}

function Get-BFRegistryDependencySummary {
    # dependency_summary projection: {total, by_status} over the task's
    # depends_on targets. Targets that do not resolve to a healthy repository
    # task are counted under 'missing' (existence is never required).
    [CmdletBinding()]
    param([AllowNull()][string[]]$DependsOn, [AllowNull()]$States)
    $byStatus = [ordered]@{}
    $total = 0
    foreach ($dependency in @($DependsOn)) {
        if ($null -eq $dependency) { continue }
        $total++
        $statusValue = 'missing'
        $dependencyId = [string]$dependency
        if ($null -ne $States -and $States.ContainsKey($dependencyId)) {
            $target = $States[$dependencyId]
            if ($null -ne $target -and $target['health'] -ceq 'ok') { $statusValue = [string](Get-BFObjectProperty $target['metadata'] 'status') }
        }
        if ($byStatus.Contains($statusValue)) { $byStatus[$statusValue]++ } else { $byStatus[$statusValue] = 1 }
    }
    return [ordered]@{ total = $total; by_status = $byStatus }
}

function New-BFRegistryRow {
    # Internal list projection; never contains prompts, raw evidence, or
    # credentials, and never will. The JSON list item is the allowlisted
    # projection (ConvertTo-BFRegistryListItem); this row additionally carries
    # the internal fields the human view, filters and sort need.
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [Parameter(Mandatory = $true)][string]$TaskId,
        [AllowNull()]$Metadata,
        [string]$CreatedAt = '',
        [string]$UpdatedAt = '',
        [AllowNull()]$Revision = $null,
        [string]$Health = 'ok',
        [string]$Diagnostic = '',
        [AllowNull()][object]$Worktree = $null,
        [AllowNull()][object]$Stage = $null,
        [AllowNull()][object]$Status = $null,
        [AllowNull()][object]$Priority = $null,
        [bool]$Archived = $false,
        [string[]]$Labels = @(),
        [string[]]$DependsOn = @(),
        [AllowNull()]$DependencySummary = $null,
        [AllowNull()][object]$NextAction = $null,
        [AllowNull()][object]$Title = $null,
        [string]$OriginWorktree = '',
        [AllowNull()][object]$CurrentWorktree = $null,
        [string]$Freshness = 'fresh'
    )
    if ($Health -ceq 'ok' -and $null -ne $Metadata) {
        $Status = [string](Get-BFObjectProperty $Metadata 'status')
        $Priority = [string](Get-BFObjectProperty $Metadata 'priority')
        $Title = [string](Get-BFObjectProperty $Metadata 'title')
        $Archived = [bool](Get-BFObjectProperty $Metadata 'archived')
        # A pipeline with zero output yields $null, which would resurface as a
        # single null element after @() re-wrapping; collect explicitly instead.
        $rowLabels = [System.Collections.Generic.List[string]]::new()
        foreach ($label in @(Get-BFObjectProperty $Metadata 'labels')) { if ($null -ne $label) { $rowLabels.Add([string]$label) } }
        $Labels = @($rowLabels.ToArray())
        $rowDependencies = [System.Collections.Generic.List[string]]::new()
        foreach ($dependency in @(Get-BFObjectProperty $Metadata 'depends_on')) { if ($null -ne $dependency) { $rowDependencies.Add([string]$dependency) } }
        $DependsOn = @($rowDependencies.ToArray())
        $Stage = [string](Get-BFObjectProperty $Metadata 'stage')
        if ([string]::IsNullOrEmpty($Stage)) { $Stage = $null }
        $Freshness = Test-BFRegistryFreshness -Document $Metadata
    }
    if ($null -eq $DependencySummary) {
        $DependencySummary = Get-BFRegistryDependencySummary -DependsOn $DependsOn -States $null
    }
    return [ordered]@{
        task_id            = $TaskId
        source             = $Source
        title              = $Title
        status             = $Status
        stage              = $Stage
        priority           = $Priority
        archived           = $Archived
        labels             = @($Labels)
        depends_on         = @($DependsOn)
        created_at         = $CreatedAt
        updated_at         = $UpdatedAt
        revision           = $Revision
        health             = $Health
        diagnostic         = [string]$Diagnostic
        worktree           = $Worktree
        next_action        = $NextAction
        origin_worktree    = $OriginWorktree
        current_worktree   = $CurrentWorktree
        dependency_summary = $DependencySummary
        freshness          = $Freshness
    }
}

function New-BFRegistryDiagnosticRow {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$Source,
        [Parameter(Mandatory = $true)][string]$TaskId,
        [Parameter(Mandatory = $true)][string]$Health,
        [Parameter(Mandatory = $true)][string]$Diagnostic,
        [AllowNull()][object]$Worktree = $null,
        [AllowNull()][string]$UpdatedAt = '',
        [AllowNull()]$Revision = $null,
        [string]$OriginWorktree = '',
        [AllowNull()][object]$CurrentWorktree = $null
    )
    return (New-BFRegistryRow -Source $Source -TaskId $TaskId -Metadata $null -Health $Health -Diagnostic $Diagnostic -Worktree $Worktree -UpdatedAt $UpdatedAt -Revision $Revision -OriginWorktree $OriginWorktree -CurrentWorktree $CurrentWorktree)
}

function Get-BFRegistryRows {
    # Repository rows plus legacy read-only discovery of the queried worktree.
    # Same UUID: identical verified history hash dedupes; anything else is a
    # visible conflict diagnostic. Returns rows plus the repository states.
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)]$Store)
    $rows = [System.Collections.Generic.List[object]]::new()
    $repositoryStates = [System.Collections.Generic.Dictionary[string, object]]::new([System.StringComparer]::Ordinal)
    if ([System.IO.Directory]::Exists($Store.TasksRoot)) {
        foreach ($directory in @(Get-ChildItem -LiteralPath $Store.TasksRoot -Directory -Force)) {
            if ($directory.Name -cnotmatch $script:BFRegistryUuidPattern) { continue }
            $repositoryStates[$directory.Name] = (Read-BFRegistryTaskState -TasksRoot $Store.TasksRoot -TaskId $directory.Name)
        }
    }
    $legacySnapshots = [System.Collections.Generic.Dictionary[string, object]]::new([System.StringComparer]::Ordinal)
    $legacyRoot = Join-Path $Store.ProjectRoot '.bsl-flow/tasks'
    if ([System.IO.Directory]::Exists($legacyRoot)) {
        foreach ($directory in @(Get-ChildItem -LiteralPath $legacyRoot -Directory -Force)) {
            if ($directory.Name -cnotmatch $script:BFRegistryUuidPattern) { continue }
            $snapshot = Read-BFLegacyTaskSnapshot -LegacyTaskDirectory $directory.FullName
            $legacySnapshots[$directory.Name] = $snapshot
        }
    }
    foreach ($taskId in @($repositoryStates.Keys)) {
        $state = $repositoryStates[$taskId]
        if ($state['health'] -cne 'ok') {
            [void]$rows.Add((New-BFRegistryDiagnosticRow -Source 'repository' -TaskId $taskId -Health ([string]$state['health']) -Diagnostic ([string]$state['diagnostic']) -UpdatedAt ([string]$state['updated_at'])))
            continue
        }
        $summary = Get-BFRegistryDependencySummary -DependsOn @((Get-BFObjectProperty $state['metadata'] 'depends_on')) -States $repositoryStates
        $row = New-BFRegistryRow -Source 'repository' -TaskId $taskId -Metadata $state['metadata'] -CreatedAt ([string]$state['created_at']) -UpdatedAt ([string]$state['updated_at']) -Revision ([string](Get-BFObjectProperty $state['latest'] 'hash')) -DependencySummary $summary -NextAction 'activate'
        if ($legacySnapshots.ContainsKey($taskId)) {
            $legacy = $legacySnapshots[$taskId]
            if ($legacy['health'] -ceq 'ok') {
                $conflictReason = Get-BFRegistryLegacyConflict -Store $Store -TaskId $taskId
                if ($null -ne $conflictReason) {
                    $row['health'] = 'conflict'
                    $row['diagnostic'] = $conflictReason
                    $row['next_action'] = $null
                }
                # else: identical verified history deduplicates on the repository row.
            }
            else {
                # Keep both: the healthy repository task and the legacy diagnostic.
            }
        }
        [void]$rows.Add($row)
    }
    foreach ($taskId in @($legacySnapshots.Keys)) {
        if ($repositoryStates.ContainsKey($taskId) -and $repositoryStates[$taskId]['health'] -ceq 'ok') { continue }
        $legacy = $legacySnapshots[$taskId]
        if ($legacy['health'] -cne 'ok') {
            [void]$rows.Add((New-BFRegistryDiagnosticRow -Source 'legacy' -TaskId $taskId -Health ([string]$legacy['health']) -Diagnostic ([string]$legacy['diagnostic']) -Worktree $Store.ProjectRoot -OriginWorktree $Store.ProjectRoot -CurrentWorktree $Store.ProjectRoot))
            continue
        }
        $latest = $null
        try { $latest = Read-BFJournal (Join-Path $legacyRoot $taskId) }
        catch {
            [void]$rows.Add((New-BFRegistryDiagnosticRow -Source 'legacy' -TaskId $taskId -Health 'corrupt' -Diagnostic ([string]$_.Exception.Message) -Worktree $Store.ProjectRoot -OriginWorktree $Store.ProjectRoot -CurrentWorktree $Store.ProjectRoot))
            continue
        }
        $legacyLabels = [System.Collections.Generic.List[string]]::new()
        foreach ($label in @(Get-BFObjectProperty $latest 'labels')) { if ($null -ne $label) { $legacyLabels.Add([string]$label) } }
        $legacyDependencies = [System.Collections.Generic.List[string]]::new()
        foreach ($dependency in @(Get-BFObjectProperty $latest 'depends_on')) { if ($null -ne $dependency) { $legacyDependencies.Add([string]$dependency) } }
        $legacyOrigin = [string](Get-BFObjectProperty $latest 'project_path')
        if ([string]::IsNullOrWhiteSpace($legacyOrigin)) { $legacyOrigin = $Store.ProjectRoot }
        $legacySummary = Get-BFRegistryDependencySummary -DependsOn @($legacyDependencies.ToArray()) -States $null
        [void]$rows.Add((New-BFRegistryRow -Source 'legacy' -TaskId $taskId -Metadata $null -Status ([string](Get-BFObjectProperty $latest 'status')) -Stage ([string](Get-BFObjectProperty $latest 'stage')) -Priority ([string](Get-BFObjectProperty $latest 'priority')) -Archived $false -Labels @($legacyLabels.ToArray()) -DependsOn @($legacyDependencies.ToArray()) -DependencySummary $legacySummary -CreatedAt (ConvertTo-BFRegistryNormalizedTimestamp ([string](Get-BFObjectProperty $latest 'created_at'))) -UpdatedAt (ConvertTo-BFRegistryNormalizedTimestamp ([string](Get-BFObjectProperty $latest 'updated_at'))) -Revision ([int](Get-BFObjectProperty $latest 'revision')) -Worktree $Store.ProjectRoot -NextAction $null -Title ([string](Get-BFObjectProperty $latest 'title')) -OriginWorktree $legacyOrigin -CurrentWorktree $Store.ProjectRoot -Freshness (Test-BFRegistryFreshness -Document $latest)))
    }
    return [pscustomobject]@{ Rows = @($rows.ToArray()); RepositoryStates = $repositoryStates }
}

function ConvertTo-BFRegistryListItem {
    # Exact allowlisted JSON list item (normative schema v1) plus the
    # requirement-18 freshness marking.
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)]$Row)
    $diagnosticState = $null
    if ([string]$Row['health'] -cne 'ok') { $diagnosticState = [string]$Row['health'] }
    return [ordered]@{
        task_id            = [string]$Row['task_id']
        title              = $Row['title']
        status             = $Row['status']
        stage              = $Row['stage']
        next_action        = $Row['next_action']
        priority           = $Row['priority']
        labels             = @($Row['labels'])
        dependency_summary = $Row['dependency_summary']
        created_at         = [string]$Row['created_at']
        updated_at         = [string]$Row['updated_at']
        origin_worktree    = [string]$Row['origin_worktree']
        current_worktree   = $Row['current_worktree']
        diagnostic_state   = $diagnosticState
        freshness          = [string]$Row['freshness']
    }
}

function Resolve-BFRegistrySortOrder {
    # Validates -Sort/-Order and applies the published defaults: updated_at by
    # default; desc for created_at/updated_at, asc for everything else.
    [CmdletBinding()]
    param([AllowNull()][string]$Sort, [AllowNull()][string]$Order)
    $sortValue = if ([string]::IsNullOrWhiteSpace($Sort)) { 'updated_at' } else { $Sort }
    if ($sortValue -cnotin $script:BFRegistrySortFields) { throw (New-BFError 'BF_INVALID' ("sort must be one of: {0}." -f ($script:BFRegistrySortFields -join ', '))) }
    $defaultOrder = if ($sortValue -cin @('updated_at', 'created_at')) { 'desc' } else { 'asc' }
    $orderValue = if ([string]::IsNullOrWhiteSpace($Order)) { $defaultOrder } else { $Order }
    if ($orderValue -cnotin @('asc', 'desc')) { throw (New-BFError 'BF_INVALID' 'order must be asc or desc.') }
    return [ordered]@{ sort = $sortValue; order = $orderValue }
}

function Get-BFRegistrySortFieldValue {
    # Primary sort key of one row. Ranks keep priority/status orders total;
    # unknown (empty) values degrade to their raw text.
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)]$Row, [Parameter(Mandatory = $true)][string]$Sort)
    switch ($Sort) {
        'created_at' { return [string]$Row['created_at'] }
        'updated_at' { return [string]$Row['updated_at'] }
        'title' { return [string]$Row['title'] }
        'priority' {
            $priorityValue = [string]$Row['priority']
            $rank = [array]::IndexOf($script:BFRegistryPriorities, $priorityValue)
            return ($(if ($rank -lt 0) { $priorityValue } else { [string]$rank }))
        }
        'status' {
            $statusValue = [string]$Row['status']
            $rank = [array]::IndexOf($script:BFRegistryStatuses, $statusValue)
            return ($(if ($rank -lt 0) { $statusValue } else { [string]$rank }))
        }
        default { throw (New-BFError 'BF_INVALID' ("sort must be one of: {0}." -f ($script:BFRegistrySortFields -join ', '))) }
    }
}

function Sort-BFRegistryRows {
    # Deterministic total order: primary key per -Sort/-Order, tie-breaker
    # always task_id ascending (ordinal). A comparison delegate keeps
    # variable-length strings (titles) correct in both directions.
    [CmdletBinding()]
    param(
        [AllowNull()][AllowEmptyCollection()][object[]]$Rows,
        [string]$Sort = 'updated_at',
        [string]$Order = ''
    )
    if ($null -eq $Rows -or $Rows.Count -lt 2) { return @($Rows) }
    $resolved = Resolve-BFRegistrySortOrder -Sort $Sort -Order $Order
    $sortValue = [string]$resolved['sort']
    $orderValue = [string]$resolved['order']
    $comparison = [Comparison[object]] {
        param($left, $right)
        $keyComparison = [string]::CompareOrdinal((Get-BFRegistrySortFieldValue -Row $left -Sort $sortValue), (Get-BFRegistrySortFieldValue -Row $right -Sort $sortValue))
        if ($orderValue -ceq 'desc') { $keyComparison = -$keyComparison }
        if ($keyComparison -eq 0) { $keyComparison = [string]::CompareOrdinal([string]$left['task_id'], [string]$right['task_id']) }
        return $keyComparison
    }
    $sorted = [object[]]$Rows
    [System.Array]::Sort($sorted, $comparison)
    return @($sorted)
}

function Test-BFRegistryFilterValue {
    [CmdletBinding()]
    param([AllowNull()][string[]]$Values, [AllowNull()][string[]]$Allowed, [string]$Name)
    if ($null -eq $Values -or $Values.Count -eq 0) { return }
    foreach ($value in @($Values)) {
        if ($null -ne $Allowed) {
            if ([string]$value -cnotin $Allowed) { throw (New-BFError 'BF_INVALID' ("{0} filter must be one of: {1}." -f $Name, ($Allowed -join ', '))) }
        }
        elseif ([string]::IsNullOrEmpty([string]$value)) {
            throw (New-BFError 'BF_INVALID' ("{0} filter values must not be empty." -f $Name))
        }
    }
}

function Select-BFRegistryRows {
    [CmdletBinding()]
    param(
        [AllowNull()][AllowEmptyCollection()][object[]]$Rows,
        [AllowNull()][string[]]$Status,
        [AllowNull()][string[]]$Stage,
        [AllowNull()][string[]]$Priority,
        [AllowNull()][string[]]$Label,
        [AllowNull()][string]$UpdatedBefore,
        [AllowNull()][string]$UpdatedAfter,
        [string]$Archived = 'false'
    )
    if ($null -eq $Rows) { $Rows = @() }
    $archivedValue = if ([string]::IsNullOrWhiteSpace($Archived)) { 'false' } else { $Archived }
    Test-BFRegistryFilterValue -Values $Status -Allowed $script:BFRegistryStatuses -Name 'status'
    Test-BFRegistryFilterValue -Values $Stage -Allowed $null -Name 'stage'
    Test-BFRegistryFilterValue -Values $Priority -Allowed $script:BFRegistryPriorities -Name 'priority'
    Test-BFRegistryFilterValue -Values $Label -Allowed $null -Name 'label'
    $before = if ([string]::IsNullOrWhiteSpace($UpdatedBefore)) { $null } else { ConvertTo-BFRegistryTimestamp $UpdatedBefore }
    $after = if ([string]::IsNullOrWhiteSpace($UpdatedAfter)) { $null } else { ConvertTo-BFRegistryTimestamp $UpdatedAfter }
    $selected = [System.Collections.Generic.List[object]]::new()
    foreach ($row in $Rows) {
        if ([string]$row['health'] -cne 'ok') {
            # Diagnostic entries stay visible under every filter combination.
            [void]$selected.Add($row)
            continue
        }
        $isArchived = [bool]$row['archived']
        if ($archivedValue -ceq 'true' -and -not $isArchived) { continue }
        if ($archivedValue -ceq 'false' -and $isArchived) { continue }
        if ($null -ne $Status -and $Status.Count -gt 0 -and ([string]$row['status'] -cnotin $Status)) { continue }
        if ($null -ne $Stage -and $Stage.Count -gt 0 -and ([string]$row['stage'] -cnotin $Stage)) { continue }
        if ($null -ne $Priority -and $Priority.Count -gt 0 -and ([string]$row['priority'] -cnotin $Priority)) { continue }
        if ($null -ne $Label -and $Label.Count -gt 0) {
            # OR semantics: one matching label is sufficient (requirement 9).
            $matchesAny = $false
            foreach ($wanted in $Label) { if (@($row['labels']) -ccontains [string]$wanted) { $matchesAny = $true; break } }
            if (-not $matchesAny) { continue }
        }
        $updated = [string]$row['updated_at']
        # RFC3339 bounds are inclusive on both ends (requirement 9).
        if ($null -ne $before) {
            if ([string]::IsNullOrEmpty($updated) -or [string]::CompareOrdinal($updated, $before) -gt 0) { continue }
        }
        if ($null -ne $after) {
            if ([string]::IsNullOrEmpty($updated) -or [string]::CompareOrdinal($updated, $after) -lt 0) { continue }
        }
        [void]$selected.Add($row)
    }
    return @($selected.ToArray())
}

function ConvertTo-BFRegistryCursor {
    # Opaque cursor bound to the repository, the full filter/sort/order
    # fingerprint, and the exact sort position of the last returned row.
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryId,
        [Parameter(Mandatory = $true)][string]$Fingerprint,
        [Parameter(Mandatory = $true)][string]$Sort,
        [Parameter(Mandatory = $true)][string]$Order,
        [Parameter(Mandatory = $true)]$Row
    )
    $payload = Get-BFCanonicalJson ([ordered]@{
        repository_id = $RepositoryId
        fingerprint   = $Fingerprint
        sort          = $Sort
        order         = $Order
        key           = (Get-BFRegistrySortFieldValue -Row $Row -Sort $Sort)
        task_id       = [string]$Row['task_id']
    })
    return [Convert]::ToBase64String([System.Text.UTF8Encoding]::new($false, $true).GetBytes($payload))
}

function ConvertFrom-BFRegistryCursor {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$Cursor,
        [Parameter(Mandatory = $true)][string]$RepositoryId,
        [Parameter(Mandatory = $true)][string]$Fingerprint
    )
    try {
        $bytes = [Convert]::FromBase64String($Cursor)
        $text = [System.Text.UTF8Encoding]::new($false, $true).GetString($bytes)
        if ((Test-BFJsonSyntax $text) -cne 'object') { throw (New-BFError 'BF_INVALID' 'Registry cursor must encode a JSON object.') }
        # -DateKind String keeps ISO timestamps as text; default parsing would
        # turn them into culture-formatted DateTime values.
        $convertCommand = Get-Command ConvertFrom-Json -ErrorAction Stop
        $decoded = if ($convertCommand.Parameters.ContainsKey('DateKind')) { ConvertFrom-Json -InputObject $text -DateKind String -ErrorAction Stop } else { ConvertFrom-Json -InputObject $text -ErrorAction Stop }
        foreach ($field in @('repository_id', 'fingerprint', 'sort', 'order', 'key', 'task_id')) {
            if ($null -eq (Get-BFObjectProperty $decoded $field)) { throw (New-BFError 'BF_INVALID' ("Registry cursor is missing field {0}." -f $field)) }
        }
        if ([string](Get-BFObjectProperty $decoded 'repository_id') -cne $RepositoryId) { throw (New-BFError 'BF_INVALID' 'Registry cursor does not belong to this repository store.') }
        if ([string](Get-BFObjectProperty $decoded 'fingerprint') -cne $Fingerprint) { throw (New-BFError 'BF_INVALID' 'Registry cursor does not match the current filters, sort, or order.') }
        return [ordered]@{
            key     = [string](Get-BFObjectProperty $decoded 'key')
            task_id = [string](Get-BFObjectProperty $decoded 'task_id')
        }
    }
    catch [System.Management.Automation.MethodException] { throw (New-BFError 'BF_INVALID' 'Registry cursor is not valid base64.') }
    catch [System.FormatException] { throw (New-BFError 'BF_INVALID' 'Registry cursor is not valid base64.') }
}

function Get-BFRegistryPage {
    [CmdletBinding()]
    param(
        [AllowNull()][AllowEmptyCollection()][object[]]$SortedRows,
        [Parameter(Mandatory = $true)][string]$RepositoryId,
        [Parameter(Mandatory = $true)][string]$Fingerprint,
        [Parameter(Mandatory = $true)][string]$Sort,
        [Parameter(Mandatory = $true)][string]$Order,
        [Parameter(Mandatory = $true)][int]$Limit,
        [AllowNull()][string]$Cursor
    )
    if ($null -eq $SortedRows) { $SortedRows = @() }
    $start = 0
    if (-not [string]::IsNullOrWhiteSpace($Cursor)) {
        $decodedCursor = ConvertFrom-BFRegistryCursor -Cursor $Cursor -RepositoryId $RepositoryId -Fingerprint $Fingerprint
        while ($start -lt $SortedRows.Count) {
            $rowKey = Get-BFRegistrySortFieldValue -Row $SortedRows[$start] -Sort $Sort
            $comparison = [string]::CompareOrdinal($rowKey, [string]$decodedCursor['key'])
            if ($Order -ceq 'desc') { $comparison = -$comparison }
            if ($comparison -eq 0) { $comparison = [string]::CompareOrdinal([string]$SortedRows[$start]['task_id'], [string]$decodedCursor['task_id']) }
            if ($comparison -gt 0) { break }
            $start++
        }
    }
    $page = [System.Collections.Generic.List[object]]::new()
    $index = $start
    while ($index -lt $SortedRows.Count -and $page.Count -lt $Limit) { [void]$page.Add($SortedRows[$index]); $index++ }
    $nextCursor = $null
    if ($page.Count -gt 0 -and ($start + $page.Count) -lt $SortedRows.Count) {
        $nextCursor = ConvertTo-BFRegistryCursor -RepositoryId $RepositoryId -Fingerprint $Fingerprint -Sort $Sort -Order $Order -Row $page[$page.Count - 1]
    }
    return [pscustomobject]@{ Rows = @($page.ToArray()); NextCursor = $nextCursor; Matched = $SortedRows.Count }
}

function ConvertTo-BFRegistryHumanText {
    [CmdletBinding()]
    param([AllowNull()][string]$Value)
    $text = if ($null -eq $Value) { '' } else { [string]$Value }
    $text = ($text -replace "[`t`r`n]", ' ')
    if ([string]::IsNullOrEmpty($text)) { return '-' }
    return $text
}

function Get-BFRegistryHumanList {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)]$Store, [AllowNull()][AllowEmptyCollection()][object[]]$Rows, [Parameter(Mandatory = $true)][int]$Matched, [AllowNull()][string]$NextCursor)
    if ($null -eq $Rows) { $Rows = @() }
    $lines = [System.Collections.Generic.List[string]]::new()
    [void]$lines.Add(("repository {0}" -f $Store.RepositoryId))
    [void]$lines.Add(("matched {0} returned {1} next_cursor {2}" -f $Matched, $Rows.Count, (ConvertTo-BFRegistryHumanText $NextCursor)))
    [void]$lines.Add(("task_id`tstatus`tpriority`tarchived`tupdated`tsource`thealth`tdiagnostic_state`tfreshness`tlabels`ttitle"))
    foreach ($row in $Rows) {
        $title = if ([string]$row['health'] -ceq 'ok') { [string]$row['title'] } else { ("[{0}] {1}" -f $row['health'], $row['diagnostic']) }
        $diagnosticState = if ([string]$row['health'] -ceq 'ok') { '-' } else { [string]$row['health'] }
        [void]$lines.Add((
            "{0}`t{1}`t{2}`t{3}`t{4}`t{5}`t{6}`t{7}`t{8}`t{9}`t{10}" -f `
                (ConvertTo-BFRegistryHumanText ([string]$row['task_id'])),
                (ConvertTo-BFRegistryHumanText ([string]$row['status'])),
                (ConvertTo-BFRegistryHumanText ([string]$row['priority'])),
                ([bool]$row['archived']).ToString().ToLowerInvariant(),
                (ConvertTo-BFRegistryHumanText ([string]$row['updated_at'])),
                (ConvertTo-BFRegistryHumanText ([string]$row['source'])),
                (ConvertTo-BFRegistryHumanText ([string]$row['health'])),
                (ConvertTo-BFRegistryHumanText $diagnosticState),
                (ConvertTo-BFRegistryHumanText ([string]$row['freshness'])),
                (ConvertTo-BFRegistryHumanText ((@($row['labels']) -join ','))),
                (ConvertTo-BFRegistryHumanText $title)
        ))
    }
    return (($lines.ToArray()) -join "`n")
}

function Get-BFRegistryHumanShow {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)]$Store, [Parameter(Mandatory = $true)]$State)
    $metadata = $State['metadata']
    $lines = [System.Collections.Generic.List[string]]::new()
    [void]$lines.Add(("task {0}" -f $State['task_id']))
    [void]$lines.Add(("repository: {0}" -f $Store.RepositoryId))
    [void]$lines.Add(("title: {0}" -f (ConvertTo-BFRegistryHumanText ([string](Get-BFObjectProperty $metadata 'title')))))
    [void]$lines.Add(("description: {0}" -f (ConvertTo-BFRegistryHumanText ([string](Get-BFObjectProperty $metadata 'description')))))
    [void]$lines.Add(("status: {0}" -f (ConvertTo-BFRegistryHumanText ([string](Get-BFObjectProperty $metadata 'status')))))
    [void]$lines.Add(("priority: {0}" -f (ConvertTo-BFRegistryHumanText ([string](Get-BFObjectProperty $metadata 'priority')))))
    [void]$lines.Add(("archived: {0}" -f ([bool](Get-BFObjectProperty $metadata 'archived')).ToString().ToLowerInvariant()))
    [void]$lines.Add(("labels: {0}" -f (ConvertTo-BFRegistryHumanText (@(Get-BFObjectProperty $metadata 'labels') -join ', '))))
    $dependencies = [System.Collections.Generic.List[string]]::new()
    foreach ($dependency in @(Get-BFObjectProperty $metadata 'depends_on')) { $dependencies.Add([string]$dependency) }
    if ($dependencies.Count -eq 0) { [void]$lines.Add('depends_on: -') }
    else {
        [void]$lines.Add('depends_on:')
        foreach ($dependency in $dependencies) {
            $target = Read-BFRegistryTaskState -TasksRoot $Store.TasksRoot -TaskId $dependency
            $targetStatus = if ($null -ne $target -and $target['health'] -ceq 'ok') { [string](Get-BFObjectProperty $target['metadata'] 'status') } else { 'missing' }
            [void]$lines.Add(("  {0} ({1})" -f $dependency, $targetStatus))
        }
    }
    [void]$lines.Add(("created: {0}" -f (ConvertTo-BFRegistryHumanText ([string]$State['created_at']))))
    [void]$lines.Add(("updated: {0}" -f (ConvertTo-BFRegistryHumanText ([string]$State['updated_at']))))
    [void]$lines.Add(("revision: {0}" -f (ConvertTo-BFRegistryHumanText ([string](Get-BFObjectProperty $State['latest'] 'hash')))))
    [void]$lines.Add(("freshness: {0}" -f (Test-BFRegistryFreshness -Document $metadata)))
    [void]$lines.Add('next_action: activate')
    return (($lines.ToArray()) -join "`n")
}

function Get-BFRegistryChangedFields {
    [CmdletBinding()]
    param([AllowNull()]$Previous, [Parameter(Mandatory = $true)]$Current)
    $changed = [System.Collections.Generic.List[string]]::new()
    foreach ($field in @('title', 'description', 'priority', 'labels', 'depends_on', 'status', 'archived')) {
        $before = if ($null -eq $Previous) { $null } else { Get-BFCanonicalJson (Get-BFObjectProperty $Previous $field) }
        $after = Get-BFCanonicalJson (Get-BFObjectProperty $Current $field)
        if ($before -cne $after) { [void]$changed.Add($field) }
    }
    return @($changed.ToArray())
}

function Get-BFRegistryHumanHistory {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)]$Store, [Parameter(Mandatory = $true)][string]$TaskId, [Parameter(Mandatory = $true)]$Entries)
    $lines = [System.Collections.Generic.List[string]]::new()
    [void]$lines.Add(("history {0} repository {1}" -f $TaskId, $Store.RepositoryId))
    $index = 0
    foreach ($entry in $Entries) {
        $index++
        [void]$lines.Add((
            "#{0} {1} {2} hash={3} {4}" -f `
                $index, ([string]$entry['type']), ([string]$entry['timestamp']), ([string]$entry['revision_hash']), ([string]$entry['summary'])
        ))
    }
    return (($lines.ToArray()) -join "`n")
}

function Get-BFRegistryHumanOverview {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)]$Document)
    $lines = [System.Collections.Generic.List[string]]::new()
    [void]$lines.Add(("overview repository {0}" -f $Document['repository_id']))
    [void]$lines.Add(("generated_at: {0}" -f $Document['generated_at']))
    [void]$lines.Add(("scope_identity: {0}" -f $Document['scope_identity']))
    $statusParts = [System.Collections.Generic.List[string]]::new()
    foreach ($key in @($Document['totals_by_status'].Keys)) { $statusParts.Add(("{0}={1}" -f $key, $Document['totals_by_status'][$key])) }
    [void]$lines.Add(("totals_by_status: {0}" -f ($statusParts -join ' ')))
    $priorityParts = [System.Collections.Generic.List[string]]::new()
    foreach ($key in @($Document['totals_by_priority'].Keys)) { $priorityParts.Add(("{0}={1}" -f $key, $Document['totals_by_priority'][$key])) }
    [void]$lines.Add(("totals_by_priority: {0}" -f ($priorityParts -join ' ')))
    $countParts = [System.Collections.Generic.List[string]]::new()
    foreach ($key in @($Document['counts'].Keys)) { $countParts.Add(("{0}={1}" -f $key, $Document['counts'][$key])) }
    [void]$lines.Add(("counts: {0}" -f ($countParts -join ' ')))
    return (($lines.ToArray()) -join "`n")
}

function Invoke-BFRegistryList {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]$Store,
        [AllowNull()][string[]]$Status,
        [AllowNull()][string[]]$Stage,
        [AllowNull()][string[]]$Priority,
        [AllowNull()][string[]]$Label,
        [AllowNull()][string]$UpdatedBefore,
        [AllowNull()][string]$UpdatedAfter,
        [string]$Archived = 'false',
        [string]$Sort,
        [string]$Order,
        [int]$Limit = 0,
        [AllowNull()][string]$Cursor
    )
    if ($Limit -lt 0) { throw (New-BFError 'BF_INVALID' 'limit must be an integer between 1 and 200.') }
    $limitValue = if ($Limit -eq 0) { $script:BFRegistryListDefaultLimit } else { $Limit }
    if ($limitValue -gt $script:BFRegistryListMaximumLimit) { throw (New-BFError 'BF_INVALID' ("limit must not exceed {0}." -f $script:BFRegistryListMaximumLimit)) }
    $archivedValue = if ([string]::IsNullOrWhiteSpace($Archived)) { 'false' } else { $Archived }
    if ($archivedValue -cnotin $script:BFRegistryArchivedModes) { throw (New-BFError 'BF_INVALID' ("archived must be one of: {0}." -f ($script:BFRegistryArchivedModes -join ', '))) }
    $resolved = Resolve-BFRegistrySortOrder -Sort $Sort -Order $Order
    # Re-normalize here: forwarding a $null filter through a [string[]] boundary
    # re-creates the @($null) artifact at this function boundary.
    $statusFilter = Get-BFRegistryValueList $Status -SplitComma
    $stageFilter = Get-BFRegistryValueList $Stage -SplitComma
    $priorityFilter = Get-BFRegistryValueList $Priority -SplitComma
    $labelFilter = Get-BFRegistryValueList $Label -SplitComma
    $before = if ([string]::IsNullOrWhiteSpace($UpdatedBefore)) { $null } else { ConvertTo-BFRegistryTimestamp $UpdatedBefore }
    $after = if ([string]::IsNullOrWhiteSpace($UpdatedAfter)) { $null } else { ConvertTo-BFRegistryTimestamp $UpdatedAfter }
    $fingerprint = Get-BFHash ([ordered]@{
        status         = $statusFilter
        stage          = $stageFilter
        priority       = $priorityFilter
        label          = $labelFilter
        updated_before = $before
        updated_after  = $after
        archived       = $archivedValue
        sort           = [string]$resolved['sort']
        order          = [string]$resolved['order']
    })
    $collected = Get-BFRegistryRows -Store $Store
    $filtered = Select-BFRegistryRows -Rows $collected.Rows -Status $statusFilter -Stage $stageFilter -Priority $priorityFilter -Label $labelFilter -UpdatedBefore $UpdatedBefore -UpdatedAfter $UpdatedAfter -Archived $archivedValue
    $sorted = Sort-BFRegistryRows -Rows $filtered -Sort ([string]$resolved['sort']) -Order ([string]$resolved['order'])
    $page = Get-BFRegistryPage -SortedRows $sorted -RepositoryId $Store.RepositoryId -Fingerprint $fingerprint -Sort ([string]$resolved['sort']) -Order ([string]$resolved['order']) -Limit $limitValue -Cursor $Cursor
    $items = [System.Collections.Generic.List[object]]::new()
    foreach ($row in @($page.Rows)) { [void]$items.Add((ConvertTo-BFRegistryListItem -Row $row)) }
    $document = [ordered]@{
        schema_version = 1
        repository_id  = $Store.RepositoryId
        tasks          = @($items.ToArray())
        next_cursor    = $page.NextCursor
    }
    $human = (Get-BFRegistryHumanList -Store $Store -Rows $page.Rows -Matched $page.Matched -NextCursor $page.NextCursor)
    return [pscustomobject]@{ Document = $document; Human = $human }
}

function Invoke-BFRegistryShow {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)]$Store, [Parameter(Mandatory = $true)][string]$TaskId)
    Assert-BFRegistryUuid $TaskId
    $state = Read-BFRegistryTaskState -TasksRoot $Store.TasksRoot -TaskId $TaskId
    if ($null -eq $state) { throw (New-BFError 'BF_INVALID' ("task {0} does not exist in the repository store." -f $TaskId)) }
    if ($state['health'] -ceq 'orphaned') { throw (New-BFError 'BF_BLOCKED' ("task journal is orphaned: {0}" -f $state['diagnostic'])) }
    if ($state['health'] -cne 'ok') { throw (New-BFError 'BF_BLOCKED' ("task journal is corrupt: {0}" -f $state['diagnostic'])) }
    $legacyConflict = Get-BFRegistryLegacyConflict -Store $Store -TaskId $TaskId
    if ($null -ne $legacyConflict) { throw (New-BFError 'BF_BLOCKED' $legacyConflict) }
    $metadata = $state['metadata']
    $statusValue = [string](Get-BFObjectProperty $metadata 'status')
    $rowDependencies = [System.Collections.Generic.List[string]]::new()
    foreach ($dependency in @(Get-BFObjectProperty $metadata 'depends_on')) { if ($null -ne $dependency) { $rowDependencies.Add([string]$dependency) } }
    $graphNodes = [System.Collections.Generic.List[string]]::new()
    $graphNodes.Add($TaskId)
    $graphEdges = [System.Collections.Generic.List[object]]::new()
    foreach ($dependency in $rowDependencies) {
        $graphNodes.Add($dependency)
        [void]$graphEdges.Add([ordered]@{ from = $TaskId; to = $dependency })
    }
    # Resolve dependency statuses for the summary against the store snapshot.
    $resolvedStates = [System.Collections.Generic.Dictionary[string, object]]::new([System.StringComparer]::Ordinal)
    foreach ($dependency in $rowDependencies) {
        $target = Read-BFRegistryTaskState -TasksRoot $Store.TasksRoot -TaskId $dependency
        if ($null -ne $target) { $resolvedStates[$dependency] = $target }
    }
    $summary = Get-BFRegistryDependencySummary -DependsOn $rowDependencies.ToArray() -States $resolvedStates
    $planned = ($statusValue -ceq 'planned')
    $document = [ordered]@{
        schema_version      = 1
        repository_id       = $Store.RepositoryId
        task_id             = $TaskId
        title               = [string](Get-BFObjectProperty $metadata 'title')
        status              = $statusValue
        stage               = $null
        next_action         = ($(if ($planned) { 'activate' } else { $null }))
        priority            = [string](Get-BFObjectProperty $metadata 'priority')
        labels              = @(Get-BFObjectProperty $metadata 'labels')
        dependency_summary  = $summary
        created_at          = [string]$state['created_at']
        updated_at          = [string]$state['updated_at']
        origin_worktree     = ''
        current_worktree    = $null
        diagnostic_state    = $null
        freshness           = (Test-BFRegistryFreshness -Document $metadata)
        request_summary     = $null
        criteria_ids        = @()
        observations        = @()
        blockers            = @()
        question            = $null
        dependency_graph    = [ordered]@{ nodes = @($graphNodes.ToArray()); edges = @($graphEdges.ToArray()) }
        attempts_summary    = [ordered]@{ total = 0; terminal = 0; active = 0 }
        acceptance_summary  = [ordered]@{ status = $null; criteria_passed = 0; criteria_total = 0 }
        evidence_references = @()
        description         = [string](Get-BFObjectProperty $metadata 'description')
        archived            = [bool](Get-BFObjectProperty $metadata 'archived')
        revision            = [string](Get-BFObjectProperty $state['latest'] 'hash')
    }
    return [pscustomobject]@{ Document = $document; Human = (Get-BFRegistryHumanShow -Store $Store -State $state) }
}

function Invoke-BFRegistryHistory {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)]$Store, [Parameter(Mandatory = $true)][string]$TaskId)
    Assert-BFRegistryUuid $TaskId
    $state = Read-BFRegistryTaskState -TasksRoot $Store.TasksRoot -TaskId $TaskId
    if ($null -eq $state) { throw (New-BFError 'BF_INVALID' ("task {0} does not exist in the repository store." -f $TaskId)) }
    if ($state['health'] -ceq 'orphaned') { throw (New-BFError 'BF_BLOCKED' ("task journal is orphaned: {0}" -f $state['diagnostic'])) }
    if ($state['health'] -cne 'ok') { throw (New-BFError 'BF_BLOCKED' ("task journal is corrupt: {0}" -f $state['diagnostic'])) }
    $legacyConflict = Get-BFRegistryLegacyConflict -Store $Store -TaskId $TaskId
    if ($null -ne $legacyConflict) { throw (New-BFError 'BF_BLOCKED' $legacyConflict) }
    $entries = [System.Collections.Generic.List[object]]::new()
    $previousPayload = $null
    foreach ($revision in @($state['revisions'])) {
        $payload = Get-BFObjectProperty $revision 'payload'
        $op = [string](Get-BFObjectProperty $revision 'op')
        $changed = Get-BFRegistryChangedFields -Previous $previousPayload -Current $payload
        $summary = switch ($op) {
            'create' { ("created planned task '{0}' (priority={1}, labels={2}, depends_on={3})" -f (Get-BFObjectProperty $payload 'title'), (Get-BFObjectProperty $payload 'priority'), @(Get-BFObjectProperty $payload 'labels').Count, @(Get-BFObjectProperty $payload 'depends_on').Count) }
            'edit' { ("metadata updated: {0}" -f (($changed | Where-Object { $_ -cne 'status' }) -join ',')) }
            'archive' { 'archived=true' }
            'unarchive' { 'archived=false' }
            default { $op }
        }
        $eventType = switch ($op) {
            'archive' { 'archive_change' }
            'unarchive' { 'archive_change' }
            default { 'metadata_change' }
        }
        [void]$entries.Add([ordered]@{
            timestamp          = [string](Get-BFObjectProperty $revision 'timestamp_utc')
            type               = $eventType
            revision_hash      = [string](Get-BFObjectProperty $revision 'hash')
            summary            = [string]$summary
            evidence_reference = $null
        })
        $previousPayload = $payload
    }
    $document = [ordered]@{
        schema_version = 1
        repository_id  = $Store.RepositoryId
        timeline       = @($entries.ToArray())
    }
    return [pscustomobject]@{ Document = $document; Human = (Get-BFRegistryHumanHistory -Store $Store -TaskId $TaskId -Entries $document['timeline']) }
}

function Invoke-BFRegistryOverview {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)]$Store)
    $collected = Get-BFRegistryRows -Store $Store
    $byStatus = [ordered]@{}
    foreach ($status in $script:BFRegistryStatuses) { $byStatus[$status] = 0 }
    $byStage = [ordered]@{}
    foreach ($stage in $script:BFRegistryStages) { $byStage[$stage] = 0 }
    $byPriority = [ordered]@{}
    foreach ($priority in $script:BFRegistryPriorities) { $byPriority[$priority] = 0 }
    $counts = [ordered]@{}
    foreach ($key in $script:BFRegistryOverviewCountKeys) { $counts[$key] = 0 }
    foreach ($row in $collected.Rows) {
        # Overview scope is the repository store; legacy discovery rows stay
        # list-visible but are not totals over the repository scope.
        if ($row['source'] -cne 'repository') { continue }
        if ([string]$row['health'] -ceq 'corrupt') { $counts['corrupt']++; continue }
        if ([string]$row['health'] -ceq 'orphaned') { $counts['orphaned']++; continue }
        if ([string]$row['health'] -cne 'ok') { continue }
        $statusValue = [string]$row['status']
        if ([string]::IsNullOrEmpty($statusValue)) { $statusValue = 'planned' }
        if ($byStatus.Contains($statusValue)) { $byStatus[$statusValue]++ }
        $priorityValue = [string]$row['priority']
        if ($byPriority.Contains($priorityValue)) { $byPriority[$priorityValue]++ }
        $stageValue = $row['stage']
        if (-not [string]::IsNullOrEmpty([string]$stageValue) -and $byStage.Contains([string]$stageValue)) { $byStage[[string]$stageValue]++ }
        if ([bool]$row['archived']) { $counts['archived']++ }
        if ($counts.Contains($statusValue)) { $counts[$statusValue]++ }
    }
    $document = [ordered]@{
        schema_version     = 1
        repository_id      = $Store.RepositoryId
        totals_by_status   = $byStatus
        totals_by_stage    = $byStage
        totals_by_priority = $byPriority
        counts             = $counts
        generated_at       = (Get-BFRegistryTimestamp ([DateTimeOffset]::UtcNow))
        scope_identity     = $Store.RepositoryId
    }
    return [pscustomobject]@{ Document = $document; Human = (Get-BFRegistryHumanOverview -Document $document) }
}

function New-BFRegistryWriteResult {
    # Write responses report `revision` as the revision HASH string (req 14).
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)]$Store,
        [Parameter(Mandatory = $true)][string]$Command,
        [Parameter(Mandatory = $true)]$Result
    )
    $metadata = $Result.Payload
    $revisionHash = [string](Get-BFObjectProperty $Result.Document 'hash')
    $document = [ordered]@{
        schema_version = 1
        repository_id  = $Store.RepositoryId
        command        = $Command
        task_id        = $Result.TaskId
        revision       = $revisionHash
        status         = [string](Get-BFObjectProperty $metadata 'status')
        archived       = [bool](Get-BFObjectProperty $metadata 'archived')
        priority       = [string](Get-BFObjectProperty $metadata 'priority')
        title          = [string](Get-BFObjectProperty $metadata 'title')
        next_action    = 'activate'
    }
    $human = [System.Collections.Generic.List[string]]::new()
    [void]$human.Add(("command: {0}" -f $Command))
    [void]$human.Add(("task: {0}" -f $Result.TaskId))
    [void]$human.Add(("repository: {0}" -f $Store.RepositoryId))
    [void]$human.Add(("revision: {0}" -f $revisionHash))
    [void]$human.Add(("status: {0}" -f $document['status']))
    [void]$human.Add(("priority: {0}" -f $document['priority']))
    [void]$human.Add(("archived: {0}" -f ([bool]$document['archived']).ToString().ToLowerInvariant()))
    [void]$human.Add(("title: {0}" -f (ConvertTo-BFRegistryHumanText $document['title'])))
    [void]$human.Add('next_action: activate')
    return [pscustomobject]@{ Document = $document; Human = (($human.ToArray()) -join "`n") }
}

function Invoke-BFRegistryCommand {
    # Registry command surface for Invoke-BSLFlowTask.ps1. Emits nothing on the
    # pipeline; returns one result object with the exit code plus the exact
    # stdout/stderr payloads (one versioned JSON document, or stable Human text).
    # JSON-mode errors are exactly { "error": { "class", "message" } }; human
    # mode writes one concise stderr line whose first field is the class.
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$Action,
        [Parameter(Mandatory = $true)][string]$ProjectPath,
        [string]$TaskId,
        [string]$Title,
        [string]$Description,
        [string]$Priority,
        [string[]]$Labels,
        [string[]]$DependsOn,
        [int]$ExpectedRevision = -1,
        [string[]]$Status,
        [string[]]$Stage,
        [string[]]$Label,
        [string]$UpdatedBefore,
        [string]$UpdatedAfter,
        [string]$Archived = 'false',
        [string]$Sort,
        [string]$Order,
        [int]$Limit = 0,
        [string]$Cursor,
        [string]$Format = 'Human',
        [string]$InputFile
    )
    $registryActions = @('Create', 'EditRegistry', 'List', 'Show', 'History', 'Overview', 'ArchiveTask', 'UnarchiveTask', 'Activate')
    try {
        if ($Action -cnotin $registryActions) { throw (New-BFError 'BF_INVALID' ("unknown registry action: {0}" -f $Action)) }
        if ($Format -cnotin @('Human', 'Json')) { throw (New-BFError 'BF_INVALID' ("format must be Human or Json, got: {0}" -f $Format)) }
        $Labels = Get-BFRegistryValueList $Labels
        $DependsOn = Get-BFRegistryValueList $DependsOn
        $store = Get-BFRegistryStore -ProjectRoot $ProjectPath
        $exitCode = 0
        $outcome = $null
        switch ($Action) {
            'Create' { $outcome = New-BFRegistryWriteResult -Store $store -Command 'create' -Result (Invoke-BFRegistryCreate -Store $store -Title $Title -Description $Description -Priority $Priority -Labels $Labels -DependsOn $DependsOn) }
            'EditRegistry' { $outcome = New-BFRegistryWriteResult -Store $store -Command 'edit' -Result (Invoke-BFRegistryEdit -Store $store -TaskId $TaskId -ExpectedRevision $ExpectedRevision -Title $Title -Description $Description -Priority $Priority -Labels $Labels -DependsOn $DependsOn) }
            'ArchiveTask' { $outcome = New-BFRegistryWriteResult -Store $store -Command 'archive' -Result (Invoke-BFRegistrySetArchived -Store $store -TaskId $TaskId -Archived $true) }
            'UnarchiveTask' { $outcome = New-BFRegistryWriteResult -Store $store -Command 'unarchive' -Result (Invoke-BFRegistrySetArchived -Store $store -TaskId $TaskId -Archived $false) }
            'List' { $outcome = Invoke-BFRegistryList -Store $store -Status $Status -Stage $Stage -Priority $Priority -Label $Label -UpdatedBefore $UpdatedBefore -UpdatedAfter $UpdatedAfter -Archived $Archived -Sort $Sort -Order $Order -Limit $Limit -Cursor $Cursor }
            'Show' { $outcome = Invoke-BFRegistryShow -Store $store -TaskId $TaskId }
            'History' { $outcome = Invoke-BFRegistryHistory -Store $store -TaskId $TaskId }
            'Overview' { $outcome = Invoke-BFRegistryOverview -Store $store }
            'Activate' {
                $activation = Invoke-BFRegistryActivate -Store $store -TaskId $TaskId -InputFile $InputFile
                # Staged outcome: BF_BLOCKED-shaped error document, exit 11, and
                # no revision (requirement 5 / error schema v1).
                $document = [ordered]@{
                    error = [ordered]@{ class = 'BF_BLOCKED'; message = [string]$activation.Reason }
                }
                $human = [System.Collections.Generic.List[string]]::new()
                [void]$human.Add('command: activate')
                [void]$human.Add(("task: {0}" -f $TaskId))
                [void]$human.Add(("repository: {0}" -f $activation.RepositoryId))
                [void]$human.Add('staged: activation blocked')
                [void]$human.Add(("BF_BLOCKED: {0}" -f $activation.Reason))
                $outcome = [pscustomobject]@{ Document = $document; Human = (($human.ToArray()) -join "`n") }
                $exitCode = 11
            }
            default { throw (New-BFError 'BF_INVALID' ("unknown registry action: {0}" -f $Action)) }
        }
        $stdoutText = if ($Format -ceq 'Json') { Get-BFCanonicalJson $outcome.Document } else { $outcome.Human }
        return [pscustomobject]@{ ExitCode = $exitCode; StdOut = $stdoutText; StdErr = '' }
    }
    catch {
        $reason = [string]$_.Exception.Message
        if ($reason -cnotmatch '^BF_(INVALID|CONFLICT|BLOCKED):') { throw }
        $class = $reason.Substring(0, $reason.IndexOf(':'))
        $message = $reason.Substring($reason.IndexOf(':') + 1).Trim()
        $document = [ordered]@{
            error = [ordered]@{ class = $class; message = $message }
        }
        $stdoutText = ''
        $stderrText = ''
        if ($Format -ceq 'Json') { $stdoutText = Get-BFCanonicalJson $document } else { $stderrText = $reason }
        return [pscustomobject]@{ ExitCode = $(if ($class -ceq 'BF_INVALID') { 2 } else { 11 }); StdOut = $stdoutText; StdErr = $stderrText }
    }
}
