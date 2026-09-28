#Requires -Version 7.0
[CmdletBinding()]
param(
    [string]$PackageRoot
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path) }

# Remediation plan Ф1.2: the code forbids downgrading M/L review routes to a
# single OpenCode reviewer (Invoke-1CSpecReview.ps1 fails closed instead), so
# no packaged instruction text may still describe OpenCode as a "fallback" for
# a required review, or claim a route degrades "when OpenCode is unavailable".
# Two files are deliberately exempt: OPENCODE.delegation.md, which is installed
# only for the OpenCode host and legitimately talks about preserving OpenCode's
# own reviewer, and the packaged opencode-reviewer.json config, which is not a
# free-text instruction file at all.
$passed = 0
function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "FAIL: $Message" }
    $script:passed++
    "PASS $Message"
}

$globalRoot = Join-Path $PackageRoot 'global'
Assert-True (Test-Path -LiteralPath $globalRoot -PathType Container) 'global/ directory exists'

$exemptPaths = @(
    (Join-Path $globalRoot 'OPENCODE.delegation.md'),
    (Join-Path $globalRoot 'skills\1c-spec-review\reviewer\opencode-reviewer.json')
) | ForEach-Object { [System.IO.Path]::GetFullPath($_) }

$forbiddenPattern = '(?i)opencode[^.\n]{0,80}fallback|fallback[^.\n]{0,80}opencode|when OpenCode'

$markdownFiles = @(Get-ChildItem -LiteralPath $globalRoot -Recurse -File -Filter '*.md')
Assert-True ($markdownFiles.Count -gt 0) 'global/ contains markdown instruction files to scan'

$violations = [System.Collections.Generic.List[string]]::new()
foreach ($file in $markdownFiles) {
    $resolved = [System.IO.Path]::GetFullPath($file.FullName)
    if ($exemptPaths -contains $resolved) { continue }
    $text = Get-Content -Raw -LiteralPath $file.FullName
    if ($text -match $forbiddenPattern) {
        $violations.Add($file.FullName)
    }
}
Assert-True ($violations.Count -eq 0) ("no packaged markdown instruction describes OpenCode as a fallback or a route that degrades when OpenCode is unavailable" + $(if ($violations.Count -gt 0) { ": " + ($violations -join ', ') } else { '' }))

# Regression fixtures: confirm the pattern itself actually catches the two
# defect phrasings the external review found (1c-task/SKILL.md and
# AGENTS.bootstrap.md, both already fixed) so this suite cannot silently stop
# checking anything.
Assert-True (('the single-reviewer OpenCode spec reviewer remains the fallback' -match $forbiddenPattern)) 'pattern still catches the original OpenCode-fallback phrasing'
Assert-True (('may not be silently skipped when OpenCode or the configured model is unavailable' -match $forbiddenPattern)) 'pattern still catches the original OpenCode-unavailable phrasing'
Assert-True (-not ('Preserve the existing OpenCode specification reviewer and its configured model.' -match $forbiddenPattern)) 'pattern does not false-positive on ordinary OpenCode-host wording'

# The exempt delegation file must still exist (it is installed only for the
# OpenCode host) and must still be the only file legitimately carrying
# OpenCode-preservation language; if it starts also using the forbidden
# phrasing that is worth failing loudly rather than silently exempting.
$delegationPath = Join-Path $globalRoot 'OPENCODE.delegation.md'
Assert-True (Test-Path -LiteralPath $delegationPath -PathType Leaf) 'global/OPENCODE.delegation.md is present for the exemption to be meaningful'
$delegationText = Get-Content -Raw -LiteralPath $delegationPath
Assert-True (-not ($delegationText -match $forbiddenPattern)) 'the exempt OpenCode delegation file itself carries no fallback/unavailable phrasing either'

# 1c-task/SKILL.md and AGENTS.bootstrap.md are the two files the external
# review named explicitly; pin their current, corrected wording so a future
# edit cannot silently reintroduce the defect under different words that this
# generic regex might miss.
$taskSkillPath = Join-Path $globalRoot 'skills\1c-task\SKILL.md'
$taskSkillText = Get-Content -Raw -LiteralPath $taskSkillPath
Assert-True ($taskSkillText.Contains('M specification review uses the configured single reviewer; L/high risk uses Council')) '1c-task/SKILL.md states the single-reviewer/Council split without a fallback claim'
Assert-True (-not $taskSkillText.Contains('remains the fallback')) '1c-task/SKILL.md no longer calls OpenCode the fallback reviewer'

$bootstrapPath = Join-Path $globalRoot 'AGENTS.bootstrap.md'
$bootstrapText = Get-Content -Raw -LiteralPath $bootstrapPath
Assert-True ($bootstrapText.Contains('when the configured reviewer is unavailable')) 'AGENTS.bootstrap.md speaks of the configured reviewer, not OpenCode specifically'

"$passed instruction-consistency checks passed"
