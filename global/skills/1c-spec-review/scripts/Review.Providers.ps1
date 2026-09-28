#Requires -Version 7.0
Set-StrictMode -Version Latest

# Single-reviewer provider abstraction (Ф4.3 of docs/plans/2026-09-26-remediation-plan.md).
# Invoke-1CSpecReview.ps1 keeps the common part: input snapshot/hashes, bounded
# timeout/output, schema validation, metric recomputation, atomic review.json
# write. This file only obtains a raw review payload (plus provenance) from the
# configured provider. Every provider function returns either a success object
# {Failed=$false; RawReview; Provider; Agent; RequestedModel; ObservedModel;
# Usage; Isolation; OutputDrained} or a failure object produced by
# New-BSLFlowProviderFailure {Failed=$true; FailurePhase; FailureMessage;
# ExitCode; OutputDrained}. Callers never need to distinguish provider-specific
# exceptions: the phase/message pair is enough to keep diagnostic.json truthful.

function New-BSLFlowProviderFailure {
    param(
        [Parameter(Mandatory)][string]$Phase,
        [Parameter(Mandatory)][string]$Message,
        [int]$ExitCode = -1,
        [bool]$OutputDrained = $true
    )
    return [pscustomobject][ordered]@{
        Failed = $true; FailurePhase = $Phase; FailureMessage = $Message
        ExitCode = $ExitCode; OutputDrained = $OutputDrained
    }
}

function New-BSLFlowProviderSuccess {
    param(
        [Parameter(Mandatory)]$RawReview,
        [Parameter(Mandatory)][string]$Provider,
        [string]$Agent = 'n/a',
        [Parameter(Mandatory)][string]$RequestedModel,
        [AllowNull()][string]$ObservedModel,
        [AllowNull()]$Usage,
        [Parameter(Mandatory)][string]$Isolation,
        [bool]$OutputDrained = $true
    )
    return [pscustomobject][ordered]@{
        Failed = $false; RawReview = $RawReview; Provider = $Provider; Agent = $Agent
        RequestedModel = $RequestedModel; ObservedModel = $ObservedModel; Usage = $Usage
        Isolation = $Isolation; OutputDrained = $OutputDrained
    }
}

function Test-BSLFlowCliProviderCapability {
    param(
        [Parameter(Mandatory)][string]$CommandName,
        [Parameter(Mandatory)][string]$CommandPath,
        [Parameter(Mandatory)][string[]]$RequiredHelpFlags
    )
    $previousErrorActionPreference = $ErrorActionPreference
    $versionOutput = $null
    $helpOutput = $null
    try {
        $ErrorActionPreference = 'Continue'
        $versionOutput = (& $CommandPath '--version' 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) { throw "BF_BLOCKED: $CommandName capability not demonstrated: --version" }
        $helpOutput = (& $CommandPath '--help' 2>&1 | Out-String)
        if ($LASTEXITCODE -ne 0) { throw "BF_BLOCKED: $CommandName capability not demonstrated: --help" }
    }
    catch { throw "BF_BLOCKED: $CommandName capability not demonstrated: $($_.Exception.Message)" }
    finally { $ErrorActionPreference = $previousErrorActionPreference }
    foreach ($flag in $RequiredHelpFlags) {
        if ($helpOutput -notmatch [regex]::Escape($flag)) {
            throw "BF_BLOCKED: $CommandName capability not demonstrated: $flag"
        }
    }
    return [pscustomobject]@{ Version = $versionOutput.Trim(); Help = $helpOutput }
}

