#Requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$EvidencePath,
    [string]$OutputPath,
    [switch]$NoThrow
)

Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
function Add-Issue([Collections.Generic.List[string]]$Issues,[string]$Text){if(-not $Issues.Contains($Text)){$Issues.Add($Text)}}
function Has-Property($Object,[string]$Name){$null-ne $Object -and $null-ne $Object.PSObject.Properties[$Name]}
function Resolve-EvidenceReference([string]$Value) {
    if ([string]::IsNullOrWhiteSpace($Value)) { return $null }
    if ([IO.Path]::IsPathRooted($Value)) { return [IO.Path]::GetFullPath($Value) }
    return [IO.Path]::GetFullPath((Join-Path $script:evidenceRoot $Value))
}
function Test-Gate($Gate,[string]$Name,[Collections.Generic.List[string]]$Issues,[bool]$Required=$true){
    if(-not $Required){return}
    if($null-eq $Gate){Add-Issue $Issues "missing_gate:$Name";return}
    if([string]$Gate.status -ne 'PASS'){Add-Issue $Issues "gate_not_passed:$Name"}
    if(-not (Has-Property $Gate 'evidence_path') -or [string]::IsNullOrWhiteSpace([string]$Gate.evidence_path)){Add-Issue $Issues "missing_evidence_path:$Name"}
    else {$resolved=Resolve-EvidenceReference ([string]$Gate.evidence_path);if(-not(Test-Path -LiteralPath $resolved -PathType Leaf)){Add-Issue $Issues "evidence_file_missing:$Name"}}
}
if(-not(Test-Path -LiteralPath $EvidencePath -PathType Leaf)){throw "Evidence file does not exist: $EvidencePath"}
$script:evidenceRoot=Split-Path -Parent ([IO.Path]::GetFullPath($EvidencePath))
try{$evidence=Get-Content -LiteralPath $EvidencePath -Raw -Encoding UTF8|ConvertFrom-Json}catch{throw "Evidence JSON is invalid: $($_.Exception.Message)"}
$issues=New-Object Collections.Generic.List[string]
if($evidence.schema_version-ne 1){Add-Issue $issues 'unsupported_schema_version'}
if(-not(Has-Property $evidence 'artifact')){Add-Issue $issues 'missing_artifact'}else{
    $type=([string]$evidence.artifact.type).ToLowerInvariant()
    if($type-notin @('epf','erf')){Add-Issue $issues 'artifact_type_must_be_epf_or_erf'}
    $path=[string]$evidence.artifact.path
    if (-not [string]::IsNullOrWhiteSpace($path) -and -not [IO.Path]::IsPathRooted($path)) {$path=[IO.Path]::GetFullPath((Join-Path $script:evidenceRoot $path))}
    if([string]::IsNullOrWhiteSpace($path)-or-not(Test-Path -LiteralPath $path -PathType Leaf)){Add-Issue $issues 'artifact_file_missing'}else{
        $item=Get-Item -LiteralPath $path
        $hash=(Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
        if(([string]$evidence.artifact.sha256)-notmatch'^[0-9a-fA-F]{64}$'){Add-Issue $issues 'artifact_sha256_invalid'}
        elseif($hash-ne([string]$evidence.artifact.sha256).ToLowerInvariant()){Add-Issue $issues 'artifact_sha256_mismatch'}
        if([int64]$evidence.artifact.bytes-ne$item.Length){Add-Issue $issues 'artifact_size_mismatch'}
        if([IO.Path]::GetExtension($path).TrimStart('.').ToLowerInvariant()-ne$type){Add-Issue $issues 'artifact_extension_mismatch'}
    }
}
if(-not(Has-Property $evidence 'source')-or([string]$evidence.source.manifest_sha256)-notmatch'^[0-9a-fA-F]{64}$'){Add-Issue $issues 'missing_or_invalid_source_manifest_sha256'}
$gates=if(Has-Property $evidence 'gates'){$evidence.gates}else{$null}
if($null-eq$gates){Add-Issue $issues 'missing_gates'}else{
    if(Has-Property $gates 'static'){
        $staticStatus=[string]$gates.static.status
        if($staticStatus-notin @('PASS','not_supported')){Add-Issue $issues 'static_gate_invalid'}
        if($staticStatus-eq'PASS'){Test-Gate $gates.static 'static' $issues}
        if($staticStatus-eq'not_supported'-and[string]::IsNullOrWhiteSpace([string]$gates.static.reason)){Add-Issue $issues 'static_not_supported_without_reason'}
    }else{Add-Issue $issues 'missing_gate:static'}
    Test-Gate $gates.native_build 'native_build' $issues
    Test-Gate $gates.roundtrip 'roundtrip' $issues
    Test-Gate $gates.native_load 'native_load' $issues
    foreach($name in @('native_build','roundtrip')){if(($null -ne $gates.$name) -and (Has-Property $evidence 'artifact')){if(([string]$gates.$name.artifact_sha256).ToLowerInvariant()-ne([string]$evidence.artifact.sha256).ToLowerInvariant()){Add-Issue $issues "$name`_artifact_hash_mismatch"}}}
    if(($null -ne $gates.roundtrip) -and (Has-Property $evidence 'source')){if(([string]$gates.roundtrip.source_manifest_sha256).ToLowerInvariant()-ne([string]$evidence.source.manifest_sha256).ToLowerInvariant()){Add-Issue $issues 'roundtrip_source_manifest_hash_mismatch'}}
    if (($null -ne $gates.native_load) -and (Has-Property $evidence 'artifact')) {
        if(([string]$gates.native_load.artifact_sha256).ToLowerInvariant()-ne([string]$evidence.artifact.sha256).ToLowerInvariant()){Add-Issue $issues 'native_load_artifact_hash_mismatch'}
        if([string]::IsNullOrWhiteSpace([string]$gates.native_load.platform_version)){Add-Issue $issues 'native_load_platform_missing'}
        if([string]::IsNullOrWhiteSpace([string]$gates.native_load.target)){Add-Issue $issues 'native_load_target_missing'}
    }
    $behaviorRequired=$false
    if ((Has-Property $gates 'behavior') -and (Has-Property $gates.behavior 'required')) {$behaviorRequired=[bool]$gates.behavior.required}
    Test-Gate $gates.behavior 'behavior' $issues $behaviorRequired
    if($behaviorRequired -and ($null -ne $gates.behavior) -and (Has-Property $evidence 'artifact')){if(([string]$gates.behavior.artifact_sha256).ToLowerInvariant()-ne([string]$evidence.artifact.sha256).ToLowerInvariant()){Add-Issue $issues 'behavior_artifact_hash_mismatch'}}
}
$result=[ordered]@{schema_version=1;checked_at_utc=[DateTime]::UtcNow.ToString('o');status=if($issues.Count){'BLOCKED'}else{'PASS'};evidence_path=[IO.Path]::GetFullPath($EvidencePath);issues=@($issues);runtime_action_performed=$false}
if($OutputPath){$parent=Split-Path -Parent $OutputPath;if($parent){[IO.Directory]::CreateDirectory($parent)|Out-Null};[IO.File]::WriteAllText([IO.Path]::GetFullPath($OutputPath),($result|ConvertTo-Json -Depth 8)+[Environment]::NewLine,[Text.UTF8Encoding]::new($false))}
if (($issues.Count -gt 0) -and (-not $NoThrow)) {throw "External artifact evidence is BLOCKED: $($issues -join ', ')"}
[pscustomobject]$result
