# Build (if needed) and start the gofer HTTP server.
#
# Override via env:
#   ADDR                 listen address (default 0.0.0.0:8765)
#   GOFER_TOKEN   bearer token; read by the server via config token_env.
#                        Prefer this over a CLI flag so the token never lands in
#                        process args / shell history.
#   TOKEN                fallback inline token; only passed as --token when set.
#   CONFIG               path to a bridge config file (optional)
$ErrorActionPreference = "Stop"

Set-Location (Join-Path $PSScriptRoot "..")

$addr   = if ($env:ADDR) { $env:ADDR } else { "0.0.0.0:8765" }
$config = $env:CONFIG

$serveArgs = @("serve", "--addr", $addr)
# Prefer GOFER_TOKEN (the default token_env): the server reads it from the
# environment. Only fall back to --token TOKEN when GOFER_TOKEN is unset.
if (-not $env:GOFER_TOKEN -and $env:TOKEN) {
    $serveArgs += @("--token", $env:TOKEN)
}
if ($config) {
    $serveArgs += @("--config", $config)
}

# Use the built binary if present, else build then run.
$binary = Join-Path "dist" "gofer.exe"
if (Test-Path $binary) {
    & $binary @serveArgs
} else {
    & go build -o $binary ./cmd/gofer
    & $binary @serveArgs
}
