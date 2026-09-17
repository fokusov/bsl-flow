#Requires -Version 7.0
Set-StrictMode -Version Latest

# Stage 3: direct API transports, credential resolution, budget admission and redaction.
# Raw bodies stay in the ignored attempt directory only; sanitized diagnostics never carry secrets.

function Resolve-BSLFlowCouncilCredential {
    param(
        [Parameter(Mandatory)][string]$ProviderName,
        [string]$TokenEnv,
        [string]$LocalToken
    )
    if (-not [string]::IsNullOrWhiteSpace($LocalToken)) {
        return [pscustomobject][ordered]@{ token = [string]$LocalToken; credential_source = 'local' }
    }
    if (-not [string]::IsNullOrWhiteSpace($TokenEnv)) {
        $value = [System.Environment]::GetEnvironmentVariable($TokenEnv)
        if (-not [string]::IsNullOrWhiteSpace($value)) {
            return [pscustomobject][ordered]@{ token = [string]$value; credential_source = 'env' }
        }
    }
    return [pscustomobject][ordered]@{ token = $null; credential_source = 'missing' }
}

function Protect-BSLFlowCouncilDiagnostic {
    param(
        [Parameter(Mandatory)][string]$Text,
        [string[]]$Secrets = @()
    )
    $clean = $Text
    foreach ($secret in @($Secrets)) {
        if (-not [string]::IsNullOrWhiteSpace($secret)) {
            $clean = $clean.Replace([string]$secret, '<redacted>')
        }
    }
    $clean = [regex]::Replace($clean, '(?is)\bBearer\s+[^\s,;]+', 'Bearer <redacted>')
    $clean = [regex]::Replace($clean, '(?i)"token"\s*:\s*"[^"]*"', '"token": "<redacted>"')
    $clean = [regex]::Replace($clean, '(?is)Authorization\s*:\s*[^\r\n]+', 'Authorization: <redacted>')
    return $clean
}

function Write-BSLFlowCouncilTransportDiagnostic {
    param(
        [Parameter(Mandatory)][string]$AttemptDir,
        [Parameter(Mandatory)][string]$Code,
        [Parameter(Mandatory)][string]$Message,
        [Parameter(Mandatory)][AllowNull()][object]$Endpoint,
        [int]$Status = 0,
        [long]$InputBytes = 0,
        [long]$OutputBytes = 0,
        [int]$MaxOutputBytes = 0,
        [bool]$DispatchStarted = $false,
        [string[]]$Secrets = @()
    )
    # Diagnostics are deliberately a projection: no URL, headers, token, body,
    # or arbitrary provider metadata is copied into this common attempt file.
    try {
        $endpointProjection = [ordered]@{}
        foreach ($name in @('scheme', 'host', 'port', 'base_path')) {
            $value = $null
            try { $value = $Endpoint.$name } catch { $value = $null }
            if ($null -ne $value) { $endpointProjection[$name] = [string]$value }
        }
        $diagnostic = [ordered]@{
            schema_version = 1
            code = $Code
            message = (Protect-BSLFlowCouncilDiagnostic -Text $Message -Secrets $Secrets)
            status = $Status
            dispatch_started = $DispatchStarted
            input_bytes = $InputBytes
            output_bytes = $OutputBytes
            max_output_bytes = $MaxOutputBytes
            endpoint = $endpointProjection
            timestamp_utc = [DateTime]::UtcNow.ToString('o', [Globalization.CultureInfo]::InvariantCulture)
        }
        $json = $diagnostic | ConvertTo-Json -Depth 10
        $json = Protect-BSLFlowCouncilDiagnostic -Text $json -Secrets $Secrets
        [IO.File]::WriteAllText((Join-Path $AttemptDir 'diagnostic.json'), $json)
    }
    catch {
        # A diagnostic must never replace the original transport outcome.
    }
}

