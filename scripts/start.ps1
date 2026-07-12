#requires -Version 5.1
<#
.SYNOPSIS
  Run the gofer HTTP server as a Windows service via nssm (out-of-process
  supervisor: auto-restart on crash, and the restarter the self-update needs).

.DESCRIPTION
  Quick start from the project dir. gofer.exe is expected under <repo>\serve-run\
  (build it there first, e.g.  go build -o serve-run\gofer.exe .\cmd\gofer ).
  nssm.exe sits next to it (serve-run\nssm.exe).

  nssm.exe launches gofer.exe as its child and restarts it on exit — so it is the
  "supervisor" for the rename-replace self-update (win-selfupdate.ps1). Because
  gofer's parent is now nssm (not win-supervisor.ps1), self-update must pass
  -SupervisorMarker 'nssm' (see NOTES).

  Actions (default = up):
    up       install-or-update the service, then start/restart it
    stop     stop the service
    restart  restart the service
    remove   stop + uninstall the service (binary/logs kept)
    status   show service state + effective nssm config
    logs     tail the stdout/stderr logs

.NOTES
  * Service ops (up/stop/restart/remove) need an ELEVATED (Administrator) shell.
  * The service runs as LocalSystem and sees NONE of your shell env, so point it at
    your gofer config dir:  -ConfigDir 'D:/work/inhere/config/win-env/gofer'  (or set
    $env:GOFER_CONFIG_DIR). It injects GOFER_CONFIG_DIR so gofer finds config.yaml +
    loads that dir's .env (GOFER_TOKEN). Without it gofer refuses to start (no token).
  * The service runs with AppDirectory = <repo>, so the RELATIVE `--web-dir ./web/dist`
    (and any relative config lookup) resolves against the repo root — no absolute
    paths in AppParameters, so a repo path with spaces stays safe.
  * Self-update under nssm (kill → nssm relaunches the swapped exe):
      gofer job run -a exec --runner local -- `
        pwsh -NoProfile -File scripts\win-selfupdate.ps1 `
          -RepoDir '<RepoDir>' -ExeDir '<repo>\serve-run' -SupervisorMarker 'nssm'
#>
param(
    [ValidateSet('up', 'stop', 'restart', 'remove', 'status', 'logs')]
    [string]$Action = 'up',
    # Windows service name.
    [string]$ServiceName = 'gofer',
    # Listen address as --addr (overrides config server.addr). Empty = let the
    # config's server.addr drive the port (don't force one).
    [string]$Addr = '',
    # Path to a gofer config file, passed as --config (optional).
    [string]$Config = '',
    # gofer config DIRECTORY, injected into the service env as GOFER_CONFIG_DIR.
    # The service runs as LocalSystem and does NOT see your shell's env, so a
    # user-level config (config.yaml + .env with GOFER_TOKEN) is invisible unless
    # pointed at here. Falls back to $env:GOFER_CONFIG_DIR. Example:
    #   -ConfigDir 'D:/work/inhere/config/win-env/gofer'
    [string]$ConfigDir = '',
    # Boot behaviour: -Auto = start at boot (SERVICE_AUTO_START); default = manual.
    [switch]$Auto,
    # Bearer token injected into the service env as GOFER_TOKEN (read via token_env).
    # Falls back to $env:GOFER_TOKEN. Leave empty if the config carries it / token is
    # disabled. NOTE: stored in the service registry (admin-readable).
    [string]$Token = ''
)

$ErrorActionPreference = 'Stop'

# --- resolve project paths from THIS script's location (cwd-independent) ---
$Repo   = Split-Path -Parent $PSScriptRoot          # <...>\tools\gofer
$ExeDir = Join-Path $Repo 'serve-run'
$Exe    = Join-Path $ExeDir 'gofer.exe'
$Nssm   = Join-Path $ExeDir 'nssm.exe'
$OutLog = Join-Path $ExeDir 'gofer.out.log'
$ErrLog = Join-Path $ExeDir 'gofer.err.log'

function Test-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    (New-Object Security.Principal.WindowsPrincipal($id)).IsInRole(
        [Security.Principal.WindowsBuiltinRole]::Administrator)
}
function Assert-Admin {
    if (-not (Test-Admin)) {
        throw "Action '$Action' modifies a Windows service and needs an elevated shell. " +
              "Re-open PowerShell as Administrator, then re-run:  pwsh -File scripts\start.ps1 -Action $Action"
    }
}
function Assert-Nssm {
    if (-not (Test-Path $Nssm)) {
        throw "nssm.exe not found at $Nssm. Download nssm (https://nssm.cc) and drop win64\nssm.exe there."
    }
}
function Test-ServiceExists { $null -ne (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) }

