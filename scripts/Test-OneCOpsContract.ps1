#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
if (-not $PackageRoot) { $PackageRoot = Split-Path $PSScriptRoot -Parent }

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$onecOpsRoot = Join-Path $PackageRoot 'global/skills/1c-verify/onec-ops'
$dispatcher = Join-Path $onecOpsRoot 'Invoke-OneCOp.ps1'
$commonScript = Join-Path $onecOpsRoot 'OneCOps.Common.ps1'
$mockExecutable = Join-Path $PackageRoot 'scripts/fixtures/onec-ops/mock-1cv8.ps1'
foreach ($p in @($dispatcher, $commonScript, $mockExecutable)) {
    if (-not (Test-Path -LiteralPath $p -PathType Leaf)) { throw "Missing required onec-ops file: $p" }
}
. $commonScript

$script:checks = 0
function Assert-OO { param([bool]$Condition, [string]$Message) if (-not $Condition) { throw "TEST_FAIL: $Message" }; $script:checks++ }

function New-OOProject {
    param([string]$YamlOnecBlock = '')
    $root = Join-Path ([IO.Path]::GetTempPath()) ('bf-onec-ops-' + [guid]::NewGuid().ToString('N'))
    [void][IO.Directory]::CreateDirectory($root)
    if ($YamlOnecBlock) { Set-Content -LiteralPath (Join-Path $root 'bsl-flow.yaml') -Value $YamlOnecBlock -Encoding utf8 }
    return $root
}

function Invoke-OODispatch {
    param([string]$Capability, [string]$ProjectPath, [object]$Params, [string]$Provider, [string]$AuthorizationFile, [string]$ImportResult)
    $bound = @{ Capability = $Capability; ProjectPath = $ProjectPath }
    if ($null -ne $Params) { $bound.Params = $Params }
    if ($Provider) { $bound.Provider = $Provider }
    if ($AuthorizationFile) { $bound.AuthorizationFile = $AuthorizationFile }
    if ($ImportResult) { $bound.ImportResult = $ImportResult }
    $stdout = & $dispatcher @bound
    $exit = $LASTEXITCODE
    $jsonText = ($stdout | ForEach-Object { $_.ToString() }) -join "`n"
    $resultObj = $null
    try { $resultObj = $jsonText | ConvertFrom-Json -ErrorAction Stop } catch {}
    return [pscustomobject]@{ Exit = $exit; Result = $resultObj; Raw = $jsonText }
}

function Write-OOAuthorization {
    param([string]$Path, [string]$Capability, [string]$Target, [datetime]$ExpiresUtc)
    [ordered]@{ capability = $Capability; target = $Target; expires_utc = $ExpiresUtc.ToString('o') } | ConvertTo-Json | Set-Content -LiteralPath $Path -Encoding utf8
}

# --- 1. Every adapter manifest validates against provider.schema.json -------------------------
$providerSchema = Get-Content -Raw -LiteralPath (Join-Path $onecOpsRoot 'schemas/provider.schema.json') -Encoding UTF8 | ConvertFrom-Json
$adapterNames = @('fake', 'bslls', 'grounding', 'native-1cv8', 'vrunner', 'unica', 'skillset')
foreach ($name in $adapterNames) {
    $manifestPath = Join-Path $onecOpsRoot "adapters/$name/provider.json"
    Assert-OO (Test-Path -LiteralPath $manifestPath -PathType Leaf) "Adapter manifest exists: $name"
    $manifest = Get-Content -Raw -LiteralPath $manifestPath -Encoding UTF8 | ConvertFrom-Json
    $issues = @(Test-OOJsonSchema -Schema $providerSchema -Instance $manifest)
    Assert-OO ($issues.Count -eq 0) "Adapter manifest is schema-valid: $name ($($issues -join '; '))"
    Assert-OO ($manifest.contract -eq 'onec-ops/v1') "Adapter declares onec-ops/v1: $name"
}

# unica must not declare extension.list (its Unica operation=extensions is mutating, not a list route)
$unicaManifest = Get-Content -Raw -LiteralPath (Join-Path $onecOpsRoot 'adapters/unica/provider.json') -Encoding UTF8 | ConvertFrom-Json
Assert-OO ($null -eq $unicaManifest.capabilities.PSObject.Properties['extension.list']) 'unica does not declare extension.list'

# --- 2. Dispatcher selection: providers list order, skip missing manifests --------------------
$proj = New-OOProject @"
onec:
  providers:
    - does-not-exist
    - fake
