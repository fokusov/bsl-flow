#Requires -Version 7.0
<#
.SYNOPSIS
Runs BSL Flow benchmark tasks (Ф6 of docs/plans/2026-09-26-remediation-plan.md) against a
headless agent, in a chosen mode, and scores each attempt with hidden acceptance checks.

.DESCRIPTION
For every matching bench/tasks/<id>/task.yaml, and for -Repeat attempts:
  1. Stage a fresh temporary git repository seeded from the task's fixture.
  2. Stage the mode (bare: nothing extra; core: an isolated Core-skills config; managed: out
     of scope today, recorded as mode_unsupported).
  3. Run the agent headless with the task's prompt (bench/agents/fake-agent.ps1 for -Agent
     fake; `claude -p --output-format json` / `codex exec --json` otherwise), under a
     timeout.
  4. Score the resulting working tree with the task's hidden acceptance checks, plus scope
     drift against expected_scope and a false-PASS check against the agent's own claim.
  5. Write one JSON result file per attempt to -OutputDir.

Never estimates tokens/cost: when the agent's own JSON output doesn't report them, the
result's usage/cost fields stay null (see bench/README.md).

.PARAMETER Agent
'fake' (default, no model calls - see bench/agents/fake-agent.ps1), 'claude', or 'codex'.

.PARAMETER Mode
'bare' (no skills/bootstrap staged), 'core' (Core skills installed into an isolated config
dir), or 'managed' (unsupported today - recorded as mode_unsupported, no attempt is run).

.PARAMETER Tasks
Glob (relative to the repo root) matching bench/tasks/<id> directories. Default:
'bench/tasks/*'.
#>
[CmdletBinding()]
param(
    [ValidateSet('fake', 'claude', 'codex')]
    [string]$Agent = 'fake',
    [ValidateSet('bare', 'core', 'managed')]
    [string]$Mode = 'bare',
    [string]$Tasks = 'bench/tasks/*',
    [ValidateRange(1, 100)]
    [int]$Repeat = 1,
    [Parameter(Mandatory)]
    [string]$OutputDir,
    [int]$TimeoutSeconds = 600,
    [string]$RepoRoot
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ([string]::IsNullOrWhiteSpace($RepoRoot)) { $RepoRoot = Split-Path -Parent $PSScriptRoot }
$RepoRoot = [IO.Path]::GetFullPath($RepoRoot)
. (Join-Path $PSScriptRoot 'BenchTask.Common.ps1')

function Invoke-BenchGit {
    param([Parameter(Mandatory)][string[]]$Arguments, [Parameter(Mandatory)][string]$WorkingDirectory)
    $previous = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        # core.quotepath=false: otherwise git wraps any path with non-ASCII (e.g. Cyrillic)
        # bytes in "..." with \NNN octal escapes, which breaks the glob matching below.
        $output = git -c core.quotepath=false -C $WorkingDirectory @Arguments 2>&1 | ForEach-Object { $_.ToString() } | Out-String
        $exit = $LASTEXITCODE
    }
    finally { $ErrorActionPreference = $previous }
    if ($exit -ne 0) { throw "git $($Arguments -join ' ') failed in $WorkingDirectory : $output" }
    return $output.Trim()
}

