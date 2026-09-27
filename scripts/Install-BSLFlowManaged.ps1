#Requires -Version 7.0
[CmdletBinding(SupportsShouldProcess)]
param([string]$MarkerPath,[switch]$SimulatePostApplyFailure)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if (-not $IsWindows) { throw 'BSL Flow Managed supports Windows only.' }
. (Join-Path $PSScriptRoot 'Install.Package.ps1')
$root = Split-Path $PSScriptRoot -Parent
$version = (Get-Content -Raw (Join-Path $root 'VERSION')).Trim()
$isolated = [bool]$MarkerPath
if (-not $MarkerPath) { $MarkerPath = Join-Path ([Environment]::GetFolderPath('UserProfile')) '.bsl-flow/installed-core.json' }
Assert-BFInstallTarget $MarkerPath -Isolated:$isolated
if (-not (Test-Path -LiteralPath $MarkerPath -PathType Leaf)) { throw "BF_BLOCKED: requires_core $version; install Core first." }
$core = Get-Content -Raw -LiteralPath $MarkerPath | ConvertFrom-Json
$requires = $version
$manifestPath = Join-Path $root 'package-manifest.json'
if (Test-Path $manifestPath) {
    $manifest = Get-Content -Raw $manifestPath | ConvertFrom-Json
    if ($manifest.package -eq 'managed') { $requires = [string]$manifest.requires_core }
}
if ($core.package -ne 'core' -or $core.version -cne $requires -or $requires -cne $version) { throw "BF_BLOCKED: requires_core $version; installed Core version is $($core.version)." }
$skills = [string]$core.skills_root
Assert-BFInstallTarget $skills -Isolated:$isolated
foreach ($name in @('1c-init-project','1c-spec','1c-spec-review','1c-implement','1c-verify','1c-debug','1c-estimate')) {
    if (-not (Test-Path (Join-Path $skills "$name/SKILL.md") -PathType Leaf)) { throw "BF_BLOCKED: Core receipt points to incomplete installation ($name)." }
}
$definition = Get-Content -Raw (Join-Path $root 'packaging/managed.json') | ConvertFrom-Json
$copies = @(foreach ($file in Get-ChildItem (Join-Path $root 'global/skills') -Recurse -File) {
    $relative = [IO.Path]::GetRelativePath($root,$file.FullName).Replace('\','/')
    if (Test-BFPackageMember $relative $definition) { @{source=$file.FullName;target=Join-Path $skills $relative.Substring('global/skills/'.Length)} }
})
if (-not ($copies.target -contains (Join-Path $skills '1c-task/SKILL.md'))) { throw 'Managed package lacks 1c-task.' }
$managedMarker = Join-Path (Split-Path $MarkerPath -Parent) 'installed-managed.json'
$writes = @{}
$writes[$managedMarker] = (@{package='managed';version=$version;requires_core=$requires;skills_root=$skills;installed_at_utc=[DateTime]::UtcNow.ToString('o')} | ConvertTo-Json)
if (-not $PSCmdlet.ShouldProcess($skills,"Install Managed $version")) { return }
$backup = Invoke-BFInstallTransaction -Copies $copies -TextWrites $writes -MarkerPath $managedMarker -Isolated:$isolated -SimulatePostApplyFailure:$SimulatePostApplyFailure
[pscustomobject]@{Package='managed';Version=$version;Backup=$backup;MarkerPath=$managedMarker}
