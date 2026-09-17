#Requires -Version 7.0
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Codex.Skills.ps1')
. (Join-Path (Split-Path $PSScriptRoot -Parent) 'scripts/Task.Execution.ps1')

function Get-BFProfiledCodexOverrides {
    param([string]$Permissions,[string]$LogDirectory)
    $arguments=@('-c','approval_policy="never"','-c','default_permissions="bsl_execution"','-c',$Permissions,'-c','windows.sandbox="elevated"','-c','skills.include_instructions=false','-c','agents.enabled=false','-c','mcp_servers={}','-c','web_search="disabled"','-c',('log_dir='+(ConvertTo-Json -InputObject $LogDirectory.Replace('\','/') -Compress)))
    foreach($feature in @('plugins','apps','multi_agent','multi_agent_v2','memories','shell_snapshot','hooks','browser_use','computer_use','in_app_browser','skill_mcp_dependency_install')){$arguments+=@('--disable',$feature)}
    return $arguments
}

function Get-BFCodexCriticModelContract {
    param([Parameter(Mandatory)][string]$ExpectedModel)
    # The sealed critic route is deliberately allowlisted.  A profile or a
    # mutable cache may select the model only after the current host has proved
    # its identity; arbitrary model metadata must never widen this route.
    switch ($ExpectedModel) {
        'gpt-5.6-luna' {
            return [ordered]@{
                slug='gpt-5.6-luna'; experimental_supported_tools=@()
                multi_agent_version='v1'; multi_agent_reasoning_effort=$null
            }
        }
        'gpt-6-astra' {
            return [ordered]@{
                slug='gpt-6-astra'; experimental_supported_tools=@('send_user_message_async','clock')
                multi_agent_version='v2'; multi_agent_reasoning_effort='xhigh'
            }
        }
        default { throw ('BF_BLOCKED: sealed Codex critic model is not allowlisted: ' + $ExpectedModel) }
    }
}

function Get-BFCodexCriticCapabilityVersion {
    param([Parameter(Mandatory)][string]$ExpectedModel)
    # Keep the native binary/version contract shared, but make the model part
    # of the capability identity.  A persisted sealed receipt must never be
    # reusable after switching the current host between the allowlisted models.
    Get-BFCodexCriticModelContract $ExpectedModel | Out-Null
    return ('codex-0.154.0-' + $ExpectedModel + '-direct-empty-tools-v1')
}

function Assert-BFCodexCriticModelSource {
    param(
        [Parameter(Mandatory)]$Model,
        [Parameter(Mandatory)][string]$ExpectedModel,
        [AllowNull()][string]$ExpectedEffort=''
    )
    $contract=Get-BFCodexCriticModelContract $ExpectedModel
    $requiredFields=@('slug','use_responses_lite','shell_type','apply_patch_tool_type','tool_mode','experimental_supported_tools')
    # Astra's model-level multi-agent and reasoning declarations are part of the
    # observed cache signature.  They are accepted as source evidence and
    # removed from the sealed catalog below; omitting them would turn a partial
    # cache entry into an apparently safe one.
    if($ExpectedModel -ceq 'gpt-6-astra'){$requiredFields+=@('multi_agent_version','multi_agent_reasoning_effort','supported_reasoning_levels')}
    foreach($field in $requiredFields){
        $hasField=if($Model -is [System.Collections.IDictionary]){$Model.Contains($field)}else{$null -ne $Model.PSObject.Properties[$field]}
        if(-not $hasField){throw ('BF_BLOCKED: ' + $ExpectedModel + ' model metadata is missing ' + $field + '.')}
    }
    if($Model.slug -cne $contract.slug -or $Model.use_responses_lite -ne $true -or $Model.shell_type -cne 'unified_exec' -or $Model.apply_patch_tool_type -cne 'freeform' -or $Model.tool_mode -cne 'code_mode_only'){
        throw ('BF_BLOCKED: ' + $ExpectedModel + ' model metadata differs from the verified sealed-critic signature.')
    }
    $experimental=@($Model.experimental_supported_tools|ForEach-Object{
        if($_ -isnot [string] -or [string]::IsNullOrWhiteSpace($_)){throw ('BF_BLOCKED: ' + $ExpectedModel + ' experimental tool metadata is invalid.')}
        [string]$_
    })
    if((Get-BFHash @($experimental|Sort-Object)) -cne (Get-BFHash @($contract.experimental_supported_tools|Sort-Object))){
        throw ('BF_BLOCKED: ' + $ExpectedModel + ' experimental tool metadata differs from the verified sealed-critic signature.')
    }
    $multiAgentVersion=Get-BFValue $Model 'multi_agent_version' $null
    if($ExpectedModel -ceq 'gpt-6-astra' -and $null -eq $multiAgentVersion){throw 'BF_BLOCKED: gpt-6-astra model metadata is missing multi_agent_version.'}
    if($null -ne $multiAgentVersion -and [string]$multiAgentVersion -cne [string]$contract.multi_agent_version){
        throw ('BF_BLOCKED: ' + $ExpectedModel + ' multi-agent metadata differs from the verified sealed-critic signature.')
    }
    $multiAgentEffort=Get-BFValue $Model 'multi_agent_reasoning_effort' $null
    if($ExpectedModel -ceq 'gpt-6-astra' -and $null -eq $multiAgentEffort){throw 'BF_BLOCKED: gpt-6-astra model metadata is missing multi_agent_reasoning_effort.'}
    if($null -ne $multiAgentEffort -and [string]$multiAgentEffort -cne [string]$contract.multi_agent_reasoning_effort){
        throw ('BF_BLOCKED: ' + $ExpectedModel + ' multi-agent reasoning metadata differs from the verified sealed-critic signature.')
    }
    if(-not [string]::IsNullOrWhiteSpace($ExpectedEffort)){
        $levels=Get-BFValue $Model 'supported_reasoning_levels' $null
        if($null -eq $levels -and $ExpectedModel -ceq 'gpt-6-astra'){throw 'BF_BLOCKED: gpt-6-astra catalog does not declare supported reasoning levels.'}
        if($null -ne $levels){
            $levelNames=@($levels|ForEach-Object{
                $name=[string](Get-BFValue $_ 'effort' '')
                if([string]::IsNullOrWhiteSpace($name)){throw ('BF_BLOCKED: ' + $ExpectedModel + ' catalog contains an invalid reasoning level.')}
                $name
            })
            if(@($levelNames|Sort-Object -Unique).Count -ne $levelNames.Count){throw ('BF_BLOCKED: ' + $ExpectedModel + ' catalog contains duplicate reasoning levels.')}
            $matches=@($levelNames|Where-Object{$_ -ceq $ExpectedEffort})
            if($matches.Count -ne 1){throw ('BF_BLOCKED: ' + $ExpectedModel + ' catalog does not declare the observed reasoning effort.')}
        }
    }
    return $contract
}

function Get-BFCodexCachedCriticModel {
    param([string]$ExpectedModel='gpt-5.6-luna',[AllowNull()][string]$ExpectedEffort='')
    $codexHome=if($env:CODEX_HOME){$env:CODEX_HOME}else{Join-Path $env:USERPROFILE '.codex'}
    $path=Assert-BFSafePath (Join-Path $codexHome 'models_cache.json')
    if(-not (Test-Path -LiteralPath $path) -or (Get-Item -LiteralPath $path).Length -gt 16777216){throw 'BF_BLOCKED: bounded native Codex model catalog is unavailable.'}
    $before=Get-BFFileHash $path
    $cache=Read-BFJson $path
    if((Get-BFFileHash $path) -cne $before){throw 'BF_BLOCKED: native model catalog changed while taking the critic snapshot.'}
    $contract=Get-BFCodexCriticModelContract $ExpectedModel
    $models=@($cache.models|Where-Object{$_.slug -ceq $contract.slug})
    if($models.Count -ne 1){throw ('BF_BLOCKED: native model catalog has no unique ' + $ExpectedModel + ' entry.')}
    Assert-BFCodexCriticModelSource $models[0] $ExpectedModel $ExpectedEffort | Out-Null
    return [ordered]@{source_cache_path=$path;source_cache_sha256=$before;model=$models[0]}
}

function Get-BFCodexCriticCatalog {
    param([string]$Directory,[string]$ExpectedModel='gpt-5.6-luna',[AllowNull()][string]$ExpectedEffort='')
    $sourcePath=Join-Path $Directory 'critic-catalog-source.json'
    $source=if(Test-Path -LiteralPath $Directory){
        if(-not(Test-Path -LiteralPath $sourcePath)){throw 'BF_BLOCKED: partial Codex critic catalog; reconcile without retry.'}
        Read-BFJson $sourcePath
    }else{Get-BFCodexCachedCriticModel -ExpectedModel $ExpectedModel -ExpectedEffort $ExpectedEffort}
    return (ConvertTo-BFCodexCriticCatalog -Source $source -ExpectedModel $ExpectedModel -ExpectedEffort $ExpectedEffort)
}

function Get-BFCodexCriticCatalogFromSourcePath {
    param([Parameter(Mandatory)][string]$SourcePath,[string]$ExpectedModel='gpt-5.6-luna',[AllowNull()][string]$ExpectedEffort='')
    $SourcePath=Assert-BFSafePath $SourcePath
    if(-not(Test-Path -LiteralPath $SourcePath -PathType Leaf)){throw 'BF_BLOCKED: exact Codex critic catalog source is missing.'}
    return (ConvertTo-BFCodexCriticCatalog -Source (Read-BFJson $SourcePath) -ExpectedModel $ExpectedModel -ExpectedEffort $ExpectedEffort)
}

function ConvertTo-BFCodexCriticCatalog {
    param([Parameter(Mandatory)]$Source,[string]$ExpectedModel='gpt-5.6-luna',[AllowNull()][string]$ExpectedEffort='')
    Assert-BFFields $source @('source_cache_path','source_cache_sha256','model') @() 'critic catalog source'
    if($source.source_cache_sha256 -cnotmatch '^[0-9a-f]{64}$'){throw 'BF_BLOCKED: invalid critic source catalog identity.'}
    $model=$source.model
    Assert-BFCodexCriticModelSource $model $ExpectedModel $ExpectedEffort | Out-Null
    $copy=ConvertFrom-Json (Get-BFCanonicalJson $model) -Depth 100
    # The raw cache declaration is retained in `source` for auditability, while
    # every model-owned tool switch is made inert in the catalog consumed by the
    # sealed process.  Optional fields are normalized only when present so old
    # Luna fixtures remain byte-compatible apart from their existing tool fields.
    $zeroToolValues=[ordered]@{
        shell_type='disabled'; apply_patch_tool_type=$null; web_search_tool_type=$null; tool_mode='direct'
        experimental_supported_tools=@(); multi_agent_version=$null; multi_agent_reasoning_effort=$null
        supports_search_tool=$false; node_repl_disabled=$true; node_repl_auto_review_required=$false
        include_skills_usage_instructions=$false; include_plugin_usage_instructions=$false; include_apps_usage_instructions=$false
    }
    foreach($name in $zeroToolValues.Keys){
        if($null -ne $copy.PSObject.Properties[$name]){$copy.$name=$zeroToolValues[$name]}
    }
    return [ordered]@{source=$source;catalog=[ordered]@{models=@($copy)}}
}

function Get-BFCodexCriticOverrides {
    param([string]$CatalogPath)
    $settings=@('model_provider="openai"','project_doc_max_bytes=0','agents.enabled=false','tools.update_plan.enabled=false','tools.experimental_request_user_input.enabled=false',('model_catalog_json='+(ConvertTo-Json -InputObject $CatalogPath.Replace('\','/') -Compress)))
    foreach($feature in @('shell_tool','view_image','deferred_executor','request_permissions_tool','token_budget','current_time_reminder','sleep_tool','tool_suggest','image_generation','goals','remote_models','remote_plugin','enable_request_compression')){$settings+='features.'+$feature+'=false'}
    $arguments=@();foreach($setting in $settings){$arguments+=@('-c',$setting)}
    return $arguments
}

function Read-BFProfiledCodexEvents {
    param([string]$Path,[int]$ExitCode,[array]$AllowedMcpTools,[switch]$NoTools)
    if($ExitCode -ne 0){throw 'BF_BLOCKED: Codex provider/process failed.'}
    $session=$null;$usage=$null;$started=0;$completed=0;$final=$null
    $items=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    if((Get-Item -LiteralPath $Path).Length -gt 16777216){throw 'BF_BLOCKED: Codex JSONL exceeds the output bound.'}
    foreach($line in [IO.File]::ReadAllLines($Path)){
        if([string]::IsNullOrWhiteSpace($line)){continue}
        try{$event=ConvertFrom-Json $line -Depth 100 -ErrorAction Stop}catch{throw 'BF_BLOCKED: malformed Codex JSONL stream.'}
        if($completed -ne 0){throw 'BF_BLOCKED: Codex emitted events after completion.'}
        switch($event.type){
            'thread.started' {if($session -or $started){throw 'BF_BLOCKED: duplicate Codex session.'};$session=$event.thread_id;Assert-BFText $session 'session_id'}
            'turn.started' {if(-not $session -or ++$started -ne 1){throw 'BF_BLOCKED: invalid Codex turn start.'}}
            'turn.completed' {if($started -ne 1 -or ++$completed -ne 1){throw 'BF_BLOCKED: invalid Codex completion.'};$usage=$event.usage}
            {$_ -in @('error','turn.failed')} {throw 'BF_BLOCKED: Codex reported a provider failure.'}
            {$_ -in @('item.started','item.updated','item.completed')} {
                if($started -ne 1){throw 'BF_BLOCKED: Codex item outside the turn.'}
                $item=$event.item
                if($NoTools -and $item.type -notin @('agent_message','reasoning')){throw 'BF_BLOCKED: attached-only Codex critic emitted a tool event.'}
                if($item.type -notin @('agent_message','reasoning','command_execution','file_change','mcp_tool_call','todo_list')){throw 'BF_BLOCKED: unregistered Codex item type.'}
                if($item.type -eq 'mcp_tool_call' -and ($item.server -cne 'unica' -or $item.tool -cnotin $AllowedMcpTools)){throw 'BF_BLOCKED: Codex called an unregistered MCP tool.'}
                if($item.type -eq 'error'){throw 'BF_BLOCKED: Codex item error.'}
                if($event.type -eq 'item.completed'){
                    if(-not $items.Add($item.id)){throw 'BF_BLOCKED: duplicate Codex completed item.'}
                    if($item.type -eq 'agent_message'){$final=$item.text}
                }
            }
            default {throw 'BF_BLOCKED: unsupported Codex JSONL event.'}
        }
    }
    if(-not $session -or $completed -ne 1 -or $null -eq $final){throw 'BF_BLOCKED: incomplete Codex session evidence.'}
    Assert-BFFields $usage @('input_tokens','cached_input_tokens','output_tokens') @('cache_write_input_tokens','reasoning_output_tokens') 'Codex usage'
    foreach($name in @('input_tokens','cached_input_tokens','output_tokens','cache_write_input_tokens','reasoning_output_tokens')){
        if($usage.PSObject.Properties.Name -cnotcontains $name){continue}
        if($usage.$name -isnot [ValueType] -or $usage.$name -is [bool] -or $usage.$name -lt 0 -or [math]::Floor([double]$usage.$name) -ne [double]$usage.$name){throw 'BF_BLOCKED: invalid Codex usage counters.'}
    }
    return [ordered]@{session_id=$session;usage=$usage;final=$final}
}

function Invoke-BFProfiledCodexWorker {
    param($State,[string]$Stage,[string]$Prompt,[string]$Directory,[string]$CodexPath,[scriptblock]$Cancelled,[int]$MaxOutputBytes=16777216)
    $dependencies=Get-BFExecutionDependencies $State;$profile=$State.request.execution_profile
    if($MaxOutputBytes -lt 65536 -or $MaxOutputBytes -gt 16777216){throw 'BF_INVALID: managed output bound is outside the supported range.'}
    if($profile.provider -cne 'codex' -or (Assert-BFSafePath $CodexPath) -cne (Assert-BFSafePath $profile.sandbox.executable)){throw 'BF_BLOCKED: managed Codex/sandbox identity mismatch.'}
    Assert-BFWorkerConfiguration $State.worker_path
    $model=if($Stage -in @('code_review','spec_review')){$State.request.models.reviewer}else{$State.request.models.worker}
    $effort=if($Stage -in @('code_review','spec_review')){$State.request.models.reviewer_effort}else{$State.request.models.worker_effort}
    $critic=$Stage -eq 'spec_review'
    # Ephemeral workers intentionally may not leave a rollout file. Only the
    # current-agent fallback opts into strict host provenance; that route sets
    # require_observed_identity on its cloned request before dispatch.
    $requireObserved=$false
    try{$requireObserved=[bool](Get-BFValue $State.request 'require_observed_identity' $false)}catch{$requireObserved=$false}
    if($critic -and (Get-BFValue $profile 'executable_sha256' $null) -cne 'be96b992178b1e467c225800da0d65f2c86d5eba1ef0b14632f65db381cbdfde'){throw 'BF_BLOCKED: no-tools critic capability requires the exact verified native executable.'}
    if($critic){
        # Ordinary sealed critics retain the historical Luna contract.  A
        # different allowlisted model is accepted only for the strict
        # current-agent route, where both host and rollout identity are bound.
        if(-not $requireObserved -and $model -cne 'gpt-5.6-luna'){throw 'BF_BLOCKED: non-strict sealed critic is pinned to gpt-5.6-luna.'}
        Get-BFCodexCriticModelContract $model | Out-Null
    }
    $directory=Assert-BFSafePath $Directory
    $controllerRoot=(Assert-BFSafePath (Join-Path $State.project_path '.bsl-flow/tasks')).TrimEnd('\','/')+'\'
    if(-not $directory.StartsWith($controllerRoot,[StringComparison]::OrdinalIgnoreCase)){throw 'BF_INVALID: Codex controller attempt must be under the private task directory.'}
    $schema=Join-Path (Split-Path $PSScriptRoot -Parent) 'schemas/worker-result.schema.json'
    $inputText=if($critic){$Prompt}else{$Prompt+(Get-BFToolsetPrompt $State)}
    $binding=[ordered]@{dependencies=$dependencies;stage=$Stage;prompt_sha256=Get-BFHash $inputText;schema_sha256=Get-BFFileHash $schema;adapter_sha256=Get-BFFileHash $PSCommandPath;rpc_sha256=Get-BFFileHash (Join-Path $PSScriptRoot 'Codex.Skills.ps1');worker_path=$State.worker_path;max_output_bytes=$MaxOutputBytes}
    $criticCatalog=$null
    if($critic){
        $catalogSourcePath=Get-BFValue $State.request 'fallback_catalog_source_path' $null
        $catalogSourceSha256=Get-BFValue $State.request 'fallback_catalog_sha256' $null
        if($requireObserved){
            if([string]::IsNullOrWhiteSpace([string]$catalogSourcePath) -or [string]$catalogSourceSha256 -notmatch '^[0-9a-f]{64}$'){
                throw 'BF_BLOCKED: strict current-agent critic requires an exact catalog source binding.'
            }
            $catalogSourcePath=Assert-BFSafePath ([string]$catalogSourcePath)
            if(-not(Test-Path -LiteralPath $catalogSourcePath -PathType Leaf) -or (Get-BFFileHash $catalogSourcePath) -cne [string]$catalogSourceSha256){
                throw 'BF_BLOCKED: strict current-agent critic catalog source bytes changed.'
            }
            $criticCatalog=Get-BFCodexCriticCatalogFromSourcePath $catalogSourcePath -ExpectedModel $model -ExpectedEffort $effort
        }elseif(-not [string]::IsNullOrWhiteSpace([string]$catalogSourcePath)){
            if([string]$catalogSourceSha256 -notmatch '^[0-9a-f]{64}$'){throw 'BF_BLOCKED: supplied critic catalog source binding is invalid.'}
            $catalogSourcePath=Assert-BFSafePath ([string]$catalogSourcePath)
            if((Get-BFFileHash $catalogSourcePath) -cne [string]$catalogSourceSha256){throw 'BF_BLOCKED: supplied critic catalog source bytes changed.'}
            $criticCatalog=Get-BFCodexCriticCatalogFromSourcePath $catalogSourcePath -ExpectedModel $model -ExpectedEffort $effort
        }else{
            $criticCatalog=Get-BFCodexCriticCatalog $directory -ExpectedModel $model -ExpectedEffort $effort
        }
        $binding.critic_catalog_sha256=Get-BFHash $criticCatalog.catalog
        $binding.critic_catalog_source_sha256=Get-BFHash $criticCatalog.source
        # Bind the sealed capability to the model selected from the verified
        # source catalog.  The native executable/version contract is shared by
        # the allowlisted models, while the model identity remains part of the
        # cache key so a Luna receipt can never be replayed for Astra (or vice
        # versa).
        $binding.critic_capability=Get-BFCodexCriticCapabilityVersion $model
    }
    $bindingHash=Get-BFHash $binding
    $hostRoot=Assert-BFSafePath (Join-Path $State.project_path ('.bsl-flow/hosts/'+$State.task_id+'/'+(Get-BFHash $directory)))
    $scratch=Join-Path $hostRoot 'scratch';$config=Join-Path $hostRoot 'config'
    $permissions=Get-BFExecutionPermissionProfile $State $scratch $config ($Stage -eq 'implement')
    $exitPath=Join-Path $directory 'exit.json';$hostPath=Join-Path $directory 'host-result.json';$resultPath=Join-Path $directory 'model-result.json'
    if(Test-Path -LiteralPath $directory){
        foreach($name in @('binding.json','exit.json','model-result.json','inventory.json','disabled.json','post-inventory.json','mcp-inventory.json','mcp-config-names.json','capability/capability.json')){if(-not(Test-Path -LiteralPath (Join-Path $directory $name))){throw 'BF_BLOCKED: partial Codex dispatch; reconcile the preserved attempt without retry.'}}
        if((Read-BFJson (Join-Path $directory 'binding.json')).sha256 -cne $bindingHash){throw 'BF_BLOCKED: cached Codex binding differs.'}
        $process=Read-BFJson $exitPath
    }else{
        if(Test-Path -LiteralPath $hostRoot){throw 'BF_BLOCKED: unregistered Codex host directory exists.'}
        foreach($path in @($directory,$scratch,$config)){[void][IO.Directory]::CreateDirectory($path)}
        Write-BFJson (Join-Path $directory 'binding.json') ([ordered]@{sha256=$bindingHash;binding=$binding;permission_sha256=Get-BFHash $permissions})
        Copy-Item -LiteralPath $schema -Destination (Join-Path $config 'result.schema.json')
        $overrides=Get-BFProfiledCodexOverrides $permissions (Join-Path $scratch 'logs')
        if($critic){
            Write-BFJson (Join-Path $directory 'critic-catalog-source.json') $criticCatalog.source
            Write-BFJson (Join-Path $directory 'critic-catalog.json') $criticCatalog.catalog
            Write-BFJson (Join-Path $config 'critic-catalog.json') $criticCatalog.catalog
            $overrides+=@(Get-BFCodexCriticOverrides (Join-Path $config 'critic-catalog.json'))
        }
        $workerOverrides=@($overrides)
        $rpc=@{Executable=$profile.executable;WorkingDirectory=$State.worker_path;Cancelled=$Cancelled;MaxOutputBytes=$MaxOutputBytes}
        # config/read resolves configuration only; unlike MCP status, it does not
        # initialize MCP transports. Never persist the returned configuration.
        $configuration=Invoke-BFCodexReadOnlyRpc @rpc -Overrides $overrides -Directory (Join-Path $directory 'mcp-config-rpc') -Method 'config/read' -Params @{cwd=$State.worker_path;includeLayers=$false}
        $mcpNames=@(Get-BFCodexMcpServerNames $configuration);$configuration=$null
        if(-not $critic -and $profile.toolset.name -eq 'unica' -and 'unica' -cin $mcpNames){throw 'BF_BLOCKED: global MCP alias unica collides with the managed registration.'}
        Write-BFJson (Join-Path $directory 'mcp-config-names.json') @{names=$mcpNames}
        $overrides+=@(Get-BFCodexMcpDenyOverrides $mcpNames)
        # Recheck config before any inventory that can start a transport. This
        # detects observed drift; it does not lock concurrent global config edits.
        $rpc.ExpectedMcpServers=$mcpNames
        $response=Invoke-BFCodexReadOnlyRpc @rpc -Overrides $overrides -Directory (Join-Path $directory 'inventory-rpc') -Method 'skills/list' -Params @{cwds=@($State.worker_path);forceReload=$true}
        $inventory=ConvertTo-BFCodexSkillInventory $response $State.worker_path
        Write-BFJson (Join-Path $directory 'inventory.json') @{skills=$inventory}
        if((Get-BFHash $inventory) -cne $profile.codex_skills_sha256){throw 'BF_BLOCKED: discovered Codex skills differ from the registered inventory.'}
        $managedOverrides=$overrides+@('-c',(Get-BFCodexSkillDenyOverride $inventory))
        $workerOverrides+=@('-c',(Get-BFCodexSkillDenyOverride $inventory))
        $response=Invoke-BFCodexReadOnlyRpc @rpc -Overrides $managedOverrides -Directory (Join-Path $directory 'disabled-rpc') -Method 'skills/list' -Params @{cwds=@($State.worker_path);forceReload=$true}
        $disabled=ConvertTo-BFCodexSkillInventory $response $State.worker_path
        Assert-BFCodexSkillDenial $inventory $disabled
        Write-BFJson (Join-Path $directory 'disabled.json') @{skills=$disabled}
        [void](Test-BFExecutionCapability $State (Join-Path $directory 'capability') $scratch $config $permissions ($Stage -eq 'implement'))
        if(-not $critic -and $profile.toolset.name -eq 'unica'){
            $mcpArguments=@('sandbox','-P','bsl_execution','-c',$permissions,'-c','windows.sandbox="elevated"','-C',$State.worker_path,(Join-Path $profile.unica.plugin_root 'bootstrap/bin/win-x64/unica-bootstrap.exe'),'run','--plugin-root',$profile.unica.plugin_root)
            $values=@($mcpArguments|ForEach-Object{ConvertTo-Json -InputObject $_ -Compress}) -join ','
            $tools=@($profile.unica.allowed_tools|ForEach-Object{ConvertTo-Json -InputObject $_ -Compress}) -join ','
            $registration='mcp_servers={unica={enabled=true,command='+(ConvertTo-Json -InputObject $profile.sandbox.executable.Replace('\','/') -Compress)+',args=['+$values+'],env={UNICA_RUNTIME_CACHE_DIR='+(ConvertTo-Json -InputObject $profile.unica.runtime_cache.Replace('\','/') -Compress)+'},enabled_tools=['+$tools+'],startup_timeout_sec=45,tool_timeout_sec=60}}'
            # The whole-table registration replaces preceding dotted CLI entries.
            # Reapply global denials after it; exec ignores global config entirely.
            $managedOverrides+=@('-c',$registration)+@(Get-BFCodexMcpDenyOverrides $mcpNames)
            $workerOverrides+=@('-c',$registration)
        }
        $mcpRpc=$rpc.Clone()
        if(-not $critic -and $profile.toolset.name -eq 'unica'){$mcpRpc.ExpectedMcpServers=@($mcpNames)+@('unica');$mcpRpc.EnabledMcpServers=@('unica')}
        $mcp=Invoke-BFCodexReadOnlyRpc @mcpRpc -Overrides $managedOverrides -Directory (Join-Path $directory 'mcp-rpc') -Method 'mcpServerStatus/list' -Params @{limit=100;detail='toolsAndAuthOnly'}
        if($null -ne (Get-BFValue $mcp 'nextCursor')){throw 'BF_BLOCKED: incomplete MCP inventory.'}
        $actual=@($mcp.data|ForEach-Object{ $server=$_; @($server.tools.PSObject.Properties|ForEach-Object{[ordered]@{server=$server.name;name=$_.Value.name}}) })
        $expected=if(-not $critic -and $profile.toolset.name -eq 'unica'){@($profile.unica.allowed_tools|ForEach-Object{[ordered]@{server='unica';name=$_}})}else{@()}
        if((Get-BFHash @($actual|Sort-Object server,name)) -cne (Get-BFHash @($expected|Sort-Object server,name))){throw 'BF_BLOCKED: Codex MCP tools differ from the exact registered allowlist.'}
        Write-BFJson (Join-Path $directory 'mcp-inventory.json') @{tools=$actual}
        $nativeResult=Join-Path $scratch 'model-result.json'
        $ephemeral=if($requireObserved){@()}else{@('--ephemeral')}
        $arguments=@('exec','--ignore-user-config','--ignore-rules')+$ephemeral+@('--skip-git-repo-check','--model',$model,'-c',('model_reasoning_effort='+(ConvertTo-Json -InputObject $effort -Compress)))+$workerOverrides+@('--json','--output-schema',(Join-Path $config 'result.schema.json'),'--output-last-message',$nativeResult,'--cd',$State.worker_path,'-')
        $configuration=Invoke-BFCodexReadOnlyRpc @rpc -Overrides $overrides -Directory (Join-Path $directory 'pre-dispatch-config-rpc') -Method 'config/read' -Params @{cwd=$State.worker_path;includeLayers=$false}
        Assert-BFCodexMcpConfiguration $configuration $mcpNames;$configuration=$null
        if((Get-BFHash (Get-BFExecutionDependencies $State)) -cne (Get-BFHash $dependencies)){throw 'BF_BLOCKED: managed inputs changed before dispatch.'}
        if($critic -and (Get-BFFileHash (Join-Path $config 'critic-catalog.json')) -cne $binding.critic_catalog_sha256){throw 'BF_BLOCKED: critic host catalog changed before dispatch.'}
        $environment=@{TEMP=$scratch;TMP=$scratch;GIT_OPTIONAL_LOCKS='0'}
        if($env:CODEX_HOME){$environment.CODEX_HOME=$env:CODEX_HOME}
        $process=Invoke-BFProcess -Executable $profile.executable -Arguments $arguments -WorkingDirectory $State.worker_path -InputText $inputText -OutputDirectory $directory -TimeoutSeconds ([int](Get-BFValue $State.request 'timeout_seconds' 1800)) -Cancelled $Cancelled -Environment $environment -CleanEnvironment -MaxOutputBytes $MaxOutputBytes
        if($process.stop_reason -or $process.exit_code -ne 0){throw 'BF_BLOCKED: Codex attempt failed; preserve sources and reconcile without retry.'}
        if(-not(Test-Path -LiteralPath $nativeResult)){throw 'BF_BLOCKED: Codex omitted the result artifact.'}
        Copy-Item -LiteralPath $nativeResult -Destination $resultPath
        $response=Invoke-BFCodexReadOnlyRpc @rpc -Overrides $overrides -Directory (Join-Path $directory 'post-inventory-rpc') -Method 'skills/list' -Params @{cwds=@($State.worker_path);forceReload=$true}
        Write-BFJson (Join-Path $directory 'post-inventory.json') @{skills=(ConvertTo-BFCodexSkillInventory $response $State.worker_path)}
    }
    foreach($name in @('inventory.json','post-inventory.json')){if((Get-BFHash (Read-BFJson (Join-Path $directory $name)).skills) -cne $profile.codex_skills_sha256){throw 'BF_BLOCKED: Codex skill inventory changed during execution.'}}
    foreach($skill in (Read-BFJson (Join-Path $directory 'inventory.json')).skills){if((Get-BFFileHash (Assert-BFSafePath $skill.path)) -cne $skill.sha256){throw 'BF_BLOCKED: cached Codex skill instructions changed.'}}
    Assert-BFCodexSkillDenial (Read-BFJson (Join-Path $directory 'inventory.json')).skills (Read-BFJson (Join-Path $directory 'disabled.json')).skills
    if($process.stdout -cne (Join-Path $directory 'stdout.txt') -or $process.executable -cne $profile.executable -or $process.stop_reason){throw 'BF_BLOCKED: invalid Codex process receipt.'}
    if($critic){
        foreach($catalogPath in @((Join-Path $directory 'critic-catalog.json'),(Join-Path $config 'critic-catalog.json'))){if((Get-BFFileHash $catalogPath) -cne $binding.critic_catalog_sha256){throw 'BF_BLOCKED: retained critic catalog bytes changed.'}}
        if((Get-BFFileHash (Join-Path $directory 'critic-catalog-source.json')) -cne $binding.critic_catalog_source_sha256){throw 'BF_BLOCKED: retained critic source catalog bytes changed.'}
    }
    $allowed=if(-not $critic -and $profile.toolset.name -eq 'unica'){@($profile.unica.allowed_tools)}else{@()}
    $expectedMcp=@($allowed|ForEach-Object{[ordered]@{server='unica';name=$_}})
    if((Get-BFHash @((Read-BFJson (Join-Path $directory 'mcp-inventory.json')).tools|Sort-Object server,name)) -cne (Get-BFHash @($expectedMcp|Sort-Object server,name))){throw 'BF_BLOCKED: cached MCP inventory differs from the exact allowlist.'}
    $parsed=Read-BFProfiledCodexEvents $process.stdout $process.exit_code $allowed -NoTools:$critic
    $result=Read-BFJson $resultPath
    Assert-BFFields $result @('schema_version','status','summary','payload_json') @() 'worker_result'
    if($result.schema_version -ne 1 -or $result.status -notin @('completed','needs_input','blocked','failed')){throw 'BF_BLOCKED: invalid Codex result.'}
    Assert-BFText $result.summary 'worker summary'
    if($result.payload_json -isnot [string]){throw 'BF_BLOCKED: Codex payload_json must be a JSON string.'}
    try{$null=ConvertFrom-Json $result.payload_json -Depth 100 -ErrorAction Stop}catch{throw 'BF_BLOCKED: invalid Codex payload JSON.'}
    if((Get-BFHash (ConvertFrom-Json $parsed.final -Depth 100)) -cne (Get-BFHash $result)){throw 'BF_BLOCKED: Codex result differs from raw session evidence.'}
    if((Get-BFHash (Get-BFExecutionDependencies $State)) -cne (Get-BFHash $dependencies)){throw 'BF_BLOCKED: managed inputs changed during execution.'}
    # Controller-observed identity comes from the rollout session file the host
    # persisted for this exact session. Non-critic and legacy critic ephemeral
    # workers may have no persisted rollout; the fallback's strict request keeps
    # the provenance contract and blocks when its rollout is absent.
    $observed=if($requireObserved){Get-BFObservedModelEffort -SessionId $parsed.session_id}else{[ordered]@{observed_model=$null;observed_effort=$null;rollout_path=$null;session_id=$parsed.session_id;missing=$true}}
    if ($requireObserved -and [bool]$observed.missing) { throw 'BF_BLOCKED: required host identity is missing from the Codex rollout.' }
    if($requireObserved){
        if([string]::IsNullOrWhiteSpace([string]$observed.turn_id)){throw 'BF_BLOCKED: required host identity has no persisted rollout turn.'}
        $selected=Get-BFObservedModelEffort -SessionId $parsed.session_id -TurnId ([string]$observed.turn_id)
        if([string]$selected.rollout_path -cne [string]$observed.rollout_path -or [string]$selected.observed_model -cne [string]$observed.observed_model -or [string]$selected.observed_effort -cne [string]$observed.observed_effort){throw 'BF_BLOCKED: persisted rollout turn identity changed while reading the strict receipt.'}
        if([string]$observed.observed_model -cne [string]$model -or [string]$observed.observed_effort -cne [string]$effort){throw 'BF_BLOCKED: observed rollout model/effort differs from the strict request.'}
    }
    $metadata=[ordered]@{session_id=$parsed.session_id;requested_model=$model;requested_effort=$effort;observed_model=$observed.observed_model;observed_effort=$observed.observed_effort;usage=$parsed.usage;usage_source=$process.stdout;binding_sha256=$bindingHash;stdout_sha256=Get-BFFileHash $process.stdout;result_sha256=Get-BFFileHash $resultPath;capability_sha256=Get-BFFileHash (Join-Path $directory 'capability/capability.json');mcp_config_names_sha256=Get-BFFileHash (Join-Path $directory 'mcp-config-names.json')}
    if ($requireObserved) {
        $metadata.rollout_path=$observed.rollout_path
        $metadata.turn_id=$observed.turn_id
    }
    if(Test-Path -LiteralPath $hostPath){if((Get-BFHash (Read-BFJson $hostPath)) -cne (Get-BFHash $metadata)){throw 'BF_BLOCKED: cached Codex receipt differs from raw evidence.'}}else{Write-BFJson $hostPath $metadata}
    return $result
}
