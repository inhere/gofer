<!-- template_id: design; template_version: 1.1.1 -->
# Windows 桌面会话常驻与 `-d` 后台模式设计（SVC-01）

> 状态：Approved 0.2 / 实施中（2026-09-22 人工批准；决策 4 改为**废弃 nssm 方式**，`start.ps1` 只保留计划任务模式）

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-09-22 | Claude | 初稿：nssm 服务跑在 session 0，`--runner local` 的 job 碰不到桌面（DTools/CODESYS/截图）。方案 = ① `internal/daemon` 补 Windows 实现（`serve -d` / `worker -d` / `stop` 优雅停）；② `start.ps1 -Mode task`：登录计划任务在**交互会话**里常驻 serve，复用 `win-supervisor.ps1` 做看门狗；③ runbook 选型表 |
| 0.2 | 2026-09-22 | Claude | 人工批准。用户决定**废弃 nssm 方式**：`start.ps1` 不再有 `-Mode`，只管计划任务；nssm 相关参数/动作/文档删除（G032），只留一段"从 nssm 迁移"。分期 W1（Go，`omp-acp`）→ W2（脚本 + 文档，omp） |

## 背景与目标

主机 gofer server 由 nssm 以 Windows 服务方式常驻（`scripts/start.ps1`）。服务一律运行在 **session 0**（与登录账号无关，`-Account '.\<user>'` 也不例外），与用户桌面（session 1+）隔离：`--runner local` 的 job 无法操作 GUI——DTools/CODESYS 自动化、离线模拟 `capture click`、任何需要窗口/剪贴板/`BitBlt` 的动作都失败（bd 记忆里的 `BitBlt Access denied` 即此）。现在的绕法是在桌面开一个前台窗口跑 `gofer worker`（`w-kzl-desktop`），要一直开着窗。

用户观察到 jcode 在主机上会拉起一个后台 `serve` 进程，退出 jcode 后它仍在——那是 `DETACHED_PROCESS` 方式 spawn 的子进程，gofer 在 Linux 上的 `serve -d` 就是同一机制，但 Windows 侧被 `internal/daemon/daemon_windows.go` 明确拒绝（`daemon mode (-d) not supported on windows; run as a service`）。

目标：

1. **`gofer serve -d` / `gofer worker -d` / `gofer serve stop` / `gofer worker stop` 在 Windows 可用**，行为与 Linux 一致（脱离终端、pidfile、优雅停）。
2. **server 常驻在用户桌面会话里**（登录后自动拉起、无窗口、崩溃自动重启、自更新链不变），使 `--runner local` 直接拥有桌面；nssm 方式废弃（0.2）。
3. 文档给出从 nssm 迁移的步骤与桌面会话的注意事项。

非目标：服务内用 `WTSQueryUserToken + CreateProcessAsUser` 把 job 投放到活动会话（等于重写一个 worker，gofer 已有 worker 抽象）；锁屏/RDP 断开状态下的 GUI 可用性（属 Windows 桌面策略，见「风险与限制」）。

## 已确认事实（代码 / 环境）

