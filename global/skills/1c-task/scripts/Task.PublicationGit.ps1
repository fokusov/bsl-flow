#Requires -Version 7.0
Set-StrictMode -Version Latest

$script:BFPublicationGitFrontendPath = 'C:\Program Files\Git\cmd\git.exe'
$script:BFPublicationGitPath = 'C:\Program Files\Git\mingw64\bin\git.exe'
$script:BFPublicationGhPath = 'C:\Program Files\GitHub CLI\gh.exe'
$script:BFPublicationProcessKeepAlive = @{}

if ($null -eq ('BFPublicationBoundedStream' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.IO;
using System.Threading;
using System.Threading.Tasks;
public sealed class BFPublicationBoundedStream : Stream {
  private readonly Stream inner; private readonly long maximum; private long stored; private long observed;
  public BFPublicationBoundedStream(Stream inner, long maximum) { this.inner=inner; this.maximum=maximum; }
  public long ObservedBytes { get { return Interlocked.Read(ref observed); } }
  private int Admit(int count) { Interlocked.Add(ref observed,count); long left=maximum-stored; int admitted=left<=0?0:(int)Math.Min(left,count); stored+=admitted; return admitted; }
  public override void Write(byte[] buffer,int offset,int count) { int n=Admit(count); if(n>0) inner.Write(buffer,offset,n); }
  public override Task WriteAsync(byte[] buffer,int offset,int count,CancellationToken token) { int n=Admit(count); return n>0?inner.WriteAsync(buffer,offset,n,token):Task.CompletedTask; }
  public override ValueTask WriteAsync(ReadOnlyMemory<byte> buffer,CancellationToken token=default) { int n=Admit(buffer.Length); return n>0?inner.WriteAsync(buffer.Slice(0,n),token):ValueTask.CompletedTask; }
  public override void Flush(){inner.Flush();} public override Task FlushAsync(CancellationToken token){return inner.FlushAsync(token);}
  public override bool CanRead=>false; public override bool CanSeek=>false; public override bool CanWrite=>true; public override long Length=>stored; public override long Position{get=>stored;set=>throw new NotSupportedException();}
  public override int Read(byte[] b,int o,int c){throw new NotSupportedException();} public override long Seek(long o,SeekOrigin so){throw new NotSupportedException();} public override void SetLength(long v){throw new NotSupportedException();}
}
'@
}

function Get-BFPublicationProperty {
    param([Parameter(Mandatory)][object]$Object, [Parameter(Mandatory)][string]$Name)
    if ($Object -is [Collections.IDictionary]) {
        foreach ($key in $Object.Keys) { if ([string]$key -ieq $Name) { return $Object[$key] } }
        return $null
    }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    return $property.Value
}

function Get-BFPublicationSha256 {
    param([Parameter(Mandatory)][string]$Path)
    $stream = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return [Convert]::ToHexString($sha.ComputeHash($stream)).ToLowerInvariant() }
    finally { $sha.Dispose(); $stream.Dispose() }
}

function Get-BFPublicationBytesSha256 {
    param([Parameter(Mandatory)][AllowEmptyCollection()][byte[]]$Bytes)
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return [Convert]::ToHexString($sha.ComputeHash($Bytes)).ToLowerInvariant() }
    finally { $sha.Dispose() }
}

function Assert-BFPublicationPath {
    param([Parameter(Mandatory)][string]$Path, [switch]$Leaf, [switch]$Container, [switch]$AllowMissing)
    if ([string]::IsNullOrWhiteSpace($Path) -or -not [IO.Path]::IsPathRooted($Path) -or $Path.StartsWith('\\?\') -or $Path.StartsWith('\\.\') -or $Path -match '^[^:]+::') {
        throw 'BF_INVALID: publication path must be an ordinary absolute filesystem path.'
    }
    try { $full = [IO.Path]::GetFullPath($Path) } catch { throw 'BF_INVALID: invalid publication path.' }
    $root = [IO.Path]::GetPathRoot($full)
    if ($full.Substring($root.Length).Contains(':')) { throw 'BF_INVALID: alternate data stream paths are not allowed.' }
    $cursor = $full
    while (-not [string]::IsNullOrEmpty($cursor)) {
        if ([IO.File]::Exists($cursor) -or [IO.Directory]::Exists($cursor)) {
            $item = Get-Item -LiteralPath $cursor -Force -ErrorAction Stop
            if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "BF_INVALID: publication path contains a reparse point: $cursor" }
        }
        $parent = [IO.Path]::GetDirectoryName($cursor.TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar))
        if ([string]::IsNullOrEmpty($parent) -or $parent -eq $cursor) { break }
        $cursor = $parent
    }
    if ($Leaf -and -not [IO.File]::Exists($full)) { throw "BF_BLOCKED: required publication file is missing: $full" }
    if ($Container -and -not [IO.Directory]::Exists($full)) { throw "BF_BLOCKED: required publication directory is missing: $full" }
    if (-not $AllowMissing -and -not $Leaf -and -not $Container -and -not ([IO.File]::Exists($full) -or [IO.Directory]::Exists($full))) { throw "BF_BLOCKED: publication path is missing: $full" }
    return $full
}

function Assert-BFPublicationRelativePath {
    param([Parameter(Mandatory)][string]$Path)
    if ([string]::IsNullOrWhiteSpace($Path) -or [IO.Path]::IsPathRooted($Path) -or $Path.Contains('\') -or $Path.Contains([char]0)) { throw 'BF_INVALID: manifest path must be a non-empty slash-separated relative path.' }
    $segments = $Path.Split('/')
    if ($segments.Count -eq 0 -or @($segments | Where-Object { $_ -in @('', '.', '..') -or $_ -ieq '.git' }).Count -gt 0) { throw "BF_INVALID: unsafe manifest path: $Path" }
    return $Path
}

function ConvertTo-BFPublicationStrictUtf8 {
    param([Parameter(Mandatory)][AllowEmptyCollection()][byte[]]$Bytes, [string]$Label = 'Git output')
    try { return [Text.UTF8Encoding]::new($false, $true).GetString($Bytes) }
    catch { throw "BF_BLOCKED: $Label is not valid UTF-8." }
}

function Protect-BFPublicationDiagnostic {
    param([AllowEmptyString()][string]$Text)
    if ($null -eq $Text) { return '' }
    # The admitted URLs reject userinfo. This is defense in depth for tool-generated diagnostics.
    return [regex]::Replace($Text, '(?i)(https?://)[^/@\s]+@', '$1[redacted]@')
}

function Read-BFPublicationSharedBytes {
    param([Parameter(Mandatory)][string]$Path)
    $stream = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::ReadWrite -bor [IO.FileShare]::Delete)
    $memory = [IO.MemoryStream]::new()
    try { $stream.CopyTo($memory); return ,$memory.ToArray() } finally { $memory.Dispose(); $stream.Dispose() }
}

function Write-BFPublicationJsonCreate {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][object]$Value)
    $bytes = [Text.UTF8Encoding]::new($false).GetBytes(($Value | ConvertTo-Json -Depth 12 -Compress))
    $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::Read)
    try { $stream.Write($bytes, 0, $bytes.Length); $stream.Flush($true) } finally { $stream.Dispose() }
}

