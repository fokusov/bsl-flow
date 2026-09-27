#Requires -Version 7.0
param([string]$ProjectPath, [string]$AdapterDir)
$indexScript = Join-Path $AdapterDir '../../../../1c-spec-review/scripts/Get-1CMetadataIndex.ps1'
if (Test-Path -LiteralPath $indexScript -PathType Leaf) { 'true' } else { 'false' }