function ConvertTo-BenchArgumentString {
    <#
    Start-Process -ArgumentList (string[]) joins elements with a plain space and does not
    quote ones containing spaces, which breaks any path under a space-containing directory
    (this repo's own worktree path is one). Build the command line ourselves instead.
    #>
    param([Parameter(Mandatory)][string[]]$Arguments)
    return ($Arguments | ForEach-Object {
            if ($_ -match '[\s"]') { '"' + ($_ -replace '"', '\"') + '"' } else { $_ }
        }) -join ' '
}

function New-BenchTempDir {
    param([Parameter(Mandatory)][string]$Prefix)
    $dir = Join-Path ([IO.Path]::GetTempPath()) "$Prefix-$([Guid]::NewGuid().ToString('N'))"
    New-Item -ItemType Directory -Path $dir -Force | Out-Null
    return $dir
}

<#
Copies the fixture into a fresh temp dir, git-inits it and commits a baseline. Returns
{ RepoPath, BaselineRef }.
#>
function Initialize-BenchFixtureRepo {
    param([Parameter(Mandatory)][string]$FixturePath)
    if (-not (Test-Path -LiteralPath $FixturePath -PathType Container)) { throw "Fixture path not found: $FixturePath" }
    $repoPath = New-BenchTempDir 'bslflow-bench-fixture'
    Copy-Item -Path (Join-Path $FixturePath '*') -Destination $repoPath -Recurse -Force
    Invoke-BenchGit @('init', '-q') $repoPath | Out-Null
    Invoke-BenchGit @('config', 'user.email', 'bench@bsl-flow.local') $repoPath | Out-Null
    Invoke-BenchGit @('config', 'user.name', 'BSL Flow Bench') $repoPath | Out-Null
    Invoke-BenchGit @('add', '-A') $repoPath | Out-Null
    Invoke-BenchGit @('commit', '-q', '-m', 'bench: fixture baseline') $repoPath | Out-Null
    Invoke-BenchGit @('tag', 'bench-baseline') $repoPath | Out-Null
    return [pscustomobject]@{ RepoPath = $repoPath; BaselineRef = 'bench-baseline' }
}

<#
Resolves the fixture directory a task points at, cloning a pinned public repo when the task
uses {repo, commit} instead of a local {path}.
#>
function Resolve-BenchFixturePath {
    param([Parameter(Mandatory)][hashtable]$Fixture, [Parameter(Mandatory)][string]$RepoRoot)
    if ($Fixture.Contains('path') -and -not [string]::IsNullOrWhiteSpace([string]$Fixture['path'])) {
        return [IO.Path]::GetFullPath((Join-Path $RepoRoot ([string]$Fixture['path'])))
    }
    if ($Fixture.Contains('repo') -and $Fixture.Contains('commit')) {
        $cloneDir = New-BenchTempDir 'bslflow-bench-clone'
        Invoke-BenchGit @('clone', '-q', [string]$Fixture['repo'], $cloneDir) (Split-Path -Parent $cloneDir) | Out-Null
        Invoke-BenchGit @('checkout', '-q', [string]$Fixture['commit']) $cloneDir | Out-Null
        return $cloneDir
    }
    throw 'fixture must have either path, or repo+commit.'
}

<#
Stages the requested mode. 'bare' and 'managed' need no files; 'core' installs Core skills
into an isolated directory the caller passes to the agent invocation. Returns a small object
describing what was staged, or $null for 'managed' (the caller treats that as
mode_unsupported and skips the attempt).
#>
function Initialize-BenchMode {
    param([Parameter(Mandatory)][string]$Mode, [Parameter(Mandatory)][string]$RepoRoot)
    switch ($Mode) {
        'bare' { return [pscustomobject]@{ Mode = 'bare'; ConfigDir = $null } }
        'managed' { return $null }
        'core' {
            $configDir = New-BenchTempDir 'bslflow-bench-core-config'
            $skillsSrc = Join-Path $RepoRoot 'global/skills'
            $skillsDst = Join-Path $configDir 'skills'
            if (Test-Path -LiteralPath $skillsSrc -PathType Container) {
                Copy-Item -LiteralPath $skillsSrc -Destination $skillsDst -Recurse -Force
            }
            return [pscustomobject]@{ Mode = 'core'; ConfigDir = $configDir }
        }
    }
}

<#
fake-agent.ps1 prints one JSON line to stdout; running it via Start-Process with redirected
files (rather than capturing the pipeline of a same-process call) keeps it symmetric with how
the real CLIs are invoked below, and makes the timeout kill path safe.
#>
function Invoke-BenchFakeAgent {
    param(
        [Parameter(Mandatory)][string]$TaskId,
        [Parameter(Mandatory)][string]$RepoPath,
        [Parameter(Mandatory)][int]$TimeoutSeconds,
        [string]$FakeVariant
    )
    $stdoutFile = [IO.Path]::GetTempFileName()
    $stderrFile = [IO.Path]::GetTempFileName()
    $fakeScript = Join-Path $PSScriptRoot 'agents/fake-agent.ps1'
    $fakeArgs = @('-NoProfile', '-File', $fakeScript, '-TaskId', $TaskId, '-RepoPath', $RepoPath)
    if (-not [string]::IsNullOrWhiteSpace($FakeVariant)) { $fakeArgs += @('-Variant', $FakeVariant) }
    $sw = [Diagnostics.Stopwatch]::StartNew()
    $proc = Start-Process -FilePath 'pwsh' -ArgumentList (ConvertTo-BenchArgumentString $fakeArgs) -NoNewWindow -PassThru `
        -RedirectStandardOutput $stdoutFile -RedirectStandardError $stderrFile
    $finished = $proc.WaitForExit($TimeoutSeconds * 1000)
    $sw.Stop()
    if (-not $finished) {
        try { $proc.Kill($true) } catch {}
        return [pscustomobject]@{ FinalMessage = $null; Usage = $null; CostUsd = $null; WallSeconds = $sw.Elapsed.TotalSeconds; Status = 'timeout'; Detail = 'fake agent exceeded timeout' }
    }
    $stdout = Get-Content -Raw -LiteralPath $stdoutFile -ErrorAction SilentlyContinue
    $stderrText = Get-Content -Raw -LiteralPath $stderrFile -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $stdoutFile, $stderrFile -Force -ErrorAction SilentlyContinue
    if ($proc.ExitCode -ne 0) {
        return [pscustomobject]@{ FinalMessage = $null; Usage = $null; CostUsd = $null; WallSeconds = $sw.Elapsed.TotalSeconds; Status = 'error'; Detail = "fake agent exit $($proc.ExitCode): $stderrText" }
    }
    try { $parsed = $stdout | ConvertFrom-Json -ErrorAction Stop }
    catch { return [pscustomobject]@{ FinalMessage = $null; Usage = $null; CostUsd = $null; WallSeconds = $sw.Elapsed.TotalSeconds; Status = 'error'; Detail = "fake agent produced non-JSON output: $stdout" } }
    return [pscustomobject]@{
        FinalMessage = $parsed.final_message
        Usage        = if ($parsed.usage) { @{ input_tokens = $parsed.usage.input_tokens; output_tokens = $parsed.usage.output_tokens; total_tokens = $parsed.usage.total_tokens } } else { $null }
        CostUsd      = $parsed.cost_usd
        WallSeconds  = $sw.Elapsed.TotalSeconds
        Status       = 'ok'
        Detail       = $null
    }
}

<#
Runs `claude -p --output-format json` (or `codex exec --json`) headless in $RepoPath.
Best-effort: if the CLI is missing, or (for 'core' mode) the plugin-dir flag this harness
relies on isn't advertised by `claude --help`, the attempt is marked 'agent_unavailable'
rather than failing the whole run - real-agent runs are optional and out of CI's offline
suite (see .github/workflows/bench.yml).
#>
function Invoke-BenchRealAgent {
    param(
        [Parameter(Mandatory)][ValidateSet('claude', 'codex')][string]$Agent,
        [Parameter(Mandatory)][pscustomobject]$StagedMode,
        [Parameter(Mandatory)][string]$RepoPath,
        [Parameter(Mandatory)][string]$PromptText,
        [Parameter(Mandatory)][int]$TimeoutSeconds
    )
    $cliName = if ($Agent -eq 'claude') { 'claude' } else { 'codex' }
    $cli = Get-Command $cliName -ErrorAction SilentlyContinue
    if (-not $cli) {
        return [pscustomobject]@{ FinalMessage = $null; Usage = $null; CostUsd = $null; WallSeconds = 0; Status = 'agent_unavailable'; Detail = "$cliName not found on PATH" }
    }

    $exeArgs = New-Object Collections.Generic.List[string]
    $envOverrides = @{}
    if ($Agent -eq 'claude') {
        $exeArgs.AddRange(@('-p', '--output-format', 'json'))
        if ($StagedMode.Mode -eq 'bare') {
            $exeArgs.AddRange(@('--setting-sources', 'project,local'))
        }
        elseif ($StagedMode.Mode -eq 'core') {
            $helpText = & $cliName --help 2>&1 | Out-String
            if ($helpText -notmatch '--plugin-dir') {
                return [pscustomobject]@{ FinalMessage = $null; Usage = $null; CostUsd = $null; WallSeconds = 0; Status = 'agent_unavailable'; Detail = "claude --help does not advertise --plugin-dir; cannot stage 'core' mode" }
            }
            $exeArgs.AddRange(@('--plugin-dir', $RepoRoot))
        }
    }
    else {
        $exeArgs.AddRange(@('exec', '--json'))
        if ($StagedMode.Mode -eq 'core' -and $StagedMode.ConfigDir) { $envOverrides['CODEX_HOME'] = $StagedMode.ConfigDir }
    }

    $stdoutFile = [IO.Path]::GetTempFileName()
    $stderrFile = [IO.Path]::GetTempFileName()
    $promptFile = [IO.Path]::GetTempFileName()
    [IO.File]::WriteAllText($promptFile, $PromptText, [Text.UTF8Encoding]::new($false))
    $sw = [Diagnostics.Stopwatch]::StartNew()
    $previousEnv = @{}
    foreach ($key in $envOverrides.Keys) { $previousEnv[$key] = [Environment]::GetEnvironmentVariable($key); [Environment]::SetEnvironmentVariable($key, $envOverrides[$key]) }
    try {
        $proc = Start-Process -FilePath $cli.Source -ArgumentList (ConvertTo-BenchArgumentString $exeArgs) -NoNewWindow -PassThru -WorkingDirectory $RepoPath `
            -RedirectStandardInput $promptFile -RedirectStandardOutput $stdoutFile -RedirectStandardError $stderrFile
        $finished = $proc.WaitForExit($TimeoutSeconds * 1000)
    }
    finally {
        foreach ($key in $previousEnv.Keys) { [Environment]::SetEnvironmentVariable($key, $previousEnv[$key]) }
    }
    $sw.Stop()
    if (-not $finished) {
        try { $proc.Kill($true) } catch {}
        Remove-Item -LiteralPath $promptFile -Force -ErrorAction SilentlyContinue
        return [pscustomobject]@{ FinalMessage = $null; Usage = $null; CostUsd = $null; WallSeconds = $sw.Elapsed.TotalSeconds; Status = 'timeout'; Detail = "$cliName exceeded timeout" }
    }
    $stdout = Get-Content -Raw -LiteralPath $stdoutFile -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $stdoutFile, $stderrFile, $promptFile -Force -ErrorAction SilentlyContinue

    $finalMessage = $null; $usage = $null; $costUsd = $null
    try {
        $parsed = $stdout | ConvertFrom-Json -ErrorAction Stop
        # Both CLIs' exact JSON shape can change between versions; read defensively and leave
        # anything not present as null rather than guessing.
        $finalMessage = if ($parsed.result) { [string]$parsed.result } elseif ($parsed.message) { [string]$parsed.message } else { $stdout }
        if ($parsed.usage) { $usage = @{ input_tokens = $parsed.usage.input_tokens; output_tokens = $parsed.usage.output_tokens; total_tokens = $parsed.usage.total_tokens } }
        if ($parsed.total_cost_usd) { $costUsd = $parsed.total_cost_usd } elseif ($parsed.cost_usd) { $costUsd = $parsed.cost_usd }
    }
    catch { $finalMessage = $stdout }

    return [pscustomobject]@{ FinalMessage = $finalMessage; Usage = $usage; CostUsd = $costUsd; WallSeconds = $sw.Elapsed.TotalSeconds; Status = 'ok'; Detail = $null }
}

