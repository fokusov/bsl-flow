#Requires -Version 7.0
[CmdletBinding()]
param([string]$PackageRoot)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($PackageRoot)) { $PackageRoot = Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)))) }
$skill = Join-Path $PackageRoot 'global\skills\1c-spec-review'
. (Join-Path $skill 'scripts\Council.Transport.ps1')

$passed = 0
function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw "FAIL: $Message" }
    $script:passed++
    "PASS $Message"
}
function Assert-Throws([scriptblock]$Block, [string]$Needle, [string]$Message) {
    try { & $Block } catch {
        if ([string]$_.Exception.Message -notmatch [regex]::Escape($Needle)) { throw "FAIL ${Message}: wrong error: $($_.Exception.Message)" }
        $script:passed++; "PASS $Message"; return
    }
    throw "FAIL (no throw): $Message"
}

function Get-CouncilFreeLoopbackPort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    try {
        $listener.Start()
        return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    }
    finally { $listener.Stop() }
}

function Start-CouncilLoopbackServer {
    param(
        [Parameter(Mandatory)][int]$Port,
        [Parameter(Mandatory)][string]$ReadyPath,
        [Parameter(Mandatory)][string]$HeadersPath,
        [Parameter(Mandatory)][string]$BodyPath,
        [Parameter(Mandatory)][int]$StatusCode,
        [Parameter(Mandatory)][string]$ResponseBody,
        [string]$Location = '',
        [int]$DelayMilliseconds = 0,
        [int]$BodyDelayMilliseconds = 0,
        [int]$AcceptTimeoutMilliseconds = 10000,
        [string]$ErrorPath = ''
    )
    $server = {
        param($Port, $ReadyPath, $HeadersPath, $BodyPath, $StatusCode, $ResponseBody, $Location, $DelayMilliseconds, $BodyDelayMilliseconds, $AcceptTimeoutMilliseconds, $ErrorPath)
        $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $Port)
        $client = $null
        $stream = $null
        try {
            $listener.Start()
            [System.IO.File]::WriteAllText($ReadyPath, 'ready')
            $accept = $listener.AcceptTcpClientAsync()
            if (-not $accept.Wait($AcceptTimeoutMilliseconds)) { return }
            $client = $accept.Result
            $stream = $client.GetStream()
            $headerBuffer = [System.IO.MemoryStream]::new()
            $one = [byte[]]::new(1)
            try {
                while ($headerBuffer.Length -lt 65536) {
                    $read = $stream.Read($one, 0, 1)
                    if ($read -le 0) { break }
                    $headerBuffer.Write($one, 0, $read)
                    $headerBytes = $headerBuffer.ToArray()
                    $length = $headerBytes.Length
                    if ($length -ge 4 -and $headerBytes[$length - 4] -eq 13 -and $headerBytes[$length - 3] -eq 10 -and $headerBytes[$length - 2] -eq 13 -and $headerBytes[$length - 1] -eq 10) { break }
                }
                $utf8 = [System.Text.UTF8Encoding]::new($false, $true)
                $headers = $utf8.GetString($headerBuffer.ToArray())
                [System.IO.File]::WriteAllText($HeadersPath, $headers)
                $contentLength = 0
                $lengthMatch = [regex]::Match($headers, '(?im)^Content-Length:\s*(\d+)\s*$')
                if ($lengthMatch.Success) { $contentLength = [int]$lengthMatch.Groups[1].Value }
                $requestBody = [System.IO.MemoryStream]::new()
                try {
                    $remaining = $contentLength
                    $buffer = [byte[]]::new(8192)
                    while ($remaining -gt 0) {
                        $read = $stream.Read($buffer, 0, [Math]::Min($buffer.Length, $remaining))
                        if ($read -le 0) { break }
                        $requestBody.Write($buffer, 0, $read)
                        $remaining -= $read
                    }
                    [System.IO.File]::WriteAllText($BodyPath, $utf8.GetString($requestBody.ToArray()))
                }
                finally { $requestBody.Dispose() }
            }
            finally { $headerBuffer.Dispose() }
            if ($DelayMilliseconds -gt 0) { Start-Sleep -Milliseconds $DelayMilliseconds }
            $statusText = switch ($StatusCode) {
                200 { 'OK' }
                302 { 'Found' }
                500 { 'Internal Server Error' }
                default { 'Synthetic' }
            }
            $responseBytes = $utf8.GetBytes($ResponseBody)
            $responseHeader = "HTTP/1.1 $StatusCode $statusText`r`nContent-Type: application/json`r`nContent-Length: $($responseBytes.Length)`r`nConnection: close`r`n"
            if (-not [string]::IsNullOrWhiteSpace($Location)) { $responseHeader += "Location: $Location`r`n" }
            $responseHeader += "`r`n"
            $responseHeaderBytes = $utf8.GetBytes($responseHeader)
            $stream.Write($responseHeaderBytes, 0, $responseHeaderBytes.Length)
            $stream.Flush()
            if ($BodyDelayMilliseconds -gt 0) { Start-Sleep -Milliseconds $BodyDelayMilliseconds }
            $stream.Write($responseBytes, 0, $responseBytes.Length)
            try {
                $stream.Flush()
            }
            catch { throw }
        }
        catch {
            if (-not [string]::IsNullOrWhiteSpace($ErrorPath)) {
                try { [System.IO.File]::WriteAllText($ErrorPath, $_.Exception.Message) } catch { }
            }
        }
        finally {
            if ($null -ne $stream) { $stream.Dispose() }
            if ($null -ne $client) { $client.Dispose() }
            $listener.Stop()
        }
    }
    $job = Start-ThreadJob -ScriptBlock $server -ArgumentList @($Port, $ReadyPath, $HeadersPath, $BodyPath, $StatusCode, $ResponseBody, $Location, $DelayMilliseconds, $BodyDelayMilliseconds, $AcceptTimeoutMilliseconds, $ErrorPath)
    $deadline = [DateTime]::UtcNow.AddSeconds(5)
    while (-not (Test-Path -LiteralPath $ReadyPath -PathType Leaf)) {
        if ([DateTime]::UtcNow -gt $deadline) {
            $state = [string]$job.State
            try { Stop-Job -Job $job -ErrorAction SilentlyContinue } catch { }
            try { Remove-Job -Job $job -Force -ErrorAction SilentlyContinue } catch { }
            throw "Loopback server did not become ready (state=$state)."
        }
        Start-Sleep -Milliseconds 20
    }
    return $job
}

