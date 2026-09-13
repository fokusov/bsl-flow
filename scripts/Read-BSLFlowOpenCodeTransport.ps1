#Requires -Version 7.0
[CmdletBinding()]
param([Parameter(Mandatory)][string]$EventsPath,[Parameter(Mandatory)][ValidateRange(0,255)][int]$ExitCode)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
. (Join-Path (Split-Path $PSScriptRoot -Parent) 'global/skills/1c-task/scripts/Task.Storage.ps1')
# This contract is deliberately limited to the measured no-tools transport probe.
# A transport receipt is never a task acceptance or a tool-isolation capability.
if($ExitCode -ne 0){throw 'BF_BLOCKED: OpenCode process failed.'}
$raw=[IO.File]::ReadAllBytes($EventsPath)
if($raw.Length -eq 0 -or $raw.Length -gt 1048576){throw 'BF_BLOCKED: empty or oversized OpenCode transport stream.'}
$text=[Text.UTF8Encoding]::new($false,$true).GetString($raw)
if(-not $text.EndsWith("`n")){throw 'BF_BLOCKED: torn OpenCode transport stream.'}
$lines=@($text.TrimEnd("`r","`n").Split("`n"))
if($lines.Count -ne 3){throw 'BF_BLOCKED: unverified event sequence; expected exactly one no-tools turn.'}
$events=@($lines | ForEach-Object { [void](Test-BFJsonSyntax $_); ConvertFrom-Json -InputObject $_ -ErrorAction Stop })
$types=@('step_start','text','step_finish');$partTypes=@('step-start','text','step-finish')
$session=$events[0].sessionID;$message=$events[0].part.messageID
if($session -isnot [string] -or $session -cnotmatch '^ses_[A-Za-z0-9]+$' -or $message -isnot [string] -or $message -cnotmatch '^msg_[A-Za-z0-9]+$'){throw 'BF_BLOCKED: missing OpenCode identity.'}
$ids=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
for($i=0;$i -lt 3;$i++){
    $event=$events[$i]
    if($event.type -cne $types[$i] -or $event.part.type -cne $partTypes[$i] -or $event.sessionID -cne $session -or $event.part.sessionID -cne $session -or $event.part.messageID -cne $message){throw 'BF_BLOCKED: inconsistent OpenCode event identity or ordering.'}
    if($event.part.id -isnot [string] -or $event.part.id -cnotmatch '^prt_[A-Za-z0-9]+$' -or -not $ids.Add($event.part.id)){throw 'BF_BLOCKED: invalid or duplicate OpenCode part identity.'}
    if(($event.timestamp -isnot [long] -and $event.timestamp -isnot [int]) -or $event.timestamp -lt 0 -or ($i -gt 0 -and $event.timestamp -lt $events[$i-1].timestamp)){throw 'BF_BLOCKED: invalid OpenCode event timestamp.'}
}
$finish=$events[2].part
if($finish.reason -cne 'stop'){throw 'BF_BLOCKED: OpenCode did not finish normally.'}
$tokens=$finish.tokens
foreach($value in @($tokens.total,$tokens.input,$tokens.output,$tokens.reasoning,$tokens.cache.read,$tokens.cache.write)){
    if(($value -isnot [long] -and $value -isnot [int]) -or $value -lt 0){throw 'BF_BLOCKED: invalid OpenCode token usage.'}
}
$cost=$finish.cost
if($cost -isnot [double] -and $cost -isnot [decimal] -and $cost -isnot [long] -and $cost -isnot [int]){throw 'BF_BLOCKED: missing OpenCode cost.'}
if([double]::IsNaN($cost) -or [double]::IsInfinity($cost) -or $cost -lt 0){throw 'BF_BLOCKED: invalid OpenCode cost.'}
[void](Test-BFJsonSyntax $events[1].part.text)
$result=ConvertFrom-Json -InputObject $events[1].part.text -ErrorAction Stop
if($result.schema_version -isnot [int] -and $result.schema_version -isnot [long]){throw 'BF_BLOCKED: schema_version must be an integer.'}
$fields=@($result.PSObject.Properties.Name)
if($fields.Count -ne 4 -or @($fields|Where-Object{$_ -cnotin @('schema_version','status','summary','payload_json')}).Count -ne 0 -or $result.schema_version -cne 1 -or $result.status -cnotin @('completed','needs_input','blocked','failed') -or $result.summary -isnot [string] -or [string]::IsNullOrWhiteSpace($result.summary) -or $result.payload_json -isnot [string]){throw 'BF_BLOCKED: invalid structured OpenCode result.'}
[void](Test-BFJsonSyntax $result.payload_json)
[void](ConvertFrom-Json -InputObject $result.payload_json -ErrorAction Stop)
[pscustomobject]@{schema_version=1;transport='opencode-no-tools-v1';session_id=$session;message_id=$message;raw_sha256=[Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($raw)).ToLowerInvariant();result=$result;usage=$tokens;reported_cost_usd=$cost;cost_source='opencode.step_finish.part.cost';observed_model=$null;task_acceptance=$false}
