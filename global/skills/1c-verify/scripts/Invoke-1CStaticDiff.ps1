#Requires -Version 7.0
<#
.SYNOPSIS
Static-diff gate over BSL Language Server (bslls) diagnostics: reports only NEW diagnostics
introduced by the current change relative to a baseline, so legacy smells never expand scope.

.NOTES
BSL Language Server facts this script relies on (verified against the official docs at
https://1c-syntax.github.io/bsl-language-server/ and https://github.com/1c-syntax/bsl-language-server,
2026-09-26):
  - Analysis mode is invoked with `--analyze` (short `-a`) and takes `--srcDir`/`-s` (required),
    `--outputDir`/`-o`, `--reporter`/`-r` (one of console, junit, json, tslint, generic) and
    `--configuration`/`-c` (defaults to `.bsl-language-server.json` discovery when omitted).
  - `--reporter json` writes `bsl-json.json` inside `--outputDir`.
  - The JSON reporter's top-level shape is `{ date, sourceDir, fileinfos: [...] }`; each fileinfo has
    `path` (a `file:///`-prefixed URI), `mdoRef`, `diagnostics` and optional `metrics`. Each diagnostic
    has `range: { start: {line, character}, end: {line, character} }` (0-based, LSP-style), `severity`
    (the string "Error", "Warning", "Information" or "Hint" - not a numeric LSP code), `code`, `source`
    and `message`.
  - A jar is invoked as `java -jar bsl-language-server.jar --analyze ...`; a native build is invoked
    directly. Both accept the same flags.
See global/skills/1c-verify/references/static-diff.md for the short usage reference and
scripts/fixtures/bslls/sample-analyze-report.json for a realistic report fixture.
#>
[CmdletBinding()]
param(
    [string]$ProjectPath = (Get-Location).Path,
    [string]$BaseRef = 'HEAD',
    [string]$SourcePath,
    [string]$BslLsCommand,
    [string]$BaselineReport,
    [string]$CurrentReport,
    [switch]$Required,
    [string]$OutputPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'TestEvidence.Common.ps1')

function ConvertTo-SDFullPath {
    param([Parameter(Mandatory)][string]$Path)
    return [IO.Path]::GetFullPath($Path)
}

function Join-SDRelative {
    param([Parameter(Mandatory)][string]$Root, [Parameter(Mandatory)][string]$RelativePath)
    $parts = $RelativePath -split '/'
    $full = $Root
    foreach ($part in $parts) { $full = Join-Path $full $part }
    return $full
}

function Get-SDLinesCacheEntry {
    param([Collections.Hashtable]$Cache, [Parameter(Mandatory)][string]$FullPath)
    if ($Cache.ContainsKey($FullPath)) { return $Cache[$FullPath] }
    $lines = if (Test-Path -LiteralPath $FullPath -PathType Leaf) { [IO.File]::ReadAllLines($FullPath) } else { @() }
    $Cache[$FullPath] = $lines
    return $lines
}

function Get-SDDefaultSourcePath {
    param([Parameter(Mandatory)][string]$ProjectPath)
    $yamlPath = Join-Path $ProjectPath 'bsl-flow.yaml'
    if (-not (Test-Path -LiteralPath $yamlPath -PathType Leaf)) { return 'src' }
    $inSource = $false; $sourceIndent = -1; $inPaths = $false; $pathsIndent = -1
    foreach ($line in (Get-Content -LiteralPath $yamlPath -Encoding UTF8)) {
        if ($line -match '^\s*#' -or $line -match '^\s*$') { continue }
        if ($line -notmatch '^(?<indent>\s*)(?<rest>.*)$') { continue }
        $indent = $Matches.indent.Length
        $rest = $Matches.rest
        if (-not $inSource) {
            if ($indent -eq 0 -and $rest -match '^source:\s*$') { $inSource = $true; $sourceIndent = $indent }
            continue
        }
        if (-not $inPaths) {
            if ($indent -le $sourceIndent) { break }
            if ($rest -match '^paths:\s*$') { $inPaths = $true; $pathsIndent = $indent }
            continue
        }
        if ($indent -le $pathsIndent) { break }
        if ($rest -match '^-\s*(?<val>.+?)\s*$') { return $Matches.val.Trim('"', "'") }
    }
    return 'src'
}

