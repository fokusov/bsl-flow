#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$graphScript = Join-Path $PackageRoot 'global\skills\1c-implement\scripts\ExecutionGraph.ps1'
if (-not (Test-Path -LiteralPath $graphScript -PathType Leaf)) { throw "Missing execution graph helpers: $graphScript" }
. $graphScript

$script:checks = 0
function Assert-T { param([bool]$Condition, [string]$Message) if (-not $Condition) { throw "ASSERTION FAILED: $Message" }; $script:checks++ }
function Expect-Throw {
    param([scriptblock]$Action, [string]$Pattern, [string]$Message)
    $observed = $null
    try { & $Action | Out-Null } catch { $observed = $_.Exception.Message }
    Assert-T ($null -ne $observed -and $observed -match $Pattern) "$Message. Observed: $observed"
}
function New-FixtureDirectory { param([string]$Root, [string]$Name) $path = Join-Path $Root $Name; New-Item -ItemType Directory -Path $path -Force | Out-Null; return $path }
function Write-FixtureText { param([string]$Path, [string]$Text) [IO.File]::WriteAllText($Path, $Text, [Text.UTF8Encoding]::new($false)) }

$fixtureSpec = @'
# fixture-change

## Требуемое поведение

1. Первое требование.
2. Второе требование.
'@

$contractYaml = @'
schema_version: 1
requirements:
  - id: R-001
    spec_ref: 1
  - id: R-002
    spec_ref: 2
'@

$verificationYaml = @'
schema_version: 1
checks:
  - id: V-001
    requirement: R-002
    type: scenario
    expect:
      result: "feature behaves as specified"
'@

$executionYaml = @'
schema_version: 1
tasks:
  - id: T-002
    kind: implement
    goal: "Implement the required behavior"
    depends_on: [T-001]
    satisfies: [R-002]
    verify: [V-001]
    allowed_scope: ["src/**"]
    forbidden: ["src/secret.md"]
    mutation: allowed
  - id: T-001
    kind: explore
    goal: "Explore the affected area"
    depends_on: []
    satisfies: [R-001]
    verify: []
    allowed_scope: []
    forbidden: []
    mutation: forbidden
  - id: T-003
    kind: test
    goal: "Add the regression test"
    depends_on: [T-002]
    satisfies: []
    verify: []
    allowed_scope: ["tests/**"]
    forbidden: []
    mutation: allowed
'@

function New-GraphFixture {
    param([string]$Root, [string]$Name)
    $dir = New-FixtureDirectory $Root $Name
    Write-FixtureText (Join-Path $dir 'spec.md') $fixtureSpec
    Write-FixtureText (Join-Path $dir 'contract.yaml') $contractYaml
    Write-FixtureText (Join-Path $dir 'execution.yaml') $executionYaml
    Write-FixtureText (Join-Path $dir 'verification.yaml') $verificationYaml
    return $dir
}
function Get-ModelTask { param($Model, [string]$TaskId) return @(@($Model.Tasks) | Where-Object { "$($_.Id)" -ceq $TaskId })[0] }

