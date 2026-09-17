#Requires -Version 7.0
Set-StrictMode -Version Latest

function Get-BFManagedCouncilEvidence {
    param($State, [int]$MaxBytes = 262144, [object]$ProviderContext = $null)
    if ($null -ne $ProviderContext) {
        $providerEvidenceCommand = Get-Command Get-BFProviderManagedCouncilEvidence -CommandType Function -ErrorAction SilentlyContinue
        if ($null -eq $providerEvidenceCommand) {
            throw 'BF_BLOCKED: provider council evidence helper is unavailable.'
        }
        $contextRoot = Assert-BFSafePath (Get-BFValue $ProviderContext 'context_root' '')
        return & $providerEvidenceCommand -State $State -ContextRoot $contextRoot -MaxBytes $MaxBytes
    }
    $entries = @((Get-BFValue $State 'evidence' @()) | Where-Object { $_.stage -eq 'inspect' -and $_.outcome -eq 'PASS' })
    $entry = if ($entries.Count -gt 0) { $entries[-1] } else { $null }
    $attemptId = $null; $resultSha256 = $null; $outcome = 'missing'; $dependencies = $null; $rawHashes = @(); $proposal = $null; $missingContext = @()
    if ($null -ne $entry) {
        $taskDirectory = Get-BFTaskDirectory $State.project_path $State.task_id
        $attemptId = [string]$entry.attempt_id
        if ($attemptId -notmatch '^[0-9a-fA-F-]{36}$') { throw 'BF_BLOCKED: inspect evidence has an invalid attempt identity.' }
        $resultPath = Assert-BFSafePath (Join-Path $taskDirectory ('attempts/' + $attemptId + '/result.json'))
        if (-not (Test-Path -LiteralPath $resultPath -PathType Leaf)) { throw 'BF_BLOCKED: verified inspect evidence result is missing.' }
        $result = Read-BFJson $resultPath
        if ((Get-BFHash $result) -cne [string]$entry.result_sha256 -or $result.stage -cne 'inspect' -or $result.outcome -cne 'PASS' -or $result.attempt_id -cne $attemptId -or $result.task_id -cne $State.task_id) {
            throw 'BF_BLOCKED: inspect evidence result no longer matches the controller receipt.'
        }
        $currentDependencies = Get-BFDependencies $State 'inspect' $null
        if ((Get-BFHash $currentDependencies) -cne (Get-BFHash $entry.dependencies) -or (Get-BFHash $currentDependencies) -cne (Get-BFHash $result.dependencies)) {
            throw 'BF_BLOCKED: inspect evidence is stale for the current task inputs.'
        }
        foreach ($raw in @($entry.raw_hashes)) {
            [void](Assert-BFSafePath $raw.path)
            if (-not (Test-Path -LiteralPath $raw.path -PathType Leaf) -or (Get-BFFileHash $raw.path) -cne $raw.sha256) {
                throw 'BF_BLOCKED: inspect evidence raw artifact changed after recording.'
            }
        }
        $resultSha256 = [string]$entry.result_sha256; $outcome = 'PASS'; $dependencies = $entry.dependencies; $rawHashes = @($entry.raw_hashes); $proposal = $result.proposal
    }
    else {
        # Keep the evidence shape explicit when no inspect receipt is available;
        # the council can see the missing evidence and the controller decides the
        # stage gate instead of receiving an empty, unbound string.
        $missingContext = @('verified inspect evidence')
    }
    # Council receives the same bounded, read-only architecture projection that
    # the controller uses for stage dependencies. The inspect dependency check
    # above proves the accepted inspect evidence is still current; this stage has
    # its own bundle identity because subjects differ by stage. The bundle carries
    # selected ADR excerpts and explicit missing_context, while its full identity
    # binds omitted applicable decisions against architecture drift.
    $architectureRoot = Get-BFArchitectureContextRoot ([string]$State.project_path)
    $architecture = Get-BFArchitectureBundle 'spec_review' $architectureRoot
    $bundle = [ordered]@{
        schema_version = 2; source = 'bsl-flow.inspect+architecture'; task_id = [string]$State.task_id
        attempt_id = $attemptId; outcome = $outcome; result_sha256 = $resultSha256
        dependencies = $dependencies; raw_hashes = $rawHashes; proposal = $proposal
        missing_context = $missingContext; architecture = $architecture
    }
    $text = Get-BFCanonicalJson $bundle
    if ([System.Text.UTF8Encoding]::new($false).GetByteCount($text) -gt $MaxBytes) { throw 'BF_BLOCKED: verified inspect evidence exceeds the council input bound.' }
    return $text
}

