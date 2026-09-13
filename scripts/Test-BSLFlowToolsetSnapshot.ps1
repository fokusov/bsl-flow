#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if (-not $PackageRoot) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$tool = Join-Path $PackageRoot 'scripts\New-BSLFlowToolsetSnapshot.ps1'
function Assert-TS([bool]$Condition, [string]$Message) { if (-not $Condition) { throw "ASSERTION FAILED: $Message" } }
function Expect-TS([scriptblock]$Action, [string]$Pattern) { try { & $Action; throw 'Expected failure did not occur.' } catch { Assert-TS ($_.Exception.Message -match $Pattern) "Unexpected failure: $($_.Exception.Message)" } }
$root = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-toolset-snapshot-' + [guid]::NewGuid().ToString('N'))
try {
    $source = Join-Path $root 'source'; $out1 = Join-Path $root 'snapshot-a'; $out2 = Join-Path $root 'snapshot-b'
    [void](New-Item -ItemType Directory -Path (Join-Path $source 'skill-a\scripts') -Force)
    [void](New-Item -ItemType Directory -Path (Join-Path $source 'skill-b') -Force)
    [IO.File]::WriteAllText((Join-Path $source 'skill-a\SKILL.md'), "# A`nUses unica.cf.info and mcp__unica__cf_info.", [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText((Join-Path $source 'skill-a\scripts\run.ps1'), 'Write-Output ok', [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText((Join-Path $source 'skill-b\SKILL.md'), '# B', [Text.UTF8Encoding]::new($false))
    [void](& $tool -ToolsetName unica -SourceRoot $source -OutputDirectory $out1)
    $first = Get-Content (Join-Path $out1 'toolset-manifest.json') -Raw | ConvertFrom-Json
    Assert-TS ($first.skills.Count -eq 2 -and (Test-Path (Join-Path $out1 'skill-a\scripts\run.ps1'))) 'Full skill trees were not copied.'
    Assert-TS ((@($first.skills[0].mcp_references.name) -contains 'mcp__unica__cf_info') -and (@($first.skills[0].mcp_references.name) -contains 'unica.cf.info')) 'MCP text references were not inventoried.'
    [void](& $tool -ToolsetName unica -SourceRoot $source -OutputDirectory $out2)
    $second = Get-Content (Join-Path $out2 'toolset-manifest.json') -Raw | ConvertFrom-Json
    Assert-TS ($first.aggregate_sha256 -eq $second.aggregate_sha256) 'Same source did not produce deterministic aggregate hash.'
    [void](& $tool -ToolsetName unica -SourceRoot $source -OutputDirectory $out1 -Verify)
    [IO.File]::AppendAllText((Join-Path $out1 'skill-a\scripts\run.ps1'), 'tamper')
    Expect-TS { & $tool -ToolsetName unica -SourceRoot $source -OutputDirectory $out1 -Verify } 'hash differs'
    [IO.File]::WriteAllText((Join-Path $out2 'extra.txt'), 'extra')
    Expect-TS { & $tool -ToolsetName unica -SourceRoot $source -OutputDirectory $out2 -Verify } 'tree differs'
    Expect-TS { & $tool -ToolsetName unica -SourceRoot $source -OutputDirectory $out2 } 'must be new'
    $unsafe = Join-Path $root 'unsafe-source'; [void](New-Item -ItemType Directory -Path (Join-Path $unsafe 'skill\nested') -Force); [IO.File]::WriteAllText((Join-Path $unsafe 'skill\SKILL.md'), '# unsafe')
    [void](New-Item -ItemType Junction -Path (Join-Path $unsafe 'skill\linked') -Target (Join-Path $unsafe 'skill\nested'))
    Expect-TS { & $tool -ToolsetName unica -SourceRoot $unsafe -OutputDirectory (Join-Path $root 'unsafe-output') } 'reparse-point'
    $badManifest = Get-Content (Join-Path $out2 'toolset-manifest.json') -Raw | ConvertFrom-Json -AsHashtable; Remove-Item -LiteralPath (Join-Path $out2 'extra.txt'); $badManifest.skills[0].files[0].path = '../escape'; $badManifest | ConvertTo-Json -Depth 16 | Set-Content -LiteralPath (Join-Path $out2 'toolset-manifest.json') -Encoding UTF8
    Expect-TS { & $tool -ToolsetName unica -SourceRoot $source -OutputDirectory $out2 -Verify } 'unsafe path'
    $out3 = Join-Path $root 'snapshot-c'; [void](& $tool -ToolsetName unica -SourceRoot $source -OutputDirectory $out3)
    $hashManifest = Get-Content (Join-Path $out3 'toolset-manifest.json') -Raw | ConvertFrom-Json -AsHashtable; $hashManifest.skills[0].sha256 = '0' * 64; $hashManifest | ConvertTo-Json -Depth 16 | Set-Content -LiteralPath (Join-Path $out3 'toolset-manifest.json') -Encoding UTF8
    Expect-TS { & $tool -ToolsetName unica -SourceRoot $source -OutputDirectory $out3 -Verify } 'skill hash differs'
    $out4 = Join-Path $root 'snapshot-d'; [void](& $tool -ToolsetName unica -SourceRoot $source -OutputDirectory $out4)
    $manifestText = Get-Content (Join-Path $out4 'toolset-manifest.json') -Raw; $manifestText = $manifestText -replace '"schema_version": 1,', '"schema_version": 1,"schema_version": 1,'; Set-Content -LiteralPath (Join-Path $out4 'toolset-manifest.json') -Value $manifestText -Encoding UTF8
    Expect-TS { & $tool -ToolsetName unica -SourceRoot $source -OutputDirectory $out4 -Verify } 'Duplicate JSON object key'
    $out5 = Join-Path $root 'snapshot-e'; [void](& $tool -ToolsetName unica -SourceRoot $source -OutputDirectory $out5)
    $manifestText = Get-Content (Join-Path $out5 'toolset-manifest.json') -Raw; $manifestText = $manifestText -replace '"schema_version": 1', '"schema_version": true'; Set-Content -LiteralPath (Join-Path $out5 'toolset-manifest.json') -Value $manifestText -Encoding UTF8
    Expect-TS { & $tool -ToolsetName unica -SourceRoot $source -OutputDirectory $out5 -Verify } 'invalid identity'
    Write-Host 'Toolset snapshot: 11 checks PASS.'
}
finally {
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}
