# Build (if needed) and start the agent-bridge HTTP server.
#
# Override via env:
#   ADDR                 listen address (default 0.0.0.0:8765)
#   AGENT_BRIDGE_TOKEN   bearer token; read by the server via config token_env.
#                        Prefer this over a CLI flag so the token never lands in
#                        process args / shell history.
#   TOKEN                fallback inline token; only passed as --token when set.
#   CONFIG               path to a bridge config file (optional)
$ErrorActionPreference = "Stop"

Set-Location (Join-Path $PSScriptRoot "..")

$addr   = if ($env:ADDR) { $env:ADDR } else { "0.0.0.0:8765" }
$config = $env:CONFIG

$serveArgs = @("serve", "--addr", $addr)
# Prefer AGENT_BRIDGE_TOKEN (the default token_env): the server reads it from the
# environment. Only fall back to --token TOKEN when AGENT_BRIDGE_TOKEN is unset.
if (-not $env:AGENT_BRIDGE_TOKEN -and $env:TOKEN) {
    $serveArgs += @("--token", $env:TOKEN)
}
if ($config) {
    $serveArgs += @("--config", $config)
}

# Use the built binary if present, else build then run.
$binary = Join-Path "dist" "agent-bridge.exe"
if (Test-Path $binary) {
    & $binary @serveArgs
} else {
    & go build -o $binary ./cmd/agent-bridge
    & $binary @serveArgs
}
