[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ProjectPath,
    [switch]$Apply,
    [string]$PlanPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$targetVersion = '0.6.1'

function Get-AbsolutePath([string]$Path, [string]$Name) {
    if ($Path -notmatch '^(?:[A-Za-z]:[\\/]|[\\/]{2}[^\\/]+[\\/][^\\/]+(?:[\\/]|$))') { throw "$Name must be an absolute filesystem path." }
    [IO.Path]::GetFullPath($Path).TrimEnd([char]'\',[char]'/')
}

function Get-NewLine([string]$Text) { if ($Text.Contains("`r`n")) { "`r`n" } else { "`n" } }

function Get-YamlMap([string]$Text, [string]$Label) {
    $entries = @{}
    $stack = New-Object Collections.Generic.List[object]
    $lines = [regex]::Split($Text, '\r?\n')
    for ($i = 0; $i -lt $lines.Count; $i++) {
        $line = $lines[$i]
        if ($line -match '^\s*\t') { throw "$Label contains tab indentation at line $($i + 1)." }
        if ($line -match '^\s*(?:#.*)?$' -or $line -match '^\s*(?:---|\.\.\.)\s*$' -or $line -match '^\s*-\s+') { continue }
        $match = [regex]::Match($line, '^( *)([A-Za-z_][A-Za-z0-9_-]*):(?:\s*(.*))?$')
        if (-not $match.Success) { throw "$Label contains unsupported YAML at line $($i + 1): $line" }
        $indent = $match.Groups[1].Length
        if (($indent % 2) -ne 0) { throw "$Label uses unsupported odd indentation at line $($i + 1)." }
        while ($stack.Count -gt 0 -and $stack[$stack.Count - 1].indent -ge $indent) { $stack.RemoveAt($stack.Count - 1) }
        if ($indent -gt 0 -and ($stack.Count -eq 0 -or $stack[$stack.Count - 1].indent -ne ($indent - 2))) {
            throw "$Label has an unsupported indentation jump at line $($i + 1)."
        }
        $key = $match.Groups[2].Value
        $path = if ($stack.Count) { $stack[$stack.Count - 1].path + '.' + $key } else { $key }
        if ($entries.ContainsKey($path)) { throw "$Label contains duplicate key path '$path'." }
        $rawValue = $match.Groups[3].Value
        $valueWithoutComment = ($rawValue -replace '\s+#.*$','').Trim()
        $isMap = [string]::IsNullOrWhiteSpace($valueWithoutComment)
        $entry = [pscustomobject]@{ path=$path; key=$key; indent=$indent; line=$i; is_map=$isMap; raw=$line }
        $entries[$path] = $entry
        if ($isMap) { $stack.Add($entry) }
    }
    [pscustomobject]@{ lines=$lines; entries=$entries }
}

function Get-Subtree([object]$Parsed, [object]$Entry) {
    $start = $Entry.line
    $end = $Parsed.lines.Count
    for ($i = $start + 1; $i -lt $Parsed.lines.Count; $i++) {
        $line = $Parsed.lines[$i]
        if ($line -match '^\s*(?:#.*)?$') { continue }
        $spaces = ([regex]::Match($line, '^( *)')).Groups[1].Value.Length
        if ($spaces -le $Entry.indent) { $end = $i; break }
    }
    ,@($Parsed.lines[$start..($end - 1)])
}

function Add-MissingTemplateNodes([string]$CurrentText, [string]$TemplateText, [Collections.Generic.List[object]]$Actions) {
    $newLine = Get-NewLine $CurrentText
    $current = Get-YamlMap $CurrentText 'bsl-flow.yaml'
    $template = Get-YamlMap $TemplateText 'packaged bsl-flow.yaml template'
    $orderedTemplate = @($template.entries.Values | Sort-Object line)
    foreach ($wanted in $orderedTemplate) {
        $current = Get-YamlMap $CurrentText 'bsl-flow.yaml'
        if ($current.entries.ContainsKey($wanted.path)) {
            $have = $current.entries[$wanted.path]
            if ($wanted.is_map -and -not $have.is_map) { throw "Managed path '$($wanted.path)' must be a mapping, but the project contains a scalar." }
            continue
        }
        $parentPath = if ($wanted.path.Contains('.')) { $wanted.path.Substring(0, $wanted.path.LastIndexOf('.')) } else { $null }
        if ($parentPath -and -not $current.entries.ContainsKey($parentPath)) { continue }
        $subtree = Get-Subtree $template $wanted
        if (-not $wanted.is_map) { $subtree = @($subtree[0]) }
        if (-not $parentPath) {
            $CurrentText = $CurrentText.TrimEnd("`r","`n") + $newLine + $newLine + ($subtree -join $newLine) + $newLine
        }
        else {
            $parent = $current.entries[$parentPath]
            if (-not $parent.is_map) { throw "Managed parent '$parentPath' is not a mapping." }
            $insertAt = $current.lines.Count
            for ($i = $parent.line + 1; $i -lt $current.lines.Count; $i++) {
                $line = $current.lines[$i]
                if ($line -match '^\s*(?:#.*)?$') { continue }
                $spaces = ([regex]::Match($line, '^( *)')).Groups[1].Value.Length
                if ($spaces -le $parent.indent) { $insertAt = $i; break }
            }
            $before = if ($insertAt -gt 0) { @($current.lines[0..($insertAt - 1)]) } else { @() }
            $after = if ($insertAt -lt $current.lines.Count) { @($current.lines[$insertAt..($current.lines.Count - 1)]) } else { @() }
            $CurrentText = (@($before) + @($subtree) + @($after)) -join $newLine
        }
        $Actions.Add([pscustomobject]@{ action='add_managed_path'; path=$wanted.path })
    }
    $CurrentText
}

function Set-SentinelVersion([string]$Text, [string]$Version) {
    $matches = [regex]::Matches($Text, '(?m)^\s*framework_version:\s*([^\s#]+)')
    if ($matches.Count -gt 1) { throw 'Project sentinel contains duplicate framework_version keys.' }
    if ($matches.Count -eq 0) { return $Text.TrimEnd("`r","`n") + (Get-NewLine $Text) + "framework_version: `"$Version`"" + (Get-NewLine $Text) }
    [regex]::Replace($Text, '(?m)^(\s*framework_version:\s*)[^\s#]+', ('$1"' + $Version + '"'), 1)
}

function Get-UpdatedGitIgnore([string]$Current,[string]$Template) {
    if ($null -eq $Current) { return $Template.TrimEnd("`r","`n") + [Environment]::NewLine }
    $pattern='(?ms)^# bsl-flow managed:start[^\r\n]*(?:\r?\n).*?^# bsl-flow managed:end[^\r\n]*'
    if ([regex]::IsMatch($Current,$pattern)) { return [regex]::Replace($Current,$pattern,$Template.TrimEnd("`r","`n"),1) }
    return $Current.TrimEnd("`r","`n") + (Get-NewLine $Current) + (Get-NewLine $Current) + $Template.TrimEnd("`r","`n") + (Get-NewLine $Current)
}

$project = Get-AbsolutePath $ProjectPath 'ProjectPath'
$configPath = Join-Path $project 'bsl-flow.yaml'
$sentinelPath = Join-Path $project '.bsl-flow\project.yaml'
if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) { throw "Missing project configuration: $configPath" }
if (-not (Test-Path -LiteralPath $sentinelPath -PathType Leaf)) { throw "Missing project sentinel: $sentinelPath" }

$sentinelText = [IO.File]::ReadAllText($sentinelPath)
$framework = [regex]::Match($sentinelText, '(?m)^\s*framework:\s*([^\s#]+)')
if (-not $framework.Success -or $framework.Groups[1].Value.Trim('"',"'") -ne 'bsl-flow') { throw 'Project sentinel is not owned by bsl-flow.' }
$currentVersionMatch = [regex]::Match($sentinelText, '(?m)^\s*framework_version:\s*([^\s#]+)')
$currentVersion = if ($currentVersionMatch.Success) { $currentVersionMatch.Groups[1].Value.Trim('"',"'") } else { 'unknown' }
if ($currentVersion -ne 'unknown') {
    try { if ([version]$currentVersion -gt [version]$targetVersion) { throw "Project version $currentVersion is newer than installed framework $targetVersion." } }
    catch [System.Management.Automation.RuntimeException] { throw }
    catch { throw "Unsupported framework_version '$currentVersion'." }
}

$skillRoot = Split-Path -Parent $PSScriptRoot
$templatePath = Join-Path $skillRoot 'assets\project\bsl-flow.yaml'
$gitIgnoreTemplatePath = Join-Path $skillRoot 'assets\project\.gitignore'
if (-not (Test-Path -LiteralPath $templatePath -PathType Leaf)) { throw "Missing packaged template: $templatePath" }
if (-not (Test-Path -LiteralPath $gitIgnoreTemplatePath -PathType Leaf)) { throw "Missing packaged template: $gitIgnoreTemplatePath" }
$configText = [IO.File]::ReadAllText($configPath)
$templateText = [IO.File]::ReadAllText($templatePath)
$actions = New-Object Collections.Generic.List[object]
$mergedText = Add-MissingTemplateNodes $configText $templateText $actions
$gitIgnorePath=Join-Path $project '.gitignore'
if(Test-Path -LiteralPath $gitIgnorePath -PathType Container){throw "A directory exists where .gitignore is required: $gitIgnorePath"}
$gitIgnoreOriginal=if(Test-Path -LiteralPath $gitIgnorePath -PathType Leaf){[IO.File]::ReadAllText($gitIgnorePath)}else{$null}
$gitIgnoreUpdated=Get-UpdatedGitIgnore $gitIgnoreOriginal ([IO.File]::ReadAllText($gitIgnoreTemplatePath))
if($gitIgnoreUpdated -ne $gitIgnoreOriginal){$actions.Add([pscustomobject]@{action='update_managed_gitignore';path='.gitignore'})}
$newSentinel = Set-SentinelVersion $sentinelText $targetVersion
if ($newSentinel -ne $sentinelText) { $actions.Add([pscustomobject]@{action='update_framework_version';path='.bsl-flow/project.yaml';from=$currentVersion;to=$targetVersion}) }

$planStatus = if ($actions.Count -gt 0) { 'changes_planned' } else { 'up_to_date' }
$actionArray = @($actions | ForEach-Object { $_ })
$plan = [pscustomobject]@{
    schema_version=1; project=$project; installed_framework_version=$targetVersion; project_framework_version=$currentVersion
    status=$planStatus; actions=$actionArray; apply_requested=[bool]$Apply
}
$planJson = $plan | ConvertTo-Json -Depth 8
Write-Host $planJson
if ($PlanPath) {
    $planFile = Get-AbsolutePath $PlanPath 'PlanPath'
    [IO.Directory]::CreateDirectory((Split-Path -Parent $planFile)) | Out-Null
    [IO.File]::WriteAllText($planFile, $planJson + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
}
if (-not $Apply -or $actions.Count -eq 0) { return $plan }

$configOriginal = $configText
$sentinelOriginal = $sentinelText
try {
    [void](Get-YamlMap $mergedText 'migrated bsl-flow.yaml')
    [IO.File]::WriteAllText($configPath, $mergedText, [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText($gitIgnorePath, $gitIgnoreUpdated, [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText($sentinelPath, $newSentinel, [Text.UTF8Encoding]::new($false))
}
catch {
    [IO.File]::WriteAllText($configPath, $configOriginal, [Text.UTF8Encoding]::new($false))
    if($null-eq$gitIgnoreOriginal){if(Test-Path -LiteralPath $gitIgnorePath -PathType Leaf){Remove-Item -LiteralPath $gitIgnorePath -Force}}else{[IO.File]::WriteAllText($gitIgnorePath,$gitIgnoreOriginal,[Text.UTF8Encoding]::new($false))}
    [IO.File]::WriteAllText($sentinelPath, $sentinelOriginal, [Text.UTF8Encoding]::new($false))
    throw "Project upgrade failed and original files were restored: $($_.Exception.Message)"
}
$plan | Add-Member -NotePropertyName applied -NotePropertyValue $true
$plan.status = 'applied'
$plan
