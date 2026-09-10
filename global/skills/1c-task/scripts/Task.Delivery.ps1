#Requires -Version 7.0
Set-StrictMode -Version Latest

function Get-BFDeliveryPath {
    param($State, [string]$Identity)
    if ($Identity -cnotmatch '^[0-9a-f]{64}$') { throw 'BF_INVALID: delivery identity must be a SHA-256 value.' }
    return Assert-BFSafePath (Join-Path (Get-BFTaskDirectory $State.project_path $State.task_id) ('delivery/' + $Identity))
}

function Get-BFDeliveryReceipt {
    param($State)
    if ($State.status -ne 'completed' -or @($State.acceptances).Count -eq 0) { throw 'BF_BLOCKED: delivery requires a completed accepted task.' }
    $accepted = $State.acceptances[-1]
    if ($accepted.verdict -ne 'PASS' -or $accepted.mode -ne 'implement' -or $accepted.intent_revision -ne $State.intent_revision) { throw 'BF_BLOCKED: latest acceptance is not a current implementation PASS.' }
    if ($accepted.sha256 -cnotmatch '^[0-9a-f]{64}$') { throw 'BF_BLOCKED: acceptance identity is invalid.' }
    $path = Assert-BFSafePath $accepted.path
    $directory = Get-BFTaskDirectory $State.project_path $State.task_id
    if (-not $path.StartsWith($directory + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw 'BF_BLOCKED: acceptance receipt escaped its controller directory.' }
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw 'BF_BLOCKED: acceptance receipt is missing.' }
    $receipt = Read-BFJson $path
    if ((Get-BFHash $receipt) -ne $accepted.sha256) { throw 'BF_BLOCKED: acceptance receipt hash does not match task state.' }
    Assert-BFFields $receipt @('schema_version','task_id','intent_revision','mode','intent_hash','policy_hash','baseline','source_manifest','gates','verdict','scope') @() 'acceptance_receipt'
    if ($receipt.schema_version -ne 1 -or $receipt.task_id -ne $State.task_id -or $receipt.intent_revision -ne $State.intent_revision -or $receipt.mode -ne 'implement' -or $receipt.intent_hash -ne $State.intent_hash -or $receipt.policy_hash -ne $State.policy_hash -or $receipt.baseline -ne $State.baseline -or $receipt.verdict -ne 'PASS') { throw 'BF_BLOCKED: acceptance receipt does not bind the current task identity.' }
    return [ordered]@{identity=$accepted.sha256;path=$path;receipt=$receipt;bytes=[IO.File]::ReadAllBytes($path);raw_sha256=Get-BFFileHash $path}
}

function Assert-BFDeliveryCurrent {
    param($State, $Receipt)
    $next = Get-BFNext $State
    if ($next.action -ne 'accept' -or $next.stage -ne 'acceptance') { throw 'BF_BLOCKED: acceptance is stale or task is not ready for delivery.' }
    $current = Get-BFSourceManifest $State
    if ($current.sha256 -ne $Receipt.receipt.source_manifest.sha256 -or (Get-BFHash $current) -ne (Get-BFHash $Receipt.receipt.source_manifest)) { throw 'BF_BLOCKED: current source does not match the accepted manifest.' }
    $expectedGates = @()
    foreach ($stage in @(Get-BFRoute $State | Where-Object { $_ -ne 'acceptance' })) {
        $evidence = @($State.evidence | Where-Object { $_.stage -eq $stage })[-1]
        if ($null -eq $evidence -or -not (Test-BFEvidenceFresh $State $evidence $current)) { throw "BF_BLOCKED: accepted $stage gate is stale." }
        $expectedGates += [ordered]@{stage=$stage;attempt_id=$evidence.attempt_id;result_sha256=$evidence.result_sha256}
    }
    if ((Get-BFHash @($expectedGates)) -ne (Get-BFHash @($Receipt.receipt.gates))) { throw 'BF_BLOCKED: acceptance gates do not match current evidence.' }
    if ((Get-BFHash $Receipt.receipt) -ne $Receipt.identity -or (Get-BFFileHash $Receipt.path) -ne $Receipt.raw_sha256) { throw 'BF_BLOCKED: acceptance receipt changed during delivery.' }
    return $current
}