function New-BFPublicationProcessDirectory {
    param([Parameter(Mandatory)][string]$Directory, [Parameter(Mandatory)][string]$Operation)
    $root = Assert-BFPublicationPath $Directory -Container
    $processRoot = Join-Path $root 'processes'
    [void][IO.Directory]::CreateDirectory($processRoot)
    [void](Assert-BFPublicationPath $processRoot -Container)
    $safeOperation = $Operation -replace '[^A-Za-z0-9_.-]', '_'
    $path = Join-Path $processRoot ($safeOperation + '-' + [guid]::NewGuid().ToString('N'))
    [void][IO.Directory]::CreateDirectory($path)
    return Assert-BFPublicationPath $path -Container
}

function Get-BFPublicationFileRemoteIdentity {
    param([Parameter(Mandatory)][string]$Remote)
    $full = Assert-BFPublicationPath $Remote -Container
    if ($full.StartsWith('\\')) { throw 'BF_INVALID: UNC FILE publication remotes are not supported.' }
    $marker = Assert-BFPublicationPath (Join-Path $full 'HEAD') -Leaf
    if ($null -eq ('BFPublicationNativePath' -as [type])) {
        Add-Type -TypeDefinition @'
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;
public static class BFPublicationNativePath {
  [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
  public static extern uint GetFinalPathNameByHandle(SafeFileHandle handle, System.Text.StringBuilder path, uint size, uint flags);
}
'@
    }
    $handle = [IO.File]::OpenHandle($marker,[IO.FileMode]::Open,[IO.FileAccess]::Read,[IO.FileShare]::ReadWrite -bor [IO.FileShare]::Delete)
    try {
        $buffer = [Text.StringBuilder]::new(32768)
        $length = [BFPublicationNativePath]::GetFinalPathNameByHandle($handle,$buffer,[uint32]$buffer.Capacity,0)
        if ($length -eq 0 -or $length -ge $buffer.Capacity) { throw 'BF_BLOCKED: physical FILE publication remote could not be resolved.' }
        $physical = $buffer.ToString()
        if ($physical.StartsWith('\\?\UNC\',[StringComparison]::OrdinalIgnoreCase)) { throw 'BF_INVALID: UNC FILE publication remotes are not supported.' }
        if ($physical.StartsWith('\\?\')) { $physical = $physical.Substring(4) }
        $resolved = [IO.Path]::GetFullPath((Split-Path $physical -Parent)).TrimEnd('\','/')
        if ($full.TrimEnd('\','/') -ine $resolved) { throw 'BF_BLOCKED: aliased FILE publication remote is not admitted; use the resolved physical path.' }
        return $resolved
    } finally { $handle.Dispose() }
}

function Assert-BFPublicationRemoteBareConfig {
    param([Parameter(Mandatory)][string]$ConfigPath)
    $text = [IO.File]::ReadAllText($ConfigPath, [Text.UTF8Encoding]::new($false, $true))
    $section = ''; $bareValues = @()
    foreach ($line in ($text -split '\r?\n')) {
        $trimmed = $line.Trim()
        if ($trimmed -eq '' -or $trimmed.StartsWith('#') -or $trimmed.StartsWith(';')) { continue }
        if ($trimmed -match '^\[([^\]]+)\]\s*(?:[#;].*)?$') {
            $sectionHeader = $Matches[1].Trim()
            if ($sectionHeader -match '^(?i:include(?:if)?)(?:\s|\.|$)') { throw 'BF_INVALID: FILE remote config includes are not admitted.' }
            $section = (($sectionHeader -split '\s+',2)[0]).Trim('"').ToLowerInvariant()
            continue
        }
        if ($trimmed -notmatch '^([^=\s]+)\s*=\s*(.*)$') { throw 'BF_INVALID: FILE remote config contains an unrecognized non-empty line.' }
        $key = $Matches[1].ToLowerInvariant(); $value = $Matches[2].Trim()
        if ($key -eq 'path' -and $section -in @('include','includeif')) { throw 'BF_INVALID: FILE remote config includes are not admitted.' }
        if ($section -eq 'core' -and $key -eq 'bare') { $bareValues += $value.ToLowerInvariant() }
    }
    if ($bareValues.Count -ne 1 -or $bareValues[0] -ne 'true') { throw 'BF_INVALID: FILE publication remote must have exactly one effective core.bare=true.' }
}

function Get-BFPublicationInputProfile {
    param([Parameter(Mandatory)][Alias('Input')][object]$Publication)
    $remote = [string](Get-BFPublicationProperty $Publication 'remote')
    $ref = [string](Get-BFPublicationProperty $Publication 'ref')
    $auth = [string](Get-BFPublicationProperty $Publication 'auth')
    if ($ref -cnotmatch '^refs/heads/codex/[A-Za-z0-9][A-Za-z0-9._/-]*$' -or $ref.Contains('..') -or $ref.EndsWith('.') -or $ref.EndsWith('/') -or $ref.Contains('@{') -or $ref.Contains('//')) { throw 'BF_INVALID: publication ref must be a narrow refs/heads/codex/* ref.' }
    if ([string]::IsNullOrWhiteSpace($remote) -or $remote.Contains([char]0) -or $remote -match '[\r\n]') { throw 'BF_INVALID: publication remote is missing or malformed.' }
    if ([IO.Path]::IsPathRooted($remote)) {
        if ($auth -cne 'none') { throw 'BF_INVALID: FILE publication requires auth=none.' }
        $remote = Get-BFPublicationFileRemoteIdentity $remote
        if (-not [IO.File]::Exists((Join-Path $remote 'HEAD')) -or -not [IO.File]::Exists((Join-Path $remote 'config')) -or -not [IO.Directory]::Exists((Join-Path $remote 'objects'))) { throw 'BF_INVALID: FILE publication remote must be an existing local bare repository.' }
        Assert-BFPublicationRemoteBareConfig (Join-Path $remote 'config')
        return [ordered]@{transport='file';remote=$remote;ref=$ref;auth='none'}
    }
    $match = [regex]::Match($remote, '^https://github\.com/([A-Za-z0-9](?:[A-Za-z0-9_.-]{0,98}[A-Za-z0-9])?)/([A-Za-z0-9](?:[A-Za-z0-9_.-]{0,98}[A-Za-z0-9])?)\.git$')
    if (-not $match.Success -or $match.Groups[1].Value -in @('.', '..') -or $match.Groups[2].Value -in @('.', '..')) { throw 'BF_INVALID: HTTPS remote must be exactly https://github.com/OWNER/REPO.git.' }
    if ($auth -cne 'github_cli') { throw 'BF_INVALID: GitHub HTTPS publication requires auth=github_cli.' }
    return [ordered]@{transport='https';remote=$remote;ref=$ref;auth='github_cli'}
}

function Get-BFPublicationFileIdentity {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Name, [switch]$RequireVersion)
    $fixed = Assert-BFPublicationPath $Path -Leaf
    $versionInfo = [Diagnostics.FileVersionInfo]::GetVersionInfo($fixed)
    $version = if (-not [string]::IsNullOrWhiteSpace($versionInfo.ProductVersion)) { $versionInfo.ProductVersion } elseif (-not [string]::IsNullOrWhiteSpace($versionInfo.FileVersion)) { $versionInfo.FileVersion } else { $null }
    if ($RequireVersion -and [string]::IsNullOrWhiteSpace($version)) { throw "BF_BLOCKED: $Name version identity is unavailable." }
    return [ordered]@{path=$fixed;sha256=Get-BFPublicationSha256 $fixed;version=$version}
}

function Get-BFPublicationGitInstallationIdentity {
    $git = Get-BFPublicationFileIdentity $script:BFPublicationGitPath 'Git runtime' -RequireVersion
    $frontend = Get-BFPublicationFileIdentity $script:BFPublicationGitFrontendPath 'Git fixed-installation frontend'
    $root = Assert-BFPublicationPath (Split-Path (Split-Path (Split-Path $git.path -Parent) -Parent) -Parent) -Container
    $sh = Get-BFPublicationFileIdentity (Join-Path $root 'usr\bin\sh.exe') 'Git transport shell'
    $remoteHttps = Get-BFPublicationFileIdentity (Join-Path $root 'mingw64\libexec\git-core\git-remote-https.exe') 'Git HTTPS transport'
    return [ordered]@{git=$git;frontend=$frontend;installation_root=$root;exec_path=(Assert-BFPublicationPath (Join-Path $root 'mingw64\libexec\git-core') -Container);shell=$sh;remote_https=$remoteHttps}
}

function Get-BFPublicationGitDependencies {
    param([Parameter(Mandatory)][Alias('Input')][object]$Publication)
    $profile = Get-BFPublicationInputProfile $Publication
    $installation = Get-BFPublicationGitInstallationIdentity
    $canonicalRemote = $profile.remote.ToLowerInvariant()
    $result = [ordered]@{schema_version=1;git=$installation.git;git_frontend=$installation.frontend;git_installation_root=$installation.installation_root;git_exec_path=$installation.exec_path;git_shell=$installation.shell;git_remote_https=$installation.remote_https;transport=$profile.transport;auth=$profile.auth;remote=$canonicalRemote;ref=$profile.ref;redirects=$false;credential_source=if ($profile.auth -eq 'github_cli') { 'github_cli' } else { 'none' }}
    if ($profile.auth -eq 'github_cli') { $result.gh = Get-BFPublicationFileIdentity $script:BFPublicationGhPath 'GitHub CLI' }
    return $result
}

function New-BFPublicationGitContext {
    param([Parameter(Mandatory)][string]$Directory)
    $root = Assert-BFPublicationPath $Directory -AllowMissing
    [void][IO.Directory]::CreateDirectory($root)
    $root = Assert-BFPublicationPath $root -Container
    $contextId = (Get-BFPublicationBytesSha256 ([Text.UTF8Encoding]::new($false).GetBytes($root.ToLowerInvariant()))).Substring(0,16)
    $localData = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
    if ([string]::IsNullOrWhiteSpace($localData)) { throw 'BF_BLOCKED: controller-owned local application data directory is unavailable.' }
    $shortBase = Join-Path $localData 'BSLFlow\publication-cwd'
    [void][IO.Directory]::CreateDirectory($shortBase); $shortBase = Assert-BFPublicationPath $shortBase -Container
    $shortRoot = Join-Path $shortBase $contextId
    [void][IO.Directory]::CreateDirectory($shortRoot); $shortRoot = Assert-BFPublicationPath $shortRoot -Container
    $emptyConfig = Join-Path $shortRoot 'empty.gitconfig'
    $emptyAttributes = Join-Path $shortRoot 'empty.gitattributes'
    $emptyHooks = Join-Path $shortRoot 'empty-hooks'
    foreach ($path in @($emptyConfig, $emptyAttributes)) {
        if (-not [IO.File]::Exists($path)) { [IO.File]::WriteAllBytes($path, [byte[]]@()) }
        [void](Assert-BFPublicationPath $path -Leaf)
        if ([IO.FileInfo]::new($path).Length -ne 0) { throw 'BF_BLOCKED: publication empty config or attributes file changed.' }
    }
    [void][IO.Directory]::CreateDirectory($emptyHooks)
    [void](Assert-BFPublicationPath $emptyHooks -Container)
    if (@(Get-ChildItem -LiteralPath $emptyHooks -Force).Count -ne 0) { throw 'BF_BLOCKED: publication hooks directory is not empty.' }
    $cwd = Join-Path $shortRoot 'cwd'
    [void][IO.Directory]::CreateDirectory($cwd); $cwd = Assert-BFPublicationPath $cwd -Container
    if (@(Get-ChildItem -LiteralPath $cwd -Force).Count -ne 0) { throw 'BF_BLOCKED: publication Git working directory is not empty.' }
    return [ordered]@{root=$root;cwd=$cwd;empty_config=$emptyConfig;empty_attributes=$emptyAttributes;empty_hooks=$emptyHooks}
}

function Get-BFPublicationBaseArguments {
    param([Parameter(Mandatory)][object]$Context, [ValidateSet('none','github_cli')][string]$Auth)
    $arguments = @('--no-replace-objects','-c','core.longpaths=true','-c',('core.hooksPath=' + $Context.empty_hooks),'-c','core.fsmonitor=false','-c','core.autocrlf=false','-c','core.safecrlf=false','-c',('core.attributesFile=' + $Context.empty_attributes),'-c','commit.gpgSign=false','-c','tag.gpgSign=false','-c','credential.helper=','-c','http.followRedirects=false')
    if ($Auth -eq 'github_cli') {
        $gh = Get-BFPublicationFileIdentity $script:BFPublicationGhPath 'GitHub CLI'
        $arguments += @('-c',('credential.helper=!"' + $gh.path.Replace('\','/') + '" auth git-credential'))
    }
    return ,$arguments
}

function Get-BFPublicationArgumentsSha256 {
    param([Parameter(Mandatory)][AllowEmptyCollection()][string[]]$Arguments)
    return Get-BFPublicationBytesSha256 ([Text.UTF8Encoding]::new($false).GetBytes(($Arguments -join "`0")))
}

function Get-BFPublicationRepositoryPath {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Directory)
    $staging = Assert-BFPublicationPath $Directory -AllowMissing
    $identity = Get-BFPublicationBytesSha256 ([Text.UTF8Encoding]::new($false).GetBytes($staging.ToLowerInvariant()))
    $localData = [Environment]::GetFolderPath([Environment+SpecialFolder]::LocalApplicationData)
    if ([string]::IsNullOrWhiteSpace($localData)) { throw 'BF_BLOCKED: controller-owned local application data directory is unavailable.' }
    $root = Join-Path $localData ('BSLFlow\publication-objects\' + $identity)
    return Join-Path $root 'repository.git'
}

function Get-BFPublicationOwnedBareConfigBytes {
    param([ValidateSet('sha1','sha256')][string]$ObjectFormat)
    $text = if ($ObjectFormat -eq 'sha256') { "[core]`n`trepositoryformatversion = 1`n`tbare = true`n[extensions]`n`tobjectformat = sha256`n" } else { "[core]`n`trepositoryformatversion = 0`n`tbare = true`n" }
    return ,[Text.UTF8Encoding]::new($false).GetBytes($text)
}

function Set-BFPublicationOwnedBareConfig {
    param([Parameter(Mandatory)][string]$Repository, [ValidateSet('sha1','sha256')][string]$ObjectFormat)
    $repo = Assert-BFPublicationPath $Repository -Container
    [IO.File]::WriteAllBytes((Join-Path $repo 'config'), (Get-BFPublicationOwnedBareConfigBytes $ObjectFormat))
}

function Assert-BFPublicationOwnedBareRepository {
    param([Parameter(Mandatory)][string]$Repository)
    $repo = Assert-BFPublicationPath $Repository -Container
    foreach ($forbidden in @('commondir','config.worktree','objects\info\alternates')) { if (Test-Path -LiteralPath (Join-Path $repo $forbidden)) { throw "BF_BLOCKED: unsupported owned bare repository indirection: $forbidden" } }
    $actual = [IO.File]::ReadAllBytes((Assert-BFPublicationPath (Join-Path $repo 'config') -Leaf))
    $sha1 = Get-BFPublicationOwnedBareConfigBytes sha1; $sha256 = Get-BFPublicationOwnedBareConfigBytes sha256
    if (-not ([Convert]::ToHexString($actual) -ceq [Convert]::ToHexString($sha1) -or [Convert]::ToHexString($actual) -ceq [Convert]::ToHexString($sha256))) { throw 'BF_BLOCKED: owned bare repository config changed or is not normalized.' }
    return $repo
}

function Invoke-BFPublicationGitProcess {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][string[]]$Arguments,
        [Parameter(Mandatory)][string]$Directory,
        [Parameter(Mandatory)][string]$Operation,
        [AllowEmptyCollection()][byte[]]$InputBytes = [byte[]]@(),
        [ValidateSet('none','file','https')][string]$Transport = 'none',
        [ValidateSet('none','github_cli')][string]$Auth = 'none',
        [Collections.IDictionary]$Environment = @{},
        [int]$TimeoutSeconds = 120,
        [int64]$MaximumOutputBytes = 67108864,
        [string]$DispatchPath,
        [switch]$Push
    )
    if ($TimeoutSeconds -lt 1 -or $TimeoutSeconds -gt 120) { throw 'BF_INVALID: Git timeout must be between 1 and 120 seconds.' }
    $installation = Get-BFPublicationGitInstallationIdentity
    $git = $installation.git
    $context = New-BFPublicationGitContext $Directory
    $gitDirectoryIndex = [Array]::IndexOf($Arguments, '--git-dir')
    if ($gitDirectoryIndex -ge 0 -and $Operation -cne 'repository-init') {
        if (($gitDirectoryIndex + 1) -ge $Arguments.Count) { throw 'BF_INVALID: --git-dir requires an exact owned repository.' }
        [void](Assert-BFPublicationOwnedBareRepository ([string]$Arguments[$gitDirectoryIndex + 1]))
    }
    $receiptDirectory = New-BFPublicationProcessDirectory $context.root $Operation
    $stdoutPath = Join-Path $receiptDirectory 'stdout.bin'
    $stderrPath = Join-Path $receiptDirectory 'stderr.txt'
    $stdoutStream = [IO.File]::Open($stdoutPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::Read)
    $stderrStream = [IO.File]::Open($stderrPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::Read)
    $stdoutSink = [BFPublicationBoundedStream]::new($stdoutStream,$MaximumOutputBytes)
    $stderrSink = [BFPublicationBoundedStream]::new($stderrStream,$MaximumOutputBytes)
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $git.path
    $start.WorkingDirectory = $context.cwd
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardInput = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($key in @($start.Environment.Keys)) {
        if ($key -match '^(?i:GIT_|SSH_|GH_|GITHUB_|HTTP_PROXY$|HTTPS_PROXY$|ALL_PROXY$|NO_PROXY$|CURL_|SSL_CERT_)') { [void]$start.Environment.Remove($key) }
    }
    $windowsRoot = [Environment]::GetFolderPath([Environment+SpecialFolder]::Windows)
    $start.Environment['PATH'] = @((Join-Path $installation.installation_root 'cmd'),(Join-Path $installation.installation_root 'mingw64\bin'),(Join-Path $installation.installation_root 'usr\bin'),(Join-Path $windowsRoot 'System32'),$windowsRoot) -join ';'
    $start.Environment['GIT_CONFIG_NOSYSTEM'] = '1'
    $start.Environment['GIT_CONFIG_GLOBAL'] = $context.empty_config
    $start.Environment['GIT_TERMINAL_PROMPT'] = '0'
    $start.Environment['GIT_NO_REPLACE_OBJECTS'] = '1'
    $start.Environment['GIT_NO_LAZY_FETCH'] = '1'
    $start.Environment['GIT_EXEC_PATH'] = $installation.exec_path
    $start.Environment['GCM_INTERACTIVE'] = 'never'
    $start.Environment['GH_PROMPT_DISABLED'] = '1'
    $start.Environment['GIT_ALLOW_PROTOCOL'] = switch ($Transport) { 'file' { 'file' } 'https' { 'https' } default { 'file' } }
    foreach ($key in $Environment.Keys) {
        if ([string]$key -notmatch '^BF_INTERNAL_[A-Z0-9_]+$') { throw 'BF_INVALID: unsupported publication environment override.' }
        $actualKey = ([string]$key).Substring('BF_INTERNAL_'.Length)
        if ($actualKey -notin @('GIT_INDEX_FILE','GIT_AUTHOR_NAME','GIT_AUTHOR_EMAIL','GIT_AUTHOR_DATE','GIT_COMMITTER_NAME','GIT_COMMITTER_EMAIL','GIT_COMMITTER_DATE')) { throw 'BF_INVALID: unsupported publication environment key.' }
        $start.Environment[$actualKey] = [string]$Environment[$key]
    }
    $baseArguments = Get-BFPublicationBaseArguments $context $Auth
    foreach ($argument in @($baseArguments + $Arguments)) {
        if ([string]$argument -match "\x00") { throw 'BF_INVALID: NUL in Git argument.' }
        [void]$start.ArgumentList.Add([string]$argument)
    }
    $argumentsSha256 = Get-BFPublicationArgumentsSha256 @($baseArguments + $Arguments)
    $dispatchSha256 = $null
    if ($Push) {
        if ([string]::IsNullOrWhiteSpace($DispatchPath)) { throw 'BF_INVALID: push requires a semantic dispatch receipt.' }
        $safeDispatch = Assert-BFPublicationPath $DispatchPath -Leaf
        $dispatchBytes = [IO.File]::ReadAllBytes($safeDispatch); $dispatchSha256 = Get-BFPublicationBytesSha256 $dispatchBytes
        try { $dispatch = ConvertFrom-Json (ConvertTo-BFPublicationStrictUtf8 $dispatchBytes 'push dispatch') -ErrorAction Stop } catch { throw 'BF_BLOCKED: push dispatch receipt is malformed.' }
        if ([string](Get-BFPublicationProperty $dispatch 'arguments_sha256') -cne $argumentsSha256) { throw 'BF_BLOCKED: push dispatch does not bind the exact hardened argv.' }
    } elseif (-not [string]::IsNullOrWhiteSpace($DispatchPath)) { throw 'BF_INVALID: dispatch receipt is only valid for push.' }
    $process = [Diagnostics.Process]::new(); $process.StartInfo = $start
    $outTask = $null; $errTask = $null; $watch = [Diagnostics.Stopwatch]::StartNew(); $completed = $false; $stopReason = $null; $started = $false
    try {
        [void]$process.Start(); $started = $true
        $identity = [ordered]@{pid=$process.Id;start_time_utc=$process.StartTime.ToUniversalTime().ToString('o');executable=$git.path;executable_sha256=$git.sha256;arguments_sha256=$argumentsSha256;dispatch_sha256=$dispatchSha256;non_interruptible=[bool]$Push;receipt_directory=$receiptDirectory}
        Write-BFPublicationJsonCreate (Join-Path $receiptDirectory 'process.json') $identity
        $outTask = $process.StandardOutput.BaseStream.CopyToAsync($stdoutSink)
        $errTask = $process.StandardError.BaseStream.CopyToAsync($stderrSink)
        $inputTask = $process.StandardInput.BaseStream.WriteAsync($InputBytes, 0, $InputBytes.Length)
        $inputClosed = $false
        while (-not $process.WaitForExit(100)) {
            if (-not $inputClosed -and $inputTask.IsCompleted) { [void]$inputTask.GetAwaiter().GetResult(); $process.StandardInput.Close(); $inputClosed = $true }
            if ($stdoutSink.ObservedBytes -gt $MaximumOutputBytes -or $stderrSink.ObservedBytes -gt $MaximumOutputBytes) { $stopReason = 'output_limit'; break }
            if ($watch.Elapsed.TotalSeconds -ge $TimeoutSeconds) { $stopReason = 'timeout'; break }
        }
        if (-not $inputClosed -and $inputTask.IsCompleted) { [void]$inputTask.GetAwaiter().GetResult(); $process.StandardInput.Close(); $inputClosed = $true }
        if ($null -ne $stopReason -and -not $Push) {
            try { $process.Kill($true); [void]$process.WaitForExit(5000) } catch { }
        }
        if ($process.HasExited) {
            if (-not $outTask.Wait(5000) -or -not $errTask.Wait(5000)) { throw 'BF_BLOCKED: Git output streams did not close.' }
            $completed = $true
        } elseif ($Push) {
            # Keep redirected streams alive. Recovery uses the persisted exact PID/start identity and never repeats a push.
            $script:BFPublicationProcessKeepAlive[[string]$process.Id] = [ordered]@{process=$process;stdout=$stdoutStream;stderr=$stderrStream;stdout_sink=$stdoutSink;stderr_sink=$stderrSink;stdout_task=$outTask;stderr_task=$errTask}
        }
        $stdoutStream.Flush($true); $stderrStream.Flush($true)
        $stdoutBytes = Read-BFPublicationSharedBytes $stdoutPath
        $stderrBytes = Read-BFPublicationSharedBytes $stderrPath
        $stderrText = Protect-BFPublicationDiagnostic (ConvertTo-BFPublicationStrictUtf8 $stderrBytes 'Git stderr')
        if ($stderrText -cne (ConvertTo-BFPublicationStrictUtf8 $stderrBytes 'Git stderr')) { [IO.File]::WriteAllText($stderrPath, $stderrText, [Text.UTF8Encoding]::new($false)) }
        $stdoutText = $null; $stdoutTextValid = $true
        try { $stdoutText = ConvertTo-BFPublicationStrictUtf8 $stdoutBytes 'Git stdout' } catch { $stdoutTextValid = $false }
        $result = [ordered]@{exit_code=if ($completed) { $process.ExitCode } else { $null };stdout=$stdoutText;stderr=$stderrText;process=$identity;completed=$completed;stop_reason=$stopReason;stdout_bytes=$stdoutBytes;stdout_text_valid=$stdoutTextValid;receipt_directory=$receiptDirectory}
        Write-BFPublicationJsonCreate (Join-Path $receiptDirectory 'result.json') ([ordered]@{exit_code=$result.exit_code;completed=$completed;stop_reason=$stopReason;process=$identity;stdout_sha256=Get-BFPublicationBytesSha256 $stdoutBytes;stderr_sha256=Get-BFPublicationBytesSha256 ([Text.UTF8Encoding]::new($false).GetBytes($stderrText))})
        return $result
    } finally {
        if ($completed -or -not $started) { $process.Dispose(); $stdoutSink.Dispose(); $stderrSink.Dispose(); $stdoutStream.Dispose(); $stderrStream.Dispose() }
        elseif (-not $Push) {
            try { $process.Kill($true); [void]$process.WaitForExit(5000) } catch { }
            $process.Dispose(); $stdoutSink.Dispose(); $stderrSink.Dispose(); $stdoutStream.Dispose(); $stderrStream.Dispose()
        } elseif (-not $script:BFPublicationProcessKeepAlive.ContainsKey([string]$process.Id)) {
            $script:BFPublicationProcessKeepAlive[[string]$process.Id] = [ordered]@{process=$process;stdout=$stdoutStream;stderr=$stderrStream;stdout_sink=$stdoutSink;stderr_sink=$stderrSink;stdout_task=$outTask;stderr_task=$errTask}
        }
    }
}

function Assert-BFPublicationGitSuccess {
    param([Parameter(Mandatory)][object]$Result, [Parameter(Mandatory)][string]$Operation)
    if (-not $Result.completed) { throw "BF_BLOCKED: $Operation did not complete within the bounded wait." }
    if ($Result.exit_code -ne 0) { throw "BF_BLOCKED: $Operation failed: $($Result.stderr.Trim())" }
    return $Result
}

function ConvertFrom-BFPublicationLsTree {
    param([Parameter(Mandatory)][byte[]]$Bytes)
    $result = [ordered]@{}
    $start = 0
    for ($index = 0; $index -lt $Bytes.Length; $index++) {
        if ($Bytes[$index] -ne 0) { continue }
        $recordBytes = $Bytes[$start..($index - 1)]; $start = $index + 1
        $record = ConvertTo-BFPublicationStrictUtf8 $recordBytes 'ls-tree record'
        $tab = $record.IndexOf("`t")
        if ($tab -lt 0) { throw 'BF_BLOCKED: malformed ls-tree record.' }
        $header = $record.Substring(0, $tab).Split(' ')
        if ($header.Count -ne 3 -or $header[2] -cnotmatch '^[0-9a-f]{40}([0-9a-f]{24})?$') { throw 'BF_BLOCKED: malformed ls-tree identity.' }
        $path = $record.Substring($tab + 1)
        if ($result.Contains($path)) { throw 'BF_BLOCKED: duplicate path in baseline tree.' }
        $result[$path] = [ordered]@{mode=$header[0];type=$header[1];oid=$header[2]}
    }
    if ($start -ne $Bytes.Length) { throw 'BF_BLOCKED: unterminated ls-tree output.' }
    return $result
}

function New-BFPublicationCommit {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][object]$State,
        [Parameter(Mandatory)][string]$DeliveryPath,
        [Parameter(Mandatory)][object]$Manifest,
        [Parameter(Mandatory)][string]$Directory,
        [Parameter(Mandatory)][object]$Metadata,
        [Parameter(Mandatory)][Alias('Input')][object]$Publication
    )
    [void](Get-BFPublicationInputProfile $Publication)
    $context = New-BFPublicationGitContext $Directory
    $delivery = Assert-BFPublicationPath $DeliveryPath -Container
    $worker = Assert-BFPublicationPath ([string](Get-BFPublicationProperty $State 'worker_path')) -Container
    $baseline = [string](Get-BFPublicationProperty $State 'baseline')
    if ($baseline -cnotmatch '^[0-9a-f]{40}([0-9a-f]{24})?$') { throw 'BF_INVALID: publication baseline is not an object identity.' }
    $name = [string](Get-BFPublicationProperty $Metadata 'name'); $email = [string](Get-BFPublicationProperty $Metadata 'email'); $message = [string](Get-BFPublicationProperty $Metadata 'message'); $timestamp = [string](Get-BFPublicationProperty $Metadata 'timestamp')
    if ([string]::IsNullOrWhiteSpace($name) -or $name -match '[\x00\r\n]' -or $email -cnotmatch '^[^<>\s@]+@[^<>\s@]+$' -or [string]::IsNullOrWhiteSpace($message) -or $message.Contains([char]0)) { throw 'BF_INVALID: invalid deterministic commit identity or message.' }
    $parsedTimestamp = [DateTimeOffset]::MinValue
    $timestampStyle = [Globalization.DateTimeStyles]::AssumeUniversal -bor [Globalization.DateTimeStyles]::AdjustToUniversal
    if ($timestamp -cnotmatch '^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,7})?Z$' -or -not [DateTimeOffset]::TryParse($timestamp, [Globalization.CultureInfo]::InvariantCulture, $timestampStyle, [ref]$parsedTimestamp) -or $parsedTimestamp.Offset -ne [TimeSpan]::Zero) { throw 'BF_INVALID: commit timestamp must be a persisted UTC ISO-8601 value ending in Z.' }
    $headBefore = Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('-C',$worker,'rev-parse','--verify','HEAD^{commit}') $context.root 'worker-head-before') 'worker HEAD read'
    if ($headBefore.stdout.Trim() -cne $baseline) { throw 'BF_BLOCKED: worker HEAD does not equal the accepted baseline.' }
    $formatResult = Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('-C',$worker,'rev-parse','--show-object-format') $context.root 'worker-object-format') 'Git object format read'
    $objectFormat = $formatResult.stdout.Trim()
    if ($objectFormat -notin @('sha1','sha256') -or ($objectFormat -eq 'sha1' -and $baseline.Length -ne 40) -or ($objectFormat -eq 'sha256' -and $baseline.Length -ne 64)) { throw 'BF_BLOCKED: unsupported or inconsistent Git object format.' }
    $repository = Get-BFPublicationRepositoryPath $context.root
    $objectRoot = Split-Path $repository -Parent
    [void][IO.Directory]::CreateDirectory($objectRoot); [void](Assert-BFPublicationPath $objectRoot -Container)
    $bundle = Join-Path $objectRoot 'baseline.bundle'
    if ([IO.File]::Exists($bundle) -or [IO.Directory]::Exists($repository)) { throw 'BF_CONFLICT: publication Git staging already exists.' }
    [void](Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('-C',$worker,'bundle','create',$bundle,'HEAD') $context.root 'bundle-create' -Transport file) 'baseline bundle creation')
    $headAfter = Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('-C',$worker,'rev-parse','--verify','HEAD^{commit}') $context.root 'worker-head-after') 'worker HEAD re-read'
    if ($headAfter.stdout.Trim() -cne $baseline) { throw 'BF_BLOCKED: worker HEAD changed during baseline export.' }
    [void](Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @(('--git-dir=' + $repository),'init','--bare','--template=',("--object-format=$objectFormat")) $context.root 'repository-init' -Transport file) 'isolated repository initialization')
    Set-BFPublicationOwnedBareConfig $repository $objectFormat
    [void](Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('--git-dir',$repository,'bundle','verify',$bundle) $context.root 'bundle-verify' -Transport file) 'baseline bundle verification')
    $heads = Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('--git-dir',$repository,'bundle','list-heads',$bundle) $context.root 'bundle-heads' -Transport file) 'baseline bundle head read'
    $headLines = @($heads.stdout -split '\r?\n' | Where-Object { $_ -ne '' })
    if ($headLines.Count -ne 1 -or $headLines[0] -cnotmatch ('^' + [regex]::Escape($baseline) + '[ \t]+HEAD$')) { throw 'BF_BLOCKED: baseline bundle does not contain exactly the pinned HEAD.' }
    [void](Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('--git-dir',$repository,'bundle','unbundle',$bundle) $context.root 'bundle-import' -Transport file) 'baseline bundle import')
    [void](Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('--git-dir',$repository,'cat-file','-e',($baseline + '^{commit}')) $context.root 'baseline-object-check') 'baseline object check')
    $baselineTreeResult = Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('--git-dir',$repository,'ls-tree','-rz','--full-tree',$baseline) $context.root 'baseline-tree') 'baseline tree read'
    $baselineTree = ConvertFrom-BFPublicationLsTree $baselineTreeResult.stdout_bytes
    $files = @(Get-BFPublicationProperty $Manifest 'files')
    if ($files.Count -eq 0) { throw 'BF_INVALID: publication manifest is empty.' }
    $ordinal = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal); $windows = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    $entries = @()
    foreach ($file in $files) {
        $path = Assert-BFPublicationRelativePath ([string](Get-BFPublicationProperty $file 'path'))
        if (-not $ordinal.Add($path) -or -not $windows.Add($path)) { throw "BF_INVALID: duplicate or case-colliding manifest path: $path" }
        $baselineEntry = if ($baselineTree.Contains($path)) { $baselineTree[$path] } else { $null }
        if ($null -ne $baselineEntry -and ($baselineEntry.type -ne 'blob' -or $baselineEntry.mode -notin @('100644','100755'))) { throw "BF_BLOCKED: symlink, submodule, or non-regular baseline entry is unsupported: $path" }
        if ([bool](Get-BFPublicationProperty $file 'deleted')) { continue }
        $expectedHash = [string](Get-BFPublicationProperty $file 'sha256')
        if ($expectedHash -cnotmatch '^[0-9a-f]{64}$') { throw "BF_INVALID: invalid source SHA-256 for $path" }
        $source = Assert-BFPublicationPath (Join-Path $delivery ('source/' + $path)) -Leaf
        if ((Get-BFPublicationSha256 $source) -cne $expectedHash) { throw "BF_BLOCKED: delivery bytes do not match accepted manifest: $path" }
        $bytes = [IO.File]::ReadAllBytes($source)
        $blobResult = Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('--git-dir',$repository,'hash-object','--no-filters','-w','--stdin') $context.root 'hash-object' -InputBytes $bytes) "blob write for $path"
        $blob = $blobResult.stdout.Trim()
        if ($blob -cnotmatch '^[0-9a-f]{40}([0-9a-f]{24})?$') { throw "BF_BLOCKED: malformed blob identity for $path" }
        $entries += [ordered]@{path=$path;mode=if ($null -eq $baselineEntry) { '100644' } else { $baselineEntry.mode };oid=$blob;sha256=$expectedHash}
    }
    $entries = @($entries | Sort-Object { $_.path })
    $indexStream = [IO.MemoryStream]::new()
    try {
        foreach ($entry in $entries) {
            $record = [Text.UTF8Encoding]::new($false, $true).GetBytes(("{0} {1}`t{2}" -f $entry.mode,$entry.oid,$entry.path))
            $indexStream.Write($record, 0, $record.Length); $indexStream.WriteByte(0)
        }
        $indexBytes = $indexStream.ToArray()
    } finally { $indexStream.Dispose() }
    $indexPath = Join-Path $context.root 'publication.index'
    $indexEnvironment = @{BF_INTERNAL_GIT_INDEX_FILE=$indexPath}
    [void](Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('--git-dir',$repository,'read-tree','--empty') $context.root 'index-empty' -Environment $indexEnvironment) 'empty index creation')
    [void](Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('--git-dir',$repository,'update-index','-z','--index-info') $context.root 'index-info' -InputBytes $indexBytes -Environment $indexEnvironment) 'exact index population')
    $treeResult = Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('--git-dir',$repository,'write-tree') $context.root 'write-tree' -Environment $indexEnvironment) 'tree creation'
    $tree = $treeResult.stdout.Trim()
    $verifyTreeResult = Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('--git-dir',$repository,'ls-tree','-rz','--full-tree',$tree) $context.root 'verify-tree') 'final tree read'
    $verifyTree = ConvertFrom-BFPublicationLsTree $verifyTreeResult.stdout_bytes
    if ($verifyTree.Count -ne $entries.Count) { throw 'BF_BLOCKED: final tree contains missing or extra files.' }
    foreach ($entry in $entries) {
        if (-not $verifyTree.Contains($entry.path) -or $verifyTree[$entry.path].type -ne 'blob' -or $verifyTree[$entry.path].mode -cne $entry.mode -or $verifyTree[$entry.path].oid -cne $entry.oid) { throw "BF_BLOCKED: final tree path/mode/blob mismatch: $($entry.path)" }
        $raw = Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('--git-dir',$repository,'cat-file','blob',$entry.oid) $context.root 'verify-blob') "blob verification for $($entry.path)"
        if ((Get-BFPublicationBytesSha256 $raw.stdout_bytes) -cne $entry.sha256) { throw "BF_BLOCKED: final blob bytes mismatch: $($entry.path)" }
    }
    $commitEnvironment = @{BF_INTERNAL_GIT_AUTHOR_NAME=$name;BF_INTERNAL_GIT_AUTHOR_EMAIL=$email;BF_INTERNAL_GIT_AUTHOR_DATE=$timestamp;BF_INTERNAL_GIT_COMMITTER_NAME=$name;BF_INTERNAL_GIT_COMMITTER_EMAIL=$email;BF_INTERNAL_GIT_COMMITTER_DATE=$timestamp}
    $messageBytes = [Text.UTF8Encoding]::new($false, $true).GetBytes($message)
    $commitResult = Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('--git-dir',$repository,'commit-tree',$tree,'-p',$baseline,'-F','-') $context.root 'commit-tree' -InputBytes $messageBytes -Environment $commitEnvironment) 'commit creation'
    $commit = $commitResult.stdout.Trim()
    $commitCheck = Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('--git-dir',$repository,'rev-parse','--verify',($commit + '^{commit}')) $context.root 'commit-check') 'commit identity check'
    if ($commitCheck.stdout.Trim() -cne $commit) { throw 'BF_BLOCKED: created commit identity did not verify.' }
    return [ordered]@{repository=$repository;staging_directory=$context.root;commit_oid=$commit;tree_oid=$tree;parent_oid=$baseline;object_format=$objectFormat;mode_policy='baseline regular files preserve 100644/100755; new files use 100644; symlinks and submodules rejected';file_count=$entries.Count}
}

