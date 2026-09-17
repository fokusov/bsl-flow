#Requires -Version 7.0
Set-StrictMode -Version Latest

$script:BFJsonMaximumBytes = 16MB
$script:BFJsonMaximumDepth = 100
$script:BFJsonMaximumValues = 1000000
if ($null -eq (Get-Variable -Name BFStorageLocks -Scope Script -ErrorAction SilentlyContinue)) {
    $script:BFStorageLocks = [System.Collections.Generic.Dictionary[string,object]]::new([System.StringComparer]::OrdinalIgnoreCase)
}

function New-BFError {
    param([Parameter(Mandatory = $true)][string]$Kind, [Parameter(Mandatory = $true)][string]$Message)
    return [System.InvalidOperationException]::new(($Kind + ': ' + $Message))
}

function ConvertTo-BFJsonString {
    param([Parameter(Mandatory = $true)][AllowEmptyString()][string]$Value)
    $builder = [System.Text.StringBuilder]::new($Value.Length + 2)
    [void]$builder.Append('"')
    for ($index = 0; $index -lt $Value.Length; $index++) {
        $character = $Value[$index]
        $number = [int]$character
        if ([char]::IsHighSurrogate($character)) {
            if (($index + 1) -ge $Value.Length -or -not [char]::IsLowSurrogate($Value[$index + 1])) {
                throw (New-BFError 'BF_INVALID' 'String contains an unpaired UTF-16 surrogate.')
            }
            [void]$builder.Append($character)
            $index++
            [void]$builder.Append($Value[$index])
            continue
        }
        if ([char]::IsLowSurrogate($character)) { throw (New-BFError 'BF_INVALID' 'String contains an unpaired UTF-16 surrogate.') }
        $escaped = $true
        switch ($number) {
            8  { [void]$builder.Append('\b') }
            9  { [void]$builder.Append('\t') }
            10 { [void]$builder.Append('\n') }
            12 { [void]$builder.Append('\f') }
            13 { [void]$builder.Append('\r') }
            34 { [void]$builder.Append('\"') }
            92 { [void]$builder.Append('\\') }
            default { $escaped = $false }
        }
        if ($escaped) { continue }
        if ($number -lt 32) { [void]$builder.Append(('\u{0:x4}' -f $number)) }
        else { [void]$builder.Append($character) }
    }
    [void]$builder.Append('"')
    return $builder.ToString()
}

function ConvertTo-BFCanonicalNumber {
    param([Parameter(Mandatory = $true)][object]$Value)
    $culture = [System.Globalization.CultureInfo]::InvariantCulture
    switch ([System.Type]::GetTypeCode($Value.GetType())) {
        'SByte'   { return ([sbyte]$Value).ToString($culture) }
        'Byte'    { return ([byte]$Value).ToString($culture) }
        'Int16'   { return ([int16]$Value).ToString($culture) }
        'UInt16'  { return ([uint16]$Value).ToString($culture) }
        'Int32'   { return ([int32]$Value).ToString($culture) }
        'UInt32'  { return ([uint32]$Value).ToString($culture) }
        'Int64'   { return ([int64]$Value).ToString($culture) }
        'UInt64'  { return ([uint64]$Value).ToString($culture) }
        'Decimal' { return ([decimal]$Value).ToString('G29', $culture) }
        'Single'  {
            $number = [single]$Value
            if ([single]::IsNaN($number) -or [single]::IsInfinity($number)) { throw (New-BFError 'BF_INVALID' 'Non-finite numbers are not valid JSON values.') }
            return $number.ToString('G9', $culture)
        }
        'Double'  {
            $number = [double]$Value
            if ([double]::IsNaN($number) -or [double]::IsInfinity($number)) { throw (New-BFError 'BF_INVALID' 'Non-finite numbers are not valid JSON values.') }
            return $number.ToString('G17', $culture)
        }
        default { throw (New-BFError 'BF_INVALID' ("Unsupported numeric type: {0}." -f $Value.GetType().FullName)) }
    }
}

function Get-BFCanonicalJson {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][AllowNull()][object]$Value)
    function Convert-Value {
        param([AllowNull()][object]$Item, [int]$Depth)
        if ($Depth -gt $script:BFJsonMaximumDepth) { throw (New-BFError 'BF_INVALID' 'JSON value exceeds the maximum nesting depth.') }
        if ($null -eq $Item) { return 'null' }
        if ($Item -is [string]) { return ConvertTo-BFJsonString $Item }
        if ($Item -is [bool]) { if ($Item) { return 'true' } else { return 'false' } }
        if ($Item -is [char] -or $Item -is [datetime] -or $Item -is [datetimeoffset] -or $Item.GetType().IsEnum) { throw (New-BFError 'BF_INVALID' ("Unsupported JSON value type: {0}." -f $Item.GetType().FullName)) }
        if ($Item -is [System.Numerics.BigInteger]) { return ([System.Numerics.BigInteger]$Item).ToString([System.Globalization.CultureInfo]::InvariantCulture) }
        if ($Item.GetType().IsPrimitive -or $Item -is [decimal]) { return ConvertTo-BFCanonicalNumber $Item }
        if ($Item -is [System.Collections.IDictionary] -or $Item -is [pscustomobject]) {
            $values = [System.Collections.Generic.Dictionary[string,object]]::new([System.StringComparer]::Ordinal)
            $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
            if ($Item -is [System.Collections.IDictionary]) {
                foreach ($keyObject in $Item.Keys) {
                    if ($keyObject -isnot [string]) { throw (New-BFError 'BF_INVALID' 'JSON object keys must be strings.') }
                    $key = [string]$keyObject
                    if (-not $seen.Add($key)) { throw (New-BFError 'BF_INVALID' ("Duplicate JSON object key: {0}." -f $key)) }
                    $values.Add($key, $Item[$keyObject])
                }
            }
            else {
                foreach ($property in $Item.PSObject.Properties) {
                    if (-not $seen.Add($property.Name)) { throw (New-BFError 'BF_INVALID' ("Duplicate JSON object key: {0}." -f $property.Name)) }
                    $values.Add($property.Name, $property.Value)
                }
            }
            $keys = [string[]]@($values.Keys)
            [array]::Sort($keys, [System.StringComparer]::Ordinal)
            $parts = [System.Collections.Generic.List[string]]::new()
            foreach ($key in $keys) { $parts.Add((ConvertTo-BFJsonString $key) + ':' + (Convert-Value $values[$key] ($Depth + 1))) }
            return '{' + [string]::Join(',', $parts.ToArray()) + '}'
        }
        if ($Item -is [System.Collections.IEnumerable]) {
            $parts = [System.Collections.Generic.List[string]]::new()
            foreach ($element in $Item) { $parts.Add((Convert-Value $element ($Depth + 1))) }
            return '[' + [string]::Join(',', $parts.ToArray()) + ']'
        }
        throw (New-BFError 'BF_INVALID' ("Unsupported JSON value type: {0}." -f $Item.GetType().FullName))
    }
    return Convert-Value $Value 0
}

