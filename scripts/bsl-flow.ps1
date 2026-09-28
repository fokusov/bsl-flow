#Requires -Version 7.0
# Proxy for use from a repository clone: the real wrapper lives inside the
# installed skill (global/skills/1c-task/scripts/bsl-flow.ps1) so it keeps
# working after the installer copies only global/skills/* to ~/.agents/skills.
$ErrorActionPreference = 'Stop'
$target = Join-Path (Split-Path -Parent $PSScriptRoot) 'global\skills\1c-task\scripts\bsl-flow.ps1'
& $target @args
exit $LASTEXITCODE