- `internal/daemon/daemon.go`：`Spawn` 用 `reexecDetached` 重新执行自身（`os.Args[1:]` + 环境哨兵 `GOFER_DAEMONIZED=1`），父进程写 pidfile 后退出；`daemon_unix.go` 用 `Setsid` + stdout/stderr 落 `run/<name>.out.log`；`PIDAlive` = `kill(pid,0)`；`Terminate` = SIGTERM。`daemon_windows.go` 三个函数全部返回 `errNotSupported`，`PIDAlive` 恒 false。
- `internal/commands/serve.go:88-100`：`-d` 时父进程 `daemon.Spawn`，子进程 `defer daemon.RemovePIDFile`；**前台运行不写 pidfile**。`worker.go:285-303` 同。`worker.go:244 runningWorkerIDs` 通过扫描 `run/worker-*.pid` + `PIDAlive` 发现本机 worker——前台起的 worker 因此不可见。
- `internal/commands/stop.go stopDaemon`：读 pidfile → `Terminate` → 轮询 `PIDAlive` 至多 12s；文案硬编码 "SIGTERM" / "kill -9"。
- 停止信号：serve 在 `internal/serve/serve.go:300` 用 `signal.NotifyContext(ctx, SIGINT, SIGTERM)`；worker 在 `internal/worker/serve.go:36` 用 `signal.Notify(sig, SIGINT, SIGTERM)`。Windows 上 Go 把控制台 Ctrl+C / CTRL_CLOSE 映射为这两个信号，但**无控制台的分离进程收不到任何信号**（`GenerateConsoleCtrlEvent` 需要共享控制台）。
- `scripts/start.ps1`：只有 nssm 一种模式；`upgrade` = `make build` → `nssm stop` → 覆盖 `serve-run\gofer.exe` → `nssm start`。`scripts/win-supervisor.ps1`：nssm 之前的看门狗循环（前台 `& $exe @ServeArgs`，快速失败 3 次回滚 `gofer.old.exe`），目前无"停止并退出循环"的开关；`win-selfupdate.ps1` 的 F4 守卫检查 gofer 祖父进程命令行含 `-SupervisorMarker`（默认 `win-supervisor`）。
- `config.ConfigDir()` 只认 `GOFER_CONFIG_DIR` 环境变量（否则 `~/.config/gofer`）；nssm 模式靠 `AppEnvironmentExtra` 注入。计划任务继承的是**用户注册表级**环境，不是某个 shell 的环境。
- `golang.org/x/sys v0.47.0` 已是依赖（`windows` 子包可用：`CreateEvent/OpenEvent/SetEvent/WaitForSingleObject/OpenProcess/GetExitCodeProcess/ProcessIdToSessionId`）。
- 主机 CI 是 Windows（h-aii-3cro），本仓的 Windows-only 测试会在那里跑；容器只能跑 Linux 侧。

## 一、问题模型与选型

| 方式 | 进程所在会话 | 依赖登录 | 崩溃重启 | GUI | 结论 |
|---|---|---|---|---|---|
| nssm / sc 服务（现状） | session 0 | 否 | nssm | ✗ | **废弃**（0.2）：`start.ps1` 不再管理 nssm；真要无人值守/无桌面机器，自行用 sc/nssm 包一层 `gofer serve`，不在本仓维护 |
| 登录计划任务 + 看门狗（**本设计**） | 用户交互会话 | 是（掉电重启需自动登录或手动登一次） | 看门狗脚本 + 任务"失败后重启" | ✓ | **唯一受管方式** |
| 终端里 `gofer serve -d`（本设计补齐） | 当前会话 | 是 | 无 | ✓ | 临时 / 开发；也是 worker 后台化的手段 |
| 服务内 `CreateProcessAsUser` 投放 | 投放到活动会话 | 否 | — | ✓ | 不做（复杂度 = 再写一个 worker） |

决定（0.2）：**`start.ps1` 只管计划任务模式**；nssm 的参数（`-Auto/-Account/-Password/-Token`）、动作实现与文档删除，runbook 留一段迁移命令。当前主机切到登录任务模式后，`w-kzl-desktop` 前台窗口可以关掉（`local` 已有桌面；若仍要隔离可 `gofer worker -d`）。

## 二、SVC-01a `internal/daemon` 的 Windows 实现

`daemon_windows.go` 改为真实现，`errNotSupported` 整个删除（G032：无人依赖的"不支持"路径不保留）。

### 1. 分离启动 `reexecDetached`

```go
cmd := exec.Command(self, os.Args[1:]...)
cmd.Env = append(os.Environ(), EnvSentinel+"=1")
cmd.Stdin = nil                 // NUL
cmd.Stdout, cmd.Stderr = lf, lf // run/<name>.out.log，追加
cmd.SysProcAttr = &syscall.SysProcAttr{
    CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_BREAKAWAY_FROM_JOB,
}
```

- `DETACHED_PROCESS`：子进程不继承控制台 → 关终端 / Ctrl+C 不连坐（jcode 的效果）。`CREATE_NO_WINDOW` 与之互斥，不用。
- `CREATE_BREAKAWAY_FROM_JOB`：部分终端宿主把子进程放进 job object，关窗口会随 job 终止；breakaway 被拒（`ERROR_ACCESS_DENIED`）时**去掉该标志重试一次**。
- 父进程不 `Wait`，写 pidfile 后返回（与 unix 相同）。

### 2. 存活判定 `PIDAlive`

`OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION)`：`ERROR_INVALID_PARAMETER` → 不存在；`ERROR_ACCESS_DENIED` → 存在（他人进程）；成功则 `GetExitCodeProcess == STILL_ACTIVE(259)`。不做映像名核对——pid 复用的误判由「停止」路径兜底（见 3：事件不存在即报错，绝不误杀）。

### 3. 优雅停止：命名事件

无控制台进程收不到 Ctrl+C，用 **命名手动复位事件** `Global\gofer-stop-<pid>` 代替 SIGTERM：

- `daemon.NotifyStop(ch chan<- os.Signal)`（新，跨平台接口）：**被停方**（serve / worker 真实进程）启动时调用。Windows：`CreateEventW(nil, manualReset=true, false, name)` + goroutine `WaitForSingleObject(INFINITE)` → 触发后向 `ch` 发送 `syscall.SIGTERM`；创建失败只 `slog.Warn("daemon.stop_event_unavailable")`，不影响启动。Unix：空实现（真实信号已由 `signal.Notify` 投递）。
- `Terminate(pid)`（停止方）：Windows = `OpenEventW(EVENT_MODIFY_STATE, name)` + `SetEvent`。事件不存在（`ERROR_FILE_NOT_FOUND`）→ 返回错误：`stop event not found for pid=N: 不是本版本 gofer 起的进程或 pid 已被复用；确认已退出则删除 <pidfile>，仍在跑则 taskkill /PID N /F`。**永不 `TerminateProcess`**——硬杀交给人，避免 pid 复用误杀。
- 事件放 `Global\` 命名空间，同一用户跨会话可停（session 0 的 job 里 `gofer serve stop` 也能停 session 1 的 serve）；跨用户不支持（默认 DACL），文案里提示 taskkill。
- `daemon.KillHint(pid) string`（新）：unix `kill -9 N`，windows `taskkill /PID N /F`；`stopDaemon` 的 "SIGTERM"/"kill -9" 文案改为平台中立（"停止信号" + `KillHint`）。

### 4. 前台也记 pidfile：`daemon.Claim`

```go
// Claim 把当前进程 pid 写入 pidPath；若文件已被另一个存活进程占用则不覆盖（owned=false）。
// release 只在 owned 且文件内容仍是自己的 pid 时删除文件。
func Claim(pidPath string) (release func(), owned bool)
```

- serve / worker 的**真实进程**（无论前台还是 `-d` 子进程）启动即 `Claim`，退出 `release`（替代现在仅子进程的 `defer RemovePIDFile`）。`-d` 父进程仍先写 pidfile（子 pid），子进程 `Claim` 看到的是自己的 pid → owned。
- 收益：登录任务模式下 serve 是**前台**跑在看门狗里，`gofer serve stop` 仍能找到它；前台起的 worker 出现在 `runningWorkerIDs`（`worker stop` / doctor 可见）。
- 不 owned（同一 config dir 下已有另一个存活实例，如带 `--addr` 的第二个开发实例）：`slog.Warn("daemon.pidfile_busy", "pid", other)` 继续运行，退出时不动文件。绝不因 Claim 失败拒绝启动（端口冲突自然会拒）。

### 5. 会话信息 `daemon.SessionInfo()`

Windows：`ProcessIdToSessionId(GetCurrentProcessId)` + `WTSGetActiveConsoleSessionId()` → `{Session uint32, Interactive bool}`（Interactive = 二者相等）；Unix：零值 + `Interactive=false`（不适用）。serve 的 `server.ready` 与 worker 的就绪日志各加 `session`/`interactive` 字段（Windows 才有值）。runbook 里"确认跑在桌面会话"就看这一行。

### 6. 测试（先写先提交，固定名）

`internal/daemon`（跨平台，Windows CI 与容器都跑）：

- `TestClaimOwnsAndReleasesOnlyOwnPID`：Claim → 文件内容 = 自己 pid、owned；再次 Claim 仍 owned（幂等）；把文件改写成另一个存活 pid（用 `os.Getppid()`）后 release 不删除。
- `TestTerminateDeliversToSelf`：`signal.Notify(ch, SIGTERM)`；`NotifyStop(ch)`；`Terminate(os.Getpid())` → 2s 内收到信号（unix 走真实 SIGTERM，windows 走事件）。
- `TestSpawnDetachedRoundTrip`：helper 模式——测试进程把 `os.Args` 设为 `[self, -test.run=^TestHelperDaemonChild$]` 后 `Spawn`；子进程在 `TestHelperDaemonChild` 里（`Daemonized()==true` 才执行，否则 `t.Skip`）向 out.log 写 `child-ready`，`NotifyStop` + `signal.Notify` 等停止信号后写 `child-stopped` 退出。父进程：pidfile 存在、`PIDAlive` true、out.log 出现 `child-ready` → `Terminate` → 5s 内 `PIDAlive` false、out.log 有 `child-stopped`。
- `TestTerminateMissingTargetReportsHint`（windows-only 文件 `daemon_windows_test.go`）：对一个已退出的 pid `Terminate` → 错误含 `taskkill`。
- 现有 `TestPIDAlive` 去掉 windows 的 false 断言，改为对 `os.Getpid()` true、对已退出子进程 false。

`internal/commands`：`TestStopDaemonHintIsPlatformNeutral`——对不存在的 pidfile / 假 pid 走 `stopDaemon`，输出不含 "SIGTERM"、"kill -9"（读 `KillHint`）。

## 三、SVC-01b serve / worker 接入

- `internal/serve/serve.go:300`：`signal.NotifyContext` 改为显式通道：`sig := make(chan os.Signal, 1); signal.Notify(sig, SIGINT, SIGTERM); daemon.NotifyStop(sig)`；`ctx` 在收到任一信号时 cancel（语义不变）。`server.ready` 日志加 `session`/`interactive`。
- `internal/worker/serve.go:36`：`signal.Notify` 之后加 `daemon.NotifyStop(sig)`；就绪日志同上。
- `internal/commands/serve.go` / `worker.go`：删除 `if daemon.Daemonized() { defer RemovePIDFile }`，改为 `release, owned := daemon.Claim(pidPath); defer release()`；`-d` 父进程分支不变。`serve -d` 的帮助文案去掉"Linux only"含义（现在没有，保持）。
- 客户端模式拒绝（`config.IsClientRunMode()`）顺序不变，仍在 Spawn 之前。

## 四、SVC-01c `start.ps1 -Mode task` 与看门狗开关

### 1. `win-supervisor.ps1` 两个新参数

- `-StopMarker <path>`（默认 `<ExeDir>\gofer.stop`）：每次（重）启动 gofer 前检查，**存在则记日志并退出循环（exit 0）**。这是 `stop` 能"优雅停 + 不被拉起"的关键：先落标记，再让 gofer 自己退。
- `-EnvExtra <string[]>`（`KEY=VALUE`）：循环开始前逐条 `Set-Item Env:`，对应 nssm 的 `AppEnvironmentExtra`——把 `GOFER_CONFIG_DIR`（和可选 `GOFER_TOKEN`）带给 gofer，不依赖用户级环境变量。
- 快速失败回滚逻辑不变；日志沿用 `win-supervisor.log`。

### 2. `start.ps1` 改为**仅计划任务模式**（nssm 代码删除）

参数：`-Action up|upgrade|stop|restart|remove|status|logs`（默认 `up`）、`-Web`、`-ConfigDir`、`-Addr`、`-Config`、`-TaskName`（默认 `gofer-serve`）、`-User`、`-Elevated`。nssm 时代的 `-ServiceName/-Auto/-Account/-Password/-Token` 与 `Assert-Nssm/Assert-Admin` 一并删除；`serve-run\nssm.exe` 不再被引用。

| Action | 做什么 |
|---|---|
| `up` | 若存在名为 `gofer` 的 Windows 服务（`Get-Service`）→ 直接报错退出并打印迁移命令（管理员窗口 `nssm stop gofer; nssm remove gofer confirm`，或 `sc.exe stop gofer; sc.exe delete gofer`）——两者抢同一端口，脚本不替人停服务。删除 `gofer.stop` 标记。`Register-ScheduledTask -TaskName gofer-serve`：Action = `conhost.exe --headless pwsh.exe -NoProfile -NonInteractive -File <repo>\scripts\win-supervisor.ps1 -ExeDir <repo>\serve-run -WorkDir <repo> -ServeArgs serve,--web-dir,./web/dist[,--addr,…][,--config,…] -EnvExtra GOFER_CONFIG_DIR=…[,GOFER_TOKEN=…]`（无 `conhost --headless` 的老系统退回 `pwsh -WindowStyle Hidden`，会闪一下窗）；Trigger = `AtLogOn -User <user>`；Principal = `-UserId <user> -LogonType Interactive -RunLevel Limited`（`-Elevated` 开关切 `Highest`，此时注册需管理员）；Settings = `-ExecutionTimeLimit 0 -RestartCount 99 -RestartInterval 1min -MultipleInstances IgnoreNew -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries`。然后 `Start-ScheduledTask`，等 `/health` 200（≤20s），打印 `gofer.exe --version` 与会话号。 |
| `stop` | 写 `gofer.stop` → `gofer.exe serve stop`（走 pidfile + 事件，优雅）→ 等任务状态离开 `Running`（≤15s）→ 超时才 `Stop-ScheduledTask`（硬）并警告。 |
| `restart` | `stop` + 删标记 + `Start-ScheduledTask`。 |
| `upgrade` | `make build`（`-Web` 同现状）→ `stop` → 备份 `gofer.exe.prev`、覆盖 → 删标记 → `Start-ScheduledTask` → 校验版本。**不需要管理员**（任务归当前用户）。 |
| `remove` | `stop` + `Unregister-ScheduledTask`（二进制/日志保留）。 |
| `status` | 任务状态、上次运行结果、Action 命令行、gofer 进程 pid/SessionId、`/health`。 |
| `logs` | `win-supervisor.log` + `serve.log` 尾部（task 模式下 stdout 由 gofer 自己的日志承载，没有 nssm 的 out/err 文件）。 |

- 用户身份：默认 `-User` = `(Get-CimInstance Win32_ComputerSystem).UserName`（当前登录的控制台用户，去掉域前缀后与 `whoami` 比对），不是 `$env:USERNAME`——从 session 0 的 job 里跑脚本时后者可能是 SYSTEM。
- 自更新链不变：gofer 的父进程仍是 `win-supervisor.ps1`（祖父是 pwsh），`win-selfupdate.ps1` 默认 `-SupervisorMarker win-supervisor` 直接通过；被 kill 后循环 2s 拉起新 exe。`gofer.stop` 不存在时 kill 才会被拉起——`stop` 与自更新互斥由标记保证。
- 不再有 `AppExit`/`ObjectName`/`-Auto` 等 nssm 概念：登录触发即"自启"，看门狗即"失败重启"。

### 3. `scripts/win-tasktest.ps1`（隔离验收，omp 在主机跑）

仿 `win-selftest.ps1`：临时 `TestRoot`、独立 `GOFER_CONFIG_DIR`（含最小 `config.yaml` + `.env`）、端口 9098、任务名 `gofer-tasktest-<rand>`，全程只动自己起的进程，结束 `Unregister` + 清理。断言：

1. `up` 后 `/health` 200；`Get-Process gofer` 中该 exe 的 `SessionId` == 控制台会话（`explorer.exe` 的 SessionId）；serve.log `server.ready` 含 `interactive=true`。
2. `serve stop`（脚本 `stop`）后：serve.log 有 `server.shutdown`，pidfile 消失，`gofer.stop` 存在，任务状态 `Ready` 且 gofer 进程不再出现（看门狗没拉起）。
3. `restart` 后再次 200；标记已删。
4. `taskkill /PID <gofer> /F` 模拟崩溃 → ≤10s 内 `/health` 恢复（看门狗拉起）。
5. 直接 `gofer.exe serve -d`（临时 config dir、端口 9097）→ 父进程退出后 `/health` 200；关闭起它的 pwsh 进程后仍 200；`gofer.exe serve stop` 优雅退出（日志 `server.shutdown`）。

## 五、文档

- `docs/runbook/2026-07-11-windows-server-selfupdate-runbook.md`：§7「nssm 服务」整节**重写为「常驻：登录计划任务（桌面会话）」**——为什么不是服务（session 0 一句话 + 本设计 §一 表）、`start.ps1` 各 Action、确认跑在桌面会话（`server.ready … interactive=true`、`-Action status` 的 SessionId）、自动登录提示（掉电重启需 `netplwiz`/Autologon，属运维决定）、常见坑表（`gofer` 服务残留导致端口冲突；`-Elevated` 与被操作程序的完整性级别要一致，否则 UIPI 挡住；锁屏/RDP 断开时 GUI 自动化不可靠；`GOFER_CONFIG_DIR` 由 `-EnvExtra` 带入，不靠 shell 环境）。原 §7.4 的 `-SupervisorMarker 'nssm'` 段删除（自更新回到默认 marker）。文末保留 5 行「从 nssm 迁移」：管理员窗口 `nssm stop gofer; nssm remove gofer confirm` → 普通窗口 `start.ps1 -Action up -ConfigDir …` → 验证。
- `scripts/README.md`：nssm 一节删除；保留 supervisor 裸跑（调试）与 `start.ps1`（常驻）两段。
- `start.ps1` 头部 `.SYNOPSIS/.NOTES` 重写；`docs/gofer-enhancements-roadmap.md` SVC-01 落地、CFG-08 描述改为"`start.ps1 -Action upgrade`（Windows 常驻实例原地升级）"；`gofer-usage` skill 不改（容器侧用法不变）。

## 横切

- G032：`daemon_windows.go` 的 `errNotSupported` 与相关注释删除；`stopDaemon` 文案不留"SIGTERM/kill -9"；`start.ps1` 的 nssm 参数/动作/帮助与 runbook、README 中的 nssm 内容删除（用户已决定废弃，不打 DEPRECATED，仅留迁移命令）。无 DEPRECATED 标记需要新增。
- 兼容：Linux 行为仅一处变化——前台 `serve`/`worker` 也会在 `<config-dir>/run/` 留 pidfile（优雅退出即删；崩溃残留由 `stop` 的存活检查处理）。`gofer worker -d` 在容器的用法不变。
- 安全：事件对象只允许同一用户 SetEvent；task 模式下 serve 以登录用户身份跑，与 `-Account` 服务等价，web 暴露面不变。
- Windows CI：`internal/daemon` 新测试会在主机 CI 跑；`win-tasktest.ps1` 不进 CI（需要交互会话），作为发布前手工/omp 验收。

## 实施分期与验收

W1 派 `omp-acp`（用户要看 ACP 通道改造后的效果），W2 派 omp；测试先写先提交。**主机 job 本身跑在 live 的 nssm 服务之下——任务书必须禁止它停止/移除/重启 live 的 `gofer` 服务或注册名为 `gofer-serve` 的任务；一切真机验证走隔离实例（临时 config dir + 非 live 端口 + 随机任务名）。** 正式切换由用户在桌面手工执行（需要管理员卸 nssm 服务）。

| 期 | 内容 | 验收 |
|---|---|---|
| W1 | §二 + §三：`daemon_windows.go` 真实现、`Claim`、`NotifyStop`、`KillHint`、`SessionInfo`、serve/worker 接入、平台中立文案；测试 §二.6 | 容器：`go build ./...`（linux + `GOOS=windows`）、`go vet`、`go test ./internal/daemon/... ./internal/commands/... ./internal/serve/... ./internal/worker/...`；主机：`go test ./internal/daemon/...` 原始输出（Windows 真跑 `TestSpawnDetachedRoundTrip`）；主机隔离实例 `gofer.exe serve -d` → 关终端存活 → `serve stop` 日志 `server.shutdown` |
| W2 | §四 + §五：`win-supervisor.ps1` 开关、`start.ps1` 重写为计划任务模式（nssm 删除）、`win-tasktest.ps1`、runbook §7 重写/README/roadmap | 主机：`pwsh -File scripts\win-tasktest.ps1` 全部 PASS 的原始输出（含 SessionId 对比行）；`win-selftest.ps1` 仍 PASS（自更新链未破）；脚本 `pwsh -NoProfile -Command "Get-Command -Syntax"` 级别的语法检查 |
| 切换（用户） | 桌面管理员窗口：`nssm stop gofer; nssm remove gofer confirm`（`serve-run\nssm.exe`）→ 普通窗口 `start.ps1 -Action up -ConfigDir …` → 从容器派 `gofer job run -a exec --runner local -- pwsh -c "(Get-Process -Id $PID).SessionId"` 应为非 0，再派一次 `hmicli … capture` 出图 | `-Action status` 显示任务 Running、SessionId 非 0、`/health` 200 |

## 风险与限制

- **依赖登录**：掉电重启后无人登录则 server 不在。选项：自动登录（Autologon/`netplwiz`）+ 锁屏策略。nssm 已废弃，不再提供"并存"选项。
- **锁屏 / RDP**：锁屏后桌面切到 Winlogon，SendInput 类自动化失败、截图可能黑屏；RDP 断开后会话 disconnected 同理。这是 Windows 桌面语义，不在本设计内解决；runbook 记录。
- **UIPI**：serve 以 `Limited` 跑时操作不了以管理员身份打开的 DTools/CODESYS 窗口；反之亦然。`-Elevated` 开关提供，但默认与用户平时开软件的方式一致（非提权）。
- **pid 复用**：pidfile 指到无关进程时 `Spawn` 会拒绝"已在运行"，`stop` 会报"事件不存在"并给出处置文案；不自动硬杀。
- **看门狗与 stop 的竞态**：`stop` 先落标记再发事件；看门狗每次拉起前检查标记，2s 睡眠窗口内也会看到标记（检查在 `Start-Sleep` 之后、启动之前）。
- **`conhost --headless` 可用性**：Windows 10 1809+ / 11 有；缺失时退回 `pwsh -WindowStyle Hidden`（登录时闪一下窗）。

## 决策（已批准 2026-09-22）

1. task 模式的任务动作是**看门狗前台跑 serve**（不是 `serve -d`），以保留崩溃重启与快速失败回滚；`serve -d` 用于终端临时起、worker 后台化。
2. 前台 serve/worker 也 `Claim` pidfile（跨平台一致；`stop` 与 `runningWorkerIDs` 因此覆盖前台实例）。
3. Windows 的 `stop` 只走命名事件，事件不存在即报错给人处置，**永不 `TerminateProcess`**。
4. **nssm 方式废弃**（用户 2026-09-22）：`start.ps1` 只有计划任务模式，nssm 参数/动作/文档删除；`up` 检测到名为 `gofer` 的服务时拒绝并打印迁移命令。
5. 事件命名空间 `Global\`（同用户跨会话可停）；跨用户不支持。

## W1 实测记录（2026-09-22，主机 Windows 11 26100 / go1.25.10 windows-amd64）

范围 = §二 + §三。提交：`5ad16ca`（测试，red，注明）→ `b887669`（`internal/daemon` 实现）→ `7f28cfd`（serve/worker 接入 + 平台中立文案）。W2（§四 + §五）与正式切换（nssm 卸载）不在本次记录内。

### 1. 测试（主机真跑，非容器）

```
=== RUN   TestPIDAlive
--- PASS: TestPIDAlive (0.02s)
=== RUN   TestClaimOwnsAndReleasesOnlyOwnPID
--- PASS: TestClaimOwnsAndReleasesOnlyOwnPID (0.01s)
=== RUN   TestTerminateDeliversToSelf
--- PASS: TestTerminateDeliversToSelf (0.00s)
=== RUN   TestSpawnDetachedRoundTrip
--- PASS: TestSpawnDetachedRoundTrip (0.10s)
=== RUN   TestTerminateMissingTargetReportsHint
--- PASS: TestTerminateMissingTargetReportsHint (0.02s)
ok  	github.com/inhere/gofer/internal/daemon	0.162s
--- PASS: TestStopDaemonHintIsPlatformNeutral (3.01s)
ok  	github.com/inhere/gofer/internal/commands	3.032s
```

`TestSpawnDetachedRoundTrip` 走的是真 detach，不是 mock：父测试进程把 `os.Args` 改成 `-test.run=^TestHelperDaemonChild$` 后 `Spawn` 自身 → 子进程（`Daemonized()==true`）写 `child-ready` → 父进程 `Terminate(pid)` → 子进程收到停止事件写 `child-stopped` 后退出，pidfile 内容 == 子 pid。`internal/serve`、`internal/worker` 的全量包测试同为 `ok`（含此前因 Windows 无 PIDAlive 而 skip 的 `TestResolveDefaultWorker*`，本次已去掉 skip）。

### 2. 隔离实例 smoke（临时 config dir + 127.0.0.1:19097；未碰真实 server、8767 端口与真实配置目录）

```
[1] gofer.exe serve -d        # GOFER_CONFIG_DIR=<tmp>/cfg, GOFER_LOG_LEVEL=debug
gofer serve 已后台启动 pid=40196 log=D:\tmp\gofer-w1-smoke\cfg\run\serve.log   (exit=0)
    parent stderr = 空（无 daemon.breakaway_denied）