$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-execution-graph-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $testRoot -Force | Out-Null
try {
    # Artifact reading goes through the authoritative lint; the valid fixture passes.
    $fixture = New-GraphFixture $testRoot 'main'
    $model = Get-ExecutionGraphArtifacts -ChangePath $fixture
    Assert-T $model.Passed "Valid fixture must pass the lint: $(@($model.Errors) -join ' | ')"
    Assert-T (@($model.Tasks).Count -eq 3 -and @($model.Requirements).Count -eq 2 -and @($model.Checks).Count -eq 1) 'Model inventory is wrong.'
    $taskExplore = Get-ModelTask $model 'T-001'
    $taskImplement = Get-ModelTask $model 'T-002'
    $taskTest = Get-ModelTask $model 'T-003'
    Assert-T ("$($taskExplore.Kind)" -eq 'explore' -and "$($taskImplement.Kind)" -eq 'implement') 'Model task kinds are wrong.'

    # Deterministic topological order: sequential, ties broken by task id.
    $order = Get-ExecutionGraphOrder -Tasks @($model.Tasks)
    Assert-T ((@($order) -join ',') -ceq 'T-001,T-002,T-003') "Topological order must be deterministic (got $(@($order) -join ','))."

    # Cycle detection reports the cycle path.
    $cyclic = New-GraphFixture $testRoot 'cycle'
    Write-FixtureText (Join-Path $cyclic 'execution.yaml') ($executionYaml -replace 'depends_on: \[T-001\]', 'depends_on: [T-003]')
    $cyclicModel = Get-ExecutionGraphArtifacts -ChangePath $cyclic
    Expect-Throw { Get-ExecutionGraphOrder -Tasks @($cyclicModel.Tasks) } 'cycle detected: T-002 -> T-003 -> T-002' 'Cycle path must be reported.'

    # kind explore is read-only: zero touched files are recorded and accepted.
    $exploreEvidence = Write-ExecutionGraphEvidence -ChangePath $fixture -Task $taskExplore -Status done -Observations @('area inspected') -TouchedFiles @()
    Assert-T ($exploreEvidence -like '*evidence\T-001.json') "Evidence path is wrong: $exploreEvidence"
    $storedEvidence = Get-Content -LiteralPath $exploreEvidence -Raw | ConvertFrom-Json
    Assert-T ("$($storedEvidence.id)" -ceq 'T-001' -and "$($storedEvidence.status)" -ceq 'done') 'Explore evidence must record id and done status.'
    Assert-T (@($storedEvidence.touched_files).Count -eq 0) 'Explore evidence must record zero touched files.'
    Assert-T ("$($storedEvidence.schema_version)" -ceq '1') 'Evidence schema_version must be 1.'
    Expect-Throw { Write-ExecutionGraphEvidence -ChangePath $fixture -Task $taskExplore -Status done -TouchedFiles @('src/a.bsl') } 'mutation is forbidden for task T-001' 'Explore task must not record touched files as done.'

    # A task with verify[] cannot be done without recorded observable results.
    $reason = Get-ExecutionGraphBlockedReason -Task $taskImplement -TouchedFiles @() -VerifyResults @()
    Assert-T ($null -ne $reason -and $reason -match "no recorded observable result for verify reference 'V-001'") "Missing V evidence must produce the BLOCKED reason (got: $reason)."
    Expect-Throw { Write-ExecutionGraphEvidence -ChangePath $fixture -Task $taskImplement -Status done -Observations @('implemented') -TouchedFiles @('src/feature.bsl') } "verify reference 'V-001'" 'done must be refused without V evidence.'

    # With the V evidence recorded the same task is done.
    $doneEvidence = Write-ExecutionGraphEvidence -ChangePath $fixture -Task $taskImplement -Status done -Observations @('implemented') -TouchedFiles @('src/feature.bsl') -VerifyResults @([ordered]@{ id = 'V-001'; result = 'scenario passed: feature behaves as specified' })
    $doneStored = Get-Content -LiteralPath $doneEvidence -Raw | ConvertFrom-Json
    Assert-T ("$($doneStored.status)" -ceq 'done' -and @($doneStored.verify).Count -eq 1 -and "$($doneStored.verify[0].result)" -like 'scenario passed*') 'Done evidence must record the V result.'

    # Scope violations are recorded in evidence; the task is not done.
    $violations = Get-ExecutionGraphScopeViolations -Task $taskImplement -TouchedFiles @('docs/readme.md')
    Assert-T (@($violations).Count -eq 1 -and @($violations)[0] -like "*'docs/readme.md' is outside allowed_scope of task T-002*") "Outside-scope touch must be a violation (got: $(@($violations) -join '; '))."
    Expect-Throw { Write-ExecutionGraphEvidence -ChangePath $fixture -Task $taskImplement -Status done -TouchedFiles @('docs/readme.md') -VerifyResults @([ordered]@{ id = 'V-001'; result = 'ok' }) } "outside allowed_scope of task T-002" 'done must be refused on scope violation.'
    $violationReason = Get-ExecutionGraphBlockedReason -Task $taskImplement -TouchedFiles @('docs/readme.md') -VerifyResults @([ordered]@{ id = 'V-001'; result = 'ok' })
    Assert-T ($null -ne $violationReason -and $violationReason -match 'outside allowed_scope') 'BLOCKED reason must name the scope violation.'

    # forbidden globs win over allowed globs; glob semantics stay frozen.
    Assert-T (Test-ExecutionGraphPathAllowed -Path 'src/a/b.bsl' -AllowedScope @('src/**') -Forbidden @('src/secret.md')) 'src/** must match nested files.'
    Assert-T (-not (Test-ExecutionGraphPathAllowed -Path 'src/secret.md' -AllowedScope @('src/**') -Forbidden @('src/secret.md'))) 'forbidden glob must win over allowed glob.'
    Assert-T (-not (Test-ExecutionGraphPathAllowed -Path 'src/a/b.bsl' -AllowedScope @('src/*') -Forbidden @())) 'Single star must not cross directory separators.'
    Assert-T (-not (Test-ExecutionGraphPathAllowed -Path 'docs/x.md' -AllowedScope @() -Forbidden @())) 'Empty allowed_scope must allow nothing.'
    Assert-T (-not (Test-ExecutionGraphPathAllowed -Path '..\escape.bsl' -AllowedScope @('**') -Forbidden @())) 'Parent paths are never writable.'

    # blocked evidence carries the reason and the recorded violations.
    $blockedEvidence = Write-ExecutionGraphEvidence -ChangePath $fixture -Task $taskImplement -Status blocked -Reason $violationReason -Violations @($violations)
    $blockedStored = Get-Content -LiteralPath $blockedEvidence -Raw | ConvertFrom-Json
    Assert-T ("$($blockedStored.status)" -ceq 'blocked' -and "$($blockedStored.reason)" -match 'outside allowed_scope') 'Blocked evidence must carry the reason.'
    Assert-T (@($blockedStored.violations).Count -eq 1) 'Blocked evidence must record the violation.'
    Expect-Throw { Write-ExecutionGraphEvidence -ChangePath $fixture -Task $taskTest -Status blocked } 'requires a non-empty reason' 'blocked without a reason must be refused.'

    # The state projection is generated from evidence and rebuilds identically.
    $null = Write-ExecutionGraphEvidence -ChangePath $fixture -Task $taskTest -Status running -Observations @('test written')
    $statePath = Write-ExecutionGraphState -ChangePath $fixture
    Assert-T ($statePath -like '*state.json') "State path is wrong: $statePath"
    $stateBefore = [Convert]::ToBase64String([IO.File]::ReadAllBytes($statePath))
    $state = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
    Assert-T ("$($state.schema_version)" -ceq '1' -and "$($state.source)" -ceq 'evidence') 'State projection must be a generated schema_version 1 document.'
    Assert-T ("$($state.tasks.'T-001'.status)" -ceq 'done') 'T-001 must project done.'
    Assert-T ("$($state.tasks.'T-002'.status)" -ceq 'blocked' -and "$($state.tasks.'T-002'.reason)" -match 'outside allowed_scope') 'T-002 must project blocked with the reason.'
    Assert-T ("$($state.tasks.'T-003'.status)" -ceq 'running') 'T-003 must project running.'
    Remove-Item -LiteralPath $statePath -Force
    Assert-T (-not (Test-Path -LiteralPath $statePath)) 'state.json must be deletable without affecting evidence.'
    $rebuiltPath = Write-ExecutionGraphState -ChangePath $fixture
    $stateAfter = [Convert]::ToBase64String([IO.File]::ReadAllBytes($rebuiltPath))
    Assert-T ($stateBefore -ceq $stateAfter) 'Rebuilt state.json must be byte-identical (deterministic projection).'
    $rebuilt = Get-ExecutionGraphState -ChangePath $fixture
    Assert-T (@($rebuilt.tasks.Keys).Count -eq 3) 'Rebuilt projection must cover every task.'

    # Evidence for an unknown task fails the projection (fail-closed).
    Write-FixtureText (Join-Path $fixture 'evidence\T-099.json') '{"schema_version":1,"id":"T-099","status":"done"}'
    Expect-Throw { Get-ExecutionGraphState -ChangePath $fixture } 'T-099 which is not defined in execution.yaml' 'Orphan evidence must fail the projection.'
    Remove-Item -LiteralPath (Join-Path $fixture 'evidence\T-099.json') -Force

    # Invalid artifacts refuse to project state.
    Expect-Throw { Get-ExecutionGraphState -ChangePath $cyclic } 'artifacts are invalid' 'Invalid artifacts must refuse state projection.'

    # Missing artifacts: existing change dirs keep working (no graph, no state).
    $bare = New-FixtureDirectory $testRoot 'bare'
    Write-FixtureText (Join-Path $bare 'spec.md') $fixtureSpec
    $bareModel = Get-ExecutionGraphArtifacts -ChangePath $bare
    Assert-T ($bareModel.Passed -and @($bareModel.Tasks).Count -eq 0) 'Change without artifacts must stay valid.'
    Assert-T (@(Get-ExecutionGraphOrder -Tasks @($bareModel.Tasks)).Count -eq 0) 'Empty graph must order to nothing.'
    Expect-Throw { Get-ExecutionGraphState -ChangePath $bare } 'state projection requires execution.yaml' 'State projection must require execution.yaml.'

    Write-Host "Execution graph discipline tests passed: $script:checks checks; model=0; network=0; DB=0."
}
finally {
    if (Test-Path -LiteralPath $testRoot) { Remove-Item -LiteralPath $testRoot -Recurse -Force }
}