function Invoke-SDGit {
    param([Parameter(Mandatory)][string[]]$Arguments, [Parameter(Mandatory)][string]$WorkDir, [bool]$ThrowOnError = $true)
    $allArgs = @('-C', $WorkDir) + $Arguments
    $output = & git @allArgs 2>&1
    $exit = $LASTEXITCODE
    $text = (($output | ForEach-Object { $_.ToString() }) -join [Environment]::NewLine)
    if ($ThrowOnError -and $exit -ne 0) { throw "git $($Arguments -join ' ') failed: $text" }
    return [pscustomobject]@{ ExitCode = $exit; Output = $text }
}

function Get-SDChangedFiles {
    # Returns changed *.bsl/*.os files under $SourceRel as entries {RelPath, BaselineRelPath, CurrentExists}.
    param([Parameter(Mandatory)][string]$ProjectPath, [Parameter(Mandatory)][string]$BaseRef, [Parameter(Mandatory)][string]$SourceRel)
    $entries = @{}
    $diff = Invoke-SDGit @('diff', '--name-status', '-M', '-C', $BaseRef, '--', $SourceRel) $ProjectPath
    foreach ($line in ($diff.Output -split "`r?`n")) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $fields = $line -split "`t"
        $status = $fields[0]
        if ($status[0] -eq 'R' -or $status[0] -eq 'C') {
            if ($fields.Count -lt 3) { continue }
            $oldPath = $fields[1]; $newPath = $fields[2]
            $entries[$newPath] = [pscustomobject]@{ RelPath = $newPath; BaselineRelPath = $oldPath; CurrentExists = $true }
        }
        elseif ($status[0] -eq 'D') {
            if ($fields.Count -lt 2) { continue }
            $entries[$fields[1]] = [pscustomobject]@{ RelPath = $fields[1]; BaselineRelPath = $fields[1]; CurrentExists = $false }
        }
        elseif ($status[0] -eq 'A') {
            if ($fields.Count -lt 2) { continue }
            $entries[$fields[1]] = [pscustomobject]@{ RelPath = $fields[1]; BaselineRelPath = $null; CurrentExists = $true }
        }
        else {
            if ($fields.Count -lt 2) { continue }
            $entries[$fields[1]] = [pscustomobject]@{ RelPath = $fields[1]; BaselineRelPath = $fields[1]; CurrentExists = $true }
        }
    }
    $untracked = Invoke-SDGit @('ls-files', '--others', '--exclude-standard', '--', $SourceRel) $ProjectPath
    foreach ($line in ($untracked.Output -split "`r?`n")) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        if (-not $entries.ContainsKey($line)) { $entries[$line] = [pscustomobject]@{ RelPath = $line; BaselineRelPath = $null; CurrentExists = $true } }
    }
    $result = @($entries.Values | Where-Object { $_.RelPath -match '\.(bsl|os)$' } | Sort-Object RelPath)
    return $result
}

function New-SDTempDirectory {
    param([Parameter(Mandatory)][string]$Prefix)
    $dir = Join-Path ([IO.Path]::GetTempPath()) ($Prefix + '-' + [guid]::NewGuid().ToString('N'))
    [void][IO.Directory]::CreateDirectory($dir)
    return $dir
}

