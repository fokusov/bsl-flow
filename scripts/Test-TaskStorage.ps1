[CmdletBinding()]
param([string]$PackageRoot)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent $PSScriptRoot }
$storage = Join-Path $PackageRoot 'global\skills\1c-task\scripts\Task.Storage.ps1'
if (-not (Test-Path -LiteralPath $storage -PathType Leaf)) { throw "Missing storage module: $storage" }
. $storage

function Assert-T { param([bool]$Condition, [string]$Message) if (-not $Condition) { throw "ASSERTION FAILED: $Message" } }
function Expect-T {
    param([scriptblock]$Action, [string]$Prefix, [string]$Message)
    $observed = $null
    try { & $Action | Out-Null } catch { $observed = $_.Exception.Message }
    Assert-T ($null -ne $observed -and $observed.StartsWith($Prefix + ':')) ($Message + " Observed: $observed")
}
function New-TDirectory { param([string]$Root, [string]$Name) $path = Join-Path $Root $Name; New-Item -ItemType Directory -Path $path -Force | Out-Null; return $path }
function Write-TText { param([string]$Path, [string]$Text) [IO.File]::WriteAllText($Path, $Text, [Text.UTF8Encoding]::new($false)) }

$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('bsl-flow-task-storage-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $testRoot -Force | Out-Null
try {
    $unicode = ([string]([char[]]@(0x041F, 0x0440, 0x0438, 0x0432, 0x0435, 0x0442))) + ' ' + [char]::ConvertFromUtf32(0x1F600)
    $canonical = Get-BFCanonicalJson ([ordered]@{ z = @(); unicode = $unicode; nested = @{ b = 2; A = 1 }; array = @('x', $null, $true) })
    $expectedCanonical = '{"array":["x",null,true],"nested":{"A":1,"b":2},"unicode":"' + $unicode + '","z":[]}'
    Assert-T ($canonical -ceq $expectedCanonical) "Canonical JSON changed: $canonical"
    $orderedHash = Get-BFHash ([ordered]@{ b = 2; a = 1 })
    Assert-T ($orderedHash -ceq (Get-BFHash ([ordered]@{ a = 1; b = 2 }))) 'Object ordering changed the canonical hash.'
    Assert-T ((Get-BFCanonicalJson @()) -ceq '[]') 'Empty array was not preserved.'
    $escapedJson = Get-BFCanonicalJson @{ path = 'C:\one'; text = ('line' + [char]10 + 'next') }
    Assert-T ($escapedJson -ceq '{"path":"C:\\one","text":"line\nnext"}') 'Backslash or control escaping is invalid.'
    Expect-T { Get-BFCanonicalJson ([double]::NaN) } 'BF_INVALID' 'NaN was accepted.'
    Expect-T { Get-BFCanonicalJson ([datetime]::UtcNow) } 'BF_INVALID' 'Unsupported DateTime was accepted.'

    $jsonDirectory = New-TDirectory $testRoot 'json'
    $jsonPath = Join-Path $jsonDirectory 'value.json'
    [void](Write-BFJson -Path $jsonPath -Value ([ordered]@{ b = 2; a = @() }))
    Assert-T (([IO.File]::ReadAllText($jsonPath)) -ceq '{"a":[],"b":2}') 'Write-BFJson did not use canonical UTF-8 JSON.'
    Assert-T ((Get-BFFileHash $jsonPath) -match '^[0-9a-f]{64}$') 'File hash format is invalid.'
    Expect-T { Write-BFJson -Path $jsonPath -Value @{ a = 2 } } 'BF_CONFLICT' 'No-overwrite publication overwrote a file.'
    Assert-T ((Get-BFObjectProperty (Read-BFJson $jsonPath) 'b') -eq 2) 'Read-BFJson did not return an object.'
    $datePath = Join-Path $jsonDirectory 'date.json'; [void](Write-BFJson -Path $datePath -Value @{ observed_at = '2026-09-09T08:48:16.9965211Z' })
    Assert-T ((Read-BFJson $datePath).observed_at -is [string]) 'ISO date text changed type while reading JSON.'
    [void](Write-BFJson -Path $jsonPath -Value ([ordered]@{ replacement = $true }) -Replace)
    Assert-T ((Get-BFObjectProperty (Read-BFJson $jsonPath) 'replacement') -eq $true) 'Replace publication failed.'

    # The target fits MAX_PATH but the same-directory atomic temporary does not.
    $longDirectory = New-TDirectory $testRoot ('nested-' + ('x' * 70))
    $longNameLength = 232 - $longDirectory.Length - 1 - '.json'.Length
    Assert-T ($longNameLength -gt 0) 'Long-path fixture root is unexpectedly long.'
    $longPath = Join-Path $longDirectory (('q' * $longNameLength) + '.json')
    [void](Write-BFJson $longPath @{ value = 1 })
    [void](Write-BFJson $longPath @{ value = 2 } -Replace)
    Assert-T ((Read-BFJson $longPath).value -eq 2) 'Atomic replacement failed when the temporary path exceeds MAX_PATH.'
    Expect-T { Assert-BFSafePath ('\\?\' + $jsonPath) } 'BF_INVALID' 'Caller-supplied device path was accepted.'

    foreach ($case in @(
        @{ name = 'duplicate.json'; text = '{"Name":1,"name":2}' },
        @{ name = 'array.json'; text = '[]' },
        @{ name = 'trailing.json'; text = '{"a":1} garbage' }
    )) {
        $path = Join-Path $jsonDirectory $case.name; Write-TText $path $case.text
        Expect-T { Read-BFJson $path } 'BF_INVALID' ("Invalid JSON was accepted: " + $case.name)
    }

    Expect-T { Assert-BFSafePath 'relative\file.json' } 'BF_INVALID' 'Relative path was accepted.'
    Expect-T { Assert-BFSafePath ($jsonPath + ':stream') } 'BF_INVALID' 'Alternate data stream path was accepted.'
    $target = New-TDirectory $testRoot 'reparse-target'; $link = Join-Path $testRoot 'reparse-link'; $reparseCreated = $false
    try { New-Item -ItemType Junction -Path $link -Target $target -ErrorAction Stop | Out-Null; $reparseCreated = $true } catch { }
    if ($reparseCreated) { Expect-T { Assert-BFSafePath (Join-Path $link 'missing\value.json') } 'BF_INVALID' 'Reparse ancestor of a missing suffix was accepted.' }

    $lockDirectory = New-TDirectory $testRoot 'lock'; $lock = Enter-BFLock $lockDirectory
    try { Expect-T { Enter-BFLock $lockDirectory } 'BF_CONFLICT' 'A second writer acquired the same lock.' } finally { $lock.Dispose() }
    Expect-T { Write-BFRevision -Directory $lockDirectory -State @{ task_id = 'no-lock' } -ExpectedRevision 0 } 'BF_CONFLICT' 'Revision write succeeded without a live caller lock.'

    $journal = New-TDirectory $testRoot 'journal'; $writer = Enter-BFLock $journal
    try {
        $one = Write-BFRevision -Directory $journal -State ([ordered]@{ task_id = 'task-a'; status = 'ready'; values = @() }) -ExpectedRevision 0
        Assert-T ($one.revision -eq 1 -and $null -eq $one.previous_sha256) 'First revision metadata is invalid.'
        $two = Write-BFRevision -Directory $journal -State ([ordered]@{ task_id = 'task-a'; status = 'running' }) -ExpectedRevision 1
        Assert-T ($two.revision -eq 2 -and $two.previous_sha256 -ceq (Get-BFHash $one)) 'Second revision did not link to the first.'
        Expect-T { Write-BFRevision -Directory $journal -State @{ task_id = 'task-a' } -ExpectedRevision 1 } 'BF_CONFLICT' 'Optimistic revision conflict was accepted.'
    } finally { $writer.Dispose() }
    Assert-T ((Read-BFJournal $journal).revision -eq 2) 'Journal did not return its latest revision.'

    $current = Join-Path $journal 'current.json'; Remove-Item -LiteralPath $current -Force
    Assert-T ((Read-BFJournal $journal).revision -eq 2) 'Missing derived current.json blocked journal recovery.'
    Write-TText $current '{"revision":1,"sha256":"stale"}'
    Assert-T ((Read-BFJournal $journal).revision -eq 2) 'Stale derived current.json overrode authoritative revisions.'
    Assert-T (([IO.File]::ReadAllText($current)) -ceq '{"revision":1,"sha256":"stale"}') 'Read-BFJournal invisibly repaired current.json.'

    $gap = New-TDirectory $testRoot 'gap'; Copy-Item -LiteralPath (Join-Path $journal 'revisions') -Destination (Join-Path $gap 'revisions') -Recurse
    Move-Item -LiteralPath (Join-Path $gap 'revisions\000002.json') -Destination (Join-Path $gap 'revisions\000003.json')
    Expect-T { Read-BFJournal $gap } 'BF_BLOCKED' 'Revision gap was accepted.'

    $corrupt = New-TDirectory $testRoot 'corrupt'; Copy-Item -LiteralPath (Join-Path $journal 'revisions') -Destination (Join-Path $corrupt 'revisions') -Recurse
    $firstPath = Join-Path $corrupt 'revisions\000001.json'; Write-TText $firstPath ([IO.File]::ReadAllText($firstPath).Replace('"ready"', '"changed"'))
    Expect-T { Read-BFJournal $corrupt } 'BF_BLOCKED' 'Broken revision hash chain was accepted.'

    $wrongTask = New-TDirectory $testRoot 'wrong-task'; Copy-Item -LiteralPath (Join-Path $journal 'revisions') -Destination (Join-Path $wrongTask 'revisions') -Recurse
    $secondPath = Join-Path $wrongTask 'revisions\000002.json'; Write-TText $secondPath ([IO.File]::ReadAllText($secondPath).Replace('"task-a"', '"task-b"'))
    Expect-T { Read-BFJournal $wrongTask } 'BF_BLOCKED' 'task_id change was accepted.'

    $missingChain = New-TDirectory $testRoot 'missing-chain'; New-Item -ItemType Directory -Path (Join-Path $missingChain 'revisions') | Out-Null
    Write-TText (Join-Path $missingChain 'revisions\000001.json') '{"revision":1,"task_id":"task-a"}'
    Expect-T { Read-BFJournal $missingChain } 'BF_BLOCKED' 'Missing previous_sha256 was accepted.'

    Write-Host ("Task storage contracts passed on PowerShell {0}." -f $PSVersionTable.PSVersion)
}
finally {
    $fullTestRoot = [IO.Path]::GetFullPath($testRoot)
    $tempParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar)
    $expectedPrefix = $tempParent + [IO.Path]::DirectorySeparatorChar + 'bsl-flow-task-storage-'
    if (-not $fullTestRoot.StartsWith($expectedPrefix, [StringComparison]::OrdinalIgnoreCase) -or [IO.Path]::GetDirectoryName($fullTestRoot).TrimEnd([IO.Path]::DirectorySeparatorChar) -ne $tempParent) {
        throw "Unsafe storage test cleanup target: $fullTestRoot"
    }
    if (Test-Path -LiteralPath $fullTestRoot) { Remove-Item -LiteralPath $fullTestRoot -Recurse -Force }
}
