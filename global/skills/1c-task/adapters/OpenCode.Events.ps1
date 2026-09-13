#Requires -Version 7.0
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'

function Read-BFOpenCodeEvents {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string]$Path,
        [Parameter(Mandatory)][int]$ExitCode,
        [string[]]$AllowedTools=@()
    )

    if($ExitCode -ne 0){throw 'BF_BLOCKED: OpenCode process failed.'}
    if(-not [IO.Path]::IsPathRooted($Path)){throw 'BF_BLOCKED: OpenCode events path must be absolute.'}
    $fullPath=Assert-BFSafePath $Path
    if(-not [IO.File]::Exists($fullPath)){throw 'BF_BLOCKED: OpenCode events file is missing.'}
    $raw=[IO.File]::ReadAllBytes($fullPath)
    if($raw.Length -eq 0 -or $raw.Length -gt 16777216){throw 'BF_BLOCKED: empty or oversized OpenCode event stream.'}
    try {$text=[Text.UTF8Encoding]::new($false,$true).GetString($raw)}catch{throw 'BF_BLOCKED: OpenCode event stream is not UTF-8.'}
    if(-not $text.EndsWith("`n")){throw 'BF_BLOCKED: torn OpenCode event stream.'}
    $lines=@($text.TrimEnd("`r","`n").Split("`n"))
    if($lines.Count -eq 0){throw 'BF_BLOCKED: OpenCode event stream is empty.'}

    $events=@()
    foreach($line in $lines){
        if([string]::IsNullOrWhiteSpace($line)){throw 'BF_BLOCKED: blank OpenCode event line.'}
        if((Test-BFJsonSyntax $line) -ne 'object'){throw 'BF_BLOCKED: OpenCode event must be a JSON object.'}
        try {$events+=ConvertFrom-Json -InputObject $line -ErrorAction Stop}catch{throw 'BF_BLOCKED: OpenCode event cannot be materialized.'}
    }

    $allowed=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach($tool in $AllowedTools){if([string]::IsNullOrWhiteSpace($tool)){throw 'BF_BLOCKED: invalid allowed OpenCode tool.'};[void]$allowed.Add($tool)}
    $partIds=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    $callIds=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    $messageIds=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    $session=$null;$previousTimestamp=$null;$current=$null;$stopped=$false;$steps=@();$toolCalls=@()
    $usage=[ordered]@{total=[decimal]0;input=[decimal]0;output=[decimal]0;reasoning=[decimal]0;cache=[ordered]@{read=[decimal]0;write=[decimal]0}}
    $cost=[decimal]0

    function Assert-BFOpenCodeId([object]$Value,[string]$Prefix,[string]$Name){
        $suffix=if($Prefix -eq 'call_'){'[A-Za-z0-9_]+'}else{'[A-Za-z0-9]+'}
        if($Value -isnot [string] -or $Value.Length -gt 256 -or $Value -cnotmatch ('^'+[regex]::Escape($Prefix)+$suffix+'$')){throw "BF_BLOCKED: invalid OpenCode $Name identity."}
    }
    function Get-BFOpenCodeNumber([object]$Value,[string]$Name){
        if($null -eq $Value -or $Value -is [bool] -or $Value -isnot [byte] -and $Value -isnot [int16] -and $Value -isnot [int] -and $Value -isnot [int64] -and $Value -isnot [single] -and $Value -isnot [double] -and $Value -isnot [decimal]){throw "BF_BLOCKED: invalid OpenCode $Name."}
        try {$number=[decimal]$Value}catch{throw "BF_BLOCKED: invalid OpenCode $Name."}
        if($number -lt 0){throw "BF_BLOCKED: negative OpenCode $Name."};return $number
    }
    function Get-BFOpenCodeField([object]$Object,[string]$Name,[string]$Context){
        if($null -eq $Object -or -not (Test-BFObjectProperty $Object $Name)){throw "BF_BLOCKED: missing OpenCode $Context.$Name."}
        return Get-BFObjectProperty $Object $Name
    }

    foreach($event in $events){
        $eventType=Get-BFOpenCodeField $event 'type' 'event';$part=Get-BFOpenCodeField $event 'part' 'event';$timestamp=Get-BFOpenCodeField $event 'timestamp' 'event';$eventSession=Get-BFOpenCodeField $event 'sessionID' 'event'
        if($eventType -isnot [string] -or $part -isnot [pscustomobject]){throw 'BF_BLOCKED: malformed OpenCode event.'}
        if(($timestamp -isnot [int] -and $timestamp -isnot [int64]) -or $timestamp -lt 0){throw 'BF_BLOCKED: invalid OpenCode timestamp.'}
        if($null -ne $previousTimestamp -and $timestamp -lt $previousTimestamp){throw 'BF_BLOCKED: non-monotonic OpenCode timestamp.'};$previousTimestamp=$timestamp
        if($null -eq $session){$session=$eventSession;Assert-BFOpenCodeId $session 'ses_' 'session'}
        if($eventSession -cne $session -or (Get-BFOpenCodeField $part 'sessionID' 'part') -cne $session){throw 'BF_BLOCKED: mixed OpenCode sessions.'}
        $partId=Get-BFOpenCodeField $part 'id' 'part';Assert-BFOpenCodeId $partId 'prt_' 'part'
        if(-not $partIds.Add($partId)){throw 'BF_BLOCKED: duplicate OpenCode part identity.'}
        if($stopped){throw 'BF_BLOCKED: OpenCode event follows terminal stop.'}

        switch($eventType){
            'step_start' {
                $messageId=Get-BFOpenCodeField $part 'messageID' 'part'
                if($null -ne $current -or (Get-BFOpenCodeField $part 'type' 'part') -cne 'step-start'){throw 'BF_BLOCKED: invalid OpenCode step start.'}
                Assert-BFOpenCodeId $messageId 'msg_' 'message'
                if(-not $messageIds.Add($messageId)){throw 'BF_BLOCKED: duplicate OpenCode step message.'}
                $current=[ordered]@{message_id=$messageId;activities=@();finish=$null}
            }
            'text' {
                $messageId=Get-BFOpenCodeField $part 'messageID' 'part';$partText=Get-BFOpenCodeField $part 'text' 'part'
                if($null -eq $current -or (Get-BFOpenCodeField $part 'type' 'part') -cne 'text' -or $messageId -cne $current.message_id -or $partText -isnot [string]){throw 'BF_BLOCKED: invalid OpenCode text event.'}
                $current.activities+=,[ordered]@{kind='text';text=$partText}
            }
            'tool_use' {
                $messageId=Get-BFOpenCodeField $part 'messageID' 'part';$name=Get-BFOpenCodeField $part 'tool' 'part';$callId=Get-BFOpenCodeField $part 'callID' 'part';$state=Get-BFOpenCodeField $part 'state' 'part'
                if($null -eq $current -or (Get-BFOpenCodeField $part 'type' 'part') -cne 'tool' -or $messageId -cne $current.message_id){throw 'BF_BLOCKED: invalid OpenCode tool event.'}
                Assert-BFOpenCodeId $callId 'call_' 'tool call'
                if(-not $allowed.Contains($name)){throw 'BF_BLOCKED: OpenCode tool is not allowlisted.'}
                if(-not $callIds.Add($callId)){throw 'BF_BLOCKED: duplicate OpenCode tool call identity.'}
                # Tool error states are retained in raw JSONL but rejected until a
                # controller contract demonstrates safe error/result reconciliation.
                if((Get-BFOpenCodeField $state 'status' 'tool state') -cne 'completed'){throw 'BF_BLOCKED: OpenCode tool did not complete successfully.'}
                $current.activities+=,[ordered]@{kind='tool';name=$name;call_id=$callId}
                $toolCalls+=,[ordered]@{name=$name;call_id=$callId;message_id=$current.message_id;part_id=$partId}
            }
            'step_finish' {
                $messageId=Get-BFOpenCodeField $part 'messageID' 'part';$reason=Get-BFOpenCodeField $part 'reason' 'part';$tokens=Get-BFOpenCodeField $part 'tokens' 'part';$cache=Get-BFOpenCodeField $tokens 'cache' 'tokens'
                if($null -eq $current -or (Get-BFOpenCodeField $part 'type' 'part') -cne 'step-finish' -or $messageId -cne $current.message_id -or $current.activities.Count -eq 0){throw 'BF_BLOCKED: invalid OpenCode step finish.'}
                if($reason -cnotin @('tool-calls','stop')){throw 'BF_BLOCKED: unknown OpenCode step finish reason.'}
                foreach($pair in @(@('total',(Get-BFOpenCodeField $tokens 'total' 'tokens')),@('input',(Get-BFOpenCodeField $tokens 'input' 'tokens')),@('output',(Get-BFOpenCodeField $tokens 'output' 'tokens')),@('reasoning',(Get-BFOpenCodeField $tokens 'reasoning' 'tokens')),@('cache.read',(Get-BFOpenCodeField $cache 'read' 'tokens.cache')),@('cache.write',(Get-BFOpenCodeField $cache 'write' 'tokens.cache')))){$number=Get-BFOpenCodeNumber $pair[1] $pair[0];switch($pair[0]){'total'{$usage.total+=$number}'input'{$usage.input+=$number}'output'{$usage.output+=$number}'reasoning'{$usage.reasoning+=$number}'cache.read'{$usage.cache.read+=$number}'cache.write'{$usage.cache.write+=$number}}}
                $cost+=(Get-BFOpenCodeNumber (Get-BFOpenCodeField $part 'cost' 'part') 'cost')
                $current.finish=$reason;$steps+=,$current
                if($reason -eq 'stop'){$stopped=$true};$current=$null
            }
            default {throw 'BF_BLOCKED: unknown OpenCode event type.'}
        }
    }
    if($null -ne $current -or -not $stopped -or $steps.Count -eq 0){throw 'BF_BLOCKED: incomplete OpenCode step sequence.'}
    for($index=0;$index -lt $steps.Count-1;$index++){if($steps[$index].finish -cne 'tool-calls'){throw 'BF_BLOCKED: non-terminal OpenCode step must end in tool-calls.'}}
    $last=$steps[-1]
    if($last.finish -cne 'stop' -or $last.activities.Count -ne 1 -or $last.activities[0].kind -cne 'text'){throw 'BF_BLOCKED: terminal OpenCode step must contain exactly one text result.'}
    $finalText=$last.activities[0].text
    if((Test-BFJsonSyntax $finalText) -ne 'object'){throw 'BF_BLOCKED: final OpenCode text is not a JSON object.'}
    try {$result=ConvertFrom-Json -InputObject $finalText -ErrorAction Stop}catch{throw 'BF_BLOCKED: final OpenCode result cannot be materialized.'}
    Assert-BFFields $result @('schema_version','status','summary','payload_json') @() 'opencode_result'
    if(($result.schema_version -isnot [int] -and $result.schema_version -isnot [int64]) -or $result.schema_version -ne 1 -or $result.status -notin @('completed','needs_input','blocked','failed')){throw 'BF_BLOCKED: invalid structured OpenCode result.'}
    Assert-BFText $result.summary 'opencode_result.summary'
    if($result.payload_json -isnot [string] -or (Test-BFJsonSyntax $result.payload_json) -notin @('object','array')){throw 'BF_BLOCKED: invalid OpenCode payload_json.'}
    try {[void](ConvertFrom-Json -InputObject $result.payload_json -ErrorAction Stop)}catch{throw 'BF_BLOCKED: OpenCode payload_json cannot be materialized.'}
    return [ordered]@{result=$result;metadata=[ordered]@{session_id=$session;usage=$usage;reported_cost_usd=$cost;cost_source='opencode.step_finish.part.cost.sum';observed_model=$null;observed_effort=$null;step_count=$steps.Count;tool_calls=@($toolCalls)}}
}
