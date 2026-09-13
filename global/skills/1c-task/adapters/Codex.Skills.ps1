#Requires -Version 7.0
Set-StrictMode -Version Latest

function Invoke-BFCodexReadOnlyRpc {
    param([string]$Executable,[string[]]$Overrides,[string]$WorkingDirectory,[string]$Directory,[string]$Method,[hashtable]$Params,[scriptblock]$Cancelled,[int]$TimeoutSeconds=60,[int]$MaxOutputBytes=16777216,[string[]]$ExpectedMcpServers,[string[]]$EnabledMcpServers=@())
    if($Method -notin @('skills/list','config/read','mcpServerStatus/list')){throw 'BF_INVALID: unsupported read-only Codex RPC.'}
    if($MaxOutputBytes -lt 65536 -or $MaxOutputBytes -gt 16777216){throw 'BF_INVALID: invalid RPC output bound.'}
    $deadline=Get-Variable -Name BFRunDeadlineUtc -ValueOnly -ErrorAction SilentlyContinue
    if($null -ne $deadline){$TimeoutSeconds=[math]::Min($TimeoutSeconds,[math]::Floor(($deadline-[DateTime]::UtcNow).TotalSeconds));if($TimeoutSeconds -le 0){throw 'BF_BLOCKED: task deadline reached before RPC dispatch.'}}
    if(Test-Path -LiteralPath $Directory){throw 'BF_BLOCKED: existing RPC attempt requires reconciliation.'}
    $guardMcp=$PSBoundParameters.ContainsKey('ExpectedMcpServers')
    if($Method -eq 'mcpServerStatus/list' -and -not $guardMcp){throw 'BF_BLOCKED: MCP inventory requires a same-process configuration guard.'}
    [void][IO.Directory]::CreateDirectory($Directory)
    $info=[Diagnostics.ProcessStartInfo]::new();$info.FileName=$Executable;$info.WorkingDirectory=$WorkingDirectory
    $info.UseShellExecute=$false;$info.CreateNoWindow=$true
    $info.RedirectStandardInput=$true;$info.RedirectStandardOutput=$true;$info.RedirectStandardError=$true
    $info.StandardInputEncoding=[Text.UTF8Encoding]::new($false);$info.StandardOutputEncoding=[Text.UTF8Encoding]::new($false)
    # Keep the real Codex home and SQLite state; neither contains managed copies.
    foreach($arg in (@($Overrides)+@('app-server','--stdio'))){$info.ArgumentList.Add($arg)}
    $process=[Diagnostics.Process]::new();$process.StartInfo=$info
    $stderr=[IO.File]::Open((Join-Path $Directory 'stderr.txt'),[IO.FileMode]::CreateNew)
    $watch=[Diagnostics.Stopwatch]::StartNew();$buffer=[char[]]::new(4096);$pending=[Text.StringBuilder]::new();$bytes=0;$phase=1;$result=$null;$identity=$null;$errorTask=$null;$primaryError=$null
    try {
        if($null -ne $Cancelled -and (& $Cancelled)){throw 'BF_BLOCKED: RPC cancelled before dispatch.'}
        [void]$process.Start()
        $identity=[ordered]@{pid=$process.Id;start_time_utc=$process.StartTime.ToUniversalTime().ToString('o');executable=$Executable}
        Write-BFJson (Join-Path $Directory 'process.json') $identity
        $errorTask=$process.StandardError.BaseStream.CopyToAsync($stderr)
        $initialize=@{id=1;method='initialize';params=@{clientInfo=@{name='bsl_flow_inventory';title='BSL Flow inventory';version='1'};capabilities=@{experimentalApi=$true}}}|ConvertTo-Json -Depth 10 -Compress
        $process.StandardInput.WriteLine($initialize);$process.StandardInput.Flush()
        $read=$process.StandardOutput.ReadAsync($buffer,0,$buffer.Length)
        while($null -eq $result){
            if($null -ne $Cancelled -and (& $Cancelled)){throw 'BF_BLOCKED: RPC cancelled.'}
            if($watch.Elapsed.TotalSeconds -ge $TimeoutSeconds){throw 'BF_BLOCKED: RPC timeout; preserve the attempt.'}
            if($stderr.Length -gt $MaxOutputBytes){throw 'BF_BLOCKED: RPC output limit.'}
            if(-not $read.Wait(100)){continue}
            $count=$read.GetAwaiter().GetResult()
            if($count -eq 0){throw 'BF_BLOCKED: RPC output closed before response.'}
            $chunk=[string]::new($buffer,0,$count);$bytes += [Text.Encoding]::UTF8.GetByteCount($chunk)
            if($bytes -gt $MaxOutputBytes){throw 'BF_BLOCKED: RPC output limit.'}
            [void]$pending.Append($chunk)
            while(($newline=$pending.ToString().IndexOf("`n")) -ge 0){
                $line=$pending.ToString(0,$newline);[void]$pending.Remove(0,$newline+1)
                $message=ConvertFrom-BFCodexRpcLine $line $Method $phase $Directory
                if($null -ne (Get-BFValue $message 'method')){continue}
                if((Get-BFValue $message 'id') -ne $phase){throw 'BF_BLOCKED: unexpected Codex RPC identity.'}
                if($null -ne (Get-BFValue $message 'error')){throw 'BF_BLOCKED: Codex RPC rejected the request.'}
                if($phase -eq 1){
                    $process.StandardInput.WriteLine('{"method":"initialized"}')
                    $request=if($guardMcp){@{id=2;method='config/read';params=@{cwd=$WorkingDirectory;includeLayers=$false}}}else{@{id=2;method=$Method;params=$Params}}
                    $request=$request|ConvertTo-Json -Depth 20 -Compress
                    $process.StandardInput.WriteLine($request);$process.StandardInput.Flush();$phase=2
                }elseif($phase -eq 2 -and $guardMcp){
                    Assert-BFCodexMcpConfiguration (Get-BFValue $message 'result') $ExpectedMcpServers $EnabledMcpServers
                    $request=@{id=3;method=$Method;params=$Params}|ConvertTo-Json -Depth 20 -Compress
                    $process.StandardInput.WriteLine($request);$process.StandardInput.Flush();$phase=3
                }else{$result=Get-BFValue $message 'result';if($null -eq $result){throw 'BF_BLOCKED: missing RPC result.'}}
            }
            if($null -eq $result){$read=$process.StandardOutput.ReadAsync($buffer,0,$buffer.Length)}
        }
        return $result
    } catch {$primaryError=$_;throw} finally {Close-BFCodexRpcProcess $process $identity $errorTask $stderr ($null -ne $primaryError)}
}

