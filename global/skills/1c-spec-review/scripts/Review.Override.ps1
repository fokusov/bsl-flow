#Requires -Version 7.0
function Get-BSLFlowOwnerOverride {
    param([string]$ChangeRoot)
    $path = Join-Path $ChangeRoot 'review-reconciliation.json'
    if (-not (Test-Path $path -PathType Leaf)) { return $null }
    $record = Get-Content -Raw $path | ConvertFrom-Json
    if (-not $record.PSObject.Properties['owner_override']) { return $null }
    $override = $record.owner_override
    foreach ($field in @('owner','reason','accepted_risks')) {
        if (-not $override.PSObject.Properties[$field] -or [string]::IsNullOrWhiteSpace([string]$override.$field)) { throw "BF_BLOCKED: owner_override requires $field." }
    }
    foreach ($binding in @(@('spec_sha256','spec.md'),@('original_task_sha256','original-task.md'),@('design_sha256','design.md'))) {
        $inputPath = Join-Path $ChangeRoot $binding[1]
        $expected = if (Test-Path $inputPath -PathType Leaf) { Get-BSLFlowSha256 $inputPath } else { $null }
        if (-not $override.PSObject.Properties[$binding[0]] -or $override.($binding[0]) -cne $expected) { throw "BF_BLOCKED: owner_override is not bound to current $($binding[1])." }
    }
    if (-not (Test-Path (Join-Path $ChangeRoot 'original-task.md') -PathType Leaf)) { throw 'BF_BLOCKED: owner_override requires original-task.md.' }
    return $override
}