function Invoke-BSLFlowBoundedProcess {
    # A single-shot (non-interactive) process run with stdin piped in and a
    # bounded wall-clock timeout. Every reviewer CLI provider is request/response,
    # so no incremental byte-limited streaming loop is needed; the full captured
    # output is checked against MaxOutputBytes once the process exits.
    param(
        [Parameter(Mandatory)][string]$CommandPath,
        [Parameter(Mandatory)][string[]]$Arguments,
        [Parameter(Mandatory)][string]$WorkingDirectory,
        [Parameter(Mandatory)][string]$StdinText,
        [Parameter(Mandatory)][int]$TimeoutSeconds,
        [Parameter(Mandatory)][int]$MaxOutputBytes,
        [hashtable]$EnvironmentOverrides = @{}
    )
    $writerEncoding = [System.Text.UTF8Encoding]::new($false)
    $startInfo = New-Object System.Diagnostics.ProcessStartInfo
    $startInfo.FileName = $CommandPath
    $startInfo.WorkingDirectory = $WorkingDirectory
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.RedirectStandardInput = $true
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $startInfo.StandardInputEncoding = $writerEncoding
    $startInfo.StandardOutputEncoding = $writerEncoding
    $startInfo.StandardErrorEncoding = $writerEncoding
    foreach ($argument in $Arguments) { $startInfo.ArgumentList.Add($argument) }
    foreach ($key in $EnvironmentOverrides.Keys) { $startInfo.Environment[$key] = [string]$EnvironmentOverrides[$key] }
    $process = New-Object System.Diagnostics.Process
    $process.StartInfo = $startInfo
    $stdout = [System.Text.StringBuilder]::new()
    $stderr = [System.Text.StringBuilder]::new()
    try {
        if (-not $process.Start()) { return New-BSLFlowProviderFailure -Phase 'launching' -Message "$CommandPath could not be started." }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $inputBytes = $writerEncoding.GetBytes($StdinText)
        try { $process.StandardInput.BaseStream.Write($inputBytes, 0, $inputBytes.Length) } catch { }
        try { $process.StandardInput.Close() } catch { }
        $exited = $process.WaitForExit($TimeoutSeconds * 1000)
        if (-not $exited) {
            try { $process.Kill() } catch { }
            $process.WaitForExit(2000) | Out-Null
            return New-BSLFlowProviderFailure -Phase 'timeout' -Message "$CommandPath exceeded timeout of $TimeoutSeconds seconds."
        }
        $drained = [System.Threading.Tasks.Task]::WaitAll(@($stdoutTask, $stderrTask), 5000)
        if (-not $drained) {
            return New-BSLFlowProviderFailure -Phase 'output_drain' -Message "$CommandPath output could not be drained within the bounded shutdown window." -OutputDrained $false
        }
        $stdout.Append($stdoutTask.Result) | Out-Null
        $stderr.Append($stderrTask.Result) | Out-Null
        $stdoutText = $stdout.ToString()
        $stderrText = $stderr.ToString()
        if ($writerEncoding.GetByteCount($stdoutText) -gt $MaxOutputBytes) {
            return New-BSLFlowProviderFailure -Phase 'output_limit' -Message "$CommandPath output exceeded $MaxOutputBytes bytes."
        }
        return [pscustomobject][ordered]@{
            Failed = $false; ExitCode = $process.ExitCode; Stdout = $stdoutText; Stderr = $stderrText
        }
    }
    finally { $process.Dispose() }
}