"@
$outcome = Invoke-OODispatch -Capability 'syntax.check' -ProjectPath $proj -Params @{ outcome = 'PASS' }
Assert-OO ($outcome.Exit -eq 0) 'Provider list skips a missing adapter and falls through to fake (exit 0)'
Assert-OO ($outcome.Result.provider -eq 'fake') 'Selected provider is fake'
Assert-OO ($outcome.Result.status -eq 'PASS') 'Result status is PASS'
Assert-OO ($outcome.Result.schema_version -eq 1) 'Result carries schema_version 1'
$reportDir = Join-Path $proj '.bsl-flow/reports/onec-ops'
Assert-OO (@(Get-ChildItem -LiteralPath $reportDir -Filter '*.json' -File).Count -ge 1) 'A report file was written under .bsl-flow/reports/onec-ops'

# --- 3. onec.overrides takes priority over onec.providers --------------------------------------
$proj2 = New-OOProject @"
onec:
  providers:
    - does-not-exist
  overrides:
    syntax.check: fake
"@
$overrideResult = Invoke-OODispatch -Capability 'syntax.check' -ProjectPath $proj2 -Params @{ outcome = 'PASS' }
Assert-OO ($overrideResult.Exit -eq 0 -and $overrideResult.Result.provider -eq 'fake') 'onec.overrides selects fake even though onec.providers only lists a missing adapter'

# -Provider explicit param wins over everything
$explicitResult = Invoke-OODispatch -Capability 'syntax.check' -ProjectPath $proj2 -Params @{ outcome = 'PASS' } -Provider 'fake'
Assert-OO ($explicitResult.Exit -eq 0 -and $explicitResult.Result.provider -eq 'fake') '-Provider overrides onec.providers/overrides'

# --- 4. No configured provider -> BLOCKED exit 11 ----------------------------------------------
$projNone = New-OOProject @"
onec:
  providers:
    - does-not-exist
"@
$noneResult = Invoke-OODispatch -Capability 'syntax.check' -ProjectPath $projNone -Params @{}
Assert-OO ($noneResult.Exit -eq 11) 'No configured provider -> exit 11'
Assert-OO ($noneResult.Result.status -eq 'BLOCKED') 'No configured provider -> status BLOCKED'
Assert-OO ($noneResult.Result.message -match 'no configured provider') 'BLOCKED message names the missing capability'

# --- 5. Fake PASS/FAIL results: evidence and raw_output are hashed by the dispatcher -----------
$projFake = New-OOProject @"
onec:
  providers:
    - fake
"@
$evidenceFile = Join-Path $projFake 'evidence.txt'
Set-Content -LiteralPath $evidenceFile -Value 'evidence-content' -Encoding utf8
$expectedEvidenceHash = (Get-FileHash -LiteralPath $evidenceFile -Algorithm SHA256).Hash.ToLowerInvariant()
$expectedRawHash = Get-OOSha256Text 'raw-output-text'
$passResult = Invoke-OODispatch -Capability 'syntax.check' -ProjectPath $projFake -Params @{ outcome = 'PASS'; evidence = @('evidence.txt'); raw_output = 'raw-output-text' }
Assert-OO ($passResult.Exit -eq 0) 'Fake PASS -> exit 0'
Assert-OO ($passResult.Result.evidence[0].sha256 -eq $expectedEvidenceHash) 'Evidence file hash matches Get-FileHash'
Assert-OO ($passResult.Result.raw_output_sha256 -eq $expectedRawHash) 'raw_output_sha256 matches the hashed raw_output text'

$failResult = Invoke-OODispatch -Capability 'syntax.check' -ProjectPath $projFake -Params @{ outcome = 'FAIL'; message = 'boom' }
Assert-OO ($failResult.Exit -eq 1) 'Fake FAIL -> exit 1'
Assert-OO ($failResult.Result.status -eq 'FAIL') 'Fake FAIL -> status FAIL'
Assert-OO ($failResult.Result.message -eq 'boom') 'Fake message is passed through'

# --- 6. Mutating capability without/with wrong/expired authorization: BLOCKED, provider not run -
$projMut = New-OOProject @"
onec:
  providers:
    - fake
"@
$noAuth = Invoke-OODispatch -Capability 'extension.load' -ProjectPath $projMut -Params @{ outcome = 'PASS'; target = 'db1' }
Assert-OO ($noAuth.Exit -eq 11 -and $noAuth.Result.status -eq 'BLOCKED') 'Mutating capability without authorization -> BLOCKED'
Assert-OO ($noAuth.Result.mutating -eq $true) 'Result reports mutating=true even when refused pre-invoke'

$wrongAuthPath = Join-Path $projMut 'wrong-auth.json'
Write-OOAuthorization -Path $wrongAuthPath -Capability 'extension.load' -Target 'OTHER-TARGET' -ExpiresUtc ([DateTime]::UtcNow.AddHours(1))
$wrongAuth = Invoke-OODispatch -Capability 'extension.load' -ProjectPath $projMut -Params @{ outcome = 'PASS'; target = 'db1' } -AuthorizationFile $wrongAuthPath
Assert-OO ($wrongAuth.Exit -eq 11) 'Authorization target mismatch -> BLOCKED'

