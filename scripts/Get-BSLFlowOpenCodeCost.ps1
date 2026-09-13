#Requires -Version 7.0
[CmdletBinding()]
param([Parameter(Mandatory)][string[]]$Paths)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
. (Join-Path (Split-Path $PSScriptRoot -Parent) 'global/skills/1c-task/scripts/Task.Storage.ps1')
$sessions=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
$rows=@();$total=[decimal]0
foreach($path in $Paths){
    $path=Assert-BFSafePath $path
    $cost=[decimal]0;$session=$null;$steps=0;$terminal=$false
    $parts=[Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach($line in [IO.File]::ReadLines($path)){
        if((Test-BFJsonSyntax $line) -ne 'object'){throw 'Cost evidence contains an invalid JSON event.'}
        $json=[Text.Json.JsonDocument]::Parse($line)
        try {
            $event=$json.RootElement
            $id=$event.GetProperty('sessionID').GetString()
            if($null -eq $session){$session=$id}elseif($session -cne $id){throw 'Mixed session in cost evidence.'}
            if($event.GetProperty('type').GetString() -ceq 'step_finish'){
                $part=$event.GetProperty('part')
                if($terminal -or -not $parts.Add($part.GetProperty('id').GetString())){throw 'Repeated or post-terminal cost event.'}
                $amount=$part.GetProperty('cost').GetDecimal()
                if($amount -lt 0){throw 'Negative cost in evidence.'}
                $cost+=$amount;$steps++
                if($part.GetProperty('reason').GetString() -ceq 'stop'){$terminal=$true}
            }
        }finally{$json.Dispose()}
    }
    if([string]::IsNullOrWhiteSpace($session) -or -not $sessions.Add($session)){throw 'Missing or duplicate session; refuse double counting.'}
    $total+=$cost
    $rows+=[ordered]@{session_id=$session;reported_cost_usd=$cost.ToString([Globalization.CultureInfo]::InvariantCulture);reported_steps=$steps;terminal_observed=$terminal;evidence_sha256=Get-BFFileHash $path}
}
[ordered]@{schema_version=1;source='opencode.step_finish.part.cost';billed_cost_verified=$false;task_acceptance=$false;reported_total_usd=$total.ToString([Globalization.CultureInfo]::InvariantCulture);sessions=$rows}|ConvertTo-Json -Depth 6
