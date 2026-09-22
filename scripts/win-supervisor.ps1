#requires -Version 5.1
<#
.SYNOPSIS
  Supervisor loop that keeps a gofer server running and enables in-place self-update.

.DESCRIPTION
  This process is gofer's PARENT and therefore lives OUTSIDE gofer's process tree.
  So an update job that kills gofer to swap the binary does not take the supervisor
  down with it -- the loop just relaunches the (now new) binary. It also gives
  crash auto-recovery for a plain foreground deployment.

  Pairs with win-selfupdate.ps1, whose flow is: git pull + go build a new exe ->
  rename-replace it into -ExeDir -> kill gofer by its precise pid -> this loop
  brings the new exe back up.

  Watchdog: a launch that exits within -FastFailSeconds counts as a "fast failure"
  (e.g. a bad new binary that cannot even start). After -FastFailThreshold
  consecutive fast failures it rolls back gofer.old.exe -> gofer.exe and continues,
  so a broken update cannot wedge the box.

.NOTES
  Parameterized on purpose -- no deployment specifics baked in. See the Windows
  server self-update runbook for concrete invocation.

  Two knobs added for the logon-scheduled-task deployment (`scripts/start.ps1`):
    -StopMarker  presence makes the loop exit before the next launch (used by `stop`
                 so the watchdog does not resurrect a deliberately stopped server).
    -EnvExtra    'KEY=VALUE' entries injected into this process, hence inherited by
                 gofer (how the task passes GOFER_CONFIG_DIR without user-level env).
#>
param(
    # Directory holding gofer.exe (and the gofer.old.exe rollback point).
    [Parameter(Mandatory)][string]$ExeDir,
    # Binary name inside -ExeDir.
    [string]$ExeName = 'gofer.exe',
    # Args passed to gofer, e.g. @('serve','--addr','0.0.0.0:8765','--web-dir','./web/dist').
    [string[]]$ServeArgs = @('serve'),
    # Working directory to launch from. Relative serve args (e.g. --web-dir ./web/dist)
    # resolve against this, so it MUST match the original launch cwd.
    [string]$WorkDir = $ExeDir,
    # An exit sooner than this many seconds after start counts as a fast failure.
    [int]$FastFailSeconds = 5,
    # Consecutive fast failures that trigger an automatic rollback to gofer.old.exe.
    [int]$FastFailThreshold = 3,
    # Supervisor log file (append). Defaults under -ExeDir.
    [string]$LogPath = (Join-Path $ExeDir 'win-supervisor.log'),
    # STOP MARKER: if this file exists, the loop exits (exit 0) instead of relaunching
    # gofer -- checked right before every launch, so after `start.ps1 -Action stop`
    # drops the marker (and gofer exits on its own) the watchdog stops respawning it.
    # Removed again by `up`/`restart`/`upgrade` before the next start.
    [string]$StopMarker = (Join-Path $ExeDir 'gofer.stop'),
    # Extra environment variables for gofer, as 'KEY=VALUE' entries. Set on THIS
    # process before the first launch, so the child inherits them (the logon task
    # passes GOFER_CONFIG_DIR this way instead of relying on user-level env).
    [string[]]$EnvExtra = @()
)

# The loop MUST survive a failed launch: a terminating error here would kill the
# supervisor itself and defeat both crash-recovery and the watchdog (plan F3).
$ErrorActionPreference = 'Continue'

$exe = Join-Path $ExeDir $ExeName
$old = Join-Path $ExeDir 'gofer.old.exe'

function Write-Sup([string]$msg) {
    $line = ('[{0}] {1}' -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $msg)
    Write-Host $line
    try { Add-Content -Path $LogPath -Value $line -Encoding utf8 } catch { }
}

Write-Sup "supervisor start: exe=$exe args=[$($ServeArgs -join ' ')] cwd=$WorkDir"

# Inject -EnvExtra into this process so gofer (our child) inherits it. Values are
# NEVER logged (GOFER_TOKEN may travel this way); only the keys.
if ($EnvExtra.Count -gt 0) {
    $keys = @()
    foreach ($kv in $EnvExtra) {
        if (-not $kv) { continue }
        $parts = $kv -split '=', 2
        if ($parts.Count -ne 2 -or -not $parts[0]) {
            Write-Sup "WARN: ignoring malformed -EnvExtra entry (expected KEY=VALUE)"
            continue
        }
        $k = $parts[0]; $v = $parts[1]
        Set-Item -Path "Env:$k" -Value $v
        $keys += $k
    }
    if ($keys.Count -gt 0) { Write-Sup "env injected: $($keys -join ', ')" }
}
Write-Sup "stop marker: $StopMarker (exists=$(Test-Path $StopMarker))"

$fails = 0
while ($true) {
    # Checked BEFORE every launch: `stop` drops this marker, gofer exits, and the
    # next iteration (<= 2s away, the sleep at the loop end) bows out instead of
    # relaunching. A self-update kill never sets the marker, so it still respawns.
    if (Test-Path $StopMarker) {
        Write-Sup "stop marker present ($StopMarker) -> supervisor exiting"
        exit 0
    }

    if (-not (Test-Path $exe)) {
        # Missing binary (e.g. a rename-replace that aborted mid-way): count it so
        # the watchdog can roll back rather than spin forever on a hole.
        Write-Sup "ERROR: $exe not found"
        $fails++
    }
    else {
        $started = Get-Date
        try {
            Push-Location $WorkDir
            & $exe @ServeArgs
            $code = $LASTEXITCODE
            Pop-Location
            $dur = ((Get-Date) - $started).TotalSeconds
            Write-Sup ("gofer exited code={0} uptime={1:N1}s" -f $code, $dur)
            # A long-lived instance that was killed for an update is NOT a failure.
            if ($dur -lt $FastFailSeconds) { $fails++ } else { $fails = 0 }
        }
        catch {
            Pop-Location -ErrorAction SilentlyContinue
            Write-Sup "ERROR launching gofer: $($_.Exception.Message)"
            $fails++
        }
    }

    if ($fails -ge $FastFailThreshold) {
        if (Test-Path $old) {
            try {
                Copy-Item $old $exe -Force
                Write-Sup "WATCHDOG: $fails consecutive fast failures -> rolled back gofer.old.exe -> $ExeName"
            }
            catch {
                Write-Sup "WATCHDOG: rollback copy failed: $($_.Exception.Message)"
            }
            $fails = 0
        }
        else {
            Write-Sup "WATCHDOG: $fails fast failures but no gofer.old.exe to roll back (manual fix needed)"
            Start-Sleep -Seconds 5   # avoid a tight spin when there is nothing to roll back
        }
    }

    Start-Sleep -Seconds 2
}
