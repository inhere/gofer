#requires -Version 5.1
<#
.SYNOPSIS
  Run the gofer HTTP server as a LOGON SCHEDULED TASK inside the user's interactive
  desktop session (watchdog: scripts/win-supervisor.ps1).

.DESCRIPTION
  Why a scheduled task and NOT a Windows service: a service -- nssm or sc -- always
  runs in SESSION 0, which is isolated from the logged-on desktop. Jobs submitted
  with `--runner local` inherit that session, so anything GUI (DTools / CODESYS
  automation, `capture click`, window screenshots) fails there. A logon task runs in
  the user's interactive session (session 1+), so local jobs own the desktop.

  gofer.exe is expected under <repo>\serve-run\ (build it there first, e.g.
  `go build -o serve-run\gofer.exe .\cmd\gofer`). The task action starts
  win-supervisor.ps1 (hidden, normally through `conhost.exe --headless`), which is
  gofer's PARENT: crash auto-restart, fast-fail rollback to gofer.old.exe and the
  restart half of the rename-replace self-update all keep working unchanged.

  Actions (default = up):
    up       register-or-update the task (trigger AtLogOn for -User), clear the stop
             marker, start it, wait for /health (<=20s), print version / pid / session
    upgrade  `make build` (add -Web for `make web`) FIRST while the server keeps
             running -- a failed build changes nothing -- then stop -> keep the old
             exe as gofer.exe.prev -> swap in dist\gofer.exe -> start -> wait /health
    stop     drop the stop marker (so the watchdog stays down), ask gofer to stop
             gracefully via `gofer serve stop` (pidfile + named stop event), wait
             <=15s for task+process to leave; only then hard-stop and warn
    restart  stop -> clear the marker -> start -> wait for /health
    remove   stop + unregister the task (binary / logs kept)
    status   task state, last run result, action command line, gofer pid/SessionId/
             StartTime, /health, stop marker
    logs     tail win-supervisor.log and the server log

.NOTES
  * `up` PREREQUISITES: no Windows service named `gofer` (it would fight this task
    for the same port -- the script refuses and prints the migration commands), a
    config dir (see below) and gofer.exe in -ExeDir.
  * -ConfigDir is only needed the FIRST time (or to change it). Resolution order:
    -ConfigDir -> $env:GOFER_CONFIG_DIR -> the registered TASK's action
    (`-EnvExtra GOFER_CONFIG_DIR=…`; after the first `up` that IS the source of
    truth, so stop/restart/status/logs/upgrade -- and a re-run of `up` -- need no
    -ConfigDir) -> gofer's default ~\.config\gofer, but only when that holds a
    config.yaml. `status` prints "config dir: <dir> (from param|env|task|default)".
    It travels into the task as GOFER_CONFIG_DIR via the supervisor's -EnvExtra,
    because the task inherits the user's REGISTRY environment, not your shell's.
    With none of the four available, `up` prints two one-time remedies: pass
    -ConfigDir once, or set the user-level env with
    [Environment]::SetEnvironmentVariable('GOFER_CONFIG_DIR','<dir>','User') --
    new shells AND the logon task inherit that, and the gofer CLI finds the config
    on its own. `stop` never needs one (stop marker + hard-stop fallback).
    The token is expected in <ConfigDir>\.env (`GOFER_TOKEN=...`) or via the
    config's token_env; no token is written into the task definition.
  * No admin needed: the task belongs to the current user (LogonType Interactive).
    Exception: -Elevated (RunLevel Highest) requires an elevated shell.
  * Power loss / reboot: a logon task needs a LOGON. Enable auto-logon
    (netplwiz / Autologon) if the box must come back unattended -- an operational
    decision, out of scope here.
  * Self-update chain unchanged: gofer's parent is still win-supervisor.ps1, so
    win-selfupdate.ps1's default -SupervisorMarker 'win-supervisor' matches -- no
    override needed.
  * -Elevated and UIPI: a Limited (default) gofer cannot drive windows owned by an
    elevated process, and an elevated gofer cannot drive Limited ones. Match the
    integrity level of the apps you automate.