function Copy-BFDeliveryFile {
    param([string]$Source, [string]$Destination, [string]$ExpectedHash)
    $safeSource = Assert-BFSafePath $Source
    if (-not (Test-Path -LiteralPath $safeSource -PathType Leaf) -or (Get-BFFileHash $safeSource) -ne $ExpectedHash) { throw 'BF_BLOCKED: source file is missing or changed before handoff copy.' }
    $parent = Split-Path -Parent $Destination
    [void][IO.Directory]::CreateDirectory($parent)
    [void](Assert-BFSafePath $parent)
    $sourceStream = [IO.File]::Open($safeSource, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    try {
        $destinationStream = [IO.File]::Open($Destination, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
        try { $sourceStream.CopyTo($destinationStream); $destinationStream.Flush($true) } finally { $destinationStream.Dispose() }
    } finally { $sourceStream.Dispose() }
    if ((Get-BFFileHash $Destination) -ne $ExpectedHash -or (Get-BFFileHash $safeSource) -ne $ExpectedHash) { throw 'BF_BLOCKED: source changed while copying handoff bytes.' }
}

function Get-BFDeliverySummary {
    param($State, $Receipt, $Manifest)
    $lines = @("# Local source handoff", '', "Task: $($State.task_id)", "Acceptance receipt SHA-256: $($Receipt.identity)", "Source manifest SHA-256: $($Manifest.sha256)", '', 'This directory is a local source handoff. No commit, push, package, installation, or environment publication was performed.', '', '## Accepted checks')
    foreach ($gate in @($Receipt.receipt.gates)) { $lines += ('- {0}: result SHA-256 {1}' -f $gate.stage, $gate.result_sha256) }
    return ($lines -join [Environment]::NewLine) + [Environment]::NewLine
}

function Test-BFDeliveryExisting {
    param([string]$Path, $State, $Receipt, $Manifest)
    $root = Assert-BFSafePath $Path
    if (-not (Test-Path -LiteralPath $root -PathType Container)) { return $false }
    foreach ($name in @('acceptance.json','source-manifest.json','handoff.json','summary.md')) {
        if (-not (Test-Path -LiteralPath (Join-Path $root $name) -PathType Leaf)) { return $false }
    }
    $handoff = Read-BFJson (Join-Path $root 'handoff.json')
    try { Assert-BFFields $handoff @('schema_version','identity','task_id','acceptance_sha256','acceptance_copy_sha256','source_manifest_sha256','source_file_count','deleted_file_count','delivery_kind') @() 'delivery_handoff' } catch { return $false }
    if ($handoff.schema_version -ne 1 -or $handoff.identity -ne $Receipt.identity -or $handoff.task_id -ne $State.task_id -or $handoff.acceptance_sha256 -ne $Receipt.identity -or $handoff.source_manifest_sha256 -ne $Manifest.sha256 -or $handoff.source_file_count -ne @($Manifest.files | Where-Object { -not $_.deleted }).Count -or $handoff.deleted_file_count -ne @($Manifest.files | Where-Object { $_.deleted }).Count -or $handoff.delivery_kind -ne 'local_source_handoff') { return $false }
    $acceptanceCopyHash = Get-BFFileHash (Join-Path $root 'acceptance.json')
    if ($acceptanceCopyHash -ne $Receipt.raw_sha256 -or $acceptanceCopyHash -ne $handoff.acceptance_copy_sha256 -or (Get-BFHash (Read-BFJson (Join-Path $root 'acceptance.json'))) -ne $Receipt.identity -or (Get-BFHash (Read-BFJson (Join-Path $root 'source-manifest.json'))) -ne (Get-BFHash $Manifest)) { return $false }
    try { $summary = [Text.UTF8Encoding]::new($false, $true).GetString([IO.File]::ReadAllBytes((Join-Path $root 'summary.md'))) } catch { return $false }
    if ($summary -cne (Get-BFDeliverySummary $State $Receipt $Manifest)) { return $false }
    $expected = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($name in @('acceptance.json','source-manifest.json','handoff.json','summary.md')) { [void]$expected.Add($name) }
    foreach ($file in @($Manifest.files | Where-Object { -not $_.deleted })) {
        $copy = Assert-BFSafePath (Join-Path $root ('source/' + $file.path))
        if (-not (Test-Path -LiteralPath $copy -PathType Leaf) -or (Get-BFFileHash $copy) -ne $file.sha256) { return $false }
        [void]$expected.Add(('source/' + $file.path).Replace('\','/'))
    }
    $actual = @(Get-ChildItem -LiteralPath $root -File -Recurse -Force)
    if ($actual.Count -ne $expected.Count) { return $false }
    foreach ($item in $actual) { $safe = Assert-BFSafePath $item.FullName; $relative = $safe.Substring($root.Length).TrimStart('\','/').Replace('\','/'); if (-not $expected.Contains($relative)) { return $false } }
    return $true
}

function Export-BFTaskDelivery {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$ProjectPath, [Parameter(Mandatory = $true)][string]$TaskId)
    $directory = Get-BFTaskDirectory $ProjectPath $TaskId
    $lock = Enter-BFLock $directory
    $staging = $null
    try {
        $state = Read-BFTask $ProjectPath $TaskId
        $receipt = Get-BFDeliveryReceipt $state
        $manifest = Assert-BFDeliveryCurrent $state $receipt
        $destination = Get-BFDeliveryPath $state $receipt.identity
        if (Test-Path -LiteralPath $destination) {
            if (-not (Test-BFDeliveryExisting $destination $state $receipt $manifest)) { throw 'BF_CONFLICT: existing delivery identity contains conflicting data.' }
            return [ordered]@{task_id=$state.task_id;identity=$receipt.identity;path=$destination;idempotent=$true;source_manifest_sha256=$manifest.sha256}
        }
        $deliveryRoot = Assert-BFSafePath (Join-Path $directory 'delivery')
        [void][IO.Directory]::CreateDirectory($deliveryRoot)
        [void](Assert-BFSafePath $deliveryRoot)
        $staging = Assert-BFSafePath (Join-Path $deliveryRoot ('.' + $receipt.identity + '.' + [guid]::NewGuid().ToString('N') + '.tmp'))
        [void][IO.Directory]::CreateDirectory($staging)
        foreach ($file in @($manifest.files | Where-Object { -not $_.deleted })) {
            Assert-BFRelativePath $file.path
            Copy-BFDeliveryFile (Join-Path $state.worker_path $file.path) (Join-Path $staging ('source/' + $file.path)) $file.sha256
        }
        [IO.File]::WriteAllBytes((Join-Path $staging 'acceptance.json'), $receipt.bytes)
        Write-BFJson -Path (Join-Path $staging 'source-manifest.json') -Value $manifest
        [IO.File]::WriteAllText((Join-Path $staging 'summary.md'), (Get-BFDeliverySummary $state $receipt $manifest), [Text.UTF8Encoding]::new($false))
        $handoff = [ordered]@{schema_version=1;identity=$receipt.identity;task_id=$state.task_id;acceptance_sha256=$receipt.identity;acceptance_copy_sha256=Get-BFFileHash (Join-Path $staging 'acceptance.json');source_manifest_sha256=$manifest.sha256;source_file_count=@($manifest.files | Where-Object { -not $_.deleted }).Count;deleted_file_count=@($manifest.files | Where-Object { $_.deleted }).Count;delivery_kind='local_source_handoff'}
        Write-BFJson -Path (Join-Path $staging 'handoff.json') -Value $handoff
        $current = Assert-BFDeliveryCurrent $state $receipt
        if ((Get-BFHash $current) -ne (Get-BFHash $manifest) -or -not (Test-BFDeliveryExisting $staging $state $receipt $manifest)) { throw 'BF_BLOCKED: handoff staging no longer matches current accepted source.' }
        try { [IO.Directory]::Move($staging, $destination); $staging = $null }
        catch [IO.IOException] {
            if (Test-BFDeliveryExisting $destination $state $receipt $manifest) { return [ordered]@{task_id=$state.task_id;identity=$receipt.identity;path=$destination;idempotent=$true;source_manifest_sha256=$manifest.sha256} }
            throw 'BF_CONFLICT: delivery identity could not be published without overwrite.'
        }
        return [ordered]@{task_id=$state.task_id;identity=$receipt.identity;path=$destination;idempotent=$false;source_manifest_sha256=$manifest.sha256}
    } finally {
        if ($null -ne $staging -and (Test-Path -LiteralPath $staging)) {
            $cleanup = Assert-BFSafePath $staging
            if (-not $cleanup.StartsWith($deliveryRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw 'BF_BLOCKED: delivery cleanup escaped its directory.' }
            Remove-Item -LiteralPath $cleanup -Recurse -Force
        }
        $lock.Dispose()
    }
}