function Get-BFHash {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][AllowNull()][object]$Value)
    $json = Get-BFCanonicalJson $Value
    $encoding = [System.Text.UTF8Encoding]::new($false, $true)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { return ([System.BitConverter]::ToString($sha.ComputeHash($encoding.GetBytes($json))).Replace('-', '').ToLowerInvariant()) }
    finally { $sha.Dispose() }
}

function Assert-BFSafePath {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Path)
    if ([string]::IsNullOrWhiteSpace($Path) -or -not [System.IO.Path]::IsPathRooted($Path)) { throw (New-BFError 'BF_INVALID' 'Path must be an absolute filesystem path.') }
    if ($Path.StartsWith('\\?\') -or $Path.StartsWith('\\.\') -or $Path -match '^[^:]+::') { throw (New-BFError 'BF_INVALID' 'Device and provider-qualified paths are not allowed.') }
    try { $fullPath = [System.IO.Path]::GetFullPath($Path) }
    catch { throw (New-BFError 'BF_INVALID' ("Path is invalid: {0}" -f $_.Exception.Message)) }
    $root = [System.IO.Path]::GetPathRoot($fullPath)
    if ($fullPath.Substring($root.Length).Contains(':')) { throw (New-BFError 'BF_INVALID' 'Alternate data stream paths are not allowed.') }
    $cursor = $fullPath
    while (-not [string]::IsNullOrEmpty($cursor)) {
        if ([System.IO.File]::Exists($cursor) -or [System.IO.Directory]::Exists($cursor)) {
            try { $item = Get-Item -LiteralPath $cursor -Force -ErrorAction Stop }
            catch { throw (New-BFError 'BF_INVALID' ("Cannot inspect path: {0}" -f $cursor)) }
            if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) { throw (New-BFError 'BF_INVALID' ("Path contains a reparse point: {0}" -f $cursor)) }
        }
        $parent = [System.IO.Path]::GetDirectoryName($cursor.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar))
        if ([string]::IsNullOrEmpty($parent) -or $parent -eq $cursor) { break }
        $cursor = $parent
    }
    return $fullPath
}

# Legacy controller journals live below a verified worktree.  The native
# repository is rooted at the Git common dir, so a source write must check the
# common-dir task path before it creates either the task directory or a JSON
# file.  Keep this boundary local to task-shaped paths; runtime, provider and
# other generic storage directories must retain their existing semantics.
function Test-BFStoragePathWithin {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$Candidate,
        [Parameter(Mandatory = $true)][string]$Root
    )
    $candidateValue = $Candidate.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    $rootValue = $Root.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    if ([string]::Equals($candidateValue, $rootValue, [StringComparison]::OrdinalIgnoreCase)) { return $true }
    $separator = [System.IO.Path]::DirectorySeparatorChar
    if ($candidateValue.StartsWith($rootValue + $separator, [StringComparison]::OrdinalIgnoreCase)) { return $true }
    if ([System.IO.Path]::AltDirectorySeparatorChar -ne $separator -and $candidateValue.StartsWith($rootValue + [System.IO.Path]::AltDirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) { return $true }
    return $false
}

function Get-BFLegacyTaskWriteContext {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Path)
    $fullPath = Assert-BFSafePath $Path
    $cursor = $fullPath.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    # The bounded walk is syntactic and never enumerates a repository or scans
    # a .git tree.  It recognizes only the exact .bsl-flow/tasks/<lowercase UUID>
    # shape used by the legacy controller.
    for ($depth = 0; $depth -lt 64 -and -not [string]::IsNullOrEmpty($cursor); $depth++) {
        $taskId = [System.IO.Path]::GetFileName($cursor)
        $tasksDirectory = [System.IO.Path]::GetDirectoryName($cursor)
        if (-not [string]::IsNullOrEmpty($tasksDirectory) -and $taskId -cmatch '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' -and [System.IO.Path]::GetFileName($tasksDirectory) -ieq 'tasks') {
            $bslFlowDirectory = [System.IO.Path]::GetDirectoryName($tasksDirectory)
            if (-not [string]::IsNullOrEmpty($bslFlowDirectory) -and [System.IO.Path]::GetFileName($bslFlowDirectory) -ieq '.bsl-flow') {
                $projectRoot = [System.IO.Path]::GetDirectoryName($bslFlowDirectory)
                if (-not [string]::IsNullOrEmpty($projectRoot)) {
                    $taskRoot = [System.IO.Path]::GetFullPath((Join-Path $tasksDirectory $taskId))
                    if (Test-BFStoragePathWithin $fullPath $taskRoot) {
                        return [pscustomobject]@{
                            Path        = $fullPath
                            ProjectRoot = [System.IO.Path]::GetFullPath($projectRoot)
                            TaskRoot    = $taskRoot
                            TaskId      = $taskId
                        }
                    }
                }
            }
        }
        $parent = [System.IO.Path]::GetDirectoryName($cursor)
        if ([string]::IsNullOrEmpty($parent) -or $parent -eq $cursor) { break }
        $cursor = $parent.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    }
    return $null
}

function Get-BFStorageGitExecutable {
    [CmdletBinding()]
    param()
    try { $command = @(Get-Command git -CommandType Application -ErrorAction Stop | Select-Object -First 1) }
    catch { throw (New-BFError 'BF_BLOCKED' 'Git executable is unavailable for legacy ownership verification.') }
    if ($command.Count -ne 1) { throw (New-BFError 'BF_BLOCKED' 'Git executable is unavailable for legacy ownership verification.') }
    $path = ''
    if ($null -ne $command[0].PSObject.Properties['Source']) { $path = [string]$command[0].Source }
    if ([string]::IsNullOrWhiteSpace($path) -and $null -ne $command[0].PSObject.Properties['Path']) { $path = [string]$command[0].Path }
    if ([string]::IsNullOrWhiteSpace($path)) { throw (New-BFError 'BF_BLOCKED' 'Git executable has no verifiable path.') }
    $path = Assert-BFSafePath $path
    if (-not [System.IO.File]::Exists($path)) { throw (New-BFError 'BF_BLOCKED' 'Git executable path is not a regular file.') }
    return $path
}

