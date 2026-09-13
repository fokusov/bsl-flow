#Requires -Version 7.0
[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:BFNativeProviderInputLimit = 16MB
$script:BFNativeProviderContract = 'bsl-flow.native-provider.windows-ps.v1'

function Read-BFNativeProviderInputBytes {
    $stream = [Console]::OpenStandardInput()
    $buffer = [byte[]]::new(65536)
    $memory = [IO.MemoryStream]::new()
    try {
        while ($true) {
            $read = $stream.Read($buffer, 0, $buffer.Length)
            if ($read -le 0) { break }
            if (($memory.Length + $read) -gt $script:BFNativeProviderInputLimit) {
                throw 'BF_INVALID: provider input exceeds the 16 MiB limit.'
            }
            $memory.Write($buffer, 0, $read)
        }
        return $memory.ToArray()
    }
    finally {
        $memory.Dispose()
        $stream.Dispose()
    }
}

function ConvertFrom-BFNativeProviderInput {
    param([byte[]]$Bytes)
    if ($null -eq $Bytes -or $Bytes.Length -eq 0) { throw 'BF_INVALID: provider input is empty.' }
    $encoding = [Text.UTF8Encoding]::new($false, $true)
    try { $text = $encoding.GetString($Bytes) }
    catch { throw 'BF_INVALID: provider input is not valid UTF-8.' }
    if ([string]::IsNullOrWhiteSpace($text)) { throw 'BF_INVALID: provider input is empty.' }
    try {
        # Task.Storage's scanner rejects duplicate keys (including case-only
        # duplicates), trailing data and malformed UTF-8-compatible JSON before
        # ConvertFrom-Json materializes a potentially ambiguous object.
        if ((Test-BFJsonSyntax $text) -cne 'object') { throw 'top-level JSON value must be an object' }
        $convertCommand = Get-Command ConvertFrom-Json -ErrorAction Stop
        if ($convertCommand.Parameters.ContainsKey('DateKind')) {
            # Keep ISO timestamps as strings, matching Read-BFJson.  Provider
            # input is part of the signed controller view, so materializing a
            # date as DateTime would silently change its canonical hash and
            # weaken the input binding.
            $value = ConvertFrom-Json -InputObject $text -DateKind String -Depth 100 -ErrorAction Stop
        }
        else {
            $value = ConvertFrom-Json -InputObject $text -Depth 100 -ErrorAction Stop
        }
    }
    catch { throw 'BF_INVALID: provider input is not one valid JSON document.' }
    if ($null -eq $value -or $value -is [array] -or ($value -isnot [pscustomobject] -and $value -isnot [System.Collections.IDictionary])) {
        throw 'BF_INVALID: provider input must be a JSON object.'
    }
    return $value
}

function Import-BFNativeProviderCore {
    $scriptsRoot = $PSScriptRoot
    # Keep this list explicit: the provider receives pure contracts, gates,
    # process helpers and stage calculation only. It never imports Task.Engine
    # or the legacy Invoke-BSLFlowTask command surface.
    foreach ($name in @('Task.Storage.ps1', 'Task.Contracts.ps1', 'Task.Memory.ps1', 'Task.Architecture.ps1', 'Task.Gates.ps1', 'Task.Process.ps1', 'Task.Execution.ps1', 'Task.Stages.ps1', 'Task.ManagedReview.ps1', 'Task.Provider.ps1')) {
        . (Join-Path $scriptsRoot $name)
    }
}

function Import-BFNativeProviderAdapter {
    param([Parameter(Mandatory = $true)]$ProviderInput)
    $profile = Get-BFValue $ProviderInput.state_view.request 'execution_profile'
    if ($null -eq $profile) { return }
    $adapterRoot = Split-Path $PSScriptRoot -Parent
    if ($profile.provider -ceq 'opencode') { . (Join-Path $adapterRoot 'adapters/OpenCode.ps1') }
    else {
        # ProfiledCodex reuses the trusted worker-configuration guard from the
        # base Codex adapter. Load that dependency explicitly before the
        # profile-specific adapter; relying on the normal controller bootstrap
        # leaves a standalone provider process with an incomplete command set.
        . (Join-Path $adapterRoot 'adapters/Codex.ps1')
        . (Join-Path $adapterRoot 'adapters/ProfiledCodex.ps1')
    }
}

try {
    # Import trusted parser/contract code before materializing input. This does
    # no provider-side filesystem work; it enables the shared duplicate-key and
    # trailing-data scanner used by ConvertFrom-BFNativeProviderInput.
    # Dot-source the loader invocation itself.  The loader dot-sources each
    # module inside its function body; invoking it normally would leave those
    # definitions in the loader's local scope and the entrypoint would then
    # fail before it could parse stdin.
    . Import-BFNativeProviderCore
    $inputObject = ConvertFrom-BFNativeProviderInput (Read-BFNativeProviderInputBytes)
    Assert-BFProviderInput $inputObject | Out-Null
    . Import-BFNativeProviderAdapter $inputObject
    $result = Invoke-BFNativeProviderRequest -ProviderInput $inputObject
    $json = Get-BFCanonicalJson $result
    $utf8 = [Text.UTF8Encoding]::new($false)
    [Console]::OpenStandardOutput().Write($utf8.GetBytes($json))
    exit 0
}
catch {
    $message = [string]$_.Exception.Message
    if ([string]::IsNullOrWhiteSpace($message)) { $message = 'BF_BLOCKED: native provider failed.' }
    [Console]::Error.WriteLine($message)
    exit 1
}