#>
param(
    [ValidateSet('up', 'upgrade', 'stop', 'restart', 'remove', 'status', 'logs')]
    [string]$Action = 'up',
    # upgrade only: also run `make web` (pnpm build + embed) before the Go build, so
    # the swapped binary carries the current web console. Off by default: the web
    # build is slow and most upgrades are Go-only.
    [switch]$Web,
    # Scheduled task name. Change it for a second, isolated instance.
    [string]$TaskName = 'gofer-serve',
    # gofer config DIRECTORY, injected into the task env as GOFER_CONFIG_DIR (which
    # is how gofer finds config.yaml + loads that dir's .env with GOFER_TOKEN).
    # Resolution order: -ConfigDir -> $env:GOFER_CONFIG_DIR -> the registered task's
    # -EnvExtra GOFER_CONFIG_DIR (the task is the source of truth after the first
    # `up`, so re-runs need no -ConfigDir) -> gofer's default ~\.config\gofer (only
    # when that holds a config.yaml). Example:
    #   -ConfigDir 'D:/work/inhere/config/win-env/gofer'
    [string]$ConfigDir = '',
    # Listen address as --addr (overrides config server.addr). Empty = let the
    # config's server.addr drive the port. Also the address used for /health probes.
    [string]$Addr = '',
    # Path to a gofer config file, passed as --config (optional). Must not contain
    # spaces (the task passes serve args as a comma-separated list).
    [string]$Config = '',
    # Serve the API without the web console (`--no-web`) instead of the default
    # `--web-dir ./web/dist` (relative to the repo, the task's working directory).
    [switch]$NoWeb,
    # Directory holding the RUNNING gofer.exe (also the gofer.stop marker and
    # win-supervisor.log). Defaults to <repo>\serve-run. Override for an isolated
    # test instance.
    [string]$ExeDir = '',
    # Account the task runs as, and whose logon triggers it. Default: the console
    # user (Win32_ComputerSystem.UserName), falling back to `whoami`. NOT
    # $env:USERNAME -- from a session-0 job that can be SYSTEM.
    [string]$User = '',
    # Register the task with RunLevel Highest (= always elevated) instead of
    # Limited. Requires an elevated shell to register, and UIPI then blocks driving
    # non-elevated windows.
    [switch]$Elevated,
    # Skip the "a Windows service named 'gofer' exists" refusal in `up`. ONLY for an
    # isolated instance (own -TaskName / -ExeDir / -ConfigDir / addr, e.g. the
    # acceptance test win-tasktest.ps1): the guard otherwise stops you from starting a
    # second instance that fights the installed service for the same port.
    [switch]$AllowServiceConflict
)

$ErrorActionPreference = 'Stop'

# --- resolve project paths from THIS script's location (cwd-independent) ---
$Repo = Split-Path -Parent $PSScriptRoot          # <...>\tools\gofer
if (-not $ExeDir) { $ExeDir = Join-Path $Repo 'serve-run' }
$ExeDir = [System.IO.Path]::GetFullPath($ExeDir)
$Exe    = Join-Path $ExeDir 'gofer.exe'
$Built  = Join-Path $Repo 'dist\gofer.exe'        # `make build` output, swapped in by upgrade
$Sup    = Join-Path $PSScriptRoot 'win-supervisor.ps1'
$StopMarker = Join-Path $ExeDir 'gofer.stop'
$SupLog = Join-Path $ExeDir 'win-supervisor.log'

# $ConfigDir / $ServeLog / $ServeOut are resolved further down (Resolve-ConfigDir):
# the registered task is one of the sources, so resolution needs the helpers below.

# ---------------------------------------------------------------- helpers

function Test-Admin {
    $id = [Security.Principal.WindowsIdentity]::GetCurrent()
    (New-Object Security.Principal.WindowsPrincipal($id)).IsInRole(
        [Security.Principal.WindowsBuiltinRole]::Administrator)
}

function Get-TaskUser {
    if ($User) { return $User }
    $u = (Get-CimInstance Win32_ComputerSystem -ErrorAction SilentlyContinue).UserName
    if (-not $u) { $u = (& whoami) }
    return $u
}

function Get-TaskState {
    $t = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($t) { return [string]$t.State }
    return ''
}