switch ($Action) {
    'up' {
        Assert-Nssm; Assert-Admin
        if (-not (Test-Path $Exe)) {
            throw "gofer.exe not found at $Exe. Build it first, e.g.:  go build -o serve-run\gofer.exe .\cmd\gofer"
        }
        # Relative --web-dir resolves against AppDirectory (=$Repo) at runtime, so no
        # absolute path lands in AppParameters (avoids the PowerShell->nssm quoting trap).
        $params = 'serve --web-dir ./web/dist'
        if ($Addr)   { $params += " --addr $Addr" }
        if ($Config) { $params += " --config $Config" }

        if (-not (Test-ServiceExists)) {
            Write-Host "installing service '$ServiceName' -> $Exe"
            & $Nssm install $ServiceName $Exe | Out-Null
        } else {
            Write-Host "service '$ServiceName' exists -> updating config"
            & $Nssm set $ServiceName Application $Exe | Out-Null
        }
        # Set config every run so edits (addr / web-dir / token) take effect on restart.
        & $Nssm set $ServiceName AppDirectory $Repo      | Out-Null   # relative config resolves here
        & $Nssm set $ServiceName AppParameters $params   | Out-Null
        & $Nssm set $ServiceName AppStdout $OutLog       | Out-Null
        & $Nssm set $ServiceName AppStderr $ErrLog       | Out-Null
        & $Nssm set $ServiceName AppExit Default Restart | Out-Null   # relaunch on any exit (incl. self-update kill)
        & $Nssm set $ServiceName AppRestartDelay 2000    | Out-Null   # ~2s, mirrors the pwsh supervisor
        & $Nssm set $ServiceName Start ($(if ($Auto) { 'SERVICE_AUTO_START' } else { 'SERVICE_DEMAND_START' })) | Out-Null

        # Service env (LocalSystem sees none of your shell's env): point at the config
        # dir (config.yaml + .env → GOFER_TOKEN) and optionally an explicit token.
        $envEntries = @()
        $cfgDir = if ($ConfigDir) { $ConfigDir } elseif ($env:GOFER_CONFIG_DIR) { $env:GOFER_CONFIG_DIR } else { '' }
        if ($cfgDir) { $envEntries += "GOFER_CONFIG_DIR=$cfgDir" }
        $tok = if ($Token) { $Token } elseif ($env:GOFER_TOKEN) { $env:GOFER_TOKEN } else { '' }
        if ($tok) { $envEntries += "GOFER_TOKEN=$tok" }
        if ($envEntries.Count -gt 0) { & $Nssm set $ServiceName AppEnvironmentExtra @envEntries | Out-Null }
        else { & $Nssm reset $ServiceName AppEnvironmentExtra 2>$null | Out-Null }
        if (-not $cfgDir) {
            Write-Warning "no -ConfigDir / GOFER_CONFIG_DIR set: the service (LocalSystem) may not find a config -> gofer refuses to start without a token."
        }

        # (Re)start cleanly (stop covers a paused/throttled state), then verify.
        & $Nssm stop  $ServiceName 2>$null | Out-Null
        & $Nssm start $ServiceName 2>$null | Out-Null
        Start-Sleep -Seconds 2
        $svc = Get-Service -Name $ServiceName
        if ($svc.Status -eq 'Running') {
            Write-Host "service '$ServiceName' RUNNING: gofer.exe $params"
        } else {
            Write-Warning "service '$ServiceName' is $($svc.Status), NOT Running — gofer likely failed to start. Check the logs:"
            Write-Warning "  pwsh -File scripts\start.ps1 -Action logs"
        }
        Write-Host "  logs: $OutLog / $ErrLog"
    }
    'stop'    { Assert-Nssm; Assert-Admin; & $Nssm stop    $ServiceName; Write-Host "stopped '$ServiceName'" }
    'restart' { Assert-Nssm; Assert-Admin; & $Nssm restart $ServiceName; Write-Host "restarted '$ServiceName'" }
    'remove'  {
        Assert-Nssm; Assert-Admin
        if (Test-ServiceExists) {
            & $Nssm stop $ServiceName 2>$null | Out-Null
            & $Nssm remove $ServiceName confirm | Out-Null
            Write-Host "removed service '$ServiceName' (gofer.exe / logs kept)"
        } else { Write-Host "service '$ServiceName' not installed" }
    }
    'status'  {
        Assert-Nssm
        if (Test-ServiceExists) {
            Get-Service -Name $ServiceName | Format-Table -AutoSize
            Write-Host "AppParameters: $(& $Nssm get $ServiceName AppParameters)"
            Write-Host "AppDirectory : $(& $Nssm get $ServiceName AppDirectory)"
            Write-Host "Start        : $(& $Nssm get $ServiceName Start)"
        } else { Write-Host "service '$ServiceName' not installed" }
    }
    'logs'    {
        if (Test-Path $OutLog) { Write-Host "== stdout =="; Get-Content $OutLog -Tail 30 }
        if (Test-Path $ErrLog) { Write-Host "== stderr =="; Get-Content $ErrLog -Tail 30 }
    }
}
