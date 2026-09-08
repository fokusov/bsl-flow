[CmdletBinding()]
param([string]$PackageRoot)
$ErrorActionPreference = 'Stop'
if (-not $PackageRoot) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$helper = Join-Path $PackageRoot 'global/skills/1c-verify/scripts/Test-ExtensionIdentities.ps1'
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-identity-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path (Join-Path $testRoot 'valid'), (Join-Path $testRoot 'empty-ids') -Force | Out-Null
try {
    '<MetaDataObject><CommonModule uuid="11111111-1111-1111-1111-111111111111"/></MetaDataObject>' | Set-Content -LiteralPath (Join-Path $testRoot 'valid/a.xml') -Encoding UTF8
    '<MetaDataObject><CommonModule/></MetaDataObject>' | Set-Content -LiteralPath (Join-Path $testRoot 'empty-ids/a.xml') -Encoding UTF8
    foreach ($roots in @(@('valid','missing'), @('valid','empty-ids'), @('empty-ids'))) {
        $result = & $helper -ProjectRoot $testRoot -SourceRoots $roots -NoThrow
        if ($result.status -eq 'PASS') { throw "Incomplete selected roots passed: $roots" }
    }
    '<MetaDataObject><CommonModule uuid="bad"/></MetaDataObject>' | Set-Content -LiteralPath (Join-Path $testRoot 'empty-ids/a.xml') -Encoding UTF8
    if ((& $helper -ProjectRoot $testRoot -SourceRoots 'empty-ids' -NoThrow).status -eq 'PASS') { throw 'Invalid UUID passed.' }
    '<OtherFormat uuid="11111111-1111-1111-1111-111111111111"/>' | Set-Content -LiteralPath (Join-Path $testRoot 'valid/reference.xml') -Encoding UTF8
    $pass = & $helper -ProjectRoot $testRoot -SourceRoots 'valid'
    if ($pass.status -ne 'PASS' -or $pass.owned_uuid_count -ne 1) { throw 'Non-metadata reference affected owned identity check.' }
    'Extension identity safety checks passed.'
} finally {
    $resolved = [IO.Path]::GetFullPath($testRoot)
    if ((Split-Path -Leaf $resolved) -like 'bsl-flow-identity-*' -and (Split-Path -Parent $resolved).TrimEnd('\') -eq ([IO.Path]::GetTempPath()).TrimEnd('\')) {
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
