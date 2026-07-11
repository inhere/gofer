# Windows gofer server 自更新重启（实施计划草案 v0.1）

> bd: h-aii-xu64.11 ｜ 来源：iss-0709 §讨论点 追问
> 状态：**v0.3 复审通过、可开工**（承重代码事实已逐条复验，F2–F6 已折入）。§4 部署形态已确认（前台手动、无 nssm）；仅剩 §10 两项非阻断待确认，可实施中定。

## 修订记录

| 版本 | 日期 | 修改人 | 说明 |
|---|---|---|---|
| v0.1 | 2026-07-09 | inhere/claude | 初稿；§4 确认前台手动后，主推 supervisor loop 方案 |
| v0.2 | 2026-07-09 | inhere/claude | 评审：确认引入 supervisor loop；补 nssm 长远路径(§5.1)；去掉 Windows-only 子命令；确认按精确 pid 杀(主机可能跑 worker) |
| v0.3 | 2026-07-11 | inhere/claude | 复审（代码逐条复验）：修正 §2.2 进程树前提在 Windows 为假(无 Job Object，子进程孤儿存活)——结论不变、理由改；父 pid 精确杀仅 `-a exec` 成立→**自更新限定 `-a exec`**(F2)；supervisor 骨架必须 try/catch 否则坏 exe 杀死 supervisor 自身(F3)；加 supervisor 存在性守卫(F4)；启动参数透传改**强制**(F5)；§3 信号措辞收紧+`$ErrorActionPreference='Stop'`(F6) |

## 1. 目标

在 Windows 主机上，能**通过一个 gofer job**（派 host codex / exec）触发：拉取最新代码 → 重建 `gofer.exe` → 停止并重启 server，让 server 用新二进制运行。人只需提交一个 job + 事后确认 `/health`。

## 2. 关键约束（已核实代码）

1. **运行中的 `.exe` 不能被覆盖**（Windows 文件锁），**但可以被 rename**。→ 用 **rename-replace 技巧**绕过。
2. **进程树 / 输出管道陷阱**（v0.3 修正）：`--runner local` 的 job 是 **gofer server 的子进程**（`internal/runner/local/runner.go:53` 裸 `exec.CommandContext`，**未设 Windows Job Object / 进程组**，全仓仅 `daemon_unix.go` 用 Setsid 且与 job 子进程无关）。
   - ⚠️ 注意：Windows **无 Unix 式进程组级联杀**——硬杀 gofer 后 job 会**孤儿化但继续存活**，并非「随 server 被带走」。所以「同一 job 内先停 server 再启新 server」在 Windows 技术上并非不可能。
   - 但真正拦路的是**输出管道**：gofer 一死，它为 job 转发 stdout 的管道即断——job 进程还活着、写的输出却**无人接收**，客户端看不到重启结果。
   - → 因此仍**必须由处于 server 进程树之外的 supervisor** 负责停启：它给崩溃自愈、干净的控制台/输出归属，避免「新 server 挂在将死的 job 上、stdout 已断」。结论不变，理由从「进程被带走」改为「输出管道断 + 需外部看门狗」。
3. **Windows 无 `-d` 后台化**：`internal/daemon/daemon_windows.go:13` 明确 `"daemon mode (-d) not supported on windows; run as a service"`；`serve stop` 的 Terminate 在 Windows 也不支持（`daemon_windows.go:21`）。→ 不能用 `gofer serve -d` / `gofer serve stop`。
4. **前台 serve 不写 pidfile**（已核实 `internal/commands/serve.go:runServe`）：`serve.pid` 只有 daemon 模式的 `daemon.Spawn` 才写。→ 前台手动起的 server **没有 pidfile**，更新脚本不能靠 `serve.pid` 找进程，需用 `Get-Process gofer` / 端口 / 父进程 pid，或**由监督进程直接持有子 pid**（见 §5）。

## 3. server graceful shutdown 现状

- serve 优雅停机靠 `httpapi.Server.RunCtx`（ctx 取消 → `Shutdown`）；触发源是 `signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)`（`internal/serve/serve.go:276`，v0.3 核实）。Windows 上 `os.Interrupt == SIGINT`（Ctrl-C 可捕获），而 **SIGTERM 在 Windows 永不被 OS 投递**、`Stop-Process` 也不发 SIGTERM——故 Windows 上唯一的优雅入口是 Ctrl-C，本 MVP 不走它（硬杀）。
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