$expiredAuthPath = Join-Path $projMut 'expired-auth.json'
Write-OOAuthorization -Path $expiredAuthPath -Capability 'extension.load' -Target 'db1' -ExpiresUtc ([DateTime]::UtcNow.AddHours(-1))
$expiredAuth = Invoke-OODispatch -Capability 'extension.load' -ProjectPath $projMut -Params @{ outcome = 'PASS'; target = 'db1' } -AuthorizationFile $expiredAuthPath
Assert-OO ($expiredAuth.Exit -eq 11) 'Expired authorization -> BLOCKED'

$validAuthPath = Join-Path $projMut 'valid-auth.json'
Write-OOAuthorization -Path $validAuthPath -Capability 'extension.load' -Target 'db1' -ExpiresUtc ([DateTime]::UtcNow.AddHours(1))
$validAuth = Invoke-OODispatch -Capability 'extension.load' -ProjectPath $projMut -Params @{ outcome = 'PASS'; target = 'db1' } -AuthorizationFile $validAuthPath
Assert-OO ($validAuth.Exit -eq 0 -and $validAuth.Result.status -eq 'PASS') 'Valid authorization reaches the provider and returns its PASS'

# --- 7. native-1cv8: authorization gate actually prevents the process from starting ------------
$projNative = New-OOProject @"
onec:
  providers:
    - native-1cv8
  native-1cv8:
    platform_bin: $mockExecutable
"@
$dummyCfe = Join-Path $projNative 'ext.cfe'
Set-Content -LiteralPath $dummyCfe -Value 'dummy' -Encoding utf8
$recordPath = Join-Path $projNative 'mock-record.txt'
if (Test-Path -LiteralPath $recordPath) { Remove-Item -LiteralPath $recordPath -Force }
$env:BF_MOCK_RECORD = $recordPath
try {
    $nativeNoAuth = Invoke-OODispatch -Capability 'extension.load' -ProjectPath $projNative -Params @{ target = (Join-Path $projNative 'db'); cfe_path = $dummyCfe; extension = 'TestExt'; executable_path = $mockExecutable }
    Assert-OO ($nativeNoAuth.Exit -eq 11) 'native-1cv8 extension.load without authorization -> BLOCKED'
    Assert-OO (-not (Test-Path -LiteralPath $recordPath)) 'native-1cv8 mock process was never started without authorization'

    $nativeAuthPath = Join-Path $projNative 'native-auth.json'
    Write-OOAuthorization -Path $nativeAuthPath -Capability 'extension.load' -Target (Join-Path $projNative 'db') -ExpiresUtc ([DateTime]::UtcNow.AddHours(1))
    $nativeAuthed = Invoke-OODispatch -Capability 'extension.load' -ProjectPath $projNative -Params @{ target = (Join-Path $projNative 'db'); cfe_path = $dummyCfe; extension = 'TestExt'; executable_path = $mockExecutable } -AuthorizationFile $nativeAuthPath
    Assert-OO ($nativeAuthed.Exit -eq 0 -and $nativeAuthed.Result.status -eq 'PASS') 'native-1cv8 extension.load with valid authorization runs and returns PASS'
    Assert-OO (Test-Path -LiteralPath $recordPath) 'native-1cv8 mock process ran once authorized'
    $recorded = Get-Content -Raw -LiteralPath $recordPath
    Assert-OO ($recorded -match '/LoadCfg') 'native-1cv8 extension.load argv includes /LoadCfg'
    Assert-OO ($recorded -match '/UpdateDBCfg') 'native-1cv8 extension.load argv includes /UpdateDBCfg'
    Assert-OO ($recorded -match '-Extension') 'native-1cv8 extension.load argv includes -Extension'
}
finally { Remove-Item Env:\BF_MOCK_RECORD -ErrorAction SilentlyContinue }

# build.cf loads the specified database, so this provider also requires authorization
$projBuild = New-OOProject @"
onec:
  providers:
    - native-1cv8
  native-1cv8:
    platform_bin: $mockExecutable
