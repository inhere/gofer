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
    [string]$LogPath = (Join-Path $ExeDir 'win-supervisor.log')
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

$fails = 0
while ($true) {
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