function Get-SDGitBlobBytes {
    # Fetches a blob's exact bytes via `git show <ref>:<path>`, bypassing PowerShell's text
    # pipeline (which decodes/re-encodes through console encoding) so BSL source containing
    # Cyrillic identifiers/comments is copied byte-for-byte, not mangled.
    param([Parameter(Mandatory)][string]$WorkDir, [Parameter(Mandatory)][string]$Ref, [Parameter(Mandatory)][string]$RelPath)
    $psi = [Diagnostics.ProcessStartInfo]::new()
    $psi.FileName = 'git'
    $psi.WorkingDirectory = $WorkDir
    foreach ($arg in @('show', "$($Ref):$($RelPath)")) { [void]$psi.ArgumentList.Add($arg) }
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $psi.UseShellExecute = $false
    $process = [Diagnostics.Process]::Start($psi)
    $memoryStream = [IO.MemoryStream]::new()
    $process.StandardOutput.BaseStream.CopyTo($memoryStream)
    [void]$process.StandardError.ReadToEnd()
    $process.WaitForExit()
    if ($process.ExitCode -ne 0) { return $null }
    return $memoryStream.ToArray()
}

function New-SDBaselineTree {
    param([Parameter(Mandatory)][object[]]$Changes, [Parameter(Mandatory)][string]$ProjectPath, [Parameter(Mandatory)][string]$BaseRef, [Parameter(Mandatory)][string]$TargetDir)
    $count = 0
    foreach ($change in $Changes) {
        if ([string]::IsNullOrWhiteSpace($change.BaselineRelPath)) { continue }
        $bytes = Get-SDGitBlobBytes $ProjectPath $BaseRef $change.BaselineRelPath
        if ($null -eq $bytes) { continue }
        $destination = Join-SDRelative $TargetDir $change.RelPath
        [void][IO.Directory]::CreateDirectory((Split-Path -Parent $destination))
        [IO.File]::WriteAllBytes($destination, $bytes)
        $count++
    }
    return $count
}

function New-SDCurrentTree {
    param([Parameter(Mandatory)][object[]]$Changes, [Parameter(Mandatory)][string]$ProjectPath, [Parameter(Mandatory)][string]$TargetDir)
    $count = 0
    foreach ($change in $Changes) {
        if (-not $change.CurrentExists) { continue }
        $sourceFile = Join-SDRelative $ProjectPath $change.RelPath
        if (-not (Test-Path -LiteralPath $sourceFile -PathType Leaf)) { continue }
        $destination = Join-SDRelative $TargetDir $change.RelPath
        [void][IO.Directory]::CreateDirectory((Split-Path -Parent $destination))
        Copy-Item -LiteralPath $sourceFile -Destination $destination -Force
        $count++
    }
    return $count
}

