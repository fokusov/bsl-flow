#Requires -Version 7.0
Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot 'Task.Toolsets.ps1')

function Assert-BFRuntimePin {
    param($Runtime)
    Assert-BFFields $Runtime @('executable','sha256','version','packages') @() 'execution_profile.runtime'
    Assert-BFText $Runtime.executable 'execution_profile.runtime.executable'
    if(-not [IO.Path]::IsPathRooted($Runtime.executable) -or [IO.Path]::GetExtension($Runtime.executable) -cne '.exe' -or $Runtime.sha256 -cnotmatch '^[0-9a-f]{64}$'){throw 'BF_INVALID: pinned runtime requires an absolute native executable and SHA-256.'}
    if($Runtime.version -cnotmatch '^[0-9]+\.[0-9]+\.[0-9]+$'){throw 'BF_INVALID: pinned runtime version must be an exact three-part version.'}
    if($Runtime.packages -isnot [array] -or $Runtime.packages.Count -eq 0){throw 'BF_INVALID: pinned runtime requires at least one exact package.'}
    $seen=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach($package in $Runtime.packages){
        Assert-BFFields $package @('name','version') @() 'execution_profile.runtime.package'
        if($package.name -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$' -or -not $seen.Add([string]$package.name)){throw 'BF_INVALID: invalid or duplicate pinned runtime package.'}
        Assert-BFText $package.version 'execution_profile.runtime.package.version' 64
    }
    if(-not $seen.Contains('lxml')){throw 'BF_INVALID: the pinned cc-1c-skills runtime must declare the mandatory lxml package.'}
}

function Assert-BFExecutionProfile {
    param($Profile)
    Assert-BFFields $Profile @('provider','executable','executable_sha256','sandbox','toolset','denied_read_roots') @('unica','codex_skills_sha256','runtime') 'execution_profile'
    if($Profile.provider -cnotin @('codex','opencode')){throw 'BF_INVALID: unsupported managed provider.'}
    if($Profile.provider -eq 'codex'){
        if((Get-BFValue $Profile 'codex_skills_sha256') -cnotmatch '^[0-9a-f]{64}$'){throw 'BF_INVALID: Codex requires a pinned automatic skill inventory.'}
    }elseif(Test-BFCoverageProperty $Profile 'codex_skills_sha256'){throw 'BF_INVALID: OpenCode cannot use a Codex skill inventory.'}
    Assert-BFText $Profile.executable 'execution_profile.executable'
    if(-not [IO.Path]::IsPathRooted($Profile.executable) -or [IO.Path]::GetExtension($Profile.executable) -cne '.exe' -or $Profile.executable_sha256 -cnotmatch '^[0-9a-f]{64}$'){throw 'BF_INVALID: provider requires an absolute native executable and SHA-256.'}
    Assert-BFFields $Profile.sandbox @('executable','sha256') @() 'sandbox'
    Assert-BFText $Profile.sandbox.executable 'sandbox.executable'
    if(-not [IO.Path]::IsPathRooted($Profile.sandbox.executable) -or [IO.Path]::GetExtension($Profile.sandbox.executable) -cne '.exe' -or $Profile.sandbox.sha256 -cnotmatch '^[0-9a-f]{64}$'){throw 'BF_INVALID: sandbox requires an absolute native executable and SHA-256.'}
    Assert-BFFields $Profile.toolset @('name','root','sha256') @() 'toolset'
    if($Profile.toolset.name -cnotin @('unica','cc-1c-skills') -or $Profile.toolset.root -isnot [string] -or -not [IO.Path]::IsPathRooted($Profile.toolset.root) -or $Profile.toolset.sha256 -cnotmatch '^[0-9a-f]{64}$'){throw 'BF_INVALID: invalid toolset identity.'}
    if($Profile.denied_read_roots -isnot [array] -or $Profile.denied_read_roots.Count -eq 0){throw 'BF_INVALID: benchmark profiles require explicit private read-denied roots.'}
    foreach($path in $Profile.denied_read_roots){if($path -isnot [string] -or -not [IO.Path]::IsPathRooted($path)){throw 'BF_INVALID: denied roots must be absolute paths.'}}
    $unica=Get-BFValue $Profile 'unica'
    if($Profile.toolset.name -eq 'unica'){
        Assert-BFFields $unica @('plugin_root','bootstrap_sha256','manifest_sha256','runtime_cache','allowed_tools') @() 'unica'
        foreach($field in @('plugin_root','runtime_cache')){Assert-BFText $unica.$field $field;if(-not [IO.Path]::IsPathRooted($unica.$field)){throw 'BF_INVALID: Unica paths must be absolute.'}}
        foreach($field in @('bootstrap_sha256','manifest_sha256')){if($unica.$field -cnotmatch '^[0-9a-f]{64}$'){throw 'BF_INVALID: missing Unica executable/manifest identity.'}}
        if($unica.allowed_tools -isnot [array] -or $unica.allowed_tools.Count -eq 0){throw 'BF_INVALID: exact Unica source tool allowlist required.'}
        # Only source operations are exposed. Runtime remains controller-owned.
        foreach($name in $unica.allowed_tools){if($name -cnotin @('unica.project.map','unica.code.patch') -and $name -cnotmatch '^unica\.(cf|cfe|meta|form|skd|dcs|mxl|role|subsystem|interface|template|code)\.(info|validate|diff|search|diagnostics|add|edit|remove|compile|init|borrow|patch_method)$'){throw "BF_INVALID: Unica tool is outside the source-only profile: $name"}}
    } elseif(Test-BFCoverageProperty $Profile 'unica'){throw 'BF_INVALID: cc-1c-skills profile cannot load Unica MCP.'}
    $runtime=Get-BFValue $Profile 'runtime'
    if($Profile.toolset.name -eq 'cc-1c-skills'){
        if($null -eq $runtime){throw 'BF_INVALID: cc-1c-skills requires a pinned runtime.'}
        Assert-BFRuntimePin $runtime
    } elseif(Test-BFCoverageProperty $Profile 'runtime'){throw 'BF_INVALID: Unica has a separate runtime contract; a pinned runtime block is forbidden.'}
}

function Get-BFExecutionDependencies {
    param($State)
    $profile=$State.request.execution_profile
    Assert-BFExecutionProfile $profile
    $exe=Assert-BFSafePath $profile.executable
    if((Get-BFFileHash $exe) -cne $profile.executable_sha256){throw 'BF_BLOCKED: provider executable changed.'}
    if((Get-BFFileHash (Assert-BFSafePath $profile.sandbox.executable)) -cne $profile.sandbox.sha256){throw 'BF_BLOCKED: sandbox executable changed.'}
    $snapshot=Test-BFToolsetSnapshot -Root (Assert-BFSafePath $profile.toolset.root) -ExpectedToolset $profile.toolset.name
    if($snapshot.aggregate_sha256 -cne $profile.toolset.sha256){throw 'BF_BLOCKED: toolset differs from the registered snapshot.'}
    if($profile.toolset.name -eq 'cc-1c-skills'){
        if((Get-BFFileHash (Assert-BFSafePath $profile.runtime.executable)) -cne $profile.runtime.sha256){throw 'BF_BLOCKED: pinned cc-1c-skills runtime executable changed.'}
    }
    if($profile.toolset.name -eq 'unica'){
        $bootstrap=Assert-BFSafePath (Join-Path $profile.unica.plugin_root 'bootstrap/bin/win-x64/unica-bootstrap.exe')
        $manifest=Assert-BFSafePath (Join-Path $profile.unica.plugin_root 'runtime-manifest.json')
        if((Get-BFFileHash $bootstrap) -cne $profile.unica.bootstrap_sha256 -or (Get-BFFileHash $manifest) -cne $profile.unica.manifest_sha256){throw 'BF_BLOCKED: Unica bootstrap/manifest changed.'}
        $runtime=Read-BFJson $manifest
        if($runtime.pluginVersion -cne '0.12.3'){throw 'BF_BLOCKED: unverified Unica runtime version.'}
        foreach($file in $runtime.targets.'win-x64'.files){
            Assert-BFRelativePath $file.path
            $path=Assert-BFSafePath (Join-Path $profile.unica.runtime_cache ('0.12.3/win-x64/'+$file.path))
            if((Get-BFFileHash $path) -cne $file.sha256){throw 'BF_BLOCKED: pinned Unica runtime file differs.'}
        }
    }
    return [ordered]@{profile=$profile;models=$State.request.models}
}

function Test-BFRuntimePreflight {
    # BFI-003: run only the exact pinned interpreter before a paid dispatch.
    # The controller never searches PATH, switches interpreters or installs.
    param($State,[string]$Directory,[string]$EvidenceRoot='')
    $profile=Get-BFValue $State.request 'execution_profile'
    if($null -eq $profile -or $profile.toolset.name -ne 'cc-1c-skills'){return $null}
    $runtime=$profile.runtime
    Assert-BFRuntimePin $runtime
    $executable=Assert-BFSafePath $runtime.executable
    if((Get-BFFileHash $executable) -cne $runtime.sha256){throw 'BF_BLOCKED: pinned cc-1c-skills runtime executable changed before preflight.'}
    # Controller-side content-addressed evidence. It must never create or write
    # the provider dispatch directory, which the adapter reserves for itself.
    $expected=[ordered]@{executable=$executable;sha256=$runtime.sha256;version=$runtime.version;packages=@($runtime.packages)}
    $identity=Get-BFHash $expected
    $evidenceRoot=if([string]::IsNullOrWhiteSpace($EvidenceRoot)){Join-Path (Get-BFTaskDirectory $State.project_path $State.task_id) 'runtime'}else{Assert-BFSafePath $EvidenceRoot}
    $evidencePath=Join-Path $evidenceRoot ('preflight-'+$identity+'.json')
    # The probe runs before every paid dispatch: a package-only change does not
    # alter the executable hash, so caching observed results would hide drift.
    [void][IO.Directory]::CreateDirectory($evidenceRoot)
    # A task may dispatch several times (for example critic then reconcile), so
    # every preflight gets its own probe directory; the evidence file is shared.
    $directory=Join-Path $evidenceRoot ('probe-'+$identity+'-'+[guid]::NewGuid().ToString('N'))
    [void][IO.Directory]::CreateDirectory($directory)
    $probe=@'
import json,sys
try:
    from importlib import metadata
except Exception:
    metadata=None
required=json.loads(sys.argv[1])
versions={}
for name in required:
    try:
        versions[name]=metadata.version(name) if metadata is not None else None
    except Exception:
        versions[name]=None
sys.stdout.write(json.dumps({"sys_executable":sys.executable,"version":"%d.%d.%d"%sys.version_info[:3],"packages":versions}))
'@
    $probePath=Join-Path $directory 'runtime-probe.py'
    [IO.File]::WriteAllText($probePath,$probe,[Text.UTF8Encoding]::new($false))
    $probeOutput=Join-Path $directory 'runtime-probe'
    [void][IO.Directory]::CreateDirectory($probeOutput)
    $namesJson=ConvertTo-Json -InputObject @($runtime.packages|ForEach-Object{$_.name}) -Compress
    $process=Invoke-BFProcess -Executable $executable -Arguments @('-I',$probePath,$namesJson) -WorkingDirectory $State.worker_path -OutputDirectory $probeOutput -TimeoutSeconds 60 -CleanEnvironment -MaxOutputBytes 65536
    if($process.stop_reason -or $process.exit_code -ne 0){throw 'BF_BLOCKED: pinned cc-1c-skills runtime preflight did not finish.'}
    $text=[IO.File]::ReadAllText($process.stdout).Trim()
    if([string]::IsNullOrWhiteSpace($text)){throw 'BF_BLOCKED: pinned runtime preflight produced no evidence.'}
    try{$observed=ConvertFrom-Json -InputObject $text -ErrorAction Stop}catch{throw 'BF_BLOCKED: pinned runtime preflight produced invalid JSON.'}
    Assert-BFFields $observed @('sys_executable','version','packages') @() 'runtime_preflight_observation'
    $observedExe=Assert-BFSafePath ([string]$observed.sys_executable)
    if(-not [string]::Equals($observedExe,$executable,[StringComparison]::OrdinalIgnoreCase)){throw 'BF_BLOCKED: pinned runtime reported a different interpreter.'}
    if([string]$observed.version -cne $runtime.version){throw 'BF_BLOCKED: pinned runtime version differs from the trusted request.'}
    $observedPackages=@()
    foreach($package in $runtime.packages){
        $actual=Get-BFObjectProperty $observed.packages $package.name
        if($actual -isnot [string] -or $actual -cne $package.version){throw "BF_BLOCKED: pinned runtime package $($package.name) differs from the trusted request."}
        $observedPackages+=[ordered]@{name=$package.name;version=$actual}
    }
    $evidence=[ordered]@{declared=$expected;observed=[ordered]@{sys_executable=$observedExe;version=[string]$observed.version;packages=$observedPackages};probe_sha256=Get-BFFileHash $probePath;checked_at_utc=[DateTime]::UtcNow.ToString('o')}
    Write-BFJson -Path $evidencePath -Value $evidence -Replace
    return $evidence
}

function Get-BFBudgetLedgerPath {
    param($State)
    return Join-Path (Join-Path (Get-BFTaskDirectory $State.project_path $State.task_id) 'budget') 'ledger.json'
}

function Read-BFBudgetLedger {
    param($State)
    $path=Get-BFBudgetLedgerPath $State
    if(-not(Test-Path -LiteralPath $path -PathType Leaf)){return [ordered]@{schema_version=1;task_id=$State.task_id;entries=@()}}
    $ledger=Read-BFJson $path
    Assert-BFFields $ledger @('schema_version','task_id','entries') @() 'budget_ledger'
    if($ledger.schema_version -ne 1 -or $ledger.task_id -ne $State.task_id -or $ledger.entries -isnot [array]){throw 'BF_BLOCKED: corrupt budget ledger.'}
    foreach($entry in $ledger.entries){
        Assert-BFFields $entry @('kind','dispatch') @('provider','stage','requested_model','observed_model','reservation_usd','reported_cost_usd','billed_cost_usd','cost_state','usage','terminal_status','currency','at') 'budget_entry'
        if($entry.kind -notin @('reservation','outcome') -or $entry.dispatch -isnot [string]){throw 'BF_BLOCKED: corrupt budget ledger entry.'}
    }
    return $ledger
}

function Get-BFDispatchKey {
    param($State,[string]$Directory)
    $taskDirectory=(Get-BFTaskDirectory $State.project_path $State.task_id).TrimEnd('\','/')+'\'
    $full=Assert-BFSafePath $Directory
    if(-not $full.StartsWith($taskDirectory,[StringComparison]::OrdinalIgnoreCase)){throw 'BF_INVALID: paid dispatch directory must be inside the task.'}
    return $full.Substring($taskDirectory.Length).Replace('\','/')
}

function Add-BFBudgetEntry {
    param($State,$Entry)
    $taskDirectory=Get-BFTaskDirectory $State.project_path $State.task_id
    $lock=Enter-BFLock $taskDirectory
    try {
        $ledger=Read-BFBudgetLedger $State
        $ledger.entries=@($ledger.entries)+@($Entry)
        Write-BFJson -Path (Get-BFBudgetLedgerPath $State) -Value $ledger -Replace
    } finally { $lock.Dispose() }
}

function New-BFBudgetOutcomeEntry {
    param($State,[string]$Directory,[string]$Provider,[string]$Stage,[string]$RequestedModel)
    $key=Get-BFDispatchKey $State $Directory
    $reported=$null;$observed=$null;$usage=$null;$costState='unknown';$terminal='unknown'
    $hostPath=Join-Path (Assert-BFSafePath $Directory) 'host-result.json'
    if(Test-Path -LiteralPath $hostPath -PathType Leaf){
        $metadata=Read-BFJson $hostPath
        $usage=Get-BFValue $metadata 'usage'
        $observed=Get-BFValue $metadata 'observed_model'
        $cost=Get-BFValue $metadata 'reported_cost_usd'
        if($null -ne $cost){
            if($cost -isnot [int] -and $cost -isnot [long] -and $cost -isnot [double] -and $cost -isnot [decimal] -and $cost -isnot [single]){throw 'BF_BLOCKED: invalid reported provider cost.'}
            $reported=[double]$cost;$costState='known'
        }
        $terminal='completed'
    }
    return [ordered]@{kind='outcome';dispatch=$key;provider=$Provider;stage=$Stage;requested_model=$RequestedModel;observed_model=$observed;reported_cost_usd=$reported;billed_cost_usd=$null;cost_state=$costState;usage=$usage;terminal_status=$terminal;at=[DateTime]::UtcNow.ToString('o')}
}

function Get-BFBudgetEntryCore {
    # Idempotency ignores the observation timestamp; replay must stay stable.
    param($Entry)
    $keys=if($Entry -is [System.Collections.IDictionary]){@($Entry.Keys)}else{@($Entry.PSObject.Properties.Name)}
    $copy=[ordered]@{}
    foreach($key in $keys){if([string]$key -ceq 'at'){continue};$copy[[string]$key]=Get-BFValue $Entry $key}
    return $copy
}

function Add-BFBudgetReservation {
    param($State,[string]$Directory,[string]$Provider,[string]$Stage,[string]$RequestedModel)
    $budget=Get-BFValue $State.request 'budget'
    if($null -eq $budget){return}
    $key=Get-BFDispatchKey $State $Directory
    $entry=[ordered]@{kind='reservation';dispatch=$key;provider=$Provider;stage=$Stage;requested_model=$RequestedModel;reservation_usd=[double]$budget.reservation;currency=$budget.currency;at=[DateTime]::UtcNow.ToString('o')}
    $ledger=Read-BFBudgetLedger $State
    $existing=@($ledger.entries|Where-Object{$_.kind -eq 'reservation' -and $_.dispatch -eq $key})
    if($existing.Count -gt 0){
        if((Get-BFHash (Get-BFBudgetEntryCore $existing[0])) -cne (Get-BFHash (Get-BFBudgetEntryCore $entry))){throw 'BF_BLOCKED: conflicting budget reservation for this dispatch.'}
        return
    }
    Add-BFBudgetEntry $State $entry
}

function Resolve-BFBudgetOpenReservations {
    # A crash may leave a reservation without its outcome. When the preserved
    # terminal receipt exists, derive the outcome from it; otherwise stay open.
    param($State)
    $ledger=Read-BFBudgetLedger $State
    $resolved=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach($entry in $ledger.entries){if($entry.kind -eq 'outcome'){[void]$resolved.Add([string]$entry.dispatch)}}
    foreach($reservation in @($ledger.entries|Where-Object{$_.kind -eq 'reservation'})){
        if($resolved.Contains([string]$reservation.dispatch)){continue}
        $directory=Join-Path (Get-BFTaskDirectory $State.project_path $State.task_id) ([string]$reservation.dispatch -replace '/','\')
        if(-not(Test-Path -LiteralPath (Join-Path $directory 'host-result.json') -PathType Leaf)){continue}
        Add-BFBudgetEntry $State (New-BFBudgetOutcomeEntry $State $directory $reservation.provider $reservation.stage $reservation.requested_model)
        [void]$resolved.Add([string]$reservation.dispatch)
    }
}

function Get-BFBudgetSummary {
    param($State)
    $ledger=Read-BFBudgetLedger $State
    $reservationKeys=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    $outcomeByKey=[Collections.Generic.Dictionary[string,object]]::new([StringComparer]::Ordinal)
    foreach($entry in $ledger.entries){
        $key=[string]$entry.dispatch
        if($entry.kind -eq 'reservation'){[void]$reservationKeys.Add($key)}
        elseif($outcomeByKey.ContainsKey($key)){throw 'BF_BLOCKED: duplicate budget outcome for one dispatch.'}
        else{$outcomeByKey[$key]=$entry}
    }
    $spent=[double]0;$open=0;$unknown=0;$known=0
    foreach($key in $reservationKeys){if(-not $outcomeByKey.ContainsKey($key)){$open++}}
    foreach($entry in $outcomeByKey.Values){
        if($entry.cost_state -ceq 'known' -and $null -ne $entry.reported_cost_usd){$spent+=[double]$entry.reported_cost_usd;$known++}else{$unknown++}
    }
    return [ordered]@{spent_usd=[math]::Round($spent,10);open=$open;unknown=$unknown;known=$known;reservations=$reservationKeys.Count;outcomes=$outcomeByKey.Count}
}

function Assert-BFBudgetAdmission {
    param($State,[string]$Directory)
    $budget=Get-BFValue $State.request 'budget'
    if($null -eq $budget){return $null}
    Resolve-BFBudgetOpenReservations $State
    $key=Get-BFDispatchKey $State $Directory
    $ledger=Read-BFBudgetLedger $State
    if(@($ledger.entries|Where-Object{$_.dispatch -eq $key}).Count -eq 0){
        $summary=Get-BFBudgetSummary $State
        if($summary.open -gt 0){throw 'BF_BLOCKED: an unresolved paid dispatch exists; reconcile the budget ledger before dispatch.'}
        if($null -ne $budget.limit){
            if($summary.unknown -gt 0){throw 'BF_BLOCKED: a prior paid dispatch has unknown cost; reconcile the budget ledger before dispatch.'}
            if(($summary.spent_usd+[double]$budget.reservation) -gt ([double]$budget.limit+1e-9)){throw 'BF_BLOCKED: the budget limit would be exceeded before dispatch.'}
        }
        return $summary
    }
    return Get-BFBudgetSummary $State
}

function Complete-BFBudgetDispatch {
    param($State,[string]$Directory,[string]$Provider,[string]$Stage,[string]$RequestedModel)
    $budget=Get-BFValue $State.request 'budget'
    if($null -eq $budget){return}
    $key=Get-BFDispatchKey $State $Directory
    $candidate=New-BFBudgetOutcomeEntry $State $Directory $Provider $Stage $RequestedModel
    $ledger=Read-BFBudgetLedger $State
    $existing=@($ledger.entries|Where-Object{$_.kind -eq 'outcome' -and $_.dispatch -eq $key})
    if($existing.Count -gt 0){
        if((Get-BFHash (Get-BFBudgetEntryCore $existing[0])) -cne (Get-BFHash (Get-BFBudgetEntryCore $candidate))){throw 'BF_BLOCKED: conflicting budget outcome for this dispatch.'}
        return
    }
    Add-BFBudgetEntry $State $candidate
}

function Get-BFProviderBudgetLedger {
    param($ProviderContext)
    $contextRoot=Assert-BFSafePath (Get-BFValue $ProviderContext 'context_root' '')
    $artifactRoot=Assert-BFSafePath (Get-BFValue $ProviderContext 'artifact_root' '')
    $artifactPath=Join-Path $artifactRoot 'budget/ledger.json'
    $contextPath=Join-Path $contextRoot 'budget/ledger.json'
    if(Test-Path -LiteralPath $artifactPath -PathType Leaf){$ledger=Read-BFJson $artifactPath}
    elseif(Test-Path -LiteralPath $contextPath -PathType Leaf){
        $reader=Get-Command Get-BFProviderContextArtifact -CommandType Function -ErrorAction SilentlyContinue
        if($null -eq $reader){throw 'BF_BLOCKED: provider context artifact resolver is unavailable.'}
        $ledger=& $reader -ContextRoot $contextRoot -RelativePath 'budget/ledger.json' -AsJson
    }else{$ledger=[ordered]@{schema_version=1;task_id=$ProviderContext.task_id;entries=@()}}
    Assert-BFFields $ledger @('schema_version','task_id','entries') @() 'provider_budget_ledger'
    if($ledger.schema_version -ne 1 -or $ledger.task_id -cne $ProviderContext.task_id -or $ledger.entries -isnot [array]){throw 'BF_BLOCKED: corrupt provider budget ledger.'}
    foreach($entry in $ledger.entries){Assert-BFFields $entry @('kind','dispatch') @('provider','stage','requested_model','observed_model','reservation_usd','reported_cost_usd','billed_cost_usd','cost_state','usage','terminal_status','currency','at') 'provider_budget_entry';if($entry.kind -notin @('reservation','outcome')){throw 'BF_BLOCKED: invalid provider budget entry kind.'}}
    return $ledger
}

function Get-BFProviderDispatchKey {
    param($ProviderContext,[string]$Directory)
    $root=(Assert-BFSafePath $ProviderContext.artifact_root).TrimEnd('\','/')
    $full=Assert-BFSafePath $Directory
    if(-not ($full -eq $root -or $full.StartsWith($root+'\',[StringComparison]::OrdinalIgnoreCase))){throw 'BF_INVALID: provider dispatch directory escaped artifact_root.'}
    $relative=$full.Substring($root.Length).TrimStart('\','/').Replace('\','/')
    if([string]::IsNullOrWhiteSpace($relative)){throw 'BF_INVALID: provider dispatch directory is empty.'}
    return 'attempts/'+$ProviderContext.attempt_id+'/provider/'+$relative
}

function Get-BFProviderBudgetSummary {
    param($Ledger)
    $reservations=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal);$outcomes=[Collections.Generic.Dictionary[string,object]]::new([StringComparer]::Ordinal);$spent=[double]0;$open=0
    foreach($entry in $Ledger.entries){$key=[string]$entry.dispatch;if($entry.kind -eq 'reservation'){[void]$reservations.Add($key)}elseif($outcomes.ContainsKey($key)){throw 'BF_BLOCKED: duplicate provider budget outcome.'}else{$outcomes[$key]=$entry}}
    foreach($key in $reservations){if(-not $outcomes.ContainsKey($key)){$open++}}
    foreach($entry in $outcomes.Values){if($entry.cost_state -ceq 'known' -and $null -ne $entry.reported_cost_usd){$spent+=[double]$entry.reported_cost_usd}}
    return [ordered]@{spent_usd=[math]::Round($spent,10);open=$open;unknown=(@($outcomes.Values|Where-Object{$_.cost_state -cne 'known'})).Count;reservations=$reservations.Count;outcomes=$outcomes.Count}
}

function Add-BFProviderBudgetEntry {
    param($ProviderContext,$Entry)
    $ledger=Get-BFProviderBudgetLedger $ProviderContext
    $ledger.entries=@($ledger.entries)+@($Entry)
    Write-BFJson -Path (Join-Path (Assert-BFSafePath $ProviderContext.artifact_root) 'budget/ledger.json') -Value $ledger -Replace
}

function Assert-BFProviderBudgetAdmission {
    param($State,$ProviderContext,[string]$Directory)
    $budget=Get-BFValue $State.request 'budget';if($null -eq $budget){return $null}
    $key=Get-BFProviderDispatchKey $ProviderContext $Directory;$ledger=Get-BFProviderBudgetLedger $ProviderContext
    if(@($ledger.entries|Where-Object{$_.dispatch -ceq $key}).Count -eq 0){$summary=Get-BFProviderBudgetSummary $ledger;if($summary.open -gt 0){throw 'BF_BLOCKED: an unresolved provider paid dispatch exists.'};if($null -ne $budget.limit -and $summary.unknown -gt 0){throw 'BF_BLOCKED: a provider paid dispatch has unknown cost.'};if($null -ne $budget.limit -and ($summary.spent_usd+[double]$budget.reservation) -gt ([double]$budget.limit+1e-9)){throw 'BF_BLOCKED: provider budget limit would be exceeded.'};return $summary}
    return Get-BFProviderBudgetSummary $ledger
}

function Add-BFProviderBudgetReservation {
    param($State,$ProviderContext,[string]$Directory,[string]$Provider,[string]$Stage,[string]$RequestedModel)
    $budget=Get-BFValue $State.request 'budget';if($null -eq $budget){return}
    $key=Get-BFProviderDispatchKey $ProviderContext $Directory;$entry=[ordered]@{kind='reservation';dispatch=$key;provider=$Provider;stage=$Stage;requested_model=$RequestedModel;reservation_usd=[double]$budget.reservation;currency=$budget.currency;at=[DateTime]::UtcNow.ToString('o')};$ledger=Get-BFProviderBudgetLedger $ProviderContext;$existing=@($ledger.entries|Where-Object{$_.kind -eq 'reservation' -and $_.dispatch -ceq $key});if($existing.Count -gt 0){if((Get-BFHash (Get-BFBudgetEntryCore $existing[0])) -cne (Get-BFHash (Get-BFBudgetEntryCore $entry))){throw 'BF_BLOCKED: conflicting provider budget reservation.'};return};Add-BFProviderBudgetEntry $ProviderContext $entry
}

function Complete-BFProviderBudgetDispatch {
    param($State,$ProviderContext,[string]$Directory,[string]$Provider,[string]$Stage,[string]$RequestedModel)
    $budget=Get-BFValue $State.request 'budget';if($null -eq $budget){return}
    $key=Get-BFProviderDispatchKey $ProviderContext $Directory;$hostPath=Join-Path (Assert-BFSafePath $Directory) 'host-result.json';$reported=$null;$observed=$null;$usage=$null;$costState='unknown';$terminal='unknown'
    if(Test-Path -LiteralPath $hostPath -PathType Leaf){$metadata=Read-BFJson $hostPath;$usage=Get-BFValue $metadata 'usage';$observed=Get-BFValue $metadata 'observed_model';$cost=Get-BFValue $metadata 'reported_cost_usd';if($null -ne $cost){$reported=[double]$cost;$costState='known'};$terminal='completed'}
    $candidate=[ordered]@{kind='outcome';dispatch=$key;provider=$Provider;stage=$Stage;requested_model=$RequestedModel;observed_model=$observed;reported_cost_usd=$reported;billed_cost_usd=$null;cost_state=$costState;usage=$usage;terminal_status=$terminal;currency=$budget.currency;at=[DateTime]::UtcNow.ToString('o')};$ledger=Get-BFProviderBudgetLedger $ProviderContext;$existing=@($ledger.entries|Where-Object{$_.kind -eq 'outcome' -and $_.dispatch -ceq $key});if($existing.Count -gt 0){if((Get-BFHash (Get-BFBudgetEntryCore $existing[0])) -cne (Get-BFHash (Get-BFBudgetEntryCore $candidate))){throw 'BF_BLOCKED: conflicting provider budget outcome.'};return};Add-BFProviderBudgetEntry $ProviderContext $candidate
}

function Resolve-BFExecutionCanonicalStoreRoot {
    param($State,[string]$CanonicalStoreRoot='')
    if(-not [string]::IsNullOrWhiteSpace($CanonicalStoreRoot)){return Assert-BFSafePath $CanonicalStoreRoot}
    $registered=Get-BFValue $State 'canonical_store_root' ''
    if(-not [string]::IsNullOrWhiteSpace([string]$registered)){return Assert-BFSafePath ([string]$registered)}
    $project=Assert-BFSafePath $State.project_path
    $common=$null
    $gitCommand=Get-Command Invoke-BFGit -CommandType Function -ErrorAction SilentlyContinue
    if($null -ne $gitCommand){$common=[string](Invoke-BFGit $project @('rev-parse','--git-common-dir'))}
    else{
        $commonText=& git -c core.hooksPath=NUL -c core.fsmonitor=false -C $project rev-parse --git-common-dir 2>$null
        if($LASTEXITCODE -ne 0){throw 'BF_BLOCKED: unable to resolve the Git common directory for execution fencing.'}
        $common=[string]$commonText.Trim()
    }
    if([IO.Path]::IsPathRooted($common)){$commonPath=Assert-BFSafePath $common}else{$commonPath=Assert-BFSafePath (Join-Path $project $common)}
    return Assert-BFSafePath (Join-Path $commonPath 'bsl-flow')
}

function Get-BFExecutionPermissionProfile {
    param($State,[string]$Scratch,[string]$Config,[bool]$Writable,[string]$CanonicalStoreRoot='')
    $profile=$State.request.execution_profile
    $access=if($Writable){'write'}else{'read'}
    # Denied trees must be disjoint from allowed trees. Windows deny ACLs on an
    # ancestor cannot be undone by a more specific permission entry.
    $entries=[ordered]@{':root'='read'}
    $controller=Assert-BFSafePath (Join-Path $State.project_path '.bsl-flow/tasks')
    $entries[$controller]='none'
    $canonical=Resolve-BFExecutionCanonicalStoreRoot $State $CanonicalStoreRoot
    $privateRoots=@($controller)+@($profile.denied_read_roots)
    if(-not [string]::IsNullOrWhiteSpace($canonical)){$canonical=Assert-BFSafePath $canonical;$privateRoots+=,$canonical;$entries[$canonical]='none'}
    $worker=Assert-BFSafePath $State.worker_path
    $gitPath=Assert-BFSafePath (Join-Path $State.project_path '.git')
    $gitRoots=if([string]::IsNullOrWhiteSpace($canonical)){@($gitPath)}else{
        $roots=@()
        if(Test-Path -LiteralPath $gitPath -PathType Leaf){$roots+=,$gitPath}
        $common=Split-Path $canonical -Parent
        foreach($name in @('HEAD','config','index','packed-refs','commondir','description','refs','objects','logs')){
            $candidate=Assert-BFSafePath (Join-Path $common $name)
            if($candidate -ne $canonical -and -not $candidate.StartsWith($canonical.TrimEnd('\','/')+'\',[StringComparison]::OrdinalIgnoreCase)){$roots+=,$candidate}
        }
        @($roots | Select-Object -Unique)
    }
    $reopened=@($worker)+@($gitRoots)+@($profile.toolset.root,$profile.executable,$profile.sandbox.executable,$Scratch,$Config)
    if($profile.toolset.name -eq 'cc-1c-skills'){$reopened+=$profile.runtime.executable}
    if($profile.toolset.name -eq 'unica'){$reopened+=@($profile.unica.plugin_root,$profile.unica.runtime_cache,(Join-Path $profile.unica.runtime_cache '.locks'))}
    foreach($private in $privateRoots){
        $deny=(Assert-BFSafePath $private).TrimEnd('\','/')
        foreach($path in $reopened){
            $allow=(Assert-BFSafePath $path).TrimEnd('\','/')
            if($deny -eq $allow -or $allow.StartsWith($deny+'\',[StringComparison]::OrdinalIgnoreCase) -or $deny.StartsWith($allow+'\',[StringComparison]::OrdinalIgnoreCase)){throw 'BF_INVALID: a reopened execution root overlaps a declared private root.'}
        }
    }
    $immutable=@($Config,$profile.toolset.root,$profile.executable,$profile.sandbox.executable)+$gitRoots
    if($profile.toolset.name -eq 'cc-1c-skills'){$immutable+=$profile.runtime.executable}
    if($profile.toolset.name -eq 'unica'){$immutable+=@($profile.unica.plugin_root,$profile.unica.runtime_cache)}
    foreach($outside in $immutable){
        foreach($writableRoot in @($worker,$Scratch)){
            $a=(Assert-BFSafePath $outside).TrimEnd('\','/');$b=(Assert-BFSafePath $writableRoot).TrimEnd('\','/')
            if($a -eq $b -or $a.StartsWith($b+'\',[StringComparison]::OrdinalIgnoreCase) -or $b.StartsWith($a+'\',[StringComparison]::OrdinalIgnoreCase)){throw 'BF_INVALID: immutable host inputs overlap writable execution roots.'}
        }
    }
    foreach($path in @($profile.toolset.root,$profile.executable,$Scratch,$Config)){
        $resolved=Assert-BFSafePath $path
        if($resolved -eq (Assert-BFSafePath $State.project_path) -or $resolved -eq $worker){throw 'BF_INVALID: host/config/toolset roots must be separate from source and project roots.'}
    }
    $entries[$worker]=$access
    foreach($gitRoot in $gitRoots){$entries[$gitRoot]='read'}
    $entries[(Assert-BFSafePath $profile.toolset.root)]='read'
    $entries[(Assert-BFSafePath $profile.executable)]='read'
    $entries[(Assert-BFSafePath $Scratch)]='write'
    $entries[(Assert-BFSafePath $Config)]='read'
    if($profile.toolset.name -eq 'cc-1c-skills'){$entries[(Assert-BFSafePath $profile.runtime.executable)]='read'}
    if($profile.toolset.name -eq 'unica'){
        $entries[(Assert-BFSafePath $profile.unica.plugin_root)]='read'
        $entries[(Assert-BFSafePath $profile.unica.runtime_cache)]='read'
        # The public bootstrap locks an already verified cache on every launch.
        # Its executable tree stays read-only; no install transaction is allowed.
        $entries[(Assert-BFSafePath (Join-Path $profile.unica.runtime_cache '.locks'))]='write'
    }
    $fields=@($entries.GetEnumerator()|ForEach-Object{(ConvertTo-Json -InputObject $_.Key.Replace('\','/') -Compress)+'="'+$_.Value+'"'})
    return 'permissions.bsl_execution={filesystem={'+($fields -join ',')+'},network={enabled=true}}'
}

function Get-BFToolsetPrompt {
    param($State)
    $profile=$State.request.execution_profile
    $manifest=Read-BFJson (Join-Path $profile.toolset.root 'toolset-manifest.json')
    $catalog=@($manifest.skills|ForEach-Object{$_.name}) -join ', '
    $runtimeNote=''
    if($profile.toolset.name -eq 'cc-1c-skills'){
        $packages=@($profile.runtime.packages|ForEach-Object{$_.name+'=='+$_.version}) -join ', '
        # The exact interpreter is pinned by the controller; PATH lookup and
        # dependency installation are forbidden. This text is guidance only.
        $runtimeNote=" Use only this pinned Python interpreter for skill scripts: $($profile.runtime.executable) (Python $($profile.runtime.version); required: $packages). Do not call another python, search PATH or install packages."
    }
    return "`nSelected 1C toolset: $($profile.toolset.name). Read only the relevant SKILL.md under this fixed root: $($profile.toolset.root). Skills: $catalog. Legacy relative .opencode/skills paths in these instructions refer to this selected root; use its absolute paths. Do not load any other skillset or install dependencies.$runtimeNote Runtime/database/build/test actions remain controller-owned; tool availability is not runtime authorization."
}

function Get-BFExecutionCanonicalProbePaths {
    param($State,[string]$CanonicalStoreRoot)
    $canonical=Assert-BFSafePath $CanonicalStoreRoot
    Assert-BFUuid $State.task_id
    $taskRoot=Assert-BFSafePath (Join-Path (Join-Path $canonical 'tasks') $State.task_id)
    $probeRoot=Assert-BFSafePath (Join-Path (Join-Path $canonical 'native-provider-probe') $State.task_id)
    $paths=[ordered]@{}

    # Read probes use real canonical task files when they already exist. The
    # activation measure may not have an inputs/activation.json yet, so the
    # controller prepares a short-lived synthetic probe tree under the
    # canonical root. This function never creates that tree or a journal.
    $current=Join-Path $taskRoot 'current.json'
    $revisionDirectory=Join-Path $taskRoot 'revisions'
    $revision=$null
    if(Test-Path -LiteralPath $revisionDirectory -PathType Container){
        $revisionFiles=@(Get-ChildItem -LiteralPath $revisionDirectory -Filter '*.json' -File | Sort-Object Name)
        if($revisionFiles.Count -gt 0){$revision=$revisionFiles[-1].FullName}
    }
    $input=Join-Path (Join-Path $taskRoot 'inputs') 'activation.json'
    if(-not(Test-Path -LiteralPath $input -PathType Leaf)){$input=Join-Path $probeRoot 'inputs/read.json'}
    if(-not(Test-Path -LiteralPath $current -PathType Leaf)){$current=Join-Path $probeRoot 'current/read.json'}
    if($null -eq $revision -or -not(Test-Path -LiteralPath $revision -PathType Leaf)){$revision=Join-Path $probeRoot 'revisions/read.json'}
    $paths.current_read=$current
    $paths.revision_read=$revision
    $paths.inputs_read=$input
    # Writes always target synthetic files. Even if a sandbox accidentally
    # grants access, no canonical current/revision/input bytes are overwritten.
    $paths.current_write=Join-Path $probeRoot 'current/write.txt'
    $paths.revision_write=Join-Path $probeRoot 'revisions/write.txt'
    $paths.inputs_write=Join-Path $probeRoot 'inputs/write.txt'
    foreach($name in @('current_read','revision_read','inputs_read','current_write','revision_write','inputs_write')){
        $path=Assert-BFSafePath ([string]$paths[$name])
        if(-not($path -eq $canonical -or $path.StartsWith($canonical+'\',[StringComparison]::OrdinalIgnoreCase))){throw "BF_INVALID: canonical capability probe escaped the store: $name"}
        if(-not(Test-Path -LiteralPath $path -PathType Leaf)){throw "BF_BLOCKED: canonical capability probe fixture is missing: $name"}
        $paths[$name]=$path
    }
    return $paths
}

function Test-BFExecutionCapability {
    param($State,[string]$Directory,[string]$Scratch,[string]$Config,[string]$Permissions,[bool]$Writable,[string]$CanonicalStoreRoot='')
    $profile=$State.request.execution_profile
    foreach($path in @($Directory,$Scratch,$Config)){
        [void][IO.Directory]::CreateDirectory((Assert-BFSafePath $path))
    }
    $version=Invoke-BFProcess $profile.sandbox.executable @('--version') $State.worker_path '' (Join-Path $Directory 'sandbox-version') 30
    if($version.exit_code -ne 0){throw 'BF_BLOCKED: sandbox version probe failed.'}
    $sandboxVersion=[IO.File]::ReadAllText($version.stdout).Trim()
    [void](Assert-BFCodexHostVersion $sandboxVersion)
    $version=Invoke-BFProcess $profile.executable @('--version') $State.worker_path '' (Join-Path $Directory 'provider-version') 30
    if($version.exit_code -ne 0){throw 'BF_BLOCKED: provider version probe failed.'}
    $providerVersion=[IO.File]::ReadAllText($version.stdout).Trim()
    if($profile.provider -eq 'opencode'){
        if($providerVersion -cne '1.18.30'){throw 'BF_BLOCKED: unverified provider version.'}
    }else{
        [void](Assert-BFCodexHostVersion $providerVersion)
        if($providerVersion -cne $sandboxVersion){throw 'BF_BLOCKED: Codex provider and sandbox versions differ.'}
    }
    $configSentinel=Join-Path $Config 'sentinel.txt'
    [IO.File]::WriteAllText($configSentinel,'config')
    $source=Assert-BFSafePath (Join-Path $State.worker_path '.bsl-flow-worker/host-probe.txt')
    [void][IO.Directory]::CreateDirectory((Split-Path $source -Parent))
    $sourceBefore=if(Test-Path -LiteralPath $source -PathType Leaf){[IO.File]::ReadAllText($source)}else{$null}
    $paths=[ordered]@{config_read=$configSentinel;config_write=$configSentinel;scratch_write=(Join-Path $Scratch 'sentinel.txt');source_write=$source}
    $canonicalPaths=$null
    if(-not [string]::IsNullOrWhiteSpace($CanonicalStoreRoot)){
        $canonicalPaths=Get-BFExecutionCanonicalProbePaths $State $CanonicalStoreRoot
        foreach($name in @('current','revision','inputs')){
            $paths[('canonical_'+$name+'_read')]=$canonicalPaths[($name+'_read')]
            $paths[('canonical_'+$name+'_write')]=$canonicalPaths[($name+'_write')]
        }
    }
    $body=@'
$ErrorActionPreference='Stop'
$paths=PATHS_JSON | ConvertFrom-Json -AsHashtable
$result=[ordered]@{}
function Read-Probe([string]$Name,[string]$Path){
    try{$null=Get-Content -LiteralPath $Path -Raw -ErrorAction Stop;$result[$Name+'_read']='allowed'}
    catch [UnauthorizedAccessException]{$result[$Name+'_read']='denied'}
    catch{$result[$Name+'_read']='error'}
}
function Write-Probe([string]$Name,[string]$Path){
    try{Set-Content -LiteralPath $Path -Value 'probe' -NoNewline -ErrorAction Stop;$result[$Name+'_write']='allowed'}
    catch [UnauthorizedAccessException]{$result[$Name+'_write']='denied'}
    catch{$result[$Name+'_write']='error'}
}
foreach($name in @('canonical_current','canonical_revision','canonical_inputs')){if($paths[($name+'_read')]){Read-Probe $name $paths[($name+'_read')]}}
foreach($name in @('config')){if($paths[($name+'_read')]){Read-Probe $name $paths[($name+'_read')]}}
foreach($name in @('canonical_current','canonical_revision','canonical_inputs','config','scratch','source')){if($paths[($name+'_write')]){Write-Probe $name $paths[($name+'_write')]}}
$result|ConvertTo-Json -Compress
'@
    # PowerShell's `ConvertFrom-Json` accepts a dictionary but property lookup
    # with a dynamic suffix is not supported on every 7.x build; pass a plain
    # object and use the indexer form in the child probe.
    $body=$body.Replace('PATHS_JSON',("'"+(ConvertTo-Json $paths -Compress).Replace("'","''")+"'"))
    $encoded=[Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($body))
    $arguments=@('sandbox','-P','bsl_execution','-c',$Permissions,'-c','windows.sandbox="elevated"','-C',$State.worker_path,(Join-Path $PSHOME 'pwsh.exe'),'-NoProfile','-EncodedCommand',$encoded)
    $process=Invoke-BFProcess $profile.sandbox.executable $arguments $State.worker_path '' (Join-Path $Directory 'filesystem') 45
    if($process.exit_code -ne 0 -or $process.stop_reason){throw 'BF_BLOCKED: managed filesystem capability did not finish.'}
    $actual=Read-BFJson $process.stdout
    $expected=[ordered]@{config_read='allowed';canonical_current_read='denied';canonical_revision_read='denied';canonical_inputs_read='denied';canonical_current_write='denied';canonical_revision_write='denied';canonical_inputs_write='denied';config_write='denied';scratch_write='allowed';source_write=if($Writable){'allowed'}else{'denied'}}
    if((Get-BFHash $actual) -cne (Get-BFHash $expected) -or [IO.File]::ReadAllText($configSentinel) -cne 'config'){throw 'BF_BLOCKED: managed filesystem capability differs from the required observations.'}
    if($null -ne $sourceBefore){
        $sourceAfter=if(Test-Path -LiteralPath $source -PathType Leaf){[IO.File]::ReadAllText($source)}else{$null}
        if($Writable){
            if($sourceAfter -cne 'probe'){throw 'BF_BLOCKED: writable source capability probe did not write the probe file.'}
            [IO.File]::WriteAllText($source,$sourceBefore)
        }elseif($sourceAfter -cne $sourceBefore){throw 'BF_BLOCKED: source capability probe changed an existing protected probe file.'}
    }elseif(-not $Writable -and (Test-Path -LiteralPath $source -PathType Leaf)){throw 'BF_BLOCKED: read-only source capability probe created a file.'}
    elseif($Writable -and (Test-Path -LiteralPath $source -PathType Leaf)){Remove-Item -LiteralPath $source -Force}
    $capability=[ordered]@{observations=$actual;permissions_sha256=Get-BFHash $Permissions;sandbox_sha256=$profile.sandbox.sha256;provider_sha256=$profile.executable_sha256;network='not_probed';database='not_accessed'}
    Write-BFJson (Join-Path $Directory 'capability.json') $capability
    return $capability
}

function Invoke-BFManagedWorker {
    param($State,[string]$Stage,[string]$Prompt,[string]$Directory,[string]$CodexPath,[scriptblock]$Cancelled,[int]$MaxOutputBytes=16777216,[object]$ProviderContext=$null)
    $profile=Get-BFValue $State.request 'execution_profile'
    if($null -eq $profile){return Invoke-BFCodexWorker $State $Stage $Prompt $Directory $CodexPath $Cancelled}
    # BFI-003/BFI-005 pre-dispatch gates live on the shared path so both
    # providers receive the same runtime and budget contract.
    $runtimeRoot=if($null -ne $ProviderContext){Join-Path (Assert-BFSafePath $ProviderContext.artifact_root) 'runtime'}else{''}
    [void](Test-BFRuntimePreflight $State $Directory $runtimeRoot)
    $requestedModel=if($Stage -in @('code_review','spec_review')){$State.request.models.reviewer}else{$State.request.models.worker}
    $hasBudget=Test-BFCoverageProperty $State.request 'budget'
    if($hasBudget){
        if($null -ne $ProviderContext){[void](Assert-BFProviderBudgetAdmission $State $ProviderContext $Directory);[void](Add-BFProviderBudgetReservation $State $ProviderContext $Directory $profile.provider $Stage $requestedModel)}
        else{[void](Assert-BFBudgetAdmission $State $Directory);[void](Add-BFBudgetReservation $State $Directory $profile.provider $Stage $requestedModel)}
    }
    if($profile.provider -eq 'opencode'){$result=Invoke-BFOpenCodeWorker $State $Stage $Prompt $Directory $CodexPath $Cancelled $MaxOutputBytes}
    else{$result=Invoke-BFProfiledCodexWorker $State $Stage $Prompt $Directory $CodexPath $Cancelled $MaxOutputBytes}
    if($hasBudget){if($null -ne $ProviderContext){[void](Complete-BFProviderBudgetDispatch $State $ProviderContext $Directory $profile.provider $Stage $requestedModel)}else{[void](Complete-BFBudgetDispatch $State $Directory $profile.provider $Stage $requestedModel)}}
    return $result
}

function Invoke-BFExecutionCheck {
    param($State,[string]$Executable,[string[]]$Arguments,[string]$Directory,[string]$CodexPath,[scriptblock]$Cancelled,[object]$ProviderContext=$null)
    [void](Get-BFExecutionDependencies $State)
    if((Assert-BFSafePath $CodexPath) -cne (Assert-BFSafePath $State.request.execution_profile.sandbox.executable)){throw 'BF_BLOCKED: verifier sandbox identity mismatch.'}
    $hostRoot=if($null -ne $ProviderContext){Assert-BFSafePath (Join-Path $ProviderContext.artifact_root ('execution/'+(Get-BFHash $Directory)))}else{Assert-BFSafePath (Join-Path $State.project_path ('.bsl-flow/hosts/'+$State.task_id+'/'+(Get-BFHash $Directory)))}
    if(Test-Path -LiteralPath $hostRoot){throw 'BF_BLOCKED: existing verifier scratch requires attempt reconciliation.'}
    $scratch=Join-Path $hostRoot 'scratch';$config=Join-Path $hostRoot 'config'
    foreach($path in @($scratch,$config)){[void][IO.Directory]::CreateDirectory($path)}
    $canonical=if($null -ne $ProviderContext){[string]$ProviderContext.canonical_store_root}else{''}
    $permissions=(Get-BFExecutionPermissionProfile $State $scratch $config $true $canonical).Replace('network={enabled=true}','network={enabled=false}')
    [void](Test-BFExecutionCapability $State (Join-Path $Directory 'capability') $scratch $config $permissions $true $canonical)
    $args=@('sandbox','-P','bsl_execution','-c',$permissions,'-c','windows.sandbox="elevated"','-C',$State.worker_path,$Executable)+$Arguments
    return Invoke-BFProcess -Executable $CodexPath -Arguments $args -WorkingDirectory $State.worker_path -OutputDirectory $Directory -TimeoutSeconds ([int](Get-BFValue $State.request 'timeout_seconds' 1800)) -Cancelled $Cancelled -CleanEnvironment -Environment @{TEMP=$scratch;TMP=$scratch;GIT_OPTIONAL_LOCKS='0'}
}
