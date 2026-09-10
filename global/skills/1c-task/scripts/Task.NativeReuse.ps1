#Requires -Version 7.0
Set-StrictMode -Version Latest

function Get-BFNativeLoadedProof {
    param($State,$Criterion,$Source,$Inventory)

    $priorId=Get-BFValue $Criterion.native_1c 'reuse_load_attempt'
    if($null -eq $priorId){return $null}
    Assert-BFUuid $priorId
    $task=Get-BFTaskDirectory $State.project_path $State.task_id
    $prior=Join-Path $task ('attempts/'+$priorId)
    $entries=@($State.evidence | Where-Object { $_.attempt_id -ceq $priorId -and $_.stage -ceq 'verify' -and $_.outcome -in @('BLOCKED','FAIL') })
    if($entries.Count -ne 1){throw 'BF_BLOCKED: reuse requires an exact recorded unsuccessful native verification.'}
    $result=Read-BFJson (Join-Path $prior 'result.json')
    if((Get-BFHash $result) -cne $entries[0].result_sha256){throw 'BF_BLOCKED: prior native result changed.'}
    foreach($raw in $result.raw_hashes){
        $path=Assert-BFSafePath $raw.path
        if(-not $path.StartsWith($prior+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase) -or (Get-BFFileHash $path) -cne $raw.sha256){throw 'BF_BLOCKED: prior native evidence changed.'}
    }
    $rawRoot=Join-Path $prior ('raw/'+$Criterion.id)
    $request=Read-BFJson (Join-Path $rawRoot 'runtime-request.json')
    $start=Read-BFJson (Join-Path $prior 'start.json')
    $snapshot=Get-BFNativeSource (Join-Path $rawRoot 'source-snapshot')
    $manifest=Get-BFSourceManifest $State
    if($request.task_id -cne $State.task_id -or $request.attempt_id -cne $priorId -or $request.target -ine $Criterion.target -or (@($request.operations) -join ',') -cne 'inventory,load,update,test'){
        throw 'BF_BLOCKED: reuse must refer to an original full native attempt in this task and target.'
    }
    if($request.source.extension -cne $Criterion.native_1c.extension -or $snapshot.sha256 -cne $Source.sha256 -or $request.source.sha256 -cne $Source.sha256 -or $start.source_manifest.sha256 -cne $manifest.sha256){throw 'BF_BLOCKED: loaded source differs from current source.'}
    $platformHash=Get-BFHash @((Get-BFNativeDependencies $Criterion))
    if($start.dependencies.native_platform -cne $platformHash -or (Get-BFHash $request.expected_tests) -cne (Get-BFHash $Criterion.expected_tests)){
        throw 'BF_BLOCKED: native platform or selected tests changed since load.'
    }
    $config=Read-BFJson (Join-Path $rawRoot 'test-config.json')
    if((@($config.filter.modules) -join ',') -cne $Criterion.native_1c.module){throw 'BF_BLOCKED: native test module changed since load.'}
    $requestHash=Get-BFHash $request
    $terminals=@()
    foreach($step in @('load','update')){
        $path=Join-Path $rawRoot ('steps/'+$step+'/terminal.json')
        $terminal=Read-BFJson $path
        $log=Assert-BFSafePath $terminal.log
        if($terminal.request_sha256 -cne $requestHash -or $terminal.exit_code -ne 0 -or -not $log.StartsWith($rawRoot+[IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase) -or (Get-BFFileHash $log) -cne $terminal.log_sha256){throw 'BF_BLOCKED: successful original load/update receipts are required.'}
        $terminals+=,[ordered]@{step=$step;sha256=Get-BFFileHash $path}
    }
    $matched=$null
    foreach($event in @($State.events | Where-Object kind -eq 'recovery')){
        $inputPath=Join-Path $task ('inputs/'+$event.input_event_id+'.json')
        $input=Read-BFJson $inputPath
        if((Get-BFHash $input) -cne $event.sha256){throw 'BF_BLOCKED: registered recovery input changed.'}
        if($input.resolution.attempt_id -cne $priorId){continue}
        $receiptPath=Join-Path $task ('inputs/recovery-'+$event.input_event_id+'.json')
        $receipt=Read-BFJson $receiptPath
        if((Get-BFHash $receipt.resolution) -cne (Get-BFHash $input.resolution) -or $receipt.resolution.scope -cne 'native_1c' -or $receipt.resolution.retry_authorized -ne $true -or $receipt.actual_source_manifest.sha256 -cne $manifest.sha256){throw 'BF_BLOCKED: loaded source has no matching committed recovery.'}
        $observed=Read-BFJson $receipt.runtime_control_read.inventory_path
        $inventoryHash=Get-BFNativeInventoryHash $observed
        if($inventoryHash -cne $receipt.resolution.inventory_sha256 -or $inventoryHash -cne (Get-BFNativeInventoryHash $Inventory)){throw 'BF_BLOCKED: inventory changed after native recovery.'}
        Assert-BFNativeInventoryTransition (Read-BFJson (Join-Path $rawRoot 'inventory-before.json')) $observed $Source
        $matched=[ordered]@{path=$receiptPath;sha256=Get-BFFileHash $receiptPath;inventory_sha256=$inventoryHash}
    }
    if($null -eq $matched -or $null -ne $State.unresolved_effect){throw 'BF_BLOCKED: test-only continuation requires committed control-read recovery.'}
    return [ordered]@{schema_version=1;kind='bsl-flow.native-loaded-proof';task_id=$State.task_id;loaded_attempt_id=$priorId;result_sha256=$entries[0].result_sha256;request_sha256=$requestHash;source_sha256=$Source.sha256;full_source_sha256=$manifest.sha256;terminals=$terminals;recovery=$matched}
}
