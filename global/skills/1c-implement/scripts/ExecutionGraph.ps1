#Requires -Version 7.0
Set-StrictMode -Version Latest

# Deterministic executor helpers for the optional execution-contract artifact
# triple (contract.yaml / execution.yaml / verification.yaml), v0.1.
#
# Parsing and validation of the artifact triple are NOT duplicated here: the
# helpers invoke Invoke-1CSpecContractLint.ps1 (installed beside the
# 1c-spec-review skill) in-process with -AsModel, so the lint stays the single
# authority for the frozen schema_version 1 format. Everything below is
# deterministic: no model calls, no network, no timestamps in state.json.

function Get-ExecutionGraphArtifacts {
    # Reads and validates the artifact triple via the authoritative lint.
    # Returns a model with Passed, Errors, Artifacts, Requirements, Tasks, Checks.
    param([Parameter(Mandatory)][string]$ChangePath)
    $lintScript = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..\1c-spec-review\scripts\Invoke-1CSpecContractLint.ps1'))
    if (-not (Test-Path -LiteralPath $lintScript -PathType Leaf)) {
        throw "Execution graph helpers require the contract lint script beside the 1c-spec-review skill (not found: $lintScript). Install the full BSL Flow package."
    }
    return (& $lintScript -ChangePath $ChangePath -NoThrow -AsModel)
}

function Get-ExecutionGraphOrder {
    # Deterministic sequential topological order; ties are broken by task id
    # (ordinal). Throws with the cycle path when depends_on is cyclic.
    param([Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Tasks)
    $indegree = @{}
    $dependents = @{}
    foreach ($task in @($Tasks)) {
        $id = "$($task.Id)"
        if ($id -cnotmatch '^T-[0-9]{3}$') { throw "Execution graph task has an invalid id: '$id'." }
        if ($indegree.ContainsKey($id)) { throw "Duplicate task id in execution graph: $id." }
        $indegree[$id] = 0
        $dependents[$id] = [System.Collections.Generic.List[string]]::new()
    }
    foreach ($task in @($Tasks)) {
        $id = "$($task.Id)"
        foreach ($dep in @($task.DependsOn)) {
            $dep = "$dep"
            if ([string]::IsNullOrEmpty($dep)) { continue }
            if (-not $indegree.ContainsKey($dep)) { throw "Task $id depends on unknown task $dep." }
            $indegree[$id]++
            $dependents[$dep].Add($id)
        }
    }
    $ready = [System.Collections.Generic.List[string]]::new()
    foreach ($taskId in @($indegree.Keys | Where-Object { $indegree[$_] -eq 0 })) { $ready.Add($taskId) }
    $order = [System.Collections.Generic.List[string]]::new()
    while ($ready.Count -gt 0) {
        $ready.Sort([System.StringComparer]::Ordinal)
        $current = $ready[0]
        $ready.RemoveAt(0)
        $order.Add($current)
        foreach ($dependent in $dependents[$current]) {
            $indegree[$dependent]--
            if ($indegree[$dependent] -eq 0) { $ready.Add($dependent) }
        }
    }
    if ($order.Count -lt $indegree.Count) {
        $remaining = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
        foreach ($id in @($indegree.Keys)) { if (-not $order.Contains($id)) { [void]$remaining.Add($id) } }
        $path = [System.Collections.Generic.List[string]]::new()
        $position = @{}
        $current = @($remaining | Sort-Object)[0]
        while ($null -ne $current -and -not $position.ContainsKey($current)) {
            $position[$current] = $path.Count
            $path.Add($current)
            $next = $null
            foreach ($candidate in @(@($Tasks | Where-Object { "$($_.Id)" -ceq $current })[0].DependsOn)) {
                if ("$candidate" -and $remaining.Contains("$candidate")) { $next = "$candidate"; break }
            }
            $current = $next
        }
        if ($null -ne $current) {
            $cyclePath = @($path | Select-Object -Skip $position[$current]) + @($current)
            throw "Dependency cycle detected: $($cyclePath -join ' -> ')."
        }
        throw "Execution graph cannot be ordered (unknown cycle state): $(@($remaining | Sort-Object) -join ', ')."
    }
    return @($order)
}

function Test-ExecutionGraphReadOnlyKind {
    # Returns $true for the read-only kinds explore|research|review|document
    # (v0.1 discipline: they never mutate files).
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Kind)
    return ($Kind -cin @('explore', 'research', 'review', 'document'))
}