function Read-BSLFlowCouncilHttpResponseBody {
    param(
        [Parameter(Mandatory)]$Response,
        [Parameter(Mandatory)][int]$MaxBytes,
        [System.Threading.CancellationToken]$CancellationToken = [System.Threading.CancellationToken]::None
    )
    $contentLength = $null
    try { $contentLength = $Response.Content.Headers.ContentLength } catch { $contentLength = $null }
    if ($null -ne $contentLength -and [long]$contentLength -gt [long]$MaxBytes) {
        throw 'BF_INVALID_RESPONSE: council response exceeds the output limit.'
    }

    $stream = $null
    $buffered = [IO.MemoryStream]::new()
    try {
        $stream = $Response.Content.ReadAsStreamAsync().GetAwaiter().GetResult()
        $bufferSize = [Math]::Min(8192, $MaxBytes + 1)
        $buffer = [byte[]]::new([int]$bufferSize)
        while ($true) {
            $remaining = ([long]$MaxBytes + 1L) - $buffered.Length
            if ($remaining -le 0) {
                throw 'BF_INVALID_RESPONSE: council response exceeds the output limit.'
            }
            $toRead = [int][Math]::Min([long]$buffer.Length, $remaining)
            $read = $stream.ReadAsync($buffer, 0, $toRead, $CancellationToken).GetAwaiter().GetResult()
            if ($read -le 0) { break }
            $buffered.Write($buffer, 0, $read)
            if ($buffered.Length -gt [long]$MaxBytes) {
                throw 'BF_INVALID_RESPONSE: council response exceeds the output limit.'
            }
        }
        $utf8 = [Text.UTF8Encoding]::new($false, $true)
        try { return $utf8.GetString($buffered.ToArray()) }
        catch { throw 'BF_INVALID_RESPONSE: council response is not valid UTF-8.' }
    }
    finally {
        if ($null -ne $stream) { $stream.Dispose() }
        $buffered.Dispose()
    }
}

function Test-BSLFlowCouncilBudgetAdmission {
    param(
        [Parameter(Mandatory)]$Budget,
        [Parameter(Mandatory)]$Dispatches
    )
    if ($Budget.currency -cne 'USD') { throw 'BF_INVALID: only a USD budget currency is supported.' }
    $list = @($Dispatches)
    $total = 0.0
    foreach ($dispatch in $list) {
        $estimate = $null
        try { $estimate = $dispatch.cost_estimate_usd } catch { $estimate = $null }
        if ($null -eq $estimate) { throw 'BF_BLOCKED: unknown dispatch cost is not zero; an explicit estimate is required.' }
        $number = 0.0
        if ($estimate -is [string]) {
            if (-not [double]::TryParse($estimate, [System.Globalization.NumberStyles]::Float, [System.Globalization.CultureInfo]::InvariantCulture, [ref]$number)) {
                throw 'BF_BLOCKED: dispatch cost estimate must be a non-negative finite number.'
            }
        }
        else { $number = [double]$estimate }
        if ([double]::IsNaN($number) -or [double]::IsInfinity($number) -or $number -lt 0) {
            throw 'BF_BLOCKED: dispatch cost estimate must be a non-negative finite number.'
        }
        $total += $number
    }
    $limit = $null
    try { $limit = $Budget.limit } catch { $limit = $null }
    if ($null -ne $limit) {
        $limitNumber = [double]$limit
        $reservation = 0.0
        try { if ($null -ne $Budget.reservation) { $reservation = [double]$Budget.reservation } } catch { $reservation = 0.0 }
        if ($total + $reservation -gt $limitNumber) { throw 'BF_BLOCKED: council budget admission refused for the full dispatch cycle.' }
    }
    return [ordered]@{ admitted = $true; estimated_total_usd = $total }
}