function Invoke-BFStorageGitRead {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$ProjectRoot,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )
    $root = Assert-BFSafePath $ProjectRoot
    if (-not [System.IO.Directory]::Exists($root)) { throw (New-BFError 'BF_BLOCKED' 'Legacy task project root is not a directory.') }
    $nullConfig = if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) { 'NUL' } else { '/dev/null' }
    $start = [System.Diagnostics.ProcessStartInfo]::new()
    $start.FileName = Get-BFStorageGitExecutable
    $start.WorkingDirectory = $root
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    # Git routing/configuration variables are untrusted for this identity
    # probe.  Keep ordinary process variables, but remove every GIT_* override
    # and disable system/global config.  Explicit -c options also disable hooks
    # and fsmonitor, so this read cannot execute repository helpers.
    $environmentKeys = @($start.Environment.Keys)
    foreach ($key in $environmentKeys) {
        if ([string]$key -match '^(?i:GIT_)') { [void]$start.Environment.Remove([string]$key) }
    }
    $start.Environment['GIT_CONFIG_NOSYSTEM'] = '1'
    $start.Environment['GIT_CONFIG_GLOBAL'] = $nullConfig
    $start.Environment['GIT_CONFIG_SYSTEM'] = $nullConfig
    $start.Environment['GIT_TERMINAL_PROMPT'] = '0'
    foreach ($argument in @('--no-replace-objects', '-c', ('core.hooksPath=' + $nullConfig), '-c', 'core.fsmonitor=false', '--no-optional-locks', '-C', $root) + @($Arguments)) {
        [void]$start.ArgumentList.Add([string]$argument)
    }
    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $start
    try {
        try {
            if (-not $process.Start()) { throw (New-BFError 'BF_BLOCKED' 'Git identity probe did not start.') }
        }
        catch [System.Management.Automation.RuntimeException] { throw }
        catch { throw (New-BFError 'BF_BLOCKED' 'Git identity probe could not start.') }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        [void]$stderrTask.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0) { throw (New-BFError 'BF_BLOCKED' 'Git identity probe failed.') }
        return $stdout.Trim()
    }
    finally { $process.Dispose() }
}

function Get-BFVerifiedLegacyGitContext {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)]$Context)
    $projectRoot = Assert-BFSafePath $Context.ProjectRoot
    $topLevel = Assert-BFSafePath (Invoke-BFStorageGitRead $projectRoot @('rev-parse', '--show-toplevel'))
    if (-not [string]::Equals($topLevel, $projectRoot, [StringComparison]::OrdinalIgnoreCase)) {
        throw (New-BFError 'BF_BLOCKED' 'Legacy task path is not below the exact Git worktree root.')
    }
    $commonText = Invoke-BFStorageGitRead $projectRoot @('rev-parse', '--path-format=absolute', '--git-common-dir')
    if ([string]::IsNullOrWhiteSpace($commonText)) { throw (New-BFError 'BF_BLOCKED' 'Git common dir is empty.') }
    $commonDir = if ([System.IO.Path]::IsPathRooted($commonText)) { Assert-BFSafePath $commonText } else { Assert-BFSafePath (Join-Path $projectRoot $commonText) }
    if (-not [System.IO.Directory]::Exists($commonDir)) { throw (New-BFError 'BF_BLOCKED' 'Git common dir is not a directory.') }
    $store = Assert-BFSafePath (Join-Path $commonDir 'bsl-flow')
    $tasks = Assert-BFSafePath (Join-Path $store 'tasks')
    # Do not call Assert-BFSafePath on the target before checking it: a broken
    # reparse point at the UUID leaf is still an occupied canonical identity.
    $canonicalTask = [System.IO.Path]::GetFullPath((Join-Path $tasks $Context.TaskId))
    return [pscustomobject]@{
        ProjectRoot       = $projectRoot
        CommonDir         = $commonDir
        CanonicalStore    = $store
        CanonicalTasks    = $tasks
        CanonicalTask     = $canonicalTask
        TaskId            = $Context.TaskId
    }
}

function Get-BFStorageExistingItem {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Path)
    try { return Get-Item -LiteralPath $Path -Force -ErrorAction Stop }
    catch {
        $category = [string]$_.CategoryInfo.Category
        $errorId = [string]$_.FullyQualifiedErrorId
        if ($category -eq 'ObjectNotFound' -or $errorId -match 'PathNotFound|ItemNotFound') { return $null }
        throw (New-BFError 'BF_BLOCKED' 'Cannot inspect canonical task ownership path.')
    }
}

function Test-BFStorageTaskLockHeld {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$TaskRoot)
    $key = $TaskRoot.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar)
    if (-not $script:BFStorageLocks.ContainsKey($key)) { return $false }
    $stream = $script:BFStorageLocks[$key]
    return $null -ne $stream -and $stream.CanWrite
}

function Assert-BFLegacyTaskWriteAllowed {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [switch]$UnderSourceLock
    )
    $context = Get-BFLegacyTaskWriteContext $Path
    if ($null -eq $context) { return $null }
    $verified = Get-BFVerifiedLegacyGitContext $context
    if ($UnderSourceLock -and -not (Test-BFStorageTaskLockHeld $context.TaskRoot)) {
        throw (New-BFError 'BF_CONFLICT' 'Legacy task ownership recheck requires the source writer lock.')
    }
    $canonicalItem = Get-BFStorageExistingItem $verified.CanonicalTask
    if ($null -ne $canonicalItem) {
        throw (New-BFError 'BF_CONFLICT' ("Legacy task {0} is owned by the canonical native repository." -f $context.TaskId))
    }
    # A missing target is safe only when all existing ancestors are still safe;
    # this catches a reparse swap in the canonical store without following it.
    [void](Assert-BFSafePath $verified.CanonicalTask)
    return $verified
}