function Resolve-SDCommandPath {
    param([Parameter(Mandatory)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "BSL LS command was not found: $Path" }
    $resolved = (Resolve-Path -LiteralPath $Path).Path
    $kind = if ($resolved.ToLowerInvariant().EndsWith('.jar')) { 'jar' } else { 'exe' }
    return [pscustomobject]@{ Kind = $kind; Path = $resolved }
}

function Find-SDBslLsCommand {
    param([string]$Explicit)
    if (-not [string]::IsNullOrWhiteSpace($Explicit)) { return Resolve-SDCommandPath $Explicit }
    if (-not [string]::IsNullOrWhiteSpace($env:BSL_FLOW_BSLLS)) { return Resolve-SDCommandPath $env:BSL_FLOW_BSLLS }
    $onPath = Get-Command 'bsl-language-server' -ErrorAction SilentlyContinue
    if ($onPath) { return [pscustomobject]@{ Kind = 'exe'; Path = $onPath.Source } }
    $profilePath = Join-Path ([Environment]::GetFolderPath('UserProfile')) '.bsl-flow/workstation.json'
    if (Test-Path -LiteralPath $profilePath -PathType Leaf) {
        try { $profile = Get-Content -Raw -LiteralPath $profilePath -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop } catch { $profile = $null }
        $bsllsPath = Get-TEProperty $profile @('bslls_path')
        if (-not [string]::IsNullOrWhiteSpace([string]$bsllsPath)) { return Resolve-SDCommandPath ([string]$bsllsPath) }
    }
    return $null
}

function Get-SDToolVersion {
    param([Parameter(Mandatory)][object]$Command)
    try {
        if ($Command.Kind -eq 'jar') { $output = & java -jar $Command.Path '--version' 2>&1 } else { $output = & $Command.Path '--version' 2>&1 }
        if ($LASTEXITCODE -ne 0) { return $null }
        $text = (($output | ForEach-Object { $_.ToString() }) -join ' ').Trim()
        if ([string]::IsNullOrWhiteSpace($text)) { return $null }
        return $text
    }
    catch { return $null }
}

function Invoke-SDBslLsAnalyze {
    param([Parameter(Mandatory)][object]$Command, [Parameter(Mandatory)][string]$SrcDir, [Parameter(Mandatory)][string]$OutDir, [string]$ConfigurationPath)
    [void][IO.Directory]::CreateDirectory($OutDir)
    $arguments = @('--analyze', '--srcDir', $SrcDir, '--outputDir', $OutDir, '--reporter', 'json')
    if (-not [string]::IsNullOrWhiteSpace($ConfigurationPath)) { $arguments += @('--configuration', $ConfigurationPath) }
    if ($Command.Kind -eq 'jar') { $output = & java -jar $Command.Path @arguments 2>&1 } else { $output = & $Command.Path @arguments 2>&1 }
    if ($LASTEXITCODE -ne 0) { throw "BSL LS analyze failed (exit $LASTEXITCODE): $((($output | ForEach-Object { $_.ToString() }) -join [Environment]::NewLine))" }
    $reportPath = Join-Path $OutDir 'bsl-json.json'
    if (-not (Test-Path -LiteralPath $reportPath -PathType Leaf)) { throw "BSL LS did not produce the expected report: $reportPath" }
    return (Get-Content -Raw -LiteralPath $reportPath -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop)
}

function ConvertFrom-SDFileUri {
    param([Parameter(Mandatory)][string]$UriPath)
    $p = $UriPath
    if ($p -match '^file:///(?<rest>.*)$') { $p = $Matches.rest } elseif ($p -match '^file://(?<rest>.*)$') { $p = $Matches.rest }
    if ($IsWindows -or $p -match '^[A-Za-z]:') { $p = $p.Replace('/', '\') }
    return $p
}

function Get-SDRelativePath {
    # Matches a bslls-reported path (a file:// URI, absolute or otherwise) against the known
    # change scope by suffix, rather than by resolving it against a particular analyzed root.
    # This keeps matching correct regardless of whether the report was produced by our own
    # temporary analysis tree or supplied by the caller from an arbitrary sourceDir.
    param([Parameter(Mandatory)][string]$UriOrPath, [Parameter(Mandatory)][Collections.Hashtable]$ScopeSet)
    $native = ConvertFrom-SDFileUri $UriOrPath
    $normalized = $native.Replace('\', '/').TrimStart('/')
    foreach ($candidate in $ScopeSet.Keys) {
        if ($normalized.Length -lt $candidate.Length) { continue }
        $tail = $normalized.Substring($normalized.Length - $candidate.Length)
        if (-not $tail.Equals($candidate, [StringComparison]::OrdinalIgnoreCase)) { continue }
        $precedingIndex = $normalized.Length - $candidate.Length - 1
        if ($precedingIndex -lt 0 -or $normalized[$precedingIndex] -eq '/') { return $candidate }
    }
    return $null
}

function Get-SDNormalizedMessage {
    param([string]$Message)
    return (([string]$Message) -replace '\s+', ' ').Trim()
}

function Get-SDLineHash {
    param([string[]]$Lines, [int]$LineIndex)
    $line1 = if ($LineIndex -ge 0 -and $LineIndex -lt $Lines.Count) { $Lines[$LineIndex].Trim() } else { '' }
    $line2 = if (($LineIndex + 1) -ge 0 -and ($LineIndex + 1) -lt $Lines.Count) { $Lines[$LineIndex + 1].Trim() } else { '' }
    $bytes = [Text.UTF8Encoding]::new($false).GetBytes($line1 + "`n" + $line2)
    return ([BitConverter]::ToString([Security.Cryptography.SHA256]::HashData($bytes)) -replace '-', '').ToLowerInvariant()
}

function Get-SDDiagnosticEntries {
    # Extracts diagnostic entries scoped to files under $ScopeSet from a bslls JSON reporter object.
    param([Parameter(Mandatory)][object]$Report, [Parameter(Mandatory)][string]$AnalyzedRoot, [Parameter(Mandatory)][Collections.Hashtable]$ScopeSet, [Parameter(Mandatory)][Collections.Hashtable]$LinesCache)
    $entries = @()
    foreach ($fileInfo in @(Get-TEArray (Get-TEProperty $Report @('fileinfos')))) {
        $rawPath = [string](Get-TEProperty $fileInfo @('path'))
        if ([string]::IsNullOrWhiteSpace($rawPath)) { continue }
        $relPath = Get-SDRelativePath $rawPath $ScopeSet
        if ($null -eq $relPath) { continue }
        $fullPath = Join-SDRelative $AnalyzedRoot $relPath
        $lines = Get-SDLinesCacheEntry $LinesCache $fullPath
        foreach ($diagnostic in @(Get-TEArray (Get-TEProperty $fileInfo @('diagnostics')))) {
            $range = Get-TEProperty $diagnostic @('range')
            $start = Get-TEProperty $range @('start')
            $lineIndex = [int](Get-TEProperty $start @('line'))
            $code = [string](Get-TEProperty $diagnostic @('code'))
            $severity = [string](Get-TEProperty $diagnostic @('severity'))
            $message = Get-SDNormalizedMessage ([string](Get-TEProperty $diagnostic @('message')))
            $hash = Get-SDLineHash $lines $lineIndex
            $key = "$relPath|$code|$message|$hash"
            $entries += [pscustomobject]@{
                Key      = $key
                RelPath  = $relPath
                Line     = $lineIndex + 1
                Code     = $code
                Severity = $severity
                Message  = $message
            }
        }
    }
    return $entries
}

function Get-SDReportObject {
    param([string]$ExplicitReportPath, [object]$Command, [string]$SrcDir, [string]$OutDir, [string]$ConfigurationPath, [int]$FileCount)
    if (-not [string]::IsNullOrWhiteSpace($ExplicitReportPath)) {
        if (-not (Test-Path -LiteralPath $ExplicitReportPath -PathType Leaf)) { throw "Report file was not found: $ExplicitReportPath" }
        return [pscustomobject]@{ Report = (Get-Content -Raw -LiteralPath $ExplicitReportPath -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop); FreshlyAnalyzed = $false }
    }
    if ($FileCount -eq 0) { return [pscustomobject]@{ Report = [pscustomobject]@{ fileinfos = @() }; FreshlyAnalyzed = $false } }
    if ($null -eq $Command) { return $null }
    return [pscustomobject]@{ Report = (Invoke-SDBslLsAnalyze $Command $SrcDir $OutDir $ConfigurationPath); FreshlyAnalyzed = $true }
}

# ---- Main ----

$projectFull = ConvertTo-SDFullPath $ProjectPath
if (-not (Test-Path -LiteralPath (Join-Path $projectFull '.git') )) { throw "ProjectPath is not a git repository: $projectFull" }
if ([string]::IsNullOrWhiteSpace($SourcePath)) { $SourcePath = Get-SDDefaultSourcePath $projectFull }
$sourceRel = $SourcePath.Replace('\', '/').Trim('/')
if ([string]::IsNullOrWhiteSpace($OutputPath)) {
    $timestamp = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssfffZ')
    $OutputPath = Join-Path $projectFull ".bsl-flow/reports/static/$timestamp.json"
}

function Write-SDVerdict {
    param([hashtable]$Verdict, [Parameter(Mandatory)][string]$OutputPath)
    $ordered = [ordered]@{
        schema_version = 1
        verdict        = $Verdict.verdict
        reason         = $Verdict.reason
        base_ref       = $Verdict.base_ref
        base_commit    = $Verdict.base_commit
        files          = @($Verdict.files)
        new            = @($Verdict.new)
        resolved_count = $Verdict.resolved_count
        legacy_count   = $Verdict.legacy_count
        tool           = $Verdict.tool
    }
    Write-TEJsonAtomic $ordered $OutputPath
    return [pscustomobject]$ordered
}

$baseCommitProbe = Invoke-SDGit @('rev-parse', $BaseRef) $projectFull $false
$baseCommit = if ($baseCommitProbe.ExitCode -eq 0) { $baseCommitProbe.Output.Trim() } else { $null }
if ($null -eq $baseCommit) { throw "BaseRef does not resolve to a commit: $BaseRef" }

$changes = Get-SDChangedFiles $projectFull $BaseRef $sourceRel
$files = @($changes | ForEach-Object { $_.RelPath } | Sort-Object -Unique)

if ($files.Count -eq 0) {
    $verdict = Write-SDVerdict @{ verdict = 'PASS'; reason = 'no_changed_files'; base_ref = $BaseRef; base_commit = $baseCommit; files = @(); new = @(); resolved_count = 0; legacy_count = 0; tool = [ordered]@{ command = $null; version = $null } } $OutputPath
    $verdict
    exit 0
}

$baselineFiles = @($changes | Where-Object { -not [string]::IsNullOrWhiteSpace($_.BaselineRelPath) })
$currentFiles = @($changes | Where-Object { $_.CurrentExists })

$needBaselineTool = ([string]::IsNullOrWhiteSpace($BaselineReport)) -and ($baselineFiles.Count -gt 0)
$needCurrentTool = ([string]::IsNullOrWhiteSpace($CurrentReport)) -and ($currentFiles.Count -gt 0)
$toolNeeded = $needBaselineTool -or $needCurrentTool

$command = $null
$toolVersion = $null
if ($toolNeeded) {
    $command = Find-SDBslLsCommand $BslLsCommand
    if ($null -eq $command) {
        if ($Required) {
            $verdict = Write-SDVerdict @{ verdict = 'BLOCKED'; reason = 'bslls_not_found'; base_ref = $BaseRef; base_commit = $baseCommit; files = $files; new = @(); resolved_count = 0; legacy_count = 0; tool = [ordered]@{ command = $null; version = $null } } $OutputPath
            $verdict
            exit 11
        }
        $verdict = Write-SDVerdict @{ verdict = 'NOT_RUN'; reason = 'bslls_not_found'; base_ref = $BaseRef; base_commit = $baseCommit; files = $files; new = @(); resolved_count = 0; legacy_count = 0; tool = [ordered]@{ command = $null; version = $null } } $OutputPath
        $verdict
        exit 0
    }
    $toolVersion = Get-SDToolVersion $command
}

$configurationPath = Join-Path $projectFull '.bsl-language-server.json'
if (-not (Test-Path -LiteralPath $configurationPath -PathType Leaf)) { $configurationPath = $null }

$baselineDir = New-SDTempDirectory 'bsl-flow-static-baseline'
$currentDir = New-SDTempDirectory 'bsl-flow-static-current'
try {
    $baselineMaterialized = New-SDBaselineTree $baselineFiles $projectFull $BaseRef $baselineDir
    [void](New-SDCurrentTree $currentFiles $projectFull $currentDir)

    $fileListHash = ([BitConverter]::ToString([Security.Cryptography.SHA256]::HashData([Text.UTF8Encoding]::new($false).GetBytes(($files -join "`n")))) -replace '-', '').ToLowerInvariant()
    $cacheDir = Join-Path $projectFull '.bsl-flow/cache/bslls'
    $cachePath = Join-Path $cacheDir "$baseCommit-$fileListHash.json"

    $baselineResult = $null
    if ([string]::IsNullOrWhiteSpace($BaselineReport) -and (Test-Path -LiteralPath $cachePath -PathType Leaf)) {
        $baselineResult = [pscustomobject]@{ Report = (Get-Content -Raw -LiteralPath $cachePath -Encoding UTF8 | ConvertFrom-Json -ErrorAction Stop); FreshlyAnalyzed = $false }
    }
    if ($null -eq $baselineResult) {
        $baselineOutDir = Join-Path $baselineDir '.bslls-output'
        $baselineResult = Get-SDReportObject $BaselineReport $command $baselineDir $baselineOutDir $configurationPath $baselineMaterialized
        if ($null -eq $baselineResult) { throw 'Baseline analysis requires BSL LS but none is available.' }
        if ($baselineResult.FreshlyAnalyzed) {
            [void][IO.Directory]::CreateDirectory($cacheDir)
            Write-TEJsonAtomic $baselineResult.Report $cachePath
        }
    }

    $currentOutDir = Join-Path $currentDir '.bslls-output'
    $currentResult = Get-SDReportObject $CurrentReport $command $currentDir $currentOutDir $configurationPath ($currentFiles.Count)
    if ($null -eq $currentResult) { throw 'Current analysis requires BSL LS but none is available.' }

    $scopeSet = @{}
    foreach ($f in $files) { $scopeSet[$f] = $true }
    $linesCache = @{}

    # Line content for hashing always comes from our own materialized trees (git-show for
    # baseline, working-copy for current), never from a caller-supplied report's own sourceDir:
    # this guarantees the hash reflects the exact BaseRef/working-tree content being compared.
    $baselineEntries = Get-SDDiagnosticEntries $baselineResult.Report $baselineDir $scopeSet $linesCache
    $currentEntries = Get-SDDiagnosticEntries $currentResult.Report $currentDir $scopeSet $linesCache

    $baselineCounts = @{}
    foreach ($entry in $baselineEntries) {
        if ($baselineCounts.ContainsKey($entry.Key)) { $baselineCounts[$entry.Key] = $baselineCounts[$entry.Key] + 1 } else { $baselineCounts[$entry.Key] = 1 }
    }

    $newDiagnostics = @()
    $legacyCount = 0
    foreach ($entry in $currentEntries) {
        if ($baselineCounts.ContainsKey($entry.Key) -and $baselineCounts[$entry.Key] -gt 0) {
            $baselineCounts[$entry.Key] = $baselineCounts[$entry.Key] - 1
            $legacyCount++
        }
        else {
            $newDiagnostics += [ordered]@{ file = $entry.RelPath; line = $entry.Line; code = $entry.Code; severity = $entry.Severity; message = $entry.Message }
        }
    }
    $resolvedCount = 0
    foreach ($value in $baselineCounts.Values) { if ($value -gt 0) { $resolvedCount += $value } }

    $hasNewError = $false
    foreach ($item in $newDiagnostics) { if ($item.severity -eq 'Error') { $hasNewError = $true; break } }

    $verdictName = if ($hasNewError) { 'FAIL' } else { 'PASS' }
    $reason = if ($hasNewError) { 'new_error_diagnostics' } elseif ($newDiagnostics.Count -gt 0) { 'new_diagnostics_below_error_severity' } else { 'no_new_diagnostics' }

    $verdict = Write-SDVerdict @{
        verdict        = $verdictName
        reason         = $reason
        base_ref       = $BaseRef
        base_commit    = $baseCommit
        files          = $files
        new            = $newDiagnostics
        resolved_count = $resolvedCount
        legacy_count   = $legacyCount
        tool           = [ordered]@{ command = if ($command) { $command.Path } else { $null }; version = $toolVersion }
    } $OutputPath
    $verdict
    if ($verdictName -eq 'FAIL') { exit 1 }
    exit 0
}
finally {
    Remove-Item -LiteralPath $baselineDir -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $currentDir -Recurse -Force -ErrorAction SilentlyContinue
}
