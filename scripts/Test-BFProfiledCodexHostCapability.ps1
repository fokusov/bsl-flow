#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$core = Join-Path $PackageRoot 'global\skills\1c-task'
foreach ($file in @(
    'scripts\Task.Storage.ps1',
    'scripts\Task.Contracts.ps1',
    'scripts\Task.Engine.ps1',
    'scripts\Task.ManagedReview.ps1',
    'adapters\Codex.ps1',
    'adapters\ProfiledCodex.ps1'
)) { . (Join-Path $core $file) }

$passed = 0
function Assert-HostCapability([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "FAIL: $Message" }
    $script:passed++
}
function Get-HostFailure([scriptblock]$Action) {
    try { & $Action | Out-Null; return '' } catch { return [string]$_.Exception.Message }
}

$root = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-host-capability-' + [guid]::NewGuid().ToString('N'))
$project = Join-Path $root 'project'
$worker = Join-Path $project 'worker'
$toolset = Join-Path $root 'toolset'
$private = Join-Path $root 'private'
$codexHome = Join-Path $root 'codex-home'
$fakeExecutable = Join-Path $root 'codex.exe'
$taskId = [guid]::NewGuid().ToString()
$nativeLunaSha256 = 'be96b992178b1e467c225800da0d65f2c86d5eba1ef0b14632f65db381cbdfde'
$oldCodexHome = $env:CODEX_HOME
$oldSessionId = $env:CODEX_SESSION_ID