function ConvertFrom-BFCodexRpcLine {
    param([string]$Line,[string]$Method,[int]$Phase,[string]$Directory)
    try{return ConvertFrom-Json $Line -Depth 100 -ErrorAction Stop}catch{
        if($Method -ceq 'mcpServerStatus/list' -and $Phase -eq 3){
            # Never save protocol text or exception messages: they can contain
            # configuration values. Only the post-guard MCP phase gets metadata.
            $failure=[ordered]@{phase=$Phase;method=$Method;line_characters=$Line.Length;line_utf8_bytes=[Text.Encoding]::UTF8.GetByteCount($Line);line_sha256=(Get-BFHash $Line);error_id=($_.FullyQualifiedErrorId.Split(',')[0]);exception_type=$_.Exception.GetType().FullName;json_line=$null;json_position=$null}
            $exception=$_.Exception
            while($null -ne $exception){
                if($null -ne $exception.PSObject.Properties['LineNumber']){$failure.json_line=$exception.LineNumber}
                if($null -ne $exception.PSObject.Properties['LinePosition']){$failure.json_position=$exception.LinePosition}
                $exception=$exception.InnerException
            }
            Write-BFJson (Join-Path $Directory 'malformed-response.json') $failure
        }
        throw 'BF_BLOCKED: malformed Codex RPC.'
    }
}

function Close-BFCodexRpcProcess {
    param($Process,$Identity,$ErrorTask,$ErrorStream,[bool]$PreservePrimaryError)
    $cleanupError=$null
    try{
        if($null -ne $Identity){
            try{$Process.StandardInput.Close()}catch{$cleanupError=$_}
            $exited=$false
            try{$exited=$Process.WaitForExit(1000)}catch{if($null -eq $cleanupError){$cleanupError=$_}}
            if(-not $exited){try{Stop-BFOwnedProcess $Identity}catch{if($null -eq $cleanupError){$cleanupError=$_}}}
            try{if($null -ne $ErrorTask -and -not $ErrorTask.Wait(2000)){throw 'BF_BLOCKED: owned RPC stderr remained open.'}}catch{if($null -eq $cleanupError){$cleanupError=$_}}
        }
    }finally{
        try{$ErrorStream.Dispose()}catch{if($null -eq $cleanupError){$cleanupError=$_}}
        try{$Process.Dispose()}catch{if($null -eq $cleanupError){$cleanupError=$_}}
    }
    if($null -ne $cleanupError -and -not $PreservePrimaryError){throw $cleanupError}
}

