# Windows gofer server 自更新重启（实施计划草案 v0.1）

> bd: h-aii-xu64.11 ｜ 来源：iss-0709 §讨论点 追问
> 状态：**草案待审**。§4 部署形态已确认（前台手动、无 nssm）。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-09 | inhere/claude | 初稿；§4 确认前台手动后，主推 supervisor loop 方案 |

## 1. 目标

在 Windows 主机上，能**通过一个 gofer job**（派 host codex / exec）触发：拉取最新代码 → 重建 `gofer.exe` → 停止并重启 server，让 server 用新二进制运行。人只需提交一个 job + 事后确认 `/health`。

## 2. 关键约束（已核实代码）

1. **运行中的 `.exe` 不能被覆盖**（Windows 文件锁），**但可以被 rename**。→ 用 **rename-replace 技巧**绕过。
2. **进程树陷阱**：`--runner local` 的 job 是 **gofer server 的子进程**。让它直接停 server，server 一倒它自己被带走，重启那步永远跑不到。→ 停+启必须由**处于 server 进程树之外**的角色执行。
3. **Windows 无 `-d` 后台化**：`internal/daemon/daemon_windows.go:13` 明确 `"daemon mode (-d) not supported on windows; run as a service"`；`serve stop` 的 Terminate 在 Windows 也不支持（`daemon_windows.go:21`）。→ 不能用 `gofer serve -d` / `gofer serve stop`。
4. **前台 serve 不写 pidfile**（已核实 `internal/commands/serve.go:runServe`）：`serve.pid` 只有 daemon 模式的 `daemon.Spawn` 才写。→ 前台手动起的 server **没有 pidfile**，更新脚本不能靠 `serve.pid` 找进程，需用 `Get-Process gofer` / 端口 / 父进程 pid，或**由监督进程直接持有子 pid**（见 §5）。

## 3. server graceful shutdown 现状

- serve 优雅停机靠 `httpapi.Server.RunCtx`（ctx 取消 → `Shutdown`），Windows 上由捕获 `os.Interrupt`(Ctrl-C) 触发。
- 从**另一个进程**给前台 gofer 发 Ctrl-C 在 Windows 上很别扭（`GenerateConsoleCtrlEvent` 要 attach 到目标控制台）。→ MVP 用 `Stop-Process`（硬停，进行中 job 会断）作兜底并记录；优雅停机作后续增强。

## 4. 部署形态（已确认）

**Windows 主机当前：前台终端手动 `gofer serve`，无 nssm、无服务、无 pidfile。** → 没有服务管理器可 `Stop-Service/Start-Service`，也没有守护进程做崩溃自愈。

## 5. 推荐方案：极简 pwsh 监督循环（supervisor loop）

**核心洞察**：给 gofer 套一层**监督循环**——它 spawn gofer 并等待，gofer 退出就重启。这个监督进程是 gofer 的**父进程**，**处于 gofer 进程树之外**，因此：

- gofer（连同其下所有 local job）被杀时，**监督进程存活**，自动重启新二进制。
- → **更新 job 只需做 rename-replace + 杀掉 gofer 即可**，重启交给监督循环，**无需再单独写 detached swapper**（§2/§3 的进程树/停启难题一并化解）。
- 顺带白送**崩溃自愈**（前台手动裸跑现在没有）。
- 仍是"在我的终端里跑"——只是终端里跑的是 supervisor（转发 gofer 输出），不是 gofer 本体。

```txt
终端: supervisor.ps1  ── spawn+wait ──▶ gofer.exe serve ── spawn ──▶ local job (codex/exec)
        ▲  (gofer 的父，进程树之外)                                        │
        └──────────── gofer 退出即重启新 exe ◀── 更新 job: rename-replace + Stop gofer
```

监督循环骨架（示意，真实脚本见 T2）：
```powershell
# supervisor.ps1
$exe = "$PSScriptRoot\gofer.exe"
while ($true) {
  & $exe serve            # 同控制台运行、阻塞；输出可见
  Start-Sleep -Seconds 2  # 退出后短暂间隔再重启（含被更新 job 杀掉的情形）
}
```

## 6. 更新流程（两阶段，均可在触发 job 内完成）

