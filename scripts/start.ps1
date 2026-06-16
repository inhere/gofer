# Start the agent-bridge HTTP server.
# Override via env: ADDR, TOKEN, CONFIG.
$ErrorActionPreference = "Stop"

Set-Location (Join-Path $PSScriptRoot "..")

$addr   = if ($env:ADDR)   { $env:ADDR }   else { "0.0.0.0:8765" }
$token  = if ($env:TOKEN)  { $env:TOKEN }  else { "dev-token" }
$config = $env:CONFIG

$serveArgs = @("serve", "--addr", $addr, "--token", $token)
if ($config) {
    $serveArgs += @("--config", $config)
}

$binary = Join-Path "dist" "agent-bridge.exe"
if (Test-Path $binary) {
    & $binary @serveArgs
} else {
    & go run ./cmd/agent-bridge @serveArgs
}