function Get-BSLFlowCouncilApiRequest {
    param(
        [Parameter(Mandatory)]$Binding,
        [Parameter(Mandatory)][string]$PromptText
    )
    if ($Binding.protocol -cnotin @('openai_responses', 'openai_compatible')) { throw 'BF_INVALID: unsupported council protocol.' }
    # Effort mapping is explicit and reviewable, not provider folklore:
    # string efforts ride the protocol reasoning field; integer efforts are an
    # explicit token budget cap (max tokens), never silently dropped.
    $effort = [string]$Binding.effort
    $isReasoningEffort = $effort -cin @('low', 'medium', 'high', 'xhigh')
    $tokenBudget = 0
    if (-not $isReasoningEffort) {
        if (-not [int]::TryParse($effort, [ref]$tokenBudget) -or $tokenBudget -lt 1) {
            throw "BF_INVALID: unsupported council effort: $effort"
        }
    }
    if ($Binding.protocol -ceq 'openai_responses') {
        $body = [ordered]@{
            model = [string]$Binding.model
            input = $PromptText
            text = [ordered]@{
                format = [ordered]@{ type = 'json_object' }
            }
        }
        if ($isReasoningEffort) { $body.reasoning = [ordered]@{ effort = $effort } }
        else { $body.max_output_tokens = $tokenBudget }
        return [ordered]@{ path = '/responses'; body = $body }
    }
    $body = [ordered]@{
        model = [string]$Binding.model
        messages = @(@{ role = 'user'; content = $PromptText })
        response_format = [ordered]@{ type = 'json_object' }
    }
    if ($isReasoningEffort) { $body.reasoning_effort = $effort }
    else { $body.max_tokens = $tokenBudget }
    return [ordered]@{ path = '/chat/completions'; body = $body }
}

function Get-BSLFlowEnvelopeField {
    param($Object, [Parameter(Mandatory)][string]$Name)
    if ($null -eq $Object) { return $null }
    if ($Object -is [System.Collections.IDictionary]) {
        if ($Object.Contains($Name)) { return $Object[$Name] }
        return $null
    }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    return $property.Value
}

function Read-BSLFlowCouncilModelText {
    param([Parameter(Mandatory)]$Envelope, [Parameter(Mandatory)][string]$Protocol)
    # Provider envelopes wrap model text; the role payload is never the raw body.
    # OpenAI Responses: output[] message items carry content[] output_text parts
    # (plus the convenience output_text field). Chat Completions (incl. DeepSeek):
    # choices[0].message.content, string or content-part array.
    if ($Protocol -ceq 'openai_responses') {
        $convenience = Get-BSLFlowEnvelopeField $Envelope 'output_text'
        if ($convenience -is [string] -and $convenience.Trim()) { return [string]$convenience }
        $texts = [System.Collections.Generic.List[string]]::new()
        foreach ($item in @(Get-BSLFlowEnvelopeField $Envelope 'output')) {
            if ($null -eq $item) { continue }
            foreach ($part in @(Get-BSLFlowEnvelopeField $item 'content')) {
                if ($null -ne $part -and [string](Get-BSLFlowEnvelopeField $part 'type') -ceq 'output_text') {
                    $text = Get-BSLFlowEnvelopeField $part 'text'
                    if ($text -is [string] -and $text.Trim()) { $texts.Add([string]$text) }
                }
            }
        }
        if ($texts.Count -eq 0) { throw 'BF_INVALID_RESPONSE: responses envelope carries no output_text.' }
        return ($texts -join "`n")
    }
    $choices = @(Get-BSLFlowEnvelopeField $Envelope 'choices')
    if ($choices.Count -eq 0 -or $null -eq $choices[0]) { throw 'BF_INVALID_RESPONSE: chat envelope carries no choices.' }
    $content = Get-BSLFlowEnvelopeField (Get-BSLFlowEnvelopeField $choices[0] 'message') 'content'
    if ($content -is [string] -and $content.Trim()) { return [string]$content }
    $texts = [System.Collections.Generic.List[string]]::new()
    foreach ($part in @($content)) {
        if ($null -eq $part) { continue }
        $text = Get-BSLFlowEnvelopeField $part 'text'
        if ($text -is [string] -and $text.Trim()) { $texts.Add([string]$text) }
    }
    if ($texts.Count -eq 0) { throw 'BF_INVALID_RESPONSE: chat choice carries no message content.' }
    return ($texts -join "`n")
}

function Assert-BSLFlowCouncilTerminalEnvelope {
    param(
        [Parameter(Mandatory)]$Envelope,
        [Parameter(Mandatory)][string]$Protocol
    )
    if ($Protocol -ceq 'openai_responses') {
        $responseStatus = Get-BSLFlowEnvelopeField $Envelope 'status'
        if ($responseStatus -isnot [string] -or $responseStatus -cne 'completed') {
            throw 'BF_INVALID_RESPONSE: responses envelope is not terminal; status must be completed.'
        }
        return
    }
    $choices = @(Get-BSLFlowEnvelopeField $Envelope 'choices')
    if ($choices.Count -eq 0 -or $null -eq $choices[0]) {
        throw 'BF_INVALID_RESPONSE: chat envelope carries no choices.'
    }
    $finishReason = Get-BSLFlowEnvelopeField $choices[0] 'finish_reason'
    if ($finishReason -isnot [string] -or $finishReason -cne 'stop') {
        throw 'BF_INVALID_RESPONSE: chat envelope is not terminal; finish_reason must be stop.'
    }
}

