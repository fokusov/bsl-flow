#Requires -Version 7.0
# Shared helpers for BSL Flow Claude Code hooks. Kept dependency-free (no
# skill script imports) so hooks stay fast and do not fail closed on an
# unrelated skill script error. Every function here must never throw on
# malformed/missing input; callers decide the safe (allow) fallback.
Set-StrictMode -Version Latest

function Read-BFHookStdin {
    # Reads and parses the hook's stdin JSON. Returns $null (never throws)
    # on empty/malformed input so callers can fail open (allow) per the
    # "hooks must never crash noisily" rule.
    try {
        $raw = [Console]::In.ReadToEnd()
        if ([string]::IsNullOrWhiteSpace($raw)) { return $null }
        return ($raw | ConvertFrom-Json -ErrorAction Stop)
    }
    catch {
        [Console]::Error.WriteLine("bsl-flow hook: could not parse stdin JSON: $($_.Exception.Message)")
        return $null
    }
}

function Write-BFHookAllow {
    # No output / exit 0 = no decision, normal permission flow applies.
    exit 0
}

function Write-BFHookDeny {
    param([Parameter(Mandatory)][string]$Reason)
    $payload = [ordered]@{
        hookSpecificOutput = [ordered]@{
            hookEventName            = 'PreToolUse'
            permissionDecision       = 'deny'
            permissionDecisionReason = $Reason
        }
    }
    Write-Output ($payload | ConvertTo-Json -Depth 6 -Compress)
    exit 0
}

function Get-BFHookFilePath {
    # Edit/Write/MultiEdit all carry tool_input.file_path.
    param($ToolInput)
    if ($null -eq $ToolInput) { return $null }
    $path = $null
    try { $path = [string]$ToolInput.file_path } catch { $path = $null }
    if ([string]::IsNullOrWhiteSpace($path)) { return $null }
    return $path
}

function Test-BF1CProjectMarker {
    # Bounded, fast 1C-project detector: Configuration.xml at root or src
    # (depth <= 2), any *.mdo under src (depth <= 3), bsl-flow.yaml, or
    # .bsl-flow/project.yaml. No deep recursive scan of the whole tree.
    param([Parameter(Mandatory)][string]$ProjectRoot)
    if (-not (Test-Path -LiteralPath $ProjectRoot -PathType Container)) { return $false }
    if (Test-Path -LiteralPath (Join-Path $ProjectRoot 'bsl-flow.yaml') -PathType Leaf) { return $true }
    if (Test-Path -LiteralPath (Join-Path $ProjectRoot '.bsl-flow/project.yaml') -PathType Leaf) { return $true }
    if (Test-Path -LiteralPath (Join-Path $ProjectRoot 'Configuration.xml') -PathType Leaf) { return $true }
    $srcRoot = Join-Path $ProjectRoot 'src'
    if (Test-Path -LiteralPath (Join-Path $srcRoot 'Configuration.xml') -PathType Leaf) { return $true }
    if (Test-Path -LiteralPath $srcRoot -PathType Container) {
        try {
            $mdo = Get-ChildItem -LiteralPath $srcRoot -Filter '*.mdo' -File -Recurse -Depth 2 -ErrorAction SilentlyContinue | Select-Object -First 1
            if ($mdo) { return $true }
        }
        catch { }
    }
    return $false
}

function Get-BFHookSourcePaths {
    # Minimal reader for the `source: paths:` YAML list in bsl-flow.yaml.
    # Defaults to @('src') when the file or key is absent, matching the
    # project template default.
    param([Parameter(Mandatory)][string]$ProjectRoot)
    $yamlPath = Join-Path $ProjectRoot 'bsl-flow.yaml'
    if (-not (Test-Path -LiteralPath $yamlPath -PathType Leaf)) { return @('src') }
    try {
        $text = [IO.File]::ReadAllText($yamlPath, (New-Object Text.UTF8Encoding($false)))
        $match = [regex]::Match($text, '(?ms)^source:\s*\r?\n(?<body>(?:^[ \t]+.*\r?\n?)*)')
        if (-not $match.Success) { return @('src') }
        $body = $match.Groups['body'].Value
        $pathsMatch = [regex]::Match($body, '(?ms)^[ \t]*paths:\s*\r?\n(?<items>(?:^[ \t]*-[ \t]*.+\r?\n?)+)')
        if (-not $pathsMatch.Success) { return @('src') }
        $items = [regex]::Matches($pathsMatch.Groups['items'].Value, '(?m)^[ \t]*-[ \t]*(?<value>.+?)\s*$') |
            ForEach-Object { $_.Groups['value'].Value.Trim('"', "'") } |
            Where-Object { $_ }
        if (@($items).Count -eq 0) { return @('src') }
        return @($items)
    }
    catch { return @('src') }
}

function Test-BFPathUnderSource {
    param(
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string[]]$SourcePaths
    )
    try {
        $fullFile = if ([IO.Path]::IsPathRooted($FilePath)) { [IO.Path]::GetFullPath($FilePath) } else { [IO.Path]::GetFullPath((Join-Path $ProjectRoot $FilePath)) }
    }
    catch { return $false }
    foreach ($sourcePath in $SourcePaths) {
        try {
            $fullSource = [IO.Path]::GetFullPath((Join-Path $ProjectRoot $sourcePath)).TrimEnd('\', '/')
        }
        catch { continue }
        $normalizedFile = $fullFile.TrimEnd('\', '/')
        $comparison = if ($IsWindows) { [StringComparison]::OrdinalIgnoreCase } else { [StringComparison]::Ordinal }
        if ($normalizedFile.Equals($fullSource, $comparison)) { return $true }
        if ($normalizedFile.StartsWith($fullSource + [IO.Path]::DirectorySeparatorChar, $comparison)) { return $true }
        if ($normalizedFile.StartsWith($fullSource + '/', $comparison)) { return $true }
    }
    return $false
}

function Get-BFHookFileSha256 {
    param([Parameter(Mandatory)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return $null }
    try { return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() }
    catch { return $null }
}

function Get-BFHookActiveChange {
    # Reads .bsl-flow/active-change.json. Returns $null when absent or
    # unreadable so callers fail open (S-route allow).
    param([Parameter(Mandatory)][string]$ProjectRoot)
    $path = Join-Path $ProjectRoot '.bsl-flow/active-change.json'
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { return $null }
    try { return ([IO.File]::ReadAllText($path) | ConvertFrom-Json -ErrorAction Stop) }
    catch { return $null }
}

function Add-BFHookGateOverrideRecord {
    param(
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)][string]$FilePath,
        [string]$Change
    )
    try {
        $reportsDir = Join-Path $ProjectRoot '.bsl-flow/reports'
        if (-not (Test-Path -LiteralPath $reportsDir -PathType Container)) { New-Item -ItemType Directory -Path $reportsDir -Force | Out-Null }
        $logPath = Join-Path $reportsDir 'gate-overrides.jsonl'
        $record = [ordered]@{
            recorded_at_utc = [DateTime]::UtcNow.ToString('o')
            hook            = 'PreToolUse-EditGate'
            file            = $FilePath
            change          = $Change
            reason          = 'BSL_FLOW_GATES=off'
        }
        Add-Content -LiteralPath $logPath -Value ($record | ConvertTo-Json -Compress)
    }
    catch { }
}
