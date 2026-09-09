[CmdletBinding(SupportsShouldProcess)]
param(
    [string]$OpenCodeConfigRoot,
    [string]$OpenCodePath,
    [string]$SharedSkillsRoot,
    [switch]$Apply,
    [switch]$SkipCliValidation,
    [switch]$SimulatePostApplyFailure
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$frameworkVersion = $null
$retiredFrameworkName='1'+'c-'+'lite'
$skillNames = @('1c-init-project','1c-spec','1c-spec-review','1c-implement','1c-verify','1c-debug','1c-task')

function Assert-TestMode {
    param([Parameter(Mandatory)][string]$ConfigRoot)
    $temp = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\','/')
    $resolved = [IO.Path]::GetFullPath($ConfigRoot).TrimEnd('\','/')
    if (-not $resolved.StartsWith($temp + [IO.Path]::DirectorySeparatorChar,[StringComparison]::OrdinalIgnoreCase)) { throw 'Test-only switches are allowed only below the system temporary directory.' }
    $relative = $resolved.Substring($temp.Length + 1)
    if ($relative -notmatch '^bsl-flow-opencode-test-[^\\/]+[\\/]opencode$') { throw 'Test-only switches require <temp>/bsl-flow-opencode-test-*/opencode.' }
}

function Invoke-NativeCommand {
    param([Parameter(Mandatory)][string]$Command,[Parameter(Mandatory)][string[]]$Arguments)
    $previous=$ErrorActionPreference
    try { $ErrorActionPreference='Continue'; $output=& $Command @Arguments 2>&1 | ForEach-Object {$_.ToString()} | Out-String; $exitCode=$LASTEXITCODE }
    finally { $ErrorActionPreference=$previous }
    if($exitCode -ne 0){throw "Command failed: $Command $($Arguments -join ' ')`n$($output.Trim())"}
    $ansi=[string]([char]27)+'\[[0-?]*[ -/]*[@-~]'
    ([regex]::Replace($output,$ansi,'')).Trim()
}

function Get-TreeManifest {
    param([Parameter(Mandatory)][string]$Root)
    $normalized=[IO.Path]::GetFullPath($Root).TrimEnd('\','/')
    @((Get-ChildItem -LiteralPath $normalized -Recurse -File -Force | Sort-Object FullName | ForEach-Object {
        [ordered]@{path=$_.FullName.Substring($normalized.Length+1).Replace('\','/');sha256=(Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash}
    }))
}

function Assert-NoNestedReparsePoint {
    param([Parameter(Mandatory)][string]$Root)
    if(-not(Test-Path -LiteralPath $Root -PathType Container)){return}
    $reparse=@(Get-ChildItem -LiteralPath $Root -Recurse -Force | Where-Object {($_.Attributes-band[IO.FileAttributes]::ReparsePoint)-ne0})
    if($reparse.Count-gt0){throw "Managed tree contains a nested reparse point: $($reparse[0].FullName)"}
}

function Compare-TreeManifest {
    param([Parameter(Mandatory)][string]$Root,[Parameter(Mandatory)][object[]]$Expected)
    if(-not(Test-Path -LiteralPath $Root -PathType Container)){return $false}
    $actual=@(Get-TreeManifest $Root | ForEach-Object{"$($_.path)|$($_.sha256)"})
    $wanted=@($Expected | ForEach-Object{"$($_.path)|$($_.sha256)"})
    -not [bool](Compare-Object $actual $wanted)
}

function Get-StringSha256 {
    param([Parameter(Mandatory)][string]$Text)
    $sha=[Security.Cryptography.SHA256]::Create()
    try { -join ($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($Text)) | ForEach-Object {$_.ToString('X2')}) }
    finally { $sha.Dispose() }
}

function Get-TextFileState {
    param([Parameter(Mandatory)][string]$Path)
    if(-not(Test-Path -LiteralPath $Path -PathType Leaf)){return [ordered]@{exists=$false;text='';encoding='utf8-no-bom';newline="`n"}}
    $bytes=[IO.File]::ReadAllBytes($Path)
    $encoding='utf8-no-bom'; $offset=0
    if($bytes.Length-ge3 -and $bytes[0]-eq0xEF -and $bytes[1]-eq0xBB -and $bytes[2]-eq0xBF){$encoding='utf8-bom';$offset=3;$decoder=[Text.UTF8Encoding]::new($false)}
    elseif($bytes.Length-ge2 -and $bytes[0]-eq0xFF -and $bytes[1]-eq0xFE){$encoding='utf16-le';$offset=2;$decoder=[Text.UnicodeEncoding]::new($false,$false)}
    elseif($bytes.Length-ge2 -and $bytes[0]-eq0xFE -and $bytes[1]-eq0xFF){$encoding='utf16-be';$offset=2;$decoder=[Text.UnicodeEncoding]::new($true,$false)}
    else{$decoder=[Text.UTF8Encoding]::new($false,$true)}
    $text=$decoder.GetString($bytes,$offset,$bytes.Length-$offset)
    $newline=if($text.Contains("`r`n")){"`r`n"}else{"`n"}
    [ordered]@{exists=$true;text=$text;encoding=$encoding;newline=$newline}
}

function Write-TextAtomic {
    param([Parameter(Mandatory)][string]$Path,[Parameter(Mandatory)][string]$Text,[Parameter(Mandatory)][string]$EncodingName)
    $parent=Split-Path -Parent $Path; [IO.Directory]::CreateDirectory($parent)|Out-Null
    switch($EncodingName){
        'utf8-bom' {$encoding=[Text.UTF8Encoding]::new($true)}
        'utf16-le' {$encoding=[Text.UnicodeEncoding]::new($false,$true)}
        'utf16-be' {$encoding=[Text.UnicodeEncoding]::new($true,$true)}
        default {$encoding=[Text.UTF8Encoding]::new($false)}
    }
    $temp=Join-Path $parent ('.bsl-flow-write-'+[Guid]::NewGuid().ToString('N')+'.tmp')
    try{
        [IO.File]::WriteAllText($temp,$Text,$encoding)
        Move-Item -LiteralPath $temp -Destination $Path -Force
    }finally{if(Test-Path -LiteralPath $temp){Remove-Item -LiteralPath $temp -Force -ErrorAction SilentlyContinue}}
}

function Get-ManagedBlock {
    param([string]$Text,[string]$Marker)
    $start="<!-- ${Marker}:start -->"; $end="<!-- ${Marker}:end -->"
    $startMatches=[regex]::Matches($Text,[regex]::Escape($start),'IgnoreCase'); $endMatches=[regex]::Matches($Text,[regex]::Escape($end),'IgnoreCase')
    if($startMatches.Count-ne$endMatches.Count -or $startMatches.Count-gt1){throw "Malformed or duplicate managed marker: $Marker"}
    if($startMatches.Count-eq0){return $null}
    if($startMatches[0].Index-ge$endMatches[0].Index){throw "Managed markers are out of order: $Marker"}
    $pattern="(?ms)^$([regex]::Escape($start))[^\r\n]*(?:\r?\n).*?^$([regex]::Escape($end))[^\r\n]*"
    $match=[regex]::Match($Text,$pattern)
    if(-not$match.Success){throw "Managed block cannot be parsed: $Marker"}
    $match.Value
}

function Merge-ManagedBlock {
    param([string]$Text,[string]$Block,[string]$Marker,[string]$NewLine)
    $existing=Get-ManagedBlock $Text $Marker
    $normalized=($Block -replace "`r?`n",$NewLine).Trim()
    if($null-ne$existing){$index=$Text.IndexOf($existing);return $Text.Remove($index,$existing.Length).Insert($index,$normalized)}
    if([string]::IsNullOrWhiteSpace($Text)){return $normalized+$NewLine}
    $Text.TrimEnd("`r","`n")+$NewLine+$NewLine+$normalized+$NewLine
}

function Remove-ManagedBlockIfPresent {
    param([string]$Text,[string]$Marker)
    $existing=Get-ManagedBlock $Text $Marker
    if($null-eq$existing){return $Text}
    $index=$Text.IndexOf($existing)
    $Text.Remove($index,$existing.Length).TrimEnd("`r","`n")
}

function Test-ReviewerConfig {
    param([string]$Command,[string]$Config)
    $oldConfig=$env:OPENCODE_CONFIG;$oldProject=$env:OPENCODE_DISABLE_PROJECT_CONFIG;$oldClaude=$env:OPENCODE_DISABLE_CLAUDE_CODE
    try{
        $env:OPENCODE_CONFIG=$Config;$env:OPENCODE_DISABLE_PROJECT_CONFIG='1';$env:OPENCODE_DISABLE_CLAUDE_CODE='1'
        foreach($name in @('bsl-flow-spec-reviewer','bsl-flow-spec-reviewer-sealed')){
            $agent=(Invoke-NativeCommand $Command @('debug','agent',$name))|ConvertFrom-Json
            foreach($tool in @('edit','write','bash','task','webfetch','skill')){if($agent.tools.$tool-ne$false){throw "Reviewer '$name' exposes forbidden tool: $tool"}}
            if($name-eq'bsl-flow-spec-reviewer'){
                foreach($tool in @('read','glob')){if($agent.tools.$tool-ne$true){throw "Reviewer '$name' is missing required read-only tool: $tool"}}
                if($agent.tools.grep-ne$false){throw "Reviewer '$name' unexpectedly exposes grep."}
            }else{
                foreach($tool in @('read','glob','grep')){if($agent.tools.$tool-ne$false){throw "Reviewer '$name' is not sealed: $tool"}}
            }
        }
    }finally{
        if($null-eq$oldConfig){Remove-Item Env:OPENCODE_CONFIG -ErrorAction SilentlyContinue}else{$env:OPENCODE_CONFIG=$oldConfig}
        if($null-eq$oldProject){Remove-Item Env:OPENCODE_DISABLE_PROJECT_CONFIG -ErrorAction SilentlyContinue}else{$env:OPENCODE_DISABLE_PROJECT_CONFIG=$oldProject}
        if($null-eq$oldClaude){Remove-Item Env:OPENCODE_DISABLE_CLAUDE_CODE -ErrorAction SilentlyContinue}else{$env:OPENCODE_DISABLE_CLAUDE_CODE=$oldClaude}
    }
}

$packageRoot=Split-Path -Parent $PSScriptRoot
$frameworkVersion=(Get-Content -Raw -LiteralPath (Join-Path $packageRoot 'VERSION')).Trim()
if($frameworkVersion-notmatch'^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$'){throw "Invalid package VERSION: $frameworkVersion"}
$sourceSkills=Join-Path $packageRoot 'global\skills';$sourceBootstrap=Join-Path $packageRoot 'global\AGENTS.bootstrap.md';$sourceDelegation=Join-Path $packageRoot 'global\OPENCODE.delegation.md';$diagnostic=Join-Path $PSScriptRoot 'Test-BSLFlowOpenCode.ps1'
$userProfile=[Environment]::GetFolderPath('UserProfile')
if([string]::IsNullOrWhiteSpace($OpenCodeConfigRoot)){$OpenCodeConfigRoot=Join-Path $userProfile '.config\opencode'}
$defaultSharedSkillsRoot=Join-Path $userProfile '.agents\skills'
if([string]::IsNullOrWhiteSpace($SharedSkillsRoot)){$SharedSkillsRoot=$defaultSharedSkillsRoot}
$configRoot=[IO.Path]::GetFullPath($OpenCodeConfigRoot).TrimEnd('\','/');$profileRoot=[IO.Path]::GetFullPath($userProfile).TrimEnd('\','/');$driveRoot=[IO.Path]::GetPathRoot($configRoot).TrimEnd('\','/')
$sharedSkills=[IO.Path]::GetFullPath($SharedSkillsRoot).TrimEnd('\','/')
if($configRoot-eq$profileRoot -or $configRoot-eq$driveRoot){throw "Unsafe OpenCode config target: $configRoot"}
if((Split-Path -Leaf $configRoot)-ine'opencode'){throw "OpenCode config root must be a directory named 'opencode': $configRoot"}
if($SkipCliValidation -or $SimulatePostApplyFailure){Assert-TestMode $configRoot}
if($sharedSkills-ne[IO.Path]::GetFullPath($defaultSharedSkillsRoot).TrimEnd('\','/')){
    if(-not($SkipCliValidation -or $SimulatePostApplyFailure)){throw 'A custom SharedSkillsRoot is test-only; production installation uses ~/.agents/skills.'}
    $expectedTestRoot=Join-Path (Split-Path -Parent $configRoot) '.agents\skills'
    if($sharedSkills-ne[IO.Path]::GetFullPath($expectedTestRoot).TrimEnd('\','/')){throw 'Fixture SharedSkillsRoot must be the sibling .agents/skills directory.'}
}
foreach($required in @($sourceSkills,$sourceBootstrap,$sourceDelegation,$diagnostic)){if(-not(Test-Path -LiteralPath $required)){throw "Package is incomplete: $required"}}

if(Test-Path -LiteralPath $configRoot -PathType Leaf){throw "A file exists where config directory is required: $configRoot"}
if(Test-Path -LiteralPath $configRoot){$item=Get-Item -LiteralPath $configRoot -Force;if(($item.Attributes-band[IO.FileAttributes]::ReparsePoint)-ne0){throw "Config root must not be a reparse point: $configRoot"}}
$targetSkills=$sharedSkills;$targetAgents=Join-Path $configRoot 'AGENTS.md';$stateRoot=Join-Path $configRoot '.bsl-flow';$manifestPath=Join-Path $stateRoot 'manifest.json'
if(Test-Path -LiteralPath $targetSkills -PathType Leaf){throw "A file exists where skills directory is required: $targetSkills"}
if(Test-Path -LiteralPath $targetAgents -PathType Container){throw "A directory exists where AGENTS.md is required: $targetAgents"}
foreach($container in @($targetSkills,$stateRoot,(Join-Path $stateRoot 'backups'))){if(Test-Path -LiteralPath $container){$containerItem=Get-Item -LiteralPath $container -Force;if(-not$containerItem.PSIsContainer -or ($containerItem.Attributes-band[IO.FileAttributes]::ReparsePoint)-ne0){throw "Managed container must be a real directory, not a file or reparse point: $container"}}}
foreach($fileTarget in @($targetAgents,$manifestPath)){if(Test-Path -LiteralPath $fileTarget){$fileItem=Get-Item -LiteralPath $fileTarget -Force;if(($fileItem.Attributes-band[IO.FileAttributes]::ReparsePoint)-ne0){throw "Managed file must not be a reparse point: $fileTarget"}}}
Assert-NoNestedReparsePoint $stateRoot

$sourceManifests=@{}
foreach($name in $skillNames){
    $source=Join-Path $sourceSkills $name;$skillFile=Join-Path $source 'SKILL.md'
    if(-not(Test-Path -LiteralPath $skillFile -PathType Leaf)){throw "Package skill is incomplete: $name"}
    $skillText=Get-Content -Raw -LiteralPath $skillFile
    if($skillText-notmatch"(?ms)^---\s*\r?\nname:\s*$([regex]::Escape($name))\s*\r?\ndescription:\s*\S.+?\r?\n---"){throw "Invalid packaged skill frontmatter: $name"}
    $sourceManifests[$name]=@(Get-TreeManifest $source)
}

$existingManifest=$null
if(Test-Path -LiteralPath $manifestPath -PathType Leaf){$existingManifest=Get-Content -Raw -LiteralPath $manifestPath|ConvertFrom-Json -ErrorAction Stop;if($existingManifest.schema_version-ne1){throw 'Unsupported OpenCode adapter manifest schema.'}}
foreach($compatRoot in @((Join-Path $configRoot 'skills'),(Join-Path $userProfile '.claude\skills'))){foreach($name in $skillNames){$collision=Join-Path $compatRoot $name;if(Test-Path -LiteralPath $collision){throw "Conflicting duplicate skill source exists: $collision"}}}

$actions=New-Object System.Collections.Generic.List[object]
foreach($name in $skillNames){
    $target=Join-Path $targetSkills $name
    if(Test-Path -LiteralPath $target){
        $item=Get-Item -LiteralPath $target -Force
        if(-not$item.PSIsContainer -or ($item.Attributes-band[IO.FileAttributes]::ReparsePoint)-ne0){throw "Unsafe managed skill target: $target"}
        Assert-NoNestedReparsePoint $target
        if(Compare-TreeManifest $target $sourceManifests[$name]){continue}
        if($null-eq$existingManifest){throw "Existing skill differs and has no bsl-flow manifest: $target"}
        $record=@($existingManifest.skills | Where-Object name -eq $name)
        if($record.Count-ne1 -or -not(Compare-TreeManifest $target @($record[0].files))){throw "Existing skill changed outside the recorded bsl-flow manifest: $target"}
        $actions.Add([ordered]@{action='update_skill';name=$name})
    }else{$actions.Add([ordered]@{action='install_skill';name=$name})}
}

$agentsState=Get-TextFileState $targetAgents;$currentBootstrap=Get-ManagedBlock $agentsState.text 'bsl-flow bootstrap';$currentDelegation=Get-ManagedBlock $agentsState.text 'bsl-flow opencode'
if($null-ne$existingManifest){
    foreach($pair in @(@('bsl-flow bootstrap',$currentBootstrap,[string]$existingManifest.agents.bootstrap_sha256),@('bsl-flow opencode',$currentDelegation,[string]$existingManifest.agents.opencode_sha256))){
        if($null-eq$pair[1]){throw "Recorded managed AGENTS block is missing: $($pair[0])"}
        if((Get-StringSha256 ([string]$pair[1]))-ne$pair[2]){throw "Managed AGENTS block changed outside the manifest: $($pair[0])"}
    }
}elseif($null-ne$currentBootstrap -or $null-ne$currentDelegation){throw 'Existing bsl-flow AGENTS markers have no chain-of-custody manifest.'}

$agentsBase=Remove-ManagedBlockIfPresent $agentsState.text "$retiredFrameworkName bootstrap"
$agentsBase=Remove-ManagedBlockIfPresent $agentsBase "$retiredFrameworkName opencode"
$newAgents=Merge-ManagedBlock $agentsBase (Get-Content -Raw -LiteralPath $sourceBootstrap) 'bsl-flow bootstrap' $agentsState.newline
$newAgents=Merge-ManagedBlock $newAgents (Get-Content -Raw -LiteralPath $sourceDelegation) 'bsl-flow opencode' $agentsState.newline
if($newAgents-ne$agentsState.text){$actions.Add([ordered]@{action=if($agentsState.exists){'update_agents'}else{'create_agents'};path='AGENTS.md'})}
if($null-eq$existingManifest -or [string]$existingManifest.framework_version-ne$frameworkVersion){$actions.Add([ordered]@{action='write_manifest';path='.bsl-flow/manifest.json'})}

if(-not$SkipCliValidation){
    if([string]::IsNullOrWhiteSpace($OpenCodePath)){$command=Get-Command opencode -ErrorAction SilentlyContinue;if(-not$command){throw 'OpenCode CLI was not found in PATH.'};$OpenCodePath=$command.Source}
    $OpenCodePath=[IO.Path]::GetFullPath($OpenCodePath);if(-not(Test-Path -LiteralPath $OpenCodePath -PathType Leaf)){throw "OpenCode executable not found: $OpenCodePath"}
    $openspec=Get-Command openspec -ErrorAction SilentlyContinue;if(-not$openspec){throw 'OpenSpec CLI was not found in PATH.'}
    [void](Invoke-NativeCommand $openspec.Source @('schema','validate','bsl-flow','--json'))
    Test-ReviewerConfig $OpenCodePath (Join-Path $sourceSkills '1c-spec-review\reviewer\opencode-reviewer.json')
}

[object[]]$actionArray=$actions.ToArray()
$plan=[ordered]@{schema_version=1;framework_version=$frameworkVersion;config_root=$configRoot;shared_skills_root=$targetSkills;status=if($actions.Count){'changes_planned'}else{'up_to_date'};actions=$actionArray;apply_requested=[bool]$Apply;preserves=@('opencode.json','providers','credentials','unmanaged skills')}
$plan|ConvertTo-Json -Depth 8
if(-not$Apply -or $actions.Count-eq0){return}
if(-not$PSCmdlet.ShouldProcess($configRoot,"Apply bsl-flow v$frameworkVersion OpenCode adapter plan")){return}

$backupRoot=Join-Path $stateRoot ('backups\v'+$frameworkVersion+'-'+(Get-Date -Format 'yyyyMMdd-HHmmssfff'));$hadAgents=$agentsState.exists;$existingSkills=@{}
foreach($name in $skillNames){$existingSkills[$name]=Test-Path -LiteralPath (Join-Path $targetSkills $name) -PathType Container}
[IO.Directory]::CreateDirectory($backupRoot)|Out-Null
if($hadAgents){Copy-Item -LiteralPath $targetAgents -Destination (Join-Path $backupRoot 'AGENTS.md')}
if(Test-Path -LiteralPath $manifestPath -PathType Leaf){Copy-Item -LiteralPath $manifestPath -Destination (Join-Path $backupRoot 'manifest.json')}
foreach($name in $skillNames){if($existingSkills[$name]){[IO.Directory]::CreateDirectory((Join-Path $backupRoot 'skills'))|Out-Null;Copy-Item -LiteralPath (Join-Path $targetSkills $name) -Destination (Join-Path $backupRoot 'skills') -Recurse}}

try{
    [IO.Directory]::CreateDirectory($targetSkills)|Out-Null
    foreach($name in $skillNames){$target=Join-Path $targetSkills $name;if(Test-Path -LiteralPath $target){Remove-Item -LiteralPath $target -Recurse -Force};Copy-Item -LiteralPath (Join-Path $sourceSkills $name) -Destination $targetSkills -Recurse}
    Write-TextAtomic $targetAgents $newAgents $agentsState.encoding
    if($SimulatePostApplyFailure){throw 'Simulated post-apply failure.'}
    if(-not$SkipCliValidation){$validation=& $diagnostic -OpenCodeConfigRoot $configRoot -OpenCodePath $OpenCodePath -SharedSkillsRoot $targetSkills}
    else{$validation=([ordered]@{status='SKIPPED_TEST_ONLY';reason='temporary_fixture_contract_test'}|ConvertTo-Json)}
    $installedText=Get-Content -Raw -LiteralPath $targetAgents;$installedBootstrap=Get-ManagedBlock $installedText 'bsl-flow bootstrap';$installedDelegation=Get-ManagedBlock $installedText 'bsl-flow opencode'
    $manifest=[ordered]@{schema_version=1;framework_version=$frameworkVersion;installed_at_utc=[DateTime]::UtcNow.ToString('o');backup_path=$backupRoot;shared_skills_root=$targetSkills;skills=@($skillNames|ForEach-Object{[ordered]@{name=$_;files=@(Get-TreeManifest (Join-Path $targetSkills $_))}});agents=[ordered]@{bootstrap_sha256=Get-StringSha256 $installedBootstrap;opencode_sha256=Get-StringSha256 $installedDelegation}}
    [IO.Directory]::CreateDirectory($stateRoot)|Out-Null
    Write-TextAtomic $manifestPath (($manifest|ConvertTo-Json -Depth 12)+"`n") 'utf8-no-bom'
}
catch{
    $installError=$_
    try{
        foreach($name in $skillNames){$target=Join-Path $targetSkills $name;if(Test-Path -LiteralPath $target){Remove-Item -LiteralPath $target -Recurse -Force};if($existingSkills[$name]){Copy-Item -LiteralPath (Join-Path $backupRoot "skills\$name") -Destination $targetSkills -Recurse}}
        if($hadAgents){Copy-Item -LiteralPath (Join-Path $backupRoot 'AGENTS.md') -Destination $targetAgents -Force}elseif(Test-Path -LiteralPath $targetAgents){Remove-Item -LiteralPath $targetAgents -Force}
        if(Test-Path -LiteralPath (Join-Path $backupRoot 'manifest.json')){Copy-Item -LiteralPath (Join-Path $backupRoot 'manifest.json') -Destination $manifestPath -Force}elseif(Test-Path -LiteralPath $manifestPath){Remove-Item -LiteralPath $manifestPath -Force}
    }catch{throw "Adapter install and rollback failed. Original: $($installError.Exception.Message). Rollback: $($_.Exception.Message). Backup: $backupRoot"}
    throw "Adapter install failed; previous managed files were restored. Error: $($installError.Exception.Message). Backup: $backupRoot"
}

Write-Host "BSL Flow v$frameworkVersion OpenCode adapter installed."
Write-Host "OpenCode config: $configRoot"
Write-Host "Shared skills: $targetSkills"
Write-Host "Backup: $backupRoot"
Write-Host 'opencode.json, providers, credentials and unmanaged skills were preserved.'
$validation
