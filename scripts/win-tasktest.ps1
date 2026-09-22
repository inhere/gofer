#requires -Version 5.1
# Isolated acceptance test for the LOGON SCHEDULED TASK deployment:
#   scripts/start.ps1 (up/stop/restart/remove) + scripts/win-supervisor.ps1.
# Registers a task under a RANDOM name whose exe/config live under $TestRoot, on test
# ports, and unregisters + kills + deletes everything in `finally`.
# NEVER touches a live server: every process action is filtered by ExecutablePath
# under $TestRoot, the service named 'gofer' is only read, and the live ports are
# never used. Self-cleaning.
#
# Run:  pwsh -NoProfile -File scripts\win-tasktest.ps1 -LiveExe <repo>\dist\gofer.exe
param(
    [string]$LiveExe = (Get-Command gofer -ErrorAction Stop).Source,
    [string]$ScriptsDir = $PSScriptRoot,
    [string]$TestRoot = (Join-Path $PSScriptRoot '..\tmp\win-tasktest'),
    [int]$Port = 9098,
    [int]$DPort = 9097
)
$ErrorActionPreference = 'Stop'
$TestRoot = [System.IO.Path]::GetFullPath($TestRoot)

# Scrub anything that could aim a child gofer at a LIVE server (this test must be
# safe to run from a gofer job, where these point at the real one). Every gofer
# invocation below either passes -ConfigDir or sets GOFER_CONFIG_DIR explicitly.
foreach ($v in 'GOFER_CONFIG_DIR', 'GOFER_SERVER_ADDR', 'GOFER_SERVER_TOKEN', 'GOFER_TOKEN',
    'GOFER_JOB_ID', 'GOFER_CWD', 'GOFER_RESULT_DIR') {
    Remove-Item "Env:$v" -ErrorAction SilentlyContinue
}

