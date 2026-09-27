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

# build.cf argument construction (non-mutating, no authorization needed)
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
    $buildResult = Invoke-OODispatch -Capability 'build.cf' -ProjectPath $projBuild -Params @{ target = (Join-Path $projBuild 'db'); source_dir = $srcDir; output_path = $outCf; executable_path = $mockExecutable }
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

# --- 9. unica: instruction payload, no extension.list, and -ImportResult relay -----------------
$projUnica = New-OOProject @"
onec:
  providers:
    - unica
"@
$unicaResult = Invoke-OODispatch -Capability 'extension.load' -ProjectPath $projUnica -Params @{ target = 'db1'; cfe_path = 'ext.cfe'; extension = 'TestExt' }
Assert-OO ($unicaResult.Exit -eq 11 -and $unicaResult.Result.status -eq 'BLOCKED') 'unica extension.load without ImportResult -> BLOCKED instruction'
Assert-OO ($unicaResult.Result.mutating -eq $true) 'unica extension.load result reports mutating=true'
Assert-OO ($unicaResult.Result.agent_tool.agent_tool -eq 'unica.runtime.execute') 'unica agent_tool payload names unica.runtime.execute'
Assert-OO ($unicaResult.Result.agent_tool.arguments.dryRun -eq $true) 'unica agent_tool payload requests dryRun:true'

$unicaExtList = Invoke-OODispatch -Capability 'extension.list' -ProjectPath $projUnica -Params @{}
Assert-OO ($unicaExtList.Exit -eq 11 -and $unicaExtList.Result.message -match 'no configured provider') 'unica has no extension.list capability, so it falls through to no-provider BLOCKED'

$importPath = Join-Path $projUnica 'imported.json'
[ordered]@{ status = 'PASS'; evidence = @(); message = 'agent observed a successful load' } | ConvertTo-Json | Set-Content -LiteralPath $importPath -Encoding utf8
$unicaImported = Invoke-OODispatch -Capability 'extension.load' -ProjectPath $projUnica -Params @{ target = 'db1' } -ImportResult $importPath
Assert-OO ($unicaImported.Exit -eq 0 -and $unicaImported.Result.status -eq 'PASS') '-ImportResult relays the agent-observed outcome instead of a fresh instruction'
Assert-OO ($unicaImported.Result.mutating -eq $true) 'Imported unica result still reports mutating=true'

# --- 10. grounding: fails closed until Get-1CMetadataIndex.ps1 exists ---------------------------
$projGrounding = New-OOProject @"
onec:
  providers:
    - grounding
"@
$groundingResult = Invoke-OODispatch -Capability 'metadata.inspect' -ProjectPath $projGrounding -Params @{}
Assert-OO ($groundingResult.Exit -eq 11 -and $groundingResult.Result.message -match 'no configured provider') 'grounding adapter fails closed (detect=false) while Get-1CMetadataIndex.ps1 is absent'

Write-Output "Test-OneCOpsContract: $script:checks checks passed."