function Test-BFProfiledCodexHostCapability {
    <#
      Take a durable, content-addressed proof of the exact host contract used by
      current-agent fallback. The request's declared hashes are inputs to this
      check, never its evidence: executable, sandbox, catalog and skill bytes are
      read from the actual host before the capability is returned.
    #>
    param(
        [Parameter(Mandatory)]$State,
        [Parameter(Mandatory)][string]$Directory,
        [string]$CodexPath,
        [scriptblock]$Cancelled,
        [object]$ProviderContext = $null
    )
    $Directory = Assert-BFSafePath $Directory
    $taskRoot = (Assert-BFSafePath (Get-BFTaskDirectory $State.project_path $State.task_id)).TrimEnd('\','/') + '\'
    $artifactRoot = if ($null -ne $ProviderContext) {
        Assert-BFSafePath (Get-BFValue $ProviderContext 'artifact_root' '')
    } else {
        $null
    }
    $allowedRoot = if ($null -ne $artifactRoot) {
        $artifactRoot.TrimEnd('\','/') + '\'
    } else {
        $taskRoot
    }
    if (-not $Directory.StartsWith($allowedRoot, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'BF_INVALID: host capability evidence must be under the selected review evidence root.'
    }
    $profile = Get-BFValue $State.request 'execution_profile'
    if ($null -eq $profile -or [string]$profile.provider -cne 'codex') {
        throw 'BF_BLOCKED: current-agent fallback has no supported sealed Codex profile.'
    }
    $nativeCodexSha256 = 'be96b992178b1e467c225800da0d65f2c86d5eba1ef0b14632f65db381cbdfde'
    $CodexPath = if ([string]::IsNullOrWhiteSpace($CodexPath)) { [string]$profile.executable } else { $CodexPath }
    $CodexPath = Assert-BFSafePath $CodexPath
    if ($CodexPath -cne (Assert-BFSafePath $profile.executable) -or $CodexPath -cne (Assert-BFSafePath $profile.sandbox.executable)) {
        throw 'BF_BLOCKED: current-agent fallback Codex and sandbox paths do not identify one native host.'
    }
    Assert-BFWorkerConfiguration $State.worker_path
    $currentHost = Get-BFCurrentHostModelEffort
    if ([string]::IsNullOrWhiteSpace([string]$currentHost.model) -or [string]::IsNullOrWhiteSpace([string]$currentHost.effort)) {
        throw 'BF_BLOCKED: current-agent fallback requires an exact current-host model/effort receipt.'
    }
    if ([string]::IsNullOrWhiteSpace([string]$currentHost.turn_id) -or
        [string]::IsNullOrWhiteSpace([string]$currentHost.rollout_path)) {
        throw 'BF_BLOCKED: current-agent fallback requires a persisted current-host rollout turn.'
    }
    $modelContract = Get-BFCodexCriticModelContract ([string]$currentHost.model)

    # Controller evidence lives under .bsl-flow/tasks, while the temporary and
    # configuration roots reopened inside the sandbox live in the dedicated host
    # topology.  Reopening a descendant of the controller tree would be rejected
    # by Get-BFExecutionPermissionProfile because that tree is private.
    $hostRoot = if ($null -ne $artifactRoot) {
        Assert-BFSafePath (Join-Path $artifactRoot ('host-capability/' + (Get-BFHash $Directory)))
    } else {
        Assert-BFSafePath (Join-Path $State.project_path ('.bsl-flow/hosts/' + $State.task_id + '/' + (Get-BFHash $Directory)))
    }
    $scratch = Join-Path $hostRoot 'scratch'
    $config = Join-Path $hostRoot 'config'
    $capabilityPath = Join-Path $Directory 'capability.json'
    if (Test-Path -LiteralPath $Directory) {
        if (-not (Test-Path -LiteralPath $capabilityPath -PathType Leaf)) {
            throw 'BF_BLOCKED: partial host capability proof exists; reconcile the preserved attempt without retry.'
        }
        if (-not (Test-Path -LiteralPath $hostRoot -PathType Container) -or
            -not (Test-Path -LiteralPath $scratch -PathType Container) -or
            -not (Test-Path -LiteralPath $config -PathType Container)) {
            throw 'BF_BLOCKED: retained host capability proof has no matching host topology.'
        }
        $proof = Read-BFJson $capabilityPath
        Assert-BFFields $proof @('schema_version','capability','dependencies_sha256','catalog_source_path','catalog_source_sha256','inventory_path') @('current_host') 'host capability proof'
        if ($proof.schema_version -ne 1) { throw 'BF_BLOCKED: unsupported host capability proof version.' }
        $capability = $proof.capability
        Assert-BFFields $capability @('capability_version','provider','model','effort','fresh_context','sealed','terminal','source','executable_sha256','sandbox_sha256','catalog_sha256','skills_sha256','catalog_source_path','catalog_source_sha256') @() 'host capability'
        Assert-BFFields $proof.current_host @('session_id','turn_id','rollout_path') @() 'host capability current_host'
        if ([string]$proof.current_host.session_id -notmatch '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$' -or
            [string]::IsNullOrWhiteSpace([string]$proof.current_host.turn_id)) {
            throw 'BF_BLOCKED: retained host capability current-host identity is invalid.'
        }
        if ([string]$capability.provider -cne 'current_agent') {
            throw 'BF_BLOCKED: retained host capability provider is not current_agent.'
        }
        if ([bool]$capability.fresh_context -ne $true) {
            throw 'BF_BLOCKED: retained host capability is not a fresh context.'
        }
        if ([bool]$capability.sealed -ne $true) {
            throw 'BF_BLOCKED: retained host capability is not sealed.'
        }
        if ([bool]$capability.terminal -ne $true) {
            throw 'BF_BLOCKED: retained host capability is not terminal.'
        }
        if ([string]$capability.source -cne 'current_host_rollout') {
            throw 'BF_BLOCKED: retained host capability source is not the current host rollout.'
        }
        $proofRolloutPath = Assert-BFSafePath ([string]$proof.current_host.rollout_path)
        if (-not (Test-Path -LiteralPath $proofRolloutPath -PathType Leaf)) {
            throw 'BF_BLOCKED: retained host capability rollout evidence is missing.'
        }
        # Validate the saved capability against its exact historical turn. The
        # current host may have appended another turn to the same session while
        # retaining the same model/effort, so latest-turn lookup is not proof of
        # the bytes that were saved with this capability.
        $proofObserved = Get-BFObservedModelEffort -SessionId ([string]$proof.current_host.session_id) -TurnId ([string]$proof.current_host.turn_id)
        if ([string]$proofObserved.rollout_path -cne $proofRolloutPath -or [string]$proofObserved.turn_id -cne [string]$proof.current_host.turn_id -or
            [string]$proofObserved.observed_model -cne [string]$capability.model -or [string]$proofObserved.observed_effort -cne [string]$capability.effort) {
            throw 'BF_BLOCKED: retained host capability current-host identity no longer matches its rollout evidence.'
        }
        if ([string]$capability.catalog_source_path -cne [string]$proof.catalog_source_path -or
            [string]$capability.catalog_source_sha256 -cne [string]$proof.catalog_source_sha256) {
            throw 'BF_BLOCKED: retained host capability catalog source binding differs.'
        }
        if ([string]$currentHost.session_id -cne [string]$proof.current_host.session_id -or
            [string]$currentHost.rollout_path -cne $proofRolloutPath -or
            [string]$capability.model -cne [string]$currentHost.model -or [string]$capability.effort -cne [string]$currentHost.effort) {
            throw 'BF_BLOCKED: retained host capability resolves to another current-agent model/effort.'
        }
        if ([string]$capability.capability_version -cne (Get-BFCodexCriticCapabilityVersion ([string]$capability.model))) {
            throw 'BF_BLOCKED: retained host capability has an unsupported model capability version.'
        }
        Get-BFCodexCriticModelContract ([string]$capability.model) | Out-Null
        if ([string]$profile.executable_sha256 -cne $nativeCodexSha256 -or
            [string]$profile.sandbox.sha256 -notmatch '^[0-9a-f]{64}$' -or
            [string]$capability.executable_sha256 -cne $nativeCodexSha256 -or [string]$capability.sandbox_sha256 -cne [string]$profile.sandbox.sha256) {
            throw 'BF_BLOCKED: retained host capability has an unsupported executable or sandbox identity.'
        }
        $dependencies = Get-BFExecutionDependencies $State
        if ((Get-BFHash $dependencies) -cne [string]$proof.dependencies_sha256) {
            throw 'BF_BLOCKED: retained host capability dependencies changed.'
        }
        if ((Get-BFFileHash $CodexPath) -cne [string]$capability.executable_sha256 -or (Get-BFFileHash $profile.sandbox.executable) -cne [string]$capability.sandbox_sha256) {
            throw 'BF_BLOCKED: retained host capability executable bytes changed.'
        }
        $catalogSourcePath = Assert-BFSafePath ([string]$proof.catalog_source_path)
        if ((Get-BFFileHash $catalogSourcePath) -cne [string]$proof.catalog_source_sha256) {
            throw 'BF_BLOCKED: retained critic catalog source bytes changed.'
        }
        $catalogDirectory = Split-Path $catalogSourcePath -Parent
        $catalog = Get-BFCodexCriticCatalog $catalogDirectory -ExpectedModel ([string]$capability.model) -ExpectedEffort ([string]$capability.effort)
        if ((Get-BFHash $catalog.catalog) -cne [string]$capability.catalog_sha256) {
            throw 'BF_BLOCKED: retained critic catalog identity changed.'
        }
        $inventoryPath = Assert-BFSafePath (Join-Path $Directory ([string]$proof.inventory_path))
        $inventoryRecord = Read-BFJson $inventoryPath
        Assert-BFFields $inventoryRecord @('skills') @() 'host skill inventory'
        $inventory = @($inventoryRecord.skills)
        if ((Get-BFHash $inventory) -cne [string]$capability.skills_sha256 -or (Get-BFHash $inventory) -cne [string]$profile.codex_skills_sha256) {
            throw 'BF_BLOCKED: retained host skill inventory identity changed.'
        }
        foreach ($skill in $inventory) {
            Assert-BFFields $skill @('name','path','scope','enabled','sha256') @() 'host skill inventory entry'
            if ((Get-BFFileHash (Assert-BFSafePath ([string]$skill.path))) -cne [string]$skill.sha256) {
                throw 'BF_BLOCKED: retained host skill instruction bytes changed.'
            }
        }
        return $capability
    }

    if (Test-Path -LiteralPath $hostRoot) {
        throw 'BF_BLOCKED: unregistered host topology exists; reconcile the preserved capability attempt without retry.'
    }
    [void][IO.Directory]::CreateDirectory($Directory)
    $dependencies = Get-BFExecutionDependencies $State
    $executableSha256 = Get-BFFileHash $CodexPath
    $sandboxSha256 = Get-BFFileHash $profile.sandbox.executable
    if ($executableSha256 -cne [string]$profile.executable_sha256 -or $sandboxSha256 -cne [string]$profile.sandbox.sha256) {
        throw 'BF_BLOCKED: actual Codex/sandbox bytes differ from the registered execution profile.'
    }
    if ($executableSha256 -cne $nativeCodexSha256) {
        throw 'BF_BLOCKED: current-agent fallback requires the verified native Codex executable.'
    }
    $providerVersionDirectory = Join-Path $Directory 'provider-version'
    $providerVersion = Invoke-BFProcess -Executable $CodexPath -Arguments @('--version') -WorkingDirectory $State.worker_path -InputText '' -OutputDirectory $providerVersionDirectory -TimeoutSeconds 60 -Cancelled $Cancelled -CleanEnvironment -MaxOutputBytes 65536
    if ($providerVersion.stop_reason -or $providerVersion.exit_code -ne 0 -or [IO.File]::ReadAllText($providerVersion.stdout).Trim() -cne 'codex-cli 0.154.0') {
        throw 'BF_BLOCKED: current-agent fallback provider version is not the verified Codex 0.154.0.'
    }
    $sandboxVersionDirectory = Join-Path $Directory 'sandbox-version'
    $sandboxVersion = Invoke-BFProcess -Executable $profile.sandbox.executable -Arguments @('--version') -WorkingDirectory $State.worker_path -InputText '' -OutputDirectory $sandboxVersionDirectory -TimeoutSeconds 60 -Cancelled $Cancelled -CleanEnvironment -MaxOutputBytes 65536
    if ($sandboxVersion.stop_reason -or $sandboxVersion.exit_code -ne 0 -or [IO.File]::ReadAllText($sandboxVersion.stdout).Trim() -cne 'codex-cli 0.154.0') {
        throw 'BF_BLOCKED: current-agent fallback sandbox version is not the verified Codex 0.154.0.'
    }

    $catalogDirectory = Join-Path $Directory 'catalog'
    $criticCatalog = Get-BFCodexCriticCatalog $catalogDirectory -ExpectedModel ([string]$currentHost.model) -ExpectedEffort ([string]$currentHost.effort)
    [void][IO.Directory]::CreateDirectory($catalogDirectory)
    $catalogSourcePath = Join-Path $catalogDirectory 'critic-catalog-source.json'
    $catalogPath = Join-Path $catalogDirectory 'critic-catalog.json'
    Write-BFJson $catalogSourcePath $criticCatalog.source
    Write-BFJson $catalogPath $criticCatalog.catalog
    $catalogSha256 = Get-BFHash $criticCatalog.catalog
    $catalogSourceSha256 = Get-BFFileHash $catalogSourcePath

    [void][IO.Directory]::CreateDirectory($hostRoot)
    foreach ($path in @($scratch,$config)) { [void][IO.Directory]::CreateDirectory($path) }
    $canonicalStoreRoot = if ($null -ne $ProviderContext) {
        Assert-BFSafePath (Get-BFValue $ProviderContext 'canonical_store_root' '')
    } else {
        ''
    }
    $permissions = Get-BFExecutionPermissionProfile $State $scratch $config $false $canonicalStoreRoot
    $overrides = Get-BFProfiledCodexOverrides $permissions (Join-Path $scratch 'logs')
    $rpc = @{ Executable=$profile.executable; WorkingDirectory=$State.worker_path; Cancelled=$Cancelled; MaxOutputBytes=16777216 }
    # Resolve the global MCP configuration in one read-only app-server process,
    # then carry its exact server-name set into the guarded skills/list request.
    # This is the same drift check used by profiled workers and prevents a bare
    # inventory RPC from observing a different MCP topology.
    $configuration = Invoke-BFCodexReadOnlyRpc @rpc -Overrides $overrides -Directory (Join-Path $Directory 'mcp-config-rpc') -Method 'config/read' -Params @{cwd=$State.worker_path;includeLayers=$false}
    $mcpNames = @(Get-BFCodexMcpServerNames $configuration)
    $configuration = $null
    Write-BFJson (Join-Path $Directory 'mcp-config-names.json') @{names=$mcpNames}
    $overrides += @(Get-BFCodexMcpDenyOverrides $mcpNames)
    $rpc.ExpectedMcpServers = $mcpNames
    $rpcDirectory = Join-Path $Directory 'skills-rpc'
    $response = Invoke-BFCodexReadOnlyRpc @rpc -Overrides $overrides -Directory $rpcDirectory -Method 'skills/list' -Params @{cwds=@($State.worker_path);forceReload=$true}
    $inventory = ConvertTo-BFCodexSkillInventory $response $State.worker_path
    $skillsSha256 = Get-BFHash $inventory
    if ($skillsSha256 -cne [string]$profile.codex_skills_sha256) {
        throw 'BF_BLOCKED: discovered Codex skills differ from the registered inventory.'
    }
    $inventoryPath = Join-Path $Directory 'inventory.json'
    Write-BFJson $inventoryPath @{skills=$inventory}

    $capability = [ordered]@{
        capability_version=(Get-BFCodexCriticCapabilityVersion ([string]$currentHost.model)); provider='current_agent'
        model=[string]$currentHost.model; effort=[string]$currentHost.effort
        fresh_context=$true; sealed=$true; terminal=$true; source=[string]$currentHost.source
        executable_sha256=$executableSha256; sandbox_sha256=$sandboxSha256
        catalog_sha256=$catalogSha256; skills_sha256=$skillsSha256
        catalog_source_path=$catalogSourcePath; catalog_source_sha256=$catalogSourceSha256
    }
    $proof = [ordered]@{
        schema_version=1; capability=$capability; dependencies_sha256=Get-BFHash $dependencies
        catalog_source_path=$catalogSourcePath; catalog_source_sha256=$catalogSourceSha256; inventory_path='inventory.json'
        current_host=[ordered]@{session_id=[string]$currentHost.session_id;turn_id=[string]$currentHost.turn_id;rollout_path=[string]$currentHost.rollout_path}
    }
    Write-BFJson $capabilityPath $proof
    return $capability
}

function Get-BFManagedCouncilHostCapability {
    param(
        [Parameter(Mandatory)]$State,
        [string]$Directory,
        [string]$CodexPath,
        [scriptblock]$Cancelled,
        [object]$ProviderContext = $null
    )
    $profile = Get-BFValue $State.request 'execution_profile'
    if ($null -eq $profile -or [string]$profile.provider -cne 'codex') {
        throw 'BF_BLOCKED: current-agent fallback has no supported sealed Codex profile.'
    }
    if ([string]::IsNullOrWhiteSpace($Directory)) {
        $Directory = Join-Path (Get-BFTaskDirectory $State.project_path $State.task_id) 'managed-council/host-capability'
    }
    if ([string]::IsNullOrWhiteSpace($CodexPath)) {
        $declaredExecutable = Get-BFValue $profile 'executable' $null
        if ($null -ne $declaredExecutable) { $CodexPath = [string]$declaredExecutable }
    }
    return Test-BFProfiledCodexHostCapability -State $State -Directory $Directory -CodexPath $CodexPath -Cancelled $Cancelled -ProviderContext $ProviderContext
}

function Get-BFManagedCouncilFallbackRoles {
    param([Parameter(Mandatory)]$Council, [Parameter(Mandatory)][string]$ProjectPath)
    . (Join-Path (Split-Path $PSScriptRoot -Parent) '../1c-spec-review/scripts/Council.Transport.ps1')
    . (Join-Path (Split-Path $PSScriptRoot -Parent) '../1c-spec-review/scripts/Council.Common.ps1')
    $overlay = Get-BSLFlowLocalProviderOverlay -ProjectPath $ProjectPath
    $localText = $null
    $localPath = Join-Path $ProjectPath '.bsl-flow/providers.local.yaml'
    if (Test-Path -LiteralPath $localPath -PathType Leaf) { $localText = Get-Content -Raw -LiteralPath $localPath }
    $fallbackRoles = [System.Collections.Generic.List[string]]::new()
    foreach ($roleName in @('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic', 'chair')) {
        $role = $Council.roles[$roleName]
        if ($null -eq $role -or -not [bool]$role.enabled -or [string]$role.fallback -cne 'current_agent') { continue }
        $model = $Council.models[[string]$role.model]
        $providerName = [string]$model.provider
        $provider = $Council.providers[$providerName]
        $localToken = ''
        if ($null -ne $localText -and $overlay.Contains($providerName) -and [bool]$overlay[$providerName].has_token) {
            $localToken = Get-BSLFlowYamlValue $localText @('providers', $providerName, 'token') ''
        }
        $credential = Resolve-BSLFlowCouncilCredential -ProviderName $providerName -TokenEnv ([string]$provider.token_env) -LocalToken $localToken
        if ([string]$credential.credential_source -ceq 'missing') { [void]$fallbackRoles.Add($roleName) }
    }
    return @($fallbackRoles)
}

function New-BFManagedCouncilHostAdapter {
    <#
      Build the one controller-owned current-agent adapter used by every
      tokenless council role. The host proof is taken once for the cycle and the
      same immutable capability is bound into each role attempt; a role receipt
      still has to prove its own terminal observed model/effort.
    #>
    param(
        [Parameter(Mandatory)]$State,
        [Parameter(Mandatory)][string]$Directory,
        [string]$CodexPath,
        [scriptblock]$Cancelled,
        [object]$ProviderContext = $null
    )
    $reviewRoot = Join-Path (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent) '1c-spec-review'
    $taskSkillRoot = Split-Path $PSScriptRoot -Parent
    # Keep the provider context in the returned closure. Task.Provider.ps1
    # initializes its prior-artifact allowlist at import time, so the closure
    # must restore that allowlist after rehydrating its private module scope.
    $providerContextForRunner = $ProviderContext
    $runnerWorkerCommand = Get-Command Invoke-BFManagedWorker -CommandType Function -ErrorAction SilentlyContinue
    $runnerWorker = if ($null -ne $runnerWorkerCommand) { $runnerWorkerCommand.ScriptBlock } else { $null }
    # GetNewClosure creates a dynamic module. Functions dot-sourced while this
    # factory runs stay in the factory scope and are therefore invisible when a
    # public caller later invokes the returned runner. Rehydrate the exact
    # runner dependencies inside that closure instead of exporting them globally.
    $runnerModulePaths = @(
        (Join-Path $taskSkillRoot 'scripts/Task.Storage.ps1'),
        (Join-Path $taskSkillRoot 'scripts/Task.Process.ps1'),
        (Join-Path $taskSkillRoot 'scripts/Task.Contracts.ps1'),
        (Join-Path $taskSkillRoot 'scripts/Task.Gates.ps1'),
        (Join-Path $taskSkillRoot 'scripts/Task.Engine.ps1'),
        (Join-Path $taskSkillRoot 'scripts/Task.Stages.ps1'),
        (Join-Path $taskSkillRoot 'scripts/Task.Provider.ps1'),
        (Join-Path $taskSkillRoot 'adapters/Codex.Skills.ps1'),
        (Join-Path $taskSkillRoot 'adapters/Codex.ps1'),
        (Join-Path $taskSkillRoot 'adapters/ProfiledCodex.ps1')
    )
    . (Join-Path $reviewRoot 'scripts/Council.Common.ps1')
    . (Join-Path $reviewRoot 'scripts/Council.Profile.ps1')
    $council = (Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $State.project_path).policy
    $fallbackRoles = @(Get-BFManagedCouncilFallbackRoles -Council $council -ProjectPath $State.project_path)
    if ($fallbackRoles.Count -eq 0) {
        return [ordered]@{ capabilities = @{}; fallback_runner = $null; fallback_roles = @() }
    }
    $hostCapabilityDirectory = Join-Path (Assert-BFSafePath $Directory) 'host-capability'
    $hostCapability = Get-BFManagedCouncilHostCapability -State $State -Directory $hostCapabilityDirectory -CodexPath $CodexPath -Cancelled $Cancelled -ProviderContext $ProviderContext
    $capabilities = @{}
    foreach ($role in $fallbackRoles) { $capabilities[$role] = $hostCapability }
    $fallbackRunner = {
        param($Attempt,$PromptText,$Capability)
        foreach ($modulePath in $runnerModulePaths) { . $modulePath }
        . (Join-Path $reviewRoot 'scripts/Review.Common.ps1')
        if ($null -ne $providerContextForRunner) {
            $priorArtifactsCommand = Get-Command Assert-BFProviderPriorArtifacts -CommandType Function -ErrorAction SilentlyContinue
            if ($null -eq $priorArtifactsCommand) {
                throw 'BF_BLOCKED: provider prior-artifact validator is unavailable.'
            }
            # Task.Provider.ps1 resets its script-scoped map on import. Restore
            # the exact immutable declaration before any worker helper can read
            # a context artifact or provider budget ledger.
            [void](& $priorArtifactsCommand $providerContextForRunner)
        }
        $catalogSourcePath = Get-BFValue $Capability 'catalog_source_path' $null
        $catalogSourceSha256 = Get-BFValue $Capability 'catalog_source_sha256' $null
        if ([string]::IsNullOrWhiteSpace([string]$catalogSourcePath) -or
            [string]$catalogSourceSha256 -notmatch '^[0-9a-f]{64}$') {
            throw 'BF_BLOCKED: fallback capability has no exact critic catalog source binding.'
        }
        $catalogSourcePath = Assert-BFSafePath ([string]$catalogSourcePath)
        if (-not (Test-Path -LiteralPath $catalogSourcePath -PathType Leaf) -or
            (Get-BFFileHash $catalogSourcePath) -cne [string]$catalogSourceSha256) {
            throw 'BF_BLOCKED: fallback critic catalog source bytes changed before dispatch.'
        }
        $roleDir = Join-Path $Directory ('fallback-' + [string]$Attempt.role + '-' + [string]$Attempt.sequence)
        $workerRequest = ConvertFrom-Json (Get-BFCanonicalJson $State)
        $workerRequest.request | Add-Member -NotePropertyName timeout_seconds -NotePropertyValue ([math]::Min([int](Get-BFValue $State.request 'timeout_seconds' 1800),600)) -Force
        # Strict identity is an explicit property of this cloned request. It
        # removes --ephemeral and requires the adapter's rollout receipt.
        $workerRequest.request | Add-Member -NotePropertyName require_observed_identity -NotePropertyValue $true -Force
        # These are controller-owned internal fields.  The worker must consume
        # this exact host-proven source instead of refreshing a global catalog.
        $workerRequest.request | Add-Member -NotePropertyName fallback_catalog_source_path -NotePropertyValue $catalogSourcePath -Force
        $workerRequest.request | Add-Member -NotePropertyName fallback_catalog_sha256 -NotePropertyValue ([string]$catalogSourceSha256) -Force
        $workerRequest.request.models.reviewer = [string]$Capability.model
        $workerRequest.request.models.reviewer_effort = [string]$Capability.effort
        if ($null -eq $runnerWorker) { throw 'BF_BLOCKED: managed fallback worker dependency is unavailable.' }
        if ($null -ne $ProviderContext) {
            $result = & $runnerWorker $workerRequest 'spec_review' $PromptText $roleDir $CodexPath $Cancelled -ProviderContext $ProviderContext
        } else {
            $result = & $runnerWorker $workerRequest 'spec_review' $PromptText $roleDir $CodexPath $Cancelled
        }
        if ($result.status -ne 'completed') {
            return [ordered]@{ status = [string]$result.status; payload = $null; usage = $null; reported_cost_usd = $null }
        }
        $hostReceiptPath = Join-Path $roleDir 'host-result.json'
        if (-not (Test-Path -LiteralPath $hostReceiptPath -PathType Leaf)) {
            throw 'BF_BLOCKED: fallback host receipt is missing; current-agent provenance is unproven.'
        }
        $hostReceipt = Read-BFJson $hostReceiptPath
        $observedModel = Get-BFValue $hostReceipt 'observed_model'
        $observedEffort = Get-BFValue $hostReceipt 'observed_effort'
        $requestedModel = Get-BFValue $hostReceipt 'requested_model'
        $requestedEffort = Get-BFValue $hostReceipt 'requested_effort'
        $receiptSessionId = Get-BFValue $hostReceipt 'session_id'
        $receiptTurnId = Get-BFValue $hostReceipt 'turn_id'
        $receiptRolloutPath = Get-BFValue $hostReceipt 'rollout_path'
        if ([string]::IsNullOrWhiteSpace([string]$requestedModel) -or [string]$requestedModel -cne [string]$Capability.model -or [string]$requestedEffort -cne [string]$Capability.effort) {
            throw 'BF_BLOCKED: fallback host receipt names a different requested model/effort than the current host capability.'
        }
        if ([string]::IsNullOrWhiteSpace([string]$observedModel) -or [string]::IsNullOrWhiteSpace([string]$observedEffort)) {
            throw 'BF_BLOCKED: fallback host receipt carries no observed model/effort; requested values are not provenance.'
        }
        if ([string]$receiptSessionId -notmatch '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$' -or
            [string]::IsNullOrWhiteSpace([string]$receiptTurnId) -or [string]::IsNullOrWhiteSpace([string]$receiptRolloutPath)) {
            throw 'BF_BLOCKED: fallback host receipt carries no persisted rollout turn identity.'
        }
        $receiptObserved = Get-BFObservedModelEffort -SessionId ([string]$receiptSessionId) -TurnId ([string]$receiptTurnId)
        if ([string]$receiptObserved.rollout_path -cne [string]$receiptRolloutPath -or
            [string]$receiptObserved.observed_model -cne [string]$observedModel -or
            [string]$receiptObserved.observed_effort -cne [string]$observedEffort) {
            throw 'BF_BLOCKED: fallback host receipt turn identity differs from its rollout evidence.'
        }
        if ([string]$observedModel -cne [string]$Capability.model -or [string]$observedEffort -cne [string]$Capability.effort) {
            throw 'BF_BLOCKED: fallback host receipt resolved a different model/effort than the current host capability.'
        }
        $payload = Read-BFPayload $result $roleDir
        return [ordered]@{ status='completed'; payload=$payload; observed_model=[string]$observedModel; observed_effort=[string]$observedEffort; usage=$null; reported_cost_usd=$null }
    }.GetNewClosure()
    return [ordered]@{ capabilities = $capabilities; fallback_runner = $fallbackRunner; fallback_roles = @($fallbackRoles) }
}

function Get-BFManagedCouncilProviderDispatchDirectory {
    param(
        [Parameter(Mandatory)]$ProviderContext,
        [Parameter(Mandatory)]$Attempt
    )
    $root = Assert-BFSafePath (Get-BFValue $ProviderContext 'artifact_root' '')
    $role = [string](Get-BFValue $Attempt 'role' '')
    if ($role -notin @('brainstorm', 'intent_critic', 'architecture_critic', 'executability_critic', 'chair')) {
        throw 'BF_INVALID: council provider dispatch has an unsupported role.'
    }
    $attemptId = [string](Get-BFValue $Attempt 'attempt_id' '')
    # Council attempt ids are intentionally compact N-format GUIDs. Keep the
    # check explicit because this value becomes part of a provider artifact key.
    if ($attemptId -notmatch '^[0-9a-fA-F]{32}$') {
        throw 'BF_INVALID: council provider dispatch has an invalid attempt identity.'
    }
    return Assert-BFSafePath (Join-Path $root ('council/' + $role + '/attempt-' + $attemptId))
}

function Add-BFManagedCouncilProviderBudgetOutcome {
    param(
        [Parameter(Mandatory)]$State,
        [Parameter(Mandatory)]$ProviderContext,
        [Parameter(Mandatory)]$Attempt,
        [Parameter(Mandatory)][string]$Directory,
        $Transport,
        [Parameter(Mandatory)][string]$Status
    )
    $budget = Get-BFValue $State.request 'budget'
    if ($null -eq $budget) { return }
    foreach ($name in @('Get-BFProviderDispatchKey', 'Get-BFProviderBudgetLedger', 'Add-BFProviderBudgetEntry')) {
        if ($null -eq (Get-Command $name -CommandType Function -ErrorAction SilentlyContinue)) {
            throw "BF_BLOCKED: provider budget helper is unavailable: $name."
        }
    }
    $observed = Get-BFValue $Transport 'observed'
    $reported = Get-BFValue $Transport 'reported_cost_usd'
    $usage = Get-BFValue $Transport 'usage'
    if ($null -ne $reported) {
        if (($reported -isnot [int]) -and ($reported -isnot [long]) -and ($reported -isnot [double]) -and
            ($reported -isnot [decimal]) -and ($reported -isnot [single])) {
            throw 'BF_BLOCKED: invalid reported council provider cost.'
        }
        $reported = [double]$reported
        if ([double]::IsNaN($reported) -or [double]::IsInfinity($reported) -or $reported -lt 0) {
            throw 'BF_BLOCKED: invalid reported council provider cost.'
        }
    }
    $key = Get-BFProviderDispatchKey $ProviderContext $Directory
    $candidate = [ordered]@{
        kind = 'outcome'; dispatch = $key
        provider = [string](Get-BFValue $Attempt.binding 'provider' '')
        stage = 'spec_review'; requested_model = [string](Get-BFValue $Attempt.binding 'model' '')
        observed_model = Get-BFValue $observed 'model'
        reported_cost_usd = $reported; billed_cost_usd = $null
        cost_state = $(if ($null -ne $reported) { 'known' } else { 'unknown' })
        usage = $usage; terminal_status = $Status; currency = $budget.currency
        at = [DateTime]::UtcNow.ToString('o')
    }
    $ledger = Get-BFProviderBudgetLedger $ProviderContext
    $existing = @($ledger.entries | Where-Object { $_.kind -eq 'outcome' -and $_.dispatch -ceq $key })
    if ($existing.Count -gt 0) {
        if ((Get-BFHash (Get-BFBudgetEntryCore $existing[0])) -cne (Get-BFHash (Get-BFBudgetEntryCore $candidate))) {
            throw 'BF_BLOCKED: conflicting provider council budget outcome.'
        }
        return
    }
    Add-BFProviderBudgetEntry $ProviderContext $candidate
}

function New-BFManagedCouncilProviderDispatchHooks {
    param(
        [Parameter(Mandatory)]$State,
        [Parameter(Mandatory)]$ProviderContext
    )
    $artifactRoot = Assert-BFSafePath (Get-BFValue $ProviderContext 'artifact_root' '')
    $beforeDispatch = {
        param($Attempt, $Route)
        # The fallback worker owns its own provider reservation/outcome through
        # Invoke-BFManagedWorker. The Council hook therefore only replaces the
        # Council ledger for direct API calls.
        if ([string](Get-BFValue $Route 'route' '') -ceq 'current_agent_fallback') { return }
        $directory = Get-BFManagedCouncilProviderDispatchDirectory -ProviderContext $ProviderContext -Attempt $Attempt
        foreach ($name in @('Assert-BFProviderBudgetAdmission', 'Add-BFProviderBudgetReservation')) {
            if ($null -eq (Get-Command $name -CommandType Function -ErrorAction SilentlyContinue)) {
                throw "BF_BLOCKED: provider budget helper is unavailable: $name."
            }
        }
        $provider = [string](Get-BFValue $Attempt.binding 'provider' '')
        $model = [string](Get-BFValue $Attempt.binding 'model' '')
        [void](Assert-BFProviderBudgetAdmission $State $ProviderContext $directory)
        [void](Add-BFProviderBudgetReservation $State $ProviderContext $directory $provider 'spec_review' $model)
    }.GetNewClosure()
    $afterDispatch = {
        param($Attempt, $Route, $Transport, $Status, $Failure)
        if ([string](Get-BFValue $Route 'route' '') -ceq 'current_agent_fallback') { return }
        $directory = Get-BFManagedCouncilProviderDispatchDirectory -ProviderContext $ProviderContext -Attempt $Attempt
        $observed = Get-BFValue $Transport 'observed'
        $receipt = [ordered]@{
            schema_version = 1; role = [string](Get-BFValue $Attempt 'role' '')
            council_attempt_id = [string](Get-BFValue $Attempt 'attempt_id' '')
            status = [string]$Status; observed_model = Get-BFValue $observed 'model'
            observed_effort = Get-BFValue $observed 'effort'
            usage = Get-BFValue $Transport 'usage'
            reported_cost_usd = Get-BFValue $Transport 'reported_cost_usd'
        }
        # This receipt is a provider artifact for the outer Go controller. It
        # carries observations only; the transport route never relays a token or
        # arbitrary controller command through this hook.
        Write-BFJson -Path (Join-Path $directory 'host-result.json') -Value $receipt -Replace
        if ([string]$Status -ceq 'completed') {
            if ($null -eq (Get-Command Complete-BFProviderBudgetDispatch -CommandType Function -ErrorAction SilentlyContinue)) {
                throw 'BF_BLOCKED: provider budget completion helper is unavailable.'
            }
            $provider = [string](Get-BFValue $Attempt.binding 'provider' '')
            $model = [string](Get-BFValue $Attempt.binding 'model' '')
            [void](Complete-BFProviderBudgetDispatch $State $ProviderContext $directory $provider 'spec_review' $model)
        }
        else {
            Add-BFManagedCouncilProviderBudgetOutcome -State $State -ProviderContext $ProviderContext -Attempt $Attempt -Directory $directory -Transport $Transport -Status ([string]$Status)
        }
    }.GetNewClosure()
    return [ordered]@{ BeforeDispatch = $beforeDispatch; AfterDispatch = $afterDispatch; artifact_root = $artifactRoot }
}

function Invoke-BFProfileSpecCritic {
    param($State,[string]$Directory,[string]$CodexPath,[scriptblock]$Cancelled,[object]$ProviderContext=$null)
    $reviewRoot=Join-Path (Split-Path (Split-Path $PSScriptRoot -Parent) -Parent) '1c-spec-review'
    . (Join-Path $reviewRoot 'scripts/Review.Common.ps1')
    $change=Get-BFChangePath $State
    $configPath=Join-Path $State.project_path 'bsl-flow.yaml'
    $configText=if(Test-Path -LiteralPath $configPath){[IO.File]::ReadAllText($configPath)}else{''}
    $culture=[Globalization.CultureInfo]::InvariantCulture
    $maxInput=[int]::Parse((Get-BSLFlowYamlValue $configText @('review','input','max_file_bytes') '262144'),$culture)
    if($maxInput -lt 1024 -or $maxInput -gt 1048576){throw 'BF_INVALID: review input bound is outside the supported range.'}
    # Council is the default managed spec_review route. The chair reconciliation
    # is inline in review.json v2; no separate spec_reconcile worker is dispatched.
    . (Join-Path $reviewRoot 'scripts/Council.Profile.ps1')
    $managedCouncil=$null
    try { $managedCouncil=(Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $State.project_path).policy } catch { throw }
    $changeName=Split-Path (Get-BFChangePath $State) -Leaf
    $preparedPath=Join-Path $State.project_path ('.bsl-flow/reports/spec-review/' + $changeName + '.council/publication/prepared.json')
    if((Test-Path -LiteralPath $preparedPath -PathType Leaf) -and
       ($null -eq $managedCouncil -or -not [bool]$managedCouncil.enabled -or $managedCouncil.legacy_mode -ceq 'opencode_compat')){
        throw 'BF_BLOCKED: prepared council publication requires the council route to remain enabled.'
    }
    if($null -ne $managedCouncil -and [bool]$managedCouncil.enabled -and $managedCouncil.legacy_mode -cne 'opencode_compat'){
        . (Join-Path $reviewRoot 'scripts/Invoke-CouncilReview.ps1')
        . (Join-Path $reviewRoot 'scripts/Council.Engine.ps1')
        # A chair may have durably prepared its final publication immediately
        # before a controller crash. Complete that exact byte package first;
        # otherwise a fresh snapshot/capability probe could create new council
        # attempts before recovery has reconciled the already dispatched cycle.
        $preparedRecovery = Resume-BSLFlowCouncilPreparedPublicationIfPresent -ProjectRoot $State.project_path -ChangeName $changeName
        if ($null -ne $preparedRecovery) {
            $recoveredReviewPath = Join-Path $State.project_path "openspec\changes\$changeName\review.json"
            if (-not (Test-Path -LiteralPath $recoveredReviewPath -PathType Leaf)) { throw 'BF_BLOCKED: prepared council publication recovery produced no review.json.' }
            return Read-BFJson $recoveredReviewPath
        }
        # Fallback provenance may come only from a controller-observed host
        # receipt, never from request.models: the runner reads host-result.json
        # written by the adapter after the fresh worker context terminated and
        # reports its observed model/effort. A receipt without observed identity
        # cannot prove the current-agent contract and blocks the role.
        $evidenceText = Get-BFManagedCouncilEvidence -State $State -MaxBytes $maxInput -ProviderContext $ProviderContext
        # Direct API roles do not depend on the current host. The shared adapter
        # probes the host only when an enabled role actually needs tokenless
        # current-agent fallback and returns one immutable capability/runner pair.
        $adapter = New-BFManagedCouncilHostAdapter -State $State -Directory $Directory -CodexPath $CodexPath -Cancelled $Cancelled -ProviderContext $ProviderContext
        $councilHooks = $null
        if ($null -ne $ProviderContext) {
            $councilHooks = New-BFManagedCouncilProviderDispatchHooks -State $State -ProviderContext $ProviderContext
        }
        $councilArguments = @{
            ProjectPath = $State.project_path; ChangeName = $changeName; EvidenceText = $evidenceText
            AllowLiveDispatch = $true; Capabilities = $adapter.capabilities; FallbackRunner = $adapter.fallback_runner
            Cancelled = $Cancelled
        }
        if ($null -ne $councilHooks) {
            $councilArguments.BeforeDispatch = $councilHooks.BeforeDispatch
            $councilArguments.AfterDispatch = $councilHooks.AfterDispatch
        }
        $managedResult=Invoke-BSLFlowCouncilReview @councilArguments
        return $managedResult.review
    }
    $lint=& (Join-Path $reviewRoot 'scripts/Test-1CSpec.ps1') -ChangePath $change
    if(-not $lint.passed){throw 'BF_BLOCKED: specification lint failed before the profiled critic.'}
    $policy=Get-BSLFlowReviewPolicy $configText
    $culture=[Globalization.CultureInfo]::InvariantCulture
    $maxInput=[int]::Parse((Get-BSLFlowYamlValue $configText @('review','input','max_file_bytes') '262144'),$culture)
    $timeout=[int]::Parse((Get-BSLFlowYamlValue $configText @('review','runtime','timeout_seconds') '600'),$culture)
    $maxOutput=[int]::Parse((Get-BSLFlowYamlValue $configText @('review','runtime','max_output_bytes') '1048576'),$culture)
    if($maxInput -lt 1024 -or $maxInput -gt 1048576 -or $timeout -lt 1 -or $timeout -gt 3600 -or $maxOutput -lt 65536 -or $maxOutput -gt 16777216){throw 'BF_INVALID: review input/runtime limits are outside the supported range.'}
    $inputs=[ordered]@{}
    $inputDirectory=Join-Path $Directory 'critic-inputs'
    [void][IO.Directory]::CreateDirectory($inputDirectory)
    $blocks=@()
    foreach($name in @('original-task.md','spec.md','design.md')){
        $path=Assert-BFSafePath (Join-Path $change $name)
        if($name -eq 'design.md' -and -not (Test-Path -LiteralPath $path)){$inputs[$name]=$null;continue}
        $snapshot=Get-BSLFlowBoundedUtf8Snapshot -Path $path -MaxBytes $maxInput
        $inputs[$name]=$snapshot.Sha256
        $saved=Join-Path $inputDirectory $name
        if(Test-Path -LiteralPath $saved){if((Get-BFFileHash $saved) -cne $snapshot.Sha256){throw 'BF_BLOCKED: retained critic input differs; use a new attempt.'}}
        else{[IO.File]::WriteAllBytes($saved,$snapshot.Bytes)}
        $blocks+="<<<BEGIN UNTRUSTED DATA: $name>>>`n$($snapshot.Text)`n<<<END UNTRUSTED DATA: $name>>>"
    }
    $promptSnapshot=Get-BSLFlowBoundedUtf8Snapshot -Path (Join-Path $reviewRoot 'reviewer/spec-reviewer-prompt.md') -MaxBytes $maxInput
    $rubric=Get-BSLFlowBoundedUtf8Snapshot -Path (Join-Path $reviewRoot 'references/reviewer-rubric.md') -MaxBytes $maxInput
    # Both hosts use the packaged sealed critic contract: attachments only,
    # no shell, source writes, MCP, or additional skill discovery in this stage.
    $prompt=$promptSnapshot.Text+"`n<<<BEGIN TRUSTED REVIEW POLICY: RUBRIC>>>`n"+$rubric.Text+"`n<<<END TRUSTED REVIEW POLICY: RUBRIC>>>`n"+($blocks -join "`n`n")+"`nThis is an attached-only independent review. Do not use tools. Place the contracted review JSON object inside payload_json of the BSL Flow worker response; outer status completed means only that the review was produced, never that the task was accepted."
    $reviewState=ConvertFrom-Json (Get-BFCanonicalJson $State)
    $reviewState.request|Add-Member -NotePropertyName timeout_seconds -NotePropertyValue ([math]::Min([int](Get-BFValue $State.request 'timeout_seconds' 1800),$timeout)) -Force
    if ($null -ne $ProviderContext) {
        $result=Invoke-BFManagedWorker -State $reviewState -Stage 'spec_review' -Prompt $prompt -Directory (Join-Path $Directory 'critic') -CodexPath $CodexPath -Cancelled $Cancelled -MaxOutputBytes $maxOutput -ProviderContext $ProviderContext
    } else {
        $result=Invoke-BFManagedWorker $reviewState 'spec_review' $prompt (Join-Path $Directory 'critic') $CodexPath $Cancelled $maxOutput
    }
    if($result.status -ne 'completed'){throw 'BF_BLOCKED: profiled independent critic did not return a completed review.'}
    $payloadPath=Join-Path $Directory 'critic/payload.json'
    if((Test-Path -LiteralPath $payloadPath) -and [IO.File]::ReadAllText($payloadPath) -cne $result.payload_json){throw 'BF_BLOCKED: cached critic payload differs from the verified model result.'}
    $rawReview=Read-BFPayload $result (Join-Path $Directory 'critic')
    $completion=@{
        RawReview=$rawReview;OriginalTaskPath=(Join-Path $change 'original-task.md');SpecPath=(Join-Path $change 'spec.md');DesignPath=(Join-Path $change 'design.md')
        Agent='bsl-flow-spec-reviewer-sealed';Model=$State.request.models.reviewer
        PassWeightedScore=$policy.PassWeightedScore;BlockBelowWeightedScore=$policy.BlockBelowWeightedScore
        MaxOverengineeringIndexForPass=$policy.MaxOverengineeringIndexForPass;MaxUnjustifiedRatioForPass=$policy.MaxUnjustifiedRatioForPass
        OriginalTaskSha256=$inputs['original-task.md'];SpecSha256=$inputs['spec.md'];DesignSha256=$inputs['design.md']
    }
    $review=Complete-BSLFlowReview @completion
    foreach($name in $inputs.Keys){
        $path=Join-Path $change $name
        $actual=if(Test-Path -LiteralPath $path){Get-BFFileHash $path}else{$null}
        if($actual -cne $inputs[$name]){throw 'BF_BLOCKED: specification input changed during independent review.'}
    }
    Write-BSLFlowJsonAtomic -Value $review -Path (Join-Path $change 'review.json')
    return $review
}
