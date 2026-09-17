#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)))) }
$skill = Join-Path $PackageRoot 'global\skills\1c-spec-review'
. (Join-Path $skill 'scripts\Invoke-CouncilReview.ps1')

$passed = 0
function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "FAIL: $Message" }
    $script:passed++
    "PASS $Message"
}

function New-TempProject([string]$ConfigText) {
    $root = Join-Path ([IO.Path]::GetTempPath()) ('council-route-' + [guid]::NewGuid().ToString('N'))
    $change = Join-Path $root 'openspec\changes\demo'
    New-Item -ItemType Directory -Path $change -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $PackageRoot 'openspec\changes\api-specification-council\original-task.md') -Destination (Join-Path $change 'original-task.md')
    Copy-Item -LiteralPath (Join-Path $PackageRoot 'openspec\changes\api-specification-council\spec.md') -Destination (Join-Path $change 'spec.md')
    [System.IO.File]::WriteAllText((Join-Path $root 'bsl-flow.yaml'), $ConfigText)
    return $root
}

function Find-ByteSequence([byte[]]$Bytes, [byte[]]$Needle) {
    if ($Needle.Length -eq 0) { return 0 }
    for ($offset = 0; $offset -le ($Bytes.Length - $Needle.Length); $offset++) {
        $matches = $true
        for ($index = 0; $index -lt $Needle.Length; $index++) {
            if ($Bytes[$offset + $index] -ne $Needle[$index]) { $matches = $false; break }
        }
        if ($matches) { return $offset }
    }
    return -1
}

function Read-LoopbackHttpBody([System.Net.Sockets.TcpClient]$Client) {
    $stream = $Client.GetStream()
    $buffer = New-Object byte[] 8192
    $all = [IO.MemoryStream]::new()
    $headerEnd = -1
    $separator = [byte[]](13, 10, 13, 10)
    while ($headerEnd -lt 0) {
        $read = $stream.Read($buffer, 0, $buffer.Length)
        if ($read -le 0) { throw 'Loopback provider closed before request headers.' }
        $all.Write($buffer, 0, $read)
        $headerEnd = Find-ByteSequence $all.ToArray() $separator
    }
    $bytes = $all.ToArray()
    $headerText = [Text.Encoding]::ASCII.GetString($bytes, 0, $headerEnd)
    $lengthMatch = [regex]::Match($headerText, '(?im)^Content-Length:\s*(\d+)\s*$')
    if (-not $lengthMatch.Success) { throw 'Loopback provider request has no Content-Length.' }
    $length = [int]$lengthMatch.Groups[1].Value
    $bodyStart = $headerEnd + $separator.Length
    while (($bytes.Length - $bodyStart) -lt $length) {
        $read = $stream.Read($buffer, 0, $buffer.Length)
        if ($read -le 0) { throw 'Loopback provider closed before request body.' }
        $all.Write($buffer, 0, $read)
        $bytes = $all.ToArray()
    }
    return [Text.Encoding]::UTF8.GetString($bytes, $bodyStart, $length)
}

function Send-LoopbackHttpJson([System.Net.Sockets.TcpClient]$Client, $Payload) {
    $utf8 = [Text.UTF8Encoding]::new($false)
    $body = ConvertTo-Json -InputObject $Payload -Depth 50 -Compress
    $bodyBytes = $utf8.GetBytes($body)
    $headers = "HTTP/1.1 200 OK`r`nContent-Type: application/json`r`nContent-Length: $($bodyBytes.Length)`r`nConnection: close`r`n`r`n"
    $headerBytes = [Text.Encoding]::ASCII.GetBytes($headers)
    $stream = $Client.GetStream()
    $stream.Write($headerBytes, 0, $headerBytes.Length)
    $stream.Write($bodyBytes, 0, $bodyBytes.Length)
    $stream.Flush()
}