function Assert-ConfigDir([string]$why) {
    if (-not $ConfigDir) {
        Write-Host (Get-ConfigDirHint)
        throw "'$why' needs the gofer config directory (how to give it once: see the hint above)."
    }
    if (-not (Test-Path $ConfigDir)) { throw "config dir not found: $ConfigDir" }
}

function Get-GoferProc {
    Get-CimInstance Win32_Process -Filter "Name='gofer.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.ExecutablePath -eq $Exe }
}

# server.addr out of <ConfigDir>\config.yaml (scans the `server:` block; handles a
# `server: {addr: ...}` inline mapping too). Empty when absent/unparsable.
function Get-ConfigAddr([string]$dir) {
    if (-not $dir) { return '' }
    $f = Join-Path $dir 'config.yaml'
    if (-not (Test-Path $f)) { return '' }
    $inServer = $false; $serverIndent = -1
    foreach ($line in (Get-Content $f)) {
        $t = $line.TrimEnd()
        if ($t -match '^(\s*)server\s*:\s*(.*)$') {
            $rest = $Matches[2].Trim()
            if ($rest.StartsWith('#')) { $rest = '' }
            if ($rest.StartsWith('{')) {
                if ($rest -match '\baddr\s*:\s*["'']?([^"''\s,}]+)') { return $Matches[1] }
                return ''
            }
            $inServer = $true; $serverIndent = $Matches[1].Length; continue
        }
        if (-not $inServer) { continue }
        if ($t.Trim() -eq '' -or $t.TrimStart().StartsWith('#')) { continue }
        $ind = $t.Length - $t.TrimStart().Length
        if ($ind -le $serverIndent) { $inServer = $false; continue }
        if ($t -match '^\s*addr\s*:\s*["'']?([^"''#\s]+)') { return $Matches[1] }
    }
    return ''
}

# '' when there is nothing to probe (no -Addr and no server.addr to read) -> the
# callers then only require the gofer process to be alive.
function Get-HealthUrl {
    $a = $Addr
    if (-not $a) { $a = Get-ConfigAddr $ConfigDir }
    if (-not $a) { return '' }
    $a = $a -replace '^\s*0\.0\.0\.0:', '127.0.0.1:'
    $a = $a -replace '^\s*\[::\]:', '127.0.0.1:'
    if ($a -notmatch '^[a-zA-Z][a-zA-Z0-9+.-]*://') { $a = "http://$a" }
    return ($a.TrimEnd('/') + '/health')
}

function Quote-Arg([string]$s) {
    if ($s -match '[\s"]') { return '"' + $s + '"' }
    return $s
}

# conhost.exe --headless exists on Windows 10 1809 (build 17763) and later; without
# it the fallback (plain pwsh -WindowStyle Hidden) flashes a window at logon.
function Test-ConhostHeadless {
    if (-not (Test-Path (Join-Path $env:SystemRoot 'System32\conhost.exe'))) { return $false }
    return ([System.Environment]::OSVersion.Version.Build -ge 17763)
}

function Get-PwshPath {
    $c = Get-Command pwsh -ErrorAction SilentlyContinue
    if ($c) { return $c.Source }
    return (Join-Path $PSHOME 'powershell.exe')
}

function Get-ServeArgs {
    $a = @('serve')
    if ($NoWeb) { $a += '--no-web' } else { $a += @('--web-dir', './web/dist') }
    if ($Addr) { $a += @('--addr', $Addr) }
    if ($Config) { $a += @('--config', $Config) }
    return $a
}