function ConvertTo-ExecutionGraphGlobRegex {
    # Supports the frozen glob subset: '*' (within a segment) and '**' (across
    # segments); everything else is literal.
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Glob)
    $pattern = [System.Text.StringBuilder]::new()
    $i = 0
    while ($i -lt $Glob.Length) {
        if ($Glob[$i] -eq '*') {
            if ($i + 1 -lt $Glob.Length -and $Glob[$i + 1] -eq '*') { [void]$pattern.Append('.*'); $i += 2 }
            else { [void]$pattern.Append('[^/\\]*'); $i++ }
        }
        else { [void]$pattern.Append(([regex]::Escape([string]$Glob[$i]))); $i++ }
    }
    return '^' + $pattern.ToString() + '$'
}

function Test-ExecutionGraphPathAllowed {
    # $true only when the path matches at least one allowed_scope glob and no
    # forbidden glob (forbidden always wins). Empty allowed_scope allows
    # nothing. Absolute paths and '..' segments are never writable even if a
    # glob would match; the lint rejects such globs, this guard keeps the
    # executor safe against hand-written models.
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$Path,
        [AllowEmptyCollection()][string[]]$AllowedScope = @(),
        [AllowEmptyCollection()][string[]]$Forbidden = @()
    )
    $normalized = $Path -replace '\\', '/'
    if ($normalized -match '^[A-Za-z]:' -or $normalized.StartsWith('/') -or $normalized.StartsWith('\')) { return $false }
    foreach ($segment in ($normalized -split '/')) {
        if ($segment -eq '' -or $segment -eq '..') { return $false }
    }
    foreach ($glob in @($Forbidden)) {
        if ($normalized -match (ConvertTo-ExecutionGraphGlobRegex ($glob -replace '\\', '/'))) { return $false }
    }
    foreach ($glob in @($AllowedScope)) {
        if ($normalized -match (ConvertTo-ExecutionGraphGlobRegex ($glob -replace '\\', '/'))) { return $true }
    }
    return $false
}

function Get-ExecutionGraphScopeViolations {
    # Contract violations for the recorded touched files; empty result means no
    # violation. Callers must record violations in evidence (never silently).
    param(
        [Parameter(Mandatory)][object]$Task,
        [AllowEmptyCollection()][string[]]$TouchedFiles = @()
    )
    if (@($TouchedFiles).Count -eq 0) { return @() }
    $violations = [System.Collections.Generic.List[string]]::new()
    if ((Test-ExecutionGraphReadOnlyKind "$($Task.Kind)") -or "$($Task.Mutation)" -ceq 'forbidden') {
        $violations.Add("mutation is forbidden for task $($Task.Id) (kind '$($Task.Kind)', mutation '$($Task.Mutation)') but files were touched: $(@($TouchedFiles) -join ', ')")
        return @($violations)
    }
    foreach ($file in @($TouchedFiles)) {
        if (-not (Test-ExecutionGraphPathAllowed -Path $file -AllowedScope @($Task.AllowedScope) -Forbidden @($Task.Forbidden))) {
            $violations.Add("file '$file' is outside allowed_scope of task $($Task.Id)")
        }
    }
    return @($violations)
}

function Get-ExecutionGraphVerifyResult {
    # Observable result recorded for a V reference, or $null when missing.
    param([AllowEmptyCollection()][object[]]$VerifyResults = @(), [Parameter(Mandatory)][string]$VerifyId)
    foreach ($entry in @($VerifyResults)) {
        if ($null -eq $entry) { continue }
        $id = $null
        $result = $null
        if ($entry -is [System.Collections.IDictionary]) { $id = $entry['id']; $result = $entry['result'] }
        else { $id = $entry.id; $result = $entry.result }
        if ("$id" -ceq $VerifyId -and -not [string]::IsNullOrWhiteSpace("$result")) { return "$result" }
    }
    return $null
}

function Get-ExecutionGraphBlockedReason {
    # $null when the task may be marked done; otherwise the deterministic
    # BLOCKED reason (scope/mutation violations, missing V results).
    param(
        [Parameter(Mandatory)][object]$Task,
        [AllowEmptyCollection()][string[]]$TouchedFiles = @(),
        [AllowEmptyCollection()][object[]]$VerifyResults = @()
    )
    $problems = [System.Collections.Generic.List[string]]::new()
    foreach ($violation in (Get-ExecutionGraphScopeViolations -Task $Task -TouchedFiles @($TouchedFiles))) { $problems.Add($violation) }
    foreach ($verifyId in @($Task.Verify)) {
        $verifyId = "$verifyId"
        if ([string]::IsNullOrEmpty($verifyId)) { continue }
        if ($null -eq (Get-ExecutionGraphVerifyResult @($VerifyResults) $verifyId)) {
            $problems.Add("no recorded observable result for verify reference '$verifyId'")
        }
    }
    if ($problems.Count -eq 0) { return $null }
    return "task $($Task.Id) cannot be marked done: $($problems -join '; ')"
}

function Write-ExecutionGraphJsonAtomic {
    param([Parameter(Mandatory)]$Value, [Parameter(Mandatory)][string]$Path, [int]$Depth = 20)
    $directory = Split-Path -Parent $Path
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) { New-Item -ItemType Directory -Path $directory -Force | Out-Null }
    $tempPath = Join-Path $directory ('.' + [System.IO.Path]::GetFileName($Path) + '.' + [guid]::NewGuid().ToString('N') + '.tmp')
    try {
        $Value | ConvertTo-Json -Depth $Depth | Set-Content -LiteralPath $tempPath -Encoding utf8
        Move-Item -LiteralPath $tempPath -Destination $Path -Force
    }
    finally {
        if (Test-Path -LiteralPath $tempPath -PathType Leaf) { Remove-Item -LiteralPath $tempPath -Force -ErrorAction SilentlyContinue }
    }
}