function Stop-CouncilLoopbackServer {
    param($Job)
    if ($null -eq $Job) { return }
    try {
        if ($Job.State -eq 'Running') { Stop-Job -Job $Job -ErrorAction SilentlyContinue }
    }
    finally {
        Receive-Job -Job $Job -ErrorAction SilentlyContinue | Out-Null
        Remove-Job -Job $Job -Force -ErrorAction SilentlyContinue
    }
}

function New-CouncilLoopbackBinding([string]$Protocol, [int]$Port) {
    [pscustomobject][ordered]@{
        provider = 'loopback'; model = if ($Protocol -ceq 'openai_responses') { 'gpt-6-astra' } else { 'compatible-model' }; effort = 'medium'; protocol = $Protocol
        endpoint = [pscustomobject][ordered]@{ scheme = 'http'; host = '127.0.0.1'; port = $Port; base_path = '/v1' }
    }
}

function New-Binding([string]$Protocol = 'openai_compatible') {
    [pscustomobject][ordered]@{
        provider = 'deepseek'; model = 'deepseek-flash'; effort = 'medium'; protocol = $Protocol
        endpoint = [pscustomobject][ordered]@{ scheme = 'https'; host = 'api.deepseek.com'; port = 443; base_path = '/' }
    }
}
$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ('council-transport-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tempRoot -Force | Out-Null
try {
    # Credential priority: local overlay wins over env.
    [System.Environment]::SetEnvironmentVariable('COUNCIL_TEST_TOKEN', 'env-token')
    $fromEnv = Resolve-BSLFlowCouncilCredential -ProviderName 'deepseek' -TokenEnv 'COUNCIL_TEST_TOKEN' -LocalToken ''
    Assert-True ($fromEnv.token -eq 'env-token' -and $fromEnv.credential_source -eq 'env') 'env credential used without local token'
    $fromLocal = Resolve-BSLFlowCouncilCredential -ProviderName 'deepseek' -TokenEnv 'COUNCIL_TEST_TOKEN' -LocalToken 'local-token'
    Assert-True ($fromLocal.token -eq 'local-token' -and $fromLocal.credential_source -eq 'local') 'local token wins over env'
    $missing = Resolve-BSLFlowCouncilCredential -ProviderName 'deepseek' -TokenEnv 'COUNCIL_TEST_MISSING_XYZ' -LocalToken ''
    Assert-True ($missing.credential_source -eq 'missing') 'missing credential is explicit'
    [System.Environment]::SetEnvironmentVariable('COUNCIL_TEST_TOKEN', $null)

    # Budget admission.
    $budget = [pscustomobject][ordered]@{ currency = 'USD'; limit = 1.0; reservation = 0.0 }
    $ok = Test-BSLFlowCouncilBudgetAdmission -Budget $budget -Dispatches @(@{ cost_estimate_usd = 0.2 }, @{ cost_estimate_usd = 0.3 })
    Assert-True ([bool]$ok.admitted) 'budget admits fitting cycle'
    Assert-Throws { Test-BSLFlowCouncilBudgetAdmission -Budget $budget -Dispatches @(@{ cost_estimate_usd = 0.9 }, @{ cost_estimate_usd = 0.5 }) } 'admission refused' 'budget blocks over-limit cycle'
    Assert-Throws { Test-BSLFlowCouncilBudgetAdmission -Budget $budget -Dispatches @(@{ }) } 'unknown dispatch cost' 'unknown cost is not zero'

    # Redaction.
    $dirty = 'Authorization: Bearer abc123 and "token": "abc123"'
    $clean = Protect-BSLFlowCouncilDiagnostic $dirty
    Assert-True ($clean -notmatch 'abc123') 'diagnostic redacts secrets'

    # 200 + chat envelope completes with the extracted role payload and
    # provider-observed model identity.
    $chatBody = '{"id":"chatcmpl-1","model":"deepseek-chat-resolved","usage":{"prompt_tokens":10,"completion_tokens":5},"choices":[{"message":{"role":"assistant","content":"{\"role\":\"intent_critic\",\"verdict\":\"PASS\"}"},"finish_reason":"stop"}]}'
    $okSend = { param($u, $b, $t, $to) [pscustomobject][ordered]@{ status = 200; body = $chatBody } }.GetNewClosure()
    $chatResult = Invoke-BSLFlowCouncilApi -Binding (New-Binding) -PromptText 'hi' -Credential 't' -AttemptDir (Join-Path $tempRoot 'ok') -HttpSend $okSend
    $payload = $chatResult.payload
    Assert-True ($payload.verdict -eq 'PASS') 'chat envelope role payload extracted'
    Assert-True ([string]$chatResult.observed_model -eq 'deepseek-chat-resolved') 'observed model comes from the provider envelope'
    Assert-True ([string]$chatResult.usage.prompt_tokens -eq '10') 'provider usage is extracted separately'

    # Responses envelope with nested output parts completes as well.
    $respBody = '{"id":"resp-1","model":"gpt-5.6-sol","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"role\":\"intent_critic\",\"verdict\":\"PASS\",\"extra\":{\"nested\":true}}"}]}]}'
    $respBinding = New-Binding 'openai_responses'
    $respSend = { param($u, $b, $t, $to) [pscustomobject][ordered]@{ status = 200; body = $respBody } }.GetNewClosure()
    $respResult = Invoke-BSLFlowCouncilApi -Binding $respBinding -PromptText 'hi' -Credential 't' -AttemptDir (Join-Path $tempRoot 'resp') -HttpSend $respSend
    Assert-True ($respResult.payload.verdict -eq 'PASS') 'responses envelope role payload extracted'
    Assert-True ([string]$respResult.observed_model -eq 'gpt-5.6-sol') 'responses observed model comes from the provider envelope'

    # Fenced role JSON is accepted only as model text inside a terminal provider
    # envelope; the provider observation remains available to the controller.
    $fenced = '{"id":"chatcmpl-fenced","model":"deepseek-chat-resolved","choices":[{"message":{"role":"assistant","content":"```json {\"role\": \"intent_critic\", \"verdict\": \"REVISE\", \"detail\": {\"deep\": [1, 2]}} ```"},"finish_reason":"stop"}]}'
    $fenceSend = { param($u, $b, $t, $to) [pscustomobject][ordered]@{ status = 200; body = $fenced } }.GetNewClosure()
    $fenceResult = Invoke-BSLFlowCouncilApi -Binding (New-Binding) -PromptText 'hi' -Credential 't' -AttemptDir (Join-Path $tempRoot 'fence') -HttpSend $fenceSend
    Assert-True ($fenceResult.payload.verdict -eq 'REVISE') 'fenced nested role payload extracted'
    Assert-True ([string]$fenceResult.observed_model -eq 'deepseek-chat-resolved' -and $null -eq $fenceResult.usage) 'fenced payload retains provider observation'

    $bareRoleSend = { param($u, $b, $t, $to) [pscustomobject][ordered]@{ status = 200; body = '{"role":"intent_critic","verdict":"PASS"}' } }
    Assert-Throws { Invoke-BSLFlowCouncilApi -Binding (New-Binding) -PromptText 'hi' -Credential 't' -AttemptDir (Join-Path $tempRoot 'bare-role') -HttpSend $bareRoleSend } 'INVALID_RESPONSE' 'bare role payload without terminal envelope rejected'

    # Envelope without model text is an invalid response, not a silent pass.
    $emptySend = { param($u, $b, $t, $to) [pscustomobject][ordered]@{ status = 200; body = '{"id":"x","choices":[]}' } }
    Assert-Throws { Invoke-BSLFlowCouncilApi -Binding (New-Binding) -PromptText 'hi' -Credential 't' -AttemptDir (Join-Path $tempRoot 'empty') -HttpSend $emptySend } 'INVALID_RESPONSE' 'envelope without content rejected'
    $lengthSend = { param($u, $b, $t, $to) [pscustomobject][ordered]@{ status = 200; body = '{"id":"x","model":"deepseek-chat","choices":[{"message":{"role":"assistant","content":"{\"role\":\"intent_critic\",\"verdict\":\"PASS\"}"},"finish_reason":"length"}]}' } }
    Assert-Throws { Invoke-BSLFlowCouncilApi -Binding (New-Binding) -PromptText 'hi' -Credential 't' -AttemptDir (Join-Path $tempRoot 'length') -HttpSend $lengthSend } 'INVALID_RESPONSE' 'chat non-terminal length finish is rejected'
    $incompleteSend = { param($u, $b, $t, $to) [pscustomobject][ordered]@{ status = 200; body = '{"id":"x","model":"gpt-6-astra","status":"incomplete","output":[{"type":"message","content":[{"type":"output_text","text":"{\"role\":\"intent_critic\",\"verdict\":\"PASS\"}"}]}]}' } }
    Assert-Throws { Invoke-BSLFlowCouncilApi -Binding (New-Binding 'openai_responses') -PromptText 'hi' -Credential 't' -AttemptDir (Join-Path $tempRoot 'incomplete') -HttpSend $incompleteSend } 'INVALID_RESPONSE' 'Responses non-terminal status is rejected'

    # Configured effort reaches the wire on both protocols.
    $chatReq = Get-BSLFlowCouncilApiRequest -Binding (New-Binding) -PromptText 'hi'
    Assert-True ([string]$chatReq.body.reasoning_effort -eq 'medium') 'chat request carries reasoning effort'
    $respReq = Get-BSLFlowCouncilApiRequest -Binding $respBinding -PromptText 'hi'
    Assert-True ([string]$respReq.body.reasoning.effort -eq 'medium') 'responses request carries reasoning effort'
    $intBinding = New-Binding
    $intBinding.effort = '100'
    $intReq = Get-BSLFlowCouncilApiRequest -Binding $intBinding -PromptText 'hi'
    Assert-True ([int]$intReq.body.max_tokens -eq 100) 'integer effort becomes an explicit token cap'

    # Ambiguous and malformed bodies are invalid responses.
    $twoSend = { param($u, $b, $t, $to) [pscustomobject][ordered]@{ status = 200; body = '{"a":1} {"b":2}' } }
    Assert-Throws { Invoke-BSLFlowCouncilApi -Binding (New-Binding) -PromptText 'hi' -Credential 't' -AttemptDir (Join-Path $tempRoot 'two') -HttpSend $twoSend } 'exactly one JSON' 'ambiguous response rejected'
    $badSend = { param($u, $b, $t, $to) [pscustomobject][ordered]@{ status = 200; body = '{broken' } }
    Assert-Throws { Invoke-BSLFlowCouncilApi -Binding (New-Binding) -PromptText 'hi' -Credential 't' -AttemptDir (Join-Path $tempRoot 'bad') -HttpSend $badSend } 'INVALID_RESPONSE' 'malformed response rejected'

    # Provider statuses map to retry-safe states.
    $denied = { param($u, $b, $t, $to) [pscustomobject][ordered]@{ status = 401; body = 'no' } }
    Assert-Throws { Invoke-BSLFlowCouncilApi -Binding (New-Binding) -PromptText 'hi' -Credential 't' -AttemptDir (Join-Path $tempRoot 'denied') -HttpSend $denied } 'FAILED_BEFORE_ACCEPTANCE' '4xx is failed before acceptance'
    $server = { param($u, $b, $t, $to) [pscustomobject][ordered]@{ status = 500; body = 'err' } }
    Assert-Throws { Invoke-BSLFlowCouncilApi -Binding (New-Binding) -PromptText 'hi' -Credential 't' -AttemptDir (Join-Path $tempRoot 'srv') -HttpSend $server } 'UNKNOWN_AFTER_DISPATCH' '5xx is unknown after dispatch'
    $timeoutSend = { param($u, $b, $t, $to) throw 'The operation was canceled' }
    Assert-Throws { Invoke-BSLFlowCouncilApi -Binding (New-Binding) -PromptText 'hi' -Credential 't' -AttemptDir (Join-Path $tempRoot 'to') -HttpSend $timeoutSend } 'UNKNOWN_AFTER_DISPATCH' 'timeout is unknown after dispatch'
    $redirectSend = { param($u, $b, $t, $to) throw 'BF_REDIRECT: council redirect refused: https://evil.example/x' }
    Assert-Throws { Invoke-BSLFlowCouncilApi -Binding (New-Binding) -PromptText 'hi' -Credential 't' -AttemptDir (Join-Path $tempRoot 'redir') -HttpSend $redirectSend } 'FAILED_BEFORE_ACCEPTANCE' 'cross-host redirect refused'
    $cancelSource = [System.Threading.CancellationTokenSource]::new()
    $cancelSource.Cancel()
    Assert-Throws { Invoke-BSLFlowCouncilApi -Binding (New-Binding) -PromptText 'hi' -Credential 't' -AttemptDir (Join-Path $tempRoot 'cancel') -CancellationToken $cancelSource.Token -HttpSend { throw 'sender must not run after pre-dispatch cancellation' } } 'NOT_DISPATCHED' 'pre-dispatch cancellation prevents the sender call'
    $cancelSource.Dispose()

    # Real loopback HTTP transport. The server captures the request before
    # replying, so these checks exercise the built-in HttpClient sender rather
    # than only the injectable mock path above.
    $loopbackCredential = 'loopback-secret'
    $loopbackRoot = Join-Path $tempRoot 'loopback'
    New-Item -ItemType Directory -Path $loopbackRoot -Force | Out-Null

    $responsesPort = Get-CouncilFreeLoopbackPort
    $responsesReady = Join-Path $loopbackRoot 'responses-ready'
    $responsesHeaders = Join-Path $loopbackRoot 'responses-headers.txt'
    $responsesRequest = Join-Path $loopbackRoot 'responses-request.json'
    $responsesBody = '{"id":"resp-loopback","model":"gpt-6-astra","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"role\":\"intent_critic\",\"verdict\":\"PASS\"}"}]}]}'
    $responsesJob = $null
    try {
        $responsesJob = Start-CouncilLoopbackServer -Port $responsesPort -ReadyPath $responsesReady -HeadersPath $responsesHeaders -BodyPath $responsesRequest -StatusCode 200 -ResponseBody $responsesBody
        $responsesAttempt = Join-Path $loopbackRoot 'responses-attempt'
        $responsesResult = Invoke-BSLFlowCouncilApi -Binding (New-CouncilLoopbackBinding 'openai_responses' $responsesPort) -PromptText 'loopback responses' -Credential $loopbackCredential -AttemptDir $responsesAttempt -TimeoutSeconds 5 -MaxInputBytes 65536 -MaxOutputBytes 16384
        $responsesRequestObject = Get-Content -Raw -LiteralPath $responsesRequest | ConvertFrom-Json
        $responsesHeadersText = Get-Content -Raw -LiteralPath $responsesHeaders
        Assert-True ($responsesResult.payload.verdict -eq 'PASS') 'loopback Responses envelope completes'
        Assert-True ($responsesResult.observed_model -eq 'gpt-6-astra') 'loopback Responses model observation is returned'
        Assert-True ($responsesHeadersText -match '(?im)^POST /v1/responses HTTP/1\.1\r?$') 'loopback Responses path reaches the wire'
        Assert-True ($responsesHeadersText -match ('(?im)^Authorization: Bearer ' + [regex]::Escape($loopbackCredential) + '\r?$')) 'loopback Responses sends the credential only to the bound host'
        Assert-True ((Get-BSLFlowEnvelopeField $responsesRequestObject 'response_format') -eq $null -and $responsesRequestObject.text.format.type -eq 'json_object') 'Responses uses text.format for structured output'
        Assert-True ((Get-Content -Raw -LiteralPath (Join-Path $responsesAttempt 'request-meta.json')) -notmatch [regex]::Escape($loopbackCredential)) 'request metadata excludes the credential'
        Assert-True (Test-Path -LiteralPath (Join-Path $responsesAttempt 'raw-response.txt') -PathType Leaf) 'loopback Responses raw response is retained only in the attempt directory'
    }
    finally { Stop-CouncilLoopbackServer $responsesJob }

    $compatiblePort = Get-CouncilFreeLoopbackPort
    $compatibleReady = Join-Path $loopbackRoot 'compatible-ready'
    $compatibleHeaders = Join-Path $loopbackRoot 'compatible-headers.txt'
    $compatibleRequest = Join-Path $loopbackRoot 'compatible-request.json'
    $compatibleBody = '{"id":"chat-loopback","model":"compatible-resolved","usage":{"prompt_tokens":3,"completion_tokens":2},"choices":[{"message":{"role":"assistant","content":"{\"role\":\"architecture_critic\",\"verdict\":\"PASS\"}"},"finish_reason":"stop"}]}'
    $compatibleJob = $null
    try {
        $compatibleJob = Start-CouncilLoopbackServer -Port $compatiblePort -ReadyPath $compatibleReady -HeadersPath $compatibleHeaders -BodyPath $compatibleRequest -StatusCode 200 -ResponseBody $compatibleBody
        $compatibleAttempt = Join-Path $loopbackRoot 'compatible-attempt'
        $compatibleResult = Invoke-BSLFlowCouncilApi -Binding (New-CouncilLoopbackBinding 'openai_compatible' $compatiblePort) -PromptText 'loopback compatible' -Credential $loopbackCredential -AttemptDir $compatibleAttempt -TimeoutSeconds 5 -MaxInputBytes 65536 -MaxOutputBytes 16384
        $compatibleRequestObject = Get-Content -Raw -LiteralPath $compatibleRequest | ConvertFrom-Json
        $compatibleHeadersText = Get-Content -Raw -LiteralPath $compatibleHeaders
        Assert-True ($compatibleResult.payload.verdict -eq 'PASS') 'loopback compatible envelope completes'
        Assert-True ($compatibleResult.observed_model -eq 'compatible-resolved') 'loopback compatible model observation is returned'
        Assert-True ($compatibleHeadersText -match '(?im)^POST /v1/chat/completions HTTP/1\.1\r?$') 'loopback compatible path reaches the wire'
        Assert-True ($compatibleRequestObject.response_format.type -eq 'json_object' -and $compatibleRequestObject.messages[0].content -eq 'loopback compatible') 'compatible request keeps response_format and message contract'
        Assert-True ((Get-Content -Raw -LiteralPath (Join-Path $compatibleAttempt 'request-meta.json')) -notmatch [regex]::Escape($loopbackCredential)) 'compatible request metadata excludes the credential'
    }
    finally { Stop-CouncilLoopbackServer $compatibleJob }

    # Input is rejected before a socket is opened; output is drained through a
    # bounded stream and is never persisted as an unbounded raw response.
    $inputPort = Get-CouncilFreeLoopbackPort
    $inputReady = Join-Path $loopbackRoot 'input-ready'
    $inputHeaders = Join-Path $loopbackRoot 'input-headers.txt'
    $inputRequest = Join-Path $loopbackRoot 'input-request.json'
    $inputJob = $null
    $inputAttempt = Join-Path $loopbackRoot 'input-attempt'
    try {
        $inputJob = Start-CouncilLoopbackServer -Port $inputPort -ReadyPath $inputReady -HeadersPath $inputHeaders -BodyPath $inputRequest -StatusCode 200 -ResponseBody '{}'
        Assert-Throws { Invoke-BSLFlowCouncilApi -Binding (New-CouncilLoopbackBinding 'openai_compatible' $inputPort) -PromptText ('p' * 512) -Credential $loopbackCredential -AttemptDir $inputAttempt -MaxInputBytes 64 } 'NOT_DISPATCHED' 'loopback input bound blocks before dispatch'
        Assert-True (-not (Test-Path -LiteralPath $inputHeaders -PathType Leaf)) 'input overflow opens no HTTP connection'
        $inputDiagnostic = Get-Content -Raw -LiteralPath (Join-Path $inputAttempt 'diagnostic.json') | ConvertFrom-Json
        Assert-True ($inputDiagnostic.code -eq 'BF_NOT_DISPATCHED' -and -not [bool]$inputDiagnostic.dispatch_started) 'input bound persists a pre-dispatch diagnostic'
        Assert-True ((Get-Content -Raw -LiteralPath (Join-Path $inputAttempt 'diagnostic.json')) -notmatch [regex]::Escape($loopbackCredential)) 'pre-dispatch diagnostic excludes the credential'
    }
    finally { Stop-CouncilLoopbackServer $inputJob }

    $overflowPort = Get-CouncilFreeLoopbackPort
    $overflowReady = Join-Path $loopbackRoot 'overflow-ready'
    $overflowHeaders = Join-Path $loopbackRoot 'overflow-headers.txt'
    $overflowRequest = Join-Path $loopbackRoot 'overflow-request.json'
    $overflowAttempt = Join-Path $loopbackRoot 'overflow-attempt'
    $overflowJob = $null
    try {
        $overflowJob = Start-CouncilLoopbackServer -Port $overflowPort -ReadyPath $overflowReady -HeadersPath $overflowHeaders -BodyPath $overflowRequest -StatusCode 200 -ResponseBody ('x' * 4096)
        Assert-Throws { Invoke-BSLFlowCouncilApi -Binding (New-CouncilLoopbackBinding 'openai_responses' $overflowPort) -PromptText 'overflow' -Credential $loopbackCredential -AttemptDir $overflowAttempt -TimeoutSeconds 5 -MaxOutputBytes 128 } 'INVALID_RESPONSE' 'loopback output bound rejects an oversized response'
        $overflowDiagnosticPath = Join-Path $overflowAttempt 'diagnostic.json'
        $overflowDiagnostic = Get-Content -Raw -LiteralPath $overflowDiagnosticPath | ConvertFrom-Json
        Assert-True ($overflowDiagnostic.code -eq 'BF_INVALID_RESPONSE' -and [bool]$overflowDiagnostic.dispatch_started) 'output overflow persists a terminal diagnostic'
        Assert-True ((Get-Content -Raw -LiteralPath $overflowDiagnosticPath) -notmatch [regex]::Escape($loopbackCredential)) 'output overflow diagnostic excludes the credential'
        Assert-True (-not (Test-Path -LiteralPath (Join-Path $overflowAttempt 'raw-response.txt') -PathType Leaf)) 'oversized response is not persisted as raw output'
    }
    finally { Stop-CouncilLoopbackServer $overflowJob }

    $redirectTargetPort = Get-CouncilFreeLoopbackPort
    $redirectSourcePort = Get-CouncilFreeLoopbackPort
    $redirectTargetReady = Join-Path $loopbackRoot 'redirect-target-ready'
    $redirectTargetHeaders = Join-Path $loopbackRoot 'redirect-target-headers.txt'
    $redirectTargetRequest = Join-Path $loopbackRoot 'redirect-target-request.json'
    $redirectSourceReady = Join-Path $loopbackRoot 'redirect-source-ready'
    $redirectSourceHeaders = Join-Path $loopbackRoot 'redirect-source-headers.txt'
    $redirectSourceRequest = Join-Path $loopbackRoot 'redirect-source-request.json'
    $redirectAttempt = Join-Path $loopbackRoot 'redirect-attempt'
    $redirectTargetJob = $null
    $redirectSourceJob = $null
    try {
        $redirectTargetJob = Start-CouncilLoopbackServer -Port $redirectTargetPort -ReadyPath $redirectTargetReady -HeadersPath $redirectTargetHeaders -BodyPath $redirectTargetRequest -StatusCode 200 -ResponseBody '{}'
        $redirectLocation = "http://127.0.0.1:$redirectTargetPort/v1/responses"
        $redirectSourceJob = Start-CouncilLoopbackServer -Port $redirectSourcePort -ReadyPath $redirectSourceReady -HeadersPath $redirectSourceHeaders -BodyPath $redirectSourceRequest -StatusCode 302 -ResponseBody '{}' -Location $redirectLocation
        Assert-Throws { Invoke-BSLFlowCouncilApi -Binding (New-CouncilLoopbackBinding 'openai_responses' $redirectSourcePort) -PromptText 'redirect' -Credential $loopbackCredential -AttemptDir $redirectAttempt -TimeoutSeconds 5 } 'FAILED_BEFORE_ACCEPTANCE' 'loopback redirect is refused'
        Start-Sleep -Milliseconds 150
        Assert-True (-not (Test-Path -LiteralPath $redirectTargetHeaders -PathType Leaf)) 'redirect target receives no credential-bearing request'
        $redirectDiagnosticPath = Join-Path $redirectAttempt 'diagnostic.json'
        Assert-True ((Get-Content -Raw -LiteralPath $redirectDiagnosticPath) -notmatch [regex]::Escape($loopbackCredential)) 'redirect diagnostic excludes the credential'
    }
    finally {
        Stop-CouncilLoopbackServer $redirectSourceJob
        Stop-CouncilLoopbackServer $redirectTargetJob
    }

    # A response timeout is terminal unknown-after-dispatch and leaves no
    # retry loop in the built-in sender.
    $timeoutPort = Get-CouncilFreeLoopbackPort
    $timeoutReady = Join-Path $loopbackRoot 'timeout-ready'
    $timeoutHeaders = Join-Path $loopbackRoot 'timeout-headers.txt'
    $timeoutRequest = Join-Path $loopbackRoot 'timeout-request.json'
    $timeoutAttempt = Join-Path $loopbackRoot 'timeout-attempt'
    $timeoutJob = $null
    try {
        $timeoutJob = Start-CouncilLoopbackServer -Port $timeoutPort -ReadyPath $timeoutReady -HeadersPath $timeoutHeaders -BodyPath $timeoutRequest -StatusCode 200 -ResponseBody '{}' -DelayMilliseconds 2000
        Assert-Throws { Invoke-BSLFlowCouncilApi -Binding (New-CouncilLoopbackBinding 'openai_compatible' $timeoutPort) -PromptText 'timeout' -Credential $loopbackCredential -AttemptDir $timeoutAttempt -TimeoutSeconds 1 } 'UNKNOWN_AFTER_DISPATCH' 'loopback timeout is unknown after dispatch'
        Assert-True (Test-Path -LiteralPath $timeoutHeaders -PathType Leaf) 'loopback timeout reached the provider before termination'
        $timeoutDiagnostic = Get-Content -Raw -LiteralPath (Join-Path $timeoutAttempt 'diagnostic.json') | ConvertFrom-Json
        Assert-True ($timeoutDiagnostic.code -eq 'BF_UNKNOWN_AFTER_DISPATCH' -and [bool]$timeoutDiagnostic.dispatch_started) 'timeout diagnostic records unknown dispatch state'
    }
    finally { Stop-CouncilLoopbackServer $timeoutJob }

    # ResponseHeadersRead completes at headers, so the body itself must also be
    # bounded by the same operation timeout. This server sends headers first and
    # stalls before the body; the call must finish once, without a retry.
    $bodyStallPort = Get-CouncilFreeLoopbackPort
    $bodyStallReady = Join-Path $loopbackRoot 'body-stall-ready'
    $bodyStallHeaders = Join-Path $loopbackRoot 'body-stall-headers.txt'
    $bodyStallRequest = Join-Path $loopbackRoot 'body-stall-request.json'
    $bodyStallAttempt = Join-Path $loopbackRoot 'body-stall-attempt'
    $bodyStallJob = $null
    try {
        $bodyStallJob = Start-CouncilLoopbackServer -Port $bodyStallPort -ReadyPath $bodyStallReady -HeadersPath $bodyStallHeaders -BodyPath $bodyStallRequest -StatusCode 200 -ResponseBody '{}' -BodyDelayMilliseconds 3000
        $bodyStallWatch = [System.Diagnostics.Stopwatch]::StartNew()
        Assert-Throws { Invoke-BSLFlowCouncilApi -Binding (New-CouncilLoopbackBinding 'openai_compatible' $bodyStallPort) -PromptText 'body stall' -Credential $loopbackCredential -AttemptDir $bodyStallAttempt -TimeoutSeconds 1 } 'UNKNOWN_AFTER_DISPATCH' 'loopback stalled body is unknown after dispatch'
        $bodyStallWatch.Stop()
        Assert-True ($bodyStallWatch.Elapsed.TotalSeconds -lt 2.5) 'loopback stalled body respects the overall timeout without retry'
        Assert-True (Test-Path -LiteralPath $bodyStallHeaders -PathType Leaf) 'loopback stalled body sent headers before timeout'
        $bodyStallDiagnostic = Get-Content -Raw -LiteralPath (Join-Path $bodyStallAttempt 'diagnostic.json') | ConvertFrom-Json
        Assert-True ($bodyStallDiagnostic.code -eq 'BF_UNKNOWN_AFTER_DISPATCH' -and [bool]$bodyStallDiagnostic.dispatch_started) 'stalled body diagnostic records unknown dispatch state'
    }
    finally { Stop-CouncilLoopbackServer $bodyStallJob }
}
finally { Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue }

"ALL_STAGE3_TRANSPORT_PASSED=$passed"