监督循环骨架（示意，真实脚本见 T1；⚠️ 骨架 **必须** 带 try/catch，见 F3）：
```powershell
# supervisor.ps1
$exe  = "$PSScriptRoot\gofer.exe"
$args = @('serve')          # F5: 原样重放原 server 启动参数(--addr/--token/-c/--web-dir)，强制、勿丢
$fails = 0                  # F3+§9: 连续快速失败计数(看门狗)
while ($true) {
  try {
    & $exe @args            # 同控制台运行、阻塞；输出可见
    $fails = 0              # 正常退出(被更新 job 杀)→计数清零
  } catch {
    # F3: 坏 exe / gofer.exe 缺失会抛终止性错误——必须捕获，否则 supervisor 自身被杀死、
    #     崩溃自愈与 §9 看门狗一并失效。把「启动失败」也计入失败计数。
    $fails++
    Write-Warning "gofer 启动失败($fails): $_"
    if ($fails -ge 3 -and (Test-Path "$PSScriptRoot\gofer.old.exe")) {
      Copy-Item "$PSScriptRoot\gofer.old.exe" $exe -Force   # §9 自动回滚
      Write-Warning "连续失败达阈值,已回滚 gofer.old.exe → gofer.exe"
      $fails = 0
    }
  }
  Start-Sleep -Seconds 2    # 退出后短暂间隔再重启（含被更新 job 杀掉的情形）
}
```
> **F5 强制**：`$args` 必须捕获并原样重放原 server 的全部启动参数，否则重启后 `--addr/--token/-c/--web-dir` 丢失、server 起错。这是硬约束，不是「可选透传」。

### 5.1 长远：需要 nssm 吗？怎么结合（v0.2）
- **何时需要**：pwsh supervisor loop 跑在**登录用户的终端**里——关终端 / 用户登出 / 机器重启后**不会自启**。若要**开机自启 / 免登录常驻 / 无人值守**，长远需要**服务化**：nssm（或 `sc.exe` / Task Scheduler "无论是否登录都运行"）。
- **怎么结合**：nssm 本身**就是进程外监督者**（拉起并守护 gofer），角色**等同** supervisor loop——用 nssm 时**不再需要** pwsh loop。
- **关键：更新机制与监督者选择解耦**。更新 job 只做三件事：`build 新 exe → rename-replace → 触发重启`。"触发重启"在两种监督者下只差最后一步：
  - supervisor loop：`Stop-Process`（精确 pid）杀 gofer → loop 2s 后重启。
  - nssm：`nssm restart gofer`（或 stop→start）。
- → **迁移路径干净**：先上 supervisor loop（零依赖、马上可用），将来要常驻自启再换 nssm，**rename-replace 更新逻辑不变**，只替换"触发重启"这一步。建议 T2 脚本把"触发重启"抽成可切换的小函数（`loop-kill` / `nssm-restart`）。

## 6. 更新流程（两阶段，均可在触发 job 内完成）

> **F2 前置约束**：自更新 job **必须走 `-a exec`**（脚本作为 gofer 的**直接子进程**运行）。原因见 §6.1——父 pid 精确杀依赖「job 父进程即 gofer」；`-a codex` 下链路多一层 codex，`$PID` 父 = codex ≠ gofer，会杀错目标。自更新是固定脚本、不需要 agent 智能，限定 exec 最简且鲁棒。

### 阶段1（安全，不碰运行中 exe）
- 脚本首行 `$ErrorActionPreference = 'Stop'`（F6）——任一步失败即中止，**决不带着半成品进入阶段2**。
- `git -C <repo> pull`（失败即 Stop，server 不受影响）
- `go build -o gofer-new.exe ./cmd/gofer`（构建到**新文件名**）
- 校验：`gofer-new.exe -V` 能打印新版本

### 阶段2（rename-replace + 杀 gofer，交监督循环重启）
- **F4 守卫（杀之前）**：确认自己确由 supervisor 拉起——即祖父进程（`gofer` 的父）为 pwsh supervisor。若 server 是裸 `gofer serve` 起的（无 supervisor），**杀完无人重启 → 服务永久 down**；此时应**报错中止**，不执行 rename/kill。
- `Rename-Item gofer.exe gofer.old.exe`（保留回滚点）→ `Rename-Item gofer-new.exe gofer.exe`（先 rename 再杀，避免空窗被拉起旧版，§9）
- 杀掉 gofer：**杀本 job 的父进程（= gofer，精确）**——
  `Stop-Process -Id (Get-CimInstance Win32_Process -Filter "ProcessId=$PID").ParentProcessId -Force`
  （**不**用 `Stop-Process -Name gofer`：主机可能同跑 gofer worker，按名会误杀，§9）