"@
$srcDir = Join-Path $projBuild 'src'
[void][IO.Directory]::CreateDirectory($srcDir)
$outCf = Join-Path $projBuild 'out.cf'
$buildRecord = Join-Path $projBuild 'mock-record.txt'
$env:BF_MOCK_RECORD = $buildRecord
try {
    $buildDenied = Invoke-OODispatch -Capability 'build.cf' -ProjectPath $projBuild -Params @{ target = (Join-Path $projBuild 'db') }
    Assert-OO ($buildDenied.Exit -eq 11 -and -not (Test-Path -LiteralPath $buildRecord)) 'Native build requires authorization before touching the target database'
    $buildAuth = Join-Path $projBuild 'auth.json'
    Write-OOAuthorization -Path $buildAuth -Capability 'build.cf' -Target (Join-Path $projBuild 'db') -ExpiresUtc ([DateTime]::UtcNow.AddHours(1))
    $buildResult = Invoke-OODispatch -AuthorizationFile $buildAuth -Capability 'build.cf' -ProjectPath $projBuild -Params @{ target = (Join-Path $projBuild 'db'); source_dir = $srcDir; output_path = $outCf; executable_path = $mockExecutable }
    Assert-OO ($buildResult.Exit -eq 0 -and $buildResult.Result.status -eq 'PASS') 'build.cf via native-1cv8 mock returns PASS'
    Assert-OO (Test-Path -LiteralPath $outCf -PathType Leaf) 'build.cf produced the output .cf file via the mocked /DumpCfg step'
    $buildRecorded = Get-Content -Raw -LiteralPath $buildRecord
    Assert-OO ($buildRecorded -match '/LoadConfigFromFiles') 'build.cf argv includes /LoadConfigFromFiles'
    Assert-OO ($buildRecorded -match '/DumpCfg') 'build.cf argv includes /DumpCfg'
}
finally { Remove-Item Env:\BF_MOCK_RECORD -ErrorAction SilentlyContinue }

# --- 8. vrunner: argument construction for a non-mutating capability ---------------------------
$projVrunner = New-OOProject @"
onec:
  providers:
    - vrunner
  vrunner:
    bin: $mockExecutable
"@
$vSrc = Join-Path $projVrunner 'src'
[void][IO.Directory]::CreateDirectory($vSrc)
$vRecord = Join-Path $projVrunner 'mock-record.txt'
$env:BF_MOCK_RECORD = $vRecord
try {
    $vResult = Invoke-OODispatch -Capability 'syntax.check' -ProjectPath $projVrunner -Params @{ src = $vSrc; vrunner_bin = $mockExecutable }
    Assert-OO ($vResult.Exit -eq 0 -and $vResult.Result.status -eq 'PASS') 'vrunner syntax.check via mock returns PASS'
    $vRecorded = Get-Content -Raw -LiteralPath $vRecord
    Assert-OO ($vRecorded -match 'syntax-check') 'vrunner syntax.check argv includes the syntax-check subcommand'
    Assert-OO ($vRecorded -match '--src') 'vrunner syntax.check argv includes --src'
}
finally { Remove-Item Env:\BF_MOCK_RECORD -ErrorAction SilentlyContinue }

# vrunner mutating capability also respects the authorization gate (no process without it)
$vRecord2 = Join-Path $projVrunner 'mock-record-2.txt'
$env:BF_MOCK_RECORD = $vRecord2
try {
    $vMutNoAuth = Invoke-OODispatch -Capability 'test.vanessa' -ProjectPath $projVrunner -Params @{ target = 'db1'; settings = 'settings.json'; vrunner_bin = $mockExecutable }
    Assert-OO ($vMutNoAuth.Exit -eq 11) 'vrunner test.vanessa without authorization -> BLOCKED'
    Assert-OO (-not (Test-Path -LiteralPath $vRecord2)) 'vrunner mock process was never started without authorization'
}
finally { Remove-Item Env:\BF_MOCK_RECORD -ErrorAction SilentlyContinue }

# --- 9. Unica restriction survives authorization and imported PASS ----------------------------
$projUnica = New-OOProject "onec:`n  providers:`n    - unica"
$unicaAuth = Join-Path $projUnica 'auth.json'
Write-OOAuthorization -Path $unicaAuth -Capability 'extension.load' -Target 'db1' -ExpiresUtc ([DateTime]::UtcNow.AddHours(1))
$unicaResult = Invoke-OODispatch -Capability 'extension.load' -ProjectPath $projUnica -Params @{ target = 'db1' } -AuthorizationFile $unicaAuth
Assert-OO ($unicaResult.Exit -eq 11 -and $unicaResult.Result.message -match 'recovery restriction') 'Unica is BLOCKED despite valid authorization'
Assert-OO ($null -eq $unicaResult.Result.PSObject.Properties['agent_tool']) 'Unica does not instruct a forbidden runtime call'
$unicaExtList = Invoke-OODispatch -Capability 'extension.list' -ProjectPath $projUnica -Params @{}
Assert-OO ($unicaExtList.Exit -eq 11 -and $unicaExtList.Result.message -match 'no configured provider') 'Unica does not expose extension.list'
$importPath = Join-Path $projUnica 'imported.json'
' {"status":"PASS","evidence":[]} ' | Set-Content -LiteralPath $importPath
$unicaImported = Invoke-OODispatch -Capability 'extension.load' -ProjectPath $projUnica -Params @{ target = 'db1' } -AuthorizationFile $unicaAuth -ImportResult $importPath
Assert-OO ($unicaImported.Exit -eq 11) 'Imported PASS cannot lift the Unica restriction'

