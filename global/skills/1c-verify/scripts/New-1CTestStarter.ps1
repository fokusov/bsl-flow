[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ProjectPath,
    [Parameter(Mandatory)][string]$TestClientFileDatabasePath,
    [Parameter(Mandatory)][string]$TestClientUser,
    [Parameter(Mandatory)][ValidateRange(1025,65535)][int]$TestClientPort,
    [string]$TestClientName,
    [Parameter(Mandatory)][string]$VanessaEpfPath,
    [Parameter(Mandatory)][string]$ReportPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-AbsolutePath([string]$Value, [string]$Name) {
    if ($Value -notmatch '^(?:[A-Za-z]:[\\/]|[\\/]{2}[^\\/]+[\\/][^\\/]+(?:[\\/]|$))') { throw "$Name must be an absolute filesystem path." }
    return [IO.Path]::GetFullPath($Value)
}
function ConvertTo-BslQuoted([string]$Value) { return '"' + $Value.Replace('"', '""') + '"' }
function Write-StarterFile([string]$Path, [string]$Content, [Collections.Generic.List[object]]$Status) {
    $normalizedContent = $Content.TrimEnd("`r", "`n")
    if (Test-Path -LiteralPath $Path) {
        if ((Get-Content -LiteralPath $Path -Raw -Encoding UTF8).TrimEnd("`r", "`n") -ne $normalizedContent) { throw "Refusing conflicting existing output: $Path" }
        $Status.Add([pscustomobject]@{ path=$Path; status='existing_verified' })
        return
    }
    New-Item -ItemType Directory -Path (Split-Path -Parent $Path) -Force | Out-Null
    Set-Content -LiteralPath $Path -Value $normalizedContent -Encoding UTF8
    $Status.Add([pscustomobject]@{ path=$Path; status='created' })
}

$project = Assert-AbsolutePath $ProjectPath 'ProjectPath'
$fileDatabase = Assert-AbsolutePath $TestClientFileDatabasePath 'TestClientFileDatabasePath'
$epf = Assert-AbsolutePath $VanessaEpfPath 'VanessaEpfPath'
$report = Assert-AbsolutePath $ReportPath 'ReportPath'
if ([string]::IsNullOrWhiteSpace($TestClientUser)) { throw 'TestClientUser must not be empty.' }
if ([string]::IsNullOrWhiteSpace($TestClientName)) {
    $sha = [Security.Cryptography.SHA256]::Create()
    try { $TestClientName = 'bsl-flow-' + ([BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($project.ToLowerInvariant()))).Replace('-','').Substring(0,12).ToLowerInvariant()) }
    finally { $sha.Dispose() }
}
if ($TestClientName -notmatch '^bsl-flow-[a-z0-9-]{6,48}$') { throw 'TestClientName must be a stable bsl-flow identifier.' }
if (-not (Test-Path -LiteralPath $project -PathType Container)) { throw "ProjectPath does not exist: $project" }

$assetRoot = Join-Path $PSScriptRoot '..\assets\test-starter'
if (-not (Test-Path -LiteralPath $assetRoot -PathType Container)) { throw "Starter assets are missing: $assetRoot" }
$target = Join-Path $project 'tests\bsl-flow-starter'
$localTarget = Join-Path $project '.bsl-flow\local\test-starter\vanessa'
$manifestPath = Join-Path $localTarget 'va-run.json'
if ($manifestPath.Contains(';')) { throw "Refusing VAParams path containing ';': $manifestPath" }
$marker = 'BSLFLOW-UI-' + [guid]::NewGuid().ToString('N')
if (Test-Path -LiteralPath $manifestPath -PathType Leaf) {
    try { $marker = [string]((Get-Content -LiteralPath $manifestPath -Raw -Encoding UTF8 | ConvertFrom-Json).marker) }
    catch { throw "Existing run manifest is not valid JSON: $manifestPath" }
    if ($marker -notmatch '^BSLFLOW-UI-[0-9a-f]{32}$') { throw "Existing run manifest has an invalid marker: $manifestPath" }
}
$status = [Collections.Generic.List[object]]::new()

foreach ($relative in @('yaxunit\ТестПилотДвижка.bsl','yaxunit\ИнтеграцияСОткатом.recipe.bsl','yaxunit\ФикстураSafeMode.recipe.bsl','vanessa\pilot-engine.feature','vanessa\ui-persisted-result.recipe.feature','README.md')) {
    $source = Join-Path $assetRoot $relative
    Write-StarterFile (Join-Path $target $relative) (Get-Content -LiteralPath $source -Raw -Encoding UTF8) $status
}

$profile = [ordered]@{
    'ПортЗапускаТестКлиента'=$TestClientPort; 'Имя'=$TestClientName; 'ПутьКИнфобазе'=('File={0};' -f (ConvertTo-BslQuoted $fileDatabase));
    'Синоним'='bsl-flow generated'; 'ИмяКомпьютера'='localhost'; 'ТипКлиента'='Тонкий'; 'ДопПараметры'=('/N' + (ConvertTo-BslQuoted $TestClientUser)); 'АктивизироватьСтроку'=$true
}
$vaParams = [ordered]@{
    'КаталогФич'=(Join-Path $target 'vanessa\pilot-engine.feature'); 'КаталогПроекта'=$project; 'КаталогИнструментов'=(Split-Path -Parent $epf);
    'ВыполнитьСценарии'=$true; 'ЗавершитьРаботуСистемы'=$true; 'ДелатьОтчетВФорматеjUnit'=$true; 'КаталогВыгрузкиJUnit'=$report;
    'ЗагрузкаФичПриОткрытии'=$false; 'ЗакрытьTestClientПослеЗапускаСценариев'=$true; 'КлиентыТестирования'=@($profile)
}
$profileJson = ConvertTo-Json -InputObject @($profile) -Compress -Depth 10
$escapedProfileJson = $profileJson.Replace(';','\;')
$vaParamsText = $vaParams | ConvertTo-Json -Depth 10
$vaParamsPath = Join-Path $localTarget 'VAParams.json'
Write-StarterFile $vaParamsPath $vaParamsText $status
$commandParameters = 'StartFeaturePlayer;DisableLoadConfig=true;DisableLoadTestClientsTable=true;VAParams={0};ДанныеКлиентовТестирования={1}' -f $vaParamsPath,$escapedProfileJson
$manifest = [ordered]@{
    schema_version=1; status='runtime_unverified'; marker=$marker; feature=(Join-Path $target 'vanessa\pilot-engine.feature'); va_params=$vaParamsPath;
    launcher=[ordered]@{ execute=$epf; file_database=$fileDatabase; testclient_name=$TestClientName; testclient_user=$TestClientUser; testclient_port=$TestClientPort; command_parameters=$commandParameters; report_path=$report }
    note='Generated locally. No 1C process was launched and no database or extension was changed.'
}
Write-StarterFile $manifestPath ($manifest | ConvertTo-Json -Depth 12) $status
[pscustomobject]@{ schema_version=1; status='runtime_unverified'; target=$target; marker=$marker; artifacts=@($status); next_step='Run the generated engine pilot through an authorized supported route; bind project recipes only after real metadata and SafeMode are confirmed.' }
