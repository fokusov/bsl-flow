#Requires -Version 7.0
[CmdletBinding(DefaultParameterSetName = 'Create')]
param(
    [Parameter(Mandatory)][ValidateSet('unica', 'cc-1c-skills')][string]$ToolsetName,
    [Parameter(Mandatory)][ValidateNotNullOrEmpty()][string]$SourceRoot,
    [Parameter(Mandatory)][ValidateNotNullOrEmpty()][string]$OutputDirectory,
    [Parameter(Mandatory, ParameterSetName = 'Verify')][switch]$Verify
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$packageRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $packageRoot 'global\skills\1c-task\scripts\Task.Storage.ps1')

. (Join-Path (Split-Path $PSScriptRoot -Parent) 'global/skills/1c-task/scripts/Task.Toolsets.ps1')

if ($PSVersionTable.PSVersion -lt [version]'7.0') { throw 'PowerShell 7 or later is required.' }
$source = Get-BFToolsetCanonicalExistingDirectory -Path $SourceRoot -Label 'SourceRoot'
$output = if ($Verify) { Get-BFToolsetCanonicalExistingDirectory -Path $OutputDirectory -Label 'Snapshot OutputDirectory' } else { Get-BFToolsetCanonicalNewDirectory -Path $OutputDirectory }
Assert-BFToolsetNoReparseAncestors -Path $source -Label 'SourceRoot'
Assert-BFToolsetNoReparseAncestors -Path (Split-Path -Parent $output) -Label 'OutputDirectory parent'
if ((Test-BFToolsetIsNestedPath -Candidate $output -Parent $source) -or (Test-BFToolsetIsNestedPath -Candidate $source -Parent $output)) { throw 'SourceRoot and OutputDirectory must not be nested.' }
if ($Verify) { Test-BFToolsetSnapshot -Root $output -ExpectedToolset $ToolsetName; return }

$skills = @(Get-ChildItem -LiteralPath $source -Directory -Force | Where-Object {
    if (($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "SourceRoot contains a reparse-point directory: $($_.FullName)" }
    Test-Path -LiteralPath (Join-Path $_.FullName 'SKILL.md') -PathType Leaf
})
if ($skills.Count -eq 0) { throw 'SourceRoot has no direct skill directories containing SKILL.md.' }
[void](New-Item -ItemType Directory -Path $output -ErrorAction Stop)
$manifestSkills = @()
foreach ($skill in @($skills | Sort-Object Name)) {
    $files = Get-BFToolsetSkillFiles -SkillDirectory $skill.FullName
    $destination = Join-Path $output $skill.Name
    [void](New-Item -ItemType Directory -Path $destination -ErrorAction Stop)
    $entries = @()
    foreach ($file in $files) {
        $relative = ConvertTo-BFToolsetSafeRelativePath -FullName $file.FullName -Root $skill.FullName
        $target = Join-Path $destination ($relative -replace '/', [IO.Path]::DirectorySeparatorChar)
        [void](New-Item -ItemType Directory -Path (Split-Path -Parent $target) -Force)
        [IO.File]::Copy($file.FullName, $target, $false)
        $entries += [ordered]@{ path = $relative; sha256 = (Get-FileHash -LiteralPath $target -Algorithm SHA256).Hash.ToLowerInvariant() }
    }
    $copiedFiles = Get-BFToolsetSkillFiles -SkillDirectory $destination
    $manifestSkills += [ordered]@{ name = $skill.Name; files = @($entries | Sort-Object path); sha256 = (Get-BFToolsetAggregateHash -Skills @([ordered]@{ name = $skill.Name; files = $entries })); mcp_references = [object[]]@(Get-BFToolsetMcpReferences -Files $copiedFiles -SkillDirectory $destination) }
}
$manifest = [ordered]@{ schema_version = 1; toolset_name = $ToolsetName; source = [ordered]@{ identity = 'local-private'; path = $source }; skills = @($manifestSkills | Sort-Object name); aggregate_sha256 = Get-BFToolsetAggregateHash -Skills $manifestSkills }
$json = $manifest | ConvertTo-Json -Depth 16
[IO.File]::WriteAllText((Join-Path $output 'toolset-manifest.json'), $json + "`n", [Text.UTF8Encoding]::new($false))
Test-BFToolsetSnapshot -Root $output -ExpectedToolset $ToolsetName
