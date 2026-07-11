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

在独立端口 + 独立配置目录起第二实例，验证 supervisor+selfupdate 全流程，自清理：

```powershell
pwsh -NoProfile -File scripts/win-selftest.ps1        # 期望 RESULT: pass=13 fail=0
```

覆盖：崩溃自愈 / rename-replace 换新 exe / 看门狗回滚 / F2 守卫拒绝非 gofer 父进程。

## 6. 排障

| 现象 | 原因 / 处理 |
|---|---|
| `guard(F2): parent is '...'` | 自更新没作为 gofer 直接子进程跑 → 用 `-a exec`，勿经 codex/shell |
| `guard(F4): ... not the supervisor` | server 是裸跑（无监督器）→ 先按 §1 用监督循环起，或用 `-SupervisorMarker` 对齐 |
| 重启后 `--web-dir` 等相对参数失效 | `-WorkDir` 未对齐原启动 cwd → 传正确 `<WorkDir>` 或改用绝对路径参数 |
| 监督器不断重启坏 exe | 达阈值会自动回滚；若无 `gofer.old.exe`，手动放回一份可用 exe |
| 主机同时跑 gofer worker | 自更新按**父 pid**精确杀，不按进程名，不会误伤 worker |

## 7. 长远：常驻 / 开机自启（可选）

监督循环跑在登录用户的终端里，**关终端 / 登出 / 重启不自启**。若需无人值守常驻，改用服务化（nssm / Task Scheduler「无论是否登录都运行」）：nssm 本身即进程外监督者，**替代**本 pwsh 循环，此时把 §2 的「杀 gofer」换成 `nssm restart gofer` 即可，**rename-replace 更新逻辑不变**。
