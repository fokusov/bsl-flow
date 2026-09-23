#Requires -Version 7.0
Set-StrictMode -Version Latest

function Get-BFOwnedProcess {
    param($Identity)
    $process=Get-Process -Id $Identity.pid -ErrorAction SilentlyContinue
    if($null -ne $process -and $process.StartTime.ToUniversalTime().ToString('o') -eq $Identity.start_time_utc){return $process}
    return $null
}

function Stop-BFOwnedProcess {
    param($Identity)
    # A database update must finish on its own; Cancel disables future dispatch.
    if ((Get-BFValue $Identity 'non_interruptible' $false) -eq $true) { return }
    $process=Get-BFOwnedProcess $Identity
    if($null -eq $process){return}
    try {
        $process.Kill($true)
        if(-not $process.WaitForExit(3000)){throw 'BF_BLOCKED: exact owned process did not stop; effects remain unknown.'}
    } finally {$process.Dispose()}
}

function Invoke-BFProcess {
    param([string]$Executable, [string[]]$Arguments, [string]$WorkingDirectory, [string]$InputText = '', [string]$OutputDirectory, [int]$TimeoutSeconds = 600, [scriptblock]$Cancelled, [System.Collections.IDictionary]$Environment, [switch]$CleanEnvironment, [int]$MaxOutputBytes = 16777216)
    if($MaxOutputBytes -lt 65536 -or $MaxOutputBytes -gt 16777216){throw 'BF_INVALID: managed output bound is outside the supported range.'}
    [void](Assert-BFSafePath $Executable)
    if (-not (Test-Path -LiteralPath $Executable -PathType Leaf) -or [IO.Path]::GetExtension($Executable) -ne '.exe') { throw 'BF_BLOCKED: managed process launch requires an existing native .exe, not a shell launcher.' }
    [void](Assert-BFSafePath $WorkingDirectory)
    [void](Assert-BFSafePath $OutputDirectory)
    [void][IO.Directory]::CreateDirectory($OutputDirectory)
    if ($null -ne $Cancelled -and (& $Cancelled)) { throw 'BF_BLOCKED: cancelled before process dispatch.' }
    $deadline=Get-Variable -Name BFRunDeadlineUtc -ValueOnly -ErrorAction SilentlyContinue
    if($null -ne $deadline){$TimeoutSeconds=[math]::Min($TimeoutSeconds,[math]::Floor(($deadline-[DateTime]::UtcNow).TotalSeconds));if($TimeoutSeconds -le 0){throw 'BF_BLOCKED: task deadline reached before dispatch.'}}
    $info = New-Object System.Diagnostics.ProcessStartInfo
    if($CleanEnvironment){
        $info.Environment.Clear()
        # Preserve Windows launch/policy inputs, never arbitrary host credentials.
        foreach($name in @('SystemRoot','WINDIR','SystemDrive','COMSPEC','PATH','PATHEXT','USERPROFILE','APPDATA','LOCALAPPDATA','ProgramFiles','ProgramFiles(x86)','ProgramW6432','ProgramData','CommonProgramFiles','CommonProgramFiles(x86)','CommonProgramW6432','COMPUTERNAME','USERNAME','USERDOMAIN','HOMEDRIVE','HOMEPATH','OS','PROCESSOR_ARCHITECTURE','NUMBER_OF_PROCESSORS','__PSLockDownPolicy')){
            $value=[Environment]::GetEnvironmentVariable($name)
            if($null -ne $value){$info.Environment[$name]=$value}
        }
    }
    if($null -ne $Environment){foreach($key in $Environment.Keys){$info.Environment[[string]$key]=[string]$Environment[$key]}}
    $info.FileName=$Executable; $info.WorkingDirectory=$WorkingDirectory
    foreach($argument in $Arguments){
        if($argument -match "[\x00]"){throw 'BF_INVALID: NUL in native argument.'}
        $info.ArgumentList.Add([string]$argument)
    }
    $info.UseShellExecute=$false; $info.CreateNoWindow=$true
    $info.RedirectStandardOutput=$true; $info.RedirectStandardError=$true; $info.RedirectStandardInput=$true
    $info.StandardInputEncoding=New-Object Text.UTF8Encoding($false)
    $info.StandardOutputEncoding=New-Object Text.UTF8Encoding($false)
    $info.StandardErrorEncoding=New-Object Text.UTF8Encoding($false)
    $process=New-Object System.Diagnostics.Process; $process.StartInfo=$info
    $stdoutFile=[IO.File]::Open((Join-Path $OutputDirectory 'stdout.txt'),[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::Read)
    $stderrFile=[IO.File]::Open((Join-Path $OutputDirectory 'stderr.txt'),[IO.FileMode]::CreateNew,[IO.FileAccess]::Write,[IO.FileShare]::Read)
    $reason=$null
    try {
        [void]$process.Start()
        Write-BFJson -Path (Join-Path $OutputDirectory 'process.json') -Value ([ordered]@{pid=$process.Id;start_time_utc=$process.StartTime.ToUniversalTime().ToString('o');executable=$Executable;arguments_sha256=Get-BFHash $Arguments})
        $outTask=$process.StandardOutput.BaseStream.CopyToAsync($stdoutFile)
        $errTask=$process.StandardError.BaseStream.CopyToAsync($stderrFile)
        $watch=[Diagnostics.Stopwatch]::StartNew()
        # Write exact UTF-8 bytes asynchronously so a blocked reader stays bounded.
        $inputBytes=[Text.Encoding]::UTF8.GetBytes($InputText)
        $inputTask=$process.StandardInput.BaseStream.WriteAsync($inputBytes,0,$inputBytes.Length)
        $inputClosed=$false
        while (-not $process.WaitForExit(200)) {
            if(-not $inputClosed -and $inputTask.IsCompleted){try{[void]$inputTask.GetAwaiter().GetResult();$process.StandardInput.Close();$inputClosed=$true}catch{$reason='stdin_failure';break}}
            if ($null -ne $Cancelled -and (& $Cancelled)) { $reason='cancelled'; break }
            if ($watch.Elapsed.TotalSeconds -ge $TimeoutSeconds) { $reason='timeout'; break }
            if (($stdoutFile.Length + $stderrFile.Length) -gt $MaxOutputBytes) { $reason='output_limit'; break }
        }
        if ($reason) {
            # Terminate only this owned process tree, after checking process start identity.
            $identity=Read-BFJson (Join-Path $OutputDirectory 'process.json')
            Stop-BFOwnedProcess $identity
        }
        if (-not $process.HasExited) { throw 'BF_BLOCKED: owned process did not terminate; effects are unknown.' }
        if (-not $outTask.Wait(2000) -or -not $errTask.Wait(2000)) { throw 'BF_BLOCKED: subprocess output still open; reconcile the owned process tree.' }
        $stdoutFile.Flush($true); $stderrFile.Flush($true)
        if(-not $reason -and (($stdoutFile.Length + $stderrFile.Length) -gt $MaxOutputBytes)){$reason='output_limit'}
        $result=[ordered]@{exit_code=$process.ExitCode;stop_reason=$reason;elapsed_seconds=[math]::Round($watch.Elapsed.TotalSeconds,3);process_id=$process.Id;executable=$Executable;stdout=Join-Path $OutputDirectory 'stdout.txt';stderr=Join-Path $OutputDirectory 'stderr.txt'}
        Write-BFJson -Path (Join-Path $OutputDirectory 'exit.json') -Value $result
        return $result
    } finally { $stdoutFile.Dispose(); $stderrFile.Dispose(); $process.Dispose() }
}

function Assert-BFCodexHostVersion {
    param([Parameter(Mandatory)][string]$VersionText)
    if ($VersionText -cnotmatch '^codex-cli (\d+\.\d+\.\d+)$') {
        throw "BF_BLOCKED: unsupported Codex host version format: $VersionText."
    }
    $version = [version]$Matches[1]
    if ($version -lt [version]'0.153.0') {
        throw "BF_BLOCKED: Codex host version predates the validated sandbox contract: $VersionText."
    }
    return $VersionText
}

function Resolve-BFCodex {
    param([string]$Path)
    if (-not $Path) {
        $command=Get-Command codex -ErrorAction SilentlyContinue
        if ($null -eq $command) { throw 'BF_BLOCKED: Codex CLI is not installed.' }
        $Path=$command.Source
        if ([IO.Path]::GetExtension($Path) -ne '.exe') {
            $candidate=Join-Path (Split-Path $Path -Parent) 'node_modules/@openai/codex/node_modules/@openai/codex-win32-x64/vendor/x86_64-pc-windows-msvc/bin/codex.exe'
            if (Test-Path -LiteralPath $candidate -PathType Leaf) { $Path=$candidate }
        }
    }
    if ([IO.Path]::GetExtension($Path) -ne '.exe') { throw 'BF_BLOCKED: supply -CodexPath to the native codex.exe; shell launchers are not executed.' }
    return Assert-BFSafePath $Path
}
