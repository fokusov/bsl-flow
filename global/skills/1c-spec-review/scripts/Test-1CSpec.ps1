#Requires -Version 7.0
[CmdletBinding()]
param(
    [Parameter(Mandatory)][string]$ChangePath,
    [string]$OutputPath,
    [int]$MaxCharacters = 30000,
    [switch]$NoThrow
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Review.Common.ps1')

$changeRoot = [System.IO.Path]::GetFullPath($ChangePath)
$specPath = Join-Path $changeRoot 'spec.md'
if (-not (Test-Path -LiteralPath $specPath -PathType Leaf)) { throw "spec.md not found: $specPath" }
$text = [IO.File]::ReadAllText($specPath, (New-Object Text.UTF8Encoding($false)))
$errors = [System.Collections.Generic.List[string]]::new()
$warnings = [System.Collections.Generic.List[string]]::new()
$lines = @($text -split "`r?`n")
function Get-SpecLineNumber([int]$Offset) {
    if ($Offset -lt 0) { return 1 }
    return ([regex]::Matches($text.Substring(0, [math]::Min($Offset, $text.Length)), "`n").Count + 1)
}
function Add-SpecError([string]$Message, [int]$Offset = 0) {
    $formatted = "spec.md line $(Get-SpecLineNumber $Offset): $Message"
    if (-not $errors.Contains($formatted)) { $errors.Add($formatted) }
}
function Get-SectionMatch([string]$Pattern) {
    return [regex]::Match($text, "(?ims)^##\s+$Pattern\s*$\s*(?<body>.*?)(?=^##\s|\z)")
}
function Get-SubstantiveBody([string]$Body) {
    $withoutComments = [regex]::Replace($Body, '(?s)<!--.*?-->', '')
    $kept = foreach ($line in ($withoutComments -split "`r?`n")) {
        $trimmed = $line.Trim()
        if (-not $trimmed) { continue }
        if ($trimmed -match '^[-*]\s*(?:[^:]+):\s*$') { continue }
        if ($trimmed -match '^\d+[.)]?\s*$') { continue }
        $trimmed
    }
    return @($kept)
}

if ([string]::IsNullOrWhiteSpace($text)) { Add-SpecError 'spec.md is empty.' }
foreach ($section in @(
    @{ Name = 'classification'; Pattern = '(?im)^##\s+(Классификация|Classification)\s*$' },
    @{ Name = 'goal'; Pattern = '(?im)^##\s+(Цель|Goal)\s*$' },
    @{ Name = 'required behavior'; Pattern = '(?im)^##\s+(Требуемое поведение|Required behavior)\s*$' },
    @{ Name = '1C context'; Pattern = '(?im)^##\s+(Контекст 1С|1C context)\s*$' },
    @{ Name = 'non-goals'; Pattern = '(?im)^##\s+(Не делать|Non-goals)\s*$' },
    @{ Name = 'acceptance criteria'; Pattern = '(?im)^##\s+(Критерии при[её]мки|Acceptance criteria)\s*$' },
    @{ Name = 'verification'; Pattern = '(?im)^##\s+(Требуемые проверки|Required verification)\s*$' },
    @{ Name = 'uncertainties'; Pattern = '(?im)^##\s+(Неопредел[её]нности\s*/\s*допущения|Uncertainties\s*/\s*assumptions)\s*$' }
)) {
    $sectionMatches = [regex]::Matches($text, $section.Pattern)
    if ($sectionMatches.Count -eq 0) { Add-SpecError "Missing required section: $($section.Name)."; continue }
    if ($sectionMatches.Count -gt 1) { Add-SpecError "Section appears more than once: $($section.Name)." $sectionMatches[1].Index }
    $sectionMatch = Get-SectionMatch (($section.Pattern -replace '^\(\?im\)\^##\\s\+','') -replace '\\s\*\$$','')
    if ($sectionMatch.Success -and @(Get-SubstantiveBody $sectionMatch.Groups['body'].Value).Count -eq 0) {
        Add-SpecError "Section requires substantive continuation: $($section.Name)." $sectionMatches[0].Index
    }
}

$complexityMatches = [regex]::Matches($text, '(?im)^\s*-\s*(?:Сложность|Complexity):\s*(S|M|L)\s*$')
$riskMatches = [regex]::Matches($text, '(?im)^\s*-\s*(?:Риск|Risk):\s*(low|medium|high)\s*$')
if ($complexityMatches.Count -ne 1) { Add-SpecError 'Specification must contain exactly one Complexity value: S, M, or L.' }
if ($riskMatches.Count -ne 1) { Add-SpecError 'Specification must contain exactly one Risk value: low, medium, or high.' }

$placeholderPatterns = @(
    '(?im)^Кратко:\s*какой результат',
    '(?im)^\s*[123]\.\s*$',
    '(?im)^\s*-\s*GIVEN\s+\.\.\.',
    '(?im)^\s*-\s*(Конфигурация/подсистема|Затрагиваемые объекты|Клиент/сервер|Существенные ограничения):\s*$',
    '(?im)<S\|M\|L>|<low\|medium\|high>|\b(TODO|TBD|FIXME)\b'
)
foreach ($pattern in $placeholderPatterns) {
    $placeholder = [regex]::Match($text, $pattern)
    if ($placeholder.Success) { Add-SpecError "Unresolved template placeholder: $($placeholder.Value.Trim())" $placeholder.Index }
}

$acceptanceMatch = Get-SectionMatch '(Критерии при[её]мки|Acceptance criteria)'
if ($acceptanceMatch.Success) {
    $acceptanceBodyRaw = $acceptanceMatch.Groups['body'].Value
    $acceptanceBodyOffset = $acceptanceMatch.Groups['body'].Index + ($acceptanceBodyRaw.Length - $acceptanceBodyRaw.TrimStart().Length)
    $body = $acceptanceBodyRaw.Trim()
    if ($body.Length -lt 30) { Add-SpecError 'Acceptance criteria are empty or too short.' $acceptanceMatch.Index }
    if ($body -notmatch '(?i)\b(GIVEN|WHEN|THEN)\b' -and $body -notmatch '(?m)^\s*[-*]\s+\S.{15,}$') {
        Add-SpecError 'Acceptance criteria are not objectively structured.' $acceptanceMatch.Index
    }
    if ($body -match '(?i)\bGIVEN\b') {
        # Validate each scenario shell, not whether its business oracle is correct.
        # The finding points at the broken scenario's own GIVEN occurrence.
        $givenMatches = [regex]::Matches($body, '(?i)\bGIVEN\b')
        for ($scenarioIndex = 0; $scenarioIndex -lt $givenMatches.Count; $scenarioIndex++) {
            $scenarioStart = $givenMatches[$scenarioIndex].Index + $givenMatches[$scenarioIndex].Length
            $scenarioEnd = if ($scenarioIndex + 1 -lt $givenMatches.Count) { $givenMatches[$scenarioIndex + 1].Index } else { $body.Length }
            $scenario = $body.Substring($scenarioStart, $scenarioEnd - $scenarioStart)
            if ($scenario -notmatch '(?is)\S.+?\bWHEN\b\s+\S.+?\bTHEN\b\s+\S') {
                Add-SpecError 'Each GIVEN acceptance scenario requires nonempty WHEN and THEN clauses.' ($acceptanceBodyOffset + $givenMatches[$scenarioIndex].Index)
            }
        }
    }
}

$verificationMatch = Get-SectionMatch '(Требуемые проверки|Required verification)'
if ($verificationMatch.Success) {
    # Comments are blanked with same-length spaces, so offsets survive into
    # the trimmed body and findings land on the item's own line.
    $verificationBodyRaw = [regex]::Replace($verificationMatch.Groups['body'].Value, '(?s)<!--.*?-->', { param($comment) ($comment.Value -replace '[^\r\n]', ' ') })
    $verificationBodyOffset = $verificationMatch.Groups['body'].Index + ($verificationBodyRaw.Length - $verificationBodyRaw.TrimStart().Length)
    $verificationBody = $verificationBodyRaw.Trim()
    $selectedChecks = [regex]::Matches($verificationBody, '(?im)^\s*[-*]\s+\[x\]\s*(?<detail>.*)$')
    foreach ($check in $selectedChecks) {
        $detail = $check.Groups['detail'].Value.Trim()
        if ($detail -match '^(?i:Static|Unit|Integration|UI|Smoke|Independent review)[\s:—-]*$' -or $detail.Length -lt 12) {
            Add-SpecError 'Each selected verification level must describe what it proves.' ($verificationBodyOffset + $check.Index)
        }
    }
    $withoutUnchecked = [regex]::Replace($verificationBody, '(?m)^\s*[-*]\s+\[ \].*(?:\r?\n|$)', '').Trim()
    if ($withoutUnchecked.Length -lt 20) {
        Add-SpecError 'Required verification must describe a concrete check or an explicit evidence blocker, not an empty checklist.' $verificationMatch.Index
    }
}

if ($text.Length -gt $MaxCharacters) { $warnings.Add("Specification length $($text.Length) exceeds $MaxCharacters characters; verify that detail is necessary.") }
if ($text -match '(?i)на будущее|future[- ]proof|универсальн(?:ый|ая|ое)\s+механизм') { $warnings.Add('Potential speculative design language found; verify concrete justification.') }

$result = [ordered]@{
    schema_version = 1
    checked_at_utc = [DateTime]::UtcNow.ToString('o')
    passed = ($errors.Count -eq 0)
    errors = @($errors)
    warnings = @($warnings)
    stats = [ordered]@{ characters = $text.Length; lines = @($text -split "`r?`n").Count }
}
if (-not $OutputPath) { $OutputPath = Join-Path $changeRoot 'spec-lint.json' }
Write-BSLFlowJsonAtomic -Value $result -Path $OutputPath
if (-not $result.passed -and -not $NoThrow) { throw "Specification lint failed. See: $OutputPath" }
return [pscustomobject]$result