function Get-BFPublicationRemote {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Repository, [Parameter(Mandatory)][Alias('Input')][object]$Publication, [Parameter(Mandatory)][string]$Directory)
    $profile = Get-BFPublicationInputProfile $Publication
    $repository = Assert-BFPublicationPath $Repository -Container
    $refCheck = Invoke-BFPublicationGitProcess @('check-ref-format',$profile.ref) $Directory 'check-ref-format'
    [void](Assert-BFPublicationGitSuccess $refCheck 'publication ref validation')
    $result = Invoke-BFPublicationGitProcess @('--git-dir',$repository,'ls-remote','--exit-code','--refs',$profile.remote,$profile.ref) $Directory 'ls-remote' -Transport $profile.transport -Auth $profile.auth
    $oid = $null
    if ($result.completed -and $result.exit_code -eq 0) {
        $lines = @($result.stdout -split '\r?\n' | Where-Object { $_ -ne '' })
        if ($lines.Count -ne 1 -or $lines[0] -cnotmatch '^([0-9a-f]{40}(?:[0-9a-f]{24})?)\t(.+)$' -or $Matches[2] -cne $profile.ref) { throw 'BF_BLOCKED: remote returned an ambiguous or unexpected ref.' }
        $oid = $Matches[1]
    } elseif ($result.completed -and $result.exit_code -eq 2 -and [string]::IsNullOrEmpty($result.stdout)) {
        $oid = $null
    }
    return [ordered]@{oid=$oid;exit_code=$result.exit_code;stdout=$result.stdout;stderr=$result.stderr;process=$result.process;completed=$result.completed;stop_reason=$result.stop_reason;receipt_directory=$result.receipt_directory}
}

