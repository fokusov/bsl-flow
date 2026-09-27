#Requires -Version 7.0
param([string]$ProjectPath, [string]$AdapterDir)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'SilentlyContinue'
. (Join-Path $AdapterDir '..\..\OneCOps.Common.ps1')

$yamlText = Get-OOBslFlowYamlText $ProjectPath
$map = Get-OOYamlFlatMap $yamlText @('onec', 'skillset', 'map')
if ($map.Count -gt 0) { 'true' } else { 'false' }
