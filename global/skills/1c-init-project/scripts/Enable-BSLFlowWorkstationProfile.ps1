[CmdletBinding()]
param(
    [Parameter(Mandatory)][string[]]$DevelopmentDatabasePath,
    [Parameter(Mandatory)][string]$PlatformBin,
    [string]$YaxunitDirectory = 'C:\YAxUnit',
    [string]$VanessaDirectory = 'C:\vanessa-automation',
    [string]$ProfilePath = (Join-Path ([Environment]::GetFolderPath('UserProfile')) '.bsl-flow\workstation.json')
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
function Resolve-Absolute([string]$Path,[string]$Name,[bool]$MustExist=$true) {
    if ($Path -notmatch '^(?:[A-Za-z]:[\\/]|[\\/]{2}[^\\/]+[\\/][^\\/]+(?:[\\/]|$))') { throw "$Name must be an absolute filesystem path." }
    $full=[IO.Path]::GetFullPath($Path)
    $root=[IO.Path]::GetPathRoot($full)
    if($full.TrimEnd([char]'\',[char]'/') -eq $root.TrimEnd([char]'\',[char]'/')){$full=$root}else{$full=$full.TrimEnd([char]'\',[char]'/')}
    if ($MustExist -and -not (Test-Path -LiteralPath $full -PathType Container)) { throw "$Name does not exist: $full" }
    $full
}
function Assert-NoReparse([string]$Path,[string]$Name) {
    $item=Get-Item -LiteralPath $Path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { throw "$Name must not be a reparse point: $Path" }
}
$platform=Resolve-Absolute $PlatformBin 'PlatformBin'
$yax=Resolve-Absolute $YaxunitDirectory 'YaxunitDirectory' $false
$va=Resolve-Absolute $VanessaDirectory 'VanessaDirectory' $false
$profileFile=Resolve-Absolute $ProfilePath 'ProfilePath' $false
$databaseByPath=@{}
if (Test-Path -LiteralPath $profileFile -PathType Leaf) {
    try { $existing=Get-Content -LiteralPath $profileFile -Raw -Encoding UTF8|ConvertFrom-Json } catch { throw "Existing workstation profile is invalid JSON: $profileFile" }
    if ($existing.schema_version -ne 1 -or $existing.profile -ne 'development_database_is_test_target') { throw 'Existing workstation profile has an unsupported contract.' }
    foreach($entry in @($existing.development_databases)) { $databaseByPath[(Resolve-Absolute ([string]$entry.path) 'existing database path' $false)]=$entry }
}
foreach($path in $DevelopmentDatabasePath) {
    $db=Resolve-Absolute $path 'DevelopmentDatabasePath'
    Assert-NoReparse $db 'DevelopmentDatabasePath'
    $marker=Join-Path $db '1Cv8.1CD'
    if (-not (Test-Path -LiteralPath $marker -PathType Leaf)) { throw "Not a FILE infobase: $db" }
    Assert-NoReparse $marker '1Cv8.1CD'
    $databaseByPath[$db]=[ordered]@{path=$db;platform_bin=$platform;authorized_at_utc=[DateTime]::UtcNow.ToString('o')}
}
$databases=@($databaseByPath.GetEnumerator()|Sort-Object Name|ForEach-Object{$_.Value})
$record=[ordered]@{schema_version=1;profile='development_database_is_test_target';enabled=$true;updated_at_utc=[DateTime]::UtcNow.ToString('o');catalogs=[ordered]@{yaxunit=$yax;vanessa=$va};development_databases=$databases;credentials='not_stored'}
[IO.Directory]::CreateDirectory((Split-Path -Parent $profileFile))|Out-Null
[IO.File]::WriteAllText($profileFile,($record|ConvertTo-Json -Depth 8)+[Environment]::NewLine,[Text.UTF8Encoding]::new($false))
[pscustomobject]@{status='enabled';profile_path=$profileFile;database_count=$databases.Count;credentials_stored=$false}