### 阶段1（安全，不碰运行中 exe）
- `git -C <repo> pull`
- `go build -o gofer-new.exe ./cmd/gofer`（构建到**新文件名**）
- 校验：`gofer-new.exe -V` 能打印新版本

### 阶段2（rename-replace + 杀 gofer，交监督循环重启）
- `Rename-Item gofer.exe gofer.old.exe`（保留回滚点）→ `Rename-Item gofer-new.exe gofer.exe`
- 杀掉 gofer：优先"杀本 job 的父进程"（精确）——
  `Stop-Process -Id (Get-CimInstance Win32_Process -Filter "ProcessId=$PID").ParentProcessId -Force`
  兜底 `Stop-Process -Name gofer -Force`（注意主机若同跑 worker 需按 pid 精确杀，勿误杀）
- 触发 job **随 gofer 一起终止**（可接受）；监督循环 2s 后重启 → 起 `gofer.exe`（已是新版）
- 结果观测：事后轮询 `GET /health` 200 / 看 supervisor 终端输出 / `gofer -V`

> 注：触发 job 在 gofer 被杀时即结束（正常现象），"重启完成"靠事后轮询，不在同一 job 内返回。

## 7. 分步实施

- **T1 引入 supervisor**：新增 `tools/gofer/scripts/win-supervisor.ps1`（§5 骨架 + 参数化 exe 路径 + 可选启动参数透传 + 循环退出保护）。改用它在终端启动 server（替代直接 `gofer serve`）。产出 runbook 说明。
- **T2 更新脚本/任务**：`tools/gofer/scripts/win-selfupdate.ps1` 或一个 md+yaml 任务文件（`gofer job run -f`）派 exec/codex：执行 §6 阶段1+2；含失败回滚（§9）。参数化 `-RepoDir -ExeDir -HealthUrl`。
- **T3（可选收敛）**：新增 `gofer serve update` 子命令，内部封装 build 新 exe + rename-replace + 自杀（依赖监督循环重启）。Windows 分支实现；非 Windows 复用既有 daemon 重启。
- **T4 验收 + runbook**：`docs/runbook/` 补"Windows server 监督运行 + 自更新"操作/排障流程。

## 8. 验收

- supervisor 循环下：手动杀 gofer → 2s 内自动重启（崩溃自愈验证）。
- 触发更新 job 后：`gofer.old.exe` 存在（旧版留底）、`gofer.exe` 为新版、进程重启、`GET /health` 200、`gofer -V` 为新版本。
- 进程树验证：更新 job 随 gofer 终止，但 supervisor 存活并重启成功。

## 9. 风险 / 回滚

- **构建失败**：阶段1 build 失败 → 不进入阶段2，server 不受影响（安全）。
- **新 exe 起不来**：supervisor 会不断重启一个坏 exe → 需**看门狗**：supervisor 记录连续快速失败次数，超阈值则 `Copy gofer.old.exe gofer.exe` 自动回滚并告警（写 `tmp/win-supervisor.log`）。
- **rename 竞态**：rename-replace 在 gofer 运行时即可做（rename 不受 exe 锁限制）；先 rename 再杀，避免"杀完还没换就被重启拉起旧版"。
- **硬停非优雅**：`Stop-Process` 会中断进行中 job；优雅停机（Ctrl-C）列为后续增强。
- **误杀 worker**：若主机同跑 gofer worker，按 name 杀会误伤 → 用父 pid 精确杀。

## 10. 待确认

1. 是否同意**引入 supervisor loop**（改用 `win-supervisor.ps1` 启动，替代裸 `gofer serve`）作为目标形态？它解决进程树 + 送崩溃自愈。
2. MVP 先落**任务文件派单**（T2），还是直接做 `gofer serve update` 子命令（T3）？
3. 优雅停机（Ctrl-C 触发 `RunCtx` 退出）是否要在本期做，还是先硬停兜底、后续增强？
4. 主机是否**同时**跑 gofer worker？（影响杀进程策略：必须按 pid 精确杀）
5. 回滚保留几份历史 exe（默认只留 `gofer.old.exe` 一份）？
6. 构建入口 `./cmd/gofer` 已核实正确；`go build` 在主机的 GOCACHE/耗时是否需要预热考虑。