# --- 10. Grounding uses the real index and preserves metadata evidence ------------------------
$projGrounding = New-OOProject "onec:`n  providers:`n    - grounding"
$fixtureRoot = Join-Path $PackageRoot 'scripts/fixtures/metadata/designer-mini'
$groundingResult = Invoke-OODispatch -Capability 'metadata.inspect' -ProjectPath $projGrounding -Params @{ source_root = $fixtureRoot }
Assert-OO ($groundingResult.Exit -eq 0) "Grounding wraps real metadata index: $($groundingResult.Raw)"
$indexPath = Join-Path $projGrounding $groundingResult.Result.evidence[0].path
$index = Get-Content -Raw -LiteralPath $indexPath | ConvertFrom-Json
Assert-OO ($null -ne $index.index.objects.PSObject.Properties['Справочник.Номенклатура']) 'Grounding evidence includes an actual fixture catalog'
Assert-OO ($groundingResult.Result.evidence[0].sha256 -eq (Get-OOSha256File $indexPath)) 'Grounding hashes the actual index evidence'
$missingSource = Invoke-OODispatch -Capability 'metadata.inspect' -ProjectPath $projGrounding -Params @{}
Assert-OO ($missingSource.Exit -eq 11) 'Missing source_root is BLOCKED'

# Empty/partial authorization fails closed, including strict-mode missing properties.
foreach ($json in @('{}', '{"capability":"extension.load"}', '{"capability":"extension.load","target":"db1"}')) {
    $badAuthPath = Join-Path $projMut 'bad-auth.json'
    $json | Set-Content -LiteralPath $badAuthPath
    $badAuth = Invoke-OODispatch -Capability 'extension.load' -ProjectPath $projMut -Params @{ target = 'db1' } -AuthorizationFile $badAuthPath
    Assert-OO ($badAuth.Exit -eq 11) 'Partial authorization JSON is BLOCKED'
}

# Failed load must never start update/dump; evidence log remains the platform log.
$env:BF_MOCK_FAIL_LOAD = '1'
$failureRecord = Join-Path $projNative 'failed-load-record.txt'
$env:BF_MOCK_RECORD = $failureRecord
try {
    $failedLoad = Invoke-OODispatch -Capability 'extension.load' -ProjectPath $projNative -Params @{ target = (Join-Path $projNative 'db'); cfe_path = $dummyCfe; extension = 'TestExt' } -AuthorizationFile $nativeAuthPath
    Assert-OO ($failedLoad.Exit -eq 1) 'Failed load is FAIL'
    $failureCommands = Get-Content -Raw -LiteralPath $failureRecord
    Assert-OO ($failureCommands -notmatch '/UpdateDBCfg') 'No update follows a failed extension load'
    $failedBuild = Invoke-OODispatch -Capability 'build.cf' -ProjectPath $projBuild -Params @{ target = (Join-Path $projBuild 'db'); source_dir = $srcDir; output_path = $outCf } -AuthorizationFile $buildAuth
    Assert-OO ($failedBuild.Exit -eq 1) 'Failed source load is FAIL despite a stale output artifact'
    $failureCommands = Get-Content -Raw -LiteralPath $failureRecord
    Assert-OO ($failureCommands -notmatch '/DumpCfg') 'No dump follows a failed source load'
}
finally { Remove-Item Env:\BF_MOCK_RECORD, Env:\BF_MOCK_FAIL_LOAD -ErrorAction SilentlyContinue }

# Native YAxUnit requires real executed cases and no failure nodes, regardless of exit code.
$testAuth = Join-Path $projNative 'test-auth.json'
Write-OOAuthorization -Path $testAuth -Capability 'test.yaxunit' -Target (Join-Path $projNative 'db') -ExpiresUtc ([DateTime]::UtcNow.AddHours(1))
foreach ($case in @(
    @{ xml = '<testsuite tests="0"/>'; exit = 1 },
    @{ xml = '<testsuite><testcase><failure/></testcase></testsuite>'; exit = 1 },
    @{ xml = '<testsuite><testcase><skipped/></testcase></testsuite>'; exit = 1 },
    @{ xml = '<testsuite tests="1"><testcase name="works"/></testsuite>'; exit = 0 }
)) {
    $env:BF_MOCK_JUNIT = $case.xml
    try {
        $testRun = Invoke-OODispatch -Capability 'test.yaxunit' -ProjectPath $projNative -Params @{ target = (Join-Path $projNative 'db'); modules = @('Tests') } -AuthorizationFile $testAuth
        Assert-OO ($testRun.Exit -eq $case.exit) "JUnit acceptance: $($case.xml)"
    }
    finally { Remove-Item Env:\BF_MOCK_JUNIT -ErrorAction SilentlyContinue }
}
# Remaining adapter contracts and native fail-closed inventory.
$inlineProject = New-OOProject "onec:`n  providers: [missing, fake]"
$inlineResult = Invoke-OODispatch -Capability 'syntax.check' -ProjectPath $inlineProject
Assert-OO ($inlineResult.Exit -eq 0 -and $inlineResult.Result.provider -eq 'fake') 'Inline provider list is supported'
$inventory = Invoke-OODispatch -Capability 'extension.list' -ProjectPath $projNative -Params @{ target = 'unknown' }
Assert-OO ($inventory.Exit -eq 11) 'Unknown installed extension state remains BLOCKED'

