#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path))) }
$skill = Join-Path $PackageRoot 'global\skills\1c-spec-review'
. (Join-Path $skill 'scripts\Review.Common.ps1')
. (Join-Path $skill 'scripts\Council.Common.ps1')
. (Join-Path $skill 'scripts\Council.Engine.ps1')
. (Join-Path $skill 'scripts\Council.Profile.ps1')
. (Join-Path $skill 'scripts\Invoke-CouncilReview.ps1')

$passed = 0
function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "FAIL: $Message" }
    $script:passed++
    "PASS $Message"
}
function Assert-Throws([scriptblock]$Block, [string]$Message) {
    try { & $Block | Out-Null } catch { $script:passed++; "PASS $Message"; return }
    throw "FAIL (no throw): $Message"
}
function Assert-ThrowsWithName([scriptblock]$Block, [string]$FileFragment, [string]$KeyFragment, [string]$Message) {
    try { & $Block | Out-Null } catch {
        $text = [string]$_.Exception.Message
        if ($text.Contains($FileFragment) -and $text.Contains($KeyFragment)) { $script:passed++; "PASS $Message"; return }
        throw "FAIL ($Message): error did not name file '$FileFragment' and key '$KeyFragment': $text"
    }
    throw "FAIL (no throw): $Message"
}

