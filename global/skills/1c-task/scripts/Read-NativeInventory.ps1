#Requires -Version 7.0
[CmdletBinding()]
param([Parameter(Mandatory)][string]$Target,[Parameter(Mandatory)][string]$Executable)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
[Console]::InputEncoding=[Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false)
$credential=$null
try {
    foreach($module in @('Task.Storage.ps1','Task.Contracts.ps1')){. (Join-Path $PSScriptRoot $module)}
    if(-not [Console]::IsInputRedirected){throw 'Private input required.'}
    $line=[Console]::In.ReadLine()
    if($null -eq $line -or $line.Length -gt 16384){throw 'Invalid input.'}
    $auth=ConvertFrom-Json -InputObject $line -ErrorAction Stop
    Assert-BFFields $auth @('username','password') @() 'auth'
    if($auth.username -isnot [string] -or $auth.password -isnot [string]){throw 'Invalid auth.'}
    $secure=[Security.SecureString]::new()
    foreach($character in $auth.password.ToCharArray()){$secure.AppendChar($character)}
    $secure.MakeReadOnly()
    $credential=[pscredential]::new($auth.username,$secure)
    $line=$null;$auth=$null
    [void](Get-BFRuntimeTargetIdentity $Target)
    $inventory=Read-BFNativeInventoryDirect $Target (Assert-BFSafePath $Executable) $credential
    [Console]::Out.WriteLine((Get-BFCanonicalJson $inventory))
} catch {
    # Never relay a COM exception or connection string to a parent log.
    [Console]::Out.WriteLine('{"kind":"bsl-flow.native-inventory-error","status":"blocked"}')
    exit 11
} finally {
    if($null -ne $credential){$credential.Password.Dispose();$credential=$null}
}
