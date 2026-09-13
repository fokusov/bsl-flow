#Requires -Version 7.0
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'OpenCode.Events.ps1')
. (Join-Path (Split-Path $PSScriptRoot -Parent) 'scripts/Task.Execution.ps1')

function Invoke-BFOpenCodeWorker {
    param($State,[string]$Stage,[string]$Prompt,[string]$Directory,[string]$CodexPath,[scriptblock]$Cancelled,[int]$MaxOutputBytes=16777216)
    $profile=$State.request.execution_profile
    $dependencies=Get-BFExecutionDependencies $State
    if($profile.provider -cne 'opencode' -or (Assert-BFSafePath $CodexPath) -cne (Assert-BFSafePath $profile.sandbox.executable)){throw 'BF_BLOCKED: managed provider/sandbox identity mismatch.'}
    $model=if($Stage -in @('code_review','spec_review')){$State.request.models.reviewer}else{$State.request.models.worker}
    $effort=if($Stage -in @('code_review','spec_review')){$State.request.models.reviewer_effort}else{$State.request.models.worker_effort}
    if($model -cne 'deepseek/deepseek-v4-flash' -or $null -ne $effort){throw 'BF_BLOCKED: only the measured Flash route with no effort override is supported.'}
    $directory=Assert-BFSafePath $Directory
    $binding=[ordered]@{dependencies=$dependencies;stage=$Stage;prompt_sha256=Get-BFHash $Prompt;worker_path=$State.worker_path}
    if($MaxOutputBytes -ne 16777216){$binding.max_output_bytes=$MaxOutputBytes}
    $bindingHash=Get-BFHash $binding
    $hostRoot=Assert-BFSafePath (Join-Path $State.project_path ('.bsl-flow/hosts/'+$State.task_id+'/'+(Get-BFHash $directory)))
    $scratch=Join-Path $hostRoot 'scratch';$config=Join-Path $hostRoot 'config'
    $permissions=Get-BFExecutionPermissionProfile $State $scratch $config ($Stage -eq 'implement')
    $allowed=@('read','glob','grep','skill','bash')
    if($profile.toolset.name -eq 'unica'){$allowed=@('read','glob','grep','skill')+@($profile.unica.allowed_tools|ForEach-Object{'unica_'+($_ -creplace '[^a-zA-Z0-9_-]','_')})}
    if($Stage -eq 'implement'){$allowed+=@('edit','write','apply_patch')}
    if($Stage -eq 'spec_review'){$allowed=@()}
    $hostPath=Join-Path $directory 'host-result.json'
    $exitPath=Join-Path $directory 'exit.json'
    if(Test-Path -LiteralPath $directory){
        # A partial dispatch can already have changed sources or spent money.
        # Resume parses complete saved evidence; it never repeats the model call.
        if(-not (Test-Path -LiteralPath (Join-Path $directory 'binding.json')) -or -not(Test-Path -LiteralPath $exitPath)){throw 'BF_BLOCKED: partial OpenCode dispatch; reconcile the preserved attempt without retry.'}
        if((Read-BFJson (Join-Path $directory 'binding.json')).sha256 -cne $bindingHash){throw 'BF_BLOCKED: cached OpenCode invocation binding differs.'}
        $process=Read-BFJson $exitPath
    } else {
        if(Test-Path -LiteralPath $hostRoot){throw 'BF_BLOCKED: unregistered OpenCode host directory exists.'}
        foreach($path in @($directory,$scratch,(Join-Path $scratch 'data'),(Join-Path $scratch 'cache'),(Join-Path $scratch 'state'),(Join-Path $scratch 'tmp'),(Join-Path $config 'opencode'))){[void][IO.Directory]::CreateDirectory($path)}
        Write-BFJson (Join-Path $directory 'binding.json') ([ordered]@{sha256=$bindingHash;binding=$binding;permission_sha256=Get-BFHash $permissions})
        $rules=[ordered]@{'*'='deny';read='allow';glob='allow';grep='allow';skill='allow';bash='allow'}
        $rules.external_directory=[ordered]@{'*'='deny';(($profile.toolset.root.Replace('\','/').TrimEnd('/'))+'/*')='allow'}
        $rules.external_directory[($scratch.Replace('\','/').TrimEnd('/')+'/*')]='allow'
        if($profile.toolset.name -eq 'unica'){
            $rules.bash='deny'
            foreach($tool in $profile.unica.allowed_tools){$rules['unica_'+($tool -creplace '[^a-zA-Z0-9_-]','_')]='allow'}
        }
        if($Stage -eq 'implement'){$rules.edit='allow'}
        if($Stage -eq 'spec_review'){$rules=[ordered]@{'*'='deny'}}
        $configuration=[ordered]@{model=$model;default_agent='bsl-flow';plugin=@();skills=@{paths=@($profile.toolset.root)};agent=@{'bsl-flow'=@{mode='primary';model=$model;permission=$rules}}}
        if($Stage -eq 'spec_review'){$configuration.skills.paths=@()}
        if($profile.toolset.name -eq 'unica' -and $Stage -ne 'spec_review'){
            $configuration.mcp=@{unica=@{type='local';command=@((Join-Path $profile.unica.plugin_root 'bootstrap/bin/win-x64/unica-bootstrap.exe'),'run','--plugin-root',$profile.unica.plugin_root);environment=@{UNICA_RUNTIME_CACHE_DIR=$profile.unica.runtime_cache};timeout=60000;enabled=$true}}
        }
        Write-BFJson (Join-Path $config 'opencode/opencode.json') $configuration
        [IO.File]::WriteAllText((Join-Path $config 'opencode/.gitignore'),'')
        [void](Test-BFExecutionCapability $State (Join-Path $directory 'capability') $scratch $config $permissions ($Stage -eq 'implement'))
        $authPath=Join-Path ([Environment]::GetFolderPath('UserProfile')) '.local/share/opencode/auth.json'
        $auth=Read-BFJson $authPath
        if((Get-BFValue (Get-BFValue $auth 'deepseek') 'type') -ne 'api'){throw 'BF_BLOCKED: existing DeepSeek API authorization is unavailable.'}
        $key=Get-BFValue $auth.deepseek 'key'
        if($key -isnot [string] -or [string]::IsNullOrWhiteSpace($key)){throw 'BF_BLOCKED: existing DeepSeek API authorization is unavailable.'}
        $environment=@{XDG_CONFIG_HOME=$config;XDG_DATA_HOME=(Join-Path $scratch 'data');XDG_CACHE_HOME=(Join-Path $scratch 'cache');XDG_STATE_HOME=(Join-Path $scratch 'state');TEMP=(Join-Path $scratch 'tmp');TMP=(Join-Path $scratch 'tmp');OPENCODE_DISABLE_PROJECT_CONFIG='1';OPENCODE_DISABLE_CLAUDE_CODE='1';OPENCODE_DISABLE_EXTERNAL_SKILLS='1';DEEPSEEK_API_KEY=$key;GIT_OPTIONAL_LOCKS='0'}
        $arguments=@('sandbox','-P','bsl_execution','-c',$permissions,'-c','windows.sandbox="elevated"','-C',$State.worker_path,$profile.executable,'run','--pure','--format','json','--model',$model,'--agent','bsl-flow','--dir',$State.worker_path)
        $toolsetPrompt=if($Stage -eq 'spec_review'){''}else{Get-BFToolsetPrompt $State}
        $inputText="The exact source working directory is: $($State.worker_path). Resolve relative source paths here; do not search parent projects.`n"+$Prompt+$toolsetPrompt+"`nReturn exactly one JSON object with schema_version:1, status:completed|needs_input|blocked|failed, summary:string, payload_json:string containing valid JSON. Do not wrap the final answer in Markdown."
        if((Get-BFHash (Get-BFExecutionDependencies $State)) -cne (Get-BFHash $dependencies)){throw 'BF_BLOCKED: host/toolset inputs changed before dispatch.'}
        try {$process=Invoke-BFProcess -Executable $CodexPath -Arguments $arguments -WorkingDirectory $State.worker_path -InputText $inputText -OutputDirectory $directory -TimeoutSeconds ([int](Get-BFValue $State.request 'timeout_seconds' 1800)) -Cancelled $Cancelled -Environment $environment -CleanEnvironment -MaxOutputBytes $MaxOutputBytes}
        finally {$environment.Remove('DEEPSEEK_API_KEY');$key=$null;$auth=$null}
    }
    if($process.stop_reason){throw "BF_BLOCKED: OpenCode $($process.stop_reason); reconcile the attempt before retry."}
    $parsed=Read-BFOpenCodeEvents -Path $process.stdout -ExitCode $process.exit_code -AllowedTools $allowed
    if((Get-BFHash (Get-BFExecutionDependencies $State)) -cne (Get-BFHash $dependencies)){throw 'BF_BLOCKED: host/toolset inputs changed during execution.'}
    $modelPath=Join-Path $directory 'model-result.json'
    if(Test-Path -LiteralPath $modelPath){
        if((Get-BFHash (Read-BFJson $modelPath)) -cne (Get-BFHash $parsed.result)){throw 'BF_BLOCKED: cached model result differs from raw OpenCode evidence.'}
    }else{Write-BFJson $modelPath $parsed.result}
    $metadata=$parsed.metadata
    # Match Read-BFJson's numeric representation so a completed receipt is stable
    # across PowerShell's decimal-to-JSON-to-double round trip.
    $metadata.reported_cost_usd=[double]$metadata.reported_cost_usd
    $metadata.requested_model=$model;$metadata.requested_effort=$effort;$metadata.binding_sha256=$bindingHash;$metadata.usage_source=$process.stdout
    if(Test-Path -LiteralPath $hostPath){if((Get-BFHash (Read-BFJson $hostPath)) -cne (Get-BFHash $metadata)){throw 'BF_BLOCKED: cached OpenCode receipt differs from raw evidence.'}}
    else{Write-BFJson $hostPath $metadata}
    return $parsed.result
}