# Hermetic profile state: save and restore the operator environment.
$savedProfileEnv = [System.Environment]::GetEnvironmentVariable('BSL_FLOW_USER_CONFIG')
$savedTokenEnv = [System.Environment]::GetEnvironmentVariable('BSL_FLOW_TEST_PROFILE_TOKEN')
$utf8 = [System.Text.UTF8Encoding]::new($false)
$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ('council-profile-' + [guid]::NewGuid().ToString('N'))
try {
    [System.Environment]::SetEnvironmentVariable('BSL_FLOW_USER_CONFIG', $null)
    [System.Environment]::SetEnvironmentVariable('BSL_FLOW_TEST_PROFILE_TOKEN', $null)
    New-Item -ItemType Directory -Path (Join-Path $tempRoot 'home\.bsl-flow') -Force | Out-Null
    $homeDir = Join-Path $tempRoot 'home'

    # P1: self-contained project council that already parses and gates without
    # any profile. Used for the absent-profile regression and precedence tests.
    $projectSelfContained = @(
        'review:',
        '  council:',
        '    enabled: true',
        '    roles:',
        '      intent_critic:',
        '        enabled: true',
        '        required: true',
        '        model: project-reviewer',
        '      architecture_critic:',
        '        enabled: true',
        '        required: true',
        '        model: project-reviewer',
        '      executability_critic:',
        '        enabled: true',
        '        required: true',
        '        model: project-reviewer',
        '      chair:',
        '        enabled: true',
        '        required: true',
        '        model: project-reviewer',
        '  routing:',
        '    m_default: required',
        'llm:',
        '  providers:',
        '    projectapi:',
        '      protocol: openai_compatible',
        '      base_url: https://api.project.example/v1',
        '      token_env: BSL_FLOW_TEST_PROFILE_TOKEN',
        '  models:',
        '    project-reviewer:',
        '      provider: projectapi',
        '      model: vendor/project-model'
    ) -join "`r`n"
    $projectSelfContained += "`r`n"

    # P2: the same council, but the chair has no model binding. The raw project
    # parse would refuse an enabled role without a model; only the merged
    # effective policy can resolve it through the user profile.
    $projectChairUnbound = @(
        'review:',
        '  council:',
        '    enabled: true',
        '    roles:',
        '      intent_critic:',
        '        enabled: true',
        '        required: true',
        '        model: project-reviewer',
        '      architecture_critic:',
        '        enabled: true',
        '        required: true',
        '        model: project-reviewer',
        '      executability_critic:',
        '        enabled: true',
        '        required: true',
        '        model: project-reviewer',
        '      chair:',
        '        enabled: true',
        '        required: true',
        'llm:',
        '  providers:',
        '    projectapi:',
        '      protocol: openai_compatible',
        '      base_url: https://api.project.example/v1',
        '      token_env: BSL_FLOW_TEST_PROFILE_TOKEN',
        '  models:',
        '    project-reviewer:',
        '      provider: projectapi',
        '      model: vendor/project-model'
    ) -join "`n"
    $projectChairUnbound += "`n"

    # Profile with profile-only provider/model plus a chair binding. The raw
    # project parse of P2 becomes valid only after this merge injects the model.
    $profileOnlyYaml = @(
        'llm:',
        '  providers:',
        '    personalapi:',
        '      protocol: openai_compatible',
        '      base_url: https://api.personal.example/v1',
        '      token_env: BSL_FLOW_TEST_PROFILE_TOKEN',
        '  models:',
        '    personal-model:',
        '      provider: personalapi',
        '      model: vendor/personal-model',
        '      effort: high',
        'review:',
        '  council:',
        '    roles:',
        '      chair:',
        '        model: personal-model'
    ) -join "`n"
    $profileOnlyYaml += "`n"

    $projectPath = Join-Path $tempRoot 'project'
    $changePath = Join-Path $projectPath 'openspec\changes\demo'
    New-Item -ItemType Directory -Path $changePath -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $PackageRoot 'openspec\changes\api-specification-council\original-task.md') -Destination (Join-Path $changePath 'original-task.md')
    Copy-Item -LiteralPath (Join-Path $PackageRoot 'openspec\changes\api-specification-council\spec.md') -Destination (Join-Path $changePath 'spec.md')
    $projectFile = Join-Path $projectPath 'bsl-flow.yaml'
    [System.IO.File]::WriteAllText($projectFile, $projectSelfContained, $utf8)
    $projectBytesBefore = [System.IO.File]::ReadAllBytes($projectFile)

    # 1. Absent profile: project-only path resolution, byte-identical text and hash.
    $absentProfile = Join-Path $homeDir '.bsl-flow\config.yaml'
    Assert-True (-not (Test-Path -LiteralPath $absentProfile)) 'default profile path is absent in clean home'
    $resolvedDefault = Resolve-BSLFlowUserProfileConfigPath -HomeDirectory $homeDir
    Assert-True ($resolvedDefault -eq (Join-Path $homeDir '.bsl-flow/config.yaml')) 'default profile path resolves under the given home'
    $empty = Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $projectPath -UserProfilePath $absentProfile
    Assert-True (-not [bool]$empty.profile_used) 'absent profile is a valid empty state'
    Assert-True ($empty.text -ceq $projectSelfContained) 'absent profile keeps effective text byte-identical to project text'
    Assert-True ($empty.hash -ceq (Get-BSLFlowCouncilPolicyHash -PolicyText $projectSelfContained)) 'absent profile keeps the policy hash identical to the raw project text hash'
    Assert-True ($empty.hash -ceq (Get-BSLFlowCouncilPolicyHash $projectFile)) 'text hash matches the legacy file-based hash'
    Assert-True ([string]$empty.policy.roles.chair.model -ceq 'project-reviewer') 'absent profile leaves project role bindings untouched'

    # 2. Project overrides profile per named provider, model profile and role.
    $profileA = Join-Path $tempRoot 'profile-a.yaml'
    [System.IO.File]::WriteAllText($profileA, (@(
        'llm:',
        '  providers:',
        '    projectapi:',
        '      protocol: openai_compatible',
        '      base_url: https://api.evil.example/v1',
        '      token_env: BSL_FLOW_TEST_PROFILE_TOKEN',
        '    personalapi:',
        '      protocol: openai_compatible',
        '      base_url: https://api.personal.example/v1',
        '      token_env: BSL_FLOW_TEST_PROFILE_TOKEN',
        '  models:',
        '    project-reviewer:',
        '      provider: personalapi',
        '      model: vendor/evil-model',
        '    personal-model:',
        '      provider: personalapi',
        '      model: vendor/personal-model',
        'review:',
        '  council:',
        '    roles:',
        '      chair:',
        '        model: personal-model',
        '      intent_critic:',
        '        model: personal-model'
    ) -join "`n") + "`n", $utf8)
    $effective = Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $projectPath -UserProfilePath $profileA
    Assert-True ([bool]$effective.profile_used) 'valid profile is reported as used'
    Assert-True ([string]$effective.policy.roles.chair.model -ceq 'project-reviewer') 'project role.model displaces the profile binding'
    Assert-True ([string]$effective.policy.roles.intent_critic.model -ceq 'project-reviewer') 'project role.model displaces the profile binding for critics'
    Assert-True ($effective.policy.providers.Contains('personalapi') -and $effective.policy.providers.Contains('projectapi')) 'profile-only provider extends the provider set'
    Assert-True ([string]$effective.policy.providers.projectapi.base_url -ceq 'https://api.project.example/v1') 'project provider fields displace matching profile fields for the shared provider'
    Assert-True ([string]$effective.policy.models.'project-reviewer'.model -ceq 'vendor/project-model') 'project model profile fields displace matching profile fields'

    # 3. Canonical merged form is frozen: project text verbatim + canonical blocks.
    $mergedText = Merge-BSLFlowCouncilPolicyText -ProjectText $projectChairUnbound -ProfileText $profileOnlyYaml -ProfilePath (Join-Path $tempRoot 'profile-canonical.yaml')
    $expectedMerged = $projectChairUnbound + (@(
        'llm:',
        '  providers:',
        '    personalapi:',
        '      protocol: openai_compatible',
        '      base_url: https://api.personal.example/v1',
        '      token_env: BSL_FLOW_TEST_PROFILE_TOKEN',
        '  models:',
        '    personal-model:',
        '      provider: personalapi',
        '      model: vendor/personal-model',
        '      effort: high',
        'review:',
        '  council:',
        '    roles:',
        '      chair:',
        '        model: personal-model'
    ) -join "`n")
    $expectedMerged += "`n"
    Assert-True ($mergedText -ceq $expectedMerged) 'canonical merged form is frozen: verbatim project text plus canonical profile-only blocks'

    # 4. Hash sensitivity: no profile vs two different valid profiles.
    $profileB = Join-Path $tempRoot 'profile-b.yaml'
    [System.IO.File]::WriteAllText($profileB, (@(
        'llm:',
        '  providers:',
        '    personalapi:',
        '      protocol: openai_compatible',
        '      base_url: https://api.personal2.example/v1',
        '      token_env: BSL_FLOW_TEST_PROFILE_TOKEN',
        '  models:',
        '    personal-model:',
        '      provider: personalapi',
        '      model: vendor/personal-model-2',
        'review:',
        '  council:',
        '    roles:',
        '      chair:',
        '        model: personal-model'
    ) -join "`n") + "`n", $utf8)
    $effectiveB = Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $projectPath -UserProfilePath $profileB
    $hashNone = $empty.hash
    $hashA = $effective.hash
    $hashB = $effectiveB.hash
    Assert-True (($hashNone -cne $hashA) -and ($hashA -cne $hashB) -and ($hashNone -cne $hashB)) 'policy hash distinguishes no-profile and two different profiles'
    Assert-True ($effectiveB.text.StartsWith($projectSelfContained)) 'effective text starts with the verbatim project text'

    # 5. Profile parse never mutates the project file.
    $projectBytesAfter = [System.IO.File]::ReadAllBytes($projectFile)
    Assert-True (($projectBytesBefore | ForEach-Object { $_.ToString('x2') }) -join '' -ceq (($projectBytesAfter | ForEach-Object { $_.ToString('x2') }) -join '')) 'profile load and merge never mutate the project config'

    # 6. Profile fills a role the project leaves unbound; role bindings and the
    # admission gate resolve the profile-only provider with a token_env credential.
    $profileProject = Join-Path $tempRoot 'project-profile'
    $profileChange = Join-Path $profileProject 'openspec\changes\demo'
    New-Item -ItemType Directory -Path $profileChange -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $PackageRoot 'openspec\changes\api-specification-council\original-task.md') -Destination (Join-Path $profileChange 'original-task.md')
    Copy-Item -LiteralPath (Join-Path $PackageRoot 'openspec\changes\api-specification-council\spec.md') -Destination (Join-Path $profileChange 'spec.md')
    [System.IO.File]::WriteAllText((Join-Path $profileProject 'bsl-flow.yaml'), $projectChairUnbound, $utf8)
    $profileEffective = Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $profileProject -UserProfilePath $profileB
    Assert-True ([string]$profileEffective.policy.roles.chair.model -ceq 'personal-model') 'profile-only role model fills a role without a project binding'
    $snapshot = New-BSLFlowCouncilSnapshot -ChangeDir $profileChange -EvidenceText 'profile fixture' -PolicyHash $profileEffective.hash
    [System.Environment]::SetEnvironmentVariable('BSL_FLOW_TEST_PROFILE_TOKEN', 'fixture-token')
    $bindings = Get-BSLFlowCouncilRoleBindings -ProjectRoot $profileProject -Council $profileEffective.policy -Snapshot $snapshot
    $chairEntry = @($bindings | Where-Object { [string]$_.role_name -ceq 'chair' })[0]
    Assert-True ($null -ne $chairEntry) 'chair binding is built from the effective policy'
    Assert-True ([string]$chairEntry.profile.model -ceq 'vendor/personal-model-2') 'chair binding resolves the profile model id'
    Assert-True ([string]$chairEntry.binding.provider -ceq 'personalapi') 'chair binding resolves the profile provider'
    Assert-True ([string]$chairEntry.credential.credential_source -ceq 'env') 'profile role passes the gate with a token_env credential'
    Assert-True (@($bindings | Where-Object { [string]$_.credential.credential_source -ceq 'missing' }).Count -eq 0) 'admission gate has no missing credentials on the effective policy'
    Assert-True ([string]$chairEntry.binding.input_hashes.policy_hash -ceq $profileEffective.hash) 'bindings freeze the effective policy hash'

    # 7. Local overlay token beats the profile token_env.
    $localDir = Join-Path $profileProject '.bsl-flow'
    New-Item -ItemType Directory -Path $localDir -Force | Out-Null
    [System.IO.File]::WriteAllText((Join-Path $localDir 'providers.local.yaml'), "providers:`n  personalapi:`n    token: fixture-local-token`n", $utf8)
    try {
        $overlayBindings = Get-BSLFlowCouncilRoleBindings -ProjectRoot $profileProject -Council $profileEffective.policy -Snapshot $snapshot
        $overlayChair = @($overlayBindings | Where-Object { [string]$_.role_name -ceq 'chair' })[0]
        Assert-True ([string]$overlayChair.credential.credential_source -ceq 'local') 'local overlay token has priority over the profile token_env'
    }
    finally { Remove-Item -LiteralPath (Join-Path $localDir 'providers.local.yaml') -Force }

    # 8. Literal token in the profile fails closed naming file and key.
    $tokenProfile = Join-Path $tempRoot 'profile-token.yaml'
    [System.IO.File]::WriteAllText($tokenProfile, "llm:`n  providers:`n    personalapi:`n      protocol: openai_compatible`n      base_url: https://api.personal.example/v1`n      token: sk-literal-secret`n", $utf8)
    Assert-ThrowsWithName { Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $projectPath -UserProfilePath $tokenProfile } $tokenProfile 'llm.providers.personalapi.token' 'literal profile token is rejected naming file and key'

    # 9. Disallowed profile keys fail closed naming file and key; council does not start.
    $forbiddenProfile = Join-Path $tempRoot 'profile-forbidden.yaml'
    [System.IO.File]::WriteAllText($forbiddenProfile, "review:`n  council:`n    enabled: true`n", $utf8)
    Assert-ThrowsWithName { Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $projectPath -UserProfilePath $forbiddenProfile } $forbiddenProfile 'review.council.enabled' 'review.council.enabled in profile is rejected naming file and key'
    $reviewerProfile = Join-Path $tempRoot 'profile-reviewer.yaml'
    [System.IO.File]::WriteAllText($reviewerProfile, "review:`n  reviewer:`n    provider: opencode`n", $utf8)
    Assert-ThrowsWithName { Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $projectPath -UserProfilePath $reviewerProfile } $reviewerProfile 'review.reviewer' 'review.reviewer block in profile is rejected naming file and key'
    $unknownRoleProfile = Join-Path $tempRoot 'profile-unknown-role.yaml'
    [System.IO.File]::WriteAllText($unknownRoleProfile, "review:`n  council:`n    roles:`n      custom_role:`n        model: personal-model`n", $utf8)
    Assert-ThrowsWithName { Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $projectPath -UserProfilePath $unknownRoleProfile } $unknownRoleProfile 'review.council.roles.custom_role' 'unknown role in profile is rejected naming file and key'
    $routingProfile = Join-Path $tempRoot 'profile-routing.yaml'
    [System.IO.File]::WriteAllText($routingProfile, "review:`n  routing:`n    m_default: optional`n", $utf8)
    Assert-ThrowsWithName { Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $projectPath -UserProfilePath $routingProfile } $routingProfile 'review.routing' 'review.routing in profile is rejected naming file and key'
    $budgetProfile = Join-Path $tempRoot 'profile-budget.yaml'
    [System.IO.File]::WriteAllText($budgetProfile, "review:`n  council:`n    budget:`n      limit: 5`n", $utf8)
    Assert-ThrowsWithName { Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $projectPath -UserProfilePath $budgetProfile } $budgetProfile 'review.council.budget' 'review.council.budget in profile is rejected naming file and key'

    # 10. Unparseable profile entity fails the run with the profile file named.
    $badProtocolProfile = Join-Path $tempRoot 'profile-bad-protocol.yaml'
    [System.IO.File]::WriteAllText($badProtocolProfile, "llm:`n  providers:`n    personalapi:`n      protocol: not_a_protocol`n      base_url: https://api.personal.example/v1`n", $utf8)
    Assert-ThrowsWithName { Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $projectPath -UserProfilePath $badProtocolProfile } $badProtocolProfile 'protocol' 'invalid profile provider protocol fails with the profile file named'

    # 11. BSL_FLOW_USER_CONFIG override: valid path, missing file, unset default.
    $envProfile = Join-Path $tempRoot 'profile-env.yaml'
    [System.IO.File]::WriteAllText($envProfile, $profileOnlyYaml, $utf8)
    [System.Environment]::SetEnvironmentVariable('BSL_FLOW_USER_CONFIG', $envProfile)
    $viaEnv = Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $profileProject
    Assert-True ([bool]$viaEnv.profile_used -and [string]$viaEnv.policy.roles.chair.model -ceq 'personal-model') 'BSL_FLOW_USER_CONFIG selects the profile file and merges before the policy parse'
    [System.Environment]::SetEnvironmentVariable('BSL_FLOW_USER_CONFIG', (Join-Path $tempRoot 'missing-profile.yaml'))
    Assert-Throws { Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $projectPath } 'missing BSL_FLOW_USER_CONFIG file fails closed'
    $resolverError = $null
    try { Resolve-BSLFlowUserProfileConfigPath | Out-Null } catch { $resolverError = [string]$_.Exception.Message }
    Assert-True ($null -ne $resolverError -and $resolverError.Contains('BSL_FLOW_USER_CONFIG')) 'resolver error names BSL_FLOW_USER_CONFIG and the missing file'
    [System.Environment]::SetEnvironmentVariable('BSL_FLOW_USER_CONFIG', $null)
    $unset = Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $projectPath -UserProfilePath $absentProfile
    Assert-True (-not [bool]$unset.profile_used) 'unset BSL_FLOW_USER_CONFIG resolves the default path state'

    # 12. Comment-only profile is a valid empty state equivalent to no profile.
    $commentOnlyProfile = Join-Path $tempRoot 'profile-comments.yaml'
    [System.IO.File]::WriteAllText($commentOnlyProfile, "# no council settings here`n`n# just comments`n", $utf8)
    $commentOnly = Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $projectPath -UserProfilePath $commentOnlyProfile
    Assert-True (-not [bool]$commentOnly.profile_used) 'comment-only profile is equivalent to no profile'
    Assert-True ($commentOnly.text -ceq $projectSelfContained) 'comment-only profile keeps effective text byte-identical to project text'
    Assert-True ($commentOnly.hash -ceq (Get-BSLFlowCouncilPolicyHash -PolicyText $projectSelfContained)) 'comment-only profile keeps the policy hash identical to the raw project text hash'

    # 13. Field-level displacement: a project provider field displaces only the
    # matching profile field; profile fields the project omits survive. The
    # project keeps its base_url while protocol and token_env come from the
    # profile.
    $projectPartialProvider = (($projectSelfContained -replace "      protocol: openai_compatible\r?\n", '') -replace "      token_env: BSL_FLOW_TEST_PROFILE_TOKEN\r?\n", '')
    [System.IO.File]::WriteAllText($projectFile, $projectPartialProvider, $utf8)
    $partialProfile = Join-Path $tempRoot 'profile-partial-provider.yaml'
    [System.IO.File]::WriteAllText($partialProfile, "llm:`n  providers:`n    projectapi:`n      protocol: openai_responses`n      base_url: https://api.personal.example/v1`n      token_env: BSL_FLOW_TEST_PROFILE_TOKEN`n", $utf8)
    $partial = Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $projectPath -UserProfilePath $partialProfile
    Assert-True ([string]$partial.policy.providers.projectapi.token_env -ceq 'BSL_FLOW_TEST_PROFILE_TOKEN') 'partial provider override keeps the profile token_env the project omits'
    Assert-True ([string]$partial.policy.providers.projectapi.protocol -ceq 'openai_responses') 'partial provider override keeps the profile protocol the project omits'
    Assert-True ([string]$partial.policy.providers.projectapi.base_url -ceq 'https://api.project.example/v1') 'partial provider override keeps the project base_url over the profile value'
    [System.IO.File]::WriteAllText($projectFile, $projectSelfContained, $utf8)

    # 14. Partial model-profile override: the project sets only the provider,
    # the profile supplies model, effort and cost; the merged profile parses
    # and resolves the chair role.
    $projectPartialModel = @(
        'review:',
        '  council:',
        '    enabled: true',
        '    roles:',
        '      chair:',
        '        enabled: true',
        '        required: true',
        '        model: reviewer',
        '      intent_critic:',
        '        enabled: false',
        '        required: false',
        '      architecture_critic:',
        '        enabled: false',
        '        required: false',
        '      executability_critic:',
        '        enabled: false',
        '        required: false',
        'llm:',
        '  providers:',
        '    projectapi:',
        '      protocol: openai_compatible',
        '      base_url: https://api.project.example/v1',
        '      token_env: BSL_FLOW_TEST_PROFILE_TOKEN',
        '  models:',
        '    reviewer:',
        '      provider: projectapi'
    ) -join "`n"
    $projectPartialModel += "`n"
    [System.IO.File]::WriteAllText($projectFile, $projectPartialModel, $utf8)
    $partialModelProfile = Join-Path $tempRoot 'profile-partial-model.yaml'
    [System.IO.File]::WriteAllText($partialModelProfile, "llm:`n  models:`n    reviewer:`n      model: vendor/profile-model`n      effort: high`n      cost_estimate_usd: 0.05`n", $utf8)
    $partialModel = Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $projectPath -UserProfilePath $partialModelProfile
    Assert-True ([string]$partialModel.policy.models.reviewer.provider -ceq 'projectapi') 'partial model profile keeps the project provider'
    Assert-True ([string]$partialModel.policy.models.reviewer.model -ceq 'vendor/profile-model') 'partial model profile keeps the profile model the project omits'
    Assert-True ([string]$partialModel.policy.models.reviewer.effort -ceq 'high') 'partial model profile keeps the profile effort the project omits'
    Assert-True ([double]$partialModel.policy.models.reviewer.cost_estimate_usd -eq 0.05) 'partial model profile keeps the profile cost estimate the project omits'
    Assert-True ([string]$partialModel.policy.roles.chair.model -ceq 'reviewer') 'partial model profile leaves the project role binding untouched'
    [System.IO.File]::WriteAllText($projectFile, $projectSelfContained, $utf8)

    # 15. Canonical hash form is frozen: recursively sorted keys, computed
    # fields removed, no insignificant whitespace; reproducible and equal to
    # the SHA-256 of the exact canonical JSON bytes.
    $knownPolicy = [ordered]@{
        transport_capability_version = 1
        council_schema_version = 1
        enabled = $true
        max_parallel = 2
        allow_local_http = $false
        legacy_mode = 'block'
        request_timeout_seconds = 300
        providers = [ordered]@{
            alpha = [ordered]@{
                protocol = 'openai_compatible'
                base_url = 'https://api.example.com/v1'
                token_env = 'ALPHA_TOKEN'
                endpoint = [ordered]@{ scheme = 'https'; host = 'api.example.com'; port = 443; base_path = '/v1' }
                transport_capability_version = 1
            }
        }
        models = [ordered]@{}
        roles = [ordered]@{
            chair = [ordered]@{ enabled = $true; required = $true; model = 'm1'; fallback = 'current_agent' }
        }
        budget = $null
    }
    $expectedCanonicalJson = '{"allow_local_http":false,"budget":null,"council_schema_version":1,"enabled":true,"legacy_mode":"block","max_parallel":2,"models":{},"providers":{"alpha":{"base_url":"https://api.example.com/v1","protocol":"openai_compatible","token_env":"ALPHA_TOKEN"}},"request_timeout_seconds":300,"roles":{"chair":{"enabled":true,"fallback":"current_agent","model":"m1","required":true}}}'
    $knownJson = ConvertTo-BSLFlowCouncilCanonicalPolicyJson -Policy $knownPolicy
    Assert-True ($knownJson -ceq $expectedCanonicalJson) 'canonical policy JSON is recursively key-sorted without computed fields or insignificant whitespace'
    $knownHash = Get-BSLFlowCouncilCanonicalPolicyHash -Policy $knownPolicy
    Assert-True ($knownHash -ceq (Get-BSLFlowBytesSha256 ($utf8.GetBytes($expectedCanonicalJson)))) 'canonical policy hash is SHA-256 over the UTF-8 canonical JSON'
    Assert-True ($knownHash -ceq (Get-BSLFlowCouncilCanonicalPolicyHash -Policy $knownPolicy)) 'canonical policy hash is reproducible for the same policy'

    # 16. Hash algorithm selection: a profile contribution or a relevant local
    # overlay contribution switches to the canonical hash; with no contribution
    # the raw project text hash is kept for backward compatibility.
    $hashProject = Join-Path $tempRoot 'project-hash'
    New-Item -ItemType Directory -Path $hashProject -Force | Out-Null
    [System.IO.File]::WriteAllText((Join-Path $hashProject 'bsl-flow.yaml'), $projectSelfContained, $utf8)
    $absentPath = Join-Path $tempRoot 'absent-profile.yaml'
    $noContribution = Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $hashProject -UserProfilePath $absentPath
    Assert-True ($noContribution.hash -ceq (Get-BSLFlowCouncilPolicyHash -PolicyText $projectSelfContained)) 'no profile and no overlay contribution keeps the raw project text hash'
    $hashLocal = Join-Path $hashProject '.bsl-flow'
    New-Item -ItemType Directory -Path $hashLocal -Force | Out-Null
    [System.IO.File]::WriteAllText((Join-Path $hashLocal 'providers.local.yaml'), "providers:`n  projectapi:`n    token: fixture-local-token`n", $utf8)
    try {
        $overlayHash = Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $hashProject -UserProfilePath $absentPath
        Assert-True ($overlayHash.hash -ceq (Get-BSLFlowCouncilCanonicalPolicyHash -Policy $overlayHash.policy)) 'relevant overlay token contribution switches to the canonical policy hash'
        Assert-True ($overlayHash.hash -cne (Get-BSLFlowCouncilPolicyHash -PolicyText $projectSelfContained)) 'canonical overlay hash differs from the raw project text hash'
    }
    finally { Remove-Item -LiteralPath (Join-Path $hashLocal 'providers.local.yaml') -Force }
    [System.IO.File]::WriteAllText((Join-Path $hashLocal 'providers.local.yaml'), "providers:`n  projectapi:`n    base_url: https://mirror.example.com/v1`n", $utf8)
    try {
        $overlayBaseUrl = Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $hashProject -UserProfilePath $absentPath
        Assert-True ($overlayBaseUrl.hash -ceq (Get-BSLFlowCouncilCanonicalPolicyHash -Policy $overlayBaseUrl.policy)) 'relevant overlay base_url contribution switches to the canonical policy hash'
    }
    finally { Remove-Item -LiteralPath (Join-Path $hashLocal 'providers.local.yaml') -Force }
    [System.IO.File]::WriteAllText((Join-Path $hashLocal 'providers.local.yaml'), "providers:`n  unknownapi:`n    token: fixture-local-token`n", $utf8)
    try {
        $irrelevantOverlay = Get-BSLFlowCouncilEffectivePolicy -ProjectRoot $hashProject -UserProfilePath $absentPath
        Assert-True ($irrelevantOverlay.hash -ceq (Get-BSLFlowCouncilPolicyHash -PolicyText $projectSelfContained)) 'overlay entry for a provider outside the config keeps the raw project text hash'
    }
    finally { Remove-Item -LiteralPath (Join-Path $hashLocal 'providers.local.yaml') -Force }
    Remove-Item -LiteralPath $hashProject -Recurse -Force

    "ALL_COUNCIL_PROFILE_PASSED=$passed"
}
finally {
    if ($null -ne $savedProfileEnv) { [System.Environment]::SetEnvironmentVariable('BSL_FLOW_USER_CONFIG', $savedProfileEnv) }
    else { [System.Environment]::SetEnvironmentVariable('BSL_FLOW_USER_CONFIG', $null) }
    if ($null -ne $savedTokenEnv) { [System.Environment]::SetEnvironmentVariable('BSL_FLOW_TEST_PROFILE_TOKEN', $savedTokenEnv) }
    else { [System.Environment]::SetEnvironmentVariable('BSL_FLOW_TEST_PROFILE_TOKEN', $null) }
    if (Test-Path -LiteralPath $tempRoot -PathType Container) { Remove-Item -LiteralPath $tempRoot -Recurse -Force }
}