function Get-BFCodexMcpServerNames {
    param($Response)
    $config=Get-BFValue $Response 'config'
    if($null -eq $config){throw 'BF_BLOCKED: missing native Codex configuration.'}
    $servers=Get-BFValue $config 'mcp_servers'
    if($null -eq $servers){return}
    if($servers -isnot [Collections.IDictionary] -and $servers -isnot [pscustomobject]){throw 'BF_BLOCKED: malformed native MCP configuration.'}
    # Access the property collection explicitly: under StrictMode an empty
    # PSCustomObject does not support member enumeration through `.Name`.
    if($servers -is [Collections.IDictionary]){$names=@($servers.Keys)}else{$names=@($servers.PSObject.Properties|ForEach-Object{$_.Name})}
    foreach($name in $names){Assert-BFText $name 'MCP server name'}
    # A comma-prefixed empty array is emitted by PowerShell as one empty
    # pipeline value.  Keep the return type explicit for nonempty inventories,
    # while returning no pipeline value for an empty native `mcp_servers={}`.
    if($names.Count -eq 0){return}
    return [string[]]($names|Sort-Object -CaseSensitive)
}

function Get-BFCodexMcpDenyOverrides {
    param([string[]]$Names)
    $overrides=@()
    foreach($name in $Names){
        # Codex CLI splits dotted keys literally; quoted TOML keys become part of the server name.
        if($name -cnotmatch '^[A-Za-z0-9_-]+$'){throw 'BF_BLOCKED: MCP server name cannot be safely addressed by the pinned CLI override parser.'}
        $overrides+=@('-c',('mcp_servers.'+$name+'.enabled=false'))
    }
    return $overrides
}

function Assert-BFCodexMcpConfiguration {
    param($Response,[string[]]$Expected,[string[]]$Enabled=@())
    $actual=@(Get-BFCodexMcpServerNames $Response)
    $expected=@($Expected|Sort-Object -CaseSensitive)
    if((Get-BFHash $actual) -cne (Get-BFHash $expected)){throw 'BF_BLOCKED: native MCP configuration inventory changed before dispatch.'}
    foreach($name in $actual){
        $server=Get-BFValue $Response.config.mcp_servers $name
        $enabledValue=Get-BFValue $server 'enabled' $true
        if($enabledValue -isnot [bool] -or $enabledValue -ne ($name -cin $Enabled)){throw 'BF_BLOCKED: native MCP server enablement differs from the exact profile.'}
    }
}

function ConvertTo-BFCodexSkillInventory {
    param($Response,[string]$WorkingDirectory)
    $data=@($Response.data)
    if($data.Count -ne 1 -or $data[0].cwd -cne $WorkingDirectory -or @($data[0].errors).Count -ne 0){throw 'BF_BLOCKED: incomplete Codex skill inventory.'}
    $seen=[Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase);$inventory=@()
    foreach($skill in $data[0].skills){
        $path=Assert-BFSafePath $skill.path
        if(-not $seen.Add($path) -or [IO.Path]::GetFileName($path) -cne 'SKILL.md' -or $skill.enabled -isnot [bool]){throw 'BF_BLOCKED: ambiguous Codex skill inventory.'}
        $inventory += [ordered]@{name=$skill.name;path=$path;scope=$skill.scope;enabled=$skill.enabled;sha256=Get-BFFileHash $path}
    }
    return ,@($inventory|Sort-Object -Property path -CaseSensitive)
}

function Get-BFCodexSkillDenyOverride {
    param([array]$Inventory)
    $entries=@($Inventory|ForEach-Object{'{path='+(ConvertTo-Json -InputObject $_.path.Replace('\','/') -Compress)+',enabled=false}'})
    return 'skills.config=['+($entries -join ',')+']'
}

function Assert-BFCodexSkillDenial {
    param([array]$Inventory,[array]$Disabled)
    $expected=@($Inventory|ForEach-Object{[ordered]@{name=$_.name;path=$_.path;scope=$_.scope;enabled=$false;sha256=$_.sha256}})
    if((Get-BFHash $expected) -cne (Get-BFHash $Disabled)){throw 'BF_BLOCKED: Codex did not disable exactly the registered discovered skills.'}
}