function Invoke-BSLFlowOpenCodeSingleReview {
    param(
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)][string]$ContextEnvelope,
        [Parameter(Mandatory)][string]$Agent,
        [Parameter(Mandatory)][string]$Model,
        [Parameter(Mandatory)][string]$Variant,
        [Parameter(Mandatory)][int]$TimeoutSeconds,
        [Parameter(Mandatory)][int]$MaxOutputBytes,
        [string]$OpenCodePath,
        [Parameter(Mandatory)][string]$ReviewerConfigPath,
        [Parameter(Mandatory)][string]$EventsPath,
        [Parameter(Mandatory)][string]$RawResponsePath,
        [Parameter(Mandatory)][string]$StderrPath
    )
    if ($Model -notmatch '^[A-Za-z0-9._-]+/[A-Za-z0-9._:#-]+$') { throw "Unsafe or invalid reviewer model id: $Model" }
    if ($Variant -notmatch '^[A-Za-z0-9._-]+$') { throw "Unsafe or invalid reviewer variant: $Variant" }

    $providerPath = $null
    if ($OpenCodePath) {
        $providerPath = [System.IO.Path]::GetFullPath($OpenCodePath)
        if (-not (Test-Path -LiteralPath $providerPath -PathType Leaf)) { throw "OpenCode executable not found: $providerPath" }
    }
    else {
        $opencode = Get-Command opencode -ErrorAction SilentlyContinue
        if (-not $opencode) { throw 'OpenCode CLI is required for this review route.' }
        $providerPath = $opencode.Source
    }

    $writerEncoding = [System.Text.UTF8Encoding]::new($false)
    $eventWriter = New-Object System.IO.StreamWriter($EventsPath, $false, $writerEncoding)
    $rawWriter = New-Object System.IO.StreamWriter($RawResponsePath, $false, $writerEncoding)
    $stderrWriter = New-Object System.IO.StreamWriter($StderrPath, $false, $writerEncoding)
    $state = @{ Bytes = 0; OutputLimitExceeded = $false; DrainIncomplete = $false; ProcessStillRunning = $false; SyncRoot = New-Object object }
    function Write-BSLFlowOpenCodeProviderLine {
        param([Parameter(Mandatory)][string]$Line, [Parameter(Mandatory)][ValidateSet('stdout','stderr')][string]$Stream)
        [Threading.Monitor]::Enter($state.SyncRoot)
        try {
            $lineBytes = $writerEncoding.GetByteCount($Line + "`n")
            if (($state.Bytes + $lineBytes) -gt $MaxOutputBytes) { $state.OutputLimitExceeded = $true; return }
            $state.Bytes += $lineBytes
            if ($Stream -eq 'stdout') {
                $eventWriter.WriteLine($Line); $eventWriter.Flush()
                $rawWriter.WriteLine($Line); $rawWriter.Flush()
            }
            else { $stderrWriter.WriteLine($Line); $stderrWriter.Flush() }
        }
        finally { [Threading.Monitor]::Exit($state.SyncRoot) }
    }

    $process = $null
    $exitCode = -1
    $failurePhase = 'launching'
    $failureMessage = $null
    $oldConfig = $env:OPENCODE_CONFIG
    $oldDisableProject = $env:OPENCODE_DISABLE_PROJECT_CONFIG
    $oldDisableClaude = $env:OPENCODE_DISABLE_CLAUDE_CODE
    try {
        $startInfo = New-Object System.Diagnostics.ProcessStartInfo
        $startInfo.FileName = $providerPath
        $startInfo.UseShellExecute = $false
        $startInfo.CreateNoWindow = $true
        $startInfo.RedirectStandardInput = $true
        $startInfo.RedirectStandardOutput = $true
        $startInfo.RedirectStandardError = $true
        $startInfo.StandardInputEncoding = $writerEncoding
        $startInfo.StandardOutputEncoding = $writerEncoding
        $startInfo.StandardErrorEncoding = $writerEncoding
        $arguments = @('run', '--pure', '--agent', $Agent, '--model', $Model, '--variant', $Variant, '--format', 'json', '--dir', $ProjectRoot, 'Review the delimited specification context from stdin. Return only the contracted JSON object.')
        foreach ($argument in $arguments) { $startInfo.ArgumentList.Add($argument) }
        $env:OPENCODE_CONFIG = $ReviewerConfigPath
        $env:OPENCODE_DISABLE_PROJECT_CONFIG = '1'
        $env:OPENCODE_DISABLE_CLAUDE_CODE = '1'
        $process = New-Object System.Diagnostics.Process
        $process.StartInfo = $startInfo
        if (-not $process.Start()) { throw 'OpenCode process could not be started.' }
        $inputBytes = $writerEncoding.GetBytes($ContextEnvelope)
        $inputTask = $process.StandardInput.BaseStream.WriteAsync($inputBytes, 0, $inputBytes.Length)
        $stdinClosed = $false
        $stdoutDone = $false; $stderrDone = $false
        $stdoutRead = $process.StandardOutput.ReadLineAsync()
        $stderrRead = $process.StandardError.ReadLineAsync()
        $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
        while (-not $process.HasExited -and [DateTime]::UtcNow -lt $deadline) {
            if (-not $stdinClosed -and $inputTask.IsCompleted) {
                try { [void]$inputTask.GetAwaiter().GetResult() }
                catch {
                    $failurePhase = 'input'
                    $failureMessage = "OpenCode stdin write failed: $($_.Exception.Message)"
                }
                $process.StandardInput.Close(); $stdinClosed = $true
                if ($failureMessage) { break }
            }
            while (-not $stdoutDone -and $stdoutRead.IsCompleted) {
                $line = $stdoutRead.Result
                if ($null -eq $line) { $stdoutDone = $true } else { Write-BSLFlowOpenCodeProviderLine -Line ([string]$line) -Stream stdout; $stdoutRead = $process.StandardOutput.ReadLineAsync() }
            }
            while (-not $stderrDone -and $stderrRead.IsCompleted) {
                $line = $stderrRead.Result
                if ($null -eq $line) { $stderrDone = $true } else { Write-BSLFlowOpenCodeProviderLine -Line ([string]$line) -Stream stderr; $stderrRead = $process.StandardError.ReadLineAsync() }
            }
            if ($state.OutputLimitExceeded) {
                $failurePhase = 'output_limit'
                $failureMessage = "OpenCode output exceeded $MaxOutputBytes bytes."
                break
            }
            Start-Sleep -Milliseconds 50
        }
        if (-not $stdinClosed) {
            if ($inputTask.IsCompleted) {
                try { [void]$inputTask.GetAwaiter().GetResult() }
                catch {
                    $failurePhase = 'input'
                    $failureMessage = "OpenCode stdin write failed: $($_.Exception.Message)"
                }
            }
            try { $process.StandardInput.Close() } catch {}
            $stdinClosed = $true
        }
        if (-not $process.HasExited -and -not $failureMessage) {
            $failurePhase = 'timeout'
            $failureMessage = "OpenCode review exceeded timeout of $TimeoutSeconds seconds."
            try { $process.Kill() } catch { $failureMessage += " Process termination failed: $($_.Exception.Message)" }
            if (-not $process.WaitForExit(2000)) { $state.ProcessStillRunning = $true }
        }
        elseif (-not $process.HasExited) {
            try { $process.Kill() } catch { $failureMessage += " Process termination failed: $($_.Exception.Message)" }
            if (-not $process.WaitForExit(2000)) { $state.ProcessStillRunning = $true }
        }
        $drainDeadline = [DateTime]::UtcNow.AddSeconds(2)
        while ((-not $stdoutDone -or -not $stderrDone) -and [DateTime]::UtcNow -lt $drainDeadline) {
            while (-not $stdoutDone -and $stdoutRead.IsCompleted) {
                $line = $stdoutRead.Result
                if ($null -eq $line) { $stdoutDone = $true } else { Write-BSLFlowOpenCodeProviderLine -Line ([string]$line) -Stream stdout; $stdoutRead = $process.StandardOutput.ReadLineAsync() }
            }
            while (-not $stderrDone -and $stderrRead.IsCompleted) {
                $line = $stderrRead.Result
                if ($null -eq $line) { $stderrDone = $true } else { Write-BSLFlowOpenCodeProviderLine -Line ([string]$line) -Stream stderr; $stderrRead = $process.StandardError.ReadLineAsync() }
            }
            if (-not $stdoutDone -or -not $stderrDone) { Start-Sleep -Milliseconds 10 }
        }
        if (-not $stdoutDone -or -not $stderrDone) { $state.DrainIncomplete = $true }
        $exitCode = $process.ExitCode
        if (-not $failureMessage -and $exitCode -ne 0) {
            $failurePhase = 'provider_failed'
            $failureMessage = "OpenCode review failed with exit code $exitCode."
        }
        if (-not $failureMessage -and $state.OutputLimitExceeded) {
            $failurePhase = 'output_limit'
            $failureMessage = "OpenCode output exceeded $MaxOutputBytes bytes."
        }
        if (-not $failureMessage -and ($state.ProcessStillRunning -or $state.DrainIncomplete)) {
            $failurePhase = 'output_drain'
            $failureMessage = 'OpenCode output could not be drained within the bounded shutdown window.'
        }
        if ($failureMessage) { return New-BSLFlowProviderFailure -Phase $failurePhase -Message $failureMessage -ExitCode $exitCode -OutputDrained (-not $state.DrainIncomplete) }
        $eventWriter.Flush()
        $eventWriter.Dispose()
        $failurePhase = 'parsing'
        $events = @([System.IO.File]::ReadAllLines($EventsPath, $writerEncoding))
        $rawReview = Get-BSLFlowJsonFromOpenCodeEvents -Lines $events
        return New-BSLFlowProviderSuccess -RawReview $rawReview -Provider 'opencode' -Agent $Agent -RequestedModel $Model -ObservedModel $null -Usage $null -Isolation 'permission_rules' -OutputDrained (-not $state.DrainIncomplete)
    }
    catch {
        return New-BSLFlowProviderFailure -Phase $failurePhase -Message $_.Exception.Message -ExitCode $exitCode -OutputDrained (-not $state.DrainIncomplete)
    }
    finally {
        if ($null -ne $process) { $process.Dispose() }
        try { $eventWriter.Dispose() } catch {}
        $rawWriter.Dispose(); $stderrWriter.Dispose()
        if ($null -eq $oldConfig) { Remove-Item Env:OPENCODE_CONFIG -ErrorAction SilentlyContinue } else { $env:OPENCODE_CONFIG = $oldConfig }
        if ($null -eq $oldDisableProject) { Remove-Item Env:OPENCODE_DISABLE_PROJECT_CONFIG -ErrorAction SilentlyContinue } else { $env:OPENCODE_DISABLE_PROJECT_CONFIG = $oldDisableProject }
        if ($null -eq $oldDisableClaude) { Remove-Item Env:OPENCODE_DISABLE_CLAUDE_CODE -ErrorAction SilentlyContinue } else { $env:OPENCODE_DISABLE_CLAUDE_CODE = $oldDisableClaude }
    }
}

