#Requires -Version 7.0
# Test double for a real 1cv8.exe: records the exact argument vector it was invoked with (so
# tests can assert on argument construction) and, for /DumpCfg, creates the target file (so
# build.cf/build.cfe's own "did it produce an artifact" check can pass). Never touches a real
# database. $args is populated automatically because this script declares no formal parameters.
if ($env:BF_MOCK_RECORD) { (($args -join ' ')) | Add-Content -LiteralPath $env:BF_MOCK_RECORD -Encoding utf8 }
for ($i = 0; $i -lt $args.Count; $i++) {
    if ($args[$i] -eq '/DumpCfg' -and ($i + 1) -lt $args.Count) { New-Item -ItemType File -Path $args[$i + 1] -Force | Out-Null }
    if ($args[$i] -eq '/Out' -and ($i + 1) -lt $args.Count) { Set-Content -LiteralPath $args[$i + 1] -Value 'mock-log' -Encoding utf8 }
}
if ($env:BF_MOCK_JUNIT) {
    foreach ($argument in $args) {
        if ($argument -like 'RunUnitTests=*') {
            $config = Get-Content -LiteralPath $argument.Substring(13) -Raw | ConvertFrom-Json
            Set-Content -LiteralPath $config.reportPath -Value $env:BF_MOCK_JUNIT -Encoding utf8
        }
    }
}
if ($env:BF_MOCK_FAIL_LOAD -and ($args -contains '/LoadCfg' -or $args -contains '/LoadConfigFromFiles')) { exit 1 }
exit 0
