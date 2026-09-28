#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
if (-not $PackageRoot) { $PackageRoot = Split-Path $PSScriptRoot -Parent }

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:mainScript = Join-Path $PackageRoot 'global/skills/1c-verify/scripts/Invoke-1CStaticDiff.ps1'
if (-not (Test-Path -LiteralPath $script:mainScript -PathType Leaf)) { throw "Static diff script is missing: $script:mainScript" }

$script:checks = [Collections.Generic.List[string]]::new()
function Assert-SDTest {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "TEST_FAIL: $Message" }
    [void]$script:checks.Add($Message)
}

function New-SDTestRepo {
    param([Parameter(Mandatory)][string]$Root)
    [void][IO.Directory]::CreateDirectory($Root)
    [void](& git -C $Root init --quiet 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "Failed to init test repository: $Root" }
    [void](& git -C $Root config user.name 'BSL Flow Test' 2>&1)
    [void](& git -C $Root config user.email 'test@bsl-flow.invalid' 2>&1)
    [void](& git -C $Root config commit.gpgsign false 2>&1)
    return $Root
}

function Write-SDFile {
    param([Parameter(Mandatory)][string]$Root, [Parameter(Mandatory)][string]$RelPath, [Parameter(Mandatory)][string]$Content, [switch]$Crlf)
    $full = Join-Path $Root ($RelPath -replace '/', [IO.Path]::DirectorySeparatorChar)
    [void][IO.Directory]::CreateDirectory((Split-Path -Parent $full))
    $text = if ($Crlf) { $Content -replace "(?<!\r)\n", "`r`n" } else { $Content }
    [IO.File]::WriteAllText($full, $text, [Text.UTF8Encoding]::new($false))
}

function Save-SDCommit {
    param([Parameter(Mandatory)][string]$Root, [Parameter(Mandatory)][string]$Message)
    [void](& git -C $Root add -A 2>&1)
    [void](& git -C $Root commit -m $Message --quiet 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "Failed to commit in test repository: $Root" }
}

function New-SDDiagnosticReport {
    # $Diagnostics: array of hashtables {RelPath, Line (1-based), Code, Severity, Message}
    param([object[]]$Diagnostics, [string]$Path)
    $byFile = [ordered]@{}
    foreach ($d in $Diagnostics) {
        if (-not $byFile.Contains($d.RelPath)) { $byFile[$d.RelPath] = [Collections.Generic.List[object]]::new() }
        $byFile[$d.RelPath].Add([ordered]@{
                range              = [ordered]@{ start = [ordered]@{ line = $d.Line - 1; character = 0 }; end = [ordered]@{ line = $d.Line - 1; character = 20 } }
                severity           = $d.Severity
                code               = $d.Code
                source             = 'bsl-language-server'
                message            = $d.Message
                tags               = $null
                relatedInformation = $null
            })
    }
    $fileinfos = @()
    foreach ($key in $byFile.Keys) { $fileinfos += [ordered]@{ path = "file:///$key"; mdoRef = $null; diagnostics = @($byFile[$key]) } }
    $report = [ordered]@{ date = '2026-09-26T00:00:00Z'; sourceDir = $null; fileinfos = $fileinfos }
    $parent = Split-Path -Parent $Path
    if ($parent) { [void][IO.Directory]::CreateDirectory($parent) }
    ($report | ConvertTo-Json -Depth 10) | Set-Content -LiteralPath $Path -Encoding UTF8
    return $Path
}

function Invoke-SDMain {
    # Hashtable splatting (`@Arguments`) binds by parameter NAME; a flat array splat would bind
    # positionally instead and silently misassign values, so this must stay a hashtable splat.
    param([hashtable]$Arguments)
    $output = & $script:mainScript @Arguments
    $exitCode = $LASTEXITCODE
    return [pscustomobject]@{ Verdict = $output; ExitCode = $exitCode }
}

$workRoot = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-static-diff-test-' + [guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($workRoot)

