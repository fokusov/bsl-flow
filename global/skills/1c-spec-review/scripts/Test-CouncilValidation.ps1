#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)))) }
$skill = Join-Path $PackageRoot 'global\skills\1c-spec-review'
. (Join-Path $skill 'scripts\Council.Validation.ps1')
. (Join-Path $skill 'scripts\Review.Common.ps1')

$passed = 0
function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "FAIL: $Message" }
    $script:passed++
    "PASS $Message"
}
function Assert-Throws([scriptblock]$Block, [string]$Message) {
    try { & $Block } catch { $script:passed++; "PASS $Message"; return }
    throw "FAIL (no throw): $Message"
}

# 1. Valid critic payload accepted.
$critic = [pscustomobject][ordered]@{
    role = 'intent_critic'; verdict = 'REVISE'
    findings = @([pscustomobject][ordered]@{ id = 'F-001'; severity = 'high'; category = 'missing_requirement'; spec_ref = 'Требуемое поведение / 1'; issue = 'lost'; evidence = 'task line'; suggested_direction = 'add' })
    do_not_change = @('keep X'); needs_input_questions = @()
}
Assert-True ($true) 'fixture ready'
try { Assert-BSLFlowCouncilModelPayload $critic; Assert-True $true 'valid critic payload accepted' } catch { throw "FAIL valid critic: $($_.Exception.Message)" }

# 2. Model provenance claims rejected.
$evil = [pscustomobject][ordered]@{
    role = 'intent_critic'; verdict = 'REVISE'; provider = 'openai'
    findings = @(); do_not_change = @(); needs_input_questions = @()
}
Assert-Throws { Assert-BSLFlowCouncilModelPayload $evil } 'model provider claim rejected'
$evil2 = [pscustomobject][ordered]@{
    role = 'intent_critic'; verdict = 'PASS'; observed = [pscustomobject]@{ model = 'x' }
    findings = @(); do_not_change = @(); needs_input_questions = @()
}
Assert-Throws { Assert-BSLFlowCouncilModelPayload $evil2 } 'model observed claim rejected'

# 3. Non-sequential finding IDs rejected.
$badSeq = [pscustomobject][ordered]@{
    role = 'intent_critic'; verdict = 'REVISE'
    findings = @([pscustomobject][ordered]@{ id = 'F-002'; severity = 'high'; category = 'clarity'; spec_ref = 's'; issue = 'i'; evidence = 'e'; suggested_direction = 'd' })
    do_not_change = @(); needs_input_questions = @()
}
Assert-Throws { Assert-BSLFlowCouncilModelPayload $badSeq } 'non-sequential finding id rejected'

# 4. Brainstorm without verdict.
$brain = [pscustomobject][ordered]@{ role = 'brainstorm'; alternatives = @('a'); risks = @(); unknowns = @(); questions = @('q?') }
try { Assert-BSLFlowBrainstormPayload $brain; Assert-True $true 'brainstorm payload accepted' } catch { throw "FAIL brainstorm: $($_.Exception.Message)" }
$brainEvil = [pscustomobject][ordered]@{ role = 'brainstorm'; verdict = 'PASS'; alternatives = @('a'); risks = @(); unknowns = @(); questions = @() }
Assert-Throws { Assert-BSLFlowBrainstormPayload $brainEvil } 'brainstorm verdict rejected'

# 5. Manifest from the real council spec is sequential and hashed.
$specText = Get-Content -Raw -LiteralPath (Join-Path $PackageRoot 'openspec\changes\api-specification-council\spec.md')
$manifest = New-BSLFlowRequirementManifest $specText
Assert-True (@($manifest.requirements).Count -ge 10) 'manifest extracted from council spec'
try { Assert-BSLFlowRequirementManifest $manifest; Assert-True $true 'manifest validation passed' } catch { throw "FAIL manifest: $($_.Exception.Message)" }
Assert-True ((@($manifest.requirements)[0]).id -eq 'REQ-001') 'manifest starts at REQ-001'

# 6. Diversity matrix.
function New-Member([string]$Role, [string]$Status, [string]$Observed, [string]$Mode) {
    [pscustomobject][ordered]@{
        role = $Role; status = $Status
        requested = [pscustomobject][ordered]@{ provider = 'p'; model = 'req'; effort = 'medium' }
        observed = [pscustomobject][ordered]@{ provider = 'p'; model = $Observed; effort = 'medium' }
        execution_mode = $Mode; fallback_reason = $null
        input_hashes = [pscustomobject][ordered]@{ original_task_sha256 = ('a' * 64); spec_sha256 = ('b' * 64) }
        payload_sha256 = ('c' * 64)
    }
}
$d1 = Get-BSLFlowCouncilDiversity @((New-Member 'intent_critic' 'completed' 'm1' 'direct_api'), (New-Member 'chair' 'completed' 'm2' 'direct_api'))
Assert-True ($d1.diversity -eq 'multi_model') 'distinct observed models give multi_model'
$d2 = Get-BSLFlowCouncilDiversity @((New-Member 'intent_critic' 'completed' 'same' 'current_agent_fallback'), (New-Member 'chair' 'completed' 'same' 'current_agent_fallback'))
Assert-True ($d2.diversity -eq 'multi_role_single_model' -and $d2.fallback_visible) 'single model fallback is not multi_model'
$d3 = Get-BSLFlowCouncilDiversity @((New-Member 'intent_critic' 'failed_before_acceptance' 'm1' 'direct_api'), (New-Member 'chair' 'completed' 'm1' 'direct_api'))
Assert-True ($d3.diversity -eq 'degraded') 'optional failure gives degraded'
$d4 = Get-BSLFlowCouncilDiversity @((New-Member 'intent_critic' 'completed' $null 'direct_api'), (New-Member 'chair' 'completed' 'm1' 'direct_api'))
Assert-True ($d4.diversity -eq 'unknown') 'unknown observed model gives unknown'

# 7. Canonical ordering is completion-independent.
$fA = [pscustomobject][ordered]@{ composite_id = 'architecture_critic:F-001'; role = 'architecture_critic'; id = 'F-001'; severity = 'low'; category = 'clarity'; spec_ref = 's'; issue = 'i'; evidence = 'e'; suggested_direction = 'd' }
$fB = [pscustomobject][ordered]@{ composite_id = 'intent_critic:F-001'; role = 'intent_critic'; id = 'F-001'; severity = 'low'; category = 'clarity'; spec_ref = 's'; issue = 'i'; evidence = 'e'; suggested_direction = 'd' }
$order1 = @(Get-BSLFlowCanonicalFindings @($fA, $fB) | ForEach-Object { $_.composite_id }) -join '|'
$order2 = @(Get-BSLFlowCanonicalFindings @($fB, $fA) | ForEach-Object { $_.composite_id }) -join '|'
Assert-True ($order1 -eq $order2 -and $order1 -eq 'intent_critic:F-001|architecture_critic:F-001') 'canonical order ignores completion order'

# 8. Partial decision without scopes is rejected at review level via gate fixture (covered in schema test below).
"ALL_STAGE1_UNIT_PASSED=$passed"