function Get-ExecutionGraphEvidencePath {
    param([Parameter(Mandatory)][string]$ChangePath, [Parameter(Mandatory)][string]$TaskId)
    if ($TaskId -cnotmatch '^T-[0-9]{3}$') { throw "Evidence task id must match T-NNN, got: $TaskId" }
    return Join-Path (Join-Path $ChangePath 'evidence') "$TaskId.json"
}

function Write-ExecutionGraphEvidence {
    # Writes evidence/T-NNN.json. 'done' is refused in code while a scope or
    # mutation violation exists or a referenced V lacks an observable result;
    # 'blocked' requires a non-empty reason.
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$ChangePath,
        [Parameter(Mandatory)][object]$Task,
        [Parameter(Mandatory)][ValidateSet('running', 'done', 'blocked')][string]$Status,
        [AllowEmptyCollection()][string[]]$Observations = @(),
        [AllowEmptyCollection()][string[]]$TouchedFiles = @(),
        [AllowEmptyCollection()][object[]]$VerifyResults = @(),
        [string]$Reason = '',
        [AllowEmptyCollection()][string[]]$Violations = @()
    )
    if ($Status -ceq 'blocked' -and [string]::IsNullOrWhiteSpace($Reason)) {
        throw "Evidence for task $($Task.Id) with status 'blocked' requires a non-empty reason."
    }
    if ($Status -ceq 'done') {
        foreach ($violation in (Get-ExecutionGraphScopeViolations -Task $Task -TouchedFiles @($TouchedFiles))) {
            throw "Evidence for task $($Task.Id) cannot be 'done': $violation."
        }
        if (@($Violations).Count -gt 0) {
            throw "Evidence for task $($Task.Id) cannot be 'done' with recorded violations: $(@($Violations) -join '; ')."
        }
        foreach ($verifyId in @($Task.Verify)) {
            $verifyId = "$verifyId"
            if ([string]::IsNullOrEmpty($verifyId)) { continue }
            if ($null -eq (Get-ExecutionGraphVerifyResult @($VerifyResults) $verifyId)) {
                throw "Evidence for task $($Task.Id) cannot be 'done': no recorded observable result for verify reference '$verifyId'."
            }
        }
    }
    $verifyEntries = [System.Collections.Generic.List[object]]::new()
    foreach ($verifyId in @($Task.Verify)) {
        $verifyId = "$verifyId"
        if ([string]::IsNullOrEmpty($verifyId)) { continue }
        $verifyEntries.Add([ordered]@{ id = $verifyId; result = [string](Get-ExecutionGraphVerifyResult @($VerifyResults) $verifyId) })
    }
    $evidence = [ordered]@{
        schema_version = 1
        id = "$($Task.Id)"
        status = $Status
        reason = $Reason
        observations = @($Observations)
        touched_files = @($TouchedFiles)
        verify = @($verifyEntries)
        violations = @($Violations)
    }
    $path = Get-ExecutionGraphEvidencePath -ChangePath $ChangePath -TaskId "$($Task.Id)"
    Write-ExecutionGraphJsonAtomic -Value $evidence -Path $path
    return $path
}

