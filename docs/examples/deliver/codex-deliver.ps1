# deliver_command wrapper for codex sessions (session relay path C).
# Usage (agent config): deliver_command: [<abs path to powershell.exe>, -NoProfile,
#   -ExecutionPolicy, Bypass, -File, <abs path to this file>, "{{session_id}}", "{{text}}"]
# Exit codes follow gofer's contract: 0 = queued to the session, 3 = no such session
# (gofer falls through to tmux / takeover), anything else = delivery failed.
# Note: a live codex TUI picks the queued message up at once; a session whose process
# has exited still accepts it (exit 0) and shows it on the next `codex resume`.
param(
    [Parameter(Mandatory)][string]$SessionId,
    [Parameter(Mandatory)][string]$Text
)
$out = & codex queue --thread $SessionId --message $Text 2>&1 | Out-String
$code = $LASTEXITCODE
if ($code -ne 0 -and $out -match 'no rollout found') {
    [Console]::Error.WriteLine($out.Trim())
    exit 3
}
if ($code -ne 0) { [Console]::Error.WriteLine($out.Trim()) }
exit $code
