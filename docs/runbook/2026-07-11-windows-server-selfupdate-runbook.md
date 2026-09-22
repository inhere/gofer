# Runbook — Windows gofer server 监督运行 + 自更新

> 配套脚本：`scripts/win-supervisor.ps1`（监督循环）/ `scripts/win-selfupdate.ps1`（自更新）/ `scripts/win-selftest.ps1`（验收）。
> 设计见 `docs/plans/2026-07-09-windows-server-selfupdate-plan.md`（v0.3）。所有路径用 `<占位符>`，按实际部署替换。

## 0. 名词 / 前提

- `<ExeDir>`：**运行中** `gofer.exe` 所在目录（监督循环启动的就是它；rename-replace 的目标）。
- `<RepoDir>`：源码仓目录，`git pull` + `go build` 在此进行。**常与 `<ExeDir>` 不同**（如 `go install` 装到 GOPATH/bin，源码在别处）。
- `<WorkDir>`：server 启动的工作目录；**相对** serve 参数（如 `--web-dir ./web/dist`）按它解析，须与原启动一致。
- `<ServeArgs>`：原来 `gofer serve` 的完整参数（如 `s --web-dir ./web/dist`）。
- 前提：主机装有 `pwsh`(7+)、`go`、`git`；`<ExeDir>` 可写（rename-replace 需要）。

## 1. 起服务：用监督循环替代裸跑 `gofer serve`

> 常驻/开机自启请直接用 §7 的 `scripts/start.ps1`（登录计划任务）；本节只用于**调试**（前台盯着看日志、临时换参数）。

在原来手动跑 `gofer serve` 的终端，改为**直接**启动监督脚本（`pwsh -File` 直启，使其命令行含 `win-supervisor` —— 自更新的 F4 守卫据此识别祖父进程）：

```powershell
pwsh -NoProfile -File scripts/win-supervisor.ps1 `
  -ExeDir '<ExeDir>' `
  -ServeArgs @('serve','--addr','0.0.0.0:8765','--web-dir','./web/dist') `
  -WorkDir '<WorkDir>'
```

- 该进程是 gofer 的**父**、处于其进程树之外：更新时杀掉 gofer 换二进制，监督循环仍在、会重启新 exe。
- 附带**崩溃自愈**（gofer 意外退出 2s 后自动重启）。
- 看门狗：新 exe 连续快速失败（启动即退）达 `-FastFailThreshold`(默认3) 次 → 自动回滚 `gofer.old.exe`。
- 日志默认 `<ExeDir>\win-supervisor.log`。

> ⚠️ 不要用包装脚本/别名间接启动监督器，否则其命令行不含 `win-supervisor`，F4 守卫会拒绝自更新（除非用 `-SupervisorMarker` 显式指定标识）。

## 2. 触发一次自更新

自更新脚本**必须作为 gofer 的直接子进程运行** —— 通过 `agent=exec` 的 job（不要用 codex / shell 包装，否则父进程不是 gofer，F2 守卫拒绝）：

```powershell
gofer job run -a exec --runner local -- `
  pwsh -NoProfile -File scripts/win-selfupdate.ps1 `
    -RepoDir '<RepoDir>' -ExeDir '<ExeDir>'
