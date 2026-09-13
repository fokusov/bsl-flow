#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot, [string]$CodexPath, [string]$OutputRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$PackageRoot = [IO.Path]::GetFullPath($PackageRoot)

function Write-FenceJson {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)]$Value)
    $parent = Split-Path -Parent $Path
    [void][IO.Directory]::CreateDirectory($parent)
    [IO.File]::WriteAllText($Path, ($Value | ConvertTo-Json -Depth 100), [Text.UTF8Encoding]::new($false))
}

function Invoke-Version {
    param([Parameter(Mandatory)][string]$Path)
    try {
        $output = & $Path '--version' 2>&1 | ForEach-Object { [string]$_ }
        return [pscustomobject]@{ code = $LASTEXITCODE; text = (@($output) -join "`n").Trim() }
    }
    catch { return [pscustomobject]@{ code = -1; text = $_.Exception.Message } }
}

function Find-Codex154 {
    param([string]$Requested)
    $candidates = [Collections.Generic.List[string]]::new()
    if (-not [string]::IsNullOrWhiteSpace($Requested)) { [void]$candidates.Add($Requested) }
    $command = Get-Command codex -ErrorAction SilentlyContinue
    if ($null -ne $command) { [void]$candidates.Add($command.Source) }
    $ambientCandidates = @()
    if ($env:CODEX_HOME) { $ambientCandidates += Join-Path $env:CODEX_HOME '.sandbox-bin\codex.exe' }
    if ($env:LOCALAPPDATA) {
        $ambientCandidates += Join-Path $env:LOCALAPPDATA 'OpenAI\Codex\codex.exe'
        $ambientCandidates += Join-Path $env:LOCALAPPDATA 'OpenAI\Codex\bin\codex.exe'
    }
    if ($env:USERPROFILE) { $ambientCandidates += Join-Path $env:USERPROFILE '.codex\.sandbox-bin\codex.exe' }
    foreach ($candidate in $ambientCandidates) {
        if (-not [string]::IsNullOrWhiteSpace([string]$candidate)) { [void]$candidates.Add([string]$candidate) }
    }
    $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($candidate in $candidates) {
        if ([string]::IsNullOrWhiteSpace($candidate)) { continue }
        try { $resolved = [IO.Path]::GetFullPath($candidate) } catch { continue }
        if (-not $seen.Add($resolved) -or -not (Test-Path -LiteralPath $resolved -PathType Leaf)) { continue }
        $version = Invoke-Version $resolved
        if ($version.code -eq 0 -and $version.text -ceq 'codex-cli 0.154.0') { return [pscustomobject]@{ path = $resolved; candidates = @($candidates); version = $version.text } }
    }
    return [pscustomobject]@{ path = $null; candidates = @($candidates); version = $null }
}

if ([string]::IsNullOrWhiteSpace($OutputRoot)) {
    $OutputRoot = Join-Path (Join-Path $PackageRoot 'work') ('native-provider-sandbox-fence-' + [guid]::NewGuid().ToString('N'))
}
$OutputRoot = [IO.Path]::GetFullPath($OutputRoot)
if (Test-Path -LiteralPath $OutputRoot) { throw "OutputRoot must be a new evidence directory: $OutputRoot" }
[void][IO.Directory]::CreateDirectory($OutputRoot)
$utf8 = [Text.UTF8Encoding]::new($false)
$found = Find-Codex154 $CodexPath
if ($null -eq $found.path) {
    $blocked = [ordered]@{
        schema_version = 1; status = 'BLOCKED'; reason = 'BF_BLOCKED: exact Codex 0.154.0 executable is unavailable.'
        required_version = 'codex-cli 0.154.0'; candidates = @($found.candidates); model_calls = 0; runtime_1c = 'not_run'
    }
    Write-FenceJson (Join-Path $OutputRoot 'result.json') $blocked
    $blocked | ConvertTo-Json -Depth 8
    exit 11
}