function Get-BFFileHash {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Path)
    $fullPath = Assert-BFSafePath $Path
    if (-not [System.IO.File]::Exists($fullPath)) { throw (New-BFError 'BF_INVALID' ("File does not exist: {0}" -f $fullPath)) }
    $stream = [System.IO.File]::Open($fullPath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::Read)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try { return ([System.BitConverter]::ToString($sha.ComputeHash($stream)).Replace('-', '').ToLowerInvariant()) }
    finally { $sha.Dispose(); $stream.Dispose() }
}

function Get-BFStoragePropertyValue {
    [CmdletBinding()]
    param(
        [AllowNull()][object]$Value,
        [Parameter(Mandatory = $true)][string]$Name
    )
    if ($null -eq $Value) { return $null }
    if ($Value -is [System.Collections.IDictionary]) {
        if ($Value.Contains($Name)) { return $Value[$Name] }
        return $null
    }
    $property = $Value.PSObject.Properties[$Name]
    if ($null -ne $property) { return $property.Value }
    return $null
}

function Get-BFObservedModelEffort {
    # Controller-observed resolved identity: the Codex rollout session file
    # (CODEX_HOME/sessions/YYYY/MM/DD/rollout-<ts>-<session_id>.jsonl) records
    # the turn_context payload with the model and reasoning effort the provider
    # actually ran. The session id must match the JSONL thread id exactly.
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$SessionId,
        [switch]$AllowMissing,
        [switch]$SelectLatest,
        [string]$TurnId
    )
    if ($SessionId -notmatch '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$') {
        throw (New-BFError 'BF_INVALID' 'Codex session id has an invalid format.')
    }
    if ($PSBoundParameters.ContainsKey('TurnId') -and [string]::IsNullOrWhiteSpace($TurnId)) {
        throw (New-BFError 'BF_INVALID' 'Codex turn id cannot be empty when selecting historical evidence.')
    }
    if ($PSBoundParameters.ContainsKey('TurnId') -and $SelectLatest) {
        throw (New-BFError 'BF_INVALID' 'Codex rollout selection cannot combine an exact turn with latest selection.')
    }
    $codeHome = $env:CODEX_HOME
    if ([string]::IsNullOrWhiteSpace($codeHome)) { $codeHome = Join-Path $env:USERPROFILE '.codex' }
    try { $sessionsRoot = Assert-BFSafePath (Join-Path (Assert-BFSafePath $codeHome) 'sessions') }
    catch { throw (New-BFError 'BF_BLOCKED' 'Codex sessions path is not a trusted local directory.') }
    if (-not (Test-Path -LiteralPath $sessionsRoot -PathType Container)) {
        if ($AllowMissing) {
            # `exec --ephemeral` is allowed to complete without creating a
            # sessions tree. Keep the ordinary worker receipt nullable while
            # preserving the strict current-agent provenance gate.
            return [ordered]@{ observed_model = $null; observed_effort = $null; rollout_path = $null; session_id = $SessionId; missing = $true }
        }
        throw (New-BFError 'BF_BLOCKED' ('Codex sessions directory is missing: {0}' -f $sessionsRoot))
    }
    # Locate the exact session suffix across the complete local session tree.
    # A long-running or resumed controller may legitimately inspect a rollout
    # older than yesterday; the UUID validation above keeps the lookup narrow.
    $candidates = @(Get-ChildItem -LiteralPath $sessionsRoot -File -Recurse -Filter ('*-' + $SessionId + '.jsonl') -ErrorAction SilentlyContinue)
    if ($candidates.Count -eq 0) {
        if ($AllowMissing) { return [ordered]@{ observed_model = $null; observed_effort = $null; rollout_path = $null; session_id = $SessionId; missing = $true } }
        throw (New-BFError 'BF_BLOCKED' ('Codex rollout session file not found for {0}.' -f $SessionId))
    }
    if ($candidates.Count -gt 1) {
        if (-not $SelectLatest) { throw (New-BFError 'BF_BLOCKED' ('Multiple Codex rollout files match session {0}.' -f $SessionId)) }
        $candidates = @($candidates | Sort-Object LastWriteTimeUtc, Name | Select-Object -Last 1)
    }
    $rolloutPath = Assert-BFSafePath $candidates[0].FullName
    $model = $null
    $effort = $null
    $lastTurnId = $null
    $selectedModel = $null
    $selectedEffort = $null
    $selectedTurnId = $null
    $selectedTurnCount = 0
    $sessionMeta = $null
    # The current controller turn can still be appending to its rollout. File.ReadLines
    # uses FileShare.Read and fails on that legitimate live file; an explicit shared
    # read lets us observe a stable prefix without weakening the identity checks.
    $stream = $null
    $reader = $null
    try {
        $stream = [System.IO.File]::Open($rolloutPath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::ReadWrite)
        $reader = [System.IO.StreamReader]::new($stream, [System.Text.UTF8Encoding]::new($false, $true), $true)
        while ($null -ne ($line = $reader.ReadLine())) {
            if ([string]::IsNullOrWhiteSpace($line)) { continue }
            try { $record = ConvertFrom-Json -InputObject $line -ErrorAction Stop } catch {
                # A writer may have exposed a partial final line. It cannot be used
                # as identity evidence; complete earlier records remain inspectable.
                continue
            }
            $recordType = Get-BFStoragePropertyValue $record 'type'
            if ($recordType -ceq 'session_meta') {
                if ($null -ne $sessionMeta) { throw (New-BFError 'BF_BLOCKED' ('Codex rollout contains duplicate session metadata for {0}.' -f $SessionId)) }
                $sessionMeta = $record
                $recordPayload = Get-BFStoragePropertyValue $record 'payload'
                $metaId = Get-BFStoragePropertyValue $recordPayload 'session_id'
                if ([string]::IsNullOrWhiteSpace([string]$metaId)) { $metaId = Get-BFStoragePropertyValue $recordPayload 'id' }
                if ([string]$metaId -cne $SessionId) { throw (New-BFError 'BF_BLOCKED' ('Codex rollout session metadata does not match {0}.' -f $SessionId)) }
                continue
            }
            if ($recordType -ceq 'turn_context') {
                # Keep the latest complete turn_context: old turns must not become
                # provenance after a model/effort change in the current session.
                $payload = Get-BFStoragePropertyValue $record 'payload'
                if ($null -eq $payload) { $model = $null; $effort = $null; $lastTurnId = $null; continue }
                $modelValue = Get-BFStoragePropertyValue $payload 'model'
                $effortValue = Get-BFStoragePropertyValue $payload 'effort'
                $turnIdValue = Get-BFStoragePropertyValue $payload 'turn_id'
                $model = if ($null -ne $modelValue) { [string]$modelValue } else { $null }
                $effort = if ($null -ne $effortValue) { [string]$effortValue } else { $null }
                $lastTurnId = if ($null -ne $turnIdValue) { [string]$turnIdValue } else { $null }
                if ($PSBoundParameters.ContainsKey('TurnId') -and [string]$turnIdValue -ceq $TurnId) {
                    $selectedTurnCount++
                    if ($selectedTurnCount -gt 1) {
                        throw (New-BFError 'BF_BLOCKED' ('Codex rollout contains duplicate turn identity {0}.' -f $TurnId))
                    }
                    $selectedModel = $model
                    $selectedEffort = $effort
                    $selectedTurnId = $lastTurnId
                }
            }
        }
    }
    catch [System.IO.IOException] {
        throw (New-BFError 'BF_BLOCKED' ('Cannot read Codex rollout for {0}: {1}' -f $SessionId, $_.Exception.Message))
    }
    finally {
        if ($null -ne $reader) { $reader.Dispose() }
        elseif ($null -ne $stream) { $stream.Dispose() }
    }
    if ($null -eq $sessionMeta) { throw (New-BFError 'BF_BLOCKED' ('Codex rollout session metadata is missing for {0}.' -f $SessionId)) }
    if ($PSBoundParameters.ContainsKey('TurnId')) {
        if ($selectedTurnCount -eq 0) {
            throw (New-BFError 'BF_BLOCKED' ('Codex rollout turn identity {0} was not found for {1}.' -f $TurnId, $SessionId))
        }
        $model = $selectedModel
        $effort = $selectedEffort
        $lastTurnId = $selectedTurnId
    }
    if ([string]::IsNullOrWhiteSpace($model) -or [string]::IsNullOrWhiteSpace($effort)) {
        $selection = if ($PSBoundParameters.ContainsKey('TurnId')) { 'selected' } else { 'latest' }
        throw (New-BFError 'BF_BLOCKED' ('Codex rollout {0} turn_context is missing resolved model/effort for {1}.' -f $selection, $SessionId))
    }
    return [ordered]@{ observed_model = $model; observed_effort = $effort; rollout_path = $rolloutPath; session_id = $SessionId; turn_id = $lastTurnId; missing = $false }
}