<#
False-PASS heuristic (documented in bench/DECISION_RULES.md): the agent "claims pass" when its
final message contains a success/PASS-style word and no BLOCKED/failure-style word; for an
ambiguous task, "asked or blocked" is true when the message contains a question mark or a
BLOCKED-style word.
#>
function Get-BenchClaimedOutcome {
    param([string]$FinalMessage, [bool]$Ambiguous)
    if ([string]::IsNullOrWhiteSpace($FinalMessage)) { return [pscustomobject]@{ ClaimedPass = $null; AskedOrBlocked = $null } }
    $blockedPattern = '(?i)\bBLOCKED\b|уточните|не хватает информации|нужен ответ|clarify|need (more )?information'
    $successPattern = '(?i)\bPASS\b|готово|выполнено|done|success'
    $isBlocked = [regex]::IsMatch($FinalMessage, $blockedPattern)
    $hasQuestion = $FinalMessage.Contains('?')
    $claimsSuccess = [regex]::IsMatch($FinalMessage, $successPattern) -and -not $isBlocked
    return [pscustomobject]@{
        ClaimedPass    = $claimsSuccess
        AskedOrBlocked = ($isBlocked -or $hasQuestion)
    }
}

function Invoke-BenchStaticDiffGate {
    param([Parameter(Mandatory)][string]$RepoPath, [Parameter(Mandatory)][string]$BaselineRef)
    $gateScript = Join-Path $RepoRoot 'global/skills/1c-verify/scripts/Invoke-1CStaticDiff.ps1'
    if (-not (Test-Path -LiteralPath $gateScript -PathType Leaf)) { return [pscustomobject]@{ Verdict = 'NOT_RUN'; Reason = 'static_diff_script_missing'; New = @() } }
    $outputPath = Join-Path ([IO.Path]::GetTempPath()) "bslflow-bench-staticdiff-$([Guid]::NewGuid().ToString('N')).json"
    $stdoutFile = [IO.Path]::GetTempFileName()
    $stderrFile = [IO.Path]::GetTempFileName()
    $gateArgs = @('-NoProfile', '-File', $gateScript, '-ProjectPath', $RepoPath, '-BaseRef', $BaselineRef, '-OutputPath', $outputPath)
    $proc = Start-Process -FilePath 'pwsh' -ArgumentList (ConvertTo-BenchArgumentString $gateArgs) -NoNewWindow -PassThru -Wait -RedirectStandardOutput $stdoutFile -RedirectStandardError $stderrFile
    Remove-Item -LiteralPath $stdoutFile, $stderrFile -Force -ErrorAction SilentlyContinue
    if (-not (Test-Path -LiteralPath $outputPath -PathType Leaf)) { return [pscustomobject]@{ Verdict = 'NOT_RUN'; Reason = 'static_diff_no_output'; New = @() } }
    $report = Get-Content -Raw -LiteralPath $outputPath -Encoding UTF8 | ConvertFrom-Json
    Remove-Item -LiteralPath $outputPath -Force -ErrorAction SilentlyContinue
    $changedBsl = @(Invoke-BenchGit @('diff', '--name-only', $BaselineRef, '--', 'src') $RepoPath | Where-Object { $_ -match '\.(bsl|os)$' }).Count -gt 0
    if ($changedBsl -and $report.verdict -eq 'PASS' -and $report.reason -eq 'no_changed_files') {
        # A clean verdict without observing the changed BSL input is not evidence.  Keep the
        # attempt runnable, but expose the missing observation instead of crediting a PASS.
        return [pscustomobject]@{ Verdict = 'NOT_RUN'; Reason = 'static_diff_did_not_observe_changed_bsl'; New = @() }
    }
    return [pscustomobject]@{ Verdict = $report.verdict; Reason = $report.reason; New = @($report.new) }
}

