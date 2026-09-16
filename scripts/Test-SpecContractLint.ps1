#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$lintScript = Join-Path $PackageRoot 'global\skills\1c-spec-review\scripts\Invoke-1CSpecContractLint.ps1'
if (-not (Test-Path -LiteralPath $lintScript -PathType Leaf)) { throw "Missing lint script: $lintScript" }

$script:checks = 0
function Assert-T { param([bool]$Condition, [string]$Message) if (-not $Condition) { throw "ASSERTION FAILED: $Message" }; $script:checks++ }
function Expect-LintFailure {
    # Runs the lint with -NoThrow and asserts that it fails naming the pattern.
    param([string]$ChangePath, [string]$OutputPath, [string]$Pattern, [string]$Message)
    $result = & $lintScript -ChangePath $ChangePath -NoThrow -OutputPath $OutputPath
    Assert-T (-not $result.passed) "$Message (lint unexpectedly passed)."
    $joined = @($result.errors) -join ' | '
    Assert-T ($joined -match $Pattern) "$Message (errors did not name the expected finding). Errors: $joined"
    Assert-T (@($result.errors).Count -gt 0) "$Message (no errors recorded)."
}
function New-FixtureDirectory { param([string]$Root, [string]$Name) $path = Join-Path $Root $Name; New-Item -ItemType Directory -Path $path -Force | Out-Null; return $path }
function Write-FixtureText { param([string]$Path, [string]$Text) [IO.File]::WriteAllText($Path, $Text, [Text.UTF8Encoding]::new($false)) }

$fixtureSpec = @'
# fixture-change

## Классификация

- Сложность: M
- Риск: low

## Цель

Fixture change for the contract lint.

## Требуемое поведение

1. Первое требование.
2. Второе требование.
3. Третье требование.

## Не делать

- Ничего.

## Критерии приёмки

- GIVEN fixture WHEN lint runs THEN pass.

## Требуемые проверки

- [x] Static — allowlist полей проверяется линтом.

## Неопределённости / допущения

- Нет.
'@

$validContract = @'
schema_version: 1
requirements:
  - id: R-001
    spec_ref: 1
    constraints:
      configuration_changes: forbidden
  - id: R-002
    spec_ref: 2
'@

$validVerification = @'
schema_version: 1
checks:
  - id: V-001
    requirement: R-002
    type: scenario
    expect:
      result: "feature behaves as specified"
'@

$validExecution = @'
schema_version: 1
tasks:
  - id: T-001
    kind: explore
    goal: "Explore the affected area"
    depends_on: []
    satisfies: [R-001]
    verify: []
    allowed_scope: []
    forbidden: []
    mutation: forbidden
  - id: T-002
    kind: implement
    goal: "Implement the required behavior"
    depends_on: [T-001]
    satisfies: [R-002]
    verify: [V-001]
    allowed_scope: ["src/**"]
    forbidden: ["docs/**"]
    mutation: allowed
'@

function New-ValidFixture {
    param([string]$Root, [string]$Name)
    $dir = New-FixtureDirectory $Root $Name
    Write-FixtureText (Join-Path $dir 'spec.md') $fixtureSpec
    Write-FixtureText (Join-Path $dir 'contract.yaml') $validContract
    Write-FixtureText (Join-Path $dir 'execution.yaml') $validExecution
    Write-FixtureText (Join-Path $dir 'verification.yaml') $validVerification
    return $dir
}