```

流程：
1. **阶段1（安全）**：`<RepoDir>` 内 `git pull` + `go build -o gofer-new.exe ./cmd/gofer` + `-V` 校验。任一失败即中止，**不碰运行中的 exe**。
2. **阶段2（切换）**：`<ExeDir>` 内 `gofer.exe → gofer.old.exe`（运行中可 rename）、`gofer-new.exe → gofer.exe`，然后**按本 job 父 pid 精确杀 gofer**；监督循环约 2s 后拉起新 exe。

> 触发 job 在 gofer 被杀后其输出转发即断，网关侧多半显示为**中断/失败——属正常现象**，不代表更新失败。以下用带外方式确认。

## 3. 确认更新结果（带外）

```powershell
Invoke-WebRequest -UseBasicParsing http://127.0.0.1:8765/health   # 200 = 已重启就绪
& '<ExeDir>\gofer.exe' -V                                         # 版本已更新
Get-Content '<ExeDir>\win-supervisor.log' -Tail 20               # 看重启/回滚记录
Test-Path '<ExeDir>\gofer.old.exe'                               # 旧版留底(回滚点)
```

## 4. 回滚

- **自动**：坏 exe 连续快速失败达阈值，监督器自动 `gofer.old.exe → gofer.exe`。
- **手动**：
  ```powershell
  Copy-Item '<ExeDir>\gofer.old.exe' '<ExeDir>\gofer.exe' -Force
  Stop-Process -Id (Get-NetTCPConnection -LocalPort 8765 -State Listen).OwningProcess -Force  # 监督器重启旧版
  ```

## 5. 验收自测（隔离，不碰 live server）

在独立端口 + 独立配置目录起第二实例，自清理：

```powershell
pwsh -NoProfile -File scripts/win-selftest.ps1      # 监督+自更新：期望 RESULT: pass=13 fail=0
pwsh -NoProfile -File scripts/win-tasktest.ps1      # 登录计划任务：期望 RESULT: pass=N fail=0
```

- `win-selftest.ps1` 覆盖：崩溃自愈 / rename-replace 换新 exe / 看门狗回滚 / F2 守卫拒绝非 gofer 父进程。
- `win-tasktest.ps1` 覆盖 §7 的任务模式：`up` 后 `/health` 200 且 gofer 的 `SessionId` == 控制台会话、`server.ready` 的 `interactive` 字段、`stop` 后优雅退出且看门狗不拉起、`restart`、崩溃后被看门狗拉起、`serve -d` 的分离存活与 `serve stop`、`remove` 注销任务；以及**不带 `-ConfigDir`** 的 `status/stop/restart/upgrade/logs`（config dir 从任务动作找回，`status` 断言 `config dir: … (from task)`）、重跑 `up` 沿用任务里记的 config dir、四路来源全空时 `status` 打印两种一次性做法并以非 0 退出。它注册的是**随机任务名**（`gofer-tasktest-<4位>`）+ 独立 `-ExeDir`/`-ConfigDir`/端口（默认 9098/9097），`finally` 里无条件注销任务、杀掉 TestRoot 下的进程、删目录。

## 6. 排障

| 现象 | 原因 / 处理 |
|---|---|
| `guard(F2): parent is '...'` | 自更新没作为 gofer 直接子进程跑 → 用 `-a exec`，勿经 codex/shell |
| `guard(F4): ... not the supervisor` | server 是裸跑（无监督器）→ 先按 §1 用监督循环起，或按 §7 用 `start.ps1 -Action up` 起常驻实例；只有监护进程非 `win-supervisor` 时才需 `-SupervisorMarker` |
| 重启后 `--web-dir` 等相对参数失效 | `-WorkDir` 未对齐原启动 cwd → 传正确 `<WorkDir>` 或改用绝对路径参数 |
| 监督器不断重启坏 exe | 达阈值会自动回滚；若无 `gofer.old.exe`，手动放回一份可用 exe |
| 主机同时跑 gofer worker | 自更新按**父 pid**精确杀，不按进程名，不会误伤 worker |

## 7. 常驻 / 开机自启：登录计划任务（桌面会话，推荐）

常驻实例由 `scripts/start.ps1` 注册成一个**登录计划任务**（默认任务名 `gofer-serve`），任务动作 = `conhost.exe --headless pwsh -File scripts\win-supervisor.ps1`（即 §1 的看门狗循环），所以崩溃自愈、坏 exe 回滚、rename-replace 自更新**全部照旧**。

**为什么不是 Windows 服务（nssm / sc）**：服务一律跑在 **session 0**，与登录用户的桌面（session 1+）隔离。`--runner local` 的 job 继承 serve 所在会话，所以 DTools / CODESYS 自动化、`capture click`、窗口截图这类**需要桌面**的动作全部失败。登录计划任务跑在你的**交互会话**里，local job 直接拥有桌面。

选型（设计 `docs/design/2026-09-22-windows-desktop-session-service-design.md` §一）：

| 方式 | 进程所在会话 | 依赖登录 | 崩溃重启 | GUI | 结论 |
|---|---|---|---|---|---|
| nssm / sc 服务 | session 0 | 否 | nssm | ✗ | **已废弃**：`up` 检测到名为 `gofer` 的服务会拒绝（两条迁移命令见 §7.5） |
| 登录计划任务 + 看门狗 | 用户交互会话 | 是 | 看门狗 + 任务失败重启 | ✓ | **本仓唯一受管方式** |
| 终端里 `gofer serve -d` / `worker -d` | 当前会话 | 是 | 无 | ✓ | 临时 / 开发；worker 后台化 |
| 服务内 `CreateProcessAsUser` 投放 | 投放到活动会话 | 否 | — | ✓ | 不做（复杂度 = 再写一个 worker） |

### 7.1 前置

- gofer.exe 在 `<repo>\serve-run\gofer.exe`（`go build -o serve-run\gofer.exe .\cmd\gofer`）；`dist\gofer.exe` 是 `-Action upgrade` 用的构建产物。
- 一个 gofer **配置目录**（`config.yaml` + `.env`）。`-ConfigDir` **只需首次 `up` 给一次**（或改用用户级 `$env:GOFER_CONFIG_DIR`）；之后所有动作自己从已注册的任务里找回 —— 见下。它经任务动作的 `-EnvExtra GOFER_CONFIG_DIR=…` 传进任务（任务继承的是**用户注册表**环境，不是你 shell 的），gofer 据此找到 `config.yaml` 并加载该目录 `.env` 里的 `GOFER_TOKEN`。缺了 → `refusing to start without a token`。任务定义里**不写** token。
- **普通窗口即可**（任务归当前用户）。只有 `-Elevated`（RunLevel Highest）注册时需要提权。
- 机器上若还装著名为 `gofer` 的服务（旧 nssm），`up` 会**拒绝**并打印迁移命令（两者抢同一端口）→ 见 §7.5。

**`-ConfigDir` 从哪来**（按序取第一个命中的）：

| # | 来源 | 说明 |
|---|---|---|
| ① | `-ConfigDir <dir>` | 显式参数，永远最优先（换配置目录就用它） |
| ② | `$env:GOFER_CONFIG_DIR` | 当前 shell 的环境变量 |
| ③ | 已注册任务的动作里的 `-EnvExtra GOFER_CONFIG_DIR=…` | **首次 `up` 之后的事实源**：`status/stop/restart/logs/upgrade` 不再需要 `-ConfigDir`，重跑 `up` 也沿用 |
| ④ | `~\.config\gofer` | 仅当其中真有 `config.yaml`（= gofer CLI 自己的默认目录） |

四路全空 → `up`（以及 `status/logs/restart/upgrade`）报错并打印两种一次性做法：`-ConfigDir <dir>`，或设一次用户级环境变量 —— 之后**新开的窗口与登录任务都会继承**，gofer CLI 也就自动找到配置：

```powershell
[Environment]::SetEnvironmentVariable('GOFER_CONFIG_DIR','<dir>','User')
```

`stop` 是例外：找不到 config dir 时照样落 `gofer.stop` 标记并退回硬停，只 Warning（停机不能被配置问题卡住）。

### 7.2 各 Action（`pwsh -File scripts\start.ps1 -Action …`）

| Action | 做什么 |
|---|---|
| `up`（默认） | 注册/更新任务（`AtLogOn -User <当前控制台用户>`、`LogonType Interactive`、默认 `Limited`），清 `gofer.stop` 标记，启动任务，等 `/health` ≤20s，打印版本 / gofer pid / SessionId |
| `upgrade` | 先 `make build`（服务照跑；构建失败**不动**二进制；`-Web` 同时 `make web`）→ `stop` → 旧 exe 存成 `gofer.exe.prev`、`dist\gofer.exe` 覆盖过去 → 启动 → 等 `/health` → 打印新旧版本与回滚命令。**不需要管理员** |
| `stop` | 先落 `gofer.stop` 标记（看门狗不再拉起）→ `gofer serve stop`（pidfile + 命名事件优雅停）→ 等 ≤15s（任务离开 Running 且进程消失）；超时才 `Stop-ScheduledTask` 硬停并 Warning |
| `restart` | `stop` → 删标记 → 启动 → 等 `/health` |
| `remove` | `stop` + `Unregister-ScheduledTask`（exe / 日志保留） |
| `status` | 任务 State、`Get-ScheduledTaskInfo`（LastRunTime / LastTaskResult / NextRunTime）、Action 的 Execute+Argument、gofer 进程 pid/SessionId/StartTime、`/health`、标记是否存在；开头打印 config dir 的来源 `config dir: <dir> (from param/env/task/default)` |
| `logs` | tail `serve-run\win-supervisor.log` 与 `<ConfigDir>\run\serve.log`（`serve -d` 另有 `serve.out.log`） |

常用参数：`-ConfigDir <dir>`（**首次 `up` 必需**，之后从任务动作找回，见 §7.1）、`-Addr 0.0.0.0:<port>`（覆盖 config `server.addr`，同时作为健康探测地址；`0.0.0.0` 会换成 `127.0.0.1` 访问）、`-NoWeb`（`--no-web`，否则默认 `--web-dir ./web/dist`）、`-Config <file>`、`-TaskName`、`-ExeDir`（默认 `<repo>\serve-run`）、`-User`（默认当前控制台用户）、`-Elevated`。

```powershell
# 首次：注册任务（唯一需要 -ConfigDir 的一次；普通窗口）
pwsh -File scripts\start.ps1 -Action up -ConfigDir '<ConfigDir>'