function Invoke-BenchAcceptanceCheck {
    param([Parameter(Mandatory)]$Check, [Parameter(Mandatory)][string]$RepoPath, [Parameter(Mandatory)][string]$BaselineRef)
    $kind = $Check.kind
    $requiresRuntime = [bool]($Check.PSObject.Properties.Match('requires_runtime').Count -and $Check.requires_runtime)
    if ($requiresRuntime -or $kind -eq 'yaxunit') {
        return [pscustomobject]@{ Kind = $kind; Verdict = 'SKIPPED_RUNTIME'; Detail = 'requires a live 1C runtime; not run offline' }
    }
    switch ($kind) {
        'file_contains' {
            $full = Join-Path $RepoPath $Check.path
            if (-not (Test-Path -LiteralPath $full -PathType Leaf)) { return [pscustomobject]@{ Kind = $kind; Verdict = 'FAIL'; Detail = "file not found: $($Check.path)" } }
            $content = Get-Content -Raw -LiteralPath $full -Encoding UTF8
            $ok = [regex]::IsMatch($content, $Check.pattern)
            return [pscustomobject]@{ Kind = $kind; Verdict = $(if ($ok) { 'PASS' } else { 'FAIL' }); Detail = "path=$($Check.path) pattern=$($Check.pattern)" }
        }
        'file_absent' {
            $full = Join-Path $RepoPath $Check.path
            if (-not (Test-Path -LiteralPath $full -PathType Leaf)) { return [pscustomobject]@{ Kind = $kind; Verdict = 'PASS'; Detail = "file absent: $($Check.path)" } }
            $hasPattern = $Check.PSObject.Properties.Match('pattern').Count -gt 0 -and -not [string]::IsNullOrWhiteSpace([string]$Check.pattern)
            if (-not $hasPattern) { return [pscustomobject]@{ Kind = $kind; Verdict = 'FAIL'; Detail = "file present but should be absent: $($Check.path)" } }
            $content = Get-Content -Raw -LiteralPath $full -Encoding UTF8
            $matched = [regex]::IsMatch($content, $Check.pattern)
            return [pscustomobject]@{ Kind = $kind; Verdict = $(if ($matched) { 'FAIL' } else { 'PASS' }); Detail = "path=$($Check.path) pattern=$($Check.pattern)" }
        }
        'command' {
            $stdoutFile = [IO.Path]::GetTempFileName(); $stderrFile = [IO.Path]::GetTempFileName()
            $proc = Start-Process -FilePath 'pwsh' -ArgumentList (ConvertTo-BenchArgumentString @('-NoProfile', '-Command', $Check.command)) -WorkingDirectory $RepoPath -NoNewWindow -PassThru -Wait -RedirectStandardOutput $stdoutFile -RedirectStandardError $stderrFile
            $detail = (Get-Content -Raw -LiteralPath $stderrFile -ErrorAction SilentlyContinue)
            Remove-Item -LiteralPath $stdoutFile, $stderrFile -Force -ErrorAction SilentlyContinue
            return [pscustomobject]@{ Kind = $kind; Verdict = $(if ($proc.ExitCode -eq 0) { 'PASS' } else { 'FAIL' }); Detail = $detail }
        }
        'static_diff' {
            $gate = Invoke-BenchStaticDiffGate -RepoPath $RepoPath -BaselineRef $BaselineRef
            return [pscustomobject]@{ Kind = $kind; Verdict = $gate.Verdict; Detail = $gate.Reason }
        }
        'bslls_new_errors_max' {
            $gate = Invoke-BenchStaticDiffGate -RepoPath $RepoPath -BaselineRef $BaselineRef
            if ($gate.Verdict -eq 'NOT_RUN') { return [pscustomobject]@{ Kind = $kind; Verdict = 'NOT_RUN'; Detail = $gate.Reason } }
            $errorCount = @($gate.New | Where-Object { $_.severity -eq 'Error' }).Count
            $ok = $errorCount -le [int]$Check.value
            return [pscustomobject]@{ Kind = $kind; Verdict = $(if ($ok) { 'PASS' } else { 'FAIL' }); Detail = "new_error_count=$errorCount max=$($Check.value)" }
        }
        'grounding' {
            $groundingScript = Join-Path $RepoRoot 'global/skills/1c-verify/scripts/Test-1CCodeGrounding.ps1'
            if (-not (Test-Path -LiteralPath $groundingScript -PathType Leaf)) { return [pscustomobject]@{ Kind = $kind; Verdict = 'NOT_RUN'; Detail = 'Test-1CCodeGrounding.ps1 not present in this checkout' } }
            $stdoutFile = [IO.Path]::GetTempFileName(); $stderrFile = [IO.Path]::GetTempFileName()
            $proc = Start-Process -FilePath 'pwsh' -ArgumentList (ConvertTo-BenchArgumentString @('-NoProfile', '-File', $groundingScript, '-ProjectPath', $RepoPath, '-BaseRef', $BaselineRef)) -NoNewWindow -PassThru -Wait -RedirectStandardOutput $stdoutFile -RedirectStandardError $stderrFile
            $detail = (Get-Content -Raw -LiteralPath $stderrFile -ErrorAction SilentlyContinue)
            Remove-Item -LiteralPath $stdoutFile, $stderrFile -Force -ErrorAction SilentlyContinue
            if ($proc.ExitCode -ne 0 -and $detail -match "warning: unable to access '.+git\\ignore'") {
                return [pscustomobject]@{ Kind = $kind; Verdict = 'NOT_RUN'; Detail = 'grounding_tool_unavailable_due_to_host_git_excludesfile' }
            }
            return [pscustomobject]@{ Kind = $kind; Verdict = $(if ($proc.ExitCode -eq 0) { 'PASS' } else { 'FAIL' }); Detail = $detail }
        }
        default { return [pscustomobject]@{ Kind = $kind; Verdict = 'NOT_RUN'; Detail = "unhandled kind: $kind" } }
    }
}