function Get-BFCurrentHostModelEffort {
    # CODEX_SESSION_ID identifies the host rollout session. CODEX_THREAD_ID is a
    # separate task/thread identity and has no trusted mapping to the current host
    # invocation, so it must never be used as a fallback: doing so could publish
    # a child rollout's model as the parent host model.
    [CmdletBinding()]
    param()
    $sessionId = [string]$env:CODEX_SESSION_ID
    if ([string]::IsNullOrWhiteSpace($sessionId)) {
        throw (New-BFError 'BF_BLOCKED' 'Current Codex host identity is unavailable: CODEX_SESSION_ID is required; CODEX_THREAD_ID is not a trusted host mapping.')
    }
    if ($sessionId -notmatch '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$') {
        throw (New-BFError 'BF_INVALID' 'CODEX_SESSION_ID has an invalid format.')
    }
    try {
        $observed = Get-BFObservedModelEffort -SessionId $sessionId -SelectLatest
        return [ordered]@{
            model = [string]$observed.observed_model; effort = [string]$observed.observed_effort
            session_id = $sessionId; turn_id = $observed.turn_id; rollout_path = $observed.rollout_path
            source = 'current_host_rollout'; capability_version = 'current-host-rollout-v1'
        }
    }
    catch {
        throw (New-BFError 'BF_BLOCKED' ('Current Codex host identity is unavailable; the exact CODEX_SESSION_ID rollout is required. ' + [string]$_.Exception.Message))
    }
}

