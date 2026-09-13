#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$PackageRoot = [IO.Path]::GetFullPath($PackageRoot)
$entry = Join-Path $PackageRoot 'global\skills\1c-task\scripts\Invoke-BFNativeProvider.ps1'
if (-not (Test-Path -LiteralPath $entry -PathType Leaf)) { throw "Missing native provider entrypoint: $entry" }

$script:checks = 0
function Assert-NativeProvider {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "ASSERTION FAILED: $Message" }
    $script:checks++
}

function Invoke-NativeProviderProcess {
    param([Parameter(Mandatory)][string]$InputText, [Parameter(Mandatory)][string]$WorkingDirectory)
    $shell = Join-Path $PSHOME 'pwsh.exe'
    if (-not (Test-Path -LiteralPath $shell -PathType Leaf)) { throw "PowerShell 7 not found: $shell" }
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $shell
    foreach ($argument in @('-NoProfile', '-NonInteractive', '-File', $entry)) { [void]$start.ArgumentList.Add($argument) }
    $start.WorkingDirectory = $WorkingDirectory
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardInput = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $start.StandardOutputEncoding = [Text.UTF8Encoding]::new($false)
    $start.StandardErrorEncoding = [Text.UTF8Encoding]::new($false)
    $process = [Diagnostics.Process]::Start($start)
    try {
        $bytes = [Text.UTF8Encoding]::new($false).GetBytes($InputText)
        $process.StandardInput.BaseStream.Write($bytes, 0, $bytes.Length)
        $process.StandardInput.Close()
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(60000)) {
            $process.Kill($true)
            throw 'Native provider process exceeded 60 seconds.'
        }
        return [pscustomobject]@{
            code = $process.ExitCode
            stdout = $stdoutTask.GetAwaiter().GetResult()
            stderr = $stderrTask.GetAwaiter().GetResult()
        }
    }
    finally { $process.Dispose() }
}

function ConvertTo-NativeProviderObject {
    param([Parameter(Mandatory)]$Value)
    $text = if ($Value -is [string]) { $Value } else { $Value | ConvertTo-Json -Depth 100 -Compress }
    return $text
}