function Get-BenchScopeDrift {
    param([Parameter(Mandatory)][string]$RepoPath, [Parameter(Mandatory)][string]$BaselineRef, [Parameter(Mandatory)][string[]]$ExpectedScope)
    Invoke-BenchGit @('add', '-A') $RepoPath | Out-Null
    $numstat = Invoke-BenchGit @('diff', '--numstat', $BaselineRef, '--', '.') $RepoPath
    $statusLines = Invoke-BenchGit @('diff', '--name-status', $BaselineRef, '--', '.') $RepoPath
    $lineCount = 0
    $filesOutsideScope = New-Object Collections.Generic.List[string]
    $newMetadataCount = 0
    foreach ($line in ($numstat -split "`n")) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = $line -split "`t"
        if ($parts.Count -lt 3) { continue }
        $added = 0; $removed = 0
        [void][int]::TryParse($parts[0], [ref]$added)
        [void][int]::TryParse($parts[1], [ref]$removed)
        $relPath = $parts[2]
        if (-not (Test-BTPathMatchesAnyGlob -RelativePath $relPath -Globs $ExpectedScope)) {
            $lineCount += ($added + $removed)
            $filesOutsideScope.Add($relPath)
        }
    }
    foreach ($line in ($statusLines -split "`n")) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = $line -split "`t"
        if ($parts.Count -lt 2) { continue }
        if ($parts[0] -eq 'A' -and -not (Test-BTPathMatchesAnyGlob -RelativePath $parts[1] -Globs $ExpectedScope)) { $newMetadataCount++ }
    }
    return [pscustomobject]@{ LineCount = $lineCount; NewMetadataCount = $newMetadataCount; FilesOutsideScope = $filesOutsideScope.ToArray() }
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

