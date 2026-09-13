#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$root=Split-Path $PSScriptRoot -Parent
. (Join-Path $root 'global/skills/1c-task/scripts/Task.Storage.ps1')
. (Join-Path $root 'global/skills/1c-task/scripts/Task.Contracts.ps1')
. (Join-Path $root 'global/skills/1c-task/adapters/OpenCode.Events.ps1')
$fixtures=Join-Path $PSScriptRoot 'fixtures/opencode'
$checks=0
function Assert-Blocked([scriptblock]$Action){try{& $Action;throw 'Expected BF_BLOCKED.'}catch{if($_.Exception.Message -notmatch 'BF_BLOCKED'){throw};$script:checks++}}
$noTools=Read-BFOpenCodeEvents -Path (Join-Path $fixtures 'no-tools.jsonl') -ExitCode 0
if($noTools.metadata.step_count -ne 1 -or $noTools.metadata.reported_cost_usd -ne [decimal]0.1 -or $noTools.metadata.tool_calls.Count -ne 0){throw 'No-tools fixture was parsed incorrectly.'};$checks++
$toolRun=Read-BFOpenCodeEvents -Path (Join-Path $fixtures 'read-write.jsonl') -ExitCode 0 -AllowedTools @('read','write')
if($toolRun.metadata.step_count -ne 3 -or $toolRun.metadata.tool_calls.Count -ne 2 -or $toolRun.metadata.usage.total -ne [decimal]60 -or $toolRun.metadata.reported_cost_usd -ne [decimal]0.6){throw 'Tool fixture usage was not summed correctly.'};$checks++
$scratch=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-opencode-events-'+[guid]::NewGuid().ToString('N'));[void][IO.Directory]::CreateDirectory($scratch)
try {
    $valid=[IO.File]::ReadAllText((Join-Path $fixtures 'read-write.jsonl'))
    foreach($case in @(
        @{name='duplicate-part';text=$valid.Replace('"prt_i"','"prt_h"')},
        @{name='duplicate-call';text=$valid.Replace('"call_b"','"call_a"')},
        @{name='mixed-session';text=$valid.Replace('"ses_fixture","part":{"id":"prt_e"','"ses_foreign","part":{"id":"prt_e"')},
        @{name='interleaved';text=$valid.Replace('"type":"tool_use","timestamp":5','"type":"step_start","timestamp":5')},
        @{name='after-stop';text=($valid + $valid.Split("`n")[0] + "`n")},
        @{name='truncated';text=$valid.TrimEnd("`r","`n")},
        @{name='foreign-tool';text=$valid.Replace('"tool":"write"','"tool":"bash"')},
        @{name='negative-usage';text=$valid.Replace('"total":20','"total":-20')},
        @{name='schema-bool';text=$valid.Replace('"schema_version":1','"schema_version":true')},
        @{name='tool-error';text=$valid.Replace('"status":"completed"','"status":"error"')}
    )) {
        $path=Join-Path $scratch ($case.name+'.jsonl');[IO.File]::WriteAllText($path,$case.text,[Text.UTF8Encoding]::new($false));Assert-Blocked { Read-BFOpenCodeEvents -Path $path -ExitCode 0 -AllowedTools @('read','write') | Out-Null }
    }
} finally {if(Test-Path -LiteralPath $scratch){Remove-Item -LiteralPath $scratch -Recurse -Force}}
Write-Output ('OPEN_CODE_EVENTS_OK checks='+$checks)
