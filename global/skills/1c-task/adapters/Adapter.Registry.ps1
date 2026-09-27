#Requires -Version 7.0
Set-StrictMode -Version Latest

# Worker adapter registry (worker-adapter/v1, ADR-11 recorded exception).
# Each adapter is declared by a flat `<name>.adapter.json` manifest next to its
# entry script. The manifest is installed package bytes, so it is already bound
# by the controller policy hash; it never comes from the project or the worker.
$script:BFAdapterIsolations=@('os_sandbox','permission_rules','current_agent')
$script:BFAdapterStages=@('inspect','spec','spec_review','spec_reconcile','implement','code_review','code_reconcile','diagnose')

function Get-BFWorkerAdapter {
    param([string]$Name)
    if($Name -cnotmatch '^[a-z][a-z0-9-]{1,31}$'){throw 'BF_INVALID: unsupported managed provider.'}
    $path=Join-Path $PSScriptRoot ($Name+'.adapter.json')
    if(-not(Test-Path -LiteralPath $path -PathType Leaf)){throw 'BF_INVALID: unsupported managed provider.'}
    $adapter=Read-BFJson $path
    Assert-BFFields $adapter @('name','contract','status','isolation','stages','observed_identity','entry','capability_function','worker_function') @('integrity_check','version','profile_required','required_help_flags','tools') 'adapter'
    if($adapter.name -cne $Name -or $adapter.contract -cne 'worker-adapter/v1' -or $adapter.status -cnotin @('supported','experimental')){throw "BF_BLOCKED: invalid worker adapter manifest: $Name"}
    if($adapter.isolation -cnotin $script:BFAdapterIsolations -or $adapter.observed_identity -isnot [bool]){throw "BF_BLOCKED: invalid worker adapter isolation: $Name"}
    if($adapter.stages -isnot [array] -or $adapter.stages.Count -eq 0){throw "BF_BLOCKED: worker adapter declares no stages: $Name"}
    foreach($stage in $adapter.stages){if($stage -cnotin $script:BFAdapterStages){throw "BF_BLOCKED: worker adapter declares an unknown stage: $stage"}}
    if($adapter.entry -cnotmatch '^[A-Za-z][A-Za-z0-9.]*\.ps1$' -or -not(Test-Path -LiteralPath (Join-Path $PSScriptRoot $adapter.entry) -PathType Leaf)){throw "BF_BLOCKED: worker adapter entry is missing: $Name"}
    foreach($field in @('capability_function','worker_function')){$value=$adapter.$field;if($null -ne $value -and $value -cnotmatch '^(Test|Invoke|Submit)-BF[A-Za-z]+$'){throw "BF_BLOCKED: invalid worker adapter $field."}}
    if($adapter.isolation -ceq 'permission_rules' -and (Get-BFValue $adapter 'integrity_check' $false) -isnot [bool]){throw 'BF_BLOCKED: invalid adapter integrity flag.'}
    return $adapter
}

function Assert-BFAdapterStagePolicy {
    # Controller policy: the minimum isolation is bound to the stage, not to
    # the adapter's own claims. current_agent is accepted only through Submit.
    param($Adapter,[string]$Stage,[switch]$Submit)
    if($Stage -cnotin @($Adapter.stages)){throw "BF_BLOCKED: adapter $($Adapter.name) does not support stage $Stage."}
    switch($Adapter.isolation){
        'current_agent' {if(-not $Submit){throw 'BF_BLOCKED: the current-agent adapter is accepted only through Submit.'}}
        'permission_rules' {if($Stage -ceq 'implement' -and (Get-BFValue $Adapter 'integrity_check' $false) -ne $true){throw 'BF_BLOCKED: implement requires os_sandbox or permission_rules with an integrity check.'}}
        'os_sandbox' {}
        default {throw 'BF_BLOCKED: unknown adapter isolation.'}
    }
    if($Submit -and $Adapter.isolation -cne 'current_agent'){throw 'BF_BLOCKED: Submit is reserved for the current-agent adapter.'}
}