function Invoke-BSLFlowClaudeCliSingleReview {
    param(
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)][string]$ContextEnvelope,
        [Parameter(Mandatory)][string]$Model,
        [string]$ReadMode = 'read_search',
        [Parameter(Mandatory)][int]$TimeoutSeconds,
        [Parameter(Mandatory)][int]$MaxOutputBytes,
        [Parameter(Mandatory)][string]$RawResponsePath,
        [Parameter(Mandatory)][string]$StderrPath,
        [string]$ClaudeCliPath
    )
    if ($Model -match '/' -or $Model -notmatch '^[A-Za-z0-9._:-]+$') { throw "Unsafe or invalid reviewer model id: $Model" }
    $claudeCommand = if ($ClaudeCliPath) { [System.IO.Path]::GetFullPath($ClaudeCliPath) } else {
        $resolved = Get-Command claude -ErrorAction SilentlyContinue
        if (-not $resolved) { throw 'BF_BLOCKED: claude CLI is required for the claude_cli review route but was not found in PATH.' }
        $resolved.Source
    }
    if (-not (Test-Path -LiteralPath $claudeCommand -PathType Leaf) -and -not (Get-Command $claudeCommand -ErrorAction SilentlyContinue)) {
        throw "claude CLI executable not found: $claudeCommand"
    }
    [void](Test-BSLFlowCliProviderCapability -CommandName 'claude' -CommandPath $claudeCommand -RequiredHelpFlags @(
        '-p, --print', '--output-format', '--model', '--allowedTools', '--disallowedTools', '--strict-mcp-config'
    ))
    $allowedTools = if ($ReadMode -eq 'attached_only') { '' } else { 'Read,Grep,Glob' }
    $arguments = @('-p', '--output-format', 'json', '--model', $Model, '--allowedTools', $allowedTools,
        '--disallowedTools', 'Edit,Write,Bash,WebFetch,WebSearch,Task,NotebookEdit', '--strict-mcp-config')
    $prompt = "Review the delimited specification context below. Return only the contracted JSON object.`n`n$ContextEnvelope"
    $run = Invoke-BSLFlowBoundedProcess -CommandPath $claudeCommand -Arguments $arguments -WorkingDirectory $ProjectRoot `
        -StdinText $prompt -TimeoutSeconds $TimeoutSeconds -MaxOutputBytes $MaxOutputBytes
    if ($run.Failed) { return $run }
    [System.IO.File]::WriteAllText($RawResponsePath, $run.Stdout, [System.Text.UTF8Encoding]::new($false))
    [System.IO.File]::WriteAllText($StderrPath, $run.Stderr, [System.Text.UTF8Encoding]::new($false))
    if ($run.ExitCode -ne 0) { return New-BSLFlowProviderFailure -Phase 'provider_failed' -Message "claude CLI review failed with exit code $($run.ExitCode)." -ExitCode $run.ExitCode }
    $envelope = $null
    try { $envelope = $run.Stdout.Trim() | ConvertFrom-Json -ErrorAction Stop }
    catch { return New-BSLFlowProviderFailure -Phase 'parsing' -Message "claude CLI did not return a JSON envelope: $($_.Exception.Message)" -ExitCode $run.ExitCode }
    if ([bool]$envelope.is_error) {
        return New-BSLFlowProviderFailure -Phase 'provider_failed' -Message "claude CLI reported an error result: $([string]$envelope.result)" -ExitCode $run.ExitCode
    }
    $resultText = [string]$envelope.result
    $rawReview = $null
    try { $rawReview = Get-BSLFlowReviewPayloadFromOpenCodeText -Text $resultText }
    catch { return New-BSLFlowProviderFailure -Phase 'parsing' -Message "claude CLI result was not one JSON object: $($_.Exception.Message)" -ExitCode $run.ExitCode }
    $observedModel = $null
    try { if ($envelope.modelUsage) { $observedModel = [string]@($envelope.modelUsage.PSObject.Properties.Name)[0] } } catch { $observedModel = $null }
    $usage = $null
    try { $usage = $envelope.usage } catch { $usage = $null }
    return New-BSLFlowProviderSuccess -RawReview $rawReview -Provider 'claude_cli' -Agent 'claude-cli' -RequestedModel $Model -ObservedModel $observedModel -Usage $usage -Isolation 'permission_rules'
}

function Invoke-BSLFlowCodexExecSingleReview {
    param(
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)][string]$ContextEnvelope,
        [Parameter(Mandatory)][string]$Model,
        [Parameter(Mandatory)][int]$TimeoutSeconds,
        [Parameter(Mandatory)][int]$MaxOutputBytes,
        [Parameter(Mandatory)][string]$RawResponsePath,
        [Parameter(Mandatory)][string]$StderrPath,
        [Parameter(Mandatory)][string]$ReviewSchemaPath,
        [string]$CodexCliPath
    )
    if ($Model -match '/' -or $Model -notmatch '^[A-Za-z0-9._:-]+$') { throw "Unsafe or invalid reviewer model id: $Model" }
    $codexCommand = if ($CodexCliPath) { [System.IO.Path]::GetFullPath($CodexCliPath) } else {
        $resolved = Get-Command codex -ErrorAction SilentlyContinue
        if (-not $resolved) { throw 'BF_BLOCKED: codex CLI is required for the codex_exec review route but was not found in PATH.' }
        $resolved.Source
    }
    [void](Test-BSLFlowCliProviderCapability -CommandName 'codex' -CommandPath $codexCommand -RequiredHelpFlags @(
        '--sandbox', '--skip-git-repo-check', '--ignore-user-config', '--json', '--output-schema', '--output-last-message', '--model'
    ))
    $lastMessagePath = [System.IO.Path]::ChangeExtension($RawResponsePath, '.last-message.txt')
    $arguments = @('exec', '--sandbox', 'read-only', '--skip-git-repo-check', '--ignore-user-config', '--json',
        '--output-schema', $ReviewSchemaPath, '--output-last-message', $lastMessagePath, '-m', $Model)
    foreach ($feature in @('plugins', 'multi_agent', 'memories', 'shell_snapshot', 'hooks', 'browser_use', 'computer_use', 'in_app_browser', 'skill_mcp_dependency_install')) {
        $arguments += @('--disable', $feature)
    }
    $arguments += '-'
    $prompt = "Review the delimited specification context below. Return only the contracted JSON object.`n`n$ContextEnvelope"
    $run = Invoke-BSLFlowBoundedProcess -CommandPath $codexCommand -Arguments $arguments -WorkingDirectory $ProjectRoot `
        -StdinText $prompt -TimeoutSeconds $TimeoutSeconds -MaxOutputBytes $MaxOutputBytes
    if ($run.Failed) { return $run }
    [System.IO.File]::WriteAllText($RawResponsePath, $run.Stdout, [System.Text.UTF8Encoding]::new($false))
    [System.IO.File]::WriteAllText($StderrPath, $run.Stderr, [System.Text.UTF8Encoding]::new($false))
    if ($run.ExitCode -ne 0) { return New-BSLFlowProviderFailure -Phase 'provider_failed' -Message "codex exec review failed with exit code $($run.ExitCode)." -ExitCode $run.ExitCode }
    if (-not (Test-Path -LiteralPath $lastMessagePath -PathType Leaf)) {
        return New-BSLFlowProviderFailure -Phase 'parsing' -Message 'codex exec did not write an output-last-message file.' -ExitCode $run.ExitCode
    }
    $lastMessage = Get-Content -Raw -LiteralPath $lastMessagePath
    $rawReview = $null
    try { $rawReview = Get-BSLFlowReviewPayloadFromOpenCodeText -Text $lastMessage }
    catch { return New-BSLFlowProviderFailure -Phase 'parsing' -Message "codex exec last message was not one JSON object: $($_.Exception.Message)" -ExitCode $run.ExitCode }
    return New-BSLFlowProviderSuccess -RawReview $rawReview -Provider 'codex_exec' -Agent 'codex-exec' -RequestedModel $Model -ObservedModel $null -Usage $null -Isolation 'os_sandbox'
}

