#Requires -Version 7.0
# Static workflow contract; execution on each OS is separate CI evidence.
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$root=Split-Path $PSScriptRoot -Parent
$contracts=@{
    core=@('windows-2025','ubuntu-latest','macos-latest','Test-CorePackage.ps1','Test-OnboardingExamples')
    managed=@('windows-2025','Build-BSLFlowPackage.ps1','-Package full -Test','-Package managed -Test')
    'hosts-opencode'=@('windows-2025','Test-OpenCodeAdapter','Test-BSLFlowOpenCodeWorker')
    'hosts-claude'=@('windows-2025','Test-ClaudeHooks','Test-ClaudeCodeAdapter','Test-CurrentAgentMode')
}
foreach($name in $contracts.Keys) {
    $path=Join-Path $root ".github/workflows/$name.yml"
    $text=Get-Content -Raw -LiteralPath $path
    foreach($required in $contracts[$name]) { if (-not $text.Contains($required)) {throw "$name missing $required"} }
    if ($text -match 'setup-go|go-version|native-cli\.yml|Build-BSLFlowCli|Test-BSLFlowCli') {throw "$name refers to removed native CLI"}
    if ($name -ne 'core' -and $text -match 'ubuntu-latest|macos-latest') {throw "$name must be Windows-only"}
}
if (Test-Path (Join-Path $root '.github/workflows/offline.yml')) {throw 'Replaced offline workflow still exists.'}
if (Test-Path (Join-Path $root '.github/workflows/native-cli.yml')) {throw 'Removed native workflow still exists.'}
'CI_MATRIX_OK'
