#requires -Version 5.1
<#
.SYNOPSIS
  Rebuild the gofer binary and hand the restart to the supervisor loop (Windows).

.DESCRIPTION
  Run this AS A DIRECT CHILD of the running gofer server (i.e. via agent=exec, not
  codex/shell wrappers): the restart step kills "my parent", which only equals the
  gofer server on that exact topology.

  Two stages:
    1 (safe)  git pull + go build a NEW exe in -RepoDir; the running exe is untouched.
    2 (swap)  rename-replace the live exe in -ExeDir, then kill gofer by its precise
              pid. The supervisor (win-supervisor.ps1, gofer's parent) relaunches the
              new exe ~2s later.

  -RepoDir and -ExeDir are frequently DIFFERENT (build tree vs installed binary),
  hence two paths.

.NOTES
  No deployment specifics baked in -- see the Windows server self-update runbook.
#>
param(
    # Build tree: git pull + go build run here.
    [Parameter(Mandatory)][string]$RepoDir,
    # Directory of the LIVE binary the supervisor launches (rename-replace target).
    [Parameter(Mandatory)][string]$ExeDir,
    [string]$ExeName = 'gofer.exe',
    [string]$BuildTarget = './cmd/gofer',
    # Optional post-restart health probe (best-effort; only reaches the log, see below).
    [string]$HealthUrl,
    # gofer's parent (the supervisor) command line must contain this marker (guard F4).
    [string]$SupervisorMarker = 'win-supervisor',
    # Test hook: reuse an existing <RepoDir>/gofer-new.exe instead of pull+build.
    [switch]$SkipBuild,
    # Test hook: bypass the F2/F4 topology guard and DO NOT kill (caller drives the kill).
    [switch]$SkipGuard,
    [string]$LogPath
)

# F6: any step failing aborts BEFORE the destructive stage. Native-command failures
# do NOT honor this (pwsh only stops on cmdlet errors), so exit codes are checked
# explicitly below.
$ErrorActionPreference = 'Stop'
if (-not $LogPath) { $LogPath = Join-Path $ExeDir 'win-selfupdate.log' }
function Log([string]$m) {
    $l = ('[{0}] {1}' -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $m)
    Write-Host $l
    try { Add-Content -Path $LogPath -Value $l -Encoding utf8 } catch { }
}

$exe = Join-Path $ExeDir  $ExeName
$old = Join-Path $ExeDir  'gofer.old.exe'
$new = Join-Path $RepoDir 'gofer-new.exe'

# ---- F2/F4 guard: chain must be supervisor(pwsh) -> gofer -> THIS(pwsh) ----
$parentPid = $null
if (-not $SkipGuard) {
    $me     = Get-CimInstance Win32_Process -Filter "ProcessId=$PID"
    $parent = Get-CimInstance Win32_Process -Filter "ProcessId=$($me.ParentProcessId)"
    if (-not $parent -or $parent.Name -ne $ExeName) {
        throw "guard(F2): parent is '$($parent.Name)', expected '$ExeName'. Run self-update as a DIRECT child of gofer (agent=exec)."
    }
    $grand = Get-CimInstance Win32_Process -Filter "ProcessId=$($parent.ParentProcessId)"
    if (-not $grand -or ($grand.CommandLine -notmatch [regex]::Escape($SupervisorMarker))) {
        throw "guard(F4): gofer's parent is not the supervisor ('$($grand.Name)'). A bare 'gofer serve' has no restarter -- aborting before any kill."
    }
    $parentPid = [int]$parent.ProcessId
    Log "guard ok: self=$PID gofer=$parentPid supervisor=$($grand.ProcessId)"
}

# ---- stage 1 (safe; never touches the running exe) ----
Push-Location $RepoDir
try {
    if (-not $SkipBuild) {
        Log "stage1: git pull ($RepoDir)"
        & git pull
        if ($LASTEXITCODE -ne 0) { throw "git pull failed (exit $LASTEXITCODE)" }
        Log "stage1: go build -o gofer-new.exe $BuildTarget"
        & go build -o gofer-new.exe $BuildTarget
        if ($LASTEXITCODE -ne 0) { throw "go build failed (exit $LASTEXITCODE)" }
    }
    else {
        Log "stage1: -SkipBuild, using existing $new"
    }
}
finally { Pop-Location }
if (-not (Test-Path $new)) { throw "stage1: $new not produced" }
$ver = (& $new -V) 2>&1
if ($LASTEXITCODE -ne 0) { throw "stage1: new exe '-V' failed (exit $LASTEXITCODE): $ver" }
Log "stage1: new exe ok -> $ver"

# ---- stage 2 (rename-replace in ExeDir + kill gofer; supervisor restarts) ----
# R1: build output lives in RepoDir; the live exe to swap sits in ExeDir.
if (Test-Path $old) { Remove-Item $old -Force }   # KeepOld=1 (plan §10; multi-copy = future)
Log "stage2: rename $ExeName -> gofer.old.exe (rename is allowed while running)"
Rename-Item $exe $old
try {
    Log "stage2: move gofer-new.exe -> $exe"
    Move-Item $new $exe -Force
}
catch {
    # Never leave a hole: put the old exe back under the live name so the supervisor
    # keeps running the previous binary instead of finding nothing.
    Log "stage2: move failed, restoring gofer.old.exe -> ${ExeName}: $($_.Exception.Message)"
    Rename-Item $old $exe
    throw
}

if ($parentPid) {
    Log "stage2: kill gofer pid=$parentPid (supervisor relaunches new exe in ~2s)"
    Stop-Process -Id $parentPid -Force
    # On Windows this job is orphaned-but-alive after the kill, but gofer's stdout
    # forwarding is gone so further output never reaches the client. The poll below
    # only lands in the log; authoritative confirmation is out-of-band (runbook).
    if ($HealthUrl) {
        Start-Sleep -Seconds 3
        for ($i = 1; $i -le 10; $i++) {
            try {
                $r = Invoke-WebRequest -UseBasicParsing $HealthUrl -TimeoutSec 3
                if ($r.StatusCode -eq 200) { Log "post-restart health 200 (attempt $i)"; break }
            }
            catch { Log "post-restart health wait ${i}: $($_.Exception.Message)" }
            Start-Sleep -Seconds 2
        }
    }
}
else {
    Log "stage2: -SkipGuard set -> swap done, NOT killing (caller drives the kill)"
}
Log "self-update done"