$scripts = Join-Path $PackageRoot 'global\skills\1c-task\scripts'
foreach ($name in @('Task.Storage.ps1', 'Task.Contracts.ps1', 'Task.Process.ps1', 'Task.Execution.ps1')) { . (Join-Path $scripts $name) }
$project = Join-Path $OutputRoot 'project'
$worker = Join-Path $OutputRoot 'worker'
$toolset = Join-Path $OutputRoot 'toolset'
$taskId = [guid]::NewGuid().ToString().ToLowerInvariant()
$canonical = Join-Path $project '.git\bsl-flow'
$directories = @(
    $project, $worker, $toolset,
    (Join-Path $canonical ('tasks\' + $taskId + '\revisions')),
    (Join-Path $canonical ('tasks\' + $taskId + '\inputs'))
)
foreach ($directory in $directories) { [void][IO.Directory]::CreateDirectory($directory) }
[IO.File]::WriteAllText((Join-Path $project '.gitignore'), ".bsl-flow/`n", $utf8)
[IO.File]::WriteAllText((Join-Path $project 'source.txt'), 'sandbox fence fixture', $utf8)
[IO.File]::WriteAllText((Join-Path $worker 'worker.txt'), 'worker read-only fixture', $utf8)
$git = (Get-Command git -ErrorAction Stop).Source
& $git -c core.hooksPath=NUL -c core.fsmonitor=false -C $project init --quiet
& $git -c core.hooksPath=NUL -c core.fsmonitor=false -C $project add .
& $git -c core.hooksPath=NUL -c core.fsmonitor=false -C $project -c user.name='BSL Flow sandbox fence' -c user.email='sandbox-fence@example.invalid' commit --quiet -m fixture
if ($LASTEXITCODE -ne 0) { throw 'Git fixture commit failed.' }
$revision = Join-Path $canonical ('tasks\' + $taskId + '\revisions\000001.json')
$current = Join-Path $canonical ('tasks\' + $taskId + '\current.json')
$activation = Join-Path $canonical ('tasks\' + $taskId + '\inputs\activation.json')
[IO.File]::WriteAllText($revision, '{"task_id":"fixture-revision"}', $utf8)
[IO.File]::WriteAllText($current, '{"revision":1}', $utf8)
[IO.File]::WriteAllText($activation, '{"task_id":"fixture-input"}', $utf8)
$probeRoot = Join-Path $canonical ('native-provider-probe\' + $taskId)
foreach ($relative in @('current\write.txt', 'revisions\write.txt', 'inputs\write.txt')) {
    [void][IO.Directory]::CreateDirectory((Join-Path $probeRoot (Split-Path -Parent $relative)))
    [IO.File]::WriteAllText((Join-Path $probeRoot $relative), 'probe target', $utf8)
}

$hash = (Get-FileHash -LiteralPath $found.path -Algorithm SHA256).Hash.ToLowerInvariant()
$toolsetManifest = [ordered]@{ schema_version = 1; toolset_name = 'sandbox-fence-fixture'; source = [ordered]@{ identity = 'local-private'; path = $toolset }; skills = @(); aggregate_sha256 = (Get-BFHash @()) }
Write-FenceJson (Join-Path $toolset 'toolset-manifest.json') $toolsetManifest
$profile = [pscustomobject]@{
    provider = 'codex'; executable = $found.path; executable_sha256 = $hash
    sandbox = [pscustomobject]@{ executable = $found.path; sha256 = $hash }
    toolset = [pscustomobject]@{ name = 'sandbox-fence-fixture'; root = $toolset }
    denied_read_roots = @()
}
$state = [pscustomobject]@{
    project_path = $project; worker_path = $worker; task_id = $taskId
    request = [pscustomobject]@{ execution_profile = $profile }
}
$evidence = Join-Path $OutputRoot 'capability'
$before = @($revision, $current, $activation) | ForEach-Object { [ordered]@{ path = $_; hash = (Get-FileHash -LiteralPath $_ -Algorithm SHA256).Hash } }
try {
    Test-BFExecutionCapability $state $evidence (Join-Path $evidence 'scratch') (Join-Path $evidence 'config') (Get-BFExecutionPermissionProfile $state (Join-Path $evidence 'scratch') (Join-Path $evidence 'config') $false $canonical) $false $canonical
}
catch {
    $blocked = [ordered]@{ schema_version = 1; status = 'BLOCKED'; reason = [string]$_.Exception.Message; codex_path = $found.path; codex_version = $found.version; model_calls = 0; runtime_1c = 'not_run' }
    Write-FenceJson (Join-Path $OutputRoot 'result.json') $blocked
    $blocked | ConvertTo-Json -Depth 8
    exit 11
}
foreach ($item in $before) {
    if ((Get-FileHash -LiteralPath $item.path -Algorithm SHA256).Hash -cne $item.hash) { throw "Canonical task control bytes changed: $($item.path)" }
}
$actual = Read-BFJson (Join-Path $evidence 'capability.json')
$result = [ordered]@{
    schema_version = 1; status = 'PASS'; codex_path = $found.path; codex_version = $found.version; model_calls = 0; runtime_1c = 'not_run'
    observations = $actual.observations; checks = @('canonical current/revision/inputs reads denied', 'canonical synthetic writes denied', 'scratch write allowed', 'read-only worker write denied', 'task control bytes unchanged')
}
Write-FenceJson (Join-Path $OutputRoot 'result.json') $result
$result | ConvertTo-Json -Depth 20
