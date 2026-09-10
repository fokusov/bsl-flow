#Requires -Version 7.0
Set-StrictMode -Version Latest

function Get-BFPermissionProfile {
    param([string]$WorkerPath, [bool]$Writable)
    # Serialize the path as a TOML basic string; JSON escaping is valid for this restricted value.
    $path=ConvertTo-Json -InputObject ($WorkerPath -replace '\\','/') -Compress
    $access=if ($Writable) {'write'} else {'read'}
    return 'permissions.bsl_flow={filesystem={":root"="read",' + $path + '="' + $access + '"},network={enabled=false}}'
}

function Assert-BFWorkerConfiguration {
    param([string]$WorkerPath)
    # Project-controlled execution configuration must not start unsandboxed MCP/hooks.
    $dir=$WorkerPath
    while ($dir) {
        foreach ($relative in @('.codex/config.toml','.codex/hooks.json','.codex/config.json')) {
            if (Test-Path -LiteralPath (Join-Path $dir $relative)) { throw "BF_BLOCKED: managed adapter does not load project execution configuration: $(Join-Path $dir $relative)" }
        }
        $parent=Split-Path $dir -Parent
        if ($parent -eq $dir) { break }; $dir=$parent
    }
}

function Test-BFCodexCapability {
    param($State, [string]$CodexPath, [string]$Directory)
    Assert-BFWorkerConfiguration $State.worker_path
    [void][IO.Directory]::CreateDirectory($Directory)
    $version=Invoke-BFProcess $CodexPath @('--version') $State.worker_path '' (Join-Path $Directory 'version') 30
    $versionText=[IO.File]::ReadAllText($version.stdout).Trim()
    if ($version.exit_code -ne 0 -or $versionText -notin @('codex-cli 0.153.0','codex-cli 0.154.0')) { throw "BF_BLOCKED: unverified Codex host version: $versionText. Run and review the host capability suite before supporting it." }
    $sentinel=Join-Path $Directory 'controller-sentinel.txt'
    [IO.File]::WriteAllText($sentinel,'controller', (New-Object Text.UTF8Encoding($false)))
    $probeRoot=Join-Path $State.worker_path '.bsl-flow-worker/capability'
    [void](Assert-BFSafePath $probeRoot); [void][IO.Directory]::CreateDirectory($probeRoot)
    $allowed=Join-Path $probeRoot ('write-'+[guid]::NewGuid().ToString('N')+'.txt')
    $quotePath = { param($p) "'" + $p.Replace("'","''") + "'" }
    $script='$ErrorActionPreference="Stop"; $a="denied"; $b="denied"; try {Set-Content -LiteralPath ' + (& $quotePath $allowed) + ' -Value "probe" -ErrorAction Stop; $a="allowed"} catch [System.UnauthorizedAccessException] {}; try {Set-Content -LiteralPath ' + (& $quotePath $sentinel) + ' -Value "tampered" -ErrorAction Stop; $b="allowed"} catch [System.UnauthorizedAccessException] {}; Write-Output ($a+":"+$b)'
    $encoded=[Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($script))
    $shell=Join-Path $PSHOME 'pwsh.exe'
    foreach ($write in @($false,$true)) {
        $name=if ($write) {'write'} else {'read'}
        $args=@('sandbox','-P','bsl_flow','-c',(Get-BFPermissionProfile $State.worker_path $write),'-c','windows.sandbox="unelevated"','-C',$State.worker_path,$shell,'-NoProfile','-EncodedCommand',$encoded)
        $process=Invoke-BFProcess $CodexPath $args $State.worker_path '' (Join-Path $Directory $name) 60
        $expected=if ($write) {'allowed:denied'} else {'denied:denied'}
        if ($process.exit_code -ne 0 -or [IO.File]::ReadAllText($process.stdout).Trim() -ne $expected -or [IO.File]::ReadAllText($sentinel) -ne 'controller') { throw "BF_BLOCKED: $name sandbox capability was not demonstrated." }
    }
    return [ordered]@{version=$versionText;executable_sha256=Get-BFFileHash $CodexPath;source_write=$true;controller_write=$false;checked_at=[DateTime]::UtcNow.ToString('o')}
}

