#Requires -Version 7.0
param([string]$ProjectPath, [string]$ParamsJson)
if ($env:BF_MOCK_RECORD) { 'skill invoked' | Add-Content -LiteralPath $env:BF_MOCK_RECORD }
$params = $ParamsJson | ConvertFrom-Json
if ($params.mode -eq 'unstructured') { 'finished'; exit 0 }
'{"status":"PASS","evidence":[]}'
if ($params.mode -eq 'failure') { exit 1 }
exit 0