function Test-BFJsonSyntax {
    param([Parameter(Mandatory = $true)][string]$Text)
    $state = [pscustomobject]@{ Text = $Text; Index = 0; Values = 0 }
    $length = $Text.Length
    $numberPattern = [regex]::new('\G-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?')
    $skipWhitespace = { while ($state.Index -lt $length -and " `t`r`n".IndexOf($state.Text[$state.Index]) -ge 0) { $state.Index++ } }
    $parseString = {
        if ($state.Index -ge $length -or $state.Text[$state.Index] -ne '"') { throw 'Expected a JSON string.' }
        $state.Index++; $builder = [System.Text.StringBuilder]::new()
        while ($state.Index -lt $length) {
            $character = $state.Text[$state.Index]; $state.Index++
            if ($character -eq '"') { return $builder.ToString() }
            if ([int]$character -lt 32) { throw 'Unescaped control character in JSON string.' }
            if ($character -ne '\') { [void]$builder.Append($character); continue }
            if ($state.Index -ge $length) { throw 'Incomplete JSON escape.' }
            $escape = $state.Text[$state.Index]; $state.Index++
            switch ($escape) {
                '"' { [void]$builder.Append('"') }
                '\' { [void]$builder.Append('\') }
                '/' { [void]$builder.Append('/') }
                'b' { [void]$builder.Append([char]8) }
                'f' { [void]$builder.Append([char]12) }
                'n' { [void]$builder.Append([char]10) }
                'r' { [void]$builder.Append([char]13) }
                't' { [void]$builder.Append([char]9) }
                'u' {
                    if (($state.Index + 4) -gt $length) { throw 'Incomplete JSON Unicode escape.' }
                    $hex = $state.Text.Substring($state.Index, 4)
                    if ($hex -notmatch '^[0-9A-Fa-f]{4}$') { throw 'Invalid JSON Unicode escape.' }
                    [void]$builder.Append([char][convert]::ToInt32($hex, 16)); $state.Index += 4
                }
                default { throw 'Invalid JSON escape.' }
            }
        }
        throw 'Unterminated JSON string.'
    }
    $parseValue = $null
    $parseValue = {
        param([int]$Depth)
        if ($Depth -gt $script:BFJsonMaximumDepth) { throw 'JSON exceeds the maximum nesting depth.' }
        $state.Values++; if ($state.Values -gt $script:BFJsonMaximumValues) { throw 'JSON contains too many values.' }
        & $skipWhitespace
        if ($state.Index -ge $length) { throw 'Expected a JSON value.' }
        $character = $state.Text[$state.Index]
        if ($character -eq '"') { [void](& $parseString); return 'scalar' }
        if ($character -eq '{') {
            $state.Index++; & $skipWhitespace
            $keys = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
            if ($state.Index -lt $length -and $state.Text[$state.Index] -eq '}') { $state.Index++; return 'object' }
            while ($true) {
                & $skipWhitespace; $key = & $parseString
                if (-not $keys.Add($key)) { throw ("Duplicate JSON object key: {0}." -f $key) }
                & $skipWhitespace
                if ($state.Index -ge $length -or $state.Text[$state.Index] -ne ':') { throw 'Expected a colon after JSON object key.' }
                $state.Index++; [void](& $parseValue ($Depth + 1)); & $skipWhitespace
                if ($state.Index -lt $length -and $state.Text[$state.Index] -eq ',') { $state.Index++; continue }
                if ($state.Index -lt $length -and $state.Text[$state.Index] -eq '}') { $state.Index++; return 'object' }
                throw 'Expected comma or closing brace in JSON object.'
            }
        }
        if ($character -eq '[') {
            $state.Index++; & $skipWhitespace
            if ($state.Index -lt $length -and $state.Text[$state.Index] -eq ']') { $state.Index++; return 'array' }
            while ($true) {
                [void](& $parseValue ($Depth + 1)); & $skipWhitespace
                if ($state.Index -lt $length -and $state.Text[$state.Index] -eq ',') { $state.Index++; continue }
                if ($state.Index -lt $length -and $state.Text[$state.Index] -eq ']') { $state.Index++; return 'array' }
                throw 'Expected comma or closing bracket in JSON array.'
            }
        }
        foreach ($literal in @('true', 'false', 'null')) {
            if (($state.Index + $literal.Length) -le $length -and $state.Text.Substring($state.Index, $literal.Length) -ceq $literal) { $state.Index += $literal.Length; return 'scalar' }
        }
        $match = $numberPattern.Match($state.Text, $state.Index)
        if (-not $match.Success) { throw 'Invalid JSON value.' }
        $state.Index += $match.Length; return 'scalar'
    }
    try {
        $kind = & $parseValue 0; & $skipWhitespace
        if ($state.Index -ne $length) { throw 'Unexpected data after JSON value.' }
        return $kind
    }
    catch { throw (New-BFError 'BF_INVALID' $_.Exception.Message) }
}

function Read-BFJson {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Path)
    $fullPath = Assert-BFSafePath $Path
    if (-not [System.IO.File]::Exists($fullPath)) { throw (New-BFError 'BF_INVALID' ("JSON file does not exist: {0}" -f $fullPath)) }
    $info = [System.IO.FileInfo]::new($fullPath)
    if ($info.Length -gt $script:BFJsonMaximumBytes) { throw (New-BFError 'BF_INVALID' 'JSON file exceeds the maximum allowed size.') }
    try { $bytes = [System.IO.File]::ReadAllBytes($fullPath) } catch { throw (New-BFError 'BF_INVALID' ("Cannot read JSON file: {0}" -f $_.Exception.Message)) }
    try { $text = [System.Text.UTF8Encoding]::new($false, $true).GetString($bytes) } catch { throw (New-BFError 'BF_INVALID' 'JSON file is not valid UTF-8.') }
    if ($text.Length -gt 0 -and $text[0] -eq [char]0xFEFF) { $text = $text.Substring(1) }
    $kind = Test-BFJsonSyntax $text
    if ($kind -ne 'object') { throw (New-BFError 'BF_INVALID' 'Top-level JSON value must be an object.') }
    try {
        $convertCommand = Get-Command ConvertFrom-Json -ErrorAction Stop
        if ($convertCommand.Parameters.ContainsKey('DateKind')) { return ConvertFrom-Json -InputObject $text -DateKind String -ErrorAction Stop }
        return ConvertFrom-Json -InputObject $text -ErrorAction Stop
    }
    catch { throw (New-BFError 'BF_INVALID' ("Cannot materialize JSON object: {0}" -f $_.Exception.Message)) }
}