function Assert-BFAdapterVersion {
    param($Adapter,[string]$ProviderVersion,[string]$SandboxVersion='')
    $policy=Get-BFValue $Adapter 'version'
    if($null -eq $policy){throw "BF_BLOCKED: adapter $($Adapter.name) declares no version policy."}
    switch($policy.policy){
        'codex_host' {
            [void](Assert-BFCodexHostVersion $ProviderVersion)
            if($ProviderVersion -cne $SandboxVersion){throw 'BF_BLOCKED: Codex provider and sandbox versions differ.'}
        }
        'exact' {
            $text=$ProviderVersion
            $pattern=Get-BFValue $policy 'pattern'
            if($null -ne $pattern){if($ProviderVersion -cnotmatch $pattern){throw 'BF_BLOCKED: unverified provider version.'};$text=$Matches[1]}
            if($text -cnotin @($policy.allowlist)){throw 'BF_BLOCKED: unverified provider version.'}
        }
        default {throw 'BF_BLOCKED: unknown adapter version policy.'}
    }
    return $ProviderVersion
}

function Get-BFTreeDigest {
    # Controller integrity check: content hash of protected trees. Lock and
    # temporary files are excluded; everything else, including hidden files,
    # is bound by relative path and SHA-256. A missing root is bound as absent.
    param([string[]]$Roots,[string[]]$Exclude=@())
    $excluded=@($Exclude|ForEach-Object{(Assert-BFSafePath $_).TrimEnd('\','/')})
    $entries=@()
    foreach($rootPath in $Roots){
        $root=(Assert-BFSafePath $rootPath).TrimEnd('\','/')
        if(-not(Test-Path -LiteralPath $root)){$entries+=,[ordered]@{root=$root;path=$null;sha256=$null};continue}
        if(Test-Path -LiteralPath $root -PathType Leaf){$entries+=,[ordered]@{root=$root;path='';sha256=Get-BFFileHash $root};continue}
        foreach($file in @(Get-ChildItem -LiteralPath $root -File -Recurse -Force|Sort-Object FullName)){
            $full=$file.FullName
            if($file.Name -ceq '.writer.lock' -or $file.Name -like '*.tmp'){continue}
            $skip=$false;foreach($item in $excluded){if($full -eq $item -or $full.StartsWith($item+'\',[StringComparison]::OrdinalIgnoreCase)){$skip=$true;break}}
            if($skip){continue}
            $entries+=,[ordered]@{root=$root;path=$full.Substring($root.Length).TrimStart('\','/').Replace('\','/');sha256=Get-BFFileHash $full}
        }
    }
    return Get-BFHash $entries
}

function Assert-BFWorkerResult {
    # Closed worker-result.schema.json shape plus a materializable payload.
    param($Result,[string]$Name='worker_result')
    Assert-BFFields $Result @('schema_version','status','summary','payload_json') @() $Name
    if(($Result.schema_version -isnot [int] -and $Result.schema_version -isnot [long]) -or $Result.schema_version -ne 1 -or $Result.status -cnotin @('completed','needs_input','blocked','failed')){throw "BF_INVALID: $Name does not match the worker result schema."}
    Assert-BFText $Result.summary "$Name.summary"
    $kind=$null;if($Result.payload_json -is [string]){try{$kind=Test-BFJsonSyntax $Result.payload_json}catch{$kind=$null}}
    if($kind -notin @('object','array')){throw "BF_INVALID: $Name.payload_json must be a JSON string."}
    try{$null=ConvertFrom-Json -InputObject $Result.payload_json -Depth 100 -ErrorAction Stop}catch{throw "BF_INVALID: $Name.payload_json cannot be materialized."}
}

function ConvertFrom-BFWorkerResultText {
    # Final assistant text: exactly one JSON object, raw or in one fenced block.
    param([string]$Text,[string]$Name='worker_result')
    $candidate=$Text.Trim()
    $fences=[regex]::Matches($candidate,'(?s)```(?:json)?[ \t]*\r?\n(.*?)\r?\n[ \t]*```')
    if($fences.Count -gt 1){throw "BF_BLOCKED: $Name contains several fenced results."}
    if($fences.Count -eq 1){$candidate=$fences[0].Groups[1].Value.Trim()}
    $kind=$null;if(-not [string]::IsNullOrWhiteSpace($candidate)){try{$kind=Test-BFJsonSyntax $candidate}catch{$kind=$null}}
    if($kind -ne 'object'){throw "BF_BLOCKED: $Name has no JSON result object."}
    try{$result=ConvertFrom-Json -InputObject $candidate -Depth 100 -ErrorAction Stop}catch{throw "BF_BLOCKED: $Name cannot be materialized."}
    Assert-BFWorkerResult $result $Name
    return $result
}
