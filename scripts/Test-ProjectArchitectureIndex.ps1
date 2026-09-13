#Requires -Version 7.0
# Stage E contract: a project may opt in with an optional docs/architecture
# ADR index. Bootstrap never creates it, never treats it as managed, and the
# existing missing_context fallback is unchanged. No model/runtime/database.
# Without the OpenSpec CLI the dynamic bootstrap part is skipped explicitly;
# the architecture contract and the static bootstrap boundary still run.
[CmdletBinding()]param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$root=[IO.Path]::GetFullPath($PackageRoot).TrimEnd('\','/')
$core=Join-Path $root 'global/skills/1c-task/scripts'
foreach($module in @('Task.Storage.ps1','Task.Contracts.ps1','Task.Architecture.ps1','Task.Gates.ps1')){. (Join-Path $core $module)}
$bootstrap=Join-Path $root 'global/skills/1c-init-project/scripts/Initialize-BSLFlowProject.ps1'
$upgrade=Join-Path $root 'global/skills/1c-init-project/scripts/Update-BSLFlowProject.ps1'
$schemaPath=Join-Path $root 'global/skills/1c-task/schemas/context.schema.json'
$script:checks=0
function Assert-E([bool]$Condition,[string]$Message){if(-not $Condition){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Failure-E([scriptblock]$Action){try{& $Action|Out-Null;return ''}catch{return $_.Exception.Message}}
function Get-TreeFingerprint([string]$Path){
    if(-not (Test-Path -LiteralPath $Path)){return ''}
    $normalized=[IO.Path]::GetFullPath($Path).TrimEnd('\','/')
    $items=Get-ChildItem -LiteralPath $normalized -File -Recurse -Force | Sort-Object FullName | ForEach-Object { [ordered]@{path=$_.FullName.Substring($normalized.Length+1);sha256=(Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash} }
    return Get-BFHash $items
}
$openspecAvailable=[bool](Get-Command openspec -ErrorAction SilentlyContinue)
$testRoot=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-project-index-'+[guid]::NewGuid().ToString('N'))
$project=Join-Path $testRoot 'project'
$utf8=[Text.UTF8Encoding]::new($false)
try {
    [void][IO.Directory]::CreateDirectory($project)
    if($openspecAvailable){
        & $bootstrap -ProjectPath $project -Explicit1CProject | Out-Null
        Assert-E (-not (Test-Path -LiteralPath (Join-Path $project 'docs/architecture'))) 'Bootstrap auto-created a project architecture index.'
        Assert-E (-not (Test-Path -LiteralPath (Join-Path $project 'docs'))) 'Bootstrap unexpectedly created docs/.'
    } else {
        # Static boundary: neither bootstrap script may manage project architecture.
        foreach($scriptPath in @($bootstrap,$upgrade)){
            Assert-E (Test-Path -LiteralPath $scriptPath -PathType Leaf) ("Bootstrap script is missing: {0}" -f $scriptPath)
            $text=[IO.File]::ReadAllText($scriptPath)
            Assert-E (-not ($text -match 'docs[\\/]architecture')) ("Bootstrap script manages project architecture: {0}" -f $scriptPath)
        }
    }

    # Fallback is unchanged: no project index -> missing_context, no failure.
    $bundle=Get-BFArchitectureBundle 'code_review' $project
    Assert-E ($bundle.missing_context -contains 'adr-index' -and @($bundle.decisions).Count -eq 0) 'Missing project index did not fall back to missing_context.'
    $state=[pscustomobject]@{task_id=([guid]::NewGuid().ToString());revision=3;status='ready';stage='inspect';project_path=$project;intent_hash=('a'*64);policy_hash=('b'*64);baseline=('c'*40);classification=[pscustomobject]@{complexity='S';risk='low';impact_flags=@();rationale='fixture'};active_attempt=$null;unresolved_effect=$null;question=$null;evidence=@();request=[pscustomobject]@{mode='analysis_only'}}
    $context=Get-BFTaskContext $state $project ([ordered]@{action='dispatch';stage='code_review';blockers=@()}) -FallbackRoot $project
    Assert-E ($context.missing_context -contains 'adr-index' -and $context.generated_from.adr_index_sha256 -ceq 'missing' -and $context.generated_from.adr_scope -ceq 'missing') 'Context hid the missing project index.'
    Assert-E ((Get-BFArchitectureContextRoot $project) -ne (Assert-BFSafePath $project)) 'Absent project index did not fall back to the default root.'
    $depsWithout=Get-BFDependencies $state 'inspect' $null
    Assert-E ($depsWithout.architecture -match '^[0-9a-f]{64}$') 'Dependency fallback did not produce a deterministic digest.'

    # A valid project index is validated and becomes the bundle/context source.
    [void][IO.Directory]::CreateDirectory((Join-Path $project 'docs/architecture'))
    Copy-Item -LiteralPath (Join-Path $root 'docs/ARCHITECTURE_RU.md') -Destination (Join-Path $project 'docs/ARCHITECTURE_RU.md')
    Copy-Item -LiteralPath (Join-Path $root 'docs/architecture/adr-index.json') -Destination (Join-Path $project 'docs/architecture/adr-index.json')
    Copy-Item -LiteralPath (Join-Path $root 'docs/architecture/adr-index.schema.json') -Destination (Join-Path $project 'docs/architecture/adr-index.schema.json')
    Assert-E ($null -ne (Assert-BFADRIndex (Read-BFArchitectureIndex $project) $project)) 'Valid project index was rejected.'
    Assert-E ((Get-BFArchitectureContextRoot $project) -eq (Assert-BFSafePath $project)) 'Present project index was not selected as the context root.'
    $projectBundle=Get-BFArchitectureBundle 'code_review' $project
    Assert-E (@($projectBundle.decisions).Count -gt 0 -and $projectBundle.missing_context.Count -eq 0) 'Project index did not feed the stage bundle.'
    $deps=Get-BFDependencies $state 'inspect' $null
    Assert-E ($deps.architecture -ceq (Get-BFArchitectureBundleHash 'inspect' $project)) 'Dependencies did not bind the project bundle.'
    $context2=Get-BFTaskContext $state $project ([ordered]@{action='dispatch';stage='code_review';blockers=@()})
    Assert-E ($context2.generated_from.adr_index_sha256 -match '^[0-9a-f]{64}$' -and $context2.generated_from.adr_scope -ceq 'project') 'Context did not resolve the project index.'
    try { if(-not (Test-Json -Json (Get-BFCanonicalJson $context2) -SchemaFile $schemaPath -ErrorAction Stop)){throw 'schema mismatch'} }
    catch { throw ("ASSERTION FAILED: project context schema: {0}" -f $_.Exception.Message) }
    $script:checks++

    # The project file is optional, not managed: a repeat bootstrap must
    # preserve it byte-for-byte and must not create anything else under docs/.
    if($openspecAvailable){
        $docsBefore=Get-TreeFingerprint (Join-Path $project 'docs')
        & $bootstrap -ProjectPath $project -Explicit1CProject | Out-Null
        Assert-E ((Get-TreeFingerprint (Join-Path $project 'docs')) -ceq $docsBefore) 'Repeat bootstrap changed the project architecture index.'
    }

    # A damaged project index fails closed instead of silently falling back.
    $indexPath=Join-Path $project 'docs/architecture/adr-index.json'
    $indexText=[IO.File]::ReadAllText($indexPath).Replace('adr-4-review-только-критикует-reconciler-принимает-решения','adr-4-broken-anchor')
    [IO.File]::WriteAllText($indexPath,$indexText,$utf8)
    Assert-E ((Failure-E {Get-BFArchitectureBundle 'code_review' $project}) -match 'BF_INVALID|anchor') 'Damaged project index did not fail the bundle closed.'
    Assert-E ((Failure-E {Get-BFDependencies $state 'inspect' $null}) -match 'BF_INVALID|anchor') 'Damaged project index did not block dependencies.'
    Assert-E ((Failure-E {Get-BFTaskContext $state $project ([ordered]@{action='dispatch';stage='code_review';blockers=@()})}) -match 'BF_INVALID|anchor') 'Damaged project index was masked by context.'
    Assert-E (Test-Path -LiteralPath $indexPath) 'A damaged project index was deleted instead of reported.'
} finally {
    if(Test-Path -LiteralPath $testRoot){Remove-Item -LiteralPath $testRoot -Recurse -Force}
}
$label=if($openspecAvailable){'PROJECT_ARCHITECTURE_INDEX_OK'}else{'PROJECT_ARCHITECTURE_INDEX_PARTIAL'}
$bootstrapState=if($openspecAvailable){'ran'}else{'skipped_no_openspec'}
Write-Output "$label checks=$script:checks; bootstrap=$bootstrapState; model/runtime/database=0"
