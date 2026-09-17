#Requires -Version 7.0
$ErrorActionPreference='Stop'
$reader=Join-Path $PSScriptRoot 'Read-BSLFlowOpenCodeTransport.ps1'
$tmp=Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-events-'+[guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($tmp)
$path=Join-Path $tmp 'events.jsonl'
$events=@(
    @{type='step_start';timestamp=100;sessionID='ses_a';part=@{id='prt_a';messageID='msg_a';sessionID='ses_a';type='step-start'}},
    @{type='text';timestamp=101;sessionID='ses_a';part=@{id='prt_b';messageID='msg_a';sessionID='ses_a';type='text';text='{"schema_version":1,"status":"completed","summary":"ok","payload_json":"{}"}'}},
    @{type='step_finish';timestamp=101;sessionID='ses_a';part=@{id='prt_c';messageID='msg_a';sessionID='ses_a';type='step-finish';reason='stop';tokens=@{total=10;input=2;output=2;reasoning=1;cache=@{read=5;write=0}};cost=0.0001}}
)
$baseline=(@($events|ForEach-Object{$_|ConvertTo-Json -Depth 10 -Compress}) -join "`n")+"`n"
$checks=0
function Reject([string]$Text,[int]$Code=0){[IO.File]::WriteAllText($path,$Text);$rejected=$false;try{& $reader -EventsPath $path -ExitCode $Code|Out-Null}catch{$rejected=$true};if(-not $rejected){throw 'Expected transport rejection.'};$script:checks++}
try {
    [IO.File]::WriteAllText($path,$baseline)
    $receipt=& $reader -EventsPath $path -ExitCode 0
    if($receipt.task_acceptance -or $receipt.session_id -cne 'ses_a' -or $receipt.reported_cost_usd -ne 0.0001 -or $receipt.usage.cache.read -ne 5 -or $null -ne $receipt.observed_model){throw 'Incorrect transport receipt.'};$checks++
    Reject $baseline 1
    Reject $baseline.TrimEnd("`n")
    Reject ($baseline+$baseline)
    Reject ($baseline.Replace('step_finish','error'))
    Reject ($baseline.Replace('"reason":"stop"','"reason":"length"'))
    Reject ($baseline.Replace('"cost":0.0001','"cost":-1'))
    Reject ($baseline.Replace('"input":2','"input":-1'))
    $events[1].part.messageID='msg_b'
    Reject ((@($events|ForEach-Object{$_|ConvertTo-Json -Depth 10 -Compress}) -join "`n")+"`n")
    Reject ($baseline.Replace('"id":"prt_b"','"id":"prt_a"'))
    Reject ($baseline.Replace('"reason":"stop"','"reason":"error","reason":"stop"'))
    $events[1].part.messageID='msg_a'
    foreach($invalid in @('{"schema_version":"1","status":"completed","summary":"ok","payload_json":"{}"}','{"schema_version":true,"status":"completed","summary":"ok","payload_json":"{}"}','{"schema_version":1,"status":"failed","status":"completed","summary":"ok","payload_json":"{}"}','{"schema_version":1,"status":"completed","summary":"ok","payload_json":"{\"a\":1,\"a\":2}"}')){
        $events[1].part.text=$invalid
        Reject ((@($events|ForEach-Object{$_|ConvertTo-Json -Depth 10 -Compress}) -join "`n")+"`n")
    }
    Write-Output "OpenCode transport: $checks checks PASS."
} finally {
    if([IO.Path]::GetFullPath($tmp).StartsWith([IO.Path]::GetFullPath([IO.Path]::GetTempPath()),[StringComparison]::OrdinalIgnoreCase) -and (Split-Path $tmp -Leaf) -like 'bsl-flow-events-*'){Remove-Item -LiteralPath $tmp -Recurse -Force}
}