function New-PublicManagedState([string]$ProjectRoot, [string]$TaskId) {
    $request = [ordered]@{
        schema_version = 1; request_id = $TaskId
        prompt = 'Review the specification through the public managed council entry point.'
        mode = 'analysis_only'; analysis_goal = 'analysis'; complexity = 'L'; risk = 'high'
        impact_flags = @(); criteria = @()
        provenance = [ordered]@{ source = 'user'; reference = 'public acceptance fixture'; text = 'public managed council fixture' }
        models = [ordered]@{ worker = 'fixture-worker'; worker_effort = 'medium'; reviewer = 'fixture-reviewer'; reviewer_effort = 'medium' }
        source_paths = @('.'); max_attempts = 16; timeout_seconds = 1800; max_source_repairs = 0
    }
    return [ordered]@{
        schema_version = 1; task_id = $TaskId; revision = 1; previous_sha256 = ('0' * 64)
        project_path = [IO.Path]::GetFullPath($ProjectRoot).TrimEnd('\', '/')
        worker_path = Join-Path $ProjectRoot '.bsl-flow/worktrees/public-fixture'
        baseline = 'public-fixture-baseline'; request = $request; request_hash = ('1' * 64)
        intent_revision = 1; authorization_revision = 1; intent_hash = ('2' * 64)
        policy_hash = ('3' * 64); policy_files = @(); policy_rules = [ordered]@{ s_review_required = $false }
        classification = [ordered]@{ complexity = 'L'; risk = 'high'; impact_flags = @(); rationale = 'Public acceptance fixture.' }
        status = 'ready'; stage = 'spec_review'; active_attempt = $null; unresolved_effect = $null
        attempts = @(); evidence = @(); events = @(); question = $null; blockers = @(); acceptances = @()
        created_at = '2026-09-12T00:00:00Z'; updated_at = '2026-09-12T00:00:00Z'; correction_rounds = 0
        repair = [ordered]@{ rounds = 0; pending_failure = $null; last_source_sha256 = $null; diagnosis_attempt = $null }
    }
}

$template = Get-Content -Raw -LiteralPath (Join-Path $PackageRoot 'global\skills\1c-init-project\assets\project\bsl-flow.yaml')
$taskSkill = Join-Path $PackageRoot 'global\skills\1c-task'
. (Join-Path $taskSkill 'scripts\Task.Storage.ps1')
. (Join-Path $taskSkill 'scripts\Task.Memory.ps1')
. (Join-Path $taskSkill 'scripts\Task.Architecture.ps1')
$expectedManagedArchitecture = Get-BFArchitectureBundle 'spec_review' $PackageRoot
$expectedManagedAdr4 = @($expectedManagedArchitecture.decisions | Where-Object { [string]$_.id -ceq 'ADR-4' })
if ($expectedManagedAdr4.Count -ne 1) { throw 'ADR-4 is required in the public managed evidence fixture.' }

# 1. Council default routes with a dry-run plan and no network.
$proj = New-TempProject $template
try {
    $plan = Invoke-BSLFlowCouncilReview -ProjectPath $proj -ChangeName 'demo' -DryRun
    Assert-True ([bool]$plan.dry_run) 'council dry-run plans without network'
    Assert-True (@($plan.plan).Count -eq 4) 'four enabled roles planned (3 critics + chair)'
    $routes = @($plan.plan | ForEach-Object { $_.route })
    Assert-True ($routes -contains 'current_agent_fallback') 'missing credentials plan fallback with reason'
    Assert-True ([int]$plan.manifest_requirements -ge 10) 'plan carries requirement manifest'
}
finally { Remove-Item -LiteralPath $proj -Recurse -Force -ErrorAction SilentlyContinue }

# 2. Explicit legacy opencode is a migration blocker, not a silent council run.
$legacy = $template + "`nreview:`n  reviewer:`n    provider: opencode`n"
$proj2 = New-TempProject $legacy
try {
    try { $null = Invoke-BSLFlowCouncilReview -ProjectPath $proj2 -ChangeName 'demo' -DryRun; throw 'FAIL legacy routed silently' }
    catch {
        Assert-True ([string]$_.Exception.Message -match 'BF_MIGRATION_BLOCKED') 'legacy opencode gives migration blocker'
    }
}
finally { Remove-Item -LiteralPath $proj2 -Recurse -Force -ErrorAction SilentlyContinue }

# 3. Explicit compat mode skips the council route.
$compat = $legacy -replace 'legacy_mode: block', 'legacy_mode: opencode_compat'
$proj3 = New-TempProject $compat
try {
    try { $null = Invoke-BSLFlowCouncilReview -ProjectPath $proj3 -ChangeName 'demo' -DryRun; throw 'FAIL compat dispatched council' }
    catch {
        Assert-True ([string]$_.Exception.Message -match 'compatibility route') 'compat mode skips council dispatch'
    }
}
finally { Remove-Item -LiteralPath $proj3 -Recurse -Force -ErrorAction SilentlyContinue }

# 4. Disabled council refuses the council route.
$disabled = $template -replace 'council:\r?\n    enabled: true', "council:`n    enabled: false"
$proj4 = New-TempProject $disabled
try {
    try { $null = Invoke-BSLFlowCouncilReview -ProjectPath $proj4 -ChangeName 'demo' -DryRun; throw 'FAIL disabled routed' }
    catch {
        Assert-True ([string]$_.Exception.Message -match 'requires review.council.enabled') 'disabled council refuses route'
    }
}
finally { Remove-Item -LiteralPath $proj4 -Recurse -Force -ErrorAction SilentlyContinue }

# 5. Unknown council fields are rejected, not silently accepted.
$unknownField = $template -replace '      protocol: openai_responses', "      protocol: openai_responses`n      unexpected_field: true"
$proj5 = New-TempProject $unknownField
try {
    try { $null = Invoke-BSLFlowCouncilReview -ProjectPath $proj5 -ChangeName 'demo' -DryRun; throw 'FAIL unknown field routed' }
    catch {
        Assert-True ([string]$_.Exception.Message -match 'Unknown provider field') 'unknown provider field rejected'
    }
}
finally { Remove-Item -LiteralPath $proj5 -Recurse -Force -ErrorAction SilentlyContinue }

# 6. Local base_url override is validated and applied to the binding.
$proj6 = New-TempProject $template
try {
    $localDir = Join-Path $proj6 '.bsl-flow'
    New-Item -ItemType Directory -Path $localDir -Force | Out-Null
    [System.IO.File]::WriteAllText((Join-Path $localDir 'providers.local.yaml'), "providers:`n  deepseek:`n    base_url: https://mirror.example.com/v1`n")
    $plan6 = Invoke-BSLFlowCouncilReview -ProjectPath $proj6 -ChangeName 'demo' -DryRun
    . (Join-Path $skill 'scripts\Council.Engine.ps1')
    $latestFlash = Get-BSLFlowCouncilLatestAttempt -RunRoot $plan6.run_root -Role 'intent_critic'
    Assert-True ([string]$latestFlash.binding.endpoint.host -eq 'mirror.example.com') 'local base_url override applied'
}
finally { Remove-Item -LiteralPath $proj6 -Recurse -Force -ErrorAction SilentlyContinue }

# 7. The public entry point must consume a registered managed state and run the
# real public council/transport route. A loopback OpenAI-compatible endpoint is
# used so the test proves HTTP dispatch without depending on external services.
    $publicTemp = Join-Path ([IO.Path]::GetTempPath()) ('council-public-' + [guid]::NewGuid().ToString('N'))
    $publicChangeName = $null
    $invalidChangeName = $null
$publicListener = $null
$publicProcess = $null
$publicTokenName = 'PUBLIC_COUNCIL_TOKEN'
$oldPublicToken = [Environment]::GetEnvironmentVariable($publicTokenName, 'Process')
try {
    $publicChangeName = 'bsl-flow-' + ([guid]::NewGuid().ToString())
    $publicChange = Join-Path $publicTemp ('openspec\changes\' + $publicChangeName)
    $taskId = $publicChangeName.Substring(9)
    $invalidChangeName = 'bsl-flow-' + ([guid]::NewGuid().ToString())
    $invalidChange = Join-Path $publicTemp ('openspec\changes\' + $invalidChangeName)
    $invalidTaskId = $invalidChangeName.Substring(9)
    New-Item -ItemType Directory -Path (Join-Path $publicTemp '.bsl-flow\tasks') -Force | Out-Null
    New-Item -ItemType Directory -Path $publicChange -Force | Out-Null
    New-Item -ItemType Directory -Path $invalidChange -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $PackageRoot 'openspec\changes\api-specification-council\original-task.md') -Destination (Join-Path $publicChange 'original-task.md')
    Copy-Item -LiteralPath (Join-Path $PackageRoot 'openspec\changes\api-specification-council\spec.md') -Destination (Join-Path $publicChange 'spec.md')
    Copy-Item -LiteralPath (Join-Path $PackageRoot 'openspec\changes\api-specification-council\original-task.md') -Destination (Join-Path $invalidChange 'original-task.md')
    Copy-Item -LiteralPath (Join-Path $PackageRoot 'openspec\changes\api-specification-council\spec.md') -Destination (Join-Path $invalidChange 'spec.md')

    $publicListener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $publicListener.Start()
    $publicPort = ([Net.IPEndPoint]$publicListener.LocalEndpoint).Port
    $publicConfig = @"
llm:
  providers:
    fixture:
      protocol: openai_compatible
      base_url: http://127.0.0.1:$publicPort/v1
      token_env: $publicTokenName
  models:
    flash:
      provider: fixture
      model: fixture-model
      effort: medium
review:
  enabled: true
  input:
    max_file_bytes: 262144
  council:
    enabled: true
    max_parallel: 1
    allow_local_http: true
    legacy_mode: block
    roles:
      brainstorm:
        enabled: false
        required: false
        model: flash
        fallback: block
      intent_critic:
        enabled: true
        required: true
        model: flash
        fallback: block
      architecture_critic:
        enabled: true
        required: true
        model: flash
        fallback: block
      executability_critic:
        enabled: true
        required: true
        model: flash
        fallback: block
      chair:
        enabled: true
        required: true
        model: flash
        fallback: block
"@
    [IO.File]::WriteAllText((Join-Path $publicTemp 'bsl-flow.yaml'), $publicConfig, [Text.UTF8Encoding]::new($false))

    $managedState = New-PublicManagedState $publicTemp $taskId
    $stateDirectory = Join-Path $publicTemp ('.bsl-flow\tasks\' + $taskId)
    $revisionDirectory = Join-Path $stateDirectory 'revisions'
    New-Item -ItemType Directory -Path $revisionDirectory -Force | Out-Null
    $statePath = Join-Path $revisionDirectory '000001.json'
    [IO.File]::WriteAllText($statePath, (ConvertTo-Json -InputObject $managedState -Depth 50), [Text.UTF8Encoding]::new($false))

    # The wrapper turns the public script's returned object into one parseable
    # line; all council files and transport diagnostics remain in the fixture.
    $publicWrapper = Join-Path $publicTemp 'invoke-public.ps1'
    $publicScript = Join-Path $skill 'scripts\Invoke-1CSpecReview.ps1'
    $wrapperText = @"
param([string]`$PublicScript,[string]`$Project,[string]`$Change,[string]`$State)
`$result = & `$PublicScript -ProjectPath `$Project -ChangeName `$Change -ManagedStatePath `$State -ForceReview -ForceReplaceReview
`$result | ConvertTo-Json -Depth 50 -Compress
"@
    [IO.File]::WriteAllText($publicWrapper, $wrapperText, [Text.UTF8Encoding]::new($false))
    [Environment]::SetEnvironmentVariable($publicTokenName, 'public-fixture-token', 'Process')

    $roles = [Collections.Generic.List[string]]::new()
    $managedEvidenceSeen = $false
    # An unregistered state is rejected before the public adapter is built. Run
    # this before the successful publication so a prepared package cannot take
    # the early recovery branch and bypass state validation.
    $unsupportedStatePath = Join-Path $publicTemp 'unsupported-state.json'
    $invalidManagedState = New-PublicManagedState $publicTemp $invalidTaskId
    $invalidManagedState.request.prompt = 'Unsupported state must be rejected before any public council dispatch.'
    [IO.File]::WriteAllText($unsupportedStatePath, (ConvertTo-Json -InputObject $invalidManagedState -Depth 50), [Text.UTF8Encoding]::new($false))
    $badPsi = [Diagnostics.ProcessStartInfo]::new()
    $badPsi.FileName = (Join-Path $PSHOME 'pwsh.exe'); $badPsi.UseShellExecute = $false; $badPsi.CreateNoWindow = $true
    $badPsi.RedirectStandardOutput = $true; $badPsi.RedirectStandardError = $true
    foreach ($arg in @('-NoProfile', '-File', $publicWrapper, $publicScript, $publicTemp, $invalidChangeName, $unsupportedStatePath)) { [void]$badPsi.ArgumentList.Add($arg) }
    $badProcess = [Diagnostics.Process]::new(); $badProcess.StartInfo = $badPsi
    if (-not $badProcess.Start()) { throw 'Unsupported-state public process did not start.' }
    $badStdout = $badProcess.StandardOutput.ReadToEnd(); $badStderr = $badProcess.StandardError.ReadToEnd(); $badProcess.WaitForExit()
    Assert-True ($badProcess.ExitCode -ne 0 -and ($badStderr + $badStdout) -match 'registered task state') 'unsupported managed state blocks before dispatch'
    Assert-True (@($roles).Count -eq 0 -and -not $publicListener.Pending()) 'unsupported managed state made no transport request'

    $psi = [Diagnostics.ProcessStartInfo]::new()
    $psi.FileName = (Join-Path $PSHOME 'pwsh.exe')
    $psi.UseShellExecute = $false; $psi.CreateNoWindow = $true
    $psi.RedirectStandardOutput = $true; $psi.RedirectStandardError = $true
    foreach ($arg in @('-NoProfile', '-File', $publicWrapper, $publicScript, $publicTemp, $publicChangeName, $statePath)) { [void]$psi.ArgumentList.Add($arg) }
    $publicProcess = [Diagnostics.Process]::new(); $publicProcess.StartInfo = $psi
    if (-not $publicProcess.Start()) { throw 'Public council process did not start.' }

    for ($requestIndex = 0; $requestIndex -lt 4; $requestIndex++) {
        $acceptTask = $publicListener.AcceptTcpClientAsync()
        if (-not $acceptTask.Wait(5000)) {
            $diagnostic = ''
            if (-not $publicProcess.HasExited) { $publicProcess.Kill(); $publicProcess.WaitForExit() }
            $diagnostic = (($publicProcess.StandardError.ReadToEnd()) + ' ' + ($publicProcess.StandardOutput.ReadToEnd())).Trim()
            throw "Timed out waiting for public council transport request. Child=$($publicProcess.HasExited) Exit=$($publicProcess.ExitCode) $diagnostic"
        }
        $client = $acceptTask.Result
        try {
            $bodyText = Read-LoopbackHttpBody $client
            $body = $bodyText | ConvertFrom-Json -ErrorAction Stop
            $promptText = [string]$body.messages[0].content
            $evidenceMatch = [regex]::Match($promptText, '(?s)<<<BEGIN UNTRUSTED DATA: evidence>>>\r?\n(?<evidence>.*?)\r?\n<<<END UNTRUSTED DATA: evidence>>>')
            if ($evidenceMatch.Success) {
                try {
                    $managedEvidence = $evidenceMatch.Groups['evidence'].Value | ConvertFrom-Json -ErrorAction Stop
                    $adr4 = @($managedEvidence.architecture.decisions | Where-Object { [string]$_.id -ceq 'ADR-4' })
                    if ($adr4.Count -eq 1 -and
                        [string]$adr4[0].id -ceq [string]$expectedManagedAdr4[0].id -and
                        [string]$adr4[0].title -ceq [string]$expectedManagedAdr4[0].title -and
                        [string]$adr4[0].excerpt -ceq [string]$expectedManagedAdr4[0].excerpt) {
                        $managedEvidenceSeen = $true
                    }
                }
                catch { }
            }
            $roleMatch = [regex]::Match($promptText, '(?m)^Role: ([A-Za-z0-9_]+)$')
            if (-not $roleMatch.Success) { throw 'Public council prompt did not expose a role contract.' }
            $role = $roleMatch.Groups[1].Value
            [void]$roles.Add($role)
            if ($role -eq 'chair') {
                $specMatch = [regex]::Match($promptText, '(?s)BEGIN UNTRUSTED DATA: spec\.md>>>\r?\n(?<spec>.*?)\r?\n<<<END UNTRUSTED DATA: spec\.md>>>')
                $aggregateMatch = [regex]::Match($promptText, '(?s)BEGIN TRUSTED AGGREGATES: member results>>>\r?\n(?<aggregate>.*?)\r?\n<<<END TRUSTED AGGREGATES: member results>>>')
                if (-not $specMatch.Success -or -not $aggregateMatch.Success) { throw 'Public chair prompt omitted spec or member aggregate.' }
                $aggregate = $aggregateMatch.Groups['aggregate'].Value | ConvertFrom-Json -ErrorAction Stop
                $refs = @($aggregate.requirements | ForEach-Object { [ordered]@{ id = [string]$_.id; final_refs = @('Требуемое поведение / 1') } })
                $modelPayload = [ordered]@{
                    verdict = 'PASS'; decisions = @(); protected_decisions = @(); requirement_refs = @($refs)
                    final_spec_text = $specMatch.Groups['spec'].Value; final_design_text = $null
                }
            }
            else {
                $modelPayload = [ordered]@{ role = $role; verdict = 'PASS'; findings = @(); do_not_change = @(); needs_input_questions = @() }
            }
            $envelope = [ordered]@{
                id = ('fixture-' + $requestIndex); object = 'chat.completion'; model = 'fixture-model'
                choices = @([ordered]@{ index = 0; message = [ordered]@{ role = 'assistant'; content = (ConvertTo-Json -InputObject $modelPayload -Depth 30 -Compress) }; finish_reason = 'stop' })
                usage = [ordered]@{ prompt_tokens = 1; completion_tokens = 1; total_tokens = 2 }
            }
            Send-LoopbackHttpJson $client $envelope
        }
        finally { $client.Dispose() }
    }
    $publicStdout = $publicProcess.StandardOutput.ReadToEnd()
    $publicStderr = $publicProcess.StandardError.ReadToEnd()
    $publicProcess.WaitForExit()
    if ($publicProcess.ExitCode -ne 0) { throw "Public council entry failed: $publicStderr $publicStdout" }
    $publicResult = $publicStdout.Trim() | ConvertFrom-Json -ErrorAction Stop
    Assert-True ([string]$publicResult.Route -eq 'council' -and [bool]$publicResult.ReviewRequired) 'public managed entry uses council route'
    $publicReviewPath = Join-Path $publicChange 'review.json'
    $publicFinalValidationPath = Join-Path $publicChange 'final-validation.json'
    Assert-True ((Get-Content -Raw -LiteralPath $publicReviewPath | ConvertFrom-Json).schema_version -eq 2) 'public managed entry writes council review v2'
    Assert-True ((Test-Path -LiteralPath $publicFinalValidationPath -PathType Leaf) -and [bool](Get-Content -Raw -LiteralPath $publicFinalValidationPath | ConvertFrom-Json).passed) 'public managed entry writes passing final validation'
    Assert-True (@($roles).Count -eq 4 -and 'brainstorm' -notin @($roles)) 'public managed entry dispatches exactly three critics and chair'
    Assert-True $managedEvidenceSeen 'public council prompts carry ADR-4 from managed evidence'

}
finally {
    if ($null -ne $publicProcess -and -not $publicProcess.HasExited) { $publicProcess.Kill(); $publicProcess.WaitForExit() }
    if ($null -ne $publicListener) { $publicListener.Stop() }
    if ($null -eq $oldPublicToken) { [Environment]::SetEnvironmentVariable($publicTokenName, $null, 'Process') }
    else { [Environment]::SetEnvironmentVariable($publicTokenName, $oldPublicToken, 'Process') }
    if (Test-Path -LiteralPath $publicTemp) { Remove-Item -LiteralPath $publicTemp -Recurse -Force -ErrorAction SilentlyContinue }
}

"ALL_STAGE5_ROUTING_PASSED=$passed"
