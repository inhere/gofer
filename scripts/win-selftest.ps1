#requires -Version 5.1
# Acceptance test for win-supervisor.ps1 + win-selfupdate.ps1 (Windows self-update).
# Runs an EXTRA gofer on a test port under an isolated dir + isolated GOFER_CONFIG_DIR.
# NEVER touches a live server: every process action is filtered by ExecutablePath
# under $TestRoot, and the test server uses its own config dir + port. Self-cleaning.
#
# Run:  pwsh -NoProfile -File scripts/win-selftest.ps1
param(
    [string]$LiveExe = (Get-Command gofer -ErrorAction Stop).Source,
    [string]$ScriptsDir = $PSScriptRoot,
    [string]$TestRoot = (Join-Path $PSScriptRoot '..\tmp\win-selftest'),
    [int]$Port = 9099
)
$ErrorActionPreference = 'Stop'
# Normalize away any '..' so derived exe paths match a launched process's
# ExecutablePath (Windows reports it normalized) for the -eq filter below.
$TestRoot = [System.IO.Path]::GetFullPath($TestRoot)
$health = "http://127.0.0.1:$Port/health"
$bin    = Join-Path $TestRoot 'bin'
$repo   = Join-Path $TestRoot 'repo'
$exe    = Join-Path $bin 'gofer.exe'
$oldExe = Join-Path $bin 'gofer.old.exe'
$sup    = Join-Path $ScriptsDir 'win-supervisor.ps1'
$upd    = Join-Path $ScriptsDir 'win-selfupdate.ps1'
$pass = 0; $fail = 0
function Ok($m){ Write-Host "  PASS: $m"; $script:pass++ }
function No($m){ Write-Host "  FAIL: $m"; $script:fail++ }
function TestGofer { Get-CimInstance Win32_Process -Filter "Name='gofer.exe'" | Where-Object { $_.ExecutablePath -eq $exe } }
function WaitHealth([int]$sec=20){ for($i=0;$i -lt $sec;$i++){ try{ if((Invoke-WebRequest -UseBasicParsing $health -TimeoutSec 2).StatusCode -eq 200){return $true} }catch{}; Start-Sleep 1 }; return $false }
function WaitDown([int]$sec=15){ for($i=0;$i -lt $sec;$i++){ try{ Invoke-WebRequest -UseBasicParsing $health -TimeoutSec 2 | Out-Null }catch{ return $true }; Start-Sleep 1 }; return $false }