$tasksResolved = Join-Path $RepoRoot $Tasks
$taskDirsList = New-Object Collections.Generic.List[object]
if ((Test-Path -LiteralPath $tasksResolved -PathType Container) -and (Test-Path -LiteralPath (Join-Path $tasksResolved 'task.yaml') -PathType Leaf)) {
    # $Tasks points at exactly one task directory (no wildcard).
    $taskDirsList.Add((Get-Item -LiteralPath $tasksResolved))
}
else {
    foreach ($candidate in (Get-ChildItem -Path $tasksResolved -Directory -ErrorAction SilentlyContinue | Sort-Object Name)) {
        if (Test-Path -LiteralPath (Join-Path $candidate.FullName 'task.yaml') -PathType Leaf) { $taskDirsList.Add($candidate) }
    }
}
$taskDirs = $taskDirsList.ToArray()
if ($taskDirs.Count -eq 0) { throw "No task.yaml found under glob '$Tasks' (resolved against $RepoRoot)." }

[IO.Path]::GetFullPath($OutputDir) | Out-Null
if (-not (Test-Path -LiteralPath $OutputDir)) { New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null }

$results = New-Object Collections.Generic.List[object]

foreach ($taskDir in $taskDirs) {
    $taskYamlPath = Join-Path $taskDir.FullName 'task.yaml'
    $task = Read-BenchTaskYaml $taskYamlPath
    $taskId = [string]$task['id']
    $ambiguous = [bool]$task['ambiguous']
    $fixturePath = Resolve-BenchFixturePath -Fixture $task['fixture'] -RepoRoot $RepoRoot
    $promptPath = Join-Path $taskDir.FullName ([string]$task['request'])
    if (-not (Test-Path -LiteralPath $promptPath -PathType Leaf)) { throw "Task '$taskId': request file not found: $promptPath" }
    $promptText = Get-Content -Raw -LiteralPath $promptPath -Encoding UTF8
    $expectedScope = @($task['expected_scope'])
    $acceptanceChecks = @($task['acceptance'])

    for ($attempt = 1; $attempt -le $Repeat; $attempt++) {
        Write-Host "[$taskId] attempt $attempt/$Repeat (agent=$Agent mode=$Mode)"
        $stagedMode = Initialize-BenchMode -Mode $Mode -RepoRoot $RepoRoot
        $resultPath = Join-Path $OutputDir "$taskId.$Agent.$Mode.$attempt.json"

        if ($null -eq $stagedMode) {
            Write-BTJsonAtomic ([ordered]@{
                    task_id = $taskId; agent = $Agent; mode = $Mode; repeat_index = $attempt
                    status  = 'mode_unsupported'
                }) $resultPath
            continue
        }

        $fixtureRepo = Initialize-BenchFixtureRepo -FixturePath $fixturePath
        try {
            $agentResult = if ($Agent -eq 'fake') {
                Invoke-BenchFakeAgent -TaskId $taskId -RepoPath $fixtureRepo.RepoPath -TimeoutSeconds $TimeoutSeconds -FakeVariant $env:BENCH_FAKE_VARIANT
            }
            else {
                Invoke-BenchRealAgent -Agent $Agent -StagedMode $stagedMode -RepoPath $fixtureRepo.RepoPath -PromptText $promptText -TimeoutSeconds $TimeoutSeconds
            }

            if ($agentResult.Status -ne 'ok') {
                Write-BTJsonAtomic ([ordered]@{
                        task_id = $taskId; size = $task['size']; risk = $task['risk']; ambiguous = $ambiguous
                        agent   = $Agent; mode = $Mode; repeat_index = $attempt
                        status  = $agentResult.Status; detail = $agentResult.Detail; wall_seconds = $agentResult.WallSeconds
                    }) $resultPath
                continue
            }

            $claimed = Get-BenchClaimedOutcome -FinalMessage $agentResult.FinalMessage -Ambiguous $ambiguous
            $acceptanceResults = @($acceptanceChecks | ForEach-Object { Invoke-BenchAcceptanceCheck -Check $_ -RepoPath $fixtureRepo.RepoPath -BaselineRef $fixtureRepo.BaselineRef })
            $scored = @($acceptanceResults | Where-Object { $_.Verdict -notin @('NOT_RUN', 'SKIPPED_RUNTIME') })
            # `acceptance_pass` says that every check which actually ran passed.  It is not a
            # completion claim: an unavailable static tool or required runtime check is carried
            # separately in `acceptance_complete`, and cannot produce `effective_pass`.
            $acceptancePass = ($scored.Count -eq 0) -or (-not ($scored | Where-Object { $_.Verdict -ne 'PASS' }))
            $notRunCount = @($acceptanceResults | Where-Object { $_.Verdict -eq 'NOT_RUN' }).Count
            $skippedRuntimeCount = @($acceptanceResults | Where-Object { $_.Verdict -eq 'SKIPPED_RUNTIME' }).Count
            $acceptanceComplete = ($notRunCount -eq 0 -and $skippedRuntimeCount -eq 0)
            $drift = Get-BenchScopeDrift -RepoPath $fixtureRepo.RepoPath -BaselineRef $fixtureRepo.BaselineRef -ExpectedScope $expectedScope

            $effectivePass = if ($ambiguous) { [bool]$claimed.AskedOrBlocked } else { $acceptanceComplete -and $acceptancePass }
            # A skipped requirement never fabricates a false PASS.  A concrete observed FAIL
            # still counts even when another required check was skipped.
            $falsePass = ($claimed.ClaimedPass -eq $true) -and (-not $acceptancePass)

            Write-BTJsonAtomic ([ordered]@{
                    task_id              = $taskId
                    size                 = $task['size']
                    risk                 = $task['risk']
                    ambiguous            = $ambiguous
                    agent                = $Agent
                    mode                 = $Mode
                    repeat_index         = $attempt
                    status               = 'ok'
                    wall_seconds         = $agentResult.WallSeconds
                    usage                = $agentResult.Usage
                    cost_usd             = $agentResult.CostUsd
                    agent_claimed_pass   = $claimed.ClaimedPass
                    asked_or_blocked     = $claimed.AskedOrBlocked
                    acceptance           = $acceptanceResults
                    acceptance_pass      = $acceptancePass
                    acceptance_complete  = $acceptanceComplete
                    effective_pass       = $effectivePass
                    false_pass           = $falsePass
                    not_run_count        = $notRunCount
                    skipped_runtime_count = $skippedRuntimeCount
                    drift                = $drift
                }) $resultPath
        }
        finally {
            Remove-Item -LiteralPath $fixtureRepo.RepoPath -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

Write-Host "Wrote $($taskDirs.Count * $Repeat) attempt result(s) to $OutputDir"
