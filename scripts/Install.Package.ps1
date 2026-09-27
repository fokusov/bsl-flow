# Shared file transaction: snapshot every touched file before the first mutation.
function Test-BFPackageMember {
    param([string]$Relative,$Definition)
    $included = @($Definition.include | Where-Object { $Relative -like ($_ -replace '\*\*','*') }).Count -gt 0
    $excluded = @($Definition.exclude | Where-Object { $Relative -like ($_ -replace '\*\*','*') }).Count -gt 0
    return $included -and -not $excluded
}
function Assert-BFInstallTarget {
    param([string]$Path,[switch]$Isolated)
    $full = [IO.Path]::GetFullPath($Path)
    if ($Isolated) {
        $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\','/')+[IO.Path]::DirectorySeparatorChar
        if (-not $full.StartsWith($tempRoot,[StringComparison]::OrdinalIgnoreCase)) { throw "Test target must be below the system temporary directory: $full" }
    }
    $cursor = $full
    while ($cursor) {
        if (Test-Path -LiteralPath $cursor) {
            if (((Get-Item -LiteralPath $cursor -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "Installation target contains a reparse point: $cursor" }
        }
        $cursor = Split-Path $cursor -Parent
    }
}
function Invoke-BFInstallTransaction {
    param([object[]]$Copies,[hashtable]$TextWrites,[string]$MarkerPath,[switch]$Isolated,[switch]$SimulatePostApplyFailure)
    $targets = @(@($Copies | ForEach-Object {$_.target}) + @($TextWrites.Keys) | Select-Object -Unique)
    foreach ($target in $targets) {
        Assert-BFInstallTarget $target -Isolated:$Isolated
        if (Test-Path -LiteralPath $target -PathType Container) { throw "Installation file target is a directory: $target" }
    }
    $backup = Join-Path (Split-Path $MarkerPath -Parent) ('backups/'+[guid]::NewGuid().ToString('N'))
    Assert-BFInstallTarget $backup -Isolated:$Isolated
    New-Item -ItemType Directory -Path $backup -Force | Out-Null
    $records = @()
    foreach ($target in $targets) {
        $saved = Join-Path $backup ([string]$records.Count)
        $exists = Test-Path -LiteralPath $target -PathType Leaf
        if ($exists) { Copy-Item -LiteralPath $target -Destination $saved }
        $records += @{target=[IO.Path]::GetFullPath($target);existed=$exists;backup=$saved}
    }
    $records | ConvertTo-Json -Depth 4 | Set-Content (Join-Path $backup 'restore.json') -Encoding utf8
    try {
        foreach ($copy in $Copies) {
            New-Item -ItemType Directory -Path (Split-Path $copy.target -Parent) -Force | Out-Null
            Copy-Item -LiteralPath $copy.source -Destination $copy.target -Force
        }
        foreach ($target in $TextWrites.Keys) {
            New-Item -ItemType Directory -Path (Split-Path $target -Parent) -Force | Out-Null
            [IO.File]::WriteAllText($target,[string]$TextWrites[$target],[Text.UTF8Encoding]::new($false))
        }
        if ($SimulatePostApplyFailure) { throw 'Simulated post-apply failure.' }
    }
    catch {
        $failure = $_
        foreach ($record in $records) {
            if ($record.existed) { Copy-Item -LiteralPath $record.backup -Destination $record.target -Force }
            elseif (Test-Path -LiteralPath $record.target -PathType Leaf) { Remove-Item -LiteralPath $record.target -Force }
        }
        throw "Installation failed; previous file contents restored. Backup: $backup. $($failure.Exception.Message)"
    }
    return $backup
}