- 监督循环 2s 后重启 → 起 `gofer.exe`（已是新版）
- 结果观测：事后轮询 `GET /health` 200 / 看 supervisor 终端输出 / `gofer -V`

> 注（v0.3 精化）：gofer 被杀后，本 job 进程在 Windows 上其实**孤儿存活**（§2.2），但 gofer 一死其 **stdout 转发管道即断**——job 的后续输出到不了客户端。故"重启完成"仍靠**事后带外轮询**，不在同一 job 内返回；job 在网关侧多半显示为中断/失败，**属正常现象**，runbook 需注明勿误判为真失败。

### 6.1 为何限定 `-a exec`（F2 展开）
进程链在 `-a exec` 下：`supervisor(pwsh) → gofer(server) → powershell(本 job)`，故本 job 的 `ParentProcessId` **恰为 gofer** ✅，且 supervisor 是**祖父**、不会被误杀。
`-a codex` 下链路为 `gofer → codex → powershell`，本 job 父 = **codex**，`Stop-Process $PID.Parent` 杀的是 codex 而非 gofer → 目标错误。→ 故限定 exec；将来若确需 codex，须改为**向上遍历祖先匹配 `gofer.exe` 镜像名**再杀（成本更高，本期不做）。

## 7. 分步实施

- **T1 引入 supervisor**：新增 `tools/gofer/scripts/win-supervisor.ps1`（§5 骨架 + 参数化 exe 路径 + **强制**启动参数透传(F5) + **try/catch 包裹 `& $exe`**(F3) + 看门狗回滚(§9)）。改用它在终端启动 server（替代直接 `gofer serve`）。产出 runbook 说明。
- **T2 更新脚本/任务**：`tools/gofer/scripts/win-selfupdate.ps1` 或一个 md+yaml 任务文件（`gofer job run -f`）**派 `-a exec`**(F2，不派 codex)：首行 `$ErrorActionPreference='Stop'`(F6)；执行 §6 阶段1+2；阶段2 含 **supervisor 存在性守卫**(F4) + 按本 job 父 pid 精确杀；含失败回滚（§9）。参数化 `-RepoDir -ExeDir -HealthUrl`。
- **T3（暂缓/重定位）**：`gofer serve update` **若只服务 Windows 则意义不大**（脚本已够）→ **暂不做 Windows-only 子命令**。仅当做成**跨平台**自更新命令（Linux/Mac 走 daemon re-exec 重启、Windows 走 rename-replace+触发监督者重启）才值得，列为**独立后续增强**，不阻塞本期。本期 MVP 用 T2 的任务文件/脚本。
- **T4 验收 + runbook**：`docs/runbook/` 补"Windows server 监督运行 + 自更新"操作/排障流程。

## 8. 验收

- supervisor 循环下：手动杀 gofer → 2s 内自动重启（崩溃自愈验证）。
- 触发更新 job 后：`gofer.old.exe` 存在（旧版留底）、`gofer.exe` 为新版、进程重启、`GET /health` 200、`gofer -V` 为新版本。
- 进程树验证：更新 job 随 gofer 终止，但 supervisor 存活并重启成功。

## 9. 风险 / 回滚

- **构建失败 / pull 失败**：阶段1 任一步失败（`$ErrorActionPreference='Stop'`，F6）→ 不进入阶段2，server 不受影响（安全）。
- **新 exe 起不来 / gofer.exe 缺失**：supervisor 的 `& $exe` 会抛终止性错误——**必须 try/catch（F3）**，否则 supervisor 自身被杀死、看门狗失效。捕获后计连续快速失败数，超阈值 `Copy gofer.old.exe gofer.exe` 自动回滚并告警（写 `tmp/win-supervisor.log`）。
- **无 supervisor 守卫（F4）**：server 若非由 supervisor 拉起（裸 `gofer serve`），阶段2 硬杀后无人重启 → 服务永久 down。→ 阶段2 杀前校验祖父进程为 supervisor，否则报错中止，不 rename/kill。
- **rename 竞态**：rename-replace 在 gofer 运行时即可做（rename 不受 exe 锁限制）；先 rename 再杀，避免"杀完还没换就被重启拉起旧版"。
- **硬停非优雅**：`Stop-Process` 会中断进行中 job；优雅停机（Ctrl-C）列为后续增强。
- **误杀 worker / codex**：主机可能同跑 gofer worker → **不按 name 杀**，用本 job 父 pid 精确杀（仅 `-a exec` 成立，F2/§6.1）。

