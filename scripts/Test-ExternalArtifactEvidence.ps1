[CmdletBinding()]param([string]$PackageRoot)
Set-StrictMode -Version Latest;$ErrorActionPreference='Stop'
function Assert-E([bool]$Condition,[string]$Message){if(-not$Condition){throw $Message}}
if(-not$PackageRoot){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$check=Join-Path $PackageRoot 'global\skills\1c-verify\scripts\Test-ExternalArtifactEvidence.ps1';$root=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-external-'+[guid]::NewGuid().ToString('N'))
try{
 New-Item -ItemType Directory -Path $root|Out-Null;$artifact=Join-Path $root 'sample.epf';Set-Content -LiteralPath $artifact -Value 'fixture' -Encoding UTF8;$item=Get-Item $artifact;$hash=(Get-FileHash $artifact -Algorithm SHA256).Hash.ToLowerInvariant()
 foreach($name in @('static','build','roundtrip','load','behavior')){Set-Content -LiteralPath (Join-Path $root ($name+'.json')) -Value '{}' -Encoding UTF8}
 $gate={param($name)[ordered]@{status='PASS';evidence_path=(Join-Path $root ($name+'.json'))}}
 $e=[ordered]@{schema_version=1;artifact=[ordered]@{type='epf';path=$artifact;sha256=$hash;bytes=$item.Length};source=[ordered]@{manifest_sha256=('a'*64)};gates=[ordered]@{static=(& $gate 'static');native_build=[ordered]@{status='PASS';evidence_path=(Join-Path $root 'build.json');artifact_sha256=$hash};roundtrip=[ordered]@{status='PASS';evidence_path=(Join-Path $root 'roundtrip.json');artifact_sha256=$hash;source_manifest_sha256=('a'*64)};native_load=[ordered]@{status='PASS';evidence_path=(Join-Path $root 'load.json');artifact_sha256=$hash;platform_version='8.3.fixture';target='authorized FILE fixture'};behavior=[ordered]@{required=$true;status='PASS';evidence_path=(Join-Path $root 'behavior.json');artifact_sha256=$hash}}}
 $path=Join-Path $root 'evidence.json';$e|ConvertTo-Json -Depth 12|Set-Content -LiteralPath $path -Encoding UTF8;$pass=&$check -EvidencePath $path;Assert-E ($pass.status-eq'PASS') 'Complete external artifact evidence did not pass.'
 Remove-Item -LiteralPath (Join-Path $root 'build.json');$missingFile=&$check -EvidencePath $path -NoThrow;Assert-E (($missingFile.status -eq 'BLOCKED') -and ($missingFile.issues -contains 'evidence_file_missing:native_build')) 'Missing evidence file did not block.';Set-Content -LiteralPath (Join-Path $root 'build.json') -Value '{}' -Encoding UTF8
 $e.gates.native_load.status='BLOCKED';$e|ConvertTo-Json -Depth 12|Set-Content -LiteralPath $path -Encoding UTF8;$blocked=&$check -EvidencePath $path -NoThrow;Assert-E ($blocked.status-eq'BLOCKED'-and$blocked.issues-contains'gate_not_passed:native_load') 'Missing native load did not block.'
 $e.gates.native_load.status='PASS';$e.gates.behavior.status='BLOCKED';$e|ConvertTo-Json -Depth 12|Set-Content -LiteralPath $path -Encoding UTF8;$blocked=&$check -EvidencePath $path -NoThrow;Assert-E ($blocked.status-eq'BLOCKED'-and$blocked.issues-contains'gate_not_passed:behavior') 'Required behavior did not block.'
 Write-Host 'External artifact evidence contracts passed; no 1C process was started.'
}finally{if(Test-Path $root){Remove-Item -LiteralPath $root -Recurse -Force}}