# The scheduled action: [{Execute, Argument}] launching the supervisor, hidden.
function Get-TaskCommand {
    if ($Config -match '\s') {
        throw "-Config '$Config' contains spaces: the task passes serve args as a comma-separated list, which cannot carry them. Use a space-free path (or drop the file into -ConfigDir as config.yaml)."
    }
    $pwsh = Get-PwshPath
    $supArgs = @(
        '-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', (Quote-Arg $Sup),
        '-ExeDir', (Quote-Arg $ExeDir), '-WorkDir', (Quote-Arg $Repo),
        '-ServeArgs', ((Get-ServeArgs) -join ','),
        # The value is ALWAYS quoted so a config dir containing spaces stays one argv
        # element (and Resolve-ConfigDir reads the quoted form back; tasks registered
        # by older runs carry the unquoted form, which it parses too).
        '-EnvExtra', ('"GOFER_CONFIG_DIR=' + $ConfigDir + '"')
    ) -join ' '
    if (Test-ConhostHeadless) {
        # conhost --headless: the child gets a console (so Ctrl+C semantics stay
        # sane) but no window, and it survives the logon shell.
        $exe = Join-Path $env:SystemRoot 'System32\conhost.exe'
        return @{ Execute = $exe; Argument = "--headless $(Quote-Arg $pwsh) $supArgs" }
    }
    return @{ Execute = $pwsh; Argument = "-WindowStyle Hidden $supArgs" }
}

function New-GoferTask {
    $cmd = Get-TaskCommand
    $action = New-ScheduledTaskAction -Execute $cmd.Execute -Argument $cmd.Argument -WorkingDirectory $Repo
    $user = Get-TaskUser
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $user
    $level = if ($Elevated) { 'Highest' } else { 'Limited' }
    if ($Elevated -and -not (Test-Admin)) {
        throw "-Elevated (RunLevel Highest) needs an elevated shell to register a task for '$user'."
    }
    $principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel $level
    $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) `
        -RestartCount 99 -RestartInterval (New-TimeSpan -Minutes 1) `
        -MultipleInstances IgnoreNew -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -StartWhenAvailable
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
        -Principal $principal -Settings $settings -Force | Out-Null
    Write-Host "task '$TaskName' registered (user=$user logon=Interactive runlevel=$level)"
    Write-Host "  action: $($cmd.Execute) $($cmd.Argument)"
    if ($cmd.Execute -notlike '*conhost.exe') {
        Write-Warning "no 'conhost --headless' on this system (OS build $([System.Environment]::OSVersion.Version.Build)): the task uses pwsh -WindowStyle Hidden and may flash a window at logon."
    }
}

# Wait for the instance to answer /health (or just to be alive when there is no
# address to probe). Returns $true/$false; a missing health URL never fails.
function Wait-Started([int]$sec = 20) {
    $url = Get-HealthUrl
    for ($i = 0; $i -lt $sec; $i++) {
        if ($url) {
            try {
                if ((Invoke-WebRequest -UseBasicParsing $url -TimeoutSec 2).StatusCode -eq 200) { return $true }
            } catch { }
        } elseif (@(Get-GoferProc).Count -gt 0) {
            Start-Sleep 1
            return $true
        }
        Start-Sleep 1
    }
    return $false
}

function Wait-Stopped([int]$sec = 15) {
    $deadline = (Get-Date).AddSeconds($sec)
    while ((Get-Date) -lt $deadline) {
        if (@(Get-GoferProc).Count -eq 0 -and (Get-TaskState) -ne 'Running') { return $true }
        Start-Sleep -Milliseconds 500
    }
    return (@(Get-GoferProc).Count -eq 0 -and (Get-TaskState) -ne 'Running')
}

function Show-Instance {
    $url = Get-HealthUrl
    if ($url) {
        try {
            $r = Invoke-WebRequest -UseBasicParsing $url -TimeoutSec 3
            Write-Host "health : $($r.StatusCode) $url"
        } catch {
            Write-Warning "health : unreachable ($url)"
        }
    } else {
        Write-Host "health : skipped (no -Addr and no server.addr in $ConfigDir\config.yaml)"
    }
    $procs = @(Get-GoferProc)
    if ($procs.Count -eq 0) {
        Write-Warning "gofer : no gofer.exe running from $Exe"
    } else {
        foreach ($p in $procs) {
            Write-Host "gofer : pid=$($p.ProcessId) session=$($p.SessionId) started=$($p.CreationDate)"
        }
        if (Test-Path $Exe) {
            $ver = (& $Exe --version 2>&1 | Select-Object -First 1)
            Write-Host "version: $ver"
        }
    }
}