[2] cfg\run\serve.pid = 40196   # 父进程已退出，子进程仍在（脱离控制台存活）
[3] curl http://127.0.0.1:19097/health → HTTP=200
[4] gofer.exe serve -d（第二次）
ERROR: serve: already running (pid=40196, pidfile=D:\tmp\gofer-w1-smoke\cfg\run\serve.pid)   (exit=2)
[5] gofer.exe serve stop
gofer: 已向 serve(pid=40196) 发送停止信号，等待退出...
gofer: serve 已停止   (exit=0)
[6] cfg\run\ 只剩 serve.log / serve.out.log（pidfile 已删）
[7] cfg\run\serve.log:
"event":"server.ready","component":"server","addr":"127.0.0.1:19097","session":0,"interactive":false}
"event":"server.shutdown","component":"server"}
```

- `session:0, interactive:false` 是**正确**读数：本 job 本身跑在 live 的 nssm 服务之下（session 0；同一时刻 `WTSGetActiveConsoleSessionId()`=1）。这正是要暴露的判据——从 session 0 起的 serve 不在桌面上。`interactive=true` 只能在登录会话里起 serve 时验证，属 W2 的 `win-tasktest.ps1` §四.3.1。
- `serve.out.log` 有内容（子进程 stderr 的文本日志），说明 Windows 的 `-d` 同样保留 sidecar 文件。
- 关终端存活：`serve -d` 的父进程（bash 调用）结束后子进程仍在并继续应答 `/health`。

### 3. breakaway 是否触发重试：**未触发**（首次带 `CREATE_BREAKAWAY_FROM_JOB` 就成功）

`parent stderr = 空` 只能证明"没有记录到拒绝"，为免推断，临时在成功分支加了一行 `slog.Debug("daemon.breakaway_accepted_TEMP_PROBE")` 复测：

```
time=2026-09-22T12:37:40.735 level=DEBUG msg=daemon.breakaway_accepted_TEMP_PROBE
```

即 `GOFER_LOG_LEVEL=debug` 的 stderr 通道确实能打出来（说明上一行为空不是日志被吞），且本次环境（gofer job → nssm 服务）不是禁止 breakaway 的 job object。临时那行已删除（`daemon.breakaway_denied` 保留为真实诊断）。禁止 breakaway 的主机（Windows Terminal / 某些 CI）走的是重试分支，未在本机复现。

### 4. 实测发现的一处设计空白（已修，待复核）

**现象**：`serve -d` 之后**立刻**（约 0.1s）`serve stop` 会失败：

```
ERROR: stop serve (pid=41620): stop event not found for pid=41620: not a gofer started by this build, or the pid was reused; ...
```

同一进程等它起来后再 stop 就正常。原因：pidfile 由父进程在 `Spawn` 时写入，而停止事件由子进程在 `serve.Start` 末尾（Core 组装之后，~1s）创建 —— 这中间存在一个"pidfile 可见但还不可停"的启动窗口。unix 没有这个窗口（`kill(SIGTERM)` 对启动中的进程立即生效，默认处理直接终止）。

**处理**：`stopDaemon` 在目标**仍存活**时把停止请求重试一小段窗口（`stopRequestRetry = 3s`，200ms 间隔），窗口过后仍失败才报原来的错误文案；unix 首次即成功、不进重试。`pid 复用`/`非本版本进程`的处置语义不变（错误文本与 `KillHint` 一致，只是晚 3s 出现）。复测：`serve -d` + 立刻 `serve stop` → `已向 serve(pid=40976) 发送停止信号` / `serve 已停止`。

这是对任务书 `stopDaemon` 描述的**超出项**（任务书只要求文案中立），如不认可可只回退该重试循环（`internal/commands/stop.go` 的 `stopRequestRetry` + 请求循环），其余不变。

### 5. 未在本机验证（W2 / 人工）

- `interactive=true`（登录会话里跑 serve）、任务模式看门狗、`start.ps1` 重写、nssm 卸载迁移：属 §四/§五。
- 禁止 breakaway 的宿主上的重试分支；跨用户 stop（应报 `taskkill` 提示）。

