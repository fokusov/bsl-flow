#Requires -Version 7.0
# Thin CLI wrapper (requirement 20): bsl-flow task <subcommand> [arguments...]
#
# Maps registry subcommands onto Invoke-BSLFlowTask.ps1 controller actions and
# forwards the remaining `-Parameter value` pairs; the controller actions
# remain the engine. Only `--project <path>` / `--project=<path>` is
# additionally translated to the controller's `-ProjectPath` parameter.
# Exit codes are propagated unchanged (0 success, 2 BF_INVALID,
# 11 BF_BLOCKED/BF_CONFLICT).
$ErrorActionPreference = 'Stop'
$actionMap = @{
    create    = 'Create'
    edit      = 'EditRegistry'
    list      = 'List'
    show      = 'Show'
    history   = 'History'
    overview  = 'Overview'
    archive   = 'ArchiveTask'
    unarchive = 'UnarchiveTask'
    activate  = 'Activate'
}
function Write-TUsage {
    [Console]::Error.WriteLine('usage: bsl-flow task <create|edit|list|show|history|overview|archive|unarchive|activate> [-Parameter value | --project <path>]...')
}
if ($args.Count -lt 2) {
    Write-TUsage
    exit 2
}
$namespace = [string]$args[0]
$subCommand = [string]$args[1]
if ($namespace -cne 'task') {
    [Console]::Error.WriteLine(("BF_INVALID: unknown command '{0}'; expected 'task'." -f $namespace))
    exit 2
}
if (-not $actionMap.ContainsKey($subCommand)) {
    [Console]::Error.WriteLine(("BF_INVALID: unknown task subcommand '{0}'." -f $subCommand))
    exit 2
}
$controller = Join-Path (Split-Path -Parent $PSScriptRoot) 'global\skills\1c-task\scripts\Invoke-BSLFlowTask.ps1'
$splat = @{ Action = [string]$actionMap[$subCommand] }
$index = 2
while ($index -lt $args.Count) {
    $argument = [string]$args[$index]
    if ($argument -ieq '--project' -or $argument -ieq '-project') {
        if ($index + 1 -ge $args.Count) {
            [Console]::Error.WriteLine('BF_INVALID: --project requires a path value.')
            exit 2
        }
        $splat['ProjectPath'] = $args[$index + 1]
        $index += 2
        continue
    }
    if ($argument -ilike '--project=*') {
        $splat['ProjectPath'] = $argument.Substring('--project='.Length)
        $index++
        continue
    }
    if ($argument.StartsWith('-')) {
        if ($index + 1 -ge $args.Count) {
            [Console]::Error.WriteLine(("BF_INVALID: parameter '{0}' requires a value." -f $argument))
            exit 2
        }
        $splat[$argument.TrimStart('-')] = $args[$index + 1]
        $index += 2
        continue
    }
    [Console]::Error.WriteLine(("BF_INVALID: unexpected argument '{0}'; parameters must be passed as '-Parameter value'." -f $argument))
    exit 2
}
& $controller @splat
exit $LASTEXITCODE