try {
    foreach ($directory in @($project, $worker, $toolset, $private, $codexHome)) {
        [void][IO.Directory]::CreateDirectory($directory)
    }
    $null = & git -c core.hooksPath=NUL -c core.fsmonitor=false -C $project init --quiet
    if ($LASTEXITCODE -ne 0) { throw 'FAIL: temporary host-capability project Git initialization failed.' }

    # A real toolset snapshot is cheap to construct and keeps dependency hashing
    # on the production path. Only native process and RPC boundaries are stubbed.
    $skillDirectory = Join-Path $toolset 'fixture-skill'
    [void][IO.Directory]::CreateDirectory($skillDirectory)
    $skillPath = Join-Path $skillDirectory 'SKILL.md'
    [IO.File]::WriteAllText($skillPath, 'fixture host capability skill')
    $skillSha256 = (Get-FileHash -LiteralPath $skillPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $skillFiles = @([ordered]@{ path = 'SKILL.md'; sha256 = $skillSha256 })
    $skill = [ordered]@{ name = 'fixture-skill'; files = $skillFiles; sha256 = ''; mcp_references = @() }
    $skill.sha256 = Get-BFToolsetAggregateHash -Skills @([ordered]@{ name = $skill.name; files = $skill.files })
    $toolsetAggregate = Get-BFToolsetAggregateHash -Skills @([ordered]@{ name = $skill.name; files = $skill.files })
    Write-BFJson -Path (Join-Path $toolset 'toolset-manifest.json') -Value ([ordered]@{
        schema_version = 1; toolset_name = 'cc-1c-skills'
        source = [ordered]@{ identity = 'local-private'; path = $toolset }
        skills = @($skill); aggregate_sha256 = $toolsetAggregate
    })

    [IO.File]::WriteAllText($fakeExecutable, 'fixture native executable')
    $fakeExecutableBaseline = (Get-FileHash -LiteralPath $fakeExecutable -Algorithm SHA256).Hash.ToLowerInvariant()
    $runtimeExecutable = Join-Path $root 'python.exe'
    [IO.File]::WriteAllText($runtimeExecutable, 'fixture pinned runtime')
    $runtimeSha256 = (Get-FileHash -LiteralPath $runtimeExecutable -Algorithm SHA256).Hash.ToLowerInvariant()
    Write-BFJson -Path (Join-Path $codexHome 'models_cache.json') -Value ([ordered]@{
        models = @(
            [ordered]@{ slug = 'gpt-5.6-luna'; use_responses_lite = $true; shell_type = 'unified_exec'; apply_patch_tool_type = 'freeform'; tool_mode = 'code_mode_only'; experimental_supported_tools = @(); base_instructions = 'fixture' },
            [ordered]@{ slug = 'gpt-6-astra'; use_responses_lite = $true; shell_type = 'unified_exec'; apply_patch_tool_type = 'freeform'; tool_mode = 'code_mode_only'; experimental_supported_tools = @('send_user_message_async','clock'); multi_agent_version = 'v2'; multi_agent_reasoning_effort = 'xhigh'; supported_reasoning_levels = @([ordered]@{ effort = 'low' },[ordered]@{ effort = 'medium' },[ordered]@{ effort = 'high' },[ordered]@{ effort = 'xhigh' },[ordered]@{ effort = 'max' },[ordered]@{ effort = 'ultra' }); web_search_tool_type = 'native'; supports_search_tool = $true; node_repl_disabled = $false; node_repl_auto_review_required = $true; include_skills_usage_instructions = $true; include_plugin_usage_instructions = $true; include_apps_usage_instructions = $true; base_instructions = 'fixture' }
        )
    })

    $profile = [pscustomobject]@{
        provider = 'codex'; executable = $fakeExecutable; executable_sha256 = $nativeLunaSha256
        sandbox = [pscustomobject]@{ executable = $fakeExecutable; sha256 = $nativeLunaSha256 }
        toolset = [pscustomobject]@{ name = 'cc-1c-skills'; root = $toolset; sha256 = $toolsetAggregate }
        denied_read_roots = @($private); codex_skills_sha256 = $null
        runtime = [pscustomobject]@{ executable = $runtimeExecutable; sha256 = $runtimeSha256; version = '3.12.14'; packages = @([pscustomobject]@{ name = 'lxml'; version = '6.1.1' }) }
    }

    $sessionId = [guid]::NewGuid().ToString()
    $sessionDirectory = Join-Path $codexHome ('sessions\{0:D4}\{1:D2}\{2:D2}' -f [DateTime]::UtcNow.Year, [DateTime]::UtcNow.Month, [DateTime]::UtcNow.Day)
    [void][IO.Directory]::CreateDirectory($sessionDirectory)
    $rolloutPath = Join-Path $sessionDirectory ('rollout-2026-09-12T00-00-00-' + $sessionId + '.jsonl')
    [IO.File]::WriteAllLines($rolloutPath, @(
        (@{ timestamp = '2026-09-12T00:00:00Z'; ordinal = 0; type = 'session_meta'; payload = [ordered]@{ session_id = $sessionId; id = $sessionId; originator = 'codex_exec'; cli_version = '0.154.0' } } | ConvertTo-Json -Depth 10 -Compress),
        (@{ timestamp = '2026-09-12T00:00:01Z'; ordinal = 1; type = 'turn_context'; payload = [ordered]@{ turn_id = 'host-turn'; model = 'gpt-5.6-luna'; effort = 'medium'; approval_policy = 'never' } } | ConvertTo-Json -Depth 10 -Compress)
    ))
    $env:CODEX_HOME = $codexHome
    $env:CODEX_SESSION_ID = $sessionId

    # The profile intentionally carries a native identity hash while the fixture
    # file has different bytes. This wrapper models the trusted file-hash boundary
    # and still exposes mutation on the second replay.
    $realGetFileHash = ${function:Get-BFFileHash}
    $script:hostFakeExecutable = $fakeExecutable
    $script:hostFakeBaseline = $fakeExecutableBaseline
    $script:hostNativeHash = $nativeLunaSha256
    $script:hostRealGetFileHash = $realGetFileHash
    function Get-BFFileHash {
        param([string]$Path)
        $actual = & $script:hostRealGetFileHash $Path
        if ([IO.Path]::GetFullPath($Path) -ieq [IO.Path]::GetFullPath($script:hostFakeExecutable) -and $actual -ceq $script:hostFakeBaseline) {
            return $script:hostNativeHash
        }
        return $actual
    }

    # The host capability producer must take the real dependency/profile path.
    # These two seams model only the external executable and app-server protocol.
    $script:hostProcessCalls = 0
    $script:hostRpcCalls = @()
    function Invoke-BFProcess {
        param($Executable, $Arguments, $WorkingDirectory, $InputText, $OutputDirectory, $TimeoutSeconds, $Cancelled, [switch]$CleanEnvironment, $MaxOutputBytes, $Environment)
        $script:hostProcessCalls++
        [void][IO.Directory]::CreateDirectory($OutputDirectory)
        $stdout = Join-Path $OutputDirectory 'stdout.txt'
        $stderr = Join-Path $OutputDirectory 'stderr.txt'
        [IO.File]::WriteAllText($stderr, '')
        [IO.File]::WriteAllText($stdout, 'codex-cli 0.154.0')
        return [pscustomobject]@{ exit_code = 0; stop_reason = $null; stdout = $stdout; stderr = $stderr; executable = $Executable }
    }
    function Invoke-BFCodexReadOnlyRpc {
        param([string]$Executable, [string[]]$Overrides, [string]$WorkingDirectory, [string]$Directory, [string]$Method, [hashtable]$Params, [scriptblock]$Cancelled, [int]$TimeoutSeconds = 60, [int]$MaxOutputBytes = 16777216, [string[]]$ExpectedMcpServers, [string[]]$EnabledMcpServers = @())
        $script:hostRpcCalls += $Method
        [void][IO.Directory]::CreateDirectory($Directory)
        [IO.File]::WriteAllText((Join-Path $Directory 'stderr.txt'), '')
        if ($Method -ceq 'config/read') {
            return [pscustomobject]@{ config = [pscustomobject]@{ mcp_servers = [pscustomobject]@{} } }
        }
        if ($Method -ceq 'skills/list') {
            if (-not $PSBoundParameters.ContainsKey('ExpectedMcpServers')) { throw 'skills/list was not guarded by the observed MCP names.' }
            if (@($ExpectedMcpServers).Count -ne 0) { throw 'fixture unexpectedly observed a global MCP server.' }
            return [pscustomobject]@{ data = @([pscustomobject]@{ cwd = $WorkingDirectory; errors = @(); skills = @([pscustomobject]@{ name = 'fixture'; path = $skillPath; scope = 'project'; enabled = $true }) }) }
        }
        throw "Unexpected host RPC method: $Method"
    }
    # The workstation's parent profile contains the user's real .codex config;
    # this test models the configuration-file boundary while keeping the proof
    # producer, dependency checks and replay logic real.
    function Assert-BFWorkerConfiguration { param([string]$WorkerPath) }

    $inventoryResponse = [pscustomobject]@{ data = @([pscustomobject]@{ cwd = $worker; errors = @(); skills = @([pscustomobject]@{ name = 'fixture'; path = $skillPath; scope = 'project'; enabled = $true }) }) }
    $profile.codex_skills_sha256 = Get-BFHash (ConvertTo-BFCodexSkillInventory $inventoryResponse $worker)
    $state = [pscustomobject]@{
        project_path = $project; task_id = $taskId; worker_path = $worker
        request = [pscustomobject]@{ execution_profile = $profile; models = [pscustomobject]@{ worker = 'fixture'; reviewer = 'fixture'; reviewer_effort = 'medium' } }
    }
    $evidenceDirectory = Join-Path (Get-BFTaskDirectory $project $taskId) 'managed-council\host-capability'
    $hostRoot = Assert-BFSafePath (Join-Path $project ('.bsl-flow/hosts/' + $taskId + '/' + (Get-BFHash $evidenceDirectory)))

    $capability = Test-BFProfiledCodexHostCapability -State $state -Directory $evidenceDirectory -CodexPath $fakeExecutable
    Assert-HostCapability ($capability.provider -ceq 'current_agent' -and $capability.catalog_source_sha256 -match '^[0-9a-f]{64}$') 'fresh proof contains current-agent and catalog source identity'
    Assert-HostCapability (Test-Path -LiteralPath (Join-Path $evidenceDirectory 'capability.json') -PathType Leaf) 'fresh proof is durable under task evidence'
    Assert-HostCapability ((Test-Path -LiteralPath (Join-Path $hostRoot 'scratch') -PathType Container) -and (Test-Path -LiteralPath (Join-Path $hostRoot 'config') -PathType Container)) 'scratch/config use the dedicated host topology'
    Assert-HostCapability ((@($script:hostRpcCalls) -join ',') -ceq 'config/read,skills/list' -and $script:hostProcessCalls -eq 2) 'fresh proof uses version probes and guarded MCP inventory'

    # Retained proofs must preserve the full current-agent provenance contract;
    # a model/effort match alone must not make a foreign capability reusable.
    $capabilityProofPath = Join-Path $evidenceDirectory 'capability.json'
    $originalCapabilityProof = Read-BFJson $capabilityProofPath
    foreach ($mutation in @(
            @{ name = 'provider'; apply = { param($p) $p.capability.provider = 'foreign_provider' }; expected = 'provider is not current_agent' }
            @{ name = 'fresh_context'; apply = { param($p) $p.capability.fresh_context = $false }; expected = 'not a fresh context' }
            @{ name = 'sealed'; apply = { param($p) $p.capability.sealed = $false }; expected = 'not sealed' }
            @{ name = 'terminal'; apply = { param($p) $p.capability.terminal = $false }; expected = 'not terminal' }
            @{ name = 'source'; apply = { param($p) $p.capability.source = 'foreign_rollout' }; expected = 'source is not the current host rollout' }
    )) {
        $mutated = Read-BFJson $capabilityProofPath
        & $mutation.apply $mutated
        Write-BFJson -Path $capabilityProofPath -Value $mutated -Replace
        $failure = Get-HostFailure { Test-BFProfiledCodexHostCapability -State $state -Directory $evidenceDirectory -CodexPath $fakeExecutable }
        Assert-HostCapability ($failure -match [regex]::Escape($mutation.expected)) ('retained proof mutation rejected: ' + $mutation.name)
        Write-BFJson -Path $capabilityProofPath -Value $originalCapabilityProof -Replace
    }

    [IO.File]::AppendAllText($rolloutPath, [Environment]::NewLine + (@{ timestamp = '2026-09-12T00:01:00Z'; ordinal = 2; type = 'turn_context'; payload = [ordered]@{ turn_id = 'new-host-turn'; model = 'gpt-5.6-luna'; effort = 'medium'; approval_policy = 'never' } } | ConvertTo-Json -Depth 10 -Compress))
    $script:hostProcessCalls = 0
    $script:hostRpcCalls = @()
    $replayed = Test-BFProfiledCodexHostCapability -State $state -Directory $evidenceDirectory -CodexPath $fakeExecutable
    Assert-HostCapability ($replayed.catalog_source_path -ceq $capability.catalog_source_path -and $script:hostProcessCalls -eq 0 -and @($script:hostRpcCalls).Count -eq 0) 'retained proof replays after a same-session same-model host turn without process or RPC dispatch'

    # Cached proofs must retain the profile hash boundary as well as the pinned
    # native hash.  A profile mutation cannot be treated as a harmless replay
    # when the executable bytes happen to be unchanged.
    $profile.executable_sha256 = ('0' * 64)
    Assert-HostCapability ((Get-HostFailure { Test-BFProfiledCodexHostCapability -State $state -Directory $evidenceDirectory -CodexPath $fakeExecutable }) -match 'unsupported executable|registered execution profile') 'mutated executable profile hash was accepted by a retained proof'
    $profile.executable_sha256 = $nativeLunaSha256

    [IO.File]::AppendAllText($fakeExecutable, ' changed')
    Assert-HostCapability ((Get-HostFailure { Test-BFProfiledCodexHostCapability -State $state -Directory $evidenceDirectory -CodexPath $fakeExecutable }) -match 'provider executable changed|bytes changed') 'changed executable blocks retained proof'
    [IO.File]::WriteAllText($fakeExecutable, 'fixture native executable')
    [IO.File]::AppendAllText($capability.catalog_source_path, ' changed')
    Assert-HostCapability ((Get-HostFailure { Test-BFProfiledCodexHostCapability -State $state -Directory $evidenceDirectory -CodexPath $fakeExecutable }) -match 'critic catalog source bytes changed') 'changed catalog source blocks retained proof'

    # The same host proof path must also accept the observed Astra model after
    # its exact cache signature is normalized to the empty sealed catalog.  This
    # is still an offline fixture: executable, version and RPC boundaries above
    # remain deterministic fakes and no model request is issued.
    $astraSessionId = [guid]::NewGuid().ToString()
    $astraRolloutPath = Join-Path $sessionDirectory ('rollout-2026-09-12T00-00-00-' + $astraSessionId + '.jsonl')
    [IO.File]::WriteAllLines($astraRolloutPath, @(
        (@{ timestamp = '2026-09-12T00:02:00Z'; ordinal = 0; type = 'session_meta'; payload = [ordered]@{ session_id = $astraSessionId; id = $astraSessionId; originator = 'codex_exec'; cli_version = '0.154.0' } } | ConvertTo-Json -Depth 10 -Compress),
        (@{ timestamp = '2026-09-12T00:02:01Z'; ordinal = 1; type = 'turn_context'; payload = [ordered]@{ turn_id = 'astra-host-turn'; model = 'gpt-6-astra'; effort = 'high'; approval_policy = 'never' } } | ConvertTo-Json -Depth 10 -Compress)
    ))
    $env:CODEX_SESSION_ID = $astraSessionId
    $astraState = [pscustomobject]@{
        project_path = $project; task_id = $taskId; worker_path = $worker
        request = [pscustomobject]@{ execution_profile = $profile; models = [pscustomobject]@{ worker = 'fixture'; reviewer = 'gpt-6-astra'; reviewer_effort = 'high' } }
    }
    $astraEvidenceDirectory = Join-Path (Get-BFTaskDirectory $project $taskId) 'managed-council\host-capability-astra'
    $astraCapability = Test-BFProfiledCodexHostCapability -State $astraState -Directory $astraEvidenceDirectory -CodexPath $fakeExecutable
    Assert-HostCapability ($astraCapability.provider -ceq 'current_agent' -and $astraCapability.model -ceq 'gpt-6-astra' -and $astraCapability.effort -ceq 'high' -and $astraCapability.capability_version -ceq 'codex-0.154.0-gpt-6-astra-direct-empty-tools-v1') 'Astra current-agent capability did not bind the observed model and effort'
    $astraCatalogPath = Join-Path (Split-Path $astraCapability.catalog_source_path -Parent) 'critic-catalog.json'
    $astraCatalog = Read-BFJson $astraCatalogPath
    Assert-HostCapability ($astraCatalog.models[0].experimental_supported_tools.Count -eq 0 -and $null -eq $astraCatalog.models[0].multi_agent_version -and $astraCatalog.models[0].tool_mode -ceq 'direct') 'Astra host proof retained a non-empty sealed tool catalog'

    "HOST_CAPABILITY_OK checks=$passed; process/RPC boundaries stubbed=$true"
}
finally {
    if ($null -eq $oldCodexHome) { Remove-Item Env:CODEX_HOME -ErrorAction SilentlyContinue } else { $env:CODEX_HOME = $oldCodexHome }
    if ($null -eq $oldSessionId) { Remove-Item Env:CODEX_SESSION_ID -ErrorAction SilentlyContinue } else { $env:CODEX_SESSION_ID = $oldSessionId }
    if (Test-Path -LiteralPath $root) {
        $resolved = [IO.Path]::GetFullPath($root)
        if (-not $resolved.StartsWith([IO.Path]::GetFullPath([IO.Path]::GetTempPath()), [StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe test cleanup target.' }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