# No repository/binary: the actual static wrapper must report BLOCKED, not a fabricated PASS.
$staticResult = Invoke-OODispatch -Capability 'static.bslls' -ProjectPath $projFake -Provider bslls -Params @{ required = $true }
Assert-OO ($staticResult.Exit -eq 11 -and $staticResult.Result.provider -eq 'bslls') 'BSL LS wrapper missing preconditions is BLOCKED'

$mockSkill = Join-Path $PackageRoot 'scripts/fixtures/onec-ops/mock-skill.ps1'
$projSkill = New-OOProject "onec:`n  providers: [skillset]`n  skillset:`n    map:`n      syntax.check: $mockSkill`n      extension.load: $mockSkill"
$skillRecord = Join-Path $projSkill 'record.txt'
$env:BF_MOCK_RECORD = $skillRecord
try {
    $skillDenied = Invoke-OODispatch -Capability 'extension.load' -ProjectPath $projSkill -Params @{ target = 'db1'; mode = 'ok' }
    Assert-OO ($skillDenied.Exit -eq 11 -and -not (Test-Path -LiteralPath $skillRecord)) 'Skillset cannot start a mutating script without authorization'
    $skillGood = Invoke-OODispatch -Capability 'syntax.check' -ProjectPath $projSkill -Params @{ mode = 'ok' }
    Assert-OO ($skillGood.Exit -eq 0) 'Structured script result is accepted'
    $skillFailed = Invoke-OODispatch -Capability 'syntax.check' -ProjectPath $projSkill -Params @{ mode = 'failure' }
    Assert-OO ($skillFailed.Exit -eq 1) 'Script nonzero exit overrides a claimed PASS'
    $skillUnstructured = Invoke-OODispatch -Capability 'syntax.check' -ProjectPath $projSkill -Params @{ mode = 'unstructured' }
    Assert-OO ($skillUnstructured.Exit -eq 11) 'Script exit 0 without structured evidence is BLOCKED'
}
finally { Remove-Item Env:\BF_MOCK_RECORD -ErrorAction SilentlyContinue }
$skillImport = Join-Path $projSkill 'import.json'
'{"status":"PASS","capability":"syntax.check","target":"db1","evidence":[]}' | Set-Content -LiteralPath $skillImport
$skillNoEvidence = Invoke-OODispatch -Capability 'syntax.check' -ProjectPath $projSkill -Params @{ target = 'db1' } -ImportResult $skillImport
Assert-OO ($skillNoEvidence.Exit -eq 11) 'Imported PASS with no evidence is BLOCKED'
$skillWrongTarget = Invoke-OODispatch -Capability 'syntax.check' -ProjectPath $projSkill -Params @{ target = 'db2' } -ImportResult $skillImport
Assert-OO ($skillWrongTarget.Exit -eq 11) 'Imported result from another target is BLOCKED'
$blockedMap = New-OOProject "onec:`n  providers: [skillset]`n  skillset:`n    map:`n      test.yaxunit: unica.runtime.job.start"
$blockedAuth = Join-Path $blockedMap 'auth.json'
Write-OOAuthorization -Path $blockedAuth -Capability 'test.yaxunit' -Target db1 -ExpiresUtc ([DateTime]::UtcNow.AddHours(1))
$blockedMapping = Invoke-OODispatch -Capability 'test.yaxunit' -ProjectPath $blockedMap -Params @{ target = 'db1' } -AuthorizationFile $blockedAuth
Assert-OO ($blockedMapping.Exit -eq 11 -and $null -eq $blockedMapping.Result.PSObject.Properties['agent_tool']) 'Skillset cannot emit a forbidden Unica durable-job instruction'