$health = "http://127.0.0.1:$Port/health"
$dhealth = "http://127.0.0.1:$DPort/health"
$bin = Join-Path $TestRoot 'bin'
$exe = Join-Path $bin 'gofer.exe'
$cfg = Join-Path $TestRoot 'cfg'
$cfg2 = Join-Path $TestRoot 'cfg2'
$start = Join-Path $ScriptsDir 'start.ps1'
$task = "gofer-tasktest-$(Get-Random -Minimum 1000 -Maximum 9999)"
$pass = 0; $fail = 0
function Ok($m) { Write-Host "  PASS: $m"; $script:pass++ }
function No($m) { Write-Host "  FAIL: $m"; $script:fail++ }
function Info($m) { Write-Host "  INFO: $m" }
function TestGofer { Get-CimInstance Win32_Process -Filter "Name='gofer.exe'" -ErrorAction SilentlyContinue | Where-Object { $_.ExecutablePath -eq $exe } }
function WaitHealth([string]$url, [int]$sec = 20) { for ($i = 0; $i -lt $sec; $i++) { try { if ((Invoke-WebRequest -UseBasicParsing $url -TimeoutSec 2).StatusCode -eq 200) { return $true } } catch { }; Start-Sleep 1 }; return $false }
function WaitDown([string]$url, [int]$sec = 15) { for ($i = 0; $i -lt $sec; $i++) { try { Invoke-WebRequest -UseBasicParsing $url -TimeoutSec 2 | Out-Null } catch { return $true }; Start-Sleep 1 }; return $false }
function TryHealth([string]$url) { try { (Invoke-WebRequest -UseBasicParsing $url -TimeoutSec 3).StatusCode } catch { 0 } }
function Stop-Test { & $start -Action stop -TaskName $task -ExeDir $bin -ConfigDir $cfg -Addr "127.0.0.1:$Port" 2>&1 | Out-Null }
# Run a gofer subcommand in its own pwsh with an explicit config dir and capture all
# output as text (byte-level redirect keeps gofer's UTF-8 intact).
# NOTE: deliberately NEVER `Start-Process -Wait` -- since PowerShell 7.4 it also waits
# for the whole process TREE, and the detached child of `serve -d` would keep it alive
# forever. `-PassThru` + WaitForExit waits for the launched process only; -Spawn adds a
# cmd.exe-level file redirect, because a detached child also inherits the launcher's
# stdout PIPE (Start-Process -RedirectStandardOutput would wait on that EOF instead).
function Invoke-Gofer([string]$cfgDir, [string]$argline, [switch]$Spawn) {
    $out = Join-Path $TestRoot 'cmd.out.txt'
    $err = Join-Path $TestRoot 'cmd.err.txt'
    Remove-Item $out, $err -Force -ErrorAction SilentlyContinue
    $cmd = "`$env:GOFER_CONFIG_DIR='$cfgDir'; & '$exe' $argline"
    if ($Spawn) {
        $p = Start-Process cmd -ArgumentList @('/c', "pwsh -NoProfile -Command `"$cmd`" > `"$out`" 2>&1") -PassThru -WindowStyle Hidden
        if (-not $p.WaitForExit(60000)) { No "serve -d launcher did not exit within 60s" }
        Start-Sleep -Milliseconds 300
        $t = if (Test-Path $out) { Get-Content $out -Raw -ErrorAction SilentlyContinue } else { '' }
        return [pscustomobject]@{ Exit = $p.ExitCode; Text = [string]$t }
    }
    $p = Start-Process pwsh -ArgumentList @('-NoProfile', '-Command', $cmd) -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput $out -RedirectStandardError $err
    if (-not $p.WaitForExit(60000)) { No "launcher did not exit within 60s" }
    Start-Sleep -Milliseconds 200
    $p.WaitForExit()   # 2nd call: also drains the redirected streams
    $text = @()
    foreach ($f in @($out, $err)) { if (Test-Path $f) { $text += (Get-Content $f -Raw) } }
    return [pscustomobject]@{ Exit = $p.ExitCode; Text = ($text -join "`n") }
}
function Show-Log([string]$f, [string]$pattern, [string]$label) {
    if (-not (Test-Path $f)) { Info "${label}: $f not found"; return }
    $hit = Select-String -Path $f -Pattern $pattern | Select-Object -Last 1
    if ($hit) { Write-Host "  ${label}: $($hit.Line)" } else { Info "${label}: no /$pattern/ in $f" }
}

Write-Host "setup: task=$task TestRoot=$TestRoot port=$Port dport=$DPort liveExe=$LiveExe"
$consoleSession = (Get-Process explorer -ErrorAction SilentlyContinue | Select-Object -First 1).SessionId
$sessionSrc = 'explorer.exe'
if (-not $consoleSession) {
    $consoleSession = [System.Diagnostics.Process]::GetCurrentProcess().SessionId
    $sessionSrc = 'THIS process (explorer.exe not found)'
}
Write-Host "console session = $consoleSession (from $sessionSrc)"

try {
    # ---- 1) setup ----
    Write-Host "`n[1] setup"
    if (Test-Path $TestRoot) { Remove-Item $TestRoot -Recurse -Force }
    New-Item -ItemType Directory -Force -Path $bin, $cfg, $cfg2 | Out-Null
    Copy-Item $LiveExe $exe -Force
    Set-Content -Path (Join-Path $cfg 'config.yaml') -Encoding utf8 -Value @"
server:
  addr: 127.0.0.1:$Port
  token: tasktest-token
"@
    $default = Get-Content (Join-Path $cfg 'config.yaml') -Raw
    Set-Content -Path (Join-Path $cfg2 'config.yaml') -Encoding utf8 -Value ($default -replace [regex]::Escape("127.0.0.1:$Port"), "127.0.0.1:$DPort")
    if ((Test-Path $exe) -and (Test-Path (Join-Path $cfg 'config.yaml')) -and (Test-Path (Join-Path $cfg2 'config.yaml'))) { Ok "prepared bin/cfg/cfg2" } else { No "setup incomplete" }

    # ---- 2) up ----
    Write-Host "`n[2] start.ps1 -Action up (task mode, interactive session)"
    if (Get-Service -Name 'gofer' -ErrorAction SilentlyContinue) {
        # The service guard must fire on this host (the old nssm service is installed).
        $guard = & pwsh -NoProfile -File $start -Action up -TaskName $task -ExeDir $bin -ConfigDir $cfg -NoWeb -Addr "127.0.0.1:$Port" 2>&1 | Out-String
        if ($LASTEXITCODE -eq 3 -and $guard -match "service named 'gofer' exists" -and $guard -match 'remove gofer confirm') {
            Ok "up refuses while service 'gofer' exists (exit=3 + nssm migration commands)"
        } else {
            No "up did not refuse on the installed 'gofer' service (exit=$LASTEXITCODE): $($guard.Trim())"
        }
    } else {
        Info "no service named 'gofer' on this host -> the up guard was not exercised"
    }
    $up = & pwsh -NoProfile -File $start -Action up -TaskName $task -ExeDir $bin -ConfigDir $cfg -NoWeb -Addr "127.0.0.1:$Port" -AllowServiceConflict 2>&1 | Out-String
    Write-Host $up.Trim()
    if (WaitHealth $health 20) { Ok "/health 200 on :$Port after up" } else {
        No "no /health after up"
        $ti = Get-ScheduledTaskInfo -TaskName $task -ErrorAction SilentlyContinue
        if ($ti) { No ("task info: state=$((Get-ScheduledTask -TaskName $task).State) lastRun=$($ti.LastRunTime) lastResult=0x{0:X8} nextRun=$($ti.NextRunTime)" -f $ti.LastTaskResult) }
        else { No "task '$task' not registered at all" }
        Info "supervisor log:"; Get-Content (Join-Path $bin 'win-supervisor.log') -ErrorAction SilentlyContinue | ForEach-Object { Write-Host "    $_" }
    }
    $procs = @(TestGofer)
    if ($procs.Count -gt 0) {
        Ok "test gofer running pid=$($procs[0].ProcessId) SessionId=$($procs[0].SessionId)"
        if ($procs[0].SessionId -eq $consoleSession) { Ok "SessionId matches the console session ($consoleSession) -> running ON the desktop" }
        else { No "SessionId=$($procs[0].SessionId) != console session $consoleSession (still session 0?)" }
    } else { No "no gofer.exe from $exe is running" }
    Show-Log (Join-Path $cfg 'run\serve.log') 'server.ready' 'server.ready'
    if (Select-String -Path (Join-Path $cfg 'run\serve.log') -Pattern 'server.ready' -Quiet -ErrorAction SilentlyContinue) {
        $ready = (Select-String -Path (Join-Path $cfg 'run\serve.log') -Pattern 'server.ready' | Select-Object -Last 1).Line
        if ($ready -match '"interactive":(true|false)') {
            Info "interactive=$($Matches[1]) (expected true; false is reported as-is for a session-0 launcher, human call)"
            Ok "server.ready carries the interactive field"
        } else { No "server.ready has no interactive field: $ready" }
    } else { No "no server.ready line in $cfg\run\serve.log" }

    # ---- 3) stop ----
    Write-Host "`n[3] start.ps1 -Action stop"
    Stop-Test
    if (WaitDown $health 15) { Ok "/health unreachable after stop" } else { No "/health still up after stop" }
    Show-Log (Join-Path $cfg 'run\serve.log') 'server.shutdown' 'server.shutdown'
    if (Select-String -Path (Join-Path $cfg 'run\serve.log') -Pattern 'server.shutdown' -Quiet -ErrorAction SilentlyContinue) { Ok "log has server.shutdown (graceful)" } else { No "no server.shutdown line" }
    if (-not (Test-Path (Join-Path $cfg 'run\serve.pid'))) { Ok "pidfile removed" } else { No "pidfile still present" }
    if (Test-Path (Join-Path $bin 'gofer.stop')) { Ok "stop marker present" } else { No "no gofer.stop marker" }
    $st = (Get-ScheduledTask -TaskName $task -ErrorAction SilentlyContinue).State
    if ($st -eq 'Ready') { Ok "task state=Ready" } else { No "task state='$st' (expected Ready)" }
    Start-Sleep 5
    if (@(TestGofer).Count -eq 0) { Ok "still no gofer 5s later (watchdog obeyed the marker)" } else { No "watchdog relaunched gofer after stop" }

    # ---- 4) restart ----
    Write-Host "`n[4] start.ps1 -Action restart"
    & pwsh -NoProfile -File $start -Action restart -TaskName $task -ExeDir $bin -ConfigDir $cfg -Addr "127.0.0.1:$Port" 2>&1 | ForEach-Object { Write-Host "    $_" }
    if (WaitHealth $health 20) { Ok "/health 200 after restart" } else { No "no /health after restart" }
    if (-not (Test-Path (Join-Path $bin 'gofer.stop'))) { Ok "stop marker cleared by restart" } else { No "stop marker still present after restart" }

    # ---- 5) crash -> watchdog ----
    Write-Host "`n[5] kill gofer -> the supervisor must bring it back"
    $p5 = @(TestGofer)
    if ($p5.Count -gt 0) {
        Stop-Process -Id $p5[0].ProcessId -Force
        $ok = $false; $used = 0
        for ($i = 0; $i -lt 15; $i++) { if ((TryHealth $health) -eq 200) { $ok = $true; $used = $i; break }; Start-Sleep 1 }
        if ($ok) { Ok "health recovered $used`s after the kill (watchdog relaunch)" } else { No "no health recovery within 15s of the kill" }
    } else { No "no gofer process to kill (step 2/4 failed)" }

    # ---- 6) serve -d (W1 path, standalone) ----
    Write-Host "`n[6] gofer serve -d on its own config dir (pidfile + named stop event)"
    $r1 = Invoke-Gofer $cfg2 "serve -d --no-web --addr 127.0.0.1:$DPort" -Spawn
    Write-Host "  serve -d #1 exit=$($r1.Exit): $($r1.Text.Trim())"
    Info "launcher pwsh for #1 has exited (its detached child is the -d server)"
    if (WaitHealth $dhealth 20) { Ok "detached serve answers /health on :$DPort (launcher gone)" } else { No "no /health after serve -d" }
    $r2 = Invoke-Gofer $cfg2 "serve -d --no-web --addr 127.0.0.1:$DPort"
    Write-Host "  serve -d #2 exit=$($r2.Exit): $($r2.Text.Trim())"
    if ($r2.Text -match 'already running') { Ok "second serve -d reports 'already running'" } else { No "second serve -d did not report 'already running': $($r2.Text.Trim())" }
    $r3 = Invoke-Gofer $cfg2 "serve stop"
    Write-Host "  serve stop exit=$($r3.Exit): $($r3.Text.Trim())"
    if ($r3.Text -match '已停止') { Ok "serve stop reports 已停止" } else { No "serve stop output lacks 已停止" }
    if (WaitDown $dhealth 15) { Ok "/health unreachable after serve stop" } else { No "/health still up after serve stop" }
    Show-Log (Join-Path $cfg2 'run\serve.log') 'server.shutdown' 'serve -d server.shutdown'
    if (Select-String -Path (Join-Path $cfg2 'run\serve.log') -Pattern 'server.shutdown' -Quiet -ErrorAction SilentlyContinue) { Ok "detached serve logged server.shutdown" } else { No "detached serve has no server.shutdown line" }

    # ---- 7) upgrade (in-place swap) + status/logs ----
    Write-Host "`n[7] start.ps1 -Action upgrade / status / logs"
    $upg = & pwsh -NoProfile -File $start -Action upgrade -TaskName $task -ExeDir $bin -ConfigDir $cfg -Addr "127.0.0.1:$Port" 2>&1 | Out-String
    Write-Host (($upg.Trim() -split "`n" | Select-Object -Last 10) -join "`n")
    if ($upg -match 'swapped:') { Ok "upgrade built + swapped the exe" } else { No "upgrade did not report a swap" }
    if (Test-Path (Join-Path $bin 'gofer.exe.prev')) { Ok "previous exe kept as gofer.exe.prev" } else { No "no gofer.exe.prev rollback point" }
    if (WaitHealth $health 25) { Ok "/health 200 after upgrade" } else { No "no /health after upgrade" }
    $st = & pwsh -NoProfile -File $start -Action status -TaskName $task -ExeDir $bin -ConfigDir $cfg -Addr "127.0.0.1:$Port" 2>&1 | Out-String
    Write-Host (($st.Trim() -split "`n" | Select-Object -First 6) -join "`n")
    if ($st -match 'state=' -and $st -match 'gofer : pid=' -and $st -match 'marker :') { Ok "status prints task state + gofer pid + marker" } else { No "status output incomplete: $($st.Trim())" }
    $lg = & pwsh -NoProfile -File $start -Action logs -TaskName $task -ExeDir $bin -ConfigDir $cfg 2>&1 | Out-String
    if ($lg -match 'win-supervisor.log' -and $lg -match 'serve\.log') { Ok "logs tails the supervisor + serve logs" } else { No "logs output incomplete: $($lg.Trim())" }

    # ---- 8) remove ----
    Write-Host "`n[8] start.ps1 -Action remove"
    & pwsh -NoProfile -File $start -Action remove -TaskName $task -ExeDir $bin -ConfigDir $cfg -Addr "127.0.0.1:$Port" 2>&1 | ForEach-Object { Write-Host "    $_" }
    if (-not (Get-ScheduledTask -TaskName $task -ErrorAction SilentlyContinue)) { Ok "task unregistered" } else { No "task '$task' still registered" }
}
catch {
    No "unexpected error: $($_.Exception.Message)"
}
finally {
    Write-Host "`n---- cleanup (only TestRoot-scoped task/procs) ----"
    try { & pwsh -NoProfile -File $start -Action remove -TaskName $task -ExeDir $bin -ConfigDir $cfg 2>&1 | Out-Null } catch { }
    if (Get-ScheduledTask -TaskName $task -ErrorAction SilentlyContinue) {
        Unregister-ScheduledTask -TaskName $task -Confirm:$false -ErrorAction SilentlyContinue
        Write-Host "WARN: task '$task' needed a forced unregister"
    }
    Start-Sleep 1
    foreach ($c in (Get-NetTCPConnection -LocalPort $Port, $DPort -State Listen -ErrorAction SilentlyContinue)) {
        Stop-Process -Id $c.OwningProcess -Force -ErrorAction SilentlyContinue
    }
    foreach ($p in (Get-CimInstance Win32_Process -Filter "Name='gofer.exe'" -ErrorAction SilentlyContinue | Where-Object { $_.ExecutablePath -like "$TestRoot*" })) {
        Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue
    }
    Start-Sleep 1
    for ($i = 0; $i -lt 5 -and (Test-Path $TestRoot); $i++) {
        Remove-Item $TestRoot -Recurse -Force -ErrorAction SilentlyContinue
        if (Test-Path $TestRoot) { Start-Sleep 1 }
    }
    if (Test-Path $TestRoot) { Write-Host "WARN: $TestRoot not fully removed" }
    Write-Host "`n==== RESULT: pass=$pass fail=$fail ===="
    if ($fail -gt 0) { exit 1 }
    exit 0
}