# 之后：config dir 从任务动作里的 -EnvExtra 找回，都不用再传 -ConfigDir
pwsh -File scripts\start.ps1 -Action status
pwsh -File scripts\start.ps1 -Action logs
pwsh -File scripts\start.ps1 -Action restart
pwsh -File scripts\start.ps1 -Action stop
pwsh -File scripts\start.ps1 -Action upgrade                 # 原地升级（不需要管理员），仅 Go
pwsh -File scripts\start.ps1 -Action upgrade -Web            # 同上，连 web 控制台
pwsh -File scripts\start.ps1 -Action up                      # 重跑 up：沿用任务里记的 config dir
```

> 想彻底不再依赖任务找回，就设一次用户级变量（新窗口与登录任务都继承）：`[Environment]::SetEnvironmentVariable('GOFER_CONFIG_DIR','<ConfigDir>','User')`。

### 7.3 确认它真的在桌面会话里

```powershell
# ① 任务状态 + gofer 进程 SessionId（应非 0；与 explorer.exe 的 SessionId 相同 = 同一个桌面）
#    首行还会打印 config dir 的来源，如 config dir: <dir> (from task)
pwsh -File scripts\start.ps1 -Action status

# ② server 自证的判据：ready 行里的 session / interactive / console
Get-Content '<ConfigDir>\run\serve.log' | Select-String 'server.ready'
#   {"event":"server.ready",...,"session":2,"interactive":true,"console":false}
```

`interactive` 与 `console` 问的是两件事：

- **`interactive`** = 「本进程**不在 session 0**」，即跑在某个用户会话里（RDP 会话也算）。这就是"能不能碰桌面"的判据：`false` 只可能是 session 0（服务），GUI 自动化（DTools/CODESYS/截图）必然失败。
- **`console`** = 「本进程就在**物理控制台**那个会话」（`WTSGetActiveConsoleSessionId()`），比 `interactive` 窄。**只用 RDP 登录**的主机上，控制台会话是那个空着的 session 1，而你和 serve 在 RDP 会话（如 session 2）→ `interactive=true, console=false`，**这是正常的**（serve 就在你的桌面上）。
- `session` 与 `explorer.exe` 的 SessionId 一致，是"在同一个桌面"的直接证据（`query session` 看会话列表与 Active 标记，`-Action status` 打印 serve 的 SessionId 与 explorer 的会话号对比）。

最硬的验证：从容器派一个 local job，`gofer job run -a exec --runner local -- pwsh -c "(Get-Process -Id $PID).SessionId"` 应返回非 0 会话号。

### 7.4 常见坑

| 现象 | 原因 / 处理 |
|---|---|
| `up` 报 `a Windows service named 'gofer' exists` 并退出 3 | 旧 nssm 服务还在（抢端口）。管理员窗口先卸，见 §7.5；**隔离实例**（自己的 TaskName/ExeDir/端口）可加 `-AllowServiceConflict` 跳过该检查 |
| local job 碰不到桌面 / `BitBlt Access denied` | serve 不在用户会话：`-Action status` 看 SessionId（0 = session 0；对不上 explorer 的会话号就是不在同一个桌面）。`serve.log` 的 `interactive=false` 就表示跑在 session 0（服务），必须处理；`console=false` 只说明不是物理控制台（RDP 登录下正常），见 §7.3 |
| 以管理员打开的 DTools/CODESYS 点不动 | UIPI：进程完整性级别不一致。gofer 也 `-Elevated` 注册（需管理员），或让目标程序以普通权限开 —— **两边必须一致** |
| 锁屏 / RDP 断开后 GUI 自动化失败、截图黑屏 | Windows 桌面语义：锁屏后活动桌面是 Winlogon，RDP 断开后会话 disconnected。属桌面策略，本仓不解决 |
| 重启后 server 不在 | 登录计划任务**依赖登录**。无人值守需开自动登录（`netplwiz` / Autologon）+ 放宽锁屏 —— 运维决定 |
| gofer 找不到 config / `refusing to start without a token` | 任务环境里没有 `GOFER_CONFIG_DIR`：靠 `-ConfigDir` 经看门狗 `-EnvExtra` 注入，别指望用户 shell 的环境变量。核对：`-Action status` 首行 `config dir: <dir> (from task)` |
| 脚本报 `no gofer config dir found` | 四路来源全空（§7.1）：任务没注册 / 动作里没有 `GOFER_CONFIG_DIR`、`$env:GOFER_CONFIG_DIR` 没设、`~\.config\gofer` 也没有 `config.yaml`。按提示的两种一次性做法之一处理（`-ConfigDir`，或用户级 `SetEnvironmentVariable`）。`stop` 不受影响：照样落标记并退回硬停 |
| 登录时闪一下黑窗 | 系统无 `conhost.exe --headless`（< Windows 10 1809）：脚本自动退回 `pwsh -WindowStyle Hidden` 并 Warning |
| 自更新报 `guard(F4)` | 任务模式下 gofer 的父进程仍是 `win-supervisor.ps1`，§2 的默认 `-SupervisorMarker` 即可过 |

### 7.5 从 nssm 迁移（一次性，需管理员）

```powershell
# 1) 管理员窗口：卸掉旧服务（nssm.exe 在 <repo>\serve-run\；没有就用 sc）
cd <repo>\serve-run; .\nssm.exe stop gofer; .\nssm.exe remove gofer confirm
#    sc.exe stop gofer; sc.exe delete gofer
# 2) 普通窗口：注册登录计划任务（自动清掉残留的 gofer.stop）
pwsh -File scripts\start.ps1 -Action up -ConfigDir '<ConfigDir>'
# 3) 验证：任务 Running + SessionId 非 0 + /health 200（首行显示 config dir 来源）
pwsh -File scripts\start.ps1 -Action status
```

> 切到计划任务后自更新**不再需要** `-SupervisorMarker 'nssm'`：gofer 的父进程回到 `win-supervisor.ps1`，§2 的调用即最终形态。
> 切换后 `w-kzl-desktop` 这类"桌面前台 worker"可以关掉：local job 已经在桌面上跑了。