# Validate representative results with the platform JSON-Schema implementation too.
$resultSchemaPath = Join-Path $onecOpsRoot 'schemas/op-result.schema.json'
foreach ($result in @($passResult, $failResult, $nativeNoAuth, $groundingResult, $staticResult, $unicaResult, $skillGood, $vResult)) {
    Assert-OO (Test-Json -Json $result.Raw -SchemaFile $resultSchemaPath -ErrorAction Stop) 'Result passes independent JSON-Schema validation'
}
# Direct entry invocation must enforce the same authorization as the dispatcher.
$directRecord = Join-Path $projNative 'direct-denied-record.txt'
$env:BF_MOCK_RECORD = $directRecord
try {
    $directCases = @(
        @{ provider = 'native-1cv8'; entry = 'Invoke-NativeExtensionLoad.ps1'; capability = 'extension.load' },
        @{ provider = 'native-1cv8'; entry = 'Invoke-NativeConfigUpdate.ps1'; capability = 'config.update' },
        @{ provider = 'native-1cv8'; entry = 'Invoke-NativeBuild.ps1'; capability = 'build.cf' },
        @{ provider = 'native-1cv8'; entry = 'Invoke-NativeBuild.ps1'; capability = 'build.cfe' },
        @{ provider = 'native-1cv8'; entry = 'Invoke-NativeYaxunit.ps1'; capability = 'test.yaxunit' },
        @{ provider = 'vrunner'; entry = 'Invoke-VrunnerOp.ps1'; capability = 'test.vanessa' },
        @{ provider = 'vrunner'; entry = 'Invoke-VrunnerOp.ps1'; capability = 'build.cf' },
        @{ provider = 'skillset'; entry = 'Invoke-SkillsetOp.ps1'; capability = 'extension.load' }
    )
    foreach ($directCase in $directCases) {
        $adapterDir = Join-Path $onecOpsRoot ('adapters/' + $directCase.provider)
        $entry = Join-Path $adapterDir $directCase.entry
        $directParams = [pscustomobject]@{ target = 'db1'; cfe_path = $dummyCfe; extension = 'TestExt'; source_dir = $srcDir; output_path = $outCf; modules = @('Tests'); src = $srcDir; settings = 'settings.json'; executable_path = $mockExecutable; vrunner_bin = $mockExecutable; mode = 'ok' }
        $direct = & $entry -ProjectPath $projSkill -Params $directParams -AdapterDir $adapterDir -Capability $directCase.capability
        Assert-OO ($direct.status -eq 'BLOCKED' -and $direct.message -match 'authorization') "Direct $($directCase.provider)/$($directCase.capability) requires authorization"
        Assert-OO (-not (Test-Path -LiteralPath $directRecord)) 'Denied direct invocation never starts the mock process'
        foreach ($bad in @(
            @{ capability = 'metadata.inspect'; target = 'db1'; expires = [DateTime]::UtcNow.AddHours(1) },
            @{ capability = $directCase.capability; target = 'different-db'; expires = [DateTime]::UtcNow.AddHours(1) },
            @{ capability = $directCase.capability; target = 'db1'; expires = [DateTime]::UtcNow.AddHours(-1) }
        )) {
            $directAuthPath = Join-Path $projNative 'direct-auth.json'
            Write-OOAuthorization -Path $directAuthPath -Capability $bad.capability -Target $bad.target -ExpiresUtc $bad.expires
            $direct = & $entry -ProjectPath $projSkill -Params $directParams -AdapterDir $adapterDir -Capability $directCase.capability -AuthorizationFile $directAuthPath
            Assert-OO ($direct.status -eq 'BLOCKED' -and -not (Test-Path -LiteralPath $directRecord)) "Direct $($directCase.capability) refuses mismatched/expired authorization without a process"
        }
    }
    # Supplying another operation's valid authorization cannot relabel a fixed native entry.
    Write-OOAuthorization -Path $directAuthPath -Capability 'metadata.inspect' -Target db1 -ExpiresUtc ([DateTime]::UtcNow.AddHours(1))
    $nativeDir = Join-Path $onecOpsRoot 'adapters/native-1cv8'
    $confusedEntry = & (Join-Path $nativeDir 'Invoke-NativeConfigUpdate.ps1') -ProjectPath $projNative -Params $directParams -AdapterDir $nativeDir -Capability 'metadata.inspect' -AuthorizationFile $directAuthPath
    Assert-OO ($confusedEntry.status -eq 'BLOCKED' -and -not (Test-Path -LiteralPath $directRecord)) 'Fixed native entry rejects a relabeled capability'
}
finally { Remove-Item Env:\BF_MOCK_RECORD -ErrorAction SilentlyContinue }

