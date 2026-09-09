[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [string]$ProjectPath = (Get-Location).Path,

    [switch]$Explicit1CProject,

    [switch]$SkipProjectUpgrade,

    [string]$DevelopmentDatabasePath,

    [switch]$ConfigureTests,

    [switch]$ConfigureUi,

    [string]$WorkstationProfilePath,

    [string[]]$SourcePath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$created = [System.Collections.Generic.List[string]]::new()
$preserved = [System.Collections.Generic.List[string]]::new()

function Invoke-NativeCommand {
    param(
        [Parameter(Mandatory)]
        [string]$Command,

        [Parameter(Mandatory)]
        [string[]]$Arguments
    )

    $previousErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $output = & $Command @Arguments 2>&1 | ForEach-Object { $_.ToString() } | Out-String
        $exitCode = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
    [pscustomobject]@{
        ExitCode = $exitCode
        Output = $output.Trim()
    }
}

function Get-ConfiguredSchemas {
    param([Parameter(Mandatory)][string]$ConfigText)

    return @([regex]::Matches($ConfigText, '(?m)^\s*schema:\s*([^\s#]+)') | ForEach-Object {
        $_.Groups[1].Value.Trim('"', "'")
    })
}

function Get-NormalizedPath {
    param([Parameter(Mandatory)][string]$Path)
    return [System.IO.Path]::GetFullPath($Path).TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
}

function Test-Confirmed1CProject {
    param(
        [Parameter(Mandatory)][string]$Root,
        [switch]$Explicit
    )

    if ($Explicit) {
        return $true
    }

    if (Test-Path -LiteralPath (Join-Path $Root '.bsl-flow\project.yaml') -PathType Leaf) {
        return $true
    }

    $strongFileNames = @('Configuration.xml', 'ConfigDumpInfo.xml')
    $strongExtensions = @('.bsl', '.cf', '.cfe', '.epf', '.erf')
    $rootFiles = @(Get-ChildItem -LiteralPath $Root -File -Force -ErrorAction SilentlyContinue)
    if ($rootFiles | Where-Object { $_.Name -in $strongFileNames -or $_.Extension.ToLowerInvariant() -in $strongExtensions } | Select-Object -First 1) {
        return $true
    }

    $strongDirectoryNames = @('Catalogs', 'Documents', 'CommonModules', 'InformationRegisters', 'AccumulationRegisters')
    $rootDirectories = @(Get-ChildItem -LiteralPath $Root -Directory -Force -ErrorAction SilentlyContinue)
    if ($rootDirectories | Where-Object { $_.Name -in $strongDirectoryNames } | Select-Object -First 1) {
        return $true
    }

    $sourceDirectoryNames = @('src', 'cf', 'cfe', 'edt')
    $sourceDirectories = @($rootDirectories | Where-Object { $_.Name.ToLowerInvariant() -in $sourceDirectoryNames })
    if ($sourceDirectories.Count -ge 2) {
        return $true
    }

    foreach ($sourceDirectory in $sourceDirectories) {
        $indicator = Get-ChildItem -LiteralPath $sourceDirectory.FullName -File -Recurse -Depth 4 -Force -ErrorAction SilentlyContinue |
            Where-Object { $_.Name -in $strongFileNames -or $_.Extension.ToLowerInvariant() -in $strongExtensions } |
            Select-Object -First 1
        if ($indicator) {
            return $true
        }
    }

    return $false
}

function Copy-TemplateIfMissing {
    param(
        [Parameter(Mandatory)][string]$Source,
        [Parameter(Mandatory)][string]$Destination,
        [Parameter(Mandatory)][string]$Label
    )

    if (Test-Path -LiteralPath $Destination -PathType Leaf) {
        $script:preserved.Add($Label)
        return
    }
    if (Test-Path -LiteralPath $Destination -PathType Container) {
        throw "A directory exists where a managed file is required: $Destination"
    }

    $destinationDirectory = Split-Path -Parent $Destination
    if ($destinationDirectory) {
        New-Item -ItemType Directory -Path $destinationDirectory -Force | Out-Null
    }
    Copy-Item -LiteralPath $Source -Destination $Destination
    $script:created.Add($Label)
}

if (-not (Test-Path -LiteralPath $ProjectPath -PathType Container)) {
    throw "Project directory does not exist: $ProjectPath"
}

$projectRoot = Get-NormalizedPath (Resolve-Path -LiteralPath $ProjectPath).Path
$pathRoot = [System.IO.Path]::GetPathRoot($projectRoot).TrimEnd('\', '/')
$userProfile = Get-NormalizedPath ([Environment]::GetFolderPath('UserProfile'))

if ($projectRoot -eq $pathRoot -or $projectRoot -eq $userProfile) {
    throw "Refusing to bootstrap a drive root or user profile: $projectRoot"
}

if (-not (Test-Confirmed1CProject -Root $projectRoot -Explicit:$Explicit1CProject)) {
    throw "The directory is not confirmed as a 1C project root. Add -Explicit1CProject only after the user explicitly identifies this exact directory as the intended 1C project."
}

$gitCommand = Get-Command git -ErrorAction SilentlyContinue
if (-not $gitCommand) {
    throw 'Git is required but was not found in PATH.'
}

$openSpecCommand = Get-Command openspec -ErrorAction SilentlyContinue
if (-not $openSpecCommand) {
    throw 'OpenSpec CLI is required but was not found in PATH.'
}

$schemaWhich = Invoke-NativeCommand -Command $openSpecCommand.Source -Arguments @('schema', 'which', 'bsl-flow')
if ($schemaWhich.ExitCode -ne 0) {
    throw "Global OpenSpec schema 'bsl-flow' was not found.`n$($schemaWhich.Output)"
}

$schemaValidation = Invoke-NativeCommand -Command $openSpecCommand.Source -Arguments @('schema', 'validate', 'bsl-flow', '--json')
if ($schemaValidation.ExitCode -ne 0) {
    throw "Global OpenSpec schema 'bsl-flow' is invalid.`n$($schemaValidation.Output)"
}

$gitProbe = Invoke-NativeCommand -Command $gitCommand.Source -Arguments @('-C', $projectRoot, 'rev-parse', '--show-toplevel')
$hasProjectGit = $false
if ($gitProbe.ExitCode -eq 0) {
    $repoRootText = ($gitProbe.Output -split "`r?`n" | Select-Object -Last 1).Trim()
    $repoRoot = Get-NormalizedPath $repoRootText
    if ($repoRoot -ne $projectRoot) {
        throw "The selected directory is inside another Git repository: $repoRoot. Confirm the real project root before bootstrap."
    }
    $hasProjectGit = $true
}

$openSpecConfigPath = Join-Path $projectRoot 'openspec\config.yaml'
if (Test-Path -LiteralPath $openSpecConfigPath -PathType Leaf) {
    $existingConfig = Get-Content -Raw -LiteralPath $openSpecConfigPath
    $configuredSchemas = @(Get-ConfiguredSchemas -ConfigText $existingConfig)
    if ($configuredSchemas.Count -gt 1) {
        throw "OpenSpec config contains duplicate schema keys. Resolve the ambiguous YAML before bootstrap: $openSpecConfigPath"
    }
    if ($configuredSchemas.Count -eq 1) {
        $existingSchema = $configuredSchemas[0]
        if ($existingSchema -ne 'bsl-flow') {
            throw "OpenSpec is already configured with schema '$existingSchema'. Resolve this workflow conflict before bootstrap."
        }
    }
}

$sentinelPath = Join-Path $projectRoot '.bsl-flow\project.yaml'
if (Test-Path -LiteralPath $sentinelPath -PathType Leaf) {
    $existingSentinel = Get-Content -Raw -LiteralPath $sentinelPath
    $frameworkMatch = [regex]::Match($existingSentinel, '(?m)^\s*framework:\s*([^\s#]+)')
    if ($frameworkMatch.Success -and $frameworkMatch.Groups[1].Value.Trim('"', "'") -ne 'bsl-flow') {
        throw "The existing project sentinel belongs to another framework: $sentinelPath"
    }
}

$skillRoot = Split-Path -Parent $PSScriptRoot
$assetRoot = Join-Path $skillRoot 'assets\project'
foreach ($requiredTemplate in @('AGENTS.md', '.gitignore', 'bsl-flow.yaml', '.bsl-flow\project.yaml')) {
    if (-not (Test-Path -LiteralPath (Join-Path $assetRoot $requiredTemplate) -PathType Leaf)) {
        throw "Bootstrap template is missing from the installed skill: $requiredTemplate"
    }
}

foreach ($managedFile in @('AGENTS.md', 'bsl-flow.yaml', '.gitignore', '.bsl-flow\project.yaml', 'openspec\config.yaml')) {
    $managedPath = Join-Path $projectRoot $managedFile
    if (Test-Path -LiteralPath $managedPath -PathType Container) {
        throw "A directory exists where a managed file is required: $managedPath"
    }
}

if (-not $SkipProjectUpgrade -and (Test-Path -LiteralPath (Join-Path $projectRoot 'bsl-flow.yaml') -PathType Leaf) -and (Test-Path -LiteralPath $sentinelPath -PathType Leaf)) {
    $upgradePreflight = Join-Path $PSScriptRoot 'Update-BSLFlowProject.ps1'
    [void](& $upgradePreflight -ProjectPath $projectRoot)
}

if (-not $hasProjectGit) {
    $gitInit = Invoke-NativeCommand -Command $gitCommand.Source -Arguments @('-C', $projectRoot, 'init')
    if ($gitInit.ExitCode -ne 0) {
        throw "git init failed.`n$($gitInit.Output)"
    }
    $created.Add('.git')
}
else {
    $preserved.Add('.git')
}

$openSpecDirectory = Join-Path $projectRoot 'openspec'
$createdOpenSpec = $false
if (-not (Test-Path -LiteralPath $openSpecDirectory -PathType Container)) {
    $openSpecInit = Invoke-NativeCommand -Command $openSpecCommand.Source -Arguments @('init', $projectRoot, '--tools', 'none', '--no-animation')
    if ($openSpecInit.ExitCode -ne 0) {
        throw "openspec init --tools none failed.`n$($openSpecInit.Output)"
    }
    $createdOpenSpec = $true
    $created.Add('openspec')
}
else {
    $preserved.Add('openspec')
}

New-Item -ItemType Directory -Path (Join-Path $openSpecDirectory 'changes\archive') -Force | Out-Null
New-Item -ItemType Directory -Path (Join-Path $openSpecDirectory 'specs') -Force | Out-Null

if (Test-Path -LiteralPath $openSpecConfigPath -PathType Leaf) {
    $configText = Get-Content -Raw -LiteralPath $openSpecConfigPath
    $configuredSchemas = @(Get-ConfiguredSchemas -ConfigText $configText)
    if ($configuredSchemas.Count -gt 1) {
        throw "OpenSpec config contains duplicate schema keys after initialization: $openSpecConfigPath"
    }
    if ($configuredSchemas.Count -eq 1) {
        $currentSchema = $configuredSchemas[0]
        if ($currentSchema -ne 'bsl-flow') {
            if (-not $createdOpenSpec) {
                throw "Refusing to replace existing OpenSpec schema '$currentSchema'."
            }
            $configText = [regex]::Replace($configText, '(?m)^\s*schema:\s*[^\r\n]+', 'schema: bsl-flow', 1)
            Set-Content -LiteralPath $openSpecConfigPath -Value $configText.TrimEnd() -Encoding utf8
            $created.Add('openspec/config.yaml schema selection')
        }
        else {
            $preserved.Add('openspec/config.yaml')
        }
    }
    else {
        Set-Content -LiteralPath $openSpecConfigPath -Value ("schema: bsl-flow`n`n" + $configText.TrimStart()) -Encoding utf8
        $created.Add('openspec/config.yaml schema selection')
    }
}
else {
    Set-Content -LiteralPath $openSpecConfigPath -Value 'schema: bsl-flow' -Encoding utf8
    $created.Add('openspec/config.yaml')
}

Copy-TemplateIfMissing -Source (Join-Path $assetRoot 'AGENTS.md') -Destination (Join-Path $projectRoot 'AGENTS.md') -Label 'AGENTS.md'
Copy-TemplateIfMissing -Source (Join-Path $assetRoot 'bsl-flow.yaml') -Destination (Join-Path $projectRoot 'bsl-flow.yaml') -Label 'bsl-flow.yaml'

$gitIgnorePath = Join-Path $projectRoot '.gitignore'
$gitIgnoreTemplate = Get-Content -Raw -LiteralPath (Join-Path $assetRoot '.gitignore')
if (Test-Path -LiteralPath $gitIgnorePath -PathType Container) {
    throw "A directory exists where .gitignore is required: $gitIgnorePath"
}
if (-not (Test-Path -LiteralPath $gitIgnorePath -PathType Leaf)) {
    Set-Content -LiteralPath $gitIgnorePath -Value $gitIgnoreTemplate.TrimEnd() -Encoding utf8
    $created.Add('.gitignore')
}
else {
    $existingGitIgnore = Get-Content -Raw -LiteralPath $gitIgnorePath
    $managedIgnorePattern = '(?ms)^# bsl-flow managed:start[^\r\n]*(?:\r?\n).*?^# bsl-flow managed:end[^\r\n]*'
    if ($existingGitIgnore -notmatch '(?m)^# bsl-flow managed:start\s*$') {
        Add-Content -LiteralPath $gitIgnorePath -Value ("`n" + $gitIgnoreTemplate.TrimEnd()) -Encoding utf8
        $created.Add('.gitignore managed block')
    }
    else {
        $updatedGitIgnore = [regex]::Replace($existingGitIgnore, $managedIgnorePattern, $gitIgnoreTemplate.TrimEnd(), 1)
        if ($updatedGitIgnore -ne $existingGitIgnore) {
            Set-Content -LiteralPath $gitIgnorePath -Value $updatedGitIgnore.TrimEnd() -Encoding utf8
            $created.Add('.gitignore managed block updated')
        }
        else { $preserved.Add('.gitignore') }
    }
}

if (-not (Test-Path -LiteralPath $sentinelPath -PathType Leaf)) {
    if (Test-Path -LiteralPath $sentinelPath -PathType Container) {
        throw "A directory exists where the project sentinel is required: $sentinelPath"
    }
    New-Item -ItemType Directory -Path (Split-Path -Parent $sentinelPath) -Force | Out-Null
    $sentinelTemplate = Get-Content -Raw -LiteralPath (Join-Path $assetRoot '.bsl-flow\project.yaml')
    $sentinelText = $sentinelTemplate.Replace('__INITIALIZED_AT__', [DateTime]::UtcNow.ToString('o'))
    Set-Content -LiteralPath $sentinelPath -Value $sentinelText.TrimEnd() -Encoding utf8
    $created.Add('.bsl-flow/project.yaml')
}
else {
    $preserved.Add('.bsl-flow/project.yaml')
}

if (-not $SkipProjectUpgrade) {
    $upgradeScript = Join-Path $PSScriptRoot 'Update-BSLFlowProject.ps1'
    [void](& $upgradeScript -ProjectPath $projectRoot -Apply)
    $preserved.Add('bsl-flow.yaml user values/comments; missing framework keys merged')
    $preserved.Add('AGENTS.md user instructions; BSL Flow managed block merged')
}

foreach ($evidenceDirectoryName in @('reports', 'evidence')) {
    $evidenceDirectory = Join-Path $projectRoot ".bsl-flow\$evidenceDirectoryName"
    New-Item -ItemType Directory -Path $evidenceDirectory -Force | Out-Null
    $gitKeep = Join-Path $evidenceDirectory '.gitkeep'
    if (-not (Test-Path -LiteralPath $gitKeep -PathType Leaf)) {
        New-Item -ItemType File -Path $gitKeep | Out-Null
        $created.Add(".bsl-flow/$evidenceDirectoryName/.gitkeep")
    }
    else {
        $preserved.Add(".bsl-flow/$evidenceDirectoryName/.gitkeep")
    }
}

$finalConfig = Get-Content -Raw -LiteralPath $openSpecConfigPath
$finalSchemas = @(Get-ConfiguredSchemas -ConfigText $finalConfig)
if ($finalSchemas.Count -ne 1 -or $finalSchemas[0] -ne 'bsl-flow') {
    throw "Bootstrap completed partially, but openspec/config.yaml does not select schema: bsl-flow"
}

$finalSchemaValidation = Invoke-NativeCommand -Command $openSpecCommand.Source -Arguments @('schema', 'validate', 'bsl-flow', '--json')
if ($finalSchemaValidation.ExitCode -ne 0) {
    throw "Final schema validation failed.`n$($finalSchemaValidation.Output)"
}

foreach ($requiredFile in @('AGENTS.md', 'bsl-flow.yaml', '.gitignore', '.bsl-flow\project.yaml', 'openspec\config.yaml')) {
    if (-not (Test-Path -LiteralPath (Join-Path $projectRoot $requiredFile) -PathType Leaf)) {
        throw "Final bootstrap validation failed; required file is missing: $requiredFile"
    }
}
foreach ($requiredDirectory in @('.git', 'openspec\changes\archive', 'openspec\specs', '.bsl-flow\reports', '.bsl-flow\evidence')) {
    if (-not (Test-Path -LiteralPath (Join-Path $projectRoot $requiredDirectory) -PathType Container)) {
        throw "Final bootstrap validation failed; required directory is missing: $requiredDirectory"
    }
}

Write-Host "BSL Flow project bootstrap complete: $projectRoot"
Write-Host "Created: $($created.Count)"
foreach ($item in $created) {
    Write-Host "  + $item"
}
Write-Host "Preserved: $($preserved.Count)"
foreach ($item in $preserved) {
    Write-Host "  = $item"
}

if ($ConfigureTests) {
    if ([string]::IsNullOrWhiteSpace($DevelopmentDatabasePath)) { throw '-ConfigureTests requires -DevelopmentDatabasePath.' }
    $setupScript = Join-Path $PSScriptRoot 'Initialize-1CTestEnvironment.ps1'
    $setupParameters = @{ ProjectPath=$projectRoot; DevelopmentDatabasePath=$DevelopmentDatabasePath; ConfigureUi=$ConfigureUi }
    if (-not [string]::IsNullOrWhiteSpace($WorkstationProfilePath)) { $setupParameters.ProfilePath=$WorkstationProfilePath }
    if ($SourcePath) { $setupParameters.SourcePath=$SourcePath }
    & $setupScript @setupParameters
}
