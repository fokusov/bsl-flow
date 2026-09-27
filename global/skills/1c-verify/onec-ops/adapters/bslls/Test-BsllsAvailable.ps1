#Requires -Version 7.0
# The wrapper is available even without a binary; the underlying gate reports
# availability and accepts offline baseline/current reports.
param([string]$ProjectPath, [string]$AdapterDir)
if (Test-Path -LiteralPath (Join-Path $AdapterDir '../../../scripts/Invoke-1CStaticDiff.ps1') -PathType Leaf) { 'true' } else { 'false' }