function Send-BFPublicationCommit {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Repository, [Parameter(Mandatory)][string]$CommitOid, [Parameter(Mandatory)][Alias('Input')][object]$Publication, [Parameter(Mandatory)][string]$Directory)
    $profile = Get-BFPublicationInputProfile $Publication
    $repository = Assert-BFPublicationPath $Repository -Container
    if ($CommitOid -cnotmatch '^[0-9a-f]{40}([0-9a-f]{24})?$') { throw 'BF_INVALID: publication commit identity is malformed.' }
    [void](Assert-BFPublicationGitSuccess (Invoke-BFPublicationGitProcess @('--git-dir',$repository,'cat-file','-e',($CommitOid + '^{commit}')) $Directory 'push-commit-check') 'publication commit check')
    $lease = '--force-with-lease=' + $profile.ref + ':'
    $refspec = $CommitOid + ':' + $profile.ref
    $pushArguments = @('--git-dir',$repository,'push','--porcelain','--no-verify',$lease,$profile.remote,$refspec)
    $pushContext = New-BFPublicationGitContext $Directory
    $argumentsSha256 = Get-BFPublicationArgumentsSha256 @((Get-BFPublicationBaseArguments $pushContext $profile.auth) + $pushArguments)
    $dispatchPath = Join-Path $pushContext.root 'dispatch.json'
    $requestHash = if ($null -ne (Get-Command Get-BFHash -ErrorAction SilentlyContinue)) { Get-BFHash $Publication } else { $null }
    $dispatch = [ordered]@{schema_version=1;publication_id=Get-BFPublicationProperty $Publication 'publication_id';task_id=Get-BFPublicationProperty $Publication 'task_id';acceptance_sha256=Get-BFPublicationProperty $Publication 'acceptance_sha256';request_sha256=$requestHash;commit_oid=$CommitOid;remote=$profile.remote.ToLowerInvariant();ref=$profile.ref;transport=$profile.transport;auth=$profile.auth;arguments_sha256=$argumentsSha256}
    if ([IO.File]::Exists($dispatchPath)) { throw 'BF_CONFLICT: push semantic dispatch receipt already exists.' }
    Write-BFPublicationJsonCreate $dispatchPath $dispatch
    $result = Invoke-BFPublicationGitProcess $pushArguments $Directory 'push-create-only' -Transport $profile.transport -Auth $profile.auth -DispatchPath $dispatchPath -Push
    return [ordered]@{exit_code=$result.exit_code;stdout=$result.stdout;stderr=$result.stderr;process=$result.process;completed=$result.completed;stop_reason=$result.stop_reason;receipt_directory=$result.receipt_directory}
}
