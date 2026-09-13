#Requires -Version 7.0
[CmdletBinding()]param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$core=Join-Path $PackageRoot 'global/skills/1c-task'
foreach($file in @('scripts/Task.Storage.ps1','scripts/Task.Contracts.ps1','scripts/Task.Process.ps1','scripts/Task.Gates.ps1','scripts/Task.Engine.ps1','adapters/Codex.ps1','adapters/ProfiledCodex.ps1','scripts/Task.ManagedReview.ps1')){. (Join-Path $core $file)}
    $checks=0
function Assert-C([bool]$Condition,[string]$Message){if(-not $Condition){throw $Message};$script:checks++}
function Failure-C([scriptblock]$Action){try{& $Action|Out-Null;return ''}catch{return $_.Exception.Message}}
$root=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-codex-'+[guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($root)
try{
    $skill=Join-Path $root 'SKILL.md';[IO.File]::WriteAllText($skill,'synthetic skill')
    $response=[pscustomobject]@{data=@([pscustomobject]@{cwd=$root;errors=@();skills=@([pscustomobject]@{name='fixture';path=$skill;scope='user';enabled=$true})})}
    $inventory=ConvertTo-BFCodexSkillInventory $response $root
    Assert-C ($inventory.Count -eq 1 -and $inventory[0].sha256 -eq (Get-BFFileHash $skill)) 'Inventory content identity missing.'
    $originalHash=Get-BFHash $inventory
    $deny=Get-BFCodexSkillDenyOverride $inventory
    Assert-C ($deny.Contains('enabled=false') -and $deny.Contains('/SKILL.md')) 'Skill denylist is not exact.'
    $response.data[0].skills[0].enabled=$false
    $disabled=ConvertTo-BFCodexSkillInventory $response $root
    Assert-C ((Failure-C {Assert-BFCodexSkillDenial $inventory $disabled}) -eq '') 'Disabled skill proof rejected.'
    Assert-C ((Failure-C {Assert-BFCodexSkillDenial $inventory $inventory}) -match 'did not disable') 'Enabled skill accepted.'
    [IO.File]::WriteAllText($skill,'mutated instruction')
    Assert-C ((Failure-C {Assert-BFCodexSkillDenial $inventory (ConvertTo-BFCodexSkillInventory $response $root)}) -match 'did not disable') 'Skill byte drift accepted.'
    $response.data[0].skills += $response.data[0].skills[0]
    Assert-C ((Failure-C {ConvertTo-BFCodexSkillInventory $response $root}) -match 'ambiguous') 'Duplicate skill identity accepted.'
    $response.data[0].errors=@('fixture error')
    Assert-C ((Failure-C {ConvertTo-BFCodexSkillInventory $response $root}) -match 'incomplete') 'Inventory errors accepted.'
    # Model-cache provenance is allowlisted independently of the execution
    # profile.  Astra's raw cache declarations include experimental and
    # multi-agent metadata, but the sealed catalog must erase every tool
    # definition before the native critic starts.
    $astraSource=[ordered]@{
        source_cache_path=(Join-Path $root 'astra-model-cache.json'); source_cache_sha256=('a'*64)
        model=[ordered]@{
            slug='gpt-6-astra'; use_responses_lite=$true; shell_type='unified_exec'; apply_patch_tool_type='freeform'; tool_mode='code_mode_only'
            experimental_supported_tools=@('send_user_message_async','clock'); multi_agent_version='v2'; multi_agent_reasoning_effort='xhigh'
            supported_reasoning_levels=@([ordered]@{effort='low'},[ordered]@{effort='medium'},[ordered]@{effort='high'},[ordered]@{effort='xhigh'},[ordered]@{effort='max'},[ordered]@{effort='ultra'})
            web_search_tool_type='native'; supports_search_tool=$true; node_repl_disabled=$false; node_repl_auto_review_required=$true
            include_skills_usage_instructions=$true; include_plugin_usage_instructions=$true; include_apps_usage_instructions=$true; base_instructions='astra fixture'
        }
    }
    $astraCatalog=ConvertTo-BFCodexCriticCatalog -Source $astraSource -ExpectedModel 'gpt-6-astra' -ExpectedEffort 'high'
    $sealedAstra=$astraCatalog.catalog.models[0]
    Assert-C ($astraCatalog.source.model.experimental_supported_tools.Count -eq 2 -and $sealedAstra.experimental_supported_tools.Count -eq 0 -and $null -eq $sealedAstra.multi_agent_version -and $null -eq $sealedAstra.multi_agent_reasoning_effort) 'Astra raw model declarations were not reduced to an empty tool catalog.'
    Assert-C ($sealedAstra.shell_type -ceq 'disabled' -and $null -eq $sealedAstra.apply_patch_tool_type -and $sealedAstra.tool_mode -ceq 'direct' -and $sealedAstra.web_search_tool_type -eq $null -and $sealedAstra.supports_search_tool -eq $false -and $sealedAstra.node_repl_disabled -eq $true) 'Astra sealed catalog retained a model-owned tool switch.'
    Assert-C ((Get-BFCodexCriticCapabilityVersion 'gpt-6-astra') -ceq 'codex-0.154.0-gpt-6-astra-direct-empty-tools-v1') 'Astra capability identity is not model-specific.'
    $missingAstra=ConvertFrom-Json (Get-BFCanonicalJson $astraSource) -Depth 100
    $missingAstra.model.multi_agent_version=$null
    Assert-C ((Failure-C {ConvertTo-BFCodexCriticCatalog -Source $missingAstra -ExpectedModel 'gpt-6-astra' -ExpectedEffort 'high'}) -match 'multi_agent_version') 'Partial Astra multi-agent metadata was accepted.'
    Assert-C ((Failure-C {ConvertTo-BFCodexCriticCatalog -Source $astraSource -ExpectedModel 'gpt-5.6-luna' -ExpectedEffort 'high'}) -match 'model metadata|experimental tool metadata|allowlisted') 'Astra source/model identity drift was accepted.'
    $astraSource.model.experimental_supported_tools=@('send_user_message_async','clock','malicious_tool')
    Assert-C ((Failure-C {ConvertTo-BFCodexCriticCatalog -Source $astraSource -ExpectedModel 'gpt-6-astra' -ExpectedEffort 'high'}) -match 'experimental tool metadata') 'Unknown Astra experimental declaration was accepted.'
    $configuration=[pscustomobject]@{config=[pscustomobject]@{mcp_servers=[pscustomobject]@{'fixture.server'=[pscustomobject]@{enabled=$false;command='sensitive fixture value'}}}}
    $names=@(Get-BFCodexMcpServerNames $configuration)
    Assert-C ($names.Count -eq 1 -and $names[0] -ceq 'fixture.server') 'MCP name projection lost identity.'
    $emptyConfiguration=[pscustomobject]@{config=[pscustomobject]@{mcp_servers=[pscustomobject]@{}}}
    Assert-C (@(Get-BFCodexMcpServerNames $emptyConfiguration).Count -eq 0) 'Empty MCP configuration projected a phantom server name.'
    Assert-C ((Failure-C {Get-BFCodexMcpDenyOverrides $names}) -match 'cannot be safely addressed') 'Unverified dotted MCP name escaping was accepted.'
    Assert-C ((Get-BFCodexMcpDenyOverrides @('fixture-server_1'))[1] -ceq 'mcp_servers.fixture-server_1.enabled=false') 'MCP CLI override did not preserve the bare server key.'
    Assert-C ((Failure-C {Assert-BFCodexMcpConfiguration $configuration $names}) -eq '') 'Exact disabled MCP configuration rejected.'
    Assert-C ((Failure-C {Assert-BFCodexMcpConfiguration $configuration @()}) -match 'inventory changed') 'New global MCP server accepted.'
    $configuration.config.mcp_servers.'fixture.server'.enabled=$true
    Assert-C ((Failure-C {Assert-BFCodexMcpConfiguration $configuration $names}) -match 'enablement differs') 'Enabled unwanted MCP server accepted.'
    $diagnostic=Join-Path $root 'rpc-diagnostic';[void][IO.Directory]::CreateDirectory($diagnostic)
    $secretFixture='{"sensitive-fixture":"DO_NOT_PERSIST",'
    Assert-C ((Failure-C {ConvertFrom-BFCodexRpcLine $secretFixture 'config/read' 2 $diagnostic}) -match 'malformed Codex RPC') 'Malformed config response was accepted.'
    Assert-C (@(Get-ChildItem -LiteralPath $diagnostic).Count -eq 0) 'Config response parsing persisted diagnostic content.'
    Assert-C ((Failure-C {ConvertFrom-BFCodexRpcLine $secretFixture 'mcpServerStatus/list' 3 $diagnostic}) -match 'malformed Codex RPC') 'Malformed MCP response was accepted.'
    $failure=Read-BFJson (Join-Path $diagnostic 'malformed-response.json')
    Assert-C ($failure.line_sha256 -ceq (Get-BFHash $secretFixture) -and -not ([IO.File]::ReadAllText((Join-Path $diagnostic 'malformed-response.json')).Contains('DO_NOT_PERSIST'))) 'MCP parsing diagnostic leaked input or lost its identity.'
    & {
        # Deterministic stopped-pipe fixture: Close fails, but ownership cleanup
        # and disposal must still run without masking the primary protocol error.
        $script:cleanupCalls=@()
        function Stop-BFOwnedProcess {param($Identity);$script:cleanupCalls+='stop'}
        $pipe=[pscustomobject]@{};$pipe|Add-Member ScriptMethod Close {throw [IO.IOException]::new('stopped pipe fixture')}
        $process=[pscustomobject]@{StandardInput=$pipe}
        $process|Add-Member ScriptMethod WaitForExit {param($Timeout);$script:cleanupCalls+='wait';return $false}
        $process|Add-Member ScriptMethod Dispose {$script:cleanupCalls+='process-dispose'}
        $stream=[pscustomobject]@{};$stream|Add-Member ScriptMethod Dispose {$script:cleanupCalls+='stream-dispose'}
        $failure=Failure-C {try{throw 'primary RPC fixture'}finally{Close-BFCodexRpcProcess $process @{pid=0} $null $stream $true}}
        Assert-C ($failure -ceq 'primary RPC fixture') 'Broken pipe cleanup masked the primary error.'
        Assert-C (($script:cleanupCalls -join ',') -ceq 'wait,stop,stream-dispose,process-dispose') 'Broken pipe skipped owned termination or disposal.'
        Assert-C ((Failure-C {Close-BFCodexRpcProcess $process @{pid=0} $null $stream $false}) -match 'stopped pipe fixture') 'Cleanup failure without a primary error was swallowed.'
    }
    $path=Join-Path $root 'events.jsonl'
    $fixtureSessionId='11111111-1111-4111-8111-111111111111'
    $events=@(@{type='thread.started';thread_id=$fixtureSessionId},@{type='turn.started'},@{type='item.completed';item=@{id='answer';type='agent_message';text='{"schema_version":1,"status":"completed","summary":"fixture","payload_json":"{}"}'}},@{type='turn.completed';usage=@{input_tokens=4;cached_input_tokens=0;output_tokens=2}})
    function Write-EventsC($Value){[IO.File]::WriteAllLines($path,@($Value|ForEach-Object{ConvertTo-Json $_ -Depth 20 -Compress}))}
    Write-EventsC $events
    $parsed=Read-BFProfiledCodexEvents $path 0 @()
    Assert-C ($parsed.session_id -eq $fixtureSessionId -and $parsed.usage.output_tokens -eq 2) 'Exact raw session/usage lost.'
    $events[-1].usage.cache_write_input_tokens=0;$events[-1].usage.reasoning_output_tokens=1
    Write-EventsC $events
    Assert-C ((Read-BFProfiledCodexEvents $path 0 @()).usage.reasoning_output_tokens -eq 1) 'Observed optional usage counters were rejected.'
    foreach($counter in @('cache_write_input_tokens','reasoning_output_tokens')){
        foreach($badCounter in @(-1,0.5,$true,'1',$null)){
            $events[-1].usage[$counter]=$badCounter;Write-EventsC $events
            Assert-C ((Failure-C {Read-BFProfiledCodexEvents $path 0 @()}) -match 'invalid Codex usage counters') 'Invalid optional usage counter accepted.'
        }
        $events[-1].usage[$counter]=0
    }
    $invalidStreams=@()
    $invalidStreams+=,@($events+@($events[-1]))
    $invalidStreams+=,@(@($events[0],$events[0])+$events[1..3])
    $invalidStreams+=,@($events[0..1]+@($events[3]))
    $invalidStreams+=,@($events[0..2])
    $invalidStreams+=,@($events[0..1]+@(@{type='turn.failed'}))
    $invalidStreams+=,@($events[0..1]+@(@{type='item.completed';item=@{id='tool';type='mcp_tool_call';server='unica';tool='unica.runtime.job.start'}})+$events[2..3])
    foreach($bad in $invalidStreams){
        Write-EventsC $bad
        Assert-C ((Failure-C {Read-BFProfiledCodexEvents $path 0 @('unica.meta.validate')}) -match '^BF_BLOCKED:') 'Malformed, incomplete or unauthorized stream accepted.'
    }
    [IO.File]::WriteAllText($path,'broken json')
    Assert-C ((Failure-C {Read-BFProfiledCodexEvents $path 0 @()}) -match 'malformed') 'Malformed JSONL accepted.'
    Write-EventsC $events
    Assert-C ((Failure-C {Read-BFProfiledCodexEvents $path 1 @()}) -match 'failed') 'Nonzero exit accepted.'
    Write-EventsC @($events[0..1]+@(@{type='item.started';item=@{id='forbidden';type='command_execution'}})+$events[2..3])
    Assert-C ((Failure-C {Read-BFProfiledCodexEvents $path 0 @() -NoTools}) -match 'critic emitted a tool event') 'Sealed critic tool event was accepted.'
    & {
        function Get-BFExecutionDependencies {param($State);@{fixture='dependency'}}
        function Assert-BFWorkerConfiguration {}
        function Invoke-BFProcess {throw 'Unexpected model dispatch.'}
        $state=[pscustomobject]@{worker_path=$root;request=[pscustomobject]@{execution_profile=[pscustomobject]@{provider='codex';sandbox=[pscustomobject]@{executable=$skill}};models=[pscustomobject]@{reviewer='unverified-model';reviewer_effort='medium';worker='other';worker_effort='high'}}}
        Assert-C ((Failure-C {Invoke-BFProfiledCodexWorker $state 'spec_review' 'attached only' (Join-Path $root 'review') $skill {$false}}) -match 'no-tools critic capability') 'Unverified critic dispatched.'
        Assert-C (-not(Test-Path (Join-Path $root 'review'))) 'Blocked critic created an attempt.'
    }
    & {
        # Exercise the complete adapter/cache path; only native process boundaries are fixtures.
        # The controller must observe resolved model/effort from the rollout session file,
        # so this fixture also provisions an isolated CODEX_HOME with a real rollout record
        # whose session id matches the synthetic JSONL thread id.
        $fixtureSessionId='11111111-1111-4111-8111-111111111111'
        $fixtureCodexHome=Join-Path $root 'codex-home'
        $fixtureSessionDir=Join-Path $fixtureCodexHome ('sessions/{0:D4}/{1:D2}/{2:D2}' -f [DateTime]::UtcNow.Year, [DateTime]::UtcNow.Month, [DateTime]::UtcNow.Day)
        [void][IO.Directory]::CreateDirectory($fixtureSessionDir)
        $rolloutLines=@(
            (@{timestamp='2026-09-12T00:00:00Z';ordinal=0;type='session_meta';payload=[ordered]@{session_id=$fixtureSessionId;id=$fixtureSessionId;originator='codex_exec';cli_version='0.154.0'}} | ConvertTo-Json -Depth 10 -Compress),
            (@{timestamp='2026-09-12T00:00:01Z';ordinal=1;type='turn_context';payload=[ordered]@{turn_id='fixture-turn';model='gpt-5.6-sol';effort='medium';approval_policy='never'}} | ConvertTo-Json -Depth 10 -Compress)
        )
        $rolloutPath=Join-Path $fixtureSessionDir ('rollout-2026-09-12T00-00-00-{0}.jsonl' -f $fixtureSessionId)
        [IO.File]::WriteAllLines($rolloutPath, $rolloutLines)
        $env:CODEX_HOME=$fixtureCodexHome
        $observed=Get-BFObservedModelEffort -SessionId $fixtureSessionId
        Assert-C ($observed.observed_model -eq 'gpt-5.6-sol' -and $observed.observed_effort -eq 'medium') 'Rollout observed identity extraction failed.'
        # Real session_meta records may expose only id, and a historical rollout
        # must remain discoverable after the short-lived current-day window.
        $legacySessionId='01234567-89ab-4cde-8123-0123456789ab'
        $legacySessionDir=Join-Path $fixtureCodexHome 'sessions/2020/01/01';[void][IO.Directory]::CreateDirectory($legacySessionDir)
        $legacyRolloutPath=Join-Path $legacySessionDir ('rollout-2020-01-01T00-00-00-{0}.jsonl' -f $legacySessionId)
        [IO.File]::WriteAllLines($legacyRolloutPath, @(
            (@{timestamp='2020-01-01T00:00:00Z';ordinal=0;type='session_meta';payload=[ordered]@{id=$legacySessionId;originator='codex_exec';cli_version='0.154.0'}}|ConvertTo-Json -Depth 10 -Compress),
            (@{timestamp='2020-01-01T00:00:01Z';ordinal=1;type='turn_context';payload=[ordered]@{model='legacy-model';effort='low'}}|ConvertTo-Json -Depth 10 -Compress)
        ))
        $legacyObserved=Get-BFObservedModelEffort -SessionId $legacySessionId
        Assert-C ($legacyObserved.observed_model -eq 'legacy-model' -and $legacyObserved.observed_effort -eq 'low' -and $null -eq $legacyObserved.turn_id) 'Historical id-only rollout metadata was not read safely.'
        Assert-C ((Failure-C {Get-BFObservedModelEffort -SessionId '22222222-2222-4222-8222-222222222222'}) -match 'rollout session file not found') 'Missing rollout accepted.'
        $script:modelCalls=0;$script:rpcCalls=0
        function Get-BFExecutionDependencies {param($State);@{profile=$State.request.execution_profile;models=$State.request.models}}
        function Assert-BFWorkerConfiguration {}
        function Get-BFToolsetPrompt {param($State);' fixed fixture catalog'}
        function Test-BFExecutionCapability {param($State,$Directory);[void][IO.Directory]::CreateDirectory($Directory);Write-BFJson (Join-Path $Directory 'capability.json') @{fixture=$true}}
        function Invoke-BFCodexReadOnlyRpc {
            param($Executable,$Overrides,$WorkingDirectory,$Directory,$Method,$Params,$Cancelled,$TimeoutSeconds,$MaxOutputBytes,$ExpectedMcpServers,$EnabledMcpServers)
            $script:rpcCalls++
            if($Method -eq 'config/read'){return [pscustomobject]@{config=[pscustomobject]@{mcp_servers=[pscustomobject]@{'fixture-server'=[pscustomobject]@{enabled=(-not ($Overrides -contains 'mcp_servers.fixture-server.enabled=false'))}}}}}
            if($null -ne $Directory){Assert-C (@($ExpectedMcpServers).Count -ge 1 -and $ExpectedMcpServers[0] -ceq 'fixture-server') 'Inventory RPC lost its same-process MCP guard.'}
            if($Method -eq 'mcpServerStatus/list'){
                if('unica' -cin $EnabledMcpServers){
                    $registration=@($Overrides|Where-Object{$_ -like 'mcp_servers={unica=*'})
                    Assert-C ($registration.Count -eq 1 -and [array]::LastIndexOf($Overrides,'mcp_servers.fixture-server.enabled=false') -gt [array]::IndexOf($Overrides,$registration[0])) 'Unica registration reset the global MCP denials.'
                    return [pscustomobject]@{data=@([pscustomobject]@{name='unica';tools=[pscustomobject]@{fixture=[pscustomobject]@{name='unica.meta.validate'}}});nextCursor=$null}
                }
                return [pscustomobject]@{data=@();nextCursor=$null}
            }
            return [pscustomobject]@{data=@([pscustomobject]@{cwd=$WorkingDirectory;errors=@();skills=@([pscustomobject]@{name='fixture';path=$skill;scope='user';enabled=(-not (@($Overrides|Where-Object{$_ -like 'skills.config=*'}).Count -gt 0))})})}
        }
        function Invoke-BFProcess {
            param($Executable,$Arguments,$WorkingDirectory,$InputText,$OutputDirectory,$TimeoutSeconds,$Cancelled,$Environment,[switch]$CleanEnvironment,$MaxOutputBytes)
            $script:modelCalls++
            Assert-C (($Arguments -contains '--ignore-user-config') -and -not ($Arguments -contains 'mcp_servers.fixture-server.enabled=false')) 'Worker inherited partial global MCP definitions.'
            Assert-C (($Arguments -contains 'agents.enabled=false') -and ($Arguments -contains 'multi_agent_v2')) 'Worker did not override model-catalog multi-agent enablement.'
            if($InputText -ceq 'attached fixture only'){
                Assert-C (-not @($Arguments|Where-Object{$_ -like 'mcp_servers={unica=*'}).Count) 'Critic inherited Unica registration.'
                foreach($setting in @('project_doc_max_bytes=0','tools.update_plan.enabled=false','tools.experimental_request_user_input.enabled=false','features.shell_tool=false','features.view_image=false','features.deferred_executor=false','features.request_permissions_tool=false','features.token_budget=false','features.current_time_reminder=false','features.sleep_tool=false','features.tool_suggest=false','features.image_generation=false','features.goals=false')){Assert-C ($Arguments -contains $setting) 'Critic lost a proven no-tools config setting.'}
                $catalogSetting=@($Arguments|Where-Object{$_ -like 'model_catalog_json=*'})
                Assert-C ($catalogSetting.Count -eq 1) 'Critic omitted its startup model catalog.'
            }
            $modelResult=ConvertFrom-Json $events[2].item.text
            Write-BFJson $Arguments[([array]::IndexOf($Arguments,'--output-last-message')+1)] $modelResult
            $stdout=Join-Path $OutputDirectory 'stdout.txt'
            [IO.File]::WriteAllLines($stdout,@($events|ForEach-Object{ConvertTo-Json $_ -Depth 20 -Compress}))
            $receipt=[ordered]@{exit_code=0;stop_reason=$null;stdout=$stdout;stderr=(Join-Path $OutputDirectory 'stderr.txt');executable=$Executable}
            Write-BFJson (Join-Path $OutputDirectory 'exit.json') $receipt
            return $receipt
        }
        $project=Join-Path $root 'project';$worker=Join-Path $root 'worker';$toolset=Join-Path $root 'toolset'
        foreach($dir in @($project,$worker,$toolset)){[void][IO.Directory]::CreateDirectory($dir)}
        $null = Invoke-BFGit $project @('init')
        $inventory=ConvertTo-BFCodexSkillInventory (Invoke-BFCodexReadOnlyRpc -WorkingDirectory $worker -Method 'skills/list' -Overrides @()) $worker
        $profile=[pscustomobject]@{provider='codex';executable=$skill;executable_sha256=(Get-BFFileHash $skill);sandbox=[pscustomobject]@{executable=$skill;sha256=(Get-BFFileHash $skill)};toolset=[pscustomobject]@{name='cc-1c-skills';root=$toolset;sha256=('a'*64)};runtime=[pscustomobject]@{executable=$skill;sha256=(Get-BFFileHash $skill);version='3.12.14';packages=@([pscustomobject]@{name='lxml';version='6.1.1'})};codex_skills_sha256=(Get-BFHash $inventory);denied_read_roots=@((Join-Path $root 'private'))}
        $state=[pscustomobject]@{project_path=$project;worker_path=$worker;task_id='fixture';request=[pscustomobject]@{execution_profile=$profile;models=[pscustomobject]@{worker='gpt-5.6-luna';worker_effort='medium';reviewer='gpt-5.6-luna';reviewer_effort='high'}}}
        $directory=Join-Path $project '.bsl-flow/tasks/worker'
        $result=Invoke-BFProfiledCodexWorker $state 'inspect' 'fixture prompt' $directory $skill {$false}
        Assert-C ($result.status -eq 'completed' -and $script:modelCalls -eq 1) 'Adapter fixture did not complete exactly once.'
        $rpcCount=$script:rpcCalls
        $result=Invoke-BFProfiledCodexWorker $state 'inspect' 'fixture prompt' $directory $skill {$false}
        Assert-C ($result.status -eq 'completed' -and $script:modelCalls -eq 1 -and $script:rpcCalls -eq $rpcCount) 'Completed cache redispatched native work.'
        Assert-C ((Failure-C {Invoke-BFProfiledCodexWorker $state 'inspect' 'changed prompt' $directory $skill {$false}}) -match 'binding differs') 'Changed prompt reused a cache.'
        $partial=Join-Path $project '.bsl-flow/tasks/partial';[void][IO.Directory]::CreateDirectory($partial)
        Assert-C ((Failure-C {Invoke-BFProfiledCodexWorker $state 'inspect' 'fixture prompt' $partial $skill {$false}}) -match 'partial Codex') 'Partial attempt was retried.'
        [IO.File]::WriteAllText((Join-Path $directory 'model-result.json'),'{"schema_version":1,"status":"completed","summary":"tampered","payload_json":"{}"}')
        Assert-C ((Failure-C {Invoke-BFProfiledCodexWorker $state 'inspect' 'fixture prompt' $directory $skill {$false}}) -match 'differs from raw') 'Tampered cached result accepted.'
        Assert-C ($script:modelCalls -eq 1) 'Rejected cache caused another model request.'
        $profile.toolset.name='unica'
        $profile|Add-Member unica ([pscustomobject]@{plugin_root=(Join-Path $root 'plugin');runtime_cache=(Join-Path $root 'runtime');allowed_tools=@('unica.meta.validate')})
        $unicaResult=Invoke-BFProfiledCodexWorker $state 'inspect' 'fixture prompt' (Join-Path $project '.bsl-flow/tasks/unica') $skill {$false}
        Assert-C ($unicaResult.status -eq 'completed' -and $script:modelCalls -eq 2) 'Unica registration fixture did not complete.'
        function Get-BFToolsetPrompt {throw 'Critic must not load a toolset prompt.'}
        function Get-BFCodexCachedCriticModel {
            return @{source_cache_path=(Join-Path $root 'synthetic-model-cache.json');source_cache_sha256=('b'*64);model=[pscustomobject]@{slug='gpt-5.6-luna';use_responses_lite=$true;shell_type='unified_exec';apply_patch_tool_type='freeform';tool_mode='code_mode_only';experimental_supported_tools=@();base_instructions='preserved synthetic model instructions';default_reasoning_level='medium';comp_hash='preserved fixture'}}
        }
        $profile.executable_sha256='be96b992178b1e467c225800da0d65f2c86d5eba1ef0b14632f65db381cbdfde'
        $criticDirectory=Join-Path $project '.bsl-flow/tasks/critic'
        $criticResult=Invoke-BFProfiledCodexWorker $state 'spec_review' 'attached fixture only' $criticDirectory $skill {$false}
        Assert-C ($criticResult.status -eq 'completed' -and $script:modelCalls -eq 3) 'Sealed critic fixture did not complete.'
        $retained=Read-BFJson (Join-Path $criticDirectory 'critic-catalog.json')
        Assert-C ($retained.models[0].base_instructions -ceq 'preserved synthetic model instructions' -and $retained.models[0].comp_hash -ceq 'preserved fixture' -and $retained.models[0].tool_mode -ceq 'direct' -and $null -eq $retained.models[0].apply_patch_tool_type) 'Critic changed non-tool metadata or retained patch tools.'
        function Get-BFCodexCachedCriticModel {throw 'Completed critic must use the retained catalog, not a refreshed global cache.'}
        $criticResult=Invoke-BFProfiledCodexWorker $state 'spec_review' 'attached fixture only' $criticDirectory $skill {$false}
        Assert-C ($criticResult.status -eq 'completed' -and $script:modelCalls -eq 3) 'Completed critic invalidated its retained catalog or redispatched.'
        [IO.File]::AppendAllText((Join-Path $criticDirectory 'critic-catalog.json'),' ')
        Assert-C ((Failure-C {Invoke-BFProfiledCodexWorker $state 'spec_review' 'attached fixture only' $criticDirectory $skill {$false}}) -match 'catalog bytes changed') 'Critic accepted changed catalog bytes.'

        # The public managed-review factory is commonly loaded in a short-lived
        # scope by the task runner. Verify that its returned fallback callback can
        # still run the real profiled worker after that loader scope has exited.
        # Only the native process/RPC boundaries are deterministic fakes here; the
        # worker binding, guarded inventories, sealed critic catalog, raw JSONL
        # parser and rollout identity receipt remain the packaged implementations.
        $adapterProject=Join-Path $root 'adapter-project'
        # Keep the worker outside the user's profile tree: the real
        # Assert-BFWorkerConfiguration intentionally rejects inherited
        # %USERPROFILE%/.codex configuration while walking its parents.
        $adapterWorker=Split-Path $PSScriptRoot -Parent
        foreach($dir in @($adapterProject,$adapterWorker)){[void][IO.Directory]::CreateDirectory($dir)}
        $null = Invoke-BFGit $adapterProject @('init')
        $adapterTaskId=[guid]::NewGuid().ToString()
        $adapterState=[pscustomobject]@{project_path=$adapterProject;worker_path=$adapterWorker;task_id=$adapterTaskId;request=[pscustomobject]@{execution_profile=$profile;models=[pscustomobject]@{worker='gpt-5.6-luna';worker_effort='medium';reviewer='gpt-5.6-luna';reviewer_effort='medium'};timeout_seconds=1800}}
        $adapterTaskDirectory=Get-BFTaskDirectory $adapterProject $adapterTaskId
        $adapterDirectory=Join-Path $adapterTaskDirectory 'council'
        $adapterCatalogSource=Join-Path $adapterProject 'critic-catalog-source.json'
        Write-BFJson -Path $adapterCatalogSource -Value ([ordered]@{
            source_cache_path=(Join-Path $adapterProject 'synthetic-model-cache.json'); source_cache_sha256=('e'*64)
            model=[ordered]@{slug='gpt-5.6-luna';use_responses_lite=$true;shell_type='unified_exec';apply_patch_tool_type='freeform';tool_mode='code_mode_only';experimental_supported_tools=@();base_instructions='scoped fixture'}
        })
        [IO.File]::WriteAllText((Join-Path $adapterProject 'bsl-flow.yaml'),"llm:`n  providers:`n    fixture:`n      protocol: openai_compatible`n      base_url: https://fixture.invalid/v1`n      token_env: SCOPED_FIXTURE_MISSING_TOKEN`n  models:`n    fixture_model:`n      provider: fixture`n      model: fixture-model`n      effort: medium`nreview:`n  council:`n    enabled: true`n    max_parallel: 1`n    allow_local_http: false`n    legacy_mode: block`n    roles:`n      brainstorm:`n        enabled: false`n        required: false`n        fallback: block`n      intent_critic:`n        enabled: true`n        required: true`n        model: fixture_model`n        fallback: current_agent`n      architecture_critic:`n        enabled: false`n        required: false`n        fallback: block`n      executability_critic:`n        enabled: false`n        required: false`n        fallback: block`n      chair:`n        enabled: true`n        required: true`n        model: fixture_model`n        fallback: current_agent`n")
        $scopedSessionId='33333333-3333-4333-8333-333333333333'
        [IO.File]::WriteAllLines((Join-Path $fixtureSessionDir ('rollout-2026-09-12T00-00-00-{0}.jsonl' -f $scopedSessionId)), @(
            (@{timestamp='2026-09-12T00:00:00Z';ordinal=0;type='session_meta';payload=[ordered]@{session_id=$scopedSessionId;id=$scopedSessionId;originator='codex_exec';cli_version='0.154.0'}}|ConvertTo-Json -Depth 10 -Compress),
            (@{timestamp='2026-09-12T00:00:01Z';ordinal=1;type='turn_context';payload=[ordered]@{turn_id='scoped-turn';model='gpt-5.6-luna';effort='medium';approval_policy='never'}}|ConvertTo-Json -Depth 10 -Compress)
        ))
        function Test-BFProfiledCodexHostCapability {
            param($State,$Directory,$CodexPath,$Cancelled)
            return [ordered]@{
                capability_version='fixture-scoped-current-agent-v1'; provider='current_agent'; model='gpt-5.6-luna'; effort='medium'
                fresh_context=$true; sealed=$true; terminal=$true; source='fixture-scoped-host-proof'
                executable_sha256='be96b992178b1e467c225800da0d65f2c86d5eba1ef0b14632f65db381cbdfde'; sandbox_sha256=(Get-BFFileHash $profile.sandbox.executable)
                catalog_sha256=(Get-BFHash (Get-BFCodexCriticCatalogFromSourcePath $adapterCatalogSource).catalog); skills_sha256=$profile.codex_skills_sha256
                catalog_source_path=$adapterCatalogSource; catalog_source_sha256=(Get-BFFileHash $adapterCatalogSource)
            }
        }
        $profiledWorker = ${function:Invoke-BFProfiledCodexWorker}
        $scopedWorker = {
            param($State,$Stage,$Prompt,$Directory,$CodexPath,$Cancelled,$MaxOutputBytes=16777216)
            # These local seams keep the test at the native process/RPC boundary;
            # the called worker itself is the real ProfiledCodex implementation.
            function Get-BFExecutionDependencies { param($ExecutionState); return [ordered]@{profile=$ExecutionState.request.execution_profile;models=$ExecutionState.request.models} }
            function Assert-BFWorkerConfiguration { param($WorkerPath) }
            function Test-BFExecutionCapability {
                param($ExecutionState,$CapabilityDirectory,$Scratch,$Config,$Permissions,[bool]$Writable)
                [void][IO.Directory]::CreateDirectory($CapabilityDirectory)
                Write-BFJson (Join-Path $CapabilityDirectory 'capability.json') @{fixture=$true}
            }
            function Invoke-BFCodexReadOnlyRpc {
                param($Executable,$Overrides,$WorkingDirectory,$Directory,$Method,$Params,$Cancelled,$TimeoutSeconds,$MaxOutputBytes,$ExpectedMcpServers,$EnabledMcpServers)
                $script:rpcCalls++
                if($Method -eq 'config/read'){
                    return [pscustomobject]@{config=[pscustomobject]@{mcp_servers=[pscustomobject]@{'fixture-server'=[pscustomobject]@{enabled=(-not ($Overrides -contains 'mcp_servers.fixture-server.enabled=false'))}}}}
                }
                if($null -ne $Directory){Assert-C (@($ExpectedMcpServers).Count -ge 1 -and $ExpectedMcpServers[0] -ceq 'fixture-server') 'Scoped worker lost its same-process MCP guard.'}
                if($Method -eq 'mcpServerStatus/list'){return [pscustomobject]@{data=@();nextCursor=$null}}
                return [pscustomobject]@{data=@([pscustomobject]@{cwd=$WorkingDirectory;errors=@();skills=@([pscustomobject]@{name='fixture';path=$skill;scope='user';enabled=(-not (@($Overrides|Where-Object{$_ -like 'skills.config=*'}).Count -gt 0))})})}
            }
            function Invoke-BFProcess {
                param($Executable,$Arguments,$WorkingDirectory,$InputText,$OutputDirectory,$TimeoutSeconds,$Cancelled,$Environment,[switch]$CleanEnvironment,$MaxOutputBytes)
                $script:modelCalls++
                Assert-C (($Arguments -contains '--ignore-user-config') -and ($Arguments -contains 'agents.enabled=false') -and ($Arguments -contains 'multi_agent_v2')) 'Scoped worker lost sealed startup overrides.'
                $modelResult=ConvertFrom-Json $events[2].item.text
                Write-BFJson $Arguments[([array]::IndexOf($Arguments,'--output-last-message')+1)] $modelResult
                $stdout=Join-Path $OutputDirectory 'stdout.txt'
                $scopedEvents=@(
                    [ordered]@{type='thread.started';thread_id=$scopedSessionId},
                    [ordered]@{type='turn.started'},
                    [ordered]@{type='item.completed';item=[ordered]@{id='answer';type='agent_message';text=(Get-BFCanonicalJson $modelResult)}},
                    [ordered]@{type='turn.completed';usage=[ordered]@{input_tokens=4;cached_input_tokens=0;output_tokens=2}}
                )
                [IO.File]::WriteAllLines($stdout,@($scopedEvents|ForEach-Object{ConvertTo-Json $_ -Depth 30 -Compress}))
                $receipt=[ordered]@{exit_code=0;stop_reason=$null;stdout=$stdout;stderr=(Join-Path $OutputDirectory 'stderr.txt');executable=$Executable}
                Write-BFJson (Join-Path $OutputDirectory 'exit.json') $receipt
                return $receipt
            }
            & $profiledWorker $State $Stage $Prompt $Directory $CodexPath $Cancelled $MaxOutputBytes
        }
        $hostCapabilityProbe = ${function:Test-BFProfiledCodexHostCapability}
        function New-ScopedManagedCouncilAdapter {
            param($State,[string]$Directory,[string]$CodexPath)
            $taskRoot=Join-Path (Split-Path $PSScriptRoot -Parent) 'global/skills/1c-task'
            foreach($module in @('scripts/Task.Storage.ps1','scripts/Task.Process.ps1','scripts/Task.Contracts.ps1','scripts/Task.Engine.ps1','scripts/Task.Stages.ps1','adapters/Codex.Skills.ps1','adapters/Codex.ps1','adapters/ProfiledCodex.ps1','scripts/Task.ManagedReview.ps1')){. (Join-Path $taskRoot $module)}
            Set-Item -Path Function:Test-BFProfiledCodexHostCapability -Value $hostCapabilityProbe
            Set-Item -Path Function:Invoke-BFManagedWorker -Value $scopedWorker
            return New-BFManagedCouncilHostAdapter -State $State -Directory $Directory -CodexPath $CodexPath
        }
        $adapter=New-ScopedManagedCouncilAdapter $adapterState $adapterDirectory $skill
        $adapterAttempt=[pscustomobject]@{role='intent_critic';sequence=1}
        $adapterResult=& $adapter.fallback_runner $adapterAttempt 'Role: intent_critic' $adapter.capabilities['intent_critic']
        Assert-C ($adapterResult.status -eq 'completed' -and $adapterResult.observed_model -eq 'gpt-5.6-luna' -and $adapterResult.observed_effort -eq 'medium') 'Scoped managed adapter did not execute the real profiled worker with rollout-observed identity.'
        # A strict fallback receipt is content-addressed to its persisted rollout
        # turn.  Mutating that identity must block before the callback can publish
        # another result, even though the model/effort values remain unchanged.
        $scopedReceiptPath=Join-Path $adapterDirectory 'fallback-intent_critic-1/host-result.json'
        $scopedReceipt=Read-BFJson $scopedReceiptPath
        $scopedReceipt.turn_id='tampered-turn'
        [IO.File]::WriteAllText($scopedReceiptPath,(ConvertTo-Json -InputObject $scopedReceipt -Depth 100))
        $strictCacheFailure=Failure-C { & $adapter.fallback_runner $adapterAttempt 'Role: intent_critic' $adapter.capabilities['intent_critic'] }
        Assert-C ($strictCacheFailure -match 'cached Codex receipt differs|turn identity differs') 'Strict fallback accepted a tampered persisted turn identity.'
    }
    Write-Output "PROFILED_CODEX_OK checks=$script:checks; model/sandbox/database processes=0"
}finally{
    Remove-Item Env:CODEX_HOME -ErrorAction SilentlyContinue
    $resolved=[IO.Path]::GetFullPath($root);$temporary=[IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\','/')+[IO.Path]::DirectorySeparatorChar
    if(-not $resolved.StartsWith($temporary,[StringComparison]::OrdinalIgnoreCase) -or (Split-Path $resolved -Leaf) -notlike 'bsl-flow-codex-*'){throw 'Unsafe test cleanup target.'}
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