function Read-BSLFlowCouncilJsonResult {
    param([Parameter(Mandatory)][string]$RawText)
    # Strict single-JSON extraction, never two candidates. Whole object first:
    # a valid envelope may itself contain fenced text inside string fields.
    $trimmed = $RawText.Trim()
    try { return ($trimmed | ConvertFrom-Json -ErrorAction Stop) }
    catch { }
    $fences = @([regex]::Matches($trimmed, '(?s)```(?:json)?\s*(?<body>\{.*\})\s*```'))
    if ($fences.Count -gt 1) { throw 'BF_INVALID_RESPONSE: ambiguous response with several JSON candidates.' }
    if ($fences.Count -eq 1) {
        try { return ($fences[0].Groups['body'].Value | ConvertFrom-Json -ErrorAction Stop) }
        catch { throw 'BF_INVALID_RESPONSE: malformed fenced JSON payload.' }
    }
    # Balanced outer-object extraction: from the first '{' to the brace that
    # closes it, ignoring braces inside string literals. This handles prose
    # around the object without ever accepting several objects.
    $first = $trimmed.IndexOf('{')
    if ($first -ge 0) {
        $inString = $false
        $escaped = $false
        $depth = 0
        $end = -1
        for ($i = $first; $i -lt $trimmed.Length; $i++) {
            $ch = $trimmed[$i]
            if ($escaped) { $escaped = $false; continue }
            if ($ch -eq '\') { if ($inString) { $escaped = $true }; continue }
            if ($ch -eq '"') { $inString = -not $inString; continue }
            if ($inString) { continue }
            if ($ch -eq '{') { $depth++ }
            elseif ($ch -eq '}') { $depth--; if ($depth -eq 0) { $end = $i; break } }
        }
        if ($end -gt $first) {
            # Only accept the balanced object when it spans to the end of the
            # payload (modulo trailing prose-free whitespace): an object plus
            # trailing fragments stays ambiguous and is rejected.
            $tail = $trimmed.Substring($end + 1).Trim()
            if ($tail -and $tail.Contains('{')) { throw 'BF_INVALID_RESPONSE: response must contain exactly one JSON object.' }
            $candidate = $trimmed.Substring($first, $end - $first + 1)
            try { return ($candidate | ConvertFrom-Json -ErrorAction Stop) }
            catch {
                # Long model strings sometimes carry raw control characters that
                # break strict JSON. Escape them and retry once; content is
                # otherwise untouched.
                $repaired = [regex]::Replace($candidate, "[\x00-\x08\x0B\x0C\x0E-\x1F]", { param($m) ('\u{0:x4}' -f [int][char]$m.Value[0]) })
                try { return ($repaired | ConvertFrom-Json -ErrorAction Stop) }
                catch { throw 'BF_INVALID_RESPONSE: malformed JSON payload.' }
            }
        }
    }
    $candidates = @([regex]::Matches($trimmed, '\{[^{}]*\}'))
    if ($candidates.Count -ge 2) { throw 'BF_INVALID_RESPONSE: response must contain exactly one JSON object.' }
    throw 'BF_INVALID_RESPONSE: malformed JSON payload.'
}

function Invoke-BSLFlowCouncilApi {
    param(
        [Parameter(Mandatory)]$Binding,
        [Parameter(Mandatory)][string]$PromptText,
        [Parameter(Mandatory)][string]$Credential,
        [Parameter(Mandatory)][string]$AttemptDir,
        [int]$TimeoutSeconds = 120,
        [int]$MaxInputBytes = 1048576,
        [int]$MaxOutputBytes = 1048576,
        [scriptblock]$HttpSend,
        [System.Threading.CancellationToken]$CancellationToken = [System.Threading.CancellationToken]::None
    )
    New-Item -ItemType Directory -Path $AttemptDir -Force | Out-Null
    $endpoint = $null
    $inputBytes = 0L
    $outputBytes = 0L
    $status = 0
    $dispatchStarted = $false
    try {
        if ([string]::IsNullOrWhiteSpace($Credential)) { throw 'BF_NOT_DISPATCHED: council credential is empty.' }
        if ($TimeoutSeconds -lt 1 -or $TimeoutSeconds -gt 900) { throw 'BF_INVALID: council timeout must be between 1 and 900 seconds.' }
        if ($MaxInputBytes -lt 1 -or $MaxInputBytes -gt 16777216) { throw 'BF_INVALID: council input limit must be between 1 and 16777216 bytes.' }
        if ($MaxOutputBytes -lt 1 -or $MaxOutputBytes -gt 16777216) { throw 'BF_INVALID: council output limit must be between 1 and 16777216 bytes.' }
        if ($CancellationToken.IsCancellationRequested) { throw 'BF_NOT_DISPATCHED: council request was cancelled before dispatch.' }

        $endpoint = $Binding.endpoint
        $scheme = ([string]$endpoint.scheme).ToLowerInvariant()
        $endpointHost = [string]$endpoint.host
        $hostForUri = $endpointHost.Trim('[', ']')
        $port = 0
        try { $port = [int]$endpoint.port } catch { throw 'BF_NOT_DISPATCHED: invalid council endpoint port.' }
        $basePath = [string]$endpoint.base_path
        if ($scheme -notin @('https', 'http') -or [string]::IsNullOrWhiteSpace($hostForUri)) {
            throw 'BF_NOT_DISPATCHED: invalid council endpoint URL.'
        }
        if ($port -lt 1 -or $port -gt 65535) { throw 'BF_NOT_DISPATCHED: invalid council endpoint port.' }
        if ($hostForUri -match '[\\/@?#]') { throw 'BF_NOT_DISPATCHED: invalid council endpoint host.' }
        if ([string]::IsNullOrWhiteSpace($basePath)) { $basePath = '/' }
        if (-not $basePath.StartsWith('/') -or $basePath -match '[?#]' -or $basePath -match '(^|/)\.\.?(/|$)') {
            throw 'BF_NOT_DISPATCHED: invalid council endpoint base path.'
        }
        $isLoopback = $hostForUri -ieq 'localhost' -or $hostForUri -ieq '127.0.0.1' -or $hostForUri -ieq '::1'
        $parsedAddress = $null
        if ([System.Net.IPAddress]::TryParse($hostForUri, [ref]$parsedAddress)) { $isLoopback = [System.Net.IPAddress]::IsLoopback($parsedAddress) }
        if ($scheme -eq 'http' -and -not $isLoopback) {
            throw 'BF_NOT_DISPATCHED: plain HTTP is allowed only for loopback council endpoints.'
        }
        $request = Get-BSLFlowCouncilApiRequest -Binding $Binding -PromptText $PromptText
        $urlHost = if ($hostForUri.Contains(':')) { "[$hostForUri]" } else { $hostForUri }
        $url = ("{0}://{1}:{2}{3}" -f $scheme, $urlHost, $port, $basePath.TrimEnd('/')) + '/' + ([string]$request.path).TrimStart('/')
        $uri = $null
        try { $uri = [System.Uri]::new($url, [System.UriKind]::Absolute) } catch { throw 'BF_NOT_DISPATCHED: invalid council endpoint URL.' }
        $uriHost = $uri.Host.Trim('[', ']').ToLowerInvariant()
        if ($uri.Scheme -cne $scheme -or $uriHost -cne $hostForUri.ToLowerInvariant() -or $uri.Port -ne $port -or $uri.UserInfo -or $uri.Query -or $uri.Fragment) {
            throw 'BF_NOT_DISPATCHED: endpoint drift between binding and request URL.'
        }

        $bodyJson = ($request.body | ConvertTo-Json -Depth 20 -Compress)
        $utf8 = [Text.UTF8Encoding]::new($false, $true)
        $inputBytes = $utf8.GetByteCount($bodyJson)
        if ($inputBytes -gt [long]$MaxInputBytes) { throw 'BF_NOT_DISPATCHED: council request exceeds the input limit.' }
        $safeBindingJson = ($Binding | ConvertTo-Json -Depth 10 -Compress)
        $safeBindingJson = Protect-BSLFlowCouncilDiagnostic -Text $safeBindingJson -Secrets @($Credential)
        [IO.File]::WriteAllText((Join-Path $AttemptDir 'request-meta.json'), $safeBindingJson)

        $send = $HttpSend
        if ($null -eq $send) {
            # This function may be dot-sourced inside a dispatcher scope. Capture
            # the helper explicitly because a closure invoked by HttpClient does
            # not reliably resolve functions from that parent local scope.
            $readResponseBody = ${function:Read-BSLFlowCouncilHttpResponseBody}
            if ($null -eq $readResponseBody) { throw 'BF_NOT_DISPATCHED: response-body reader is unavailable in the transport scope.' }
            $send = {
                param($MethodUrl, $MethodBody, $MethodToken, $MethodTimeout, $MethodMaxOutputBytes, $MethodCancellationToken)
                $connectSeconds = [Math]::Max(1, [Math]::Min($MethodTimeout, 30))
                $handler = [System.Net.Http.SocketsHttpHandler]::new()
                $handler.AllowAutoRedirect = $false
                $handler.UseProxy = $false
                $handler.ConnectTimeout = [System.TimeSpan]::FromSeconds($connectSeconds)
                $client = [System.Net.Http.HttpClient]::new($handler)
                $timeoutSource = $null
                $message = $null
                $response = $null
                try {
                    # ResponseHeadersRead returns after headers, so HttpClient.Timeout
                    # alone would leave a stalled response body unbounded. One linked
                    # token covers the caller cancellation and the complete operation,
                    # including every response-stream read.
                    $timeoutSource = [System.Threading.CancellationTokenSource]::CreateLinkedTokenSource([System.Threading.CancellationToken]$MethodCancellationToken)
                    $timeoutSource.CancelAfter([System.TimeSpan]::FromSeconds($MethodTimeout))
                    $operationToken = $timeoutSource.Token
                    $client.Timeout = [System.Threading.Timeout]::InfiniteTimeSpan
                    $message = [System.Net.Http.HttpRequestMessage]::new([System.Net.Http.HttpMethod]::Post, $MethodUrl)
                    $message.Content = [System.Net.Http.StringContent]::new($MethodBody, [System.Text.Encoding]::UTF8, 'application/json')
                    $message.Headers.Authorization = [System.Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer', $MethodToken)
                    $response = $client.SendAsync($message, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead, $operationToken).GetAwaiter().GetResult()
                    $responseStatus = [int]$response.StatusCode
                    if ($responseStatus -ge 300 -and $responseStatus -lt 400) {
                        $location = ''
                        try { if ($null -ne $response.Headers.Location) { $location = $response.Headers.Location.ToString() } } catch { }
                        throw "BF_REDIRECT: council redirect refused: $location"
                    }
                    $text = & $readResponseBody -Response $response -MaxBytes $MethodMaxOutputBytes -CancellationToken $operationToken
                    return [pscustomobject][ordered]@{ status = $responseStatus; body = [string]$text }
                }
                finally {
                    if ($null -ne $response) { $response.Dispose() }
                    if ($null -ne $message) { $message.Dispose() }
                    if ($null -ne $timeoutSource) { $timeoutSource.Dispose() }
                    $client.Dispose()
                    $handler.Dispose()
                }
            }.GetNewClosure()
        }

        $dispatchStarted = $true
        $result = & $send $url $bodyJson $Credential $TimeoutSeconds $MaxOutputBytes $CancellationToken
        if ($null -eq $result) { throw 'BF_UNKNOWN_AFTER_DISPATCH: council transport returned no response.' }
        try { $status = [int]$result.status } catch { throw 'BF_UNKNOWN_AFTER_DISPATCH: council transport returned an invalid status.' }
        if ($status -ge 300 -and $status -lt 400) { throw 'BF_FAILED_BEFORE_ACCEPTANCE: council redirect was refused.' }
        if ($status -ge 500) { throw 'BF_UNKNOWN_AFTER_DISPATCH: council provider returned a server error.' }
        if ($status -eq 429 -or ($status -ge 400 -and $status -lt 500)) {
            throw 'BF_FAILED_BEFORE_ACCEPTANCE: council request was not accepted by the provider.'
        }
        if ($status -ne 200) { throw 'BF_UNKNOWN_AFTER_DISPATCH: unexpected council provider status.' }
        $bodyText = [string]$result.body
        try { $outputBytes = $utf8.GetByteCount($bodyText) } catch { throw 'BF_INVALID_RESPONSE: council response is not valid UTF-8.' }
        if ($outputBytes -gt [long]$MaxOutputBytes) { throw 'BF_INVALID_RESPONSE: council response exceeds the output limit.' }
        [IO.File]::WriteAllText((Join-Path $AttemptDir 'raw-response.txt'), $bodyText)

        $envelope = Read-BSLFlowCouncilJsonResult $bodyText
        # A raw role object has no provider terminal receipt. Always require the
        # protocol envelope before extracting model text; fences are permitted only
        # inside that envelope's content and are parsed below.
        Assert-BSLFlowCouncilTerminalEnvelope -Envelope $envelope -Protocol ([string]$Binding.protocol)
        # Provider-observed identity/usage come from the response envelope, never
        # from the requested binding: a provider may alias or reroute models.
        $observedModel = $null
        try {
            $rawModel = Get-BSLFlowEnvelopeField $envelope 'model'
            if ($rawModel -is [string] -and ([string]$rawModel).Trim()) { $observedModel = ([string]$rawModel).Trim() }
        } catch { $observedModel = $null }
        $usage = $null
        try { $usage = Get-BSLFlowEnvelopeField $envelope 'usage' } catch { $usage = $null }
        $modelText = Read-BSLFlowCouncilModelText -Envelope $envelope -Protocol ([string]$Binding.protocol)
        [IO.File]::WriteAllText((Join-Path $AttemptDir 'model-text.txt'), $modelText)
        return [ordered]@{
            payload = (Read-BSLFlowCouncilJsonResult $modelText)
            observed_model = $observedModel
            usage = $usage
        }
    }
    catch {
        $rawMessage = [string]$_.Exception.Message
        $message = Protect-BSLFlowCouncilDiagnostic -Text $rawMessage
        $code = 'BF_UNKNOWN_AFTER_DISPATCH'
        if ($message -match 'BF_INVALID_RESPONSE') { $code = 'BF_INVALID_RESPONSE' }
        elseif ($message -match '^BF_INVALID(?:\:|\s)') { $code = 'BF_INVALID' }
        elseif ($message -match 'BF_NOT_DISPATCHED') { $code = 'BF_NOT_DISPATCHED' }
        elseif ($message -match 'BF_FAILED_BEFORE_ACCEPTANCE') { $code = 'BF_FAILED_BEFORE_ACCEPTANCE' }
        elseif ($message -match 'BF_REDIRECT') {
            $code = 'BF_FAILED_BEFORE_ACCEPTANCE'
            $message = 'BF_FAILED_BEFORE_ACCEPTANCE: council redirect was refused.'
        }
        elseif ($_.Exception -is [OperationCanceledException] -or $message -match '(?i)canceled|timeout|TaskCanceled') { $code = 'BF_UNKNOWN_AFTER_DISPATCH' }
        if ($code -eq 'BF_UNKNOWN_AFTER_DISPATCH' -and $message -notmatch 'BF_UNKNOWN_AFTER_DISPATCH') {
            $message = "BF_UNKNOWN_AFTER_DISPATCH: council transport failed after dispatch: $message"
        }
        $publicMessage = $message
        if ($Credential.Length -ge 4) {
            $publicMessage = Protect-BSLFlowCouncilDiagnostic -Text $message -Secrets @($Credential)
        }
        Write-BSLFlowCouncilTransportDiagnostic -AttemptDir $AttemptDir -Code $code -Message $message -Endpoint $endpoint -Status $status -InputBytes $inputBytes -OutputBytes $outputBytes -MaxOutputBytes $MaxOutputBytes -DispatchStarted $dispatchStarted -Secrets @($Credential)
        throw $publicMessage
    }
}