function Get-ExecutionGraphState {
    # GENERATED projection of evidence/ (schema_version 1): T -> pending|running|
    # done|blocked plus the blocked reason. Never hand-authored; rebuildable at
    # any time and byte-stable (no timestamps).
    param([Parameter(Mandatory)][string]$ChangePath)
    $model = Get-ExecutionGraphArtifacts -ChangePath $ChangePath
    if (-not $model.Passed) {
        throw "Execution graph artifacts are invalid; rebuild the state after Invoke-1CSpecContractLint passes: $(@($model.Errors) -join ' ')"
    }
    if ("$($model.Artifacts.execution)" -ne 'True') { throw "state projection requires execution.yaml: $ChangePath" }
    $order = Get-ExecutionGraphOrder -Tasks @($model.Tasks)
    $evidenceByTask = @{}
    $evidenceDirectory = Join-Path $ChangePath 'evidence'
    if (Test-Path -LiteralPath $evidenceDirectory -PathType Container) {
        foreach ($file in @(Get-ChildItem -LiteralPath $evidenceDirectory -Filter 'T-*.json' -File | Sort-Object Name)) {
            $parsed = $null
            try { $parsed = Get-Content -LiteralPath $file.FullName -Raw | ConvertFrom-Json -ErrorAction Stop }
            catch { throw "Evidence file could not be parsed: $($file.FullName)" }
            foreach ($propertyName in @('id', 'status')) {
                if (-not $parsed.PSObject.Properties[$propertyName]) { throw "Evidence file is missing the '$propertyName' field: $($file.FullName)" }
            }
            $id = "$($parsed.id)"
            if ($id -cnotmatch '^T-[0-9]{3}$') { throw "Evidence file has an invalid task id '$id': $($file.FullName)" }
            if ("$($parsed.status)" -cnotin @('running', 'done', 'blocked')) { throw "Evidence file has an invalid status '$($parsed.status)': $($file.FullName)" }
            if ($evidenceByTask.ContainsKey($id)) { throw "Duplicate evidence for task ${id}: $($file.FullName)" }
            $evidenceByTask[$id] = $parsed
        }
    }
    $tasks = [ordered]@{}
    foreach ($taskId in $order) {
        $status = 'pending'
        $reason = ''
        if ($evidenceByTask.ContainsKey($taskId)) {
            $status = "$($evidenceByTask[$taskId].status)"
            if ($status -ceq 'blocked') { $reason = "$($evidenceByTask[$taskId].reason)" }
        }
        $tasks[$taskId] = [ordered]@{ status = $status; reason = $reason }
    }
    foreach ($evidenceId in @($evidenceByTask.Keys)) {
        if (-not $tasks.Contains($evidenceId)) { throw "Evidence references task $evidenceId which is not defined in execution.yaml." }
    }
    return [ordered]@{ schema_version = 1; source = 'evidence'; tasks = $tasks }
}

function Write-ExecutionGraphState {
    # Regenerates state.json from evidence/; the file is a local runtime
    # projection and must not be committed (see .gitignore).
    param([Parameter(Mandatory)][string]$ChangePath, $State)
    if (-not $State) { $State = Get-ExecutionGraphState -ChangePath $ChangePath }
    $path = Join-Path $ChangePath 'state.json'
    Write-ExecutionGraphJsonAtomic -Value $State -Path $path
    return $path
}
