#Requires -Version 7.0
# Stage C contract: architecture bundle is deterministic, subject-filtered,
# size-bounded, hash-bound into stage dependencies, and prompt-safe.
[CmdletBinding()]param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$root=[IO.Path]::GetFullPath($PackageRoot).TrimEnd('\','/')
$scripts=Join-Path $root 'global/skills/1c-task/scripts'
foreach($module in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Architecture.ps1','Task.Gates.ps1','Task.Process.ps1','Task.Engine.ps1','Task.Stages.ps1')){. (Join-Path $scripts $module)}
$script:checks=0
function Assert-B([bool]$Condition,[string]$Message){if(-not $Condition){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Failure-B([scriptblock]$Action){try{& $Action|Out-Null;return ''}catch{return $_.Exception.Message}}
$utf8=[Text.UTF8Encoding]::new($false)
function Get-Ids($Bundle){return @($Bundle.decisions | ForEach-Object { $_.id })}

# 1. Subject filtering is minimal and deterministic.
$review=Get-BFArchitectureBundle 'code_review'
$reviewIds=Get-Ids $review
Assert-B (($reviewIds -join ',') -ceq 'ADR-10,ADR-2,ADR-3,ADR-4,ADR-8') ('code_review bundle changed: '+($reviewIds -join ','))
Assert-B ($reviewIds -contains 'ADR-4' -and $reviewIds -notcontains 'ADR-7' -and $reviewIds -notcontains 'ADR-9') 'code_review bundle mixed inapplicable decisions.'
Assert-B (@($review.decisions[0].excerpt).Count -eq 1 -and $review.decisions[0].excerpt.Length -gt 20) 'Bundle excerpt is missing.'
Assert-B ((Get-BFArchitectureBundleHash 'code_review') -ceq (Get-BFArchitectureBundleHash 'code_review')) 'Bundle hash is not deterministic.'
Assert-B ($review.subjects -contains 'adapter.codex') 'code_review subject set lost its adapter.'
Assert-B ($review.decisions[0].section_sha256 -match '^[0-9a-f]{64}$') 'Bundle omitted the full ADR section hash.'
Assert-B (@($review.subject_refs).Count -ge 1 -and @($review.subject_refs[0].refs).Count -ge 1) 'Bundle omitted subject references.'

$verifyIds=Get-Ids (Get-BFArchitectureBundle 'verify')
Assert-B (($verifyIds -join ',') -ceq 'ADR-10,ADR-2,ADR-3,ADR-7,ADR-8') ('verify bundle changed: '+($verifyIds -join ','))
$acceptIds=Get-Ids (Get-BFArchitectureBundle 'acceptance')
Assert-B ($acceptIds -contains 'ADR-9' -and $acceptIds -notcontains 'ADR-4') 'acceptance bundle did not select publication.'
$unknownBundle=Get-BFArchitectureBundle 'unknown-stage'
Assert-B (@($unknownBundle.decisions).Count -eq 0) 'Unknown stage received a bundle.'
$reviewBundle=Get-BFArchitectureBundle 'code_review'
Assert-B (@($reviewBundle.decisions).Count -le $script:BFArchitectureBundleMaxDecisions) 'Bundle exceeded the decision limit.'
$totalChars=@($reviewBundle.decisions | ForEach-Object { $_.excerpt.Length }) | Measure-Object -Sum
Assert-B ($totalChars.Sum -le $script:BFArchitectureBundleMaxChars) 'Bundle exceeded the character limit.'
# A lowered budget must bound the real rendered prompt and record exclusions.
$savedMax=$script:BFArchitectureBundleMaxChars
try {
    $script:BFArchitectureBundleMaxChars=320
    $small=Get-BFArchitectureBundle 'code_review'
    Assert-B ((Format-BFArchitectureBundlePrompt $small).Length -le 320) 'Rendered bundle exceeded the configured limit.'
    Assert-B (@($small.excluded).Count -ge 1) 'Size cap did not record excluded decisions.'
} finally { $script:BFArchitectureBundleMaxChars=$savedMax }

# 2. Dependency binding: the bundle hash is part of stage dependencies.
$state=[pscustomobject]@{task_id=([guid]::NewGuid().ToString());intent_hash=('a'*64);policy_hash=('b'*64);baseline=('c'*40);request=[pscustomobject]@{mode='analysis_only'}}
$deps=Get-BFDependencies $state 'inspect' $null
Assert-B ($deps.Contains('architecture')) 'Stage dependencies omitted the architecture bundle hash.'
Assert-B ($deps.architecture -ceq (Get-BFArchitectureBundleHash 'inspect')) 'Dependency bundle hash differs from the built bundle.'

# 3. Applicable ADR change invalidates the bundle; inapplicable ADR change does not.
$tmp=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-arch-bundle-'+[guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory((Join-Path $tmp 'docs/architecture'))
Copy-Item -LiteralPath (Join-Path $root 'docs/ARCHITECTURE_RU.md') -Destination (Join-Path $tmp 'docs/ARCHITECTURE_RU.md')
Copy-Item -LiteralPath (Join-Path $root 'docs/architecture/adr-index.json') -Destination (Join-Path $tmp 'docs/architecture/adr-index.json')
Copy-Item -LiteralPath (Join-Path $root 'docs/architecture/adr-index.schema.json') -Destination (Join-Path $tmp 'docs/architecture/adr-index.schema.json')
try {
    $baselineHash=Get-BFArchitectureBundleHash 'code_review' $tmp
    $sourcePath=Join-Path $tmp 'docs/ARCHITECTURE_RU.md'
    $text=[IO.File]::ReadAllText($sourcePath)
    $inapplicable=$text.Replace('Publication получает собственный UUID','Publication получает собственный UUID (drift)')
    Assert-B ($inapplicable -cne $text) 'Inapplicable ADR fixture did not change the source.'
    [IO.File]::WriteAllText($sourcePath,$inapplicable,$utf8)
    Assert-B ((Get-BFArchitectureBundleHash 'code_review' $tmp) -ceq $baselineHash) 'Inapplicable ADR change altered the code_review bundle.'
    $applicable=$inapplicable.Replace('reviewer читает полный актуальный diff','reviewer читает полный актуальный diff (drift)')
    Assert-B ($applicable -cne $inapplicable) 'Applicable ADR fixture did not change the source.'
    [IO.File]::WriteAllText($sourcePath,$applicable,$utf8)
    $excerptHash=Get-BFArchitectureBundleHash 'code_review' $tmp
    Assert-B ($excerptHash -cne $baselineHash) 'Applicable excerpt change did not invalidate the bundle.'
    # Defect 1: a normative change outside the short excerpt must still change
    # the bundle identity.
    $outside=$applicable.Replace('Появляется дополнительный model call','Появляется дополнительный model call (drift)')
    Assert-B ($outside -cne $applicable) 'Outside-excerpt fixture did not change the source.'
    [IO.File]::WriteAllText($sourcePath,$outside,$utf8)
    Assert-B ((Get-BFArchitectureBundleHash 'code_review' $tmp) -cne $excerptHash) 'Change outside the excerpt did not invalidate the bundle.'

    # Defect 1 regression: an ADR omitted from the presentation by the size cap
    # must still participate in the identity.
    $savedMax=$script:BFArchitectureBundleMaxChars
    try {
        $script:BFArchitectureBundleMaxChars=200
        $capped=Get-BFArchitectureBundle 'code_review' $tmp
        Assert-B (@($capped.decisions).Count -lt 5 -and @($capped.excluded).Count -ge 1) 'Size cap did not exclude decisions in the fixture.'
        $cappedHash=Get-BFArchitectureBundleHash 'code_review' $tmp
        $excludedId=[string]$capped.excluded[0]
        $pattern='(?ms)(^##[ \t]+'+[regex]::Escape($excludedId)+':.*?)(?=^##[ \t]|\z)'
        $drifted=[regex]::Replace($outside,$pattern,{param($m) $m.Groups[1].Value.TrimEnd()+" excluded-identity-drift.`n"})
        Assert-B ($drifted -cne $outside) 'Excluded-ADR fixture did not change the source.'
        [IO.File]::WriteAllText($sourcePath,$drifted,$utf8)
        Assert-B ((Get-BFArchitectureBundleHash 'code_review' $tmp) -cne $cappedHash) 'Excluded ADR was not part of the bundle identity.'
    } finally { $script:BFArchitectureBundleMaxChars=$savedMax }

    # Damaged link must fail closed; missing index must degrade to missing_context.
    $indexPath=Join-Path $tmp 'docs/architecture/adr-index.json'
    $indexText=[IO.File]::ReadAllText($indexPath).Replace('adr-4-review-только-критикует-reconciler-принимает-решения','adr-4-broken-anchor')
    [IO.File]::WriteAllText($indexPath,$indexText,$utf8)
    Assert-B ((Failure-B {Get-BFArchitectureBundle 'code_review' $tmp}) -match 'anchor|BF_INVALID') 'Damaged index did not fail closed.'
}
finally { Remove-Item -LiteralPath $tmp -Recurse -Force }

$emptyRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-arch-empty-'+[guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($emptyRoot)
try {
    $missing=Get-BFArchitectureBundle 'code_review' $emptyRoot
    Assert-B ($missing.missing_context -contains 'adr-index' -and @($missing.decisions).Count -eq 0) 'Missing index did not degrade to missing_context.'
    Assert-B ($missing.bundle_sha256 -match '^[0-9a-f]{64}$') 'Missing-index bundle lost its deterministic digest.'
}
finally { Remove-Item -LiteralPath $emptyRoot -Recurse -Force }

# 3b. A valid index may omit a stage subject; it is missing_context, not a crash.
$subjectRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-arch-subject-'+[guid]::NewGuid().ToString('N'))
try {
    [void][IO.Directory]::CreateDirectory((Join-Path $subjectRoot 'docs/architecture'))
    [IO.File]::WriteAllText((Join-Path $subjectRoot 'docs/ARCHITECTURE_RU.md'),"## ADR-1: only state`n`n**Решение.** State only.`n",$utf8)
    $minimal=[ordered]@{schema_version=1;subjects=@([ordered]@{id='controller.state';kind='module';refs=@('global/skills/1c-task/scripts/Task.Storage.ps1')});decisions=@([ordered]@{id='ADR-1';title='only state';status='accepted';applies_to=@('controller.state');source=[ordered]@{path='docs/ARCHITECTURE_RU.md';anchor='adr-1-only-state'};informed_by=@();supersedes=@();revisit_triggers=@()})}
    [IO.File]::WriteAllText((Join-Path $subjectRoot 'docs/architecture/adr-index.json'),($minimal|ConvertTo-Json -Depth 12),$utf8)
    Copy-Item -LiteralPath (Join-Path $root 'docs/architecture/adr-index.schema.json') -Destination (Join-Path $subjectRoot 'docs/architecture/adr-index.schema.json')
    $noSubject=Get-BFArchitectureBundle 'spec' $subjectRoot
    Assert-B (@($noSubject.decisions).Count -eq 0 -and ($noSubject.missing_context -contains 'subject:controller.gates')) 'Missing stage subject crashed the bundle or was hidden.'
}
finally { Remove-Item -LiteralPath $subjectRoot -Recurse -Force }

# 3c. A large valid index cannot bypass the decision/char limits.
$manyRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-arch-many-'+[guid]::NewGuid().ToString('N'))
try {
    [void][IO.Directory]::CreateDirectory((Join-Path $manyRoot 'docs/architecture'))
    $count=80;$source=[Text.StringBuilder]::new();$manyDecisions=@()
    for($i=1;$i -le $count;$i++){
        [void]$source.AppendLine("## ADR-${i}: decision ${i}").AppendLine().AppendLine("**Решение.** Body $i.").AppendLine()
        $manyDecisions+=[ordered]@{id="ADR-$i";title="decision $i";status='accepted';applies_to=@('controller.gates');source=[ordered]@{path='docs/ARCHITECTURE_RU.md';anchor=(Get-BFArchitectureAnchor "ADR-${i}: decision ${i}")};informed_by=@();supersedes=@();revisit_triggers=@()}
    }
    [IO.File]::WriteAllText((Join-Path $manyRoot 'docs/ARCHITECTURE_RU.md'),$source.ToString(),$utf8)
    $manyIndex=[ordered]@{schema_version=1;subjects=@([ordered]@{id='controller.gates';kind='module';refs=@('global/skills/1c-task/scripts/Task.Gates.ps1')});decisions=$manyDecisions}
    [IO.File]::WriteAllText((Join-Path $manyRoot 'docs/architecture/adr-index.json'),($manyIndex|ConvertTo-Json -Depth 12),$utf8)
    Copy-Item -LiteralPath (Join-Path $root 'docs/architecture/adr-index.schema.json') -Destination (Join-Path $manyRoot 'docs/architecture/adr-index.schema.json')
    $many=Get-BFArchitectureBundle 'spec' $manyRoot
    Assert-B (@($many.decisions).Count -le $script:BFArchitectureBundleMaxDecisions) 'Hard decision limit was bypassed.'
    Assert-B ((Format-BFArchitectureBundlePrompt $many).Length -le $script:BFArchitectureBundleMaxChars) 'Large index bypassed the rendered prompt limit.'
    Assert-B ($many.excluded_count -ge ($count-$script:BFArchitectureBundleMaxDecisions)) 'Excluded count was not reported for a large index.'
    Assert-B (@($many.excluded).Count -le $script:BFArchitectureBundleMaxExcluded) 'Excluded sample exceeded its own bound.'
}
finally { Remove-Item -LiteralPath $manyRoot -Recurse -Force }

# 4. Prompt regression: the bundle is instructional context, never authority.
$project=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-arch-prompt-'+[guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($project)
try {
    $promptState=[pscustomobject]@{
        task_id=([guid]::NewGuid().ToString());project_path=$project;baseline=('c'*40)
        classification=[pscustomobject]@{complexity='S';risk='low';impact_flags=@();rationale='fixture'}
        request=[pscustomobject]@{prompt='Fixture request.';criteria=@()}
    }
    $prompt=Get-BFStagePrompt $promptState 'code_review' ''
    Assert-B ($prompt.Contains('Architecture context')) 'Stage prompt omitted the architecture section.'
    Assert-B ($prompt.Contains('not user authorization, acceptance, runtime evidence, or a transition authority')) 'Stage prompt omitted the bundle boundary.'
    Assert-B ($prompt.Contains('ADR-4')) 'Stage prompt omitted an applicable ADR.'
    Assert-B (-not $prompt.Contains('ADR-7')) 'Stage prompt included an inapplicable ADR.'
    Assert-B ($prompt -notmatch '(?i)architecture (authorizes|grants|accepts)') 'Stage prompt treated the bundle as authority.'
}
finally { Remove-Item -LiteralPath $project -Recurse -Force }

Write-Output "TASK_ARCHITECTURE_BUNDLE_OK checks=$script:checks; model/runtime/database=0"