## 10. 已定 / 待确认

**已定（v0.2 评审）**：
- ✅ 引入 supervisor loop（`win-supervisor.ps1`）作为目标形态；nssm 作长远常驻/自启选项，更新逻辑与监督者解耦（§5.1）。
- ✅ MVP 走任务文件/脚本（T2）；**不做** Windows-only 子命令（T3 暂缓，除非跨平台）。
- ✅ 主机**可能同时跑 gofer worker** → 杀进程**必须按精确 pid**（本 job 父 pid），不按进程名。

**已定（v0.3 复审新增）**：
- ✅ **F2**：自更新 job **限定 `-a exec`**（脚本作 gofer 直接子进程，父 pid 精确杀才成立）；codex 路径本期不支持。
- ✅ **F3**：supervisor 骨架**必须 try/catch**，启动失败计入看门狗失败数，否则坏 exe 杀死 supervisor 自身。
- ✅ **F4**：阶段2 杀前加 **supervisor 存在性守卫**，无 supervisor 则报错中止。
- ✅ **F5**：启动参数透传为**强制**（原样重放 `--addr/--token/-c/--web-dir`）。
- ✅ **F6**：脚本 `$ErrorActionPreference='Stop'`；§3 信号措辞已按代码（SIGINT+SIGTERM，Windows 仅 Ctrl-C 有效）收紧。

**待确认**：
1. 优雅停机（Ctrl-C 触发 `RunCtx` 退出）本期做，还是先硬停兜底、后续增强？（倾向：先硬停 MVP）
2. 回滚保留几份历史 exe（默认只留 `gofer.old.exe` 一份）？
3. ~~`go build` GOCACHE~~ → **已澄清**：非 GOCACHE 问题，是 **codex 权限**（用户手动 build 正常）；用户已给 host codex 完全访问标志，解决。更新脚本走 codex/exec 时需确保其对仓库/构建缓存有写权限。

## 11. 进度跟踪（本文档即 Windows MVP 实施计划，规模小无需再拆子目录）

- [ ] **T1** `scripts/win-supervisor.ps1`：§5 监督循环 + 参数化（exe 路径 / **强制**重放 serve 启动参数 F5）+ **try/catch 包裹 `& $exe`**(F3) + 看门狗（连续快速失败计数→回滚告警，§9）。改用它在终端启动 server。
- [ ] **T2** `scripts/win-selfupdate.ps1`（或 `gofer job run -f` 任务文件，**派 `-a exec`** F2）：首行 `$ErrorActionPreference='Stop'`(F6)；阶段1 `git pull` + `go build -o gofer-new.exe ./cmd/gofer` + `-V` 校验；阶段2 **先校验 supervisor 存在**(F4) → rename-replace（`gofer.exe`→`gofer.old.exe`→新）+ **按本 job 父 pid 精确杀** gofer（`Get-CimInstance Win32_Process ... ParentProcessId`）→ 监督循环重启 → 轮询 `/health`。"触发重启"抽成可切换函数（`loop-kill` / 预留 `nssm-restart`，§5.1）。
- [ ] **T3** runbook：`docs/runbook/` 补"Windows server 监督运行 + 自更新"操作/排障流程。
- [ ] **T4** 验收（§8）：崩溃自愈 / 更新后新版 running + health 200 + 旧版留底 / 进程树分离（触发 job 随 gofer 亡但 supervisor 存活重启）。

> 待确认（§10）仅剩 2 项非阻断（优雅停机时机 / 历史 exe 保留份数；构建权限项已解决），可在实施中定；已定项（含 v0.3 复审 F2–F6）足以开工。全部承重代码事实已逐条复验（§2/§3 引用行号属实，构建入口 `./cmd/gofer` / `module github.com/inhere/gofer` 正确）。