# Stop = marker first (so the watchdog bows out), then the graceful request through
# the pidfile + named stop event, then wait. Hard stop only on timeout.
function Invoke-StopTask {
    if (-not (Test-Path $StopMarker)) {
        New-Item -ItemType File -Path $StopMarker -Force | Out-Null
        Write-Host "stop marker set: $StopMarker (watchdog will not relaunch)"
    } else {
        Write-Host "stop marker already present: $StopMarker"
    }

    $procs = @(Get-GoferProc)
    if ($procs.Count -eq 0) {
        Write-Host "no gofer.exe running from $Exe"
    } elseif (-not $ConfigDir) {
        Write-Warning "no -ConfigDir / GOFER_CONFIG_DIR: cannot run 'gofer serve stop'; falling back to a hard stop."
    } else {
        Write-Host "asking gofer to stop (pid=$($procs.ProcessId -join ',')) ..."
        $saved = $env:GOFER_CONFIG_DIR
        try {
            $env:GOFER_CONFIG_DIR = $ConfigDir
            & $Exe serve stop
            if ($LASTEXITCODE -ne 0) { Write-Warning "'gofer serve stop' exited $LASTEXITCODE (see above)" }
        } finally { $env:GOFER_CONFIG_DIR = $saved }
    }

    if (Wait-Stopped 15) {
        Write-Host "stopped (task state='$(Get-TaskState)')"
        return
    }
    Write-Warning "still running after 15s -> hard stop"
    if (Get-TaskState) { Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue }
    foreach ($p in @(Get-GoferProc)) { Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue }
}

# Start = clear the marker, then (re)start the task and wait for health.
function Invoke-StartTask {
    if (Test-Path $StopMarker) {
        Remove-Item $StopMarker -Force
        Write-Host "stop marker cleared: $StopMarker"
    }
    if (-not (Get-TaskState)) { throw "task '$TaskName' is not registered; run:  pwsh -File scripts\start.ps1 -Action up" }
    Start-ScheduledTask -TaskName $TaskName
    Write-Host "task '$TaskName' started (state='$(Get-TaskState)')"
    if (Wait-Started 20) {
        Write-Host "up: /health OK"
    } else {
        Write-Warning "not healthy within 20s; check:  pwsh -File scripts\start.ps1 -Action logs"
    }
    Show-Instance
}

# ---------------------------------------------------------------- config dir
#
# The config dir comes from, in order:
#   ① -ConfigDir
#   ② $env:GOFER_CONFIG_DIR
#   ③ the REGISTERED TASK's action (`-EnvExtra GOFER_CONFIG_DIR=…`) -- after the
#     first `up` the task is the source of truth, so stop/restart/status/logs/
#     upgrade no longer need -ConfigDir
#   ④ gofer's own default ~\.config\gofer, only when that actually holds a
#     config.yaml (same dir the gofer CLI would fall back to)
# Resolve-ConfigDir sets $ConfigDir + $ConfigDirFrom (param/env/task/default/'').

# GOFER_CONFIG_DIR out of the task actions' command lines, e.g.
#   -EnvExtra "GOFER_CONFIG_DIR=D:\cfg"    quoted   (what this script writes)
#   -EnvExtra GOFER_CONFIG_DIR=D:\cfg      unquoted (tasks of older runs)
# The value runs to the closing quote (quoted) or to the first whitespace (unquoted).
function Get-TaskConfigDir {
    $t = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    if (-not $t) { return '' }
    foreach ($a in $t.Actions) {
        $argline = [string]$a.Arguments
        if (-not $argline) { continue }
        foreach ($m in [regex]::Matches($argline, '-EnvExtra\s+(?:"(?<q>[^"]*)"|(?<u>\S+))')) {
            $item = if ($m.Groups['q'].Success) { $m.Groups['q'].Value } else { $m.Groups['u'].Value }
            if ($item -like 'GOFER_CONFIG_DIR=*') { return $item.Substring('GOFER_CONFIG_DIR='.Length) }
        }
    }
    return ''
}

