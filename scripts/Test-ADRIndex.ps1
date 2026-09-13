#Requires -Version 7.0
# Offline ADR-index contract: deterministic canonical hash, valid source links,
# subject registry and fail-closed negative fixtures. No model/runtime/process.
[CmdletBinding()]param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
if(-not $PackageRoot){$PackageRoot=Split-Path -Parent $PSScriptRoot}
$root=[IO.Path]::GetFullPath($PackageRoot).TrimEnd('\','/')
$scripts=Join-Path $root 'global/skills/1c-task/scripts'
. (Join-Path $scripts 'Task.Storage.ps1')
. (Join-Path $scripts 'Task.Architecture.ps1')
$script:checks=0
function Assert-A([bool]$Condition,[string]$Message){if(-not $Condition){throw "ASSERTION FAILED: $Message"};$script:checks++}
function Failure-A([scriptblock]$Action){try{& $Action|Out-Null;return ''}catch{return $_.Exception.Message}}

$index=Read-BFArchitectureIndex $root
Assert-A ($null -ne (Assert-BFADRIndex $index $root)) 'Valid ADR index was rejected.'
Assert-A ($index.decisions.Count -eq 10 -and $index.subjects.Count -eq 9) 'ADR index inventory changed unexpectedly.'

# Deterministic canonical identity across repeated recomputation.
$first=Get-BFArchitectureIndexHash $index
$second=Get-BFArchitectureIndexHash ((Get-BFCanonicalJson $index)|ConvertFrom-Json)
Assert-A ($first -ceq $second -and $first -match '^[0-9a-f]{64}$') 'ADR index hash is not deterministic.'

# Subject selection is fail-closed: superseded ADR-6 is never normative.
$recovery=@(Get-BFArchitectureApplicableDecisions $index @('controller.recovery') | ForEach-Object { $_.id })
Assert-A (($recovery -join ',') -ceq 'ADR-5') 'controller.recovery must select exactly ADR-5.'
$native=@(Get-BFArchitectureApplicableDecisions $index @('runtime.native-1c') | ForEach-Object { $_.id })
Assert-A (($native -join ',') -ceq 'ADR-7') 'Superseded ADR-6 leaked into the applicable set.'
$gates=@(Get-BFArchitectureApplicableDecisions $index @('controller.gates') | ForEach-Object { $_.id })
Assert-A (($gates -join ',') -ceq 'ADR-10,ADR-2,ADR-3,ADR-8') ('controller.gates selection changed: '+($gates -join ','))
$adapters=@(Get-BFArchitectureApplicableDecisions $index @('adapter.codex','adapter.opencode') | ForEach-Object { $_.id })
Assert-A (($adapters -join ',') -ceq 'ADR-4') 'Review decision is not bound to the adapter subjects.'
Assert-A (@(Get-BFArchitectureApplicableDecisions $index @('controller.unknown')).Count -eq 0) 'Unknown subject must not match any decision.'

# Negative fixtures: a damaged link must block validation.
$sourcePath=Join-Path $root 'docs/ARCHITECTURE_RU.md'
$bad=(Get-BFCanonicalJson $index)|ConvertFrom-Json
$duplicate=(Get-BFCanonicalJson $bad.decisions[0])|ConvertFrom-Json
$duplicate.title='duplicate decision'
$bad.decisions=@($bad.decisions+@($duplicate))
Assert-A ((Failure-A {Assert-BFADRIndex $bad $root}) -match 'Duplicate ADR id') 'Duplicate ADR id accepted.'

$bad=(Get-BFCanonicalJson $index)|ConvertFrom-Json
$bad.decisions[0].source.anchor='adr-1-does-not-exist'
Assert-A ((Failure-A {Assert-BFADRIndex $bad $root}) -match 'anchor') 'Wrong source anchor accepted.'

$bad=(Get-BFCanonicalJson $index)|ConvertFrom-Json
$bad.decisions[0].source.path='docs/architecture/missing-adr-source.md'
Assert-A ((Failure-A {Assert-BFADRIndex $bad $root}) -match 'source is missing') 'Missing source file accepted.'

$bad=(Get-BFCanonicalJson $index)|ConvertFrom-Json
$bad.decisions[0].informed_by=@('ADR-999')
Assert-A ((Failure-A {Assert-BFADRIndex $bad $root}) -match 'invalid informed_by reference') 'Dangling informed_by reference accepted.'

$bad=(Get-BFCanonicalJson $index)|ConvertFrom-Json
$bad.decisions[0].applies_to=@('controller.unknown')
Assert-A ((Failure-A {Assert-BFADRIndex $bad $root}) -match 'Unknown architecture subject') 'Unknown subject accepted.'

$bad=(Get-BFCanonicalJson $index)|ConvertFrom-Json
$bad.decisions[0].supersedes=@('ADR-2')
$bad.decisions[1].supersedes=@('ADR-1')
Assert-A ((Failure-A {Assert-BFADRIndex $bad $root}) -match 'Supersedes cycle') 'Supersedes cycle accepted.'

$bad=(Get-BFCanonicalJson $index)|ConvertFrom-Json
$bad.decisions[6].supersedes=@()
Assert-A ((Failure-A {Assert-BFADRIndex $bad $root}) -match 'requires exactly one accepted') 'Superseded ADR without accepted superseder accepted.'

# The schema itself rejects structurally invalid indexes.
$bad=(Get-BFCanonicalJson $index)|ConvertFrom-Json
$bad|Add-Member -NotePropertyName unexpected -NotePropertyValue $true
Assert-A ((Failure-A {Assert-BFADRIndex $bad $root}) -match 'schema') 'Unknown top-level field accepted.'

$bad=(Get-BFCanonicalJson $index)|ConvertFrom-Json
$bad.decisions[0].status='unknown'
Assert-A ((Failure-A {Assert-BFADRIndex $bad $root}) -match 'schema') 'Unknown decision status accepted.'

# Containment and required registry fields (review defects 2 and 5).
$bad=(Get-BFCanonicalJson $index)|ConvertFrom-Json
$bad.decisions[0].source.path='..\..\ARCHITECTURE_RU.md'
Assert-A ((Failure-A {Assert-BFADRIndex $bad $root}) -match 'Unsafe relative|escapes') 'Escaping source path accepted.'

$bad=(Get-BFCanonicalJson $index)|ConvertFrom-Json
$bad.decisions[0].source.path='C:\Windows\win.ini'
Assert-A ((Failure-A {Assert-BFADRIndex $bad $root}) -match 'Unsafe relative|escapes') 'Rooted source path accepted.'

$bad=(Get-BFCanonicalJson $index)|ConvertFrom-Json
$bad.subjects[0].refs=@('does-not-exist.ps1#DoesNotExist')
Assert-A ((Failure-A {Assert-BFADRIndex $bad $root}) -match 'reference is missing|symbol is missing') 'Missing subject reference accepted.'

$bad=(Get-BFCanonicalJson $index)|ConvertFrom-Json
$bad.subjects[0].refs=@('..\..\escape.ps1')
Assert-A ((Failure-A {Assert-BFADRIndex $bad $root}) -match 'Unsafe relative|escapes') 'Escaping subject reference accepted.'

$bad=(Get-BFCanonicalJson $index)|ConvertFrom-Json
$bad.subjects[0].refs=@('global/skills/1c-task/scripts/Task.Storage.ps1#NoSuchSymbol')
Assert-A ((Failure-A {Assert-BFADRIndex $bad $root}) -match 'symbol is missing') 'Missing subject symbol accepted.'

$bad=(Get-BFCanonicalJson $index)|ConvertFrom-Json
$bad.decisions[0].PSObject.Properties.Remove('informed_by')
Assert-A ((Failure-A {Assert-BFADRIndex $bad $root}) -match 'schema') 'Decision without informed_by accepted (validator crash instead of schema error).'

$bad=(Get-BFCanonicalJson $index)|ConvertFrom-Json
$bad.decisions[0].title=('x'*250)
Assert-A ((Failure-A {Assert-BFADRIndex $bad $root}) -match 'schema') 'Over-long decision title accepted.'

Assert-A (Test-Path -LiteralPath $sourcePath -PathType Leaf) 'Normative ADR source is missing.'
Write-Output "ADR_INDEX_OK checks=$script:checks hash=$first; model/runtime/process=0"