function Invoke-BSLFlowApiSingleReview {
    param(
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)][string]$ContextEnvelope,
        [Parameter(Mandatory)][string]$Model,
        [Parameter(Mandatory)]$CouncilRouting,
        [Parameter(Mandatory)][int]$TimeoutSeconds,
        [Parameter(Mandatory)][int]$MaxOutputBytes,
        [Parameter(Mandatory)][string]$RawResponsePath,
        [Parameter(Mandatory)][string]$AttemptDir,
        # Test-only seam: forwarded to Invoke-BSLFlowCouncilApi exactly as its own
        # -HttpSend parameter (see Test-CouncilTransport.ps1). Production callers
        # never set it, so the real HttpClient transport always dispatches.
        [scriptblock]$HttpSend
    )
    if ($Model -cnotmatch '^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$') { throw "Unsafe or invalid reviewer model profile name: $Model" }
    . (Join-Path $PSScriptRoot 'Review.Api.Config.ps1')
    . (Join-Path $PSScriptRoot 'Review.Api.Transport.ps1')
    $profileEntry = $CouncilRouting.models[$Model]
    if ($null -eq $profileEntry) { throw "BF_BLOCKED: model profile '$Model' is not bound; add llm.models.$Model to bsl-flow.yaml or ~/.bsl-flow/config.yaml." }
    $provider = $CouncilRouting.providers[[string]$profileEntry.provider]
    if ($null -eq $provider) { throw "BF_BLOCKED: llm.models.$Model references an unknown provider." }
    $overlay = Get-BSLFlowLocalProviderOverlay -ProjectPath $ProjectRoot
    $localToken = ''
    $endpoint = [pscustomobject][ordered]@{
        scheme = [string]$provider.endpoint.scheme; host = [string]$provider.endpoint.host
        port = [int]$provider.endpoint.port; base_path = [string]$provider.endpoint.base_path
    }
    if ($overlay.Contains([string]$profileEntry.provider)) {
        $localPath = Join-Path $ProjectRoot '.bsl-flow/providers.local.yaml'
        $localText = Get-Content -Raw -LiteralPath $localPath
        $localToken = Get-BSLFlowYamlValue $localText @('providers', [string]$profileEntry.provider, 'token') ''
        $localBaseUrl = [string]$overlay[[string]$profileEntry.provider].base_url
        if (-not [string]::IsNullOrWhiteSpace($localBaseUrl)) {
            $localEndpoint = Assert-BSLFlowEndpointUrl -Url $localBaseUrl -Name "providers.local.$([string]$profileEntry.provider)" -AllowLocalHttp ([bool]$CouncilRouting.allow_local_http)
            $endpoint = [pscustomobject][ordered]@{
                scheme = [string]$localEndpoint.scheme; host = [string]$localEndpoint.host
                port = [int]$localEndpoint.port; base_path = [string]$localEndpoint.base_path
            }
        }
    }
    $credential = Resolve-BSLFlowCouncilCredential -ProviderName ([string]$profileEntry.provider) -TokenEnv ([string]$provider.token_env) -LocalToken $localToken
    if ($credential.credential_source -eq 'missing') {
        throw "BF_BLOCKED: no credential is configured for provider '$($profileEntry.provider)' (llm.providers.$($profileEntry.provider).token_env, or providers.local.yaml)."
    }
    $binding = [pscustomobject][ordered]@{
        provider = [string]$profileEntry.provider; model = [string]$profileEntry.model; effort = [string]$profileEntry.effort
        protocol = [string]$provider.protocol; endpoint = $endpoint
    }
    $prompt = "You are the single independent spec reviewer for a 1C:Enterprise change. Review the delimited specification context below and return only the contracted JSON object (schema_version, reviewer_verdict, summary, scores, overengineering, findings, do_not_change, confidence).`n`n$ContextEnvelope"
    try {
        $apiCallArgs = @{
            Binding = $binding; PromptText = $prompt; Credential = [string]$credential.token; AttemptDir = $AttemptDir
            TimeoutSeconds = $TimeoutSeconds; MaxInputBytes = [Math]::Max($MaxOutputBytes, 1048576); MaxOutputBytes = $MaxOutputBytes
        }
        if ($HttpSend) { $apiCallArgs.HttpSend = $HttpSend }
        $apiResult = Invoke-BSLFlowCouncilApi @apiCallArgs
    }
    catch {
        return New-BSLFlowProviderFailure -Phase 'provider_failed' -Message $_.Exception.Message
    }
    $attemptRawResponse = Join-Path $AttemptDir 'raw-response.txt'
    if (Test-Path -LiteralPath $attemptRawResponse -PathType Leaf) { Copy-Item -LiteralPath $attemptRawResponse -Destination $RawResponsePath -Force }
    return New-BSLFlowProviderSuccess -RawReview $apiResult.payload -Provider 'api' -Agent 'council-transport' -RequestedModel $Model -ObservedModel $apiResult.observed_model -Usage $apiResult.usage -Isolation 'attached_only'
}

