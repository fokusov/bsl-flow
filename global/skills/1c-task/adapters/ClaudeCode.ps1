#Requires -Version 7.0
Set-StrictMode -Version Latest

# Experimental Claude Code worker (plan Ф4.6). Isolation is permission rules
# plus a controller integrity check, not an OS sandbox: no stage receives
# Bash, web, subagent or MCP tools, and tests/builds stay controller-owned.

function Get-BFClaudeCodeTools {
    param($Adapter,[string]$Stage)
    if($Stage -ceq 'implement'){return @($Adapter.tools.implement)}
    return @($Adapter.tools.read_only)
}

function Get-BFClaudeCodeArguments {
    param($Adapter,[string]$Stage,[string]$Model,$Effort,[string]$McpConfig)
    $tools=(Get-BFClaudeCodeTools $Adapter $Stage) -join ','
    $arguments=@('-p','--output-format','stream-json','--verbose','--model',$Model)
    if($null -ne $Effort){$arguments+=@('--effort',[string]$Effort)}
    # --tools narrows the built-in set; --allowedTools pre-approves exactly that
    # set for dontAsk mode, which denies everything else instead of prompting.
    # An empty --setting-sources loads no user/project/local settings, hooks
    # or plugins (verified accepted by 2.1.142; managed policy still applies).
    $arguments+=@('--tools',$tools,'--allowedTools',$tools,'--disallowedTools',(@($Adapter.tools.denied) -join ','),'--permission-mode','dontAsk','--strict-mcp-config','--mcp-config',$McpConfig,'--setting-sources','','--no-session-persistence','--disable-slash-commands')
    return $arguments
}

function Test-BFClaudeCodeCapability {
    param($State,[string]$Directory,$Adapter)
    $profile=$State.request.execution_profile
    [void][IO.Directory]::CreateDirectory((Assert-BFSafePath $Directory))
    $version=Invoke-BFProcess -Executable $profile.executable -Arguments @('--version') -WorkingDirectory $State.worker_path -OutputDirectory (Join-Path $Directory 'version') -TimeoutSeconds 30 -CleanEnvironment
    if($version.exit_code -ne 0 -or $version.stop_reason){throw 'BF_BLOCKED: Claude Code version probe failed.'}
    $versionText=[IO.File]::ReadAllText($version.stdout).Trim()
    [void](Assert-BFAdapterVersion $Adapter $versionText)
    $help=Invoke-BFProcess -Executable $profile.executable -Arguments @('--help') -WorkingDirectory $State.worker_path -OutputDirectory (Join-Path $Directory 'help') -TimeoutSeconds 30 -CleanEnvironment
    if($help.exit_code -ne 0 -or $help.stop_reason){throw 'BF_BLOCKED: Claude Code help probe failed.'}
    $helpText=[IO.File]::ReadAllText($help.stdout)
    foreach($flag in @($Adapter.required_help_flags)){
        if($helpText -cnotmatch ('(?<![A-Za-z0-9-])'+[regex]::Escape($flag)+'(?![A-Za-z0-9-])')){throw "BF_BLOCKED: Claude Code does not document required flag $flag."}
    }
    $capability=[ordered]@{version=$versionText;executable_sha256=$profile.executable_sha256;help_sha256=Get-BFFileHash $help.stdout;flags=@($Adapter.required_help_flags);isolation='permission_rules';os_sandbox=$false;checked_at_utc=[DateTime]::UtcNow.ToString('o')}
    Write-BFJson (Join-Path $Directory 'capability.json') $capability
    return $capability
}