# Load/update evidence is durable platform logs, explicitly limited to process completion.
Assert-OO ($nativeAuthed.Result.evidence.Count -eq 2) 'Successful native extension load retains both platform logs'
foreach ($logEvidence in $nativeAuthed.Result.evidence) {
    Assert-OO ((Get-OOSha256File $logEvidence.path) -eq $logEvidence.sha256) 'Load log evidence hash matches its durable file'
    Assert-OO ((Get-Content -LiteralPath $logEvidence.path -Raw) -match 'mock-log') 'Original platform log content is retained'
}
Assert-OO ($nativeAuthed.Result.message -match 'post-state and behavior unverified') 'Load PASS states the evidence limitation'
$updateAuth = Join-Path $projNative 'update-auth.json'
Write-OOAuthorization -Path $updateAuth -Capability 'config.update' -Target db1 -ExpiresUtc ([DateTime]::UtcNow.AddHours(1))
$update = Invoke-OODispatch -Capability 'config.update' -ProjectPath $projNative -Params @{ target = 'db1' } -AuthorizationFile $updateAuth
Assert-OO ($update.Exit -eq 0 -and $update.Result.evidence.Count -eq 1) 'Successful config update attaches its platform log'
Assert-OO ((Get-OOSha256File $update.Result.evidence[0].path) -eq $update.Result.evidence[0].sha256) 'Update log hash matches the durable file'
Assert-OO ($update.Result.message -match 'post-state and behavior unverified') 'Update PASS states the evidence limitation'
Assert-OO ($failedLoad.Result.evidence.Count -eq 1 -and (Test-Path -LiteralPath $failedLoad.Result.evidence[0].path)) 'Failed load retains its platform log'
# Credential redaction belongs to the shared display argv, never to process arguments.
$nativeDir = Join-Path $onecOpsRoot 'adapters/native-1cv8'
. (Join-Path $nativeDir 'Native.Common.ps1')
$syntheticPassword = 'test-only-secret-P4ss!'
$syntheticUser = 'test-only-user'
$originalArgs = @('DESIGNER', '/N', $syntheticUser, '/P', $syntheticPassword, '/F', 'db1', ('--password=' + $syntheticPassword), '--db-pwd', $syntheticPassword)
$beforeArgs = ConvertTo-Json -InputObject $originalArgs -Compress
$safeArgs = @(Get-N1PreviewArguments -Argv $originalArgs)
Assert-OO (($safeArgs -join ' ') -notmatch [regex]::Escape($syntheticPassword)) 'Preview helper redacts native and vrunner password flags'
Assert-OO (($safeArgs -join ' ') -notmatch [regex]::Escape($syntheticUser)) 'Preview helper redacts credentials username'
Assert-OO ((ConvertTo-Json -InputObject $originalArgs -Compress) -ceq $beforeArgs) 'Redaction does not mutate actual process arguments'
$previewRecord = Join-Path $projNative 'preview-process-record.txt'
$env:BF_MOCK_RECORD = $previewRecord
try {
    foreach ($dryCase in @(
        @{ entry = 'Invoke-NativeExtensionLoad.ps1'; capability = 'extension.load' },
        @{ entry = 'Invoke-NativeConfigUpdate.ps1'; capability = 'config.update' },
        @{ entry = 'Invoke-NativeBuild.ps1'; capability = 'build.cf' },
        @{ entry = 'Invoke-NativeBuild.ps1'; capability = 'build.cfe' },
        @{ entry = 'Invoke-NativeYaxunit.ps1'; capability = 'test.yaxunit' }
    )) {
        $dryAuth = Join-Path $projNative 'dry-auth.json'
        Write-OOAuthorization -Path $dryAuth -Capability $dryCase.capability -Target db1 -ExpiresUtc ([DateTime]::UtcNow.AddHours(1))
        $dryParams = [pscustomobject]@{ target = 'db1'; cfe_path = $dummyCfe; extension = 'TestExt'; source_dir = $srcDir; output_path = $outCf; modules = @('Tests'); username = $syntheticUser; password = $syntheticPassword; executable_path = $mockExecutable; dry_run = $true }
        $dryResult = & (Join-Path $nativeDir $dryCase.entry) -ProjectPath $projNative -Params $dryParams -AdapterDir $nativeDir -Capability $dryCase.capability -AuthorizationFile $dryAuth
        $dryJson = $dryResult | ConvertTo-Json -Depth 12
        Assert-OO ($dryResult.status -eq 'BLOCKED' -and $dryResult.raw_output -match '\[REDACTED\]') "Direct native $($dryCase.capability) preview redacts credentials"
        Assert-OO ($dryJson -notmatch [regex]::Escape($syntheticPassword) -and $dryJson -notmatch [regex]::Escape($syntheticUser)) 'Serialized dry-run result contains no synthetic credentials'
    }
    $vrunnerDir = Join-Path $onecOpsRoot 'adapters/vrunner'
    $vrunnerPreview = & (Join-Path $vrunnerDir 'Invoke-VrunnerOp.ps1') -ProjectPath $projVrunner -AdapterDir $vrunnerDir -Capability syntax.check -Params ([pscustomobject]@{ src = $srcDir; vrunner_bin = $mockExecutable; dry_run = $true; password = $syntheticPassword })
    Assert-OO (($vrunnerPreview | ConvertTo-Json -Depth 12) -notmatch [regex]::Escape($syntheticPassword)) 'Vrunner dry-run does not serialize supplied credentials'
    Assert-OO (-not (Test-Path -LiteralPath $previewRecord)) 'Credential preview tests do not launch processes'
}
finally { Remove-Item Env:\BF_MOCK_RECORD -ErrorAction SilentlyContinue }
Write-Output "Test-OneCOpsContract: $script:checks checks passed."