function Invoke-BSLFlowClaudeSubagentSingleReview {
    param(
        [Parameter(Mandatory)][string]$ChangeName,
        [string]$ImportRawPath,
        [string]$AuthorModel
    )
    $blockedMessage = "BF_BLOCKED: run the bsl-flow-spec-reviewer subagent, save its JSON to .bsl-flow/reports/spec-review/$ChangeName.subagent/raw.json, then rerun with -ImportRaw <that path>."
    if ([string]::IsNullOrWhiteSpace($ImportRawPath)) {
        return New-BSLFlowProviderFailure -Phase 'launching' -Message $blockedMessage
    }
    $resolvedImportPath = [System.IO.Path]::GetFullPath($ImportRawPath)
    if (-not (Test-Path -LiteralPath $resolvedImportPath -PathType Leaf)) {
        return New-BSLFlowProviderFailure -Phase 'launching' -Message "BF_BLOCKED: -ImportRaw path does not exist: $resolvedImportPath"
    }
    $rawText = Get-Content -Raw -LiteralPath $resolvedImportPath
    $rawReview = $null
    try { $rawReview = $rawText | ConvertFrom-Json -ErrorAction Stop }
    catch { return New-BSLFlowProviderFailure -Phase 'parsing' -Message "Imported subagent raw review was not valid JSON: $($_.Exception.Message)" }
    $observedModel = $null
    try { if ($rawReview.PSObject.Properties['observed_model']) { $observedModel = [string]$rawReview.observed_model } } catch { $observedModel = $null }
    if (-not $observedModel) {
        # The packaged Claude Code subagent (hosts/claude-code/agents/bsl-flow-spec-reviewer.md)
        # never emits observed_model itself (its output contract is only the raw
        # review fields), but it pins its own model in YAML frontmatter, which is
        # the actual identity independence depends on. Read it best-effort so
        # same_model_as_author is not always null just because the raw JSON is silent.
        try {
            $packageRoot = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)))
            $agentDefinitionPath = Join-Path $packageRoot 'hosts\claude-code\agents\bsl-flow-spec-reviewer.md'
            if (Test-Path -LiteralPath $agentDefinitionPath -PathType Leaf) {
                $agentDefinitionText = Get-Content -Raw -LiteralPath $agentDefinitionPath
                $frontmatterMatch = [regex]::Match($agentDefinitionText, '(?ms)^---\s*\n.*?^model:\s*(?<model>\S+)\s*$.*?^---')
                if ($frontmatterMatch.Success) { $observedModel = $frontmatterMatch.Groups['model'].Value.Trim() }
            }
        }
        catch { $observedModel = $null }
    }
    $requestedModel = if ($observedModel) { $observedModel } else { 'unknown' }
    return New-BSLFlowProviderSuccess -RawReview $rawReview -Provider 'claude_subagent' -Agent 'bsl-flow-spec-reviewer' -RequestedModel $requestedModel -ObservedModel $observedModel -Usage $null -Isolation 'host_subagent'
}