$supProc = $null
try {
    # ---- setup: isolated dirs + config + a copy of the live exe ----
    if (Test-Path $TestRoot) { Remove-Item $TestRoot -Recurse -Force }
    $cfg = Join-Path $TestRoot 'cfg'
    New-Item -ItemType Directory -Force -Path $bin, $repo, $cfg | Out-Null
    Copy-Item $LiveExe $exe -Force
    Copy-Item $LiveExe (Join-Path $repo 'gofer-new.exe') -Force   # simulate a fresh build output
    Write-Host "setup: TestRoot=$TestRoot port=$Port liveExe=$LiveExe"

    # ---- start supervisor (background) ----
    # A launcher wraps the call so (a) the [string[]] serve args are a literal array
    # (robust vs cross-process arg binding + '--' tokens) and (b) an ISOLATED
    # GOFER_CONFIG_DIR keeps the test server off any live server's storage/DB.
    $launcher = Join-Path $TestRoot 'run-sup.ps1'
    @"
`$env:GOFER_CONFIG_DIR = '$cfg'
& '$sup' -ExeDir '$bin' -WorkDir '$bin' -FastFailThreshold 3 -ServeArgs @('s','--addr','127.0.0.1:$Port','--no-web','--allow-empty-token')
"@ | Set-Content -Path $launcher -Encoding utf8
    $supProc = Start-Process pwsh -ArgumentList @('-NoProfile','-File',$launcher) -PassThru -WindowStyle Hidden
    Write-Host "supervisor pid=$($supProc.Id) cfg=$cfg"

    Write-Host "`n[A] supervisor brings server up + crash-recovery"
    if (WaitHealth) { Ok "health up on :$Port" } else { No "server never came healthy"; throw "abort" }
    $pid1 = (TestGofer).ProcessId
    Ok "test gofer running pid=$pid1"
    Stop-Process -Id $pid1 -Force                    # simulate a crash
    Start-Sleep 4
    if (WaitHealth) { Ok "health recovered after crash" } else { No "no recovery after crash" }
    $pid2 = (TestGofer).ProcessId
    if ($pid2 -and $pid2 -ne $pid1) { Ok "restarted with new pid ($pid1 -> $pid2)" } else { No "pid did not change ($pid1 -> $pid2)" }

    Write-Host "`n[B] self-update swap (rename-replace) + supervisor picks up new exe"
    & pwsh -NoProfile -File $upd -RepoDir $repo -ExeDir $bin -SkipBuild -SkipGuard | Out-Null
    if (Test-Path $oldExe) { Ok "gofer.old.exe created (rollback point)" } else { No "no gofer.old.exe" }
    if (-not (Test-Path (Join-Path $repo 'gofer-new.exe'))) { Ok "gofer-new.exe moved out of repo" } else { No "gofer-new.exe still in repo" }
    if (Test-Path $exe) { Ok "gofer.exe present (swapped)" } else { No "gofer.exe missing after swap" }
    $pidB0 = (TestGofer).ProcessId
    Stop-Process -Id $pidB0 -Force                   # the kill step the update job would perform
    Start-Sleep 4
    if (WaitHealth) { Ok "health up after swap+restart" } else { No "no health after swap" }
    $pidB1 = (TestGofer).ProcessId
    if ($pidB1 -and $pidB1 -ne $pidB0) { Ok "new exe running (pid $pidB0 -> $pidB1)" } else { No "pid unchanged after swap" }

    Write-Host "`n[C] watchdog: broken exe -> rollback to gofer.old.exe"
    # gofer.old.exe currently = a known-good server exe (from [B]). Stage a broken exe
    # via RENAME (a running exe cannot be overwritten but CAN be renamed) BEFORE the
    # kill -- avoids racing the supervisor's ~2s relaunch.
    Rename-Item $exe (Join-Path $bin 'gofer.good2.exe')                       # frees the gofer.exe path
    Set-Content -Path $exe -Value 'not a real exe' -Encoding ascii -Force     # broken -> fast-fail
    $pidC0 = (TestGofer).ProcessId
    Stop-Process -Id $pidC0 -Force
    if (WaitDown 10) { Ok "server down while supervisor churns broken exe" } else { No "server stayed up unexpectedly" }
    # threshold=3 fast fails * (~launch + 2s sleep) -> allow a generous window
    if (WaitHealth 40) { Ok "watchdog rolled back -> health recovered" } else { No "watchdog did not recover" }
    $log = Join-Path $bin 'win-supervisor.log'
    if ((Test-Path $log) -and (Select-String -Path $log -Pattern 'WATCHDOG.*rolled back' -Quiet)) { Ok "supervisor log shows rollback" } else { No "no rollback line in supervisor log" }

    Write-Host "`n[D] guard: running self-update NOT under gofer must be refused (F2)"
    # This pwsh's parent is the harness (not gofer.exe) -> guard(F2) must fire before stage1.
    $out = (& pwsh -NoProfile -File $upd -RepoDir $repo -ExeDir $bin -SkipBuild 2>&1 | Out-String)
    if ($out -match 'guard\(F2\)') { Ok "guard rejected non-gofer parent (F2)" } else { No "guard did not fire; out='$($out.Trim())'" }
}
finally {
    Write-Host "`n---- cleanup (only TestRoot-scoped procs) ----"
    # 1) Kill the supervisor first so it stops respawning the test server.
    if ($supProc) { Stop-Process -Id $supProc.Id -Force -ErrorAction SilentlyContinue }
    Start-Sleep 1
    # 2) Kill whatever now listens on the TEST port (reliable even if the CIM name
    #    query flakes) -- this is the orphaned test gofer, scoped strictly by port.
    foreach ($c in (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue)) {
        Stop-Process -Id $c.OwningProcess -Force -ErrorAction SilentlyContinue
    }
    # 3) Backup: any gofer.exe whose image is under TestRoot.
    foreach ($p in (Get-CimInstance Win32_Process -Filter "Name='gofer.exe'" -ErrorAction SilentlyContinue | Where-Object { $_.ExecutablePath -like "$TestRoot*" })) {
        Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue
    }
    Start-Sleep 1
    # 4) Remove the dir with a couple of retries (exe handle release can lag the kill).
    for ($i = 0; $i -lt 5 -and (Test-Path $TestRoot); $i++) {
        Remove-Item $TestRoot -Recurse -Force -ErrorAction SilentlyContinue
        if (Test-Path $TestRoot) { Start-Sleep 1 }
    }
    if (Test-Path $TestRoot) { Write-Host "WARN: $TestRoot not fully removed" }
    Write-Host "`n==== RESULT: pass=$pass fail=$fail ===="
}