$root = Join-Path (Join-Path $PackageRoot 'work') ('native-provider-smoke-' + [guid]::NewGuid().ToString('N'))
$project = Join-Path $root 'project'
$context = Join-Path $root 'context'
$artifact = Join-Path $root 'artifact'
$utf8 = [Text.UTF8Encoding]::new($false)
$loaded = @()
try {
    foreach ($directory in @($project, $context, $artifact)) { [void][IO.Directory]::CreateDirectory($directory) }
    [IO.File]::WriteAllText((Join-Path $project 'hello.txt'), "native provider fixture`n", $utf8)
    [IO.File]::WriteAllText((Join-Path $project '.gitignore'), ".bsl-flow/`n", $utf8)
    $git = (Get-Command git -ErrorAction Stop).Source
    & $git -c core.hooksPath=NUL -c core.fsmonitor=false -C $project init --quiet
    & $git -c core.hooksPath=NUL -c core.fsmonitor=false -C $project add .
    & $git -c core.hooksPath=NUL -c core.fsmonitor=false -C $project -c user.name='BSL Flow native provider test' -c user.email='native-provider@example.invalid' commit --quiet -m fixture
    if ($LASTEXITCODE -ne 0) { throw 'Git fixture commit failed.' }

    $scripts = Join-Path $PackageRoot 'global\skills\1c-task\scripts'
    foreach ($name in @('Task.Storage.ps1', 'Task.Contracts.ps1', 'Task.Gates.ps1', 'Task.Engine.ps1')) {
        . (Join-Path $scripts $name)
        $loaded += $name
    }
    $taskId = [guid]::NewGuid().ToString().ToLowerInvariant()
    $request = [ordered]@{
        schema_version = 1; request_id = $taskId; prompt = 'native provider smoke'; mode = 'analysis_only'; analysis_goal = 'analysis'
        complexity = 'S'; risk = 'low'; impact_flags = @()
        criteria = @([ordered]@{ id = 'hello'; kind = 'file_assertion'; observation = 'fixture text remains available'; path = 'hello.txt'; contains = 'native provider' })
        provenance = [ordered]@{ source = 'user'; reference = 'native-provider-test'; text = 'exercise the packaged provider stdin contract' }
        models = [ordered]@{ worker = 'gpt-6-astra'; worker_effort = 'medium'; reviewer = 'gpt-6-astra'; reviewer_effort = 'high' }
    }
    $state = Start-BFTask $project $request
    $state = Read-BFTask $project $taskId
    $canonical = [IO.Path]::GetFullPath((Join-Path $project '.git\bsl-flow'))
    $contract = [ordered]@{ name = 'bsl-flow.native-provider.windows-ps.v1'; version = 1; host_sha256 = ('0' * 64); provider_sha256 = ('1' * 64); asset_manifest_sha256 = ('2' * 64) }

    # This is the same provisional view used by native activation: it points at
    # the exact Git root and carries no active attempt or controller authority.
    $initialView = ConvertFrom-Json -InputObject ($state | ConvertTo-Json -Depth 100) -Depth 100
    $initialView.worker_path = $project
    $input = [ordered]@{
        schema_version = 1; contract = 'bsl-flow.native-provider.windows-ps.v1'; operation = 'measure'; task_id = $taskId
        state_view = $initialView; attempt = $null; context_root = $context; artifact_root = $artifact; canonical_store_root = $canonical
        cancel_signal = Join-Path $context 'cancel.signal'; provider_contract = $contract; prior_artifacts = @()
    }
    $beforeCanonical = if (Test-Path -LiteralPath $canonical) { @(Get-ChildItem -LiteralPath $canonical -File -Recurse -Force | ForEach-Object { $_.FullName.Substring($canonical.Length + 1) + ':' + (Get-FileHash $_.FullName -Algorithm SHA256).Hash }) } else { @() }
    $valid = Invoke-NativeProviderProcess -InputText (ConvertTo-NativeProviderObject $input) -WorkingDirectory $root
    Assert-NativeProvider ($valid.code -eq 0) "valid measure exited with $($valid.code): $($valid.stderr)"
    Assert-NativeProvider ([string]::IsNullOrWhiteSpace($valid.stderr)) 'valid measure wrote diagnostics to stderr.'
    $measurement = $valid.stdout | ConvertFrom-Json -Depth 100 -ErrorAction Stop
    Assert-NativeProvider ($measurement.schema_version -eq 1 -and $measurement.contract -ceq 'bsl-flow.native-provider.windows-ps.v1' -and $measurement.operation -ceq 'measure' -and $measurement.task_id -ceq $taskId) 'measure identity is not closed and canonical.'
    Assert-NativeProvider ($measurement.request_valid -eq $true) 'valid request was not accepted by the provider.'
    Assert-NativeProvider (@($measurement.blockers | Where-Object { $_ -like 'BF_BLOCKED: measure requires a trusted execution_profile.' }).Count -eq 1) 'missing execution profile did not remain an explicit measure blocker.'
    $afterCanonical = if (Test-Path -LiteralPath $canonical) { @(Get-ChildItem -LiteralPath $canonical -File -Recurse -Force | ForEach-Object { $_.FullName.Substring($canonical.Length + 1) + ':' + (Get-FileHash $_.FullName -Algorithm SHA256).Hash }) } else { @() }
    Assert-NativeProvider ((Get-BFHash $beforeCanonical) -ceq (Get-BFHash $afterCanonical)) 'measure wrote or changed canonical store bytes.'

    $malformed = Invoke-NativeProviderProcess -InputText '{}' -WorkingDirectory $root
    Assert-NativeProvider ($malformed.code -eq 1 -and $malformed.stdout -eq '' -and $malformed.stderr.Trim() -like 'BF_INVALID: provider_input.*') 'empty object did not return a closed BF_INVALID diagnostic.'
    $duplicate = Invoke-NativeProviderProcess -InputText '{"schema_version":1,"schema_version":1}' -WorkingDirectory $root
    Assert-NativeProvider ($duplicate.code -eq 1 -and $duplicate.stderr.Trim() -like 'BF_INVALID:*') 'duplicate JSON keys were accepted by the provider wire.'
    $trailing = Invoke-NativeProviderProcess -InputText '{"schema_version":1} {"schema_version":1}' -WorkingDirectory $root
    Assert-NativeProvider ($trailing.code -eq 1 -and $trailing.stderr.Trim() -like 'BF_INVALID:*') 'trailing JSON document was accepted by the provider wire.'

    # A stage-bound measurement is read-only but carries the exact registered
    # attempt shape.  It must validate the binding and preserve the stage when
    # selecting dependency observations, without dispatching a worker.
    $attemptId = [guid]::NewGuid().ToString().ToLowerInvariant()
    $stageView = ConvertFrom-Json -InputObject ($state | ConvertTo-Json -Depth 100) -Depth 100
    # The core can prepare this binding before publishing active_attempt.  The
    # provider may validate it read-only; if the view already carries an active
    # attempt, the provider requires the same exact ID.
    $stageView.status = 'ready'; $stageView.stage = 'inspect'; $stageView.active_attempt = $null
    $stageView.worker_path = $project; $stageView.attempts = @()
    $attempt = [ordered]@{
        schema_version = 1; task_id = $taskId; attempt_id = $attemptId; stage = 'inspect'; intent_revision = 1; authorization_revision = 1
        dependencies = [ordered]@{}; source_manifest = [ordered]@{}; worker_path = $project; executable = (Join-Path $PSHOME 'pwsh.exe')
        requested_models = $request.models; started_at = [DateTime]::UtcNow.ToString('o'); operation_id = $attemptId
    }
    $stageInput = ConvertFrom-Json -InputObject ($input | ConvertTo-Json -Depth 100) -Depth 100
    $stageInput.state_view = $stageView; $stageInput.attempt = $attempt
    $stageMeasure = Invoke-NativeProviderProcess -InputText (ConvertTo-NativeProviderObject $stageInput) -WorkingDirectory $root
    Assert-NativeProvider ($stageMeasure.code -eq 0) "stage-bound measure exited with $($stageMeasure.code): $($stageMeasure.stderr)"
    $stageObservation = $stageMeasure.stdout | ConvertFrom-Json -Depth 100 -ErrorAction Stop
    Assert-NativeProvider ($stageObservation.operation -ceq 'measure' -and $stageObservation.task_id -ceq $taskId) 'stage-bound measure did not return a measure observation.'
    Assert-NativeProvider (@($stageObservation.blockers | Where-Object { $_ -like 'BF_INVALID: unsupported provider stage.*' }).Count -eq 0) 'stage-bound measure rejected a supported stage.'

    $summary = [ordered]@{ schema_version = 1; status = 'PASS'; checks = $script:checks; entrypoint = $entry; task_id = $taskId; model_calls = 0; runtime_1c = 'not_run' }
    $summary | ConvertTo-Json -Depth 8
}
finally {
    $resolved = [IO.Path]::GetFullPath($root)
    $workRoot = [IO.Path]::GetFullPath((Join-Path $PackageRoot 'work')).TrimEnd('\', '/')
    if (-not $resolved.StartsWith($workRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { throw "Unsafe native provider cleanup target: $resolved" }
    if (Test-Path -LiteralPath $resolved) { Remove-Item -LiteralPath $resolved -Recurse -Force }
}
