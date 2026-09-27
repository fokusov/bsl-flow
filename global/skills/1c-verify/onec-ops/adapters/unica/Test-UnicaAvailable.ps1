#Requires -Version 7.0
# unica is an MCP server the calling agent invokes directly, not a local binary this dispatcher
# process can probe. This adapter's entry script only ever produces an instruction payload (or
# relays an -ImportResult the agent already obtained), so it is always "available" - it is the
# agent's own MCP tool access, not this detect script, that actually gates execution.
param([string]$ProjectPath, [string]$AdapterDir)
'true'