function Test-BFClaudeCodeToolPath {
    param([string]$Tool,$ToolInput,[string]$WorkerPath,[string[]]$ReadRoots)
    $field=if($Tool -cin @('Grep','Glob')){'path'}else{'file_path'}
    $value=Get-BFValue $ToolInput $field
    if($null -eq $value){if($field -ceq 'path'){return};throw "BF_BLOCKED: Claude Code $Tool call has no path."}
    if($value -isnot [string] -or [string]::IsNullOrWhiteSpace($value)){throw "BF_BLOCKED: invalid Claude Code $Tool path."}
    $candidate=if([IO.Path]::IsPathRooted($value)){$value}else{Join-Path $WorkerPath $value}
    try{$full=Assert-BFSafePath $candidate}catch{throw "BF_BLOCKED: unsafe Claude Code $Tool path."}
    $roots=if($Tool -cin @('Edit','Write')){@($WorkerPath)}else{@($WorkerPath)+@($ReadRoots)}
    $inside=$false
    foreach($root in $roots){$r=(Assert-BFSafePath $root).TrimEnd('\','/');if($full -eq $r -or $full.StartsWith($r+'\',[StringComparison]::OrdinalIgnoreCase)){$inside=$true;break}}
    if(-not $inside){throw "BF_BLOCKED: Claude Code $Tool reached outside the permitted roots."}
    if($Tool -cin @('Edit','Write')){
        $relative=$full.Substring(([string]$WorkerPath).TrimEnd('\','/').Length).Replace('\','/')
        if($relative -match '(^|/)(\.git|\.bsl-flow)(/|$)'){throw 'BF_BLOCKED: Claude Code wrote an administrative path.'}
    }
}

function Read-BFClaudeCodeEvents {
    param([string]$Path,[int]$ExitCode,[string[]]$AllowedTools,[string[]]$DeniedTools,[string]$WorkerPath,[string[]]$ReadRoots=@(),[int]$MaxBytes=16777216)
    $fullPath=Assert-BFSafePath $Path
    if(-not [IO.File]::Exists($fullPath)){throw 'BF_BLOCKED: Claude Code event stream is missing.'}
    $raw=[IO.File]::ReadAllBytes($fullPath)
    if($raw.Length -eq 0 -or $raw.Length -gt $MaxBytes){throw 'BF_BLOCKED: empty or oversized Claude Code event stream.'}
    try{$text=[Text.UTF8Encoding]::new($false,$true).GetString($raw)}catch{throw 'BF_BLOCKED: Claude Code event stream is not UTF-8.'}
    if(-not $text.EndsWith("`n")){throw 'BF_BLOCKED: torn Claude Code event stream.'}
    $session=$null;$observedModel=$null;$final=$null;$toolCalls=@();$ids=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach($line in @($text.TrimEnd("`r","`n").Split("`n"))){
        $line=$line.TrimEnd("`r")
        if([string]::IsNullOrWhiteSpace($line)){throw 'BF_BLOCKED: blank Claude Code event line.'}
        $kind=$null;try{$kind=Test-BFJsonSyntax $line}catch{$kind=$null}
        if($kind -ne 'object'){throw 'BF_BLOCKED: Claude Code event must be a JSON object.'}
        $event=ConvertFrom-Json -InputObject $line -Depth 100
        $type=Get-BFValue $event 'type'
        if($type -isnot [string]){throw 'BF_BLOCKED: Claude Code event has no type.'}
        if($null -ne $final){throw 'BF_BLOCKED: Claude Code event follows the terminal result.'}
        $eventSession=Get-BFValue $event 'session_id'
        if($type -ceq 'system' -and (Get-BFValue $event 'subtype') -ceq 'init'){
            if($null -ne $session){throw 'BF_BLOCKED: multiple Claude Code init events.'}
            $observedModel=Get-BFValue $event 'model'
            if($eventSession -isnot [string] -or [string]::IsNullOrWhiteSpace($eventSession) -or $observedModel -isnot [string] -or [string]::IsNullOrWhiteSpace($observedModel)){throw 'BF_BLOCKED: Claude Code init lacks session or model identity.'}
            $session=$eventSession
            foreach($tool in @(Get-BFValue $event 'tools' @())){if([string]$tool -cnotin $AllowedTools -or [string]$tool -cin $DeniedTools){throw "BF_BLOCKED: Claude Code exposed a denied tool: $tool"}}
            if(@(Get-BFValue $event 'mcp_servers' @()).Count -ne 0){throw 'BF_BLOCKED: Claude Code loaded MCP servers.'}
            continue
        }
        if($null -eq $session){throw 'BF_BLOCKED: Claude Code event precedes init.'}
        if($null -ne $eventSession -and $eventSession -cne $session){throw 'BF_BLOCKED: mixed Claude Code sessions.'}
        if($type -ceq 'assistant'){
            foreach($part in @(Get-BFValue (Get-BFValue $event 'message') 'content' @())){
                if((Get-BFValue $part 'type') -cne 'tool_use'){continue}
                $name=[string](Get-BFValue $part 'name');$id=[string](Get-BFValue $part 'id')
                if($name -cnotin $AllowedTools){throw "BF_BLOCKED: Claude Code tool is not allowlisted: $name"}
                if([string]::IsNullOrWhiteSpace($id) -or -not $ids.Add($id)){throw 'BF_BLOCKED: missing or duplicate Claude Code tool call identity.'}
                Test-BFClaudeCodeToolPath $name (Get-BFValue $part 'input') $WorkerPath $ReadRoots
                $toolCalls+=,[ordered]@{name=$name;id=$id}
            }
        }elseif($type -ceq 'result'){$final=$event}
    }
    if($null -eq $session){throw 'BF_BLOCKED: Claude Code stream has no init event.'}
    if($null -eq $final){throw 'BF_BLOCKED: Claude Code stream has no result event.'}
    if((Get-BFValue $final 'is_error') -ne $false -or (Get-BFValue $final 'subtype') -cne 'success'){throw 'BF_BLOCKED: Claude Code reported an error result.'}
    if($ExitCode -ne 0){throw 'BF_BLOCKED: Claude Code process failed.'}
    $cost=Get-BFValue $final 'total_cost_usd';$usage=Get-BFValue $final 'usage';$resultText=Get-BFValue $final 'result'
    if($null -eq $cost -or $cost -is [bool] -or $cost -isnot [ValueType] -or -not [double]::IsFinite([double]$cost) -or [double]$cost -lt 0){throw 'BF_BLOCKED: invalid Claude Code reported cost.'}
    if($null -eq $usage -or $usage -isnot [pscustomobject]){throw 'BF_BLOCKED: Claude Code result has no usage.'}
    if($resultText -isnot [string]){throw 'BF_BLOCKED: Claude Code result has no text.'}
    $result=ConvertFrom-BFWorkerResultText $resultText 'claude_code_result'
    return [ordered]@{result=$result;metadata=[ordered]@{session_id=$session;observed_model=$observedModel;observed_effort=$null;usage=$usage;reported_cost_usd=[double]$cost;cost_source='claude.result.total_cost_usd';tool_calls=@($toolCalls);permission_denials=@(Get-BFValue $final 'permission_denials' @()).Count}}
}

function Invoke-BFClaudeCodeWorker {
    param($State,[string]$Stage,[string]$Prompt,[string]$Directory,[string]$CodexPath,[scriptblock]$Cancelled,[int]$MaxOutputBytes=16777216)
    Assert-BFAdapterDispatchReady (Get-BFWorkerAdapter 'claude-code')
    $dependencies=Get-BFExecutionDependencies $State;$profile=$State.request.execution_profile
    if($MaxOutputBytes -lt 65536 -or $MaxOutputBytes -gt 16777216){throw 'BF_INVALID: managed output bound is outside the supported range.'}
    if($profile.provider -cne 'claude-code'){throw 'BF_BLOCKED: managed Claude Code identity mismatch.'}
    $adapter=Get-BFWorkerAdapter 'claude-code'
    Assert-BFAdapterStagePolicy $adapter $Stage
    $model=if($Stage -in @('code_review','spec_review')){$State.request.models.reviewer}else{$State.request.models.worker}
    $effort=if($Stage -in @('code_review','spec_review')){$State.request.models.reviewer_effort}else{$State.request.models.worker_effort}
    $directory=Assert-BFSafePath $Directory
    $taskRoot=Assert-BFSafePath (Join-Path $State.project_path ('.bsl-flow/tasks/'+$State.task_id))
    if(-not $directory.StartsWith($taskRoot.TrimEnd('\','/')+'\',[StringComparison]::OrdinalIgnoreCase)){throw 'BF_INVALID: Claude Code attempt must be under the private task directory.'}
    $hostRoot=Assert-BFSafePath (Join-Path $State.project_path ('.bsl-flow/hosts/'+$State.task_id+'/'+(Get-BFHash $directory)))
    $config=Join-Path $hostRoot 'config';$scratch=Join-Path $hostRoot 'scratch';$mcp=Join-Path $config 'mcp.json'
    $arguments=Get-BFClaudeCodeArguments $adapter $Stage $model $effort $mcp
    $inputText="The exact source working directory is: $($State.worker_path). Resolve relative source paths here; do not read or write outside it.`n"+$Prompt+(Get-BFToolsetPrompt $State)+"`nYou have no shell. Return exactly one JSON object with schema_version:1, status:completed|needs_input|blocked|failed, summary:string, payload_json:string containing valid JSON. Do not add any other text."
    $binding=[ordered]@{dependencies=$dependencies;stage=$Stage;prompt_sha256=Get-BFHash $inputText;arguments_sha256=Get-BFHash $arguments;adapter_sha256=Get-BFFileHash $PSCommandPath;manifest_sha256=Get-BFFileHash (Join-Path $PSScriptRoot 'claude-code.adapter.json');worker_path=$State.worker_path;max_output_bytes=$MaxOutputBytes}
    $bindingHash=Get-BFHash $binding
    $protected=@($taskRoot,$config)
    $exitPath=Join-Path $directory 'exit.json';$hostPath=Join-Path $directory 'host-result.json';$resultPath=Join-Path $directory 'model-result.json';$integrityPath=Join-Path $directory 'integrity.json'
    if(Test-Path -LiteralPath $directory){
        # Resume parses retained evidence only; a paid call is never repeated.
        foreach($name in @('binding.json','exit.json','integrity.json','capability/capability.json')){if(-not(Test-Path -LiteralPath (Join-Path $directory $name))){throw 'BF_BLOCKED: partial Claude Code dispatch; reconcile the preserved attempt without retry.'}}
        if((Read-BFJson (Join-Path $directory 'binding.json')).sha256 -cne $bindingHash){throw 'BF_BLOCKED: cached Claude Code binding differs.'}
        $process=Read-BFJson $exitPath;$integrity=Read-BFJson $integrityPath
    }else{
        if(Test-Path -LiteralPath $hostRoot){throw 'BF_BLOCKED: unregistered Claude Code host directory exists.'}
        foreach($path in @($directory,$config,$scratch)){[void][IO.Directory]::CreateDirectory($path)}
        Write-BFJson (Join-Path $directory 'binding.json') ([ordered]@{sha256=$bindingHash;binding=$binding})
        [void](Test-BFClaudeCodeCapability $State (Join-Path $directory 'capability') $adapter)
        [IO.File]::WriteAllText($mcp,'{"mcpServers":{}}',[Text.UTF8Encoding]::new($false))
        $before=Get-BFTreeDigest $protected @($directory)
        if((Get-BFHash (Get-BFExecutionDependencies $State)) -cne (Get-BFHash $dependencies)){throw 'BF_BLOCKED: managed inputs changed before dispatch.'}
        $environment=@{TEMP=$scratch;TMP=$scratch;DISABLE_AUTOUPDATER='1';GIT_OPTIONAL_LOCKS='0'}
        foreach($name in @('ANTHROPIC_API_KEY','CLAUDE_CONFIG_DIR')){$value=[Environment]::GetEnvironmentVariable($name);if(-not [string]::IsNullOrEmpty($value)){$environment[$name]=$value}}
        try{$process=Invoke-BFProcess -Executable $profile.executable -Arguments $arguments -WorkingDirectory $State.worker_path -InputText $inputText -OutputDirectory $directory -TimeoutSeconds ([int](Get-BFValue $State.request 'timeout_seconds' 1800)) -Cancelled $Cancelled -Environment $environment -CleanEnvironment -MaxOutputBytes $MaxOutputBytes}
        finally{$environment.Remove('ANTHROPIC_API_KEY')}
        $integrity=[ordered]@{roots=@($protected);excluded=$directory;before_sha256=$before;after_sha256=(Get-BFTreeDigest $protected @($directory))}
        Write-BFJson $integrityPath $integrity
    }
    if($integrity.before_sha256 -cne $integrity.after_sha256){throw 'BF_BLOCKED: controller state modified by worker.'}
    if($process.stop_reason){throw "BF_BLOCKED: Claude Code $($process.stop_reason); preserve sources and reconcile the attempt before retry."}
    $readRoots=@([string]$profile.toolset.root)
    $parsed=Read-BFClaudeCodeEvents -Path $process.stdout -ExitCode $process.exit_code -AllowedTools (Get-BFClaudeCodeTools $adapter $Stage) -DeniedTools @($adapter.tools.denied) -WorkerPath $State.worker_path -ReadRoots $readRoots -MaxBytes $MaxOutputBytes
    if((Get-BFHash (Get-BFExecutionDependencies $State)) -cne (Get-BFHash $dependencies)){throw 'BF_BLOCKED: managed inputs changed during execution.'}
    $observed=[string]$parsed.metadata.observed_model
    if($model -cmatch '^claude-' -and -not $observed.StartsWith($model,[StringComparison]::Ordinal)){throw 'BF_BLOCKED: observed Claude Code model differs from the requested model.'}
    if(Test-Path -LiteralPath $resultPath){if((Get-BFHash (Read-BFJson $resultPath)) -cne (Get-BFHash $parsed.result)){throw 'BF_BLOCKED: cached model result differs from raw Claude Code evidence.'}}
    else{Write-BFJson $resultPath $parsed.result}
    $metadata=$parsed.metadata
    $metadata.requested_model=$model;$metadata.requested_effort=$effort;$metadata.adapter='claude-code';$metadata.adapter_status=$adapter.status;$metadata.isolation='permission_rules';$metadata.integrity_sha256=Get-BFFileHash $integrityPath
    $metadata.binding_sha256=$bindingHash;$metadata.stdout_sha256=Get-BFFileHash $process.stdout;$metadata.usage_source=$process.stdout
    if(Test-Path -LiteralPath $hostPath){if((Get-BFHash (Read-BFJson $hostPath)) -cne (Get-BFHash $metadata)){throw 'BF_BLOCKED: cached Claude Code receipt differs from raw evidence.'}}
    else{Write-BFJson $hostPath $metadata}
    return $parsed.result
}