function Invoke-BFCodexWorker {
    param($State, [string]$Stage, [string]$Prompt, [string]$Directory, [string]$CodexPath, [scriptblock]$Cancelled)
    Assert-BFWorkerConfiguration $State.worker_path
    $model=if ($Stage -eq 'code_review') {$State.request.models.reviewer} else {$State.request.models.worker}
    $effort=if ($Stage -eq 'code_review') {$State.request.models.reviewer_effort} else {$State.request.models.worker_effort}
    $schema=Join-Path (Split-Path $PSScriptRoot -Parent) 'schemas/worker-result.schema.json'
    $resultPath=Join-Path $Directory 'model-result.json'
    $args=@('exec','--ignore-user-config','--ignore-rules','--ephemeral','--skip-git-repo-check','--model',$model,'-c',('model_reasoning_effort="'+$effort+'"'),'-c','approval_policy="never"','-c','default_permissions="bsl_flow"','-c',(Get-BFPermissionProfile $State.worker_path ($Stage -eq 'implement')),'-c','windows.sandbox="unelevated"')
    foreach ($feature in @('plugins','multi_agent','memories','shell_snapshot','hooks','browser_use','computer_use','in_app_browser','skill_mcp_dependency_install')) { $args += @('--disable',$feature) }
    $args += @('--json','--output-schema',$schema,'--output-last-message',$resultPath,'--cd',$State.worker_path,'-')
    $exitPath=Join-Path $Directory 'exit.json'
    $hostPath=Join-Path $Directory 'host-result.json'
    if((Test-Path -LiteralPath $exitPath -PathType Leaf) -and (Test-Path -LiteralPath $hostPath -PathType Leaf) -and (Test-Path -LiteralPath $resultPath -PathType Leaf)){
        $process=Read-BFJson $exitPath
        $saved=Read-BFJson $hostPath
        if($saved.requested_model -ne $model -or $saved.requested_effort -ne $effort){throw 'BF_BLOCKED: cached worker model/effort differs from the registered stage.'}
    }else{
        $process=Invoke-BFProcess $CodexPath $args $State.worker_path $Prompt $Directory ([int](Get-BFValue $State.request 'timeout_seconds' 1800)) $Cancelled
    }
    if ($process.stop_reason) { throw "BF_BLOCKED: worker $($process.stop_reason); preserve changed sources and reconcile the attempt before retry." }
    if ($process.exit_code -ne 0 -or -not (Test-Path -LiteralPath $resultPath -PathType Leaf)) { throw 'BF_BLOCKED: Codex provider/process did not produce a complete result.' }
    $session=$null; $usage=$null; $turnComplete=$false
    foreach ($line in [IO.File]::ReadLines($process.stdout)) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        try { $event=ConvertFrom-Json -InputObject $line -ErrorAction Stop } catch { throw 'BF_BLOCKED: malformed Codex JSONL stream.' }
        if ($event.type -eq 'thread.started') { if ($session) { throw 'BF_BLOCKED: multiple session identities.' }; $session=$event.thread_id }
        if ($event.type -eq 'turn.completed') { $turnComplete=$true; $usage=$event.usage }
        if ($event.type -in @('error','turn.failed')) { throw 'BF_BLOCKED: Codex reported a provider failure.' }
    }
    if (-not $session -or -not $turnComplete) { throw 'BF_BLOCKED: missing exact session identity or completed turn.' }
    $result=Read-BFJson $resultPath
    Assert-BFFields $result @('schema_version','status','summary','payload_json') @() 'worker_result'
    if ($result.schema_version -ne 1 -or $result.status -notin @('completed','needs_input','blocked','failed')) { throw 'BF_INVALID: unsupported worker result.' }
    Assert-BFText $result.summary 'worker summary'
    $metadata=[ordered]@{session_id=$session;requested_model=$model;requested_effort=$effort;observed_model=$null;observed_effort=$null;usage=$usage;usage_source=$process.stdout}
    if(Test-Path -LiteralPath $hostPath -PathType Leaf){if((Get-BFHash (Read-BFJson $hostPath)) -ne (Get-BFHash $metadata)){throw 'BF_BLOCKED: cached host evidence no longer matches raw events.'}}
    else{Write-BFJson -Path $hostPath -Value $metadata}
    return $result
}