function Resolve-ConfigDir {
    $script:ConfigDirFrom = ''
    if ($ConfigDir) {
        $script:ConfigDirFrom = 'param'
    } elseif ($env:GOFER_CONFIG_DIR) {
        $script:ConfigDir = $env:GOFER_CONFIG_DIR
        $script:ConfigDirFrom = 'env'
    } else {
        $fromTask = Get-TaskConfigDir
        if ($fromTask) {
            $script:ConfigDir = $fromTask
            $script:ConfigDirFrom = 'task'
        } else {
            $dflt = if ($HOME) { Join-Path $HOME '.config\gofer' } else { '' }
            if ($dflt -and (Test-Path (Join-Path $dflt 'config.yaml'))) {
                $script:ConfigDir = $dflt
                $script:ConfigDirFrom = 'default'
            }
        }
    }
    if ($script:ConfigDir) { $script:ConfigDir = [System.IO.Path]::GetFullPath($script:ConfigDir) }
}

function Write-ConfigDirInfo {
    if ($ConfigDir) { Write-Host "config dir: $ConfigDir (from $ConfigDirFrom)" }
}

# The two one-time remedies when no config dir can be found anywhere. Printed (not
# thrown) so the commands stay copy-pasteable: PowerShell's error rendering re-wraps
# a long message.
function Get-ConfigDirHint {
    $dflt = if ($HOME) { Join-Path $HOME '.config\gofer' } else { "`$HOME\.config\gofer" }
    $taskState = if (Get-TaskState) { "no GOFER_CONFIG_DIR in its action" } else { 'not registered' }
    @(
        'no gofer config dir found. Checked:',
        "  -ConfigDir                     (not given)",
        "  `$env:GOFER_CONFIG_DIR          (not set)",
        "  task '$TaskName'   ($taskState)",
        "  $dflt   (no config.yaml)",
        "Give it once:      pwsh -File scripts\start.ps1 -Action $Action -ConfigDir '<dir>'",
        "Or set it once for this user -- new shells AND the logon task inherit it, and the gofer CLI then finds the config on its own:",
        "  [Environment]::SetEnvironmentVariable('GOFER_CONFIG_DIR','<dir>','User')"
    ) -join "`n"
}

Resolve-ConfigDir
$ServeLog = if ($ConfigDir) { Join-Path $ConfigDir 'run\serve.log' } else { '' }
$ServeOut = if ($ConfigDir) { Join-Path $ConfigDir 'run\serve.out.log' } else { '' }

# ---------------------------------------------------------------- actions