function Invoke-BFAtomicReplace {
    param([Parameter(Mandatory = $true)][string]$Source, [Parameter(Mandatory = $true)][string]$Destination)
    if ($null -eq ('BFNativeFile' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class BFNativeFile {
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    public static extern bool MoveFileEx(string existingName, string newName, int flags);
}
'@
    }
    # Write-BFJson validated ordinary absolute paths. Only the native boundary
    # needs extended paths; its temporary filename can exceed MAX_PATH.
    $nativeSource = if ($Source.StartsWith('\\')) { '\\?\UNC\' + $Source.Substring(2) } else { '\\?\' + $Source }
    $nativeDestination = if ($Destination.StartsWith('\\')) { '\\?\UNC\' + $Destination.Substring(2) } else { '\\?\' + $Destination }
    if (-not [BFNativeFile]::MoveFileEx($nativeSource, $nativeDestination, 9)) {
        $errorCode = [Runtime.InteropServices.Marshal]::GetLastWin32Error()
        throw [System.ComponentModel.Win32Exception]::new($errorCode)
    }
}

function Write-BFJson {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Path, [Parameter(Mandatory = $true)][AllowNull()][object]$Value, [switch]$Replace)
    $fullPath = Assert-BFSafePath $Path
    $legacyContext = Get-BFLegacyTaskWriteContext $fullPath
    if ($null -ne $legacyContext) {
        # This check intentionally runs before parent creation.  A legacy
        # writer must not create a task-local input/attempt directory after a
        # canonical UUID has been reserved, even when the target is corrupt.
        [void](Assert-BFLegacyTaskWriteAllowed $fullPath)
    }
    $implicitSourceLock = $null
    $temporary = $null
    try {
        if ($null -ne $legacyContext) {
            # Direct task-local artifact writes are also serialized. Existing
            # controller paths already own this lock; direct saved actions take
            # it here so their final ownership check is genuinely under the
            # exact source .writer.lock.
            if (-not (Test-BFStorageTaskLockHeld $legacyContext.TaskRoot)) {
                $implicitSourceLock = Enter-BFLock $legacyContext.TaskRoot
            }
        }
        $parent = [System.IO.Path]::GetDirectoryName($fullPath)
        if ([string]::IsNullOrEmpty($parent)) { throw (New-BFError 'BF_INVALID' 'JSON path must have a parent directory.') }
        try { [void][System.IO.Directory]::CreateDirectory($parent) }
        catch { throw (New-BFError 'BF_INVALID' ("Cannot create JSON parent directory: {0}" -f $_.Exception.Message)) }
        [void](Assert-BFSafePath $parent)
        if (-not $Replace -and [System.IO.File]::Exists($fullPath)) { throw (New-BFError 'BF_CONFLICT' ("Refusing to overwrite JSON file: {0}" -f $fullPath)) }
        $bytes = [System.Text.UTF8Encoding]::new($false, $true).GetBytes((Get-BFCanonicalJson $Value))
        $temporary = [System.IO.Path]::Combine($parent, ('.' + [System.IO.Path]::GetFileName($fullPath) + '.' + [guid]::NewGuid().ToString('N') + '.tmp'))
        try {
            $stream = [System.IO.FileStream]::new($temporary, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
            try { $stream.Write($bytes, 0, $bytes.Length); $stream.Flush($true) } finally { $stream.Dispose() }
            if ($null -ne $legacyContext) {
                # Close the check-to-publish race for every task-local JSON
                # write, whether the source lock was inherited or acquired
                # above.
                [void](Assert-BFLegacyTaskWriteAllowed $fullPath -UnderSourceLock)
            }
            if ($Replace -and [System.IO.File]::Exists($fullPath)) { Invoke-BFAtomicReplace $temporary $fullPath } else { [System.IO.File]::Move($temporary, $fullPath) }
        }
        catch [System.IO.IOException] { throw (New-BFError 'BF_CONFLICT' ("Could not publish JSON file: {0}" -f $_.Exception.Message)) }
    }
    finally {
        if ($null -ne $temporary -and [System.IO.File]::Exists($temporary)) { [System.IO.File]::Delete($temporary) }
        if ($null -ne $implicitSourceLock) { $implicitSourceLock.Dispose() }
    }
}

function Enter-BFLock {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Directory)
    $fullDirectory = Assert-BFSafePath $Directory
    $legacyContext = Get-BFLegacyTaskWriteContext $fullDirectory
    if ($null -ne $legacyContext) {
        # Check before creating the directory or .writer.lock.  The canonical
        # target may be an orphan, a file, or malformed JSON; any existing leaf
        # owns the UUID for legacy routing purposes.
        [void](Assert-BFLegacyTaskWriteAllowed $fullDirectory)
    }
    if ([System.IO.File]::Exists($fullDirectory)) { throw (New-BFError 'BF_INVALID' 'Lock path is a file, not a directory.') }
    try { [void][System.IO.Directory]::CreateDirectory($fullDirectory) }
    catch { throw (New-BFError 'BF_INVALID' ("Cannot create lock directory: {0}" -f $_.Exception.Message)) }
    $fullDirectory = Assert-BFSafePath $fullDirectory
    if (-not [System.IO.Directory]::Exists($fullDirectory)) { throw (New-BFError 'BF_INVALID' 'Lock path is not a directory.') }
    if ($script:BFStorageLocks.ContainsKey($fullDirectory)) {
        $owned = $script:BFStorageLocks[$fullDirectory]
        if ($null -ne $owned -and $owned.CanWrite) { throw (New-BFError 'BF_CONFLICT' 'Writer lock is already held.') }
        [void]$script:BFStorageLocks.Remove($fullDirectory)
    }
    try { $stream = [System.IO.FileStream]::new([System.IO.Path]::Combine($fullDirectory, '.writer.lock'), [System.IO.FileMode]::OpenOrCreate, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None) }
    catch [System.IO.IOException] { throw (New-BFError 'BF_CONFLICT' 'Writer lock is held by another controller.') }
    $script:BFStorageLocks[$fullDirectory] = $stream
    if ($null -ne $legacyContext) {
        try {
            # Re-read canonical ownership while the source lock is held.  Do
            # not publish any task-local state if native adoption won the race.
            [void](Assert-BFLegacyTaskWriteAllowed $fullDirectory -UnderSourceLock)
        }
        catch {
            [void]$script:BFStorageLocks.Remove($fullDirectory)
            $stream.Dispose()
            throw
        }
    }
    return $stream
}

function Get-BFObjectProperty {
    param([Parameter(Mandatory = $true)][object]$Object, [Parameter(Mandatory = $true)][string]$Name)
    if ($Object -is [System.Collections.IDictionary]) { foreach ($key in $Object.Keys) { if ([string]$key -ieq $Name) { return $Object[$key] } }; return $null }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) { return $null }
    return $property.Value
}

function Test-BFObjectProperty {
    param([Parameter(Mandatory = $true)][object]$Object, [Parameter(Mandatory = $true)][string]$Name)
    if ($Object -is [System.Collections.IDictionary]) { foreach ($key in $Object.Keys) { if ([string]$key -ieq $Name) { return $true } }; return $false }
    return $null -ne $Object.PSObject.Properties[$Name]
}

function Read-BFJournal {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Directory)
    $fullDirectory = Assert-BFSafePath $Directory
    if (-not [System.IO.Directory]::Exists($fullDirectory)) { return $null }
    $revisionDirectory = [System.IO.Path]::Combine($fullDirectory, 'revisions')
    if (-not [System.IO.Directory]::Exists($revisionDirectory)) { return $null }
    [void](Assert-BFSafePath $revisionDirectory)
    $revisionFiles = @()
    try { $allRevisionFiles = @(Get-ChildItem -LiteralPath $revisionDirectory -File -Force -ErrorAction Stop) }
    catch { throw (New-BFError 'BF_BLOCKED' ("Cannot enumerate revision journal: {0}" -f $_.Exception.Message)) }
    foreach ($file in $allRevisionFiles) {
        if ($file.Name -match '\.tmp$') { continue }
        if ($file.Name -notmatch '^[0-9]{6}\.json$') { if ($file.Extension -ieq '.json') { throw (New-BFError 'BF_BLOCKED' ("Unexpected revision filename: {0}" -f $file.Name)) }; continue }
        $revisionFiles += $file
    }
    $revisionFiles = @($revisionFiles | Sort-Object Name)
    if ($revisionFiles.Count -eq 0) { return $null }
    $expectedNumber = 1; $previousHash = $null; $taskId = $null; $latest = $null
    foreach ($file in $revisionFiles) {
        $fileNumber = [int]$file.BaseName
        if ($fileNumber -ne $expectedNumber) { throw (New-BFError 'BF_BLOCKED' ("Revision chain has a gap before {0}." -f $file.Name)) }
        try { $state = Read-BFJson $file.FullName } catch { throw (New-BFError 'BF_BLOCKED' ("Corrupt revision {0}: {1}" -f $file.Name, $_.Exception.Message)) }
        foreach ($requiredField in @('revision', 'previous_sha256', 'task_id')) {
            if (-not (Test-BFObjectProperty $state $requiredField)) { throw (New-BFError 'BF_BLOCKED' ("Required chain field {0} is missing in {1}." -f $requiredField, $file.Name)) }
        }
        $revision = Get-BFObjectProperty $state 'revision'
        if (($revision -isnot [int] -and $revision -isnot [long]) -or [int64]$revision -ne $fileNumber) { throw (New-BFError 'BF_BLOCKED' ("Filename and revision disagree in {0}." -f $file.Name)) }
        $currentTaskId = Get-BFObjectProperty $state 'task_id'
        if ($currentTaskId -isnot [string] -or [string]::IsNullOrWhiteSpace($currentTaskId)) { throw (New-BFError 'BF_BLOCKED' ("task_id is missing in {0}." -f $file.Name)) }
        if ($null -eq $taskId) { $taskId = $currentTaskId } elseif ($currentTaskId -cne $taskId) { throw (New-BFError 'BF_BLOCKED' ("task_id changed in {0}." -f $file.Name)) }
        $linkedHash = Get-BFObjectProperty $state 'previous_sha256'
        if ($expectedNumber -eq 1) { if ($null -ne $linkedHash) { throw (New-BFError 'BF_BLOCKED' 'First revision must have null previous_sha256.') } }
        elseif ($linkedHash -isnot [string] -or $linkedHash -cne $previousHash) { throw (New-BFError 'BF_BLOCKED' ("Revision hash chain is broken at {0}." -f $file.Name)) }
        $previousHash = Get-BFHash $state; $latest = $state; $expectedNumber++
    }
    return $latest
}