try {
    # --- Scenario 1: legacy diagnostics only -> PASS ---
    $repo1 = New-SDTestRepo (Join-Path $workRoot 'legacy-only')
    Write-SDFile $repo1 'src/Module.bsl' "Процедура Тест()`n	Сообщить(""привет"");`nКонецПроцедуры`n"
    Save-SDCommit $repo1 'baseline'
    # Trailing content is appended after the diagnostic's own line window so the change registers
    # as a modified file without disturbing the two lines the diagnostic key is hashed from.
    Write-SDFile $repo1 'src/Module.bsl' "Процедура Тест()`n	Сообщить(""привет"");`nКонецПроцедуры`n`n// unrelated trailing comment`n"
    $baseline1 = New-SDDiagnosticReport @(@{RelPath = 'src/Module.bsl'; Line = 2; Code = 'UsingServiceTag'; Severity = 'Warning'; Message = 'legacy smell   with   spaces' }) (Join-Path $repo1 'baseline.json')
    $current1 = New-SDDiagnosticReport @(@{RelPath = 'src/Module.bsl'; Line = 2; Code = 'UsingServiceTag'; Severity = 'Warning'; Message = 'legacy smell with spaces' }) (Join-Path $repo1 'current.json')
    $result1 = Invoke-SDMain @{ ProjectPath = $repo1; BaselineReport = $baseline1; CurrentReport = $current1 }
    Assert-SDTest ($result1.ExitCode -eq 0) 'legacy-only: exit code must be 0'
    Assert-SDTest ($result1.Verdict.verdict -eq 'PASS') 'legacy-only: verdict must be PASS'
    Assert-SDTest ((@($result1.Verdict.new)).Count -eq 0) 'legacy-only: no new diagnostics expected (whitespace-normalized message must match)'
    Assert-SDTest ($result1.Verdict.legacy_count -eq 1) 'legacy-only: legacy_count must be 1'

    # --- Scenario 2: new error -> FAIL ---
    $repo2 = New-SDTestRepo (Join-Path $workRoot 'new-error')
    Write-SDFile $repo2 'src/Module.bsl' "Функция Тест()`n	Возврат Ложь;`nКонецФункции`n"
    Save-SDCommit $repo2 'baseline'
    Write-SDFile $repo2 'src/Module.bsl' "Функция Тест()`n	// broken`nКонецФункции`n"
    $baseline2 = New-SDDiagnosticReport @() (Join-Path $repo2 'baseline.json')
    $current2 = New-SDDiagnosticReport @(@{RelPath = 'src/Module.bsl'; Line = 2; Code = 'FunctionShouldHaveReturnDiagnostic'; Severity = 'Error'; Message = 'Function should always return a value' }) (Join-Path $repo2 'current.json')
    $result2 = Invoke-SDMain @{ ProjectPath = $repo2; BaselineReport = $baseline2; CurrentReport = $current2 }
    Assert-SDTest ($result2.ExitCode -eq 1) 'new-error: exit code must be 1'
    Assert-SDTest ($result2.Verdict.verdict -eq 'FAIL') 'new-error: verdict must be FAIL'
    Assert-SDTest ((@($result2.Verdict.new)).Count -eq 1) 'new-error: exactly one new diagnostic'
    Assert-SDTest ((@($result2.Verdict.new))[0].severity -eq 'Error') 'new-error: new diagnostic must be Error severity'

    # --- Scenario 3: new warning only -> PASS, but reported ---
    $repo3 = New-SDTestRepo (Join-Path $workRoot 'new-warning')
    Write-SDFile $repo3 'src/Module.bsl' "Процедура Тест()`nКонецПроцедуры`n"
    Save-SDCommit $repo3 'baseline'
    Write-SDFile $repo3 'src/Module.bsl' "Процедура Тест()`n	Сообщить(""debug"");`nКонецПроцедуры`n"
    $baseline3 = New-SDDiagnosticReport @() (Join-Path $repo3 'baseline.json')
    $current3 = New-SDDiagnosticReport @(@{RelPath = 'src/Module.bsl'; Line = 2; Code = 'UsingServiceTag'; Severity = 'Warning'; Message = 'debug output left in code' }) (Join-Path $repo3 'current.json')
    $result3 = Invoke-SDMain @{ ProjectPath = $repo3; BaselineReport = $baseline3; CurrentReport = $current3 }
    Assert-SDTest ($result3.ExitCode -eq 0) 'new-warning: exit code must be 0 (warning does not fail the gate)'
    Assert-SDTest ($result3.Verdict.verdict -eq 'PASS') 'new-warning: verdict must be PASS'
    Assert-SDTest ((@($result3.Verdict.new)).Count -eq 1) 'new-warning: new diagnostic must still be listed'

    # --- Scenario 4: line shift of a legacy diagnostic -> PASS, not new ---
    $repo4 = New-SDTestRepo (Join-Path $workRoot 'line-shift')
    Write-SDFile $repo4 'src/Module.bsl' "Процедура Тест()`n	Сообщить(""старый код"");`nКонецПроцедуры`n"
    Save-SDCommit $repo4 'baseline'
    Write-SDFile $repo4 'src/Module.bsl' "Процедура Тест()`n	// добавили строку выше`n	// и еще одну`n	Сообщить(""старый код"");`nКонецПроцедуры`n"
    $baseline4 = New-SDDiagnosticReport @(@{RelPath = 'src/Module.bsl'; Line = 2; Code = 'UsingServiceTag'; Severity = 'Warning'; Message = 'legacy call site' }) (Join-Path $repo4 'baseline.json')
    $current4 = New-SDDiagnosticReport @(@{RelPath = 'src/Module.bsl'; Line = 4; Code = 'UsingServiceTag'; Severity = 'Warning'; Message = 'legacy call site' }) (Join-Path $repo4 'current.json')
    $result4 = Invoke-SDMain @{ ProjectPath = $repo4; BaselineReport = $baseline4; CurrentReport = $current4 }
    Assert-SDTest ($result4.ExitCode -eq 0) 'line-shift: exit code must be 0'
    Assert-SDTest ($result4.Verdict.verdict -eq 'PASS') 'line-shift: verdict must be PASS'
    Assert-SDTest ((@($result4.Verdict.new)).Count -eq 0) 'line-shift: a shifted legacy diagnostic must not be reported as new'
    Assert-SDTest ($result4.Verdict.legacy_count -eq 1) 'line-shift: shifted diagnostic must still count as legacy'

    # --- Scenario 5: duplicate identical diagnostics count increase -> FAIL when severity is Error ---
    $repo5 = New-SDTestRepo (Join-Path $workRoot 'duplicate-increase')
    Write-SDFile $repo5 'src/Module.bsl' "Функция А()`n	Возврат Истина;`nКонецФункции`nФункция Б()`n	Возврат Истина;`nКонецФункции`n"
    Save-SDCommit $repo5 'baseline'
    # Trailing content registers the file as changed without disturbing either diagnostic's window.
    Write-SDFile $repo5 'src/Module.bsl' "Функция А()`n	Возврат Истина;`nКонецФункции`nФункция Б()`n	Возврат Истина;`nКонецФункции`n`n// unrelated trailing comment`n"
    $baseline5 = New-SDDiagnosticReport @(@{RelPath = 'src/Module.bsl'; Line = 2; Code = 'SameCode'; Severity = 'Error'; Message = 'duplicate finding' }) (Join-Path $repo5 'baseline.json')
    $current5 = New-SDDiagnosticReport @(
        @{RelPath = 'src/Module.bsl'; Line = 2; Code = 'SameCode'; Severity = 'Error'; Message = 'duplicate finding' },
        @{RelPath = 'src/Module.bsl'; Line = 5; Code = 'SameCode'; Severity = 'Error'; Message = 'duplicate finding' }
    ) (Join-Path $repo5 'current.json')
    $result5 = Invoke-SDMain @{ ProjectPath = $repo5; BaselineReport = $baseline5; CurrentReport = $current5 }
    Assert-SDTest ($result5.ExitCode -eq 1) 'duplicate-increase: exit code must be 1'
    Assert-SDTest ($result5.Verdict.verdict -eq 'FAIL') 'duplicate-increase: verdict must be FAIL'
    Assert-SDTest ($result5.Verdict.legacy_count -eq 1) 'duplicate-increase: one occurrence must match the baseline'
    Assert-SDTest ((@($result5.Verdict.new)).Count -eq 1) 'duplicate-increase: exactly one extra occurrence must be new'

    # --- Scenario 6: no changed files -> PASS with empty lists, no reports/tool needed ---
    $repo6 = New-SDTestRepo (Join-Path $workRoot 'no-changes')
    Write-SDFile $repo6 'src/Module.bsl' "Процедура Тест()`nКонецПроцедуры`n"
    Write-SDFile $repo6 'readme.txt' "not bsl`n"
    Save-SDCommit $repo6 'baseline'
    $result6 = Invoke-SDMain @{ ProjectPath = $repo6 }
    Assert-SDTest ($result6.ExitCode -eq 0) 'no-changes: exit code must be 0'
    Assert-SDTest ($result6.Verdict.verdict -eq 'PASS') 'no-changes: verdict must be PASS'
    Assert-SDTest ($result6.Verdict.reason -eq 'no_changed_files') 'no-changes: reason must be no_changed_files'
    Assert-SDTest ((@($result6.Verdict.files)).Count -eq 0) 'no-changes: files list must be empty'

    # --- Scenario 7: missing tool without reports ---
    $repo7 = New-SDTestRepo (Join-Path $workRoot 'missing-tool')
    Write-SDFile $repo7 'src/Module.bsl' "Процедура Тест()`nКонецПроцедуры`n"
    Save-SDCommit $repo7 'baseline'
    Write-SDFile $repo7 'src/Module.bsl' "Процедура Тест()`n	Сообщить(""x"");`nКонецПроцедуры`n"

    $gitCommand = Get-Command git -ErrorAction Stop
    $gitDir = Split-Path -Parent $gitCommand.Source
    $savedPath = $env:PATH
    $savedBslLsEnv = $env:BSL_FLOW_BSLLS
    $savedUserProfile = $env:USERPROFILE
    $savedHome = $env:HOME
    $isolatedHome = Join-Path $workRoot 'isolated-home'
    [void][IO.Directory]::CreateDirectory($isolatedHome)
    try {
        $env:PATH = $gitDir
        $env:BSL_FLOW_BSLLS = $null
        $env:USERPROFILE = $isolatedHome
        $env:HOME = $isolatedHome

        $result7a = Invoke-SDMain @{ ProjectPath = $repo7 }
        Assert-SDTest ($result7a.ExitCode -eq 0) 'missing-tool (not required): exit code must be 0'
        Assert-SDTest ($result7a.Verdict.verdict -eq 'NOT_RUN') 'missing-tool (not required): verdict must be NOT_RUN'
        Assert-SDTest ($result7a.Verdict.reason -eq 'bslls_not_found') 'missing-tool (not required): reason must be bslls_not_found'

        $result7b = Invoke-SDMain @{ ProjectPath = $repo7; Required = $true }
        Assert-SDTest ($result7b.ExitCode -eq 11) 'missing-tool (required): exit code must be 11'
        Assert-SDTest ($result7b.Verdict.verdict -eq 'BLOCKED') 'missing-tool (required): verdict must be BLOCKED'
    }
    finally {
        $env:PATH = $savedPath
        $env:BSL_FLOW_BSLLS = $savedBslLsEnv
        $env:USERPROFILE = $savedUserProfile
        $env:HOME = $savedHome
    }

    # --- Scenario 8: key stability across CRLF/LF ---
    $repo8 = New-SDTestRepo (Join-Path $workRoot 'crlf-lf')
    Write-SDFile $repo8 'src/Module.bsl' "Процедура Тест()`n	Сообщить(""значение"");`nКонецПроцедуры`n" -Crlf
    Save-SDCommit $repo8 'baseline'
    Write-SDFile $repo8 'src/Module.bsl' "Процедура Тест()`n	Сообщить(""значение"");`nКонецПроцедуры`n"
    $baseline8 = New-SDDiagnosticReport @(@{RelPath = 'src/Module.bsl'; Line = 2; Code = 'UsingServiceTag'; Severity = 'Warning'; Message = 'crlf baseline' }) (Join-Path $repo8 'baseline.json')
    $current8 = New-SDDiagnosticReport @(@{RelPath = 'src/Module.bsl'; Line = 2; Code = 'UsingServiceTag'; Severity = 'Warning'; Message = 'crlf baseline' }) (Join-Path $repo8 'current.json')
    $result8 = Invoke-SDMain @{ ProjectPath = $repo8; BaselineReport = $baseline8; CurrentReport = $current8 }
    Assert-SDTest ($result8.ExitCode -eq 0) 'crlf-lf: exit code must be 0'
    Assert-SDTest ($result8.Verdict.verdict -eq 'PASS') 'crlf-lf: verdict must be PASS'
    Assert-SDTest ((@($result8.Verdict.new)).Count -eq 0) 'crlf-lf: identical logical line must match across CRLF/LF baseline vs current'

    # --- Scenario 9: invalid BaseRef is BLOCKED with an evidence report, never no-change PASS.
    $repo9 = New-SDTestRepo (Join-Path $workRoot 'invalid-base-ref')
    Write-SDFile $repo9 'src/Module.bsl' "Процедура Тест()`nКонецПроцедуры`n"
    Save-SDCommit $repo9 'baseline'
    $result9 = Invoke-SDMain @{ ProjectPath = $repo9; BaseRef = 'definitely-not-a-commit' }
    Assert-SDTest ($result9.ExitCode -eq 11) 'invalid-base-ref: exit code must be 11'
    Assert-SDTest ($result9.Verdict.verdict -eq 'BLOCKED' -and $result9.Verdict.reason -eq 'invalid_base_ref') 'invalid-base-ref: must return BLOCKED evidence'

    # --- Scenario 10: a changed Cyrillic path is observed from stdout, not corrupted by Git stderr.
    $repo10 = New-SDTestRepo (Join-Path $workRoot 'cyrillic-path')
    $cyrillicPath = 'src/ОбщийМодуль.bsl'
    Write-SDFile $repo10 $cyrillicPath "Процедура Тест()`nКонецПроцедуры`n"
    Save-SDCommit $repo10 'baseline'
Write-SDFile $repo10 $cyrillicPath "Процедура Тест()`n`tСообщить(""изменено"");`nКонецПроцедуры`n"
    $baseline10 = New-SDDiagnosticReport @() (Join-Path $repo10 'baseline.json')
    $current10 = New-SDDiagnosticReport @() (Join-Path $repo10 'current.json')
    $result10 = Invoke-SDMain @{ ProjectPath = $repo10; BaselineReport = $baseline10; CurrentReport = $current10 }
    Assert-SDTest ($result10.ExitCode -eq 0 -and $result10.Verdict.verdict -eq 'PASS') 'cyrillic-path: fixture reports must run without BSL LS'
    Assert-SDTest (@($result10.Verdict.files) -contains $cyrillicPath) 'cyrillic-path: changed BSL file was not observed verbatim'

    # --- Scenario 11 (optional): a real BSL LS run when one is actually available ---
    $realCommand = $null
    if (-not [string]::IsNullOrWhiteSpace($env:BSL_FLOW_BSLLS) -and (Test-Path -LiteralPath $env:BSL_FLOW_BSLLS -PathType Leaf)) { $realCommand = $env:BSL_FLOW_BSLLS }
    else { $onPath = Get-Command 'bsl-language-server' -ErrorAction SilentlyContinue; if ($onPath) { $realCommand = $onPath.Source } }
    if ($null -eq $realCommand) {
        Write-Host 'SKIP: real BSL LS run (no jar/executable found via BSL_FLOW_BSLLS or PATH).'
    }
    else {
        $repo9 = New-SDTestRepo (Join-Path $workRoot 'real-run')
        Write-SDFile $repo9 'src/Module.bsl' "Процедура Тест()`nКонецПроцедуры`n"
        Save-SDCommit $repo9 'baseline'
        Write-SDFile $repo9 'src/Module.bsl' "Процедура Тест()`n	Сообщить(""реальный прогон"");`nКонецПроцедуры`n"
        $result9 = Invoke-SDMain @{ ProjectPath = $repo9; BslLsCommand = $realCommand }
        Assert-SDTest ($result9.Verdict.schema_version -eq 1) 'real-run: verdict must carry schema_version 1'
        Assert-SDTest ($result9.Verdict.verdict -in @('PASS', 'FAIL')) 'real-run: verdict must be a real outcome, not NOT_RUN/BLOCKED'
    }

    Write-Host "Test-1CStaticDiff: $($script:checks.Count) checks passed."
}
finally {
    Remove-Item -LiteralPath $workRoot -Recurse -Force -ErrorAction SilentlyContinue
}