switch ($Action) {
    'up' {
        Assert-ConfigDir 'up'
        Write-ConfigDirInfo
        if (Get-Service -Name 'gofer' -ErrorAction SilentlyContinue) {
            if ($AllowServiceConflict) {
                Write-Warning "a Windows service named 'gofer' exists; -AllowServiceConflict given -> continuing (isolated instance only)."
            } else {
                Write-Host "ERROR: a Windows service named 'gofer' exists -- it would fight this task for the same port." -ForegroundColor Red
                Write-Host "Migrate in an ADMIN window first:"
                Write-Host "  `"$ExeDir\nssm.exe`" stop gofer; `"$ExeDir\nssm.exe`" remove gofer confirm"
                Write-Host "  (or:  sc.exe stop gofer; sc.exe delete gofer)"
                Write-Host "Then re-run:  pwsh -File scripts\start.ps1 -Action up -ConfigDir '$ConfigDir'"
                Write-Host "(-AllowServiceConflict skips this check for an isolated instance.)"
                exit 3
            }
        }
        if (-not (Test-Path $Exe)) {
            throw "gofer.exe not found at $Exe. Build it first, e.g.:  go build -o serve-run\gofer.exe .\cmd\gofer"
        }
        if (-not (Test-Path $Sup)) { throw "supervisor script not found at $Sup" }
        # Make it so: a running task is stopped first, so the settings just written
        # (addr / web-dir / config dir) actually take effect on the fresh start.
        if (Get-TaskState -eq 'Running' -or @(Get-GoferProc).Count -gt 0) { Invoke-StopTask }
        New-GoferTask
        Invoke-StartTask
    }
    'upgrade' {
        Assert-ConfigDir 'upgrade'
        if (-not (Test-Path $Exe)) {
            throw "gofer.exe not found at $Exe; run  pwsh -File scripts\start.ps1 -Action up  first."
        }
        if (-not (Get-Command make -ErrorAction SilentlyContinue)) {
            throw "make not found on PATH (Git Bash / MSYS make is expected). Build by hand instead:  go build -o serve-run\gofer.exe .\cmd\gofer"
        }
        # 1) Build while the old instance keeps serving. `make build` writes
        #    dist\gofer.exe, which nothing holds open, so this never touches the live
        #    binary and a compile error leaves the server exactly as it was.
        $targets = if ($Web) { 'web build' } else { 'build' }
        Write-Host "building ($targets) in $Repo ..."
        Push-Location $Repo
        try {
            & make $targets.Split(' ')
            if ($LASTEXITCODE -ne 0) { throw "make $targets failed (exit $LASTEXITCODE); server left untouched." }
        } finally { Pop-Location }
        if (-not (Test-Path $Built)) { throw "build succeeded but $Built is missing; check the Makefile DIST_DIR." }
        $newVer = (& $Built --version 2>&1 | Select-Object -First 1)
        $oldVer = if (Test-Path $Exe) { (& $Exe --version 2>&1 | Select-Object -First 1) } else { '(none)' }

        # 2) Swap: stop (releases the exe lock), copy, start. The previous exe is kept
        #    as gofer.exe.prev so a bad build can be rolled back by hand.
        Invoke-StopTask
        Copy-Item $Exe "$Exe.prev" -Force
        Copy-Item $Built $Exe -Force
        Write-Host "swapped: $oldVer  ->  $newVer"
        Invoke-StartTask
        Write-Host "version now: $(& $Exe --version 2>&1 | Select-Object -First 1)"
        Write-Host "Roll back if needed:  Copy-Item '$Exe.prev' '$Exe' -Force; pwsh -File scripts\start.ps1 -Action restart"
    }
    'stop' {
        # No Assert-ConfigDir: stop must still work without one (stop marker + hard
        # stop); Invoke-StopTask warns about the degraded path.
        Invoke-StopTask
    }
    'restart' {
        Assert-ConfigDir 'restart'
        Invoke-StopTask
        Invoke-StartTask
    }
    'remove' {
        Invoke-StopTask
        if (Get-TaskState) {
            Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
            Write-Host "unregistered task '$TaskName' (gofer.exe / logs kept)"
        } else {
            Write-Host "task '$TaskName' not registered"
        }
    }
    'status' {
        Assert-ConfigDir 'status'
        Write-ConfigDirInfo
        if (Get-TaskState) {
            Write-Host "task   : $TaskName  state=$(Get-TaskState)"
            $info = Get-ScheduledTaskInfo -TaskName $TaskName
            Write-Host ("info   : lastRun={0} lastResult=0x{1:X8} nextRun={2}" -f $info.LastRunTime, $info.LastTaskResult, $info.NextRunTime)
            foreach ($a in (Get-ScheduledTask -TaskName $TaskName).Actions) {
                # the CIM property is `Arguments` (plural); `Argument` is only the
                # New-ScheduledTaskAction parameter name and reads as empty here.
                Write-Host "action : $($a.Execute) $($a.Arguments)"
                Write-Host "workdir: $($a.WorkingDirectory)"
            }
            $pr = (Get-ScheduledTask -TaskName $TaskName).Principal
            Write-Host "principal: $($pr.UserId) logon=$($pr.LogonType) runlevel=$($pr.RunLevel)"
        } else {
            Write-Host "task   : $TaskName NOT registered"
        }
        Show-Instance
        Write-Host "marker : $(if (Test-Path $StopMarker) { "present ($StopMarker)" } else { 'absent' })"
    }
    'logs' {
        Assert-ConfigDir 'logs'
        foreach ($f in @($SupLog, $ServeLog, $ServeOut)) {
            if ($f -and (Test-Path $f)) { Write-Host "== $f (tail 30) =="; Get-Content $f -Tail 30 }
        }
        if (-not (Test-Path $SupLog) -and -not ($ServeLog -and (Test-Path $ServeLog))) {
            Write-Host "no logs yet ($SupLog / $ServeLog)"
        }
    }
}