function Invoke-BSLFlowSingleReviewProvider {
    param(
        [Parameter(Mandatory)][ValidateSet('opencode', 'claude_cli', 'codex_exec', 'api', 'claude_subagent')][string]$Provider,
        [Parameter(Mandatory)][string]$ProjectRoot,
        [Parameter(Mandatory)][string]$ChangeName,
        [Parameter(Mandatory)][string]$ContextEnvelope,
        [string]$Agent,
        [string]$Model,
        [string]$Variant,
        [string]$ReadMode = 'read_search',
        [Parameter(Mandatory)][int]$TimeoutSeconds,
        [Parameter(Mandatory)][int]$MaxOutputBytes,
        [string]$OpenCodePath,
        [string]$ReviewerConfigPath,
        [string]$ReviewSchemaPath,
        [string]$ImportRawPath,
        [string]$AuthorModel,
        [string]$ClaudeCliPath,
        [string]$CodexCliPath,
        $CouncilRouting,
        [Parameter(Mandatory)][string]$RunRoot,
        [Parameter(Mandatory)][string]$EventsPath,
        [Parameter(Mandatory)][string]$RawResponsePath,
        [Parameter(Mandatory)][string]$StderrPath
    )
    switch ($Provider) {
        'opencode' {
            return Invoke-BSLFlowOpenCodeSingleReview -ProjectRoot $ProjectRoot -ContextEnvelope $ContextEnvelope -Agent $Agent -Model $Model -Variant $Variant `
                -TimeoutSeconds $TimeoutSeconds -MaxOutputBytes $MaxOutputBytes -OpenCodePath $OpenCodePath -ReviewerConfigPath $ReviewerConfigPath `
                -EventsPath $EventsPath -RawResponsePath $RawResponsePath -StderrPath $StderrPath
        }
        'claude_cli' {
            return Invoke-BSLFlowClaudeCliSingleReview -ProjectRoot $ProjectRoot -ContextEnvelope $ContextEnvelope -Model $Model -ReadMode $ReadMode `
                -TimeoutSeconds $TimeoutSeconds -MaxOutputBytes $MaxOutputBytes -RawResponsePath $RawResponsePath -StderrPath $StderrPath -ClaudeCliPath $ClaudeCliPath
        }
        'codex_exec' {
            return Invoke-BSLFlowCodexExecSingleReview -ProjectRoot $ProjectRoot -ContextEnvelope $ContextEnvelope -Model $Model `
                -TimeoutSeconds $TimeoutSeconds -MaxOutputBytes $MaxOutputBytes -RawResponsePath $RawResponsePath -StderrPath $StderrPath -ReviewSchemaPath $ReviewSchemaPath -CodexCliPath $CodexCliPath
        }
        'api' {
            return Invoke-BSLFlowApiSingleReview -ProjectRoot $ProjectRoot -ContextEnvelope $ContextEnvelope -Model $Model -CouncilRouting $CouncilRouting `
                -TimeoutSeconds $TimeoutSeconds -MaxOutputBytes $MaxOutputBytes -RawResponsePath $RawResponsePath -AttemptDir $RunRoot
        }
        'claude_subagent' {
            return Invoke-BSLFlowClaudeSubagentSingleReview -ChangeName $ChangeName -ImportRawPath $ImportRawPath -AuthorModel $AuthorModel
        }
    }
}