$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-spec-contract-lint-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $testRoot -Force | Out-Null
try {
    $outPath = Join-Path $testRoot 'lint-result.json'

    # Valid triple passes end-to-end with resolved references and inventory.
    $valid = New-ValidFixture $testRoot 'valid'
    $result = & $lintScript -ChangePath $valid -NoThrow -OutputPath $outPath
    Assert-T $result.passed "Valid triple was rejected: $(@($result.errors) -join ' | ')"
    Assert-T (@($result.errors).Count -eq 0) 'Valid triple recorded errors.'
    Assert-T ($result.artifacts.contract -and $result.artifacts.execution -and $result.artifacts.verification) 'Artifact inventory is wrong.'
    Assert-T ($result.stats.requirements -eq 2 -and $result.stats.tasks -eq 2 -and $result.stats.checks -eq 1) 'Artifact counts are wrong.'
    Assert-T ($result.summary -match 'contract: 2 requirements') "Summary does not describe the inventory: $($result.summary)"
    Assert-T (Test-Path -LiteralPath $outPath -PathType Leaf) 'Result JSON was not written.'
    $stored = Get-Content -LiteralPath $outPath -Raw | ConvertFrom-Json
    Assert-T ($stored.passed -eq $true) 'Stored result JSON lost the passed flag.'

    # Cycle T2 -> T3 -> T2 fails naming the tasks and the cycle path.
    $t003Block = @'
  - id: T-003
    kind: test
    goal: "Cycle probe"
    depends_on: [T-002]
    satisfies: []
    verify: []
    allowed_scope: []
    forbidden: []
    mutation: forbidden
'@
    $cycle = New-ValidFixture $testRoot 'cycle'
    Write-FixtureText (Join-Path $cycle 'execution.yaml') (($validExecution -replace 'depends_on: \[T-001\]', 'depends_on: [T-003]') + "`n" + $t003Block)
    Expect-LintFailure $cycle $outPath 'execution\.yaml.*cycle.*T-002 -> T-003 -> T-002' 'Cycle must be reported with the task path.'

    # Dangling satisfies fails naming the task and the broken reference.
    $dangling = New-ValidFixture $testRoot 'dangling'
    Write-FixtureText (Join-Path $dangling 'execution.yaml') ($validExecution -replace 'satisfies: \[R-002\]', 'satisfies: [R-999]')
    Expect-LintFailure $dangling $outPath 'execution\.yaml.*task T-002.*dangling satisfies reference ''R-999''' 'Dangling satisfies must name task and reference.'

    # Scope globs: '..' segment, drive letter and leading slash are rejected.
    $escape = New-ValidFixture $testRoot 'scope-escape'
    Write-FixtureText (Join-Path $escape 'execution.yaml') ($validExecution -replace 'allowed_scope: \["src/\*\*"\]', 'allowed_scope: ["src/../escape"]')
    Expect-LintFailure $escape $outPath "task T-002.*invalid allowed_scope glob 'src/\.\./escape'" "'..' segment must be rejected."
    $absolute = New-ValidFixture $testRoot 'scope-absolute'
    Write-FixtureText (Join-Path $absolute 'execution.yaml') ($validExecution -replace 'allowed_scope: \["src/\*\*"\]', "allowed_scope: ['C:/src']")
    Expect-LintFailure $absolute $outPath "invalid allowed_scope glob 'C:/src'" 'Drive letter must be rejected.'
    $rooted = New-ValidFixture $testRoot 'scope-rooted'
    Write-FixtureText (Join-Path $rooted 'execution.yaml') ($validExecution -replace 'allowed_scope: \["src/\*\*"\]', 'allowed_scope: ["/src"]')
    Expect-LintFailure $rooted $outPath "invalid allowed_scope glob '/src'" 'Leading slash must be rejected.'

    # Unknown fields and unknown kinds are rejected (allowlist, fail-closed).
    $unknownField = New-ValidFixture $testRoot 'unknown-field'
    Write-FixtureText (Join-Path $unknownField 'execution.yaml') ($validExecution -replace 'kind: implement', "kind: implement`n    priority: high")
    Expect-LintFailure $unknownField $outPath "execution\.yaml.*task T-002: unknown field 'priority'" 'Unknown task field must be rejected.'
    $unknownTop = New-ValidFixture $testRoot 'unknown-top'
    Write-FixtureText (Join-Path $unknownTop 'contract.yaml') ("summary: extra`n" + $validContract)
    Expect-LintFailure $unknownTop $outPath "contract\.yaml: document: unknown field 'summary'" 'Unknown document field must be rejected.'
    $unknownKind = New-ValidFixture $testRoot 'unknown-kind'
    Write-FixtureText (Join-Path $unknownKind 'execution.yaml') ($validExecution -replace 'kind: implement', 'kind: deploy')
    Expect-LintFailure $unknownKind $outPath "task T-002 unknown kind 'deploy'" 'Unknown kind must be rejected.'
    $unknownMutation = New-ValidFixture $testRoot 'unknown-mutation'
    Write-FixtureText (Join-Path $unknownMutation 'execution.yaml') ($validExecution -replace 'mutation: allowed', 'mutation: maybe')
    Expect-LintFailure $unknownMutation $outPath "mutation must be 'allowed' or 'forbidden'" 'Unknown mutation must be rejected.'

    # verification.yaml: missing and malformed expect fail.
    $noExpectVerification = @'
schema_version: 1
checks:
  - id: V-001
    requirement: R-002
    type: scenario
'@
    $noExpect = New-ValidFixture $testRoot 'no-expect'
    Write-FixtureText (Join-Path $noExpect 'verification.yaml') $noExpectVerification
    Expect-LintFailure $noExpect $outPath "verification\.yaml: check V-001: missing required field 'expect'" 'V without expect must fail.'
    $emptyExpect = New-ValidFixture $testRoot 'empty-expect'
    Write-FixtureText (Join-Path $emptyExpect 'verification.yaml') ($validVerification -replace 'expect:\n\s+result: "[^"]*"', 'expect: {}')
    Expect-LintFailure $emptyExpect $outPath 'check V-001 expect must be a mapping' 'Empty expect mapping must fail.'
    $danglingRequirement = New-ValidFixture $testRoot 'dangling-requirement'
    Write-FixtureText (Join-Path $danglingRequirement 'verification.yaml') ($validVerification -replace 'requirement: R-002', 'requirement: R-009')
    Expect-LintFailure $danglingRequirement $outPath "check V-001 dangling requirement reference 'R-009'" 'V requirement must resolve into contract.yaml.'

    # Missing artifacts: valid state, identical behavior for existing changes.
    $bare = New-FixtureDirectory $testRoot 'bare'
    $result = & $lintScript -ChangePath $bare -NoThrow -OutputPath $outPath
    Assert-T $result.passed 'Empty change directory must pass.'
    Assert-T ($result.summary -ceq 'artifacts: none') "Empty change directory must report artifacts: none (got '$($result.summary)')."
    Assert-T (-not $result.artifacts.contract -and -not $result.artifacts.execution -and -not $result.artifacts.verification) 'Empty inventory must be all false.'
    $specOnly = New-FixtureDirectory $testRoot 'spec-only'
    Write-FixtureText (Join-Path $specOnly 'spec.md') $fixtureSpec
    $result = & $lintScript -ChangePath $specOnly -NoThrow -OutputPath $outPath
    Assert-T ($result.passed -and $result.summary -ceq 'artifacts: none') 'Change with only spec.md must pass unchanged.'

    # Existing repository change directories without artifacts are unaffected.
    foreach ($realChange in @('repository-task-registry', 'task-estimation-skill')) {
        $realPath = Join-Path $PackageRoot "openspec\changes\$realChange"
        $result = & $lintScript -ChangePath $realPath -NoThrow -OutputPath $outPath
        Assert-T $result.passed "Real change $realChange must pass without artifacts: $(@($result.errors) -join ' | ')"
        Assert-T ($result.summary -ceq 'artifacts: none') "Real change $realChange must report artifacts: none."
    }

    # Fail-closed parser: duplicate keys and tabs in indentation.
    $dupKey = New-ValidFixture $testRoot 'duplicate-key'
    Write-FixtureText (Join-Path $dupKey 'contract.yaml') ("schema_version: 1`n" + $validContract)
    Expect-LintFailure $dupKey $outPath "contract\.yaml line \d+: duplicate key 'schema_version'" 'Duplicate key must be rejected.'
    $tabbed = New-ValidFixture $testRoot 'tabbed'
    Write-FixtureText (Join-Path $tabbed 'contract.yaml') ($validContract -replace '  - id: R-001', "`t- id: R-001")
    Expect-LintFailure $tabbed $outPath 'contract\.yaml line \d+: tabs are not supported' 'Tabs in indentation must be rejected.'

    # spec_ref anchors: must be positive integers pointing at real spec items.
    $badAnchor = New-ValidFixture $testRoot 'bad-anchor'
    Write-FixtureText (Join-Path $badAnchor 'contract.yaml') ($validContract -replace 'spec_ref: 2', 'spec_ref: 9')
    Expect-LintFailure $badAnchor $outPath "requirement R-002 spec_ref 9 does not match any numbered item" 'Anchor must be checked against spec.md.'
    $zeroAnchor = New-ValidFixture $testRoot 'zero-anchor'
    Write-FixtureText (Join-Path $zeroAnchor 'contract.yaml') ($validContract -replace 'spec_ref: 2', 'spec_ref: 0')
    Expect-LintFailure $zeroAnchor $outPath "spec_ref must be a positive integer" 'spec_ref 0 must be rejected.'
    $textAnchor = New-ValidFixture $testRoot 'text-anchor'
    Write-FixtureText (Join-Path $textAnchor 'contract.yaml') ($validContract -replace 'spec_ref: 2', 'spec_ref: "Требуемое поведение / 2"')
    Expect-LintFailure $textAnchor $outPath "spec_ref must be a positive integer" 'Non-integer spec_ref must be rejected.'
    $missingSpec = New-ValidFixture $testRoot 'missing-spec'
    Remove-Item -LiteralPath (Join-Path $missingSpec 'spec.md') -Force
    Expect-LintFailure $missingSpec $outPath 'spec\.md: file not found; cannot verify contract\.yaml spec_ref anchors' 'Missing spec.md with contract.yaml must fail.'

    # Duplicate ids and self/duplicate/dangling references.
    $dupTask = New-ValidFixture $testRoot 'duplicate-task'
    Write-FixtureText (Join-Path $dupTask 'execution.yaml') ($validExecution -replace 'id: T-002', 'id: T-001')
    Expect-LintFailure $dupTask $outPath "execution\.yaml: duplicate task id T-001" 'Duplicate task id must be rejected.'
    $dupCheck = New-ValidFixture $testRoot 'duplicate-check'
    $doubleCheck = $validVerification + $validVerification.Substring($validVerification.IndexOf('checks:') + 'checks:'.Length)
    Write-FixtureText (Join-Path $dupCheck 'verification.yaml') $doubleCheck
    Expect-LintFailure $dupCheck $outPath 'verification\.yaml: duplicate check id V-001' 'Duplicate check id must be rejected.'
    $dupRequirement = New-ValidFixture $testRoot 'duplicate-requirement'
    Write-FixtureText (Join-Path $dupRequirement 'contract.yaml') ($validContract -replace 'id: R-002', 'id: R-001')
    Expect-LintFailure $dupRequirement $outPath 'contract\.yaml: duplicate requirement id R-001' 'Duplicate requirement id must be rejected.'
    $danglingDep = New-ValidFixture $testRoot 'dangling-depend'
    Write-FixtureText (Join-Path $danglingDep 'execution.yaml') ($validExecution -replace 'depends_on: \[T-001\]', 'depends_on: [T-009]')
    Expect-LintFailure $danglingDep $outPath "task T-002: dangling depends_on reference 'T-009'" 'Dangling depends_on must be rejected.'
    $selfDep = New-ValidFixture $testRoot 'self-depend'
    Write-FixtureText (Join-Path $selfDep 'execution.yaml') ($validExecution -replace 'depends_on: \[T-001\]', 'depends_on: [T-002]')
    Expect-LintFailure $selfDep $outPath "task T-002 self-reference in depends_on \('T-002'\)" 'Self-reference must be rejected.'
    $dupRef = New-ValidFixture $testRoot 'duplicate-ref'
    Write-FixtureText (Join-Path $dupRef 'execution.yaml') ($validExecution -replace 'depends_on: \[T-001\]', 'depends_on: [T-001, T-001]')
    Expect-LintFailure $dupRef $outPath "duplicate reference 'T-001' in depends_on" 'Duplicate references must be rejected.'
    $danglingVerify = New-ValidFixture $testRoot 'dangling-verify'
    Write-FixtureText (Join-Path $danglingVerify 'execution.yaml') ($validExecution -replace 'verify: \[V-001\]', 'verify: [V-999]')
    Expect-LintFailure $danglingVerify $outPath "task T-002: dangling verify reference 'V-999'" 'Dangling verify must be rejected.'
    $satisfiesWithoutContract = New-ValidFixture $testRoot 'satisfies-without-contract'
    Remove-Item -LiteralPath (Join-Path $satisfiesWithoutContract 'contract.yaml') -Force
    Expect-LintFailure $satisfiesWithoutContract $outPath "dangling satisfies reference 'R-001' \(contract\.yaml is absent\)" 'satisfies without contract.yaml must be rejected.'

    # Wrong types fail closed.
    $listGoal = New-ValidFixture $testRoot 'list-goal'
    Write-FixtureText (Join-Path $listGoal 'execution.yaml') ($validExecution -replace 'goal: "Implement the required behavior"', 'goal: []')
    Expect-LintFailure $listGoal $outPath "task T-002: field 'goal' must be a scalar" 'Non-scalar goal must be rejected.'
    $nestedConstraint = New-ValidFixture $testRoot 'nested-constraint'
    Write-FixtureText (Join-Path $nestedConstraint 'contract.yaml') ($validContract -replace '      configuration_changes: forbidden', '      configuration_changes: [a, b]')
    Expect-LintFailure $nestedConstraint $outPath 'constraints\.configuration_changes must be a non-empty scalar' 'Non-scalar constraint value must be rejected.'

    # Exit semantics mirror Test-1CSpec.ps1: throw without -NoThrow on failure.
    $throwObserved = $null
    try { & $lintScript -ChangePath $dangling -OutputPath $outPath | Out-Null } catch { $throwObserved = $_.Exception.Message }
    Assert-T ($null -ne $throwObserved -and $throwObserved -match 'contract lint failed') "Failing lint must throw without -NoThrow. Observed: $throwObserved"

    Write-Host "Spec contract lint tests passed: $script:checks checks; model=0; network=0; DB=0."
}
finally {
    if (Test-Path -LiteralPath $testRoot) { Remove-Item -LiteralPath $testRoot -Recurse -Force }
}