function Write-BFRevision {
    [CmdletBinding()]
    param([Parameter(Mandatory = $true)][string]$Directory, [Parameter(Mandatory = $true)][object]$State, [Parameter(Mandatory = $true)][int64]$ExpectedRevision)
    if ($ExpectedRevision -lt 0) { throw (New-BFError 'BF_INVALID' 'ExpectedRevision cannot be negative.') }
    $fullDirectory = Assert-BFSafePath $Directory
    $legacyContext = Get-BFLegacyTaskWriteContext $fullDirectory
    if ($null -ne $legacyContext) {
        # Direct revision callers get the same pre-write ownership check as
        # Write-BFJson.  The lock check below retains the generic helper's
        # existing contract for non-task directories.
        [void](Assert-BFLegacyTaskWriteAllowed $fullDirectory)
    }
    if (-not $script:BFStorageLocks.ContainsKey($fullDirectory)) { throw (New-BFError 'BF_CONFLICT' 'Write-BFRevision requires the caller to hold the writer lock.') }
    $lock = $script:BFStorageLocks[$fullDirectory]
    if ($null -eq $lock -or -not $lock.CanWrite) { [void]$script:BFStorageLocks.Remove($fullDirectory); throw (New-BFError 'BF_CONFLICT' 'Writer lock is no longer held.') }
    if ($null -ne $legacyContext) {
        [void](Assert-BFLegacyTaskWriteAllowed $fullDirectory -UnderSourceLock)
    }
    [void](Get-BFCanonicalJson $State)
    if ($State -isnot [System.Collections.IDictionary] -and $State -isnot [pscustomobject]) { throw (New-BFError 'BF_INVALID' 'Revision state must be an object.') }
    $latest = Read-BFJournal $fullDirectory
    $actualRevision = if ($null -eq $latest) { 0L } else { [int64](Get-BFObjectProperty $latest 'revision') }
    if ($actualRevision -ne $ExpectedRevision) { throw (New-BFError 'BF_CONFLICT' ("Expected revision {0}, actual revision {1}." -f $ExpectedRevision, $actualRevision)) }
    if ($actualRevision -ge 999999) { throw (New-BFError 'BF_BLOCKED' 'Revision journal reached its supported limit.') }
    $newState = [ordered]@{}
    if ($State -is [System.Collections.IDictionary]) { foreach ($keyObject in $State.Keys) { $key = [string]$keyObject; if ($key -ieq 'revision' -or $key -ieq 'previous_sha256') { continue }; $newState[$key] = $State[$keyObject] } }
    else { foreach ($property in $State.PSObject.Properties) { if ($property.Name -ieq 'revision' -or $property.Name -ieq 'previous_sha256') { continue }; $newState[$property.Name] = $property.Value } }
    $newTaskId = Get-BFObjectProperty $newState 'task_id'
    if ($newTaskId -isnot [string] -or [string]::IsNullOrWhiteSpace($newTaskId)) { throw (New-BFError 'BF_INVALID' 'Revision state requires a non-empty task_id.') }
    if ($null -ne $latest -and $newTaskId -cne (Get-BFObjectProperty $latest 'task_id')) { throw (New-BFError 'BF_CONFLICT' 'task_id cannot change within a revision journal.') }
    $newState['revision'] = $actualRevision + 1
    $newState['previous_sha256'] = if ($null -eq $latest) { $null } else { Get-BFHash $latest }
    $revisionDirectory = [System.IO.Path]::Combine($fullDirectory, 'revisions')
    [void][System.IO.Directory]::CreateDirectory($revisionDirectory); [void](Assert-BFSafePath $revisionDirectory)
    $revisionPath = [System.IO.Path]::Combine($revisionDirectory, ('{0:D6}.json' -f ($actualRevision + 1)))
    [void](Write-BFJson -Path $revisionPath -Value $newState)
    $persisted = Read-BFJson $revisionPath
    [void](Write-BFJson -Path ([System.IO.Path]::Combine($fullDirectory, 'current.json')) -Value ([ordered]@{ revision = $actualRevision + 1; sha256 = Get-BFHash $persisted }) -Replace)
    return $persisted
}
